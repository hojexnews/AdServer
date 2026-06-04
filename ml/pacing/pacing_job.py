"""
ml/pacing/pacing_job.py
========================
Job near-line de pacing: le campanhas ativas, calcula deficit proporcional
e escreve o resultado no Redis de pacing.

MODO DE OPERACAO (near-line, nao hot path):
  Este job e chamado periodicamente (ex.: a cada 5 min) pelo scheduler
  de infraestrutura (cron, Temporal, etc.). NAO e invocado no hot path
  de decisao (DA-4: pacing e computacao near-line, nao inline).

FLUXO:
  1. Le campanhas Contract ativas de alguma fonte (Postgres/Redis snapshot).
  2. Para cada campanha, le o historico horario de StatsHourly (ClickHouse).
  3. Calcula pacing_deficit via ProportionalPacingController.
  4. Escreve pacing_deficit no Redis com TTL.

INTEGRACAO COM A CASCATA:
  O motor de decisao (services/decision) le pacing:deficit:{campaign_id}
  do Redis e popula cascade.Candidate.PacingDeficit. Este numero e o mesmo
  que aparece na feature index 12 da feature_spec.yaml.

USO (linha de comando, para testes/manobra manual):
  python pacing_job.py --dry-run  # imprime deficit sem escrever Redis
"""
from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import List, Optional

# Resolve repo root
_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.pacing.controller import (  # noqa: E402
    CampaignPacingState,
    ProportionalPacingController,
    load_hourly_history_from_stats,
    write_pacing_deficits_to_redis,
)


def run_pacing_cycle(
    campaign_states: List[CampaignPacingState],
    clickhouse_client=None,
    redis_client=None,
    kp: float = 1.0,
    dry_run: bool = False,
) -> dict:
    """
    Executa um ciclo de calculo de pacing para uma lista de campanhas.

    Args:
        campaign_states:   estados de campanha (sem historico ainda)
        clickhouse_client: cliente ClickHouse para buscar historico (opcional)
        redis_client:      cliente Redis para escrever deficits (opcional)
        kp:                ganho proporcional
        dry_run:           se True, nao escreve no Redis

    Retorna dict com os PacingAdjustments calculados (campaign_id -> dict).
    """
    controller = ProportionalPacingController(kp=kp)

    # Enriquece estados com historico de StatsHourly se ClickHouse disponivel
    if clickhouse_client is not None:
        enriched = []
        for s in campaign_states:
            history = load_hourly_history_from_stats(
                campaign_id=s.campaign_id,
                clickhouse_client=clickhouse_client,
                lookback_hours=48,
            )
            enriched.append(CampaignPacingState(
                campaign_id=s.campaign_id,
                goal_impressions=s.goal_impressions,
                delivered_impressions=s.delivered_impressions,
                flight_start_utc=s.flight_start_utc,
                flight_end_utc=s.flight_end_utc,
                now_utc=s.now_utc,
                forecast_hourly_impressions=history or s.forecast_hourly_impressions,
            ))
        campaign_states = enriched

    adjustments = controller.compute_batch(campaign_states)

    # Escreve no Redis (nao em dry_run)
    if not dry_run and redis_client is not None:
        write_pacing_deficits_to_redis(adjustments, redis_client)
    elif dry_run:
        for cid, adj in adjustments.items():
            print(f"[dry-run] {cid}: pacing_deficit={adj.pacing_deficit:.4f} "
                  f"adjustment_factor={adj.adjustment_factor:.4f}")

    return {cid: {
        "pacing_deficit": adj.pacing_deficit,
        "adjustment_factor": adj.adjustment_factor,
        "expected_delivery_fraction": adj.expected_delivery_fraction,
        "actual_delivery_fraction": adj.actual_delivery_fraction,
        "forecast_remaining_impressions": adj.forecast_remaining_impressions,
        "computed_at_utc": adj.computed_at_utc,
    } for cid, adj in adjustments.items()}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Job near-line de pacing proporcional (DA-4)")
    parser.add_argument("--dry-run", action="store_true",
                        help="Calcula sem escrever no Redis")
    parser.add_argument("--kp", type=float, default=1.0,
                        help="Ganho proporcional (padrao 1.0)")
    args = parser.parse_args()

    print(f"[pacing_job] dry_run={args.dry_run} kp={args.kp}")
    print("[pacing_job] MODO DEMO: sem ClickHouse/Redis reais neste ambiente.")
    print("[pacing_job] Em producao, injetar clickhouse_client e redis_client.")

    # Demo com estado sintetico
    now = datetime.now(timezone.utc)
    demo_states = [
        CampaignPacingState(
            campaign_id="demo-camp-001",
            goal_impressions=100_000,
            delivered_impressions=30_000,
            flight_start_utc=now.replace(hour=0, minute=0, second=0, microsecond=0),
            flight_end_utc=now.replace(hour=23, minute=59, second=59, microsecond=0),
            now_utc=now,
            forecast_hourly_impressions=[1500, 1600, 1400, 1700, 1800] * 8,
        ),
    ]
    result = run_pacing_cycle(demo_states, dry_run=True)
    print(json.dumps(result, indent=2))
