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
        data-billing-test data-integration-test data-help

# Host/porta do ClickHouse usado por data-integration-test. Sobrescrevivel
# via ambiente (CH_HOST=... CH_PORT=... make data-integration-test).
CH_HOST ?= localhost
CH_PORT ?= 9000

## data-lint: valida DDL SQL, specs YAML e jobs Python de data/ (sem subir ClickHouse)
data-lint: data-no-float data-sql-check data-yaml-check data-py-syntax
	@echo "data-lint: OK — data/ sem violacoes detectadas estaticamente."

## data-no-float: verifica ausencia de float em colunas monetarias de data/ (TX-2)
data-no-float:
	@echo "== data-no-float (TX-2) =="
	@bash scripts/ci/no-float-data-sql.sh

## data-sql-check: verifica erros comuns de sintaxe SQL sem subir ClickHouse
## (por-statement: cada CREATE TABLE precisa de ENGINE dentro do seu proprio
## statement — ver scripts/ci/data-sql-check.py)
data-sql-check:
	@echo "== data-sql-check =="
	@python3 scripts/ci/data-sql-check.py

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
## Usa ml/.venv se existir (dev local); senao cai para python3 do PATH (CI, que
## instala numpy/pandas/mmh3 via pip antes deste alvo — ver .github/workflows/data.yml).
data-ivt-test:
	@echo "== data-ivt-test (TX-6 / J6) =="
	@if [ -x ml/.venv/bin/python ]; then \
		ml/.venv/bin/python data/fraud/test_ivt_scoring_job.py; \
	else \
		python3 data/fraud/test_ivt_scoring_job.py; \
	fi

## data-schema-invariants: verifica invariantes normativas do StatsHourly e billing
data-schema-invariants:
	@echo "== data-schema-invariants =="
	@python3 scripts/ci/data-schema-invariants.py

## data-billing-test: testa a semantica de FLOOR do CPM (BILLING.md 4.1). So stdlib.
data-billing-test:
	@echo "== data-billing-test (CPM floor, BILLING.md 4.1 / sweep MONEY-01) =="
	@python3 data/iceberg/jobs/test_billing_batch_hourly.py

## data-integration-test: aplica as migrations do ClickHouse e roda o teste de
##   isolamento cross-tenant (TX-3, data/clickhouse/tests/tenant_isolation_test.sql)
##   statement-a-statement, com asserções reais (nao so ausencia de erro SQL),
##   contra um ClickHouse REAL. Fecha o achado #10 (31a onda): o arquivo .sql
##   existia mas nenhum alvo make/workflow o executava.
##   NAO tem modo skip/stub: falha em voz alta (exit 1) se clickhouse-client
##   nao estiver no PATH, se nao houver ClickHouse acessivel em
##   CH_HOST:CH_PORT (default localhost:9000), ou se qualquer migration ou
##   asserção falhar. Ao contrario de db-test-all (make/db.mk), este alvo NAO
##   sobe container nenhum sozinho -- espera um ClickHouse ja rodando (dev
##   local, ou o job data-integration-test de .github/workflows/data.yml, que
##   sobe um ClickHouse efemero via `docker run` + data/clickhouse/ci/single_node_cluster.xml).
data-integration-test:
	@echo "== data-integration-test: isolamento cross-tenant (TX-3) contra ClickHouse real =="
	@if ! command -v clickhouse-client >/dev/null 2>&1; then \
	  echo "ERRO: clickhouse-client nao encontrado no PATH. data-integration-test requer um cliente ClickHouse real (sem modo skip/stub)."; \
	  exit 1; \
	fi
	@if ! clickhouse-client --host $(CH_HOST) --port $(CH_PORT) --query "SELECT 1" >/dev/null 2>&1; then \
	  echo "ERRO: nao foi possivel conectar a um ClickHouse em $(CH_HOST):$(CH_PORT)."; \
	  echo "  data-integration-test NAO pula em silencio -- suba um ClickHouse real"; \
	  echo "  (ex.: docker run --rm -p 9000:9000 -v \$$(pwd)/data/clickhouse/ci/single_node_cluster.xml:/etc/clickhouse-server/config.d/single_node_cluster.xml clickhouse/clickhouse-server)"; \
	  echo "  ou exporte CH_HOST/CH_PORT apontando para uma instancia acessivel."; \
	  exit 1; \
	fi
	@echo "-- aplicando migrations (lista derivada do diretorio, ordem lexicografica) --"
	@files=$$(ls $(CH_DDL_DIR)/*.sql 2>/dev/null | LC_ALL=C sort); \
	 if [ -z "$$files" ]; then \
	   echo "ERRO: nenhuma migration *.sql encontrada em $(CH_DDL_DIR) -- sentinela anti-vazio."; \
	   exit 1; \
	 fi; \
	 for f in $$files; do \
	   echo "  aplicando: $$(basename $$f)"; \
	   clickhouse-client --host $(CH_HOST) --port $(CH_PORT) --multiquery < "$$f" || { echo "ERRO: migration $$f falhou ao aplicar."; exit 1; }; \
	 done
	@echo "-- rodando data/clickhouse/tests/tenant_isolation_test.sql (statement-a-statement, com asserções) --"
	@CH_HOST=$(CH_HOST) CH_PORT=$(CH_PORT) python3 scripts/ci/data-integration-test.py

## data-help: lista os alvos de data/
data-help:
	@echo ""
	@echo "Alvos de data/ (I3-prep + J6 IVT, data-platform-engineer):"
	@grep -E '^## data-' make/data.mk | sed 's/^## //' | awk -F': ' '{printf "  \033[1m%-30s\033[0m %s\n", $$1, $$2}'
	@echo ""
