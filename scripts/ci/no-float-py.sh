#!/usr/bin/env bash
# no-float-py.sh — TX-2: proibe float em codigo monetario Python.
# Escopo financeiro: money/ledger/billing/payments. Dinheiro usa
# decimal.Decimal (contexto fixo ROUND_HALF_EVEN). Ver contracts/lint/no-float.md.
set -euo pipefail
files=$(git ls-files '*money*/*.py' '*ledger*/*.py' '*billing*/*.py' '*payments*/*.py' 2>/dev/null || true)
[ -z "$files" ] && { echo "no-float-py: nenhum arquivo financeiro Python (ok)"; exit 0; }
# proibe float(), anotacoes : float e literais float (1.0, .5, 1e3)
hits=$(grep -RInE '\bfloat\s*\(|:\s*float\b|[^A-Za-z0-9_.]([0-9]+\.[0-9]+|\.[0-9]+|[0-9]+[eE][+-]?[0-9]+)' $files || true)
[ -z "$hits" ] || { echo "float proibido em codigo monetario (Python/TX-2): use decimal.Decimal"; echo "$hits"; exit 1; }
echo "no-float-py: ok"
