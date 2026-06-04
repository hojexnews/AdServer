-- =============================================================================
-- 0002_reconciliation_exceptions_down.sql
-- Reverte a migration 0002 (tabela ledger.reconciliation_exceptions).
-- =============================================================================

DROP TABLE IF EXISTS ledger.reconciliation_exceptions;
DROP TYPE  IF EXISTS ledger.exception_status;
