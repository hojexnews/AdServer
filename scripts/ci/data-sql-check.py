#!/usr/bin/env python3
"""
scripts/ci/data-sql-check.py
Verifica erros comuns de sintaxe SQL nas migrations de data/clickhouse/migrations/
sem subir ClickHouse.

Checks (por STATEMENT, nao por arquivo):
  - Toda CREATE TABLE precisa ter sua PROPRIA clausula ENGINE = ... dentro do
    mesmo statement. Um CREATE TABLE sem ENGINE em um arquivo com multiplas
    tabelas (ex.: 002_raw_tables.sql define 5) nao pode ser mascarado por
    outro CREATE TABLE, no mesmo arquivo, que tenha ENGINE valido — cada
    statement e escopado individualmente (split por ';' apos remover
    comentarios de linha).
"""
import pathlib
import re
import sys

CH_DDL_DIR = pathlib.Path("data/clickhouse/migrations")


def strip_sql_comments(text: str) -> str:
    """Remove comentarios de linha (--...) de cada linha, preservando quebras."""
    out = []
    for line in text.splitlines():
        idx = line.find("--")
        if idx != -1:
            line = line[:idx]
        out.append(line)
    return "\n".join(out)


def split_statements(code_text: str):
    """Divide DDL (ja sem comentarios) em statements top-level por ';'."""
    return [s.strip() for s in code_text.split(";") if s.strip()]


fail = 0

ddl_files = sorted(CH_DDL_DIR.glob("*.sql"))
if not ddl_files:
    print("ERRO: nenhum DDL SQL encontrado em data/clickhouse/migrations/", file=sys.stderr)
    sys.exit(1)

for f in ddl_files:
    print(f"  verificando {f}")
    code = strip_sql_comments(f.read_text())
    for stmt in split_statements(code):
        if re.match(r"CREATE\s+TABLE\b", stmt, re.I):
            if not re.search(r"\bENGINE\s*=", stmt, re.I):
                m = re.search(
                    r"CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_.]+)",
                    stmt,
                    re.I,
                )
                table_name = m.group(1) if m else "?"
                print(
                    f"  ERRO: CREATE TABLE sem ENGINE em {f} (tabela: {table_name})",
                    file=sys.stderr,
                )
                fail = 1

if fail == 0:
    print("data-sql-check: ok")
sys.exit(fail)
