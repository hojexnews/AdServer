#!/usr/bin/env python3
"""workflow-paths-mirror-check.py — push.paths TEM de espelhar pull_request.paths.

POR QUE ESTE GATE EXISTE
    O merge canônico deste repo para a `main` é **push FF direto**, sem PR
    intermediário. Então, para uma mudança que chega na `main`, o gatilho
    `push.paths` é a ÚNICA reavaliação que acontece: um path que exista só no
    bloco `pull_request` cria uma classe inteira de mudança que entra na `main`
    sem nunca ser revalidada.

    `.github/workflows/go.yml` escreveu essa regra por extenso na 32ª onda —
    "push.paths espelha pull_request.paths na íntegra, VERBATIM. Ao editar um
    dos dois blocos, edite o outro no mesmo commit" — e o workflow irmão
    `db.yml` já a violava no mesmo diff: `make/db.mk` e
    `.github/workflows/db.yml` estavam só no lado do PR. Ou seja, alguém podia
    neutralizar as sentinelas de schema (`db-check-schema-list`,
    `db-check-migration-pairing`) editando `make/db.mk` e empurrar direto para
    a `main` sem que o job `db` rodasse uma única vez.

    Uma regra escrita em comentário é uma regra que depende de alguém lembrar.
    Esta é a mesma classe que o repo já pagou quatro vezes (enumeração mantida
    à mão); a correção é a mesma: transformar a regra em gate.

    GitHub Actions **não** suporta âncoras/aliases YAML em workflow files, então
    a alternativa "declarar uma vez e referenciar" não existe — daí o gate.

O QUE É VERIFICADO (default-deny)
    Para todo `.github/workflows/*.yml` que tenha `on.pull_request` E um
    `on.push` **de branch**: se qualquer um dos dois declarar `paths`, os dois
    têm de declarar, e os conjuntos têm de ser IGUAIS.

    Um `on.push` que dispara por **tag** (`push.tags`, sem `push.branches`) NÃO
    entra na regra: é o gatilho de release, não a reavaliação do merge, e ele
    deve rodar para toda tag independentemente de path. Foi o caso de
    `supply-chain.yml` — a primeira versão desta checagem, nesta mesma onda, o
    reprovou por engano. Falso-positivo do próprio sweep (protocolo #5), pego
    antes do fecho: a regra é sobre o caminho de MERGE, e só sobre ele.

    - Nenhum dos dois com `paths`  -> ok (dispara sempre, default-deny de
      acionamento; é o caso de no-float.yml e repo-gates.yml).
    - Só um com `paths`            -> ERRO. O lado sem filtro dispara sempre e
      o outro não; a assimetria é sempre um acidente, nas duas direções.
    - Ambos com `paths` diferentes -> ERRO, listando a diferença nos dois sentidos.

    `paths-ignore` recebe o mesmo tratamento, pela mesma razão.

    Não há allowlist. Se um workflow precisar mesmo de gatilhos assimétricos, a
    forma correta é separar em dois workflows com nomes que digam isso — não
    abrir exceção aqui.

PONTO CEGO CONHECIDO (residual declarado na 32ª onda)
    A checagem só olha `on.pull_request`. Um workflow que usasse
    `on.pull_request_target` teria push.paths sem espelho e passaria invisível.
    Verificado em 2026-07-28: `grep -l pull_request_target .github/workflows/*.yml`
    não retorna nada — nenhum workflow do repo usa essa chave hoje, então não é
    achado ativo, é lacuna de FORMA. Se algum workflow futuro migrar para
    `pull_request_target`, trate a chave como sinônimo de `pull_request` aqui,
    do mesmo jeito que já tratamos `on:` → `True` do YAML 1.1.

PROVA DE NÃO-TAUTOLOGIA (protocolo de mutação: cp backup -> mutar -> rodar -> mv restaurar)
    cp .github/workflows/go.yml /tmp/bk.yml
    # remove um path só do bloco push:
    python3 - <<'EOF'
    p='.github/workflows/go.yml'; s=open(p).read()
    i=s.index('  push:'); j=s.index('- "make/parity.mk"', i)
    s=s[:j]+s[j+len('- "make/parity.mk"'):]; open(p,'w').write(s)
    EOF
    python3 scripts/ci/workflow-paths-mirror-check.py   # -> ERRO nomeando go.yml e o path
    mv /tmp/bk.yml .github/workflows/go.yml
"""

