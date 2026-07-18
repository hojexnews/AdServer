# AdServer (Hojex News)

Ad server modelado a partir do **Revive Adserver 6.x**, com hot path reescrito
em stack poliglota (Go + Postgres + Redis + Redpanda + ClickHouse) e camadas de
IA/copiloto e pagamentos multi-trilho adicionadas **sob medição**, conforme o
design aprovado.

> **Documentos normativos** (leia primeiro):
> - [docs/documentacao-tecnica.md](docs/documentacao-tecnica.md) — entidades, motor de decisão, decisões `DA-1…DA-12`, critérios de aceitação `CA-1…CA-9`.
> - [docs/stack-tecnologico.md](docs/stack-tecnologico.md) — stack, decisões transversais `TX-1…TX-6`, roadmap em fases, riscos.

---

## Estado atual — Fase 0 (Fundações)

A Fase 0 é **bloqueante**: contratos, observabilidade e loop de atribuição
**antes** de qualquer ML. Esta entrega cobre a **camada de contratos**, que é a
porção da Fase 0 construível agora num repositório greenfield.

### ✅ Entregue na Fase 0

| Artefato | Local | Cobre |
|---|---|---|
| Schema Registry Protobuf/Buf | [proto/](proto/) | TX-1 (envelope único, BACKWARD-compat) |
| Envelope universal de eventos | [proto/adserver/common/v1/envelope.proto](proto/adserver/common/v1/envelope.proto) | `tenant_id`/`event_id`/`decision_id`/`model_version` (TX-1) |
| Eventos de telemetria (Request/Impression/Click/Conversion) | [proto/adserver/telemetry/v1/events.proto](proto/adserver/telemetry/v1/events.proto) | §4.7, DA-8 |
| **Decision log + propensão** (loop de atribuição) | [proto/adserver/decision/v1/decision.proto](proto/adserver/decision/v1/decision.proto) · [contrato](contracts/telemetry/propensity-logging.md) | TX-1, §2.3 (OPE/IPS/DR) |
| Tipo `Money` no fio | [proto/adserver/money/v1/money.proto](proto/adserver/money/v1/money.proto) | TX-2 |
| Contrato canônico `Money` (todas as fronteiras) | [contracts/money/money-type.md](contracts/money/money-type.md) | TX-2, DA-10 |
| Asset Registry (schema + DDL + seed) | [contracts/money/asset-registry.md](contracts/money/asset-registry.md) · [seed](contracts/money/asset-registry.seed.csv) | §2.6, DA-10 |
| Política de lint anti-`float` (Go/TS/Python/SQL) | [contracts/lint/no-float.md](contracts/lint/no-float.md) | TX-2 ("float proibido em CI") |
| **CI de schema (buf) + money (no-float)** + Makefile | [.github/workflows/](.github/workflows/) · [Makefile](Makefile) · [scripts/ci/](scripts/ci/) | TX-1, TX-2 (validação em CI) |
| **ADR-0001 — near-real-time resolvido** | [docs/adr/0001-near-real-time-nao-e-requisito-v1.md](docs/adr/0001-near-real-time-nao-e-requisito-v1.md) | DA-7, §2.2, §5 |
| **Plataforma-base como código** (Tofu/Argo CD/Cilium/OTel-PII/OpenBao) | [platform/](platform/) | §2.7, TX-3, **TX-5** |

### ✅ Pendências da Fase 0 — RESOLVIDAS nesta iteração

- **Decisão de produto bloqueante** *(near-real-time 1–5s é requisito?)* →
  **Resolvida em [ADR-0001](docs/adr/0001-near-real-time-nao-e-requisito-v1.md):**
  **não** é requisito de v1/v2. Frescor "ao vivo" vem das MVs do ClickHouse (sem
  Flink); o faturável continua batch horário (DA-7). Flink fica deferido sob
  **gatilho mensurável**. Desbloqueia o eixo streaming da Fase 1.
- **Plataforma base** (EKS + OpenTofu + Argo CD + Cilium + OTel + OpenBao) →
  **autorada como código** em [platform/](platform/) (esqueleto verificado:
  `tofu validate` OK, YAML bem-formado). *Aplicar* ainda requer cloud; o **código**
  está pronto. Inclui o gate de privacidade **OTel com redação de PII** (TX-5).
- **Instrumentação `decision_id`+`model_version`+propensão** → o **contrato/schema**
  (parte Fase 0) está entregue:
  [`decision.proto`](proto/adserver/decision/v1/decision.proto) +
  [contrato de propensão](contracts/telemetry/propensity-logging.md). A
  **instrumentação no motor Go** e nos collectors lg/ck/ct é **Fase 1** (implementa
  este contrato).

## Estado atual — Fase 1 (MVP de paridade) **em andamento**

A Fase 1 foi **aberta e sequenciada** em
[ADR-0002](docs/adr/0002-fase-1-sequenciamento-e-layout.md), que ratifica o
**layout do monorepo** (módulo Go único `github.com/hojex/adserver`), resolve as
perguntas em aberto bloqueantes (BFF Node/TS+tRPC, budget p99 ≤25 ms hot path puro,
capping eventual+fail-safe, atribuição last-click 7d, premissa de volume) e define
**5 incrementos** (I0…I4). Nada de cutover antes do gate de paridade
(golden + shadow + dual-run) dentro da tolerância.

### ✅ Entregue na Fase 1 (código-completo; cutover pende de infra)

| Inc | Artefato | Local | Cobre |
|---|---|---|---|
| **I0** | Motor de decisão Go — cascata DA-3, regras §4.6 (+anti-contradição), snapshot, geo, helpers Money | [internal/](internal/) · [services/decision/](services/decision/) | CA-2, CA-4 |
| **I1** | Ledger double-entry + config §4.1 + Asset Registry (SQL, RLS por tenant) | [db/](db/) | CA-1 (RLS), CA-7 |
| **I2** | Capping Redis+fail-safe DA-6, telemetria WAL+dedupe, collector lg/ck/ct + asyncjs/pixel/302/VAST 4.x | [internal/capping/](internal/capping/) · [internal/telemetry/](internal/telemetry/) · [services/collector/](services/collector/) | CA-3, CA-5, CA-6, CA-8 |
| **I3** | Pipeline Redpanda→ClickHouse(`StatsHourly` + "ao vivo")→Iceberg; dedupe por `event_id`; billing batch | [data/](data/) | CA-6, CA-7 |
| **I4** | Console Next.js + BFF tRPC (fronteira de ACL, vínculo N:N, dashboards ≤1h vs ao vivo, anti-contradição) | [web/console/](web/console/) · [bff/](bff/) | CA-1 |
| **Gate** | Golden tests CA-mapeados (62 casos: CA2 11 / CA3 7 / CA4 23 / CA5 11 / CA6 10) + harness shadow/dual-run + tolerâncias | [tests/parity/](tests/parity/) | §5 (cutover) |
| **I5** | Integração local: loader Postgres→snapshot (decision serve config real), produtor↔ClickHouse (wire JSON p/ dev), seed demo, docker-compose, smoke E2E | [internal/configload/](internal/configload/) · [deploy/local/](deploy/local/) · [db/seed/](db/seed/) | wiring I1↔I0; CA-1 (loader BYPASSRLS) |

**Gates verdes:** `security-reviewer` + `privacy-compliance-auditor` sem CRITICAL/HIGH
abertos (1 CRITICAL + 4 HIGH + 4 MEDIUM remediados — token HMAC no `/ck`, tenant
derivado server-side, VAST sem injeção, UA reduzido a classe, capping confinado,
salt fail-closed, row-policy ClickHouse robusta, SSRF `/vast`, allowlist de logs OTel).
`make verify` (TX-1/TX-2) + `go test` (incl. `tests/parity`) + typecheck/build de
web/bff **verdes**. CI: [buf](.github/workflows/buf.yml) · [no-float](.github/workflows/no-float.yml) · [go](.github/workflows/go.yml).

### 🧪 Rodar localmente (I5 — sem cloud)

O incremento **I5** ([deploy/local/](deploy/local/), [internal/configload/](internal/configload/))
transforma 3 dos pendentes de infra em artefatos **rodáveis localmente**:

```bash
# Caminho A — Postgres local (sem Docker), VERIFICADO:
make dev-db-setup      # cria adserver_dev, aplica migrations + roles + seed demo
make dev-it            # teste de integração: loader Postgres→snapshot (papel BYPASSRLS)
make dev-decision-run  # sobe o decision; em outro shell:
make dev-smoke         # E2E: BR→CONTRACT, US→REMNANT (regra de geo), zona desconhecida→BLANK

# Caminho B — stack completo via Docker Compose:
make dev-up            # core: postgres + redis + decision + collector
make dev-up-streaming  # + redpanda + clickhouse (produtor emite JSONEachRow → ClickHouse)
make dev-validate      # valida compose/scripts sem Docker
```

O **loader de config** (`internal/configload`) fecha o wiring I1↔I0: o decision
service agora monta o snapshot a partir de `db/config` (Postgres) e **serve
anúncios reais** (antes era sempre BLANK com `EmptySnapshot`). O snapshot é
global cross-tenant via papel **`adserver_loader` (BYPASSRLS)**; CA-1 é imposto
na cascata (zona→tenant), não na leitura. O **produtor↔ClickHouse** ganhou
`TELEMETRY_WIRE_FORMAT=json` (`internal/telemetry/wire.go`) cujos nomes de campo
casam exatamente com as colunas da kafka-engine (incl. `user_agent` = classe
coarse → `user_agent_class` na MV); Protobuf segue como padrão de produção.

### ⏭️ Pendente de ambiente (não-código) para fechar a Fase 1

Cutover só após **infra real**: aplicar [platform/](platform/) em cloud; rodar o
stack [deploy/local/](deploy/local/) (ou equivalente gerenciado) com
Postgres/Redis/Redpanda/ClickHouse; MaxMind `.mmdb`; e rodar **shadow-traffic +
dual-run contábil** contra o Revive legado dentro da tolerância
(`parity-shadow`/`parity-dual-run`). CA-3/CA-9 operacionais (upload de criativo,
MaxMind auto-update, Mailer/SMTP) dependem desse ambiente. Ver matriz
CA-1…CA-9→status em [tests/parity/](tests/parity/).

---

## Estado atual — Fase 2 (otimização por ML + copiloto) **em andamento**

A Fase 2 foi **aberta e sequenciada** em
[ADR-0003](docs/adr/0003-fase-2-sequenciamento-ml-copiloto.md), que trava o ponto
de extensão do ML na cascata (re-ranker **dentro** do estrato, fail-open por
padrão — DA-3 segue autoridade final), o layout (`internal/ranker` + sidecar CPU
via Unix socket; treino em `ml/`; copiloto em `services/copilot` atrás do BFF),
resolve a pergunta aberta remanescente (§6.5 identidade cookieless: first-party/edge
efêmero, sem PII central, fail-safe DA-6 mantido) e define **7 incrementos** (J0…J6).
Nada de promover modelo sem **prova de uplift A/B + kill-switch**; HITL obrigatório
em toda escrita do copiloto.

### ✅ Entregue na Fase 2 (código-completo; serving real pende de infra/modelo)

| Inc | Artefato | Local | Cobre |
|---|---|---|---|
| **J0** | Instrumentação de propensão no hot path (`propensity`/`exploration_policy`/`candidates[]`/`ml_fail_open`); `decision_id`+`model_version` ponta-a-ponta | [services/decision/](services/decision/) · [internal/cascade/](internal/cascade/) · [services/collector/](services/collector/) | Loop de atribuição (TX-1); pré-requisito de OPE; golden da Fase 1 **intactos** |
| **—** | Spec única de featurização (anti-skew Go↔Python) + teste de paridade por fixtures gold | [ml/features/](ml/features/) | função única (TX-4/TX-5); 23 features PII-free, `feature_spec_version` |
| **J1** | Re-ranker `internal/ranker` (featurização Go, IPC UDS, **timeout duro + fail-open**) + `ranker-sidecar` (ONNX, modelo dummy) atrás de `RANKER_ENABLED` (off) | [internal/ranker/](internal/ranker/) · [services/ranker-sidecar/](services/ranker-sidecar/) | TX-4; DA-3 (re-rank só no estrato); eCPM=pCTR×bid em minor-units (TX-2) |
| **J2** | Treino pCTR LightGBM→ONNX + calibração isotônica (ECE) + MLflow registry | [ml/training/](ml/training/) · [ml/calibration/](ml/calibration/) · [ml/registry/](ml/registry/) | anti-skew; treino lê Iceberg (dados reais pendem de infra) |
| **J3** | OPE **IPS/SNIPS/DR** sobre propensão logada (filtra `ml_fail_open`, checa positividade/ESS) + **shadow** do ranker (loga decisão-sombra, não serve; `SHADOW_ENABLED` off) + bandit preparado (não exposto) | [ml/ope/](ml/ope/) · [internal/ranker/shadow.go](internal/ranker/shadow.go) · [services/decision/](services/decision/) | OPE honesto (overlap/positividade); shadow parity-safe (servido ≡ cascata pura) |
| **J4** | A/B determinístico por zona/tenant (FNV-1a) + **guarda de receita** (eCPM minor-units) + **kill-switch** fail-safe + **promoção** de `model_version` recusada por código sem prova de uplift + bandit exposto (ε-greedy/Thompson, `propensity<1.0` fecha o loop do OPE) | [internal/ranker/ab.go](internal/ranker/ab.go) · [internal/ranker/guard.go](internal/ranker/guard.go) · [ml/registry/promote_model.py](ml/registry/promote_model.py) | nada promovido sem uplift+kill-switch; DA-3; control ≡ cascata pura; TX-2 |
| **J5** | Copiloto LangGraph (HITL obrigatório, ferramentas tipadas server-side, roteamento Haiku/Sonnet/Opus, Haiku-as-judge fail-closed) + RAG pgvector RLS + C2PA; rota BFF + UI (SSE, diff HITL, builder anti-contradição, WCAG 2.2 AA) | [services/copilot/](services/copilot/) · [db/vector/](db/vector/) · [bff/](bff/) · [web/console/](web/console/) | TX-3 (isolamento + HITL); TX-5 (sem PII); EU AI Act Art. 50 |
| **J6** | Pacing **proporcional** (DA-4) + forecast leve sobre StatsHourly; **fraude/IVT** GBDT supervisionado (TX-6) marcado **antes** do StatsHourly/faturamento + reconciliação **batch contra Iceberg** | [ml/pacing/](ml/pacing/) · [ml/fraud/](ml/fraud/) · [data/fraud/](data/fraud/) · [data/clickhouse/migrations/007_ivt_scoring.sql](data/clickhouse/migrations/007_ivt_scoring.sql) | DA-4 (proporcional, não RL/MPC); TX-6 (fora do hot path, fatura só válido); RLS por tenant |

**Gates verdes (re-auditados):** `security-reviewer` **PASSA** (2 CRITICAL + 4 HIGH
remediados — IDOR cross-tenant no HITL, XSS do ad tag via iframe sandbox, HMAC
interno fail-closed no boot+runtime, `set_config` parametrizado + schema RLS, judge
fail-closed, SSE com tenant server-side + denylist de headers, CSRF no BFF);
`privacy-compliance-auditor` **APROVADO**; `parity-golden-test-guardian` **PASSA**
(golden da Fase 1 intactos, ordem stub-idêntica à cascata, DA-3 confinado, paridade
Go↔Python); `money-ledger-guardian` **PASSA** (eCPM=pCTR×bid em minor-units, sem
float em dinheiro). `make verify` + `go test ./...` + pytest do copiloto +
typecheck/build de web/bff **verdes**.

