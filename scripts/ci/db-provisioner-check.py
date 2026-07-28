#!/usr/bin/env python3
"""db-provisioner-check.py — gate default-deny contra enumeração de migration
mantida à mão (32ª onda).

POR QUE ESTE GATE EXISTE
    Quatro ondas seguidas acharam o mesmo defeito com formas diferentes: uma
    lista escrita à mão que divergiu do disco.
      28ª      — ledger.postings sem imutabilidade (migration 0004 criada);
      29ª #10  — .github/workflows/db.yml nunca aplicava a 0004 do ledger;
      30ª      — a migration config/0004 nasceu órfã dos 4 runners, que
                 enumeravam migrations por lista hardcoded parando em 0003;
      32ª      — make/dev.mk::dev-db-setup e deploy/local/postgres/10-init.sh
                 AINDA enumeravam à mão. Medido de 1ª mão: no banco que
                 `make dev-db-setup` monta, config.campaign_zones_tenant_isolation
                 era a ÚNICA policy do schema com pg_policy.polwithcheck IS NULL
                 e `make db-test` reprovava; no initdb do Docker o ledger ficava
                 sem RLS (0003) e sem append-only (0004).
    A 30ª corrigiu a instância nos runners de teste. Este gate corrige a FORMA.

AS DUAS FORMAS QUE O DEFEITO JÁ TEVE — e as duas checagens que as pegam.
Nenhuma lista de migrations existe neste arquivo: tudo é derivado de
db/*/migrations/*_up.sql.

    A) MIGRATION NOMEADA OU MONTADA EM LINHA EXECUTÁVEL. As formas conhecidas:
           for f in 0001_config_schema 0002_config_rls 0003_campaign_zones_rls
           run /repo/db/ledger/migrations/0001_ledger_schema_up.sql
           psql -f "db/config/migrations/${f}_up.sql"      # id montado por concat
       Dois predicados, ambos sobre a LINHA LÓGICA (não física):
         A1 — nomear QUALQUER migration numa linha executável não-excluída.
              Medido em 2026-07-28: zero linhas assim no repo, então o predicado
              é default-deny a custo zero de falso-positivo. A versão anterior
              exigia 2+ nomes na linha, e o tech-lead demonstrou o furo — uma
              migration por linha, com o diretório escondido numa variável de
              make e mais de B_WINDOW linhas de separação, passava verde.
         A2 — referenciar `migrations/<algo>_up.sql` (ou `_down.sql`) onde
              `<algo>` NÃO é o glob `*`. Aplicação legítima é sempre
              `ls "$dir"/*_up.sql`; qualquer nome montado (`${f}_up.sql`,
              `${num}_${name}_up.sql`) reprova por construção — foi o bypass
              por concatenação que o parity-golden-test-guardian executou.

    B) ENUMERAÇÃO PARCIAL EM PROSA. Um `echo`/`RAISE NOTICE`/runbook que manda o
       operador aplicar "0001, 0002, 0003" quando o disco tem quatro.
       O discriminador é PROXIMIDADE: citar uma migration no ponto de uso é uma
       ÂNCORA (legítima e exigida); o que caracteriza uma LISTA é 2+ migrations
       do mesmo schema dentro de uma janela de B_WINDOW linhas.

    Toda busca roda sobre texto NORMALIZADO (sem aspas — ver o comentário de
    QUOTES abaixo), mas a JANELA difere por checagem: **A** usa linha LÓGICA
    (continuações juntadas), **B** usa linha FÍSICA. É deliberado: B mede prosa,
    que não se beneficia de juntar continuação de shell, e `B_WINDOW`/`B_LOCAL`
    já toleram lista partida em blocos vizinhos. A primeira versão desta frase
    dizia "todas as buscas rodam sobre a linha lógica" — generalização que
    incluía B indevidamente, pega pelo parity-golden-test-guardian na 3ª
    re-guarda.

TRÊS BLOQUEIOS DA BARREIRA, NA MESMA ONDA QUE CRIOU O GATE
    O `parity-golden-test-guardian` bloqueou esta onda três vezes, cada uma com
    um bypass que ele mesmo executou num clone descartável. Vale ler a sequência
    inteira: ela é o melhor argumento existente contra confiar num gate recém-
    escrito só porque ele está verde.

    1. LINHA FÍSICA vs LINHA LÓGICA. A checagem A varria linha física, então um
       `for f in \\` quebrado em continuações — um nome por linha, com o caminho
       montado por `${f}_up.sql` em vez de literal — não tinha nunca 2 nomes na
       mesma linha nem um nome ao lado de `migrations/`. Passava verde aplicando
       3 das 4 migrations de `config`. É exatamente o `no-float-multiline-split`
       (CRITICAL da 30ª), onde a conjunção era avaliada por linha física e
       qualquer quebra do Prettier sumia com a violação; a correção lá foi
       "janela trocada para linha lógica", e é a mesma aqui.

    2. COMPLETUDE DE ARQUIVO vs COMPLETUDE LOCAL. A checagem B media completude
       no ARQUIVO INTEIRO, então uma citação isolada e distante — um comentário
       histórico a 100 linhas — "completava" uma lista executável incompleta em
       outro ponto. No bypass demonstrado, o comentário desta docstring citando
       `0004_campaign_zones_with_check_up.sql` era o que absolvia a lista
       injetada. Completude passa a ser medida num RAIO LOCAL (B_LOCAL).

    3. ID MONTADO EM TEMPO DE EXECUÇÃO. Duas formas, fechadas por A2 e pela
       normalização de aspas: concatenação por variável
       (`f="${num}_${name}"` → o id nunca aparece literal em lugar nenhum) e
       **strings adjacentes** (`"...0001_config""_schema_up.sql"`, que o shell
       colapsa numa palavra só, sem variável alguma). A segunda é a mais
       perigosa das duas por ser a mais inocente: aparece sozinha num refatoro
       de quebra de linha longa, sem nenhuma intenção de burlar nada.

    Corolário registrado no fecho da onda: quando um gate novo pertence à mesma
    família de um gate antigo, releia as lições daquele antes de escrever o novo
    — a família repete o modo de falha. E o gate só é tão bom quanto o
    adversário que tentou quebrá-lo: as três formas acima passariam verde se
    ninguém tivesse sido pago para atacá-las.

LIMITE DECLARADO — leia antes de confiar
    Este é um gate TEXTUAL, e nenhum gate textual vence ofuscação total: quem
    esconder ao mesmo tempo o diretório numa variável E montar o id por
    concatenação não deixa nenhuma assinatura na linha. O que fecha a classe de
    vez não é texto, é COMPORTAMENTO — e ele existe: os seis `make db-test-*` e
    o `go-test-integration` rodam contra um banco montado PELO provisionador, e
    foi exatamente assim que o defeito original apareceu (`make db-test`
    reprovando contra o banco que `make dev-db-setup` acabara de montar). Este
    gate é a primeira linha, barata e imediata; a garantia é a segunda. Não
    descreva este arquivo como "a" prova — ele é o alarme.

PROVA DE NÃO-TAUTOLOGIA (protocolo de mutação: cp backup -> mutar -> rodar -> mv restaurar;
NUNCA `git add -N` + `git checkout`, que grava blob de zero bytes e apaga o arquivo — lição da 30ª):
    cp make/dev.mk /tmp/bk.mk
    # 1) forma clássica, uma linha:
    #    @for f in 0001_config_schema 0002_config_rls 0003_campaign_zones_rls; do ...
    # 2) forma multi-linha (o bypass que a barreira demonstrou):
    #    @for f in \\
    #        0001_config_schema \\
    #        0002_config_rls \\
    #        0003_campaign_zones_rls; do psql -f "db/config/migrations/${f}_up.sql"; done
    # 3) migration nova no disco: cria db/ledger/migrations/0005_probe_{up,down}.sql
    python3 scripts/ci/db-provisioner-check.py   # -> VERMELHO nos três casos
    mv /tmp/bk.mk make/dev.mk
"""

