"""
ml/fraud/unsup_scorer.py
=========================
Scorer nao-supervisionado de IVT: combina Isolation Forest + Autoencoder.

=============================================================================
PAPEL (ADR-0004 §B / K2 / Fase 3):
=============================================================================

  O scorer nao-supervisionado COMPLEMENTA o GBDT supervisionado (ivt_classifier):
  detecta padroes de fraude NOVOS / nao-rotulados que o GBDT nao pega.

  NUNCA substitui o supervisionado. A logica de combinacao e OR sobre limiares:
    is_ivt_final = is_ivt_supervised OR is_ivt_unsupervised

  Isso garante que:
    - Fraude conhecida (GBDT detectou): e marcada como IVT.
    - Fraude nova/anomalia (GBDT nao detectou, IF ou AE detectou): tambem marcada.
    - Fail-open preservado: se ambos os scorers falharem, o evento passa como clean.

  GNN (futuro): documentado em train_unsup.py como work-future.

=============================================================================
SEMÂNTICA IVT-ANTES-DO-FATURAMENTO (TX-6):
=============================================================================

  O scorer nao-supervisionado roda na INGESTAO (near-line), ANTES do
  StatsHourly e do faturamento. Nunca no hot path de decisao.

  Sequencia:
    1. Evento chega em raw_impression (ivt_status = '' default).
    2. ivt_scoring_job.py (estendido em K2) pontua via:
         a. IVTScorer (GBDT supervisionado, scorer.py)
         b. IVTUnsupScorer (nao-supervisionado, este modulo)
         c. Combinacao OR: is_ivt_final = sup OR unsup
    3. ivt_status='ivt' propagado para raw_impression (via raw_ivt_score + MV 007)
    4. raw_ivt_unsup_score gravado em tabela separada (migr 008)
       para auditoria e calibracao futura.
    5. mv_impression_to_hourly continua filtrando ivt_status='' (004 INALTERADA).
    6. billing_batch_hourly.py fatura apenas trafego clean (Iceberg).

=============================================================================
INVARIANTES (nao violar):
=============================================================================

  TX-6:  fora do hot path; fail-open (is_unsup_ivt=False em qualquer falha).
  TX-5/DA-11: IVTScoringInput e PII-free (reusa a mesma entrada do supervisionado).
  TX-2:  nenhum valor monetario neste scorer.
  Anti-skew: reusa featurize_ivt() de ml/fraud/features.py (nunca reimplementa).

=============================================================================
CONTRATO PUBLICO:
=============================================================================

  score_event(inp: IVTScoringInput) -> IVTUnsupScore
    - Thread-safe (lock interno, igual ao IVTScorer supervisionado).
    - Fail-open: excecao -> IVTUnsupScore com is_unsup_ivt=False.

  score_batch(inputs: List[IVTScoringInput]) -> List[IVTUnsupScore]
    - Fail-open individual por evento.

  combine_scores(sup: IVTScore, unsup: IVTUnsupScore) -> bool
    - Retorna True (IVT) se o supervisionado OU o nao-supervisionado marcou IVT.
    - Implementa a politica OR (mais ampla, cobre fraude nova).

=============================================================================
ARTEFATOS NECESSARIOS (gerados por train_unsup.py):
=============================================================================

  ivt_unsup_if.onnx       — Isolation Forest ONNX
  ivt_unsup_ae_weights.json — Pesos do autoencoder em JSON
  ivt_unsup_ae_meta.json  — Metadata (threshold, feature names, etc.)
"""
from __future__ import annotations

import json
import sys
import threading
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import List, Optional

import numpy as np

_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.fraud.features import (  # noqa: E402
    IVT_VECTOR_LENGTH,
    IVTEventInput,
    featurize_ivt,
)
from ml.features.python.featurize import GeoInput, RequestTimeInput  # noqa: E402
from ml.fraud.scorer import IVTScore, IVTScoringInput  # noqa: E402


# ---------------------------------------------------------------------------
# Tipo de saida do scorer nao-supervisionado
# ---------------------------------------------------------------------------

