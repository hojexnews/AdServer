-- =============================================================================
-- 0003_ledger_rls_up.sql
-- RLS (Row Level Security) nas tabelas core do ledger (CRITICAL-2).
--
-- Por que esta migration esta separada da 0001:
--   A 0001 criou o schema e as tabelas; a 0003 adiciona a camada de
--   isolamento por tenant sem alterar estrutura de colunas ou indices.
--   Separacao facilita rollback cirurgico em caso de emergencia.
--
-- Tabelas protegidas:
--   ledger.accounts, ledger.journal_entries, ledger.postings
--
-- View afetada:
--   ledger.account_balances -> SECURITY INVOKER = true (RLS das tabelas-base
--   se aplica atraves da view, eliminando bypass silencioso).
--
-- Padrao canonico do repo:
--   Identico ao schema compliance (0001_compliance_schema_up.sql ~L51-145)
--   e ao 0002_reconciliation_exceptions_up.sql ~L126-135.
--   Funcao helper ledger.current_tenant_id() segue compliance.current_tenant_id().
--   Variavel de sessao: adserver.tenant_id (SET LOCAL pelo middleware de app).
--   Fail-closed: NULLIF retorna NULL quando a variavel nao esta setada ->
--   nenhuma linha e visivel (equivalente a tabela vazia para o role).
--
-- RLS nao afeta superusuarios nem roles com BYPASSRLS (ex.: adserver_loader).
-- O adserver_app (role de aplicacao) e um role nao-superuser — aplica-se RLS.
--
-- Invariante TX-2: nenhuma coluna monetaria e tocada por esta migration.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- Funcao auxiliar: le tenant_id da sessao corrente.
-- Padrao canonico: identico a compliance.current_tenant_id()
-- (0001_compliance_schema_up.sql ~L51-59).
-- Retorna NULL se adserver.tenant_id nao estiver setado -> fail-closed.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION ledger.current_tenant_id()
RETURNS UUID LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT NULLIF(current_setting('adserver.tenant_id', true), '')::UUID
$$;

COMMENT ON FUNCTION ledger.current_tenant_id() IS
    'Le tenant_id da sessao via SET LOCAL adserver.tenant_id. '
    'Retorna NULL se ausente — rejeita acesso (fail-closed). '
    'Padrao canonico: identico a compliance.current_tenant_id().';

-- ---------------------------------------------------------------------------
-- ledger.accounts
-- ---------------------------------------------------------------------------
ALTER TABLE ledger.accounts ENABLE  ROW LEVEL SECURITY;
ALTER TABLE ledger.accounts FORCE   ROW LEVEL SECURITY;

CREATE POLICY accounts_tenant_isolation ON ledger.accounts
    USING (tenant_id = ledger.current_tenant_id());

COMMENT ON POLICY accounts_tenant_isolation ON ledger.accounts IS
    'Isola contas por tenant. Fail-closed: sem adserver.tenant_id setado, nenhuma linha visivel.';

-- ---------------------------------------------------------------------------
-- ledger.journal_entries
-- ---------------------------------------------------------------------------
ALTER TABLE ledger.journal_entries ENABLE  ROW LEVEL SECURITY;
ALTER TABLE ledger.journal_entries FORCE   ROW LEVEL SECURITY;

CREATE POLICY journal_entries_tenant_isolation ON ledger.journal_entries
    USING (tenant_id = ledger.current_tenant_id());

COMMENT ON POLICY journal_entries_tenant_isolation ON ledger.journal_entries IS
    'Isola entradas de diario por tenant. Fail-closed: sem adserver.tenant_id setado, nenhuma linha visivel.';

-- ---------------------------------------------------------------------------
-- ledger.postings (tabela particionada)
--
-- Em Postgres 16, CREATE POLICY na tabela mae se propaga automaticamente
-- para todas as particoes existentes e futuras — nao e necessario criar
-- policy em cada particao individualmente.
-- ---------------------------------------------------------------------------
ALTER TABLE ledger.postings ENABLE  ROW LEVEL SECURITY;
ALTER TABLE ledger.postings FORCE   ROW LEVEL SECURITY;

CREATE POLICY postings_tenant_isolation ON ledger.postings
    USING (tenant_id = ledger.current_tenant_id());

COMMENT ON POLICY postings_tenant_isolation ON ledger.postings IS
    'Isola postings por tenant. Propaga para todas as particoes (PG16). '
    'Fail-closed: sem adserver.tenant_id setado, nenhuma linha visivel.';

-- ---------------------------------------------------------------------------
-- ledger.account_balances (view)
--
-- A view foi criada em 0001 como SECURITY DEFINER implicito (padrao PG).
-- Com SECURITY DEFINER, as queries internas da view rodam com privilegios
-- do dono da view — a RLS das tabelas-base NAO e aplicada, tornando a
-- view um bypass silencioso de RLS.
--
-- A correcao e recriar a view com SECURITY INVOKER, fazendo-a executar
-- com os privilegios do caller — a RLS de accounts e postings se aplica
-- normalmente atraves da view.
--
-- A view e identica a da 0001 em conteudo; apenas o atributo de seguranca
-- e alterado.
-- ---------------------------------------------------------------------------
DROP VIEW IF EXISTS ledger.account_balances;

CREATE VIEW ledger.account_balances
    WITH (security_invoker = true)
AS
SELECT
    a.id               AS account_id,
    a.tenant_id,
    a.code             AS account_code,
    a.name             AS account_name,
    a.kind,
    a.asset_code,
    -- Saldo = sum(debitos) - sum(creditos), em minor units NUMERIC
    COALESCE(SUM(p.debit_amount - p.credit_amount), 0) AS balance,
    COUNT(p.id)        AS posting_count
FROM ledger.accounts a
LEFT JOIN ledger.postings p ON p.account_id = a.id
GROUP BY a.id, a.tenant_id, a.code, a.name, a.kind, a.asset_code;

COMMENT ON VIEW ledger.account_balances IS
    'Saldo derivado de postings (nao armazenado). SECURITY INVOKER: RLS de accounts '
    'e postings se aplica atraves desta view. Use para leitura; nunca faca UPDATE baseado nisso.';
