#!/usr/bin/env bash
# no-float-py.sh — TX-2: proibe float MONETARIO em codigo financeiro Python.
# Escopo financeiro: money/ledger/billing/payments (por DIRETORIO), mais
# ml/fraud e ml/pacing que declaram TX-2 e processam dados financeiros
# indiretamente. Dinheiro usa int64 minor-units. Ver contracts/lint/no-float.md.
#
# NOTA DE ESCOPO (29a onda #3): o glob '*billing*/*.py' casa "billing" num
# COMPONENTE DE DIRETORIO — NAO cobre arquivos billing-NOMEADOS fora de um dir
# "billing/" (ex.: data/iceberg/jobs/billing_batch_hourly.py, o motor canonico
# de CPM/CPC/CPA). Esses jobs de faturamento em data/iceberg/jobs sao cobertos
# pelo bloco Python string-aware de scripts/ci/no-float-data-sql.sh (que detecta
# float() E literal float nu em nomes monetarios, incl. 'rate' — seguro ali, sem
# os 'learning_rate' float de ML). Nao duplicar aqui p/ nao arriscar novo FP.
#
# DISTINCAO FLOAT MONETARIO vs FLOAT DE FEATURE (2026-06-08):
#   ml/fraud/*.py e ml/pacing/*.py contem float/float32/np.float LEGITIMOS
#   como vetores de feature ONNX/LightGBM/numpy — nao sao violacoes TX-2.
#   O gate varre por PADRAO MONETARIO: float aplicado a nomes financeiros
#   (amount, price, cpm, cpc, cpa, bid, budget, revenue, cost, minor_units,
#   money). Float puro em contexto de feature/ML nao e capturado.
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

# Escopo 1: dirs de contabilidade nominais (money/ledger/billing/payments)
# Escopo 2: ml/fraud e ml/pacing — declaram TX-2 e operam sobre dados financeiros
mapfile -t files < <(git ls-files \
    '*money*/*.py' '*ledger*/*.py' '*billing*/*.py' '*payments*/*.py' \
    'ml/fraud/*.py' 'ml/pacing/*.py' \
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

# Nomes financeiros que NUNCA devem receber float (TX-2).
# Cobre snake_case e variantes: amount, price, cpm, cpc, cpa, bid, budget,
# revenue, cost, minor_units, money (e plurais/compostos comuns).
# Float de feature ML (feature_vec, X_train, score, deficit, etc.) NAO casa.
# Fronteiras via lookaround (nao \b): \b trata '_' como caractere de palavra, entao
# '\bcost\b' NAO casa 'total_cost' (falso-negativo TX-2). Os lookarounds abaixo tratam
# '_' e alfanumericos como delimitadores -> pega composicoes snake_case (total_cost,
# ad_revenue, cost_minor_units) sem casar features ML (costume, pricey, bidirectional).
MONEY_NAMES = re.compile(
    r'(?<![A-Za-z0-9])(?:amount|price|cpm|cpc|cpa|bid|budget|revenue|cost|minor_units?|money'
    r'|spend|payout|charge|rate_money|billing_amount)(?![A-Za-z0-9])',
    re.IGNORECASE,
)

def line_has_monetary_float(line: str) -> bool:
    """
    Retorna True se a linha de codigo contiver float() ou um literal de ponto
    flutuante atribuido/aplicado a um nome financeiro.

    Logica: a linha tem um nome monetario E tem float() ou literal float.
    Float puro sem nome monetario na mesma linha nao e flagrado — preserva
    vetores de feature (np.float32, astype(np.float32), etc.).
    """
    has_money = bool(MONEY_NAMES.search(line))
    if not has_money:
        return False
    # Verifica se ha float() como chamada/cast ou literal numerico float
    has_float_call = bool(re.search(r'\bfloat\s*\(', line))
    has_float_lit  = bool(re.search(
        r'(?<![A-Za-z0-9_])([0-9]+\.[0-9]+|\.[0-9]+|[0-9]+[eE][+-]?[0-9]+)',
        line,
    ))
    return has_float_call or has_float_lit

found = []      # lista de strings "path:lineno:text"
seen  = set()   # conjunto (path, lineno) para deduplicar multiplos hits na mesma linha

for path in sys.argv[1:]:
    with open(path, encoding='utf-8', errors='replace') as f:
        src = f.read()
    lines = src.splitlines()

    try:
        tokens = list(tokenize.generate_tokens(io.StringIO(src).readline))
    except tokenize.TokenError:
        # Fallback para arquivo com erro de sintaxe: varre linha a linha
        for lineno, line in enumerate(lines, 1):
            code_part = line.split('#', 1)[0]
            if line_has_monetary_float(code_part):
                key = (path, lineno)
                if key not in seen:
                    seen.add(key)
                    found.append(f"{path}:{lineno}:{line.rstrip()}")
        continue

    # Varre linha a linha usando tokenize apenas para pular comentarios/strings
    # Agrupa tokens por linha; testa a linha de codigo limpa (sem comentarios/strings)
    skip_lines: set = set()
    for ttype, tstring, tstart, tend, _line in tokens:
        if ttype in (tok_mod.COMMENT, tok_mod.STRING):
            # Marca todas as linhas cobertas por este token como parcialmente comentadas.
            # Usamos abordagem mais simples: reconstruimos a linha sem esses tokens.
            pass  # tratado abaixo

    # Abordagem linha a linha: para cada linha do arquivo, reconstroi o trecho de
    # codigo sem tokens de comentario/string usando o tokenizer.
    line_code: dict = {i+1: list(lines[i]) for i in range(len(lines))}
    # Blinda strings e comentarios zerando seus caracteres na representacao de linha
    for ttype, tstring, tstart, tend, _line in tokens:
        if ttype not in (tok_mod.COMMENT, tok_mod.STRING):
            continue
        srow, scol = tstart
        erow, ecol = tend
        if srow == erow:
            for c in range(scol, min(ecol, len(line_code.get(srow, [])))):
                line_code[srow][c] = ' '
        else:
            # Multi-line string: zera da coluna inicial ate fim da primeira linha
            # e linhas intermediarias inteiras, e ate ecol na ultima linha
            row = srow
            for c in range(scol, len(line_code.get(row, []))):
                line_code[row][c] = ' '
            for row in range(srow+1, erow):
                line_code[row] = [' '] * len(line_code.get(row, []))
            row = erow
            for c in range(0, min(ecol, len(line_code.get(row, [])))):
                line_code[row][c] = ' '

    for lineno, chars in line_code.items():
        code_line = ''.join(chars)
        if line_has_monetary_float(code_line):
            key = (path, lineno)
            if key not in seen:
                seen.add(key)
                orig = lines[lineno-1].rstrip() if lineno <= len(lines) else code_line
                found.append(f"{path}:{lineno}:{orig}")

if found:
    for hit in found:
        print(hit)
    sys.exit(1)
PYEOF

hits=$(cat "$_tmpout")

if [ "$py_status" -ne 0 ] || [ -n "$hits" ]; then
    echo "float monetario proibido (Python/TX-2): use int64 minor-units ou decimal.Decimal"
    [ -n "$hits" ] && echo "$hits"
    exit 1
fi
echo "no-float-py: ok"