@dataclass
class IVTUnsupScore:
    """
    Saida do scorer nao-supervisionado IVT.

    is_unsup_ivt:    True se IF ou AE detectou anomalia.
    if_label:        1=anomalia, -1=normal (saida do IsolationForest).
    if_score:        Score de anomalia do IF (mais negativo = mais anomalo).
    ae_error:        Erro quadratico medio de reconstrucao do autoencoder.
    ae_threshold:    Limiar usado para classificar AE (erro > threshold = anomalia).
    model_version:   versao dos modelos carregados.
    scored_at_utc:   ISO 8601 do instante de scoring.
    """
    is_unsup_ivt:    bool
    if_label:        int     # 1=normal, -1=anomalia (convencao IsolationForest)
    if_score:        float   # anomaly score (float legitimo: score ML, nao dinheiro)
    ae_error:        float   # erro de reconstrucao (float legitimo: metrica ML)
    ae_threshold:    float   # limiar do AE (float legitimo: hiperparametro ML)
    model_version:   str
    scored_at_utc:   str


def _make_fail_open_unsup() -> IVTUnsupScore:
    return IVTUnsupScore(
        is_unsup_ivt=False,
        if_label=1,      # normal (fail-open = nao bloquear trafego)
        if_score=0.0,
        ae_error=0.0,
        ae_threshold=float("inf"),
        model_version="fail_open",
        scored_at_utc=datetime.now(timezone.utc).isoformat(),
    )


# ---------------------------------------------------------------------------
# Forward pass do autoencoder em numpy (sem PyTorch — MVP, ADR-0004 §A)
# ---------------------------------------------------------------------------

def _relu(x: np.ndarray) -> np.ndarray:
    return np.maximum(0.0, x)


_ACTIVATIONS = {
    "relu":     _relu,
    "identity": lambda x: x,
    "tanh":     np.tanh,
    "logistic":  lambda x: 1.0 / (1.0 + np.exp(-x)),
}


def _ae_forward(
    x: np.ndarray,         # shape (n_features,) ou (N, n_features)
    weights: list,         # list de np.ndarray, um por camada
    biases: list,          # list de np.ndarray, um por camada
    activation: str = "relu",
) -> np.ndarray:
    """
    Forward pass do autoencoder em numpy.

    Arquitetura: n_features -> hidden -> n_features
    Ativacao: relu nas camadas ocultas; identity na ultima camada.
    """
    act_fn = _ACTIVATIONS.get(activation, _relu)
    h = x.astype(np.float64)
    for i, (W, b) in enumerate(zip(weights, biases)):
        W_arr = np.array(W, dtype=np.float64)
        b_arr = np.array(b, dtype=np.float64)
        h = h @ W_arr + b_arr
        if i < len(weights) - 1:
            h = act_fn(h)
        # Ultima camada: identity (regressor)
    return h


# ---------------------------------------------------------------------------
# Scorer nao-supervisionado (thread-safe, fail-open)
# ---------------------------------------------------------------------------

