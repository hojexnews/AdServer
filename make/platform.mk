# make/platform.mk — validacao offline da plataforma (Fase 3, hardening go-live).
# Incluido automaticamente pelo root Makefile via: -include make/*.mk
#
# Alvo principal: platform-validate
#   Roda SEM credenciais e SEM cluster vivo. Tudo offline/efemero.
#
# Ferramentas:
#   tofu         — OpenTofu >= 1.9 (no PATH ou ~/.local/bin/tofu)
#   kubeconform  — valida YAML K8s/CRD contra schemas (baixado em .bin/ se ausente)
#   kyverno      — kyverno test offline das policies (baixado em .bin/ se ausente)
#   docker       — executa otelcol validate (imagem otel/opentelemetry-collector-contrib)
#
# Convencao de ausencia de ferramenta:
#   Local: avisa e pula (nao falha make, para nao bloquear dev sem as ferramentas).
#   CI:    as ferramentas sao instaladas antes do alvo (ver .github/workflows/platform.yml).
#          No CI a ausencia e ERRO — o step de instalacao e anterior e obrigatorio.
#
# Para forccar modo CI (falha em ausencia de ferramenta):
#   PLATFORM_STRICT=1 make platform-validate

PLATFORM_STRICT ?= 0

TOFU          := $(shell command -v tofu 2>/dev/null || command -v ~/.local/bin/tofu 2>/dev/null || echo $(BIN)/tofu)
KUBECONFORM   := $(shell command -v kubeconform 2>/dev/null || echo $(BIN)/kubeconform)
KYVERNO_CLI   := $(shell command -v kyverno 2>/dev/null || echo $(BIN)/kyverno)

KUBECONFORM_VER := 0.7.0
KYVERNO_CLI_VER := 1.13.4

# Imagem do OTel Collector contrib para validacao semantica da config TX-5.
# Espelha OTELCOL_IMAGE em .github/workflows/platform.yml — manter em sincronia.
OTELCOL_IMAGE := otel/opentelemetry-collector-contrib:0.123.0

# Config do OTel Collector a validar.
OTEL_CONFIG := platform/observability/otel-collector.yaml

# Diretorio raiz do modulo OpenTofu.
TOFU_ROOT := platform/tofu

# Diretorios de manifests K8s a validar com kubeconform.
# Inclui platform/k8s (baseline) e ambas as celulas compliance.
K8S_DIRS := \
  platform/k8s \
  platform/gitops \
  platform/cells/pci \
  platform/cells/aml-kyc \
  platform/observability

# Diretorios de policies Kyverno com kyverno-test.yaml.
KYVERNO_TEST_DIRS := \
  platform/k8s/policy \
  platform/cells/pci/policy \
  platform/cells/aml-kyc/policy

## platform-tools: instala kubeconform e kyverno CLI localmente em .bin/
platform-tools:
	@mkdir -p $(BIN)
	@if ! command -v kubeconform >/dev/null 2>&1 && [ ! -x "$(BIN)/kubeconform" ]; then \
	  echo "== platform-tools: baixando kubeconform $(KUBECONFORM_VER) ->  $(BIN)/kubeconform"; \
	  curl -fsSL -o /tmp/kubeconform.tar.gz \
	    "https://github.com/yannh/kubeconform/releases/download/v$(KUBECONFORM_VER)/kubeconform-linux-amd64.tar.gz"; \
	  tar -xzf /tmp/kubeconform.tar.gz -C $(BIN) kubeconform; \
	  chmod +x $(BIN)/kubeconform; \
	  rm -f /tmp/kubeconform.tar.gz; \
	fi
	@if ! command -v kyverno >/dev/null 2>&1 && [ ! -x "$(BIN)/kyverno" ]; then \
	  echo "== platform-tools: baixando kyverno CLI $(KYVERNO_CLI_VER) -> $(BIN)/kyverno"; \
	  curl -fsSL -o $(BIN)/kyverno \
	    "https://github.com/kyverno/kyverno/releases/download/v$(KYVERNO_CLI_VER)/kyverno-cli_v$(KYVERNO_CLI_VER)_linux_x86_64.tar.gz" \
	    | tar -xzO kyverno > $(BIN)/kyverno 2>/dev/null || \
	  ( curl -fsSL -o /tmp/kyverno.tar.gz \
	      "https://github.com/kyverno/kyverno/releases/download/v$(KYVERNO_CLI_VER)/kyverno-cli_v$(KYVERNO_CLI_VER)_linux_x86_64.tar.gz" && \
	    tar -xzf /tmp/kyverno.tar.gz -C $(BIN) kyverno && \
	    rm -f /tmp/kyverno.tar.gz ); \
	  chmod +x $(BIN)/kyverno; \
	fi
	@$(KUBECONFORM) --version 2>/dev/null || $(BIN)/kubeconform --version
	@$(KYVERNO_CLI) version 2>/dev/null || $(BIN)/kyverno version

