# make/web.mk — alvos do console Next.js
# Fan-out do root Makefile (-include make/*.mk).
# NÃO editar o root Makefile para adicionar estes alvos.

WEB_DIR := web/console

## web-install: instala dependências do console
web-install:
	cd $(WEB_DIR) && npm install

## web-typecheck: tsc --noEmit strict no console
web-typecheck:
	cd $(WEB_DIR) && npm run typecheck

## web-build: next build (produção)
web-build:
	cd $(WEB_DIR) && npm run build

## web-lint: next lint (max-warnings 0)
web-lint:
	cd $(WEB_DIR) && npm run lint

## web-test: testes unitários com o runner NATIVO do Node (node:test), sem jest/vitest.
## O console não tem framework de teste instalado; usa o type-stripping nativo do
## Node 24 para rodar .ts diretamente (ver src/lib/session-guard.test.ts — fail-closed
## do middleware, G0/frontend E9). Determinístico e sem rede (nenhum npm install).
web-test:
	cd $(WEB_DIR) && node --test $$(find src -name '*.test.ts')

## web-ci: typecheck + lint + testes unitários (sem build — build é separado e lento)
web-ci: web-typecheck web-lint web-test
	@echo "OK — Web CI verde."

.PHONY: web-install web-typecheck web-build web-lint web-test web-ci
