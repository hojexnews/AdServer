-- =============================================================================
-- 0001_stats_schema_down.sql
-- Reverte o schema stats inteiramente.
--
-- adserver_stats_writer NÃO é dropada (mesma convenção do repo: nenhuma
-- migration de schema dropa adserver_app/adserver_loader/adserver_copilot —
-- roles são um concern de vida mais longa que um schema). Seus GRANTs
-- escopados a `stats` somem junto com o DROP SCHEMA CASCADE abaixo (o
-- Postgres remove ACLs de objetos removidos automaticamente); a role em si
-- fica órfã mas inofensiva, pronta para o próximo `up` reidratar o schema.
-- =============================================================================

DROP VIEW     IF EXISTS stats.live_kpis                                 CASCADE;
DROP POLICY   IF EXISTS events_raw_tenant_isolation ON stats.events_raw;
DROP TABLE    IF EXISTS stats.events_raw                                CASCADE;
DROP FUNCTION IF EXISTS stats.current_tenant_id()                       CASCADE;

DROP SCHEMA IF EXISTS stats CASCADE;
