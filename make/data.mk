# make/data.mk — Fragmento de Makefile para data/ (I3-prep).
# Dono: data-platform-engineer.
# Incluido pelo root Makefile via -include make/*.mk.
#
# Convencao (README.md de make/):
#   - Nao reusar nomes de alvo do root Makefile.
#   - Declarar .PHONY para todos os alvos.
#   - Prefixar com 'data-'.
#   - Nao depende de ClickHouse/Redpanda em execucao (validacao estatica).

DATA_DIR         := data
CH_DDL_DIR       := $(DATA_DIR)/clickhouse/migrations
ICEBERG_SPEC_DIR := $(DATA_DIR)/iceberg/specs
ICEBERG_JOBS_DIR := $(DATA_DIR)/iceberg/jobs
RP_DIR           := $(DATA_DIR)/redpanda

.PHONY: data-lint data-no-float data-sql-check data-yaml-check data-py-syntax \
        data-validate data-schema-invariants data-ivt-test data-ivt-sql-check \
        data-help

## data-lint: valida DDL SQL, specs YAML e jobs Python de data/ (sem subir ClickHouse)
data-lint: data-no-float data-sql-check data-yaml-check data-py-syntax
	@echo "data-lint: OK — data/ sem violacoes detectadas estaticamente."

## data-no-float: verifica ausencia de float em colunas monetarias de data/ (TX-2)
data-no-float:
	@echo "== data-no-float (TX-2) =="
	@bash scripts/ci/no-float-data-sql.sh

## data-sql-check: verifica erros comuns de sintaxe SQL sem subir ClickHouse
data-sql-check:
	@echo "== data-sql-check =="
	@fail=0; \
	for f in $(CH_DDL_DIR)/*.sql; do \
		[ -f "$$f" ] || continue; \
		echo "  verificando $$f"; \
		if grep -qiE '^\s*CREATE\s+TABLE' "$$f" && ! grep -qiE 'ENGINE\s*=' "$$f"; then \
			echo "  ERRO: CREATE TABLE sem ENGINE em $$f"; \
			fail=1; \
		fi; \
	done; \
	[ "$$fail" -eq 0 ] || exit 1; \
	echo "data-sql-check: ok"

## data-yaml-check: verifica YAML bem-formado nas specs Iceberg e Redpanda
data-yaml-check:
	@echo "== data-yaml-check =="
	@python3 scripts/ci/data-yaml-check.py

## data-py-syntax: verifica sintaxe Python dos jobs de dados (py_compile)
data-py-syntax:
	@echo "== data-py-syntax =="
	@fail=0; \
	for f in $(ICEBERG_JOBS_DIR)/*.py; do \
		[ -f "$$f" ] || continue; \
		python3 -m py_compile "$$f" && echo "  ok: $$f" || { echo "  ERRO sintaxe Python: $$f"; fail=1; }; \
	done; \
	[ "$$fail" -eq 0 ] || exit 1; \
	echo "data-py-syntax: ok"

## data-validate: lint + invariantes de schema (StatsHourly, billing, ao-vivo, row-policies, IVT)
data-validate: data-lint data-schema-invariants data-ivt-sql-check
	@echo "data-validate: OK — DDL/specs de data/ validados (inclui IVT J6)."

## data-ivt-sql-check: verifica invariantes SQL da migration IVT (007) sem subir ClickHouse
data-ivt-sql-check:
	@echo "== data-ivt-sql-check (TX-6 / J6) =="
	@python3 scripts/ci/data-ivt-sql-check.py

## data-ivt-test: executa testes unitarios do job de scoring IVT com o sample sintetico
data-ivt-test:
	@echo "== data-ivt-test (TX-6 / J6) =="
	@ml/.venv/bin/python data/fraud/test_ivt_scoring_job.py

## data-schema-invariants: verifica invariantes normativas do StatsHourly e billing
data-schema-invariants:
	@echo "== data-schema-invariants =="
	@python3 scripts/ci/data-schema-invariants.py

## data-help: lista os alvos de data/
data-help:
	@echo ""
	@echo "Alvos de data/ (I3-prep + J6 IVT, data-platform-engineer):"
	@grep -E '^## data-' make/data.mk | sed 's/^## //' | awk -F': ' '{printf "  \033[1m%-30s\033[0m %s\n", $$1, $$2}'
	@echo ""
