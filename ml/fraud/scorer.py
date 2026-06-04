"""
ml/fraud/scorer.py
==================
Interface de scoring IVT para o incremento de data-platform.

=============================================================================
CONTRATO DA INTERFACE (para fiacao na ingestao)
=============================================================================

ENTRYPOINT PUBLICO:
  score_event(event: IVTScoringInput) -> IVTScore

ENTRADA — IVTScoringInput (dataclass, todos os campos sem PII):
  device_class:        str   — classe coarsa de UA (ex.: "bot", "chrome-mobile")
                               O UA BRUTO deve ser descartado antes de chamar scorer.
  hour_of_day:         int   — hora UTC [0, 23]
  day_of_week:         int   — dia da semana [0=Domingo..6=Sabado]
  geo_country:         str   — ISO 3166-1 alpha-2 (derivado de IP; IP descartado)
  geo_city:            str   — nome de cidade (derivado; padrao "" se desconhecido)
  hourly_requests:     int   — total de requests da campanha/zona na hora corrente
                               (de StatsHourly ou buffer near-line; int64)
  hourly_impressions:  int   — total de impressoes da campanha/zona na hora
  hourly_clicks:       int   — total de clicks da campanha/zona na hora
  recent_hourly_imps:  list[int] — historico de impressoes das ultimas 24h
                               (ordem cronologica; pode ser [] se indisponivel)

SAIDA — IVTScore (dataclass):
  is_ivt:        bool  — True se o evento e classificado como IVT
  ivt_prob:      float — probabilidade de IVT em [0.0, 1.0]
  ivt_label:     int   — 0=clean, 1=IVT (threshold padrao 0.5)
  model_version: str   — versao do modelo carregado (do MLflow registry)
  scored_at_utc: str   — ISO 8601 do instante de scoring

CONTRATO DE RESPONSABILIDADE (TX-6):
  - O scorer e invocado na INGESTAO (batch/near-line), ANTES do StatsHourly
    e do faturamento (reconciliacao batch contra Iceberg).
  - Eventos com is_ivt=True devem ter ivt_status='ivt' gravado em
    raw_impression ANTES da MV mv_impression_to_hourly ser disparada.
  - O scorer NAO e invocado no hot path de decisao (latencia de minutos ok).
  - Se o scorer falhar (excecao, timeout, modelo nao carregado): fail-open
    com is_ivt=False, ivt_prob=0.0, model_version='fail_open'. O evento
    passa como clean — conservador para nao bloquear trafego legitimo.
    O incremento de data-platform DEVE monitorar a taxa de fail_open.

CONTRATO DE THREADING:
  IVTScorer e thread-safe para leituras (score_event). O metodo load() deve
  ser chamado uma vez durante a inicializacao do processo de ingestao.
  Hot-reload de modelo: chamar load() novamente com nova versao — e
  thread-safe via lock interno.

EXEMPLO DE USO (no incremento de data-platform):
  from ml.fraud.scorer import IVTScorer, IVTScoringInput

  scorer = IVTScorer(
      onnx_path="/path/to/ivt_model.onnx",
      model_version="1",
  )
  scorer.load()

  result = scorer.score_event(IVTScoringInput(
      device_class="chrome-mobile",
      hour_of_day=14,
      day_of_week=2,
      geo_country="BR",
      geo_city="Sao Paulo",
      hourly_requests=500,
      hourly_impressions=400,
      hourly_clicks=10,
      recent_hourly_imps=[380, 420, 390, 410, 430, ...],
  ))

  if result.is_ivt:
      # Gravar ivt_status='ivt' em raw_impression
      # Excluir da MV mv_impression_to_hourly (StatsHourly)
      ...

=============================================================================
INVARIANTES DE SEGURANCA (TX-5/DA-11 / TX-6):
  - scorer.score_event() nao loga, nao armazena, nao transmite o IVTScoringInput.
  - Nenhum campo de IVTScoringInput pode ser PII (UA bruto, IP, ID usuario).
  - O modelo e carregado de artefato local (ONNX) — sem chamada de rede no scoring.
  - fail-open: excecao -> is_ivt=False (nao bloqueia trafego limpo por falha de ML).
=============================================================================
"""
from __future__ import annotations

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
    IVTEventInput,
    featurize_ivt,
)
from ml.features.python.featurize import GeoInput, RequestTimeInput  # noqa: E402


# ---------------------------------------------------------------------------
# Tipos de entrada/saida do contrato publico
# ---------------------------------------------------------------------------

