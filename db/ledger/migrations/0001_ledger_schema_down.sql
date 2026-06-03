-- =============================================================================
-- 0001_ledger_schema_down.sql
-- Reverte o schema ledger inteiramente.
-- =============================================================================

DROP VIEW IF EXISTS ledger.account_balances CASCADE;

DROP TRIGGER  IF EXISTS postings_balance_chk_trg ON ledger.postings;
DROP FUNCTION IF EXISTS ledger.check_entry_balance() CASCADE;

DROP TRIGGER  IF EXISTS accounts_updated_at_trg ON ledger.accounts;
DROP FUNCTION IF EXISTS ledger.set_updated_at() CASCADE;

-- Partições (drop na ordem inversa de dependência)
DROP TABLE IF EXISTS ledger.postings_future  CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_12 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_11 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_10 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_09 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_08 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_07 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_06 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_05 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_04 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_03 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_02 CASCADE;
DROP TABLE IF EXISTS ledger.postings_2026_01 CASCADE;

DROP TABLE IF EXISTS ledger.postings         CASCADE;
DROP TABLE IF EXISTS ledger.journal_entries  CASCADE;
DROP TABLE IF EXISTS ledger.accounts         CASCADE;

DROP TYPE IF EXISTS ledger.entry_status  CASCADE;
DROP TYPE IF EXISTS ledger.account_kind  CASCADE;

DROP SCHEMA IF EXISTS ledger CASCADE;
