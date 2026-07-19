#!/usr/bin/env python3
"""
platform/secrets/openbao/policy-check.py

Validador estatico (sem depender de "bao"/"vault" CLI, indisponiveis no ambiente
hermetico) das policies HCL de menor privilegio por celula (mandato item 5 do
platform-infra-engineer, este diretorio).

Antes desta checagem, platform/secrets/openbao/*.hcl nao era lido por nenhum
make target nem workflow de CI (achado #8, auditoria 27a onda) -- uma policy
podia ganhar acesso cross-cell (ex.: policy-pci.hcl passando a enxergar
aml-kyc/*) ou uma capability excessiva (ex.: "sudo") sem que nenhum gate
reprovasse. Chamado por `make platform-openbao-policy-check`
(ver make/platform.mk), que roda como parte de `make platform-validate`.

Regras impostas (minimas e cirurgicas -- nao reimplementa o parser HCL da
HashiCorp, so o suficiente para os blocos "path ... { capabilities = [...] }"
usados por estas policies):

  1. path "*" (wildcard nu, cobre TODOS os mounts) -- proibido.
  2. capability "sudo" -- proibido (nenhuma policy de celula/workload precisa
     de root token capability).
  3. capability "*" (wildcard de capacidade -- nao e uma capability valida do
     Vault/OpenBao, mas serve de guarda contra typo/edicao perigosa).
  4. Isolamento de celula: para cada arquivo "policy-<celula>.hcl" (celula
     derivada do proprio nome do arquivo), nenhum "path" pode conter, como
     substring de algum segmento (separado por "/"), o nome de OUTRA celula
     descoberta entre os arquivos irmaos (ex.: policy-pci.hcl nao pode conter
     um path com segmento "aml-kyc"; policy-aml-kyc.hcl nao pode conter
     segmento "pci" nem "platform"; etc). Reproduz exatamente o cenario do
     achado: "PCI le secret/aml-kyc" vira ERRO aqui.

Uso: python3 platform/secrets/openbao/policy-check.py [dir]
     (default dir: platform/secrets/openbao)
"""
import glob
import os
import re
import sys

DEFAULT_DIR = os.path.dirname(os.path.abspath(__file__))

PATH_BLOCK_RE = re.compile(r'path\s+"([^"]+)"\s*\{(.*?)\}', re.DOTALL)
CAPS_RE = re.compile(r'capabilities\s*=\s*\[([^\]]*)\]', re.DOTALL)

FORBIDDEN_CAPABILITIES = {"sudo", "*"}


def cell_name_from_filename(path):
    """policy-pci.hcl -> pci; policy-aml-kyc.hcl -> aml-kyc; policy-platform.hcl -> platform."""
    base = os.path.basename(path)
    m = re.match(r"^policy-(.+)\.hcl$", base)
    if not m:
        return None
    return m.group(1)


def parse_capabilities(raw):
    caps = []
    for item in raw.split(","):
        item = item.strip().strip('"').strip("'")
        if item:
            caps.append(item)
    return caps


def check_file(path, own_cell, other_cells, errors):
    with open(path, encoding="utf-8") as fh:
        content = fh.read()

    blocks = PATH_BLOCK_RE.findall(content)
    if not blocks:
        errors.append(f"{path}: nenhum bloco 'path \"...\" {{ ... }}' encontrado "
                       f"(arquivo vazio/malformado?)")
        return

    for path_value, body in blocks:
        # Regra 1: wildcard nu cobrindo todos os mounts.
        if path_value.strip() == "*":
            errors.append(f'{path}: path "*" (wildcard nu, cobre todos os mounts) '
                           f"e proibido -- viola menor privilegio")

        # Regra 4: isolamento de celula -- nenhum segmento do path pode citar
        # outra celula.
        if own_cell is not None:
            segments = [s for s in path_value.split("/") if s]
            for seg in segments:
                seg_l = seg.lower()
                for other in other_cells:
                    if other == own_cell:
                        continue
                    if other.lower() in seg_l:
                        errors.append(
                            f'{path}: path "{path_value}" referencia a celula '
                            f'"{other}" (segmento "{seg}") -- vazamento cross-cell, '
                            f'proibido para a policy da celula "{own_cell}"'
                        )

        # Regras 2/3: capabilities excessivas.
        caps_match = CAPS_RE.search(body)
        if caps_match:
            caps = parse_capabilities(caps_match.group(1))
            for cap in caps:
                if cap in FORBIDDEN_CAPABILITIES:
                    errors.append(
                        f'{path}: path "{path_value}" declara capability '
                        f'"{cap}" -- excessiva/proibida (menor privilegio)'
                    )


def main():
    target_dir = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_DIR
    files = sorted(glob.glob(os.path.join(target_dir, "*.hcl")))

    if not files:
        print(f"openbao-policy-check: nenhum .hcl encontrado em {target_dir} (ok)")
        sys.exit(0)

    # Descobre todas as celulas a partir dos nomes de arquivo "policy-<celula>.hcl"
    # presentes no diretorio -- generico, nao hardcoded, para escalar a novas celulas.
    all_cells = set()
    for f in files:
        c = cell_name_from_filename(f)
        if c is not None:
            all_cells.add(c)

    errors = []
    for f in files:
        own_cell = cell_name_from_filename(f)
        other_cells = sorted(c for c in all_cells if c != own_cell)
        check_file(f, own_cell, other_cells, errors)

    if errors:
        print("openbao-policy-check: FALHOU")
        for e in errors:
            print(f"  ERRO: {e}", file=sys.stderr)
        sys.exit(1)

    for f in files:
        print(f"  ok: {f}")
    print("openbao-policy-check: ok")
    sys.exit(0)


if __name__ == "__main__":
    main()