@dataclass
class IVTScoringInput:
    """
    Entrada publica do scorer IVT.

    TODOS OS CAMPOS SAO PII-FREE (TX-5/DA-11):
      - device_class: classe coarsa de UA (nao UA bruto)
      - geo_country/city: derivados de IP via GeoLite2 (IP bruto descartado)
      - campos horarios: agregados de campanha/zona (nao de usuario individual)

    CONTRATO DE TIPO:
      hourly_requests/impressions/clicks: int64 (contadores, nao monetario)
      recent_hourly_imps: list[int] em ordem cronologica (mais antigo primeiro)
    """
    device_class: str
    hour_of_day: int           # UTC [0, 23]
    day_of_week: int           # [0=Dom .. 6=Sab]
    geo_country: str           # ISO 3166-1 alpha-2 ou ""
    geo_city: str = ""         # nome de cidade ou ""
    hourly_requests: int = 0   # int64 — sem monetario (TX-2 n/a)
    hourly_impressions: int = 0
    hourly_clicks: int = 0
    recent_hourly_imps: List[int] = field(default_factory=list)


@dataclass
class IVTScore:
    """
    Saida do scorer IVT.

    is_ivt:        resultado de classificacao (threshold 0.5)
    ivt_prob:      probabilidade de IVT em [0.0, 1.0]
    ivt_label:     0=clean, 1=IVT
    model_version: versao do modelo carregado
    scored_at_utc: ISO 8601
    """
    is_ivt: bool
    ivt_prob: float     # probabilidade — float e correto para probabilidade
    ivt_label: int      # 0 ou 1
    model_version: str
    scored_at_utc: str


# Score de fail-open: clean (conservador — nao bloqueia trafego sem certeza)
_FAIL_OPEN_SCORE = IVTScore(
    is_ivt=False,
    ivt_prob=0.0,
    ivt_label=0,
    model_version="fail_open",
    scored_at_utc="",
)


# ---------------------------------------------------------------------------
# Scorer principal
# ---------------------------------------------------------------------------

class IVTScorer:
    """
    Scorer IVT thread-safe baseado em ONNX Runtime.

    Ciclo de vida:
      1. scorer = IVTScorer(onnx_path, model_version)
      2. scorer.load()           — carrega o modelo (uma vez por processo)
      3. result = scorer.score_event(inp)  — scoring thread-safe

    Hot-reload: chamar scorer.load(new_onnx_path) substitui o modelo
    atomicamente (lock interno). Nenhum request intermediario e bloqueado —
    o modelo anterior e usado ate o novo estar carregado.

    Fail-open: qualquer excecao em score_event() retorna is_ivt=False.
    O incremento de data-platform DEVE monitorar fail_open via metricas.
    """

    def __init__(
        self,
        onnx_path: Optional[Path] = None,
        model_version: str = "unknown",
        threshold: float = 0.5,
    ):
        """
        Args:
            onnx_path:     caminho para o artefato ivt_model.onnx.
                           Se None, o scorer opera em fail-open ate load() ser chamado.
            model_version: versao do modelo (do MLflow registry) para auditoria.
            threshold:     limiar de classificacao (padrao 0.5).
        """
        self._onnx_path = Path(onnx_path) if onnx_path else None
        self._model_version = model_version
        self._threshold = threshold
        self._session = None  # onnxruntime.InferenceSession
        self._lock = threading.Lock()

    def load(self, onnx_path: Optional[Path] = None) -> None:
        """
        Carrega (ou recarrega) o modelo ONNX.

        Thread-safe: usa lock para substituicao atomica do session.
        Fail-open: se o carregamento falhar, o scorer continua operando
        em fail-open com score de clean.
        """
        path = Path(onnx_path) if onnx_path else self._onnx_path
        if path is None or not path.exists():
            print(f"[IVTScorer] AVISO: modelo ONNX nao encontrado em {path}. "
                  "Operando em fail-open.")
            return

        try:
            import onnxruntime as ort
            sess_opts = ort.SessionOptions()
            sess_opts.log_severity_level = 3
            new_session = ort.InferenceSession(
                str(path),
                sess_opts=sess_opts,
                providers=["CPUExecutionProvider"],
            )
            with self._lock:
                self._session = new_session
                if onnx_path:
                    self._onnx_path = path
            print(f"[IVTScorer] Modelo carregado: {path} "
                  f"(model_version={self._model_version})")
        except Exception as exc:
            print(f"[IVTScorer] ERRO ao carregar modelo: {exc}. Fail-open ativado.")

    def score_event(self, inp: IVTScoringInput) -> IVTScore:
        """
        Pontua um evento de ingestao para IVT.

        CONTRATO:
          - Thread-safe (leitura do session via lock).
          - Fail-open: qualquer excecao retorna IVTScore com is_ivt=False.
          - NAO loga, NAO armazena, NAO transmite o IVTScoringInput.
          - Latencia esperada: << 1ms (ONNX CPU, vetor de 10 features).
            TX-6: scorer e near-line, nao no hot path. Mas deve ser rapido
            para nao bloquear o pipeline de ingestao.

        Args:
            inp: IVTScoringInput com campos PII-free

        Returns:
            IVTScore com is_ivt, ivt_prob, ivt_label, model_version, scored_at_utc
        """
        try:
            with self._lock:
                session = self._session

            if session is None:
                # Fail-open: modelo nao carregado
                return _make_fail_open()

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
            vec = featurize_ivt(ivt_inp).reshape(1, -1).astype(np.float32)

            # Inferencia ONNX
            input_name = session.get_inputs()[0].name
            outputs = session.run(None, {input_name: vec})

            # Extrai P(IVT=1) de outputs[1]
            probs_raw = outputs[1]
            if isinstance(probs_raw, list):
                ivt_prob = float(probs_raw[0][1])
            elif hasattr(probs_raw, "shape"):
                arr = np.array(probs_raw, dtype=np.float32)
                ivt_prob = float(arr[0, 1] if arr.ndim == 2 else arr[0])
            else:
                ivt_prob = float(list(probs_raw)[0])

            ivt_prob = float(np.clip(ivt_prob, 0.0, 1.0))
            ivt_label = int(ivt_prob >= self._threshold)

            return IVTScore(
                is_ivt=bool(ivt_label),
                ivt_prob=ivt_prob,
                ivt_label=ivt_label,
                model_version=self._model_version,
                scored_at_utc=datetime.now(timezone.utc).isoformat(),
            )

        except Exception as exc:
            # Fail-open: log e retorna clean (conservador)
            print(f"[IVTScorer] ERRO em score_event: {exc}. Fail-open.")
            return _make_fail_open()

    def score_batch(self, inputs: List[IVTScoringInput]) -> List[IVTScore]:
        """
        Pontua um lote de eventos.

        Util para o pipeline de ingestao processar micro-batches eficientemente.
        Fail-open individual: cada evento tem seu proprio score; falha em um
        nao propaga para os outros.
        """
        return [self.score_event(inp) for inp in inputs]

    @property
    def is_loaded(self) -> bool:
        """True se o modelo ONNX esta carregado e pronto para scoring."""
        with self._lock:
            return self._session is not None


