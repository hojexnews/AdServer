#!/usr/bin/env python3
"""
scripts/ci/data-schema-invariants.py
Verifica invariantes de schema do StatsHourly e das specs de billing no Iceberg.
Roda sem ClickHouse em execucao (analise estatica de DDL).
"""
import pathlib
import re
import sys

CH_DDL_DIR = pathlib.Path("data/clickhouse/migrations")
ICEBERG_SPEC_DIR = pathlib.Path("data/iceberg/specs")

# Colunas obrigatorias do contrato StatsHourly (§4.1 da documentacao tecnica / CA-6)
REQUIRED_STATS_COLS = {
    "hour_bucket", "campaign_id", "banner_id", "zone_id",
    "requests", "impressions", "clicks", "conversions",
    "conversion_value", "currency",
}

fail = 0

# ---------------------------------------------------------------------------
# Le todos os DDLs de ClickHouse
# ---------------------------------------------------------------------------
ddl_files = sorted(CH_DDL_DIR.glob("*.sql"))
if not ddl_files:
    print("ERRO: nenhum DDL SQL encontrado em data/clickhouse/migrations/", file=sys.stderr)
    sys.exit(1)

ddl_text = ""
for f in ddl_files:
    ddl_text += f.read_text()

# ---------------------------------------------------------------------------
# Verifica que a VIEW stats_hourly existe
# ---------------------------------------------------------------------------
if "stats_hourly" not in ddl_text:
    print("ERRO: VIEW stats_hourly nao encontrada em DDL do ClickHouse.", file=sys.stderr)
    fail = 1

# ---------------------------------------------------------------------------
# Verifica colunas obrigatorias na VIEW/tabela stats_hourly
# ---------------------------------------------------------------------------
missing = [c for c in REQUIRED_STATS_COLS if c not in ddl_text]
if missing:
    print(f"ERRO: colunas faltando no contrato StatsHourly: {missing}", file=sys.stderr)
    fail = 1

# ---------------------------------------------------------------------------
# Verifica que stats_hourly_state usa AggregatingMergeTree (DA-7)
# ---------------------------------------------------------------------------
if "AggregatingMergeTree" not in ddl_text:
    print("ERRO: AggregatingMergeTree nao encontrado; StatsHourly exige AggregatingMergeTree.", file=sys.stderr)
    fail = 1

# ---------------------------------------------------------------------------
# Verifica que conversion_value nao usa Float em colunas monetarias (TX-2)
# ---------------------------------------------------------------------------
for line in ddl_text.splitlines():
    stripped = line.strip()
    if stripped.startswith("--"):
        continue
    if "conversion_value" in stripped.lower() and re.search(r"\bFloat(32|64)\b", stripped, re.I):
        print(f"ERRO: conversion_value usa Float (TX-2 violado): {stripped}", file=sys.stderr)
        fail = 1

# ---------------------------------------------------------------------------
# Verifica que billing_hourly.yaml usa decimal(38, 18) para valores monetarios
# ---------------------------------------------------------------------------
billing_yaml_path = ICEBERG_SPEC_DIR / "billing_hourly.yaml"
if billing_yaml_path.exists():
    billing_text = billing_yaml_path.read_text()
    if "decimal(38, 18)" not in billing_text and "Decimal(38,18)" not in billing_text:
        print("ERRO: billing_hourly.yaml nao encontrou decimal(38, 18) para valores monetarios.", file=sys.stderr)
        fail = 1
    # Garantir que 'float' nao aparece em campos monetarios do billing YAML
    for line in billing_text.splitlines():
        stripped = line.strip()
        if stripped.startswith("#"):
            continue
        if re.search(r"type:\s*float", stripped, re.I):
            if re.search(r"(value|amount|rate|decimal)", stripped, re.I):
                print(f"ERRO: tipo float em campo monetario do billing_hourly.yaml: {stripped}", file=sys.stderr)
                fail = 1
else:
    print("AVISO: billing_hourly.yaml nao encontrado em data/iceberg/specs/.")

# ---------------------------------------------------------------------------
# Verifica que live_stats / ao_vivo esta rotulado como NAO-FATURAVEL (ADR-0001)
# ---------------------------------------------------------------------------
if "NAO-FATURAVEL" not in ddl_text:
    print("ERRO: live_stats sem rotulo NAO-FATURAVEL detectado no DDL. ADR-0001 exige rotulacao.", file=sys.stderr)
    fail = 1

if "live" not in ddl_text.lower():
    print("AVISO: nenhuma visao 'live' encontrada no DDL do ClickHouse.", file=sys.stderr)

# ---------------------------------------------------------------------------
# Verifica que row-policies por tenant_id existem (TX-3)
# ---------------------------------------------------------------------------
if "ROW POLICY" not in ddl_text.upper():
    print("ERRO: row-policies por tenant_id (TX-3) nao encontradas no DDL.", file=sys.stderr)
    fail = 1

# ---------------------------------------------------------------------------
# Verifica que ReplacingMergeTree e usado para dedupe (TX-1)
# ---------------------------------------------------------------------------
if "ReplacingMergeTree" not in ddl_text:
    print("ERRO: ReplacingMergeTree nao encontrado; necessario para dedupe por event_id (TX-1).", file=sys.stderr)
    fail = 1

if fail == 0:
    print("data-schema-invariants: ok")
sys.exit(fail)
