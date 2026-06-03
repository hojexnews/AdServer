"""
data/iceberg/jobs/iceberg_sink_job.py

Esqueleto do job de sink: exporta eventos das tabelas raw_* do ClickHouse
(ou consome diretamente do Redpanda) para as tabelas Iceberg.

PAPEL:
    Garante que o lakehouse Iceberg tenha todos os eventos deduplicados
    antes que o billing batch os consuma. Este job fecha o pipeline:

    Redpanda -> ClickHouse raw_* (via Kafka engine + MV)
                          |
                          v (sink job / export batch)
                    Iceberg events_*  <- FONTE DE VERDADE CONTABIL

ESTRATEGIA DE DEDUPLICACAO NO SINK:
    O ClickHouse raw_* usa ReplacingMergeTree; o sink le com SELECT FINAL
    (que deduplica por event_id) e faz MERGE INTO no Iceberg.
    Alternativa: ler do Redpanda diretamente (sem passar pelo ClickHouse);
    nesse caso a dedupe e feita no job via set(seen_event_ids).

    Para Fase 1 com volume <= 1B eventos/dia (ADR-0002 B.1), a leitura
    via ClickHouse FINAL e suficiente. Sem Flink (ADR-0001).

IDEMPOTENCIA:
    O MERGE INTO do Iceberg usa event_id como chave de upsert.
    Reprocessamento de um intervalo nao duplica eventos.

EXECUCAO (periodica, ex.: a cada 15 minutos via cron/Airflow):
    python iceberg_sink_job.py \
        --since "2026-06-03T14:00:00Z" \
        --until "2026-06-03T14:15:00Z" \
        --table impression

FRONTEIRA: data/ apenas. Nao toca em db/ nem em services/.
"""

from __future__ import annotations

import argparse
import logging
import os
from datetime import datetime, timezone

log = logging.getLogger("iceberg_sink_job")
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")

SUPPORTED_TABLES = ("ad_request", "impression", "click", "conversion", "decision")


def sink_table(
    table: str,
    since: datetime,
    until: datetime,
    clickhouse_uri: str,
    iceberg_catalog_uri: str,
    batch_size: int = 50_000,
    dry_run: bool = False,
) -> int:
    """
    Exporta linhas de raw_{table} do ClickHouse para adserver.events_{table} no Iceberg.
    Usa SELECT FINAL para dedupe. Retorna numero de linhas gravadas.

    INVARIANTE: conversion_value_decimal exportado como Decimal, nunca float.
    """
    log.info(
        "sink_table: table=%s since=%s until=%s dry_run=%s",
        table, since.isoformat(), until.isoformat(), dry_run,
    )

    # Query ClickHouse com FINAL (dedupe) e filtro de janela de tempo
    # TODO(data-platform): implementar com clickhouse-connect ou httpx
    # query = f"""
    #   SELECT *
    #   FROM adserver.raw_{table} FINAL
    #   WHERE occurred_at >= '{since.isoformat()}'
    #     AND occurred_at <  '{until.isoformat()}'
    #   ORDER BY occurred_at, event_id
    # """
    # rows = clickhouse_client.query(query).result_rows

    # Escrita no Iceberg via MERGE INTO (upsert por event_id)
    # TODO(data-platform): from pyiceberg.catalog import load_catalog
    # catalog = load_catalog("adserver", **{"uri": iceberg_catalog_uri})
    # table_ref = catalog.load_table(f"adserver.events_{table}")
    # Batch de rows em Arrow -> table_ref.merge_into(...)
    #   .when_matched_update_all()
    #   .when_not_matched_insert_all()
    #   .merge()

    rows_count = 0  # placeholder
    log.info("sink_table: %d linhas gravadas no Iceberg (esqueleto).", rows_count)
    return rows_count


def main() -> None:
    p = argparse.ArgumentParser(description="Sink ClickHouse raw_* -> Iceberg events_*")
    p.add_argument("--since", required=True, help="ISO 8601 UTC inicio da janela.")
    p.add_argument("--until", required=True, help="ISO 8601 UTC fim da janela.")
    p.add_argument(
        "--table",
        required=True,
        choices=SUPPORTED_TABLES,
        help="Tabela a exportar.",
    )
    p.add_argument("--batch-size", type=int, default=50_000)
    p.add_argument("--dry-run", action="store_true")
    p.add_argument(
        "--clickhouse-uri",
        default=os.environ.get("CLICKHOUSE_URI", "http://localhost:8123"),
    )
    p.add_argument(
        "--catalog-uri",
        default=os.environ.get("ICEBERG_CATALOG_URI", "http://localhost:8181"),
    )
    args = p.parse_args()

    since = datetime.fromisoformat(args.since.replace("Z", "+00:00"))
    until = datetime.fromisoformat(args.until.replace("Z", "+00:00"))

    sink_table(
        table=args.table,
        since=since,
        until=until,
        clickhouse_uri=args.clickhouse_uri,
        iceberg_catalog_uri=args.catalog_uri,
        batch_size=args.batch_size,
        dry_run=args.dry_run,
    )


if __name__ == "__main__":
    main()
