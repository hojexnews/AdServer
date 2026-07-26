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
# DSN do writer de telemetria via Postgres (onda "perfil BETA" / addon §3.3):
# role criada de forma auto-contida por db/stats/migrations/0001_stats_schema_up.sql
# (INSERT-only em stats.events_raw, NOBYPASSRLS — ver comentário da migration).
# Valor a passar em TELEMETRY_PG_DSN para services/collector/cmd/collector.
DEV_STATS_WRITER_DSN ?= postgres://adserver_stats_writer:stats_writer_dev_only@localhost:5432/$(DEV_DB)?sslmode=disable

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
	@# Ledger: TODAS as 4 migrations, não só a 0001 (achado C-1,
	@# security-reviewer). Antes desta correção, dev-db-setup aplicava
	@# SÓ 0001_ledger_schema_up.sql — schema+tabelas, sem RLS (0003) e sem o
	@# trigger de imutabilidade append-only (0004). Consequência prática:
	@# ledger.current_tenant_id() nem existia neste banco (0003 é quem a
	@# cria) e a FORCE ROW LEVEL SECURITY do ledger nunca chegava a ser
	@# habilitada no beta local — conceder acesso a adserver_app sem isto
	@# seria PIOR que continuar usando adserver_loader (acesso total, sem
	@# nenhuma barreira). Ordem numérica é uma dependência real aqui: 0003
	@# referencia as tabelas da 0001, 0004 referencia o trigger da 0001.
	@for f in 0001_ledger_schema 0002_reconciliation_exceptions 0003_ledger_rls 0004_ledger_postings_immutable; do \
	  psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/ledger/migrations/$$f\_up.sql; \
	done
	@# stats (onda "perfil BETA" / addon §3.3): persistência do collector via
	@# Postgres — internal/telemetry/pgsink. A migration cria só o schema,
	@# tabela, view, função e policy — NÃO cria mais nenhum role (achado H-2,
	@# security-reviewer: a versão anterior fazia `CREATE ROLE ... LOGIN
	@# PASSWORD` inline dentro da migration, o que um `make db-migrate-up`
	@# comum rodaria contra QUALQUER DATABASE_URL, inclusive staging/produção).
	@# adserver_stats_writer agora é criada por db/seed/dev_roles.sql, junto
	@# com adserver_app/adserver_loader/adserver_copilot — logo este passo
	@# pode rodar em qualquer ponto ANTES de dev_roles.sql (só precisa que o
	@# schema `stats` exista antes dos GRANTs comentados dela, aplicados
	@# abaixo). Ordem canônica com DB_SCHEMAS/DB_SCHEMAS_REV em make/db.mk:
	@# ledger -> stats -> vector.
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/stats/migrations/0001_stats_schema_up.sql
	@# vector precisa existir ANTES de db/seed/dev_roles.sql (que faz GRANT ... ON
	@# SCHEMA vector_store TO adserver_copilot) — mesma ordem canonica de
	@# DB_SCHEMAS em make/db.mk e do workflow .github/workflows/db.yml
	@# (asset_registry -> config -> ledger -> vector -> compliance). Faltar este
	@# passo faz dev_roles.sql abortar com "schema vector_store does not exist"
	@# (achado HIGH / bundle F).
	@for f in 0001_vector_schema 0002_vector_rls; do \
	  psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/vector/migrations/$$f\_up.sql; \
	done
	@# db/seed/dev_roles.sql cria adserver_loader/adserver_app/adserver_copilot
	@# E (achado H-2) adserver_stats_writer. TUDO que segue abaixo depende
	@# desses quatro roles já existirem.
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/seed/dev_roles.sql
	@# Grants de leitura em stats para adserver_app (BFF), executados AQUI —
	@# depois de dev_roles.sql garantir que adserver_app existe. Descomentados
	@# a partir do bloco de exemplo deixado em
	@# db/stats/migrations/0001_stats_schema_up.sql (não executado por lá de
	@# propósito, para a migration não falhar num banco sem adserver_app).
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q \
	  -c "GRANT USAGE ON SCHEMA stats TO adserver_app;" \
	  -c "GRANT SELECT ON stats.events_raw TO adserver_app;" \
	  -c "GRANT SELECT ON stats.live_kpis TO adserver_app;" \
	  -c "GRANT EXECUTE ON FUNCTION stats.current_tenant_id() TO adserver_app;"
	@# Leitura em stats para adserver_loader (BYPASSRLS) — o construtor de
	@# snapshot (internal/configload) precisa somar as contagens ENTREGUES por
	@# campanha para que o pacing DA-4 deixe de ser inerte (hoje
	@# snapshot.Campaign.DeliveredImpressions nunca é escrito por nenhum código
	@# de produção, então computeDeficit devolve 1.0 sempre). O loader já lê
	@# todo o `config` de todos os tenants pelo mesmo motivo — ler `stats` é o
	@# mesmo escopo, não um alargamento. `make beta-check` também consulta
	@# stats.events_raw por este DSN.
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q \
	  -c "GRANT USAGE ON SCHEMA stats TO adserver_loader;" \
	  -c "GRANT SELECT ON stats.events_raw TO adserver_loader;" \
	  -c "GRANT SELECT ON stats.live_kpis TO adserver_loader;" \
	  -c "GRANT EXECUTE ON FUNCTION stats.current_tenant_id() TO adserver_loader;"
	@# Escrita em stats para adserver_stats_writer (o role do pgsink,
	@# TELEMETRY_PG_DSN) — GRANTs comentados na própria migration (achado H-2,
	@# mesmo motivo do bloco de adserver_app acima: a migration não pode
	@# depender de um role que ela mesma não cria mais).
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q \
	  -c "GRANT USAGE ON SCHEMA stats TO adserver_stats_writer;" \
	  -c "GRANT SELECT, INSERT ON stats.events_raw TO adserver_stats_writer;" \
	  -c "GRANT EXECUTE ON FUNCTION stats.current_tenant_id() TO adserver_stats_writer;"
	@# Ledger para adserver_app (achado C-1, security-reviewer): sem isto o
	@# BFF (bff/src/adapters/postgres-payments.ts, PostgresPaymentsAdapter)
	@# não tinha NENHUM role NOBYPASSRLS com acesso a `ledger`, o que empurrava
	@# a doc do beta local a usar adserver_loader (BYPASSRLS) para o BFF
	@# inteiro — desligando RLS de toda a aplicação, não só do ledger. Mesmo
	@# bloco de .github/workflows/db.yml e da FASE 3 de make/db.mk::db-test-all
	@# (fonte única de verdade replicada aqui, não inventada). O REVOKE
	@# reafirma o least-privilege já provado pelo trigger
	@# postings_immutable_trg (migration 0004 do ledger): adserver_app nunca
	@# faz UPDATE/DELETE em ledger.postings (internal/ledger/posting.go só
	@# faz INSERT/SELECT).
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q \
	  -c "GRANT USAGE ON SCHEMA ledger TO adserver_loader, adserver_app;" \
	  -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ledger TO adserver_app;" \
	  -c "GRANT SELECT ON ALL TABLES IN SCHEMA ledger TO adserver_loader;" \
	  -c "GRANT USAGE ON ALL SEQUENCES IN SCHEMA ledger TO adserver_app;" \
	  -c "GRANT EXECUTE ON FUNCTION ledger.current_tenant_id() TO adserver_app, adserver_loader;" \
	  -c "REVOKE UPDATE, DELETE ON ledger.postings FROM adserver_app;"
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/seed/dev_seed.sql
	@# Provisionamento real do publisher Hojex News: zonas por-placement (E11).
	@psql "$(DEV_ADMIN_DSN)" -v ON_ERROR_STOP=1 -q -f db/seed/hojex_news_seed.sql
	@echo "== dev-db-setup: OK. Loader DSN:"
	@echo "   $(DEV_LOADER_DSN)"
	@echo "   Telemetry (pgsink) writer DSN — export as TELEMETRY_PG_DSN for services/collector:"
	@echo "   $(DEV_STATS_WRITER_DSN)"

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
