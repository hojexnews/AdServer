-- =============================================================================
-- 0001_vector_schema_up.sql
-- Schema de embeddings pgvector para o RAG do copiloto (J5/Fase 2).
--
-- ESCOPO DO RAG (§2.4 / ADR-0003 §C):
--   pgvector é APENAS para:
--     1. "Criativos similares por CTR" — creative_embeddings
--     2. "Docs de ajuda" — help_doc_embeddings
--
--   Catálogo e taxonomia de regras §4.6 vão DIRETO no contexto
--   com prompt caching — NÃO estão aqui (cabem em 1M tokens).
--
-- RLS POR TENANT (TX-3):
--   Toda tabela vetorial tem tenant_id + RLS habilitado.
--   A policy usa a mesma função config.current_tenant_id() da Fase 1.
--   Teste de isolamento: db/vector/tests/vector_rls_isolation_test.sql
--
-- EMBEDDINGS:
--   Provedor: Voyage AI (voyage-multilingual-2) — multilíngue PT-BR.
--   Alternativa: Cohere Embed v3 (embed-multilingual-v3.0).
--   Dimensão: 1024 (voyage-multilingual-2 default).
--   Índice: HNSW (Hierarchical Navigable Small World) — melhor recall/latência
--           para buscas de vizinhos mais próximos em produção.
--           IVFFlat como alternativa para datasets maiores com build time menor.
--
-- DEPENDÊNCIA:
--   - Extensão pgvector deve estar instalada: CREATE EXTENSION IF NOT EXISTS vector;
--   - Schema config deve existir (0001_config_schema_up.sql da Fase 1).
--   - Função config.current_tenant_id() deve existir (0002_config_rls_up.sql).
--
-- MIGRAÇÃO: golang-migrate (compatível com db/config/).
-- float PROIBIDO em colunas monetárias (CA-7/TX-2). CTR é sinal de ML, não
-- dinheiro — float é legítimo aqui (mesma lógica de propensity no decision.proto).
-- =============================================================================

-- ---------------------------------------------------------------------------
-- Pré-requisito: extensão pgvector
-- ---------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS vector;

-- ---------------------------------------------------------------------------
-- Schema dedicado ao RAG do copiloto
-- ---------------------------------------------------------------------------
CREATE SCHEMA IF NOT EXISTS vector_store;

-- ---------------------------------------------------------------------------
-- Tabela 1: creative_embeddings
-- "Criativos similares por CTR" (§2.4)
--
-- Armazena embeddings dos criativos (banner) com CTR histórico.
-- O copiloto busca criativos com alto CTR similares ao objetivo da campanha.
-- RLS por tenant_id: um tenant NUNCA vê embeddings de outro (TX-3).
-- ---------------------------------------------------------------------------
CREATE TABLE vector_store.creative_embeddings (
    id              BIGSERIAL       NOT NULL,
    tenant_id       UUID            NOT NULL,       -- RLS: isolamento por tenant
    banner_id       BIGINT          NOT NULL,       -- FK lógica para config.banners
    campaign_id     BIGINT          NOT NULL,       -- FK lógica para config.campaigns

    -- Tipo do criativo (espelha config.creative_type para filtragem rápida)
    creative_type   TEXT            NOT NULL
                        CHECK (creative_type IN ('image', 'html5', 'thirdparty_tag', 'video')),

    -- CTR histórico do criativo. FLOAT: sinal de ML/ranking, NÃO dinheiro (TX-2).
    -- Atualizado periodicamente pelo pipeline de ML (J2/J6).
    ctr             DOUBLE PRECISION NOT NULL DEFAULT 0.0  -- no-float-ok: taxa de ML/ranking [0,1], não é dinheiro (TX-2)
                        CHECK (ctr >= 0.0 AND ctr <= 1.0),

    -- Impressões totais (para filtrar criativos sem volume suficiente)
    impression_count BIGINT          NOT NULL DEFAULT 0,

    -- Trecho do texto do criativo para exibição no resultado da busca
    -- SEM PII — apenas copy/CTA do criativo (TX-5/DA-11)
    description_snippet TEXT        NOT NULL DEFAULT '',

    -- Vetor de embedding multilíngue PT-BR
    -- Dimensão: 1024 (voyage-multilingual-2) — deve coincidir com EMBEDDING_DIMENSIONS
    embedding       vector(1024)    NOT NULL,

    -- Versão do modelo de embedding (rastreabilidade — mudança de modelo invalida cache)
    embedding_model TEXT            NOT NULL DEFAULT 'voyage-multilingual-2',

    -- Metadados de rastreamento
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),

    CONSTRAINT creative_embeddings_pk PRIMARY KEY (id)
);

