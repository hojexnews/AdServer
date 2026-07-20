# Runbook de Cutover de Go-Live — AdServer Hojex

**Fase:** 3 (hardening go-live)
**Audiencia:** SRE / plataforma + revisor de seguranca + guardiao de ledger
**Data de criacao:** 2026-06-04
**Status:** Documentacao operacional pre-go-live — sem segredos, sem infra viva.

---

## 1. Pre-requisitos gerais

Antes de executar qualquer etapa deste runbook:

- `make verify` VERDE (buf TX-1 + no-float TX-2).
- `make platform-validate` VERDE (tofu + kubeconform + kyverno test + otel-validate TX-5).
- Repositorio sincronizado com `main`; nenhum PR pendente afetando schema, ledger ou celulas.
- Credenciais de acesso ao banco de producao disponIveis via OpenBao (nao via .env local).
- Conta cloud separada para celula PCI provisionada e revisada pelo QSA.
- Conta cloud separada (ou namespace isolado com Cilium deny-all) para celula AML/KYC.

---

## 2. Ordem de aplicacao das migracoes de banco

Aplicar na ordem abaixo por schema. Cada schema e independente no Postgres (schemas
separados, nao bancos separados — exceto o schema `compliance` que roda em instancia
propria na celula AML/KYC). Os comandos assumem `PGURL` apontando para o banco alvo.

### 2.1 Schema `asset_registry`

```bash
psql "$PGURL" -f db/asset_registry/migrations/0001_asset_registry_up.sql
```

Sem dependencias de outros schemas. Aplica primeiro — o ledger e o BFF dependem do
`asset_registry.assets` (scale por ativo, TX-2/DA-10).

### 2.2 Schema `config`

```bash
psql "$PGURL" -f db/config/migrations/0001_config_schema_up.sql
psql "$PGURL" -f db/config/migrations/0002_config_rls_up.sql
psql "$PGURL" -f db/config/migrations/0003_campaign_zones_rls_up.sql
psql "$PGURL" -f db/config/migrations/0004_campaign_zones_with_check_up.sql
```

Aplicar em ordem (0001 -> 0002 -> 0003 -> 0004). As migracoes de RLS dependem do schema
criado pela 0001.

**Atencao (0004):** a `0003` cria `campaign_zones_tenant_isolation` como policy `FOR ALL`
**USING-only** — o isolamento de ESCRITA existe apenas por fallback implicito do Postgres
(`pg_policy.polwithcheck` fica NULL). A `0004` grava o `WITH CHECK` explicito no catalogo.
Omiti-la faz `db/config/tests/rls_isolation_test.sql` falhar no BLOCO 5.5 (introspecao
default-deny de `pg_policy`) — verificado de 1a mao contra Postgres 16 nativo:
sem a 0004 o teste aborta com `ASSERT FALHOU [WITH CHECK ausente no catalogo pg_policy]`;
com a 0004 imprime `== RLS ISOLATION: ALL TESTS PASSED ==`.

### 2.3 Schema `ledger`

```bash
psql "$PGURL" -f db/ledger/migrations/0001_ledger_schema_up.sql
psql "$PGURL" -f db/ledger/migrations/0002_reconciliation_exceptions_up.sql
psql "$PGURL" -f db/ledger/migrations/0003_ledger_rls_up.sql
psql "$PGURL" -f db/ledger/migrations/0004_ledger_postings_immutable_up.sql
```

**Atencao:** `deploy/local/postgres/10-init.sh` (hook do Docker local) aplica APENAS a
`0001_ledger_schema_up.sql`. As migracoes `0002` (reconciliation_exceptions), `0003`
(RLS por tenant) e `0004` (imutabilidade append-only) **precisam ser aplicadas
explicitamente** em producao. Omiti-las resulta em falha do smoke-payments (trilho c + d),
ausencia de RLS no ledger e — no caso da `0004` — um ledger em que `UPDATE`/`DELETE` de
postings ja lancados sao ACEITOS pelo banco.

