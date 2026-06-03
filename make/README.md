# make/ — Fragmentos de Makefile por servico/area

O root Makefile inclui todos os arquivos `make/*.mk` via `-include make/*.mk`.
Cada engenheiro/agente versiona seu proprio fragmento aqui, sem tocar no root.

Convencoes:

- `make/go.mk`    — decision-engine, hot path Go (dono: decision-engine-engineer)
- `make/db.mk`    — schema ClickHouse e migrations (dono: clickhouse-sink-engineer)
- `make/data.mk`  — pipelines de dados, feature store (dono: data-engineer)
- `make/bff.mk`   — BFF / Next.js (dono: bff-engineer)

Regras:

1. Nao reusar nomes de alvo que ja existem no root Makefile.
2. Declare `.PHONY` para todos os seus alvos.
3. Prefixe alvos com o nome do seu dominio (ex.: `go-build`, `db-migrate`).
4. O root Makefile define apenas os alvos de contratos/TX: lint, format, breaking,
   gen, verify. Tudo de aplicacao vai nos fragmentos.