**Re-gate J3/J4/J6 (todos verdes):** `parity-golden-test-guardian` **PASS**
(control e shadow ≡ cascata pura bit-a-bit; DA-3 sob treatment — re-rank só dentro
do estrato; fail-open ≡ cascata; filtro IVT não corrompe double-entry/DA-10);
`security-reviewer` **PASS** (RLS fail-closed em `raw_ivt_score`, sem SQLi no job de
ingestão, kill-switch e A/B só server-side); `privacy-compliance-auditor`
**APROVADO** (shadow/OPE/IVT/pacing PII-free; redação OTel); `money-ledger-guardian`
**PASS** (guarda de receita/billing em minor-units; DA-10 intacto). Achados
remediados antes do merge: SQLi por f-string no pacing, footgun shadow+AB (WARN de
runtime), validação de `model_name` no filtro MLflow, cobertura de isolamento de
tenant para `raw_ivt_score`. `make verify` + `go test ./...` (incl. `tests/parity` +
`tests/parity/shadow`) + **112 pytest** (ope/pacing/fraud/promote/data) **verdes**.

### ⏭️ Pendente para fechar a Fase 2

**Todos os incrementos de código J0…J6 estão completos.** O que resta é
**ambiente/modelo:** ONNX Runtime nativo no sidecar (hoje stub) + promoção do modelo
real (pCTR/IVT); dados reais no Iceberg para treinar e rodar **OPE/A-B com tráfego
real** (a promoção é recusada por código sem prova de uplift); `ANTHROPIC_API_KEY` +
pgvector + Langfuse self-hosted para o copiloto vivo; RLS executável do RAG + teste
de isolamento com Postgres real; `SESSION_SECRET`/OpenBao para o fail-closed do
middleware Next; ClickHouse real para o teste de isolamento de tenant de
`raw_ivt_score`. Depois → **Fase 3** (deep ranking sob uplift A/B + cripto/AEV/BND).

---

## Estado atual — Fase 3 (IA avançada + cripto/tokens) **em andamento**

A Fase 3 foi **aberta e sequenciada** em
[ADR-0004](docs/adr/0004-fase-3-sequenciamento-ia-avancada-cripto.md), que trava o
deep ranking **atrás do mesmo ponto de extensão** do GBDT (re-ranker dentro do
estrato, fail-open — DA-3 segue autoridade final), define a **interface
`ChainConnector` única** e as **células PCI / AML-KYC** para os trilhos de
pagamento **fora do hot path**, resolve as **10 perguntas abertas de AEV/BND**
(§3) com defaults recomendados + gatilhos de reabertura (token **ERC-20/EVM**,
custódia **Safe → Fireblocks sob AUM**, ramp **USDC**, preço **administrado/
governado**, **Sumsub+Chainalysis+Travel Rule**), e impõe a **regra de ouro**:
deep/Triton/GPU **só sob prova de uplift A/B**, Fireblocks só sob AUM, TigerBeetle
só sob gargalo provado. Define **9 incrementos** (K0…K8). Nada de promover deep sem
**uplift A/B + kill-switch**; AEV/BND seguem `enabled=false` até a spec definir `scale`.

### ✅ Entregue na Fase 3 — 1ª onda (K0 ∥ K1 ∥ K2; gates verdes)

A primeira onda cobre os incrementos sem dependência: as **fundações cripto** e os
dois eixos de **IA** que reusam o arcabouço da Fase 2. K3…K7 (ledger cripto + trilhos
fiat/cripto + compliance + UI) dependem de K0; **K8** (promoção do deep) é gated por
**tráfego real**.

| Inc | Artefato | Local | Cobre |
|---|---|---|---|
| **K0** | Interface `ChainConnector` única (impl EVM stub) + esqueleto `services/payments` (default-off) + proto de pagamentos (BACKWARD-compat) + **células PCI/AML-KYC** (Cilium deny-all); valida AEV/BND no Asset Registry (`enabled=false`, `scale=NULL`, CHECKs) | [internal/chainconnector/](internal/chainconnector/) · [services/payments/](services/payments/) · [proto/adserver/payments/v1/](proto/adserver/payments/v1/) · [platform/cells/](platform/cells/) | TX-1 (buf BACKWARD); TX-2/DA-10 (Money, sem float, sem câmbio implícito); §2.7 (células); cripto **fora do hot path** |
| **K1** | Scaffolding do **deep ranker two-tower DCN-v2/DLRM** (treino PyTorch→ONNX INT8, alvo Triton/GPU) **atrás de flag, default-off**; fiação mínima no `internal/ranker`/sidecar; reusa `ml/features` (anti-skew) | [ml/deep/](ml/deep/) · [services/ranker-sidecar/](services/ranker-sidecar/) · [internal/ranker/](internal/ranker/) | DA-3 (re-rank só no estrato); TX-4 (timeout duro + fail-open, budget 5–8 ms **não** ampliado); deep-off ≡ cascata pura |
| **K2** | **Fraude não-supervisionada** (Isolation Forest + autoencoder) complementando o GBDT de IVT, treinável sobre sample sintético; marcação combinada (OR) **antes** do StatsHourly/faturamento; migração 008 (RLS fail-closed) | [ml/fraud/](ml/fraud/) · [data/fraud/](data/fraud/) · [data/clickhouse/migrations/008_ivt_unsup_scoring.sql](data/clickhouse/migrations/008_ivt_unsup_scoring.sql) | TX-6 (fora do hot path, marca IVT); RLS por tenant; reconciliação contra Iceberg |

**Gates verdes (1ª onda):** `parity-golden-test-guardian` **PASS** (deep-off ≡ cascata
pura bit-a-bit; golden/shadow/dual-run intactos; DA-3 confinado; TX-4 não ampliado);
`security-reviewer` **APROVADO** (0 CRITICAL/HIGH; RLS fail-closed na 008 idêntica ao
padrão 006/007; sem SQLi no job; `services/payments` default-off sem segredo; Cilium
deny-all correto); `privacy-compliance-auditor` **APROVADO** (0 PII; K2 PII-free reusa
features da Fase 2; K0 confina KYC fora do ledger/telemetria; OTel allowlist fail-closed);
`money-ledger-guardian` **PASS** (proto/ChainConnector em `Money`/minor-units, sem float;
DA-10 sem câmbio implícito; AEV/BND disabled com CHECKs). **2 LOW remediados:** no-float-go
passou a varrer `internal/chainconnector`; teste de payout negativo. `make verify` +
`go test ./...` (incl. `tests/parity`) + **89 pytest** (deep + fraude não-superv. + regressão Fase 2) **verdes**.

### ✅ Entregue na Fase 3 — 2ª onda (K3 → K4 → K5 → {K6, K7}; gates verdes)

A segunda onda entrega o **eixo cripto/pagamentos** dependente de K0, na ordem de
dependências. Todo o trilho vive **fora do hot path**; o ledger double-entry da Fase 1
recebe cripto **sem migração de schema** (AEV/BND seguem `enabled=false` até a spec de
`scale`); PII/KYC fica isolada na **célula AML/KYC**; a UI consome **só status** (cripto
fora do cliente).

| Inc | Artefato | Local | Cobre |
|---|---|---|---|
| **K3** | Camada Go de acesso ao **ledger cripto** (par de postings idempotente, depósito `pending`→`posted` na finalidade, saldo derivado, câmbio explícito DA-10, estorno auditável) + reconciliação que **abre exceção e nunca autocorrige** (fonte Iceberg) + migração `0002` (`reconciliation_exceptions`, RLS) | [internal/ledger/](internal/ledger/) · [db/ledger/migrations/](db/ledger/migrations/) | TX-2 (int64↔NUMERIC via `math/big`, sem float); DA-10 (sem câmbio implícito); invariantes contábeis Fase 1; AEV/BND travados (CHECK) |
| **K4** | **Trilho fiat**: Stripe (SAQ-A, cartão nunca no backend) + Asaas/PIX (QR dinâmico, conciliação E2E) + Mercado Pago failover; webhooks com assinatura tempo-constante + anti-replay; **célula PCI** (egress allowlist Stripe, Kyverno exige Vault Agent) | [services/payments/](services/payments/) · [platform/cells/pci/](platform/cells/pci/) | PCI não escapa da célula; par de postings idempotente (K3); SAQ-A; segredos via OpenBao |
| **K5** | **Trilho cripto** Safe multisig + USDC via `ChainConnector` EVM real (JSON-RPC enxuto, **sem go-ethereum**); `uint256→int64` com checagem de overflow; depósito `pending` até N confirmações via webhook do custodiante; reorg → estorno; **Fireblocks deferido sob AUM** | [internal/chainconnector/](internal/chainconnector/) · [services/payments/internal/crypto/](services/payments/internal/crypto/) | finalidade antes de saldo (E.8); custódia Safe (E.5); cripto fora do hot path; sem câmbio implícito |
| **K6** | **Compliance** célula AML/KYC: cofre `db/compliance` (PII/KYC pseudônimo, RLS fail-closed) + Sumsub (KYC/KYB) + **Chainalysis screening fail-closed** (sanção/risco bloqueia depósito/payout) + Travel Rule | [db/compliance/](db/compliance/) · [services/payments/internal/{sumsub,chainalysis,travelrule}/](services/payments/internal/) · [platform/cells/aml-kyc/](platform/cells/aml-kyc/) | TX-3 (PII isolada, pseudônimo); TX-5/DA-11 (ledger/telemetria sem PII); E.10 |
| **K7** | **BFF + UI de pagamentos** (status): router tRPC só-leitura (tenant via ctx, sem IDOR), página self-service de saldo/faturamento; **Money como string DECIMAL** (sem aritmética no cliente); **sem cripto no front** | [bff/src/routers/payments.ts](bff/src/routers/payments.ts) · [web/console/src/app/billing/](web/console/src/app/billing/) | TX-2 (sem aritmética monetária no cliente); TX-3 (BFF injeta tenant, protege segredos); cripto fora do cliente (§2.5/C) |

**Gates verdes (2ª onda):** `money-ledger-guardian` **APROVADO** em K3/K4/K5 (0 float;
`uint256→int64` com overflow-check; scale do Asset Registry, nunca hardcode; DA-10 sem
câmbio implícito); `security-reviewer` **APROVADO** em K4/K7 (PCI não escapa da célula;
webhooks verificados; sem segredo/PII no front; sem IDOR/forja de tenant; sem cripto no
cliente); `privacy-compliance-auditor` **APROVADO** em K6 (screening fail-closed
enforçado; 0 PII em ledger/telemetria/logs; RLS canônica `adserver.tenant_id`
fail-closed; célula isolada). **Achados HIGH/MEDIUM/LOW remediados na mesma janela**
(path de webhook, injeção form-body, SSRF MercadoPago, scale hardcode, truncamento
decimal, RLS do K3 corrigida p/ `adserver.tenant_id`). `go build ./...` + **27 pacotes
Go** + `make verify` (buf TX-1 + no-float TX-2) + BFF/console (typecheck/lint/build) **verdes**.

### ✅ Entregue na Fase 3 — hardening de go-live (sob ADR-0004, sem ADR novo; gates verdes)

Passada de hardening **código-endereçável** sequenciada pelo `tech-lead-architect`:
3 das pré-condições de go-live que **não** exigem infra viva, executadas pelos
engenheiros de camada e gateadas antes do merge.

| Item | Escopo | Arquivos | Invariantes |
|---|---|---|---|
| **Cifra PII** | KMS-envelope (AES-256-GCM, ciphertext versionado `v1$`) das colunas de nome do cofre `compliance` (Travel Rule/KYC); cifra-antes-do-INSERT fail-closed; chave via `PII_ENVELOPE_KEY`/OpenBao | [services/payments/internal/kmsenvelope/](services/payments/internal/kmsenvelope/) | TX-3/DA-11 (PII só no cofre, 0 em log); TX-2 (dinheiro nunca cifrado) |
| **tenant_id no payout** | `TenantID` (UUID-validado, do contexto autenticado) propagado ao screening/Travel Rule; elimina o `TenantID:""`; fail-closed | [internal/chainconnector/connector.go](internal/chainconnector/connector.go) · [safe_webhook.go](services/payments/internal/crypto/safe_webhook.go) | TX-3 (tenant pseudônimo do ctx alimenta a RLS) |
| **Adapter Postgres do BFF** | `PostgresPaymentsAdapter` real: RLS por request via `set_config('adserver.tenant_id',…,true)` em transação + WHERE defense-in-depth; Money string DECIMAL; **+ migração `0003`** de RLS no ledger (`accounts`/`journal_entries`/`postings` + view `security_invoker`) | [bff/src/adapters/postgres-payments.ts](bff/src/adapters/postgres-payments.ts) · [db/ledger/migrations/0003_ledger_rls_up.sql](db/ledger/migrations/0003_ledger_rls_up.sql) | TX-2/DA-10 (sem float/câmbio); TX-3 (RLS efetiva, sem IDOR) |

**Gates verdes (hardening):** `security-reviewer` **APROVADO** após remediar **2 CRITICAL**
— RLS ausente nas tabelas do ledger (migração `0003`) e `SET LOCAL = $1` inválido no
PostgreSQL → `set_config` parametrizado em transação — **+ HIGH-1** (validação de UUID do
`TenantID`) e versionamento do ciphertext; `privacy-compliance-auditor` **APROVADO**;
`money-ledger-guardian` **APROVADO**. `go build ./...` + **go test (35 pacotes)** +
`make verify` (buf TX-1 + no-float TX-2) + BFF (typecheck/lint/build + **51 testes**) **verdes**.

### ✅ Entregue na Fase 3 — 4ª onda: verificação pré-go-live (sob ADR-0004, sem ADR novo; gates verdes)

Onda **código-endereçável** sequenciada pelo `tech-lead-architect`: as pré-condições de
go-live que **provam** (não apenas afirmam) os invariantes já entregues, antes do cutover.
Tudo roda **sem infra viva** (Postgres efêmero/local, stubs, validação offline).

| Item | Escopo | Arquivos | Invariantes |
|---|---|---|---|
| **Teste RLS do ledger** | Isolamento por tenant das tabelas de **dinheiro** (`accounts`/`journal_entries`/`postings` + view `account_balances`): leitura cross-tenant, fail-closed, `security_invoker`, e **`WITH CHECK`** provando rejeição de INSERT/UPDATE com `tenant_id` forjado (Bloco 7). Prova o CRITICAL-2 antes só afirmado | [db/ledger/tests/rls_isolation_test.sql](db/ledger/tests/rls_isolation_test.sql) · [db/ledger/migrations/0003_ledger_rls_up.sql](db/ledger/migrations/0003_ledger_rls_up.sql) | TX-3/DA-11 (fail-closed `adserver.tenant_id`); TX-2 (minor-units, sem float) |
| **Smoke e2e de pagamentos** | Trilho fora do hot path, com stubs: par de postings idempotente, depósito `pending`→finalidade, reconciliação que abre exceção e **nunca autocorrige**, status via BFF (Money string DECIMAL, sem IDOR). Rodado real: **PASS=20** | [deploy/local/smoke-payments.sh](deploy/local/smoke-payments.sh) | TX-2/DA-10 (sem float/câmbio); E.8 (finalidade antes de saldo); cripto fora do cliente |
| **Validação Makefile/CI offline** | `platform-validate` (`tofu validate` + `kubeconform` + `kyverno test` das células PCI/AML) + `db-test-all` (migrações up/down + 4 RLS isolation tests em Postgres efêmero) + `platform-otel-validate` (`otelcol validate` + grep fail-closed da redação TX-5); **SHA256** nos downloads de CLI do CI | [make/platform.mk](make/platform.mk) · [make/db.mk](make/db.mk) · [.github/workflows/platform.yml](.github/workflows/platform.yml) · [.github/workflows/db.yml](.github/workflows/db.yml) | TX-3/TX-5 (isolamento de célula + redação OTel validados, não cosméticos); supply-chain |
| **Runbook de go-live** | Ordem de migrações, segredos por célula (OpenBao/KMS), FQDNs, sequência de smoke, checklist dos 4 gates, rollback, limitações conhecidas | [docs/ops/go-live-runbook.md](docs/ops/go-live-runbook.md) | operacional |

