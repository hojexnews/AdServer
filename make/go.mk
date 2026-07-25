# make/go.mk — Go build targets
#
# Included automatically by the root Makefile via `-include make/*.mk`.
# DO NOT add targets here that belong to other fragments (db.mk, data.mk).
#
# Requires: go >= 1.26.4 in PATH.
#
# TARGETS ADDED IN I2, TORNADO REAL NO BUNDLE A (achado HIGH, money-ledger-guardian):
#   go-test-integration — roda os testes tagged //go:build integration que
#                          exigem infra viva. O ESCOPO DE PACOTES E DERIVADO
#                          (nunca hardcoded): so pacotes que de fato contem um
#                          arquivo `//go:build integration` sao compilados sob
#                          -tags=integration — nunca $(GOPKGS) inteiro. Rodar a
#                          suite TODA sob a tag re-executaria os testes SEM
#                          infra de pacotes nao relacionados sob um ambiente
#                          (env vars, etc.) que eles nao esperam; escopo
#                          estreito por pacote e o default-deny correto aqui.
#                          Hoje: internal/ledger (Postgres 16 real via
#                          DATABASE_URL — ver internal/ledger/posting_integration_test.go
#                          e .github/workflows/db.yml). Redis/Redpanda/MaxMind
#                          .mmdb (I2) seguem SEM teste tagged ainda; quando o
#                          primeiro existir, o pacote entra automaticamente no
#                          escopo derivado (nada aqui precisa mudar). Cada
#                          pacote integration-tagged e responsavel pelo seu
#                          proprio fail-fast de env (t.Fatal, nao t.Skip — um
#                          teste tagged que roda sem a infra que promete e o
#                          "no-op mascarado de validacao real" que este alvo
#                          existe para evitar).

GO      := go
GOFLAGS :=

# golangci-lint para o gate TX-2 forbidigo (metade "lint" de contracts/lint/no-float.md).
# Pinado por versao + SHA256 (mesma filosofia de make/platform.mk). Instalado em .bin/ sob demanda.
GOLANGCI_VER    := 2.12.2
GOLANGCI_SHA256 := 8df580d2670fed8fa984aac0507099af8df275e665215f5c7a2ae3943893a553
# Escopo do gate forbidigo: pacotes PURO-DINHEIRO + o arquivo do money-point eCPM (score.go).
# O reporte e' estreitado por path-except no .golangci.yml; aqui listamos as arvores a analisar.
GO_MONEY_PKGS   := ./internal/money/... ./internal/ledger/... ./internal/ranker/... ./services/payments/... ./internal/chainconnector/... ./internal/configload/...

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

## go-test-integration: testes tagged //go:build integration (infra viva).
## Escopo DERIVADO por pacote (nao $(GOPKGS) inteiro sob a tag — ver comentario
## de topo do arquivo). Hoje: internal/ledger, que exige DATABASE_URL apontando
## para um Postgres 16 com db/ledger/migrations/0001..0004 JA aplicadas (as
## QUATRO — dev-db-setup em make/dev.mk hoje aplica so a 0001; use
## .github/workflows/db.yml como referencia de provisionamento completo, ou
## aplique 0001..0004 manualmente). Falha explicitamente se NENHUM pacote
## tiver teste tagged (evita um no-op verde mascarando validacao real).
go-test-integration:
	@_PKGS="$$(grep -rl '//go:build integration' ./internal ./services ./tests 2>/dev/null \
	            | xargs -r -n1 dirname | LC_ALL=C sort -u | sed 's#$$#/...#')"; \
	 if [ -z "$$_PKGS" ]; then \
	   echo "go-test-integration: nenhum teste //go:build integration definido — alvo e scaffolding reservado."; \
	   echo "  Adicione ao menos um teste tagged antes de usar; rodar hoje seria um no-op mascarado de validacao real."; \
	   exit 1; \
	 fi; \
	 echo "== go-test-integration: pacotes com teste tagged (escopo derivado): $$_PKGS =="; \
	 $(GO) test $(GOFLAGS) -count=1 -race -tags=integration $$_PKGS

## go-lint: gate TX-2 forbidigo (float PROIBIDO em codigo monetario) via golangci-lint.
## Metade "lint" de contracts/lint/no-float.md; a "teste" sao os known-answer (ex.:
## TestScoreCandidateECPM_*) + o backstop grep no-float-*.sh. Config: .golangci.yml.
## Instala golangci-lint em .bin/ (SHA-pinado) se ausente. Paths aspados (repo pode ter espaco).
go-lint:
	@_GCL=""; \
	 if command -v golangci-lint >/dev/null 2>&1; then _GCL=golangci-lint; \
	 elif [ -x "$(BIN)/golangci-lint" ]; then _GCL="$(BIN)/golangci-lint"; \
	 fi; \
	 if [ -z "$$_GCL" ]; then \
	   echo "== go-lint: baixando golangci-lint $(GOLANGCI_VER) -> $(BIN)/golangci-lint"; \
	   mkdir -p "$(BIN)"; \
	   curl -fsSL -o /tmp/golangci.tar.gz "https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_VER)/golangci-lint-$(GOLANGCI_VER)-linux-amd64.tar.gz"; \
	   echo "$(GOLANGCI_SHA256)  /tmp/golangci.tar.gz" | sha256sum -c - || { echo "ERRO: sha256 golangci-lint nao confere"; rm -f /tmp/golangci.tar.gz; exit 1; }; \
	   tar -xzf /tmp/golangci.tar.gz --strip-components=1 -C "$(BIN)" "golangci-lint-$(GOLANGCI_VER)-linux-amd64/golangci-lint"; \
	   chmod +x "$(BIN)/golangci-lint"; \
	   rm -f /tmp/golangci.tar.gz; \
	   _GCL="$(BIN)/golangci-lint"; \
	 fi; \
	 echo "== go-lint: forbidigo TX-2 (float em dinheiro) escopo money-critico =="; \
	 "$$_GCL" run $(GO_MONEY_PKGS)

## go-bench: roda benchmarks no hot path (cascade + rules + money + capping)
go-bench:
	$(GO) test $(GOFLAGS) -run='^$$' -bench=. -benchmem \
		./internal/cascade/... \
		./internal/rules/... \
		./internal/money/... \
		./internal/capping/...

.PHONY: go-build go-vet go-test go-test-integration go-lint go-bench
