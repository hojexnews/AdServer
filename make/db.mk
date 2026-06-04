# make/db.mk — alvos de banco de dados (Fase 1+3).
# Incluído automaticamente pelo root Makefile via: -include make/*.mk
# NÃO edite o root Makefile para adicionar alvos deste fragmento.
#
# Dois modos de operação:
#
#   1. Postgres externo (DATABASE_URL):
#      db-migrate-up / db-migrate-down / db-migrate-status
#      db-test / db-test-compliance / db-test-ledger / db-test-vector
#      Requer: golang-migrate CLI no PATH e DATABASE_URL exportado.
#
#   2. Postgres efêmero via Docker (db-test-all):
#      Sobe um container postgres:16, aplica TODAS as migrations de todos
#      os schemas em ordem, roda TODOS os rls_isolation_test.sql, aplica
#      os _down e confirma reversão. Derruba o container ao fim.
#      Requer: docker no PATH.
#      Nenhuma credencial externa necessária — completamente efêmero.
#
# Ordem de aplicação obrigatória das migrations (dependências de schema):
#   asset_registry → config (depende de asset_registry) →
#   ledger (independente mas antes de compliance) →
#   vector (independente) →
#   compliance (depende de config para current_tenant_id())

MIGRATE := $(shell command -v migrate 2>/dev/null || echo migrate)

# Schemas e suas pastas de migrations (ordem de aplicação obrigatória).
# compliance é o último: depende do schema config (função current_tenant_id).
DB_SCHEMAS     := asset_registry config ledger vector compliance
DB_SCHEMAS_REV := compliance vector ledger config asset_registry

# Container efêmero para db-test-all.
_DB_TEST_CONTAINER := adserver-db-test-ephemeral
_DB_TEST_PORT      := 15432
_DB_TEST_USER      := postgres
_DB_TEST_PASS      := postgres_test_only
_DB_TEST_DB        := adserver_test
_DB_TEST_DSN       := postgres://$(_DB_TEST_USER):$(_DB_TEST_PASS)@localhost:$(_DB_TEST_PORT)/$(_DB_TEST_DB)?sslmode=disable

## db-lint: roda o guard anti-float (TX-2) sobre todos os arquivos SQL em db/
db-lint:
	@echo "== db-lint: verificando tipos flutuantes em db/ (TX-2) =="
	@bash scripts/ci/no-float-sql.sh db/
	@echo "== db-lint: ok =="

## db-migrate-up: aplica todas as migrations em ordem (asset_registry → config → ledger → vector → compliance)
db-migrate-up: _db-check-url
	@for schema in $(DB_SCHEMAS); do \
	  echo "-- migrate up: $$schema"; \
	  $(MIGRATE) -database "$(DATABASE_URL)" -path "db/$$schema/migrations" up || exit 1; \
	done
	@echo "== db-migrate-up: concluído =="

## db-migrate-down: reverte UM passo de cada schema (em ordem inversa: compliance → vector → ledger → config → asset_registry)
db-migrate-down: _db-check-url
	@for schema in $(DB_SCHEMAS_REV); do \
	  echo "-- migrate down 1: $$schema"; \
	  $(MIGRATE) -database "$(DATABASE_URL)" -path "db/$$schema/migrations" down 1 || exit 1; \
	done
	@echo "== db-migrate-down: concluído =="

## db-migrate-status: exibe versão corrente de cada schema
db-migrate-status: _db-check-url
	@for schema in $(DB_SCHEMAS); do \
	  echo "-- status: $$schema"; \
	  $(MIGRATE) -database "$(DATABASE_URL)" -path "db/$$schema/migrations" version; \
	done

## db-test: executa o teste de isolamento RLS (TX-3) do schema config contra Postgres externo.
##   Requer DATABASE_URL e que as migrations do schema config já tenham sido aplicadas.
##   O teste roda dentro de ROLLBACK — não persiste dados.
db-test: _db-check-url
	@echo "== db-test: isolamento RLS por tenant — schema config (TX-3) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/config/tests/rls_isolation_test.sql
	@echo "== db-test: concluído =="

