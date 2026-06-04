#!/usr/bin/env bash
# no-float-sql.sh — TX-2: proibe FLOAT/DOUBLE/REAL/MONEY como tipo de coluna em SQL.
# Dinheiro usa NUMERIC(precision, scale) por ativo. Ver contracts/lint/no-float.md.
#
# Uso:
#   bash scripts/ci/no-float-sql.sh              # escaneia caminhos padrao via git ls-files
#   bash scripts/ci/no-float-sql.sh <dir>        # escaneia diretorio especifico recursivamente
#
# Exclusoes do scanner:
#   - Linhas de comentario SQL (contendo '--' antes do tipo proibido)
#   - DDL COMMENT ON
#   - Ocorrencias dentro de strings literais SQL ('')
set -euo pipefail

SCAN_DIR="${1:-}"

if [ -n "$SCAN_DIR" ]; then
    mapfile -t files < <(find "$SCAN_DIR" -name '*.sql' -type f 2>/dev/null | sort)
else
    mapfile -t files < <(git ls-files \
        'migrations/*.sql' 'db/migrations/*.sql' 'sql/*.sql' \
        'db/*/migrations/*.sql' 2>/dev/null | sort)
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

if [ $status -ne 0 ] || [ -n "$hits" ]; then
    echo "ERRO TX-2: tipo flutuante/MONEY proibido em declaracao de coluna SQL. Use NUMERIC(p,s)."
    echo "$hits"
    exit 1
fi
echo "no-float-sql: ok"
