-- =============================================================================
-- 0004_ledger_postings_immutable_down.sql
-- Reverte a migration 0004 (imutabilidade de postings/journal_entries).
--
-- AVISO: reverter esta migration remove a garantia de append-only no banco —
-- UPDATE/DELETE em ledger.postings e ledger.journal_entries voltam a ser
-- possíveis (sujeitos apenas a GRANT/RLS). Usar apenas em rollback
-- emergencial — não executar em produção sem aprovação da equipe de billing.
-- =============================================================================

DROP TRIGGER  IF EXISTS journal_entries_immutable_trg ON ledger.journal_entries;
DROP FUNCTION IF EXISTS ledger.check_journal_entry_immutable();

DROP TRIGGER  IF EXISTS postings_immutable_trg ON ledger.postings;
DROP FUNCTION IF EXISTS ledger.check_postings_immutable();
