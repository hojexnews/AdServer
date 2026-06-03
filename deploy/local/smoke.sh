#!/usr/bin/env bash
# End-to-end smoke for the local stack: the decision service must serve a real
# ad from the seeded config (contract for BR, remnant house otherwise).
#
# Works against any running decision service backed by the dev seed — Docker
# Compose OR a binary started by `make dev-decision-run`.  Needs psql on PATH
# to resolve the (serial) zone id from the seed.
set -euo pipefail

DEC="${DECISION_URL:-http://localhost:8080}"
PGURL="${PGURL:-postgres://postgres:postgres@localhost:5432/adserver?sslmode=disable}"

zone="$(psql "$PGURL" -tAc "select id from config.zones where name='Sidebar 300x250' limit 1" | tr -d '[:space:]')"
[ -n "$zone" ] || { echo "FAIL: seed zone 'Sidebar 300x250' not found in $PGURL"; exit 1; }
echo "[smoke] zone_id=$zone  decision=$DEC"

decide() {
  curl -fsS -X POST "$DEC/v1/decide" -H 'content-type: application/json' \
    -d "{\"zone_id\":\"$zone\",\"geo_country\":\"$1\"}"
}

br="$(decide BR)";  echo "[smoke] BR -> $br"
echo "$br" | grep -q SERVED_TIER_CONTRACT || { echo "FAIL: BR should be CONTRACT"; exit 1; }

us="$(decide US)";  echo "[smoke] US -> $us"
echo "$us" | grep -q SERVED_TIER_REMNANT || { echo "FAIL: US should be REMNANT"; exit 1; }

echo "[smoke] OK — decision serves real ads from db/config"
