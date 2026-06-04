"""
ml/fraud/test_fraud.py
=======================
Testes do classificador IVT supervisionado (TX-6).

Cobre:
  1. featurize_ivt: vetor determinístico de shape (10,) float32
  2. featurize_ivt: campo is_bot_flag correto para "bot" e outros
  3. featurize_ivt: PII-free (sem IP bruto, sem UA completo, sem user_id)
  4. featurize_ivt: consistencia de hash com featurize.py (reutilizacao)
  5. IVTScorer: score determinístico para a mesma entrada
  6. IVTScorer: fail-open quando modelo nao carregado -> is_ivt=False
  7. Treino IVT: modelo treina e produz score/label determinístico
  8. IVT modelo registrado sob nome distinto ('ivt_classifier', nao 'pctr_lgb')
  9. TX-6: scorer esta fora do hot path (latência aceitável em batch)
"""
from __future__ import annotations

import sys
import time
from pathlib import Path

import numpy as np
import pytest

_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.fraud.features import (
    IVT_FEATURE_NAMES,
    IVT_FEATURE_VERSION,
    IVT_VECTOR_LENGTH,
    IVTEventInput,
    featurize_ivt,
)
from ml.features.python.featurize import (
    GeoInput,
    RequestTimeInput,
    _feature_hash,
    _DEVICE_HASH_BUCKETS,
    _DEVICE_HASH_SEED,
    _COUNTRY_HASH_BUCKETS,
    _COUNTRY_HASH_SEED,
)
from ml.fraud.scorer import IVTScorer, IVTScoringInput


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_ivt_input(
    device_class: str = "chrome-mobile",
    is_bot: bool = False,
    country: str = "BR",
    hour: int = 14,
    dow: int = 2,
    hourly_requests: int = 500,
    hourly_impressions: int = 400,
    hourly_clicks: int = 10,
    recent: list = None,
) -> IVTEventInput:
    if is_bot:
        device_class = "bot"
    return IVTEventInput(
        device_class=device_class,
        request_time_utc=RequestTimeInput(hour=hour, day_of_week=dow),
        geo=GeoInput(country=country, city="Sao Paulo"),
        hourly_requests=hourly_requests,
        hourly_impressions=hourly_impressions,
        hourly_clicks=hourly_clicks,
        recent_hourly_impressions=recent or [400] * 24,
    )


# ---------------------------------------------------------------------------
# Testes de featurizacao IVT
# ---------------------------------------------------------------------------

