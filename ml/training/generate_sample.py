"""
ml/training/generate_sample.py
================================
Gerador de dataset sintetico para treino do pCTR em ambiente sem Iceberg.

PRODUCAO: substitua este modulo pela leitura do Iceberg via PyArrow/PyIceberg.
Ver train_pctr.py, secao "DATA LOADING" para o ponto de substituicao.

Schema produzido e equivalente ao schema Iceberg:
  impressions JOIN clicks ON decision_id → label_click (0/1)

Colunas:
  decision_id           string    identificador unico da decisao (UUID4)
  zone_width            int       pixels
  zone_height           int       pixels
  hour_of_day           int       [0, 23] UTC
  day_of_week           int       [0, 6]  0=Domingo
  geo_country           string    ISO 3166-1 alpha-2
  geo_city              string    nome da cidade
  device_class          string    classe de UA
  candidate_tier        int       1=Override 2=Contract 3=Remnant
  campaign_priority     int       [0, 100]
  pacing_deficit        float     [0.0, 1.0]
  ecpm_minor_units      int       int64 minor-units (TX-2 — NUNCA float)
  banner_width          int       pixels
  banner_height         int       pixels
  creative_type         string    image/html/video
  candidate_count       int       [1, 20]
  delivered_impressions int       contador aproximado de snapshot
  goal_impressions      int       0 = sem goal (Override/Remnant)
  delivered_clicks      int       contador aproximado
  propensity            float     (0, 1] — propensidade logada (contrato propensity-logging.md)
  model_version         string    versao do modelo na decisao (DETERMINISTIC = pre-ML)
  feature_spec_version  string    "1.0.0"
  label_click           int       0 ou 1 — rotuulo binario (JOIN impression + click)

PRIVACIDADE (TX-5/DA-11): sem IP bruto, sem identificador de usuario.
DINHEIRO (TX-2): ecpm_minor_units e int64, nunca float.
"""
from __future__ import annotations

import sys
import uuid
from pathlib import Path
from typing import Optional

import numpy as np
import pandas as pd

# Resolve repo root para importar ml.features
_REPO_ROOT = Path(__file__).parent.parent.parent
if str(_REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(_REPO_ROOT))

from ml.features.python.featurize import (  # noqa: E402
    _ZONE_HEIGHT_BOUNDARIES,
    _ZONE_WIDTH_BOUNDARIES,
    _bucketize,
)

# ---------------------------------------------------------------------------
# Distribuicoes de populacao para o sample sintetico
# ---------------------------------------------------------------------------

_COUNTRIES = ["BR", "US", "PT", "AR", "MX", "DE", "FR", "GB", "ES", "IT"]
_CITIES = {
    "BR": ["Sao Paulo", "Rio de Janeiro", "Belo Horizonte", "Curitiba", "Recife"],
    "US": ["New York", "Los Angeles", "Chicago", "Houston", "Phoenix"],
    "PT": ["Lisboa", "Porto", "Braga", "Coimbra", "Setubal"],
    "AR": ["Buenos Aires", "Cordoba", "Rosario", "Mendoza", "Tucuman"],
    "MX": ["Mexico City", "Guadalajara", "Monterrey", "Puebla", "Tijuana"],
    "DE": ["Berlin", "Munich", "Hamburg", "Cologne", "Frankfurt"],
    "FR": ["Paris", "Lyon", "Marseille", "Toulouse", "Bordeaux"],
    "GB": ["London", "Manchester", "Birmingham", "Leeds", "Glasgow"],
    "ES": ["Madrid", "Barcelona", "Valencia", "Seville", "Zaragoza"],
    "IT": ["Rome", "Milan", "Naples", "Turin", "Palermo"],
}
_DEVICE_CLASSES = [
    "chrome-desktop", "chrome-mobile", "safari-mobile", "safari-desktop",
    "firefox-desktop", "edge-desktop", "other-mobile", "other-desktop", "bot",
]
_DEVICE_WEIGHTS = [0.30, 0.25, 0.15, 0.08, 0.08, 0.05, 0.04, 0.04, 0.01]

_ZONE_SIZES = [(300, 250), (728, 90), (160, 600), (320, 50), (300, 600),
               (970, 250), (320, 100), (468, 60), (336, 280), (250, 250)]
_CREATIVE_TYPES = ["image", "html", "video"]
_CREATIVE_WEIGHTS = [0.55, 0.35, 0.10]

# CTR base por tier (sintetico — apenas para produzir labels plausíveis)
_BASE_CTR = {1: 0.04, 2: 0.025, 3: 0.015}


