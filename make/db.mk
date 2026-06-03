# make/db.mk — alvos de banco de dados (Fase 1, I1).
# Incluído automaticamente pelo root Makefile via: -include make/*.mk
# NÃO edite o root Makefile para adicionar alvos deste fragmento.
#
# Pré-requisitos:
#   - golang-migrate CLI no PATH (https://github.com/golang-migrate/migrate)
#   - DATABASE_URL exportado: postgres://user:pass@host:5432/adserver?sslmode=require
#   - scripts/ci/no-float-sql.sh presente (guard TX-2)
#
# Não há Postgres rodando em CI durante Fase 1. Os alvos db-migrate-* exigem
# instância Postgres 16 local com DATABASE_URL configurado.

MIGRATE := $(shell command -v migrate 2>/dev/null || echo migrate)

# Schemas e suas pastas de migrations (ordem de aplicação obrigatória)
DB_SCHEMAS := asset_registry config ledger

## db-lint: roda o guard anti-float (TX-2) sobre todos os arquivos SQL em db/
db-lint:
	@echo "== db-lint: verificando tipos flutuantes em db/ (TX-2) =="
	@bash scripts/ci/no-float-sql.sh db/
	@echo "== db-lint: ok =="

## db-migrate-up: aplica todas as migrations em ordem (asset_registry → config → ledger)
db-migrate-up: _db-check-url
	@for schema in $(DB_SCHEMAS); do \
	  echo "-- migrate up: $$schema"; \
	  $(MIGRATE) -database "$(DATABASE_URL)" -path "db/$$schema/migrations" up || exit 1; \
	done
	@echo "== db-migrate-up: concluído =="

## db-migrate-down: reverte UM passo de cada schema (em ordem inversa)
db-migrate-down: _db-check-url
	@for schema in ledger config asset_registry; do \
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

# Alvo interno: verifica se DATABASE_URL está definido
_db-check-url:
	@if [ -z "$(DATABASE_URL)" ]; then \
	  echo "ERRO: DATABASE_URL não está definido."; \
	  echo "  export DATABASE_URL=postgres://user:pass@host:5432/adserver?sslmode=require"; \
	  exit 1; \
	fi

.PHONY: db-lint db-migrate-up db-migrate-down db-migrate-status _db-check-url
