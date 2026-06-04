-- =============================================================================
-- 0001_compliance_schema_down.sql
-- Reverte a migration 0001_compliance_schema_up.sql.
-- =============================================================================

-- Remove triggers antes das tabelas
DROP TRIGGER IF EXISTS kyc_subjects_updated_at      ON compliance.kyc_subjects;
DROP TRIGGER IF EXISTS travel_rule_records_updated_at ON compliance.travel_rule_records;

DROP FUNCTION IF EXISTS compliance.set_updated_at();
DROP FUNCTION IF EXISTS compliance.current_tenant_id();

DROP TABLE IF EXISTS compliance.travel_rule_records;
DROP TABLE IF EXISTS compliance.screening_results;
DROP TABLE IF EXISTS compliance.kyc_subjects;

DROP SCHEMA IF EXISTS compliance CASCADE;
