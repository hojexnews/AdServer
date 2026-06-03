#!/usr/bin/env bash
# no-float-go.sh — TX-2: proibe float em codigo monetario Go.
# Escopo financeiro: money/ledger/billing/payments. Fora disso, ignora
# (floats de ML/telemetria sao legitimos). Ver contracts/lint/no-float.md.
set -euo pipefail
files=$(git ls-files '*money*/*.go' '*ledger*/*.go' '*billing*/*.go' '*payments*/*.go' 2>/dev/null || true)
[ -z "$files" ] && { echo "no-float-go: nenhum arquivo financeiro Go (ok)"; exit 0; }
hits=$(grep -RInE '\bfloat(32|64)\b' --include='*.go' $files || true)
[ -z "$hits" ] || { echo "float proibido em codigo monetario (Go/TX-2): use Money/int64 ou shopspring/decimal"; echo "$hits"; exit 1; }
echo "no-float-go: ok"
