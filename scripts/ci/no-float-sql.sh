#!/usr/bin/env bash
# no-float-sql.sh — TX-2: proibe FLOAT/DOUBLE/REAL/MONEY como tipo de coluna em SQL.
# Dinheiro usa NUMERIC(precision, scale) por ativo. Ver contracts/lint/no-float.md.
#
# Uso:
#   bash scripts/ci/no-float-sql.sh              # escaneia TODO .sql rastreado (default-deny)
#   bash scripts/ci/no-float-sql.sh <dir>        # escaneia diretorio especifico recursivamente
#
# Exclusoes do scanner:
#   - Linhas de comentario SQL (contendo '--' antes do tipo proibido)
#   - DDL COMMENT ON
#   - Ocorrencias dentro de strings literais SQL ('')
set -euo pipefail

SCAN_DIR="${1:-}"

# DEFAULT-DENY (achado no-float-sql-blind-to-tests-and-seed, 30a onda): o modo
# sem argumento (usado por .github/workflows/no-float.yml e por `make
# no-float`) tem de cobrir TODO .sql rastreado no repo, e nao so uma
# allowlist de padroes de migrations. Uma allowlist por padrao de caminho
# (antes: 'migrations/*.sql' 'db/migrations/*.sql' 'sql/*.sql'
# 'db/*/migrations/*.sql') deixa escapar qualquer coisa fora da convencao —
# foi assim que db/*/tests/*.sql (rls_isolation_test.sql,
# postings_immutability_test.sql, vector_rls_isolation_test.sql) e
# db/seed/*.sql (dev_seed.sql, dev_roles.sql, hojex_news_seed.sql — o de
# maior risco monetario real, insere em ledger/config) nunca foram escaneados
# pela CI nem por `make no-float`.
#
# UNICA exclusao (por DIRETORIO, nao por nome de arquivo/convencao): tudo sob
# data/clickhouse/ e data/iceberg/, que ja e coberto pelo script irmao
# scripts/ci/no-float-data-sql.sh. Esse script tem logica de excecao
# consciente de NOME DE COLUNA para floats de ML legitimos (propensity,
# ivt_prob, if_score, ae_error, ae_threshold) que nao carregam o marcador
# 'no-float-ok' na mesma linha fisica — o matcher generico abaixo os
# sinalizaria como falso-positivo. Double-scan dessas duas arvores com
# convencoes de excecao incompativeis quebraria o baseline verde sem
# corrigir defeito real algum.
if [ -n "$SCAN_DIR" ]; then
    mapfile -t files < <(find "$SCAN_DIR" -name '*.sql' -type f 2>/dev/null | sort)
else
    mapfile -t files < <(git ls-files '*.sql' 2>/dev/null \
        | grep -v -E '^(data/clickhouse/|data/iceberg/)' \
        | sort)
fi

if [ "${#files[@]}" -eq 0 ]; then
    echo "no-float-sql: nenhuma migracao SQL (ok)"
    exit 0
fi

# Usamos Python para filtrar com precisao:
#   - ignora linhas de comentario (strip -> startswith('--'))
#   - ignora COMMENT ON
#   - remove strings SQL literais ('...') antes de testar o padrao
#   - COMMENT-AWARE: remove o comentario inline ('-- ...') antes de testar o
#     tipo, de modo que so o codigo DDL real dispara o alarme.
#   - OPT-OUT EXPLICITO: uma coluna nao-monetaria que precise de FLOAT/DOUBLE
#     (ex.: 'ctr' como taxa de ML/ranking, NAO dinheiro) carrega o marcador
#     'no-float-ok' no comentario inline da propria linha. E greppavel e
#     auditavel — mesma filosofia do //nolint do guard Go.
#   Assim 'Real Brasileiro' nao dispara o alarme; REAL como tipo DDL dispara.
# set +e/-e ao redor da substituicao de comando: sob 'set -e', quando o
# python3 sai com status 1 (hits encontrados) dentro de 'hits=$(...)',
# o bash aborta o script IMEDIATAMENTE apos a atribuicao — o corpo do
# 'if' abaixo (a mensagem "ERRO TX-2" e a lista de hits) NUNCA roda.
# O exit code final ainda e correto (1), mas o operador nao ve NADA no
# stdout/stderr, so o exit code — falha muda que engana quem le so a
# saida. Desliga errexit so para esta atribuicao, religa em seguida.
set +e
hits=$(python3 - "${files[@]}" << 'PYEOF'
import sys, re

# Padrao de tipos proibidos como palavras SQL (nao dentro de strings)
TYPE_PAT = re.compile(r'\b(float\d*|double\s+precision|real|money)\b', re.IGNORECASE)
# Remove strings SQL literais da linha antes de testar
STR_LIT  = re.compile(r"'[^']*'")
# Marcador explicito de opt-out (coluna nao-monetaria documentada)
ALLOW_PAT = re.compile(r'no-float-ok', re.IGNORECASE)

found = []
for path in sys.argv[1:]:
    with open(path, encoding='utf-8', errors='replace') as f:
        for lineno, raw in enumerate(f, 1):
            line = raw.rstrip('\n')
            stripped = line.lstrip()
            # Pula comentarios SQL puros e COMMENT ON
            if stripped.startswith('--'):
                continue
            if re.match(r'COMMENT\s+ON', stripped, re.IGNORECASE):
                continue
            # Remove strings literais antes de separar codigo de comentario
            cleaned = STR_LIT.sub("''", line)
            # Separa o codigo DDL do comentario inline ('-- ...')
            comment_idx = cleaned.find('--')
            if comment_idx >= 0:
                code, comment = cleaned[:comment_idx], cleaned[comment_idx:]
            else:
                code, comment = cleaned, ''
            # Opt-out explicito e documentado na propria linha
            if ALLOW_PAT.search(comment):
                continue
            # So o codigo DDL real (sem comentario) dispara o alarme
            if TYPE_PAT.search(code):
                found.append(f"{path}:{lineno}:{line}")

if found:
    for hit in found:
        print(hit)
    sys.exit(1)
PYEOF
)
status=$?
set -e

if [ $status -ne 0 ] || [ -n "$hits" ]; then
    echo "ERRO TX-2: tipo flutuante/MONEY proibido em declaracao de coluna SQL. Use NUMERIC(p,s)."
    echo "$hits"
    exit 1
fi
echo "no-float-sql: ok"
