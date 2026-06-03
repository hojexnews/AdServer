-- =============================================================================
-- 0001_config_schema_down.sql
-- Reverte o schema de configuração inteiramente.
-- =============================================================================

DROP TABLE IF EXISTS config.caps                CASCADE;
DROP TABLE IF EXISTS config.delivery_rules      CASCADE;
DROP TABLE IF EXISTS config.delivery_rule_sets  CASCADE;
DROP TABLE IF EXISTS config.campaign_zones      CASCADE;
DROP TABLE IF EXISTS config.zones               CASCADE;
DROP TABLE IF EXISTS config.sites               CASCADE;
DROP TABLE IF EXISTS config.banners             CASCADE;
DROP TABLE IF EXISTS config.campaigns           CASCADE;
DROP TABLE IF EXISTS config.advertisers         CASCADE;

DROP FUNCTION IF EXISTS config.set_updated_at() CASCADE;

DROP TYPE IF EXISTS config.cap_scope       CASCADE;
DROP TYPE IF EXISTS config.logical_op      CASCADE;
DROP TYPE IF EXISTS config.owner_entity    CASCADE;
DROP TYPE IF EXISTS config.creative_type   CASCADE;
DROP TYPE IF EXISTS config.pricing_model   CASCADE;
DROP TYPE IF EXISTS config.goal_metric     CASCADE;
DROP TYPE IF EXISTS config.campaign_type   CASCADE;

DROP SCHEMA IF EXISTS config CASCADE;
