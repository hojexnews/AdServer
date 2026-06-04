"""
ml/deep/generate_deep_sample.py
================================
Gera sample sintetico para treino do deep ranker (K1).

NOTA DE INFRA: dados reais vem do lakehouse Iceberg (pendencia de infra
da Fase 2 — cutover bloqueado neste ambiente). Este gerador produz vetores
de features validos conforme spec 1.0.0 usando ml/features/python/featurize.py
(anti-skew: mesma funcao de featurizacao usada em serving e treino GBDT).

Nao usa PII (TX-5/DA-11): todas as features sao sinteticas e PII-free,
seguindo as mesmas restricoes documentadas em ml/features/spec/feature_spec.yaml.

Uso:
    python generate_deep_sample.py [--n-rows 10000] [--output-path PATH]
"""
from __future__ import annotations

import argparse
import os
import random
import sys
from pathlib import Path

import numpy as np
import pandas as pd

# Adiciona raiz do repo ao path para importar ml.features
_REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(_REPO_ROOT))

from ml.features.python.featurize import (
    CandidateInput,
    FeaturizeInput,
    GeoInput,
    RequestTimeInput,
    ZoneInput,
    featurize,
)

# Semente reproducivel (dados sinteticos devem ser deterministas)
_RNG_SEED = 42

# Valores possiveis para features categoricas (subset realista)
_COUNTRIES = ["BR", "US", "MX", "AR", "PT", ""]
_CITIES = ["Sao Paulo", "Rio de Janeiro", "Brasilia", "New York", ""]
_DEVICE_CLASSES = ["chrome-mobile", "chrome-desktop", "safari-mobile",
                   "safari-desktop", "bot", "other"]
_CREATIVE_TYPES = ["image", "html", "video"]
_TIERS = [1, 2, 3]  # Override=1, Contract=2, Remnant=3

# Dimensoes comuns de zonas e banners (IAB)
_ZONE_SIZES = [(300, 250), (728, 90), (320, 50), (160, 600), (970, 250)]
_BANNER_SIZES = [(300, 250), (728, 90), (320, 50), (160, 600), (970, 250),
                 (300, 600), (250, 250)]


def _make_synthetic_row(rng: random.Random) -> dict:
    """Gera uma linha sintetica com features e label simulado."""
    zone_w, zone_h = rng.choice(_ZONE_SIZES)
    banner_w, banner_h = rng.choice(_BANNER_SIZES)
    tier = rng.choice(_TIERS)
    ecpm_minor = rng.randint(0, 500_00) if tier == 3 else 0  # Remnant tem eCPM
    pacing_deficit = rng.uniform(0.0, 1.0) if tier == 2 else 0.0
    priority = rng.randint(0, 100)
    candidate_count = rng.randint(1, 20)
    delivered_imp = rng.randint(0, 100_000)
    goal_imp = rng.randint(0, 200_000) if tier == 2 else 0
    delivered_clicks = int(delivered_imp * rng.uniform(0.001, 0.05))

    inp = FeaturizeInput(
        zone=ZoneInput(width=zone_w, height=zone_h),
        request_time_utc=RequestTimeInput(
            hour=rng.randint(0, 23),
            day_of_week=rng.randint(0, 6),
        ),
        geo=GeoInput(
            country=rng.choice(_COUNTRIES),
            city=rng.choice(_CITIES),
        ),
        device_class=rng.choice(_DEVICE_CLASSES),
        candidate=CandidateInput(
            tier=tier,
            priority=priority,
            pacing_deficit=pacing_deficit,
            ecpm_minor_units=ecpm_minor,
            banner_width=banner_w,
            banner_height=banner_h,
            creative_type=rng.choice(_CREATIVE_TYPES),
        ),
        candidate_count=candidate_count,
        campaign_delivered_impressions=delivered_imp,
        campaign_goal_impressions=goal_imp,
        campaign_delivered_clicks=delivered_clicks,
    )

    vec = featurize(inp)  # float32 [23] — anti-skew: funcao unica

    # Label sintetico: simula pCTR com sinal fraco baseado em features relevantes
    # (nao e um modelo real — apenas para viabilizar o treino do scaffolding)
    base_ctr = 0.02
    if vec[18] > 0:        # banner_size_match
        base_ctr += 0.01
    if vec[19] in (1, 3):  # creative_type: image ou video
        base_ctr += 0.005
    if vec[5] > 0:         # is_weekend
        base_ctr += 0.003
    ctr_noise = rng.gauss(0, 0.005)
    true_pctr = float(np.clip(base_ctr + ctr_noise, 0.0, 1.0))
    label_click = int(rng.random() < true_pctr)

    row = {"label_click": label_click, "true_pctr": true_pctr}
    for i, v in enumerate(vec.tolist()):
        row[f"f{i}"] = v
    return row


def generate(n_rows: int, output_path: Path, seed: int = _RNG_SEED) -> None:
    """Gera n_rows linhas sinteticas e salva em Parquet."""
    rng = random.Random(seed)
    rows = [_make_synthetic_row(rng) for _ in range(n_rows)]
    df = pd.DataFrame(rows)

    output_path.parent.mkdir(parents=True, exist_ok=True)
    df.to_parquet(output_path, index=False)
    print(f"[generate_deep_sample] {n_rows} linhas em {output_path}")
    print(f"  label_click rate: {df['label_click'].mean():.4f}")
    print(f"  features: f0..f22 (spec {from_spec_version()}), "
          f"shape {df.shape}")


def from_spec_version() -> str:
    """Retorna a versao da feature spec usada neste gerador."""
    from ml.features.python.featurize import FEATURE_SPEC_VERSION
    return FEATURE_SPEC_VERSION


def main() -> None:
    parser = argparse.ArgumentParser(description="Gera sample sintetico para deep ranker K1.")
    parser.add_argument("--n-rows", type=int, default=10_000,
                        help="Numero de linhas a gerar (default: 10000).")
    parser.add_argument("--output-path", type=Path,
                        default=Path(__file__).parent / "testdata" / "sample_deep.parquet",
                        help="Caminho de saida do Parquet.")
    parser.add_argument("--seed", type=int, default=_RNG_SEED,
                        help="Semente aleatoria (default: 42).")
    args = parser.parse_args()
    generate(args.n_rows, args.output_path, args.seed)


if __name__ == "__main__":
    main()
