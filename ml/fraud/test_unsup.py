"""
ml/fraud/test_unsup.py
=======================
Testes dos modelos nao-supervisionados de IVT (K2 / Fase 3).

Cobre:
  1. train_unsup.run(): treino IF + AE sobre sample sintetico
  2. Artefatos gerados (ONNX do IF, pesos JSON do AE, meta JSON)
  3. IVTUnsupScorer: fail-open quando modelos nao carregados
  4. IVTUnsupScorer: scoring deterministico com modelos carregados
  5. combine_ivt_scores: logica OR (supervisionado OR nao-supervisionado)
  6. PII-free: IVTScoringInput nao contem campos PII (TX-5/DA-11)
  7. TX-6: scorer nao-supervisionado esta fora do hot path
  8. Nomes de modelo distintos de 'ivt_classifier' e 'pctr_lgb'
  9. Forward pass numpy do autoencoder: reproducibilidade
  10. combine_ivt_scores: fail-open bilateral -> False (nao bloquear trafego)
"""
from __future__ import annotations

import json
import os
import sys
import tempfile
from pathlib import Path

import numpy as np
import pytest

_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))
os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

from ml.fraud.unsup_scorer import (
    IVTUnsupScorer,
    IVTUnsupScore,
    combine_ivt_scores,
    _ae_forward,
    _make_fail_open_unsup,
)
from ml.fraud.scorer import IVTScore, IVTScoringInput


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_sup_score(is_ivt: bool = False, fail_open: bool = False) -> IVTScore:
    return IVTScore(
        is_ivt=is_ivt,
        ivt_prob=0.9 if is_ivt else 0.1,
        ivt_label=1 if is_ivt else 0,
        model_version="fail_open" if fail_open else "1",
        scored_at_utc="",
    )


def _make_scoring_input(**kwargs) -> IVTScoringInput:
    defaults = dict(
        device_class="chrome-mobile",
        hour_of_day=14,
        day_of_week=2,
        geo_country="BR",
        geo_city="Sao Paulo",
        hourly_requests=500,
        hourly_impressions=400,
        hourly_clicks=10,
        recent_hourly_imps=[400] * 24,
    )
    defaults.update(kwargs)
    return IVTScoringInput(**defaults)


# ---------------------------------------------------------------------------
# 1. Treino e artefatos
# ---------------------------------------------------------------------------