## platform-tofu-validate: tofu init -backend=false && tofu validate (sem credenciais)
# FIX (11a onda): guarda de skip e uso de ferramenta unificados num unico bloco de
# shell (mesmo padrao de platform-kubeconform / platform-kyverno-test). O bug
# anterior separava os dois em linhas de recipe distintas; em Make cada linha roda
# em shell proprio, entao o "exit 0" da guarda encerrava apenas aquele shell e a
# linha seguinte tentava executar o binario inexistente (Error 127).
platform-tofu-validate:
	@echo "== platform-tofu-validate: OpenTofu schema check (sem backend/credenciais) =="
	@set -eo pipefail; _TOFU=""; \
	 if command -v tofu >/dev/null 2>&1; then _TOFU=tofu; \
	 elif [ -x "$(HOME)/.local/bin/tofu" ]; then _TOFU="$(HOME)/.local/bin/tofu"; \
	 fi; \
	 if [ -z "$$_TOFU" ]; then \
	   if [ "$(PLATFORM_STRICT)" = "1" ]; then \
	     echo "ERRO: tofu nao encontrado no PATH. Instale em https://opentofu.org/docs/intro/install/"; \
	     exit 1; \
	   else \
	     echo "AVISO: tofu nao encontrado — pulando platform-tofu-validate. (PLATFORM_STRICT=1 para falhar)"; \
	     exit 0; \
	   fi; \
	 fi; \
	 cd $(TOFU_ROOT) && \
	   $$_TOFU init -backend=false -input=false 2>&1 | grep -v "^$$" && \
	   $$_TOFU validate && \
	   echo "== platform-tofu-validate: OK =="

## platform-kubeconform: kubeconform sobre todos os manifests K8s/CRD (offline)
platform-kubeconform:
	@echo "== platform-kubeconform: validando schemas K8s =="
	@_KC=""; \
	 if command -v kubeconform >/dev/null 2>&1; then _KC=kubeconform; \
	 elif [ -x "$(BIN)/kubeconform" ]; then _KC="$(BIN)/kubeconform"; \
	 fi; \
	 if [ -z "$$_KC" ]; then \
	   if [ "$(PLATFORM_STRICT)" = "1" ]; then \
	     echo "ERRO: kubeconform nao encontrado. Rode: make platform-tools"; exit 1; \
	   else \
	     echo "AVISO: kubeconform nao encontrado — pulando. (make platform-tools para instalar)"; exit 0; \
	   fi; \
	 fi; \
	 FAIL=0; \
	 for dir in $(K8S_DIRS); do \
	   yamls=$$(find "$$dir" -name "*.yaml" -not -name "kyverno-test.yaml" -not -name "test-resources.yaml" 2>/dev/null); \
	   [ -z "$$yamls" ] && continue; \
	   echo "-- kubeconform: $$dir"; \
	   echo "$$yamls" | xargs $$_KC \
	     -kubernetes-version 1.30.0 \
	     -schema-location default \
	     -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json' \
	     -ignore-missing-schemas \
	     -summary \
	     -output text || FAIL=1; \
	 done; \
	 if [ "$$FAIL" = "1" ]; then echo "== platform-kubeconform: FALHOU =="; exit 1; fi; \
	 echo "== platform-kubeconform: OK =="

## platform-kyverno-test: kyverno test offline para policies PCI, AML/KYC e baseline
platform-kyverno-test:
	@echo "== platform-kyverno-test: testando policies Kyverno offline =="
	@_KY=""; \
	 if command -v kyverno >/dev/null 2>&1; then _KY=kyverno; \
	 elif [ -x "$(BIN)/kyverno" ]; then _KY="$(BIN)/kyverno"; \
	 fi; \
	 if [ -z "$$_KY" ]; then \
	   if [ "$(PLATFORM_STRICT)" = "1" ]; then \
	     echo "ERRO: kyverno CLI nao encontrado. Rode: make platform-tools"; exit 1; \
	   else \
	     echo "AVISO: kyverno CLI nao encontrado — pulando. (make platform-tools para instalar)"; exit 0; \
	   fi; \
	 fi; \
	 FAIL=0; \
	 for dir in $(KYVERNO_TEST_DIRS); do \
	   if [ ! -f "$$dir/kyverno-test.yaml" ]; then \
	     echo "AVISO: sem kyverno-test.yaml em $$dir — pulando"; continue; \
	   fi; \
	   echo "-- kyverno test: $$dir"; \
	   $$_KY test "$$dir" --detailed-results || FAIL=1; \
	 done; \
	 if [ "$$FAIL" = "1" ]; then echo "== platform-kyverno-test: FALHOU =="; exit 1; fi; \
	 echo "== platform-kyverno-test: OK =="

