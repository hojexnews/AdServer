#!/usr/bin/env bash
# ClickHouse initdb hook — applies data/clickhouse/migrations for SINGLE-NODE
# dev.  The production DDL targets a replicated cluster; for a one-node dev
# server we render it on the fly:
#
#   1. strip `ON CLUSTER '{cluster}'` (no Keeper/cluster in dev);
#   2. substitute the ${REDPANDA_BROKERS} placeholder with the dev broker;
#   3. drop `COMMENT ON TABLE ...;` statements (Postgres-style; ClickHouse
#      carries comments differently — they are non-essential for dev).
#
# The .proto remains the source of truth; the wire format in dev is JSONEachRow
# (the kafka engines in 001 use kafka_format='JSONEachRow'), matched by the Go
# producer's TELEMETRY_WIRE_FORMAT=json.
set -euo pipefail

BROKERS="${REDPANDA_BROKERS:-redpanda:9092}"

for f in $(ls /repo/ch/*.sql | sort); do
  echo "[ch-init] applying $(basename "$f")"
  sed -e "s/ ON CLUSTER '{cluster}'//g" \
      -e "s|\${REDPANDA_BROKERS}|${BROKERS}|g" "$f" \
    | awk 'BEGIN{skip=0}
           /^COMMENT ON/{skip=1}
           skip==1{ if (index($0,";")>0) skip=0; next }
           {print}' \
    | clickhouse-client --multiquery
done

echo "[ch-init] done"
