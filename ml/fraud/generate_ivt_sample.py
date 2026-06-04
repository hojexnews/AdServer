"""
ml/fraud/generate_ivt_sample.py
================================
Gerador de dataset sintetico supervisionado para treino do classificador IVT.

PRODUCAO:
  Substitua este gerador pela leitura do Iceberg:
    tabela: adserver.raw_impressions (com ivt_status anotado por regras)
    JOIN com StatsHourly para metricas horarias de contexto.

  A label ivt_label (0=clean, 1=IVT) em producao vem de:
    - Regras deterministicas pre-existentes (is_bot, IP de lista negra — fora
      do hot path) que populam raw_impression.ivt_status na ingestao.
    - O modelo supervisionado aprende a generalizar alem das regras.
    - Reconciliacao e batch contra Iceberg (TX-6): impressoes marcadas IVT
      sao excluidas do StatsHourly e do faturamento.

SCHEMA DO DATASET:
  Colunas brutas para construir IVTEventInput + label:
    device_class         string  classe coarsa de UA
    hour_of_day          int     [0, 23] UTC
    day_of_week          int     [0, 6]
    geo_country          string  ISO 3166-1 alpha-2
    geo_city             string  nome de cidade
    hourly_requests      int     requests da campanha/zona na hora
    hourly_impressions   int     impressoes da campanha/zona na hora
    hourly_clicks        int     clicks da campanha/zona na hora
    recent_imps_*        float   impressoes das ultimas 24h (colunas h0..h23)
    ivt_label            int     0=clean, 1=IVT

PRIVACIDADE (TX-5/DA-11): sem IP bruto, sem ID de usuario, sem UA completo.
DINHEIRO (TX-2): sem campos monetarios neste dataset.
"""
from __future__ import annotations

import sys
from pathlib import Path
from typing import Optional

import numpy as np
import pandas as pd

_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

_COUNTRIES = ["BR", "US", "PT", "AR", "MX", "DE", "FR", "GB", "ES", "IT"]
_DEVICE_CLASSES = [
    "chrome-desktop", "chrome-mobile", "safari-mobile", "safari-desktop",
    "firefox-desktop", "edge-desktop", "other-mobile", "other-desktop", "bot",
]
_DEVICE_WEIGHTS = [0.28, 0.24, 0.14, 0.08, 0.08, 0.05, 0.04, 0.04, 0.05]
# Peso de bot maior que no pCTR (IVT dataset com fracao de fraude maior)


def _make_ivt_row(rng: np.random.Generator) -> dict:
    """
    Gera uma linha sintetica de evento para treino IVT.

    Label sintetica: IVT=1 se device_class=="bot" OU inventario muito baixo
    OU z-score alto — captura os padroes mais simples que o classificador deve aprender.
    """
    device_class = rng.choice(_DEVICE_CLASSES, p=_DEVICE_WEIGHTS)
    country = rng.choice(_COUNTRIES)
    hour = int(rng.integers(0, 24))
    dow = int(rng.integers(0, 7))

    # Volume horario normal
    is_fraud_campaign = rng.random() < 0.15  # 15% de campanhas com trafico fraudulento

    if is_fraud_campaign or device_class == "bot":
        # Trafego fraudulento: volume muito alto ou muito baixo anormalmente
        hourly_requests = int(rng.integers(5000, 50000))
        hourly_impressions = int(rng.integers(100, 1000))  # alta perda de inventario
        hourly_clicks = int(rng.integers(0, 10))
        # Historico recente: media muito menor (pico anormal)
        base_imps = float(rng.integers(50, 200))
    else:
        # Trafego normal
        hourly_requests = int(rng.integers(10, 2000))
        hourly_impressions = int(rng.integers(5, min(hourly_requests, 1500)))
        hourly_clicks = int(rng.integers(0, max(1, hourly_impressions // 20 + 1)))
        base_imps = float(hourly_impressions)

    # Historico das ultimas 24h (para z-score)
    recent = [max(0, int(rng.normal(base_imps, base_imps * 0.2 + 1))) for _ in range(24)]

    # Label IVT:
    # 1 = bot, ou perda de inventario muito alta, ou z-score extremo
    inv_loss = max(0.0, (hourly_requests - hourly_impressions) / max(1, hourly_requests))
    mean_h = float(np.mean(recent)) if recent else 0.0
    std_h = float(np.std(recent)) if len(recent) > 1 else 1.0
    zscore = abs(hourly_impressions - mean_h) / max(1e-3, std_h)

    is_ivt = (
        device_class == "bot"
        or inv_loss > 0.7
        or zscore > 4.0
        or is_fraud_campaign
    )
    ivt_label = int(is_ivt)

    row = {
        "device_class": device_class,
        "hour_of_day": hour,
        "day_of_week": dow,
        "geo_country": country,
        "geo_city": "Unknown",  # cidade irrelevante para IVT neste sample
        "hourly_requests": hourly_requests,
        "hourly_impressions": hourly_impressions,
        "hourly_clicks": hourly_clicks,
        "ivt_label": ivt_label,
    }
    # Historico das ultimas 24h como colunas separadas (h0=mais antigo, h23=mais recente)
    for i, v in enumerate(recent):
        row[f"recent_h{i:02d}"] = v

    return row


def generate_ivt_sample(
    n_rows: int = 30_000,
    seed: int = 42,
    output_path: Optional[Path] = None,
) -> pd.DataFrame:
    """
    Gera dataset sintetico de eventos para treino IVT.

    Retorna DataFrame com schema descrito acima.
    Taxa de IVT sintetica: ~25-30% (superamostrado para treino supervisionado).
    """
    rng = np.random.default_rng(seed)
    rows = [_make_ivt_row(rng) for _ in range(n_rows)]
    df = pd.DataFrame(rows)

    df["ivt_label"] = df["ivt_label"].astype("int8")
    df["hourly_requests"] = df["hourly_requests"].astype("int64")
    df["hourly_impressions"] = df["hourly_impressions"].astype("int64")
    df["hourly_clicks"] = df["hourly_clicks"].astype("int64")

    print(f"[generate_ivt_sample] {len(df)} linhas. "
          f"IVT rate: {df['ivt_label'].mean():.3f}")

    if output_path is not None:
        output_path = Path(output_path)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        df.to_parquet(str(output_path), index=False, engine="pyarrow")
        print(f"[generate_ivt_sample] Salvo em {output_path}")

    return df


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--n-rows", type=int, default=30_000)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument("--output", type=Path,
                        default=Path(__file__).parent / "testdata" / "sample_ivt.parquet")
    args = parser.parse_args()
    generate_ivt_sample(n_rows=args.n_rows, seed=args.seed, output_path=args.output)
