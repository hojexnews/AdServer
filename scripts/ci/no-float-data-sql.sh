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
    # Estrategia: grep por padrao 'word Float(32|64)' excluindo comentarios.
    local hits
    hits=$(grep -inE '^\s+[a-z_][a-z0-9_]*\s+Float(32|64)' "$f" \
           | grep -iE '(value|amount|rate|decimal)' \
           | grep -vE 'propensity|score|epsilon|pct|probability' \
           || true)
    if [ -n "$hits" ]; then
        echo "ERRO no-float-data-sql: Float em coluna monetaria em $f:"
        echo "$hits"
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
# Python (billing jobs): float() ou literais float em variaveis monetarias
# Escopo restrito: nome de variavel contem _value, _amount, _rate
# Exclui linhas de comentario (#) e strings de docstring.
# ---------------------------------------------------------------------------
PY_FILES=$(find "data/iceberg/jobs" -name "*.py" 2>/dev/null | sort || true)
for f in $PY_FILES; do
    [ -f "$f" ] || continue
    # Detecta: nome_monetario = float(...) ou nome_monetario: float
    hits=$(grep -inE '(value|amount|rate)\s*[:=].*\bfloat\s*\(' "$f" \
           | grep -v '^\s*#' \
           | grep -v '^\s*"""' \
           || true)
    if [ -n "$hits" ]; then
        echo "ERRO no-float-data-sql: float() em variavel monetaria em $f:"
        echo "$hits"
        FAIL=1
    fi
done

if [ "$FAIL" -eq 0 ]; then
    echo "no-float-data-sql: ok — nenhum float em colunas monetarias de data/"
fi

exit "$FAIL"
