# ADR-0002 — Layout do monorepo, perguntas abertas e sequenciamento da Fase 1

> **Status:** Aceito · **Data:** 2026-06-03 · **Decisores:** Arquiteto-chefe / Tech Lead
> **Âncoras:** TX-1, TX-2, TX-3, TX-4, TX-5 · DA-3, DA-6, DA-7, DA-10 · CA-1…CA-7 · `docs/stack-tecnologico.md` §2.1, §2.2, §2.6, §4 (roadmap), §6 (q.1, q.2, q.3, q.6, q.7)
> **Supersede:** — · **Substituído por:** —

## Contexto

A Fase 0 (camada de contratos) está completa e verde (`make verify`). Abrimos a
**Fase 1 — MVP de paridade** (`docs/stack-tecnologico.md §4`): motor Go (cascata
DA-3, regras §4.6, capping Redis + fail-safe DA-6), ad tag JS + pixel/redirect/
conversão + VAST 4.x, collector + Redpanda + ClickHouse (`StatsHourly`) + Iceberg,
golden tests + shadow + dual-run, ledger Postgres + Asset Registry + billing
CPM/CPC/CPA/Tenancy, console Next.js + BFF, privacidade (IP descartado, capping
efêmero, redação OTel).

Quatro engenheiros vão atuar **em paralelo** sobre um repositório greenfield que
hoje só tem `proto/`, `contracts/`, `platform/`, `docs/`. Sem um layout
ratificado e sem fronteiras de diretório/módulo, eles colidem em arquivos
estruturais (quem cria o `go.mod`? quem versiona `gen/go`? onde vivem as migrations?).
Este ADR é **pré-requisito do fan-out**: trava o esqueleto antes da primeira linha
de hot path.

Forças em jogo:

- Os `proto/*.proto` **já fixam** `option go_package = "github.com/hojex/adserver/gen/go/..."`.
  Isso **implica** um módulo Go cujo path é `github.com/hojex/adserver` — a decisão
  de módulo único já está, de fato, embutida no contrato da Fase 0.
- `gen/` está no `.gitignore` (código gerado, não versionado) e `buf generate`
  usa **plugins remotos** (`buf.build/protocolbuffers/go`, `buf.build/bufbuild/es`),
  que **exigem rede**. Um build Go offline/hermético precisa do `gen/go` presente.
  Há um conflito real a resolver entre "não versionar gerado" e "build reprodutível".
- Várias perguntas de `stack §6` ainda em aberto bloqueiam decisões de Fase 1
  (BFF, latência, consistência de capping, janelas de atribuição, volume-alvo).
  Em modo autônomo, cada uma recebe um **default recomendado + gatilho de reversão**.

## Decisão

### A. Layout do repositório — monorepo, módulo Go único `github.com/hojex/adserver`

**Adotamos um monorepo com um único módulo Go `github.com/hojex/adserver`** (um
`go.mod` na raiz), com os serviços do hot path como binários sob `services/` e os
pacotes compartilhados sob `internal/`. Multi-módulo é **rejeitado** para a Fase 1
(ver Alternativas).

Árvore de diretórios ratificada para a Fase 1 (apenas o que a Fase 1 cria; o que
já existe está marcado *(existe)*):

