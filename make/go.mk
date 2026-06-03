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

## go-build: compila todos os pacotes Go do monorepo
go-build:
	$(GO) build $(GOFLAGS) ./...

## go-vet: analise estatica (go vet) em todos os pacotes
go-vet:
	$(GO) vet $(GOFLAGS) ./...

## go-test: executa a suite de testes unitarios e golden tests (sem infra)
go-test:
	$(GO) test $(GOFLAGS) -count=1 -race ./...

## go-test-integration: testes que requerem Redis + Redpanda + MaxMind mmdb
## Marcados com //go:build integration; NAO rodam no CI padrao.
## Use: REDIS_ADDR=localhost:6379 REDPANDA_BROKERS=localhost:9092 make go-test-integration
go-test-integration:
	$(GO) test $(GOFLAGS) -count=1 -race -tags=integration ./...

## go-bench: roda benchmarks no hot path (cascade + rules + money + capping)
go-bench:
	$(GO) test $(GOFLAGS) -run='^$$' -bench=. -benchmem \
		./internal/cascade/... \
		./internal/rules/... \
		./internal/money/... \
		./internal/capping/...

.PHONY: go-build go-vet go-test go-test-integration go-bench
