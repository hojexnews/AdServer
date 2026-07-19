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
# Helpers de escopo por-statement (evita tautologia de substring no corpus
# inteiro: um comentario ou uma tabela vizinha nao pode satisfazer um check
# que deveria provar algo sobre UM statement especifico).
# ---------------------------------------------------------------------------
def strip_sql_comments(text: str) -> str:
    """Remove comentarios de linha (--...) de cada linha, preservando quebras."""
    out = []
    for line in text.splitlines():
        idx = line.find("--")
        if idx != -1:
            line = line[:idx]
        out.append(line)
    return "\n".join(out)


def split_statements(code_text: str):
    """Divide DDL (ja sem comentarios) em statements top-level por ';'."""
    return [s.strip() for s in code_text.split(";") if s.strip()]


def find_statement(statements, pattern: str):
    """Retorna o primeiro statement cujo inicio (apos strip) casa com pattern."""
    for s in statements:
        if re.match(pattern, s, re.I):
            return s
    return None


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

ddl_code = strip_sql_comments(ddl_text)
ddl_statements = split_statements(ddl_code)

# ---------------------------------------------------------------------------
# Verifica que a VIEW stats_hourly existe, escopado ao statement real
# (nao satisfeito por 'stats_hourly_state' nem por comentarios/prosa)
# ---------------------------------------------------------------------------
stats_hourly_view = find_statement(
    ddl_statements,
    r"CREATE\s+VIEW\s+IF\s+NOT\s+EXISTS\s+adserver\.stats_hourly\b",
)
if stats_hourly_view is None:
    print(
        "ERRO: VIEW adserver.stats_hourly (statement CREATE VIEW real, nao "
        "prosa/comentario) nao encontrada em DDL do ClickHouse.",
        file=sys.stderr,
    )
    fail = 1

# ---------------------------------------------------------------------------
# Verifica colunas obrigatorias DENTRO do statement da VIEW stats_hourly
# (nao satisfeito por colunas de outras tabelas/MVs)
# ---------------------------------------------------------------------------
if stats_hourly_view is not None:
    missing = [c for c in REQUIRED_STATS_COLS if c not in stats_hourly_view]
    if missing:
        print(f"ERRO: colunas faltando no contrato StatsHourly (VIEW stats_hourly): {missing}", file=sys.stderr)
        fail = 1

# ---------------------------------------------------------------------------
# Verifica que stats_hourly_state usa AggregatingMergeTree (DA-7), escopado
# ao statement CREATE TABLE de stats_hourly_state (nao satisfeito por prosa
# em outros arquivos nem por outra tabela com esse engine)
# ---------------------------------------------------------------------------
stats_hourly_state_table = find_statement(
    ddl_statements,
    r"CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+adserver\.stats_hourly_state\b",
)
if stats_hourly_state_table is None:
    print("ERRO: tabela adserver.stats_hourly_state nao encontrada (DA-7).", file=sys.stderr)
    fail = 1
elif not re.search(r"ENGINE\s*=\s*AggregatingMergeTree\b", stats_hourly_state_table, re.I):
    print(
        "ERRO: adserver.stats_hourly_state nao usa ENGINE = AggregatingMergeTree "
        "(DA-7). StatsHourly exige AggregatingMergeTree na tabela de estado.",
        file=sys.stderr,
    )
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
# Verifica que row-policies NAO usam replaceAll() para extracao de tenant
# (MEDIUM #5: replaceAll remove todas as ocorrencias, vulnerabilidade cross-tenant)
# ---------------------------------------------------------------------------
for line in ddl_text.splitlines():
    stripped = line.strip()
    if stripped.startswith("--"):
        continue
    # Detectar uso de replaceAll em contexto de row policy (USING clause)
    if re.search(r"\breplaceAll\s*\(\s*currentUser\(\)", stripped, re.I):
        print(
            f"ERRO: row-policy usa replaceAll(currentUser(),...) — vulnerabilidade "
            f"cross-tenant (MEDIUM #5). Usar substring()+match(UUID) com fail-closed. "
            f"Linha: {stripped}",
            file=sys.stderr
        )
        fail = 1

