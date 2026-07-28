#!/usr/bin/env bash
# Postgres initdb hook — applies the AdServer schema, dev roles and demo seed.
# Runs once, on first container start, against the POSTGRES_DB created by the
# entrypoint.
#
# NADA é enumerado à mão aqui. A ordem ENTRE schemas vem de
# /repo/db/schema-order.txt (a MESMA fonte que make/db.mk deriva em
# DB_SCHEMAS); a ordem DENTRO de cada schema vem de glob + sort sobre
# *_up.sql. É o mesmo par de regras de make/db.mk::db-test-all,
# make/dev.mk::dev-db-setup e .github/workflows/db.yml — os quatro
# provisionadores aplicam agora o mesmo conjunto, por construção.
#
# 32ª onda — o que este arquivo fazia antes: enumerava config parando em
# `0003_campaign_zones_rls` e aplicava do ledger APENAS
# `0001_ledger_schema_up.sql`. Consequências no stack Docker local:
#   - config.campaign_zones_tenant_isolation sem WITH CHECK no catálogo
#     (pg_policy.polwithcheck IS NULL) — o fallback implícito valida só o
#     lado `campaign_id`, deixando `zone_id` livre: tenant A podia vincular
#     campanha sua a zona de outro tenant (migration 0004 existe para isto);
#   - ledger SEM RLS (0003 nunca aplicada, ledger.current_tenant_id()
#     inexistente) e SEM o trigger append-only de ledger.postings (0004) —
#     ou seja, dinheiro mutável e legível cross-tenant no perfil local;
#   - schemas stats, vector e compliance nunca criados.
# E o cabeçalho afirmava "The same migration order is used by make
# dev-db-setup", que havia deixado de ser verdade — doc-lie.
set -euo pipefail

DB="${POSTGRES_DB:-adserver}"
REPO_DB="/repo/db"
run() { psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$1"; }
psql_c() { psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -c "$1"; }

ORDER_FILE="$REPO_DB/schema-order.txt"
if [ ! -f "$ORDER_FILE" ]; then
  echo "[pg-init] ERRO: $ORDER_FILE ausente — sem ele este script não sabe a ordem entre schemas."
  echo "[pg-init]       (o compose monta ../../db em /repo/db:ro; verifique o volume)"
  exit 1
fi

SCHEMAS=$(sed -e 's/#.*//' -e '/^[[:space:]]*$/d' "$ORDER_FILE")
if [ -z "$SCHEMAS" ]; then
  echo "[pg-init] ERRO: $ORDER_FILE não declarou nenhum schema — sentinela anti-vazio."
  echo "[pg-init]       Sem ela, o laço abaixo aplicaria ZERO migrations e o init terminaria verde."
  exit 1
fi

for schema in $SCHEMAS; do
  dir="$REPO_DB/$schema/migrations"
  files=$(ls "$dir"/*_up.sql 2>/dev/null | LC_ALL=C sort || true)
  if [ -z "$files" ]; then
    echo "[pg-init] ERRO: nenhuma migration *_up.sql em $dir (schema '$schema' vazio ou mal formado) — sentinela anti-vazio."
    exit 1
  fi
  echo "[pg-init] $schema"
  for f in $files; do
    echo "[pg-init]   up: $(basename "$f" _up.sql)"
    run "$f"
  done
done

echo "[pg-init] dev roles (adserver_loader BYPASSRLS / adserver_app RLS / adserver_copilot / adserver_stats_writer)"
run "$REPO_DB/seed/dev_roles.sql"

# GRANTs que dev_roles.sql deliberadamente não faz: ele cobre config +
# asset_registry, e concede vector_store SOMENTE a adserver_copilot
# (SELECT-only, papel de RAG). ledger/vector/compliance/stats para
# adserver_app/adserver_loader vêm daqui — mesmo bloco da FASE 3 de
# make/db.mk::db-test-all, do step "grants" de .github/workflows/db.yml e de
# make/dev.mk::dev-db-setup. O REVOKE em ledger.postings é o least-privilege
# já provado pelo trigger postings_immutable_trg (ledger/0004):
# internal/ledger/posting.go só faz INSERT/SELECT.
echo "[pg-init] grants (ledger / vector_store / compliance / stats)"
psql_c "GRANT USAGE ON SCHEMA ledger TO adserver_loader, adserver_app;"
psql_c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ledger TO adserver_app;"
psql_c "REVOKE UPDATE, DELETE ON ledger.postings FROM adserver_app;"
psql_c "GRANT SELECT ON ALL TABLES IN SCHEMA ledger TO adserver_loader;"
psql_c "GRANT USAGE ON ALL SEQUENCES IN SCHEMA ledger TO adserver_app;"
psql_c "GRANT EXECUTE ON FUNCTION ledger.current_tenant_id() TO adserver_app, adserver_loader;"
psql_c "GRANT USAGE ON SCHEMA vector_store TO adserver_loader, adserver_app;"
psql_c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA vector_store TO adserver_app;"
psql_c "GRANT SELECT ON ALL TABLES IN SCHEMA vector_store TO adserver_loader;"
psql_c "GRANT USAGE ON SCHEMA compliance TO adserver_loader, adserver_app;"
psql_c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA compliance TO adserver_app;"
psql_c "GRANT SELECT ON ALL TABLES IN SCHEMA compliance TO adserver_loader;"
psql_c "GRANT USAGE ON SCHEMA stats TO adserver_loader, adserver_app;"
psql_c "GRANT SELECT ON stats.events_raw, stats.live_kpis TO adserver_loader, adserver_app;"
psql_c "GRANT EXECUTE ON FUNCTION stats.current_tenant_id() TO adserver_loader, adserver_app;"
psql_c "GRANT USAGE ON SCHEMA stats TO adserver_stats_writer;"
psql_c "GRANT SELECT, INSERT ON stats.events_raw TO adserver_stats_writer;"
psql_c "GRANT EXECUTE ON FUNCTION stats.current_tenant_id() TO adserver_stats_writer;"

echo "[pg-init] demo seed (one tenant: contract BR + remnant house)"
run "$REPO_DB/seed/dev_seed.sql"

echo "[pg-init] provisionamento real Hojex News (zonas por-placement 1001-1004)"
run "$REPO_DB/seed/hojex_news_seed.sql"

echo "[pg-init] done"
