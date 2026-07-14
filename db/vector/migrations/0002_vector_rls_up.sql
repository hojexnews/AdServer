-- =============================================================================
-- 0002_vector_rls_up.sql
-- RLS (Row-Level Security) por tenant_id no schema vector_store (TX-3).
--
-- Estratégia IDÊNTICA à do schema config (0002_config_rls_up.sql):
--   - Cada tabela habilita RLS com FORCE ROW LEVEL SECURITY.
--   - Policy usa config.current_tenant_id() (função existente da Fase 1).
--   - SET LOCAL adserver.tenant_id = '<uuid>' antes de qualquer query.
--
-- SEPARAÇÃO RAG (§2.4 / ADR-0003 §C):
--   - creative_embeddings: RLS estrito por tenant (isolamento total).
--   - help_doc_embeddings: policy especial que permite NULL (docs públicos)
--     OU tenant_id = current_tenant_id() (docs privados do tenant).
--
-- TESTE DE ISOLAMENTO: db/vector/tests/vector_rls_isolation_test.sql
--   Prova que tenant A NUNCA lê embeddings de tenant B.
--
-- DEPENDÊNCIA: config.current_tenant_id() de 0002_config_rls_up.sql.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- creative_embeddings — RLS estrito (tenant_id obrigatório, nunca NULL)
-- ---------------------------------------------------------------------------
ALTER TABLE vector_store.creative_embeddings ENABLE ROW LEVEL SECURITY;
ALTER TABLE vector_store.creative_embeddings FORCE  ROW LEVEL SECURITY;

CREATE POLICY creative_embeddings_tenant_isolation
    ON vector_store.creative_embeddings
    USING      (tenant_id = config.current_tenant_id())
    WITH CHECK (tenant_id = config.current_tenant_id());

COMMENT ON POLICY creative_embeddings_tenant_isolation
    ON vector_store.creative_embeddings IS
    'Isolamento estrito por tenant (TX-3): nenhum tenant lê embeddings de outro. '
    'Fail-closed: sem adserver.tenant_id → 0 linhas. WITH CHECK: rejeita INSERT/UPDATE '
    'com tenant_id ≠ do tenant corrente.';

-- ---------------------------------------------------------------------------
-- help_doc_embeddings — RLS com suporte a docs públicos (tenant_id NULL)
-- ---------------------------------------------------------------------------
ALTER TABLE vector_store.help_doc_embeddings ENABLE ROW LEVEL SECURITY;
ALTER TABLE vector_store.help_doc_embeddings FORCE  ROW LEVEL SECURITY;

-- Policy de LEITURA: doc público (NULL) OU doc do tenant corrente.
-- Policy de ESCRITA (WITH CHECK): SÓ o próprio tenant. Sem o WITH CHECK
-- explícito, o Postgres reusa o USING como verificação de escrita — e o ramo
-- `tenant_id IS NULL` deixaria um role de tenant INSERIR um doc "público"
-- (tenant_id NULL) que TODOS os tenants passam a ler (envenenamento do corpus
-- RAG compartilhado). Docs públicos são semeados só pelo loader/superuser
-- (BYPASSRLS), nunca por um tenant.
CREATE POLICY help_doc_embeddings_tenant_isolation
    ON vector_store.help_doc_embeddings
    USING (
        tenant_id IS NULL
        OR tenant_id = config.current_tenant_id()
    )
    WITH CHECK (tenant_id = config.current_tenant_id());

COMMENT ON POLICY help_doc_embeddings_tenant_isolation
    ON vector_store.help_doc_embeddings IS
    'LEITURA: docs públicos (tenant_id IS NULL) visíveis a todos os tenants '
    'autenticados + docs privados do próprio tenant (TX-3). ESCRITA (WITH CHECK): '
    'só o próprio tenant — um tenant NÃO grava doc público (NULL) nem de outro '
    'tenant; públicos são semeados via loader/superuser BYPASSRLS.';

-- ---------------------------------------------------------------------------
-- Grants para o usuário de aplicação
-- (executar como superuser; adserver_app deve existir)
-- ---------------------------------------------------------------------------
-- GRANT USAGE ON SCHEMA vector_store TO adserver_app;
-- GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA vector_store TO adserver_app;
-- GRANT USAGE ON ALL SEQUENCES IN SCHEMA vector_store TO adserver_app;
-- (Descomentar e executar em ambiente real — aqui comentado para não falhar em CI sem o role)
