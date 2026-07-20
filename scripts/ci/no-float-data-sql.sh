#!/usr/bin/env bash
# no-float-data-sql.sh — TX-2: proibe FLOAT/DOUBLE/REAL em colunas monetarias
# nos arquivos DDL de data/clickhouse/ e data/iceberg/.
#
# ESCOPO: colunas de tabelas ClickHouse e campos de specs Iceberg que
# representam valores monetarios (_value, _amount, _rate, _decimal).
# Float em colunas de ML (propensity, score, epsilon) e LEGITIMO e excluido.
#
# ESTRATEGIA: analisa apenas linhas de definicao de coluna DDL SQL
# (linhas que iniciam com nome_de_coluna seguido de tipo).
# Comentarios SQL (--..) e Python (#..) sao excluidos antes da analise.

set -euo pipefail

FAIL=0

# ---------------------------------------------------------------------------
# Normalizacao: junta definicao de coluna quebrada em 2+ linhas fisicas numa
# unica linha logica ANTES do match por-linha (TX-2).
#
# MOTIVACAO (achado #9, 28a onda): o match por-linha abaixo (nome_coluna +
# Float32/64 na MESMA linha) e escapado por um DDL formatado como:
#     conversion_value_broken
#         Float64,
# porque o nome da coluna e o tipo ficam em linhas fisicas diferentes.
#
# ESTRATEGIA (sem parser SQL completo, suficiente para DDL ClickHouse real):
#   uma linha indentada que contem SOMENTE um identificador valido (sem tipo,
#   sem virgula, sem parenteses) e tratada como o INICIO de uma definicao de
#   coluna cujo tipo continua na proxima linha nao-vazia; as duas linhas sao
#   concatenadas (preservando a indentacao original da 1a, exigida pelo
#   ancora '^\s+' do regex de match) numa unica linha logica.
# ---------------------------------------------------------------------------
join_wrapped_column_lines() {
    local f="$1"
    awk '
        {
            line = $0
            trimmed = line
            gsub(/^[ \t]+|[ \t]+$/, "", trimmed)
            if (pending_full != "") {
                if (trimmed == "") { next }  # aguarda proxima linha nao-vazia
                print pending_full " " trimmed
                pending_full = ""
                next
            }
            # Candidato a "nome de coluna sem tipo": indentado, so identificador,
            # sem virgula/parenteses/dois-pontos.
            if (line ~ /^[ \t]/ && trimmed ~ /^[A-Za-z_][A-Za-z0-9_]*$/) {
                pending_full = line
                next
            }
            print line
        }
        END { if (pending_full != "") print pending_full }
    ' "$f"
}