```text
.
├── go.mod                      # módulo único github.com/hojex/adserver (raiz)
├── go.sum
├── Makefile                    # (existe) — Fase 1 ESTENDE com alvos go-* / db-* / data-*
├── buf.gen.yaml  ── NÃO        # permanece em proto/ (existe); ver política de gen abaixo
├── proto/                      # (existe) contrato de eventos — fonte da verdade (TX-1)
├── contracts/                  # (existe) contratos cross-cutting (Money, propensão, no-float)
├── platform/                   # (existe) plataforma-base como código (§2.7)
├── docs/                       # (existe) normativos + adr/
│
├── gen/                        # CÓDIGO GERADO pelo buf — VERSIONADO a partir da Fase 1 (ver §C)
│   ├── go/                     # consumido pelo módulo Go (import github.com/hojex/adserver/gen/go/...)
│   └── ts/                     # consumido pelo console/BFF
│
├── services/                   # binários (main packages) — um subdir por serviço
│   ├── decision/               # motor de decisão (hot path) — cascata DA-3, regras §4.6, capping
│   │   └── cmd/decision/main.go
│   ├── collector/              # endpoints lg/ck/ct + asyncjs — emite Decision/telemetria p/ Redpanda
│   │   └── cmd/collector/main.go
│   └── adtag/                  # geração do snippet JS (asyncjs) + pixel/redirect/VAST — assets servidos
│       └── cmd/adtag/main.go   # (pode iniciar dentro de collector; separado quando justificar)
│
├── internal/                   # pacotes Go compartilhados (não exportáveis fora do módulo)
│   ├── cascade/                # avaliação Override>Contract>Remnant (DA-3) — autoridade final
│   ├── rules/                  # motor de regras §4.6 (AND/OR, vetores, anti-contradição)
│   ├── capping/                # cliente Redis + fail-safe DA-6 (chave efêmera, hash+salt+TTL)
│   ├── snapshot/               # snapshot versionado de config (pull do Postgres, refresh periódico)
│   ├── money/                  # helpers Money(asset,int,scale) sobre gen/go/.../money/v1 (TX-2)
│   ├── geo/                    # MaxMind GeoLite2 em memória (DA-9) — IP descartado após derivar
│   └── telemetry/              # produtor fire-and-forget + WAL local + dedupe por event_id
│
├── db/                         # SQL/migrations — FONTE DA VERDADE do schema relacional
│   ├── config/                 # schema de config (advertiser/campaign/banner/zone/rule/cap) — lido pelo snapshot
│   │   └── migrations/
│   └── ledger/                 # double-entry (accounts/journal_entries/postings) + billing (TX-2, §2.6)
│       └── migrations/
│
├── data/                       # pipeline de dados (não-Go ou Go-batch)
│   ├── clickhouse/             # DDL: Kafka engine + AggregatingMergeTree (StatsHourly + "ao vivo")
│   ├── iceberg/                # specs de tabela (verdade contábil/treino) + jobs de billing batch
│   └── redpanda/               # tópicos, particionamento por hash(event_id/zone_id), retenção
│
├── web/
│   └── console/                # Next.js 16 + React 19 (App Router) — front self-service
│
└── bff/                        # Backend-for-Frontend (fronteira de ACL server-side, CA-1)
    └── (Node/TS — ver decisão B.6)
```

**Princípios de fronteira embutidos no layout:**

- `proto/` é a **única** fonte de schema de eventos; `gen/` é **derivado** dele e
  ninguém edita à mão. `db/` é a fonte do schema **relacional** (config + ledger);
  `data/` é a fonte do schema **analítico** (ClickHouse/Iceberg). Essas três fontes
  não se sobrepõem.
- O **ledger** (`db/ledger/`) e o **billing batch** (`data/iceberg/`) são do
  `money-ledger-guardian`; o **billing reconcilia contra Iceberg, nunca contra o
  streaming** (invariante). O ClickHouse "ao vivo" é não-faturável e a UI nunca soma
  (ADR-0001).
- Capping (`internal/capping/`) é **best-effort + fail-safe DA-6** e **não é** fonte
  de verdade contábil — pode sub/sobre-entregar dentro da tolerância (ver B.3).

### B. Resolução das perguntas abertas bloqueantes (stack §6)

#### B.6 — BFF é **Node/TS** (habilita tRPC v11)

Adotamos **BFF em Node/TS** com **tRPC v11**, schemas **Zod** como fonte única,
servindo o console Next.js. Motivo-âncora: §2.5 já define o front em TS strict com
Zod/TanStack Query e cita tRPC "se o BFF for Node/TS"; manter o contorno
front↔BFF em uma única linguagem (TS de ponta a ponta, tipos derivados sem
codegen cruzado) reduz dívida operacional na Fase 1 e mantém a coerência poliglota
(Go no hot path, TS no front/BFF). O BFF **não** entra no hot path de decisão: é a
fronteira de ACL/agregação para o painel, que consome telemetria já agregada.
- **Gatilho de reversão:** se o BFF precisar de uma rota **quente** (p99 sensível)
  que justifique reescrita em Go, OU se surgir um **segundo consumidor não-TS** do
  mesmo contrato (mobile nativo, integração de parceiro), migra-se o contorno para
  **OpenAPI + cliente gerado** poliglota. Até lá, tRPC.

#### B.2 — Orçamento de latência da decisão

Declaramos o **budget total ponta-a-ponta da decisão** (do request à resposta JSON
do criativo, medido na borda, excluindo rede do cliente):

