-- =============================================================================
-- 0002_vector_rls_down.sql — Reverso de 0002_vector_rls_up.sql
-- =============================================================================

DROP POLICY IF EXISTS creative_embeddings_tenant_isolation
    ON vector_store.creative_embeddings;

ALTER TABLE IF EXISTS vector_store.creative_embeddings
    DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS help_doc_embeddings_tenant_isolation
    ON vector_store.help_doc_embeddings;

ALTER TABLE IF EXISTS vector_store.help_doc_embeddings
    DISABLE ROW LEVEL SECURITY;