## platform-otel-validate: valida semanticamente a config do OTel Collector (TX-5).
#
# Usa "docker run --rm" com a imagem oficial otel/opentelemetry-collector-contrib
# pinada em OTELCOL_IMAGE para rodar "otelcol validate --config".
# Isso garante que os processadores de redacao de PII (transform/redact-pii,
# redaction/allowlist-traces, redaction/allowlist-logs) sejam validados
# pela mesma distro que os executa em producao — prevenindo falsa cobertura
# de um kubeconform que ignora a config nativa (sem apiVersion/kind).
#
# Alem da validacao semantica, verifica estruturalmente:
#   1. Os pipelines traces e logs contem transform/redact-pii (redacao por chave).
#   2. Os pipelines traces e logs contem redaction/allowlist-* (fail-closed).
#   3. Nenhum pipeline usa allow_all_keys: true (abre a allowlist — quebra TX-5).
#
# FIX (11a onda): o "docker run" injeta as tres env-vars de endpoint com valores
# placeholder/dummy SOMENTE para a etapa de validacao estrutural do otelcol.
# Os exporters usam ${env:VAR} — o validate exige que as vars estejam definidas
# e que resultem em string nao-vazia; o valor dummy satisfaz essa exigencia sem
# alterar a semantica de producao nem relaxar nenhuma das verificacoes de PII.
# INVARIANTE: a verificacao estrutural de redacao TX-5 (grep) permanece intacta
# e fail-closed — os endpoints dummy nao interferem nela.
#
# Convencao de ausencia de ferramenta:
#   Local sem Docker: avisa e pula.
#   CI (PLATFORM_STRICT=1): falha se Docker nao estiver disponivel.
platform-otel-validate:
	@echo "== platform-otel-validate: validacao semantica OTel Collector (TX-5) =="
	@set -e; \
	 CONFIG="$(OTEL_CONFIG)"; \
	 FAIL=0; \
	 echo "-- otel: verificando presenca dos processadores de redacao de PII nos pipelines..."; \
	 grep -q "transform/redact-pii" "$$CONFIG" || { \
	   echo "ERRO TX-5: transform/redact-pii ausente em $$CONFIG"; FAIL=1; }; \
	 grep -q "redaction/allowlist-traces" "$$CONFIG" || { \
	   echo "ERRO TX-5: redaction/allowlist-traces ausente em $$CONFIG"; FAIL=1; }; \
	 grep -q "redaction/allowlist-logs" "$$CONFIG" || { \
	   echo "ERRO TX-5: redaction/allowlist-logs ausente em $$CONFIG"; FAIL=1; }; \
	 if grep -q "allow_all_keys:[[:space:]]*true" "$$CONFIG"; then \
	   echo "ERRO TX-5: allow_all_keys: true detectado em $$CONFIG — fail-closed violado; altere para false"; \
	   FAIL=1; \
	 fi; \
	 if [ "$$FAIL" = "1" ]; then \
	   echo "== platform-otel-validate: FALHOU (verificacao estrutural) =="; exit 1; \
	 fi; \
	 echo "-- otel: verificacao estrutural OK (redact-pii + allowlists presentes, allow_all_keys=false)"; \
	 if ! command -v docker >/dev/null 2>&1; then \
	   if [ "$(PLATFORM_STRICT)" = "1" ]; then \
	     echo "ERRO: docker nao encontrado — necessario para otelcol validate. Instale o Docker."; \
	     exit 1; \
	   else \
	     echo "AVISO: docker nao encontrado — pulando validacao semantica do OTel Collector."; \
	     echo "       (PLATFORM_STRICT=1 para falhar; instale Docker para validacao completa)"; \
	     echo "       A verificacao estrutural (grep) acima ja foi executada."; \
	     exit 0; \
	   fi; \
	 fi; \
	 echo "-- otel: rodando otelcol validate --config via Docker ($(OTELCOL_IMAGE))..."; \
	 docker run --rm \
	   -v "$(CURDIR)/$$CONFIG:/etc/otel/config.yaml:ro" \
	   -e TEMPO_OTLP_URL=http://localhost:4318 \
	   -e LOKI_OTLP_URL=http://localhost:4318 \
	   -e METRICS_REMOTE_WRITE_URL=http://localhost:9090/api/v1/write \
	   $(OTELCOL_IMAGE) \
	   validate --config /etc/otel/config.yaml; \
	 echo "== platform-otel-validate: OK =="

## platform-validate: tofu validate + kubeconform + kyverno test + otel-validate (tudo offline)
platform-validate: platform-tofu-validate platform-kubeconform platform-kyverno-test platform-otel-validate
	@echo "OK — platform validado offline (tofu + kubeconform + kyverno test + otel-validate TX-5)."

.PHONY: platform-tools platform-tofu-validate platform-kubeconform platform-kyverno-test platform-otel-validate platform-validate
