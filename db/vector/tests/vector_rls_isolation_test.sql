-- =============================================================================
-- vector_rls_isolation_test.sql — Teste de isolamento RLS para tabelas vetoriais
--
-- PROPÓSITO:
--   Provar que o RLS do schema vector_store impede tenant A de ler
--   embeddings de tenant B. Cobertura de:
--     - creative_embeddings: isolamento estrito por tenant
--     - help_doc_embeddings: isolamento + docs públicos (tenant_id NULL)
--     - Caso fail-closed: sem adserver.tenant_id → 0 linhas
--     - Cross-tenant: A não lê embeddings de B mesmo filtrando por tenant_id de B
--
-- DEPENDÊNCIAS:
--   - Postgres 16 com extensão pgvector instalada
--   - Migrations 0001 e 0002 do schema vector_store aplicadas
--   - config.current_tenant_id() de 0002_config_rls_up.sql (Fase 1)
--   - Role adserver_app (não-superuser) com GRANTs nas tabelas vector_store
--
-- COMO RODAR:
--   psql -U adserver_admin -d adserver \
--        -v ON_ERROR_STOP=1 \
--        -f db/vector/tests/vector_rls_isolation_test.sql
--
--   Resultado esperado: "== VECTOR RLS ISOLATION: ALL TESTS PASSED ==" ao final.
--
-- NOTAS:
--   - Roda em transação única com ROLLBACK ao final (idempotente, sem side effects).
--   - Usa vetores zero (all-zeros) pois o conteúdo semântico não é relevante
--     para o teste de isolamento — apenas o RLS importa.
--   - Os UUIDs dos tenants são determinísticos e diferentes dos da Fase 1
--     para evitar colisão em ambiente compartilhado.
-- =============================================================================

BEGIN;

-- ---------------------------------------------------------------------------
-- SETUP: fixtures para dois tenants
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    v_tenant_a UUID := 'cccccccc-0000-0000-0000-000000000001';  -- diferente dos da Fase 1
    v_tenant_b UUID := 'dddddddd-0000-0000-0000-000000000001';
    -- Vetor zero com dimensão 1024 (voyage-multilingual-2)
    v_zero_vec TEXT := '[' || repeat('0,', 1023) || '0]';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'adserver_app') THEN
        RAISE EXCEPTION
            'Role adserver_app não existe. Crie antes de rodar este teste.';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_extension WHERE extname = 'vector'
    ) THEN
        RAISE EXCEPTION
            'Extensão pgvector não instalada. Execute: CREATE EXTENSION vector;';
    END IF;

    -- Insere embeddings de criativos para tenant A e B (como superuser — bypass RLS)
    INSERT INTO vector_store.creative_embeddings
        (tenant_id, banner_id, campaign_id, creative_type, ctr,
         impression_count, description_snippet, embedding, embedding_model)
    VALUES
        (v_tenant_a, 101, 1001, 'image', 0.05, 10000,
         'Banner verão tenant A', v_zero_vec::vector, 'test-model'),
        (v_tenant_a, 102, 1001, 'html5', 0.08, 5000,
         'Banner HTML5 promoção tenant A', v_zero_vec::vector, 'test-model'),
        (v_tenant_b, 201, 2001, 'image', 0.03, 8000,
         'Banner inverno tenant B', v_zero_vec::vector, 'test-model');

    -- Insere docs de ajuda: 1 público + 1 privado de A + 1 privado de B
    INSERT INTO vector_store.help_doc_embeddings
        (tenant_id, doc_id, title, content, chunk_index, embedding, embedding_model)
    VALUES
        (NULL,        'help-001', 'Guia de Campanhas', 'Como criar campanhas...', 0,
         v_zero_vec::vector, 'test-model'),   -- doc público
        (v_tenant_a,  'help-a01', 'Guia Tenant A',     'Específico para tenant A...', 0,
         v_zero_vec::vector, 'test-model'),   -- doc privado A
        (v_tenant_b,  'help-b01', 'Guia Tenant B',     'Específico para tenant B...', 0,
         v_zero_vec::vector, 'test-model');   -- doc privado B

    RAISE NOTICE 'Setup vector RLS: tenant_a=% tenant_b=%', v_tenant_a, v_tenant_b;
