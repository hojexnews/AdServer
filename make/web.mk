# make/web.mk — alvos do console Next.js
# Fan-out do root Makefile (-include make/*.mk).
# NÃO editar o root Makefile para adicionar estes alvos.

WEB_DIR := web/console

## web-install: instala dependências do console a partir do lockfile.
## `npm ci` (não `npm install`): instalação REPRODUTÍVEL e falha dura se
## package.json e package-lock.json divergirem. Com `npm install`, um lockfile
## dessincronizado era silenciosamente reescrito no runner e a CI validava um
## conjunto de versões diferente do que o repo declara.
web-install:
	cd $(WEB_DIR) && npm ci

## web-bff-deps: garante que bff/node_modules existe antes de qualquer
## typecheck/build do console.
##
## POR QUE ISTO EXISTE (achado da 31ª onda, CRÍTICO): o console NÃO é um
## pacote isolado para efeito de type-checking. web/console/src/types/bff.ts
## reexporta `AppRouter` de ../../../../bff/src/index, então `tsc --noEmit` e
## `next build` carregam TODA a árvore de fontes do BFF no program e resolvem
## `pg`, `zod` e `@trpc/server` a partir de **bff/node_modules**.
## Os workflows web.yml e a11y.yml só rodavam `make web-install`, nunca
## `make bff-install` — em checkout limpo o gate morria com TS2307 em cascata.
## Consequência: `make web-ci` e `make web-a11y` estavam VERMELHOS em CI desde
## que foram criados, e o axe NUNCA chegou a executar. Verde local (onde
## bff/node_modules existe por acaso) mascarava o vermelho de CI.
##
## Alvo definido em make/bff.mk — ambos entram pelo `-include make/*.mk` da raiz.
## O marcador é `.package-lock.json` dentro de node_modules — o npm só o escreve
## ao CONCLUIR a instalação. `test -d node_modules` seria fraco demais: um
## `npm ci` interrompido deixa o diretório existindo pela metade (sem typescript,
## por exemplo) e o guard passaria, devolvendo um `tsc: not found` confuso em vez
## de reinstalar. Verificado na prática durante a 31ª onda.
web-bff-deps:
	@test -f $(BFF_DIR)/node_modules/.package-lock.json || $(MAKE) bff-install

## web-typecheck: tsc --noEmit strict no console
web-typecheck: web-bff-deps
	cd $(WEB_DIR) && npm run typecheck

## web-build: next build (produção)
web-build: web-bff-deps
	cd $(WEB_DIR) && npm run build

## web-lint: eslint --max-warnings 0 (o Next 16 removeu `next lint`; flat config nativa)
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
web-a11y: web-bff-deps
	cd $(WEB_DIR) && npx tsc --noEmit -p scripts/tsconfig.json
	cd $(WEB_DIR) && A11Y_HARNESS=1 npm run build
	cd $(WEB_DIR) && node --test scripts/a11y-check.test.ts

.PHONY: web-install web-bff-deps web-typecheck web-build web-lint web-test web-ci web-a11y