#### 2.3.1 Least-privilege de `ledger.postings` (passo manual — NAO esta na migration)

A `0004` instala os triggers `postings_immutable_trg` e `journal_entries_immutable_trg`
(garantia **primaria**, vale ate para superusuario e para acesso direto a uma particao).
A metade **least-privilege** do controle **nao esta dentro de nenhum arquivo de migration**
— hoje ela so existe em `make/db.mk` (Postgres efemero de `db-test-all`) e no SQL inline
de `.github/workflows/db.yml`. Em producao ela precisa ser executada a mao, **depois** dos
GRANTs de schema:

```bash
psql "$PGURL" -v ON_ERROR_STOP=1 \
  -c "REVOKE UPDATE, DELETE ON ledger.postings FROM adserver_app;"
```

Mudanca de comportamento ZERO para a aplicacao (`internal/ledger/posting.go` so faz
INSERT/SELECT em postings). Correcoes contabeis sao **sempre** um novo par invertido via
`RecordReversal`, nunca edicao do posting original.

**Verificacao de 1a mao (Postgres 16 nativo, executada nesta revisao do runbook):**
aplicar apenas `0001+0002+0003` — isto e, o que este runbook mandava ate agora — e rodar
`psql "$PGURL" -f db/ledger/tests/postings_immutability_test.sql` **falha** com
`ASSERT FALHOU [(b) UPDATE posting lancado]: banco ACEITOU — trigger de imutabilidade
ausente ou ineficaz`. Com `0004` + o REVOKE acima, o mesmo teste imprime
`== LEDGER POSTINGS IMMUTABILITY: ALL TESTS PASSED ==`.

### 2.4 Schema `vector`

```bash
psql "$PGURL" -f db/vector/migrations/0001_vector_schema_up.sql
psql "$PGURL" -f db/vector/migrations/0002_vector_rls_up.sql
```

Aplicar em ordem (0001 -> 0002).

### 2.5 Schema `compliance` (instancia separada — celula AML/KYC)

```bash
psql "$PGURL_COMPLIANCE" -f db/compliance/migrations/0001_compliance_schema_up.sql
```

Este schema roda em instancia Postgres **separada** dentro da celula AML/KYC (ou RDS
em conta cloud isolada). Nunca aplicar na mesma instancia dos demais schemas — a
separacao de instancia e parte do isolamento de PII/KYC (ADR-0004 §F / DA-11).

A camada de aplicacao ja esta pronta: cifra KMS-envelope da PII (`v1$` versionado) em
`services/payments/internal/kmsenvelope/kmsenvelope.go`. O que falta e a chave real (ver §3).

### 2.6 ClickHouse (schema de analytics)

```bash
# Aplicar em ordem — cada migracao depende da anterior.
for n in 001 002 003 004 005 006 007 008; do
  clickhouse-client --multiquery < data/clickhouse/migrations/${n}_*.sql
done
```

Nao ha downgrade automatico de ClickHouse — migrations sao append-only. Rollback
requer recriacao de tabelas (ver §9).

---

## 3. Segredos a injetar via OpenBao/KMS reais

**Regra absoluta:** nenhum segredo entra em imagem Docker, variavel de ambiente estatica,
arquivo git ou ConfigMap em texto plano. Tudo via OpenBao com Pod Identity (IRSA/OIDC).

### 3.1 Chave do envelope de PII (celula AML/KYC)

| Segredo | Path OpenBao | Observacao |
|---------|-------------|------------|
| `PII_ENVELOPE_KEY` | `secret/aml-kyc/pii-envelope-key` | Chave AES-256 para envelope KMS sobre dados KYC/Travel Rule. Deve ser gerada em KMS/HSM real (AWS KMS ou equivalente). A camada de codigo ja usa cifra versionada `v1$` em `services/payments/internal/kmsenvelope/kmsenvelope.go`. O que falta e substituir o stub local pela chave real. |

