#!/usr/bin/env bash
# Postgres initdb hook — applies the AdServer schema, dev roles and demo seed.
# Runs once, on first container start, against the POSTGRES_DB created by the
# entrypoint.  The same migration order is used by make dev-db-setup (verified
# against a local Postgres outside Docker).
set -euo pipefail

DB="${POSTGRES_DB:-adserver}"
run() { psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$1"; }

echo "[pg-init] asset_registry"
run /repo/db/asset_registry/migrations/0001_asset_registry_up.sql

echo "[pg-init] config schema + RLS"
for f in 0001_config_schema 0002_config_rls 0003_campaign_zones_rls; do
  run "/repo/db/config/migrations/${f}_up.sql"
done

echo "[pg-init] ledger schema"
run /repo/db/ledger/migrations/0001_ledger_schema_up.sql

echo "[pg-init] dev roles (adserver_loader BYPASSRLS / adserver_app RLS)"
run /repo/db/seed/dev_roles.sql

echo "[pg-init] demo seed (one tenant: contract BR + remnant house)"
run /repo/db/seed/dev_seed.sql

echo "[pg-init] provisionamento real Hojex News (zonas por-placement 1001-1004)"
run /repo/db/seed/hojex_news_seed.sql

echo "[pg-init] done"