# ---------------------------------------------------------------------------
# Verifica que row-policies usam o padrao correto: substring + match UUID
# (fail-closed: sufixo invalido retorna NULL, nao casa nenhuma linha)
# ---------------------------------------------------------------------------
has_substring_uuid = (
    "startsWith(currentUser(), 'tenant_')" in ddl_text
    and "match(" in ddl_text
    and "[0-9a-f]{8}-[0-9a-f]{4}" in ddl_text
    and "substring(currentUser()," in ddl_text
)
if "ROW POLICY" in ddl_text.upper() and not has_substring_uuid:
    print(
        "ERRO: row-policies existem mas nao usam o padrao fail-closed "
        "(startsWith + match UUID + substring). Risco de extracao incorreta "
        "de tenant_id em nomes de usuario nao-canonicos.",
        file=sys.stderr
    )
    fail = 1

# ---------------------------------------------------------------------------
# Verifica que a tabela mais critica (stats_hourly_state, base da VIEW
# faturavel stats_hourly) tem SUA PROPRIA row-policy de tenant fail-closed
# (TX-3). Escopado ao statement: a presenca global de "ROW POLICY" (check
# acima) e satisfeita por QUALQUER uma das 6 policies de tenant; este check
# prova que especificamente a de stats_hourly_state nao foi removida.
# ---------------------------------------------------------------------------
stats_hourly_state_tenant_policy = None
for _s in ddl_statements:
    if (
        re.match(r"CREATE\s+ROW\s+POLICY\s+IF\s+NOT\s+EXISTS\b", _s, re.I)
        and re.search(r"\bON\s+adserver\.stats_hourly_state\b", _s, re.I)
        and re.search(r"\bTO\s+tenant_role\b", _s, re.I)
    ):
        stats_hourly_state_tenant_policy = _s
        break

if stats_hourly_state_tenant_policy is None:
    print(
        "ERRO: row-policy de tenant para adserver.stats_hourly_state (TX-3) "
        "nao encontrada. A tabela base da VIEW stats_hourly faturavel precisa "
        "de sua PROPRIA ROW POLICY ... ON adserver.stats_hourly_state ... TO tenant_role.",
        file=sys.stderr,
    )
    fail = 1
else:
    has_fail_closed_scoped = (
        "startsWith(currentUser(), 'tenant_')" in stats_hourly_state_tenant_policy
        and "match(" in stats_hourly_state_tenant_policy
        and "[0-9a-f]{8}-[0-9a-f]{4}" in stats_hourly_state_tenant_policy
        and "substring(currentUser()," in stats_hourly_state_tenant_policy
    )
    if not has_fail_closed_scoped:
        print(
            "ERRO: row-policy de tenant em adserver.stats_hourly_state existe "
            "mas nao usa o padrao fail-closed (startsWith + match UUID + substring).",
            file=sys.stderr,
        )
        fail = 1

# ---------------------------------------------------------------------------
# Invariantes de UA: contrato produtor Go <-> ingestao ClickHouse (HIGH privacy, TX-5)
#
# Campo proto `user_agent` (no 5, wire-locked BACKWARD, TX-1) carrega a CLASSE coarse.
# Regras:
#   (A) 002_raw_tables.sql NAO pode ter coluna `user_agent` sem `_class`
#       (garantia de que o UA bruto nunca persiste na raw).
#   (B) 001_kafka_engines.sql DEVE ter coluna `user_agent` (sem `_class`)
#       para casar o campo proto no mapeamento Protobuf->ClickHouse por nome.
#   (C) 003_kafka_to_raw_mvs.sql DEVE projetar `user_agent AS user_agent_class`
#       (a MV e o ponto de traducao nome-proto -> nome-semantico).
# ---------------------------------------------------------------------------

# (A) raw_tables (002): sem coluna `user_agent` crua
raw_tables_ddl = ""
raw_file = CH_DDL_DIR / "002_raw_tables.sql"
if raw_file.exists():
    raw_tables_ddl = raw_file.read_text()