END;
$$;

-- ---------------------------------------------------------------------------
-- Helper assert
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION pg_temp.vec_assert_count(
    p_label    TEXT,
    p_actual   BIGINT,
    p_expected BIGINT
) RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    IF p_actual <> p_expected THEN
        RAISE EXCEPTION 'ASSERT FALHOU [%]: esperado=% obtido=%',
            p_label, p_expected, p_actual;
    END IF;
    RAISE NOTICE 'PASS [%]: count=% (esperado=%)', p_label, p_actual, p_expected;
END;
$$;

-- ===========================================================================
-- BLOCO 1 — Tenant A vê apenas seus criativos (não os de B)
-- ===========================================================================
DO $$
DECLARE
    v_tenant_a UUID := 'cccccccc-0000-0000-0000-000000000001';
    v_count    BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'cccccccc-0000-0000-0000-000000000001';

    -- 1a. creative_embeddings: tenant A vê 2 criativos (os seus)
    SELECT COUNT(*) INTO v_count FROM vector_store.creative_embeddings;
    PERFORM pg_temp.vec_assert_count('creative_embeddings: tenant_a vê 2', v_count, 2);

    -- 1b. help_doc_embeddings: tenant A vê 2 docs (1 público + 1 privado de A)
    SELECT COUNT(*) INTO v_count FROM vector_store.help_doc_embeddings;
    PERFORM pg_temp.vec_assert_count('help_doc_embeddings: tenant_a vê 2 (1 público + 1 privado)', v_count, 2);

    -- 1c. Busca vetorial (cosine distance) retorna apenas criativos do tenant A.
    --     COUNT(*) precisa envolver um subselect: no mesmo nível, ORDER BY
    --     embedding <=> (...) LIMIT (vizinhos) colide com o agregado (a coluna
    --     embedding não-agrupada). E o literal precisa de parênteses ANTES do
    --     ::vector — o cast liga mais forte que ||, senão vira "malformed vector".
    SELECT COUNT(*) INTO v_count FROM (
        SELECT 1
        FROM vector_store.creative_embeddings
        ORDER BY embedding <=> ('[' || repeat('0.1,', 1023) || '0.1]')::vector
        LIMIT 10
    ) s;
    -- Há 2 criativos do tenant A; a busca vetorial (LIMIT 10) retorna no máximo 2
    PERFORM pg_temp.vec_assert_count('busca vetorial: tenant_a vê <= 2', v_count, 2);

    RESET ROLE;
END;
$$;

-- ===========================================================================
-- BLOCO 2 — Tenant B vê apenas seus criativos (não os de A)
-- ===========================================================================
DO $$
DECLARE
    v_tenant_b UUID := 'dddddddd-0000-0000-0000-000000000001';
    v_count    BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'dddddddd-0000-0000-0000-000000000001';

    -- 2a. creative_embeddings: tenant B vê 1 criativo (o seu)
    SELECT COUNT(*) INTO v_count FROM vector_store.creative_embeddings;
    PERFORM pg_temp.vec_assert_count('creative_embeddings: tenant_b vê 1', v_count, 1);

    -- 2b. help_doc_embeddings: tenant B vê 2 docs (1 público + 1 privado de B)
    SELECT COUNT(*) INTO v_count FROM vector_store.help_doc_embeddings;
    PERFORM pg_temp.vec_assert_count('help_doc_embeddings: tenant_b vê 2 (1 público + 1 privado)', v_count, 2);

    RESET ROLE;
END;
$$;

