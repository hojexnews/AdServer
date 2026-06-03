#!/usr/bin/env bash
# no-float-py.sh — TX-2: proibe float em codigo monetario Python.
# Escopo financeiro: money/ledger/billing/payments. Dinheiro usa
# decimal.Decimal (contexto fixo ROUND_HALF_EVEN). Ver contracts/lint/no-float.md.
#
# COMMENT-AWARE (2026-06-03):
#   Usa o modulo `tokenize` da stdlib CPython para separar tokens de codigo
#   de comentarios (# ...) e string literals (incluindo docstrings triplas).
#   Apenas tokens de codigo real sao testados contra o padrao de float.
#   A linha reportada e a linha REAL do arquivo original (tokenize retorna
#   lineno exato do token no arquivo fonte).
#
#   DEDUPLICACAO: multiplos tokens float na mesma linha sao reportados
#   uma unica vez (arquivo:linha:texto-original), evitando ruido.
#
#   CAPTURA DE STATUS: o Python escreve hits em arquivo temporario e retorna
#   exit 1 quando encontra violacoes.  Usamos `|| py_status=$?` para evitar
#   que set -e aborte o script antes de reportar os hits ao usuario.
set -euo pipefail

mapfile -t files < <(git ls-files \
    '*money*/*.py' '*ledger*/*.py' '*billing*/*.py' '*payments*/*.py' \
    2>/dev/null | sort)

if [ "${#files[@]}" -eq 0 ]; then
    echo "no-float-py: nenhum arquivo financeiro Python (ok)"
    exit 0
fi

_tmpout=$(mktemp)
trap 'rm -f "$_tmpout"' EXIT

py_status=0
python3 - "${files[@]}" > "$_tmpout" << 'PYEOF' || py_status=$?
import sys, re, tokenize, io, token as tok_mod

# Tipos de token que NAO sao codigo executavel — ignorados pelo guard
SKIP_TYPES = {
    tok_mod.COMMENT,    # # comentarios de linha
    tok_mod.STRING,     # strings e docstrings (''', """, ', ")
    tok_mod.NEWLINE,
    tok_mod.NL,
    tok_mod.INDENT,
    tok_mod.DEDENT,
    tok_mod.ENCODING,
    tok_mod.ENDMARKER,
}

# float como nome de tipo/funcao
FLOAT_NAME = re.compile(r'^float$')

# Literal numerico de ponto flutuante (inclui underscore separators, complex)
# Cobre: 1.0  .5  1e3  1_000.5  1.0j  1e3j  — mas nao inteiros puros
FLOAT_LIT = re.compile(
    r'^[0-9][0-9_]*\.[0-9_]*([eE][+-]?[0-9_]+)?[jJ]?$'
    r'|^\.[0-9][0-9_]*([eE][+-]?[0-9_]+)?[jJ]?$'
    r'|^[0-9][0-9_]*[eE][+-]?[0-9_]+[jJ]?$'
)

found = []      # lista de strings "path:lineno:text"
seen  = set()   # conjunto (path, lineno) para deduplicar multiplos hits na mesma linha

for path in sys.argv[1:]:
    with open(path, encoding='utf-8', errors='replace') as f:
        src = f.read()
    lines = src.splitlines()

    try:
        tokens = list(tokenize.generate_tokens(io.StringIO(src).readline))
    except tokenize.TokenError:
        # Fallback para arquivo com erro de sintaxe: remove comentarios # e checa
        for lineno, line in enumerate(lines, 1):
            code_part = line.split('#', 1)[0]
            if re.search(r'\bfloat\b', code_part) or \
               re.search(r'(?<![A-Za-z0-9_])'
                         r'([0-9]+\.[0-9]+|\.[0-9]+|[0-9]+[eE][+-]?[0-9]+)',
                         code_part):
                key = (path, lineno)
                if key not in seen:
                    seen.add(key)
                    found.append(f"{path}:{lineno}:{line.rstrip()}")
        continue

    for ttype, tstring, tstart, _tend, _line in tokens:
        if ttype in SKIP_TYPES:
            continue
        lineno = tstart[0]
        key = (path, lineno)
        if key in seen:
            continue  # ja reportou esta linha
        orig = lines[lineno - 1].rstrip() if lineno <= len(lines) else tstring

        # float como identificador (nome de tipo ou chamada de funcao)
        if ttype == tok_mod.NAME and FLOAT_NAME.match(tstring):
            seen.add(key)
            found.append(f"{path}:{lineno}:{orig}")
            continue

        # Literal numerico de ponto flutuante
        if ttype == tok_mod.NUMBER and FLOAT_LIT.match(tstring):
            seen.add(key)
            found.append(f"{path}:{lineno}:{orig}")
            continue

if found:
    for hit in found:
        print(hit)
    sys.exit(1)
PYEOF

hits=$(cat "$_tmpout")

if [ "$py_status" -ne 0 ] || [ -n "$hits" ]; then
    echo "float proibido em codigo monetario (Python/TX-2): use decimal.Decimal"
    [ -n "$hits" ] && echo "$hits"
    exit 1
fi
echo "no-float-py: ok"
