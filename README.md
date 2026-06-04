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
| **Gate** | Golden tests CA-mapeados (85 casos) + harness shadow/dual-run + tolerâncias | [tests/parity/](tests/parity/) | §5 (cutover) |
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

### ⏭️ Pendente da Fase 3

**K8** (promoção do deep ranking sob **uplift A/B + kill-switch**) segue **gated por
tráfego real** — o código está pronto desde K1 (flag default-off); a promoção espera o
número de uplift sobre o GBDT, que depende do cutover de infra da Fase 2. A **habilitação
de AEV/BND** segue **gated pela spec de produto** (`scale`/classificação/supply — CHECK
estrutural impede habilitar sem `scale`). **Pré-condições de go-live** dos trilhos vivos:
chaves reais via OpenBao, cifra KMS-envelope das colunas de PII do Travel Rule antes de
popular nomes, FQDNs reais das células, e o adapter Postgres real do BFF de pagamentos.

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
