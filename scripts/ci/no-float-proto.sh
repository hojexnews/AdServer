#!/usr/bin/env bash
# no-float-proto.sh — TX-2: proibe float/double em campos .proto de dinheiro.
#
# Escopo financeiro no NIVEL DE CONTRATO: proto/adserver/money/** e
# proto/adserver/payments/** — o Money canonico do sistema inteiro e todo
# evento de pagamento. `buf breaking` (WIRE_JSON) so protege um campo
# EXISTENTE de trocar de tipo; um campo NOVO aditivo com `double`/`float`
# passaria despercebido por qualquer outro gate deste repo. Ver
# contracts/lint/no-float.md e proto/adserver/money/v1/money.proto (regras
# invioláveis).
#
# COMMENT-AWARE: remove comentarios proto (//, /* ... */) e string literals
# ("...", '...') antes de testar \b(double|float)\b, para nao disparar
# falso-positivo nos proprios comentarios que DOCUMENTAM a proibicao (ex.:
# "FLOAT E PROIBIDO" no cabecalho de money.proto).
set -euo pipefail

mapfile -t files < <(git ls-files \
    'proto/adserver/money/**/*.proto' 'proto/adserver/payments/**/*.proto' \
    2>/dev/null | sort)

if [ "${#files[@]}" -eq 0 ]; then
    echo "no-float-proto: nenhum arquivo .proto de money/payments (ok)"
    exit 0
fi

_tmpout=$(mktemp)
trap 'rm -f "$_tmpout"' EXIT

py_status=0
python3 - "${files[@]}" > "$_tmpout" << 'PYEOF' || py_status=$?
import sys, re

FLOAT_PAT = re.compile(r'\b(double|float)\b', re.IGNORECASE)

def strip_proto_non_code(src):
    """Remove comentarios (//, /* */) e string literals ("...", '...') de
    um .proto, preservando a numeracao de linha original para relatorio."""
    lines_code = {}
    state = 'normal'
    lineno = 1
    i = 0
    n = len(src)

    def emit(c):
        lines_code.setdefault(lineno, []).append(c)

    while i < n:
        c = src[i]

        if state == 'normal':
            if c == '/' and i + 1 < n:
                nxt = src[i + 1]
                if nxt == '/':
                    state = 'line_comment'
                    i += 2
                    continue
                if nxt == '*':
                    state = 'block_comment'
                    i += 2
                    continue
            if c == '"':
                state = 'dq_string'
                i += 1
                continue
            if c == "'":
                state = 'sq_string'
                i += 1
                continue
            if c == '\n':
                lineno += 1
            else:
                emit(c)

        elif state == 'line_comment':
            if c == '\n':
                state = 'normal'
                lineno += 1

        elif state == 'block_comment':
            if c == '*' and i + 1 < n and src[i + 1] == '/':
                state = 'normal'
                i += 2
                continue
            if c == '\n':
                lineno += 1

        elif state == 'dq_string':
            if c == '\\' and i + 1 < n:
                i += 2
                continue
            if c == '"':
                state = 'normal'
            elif c == '\n':
                lineno += 1
                state = 'normal'  # string nao-terminada (erro de sintaxe)

        elif state == 'sq_string':
            if c == '\\' and i + 1 < n:
                i += 2
                continue
            if c == "'":
                state = 'normal'
            elif c == '\n':
                lineno += 1
                state = 'normal'

        i += 1

    return lines_code

found = []
for path in sys.argv[1:]:
    with open(path, encoding='utf-8', errors='replace') as f:
        src = f.read()
    src_lines = src.splitlines()
    lines_code = strip_proto_non_code(src)
    for lineno in sorted(lines_code):
        fragment = ''.join(lines_code[lineno])
        if FLOAT_PAT.search(fragment):
            orig = src_lines[lineno - 1] if lineno <= len(src_lines) else fragment
            found.append(f"{path}:{lineno}:{orig}")

if found:
    for hit in found:
        print(hit)
    sys.exit(1)
PYEOF

hits=$(cat "$_tmpout")

if [ "$py_status" -ne 0 ] || [ -n "$hits" ]; then
    echo "float/double proibido em campo .proto monetario (TX-2): use adserver.money.v1.Money (int64 amount + uint32 scale)"
    [ -n "$hits" ] && echo "$hits"
    exit 1
fi
echo "no-float-proto: ok"
