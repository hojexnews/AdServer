#!/usr/bin/env python3
"""
platform/tofu/check-cell-consistency.py

Gate barato (offline, sem credenciais) contra DRIFT de nome de célula entre o
root OpenTofu (main.tf/variables.tf) e o layout REAL de platform/ (namespaces
declarados em platform/k8s/namespaces.yaml + diretórios de célula-compliance
em platform/cells/).

Motivação (achado #20, 27ª onda de auditoria adversarial): o root usava "aml"
em local.cell_namespaces / local.compliance_cells / var.cells.default, enquanto
o nome CANÔNICO real em todo o resto de platform/ (namespace, netpol, RBAC,
policy Kyverno, policy OpenBao, app Argo CD) sempre foi "aml-kyc". Sem este
gate, um `tofu validate` isolado passa verde mesmo com o nome errado — ele só
valida sintaxe HCL, não confere consistência cross-arquivo/cross-diretório.

Verificações (todas fail-closed — qualquer divergência é ERRO, exit 1):

  1. var.cells (variables.tf) == chaves de local.cell_namespaces (main.tf).
     (consistência interna dentro do próprio root OpenTofu)
  2. local.compliance_cells (main.tf) é subconjunto de var.cells.
  3. local.compliance_cells (main.tf) == nomes dos subdiretórios REAIS de
     platform/cells/ (ex.: "pci", "aml-kyc"). ESTA é a checagem que teria
     pego o achado #20: compliance_cells=["pci","aml"] != dirs {"pci","aml-kyc"}.
  4. Cada par (célula, namespace) de local.cell_namespaces (main.tf) precisa
     existir em platform/k8s/namespaces.yaml como um Namespace cujo
     metadata.name == namespace e labels["adserver.hojex/cell"] == célula.

Uso:
    python3 platform/tofu/check-cell-consistency.py
Saída: "OK" + exit 0 se consistente; lista de divergências + exit 1 caso
contrário. Não requer tofu/terraform instalado (parsing é regex leve sobre o
HCL, suficiente para os blocos usados neste root enxuto — não reimplementa um
parser HCL completo).
"""
import pathlib
import re
import sys

try:
    import yaml
except ImportError:
    print("ERRO: PyYAML ausente (python3 -c 'import yaml' falhou). "
          "Necessário para ler platform/k8s/namespaces.yaml.", file=sys.stderr)
    sys.exit(1)

HERE = pathlib.Path(__file__).resolve().parent          # platform/tofu
PLATFORM_DIR = HERE.parent                                # platform/
REPO_ROOT = PLATFORM_DIR.parent                           # raiz do repo

MAIN_TF = HERE / "main.tf"
VARIABLES_TF = HERE / "variables.tf"
NAMESPACES_YAML = PLATFORM_DIR / "k8s" / "namespaces.yaml"
CELLS_DIR = PLATFORM_DIR / "cells"


def _extract_quoted_list(text: str, anchor_pattern: str) -> list[str]:
    """Acha o primeiro `default = [...]` (ou `= [...]`) após o anchor e
    retorna as strings entre aspas, na ordem em que aparecem."""
    m = re.search(anchor_pattern, text, re.DOTALL)
    if not m:
        raise ValueError(f"padrão não encontrado: {anchor_pattern!r}")
    bracket_start = text.index("[", m.end() - 1)
    bracket_end = text.index("]", bracket_start)
    inner = text[bracket_start + 1 : bracket_end]
    return re.findall(r'"([^"]+)"', inner)


def parse_var_cells(variables_tf_text: str) -> list[str]:
    # variable "cells" { ... default = [...] }
    m = re.search(r'variable\s+"cells"\s*\{(.*?)\n\}', variables_tf_text, re.DOTALL)
    if not m:
        raise ValueError('bloco variable "cells" não encontrado em variables.tf')
    block = m.group(1)
    return _extract_quoted_list(block, r"default\s*=\s*\[")


