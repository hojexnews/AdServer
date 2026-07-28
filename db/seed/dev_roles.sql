-- =============================================================================
-- db/seed/dev_roles.sql — DEV/LOCAL ONLY roles for the AdServer stack.
--
-- Three distinct roles enforce the read/write split around RLS (TX-3):
--
--   adserver_loader   — read-only, BYPASSRLS.  Used ONLY by the decision
--                        engine's config snapshot loader (internal/configload),
--                        which must read config CROSS-TENANT to build the global
--                        in-memory snapshot.  CA-1 isolation is enforced in the
--                        cascade (zone→tenant), not on this read path.
--   adserver_app       — read/write, RLS ENFORCED.  Used by the BFF/console.
--                        Every session sets `SET LOCAL adserver.tenant_id` and
--                        sees only its own tenant's rows (FORCE RLS).
--   adserver_copilot   — read-only, NOBYPASSRLS EXPLICIT.  Used by the copiloto
--                        (services/copilot) for its RAG lookups against
--                        vector_store.{creative_embeddings,help_doc_embeddings}
--                        (achado #7, auditoria bundle F): those pgvector
--                        queries depend 100% on RLS for tenant isolation — the
--                        copiloto's DATABASE_URL must NEVER resolve to a
--                        BYPASSRLS role (unlike adserver_loader, whose
--                        cross-tenant read is a deliberate, narrow exception
--                        for the decision-engine snapshot only). NOBYPASSRLS
--                        is already Postgres' default when omitted, but it is
--                        spelled out here so the invariant is auditable at a
--                        glance instead of relying on an implicit default.
--   adserver_stats_writer — INSERT/SELECT-only, NOBYPASSRLS EXPLICIT.  Used
--                        by internal/telemetry/pgsink (the collector's
--                        Postgres telemetry sink, ADR-0005 "perfil BETA") to
--                        write stats.events_raw. MOVED HERE (achado H-2,
--                        security-reviewer): the first version of
--                        db/stats/migrations/0001_stats_schema_up.sql created
--                        this role INLINE with a literal `LOGIN PASSWORD`,
--                        which meant a plain `make db-migrate-up` against
--                        ANY DATABASE_URL — including staging/production —
--                        would silently mint a login role with a known dev
--                        password in the real database (and the migration's
--                        _down deliberately never drops roles, so the
--                        credential would survive a rollback). Centralizing
--                        role creation here matches every other role in this
--                        file and keeps schema migrations free of
--                        `CREATE ROLE ... LOGIN PASSWORD` literals. The
--                        GRANTs themselves stay in
--                        db/stats/migrations/0001_stats_schema_up.sql
--                        (commented out, same convention as its
--                        adserver_app block) — run explicitly by
--                        make/dev.mk::dev-db-setup and
--                        .github/workflows/db.yml AFTER this file.
--
-- Passwords here are DEV defaults — NEVER use these in staging/production
-- (production secrets come from OpenBao; see platform/secrets/openbao).
-- Run this AFTER the schema migrations (the GRANTs need the tables to exist,
-- including vector_store — see db/vector/migrations/0001_vector_schema_up.sql).
-- =============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_loader') THEN
        CREATE ROLE adserver_loader LOGIN PASSWORD 'loader_dev_only' BYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_app') THEN
        CREATE ROLE adserver_app LOGIN PASSWORD 'app_dev_only';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_copilot') THEN
        -- NOBYPASSRLS explicit (see header): the copiloto's RAG reads MUST
        -- go through RLS — this role may never be granted BYPASSRLS.
        CREATE ROLE adserver_copilot LOGIN PASSWORD 'copilot_dev_only' NOBYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_stats_writer') THEN
        -- NOBYPASSRLS explicit (see header, achado H-2): the pgsink write
        -- path depends on stats.events_raw_tenant_isolation's WITH CHECK to
        -- keep the session GUC and the row's tenant_id consistent — a
        -- BYPASSRLS role would silently skip that check.
        CREATE ROLE adserver_stats_writer LOGIN PASSWORD 'stats_writer_dev_only' NOBYPASSRLS;
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

-- ---------------------------------------------------------------------------
-- adserver_copilot — RAG read-only role (achado #7, auditoria bundle F).
--
-- SELECT-only on vector_store: the copiloto's RAG tools (services/copilot/
-- tools/gateway.py, "criativos similares por CTR" + "docs de ajuda") only
-- ever SELECT from vector_store.creative_embeddings / help_doc_embeddings.
-- No INSERT/UPDATE/DELETE grant here — minimal privilege for the query path
-- actually exercised today. apply_write's future INSERT/UPDATE dispatch
-- against config (pending G1, see docs/ops/go-live-runbook.md) is NOT wired
-- yet in services/copilot and therefore out of scope for this role until
-- that dispatch exists.
--
-- Depends on vector_store existing (db/vector/migrations/0001), and on
-- config.current_tenant_id() (db/config/migrations/0002) since the
-- vector_store RLS policies call it — this file must run AFTER both.
-- Both callers of this script now guarantee that order:
--   - make/dev.mk::dev-db-setup applies db/vector/migrations/0001+0002
--     BEFORE this file (achado HIGH / bundle F: it used to skip vector
--     entirely and this GRANT aborted the whole target).
--   - .github/workflows/db.yml applies "migrations up — vector" before the
--     "grants" step that runs this file.
--
-- Defense in depth: guarded by IF EXISTS so a future caller that (again)
-- forgets to migrate vector_store first gets a LOUD, non-fatal WARNING
-- instead of ON_ERROR_STOP aborting the whole script (which would also
-- skip every seed/grant statement AFTER this block, e.g. dev_seed.sql
-- never even running). This is intentionally NOT a silent no-op: the
-- WARNING is emitted on every run where the schema is missing, and
-- adserver_copilot keeps its NOBYPASSRLS/least-privilege posture either
-- way (no fallback grant of broader access is ever substituted here).
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_namespace WHERE nspname = 'vector_store') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA vector_store TO adserver_copilot';
        EXECUTE 'GRANT SELECT ON ALL TABLES IN SCHEMA vector_store TO adserver_copilot';
        EXECUTE 'GRANT EXECUTE ON FUNCTION config.current_tenant_id() TO adserver_copilot';
    ELSE
        RAISE WARNING 'dev_roles.sql: schema "vector_store" does not exist yet — '
            'SKIPPING adserver_copilot grants. Apply db/vector/migrations/'
            '0001_vector_schema_up.sql and 0002_vector_rls_up.sql BEFORE this '
            'file, then re-run db/seed/dev_roles.sql so the copiloto RAG role '
            'actually gets its SELECT-only access (it has none until then).';
    END IF;
END
$$;
