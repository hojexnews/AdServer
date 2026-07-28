#!/usr/bin/env bash
# no-float-proto.sh — TX-2: proibe float/double em campos .proto monetarios.
#
# ESCOPO: TODO o Schema Registry (`proto/adserver/**/*.proto`), nao apenas
# money/**  e payments/**. Motivo (falso-positivo corrigido): um campo
# monetario pode ser adicionado a QUALQUER mensagem do contrato — ex.:
# `Conversion.conversion_value` (telemetry/v1/events.proto) carrega
# `adserver.money.v1.Money`, mas nada impedia alguem de adicionar um campo
# NOVO `double` diretamente ali (ou em decision.proto/envelope.proto) sem
# passar por Money. `buf breaking` (WIRE_JSON) so protege um campo
# EXISTENTE de trocar de tipo; um campo aditivo com `double`/`float` passa
# despercebido por qualquer outro gate deste repo. Ver
# contracts/lint/no-float.md e proto/adserver/money/v1/money.proto (regras
# invioláveis).
#
# DEFAULT-DENY COM ALLOWLIST EXPLICITA (inverte o onus):
# Varrer o contrato inteiro pegaria tambem floats LEGITIMOS de ML/ranking
# que nao sao dinheiro (ex.: `propensity`, `score`, `epsilon` em
# decision/v1/decision.proto — sinais de OPE/bandit, TX-2 nao se aplica).
# Em vez de reintroduzir um escopo permissivo por diretorio (o bug
# original), cada campo double/float fora de money/payments precisa estar
# EXPLICITAMENTE listado em ALLOWLIST abaixo, chaveado por
# (arquivo, nome_da_MENSAGEM, nome_do_campo, numero_da_tag) — o numero da
# tag e imutavel por TX-1 (nunca reutilizado/renumerado) MAS e por-MENSAGEM
# em proto3 (cada `message` tem seu proprio espaco 1..N), entao o nome da
# mensagem tem de entrar na chave: sem ele, uma mensagem NOVA que colida por
# coincidencia com (campo, tag) de uma mensagem ja revisada seria liberada
# em silencio. Com a chave de 4 elementos, a allowlist so vale para o campo
# revisado exato; um campo NOVO (mesmo com o mesmo nome/tag, em outra
# mensagem) exige nova entrada explicita e revisao. A lista e greppavel/auditavel via
# `git log -p -- scripts/ci/no-float-proto.sh` — mesma filosofia do
# `no-float-ok` do guard SQL e dos `//nolint:forbidigo` do guard Go.
#
# COMMENT-AWARE: remove comentarios proto (//, /* ... */) e string literals
# ("...", '...') antes de testar \b(double|float)\b, para nao disparar
# falso-positivo nos proprios comentarios que DOCUMENTAM a proibicao (ex.:
# "FLOAT E PROIBIDO" no cabecalho de money.proto, ou "DOUBLE: sinal de ML"
# em decision.proto).
set -euo pipefail

mapfile -t files < <(git ls-files 'proto/adserver/**/*.proto' 2>/dev/null | sort)

if [ "${#files[@]}" -eq 0 ]; then
    echo "no-float-proto: nenhum arquivo .proto em proto/adserver (ok)"
    exit 0
fi

_tmpout=$(mktemp)
trap 'rm -f "$_tmpout"' EXIT

py_status=0
python3 - "${files[@]}" > "$_tmpout" << 'PYEOF' || py_status=$?
import sys, re

FLOAT_PAT = re.compile(r'\b(double|float)\b', re.IGNORECASE)
# Extrai "nome_do_campo = numero_da_tag ;" do fragmento de codigo (ja sem
# comentarios/strings) de uma linha de declaracao de campo proto3. Cobre
# escalar (`double x = 4;`), `repeated double x = 4;` e
# `map<string, double> x = 4;` — em todos os casos o campo termina com
# `nome = numero;`.
FIELD_NAME_NUM_PAT = re.compile(r'([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(\d+)\s*;')