class IVTUnsupScorer:
    """
    Scorer nao-supervisionado de IVT: combina IsolationForest + Autoencoder.

    Ciclo de vida:
      1. scorer = IVTUnsupScorer(if_onnx_path, ae_weights_path, ae_meta_path)
      2. scorer.load()                        — carrega artefatos
      3. result = scorer.score_event(inp)     — scoring thread-safe

    Fail-open: qualquer falha retorna IVTUnsupScore com is_unsup_ivt=False.
    """

    def __init__(
        self,
        if_onnx_path: Optional[Path] = None,
        ae_weights_path: Optional[Path] = None,
        ae_meta_path: Optional[Path] = None,
        model_version: str = "unknown",
        if_threshold: float = -0.05,   # IF score < if_threshold -> anomalia
                                       # (complementa o label -1 do IF)
    ):
        """
        Args:
            if_onnx_path:    Caminho para ivt_unsup_if.onnx.
            ae_weights_path: Caminho para ivt_unsup_ae_weights.json.
            ae_meta_path:    Caminho para ivt_unsup_ae_meta.json (threshold).
            model_version:   Versao dos modelos (do MLflow registry).
            if_threshold:    Limiar para o score do IF (score < threshold = anomalia).
                             Padrao -0.05 (scores negativos indicam anomalia no IF).
                             Gated por calibracao com trafego real.
        """
        self._if_onnx_path    = Path(if_onnx_path) if if_onnx_path else None
        self._ae_weights_path = Path(ae_weights_path) if ae_weights_path else None
        self._ae_meta_path    = Path(ae_meta_path) if ae_meta_path else None
        self._model_version   = model_version
        self._if_threshold    = if_threshold

        # Artefatos carregados
        self._if_session  = None    # onnxruntime.InferenceSession
        self._ae_weights  = None    # dict com pesos JSON
        self._ae_threshold = float("inf")  # limiar de erro de reconstrucao
        self._scaler_mean  = None   # np.ndarray
        self._scaler_scale = None   # np.ndarray
        self._lock = threading.Lock()

    def load(
        self,
        if_onnx_path: Optional[Path] = None,
        ae_weights_path: Optional[Path] = None,
        ae_meta_path: Optional[Path] = None,
    ) -> None:
        """
        Carrega (ou recarrega) os artefatos dos modelos nao-supervisionados.
        Thread-safe via lock. Fail-open em caso de erro.
        """
        if_path  = Path(if_onnx_path)  if if_onnx_path  else self._if_onnx_path
        ae_path  = Path(ae_weights_path) if ae_weights_path else self._ae_weights_path
        ae_meta  = Path(ae_meta_path)  if ae_meta_path  else self._ae_meta_path

        new_if_session  = None
        new_ae_weights  = None
        new_ae_threshold = float("inf")
        new_scaler_mean  = None
        new_scaler_scale = None

        # Carrega Isolation Forest (ONNX)
        if if_path and if_path.exists():
            try:
                import onnxruntime as ort
                sess_opts = ort.SessionOptions()
                sess_opts.log_severity_level = 3
                new_if_session = ort.InferenceSession(
                    str(if_path),
                    sess_opts=sess_opts,
                    providers=["CPUExecutionProvider"],
                )
                print(f"[IVTUnsupScorer] IF ONNX carregado: {if_path}")
            except Exception as exc:
                print(f"[IVTUnsupScorer] ERRO ao carregar IF ONNX: {exc}. Fail-open para IF.")
        else:
            print(f"[IVTUnsupScorer] IF ONNX nao encontrado em {if_path}. Fail-open para IF.")

        # Carrega Autoencoder (pesos JSON)
        if ae_path and ae_path.exists():
            try:
                with open(ae_path, "r", encoding="utf-8") as f:
                    new_ae_weights = json.load(f)
                new_scaler_mean  = np.array(new_ae_weights["scaler_mean"],  dtype=np.float64)
                new_scaler_scale = np.array(new_ae_weights["scaler_scale"], dtype=np.float64)
                print(f"[IVTUnsupScorer] AE pesos carregados: {ae_path}")
            except Exception as exc:
                print(f"[IVTUnsupScorer] ERRO ao carregar AE pesos: {exc}. Fail-open para AE.")

        # Carrega metadata do AE (threshold)
        if ae_meta and ae_meta.exists():
            try:
                with open(ae_meta, "r", encoding="utf-8") as f:
                    meta = json.load(f)
                new_ae_threshold = float(meta["ae_threshold"])
                print(f"[IVTUnsupScorer] AE threshold={new_ae_threshold:.4f}")
            except Exception as exc:
                print(f"[IVTUnsupScorer] ERRO ao carregar AE meta: {exc}.")

        with self._lock:
            self._if_session   = new_if_session
            self._ae_weights   = new_ae_weights
            self._ae_threshold = new_ae_threshold
            self._scaler_mean  = new_scaler_mean
            self._scaler_scale = new_scaler_scale
            if if_onnx_path:
                self._if_onnx_path = if_path
            if ae_weights_path:
                self._ae_weights_path = ae_path
            if ae_meta_path:
                self._ae_meta_path = ae_meta

    def _score_if(self, vec: np.ndarray) -> tuple:
        """
        Pontua via IsolationForest ONNX.
        Retorna (if_label: int, if_score: float) ou (1, 0.0) em fail-open.
        if_label: 1=normal, -1=anomalia (convencao sklearn IsolationForest).
        """
        with self._lock:
            session = self._if_session

        if session is None:
            return 1, 0.0  # fail-open: normal

        try:
            input_name = session.get_inputs()[0].name
            x = vec.reshape(1, -1).astype(np.float32)
            outputs = session.run(None, {input_name: x})
            # output[0]: label (shape [1,1]), output[1]: score (shape [1,1])
            if_label = int(outputs[0].flatten()[0])
            if_score = float(outputs[1].flatten()[0])
            return if_label, if_score
        except Exception as exc:
            print(f"[IVTUnsupScorer] ERRO IF inference: {exc}. Fail-open.")
            return 1, 0.0

    def _score_ae(self, vec: np.ndarray) -> tuple:
        """
        Pontua via Autoencoder (forward pass numpy).
        Retorna (ae_error: float, ae_threshold: float) ou (0.0, inf) em fail-open.
        """
        with self._lock:
            ae_weights   = self._ae_weights
            scaler_mean  = self._scaler_mean
            scaler_scale = self._scaler_scale
            ae_threshold = self._ae_threshold

        if ae_weights is None or scaler_mean is None:
            return 0.0, float("inf")  # fail-open: nao e anomalia

        try:
            # Normaliza (StandardScaler)
            x = vec.astype(np.float64)
            x_scaled = (x - scaler_mean) / np.maximum(scaler_scale, 1e-9)

            # Forward pass
            x_rec = _ae_forward(
                x_scaled,
                weights=ae_weights["weights"],
                biases=ae_weights["biases"],
                activation=ae_weights.get("activation", "relu"),
            )

            # Erro de reconstrucao: MSE
            ae_error = float(np.mean((x_scaled - x_rec) ** 2))
            return ae_error, ae_threshold
        except Exception as exc:
            print(f"[IVTUnsupScorer] ERRO AE inference: {exc}. Fail-open.")
            return 0.0, float("inf")

    def score_event(self, inp: IVTScoringInput) -> IVTUnsupScore:
        """
        Pontua um evento para IVT via modelos nao-supervisionados.

        CONTRATO:
          - Thread-safe (lock interno para leitura de sessao/pesos).
          - Fail-open: excecao -> IVTUnsupScore com is_unsup_ivt=False.
          - NAO loga, NAO armazena, NAO transmite o IVTScoringInput.
          - Latencia esperada: << 1ms (ONNX CPU + forward numpy).
            TX-6: scorer e near-line, nao no hot path.
        """
        try:
            # Fail-open rapido: nenhum modelo carregado
            with self._lock:
                no_models = (self._if_session is None and self._ae_weights is None)
            if no_models:
                return _make_fail_open_unsup()

            # Featurizacao (anti-skew: funcao unica de ml/fraud/features.py)
            ivt_inp = IVTEventInput(
                device_class=inp.device_class,
                request_time_utc=RequestTimeInput(
                    hour=inp.hour_of_day,
                    day_of_week=inp.day_of_week,
                ),
                geo=GeoInput(
                    country=inp.geo_country,
                    city=inp.geo_city,
                ),
                hourly_requests=inp.hourly_requests,
                hourly_impressions=inp.hourly_impressions,
                hourly_clicks=inp.hourly_clicks,
                recent_hourly_impressions=inp.recent_hourly_imps or None,
            )
            vec = featurize_ivt(ivt_inp).astype(np.float32)

            # Isolation Forest
            if_label, if_score = self._score_if(vec)

            # Autoencoder
            ae_error, ae_threshold = self._score_ae(vec)

            # Combinacao: OR sobre criterios de anomalia
            #   IF: label == -1 (anomalia pela arvore de isolamento)
            #   AE: erro de reconstrucao > threshold (desvio do padrao normal)
            is_if_anomaly = (if_label == -1)
            is_ae_anomaly = (ae_error > ae_threshold)
            is_unsup_ivt  = is_if_anomaly or is_ae_anomaly

            return IVTUnsupScore(
                is_unsup_ivt=bool(is_unsup_ivt),
                if_label=if_label,
                if_score=float(if_score),
                ae_error=float(ae_error),
                ae_threshold=float(ae_threshold),
                model_version=self._model_version,
                scored_at_utc=datetime.now(timezone.utc).isoformat(),
            )

        except Exception as exc:
            print(f"[IVTUnsupScorer] ERRO em score_event: {exc}. Fail-open.")
            return _make_fail_open_unsup()

    def score_batch(self, inputs: List[IVTScoringInput]) -> List[IVTUnsupScore]:
        """
        Pontua um lote de eventos.
        Fail-open individual: falha em um nao propaga para os outros.
        """
        return [self.score_event(inp) for inp in inputs]

    @property
    def is_loaded(self) -> bool:
        """True se pelo menos um modelo esta carregado."""
        with self._lock:
            return self._if_session is not None or self._ae_weights is not None


