#!/usr/bin/env bash
# no-float-ts.sh — TX-2: BACKSTOP textual para TypeScript/TSX de dinheiro no
# BFF e no console (web/console), independente do NOME do arquivo.
#
# PROBLEMA QUE ESTE SCRIPT FECHA (27ª/28ª onda, achado #2):
#   O gate TS existente (eslint.config.mjs raiz + step do no-float.yml) so
#   seleciona arquivo por CONVENCAO DE NOME (glob *money*/*ledger*/*billing*/
#   *payments*). Um arquivo de dinheiro cujo NOME foge da convencao (ex.:
#   bff/src/routers/refunds-cash.ts) NUNCA e selecionado pelo glob — o ESLint
#   nem roda sobre ele — e parseFloat/Number(/literal-float nesse arquivo
#   passa verde silenciosamente.
#
# ESCOPO ESTENDIDO (29ª onda, FP #2): o mesmo ponto-cego existia no console
# (web/console) — o MONEY_GLOBS do web/console/eslint.config.mjs cobre hoje
# toda pagina sob src/app/**/*.tsx (ver eslint.config.mjs), mas esse backstop
# so escaneava bff/src/**/*.ts. Um .ts/.tsx de dinheiro do console fora de
# src/app/ (ex.: um novo helper em src/lib/ sem "money"/"billing" no nome)
# ainda escaparia do ESLint por nome E do backstop por escopo. Agora este
# script escaneia TAMBEM web/console/src/**/*.ts e **/*.tsx.
#
# FIX: backstop AMPLO, escaneia TODO bff/src/**/*.ts e web/console/src/**/*.ts
# e **/*.tsx (exceto *.test.ts(x) e gen/**), independente do nome do arquivo,
# mas so falha uma LINHA se ela contiver AO MESMO TEMPO:
#   (a) um identificador de dinheiro conhecido (amount, price, total, budget,
#       revenue, cost, bid, cpm, cpc, cpa, balance, rate, conversion_value,
#       minor_units — em snake_case OU camelCase, como token de identificador
#       inteiro, nao substring — "forbidden" nao casa "bid", "costume" nao
#       casa "cost", "operate"/"separate"/"generate" nao casam "rate" pois
#       sao uma unica corrida de minusculas, sem fronteira de camelCase); E
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
# TSX/JSX (29ª onda, FP #2): arquivos .tsx tem um terceiro tipo de texto
# opaco que .ts nao tem — o CONTEUDO TEXTUAL de filhos JSX (ex.:
# `<label>Rate (valor decimal, ex.: 5.00)</label>` — "5.00" ali e um hint
# de UI, nao aritmetica). Um parser JSX completo esta fora do escopo de um
# backstop textual; em vez disso, para arquivos .tsx, o padrao de LITERAL
# decimal (12.34) so dispara se a MESMA linha tambem tiver um sinal
# independente de codigo real (=, ;, {, +, -, *, ou a palavra `return`) —
# texto de UI em portugues legitimamente nao carrega nenhum desses tokens.
# parseFloat(/Number( continuam disparando sem essa guarda extra em .tsx:
# uma chamada de funcao JS nunca aparece verbatim em prosa. Esta guarda so
# se aplica a .tsx; o comportamento de .ts (bff/src e web/console/src) fica
# INALTERADO (evita regressao no backstop ja validado nas ondas 27/28).
#
# LIMITACAO CONHECIDA (documentada, aceita para um backstop — nao um
# parser completo): dentro de uma expressao ${...} o rastreamento de chaves
# aninhadas e feito por contagem simples de '{'/'}' sem re-entrar na
# maquina de estados para strings/comentarios aninhados dentro da propria
# expressao. Objetos literais complexos dentro de ${} sao raros em
# contexto de dinheiro; aceitavel para um backstop.
set -euo pipefail