**Gates verdes (4ª onda):** `money-ledger-guardian` **APROVADO** após remediar **3 CRITICAL**
(policies RLS do ledger sem `WITH CHECK` → INSERT/UPDATE cross-tenant não barrado) **+ BLOQUEIO**
do smoke (5 asserções `NUMERIC(40,18)::text` vs literal inteiro falhavam em runtime →
`trunc(...)::bigint::text`, sem float); `security-reviewer` **APROVADO** (smoke sem segredo/rede
externa; CI sem `pull_request_target`, token mínimo, downloads com checksum); `privacy-compliance-auditor`
**APROVADO** (isolamento de célula PCI/AML testado fail/pass, ledger PII-free, redação TX-5 deixou de
ser cosmética); `parity-golden-test-guardian` **PASS** (smoke fora do hot path; golden/dual-run/shadow
verdes; deep default-off, K8 não promovido). **Sweep pré-go-live verde:** `make verify` (buf TX-1 +
no-float TX-2) + `go test ./...` (todos os pacotes) + **BFF 51 testes** + **ml/ + data/ 197 pytest** +
smoke de pagamentos **PASS=20** + RLS do ledger **33 PASS** (2 ciclos up/down/up). *(A coleção do pytest de
`services/copilot` falhava por `pydantic_settings` ausente — diagnosticado e fechado na 5ª onda abaixo: era
defeito de empacotamento, não gap de ambiente.)*

### ✅ Entregue na Fase 3 — 5ª onda: build-backend do copiloto (sob ADR-0004, sem ADR novo; gates verdes)