Rotacao: a chave e versionada (prefixo `v1$`); rotacionar requer re-cifrar os registros
existentes com a nova chave antes de desabilitar a antiga — procedimento separado de
key rotation nao coberto neste runbook.

### 3.2 Credenciais de pagamento — celula PCI

| Segredo | Path OpenBao | Observacao |
|---------|-------------|------------|
| `STRIPE_SECRET_KEY` | `secret/pci/stripe-secret-key` | Chave secreta Stripe (sk_live_...). Nunca fora da celula PCI. |
| `STRIPE_WEBHOOK_SECRET` | `secret/pci/stripe-webhook-secret` | Segredo de validacao de webhook Stripe. |
| `ASAAS_API_KEY` | `secret/pci/asaas-api-key` | API key Asaas (PIX/boleto). |
| `MERCADOPAGO_ACCESS_TOKEN` | `secret/pci/mercadopago-access-token` | Token de acesso Mercado Pago (se habilitado). |
| DSN Postgres PCI | `secret/pci/postgres-dsn` | DSN da instancia Postgres da celula PCI (schema `ledger` escopo PCI). Credenciais dinamicas via OpenBao database engine. |

### 3.3 Credenciais de custodia cripto — celula AML/KYC

| Segredo | Path OpenBao | Observacao |
|---------|-------------|------------|
| `SAFE_RPC_URL` | `secret/aml-kyc/safe-rpc-url` | URL do RPC Ethereum/L2 para o Safe multisig. Placeholder ate definicao de rede (ADR-0004 §B). |
| `FIREBLOCKS_API_KEY` | `secret/aml-kyc/fireblocks-api-key` | Fireblocks API key. Ativar apenas quando AUM justificar (ADR-0004 §C: Safe multisig primeiro). |
| `FIREBLOCKS_API_SECRET` | `secret/aml-kyc/fireblocks-api-secret` | Fireblocks API secret (RSA privado). Gerenciar em HSM. |
| `SUMSUB_API_KEY` | `secret/aml-kyc/sumsub-api-key` | API key Sumsub (KYC/KYB). |
| `SUMSUB_SECRET_KEY` | `secret/aml-kyc/sumsub-secret-key` | Segredo HMAC Sumsub para validacao de webhook. |
| `CHAINALYSIS_API_KEY` | `secret/aml-kyc/chainalysis-api-key` | API key Chainalysis (screening on-chain). |
| DSN Postgres compliance | `secret/aml-kyc/postgres-compliance-dsn` | DSN da instancia separada do schema `compliance`. Credenciais dinamicas. |

### 3.4 DSNs por celula (todas as celulas)

Credenciais de banco devem ser **dinamicas** (OpenBao database engine, TTL curto),
nunca estaticas. Cada celula tem seu proprio role Postgres com privilegios minimos:

| Celula | Role Postgres | Permissoes |
|--------|--------------|-----------|
| delivery | `adserver_app` | SELECT/INSERT/UPDATE em schemas config, asset_registry (via RLS) |
| ml | `adserver_ml` | SELECT em asset_registry; INSERT em vector (RLS) |
| pci | `adserver_pci` | SELECT/INSERT em `ledger.postings` (**nunca UPDATE/DELETE** — append-only, §2.3.1); SELECT/INSERT/UPDATE nas demais tabelas de ledger (RLS); BYPASSRLS para `adserver_loader` |
| aml-kyc | `adserver_compliance` | SELECT/INSERT/UPDATE em compliance (RLS); sem acesso a ledger de outros tenants |

---

## 4. FQDNs reais das celulas (placeholders a preencher)