-- ===========================================================================
-- BLOCO 3 — Cross-tenant: tenant A NÃO lê embeddings de tenant B
-- ===========================================================================
DO $$
DECLARE
    v_tenant_b_uuid UUID := 'dddddddd-0000-0000-0000-000000000001';
    v_count         BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'cccccccc-0000-0000-0000-000000000001';

    -- 3a. Filtrando explicitamente pelo tenant_id de B — RLS bloqueia
    SELECT COUNT(*) INTO v_count
    FROM vector_store.creative_embeddings
    WHERE tenant_id = v_tenant_b_uuid;
    PERFORM pg_temp.vec_assert_count(
        'cross-tenant creative_embeddings: A não lê de B (filtro explícito)',
        v_count, 0
    );

    -- 3b. Busca vetorial não retorna embeddings de B mesmo sem filtro explícito
    SELECT COUNT(*) INTO v_count
    FROM vector_store.creative_embeddings
    WHERE description_snippet LIKE '%tenant B%';
    PERFORM pg_temp.vec_assert_count(
        'cross-tenant: A não vê snippet de B via busca por texto',
        v_count, 0
    );

    -- 3c. Doc privado de B não é visível para A
    SELECT COUNT(*) INTO v_count
    FROM vector_store.help_doc_embeddings
    WHERE tenant_id = v_tenant_b_uuid;
    PERFORM pg_temp.vec_assert_count(
        'cross-tenant help_docs: A não lê doc privado de B',
        v_count, 0
    );

    -- 3d. Doc público (NULL) é visível para A — comportamento esperado
    SELECT COUNT(*) INTO v_count
    FROM vector_store.help_doc_embeddings
    WHERE tenant_id IS NULL;
    PERFORM pg_temp.vec_assert_count(
        'doc público (NULL): visível para tenant A',
        v_count, 1
    );

    RESET ROLE;
END;
$$;

-- ===========================================================================
-- BLOCO 4 — Fail-closed: sem adserver.tenant_id → 0 linhas em creative_embeddings
-- ===========================================================================
DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = '';  -- sem tenant_id

    -- 4a. creative_embeddings: 0 linhas (current_tenant_id() retorna NULL → rejeita tudo)
    SELECT COUNT(*) INTO v_count FROM vector_store.creative_embeddings;
    PERFORM pg_temp.vec_assert_count(
        'fail-closed creative_embeddings (tenant_id vazio): 0',
        v_count, 0
    );

    -- 4b. help_doc_embeddings: apenas docs públicos (NULL IS NULL → TRUE)
    --     ATENÇÃO: docs públicos ficam acessíveis mesmo sem tenant_id.
    --     Isso é intencional — são docs de ajuda públicos do sistema.
    --     Se quiser bloquear completamente sem tenant_id, mudar a policy.
    SELECT COUNT(*) INTO v_count
    FROM vector_store.help_doc_embeddings
    WHERE tenant_id IS NULL;
    PERFORM pg_temp.vec_assert_count(
        'fail-closed help_docs: docs públicos ainda acessíveis (intencional)',
        v_count, 1
    );

    -- Docs privados NÃO são acessíveis sem tenant_id
    SELECT COUNT(*) INTO v_count
    FROM vector_store.help_doc_embeddings
    WHERE tenant_id IS NOT NULL;
    PERFORM pg_temp.vec_assert_count(
        'fail-closed help_docs: docs privados inacessíveis sem tenant_id',
        v_count, 0
    );

    RESET ROLE;
END;
$$;

-- ===========================================================================
-- BLOCO 5 — Superuser bypassa RLS (comportamento esperado para migrations)
-- ===========================================================================
DO $$
DECLARE
    v_count BIGINT;
BEGIN
    -- Sem SET ROLE: continua como adserver_admin (superuser)
    SELECT COUNT(*) INTO v_count FROM vector_store.creative_embeddings
    WHERE tenant_id IN (
        'cccccccc-0000-0000-0000-000000000001',
        'dddddddd-0000-0000-0000-000000000001'
    );
    PERFORM pg_temp.vec_assert_count(
        'superuser vê todos os criativos (bypass RLS)',
        v_count, 3
    );

    SELECT COUNT(*) INTO v_count FROM vector_store.help_doc_embeddings;
    PERFORM pg_temp.vec_assert_count(
        'superuser vê todos os docs (3: 1 público + 1 A + 1 B)',
        v_count, 3
    );
END;
$$;