from __future__ import annotations

import re
import subprocess
import sys
from collections import defaultdict
from pathlib import Path

# Janela que caracteriza uma LISTA (e não âncoras dispersas). Medido no repo em
# 2026-07-28: na superfície EXCLUÍDA — a que abriga âncoras no ponto de uso — as
# distâncias mínimas entre citações do mesmo schema são de 1 a 26 linhas
# (`internal/ledger/doc.go` cita ledger/0001 e 0002 em linhas consecutivas). Por
# isso a proteção dessas âncoras é a EXCLUSÃO por tipo de arquivo, não a folga
# da janela; a janela protege a superfície NÃO excluída, onde hoje todo par
# próximo é lista completa. Não confunda os dois mecanismos: a primeira versão
# desta docstring alegava que as âncoras estavam a "100+ linhas" e a alegação
# não sobreviveu à medição (achado do tech-lead na barreira).
B_WINDOW = 12

# Raio local em que a completude de uma lista é medida. Maior que B_WINDOW de
# propósito: uma lista pode ser escrita em dois blocos vizinhos (o cabeçalho
# "aplique 0001/0002" e o "e depois 0003/0004" logo abaixo) e continuar sendo
# uma lista completa. O que ele NÃO deixa passar é a citação a 100 linhas de
# distância absolvendo a lista — o bypass que a barreira demonstrou.
B_LOCAL = 40