from __future__ import annotations

import sys
from pathlib import Path

try:
    import yaml
except ImportError:  # pragma: no cover - ambiente sem PyYAML
    print(
        "ERRO: PyYAML ausente (python3 -c 'import yaml' falhou). "
        "Instale com: pip install pyyaml",
        file=sys.stderr,
    )
    sys.exit(2)

WORKFLOW_DIR = Path(".github/workflows")
KEYS = ("paths", "paths-ignore")


def load_on_block(doc: dict) -> dict | None:
    """Devolve o bloco `on:` do workflow.

    Cuidado com a peculiaridade do YAML 1.1: `on:` sem aspas é lido como o
    booleano True, não como a string "on". Um gate que procurasse só a chave
    "on" ficaria silenciosamente vazio — falso-verde — em TODOS os workflows
    do repo, que é exatamente a classe de defeito que este arquivo existe para
    combater. Procuramos as duas formas.
    """
    for key in ("on", True):
        if key in doc:
            block = doc[key]
            return block if isinstance(block, dict) else None
    return None


def main() -> int:
    if not WORKFLOW_DIR.is_dir():
        print(f"ERRO: {WORKFLOW_DIR} não existe — nada a verificar (sentinela anti-vazio).")
        return 1

    files = sorted(WORKFLOW_DIR.glob("*.yml")) + sorted(WORKFLOW_DIR.glob("*.yaml"))
    if not files:
        print(f"ERRO: nenhum workflow em {WORKFLOW_DIR} — sentinela anti-vazio.")
        print("      Sem ela este gate passaria verde sem verificar coisa alguma.")
        return 1

    failures = 0
    checked = 0

    for path in files:
        try:
            doc = yaml.safe_load(path.read_text(encoding="utf-8"))
        except yaml.YAMLError as exc:
            print(f"ERRO: {path} não é YAML válido: {exc}")
            failures += 1
            continue

        if not isinstance(doc, dict):
            continue

        on_block = load_on_block(doc)
        if on_block is None:
            continue

        pr = on_block.get("pull_request")
        push = on_block.get("push")
        if not isinstance(pr, dict) or not isinstance(push, dict):
            # Sem os dois gatilhos não há espelho a manter.
            continue

        # Gatilho de release (push por tag, sem branch) não é o caminho de
        # merge — ver docstring. Ele deve rodar para toda tag, sem filtro.
        if "tags" in push and "branches" not in push:
            continue

        checked += 1

        for key in KEYS:
            pr_v = pr.get(key)
            push_v = push.get(key)

            if pr_v is None and push_v is None:
                continue

            if (pr_v is None) != (push_v is None):
                lado_com = "pull_request" if pr_v is not None else "push"
                lado_sem = "push" if pr_v is not None else "pull_request"
                print(f"ERRO: {path} — `{key}` existe em `{lado_com}` mas não em `{lado_sem}`.")
                print(f"      O lado sem filtro dispara sempre e o outro não; a assimetria")
                print(f"      significa que uma classe de mudança é avaliada num caminho e não no outro.")
                print(f"      Merge para a main aqui é push FF direto: o lado `push` é a última palavra.")
                failures += 1
                continue

            pr_set = set(pr_v or [])
            push_set = set(push_v or [])
            if pr_set == push_set:
                continue

            so_pr = sorted(pr_set - push_set)
            so_push = sorted(push_set - pr_set)
            print(f"ERRO: {path} — `on.pull_request.{key}` e `on.push.{key}` divergem.")
            if so_pr:
                print(f"      só no pull_request: {so_pr}")
                print("      -> mudança nesses paths entra na main por push FF sem o gate rodar.")
            if so_push:
                print(f"      só no push: {so_push}")
                print("      -> mudança nesses paths não é avaliada no PR, só depois de mesclada.")
            failures += 1

    if failures:
        print()
        print(f"== workflow-paths-mirror-check: REPROVADO ({failures} divergência(s)) ==")
        return 1

    print(
        f"workflow-paths-mirror-check: ok — {checked} workflow(s) com pull_request+push, "
        "paths espelhados."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
