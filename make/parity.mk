# make/parity.mk — Gate de cutover: suite de paridade, shadow-traffic e dual-run
#
# Incluído automaticamente pelo root Makefile via `-include make/*.mk`.
# NÃO adicionar aqui targets de build/vet/db — pertencem a outros fragments.
#
# Requires: Go >= 1.26 no PATH.
#           Env vars abaixo para targets que tocam serviços.
#
# ---------------------------------------------------------------------------
# ALVOS EXECUTÁVEIS SEM INFRA (gate mínimo de CI)
# ---------------------------------------------------------------------------
#
#   parity-golden        → suite golden executável (CA-2, CA-4, CA-5, CA-6,
#                          shadow harness, dual-run spec).
#                          NÃO requer Redis, Redpanda, Postgres, MaxMind.
#
# ---------------------------------------------------------------------------
# ALVOS QUE EXIGEM INFRA (documentados, NÃO rodam no CI padrão)
# ---------------------------------------------------------------------------
#
#   parity-shadow        → shadow-traffic contra Revive legado + motor Go em prod.
#                          Requer: REVIVE_ENDPOINT, GO_DECISION_URL, acesso de rede.
#
#   parity-dual-run      → dual-run contábil: billing Go vs. Revive por hour_bucket.
#                          Requer: ICEBERG_CATALOG_URI, DATABASE_URL (Postgres),
#                                  REVIVE_MYSQL_DSN, acesso ao lakehouse.
#
#   parity-cutover-gate  → avalia DivergenceCollector e emite veredicto de cutover.
#                          Requer: artefatos produzidos por parity-shadow e
#                                  parity-dual-run (relatórios JSON em /tmp/parity/).
#
# ---------------------------------------------------------------------------

GO      := go
GOFLAGS :=

## parity-golden: executa a suite golden completa (CA-2, CA-4, CA-5, CA-6,
##                shadow harness, dual-run spec) — sem infra externa.
##                É o gate mínimo de CI antes do cutover.
parity-golden:
	@set -o pipefail; $(GO) test $(GOFLAGS) -count=1 -race -v \
		./tests/parity/... \
		2>&1 | tee /tmp/parity-golden-$(shell date +%Y%m%d-%H%M%S).log
	@echo ""
	@echo "parity-golden: COMPLETO. Verifique o log acima para PASS/FAIL."

## parity-golden-short: mesma suite sem -v (output compacto para CI)
parity-golden-short:
	$(GO) test $(GOFLAGS) -count=1 -race ./tests/parity/...

## parity-shadow: TODO-INFRA — shadow traffic contra Revive + motor Go em produção.
##   Requer: REVIVE_ENDPOINT, GO_DECISION_URL definidos.
##   NÃO roda no CI padrão; executar manualmente em staging.
##   Exemplo: REVIVE_ENDPOINT=http://... GO_DECISION_URL=http://... make parity-shadow
parity-shadow:
	@echo "parity-shadow: TODO-INFRA."
	@echo "  Requer: REVIVE_ENDPOINT, GO_DECISION_URL, acesso de rede ao staging."
	@echo "  Implementar em tests/parity/shadow/shadow_runner_main.go quando"
	@echo "  o ambiente de staging estiver disponível."
	@echo "  Critério de cutover: taxa de divergência < 0.1% por >= 48 horas"
	@echo "  e >= 100.000 decisões observadas (ver tests/parity/shadow/shadow_harness.go)."
	@exit 1

## parity-dual-run: TODO-INFRA — dual-run contábil Go vs. Revive.
##   Requer: ICEBERG_CATALOG_URI, DATABASE_URL, REVIVE_MYSQL_DSN.
##   NÃO roda no CI padrão; executar manualmente antes do cutover.
parity-dual-run:
	@echo "parity-dual-run: TODO-INFRA."
	@echo "  Requer: ICEBERG_CATALOG_URI, DATABASE_URL (Postgres ledger),"
	@echo "           REVIVE_MYSQL_DSN (banco MySQL do Revive)."
	@echo "  Metodologia: ver tests/parity/dual_run/dual_run_spec.go."
	@echo "  Tolerância declarada: 0.5%% de divergência de faturamento (BillingTolerancePct)."
	@echo "  Fonte de verdade: Iceberg (NUNCA streaming/ClickHouse)."
	@echo "  Divergência acima da tolerância BLOQUEIA o cutover."
	@exit 1

## parity-cutover-gate: avalia os artefatos de shadow e dual-run e emite veredicto.
##   Requer: relatórios JSON gerados por parity-shadow e parity-dual-run.
parity-cutover-gate:
	@echo "parity-cutover-gate: TODO-INFRA."
	@echo "  Executar APÓS parity-shadow e parity-dual-run concluírem."
	@echo "  Critérios numéricos declarados em:"
	@echo "    tests/parity/shadow/shadow_harness.go (CutoverCriteria)"
	@echo "    tests/parity/dual_run/dual_run_spec.go (BillingTolerancePct)"
	@exit 1

## parity-status: mostra o status dos alvos de paridade (executável vs. pendente de infra)
parity-status:
	@echo "=== Status da Suite de Paridade ==="
	@echo ""
	@echo "EXECUTAVEL SEM INFRA (green gate mínimo):"
	@echo "  make parity-golden     → go test ./tests/parity/..."
	@echo "  make parity-golden-short"
	@echo ""
	@echo "PENDENTE DE INFRA (TODO-INFRA):"
	@echo "  make parity-shadow     → shadow traffic (Revive + Go em staging)"
	@echo "  make parity-dual-run   → dual-run contábil (Iceberg vs. MySQL Revive)"
	@echo "  make parity-cutover-gate → veredicto final de cutover"
	@echo ""
	@echo "CRITERIOS DE CUTOVER (declarados, exigem ADR para alterar):"
	@echo "  MaxDecisionDivergenceRate : 0.1%%  (tests/parity/shadow/shadow_harness.go)"
	@echo "  MaxBillingDivergencePct   : 0.5%%  (tests/parity/dual_run/dual_run_spec.go)"
	@echo "  MinShadowHours            : 48h"
	@echo "  MinShadowDecisions        : 100.000"
	@echo ""
	@echo "MATRIZ CA-1..CA-9 → status:"
	@echo "  CA-1  (multi-tenancy/ACL)  : parcial — unit tests (cascade TenantIsolation)"
	@echo "  CA-2  (cascata DA-3)       : VERDE — golden executavel (11 casos)"
	@echo "  CA-3  (criativos)          : pendente console/BFF (upload, HTML5, video)"
	@echo "  CA-4  (regras §4.6)        : VERDE — golden executavel (23+ casos)"
	@echo "  CA-5  (capping §4.8)       : VERDE — golden executavel (10+ casos)"
	@echo "  CA-6  (telemetria)         : PARCIAL — unit (dedupe, WAL, token, geo)"
	@echo "  CA-7  (precificacao/moeda) : pendente dual-run contabil"
	@echo "  CA-8  (privacidade)        : parcial — UA coarse, referer sanitizado"
	@echo "  CA-9  (plataforma/ops)     : pendente infra (MaxMind, SMTP, plugins)"

.PHONY: parity-golden parity-golden-short parity-shadow parity-dual-run \
        parity-cutover-gate parity-status
