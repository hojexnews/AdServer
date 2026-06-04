# make/db.mk — alvos de banco de dados (Fase 1, I1).
# Incluído automaticamente pelo root Makefile via: -include make/*.mk
# NÃO edite o root Makefile para adicionar alvos deste fragmento.
#
# Pré-requisitos:
#   - golang-migrate CLI no PATH (https://github.com/golang-migrate/migrate)
#   - DATABASE_URL exportado: postgres://user:pass@host:5432/adserver?sslmode=require
#   - scripts/ci/no-float-sql.sh presente (guard TX-2)
#
# Não há Postgres rodando em CI durante Fase 1. Os alvos db-migrate-* e db-test
# exigem instância Postgres 16 local com DATABASE_URL configurado.

MIGRATE := $(shell command -v migrate 2>/dev/null || echo migrate)

# Schemas e suas pastas de migrations (ordem de aplicação obrigatória)
# compliance e o ultimo: depende do schema config (funcao current_tenant_id) e ledger.
DB_SCHEMAS := asset_registry config ledger compliance

## db-lint: roda o guard anti-float (TX-2) sobre todos os arquivos SQL em db/
db-lint:
	@echo "== db-lint: verificando tipos flutuantes em db/ (TX-2) =="
	@bash scripts/ci/no-float-sql.sh db/
	@echo "== db-lint: ok =="

## db-migrate-up: aplica todas as migrations em ordem (asset_registry → config → ledger → compliance)
db-migrate-up: _db-check-url
	@for schema in $(DB_SCHEMAS); do \
	  echo "-- migrate up: $$schema"; \
	  $(MIGRATE) -database "$(DATABASE_URL)" -path "db/$$schema/migrations" up || exit 1; \
	done
	@echo "== db-migrate-up: concluído =="

## db-migrate-down: reverte UM passo de cada schema (em ordem inversa: compliance → ledger → config → asset_registry)
db-migrate-down: _db-check-url
	@for schema in compliance ledger config asset_registry; do \
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

## db-test: executa o teste de isolamento RLS (TX-3) do schema config contra Postgres local.
##   Requer DATABASE_URL e que as migrations 0001/0002/0003 do schema config
##   já tenham sido aplicadas (make db-migrate-up).
##   O role adserver_app deve existir com GRANT SELECT/INSERT/UPDATE/DELETE
##   em todas as tabelas do schema config (ver db/config/tests/rls_isolation_test.sql).
##   O teste roda dentro de ROLLBACK — não persiste dados.
##
##   Uso:
##     export DATABASE_URL="postgres://adserver_admin:pass@localhost:5432/adserver?sslmode=disable"
##     make db-test
db-test: _db-check-url
	@echo "== db-test: isolamento RLS por tenant — schema config (TX-3) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/config/tests/rls_isolation_test.sql
	@echo "== db-test: concluído =="

## db-test-compliance: executa o teste de isolamento RLS do cofre de compliance (K6 / TX-3).
##   Requer DATABASE_URL e que a migration 0001_compliance_schema_up.sql tenha sido aplicada.
##   O role adserver_app deve existir com GRANT SELECT/INSERT/UPDATE/DELETE
##   em todas as tabelas do schema compliance.
##   O teste roda dentro de ROLLBACK — não persiste dados.
##
##   Uso:
##     export DATABASE_URL="postgres://adserver_admin:pass@localhost:5432/adserver?sslmode=disable"
##     make db-test-compliance
db-test-compliance: _db-check-url
	@echo "== db-test-compliance: isolamento RLS cofre compliance (K6 / TX-3) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/compliance/tests/rls_isolation_test.sql
	@echo "== db-test-compliance: concluído =="

# Alvo interno: verifica se DATABASE_URL está definido
_db-check-url:
	@if [ -z "$(DATABASE_URL)" ]; then \
	  echo "ERRO: DATABASE_URL não está definido."; \
	  echo "  export DATABASE_URL=postgres://user:pass@host:5432/adserver?sslmode=require"; \
	  exit 1; \
	fi

.PHONY: db-lint db-migrate-up db-migrate-down db-migrate-status db-test db-test-compliance _db-check-url
