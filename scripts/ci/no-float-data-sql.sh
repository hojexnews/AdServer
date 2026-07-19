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
    # Vocabulario monetario ampliado (espelha scripts/ci/no-float-py.sh) — antes so
    # (value|amount|rate|decimal), o que deixava 'revenue/cost/budget/... Float64' escapar (TX-2).
    hits=$(printf '%s\n' "$normalized" | grep -inE '^\s+[a-z_][a-z0-9_]*\s+Float(32|64)' \
           | grep -iE '(value|amount|rate|decimal|revenue|cost|budget|price|cpm|cpc|cpa|bid|spend|payout|charge|billing|money|minor_units)' \
           | grep -vE 'propensity|score|epsilon|pct|probability' \
           || true)
    if [ -n "$hits" ]; then
        echo "ERRO no-float-data-sql: Float em coluna monetaria em $f (apos normalizar linhas quebradas):"
        echo "$hits"
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
# YAML (specs Iceberg): verificar tipo 'float' em campos monetarios
# Campos monetarios identificados por nome (_value, _amount, _rate, _decimal)
# ---------------------------------------------------------------------------
YAML_FILES=$(find "data/iceberg/specs" -name "*.yaml" 2>/dev/null | sort || true)
for f in $YAML_FILES; do
    [ -f "$f" ] || continue
    # Procura linhas 'type: float' (tipo Iceberg invalido para dinheiro)
    # Excluir linhas de comentario YAML (#)
    hits=$(grep -inE '^\s*type:\s*float\s*$' "$f" || true)
    if [ -n "$hits" ]; then
        echo "ERRO no-float-data-sql: tipo float em spec Iceberg $f:"
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
try:
    for t in tokenize.generate_tokens(io.StringIO(src).readline):
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
except tokenize.TokenError:
    pass  # fail-open no lexer; o backstop de token ainda roda linha a linha abaixo
out = []
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
