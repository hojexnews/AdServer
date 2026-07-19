#!/usr/bin/env bash
# no-float-ts.sh — TX-2: BACKSTOP textual para TypeScript de dinheiro no BFF,
# independente do NOME do arquivo.
#
# PROBLEMA QUE ESTE SCRIPT FECHA (27ª/28ª onda, achado #2):
#   O gate TS existente (eslint.config.mjs raiz + step do no-float.yml) so
#   seleciona arquivo por CONVENCAO DE NOME (glob *money*/*ledger*/*billing*/
#   *payments*). Um arquivo de dinheiro cujo NOME foge da convencao (ex.:
#   bff/src/routers/refunds-cash.ts) NUNCA e selecionado pelo glob — o ESLint
#   nem roda sobre ele — e parseFloat/Number(/literal-float nesse arquivo
#   passa verde silenciosamente.
#
# FIX: backstop AMPLO, escaneia TODO bff/src/**/*.ts (exceto *.test.ts e
# gen/**), independente do nome do arquivo, mas so falha uma LINHA se ela
# contiver AO MESMO TEMPO:
#   (a) um identificador de dinheiro conhecido (amount, price, total, budget,
#       revenue, cost, bid, cpm, cpc, cpa, balance, conversion_value,
#       minor_units — em snake_case OU camelCase, como token de identificador
#       inteiro, nao substring — "forbidden" nao casa "bid", "costume" nao
#       casa "cost"); E
#   (b) um padrao de float: parseFloat(, Number( (chamada direta, nao
#       Number.isInteger(...) etc.) ou um literal decimal (12.34).
#
# Isso mantem o backstop conservador: codigo nao-monetario legitimo com
# Number(index) ou parseFloat(rawInput) em outra linha continua passando;
# so a CO-OCORRENCIA na mesma linha de logica dispara.
#
# COMMENT/STRING-AWARE: reusa a mesma filosofia comment-aware do
# scripts/ci/no-float-go.sh (tokenizer que remove // , /* */, "...", '...'
# antes de testar os padroes). Estende para o caso TS/JS de template
# literals (`...`) COM suporte a expressoes interpoladas ${...} — o
# conteudo dentro de ${} E codigo real (ex.: `${parseFloat(amount)}` e uma
# violacao de verdade) e portanto NAO e stripado; so o texto literal do
# template (fora de ${}) e tratado como string opaca, igual "..".
#
# LIMITACAO CONHECIDA (documentada, aceita para um backstop — nao um
# parser completo): dentro de uma expressao ${...} o rastreamento de chaves
# aninhadas e feito por contagem simples de '{'/'}' sem re-entrar na
# maquina de estados para strings/comentarios aninhados dentro da propria
# expressao. Objetos literais complexos dentro de ${} sao raros em
# contexto de dinheiro; aceitavel para um backstop.
set -euo pipefail

mapfile -t files < <(git ls-files 'bff/src/**/*.ts' \
    2>/dev/null | grep -v '\.test\.ts$' | grep -v '/gen/' | grep -v '^gen/' | sort)

if [ "${#files[@]}" -eq 0 ]; then
    echo "no-float-ts: nenhum arquivo TS em bff/src (ok)"
    exit 0
fi

_tmpout=$(mktemp)
trap 'rm -f "$_tmpout"' EXIT

py_status=0
python3 - "${files[@]}" > "$_tmpout" << 'PYEOF' || py_status=$?
import sys, re

# Padroes de float "perigoso": parseFloat(...), Number(...) chamada DIRETA
# (nao Number.isInteger, Number.parseFloat com objeto — essas tem '.' entre
# o nome e a chamada) e literal decimal (12.34, .5).
FLOAT_CALL_PAT = re.compile(r'\bparseFloat\s*\(|(?<![.\w])Number\s*\(')
FLOAT_LIT_PAT = re.compile(r'(?<![A-Za-z0-9_])(?:[0-9]+\.[0-9]+|\.[0-9]+)\b')

# Identificadores de dinheiro (token INTEIRO de identificador, nao
# substring livre) — casam tanto snake_case (conversion_value, minor_units)
# quanto camelCase (conversionValue, minorUnits, totalBudget).
MONEY_WORDS = {
    "amount", "price", "total", "budget", "revenue", "cost", "bid",
    "cpm", "cpc", "cpa", "balance", "conversion", "value", "minor", "units",
}
# "conversion_value"/"minor_units" sao PARES de palavras — exigimos as duas
# palavras adjacentes no MESMO identificador para casar (evita que um
# "value" ou "units" soltos e genericos (ex.: "units" de paginacao) disparem
# sozinhos). O restante (amount, price, total, budget, revenue, cost, bid,
# cpm, cpc, cpa, balance) casa como palavra unica dentro do identificador.
MONEY_SOLO = {
    "amount", "price", "total", "budget", "revenue", "cost", "bid",
    "cpm", "cpc", "cpa", "balance",
}
MONEY_PAIRS = {("conversion", "value"), ("minor", "units")}

