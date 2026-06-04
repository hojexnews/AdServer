"""
ml/features/python/featurize.py
================================
Implementacao Python da funcao unica de featurizacao (anti-skew J1<->J2).

FONTE DE VERDADE: ml/features/spec/feature_spec.yaml versao 1.0.0

REGRAS INVIOLAVEIS:
  - Esta implementacao DEVE ser equivalente a implementacao Go em
    internal/ranker/featurize.go bit-a-bit (dentro de tolerancia 1e-9).
  - NUNCA alterar sem atualizar a spec E os fixtures (testdata/parity_cases.json)
    E a implementacao Go.
  - Valores monetarios entram como int64 minor-units. A conversao para float32
    ocorre apenas dentro desta funcao para compor o vetor de ML (TX-2 / DA-10).
  - NENHUMA feature pode derivar de PII / IP bruto (TX-5/DA-11).
    O campo 'geo' aqui ja e o derivado minimo (country/city) — o IP foi
    descartado antes de chegar aqui.

DEPENDENCIAS:
  - mmh3  (pip install mmh3)   — MurmurHash3 canonico
  - numpy (pip install numpy)  — vetor float32

HASH CANONICO:
  mmh3.hash(value: str, seed: int, signed=False) % num_buckets
  (identico ao Go: murmur3.SeedSum32(seed, []byte(value)) % num_buckets)
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field
from typing import Dict, Optional

import mmh3
import numpy as np

FEATURE_SPEC_VERSION = "1.0.0"
VECTOR_LENGTH = 23  # indices 0..22 conforme spec

# ---------------------------------------------------------------------------
# Tipos de entrada (espelham os tipos do hot path Go)
# ---------------------------------------------------------------------------

@dataclass
class ZoneInput:
    width: int
    height: int


@dataclass
class RequestTimeInput:
    hour: int        # UTC, [0, 23]
    day_of_week: int # 0=Domingo ... 6=Sabado (igual a time.Weekday Go)


@dataclass
class GeoInput:
    country: str  # ISO 3166-1 alpha-2 ou "" se desconhecido
    city: str     # nome de cidade ou "" se desconhecido


@dataclass
class CandidateInput:
    tier: int               # 1=Override, 2=Contract, 3=Remnant
    priority: int           # snapshot.Campaign.Priority
    pacing_deficit: float   # [0.0, 1.0] — adimensional, nao monetario
    ecpm_minor_units: int   # int64 minor-units (TX-2); 0 para nao-Remnant
    banner_width: int
    banner_height: int
    creative_type: str      # "image", "html", "video"


@dataclass
class FeaturizeInput:
    zone: ZoneInput
    request_time_utc: RequestTimeInput
    geo: GeoInput
    device_class: str          # classe retornada por useragent.Classify
    candidate: CandidateInput
    candidate_count: int       # uint32 — tamanho real do conjunto elegivel
    campaign_delivered_impressions: int = 0
    campaign_goal_impressions: int = 0
    campaign_delivered_clicks: int = 0


# ---------------------------------------------------------------------------
# Constantes de transformacao (espelham a spec YAML)
# ---------------------------------------------------------------------------

_ZONE_WIDTH_BOUNDARIES  = [120, 160, 200, 234, 250, 300, 320, 336, 468, 728, 970]
_ZONE_HEIGHT_BOUNDARIES = [50, 60, 90, 100, 250, 280, 600, 1024]
_ASPECT_BOUNDARIES      = [50, 80, 100, 133, 200, 400, 800]
_PACING_BOUNDARIES      = [0.1, 0.25, 0.5, 0.75, 0.9]
_ECPM_LOG1P_BOUNDARIES  = [1.0, 2.0, 3.5, 5.0, 7.0, 9.0, 11.0]

_COUNTRY_HASH_SEED      = 42
_COUNTRY_HASH_BUCKETS   = 512
_CITY_HASH_SEED         = 43
_CITY_HASH_BUCKETS      = 2048
_DEVICE_HASH_SEED       = 44
_DEVICE_HASH_BUCKETS    = 16

_CREATIVE_TYPE_MAP: Dict[str, int] = {"image": 1, "html": 2, "video": 3}


# ---------------------------------------------------------------------------
# Funcoes auxiliares
# ---------------------------------------------------------------------------

def _bucketize(value: float, boundaries: list[float]) -> int:
    """
    Retorna o indice do bucket (0-based) para value dado os limites.
    Equivalente a bisect_right: bucket[i] se boundaries[i-1] <= value < boundaries[i].
    """
    for i, b in enumerate(boundaries):
        if value < b:
            return i
    return len(boundaries)


def _feature_hash(value: str, seed: int, num_buckets: int) -> int:
    """
    MurmurHash3 canonico.
    mmh3.hash(value, seed, signed=False) % num_buckets

    DEVE ser identico ao Go:
        murmur3.SeedSum32(uint32(seed), []byte(value)) % uint32(num_buckets)

    Se value for vazio, retorna 0 (missing conforme spec).
    """
    if not value:
        return 0
    h = mmh3.hash(value, seed, signed=False)
    return int(h % num_buckets)


def _log1p(value: float) -> float:
    """math.log1p — identico ao Go math.Log1p."""
    return math.log1p(value)


def _clip(value: float, lo: float, hi: float) -> float:
    return max(lo, min(hi, value))


def _ratio(num: int, den: int, clip_lo: float = 0.0, clip_hi: float = 1.0) -> float:
    if den == 0:
        return 0.0
    return _clip(num / den, clip_lo, clip_hi)


# ---------------------------------------------------------------------------
# Funcao principal de featurizacao
# ---------------------------------------------------------------------------

def featurize(inp: FeaturizeInput) -> np.ndarray:
    """
    Transforma um FeaturizeInput em um vetor numpy float32 de comprimento 23.

    A ordem dos elementos e IMUTAVEL dentro da versao 1.x da spec
    (serving_index 0..22 em feature_spec.yaml).

    Esta funcao e a UNICA implementacao Python da featurizacao.
    Treino (ml/training) e avaliacao (ml/ope) chamam esta funcao.
    NUNCA reimplementar inline em notebooks ou pipelines.
    """
    vec = np.zeros(VECTOR_LENGTH, dtype=np.float32)

    z = inp.zone
    t = inp.request_time_utc
    g = inp.geo
    c = inp.candidate

    # --- GRUPO 1: Zona ---
    # index 0: zone_width_bucket
    vec[0] = _bucketize(z.width, _ZONE_WIDTH_BOUNDARIES)
    # index 1: zone_height_bucket
    vec[1] = _bucketize(z.height, _ZONE_HEIGHT_BOUNDARIES)
    # index 2: zone_aspect_ratio_bucket
    aspect = (z.width * 100 // z.height) if z.height > 0 else 0
    vec[2] = _bucketize(aspect, _ASPECT_BOUNDARIES)

    # --- GRUPO 2: Temporal ---
    # index 3: hour_of_day
    vec[3] = t.hour
    # index 4: day_of_week
    vec[4] = t.day_of_week
    # index 5: is_weekend (domingo=0, sabado=6)
    vec[5] = 1 if t.day_of_week in (0, 6) else 0

    # --- GRUPO 3: Geo ---
    # index 6: geo_country_hash
    vec[6] = _feature_hash(g.country, _COUNTRY_HASH_SEED, _COUNTRY_HASH_BUCKETS)
    # index 7: geo_city_hash
    vec[7] = _feature_hash(g.city, _CITY_HASH_SEED, _CITY_HASH_BUCKETS)

    # --- GRUPO 4: Dispositivo ---
    # index 8: device_class_hash
    vec[8] = _feature_hash(inp.device_class, _DEVICE_HASH_SEED, _DEVICE_HASH_BUCKETS)
    # index 9: is_bot
    vec[9] = 1 if inp.device_class == "bot" else 0

    # --- GRUPO 5: Tier e prioridade ---
    # index 10: candidate_tier
    vec[10] = c.tier
    # index 11: campaign_priority
    vec[11] = _clip(c.priority, 0, 100)

    # --- GRUPO 6: Pacing ---
    # index 12: pacing_deficit
    vec[12] = float(c.pacing_deficit)
    # index 13: pacing_deficit_bucket
    vec[13] = _bucketize(c.pacing_deficit, _PACING_BOUNDARIES)

    # --- GRUPO 7: eCPM ---
    # NOTA TX-2/DA-10: ecpm_minor_units e int64. A transformacao log1p e apenas
    # para o vetor de ML — nao altera o valor monetario original.
    ecpm_log1p = _log1p(float(c.ecpm_minor_units))
    # index 14: ecpm_minor_units_log1p
    vec[14] = ecpm_log1p
    # index 15: ecpm_tier_bucket
    vec[15] = _bucketize(ecpm_log1p, _ECPM_LOG1P_BOUNDARIES)

    # --- GRUPO 8: Banner/criativo ---
    b_w_bucket = _bucketize(c.banner_width, _ZONE_WIDTH_BOUNDARIES)
    b_h_bucket = _bucketize(c.banner_height, _ZONE_HEIGHT_BOUNDARIES)
    # index 16: banner_width_bucket
    vec[16] = b_w_bucket
    # index 17: banner_height_bucket
    vec[17] = b_h_bucket
    # index 18: banner_size_match
    vec[18] = 1 if (b_w_bucket == vec[0] and b_h_bucket == vec[1]) else 0
    # index 19: creative_type
    vec[19] = _CREATIVE_TYPE_MAP.get(c.creative_type, 0)

    # --- GRUPO 9: Competicao ---
    # index 20: candidate_count_log1p
    vec[20] = _log1p(float(inp.candidate_count))

    # --- GRUPO 10: Historico de campanha ---
    # index 21: campaign_ctr_estimate
    vec[21] = _ratio(
        inp.campaign_delivered_clicks,
        inp.campaign_delivered_impressions,
        0.0, 1.0,
    )
    # index 22: campaign_delivery_ratio
    vec[22] = _ratio(
        inp.campaign_delivered_impressions,
        inp.campaign_goal_impressions,
        0.0, 2.0,
    )

    return vec
