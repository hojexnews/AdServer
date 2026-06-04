-- =============================================================================
-- 0003_ledger_rls_down.sql
-- Reverte a migration 0003 (RLS nas tabelas core do ledger).
--
-- AVISO: reverter esta migration remove o isolamento por tenant das tabelas
-- ledger.accounts, journal_entries e postings. Usar apenas em rollback
-- emergencial — nao executar em producao sem aprovacao da equipe de seguranca.
-- =============================================================================

-- Remove policies
DROP POLICY IF EXISTS accounts_tenant_isolation       ON ledger.accounts;
DROP POLICY IF EXISTS journal_entries_tenant_isolation ON ledger.journal_entries;
DROP POLICY IF EXISTS postings_tenant_isolation        ON ledger.postings;

-- Desabilita RLS (FORCE e implicito ao DISABLE)
ALTER TABLE ledger.accounts        DISABLE ROW LEVEL SECURITY;
ALTER TABLE ledger.journal_entries DISABLE ROW LEVEL SECURITY;
ALTER TABLE ledger.postings        DISABLE ROW LEVEL SECURITY;

-- Restaura a view sem SECURITY INVOKER (volta ao comportamento implicito da 0001)
DROP VIEW IF EXISTS ledger.account_balances;

CREATE VIEW ledger.account_balances AS
SELECT
    a.id               AS account_id,
    a.tenant_id,
    a.code             AS account_code,
    a.name             AS account_name,
    a.kind,
    a.asset_code,
    COALESCE(SUM(p.debit_amount - p.credit_amount), 0) AS balance,
    COUNT(p.id)        AS posting_count
FROM ledger.accounts a
LEFT JOIN ledger.postings p ON p.account_id = a.id
GROUP BY a.id, a.tenant_id, a.code, a.name, a.kind, a.asset_code;

COMMENT ON VIEW ledger.account_balances IS
    'Saldo derivado de postings (nao armazenado). Use para leitura; nunca faca UPDATE baseado nisso.';

-- A 0003_down dropa e recria a view; reaplica o GRANT para adserver_app.
GRANT SELECT ON ledger.account_balances TO adserver_app;

-- Remove funcao helper
DROP FUNCTION IF EXISTS ledger.current_tenant_id();
