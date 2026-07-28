# Makefile — DX da camada de contratos (Fase 0).
#
# Alvos de validacao local que espelham a CI. Rode `make help`.
# Requer: buf (https://buf.build) no PATH. `make tools` instala localmente.

SHELL    := bash
BIN      := $(CURDIR)/.bin
BUF      := $(shell command -v buf 2>/dev/null || echo $(BIN)/buf)
BUF_VER  := 1.70.0

# Fan-out paralelo: cada servico/area versiona seu proprio fragmento em make/.
# Ex.: make/go.mk (decision-engine), make/db.mk (clickhouse), make/data.mk.
# Nao edite o root Makefile para adicionar alvos especificos de servico.
-include make/*.mk

.DEFAULT_GOAL := help

## help: lista os alvos
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | awk -F': ' '{printf "  \033[1m%-18s\033[0m %s\n", $$1, $$2}'

## tools: instala o buf localmente em .bin/ (binario unico)
tools:
	@mkdir -p "$(BIN)"
	@if ! command -v buf >/dev/null 2>&1; then \
	  echo "baixando buf $(BUF_VER) -> $(BIN)/buf"; \
	  curl -fsSL -o "$(BIN)/buf" "https://github.com/bufbuild/buf/releases/download/v$(BUF_VER)/buf-$$(uname -s)-$$(uname -m)"; \
	  chmod +x "$(BIN)/buf"; \
	fi
	@"$(BUF)" --version

## proto-lint: buf lint (STANDARD + COMMENTS)
proto-lint:
	"$(BUF)" lint proto

## proto-format: reescreve os .proto no formato canonico (buf format -w)
proto-format:
	"$(BUF)" format -w proto

## proto-format-check: falha se algum .proto nao estiver formatado
proto-format-check:
	"$(BUF)" format --diff --exit-code proto

## proto-breaking: compat BACKWARD vs. a branch main (TX-1)
proto-breaking:
	"$(BUF)" breaking proto --against '.git#branch=main,subdir=proto'

## proto-build: valida que o modulo compila
proto-build:
	"$(BUF)" build proto

## proto-gen: gera codigo (Go + TS) — requer rede p/ plugins remotos
proto-gen:
	cd proto && "$(BUF)" generate

## proto-gen-check: verifica que gen/ esta em sync com os .proto atuais (requer rede)
# Regenera em tmpdir isolado e compara com gen/ via diff -rq.
# Detecta arquivos novos, modificados e removidos sem tocar o working tree.
# NAO entra no alvo verify (verify e offline/hermetico; proto-gen-check depende de rede).
proto-gen-check:
	@set -eo pipefail; \
	REPO_ROOT="$(CURDIR)"; \
	TMPDIR_GEN=$$(mktemp -d); \
	TMPDIR_PROTO=$$(mktemp -d); \
	trap 'rm -rf "$$TMPDIR_GEN" "$$TMPDIR_PROTO"' EXIT; \
	echo "proto-gen-check: preparando tmpdir ..."; \
	mkdir -p "$$TMPDIR_GEN/go" "$$TMPDIR_GEN/ts"; \
	TEMPLATE="$$TMPDIR_PROTO/buf.gen.yaml"; \
	sed \
	  "s|out: ../gen/go|out: $$TMPDIR_GEN/go|; s|out: ../gen/ts|out: $$TMPDIR_GEN/ts|" \
	  "$$REPO_ROOT/proto/buf.gen.yaml" > "$$TEMPLATE"; \
	echo "proto-gen-check: regenerando ..."; \
	cd "$$REPO_ROOT/proto" && "$(BUF)" generate --template "$$TEMPLATE"; \
	echo "proto-gen-check: comparando go/ ..."; \
	DIFF_GO=$$(diff -rq "$$REPO_ROOT/gen/go" "$$TMPDIR_GEN/go" 2>&1 || true); \
	echo "proto-gen-check: comparando ts/ ..."; \
	DIFF_TS=$$(diff -rq "$$REPO_ROOT/gen/ts" "$$TMPDIR_GEN/ts" 2>&1 || true); \
	if [ -n "$$DIFF_GO" ] || [ -n "$$DIFF_TS" ]; then \
	  echo "FAIL — gen/ diverge do proto/ atual:"; \
	  [ -n "$$DIFF_GO" ] && echo "  Go:" && echo "$$DIFF_GO" | sed 's/^/    /'; \
	  [ -n "$$DIFF_TS" ] && echo "  TS:" && echo "$$DIFF_TS" | sed 's/^/    /'; \
	  echo "Execute 'make proto-gen' e commit os arquivos gerados."; \
	  exit 1; \
	fi; \
	echo "OK — gen/ esta em sync com proto/."

# Numero de guards scripts/ci/no-float-*.sh esperados (TX-2). Atualize este
# numero SEMPRE que adicionar/remover um guard. Existe como SENTINELA
# anti-skip (achado make-no-float-sem-sentinela-de-contagem, 30a onda): sem
# ela, um glob que nao casa nada (script renomeado/movido, sparse-checkout,
# checkout parcial) deixa o loop vazio, 'failed' fica 0 e o alvo sai 0 —
# "verde silencioso" que engana `verify` e o go-live-runbook.
NO_FLOAT_SCRIPTS_EXPECTED := 6

## no-float: roda os guards anti-float (TX-2); FALHA se descobrir menos que
## NO_FLOAT_SCRIPTS_EXPECTED scripts (sentinela anti-skip, nao so "se existirem")
no-float:
	@scripts=(scripts/ci/no-float-*.sh); \
	count=0; \
	for s in "$${scripts[@]}"; do [ -f "$$s" ] && count=$$((count + 1)); done; \
	if [ "$$count" -lt $(NO_FLOAT_SCRIPTS_EXPECTED) ]; then \
	  echo "FAIL — esperava >= $(NO_FLOAT_SCRIPTS_EXPECTED) guards em scripts/ci/no-float-*.sh, encontrei $$count. O glob nao casou algum script (renomeado, movido, sparse-checkout?). Restaure o script ou ajuste NO_FLOAT_SCRIPTS_EXPECTED no Makefile de proposito." >&2; \
	  exit 1; \
	fi; \
	failed=0; \
	for s in "$${scripts[@]}"; do [ -f "$$s" ] && { echo "== $$s"; bash "$$s" || failed=1; }; done; \
	exit $$failed

## make-quoting-check: reprova variavel de path NUA em recipe de make
# O repo vive em path COM ESPACO; `$(BIN)` nu vira duas palavras para o shell.
# A 24a onda consertou a INSTANCIA em make/platform.mk, nao a FORMA — e o alvo
# `tools` daqui ficou nu por mais 7 ondas (181 MB de binarios orfaos fora do
# repo como consequencia medida). Este gate deriva as variaveis-de-path das
# proprias definicoes (sem lista mantida a mao) e varre as recipes.
make-quoting-check:
	@python3 scripts/ci/make-quoting-check.py

## verify: lint + format-check + build + breaking + no-float + quoting (espelha a CI)
verify: proto-lint proto-format-check proto-build proto-breaking no-float make-quoting-check
	@echo "OK — contratos validados (TX-1/TX-2)."

.PHONY: help tools proto-lint proto-format proto-format-check proto-breaking proto-build proto-gen proto-gen-check no-float make-quoting-check verify
