-- =============================================================================
-- 0001_stats_schema_down.sql
-- Reverte o schema stats inteiramente.
--
-- adserver_stats_writer NÃO é dropada aqui — e, desde o achado H-2
-- (security-reviewer), também não é mais CRIADA pela migration _up: o role
-- vive em db/seed/dev_roles.sql, mesmo padrão de adserver_app/
-- adserver_loader/adserver_copilot (roles são um concern de vida mais longa
-- que um schema, e nenhuma migration de schema deste repo dropa um role).
-- Seus GRANTs escopados a `stats` somem junto com o DROP SCHEMA CASCADE
-- abaixo (o Postgres remove ACLs de objetos removidos automaticamente); a
-- role em si fica órfã mas inofensiva, pronta para o próximo `up` reidratar
-- o schema (e para make/dev.mk / .github/workflows/db.yml reaplicarem os
-- GRANTs comentados na migration _up, depois de dev_roles.sql).
-- =============================================================================

DROP VIEW     IF EXISTS stats.live_kpis                                 CASCADE;
DROP POLICY   IF EXISTS events_raw_tenant_isolation ON stats.events_raw;
DROP TABLE    IF EXISTS stats.events_raw                                CASCADE;
DROP FUNCTION IF EXISTS stats.current_tenant_id()                       CASCADE;

DROP SCHEMA IF EXISTS stats CASCADE;
