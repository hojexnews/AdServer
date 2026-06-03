-- =============================================================================
-- 0002_config_rls_down.sql
-- Reverte as políticas RLS do schema config.
-- =============================================================================

DROP POLICY IF EXISTS advertisers_tenant_isolation     ON config.advertisers;
DROP POLICY IF EXISTS campaigns_tenant_isolation        ON config.campaigns;
DROP POLICY IF EXISTS banners_tenant_isolation          ON config.banners;
DROP POLICY IF EXISTS sites_tenant_isolation            ON config.sites;
DROP POLICY IF EXISTS zones_tenant_isolation            ON config.zones;
DROP POLICY IF EXISTS delivery_rule_sets_tenant_isolation ON config.delivery_rule_sets;
DROP POLICY IF EXISTS delivery_rules_tenant_isolation   ON config.delivery_rules;
DROP POLICY IF EXISTS caps_tenant_isolation             ON config.caps;

ALTER TABLE config.advertisers      DISABLE ROW LEVEL SECURITY;
ALTER TABLE config.campaigns        DISABLE ROW LEVEL SECURITY;
ALTER TABLE config.banners          DISABLE ROW LEVEL SECURITY;
ALTER TABLE config.sites            DISABLE ROW LEVEL SECURITY;
ALTER TABLE config.zones            DISABLE ROW LEVEL SECURITY;
ALTER TABLE config.delivery_rule_sets DISABLE ROW LEVEL SECURITY;
ALTER TABLE config.delivery_rules   DISABLE ROW LEVEL SECURITY;
ALTER TABLE config.caps             DISABLE ROW LEVEL SECURITY;

DROP FUNCTION IF EXISTS config.current_tenant_id() CASCADE;
