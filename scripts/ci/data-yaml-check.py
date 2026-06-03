#!/usr/bin/env python3
"""
scripts/ci/data-yaml-check.py
Verifica que todos os YAML em data/iceberg/specs/ e data/redpanda/ sao bem formados.
"""
import glob
import sys
import yaml

files = (
    glob.glob("data/iceberg/specs/*.yaml")
    + glob.glob("data/redpanda/*.yaml")
)

if not files:
    print("data-yaml-check: nenhum arquivo YAML encontrado (ok)")
    sys.exit(0)

fail = 0
for f in sorted(files):
    try:
        with open(f) as fh:
            yaml.safe_load(fh)
        print(f"  ok: {f}")
    except yaml.YAMLError as e:
        print(f"  ERRO YAML em {f}: {e}", file=sys.stderr)
        fail = 1

if fail == 0:
    print("data-yaml-check: ok")
sys.exit(fail)
