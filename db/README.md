# db/ — Migrations SQL do AdServer (Fase 1, I1)

> Responsável: Guardião de Dinheiro & Ledger.
> Fronteira: apenas `db/` e `make/db.mk`. Não toque em `go.mod`, `gen/`, `services/`, `internal/`.

---

## Ferramenta de migração: golang-migrate

Todas as migrations usam o formato do
[golang-migrate](https://github.com/golang-migrate/migrate) (`github.com/golang-migrate/migrate/v4`).

Convenção de nome de arquivo:
```
{version}_{title}.up.sql    — aplica a migration
{version}_{title}.down.sql  — reverte a migration
```

Versão = inteiro sequencial com 4 dígitos (`0001`, `0002`, …).

Instalação do CLI:
```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

Variável de ambiente obrigatória:
```bash
export DATABASE_URL="postgres://user:pass@host:5432/adserver?sslmode=require"
```

Não há instância Postgres em CI durante o desenvolvimento de Fase 1. Rode `make db-migrate-up`
em ambiente de desenvolvimento local com Postgres 16 disponível.

---

## Estrutura de diretórios

```
db/
  asset_registry/
    migrations/
      0001_asset_registry_up.sql    — schema + tabela + seed completo
      0001_asset_registry_down.sql
  config/
    migrations/
      0001_config_schema_up.sql     — schema config (advertiser, campaign, banner, site, zone,
                                      campaign_zone, delivery_rule_set, delivery_rule, cap)
      0001_config_schema_down.sql
      0002_config_rls_up.sql        — Row-Level Security por tenant_id
      0002_config_rls_down.sql
      0003_campaign_zones_rls_up.sql  — Defesa-em-profundidade: RLS para campaign_zones
      0003_campaign_zones_rls_down.sql
    tests/
      rls_isolation_test.sql          — Teste de isolamento cross-tenant (TX-3)
  ledger/
    migrations/
      0001_ledger_schema_up.sql     — accounts, journal_entries, postings (particionado),
                                      constraint trigger de balanço, view account_balances
      0001_ledger_schema_down.sql
    BILLING.md                      — modelo de postings CPM/CPC/CPA/Tenancy
  README.md                         — este arquivo
```

---

## Ordem de aplicação

As migrations são independentes por banco (`asset_registry`, `config`, `ledger` podem ser
schemas no mesmo banco ou bancos separados). Se no mesmo banco, aplicar nesta ordem:

1. `asset_registry` — sem dependências externas.
2. `config` — sem dependência de `ledger` ou `asset_registry` (FK lógica, não física).
3. `ledger` — sem dependência de `config` (FK lógica para `asset_registry.code`).

Comandos (substituir `{schema}` pelo nome do diretório):
```bash
migrate -database "$DATABASE_URL" -path db/{schema}/migrations up
migrate -database "$DATABASE_URL" -path db/{schema}/migrations down
```

Ou use os alvos Make:
```bash
make db-migrate-up    # aplica todos os schemas na ordem acima
make db-migrate-down  # reverte um passo por schema (use com cuidado)
make db-lint          # roda scripts/ci/no-float-sql.sh sobre db/
make db-test          # executa db/config/tests/rls_isolation_test.sql (exige Postgres 16 local)
```

---

## RLS (Row-Level Security) por tenant_id

### Estratégia

Todas as tabelas com `tenant_id` no schema `config` têm RLS habilitado via
`0002_config_rls_up.sql`. A policy usa `current_setting('adserver.tenant_id', true)`.

### Como o tenant_id chega à sessão

O middleware da aplicação (Go/BFF) injeta o `tenant_id` na sessão antes de qualquer query:

```sql
SET LOCAL adserver.tenant_id = '<uuid-do-tenant>';
-- agora todas as queries nesta transação só enxergam linhas do tenant
```

Com PgBouncer em modo `transaction pooling`, o `SET LOCAL` deve ser feito dentro de cada
transação (não de sessão). O pool de conexões não persiste `SET LOCAL` entre transações.

### Usuários de banco

| Usuário         | Papel                                 | RLS |
|-----------------|---------------------------------------|-----|
| `adserver_app`  | Aplicação (Go/BFF)                    | Sim — policy filtra por `adserver.tenant_id` |
| `adserver_admin`| Migrations e admin                    | Não — superuser bypassa RLS por padrão |
| `adserver_ro`   | Leitura (dashboards, relatórios)      | Sim — mesma policy |

Para o usuário de aplicação, conceder permissões mínimas:
```sql
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA config TO adserver_app;
GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA ledger TO adserver_app;
GRANT SELECT ON ALL TABLES IN SCHEMA asset_registry TO adserver_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA config TO adserver_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA ledger TO adserver_app;
```

### `campaign_zones` — defesa-em-profundidade (migration 0003)

A tabela `config.campaign_zones` é uma tabela de junção N:N (`campaign_id`, `zone_id`)
sem `tenant_id` próprio. O isolamento poderia ser feito apenas por JOIN obrigatório
com `config.campaigns`/`config.zones` (que têm `FORCE RLS`), mas essa estratégia depende
de uma invariante de aplicação — não do banco.

**Decisão (TX-3 / §2.6):** adicionar RLS via subquery EXISTS em `campaigns`, sem
desnormalizar `tenant_id` em `campaign_zones`.

```sql
CREATE POLICY campaign_zones_tenant_isolation ON config.campaign_zones
    USING (
        EXISTS (
            SELECT 1 FROM config.campaigns c
            WHERE  c.id = campaign_zones.campaign_id
        )
    );
```

Como `campaigns` tem `FORCE ROW LEVEL SECURITY`, a subquery herda automaticamente a
filtragem por `config.current_tenant_id()`. Qualquer acesso direto à tabela (DBA,
ferramenta ad-hoc, query sem JOIN) fica protegido em nível de banco.

Custo: nested lookup por row avaliada pela policy (amortizado pelo índice `campaigns_pk`,
O(log N)); aceitável para tabela de configuração lida em snapshot batch.

### Ledger — RLS pendente (I2)

O schema `ledger` não tem RLS nesta migration (I1). O ledger é acessado apenas por jobs
batch internos (não pelo anunciante diretamente). RLS no ledger será adicionado em I2 quando
o BFF de relatórios financeiros for definido.

### Teste de isolamento RLS (TX-3)

O arquivo `db/config/tests/rls_isolation_test.sql` prova o isolamento cross-tenant.

**Casos cobertos:**

| Bloco | Caso | Resultado esperado |
|-------|------|--------------------|
| 1     | Tenant A lê todas as tabelas com RLS | Vê exatamente 1 linha por tabela (somente as suas) |
| 1i    | Tenant A lê `campaign_zones` via JOIN | 1 vínculo (camp_a ↔ zone_a), 0 de B |
| 1j    | Tenant A lê `campaign_zones` direto (sem JOIN) | 1 linha (RLS 0003 via EXISTS filtra) |
| 2     | Tenant B lê todas as tabelas com RLS | Vê exatamente 1 linha por tabela (somente as suas) |
| 3     | Tenant A tenta WHERE tenant_id = B | 0 linhas (cross-tenant bloqueado) |
| 3d    | Tenant A tenta acessar `campaign_zones` de B | 0 linhas (EXISTS em campaigns descarta B) |
| 4     | Sem `adserver.tenant_id` setado (GUC vazio) | 0 linhas em todas as tabelas (fail-closed) |
| 4i/4j | `campaign_zones` direto e via JOIN sem tenant_id | 0 linhas (fail-closed) |
| 5     | Superuser (adserver_admin) lê dados de ambos | 2 linhas (bypass RLS esperado para migrations) |

**Como rodar:**

```bash
# Pré-requisito: migrations aplicadas e role adserver_app criada
make db-migrate-up
make db-test
```

O teste roda dentro de `BEGIN ... ROLLBACK` — não persiste dados. Saída esperada na última
linha: `== RLS ISOLATION: ALL TESTS PASSED ==`.

---

## Invariantes do ledger (não negociáveis)

1. `sum(debit_amount) = sum(credit_amount)` por `journal_entry_id + asset_code` —
   garantido pelo constraint trigger `DEFERRABLE INITIALLY DEFERRED`.
2. Nenhuma captura grava saldo direto em `accounts` — saldo é derivado de `postings`.
3. Toda movimentação tem `idempotency_key` único — reprocessamentos at-least-once são seguros.
4. Postings são append-only — estorno via novo par (nunca DELETE/UPDATE de postings).
5. Reconciliação abre exceção, nunca autocorrige — ver `BILLING.md §7`.
6. Faturamento reconcilia contra Iceberg (lakehouse), nunca contra streaming (ClickHouse).

---

## Particionamento de postings

Partições mensais pré-criadas: `2026-01` a `2026-12` + `postings_future` (catch-all).
Novas partições devem ser criadas mensalmente via job agendado (pg_partman ou script).

Script sugerido para criar partição do próximo mês (rodar no dia 25 de cada mês):
```sql
-- Exemplo: criar partição de 2027-02
CREATE TABLE ledger.postings_2027_02 PARTITION OF ledger.postings
    FOR VALUES FROM ('2027-02-01') TO ('2027-03-01');
```

Automatizar via `pg_partman` extensão ou cron job com `psql -c`.

---

## Float PROIBIDO

Nenhuma coluna monetária usa `FLOAT`, `REAL`, `DOUBLE PRECISION` ou o tipo `MONEY` do Postgres.
Todo valor monetário usa `NUMERIC(p,s)` onde `s` é o `scale` do Asset Registry:

| Ativo           | Tipo Postgres    |
|-----------------|------------------|
| BRL/USD/EUR     | `NUMERIC(20, 2)` |
| USDC/USDT       | `NUMERIC(26, 6)` |
| ERC-20 (scale=18)| `NUMERIC(40,18)` |
| AEV/BND         | TBD (disabled)   |

O guard `scripts/ci/no-float-sql.sh` valida todos os arquivos `db/**/*.sql` em CI e no alvo
`make db-lint`. Toda PR que introduzir `float`/`real`/`double precision`/`money` em coluna
monetária falhará o lint antes do merge.