| Métrica | Budget total (E2E na borda) | Hot path puro Fase 1 (sem ML) | Reserva ML (Fase 2, TX-4) |
|---|---|---|---|
| p50 | ≤ 15 ms | ≤ 8 ms | (n/a na Fase 1) |
| p99 | ≤ 40 ms | ≤ 25 ms | 5–8 ms dentro do p99 |
| p99.9 | ≤ 80 ms | ≤ 60 ms | timeout duro + fail-open p/ cascata pura |

Na Fase 1 **não há ML síncrono**: a decisão é cascata + regras + capping **em
memória** (snapshot versionado, avaliação O(1), sem ida à rede no hot path exceto
o GET de capping no Redis). A reserva de **5–8 ms p99 para ML** (TX-4) é declarada
**agora** para que o design da Fase 1 já caiba o ML da Fase 2 sem reabrir o budget,
sempre com **timeout duro + fail-open determinístico** para a cascata pura.
- **Gatilho de reversão:** se o hot path puro medido (golden/shadow) estourar
  **p99 > 25 ms** de forma sustentada com a carga-premissa (B.1) em Go, abre-se ADR
  para avaliar o escape hatch Rust+Axum **em componente de cauda específico** (§2.1)
  — nunca reescrita global por aspiração.

#### B.3 — Consistência de capping: **eventual (best-effort) + fail-safe**

Adotamos **capping eventualmente consistente (best-effort)** em Redis com TTL,
mais o **fail-safe DA-6**: campanha capeada **sem identificador estável** tem a
entrega **abortada** (silêncio preferido a estouro de contabilidade). Tolerância de
**sub/sobre-entrega declarada como aceitável** (padrão adtech, §2.1). Capping
**não** é o sistema de verdade do faturamento — o faturável vem do batch sobre
Iceberg (DA-7), reconciliado, não do contador de capping. Não há garantia
transacional estrita de "no máximo N exibições globais" através de réplicas.
- **Gatilho de reversão:** se um contrato comercial impuser **garantia de não-estouro
  estrita** (cláusula contratual de sobre-entrega com penalidade), abre-se ADR para
  capping forte por chave (incremento atômico com quórum / lease) **apenas para os
  caps daquele tenant**, medindo o custo de latência adicionado ao hot path antes de
  adotar.

#### B.7 — Janelas de atribuição: **last-click, lookback 7 dias** (Fase 1)

Adotamos **atribuição last-click** com **janela de lookback click→conversion de 7
dias** para o billing CPA da Fase 1. O `StatsHourly` agrega por `hour_bucket` e o
join click→conversion é feito no **batch sobre Iceberg** (verdade faturável), com a
visão "ao vivo" do ClickHouse rotulada e nunca somada (ADR-0001). Multi-touch fica
**fora** da Fase 1 (não há ML/atribuição probabilística ainda). O schema da Fase 1
**carrega `decision_id` + `occurred_at`** (já no contrato TX-1), o que torna a
janela um **parâmetro do job de billing**, não um fato gravado no schema — trocar a
janela ou o modelo no futuro **não** exige migração de schema.
- **Gatilho de reversão:** quando a Fase 2 fechar o loop de atribuição com propensão
  (pCVR) e houver demanda comercial por crédito multi-touch, abre-se ADR para
  multi-touch/position-based, reusando o mesmo `decision_id`/`occurred_at` sem
  migração. Ajustar de 7 dias é mudança de parâmetro do job, não exige ADR.

#### B.1 — Volume-alvo: **PREMISSA de dimensionamento (revisável)**

Não temos número do cliente. Registramos uma **premissa explícita e revisável** de
dimensionamento da Fase 1, suficiente para justificar **Go + Postgres + Redis + 1
bus (Redpanda) + ClickHouse** sem escalar para Rust/Aerospike/Flink/TigerBeetle:

| Dimensão | Premissa Fase 1 (revisável) |
|---|---|
| Pico de ad requests | **≤ 5.000 req/s** agregado (todos os tenants) |
| Tenants ativos | **≤ 200** |
| Campanhas/regras ativas no snapshot | **≤ 50.000** campanhas, **≤ 200.000** regras §4.6 |
| Estado de capping (Redis) | **≤ 50 GB** (cabe em Redis Cluster com TTL; longe de TB→Aerospike) |
| Eventos/dia | **≤ 1 bilhão** (cabe no padrão Redpanda→ClickHouse 1-bus) |