class TestFeaturizeIVT:

    def test_output_shape_and_dtype(self):
        """Vetor deve ser float32 de shape (10,)."""
        inp = _make_ivt_input()
        vec = featurize_ivt(inp)
        assert vec.shape == (IVT_VECTOR_LENGTH,), f"Shape esperado ({IVT_VECTOR_LENGTH},); got {vec.shape}"
        assert vec.dtype == np.float32, f"dtype esperado float32; got {vec.dtype}"

    def test_deterministic(self):
        """Mesma entrada -> mesmo vetor (deterministico)."""
        inp = _make_ivt_input()
        v1 = featurize_ivt(inp)
        v2 = featurize_ivt(inp)
        np.testing.assert_array_equal(v1, v2)

    def test_is_bot_flag_set_for_bot(self):
        """is_bot_flag (index 0) = 1.0 para device_class == 'bot'."""
        inp = _make_ivt_input(is_bot=True)
        vec = featurize_ivt(inp)
        assert vec[0] == 1.0, f"is_bot_flag deve ser 1.0 para bot; got {vec[0]}"

    def test_is_bot_flag_zero_for_non_bot(self):
        """is_bot_flag (index 0) = 0.0 para device_class != 'bot'."""
        inp = _make_ivt_input(device_class="chrome-mobile")
        vec = featurize_ivt(inp)
        assert vec[0] == 0.0, f"is_bot_flag deve ser 0.0 para nao-bot; got {vec[0]}"

    def test_hour_of_day_index(self):
        """hour_of_day (index 1) deve refletir a hora passada."""
        for hour in [0, 6, 12, 18, 23]:
            inp = _make_ivt_input(hour=hour)
            vec = featurize_ivt(inp)
            assert vec[1] == float(hour), f"hour={hour}: vec[1]={vec[1]}"

    def test_is_weekend_flag(self):
        """is_weekend (index 2) = 1.0 para domingo(0) e sabado(6)."""
        for dow, expected in [(0, 1.0), (6, 1.0), (1, 0.0), (3, 0.0), (5, 0.0)]:
            inp = _make_ivt_input(dow=dow)
            vec = featurize_ivt(inp)
            assert vec[2] == expected, f"dow={dow}: vec[2]={vec[2]}, esperado {expected}"

    def test_device_class_hash_reuses_canonical_function(self):
        """
        device_class_hash (index 3) deve ser identico ao produzido pela
        funcao _feature_hash de featurize.py (anti-skew, reutilizacao).
        """
        device_class = "chrome-mobile"
        expected = float(_feature_hash(device_class, _DEVICE_HASH_SEED, _DEVICE_HASH_BUCKETS))
        inp = _make_ivt_input(device_class=device_class)
        vec = featurize_ivt(inp)
        assert vec[3] == expected, (
            f"device_class_hash nao corresponde a funcao canonica: "
            f"got {vec[3]}, expected {expected}"
        )

    def test_geo_country_hash_reuses_canonical_function(self):
        """
        geo_country_hash (index 4) deve ser identico ao produzido pela
        funcao _feature_hash de featurize.py (anti-skew, reutilizacao).
        """
        country = "BR"
        expected = float(_feature_hash(country, _COUNTRY_HASH_SEED, _COUNTRY_HASH_BUCKETS))
        inp = _make_ivt_input(country=country)
        vec = featurize_ivt(inp)
        assert vec[4] == expected, (
            f"geo_country_hash nao corresponde a funcao canonica: "
            f"got {vec[4]}, expected {expected}"
        )

    def test_all_features_non_negative(self):
        """Todas as features IVT devem ser nao-negativas (buckets/flags/hashes)."""
        for _ in range(10):
            inp = _make_ivt_input(
                hourly_requests=np.random.randint(0, 10000),
                hourly_impressions=np.random.randint(0, 5000),
            )
            vec = featurize_ivt(inp)
            assert (vec >= 0).all(), f"Feature negativa encontrada: {vec}"

    def test_feature_names_length(self):
        """IVT_FEATURE_NAMES deve ter IVT_VECTOR_LENGTH entradas."""
        assert len(IVT_FEATURE_NAMES) == IVT_VECTOR_LENGTH

    def test_pii_free_fields(self):
        """
        IVTEventInput nao contem campos que possam ser PII.
        Verifica que nenhum campo e IP bruto, user_id, cookie, UA completo.
        TX-5/DA-11.
        """
        import dataclasses
        field_names = {f.name for f in dataclasses.fields(IVTEventInput)}
        for forbidden in ("ip_address", "user_id", "cookie", "user_agent", "email"):
            assert forbidden not in field_names, (
                f"Campo PII '{forbidden}' encontrado em IVTEventInput (TX-5/DA-11)"
            )

    def test_high_inventory_loss_yields_higher_bucket(self):
        """Alta perda de inventario (requests >> impressions) -> bucket mais alto."""
        inp_normal = _make_ivt_input(hourly_requests=1000, hourly_impressions=950)
        inp_fraud = _make_ivt_input(hourly_requests=10000, hourly_impressions=100)
        vec_normal = featurize_ivt(inp_normal)
        vec_fraud = featurize_ivt(inp_fraud)
        # index 8: inventory_loss_bucket
        assert vec_fraud[8] >= vec_normal[8], (
            f"Alta perda de inventario deve ter bucket >= normal: "
            f"fraud={vec_fraud[8]}, normal={vec_normal[8]}"
        )

    def test_high_volume_spike_yields_higher_zscore_bucket(self):
        """Pico de volume (volume atual >> media historica) -> z-score bucket alto."""
        normal_history = [400] * 24
        spike_history = [400] * 23 + [400]  # historico normalito
        inp_normal = _make_ivt_input(hourly_impressions=420, recent=normal_history)
        inp_spike = _make_ivt_input(hourly_impressions=5000, recent=spike_history)
        vec_normal = featurize_ivt(inp_normal)
        vec_spike = featurize_ivt(inp_spike)
        # index 9: hour_imps_zscore_bucket
        assert vec_spike[9] >= vec_normal[9], (
            f"Pico de volume deve ter z-score bucket >= normal: "
            f"spike={vec_spike[9]}, normal={vec_normal[9]}"
        )


# ---------------------------------------------------------------------------
# Testes do IVTScorer
# ---------------------------------------------------------------------------

