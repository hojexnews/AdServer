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

## web-ci: typecheck + lint (sem build — build é separado e lento)
web-ci: web-typecheck web-lint
	@echo "OK — Web CI verde."

.PHONY: web-install web-typecheck web-build web-lint web-ci
