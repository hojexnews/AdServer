"""
ml/fraud/features.py
=====================
Features PII-free para o classificador IVT supervisionado.

PRINCIPIO DE DESIGN:
  As features de fraude REUSAM a featurizacao base de ml/features/python/featurize.py
  (anti-skew, spec 1.0.0). Features adicionais especificas de IVT sao derivadas
  de metricas de evento agregado — sem PII, sem IP bruto (TX-5/DA-11).

CONFORMIDADE PII (TX-5/DA-11):
  - NENHUMA feature pode ser ou derivar de: IP bruto, ID de usuario persistente,
    cookie cross-site, fingerprint de navegador, User-Agent completo.
  - Features permitidas: classe coarsa de UA (is_bot, device_class_hash),
    geo nivel de cidade (hash), hora do dia, metricas de taxa (req/imps por
    campanha/zona) — todos agregados, sem PII individual.
  - O IP e descartado pelo geo.Resolver ANTES de chegar aqui (identico ao
    pipeline de pCTR).

FEATURES IVT-ESPECIFICAS (alem das 23 da spec pCTR):
  As features abaixo sao derivadas de metricas de evento/agregado e nao
  estao na spec pCTR porque sao especificas do contexto de ingestao:

  ivt_features (indices 0..9 no vetor IVT, separado do vetor pCTR):
    0  is_bot_flag            — device_class == "bot" (ja na spec, reusado)
    1  hour_of_day            — hora UTC (reusado da spec)
    2  is_weekend             — flag fim-de-semana (reusado)
    3  device_class_hash      — hash da classe de UA (reusado)
    4  geo_country_hash       — hash do pais (reusado)
    5  request_rate_bucket    — taxa de requests por campanha/zona na hora corrente
                                (StatsHourly: requests/hora, bucketizado)
    6  impression_rate_bucket — taxa de impressoes/hora da campanha/zona
    7  click_rate_bucket      — taxa de clicks/hora da campanha/zona
    8  inventory_loss_bucket  — (requests - impressions)/requests por hora
                                (sinal de escassez/abuso)
    9  hour_imps_zscore_bucket — z-score das impressoes correntes vs. media das
                                 ultimas 24h para a campanha/zona (pico anormal)

  TOTAL: 10 features IVT

CONTRATO DO VETOR:
  ivt_feature_vector: np.ndarray float32, shape (10,)
  Ordem dos elementos e IMUTAVEL para esta versao do classificador.
  Se novas features forem adicionadas, criar nova versao do modelo IVT
  (nao sobrescrever ivt_classifier v1 no MLflow).

DINHEIRO (TX-2): nenhuma feature monetaria. Todas as features sao
  contadores/ratios de impressoes/clicks — adimensionais.
"""
from __future__ import annotations

import math
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import List, Optional, Sequence

import numpy as np

# Resolve repo root para importar featurize
_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.features.python.featurize import (  # noqa: E402
    GeoInput,
    RequestTimeInput,
    _feature_hash,
    _DEVICE_HASH_BUCKETS,
    _DEVICE_HASH_SEED,
    _COUNTRY_HASH_BUCKETS,
    _COUNTRY_HASH_SEED,
)

# ---------------------------------------------------------------------------
# Constantes
# ---------------------------------------------------------------------------

IVT_FEATURE_VERSION = "1.0.0"
IVT_VECTOR_LENGTH = 10

# Buckets para taxas de request/impression/click por hora
# Unidade: contagens por hora (StatsHourly)
_REQUEST_RATE_BUCKETS  = [10, 50, 200, 500, 1000, 5000, 10000]
_IMPRESSION_RATE_BUCKETS = [5, 25, 100, 250, 500, 2500, 5000]
_CLICK_RATE_BUCKETS    = [1, 5, 20, 50, 100, 500]
_INV_LOSS_BUCKETS      = [0.05, 0.1, 0.2, 0.4, 0.6, 0.8]  # fracao de perda
_ZSCORE_BUCKETS        = [1.0, 2.0, 3.0, 4.0, 6.0]  # z-score de volume


def _bucketize_ivt(value: float, boundaries: list) -> int:
    """Bucketizacao identica a ml/features/python/featurize._bucketize."""
    for i, b in enumerate(boundaries):
        if value < b:
            return i
    return len(boundaries)


# ---------------------------------------------------------------------------
# Tipo de entrada para featurizacao IVT
# ---------------------------------------------------------------------------

