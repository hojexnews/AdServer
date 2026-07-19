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
#
# LITERAL DECIMAL / FLOAT IMPLICITO (2026-07-19, 27a onda #5):
#   forbidigo (\bfloat32\b/\bfloat64\b) e o check acima sao cegos a float
#   IMPLICITO: `price := 12.50` ou `total * 0.9` em codigo puro-dinheiro
#   passam verde porque o TOKEN "float64" nunca aparece no fonte — Go infere
#   o tipo a partir do literal. Dinheiro em Go e' inteiro (minor-units); um
#   literal decimal (`[0-9]+\.[0-9]+`) em codigo executavel de um pacote
#   puro-dinheiro e' o proprio smell, independente de aparecer o token
#   "float64". Adicionamos um segundo check, DECIMAL_PAT, sobre o MESMO
#   fragmento ja limpo de comentario/string (reaproveita strip_go_non_code)
#   nos MESMOS arquivos financeiros do check acima.
#
#   ESCOPO ESTENDIDO: o gate de literal decimal tambem cobre
#   internal/ranker/score.go — o unico "money-point" fora dos diretorios
#   puro-dinheiro (eCPM = pCTR x bid, ver .golangci.yml + doc do arquivo).
#   score.go NAO entra no check de TOKEN float32/float64 acima (linhas 86/91/99
#   usam float32/float64 legitimamente, com //nolint:forbidigo documentado, e
#   o backstop grep nunca cobriu esse arquivo) — so no check de literal
#   decimal, onde nenhum float legitimo do arquivo hoje usa literal decimal
#   (a arithmetic e' toda sobre variaveis pCTR/bidMinorUnits, nao constantes).
#
#   FALSOS-POSITIVOS EVITADOS: numeros de versao (`protoc-gen-go v1.36.11`),
#   exemplos em comentario (`R$10 revenue`) e strings JSON (`"150.00"`) ficam
#   dentro de comentario/string e sao removidos pelo MESMO strip usado no
#   check de token — nao precisam de allowlist adicional (auditado nas 27a
#   onda: todo hit bruto de `[0-9]+\.[0-9]+` nos arquivos em escopo, antes do
#   strip, caia em comentario/string; zero ocorrencia em codigo executavel).
set -euo pipefail

mapfile -t float_files < <(git ls-files \
    '*money*/*.go' '*ledger*/*.go' '*billing*/*.go' '*payments*/*.go' \
    '*chainconnector*/*.go' \
    2>/dev/null | sort)

# Arquivos money-building fora dos globs por-diretorio (29a onda #1):
# internal/configload/{loader,assemble}.go convertem NUMERIC->minor-units
# (decimalToMinor) e montam o moneyv1.Money.Amount AUTORITATIVO a partir do
# Postgres. O nome do diretorio ("configload") nao casa os globs financeiros,
# mas o INVARIANTE TX-2 (sem float no dinheiro Go) vale aqui — escopar por
# NOME de diretorio era o ponto-cego da licao-mae da 28a onda. Full check
# (token float32/float64 + literal decimal): o pacote nao tem float legitimo.
for _mf in internal/configload/loader.go internal/configload/assemble.go; do
    if git ls-files --error-unmatch "$_mf" >/dev/null 2>&1; then
        float_files+=("$_mf")
    fi
done

# Arquivos money-critical adicionais que NAO caem nos globs por-diretorio
# acima, mas participam do check de LITERAL DECIMAL implicito (nao do check
# de token float32/float64 — ver nota 27a onda #5 acima).
decimal_only_files=()
if git ls-files --error-unmatch internal/ranker/score.go >/dev/null 2>&1; then
    decimal_only_files+=("internal/ranker/score.go")
fi

if [ "${#float_files[@]}" -eq 0 ] && [ "${#decimal_only_files[@]}" -eq 0 ]; then
    echo "no-float-go: nenhum arquivo financeiro Go (ok)"
    exit 0
fi

_tmpout=$(mktemp)
trap 'rm -f "$_tmpout"' EXIT

py_status=0
python3 - "${float_files[@]}" -- "${decimal_only_files[@]}" > "$_tmpout" << 'PYEOF' || py_status=$?
import sys, re

FLOAT_PAT   = re.compile(r'\bfloat(32|64)\b')
# Literal decimal: digito(s) + ponto + digito(s), delimitado por nao-word/nao-ponto
# nas duas pontas (evita casar dentro de versoes tipo "v1.36.11" — essas ja
# ficam em comentario/string e sao removidas pelo strip antes de chegar aqui;
# o delimitador so protege contra colar em sequencias tipo "1.2.3" reportando
# so o par "1.2", o que ainda e' o comportamento correto: um hit basta).
DECIMAL_PAT = re.compile(r'(?<![\w.])[0-9]+\.[0-9]+(?!\w)')

# Separa os dois grupos de arquivos pelo marcador "--".
_args = sys.argv[1:]
if '--' in _args:
    _sep = _args.index('--')
    float_files = _args[:_sep]
    decimal_only_files = _args[_sep + 1:]
else:
    float_files = _args
    decimal_only_files = []

float_file_set = set(float_files)
all_files = list(dict.fromkeys(float_files + decimal_only_files))  # uniao, preserva ordem

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
for path in all_files:
    with open(path, encoding='utf-8', errors='replace') as f:
        src = f.read()
    src_lines = src.splitlines()
    lines_code = strip_go_non_code(src)
    for lineno in sorted(lines_code):
        fragment = ''.join(lines_code[lineno])
        reasons = []
        if path in float_file_set and FLOAT_PAT.search(fragment):
            reasons.append('token float32/float64')
        if DECIMAL_PAT.search(fragment):
            reasons.append('literal decimal implicito')
        if reasons:
            orig = src_lines[lineno - 1] if lineno <= len(src_lines) else fragment
            found.append(f"{path}:{lineno}:[{', '.join(reasons)}]:{orig}")

if found:
    for hit in found:
        print(hit)
    sys.exit(1)
PYEOF

hits=$(cat "$_tmpout")

if [ "$py_status" -ne 0 ] || [ -n "$hits" ]; then
    echo "float proibido em codigo monetario (Go/TX-2): use Money/int64 ou shopspring/decimal"
    echo "(inclui literal decimal implicito — dinheiro em Go e' inteiro, minor-units)"
    [ -n "$hits" ] && echo "$hits"
    exit 1
fi
echo "no-float-go: ok"