# ---------------------------------------------------------------------------
# Funcao de combinacao de scores
# ---------------------------------------------------------------------------

def combine_ivt_scores(
    sup_score: IVTScore,
    unsup_score: IVTUnsupScore,
) -> bool:
    """
    Combina os scores supervisionado e nao-supervisionado para decisao final de IVT.

    POLITICA: OR sobre limiares
      is_ivt_final = is_ivt_sup OR is_unsup_ivt

    JUSTIFICATIVA:
      - O supervisionado (GBDT) detecta fraude conhecida/rotulada com alta precisao.
      - O nao-supervisionado detecta anomalias/fraude nova nao-rotulada.
      - OR e a politica mais ampla: cobre o maximo de fraude sem sacrificar
        trafego limpo (fail-open preservado: ambos em fail-open -> False).
      - Em producao real, avaliar a taxa de falso-positivo e ajustar se necessario
        via pesos/ponderacao (AND mais conservador, OR mais amplo).

    Fail-open:
      Se ambos os scorers estiverem em fail-open (model_version='fail_open'),
      retorna False (nao bloquear trafego limpo por falha de ML, TX-6).

    Args:
      sup_score:   IVTScore do scorer supervisionado (ivt_classifier GBDT).
      unsup_score: IVTUnsupScore do scorer nao-supervisionado (IF + AE).

    Retorna True se o evento deve ser marcado como IVT.
    """
    sup_is_ivt   = sup_score.is_ivt
    unsup_is_ivt = unsup_score.is_unsup_ivt
    return bool(sup_is_ivt or unsup_is_ivt)