## db-test-compliance: executa o teste de isolamento RLS do cofre de compliance (K6 / TX-3).
db-test-compliance: _db-check-url
	@echo "== db-test-compliance: isolamento RLS cofre compliance (K6 / TX-3) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/compliance/tests/rls_isolation_test.sql
	@echo "== db-test-compliance: concluído =="

## db-test-ledger: executa o teste de isolamento RLS do ledger (Fase 3 / TX-3 / DA-11).
##   Valida isolamento de accounts, journal_entries, postings e account_balances (view).
##   Requer DATABASE_URL e que as migrations 0001/0002/0003 do schema ledger tenham sido aplicadas.
##   O teste roda dentro de ROLLBACK — não persiste dados.
db-test-ledger: _db-check-url
	@echo "== db-test-ledger: isolamento RLS ledger (Fase 3 / TX-3 / DA-11) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/ledger/tests/rls_isolation_test.sql
	@echo "== db-test-ledger: concluído =="

## db-test-vector: executa o teste de isolamento RLS do schema vector_store.
##   Requer DATABASE_URL e que as migrations do schema vector tenham sido aplicadas.
##   O teste roda dentro de ROLLBACK — não persiste dados.
db-test-vector: _db-check-url
	@echo "== db-test-vector: isolamento RLS embeddings (TX-3) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/vector/tests/vector_rls_isolation_test.sql
	@echo "== db-test-vector: concluído =="

