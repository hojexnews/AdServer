#!/usr/bin/env bash
# no-float-go.sh — TX-2: proibe float em codigo monetario Go.
# Escopo financeiro: money/ledger/billing/payments. Fora disso, ignora
# (floats de ML/telemetria sao legitimos). Ver contracts/lint/no-float.md.
#
# COMMENT-AWARE (2026-06-03):
#   Usa tokenizer Python (estado de maquina) para remover comentarios Go
#   (//, /* ... */) e string literals (raw strings `...`, strings "...")
#   antes de testar \bfloat(32|64)\b.  Comentarios que documentam a
#   proibicao TX-2 nao disparam falso-positivo.
#
#   NUMERACAO DE LINHA: reporta a linha REAL do arquivo original.
#   O tokenizer rastreia lineno ao processar cada caractere, independente
#   de o caractere estar dentro ou fora de comentario/string.
#
#   CAPTURA DE STATUS: o Python escreve hits em arquivo temporario e retorna
#   exit 1 quando encontra violacoes.  Usamos `|| py_status=$?` para evitar
#   que set -e aborte o script antes de reportar os hits ao usuario.
set -euo pipefail

mapfile -t files < <(git ls-files \
    '*money*/*.go' '*ledger*/*.go' '*billing*/*.go' '*payments*/*.go' \
    2>/dev/null | sort)

if [ "${#files[@]}" -eq 0 ]; then
    echo "no-float-go: nenhum arquivo financeiro Go (ok)"
    exit 0
fi

_tmpout=$(mktemp)
trap 'rm -f "$_tmpout"' EXIT

py_status=0
python3 - "${files[@]}" > "$_tmpout" << 'PYEOF' || py_status=$?
import sys, re

FLOAT_PAT = re.compile(r'\bfloat(32|64)\b')

def strip_go_non_code(src):
    """
    Tokeniza src caractere a caractere removendo comentarios Go e string
    literals.  Retorna dict {lineno: [chars]} com apenas codigo real.
    Mantém numeracao de linha original para relatorio preciso.

    Estados do tokenizer:
      normal        — codigo Go executavel
      line_comment  — dentro de // ate fim de linha
      block_comment — dentro de /* ... */
      dq_string     — dentro de "..." (interpreta escapes)
      raw_string    — dentro de `...` (raw string, sem escapes)
      rune          — dentro de '.' (rune literal)
    """
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
                elif nxt == '*':
                    state = 'block_comment'
                    i += 2
                    continue
            if c == '"':
                state = 'dq_string'
                i += 1
                continue
            if c == '`':
                state = 'raw_string'
                i += 1
                continue
            if c == "'":
                state = 'rune'
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
                if src[i + 1] == '\n':
                    lineno += 1
                i += 2
                continue
            if c == '"':
                state = 'normal'
            elif c == '\n':
                lineno += 1
                state = 'normal'   # string nao-terminada (erro de sintaxe)

        elif state == 'raw_string':
            if c == '`':
                state = 'normal'
            elif c == '\n':
                lineno += 1

        elif state == 'rune':
            if c == '\\' and i + 1 < n:
                if src[i + 1] == '\n':
                    lineno += 1
                i += 2
                continue
            if c == "'" or c == '\n':
                if c == '\n':
                    lineno += 1
                state = 'normal'

        i += 1

    return lines_code

found = []
for path in sys.argv[1:]:
    with open(path, encoding='utf-8', errors='replace') as f:
        src = f.read()
    src_lines = src.splitlines()
    lines_code = strip_go_non_code(src)
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
    echo "float proibido em codigo monetario (Go/TX-2): use Money/int64 ou shopspring/decimal"
    [ -n "$hits" ] && echo "$hits"
    exit 1
fi
echo "no-float-go: ok"
