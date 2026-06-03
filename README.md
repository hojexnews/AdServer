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
