#!/usr/bin/env python3
"""Gate: variavel de path NUNCA aparece nua em recipe de make.

POR QUE ISTO EXISTE
-------------------
O repositorio vive em um caminho COM ESPACO (`/home/agencia/Hojex News/AdServer`).
Em recipe de make, `$(BIN)` nu vira duas palavras para o shell:

    BIN := $(CURDIR)/.bin
    tools:
        @mkdir -p $(BIN)      # -> mkdir -p /home/agencia/Hojex News/AdServer/.bin
                              #    cria /home/agencia/Hojex E News/AdServer/.bin

A 24a onda gastou uma onda inteira consertando exatamente esta classe em
`make/platform.mk` — mas corrigiu a INSTANCIA, nao a FORMA. O alvo `tools` do
root Makefile permaneceu nu por mais 7 ondas, com consequencia medida:
`make tools` criava `News/AdServer/.bin` dentro do repo e havia 181 MB de
binarios orfaos em `/home/agencia/Hojex/` — incluindo um `kyverno 1.13.4`, a
versao que a 27a onda substituiu por mascarar "declared fail, actual pass"
(kyverno/kyverno#15361). Em um clone novo sob path com espaco, `make tools`
nao consegue nem instalar o `buf`, logo `make verify` fica inbootavel.

Corrigir a instancia garante a reincidencia. Este gate corrige a forma.

COMO FUNCIONA (sem lista hardcoded)
-----------------------------------
1. Deriva quais variaveis carregam path lendo as definicoes: RHS que contenha
   `$(CURDIR)`, `$(HOME)`, `command -v`, `shell pwd` ou `~/` — mais o fecho
   transitivo (variavel definida a partir de outra variavel-de-path).
   Nao ha lista mantida a mao: variavel nova e coberta por construcao.
2. Varre as linhas de recipe (iniciadas por TAB) rastreando estado de aspas.
   Ocorrencia de `$(VAR)` FORA de aspas (simples ou duplas) e violacao.

Uso: python3 scripts/ci/make-quoting-check.py [arquivos...]
     (sem argumentos: Makefile + make/*.mk rastreados pelo git)
"""

from __future__ import annotations

import glob
import re
import subprocess
import sys

VAR_DEF = re.compile(r"^\s*([A-Za-z_][A-Za-z0-9_]*)\s*[:?+]?=\s*(.*)$")
VAR_USE = re.compile(r"\$\(([A-Za-z_][A-Za-z0-9_]*)\)")

# Sinais de que o valor da variavel e (ou contem) um caminho de sistema de
# arquivos, que portanto pode conter espaco.
PATH_SEEDS = ("$(CURDIR)", "$(HOME)", "command -v", "shell pwd", "~/", "$(PWD)")


def tracked_make_files() -> list[str]:
    """Makefile + make/*.mk rastreados pelo git (arquivo untracked e invisivel
    a todo gate por `git ls-files` — licao da 30a/31a onda)."""
    try:
        out = subprocess.run(
            ["git", "ls-files", "Makefile", "make/*.mk"],
            capture_output=True, text=True, check=True,
        ).stdout.split()
    except (subprocess.CalledProcessError, FileNotFoundError):
        out = []
    if not out:  # fallback fora de um checkout git
        out = ["Makefile", *sorted(glob.glob("make/*.mk"))]
    return [f for f in out if f]


def derive_path_vars(sources: dict[str, list[str]]) -> set[str]:
    """Variaveis que carregam path, por fecho transitivo sobre as definicoes."""
    definitions: dict[str, str] = {}
    for lines in sources.values():
        for line in lines:
            if line.startswith("\t") or line.lstrip().startswith("#"):
                continue
            m = VAR_DEF.match(line)
            if m:
                definitions.setdefault(m.group(1), m.group(2))

    path_vars = {
        name for name, rhs in definitions.items()
        if any(seed in rhs for seed in PATH_SEEDS)
    }
    # Fecho transitivo: VAR := $(OUTRA_VAR)/sub tambem carrega path.
    changed = True
    while changed:
        changed = False
        for name, rhs in definitions.items():
            if name in path_vars:
                continue
            if any(ref in path_vars for ref in VAR_USE.findall(rhs)):
                path_vars.add(name)
                changed = True
    return path_vars


def unquoted_uses(line: str, path_vars: set[str]) -> list[str]:
    """Usos de $(VAR) fora de aspas, rastreando o estado de aspas do shell."""
    found: list[str] = []
    in_single = in_double = False
    i = 0
    while i < len(line):
        ch = line[i]
        if ch == "\\" and i + 1 < len(line):
            i += 2
            continue
        if ch == "'" and not in_double:
            in_single = not in_single
        elif ch == '"' and not in_single:
            in_double = not in_double
        elif ch == "$" and not in_single and not in_double:
            m = VAR_USE.match(line, i)
            if m and m.group(1) in path_vars:
                found.append(m.group(1))
                i = m.end()
                continue
        i += 1
    return found


def main() -> int:
    files = sys.argv[1:] or tracked_make_files()
    sources: dict[str, list[str]] = {}
    for path in files:
        try:
            with open(path, encoding="utf-8") as fh:
                sources[path] = fh.read().split("\n")
        except OSError as exc:
            print(f"make-quoting-check: FAIL — nao consegui ler {path}: {exc}", file=sys.stderr)
            return 1

    if not sources:
        # Sentinela anti-skip: glob vazio nao pode sair verde (licao da 30a onda,
        # NO_FLOAT_SCRIPTS_EXPECTED).
        print("make-quoting-check: FAIL — nenhum arquivo de make encontrado. "
              "O glob nao casou nada (renomeado, movido, sparse-checkout?).", file=sys.stderr)
        return 1

    path_vars = derive_path_vars(sources)
    if not path_vars:
        print("make-quoting-check: FAIL — nenhuma variavel de path derivada; "
              "a heuristica de deteccao quebrou (checar PATH_SEEDS).", file=sys.stderr)
        return 1

    violations: list[str] = []
    for path, lines in sources.items():
        for lineno, line in enumerate(lines, start=1):
            if not line.startswith("\t"):
                continue
            stripped = line.lstrip("\t")
            if stripped.startswith("#"):
                continue
            for var in unquoted_uses(line, path_vars):
                violations.append(
                    f"{path}:{lineno}: $({var}) sem aspas em recipe — "
                    f"o path do repo contem espaco e o shell vai quebrar em duas palavras\n"
                    f"    {line.strip()}"
                )

    if violations:
        print("make-quoting-check: FAIL — variavel de path nua em recipe de make:\n", file=sys.stderr)
        for v in violations:
            print(f"  {v}", file=sys.stderr)
        print(
            "\nCorrija envolvendo em aspas duplas: \"$(VAR)\" ou \"$(VAR)/sub\".\n"
            "Contexto: 24a onda (falso-RED do platform-validate) e a reincidencia "
            "no alvo `tools` do root Makefile.",
            file=sys.stderr,
        )
        return 1

    print(f"make-quoting-check: ok — {len(sources)} arquivo(s) de make, "
          f"{len(path_vars)} variavel(is) de path derivada(s), 0 uso nu em recipe.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
