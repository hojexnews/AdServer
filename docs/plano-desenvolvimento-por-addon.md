# Plano de Desenvolvimento por Addon — AdServer (Hojex News)

> **Gerado por fan-out multiagente** (13 subagentes: 10 engenheiros-donos + 2 guardiões transversais + tech-lead) · 2026-07-14  
> **Estado-âncora:** Fases 0–3 **código-completas e provadas na `main`**; o que resta é **ativação de infra/tráfego/spec viva** — ver [§0](#0-sumário-executivo).  
> **Como cada decisão se ancora:** cada etapa cita os IDs de seção verificados em `docs/documentacao-tecnica.md` (DA-x, §4.x, CA-x), `docs/stack-tecnologico.md` (TX-x, §2.x, §3, §4, §5) e `docs/adr/000{1..4}` (incrementos I/J/K).

**Legenda de status:** ✅ concluída (código na `main`, gate-verificado) · → próxima / ◻ pendente (código-endereçável agora, sem infra) · ⏳ gated (bloqueado por infra viva ou gatilho mensurável) · ◐ em-andamento.

---

## Índice

- [§0 Sumário executivo](#0-sumário-executivo)
- [§1 Sequenciamento e eixos paralelos](#1-sequenciamento-e-eixos-paralelos)
- [§2 Dependências entre addons](#2-dependências-entre-addons)
- [§3 Planos por addon](#3-planos-por-addon)
  - [§3.1 Contratos de eventos](#31) — `schema-contracts-steward` · 11 etapas
  - [§3.2 Motor de decisão e veiculação (hot path Go)](#32) — `decision-engine-engineer` · 9 etapas
  - [§3.3 Plataforma de dados, telemetria e analytics (Redpanda → ClickHouse → Iceberg; fraude/IVT na ingestão)](#33) — `data-platform-engineer` · 9 etapas
  - [§3.4 IA / Deep Learning para otimização (ranking + fraude)](#34) — `ml-optimization-engineer` · 15 etapas
  - [§3.5 Copiloto de IA para anunciantes (Claude + LangGraph)](#35) — `copilot-llm-engineer` · 13 etapas
  - [§3.6 Front-end self-service do anunciante + BFF](#36) — `frontend-bff-engineer` · 13 etapas
  - [§3.7 Pagamentos multi-trilho (fiat + cripto + AEV/BND)](#37) — `payments-crypto-engineer` · 12 etapas
  - [§3.8 Infra, segurança, observabilidade e conformidade (plataforma-base)](#38) — `platform-infra-engineer` · 10 etapas
  - [§3.9 Dinheiro: tipo canônico, Asset Registry, ledger double-entry, billing](#39) — `money-ledger-guardian` · 12 etapas
  - [§3.10 Paridade e testes (golden tests + shadow-traffic + dual-run contábil)](#310) — `parity-golden-test-guardian` · 14 etapas
- [§4 Malha de gates transversais](#4-malha-de-gates-transversais)
- [§5 O próximo plano — Onda de Ativação (G0→G4) + Sucessores (S1→S8)](#5-o-próximo-plano)
- [§6 Ressalvas de coerência doc↔código](#6-ressalvas-de-coerência-doccódigo)

---

## 0. Sumário executivo

#### Estado global — main ESGOTADA em código, aberta em ativação

As Fases 0–3 estão **código-completas e provadas na `main`**, com todos os gates de merge verdes. O que resta **não é engenharia represada**: é (a) um punhado de itens código-endereçáveis de pré-condição que não dependem de infra, (b) a **ativação de go-live** (transformar código-completo em produção sob infra viva, seguindo `docs/ops/go-live-runbook.md`), e (c) sucessores pós-Fase-3 que só destravam **sob gatilho mensurável** — nunca por aspiração.

Inventário por addon (todos código-completos):
- **Contratos/Schema** (`schema-contracts-steward`): 5 pacotes proto verdes (common/telemetry/decision/money/payments); `buf lint/breaking/make verify` verdes contra `main`. O ruído do `proto-gen-check` (falso-positivo por bump de plugin remoto) foi **resolvido na 20ª onda** (E8: pin de versão dos plugins remotos em `buf.gen.yaml`).
- **Motor de decisão** (`decision-engine-engineer`): I0/I2/I5 + fiação do ranker (J0/J1/K1) atrás de flags default-off; 5 goldens CA-2..CA-6 completos. **Itens de código de G0 FECHADOS:** hot-reload GeoLite2 (E5, 18ª onda) e HOT-1/HOT-3 (E6, 17ª onda — RankResult por-request).
- **Plataforma de dados** (`data-platform-engineer`): Redpanda→ClickHouse→Iceberg (I3/J6/K2); I/O real dos jobs Iceberg é esqueleto com TODOs (E8).
- **ML** (`ml-optimization-engineer`): J0–J6 + K1/K2; **itens de código de G0 FECHADOS:** HOT-1/HOT-3 (E10, 17ª onda, espelha decision-engine) e ONNX Runtime nativo (E11, 19ª onda — `OnnxInferencer` real sob build-tag hermético; **não mais `StubInferencer`**).
- **Copiloto** (`copilot-llm-engineer`): J5 completo + hardening; 100% dos stubs são infra viva (ANTHROPIC_API_KEY, AsyncPostgresSaver, Redis, embeddings, Langfuse, C2PA/SynthID).
- **Front/BFF** (`frontend-bff-engineer`): I4/J5/K7; **dívidas de código de G0 FECHADAS:** middleware fail-closed (E9, 22ª onda) e alinhamento de stack §2.5 (E10, 23ª onda — Next 16.2.10 + React 19.2.7 + eslint flat + a11y-ci axe/`puppeteer-core`; Zustand e Vercel AI SDK v5 diferidos c/ gatilho documentado).
- **Pagamentos** (`payments-crypto-engineer`): K0/K3–K7 + hardening; sem código pendente.
- **Ledger/Money** (`money-ledger-guardian`): I1/K0/K3 + billing; sem código pendente.
- **Infra** (`platform-infra-engineer`): plataforma-base validada offline; os 2 gaps de código (`policy-aml-kyc.hcl` faltante e item 4 do mandato cosign/SBOM/Trivy/Falco só 25% coberto) foram **fechados na 21ª onda** (E8: pipeline supply-chain + cosign keyless + Falco). Na verificação do gate do E8 descobri e corrigi um **falso-positivo pré-existente**: `make platform-validate` estava vermelho na main (httproute com filtro `RequestTimeout` inexistente, otel-collector.yaml varrido pelo kubeconform, e policies das células puladas como inválidas pelo kyverno 1.13.4) — agora **genuinamente verde**.
- **Paridade** (`parity-golden-test-guardian`): suíte + harnesses de shadow/dual-run código-completos; execução real é 100% infra.

#### Como ler o plano por addon

Cada plano é uma **escada de estágios (E1…En)** com o mesmo formato de leitura:
1. **`status: concluída`** → código na `main`, gate-verificado. Não reabrir sem ADR.
2. **`status: próxima/pendente`** → código-endereçável **agora**, sem infra externa (ex.: HOT-1/HOT-3, hot-reload .mmdb, fail-closed middleware, cosign real). São os únicos "trabalhos de teclado" restantes.
3. **`status: gated`** → bloqueado por **infra viva** (cluster/OpenBao/KMS/Iceberg real) **ou** por **gatilho mensurável** (uplift A/B, AUM, gargalo de escrita, liquidez, spec de produto). Nunca antecipar.

A leitura correta separa rigorosamente **"pendência de código"** (raro, endereçável) de **"pendência de ativação"** (infra) e **"pendência de gatilho"** (medição/spec). Confundir os três é o erro que a regra de ouro proíbe: nenhuma tecnologia pesada (Flink, Triton/GPU, TigerBeetle, Fireblocks, oráculo, Feast) entra sem o número que a destrava anexado a um ADR sucessor.

---

## 1. Sequenciamento e eixos paralelos

#### Eixos paralelos, ordem e dependências entre addons

##### Eixo 0 — Contrato como raiz de tudo (já fechado)
`proto/` + `gen/` (schema-contracts-steward) é a **raiz do grafo**: todo addon consome o envelope/Money/Decision/PaymentEvent. Ordem histórica já cumprida: **E1 (envelope+Money) → E2 (telemetria) / E3 (decision+propensão) / E4 (Money prosa) → E5 (CI) → E6 (payments/v1, K0)**. Ref: `TX-1`, `ADR-0002 §C` (gen versionado), `ADR-0004 §H (K0)`.

##### Fase 1 — sequência canônica de build (ADR-0002 §D)
**I0 ∥ I1 → I2 → I3 → I4**, com o wiring local **I5** (definido no README, não no ADR — ver doc_ref_issues). O motor de decisão (I0/I2) e o ledger/config (I1) rodam em paralelo por não compartilharem arquivos; I2 é o ponto de junção. **Nenhum cutover antes de I4 com golden+shadow+dual-run dentro da tolerância** (ADR-0002 §D, `parity-golden-test-guardian` é o dono do gate).

##### Fase 2 — J0 é gate duro, depois paralelismo (ADR-0003 §G)
**J0 (propensão instrumentada no hot path, dono decision-engine) é pré-requisito absoluto de todo ML** — sem loop de atribuição fechado, OPE é inválido (`propensity-logging.md §5`). Após J0: **J1 ∥ J2 ∥ J5** (ranker Go+sidecar / treino Python / copiloto). **J3 e J4 são pontos de junção** (ligam ranker treinado ao hot path sob A/B). **J6** fecha (pacing + fraude na ingestão). O ranker **só serve tráfego real em J4** e **só com uplift A/B provado**.

##### Fase 3 — dois eixos disjuntos (ADR-0004 §H)
- **Eixo IA:** K1 (deep scaffolding, default-off) + K2 (fraude não-supervisionada). **K8 (promoção do deep) é gated por tráfego real do cutover da Fase 2** — código pronto em K1, promoção espera o número.
- **Eixo cripto/pagamentos:** **K0 é gate do eixo** (ChainConnector + células + Asset Registry AEV/BND + proto). Depois **K0→K3→{K4,K5}→{K6,K7}**. K3 depende de K0 (Asset Registry vivo); K5 de K3 (ledger); K6 de K5; K7 de K4+K5.
- Os dois eixos **não compartilham arquivos** (Python ML/Go-decision vs. Go-payments/db-ledger/proto-payments/cells); unidos só por gates de merge comuns.

##### O eixo de ATIVAÇÃO (cutover) que atravessa todos os addons
A ordem de destrave em produção é rígida:
1. **platform-infra E9 (cutover de infra)** aplica `platform/` em cloud sob aprovação humana → destrava **todas** as etapas "Ativação em produção" (`E7`-`E11` conforme addon).
2. **Cutover de infra destrava paridade:** `parity` E11 (shadow) + E12 (dual-run) só rodam com `platform/` viva + Revive legado acessível → E13 (veredito único de cutover Fase 1).
3. **Cutover destrava ML/copiloto:** decision-engine E8 e ml E12 (ligar RANKER/AB/SHADOW/DEEP) exigem tráfego real do cutover; copiloto E10 exige credenciais vivas.
4. **Tráfego real da Fase 2 destrava K8** (promoção do deep sob uplift A/B).

##### Dependências ENTRE addons (resumo dirigido)
- **data-platform → schema:** `topics.yaml` e raw tables não podem divergir do envelope (TX-1); schema-steward assina o gate (data E1).
- **ml → decision-engine:** J0 (propensão) é pré-requisito de todo treino (ADR-0003 J0); `ml/features` é consumido pelo `internal/ranker` (anti-skew, parity E7).
- **ml → data-platform:** treino pCTR/pCVR lê Iceberg real (pós-cutover); `ml/fraud` alimenta a marcação IVT do data (J6/K2) **antes** do StatsHourly/billing.
- **money-ledger → data-platform:** billing reconcilia **exclusivamente** contra Iceberg (nunca ClickHouse); IVT deve marcar antes da consolidação.
- **payments → money-ledger:** K3+ usa `internal/ledger` + Asset Registry; nenhuma captura grava saldo direto.
- **payments/copiloto/front → platform-infra:** segredos (OpenBao/KMS), células PCI/AML-KYC, FQDNs.
- **parity ← decision-engine (HOT-1/HOT-3):** parity E14 **veta** a promoção K8 até o fix de RankResult por-request; pré-condição de código, não só de infra.

---

## 2. Dependências entre addons

- data-platform (topics.yaml + raw tables) DEPENDE DE schema-contracts-steward (envelope Protobuf não pode divergir da chave de dedupe event_id nem do envelope) — motivo: gate TX-1 BACKWARD, ref data addon E1 gate + TX-1.
- ml-optimization (todo treino/OPE) DEPENDE DE decision-engine (J0: propensity/exploration_policy/epsilon/candidates[]/ml_fail_open instrumentados no hot path) — motivo: sem loop de atribuição fechado o OPE é inválido, ref ADR-0003 §G (J0 gate duro) + propensity-logging.md §5.
- decision-engine (internal/ranker/featurize.go) DEPENDE DE ml-optimization (ml/features/spec/feature_spec.yaml, função de featurização única) — motivo: anti-skew treino↔serving byte-a-byte (hash twmb/murmur3), ref ADR-0003 §D + parity addon E7.
- ml-optimization (treino pCTR/pCVR real) DEPENDE DE data-platform (Iceberg com dados reais pós-cutover) — motivo: modelo de produção treina sobre o lakehouse, não sobre sample sintético, ref ADR-0003 §G (J2) + data addon E8 (desbloqueia treino/OPE).
- data-platform (marcação IVT antes do StatsHourly) DEPENDE DE ml-optimization (ml/fraud GBDT supervisionado J6 + não-supervisionado K2) — motivo: TX-6 exige faturar só tráfego válido, ref ADR-0003 §G (J6) + ADR-0004 §H (K2).
- money-ledger (billing_batch_hourly) DEPENDE DE data-platform (Iceberg como fonte de verdade + filtro IVT definitivo) — motivo: faturamento reconcilia contra o lakehouse, nunca contra o streaming, ref DA-7 + data addon E7 + money addon E5.
- payments-crypto (K3 ledger cripto / K5 Safe) DEPENDE DE money-ledger (internal/ledger RecordEntry + Asset Registry vivo, scale autoritativo) — motivo: nenhuma captura grava saldo direto, par de postings idempotente, ref ADR-0004 §D/§H (K3) + money addon E4.
- payments-crypto + money-ledger (habilitar AEV/BND) DEPENDE DE schema-contracts-steward (Money.scale no fio) e de SPEC DE PRODUTO (decimais oficiais) — motivo: campo já existe no proto (sem migração), mas enabled=true é travado por CHECK até o scale oficial, ref ADR-0004 §E.2 + schema addon E9 + money addon E10.
- frontend-bff (BFF de pagamentos K7) DEPENDE DE money-ledger (PostgresPaymentsAdapter + RLS) e payments-crypto (status via ledger) — motivo: Money como string DECIMAL, cripto fora do cliente, sem IDOR, ref ADR-0004 §H (K7) + TX-2/TX-3.
- copiloto (proxy SSE + tenant server-side) DEPENDE DE frontend-bff (bff/src/routers/copilot.ts injeta tenant_id, protege a chave Claude) — motivo: TX-3, o BFF é a única fronteira voltada ao cliente, ref ADR-0003 §C (J5).
- copiloto (simulate_forecast read-only) DEPENDE DE ml-optimization (ranker-sidecar / modelo pCTR como única fonte do número) — motivo: o LLM nunca produz o número, só verbaliza com incerteza, ref ADR-0003 §C/§D.
- TODAS as etapas 'Ativação em produção' (decision E7, data E8, ml E12, copiloto E10, front E11, payments E7, money E9, schema E9) DEPENDEM DE platform-infra (E9: platform/ aplicada em cloud + OpenBao/KMS + FQDNs) — motivo: swap stub→real e segredos vivos, ref go-live-runbook §1-§6.
- parity (E11 shadow / E12 dual-run) DEPENDE DE platform-infra (cutover de infra) + decision-engine (motor Go real) + data-platform (Iceberg real) + Revive legado acessível — motivo: gate de cutover da Fase 1 exige os quatro simultâneos, ref ADR-0002 §D + parity addon E11/E12.
- parity (E14 veto da promoção K8) DEPENDE DE decision-engine (E6: fix HOT-1/HOT-3, RankResult por-request) — motivo: pré-condição de código; sem ela o OPE que decide a promoção é enviesável sob concorrência, ref README HOT-1/HOT-3 + ADR-0003 §G (J3/J4).
- decision-engine E8 + ml E12 (ligar RANKER/AB/SHADOW/DEEP) DEPENDEM DE parity (E13 cutover Fase 1 verde) e DE decision-engine E6/E7 (HOT-1/HOT-3 + infra real) — motivo: nenhum flag de ML liga sob tráfego real antes do gate de paridade e da correção de concorrência, ref ADR-0003 §G + go-live-runbook §7.
- ml-optimization (K8 promoção do deep) DEPENDE DE decision-engine E8 (tráfego real da Fase 2 já cutada) — motivo: uplift A/B só é mensurável sob tráfego real; Triton/GPU nunca por aspiração, ref ADR-0004 §A/§H (K8).
- payments-crypto (E9 Fireblocks / E10 chain não-EVM / E11 oráculo) e money-ledger (E11 TigerBeetle) DEPENDEM DE payments-crypto E7 (produção viva para medir AUM/liquidez/gargalo) — motivo: cada sucessor só abre ADR com o número/spec medido, ref ADR-0004 §C/§D/§E.4/§E.1.
- platform-infra E8 (Dockerfiles de produção por serviço para cosign/SBOM/Trivy) DEPENDE DOS donos de camada (decision/data/ml/payments/frontend) — motivo: o pipeline/template de CI é da infra, mas a imagem de cada app é do dono da camada, ref mandato item 4 + platform addon E8 blocker.

---

## 3. Planos por addon

<a id="31"></a>

### 3.1 Contratos de eventos — Schema Registry Protobuf/Buf (proto/ + contracts/)

**Subagente-dono:** `schema-contracts-steward`  
**Camada de documentação:** stack TX-1 (contrato de eventos único) + docs-técnica §4.1/§4.5/§4.7/§4.9 + CA-6 + ADR-0002 (layout) + ADR-0004 §G/§H·K0 (proto/adserver/payments/v1)  
**Caminhos:** `proto/buf.yaml` · `proto/buf.gen.yaml` · `proto/README.md` · `proto/adserver/common/v1/envelope.proto` · `proto/adserver/telemetry/v1/events.proto` · `proto/adserver/decision/v1/decision.proto` · `proto/adserver/money/v1/money.proto` · `proto/adserver/payments/v1/payments.proto` · `gen/go/adserver` · `gen/ts/adserver` · `contracts/README.md` · `contracts/money/money-type.md` · `contracts/money/asset-registry.md` · `contracts/money/asset-registry.seed.csv` · `contracts/lint/no-float.md` · `contracts/telemetry/propensity-logging.md` · `.github/workflows/buf.yml` · `Makefile`  

**Estado atual.** Registry código-completo e verde: rodei ao vivo `buf lint`/`buf format --diff --exit-code`/`buf breaking --against main`/`make verify` na raiz — todos passam sem diff contra `main` (5 pacotes: common, telemetry, decision, money, payments; envelope com os 4 campos críticos; Money int64+scale; Decision/Candidate/ExplorationPolicy para OPE; payments/v1 do K0 do ADR-0004 gerado em Go+TS). Isso fecha o mandato de TX-1 para Fases 0-1-2-3. O achado de staleness gate — `make proto-gen-check` falhava só nos artefatos TS por bump de versão do plugin remoto `protoc-gen-es` (v2.12.0→v2.12.1 no header `@generated by`), não por drift de schema — foi **corrigido na 20ª onda (E8)** fixando a versão dos plugins remotos em `buf.gen.yaml` (`protocolbuffers/go:v1.36.11`, `bufbuild/es:v2.12.0`); regeneração byte-idêntica ao versionado, gate verde e provado não-tautológico (drift real de `.proto` ainda reprova).

| Etapa | Título | Status | Subagente | Âncoras de doc | Bloqueador |
|---|---|---|---|---|---|
| `E1` | Fundações do registry — buf.yaml/buf.gen.yaml + envelope universal + Money no fio | ✅ concluída | `schema-contracts-steward` | `TX-1` · `TX-2` · `DA-10` · `TX-5` · `DA-11` · `DA-3` | — |
| `E2` | Telemetria volumétrica (AdRequest/Impression/Click/Conversion) | ✅ concluída | `schema-contracts-steward` | `§4.7` · `DA-8` · `TX-5` · `DA-11` · `TX-6` · `CA-6` | — |
| `E3` | Decision log + propensão — fecha o loop de atribuição/OPE | ✅ concluída | `schema-contracts-steward` | `TX-1` · `stack §2.3` · `TX-4` · `DA-3` · `propensity-logging.md` | — |
| `E4` | Contratos em prosa cross-cutting de dinheiro (Money/Asset Registry/anti-float) | ✅ concluída | `schema-contracts-steward` | `TX-2` · `DA-10` · `§2.6` · `§3` | — |
| `E5` | CI de contrato: buf lint/format/breaking + gates de geração | ✅ concluída | `schema-contracts-steward` | `TX-1` · `.github/workflows/buf.yml` | — |
| `E6` | proto/adserver/payments/v1 — trilho de pagamento multi-trilho (K0, ADR-0004 §G/§H) | ✅ concluída | `schema-contracts-steward` | `ADR-0004 §G` · `ADR-0004 §H (K0)` · `TX-1` · `TX-2` · `DA-10` · `DA-11` | — |
| `E7` | Gate contínuo de contrato em incrementos consumidores (revisão cross-camada) | ✅ concluída | `schema-contracts-steward` | `TX-2` · `CA-3` · `README.md (14ª onda)` | — |
| `E8` | Corrigir o falso-positivo de `proto-gen-check` (drift de versão de plugin remoto, não de schema) | ✅ concluída (20ª onda) | `schema-contracts-steward` | `TX-1` · `.github/workflows/buf.yml` · `Makefile (proto-gen-check)` | — |
| `E9` | Ativação em produção do registry sob tráfego/spec reais (pós-código, sob gatilho) | ⏳ gated | `schema-contracts-steward` | `TX-1` · `ADR-0004 E.2` · `go-live-runbook.md` | infra real (cluster/produção aplicando platform/) + spec de produto (scale/decimais oficiais de AEV/BND, ADR-0004 E.2) — nenhum dos dois é resolvido por código do registry |
| `E10` | Sucessor pós-Fase-3: eventos near-real-time/Flink no schema (sob gatilho ADR-0001) | ⏳ gated | `schema-contracts-steward` | `ADR-0001 (Gatilho de reabertura)` · `TX-1` · `§2.2` | tráfego real medido que dispare o gatilho do ADR-0001 — hoje não confirmado, portanto sem trabalho de schema pendente |
| `E11` | Sucessor pós-Fase-3: multi-touch attribution no schema (sob gatilho ADR-0002 §B.7) | ⏳ gated | `schema-contracts-steward` | `ADR-0002 §B.7 (Gatilho de reversão)` · `TX-1` · `§4.9` | demanda comercial por multi-touch ainda não registrada — hoje o schema atual (last-click, `decision_id`+`occurred_at`) já é suficiente e não pede mudança |

<details><summary><strong>Detalhamento das etapas</strong> (objetivo · tarefas · gate · dependências)</summary>

##### E1 · Fundações do registry — buf.yaml/buf.gen.yaml + envelope universal + Money no fio — ✅ concluída

Estabelecer o schema registry Protobuf-first com lint STANDARD+COMMENTS, breaking WIRE_JSON e o envelope que fecha o loop de atribuição, entregues na Fase 0.

- buf.yaml (v2, lint STANDARD+COMMENTS, breaking WIRE_JSON, enum_zero_value_suffix=_UNSPECIFIED)
- buf.gen.yaml (Go source_relative → gen/go; TS via buf.build/bufbuild/es → gen/ts)
- adserver.common.v1.Envelope: tenant_id, event_id, decision_id, model_version, occurred_at, schema_version, source + reserved 8-15
- adserver.common.v1.Geo (sem PII, sem IP bruto) e enum ServedTier (cascata DA-3)
- adserver.money.v1.Money: asset_code + int64 amount (minor units) + uint32 scale, reserved 4-7

**Subagente:** `schema-contracts-steward` · **Doc:** `TX-1` · `TX-2` · `DA-10` · `TX-5` · `DA-11` · `DA-3` · **Gate:** buf lint + buf breaking verdes (Fase 0) — verificado ao vivo nesta sessão contra `main`: 0 violações · **Depende de:** —

##### E2 · Telemetria volumétrica (AdRequest/Impression/Click/Conversion) — ✅ concluída

Modelar os quatro eventos de medição do §4.7/DA-8 embutindo o envelope universal, sem PII, servindo de contrato para collectors lg/ck/ct e ClickHouse/Iceberg.

- adserver.telemetry.v1.AdRequest (zone_id, site_id, Geo, user_agent, referer_url, cachebuster, custom_vars mapa opaco)
- Impression (campaign_id/banner_id/zone_id/served_tier/billable/blank/ivt_status)
- Click (campaign_id/banner_id/zone_id/dest_url)
- Conversion (campaign_id/banner_id/conversion_value:Money/attribution_decision_id/deduplicated)
- reserved ranges em todas as mensagens para evolução aditiva futura

**Subagente:** `schema-contracts-steward` · **Doc:** `§4.7` · `DA-8` · `TX-5` · `DA-11` · `TX-6` · `CA-6` · **Gate:** buf lint + buf breaking verdes; consumo confirmado pelos golden tests de paridade (parity-golden-test-guardian) · **Depende de:** E1

##### E3 · Decision log + propensão — fecha o loop de atribuição/OPE — ✅ concluída

Modelar Decision/Candidate/ExplorationPolicy e o contrato em prosa de propensão, pré-requisito estrutural de todo ML da Fase 2 (IPS/SNIPS/DR).

- enum ExplorationPolicy (DETERMINISTIC/EPSILON_GREEDY/THOMPSON/LINUCB)
- message Candidate (campaign_id/banner_id/tier/score double/propensity double/served)
- message Decision (envelope, zone_id, served_tier, propensity∈(0,1], exploration_policy, epsilon, candidates[], candidate_count, ml_fail_open)
- contracts/telemetry/propensity-logging.md: modelo de dois registros (Decision normalizado + join por decision_id em lg/ck/ct), obrigações de instrumentação por endpoint, semântica de propensão e fail-open

**Subagente:** `schema-contracts-steward` · **Doc:** `TX-1` · `stack §2.3` · `TX-4` · `DA-3` · `propensity-logging.md` · **Gate:** buf lint + buf breaking verdes; contrato de propensão publicado e referenciado pelo README/proto README · **Depende de:** E1

##### E4 · Contratos em prosa cross-cutting de dinheiro (Money/Asset Registry/anti-float) — ✅ concluída

Garantir coerência semântica do tipo Money em todas as fronteiras (evento→ledger→BFF→UI) e a política de lint anti-float (hoje **6 guards** cobrindo Proto/Go/TS/Python/SQL — ver `contracts/lint/no-float.md` §Escopo), sem editar proto/ (fronteira de propriedade com money-ledger-guardian).

- contracts/money/money-type.md: representação por fronteira (fio/Postgres/BFF/front), invariantes, ROUND_HALF_EVEN, mapa de tipos por linguagem
- contracts/money/asset-registry.md + seed.csv: schema plugável, AEV/BND como linhas TBD (enabled=false)
- contracts/lint/no-float.md: escopo + scripts `scripts/ci/no-float-{go,py,sql}.sh` + regras ESLint de dinheiro _(entrega original de E4; o escopo descrito ali então — "restrito a money/ledger/billing/payments/asset_registry/migrations" — **não vale mais**: as ondas 27ª–30ª o levaram a default-deny. Escopo vigente: `contracts/lint/no-float.md` §Escopo, hoje com 6 guards)_
- contracts/README.md como índice e fronteira explícita ("referenciam o schema, nunca editam proto/")

**Subagente:** `schema-contracts-steward` · **Doc:** `TX-2` · `DA-10` · `§2.6` · `§3` · **Gate:** no-float verde nos 6 guards `scripts/ci/no-float-*.sh` (make verify, com sentinela `NO_FLOAT_SCRIPTS_EXPECTED := 6`); money-ledger-guardian valida a semântica contábil consumidora · **Depende de:** E1

##### E5 · CI de contrato: buf lint/format/breaking + gates de geração — ✅ concluída

Materializar TX-1 em CI: nenhum PR quebra compat BACKWARD; `make verify` espelha a CI localmente e é hermético/offline.

- .github/workflows/buf.yml (bufbuild/buf-action, lint+format+breaking contra main#subdir=proto, fetch-depth 0)
- Makefile: proto-lint/proto-format/proto-format-check/proto-breaking/proto-build/proto-gen/proto-gen-check + verify agregando proto-lint+format-check+build+breaking+no-float
- proto-gen-check como job de CI separado (depende de rede/plugins remotos, propositalmente fora de `verify` que é hermético)

**Subagente:** `schema-contracts-steward` · **Doc:** `TX-1` · `.github/workflows/buf.yml` · **Gate:** CI verde no PR; `make verify` verde localmente (confirmado ao vivo nesta sessão) · **Depende de:** E1, E2, E3, E4

##### E6 · proto/adserver/payments/v1 — trilho de pagamento multi-trilho (K0, ADR-0004 §G/§H) — ✅ concluída

Estender o registry para o eixo cripto/pagamentos da Fase 3 sem quebrar compat e sem PII, reusando Envelope e Money — pré-requisito de K0 antes de qualquer código de services/payments ou internal/chainconnector.

- enum PaymentRail (STRIPE/ASAAS_PIX/MERCADOPAGO/SAFE_MULTISIG/FIREBLOCKS_MPC)
- enum PaymentStatus (PENDING/AUTHORIZED/CAPTURED/REVERSED/FAILED) e PostingKind (DEPOSIT/PAYOUT/REVERSAL/FX_EXCHANGE)
- message PaymentEvent (envelope, payment_id, rail, status, amount:Money, confirmations, tx_hash, block_number, chain_id, pix_end_to_end_id, provider_confirmed_at)
- message DepositFinalizedEvent e ReversalEvent (finalidade via webhook do custodiante; estorno sempre novo par de postings)
- message LedgerPostingRef (referência imutável evento↔journal_entry, sem duplicar o posting em si)
- gerar e versionar gen/go + gen/ts de payments/v1 (achado fechado nesta trilha: artefatos gerados faltantes)

**Subagente:** `schema-contracts-steward` · **Doc:** `ADR-0004 §G` · `ADR-0004 §H (K0)` · `TX-1` · `TX-2` · `DA-10` · `DA-11` · **Gate:** buf breaking verde contra main; money-ledger-guardian confere Money/minor-units sem float; privacy-compliance-auditor confere ausência de PII (endereço só da plataforma, nunca do anunciante) · **Depende de:** E1, E4

##### E7 · Gate contínuo de contrato em incrementos consumidores (revisão cross-camada) — ✅ concluída

Atuar como schema-contracts-steward em achados de paridade contrato↔implementação levantados por varreduras de outras camadas (BFF, DB), garantindo que CHECKs de banco e schemas Zod não divirjam do contrato Protobuf/Money.

- Revisão do achado `updateBanner` nulificando `asset_url`/`dest_url` (violaria `banners_asset_xor_chk`/`banners_dest_url_chk`) — fechado na 14ª onda
- Auditoria periódica de paridade contrato↔CHECK em cada onda de varredura (README registra 2 achados fechados)
- Manter proto/README.md e contracts/README.md sincronizados com qualquer pacote novo

**Subagente:** `schema-contracts-steward` · **Doc:** `TX-2` · `CA-3` · `README.md (14ª onda)` · **Gate:** achados fechados e registrados no README; sem regressão de paridade contrato↔schema · **Depende de:** E4, E6

##### E8 · Corrigir o falso-positivo de `proto-gen-check` (drift de versão de plugin remoto, não de schema) — ✅ concluída (20ª onda)

Eliminou o ruído do gate de staleness de CI: ele falhava nos artefatos TS por bump de versão do `protoc-gen-es` remoto (v2.12.0→v2.12.1, só o header `@generated by` muda), o que poderia mascarar um drift real futuro se o time passasse a ignorar falhas do gate por hábito.

- ✅ Fixada a versão dos plugins remotos em `proto/buf.gen.yaml`: `buf.build/protocolbuffers/go:v1.36.11` e `buf.build/bufbuild/es:v2.12.0` (pin por versão — suportado pelo buf v2, confirmado ao vivo), para que `proto-gen-check` só falhe em drift real de schema. Regeneração byte-idêntica ao versionado (só `buf.gen.yaml` muda; `gen/` intacto)
- Pin por versão foi suportado → plugin local vendorizado não foi necessário (registrado como alternativa se o BSR algum dia remover as tags fixadas)
- ✅ Documentada em `proto/README.md` e no comentário do job de CI (`.github/workflows/buf.yml`) a distinção entre 'diff de schema' (deve falhar) e 'diff de versão de gerador' (não deve falhar o merge)
- Regeneração confirmada byte-idêntica: nenhum arquivo `gen/go`/`gen/ts` mudou (o pin bate exatamente com o header `@generated by` já versionado)

**Gate provado (20ª onda):** `schema-contracts-steward` aplicou; `make proto-gen-check` verde (`OK — gen/ esta em sync com proto/.`); `make verify` (proto-lint+format-check+build+breaking+no-float) verde; `buf lint/format/breaking/build` verdes. Verificação adversarial **`parity-golden-test-guardian` PASS** + **`tech-lead-architect` PASS** — ambos provaram que o gate **não virou tautológico**: injetar um campo real num `.proto` (ou corromper um `gen/`) sob o pin **ainda reprova** `proto-gen-check` (drift real detectado), revertido em seguida. Zero drift de schema; `buf breaking` limpo contra `main`.

**Subagente:** `schema-contracts-steward` · **Doc:** `TX-1` · `.github/workflows/buf.yml` · `Makefile (proto-gen-check)` · **Gate:** `make proto-gen-check` verde de forma estável (schema-contracts-steward é dono; platform-infra-engineer coadjuva se exigir infra de plugin local) · **Depende de:** E5, E6

##### E9 · Ativação em produção do registry sob tráfego/spec reais (pós-código, sob gatilho) — ⏳ gated

O contrato está pronto; o que falta é não-código — publicar/consumir em produção real e resolver os TBDs de produto que hoje travam campos específicos (AEV/BND scale) sem bloquear o resto do schema.

- Publicar o módulo no BSR (Buf Schema Registry) ou repositório interno equivalente quando houver múltiplos consumidores externos ao monorepo — hoje `push: false` na CI é suficiente (mono-repo)
- Quando a spec oficial de AEV/BND definir `scale`/decimais (E.2 do ADR-0004), atualizar apenas a linha do Asset Registry e materializar o valor no `Money.scale` do fio — sem migração de schema Protobuf (o campo já existe)
- Acompanhar o primeiro tráfego real em `services/payments`/`internal/chainconnector` para confirmar que `PaymentEvent`/`DepositFinalizedEvent`/`ReversalEvent` cobrem os casos observados; campos `reserved` já preparados para extensão aditiva
- Confirmar em produção que collectors/ClickHouse/BFF continuam decodificando o envelope sem quebra (dry-run de compat antes do cutover real da Fase 1/2)

**Subagente:** `schema-contracts-steward` · **Doc:** `TX-1` · `ADR-0004 E.2` · `go-live-runbook.md` · **Gate:** tech-lead-architect confirma cutover real (infra viva); money-ledger-guardian confirma `scale` oficial antes de habilitar AEV/BND · **Depende de:** E6, E8 · **Bloqueador:** infra real (cluster/produção aplicando platform/) + spec de produto (scale/decimais oficiais de AEV/BND, ADR-0004 E.2) — nenhum dos dois é resolvido por código do registry

##### E10 · Sucessor pós-Fase-3: eventos near-real-time/Flink no schema (sob gatilho ADR-0001) — ⏳ gated

Só se o gatilho mensurável do ADR-0001 disparar (latência do 'ao vivo' via ClickHouse deixar de ser suficiente, medido em produção), avaliar se o contrato de telemetria precisa de campos adicionais para suportar streaming stateful (ex.: chaves de particionamento explícitas para Flink, watermark hints).

- Monitorar o sintoma que reabre o ADR-0001 (frescor 'ao vivo' insuficiente, medido)
- Se disparado: avaliar aditivamente (campos novos com reserved consumidos, nunca renumeração) se Impression/Click/Conversion precisam de metadata de streaming
- Redigir ADR sucessor citando o número medido, coordenando com data-platform-engineer

**Subagente:** `schema-contracts-steward` · **Doc:** `ADR-0001 (Gatilho de reabertura)` · `TX-1` · `§2.2` · **Gate:** tech-lead-architect abre ADR sucessor só com número medido anexado; schema-contracts-steward só altera .proto após ADR aceito · **Depende de:** E9 · **Bloqueador:** tráfego real medido que dispare o gatilho do ADR-0001 — hoje não confirmado, portanto sem trabalho de schema pendente

##### E11 · Sucessor pós-Fase-3: multi-touch attribution no schema (sob gatilho ADR-0002 §B.7) — ⏳ gated

Só sob demanda comercial por crédito multi-touch (gatilho explícito do ADR-0002 §B.7, após a Fase 2 fechar o loop de propensão/pCVR), avaliar se o Decision/Conversion precisam de novos campos para modelo de atribuição position-based, sem migração do que já existe (`decision_id`+`occurred_at` já bastam para o parâmetro do job).

- Confirmar que o gatilho disparou: pCVR fechado (Fase 2, já código-completa) + demanda comercial explícita por multi-touch
- Se disparado: avaliar se basta ajuste de parâmetro no job de billing (sem tocar .proto) ou se multi-touch exige campo aditivo novo em Conversion (ex.: lista de decision_ids fracionados com peso) — nesse caso, campo novo com reserved, nunca renumeração
- Coordenar com money-ledger-guardian se pesos fracionários de atribuição tocarem faturamento (CPA)

**Subagente:** `schema-contracts-steward` · **Doc:** `ADR-0002 §B.7 (Gatilho de reversão)` · `TX-1` · `§4.9` · **Gate:** tech-lead-architect abre ADR sucessor citando a demanda comercial; parity-golden-test-guardian revalida que o schema atual (last-click 7d) não regride até a troca · **Depende de:** E9 · **Bloqueador:** demanda comercial por multi-touch ainda não registrada — hoje o schema atual (last-click, `decision_id`+`occurred_at`) já é suficiente e não pede mudança

</details>

**→ Próximo plano deste addon.** E8 **fechado na 20ª onda**: os plugins remotos foram fixados por versão em `buf.gen.yaml` (`buf.build/protocolbuffers/go:v1.36.11`, `buf.build/bufbuild/es:v2.12.0`), de modo que `make proto-gen-check` deixou de falhar por bump cosmético de versão do gerador (`protoc-gen-es` v2.12.0→v2.12.1, só o header `@generated by`) e passou a reprovar **apenas** drift real de schema — provado adversarialmente (injetar campo em `.proto` sob o pin ainda reprova o gate). A distinção 'diff de schema' vs 'diff de gerador' foi documentada em `proto/README.md` e no job de CI. Regeneração byte-idêntica ao versionado (nenhum `gen/` mudou). Não há mais item de código do schema registry pendente: E9 (ativação em produção real) e E10/E11 (near-real-time/Flink; multi-touch) permanecem 'gated' aguardando, respectivamente, infra viva aplicando platform/ + cutover real (E9) e os dois gatilhos mensuráveis explícitos dos ADR-0001/ADR-0002 §B.7 (E10/E11); o trabalho de schema em si (E1-E8) está código-completo, provado ao vivo (`buf lint`/`buf breaking`/`make verify`/`make proto-gen-check` verdes contra `main`) e sem diff pendente.

---

<a id="32"></a>

### 3.2 Motor de decisão e veiculação (hot path Go) — cascata, regras, capping, ad tag/telemetria, ponto de extensão do ranker

**Subagente-dono:** `decision-engine-engineer`  
**Camada de documentação:** stack §2.1 (motor de decisão / hot path)  
**Caminhos:** `internal/cascade` · `internal/rules` · `internal/capping` · `internal/geo` · `internal/clicktoken` · `internal/configload` · `internal/snapshot` · `internal/useragent` · `internal/ranker` · `internal/telemetry` · `services/decision` · `services/collector` · `tests/parity` · `docs/documentacao-tecnica.md` · `docs/stack-tecnologico.md` · `docs/adr/0002-fase-1-sequenciamento-e-layout.md` · `docs/adr/0003-fase-2-sequenciamento-ml-copiloto.md` · `docs/adr/0004-fase-3-sequenciamento-ia-avancada-cripto.md` · `docs/ops/go-live-runbook.md` · `README.md`  
**Incrementos fechados:** `I0 (esqueleto Go + cascata DA-3 + regras §4.6 + geo DA-9)` · `I2 (capping Redis+fail-safe DA-6 + clicktoken DA-8 + telemetria WAL+dedupe + collector lg/ck/ct/asyncjs/VAST)` · `I5 (loader Postgres→snapshot, wiring local, smoke E2E)` · `J0 (instrumentação de propensão/decision_id/model_version no hot path)` · `J1 (esqueleto internal/ranker + sidecar fail-open, flag default-off)` · `K1 (fiação mínima do deep ranker no internal/ranker/sidecar via model_version, flag default-off)` · `16ª onda (golden CA-3 — único golden que faltava na suíte de paridade)`

**Estado atual.** O hot path Go está código-completo: I0 (cascata Override>Contract>Remnant>blank, regras §4.6, snapshot, geo MaxMind in-memory), I2 (capping Redis fail-safe, clicktoken HMAC, telemetria WAL+dedupe, collector lg/ck/ct/asyncjs/VAST), I5 (loader Postgres→snapshot, wiring local) e a fiação do ponto de extensão de ML (J0/J1 ADR-0003, K1 ADR-0004 — re-ranker/deep atrás de flags default-off) estão na main com gates verdes (16 ondas de hardening, última fechando o golden CA-3 que faltava). A suíte de paridade tem os 5 golden CA-2/CA-3/CA-4/CA-5/CA-6 completos; achados reais da varredura (bug de fast-path do capping servindo sempre BLANK, WAL de telemetria perdendo o topic no replay, floor no billing) foram corrigidos na 12ª/15ª onda. Dois itens código-endereçáveis genuínos permanecem em aberto sem exigir infra: hot-reload do `.mmdb` do GeoLite2 sem restart (hoje só carrega uma vez no boot) e a correção HOT-1/HOT-3 (RankResult compartilhado via campo `last` em vez de por-request), documentada como pré-condição bloqueante antes de ligar `RANKER_ENABLED`/`AB_ENABLED`/`SHADOW_ENABLED` sob tráfego real.

| Etapa | Título | Status | Subagente | Âncoras de doc | Bloqueador |
|---|---|---|---|---|---|
| `E1` | Cascata em memória + regras de entrega + geo MaxMind (I0) | ✅ concluída | `decision-engine-engineer` | `DA-2` · `DA-3` · `DA-4` · `DA-9` · `§4.2` · `§4.6` · `CA-2` · `CA-4` · `ADR-0002 §A` · `ADR-0002 §D (I0)` | — |
| `E2` | Capping Redis fail-safe + ad tag + telemetria + collector (I2) | ✅ concluída | `decision-engine-engineer` | `DA-5` · `DA-6` · `DA-8` · `§4.4` · `§4.7` · `§4.8` · `CA-3` · `CA-5` · `CA-6` · `CA-8` · `ADR-0002 §D (I2)` | — |
| `E3` | Wiring local + suíte golden de paridade completa (I5 + 16ª onda) | ✅ concluída | `decision-engine-engineer` | `ADR-0002 §D (I4)` · `README.md (I5)` · `CA-2` · `CA-3` · `CA-4` · `CA-5` · `CA-6` · `stack §5 (risco 'reescrita Go divergir da semântica legada')` | — |
| `E4` | Fiação do ponto de extensão do ranker (ML/deep) dentro da cascata, atrás de flags default-off | ✅ concluída | `decision-engine-engineer` | `DA-3` · `TX-4` · `ADR-0002 §B.2` · `ADR-0003 §A` · `ADR-0003 §G (J0, J1)` · `ADR-0004 §A` · `ADR-0004 §H (K1)` | — |
| `E5` | Hot-reload do GeoLite2 (.mmdb) sem restart — fecha CA-9 do lado do motor | ✅ concluída (18ª onda) | `decision-engine-engineer` | `DA-9` · `CA-9` · `§4.10` | swap atômico do reader via `sync.RWMutex` (`internal/geo/maxmind.go` `Reload`) + poll de mtime no collector (`runGeoReloader`); prova `-race` (swap-in-place, DA-9 retém DB antigo em falha, 50×Resolve∥4×Reload); gate parity PASS (canário confirma o teste). A validação end-to-end ainda depende de um job externo de download/rotação automática do .mmdb com chave de licença MaxMind real (platform-infra-engineer) — o checkbox CA-9 permanece do lado da infra. |
| `E6` | Correção HOT-1/HOT-3 — RankResult por-request em vez de campo `last` compartilhado | ✅ concluída (17ª onda) | `decision-engine-engineer` | `TX-4` · `DA-3` · `ADR-0003 §G (J3, J4)` · `ADR-0004 §A` | fechada via `cascade.Engine.DecideWithRanker` + `internal/ranker/request.go` (RankResult por-request); prova `-race` em `internal/ranker/request_race_test.go`. |
| `E7` | Ativação em produção — cutover sob infra real (shadow-traffic + dual-run contábil) | ⏳ gated | `decision-engine-engineer` | `ADR-0002 §D ('nada de cutover antes de I4 com golden+shadow+dual-run')` · `CA-9` · `DA-9` · `go-live-runbook.md §1` · `go-live-runbook.md §5` · `go-live-runbook.md §6` · `go-live-runbook.md §7` | infra real: platform/ aplicada em cloud (EKS/Postgres/Redis/Redpanda/ClickHouse); chave de licença MaxMind real; tráfego real (ou volume equivalente) do Revive legado para o dual-run — nenhum destes é reproduzível neste ambiente. |
| `E8` | Ativação dos flags de ML sob tráfego real (RANKER/AB/SHADOW/DEEP_ENABLED) + suporte à promoção do deep (K8) | ⏳ gated | `decision-engine-engineer` | `DA-3` · `TX-4` · `ADR-0003 §G (J4)` · `ADR-0004 §A` · `ADR-0004 §H (K8)` · `go-live-runbook.md §7 (linha K8)` · `go-live-runbook.md §6 (checklist parity-golden-test-guardian)` | tráfego real com volume estatisticamente significativo para A/B (uplift sobre a cascata pura/GBDT); sem isso a promoção é recusada por design, não só por código. |
| `E9` | Reabertura do orçamento de latência (TX-4/ADR-0002 §B.2) sob número medido | ⏳ gated | `decision-engine-engineer` | `TX-4` · `ADR-0002 §B.2 (gatilho de reversão)` · `ADR-0003 §B (gatilho IPC > 2 ms p99)` · `ADR-0004 (gatilho global: deep não couber em 5–8 ms)` · `stack §2.1` | número de p99/IPC medido de forma sustentada em produção real sob tráfego real — o budget não se amplia por aspiração (regra de ouro), então esta etapa fica sem início até a medição existir. |

<details><summary><strong>Detalhamento das etapas</strong> (objetivo · tarefas · gate · dependências)</summary>

##### E1 · Cascata em memória + regras de entrega + geo MaxMind (I0) — ✅ concluída

Construir o núcleo determinístico da decisão: hierarquia Override>Contract>Remnant>impressão em branco, motor de regras §4.6 com anti-contradição, snapshot versionado de config e resolução de geo por MaxMind em memória — tudo O(1), sem ida à rede.

- internal/cascade: avaliação estrita Override>Contract>Remnant>blank; computeDeficit (pacing declarativo DA-4, sort por déficit desc dentro do estrato Contract)
- internal/rules: vetores Time/Site-URL/Geo-Country/Geo-City/Client-Useragent/Site-Variable, operadores is/is not/contains, AND/OR, Delivery Rule Sets, detecção de contradição
- internal/snapshot: modelo de config em memória (campanhas/banners/zonas/regras/caps) carregado por pull periódico do Postgres
- internal/geo (MaxMindResolver): resolução país/cidade via GeoLite2 .mmdb carregado em memória, fallback EmptyResolver se arquivo ausente
- services/decision: binário do motor, endpoint JSON de decisão

**Subagente:** `decision-engine-engineer` · **Doc:** `DA-2` · `DA-3` · `DA-4` · `DA-9` · `§4.2` · `§4.6` · `CA-2` · `CA-4` · `ADR-0002 §A` · `ADR-0002 §D (I0)` · **Gate:** parity-golden-test-guardian aprova CA-2 (tests/parity/ca2_cascade_golden_test.go) e CA-4 (ca4_rules_golden_test.go) verdes; go build/vet limpos. · **Depende de:** —

##### E2 · Capping Redis fail-safe + ad tag + telemetria + collector (I2) — ✅ concluída

Fechar o contrato de borda: frequency capping best-effort com fail-safe DA-6, click token HMAC server-side (DA-8), telemetria fire-and-forget com WAL+dedupe, e os endpoints lg/ck/ct/asyncjs com pixel 1×1, redirect 302 e VAST 4.x.

- internal/capping: cliente Redis (TTL session/clock), chave efêmera hash+salt+TTL, sobrescrita banner>campanha, abort fail-safe sem identificador estável (DA-6)
- internal/clicktoken: assinatura HMAC do dest_url, vínculo server-side do clique (DA-8), rejeição de token adulterado/forjado
- internal/telemetry: produtor fire-and-forget em lote, WAL local durável (replay reconstrói topic+key), dedupe idempotente por event_id
- internal/useragent: redução de UA a classe coarse (privacidade TX-5)
- services/collector: lg/ck/ct + asyncjs, pixel 1×1, redirect 302, pixel de conversão, VAST 4.x sem VPAID

**Subagente:** `decision-engine-engineer` · **Doc:** `DA-5` · `DA-6` · `DA-8` · `§4.4` · `§4.7` · `§4.8` · `CA-3` · `CA-5` · `CA-6` · `CA-8` · `ADR-0002 §D (I2)` · **Gate:** security-reviewer + privacy-compliance-auditor aprovam (HMAC no /ck, VAST sem injeção SSRF, UA reduzido a classe, IP descartado pós-geo, capping efêmero com TTL real via Lua INCR+PEXPIRE); parity-golden-test-guardian aprova CA-5 (ca5_capping_golden_test.go) e CA-6 (ca6_telemetry_golden_test.go). · **Depende de:** —

##### E3 · Wiring local + suíte golden de paridade completa (I5 + 16ª onda) — ✅ concluída

Fechar o loop administração→decisão localmente (sem cloud) e completar a suíte de golden tests que protege a semântica legada antes de qualquer cutover.

- internal/configload: loader Postgres→snapshot (papel adserver_loader BYPASSRLS), Assemble() mapeia creative_type→campo do Banner
- deploy/local + db/seed: docker-compose, seed demo, smoke E2E (BR→CONTRACT, US→REMNANT, zona desconhecida→BLANK)
- tests/parity: golden CA-2/CA-4/CA-5/CA-6 (I4) + golden CA-3 ausente fechado na 16ª onda (ca3_creatives_golden_test.go, cross-ref aos 11 testes do collector)
- harness de shadow-traffic (tests/parity/shadow) e dual-run contábil (tests/parity/dual_run) prontos para tráfego real

**Subagente:** `decision-engine-engineer` · **Doc:** `ADR-0002 §D (I4)` · `README.md (I5)` · `CA-2` · `CA-3` · `CA-4` · `CA-5` · `CA-6` · `stack §5 (risco 'reescrita Go divergir da semântica legada')` · **Gate:** parity-golden-test-guardian aprova a suíte completa (5/5 golden CA-mapeados, harness shadow/dual-run presente); make go-test -race verde em todos os pacotes deste addon. · **Depende de:** —

##### E4 · Fiação do ponto de extensão do ranker (ML/deep) dentro da cascata, atrás de flags default-off — ✅ concluída

Plugar o cliente Go do re-ranker (J0/J1) e o alvo de model_version do deep (K1) no ponto de extensão único dentro do estrato vencedor, com timeout duro + fail-open determinístico, sem furar DA-3 e sem ampliar o budget TX-4.

- services/decision: preenche Decision.propensity/exploration_policy/epsilon/candidates[]/ml_fail_open (J0); decision_id+model_version ponta-a-ponta
- internal/ranker: featurize.go (função única anti-skew, hash twmb/murmur3), ipc.go (UDS timeout duro), ranker.go/ab.go/guard.go/shadow.go/bandit_ranker.go/deep_flag.go — todos atrás de RANKER_ENABLED/AB_ENABLED/SHADOW_ENABLED/DEEP_ENABLED (default off)
- garantir que o re-rank ocorre estritamente dentro do Candidate.tier do estrato vencedor da cascata pura

**Subagente:** `decision-engine-engineer` · **Doc:** `DA-3` · `TX-4` · `ADR-0002 §B.2` · `ADR-0003 §A` · `ADR-0003 §G (J0, J1)` · `ADR-0004 §A` · `ADR-0004 §H (K1)` · **Gate:** parity-golden-test-guardian aprova: ranker/deep desligado E em fail-open reproduzem a cascata pura bit-a-bit (golden CA-2/CA-4/CA-5/CA-6 intactos); go-live-runbook.md §6 confirma fail-open determinístico (timeout duro retorna cascata pura). · **Depende de:** —

##### E5 · Hot-reload do GeoLite2 (.mmdb) sem restart — fecha CA-9 do lado do motor — ✅ concluída (18ª onda)

Substituir o carregamento único no boot (documentado como aceitável só para o MVP) por um mecanismo de hot-reload do arquivo .mmdb já atualizado em disco, sem derrubar o processo de decisão. **Entregue na 18ª onda:**

- internal/geo (`maxmind.go`): `*maxminddb.Reader` agora guardado por `sync.RWMutex` (não `atomic.Pointer` nu — o reader é mmap'd e `Close()` faz munmap; RWMutex garante que o reader antigo só é fechado após todos os `Lookup` em voo drenarem, eliminando use-after-munmap por construção); novo `Reload(dbPath)` troca atomicamente e mantém o reader anterior em falha (DA-9)
- collector (`main.go` `runGeoReloader`): poll periódico de mtime (env `GEOIP_RELOAD_INTERVAL`, default 1h) chama `Reload` quando o job externo substitui o arquivo — sem sinal do operador; atado ao ctx de shutdown; nunca toca IP (TX-5/DA-11)
- teste `-race` (`maxmind_reload_test.go`, fixtures via `mmdbwriter` só-teste, build hermético preservado): swap-in-place (o resolver antigo serve durante a troca, zero downtime), arquivo ruim/corrompido mantém o reader anterior, e 50×Resolve ∥ 4×Reload sem data race nem panic. Gate parity-golden-test-guardian **PASS** (canário: remover o RWMutex faz o teste travar com SIGSEGV — prova que o teste detecta o bug real)
- resta ao runbook/infra documentar o gatilho de rotação e prover a chave MaxMind real (job de download é do platform-infra-engineer)

**Subagente:** `decision-engine-engineer` · **Doc:** `DA-9` · `CA-9` · `§4.10` · **Gate:** parity-golden-test-guardian confirma zero regressão nos golden de geo/regras; critério CA-9 'arquivos GeoLite2 auto-atualizam sem intervenção manual' passa a ser satisfeito do lado do motor (o download/rotação do arquivo em si é um job externo de infra). · **Depende de:** E1 · **Bloqueador:** validação end-to-end depende de um job externo de download/rotação automática do .mmdb com chave de licença MaxMind real (platform-infra-engineer); o hot-reload em si é código-endereçável agora com um arquivo de teste local.

##### E6 · Correção HOT-1/HOT-3 — RankResult por-request em vez de campo `last` compartilhado — ✅ concluída (17ª onda)

Eliminar a limitação conhecida documentada em bandit_ranker.go (gate E4/J4, HOT-1) e shadow.go (gate J3, HOT-3): hoje uma única instância de BanditRanker/ShadowRanker compartilhada sob concorrência pode trocar propensity/model_version/decision_id/scores entre requests, enviesando o OPE que decide a promoção do modelo. Inerte enquanto os flags estão off, mas é pré-condição de código antes de ligá-los sob tráfego real.

- internal/ranker/ranker.go, bandit_ranker.go: fazer Rank() retornar o RankResult diretamente (ou usar um ranker por-request) em vez de armazenar em `last` e ler via LastResult() depois de cascade.Decide() retornar
- internal/ranker/shadow.go: substituir SetRequestContext (campo compartilhado) por passagem de decisionID/zoneID através de Rank()
- services/decision: adaptar o handler para consumir o resultado retornado por-request, preservando o timeout duro + fail-open (TX-4) e a ordem determinística de fallback (DA-3)
- adicionar teste de concorrência (-race, N goroutines simultâneas) que prove que request A nunca lê propensity/model_version de request B

**Subagente:** `decision-engine-engineer` · **Doc:** `TX-4` · `DA-3` · `ADR-0003 §G (J3, J4)` · `ADR-0004 §A` · **Gate:** parity-golden-test-guardian aprova: fix não altera semântica com flags off (golden/shadow intactos) e um novo teste -race prova atribuição correta por-request antes de ligar RANKER_ENABLED/AB_ENABLED/SHADOW_ENABLED. · **Depende de:** E4

##### E7 · Ativação em produção — cutover sob infra real (shadow-traffic + dual-run contábil) — ⏳ gated

Ligar o motor de decisão Go contra infraestrutura real (Postgres/Redis/Redpanda/ClickHouse aplicados via platform/), com o GeoLite2 real e MaxMind license key, e provar paridade com o Revive legado via shadow-traffic + dual-run contábil dentro da tolerância antes de qualquer corte de tráfego real.

- aplicar platform/ em cloud (fora deste addon, dependência de platform-infra-engineer) e apontar services/decision + services/collector para Postgres/Redis/Redpanda/ClickHouse reais
- obter chave de licença MaxMind real e o arquivo GeoLite2-City.mmdb (consumido por E5)
- rodar tests/parity/shadow (shadow-traffic) e tests/parity/dual_run (dual-run contábil) contra o Revive legado, dentro da tolerância declarada
- executar a sequência de smoke do go-live-runbook.md (§5: make verify, go-build/vet/test, bff-ci/ml-test, platform-validate) na parte que toca o motor de decisão
- checklist do parity-golden-test-guardian (go-live-runbook §6): fail-open determinístico, deep NÃO ativo sem uplift A/B

**Subagente:** `decision-engine-engineer` · **Doc:** `ADR-0002 §D ('nada de cutover antes de I4 com golden+shadow+dual-run')` · `CA-9` · `DA-9` · `go-live-runbook.md §1` · `go-live-runbook.md §5` · `go-live-runbook.md §6` · `go-live-runbook.md §7` · **Gate:** parity-golden-test-guardian aprova shadow-traffic + dual-run contábil dentro da tolerância (gate de cutover, ADR-0002 §D); security-reviewer + privacy-compliance-auditor assinam o checklist do go-live-runbook.md §6 antes de direcionar tráfego real. · **Depende de:** E1, E2, E3, E4, E5 · **Bloqueador:** infra real: platform/ aplicada em cloud (EKS/Postgres/Redis/Redpanda/ClickHouse); chave de licença MaxMind real; tráfego real (ou volume equivalente) do Revive legado para o dual-run — nenhum destes é reproduzível neste ambiente.

##### E8 · Ativação dos flags de ML sob tráfego real (RANKER/AB/SHADOW/DEEP_ENABLED) + suporte à promoção do deep (K8) — ⏳ gated

Ligar o re-ranker GBDT (J4) e, se o A/B provar uplift, o deep ranking (K8) — sempre atrás do mesmo ponto de extensão fiado em E4, nunca furando DA-3, nunca ampliando o budget TX-4 por aspiração.

- ligar RANKER_ENABLED em shadow primeiro (SHADOW_ENABLED), depois AB_ENABLED por zona/tenant com guarda de receita + kill-switch (J4)
- acompanhar ml/ope (IPS/SNIPS/DR) e a promoção de model_version via ml/registry/promote_model.py (dono: ml-optimization-engineer) — este addon só fia o consumo do model_version promovido
- se o A/B provar uplift do deep sobre o GBDT: habilitar DEEP_ENABLED e o runtime Triton/GPU no ranker-sidecar (K8), sem criar novo ponto de extensão
- monitorar p99 do bloco de ranker (5–8 ms) e degradar com mais frequência (fail-open) em vez de ampliar o budget se o modelo não couber

**Subagente:** `decision-engine-engineer` · **Doc:** `DA-3` · `TX-4` · `ADR-0003 §G (J4)` · `ADR-0004 §A` · `ADR-0004 §H (K8)` · `go-live-runbook.md §7 (linha K8)` · `go-live-runbook.md §6 (checklist parity-golden-test-guardian)` · **Gate:** parity-golden-test-guardian + ml-optimization-engineer confirmam uplift A/B estatisticamente significativo + kill-switch testado antes de qualquer promoção; nenhuma promoção sem prova (ADR-0004 §H). · **Depende de:** E6, E7 · **Bloqueador:** tráfego real com volume estatisticamente significativo para A/B (uplift sobre a cascata pura/GBDT); sem isso a promoção é recusada por design, não só por código.

##### E9 · Reabertura do orçamento de latência (TX-4/ADR-0002 §B.2) sob número medido — ⏳ gated

Trabalho sucessor pós-Fase-3, estritamente sob gatilho mensurável: só se o hot path puro medido em produção estourar p99 > 25 ms de forma sustentada (ou o IPC ranker↔sidecar consumir > 2 ms p99), abrir ADR sucessor para avaliar poda/INT8 mais agressivo, degradar com mais frequência, ou o escape hatch Rust+Axum em componente de cauda específico — nunca reescrita global por aspiração.

- medir p99/p99.9 do hot path puro (sem ML) e do salto de IPC (UDS) em produção real sob a premissa de volume revisada
- se p99 > 25 ms sustentado: abrir ADR sucessor com o número medido, avaliando Rust+Axum apenas no componente de cauda extrema identificado (nunca reescrita ampla)
- se IPC > 2 ms p99 sustentado: avaliar Treelite linkado in-process (CGO) apenas para o ranker, medindo o custo no build hermético antes de adotar
- near-real-time/Flink (gatilho ADR-0001) não é acionado por este addon — o motor de decisão não consome ClickHouse/Flink; cross-referência a data-platform-engineer caso a necessidade de telemetria de baixa latência mude o contrato de saída da decisão

**Subagente:** `decision-engine-engineer` · **Doc:** `TX-4` · `ADR-0002 §B.2 (gatilho de reversão)` · `ADR-0003 §B (gatilho IPC > 2 ms p99)` · `ADR-0004 (gatilho global: deep não couber em 5–8 ms)` · `stack §2.1` · **Gate:** tech-lead-architect abre e aprova ADR sucessor com o número medido anexado antes de qualquer mudança de runtime/arquitetura; parity-golden-test-guardian confirma zero regressão de semântica contábil em qualquer mudança de serving. · **Depende de:** E7 · **Bloqueador:** número de p99/IPC medido de forma sustentada em produção real sob tráfego real — o budget não se amplia por aspiração (regra de ouro), então esta etapa fica sem início até a medição existir.

</details>

**→ Próximo plano deste addon.** Os dois itens código-endereçáveis de G0 já estão **FECHADOS** (E5 hot-reload do GeoLite2 na 18ª onda; E6 HOT-1/HOT-3 na 17ª onda) — a porta de produção não abre com correção de concorrência pendente. Ativação em produção (E7): destravar o cutover assim que platform/ estiver aplicada em cloud, a chave MaxMind real disponível e o dual-run contábil provar paridade com o Revive legado dentro da tolerância. Em seguida, E8 liga os flags de ML (ranker/AB/shadow/deep) só sob tráfego real e uplift A/B provado — nunca por aspiração. Sucessor pós-Fase-3 (E9): reabertura do orçamento de 5–8 ms/25 ms só sob número de p99 medido e sustentado, com ADR sucessor explícito; nenhuma tecnologia pesada (Rust+Axum, Treelite in-process/CGO) entra sem esse gatilho.

---

<a id="33"></a>

### 3.3 Plataforma de dados, telemetria e analytics (Redpanda → ClickHouse → Iceberg; fraude/IVT na ingestão)

**Subagente-dono:** `data-platform-engineer`  
**Camada de documentação:** stack §2.2 (dados/telemetria/analytics)  
**Caminhos:** `data/redpanda/topics.yaml` · `data/clickhouse/migrations/001_kafka_engines.sql` · `data/clickhouse/migrations/002_raw_tables.sql` · `data/clickhouse/migrations/003_kafka_to_raw_mvs.sql` · `data/clickhouse/migrations/004_stats_hourly.sql` · `data/clickhouse/migrations/005_live_view.sql` · `data/clickhouse/migrations/006_access_control.sql` · `data/clickhouse/migrations/007_ivt_scoring.sql` · `data/clickhouse/migrations/008_ivt_unsup_scoring.sql` · `data/clickhouse/tests/tenant_isolation_test.sql` · `data/iceberg/specs/events.yaml` · `data/iceberg/specs/billing_hourly.yaml` · `data/iceberg/specs/ivt_scores.yaml` · `data/iceberg/jobs/billing_batch_hourly.py` · `data/iceberg/jobs/iceberg_sink_job.py` · `data/iceberg/jobs/test_billing_batch_hourly.py` · `data/fraud/ivt_scoring_job.py` · `data/fraud/ivt_unsup_scoring_job.py` · `data/fraud/test_ivt_scoring_job.py` · `data/fraud/test_ivt_unsup_scoring_job.py` · `make/data.mk` · `scripts/ci/data-schema-invariants.py` · `scripts/ci/data-ivt-sql-check.py` · `scripts/ci/data-yaml-check.py` · `scripts/ci/no-float-data-sql.sh` · `docs/adr/0001-near-real-time-nao-e-requisito-v1.md` · `docs/adr/0002-fase-1-sequenciamento-e-layout.md` · `docs/adr/0003-fase-2-sequenciamento-ml-copiloto.md` · `docs/adr/0004-fase-3-sequenciamento-ia-avancada-cripto.md` · `docs/ops/go-live-runbook.md`  
**Incrementos fechados:** `I3 (Fase 1 — Redpanda→ClickHouse StatsHourly/ao-vivo→Iceberg, dedupe por event_id, billing batch skeleton testado)` · `J6 (Fase 2 — pacing proporcional + fraude/IVT supervisionada na ingestão, migration 007)` · `K2 (Fase 3 — fraude não-supervisionada Isolation Forest/Autoencoder complementando o GBDT, migration 008)` · `Fix MONEY-01 (14ª/15ª onda — floor(imp/1000) no CPM, BILLING.md §4.1)`

**Estado atual.** O pipeline de dados está código-completo na `main`: Redpanda (5 tópicos, partição por hash(event_id), nunca tenant_id), ClickHouse (Kafka engine + raw tables com dedupe ReplacingMergeTree + StatsHourly AggregatingMergeTree/uniqState/sumState + visão "ao vivo" sem Flink + row-policies fail-closed/quotas TX-3) e Iceberg (specs de eventos/billing + jobs de sink/billing batch com aritmética Decimal testada, incl. fix do floor de CPM da 14ª onda) fecham os incrementos I3 (Fase 1), J6 (Fase 2, IVT supervisionado) e K2 (Fase 3, IVT não-supervisionado). `make data-validate` (12 invariantes), `data-billing-test` e `data-ivt-test` estão verdes. Os jobs Python de Iceberg (`billing_batch_hourly.py`, `iceberg_sink_job.py`) têm a aritmética monetária, a idempotência e a lógica de reconciliação implementadas e testadas, mas a fiação de I/O real (catálogo PyIceberg vivo, cliente ClickHouse) é esqueleto com TODOs — mesma classe de pendência de infra que o restante do repo, não dívida de código.

| Etapa | Título | Status | Subagente | Âncoras de doc | Bloqueador |
|---|---|---|---|---|---|
| `E1` | Redpanda — backbone único de eventos, particionamento sem hot-partition | ✅ concluída | `data-platform-engineer` | `TX-1` · `§2.2` · `DA-7` · `ADR-0002 §B.1` · `ADR-0002 §D (I3)` | — |
| `E2` | ClickHouse — ingestão direta (Kafka engine) + raw tables com dedupe idempotente por event_id | ✅ concluída | `data-platform-engineer` | `TX-1` · `DA-7` · `TX-5` · `DA-11` | — |
| `E3` | StatsHourly — rollup faturável horário preservando o contrato do admin | ✅ concluída | `data-platform-engineer` | `DA-7` · `CA-6` · `§4.1` · `TX-2` · `ADR-0002 §D (I3)` | — |
| `E4` | Visão "ao vivo" sem Flink — atribuição dupla nunca somada | ✅ concluída | `data-platform-engineer` | `ADR-0001` · `§2.2` · `CA-6` | — |
| `E5` | Row-policies e quotas por tenant_id no ClickHouse | ✅ concluída | `data-platform-engineer` | `TX-3` · `CA-1` | — |
| `E6` | Fraude/IVT na ingestão (supervisionado J6 + não-supervisionado K2) — marca antes do StatsHourly/faturamento | ✅ concluída | `data-platform-engineer` | `TX-6` · `ADR-0003 §G (J6)` · `ADR-0004 §H (K2)` · `DA-7` | — |
| `E7` | Iceberg lakehouse — specs de eventos/billing + jobs de sink e billing batch (aritmética Decimal testada) | ✅ concluída | `data-platform-engineer` | `DA-7` · `DA-10` · `TX-2` · `ADR-0001` · `ADR-0002 §B.7` · `CA-7` | — |
| `E8` | Ativação em produção — sink real, reconciliação real, dual-run contábil e testes de idempotência sob reentrega/out-of-order | ◻ pendente | `data-platform-engineer` | `go-live-runbook §2.6` · `go-live-runbook §5` · `go-live-runbook §7` · `ADR-0002 §D (I3)` · `DA-7` | infra real: cluster Redpanda/ClickHouse provisionado, catálogo Iceberg REST + object storage vivos, credenciais via OpenBao — não aplicável neste ambiente; também desbloqueia dados reais no Iceberg para treino/OPE do ml-optimization-engineer (cutover Fase 2). |
| `E9` | Sucessor pós-Fase-3 sob gatilho medido — Flink incremental (atribuição longa near-real-time + fraude streaming) | ⏳ gated | `tech-lead-architect` | `ADR-0001` · `TX-6` · `§2.2` · `§5` | nenhum gatilho mensurável observado ainda em produção (requer tráfego real pós-E8); não medir por aspiração de escala. |

<details><summary><strong>Detalhamento das etapas</strong> (objetivo · tarefas · gate · dependências)</summary>

##### E1 · Redpanda — backbone único de eventos, particionamento sem hot-partition — ✅ concluída

Definir os tópicos Redpanda como bus único por tipo de evento, particionados por hash(event_id) (nunca tenant_id/zone_id-no-conversion), com retenção e replicação por tópico alinhadas ao contrato Protobuf da Fase 0.

- Declarar os 5 tópicos (ad-request/impression/click/conversion/decision) com partitions/replication_factor/retention por volume e criticidade de billing
- Documentar a chave de partição (event_id via producer key) e a proibição explícita de hash(tenant_id) (evita hot partitions, ADR-0002 §B.1)
- Registrar o contrato de referência do producer (acks=all, enable.idempotence=true, WAL local) consumido por internal/telemetry (fronteira com decision-engine-engineer)

**Subagente:** `data-platform-engineer` · **Doc:** `TX-1` · `§2.2` · `DA-7` · `ADR-0002 §B.1` · `ADR-0002 §D (I3)` · **Gate:** schema-contracts-steward confirma que topics.yaml não diverge do envelope Protobuf (TX-1, BACKWARD) nem da chave de dedupe event_id — verificado. · **Depende de:** —

##### E2 · ClickHouse — ingestão direta (Kafka engine) + raw tables com dedupe idempotente por event_id — ✅ concluída

Consumir os tópicos Redpanda via Kafka engine e materializar raw tables (ReplacingMergeTree) que garantem exactly-once lógico por event_id a partir de um broker at-least-once, sem PII.

- Migrations 001 (Kafka engines) + 002 (raw_ad_request/impression/click/conversion/decision, ReplacingMergeTree(ingested_at), ORDER BY event_id-first)
- Migration 003 (MVs Kafka engine → raw_*)
- Confirmar ausência de IP bruto/UA cru: user_agent_class (classe coarse) e referer_url sanitizado (scheme+host+path) — já corrigidos (HIGH da Fase 1)

**Subagente:** `data-platform-engineer` · **Doc:** `TX-1` · `DA-7` · `TX-5` · `DA-11` · **Gate:** privacy-compliance-auditor confirma ausência de PII/IP bruto nas raw tables; security-reviewer confirma que o HIGH de UA reidentificável foi remediado — ambos verdes. · **Depende de:** E1

##### E3 · StatsHourly — rollup faturável horário preservando o contrato do admin — ✅ concluída

Materializar a visão StatsHourly (AggregatingMergeTree/uniqState/sumState) que é a única fonte faturável junto do Iceberg, com defasagem ≤1h, excluindo IVT e nunca usando Float para dinheiro.

- Migration 004: stats_hourly_state + MVs incrementais por evento + VIEW stats_hourly com colunas do contrato §4.1 (hour_bucket/campaign_id/banner_id/zone_id/requests/impressions/clicks/conversions/conversion_value/currency)
- Garantir conversion_value em Decimal(38,18) via sumState (nunca Float) e GROUP BY incluindo currency (ledgers isolados por ativo, DA-10)
- Expor inventory_loss (requests-impressions) como indicador de escassez (CA-6)

**Subagente:** `data-platform-engineer` · **Doc:** `DA-7` · `CA-6` · `§4.1` · `TX-2` · `ADR-0002 §D (I3)` · **Gate:** parity-golden-test-guardian confirma que o contrato do admin (§4.1/CA-6) não regrediu; money-ledger-guardian confirma ausência de Float em conversion_value (TX-2) — ambos verificados via data-schema-invariants.py. · **Depende de:** E2

##### E4 · Visão "ao vivo" sem Flink — atribuição dupla nunca somada — ✅ concluída

Servir o número "ao vivo" (janela de segundos-minutos) como subproduto da ingestão do ClickHouse, sem processador de stream stateful, rotulado e nunca somado ao consolidado ≤1h (ADR-0001).

- Migration 005: live_stats_exact (com FINAL, dedupe garantido) e live_stats_fast (sem FINAL, duplicatas transientes aceitas)
- Rotular data_source='live' em toda linha e documentar o contrato de saída esperado pelo BFF ("source": "live" vs "consolidated")
- Confirmar que nenhuma consulta soma live_stats_* com stats_hourly

**Subagente:** `data-platform-engineer` · **Doc:** `ADR-0001` · `§2.2` · `CA-6` · **Gate:** tech-lead-architect (dono do ADR-0001) confirma que a rotulagem dual está implementada e que o contrato de saída com frontend-bff-engineer preserva a não-soma — verificado no DDL e no data-schema-invariants.py (checagem "NAO-FATURAVEL"). · **Depende de:** E2

##### E5 · Row-policies e quotas por tenant_id no ClickHouse — ✅ concluída

Isolar cada tenant a suas próprias linhas via ROW POLICY fail-closed (nunca falha aberta) e aplicar quotas de fairness multi-tenant, sem criar usuários/credenciais em migration versionada.

- Migration 006: roles tenant_role/adserver_admin_role, row-policies por tabela (stats_hourly_state, raw_*, live_stats_*) via extração fail-closed de tenant_id do nome de usuário (prefixo + validação UUID)
- Quotas por tenant (max queries/read_rows/result_rows/execution_time) vs. quota ilimitada para roles de plataforma (ingest/billing)
- tenant_isolation_test.sql cobrindo caso normal e caso adversarial (username com 'tenant_' fora do prefixo)

**Subagente:** `data-platform-engineer` · **Doc:** `TX-3` · `CA-1` · **Gate:** security-reviewer confirma o fail-closed (substring+match UUID, correção do MEDIUM #5 que usava replaceAll inseguro) — remediado e verificado. · **Depende de:** E2, E3

##### E6 · Fraude/IVT na ingestão (supervisionado J6 + não-supervisionado K2) — marca antes do StatsHourly/faturamento — ✅ concluída

Rodar scoring de IVT fora do hot path, antes do StatsHourly e do billing, combinando GBDT supervisionado (J6) e Isolation Forest/Autoencoder não-supervisionado (K2) via OR, com fail-open conservador.

- data/fraud/ivt_scoring_job.py: scoring near-line, grava em raw_ivt_score (log auditável), MV mv_ivt_score_to_raw_impression propaga ivt_status='ivt' via ReplacingMergeTree (migration 007)
- data/fraud/ivt_unsup_scoring_job.py + migration 008: raw_ivt_unsup_score (IF/AE), decisão combinada (is_ivt_supervisionado OR is_unsup_ivt) sem alterar mv_impression_to_hourly nem StatsHourly
- Preservar fail-open: model_version='fail_open' → tratado como clean (não bloquear tráfego legítimo)
- Row-policies fail-closed idênticas ao padrão de 006 aplicadas a raw_ivt_score/raw_ivt_unsup_score

**Subagente:** `data-platform-engineer` · **Doc:** `TX-6` · `ADR-0003 §G (J6)` · `ADR-0004 §H (K2)` · `DA-7` · **Gate:** money-ledger-guardian confirma que IVT é excluído do StatsHourly/billing antes da consolidação (nenhuma captura fatura tráfego marcado); privacy-compliance-auditor confirma que o input de scoring é PII-free — ambos verificados por data-ivt-sql-check.py/data-ivt-test. · **Depende de:** E3

##### E7 · Iceberg lakehouse — specs de eventos/billing + jobs de sink e billing batch (aritmética Decimal testada) — ✅ concluída

Especificar o Iceberg como única fonte de verdade contábil/treino (time-travel), com jobs idempotentes de sink (ClickHouse→Iceberg) e billing batch horário em Decimal, sem nunca ler o streaming para faturar.

- Specs events.yaml (events_ad_request/impression/click/conversion, sem PII, particionado tenant_id/ano/mês/dia), ivt_scores.yaml, billing_hourly.yaml (Decimal(38,18), status pending/posted/error)
- billing_batch_hourly.py: calc_cpm/cpc/cpa_amount em Decimal com ROUND_HALF_EVEN; floor(impressions/1000) para CPM (fix da 14ª onda, BILLING.md §4.1, testado em test_billing_batch_hourly.py)
- apply_ivt_filter_to_impressions: filtro IVT definitivo via events_ivt_score (Iceberg), nunca via ivt_status do ClickHouse; política de fallback 'unscored'→não faturar
- iceberg_sink_job.py: MERGE INTO por event_id (upsert) a partir de SELECT FINAL do ClickHouse; reconcile_with_clickhouse(): divergência acima da tolerância (1%) abre ReconciliationException, nunca autocorrige
- Documentar explicitamente os TODOs de I/O real (conexão PyIceberg/clickhouse-connect) como pendência de infra, não de código

**Subagente:** `data-platform-engineer` · **Doc:** `DA-7` · `DA-10` · `TX-2` · `ADR-0001` · `ADR-0002 §B.7` · `CA-7` · **Gate:** money-ledger-guardian confirma ausência de float em toda aritmética de billing (asserts em write_billing_postings) e a semântica de floor do CPM; schema-contracts-steward confirma que a leitura do Asset Registry é só-leitura (contrato com db/ledger/, sem duplicar schema) — verificados via data-billing-test/data-schema-invariants.py. · **Depende de:** E3, E6

##### E8 · Ativação em produção — sink real, reconciliação real, dual-run contábil e testes de idempotência sob reentrega/out-of-order — ◻ pendente

Cablear a fiação de I/O real dos jobs (catálogo Iceberg vivo, cliente ClickHouse), aplicar as 8 migrations em um cluster ClickHouse real e provar idempotência/reconciliação com dados reais antes do cutover contábil.

- Implementar a leitura/escrita real com PyIceberg (load_catalog, table.scan com predicate pushdown por snapshot_id, merge_into) em iceberg_sink_job.py e billing_batch_hourly.py, substituindo os placeholders
- Aplicar migrations 001-008 do ClickHouse em ordem em cluster real (go-live-runbook §2.6); criar usuários tenant_{uuid}/adserver_ingest/adserver_billing via IaC/OpenBao (nunca em migration versionada)
- Rodar tenant_isolation_test.sql contra ClickHouse real (hoje só validado estaticamente) — criar make data-integration-test
- Medir reconciliação real Iceberg↔ClickHouse com a tolerância declarada (1%) sob volume real; qualquer divergência acima do limiar abre exceção, nunca autocorrige
- Testes de idempotência sob reentrega (mesmo event_id duas vezes) e ordering fora de ordem (occurred_at não-monotônico) contra o ReplacingMergeTree/AggregatingMergeTree real
- Rodar dual-run contábil (Iceberg via este pipeline vs. Revive legado) dentro da tolerância, coordenado com o gate de cutover da Fase 1 (I4)

**Subagente:** `data-platform-engineer` · **Doc:** `go-live-runbook §2.6` · `go-live-runbook §5` · `go-live-runbook §7` · `ADR-0002 §D (I3)` · `DA-7` · **Gate:** parity-golden-test-guardian confirma dual-run dentro da tolerância antes de qualquer cutover; money-ledger-guardian confirma reconciliação Iceberg↔ClickHouse sem autocorreção e sem float ponta-a-ponta. · **Depende de:** E7, E5 · **Bloqueador:** infra real: cluster Redpanda/ClickHouse provisionado, catálogo Iceberg REST + object storage vivos, credenciais via OpenBao — não aplicável neste ambiente; também desbloqueia dados reais no Iceberg para treino/OPE do ml-optimization-engineer (cutover Fase 2).

##### E9 · Sucessor pós-Fase-3 sob gatilho medido — Flink incremental (atribuição longa near-real-time + fraude streaming) — ⏳ gated

Não adotar Flink por aspiração; reabrir a decisão do ADR-0001 apenas quando um dos três gatilhos mensuráveis for observado em produção, com o número anexado ao ADR sucessor.

- Monitorar em produção (pós-E8): (1) SLO de frescor contratual <5s ponta-a-ponta causando prejuízo material no pacing (DA-4) dentro da janela de lag do ClickHouse; (2) custo/latência do re-batch de atribuição de janela longa sobre Iceberg comprovadamente mais caro que um join stateful incremental; (3) necessidade de bloquear IVT dentro do ciclo de vida do request (não na ingestão horária) com perda de receita demonstrada pelo atraso atual
- Não iniciar nenhum design de Flink sem o número medido anexado a um ADR sucessor (regra de ouro)
- Se acionado: redesenhar o eixo de stream processing preservando o schema atual (decision_id/occurred_at já suportam consumo por um pipeline streaming futuro sem migração)

**Subagente:** `tech-lead-architect` · **Doc:** `ADR-0001` · `TX-6` · `§2.2` · `§5` · **Gate:** tech-lead-architect é o dono da reabertura do ADR-0001; data-platform-engineer não inicia nenhum trabalho de Flink sem o gatilho medido aprovado. · **Depende de:** E8 · **Bloqueador:** nenhum gatilho mensurável observado ainda em produção (requer tráfego real pós-E8); não medir por aspiração de escala.

</details>

**→ Próximo plano deste addon.** Ativação em produção (E8): cablear I/O real do sink/billing Iceberg (PyIceberg contra catálogo vivo), aplicar as 8 migrations ClickHouse em cluster real, provar idempotência sob reentrega/out-of-order e reconciliação Iceberg↔ClickHouse com tolerância declarada, e fechar o dual-run contábil do cutover — tudo bloqueado por infra real (não aplicável neste ambiente). Em paralelo, nenhum trabalho no eixo Flink (E9) começa sem um dos três gatilhos mensuráveis do ADR-0001 (SLO de frescor com prejuízo material, custo de re-batch de atribuição longa, ou perda de receita por IVT tardio) medido em produção.

**Riscos:**
- Reconciliação Iceberg↔ClickHouse e testes de idempotência sob reentrega/out-of-order só foram validados estaticamente (data-schema-invariants.py); comportamento real com dados reais é desconhecido até E8
- iceberg_sink_job.py e billing_batch_hourly.py têm I/O real (PyIceberg, clickhouse-connect) como esqueleto com TODOs — primeira integração real pode revelar bugs de contrato (ex.: schema Arrow vs. Parquet, snapshot pruning)
- Row-policy do ClickHouse depende do nome do usuário (tenant_{uuid}); a migração para SET custom_setting_tenant_id (TX-3-v2), mais robusta em escala, está adiada e documentada como próximo passo, não implementada
- mv_ivt_score_to_raw_impression usa MV com defaults conservadores para campos ausentes; o próprio arquivo 007 recomenda migrar para ALTER TABLE UPDATE explícito sob volume alto — não medido ainda
- Sem Flink, atribuição de janela longa fica presa a batch sobre Iceberg; se o SLA contratual mudar, há retrabalho para introduzir streaming (mitigado por decision_id/occurred_at já no schema)

---

<a id="34"></a>

### 3.4 IA / Deep Learning para otimização (ranking + fraude) — re-ranker de yield na cascata

**Subagente-dono:** `ml-optimization-engineer`  
**Camada de documentação:** docs/stack-tecnologico.md §2.3 (IA/DL para otimização) — subordinado a DA-3/TX-4 (docs/documentacao-tecnica.md)  
**Caminhos:** `ml/features/` · `ml/training/` · `ml/calibration/` · `ml/ope/` · `ml/registry/` · `ml/pacing/` · `ml/fraud/` · `ml/deep/` · `internal/ranker/` · `services/ranker-sidecar/` · `docs/adr/0003-fase-2-sequenciamento-ml-copiloto.md` · `docs/adr/0004-fase-3-sequenciamento-ia-avancada-cripto.md` · `contracts/telemetry/propensity-logging.md` · `README.md`  

**Estado atual.** Todos os incrementos de código do addon estão fechados na main: J0 (propensão instrumentada), J1 (re-ranker + sidecar fail-open), J2 (treino pCTR + calibração + MLflow), J3 (OPE + shadow), J4 (A/B + kill-switch + promoção gated), J6 (pacing proporcional + fraude/IVT supervisionada), K1 (deep scaffolding default-off) e K2 (fraude não-supervisionada) — todos com gates verdes (parity/security/privacy/money) reafirmados em ondas sucessivas de hardening. Os dois itens de código de G0 (não gated por infra) já estão **FECHADOS**: a correção HOT-1/HOT-3 (atribuição de RankResult por-request, antes via campo `last` compartilhado) na **17ª onda**, e o ONNX Runtime nativo no sidecar (`OnnxInferencer` real sob build-tag, **não mais `StubInferencer`**) na **19ª onda**. K8 (promoção do deep) e a ativação real dos flags seguem gated por tráfego real/infra da Fase 1-2.

| Etapa | Título | Status | Subagente | Âncoras de doc | Bloqueador |
|---|---|---|---|---|---|
| `E1` | J0 — Instrumentação de propensão no hot path (pré-requisito absoluto) | ✅ concluída | `decision-engine-engineer` | `ADR-0003 §G (J0)` · `TX-1` · `contracts/telemetry/propensity-logging.md §5` · `contracts/telemetry/propensity-logging.md §6` | — |
| `E2` | Função de featurização única (anti-skew treino↔serving) | ✅ concluída | `ml-optimization-engineer` | `ADR-0003 §D` · `TX-4` · `TX-5` · `DA-11` | — |
| `E3` | J1 — Esqueleto do re-ranker + sidecar fail-open (ponto de extensão) | ✅ concluída | `ml-optimization-engineer` | `ADR-0003 §A` · `ADR-0003 §B` · `ADR-0003 §G (J1)` · `TX-4` · `DA-3` | — |
| `E4` | J2 — Pipeline de treino pCTR + calibração isotônica + MLflow registry | ✅ concluída | `ml-optimization-engineer` | `ADR-0003 §G (J2)` · `TX-2` · `DA-10` · `stack §2.3` | — |
| `E5` | J3 — OPE + shadow do ranker calibrado | ✅ concluída | `ml-optimization-engineer` | `ADR-0003 §G (J3)` · `stack §2.3` | — |
| `E6` | J4 — A/B por zona/tenant + kill-switch + promoção gated por uplift | ✅ concluída | `ml-optimization-engineer` | `ADR-0003 §G (J4)` · `CA-2` · `DA-3` · `TX-2` | — |
| `E7` | J6 — Pacing proporcional + fraude/IVT supervisionada na ingestão | ✅ concluída | `ml-optimization-engineer` | `ADR-0003 §G (J6)` · `DA-4` · `TX-6` | — |
| `E8` | K1 — Scaffolding do deep ranker (two-tower DCN-v2/DLRM) atrás de flag, default-off | ✅ concluída | `ml-optimization-engineer` | `ADR-0004 §A` · `ADR-0004 §H (K1)` · `DA-3` · `TX-4` | — |
| `E9` | K2 — Fraude não-supervisionada complementando o GBDT de IVT | ✅ concluída | `ml-optimization-engineer` | `ADR-0004 §B` · `ADR-0004 §H (K2)` · `TX-6` | — |
| `E10` | Correção HOT-1/HOT-3 — atribuição de RankResult por-request (pré-condição de código antes de tráfego real) | ✅ concluída (17ª onda) | `ml-optimization-engineer` | `README.md — 'Pendente da Fase 3' (HOT-1/HOT-3)` · `ADR-0003 §A (fail-open/DA-3)` · `CA-2` | atribuição 100% por-request via `RequestRanker`/`RequestBanditRanker`/`RequestShadowRanker` (`internal/ranker/request.go`); gate ml-optimization-engineer PASS. |
| `E11` | ONNX Runtime nativo no sidecar (substituir StubInferencer por OnnxInferencer) | ✅ concluída (19ª onda) | `ml-optimization-engineer` | `ADR-0003 §B` · `README.md — Fase 2 pendências ('ONNX Runtime nativo no sidecar, hoje stub')` · `TX-4` | `OnnxInferencer` real em `services/ranker-sidecar/internal/onnx/onnx.go` (`//go:build onnx`, via `yalue/onnxruntime_go`) + contraparte `disabled.go` (`//go:build !onnx`, CGO-free) → **build default hermético** (ADR-0002 §C provado: `onnxruntime` fora de `go list -deps` do binário). Modelo re-exportado `zipmap=False` (tensor plano `[N,2]`, P(1)=col 1) via `train_pctr.py`. Paridade Go≡Python **bit-exata** (diff=0.00e+00) sob `-tags onnx` + teste de contrato `numFeatures↔ranker.FeatureVectorLength` (CI default) + pytest `zipmap=False` (`test_onnx_export.py`, CI). Gates tech-lead-architect + parity-golden-test-guardian **PASS**. Binário CGO só é linkado sob `-tags onnx` com `libonnxruntime.so`. |
| `E12` | Ativação em produção — ligar RANKER_ENABLED/AB_ENABLED/SHADOW_ENABLED sob tráfego real | ⏳ gated | `ml-optimization-engineer` | `ADR-0003 §A` · `ADR-0003 §G (J3/J4)` · `TX-4` · `docs/ops/go-live-runbook.md` | infra real: cutover da Fase 1/2 (platform/ em cloud, Postgres/Redis/Redpanda/ClickHouse/Iceberg reais) + tráfego real para o OPE — nada disso existe neste ambiente |
| `E13` | K8 — Promoção do deep ranking sob uplift A/B provado (gate final da Fase 3-IA) | ⏳ gated | `ml-optimization-engineer` | `ADR-0004 §A` · `ADR-0004 §H (K8)` · `DA-3` · `TX-4` | tráfego real + número de uplift medido sobre o GBDT (não existe ainda); Triton/GPU só entra sob esta prova, nunca antes |
| `E14` | pCVR pós-atribuição confiável (sucessor sob gatilho medido) | ◻ pendente | `ml-optimization-engineer` | `stack §2.3 ('pCVR só após atribuição confiável')` · `ADR-0003 §G (nota J2/J4)` · `TX-1` | produto/dados: atribuição confiável sob tráfego real ainda não medida (depende do cutover e de volume de conversões real) |
| `E15` | Gatilhos de reversão pós-Fase-3 (Treelite in-process, PID de pacing, Feast/Tecton) — só sob medição | ◻ pendente | `ml-optimization-engineer` | `ADR-0003 §B (gatilho Treelite in-process)` · `stack §2.3 (PID só sob oscilação observada)` · `ADR-0003 §D (gatilho Feast/Tecton)` · `stack §5` | medição real de produção ainda não disponível para nenhum dos três eixos — nenhuma tecnologia é antecipada por aspiração |

<details><summary><strong>Detalhamento das etapas</strong> (objetivo · tarefas · gate · dependências)</summary>

##### E1 · J0 — Instrumentação de propensão no hot path (pré-requisito absoluto) — ✅ concluída

Garantir que o motor de decisão preenche propensity/exploration_policy/epsilon/candidates[]/ml_fail_open a cada request, fechando o loop de atribuição antes de qualquer treino de ML.

- Confirmar Decision.propensity/exploration_policy/epsilon/candidates[]/ml_fail_open preenchidos em services/decision conforme contracts/telemetry/propensity-logging.md
- Confirmar decision_id+model_version fluindo ponta-a-ponta em lg/ck/ct até Iceberg
- Confirmar golden tests da Fase 1 intactos com DETERMINISTIC/propensity=1.0 (sem modelo)

**Subagente:** `decision-engine-engineer` · **Doc:** `ADR-0003 §G (J0)` · `TX-1` · `contracts/telemetry/propensity-logging.md §5` · `contracts/telemetry/propensity-logging.md §6` · **Gate:** parity-golden-test-guardian — golden da Fase 1 continuam verdes com ML desligado (DETERMINISTIC, propensity=1.0) · **Depende de:** —

##### E2 · Função de featurização única (anti-skew treino↔serving) — ✅ concluída

Especificar e versionar em ml/features/spec/feature_spec.yaml a única transformação contexto→vetor, usada por ml/training (Python) e internal/ranker (Go), com teste de paridade byte-a-byte.

- Manter feature_spec.yaml como fonte única versionada (semver PATCH/MINOR/MAJOR)
- Manter ml/features/go/parity_contract.go e ml/features/python/featurize.py sincronizados
- Rodar teste de paridade (mesma entrada → mesmo vetor Go/Python) sobre os fixtures em ml/features/testdata/parity_cases.json a cada mudança de spec

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0003 §D` · `TX-4` · `TX-5` · `DA-11` · **Gate:** parity-golden-test-guardian — teste de paridade treino↔serving (fixtures gold) verde; 23 features PII-free · **Depende de:** E1

##### E3 · J1 — Esqueleto do re-ranker + sidecar fail-open (ponto de extensão) — ✅ concluída

Implementar internal/ranker (featurização, IPC via UDS, timeout duro + fail-open) e services/ranker-sidecar (StubInferencer), plugados em services/decision atrás de RANKER_ENABLED (off), sem alterar a ordem da cascata.

- Cliente Go internal/ranker/ranker.go, ipc.go, score.go, featurize.go com budget e fail-open
- Sidecar services/ranker-sidecar (StubInferencer, score=0, protocolo length-prefixed JSON sobre UDS)
- Golden tests com ranker ON-fail-open ≡ cascata pura

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0003 §A` · `ADR-0003 §B` · `ADR-0003 §G (J1)` · `TX-4` · `DA-3` · **Gate:** parity-golden-test-guardian — cascata pura ≡ ranker fail-open, bit-a-bit · **Depende de:** E1, E2

##### E4 · J2 — Pipeline de treino pCTR + calibração isotônica + MLflow registry — ✅ concluída

Treinar LightGBM pCTR sobre Iceberg, calibrar isotonicamente com monitor de ECE, exportar para ONNX e registrar no MLflow.

- ml/training/train_pctr.py (LightGBM sobre a spec única de E2)
- ml/calibration/calibrate.py (isotônica + ECE/reliability, before/after)
- ml/registry (tracking + registry MLflow; artefatos pctr_booster.lgb/pctr_model.onnx versionados fora do git)
- Validar eCPM = pCTR × bid em minor-units (sem float em dinheiro)

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0003 §G (J2)` · `TX-2` · `DA-10` · `stack §2.3` · **Gate:** money-ledger-guardian — eCPM em minor-units, TX-2 sem float; parity-golden-test-guardian confirma calibração não altera semântica contábil · **Depende de:** E2

##### E5 · J3 — OPE + shadow do ranker calibrado — ✅ concluída

Avaliar off-policy (IPS/SNIPS/DR) sobre a propensão logada e servir o ranker calibrado em shadow (loga decisão-sombra, não serve tráfego real).

- ml/ope/estimators.py + dataset.py (IPS/SNIPS/DR, filtrando ml_fail_open, checando overlap/positividade/ESS)
- internal/ranker/shadow.go (SHADOW_ENABLED off por padrão)
- Preparar bandit (epsilon-greedy/Thompson) sem expor ainda

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0003 §G (J3)` · `stack §2.3` · **Gate:** parity-golden-test-guardian — shadow não altera o que é servido (control ≡ cascata pura); OPE honesto (overlap/positividade) · **Depende de:** E3, E4

##### E6 · J4 — A/B por zona/tenant + kill-switch + promoção gated por uplift — ✅ concluída

Ativar o re-ranker calibrado em A/B determinístico (FNV-1a) por zona/tenant com guarda de receita e kill-switch, expor o bandit e recusar por código qualquer promoção sem prova de uplift.

- internal/ranker/ab.go (split determinístico) + guard.go (guarda de receita em minor-units) + kill-switch fail-safe
- ml/registry/promote_model.py — UpliftProofRequired: estimator/uplift_pct/n_valid/ess_fraction/model_version/janela
- bandit_ranker.go expõe ExploreRank (epsilon-greedy/Thompson) sob A/B, propensity<1.0 fecha o loop do OPE

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0003 §G (J4)` · `CA-2` · `DA-3` · `TX-2` · **Gate:** parity-golden-test-guardian — control ≡ cascata pura; nada promovido sem uplift A/B + kill-switch (promote_model.py recusa por código) · **Depende de:** E5

##### E7 · J6 — Pacing proporcional + fraude/IVT supervisionada na ingestão — ✅ concluída

Controlador proporcional por déficit vs. cronograma (DA-4) e GBDT supervisionado de IVT marcando fraude antes do StatsHourly/faturamento (TX-6), reconciliando contra Iceberg.

- ml/pacing/controller.py + pacing_job.py (proporcional, forecast leve sobre StatsHourly)
- ml/fraud/train_ivt.py + scorer.py + register_ivt_model.py (GBDT supervisionado)
- data/clickhouse/migrations/007_ivt_scoring.sql — marcação IVT antes do faturamento

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0003 §G (J6)` · `DA-4` · `TX-6` · **Gate:** money-ledger-guardian + parity-golden-test-guardian — faturamento só sobre tráfego válido, reconciliação contra lakehouse (não streaming) · **Depende de:** E1

##### E8 · K1 — Scaffolding do deep ranker (two-tower DCN-v2/DLRM) atrás de flag, default-off — ✅ concluída

Construir o código de treino/export do deep model e a fiação mínima no mesmo ponto de extensão do GBDT, sem criar caminho paralelo nem ampliar o budget, DEEP_ENABLED=false por padrão.

- ml/deep/model.py + train_deep.py (PyTorch, torre de demanda pré-computada) + export_deep.py (INT8/ONNX→Triton)
- services/ranker-sidecar/internal/triton/selector.py — seleção de runtime por prefixo model_version 'deep-', fail-open se Triton indisponível
- internal/ranker/deep_flag.go — DEEP_ENABLED off por padrão; teste garantindo model_version não-deep por default

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0004 §A` · `ADR-0004 §H (K1)` · `DA-3` · `TX-4` · **Gate:** parity-golden-test-guardian — deep-off ≡ cascata pura bit-a-bit; budget 5-8ms não ampliado · **Depende de:** E3, E4

##### E9 · K2 — Fraude não-supervisionada complementando o GBDT de IVT — ✅ concluída

Adicionar Isolation Forest + autoencoder complementando (OR) o GBDT supervisionado, ainda na ingestão, ainda fora do hot path.

- ml/fraud/train_unsup.py + unsup_scorer.py (Isolation Forest + autoencoder sobre sample sintético de generate_ivt_sample.py)
- data/clickhouse/migrations/008_ivt_unsup_scoring.sql (RLS fail-closed, mesmo padrão de 006/007)
- Marcação combinada (OR) antes do StatsHourly/faturamento

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0004 §B` · `ADR-0004 §H (K2)` · `TX-6` · **Gate:** privacy-compliance-auditor + parity-golden-test-guardian — PII-free, reconciliação contra Iceberg intacta · **Depende de:** E7

##### E10 · Correção HOT-1/HOT-3 — atribuição de RankResult por-request (pré-condição de código antes de tráfego real) — ✅ concluída (17ª onda)

Eliminar a mis-atribuição de propensity/model_version/scores entre requests concorrentes fazendo o RankResult fluir por-request de Decide() (retorno direto ou ranker por-request) em vez do campo `last` compartilhado hoje protegido só por mutex (livre de data-race, mas não de mis-atribuição de OPE).

- Refatorar internal/ranker/ranker.go (MLRanker.last) para retornar RankResult por chamada de Decide()
- Refatorar internal/ranker/bandit_ranker.go (BanditRanker.last/LastResult) na mesma linha
- Refatorar internal/ranker/shadow.go (SetRequestContext) para o mesmo modelo por-request
- Rodar golden tests + go test -race ./internal/ranker/... confirmando ausência de regressão

**Subagente:** `ml-optimization-engineer` · **Doc:** `README.md — 'Pendente da Fase 3' (HOT-1/HOT-3)` · `ADR-0003 §A (fail-open/DA-3)` · `CA-2` · **Gate:** parity-golden-test-guardian + tech-lead-architect — golden/dual-run intactos, sem regressão de concorrência (-race), confirma que o OPE deixa de ser enviesável por request concorrente · **Depende de:** E3, E5, E6

##### E11 · ONNX Runtime nativo no sidecar (substituir StubInferencer por OnnxInferencer) — ✅ concluída (19ª onda)

Implementar o wrapper Go real (CGO + github.com/yalue/onnxruntime_go) que carrega o .onnx do MLflow registry e serve score real, mantendo o mesmo protocolo de wire e a mesma interface stub.Inferencer — sem tocar no hot path Go além da troca do inferencer.

- Implementar services/ranker-sidecar/internal/onnx/onnx.go (build tag 'onnx') satisfazendo stub.Inferencer
- Cablear RANKER_MODEL_PATH + libonnxruntime.so.1 em cmd/ranker-sidecar/main.go substituindo StubInferencer quando o build tag estiver presente
- Rodar internal/ranker/parity_test.go contra o modelo real para confirmar contrato de vetor
- Documentar hot-reload de versão (SIGHUP) mencionado no cabeçalho de main.go, hoje não implementado

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0003 §B` · `README.md — Fase 2 pendências ('ONNX Runtime nativo no sidecar, hoje stub')` · `TX-4` · **Gate:** tech-lead-architect (preserva build hermético do ADR-0002 §C fora do build tag default) + parity-golden-test-guardian (parity_test.go com modelo real) · **Depende de:** E4 · **Bloqueador:** infra: CGO + libonnxruntime.so.1 indisponíveis neste ambiente de build; o wrapper Go é escrito e testado sob build tag, mas o binário linkado (CGO) só é validado onde a lib nativa existir

##### E12 · Ativação em produção — ligar RANKER_ENABLED/AB_ENABLED/SHADOW_ENABLED sob tráfego real — ⏳ gated

Ligar o re-ranker calibrado em shadow e depois em A/B sob tráfego real de produção, medindo o budget de 5-8ms p99 e alimentando o OPE com dados reais de decision_id/model_version/propensity do Iceberg.

- Confirmar E10 (HOT-1/HOT-3) e E11 (ONNX nativo) mesclados antes de ligar qualquer flag
- Aplicar platform/ em cloud (cutover Fase 1/2) e treinar pCTR sobre Iceberg com dados reais (não sintéticos)
- Ligar SHADOW_ENABLED, medir IPC ranker↔sidecar (gatilho de referência: >2ms p99 sustentado reabre ADR-0003 §B)
- Ligar AB_ENABLED por zona/tenant com guarda de receita + kill-switch; rodar OPE (IPS/SNIPS/DR) real

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0003 §A` · `ADR-0003 §G (J3/J4)` · `TX-4` · `docs/ops/go-live-runbook.md` · **Gate:** parity-golden-test-guardian (golden intactos sob treatment real) + money-ledger-guardian (guarda de receita real) + tech-lead-architect (sign-off de go-live) · **Depende de:** E10, E11 · **Bloqueador:** infra real: cutover da Fase 1/2 (platform/ em cloud, Postgres/Redis/Redpanda/ClickHouse/Iceberg reais) + tráfego real para o OPE — nada disso existe neste ambiente

##### E13 · K8 — Promoção do deep ranking sob uplift A/B provado (gate final da Fase 3-IA) — ⏳ gated

Promover o modelo deep (Triton/GPU) via MLflow registry SOMENTE com prova de uplift A/B sobre o GBDT de produção, reusando o mesmo arcabouço de ab/guard/shadow — nunca por aspiração.

- Rodar deep em shadow sob tráfego real (pós E12) e coletar OPE (IPS/SNIPS/DR filtrando ml_fail_open)
- Ativar DEEP_ENABLED=true + A/B por zona/tenant com kill-switch
- Submeter prova de uplift a ml/registry/promote_model.py (recusa automática sem prova válida)
- Confirmar golden tests intactos com deep ativo e em fail-open

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0004 §A` · `ADR-0004 §H (K8)` · `DA-3` · `TX-4` · **Gate:** ml-optimization-engineer + parity-golden-test-guardian — nada promovido sem uplift A/B + kill-switch; budget 5-8ms não ampliado (degrada com mais frequência se o deep não couber) · **Depende de:** E12 · **Bloqueador:** tráfego real + número de uplift medido sobre o GBDT (não existe ainda); Triton/GPU só entra sob esta prova, nunca antes

##### E14 · pCVR pós-atribuição confiável (sucessor sob gatilho medido) — ◻ pendente

Treinar pCVR somente depois que o loop de atribuição (decision_id+model_version+propensity) estiver fechado com volume suficiente de conversões rastreadas sob tráfego real — nunca antes.

- Medir volume/qualidade de atribuição real (janela last-click 7d, ADR-0002 §B.7) pós-cutover
- Definir gatilho quantitativo de 'atribuição confiável' (mínimo de conversões rastreadas por decision_id com propensão não-degenerada)
- Só então: estender ml/training + ml/features para pCVR reusando a função de featurização única

**Subagente:** `ml-optimization-engineer` · **Doc:** `stack §2.3 ('pCVR só após atribuição confiável')` · `ADR-0003 §G (nota J2/J4)` · `TX-1` · **Gate:** ml-optimization-engineer (+ money-ledger-guardian se pCVR alimentar eCPM) — mesmo arcabouço de calibração/OPE/promoção do pCTR · **Depende de:** E12 · **Bloqueador:** produto/dados: atribuição confiável sob tráfego real ainda não medida (depende do cutover e de volume de conversões real)

##### E15 · Gatilhos de reversão pós-Fase-3 (Treelite in-process, PID de pacing, Feast/Tecton) — só sob medição — ◻ pendente

Consolidar os três gatilhos de reversão já declarados nos ADRs, para não antecipar tecnologia pesada sem prova: Treelite linkado (CGO) só se IPC ranker↔sidecar sustentar >2ms p99; PID só se pacing proporcional oscilar sob medição; Feast/Tecton só se features online não couberem em snapshot+Redis (>1TB ou >2ms p99 na materialização).

- Instrumentar e medir p99 do transporte ranker↔sidecar (UDS) em produção (E12/E13) antes de considerar Treelite CGO
- Instrumentar detecção de oscilação do controlador proporcional de ml/pacing antes de considerar PID
- Medir volume/latência de materialização de features online antes de considerar Feast/Tecton
- Abrir ADR sucessor específico só quando um gatilho for cruzado, com o número medido anexado

**Subagente:** `ml-optimization-engineer` · **Doc:** `ADR-0003 §B (gatilho Treelite in-process)` · `stack §2.3 (PID só sob oscilação observada)` · `ADR-0003 §D (gatilho Feast/Tecton)` · `stack §5` · **Gate:** tech-lead-architect — só abre ADR sucessor com o número medido anexado; até lá, mantém sidecar UDS + controlador proporcional + snapshot in-process/Redis · **Depende de:** E12 · **Bloqueador:** medição real de produção ainda não disponível para nenhum dos três eixos — nenhuma tecnologia é antecipada por aspiração

</details>

**→ Próximo plano deste addon.** Ativação em produção do addon segue em duas frentes sequenciais sob gatilho medido: (1) os dois itens de código de G0 já estão **FECHADOS** — E10 (HOT-1/HOT-3, atribuição de RankResult por-request, 17ª onda) e E11 (ONNX Runtime nativo no sidecar, 19ª onda — `OnnxInferencer` real sob build-tag, **não mais stub**); (2) só então E12 liga RANKER_ENABLED/AB_ENABLED/SHADOW_ENABLED sob o cutover real da Fase 1/2 (platform/ em cloud + Iceberg com tráfego real), medindo o budget de 5-8ms p99 e alimentando OPE real. Sucessor pós-Fase-3 sob gatilho medido: E13 (K8 — promoção do deep/Triton só com uplift A/B provado sobre o GBDT), E14 (pCVR só após atribuição confiável medida) e E15 (Treelite in-process / PID / Feast-Tecton, cada um só sob seu próprio gatilho numérico cruzado — nunca por aspiração).

---

<a id="35"></a>

### 3.5 Copiloto de IA para anunciantes (Claude + LangGraph)

**Subagente-dono:** `copilot-llm-engineer`  
**Camada de documentação:** stack-tecnologico.md §2.4 (copiloto de IA para anunciantes)  
**Caminhos:** `services/copilot/app/server.py` · `services/copilot/app/auth.py` · `services/copilot/app/config.py` · `services/copilot/app/model_router.py` · `services/copilot/graph/builder.py` · `services/copilot/graph/nodes.py` · `services/copilot/graph/state.py` · `services/copilot/tools/gateway.py` · `services/copilot/tools/schemas.py` · `services/copilot/observability/langfuse_setup.py` · `services/copilot/observability/golden_set.py` · `services/copilot/guardrails/` · `services/copilot/rag/` · `services/copilot/tests/test_security.py` · `services/copilot/tests/test_gateway.py` · `services/copilot/tests/test_model_router.py` · `services/copilot/tests/test_schemas.py` · `services/copilot/pyproject.toml` · `db/vector/migrations/0001_vector_schema_up.sql` · `db/vector/migrations/0002_vector_rls_up.sql` · `db/vector/tests/vector_rls_isolation_test.sql` · `bff/src/routers/copilot.ts` · `.github/workflows/db.yml` · `docs/adr/0003-fase-2-sequenciamento-ml-copiloto.md` · `docs/adr/0004-fase-3-sequenciamento-ia-avancada-cripto.md` · `docs/stack-tecnologico.md` · `docs/documentacao-tecnica.md` · `README.md`  
**Incrementos fechados:** `J5 (ADR-0003 §G) — grafo LangGraph + HITL + gateway tipado + guardrails + RAG RLS + proveniência C2PA/PII + roteamento Haiku/Sonnet/Opus + Langfuse/golden-set` · `Hardening pós-J5 (rondas C1/H1/H2/H3/M2/M4/L2/L3 do security-reviewer, sem incremento ADR próprio)`

**Estado atual.** J5 (ADR-0003 §G) está código-completo na main: grafo LangGraph com HITL obrigatório em toda escrita, gateway de autorização server-side (TX-3), 7 ferramentas read-only + 7 write-drafts tipadas Pydantic, roteamento Haiku/Sonnet/Opus com gating de custo, guardrails 2 camadas (validação estrutural + Haiku-as-judge fail-closed), RAG pgvector com RLS por tenant, gate de proveniência C2PA/SynthID+PII em validate_creative, e Langfuse/golden-set de observabilidade. security-reviewer e privacy-compliance-auditor aprovaram após várias rondas de hardening (IDOR C1, HMAC fail-closed H1, SQL injection H2, judge fail-closed H3, vazamento de erro M4, PII L2, CORS L3); o teste de isolamento RLS do RAG (vector_rls_isolation_test.sql) hoje roda de fato contra Postgres 16 + pgvector real efêmero em CI (.github/workflows/db.yml), não é mais teórico. O que resta é 100% infra/execução real, nunca lógica: ANTHROPIC_API_KEY vivo (a chamada ao Claude em agent_node é stub), checkpointer durável (MemorySaver → AsyncPostgresSaver), budget tracker persistente (in-memory → Redis), embeddings reais indexados (Voyage/Cohere), Langfuse self-hosted implantado, SDKs C2PA/SynthID reais, e execução do golden set contra um modelo vivo.

| Etapa | Título | Status | Subagente | Âncoras de doc | Bloqueador |
|---|---|---|---|---|---|
| `E1` | Grafo LangGraph com checkpointing e HITL obrigatório em toda escrita | ✅ concluída | `copilot-llm-engineer` | `stack-tecnologico.md §2.4` · `ADR-0003 §C` · `ADR-0003 §G (linha J5)` | — |
| `E2` | Bridge seguro — gateway de autorização server-side (TX-3) | ✅ concluída | `copilot-llm-engineer` | `TX-3` · `stack-tecnologico.md §2.4 (bridge seguro)` · `ADR-0003 §C` | — |
| `E3` | Ferramentas tipadas READ-ONLY: forecast, anti-contradição, proveniência, RAG | ✅ concluída | `copilot-llm-engineer` | `stack-tecnologico.md §2.4 (forecast/RAG/proveniência)` · `documentacao-tecnica.md §4.6` · `CA-4` · `TX-5 (EU AI Act Art. 50)` · `ADR-0003 §D (fronteira: featurização única é do ml-optimization-engineer, o copiloto só consome)` | — |
| `E4` | Ferramentas de ESCRITA (drafts) + verificação de posse cross-tenant | ✅ concluída | `copilot-llm-engineer` | `TX-3` · `stack-tecnologico.md §2.4 ('HITL obrigatório em toda escrita... nada publicado autonomamente')` · `ADR-0003 §G (linha J5)` | — |
| `E5` | RAG pgvector (HNSW) escopado com RLS por tenant + teste de isolamento executável | ✅ concluída | `copilot-llm-engineer` | `TX-3 (RAG sempre filtrado por tenant + teste de isolamento)` · `stack-tecnologico.md §2.4 ('RAG escopado')` · `ADR-0003 §C` | — |
| `E6` | Guardrails enxutos de 2 camadas (Pydantic determinístico + Haiku-as-judge fail-closed) | ✅ concluída | `copilot-llm-engineer` | `stack-tecnologico.md §2.4 ('Guardrails enxutos (2 camadas)')` · `TX-5` · `documentacao-tecnica.md DA-11` · `CA-8` | — |
| `E7` | Roteamento de modelo Haiku→Sonnet→Opus + gating de custo por tenant | ✅ concluída | `copilot-llm-engineer` | `stack-tecnologico.md §2.4 ('Roteamento por dificuldade... prompt caching + Batch API')` · `stack-tecnologico.md §5 (risco 'Custo de LLM/vídeo')` | — |
| `E8` | Observabilidade — Langfuse self-hosted + golden set de evals | ✅ concluída | `copilot-llm-engineer` | `stack-tecnologico.md §2.4 ('Observabilidade/evals: Langfuse self-hosted + golden set... gating de regressão de qualidade E de custo')` · `TX-5` | — |
| `E9` | Hardening de segurança pós-J5 (rondas de achados C1/H1/H2/H3/M2/M4/L2/L3) | ✅ concluída | `security-reviewer` | `TX-3` · `stack-tecnologico.md §5 (risco 'Prompt injection / vazamento entre tenants')` · `ADR-0003 §G (gates de merge comuns)` | — |
| `E10` | Ativação em produção — plugar credenciais e infra vivas atrás dos pontos de extensão já prontos | ⏳ gated | `copilot-llm-engineer` | `stack-tecnologico.md §2.4 (roteamento, prompt caching, Batch API)` · `TX-3` · `TX-5 (EU AI Act Art. 50)` · `ADR-0003 §C (gatilho de reabertura: hop BFF→copilot > 300ms p95 medido)` | infra viva: ANTHROPIC_API_KEY, Postgres+pgvector/Redis de produção, Langfuse self-hosted implantado, OpenBao com os segredos, SDKs C2PA/SynthID reais — tudo bloqueado neste ambiente (mesma pendência não-código que fecha as Fases 1/2/3) |
| `E11` | Gate de promoção de modelo — golden set real + gating de qualidade E de custo (sucessor pós-Fase-3, sob gatilho medido) | ⏳ gated | `copilot-llm-engineer` | `stack-tecnologico.md §2.4 ('gating de regressão de qualidade E de custo/tokenização antes de promover qualquer upgrade de modelo')` · `stack-tecnologico.md §5 (risco 'Custo de LLM/vídeo')` | tráfego real + ANTHROPIC_API_KEY vivo — sem dados reais de qualidade/custo não há número para o gate |
| `E12` | Higiene de layout — resolver os pacotes reservados vazios guardrails/ e rag/ | ✅ concluída (23ª onda) | `copilot-llm-engineer` | `ADR-0003 §F (política de layout — não mandata subpastas dentro de services/copilot/, só o serviço como um todo)` | — |
| `E13` | MCP como evolução opcional (multi-cliente) — sob gatilho medido | ⏳ gated | `copilot-llm-engineer` | `stack-tecnologico.md §2.4 ('MCP é evolução opcional, não pré-requisito')` | gatilho medido ainda não observado: nenhum segundo cliente de ferramentas server-side existe hoje |

<details><summary><strong>Detalhamento das etapas</strong> (objetivo · tarefas · gate · dependências)</summary>

##### E1 · Grafo LangGraph com checkpointing e HITL obrigatório em toda escrita — ✅ concluída

Orquestrar o copiloto como um grafo de estados durável (LangGraph) onde nenhuma escrita de campanha/banner/regra é aplicada sem um interrupt() explícito aprovado por um humano.

- Definir CopilotState (graph/state.py) com tenant_id, pending_diff, hitl_approved, judge_result etc.
- Construir o grafo (graph/builder.py): START→agent→[tool_execute|write_draft|guardrail]→hitl_approval→apply_write|reject_write→agent→guardrail→END
- Implementar hitl_approval_node com interrupt() do LangGraph — pausa real, retomada só via Command(resume=...)
- Expor endpoints FastAPI /v1/chat (SSE), /v1/chat/{thread_id}/resume, /v1/hitl/{thread_id}/approve|reject, /v1/session/{id}/state
- Escrever o contrato de reconexão SSE pós-HITL (docstring de app/server.py) para o frontend-bff-engineer

**Subagente:** `copilot-llm-engineer` · **Doc:** `stack-tecnologico.md §2.4` · `ADR-0003 §C` · `ADR-0003 §G (linha J5)` · **Gate:** tech-lead-architect (design do grafo) + parity-golden-test-guardian (não altera hot path/faturável) — PASS; suíte pytest do copiloto verde · **Depende de:** —

##### E2 · Bridge seguro — gateway de autorização server-side (TX-3) — ✅ concluída

Garantir que o LLM nunca receba credencial nem tenant_id do payload; toda ferramenta é chamada através de um gateway que injeta tenant_id/segredos server-side e ignora instruções do usuário.

- Implementar ToolGateway (tools/gateway.py) com tenant_id como primeiro parâmetro de todo método, nunca lido do LLM
- Implementar HMAC interno BFF↔copilot (app/auth.py) com janela anti-replay de 60s e rejeição da sentinela 'dev-skip'
- Implementar check_auth_config_on_startup fail-closed: aborta o boot se SKIP_AUTH_DEV=true em produção ou secret vazio
- Documentar SET LOCAL adserver.tenant_id parametrizado ($1) como padrão obrigatório para toda query — nunca f-string
- Cobrir com testes de segurança C1 (IDOR cross-tenant no HITL/session-state) e H1/H2 (auth.py, tools/gateway.py)

**Subagente:** `copilot-llm-engineer` · **Doc:** `TX-3` · `stack-tecnologico.md §2.4 (bridge seguro)` · `ADR-0003 §C` · **Gate:** security-reviewer — PASS (C1 IDOR, H1 HMAC fail-closed, H2 SQLi/schema corrigidos; ver test_security.py) · **Depende de:** —

##### E3 · Ferramentas tipadas READ-ONLY: forecast, anti-contradição, proveniência, RAG — ✅ concluída

Expor simulate_forecast (nunca o LLM produz o número), validate_segmentation (anti-contradição §4.6/CA-4) e validate_creative (gate C2PA/SynthID/PII) como ferramentas Pydantic tipadas que o agente só consome via ToolGateway.

- simulate_forecast: tenta o serviço de ML (ranker-sidecar) e cai em baseline Monte Carlo sobre StatsHourly se indisponível — sempre com uncertainty_note (p10/p50/p90)
- validate_segmentation: detecta contradições AND (IS×IS divergente, IS×IS NOT) — roda também sobre sugestões da própria IA antes do draft
- validate_creative: checa manifesto C2PA, watermark SynthID e disclosure 'gerado por IA' para criativos is_ai_generated=True; detecta PII em HTML (CPF/email/IP/telefone/cartão)
- search_similar_creatives / search_help_docs: interface RAG (ver E5) — nunca envia o asset em si ao LLM, só metadados/URL
- Cobrir com golden set GS-01/GS-05/GS-06/GS-07 (observability/golden_set.py)

**Subagente:** `copilot-llm-engineer` · **Doc:** `stack-tecnologico.md §2.4 (forecast/RAG/proveniência)` · `documentacao-tecnica.md §4.6` · `CA-4` · `TX-5 (EU AI Act Art. 50)` · `ADR-0003 §D (fronteira: featurização única é do ml-optimization-engineer, o copiloto só consome)` · **Gate:** privacy-compliance-auditor — APROVADO (PII gate, TX-5); parity-golden-test-guardian n/a (read-only, não toca hot path) · **Depende de:** E2

##### E4 · Ferramentas de ESCRITA (drafts) + verificação de posse cross-tenant — ✅ concluída

Toda proposta de create/update de campanha, banner, regra, cap e vínculo campanha↔zona vira um WriteDiff que só persiste após aprovação humana explícita, com dupla verificação de tenant.

- Implementar create/update_campaign_draft, create/update_banner_draft, create_delivery_rule_draft, create_cap_draft, link_campaign_zone_draft — todos retornam WriteDiff, nenhum persiste
- Rodar validate_creative ANTES do WriteDiff para create/update_banner_draft (M2) e anexar validation_result ao diff
- Rodar validate_segmentation ANTES do WriteDiff para create_delivery_rule_draft (CA-4 sobre sugestão da IA)
- apply_write: único ponto de mutação real, chamado só após hitl_approved=True; filtra tenant_id do resultado antes de repassar ao LLM (L2)
- Verificação C1 em profundidade: apply_write_node compara diff.after['tenant_id'] vs state['tenant_id'] além do check já feito no endpoint HTTP

**Subagente:** `copilot-llm-engineer` · **Doc:** `TX-3` · `stack-tecnologico.md §2.4 ('HITL obrigatório em toda escrita... nada publicado autonomamente')` · `ADR-0003 §G (linha J5)` · **Gate:** security-reviewer — PASS (C1 dupla verificação, M2 gate de criativo no caminho de escrita, L2 sem vazamento de tenant_id ao LLM) · **Depende de:** E1, E2

##### E5 · RAG pgvector (HNSW) escopado com RLS por tenant + teste de isolamento executável — ✅ concluída

Garantir que 'criativos similares por CTR' e docs de ajuda nunca vazem entre tenants — RLS obrigatório e provado por um teste SQL que hoje roda contra Postgres+pgvector real (não é mais teórico).

- Schema vector_store (db/vector/migrations/0001, 0002): creative_embeddings + help_doc_embeddings, RLS USING e WITH CHECK por adserver.tenant_id
- Rodar vector_rls_isolation_test.sql em CI (.github/workflows/db.yml, imagem pgvector/pgvector:pg16) cobrindo leitura, escrita forjada (WITH CHECK), fail-closed sem tenant_id, e bypass do superuser
- search_similar_creatives/search_help_docs: set_config parametrizado ($1) em statement isolado ANTES do SELECT, conexão dedicada (anti-leak em PgBouncer/transaction-pooling)
- Catálogo/taxonomia de regras §4.6 permanecem DIRETO no contexto com prompt caching — não usar o RAG para isso (evitar over-engineering)
- Manter docs de ajuda 'públicos' (tenant_id NULL) só leitura — nenhum tenant pode forjar um doc público via INSERT (bloco 7 do teste)

**Subagente:** `copilot-llm-engineer` · **Doc:** `TX-3 (RAG sempre filtrado por tenant + teste de isolamento)` · `stack-tecnologico.md §2.4 ('RAG escopado')` · `ADR-0003 §C` · **Gate:** security-reviewer — PASS (achado HIGH 'teste nunca passou' remediado; db.yml agora aciona o gate de fato) · **Depende de:** E2

##### E6 · Guardrails enxutos de 2 camadas (Pydantic determinístico + Haiku-as-judge fail-closed) — ✅ concluída

Bloquear saída insegura (prompt injection, vazamento de PII/tenant, solicitação de credencial) sem framework pesado, com postura fail-closed: qualquer ambiguidade ou exceção bloqueia, nunca deixa passar.

- Camada (a) validação estrutural: JSON Schema/Pydantic de tools/schemas.py + specs IAB/HTML5 + ausência de PII, gate de publicação em validate_creative
- Camada (b) haiku_judge: detecção de prompt injection, tenant leak, solicitação de credencial e PII na saída — hoje determinístico (regex/keyword), documentado como base fail-closed
- guardrail_node substitui a resposta por mensagem genérica quando is_safe=False, sem vazar detalhes da violação ao usuário
- Documentar explicitamente no código que o judge é defesa-em-profundidade — o isolamento real vem do RLS+posse (C1/H2), não do judge
- Cobertura de testes H3 (exceção→fail-closed, padrões de injection/credencial/PII bloqueados)

**Subagente:** `copilot-llm-engineer` · **Doc:** `stack-tecnologico.md §2.4 ('Guardrails enxutos (2 camadas)')` · `TX-5` · `documentacao-tecnica.md DA-11` · `CA-8` · **Gate:** security-reviewer — PASS (H3 fail-closed provado); privacy-compliance-auditor — APROVADO (PII no output) · **Depende de:** E2, E3

##### E7 · Roteamento de modelo Haiku→Sonnet→Opus + gating de custo por tenant — ✅ concluída

Rotear cada tarefa para o tier de modelo certo por dificuldade e aplicar orçamento diário por tenant, degradando graciosamente (Opus→Sonnet→Haiku) em vez de falhar.

- Política declarativa TASK_ROUTING: Haiku para inline/judge, Sonnet padrão para tool-use/forecast/criativo/segmentação/campanha, Opus só sob pedido explícito ou fallback de qualidade
- BudgetTracker com custo relativo por tier (Haiku=1, Sonnet=10, Opus=50) e downgrade em cascata quando o tenant estoura o teto diário de tokens
- InMemoryBudgetTracker para dev/CI — interface pronta para substituição por Redis em produção (ver E10)
- IDs de modelo lidos de CopilotSettings (claude-haiku-4-5-20251001, claude-sonnet-4-6, claude-opus-4-8), nunca hardcoded no roteador
- Cobertura de testes de roteamento e degradação por orçamento (test_model_router.py)

**Subagente:** `copilot-llm-engineer` · **Doc:** `stack-tecnologico.md §2.4 ('Roteamento por dificuldade... prompt caching + Batch API')` · `stack-tecnologico.md §5 (risco 'Custo de LLM/vídeo')` · **Gate:** tech-lead-architect (política de roteamento) — sem achado aberto · **Depende de:** E1

##### E8 · Observabilidade — Langfuse self-hosted + golden set de evals — ✅ concluída

Instrumentar cada conversa com um trace Langfuse (tenant como user_id pseudônimo, sem PII) e manter um golden set de 8 casos que serve de gate de regressão de qualidade e custo antes de qualquer promoção de modelo.

- get_langfuse_client: no-op seguro se LANGFUSE_ENABLED=false ou chaves ausentes (dev/CI não quebra)
- Trace por request de /v1/chat com user_id=tenant_id (pseudônimo, TX-5), metadata de model_tier
- TenantUsageTracker: emitir métrica OTel copilot_tokens_total{tenant_id,model_tier,direction} sem PII nos labels
- GOLDEN_SET (GS-01..GS-08): forecast sem invenção de número, HITL obrigatório, anti-prompt-injection, anti-contradição §4.6, gate C2PA, PII bloqueado, incerteza no forecast, degradação de orçamento
- run_eval_suite(): estrutura pronta, hoje retorna 'not_run' — execução real depende de ANTHROPIC_API_KEY + servidor vivo (ver E11)

**Subagente:** `copilot-llm-engineer` · **Doc:** `stack-tecnologico.md §2.4 ('Observabilidade/evals: Langfuse self-hosted + golden set... gating de regressão de qualidade E de custo')` · `TX-5` · **Gate:** privacy-compliance-auditor — APROVADO (sem PII em traces/labels) · **Depende de:** E1, E7

##### E9 · Hardening de segurança pós-J5 (rondas de achados C1/H1/H2/H3/M2/M4/L2/L3) — ✅ concluída

Fechar, com teste específico por achado, todas as vulnerabilidades encontradas em revisões adversariais sucessivas do copiloto antes de considerar o gate de segurança verde.

- C1 — IDOR cross-tenant: verificação de posse em hitl_approve/reject, chat_resume e get_session_state (403 antes de expor/retomar qualquer estado)
- H1 — HMAC fail-closed: sentinela 'dev-skip' nunca aceita; boot aborta (RuntimeError) se config insegura em produção
- H2 — SQL injection/schema: set_config parametrizado, schema vector_store correto, conexão dedicada por operação (anti-leak PgBouncer)
- H3 — judge fail-closed: exceção no judge → is_safe=False, nunca fail-open
- M2/M4/L2/L3 — gate de criativo no caminho de escrita, mensagens de erro genéricas + correlation_id, tenant_id nunca no payload de volta ao LLM, CORS allow_headers enumerado

**Subagente:** `security-reviewer` · **Doc:** `TX-3` · `stack-tecnologico.md §5 (risco 'Prompt injection / vazamento entre tenants')` · `ADR-0003 §G (gates de merge comuns)` · **Gate:** security-reviewer — PASS (todos os achados remediados e cobertos em services/copilot/tests/test_security.py) · **Depende de:** E1, E2, E3, E4, E5, E6

##### E10 · Ativação em produção — plugar credenciais e infra vivas atrás dos pontos de extensão já prontos — ⏳ gated

Ligar o copiloto real substituindo cada stub explícito do código por integração viva, sem tocar em nenhuma lógica de HITL/guardrail/RLS já provada.

- Wiring real do Claude em agent_node: substituir o AIMessage stub por chamada real via anthropic.AsyncAnthropic/langchain-anthropic (já são dependências declaradas em pyproject.toml), com cache_control (prompt caching) no system prompt e tools_spec gerado a partir de tools/schemas.py
- Trocar MemorySaver por AsyncPostgresSaver (LangGraph) contra o mesmo Postgres com RLS — durabilidade real de checkpoint/HITL sobrevivendo a restart
- Trocar InMemoryBudgetTracker por um tracker Redis-backed (TTL=dia) — orçamento por tenant confiável em múltiplas réplicas
- Popular vector_store com embeddings reais (Voyage voyage-multilingual-2 ou Cohere embed-multilingual-v3.0) a partir do corpus de criativos/docs de ajuda
- Implantar Langfuse self-hosted vivo (LANGFUSE_ENABLED=true) + segredos (ANTHROPIC_API_KEY, DATABASE_URL, LANGFUSE_*, VOYAGE/COHERE_API_KEY, COPILOT_INTERNAL_SECRET) via OpenBao, nunca estáticos em imagem/git
- Substituir os stubs _stub_verify_c2pa/_stub_verify_syntid por integração real (c2pa-python + COPILOT_C2PA_SIGNING_KEY; SynthID SDK) — hoje sempre retornam False (postura conservadora correta, mas bloqueia todo criativo IA até isto existir)
- Usar Batch API (-50%) para chamadas não-interativas (ex.: execução do golden set em E11), preservando streaming síncrono só no chat

**Subagente:** `copilot-llm-engineer` · **Doc:** `stack-tecnologico.md §2.4 (roteamento, prompt caching, Batch API)` · `TX-3` · `TX-5 (EU AI Act Art. 50)` · `ADR-0003 §C (gatilho de reabertura: hop BFF→copilot > 300ms p95 medido)` · **Gate:** security-reviewer + privacy-compliance-auditor sem CRITICAL/HIGH pós-wiring de credenciais reais (chaves vivas, C2PA/SynthID real); tech-lead-architect confirma que nenhuma lógica de HITL/RLS/fail-closed foi relaxada na troca de stub→real · **Depende de:** E1, E2, E3, E4, E5, E6, E7, E8, E9 · **Bloqueador:** infra viva: ANTHROPIC_API_KEY, Postgres+pgvector/Redis de produção, Langfuse self-hosted implantado, OpenBao com os segredos, SDKs C2PA/SynthID reais — tudo bloqueado neste ambiente (mesma pendência não-código que fecha as Fases 1/2/3)

##### E11 · Gate de promoção de modelo — golden set real + gating de qualidade E de custo (sucessor pós-Fase-3, sob gatilho medido) — ⏳ gated

Nunca promover um upgrade de tier/versão de modelo (ex.: Sonnet/Opus mais novo) sem antes provar, com dados reais, que não há regressão de qualidade nem de custo/tokenização.

- Rodar run_eval_suite() de fato contra o servidor vivo (E10) e o modelo candidato, medindo pass/fail dos 8 casos GS-01..GS-08
- Medir custo/tokenização médio por caso e comparar contra o budget de CopilotSettings (budget_input/output_tokens_per_tenant_day)
- Expandir o golden set com casos observados em produção (Langfuse) que hoje não estão cobertos, mantendo LLM-as-judge como critério de qualidade
- Só promover o novo model_id em CopilotSettings/model_router.py se AMBOS os gates (qualidade E custo) passarem — nunca por aspiração de capacidade
- Registrar o número medido (custo real, taxa de aprovação do golden set) na decisão de promoção, seguindo o padrão de gatilho mensurável do ADR-0003/0004

**Subagente:** `copilot-llm-engineer` · **Doc:** `stack-tecnologico.md §2.4 ('gating de regressão de qualidade E de custo/tokenização antes de promover qualquer upgrade de modelo')` · `stack-tecnologico.md §5 (risco 'Custo de LLM/vídeo')` · **Gate:** tech-lead-architect autoriza a promoção só com o número medido (golden set + custo) anexado; money-ledger-guardian n/a (custo de LLM não é ledger de tenant financeiro, mas orçamento operacional) · **Depende de:** E10 · **Bloqueador:** tráfego real + ANTHROPIC_API_KEY vivo — sem dados reais de qualidade/custo não há número para o gate

##### E12 · Higiene de layout — resolver os pacotes reservados vazios guardrails/ e rag/ — ✅ concluída (23ª onda)

Decidir e executar, sem risco, se a lógica hoje em tools/gateway.py (haiku_judge, search_similar_creatives, search_help_docs) migra para os pacotes guardrails/ e rag/ já declarados em pyproject.toml (packages.find.include) mas ainda vazios, ou se esses diretórios são removidos por não serem mais necessários.

- Verificar se guardrails/ e rag/ têm arquivos versionados (hoje: nenhum — git ls-files confirma diretórios vazios sem sequer .gitkeep commitado)
- Decidir: (a) mover haiku_judge + validate_creative (parte de proveniência) para guardrails/, e search_similar_creatives/search_help_docs para rag/, deixando tools/gateway.py como fachada fina; ou (b) remover os diretórios vazios e o include correspondente do pyproject.toml se a colocation atual for preferida
- Se optar por (a): mover código com testes junto, sem alterar comportamento (achados de segurança já fechados não podem regredir)
- Atualizar SOURCES.txt/egg-info e a árvore documentada no ADR-0003 §F se o layout mudar

**Subagente:** `copilot-llm-engineer` · **Doc:** `ADR-0003 §F (política de layout — não mandata subpastas dentro de services/copilot/, só o serviço como um todo)` · **Gate:** tech-lead-architect (decisão de layout, baixo risco) — não é gate de segurança/paridade/money · **Depende de:** —

##### E13 · MCP como evolução opcional (multi-cliente) — sob gatilho medido — ⏳ gated

Só migrar as ferramentas tipadas do ToolGateway para o protocolo MCP quando houver um segundo cliente de ferramentas além do BFF (ex.: IDE do anunciante, integração de terceiros) que justifique um protocolo padronizado.

- Não implementar MCP hoje — o BFF é o único consumidor e o ToolGateway interno já cobre o caso de uso
- Definir o gatilho de reabertura: aparecimento de um segundo cliente de ferramentas server-side (não o BFF) que precise descobrir/chamar as mesmas ferramentas
- Se o gatilho disparar: expor os schemas já tipados de tools/schemas.py como tool definitions MCP, mantendo o mesmo ToolGateway como implementação server-side (TX-3 preservado)

**Subagente:** `copilot-llm-engineer` · **Doc:** `stack-tecnologico.md §2.4 ('MCP é evolução opcional, não pré-requisito')` · **Gate:** tech-lead-architect confirma a necessidade de multi-cliente antes de abrir a migração · **Depende de:** E2 · **Bloqueador:** gatilho medido ainda não observado: nenhum segundo cliente de ferramentas server-side existe hoje

</details>

**→ Próximo plano deste addon.** Ativação em produção (E10): plugar ANTHROPIC_API_KEY real na chamada ao Claude (com prompt caching + Batch API), trocar MemorySaver→AsyncPostgresSaver, budget tracker in-memory→Redis, indexar embeddings reais no pgvector, subir Langfuse self-hosted e integrar SDKs C2PA/SynthID reais — tudo bloqueado por infra viva (OpenBao, Postgres/Redis/Langfuse de produção), não por código pendente. Sucessor pós-Fase-3 sob gatilho medido (E11): só rodar run_eval_suite() de fato e promover qualquer upgrade de tier de modelo depois que o gate de qualidade E custo/tokenização vier com número real anexado — nunca por aspiração de capacidade.

**Riscos:**
- agent_node hoje retorna um AIMessage stub — o tool-calling real (extração de tool_calls de uma resposta de API de verdade) nunca foi exercitado end-to-end sem ANTHROPIC_API_KEY
- _stub_verify_c2pa/_stub_verify_syntid sempre retornam False — postura conservadora correta (bloqueia por padrão), mas nenhum criativo gerado por IA pode ser aprovado até a integração real das SDKs existir
- haiku_judge é 100% determinístico (regex/keyword) hoje — a chamada real ao Haiku 4.5 como judge semântico ainda não está implementada; a base fail-closed é sólida mas não captura nuances
- MemorySaver não é durável — reinício do processo perde threads HITL pendentes; produção exige AsyncPostgresSaver antes de qualquer tráfego real
- InMemoryBudgetTracker não persiste entre réplicas — gating de custo por tenant não é confiável em produção horizontal sem Redis
- search_similar_creatives/search_help_docs retornam sempre resultados vazios sem um corpus indexado — o RAG estruturalmente correto (RLS provado) ainda não tem dado real para servir
- run_eval_suite() retorna 'not_run' — o gate de regressão de qualidade/custo tem a estrutura (8 casos) mas nenhuma execução real ainda ocorreu
- services/copilot/guardrails/ e rag/ estão vazios mas já declarados como pacotes Python no pyproject.toml — risco baixo de confundir quem espera achar lógica lá (ela vive hoje em tools/gateway.py e graph/nodes.py)

---

<a id="36"></a>

### 3.6 Front-end self-service do anunciante + BFF

**Subagente-dono:** `frontend-bff-engineer`  
**Camada de documentação:** stack §2.5 (front-end self-service + BFF)  
**Caminhos:** `web/console/src/app` · `web/console/src/middleware.ts` · `web/console/src/lib/money.ts` · `web/console/src/lib/contradiction.ts` · `web/console/src/lib/trpc.ts` · `web/console/src/lib/copilot-schemas.ts` · `web/console/src/lib/use-copilot-session.ts` · `web/console/src/components/copilot` · `web/console/src/components/ui` · `web/console/src/app/dashboard/page.tsx` · `web/console/src/app/billing/page.tsx` · `web/console/src/app/rules/page.tsx` · `web/console/src/app/copilot/page.tsx` · `web/console/package.json` · `bff/src/routers/config.ts` · `bff/src/routers/payments.ts` · `bff/src/routers/copilot.ts` · `bff/src/routers/stats.ts` · `bff/src/adapters/postgres-config.ts` · `bff/src/adapters/postgres-payments.ts` · `bff/src/schemas` · `docs/documentacao-tecnica.md` · `docs/stack-tecnologico.md` · `docs/adr/0002-fase-1-sequenciamento-e-layout.md` · `docs/adr/0003-fase-2-sequenciamento-ml-copiloto.md` · `docs/adr/0004-fase-3-sequenciamento-ia-avancada-cripto.md` · `docs/ops/go-live-runbook.md` · `README.md` · `.github/workflows/web.yml` · `.github/workflows/bff.yml`  
**Incrementos fechados:** `I4 (ADR-0002) — console Next.js + BFF tRPC, CRUD, vínculo N:N, ACL` · `J5 (ADR-0003) — copiloto LangGraph via BFF: SSE, HITL, RAG, C2PA` · `K7 (ADR-0004 §H) — BFF + UI de pagamentos, status read-only, sem cripto no cliente` · `12ª–16ª onda README — hardening de segurança/CI do console e BFF (IDOR, CSRF, scale sem fallback, gates órfãos fechados)`

**Estado atual.** O console (web/console) e o BFF (bff/src) estão CÓDIGO-COMPLETOS para os incrementos I4 (Fase 1), J5 (Fase 2) e K7 (Fase 3 §H): CRUD com ACL server-side (CA-1), builder de segmentação com anti-contradição (CA-4), dashboards que nunca somam "ao vivo" com "consolidado ≤1h" (CA-6/ADR-0001), dinheiro tratado como string DECIMAL de ponta a ponta sem Number()/aritmética no cliente (TX-2), copiloto com streaming SSE + HITL obrigatório (ADR-0003 J5), e UI/BFF de pagamentos somente-leitura sem cripto no cliente (ADR-0004 K7). Todos os gates de segurança/privacidade/paridade citados no README (12ª–16ª onda) estão verdes para este componente; `make bff-ci`/`make web-ci` passam. Os 2 desvios vs. o mandato §2.5 que a varredura encontrara foram **CORRIGIDOS** (G0): (1) **middleware fail-closed** (E9, 22ª onda — dev-stub nunca alcançável em produção); (2) **alinhamento de stack** (E10, 23ª onda — Next 16.2.10/React 19.2.7, alicerce shadcn/ui, **a11y-ci mecânico WCAG 2.2 AA com axe-core + `puppeteer-core`** [Chrome do sistema, sem Playwright] + focus-trap do modal HITL). Zustand e Vercel AI SDK v5 ficaram **diferidos com gatilho documentado** (triagem de escopo mínimo do tech-lead: sem estado cross-route real hoje; o SSE/HITL bespoke já passou no security gate).

| Etapa | Título | Status | Subagente | Âncoras de doc | Bloqueador |
|---|---|---|---|---|---|
| `E1` | CRUD self-service + ACL server-side no BFF (I4) | ✅ concluída | `frontend-bff-engineer` | `CA-1` · `DA-1` · `DA-2` · `§4.1` · `TX-3` · `ADR-0002 (I4)` | — |
| `E2` | Dinheiro na UI como string DECIMAL (TX-2/DA-10) | ✅ concluída | `frontend-bff-engineer` | `TX-2` · `DA-10` · `CA-7` | — |
| `E3` | Builder de segmentação com anti-contradição (CA-4/§4.6) | ✅ concluída | `frontend-bff-engineer` | `CA-4` · `§4.6` | — |
| `E4` | Dashboards ≤ 1h vs. ao vivo — nunca somar (CA-6/ADR-0001) | ✅ concluída | `frontend-bff-engineer` | `CA-6` · `§4.7` · `ADR-0001` | — |
| `E5` | Copiloto na UI — SSE + HITL obrigatório (J5, ADR-0003) | ✅ concluída | `frontend-bff-engineer` | `TX-3` · `TX-5` · `ADR-0003 (J5)` · `CA-4` | — |
| `E6` | BFF + UI de pagamentos — status somente-leitura, cripto fora do cliente (K7, ADR-0004 §H) | ✅ concluída | `frontend-bff-engineer` | `ADR-0004 §H (K7)` · `TX-2` · `TX-3` · `§2.5` | — |
| `E7` | CI do front/BFF deixa de ser órfã (bff-ci/web-ci) | ✅ concluída | `frontend-bff-engineer` | `§2.5` · `ADR-0002` | — |
| `E8` | PostgresConfigAdapter real — fecha o laço console→decisão (14ª onda) | ✅ concluída | `frontend-bff-engineer` | `CA-1` · `TX-3` · `DA-11` | — |
| `E9` | Fail-closed real do middleware de sessão em produção | ✅ concluída (22ª onda) | `frontend-bff-engineer` | `TX-3` · `CA-1` · `§2.5` | — |
| `E10` | Alinhamento de stack com o mandato §2.5 (dívida verificada, sem bloqueio de infra) | ✅ concluída (escopo mínimo; Zustand/AI SDK v5 diferidos c/ gatilho) | `frontend-bff-engineer` | `§2.5` · `WCAG 2.2 AA` · `ADR-0003` | — |
| `E11` | Ativação em produção sob infra real (SESSION_SECRET/OpenBao, FQDNs, cutover) | ⏳ gated | `frontend-bff-engineer` | `TX-3` · `docs/ops/go-live-runbook.md §3` · `README.md (I5)` | infra real: cluster/OpenBao/DNS de produção ainda não aplicados neste ambiente (mesma pendência não-código do restante do roadmap) |
| `E12` | Copiloto embutido contextualmente na UI (sucessor pós-F3, sob gatilho de uso medido) | ⏳ gated | `frontend-bff-engineer` | `§2.5` · `ADR-0003 (J5)` | requer tráfego real em produção (E11) para medir o sinal de uso que justifica o investimento — não mensurável em ambiente local/CI |
| `E13` | Assinatura on-chain pelo anunciante no front (fora de escopo padrão, sob spec AEV/BND) | ⏳ gated | `payments-crypto-engineer` | `ADR-0004 E.9` · `§2.5` · `§3 (q.9)` | bloqueio de produto: spec oficial de AEV/BND ainda não define se o anunciante assina on-chain ou se a plataforma custodia por ele (ADR-0004 §3 q.9, sem resposta) |

<details><summary><strong>Detalhamento das etapas</strong> (objetivo · tarefas · gate · dependências)</summary>

##### E1 · CRUD self-service + ACL server-side no BFF (I4) — ✅ concluída

Entregar o console Next.js e o BFF tRPC como fronteira rígida de ACL server-side sobre a taxonomia de dois hemisférios (DA-1) e o vínculo N:N campanha↔zona (DA-2), com isolamento de tenant garantido no servidor, nunca no cliente.

- CRUD de anunciantes/campanhas/banners/sites/zonas em web/console/src/app/{advertisers,campaigns,banners,sites,zones}/page.tsx
- Router campaignZone (link/unlink/list) em bff/src/routers/config.ts materializando o vínculo N:N (DA-2)
- tenantProcedure em bff/src/lib/trpc.ts + ctx.tenantId nunca aceito de input do cliente
- middleware.ts remove headers X-Adserver-*/X-Tenant-* do browser e injeta tenant a partir de sessão verificada

**Subagente:** `frontend-bff-engineer` · **Doc:** `CA-1` · `DA-1` · `DA-2` · `§4.1` · `TX-3` · `ADR-0002 (I4)` · **Gate:** security-reviewer — APROVADO (README 12ª onda: 403 cross-tenant, sem forja de tenant); parity-golden-test-guardian sem regressão em CA-1 · **Depende de:** —

##### E2 · Dinheiro na UI como string DECIMAL (TX-2/DA-10) — ✅ concluída

Garantir que nenhuma aritmética monetária, `Number()` ou conversão automática de câmbio ocorra no cliente; o BFF entrega string DECIMAL + rótulo de moeda, o front só formata.

- web/console/src/lib/money.ts — formatMoney/formatCtr via decimal.js + Intl.NumberFormat, comentário explícito proibindo Number() para dinheiro
- components/ui/money-display.tsx e payment-status-badge.tsx consumindo MoneyWire (amount:string, currency:string)
- bff/src/schemas/money.ts como fonte única Zod do contrato Money nas fronteiras BFF↔front
- postgres-payments.ts sem fallback silencioso de scale (JOIN obrigatório com asset_registry, erro explícito se ausente)

**Subagente:** `frontend-bff-engineer` · **Doc:** `TX-2` · `DA-10` · `CA-7` · **Gate:** money-ledger-guardian — PASS (README: scale sem fallback silencioso, 0 float, DA-10 sem câmbio implícito) · **Depende de:** —

##### E3 · Builder de segmentação com anti-contradição (CA-4/§4.6) — ✅ concluída

RHF + Zod + react-querybuilder para AND/OR de regras de entrega, com validação anti-contradição que alerta antes de salvar uma regra AND mutuamente exclusiva — inclusive sobre sugestões futuras da IA.

- web/console/src/lib/contradiction.ts — detectContradictions() sobre vetores discretos mutuamente exclusivos (Time-Day-of-Week, Geo-Country, Geo-City, Client-Useragent)
- web/console/src/app/rules/page.tsx integrando react-querybuilder + alerta de contradição antes do submit
- Reuso da mesma função de detecção no builder de sugestões do copiloto (E6) para não silenciar banners via IA

**Subagente:** `frontend-bff-engineer` · **Doc:** `CA-4` · `§4.6` · **Gate:** parity-golden-test-guardian — CA-4 coberto pelo golden suite (tests/parity), sem regressão · **Depende de:** —

##### E4 · Dashboards ≤ 1h vs. ao vivo — nunca somar (CA-6/ADR-0001) — ✅ concluída

Separar visualmente a fonte 'consolidado ≤1h' (stats_hourly, faturável) da fonte 'ao vivo' (live_stats_*, não faturável), rotulando ambas e proibindo qualquer soma entre elas na UI.

- web/console/src/app/dashboard/page.tsx — seções separadas com DataSourceBadge('consolidated'|'live') e headings distintos aria-labelledby
- Empty states dedicados por fonte (nunca mesclar dados ausentes de uma fonte com a outra)
- Recharts para visualização única, sem lib duplicada

**Subagente:** `frontend-bff-engineer` · **Doc:** `CA-6` · `§4.7` · `ADR-0001` · **Gate:** parity-golden-test-guardian — CA-6 no golden suite; privacy-compliance-auditor sem PII nos KPIs exibidos · **Depende de:** —

##### E5 · Copiloto na UI — SSE + HITL obrigatório (J5, ADR-0003) — ✅ concluída

Expor o copiloto Claude via BFF (que protege a chave e injeta tenant_id) com streaming SSE, diff de preview e mutation otimista, garantindo que nenhuma escrita ocorra sem aprovação explícita do anunciante.

- components/copilot/{chat-panel,hitl-diff-preview,tool-call-indicator}.tsx + lib/use-copilot-session.ts (parser SSE + reducer de estado)
- bff/src/routers/copilot.ts — proxy SSE ao services/copilot, injeta tenant_id, nunca repassa credencial ao LLM
- PATCH validado por Zod + preview de diff antes de aplicar sugestão 1-clique (mutation otimista TanStack Query)
- Reuso de lib/contradiction.ts sobre sugestões de regra geradas pela IA antes de permitir aplicar

**Subagente:** `frontend-bff-engineer` · **Doc:** `TX-3` · `TX-5` · `ADR-0003 (J5)` · `CA-4` · **Gate:** security-reviewer — PASS (IDOR cross-tenant fechado, SSE com tenant server-side, CSRF no BFF — README Fase 2/12ª onda) · **Depende de:** —

##### E6 · BFF + UI de pagamentos — status somente-leitura, cripto fora do cliente (K7, ADR-0004 §H) — ✅ concluída

Expor saldo/faturamento do tenant via router tRPC somente-leitura, sem nenhuma mutation que mova dinheiro e sem qualquer biblioteca de carteira/assinatura on-chain no front por padrão.

- bff/src/routers/payments.ts — balances/paymentStatus/paymentDetail, tenant sempre de ctx, sem input parametrizável de tenant
- web/console/src/app/billing/page.tsx — página self-service de saldo/faturamento consumindo MoneyWire
- Nenhuma dependência wagmi/viem/WalletConnect no web/console/package.json (verificado)

**Subagente:** `frontend-bff-engineer` · **Doc:** `ADR-0004 §H (K7)` · `TX-2` · `TX-3` · `§2.5` · **Gate:** security-reviewer — APROVADO em K7 (sem segredo/PII no front, sem IDOR, sem cripto no cliente) · **Depende de:** —

##### E7 · CI do front/BFF deixa de ser órfã (bff-ci/web-ci) — ✅ concluída

Garantir que os alvos `make bff-ci` (typecheck+lint+jest) e `make web-ci` (tsc strict+next lint) sejam de fato acionados por workflow, fechando o gate cego encontrado na 15ª onda.

- .github/workflows/bff.yml e .github/workflows/web.yml criados e disparando em PR/push sobre bff/** e web/console/**
- bff-ci cobre a suíte Jest do BFF (config-adapter + payments-adapter + in-memory fixtures + os testes-guarda de ACL/HITL/IDOR das 27ª–30ª ondas). **Sem contagem fixa aqui de propósito:** nenhuma automação assere número de testes, então um número em prosa só envelhece e mente (ver `docs/ops/go-live-runbook.md` §5, "Nota sobre contagens de teste"). O gate é o exit code de `make bff-ci`.

> **Correção da 31ª onda — este item esteve MENTINDO desde que foi escrito.**
> "Acionado por workflow" não é o mesmo que "passa". `web.yml` e `a11y.yml`
> rodavam apenas `make web-install`, mas o typecheck do console carrega
> `bff/src/**` no program (`web/console/src/types/bff.ts` reexporta `AppRouter`
> do BFF) e resolve `pg`/`zod`/`@trpc/server` a partir de **`bff/node_modules`**.
> Em checkout limpo de runner os dois jobs morriam com `TS2307` em cascata —
> ou seja, `make web-ci` nunca passou em CI e o gate de acessibilidade
> **nunca chegou a executar o axe**. O verde local mascarava o vermelho de CI,
> porque `bff/node_modules` existe por acaso na máquina de quem desenvolve.
> Fechado com: passo explícito `make bff-install` nos dois workflows, alvo
> `web-bff-deps` em `make/web.mk` (torna a dependência oculta explícita e
> auto-instalável), `bff/**` nos path-filters de ambos, e `npm ci` no lugar de
> `npm install`. Provado por mutação: com `bff/node_modules` removido,
> `make web-typecheck` agora se auto-corrige e sai 0.

**Subagente:** `frontend-bff-engineer` · **Doc:** `§2.5` · `ADR-0002` · **Gate:** tech-lead-architect — triagem confirmou gate órfão fechado (README sweep item 6, 15ª onda); **31ª onda reabriu e fechou o gate que era órfão de DEPENDÊNCIA, não de trigger** · **Depende de:** —

##### E8 · PostgresConfigAdapter real — fecha o laço console→decisão (14ª onda) — ✅ concluída

Promover o BFF de config do stub in-memory para o adapter Postgres real, fazendo o que o anunciante cadastra no console ser efetivamente lido no snapshot do motor de decisão.

- bff/src/adapters/postgres-config.ts — CRUD real com RLS por tenant via set_config
- Seleção por BFF_PG_DSN; fallback in-memory preservado para dev/CI sem Postgres
- 19 novos testes cobrindo o adapter real (postgres-config.test.ts)

**Subagente:** `frontend-bff-engineer` · **Doc:** `CA-1` · `TX-3` · `DA-11` · **Gate:** money/security/schema-contracts (revisão adversarial multi-lente) — APROVADO na 14ª onda · **Depende de:** —

##### E9 · Fail-closed real do middleware de sessão em produção — ✅ concluída (22ª onda)

Fechar um gap verificado no código: middleware.ts cai em modo dev-stub (aceita token sem assinatura HMAC) sempre que SESSION_SECRET está ausente, sem nenhum hard-fail explícito se NODE_ENV=production — hoje a proteção depende só de disciplina operacional, não de um guard estrutural no código.

- Adicionar guard em middleware.ts: se process.env.NODE_ENV === 'production' e SESSION_SECRET ausente, recusar boot/retornar 500 em vez de cair no branch dev-stub
- Cobrir com teste que simula produção sem segredo e espera falha fechada, não bypass
- Documentar em comentário que isto é independente da injeção real do segredo (E11) — é hardening de código, não infra

**Subagente:** `frontend-bff-engineer` · **Doc:** `TX-3` · `CA-1` · `§2.5` · **Gate:** security-reviewer — deve aprovar que não há caminho de produção sem HMAC verificado · **Depende de:** E1

> **Fechado na 22ª onda.** Predicado puro `web/console/src/lib/session-guard.ts` (`sessionConfigError`: produção com segredo ausente **ou < 32 bytes** → erro) em **dupla defesa** — camada 1 no topo de `middleware()` (500 para toda rota casada, "recusa boot") + camada 2 em `verifySessionToken` (produção sem segredo → `null`, nunca cai no dev-stub). Teste `session-guard.test.ts` via runner nativo `node:test` (10 casos; alvo `make web-test` em `web-ci`; `web.yml` no Node 24; `tsconfig` com `allowImportingTsExtensions`). Comportamento dev/CI 100% preservado. **Gate `security-reviewer` PASS** (`productionBypassPossible=false`, 0 CRITICAL/HIGH). `make web-ci` verde de 1ª mão (tsc + lint + 10/10 testes). **Residual MEDIUM diferido a E11:** o guard depende de `NODE_ENV` — enforce `NODE_ENV=production` no manifesto do pod (ou flag explícito) fecha o risco de deploy com `NODE_ENV` unset. É hardening de infra, não de código.

##### E10 · Alinhamento de stack com o mandato §2.5 (dívida verificada, sem bloqueio de infra) — ✅ concluída (23ª onda; escopo mínimo, Zustand/AI SDK v5 diferidos c/ gatilho)

Fechar 4 desvios reais encontrados nesta varredura entre o código publicado e o mandato: versões pinadas abaixo do alvo, ausência do design system shadcn/ui+Base UI, ausência de Zustand, e copiloto sem Vercel AI SDK v5; e suprir a lacuna de verificação mecânica de WCAG 2.2 AA.

- Atualizar web/console/package.json: next 15.3.3→16.x, react/react-dom 19.1.0→19.2.x (validar breaking changes do App Router/Turbopack)
- Introduzir shadcn/ui sobre Base UI/React Aria para os componentes já duplicados à mão (status-badge, empty-state, money-display, payment-status-badge)
- Introduzir Zustand para estado de UI cross-page (ex.: sessão do copiloto, filtros de dashboard) mantendo TanStack Query só para server state
- Migrar lib/use-copilot-session.ts do parser SSE hand-rolled para Vercel AI SDK v5 (tool-calling tipado), preservando HITL obrigatório
- Adicionar axe-core + Playwright como novo workflow de CI (a11y-ci) cobrindo páginas principais (dashboard, rules, billing, copilot)

**Subagente:** `frontend-bff-engineer` · **Doc:** `§2.5` · `WCAG 2.2 AA` · `ADR-0003` · **Gate:** tech-lead-architect — triagem de escopo mínimo (não reabrir arquitetura) + web-ci e novo a11y-ci verdes · **Depende de:** E1, E5

> **Progresso sob triagem de escopo mínimo do tech-lead-architect.** Parte 1
> (versões): Next 15.3.3→16.2.10, React 19.1.0→19.2.7, `eslint.config.mjs`
> migrado pra flat nativa (Next 16 removeu `next lint`), `watch()` do RHF →
> `useWatch` (React Compiler). Parte 2 (esta rodada): **a11y-ci fechado SEM
> Playwright** (decisão do tech-lead — axe-core + `@axe-core/puppeteer` +
> `puppeteer-core` contra o Chrome do sistema/runner via `executablePath`,
> nenhum browser baixado) rodando contra uma rota `/a11y-harness` client-only
> gated por `A11Y_HARNESS=1` (404 em qualquer build sem a env var — nunca
> pública em produção) que monta os componentes REAIS com props-fixture, sem
> BFF/tRPC vivo; alvo `make web-a11y`, workflow `.github/workflows/a11y.yml`
> separado do `web.yml`. Achados do axe remediados cirurgicamente: contraste
> insuficiente do botão "Aplicar (ciente do aviso)" no `hitl-diff-preview`
> (`bg-amber-600`→`bg-amber-700`, 3.19:1→passa 4.5:1) e semântica de
> `<dl>`/`<dt>`/`<dd>` quebrada (`DiffRow`/`MoneyRow` usavam `<span>` solto
> dentro de `<dl>`, viola WCAG 1.3.1). **Focus-trap do modal HITL: CORRIGIDO na
> 23ª onda** (commit `b4cb624`) — o `hitl-diff-preview` (`role="dialog"
> aria-modal="true"`) não implementava focus-trap (Tab/Shift+Tab escapavam);
> foi sinalizado na E10 parte 2 (axe estático não simula Tab) e depois **fechado**
> com um focus-trap real (ciclo Tab/Shift+Tab no diálogo, Escape sai, restauração
> de foco SC 2.4.3) + **verificação mecânica** no a11y-ci (puppeteer simula Tab),
> sob `security-reviewer` **APROVADO** (HITL/TX-3/CA-4 intactos). Alicerce shadcn/ui
> instalado (`src/lib/utils.ts::cn()` com `clsx`+`tailwind-merge`,
> `components.json` Base UI + Tailwind v4 CSS-first) sem reescrever os 4
> componentes estáticos já gate-verdes (só trocou `[...].join(" ")` por
> `cn(...)` em `status-badge.tsx`/`payment-status-badge.tsx`, zero mudança de
> markup). **Diferido, fora deste escopo:** Zustand (estado de UI cross-page)
> e migração de `lib/use-copilot-session.ts` para Vercel AI SDK v5 — nenhum
> dos dois foi tocado. **Gatilho de reabertura do Zustand:** hoje todo estado
> do console é page-local (TanStack Query cobre server state, `useState`
> cobre UI state por página); Zustand só se justifica quando existir estado
> de UI genuinamente cross-route — o candidato natural é o copiloto embutido
> contextualmente (E12: `ChatPanel` teria que sobreviver à navegação entre
> `/campaigns`, `/rules`, `/banners` etc. em vez de resetar por rota). Até
> E12 disparar (sob gatilho de uso medido, ele mesmo gated por E11/infra
> real), Zustand permanece fora da árvore de dependências. Vercel AI SDK v5:
> gatilho de reabertura registrado em `docs/adr/0003-fase-2-sequenciamento-ml-copiloto.md`
> (seção "Gatilho de reabertura").

##### E11 · Ativação em produção sob infra real (SESSION_SECRET/OpenBao, FQDNs, cutover) — ⏳ gated

Ligar o console/BFF a segredos e endpoints reais de produção, completando o que o README classifica como pendência de infra (não de código) para fechar a Fase 1/2/3 no que toca este componente.

- Provisionar SESSION_SECRET real (256 bits) via OpenBao Pod Identity, nunca em .env estático (docs/ops/go-live-runbook.md §3)
- Preencher ALLOWED_ORIGINS de produção e validar CSRF fim-a-fim contra o domínio real do console
- Confirmar que o console/BFF falam com decision/collector reais (Fase 1 I5 wiring) sob tráfego real, não mais docker-compose local
- Rodar smoke pós-deploy do runbook (Passo 1-6) com o front incluído

**Subagente:** `frontend-bff-engineer` · **Doc:** `TX-3` · `docs/ops/go-live-runbook.md §3` · `README.md (I5)` · **Gate:** security-reviewer (checklist do go-live-runbook §6) — nenhuma credencial estática, CSRF validado contra origem real · **Depende de:** E9 · **Bloqueador:** infra real: cluster/OpenBao/DNS de produção ainda não aplicados neste ambiente (mesma pendência não-código do restante do roadmap)

##### E12 · Copiloto embutido contextualmente na UI (sucessor pós-F3, sob gatilho de uso medido) — ⏳ gated

Evoluir o copiloto de página dedicada (/copilot) para um ponto de entrada contextual dentro dos builders de campanha/regra/banner, reduzindo fricção de navegação — só depois de dados de uso reais justificarem o investimento.

- Instrumentar telemetria de uso da página /copilot standalone (ex.: taxa de sessões que navegam para /copilot vindo de campaigns/rules/banners)
- Definir o gatilho mensurável: ex. ≥X% das sessões de edição de regra/campanha navegam manualmente para /copilot dentro de Y segundos
- Sob gatilho confirmado: extrair ChatPanel para um launcher contextual reusável (mesmo componente, novo ponto de montagem), sem duplicar lógica de HITL/SSE
- Reabre a avaliação de Zustand (diferido em E10): um launcher contextual sobrevivendo à navegação entre /campaigns, /rules, /banners etc. é o primeiro estado de UI genuinamente cross-route do console — até aqui, TanStack Query (server state) + useState local bastam

**Subagente:** `frontend-bff-engineer` · **Doc:** `§2.5` · `ADR-0003 (J5)` · **Gate:** tech-lead-architect — valida o número medido antes de aprovar o investimento de UX (regra de ouro: tecnologia/feature pesada só sob medição) · **Depende de:** E5, E11 · **Bloqueador:** requer tráfego real em produção (E11) para medir o sinal de uso que justifica o investimento — não mensurável em ambiente local/CI

##### E13 · Assinatura on-chain pelo anunciante no front (fora de escopo padrão, sob spec AEV/BND) — ⏳ gated

Registrar o limite explícito: wagmi/viem/WalletConnect só entram no cliente se a spec oficial de AEV/BND exigir autocustódia/assinatura on-chain pelo anunciante (E.9 do ADR-0004) — hoje a plataforma custodia e move por conta dele, e este componente permanece sem cripto no cliente por padrão.

- Nenhuma tarefa de código a fazer agora — este item existe só para deixar o limite documentado e não ser reaberto por engano
- Se a spec confirmar self-custody do anunciante: abrir novo ADR sucessor definindo o fluxo de assinatura, dono payments-crypto-engineer com este addon consumindo só a UI de conexão de carteira

**Subagente:** `payments-crypto-engineer` · **Doc:** `ADR-0004 E.9` · `§2.5` · `§3 (q.9)` · **Gate:** security-reviewer + payments-crypto-engineer — nenhuma lib de carteira entra no front sem a spec confirmada · **Depende de:** — · **Bloqueador:** bloqueio de produto: spec oficial de AEV/BND ainda não define se o anunciante assina on-chain ou se a plataforma custodia por ele (ADR-0004 §3 q.9, sem resposta)

</details>

**→ Próximo plano deste addon.** Plano imediato (código puro) já **FECHADO**: (E9, 22ª onda) middleware fail-closed em produção sem segredo real, gate `security-reviewer` PASS; (E10, 23ª onda) gap de stack §2.5 — bump Next 16.2.10/React 19.2.7, `next lint`→eslint flat, alicerce shadcn/ui, **a11y-ci mecânico com axe-core + `puppeteer-core` (Chrome do sistema, SEM Playwright — decisão do tech-lead)** + focus-trap do modal HITL. Zustand e Vercel AI SDK v5 **diferidos com gatilho documentado** (não reabrir arquitetura). Plano de ativação em produção (E11): injetar `SESSION_SECRET`/cookie de sessão real via OpenBao, preencher FQDNs e concluir o cutover que liga o console ao BFF↔decision real sob tráfego real — bloqueado por infra viva (fora do meu poder aplicar aqui). Plano sucessor pós-F3 sob gatilho medido (E12): embutir o copiloto contextualmente dentro dos builders (campanhas/regras/banners) em vez de só na página `/copilot` dedicada, gatilho = dados de uso em produção mostrando taxa de navegação manual para `/copilot` a partir de outras páginas. E13 (assinatura on-chain no front) permanece fora do meu escopo por padrão, sob spec de produto (ADR-0004 E.9), dono `payments-crypto-engineer`.

---

<a id="37"></a>

### 3.7 Pagamentos multi-trilho (fiat + cripto + AEV/BND) — services/payments, internal/chainconnector, db/asset_registry, db/compliance

**Subagente-dono:** `payments-crypto-engineer`  
**Camada de documentação:** docs/stack-tecnologico.md §2.6 (pagamentos) + §3 (Aevum/Bond plugáveis); docs/documentacao-tecnica.md DA-10/CA-7; docs/adr/0004-fase-3-sequenciamento-ia-avancada-cripto.md §C/§D/§E/§F/§H  
**Caminhos:** `services/payments/` · `internal/chainconnector/` · `db/asset_registry/migrations/` · `db/compliance/migrations/` · `proto/adserver/payments/v1/payments.proto` · `platform/cells/pci/` · `platform/cells/aml-kyc/` · `bff/src/routers/payments.ts` · `bff/src/adapters/postgres-payments.ts` · `docs/ops/go-live-runbook.md` · `docs/adr/0004-fase-3-sequenciamento-ia-avancada-cripto.md`  

**Estado atual.** Todo o trilho multi-trilho está código-completo e provado na main: K0 (ChainConnector + esqueleto services/payments + proto + células), K3 (ledger cripto Go), K4 (Stripe/Asaas/MercadoPago), K5 (Safe multisig EVM real via JSON-RPC + USDC), K6 (compliance: Sumsub + Chainalysis fail-closed + Travel Rule + cofre PII cifrado KMS-envelope), K7 (BFF/UI status-only, RLS real via PostgresPaymentsAdapter). Hardening pós-onda fechou 2 CRITICAL de segurança (RLS ausente no ledger, TenantID sem validação) e o smoke e2e de pagamentos roda PASS=20. Só restam: (a) ativação com segredos/infra reais (Stripe/Asaas/Sumsub/Chainalysis/OpenBao/FQDNs), travada pelo runbook; (b) sucessores sob gatilho medido (Fireblocks/AUM, AEV/BND/spec, chain não-EVM, oráculo de preço/liquidez) — nenhum deles é código pendente na main.

| Etapa | Título | Status | Subagente | Âncoras de doc | Bloqueador |
|---|---|---|---|---|---|
| `E0` | Fundações cripto — ChainConnector, esqueleto services/payments, proto e células (K0) | ✅ concluída | `payments-crypto-engineer` | `ADR-0004 §C` · `ADR-0004 §G` · `ADR-0004 §H (K0)` · `TX-1` · `TX-2` · `DA-10` | — |
| `E1` | Ledger cripto + Asset Registry vivo + reconciliação (K3) | ✅ concluída | `payments-crypto-engineer` | `ADR-0004 §D` · `ADR-0004 §H (K3)` · `TX-2` · `DA-10` | — |
| `E2` | Trilho fiat — Stripe SAQ-A + Asaas/PIX + Mercado Pago failover (K4) | ✅ concluída | `payments-crypto-engineer` | `ADR-0004 §H (K4)` · `ADR-0004 §F` · `stack §2.6` | — |
| `E3` | Trilho cripto — Safe multisig + USDC via ChainConnector EVM real (K5) | ✅ concluída | `payments-crypto-engineer` | `ADR-0004 §C` · `ADR-0004 §E.5` · `ADR-0004 §E.8` · `ADR-0004 §H (K5)` | — |
| `E4` | Compliance — célula AML/KYC, Sumsub, Chainalysis, Travel Rule (K6) | ✅ concluída | `payments-crypto-engineer` | `ADR-0004 §E.10` · `ADR-0004 §F` · `ADR-0004 §H (K6)` · `TX-3` · `DA-11` | — |
| `E5` | BFF de pagamentos — status-only, cripto fora do cliente (K7) | ✅ concluída | `payments-crypto-engineer` | `ADR-0004 §H (K7)` · `TX-2` · `TX-3` · `stack §2.5` | — |
| `E6` | Verificação pré-go-live: RLS provada, smoke e2e, runbook | ✅ concluída | `payments-crypto-engineer` | `ADR-0004 §H` · `TX-3` · `DA-11` · `TX-2` | — |
| `E7` | Ativação em produção — segredos vivos, células reais, cutover do trilho | ⏳ gated | `payments-crypto-engineer` | `docs/ops/go-live-runbook.md §3` · `ADR-0004 §F` · `ADR-0004 §H` | infra real (cluster cloud vivo, contas Stripe/Asaas/Sumsub/Chainalysis reais, OpenBao operacional, KMS/HSM real) — nenhum código pendente |
| `E8` | Habilitação AEV/BND — scale oficial + classificação regulatória (spec de produto) | ⏳ gated | `payments-crypto-engineer` | `stack-tecnologico.md §3 (q.2, q.3, q.5, q.7)` · `ADR-0004 §E.2` · `ADR-0004 §E.3` · `ADR-0004 §E.5` · `ADR-0004 §E.7` · `ADR-0004 (Gatilho de reabertura)` | spec de produto (decimais oficiais, classificação regulatória MiCA/BACEN/CVM, definição contratual de quem detém o supply) — bloqueio de produto, não de código |
| `E9` | Fireblocks (MPC) sob AUM — upgrade de custódia pós-F3 | ⏳ gated | `payments-crypto-engineer` | `ADR-0004 §C (Gatilho de reabertura: Fireblocks)` · `stack §2.6` · `stack §5` | AUM medido em produção ainda não existe (gatilho quantitativo, não atingido) — nenhum código pendente, FIREBLOCKS_API_KEY já mapeado no runbook mas não ativado por design |
| `E10` | Chain própria não-EVM — sucessor sob spec de produto | ⏳ gated | `payments-crypto-engineer` | `stack-tecnologico.md §3 (q.1)` · `ADR-0004 §E.1` · `ADR-0004 §H (K0, escopo aberto)` | spec de produto ainda não define chain própria não-EVM (bloqueio de produto) — default atual é ERC-20/EVM (E1/E3 já entregues) |
| `E11` | Oráculo de preço (Chainlink/Pyth) sob liquidez — sucessor pós-F3 | ⏳ gated | `payments-crypto-engineer` | `stack-tecnologico.md §3 (q.4)` · `ADR-0004 §E.4` · `ADR-0004 §E.6 (liquidez/ramp)` | liquidez real (desk/exchange trocando AEV/BND↔fiat/stablecoin) ainda não existe — hoje é premissa de crédito fechado no ecossistema (E.6) |

<details><summary><strong>Detalhamento das etapas</strong> (objetivo · tarefas · gate · dependências)</summary>

##### E0 · Fundações cripto — ChainConnector, esqueleto services/payments, proto e células (K0) — ✅ concluída

Estabelecer a interface única ChainConnector, o serviço de pagamentos default-off, o contrato proto BACKWARD-compat e as células de segregação, sem tocar o hot path.

- internal/chainconnector/connector.go: interface ChainConnector (watchDeposits/getBalance/buildPayout/confirmations) + EVMStub determinístico para testes
- services/payments/cmd/payments/main.go: esqueleto do binário, default-off sem segredo em git/imagem
- proto/adserver/payments/v1/payments.proto: eventos de pagamento/custódia BACKWARD-compat, reusando Money de proto/adserver/money/v1
- platform/cells/pci/ e platform/cells/aml-kyc/: Cilium deny-all de escopo mínimo
- Validar AEV/BND seedadas no Asset Registry com enabled=false, scale=NULL, CHECKs ativos

**Subagente:** `payments-crypto-engineer` · **Doc:** `ADR-0004 §C` · `ADR-0004 §G` · `ADR-0004 §H (K0)` · `TX-1` · `TX-2` · `DA-10` · **Gate:** parity-golden-test-guardian PASS + security-reviewer APROVADO (Cilium deny-all correto, services/payments default-off sem segredo) + privacy-compliance-auditor APROVADO (0 PII) + money-ledger-guardian PASS (Money/minor-units sem float) — 1ª onda Fase 3, README linhas 197-219 · **Depende de:** —

##### E1 · Ledger cripto + Asset Registry vivo + reconciliação (K3) — ✅ concluída

Dar ao services/payments acesso Go ao ledger double-entry existente para postings cripto idempotentes, sem migração de schema, com reconciliação que abre exceção e nunca autocorrige.

- internal/ledger/ (camada Go): par de postings idempotente, depósito pending→posted na finalidade, saldo derivado (nunca gravado direto)
- Câmbio explícito (DA-10): AEV/BND↔USDC/fiat só como par de postings com taxa registrada, nunca implícito
- db/ledger/migrations/0002: reconciliation_exceptions com RLS
- Estorno auditável para reorg (novo par de postings, nunca edição)

**Subagente:** `payments-crypto-engineer` · **Doc:** `ADR-0004 §D` · `ADR-0004 §H (K3)` · `TX-2` · `DA-10` · **Gate:** money-ledger-guardian APROVADO (int64↔NUMERIC via math/big, 0 float, invariantes contábeis da Fase 1 preservadas, AEV/BND travados por CHECK) — 2ª onda Fase 3, README linhas 221-246 · **Depende de:** E0

##### E2 · Trilho fiat — Stripe SAQ-A + Asaas/PIX + Mercado Pago failover (K4) — ✅ concluída

Integrar os provedores fiat atrás da interface tipada, com tokenização client-side e webhooks verificados, na célula PCI de escopo mínimo.

- services/payments/internal/stripe: Payment Intents/Billing/Tax, cartão nunca no backend (SAQ-A)
- services/payments/internal/asaas: PIX (QR dinâmico, Pix Automático p/ Tenancy, conciliação txid/E2E)
- services/payments/internal/mercadopago: failover
- services/payments/internal/fiat/failover.go: orquestração de fallback
- Webhooks com assinatura tempo-constante + anti-replay; célula PCI (egress allowlist Stripe, Kyverno exige Vault Agent)

**Subagente:** `payments-crypto-engineer` · **Doc:** `ADR-0004 §H (K4)` · `ADR-0004 §F` · `stack §2.6` · **Gate:** security-reviewer APROVADO (PCI não escapa da célula; webhooks verificados; achados HIGH/MEDIUM remediados na mesma janela — path de webhook, injeção form-body, SSRF MercadoPago); money-ledger-guardian APROVADO (par de postings idempotente) — 2ª onda Fase 3, README linhas 221-246 · **Depende de:** E0, E1

##### E3 · Trilho cripto — Safe multisig + USDC via ChainConnector EVM real (K5) — ✅ concluída

Implementar a primeira encarnação real do ChainConnector (EVM, JSON-RPC enxuto, sem go-ethereum), com custódia Safe multisig e depósito pending até finalidade via webhook.

- internal/chainconnector/evm_rpc.go + evm_safe.go: watchDeposits/getBalance/buildPayout/confirmations reais sobre Safe multisig
- uint256→int64 com checagem de overflow (sem perda de precisão)
- services/payments/internal/crypto/safe_webhook.go: webhook do custodiante como fonte de finalidade (preferido a lógica de reorg própria)
- Reorg → estorno auditável (reusa E1)
- USDC (Circle Mint) como stablecoin/ramp

**Subagente:** `payments-crypto-engineer` · **Doc:** `ADR-0004 §C` · `ADR-0004 §E.5` · `ADR-0004 §E.8` · `ADR-0004 §H (K5)` · **Gate:** money-ledger-guardian APROVADO (uint256→int64 com overflow-check, scale do Asset Registry nunca hardcode); security-reviewer APROVADO — 2ª onda Fase 3, README linhas 221-246 · **Depende de:** E0, E1

##### E4 · Compliance — célula AML/KYC, Sumsub, Chainalysis, Travel Rule (K6) — ✅ concluída

Isolar KYC/KYB e screening on-chain em cofre pseudônimo dedicado, com fail-closed no bloqueio de depósito/payout sob risco.

- db/compliance/migrations: cofre PII/KYC pseudônimo referenciado por tenant_id, RLS fail-closed
- services/payments/internal/sumsub: KYC/KYB
- services/payments/internal/chainalysis: screening on-chain fail-closed (sanção/risco bloqueia depósito/payout)
- services/payments/internal/travelrule: Travel Rule no trilho cripto
- services/payments/internal/kmsenvelope: cifra AES-256-GCM versionada (v1$) das colunas de PII, fail-closed antes do INSERT
- platform/cells/aml-kyc: segregação de rede (Cilium deny-all)

**Subagente:** `payments-crypto-engineer` · **Doc:** `ADR-0004 §E.10` · `ADR-0004 §F` · `ADR-0004 §H (K6)` · `TX-3` · `DA-11` · **Gate:** privacy-compliance-auditor APROVADO (screening fail-closed enforçado, 0 PII em ledger/telemetria/logs, RLS canônica adserver.tenant_id fail-closed, célula isolada) — 2ª onda Fase 3, README linhas 221-246; hardening pós-onda fechou tenant_id de payout e versionamento de cifra · **Depende de:** E0, E3

##### E5 · BFF de pagamentos — status-only, cripto fora do cliente (K7) — ✅ concluída

Expor status de pagamento/saldo ao console via BFF, sem aritmética monetária no cliente e sem cripto (wagmi/viem) por padrão.

- bff/src/routers/payments.ts: router só-leitura, tenant via ctx autenticado, sem IDOR
- bff/src/adapters/postgres-payments.ts: PostgresPaymentsAdapter real com RLS por request (set_config adserver.tenant_id em transação) + WHERE defense-in-depth
- Money como string DECIMAL + rótulo de moeda (nunca Number/bigint aritmético no front)
- db/ledger/migrations/0003: RLS em accounts/journal_entries/postings + view security_invoker

**Subagente:** `payments-crypto-engineer` · **Doc:** `ADR-0004 §H (K7)` · `TX-2` · `TX-3` · `stack §2.5` · **Gate:** security-reviewer APROVADO (sem segredo/PII no front, sem IDOR/forja de tenant, sem cripto no cliente) — 2ª onda Fase 3, README linhas 221-246; 2 CRITICAL de RLS remediados no hardening (README 4ª onda, linhas 267-291) · **Depende de:** E2, E3

##### E6 · Verificação pré-go-live: RLS provada, smoke e2e, runbook — ✅ concluída

Provar (não apenas afirmar) os invariantes de isolamento por tenant e o fluxo fim-a-fim do trilho fora do hot path, com stubs, antes do cutover real.

- db/ledger/tests/rls_isolation_test.sql: prova WITH CHECK contra INSERT/UPDATE com tenant_id forjado
- deploy/local/smoke-payments.sh: par de postings idempotente, pending→finalidade, reconciliação abre-exceção, status via BFF (PASS=20 registrado)
- docs/ops/go-live-runbook.md: ordem de migrações, segredos por célula, FQDNs, checklist dos 4 gates, rollback

**Subagente:** `payments-crypto-engineer` · **Doc:** `ADR-0004 §H` · `TX-3` · `DA-11` · `TX-2` · **Gate:** money-ledger-guardian APROVADO (3 CRITICAL de RLS remediados + bloqueio de precisão NUMERIC(40,18) corrigido); security-reviewer APROVADO; privacy-compliance-auditor APROVADO; parity-golden-test-guardian PASS — 4ª onda Fase 3, README linhas 267-291 · **Depende de:** E1, E2, E3, E4, E5

##### E7 · Ativação em produção — segredos vivos, células reais, cutover do trilho — ⏳ gated

Ligar o trilho de pagamentos já código-completo sob infra e segredos reais: chaves Stripe/Asaas/Sumsub/Chainalysis via OpenBao, FQDNs das células PCI/AML-KYC, KMS/HSM real para o envelope de PII.

- Injetar via OpenBao: STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET, ASAAS_API_KEY (célula PCI); SUMSUB_API_KEY/SECRET_KEY, CHAINALYSIS_API_KEY (célula AML/KYC)
- Substituir o stub local de PII_ENVELOPE_KEY por chave gerada em KMS/HSM real (AWS KMS ou equivalente)
- Preencher FQDNs reais: ingress webhook Stripe (payments.hojex.io), ingress webhook Sumsub, egress Chainalysis — habilitar as Cilium NetworkPolicies com FQDN literal
- Rodar smoke-payments.sh contra infra real + RLS isolation test contra Postgres de produção
- Confirmar checklist do go-live-runbook.md (4 gates: security/privacy/money/parity) antes do cutover

**Subagente:** `payments-crypto-engineer` · **Doc:** `docs/ops/go-live-runbook.md §3` · `ADR-0004 §F` · `ADR-0004 §H` · **Gate:** platform-infra-engineer aplica platform/ em cloud + tech-lead-architect confirma checklist do runbook antes do cutover; security-reviewer + privacy-compliance-auditor + money-ledger-guardian re-validam contra segredos/dados reais (não simulados) · **Depende de:** E6 · **Bloqueador:** infra real (cluster cloud vivo, contas Stripe/Asaas/Sumsub/Chainalysis reais, OpenBao operacional, KMS/HSM real) — nenhum código pendente

##### E8 · Habilitação AEV/BND — scale oficial + classificação regulatória (spec de produto) — ⏳ gated

Ligar as linhas AEV/BND já seedadas no Asset Registry (enabled=false hoje) assim que a spec oficial definir scale/classificação/supply, sem migração de schema.

- Receber a spec oficial de decimais (scale) — travada estruturalmente pelo CHECK assets_enabled_needs_scale_chk até então
- Confirmar classificação regulatória (utility/payment/security) com parecer jurídico — hoje premissa conservadora de utility/payment token com pipeline como se security-adjacent
- Definir quem detém as chaves de mint/burn do supply (hoje premissa: custody_mode='client_supply', cliente custodia o supply)
- Resolver mecânica do contrato (rebasing/fee/pause/blocklist/upgrade); se BND implicar cupom/maturidade, acionar tech-lead-architect para modelar accruals no ledger (par de postings periódico, sem UPDATE de saldo)
- UPDATE nas linhas AEV/BND: scale definido, enabled=true, cablear o ChainConnector EVM existente (E3) para o contrato real

**Subagente:** `payments-crypto-engineer` · **Doc:** `stack-tecnologico.md §3 (q.2, q.3, q.5, q.7)` · `ADR-0004 §E.2` · `ADR-0004 §E.3` · `ADR-0004 §E.5` · `ADR-0004 §E.7` · `ADR-0004 (Gatilho de reabertura)` · **Gate:** schema-contracts-steward confirma que nenhuma migração de schema é necessária (CHECK estrutural já protege); money-ledger-guardian valida scale/aritmética antes de enabled=true; tech-lead-architect decide se accruals são necessários (BND=cupom?) · **Depende de:** E1, E3 · **Bloqueador:** spec de produto (decimais oficiais, classificação regulatória MiCA/BACEN/CVM, definição contratual de quem detém o supply) — bloqueio de produto, não de código

##### E9 · Fireblocks (MPC) sob AUM — upgrade de custódia pós-F3 — ⏳ gated

Migrar de Safe multisig para Fireblocks quando o AUM sob custódia justificar o custo MPC, trocando apenas a implementação atrás da interface ChainConnector já pronta.

- Medir AUM real sob custódia em produção (pós-E7)
- Calcular ponto de corte: custo anual de Fireblocks < 1% do AUM E volume de payouts exigindo política MPC de assinatura
- Implementar EVMFireblocks (já referenciado no cabeçalho de connector.go como implementação prevista) trocando só a linha custody_mode no Asset Registry, sem reescrever o trilho
- Anexar o número de AUM medido a um ADR sucessor antes de ativar

**Subagente:** `payments-crypto-engineer` · **Doc:** `ADR-0004 §C (Gatilho de reabertura: Fireblocks)` · `stack §2.6` · `stack §5` · **Gate:** tech-lead-architect aprova o ADR sucessor com o número de AUM medido anexado; money-ledger-guardian confirma que a troca de custody_mode não altera aritmética/postings · **Depende de:** E7 · **Bloqueador:** AUM medido em produção ainda não existe (gatilho quantitativo, não atingido) — nenhum código pendente, FIREBLOCKS_API_KEY já mapeado no runbook mas não ativado por design

##### E10 · Chain própria não-EVM — sucessor sob spec de produto — ⏳ gated

Implementar uma segunda encarnação do ChainConnector com SDK nativo, caso a spec de AEV/BND (ou outro ativo) exija uma chain não-EVM.

- Aguardar definição de produto: AEV/BND (ou outro ativo) roda em chain própria não-EVM em vez de ERC-20/EVM (E.1 do ADR-0004)
- Implementar NativeChain (já referenciado no cabeçalho de connector.go como implementação prevista) com signer/indexer/confirmações próprios — único cenário que justifica infra própria
- Definir modelo de finalidade/reorg específico dessa chain (acopla a E.8 de confirmações)

**Subagente:** `payments-crypto-engineer` · **Doc:** `stack-tecnologico.md §3 (q.1)` · `ADR-0004 §E.1` · `ADR-0004 §H (K0, escopo aberto)` · **Gate:** tech-lead-architect confirma que a interface ChainConnector cobre o novo caso sem reabrir o layout; security-reviewer valida o novo signer/indexer · **Depende de:** E8 · **Bloqueador:** spec de produto ainda não define chain própria não-EVM (bloqueio de produto) — default atual é ERC-20/EVM (E1/E3 já entregues)

##### E11 · Oráculo de preço (Chainlink/Pyth) sob liquidez — sucessor pós-F3 — ⏳ gated

Substituir o preço administrado/manual de AEV/BND por feed de oráculo real quando existir ativo líquido com mercado formado.

- Medir liquidez real do ativo (existência de desk/exchange que troca AEV/BND↔fiat/stablecoin, volume, profundidade de book)
- Trocar a linha price_source de 'administered' para 'oracle_chainlink'/'oracle_pyth' no Asset Registry (sem migração)
- Implementar o conector de oráculo dentro da mesma fronteira plugável (ChainConnector ou serviço de preço dedicado)
- Manter price_governance como fallback documentado em caso de falha de feed

**Subagente:** `payments-crypto-engineer` · **Doc:** `stack-tecnologico.md §3 (q.4)` · `ADR-0004 §E.4` · `ADR-0004 §E.6 (liquidez/ramp)` · **Gate:** money-ledger-guardian valida a troca de price_source sem quebrar aritmética; tech-lead-architect aprova o ADR sucessor com evidência de liquidez · **Depende de:** E8 · **Bloqueador:** liquidez real (desk/exchange trocando AEV/BND↔fiat/stablecoin) ainda não existe — hoje é premissa de crédito fechado no ecossistema (E.6)

</details>

**→ Próximo plano deste addon.** Não há código pendente no addon: E0-E6 (K0, K3-K7 + hardening + verificação pré-go-live) estão código-completos e gate-verificados na main. O próximo movimento real é (a) E7 — ativação em produção: aplicar platform/ com o platform-infra-engineer, injetar segredos vivos (Stripe/Asaas/Sumsub/Chainalysis) via OpenBao, gerar PII_ENVELOPE_KEY em KMS/HSM real, preencher FQDNs das células PCI/AML-KYC, e rodar o checklist do go-live-runbook.md contra infra real antes do cutover. Em paralelo, três sucessores ficam à espera de gatilho medido, sem bloquear nada: E9 (Fireblocks quando o AUM sob custódia justificar o custo MPC — medir pós-E7), E8 (habilitar AEV/BND assim que a spec de produto definir scale/classificação/supply — bloqueio genuinamente de produto, não de código, protegido por CHECK estrutural), e E11/E10 (oráculo de preço sob liquidez real; chain não-EVM só se a spec exigir). Nenhum desses sucessores justifica trabalho de código hoje — a regra de ouro do stack (tecnologia pesada só sob medição) proíbe antecipá-los.

---

<a id="38"></a>

### 3.8 Infra, segurança, observabilidade e conformidade (plataforma-base)

**Subagente-dono:** `platform-infra-engineer`  
**Camada de documentação:** stack §2.7 (infra/segurança/observabilidade/conformidade)  
**Caminhos:** `platform/tofu/` · `platform/gitops/` · `platform/k8s/` · `platform/observability/` · `platform/secrets/openbao/` · `platform/cells/pci/` · `platform/cells/aml-kyc/` · `.github/workflows/platform.yml` · `make/platform.mk` · `docs/ops/go-live-runbook.md` · `deploy/local/`  

**Estado atual.** A plataforma-base está CÓDIGO-COMPLETA e verificada offline (`tofu validate`, `kubeconform`, `kyverno test`, `otelcol validate` — todos espelhados em `make platform-validate` e no CI `.github/workflows/platform.yml`, com SHA256 fixado nos downloads — M-1). Isso fecha a Fase 0 (plataforma-base) e os incrementos K0/K4/K6 do ADR-0004 (células PCI e AML/KYC). Duas ondas de hardening (11ª e 13ª, README) já corrigiram bugs de CI que mascaravam falha (pipefail ausente no tofu-validate; endpoints placeholder faltando no otel-validate). Os 2 gaps de código encontrados nesta varredura foram **fechados na 21ª onda (E8)**: (a) escrito `platform/secrets/openbao/policy-aml-kyc.hcl` (menor privilégio, espelhando `policy-pci.hcl`); (b) mandato item 4 agora **4/4** — pipeline de supply-chain (`.github/workflows/supply-chain.yml`: build → SBOM syft → cosign sign **keyless** → Trivy fail-CRITICAL → push ghcr) + Dockerfiles de produção (`deploy/docker/Dockerfile.go-service` reusa a receita hermética; `Dockerfile.copilot` novo) + ruleset Falco (`platform/observability/falco-rules.yaml`, ConfigMap) + o placeholder `REPLACE_WITH_COSIGN_PUBLIC_KEY` substituído por atestador keyless (OIDC/Fulcio/Rekor) em `kyverno-baseline.yaml`. **Correção de falso-positivo (mesma onda):** o próprio `make platform-validate` estava vermelho na main por 3 bugs pré-existentes não-E8 — httproute com filtro `type: RequestTimeout` inexistente (Gateway API), `otel-collector.yaml` (config nativa sem `kind`) varrido pelo kubeconform, e as `Policy` namespaced das células puladas como inválidas pelo kyverno 1.13.4 (filtro `namespaces` proibido) + JMESPath que erra em campo ausente. Todos corrigidos sem enfraquecer enforcement (provado por `kyverno apply`); gate agora **genuinamente verde** (tofu + kubeconform + kyverno-test + otel estrutural; eram 22/22 casos kyverno à época da 21ª onda, 26/26 em 2026-07-19 — **a contagem não é gate**, o exit code do alvo é).

| Etapa | Título | Status | Subagente | Âncoras de doc | Bloqueador |
|---|---|---|---|---|---|
| `E1` | Fundações IaC/GitOps: root OpenTofu + Argo CD app-of-apps + namespaces por célula | ✅ concluída | `platform-infra-engineer` | `§2.7` · `TX-3` · `CA-9` · `DA-12` | — |
| `E2` | Rede zero-trust: Cilium eBPF deny-all + Gateway API (Envoy Gateway) | ✅ concluída | `platform-infra-engineer` | `§2.7` · `TX-3` · `ADR-0004 §F` | — |
| `E3` | Supply chain & policy de admissão (Kyverno baseline + RBAC mínimo por célula) | ✅ concluída | `platform-infra-engineer` | `mandato item 4 (cosign+SBOM+Kyverno+Trivy+Falco)` · `§2.7` · `CA-9` | — |
| `E4` | Observabilidade 100% OTel com redação de PII antes de qualquer export (TX-5) | ✅ concluída | `platform-infra-engineer` | `TX-5` · `DA-11` | — |
| `E5` | Segredos OpenBao/Vault com menor privilégio por célula (dynamic secrets, Pod Identity) | ✅ concluída | `platform-infra-engineer` | `TX-3` · `§2.7` · `ADR-0004 §F` | — |
| `E6` | Segregação em células: PCI (escopo mínimo) + AML/KYC/Travel Rule | ✅ concluída | `platform-infra-engineer` | `ADR-0004 §F` · `ADR-0004 §H (K0, K4, K6)` | — |
| `E7` | Gate de CI `platform-validate` espelhando `make verify` (buf/no-float) para a plataforma | ✅ concluída | `platform-infra-engineer` | `mandato item 8` · `go-live-runbook.md §1` | — |
| `E8` | Fechar gaps de supply chain do mandato (cosign real + SBOM + Trivy + Falco) — construível agora, sem cloud | ✅ concluída (21ª onda) | `platform-infra-engineer` | `mandato item 4` · `§2.7` · `CA-9` | a construção dos Dockerfiles de produção por serviço é compartilhada com os donos de camada (decision-engine-engineer, data-platform-engineer, ml-optimization-engineer, payments-crypto-engineer, frontend-bff-engineer); platform-infra-engineer entrega o pipeline/template de CI, não a imagem de cada app |
| `E9` | Ativação em produção: cutover (aplicar platform/ em cloud real sob aprovação humana) | ⏳ gated | `platform-infra-engineer` | `go-live-runbook.md §1-§6, §8` · `§2.7` | infra real (cloud) + aprovação humana explícita — nenhuma ação de apply/cutover é autônoma neste papel |
| `E10` | Sucessor pós-Fase-3: escalar infra só sob gatilho medido (multi-região por célula; reavaliação Crossplane/vCluster; node pools GPU/Fireblocks/TigerBeetle) | ⏳ gated | `tech-lead-architect` | `mandato "Limites (regra de ouro)"` · `stack §5 (riscos: over-engineering)` · `ADR-0004 (Gatilho de reabertura)` | tráfego real / medição de produção — nenhum dos gatilhos acima foi atingido neste ambiente |

<details><summary><strong>Detalhamento das etapas</strong> (objetivo · tarefas · gate · dependências)</summary>

##### E1 · Fundações IaC/GitOps: root OpenTofu + Argo CD app-of-apps + namespaces por célula — ✅ concluída

Estabelecer o esqueleto de infraestrutura como código e o mecanismo de reconciliação (GitOps) que sustenta todas as camadas subsequentes, validável sem credenciais de cloud.

- Root `platform/tofu/main.tf` com provider AWS + locals de namespaces por célula (platform, delivery, ml, data, pci, aml) e plano de módulos network/eks/addons documentado (ainda comentado — cloud real entra em E9)
- `backend.tf`/`versions.tf`/`variables.tf`/`outputs.tf` mantendo o root `tofu validate`-limpo sem backend/credenciais
- Argo CD `AppProject` (`platform/gitops/bootstrap/project.yaml`) + `app-of-apps` (`platform/gitops/bootstrap/app-of-apps.yaml`)
- Apps filhas ordenadas por prefixo numérico: 00-namespaces, 10-netpol, 20-kyverno, 30/31-células
- `platform/k8s/namespaces.yaml` com labels de célula

**Subagente:** `platform-infra-engineer` · **Doc:** `§2.7` · `TX-3` · `CA-9` · `DA-12` · **Gate:** tech-lead-architect confirma que o layout espelha o roadmap (ADR-0002/0003/0004) sem reabrir arquitetura; `tofu validate` verde no CI · **Depende de:** —

##### E2 · Rede zero-trust: Cilium eBPF deny-all + Gateway API (Envoy Gateway) — ✅ concluída

Isolamento de rede por namespace/célula por padrão, com ingress explícito e mínimo por webhook.

- `platform/k8s/netpol/cilium-default-deny.yaml` + `allow-dns.yaml` (baseline global)
- `default-deny.yaml` + `allow-dns`/`allow-egress-*`/`allow-ingress-*` específicos por célula em `platform/cells/{pci,aml-kyc}/netpol/`
- `HTTPRoute` (Gateway API) para webhook Stripe (`platform/cells/pci/gateway/`) e webhook Sumsub (`platform/cells/aml-kyc/gateway/`)

**Subagente:** `platform-infra-engineer` · **Doc:** `§2.7` · `TX-3` · `ADR-0004 §F` · **Gate:** security-reviewer confirma deny-all aplicado + nenhuma porta extra sem allow explícito (checklist runbook §6); nota L-1/Ressalva-2 do runbook: comportamento de bloqueio real só é verificável em cluster vivo · **Depende de:** E1

##### E3 · Supply chain & policy de admissão (Kyverno baseline + RBAC mínimo por célula) — ✅ concluída

Bloquear na admissão imagens não assinadas/não versionadas e privilégios excessivos; RBAC de menor privilégio.

- `platform/k8s/policy/kyverno-baseline.yaml`: `verify-image-signatures` (cosign), `disallow-latest-tag`, `require-resource-limits`
- Kyverno por célula: `kyverno-pci.yaml` (proíbe hostPath/imagem sem digest), `kyverno-aml-kyc.yaml` (proíbe acesso a secret de outra célula, exige Vault Agent)
- `kyverno-test.yaml` + `test-resources.yaml` offline (asserts accept/reject) em cada diretório de policy
- `platform/k8s/rbac/baseline.yaml` + RBAC por célula (`pci-rbac.yaml`, `aml-kyc-rbac.yaml`)

**Subagente:** `platform-infra-engineer` · **Doc:** `mandato item 4 (cosign+SBOM+Kyverno+Trivy+Falco)` · `§2.7` · `CA-9` · **Gate:** security-reviewer PASS (checklist runbook §6: Kyverno baseline/PCI/AML-KYC); ressalva registrada — chave pública cosign é placeholder até E8 · **Depende de:** E1

##### E4 · Observabilidade 100% OTel com redação de PII antes de qualquer export (TX-5) — ✅ concluída

Telemetria centralizada (VictoriaMetrics/Grafana/Loki/Tempo) sem vazar PII — gate de privacidade objetivo e testável offline.

- `platform/observability/otel-collector.yaml`: pipelines traces/logs/metrics com `transform/redact-pii` + `redaction/allowlist-{traces,logs,metrics}` (`allow_all_keys: false` fail-closed nas **três** — `allowlist-metrics` entrou na 30ª onda)
- `make platform-otel-validate`: `otelcol validate` via imagem contrib pinada (SHA/tag fixados) + verificação estrutural fail-closed via `platform/observability/otel-pipeline-redaction-check.py` — **membresia** nos `.processors` de **todos** os pipelines de `service.pipelines` (não presença no arquivo, e não uma lista de nomes hardcoded)

**Subagente:** `platform-infra-engineer` · **Doc:** `TX-5` · `DA-11` · **Gate:** privacy-compliance-auditor PASS — todo pipeline contém redact-pii E allowlist do seu tipo, `allow_all_keys` falso em todas (checklist runbook §6) · **Depende de:** E1

##### E5 · Segredos OpenBao/Vault com menor privilégio por célula (dynamic secrets, Pod Identity) — ✅ concluída

Nenhum segredo estático em imagem/git; credenciais dinâmicas com TTL curto; KMS/HSM para chaves de pagamento.

- `policy-platform.hcl` (delivery/ml/data): leitura de KV v2 escopada + credenciais dinâmicas de Postgres + renovação/revogação do próprio lease
- `policy-pci.hcl`: escopo exclusivo do trilho fiat + `transit/encrypt`/`decrypt` para envelope de dados em repouso
- Padrão Vault Agent sidecar documentado por célula (`secrets/openbao-auth.yaml`: ServiceAccount + ConfigMap de referência, zero valores de segredo)
- GAP encontrado nesta varredura: `platform/secrets/openbao/policy-aml-kyc.hcl` é citado em comentário no `openbao-auth.yaml` da célula aml-kyc mas o arquivo real nunca foi escrito — falta fechar.

**Subagente:** `platform-infra-engineer` · **Doc:** `TX-3` · `§2.7` · `ADR-0004 §F` · **Gate:** security-reviewer + privacy-compliance-auditor validam a policy antes do go-live (nota do próprio arquivo); GAP do policy-aml-kyc.hcl vira tarefa de E8 · **Depende de:** E1

##### E6 · Segregação em células: PCI (escopo mínimo) + AML/KYC/Travel Rule — ✅ concluída

Isolamento regulatório com fronteiras de rede rígidas, validável por QSA, sem dependência do hot path.

- `platform/cells/pci/*` completo: namespace, netpol (default-deny + allow-dns/egress-stripe/ingress-stripe-webhook), kyverno+test, rbac, gateway httproute-webhook, secrets ref
- `platform/cells/aml-kyc/*` completo: idem + allow-egress-{sumsub,chainalysis,travel-rule,postgres-compliance}, gateway httproute-sumsub-webhook
- GitOps: `platform/gitops/apps/30-cell-pci.yaml` / `31-cell-aml-kyc.yaml` (Argo CD automated prune+selfHeal, ServerSideApply)

**Subagente:** `platform-infra-engineer` · **Doc:** `ADR-0004 §F` · `ADR-0004 §H (K0, K4, K6)` · **Gate:** security-reviewer PASS (PCI não escapa da célula) + privacy-compliance-auditor PASS (PII isolada em compliance, referenciada por tenant_id pseudônimo) · **Depende de:** E2, E3, E5

##### E7 · Gate de CI `platform-validate` espelhando `make verify` (buf/no-float) para a plataforma — ✅ concluída

CI objetiva, determinística, sem credenciais/cluster vivo — gatekeeper das fases antes de qualquer merge que toque platform/.

- `.github/workflows/platform.yml`: instala tofu/kubeconform/kyverno com verificação SHA256 (M-1) e roda os 4 alvos make
- `make/platform.mk`: modo `PLATFORM_STRICT` (CI falha na ausência de ferramenta) vs. skip gracioso local
- Correções de bugs de mascaramento de falha já fechadas: `set -eo pipefail` no tofu-validate (achado HIGH, 11ª onda); endpoints placeholder no otel-validate; achado LOW residual do `grep -v "^$"` fechado na 13ª onda

**Subagente:** `platform-infra-engineer` · **Doc:** `mandato item 8` · `go-live-runbook.md §1` · **Gate:** tech-lead-architect confirma que o CI é gate objetivo (não cosmético); security-reviewer validou os achados HIGH/LOW do pipeline de instalação · **Depende de:** E1, E2, E3, E4, E5, E6

##### E8 · Fechar gaps de supply chain do mandato (cosign real + SBOM + Trivy + Falco) — construível agora, sem cloud — ✅ concluída (21ª onda)

Completou o item 4 do mandato (antes só Kyverno tinha código real) + fechou o gap do `policy-aml-kyc.hcl`.

- ✅ Escrito `platform/secrets/openbao/policy-aml-kyc.hcl` (menor privilégio, escopo `aml-kyc/*` — KV `aml-kyc/data/{sumsub,chainalysis,custody}/*`, DB dinâmico `aml-kyc/db/compliance`, transit `aml-kyc`, lease próprio; nega por omissão `pci/*` e `platform/*`), espelhando `policy-pci.hcl`; tabela em `openbao/README.md` atualizada
- ✅ Workflow `.github/workflows/supply-chain.yml` (matrix dos 5 serviços): build → SBOM (syft SPDX+CycloneDX) → Trivy fail-CRITICAL/HIGH (antes do push) → push `ghcr.io` → cosign sign **keyless**. Job de PR faz build+scan sem publicar; publish só em tag/release. Dockerfiles de produção: `deploy/docker/Dockerfile.go-service` (reusa a receita hermética de `deploy/local/Dockerfile` para os 4 Go) + `deploy/docker/Dockerfile.copilot` (1º do copiloto Python)
- ✅ Adotado **cosign keyless** (OIDC/Fulcio/Rekor) em vez de par de chaves estático (não introduz segredo a versionar/rotacionar; §2.7 "nada estático em git"); `REPLACE_WITH_COSIGN_PUBLIC_KEY` substituído por atestador `keyless` (issuer GitHub OIDC + subject do workflow + rekor) em `kyverno-baseline.yaml`; nota em `kyverno-test.yaml` reconciliada
- ✅ Ruleset Falco `platform/observability/falco-rules.yaml` (ConfigMap): exec privilegiado (geral + reforçado nas células), escrita fora de paths esperados e binários/syscalls suspeitos nas células PCI/AML — deploy do daemonset é E9, o ruleset é código hoje

**Gate provado (21ª onda):** verificação adversarial **`security-reviewer` PASS** (cosign não é mais placeholder; SBOM+Trivy-fail+cosign cobrem supply chain, Falco cobre runtime; nenhum segredo versionado; `policy-aml-kyc.hcl` menor-privilégio) + **`privacy-compliance-auditor` PASS** (isolamento de célula, sem PII, TX-5 intacto) + **`tech-lead-architect` PASS** (keyless é config não infra pesada; mandato item 4 = 4/4 genuíno). **Falso-positivo pré-existente corrigido na mesma onda:** `make platform-validate` estava vermelho na main (não por E8) — httproute com filtro `RequestTimeout` inexistente → movido para `spec.rules[].timeouts.request`; `otel-collector.yaml` excluído do kubeconform (config nativa validada por `platform-otel-validate`); `Policy` namespaced das células destravadas (removido filtro `namespaces` proibido pelo kyverno 1.13.4 + JMESPath null-safe + `label_match`), passando a **carregar e enforçar** (antes eram puladas como inválidas). Enforcement provado idêntico por `kyverno apply` (mesmos pods reprovados; erros viraram pass); gate agora **genuinamente verde** (tofu + kubeconform + kyverno-test + otel estrutural; eram 22/22 casos kyverno à época da 21ª onda, 26/26 em 2026-07-19 — **a contagem não é gate**, o exit code do alvo é), verificado de 1ª mão.

**Subagente:** `platform-infra-engineer` · **Doc:** `mandato item 4` · `§2.7` · `CA-9` · **Gate:** security-reviewer confirma que SBOM/Trivy/Falco cobrem supply chain E runtime e que cosign deixou de ser placeholder · **Depende de:** E3 · **Bloqueador:** a construção dos Dockerfiles de produção por serviço é compartilhada com os donos de camada (decision-engine-engineer, data-platform-engineer, ml-optimization-engineer, payments-crypto-engineer, frontend-bff-engineer); platform-infra-engineer entrega o pipeline/template de CI, não a imagem de cada app

##### E9 · Ativação em produção: cutover (aplicar platform/ em cloud real sob aprovação humana) — ⏳ gated

Transformar o esqueleto validado offline (tofu validate/kubeconform/kyverno test/otelcol validate) em cluster EKS vivo com Cilium/Argo CD/Kyverno/OTel/OpenBao operando de fato.

- Provisionar conta(s) cloud, incluindo contas separadas para as células PCI e AML/KYC (runbook §1)
- `tofu plan`/`apply` real dos módulos network/eks/addons (hoje só planejados em comentário no `main.tf`) — SOMENTE sob aprovação humana explícita, nunca autônomo
- Bootstrap do Argo CD real + sync do app-of-apps; instalar OpenBao server real + Pod Identity (IRSA/OIDC)
- Gerar chaves KMS/HSM reais (PII_ENVELOPE_KEY, Stripe/Asaas/Fireblocks/Sumsub/Chainalysis — runbook §3) e preencher os FQDNs placeholder das células (runbook §4: webhook PCI/AML-KYC, egress Chainalysis/Travel Rule/Safe RPC)
- Rodar a sequência de smoke pré-cutover completa (runbook §5, passos 1–6) contra a infra real e validar comportamentalmente o deny-all do Cilium entre namespaces (L-1/Ressalva-2 do runbook — só testável em cluster vivo, ex.: netpol-tester/Sonobuoy)
- Estabelecer o snapshot de saúde contínuo (p50/p99 do hot path, fill rate, sinks Redpanda/ClickHouse) e o procedimento de rollback por camada (runbook §8) como operação de SRE

**Subagente:** `platform-infra-engineer` · **Doc:** `go-live-runbook.md §1-§6, §8` · `§2.7` · **Gate:** security-reviewer + privacy-compliance-auditor + money-ledger-guardian (checklist completo do runbook §6) + aprovação humana explícita para qualquer apply em cloud — regra de ouro deste addon: sem ações destrutivas ou de apply autônomas · **Depende de:** E1, E2, E3, E4, E5, E6, E7 · **Bloqueador:** infra real (cloud) + aprovação humana explícita — nenhuma ação de apply/cutover é autônoma neste papel

##### E10 · Sucessor pós-Fase-3: escalar infra só sob gatilho medido (multi-região por célula; reavaliação Crossplane/vCluster; node pools GPU/Fireblocks/TigerBeetle) — ⏳ gated

Aplicar a regra de ouro à própria camada de plataforma: nenhuma tecnologia pesada de infra entra por aspiração, só por número medido em produção.

- Medir necessidade real de multi-região por célula (ex.: p99 cross-region, requisito de residência de dados por nova jurisdição) antes de propor qualquer módulo Tofu multi-região
- Reavaliar Crossplane/vCluster somente se o número de tenants/times operando namespaces isolados justificar self-service de infra — hoje explicitamente evitado ("excesso para o estágio atual")
- Provisionar node pool GPU no EKS SE E SOMENTE SE K8 (uplift A/B do deep ranking) for promovido em produção — GPU nunca por aspiração (ADR-0004 §A/Gatilho)
- Adicionar rede/egress para Fireblocks SE o AUM sob custódia justificar a migração de Safe multisig (ADR-0004 §C/Gatilho)
- Avaliar módulo de storage para TigerBeetle SE gargalo de escrita financeira do Postgres double-entry for provado sob carga real (ADR-0004 §D/Gatilho)

**Subagente:** `tech-lead-architect` · **Doc:** `mandato "Limites (regra de ouro)"` · `stack §5 (riscos: over-engineering)` · `ADR-0004 (Gatilho de reabertura)` · **Gate:** tech-lead-architect abre ADR sucessor só com o número medido anexado (mesmo padrão dos gatilhos já documentados em ADR-0004 C/D e K8) · **Depende de:** E9 · **Bloqueador:** tráfego real / medição de produção — nenhum dos gatilhos acima foi atingido neste ambiente

</details>

**→ Próximo plano deste addon.** E8 **fechado na 21ª onda** (item 4 do mandato agora 4/4: pipeline supply-chain com SBOM+cosign-keyless+Trivy, Dockerfiles de produção, ruleset Falco, `policy-aml-kyc.hcl`; + falso-positivo pré-existente do `make platform-validate` corrigido → gate genuinamente verde). Não há mais código de infra pendente em G0. Médio prazo (E9, sob aprovação humana): executar o cutover — aplicar `platform/` em cloud real, gerar segredos/KMS vivos, preencher FQDNs das células e rodar a sequência de smoke do runbook contra infra real, validando comportamentalmente o deny-all do Cilium (L-1) que hoje só é verificável estruturalmente. Longo prazo (E10, sob gatilho medido): só escalar multi-região, Crossplane/vCluster, node pool GPU, rede Fireblocks ou storage TigerBeetle quando um número de produção (não aspiração) justificar — cada gatilho já está registrado no ADR-0004 e será anexado ao ADR sucessor quando medido.

---

<a id="39"></a>

### 3.9 Dinheiro: tipo canônico, Asset Registry, ledger double-entry, billing

**Subagente-dono:** `money-ledger-guardian`  
**Camada de documentação:** stack TX-2 (dinheiro canônico, §2.6 pagamentos) + docs-técnica §4.9/CA-7 (precificação) + ADR-0002 (I1) + ADR-0004 §C/§D/§E/§H (K0/K3/K4/K5, AEV/BND)  
**Caminhos:** `contracts/money/money-type.md` · `contracts/money/asset-registry.md` · `contracts/money/asset-registry.seed.csv` · `contracts/lint/no-float.md` · `db/asset_registry/migrations/0001_asset_registry_up.sql` · `db/ledger/migrations/0001_ledger_schema_up.sql` · `db/ledger/migrations/0002_reconciliation_exceptions_up.sql` · `db/ledger/migrations/0003_ledger_rls_up.sql` · `db/ledger/tests/rls_isolation_test.sql` · `db/ledger/BILLING.md` · `internal/ledger/posting.go` · `internal/ledger/account.go` · `internal/ledger/asset_registry.go` · `internal/ledger/asset_loader_pg.go` · `internal/ledger/crypto.go` · `internal/ledger/recon.go` · `internal/ledger/doc.go` · `internal/ledger/ledger_test.go` · `internal/money/ecpm.go` · `internal/money/ecpm_test.go` · `data/iceberg/jobs/billing_batch_hourly.py` · `data/iceberg/jobs/test_billing_batch_hourly.py` · `.github/workflows/no-float.yml` · `scripts/ci/no-float-go.sh` · `scripts/ci/no-float-py.sh` · `scripts/ci/no-float-sql.sh` · `scripts/ci/no-float-data-sql.sh` · `docs/ops/go-live-runbook.md` · `docs/adr/0002-fase-1-sequenciamento-e-layout.md` · `docs/adr/0004-fase-3-sequenciamento-ia-avancada-cripto.md`  
**Incrementos fechados:** `I1 (ADR-0002) — ledger + Asset Registry + schema de config, CA-7 parcial (NUMERIC sem float; multi-moeda como rótulo)` · `K0 (ADR-0004 §H) — Asset Registry recebe AEV/BND como linhas (enabled=false, scale=NULL), validado sob CHECK, sem migração de schema` · `K3 (ADR-0004 §H) — internal/ledger vivo: postings idempotentes, pending→posted, câmbio explícito, estorno auditável, reconciliação que abre exceção` · `Hardening 4ª onda (README) — CRITICAL de RLS ledger corrigido (WITH CHECK ausente permitia INSERT/UPDATE cross-tenant)` · `13ª onda (README) — gate no-float-py corrigido (glob nominal casava 0 arquivos; regex de nome monetário ganhou precisão)` · `15ª onda (README, commit 04f951a) — calc_cpm_amount corrigido para floor(imp/1000)*rate (BILLING.md §4.1), fatura sub-mille eliminada`

**Estado atual.** Todo o escopo de código do addon está código-completo e provado na main: tipo Money (contrato TX-2 + no-float lint nos 6 guards Proto/Go/TS/Python/SQL), Asset Registry plugável com scale autoritativo e CHECKs estruturais, ledger double-entry Postgres (par de postings idempotente, saldo derivado, sum(debit)=sum(credit) via constraint trigger), pacote Go internal/ledger (RecordEntry/EnsureAccount/Balance/AssetLoader+cache/reconciliação/operações cripto — 21 testes cobrindo idempotência, balanceamento, ativo desabilitado, FX explícito, estorno, reconciliação sem autocorreção), billing CPM/CPC/CPA/Tenancy (BILLING.md + job Python com floor(imp/1000) corrigido na 15ª onda), RLS por tenant no ledger (WITH CHECK corrigido na 4ª onda), e as linhas AEV/BND seedadas e travadas por CHECK (`assets_enabled_needs_scale_chk`). Isto fecha os incrementos I1 (Fase 1), K0 e K3 (Fase 3) do lado do money-ledger-guardian, mais o hardening das ondas 4/13/15 do README. Não há pendência de código identificada — só ativação sob infra real e bloqueios de spec de produto (AEV/BND) ou gatilho de gargalo medido (TigerBeetle).

| Etapa | Título | Status | Subagente | Âncoras de doc | Bloqueador |
|---|---|---|---|---|---|
| `E1` | Tipo canônico Money + lint anti-float (Fase 0) | ✅ concluída | `money-ledger-guardian` | `TX-2` · `DA-10` · `contracts/money/money-type.md` · `contracts/lint/no-float.md` | — |
| `E2` | Asset Registry plugável — scale autoritativo + seed + CHECKs estruturais | ✅ concluída | `money-ledger-guardian` | `DA-10` · `§2.6` · `ADR-0004 §D` · `ADR-0004 §H (K0)` · `contracts/money/asset-registry.md` | — |
| `E3` | Ledger double-entry Postgres — schema estrutural (accounts/journal_entries/postings) | ✅ concluída | `money-ledger-guardian` | `§2.6` · `ADR-0002 (I1)` · `TX-2` | — |
| `E4` | Pacote Go internal/ledger — operações de captura, cripto e reconciliação | ✅ concluída | `money-ledger-guardian` | `ADR-0004 §C` · `ADR-0004 §D` · `ADR-0004 §H (K3)` · `TX-2` · `DA-10` | — |
| `E5` | Billing CPM/CPC/CPA/Tenancy — modelo de postings + job batch Iceberg | ✅ concluída | `money-ledger-guardian` | `§4.9` · `CA-7` · `DA-7` · `TX-6` · `db/ledger/BILLING.md` | — |
| `E6` | RLS por tenant no ledger + tabela de exceções de reconciliação | ✅ concluída | `money-ledger-guardian` | `TX-3` · `DA-11` · `ADR-0004 §D` | — |
| `E7` | Gate de CI no-float — piso, não teto (6 guards: Proto/Go/TS/Python/SQL + detecção de furos) | ✅ concluída | `money-ledger-guardian` | `TX-2` · `.github/workflows/no-float.yml` | — |
| `E8` | Ledger cripto vivo (K3) sob ChainConnector — smoke ponta-a-ponta com stubs | ✅ concluída | `money-ledger-guardian` | `ADR-0004 §C` · `ADR-0004 §H (K3, K5)` · `DA-10` | — |
| `E9` | Ativação em produção — migrations reais + reconciliador contra Iceberg real + chaves vivas | ⏳ gated | `money-ledger-guardian` | `docs/ops/go-live-runbook.md §2.3` · `docs/ops/go-live-runbook.md §6 (checklist money-ledger-guardian)` · `docs/ops/go-live-runbook.md §7` | infra real (plataforma EKS/OpenTofu aplicada, chaves OpenBao/KMS vivas, dados reais no Iceberg via data-platform-engineer, aprovação humana de cutover) — não-código, fora do controle deste addon |
| `E10` | Sucessor pós-F3 sob gatilho medido — habilitar AEV/BND com scale oficial (ADR-0004 §E.2) | ⏳ gated | `money-ledger-guardian` | `ADR-0004 §E.2` · `ADR-0004 §D` · `contracts/money/asset-registry.md §5` | spec de produto (scale/decimais oficiais de AEV/BND) — bloqueio que código nenhum resolve, ver §3 q.2 do stack doc |
| `E11` | Sucessor pós-F3 sob gatilho medido — TigerBeetle só sob gargalo de escrita provado (ADR-0004 §D) | ⏳ gated | `money-ledger-guardian` | `§2.6` · `§5 (riscos)` · `ADR-0004 §D` · `ADR-0004 (Gatilho de reabertura)` | tráfego real de produção (medição de TPS/p99 de commit) — não existe carga real neste ambiente |
| `E12` | Sucessor pós-F3 sob gatilho medido — accruals BND se a spec confirmar cupom/maturidade (ADR-0004 §E.7) | ⏳ gated | `money-ledger-guardian` | `ADR-0004 §E.7` · `ADR-0004 §D` | spec do contrato BND (ABI + comportamento de transferência/cupom) — bloqueio de produto, não resolvido por código |

<details><summary><strong>Detalhamento das etapas</strong> (objetivo · tarefas · gate · dependências)</summary>

##### E1 · Tipo canônico Money + lint anti-float (Fase 0) — ✅ concluída

Estabelecer Money(asset_code, integer_amount, scale) como único tipo monetário atravessando fio/ledger/BFF/UI, com float banido em CI pelos 6 guards `scripts/ci/no-float-*.sh` (Proto/Go/TS/Python/SQL), em escopo **default-deny com allowlist explícita** — ver `contracts/lint/no-float.md` §Escopo.

- Contrato contracts/money/money-type.md ratificado (representação por fronteira, arredondamento ROUND_HALF_EVEN, mapeamento por linguagem)
- Lint contracts/lint/no-float.md + scripts `scripts/ci/no-float-{go,py,sql,data-sql}.sh` + workflow `.github/workflows/no-float.yml` _(4 guards à época de E1; hoje são **6** — `proto` e `ts` entraram nas 28ª/29ª ondas, e `NO_FLOAT_SCRIPTS_EXPECTED := 6` no Makefile é a sentinela que impede um sumir em silêncio)_
- Helpers Go internal/money (Compare/ECPMMinorUnits/IsZero/GreaterThan) com panic em cross-currency

**Subagente:** `money-ledger-guardian` · **Doc:** `TX-2` · `DA-10` · `contracts/money/money-type.md` · `contracts/lint/no-float.md` · **Gate:** money-ledger-guardian — no-float verde nos 6 guards (CI); zero float em campo monetário · **Depende de:** —

##### E2 · Asset Registry plugável — scale autoritativo + seed + CHECKs estruturais — ✅ concluída

Fonte única de scale/metadados por ativo, aceitando novos ativos (incl. AEV/BND) como linhas, sem migração de schema.

- DDL db/asset_registry/migrations/0001_asset_registry_up.sql (assets_enabled_needs_scale_chk, assets_administered_needs_governance_chk)
- Seed BRL/USD/EUR/USDC/USDT/ERC20 (enabled=true) + AEV/BND (enabled=false, scale=NULL, custody_mode=client_supply, price_source=administered)
- K0 (Fase 3) valida as linhas AEV/BND e mantém disabled sob o CHECK estrutural

**Subagente:** `money-ledger-guardian` · **Doc:** `DA-10` · `§2.6` · `ADR-0004 §D` · `ADR-0004 §H (K0)` · `contracts/money/asset-registry.md` · **Gate:** money-ledger-guardian — CHECK impede enabled=true com scale NULL; seed AEV/BND presente e travado · **Depende de:** —

##### E3 · Ledger double-entry Postgres — schema estrutural (accounts/journal_entries/postings) — ✅ concluída

Fonte da verdade contábil única: par de postings idempotente, saldo nunca gravado direto, sum(debit)=sum(credit) por journal_entry+asset_code.

- db/ledger/migrations/0001_ledger_schema_up.sql: enums account_kind/entry_status; constraint trigger DEFERRABLE postings_balance_chk_trg; view account_balances (saldo derivado)
- Particionamento mensal de postings (2026-01..12 + catch-all)
- idempotency_key UNIQUE(tenant_id, key) em journal_entries

**Subagente:** `money-ledger-guardian` · **Doc:** `§2.6` · `ADR-0002 (I1)` · `TX-2` · **Gate:** money-ledger-guardian — trigger de balanço dispara em INSERT/UPDATE/DELETE; nenhuma coluna balance em accounts · **Depende de:** —

##### E4 · Pacote Go internal/ledger — operações de captura, cripto e reconciliação — ✅ concluída

Camada de aplicação sobre o schema: RecordEntry/EnsureAccount/Balance com AssetLoader+cache, operações cripto (deposit/finalize/reversal/payout/FX) e Reconciler contra Iceberg.

- posting.go: RecordEntry idempotente (ErrIdempotentDuplicate), checkBalance pré-DB, Balance via math/big
- asset_registry.go/asset_loader_pg.go: AssetLoader + AssetCache TTL + validação enabled/scale
- crypto.go: RecordDeposit (pending), FinalizeDeposit, RecordReversal (estorno auditável), RecordPayout, RecordFXExchange (DA-10, dois pares isolados por ativo)
- recon.go: Reconciler contra ExpectedValueSource (Iceberg), abre exceção, nunca autocorrige
- 21 testes em ledger_test.go cobrindo idempotência, balanço, ativo desabilitado (AEV/BND), FX, estorno, reconciliação, scale mismatch

**Subagente:** `money-ledger-guardian` · **Doc:** `ADR-0004 §C` · `ADR-0004 §D` · `ADR-0004 §H (K3)` · `TX-2` · `DA-10` · **Gate:** money-ledger-guardian — go test ./internal/ledger/... -race verde (21 testes); ErrAssetDisabled bloqueia AEV/BND · **Depende de:** —

##### E5 · Billing CPM/CPC/CPA/Tenancy — modelo de postings + job batch Iceberg — ✅ concluída

Mapear cada evento faturável (pós-IVT) em par de postings, com plano de contas canônico e idempotency_key por tipo, faturando só a partir do lakehouse.

- BILLING.md: plano de contas (adv/platform:revenue/platform:cash/platform:receivable/rounding), formato de idempotency_key por pricing_model, câmbio explícito §5, estorno §6, reconciliação §7
- data/iceberg/jobs/billing_batch_hourly.py: leitura Iceberg (nunca ClickHouse), decimal.Decimal+ROUND_HALF_EVEN, filtro IVT, atribuição last-click 7d
- Fix 15ª onda: calc_cpm_amount usa floor(imp/1000)*rate (BILLING.md §4.1), elimina fatura de fração sub-mille

**Subagente:** `money-ledger-guardian` · **Doc:** `§4.9` · `CA-7` · `DA-7` · `TX-6` · `db/ledger/BILLING.md` · **Gate:** money-ledger-guardian — make data-billing-test verde (floor semântica); reconciliação exclusiva contra Iceberg · **Depende de:** —

##### E6 · RLS por tenant no ledger + tabela de exceções de reconciliação — ✅ concluída

Isolamento contábil por tenant_id (fail-closed) e superfície auditável de divergências, sem autocorreção silenciosa.

- db/ledger/migrations/0002: ledger.reconciliation_exceptions (UNIQUE tenant+asset+período, GENERATED divergence_minor_units, RLS FORCE)
- db/ledger/migrations/0003: RLS em accounts/journal_entries/postings + view security_invoker
- Fix hardening (4ª onda): policies sem WITH CHECK permitiam INSERT/UPDATE cross-tenant — corrigido; db/ledger/tests/rls_isolation_test.sql prova rejeição (38 PASS, 2 ciclos up/down/up)

**Subagente:** `money-ledger-guardian` · **Doc:** `TX-3` · `DA-11` · `ADR-0004 §D` · **Gate:** money-ledger-guardian + security-reviewer — make db-test-all verde (RLS ledger 38 PASS, WITH CHECK provado) · **Depende de:** —

##### E7 · Gate de CI no-float — piso, não teto (6 guards: Proto/Go/TS/Python/SQL + detecção de furos) — ✅ concluída

Manter o piso de CI honesto: glob que realmente casa arquivos, regex que distingue float monetário de float legítimo de ML/feature.

- 13ª onda: corrigido glob nominal de no-float-py.sh que casava 0 arquivos (falso-verde)
- Detector de pow() em MATERIALIZED/billing no-float-data-sql.sh (retorna Float64 em ClickHouse)
- Escopo restrito documentado: money/ledger/billing/payments/chainconnector + ml/fraud, ml/pacing (não pune float de feature/score)

**Subagente:** `money-ledger-guardian` · **Doc:** `TX-2` · `.github/workflows/no-float.yml` · **Gate:** money-ledger-guardian — CI no-float roda de fato (não apenas imprime 'ok' sem varrer) · **Depende de:** —

##### E8 · Ledger cripto vivo (K3) sob ChainConnector — smoke ponta-a-ponta com stubs — ✅ concluída

Provar os 4 invariantes do trilho cripto fora do hot path: par idempotente, pending→finalidade, reconciliação sem autocorreção, status via BFF sem float.

- deploy/local/smoke-payments.sh exercitando RecordDeposit/FinalizeDeposit/Reconciler/BFF status
- Integração com internal/chainconnector (K0) e services/payments (K4/K5) — cripto 100% fora do hot path
- PASS=20 registrado na 4ª/5ª onda do README

**Subagente:** `money-ledger-guardian` · **Doc:** `ADR-0004 §C` · `ADR-0004 §H (K3, K5)` · `DA-10` · **Gate:** money-ledger-guardian — smoke-payments.sh PASS=20 (a,b,c,d) contra Postgres local · **Depende de:** —

##### E9 · Ativação em produção — migrations reais + reconciliador contra Iceberg real + chaves vivas — ⏳ gated

Ligar o ledger/Asset Registry/billing sob infra real: aplicar as 3 migrations do ledger em produção (não só a 0001 do Docker local), apontar o Reconciler para o Iceberg real e confirmar USDC/scale antes do 1º depósito.

- Aplicar db/asset_registry/migrations/0001, db/ledger/migrations/0001+0002+0003+0004 na ordem do runbook (§2.1/§2.3) — a `0004` (imutabilidade append-only) **mais** o `REVOKE UPDATE, DELETE ON ledger.postings` do runbook §2.3.1, que não vem em nenhuma migration
- Confirmar USDC scale=6 inserido antes do primeiro depósito real
- Rodar make db-test-all + smoke-payments.sh + make verify contra o ambiente de staging/produção antes do cutover
- Configurar NewReconciler com ExpectedValueSource real (Iceberg), substituindo o stub de teste
- Confirmar Fireblocks NÃO ativado (Safe multisig é o default) e TigerBeetle NÃO ativado sem gargalo provado

**Subagente:** `money-ledger-guardian` · **Doc:** `docs/ops/go-live-runbook.md §2.3` · `docs/ops/go-live-runbook.md §6 (checklist money-ledger-guardian)` · `docs/ops/go-live-runbook.md §7` · **Gate:** money-ledger-guardian — checklist do runbook §6 100% marcado antes de liberar tráfego real ao ledger · **Depende de:** E6, E8 · **Bloqueador:** infra real (plataforma EKS/OpenTofu aplicada, chaves OpenBao/KMS vivas, dados reais no Iceberg via data-platform-engineer, aprovação humana de cutover) — não-código, fora do controle deste addon

##### E10 · Sucessor pós-F3 sob gatilho medido — habilitar AEV/BND com scale oficial (ADR-0004 §E.2) — ⏳ gated

Virar enabled=true e definir o scale real de AEV/BND assim que a spec oficial de decimais chegar, sem tocar o schema.

- Receber a spec oficial de decimais do Comitê de Tesouraria (bloqueio de produto, não de código)
- UPDATE asset_registry.assets SET scale=<oficial>, enabled=true — CHECK assets_enabled_needs_scale_chk já impede fazer isso sem scale
- Estender internal/ledger/ledger_test.go com casos AEV/BND habilitados (hoje só cobre o caminho desabilitado)
- Reavaliar precision NUMERIC(p,s) da coluna dedicada ao ativo (contracts/money/money-type.md §5 — hoje só ilustrativo/TBD)

**Subagente:** `money-ledger-guardian` · **Doc:** `ADR-0004 §E.2` · `ADR-0004 §D` · `contracts/money/asset-registry.md §5` · **Gate:** money-ledger-guardian — nenhum posting AEV/BND aceito antes do UPDATE de scale; CHECK estrutural é a barreira · **Depende de:** E2 · **Bloqueador:** spec de produto (scale/decimais oficiais de AEV/BND) — bloqueio que código nenhum resolve, ver §3 q.2 do stack doc

##### E11 · Sucessor pós-F3 sob gatilho medido — TigerBeetle só sob gargalo de escrita provado (ADR-0004 §D) — ⏳ gated

Migrar o ledger de Postgres para TigerBeetle apenas se a latência de escrita do par de postings ultrapassar o orçamento contábil de forma sustentada sob carga real — nunca por aspiração de '1M tps'.

- Instrumentar/medir TPS de postings e p99 de commit do Postgres em produção (pré-requisito de dados reais)
- Anexar o número medido a um ADR sucessor antes de qualquer decisão de migração
- Enquanto não medido: manter Postgres double-entry como única fonte da verdade

**Subagente:** `money-ledger-guardian` · **Doc:** `§2.6` · `§5 (riscos)` · `ADR-0004 §D` · `ADR-0004 (Gatilho de reabertura)` · **Gate:** tech-lead-architect — ADR sucessor só é aberto com número medido de gargalo de escrita anexado · **Depende de:** E9 · **Bloqueador:** tráfego real de produção (medição de TPS/p99 de commit) — não existe carga real neste ambiente

##### E12 · Sucessor pós-F3 sob gatilho medido — accruals BND se a spec confirmar cupom/maturidade (ADR-0004 §E.7) — ⏳ gated

Modelar rendimento/maturidade de BND como pares de postings periódicos (juro provisionado), SE E SOMENTE SE a spec do contrato confirmar a mecânica de 'Bond' — sem UPDATE de saldo, coerente com double-entry.

- Receber a spec/ABI oficial de BND confirmando ou descartando rebasing/cupom/maturidade (bloqueio de produto)
- Se confirmado: desenhar posting periódico de accrual (débito/crédito datado) reusando RecordEntry existente — nenhuma primitiva nova de ledger é necessária
- Se descartado: BND permanece token ERC-20 simples (premissa já adotada) — nenhuma ação de código

**Subagente:** `money-ledger-guardian` · **Doc:** `ADR-0004 §E.7` · `ADR-0004 §D` · **Gate:** money-ledger-guardian — accrual gravado como par de postings explícito, nunca como ajuste de saldo direto · **Depende de:** E10 · **Bloqueador:** spec do contrato BND (ABI + comportamento de transferência/cupom) — bloqueio de produto, não resolvido por código

</details>

**→ Próximo plano deste addon.** Próximo passo imediato (E9): validar o checklist de ativação em produção do go-live-runbook.md §6 assim que a infra real (EKS/OpenTofu, OpenBao/KMS, Iceberg com dados reais) estiver disponível — aplicar as 3 migrations do ledger na ordem documentada, apontar o Reconciler para o Iceberg real, e confirmar USDC/scale antes do 1º depósito; isto não é código novo, é ativação do código já provado. Em paralelo, sob gatilho medido e independente de E9: (E10) habilitar AEV/BND assim que a spec oficial de scale chegar (bloqueio de produto, CHECK já protege); (E11) abrir ADR sucessor para TigerBeetle somente com número de gargalo de escrita medido em produção; (E12) modelar accruals de BND somente se a spec do contrato confirmar cupom/maturidade. Nenhum dos três sucessores é código pendente hoje — são gates de espera por spec ou por medição real, não trabalho de engenharia represado.

---

<a id="310"></a>

### 3.10 Paridade e testes (golden tests + shadow-traffic + dual-run contábil) — guardião de não-divergência Go↔Revive

**Subagente-dono:** `parity-golden-test-guardian`  
**Camada de documentação:** docs/documentacao-tecnica.md §5 (critérios de aceitação CA-1…CA-9) + docs/stack-tecnologico.md §5 (risco nº1 "reescrita Go divergir da semântica legada") + ADR-0002 §D (gate de cutover I4) + ADR-0004 §A (deep não altera faturável)  
**Caminhos:** `tests/parity/ca2_cascade_golden_test.go` · `tests/parity/ca3_creatives_golden_test.go` · `tests/parity/ca4_rules_golden_test.go` · `tests/parity/ca5_capping_golden_test.go` · `tests/parity/ca6_telemetry_golden_test.go` · `tests/parity/ab_parity_test.go` · `tests/parity/golden/ca2_cascade.json` · `tests/parity/golden/ca3_creatives.json` · `tests/parity/golden/ca4_rules.json` · `tests/parity/golden/ca5_capping.json` · `tests/parity/golden/ca6_telemetry.json` · `tests/parity/shadow/shadow_harness.go` · `tests/parity/shadow/shadow_harness_test.go` · `tests/parity/shadow/shadow_wiring_test.go` · `tests/parity/dual_run/dual_run_spec.go` · `tests/parity/dual_run/dual_run_spec_test.go` · `make/parity.mk` · `ml/features/go/parity_contract.go` · `ml/features/testdata/parity_cases.json` · `docs/documentacao-tecnica.md` · `docs/adr/0002-fase-1-sequenciamento-e-layout.md` · `docs/adr/0004-fase-3-sequenciamento-ia-avancada-cripto.md` · `docs/ops/go-live-runbook.md` · `README.md`  
**Incrementos fechados:** `I0` · `I2` · `I3` · `I4 (gate de cutover)` · `J0` · `J3` · `J4` · `K1 (parity de deep-off)` · `7ª onda (swap murmur3, paridade byte-a-byte)` · `8ª onda (contrato de paridade sincronizado)` · `9ª onda (gate canônico + cabeçalho do contrato)` · `15ª onda (fix WAL topic + fix capper uncapped fast-path, golden CA5-005b)` · `16ª onda (golden CA-3 — único faltante da suíte)`

**Estado atual.** A suíte de paridade está CÓDIGO-COMPLETA e VERDE na main: 5 goldens CA-2/CA-3/CA-4/CA-5/CA-6 (CA-3 fechado na 16ª onda, o único que faltava), o gate de re-ranking ML/deep ≡ cascata pura bit-a-bit (ab_parity_test.go + shadow_wiring_test.go, I0→J0→J3→J4→K1), a paridade cross-language Go↔Python de featurização (ml/features, 7ª/8ª/9ª ondas), e os HARNESSES executáveis (não a execução real) de shadow-traffic e dual-run contábil com critério numérico de cutover já declarado em código (CutoverCriteria e BillingTolerancePct). Reverifiquei agora: `make parity-golden-short` roda limpo (3 pacotes ok, `-race`). O que falta é 100% infra/tráfego real (proxy de espelhamento, Revive legado vivo, Iceberg/MySQL reais) — os três alvos `make parity-shadow`/`parity-dual-run`/`parity-cutover-gate` estão declarados como TODO-INFRA no próprio Makefile.

| Etapa | Título | Status | Subagente | Âncoras de doc | Bloqueador |
|---|---|---|---|---|---|
| `E1` | Golden CA-2 — Cascata DA-3 bit-a-bit | ✅ concluída | `parity-golden-test-guardian` | `CA-2` · `DA-3` · `§4.2` | — |
| `E2` | Golden CA-4 — Regras de entrega §4.6 (AND/OR, anti-contradição) | ✅ concluída | `parity-golden-test-guardian` | `CA-4` · `DA-9` · `§4.6` | — |
| `E3` | Golden CA-5 — Frequency capping §4.8 (campaign_total/session/clock, override, fail-safe) | ✅ concluída | `parity-golden-test-guardian` | `CA-5` · `DA-6` · `§4.8` | — |
| `E4` | Golden CA-3 — Criativos §4.3 (imagem/HTML5/third-party/vídeo) | ✅ concluída | `parity-golden-test-guardian` | `CA-3` · `§4.3` | — |
| `E5` | Golden CA-6 — Telemetria §4.7 (dedupe idempotente, at-least-once, blank não-faturável) | ✅ concluída | `parity-golden-test-guardian` | `CA-6` · `DA-7` · `DA-8` · `§4.7` | — |
| `E6` | Gate de paridade do re-ranker ML/deep — cascata pura ≡ fail-open bit-a-bit | ✅ concluída | `parity-golden-test-guardian` | `ADR-0004 §A` · `CA-2` · `DA-3` | — |
| `E7` | Paridade cross-language Go↔Python (featurização anti-skew, hash murmur3 byte-a-byte) | ✅ concluída | `parity-golden-test-guardian` | `ADR-0003 (§A/§B, reuso do internal/ranker)` · `ADR-0004 §A (anti-skew reusa ml/features)` | — |
| `E8` | Harness executável de shadow-traffic (comparador semântico + critério numérico de cutover) | ✅ concluída | `parity-golden-test-guardian` | `§5 (risco nº1, stack)` · `ADR-0002 §D (gate de cutover I4)` | — |
| `E9` | Harness executável de dual-run contábil (BillingRecord/ReconcileHour/DualRunReport) | ✅ concluída | `parity-golden-test-guardian` | `CA-7` · `DA-10` · `§5 (risco nº1 e nº2, stack)` | — |
| `E10` | Matriz de status CA-1…CA-9 (checklist Given/When/Then vivo, gate de fechamento de fase) | ◐ em-andamento | `parity-golden-test-guardian` | `CA-1` · `CA-2` · `CA-3` · `CA-4` · `CA-5` · `CA-6` · `CA-7` · `CA-8` · `CA-9` · `§5` | — |
| `E11` | Ativação real do shadow-traffic (proxy de espelhamento + Revive legado + Go em paralelo) | → próxima | `parity-golden-test-guardian` | `§5 (risco nº1)` · `ADR-0002 §D` | infra real (platform/ aplicado em cloud) + endpoint do Revive legado acessível em paralelo ao motor Go — nada disto existe neste ambiente. |
| `E12` | Ativação real do dual-run contábil (Iceberg vs. MySQL do Revive) | → próxima | `parity-golden-test-guardian` | `CA-7` · `DA-10` · `§5 (risco nº2)` | infra real (Iceberg catalog vivo + acesso ao MySQL do Revive legado) — I3 (billing batch Go) já está pronto, mas não há par legado real para reconciliar. |
| `E13` | Veredito único de cutover Fase 1 (parity-cutover-gate) | ⏳ gated | `parity-golden-test-guardian` | `ADR-0002 §D` · `docs/ops/go-live-runbook.md §6` | depende de E11 e E12 (ambos gated por infra real) terem rodado por janela suficiente. |
| `E14` | Gate de paridade contínuo durante a promoção K8 (deep ranking sob uplift A/B real) | ⏳ gated | `parity-golden-test-guardian` | `ADR-0004 §A` · `ADR-0004 §H (K1, K8)` · `DA-3` | tráfego real pós-cutover da Fase 1/2 (para OPE estatisticamente significativo) + fix de código HOT-1/HOT-3 pelo decision-engine-engineer (pré-condição registrada na 15ª onda, hoje inerte porque os flags estão off). |

<details><summary><strong>Detalhamento das etapas</strong> (objetivo · tarefas · gate · dependências)</summary>

##### E1 · Golden CA-2 — Cascata DA-3 bit-a-bit — ✅ concluída

Travar Override > Contract > Remnant > impressão em branco, incluindo o caso "nenhum elegível → página não quebra", contra a semântica legada do Revive.

- Manter tests/parity/ca2_cascade_golden_test.go + tests/parity/golden/ca2_cascade.json como fonte de verdade versionada.
- Recusar relaxar qualquer caso sem ADR do tech-lead-architect se a semântica mudar de propósito.
- Re-rodar a cada mudança em internal/cascade (decision-engine-engineer) antes de merge.

**Subagente:** `parity-golden-test-guardian` · **Doc:** `CA-2` · `DA-3` · `§4.2` · **Gate:** go test ./tests/parity/... -race verde (parity-golden-test-guardian); zero regressão no motor de decisão a cada onda (12ª/15ª ondas reconfirmaram). · **Depende de:** —

##### E2 · Golden CA-4 — Regras de entrega §4.6 (AND/OR, anti-contradição) — ✅ concluída

Travar dia-da-semana, URL contextual, geo país/cidade, useragent, custom var, lógica AND/OR e o caso anti-contradição (AND mutuamente exclusivo silencia o banner).

- Manter tests/parity/ca4_rules_golden_test.go + golden/ca4_rules.json.
- Garantir cobertura do Rule Set reaplicável em ≥2 banners (último bullet de CA-4).
- Coordenar com decision-engine-engineer (motor) e frontend-bff-engineer (alerta de anti-contradição na UI, que é o lado de produto do mesmo invariante).

**Subagente:** `parity-golden-test-guardian` · **Doc:** `CA-4` · `DA-9` · `§4.6` · **Gate:** go test ./tests/parity/... verde; anti-contradição confirmada como silêncio (banner suprimido), nunca erro 500. · **Depende de:** —

##### E3 · Golden CA-5 — Frequency capping §4.8 (campaign_total/session/clock, override, fail-safe) — ✅ concluída

Travar os três tipos de cap, a sobrescrita banner>campanha, e o fail-safe sem cookie (entrega abortada, nunca estourada).

- Manter tests/parity/ca5_capping_golden_test.go + golden/ca5_capping.json.
- Caso CA5-005b (12ª/15ª onda): fast-path uncapped resolvido ANTES do check de userID, para que cookieless sirva anônimo em vez de sempre BLANK — travado como golden.
- Vigiar contra regressão do TTL do contador (chave pseudônima por-usuário, DA-6/TX-5) toda vez que internal/capping mudar.

**Subagente:** `parity-golden-test-guardian` · **Doc:** `CA-5` · `DA-6` · `§4.8` · **Gate:** go test ./tests/parity/... verde; fail-safe sem cookie prova ABORT (não estouro); known-issue "capper→BLANK" fechado e travado por golden. · **Depende de:** —

##### E4 · Golden CA-3 — Criativos §4.3 (imagem/HTML5/third-party/vídeo) — ✅ concluída

Travar o mapeamento creative_type→campo do Banner, seleção+exposição via cascade.Engine.Decide, e o vínculo server-side do dest_url no token HMAC (a metade importável e real de CA-3; a metade de serving em services/collector é cross-referenciada, não re-testada).

- Manter tests/parity/ca3_creatives_golden_test.go + golden/ca3_creatives.json (16ª onda — único golden que faltava na suíte).
- Manter o gate de cross-referência TestCA3_CollectorServing_CrossRef que nomeia os 11 testes co-localizados do collector (package main, não importável) — um rename lá deve quebrar este CI.
- Vigiar o invariante "no máximo um" campo de payload (não "exatamente um") por causa do switch sem default em setCreative (CA3-007).

**Subagente:** `parity-golden-test-guardian` · **Doc:** `CA-3` · `§4.3` · **Gate:** go test ./tests/parity/... -race verde — 7 casos CA3-001..007 + 2 auxiliares + cross-ref, todos PASS; gofmt/JSON válidos (16ª onda). · **Depende de:** —

##### E5 · Golden CA-6 — Telemetria §4.7 (dedupe idempotente, at-least-once, blank não-faturável) — ✅ concluída

Provar que impressão só conta no pixel (não no request), clique passa por 302 contabilizado, blank é billable=false, e dedupe por event_id é idempotente sob at-least-once.

- Manter tests/parity/ca6_telemetry_golden_test.go + golden/ca6_telemetry.json.
- Coordenar com data-platform-engineer a garantia de que o dedupe por event_id sobrevive ao WAL local + replay pós-crash (bug do 15ª onda — WAL não persistia topic — já corrigido e testado e2e).
- Confirmar UA bruto → classe grossa (TX-5) e referer sanitizado permanecem sem PII.

**Subagente:** `parity-golden-test-guardian` · **Doc:** `CA-6` · `DA-7` · `DA-8` · `§4.7` · **Gate:** go test ./tests/parity/... verde; dedupe idempotente provado por caso golden; coordenação data-platform-engineer para reconciliação contra Iceberg (nunca ClickHouse) fora deste harness unitário. · **Depende de:** —

##### E6 · Gate de paridade do re-ranker ML/deep — cascata pura ≡ fail-open bit-a-bit — ✅ concluída

Provar que control≡cascata pura, treatment nunca muda o tier (só reordena dentro do estrato), fail-open reproduz a cascata pura, e deep default-off não altera faturável (ADR-0004 §A).

- Manter tests/parity/ab_parity_test.go (J4) e tests/parity/shadow/shadow_wiring_test.go (J3) verdes.
- Re-rodar `make ml-deep-test` (K1) a cada onda — invariante DEEP_ENABLED=false até uplift A/B provado (K8). Conferir que os testes de paridade ONNX **rodaram** (não SKIPPED por falta de `onnxscript`); o gate é o exit code, não uma contagem em prosa.
- Vigiar a pré-condição HOT-1/HOT-3 (15ª onda): RankResult por-request deve fluir de Decide() antes de qualquer ativação real de RANKER_ENABLED/AB_ENABLED/SHADOW_ENABLED sob tráfego concorrente — hoje inerte porque os flags estão off.

**Subagente:** `parity-golden-test-guardian` · **Doc:** `ADR-0004 §A` · `CA-2` · `DA-3` · **Gate:** go test -race ./... (ranker+parity+shadow) verde; make ml-deep-test verde; DA-3 confinado (re-rank só dentro do estrato); zero mudança de tier por re-ranking. · **Depende de:** —

##### E7 · Paridade cross-language Go↔Python (featurização anti-skew, hash murmur3 byte-a-byte) — ✅ concluída

Provar que a featurização usada pelo re-ranker é idêntica entre o hot path Go e o treino Python (anti-skew), incluindo a identidade byte-a-byte do hash após o swap spaolacci→twmb/murmur3.

- Manter ml/features/go/parity_contract.go sincronizado com ml/features/python/test_parity_cases.py e ml/features/spec/feature_spec.yaml (nenhuma referência órfã à lib morta).
- Re-verificar os 11 pares de fixture (geo_country_hash/geo_city_hash/device_class_hash) a cada mudança em internal/ranker/featurize.go.
- Bloquear qualquer mudança de seed/assinatura de hash sem revisão adversarial explícita (já exigida nas ondas 7/8/9).

**Subagente:** `parity-golden-test-guardian` · **Doc:** `ADR-0003 (§A/§B, reuso do internal/ranker)` · `ADR-0004 §A (anti-skew reusa ml/features)` · **Gate:** TestParityFromFixtures 5/5 (Go) + test_parity_cases.py (Python) verdes; zero drift de spec vs. lib real; make parity-golden-short intacto. · **Depende de:** —

##### E8 · Harness executável de shadow-traffic (comparador semântico + critério numérico de cutover) — ✅ concluída

Ter, ANTES de qualquer infra viva, o tipo de entrada/saída, o comparador semântico (CompareDecisions) e o CutoverGate com critério numérico declarado — para que a execução real (E11) seja só "plugar dados", não "inventar critério sob pressão de cutover".

- Manter tests/parity/shadow/shadow_harness.go: DecisionInput/DecisionOutput, DivergenceCollector, CutoverCriteria.
- Não alterar CutoverCriteria (MaxDecisionDivergenceRate=0.001, MaxBillingDivergencePct=0.005, MinShadowHours=48, MinShadowDecisions=100_000) sem ADR do tech-lead-architect.
- Garantir que CutoverGate.Evaluate() nunca aprova silenciosamente (Collector nil ou dados insuficientes → Approved=false com razão explícita).

**Subagente:** `parity-golden-test-guardian` · **Doc:** `§5 (risco nº1, stack)` · `ADR-0002 §D (gate de cutover I4)` · **Gate:** go test ./tests/parity/shadow/... verde; CutoverGate testado com Collector nil/insuficiente → sempre reprovado; critério numérico documentado no próprio arquivo. · **Depende de:** —

##### E9 · Harness executável de dual-run contábil (BillingRecord/ReconcileHour/DualRunReport) — ✅ concluída

Ter a lógica de reconciliação de faturamento Go vs. Revive pronta e testada, com tolerância declarada e a proibição de comparar contra o streaming.

- Manter tests/parity/dual_run/dual_run_spec.go: BillingAmount (string minor-units, nunca float64), ReconcileHour, BuildReport.
- Preservar o invariante "nunca comparar contra ClickHouse para faturamento — só Iceberg" (comentário INVARIANTE no topo do arquivo).
- Não alterar BillingTolerancePct=0.005 sem ADR do tech-lead-architect.

**Subagente:** `parity-golden-test-guardian` · **Doc:** `CA-7` · `DA-10` · `§5 (risco nº1 e nº2, stack)` · **Gate:** go test ./tests/parity/dual_run/... verde; ErrAssetMismatch cobre comparação cross-currency; toda asserção monetária em string/int64, zero float. · **Depende de:** —

##### E10 · Matriz de status CA-1…CA-9 (checklist Given/When/Then vivo, gate de fechamento de fase) — ◐ em-andamento

Manter um retrato honesto de quais CA estão code-complete+golden-verde vs. quais dependem de prova operacional real (upload de criativo, MaxMind auto-update, Mailer/SMTP, isolamento RLS com Postgres real), consolidando os gates dos outros donos (CA-1 frontend-bff-engineer+security-reviewer, CA-7 money-ledger-guardian, CA-8 privacy-compliance-auditor, CA-9 platform-infra-engineer) num único veredito para o tech-lead-architect.

- Cross-referenciar o checklist Given/When/Then de docs/documentacao-tecnica.md §5 (CA-1…CA-9) contra as evidências reais já produzidas: db/config/tests + db/ledger/tests + db/vector/tests (RLS, CA-1/CA-7/CA-8), tests/parity/* (CA-2..CA-6), docs/ops/go-live-runbook.md §6 (checklist dos 4 gates).
- Registrar explicitamente que os `- [ ]` do §5 do documento técnico permanecem sem marcação formal apesar da evidência de código verde — achado de higiene documental, não de código (candidato a fechamento pelo tech-lead-architect, não uma reabertura de escopo).
- Nunca declarar um CA "verde" sem a evidência executável correspondente citada (teste real rodado, não afirmação).

**Subagente:** `parity-golden-test-guardian` · **Doc:** `CA-1` · `CA-2` · `CA-3` · `CA-4` · `CA-5` · `CA-6` · `CA-7` · `CA-8` · `CA-9` · `§5` · **Gate:** tech-lead-architect adjudica a matriz consolidada antes de qualquer fechamento formal de fase; nenhum CA marcado verde sem teste/evidência citável. · **Depende de:** —

##### E11 · Ativação real do shadow-traffic (proxy de espelhamento + Revive legado + Go em paralelo) — → próxima

Preencher os itens TODO-INFRA de shadow_harness.go: proxy HTTP que espelha requests de produção, chamada real ao Revive legado, chamada real ao motor Go, exportação de métricas — e rodar o CutoverGate com dados reais.

- Coordenar com platform-infra-engineer a aplicação de platform/ em cloud e a disponibilidade do Revive legado em paralelo.
- Rodar make parity-shadow com REVIVE_ENDPOINT e GO_DECISION_URL reais, alimentando o DivergenceCollector.
- Avaliar CutoverGate.Evaluate() só após MinShadowHours=48h e MinShadowDecisions=100.000 atingidos; nunca aprovar com amostragem insuficiente.

**Subagente:** `parity-golden-test-guardian` · **Doc:** `§5 (risco nº1)` · `ADR-0002 §D` · **Gate:** parity-golden-test-guardian avalia CutoverGate com os 4 critérios simultâneos (MaxDecisionDivergenceRate ≤0.1%, amostra e janela mínimas); reprovação bloqueia cutover, nunca silêncio. · **Depende de:** E8 · **Bloqueador:** infra real (platform/ aplicado em cloud) + endpoint do Revive legado acessível em paralelo ao motor Go — nada disto existe neste ambiente.

##### E12 · Ativação real do dual-run contábil (Iceberg vs. MySQL do Revive) — → próxima

Preencher os itens TODO-INFRA de dual_run_spec.go: leitura real do Iceberg e do MySQL do Revive, execução paralela dos dois billing batches, exportação para Grafana/PagerDuty.

- Coordenar com data-platform-engineer o acesso ao Iceberg catalog real (ICEBERG_CATALOG_URI) e ao MySQL do Revive (REVIVE_MYSQL_DSN).
- Rodar make parity-dual-run por hour_bucket e alimentar BuildReport(); nunca comparar contra ClickHouse.
- Coordenar com money-ledger-guardian toda comparação (NUMERIC/string minor-units, nunca float) — o dono da correção monetária é ele; o guardião de paridade prova que não há divergência acima da tolerância.

**Subagente:** `parity-golden-test-guardian` · **Doc:** `CA-7` · `DA-10` · `§5 (risco nº2)` · **Gate:** DualRunReport.CanCutover=true (HoursBlocking=0) sobre janela real; qualquer BlockingReasons não-vazio bloqueia o cutover, sem exceção. · **Depende de:** E9 · **Bloqueador:** infra real (Iceberg catalog vivo + acesso ao MySQL do Revive legado) — I3 (billing batch Go) já está pronto, mas não há par legado real para reconciliar.

##### E13 · Veredito único de cutover Fase 1 (parity-cutover-gate) — ⏳ gated

Consolidar CutoverGate.Evaluate() (decisão) + DualRunReport.CanCutover (faturamento) num veredito único, reportado ao tech-lead-architect, antes do go-live formal.

- Rodar make parity-cutover-gate sobre os relatórios JSON produzidos por E11+E12.
- Aplicar a regra invólavel: nunca aprovar cutover com divergência acima da tolerância declarada em qualquer um dos dois critérios.
- Registrar o veredito (aprovado/reprovado + razões) no runbook de go-live (docs/ops/go-live-runbook.md §6, checklist parity-golden-test-guardian).

**Subagente:** `parity-golden-test-guardian` · **Doc:** `ADR-0002 §D` · `docs/ops/go-live-runbook.md §6` · **Gate:** tech-lead-architect aprova o go-live só com os dois relatórios (shadow + dual-run) verdes simultaneamente; parity-golden-test-guardian é o dono do veredito técnico. · **Depende de:** E11, E12 · **Bloqueador:** depende de E11 e E12 (ambos gated por infra real) terem rodado por janela suficiente.

##### E14 · Gate de paridade contínuo durante a promoção K8 (deep ranking sob uplift A/B real) — ⏳ gated

Sucessor pós-Fase-3: sob tráfego real e infra da Fase 2 já cutada, provar continuamente que o deep ranker em shadow/A-B permanece ≡ cascata pura bit-a-bit, e que a promoção de model_version só ocorre com uplift provado (ml/ope IPS/SNIPS/DR).

- Verificar, a cada ciclo de OPE real, que ml_fail_open/exploration_policy/propensity continuam corretos sob carga real (não só nos golden estáticos).
- Vetar a ativação real de RANKER_ENABLED/AB_ENABLED/SHADOW_ENABLED até que decision-engine-engineer feche a pré-condição HOT-1/HOT-3 (RankResult por-request fluindo de Decide(), não do campo last compartilhado) — hoje inerte, mas passa a enviesar o OPE assim que os flags ligarem sob concorrência real.
- Recusar promoção de model_version (ml/registry/promote_model.py) sem prova de uplift A/B + kill-switch testado sob tráfego real.

**Subagente:** `parity-golden-test-guardian` · **Doc:** `ADR-0004 §A` · `ADR-0004 §H (K1, K8)` · `DA-3` · **Gate:** parity-golden-test-guardian bloqueia promoção sem uplift A/B + kill-switch; nenhuma exceção de budget/autoridade para o deep (regra de ouro). · **Depende de:** E13, E6 · **Bloqueador:** tráfego real pós-cutover da Fase 1/2 (para OPE estatisticamente significativo) + fix de código HOT-1/HOT-3 pelo decision-engine-engineer (pré-condição registrada na 15ª onda, hoje inerte porque os flags estão off).

</details>

**→ Próximo plano deste addon.** Ativação em produção (E11→E12→E13): plugar os harnesses já código-completos e testados (E8/E9) em infra real assim que platform-infra-engineer aplicar platform/ em cloud e o Revive legado estiver acessível em paralelo — rodar make parity-shadow/parity-dual-run/parity-cutover-gate com os critérios numéricos JÁ declarados (nunca inventados sob pressão): MaxDecisionDivergenceRate≤0.1%, MaxBillingDivergencePct≤0.5%, MinShadowHours=48, MinShadowDecisions=100.000. Sucessor pós-F3 sob gatilho medido (E14): gate de paridade contínuo na promoção K8 do deep ranking, condicionado a tráfego real suficiente para OPE E ao fix de código HOT-1/HOT-3 (RankResult por-request) pelo decision-engine-engineer — sem isso, o guardião de paridade recusa avaliar a promoção. Em paralelo, fechar E10 (formalizar a matriz CA-1…CA-9 com o tech-lead-architect) como higiene documental, não como reabertura de escopo.

**Riscos:**
- Silêncio sobre divergência é o pior resultado possível — qualquer execução real de E11/E12 que rode sem CutoverGate.Evaluate()/BuildReport() explícitos (ex.: só 'parece bater') deve ser tratada como reprovação, não como aprovação por omissão.
- Ativar RANKER_ENABLED/AB_ENABLED/SHADOW_ENABLED sob tráfego real ANTES do fix HOT-1/HOT-3 enviesaria o OPE que decide a promoção K8 — bloqueador de código explícito, não apenas de infra.
- docs/documentacao-tecnica.md §5 mantém todos os `- [ ]` sem marcação apesar dos goldens verdes — risco de a matriz formal ficar permanentemente desatualizada em relação ao README (fonte de verdade de fato); requer decisão do tech-lead-architect sobre qual documento é canônico.
- make/parity.mk comenta 'parity-golden (CA-2, CA-4, CA-5, CA-6...)' sem citar CA-3 no texto do alvo, apesar do golden CA-3 já existir e rodar dentro do mesmo `go test ./tests/parity/...` — drift cosmético de comentário, não de execução (o alvo roda os 5 goldens de fato).
- Tolerâncias (0.1% decisão, 0.5% faturamento) são as únicas legitimadas por ADR/comentário no código — qualquer pressão de negócio para afrouxá-las sem ADR do tech-lead-architect deve ser recusada.

---

## 4. Malha de gates transversais

Guardiões que não são donos de addon: produzem a treliça de gates que **atravessa** todos os componentes. São **condição de merge/cutover**, não pendências de código.

### 4.1 Segurança — `security-reviewer`

Segurança — malha transversal de gates que atravessa TODOS os addons do AdServer: isolamento multi-tenant fim-a-fim (borda→BFF→RLS Postgres/pgvector→row-policy ClickHouse), copiloto/LLM (sem credencial, autz server-side, prompt-injection, RAG isolado, HITL), endpoints de delivery (ad tag/lg/ck/ct: open-redirect/SSRF/cache-poisoning), segredos (OpenBao/KMS), web/ORM (SQLi/XSS/CSRF/escalada), supply chain (cosign/SBOM/Trivy/Kyverno) e segregação em células PCI + AML/KYC (ADR-0004 §F). O código-alvo está código-completo na main e verde; estes gates são a condição de merge/cutover, não pendências de código.

**Aplica-se a:** motor-de-decisao (services/decision + internal/cascade|rules|capping|snapshot), delivery/ad-tag (services/collector + internal/clicktoken), bff (bff/src — fronteira de ACL server-side, CA-1), copiloto-llm (services/copilot + bff/src/routers/copilot.ts), rag-vector (db/vector + copilot RAG), ml-ranker (internal/ranker + services/ranker-sidecar), pagamentos-cripto (services/payments + internal/chainconnector + bff/src/routers/payments.ts), ledger (db/ledger), plataforma/celulas (platform/cells/pci + platform/cells/aml-kyc + platform/k8s)

##### S1 · Isolamento multi-tenant fim-a-fim: tenant_id imposto server-side da borda ao banco (borda→BFF→RLS)

- BFF é a fronteira de ACL: ensureTenant retorna 403 sem tenantId (bff/src/lib/trpc.ts:18-27) e TODA rota de dados usa tenantProcedure (bff/src/routers/stats.ts:30-41, config.ts, payments.ts). Achado = qualquer procedure de dados fora de tenantProcedure ou ACL avaliada no cliente.
- tenant_id SEMPRE de ctx.tenantId (sessão autenticada), NUNCA do body/input do cliente (bff/src/routers/copilot.ts:28,153,211-212). Achado = qualquer leitura de tenant_id vinda do payload.
- RLS por tenant com USING E WITH CHECK em toda tabela: config (db/config/migrations/0002_config_rls_up.sql:48-66), campaign_zones (0003), ledger accounts/journal_entries/postings (db/ledger/migrations/0003_ledger_rls_up.sql:55-91), pgvector (db/vector/migrations/0002_vector_rls_up.sql), compliance (db/compliance/migrations/0001). Achado = tabela com tenant_id sem ENABLE ROW LEVEL SECURITY ou policy só com USING.
- Fail-closed: config.current_tenant_id() retorna NULL sem SET LOCAL (db/config/migrations/0002_config_rls_up.sql:32-38) → sessão sem tenant vê 0 linhas, nunca todas. FORCE ROW LEVEL SECURITY onde o owner insere dados de tenant.
- ClickHouse: row-policies + quotas por tenant_id nas tabelas de StatsHourly/ao-vivo (stack §2.2). Achado = query analítica servida ao painel sem row-policy de tenant.
- Testes de isolamento executam e passam: db/config/tests/rls_isolation_test.sql e db/ledger/tests/rls_isolation_test.sql (tenant A não lê B; sem tenant setado = 0 linhas) via make db-test-all (runbook §5 Passo 1).
- REGRA INVIOLÁVEL: nunca aprovar ACL avaliada no cliente; a fronteira é o BFF server-side (CA-1). BYPASSRLS restrito ao role adserver_loader (runbook §3.4), nunca à app.

**Doc:** `TX-3` · `CA-1` · `stack §2.6` · `ADR-0002 §D (I4)` · `ADR-0003 §G (J0)` · `ADR-0004 §H (K3/K7)` · `runbook §2.2/§2.3/§2.4/§5` · `DA-11` · **Trava:** bff, motor-de-decisao, ledger, rag-vector, copiloto-llm, pagamentos-cripto, plataforma/celulas, etapa I4, etapa J0, etapa K3/K7

##### S2 · Copiloto: o LLM nunca recebe credencial; autorização server-side ignora instruções do payload (defesa prompt-injection)

- Gateway de ferramentas injeta tenant_id server-side e IGNORA instruções do payload do LLM (services/copilot/tools/gateway.py:5-11,76-77). O tenant_id vem do estado do thread, não da tool call.
- tenant_id lido de state["tenant_id"] em TODOS os nós, nunca das mensagens do modelo (services/copilot/graph/nodes.py:102,104,203,205). Achado = tenant_id/credencial extraídos de mensagem ou tool_input.
- Chave ANTHROPIC_API_KEY nunca chega ao front nem ao LLM: fica no copiloto Python atrás do BFF; BFF injeta tenant via HMAC X-Tenant-ID (bff/src/routers/copilot.ts:10,63-82). SSE sempre através do BFF, nunca direto à API Anthropic.
- Autenticação HMAC interna BFF→copilot com rejeição de replay, sentinela de skip e segredo errado (services/copilot/tests/test_security.py:126-213); em produção, skip-auth bloqueado no startup.
- Guardrail Haiku-as-judge para brand-safety/claims/prompt-injection presente como 2ª camada, além da validação estrutural determinística (stack §2.4).
- REGRA INVIOLÁVEL: nunca aprovar o LLM com credencial nem autorização derivada do payload — a autoridade é o gateway server-side.

**Doc:** `TX-3` · `stack §2.4` · `stack §5 (prompt injection/vazamento entre tenants)` · `ADR-0003 §C (J5)` · `ADR-0004 §H (gates de merge)` · **Trava:** copiloto-llm, bff, etapa J5

##### S3 · RAG do copiloto: pgvector sempre filtrado por tenant (RLS) + teste de isolamento entre tenants; sem SQLi

- set_config('adserver.tenant_id', $1, true) PARAMETRIZADO antes de toda query vetorial (services/copilot/tools/gateway.py:416,473) — proibida interpolação f-string de tenant_id; teste trava a regressão (services/copilot/tests/test_security.py:260-274).
- RLS por tenant na tabela pgvector (db/vector/migrations/0002_vector_rls_up.sql) com USING+WITH CHECK; a query roda dentro de transação por request para não vazar tenant em transaction-pooling/PgBouncer (gateway.py:408).
- Teste de isolamento entre tenants OBRIGATÓRIO: nenhum tenant lê embeddings/criativos de outro (stack §2.4 / ADR-0003 §C). Achado = ausência de teste de isolamento no RAG.
- search_similar_creatives e search_help_docs usam o schema vector correto e não montam SQL por concatenação (test_security.py:227-274).
- REGRA INVIOLÁVEL: nunca aprovar RAG sem RLS + teste de isolamento; nunca enfraquecer o filtro de tenant 'para um teste passar'.

**Doc:** `TX-3` · `stack §2.4` · `stack §5` · `ADR-0003 §C/§D/§F (J5)` · **Trava:** copiloto-llm, rag-vector, etapa J5

##### S4 · HITL obrigatório em TODA escrita do copiloto — nada publicado autonomamente

- Ferramentas de escrita retornam WriteDiff e NÃO persistem (drafts): create/update campaign/banner/rule/cap (services/copilot/tools/gateway.py:481-571). Persistência só em apply após aprovação humana.
- write_draft_node produz diff para HITL, nunca grava (services/copilot/graph/nodes.py:331-366); validação §4.6/CA-4 anti-contradição roda ANTES do HITL (gateway.py:563-571).
- Apply 1-clique = PATCH validado por Zod + preview de diff + hitlApprove no BFF encaminhando ctx.tenantId (bff/src/routers/copilot.ts:207-215) — human-in-the-loop.
- REGRA INVIOLÁVEL: nunca aprovar escrita autônoma; qualquer caminho de mutação de campanha/banner/regra sem passo HITL é achado CRITICAL.

**Doc:** `stack §2.4` · `stack §2.5` · `ADR-0003 §C (J5)` · `ADR-0003 §G (gate J5: HITL em toda escrita)` · `CA-4` · **Trava:** copiloto-llm, bff, etapa J5

##### S5 · Endpoints de delivery (asyncjs/lg/ck/ct): open-redirect, SSRF, injeção via custom vars e cache-poisoning

- Clique = 302 server-side sem dest_url em query: /ck aceita só um token HMAC-SHA256 assinado (decision_id:banner_id:expiry:dest_url) mintado do config server-side (internal/clicktoken/clicktoken.go:8-16,83-108). Achado = qualquer dest_url lido de parâmetro em claro.
- Fail-closed: sem CK_HMAC_SECRET o Signer recusa iniciar (clicktoken.go:58) e /ck retorna 503 (services/collector/cmd/collector/main.go, cabeçalho de segurança #1).
- validateDestURL (defesa em profundidade) exige http/https, bloqueia userinfo@host (evil@127.0.0.1), IP privado/loopback/link-local/ULA/unspecified e formas hex/octal/decimal (services/collector/cmd/collector/main.go:546-664); aplicado no /ck (main.go:523) e nas URLs de vídeo/clique do VAST (main.go:730-741). Guarda contra SSRF de fetch de criativo/third-party tag.
- Ad tag construída via DOM API (a.href, sem innerHTML) e iframe sandbox com allow-top-navigation negado (main.go:343,357-361) — custom vars first-party não injetam markup nem hijack de página do publisher (DA-5).
- Cachebuster cb: confirmar que a resposta de decisão/pixel não é cacheável de forma envenenável (Cache-Control/Vary) — CA-6, sem cache-poisoning por cb controlado pelo cliente.
- Impressão só conta na carga do pixel lg 1x1, clique passa pelo servidor e então 302 (DA-8/CA-6).

**Doc:** `DA-5` · `DA-8` · `stack §2.1` · `stack §4.7` · `CA-6` · `ADR-0002 §D (I2)` · **Trava:** delivery/ad-tag, motor-de-decisao, etapa I2

##### S6 · Segredos: OpenBao/Vault dynamic secrets + Pod Identity + KMS/HSM — nada estático em imagem/git

- Nenhum segredo em código de aplicação: varredura de sk_live_/AKIA/PEM/api_key/password em services|internal|bff|platform|db|ml só retorna node_modules/.venv/docs — zero em app code. Achado = qualquer chave/token/connection-string hardcoded no caminho de build.
- Todos os segredos vêm de OpenBao com Pod Identity (IRSA/OIDC), TTL curto, DSNs dinâmicos por célula (runbook §3.1-§3.4; platform/secrets/openbao/policy-*.hcl; platform/cells/*/secrets/openbao-auth.yaml).
- PII/KYC cifrada por envelope KMS versionado v1$ (services/payments/internal/kmsenvelope/kmsenvelope.go); chave real em KMS/HSM, nunca stub em produção (runbook §3.1 / §6 privacy).
- KMS/HSM para chaves de pagamento (Stripe/Asaas/Fireblocks/Sumsub/Chainalysis) confinadas na célula respectiva (runbook §3.2/§3.3).
- REGRA: coordenar rotação/paths de segredo e roles Postgres com platform-infra-engineer; qualquer segredo em ConfigMap/env estático/imagem é achado.

**Doc:** `TX-3` · `stack §2.7` · `ADR-0004 §F` · `runbook §3` · `runbook §6 (security-reviewer)` · **Trava:** pagamentos-cripto, copiloto-llm, delivery/ad-tag, plataforma/celulas, ledger

##### S7 · Segregação em células: PCI de escopo mínimo não escapa da célula; AML/KYC isolada; Cilium deny-all (ADR-0004 §F)

- Cilium default-deny em cada célula: platform/cells/pci/netpol/default-deny.yaml, platform/cells/aml-kyc/netpol/default-deny.yaml, e delivery em platform/k8s/netpol/cilium-default-deny.yaml. Nenhuma porta extra sem allow explícito (runbook §6).
- Egress só para APIs necessárias via allowlist FQDN: pci (allow-egress-stripe, allow-egress-openbao), aml-kyc (allow-egress-chainalysis/sumsub/travel-rule/postgres-compliance/openbao) — controla SSRF de saída dos conectores.
- PCI SAQ-A: cartão tokenizado client-side (Elements/Checkout), NUNCA transita pelo backend; célula em conta cloud separada. Achado = qualquer PAN/cartão no nosso backend.
- Kyverno PCI/AML proíbe acesso a secret de outra célula e imagens sem digest (platform/cells/pci/policy/kyverno-pci.yaml, aml-kyc/kyverno-aml-kyc.yaml); baseline proíbe container privilegiado/hostPath/root (platform/k8s/policy/kyverno-baseline.yaml).
- Hot path intocado: motor de decisão na borda não ganha dependência de PCI/AML (ADR-0004 §F).
- L-1 (runbook §9): deny-all do Cilium NÃO é asserido por kyverno test — exigir smoke comportamental de netpol (curl entre pods de namespaces distintos / netpol-tester) pós-deploy antes de tráfego real. Bloqueio de infra, não de código.

**Doc:** `stack §2.7` · `ADR-0004 §F` · `ADR-0004 §H (K4/K5/K6)` · `runbook §6` · `runbook §9 (L-1)` · `DA-11` · **Trava:** plataforma/celulas, pagamentos-cripto, etapa K4/K5/K6

##### S8 · Web/ORM: SQL raw/concatenado, XSS no markup do banner, CSRF em mutações do BFF, escalada de privilégio

- SQL sempre parametrizado ($1); f-string/concatenação com tenant_id proibida e travada por teste (services/copilot/tests/test_security.py:269-274). Achado = qualquer fmt.Sprintf/f-string montando SELECT/INSERT/UPDATE/DELETE.
- XSS: markup do banner servido via DOM API, sem dangerouslySetInnerHTML/innerHTML no app code (services/collector/cmd/collector/main.go:357-361); varredura de dangerouslySetInnerHTML/eval/os.system só retorna node_modules.
- CSRF: mutações do BFF (tRPC) protegidas por cookie de sessão SameSite + verificação de origem — validar que hitlApprove/apply e writes de config não são acionáveis cross-site.
- Escalada de privilégio: nenhuma chamada ORM/raw com BYPASSRLS na app; adserver_loader (BYPASSRLS) restrito a loader, roles por célula com privilégio mínimo (runbook §3.4).
- REGRA: coordenar PII/privacidade com privacy-compliance-auditor; nenhum enfraquecimento de isolamento para passar teste.

**Doc:** `TX-3` · `CA-1` · `stack §2.5` · `runbook §3.4` · `CA-4 (anti-contradição na UI)` · **Trava:** bff, copiloto-llm, delivery/ad-tag, etapa I4/J5

##### S9 · Supply chain: cosign (imagens assinadas) + SBOM + Trivy + Kyverno + Falco no caminho de build

- Kyverno verifyImages exige imagens assinadas via cosign, sem tag :latest e com resource limits (platform/k8s/policy/kyverno-baseline.yaml:23-29); chave pública cosign do registry substituída no apply.
- cosign + Trivy + Falco ativos no cluster antes de redirecionar tráfego; SHA256 dos binários de CI (kubeconform/kyverno/tofu) verificados (runbook §6 security-reviewer).
- Células PCI/AML exigem imagens com digest (não tag mutável) — Kyverno PCI/AML (platform/cells/*/policy).
- Achado = qualquer imagem sem assinatura/digest no path de deploy, ou dependência não-verificada no build.

**Doc:** `stack §2.7` · `runbook §6 (security-reviewer)` · `runbook §7` · **Trava:** plataforma/celulas, motor-de-decisao, copiloto-llm, pagamentos-cripto, ml-ranker

##### S10 · Pagamentos/cripto: SSRF em conectores/webhooks, validação de assinatura de webhook, IDOR de status, screening (ADR-0004 §C/§F)

- Conectores e webhooks (internal/chainconnector/evm_rpc.go, services/payments/internal/crypto/safe_webhook.go, sumsub/sumsub.go, asaas/asaas.go) só alcançam FQDNs da allowlist da célula (S7); RPC/URL de custódia vem de OpenBao (SAFE_RPC_URL, runbook §3.3), nunca de input do cliente — guarda contra SSRF.
- Webhooks validam assinatura antes de agir: HMAC Sumsub (SUMSUB_SECRET_KEY), segredo de webhook Stripe, Safe webhook — depósito fica pending até finalidade (N confirmações) e reorg vira estorno auditável (ADR-0004 §C/E.8).
- IDOR: status de pagamento via BFF sempre sob RLS + tenant de sessão; smoke-payments trilho (d) verifica string DECIMAL sem float (TX-2) e isolamento RLS (runbook §5 Passo 2). Cripto NUNCA no cliente (só status via BFF; wagmi só sob spec E.9).
- Screening/Travel Rule (Chainalysis + Sumsub) e PII/KYC confinados na célula AML/KYC, pseudônimo por tenant_id; ledger e telemetria sem PII (ADR-0004 §H K6 / DA-11).
- Fireblocks desativado até AUM justificar; Safe multisig é default (ADR-0004 §C) — segredos Fireblocks só provisionados sob gatilho. Coordenar com money-ledger-guardian (nenhuma captura grava saldo direto) e platform-infra-engineer (células).

**Doc:** `stack §2.6` · `ADR-0004 §C` · `ADR-0004 §F` · `ADR-0004 §H (K5/K6/K7)` · `runbook §3.3/§5 Passo 2` · `TX-2` · `DA-11` · **Trava:** pagamentos-cripto, bff, ledger, plataforma/celulas, etapa K5/K6/K7

---

### 4.2 Privacidade & Conformidade — `privacy-compliance-auditor`

Privacidade & Conformidade — Privacy by Design como GATE DE ACEITAÇÃO (TX-5/DA-11/CA-8), não recomendação. Malha transversal read-only que atravessa todos os addons: sem PII/IP bruto nos eventos, redação no OTel, capping efêmero, isolamento entre tenants (RLS+teste), cofre de compliance PII/KYC, proveniência C2PA (EU AI Act Art. 50) e sem transmissão inter-regional opaca.

**Aplica-se a:** dados-telemetria (collector, proto de eventos, Redpanda->ClickHouse->Iceberg), motor-de-decisao (hot path, capping Redis), copiloto (LangGraph, RAG pgvector, validate_creative, geracao de criativo IA), pagamentos-compliance (ledger cripto, cofre AML/KYC, Sumsub/Chainalysis/Travel Rule, cifra KMS-envelope), plataforma-observabilidade (OTel Collector, celulas PCI/AML-KYC, OpenBao/KMS), BFF-console (ACL server-side, RLS por request, dashboards <=1h)

##### P1 · Ausencia de PII / IP bruto nos eventos e no fio (ciclo de vida do IP)

- Rastrear o IP: entra SO no collector e e descartado apos derivar geo - resolveAndDiscardIP (services/collector/cmd/collector/main.go:951-962) e MaxMindResolver.Resolve consome-e-nao-propaga (internal/geo/maxmind.go:79-103). BLOQUEAR qualquer caminho onde o IP seja logado/exportado/persistido (CRITICAL se houver 1 caminho).
- Proto de telemetria sem IP: Envelope.Geo carrega so country+city, sem coordenadas/CEP (proto/adserver/common/v1/envelope.proto:69-81); AdRequest nao possui campo de IP (proto/adserver/telemetry/v1/events.proto:31-68).
- User-Agent bruto reduzido a classe coarse no UNICO ponto permitido (internal/useragent/classify.go:33) antes de emitir (main.go:184); UA cru e vetor de fingerprinting - confirmar que nunca e persistido/encaminhado.
- Referer sanitizado: querystring e fragment removidos para nao vazar token de sessao/identificador embutido em URL (main.go:186-188).
- Decision nao carrega PII nem IP; contexto de features nao vai cru (so feature_vector_ref/hash) - contrato de propensao (contracts/telemetry/propensity-logging.md secao 6).

**Doc:** `DA-11` · `DA-9` · `CA-8` · `TX-5` · `documentacao-tecnica.md 4.5` · `documentacao-tecnica.md 4.7` · **Trava:** I0 (proto/envelope), I3 (collector/telemetria), J1 (featurizacao PII-free), dados-telemetria, motor-de-decisao

##### P2 · Redacao de PII no OTel Collector ANTES de qualquer export (todos os exporters)

> Sem numeros de linha de proposito: as citacoes `otel-collector.yaml:31-62` que este bloco
> carregava apodreceram no primeiro commit que mexeu no arquivo (a 30a onda o alterou em ~80
> linhas). Cite o ARTEFATO e o NOME do processador/pipeline — ambos sao greppaveis e nao
> envelhecem: `platform/observability/otel-collector.yaml`.

- transform/redact-pii DELETA client.address/http.client_ip/net.sock.peer.addr/net.peer.ip/url.full/enduser.id/user.id/user.email.
- Allowlist fail-closed (`allow_all_keys: false`) em `redaction/allowlist-{traces,logs,metrics}`: qualquer chave nova/inesperada e bloqueada por padrao.
- blocked_values mascaram IPv4 e e-mail como defesa em profundidade; capping.key hasheada com SHA256 se aparecer.
- COBERTURA DE TODOS OS PIPELINES (30a onda): traces, logs **E metrics** passam por `transform/redact-pii` + `redaction/allowlist-<tipo>` antes de batch/export. A ressalva anterior deste bullet ("o pipeline de METRICS NAO passa por redacao - exigir prova de que labels de metrica jamais carregam PII") esta **RESOLVIDA**: `metrics` recebeu `redaction/allowlist-metrics` com `allow_all_keys: false`, entao a garantia nao depende mais de prova por inspecao.
- CI impoe a redacao: `platform-otel-validate` roda `otelcol validate` + a checagem estrutural `platform/observability/otel-pipeline-redaction-check.py` (30a onda; antes era grep sobre nomes de pipeline HARDCODED "traces"/"logs" — um pipeline `traces/raw` ou o proprio `metrics` escapavam sem verificacao). O script enumera **todos** os pipelines de `service.pipelines`, deriva o tipo de sinal do prefixo antes de `/`, reprova tipo desconhecido por construcao, e exige `transform/redact-pii` + `redaction/allowlist-<tipo>*` cabeados em `.processors` de CADA um — default-deny, sem lista de nomes. Alem disso, nenhuma ocorrencia de `allow_all_keys:` pode resolver para verdadeiro (case-insensitive, sobre o arquivo inteiro).

**Doc:** `TX-5` · `stack-tecnologico.md 2.7` · `stack-tecnologico.md 5 (risco Conformidade)` · **Trava:** plataforma-base (platform/observability), todos os servicos que emitem telemetria, go-live-runbook 5 (smoke)

##### P3 · Capping efemero: chave hasheada com salt rotativo + TTL curto + fail-safe (DA-6)

- Chave = cap:+SHA-256(userID+:+salt)[:32]+scope+campaign+banner (internal/capping/capping.go:246-260); salt rotativo do OpenBao (CAPPING_SALT), fail-closed com panic se vazio (capping.go:99-115).
- TTL SEMPRE setado atomicamente junto do INCR via Lua (INCR+PEXPIRE) - janela onde uma chave pseudonima ficaria PERMANENTE e fechada (capping.go:214-244); teto de retencao 90 dias no campaign_total + grace TTL para campanha expirada, nunca TTL=0 (capping.go:262-294).
- Fail-safe DA-6: campanha capeada SEM identificador estavel -> Allowed=false (aborta a entrega, NAO cria perfil) (capping.go:152-156); Redis indisponivel em campanha capeada -> false (capping.go:167-205); campanha sem cap serve anonimo sem Redis (capping.go:145-150).
- userID cru confinado ao pacote - nunca em struct/log/evento/telemetria/store (capping.go:16-31).
- Golden de fail-safe presente e verde (tests/parity/ca5_capping_golden_test.go).

**Doc:** `TX-5` · `DA-6` · `CA-5` · `stack-tecnologico.md 2.1` · **Trava:** I2 (capping no decision), motor-de-decisao

##### P4 · Isolamento entre tenants: RLS + TESTE de isolamento em config/ledger/RAG/analytics

- RLS ENABLE+FORCE com policy USING(tenant_id=current_tenant_id()) fail-closed (helper retorna NULL sem SET LOCAL -> rejeita tudo) em config, ledger, vector e compliance (db/*/migrations).
- EXIGIR o teste de isolamento entre tenants executavel para RAG/pgvector (db/vector/tests/vector_rls_isolation_test.sql): leitura cross-tenant=0 (Bloco 3), fail-closed sem tenant_id (Bloco 4), WITH CHECK rejeita INSERT com tenant_id forjado / doc publico NULL forjado (Bloco 7).
- Testes equivalentes para ledger (db/ledger/tests/rls_isolation_test.sql) e compliance (db/compliance/tests/rls_isolation_test.sql) - dinheiro e PII cobertos por leitura+escrita cross-tenant.
- BFF aplica RLS por request via set_config(adserver.tenant_id,...,true) em transacao + WHERE defense-in-depth (bff/src/adapters/postgres-payments.ts); LLM nunca recebe credencial e RAG e SEMPRE filtrado por tenant_id (search_similar_creatives, services/copilot/tools/gateway.py:386-394).
- adserver_loader (BYPASSRLS) confinado ao loader read-only do hot path - CA-1 imposto no snapshot em memoria (db/seed/dev_roles.sql:6-13).
- CI: db-test-all roda os 4 testes RLS em Postgres efemero (make/db.mk).

**Doc:** `TX-3` · `CA-1` · `DA-11` · `ADR-0004 F` · **Trava:** I1 (config/ledger RLS), J5 (RAG pgvector), K3/K6 (ledger cripto/compliance), BFF-console, copiloto

##### P5 · Cofre de compliance PII/KYC isolado + cifra KMS-envelope + ledger/telemetria sem PII

- PII/KYC vive SO no schema compliance, referenciada por tenant_id pseudonimo (UUID) e subject_ref - nunca CPF/e-mail/CNPJ (db/compliance/migrations/0001_compliance_schema_up.sql:72-135); documentos reais ficam no Sumsub (fonte de verdade), a tabela guarda so referencia+status.
- Enderecos on-chain em screening_results NAO sao PII de identidade; a associacao endereco<->identidade fica so em kyc_subjects (schema:155-198).
- Colunas de nome (full_name/originator_name/beneficiary_name) marcadas PII-cifrar em repouso (KMS envelope) (schema:110,244,246); cifra AES-256-GCM versionada v1$ antes do INSERT, fail-closed, chave via PII_ENVELOPE_KEY/OpenBao (services/payments/internal/kmsenvelope).
- INVARIANTE: ledger, proto de eventos e telemetria NUNCA recebem PII (comentario-invariante schema:6-8,19-22; Envelope.tenant_id pseudonimo, envelope.proto:23-26). Dinheiro NUNCA e cifrado (TX-2).
- Valores em screening_results/travel_rule usam INTEGER/NUMERIC(20,0), sem float (schema:171-174,254) - TX-2.
- Celula AML/KYC segregada em instancia separada; sanctions_hit / status=failed bloqueiam pagamento e NUNCA tocam a decisao de veiculacao (schema:198,279).

**Doc:** `TX-3` · `DA-11` · `TX-2` · `ADR-0004 F` · `ADR-0004 E.10` · `stack-tecnologico.md 2.7` · **Trava:** K6 (cofre/compliance), K4 (celula PCI), pagamentos, go-live-runbook 2.5/3.1

##### P6 · Proveniencia C2PA/SynthID + disclosure de IA (EU AI Act Art. 50) como gate de publicacao

- validate_creative e gate: criativo de IA SEM C2PA OU SEM SynthID OU SEM disclosure -> gate_passed=False (bloqueia publicacao, nao observacao) (services/copilot/tools/gateway.py:308-341,363).
- Disclosure gerado por IA / generated by ai / data-ai-generated exigido em HTML ou metadata EXIF/XMP (gateway.py:326-341); criativo nao-IA dispensa C2PA/SynthID (gateway.py:342-346).
- PII no HTML do criativo bloqueia publicacao: _detect_pii_in_html cobre CPF/e-mail/IP/telefone BR/cartao (gateway.py:348-355,857-870).
- Bytes do asset NAO vao ao LLM - so metadados/URL (gateway.py:292-293) - TX-5.
- GATILHO DE PRODUCAO (medivel): C2PA/SynthID hoje sao STUB (_stub_verify_c2pa gateway.py:311,319,828-835). Antes do vigor 02/08/2026 e do go-live, substituir por SDK real (c2pa-python) + COPILOT_C2PA_SIGNING_KEY (PEM no OpenBao) - sem isso o gate e cosmetico. Testes de contrato presentes (services/copilot/tests/test_gateway.py:165-232).

**Doc:** `TX-5` · `stack-tecnologico.md 2.4` · `EU AI Act Art. 50 (stack 4 Fase 2/3)` · `CA-8` · **Trava:** J5 (copiloto/criativos IA), geracao de criativo, go-live (chave C2PA/SynthID)

##### P7 · Sem transmissao inter-regional opaca de dados pessoais + residencia por celula/regiao

- Inspecao do fluxo confirma ausencia de transmissao inter-regional opaca de dados pessoais (CA-8): exporters OTel por endpoint regional via env (otel-collector.yaml:139-148); nenhum PII cruza celula/regiao (a redacao P2 garante que nem PII trafega).
- Segregacao em celulas (PCI + AML/KYC) com Cilium deny-all, contas cloud separadas e egress controlado (ADR-0004 F; stack 2.7); FQDNs e egress por celula (go-live-runbook 4).
- Observabilidade do copiloto (Langfuse) self-hosted - telemetria do LLM nao sai para terceiro (TX-5; 2.4).
- Multi-regiao por celula so sob prova; residencia de dados respeitada por celula/regiao (stack 2.7).
- GATILHO: preencher FQDNs reais das celulas e validar fronteiras de rede por QSA/auditoria regional antes do cutover (go-live-runbook 4; stack 5 risco Conformidade).

**Doc:** `CA-8` · `DA-11` · `TX-5` · `ADR-0004 F` · `stack-tecnologico.md 2.7` · `stack-tecnologico.md 5` · **Trava:** plataforma/celulas PCI+AML-KYC, K6, go-live-runbook 4, cross-region

##### P8 · First-party data / custom_vars nao vira perfil central + screening de PII no sink

- custom_vars e first-party data do publisher, casada pela regra Site-Variable, e DA-11 proibe torna-la perfil central (documentacao-tecnica.md 4.4; DA-11).
- ACHADO A IMPOR (gap): o collector encaminha custom_vars CRU para o AdRequest (services/collector/cmd/collector/main.go:174-180,199) e o proto trata como mapa OPACO sem semantica (events.proto:59-63) - NAO ha allowlist de chaves nem screening de PII nesse caminho. O event stream Redpanda->ClickHouse->Iceberg NAO passa pelo OTel Collector, logo a redacao de P2 nao o cobre. Exigir: (a) allowlist de chaves de custom var por tenant OU (b) redacao/screening de PII no sink antes de persistir - senao idade/genero/e-mail podem chegar ao store analitico como perfil.
- Confirmar que custom_vars nao alimenta NENHUM store de perfil persistente por usuario (so targeting por requisicao + agregacao StatsHourly sem chave de usuario) - DA-11/CA-8.
- OTel allowlist ja bloqueia qualquer atributo de custom var nao listado no export de telemetria (otel-collector.yaml:67-132); validar cobertura equivalente no sink de eventos.

**Doc:** `DA-11` · `documentacao-tecnica.md 4.4` · `documentacao-tecnica.md 4.6` · `CA-8` · `TX-5` · **Trava:** I3 (collector/custom_vars), dados-telemetria (sink ClickHouse/Iceberg), motor-de-decisao (regra Site-Variable)

##### P9 · Gate de ATIVACAO em producao: chaves vivas + stubs->reais (sob infra real)

- PII_ENVELOPE_KEY real (AES-256 KMS/HSM) injetada via OpenBao secret/aml-kyc/pii-envelope-key, substituindo o stub local (go-live-runbook 3.1) - sem ela o cofre nao cifra PII em producao.
- COPILOT_C2PA_SIGNING_KEY (PEM no OpenBao) + SDKs C2PA/SynthID reais substituindo os stubs (P6; gateway.py:286-289).
- CAPPING_SALT vivo com rotacao agendada (ex.: diaria) do OpenBao (P3; TX-5).
- Aplicar EXPLICITAMENTE as migracoes de RLS nos 4 schemas (config/ledger/vector/compliance) em producao - omissao remove o isolamento (go-live-runbook 2.2-2.6).
- Rodar os 4 testes de isolamento RLS contra Postgres REAL no Passo 1 do smoke pre-cutover (go-live-runbook 5).
- make platform-validate VERDE (inclui otel-validate TX-5 / redacao P2) antes do cutover (go-live-runbook 1); celula compliance em instancia separada, nao co-localizada com ledger/telemetria (go-live-runbook 2.5; ADR-0004 F). BLOQUEADOR: infra/chaves vivas - trabalho nao-codigo, gated por go-live real.

**Doc:** `TX-5` · `DA-11` · `ADR-0004 F` · `go-live-runbook 1` · `go-live-runbook 2.5` · `go-live-runbook 3.1` · `go-live-runbook 5` · **Trava:** go-live cutover, K6/K4 (ativacao compliance/PCI), todos os addons (gate transversal de producao)

---

### 4.3 Malha de gates comuns a todo incremento

#### Malha de gates comuns a TODO incremento (condição de merge, não pendência)

Todo PR que toca qualquer addon atravessa a mesma treliça de gates antes do merge, e um subconjunto reforçado antes do cutover:

##### 1. Gate de contrato + dinheiro (hermético, roda em CI e local)
- **`make verify` = buf (TX-1) + no-float (TX-2)**, offline e determinístico:
  - `buf lint` (STANDARD+COMMENTS) + `buf format --exit-code` + `buf breaking --against main` → **compatibilidade BACKWARD obrigatória** (`.github/workflows/buf.yml`). Qualquer PR que quebre compat de evento é reprovado.
  - `no-float` em **6 guards** (`scripts/ci/no-float-{proto,go,ts,py,sql,data-sql}.sh`), cobrindo **Proto, Go, TS/TSX, Python e SQL**. `make no-float` roda os **6**; o workflow `.github/workflows/no-float.yml` roda **5** — o 6º (`no-float-data-sql.sh`, DDL de `data/` + jobs de faturamento) roda em `.github/workflows/data.yml` via `make data-validate`. **`float` proibido para dinheiro (TX-2), é piso não teto.**
    - **O escopo é DEFAULT-DENY com allowlist explícita — não uma lista de diretórios "financeiros".** Escopar por nome de arquivo/diretório foi a **classe dominante de falso-positivo em três ondas seguidas (27ª/28ª/29ª)**: dinheiro fora da convenção de nome escapava do gate. A fonte normativa do escopo vigente é **[`contracts/lint/no-float.md` §Escopo](../contracts/lint/no-float.md)** — consulte-a, não reproduza a lista aqui, e **nunca re-estreite um guard para um conjunto de diretórios** sem ADR que registre o motivo.
    - **Não reproduza aqui a lista de escopo.** Um resumo de escopo nesta página é uma cópia sem gate: a 30ª onda adicionou um bullet "em resumo (verificado hoje)" que dois fixes da **própria onda** tornaram falso no mesmo commit (a chave da allowlist Proto passou a (arquivo, **mensagem**, campo, tag), e o guard SQL passou de allowlist de migrations para **default-deny sobre todo `.sql` rastreado**). Uma linha nova com selo de "verificado" e conteúdo falso é pior que uma linha velha desatualizada. O escopo vigente, por linguagem e com o comando que o re-deriva, vive em **[`contracts/lint/no-float.md` §Escopo](../contracts/lint/no-float.md)**; os scripts em `scripts/ci/` são a fonte única da verdade.
    - `make no-float` carrega a sentinela anti-skip `NO_FLOAT_SCRIPTS_EXPECTED := 6` (Makefile): se o glob deixar de casar algum guard (renomeado, movido, sparse-checkout), o alvo **falha** em vez de sair verde com o loop vazio.
- `proto-gen-check` é job separado (depende de rede/plugins remotos, propositalmente fora de `verify` hermético). O falso-positivo por bump cosmético de plugin remoto foi **resolvido na 20ª onda** (E8: pin de versão em `buf.gen.yaml`); o gate hoje reprova **apenas** drift real de schema.

##### 2. Gate de segurança + privacidade (sem CRITICAL/HIGH)
- **`security-reviewer`** e **`privacy-compliance-auditor`** assinam **sem CRITICAL/HIGH** (regra dos ADR-0003 §G / ADR-0004 §H). A malha transversal ataca por dimensão:
  - **S1** isolamento multi-tenant fim-a-fim (borda→BFF `tenantProcedure`→RLS Postgres/pgvector `USING`+`WITH CHECK` fail-closed→row-policies ClickHouse); **CA-1: ACL só server-side, nunca no cliente**.
  - **S2–S4** copiloto (LLM sem credencial, autz server-side ignora payload, RAG com RLS+teste de isolamento, **HITL obrigatório em toda escrita**).
  - **S5/S10** delivery e webhooks (HMAC no /ck, validateDestURL anti-SSRF, assinatura de webhook, sem PAN no backend).
  - **S6** segredos (OpenBao dynamic + Pod Identity + KMS/HSM; **nada estático em imagem/git**).
  - **S7/S9** células PCI+AML/KYC (Cilium deny-all, Kyverno digest, cosign/SBOM/Trivy/Falco).
  - **P1–P9** privacidade como gate de aceitação (TX-5/DA-11/CA-8): sem PII/IP bruto nos eventos, redação OTel fail-closed antes de qualquer export, capping efêmero, cofre de compliance cifrado (KMS-envelope), proveniência C2PA (EU AI Act Art. 50).

##### 3. Gate de ledger (em TODO valor monetário)
- **`money-ledger-guardian`** revisa **todo incremento com valor monetário** (eCPM, billing CPM/CPC/CPA/Tenancy, postings, Money no fio): int64+scale minor-units, `ROUND_HALF_EVEN`, `sum(debit)=sum(credit)`, saldo derivado (nunca gravado direto), câmbio só como par de postings explícito (DA-10), **reconciliação exclusiva contra Iceberg (nunca ClickHouse/streaming)**, exceção que nunca autocorrige. Ref: `TX-2`, `DA-10`, `CA-7`.

##### 4. Gate de paridade (antes de qualquer cutover)
- **`parity-golden-test-guardian`** exige goldens **CA-2/CA-3/CA-4/CA-5/CA-6** verdes com `-race` e o invariante **cascata pura ≡ ranker/deep fail-open bit-a-bit** (ML/deep nunca muda tier nem faturável, DA-3/ADR-0004 §A). Antes do corte de tráfego: **shadow-traffic + dual-run contábil dentro da tolerância declarada** (`ADR-0002 §D`), com os critérios numéricos fixos em código (0.1% decisão / 0.5% faturamento / 48h / 100k decisões). **Divergência acima da tolerância bloqueia o cutover, sem exceção; silêncio = reprovação.**

##### 5. Gate de plataforma (quando toca `platform/`)
- **`make platform-validate`** = **6 checks** (`make/platform.mk`): `platform-tofu-validate` + `platform-kubeconform` + `platform-kyverno-test` + `platform-otel-validate` (TX-5) + `platform-openbao-policy-check` (least-privilege por célula, HCL) + `platform-cell-consistency` (sem drift de nome de célula entre `tofu/{main,variables}.tf`, `k8s/namespaces.yaml` e `cells/*` — ligado na 28ª onda). SHA256 fixado nos binários de CI (`.github/workflows/platform.yml`). Espelha `make verify` para a plataforma; hermético.

##### 6. Fecho de fase e apply em cloud
- Uma fase só fecha quando seus **CA-n estão verdes e validados** pelo guardião dono (delegado ao parity-golden-test-guardian consolidar CA-1…CA-9). **Qualquer `tofu apply`/cutover em cloud exige aprovação humana explícita** — nenhuma ação destrutiva/remota autônoma na plataforma (regra do addon de infra).

---

<a id="5-o-próximo-plano"></a>

## 5. O próximo plano

> Como todas as fases estão código-completas, **o próximo plano é a Onda de Ativação de Go-Live** (transformar código-completo em produção sob infra viva, seguindo `docs/ops/go-live-runbook.md`) **mais os sucessores pós-Fase-3**, cada um sob gatilho mensurável.

#### O PRÓXIMO PLANO — Onda de Ativação de Go-Live + Sucessores sob gatilho

Sequenciamento em 5 etapas de ativação (**G0→G4**) seguindo `docs/ops/go-live-runbook.md`, mais os sucessores pós-Fase-3 (**S1…S8**), cada um sob seu próprio gatilho.

##### G0 — Fechar as pré-condições de CÓDIGO (sem infra) — ✅ CÓDIGO-COMPLETO (7/7, ondas 17ª–23ª; mergeado na `main` `b4cb624`)
Estas eram as únicas pendências de teclado; entraram **antes** de abrir a porta de produção. **Todas fechadas** — o próximo movimento real é **G1 (cutover de infra)**, gated.
- ✅ **decision-engine E6 + ml E10 — HOT-1/HOT-3 (RankResult por-request).** **Fechado na 17ª onda.** Bloqueante era: `parity` E14 recusa avaliar a promoção K8 enquanto o campo `last` compartilhado puder enviesar o OPE sob concorrência. Donos: `decision-engine-engineer` + `ml-optimization-engineer`; gate `parity-golden-test-guardian`. Ref: `ADR-0003 §G (J3/J4)`, `TX-4`, `DA-3`, README (HOT-1/HOT-3).
- ✅ **decision-engine E5 — hot-reload do GeoLite2 (.mmdb)** sem restart (RWMutex). **Fechado na 18ª onda** (`sync.RWMutex` em `internal/geo/maxmind.go` + `runGeoReloader` no collector + teste `-race`; gate parity PASS). Dono: `decision-engine-engineer`. Ref: `DA-9`, `CA-9`, `§4.10`.
- ✅ **ml E11 — ONNX Runtime nativo** (OnnxInferencer sob build tag, preservando build hermético `ADR-0002 §C`). **Fechado na 19ª onda** (`services/ranker-sidecar/internal/onnx/`, `//go:build onnx` + `!onnx`; re-export `zipmap=False`; paridade Go≡Python bit-exata; gates tech-lead + parity PASS). Dono: `ml-optimization-engineer`. Ref: `ADR-0003 §B`.
- ✅ **schema E8 — corrigir falso-positivo do `proto-gen-check`** (pin de versão dos plugins remotos em `buf.gen.yaml`: `protocolbuffers/go:v1.36.11`, `bufbuild/es:v2.12.0`). **Fechado na 20ª onda** (regeneração byte-idêntica; gate verde e provado não-tautológico; distinção schema-diff vs gerador-diff documentada em `proto/README.md`+CI; gates `parity-golden-test-guardian` + `tech-lead-architect` **PASS**). Dono: `schema-contracts-steward`. Ref: `TX-1`, `.github/workflows/buf.yml`.
- ✅ **platform-infra E8 — fechar item 4 do mandato.** **Fechado na 21ª onda** (item 4 = 4/4: pipeline `supply-chain.yml` com SBOM syft + cosign **keyless** + Trivy fail-CRITICAL + push ghcr; Dockerfiles de produção `deploy/docker/`; ruleset Falco `platform/observability/falco-rules.yaml`; `policy-aml-kyc.hcl` menor-privilégio; `REPLACE_WITH_COSIGN_PUBLIC_KEY` substituído por keyless). **Bônus:** corrigido o falso-positivo pré-existente que deixava `make platform-validate` vermelho (httproute/otel/kyverno-cells) → gate genuinamente verde. Gates `security-reviewer` + `privacy-compliance-auditor` + `tech-lead-architect` **PASS**. Dono: `platform-infra-engineer`. Ref: mandato item 4, `§2.7`, `CA-9`.
- ✅ **frontend E9 — fail-closed do middleware** em produção sem `SESSION_SECRET`. **Fechado na 22ª onda** (`60fb0c8`; predicado puro `session-guard.ts` + dupla defesa; `security-reviewer` PASS `productionBypassPossible=false`). Dono: `frontend-bff-engineer`. Ref: `TX-3`, `CA-1`, `§2.5`.
- ✅ **frontend E10 — alinhamento de stack §2.5.** **Fechado na 23ª onda** (Next 15.3.3→**16.2.10**, React 19.1→**19.2.7**, `next lint`→eslint flat nativa, `watch()`→`useWatch()`; **a11y-ci axe/WCAG-2.2-AA sem Playwright** — `puppeteer-core` + Chrome do sistema, decisão do tech-lead; alicerce shadcn; **focus-trap do modal HITL** corrigido + verificação mecânica). **Zustand e Vercel AI SDK v5 diferidos com gatilho** documentado (Zustand→E12; AI-SDK→ADR-0003 sucessor). Gates: `tech-lead-architect` (escopo mínimo) + `make web-ci`/`web-a11y` + `security-reviewer` **APROVADO**. Dono: `frontend-bff-engineer`.
- ✅ **copiloto E12 — higiene de layout** (removidos os pacotes vazios `guardrails/`/`rag/` do `pyproject.toml` + rmdir). **Fechado na 23ª onda** (`e3aca4c`; `find_packages`=`['app','graph','observability','tools']`; `make copilot-test` 126). Dono: `copilot-llm-engineer`; gate `tech-lead-architect`.

> **Sweeps pós-G0 de falsos-positivos (a malha de gates é o produto tanto quanto o código que ela guarda).** Como G0 fechou o último item de código, o trabalho pós-G0 é garantir que os gates *realmente imponham* o que prometem — não haja gate tautológico, doc-lie ou falso-RED. 24ª (falso-RED do `platform-validate` com path-espaço), 25ª (13 gates tautológicos por mutation-testing), 26ª (doc-lie do forbidigo). **27ª onda (2026-07-19): auditoria adversarial multiagente das 12 famílias de gate → 31 falsos-positivos confirmados e corrigidos, cada um provado por mutação empírica; 3 guardiões (money/security/privacy) APROVARAM sem CRITICAL/HIGH.** Principais: gate TX-2 no nível `.proto` inexistente apesar do `make verify` ecoar "TX-2 validado" (novo `no-float-proto.sh`); step TS do `no-float.yml` quebrado (sem `eslint.config` na raiz → CI verde sem lintar dinheiro) — novo `eslint.config.mjs` por glob de convenção; `data-schema-invariants.py` satisfeito por substring global (agora escopado por-statement); **kyverno 1.13.4 mascarava "declared fail, actual pass" (bug upstream #15361) deixando a direção de rejeição de admissão não-imposta → bump 1.18.2 + schema `resources:`**; `data-ivt-test`/`ml-test` full órfãos da CI (agora plugados); + testes-guarda de invariantes afirmados sem cobertura (HMAC/HITL/IDOR do copiloto, ACL do BFF, RLS WITH CHECK do cofre KYC e do RAG, double-entry negativo, hot-swap de snapshot, hash do capping). **LIÇÃO: um gate verde não é prova de gate real — só a mutação que ele DEVERIA pegar prova; e checagens por presença de substring no corpus são tautológicas, precisam ser escopadas ao statement.**
>
> **28ª onda (2026-07-19): auditoria adversarial das 12 famílias (32 agentes de audit+verify) → 20 falsos-positivos confirmados de 1ª mão por mutação; 10 bundles-dono corrigiram cada um provado por mutação (fix permanece, só a sonda é removida); 5 guardiões (money/security/privacy/parity/tech-lead) PASS sem CRITICAL/HIGH.** Destaques: (TX-2) `no-float-proto.sh` só varria `money/**`+`payments/**` — um `double` na msg `Conversion` (que carrega `Money`) escapava → agora varre **todo** `proto/adserver/**` com default-deny + allowlist por (arquivo,campo,tag); lint TS de dinheiro escopado por **nome de arquivo** deixava `parseFloat` passar em `refunds-cash.ts` → novo `scripts/ci/no-float-ts.sh` (backstop por CONTEÚDO sobre todo `bff/src`); console eslint impunha 1-de-4 regras → melhorado (**follow-up #1** — mas a alegação de "4 regras" era ela própria um doc-lie: o `TSNumberKeyword` nunca esteve em config algum; **29ª onda #14** corrige para **3 seletores de lint** [parseFloat/Number/literal-float] + ban do tipo `number` cru via tipo branded `Money` em compile-time); forbidigo Go cego a float64 implícito (`price := 12.50`) → detecção de literal decimal nos pacotes puro-dinheiro; **`ledger.postings` sem imutabilidade no DB — a role da app fazia `UPDATE`/`DELETE` de postings lançados** → migration `0004` (trigger `BEFORE UPDATE/DELETE` que RAISE + `REVOKE` least-privilege, provado contra PostgreSQL 16 nativo); `ml-batch-no-float` `\b` não pegava `bid_minor_units` (underscore) → fronteira _-aware; `ml-deep-gate` não instalava `onnxscript` (3 testes de paridade ONNX **SKIPPED em silêncio**, job verde) → instala + step-sentinela; no-float SQL por-linha e fail-closed de row-policy só escopado p/ `stats_hourly_state` → por-statement nas 6 tabelas; guard-tests de `WITH CHECK` tautológicos (USING≡WITH CHECK) → introspecção `pg_policy.polwithcheck IS NULL`; `copilot.test.ts` abortava com `jest is not defined` (**`bff-ci` vermelho e NÃO-discriminante**) → `import @jest/globals` + testes funcionais do router (bff-ci 54→92); `checkDiffForContradictions()` fazia self-compare (`void selfCheck`) em vez de reusar `detectContradictions()` → reuso real; **`apply_write_node` não impunha o gate de proveniência C2PA/SynthID/ausência-de-PII nem o CA-4** (Art. 50 EU AI Act, vigor 02/08/2026) → bloqueia antes de persistir; drift de célula `aml` vs `aml-kyc` no OpenTofu → rename + gate `check-cell-consistency` ligado ao `platform-validate` e à CI. **#11** (double-count de faturamento do StatsHourly sob reentrega at-least-once do mesmo `event_id`) recebeu fix conservador espelhando o `FINAL` de `live_stats_exact` + teste de idempotência, mas **`run_verified=false` (ClickHouse ausente)** — lacuna arquitetural documentada e escalada ao tech-lead/ADR, não corrigida às cegas. **Follow-ups #1 e #2 do plano FECHADOS** (console — 3 seletores de lint + branded `Money`, ver 29ª #14; `_verify_hmac_pure` removido com cobertura migrada p/ o código real). Re-gate de 1ª mão (`make verify`, `go-lint`, ml/data, `bff-ci` 92, `copilot-test` 139, `web-test` 34, `platform-validate`, DB contra PG16 nativo) + 3 spot-checks de mutação (proto/ts/ledger) confirmaram não-tautologia. O próprio sweep introduziu 2 doc-lies/orphans (o §5 do `no-float.md` e o `check-cell-consistency` desligado da CI) — **detectados pelos guardiões e corrigidos na mesma onda**. **LIÇÃO: escopar um gate a diretório/nome "financeiro" é ponto-cego ativo — dinheiro fora da convenção escapa; use default-deny abrangente + allowlist explícita. E todo sweep gera seus próprios falsos-positivos — re-audite o diff do sweep antes de fechar.** Residuais não-bloqueantes p/ a 29ª: cópia `_check_auth_config_production_pure` em `test_security.py` (irmã do já-removido `_verify_hmac_pure`); redação de `str(conflict)` no log `apply_write_node.validation_gate_blocked`; imagem `pgvector/pgvector:pg16` do `db-test-all` e run-verify do `#11` pendem de Docker/ClickHouse.

> **29ª onda (2026-07-19): 5ª varredura pós-G0, precedida do seed E11 real.** Antes da varredura, a pré-condição de go-live de CÓDIGO **E11 (lado-AdServer)** foi fechada (commit `5ba7402`, push origin/main FF): `db/seed/hojex_news_seed.sql` provisiona as **4 zonas por-placement reais do Hojex News** (1001 sidebar · 1002 in-article · 1003 leaderboard 728×90 · 1004 listing MREC), fonte-única-da-verdade partilhada com o `ZONES` do site (`web/src/lib/ads/config.ts`); teste `internal/configload/hojex_zones_integration_test.go` (4/4) provado NÃO-tautológico pelo `parity-golden-test-guardian` (mutar 1001→9999 faz só o subteste sidebar falhar) + isolamento RLS por-tenant provado ao vivo. **Depois**, auditoria adversarial das 12 famílias de gate (workflow `wave29-false-positive-audit`: dono-por-família find → cético default-refute) → **15 falsos-positivos CONFIRMADOS** (de 19 candidatos; 3 refutados, 1 incerto), cada um provado por mutação e corrigido por bundles-dono em arquivos disjuntos. **A lição-mãe da 28ª reincidiu como classe dominante (scope-blindspot):** (#1) o no-float Go não cobria `internal/configload/{loader,assemble}.go`, que monta o Money/ECPM AUTORITATIVO (float ali escapava do grep E do forbidigo) → adicionado a `no-float-go.sh`+`GO_MONEY_PKGS`+`.golangci.yml`; (#2) o lint TS de dinheiro do console não cobria `app/campaigns/page.tsx` (form da Rate) → glob eslint estendido a `src/app/**/*.tsx` (default-deny) + `no-float-ts.sh` estendido a `web/console`; (#3/#13) o motor canônico de faturamento `data/iceberg/jobs/billing_batch_hourly.py` escapava (glob dir-vs-arquivo + regex só-`float(`) → bloco Python de `no-float-data-sql.sh` reescrito **string-aware** (pega `Decimal(10.00)` nu mas não `Decimal("10.00")` string), vocab ampliado, default-deny; (#5) `ledger.reconciliation_exceptions` tinha FORCE RLS mas policy **USING-only** e zero teste (a allowlist hardcoded de 3 policies do BLOCO 6.5 a deixava escapar) → +`WITH CHECK` + introspecção default-deny `pg_policy.polwithcheck IS NULL` + BLOCO 9/9.5; (#6) kyverno `proibir-env-secretkeyref` não cobria `initContainers[]` → condição espelhada + fixtures; (#9) `go.yml` não disparava para diffs só-`ml/**`, então o gate Go de paridade anti-skew NUNCA rodava para mudanças de featurização Python → paths `ml/features/**` adicionados. **Tautológicos/hollow/orphan/doc-lie:** (#4) `buf.yml` no push-trigger fazia self-diff contra a própria `main` já avançada (breaking sempre verde) → `breaking_against` condicional por evento (`github.event.before` no push); (#7) o gate otel `allow_all_keys` só pegava `true` minúsculo (yaml.v3 aceita `True`/`yes`/`on`) → default-deny case-insensitive; (#8) o job `ml-full` não tinha a sentinela anti-skip do contrato ONNX zipmap=False (par não-espelhado do fix #8 da 28ª) → sentinela + guard de skip; (#10) `db.yml` **nunca aplicava a migration 0004 nem rodava `postings_immutability_test.sql`** (a garantia append-only da 28ª era órfã da CI por-PR) → aplica 0004 + REVOKE + roda o teste; (#11) `TestBalance_*` testava uma reimplementação local, não o `checkBalance` de produção → wrapper `CheckBalanceForTest` + testes reais; (#12) o check CA-6 `NAO-FATURAVEL` era substring no corpus concatenado (satisfeito por comentário de 004/007) → escopado ao `COMMENT ON TABLE` de cada view ao-vivo; (#14) o doc-lie "4 regras" do console (seletor `TSNumberKeyword` fantasma, nunca em config) → doc corrigida para 3 seletores de lint + tipo branded `Money` em compile-time. **Barreira de 5 guardiões sobre o diff completo (27 arquivos) — e ela pegou um falso-positivo que o PRÓPRIO sweep introduziu:** o `money-ledger-guardian` **BLOQUEOU** a 1ª versão do #3/#13 — a exclusão ML `non_money` que eu adicionei era **por-linha inteira**, mascarando um `float()` monetário real quando um termo ML co-ocorria na linha (ex.: `payout_rate = float(base_rate) * score_multiplier`), **regredindo** cobertura que o script antigo tinha (a mesma classe da tautologia "substring por-linha"). Corrigido removendo a exclusão (default-deny puro; baseline limpo sem ela, provado), re-blessed PASS. O `tech-lead-architect` exigiu 1 correção doc-only (o seletor de exemplo `callee.object.name='Number']` em `no-float.md` só casa `Number.foo(x)`, nunca `Number(x)` direto → `callee.name`). **5 guardiões (money/security/privacy/parity/tech-lead) PASS 0 CRITICAL/HIGH**; re-gate de 1ª mão VERDE (make verify + 6 no-float, go-lint incl configload, go build/vet/test, db-test-ledger + imutabilidade contra PG16 nativo com spot-check de mutação, platform-validate kyverno 11/11, data-validate, web-ci 39/39). **LIÇÕES novas: (1) uma EXCLUSÃO por-linha é o espelho da inclusão por-substring — ambas são over/under-reach de escopo; escope ao TOKEN/identificador. (2) o guardião de dinheiro sobre o diff é o que pega o falso-positivo que o sweep de dinheiro cria. (3) ao editar um bloco de doc por honestidade, limpe TODO o snippet (o exemplo vizinho quebrado também engana).** Residuais não-bloqueantes p/ a 30ª: assimetria `rate`-solo do `no-float-ts.sh` (sem exclusão ML — default-deny intencional, allowlist quando surgir `conversionRate=0.5`); cego do backstop `.tsx` para literal nu em property-line sem sinal-de-código (net-aditivo, coberto pelo ESLint AST); redação de `str(conflict)` no log do `apply_write_node` (herdado da 28ª); `pgvector`/ClickHouse do `db-test-all`/#11 pendem de infra. **G0 segue código-completo; E11 (seed real) fechado; próximo movimento real = G1 (cutover de infra, gated).**
>
> **30ª onda (2026-07-19/20): 6ª varredura pós-G0 — a que achou funcionalidade AUSENTE, não só gate frouxo.** Auditoria adversarial das 12 famílias de gate + 1 família de coerência doc↔código (workflow `wave30-false-positive-audit`, 45 agentes: dono-por-família *find* → cético *default-refute* obrigado a executar a mutação) → **27 falsos-positivos CONFIRMADOS de 31 candidatos** (4 refutados — e as refutações foram substantivas: p.ex. a alegação de que `internal/cascade/cascade.go` escapava do no-float caiu porque a exclusão é **deliberada e documentada em dois lugares independentes**, `.golangci.yml` + `no-float.md:59-60`). Todos os 27 com `run_verified=true`.
>
> **O achado que não era gate frouxo, e sim código faltando — `calibration-never-applied-in-serving` (CRITICAL).** A calibração isotônica (mandato #3, J2 declarado completo desde a Fase 2) é computada, testada, versionada no MLflow — e **nunca lida por nada**. Verificado de 1ª mão: `grep -rni calibrat services/ranker-sidecar/` = 0; zero consumidores de `calibration_map.json` fora de `ml/`; `onnx.go:193` devolve `data[1]` cru com clip e nada mais; o `.onnx` sai do booster sem camada de calibração embutida. A assimetria é a prova: **inverter completamente `apply_calibration` deixa `make ml-calibration-test` VERMELHO (3 falhas) e não move um bit em nenhum gate do caminho servido**. Produção serviria `pCTR_raw × bid`, não `pCTR_cal × bid`, com a doc afirmando o contrário em 4 lugares (`calibrate.py:13-16,29-30,229,246-247`; `score.go:68` "from the calibrated sidecar output"). Fechado em três partes: (a) `services/ranker-sidecar/internal/calibration` (interpolação linear por busca binária, fail-open que serve o raw se o mapa faltar, build-tag-neutro preservando `ADR-0002 §C`) + `internal/wiring.BuildInferencer` como fiação única de produção; (b) o gate que faltava — `TestCalibratedServingParity` compara o valor SERVIDO contra `apply_calibration(booster.predict(x))` do Python; (c) só então os comentários. **Não chegou a distorcer faturamento porque `RANKER_ENABLED` é default-off e não há tráfego real — mas teria, no G4.**
>
> **Classe dominante pela 3ª onda seguida (scope-blindspot / escopo estreito):** `no-float-multiline-split` (CRITICAL) — a conjunção *(identificador de dinheiro)* **E** *(literal float)* era avaliada por **linha física** nos 3 backstops de conteúdo, então qualquer quebra do Prettier/Black separava os dois e a violação sumia → janela trocada para **linha lógica** (tokenizer da stdlib no Python, heurística de continuação no TS); `no-float-clickhouse-nullable` (CRITICAL) — `Nullable(Float64)` monetário escapava porque o regex não via dentro do wrapper → wrapper transparente; `dedupe-order-by-event-id` (CRITICAL) — validava `ENGINE=ReplacingMergeTree` mas **nunca que `event_id` está na `ORDER BY`**, e é a `ORDER BY` que deduplica → o gate aprovava dedupe inexistente; `no-float-proto` allowlist chaveada por *(arquivo, campo, tag)* sem a **mensagem** → em proto3 a numeração é por-mensagem, e uma mensagem nova colidindo em (nome, tag) era absorvida pela revisão de outra → chave de 4 elementos; `creative-pii-gate` só varria `html_content` → CPF/e-mail em `dest_url`/`asset_url` publicavam com `gate_passed=True`. **Tautológicos/ocos:** `copilot-rag-isolation` (CRITICAL) asseria substrings **existentes apenas em código COMENTADO**; `ca6-blank-billable` (CRITICAL) — o único gate de "impressão em branco nunca é faturável" testava uma reimplementação de 2 linhas dentro do próprio teste → extraído para `ComputeBlankBillable`, produção e golden chamam a MESMA função; guards de ACL do BFF e de GUC-de-tenant do adapter cobriam 1 router e 2-de-45 métodos → **exaustivos por reflexão** (default-deny, um método novo é coberto por construção).
>
> **A barreira de guardiões voltou a ser o que pega o que o sweep quebra — e desta vez 4 dos 5 BLOQUEARAM.** CRITICAL apontado independentemente por `security` e `tech-lead` (este reproduzindo contra **PG16.14 nativo**): a migration `config/0004` criada pela onda nasceu **órfã** — os 4 runners enumeravam migrations por **lista hardcoded** parando em 0003, e o job `db` iria vermelho. É a 3ª ocorrência da mesma classe (a 29ª #10 achou a `0004` do ledger órfã do `db.yml`), então a remediação não adicionou a instância à lista: **trocou a enumeração por glob derivado** com sentinela anti-vazio, mais `db-check-migration-pairing` (todo `_up` tem `_down`) e `db-check-schema-list` (diretórios reais vs `DB_SCHEMAS`). HIGH: **o gate que provava a correção da calibração era ele próprio órfão da CI** — build tag excluía-o da invocação padrão, e trocar `calibration.Wrap(onnxInf, calMap)` por `onnxInf` mantinha o comando da CI verde → novo job `go-onnx-parity` com sentinela anti-skip. HIGH: o fix do `contradiction-vector-allowlist-gap` **reintroduziu a mesma classe um nível acima** (cópia manual do enum) → derivado de `DeliveryRuleVectorSchema.options` com exaustividade em compile-time (`satisfies`), mais backstop CA-4 em **todos** os mutadores (o fix cobria só `create`; `update` era bypass trivial). HIGH: a correção *de honestidade* do OTel afirmou que traces/metrics atravessam o collector — tão falso quanto o que acabara de corrigir para logs.
>
> **Na 4ª rodada, o `tech-lead` achou o buraco de dinheiro mais direto da onda, e o tech lead humano reproduziu de 1ª mão:** o gate TX-2 das specs Iceberg casava apenas `type: float` — mas o primitivo de ponto flutuante de 64 bits do Iceberg chama-se **`double`**. Mutar `rate_amount` (a **taxa contratual contra a qual o faturamento reconcilia**) de `decimal(38, 18)` para `double` em `billing_hourly.yaml` deixava o guard, o `make no-float` e o `make verify` **todos verdes**. Uma palavra. Corrigido por default-deny sobre o TIPO; o braço ClickHouse teve a forma invertida no mesmo movimento (era denylist de nomes mantida à mão — a onda a havia AMPLIADO em vez de inverter, então `gross_earnings Float64` escapava por construção).
>
> **Veredito final: 5 guardiões PASS, 0 bloqueios de commit** (`security` e `tech-lead` explícitos: *"esta árvore pode ir para a main — não que ela seja perfeita"*). Re-gate de 1ª mão do tech lead humano, VERDE: `make verify` · `go-build/vet/test/lint` · `data-validate` · `bff-ci` · `web-ci` · `copilot-test` · `platform-validate` · `parity-golden` · os 3 gates novos (`db-check-migration-pairing`, `db-check-schema-list`, `check-trivyignore`). Mais 2 spot-checks de mutação de 1ª mão: `rate_amount → double` agora **VERMELHO** com mensagem nomeando o campo; remover `calibration.Wrap` do wiring agora **VERMELHO** (`calibration was not wired into serving despite a valid model and calibration map being present`).
>
> **Ressalvas §6 fechadas:** ref fantasma `ADR-0002 §D (I5)` corrigida para a fonte real (o ADR diz literalmente "5 incrementos", I0–I4; I5 só existe no README) · refs bare `§2.3` qualificadas como `stack §2.3` · CA-1…CA-9 do `documentacao-tecnica.md §5` adjudicados — e a adjudicação revelou o que a ressalva original não registrava: **CA-9 exigia "instala em stack LAMP/LEMP (PHP + MySQL/MariaDB)" e "plugins em `/etc/plugins`"**, critérios do Revive legado que a reescrita Go nunca satisfará por construção; o §5 não estava só estagnado, estava parcialmente **inaplicável**.
>
> **LIÇÕES novas: (1) `git add -N` grava blob de ZERO bytes no índice — `git checkout -- <arquivo>` num arquivo intent-to-add APAGA o conteúdo em vez de restaurar; um guardião destruiu `contradiction.ts` assim e o recuperou de um sourcemap do `.next`. Protocolo de mutação passa a ser `cp` backup → mutar → `mv` restaurar. (2) Arquivo untracked é INVISÍVEL a todo gate que seleciona por `git ls-files` — o `make no-float` verde que 10 bundles relataram não cobria NENHUM dos 15 arquivos novos da própria onda. Todo sweep tem de tornar o código novo visível ao índice ANTES de alegar cobertura. (3) Corrigir a instância sem corrigir a FORMA garante a reincidência: lista hardcoded, denylist de nomes e cópia-sincronizada-por-comentário reabriram buracos que a mesma onda acabara de fechar. (4) Número em prosa apodrece — a onda corrigiu "contagens de teste estagnadas no runbook" e reintroduziu contagens erradas na mesma edição.** Residuais não-bloqueantes p/ a 31ª (16 registrados, os 3 de maior peso): `DATABASE_URL` do copiloto precisa de role **sem `BYPASSRLS`** (as queries pgvector dependem 100% do RLS e nada impede provisionar com `adserver_loader`) → item de checklist de G1; o fail-open da calibração **não é observável** (se o mapa não carregar, serve `pCTR_raw` com só um WARN de boot) → gauge alertável antes do G4; `capping.SetSalt` não revalida salt vazio, então o invariante DA-6/TX-5 vale só na construção. **G0 segue código-completo; próximo movimento real = G1 (cutover de infra, gated por aprovação humana).**

> **31ª onda (2026-07-25/26): 7ª varredura pós-G0 — a que achou um BUG DE PRODUÇÃO no ledger.** Auditoria adversarial por mutação em 5 superfícies (18 agentes: dono-por-família *find* → cético *default-refute* obrigado a **executar** a mutação) → 13 candidatos, **11 CONFIRMED, 2 REFUTED**. **O achado que nenhum teste pegava:** `internal/ledger/posting.go::insertPostings` enviava o parâmetro `$2` sem que o SQL **jamais o referenciasse** → Postgres rejeita com `could not determine data type of parameter $2` (SQLSTATE 42P18), ou seja **qualquer** `RecordEntry`/`RecordDeposit`/`RecordPayout`/`RecordReversal`/`RecordFXExchange` real falharia e toda captura Stripe/Asaas/MercadoPago daria 500. Causa-raiz: os "testes-guarda de double-entry" validavam uma **reimplementação em memória** (`store` stub) — 17 das 25 funções de teste nunca tocaram `ledger.RecordEntry`. Só apareceu ao apontar o **primeiro teste de integração contra um Postgres de verdade** (`posting_integration_test.go`, `//go:build integration`); `make go-test-integration` deixou de ser scaffolding (deriva pacotes por build tag, falha se nenhum existir) e entrou no `db.yml`. **Demais achados:** novo pacote `internal/privacyscan` (gate TX-5 default-deny de IP bruto em evento/WAL/log, que não existia); ad tag deixa de enviar `location.href` completo; doc-lie triplo do contrato `DecideRequest`; scan de PII do copiloto passa a rodar sobre o **payload de escrita** por introspecção; `RAW_TABLES_REQUIRING_DEDUPE` deixa de ser lista hardcoded; teste de isolamento de tenant do ClickHouse deixa de ser órfão da CI; fail-open da calibração deixa de ser silencioso (residual que a 30ª registrou); `policy-check.py` compara capability case-insensitive. **A barreira pegou o que o sweep quebrou — 3 de 4 BLOQUEARAM:** (a) **CRITICAL** — o scanner de IP recém-criado **não era default-deny**: `strings.Trim(cand, ".:")` decapitava IPv6, então `::1`, `2001:db8::` e **`::ffff:203.0.113.5`** (a forma que o stack dual-stack do Go entrega o IP do cliente) passavam batido → reescrito com sub-candidatos + `netip.ParseAddr`; (b) **HIGH** — `db/seed/dev_roles.sql` quebrou `make dev-db-setup` (GRANT em `vector_store` antes da migration existir, sob `ON_ERROR_STOP=1`); (c) **HIGH** — a derivação nova de dedupe **enfraqueceu** o gate (remover o `ReplacingMergeTree` passou a *isentar* a tabela em vez de violar); (d) **HIGH** — `data-integration-test.py` podia ficar **oco**. Todos remediados; re-guarda 3/3 PASS. **LIÇÃO-MÃE: interromper um sweep no meio deixa sonda de mutação no código** — ao detectar sessão concorrente viva, o `TaskStop` matou dois agentes *entre* mutar e restaurar, deixando duas violações TX-2 (`parseFloat("12.34")`, `Number(header) * 1.15`) em arquivos do console/BFF; só ficaram visíveis quando o gate passou a enxergar arquivos antes **untracked**. **Corolário: código untracked é invisível a todo gate por `git ls-files` — torne-o visível ao índice antes de alegar cobertura.** Registro completo no README (bloco da 31ª). Esta onda ficou **sem entrada no plano até a 32ª** — a 32ª a registrou retroativamente, junto da sua própria.

> **32ª onda (2026-07-28): o provisionamento LOCAL divergiu do disco — `dev-db-setup` montava um banco sem `WITH CHECK`.** Esta onda também **fecha o corpo de trabalho não-mergeado** que colidia em numeração com a 31ª (metade BFF da 31ª + perfil BETA/ADR-0005 + console, commits `f952ae5`/`73cd2e4`/`0caeb84`): ele estava commitado num branch, sem registro em README nem aqui, sem re-gate de fecho e sem barreira. **O achado, medido antes de descrito:** no banco que `make dev-db-setup` monta — o "Caminho A", que o README chama de VERIFICADO —, `config.campaign_zones_tenant_isolation` era a **única** policy do schema `config` com `pg_policy.polwithcheck IS NULL`, e `make db-test` **reprovava contra o banco que aquele mesmo alvo acabara de montar**, porque `make/dev.mk` enumerava as migrations de `config` numa lista escrita à mão que **parava em `0003`**. A `0004` não é cosmética: seu `WITH CHECK` é **estritamente mais forte** que o `USING` herdado por fallback — exige que o `zone_id` pertença ao tenant da sessão —, então sem ela um advertiser do tenant A podia vincular a **sua** campanha a uma zona de **outro** tenant, veiculando criativo em inventário alheio (TX-3, `documentacao-tecnica §4.6`). **O caminho Docker estava pior:** `deploy/local/postgres/10-init.sh` aplicava do ledger **só a `0001`** — sem RLS (`0003`) e sem append-only (`0004`) —, não criava `stats`/`vector`/`compliance` (o compose pinava `postgres:16`, sem `pgvector`: a **última instância viva** de um bug que o README já registra como corrigido em `db.yml`), e o cabeçalho afirmava paridade com `dev-db-setup` (doc-lie). **4ª reincidência da classe "enumeração mantida à mão"** (28ª → 29ª #10 → 30ª → esta): a 30ª corrigiu a **instância** nos 4 runners de teste e deixou os dois provisionadores de desenvolvimento de fora. Correção da **FORMA**: `db/schema-order.txt` (fonte única da ordem entre schemas, com `$(error)` anti-vazio em `make/db.mk`); os dois provisionadores derivam por **glob + sort** com sentinela por schema, como `db-test-all` e `db.yml`; GRANTs de `vector_store`/`compliance` alinhados com a CI; compose → `pgvector/pgvector:pg16`; e o gate novo **`make db-check-provisioners`** (default-deny: (A) nenhuma linha executável aplica migration por enumeração à mão; (B) enumeração em prosa numa janela de 12 linhas tem de estar completa — auto-atualizável, uma `0005` deixa vermelha toda lista velha). **Medido: 6/6 `db-test-*` PASS contra PG16 nativo num banco montado do zero por `dev-db-setup`; antes da onda, 3/6 FAIL.** **A varredura adversarial do diff não-mergeado** (5 donos-por-família → cético default-refute) devolveu 3 confirmados / 1 refutado, e os dois HIGH eram da família "gate que não alcança o que guarda": (i) **`make-quoting-check` órfão de novo, um nível acima** — a onda "perfil BETA" o tirou de `make verify` (que nenhum workflow roda) e o cabeou em `buf.yml`, cujo gatilho é `paths: proto/**`; editar `make/dev.mk` nunca o acionou → novo `.github/workflows/repo-gates.yml` **sem `paths:`**, casa de gates de superfície ampla, com critério de admissão escrito; (ii) **`push.paths` não espelhava `pull_request.paths` em `db.yml`** — o merge canônico é **push FF direto**, logo `push.paths` é a única reavaliação, e faltavam lá `make/db.mk` e o próprio `db.yml`: dava para neutralizar as sentinelas de schema e empurrar para a `main` sem o job `db` rodar. `go.yml` escrevera a regra ("espelha VERBATIM") no mesmo diff em que `db.yml` a violava → novo `scripts/ci/workflow-paths-mirror-check.py`, que reprovou **2 workflows** (`db.yml`, 2 paths; `buf.yml`, 1 path), não só o que a varredura apontara. (iii) LOW: o commit do console encurtara `Custo total (primeira hora — stub)` para `Custo total` mantendo `rows[0]` → rótulo honesto restaurado, **sem** somar no cliente (`stack §2.5` e `ADR-0004 K7` proíbem aritmética monetária no front). **O sweep gerou os próprios falsos-positivos e eles foram pegos antes do fecho** (3ª onda seguida): a 1ª versão do `db-provisioner-check` confundia *âncora* com *lista* → afiada para janela de proximidade; a 1ª do mirror-check reprovava `supply-chain.yml`, cujo `push` é por **tag** (release, não merge) → passou a ignorar `push.tags` sem `push.branches`. **A barreira BLOQUEOU a onda, e o bloqueio era da mesma família que ela estava fechando.** 4 PASS (money/security/privacy/tech-lead) e 1 BLOQUEIO do `parity-golden-test-guardian`, que executou o bypass num clone descartável: o `db-provisioner-check` varria **linha física**, então um `for f in \` quebrado em continuações (um nome por linha, caminho montado por `${f}_up.sql`) nunca tinha 2 nomes na mesma linha nem um nome ao lado de `migrations/`, e passava verde aplicando 3 das 4 migrations de `config`; e a checagem B media completude no ARQUIVO INTEIRO, então uma citação distante — inclusive a do próprio cabeçalho do gate — absolvia a lista incompleta. É o `no-float-multiline-split` (CRITICAL da 30ª) de novo, na mesma forma: janela física em vez de linha lógica. Gate reescrito em Python (`scripts/ci/db-provisioner-check.py`) com junção de continuações e completude num raio local. **Na RE-GUARDA o parity bloqueou de novo**, com bypass diferente: montar o id por concatenação (`f="${num}_${name}"`) faz o identificador nunca aparecer literal, e detecção por substring não tem o que casar; em paralelo o tech-lead demonstrou um terceiro (1 migration por linha + diretório em variável + espaçamento > B_WINDOW). A checagem A virou **dois predicados**: A1 — nomear QUALQUER migration em linha executável reprova (medido: zero linhas assim no repo, default-deny a custo zero de falso-positivo); A2 — referenciar `migrations/<nome>_up.sql` onde `<nome>` não é o glob `*` reprova por construção. E o guardião bloqueou uma TERCEIRA vez, com o bypass mais inocente: strings ADJACENTES (`"db/config/migrations/0001_config""_schema_up.sql"`), que o shell colapsa numa palavra só SEM variável nenhuma — quebrava a busca de A1 e o `migrations/` de A2 ao mesmo tempo e não caía no resíduo declarado; é a forma que aparece sozinha num refatoro de quebra de linha longa. Fechado normalizando a linha lógica (remoção de aspas) antes de toda busca, em A1, A2 e B. Os quatro bypasses conhecidos saem vermelhos; árvore limpa verde, zero falso-positivo. **Limite declarado em vez de rótulo confortável:** nenhum gate textual vence ofuscação total (esconder o diretório em variável E montar o id por concatenação não deixa assinatura); o que fecha a classe é COMPORTAMENTO — os seis `db-test-*` + `go-test-integration` contra um banco montado pelo provisionador, que foi como o defeito original apareceu. O gate textual é o alarme; a prova é a segunda linha, e está escrito assim no cabeçalho do script. **Os guardiões também pegaram 4 doc-lies escritos por esta própria onda:** `stack §2.2` → `§2.5` (a norma "nunca aritmética monetária no cliente" está em §2.5, citação errada em 3 lugares); a justificativa do `B_WINDOW=12` alegava âncoras a "100+ linhas" quando a medição dá 1–26 (quem as protege é a exclusão por tipo de arquivo, não a folga); o número "3 divergências" do mirror-check não era reproduzível contra a base da onda; e o comentário do compose atribuía à imagem a causalidade que é da lista à mão. Mais: `override DB_SCHEMAS` (sem ele, `make DB_SCHEMAS="asset_registry" <alvo>` vencia o arquivo e pulava 5 schemas — a sentinela só pegava lista vazia); `repo-gates.yml` com `setup-python` e cabeçalho honesto; e duas linhas do `go-live-runbook.md` que o próprio diff tornou falsas no mesmo commit. **Residuais não-bloqueantes p/ a 33ª:** (1) nada compara os steps de `db.yml` com `db/schema-order.txt` — nem ordem nem CONJUNTO; um schema declarado lá e esquecido no YAML nunca é migrado nem tem RLS exercitada na CI (TX-3); o gate cabe em ~10 linhas e já tem casa em `repo-gates.yml`; (2) `bff/src/routers/stats.ts` devolve `consolidatedUnavailable` com a mensagem crua da rejeição dentro de resposta de SUCESSO, contornando o `errorFormatter` que este mesmo diff criou (hoje inalcançável, mas é a segunda porta da classe que a primeira fechou); (3) `workflow-paths-mirror-check` não enxerga `on.pull_request_target` (nenhum workflow usa hoje); (4) `repo-gates.yml` instala PyYAML sem pin/hash, como `ml.yml`/`data.yml`/`go.yml` já fazem; (5) a ordem entre schemas segue declarada em dois lugares (exceção declarada no cabeçalho de `db/schema-order.txt`). **LIÇÕES novas: (1) corrigir a instância nos runners de TESTE e deixar os provisionadores de DESENVOLVIMENTO enumerando à mão é meia-correção — e a metade que fica para trás é a que monta o banco em que a pessoa acredita. (2) Um gate de superfície ampla dentro de um workflow com `paths:` é um órfão com aparência de gate. (3) Quando o merge é push FF direto, `push.paths` é a última palavra sobre o que é revalidado. (4) Um gate novo herda o modo de falha da FAMÍLIA a que pertence — este nasceu com o mesmo defeito linha-física-vs-linha-lógica que a 30ª já pagara no `no-float`; antes de escrever um gate, releia as lições do gate irmão.** **G0 segue código-completo; próximo movimento real = G1 (cutover de infra, gated por aprovação humana).**

##### G1 — Cutover de infra (platform-infra é dono; APROVAÇÃO HUMANA obrigatória)
- **platform-infra E9:** provisionar contas cloud (incluindo **contas separadas PCI e AML/KYC**), `tofu plan/apply` de network/eks/addons **só sob aprovação humana explícita**, bootstrap Argo CD + OpenBao server, gerar chaves KMS/HSM, preencher FQDNs das células, rodar smoke §5, e **validar comportamentalmente o deny-all do Cilium (L-1)** via netpol-tester/Sonobuoy. Ref: `go-live-runbook §1-§6, §8`, `§2.7`. Gate: checklist dos 4 gates + aprovação humana. **Regra de ouro deste papel: nenhum apply autônomo.**

##### G2 — Injeção de segredos + swap stub→real por addon (paralelo, todos gated por G1)
- **money-ledger E9:** aplicar migrations `0001+0002+0003+0004` do ledger + asset_registry na ordem do runbook, **mais o `REVOKE` de §2.3.1** (least-privilege de `ledger.postings`, ausente das migrations); **USDC scale=6 antes do 1º depósito**; Reconciler→Iceberg real. Ref: `runbook §2.3/§2.3.1/§6`.
- **payments E7:** chaves Stripe/Asaas/Sumsub/Chainalysis via OpenBao; `PII_ENVELOPE_KEY` em KMS/HSM real; FQDNs das células; `smoke-payments.sh` contra infra real. Ref: `runbook §3`, `ADR-0004 §F/§H`.
- **copiloto E10:** `ANTHROPIC_API_KEY` (prompt caching + Batch API), MemorySaver→AsyncPostgresSaver, budget tracker→Redis, embeddings reais (Voyage/Cohere), Langfuse self-hosted, **SDKs C2PA/SynthID reais** (gate de proveniência antes do vigor EU AI Act Art. 50 em 02/08/2026). Ref: `stack §2.4`, `TX-5`, `ADR-0003 §C`.
- **data-platform E8:** cablear I/O real PyIceberg/clickhouse-connect, aplicar 8 migrations ClickHouse, testes de idempotência sob reentrega/out-of-order. Ref: `runbook §2.6`, `DA-7`.
- **frontend E11 + schema E9:** `SESSION_SECRET`/`ALLOWED_ORIGINS` via OpenBao; dry-run de compat do envelope em collectors/ClickHouse/BFF antes do corte. Ref: `runbook §3`, `TX-1`.

##### G3 — GATE DE CUTOVER DA FASE 1 (parity é dono do veredito técnico)
- **parity E11→E12→E13:** rodar `make parity-shadow` (proxy espelhando produção vs. Revive legado vs. Go) e `make parity-dual-run` (Iceberg vs. MySQL do Revive), consolidar em `make parity-cutover-gate`. Critérios **já declarados em código, nunca inventados sob pressão**: `MaxDecisionDivergenceRate ≤ 0.1%`, `MaxBillingDivergencePct ≤ 0.5%`, `MinShadowHours = 48`, `MinShadowDecisions = 100.000`. Dono: `parity-golden-test-guardian`; adjudicação: `tech-lead-architect`. Ref: `ADR-0002 §D`, `runbook §6`. **Silêncio sobre divergência = reprovação, nunca aprovação por omissão.** Nenhum tráfego real antes deste gate verde.

##### G4 — Ativação da Fase 2 sob tráfego real
- **decision-engine E8 + ml E12:** ligar `SHADOW_ENABLED` → medir IPC ranker↔sidecar (**>2 ms p99 sustentado reabre `ADR-0003 §B`**) → `AB_ENABLED` por zona/tenant com guarda de receita + kill-switch; OPE (IPS/SNIPS/DR) sobre propensão real. **Pré-condição: G0 (HOT-1/HOT-3) mesclado.** Donos: `decision-engine-engineer` + `ml-optimization-engineer`; gate `parity` + `money-ledger-guardian`. Ref: `ADR-0003 §G (J3/J4)`, `TX-4`. Este é o passo que gera o tráfego real que habilita K8.

---

#### Sucessores pós-Fase-3 — SÓ sob gatilho mensurável (ADR sucessor com número anexado)
Nenhum justifica trabalho de código hoje. Cada um abre um ADR sucessor **só com o número medido/spec anexado** (`tech-lead-architect` é o dono da abertura).

- **S1 — Deep ranking promovido (K8):** `DEEP_ENABLED` + Triton/GPU **só sob uplift A/B estatisticamente significativo** sobre o GBDT; node pool GPU no EKS provisionado só então. Donos: `ml-optimization-engineer` + `parity-golden-test-guardian`; `platform-infra` provisiona GPU. Ref: `ADR-0004 §A/§H (K8)`, `runbook §7`.
- **S2 — Fireblocks (MPC) sob AUM:** troca de custódia Safe→Fireblocks quando custo anual < ~1% do AUM medido. Dono: `payments-crypto-engineer`. Ref: `ADR-0004 §C`.
- **S3 — TigerBeetle sob gargalo de escrita provado:** só se p99 de commit dos postings no Postgres estourar o orçamento de forma sustentada. Dono: `money-ledger-guardian`. Ref: `ADR-0004 §D`.
- **S4 — Oráculo de preço (Chainlink/Pyth) sob liquidez:** `price_source` administered→oracle quando existir mercado líquido de AEV/BND. Dono: `payments-crypto-engineer`. Ref: `ADR-0004 §E.4`.
- **S5 — Chain não-EVM sob spec:** 2ª encarnação do `ChainConnector` só se a spec de AEV/BND exigir chain própria. Dono: `payments-crypto-engineer`. Ref: `ADR-0004 §E.1`.
- **S6 — Flink sob near-real-time:** reabrir `ADR-0001` só se um dos 3 gatilhos medidos (SLO de frescor com prejuízo material no pacing / re-batch de atribuição longa mais caro que join incremental / IVT tardio com perda de receita) for observado. Donos: `tech-lead-architect` + `data-platform-engineer`. Ref: `ADR-0001` (gatilho de reabertura).
- **S7 — Habilitar AEV/BND (spec de produto):** `UPDATE asset_registry ... enabled=true` só quando a spec oficial definir `scale`/classificação/supply (CHECK `assets_enabled_needs_scale_chk` protege estruturalmente); se BND = cupom, modelar accruals como par de postings periódico (sem UPDATE de saldo). Donos: `money-ledger-guardian` + `payments-crypto-engineer` + `schema-contracts-steward`. Ref: `ADR-0004 §E.2/§E.7`, `runbook §7`.
- **S8 — Sucessores menores sob gatilho:** pCVR após atribuição confiável medida (ml E14, `stack §2.3`); multi-touch attribution sob demanda comercial (schema E11, `ADR-0002 §B.7`); gate de promoção de tier de modelo do copiloto com número de qualidade+custo (copiloto E11); Treelite in-process/PID/Feast-Tecton (ml E15); copiloto embutido nos builders sob dado de uso (frontend E12); assinatura on-chain no front sob spec (frontend E13, `ADR-0004 §E.9`).

---

## 6. Ressalvas de coerência doc↔código

Divergências reais detectadas pelo tech-lead ao validar os planos contra os documentos normativos (a corrigir; nenhuma bloqueia código):

- ~~Incremento **I5** citado como 'ADR-0002 §D (I4, I5)' … 'ADR-0002 (I5)' (plano frontend, E11)~~ **RESOLVIDO na 30ª onda (tech-lead).** Confirmado de 1ª mão: `docs/adr/0002-fase-1-sequenciamento-e-layout.md` §D diz literalmente "A Fase 1 é construída em **5 incrementos**" e "I4 fecha o ciclo" — **não há I5 no ADR**; I5 (loader Postgres→snapshot / wiring local) existe só em `README.md` (linhas 74/85). **Decisão: corrigir a ref, não emendar o ADR** — um ADR é decisão *arquivada*; reescrevê-lo para acomodar o que foi construído depois destrói o registro histórico e é exatamente o antipadrão que o formato existe para evitar. As 4 ocorrências passaram a `ADR-0002 §D (I4)` · `README.md (I5)` (2×) e `README.md (I5)` (2×), separando a fonte normativa (ADR) da fonte de-facto (README). Se algum dia I5 precisar de força normativa, o caminho é um **ADR sucessor**, não uma emenda retroativa.

- ~~docs/documentacao-tecnica.md §5 (CA-1…CA-9): TODOS os checkboxes desmarcados apesar dos goldens verdes~~ **RESOLVIDO na 30ª onda (tech-lead) — e a ressalva original subestimava o problema.** Ao adjudicar, descobriu-se que o §5 não estava apenas *estagnado*: estava parcialmente **inaplicável**. O **CA-9** exige "instala em stack LAMP/LEMP (PHP + MySQL/MariaDB)" e "Plugins residem em `/etc/plugins`" — critérios da plataforma do **Revive legado** que a reescrita Go **nunca satisfará por construção**. Marcá-los como pendentes era tão enganoso quanto marcá-los como feitos.
  **Adjudicação registrada em `docs/documentacao-tecnica.md` §5.0:** (i) o **§5 é canônico** para os `CA-n` — o README descreve incrementos, não assina critérios; (ii) legenda de 4 estados: `[x]` provado por gate executável **citado nominalmente**, `[~]` parcial com a lacuna declarada, `[ ]` sem gate, `N/A-legado` revogado por decisão arquitetural com justificativa e critério sucessor; (iii) **regra inviolável: nada é marcado sem um gate que rode hoje** — subrepresentar é aceitável, superrepresentar não.
  Resultado honesto, não cosmético: CA-2 e CA-5 fecham integralmente contra os goldens; CA-4 fecha 6 de 7 (a injeção `document.write` do custom var não tem gate); **CA-3 permanece o mais fraco — o próprio golden `CA3-002` documenta que imagem sem `dest_url` NÃO é rejeitada**; **CA-7 item 1 é parcial: `make data-billing-test` cobre só CPM (floor, milhar parcial, USDC scale=6) — CPC/CPA/Tenancy não têm teste**; CA-6 tem lacuna de UI (rótulo "≤1h" vs "ao vivo" não asserido no front); **CA-9 mailer/SMTP não existe no código** (`grep` retorna zero). Estas lacunas são agora *visíveis*, que era o ponto.

- ~~make/parity.mk: o comentário do alvo lista 'parity-golden (CA-2, CA-4, CA-5, CA-6…)' OMITINDO CA-3…~~ **RESOLVIDO na 27ª onda (auditoria de falsos-positivos):** o comentário de `parity-golden` e a linha de `parity-status` agora citam CA-3 (`make/parity.mk`); `parity-status` deixou de dizer que CA-3 estava só 'pendente console/BFF' — agora reporta honestamente 'PARCIAL — golden executável (7 casos: mapping/seleção/HMAC); render/VAST/SSRF/upload pendentes'.

- ~~Ref bare '§2.3' no plano do schema-contracts-steward (stage E3 — decision log/propensão/OPE) sem qualificador de documento~~ **RESOLVIDO na 30ª onda (tech-lead).** Confirmado: `docs/documentacao-tecnica.md` tem apenas §2.1/§2.2 — a §2.3 (IA/DL para otimização) vive em `docs/stack-tecnologico.md`. As 2 ocorrências (linhas do stage E3, tabela e bloco de detalhe) foram qualificadas para `stack §2.3`, alinhando com o que o plano de ML já fazia corretamente.

- Nota de coerência código↔doc: **RESOLVIDO na 23ª onda (G0/E10).** web/console estava pinado em Next 15.3.3/React 19.1.0 enquanto stack §2.5 mandata Next 16/React 19.2 — agora em **Next 16.2.10 / React 19.2.7** + alicerce shadcn/ui + a11y-ci (axe + `puppeteer-core`; Playwright evitado por decisão do tech-lead). Zustand e Vercel AI SDK v5 ficaram diferidos com gatilho documentado (não são desvio de conformidade — são decisão de escopo mínimo registrada). Ressalva encerrada.

---

*Documento derivado de fan-out multiagente com validação adversarial de refs de doc. Fonte estruturada: `scratchpad/plan.json` (saída do workflow `plano-dev-por-addon`).*
