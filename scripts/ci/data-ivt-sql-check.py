#!/usr/bin/env python3
"""
scripts/ci/data-ivt-sql-check.py
Verifica invariantes da migration IVT (007_ivt_scoring.sql) sem ClickHouse.

Checks:
  1. Migration 007 existe e tem CREATE TABLE raw_ivt_score.
  2. raw_ivt_score tem tenant_id (isolamento TX-3).
  3. raw_ivt_score tem ivt_status (resultado do scoring TX-6).
  4. raw_ivt_score tem ivt_prob declarado com comentario 'no-float-ok'
     (probabilidade ML, nao dinheiro — TX-2 excecao explícita).
  5. MV mv_ivt_score_to_raw_impression existe (propaga ivt_status para raw_impression).
  6. MV filtra WHERE ivt_status = 'ivt' (propaga apenas eventos IVT).
  7. Row-policy rp_raw_ivt_score_tenant existe (TX-3, isolamento de tenant).
  8. Row-policy usa padrao fail-closed (substring + match UUID), nao replaceAll.
  9. raw_impression_clean VIEW existe (alias de leitura).
  10. Nenhum float em colunas monetarias na migration 007 (TX-2).
  11. Invariante StatsHourly preservado: 004_stats_hourly.sql ainda filtra
      ivt_status = '' (nao foi alterado por esta migration).
  12a. Tenant isolation test ainda cobre raw_impression (ERRO se ausente).
  12b. Tenant isolation test cobre raw_ivt_score (ERRO se ausente — TX-3).
"""
import pathlib
import re
import sys

CH_DDL_DIR = pathlib.Path("data/clickhouse/migrations")

fail = 0

# ---------------------------------------------------------------------------
# Carrega todos os DDLs
# ---------------------------------------------------------------------------
ddl_files = sorted(CH_DDL_DIR.glob("*.sql"))
ddl_all = ""
for f in ddl_files:
    ddl_all += f.read_text()

# Carrega migration 007 especificamente
migration_007_path = CH_DDL_DIR / "007_ivt_scoring.sql"
if not migration_007_path.exists():
    print("ERRO: 007_ivt_scoring.sql nao encontrado em data/clickhouse/migrations/",
          file=sys.stderr)
    sys.exit(1)

ddl_007 = migration_007_path.read_text()

# Carrega migration 004 para verificar que nao foi alterada
migration_004_path = CH_DDL_DIR / "004_stats_hourly.sql"
ddl_004 = migration_004_path.read_text() if migration_004_path.exists() else ""

# ---------------------------------------------------------------------------
# Check 1: raw_ivt_score existe
# ---------------------------------------------------------------------------
if "raw_ivt_score" not in ddl_007:
    print("ERRO [1]: tabela raw_ivt_score nao encontrada em 007_ivt_scoring.sql",
          file=sys.stderr)
    fail = 1
else:
    print("ok [1]: raw_ivt_score definida em 007_ivt_scoring.sql")

# ---------------------------------------------------------------------------
# Check 2: raw_ivt_score tem tenant_id (isolamento TX-3)
# ---------------------------------------------------------------------------
if "tenant_id" not in ddl_007:
    print("ERRO [2]: tenant_id nao encontrado em 007_ivt_scoring.sql (TX-3 violado)",
          file=sys.stderr)
    fail = 1
else:
    print("ok [2]: tenant_id presente em raw_ivt_score (TX-3)")

# ---------------------------------------------------------------------------
# Check 3: raw_ivt_score tem ivt_status (TX-6)
# ---------------------------------------------------------------------------
if "ivt_status" not in ddl_007:
    print("ERRO [3]: ivt_status nao encontrado em 007_ivt_scoring.sql (TX-6 violado)",
          file=sys.stderr)
    fail = 1
else:
    print("ok [3]: ivt_status presente em raw_ivt_score (TX-6)")

