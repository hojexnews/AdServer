-- =============================================================================
-- 0003_campaign_zones_rls_down.sql
-- Reverte a policy RLS de campaign_zones adicionada em 0003_up.
-- =============================================================================

DROP POLICY IF EXISTS campaign_zones_tenant_isolation ON config.campaign_zones;

ALTER TABLE config.campaign_zones DISABLE ROW LEVEL SECURITY;
