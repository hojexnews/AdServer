# make/bff.mk — alvos do BFF (Node/TS + tRPC v11)
# Fan-out do root Makefile (-include make/*.mk).
# NÃO editar o root Makefile para adicionar estes alvos.

BFF_DIR := bff

## bff-install: instala dependências do BFF a partir do lockfile.
## `npm ci` (não `npm install`) — mesma razão de make/web.mk: reprodutibilidade
## e falha dura em drift de lockfile.
bff-install:
	cd $(BFF_DIR) && npm ci

## bff-typecheck: tsc --noEmit strict no BFF
bff-typecheck:
	cd $(BFF_DIR) && npm run typecheck

## bff-build: compila o BFF para dist/
bff-build:
	cd $(BFF_DIR) && npm run build

## bff-lint: ESLint no BFF (max-warnings 0)
bff-lint:
	cd $(BFF_DIR) && npm run lint

## bff-test: testes unitários do BFF (jest)
bff-test:
	cd $(BFF_DIR) && npm test

## bff-ci: typecheck + lint + test (sem build — build é separado)
bff-ci: bff-typecheck bff-lint bff-test
	@echo "OK — BFF CI verde."

.PHONY: bff-install bff-typecheck bff-build bff-lint bff-test bff-ci