# Exclusões (default-deny + allowlist explícita e justificada):
#   db/*/migrations/**  — o cabeçalho de uma migration explica sua relação com as
#                         anteriores; é o registro da decisão, não aplicação.
#   db/*/tests/**       — o teste ancora no invariante da migration que prova;
#                         citar o número é a âncora exigida, não uma lista.
#   docs/adr/**, README.md, docs/plano-desenvolvimento-por-addon.md
#                       — histórico arquivado (ADR + log de ondas). Reescrever
#                         registro histórico para agradar um gate é o antipadrão
#                         que o formato ADR existe para evitar.
#   *.go, *.ts, *.tsx   — código de aplicação não provisiona banco; quando cita
#                         uma migration é em mensagem de erro/skip (prosa). O
#                         provisionamento vive em make/, deploy/, scripts/, .github/.
#   este arquivo        — os exemplos do próprio cabeçalho.
EXCLUDE = [
    re.compile(r"^db/[^/]+/migrations/"),
    re.compile(r"^db/[^/]+/tests/"),
    re.compile(r"^docs/adr/"),
    re.compile(r"^README\.md$"),
    re.compile(r"^docs/plano-desenvolvimento-por-addon\.md$"),
    re.compile(r"\.go$"),
    re.compile(r"\.tsx?$"),
    re.compile(r"^scripts/ci/db-provisioner-check\.py$"),
]

# Provisionadores conhecidos. NÃO é a lista de quem é varrido (a varredura é o
# repo inteiro) — é a prova de que ela não ficou cega justamente para eles. A
# 29ª #10 e a 30ª nasceram de runner invisível ao gate; a 31ª aprendeu que
# `git ls-files` é cego a arquivo untracked.
MUST_BE_TRACKED = [
    "make/db.mk",
    "make/dev.mk",
    "deploy/local/postgres/10-init.sh",
    ".github/workflows/db.yml",
    "db/schema-order.txt",
]

PROSE_PREFIXES = ("#", "@#", "--", "//")

# Aspas nunca fazem parte do nome de uma migration nem do glob `*`, mas o shell
# concatena strings adjacentes: `"db/config/migrations/0001_config""_schema_up.sql"`
# é UMA palavra só, igual ao caminho literal — e sem nenhuma variável envolvida.
# O parity-golden-test-guardian bloqueou a onda uma terceira vez com exatamente
# isso: o nome sumia da busca por substring de A1 e a aspa quebrava o `migrations/`
# de A2, tudo sem cair no resíduo declarado (que exige variável). Normalizar
# removendo aspas ANTES de qualquer busca colapsa a forma de volta ao literal.
# A detecção de prosa não é afetada: ela roda sobre a primeira linha FÍSICA crua.
QUOTES = str.maketrans("", "", "\"'")
PROSE_EMITTERS = re.compile(
    r"^@?\s*(echo|printf|RAISE|>&2)\b", re.IGNORECASE
)


