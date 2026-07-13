-- =============================================================================
-- db/seed/dev_roles.sql — DEV/LOCAL ONLY roles for the AdServer stack.
--
-- Two distinct roles enforce the read/write split around RLS (TX-3):
--
--   adserver_loader  — read-only, BYPASSRLS.  Used ONLY by the decision
--                      engine's config snapshot loader (internal/configload),
--                      which must read config CROSS-TENANT to build the global
--                      in-memory snapshot.  CA-1 isolation is enforced in the
--                      cascade (zone→tenant), not on this read path.
--   adserver_app     — read/write, RLS ENFORCED.  Used by the BFF/console.
--                      Every session sets `SET LOCAL adserver.tenant_id` and
--                      sees only its own tenant's rows (FORCE RLS).
--
-- Passwords here are DEV defaults — NEVER use these in staging/production
-- (production secrets come from OpenBao; see platform/secrets/openbao).
-- Run this AFTER the schema migrations (the GRANTs need the tables to exist).
-- =============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_loader') THEN
        CREATE ROLE adserver_loader LOGIN PASSWORD 'loader_dev_only' BYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_app') THEN
        CREATE ROLE adserver_app LOGIN PASSWORD 'app_dev_only';
    END IF;
END
$$;

GRANT USAGE ON SCHEMA config         TO adserver_loader, adserver_app;
GRANT USAGE ON SCHEMA asset_registry TO adserver_loader, adserver_app;

-- Loader: read-only across all config + asset registry tables.
GRANT SELECT ON ALL TABLES IN SCHEMA config         TO adserver_loader;
GRANT SELECT ON ALL TABLES IN SCHEMA asset_registry TO adserver_loader;

-- App: full DML on config (RLS still filters rows per tenant).
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA config TO adserver_app;
GRANT SELECT ON ALL TABLES IN SCHEMA asset_registry TO adserver_app;

-- The RLS policy helper must be callable by the app role.
GRANT EXECUTE ON FUNCTION config.current_tenant_id() TO adserver_app, adserver_loader;

-- App writes INSERT rows into BIGSERIAL tables, so it needs nextval() on the
-- owning sequences (USAGE covers nextval + currval). Read-only loader does not.
GRANT USAGE ON ALL SEQUENCES IN SCHEMA config TO adserver_app;