# ---------------------------------------------------------------------------
# Funcao auxiliar: strip de comentarios SQL e verifica tipo Float em colunas
# ---------------------------------------------------------------------------
check_sql_no_monetary_float() {
    local f="$1"
    # Extrai linhas que definem colunas com Float32/Float64 no ClickHouse.
    # Uma linha de definicao de coluna tipicamente tem formato:
    #   nome_coluna   Tipo   [DEFAULT ...] [COMMENT ...]
    # Excluir: linhas de comentario (-- ou #), linhas com ENGINE, ORDER BY, etc.
    # Incluir apenas linhas onde o segundo token e o tipo Float32 ou Float64.
    #
    # Estrategia: grep por padrao 'word Float(32|64)' excluindo comentarios,
    # sobre o conteudo NORMALIZADO (join_wrapped_column_lines) para que uma
    # coluna monetaria com tipo Float quebrada em 2 linhas fisicas nao escape.
    local normalized
    normalized="$(join_wrapped_column_lines "$f")"

    local hits
    # DEFAULT-DENY sobre o TIPO (30a onda, 4a rodada, achado tech-lead HIGH):
    # a forma antiga decidia "isto e dinheiro" por uma DENYLIST enumerada a
    # mao de nomes (value|amount|rate|decimal|revenue|cost|budget|price|cpm|
    # cpc|cpa|bid|spend|payout|charge|billing|money|minor_units) — uma coluna
    # monetaria com nome FORA do vocabulario (ex.: gross_earnings,
    # receita_liquida, gmv) escapava POR CONSTRUCAO. E a mesma classe de
    # scope-blindspot que as ondas 28a/29a ja tinham declarado licao-mae:
    # ampliar a lista so adia o proximo nome que faltar.
    #
    # FIX: inverte a forma. TODA coluna Float32/64 no escopo (data/clickhouse)
    # dispara por padrao; libera-se apenas por EXCECAO EXPLICITA — reusa o
    # vocabulario de ML ja existente (propensity|score|epsilon|pct|
    # probability) e o marcador por-linha `no-float-ok` (ja usado nas
    # migrations reais: ivt_prob, if_score, ae_error, ae_threshold). Colunas
    # monetarias fora do vocabulario ML e sem marcador agora SAO pegas —
    # exatamente o caso que o gate antigo deixava passar.
    #
    # WRAPPER-TRANSPARENTE (achado no-float-clickhouse-nullable-wrapper-escapes,
    # CRITICAL, herdado): o regex exige 'Float(32|64)' colado ao nome OU
    # dentro de wrapper(s) `Nullable(`/`LowCardinality(`/`Array(` (inclusive
    # aninhados) — `(?:[A-Za-z]+\(\s*)*` consome zero ou mais prefixos de
    # wrapper antes de exigir Float32/64, tratando o wrapper como
    # transparente em vez de opaco.
    hits=$(printf '%s\n' "$normalized" | grep -inE '^\s+[a-z_][a-z0-9_]*\s+(([A-Za-z]+\(\s*)*)Float(32|64)' \
           | grep -viE 'no-float-ok' \
           | grep -viE 'propensity|score|epsilon|pct|probability' \
           || true)
    if [ -n "$hits" ]; then
        echo "ERRO no-float-data-sql: Float32/64 em coluna de $f sem excecao de nome ML nem marcador no-float-ok (DEFAULT-DENY, TX-2; apos normalizar linhas quebradas):"
        echo "$hits"
        echo "  Se e dinheiro: use Decimal(38,18) ou int64 minor-units. Se e float legitimo"
        echo "  (ML/probabilidade/metrica), marque com '-- no-float-ok: <motivo>' na mesma linha."
        return 1
    fi

    # Detecta pow() em contexto de MATERIALIZED/coluna monetaria (TX-2).
    # pow() retorna Float64 e contamina billing quando usado em MATERIALIZED
    # de colunas como conversion_value_decimal/_amount (ex.: pow(10, scale)).
    # Usos legítimos de pow() em contexto nao-monetario (propensity, score)
    # sao excluidos pelo grep -vE abaixo.
    local pow_hits
    pow_hits=$(grep -inE 'MATERIALIZED.*pow\s*\(|pow\s*\(.*conversion_value' "$f" \
               | grep -vE '^\s*--' \
               | grep -vE 'propensity|score|epsilon|pct|probability' \
               || true)
    if [ -n "$pow_hits" ]; then
        echo "ERRO no-float-data-sql: pow() em contexto MATERIALIZED/billing em $f (retorna Float64, TX-2):"
        echo "$pow_hits"
        echo "  Use multiIf dispatch com literais inteiros 10^N em vez de pow()."
        return 1
    fi

    return 0
}

# ---------------------------------------------------------------------------
# SQL: ClickHouse DDL
# ---------------------------------------------------------------------------
SQL_FILES=$(find "data/clickhouse" -name "*.sql" 2>/dev/null | sort || true)
for f in $SQL_FILES; do
    [ -f "$f" ] || continue
    check_sql_no_monetary_float "$f" || FAIL=1
done

