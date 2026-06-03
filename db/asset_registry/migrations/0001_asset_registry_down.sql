-- =============================================================================
-- 0001_asset_registry_down.sql
-- Reverte a criação do schema asset_registry e da tabela assets.
-- =============================================================================

DROP TABLE IF EXISTS asset_registry.assets CASCADE;
DROP FUNCTION IF EXISTS asset_registry.assets_set_updated_at() CASCADE;
DROP SCHEMA IF EXISTS asset_registry CASCADE;
