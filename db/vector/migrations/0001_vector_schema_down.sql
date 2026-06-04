-- =============================================================================
-- 0001_vector_schema_down.sql — Reverso de 0001_vector_schema_up.sql
-- =============================================================================

DROP TABLE IF EXISTS vector_store.creative_embeddings CASCADE;
DROP TABLE IF EXISTS vector_store.help_doc_embeddings CASCADE;
DROP FUNCTION IF EXISTS vector_store.set_updated_at() CASCADE;
DROP SCHEMA IF EXISTS vector_store CASCADE;