# ---------------------------------------------------------------------------
# Check 4: ivt_prob com marcador no-float-ok (excecao explícita ao TX-2)
# ---------------------------------------------------------------------------
# A coluna ivt_prob e Float64 (probabilidade ML, nao dinheiro).
# O marcador 'no-float-ok' e obrigatorio para o no-float-data-sql.sh nao rejeitar.
found_ivt_prob_with_marker = False
for line in ddl_007.splitlines():
    stripped = line.strip()
    if stripped.startswith("--"):
        continue
    # Procura linha que declara ivt_prob como Float e tem o comentario no-float-ok
    if re.search(r"\bivt_prob\b", stripped) and "Float" in stripped and "no-float-ok" in stripped:
        found_ivt_prob_with_marker = True
        break

if not found_ivt_prob_with_marker:
    print(
        "ERRO [4]: ivt_prob Float64 em 007_ivt_scoring.sql sem marcador 'no-float-ok'. "
        "Adicionar '-- no-float-ok: probabilidade ML, nao dinheiro (TX-2 N/A)' na mesma linha.",
        file=sys.stderr,
    )
    fail = 1
else:
    print("ok [4]: ivt_prob Float64 com marcador no-float-ok (excecao explícita TX-2)")

# ---------------------------------------------------------------------------
# Check 5: MV mv_ivt_score_to_raw_impression existe
# ---------------------------------------------------------------------------
if "mv_ivt_score_to_raw_impression" not in ddl_007:
    print("ERRO [5]: MV mv_ivt_score_to_raw_impression nao encontrada em 007_ivt_scoring.sql",
          file=sys.stderr)
    fail = 1
else:
    print("ok [5]: MV mv_ivt_score_to_raw_impression definida")

# ---------------------------------------------------------------------------
# Check 6: MV filtra WHERE ivt_status = 'ivt'
# ---------------------------------------------------------------------------
if "WHERE ivt_status = 'ivt'" not in ddl_007:
    print(
        "ERRO [6]: MV mv_ivt_score_to_raw_impression nao filtra WHERE ivt_status = 'ivt'. "
        "A MV deve propagar apenas eventos IVT para raw_impression.",
        file=sys.stderr,
    )
    fail = 1
else:
    print("ok [6]: MV filtra WHERE ivt_status = 'ivt' (propaga apenas IVT)")

# ---------------------------------------------------------------------------
# Check 7: row-policy rp_raw_ivt_score_tenant existe (TX-3)
# ---------------------------------------------------------------------------
if "rp_raw_ivt_score_tenant" not in ddl_007:
    print("ERRO [7]: row-policy rp_raw_ivt_score_tenant nao encontrada em 007 (TX-3 violado)",
          file=sys.stderr)
    fail = 1
else:
    print("ok [7]: row-policy rp_raw_ivt_score_tenant presente (TX-3)")

# ---------------------------------------------------------------------------
# Check 8: row-policy usa padrao fail-closed (substring + match UUID, nao replaceAll)
# ---------------------------------------------------------------------------
has_replaceAll_in_policy = False
in_policy_context = False
for line in ddl_007.splitlines():
    stripped = line.strip()
    if stripped.startswith("--"):
        continue
    if "ROW POLICY" in stripped.upper() or "USING tenant_id" in stripped:
        in_policy_context = True
    if in_policy_context and re.search(r"\breplaceAll\s*\(\s*currentUser\(\)", stripped, re.I):
        has_replaceAll_in_policy = True
        break

if has_replaceAll_in_policy:
    print(
        "ERRO [8]: row-policy em 007 usa replaceAll(currentUser(),...) — "
        "vulnerabilidade cross-tenant (MEDIUM #5). Usar substring()+match(UUID).",
        file=sys.stderr,
    )
    fail = 1
else:
    print("ok [8]: row-policy usa padrao fail-closed (sem replaceAll)")

# Verifica que o padrao correto (startsWith + match UUID) esta presente
has_fail_closed = (
    "startsWith(currentUser(), 'tenant_')" in ddl_007
    and "match(" in ddl_007
    and "[0-9a-f]{8}-[0-9a-f]{4}" in ddl_007
    and "substring(currentUser()," in ddl_007
)
if "rp_raw_ivt_score_tenant" in ddl_007 and not has_fail_closed:
    print(
        "ERRO [8b]: row-policy em 007 existe mas nao usa padrao fail-closed "
        "(startsWith + match UUID + substring).",
        file=sys.stderr,
    )
    fail = 1