def sh(*args: str) -> str:
    return subprocess.run(args, capture_output=True, text=True).stdout


def is_excluded(path: str) -> bool:
    return any(p.search(path) for p in EXCLUDE)


def is_prose(path: str, first_physical: str) -> bool:
    """Conservador: na dúvida, a linha é EXECUTÁVEL (default-deny)."""
    if path.endswith(".md"):
        return True  # markdown inteiro é prosa; a checagem B cobre
    s = first_physical.lstrip()
    if s.startswith(PROSE_PREFIXES):
        return True
    if s.startswith(("'", '"')):  # continuação de string literal
        return True
    return bool(PROSE_EMITTERS.match(s))


def logical_lines(text: str):
    """Junta continuações de shell/make ('\\' no fim) numa única linha lógica.

    Devolve (numero_da_primeira_linha_fisica, texto_juntado, primeira_linha_fisica).
    Sem isto, um `for f in \\` quebrado em continuações escapa da checagem A —
    o bypass que a barreira de guardiões demonstrou nesta mesma onda.
    """
    physical = text.split("\n")
    i = 0
    while i < len(physical):
        start = i
        parts = [physical[i]]
        while parts[-1].rstrip().endswith("\\") and i + 1 < len(physical):
            i += 1
            parts.append(physical[i])
        yield start + 1, " ".join(p.rstrip().rstrip("\\") for p in parts), physical[start]
        i += 1