| Componente | FQDN / Placeholder | Observacao |
|------------|-------------------|-----------|
| Celula PCI — ingress webhook Stripe | `PLACEHOLDER_PCI_WEBHOOK_FQDN` | Ex.: `payments.hojex.io`. Preencher antes de ativar a Cilium policy `allow-ingress-stripe-webhook.yaml`. |
| Celula AML/KYC — ingress webhook Sumsub | `PLACEHOLDER_AML_KYC_WEBHOOK_FQDN` | Ex.: `kyc-webhook.hojex.io`. Preencher em `platform/cells/aml-kyc/gateway/httproute-sumsub-webhook.yaml`. |
| Egress Stripe API | `api.stripe.com` | Ja presente em `allow-egress-stripe.yaml` como FQDN literal. Verificar zona DNS no cluster. |
| Egress Chainalysis API | `PLACEHOLDER_CHAINALYSIS_FQDN` | Preencher em `platform/cells/aml-kyc/netpol/allow-egress-chainalysis.yaml` com o FQDN real da API Chainalysis. |
| Egress Travel Rule | `PLACEHOLDER_TRAVEL_RULE_FQDN` | Preencher em `allow-egress-travel-rule.yaml`. FQDN depende do provedor adotado (Notabene, Sygna, etc.). |
| Egress Safe RPC | `PLACEHOLDER_SAFE_RPC_FQDN` | FQDN do nodo RPC (Infura, Alchemy, self-hosted). |
| OpenBao | `openbao.platform-system.svc.cluster.local` | Interno ao cluster. Ja presente nas policies de egress das celulas. |

---

## 5. Sequencia de smoke pre-cutover

Executar nesta ordem antes de direcionar trafego real:

### Passo 1 — Smoke de banco (RLS dos 4 schemas)

```bash
make db-test-all
```

Valida isolamento por tenant (RLS) nos schemas config, ledger, vector e compliance.
Inclui os testes de ledger que cobrem 0002 (reconciliation), 0003 (RLS) e 0004
(`db/ledger/tests/postings_immutability_test.sql`, append-only).

**Lacuna FECHADA na mesma 30a onda (nao reabra a mao):** por algumas horas o alvo
`db-test-all` (`make/db.mk`) enumerava as migrations de `config` a mao e parava na `0003`,
enquanto `db/config/tests/rls_isolation_test.sql` (BLOCO 5.5) ja exigia a
`0004_campaign_zones_with_check_up.sql` — `make db-test-all` falhava por lista
desatualizada, nao por regressao de RLS. A correcao **eliminou a forma do defeito**, nao a
instancia: `make/db.mk` e `.github/workflows/db.yml` agora derivam a lista do proprio
diretorio (`ls db/<schema>/migrations/*_up.sql | sort`, e `sort -r` no rollback), com
sentinela que FALHA se um diretorio de migrations vier vazio. Uma `0005_*_{up,down}.sql`
nova e aplicada automaticamente; nenhum runner precisa ser editado. A **ordem entre
schemas** (asset_registry → config → … → compliance) continua explicita, porque e
dependencia real. Requisito que permanece: prefixo numerico de 4 digitos zero-padded, que
e o que faz a ordem lexicografica coincidir com a numerica.

### Passo 2 — Smoke do trilho de pagamentos

```bash
PGURL="postgres://adserver_loader:<senha>@<host>:5432/adserver?sslmode=require" \
  bash deploy/local/smoke-payments.sh
```

Exercita os 4 invariantes do trilho (a)(b)(c)(d):
- (a) Par de postings idempotente (sem duplicacao).
- (b) Deposito pending -> finalidade (Safe webhook stub).
- (c) Reconciliacao abre excecao, nunca autocorrige.
- (d) Status via BFF: string DECIMAL sem float (TX-2), isolamento RLS (IDOR).

Pre-requisito: migracoes 0001+0002+0003+0004 do ledger aplicadas, o REVOKE de §2.3.1
executado, e roles dev criados.

### Passo 3 — Gates de contratos e no-float

```bash
make verify
```

buf TX-1 (contratos Protobuf) + no-float TX-2 (proibicao de float em codigo financeiro).

### Passo 4 — Testes Go

```bash
make go-build && make go-vet && make go-test
```