for line in raw_tables_ddl.splitlines():
    stripped = line.strip()
    if stripped.startswith("--"):
        continue
    # Definicao de coluna: "user_agent  String" ou "user_agent   LowCardinality"
    # mas NAO "user_agent_class ..."
    if re.match(r"user_agent\s+(String|LowCardinality)", stripped, re.I):
        print(
            f"ERRO (A) UA: coluna 'user_agent' (UA bruto) encontrada em 002_raw_tables.sql "
            f"— a raw deve usar 'user_agent_class' (classe coarse, sem PII, TX-5). "
            f"Linha: {stripped}",
            file=sys.stderr
        )
        fail = 1

# (B) kafka_engines (001): DEVE ter coluna `user_agent` (casa o campo proto no 5)
kafka_engines_ddl = ""
kafka_file = CH_DDL_DIR / "001_kafka_engines.sql"
if kafka_file.exists():
    kafka_engines_ddl = kafka_file.read_text()
    # Procura linha de definicao de coluna `user_agent` (sem _class) na kafka_ad_request
    found_proto_col = False
    for line in kafka_engines_ddl.splitlines():
        stripped = line.strip()
        if stripped.startswith("--"):
            continue
        if re.match(r"user_agent\s+(String|LowCardinality)", stripped, re.I):
            found_proto_col = True
            break
    if not found_proto_col:
        print(
            "ERRO (B) UA: 001_kafka_engines.sql nao tem coluna 'user_agent' na kafka-engine. "
            "O mapeamento Protobuf->ClickHouse por nome exige que a coluna de ingestao "
            "se chame 'user_agent' (campo proto no 5, wire-locked, TX-1). "
            "Sem essa coluna a ingestao Protobuf resulta em coluna VAZIA.",
            file=sys.stderr
        )
        fail = 1
else:
    print("AVISO: 001_kafka_engines.sql nao encontrado em data/clickhouse/migrations/.")

# (C) kafka_to_raw_mvs (003): DEVE projetar `user_agent AS user_agent_class`
mvs_ddl = ""
mvs_file = CH_DDL_DIR / "003_kafka_to_raw_mvs.sql"
if mvs_file.exists():
    mvs_ddl = mvs_file.read_text()
    # Procura a projecao de traducao (ignora comentarios)
    found_projection = False
    for line in mvs_ddl.splitlines():
        stripped = line.strip()
        if stripped.startswith("--"):
            continue
        if re.search(r"\buser_agent\s+AS\s+user_agent_class\b", stripped, re.I):
            found_projection = True
            break
    if not found_projection:
        print(
            "ERRO (C) UA: 003_kafka_to_raw_mvs.sql nao projeta 'user_agent AS user_agent_class'. "
            "A MV de ingestao deve traduzir o nome do campo proto para o nome semantico da raw. "
            "Sem essa projecao, a coluna user_agent_class em raw_ad_request ficaria VAZIA.",
            file=sys.stderr
        )
        fail = 1
else:
    print("AVISO: 003_kafka_to_raw_mvs.sql nao encontrado em data/clickhouse/migrations/.")

# ---------------------------------------------------------------------------
# Verifica que ReplacingMergeTree e usado para dedupe por event_id (TX-1),
# escopado ao statement CREATE TABLE de CADA tabela raw_* (nao satisfeito
# por prosa/diagrama em comentarios nem por outra tabela com esse engine)
# ---------------------------------------------------------------------------
RAW_TABLES_REQUIRING_DEDUPE = (
    "raw_ad_request", "raw_impression", "raw_click", "raw_conversion", "raw_decision",
)
for _tbl in RAW_TABLES_REQUIRING_DEDUPE:
    _stmt = find_statement(
        ddl_statements,
        rf"CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+adserver\.{_tbl}\b",
    )
    if _stmt is None:
        print(f"ERRO: tabela adserver.{_tbl} nao encontrada (dedupe TX-1).", file=sys.stderr)
        fail = 1
    elif not re.search(r"ENGINE\s*=\s*ReplacingMergeTree\b", _stmt, re.I):
        print(
            f"ERRO: adserver.{_tbl} nao usa ENGINE = ReplacingMergeTree; "
            f"necessario para dedupe por event_id (TX-1).",
            file=sys.stderr,
        )
        fail = 1

if fail == 0:
    print("data-schema-invariants: ok")
sys.exit(fail)