mapfile -t files < <({
    git ls-files 'bff/src/**/*.ts' 2>/dev/null
    git ls-files 'web/console/src/**/*.ts' 2>/dev/null
    git ls-files 'web/console/src/**/*.tsx' 2>/dev/null
} | grep -v '\.test\.ts$' | grep -v '\.test\.tsx$' | grep -v '/gen/' | grep -v '^gen/' | sort -u)

if [ "${#files[@]}" -eq 0 ]; then
    echo "no-float-ts: nenhum arquivo TS/TSX em bff/src ou web/console/src (ok)"
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

# Guarda extra SO para .tsx (ver docstring do arquivo, secao "TSX/JSX"):
# exige um sinal independente de codigo real na mesma linha antes de deixar
# um literal decimal (sozinho, sem parseFloat/Number) disparar. Nunca inclui
# '(' ')' ':' — esses SIM aparecem em prosa/hint de UI (ex.: "ex.: 5.00)")
# e reintroduziriam o falso-positivo que esta guarda existe para evitar.
TSX_CODE_SIGNAL_PAT = re.compile(r'[=;{]|[+\-*]|(?<![A-Za-z_$])return(?![A-Za-z0-9_$])')

# Identificadores de dinheiro (token INTEIRO de identificador, nao
# substring livre) — casam tanto snake_case (conversion_value, minor_units)
# quanto camelCase (conversionValue, minorUnits, totalBudget, rateAmount).
MONEY_WORDS = {
    "amount", "price", "total", "budget", "revenue", "cost", "bid",
    "cpm", "cpc", "cpa", "balance", "rate", "conversion", "value",
    "minor", "units",
}
# "conversion_value"/"minor_units" sao PARES de palavras — exigimos as duas
# palavras adjacentes no MESMO identificador para casar (evita que um
# "value" ou "units" soltos e genericos (ex.: "units" de paginacao) disparem
# sozinhos). O restante (amount, price, total, budget, revenue, cost, bid,
# cpm, cpc, cpa, balance, rate) casa como palavra unica dentro do
# identificador — "rate" adicionada na 29ª onda (FP #2): "rateAmount" ja
# casava via "amount", mas um campo nomeado so "rate"/"rateCurrency" sem
# "amount" no identificador nao casaria sem este token.
MONEY_SOLO = {
    "amount", "price", "total", "budget", "revenue", "cost", "bid",
    "cpm", "cpc", "cpa", "balance", "rate",
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

def line_has_float_pattern(line: str, is_tsx: bool) -> bool:
    if FLOAT_CALL_PAT.search(line):
        return True
    if FLOAT_LIT_PAT.search(line):
        if not is_tsx:
            return True
        # .tsx: literal isolado so conta com um sinal de codigo real junto
        # (ver TSX_CODE_SIGNAL_PAT) — evita disparar em texto de UI (JSX
        # children), que nao carrega nenhum desses tokens.
        if TSX_CODE_SIGNAL_PAT.search(line):
            return True
    return False


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


# -----------------------------------------------------------------------
# JANELA DE ANALISE: co-ocorrencia por LOGICAL STATEMENT, nao por linha
# fisica isolada (achado no-float-multiline-split-defeats-cooccurrence,
# CRITICAL). O match por-linha-fisica original era derrotado por QUALQUER
# quebra de linha do Prettier numa atribuicao/expressao longa:
#   const rateAmount =
#       12.50;
# ("rateAmount" cai numa linha, "12.50" cai na proxima — nenhuma das duas
# isoladas satisfaz a conjuncao E, e a violacao passava despercebida).
#
# FIX: agrupa linhas fisicas em "segmentos logicos" antes do match,
# usando dois sinais de continuacao (TS nao tem tokenizer de linha logica
# como Python; isto e um backstop textual, nao um parser completo):
#   (a) parenteses/colchetes/chaves ainda abertos ao fim da linha
#       (profundidade > 0) — cobre `const x = (\n  12.50\n)`;
#   (b) a linha termina com um operador binario/atribuicao que exige
#       continuacao (=, +, -, *, /, %, `,`, `:`, `?`, `&&`, `||`, `??`,
#       `=>`) — cobre `const rateAmount =\n  12.50;` (sem parenteses).
# CAP DE SEGURANCA (MAX_SEGMENT_LINES): sem isso, um `return (<JSX
# gigante>)` classico do React manteria depth>0 por CENTENAS de linhas,
# transformando o componente inteiro num unico segmento e explodindo
# falsos-positivos (qualquer texto JSX com "R$ 12.50" en algum lugar do
# componente co-ocorreria com qualquer sinal de codigo alhures no mesmo
# JSX). Alem do cap, o segmento e forcado a fechar — cobre o caso real
# (quebra do Prettier, tipicamente 2-4 linhas) sem se estender a blocos
# JSX inteiros.
# -----------------------------------------------------------------------
CONT_TRAIL_PAT = re.compile(r'(?:(?<=\s)=>|(?<=\s)&&|(?<=\s)\|\||(?<=\s)\?\?|[=+\-*/%,:?])\s*$')
MAX_SEGMENT_LINES = 6

def build_logical_segments(lines_code: dict, total_lines: int):
    """
    Agrupa {lineno: [chars]} (ja sem comentarios/strings) em segmentos
    logicos. Retorna lista de (start_lineno, {lineno: fragment_str}).
    """
    segments = []
    buf: dict = {}
    buf_start = None
    depth = 0
    for ln in range(1, total_lines + 1):
        text = ''.join(lines_code.get(ln, []))
        stripped = text.strip()
        if buf_start is None:
            if not stripped:
                continue
            buf_start = ln
        buf[ln] = text
        depth += text.count('(') + text.count('[') + text.count('{')
        depth -= text.count(')') + text.count(']') + text.count('}')
        if depth < 0:
            depth = 0
        at_cap = len(buf) >= MAX_SEGMENT_LINES
        continues = (depth > 0 or (bool(stripped) and CONT_TRAIL_PAT.search(stripped))) and not at_cap
        if not continues:
            segments.append((buf_start, buf))
            buf = {}
            buf_start = None
            depth = 0
    if buf:
        segments.append((buf_start, buf))
    return segments


found = []
for path in sys.argv[1:]:
    is_tsx = path.endswith('.tsx')
    with open(path, encoding='utf-8', errors='replace') as f:
        src = f.read()
    src_lines = src.splitlines()
    lines_code = strip_ts_non_code(src)
    total_lines = len(src_lines)
    for seg_start, seg_map in build_logical_segments(lines_code, total_lines):
        joined = '\n'.join(seg_map[ln] for ln in sorted(seg_map))
        if not (line_has_money_identifier(joined) and line_has_float_pattern(joined, is_tsx)):
            continue
        # Reporta na linha onde o padrao de float de fato ocorre (mais
        # acionavel que a 1a linha do segmento); fallback = inicio do segmento.
        report_lineno = seg_start
        for ln in sorted(seg_map):
            frag = seg_map[ln]
            if FLOAT_CALL_PAT.search(frag) or FLOAT_LIT_PAT.search(frag):
                report_lineno = ln
                break
        orig = src_lines[report_lineno - 1] if report_lineno <= len(src_lines) else joined
        found.append(f"{path}:{report_lineno}:{orig}")

if found:
    for hit in found:
        print(hit)
    sys.exit(1)
PYEOF

hits=$(cat "$_tmpout")

if [ "$py_status" -ne 0 ] || [ -n "$hits" ]; then
    echo "float/Number/parseFloat proibido em codigo monetario TS/TSX (BFF/console, TX-2): use decimal.js ou bigint"
    echo "(backstop por CONTEUDO, independente do nome do arquivo — ver scripts/ci/no-float-ts.sh)"
    [ -n "$hits" ] && echo "$hits"
    exit 1
fi
echo "no-float-ts: ok"