def _make_row(rng: np.random.Generator, row_id: int) -> dict:
    """Gera uma linha sintetica de treino."""
    country = rng.choice(_COUNTRIES, p=[0.35, 0.20, 0.10, 0.08, 0.07,
                                         0.05, 0.05, 0.04, 0.03, 0.03])
    city = rng.choice(_CITIES[country])
    device_class = rng.choice(_DEVICE_CLASSES, p=_DEVICE_WEIGHTS)
    zone_w, zone_h = _ZONE_SIZES[rng.integers(len(_ZONE_SIZES))]
    banner_w, banner_h = _ZONE_SIZES[rng.integers(len(_ZONE_SIZES))]
    creative_type = rng.choice(_CREATIVE_TYPES, p=_CREATIVE_WEIGHTS)
    hour = int(rng.integers(0, 24))
    dow = int(rng.integers(0, 7))
    tier = int(rng.choice([1, 2, 3], p=[0.10, 0.30, 0.60]))
    priority = int(rng.integers(0, 101)) if tier == 1 else int(rng.integers(0, 51))
    pacing_deficit = float(rng.uniform(0.0, 1.0)) if tier == 2 else 0.0

    # eCPM: int64 minor-units (TX-2). Para Remnant, sorteio plausivel em centavos.
    ecpm_minor_units: int = int(rng.integers(50, 2000)) if tier == 3 else 0
    candidate_count = int(rng.integers(1, 21))

    # Historico de campanha (snapshot aproximado)
    delivered_impressions = int(rng.integers(0, 100_001))
    goal_impressions = int(rng.integers(500, 50_001)) if tier == 2 else 0
    delivered_clicks = int(min(
        delivered_impressions,
        rng.integers(0, max(1, delivered_impressions // 20 + 1))
    ))

    # Propensidade logada: epsilon-greedy decrescente (placeholder DETERMINISTIC=1.0)
    # Em producao, vem do campo Decision.propensity (propensity-logging.md)
    propensity = 1.0  # DETERMINISTIC antes de ML ativo (J0)

    # Label de CTR sintetico: funcao logistica sobre features-sinal
    # Em producao: JOIN decision_id com Click no Iceberg
    base = _BASE_CTR[tier]
    # Bonus de hora (hora central do dia tem maior CTR)
    hour_bonus = 0.01 * np.sin(np.pi * hour / 23.0)
    # Bonus de fim de semana (ligeiro aumento de CTR)
    weekend_bonus = 0.005 if dow in (0, 6) else 0.0
    # Bonus de match de tamanho (banner == zona)
    bw_bucket = _bucketize(banner_w, _ZONE_WIDTH_BOUNDARIES)
    zw_bucket = _bucketize(zone_w, _ZONE_WIDTH_BOUNDARIES)
    bh_bucket = _bucketize(banner_h, _ZONE_HEIGHT_BOUNDARIES)
    zh_bucket = _bucketize(zone_h, _ZONE_HEIGHT_BOUNDARIES)
    size_match_bonus = 0.01 if (bw_bucket == zw_bucket and bh_bucket == zh_bucket) else 0.0
    # Penalidade de bot
    bot_penalty = 0.03 if device_class == "bot" else 0.0

    p_click = base + hour_bonus + weekend_bonus + size_match_bonus - bot_penalty
    p_click = float(np.clip(p_click, 0.001, 0.15))
    label_click = int(rng.binomial(1, p_click))

    return {
        "decision_id": str(uuid.uuid4()),
        "zone_width": zone_w,
        "zone_height": zone_h,
        "hour_of_day": hour,
        "day_of_week": dow,
        "geo_country": country,
        "geo_city": city,
        "device_class": device_class,
        "candidate_tier": tier,
        "campaign_priority": priority,
        "pacing_deficit": pacing_deficit,
        "ecpm_minor_units": ecpm_minor_units,  # int64, TX-2
        "banner_width": banner_w,
        "banner_height": banner_h,
        "creative_type": creative_type,
        "candidate_count": candidate_count,
        "delivered_impressions": delivered_impressions,
        "goal_impressions": goal_impressions,
        "delivered_clicks": delivered_clicks,
        "propensity": propensity,
        "model_version": "DETERMINISTIC",
        "feature_spec_version": "1.0.0",
        "label_click": label_click,
    }


def generate(
    n_rows: int = 50_000,
    seed: int = 42,
    output_path: Optional[Path] = None,
) -> pd.DataFrame:
    """
    Gera um DataFrame sintetico de treino/validacao com n_rows linhas.

    Args:
        n_rows: numero de linhas a gerar
        seed:   seed do RNG para reproducibilidade
        output_path: se fornecido, salva como Parquet neste caminho

    Returns:
        DataFrame com o schema descrito acima.

    NOTA DE PRODUCAO:
        Em producao este DataFrame e substituido pela leitura do Iceberg:

            import pyiceberg.catalog
            catalog = pyiceberg.catalog.load_catalog("glue", **cfg)
            table = catalog.load_table("adserver.impressions_clicks_joined")
            df = table.scan(
                row_filter=f"window_start >= '{window_start}' AND window_end < '{window_end}'"
            ).to_pandas()

        O schema de colunas e identico ao produzido aqui.
        A coluna ecpm_minor_units DEVE ser int64 (TX-2).
    """
    rng = np.random.default_rng(seed)
    rows = [_make_row(rng, i) for i in range(n_rows)]
    df = pd.DataFrame(rows)

    # Garantia TX-2: ecpm_minor_units nunca float — forca int64
    df["ecpm_minor_units"] = df["ecpm_minor_units"].astype("int64")
    df["label_click"] = df["label_click"].astype("int8")

    # Filtra decisoes DETERMINISTIC com propensidade 1.0 (sem OPE ainda; nao filtrar neste sample)
    # Em producao: filtrar ml_fail_open=true antes de usar como peso de OPE/IPS

    if output_path is not None:
        output_path = Path(output_path)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        df.to_parquet(str(output_path), index=False, engine="pyarrow")
        print(f"[generate_sample] Salvo {len(df)} linhas em {output_path}")

    return df


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Gera dataset sintetico de treino pCTR")
    parser.add_argument("--n-rows", type=int, default=50_000)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument(
        "--output",
        type=Path,
        default=Path(__file__).parent / "testdata" / "sample_train.parquet",
    )
    args = parser.parse_args()
    df = generate(n_rows=args.n_rows, seed=args.seed, output_path=args.output)
    print(f"[generate_sample] CTR medio: {df['label_click'].mean():.4f}")
    print(f"[generate_sample] Distribuicao de tier:\n{df['candidate_tier'].value_counts().sort_index()}")