@dataclass
class IVTEventInput:
    """
    Entrada para featurizacao de um evento de impressao/request para scoring IVT.

    CAMPOS OBRIGATORIOS (sem PII):
      device_class:       classe coarsa do UA (ex.: "bot", "chrome-mobile")
                          O UA bruto deve ser descartado antes de chegar aqui.
      request_time_utc:   hora/dia do request
      geo:                pais + cidade (derivados de IP via GeoLite2;
                          IP bruto descartado antes de chegar aqui)

    CAMPOS DE CONTEXTO HORARIO (de StatsHourly — agregados, sem PII):
      hourly_requests:    total de requests da campanha/zona na hora corrente
      hourly_impressions: total de impressoes da campanha/zona na hora corrente
      hourly_clicks:      total de clicks da campanha/zona na hora corrente
      recent_hourly_impressions: historico de impressoes por hora nas ultimas 24h
                          (para calculo do z-score de volume anormal)

    INVARIANTE TX-5/DA-11:
      Nenhum campo abaixo pode ser IP bruto, ID de usuario, cookie, UA completo.
      Todos os campos de usuario sao derivados coarsos (classe de UA, geo de cidade).
    """
    device_class: str            # classe coarsa — UA bruto descartado
    request_time_utc: RequestTimeInput
    geo: GeoInput
    # Metricas horarias agregadas (StatsHourly, sem PII)
    hourly_requests: int = 0
    hourly_impressions: int = 0
    hourly_clicks: int = 0
    # Historico das ultimas 24h de impressoes para z-score
    recent_hourly_impressions: Optional[List[int]] = None


# ---------------------------------------------------------------------------
# Funcao de featurizacao IVT
# ---------------------------------------------------------------------------

def featurize_ivt(inp: IVTEventInput) -> np.ndarray:
    """
    Transforma um IVTEventInput no vetor de features IVT (float32[10]).

    REUSA as funcoes canonicas de ml/features/python/featurize.py para
    os campos compartilhados (is_bot, hour_of_day, is_weekend,
    device_class_hash, geo_country_hash) — sem reimplementar logica.

    Features especificas de IVT (indices 5..9): derivadas de metricas
    agregadas de StatsHourly (request/impression/click rates, inventory loss,
    z-score de volume).

    ANTI-PII (TX-5/DA-11):
      - is_bot:           derivado de classe de UA (nao UA bruto)
      - device_class_hash: hash da classe coarsa (nao UA bruto)
      - geo_country_hash: hash do pais (nao IP)
      - taxas horarias:   agregados de campanha/zona (sem usuario)

    Retorna np.ndarray float32 de shape (10,).
    """
    vec = np.zeros(IVT_VECTOR_LENGTH, dtype=np.float32)

    t = inp.request_time_utc
    g = inp.geo

    # --- index 0: is_bot_flag ---
    vec[0] = 1.0 if inp.device_class == "bot" else 0.0

    # --- index 1: hour_of_day ---
    vec[1] = float(t.hour)

    # --- index 2: is_weekend ---
    vec[2] = 1.0 if t.day_of_week in (0, 6) else 0.0

    # --- index 3: device_class_hash ---
    # Reusa _feature_hash de featurize.py (mesma funcao canonica)
    vec[3] = float(_feature_hash(inp.device_class, _DEVICE_HASH_SEED, _DEVICE_HASH_BUCKETS))

    # --- index 4: geo_country_hash ---
    vec[4] = float(_feature_hash(g.country, _COUNTRY_HASH_SEED, _COUNTRY_HASH_BUCKETS))

    # --- index 5: request_rate_bucket ---
    vec[5] = float(_bucketize_ivt(float(inp.hourly_requests), _REQUEST_RATE_BUCKETS))

    # --- index 6: impression_rate_bucket ---
    vec[6] = float(_bucketize_ivt(float(inp.hourly_impressions), _IMPRESSION_RATE_BUCKETS))

    # --- index 7: click_rate_bucket ---
    vec[7] = float(_bucketize_ivt(float(inp.hourly_clicks), _CLICK_RATE_BUCKETS))

    # --- index 8: inventory_loss_bucket ---
    # inventory_loss = (requests - impressions) / requests — sinal de abuso/escassez
    reqs = max(1, inp.hourly_requests)
    imps = max(0, inp.hourly_impressions)
    inv_loss = max(0.0, (reqs - imps) / reqs)
    vec[8] = float(_bucketize_ivt(inv_loss, _INV_LOSS_BUCKETS))

    # --- index 9: hour_imps_zscore_bucket ---
    # Z-score das impressoes correntes vs. media das ultimas 24h
    # Sinal de volume anormal (pico atipico = possivel fraude)
    if inp.recent_hourly_impressions and len(inp.recent_hourly_impressions) >= 2:
        history = np.array(inp.recent_hourly_impressions, dtype=np.float64)
        mean_h = float(history.mean())
        std_h = float(history.std())
        if std_h > 0:
            zscore = abs(inp.hourly_impressions - mean_h) / std_h
        else:
            zscore = 0.0
    else:
        zscore = 0.0
    vec[9] = float(_bucketize_ivt(zscore, _ZSCORE_BUCKETS))

    return vec


# ---------------------------------------------------------------------------
# Nomes das features (para LightGBM e MLflow)
# ---------------------------------------------------------------------------

IVT_FEATURE_NAMES = [
    "is_bot_flag",              # 0
    "hour_of_day",              # 1
    "is_weekend",               # 2
    "device_class_hash",        # 3
    "geo_country_hash",         # 4
    "request_rate_bucket",      # 5
    "impression_rate_bucket",   # 6
    "click_rate_bucket",        # 7
    "inventory_loss_bucket",    # 8
    "hour_imps_zscore_bucket",  # 9
]

assert len(IVT_FEATURE_NAMES) == IVT_VECTOR_LENGTH