# ---------------------------------------------------------------------------
# YAML (specs Iceberg): DEFAULT-DENY sobre o TIPO — qualquer tipo de ponto
# flutuante Iceberg em campo, liberado so por excecao EXPLICITA de nome.
#
# 30a onda, 4a rodada (achado tech-lead, HIGH): o guard antigo casava apenas
# `type: float` — mas o tipo de ponto flutuante de 64 bits do Iceberg chama-se
# 'double' (a spec de tipos primitivos Iceberg define exatamente DOIS tipos de
# ponto flutuante: 'float' [32 bits] e 'double' [64 bits]; nao ha 'float32'/
# 'float64' como nomes de tipo Iceberg). PROVADO por mutacao pelo tech lead:
# `rate_amount: decimal(38, 18)` -> `type: double` em billing_hourly.yaml (a
# TAXA CONTRATUAL de faturamento) saia com `no-float-data-sql: ok` e EXIT=0.
#
# FIX: casa 'float' E 'double' (cobre o conjunto INTEIRO de tipos de ponto
# flutuante do Iceberg) e inverte a forma: em vez de exigir que o NOME do
# campo contenha vocabulario monetario para disparar (allowlist por
# construcao — o mesmo scope-blindspot das ondas 28a/29a), agora QUALQUER
# campo com tipo float/double dispara por padrao. A unica excecao e NOMEADA
# e EXPLICITA: (a) o nome do campo (`- name: ...` mais proximo, rastreado
# associando cada `type:` ao ultimo `- name:` visto — suficiente para a
# estrutura plana `- name: / type:` real destas specs, sem parser YAML
# completo) casa o vocabulario de ML/probabilidade (propensity|score|
# epsilon|pct|probability|prob — 'prob' cobre `ivt_prob`, unico campo
# double hoje no corpus, cujo comentario de bloco ja documenta ser
# probabilidade de classificacao IVT, TX-2 N/A); OU (b) a linha do `type:`
# carrega o marcador `no-float-ok` (mesma convencao do bloco SQL acima e de
# `scripts/ci/no-float-sql.sh`, greppavel e auditavel).
# ---------------------------------------------------------------------------
YAML_FILES=$(find "data/iceberg/specs" -name "*.yaml" 2>/dev/null | sort || true)
for f in $YAML_FILES; do
    [ -f "$f" ] || continue
    hits=$(awk '
        BEGIN { fname = "" }
        /^[[:space:]]*#/ { next }
        /^[[:space:]]*-[[:space:]]*name:[[:space:]]*/ {
            line = $0
            sub(/^[[:space:]]*-[[:space:]]*name:[[:space:]]*/, "", line)
            sub(/[[:space:]]*#.*$/, "", line)
            fname = line
            next
        }
        /^[[:space:]]*type:[[:space:]]*(float|double)[[:space:]]*(#.*)?$/ {
            lc_line = tolower($0)
            lc_name = tolower(fname)
            if (lc_line ~ /no-float-ok/) { next }
            if (lc_name ~ /(propensity|score|epsilon|pct|probability|prob)/) { next }
            printf "%s: %s\n", fname, $0
        }
    ' "$f" || true)
    if [ -n "$hits" ]; then
        echo "ERRO no-float-data-sql: tipo de ponto flutuante Iceberg (float/double) em campo $f, sem excecao de nome ML nem marcador no-float-ok (DEFAULT-DENY, TX-2):"
        echo "$hits"
        echo "  Use 'type: decimal(38, 18)' para valores monetarios (TX-2)."
        FAIL=1
    fi
done

# ---------------------------------------------------------------------------
# Python (billing jobs em data/iceberg/jobs): float MONETARIO — float() OU
# literal float NU — em variaveis financeiras.
#
# 29a onda #3/#13: o grep antigo tinha DOIS buracos que deixavam o motor
# canonico de faturamento (billing_batch_hourly.py) escapar (provado por
# mutacao: `make no-float` saia 0):
#   (1) vocabulario estreito (value|amount|rate) — 'spend/cost/revenue/budget/
#       cpm/cpc/cpa/...' escapavam. Ampliado para espelhar o bloco SQL/no-float-py.
#   (2) so casava `\bfloat\s*\(` — `rate = Decimal(10.00)` (literal float NU
#       alimentando Decimal, a armadilha classica TX-2) NAO tem 'float(' e
#       passava verde. Agora tambem casa literal float nu.
# STRING-AWARE (tokenizer stdlib): `rate = Decimal("10.00")` (string DECIMAL
# LEGITIMA) tem o literal DENTRO de string -> ignorado; `rate = Decimal(10.00)`
# (nu, em codigo) -> flagrado. Escopo contabil restrito a data/iceberg/jobs, onde
# 'rate' e sempre dinheiro (CPM/CPC/CPA) — nao ha os 'learning_rate' float de ML.
# ---------------------------------------------------------------------------
PY_FILES=$(find "data/iceberg/jobs" -name "*.py" 2>/dev/null | sort || true)
_PY_MONEY='(value|amount|rate|revenue|cost|budget|price|cpm|cpc|cpa|bid|spend|payout|charge|billing|money|minor_units|decimal)'
for f in $PY_FILES; do
    [ -f "$f" ] || continue
    hits=$(python3 - "$f" "$_PY_MONEY" <<'PYEOF' || true
import sys, re, tokenize, io, token as tok_mod
path, money = sys.argv[1], sys.argv[2]
money_re = re.compile(money, re.IGNORECASE)
float_call = re.compile(r'\bfloat\s*\(')
float_lit = re.compile(r'(?<![A-Za-z0-9_.])([0-9]+\.[0-9]+|\.[0-9]+|[0-9]+[eE][+-]?[0-9]+)')
# DEFAULT-DENY (29a onda #3/#13, ajuste pós-barreira money-ledger): NAO ha exclusao
# por-linha de termos ML. Escopo = data/iceberg/jobs (billing puro); dinheiro aqui e
# SEMPRE Decimal/int64, entao um nome monetario + float na MESMA linha e SEMPRE bug
# (mesmo que um termo ML co-ocorra, ex.: `payout_rate = float(base_rate)*score_mult`).
# Uma exclusao por-linha ("pula a linha se contem 'score'") mascarava esse float
# monetario real — a mesma classe da tautologia "substring por-linha" das ondas 27/28.
# Se algum dia surgir um float legitimo num nome monetario aqui, use allowlist explicita.
src = open(path, encoding='utf-8', errors='replace').read()
lines = src.splitlines()
# Blinda comentarios e strings (incl. docstrings multi-linha) preservando o lineno.
code = {i + 1: list(lines[i]) for i in range(len(lines))}
tokens = []
try:
    tokens = list(tokenize.generate_tokens(io.StringIO(src).readline))
except tokenize.TokenError:
    tokens = []  # fail-open no lexer; cai no backstop por-linha-fisica abaixo

for t in tokens:
    if t.type not in (tok_mod.COMMENT, tok_mod.STRING):
        continue
    (sr, sc), (er, ec) = t.start, t.end
    if sr == er:
        for c in range(sc, min(ec, len(code.get(sr, [])))):
            code[sr][c] = ' '
    else:
        for c in range(sc, len(code.get(sr, []))):
            code[sr][c] = ' '
        for r in range(sr + 1, er):
            code[r] = [' '] * len(code.get(r, []))
        for c in range(0, min(ec, len(code.get(er, [])))):
            code[er][c] = ' '

# JANELA DE ANALISE: co-ocorrencia por LINHA LOGICA Python, nao por linha
# fisica isolada (achado no-float-python-billing-multiline-literal-escapes,
# HIGH — mesma raiz do achado CRITICAL no-float-multiline-split-defeats-
# cooccurrence do bloco irmao no-float-py.sh). O match por-linha-fisica
# original era derrotado por QUALQUER quebra de linha do Black:
#   billing_rate_broken = (
#       12.50
#   )
# FIX: usa o proprio tokenizer da stdlib CPython para achar os limites de
# LINHA LOGICA (nao linha fisica) — o tokenizer ja sabe, sem heuristica
# textual nenhuma, que uma expressao entre parenteses/colchetes/chaves
# abertos ou uma linha com continuacao '\\' NAO fecha a linha logica
# (emite token NL, nao NEWLINE).
def build_logical_line_ranges(toks):
    ranges = []
    seg_start = None
    seg_end = None
    for tt in toks:
        if tt.type in (tok_mod.ENCODING, tok_mod.ENDMARKER, tok_mod.INDENT, tok_mod.DEDENT):
            continue
        if tt.type == tok_mod.NL and seg_start is None:
            continue
        if seg_start is None:
            seg_start = tt.start[0]
        seg_end = tt.end[0] if seg_end is None else max(seg_end, tt.end[0])
        if tt.type == tok_mod.NEWLINE:
            ranges.append((seg_start, seg_end))
            seg_start = None
            seg_end = None
    if seg_start is not None:
        ranges.append((seg_start, seg_end))
    return ranges

out = []
if tokens:
    for seg_start, seg_end in build_logical_line_ranges(tokens):
        joined = '\n'.join(''.join(code.get(ln, [])) for ln in range(seg_start, seg_end + 1))
        if not (money_re.search(joined) and (float_call.search(joined) or float_lit.search(joined))):
            continue
        report_ln = seg_start
        for ln in range(seg_start, seg_end + 1):
            frag = ''.join(code.get(ln, []))
            if float_call.search(frag) or float_lit.search(frag):
                report_ln = ln
                break
        out.append(f"{path}:{report_ln}:{lines[report_ln - 1].rstrip()}")
else:
    # fail-open no lexer (erro de sintaxe): backstop conservador por linha fisica.
    for ln in sorted(code):
        s = ''.join(code[ln])
        if money_re.search(s) and (float_call.search(s) or float_lit.search(s)):
            out.append(f"{path}:{ln}:{lines[ln - 1].rstrip()}")
if out:
    print("\n".join(out))
    sys.exit(1)
PYEOF
)
    if [ -n "$hits" ]; then
        echo "ERRO no-float-data-sql: float monetario (float() ou literal nu) em variavel financeira em $f:"
        echo "$hits"
        FAIL=1
    fi
done

if [ "$FAIL" -eq 0 ]; then
    echo "no-float-data-sql: ok — nenhum float em colunas monetarias de data/"
fi

exit "$FAIL"