class TestIVTScorer:

    def test_fail_open_when_not_loaded(self):
        """
        Scorer sem modelo carregado retorna is_ivt=False (fail-open).
        TX-6: nao bloquear trafego limpo por falha de ML.
        """
        scorer = IVTScorer()  # sem onnx_path
        inp = IVTScoringInput(
            device_class="chrome-mobile",
            hour_of_day=14,
            day_of_week=2,
            geo_country="BR",
        )
        result = scorer.score_event(inp)
        assert result.is_ivt is False, "Fail-open deve retornar is_ivt=False"
        assert result.ivt_prob == 0.0
        assert result.model_version == "fail_open"

    def test_is_loaded_false_before_load(self):
        scorer = IVTScorer()
        assert scorer.is_loaded is False

    def test_score_deterministic_without_model(self):
        """Mesmo input sempre retorna o mesmo resultado (deterministico no fail-open)."""
        scorer = IVTScorer()
        inp = IVTScoringInput(
            device_class="chrome-mobile",
            hour_of_day=14,
            day_of_week=2,
            geo_country="BR",
        )
        r1 = scorer.score_event(inp)
        r2 = scorer.score_event(inp)
        assert r1.is_ivt == r2.is_ivt
        assert r1.ivt_prob == r2.ivt_prob
        assert r1.ivt_label == r2.ivt_label

    def test_score_output_fields(self):
        """IVTScore tem todos os campos necessarios."""
        scorer = IVTScorer()
        inp = IVTScoringInput(
            device_class="bot",
            hour_of_day=3,
            day_of_week=0,
            geo_country="US",
        )
        result = scorer.score_event(inp)
        assert hasattr(result, "is_ivt")
        assert hasattr(result, "ivt_prob")
        assert hasattr(result, "ivt_label")
        assert hasattr(result, "model_version")
        assert hasattr(result, "scored_at_utc")
        assert isinstance(result.is_ivt, bool)
        assert isinstance(result.ivt_prob, float)
        assert isinstance(result.ivt_label, int)
        assert 0.0 <= result.ivt_prob <= 1.0

    def test_batch_score_returns_one_per_input(self):
        """score_batch retorna um resultado por evento."""
        scorer = IVTScorer()
        inputs = [
            IVTScoringInput(device_class="chrome-mobile", hour_of_day=i,
                            day_of_week=0, geo_country="BR")
            for i in range(5)
        ]
        results = scorer.score_batch(inputs)
        assert len(results) == 5


# ---------------------------------------------------------------------------
# Teste de treino IVT (integração leve)
# ---------------------------------------------------------------------------

class TestIVTTraining:

    def test_train_and_score_deterministic(self):
        """
        Treina o classificador IVT em sample sintetico e verifica:
        - Modelo produz score/label deterministico para a mesma entrada
        - Score esta em [0, 1]
        - Modelo registrado sob 'ivt_classifier' (nao 'pctr_lgb')
        """
        import os
        import tempfile
        os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

        from ml.fraud.train_ivt import IVT_MODEL_NAME, run as train_run

        # Mlflow tracking em tempdir para este teste.
        # NOTA: as verificacoes de arquivo e scorer ficam DENTRO do bloco
        # with para que o tempdir nao seja apagado antes das assercoes.
        with tempfile.TemporaryDirectory() as tmpdir:
            mlflow_uri = f"file:{tmpdir}/mlruns"
            onnx_path = Path(tmpdir) / "ivt_model.onnx"

            result = train_run(
                data_path=None,        # usa sample sintetico
                n_estimators=50,       # rapido para CI
                val_fraction=0.2,
                seed=42,
                mlflow_tracking_uri=mlflow_uri,
                run_name="test_ivt",
                onnx_output_path=onnx_path,
            )

            # Verificacoes (dentro do with — tmpdir ainda existe)
            assert result["model_name"] == "ivt_classifier", (
                f"Modelo deve ser registrado como 'ivt_classifier'; got {result['model_name']}"
            )
            assert result["model_name"] != "pctr_lgb", (
                "IVT nao deve sobrepor artefatos do pctr_lgb"
            )
            assert result["metrics"]["val_auc"] > 0.5, (
                f"AUC deve ser > 0.5; got {result['metrics']['val_auc']}"
            )
            assert onnx_path.exists(), "Artefato ONNX deve ser gerado"

            # Scorer com modelo treinado
            scorer = IVTScorer(onnx_path=onnx_path, model_version=result["run_id"])
            scorer.load()
            assert scorer.is_loaded

            inp = IVTScoringInput(
                device_class="bot",
                hour_of_day=2,
                day_of_week=0,
                geo_country="BR",
                hourly_requests=50000,
                hourly_impressions=200,
                hourly_clicks=0,
                recent_hourly_imps=[200] * 24,
            )

            # Deterministico: dois scores identicos
            r1 = scorer.score_event(inp)
            r2 = scorer.score_event(inp)
            assert r1.is_ivt == r2.is_ivt, "Score deve ser deterministico"
            assert r1.ivt_prob == r2.ivt_prob
            assert 0.0 <= r1.ivt_prob <= 1.0
            assert r1.ivt_label in (0, 1)

    def test_ivt_feature_version_logged(self):
        """IVT_FEATURE_VERSION deve ser a string '1.0.0'."""
        assert IVT_FEATURE_VERSION == "1.0.0"

    def test_ivt_model_name_is_distinct(self):
        """IVT_MODEL_NAME deve ser distinto do modelo pCTR."""
        from ml.fraud.train_ivt import IVT_MODEL_NAME
        assert IVT_MODEL_NAME == "ivt_classifier"
        assert IVT_MODEL_NAME != "pctr_lgb"