Todos os unit/integration tests do monorepo Go. Os alvos `make` filtram `node_modules/`
(algumas deps npm vendoram arquivos `.go` benignos que quebrariam `go test ./...` cru
numa maquina com `npm install` ja executado — ver `make/go.mk`).

### Passo 5 — Suites BFF/pytest

```bash
# BFF (Node/TypeScript) — typecheck + lint + suite Jest
make bff-ci

# ML — OPE, features, training, calibration, promote (make ml-test)
#      + pacing, fraud supervisionado e fraud nao-supervisionado K2 (make ml-batch-test)
#      + DDL/IVT de ClickHouse (make data-validate)
make ml-test && make ml-batch-test && make data-validate

# Deep ranker K1 (TwoTowerDCNv2 + paridade ONNX) — alvo dedicado, ~43 s.
# Obrigatorio antes de ativar DEEP_ENABLED / promover qualquer versao "deep-*".
# Fora do agregado ml-test para nao inflar o gate rapido de CI.
make ml-deep-test

# Copiloto — alvo make canonico (suite gateway/model-router/schemas/security/HITL)
make copilot-test
```

Os alvos `make` sao obrigatorios: os de Node filtram `node_modules/` (mesmo motivo do
Passo 4) e os de Python injetam `PYTHONPATH=.` na raiz do repositorio — sem isso,
`cd ml && pytest` falha com `ModuleNotFoundError: No module named 'ml'`.

`make ml-batch-test` agora inclui `ml-batch-test-unsup` (K2: Isolation Forest + Autoencoder
nao-supervisionado). `make ml-deep-test` cobre o K1 (TwoTowerDCNv2); deep permanece
default-off — DEEP_ENABLED=false ate uplift A/B provado (K8 gate, ADR-0004).

#### Nota sobre contagens de teste

Versoes anteriores deste runbook fixavam contagens ("73 testes", "22 testes") como se
fossem sentinelas de encolhimento de suite. **Nao eram.** Nao existe, em nenhum alvo
`make`, script de CI ou workflow, qualquer assercao sobre numero de testes
(`grep -rnE 'numPassedTests|testsuites|baseline.*count' make/ .github/workflows/ scripts/ci/`
retorna vazio). Um numero em prosa nao detecta suite encolhida — ele apenas envelhece e
passa a mentir, exatamente como o `//nolint:<linter>` sem linter configurado.

O que vale como gate e o **exit code** do alvo `make`. Para deteccao de regressao por
remocao de teste, o criterio verificavel e a **presenca nominal** dos testes-guarda de
seguranca/dinheiro criados nas ondas 27a–29a — se algum destes arquivos ou casos sumir,
a suite regrediu ainda que o alvo fique verde:

- `bff/src/routers/copilot.test.ts` — HITL obrigatorio + IDOR do copiloto.
- `bff/src/lib/trpc.test.ts` — ACL `tenantProcedure` server-side (CA-1).
- `db/ledger/tests/postings_immutability_test.sql` — append-only (§2.3.1).
- `db/config/tests/rls_isolation_test.sql` BLOCO 5.5 — `WITH CHECK` por introspecao.
- `tests/parity/ca{2,3,4,5,6}_*_golden_test.go` — os 5 goldens de paridade.

**Este runbook nao registra contagens.** Nao ha numero de teste escrito aqui, de
proposito: um numero em prosa nasce correto e apodrece no commit seguinte — a
revisao de 2026-07-19 provou isso ao reintroduzir contagens que ja estavam erradas
ao serem medidas de 1a mao no mesmo dia. Se voce precisa da contagem de hoje, ela
tem uma fonte-unica executavel; derive, nao copie:

```bash
make bff-ci        # rodape do Jest: "Tests: N passed" / "Test Suites: N passed"
make copilot-test  # rodape do pytest: "N passed"
make ml-deep-test  # idem
make web-ci        # idem
```

