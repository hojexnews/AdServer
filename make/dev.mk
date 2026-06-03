# make/dev.mk — ambiente de integração local (I5).
# Incluído pelo root Makefile via -include make/*.mk. Prefixo: dev-.
#
# Dois caminhos:
#   1) Postgres local (sem Docker) — alvos dev-db-* / dev-it / dev-decision-run.
#      É o caminho VERIFICADO: monta o schema+seed e roda o loader/decision.
#   2) Docker Compose — alvos dev-up* / dev-down / dev-smoke (sobe o stack
#      completo: Postgres+Redis+Redpanda+ClickHouse+serviços). Requer Docker.
#
# Pré-requisitos do caminho 1: psql no PATH e um Postgres 16 local acessível
# via socket como usuário com privilégio de superuser (migrations + seed criam
# schema e inserem sob FORCE RLS).

DEV_DB        ?= adserver_dev
DEV_PGHOST    ?= /var/run/postgresql
DEV_PGUSER    ?= $(shell id -un)
DEV_SUPER_DSN ?= postgres://$(DEV_PGUSER)@/postgres?host=$(DEV_PGHOST)&sslmode=disable
DEV_ADMIN_DSN ?= postgres://$(DEV_PGUSER)@/$(DEV_DB)?host=$(DEV_PGHOST)&sslmode=disable
# DSN do loader (papel BYPASSRLS) via TCP — usado pelo decision e pelo teste IT.
DEV_LOADER_DSN ?= postgres://adserver_loader:loader_dev_only@localhost:5432/$(DEV_DB)?sslmode=disable

COMPOSE := docker compose -f deploy/local/docker-compose.yml

.PHONY: dev-help dev-db-setup dev-db-drop dev-it dev-decision-run \
        dev-up dev-up-streaming dev-down dev-smoke dev-validate

## dev-help: lista os alvos do ambiente local
dev-help:
	@echo ""
	@echo "Ambiente local (I5):"
	@grep -E '^## dev-' make/dev.mk | sed 's/^## //' | awk -F': ' '{printf "  \033[1m%-22s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  DEV_DB=$(DEV_DB)  loader DSN=$(DEV_LOADER_DSN)"

## dev-db-setup: cria $(DEV_DB), aplica migrations + roles + seed (Postgres local)
dev-db-setup:
	@echo "== dev-db-setup: (re)criando $(DEV_DB) =="
	@psql "$(DEV_SUPER_DSN)" -v ON_ERROR_STOP=1 -q -c "DROP DATABASE IF EXISTS $(DEV_DB);"
	@psql "$(DEV_SUPER_DSN)" -v ON_ERROR_STOP=1 -q -c "CREATE DATABASE $(DEV_DB);"
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/asset_registry/migrations/0001_asset_registry_up.sql
	@for f in 0001_config_schema 0002_config_rls 0003_campaign_zones_rls; do \
	  psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/config/migrations/$$f\_up.sql; \
	done
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/ledger/migrations/0001_ledger_schema_up.sql
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/seed/dev_roles.sql
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/seed/dev_seed.sql
	@echo "== dev-db-setup: OK. Loader DSN:"
	@echo "   $(DEV_LOADER_DSN)"

## dev-db-drop: remove o banco de dev $(DEV_DB)
dev-db-drop:
	@psql "$(DEV_SUPER_DSN)" -v ON_ERROR_STOP=1 -q -c "DROP DATABASE IF EXISTS $(DEV_DB);"
	@echo "== dev-db-drop: $(DEV_DB) removido =="

## dev-it: roda o teste de integração do loader contra $(DEV_DB) (papel BYPASSRLS)
dev-it:
	CONFIGLOAD_TEST_DSN="$(DEV_LOADER_DSN)" go test -count=1 -run Integration -v ./internal/configload/

## dev-decision-run: sobe o decision service local contra $(DEV_DB) (Ctrl-C p/ parar)
dev-decision-run:
	DATABASE_URL="$(DEV_LOADER_DSN)" \
	CAPPING_SALT="dev-salt" CK_HMAC_SECRET="dev-ck-secret" \
	DECISION_ADDR=":8080" SNAPSHOT_REFRESH_INTERVAL="15s" \
	go run ./services/decision/cmd/decision/

## dev-up: sobe o stack core (postgres+redis+decision+collector) via Docker
dev-up:
	$(COMPOSE) up -d --build

## dev-up-streaming: sobe o stack completo (+ redpanda + clickhouse, wire JSON)
dev-up-streaming:
	REDPANDA_BROKERS=redpanda:9092 TELEMETRY_WIRE_FORMAT=json \
	$(COMPOSE) --profile streaming up -d --build

## dev-down: derruba o stack e remove volumes
dev-down:
	$(COMPOSE) down -v

## dev-smoke: smoke end-to-end (decision serve anúncio real do seed)
dev-smoke:
	bash deploy/local/smoke.sh

## dev-validate: valida YAML do compose e sintaxe dos scripts (sem Docker)
dev-validate:
	@python3 -c "import yaml,sys; yaml.safe_load(open('deploy/local/docker-compose.yml')); print('compose YAML ok')"
	@for s in deploy/local/postgres/10-init.sh deploy/local/redpanda/topics.sh \
	          deploy/local/clickhouse/10-ddl.sh deploy/local/smoke.sh; do \
	  bash -n "$$s" && echo "bash ok: $$s"; \
	done