# ---------------------------------------------------------------------------
# Factory: carrega scorer a partir do MLflow registry
# ---------------------------------------------------------------------------

def load_unsup_scorer_from_registry(
    mlflow_tracking_uri: str,
    if_model_name: str = "ivt_unsup_isolation_forest",
    ae_model_name: str = "ivt_unsup_autoencoder",
    model_stage: str = "Staging",
) -> IVTUnsupScorer:
    """
    Factory: carrega artefatos nao-supervisionados do MLflow registry.

    Tenta carregar de 'ivt_unsup' (run mais recente do experimento).
    Se falhar: retorna scorer em fail-open.
    """
    import os
    os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

    try:
        import mlflow
        mlflow.set_tracking_uri(mlflow_tracking_uri)

        # Busca o run mais recente do experimento 'ivt_unsup'
        runs = mlflow.search_runs(
            experiment_names=["ivt_unsup"],
            order_by=["start_time DESC"],
            max_results=1,
        )
        if runs.empty:
            print("[load_unsup_scorer] Nenhum run do experimento 'ivt_unsup'. Fail-open.")
            return IVTUnsupScorer()

        run_id = runs.iloc[0]["run_id"]
        artifacts_dir = Path(
            mlflow.artifacts.download_artifacts(f"runs:/{run_id}/models")
        )

        if_onnx_path   = artifacts_dir / "ivt_unsup_if.onnx"
        ae_weights_path = artifacts_dir / "ivt_unsup_ae_weights.json"
        ae_meta_path   = artifacts_dir / "ivt_unsup_ae_meta.json"

        scorer = IVTUnsupScorer(
            if_onnx_path=if_onnx_path,
            ae_weights_path=ae_weights_path,
            ae_meta_path=ae_meta_path,
            model_version=run_id,
        )
        scorer.load()
        return scorer

    except Exception as exc:
        print(f"[load_unsup_scorer] ERRO: {exc}. Fail-open.")
        return IVTUnsupScorer()