Micro-onda de **uma tarefa**, triada pelo `tech-lead-architect` com veredito de que era o **único item
código-endereçável genuíno** restante na `main` (todo o resto é infra/spec viva externa — a regra de ouro
proíbe inventar escopo). O que a 4ª onda registrou como "gap de ambiente: `pydantic_settings` ausente" era,
na raiz, um **defeito de empacotamento**: [services/copilot/pyproject.toml:3](services/copilot/pyproject.toml#L3)
declarava `build-backend = "setuptools.backends.legacy:build"` — entry point **inexistente** que fazia
`pip install -e` falhar na resolução PEP 517 **antes** de instalar qualquer dependência (inclusive a
`pydantic-settings` já corretamente declarada). `copilot-llm` corrigiu para o canônico `"setuptools.build_meta"`
(1 linha); prova em venv efêmero: instalação sem erro de backend + **pytest 125 PASS / 0 FAIL** (a coleção
deixou de falhar). +`.gitignore`: `*.egg-info/` e `services/copilot/.venv/`.

**Gates verdes (5ª onda):** `parity-golden-test-guardian` **PASS** (diff de 1 linha, zero toque no hot
path/contratos/dinheiro; sweep canônico verde: `make verify` + `go test ./...` + **BFF 51** + **ml/+data/
197 pytest**); `privacy-compliance-auditor` **APROVADO** (mudança só de build-backend; TX-3/TX-5/DA-11 do
copiloto intocados; nenhum guardrail relaxado). `security-reviewer`/`money-ledger-guardian` **não acionados**
— zero superfície de rede/segredo/dinheiro/contrato (a regra de ouro vale para os gates também). Veredito do
arquiteto após esta onda: **a `main` está genuinamente esgotada em código** — o próximo movimento real é
infra/spec viva externa.

### ✅ Entregue na Fase 3 — 6ª onda: exatidão de go-live + higiene de repo (sob ADR-0004, sem ADR novo; gate verde)

Micro-onda de **dois itens** (paralelos, donos distintos), triada pelo `tech-lead-architect` numa
re-triagem fresca da `main` pós-5ª onda. Re-confirmou o veredito de esgotamento para escopo de
produto/feature, mas encontrou **dois defeitos código-endereçáveis reais** da mesma classe da 5ª onda
(doc/higiene, não invenção de escopo):

- **Drift de caminho no runbook** (`platform-infra-engineer`): [docs/ops/go-live-runbook.md](docs/ops/go-live-runbook.md)
  §2.5 e §3.1 apontavam 2× para `services/compliance/internal/pii/envelope.go` — caminho **inexistente** —
  exatamente no passo mais crítico do cutover (injeção da chave real `PII_ENVELOPE_KEY`). Corrigido para o
  módulo real [services/payments/internal/kmsenvelope/kmsenvelope.go](services/payments/internal/kmsenvelope/kmsenvelope.go)
  (cifra `v1$` versionada AES-256-GCM, fail-closed, `amount_minor_units` nunca cifrado), usado por
  `travelrule.go`/`sumsub.go` antes do INSERT no cofre de compliance.
- **`.pyc` rastreado** (`data-platform-engineer`): `data/iceberg/jobs/__pycache__/iceberg_sink_job.cpython-312.pyc`
  estava em HEAD violando o próprio `.gitignore` (linhas 17–18). `git rm --cached` (artefato regenerável).

**Gate verde (6ª onda):** `privacy-compliance-auditor` **APROVADO** — conserto puro de ponteiro; nenhum
controle (DA-11 isolamento de instância, TX-3 PII cifrada/pseudônima, TX-5 redação OTel, KMS/HSM real,
allowlist fail-closed) removido ou afrouxado, 0 PII nova. `parity`/`money`/`security` **não acionados** —
zero superfície de hot path/dinheiro/rede/contrato. Veredito do arquiteto após esta onda: estes dois itens
eram a **última poeira** do mesmo tipo da 5ª onda; a `main` permanece **genuinamente esgotada em código** e
o próximo movimento real é exclusivamente **infra/spec viva externa**.

### ✅ Entregue na Fase 3 — 7ª onda: swap `spaolacci`→`twmb/murmur3` (gate `-race` vermelho; sob ADR-0004, sem ADR novo; gate verde)

Defeito real identificado pelo `tech-lead-architect` na re-triagem: `go test -count=1 -race ./internal/ranker/...`
falhava com `fatal error: checkptr: pointer arithmetic result points to invalid allocation` em
`spaolacci/murmur3.Sum32WithSeed` — lib não mantida desde 2018, com aritmética `unsafe` incompatível com
o `checkptr` do Go 1.26. Sem `-race` passava; o defeito era invisível fora do CI canônico.

Fix: `github.com/spaolacci/murmur3 v1.1.0` → `github.com/twmb/murmur3 v1.1.8` em `internal/ranker/featurize.go`
e `go.mod`. A nova lib usa `bits.RotateLeft32` (sem `unsafe`), implementa o mesmo MurmurHash3 x86_32 canônico
e expõe `SeedSum32(seed, data)` — a chamada foi atualizada de `Sum32WithSeed(data, seed)` para `SeedSum32(seed, data)`;
a lógica de `featureHash` e os seeds não foram tocados. A marca `// indirect` incorreta no `go.mod` foi corrigida
pelo `go mod tidy` (import é direto em produção).

**Gate verde (7ª onda):** `go build ./...` + `go vet ./...` + `go test -count=1 -race ./internal/ranker/...` (ok) +
`go test -count=1 -race ./...` (27 pacotes ok, sem checkptr crash) + `make parity-golden-short` (ok) +
`TestParityFromFixtures` (5/5 casos PASS) — identidade byte-a-byte do hash Go contra o oráculo Python (`mmh3`)
confirmada: os índices de feature `geo_country_hash`, `geo_city_hash` e `device_class_hash` produzem os mesmos
valores antes e após o swap, provado pelos fixtures em `ml/features/testdata/parity_cases.json`.

### ✅ Entregue na Fase 3 — 8ª onda: contrato de paridade sincronizado + higiene de teste do copiloto (sob ADR-0004, sem ADR novo; gates verdes)

Micro-onda de **dois itens** (paralelos, donos distintos), triada pelo `tech-lead-architect` numa
re-triagem fresca da `main` pós-7ª onda. Sweep de saúde **todo verde com saída real verificada** (Go `-race`
27 pacotes ok, `buf breaking` BACKWARD ok, `ml/`+`data/` **197 pytest**, copiloto **125 pytest**, BFF **51**);
veredito de esgotamento de **feature** reconfirmado — e, como nas ondas 5/6/7, a re-triagem achou **dois
defeitos código-endereçáveis reais** da mesma classe (drift/higiene, não invenção de escopo):

- **Drift do contrato de paridade** (`ml-optimization-engineer`): a 7ª onda trocou a implementação de hash para
  `twmb/murmur3`, mas o **contrato canônico de paridade cross-language** ainda documentava a lib morta
  `spaolacci/murmur3` em 4 locais — a spec que **prova** a identidade byte-a-byte do hash Go↔Python mentia
  sobre qual lib é canônica. Sincronizado em [ml/features/go/parity_contract.go](ml/features/go/parity_contract.go)
  (bloco "HASH CANONICO"), [ml/features/spec/feature_spec.yaml](ml/features/spec/feature_spec.yaml) (`hash_seed_note`)
  e [ml/features/README.md](ml/features/README.md) (incl. a instrução acionável "adicionar ao `go.mod`", que
  apontava para a lib morta). Registros **históricos** do swap em `README.md` (raiz) e
  [docs/ops/go-live-runbook.md](docs/ops/go-live-runbook.md) foram preservados (são changelog, não spec).
- **Higiene de teste do copiloto** (`copilot-llm-engineer`): `@pytest.mark.asyncio` em **nível de classe** em
  `services/copilot/tests/test_security.py` cascateava para métodos síncronos, gerando 5 `PytestWarning`
  (que o pytest-asyncio promove a erro duro em major futura). Sob `asyncio_mode = "auto"` o mark de classe é
  redundante para async e errado para síncronos; removido — métodos async seguem autodetectados.

**Gates verdes (8ª onda):** `parity-golden-test-guardian` **APROVADO** (revisão adversarial obrigatória do T1) —
identidade byte-a-byte **provada** nos 11 pares de fixture (`geo_country_hash`/`geo_city_hash`/`device_class_hash`,
Go `twmb/murmur3.SeedSum32` ≡ Python `mmh3`), assinatura documentada bate com `featurize.go:328`, **zero** ref
órfã de `spaolacci` em spec/contrato (só as 3 históricas), escopo doc-only; `go test -count=1 -race ./...`
(27 pacotes ok) + `make verify` verde. Copiloto: **125 passed, 0 warnings** (5 `PytestWarning` eliminados).
`privacy`/`money`/`security` **não acionados** — zero superfície de PII/dinheiro/rede/contrato de evento
(a regra de ouro vale para os gates). Veredito do arquiteto: a `main` permanece **genuinamente esgotada em
código** para escopo de produto; o próximo movimento real segue sendo exclusivamente **infra/spec viva externa**.

### ✅ Entregue na Fase 3 — 9ª onda: gate canônico no runbook + cabeçalho do contrato de paridade (sob ADR-0004, sem ADR novo; gate verde)

Micro-onda de **dois itens** (paralelos, donos distintos, superfícies disjuntas), triada pelo
`tech-lead-architect` numa re-triagem fresca da `main` pós-8ª onda. Sweep de saúde **todo verde com
saída real verificada** (Go `build`/`vet`/`test -race` todos os pacotes ok, `make verify` = buf TX-1 +
no-float TX-2 verde, `ml/` **176 pytest**, `data/` **21 pytest**, copiloto **125 pytest 0 warn**, BFF **51**,
web typecheck ok); veredito de esgotamento de **feature** reconfirmado — e, como nas ondas 5/6/7/8, a
re-triagem achou **dois drifts doc-only reais** da mesma classe ("a spec não mente"), inócuos em runtime:

- **Runbook prescrevia gate Go não-canônico** (`platform-infra-engineer`): o
  [runbook de go-live](docs/ops/go-live-runbook.md) (rodado pelo operador numa máquina **com** `node_modules`)
  mandava `go test ./...` cru (Passo 4) e rotulava `go test -count=1 -race ./...` como "gate canônico"
  (checklist) — mas o gate canônico real são os alvos `make go-build`/`go-vet`/`go-test`, que **filtram
  `node_modules/`** por design (`make/go.mk`, `GOPKGS`/`GOBUILDPKGS` com `grep -v`) porque deps npm vendoram
  `.go` benignos. CI mascarava (sem `npm install`); o runbook do operador, não. Alinhado para
  `make go-build && make go-vet && make go-test` + `make parity-golden-short`, com a justificativa
  node_modules-safe que o próprio `go.mk` documenta.
- **Cabeçalho do contrato de paridade mentia sobre a localização do pacote** (`ml-optimization-engineer`):
  [ml/features/go/parity_contract.go](ml/features/go/parity_contract.go) afirmava "NÃO é pacote Go importável /
  fica fora do go.mod" — falso: `go list ./ml/features/go` → `github.com/hojex/adserver/ml/features/go`, e ele
  compila em `make go-build` (o `go.mod` raiz único absorve tudo sob a raiz). Comentário stale de quando `ml/`
  era pensado fora do módulo. Corrigido (cabeçalho + comentário do pacote) para a verdade: é pacote válido no
  módulo único, papel **documental/contratual** (spec canônica da featurização anti-skew espelhada Go↔Python),
  **não** importado pelo hot path. Sem `//go:build ignore` — correção doc-only, zero mudança de comportamento de build.

**Gate verde (9ª onda):** `parity-golden-test-guardian` **APROVADO** (revisão adversarial) — escopo de **2 arquivos
doc-only** confirmado (zero toque em runtime/`.proto`/hot path/dinheiro/fixtures); os 4 alvos `make` existem e
filtram `node_modules` (saída real: `go-build` 34 pacotes, `go-test` 22 ok + 13 sem testes com `-race`,
`parity-golden-short` 3 pacotes ok); `ml/features/go` provadamente no módulo e compilando; **paridade byte-a-byte
intacta** nos 11 fixtures de hash. `security`/`privacy`/`money` **não acionados** — zero superfície sensível
(a regra de ouro vale para os gates). Veredito do arquiteto: a `main` permanece **genuinamente esgotada em código**
para escopo de produto; o próximo movimento real segue sendo exclusivamente **infra/spec viva externa**.

### ✅ Entregue na Fase 3 — 10ª onda: Passo 5 do runbook usa gates `make` canônicos (sob ADR-0004, sem ADR novo; gate verde)

Micro-onda de **um item** (um dono, um arquivo), triada pelo `tech-lead-architect` numa re-triagem fresca da
`main` pós-9ª onda. Sweep de saúde **todo verde com saída real verificada** (`make go-build` 38 pacotes,
`make go-vet`, `make go-test` **27 pacotes `-race`**, `make verify` = buf TX-1 + no-float TX-2, `make
parity-golden-short`, `make ml-test`, `make ml-batch-test`, `make data-validate`, copiloto **125 pytest**, BFF
**51**, `make db-lint`, `make platform-tofu-validate`); veredito de esgotamento de **feature** reconfirmado — e,
como nas ondas 5/6/7/8/9, a re-triagem achou **um defeito código-endereçável real** da mesma classe ("a spec não
mente"), resíduo direto da própria 9ª onda:

- **Passo 5 do runbook ficou com comandos crus** (`platform-infra-engineer`): a 9ª onda padronizou o **Passo 4**
  e o checklist do [runbook de go-live](docs/ops/go-live-runbook.md) para alvos `make` node_modules-safe, mas
  deixou o **Passo 5 — Suites BFF/pytest** com comandos crus. `cd bff && npm test` era apenas não-canônico, mas
  **`cd ml && pytest` estava QUEBRADO**: falha com `ModuleNotFoundError: No module named 'ml'` porque, ao fazer
  `cd ml`, o CWD deixa de ser a raiz do repo e o import `from ml....` não resolve — todos os alvos `make` de
  Python injetam `PYTHONPATH=.` na raiz justamente por isso. O passo de smoke pré-cutover, como escrito, **não
  rodava**. Alinhado para `make bff-ci` (BFF) + `make ml-test && make ml-batch-test && make data-validate`
  (ml/+data/) + `PYTHONPATH=. ml/.venv/bin/python -m pytest services/copilot/tests/` (copiloto, que não tem alvo
  `make` próprio), com a justificativa node_modules-safe / `PYTHONPATH=.` explicitada no texto.

**Gate verde (10ª onda):** `parity-golden-test-guardian` **APROVADO** (revisão adversarial) — os 4 alvos `make`
existem e rodam verde com saída real (`bff-ci` **51**, `ml-test` **90**, `ml-batch-test` **39**, `data-validate`
12 invariantes, copiloto **125**); `cd ml && pytest` antigo **provadamente** falhava (`ModuleNotFoundError`);
cobertura **não regrediu**; escopo de **1 arquivo doc-only** (zero toque em runtime/`.proto`/hot path/dinheiro/
fixtures); **paridade byte-a-byte intacta** (`make parity-golden-short` + `make verify` verdes). Achado **não-
bloqueante** registrado: `ml/deep/test_deep.py` e `ml/fraud/test_unsup.py` (K1/K2, 47 testes verdes) não têm alvo
`make` dedicado — lacuna **pré-existente**, candidata a backlog do `decision-engine-engineer`, fora do escopo
desta onda. `security`/`privacy`/`money` **não acionados** — zero superfície sensível. Veredito do arquiteto: a
`main` permanece **genuinamente esgotada em código** para escopo de produto; o próximo movimento real segue sendo
exclusivamente **infra/spec viva externa**.

### ✅ Entregue na Fase 3 — 11ª onda: gates de IaC/OTel que mentiam + malha de gate ML furada (sob ADR-0004, sem ADR novo; gates verdes)

Micro-onda de **três itens** (paralelos, donos distintos, superfícies disjuntas), triada pelo
`tech-lead-architect` numa re-triagem fresca da `main` pós-10ª onda. Sweep de saúde **todo verde com
saída real verificada** (`make go-build` 38 pacotes, `make go-vet`, `make go-test` **27 pacotes `-race`**
sem corrida, `make verify` = buf TX-1 BACKWARD + no-float TX-2 Go/Py/SQL/data, `make parity-golden-short`
3 pacotes ok, `make ml-test` **90** inalterado, `make data-validate` 12 invariantes, copiloto **125 pytest**,
BFF **51**, `make db-lint`); veredito de esgotamento de **feature** reconfirmado — e, como nas ondas
5/6/7/8/9/10, a re-triagem achou **três defeitos código-endereçáveis reais** da mesma classe ("a spec não
mente"): gates de CI que reportavam verde sem verificar de fato. Desta vez os defeitos eram **mais sérios**
que drift doc-only — eram gates que **mascaravam falha**:

- **`platform-tofu-validate` reportava verde com falha de validação** (`platform-infra-engineer`): dois
  defeitos no alvo [make/platform.mk](make/platform.mk) (~86-104). (1) A guarda de skip (`@if … exit 0`)
  ficava num bloco de recipe **separado** do uso da ferramenta; como em Make cada linha roda em shell próprio,
  o `exit 0` encerrava só aquele shell e a linha seguinte morria com `Error 127` quando `tofu` ausente, em vez
  de skip graceful. Fix: guarda+uso unificados num **único bloco encadeado** (mesmo padrão dos irmãos
  `platform-kubeconform`/`platform-kyverno-test`). (2) — **achado HIGH do `security-reviewer` que BLOQUEOU** —
  o pipeline `tofu init … | grep -v "^$$"` **sem `set -o pipefail`** engolia a falha do `init` (exit do
  `grep`=0), imprimindo "OK"/EXIT 0 mesmo com IaC que nem inicializa: furo de invariante de CI sob
  `PLATFORM_STRICT=1`. Fix: `@set -eo pipefail` no início do bloco ([make/platform.mk:88](make/platform.mk)).
- **`platform-otel-validate` não passava o `otelcol validate`** (`platform-infra-engineer`): com Docker
  presente, o `otelcol validate` da distro contrib falhava `at least one endpoint must be specified` porque os
  exporters `otlphttp/tempo`/`otlphttp/loki`/`prometheusremotewrite` usam `endpoint: ${env:TEMPO_OTLP_URL/…}`
  e essas vars não eram definidas — CI verde por **não-acionamento** (workflow só dispara em `platform/**`),
  não por sucesso real. Fix: injeção de endpoints **placeholder** (`http://localhost:4318`,
  `…:9090/api/v1/write`) via `-e` no `docker run` **somente** para o validate, doc espelhada em
  [.github/workflows/platform.yml](.github/workflows/platform.yml) ([make/platform.mk](make/platform.mk)
  ~218-225). O `otel-collector.yaml` **não foi tocado** — a verificação estrutural TX-5 (grep
  `transform/redact-pii` + allowlists `allow_all_keys:false`) roda **antes** do Docker e permanece fail-closed.
- **Suites K1/K2 fora de qualquer alvo `make`** (`ml-optimization-engineer`): `ml/deep/test_deep.py` (K1, 22
  testes) e `ml/fraud/test_unsup.py` (K2, 25 testes) estavam **fora da malha de gate** — exatamente o achado
  não-bloqueante registrado na 10ª onda. Fix: novo alvo `ml-deep-test` em [make/ml.mk](make/ml.mk) (~74),
  **fora** do agregado rápido `ml-test` (custo PyTorch+ONNX ~6s) mas **mandatório pré-K8 no runbook**; novo
  `ml-batch-test-unsup` em [make/ml-batch.mk](make/ml-batch.mk) (~41), **dentro** do agregado `ml-batch-test`
  (perfil numpy/sklearn). Passo 5 e checklist §6 do [runbook](docs/ops/go-live-runbook.md) atualizados para
  listar `make ml-deep-test`.

**Gates verdes (11ª onda):** `security-reviewer` **BLOQUEOU → remediou → APROVADO** no item A — bloqueio
levantado, vetor de mascaramento fechado, provado com binário `tofu` fake em 5 casos (skip graceful EXIT 0,
strict falha EXIT≠0, caminho feliz "OK" EXIT 0, **init falha → EXIT≠0 sem "OK"**, validate falha → EXIT≠0).
`privacy-compliance-auditor` **APROVADO** no item B — provado adversarialmente que a verificação estrutural
TX-5 roda antes do Docker e permanece fail-closed (afrouxar a redação ainda barra o alvo), placeholders não
vazam p/ produção/imagem/git, `make platform-otel-validate` **VERDE EXIT 0 com Docker**.
`parity-golden-test-guardian` **APROVADO** no item C — paridade/golden intacta (`make parity-golden-short`
verde), deep **default-off preservado** (`DEEP_ENABLED=false`, `test_default_model_version_is_not_deep` verde),
zero toque em runtime/`.proto`/hot path/dinheiro/fixtures; números reais: `make ml-deep-test` **22 passed**,
`make ml-batch-test` agora **64** (18 pacing + 21 fraud + 25 unsup, era 39), `make ml-test` **90** inalterado.
Achados **LOW não-bloqueantes** registrados p/ backlog: `| grep -v "^$$"` no `tofu-validate` pode dar
falso-negativo se o `init` emitir só linhas vazias — pré-existente e inalcançável na prática (tofu real sempre
imprime texto), candidato a `| { grep -v "^$$" || true; }` numa próxima onda. `money` **não acionado** — zero
superfície de dinheiro. Veredito do arquiteto: a `main` permanece **genuinamente esgotada em código** para
escopo de produto; o próximo movimento real segue sendo exclusivamente **infra/spec viva externa**.

### ✅ Entregue na Fase 3 — 12ª onda: caminho Docker/streaming bootado + correções de bugs latentes (sob ADR-0004, sem ADR novo; gates verdes)

Onda **larga** (24 commits, donos múltiplos), triada pelo `tech-lead-architect` após **bootar pela 1ª vez
os caminhos que o README admitia nunca terem rodado** ("boote-o antes de confiar nele"). Diferente das ondas
5–11 (drift doc-only), esta achou **defeitos de runtime reais** no caminho Docker/streaming, mais um lote de
endurecimento de CI/contratos/concorrência da mesma classe. Os dois itens registrados como **known-issues**
(capper→BLANK e DDL 004 do ClickHouse) ficam **declarados RESOLVIDOS** aqui.

- **ClickHouse / streaming** (`data-platform-engineer` / `platform-infra-engineer`): as **8 migrations agora
  aplicam limpo em CH 24.8** (verificado). (a) **004** `zero-init` dos `AggregateFunction` —
  `uniqState(toNullable(''))` (Code 70) → `uniqStateIf(event_id, 1=0)` e `sumState(toDecimal256(0,18))` →
  `toDecimal128(0,18)` (bate `Decimal(38,18)`): **destrava as MVs de billing** ([004_stats_hourly.sql](data/clickhouse/migrations/004_stats_hourly.sql)).
  (b) **002** `conversion_value_decimal` sem `pow()`/Float — `multiIf` com divisores inteiros 10^scale (0..18),
  preservando `amount/10^scale` sem float (TX-2) ([002_raw_tables.sql](data/clickhouse/migrations/002_raw_tables.sql)).
  (c) **005** projeta colunas `MATERIALIZED` explicitamente no JOIN (`SELECT *` as excluía em CH 24.x)
  ([005_live_view.sql](data/clickhouse/migrations/005_live_view.sql)). (d) **006** cria `ROLE` antes das `ROW
  POLICY`, admin via `adserver_admin_role` (role-only, sem `CREATE USER` em migration) ([006_access_control.sql](data/clickhouse/migrations/006_access_control.sql));
  **007/008** alinhados ao mesmo role. (e) **ch-init** [10-ddl.sh](deploy/local/clickhouse/10-ddl.sh): strip de
  `;` robusto a `;` dentro de comentário SQL (bug só do strip local; produção usa `--multiquery`).
  `no-float-data-sql.sh` ganhou detector de `pow()` em billing ([scripts/ci/no-float-data-sql.sh](scripts/ci/no-float-data-sql.sh)).
  → **Known-issue "DDL 004 do ClickHouse" RESOLVIDO.**
- **Capping** (`decision-engine-engineer`): (a) **fast-path uncapped antes do check de userID** — o `Capper`
  real retornava `false` p/ qualquer campanha com `userID` vazio (fail-safe DA-6) **antes** do fast-path "sem
  cap", então o decision em Docker servia **sempre BLANK** cookieless; `Allowed()` agora resolve `effectiveCaps`
  → fast-path uncapped (serve anônimo) → só então o check de userID (só p/ campanhas capeadas), DA-6/CA-5
  ([capping.go](internal/capping/capping.go)); golden CA5-005b cobre ([ca5_capping.json](tests/parity/golden/ca5_capping.json)).
  (b) **TTL limitado** no contador `campaign_total` — sem chave permanente por usuário (privacidade DA-6/TX-5).
  → **Known-issue "capper→BLANK" RESOLVIDO.**
- **Ledger** (`money-ledger-guardian` / `decision-engine-engineer`): trigger de balanço dispara também em
  **posting unilateral** — backstop double-entry que rejeita débito/crédito sem par
  ([0001_ledger_schema_up.sql](db/ledger/migrations/0001_ledger_schema_up.sql)).
- **Copiloto** (`copilot-llm-engineer`): (a) **IDOR cross-tenant** em `GET /v1/session/{id}/state` → check de
  posse + **403** ([server.py](services/copilot/app/server.py), cobertura em [test_security.py](services/copilot/tests/test_security.py));
  (b) rota SSE encaminha a **mensagem do usuário no 1º turno** sem **422** vazio ([stream route](web/console/src/app/api/copilot/stream/[sessionId]/route.ts));
  (c) `hitlApprove` do BFF **não vaza corpo da resposta interna** no erro (só `correlation_id`) ([copilot.ts](bff/src/routers/copilot.ts)).
- **BFF** (`bff-platform-engineer`): **scale monetário sem fallback silencioso** — `INNER JOIN asset_registry`
  + erro explícito quando o ativo não tem `scale` (em vez de assumir um default e mentir sobre o dinheiro),
  TX-2/DA-10 ([postgres-payments.ts](bff/src/adapters/postgres-payments.ts)).
- **Ranker** (`ml-optimization-engineer`): (a) **mutex** em `MLRanker.last` (data race com handlers
  concorrentes sob `RANKER_ENABLED`) ([ranker.go](internal/ranker/ranker.go)) e em `BanditRanker` cfg/last
  (+ remoção do comentário "atomically" falso) ([bandit_ranker.go](internal/ranker/bandit_ranker.go));
  (b) **math caseiro → stdlib** — Taylor/Newton próprio divergia da `math` da stdlib p/ `|x|>4.5` no
  `mathExp`; trocado por `math.Exp`/`math.Log`, com teste de fronteira ([bandit.go](internal/ranker/bandit.go), [bandit_math_test.go](internal/ranker/bandit_math_test.go)).
- **CI / contratos** (`platform-infra-engineer` / `schema-contracts-steward`): (a) **proto-gen-check** fecha
  o **gate cego de staleness** do `gen/` (TX-1) — regenera e falha se o commitado divergir ([buf.yml](.github/workflows/buf.yml), [Makefile](Makefile));
  (b) versiona o `gen` **go+ts** de `payments/v1` (artefatos gerados que faltavam, TX-1) ([payments.pb.go](gen/go/adserver/payments/v1/payments.pb.go), [payments_pb.ts](gen/ts/adserver/payments/v1/payments_pb.ts));
  (c) testes **Python ML e `services/copilot`** passam a rodar em CI (gate ausente) ([ml.yml](.github/workflows/ml.yml), [make/copilot.mk](make/copilot.mk));
  (d) gates de teste **propagam exit code** (`pipefail` nos pipes + fim de `;true`/`||true`);
  (e) **platform-tools verifica SHA256** de kubeconform/kyverno (espelha M-1 do CI) ([make/platform.mk](make/platform.mk)).
- **Console** (`web-console-engineer`): **logo Hojex** no header + favicon + apple-icon + OG — origem do nome
  da branch `feat/console-brand-logo`.
- **Paridade** (`ml-optimization-engineer`): `parity_contract` sincroniza **tolerância 1e-6** e a **assinatura
  com o `featurize` real** ([parity_contract.go](ml/features/go/parity_contract.go), [test_parity_cases.py](ml/features/python/test_parity_cases.py)).

**Gates verdes (12ª onda) — números reais re-triados:** `make go-build` + `make go-vet` + `make go-test`
(**28 pacotes**, `-race` clean) + `make verify` (buf TX-1 BACKWARD + no-float TX-2) + `make proto-gen-check` +
`make parity-golden-short` (3 pacotes) + `make ml-test` (**29**) + `make ml-deep-test` (**22**) +
`make ml-batch-test` (**25**) + `make data-validate` (12 invariantes) + `make copilot-test` (**126**) +
`make bff-ci` (**54**) + `make web-ci` + `make db-lint` — **todos verdes**. `parity-golden-test-guardian`
**PASS** (golden/shadow/dual-run intactos; o fix de capping serve uncapped sem alterar a autoridade da cascata
DA-3; deep default-off preservado, K8 não promovido); `money-ledger-guardian` **PASS** (trigger double-entry
reforçado, scale do Asset Registry sem fallback, 0 float); `privacy-compliance-auditor` **APROVADO** (IDOR do
copiloto fechado, TTL do contador de capping sem chave permanente DA-6/TX-5, 0 PII nova); `security-reviewer`
**PASS** (403 cross-tenant, sem vazamento de corpo interno, SHA256 nos downloads de CLI).

**Regra de ouro mantida:** nenhuma tecnologia pesada entrou sem gatilho mensurável (Flink/Triton/GPU/TigerBeetle/
Fireblocks seguem deferidos); nada foi promovido sem **uplift A/B + kill-switch** (deep/K8 e AEV/BND seguem
travados). Veredito do arquiteto após bootar e corrigir o caminho Docker/streaming: a `main`/branch permanece
**genuinamente esgotada em código de produto** — o próximo movimento real é exclusivamente **infra/spec viva
externa**.

### ✅ Entregue na Fase 3 — 13ª onda: gates TX-2/CI que reportavam verde sem verificar (sob ADR-0004, sem ADR novo; gates verdes)

Micro-onda de **três itens** (paralelos, donos disjuntos), triada pelo `tech-lead-architect` numa
re-triagem fresca da `main` pós-12ª onda. Sweep de saúde **todo verde com saída real verificada**
(`make go-build` 38 pacotes, `make go-vet`, `make go-test` **28 pacotes `-race`** sem corrida, `make verify`
= buf TX-1 BACKWARD + no-float TX-2, `make proto-gen-check`, `make parity-golden-short` 3 pacotes, `make
ml-test` **29**, `make ml-deep-test` **22**, `make ml-batch-test`, `make data-validate` 12 invariantes,
`make copilot-test` **126**, `make bff-ci` **54**, `make web-ci`, `make db-lint`; `-race -count=10` em
ranker/capping/telemetry/clicktoken **0 flakes**); veredito de esgotamento de **feature** reconfirmado — e,
como nas ondas 5–11, a re-triagem achou **três defeitos código-endereçáveis reais** da classe "a spec/gate
não mente": gates que reportavam verde sem verificar de fato (o filão mais sério, igual à 11ª onda):

- **Gate canônico TX-2 Python varria ZERO arquivos** (`ml-optimization-engineer`): o
  [no-float-py.sh](scripts/ci/no-float-py.sh) — invocado por `make verify` e por
  [no-float.yml](.github/workflows/no-float.yml) — usava o glob nominal `'*money*/*.py' '*ledger*/*.py'
  '*billing*/*.py' '*payments*/*.py'`, que casava **0 arquivos** e imprimia "ok" sem varrer nada. Mas
  [ml/fraud/train_ivt.py](ml/fraud/train_ivt.py) e [train_unsup.py](ml/fraud/train_unsup.py) **declaram
  "TX-2 (dinheiro em minor-units int64)"** — código financeiro Python fora do glob; e `ml-batch-no-float`
  (único alvo que olhava `ml/fraud`) era **órfão** (fora de todo agregado). Proteção TX-2 **ilusória** (verde
  por não-acionamento). Fix: glob ampliado p/ `ml/fraud/*.py`+`ml/pacing/*.py` (**14 arquivos** varridos) com
  detector **conjuntivo** `line_has_monetary_float()` (nome financeiro `amount|price|cpm|cpc|cpa|bid|budget|
  revenue|cost|minor_units|money|spend|payout|charge` **E** `float()` cast/literal) — não flagra o `float32`
  legítimo de vetor de feature ONNX/LightGBM ([no-float-py.sh](scripts/ci/no-float-py.sh)); `ml-batch-no-float`
  com regex monetária melhorada + plugado como 1ª dependência de `ml-batch-test` ([ml-batch.mk](make/ml-batch.mk)).
- **`ml-calibration-test` mascarava falha com `|| echo OK`** (`ml-optimization-engineer`): o `cmd || echo OK`
  engolia qualquer `exit≠0` ([ml.mk](make/ml.mk) ~47); hoje código morto (sem testes em `ml/calibration/`,
  pytest sai 0), mas no dia em que um teste falhar lá, `ml-test` (que depende deste alvo) reportaria verde.
  Fix: captura o rc — `rc=5` (no-tests) → ok, qualquer outro `≠0` → propaga `exit rc`.
- **`ml.yml` prometia acionamento inexistente** (`platform-infra-engineer`): o comentário dizia que
  `ml-deep`/`ml-training` eram acionáveis via `workflow_dispatch`/`scheduled`, mas o `on:` só tinha
  `pull_request`/`push` — o `ml-deep-test` ("mandatório pré-K8" desde a 11ª onda) **não rodava em nenhum CI**.
  Fix: adicionado `workflow_dispatch:` + job `ml-deep-gate` (gateada por `if: github.event_name ==
  'workflow_dispatch'`, instala PyTorch CPU+ONNX, roda `make ml-deep-test`) — não dispara em PR/push, alinha
  spec↔realidade ([ml.yml](.github/workflows/ml.yml)).
- **Backlog tofu fechado corretamente** (`platform-infra-engineer`): o LOW registrado na 11ª onda
  (`tofu init … | grep -v "^$$"` podia dar falso-negativo). O fix sugerido lá (`| { grep -v … || true; }`)
  foi **provado errado** sob `set -o pipefail` (rc do `grep` avaliado individualmente). Forma correta:
  `grep -v "^$$"` → `sed '/^$$/d'` (sempre sai 0), **preservando** a propagação da falha do `init` sob
  `pipefail` — o vetor de mascaramento que o `security-reviewer` bloqueou na 11ª onda **não** reabre
  ([platform.mk](make/platform.mk):113).

**Gates verdes (13ª onda):** `money-ledger-guardian` **PASS** (revisão adversarial obrigatória — gate TX-2 agora
varre **14 arquivos** reais antes invisíveis, sem falso-positivo na featurização ML, plugado em
`ml-batch-test`/`verify`; catalogou 4 vetores de evasão estruturais ao design conjuntivo intra-linha
— `np.float64()`, alias intra-linha, pandas `.mean()`, divisão implícita — **nenhum material hoje** pois
`ml/fraud`/`ml/pacing` não manuseiam dinheiro real, e o TX-2 canônico é o tipo `Money` no fio, não este piso de
lint; ressalva registrada p/ quando uma feature consumir `minor_units` direto); `parity-golden-test-guardian`
**PASS** (escopo 100% gate/CI, zero toque em runtime/`.proto`/hot path/dinheiro/fixtures; paridade Go↔Python
byte-a-byte intacta; `make parity-golden-short` verde; `ml-batch-test` com o novo gate não quebra o agregado;
deep default-off preservado, `ml-deep-gate` gateada por `workflow_dispatch`). `security`/`privacy` **não
acionados** no fechamento — superfície de gate de lint/CI (a regra de ouro vale para os gates). Veredito do
arquiteto: a `main` permanece **genuinamente esgotada em código de produto**; o próximo movimento real segue
sendo exclusivamente **infra/spec viva externa**.

### ✅ Entregue na Fase 3 — 14ª onda: `PostgresConfigAdapter` real (fecha o laço console→decisão) (sob ADR-0004, sem ADR novo; gates verdes)

Onda de **um artefato de produto** com endurecimento adversarial: promove o BFF de config do stub
`InMemoryConfigAdapter` (I4) para o **`PostgresConfigAdapter` real** — espelho exato do já-committado
`PostgresPaymentsAdapter` (hardening) —, escrevendo no **mesmo schema `config` que o motor de decisão (Go)
lê no snapshot**, fechando o laço "administrar no console → servir no site". Selecionado por `BFF_PG_DSN`
(mesmo `pgPool` compartilhado com payments); sem DSN, cai no stub in-memory (dev/CI). Toda operação corre
numa transação com `SELECT set_config('adserver.tenant_id',$1,true)` (TX-3/CA-1, tenant SEMPRE do ctx),
`rate` lido como `rate::text`→`Money` (TX-2, nunca `Number`), senha do anunciante gravada como hash `scrypt$`
(DA-11). Uma **revisão adversarial multi-lente** (money/security/schema-contract/bff-qa, cada achado
refutado-por-omissão e adjudicado em primeira-mão) rendeu **3 defeitos reais** (2 FPs corretamente descartados):

- **[HIGH] Escrita cross-tenant por owner_id forjado** (`security-reviewer`): os `create*` aceitavam
  `owner_id`/`parent_id` do cliente e os INSERTiam **sem** verificar que o pai pertence ao tenant — o RLS
  `WITH CHECK` valida só o `tenant_id` da PRÓPRIA linha, e a FK de banner/zone/campaign **ignora** o RLS
  (cap/rule têm owner polimórfico **sem FK**). Confirmado em primeira-mão: o loader `BYPASSRLS`
  ([internal/configload/assemble.go](internal/configload/assemble.go)) agrupa caps por `owner_type:owner_id`
  **sem** guarda de tenant (a SELECT de caps nem lê `tenant_id`), então `cap.create{ownerId:<campanha do
  tenant B>, limitCount:1}` capava a campanha de B a 1 impressão — **DoS cross-tenant que B não consegue
  diagnosticar** (o RLS esconde a linha de A). **Corrigido** com `assertParentVisible()` — SELECT filtrado
  pelo RLS ANTES do INSERT em `createCampaign/Banner/Zone/Cap/DeliveryRule` (+`rule_set_id`), mesma disciplina
  do `linkCampaignZones` que já validava os dois lados via `INSERT...SELECT`. (`WITH CHECK` **não** fecharia
  isto — valida o dono da linha, não os IDs que ela referencia.)
- **[MEDIUM/LOW] `updateBanner` nulificando `asset_url`/`dest_url`** (`schema-contracts-steward`):
  `UpdateBannerInputSchema` deixava ambos `nullable`, mas o `updateBanner` não gere `asset_blob`/`creative_type`
  — gravar NULL violaria `banners_asset_xor_chk`/`banners_dest_url_chk` (0001), erro 23514 → 500 não-tratado
  (o stub in-memory mascarava). **Confirmado em Postgres real** (ambos 23514). **Corrigido** removendo o
  `.nullable()` do schema de update (espelha os `.refine()` do create); alternar a representação do criativo
  é recriar o banner.
- **[REAL, alta-alavancagem] Teste de RLS do config quebrado + prova de escrita ausente**: o
  [rls_isolation_test.sql](db/config/tests/rls_isolation_test.sql) **abortava no próprio seed** (INSERT de 2
  linhas com `RETURNING id INTO <escalar>` → "query returned more than one row") — **nunca passou**, então a
  isolação CA-1 do config estava **afirmada, não provada** (o caminho `make db-test`/`db-test-all` exige
  Postgres externo/Docker, fora do `make verify`). **Corrigido** (dois INSERTs de 1 linha) + **BLOCO 6 novo**
  provando a REJEIÇÃO de escrita cross-tenant (INSERT forjado + UPDATE tenant-flip → 42501), espelhando o
  Bloco 7 do ledger. Roda agora ponta-a-ponta: **38 PASS** contra Postgres local, reversível (up/down/up).

Endurecimentos de acompanhamento: **`WITH CHECK` explícito** nas 8 policies do
[0002_config_rls_up.sql](db/config/migrations/0002_config_rls_up.sql) — defesa-em-profundidade + paridade com
o ledger (medido em primeira-mão: a policy permissiva `FOR ALL` só-`USING` **já** barra INSERT/UPDATE
cross-tenant por omissão, então **não era um buraco funcional**, é robustez a policies por-comando futuras);
**GRANT USAGE ON SEQUENCES** ao `adserver_app` em [dev_roles.sql](db/seed/dev_roles.sql) (INSERT em BIGSERIAL
precisa de `nextval` — latente sem isto); e **`postgres-config.test.ts` novo** (a lacuna vs. o precedente
payments) provando a sequência de tenant, o par `rate+currency` atômico, o hash de senha, `buildSet` e a
guarda de owner. **2 FALSOS-POSITIVOS descartados:** (a) `campaign_zones` só-`USING` valida só o lado campanha
— mas o `linkCampaignZones` já valida os dois lados via `INSERT...SELECT` filtrado, e a cascata dupla-fecha
por tenant (nenhuma serving/leak/crash reproduzível); (b) "sem teste unitário" — endereçado nesta onda.

**Gates verdes (14ª onda):** `bff-ci` typecheck+lint+**73 testes** (era 54; +19 do config adapter);
config RLS **38 PASS** (fresh DB, up/down/up); `db-lint` (no-float SQL) ok; loader Go inalterado (a guarda
vive no BFF, camada certa da escrita). `security-reviewer` — vetor HIGH fechado com prova; `money-ledger-guardian`
**sem achados** (`rate::text`/`Money`, sem float, par atômico); `schema-contracts-steward` — 2 achados de
paridade contrato↔CHECK fechados; `parity-golden-test-guardian` — zero toque em hot path/`.proto`/dinheiro/
fixtures do motor de decisão. **Regra de ouro mantida:** nenhum escopo inventado — o adapter era o WIP em
curso, e cada mudança rastreia a um invariante documentado (TX-2/TX-3/CA-1/DA-11) ou a um defeito reproduzido
em primeira-mão contra Postgres real. Disciplina de medição: as perguntas de RLS `WITH CHECK` e de violação de
CHECK foram **decididas rodando SQL real no Postgres local**, não por aritmética de LLM.

### ✅ Entregue na Fase 3 — 15ª onda: varredura profunda de bugs (item 6), 11 achados corrigidos/gated (sob ADR-0004, sem ADR novo; gates verdes)

Varredura multi-lente do addon (5 lentes: isolamento-de-tenant/owner-ref, dinheiro TX-2/DA-10, hot-path Go +
concorrência, gates-de-CI-que-mentem, migrações/testes de BD), cada achado **adversarialmente verificado
(refute-por-omissão) e adjudicado em primeira-mão** contra Postgres/pgvector real, Go `-race` e execução real
dos gates. **12 achados reais** (2 eram o mesmo defeito por 2 lentes → **11 distintos**), 1 FP descartado. A
varredura confirmou 2 classes que a onda 14 previu: "teste que nunca prova o invariante" (o bug de seed do
config **se repete no vector**) e a classe `WITH CHECK` (descartada como FP no config por ter USING de igualdade
estrita, **é real no help_doc** por causa do ramo `IS NULL`). Corrigido em 4 commits:

- **DB/vector** (`a083181`): [HIGH] o `vector_rls_isolation_test.sql` **nunca passou** (3 defeitos SQL
  empilhados na BLOCO 1c) → as provas cross-tenant dos embeddings RAG nunca rodavam; corrigido +
  **BLOCO 7** de rejeição de escrita (42501), **18 PASS**. [MED-sec] `help_doc_embeddings` só-`USING`
  (ramo `tenant_id IS NULL`) deixava um tenant **inserir doc "público" que todos leem** — `WITH CHECK`
  explícito fecha. [MED-ci] `db.yml` usava `postgres:16` sem pgvector → o gate RLS inteiro nunca rodava →
  imagem `pgvector/pgvector:pg16`.
- **Hot-path Go** (`39c03c0`): [HIGH] o registro do WAL de telemetria persistia só o payload → no replay
  pós-crash o `topic` sumia → Kafka rejeita → **evento perdido** (quebra a durabilidade que a reconciliação
  DA-7 assume); + nunca compactava (re-replay infinito). Fix: WAL carrega topic+key, replay reconstrói,
  Close() compacta; teste e2e prova replay→produce com topic REAL. [MED-sec] capping fazia `Incr` e depois
  `Expire` (erro engolido) → chave pseudônima por-usuário **sem TTL, permanente** (viola DA-6/TX-5); fix:
  `INCR`+`PEXPIRE` atômicos (Lua).
- **CI órfão** (`f68fb04`): [MED/MED/LOW] `bff-ci`, `data-validate`, `web-ci` existiam como alvos `make` mas
  **nenhum workflow os acionava** (classe das ondas 11/13) → 3 novos workflows (bff/data/web).
- **Billing + ranker** (`04f951a`): [LOW] `calc_cpm_amount` faturava a fração sub-mille (`(imp/1000)*rate`) em
  vez de `floor(imp/1000)*rate` (BILLING.md §4.1) — 999 impressões faturavam 2.00 em vez de 0.00; fix floor +
  teste. [HIGH/MED **gated**] a race de mis-atribuição OPE do ranker/shadow (`last` compartilhado lido após
  `Decide`) é **inerte** (RANKER/AB/SHADOW off até E4/J3) e seu conserto é refactor de hot-path — **documentada
  como pré-condição bloqueante de E4/J3** (comentário FALSO "handler não reusado entre goroutines" corrigido),
  não apressada em código gated-off.

**Gates verdes (15ª onda):** `make go-build`+`go-vet`+`go-test` (**28 pacotes `-race`**, golden de paridade
intacto — sem regressão no motor de decisão) + `make parity-golden-short`; config **38 PASS** e vector **18
PASS** (fresh DB, up/down/up); `make data-validate` (12 invariantes) + `data-billing-test`; `bff` typecheck+lint+
**73 testes**; `web`/`db`/YAML válidos. **Regra de ouro:** nenhum escopo inventado — cada correção rastreia a um
invariante (TX-2/TX-3/CA-1/DA-6/DA-7/DA-11) ou a um defeito **reproduzido em primeira-mão**; o achado gated-off
foi **documentado sob seu gate**, não reescrito às pressas.

### ✅ Entregue na Fase 3 — 16ª onda: golden test de paridade **CA-3 (Criativos §4.3)** — o único golden faltante da suíte (sob ADR-0004, sem ADR novo; gates verdes)

Adicionado o **golden test CA-3 ausente** — o único deliverable de código que o Plano de Desenvolvimento nomeia
para E3 ("*adicionar o golden test CA-3 ausente — hoje o repo só tem `ca2/ca4/ca5/ca6_*_golden_test.go`*"),
e o único item de código de produto do addon que **não** está gated por tráfego/infra/legal (o golden exercita
os pacotes `internal/*` diretamente, sem infra externa). Fecha o gate de aceitação de E3 do lado da suíte de
paridade: **CA-2/CA-4/CA-5/CA-6 (existentes) + CA-3 (adicionado nesta onda)**. Dois arquivos novos:
`tests/parity/ca3_creatives_golden_test.go` + `tests/parity/golden/ca3_creatives.json` — nenhum código de
produto tocado.

- **Escopo HONESTO (metade importável vs `package main`):** CA-3 tem quatro sub-critérios, mas a lógica que
  "faz algo" (render responsivo do HTML5, contagem própria/dupla do 3p, geração/validação de VAST, guarda SSRF
  do `dest_url`) vive no `services/collector` **`package main`** (NÃO importável de um `parity_test` externo) ou
  apenas no Postgres (CHECK `banners_dest_url_chk`). O golden trava a metade **importável e real**: o
  MAPEAMENTO `creative_type` → campo do `Banner` via `configload.Assemble` (image→ImageURL, video→VideoURL,
  html5|thirdparty_tag→HTML com AssetBlob preferido, DestURL→ClickURL incondicional), a SELEÇÃO+EXPOSIÇÃO via
  `cascade.Engine.Decide`, e o vínculo server-side do `dest_url` no token HMAC + rejeição de token
  adulterado/forjado/vazio via `clicktoken.Signer` (a fatia importável real de CA-3.1). Um **gate de
  cross-referência** (`TestCA3_CollectorServing_CrossRef`) NOMEIA os **11 testes co-localizados do collector**
  (`package main`) que cobrem a metade de serving — todos verificados **presentes e verdes** — para que um
  rename quebre o CI (mesmo padrão de contrato-documental do CA-6).
- **Varredura adversarial (3 céticos refute-por-omissão + 1 crítico-de-completude, adjudicação em 1ª mão):**
  **0 casos fabricados** — os céticos recomputaram cada valor esperado contra o código real
  (`assemble.go`/`cascade.go`/`clicktoken.go`), confirmaram que cada caso chama o símbolo de produção (sem
  re-implementação/mock) e rastreia a um sub-critério real de CA-3, e que só os 2 arquivos foram tocados. O
  crítico-de-completude achou — e **eu confirmei medindo em primeira mão** — 1 lacuna real na camada importável:
  `setCreative` (`assemble.go:437-450`) é um `switch` **sem `default`** (ao contrário dos 4 switches-irmãos
  tier/pricing/vector/operator, que têm), logo um `creative_type` não-reconhecido deixa **zero** campos de
  payload, mas o banner **ainda é selecionado** (`eligibleBanners` filtra só por Active+regras, nunca pelo
  payload). O invariante REAL é "**no máximo um**", não "exatamente um" — a asserção original o superestimava.
  Fechado com **CA3-007** (tipo `native` não-reconhecido → 0 payload, `ClickURL=dest`, ainda selecionado) e a
  asserção corrigida para `>1` (a violação estrutural real; os expects por-campo já fixam o "exatamente um" no
  caminho feliz). Paralelo à honestidade de CA3-002 (não-rejeição por `dest` ausente).

**Gates verdes (16ª onda):** `go build ./...` limpo; `go vet ./tests/parity/... ./internal/...` limpo;
`go test ./tests/parity/... -race` verde — **CA-3: 7 casos golden (CA3-001..007) + 2 testes auxiliares**
(`TestCA3_DestURL_ServerSideBinding_TamperRejected`, `TestCA3_CollectorServing_CrossRef`), todos PASS; ca2/ca4/
ca5/ca6 **intactos** (sem regressão no motor de decisão); os **11 testes de collector cross-referenciados**
rodados e verdes. `gofmt` limpo, JSON válido. **Regra de ouro:** nenhum escopo inventado — o golden trava só
comportamento Go **real e importável**, a metade de serving é cross-referenciada (não re-testada nem
refatorada), e a única correção sobre o gerado (CA3-007 + `>1`) **rastreia a um invariante reproduzido em
primeira mão** (`switch` sem `default`), não a uma mudança manufaturada.

### ✅ Entregue na Fase 3 — 17ª onda: correção **HOT-1/HOT-3 — RankResult por-request** (G0/E6+E10; sob ADR-0004, sem ADR novo; gates verdes)

Fecha a **única pré-condição de código marcada como bloqueante** no próximo plano
([docs/plano-desenvolvimento-por-addon.md](docs/plano-desenvolvimento-por-addon.md) §5 **G0**,
`E6` do `decision-engine-engineer` + `E10` do `ml-optimization-engineer`, gate
`parity-golden-test-guardian`). Refs: `TX-4`, `DA-3`, `ADR-0003 §G (J3/J4)`, `ADR-0004 §A`, `CA-2`.

- **Defeito (HOT-1/HOT-3):** o ponto de extensão de ML (`internal/ranker`) devolvia o resultado
  rico de cada request — propensity/model_version/decision_id/zone_id/scores — via **campos
  compartilhados** (`MLRanker.last`; `BanditRanker.last`/`cfg` via `WithConfig`;
  `ShadowRanker.shadowDecisionID`/`lastShadow` via `SetRequestContext`) lidos pelo handler
  **depois** que `cascade.Decide()` retorna. O mutex eliminava o *data race*, mas **não** a
  mis-atribuição por-request: como uma única instância é compartilhada entre goroutines
  concorrentes (net/http reusa o handler), o request A podia registrar a atribuição do request B
  → **OPE/IPS/DR enviesado** e join de shadow envenenado. Inerte com os flags off; travante antes
  de ligá-los.
- **Fix (Design A — ranker por-request):** novo `cascade.Engine.DecideWithRanker(req, snap, r)`
  (mesma `decide()` interna de `Decide()`; `Decide()` intacto ⇒ caminho default byte-idêntico) +
  `internal/ranker/request.go` com `RequestRanker`/`RequestBanditRanker`/`RequestShadowRanker`
  construídos **a cada request** no handler, carregando os inputs do request (decisionID/zoneID/
  BanditConfig) e referenciando só as deps imutáveis-durante-serving (o `*MLRanker` compartilhado:
  socket/client/budget/model_version; o `ShadowSink`). O resultado é gravado em campo **local**
  (nunca compartilhado) e lido via `Result()` na mesma goroutine — race-free por construção, sem
  socket/goroutine/Engine novo (TX-4). **Simplificação habilitada pelo design:** o 2º
  `cascade.Engine` (`cascadePure`) e o rebuild do engine de tratamento A/B saíram — agora há **um
  único Engine**, e controle/tratamento/legacy/shadow são só a escolha do `Ranker` passado a
  `DecideWithRanker`.
- **Prova (`-race`, `internal/ranker/request_race_test.go`):** 3 testes com 60 goroutines cada
  provam isolamento por-request nos três caminhos (MLRanker legacy; BanditRanker propensity+epsilon;
  ShadowRanker decision_id+zone_id); um **canário de regressão**
  (`TestLegacySharedFieldPattern_ExhibitsMisattribution_Regression`) reproduz a mis-atribuição do
  padrão antigo em ~92–96% das leituras sob carga — provando que o teste detecta o bug real e o novo
  design o elimina. As APIs antigas (`Rank`/`LastResult`/`WithConfig`/`SetRequestContext`) ficam **só
  para os testes single-goroutine/canário**; comentários atualizados (`ranker.go`/`bandit_ranker.go`/
  `shadow.go`) de "KNOWN LIMITATION" → "FIXED", com aviso de não re-wire via `cascade.WithRanker` no
  serving (achado LOW convergente dos dois gates — classe "a spec não mente" — corrigido na janela).

**Gates verdes (17ª onda):** revisão adversarial `parity-golden-test-guardian` **PASS** (goldens
CA-2/3/4/5/6 + shadow harness + dual-run + `TestABParity_*` de controle bit-idêntico/kill-switch/
revenue-guard, todos verdes sob `-race`; o teste `-race` é genuíno; DA-3/TX-4 intocados; a
eliminação do `cascadePure` comprovada pelos AB parity tests) + `ml-optimization-engineer` **PASS**
(propensity/model_version/scores/decision_id/zone_id 100% por-request; matemática de ranking —
`rankInternal`/`ExploreRank`/`ScoreCandidate` — intocada). Verificação inline de 1ª mão: `go build`,
`go vet`, `go test -race -count=2` (cascade/ranker/decision), `make parity-golden-short`,
`go test ./...` (módulo inteiro) e `make verify` (buf TX-1 + no-float TX-2) **todos verdes**.
**Backlog não-bloqueante** (registrado pelo gate parity): teste E2E `httptest.Server` + N goroutines
batendo em `/v1/decide` fecharia a prova de isolamento no limite de produção completo (hoje provado
nos wrappers isolados + argumento arquitetural).

### ✅ Entregue na Fase 3 — 18ª onda: **hot-reload do GeoLite2 (.mmdb) sem restart** (G0/E5; sob ADR-0004, sem ADR novo; gates verdes)

Fecha o **próximo item de código de G0** na sequência ordenada do próximo plano
([docs/plano-desenvolvimento-por-addon.md](docs/plano-desenvolvimento-por-addon.md) §5 **G0**,
`E5` do `decision-engine-engineer`, gate `parity-golden-test-guardian`), logo após o HOT-1/HOT-3
(E6/E10) fechado na 17ª onda. Refs: `DA-9`, `CA-9`, `§4.10`, `TX-5/DA-11`, `ADR-0002 §C`.
Triada por fan-out multiagente read-only sobre os 6 itens restantes de G0 (0 falsos-positivos: o
plano representava a realidade; E5 era o próximo genuíno).

- **Defeito (E5):** o `.mmdb` do GeoLite2 era carregado **uma única vez no boot**
  ([internal/geo/maxmind.go](internal/geo/maxmind.go)) — o comentário admitia "not implemented in I2".
  Atualizar os dados (DA-9/§4.10: "auto-atualizam sem intervenção manual", CA-9) exigia **restart** do
  collector, que resolve geo por request concorrente ([collector `Resolve`](services/collector/cmd/collector/main.go)).
- **Fix (RWMutex, não `atomic.Pointer` nu):** `MaxMindResolver` passa a guardar o `*maxminddb.Reader`
  sob `sync.RWMutex`; `Resolve` faz `Lookup` sob `RLock`; novo `Reload(dbPath)` abre o novo reader, troca
  sob `Lock` e **só então** fecha o antigo. A escolha do RWMutex é **de correção, não de estilo**: o reader
  é lastreado por **mmap** e `Close()` faz **munmap** — com `atomic.Pointer` sozinho, um `Lookup` em voo
  poderia dereferenciar memória já desmapeada por um `Reload` concorrente (use-after-munmap/SIGSEGV). O
  `Lock()` só é concedido após todos os `RLock` drenarem, então o close do reader antigo nunca corre contra
  um `Lookup` que ainda o usa. Em **falha** de reload (arquivo ruim/corrompido), o reader anterior **continua
  servindo** — nunca rebaixa um DB funcionando para vazio (**DA-9**).
- **Gatilho (collector):** `runGeoReloader` faz **poll de mtime** (`GEOIP_RELOAD_INTERVAL`, default 1h) e
  chama `Reload` quando o job externo de auto-atualização substitui o arquivo — sem sinal do operador,
  espelhando o `Refresher` de [internal/snapshot/loader.go](internal/snapshot/loader.go). Atado ao `ctx` de
  shutdown; **nunca vê nem loga IP** (só `os.Stat`/`os.Open` de um path — TX-5/DA-11 intactos).
- **Prova (`-race`, [internal/geo/maxmind_reload_test.go](internal/geo/maxmind_reload_test.go)):** fixtures
  `.mmdb` gerados com `mmdbwriter` **só-teste** (Go puro, sem cgo → build de produção segue hermético,
  **ADR-0002 §C**; nenhum binário de produção importa `mmdbwriter`). Três testes: swap-in-place (BR→US na
  mesma instância, zero downtime), falha retém o DB antigo (DA-9) e **50×Resolve ∥ 4×Reload** sem data race
  nem panic.

**Gates verdes (18ª onda):** revisão adversarial `parity-golden-test-guardian` **PASS** — o gate **injetou
o bug** (removeu o RWMutex) e confirmou que o teste de concorrência trava com SIGSEGV/use-after-munmap
(**canário genuíno, não teatro**), depois restaurou o arquivo e reconfirmou tudo verde; paridade bit-idêntica
(o caminho existente é intocado; os goldens usam `StubResolver`), DA-9, TX-5/DA-11 e build hermético todos
verificados de forma independente. Verificação inline de 1ª mão: `go build ./...`, `go vet`
(geo/collector/decision), `go test -race -count=2 ./internal/geo/...` (8 testes), `make parity-golden-short`
(3 pacotes `-race`) e `make verify` (buf TX-1 + no-float TX-2) — **todos verdes**; `go list -deps` prova
`mmdbwriter` fora de todo binário de produção. **Backlog não-bloqueante** (registrado pelo gate): endurecer
`Reload` com guard `closed` contra reabrir após `Close()` (hoje inatingível — nenhum caminho de produção chama
`Close`); table-test de `geoReloadInterval`; sub-caso de arquivo truncado no teste de DA-9; e o gap de boot
(se o `.mmdb` falha ao abrir no boot, cai para `EmptyResolver` e o reloader não arma — candidato do
`platform-infra-engineer`, não regressão desta onda). Junto, a onda corrigiu o **drift de status do plano**:
E6/E10 (fechados na 17ª) ainda constavam "→ próxima" e o E11 trazia um **overclaim** ("o wrapper Go é escrito e
testado") — ambos corrigidos em [docs/plano-desenvolvimento-por-addon.md](docs/plano-desenvolvimento-por-addon.md).

> **G0 — progresso:** 2 de 7 itens de código fechados (E6/E10 na 17ª, **E5 na 18ª**). Restam, sem exigir
> infra: **ml E11** (ONNX nativo — próximo), **schema E8** (falso-positivo do `proto-gen-check`),
> **platform E8** (mandato item 4), **frontend E9+E10** (fail-closed + stack §2.5) e **copiloto E12**
> (higiene de layout). Triagem read-only confirmou os 5 como pendências genuínas (0 falsos-positivos).

### ✅ Entregue na Fase 3 — 19ª onda: **ONNX Runtime nativo no ranker-sidecar** (G0/E11; sob ADR-0003 §B, sem ADR novo; gates verdes)

Fecha o **3º item de código de G0** (ml E11), o próximo após E5 (18ª onda). Substitui o `StubInferencer`
sempre-0.0 por um `OnnxInferencer` real, **sob build tag**, mantendo o build default **hermético**
(ADR-0002 §C). Dono `ml-optimization-engineer`; gates `tech-lead-architect` + `parity-golden-test-guardian`.
Refs: `ADR-0003 §B` (sidecar Treelite/ONNX via UDS, hot path Go CGO-free), `TX-4`, `DA-3` (fail-open).

- **Defeito (E11):** o sidecar ([main.go](services/ranker-sidecar/cmd/ranker-sidecar/main.go)) SEMPRE
  instanciava `stub.NewStub` (`Score`→0.0) — mesmo com `RANKER_MODEL_PATH` setado logava "ONNX runtime not
  compiled in". Não havia `OnnxInferencer`, nem build tag, nem dep de runtime → o re-ranker de yield era
  inerte (0.0 preserva a ordem pura da cascata, mas nunca havia inferência real).
- **Fix (build-tag, hermético por construção):** novo pacote
  [services/ranker-sidecar/internal/onnx/](services/ranker-sidecar/internal/onnx/): `onnx.go` (`//go:build
  onnx`) implementa `stub.Inferencer` via `github.com/yalue/onnxruntime_go` (carrega o `.onnx`, tensor
  `[1,23]`, extrai P(click=1); erro transiente → `(0,nil)` fail-open ADR-0003 §A/DA-3); `disabled.go`
  (`//go:build !onnx`) devolve `ErrNotCompiled` → o **build default (`go build ./...`) fica CGO-free** e o
  `main` cai no stub (fail-safe no boot, nunca mid-request). Wiring troca o stub incondicional por
  `onnx.New(...)` com fallback. `go list -deps` prova `onnxruntime` **fora** de todo binário de produção
  default. `yalue/onnxruntime_go` entra no `go.mod` mas só compila sob `-tags onnx`.
- **Contrato de serving sem ZipMap:** o `pctr_model.onnx` era exportado com **ZipMap**
  (`seq(map(int64,tensor(float)))`), hostil a runtimes embarcados. Re-exportado do booster salvo com
  `zipmap=False` ([train_pctr.py](ml/training/train_pctr.py) `export_onnx`) → output `probabilities` vira
  **tensor plano `[N,2]`** (P(1)=coluna 1); equivalência numérica booster≡onnx `max_abs_diff≈8e-08`. Único
  consumidor Python (`validate_onnx`) já era defensivo; `make ml-test` verde (0 regressão).
- **Prova (paridade de inferência real):** o ambiente tinha `libonnxruntime.so.1.26.0` (venv) + gcc + CGO,
  então o path `-tags onnx` foi **buildado, linkado e RODADO** de fato. `onnx_parity_test.go` (`//go:build
  onnx`, `t.Skip` sem lib/modelo) prova `OnnxInferencer.Score` (Go) ≡ referência Python `onnxruntime` sobre
  os mesmos vetores canônicos de `parity_cases.json` — **diff=0.00e+00 nos 5 casos** + fail-open em input
  malformado. Cadeia end-to-end: Go `Featurize` ≡ vetores (`internal/ranker/parity_test.go`) + Go
  `Score(vetores)` ≡ golden.

**Gates verdes (19ª onda):** `tech-lead-architect` **PASS** (build hermético provado com `CGO_ENABLED=0`;
`onnxruntime` fora de `go list -deps`; regra de ouro mantida — ONNX é sancionado por ADR-0003 §B, sem
Triton/GPU/novo processo, TX-4 não ampliado; re-export `zipmap=False` numericamente provado, sem quebrar
consumidor) + `parity-golden-test-guardian` **PASS** (cadeia de paridade **genuína, não tautológica** —
provada por 2 canários: trocar a coluna extraída ou perturbar o vetor faz o teste FALHAR). Verificação inline
de 1ª mão: `go build ./...`, `go vet`, `go test` default, `go test -tags onnx` (paridade bit-exata),
`make ml-training-test` (17 testes), `make parity-golden-short`, `make verify` (buf TX-1 + no-float TX-2) —
**todos verdes**. **Correções aplicadas na janela** (exigidas/recomendadas pelos gates): (a) o comentário de
`onnx.go` alegava um cross-check `numFeatures↔ranker.FeatureVectorLength` inexistente → adicionado de verdade
em `features_contract_test.go` (roda no **CI default**, sem CGO); (b) novo pytest `test_onnx_export.py` cobre
o contrato `zipmap=False` em CI normal (fecha o "gate que sempre skipa" — o parity `-tags onnx` depende de lib
não disponível em CI); (c) reconciliação de doc em `ADR-0003 §B` (a nota "fora do go.mod" agora qualifica a
opção Treelite; o binding Go do ONNX entra no go.mod só sob build tag). **Backlog não-bloqueante** (gates): o
golden `score_golden.json` depende do `.onnx` gitignored (teste `-tags onnx` skipa em CI sem o artefato);
`model_version` reportado como a versão real mesmo no fallback stub (candidato a `stub-fallback`); warning
cosmético de shape no output `label` (batch N>1). O golden e o artefato do modelo seguem gitignored (padrão
ADR-0003 §B: modelos compilados não versionados como blobs).

### ✅ Entregue na Fase 3 — 20ª onda: **falso-positivo do `proto-gen-check` corrigido (pin de plugin)** (G0/E8; sob TX-1, sem ADR novo; gates verdes)

Fecha o **4º item de código de G0** (schema E8), o próximo após E11 (19ª onda). Elimina o
**falso-positivo do staleness gate** `proto-gen-check` sem afrouxar o gate. Dono
`schema-contracts-steward`; verificação adversarial `parity-golden-test-guardian` + `tech-lead-architect`.
Refs: `TX-1`, [.github/workflows/buf.yml](.github/workflows/buf.yml), [Makefile](Makefile) (`proto-gen-check`).

- **Defeito (E8):** [proto/buf.gen.yaml](proto/buf.gen.yaml) declarava os plugins remotos **sem versão
  fixada** (`remote: buf.build/protocolbuffers/go` e `remote: buf.build/bufbuild/es` = `latest` flutuante).
  O `make proto-gen-check` regenera `gen/` e compara com o versionado — mas ao puxar o plugin `latest` pegava
  `protoc-gen-es v2.12.1` (o versionado foi gerado com **v2.12.0**), fazendo o gate **FALHAR só pela mudança
  cosmética do header `@generated by`**, sem nenhum drift de schema. Risco: o time começar a ignorar o gate
  por hábito e mascarar um drift **real** futuro.
- **Fix (config-only, 1 arquivo):** versões dos dois plugins remotos **fixadas** em `buf.gen.yaml` —
  `buf.build/protocolbuffers/go:v1.36.11` e `buf.build/bufbuild/es:v2.12.0` (pin por versão, suportado pelo
  buf v2) — batendo **exatamente** com os headers `@generated by` já versionados. A regeneração sob o pin é
  **byte-idêntica** ao commitado (`git status` mostra só `proto/buf.gen.yaml`; nada muda em `gen/`). A
  distinção **'diff de schema' (deve falhar) vs 'diff de gerador' (não deve falhar)** foi documentada em
  [proto/README.md](proto/README.md) e no comentário do job de CI.
- **Prova (gate não virou tautológico):** `parity-golden-test-guardian` injetou um campo real
  (`AdRequest`) num `.proto` sob o pin → `proto-gen-check` **reprovou** (drift em `gen/go` **e** `gen/ts`),
  revertido em seguida; `tech-lead-architect` corrompeu um `gen/` → **reprovou** (exit 2). Ou seja, o pin
  neutraliza só o bump cosmético de plugin; **drift real de schema continua reprovando o merge**.

**Gates verdes (20ª onda):** `schema-contracts-steward` aplicou; `make proto-gen-check` verde
(`OK — gen/ esta em sync com proto/.`); `make verify` (proto-lint + proto-format-check + proto-build +
proto-breaking + no-float) verde; `buf lint` / `buf format --diff --exit-code` / `buf breaking --against main`
/ `buf build` — todos exit 0. Verificação adversarial **`parity-golden-test-guardian` PASS** (byte-identidade
independente via `diff -rq`; `buf breaking` limpo contra `main`; gate não-tautológico provado) +
**`tech-lead-architect` PASS** (pins batem com os headers `@generated`; CI `buf.yml` funciona idêntico com o
pin — plugins públicos no BSR, `push:false`; regra de ouro mantida — config-only, zero dep/infra nova).
Verificação de 1ª mão (main loop) via `git diff`/`git status`: árvore com única mudança em
`proto/buf.gen.yaml`, `gen/` intacto. **Observação operacional** (não-bloqueio): o pin cria o dever de
bumpar a versão **deliberadamente** (documentado em comentário no próprio `buf.gen.yaml`); candidato a
automação via PR (Renovate) mantendo a regeneração de `gen/` acoplada.

### ✅ Entregue na Fase 3 — 21ª onda: **supply chain do mandato item 4 (cosign keyless + SBOM + Trivy + Falco) + `policy-aml-kyc.hcl`** (G0/platform-E8; sob mandato item 4, sem ADR novo; gates verdes)

Fecha o **5º item de código de G0** (platform-infra E8), o próximo após E8-schema (20ª onda). Leva o **item 4
do mandato de 1/4 → 4/4** controles com código real, sem cloud. Dono `platform-infra-engineer`; verificação
adversarial `security-reviewer` + `privacy-compliance-auditor` + `tech-lead-architect`. Refs: mandato item 4
(cosign+SBOM+Kyverno+Trivy+Falco), `§2.7`, `CA-9`.

- **Gap 1 — OpenBao `policy-aml-kyc.hcl`:** existia só como comentário em
  [openbao-auth.yaml](platform/cells/aml-kyc/secrets/openbao-auth.yaml). Escrito
  [platform/secrets/openbao/policy-aml-kyc.hcl](platform/secrets/openbao/policy-aml-kyc.hcl) espelhando
  `policy-pci.hcl`: leitura só de `aml-kyc/data/{sumsub,chainalysis,custody}/*`, DB dinâmico
  `aml-kyc/db/compliance`, transit `aml-kyc`, ciclo de vida do próprio lease — **nega por omissão** `pci/*`
  e `platform/*` (isolamento de célula ADR-0004 §F).
- **Gap 2 — item 4 do mandato (era 1/4, só Kyverno):** novo workflow
  [.github/workflows/supply-chain.yml](.github/workflows/supply-chain.yml) (matrix dos 5 serviços): build →
  **SBOM** (syft SPDX+CycloneDX) → **Trivy** (fail em CRITICAL/HIGH, `exit-code 1`, **antes** do push) →
  push `ghcr.io` → **cosign sign keyless**. Job de PR faz build+scan sem publicar; publish só em tag/release
  (`id-token: write` só nele). Dockerfiles de produção: [deploy/docker/Dockerfile.go-service](deploy/docker/Dockerfile.go-service)
  (reusa a receita hermética distroless/nonroot/CGO-free de [deploy/local/Dockerfile](deploy/local/Dockerfile)
  para os 4 Go) + [deploy/docker/Dockerfile.copilot](deploy/docker/Dockerfile.copilot) (1º do copiloto Python).
- **cosign real via keyless (não placeholder):** adotado **keyless** (OIDC/Fulcio/Rekor) em vez de gerar par
  de chaves estático — elimina a superfície de chave privada a versionar/rotacionar (§2.7 "nada estático em
  git/imagem"). O bloco `publicKeys` com `REPLACE_WITH_COSIGN_PUBLIC_KEY` em
  [kyverno-baseline.yaml](platform/k8s/policy/kyverno-baseline.yaml) virou um atestador `keyless` (issuer
  GitHub OIDC + subject do workflow do repo + `rekor.url`). `grep` do placeholder agora **vazio** em todo código/config.
- **Ruleset Falco:** [platform/observability/falco-rules.yaml](platform/observability/falco-rules.yaml)
  (ConfigMap `falco-custom-rules`): exec em container privilegiado (geral + reforçado nas células reguladas),
  escrita fora de paths esperados nas células `pci`/`aml-kyc`, e binários/syscalls suspeitos. O deploy do
  daemonset é E9 (pós-cluster); o ruleset é código validável hoje.

**Falso-positivo pré-existente corrigido na mesma onda** (descoberto ao verificar o gate do E8): o próprio
`make platform-validate` estava **vermelho na main** — não por E8, mas por 3 bugs pré-existentes de outras
ondas. (1) [httproute-webhook.yaml](platform/cells/pci/gateway/httproute-webhook.yaml) e o do Sumsub usavam
um filtro `type: RequestTimeout` **que não existe** no Gateway API → movido para o campo correto
`spec.rules[].timeouts.request` (kubeconform passa a validar). (2) `otel-collector.yaml` (config nativa OTel,
sem `apiVersion`/`kind`) era varrido pelo kubeconform → excluído do `find` em
[make/platform.mk](make/platform.mk) (é validado por `platform-otel-validate`). (3) As `Policy` **namespaced**
das células eram **puladas como inválidas** pelo kyverno 1.13.4 (filtro `namespaces` proibido) → removido o
filtro redundante + JMESPath **null-safe** (`(X || []）[]`) + `label_match` para não errar em Pod sem campo.
As policies passaram a **carregar e enforçar** (antes não enforçavam nada). **Enforcement provado idêntico**
via `kyverno apply` (mesmos pods maus reprovados; os que davam `error` viraram `pass`/`fail` correto; totais
iguais).

**Gates verdes (21ª onda):** **`security-reviewer` PASS** (cosign não é mais placeholder; SBOM+Trivy-fail+cosign
cobrem supply chain e Falco cobre runtime; nenhum segredo versionado; `policy-aml-kyc.hcl` menor-privilégio;
**`enforcement_weakened: false`** nas células) + **`privacy-compliance-auditor` PASS** (isolamento de célula,
0 PII, TX-5/allowlists intactas) + **`tech-lead-architect` PASS** (keyless é config, não infra pesada; mandato
item 4 genuinamente 4/4; falso-positivo do plano agora verdadeiro). Verificação de 1ª mão (main loop):
`make platform-validate` genuinamente verde — `platform-tofu-validate` OK, `platform-kubeconform` OK (64
recursos, 0 inválidos), `platform-kyverno-test` **22/22** (baseline 4 + pci 8 + aml-kyc 10), `platform-otel-validate`
estrutural OK (semântico pula sem Docker local; CI tem Docker). **Diferido a E9** (honestamente, sem
falso-fechamento): deploy do daemonset Falco, verificação real do cosign keyless (Fulcio/Rekor via rede) e o
otel semântico. **Achado não-bloqueante** registrado (pré-existente, não introduzido): `proibir-env-secretkeyref`
inspeciona só `spec.containers[]`, não `initContainers[]` — candidato a hardening de cobertura.

> **G0 — progresso:** **5 de 7** itens de código fechados (E6/E10 na 17ª, E5 na 18ª, E11 na 19ª, E8-schema na
> 20ª, **platform E8 na 21ª**). Restam, sem exigir infra: **frontend E9+E10** (fail-closed + stack §2.5 —
> próximo) e **copiloto E12** (higiene de layout).

### ✅ Entregue na Fase 3 — 22ª onda: **fail-closed real do middleware de sessão em produção** (G0/frontend-E9; sob TX-3/CA-1, sem ADR novo; gate verde)

Fecha a **1ª das 2 dívidas de código do addon front/BFF** (frontend E9), a próxima na sequência ordenada do
próximo plano ([docs/plano-desenvolvimento-por-addon.md](docs/plano-desenvolvimento-por-addon.md) §5 **G0**,
addon front/BFF E9). Dono `frontend-bff-engineer`; gate adversarial obrigatório `security-reviewer`. Refs:
`TX-3` (isolamento de tenant), `CA-1` (ACL server-side), `§2.5`.

- **Gap corrigido (risco ALTO):** [web/console/src/middleware.ts](web/console/src/middleware.ts) deriva o
  `tenant_id` de um cookie de sessão HttpOnly. `verifySessionToken()` tinha um ramo **dev-stub** que aceitava
  o token como `base64url(JSON)` **sem verificar a assinatura HMAC** sempre que `SESSION_SECRET` estava ausente
  — e **não havia hard-fail se `NODE_ENV=production`**. Ou seja, em produção sem o segredo o sistema fazia
  **fail-OPEN** (aceitava `tenant_id` forjado → bypass de isolamento de tenant). A proteção dependia só de
  disciplina operacional, não de um guard estrutural.
- **Correção em dupla defesa (defense-in-depth):** predicado **puro** e testável
  [web/console/src/lib/session-guard.ts](web/console/src/lib/session-guard.ts) —
  `sessionConfigError(nodeEnv, secret)` retorna erro se, em produção, o segredo estiver **ausente ou < 32 bytes**.
  **Camada 1**: no topo de `middleware()`, se o predicado acusa erro, loga (`console.error`, sem vazar o motivo)
  e retorna **500 para toda rota casada pelo matcher** — "recusar boot" em nível de requisição, antes de qualquer
  outra lógica. **Camada 2**: dentro de `verifySessionToken`, produção sem segredo retorna `null` — nunca cai no
  parse do dev-stub, mesmo que a camada 1 seja removida por engano. Comportamento **dev/CI 100% preservado**
  (fora de produção o dev-stub segue funcionando; com segredo, o HMAC como antes). Hardening de **código**,
  independente da injeção real do segredo via OpenBao (item **E11**, separado e ainda gated).
- **Teste sem framework (network-free):** o console não tem jest/vitest;
  [web/console/src/lib/session-guard.test.ts](web/console/src/lib/session-guard.test.ts) usa o runner **nativo do
  Node 24** (`node:test` + type-stripping de `.ts`), 10 casos (produção sem/curto/vazio/válido segredo, UTF-8
  multi-byte por bytes, dev/test/undefined). Novo alvo `make web-test` ([make/web.mk](make/web.mk)) incluído em
  `make web-ci`; [web.yml](.github/workflows/web.yml) alinhado ao **Node 24** (type-stripping sem flag).
  `tsconfig.json` ganhou `allowImportingTsExtensions` (import `.ts` do teste sob `noEmit`).
- **Gate `security-reviewer` PASS** (adversarial): **`productionBypassPossible = false`**, **zero CRITICAL/HIGH**
  — traçou os 3 caminhos em produção (sem/curto/válido segredo) e confirmou que o header
  `x-adserver-session-tenant` só é injetado após HMAC+exp+UUID; denylist/CSRF/injeção intactas; teste
  **não-tautológico**. **1 MEDIUM residual registrado (não é bypass no escopo E9):** o guard é atrelado a
  `NODE_ENV` — um deploy produtivo com `NODE_ENV` unset/`development` reabriria o dev-stub (exige 2ª má-config;
  `next start` seta `NODE_ENV=production` por padrão). Fechamento pertence a **E11/infra** (enforce `NODE_ENV`
  no manifesto do pod, ou trocar a chave por flag explícito) — coordenar com `platform-infra-engineer`.
- **Verificação de 1ª mão (main loop):** `make web-ci` genuinamente verde — `tsc --noEmit` OK, `next lint`
  "No ESLint warnings or errors", **`node:test` 10/10 pass**. (No ambiente local os bins do npm estavam como
  cópias sem symlink — reparado só localmente; `node_modules` é gitignored e o CI faz install limpo.)

> **G0 — progresso:** dos 7 itens de código, 5 estavam fechados (17ª–21ª) e a **22ª fecha o fail-closed do
> middleware (frontend E9)** — a 1ª das 2 dívidas do addon front/BFF. Restam, sem exigir infra: **frontend E10**
> (alinhamento de stack §2.5: Next 16/React 19.2/shadcn/Zustand/Vercel AI SDK v5/a11y CI — **próximo**) e
> **copiloto E12** (higiene de layout).

### ✅ Entregue na Fase 3 — 23ª onda: **os 2 últimos itens de G0 (copiloto E12 + frontend E10) — G0 CÓDIGO-COMPLETO (7/7)** (sob triagem de escopo mínimo do tech-lead; gates verdes; mergeado na `main`)

Fecha o **G0** (Onda de Ativação de Go-Live). Dois itens em 3 commits + o focus-trap; depois **FF direto para a `main`** (`d60cd34`→`b4cb624`), com o CI substituído por verificação local de 1ª mão (o PR #1 era cross-fork/stale e não pôde ser atualizado — ver [[git-remote-push]]).

- **Copiloto E12 (`e3aca4c`)** — higiene de layout: os diretórios `services/copilot/guardrails/` e `rag/` eram
  **scaffolding VAZIO** (0 arquivos, nunca versionados) e os globs `guardrails*`/`rag*` no
  `[tool.setuptools.packages.find]` não casavam pacote nenhum (a funcionalidade real de guardrails/RAG vive em
  [graph/nodes.py](services/copilot/graph/nodes.py) · [tools/gateway.py](services/copilot/tools/gateway.py) · `app/*`).
  Removidos + `rmdir`. `find_packages`=`['app','graph','observability','tools']`; `make copilot-test` **126 passed**.
- **Frontend E10 (`f109767` + `18a02fe` + `b0cc569`)** — alinhamento ao mandato §2.5, sob triagem de **escopo
  mínimo** do `tech-lead-architect` (não reabrir arquitetura):
  - **Versões:** Next 15.3.3→**16.2.10**, React 19.1.0→**19.2.7**, `eslint-config-next` 16. O Next 16 **removeu
    `next lint`** → script migrado p/ `eslint` direto + `eslint.config.mjs` p/ a flat config nativa
    (`eslint-config-next/core-web-vitals`; `FlatCompat.extends` quebra com "circular structure"). React Compiler
    → `watch()`→`useWatch()` em [banners](web/console/src/app/banners/page.tsx)/[campaigns](web/console/src/app/campaigns/page.tsx).
    `next build` (16 rotas) verde.
  - **a11y-ci mecânico WCAG 2.2 AA — SEM Playwright:** axe-core + `@axe-core/puppeteer` + `puppeteer-core`
    contra o **Chrome do sistema** via `executablePath` (nenhum browser baixado; `overrides` puppeteer→puppeteer-core).
    Rota [/a11y-harness](web/console/src/app/a11y-harness/page.tsx) client-only, gated por `A11Y_HARNESS=1`
    (404 em qualquer build de produção). Alvo `make web-a11y` + workflow [a11y.yml](.github/workflows/a11y.yml).
  - **Alicerce shadcn** (`cn` util + `components.json`), **zero reescrita** dos 4 componentes estáticos gate-verdes.
  - **Diferidos com gatilho documentado** (regra de ouro): Zustand (gatilho = E12, sem estado cross-route real
    hoje) e Vercel AI SDK v5 (gatilho de reabertura anexado ao ADR-0003 — o SSE/HITL bespoke já passou no security gate).
- **Focus-trap do modal HITL (`b4cb624`)** — o `hitl-diff-preview` (`aria-modal="true"`) não implementava
  focus-trap (Tab escapava); adicionado (ciclo Tab/Shift+Tab no diálogo; Escape sai; restauração de foco SC 2.4.3),
  com **verificação mecânica** no a11y-ci (puppeteer simula Tab e afirma que o foco fica no diálogo). Corrige o
  achado que a E10 parte 2 havia **sinalizado, não corrigido**. + 2 achados do axe remediados na E10 (contraste
  do botão HITL amber-600→700; `<dl>` sem `<dt>/<dd>`).

**Gates (1ª mão):** `tech-lead-architect` (triagem de escopo mínimo) · `make web-ci` (tsc + `eslint --max-warnings 0`
+ node:test) · **`make web-a11y`** (0 violações axe + focus-trap) · `make copilot-test` (126) · `next build` (16 rotas)
· `security-reviewer` **APROVADO** nas 3 mudanças que tocaram superfície sensível (middleware E9, modal HITL, harness a11y).

> **G0 — CÓDIGO-COMPLETO (7/7).** E6/E10-HOT (17ª) · E5 (18ª) · E11 (19ª) · schema E8 (20ª) · platform E8 (21ª) ·
> frontend E9 (22ª) · **copiloto E12 + frontend E10 (23ª)**. Não há mais item de código de G0. Mergeado na `main`
> (`b4cb624`). **Próximo movimento real = G1 (cutover de infra)** — gated por cluster/OpenBao/FQDNs reais, não por código.

### ⏭️ Pendente da Fase 3

**K8** (promoção do deep ranking sob **uplift A/B + kill-switch**) segue **gated por
tráfego real** — o código está pronto desde K1 (flag default-off); a promoção espera o
número de uplift sobre o GBDT, que depende do cutover de infra da Fase 2.

> **HOT-1/HOT-3 — RESOLVIDO na 17ª onda (G0/E6+E10).** O **RankResult por-request** agora flui de
> `cascade.Engine.DecideWithRanker()` via `RequestRanker`/`RequestBanditRanker`/`RequestShadowRanker`
> (`internal/ranker/request.go`), em vez dos campos `last`/`cfg`/`SetRequestContext` compartilhados —
> `RANKER_ENABLED`/`AB_ENABLED`/`SHADOW_ENABLED` já podem ser ligados sob tráfego real concorrente sem
> enviesar o OPE. Provado por `internal/ranker/request_race_test.go` (`-race`: isolamento por-request
> nos 3 caminhos + canário de regressão do padrão antigo). Isso **destrava o veto de código do gate
> de paridade** (E14) sobre a promoção K8; o restante do gate K8 segue **só por tráfego real**.

A **habilitação de AEV/BND** segue **gated pela spec de produto** (`scale`/classificação/supply — CHECK
estrutural impede habilitar sem `scale`). **Pré-condições de go-live** restantes são
**só de infra/spec viva** (a camada de código está pronta **e provada** — ver hardening +
4ª onda acima, com runbook em [docs/ops/go-live-runbook.md](docs/ops/go-live-runbook.md)):
chaves reais via OpenBao, **KMS/HSM real** para a chave do envelope de PII (a cifra e seu
versionamento já existem), FQDNs reais das células, e Triton/GPU + Fireblocks sob seus
gatilhos mensuráveis.

---

## Layout do repositório

```text
.
├── Makefile                      # alvos proto-* e verify (espelham a CI)
├── docs/                         # documentos normativos (técnico + stack)
│   └── adr/                      # Architecture Decision Records (ADR-0001: near-real-time)
├── proto/                        # Schema Registry Protobuf (TX-1) — fonte do contrato de eventos
│   ├── buf.yaml                  # lint STANDARD+COMMENTS, breaking WIRE_JSON (BACKWARD-compat)
│   ├── buf.gen.yaml              # geração Go (hot path) + TS (front)
│   └── adserver/
│       ├── common/v1/            # Envelope, Geo, ServedTier
│       ├── money/v1/             # Money (asset_code, int64 amount, uint32 scale)
│       ├── telemetry/v1/         # AdRequest, Impression, Click, Conversion
│       └── decision/v1/          # Decision (propensão), Candidate, ExplorationPolicy
├── contracts/                    # contratos cross-cutting (prosa + DDL/seed)
│   ├── money/                    # tipo Money em todas as fronteiras + Asset Registry
│   ├── telemetry/                # contrato de propensão / loop de atribuição
│   └── lint/                     # política anti-float multi-linguagem
├── platform/                     # plataforma-base como código (§2.7) — aplica com cloud
│   ├── tofu/                     # OpenTofu (EKS/rede/addons) — root validável
│   ├── gitops/                   # Argo CD (AppProject + app-of-apps)
│   ├── k8s/                      # namespaces por célula, Cilium deny-all, Kyverno, RBAC
│   ├── observability/            # OTel Collector com redação de PII (TX-5)
│   └── secrets/openbao/          # políticas OpenBao (menor privilégio por célula)
├── scripts/ci/                   # guards anti-float (Go/Python/SQL)
└── .github/workflows/            # CI: buf (TX-1) + no-float (TX-2)
```

## Como usar a camada de contratos

```bash
# Schema registry (requer buf — https://buf.build; `make tools` instala em .bin/)
make proto-lint            # buf lint proto (STANDARD + COMMENTS)
make proto-format-check    # falha se algum .proto não estiver formatado
make proto-breaking        # rejeita mudanças não-BACKWARD vs. main (TX-1)
make proto-gen             # gera gen/go e gen/ts (requer rede p/ plugins remotos)
make verify                # tudo acima + guards anti-float (espelha a CI)
```

> A CI roda o equivalente em [.github/workflows/buf.yml](.github/workflows/buf.yml)
> (lint + format + breaking). A invocação do `buf breaking` é a partir da **raiz**
> (`buf breaking proto --against '.git#branch=main,subdir=proto'`), pois o `.git`
> vive na raiz e `proto/` é subdiretório.

O **Asset Registry** ([contracts/money/asset-registry.md](contracts/money/asset-registry.md))
é a fonte autoritativa de `scale` por ativo — sem ele não há aritmética
monetária correta. O seed inicial inclui BRL/USD/EUR/USDC/USDT/ERC-20 e as
linhas **AEV/BND** desabilitadas (`scale` a definir), que **não bloqueiam** as
Fases 0–1 (pagamentos ficam 100% fora do hot path; ver stack §3).

## Princípios invioláveis (resumo)

- **`float` proibido para dinheiro** em qualquer linguagem (TX-2). Use `Money(asset_code, amount, scale)` / `NUMERIC` / `decimal.js`.
- **Multi-moeda sem conversão automática** (DA-10): câmbio só como par de postings explícito.
- **Sem PII / sem IP bruto nos eventos** (TX-5/DA-11): `Geo` é derivado e mínimo.
- **Cascata é a autoridade final** (DA-3): Override → Contract → Remnant → impressão em branco; a IA só re-rankeia dentro de cada estrato.
- **Compatibilidade BACKWARD obrigatória** no schema de eventos (TX-1).