A contagem nao e criterio de aceitacao em nenhum destes passos. Os criterios sao o
**exit code** do alvo e a **presenca nominal** da lista acima — ambos gateados, nenhum
sujeito a apodrecimento.

### Passo 6 — Validacao de plataforma

```bash
make platform-validate
```

Inclui tofu validate + kubeconform + kyverno test + otel-validate (TX-5).

---

## 6. Checklist dos 4 gates

### security-reviewer

Confirma:
- [ ] Cilium deny-all aplicado nas celulas delivery, ml, data (este arquivo), pci e aml-kyc (arquivos proprios de cada celula).
- [ ] Nenhuma porta extra aberta nas Cilium policies sem policy de allow explicita.
- [ ] Kyverno baseline proibe containers privilegiados, hostPath, root UID.
- [ ] Kyverno PCI proibe volumes hostPath e imagens sem digest na celula pci.
- [ ] Kyverno AML/KYC proibe acesso a secret de outra celula e imagens sem digest.
- [ ] OpenBao: nenhuma credencial estatica em imagem ou git.
- [ ] cosign + Trivy + Falco ativos no cluster antes de redirecionar trafego.
- [ ] SHA256 dos binarios de CI verificados (kubeconform, kyverno, tofu — M-1 fechado).

### privacy-compliance-auditor

Confirma:
- [ ] OTel Collector em producao usa a mesma config de `platform/observability/otel-collector.yaml`.
- [ ] `otelcol validate` VERDE na imagem de producao (CI gate otel-validate passando — TX-5).
- [ ] **TODO** pipeline de `service.pipelines` (hoje `traces`, `metrics` e `logs`) contem
      `transform/redact-pii` E o `redaction/allowlist-<tipo>` do seu tipo de sinal — nao
      apenas os dois historicos. Nao confira a olho: rode
      `python3 platform/observability/otel-pipeline-redaction-check.py platform/observability/otel-collector.yaml`,
      que enumera os pipelines existentes e reprova qualquer um sem redacao (default-deny,
      30a onda — antes o gate so olhava os nomes literais "traces"/"logs").
- [ ] `allow_all_keys: false` em TODAS as allowlists (hoje 3: traces, logs, metrics).
- [ ] IP bruto descartado no servico antes de chegar ao OTel Collector (TX-5 defense-in-depth).
- [ ] PII/KYC confinada na celula AML/KYC; telemetria exportada sem PII.
- [ ] Cifra KMS-envelope ativa sobre dados `compliance` (chave real em HSM/KMS — §3.1).

### money-ledger-guardian

Confirma:
- [ ] Migracoes 0001+0002+0003+0004 do ledger aplicadas (inclusive 0002 recon + 0003 RLS + 0004 imutabilidade — NAO apenas 0001 como no Docker local).
- [ ] `REVOKE UPDATE, DELETE ON ledger.postings FROM adserver_app;` executado em producao (§2.3.1 — **nao** vem em nenhuma migration; so existe em `make/db.mk` e `db.yml`, ambos ambientes de teste).
- [ ] `psql "$PGURL" -f db/ledger/tests/postings_immutability_test.sql` VERDE contra o banco de PRODUCAO ja migrado (imprime `== LEDGER POSTINGS IMMUTABILITY: ALL TESTS PASSED ==`). Este e o unico check que prova que a 0004 pegou de fato.
- [ ] Migration `0004_campaign_zones_with_check_up.sql` do schema config aplicada (§2.2 — `WITH CHECK` explicito no catalogo).
- [ ] `smoke-payments.sh` VERDE: todos os 4 invariantes (a)(b)(c)(d).
- [ ] Nenhum float em codigos de ledger (`make verify` no-float TX-2 VERDE).
- [ ] USDC com scale=6 inserido no `asset_registry.assets` antes do primeiro deposito.
- [ ] Reconciliador configurado com fonte Iceberg real apos os dados estarem disponiveis.
- [ ] Fireblocks NAO ativado ate AUM justificar (Safe multisig primeiro — ADR-0004 §C).
- [ ] TigerBeetle NAO ativado sem gargalo de escrita provado (Postgres double-entry primeiro).