-- Índice HNSW para busca de vizinhos mais próximos por distância cosseno.
-- Parâmetros HNSW:
--   m=16: conectividade do grafo (padrão pgvector; 16–64 para alta recall)
--   ef_construction=64: tamanho do conjunto de candidatos na construção
-- Ajustar m e ef_construction com benchmarks em produção (trade-off build_time/recall).
CREATE INDEX creative_embeddings_hnsw_idx
    ON vector_store.creative_embeddings
    USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

-- Índice por tenant para filtro RLS eficiente
CREATE INDEX creative_embeddings_tenant_idx
    ON vector_store.creative_embeddings (tenant_id);

-- Índice por creative_type para filtragem rápida
CREATE INDEX creative_embeddings_type_idx
    ON vector_store.creative_embeddings (creative_type);

-- Índice por CTR para filtragem por min_ctr
CREATE INDEX creative_embeddings_ctr_idx
    ON vector_store.creative_embeddings (ctr DESC);

COMMENT ON TABLE vector_store.creative_embeddings IS
    'Embeddings de criativos (banner) para busca de "similares por CTR". '
    'RAG escopado — apenas criativos + docs de ajuda. '
    'Regras §4.6 vão direto no contexto (prompt caching). RLS por tenant_id (TX-3).';

COMMENT ON COLUMN vector_store.creative_embeddings.embedding IS
    'Vetor de embedding multilíngue PT-BR (voyage-multilingual-2, dim=1024). '
    'Busca por distância cosseno via HNSW. Nunca contém PII (TX-5).';

COMMENT ON COLUMN vector_store.creative_embeddings.ctr IS
    'CTR histórico [0,1]. FLOAT legítimo: sinal de ML/ranking, não dinheiro (TX-2/decision.proto §PRECISAO).';

-- ---------------------------------------------------------------------------
-- Tabela 2: help_doc_embeddings
-- "Docs de ajuda" para o copiloto (§2.4)
--
-- Armazena embeddings de documentação do sistema (FAQs, guias).
-- Tenant-agnostic (docs públicos), mas com RLS por consistência arquitetural.
-- ---------------------------------------------------------------------------
CREATE TABLE vector_store.help_doc_embeddings (
    id              BIGSERIAL       NOT NULL,
    -- tenant_id = NULL indica doc público (todos os tenants podem ver)
    -- tenant_id != NULL indica doc específico do tenant
    tenant_id       UUID,                           -- NULL = doc público

    -- Identificador único do documento de ajuda
    doc_id          TEXT            NOT NULL,
    title           TEXT            NOT NULL,
    -- Conteúdo completo do chunk (para resposta ao usuário)
    content         TEXT            NOT NULL,
    -- Posição do chunk no documento original (para ordenação)
    chunk_index     INTEGER         NOT NULL DEFAULT 0,

    -- Vetor de embedding
    embedding       vector(1024)    NOT NULL,
    embedding_model TEXT            NOT NULL DEFAULT 'voyage-multilingual-2',

    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),

    CONSTRAINT help_doc_embeddings_pk PRIMARY KEY (id)
);

CREATE INDEX help_doc_embeddings_hnsw_idx
    ON vector_store.help_doc_embeddings
    USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

CREATE INDEX help_doc_embeddings_tenant_idx
    ON vector_store.help_doc_embeddings (tenant_id);

CREATE INDEX help_doc_embeddings_doc_idx
    ON vector_store.help_doc_embeddings (doc_id);

COMMENT ON TABLE vector_store.help_doc_embeddings IS
    'Embeddings de documentação de ajuda do sistema. '
    'tenant_id NULL = doc público. RLS por tenant_id (TX-3). '
    'Não contém dados de campanha nem PII.';

-- ---------------------------------------------------------------------------
-- Triggers de updated_at
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION vector_store.set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER creative_embeddings_updated_at_trg
    BEFORE UPDATE ON vector_store.creative_embeddings
    FOR EACH ROW EXECUTE FUNCTION vector_store.set_updated_at();

CREATE TRIGGER help_doc_embeddings_updated_at_trg
    BEFORE UPDATE ON vector_store.help_doc_embeddings
    FOR EACH ROW EXECUTE FUNCTION vector_store.set_updated_at();
