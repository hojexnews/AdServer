#!/usr/bin/env bash
# no-float-sql.sh — TX-2: proibe FLOAT/DOUBLE/REAL/MONEY em colunas monetarias.
# Dinheiro usa NUMERIC(precision, scale) por ativo. Ver contracts/lint/no-float.md.
set -euo pipefail
files=$(git ls-files 'migrations/*.sql' 'db/migrations/*.sql' 'sql/*.sql' 2>/dev/null || true)
[ -z "$files" ] && { echo "no-float-sql: nenhuma migracao SQL (ok)"; exit 0; }
hits=$(grep -RIniE '\b(float[0-9]*|double\s+precision|real|money)\b' $files || true)
[ -z "$hits" ] || { echo "tipo flutuante/MONEY proibido em coluna monetaria (SQL/TX-2): use NUMERIC"; echo "$hits"; exit 1; }
echo "no-float-sql: ok"
