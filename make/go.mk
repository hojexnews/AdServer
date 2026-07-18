# make/go.mk — Go build targets
#
# Included automatically by the root Makefile via `-include make/*.mk`.
# DO NOT add targets here that belong to other fragments (db.mk, data.mk).
#
# Requires: go >= 1.26.4 in PATH.
#
# TARGETS ADDED IN I2:
#   go-test-integration — SCAFFOLDING RESERVADO (I2): destinado a testes tagged
#                          //go:build integration (Redis + Redpanda + MaxMind .mmdb
#                          vivos). NENHUM teste //go:build integration existe hoje;
#                          o alvo se AUTO-VERIFICA e FALHA ate um ser adicionado
#                          (nao roda no CI padrao; gatilho: cutover G1).

GO      := go
GOFLAGS :=

# Pacotes Go do modulo, EXCLUINDO node_modules/ (web/ e bff/ sao projetos npm;
# algumas deps npm vendoram .go benignos — ex.: flatted — que NAO devem entrar
# no build/test do hot path). `go list` resolve em tempo de parse do make.
#   GOPKGS      — todos os pacotes (para vet/test; test-only e' valido aqui).
#   GOBUILDPKGS — apenas pacotes com fontes NAO-teste (go build falha em
#                 pacotes so-de-teste como tests/parity; .GoFiles os exclui).
GOPKGS      := $(shell $(GO) list ./... 2>/dev/null | grep -v '/node_modules/')
GOBUILDPKGS := $(shell $(GO) list -f '{{if or .GoFiles .CgoFiles}}{{.ImportPath}}{{end}}' ./... 2>/dev/null | grep -v '/node_modules/')

## go-build: compila os pacotes com fontes nao-teste (sem node_modules)
go-build:
	$(GO) build $(GOFLAGS) $(GOBUILDPKGS)

## go-vet: analise estatica (go vet) em todos os pacotes (sem node_modules)
go-vet:
	$(GO) vet $(GOFLAGS) $(GOPKGS)

## go-test: executa a suite de testes unitarios e golden tests (sem infra)
go-test:
	$(GO) test $(GOFLAGS) -count=1 -race $(GOPKGS)

## go-test-integration: testes que requerem Redis + Redpanda + MaxMind mmdb (I2).
## SCAFFOLDING RESERVADO: nenhum teste //go:build integration existe hoje; o guard
## abaixo FALHA (em vez de rodar um no-op verde que finge validar infra real).
## Ao adicionar o 1o teste: REDIS_ADDR=... REDPANDA_BROKERS=... make go-test-integration
go-test-integration:
	@grep -rlq '//go:build integration' ./internal ./services ./tests 2>/dev/null || { \
	  echo "go-test-integration: nenhum teste //go:build integration definido — alvo e scaffolding reservado (I2)."; \
	  echo "  Adicione ao menos um teste tagged antes de usar; rodar hoje seria um no-op mascarado de validacao real."; \
	  exit 1; }
	$(GO) test $(GOFLAGS) -count=1 -race -tags=integration $(GOPKGS)

## go-bench: roda benchmarks no hot path (cascade + rules + money + capping)
go-bench:
	$(GO) test $(GOFLAGS) -run='^$$' -bench=. -benchmem \
		./internal/cascade/... \
		./internal/rules/... \
		./internal/money/... \
		./internal/capping/...

.PHONY: go-build go-vet go-test go-test-integration go-bench