class TestTrainUnsup:

    def test_train_produces_artifacts(self):
        """
        Treino gera artefatos: IF ONNX + AE pesos JSON + AE meta JSON.
        """
        from ml.fraud.train_unsup import run as train_run

        with tempfile.TemporaryDirectory() as tmpdir:
            mlflow_uri = f"file:{tmpdir}/mlruns"
            artifacts_dir = Path(tmpdir) / "artifacts"

            result = train_run(
                data_path=None,         # sample sintetico
                n_estimators_if=30,     # rapido para CI
                ae_max_iter=50,
                ae_hidden=4,
                seed=42,
                val_fraction=0.2,
                mlflow_tracking_uri=mlflow_uri,
                run_name="test_unsup_ci",
                artifacts_dir=artifacts_dir,
            )

            assert result["run_id"], "run_id deve ser nao-vazio"

            # Artefatos criados
            assert Path(result["if_onnx_path"]).exists(), "IF ONNX deve existir"
            assert Path(result["ae_weights_path"]).exists(), "AE pesos JSON deve existir"
            assert Path(result["ae_meta_path"]).exists(), "AE meta JSON deve existir"

            # Metricas retornadas (nao exige limiar — sample sintetico)
            assert "if_f1" in result["metrics"]
            assert "ae_f1" in result["metrics"]
            assert "combined_f1" in result["metrics"]

            # Nomes distintos
            assert result["if_model_name"] == "ivt_unsup_isolation_forest"
            assert result["ae_model_name"] == "ivt_unsup_autoencoder"
            assert result["if_model_name"] != "ivt_classifier"
            assert result["if_model_name"] != "pctr_lgb"

    def test_ae_weights_json_structure(self):
        """
        Pesos do AE exportados em JSON tem a estrutura correta.
        """
        from ml.fraud.train_unsup import run as train_run, IVT_VECTOR_LENGTH

        with tempfile.TemporaryDirectory() as tmpdir:
            mlflow_uri = f"file:{tmpdir}/mlruns"
            artifacts_dir = Path(tmpdir) / "artifacts"

            result = train_run(
                data_path=None,
                n_estimators_if=10,
                ae_max_iter=30,
                ae_hidden=4,
                seed=42,
                mlflow_tracking_uri=mlflow_uri,
                artifacts_dir=artifacts_dir,
            )

            with open(result["ae_weights_path"]) as f:
                weights = json.load(f)

            assert "scaler_mean" in weights
            assert "scaler_scale" in weights
            assert "weights" in weights
            assert "biases" in weights
            assert "activation" in weights
            assert "n_features" in weights
            assert weights["n_features"] == IVT_VECTOR_LENGTH
            assert len(weights["scaler_mean"]) == IVT_VECTOR_LENGTH
            assert len(weights["scaler_scale"]) == IVT_VECTOR_LENGTH

    def test_ae_meta_json_has_threshold(self):
        """Meta JSON do AE contem o threshold."""
        from ml.fraud.train_unsup import run as train_run

        with tempfile.TemporaryDirectory() as tmpdir:
            mlflow_uri = f"file:{tmpdir}/mlruns"
            artifacts_dir = Path(tmpdir) / "artifacts"

            result = train_run(
                data_path=None,
                n_estimators_if=10,
                ae_max_iter=30,
                ae_hidden=4,
                seed=42,
                mlflow_tracking_uri=mlflow_uri,
                artifacts_dir=artifacts_dir,
            )

            with open(result["ae_meta_path"]) as f:
                meta = json.load(f)

            assert "ae_threshold" in meta
            assert isinstance(meta["ae_threshold"], float)
            assert meta["ae_threshold"] > 0.0
            assert meta["combination_policy"] == "OR_over_thresholds"
            assert meta["gnn_status"] == "future_work"


# ---------------------------------------------------------------------------
# 2. IVTUnsupScorer: fail-open
# ---------------------------------------------------------------------------

class TestIVTUnsupScorerFailOpen:

    def test_fail_open_when_not_loaded(self):
        """
        Scorer sem modelos carregados retorna is_unsup_ivt=False (fail-open, TX-6).
        """
        scorer = IVTUnsupScorer()  # sem caminhos de artefatos
        inp = _make_scoring_input()
        result = scorer.score_event(inp)

        assert result.is_unsup_ivt is False, "Fail-open deve retornar is_unsup_ivt=False"
        assert result.model_version == "fail_open"

    def test_is_loaded_false_before_load(self):
        scorer = IVTUnsupScorer()
        assert scorer.is_loaded is False

    def test_fail_open_score_fields(self):
        """IVTUnsupScore de fail-open tem todos os campos corretos."""
        result = _make_fail_open_unsup()
        assert hasattr(result, "is_unsup_ivt")
        assert hasattr(result, "if_label")
        assert hasattr(result, "if_score")
        assert hasattr(result, "ae_error")
        assert hasattr(result, "ae_threshold")
        assert hasattr(result, "model_version")
        assert hasattr(result, "scored_at_utc")
        assert result.is_unsup_ivt is False
        assert result.model_version == "fail_open"

    def test_batch_fail_open(self):
        """score_batch em fail-open retorna um resultado por entrada."""
        scorer = IVTUnsupScorer()
        inputs = [_make_scoring_input(hour_of_day=i) for i in range(4)]
        results = scorer.score_batch(inputs)
        assert len(results) == 4
        for r in results:
            assert r.is_unsup_ivt is False

    def test_fail_open_deterministic(self):
        """Fail-open retorna resultado consistente para mesma entrada."""
        scorer = IVTUnsupScorer()
        inp = _make_scoring_input()
        r1 = scorer.score_event(inp)
        r2 = scorer.score_event(inp)
        assert r1.is_unsup_ivt == r2.is_unsup_ivt
        assert r1.if_label == r2.if_label


# ---------------------------------------------------------------------------
# 3. IVTUnsupScorer: scoring com modelos reais
# ---------------------------------------------------------------------------