Nesta faixa, Go com snapshot in-process (avaliação O(1)) e Redis para capping
atendem com folga; **nenhum** gatilho de escala pesada (Rust, Aerospike, Flink,
TigerBeetle) é acionado. **Isto é premissa, não requisito do cliente.**
- **Gatilho de reversão (por tecnologia):** Rust → p99 hot path estourar (ver B.2);
  Aerospike → estado de capping > **1 TB**; Flink → gatilho do ADR-0001; TigerBeetle
  → gargalo de **escrita** financeira provado no Postgres (§2.6). Cada um exige o
  **número medido** anexado a um ADR sucessor antes de aprovar.

### C. Política de código gerado (`gen/`) — versionado a partir da Fase 1

**Mudamos a política da Fase 0:** a partir da Fase 1, `gen/go/` e `gen/ts/` passam
a ser **versionados** (committed) e o `.gitignore` é ajustado para **deixar de
ignorá-los**. Razão: `buf generate` usa **plugins remotos** que exigem **rede**, e
o build Go do hot path precisa ser **hermético e reprodutível** (CI, dev offline,
ambientes de cutover). Versionar o gerado:

- torna o build Go independente de rede e de disponibilidade do BSR;
- mantém o diff do contrato **visível** no PR que muda o `.proto` (quem mexe no
  contrato regenera e commita junto — o `schema-contracts-steward` é o dono);
- a CI **valida que o gerado está em dia** rodando `buf generate` e falhando se o
  `git diff` ficar sujo (drift entre `.proto` e `gen/`).

`gen/` continua sendo **derivado** — ninguém edita à mão; só o `schema-contracts-steward`
regenera via `make proto-gen`. O `buf.gen.yaml` permanece em `proto/` e gera para
`../gen/` (path relativo à raiz já é o efeito atual, pois `out: gen/go` resolve a
partir de `proto/`; o ADR ajusta para a raiz — ver despacho).

### D. Sequenciamento da Fase 1 em incrementos

A Fase 1 é construída em **5 incrementos** com ordem de build e dependências
explícitas. Cada incremento lista os `CA-n` que fecha (verde só após validação do
`parity-golden-test-guardian`). Incrementos **I0 e I1 rodam em paralelo** (sem
dependência mútua); I2 depende de I0+I1; I3 depende de I2; I4 fecha o ciclo.

| Inc | Nome | Depende de | Entrega | Fecha (CA-n) |
|---|---|---|---|---|
| **I0** | Esqueleto Go + cascata em memória | — | `go.mod` raiz; `gen/go` versionado; `internal/cascade` (DA-3), `internal/rules` (§4.6), `internal/snapshot`, `internal/geo`; `services/decision`. Decisão pura, sem capping/telemetria ainda. | CA-2, CA-4 (cascata + regras; anti-contradição na avaliação) |
| **I1** | Ledger + Asset Registry + schema de config | — | `db/ledger/migrations` (double-entry, NUMERIC por ativo, idempotency key); `db/config/migrations` (advertiser/campaign/banner/zone/rule/cap); Asset Registry como tabela seeded. | CA-7 (parcial: tipos NUMERIC, sem float; multi-moeda como rótulo) |
| **I2** | Capping + ad tag + telemetria + collector | I0, I1 | `internal/capping` (Redis + fail-safe DA-6, chave efêmera hash+salt+TTL); `internal/telemetry` (WAL + dedupe `event_id`); `services/collector` (lg/ck/ct + asyncjs, pixel/redirect 302/VAST 4.x); IP descartado pós-geo. | CA-3, CA-5, CA-6 (parcial: medição por pixel/redirect), CA-8 |
| **I3** | Pipeline de dados (Redpanda→ClickHouse→Iceberg) | I2 | `data/redpanda` (tópicos, hash partition); `data/clickhouse` (Kafka engine + MV `StatsHourly` + "ao vivo"); `data/iceberg` (verdade contábil + job billing batch horário, last-click 7d). | CA-6 (consolidação horária ≤1h; Request−Impression), CA-7 (billing CPM/CPC/CPA/Tenancy reconciliado em Iceberg) |
| **I4** | Console + BFF + golden/shadow/dual-run | I3 | `web/console` (Next.js: CRUD, vínculo N:N, dashboards ≤1h vs ao vivo, ACL); `bff` (tRPC, fronteira ACL CA-1); harness de golden tests + shadow-traffic + dual-run contábil. | CA-1 (ACL/isolamento), e **gate de cutover** (todos os CA-n verdes via dual-run) |