# ALLOWLIST explicita e auditavel: (caminho_relativo, nome_da_MENSAGEM,
# nome_do_campo, numero_da_tag) -> justificativa. Cada entrada e um campo
# NAO-monetario revisado (sinal de ML/ranking/OPE), documentado no proprio
# .proto com comentario "DOUBLE: ... nao dinheiro". Adicionar uma entrada
# aqui exige revisao humana explicita (inverte o onus vs. escopo permissivo
# por dir).
#
# POR QUE O NOME DA MENSAGEM ENTRA NA CHAVE: em proto3 a numeracao de tags e
# POR-MENSAGEM (cada `message` tem seu proprio espaco 1..N) — e normal e
# esperado que mensagens DIFERENTES no MESMO arquivo reusem por coincidencia
# o mesmo par (nome_do_campo, tag) (ex.: outra mensagem tambem ter um campo
# `score = 4`). Uma chave (arquivo, campo, tag) SEM o nome da mensagem
# libera silenciosamente qualquer mensagem NOVA que colida por acaso com uma
# entrada ja revisada — contradizendo a garantia de "so vale para o campo
# revisado exato". A chave de 4 elementos fecha essa lacuna.
ALLOWLIST = {
    # decision/v1/decision.proto, message Candidate — score: eCPM/score do
    # ranker (ML), nao Money. Comparacao so dentro do mesmo ativo/tenant (TX-2).
    ("proto/adserver/decision/v1/decision.proto", "Candidate", "score", "4"),
    # decision/v1/decision.proto, message Candidate — propensity: probabilidade
    # de OPE/IPS/DR sob a politica de logging, nao dinheiro.
    ("proto/adserver/decision/v1/decision.proto", "Candidate", "propensity", "5"),
    # decision/v1/decision.proto, message Decision — propensity: propensao da
    # acao SERVIDA (nucleo do loop de atribuicao/off-policy), nao dinheiro.
    ("proto/adserver/decision/v1/decision.proto", "Decision", "propensity", "6"),
    # decision/v1/decision.proto, message Decision — epsilon: hiperparametro
    # de exploracao (epsilon-greedy), nao dinheiro.
    ("proto/adserver/decision/v1/decision.proto", "Decision", "epsilon", "8"),
}

# Reconhece a ABERTURA de uma mensagem ("message Nome {") no fragmento de
# codigo (ja sem comentarios/strings) de uma linha, para manter uma PILHA de
# mensagens correntes (suporta mensagens aninhadas): ao ver "{" que fecha
# esse padrao, empilha o nome; qualquer outro "{" (enum/oneof/service/etc.)
# empilha None (nao redefine o contexto de mensagem); "}" desempilha. O
# campo pertence a mensagem mais interna e NAO-None da pilha no momento em
# que sua linha e processada (antes de aplicar as aberturas/fechamentos da
# propria linha — uma "message X {" nunca compartilha linha com um campo).
MESSAGE_OPEN_PAT = re.compile(r'\bmessage\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{')


def update_message_stack(stack, fragment):
    i = 0
    n = len(fragment)
    while i < n:
        c = fragment[i]
        if c == '{':
            sub = fragment[:i + 1]
            mm = None
            for cand in MESSAGE_OPEN_PAT.finditer(sub):
                if cand.end() == i + 1:
                    mm = cand
            stack.append(mm.group(1) if mm else None)
        elif c == '}':
            if stack:
                stack.pop()
        i += 1


def current_message_name(stack):
    for name in reversed(stack):
        if name is not None:
            return name
    return ""

def strip_proto_non_code(src):
    """Remove comentarios (//, /* */) e string literals ("...", '...') de
    um .proto, preservando a numeracao de linha original para relatorio."""
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
                i += 2
                continue
            if c == '"':
                state = 'normal'
            elif c == '\n':
                lineno += 1
                state = 'normal'  # string nao-terminada (erro de sintaxe)

        elif state == 'sq_string':
            if c == '\\' and i + 1 < n:
                i += 2
                continue
            if c == "'":
                state = 'normal'
            elif c == '\n':
                lineno += 1
                state = 'normal'

        i += 1

    return lines_code

found = []
for path in sys.argv[1:]:
    with open(path, encoding='utf-8', errors='replace') as f:
        src = f.read()
    src_lines = src.splitlines()
    lines_code = strip_proto_non_code(src)
    msg_stack = []
    for lineno in sorted(lines_code):
        fragment = ''.join(lines_code[lineno])
        current_message = current_message_name(msg_stack)

        if FLOAT_PAT.search(fragment):
            orig = src_lines[lineno - 1] if lineno <= len(src_lines) else fragment

            m = FIELD_NAME_NUM_PAT.search(fragment)
            allowed = False
            if m:
                key = (path, current_message, m.group(1), m.group(2))
                allowed = key in ALLOWLIST
            if not allowed:
                found.append(f"{path}:{lineno}:{orig}")

        update_message_stack(msg_stack, fragment)

if found:
    for hit in found:
        print(hit)
    sys.exit(1)
PYEOF

hits=$(cat "$_tmpout")

if [ "$py_status" -ne 0 ] || [ -n "$hits" ]; then
    echo "float/double proibido em campo .proto monetario (TX-2): use adserver.money.v1.Money (int64 amount + uint32 scale)"
    echo "Se o campo NAO for monetario (sinal de ML/ranking), adicione (arquivo, mensagem, nome, tag) a ALLOWLIST em scripts/ci/no-float-proto.sh, com justificativa."
    [ -n "$hits" ] && echo "$hits"
    exit 1
fi
echo "no-float-proto: ok"