def _make_fail_open() -> IVTScore:
    return IVTScore(
        is_ivt=False,
        ivt_prob=0.0,
        ivt_label=0,
        model_version="fail_open",
        scored_at_utc=datetime.now(timezone.utc).isoformat(),
    )


# ---------------------------------------------------------------------------
# Factory: carrega scorer a partir do MLflow registry
# ---------------------------------------------------------------------------

def load_scorer_from_registry(
    mlflow_tracking_uri: str,
    model_name: str = "ivt_classifier",
    model_stage: str = "Staging",
    threshold: float = 0.5,
) -> IVTScorer:
    """
    Factory: carrega o artefato ivt_model.onnx do MLflow registry e
    retorna um IVTScorer pronto para uso.

    Uso tipico no processo de ingestao:
        scorer = load_scorer_from_registry(
            mlflow_tracking_uri="http://mlflow-server:5000",
            model_name="ivt_classifier",
            model_stage="Staging",
        )
        result = scorer.score_event(inp)

    Se o MLflow nao estiver disponivel: retorna IVTScorer sem modelo carregado
    (operara em fail-open).
    """
    import os
    os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

    try:
        import mlflow
        from mlflow import MlflowClient

        mlflow.set_tracking_uri(mlflow_tracking_uri)
        client = MlflowClient()

        # Defesa em profundidade (gate de segurança): model_name é argumento de
        # operador (não vem de request), mas é interpolado no filtro MLflow.
        import re
        if not re.fullmatch(r"[A-Za-z0-9_.\-]+", model_name):
            raise ValueError(f"model_name invalido: {model_name!r}")
        versions = client.search_model_versions(f"name='{model_name}'")
        if not versions:
            print(f"[load_scorer] Nenhuma versao de '{model_name}' no registry. Fail-open.")
            return IVTScorer(threshold=threshold)

        # Filtra por stage ou pega a mais recente
        staged = [v for v in versions if v.current_stage == model_stage]
        target = (
            sorted(staged, key=lambda v: int(v.version), reverse=True)[0]
            if staged
            else sorted(versions, key=lambda v: int(v.version), reverse=True)[0]
        )

        model_version = target.version
        local_path = mlflow.artifacts.download_artifacts(
            f"runs:/{target.run_id}/model/ivt_model.onnx"
        )

        scorer = IVTScorer(
            onnx_path=Path(local_path),
            model_version=str(model_version),
            threshold=threshold,
        )
        scorer.load()
        return scorer

    except Exception as exc:
        print(f"[load_scorer] ERRO ao carregar do registry: {exc}. Fail-open.")
        return IVTScorer(threshold=threshold)
