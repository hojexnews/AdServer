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

## web-a11y: gate mecânico de acessibilidade WCAG 2.2 AA (axe-core + Chrome
## do sistema via puppeteer-core, SEM Playwright — G0/frontend E10).
## scripts/ tem tsconfig próprio (fora do tsconfig.json do app Next — evita
## colisão de tipos entre puppeteer-core direto e o puppeteer-core aliased
## via "puppeteer" no overrides do package.json, ver comentário lá).
## Builda com A11Y_HARNESS=1 (habilita a rota /a11y-harness, gated e
## inacessível em qualquer outro build) e audita via node:test. Lento
## (build + browser) — de propósito FORA do web-ci rápido; workflow CI
## dedicado em .github/workflows/a11y.yml.
web-a11y:
	cd $(WEB_DIR) && npx tsc --noEmit -p scripts/tsconfig.json
	cd $(WEB_DIR) && A11Y_HARNESS=1 npm run build
	cd $(WEB_DIR) && node --test scripts/a11y-check.test.ts

.PHONY: web-install web-typecheck web-build web-lint web-test web-ci web-a11y
