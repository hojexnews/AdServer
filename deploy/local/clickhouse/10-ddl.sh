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
  sed -e "s/[[:space:]]*ON CLUSTER '{cluster}'//g" \
      -e "s|\${REDPANDA_BROKERS}|${BROKERS}|g" "$f" \
    | awk '
        BEGIN { skip = 0; in_str = 0 }
        # Detect start of a COMMENT ON statement (first token at line start).
        /^COMMENT[[:space:]]+ON/ { skip = 1; in_str = 0 }
        skip == 1 {
            # Walk character by character to find the real statement-terminating ;
            # while tracking single-quoted SQL strings (avoids ; inside string literals
            # or inside -- inline comments prematurely ending the skip).
            found_end = 0
            n = length($0)
            in_comment = 0
            for (i = 1; i <= n; i++) {
                c = substr($0, i, 1)
                c2 = (i < n) ? substr($0, i, 2) : ""
                if (!in_str && !in_comment && c2 == "--") { in_comment = 1 }
                if (in_comment) { continue }         # rest of line is -- comment
                if (!in_str && c == "'"'"'") { in_str = 1; continue }
                if ( in_str && c == "'"'"'") { in_str = 0; continue }
                if (!in_str && c == ";")     { found_end = 1; break }
            }
            if (found_end) { skip = 0; in_str = 0 }
            next
        }
        { print }
    ' \
    | clickhouse-client --multiquery
done

echo "[ch-init] done"