## db-test-all: sobe Postgres efêmero (Docker), aplica TODAS as migrations,
##   roda TODOS os rls_isolation_test.sql (config, ledger, vector, compliance),
##   aplica _down e confirma reversão. Derruba o container ao fim.
##   Sem DATABASE_URL externo — completamente efêmero e sem credenciais.
##   Requer: docker no PATH.
db-test-all:
	@echo "== db-test-all: iniciando Postgres efêmero =="
	@if ! command -v docker >/dev/null 2>&1; then \
	  echo "ERRO: docker não encontrado no PATH. db-test-all requer Docker."; \
	  exit 1; \
	fi
	@if ! command -v psql >/dev/null 2>&1; then \
	  echo "ERRO: psql não encontrado no PATH. db-test-all requer psql (cliente PostgreSQL)."; \
	  exit 1; \
	fi
	@# Garante que não há container residual de execução anterior.
	@docker rm -f $(_DB_TEST_CONTAINER) >/dev/null 2>&1 || true
	@echo "-- subindo postgres:16 na porta $(_DB_TEST_PORT) ..."
	@docker run -d \
	  --name $(_DB_TEST_CONTAINER) \
	  -e POSTGRES_USER=$(_DB_TEST_USER) \
	  -e POSTGRES_PASSWORD=$(_DB_TEST_PASS) \
	  -e POSTGRES_DB=$(_DB_TEST_DB) \
	  -p $(_DB_TEST_PORT):5432 \
	  postgres:16 >/dev/null
	@echo "-- aguardando Postgres ficar pronto ..."
	@_DSN="$(_DB_TEST_DSN)"; \
	 for i in $$(seq 1 30); do \
	   psql "$$_DSN" -c "SELECT 1" >/dev/null 2>&1 && break; \
	   [ "$$i" = "30" ] && { echo "ERRO: Postgres não ficou pronto em 30s"; docker rm -f $(_DB_TEST_CONTAINER); exit 1; }; \
	   sleep 1; \
	 done
	@echo "-- Postgres pronto."
	@# --------------------------------------------------------------------------
	@# FASE 1: aplicar roles de dev (adserver_loader BYPASSRLS / adserver_app RLS)
	@# DEVE rodar antes das migrations que criam GRANTs e dependem dos roles.
	@# --------------------------------------------------------------------------
	@echo "-- criando roles de aplicação (adserver_loader / adserver_app) ..."
	@psql "$(_DB_TEST_DSN)" -v ON_ERROR_STOP=1 -q \
	  -c "DO \$$\$$ BEGIN \
	       IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_loader') THEN \
	         CREATE ROLE adserver_loader LOGIN PASSWORD 'loader_dev_only' BYPASSRLS; \
	       END IF; \
	       IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_app') THEN \
	         CREATE ROLE adserver_app LOGIN PASSWORD 'app_dev_only'; \
	       END IF; \
	     END \$$\$$;"
	@# --------------------------------------------------------------------------
	@# FASE 2: aplicar migrations _up em ordem obrigatória
	@# --------------------------------------------------------------------------
	@echo "-- aplicando migrations _up ..."
	@_DSN="$(_DB_TEST_DSN)"; FAIL=0; \
	 psql "$$_DSN" -v ON_ERROR_STOP=1 -q \
	   -f db/asset_registry/migrations/0001_asset_registry_up.sql || FAIL=1; \
	 echo "  up: asset_registry/0001"; \
	 for f in 0001_config_schema 0002_config_rls 0003_campaign_zones_rls; do \
	   echo "  up: config/$$f"; \
	   psql "$$_DSN" -v ON_ERROR_STOP=1 -q \
	     -f "db/config/migrations/$${f}_up.sql" || FAIL=1; \
	 done; \
	 for f in 0001_ledger_schema 0002_reconciliation_exceptions 0003_ledger_rls; do \
	   echo "  up: ledger/$$f"; \
	   psql "$$_DSN" -v ON_ERROR_STOP=1 -q \
	     -f "db/ledger/migrations/$${f}_up.sql" || FAIL=1; \
	 done; \
	 for f in 0001_vector_schema 0002_vector_rls; do \
	   echo "  up: vector/$$f"; \
	   psql "$$_DSN" -v ON_ERROR_STOP=1 -q \
	     -f "db/vector/migrations/$${f}_up.sql" || FAIL=1; \
	 done; \
	 psql "$$_DSN" -v ON_ERROR_STOP=1 -q \
	   -f db/compliance/migrations/0001_compliance_schema_up.sql || FAIL=1; \
	 echo "  up: compliance/0001_compliance_schema"; \
	 if [ "$$FAIL" = "1" ]; then \
	   echo "ERRO: migrations _up falharam."; docker rm -f $(_DB_TEST_CONTAINER); exit 1; \
	 fi
	@# --------------------------------------------------------------------------
	@# FASE 3: aplicar grants sobre os schemas criados pelas migrations.
	@# dev_roles.sql cobre config + asset_registry; adicionar ledger/vector/compliance.
	@# --------------------------------------------------------------------------
	@echo "-- aplicando grants sobre os schemas ..."
	@psql "$(_DB_TEST_DSN)" -v ON_ERROR_STOP=1 -q -f db/seed/dev_roles.sql
	@psql "$(_DB_TEST_DSN)" -v ON_ERROR_STOP=1 -q \
	  -c "GRANT USAGE ON SCHEMA ledger TO adserver_loader, adserver_app;" \
	  -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ledger TO adserver_app;" \
	  -c "GRANT SELECT ON ALL TABLES IN SCHEMA ledger TO adserver_loader;" \
	  -c "GRANT USAGE ON ALL SEQUENCES IN SCHEMA ledger TO adserver_app;" \
	  -c "GRANT EXECUTE ON FUNCTION ledger.current_tenant_id() TO adserver_app, adserver_loader;" \
	  -c "GRANT USAGE ON SCHEMA vector_store TO adserver_loader, adserver_app;" \
	  -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA vector_store TO adserver_app;" \
	  -c "GRANT SELECT ON ALL TABLES IN SCHEMA vector_store TO adserver_loader;" \
	  -c "GRANT USAGE ON SCHEMA compliance TO adserver_loader, adserver_app;" \
	  -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA compliance TO adserver_app;" \
	  -c "GRANT SELECT ON ALL TABLES IN SCHEMA compliance TO adserver_loader;"
	@# --------------------------------------------------------------------------
	@# FASE 4: rodar todos os testes de isolamento RLS
	@# --------------------------------------------------------------------------
	@echo "== db-test-all: rodando testes de isolamento RLS =="
	@_DSN="$(_DB_TEST_DSN)"; FAIL=0; \
	 for schema_test in \
	   "config:db/config/tests/rls_isolation_test.sql" \
	   "ledger:db/ledger/tests/rls_isolation_test.sql" \
	   "vector:db/vector/tests/vector_rls_isolation_test.sql" \
	   "compliance:db/compliance/tests/rls_isolation_test.sql"; do \
	   schema=$$(echo "$$schema_test" | cut -d: -f1); \
	   testfile=$$(echo "$$schema_test" | cut -d: -f2); \
	   echo "-- RLS test: $$schema"; \
	   if psql "$$_DSN" -v ON_ERROR_STOP=1 -f "$$testfile" 2>&1; then \
	     echo "   PASS: $$schema"; \
	   else \
	     echo "   FAIL: $$schema"; \
	     FAIL=1; \
	   fi; \
	 done; \
	 echo ""; \
	 if [ "$$FAIL" = "1" ]; then \
	   echo "== db-test-all: ALGUM TESTE FALHOU =="; \
	   docker rm -f $(_DB_TEST_CONTAINER) >/dev/null 2>&1; \
	   exit 1; \
	 fi; \
	 echo "== db-test-all: todos os RLS tests PASSARAM =="
	@# --------------------------------------------------------------------------
	@# FASE 5: aplicar migrations _down em ordem inversa e confirmar reversão
	@# --------------------------------------------------------------------------
	@echo "-- aplicando migrations _down (reversão) ..."
	@_DSN="$(_DB_TEST_DSN)"; FAIL=0; \
	 psql "$$_DSN" -v ON_ERROR_STOP=1 -q \
	   -f db/compliance/migrations/0001_compliance_schema_down.sql || FAIL=1; \
	 for f in 0002_vector_rls 0001_vector_schema; do \
	   psql "$$_DSN" -v ON_ERROR_STOP=1 -q \
	     -f "db/vector/migrations/$${f}_down.sql" || FAIL=1; \
	 done; \
	 for f in 0003_ledger_rls 0002_reconciliation_exceptions 0001_ledger_schema; do \
	   psql "$$_DSN" -v ON_ERROR_STOP=1 -q \
	     -f "db/ledger/migrations/$${f}_down.sql" || FAIL=1; \
	 done; \
	 for f in 0003_campaign_zones_rls 0002_config_rls 0001_config_schema; do \
	   psql "$$_DSN" -v ON_ERROR_STOP=1 -q \
	     -f "db/config/migrations/$${f}_down.sql" || FAIL=1; \
	 done; \
	 psql "$$_DSN" -v ON_ERROR_STOP=1 -q \
	   -f db/asset_registry/migrations/0001_asset_registry_down.sql || FAIL=1; \
	 if [ "$$FAIL" = "1" ]; then \
	   echo "ERRO: migrations _down falharam."; docker rm -f $(_DB_TEST_CONTAINER); exit 1; \
	 fi; \
	 echo "-- reversão concluída."
	@# --------------------------------------------------------------------------
	@# FASE 6: derrubar container efêmero
	@# --------------------------------------------------------------------------
	@docker rm -f $(_DB_TEST_CONTAINER) >/dev/null
	@echo "== db-test-all: PASS — up + RLS tests + down OK. Container removido. =="

# Alvo interno: verifica se DATABASE_URL está definido
_db-check-url:
	@if [ -z "$(DATABASE_URL)" ]; then \
	  echo "ERRO: DATABASE_URL não está definido."; \
	  echo "  export DATABASE_URL=postgres://user:pass@host:5432/adserver?sslmode=require"; \
	  exit 1; \
	fi

.PHONY: db-lint db-migrate-up db-migrate-down db-migrate-status \
        db-test db-test-compliance db-test-ledger db-test-vector db-test-all \
        _db-check-url
