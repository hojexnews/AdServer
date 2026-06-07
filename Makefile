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
	@mkdir -p $(BIN)
	@if ! command -v buf >/dev/null 2>&1; then \
	  echo "baixando buf $(BUF_VER) -> $(BIN)/buf"; \
	  curl -fsSL -o $(BIN)/buf "https://github.com/bufbuild/buf/releases/download/v$(BUF_VER)/buf-$$(uname -s)-$$(uname -m)"; \
	  chmod +x $(BIN)/buf; \
	fi
	@$(BUF) --version

## proto-lint: buf lint (STANDARD + COMMENTS)
proto-lint:
	$(BUF) lint proto

## proto-format: reescreve os .proto no formato canonico (buf format -w)
proto-format:
	$(BUF) format -w proto

## proto-format-check: falha se algum .proto nao estiver formatado
proto-format-check:
	$(BUF) format --diff --exit-code proto

## proto-breaking: compat BACKWARD vs. a branch main (TX-1)
proto-breaking:
	$(BUF) breaking proto --against '.git#branch=main,subdir=proto'

## proto-build: valida que o modulo compila
proto-build:
	$(BUF) build proto

## proto-gen: gera codigo (Go + TS) — requer rede p/ plugins remotos
proto-gen:
	cd proto && $(BUF) generate

## no-float: roda os guards anti-float (TX-2) se os scripts existirem
no-float:
	@failed=0; for s in scripts/ci/no-float-*.sh; do [ -f "$$s" ] && { echo "== $$s"; bash "$$s" || failed=1; }; done; exit $$failed

## verify: lint + format-check + build + breaking + no-float (espelha a CI)
verify: proto-lint proto-format-check proto-build proto-breaking no-float
	@echo "OK — contratos validados (TX-1/TX-2)."

.PHONY: help tools proto-lint proto-format proto-format-check proto-breaking proto-build proto-gen no-float verify
