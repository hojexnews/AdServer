# make/go.mk — Go build targets (decision-engine-engineer, I0)
#
# Included automatically by the root Makefile via `-include make/*.mk`.
# DO NOT add targets here that belong to other fragments (db.mk, data.mk).
#
# Requires: go >= 1.26.4 in PATH.

GO      := go
GOFLAGS :=

## go-build: compila todos os pacotes Go do monorepo
go-build:
	$(GO) build $(GOFLAGS) ./...

## go-vet: analise estatica (go vet) em todos os pacotes
go-vet:
	$(GO) vet $(GOFLAGS) ./...

## go-test: executa a suite de testes unitarios e golden tests
go-test:
	$(GO) test $(GOFLAGS) -count=1 -race ./...

## go-bench: roda benchmarks no hot path (cascade + rules + money)
go-bench:
	$(GO) test $(GOFLAGS) -run='^$$' -bench=. -benchmem \
		./internal/cascade/... \
		./internal/rules/... \
		./internal/money/...

.PHONY: go-build go-vet go-test go-bench