-- ===========================================================================
-- BLOCO 6 — Busca vetorial com filtro de CTR respeitando RLS
-- Prova que WHERE ctr >= X não "vaza" criativos de outro tenant
-- ===========================================================================
DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'cccccccc-0000-0000-0000-000000000001';

    -- Tenant A com filtro ctr >= 0.02: seus DOIS criativos (0.05 e 0.08) qualificam.
    -- O criativo de B (ctr=0.03) TAMBÉM passaria o filtro por valor, mas o RLS o
    -- bloqueia — logo A vê 2, não 3. Isso prova que é o RLS (não o filtro de ctr)
    -- que isola B. (O assert antigo esperava 1 assumindo, errado, que só o 0.08
    -- qualificava — o criativo image de A tem ctr=0.05, também >= 0.04.)
    SELECT COUNT(*) INTO v_count
    FROM vector_store.creative_embeddings
    WHERE ctr >= 0.02;
    PERFORM pg_temp.vec_assert_count(
        'busca com min_ctr=0.02: tenant A vê seus 2 criativos (B bloqueado pelo RLS)',
        v_count, 2
    );

    RESET ROLE;
END;
$$;

-- ===========================================================================
-- BLOCO 7 — WITH CHECK: um tenant NÃO grava linha de outro tenant nem doc
--   "público" (tenant_id NULL). Os BLOCOS 1–3 provam só a LEITURA; este prova
--   a ESCRITA. Sem o WITH CHECK explícito, a policy do help_doc (com o ramo
--   `tenant_id IS NULL` no USING) deixaria um tenant INSERIR um doc público
--   que todos leem — envenenamento do corpus RAG. Cada tentativa deve abortar
--   com 42501; cada probe roda num sub-bloco (savepoint), então a rejeição
--   esperada não derruba a transação de teste.
-- ===========================================================================
DO $$
DECLARE
    v_zero_vec TEXT := '[' || repeat('0,', 1023) || '0]';
    v_tenant_b UUID := 'dddddddd-0000-0000-0000-000000000001';
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'cccccccc-0000-0000-0000-000000000001';

    -- 7a. help_doc "público" forjado (tenant_id NULL) por um tenant → rejeitado
    BEGIN
        INSERT INTO vector_store.help_doc_embeddings
            (tenant_id, doc_id, title, content, chunk_index, embedding, embedding_model)
        VALUES (NULL, 'help-forjado', 'Forjado', 'x', 0, v_zero_vec::vector, 'test-model');
        RAISE EXCEPTION
            'ASSERT FALHOU [WITH CHECK help_doc NULL]: doc público por tenant ACEITO (esperado 42501)';
    EXCEPTION WHEN insufficient_privilege THEN
        RAISE NOTICE 'PASS [WITH CHECK: help_doc público (tenant_id NULL) forjado rejeitado]: %', SQLSTATE;
    END;

    -- 7b. help_doc com tenant_id de B → rejeitado
    BEGIN
        INSERT INTO vector_store.help_doc_embeddings
            (tenant_id, doc_id, title, content, chunk_index, embedding, embedding_model)
        VALUES (v_tenant_b, 'help-b-forjado', 'Forjado B', 'x', 0, v_zero_vec::vector, 'test-model');
        RAISE EXCEPTION
            'ASSERT FALHOU [WITH CHECK help_doc B]: INSERT cross-tenant ACEITO (esperado 42501)';
    EXCEPTION WHEN insufficient_privilege THEN
        RAISE NOTICE 'PASS [WITH CHECK: help_doc do tenant B rejeitado]: %', SQLSTATE;
    END;

    -- 7c. creative com tenant_id de B → rejeitado
    BEGIN
        INSERT INTO vector_store.creative_embeddings
            (tenant_id, banner_id, campaign_id, creative_type, ctr,
             impression_count, description_snippet, embedding, embedding_model)
        VALUES (v_tenant_b, 999, 9999, 'image', 0.01, 1, 'forjado',
                v_zero_vec::vector, 'test-model');
        RAISE EXCEPTION
            'ASSERT FALHOU [WITH CHECK creative B]: INSERT cross-tenant ACEITO (esperado 42501)';
    EXCEPTION WHEN insufficient_privilege THEN
        RAISE NOTICE 'PASS [WITH CHECK: creative do tenant B rejeitado]: %', SQLSTATE;
    END;

    RESET ROLE;
END;
$$;

-- ===========================================================================
-- ROLLBACK: não persiste dados de teste
-- ===========================================================================
ROLLBACK;

\echo '== VECTOR RLS ISOLATION: ALL TESTS PASSED =='