else:
    print("ok [8b]: padrao fail-closed (substring+match UUID) presente na row-policy de 007")

# ---------------------------------------------------------------------------
# Check 9: VIEW raw_impression_clean existe
# ---------------------------------------------------------------------------
if "raw_impression_clean" not in ddl_007:
    print("ERRO [9]: VIEW raw_impression_clean nao encontrada em 007_ivt_scoring.sql",
          file=sys.stderr)
    fail = 1
else:
    print("ok [9]: VIEW raw_impression_clean definida (alias de leitura)")

# ---------------------------------------------------------------------------
# Check 10: sem float em colunas monetarias em 007 (TX-2)
# ---------------------------------------------------------------------------
monetary_float_violations = []
for line in ddl_007.splitlines():
    stripped = line.strip()
    if stripped.startswith("--"):
        continue
    # Detecta Float em colunas com nome monetario (_value, _amount, _rate, _decimal)
    if re.search(r"\bFloat(32|64)\b", stripped, re.I):
        if re.search(r"(value|amount|rate|decimal)", stripped, re.I):
            # Excluir se tem marcador 'no-float-ok'
            if "no-float-ok" not in stripped:
                monetary_float_violations.append(stripped)

if monetary_float_violations:
    print(
        f"ERRO [10]: Float em coluna monetaria em 007_ivt_scoring.sql (TX-2 violado): "
        f"{monetary_float_violations}",
        file=sys.stderr,
    )
    fail = 1
else:
    print("ok [10]: sem Float em colunas monetarias em 007 (TX-2)")

# ---------------------------------------------------------------------------
# Check 11: StatsHourly (004) preservado — ainda filtra ivt_status = '' (TX-6)
# ---------------------------------------------------------------------------
if ddl_004:
    if "ivt_status = ''" not in ddl_004:
        print(
            "ERRO [11]: 004_stats_hourly.sql nao filtra ivt_status = ''. "
            "A migration 007 nao deve alterar esta invariante.",
            file=sys.stderr,
        )
        fail = 1
    else:
        print("ok [11]: StatsHourly (004) preservado — filtra ivt_status = '' (TX-6)")

# ---------------------------------------------------------------------------
# Check 12: tenant isolation test cobre raw_impression E raw_ivt_score (TX-3)
# ---------------------------------------------------------------------------
test_path = pathlib.Path("data/clickhouse/tests/tenant_isolation_test.sql")
if test_path.exists():
    test_sql = test_path.read_text()
    if "raw_impression" not in test_sql:
        print(
            "ERRO [12a]: tenant_isolation_test.sql nao cobre raw_impression (TX-3 violado).",
            file=sys.stderr,
        )
        fail = 1
    else:
        print("ok [12a]: tenant_isolation_test.sql cobre raw_impression (TX-3)")

    if "raw_ivt_score" not in test_sql:
        print(
            "ERRO [12b]: tenant_isolation_test.sql nao cobre raw_ivt_score (TX-3 violado). "
            "Adicionar queries cross_tenant_leak_ivt_score, own_rows_ivt_score e "
            "adversarial_leak_ivt_score em data/clickhouse/tests/tenant_isolation_test.sql.",
            file=sys.stderr,
        )
        fail = 1
    else:
        print("ok [12b]: tenant_isolation_test.sql cobre raw_ivt_score (TX-3)")
else:
    print(
        "ERRO [12]: tenant_isolation_test.sql nao encontrado em "
        "data/clickhouse/tests/ (TX-3 violado).",
        file=sys.stderr,
    )
    fail = 1

# ---------------------------------------------------------------------------
# Resultado
# ---------------------------------------------------------------------------
if fail == 0:
    print("\ndata-ivt-sql-check: ok — invariantes IVT (007_ivt_scoring.sql) verificados.")
else:
    print(f"\ndata-ivt-sql-check: FALHOU — {fail} verificacao(oes) falharam.", file=sys.stderr)
sys.exit(fail)