**Regras de sequenciamento:**

- **Nada de cutover** antes de I4 com golden + shadow + dual-run dentro da
  tolerância (§5 do stack; risco "reescrita Go divergir"). O
  `parity-golden-test-guardian` é o dono da verificação e do gate.
- **I0/I1 em paralelo** porque não compartilham arquivos (decision/internal vs.
  db/). I2 é o ponto de junção: consome o `gen/go` (telemetria/decision) e o schema
  de config de I1.
- O **billing reconcilia contra Iceberg** (I3), nunca contra o streaming "ao vivo"
  (ClickHouse) — invariante materializada no incremento.

### Fora do escopo deste ADR

Não decide identidade cookieless (q.5) além do fail-safe DA-6 já normativo; não
decide specs de AEV/BND (q.8, Fase 3); não decide a estrutura interna do ML
(Fase 2); não escreve hot path nem ledger linha a linha (delegado aos engenheiros).

## Gatilho de reabertura

Cada decisão B.* carrega seu **próprio gatilho de reversão mensurável** acima.
Globalmente, este ADR é reaberto se a estrutura de módulo único causar **tempo de
build/CI inaceitável** (alvo de referência: build Go completo > 5 min de forma
sustentada) — sintoma que justificaria split multi-módulo — com o número medido
anexado ao ADR sucessor.

## Alternativas consideradas

- **Multi-módulo Go (um `go.mod` por serviço).** Rejeitada para a Fase 1:
  contradiz o `go_package` já fixado nos `.proto` (`github.com/hojex/adserver/gen/go`),
  multiplica `go.mod`/`go.sum` e `replace` locais, e adiciona atrito de versionamento
  interno entre `internal/cascade`, `internal/money` etc. sem ganho — o monólito de
  build é trivial nesta faixa de volume (B.1). Fica como upgrade sob o gatilho de
  reabertura (tempo de build).
- **`gen/` permanece ignorado (gerado só em CI/local).** Rejeitada: torna o build Go
  dependente de rede/BSR (plugins remotos), quebra dev offline e cutover hermético,
  e esconde o diff do contrato no PR. Custo de versionar o gerado (ruído no diff) é
  menor que o custo de build não-reprodutível em hot path financeiro.
- **BFF poliglota (OpenAPI/codegen) já na Fase 1.** Rejeitada por ora: adiciona um
  passo de codegen e divergência de tipos sem um segundo consumidor não-TS para
  justificá-lo; tRPC é mais enxuto enquanto o único consumidor é o console Next.js.
- **Capping fortemente consistente no v1.** Rejeitada: §2.1/DA-6 declaram
  best-effort + fail-safe e tolerância de sub/sobre-entrega como padrão adtech;
  consistência forte custa latência no hot path sem requisito contratual.

## Consequências

- **Positivas:**
  - **Desbloqueia o fan-out paralelo** de 4 engenheiros com fronteiras de diretório
    explícitas e zero colisão estrutural (quem cria `go.mod`, quem toca `gen/`, quem
    cria `db/`/`data/` está ratificado abaixo).
  - Build Go **hermético e reprodutível** (gen versionado) — crítico para um hot
    path que toca faturamento e para o cutover/dual-run.
  - Cada pergunta de §6 sai do limbo com **default + gatilho mensurável** — sem
    decisão por inércia, e sem travar a Fase 1 esperando número do cliente.
- **Negativas / custos aceitos:**
  - Versionar `gen/` gera **ruído de diff** quando o `.proto` muda (mitigado: dono
    único regenera; CI detecta drift).
  - Módulo único acopla o ciclo de build dos serviços (mitigado: faixa de volume
    pequena; gatilho de split documentado).
  - Premissa de volume (B.1) pode estar **errada** quando o cliente trouxer números;
    aceito explicitamente como premissa revisável com gatilhos por tecnologia.
- **Impacto por fase do roadmap:**
  - **Fase 1:** layout e fronteiras valem agora; `services/`, `internal/`, `db/`,
    `data/`, `web/console/`, `bff/`, `gen/` versionado.
  - **Fase 2:** ML entra como `internal/` + sidecar dentro do budget B.2 (reserva
    5–8 ms já declarada); copiloto/BFF reusam o contorno TS.
  - **Fase 3:** cripto/pagamentos entram fora do hot path; `db/ledger/` já comporta
    AEV/BND como linhas do Asset Registry (sem migração de schema).