### parity-golden-test-guardian

Confirma:

- [ ] `make go-test` VERDE — gate canonico: unit tests + golden tests com `-race` (toolchain Go 1.26, filtra `node_modules/`; destravado pelo swap `spaolacci`→`twmb/murmur3` na 7a onda).
- [ ] `make parity-golden-short` VERDE — sem regressao no motor de decisao.
- [ ] `make ml-deep-test` VERDE — K1 deep ranker (TwoTowerDCNv2 + paridade ONNX) + invariante default-off (DEEP_ENABLED=false ate uplift A/B provado — K8 gate, ADR-0004). Conferir que os testes de paridade ONNX **rodaram** e nao foram SKIPPED por falta de `onnxscript` (sentinela anti-skip, 28a/29a onda).
- [ ] Deep ranking (Triton/GPU) NAO ativo no hot path sem uplift A/B provado (K8 pendente).
- [ ] Fail-open deterministico do ranker verificado: timeout duro retorna cascata pura.

---

## 7. Gates ainda pendentes de infra/trafego real

Os itens abaixo estao **codigo-completos na main** mas bloqueados por pre-requisitos de
infra ou trafego que nao existem neste ambiente:

| Gate | Codigo | Bloqueio |
|------|--------|---------|
| K8 — Deep ranking uplift A/B | Scaffolding completo (flag desligada). Sidecar Triton pronto. | Requer trafego real para A/B estatisticamente significativo. Proibido promover sem uplift provado (ADR-0004 §A). |
| AEV/BND spec `scale` | `asset_registry` aceita AEV/BND como linhas sem migracao de schema. | Bloqueado por spec oficial dos tokens (decimals, jurisdicao) — perguntas abertas de §3 do ADR-0004. |
| Triton/GPU em producao | Codigo pronto; imagem sidecar buildavel. | Requer uplift A/B (K8) + provisionamento de no GPU no EKS. |
| Fireblocks | Interface `ChainConnector` pronta; Fireblocks atrás de feature flag. | Ativar apenas quando AUM justificar (ADR-0004 §C). Safe multisig e o default. |
| Trafego real (cutover) | Plataforma EKS + Cilium + Argo CD provisionada via OpenTofu. | Requer aplicacao em cloud com aprovacao humana — nao automatizavel aqui. |

---

## 8. Rollback por migracao

Rollback em ordem inversa da aplicacao. Nunca pular etapas.

### Rollback Postgres (ordem inversa)

```bash
# 1. compliance (celula AML/KYC — instancia separada)
psql "$PGURL_COMPLIANCE" -f db/compliance/migrations/0001_compliance_schema_down.sql

# 2. vector (ordem inversa: 0002 antes de 0001)
psql "$PGURL" -f db/vector/migrations/0002_vector_rls_down.sql
psql "$PGURL" -f db/vector/migrations/0001_vector_schema_down.sql

# 3. ledger (ordem inversa: 0004, 0003, 0002, 0001)
psql "$PGURL" -f db/ledger/migrations/0004_ledger_postings_immutable_down.sql
psql "$PGURL" -f db/ledger/migrations/0003_ledger_rls_down.sql
psql "$PGURL" -f db/ledger/migrations/0002_reconciliation_exceptions_down.sql
psql "$PGURL" -f db/ledger/migrations/0001_ledger_schema_down.sql

# 4. config (ordem inversa: 0004, 0003, 0002, 0001)
psql "$PGURL" -f db/config/migrations/0004_campaign_zones_with_check_down.sql
psql "$PGURL" -f db/config/migrations/0003_campaign_zones_rls_down.sql
psql "$PGURL" -f db/config/migrations/0002_config_rls_down.sql
psql "$PGURL" -f db/config/migrations/0001_config_schema_down.sql

# 5. asset_registry
psql "$PGURL" -f db/asset_registry/migrations/0001_asset_registry_down.sql
```