def main() -> int:
    root = sh("git", "rev-parse", "--show-toplevel").strip()
    if not root:
        print("ERRO: não estou dentro de um repositório git.")
        return 2
    import os

    os.chdir(root)

    # --- lista de migrations: DERIVADA, nunca escrita ------------------------
    migrations: dict[str, list[str]] = {}
    for up in sorted(Path("db").glob("*/migrations/*_up.sql")):
        schema = up.parts[1]
        migrations.setdefault(schema, []).append(up.name[: -len("_up.sql")])
    for schema in migrations:
        migrations[schema].sort()

    all_migrations = sorted({m for v in migrations.values() for m in v})
    if not all_migrations:
        print("ERRO: nenhuma migration encontrada em db/*/migrations/*_up.sql — sentinela anti-vazio.")
        print("      Sem ela este gate passaria verde sem verificar coisa alguma.")
        return 1

    tracked = set(sh("git", "ls-files").splitlines())
    for p in MUST_BE_TRACKED:
        if p not in tracked:
            print(f"ERRO: '{p}' não está rastreado pelo git — este gate seleciona por 'git ls-files'")
            print("      e seria cego a ele (lição da 31ª onda).")
            return 1

    candidates = [f for f in sorted(tracked) if not is_excluded(f)]
    failures = 0

    # --- A) lista de migrations em linha lógica executável -------------------
    print("== db-provisioner-check A: migration aplicada por enumeração à mão (default-deny) ==")
    a_fail = 0
    for path in candidates:
        try:
            text = Path(path).read_text(encoding="utf-8", errors="replace")
        except (OSError, IsADirectoryError):
            continue
        norm_text = text.translate(QUOTES)
        if not any(m in norm_text for m in all_migrations):
            continue
        for lineno, joined, first_physical in logical_lines(text):
            norm = joined.translate(QUOTES)
            named = [m for m in all_migrations if m in norm]
            if not named:
                continue
            if is_prose(path, first_physical):
                continue
            # A1: nomear qualquer migration numa linha executável.
            print(f"ERRO [A1]: {path}:{lineno} nomeia migration em linha executável ({len(named)}: {' '.join(named)}).")
            print(f"           {joined.strip()[:150]}")
            print("           Aplicação é por glob + sort sobre db/<schema>/migrations/*_up.sql, com a")
            print("           ordem entre schemas vinda de db/schema-order.txt. Modelo: make/db.mk::db-test-all.")
            a_fail = 1
            failures += 1

    # A2: caminho de migration com nome MONTADO (não-glob) — pega o id que
    # nunca aparece literal porque foi concatenado em tempo de execução.
    built_path = re.compile(r"migrations/([^\s\"';|)]*)_(?:up|down)\.sql")
    for path in candidates:
        try:
            text = Path(path).read_text(encoding="utf-8", errors="replace")
        except (OSError, IsADirectoryError):
            continue
        if "migrations/" not in text.translate(QUOTES):
            continue
        for lineno, joined, first_physical in logical_lines(text):
            if is_prose(path, first_physical):
                continue
            for m in built_path.finditer(joined.translate(QUOTES)):
                name = m.group(1)
                if name == "*":  # glob literal: a forma legítima
                    continue
                print(f"ERRO [A2]: {path}:{lineno} referencia migration por nome MONTADO, não por glob.")
                print(f"           trecho: migrations/{name}_up.sql")
                print(f"           {joined.strip()[:150]}")
                print("           Só `ls \"$dir\"/*_up.sql` é aplicação legítima. Um nome montado em tempo de")
                print("           execução (`${f}_up.sql`) esconde do gate QUAIS migrations são aplicadas.")
                a_fail = 1
                failures += 1
    if not a_fail:
        print("  ok — nenhuma aplicação de migration enumerada à mão.")

    # --- B) enumeração em prosa tem de estar completa, LOCALMENTE ------------
    print("== db-provisioner-check B: enumeração em prosa tem de estar completa ==")
    b_fail = 0
    for path in candidates:
        try:
            text = Path(path).read_text(encoding="utf-8", errors="replace")
        except (OSError, IsADirectoryError):
            continue
        lines = text.split("\n")
        for schema, names in migrations.items():
            if len(names) < 2:
                continue
            hits: list[tuple[int, str]] = []
            for idx, line in enumerate(lines, start=1):
                norm_line = line.translate(QUOTES)
                for m in names:
                    if m in norm_line:
                        hits.append((idx, m))
            if len(hits) < 2:
                continue

            # clusters: 2+ migrations DISTINTAS dentro de B_WINDOW linhas
            reported: set[int] = set()
            for i, (ln_i, _) in enumerate(hits):
                window = [(ln, m) for ln, m in hits[i:] if ln - ln_i <= B_WINDOW]
                distinct = {m for _, m in window}
                if len(distinct) < 2:
                    continue
                span_start, span_end = ln_i, window[-1][0]
                # completude medida num RAIO LOCAL, não no arquivo inteiro:
                # uma citação distante não pode absolver uma lista incompleta.
                local = {
                    m for ln, m in hits
                    if span_start - B_LOCAL <= ln <= span_end + B_LOCAL
                }
                if len(local) == len(names):
                    continue
                if span_start in reported:
                    continue
                reported.add(span_start)
                missing = sorted(set(names) - local)
                print(f"ERRO [B]: {path}:{span_start} enumera {len(local)} das {len(names)} migrations do schema '{schema}' — lista incompleta.")
                print(f"          faltam: {' '.join(missing)}")
                print(f"          (2+ migrations em {B_WINDOW} linhas = lista; completude medida num raio de {B_LOCAL} linhas.)")
                print("          Enumeração parcial é instrução operacional já apodrecida: quem a seguir")
                print("          monta um banco sem o que as ausentes garantem.")
                b_fail = 1
                failures += 1
    if not b_fail:
        print("  ok — nenhuma enumeração parcial em prosa.")

    if failures:
        print()
        print("== db-provisioner-check: REPROVADO ==")
        return 1
    print("== db-provisioner-check: ok ==")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