IDENT_PAT = re.compile(r'[A-Za-z_$][A-Za-z0-9_$]*')
CAMEL_SPLIT_PAT = re.compile(r'[A-Z]+(?=[A-Z][a-z])|[A-Z]?[a-z0-9]+|[A-Z]+|[0-9]+')

def split_identifier_words(ident: str):
    """snake_case E camelCase -> lista de palavras minusculas."""
    words = []
    for part in ident.strip('_$').split('_'):
        if not part:
            continue
        sub = CAMEL_SPLIT_PAT.findall(part)
        words.extend(w.lower() for w in sub if w)
    return words

def line_has_money_identifier(line: str) -> bool:
    for m in IDENT_PAT.finditer(line):
        words = split_identifier_words(m.group(0))
        if not words:
            continue
        for w in words:
            if w in MONEY_SOLO:
                return True
        for i in range(len(words) - 1):
            if (words[i], words[i + 1]) in MONEY_PAIRS:
                return True
    return False

def line_has_float_pattern(line: str) -> bool:
    return bool(FLOAT_CALL_PAT.search(line)) or bool(FLOAT_LIT_PAT.search(line))


def strip_ts_non_code(src: str):
    """
    Tokeniza src caractere a caractere removendo comentarios (// , /* */),
    strings ('...', "...") e o texto literal de template strings (`...`
    fora de qualquer ${...}). Expressoes ${...} dentro de template strings
    SAO mantidas como codigo (ver docstring do modulo). Retorna
    {lineno: [chars]} so com codigo real, preservando numeracao de linha.
    """
    lines_code = {}
    # Pilha de contextos de template literal abertos (cada entrada e a
    # profundidade de chaves '{' pendentes da expressao ${...} atual, ou
    # None se estamos no texto literal do template, fora de qualquer ${}).
    template_stack = []
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
            if c == '`':
                state = 'template_lit'
                i += 1
                continue
            # Se estamos dentro de uma expressao ${...} (topo da pilha e
            # a profundidade de chaves pendente dessa expressao), rastreia
            # '{'/'}' para achar o '}' de fechamento que devolve ao texto
            # literal do template que a envolve (pilha permite aninhamento:
            # template dentro de expressao dentro de template...).
            if template_stack:
                if c == '{':
                    template_stack[-1] += 1
                elif c == '}':
                    template_stack[-1] -= 1
                    if template_stack[-1] == 0:
                        template_stack.pop()
                        state = 'template_lit'
                        if c == '\n':
                            lineno += 1
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
                state = 'normal'

        elif state == 'sq_string':
            if c == '\\' and i + 1 < n:
                if src[i + 1] == '\n':
                    lineno += 1
                i += 2
                continue
            if c == "'":
                state = 'normal'
            elif c == '\n':
                lineno += 1
                state = 'normal'

        elif state == 'template_lit':
            if c == '\\' and i + 1 < n:
                if src[i + 1] == '\n':
                    lineno += 1
                i += 2
                continue
            if c == '`':
                state = 'normal'
            elif c == '$' and i + 1 < n and src[i + 1] == '{':
                # entra em expressao ${...}: codigo real, profundidade=1
                template_stack.append(1)
                state = 'normal'
                i += 2
                continue
            elif c == '\n':
                lineno += 1

        i += 1

    return lines_code


found = []
for path in sys.argv[1:]:
    with open(path, encoding='utf-8', errors='replace') as f:
        src = f.read()
    src_lines = src.splitlines()
    lines_code = strip_ts_non_code(src)
    for lineno in sorted(lines_code):
        fragment = ''.join(lines_code[lineno])
        if line_has_money_identifier(fragment) and line_has_float_pattern(fragment):
            orig = src_lines[lineno - 1] if lineno <= len(src_lines) else fragment
            found.append(f"{path}:{lineno}:{orig}")

if found:
    for hit in found:
        print(hit)
    sys.exit(1)
PYEOF

hits=$(cat "$_tmpout")

if [ "$py_status" -ne 0 ] || [ -n "$hits" ]; then
    echo "float/Number/parseFloat proibido em codigo monetario TS (BFF/TX-2): use decimal.js ou bigint"
    echo "(backstop por CONTEUDO, independente do nome do arquivo — ver scripts/ci/no-float-ts.sh)"
    [ -n "$hits" ] && echo "$hits"
    exit 1
fi
echo "no-float-ts: ok"