class TestIVTUnsupScorerWithModels:

    def _train_and_load_scorer(self, tmpdir: Path) -> IVTUnsupScorer:
        from ml.fraud.train_unsup import run as train_run

        result = train_run(
            data_path=None,
            n_estimators_if=20,
            ae_max_iter=50,
            ae_hidden=4,
            seed=42,
            mlflow_tracking_uri=f"file:{tmpdir}/mlruns",
            artifacts_dir=tmpdir / "artifacts",
        )
        scorer = IVTUnsupScorer(
            if_onnx_path=Path(result["if_onnx_path"]),
            ae_weights_path=Path(result["ae_weights_path"]),
            ae_meta_path=Path(result["ae_meta_path"]),
            model_version=result["run_id"],
        )
        scorer.load()
        return scorer

    def test_scorer_loaded_after_load(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            scorer = self._train_and_load_scorer(Path(tmpdir))
            assert scorer.is_loaded

    def test_score_deterministic(self):
        """Mesma entrada -> mesmo resultado (deterministico)."""
        with tempfile.TemporaryDirectory() as tmpdir:
            scorer = self._train_and_load_scorer(Path(tmpdir))
            inp = _make_scoring_input()
            r1 = scorer.score_event(inp)
            r2 = scorer.score_event(inp)
            assert r1.is_unsup_ivt == r2.is_unsup_ivt
            assert r1.if_label == r2.if_label
            assert r1.if_score == r2.if_score
            assert r1.ae_error == r2.ae_error

    def test_score_output_fields(self):
        """IVTUnsupScore tem todos os campos necessarios."""
        with tempfile.TemporaryDirectory() as tmpdir:
            scorer = self._train_and_load_scorer(Path(tmpdir))
            inp = _make_scoring_input()
            result = scorer.score_event(inp)

            assert isinstance(result.is_unsup_ivt, bool)
            assert isinstance(result.if_label, int)
            assert isinstance(result.if_score, float)
            assert isinstance(result.ae_error, float)
            assert isinstance(result.ae_threshold, float)
            assert isinstance(result.model_version, str)
            assert isinstance(result.scored_at_utc, str)

    def test_bot_event_tends_to_be_anomaly(self):
        """
        Evento bot com volume anormal tende a ser detectado como anomalia.
        Nao e garantia (modelo nao-supervisionado sem label), mas deve ocorrer
        com frequencia dado o sample sintetico.
        """
        with tempfile.TemporaryDirectory() as tmpdir:
            scorer = self._train_and_load_scorer(Path(tmpdir))

            # Evento extremamente anomalo: bot, volume altissimo, zero impressoes
            bot_inp = _make_scoring_input(
                device_class="bot",
                hourly_requests=50000,
                hourly_impressions=10,
                hourly_clicks=0,
                recent_hourly_imps=[400] * 24,
            )
            # Evento normal
            normal_inp = _make_scoring_input(
                device_class="chrome-mobile",
                hourly_requests=500,
                hourly_impressions=450,
                hourly_clicks=20,
                recent_hourly_imps=[450] * 24,
            )

            bot_score    = scorer.score_event(bot_inp)
            normal_score = scorer.score_event(normal_inp)

            # O AE deve ter erro maior para o evento anomalo
            assert bot_score.ae_error >= normal_score.ae_error, (
                f"Evento bot deve ter AE error >= normal: "
                f"bot={bot_score.ae_error:.4f}, normal={normal_score.ae_error:.4f}"
            )

    def test_pii_free_input(self):
        """
        IVTScoringInput nao tem campos PII (TX-5/DA-11).
        Scorer nao-supervisionado reutiliza a mesma entrada PII-free do supervisionado.
        """
        import dataclasses
        field_names = {f.name for f in dataclasses.fields(IVTScoringInput)}
        for forbidden in ("ip_address", "user_id", "cookie", "user_agent", "email"):
            assert forbidden not in field_names, (
                f"Campo PII '{forbidden}' encontrado em IVTScoringInput (TX-5/DA-11)"
            )


# ---------------------------------------------------------------------------
# 4. combine_ivt_scores: logica OR
# ---------------------------------------------------------------------------

class TestCombineIVTScores:

    def test_both_clean_returns_false(self):
        """Supervisionado clean + nao-supervisionado clean -> nao e IVT."""
        sup   = _make_sup_score(is_ivt=False)
        unsup = IVTUnsupScore(
            is_unsup_ivt=False, if_label=1, if_score=0.1,
            ae_error=0.01, ae_threshold=1.0,
            model_version="1", scored_at_utc="",
        )
        assert combine_ivt_scores(sup, unsup) is False

    def test_supervised_ivt_returns_true(self):
        """Supervisionado detecta IVT -> combinado e IVT."""
        sup   = _make_sup_score(is_ivt=True)
        unsup = IVTUnsupScore(
            is_unsup_ivt=False, if_label=1, if_score=0.1,
            ae_error=0.01, ae_threshold=1.0,
            model_version="1", scored_at_utc="",
        )
        assert combine_ivt_scores(sup, unsup) is True

    def test_unsupervised_ivt_returns_true(self):
        """Nao-supervisionado detecta anomalia -> combinado e IVT."""
        sup   = _make_sup_score(is_ivt=False)
        unsup = IVTUnsupScore(
            is_unsup_ivt=True, if_label=-1, if_score=-0.3,
            ae_error=2.0, ae_threshold=0.5,
            model_version="1", scored_at_utc="",
        )
        assert combine_ivt_scores(sup, unsup) is True

    def test_both_ivt_returns_true(self):
        """Ambos detectam IVT -> combinado e IVT."""
        sup   = _make_sup_score(is_ivt=True)
        unsup = IVTUnsupScore(
            is_unsup_ivt=True, if_label=-1, if_score=-0.3,
            ae_error=2.0, ae_threshold=0.5,
            model_version="1", scored_at_utc="",
        )
        assert combine_ivt_scores(sup, unsup) is True

    def test_both_fail_open_returns_false(self):
        """
        Ambos os scorers em fail-open -> nao bloquear trafego (TX-6).
        is_ivt=False, is_unsup_ivt=False -> combine = False.
        """
        sup   = _make_sup_score(is_ivt=False, fail_open=True)
        unsup = _make_fail_open_unsup()
        assert combine_ivt_scores(sup, unsup) is False, (
            "Fail-open bilateral nao deve bloquear trafego (TX-6)"
        )

    def test_supervised_fail_open_unsup_anomaly(self):
        """
        Supervisionado em fail-open + nao-supervisionado detecta anomalia -> IVT.
        O nao-supervisionado cobre o gap do supervisionado em fail-open.
        """
        sup   = _make_sup_score(is_ivt=False, fail_open=True)
        unsup = IVTUnsupScore(
            is_unsup_ivt=True, if_label=-1, if_score=-0.3,
            ae_error=2.0, ae_threshold=0.5,
            model_version="1", scored_at_utc="",
        )
        assert combine_ivt_scores(sup, unsup) is True


# ---------------------------------------------------------------------------
# 5. Forward pass numpy do autoencoder
# ---------------------------------------------------------------------------

class TestAutoencoderForwardPass:

    def test_forward_pass_shape(self):
        """Forward pass numpy retorna shape correto."""
        n_feat = 10
        hidden = 4

        # Pesos aleatorios (simula pesos treinados)
        rng = np.random.default_rng(42)
        W1 = rng.normal(size=(n_feat, hidden))
        b1 = rng.normal(size=(hidden,))
        W2 = rng.normal(size=(hidden, n_feat))
        b2 = rng.normal(size=(n_feat,))

        x = rng.normal(size=(n_feat,)).astype(np.float64)
        x_rec = _ae_forward(x, weights=[W1, W2], biases=[b1, b2], activation="relu")
        assert x_rec.shape == (n_feat,), f"Shape esperado ({n_feat},), got {x_rec.shape}"

    def test_forward_pass_reproducible(self):
        """Forward pass e deterministico para mesma entrada."""
        rng = np.random.default_rng(1)
        W1 = rng.normal(size=(10, 4))
        b1 = rng.normal(size=(4,))
        W2 = rng.normal(size=(4, 10))
        b2 = rng.normal(size=(10,))

        x = rng.normal(size=(10,)).astype(np.float64)
        r1 = _ae_forward(x, [W1, W2], [b1, b2])
        r2 = _ae_forward(x, [W1, W2], [b1, b2])
        np.testing.assert_array_equal(r1, r2)

    def test_forward_pass_relu_no_negative_in_hidden(self):
        """Relu nao produz valores negativos na camada oculta."""
        rng = np.random.default_rng(2)
        W1 = rng.normal(size=(10, 4))
        b1 = rng.normal(size=(4,))
        W2 = rng.normal(size=(4, 10))
        b2 = rng.normal(size=(10,))

        x = rng.normal(size=(10,)).astype(np.float64)
        # Verifica que a ativacao relu foi aplicada na camada oculta
        h = x @ np.array(W1) + np.array(b1)
        h_relu = np.maximum(0, h)
        x_rec = _ae_forward(x, [W1, W2], [b1, b2], activation="relu")
        expected_rec = h_relu @ np.array(W2) + np.array(b2)
        np.testing.assert_allclose(x_rec, expected_rec, rtol=1e-10)

    def test_ae_numpy_matches_sklearn_prediction(self):
        """
        Forward pass numpy deve reproduzir as predicoes do MLPRegressor sklearn
        (mesmos pesos, mesma algebra).
        """
        import warnings
        warnings.filterwarnings("ignore")

        from sklearn.neural_network import MLPRegressor
        from sklearn.preprocessing import StandardScaler

        rng = np.random.default_rng(7)
        X = rng.normal(size=(100, 10)).astype(np.float32)

        scaler = StandardScaler()
        X_scaled = scaler.fit_transform(X)

        ae = MLPRegressor(
            hidden_layer_sizes=(4,),
            activation="relu",
            max_iter=20,
            random_state=42,
        )
        ae.fit(X_scaled, X_scaled)

        # Predicao sklearn
        x_sample = X_scaled[:5]
        sk_pred = ae.predict(x_sample)

        # Predicao numpy
        for i, x in enumerate(x_sample):
            np_pred = _ae_forward(x, ae.coefs_, ae.intercepts_, activation="relu")
            np.testing.assert_allclose(
                sk_pred[i], np_pred, rtol=1e-5, atol=1e-6,
                err_msg=f"Discrepancia no forward pass numpy vs sklearn para sample {i}",
            )


# ---------------------------------------------------------------------------
# 6. TX-6: scorer nao-supervisionado esta fora do hot path (latencia aceitavel)
# ---------------------------------------------------------------------------

class TestTX6UnsupOutsideHotPath:

    def test_unsup_scorer_not_in_hot_path(self):
        """
        Verifica que IVTUnsupScorer nao importa modulos do hot path de decisao
        (services/, internal/). TX-6: scoring na ingestao, nunca na decisao.
        """
        # O scorer deve importar apenas ml.fraud.*, ml.features.*
        # Nunca importar de services/ ou internal/ (hot path)
        import ml.fraud.unsup_scorer as unsup_mod
        source_file = unsup_mod.__file__
        with open(source_file) as f:
            content = f.read()

        # Nenhum import de services/ ou internal/ (hot path de decisao)
        assert "from services." not in content, (
            "unsup_scorer nao deve importar de services/ (hot path)"
        )
        assert "from internal." not in content, (
            "unsup_scorer nao deve importar de internal/ (hot path)"
        )

    def test_scorer_latency_acceptable_in_failopen(self):
        """
        Latencia do scorer em fail-open e << 1ms (sem modelo carregado).
        TX-6: scorer near-line, nao no hot path, mas deve ser rapido.
        """
        import time

        scorer = IVTUnsupScorer()
        inp = _make_scoring_input()

        # Mede latencia de 100 chamadas em fail-open
        t0 = time.perf_counter()
        for _ in range(100):
            scorer.score_event(inp)
        elapsed_ms = (time.perf_counter() - t0) * 1000

        # 100 chamadas em fail-open devem terminar em < 100ms (1ms/call)
        assert elapsed_ms < 100, (
            f"100 chamadas em fail-open levaram {elapsed_ms:.1f}ms (esperado < 100ms)"
        )
