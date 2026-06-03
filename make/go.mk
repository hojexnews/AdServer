# make/go.mk — Go build targets
#
# Included automatically by the root Makefile via `-include make/*.mk`.
# DO NOT add targets here that belong to other fragments (db.mk, data.mk).
#
# Requires: go >= 1.26.4 in PATH.
#
# TARGETS ADDED IN I2:
#   go-test-integration — runs tests tagged //go:build integration (requires
#                          Redis + Redpanda + MaxMind .mmdb to be up).
#                          NOT run as part of the default `go-test` target.

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

## go-test-integration: testes que requerem Redis + Redpanda + MaxMind mmdb
## Marcados com //go:build integration; NAO rodam no CI padrao.
## Use: REDIS_ADDR=localhost:6379 REDPANDA_BROKERS=localhost:9092 make go-test-integration
go-test-integration:
	$(GO) test $(GOFLAGS) -count=1 -race -tags=integration $(GOPKGS)

## go-bench: roda benchmarks no hot path (cascade + rules + money + capping)
go-bench:
	$(GO) test $(GOFLAGS) -run='^$$' -bench=. -benchmem \
		./internal/cascade/... \
		./internal/rules/... \
		./internal/money/... \
		./internal/capping/...

.PHONY: go-build go-vet go-test go-test-integration go-bench