**Atencao (0004_down):** reverter a `0004` do ledger **remove a garantia de append-only**
— `UPDATE`/`DELETE` em `ledger.postings` e em `journal_entries` ja finalizadas voltam a
ser aceitos pelo banco. Se o objetivo do rollback nao for descartar o schema inteiro, NAO
reverta a `0004`: ela nao tem dependencia estrutural com as demais e pode permanecer.
Reverter a `0004` exige aprovacao explicita do guardiao de ledger, e o REVOKE de §2.3.1
deve ser mantido mesmo assim (ele nao e desfeito por nenhum `_down`).

**Atencao:** o rollback do ledger (`0001_down`) remove o schema `ledger` inteiro,
incluindo `journal_entries`, `postings` e `reconciliation_exceptions`. Toda a
contabilidade registrada sera perdida. Executar apenas apos backup verificado e com
aprovacao explicita do guardiao de ledger.

### Rollback ClickHouse

ClickHouse nao tem migrations `_down` — as tabelas sao append-only por design.
Rollback requer:
1. Parar todos os consumers (Kafka Engine) que escrevem nas tabelas afetadas.
2. `DROP TABLE` das MVs e tabelas na ordem inversa de dependencia.
3. Re-aplicar a migracao anterior.

Procedimento detalhado fora do escopo deste runbook — requer aprovacao do time de dados.

### Rollback de plataforma (Kubernetes/Argo CD)

Argo CD mantem historico de sincronizacao. Para reverter um deploy:

```bash
argocd app rollback <app-name> <revision>
```

Para rollback da plataforma inteira (OpenTofu): requer `tofu plan` + aprovacao humana.
Nunca executar `tofu destroy` sem aprovacao explicita.

---

## 9. Limitacoes conhecidas

### L-1 / Ressalva-2 — Deny-all do Cilium nao asserido comportamentalmente

O `kyverno test` valida apenas policies de admission control (mutate/validate). Ele NAO
consegue testar comportamento de rede — nao ha como verificar via `kyverno test` que
o deny-all do Cilium realmente bloqueia pacotes entre namespaces.

**Consequencia:** e teoricamente possivel ter um manifest Cilium sintaticamente valido
que nao produza o comportamento de bloqueio esperado em cluster (ex.: versao do Cilium
sem suporte ao campo, CRD nao instalada, agente Cilium crashando).

**Mitigacao:** validacao comportamental requer testes de conectividade em cluster real
(ex.: `kubectl exec` + `curl` entre pods de namespaces distintos, Sonobuoy network
policy tests, ou netpol-tester). Incluir como parte do smoke pos-deploy antes de
direcionar trafego de producao.

**Exclusao reciproca no lado `delivery`:** a policy de deny-all do namespace `delivery`
esta em `platform/k8s/netpol/cilium-default-deny.yaml`. Nao ha kyverno test que verifique
que um pod do namespace `delivery` e impossibilitado de abrir conexao com `pci` ou
`aml-kyc`. Isto so e verificavel comportamentalmente (ver acima).

### Cobertura de otelcol validate limitada a componentes da distro contrib

O `otelcol validate --config` usa a distro `otel/opentelemetry-collector-contrib`.
Processadores customizados ou fora desta distro nao serao validados. Se a imagem de
producao for uma distro customizada (OCB), o CI deve usar a imagem customizada — nao
a contrib generica.

### FQDNs de FQDN-based egress (Cilium) nao verificados sem cluster

As policies de egress com FQDN (ex.: `allow-egress-chainalysis.yaml`,
`allow-egress-travel-rule.yaml`) usam `toFQDNs` do Cilium. A validacao offline
(kubeconform com `-ignore-missing-schemas`) nao verifica se os FQDNs resolvem ou se
o Cilium DNS proxy esta funcionando. Validar em cluster real apos deploy.