def parse_cell_namespaces(main_tf_text: str) -> dict[str, str]:
    m = re.search(r"cell_namespaces\s*=\s*\{(.*?)\n\s*\}", main_tf_text, re.DOTALL)
    if not m:
        raise ValueError("bloco local.cell_namespaces não encontrado em main.tf")
    block = m.group(1)
    pairs = re.findall(r'([a-zA-Z0-9_-]+)\s*=\s*"([^"]+)"', block)
    return dict(pairs)


def parse_compliance_cells(main_tf_text: str) -> list[str]:
    return _extract_quoted_list(main_tf_text, r"compliance_cells\s*=\s*\[")


def parse_real_namespaces(namespaces_yaml_path: pathlib.Path) -> list[tuple[str, str]]:
    """Retorna [(namespace_name, cell_label), ...] para cada Namespace com o
    label adserver.hojex/cell (ignora namespaces sem esse label, ex. futuros
    namespaces técnicos sem célula própria)."""
    docs = yaml.safe_load_all(namespaces_yaml_path.read_text())
    out = []
    for doc in docs:
        if not doc or doc.get("kind") != "Namespace":
            continue
        meta = doc.get("metadata", {})
        labels = meta.get("labels", {}) or {}
        cell = labels.get("adserver.hojex/cell")
        if cell:
            out.append((meta.get("name"), cell))
    return out


def real_cell_dirs(cells_dir: pathlib.Path) -> list[str]:
    return sorted(p.name for p in cells_dir.iterdir() if p.is_dir())


def main() -> int:
    errors: list[str] = []

    main_tf_text = MAIN_TF.read_text()
    variables_tf_text = VARIABLES_TF.read_text()

    var_cells = parse_var_cells(variables_tf_text)
    cell_namespaces = parse_cell_namespaces(main_tf_text)
    compliance_cells = parse_compliance_cells(main_tf_text)
    real_ns = parse_real_namespaces(NAMESPACES_YAML)
    real_cells_with_dir = real_cell_dirs(CELLS_DIR)

    # 1. var.cells == chaves de cell_namespaces
    if set(var_cells) != set(cell_namespaces.keys()):
        errors.append(
            "DRIFT interno root OpenTofu: var.cells (variables.tf) = "
            f"{sorted(var_cells)} != chaves de local.cell_namespaces (main.tf) = "
            f"{sorted(cell_namespaces.keys())}"
        )

    # 2. compliance_cells ⊆ var.cells
    missing_from_cells = set(compliance_cells) - set(var_cells)
    if missing_from_cells:
        errors.append(
            "local.compliance_cells (main.tf) contém célula(s) ausente(s) de "
            f"var.cells (variables.tf): {sorted(missing_from_cells)}"
        )

    # 3. compliance_cells == diretórios reais de platform/cells/ (a checagem
    #    que pega o achado #20: "aml" vs "aml-kyc").
    if set(compliance_cells) != set(real_cells_with_dir):
        errors.append(
            "DRIFT de nome de célula compliance: local.compliance_cells (main.tf) = "
            f"{sorted(compliance_cells)} != diretórios reais em platform/cells/ = "
            f"{real_cells_with_dir}"
        )

    # 4. cada (célula, namespace) de cell_namespaces deve existir de fato em
    #    platform/k8s/namespaces.yaml com o mesmo label de célula.
    real_ns_set = set(real_ns)
    for cell, ns in cell_namespaces.items():
        if (ns, cell) not in real_ns_set:
            errors.append(
                f"local.cell_namespaces[{cell!r}] = {ns!r} (main.tf) não bate com "
                f"nenhum Namespace real em platform/k8s/namespaces.yaml rotulado "
                f"adserver.hojex/cell={cell!r} e metadata.name={ns!r}"
            )

    if errors:
        print("== check-cell-consistency: FALHOU ==", file=sys.stderr)
        for e in errors:
            print(f"ERRO: {e}", file=sys.stderr)
        return 1

    print(
        "OK — nomes de célula consistentes entre platform/tofu/{main,variables}.tf, "
        "platform/k8s/namespaces.yaml e platform/cells/*."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
