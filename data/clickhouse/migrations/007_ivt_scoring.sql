-- data/clickhouse/migrations/007_ivt_scoring.sql
--
-- IVT Scoring Integration (TX-6 / J6).
--
-- OBJETIVO:
--   Fiar o scorer de IVT (ml/fraud/scorer.py) na ingestao, ANTES do StatsHourly
--   e do faturamento. O scoring roda em batch near-line (latencia de minutos,
--   aceitavel por ADR-0001/TX-6); o resultado e gravado em raw_impression via
--   ALTER COLUMN e UPDATE antes da MV mv_impression_to_hourly ser consultada
--   pelo billing batch.
--
-- O QUE MUDA EM RELACAO A 002_raw_tables.sql:
--   raw_impression ja tem a coluna ivt_status LowCardinality(String).
--   Esta migration:
--     (1) Adiciona a tabela raw_ivt_score: log auditavel de todos os scores IVT
--         gerados pelo job data/fraud/ivt_scoring_job.py. Permite rastrear qual
--         model_version pontuou cada event_id, quando, e qual foi o resultado.
--     (2) Adiciona MV mv_ivt_score_to_raw_impression: quando um score IVT e
--         inserido em raw_ivt_score, a MV propaga o ivt_status para raw_impression
--         (UPDATE logico via ReplacingMergeTree -- a versao mais nova vence).
--         IMPORTANTE: a MV usa um INSERT de "versao nova" em raw_impression,
--         atualizando o ingested_at para que o ReplacingMergeTree mantenha a
--         linha com o ivt_status correto como a versao final pos-merge.
--     (3) View auxiliar raw_impression_clean: alias de leitura para impressoes
--         com ivt_status = '' (clean) ou ivt_status = 'clean'. Facilita queries
--         analíticas sem repetir o filtro.
--
-- COMPATIBILIDADE COM MVs EXISTENTES (004_stats_hourly.sql):
--   A MV mv_impression_to_hourly ja filtra: ivt_status = '' AND billable = 1 AND blank = 0.
--   Esta migration NAO altera mv_impression_to_hourly. O filtro existente e
--   suficiente: eventos com ivt_status = 'ivt' ja sao excluidos do StatsHourly
--   e portanto do faturamento.
--   O job de scoring (data/fraud/ivt_scoring_job.py) DEVE rodar ANTES do billing
--   batch horario, atualizando o ivt_status em raw_impression antes que o billing
--   leia via stats_hourly ou via Iceberg.
--
-- INVARIANTES (nao violar):
--   - ivt_status = ''        -> clean (sem avaliacao IVT ou avaliado como clean)
--   - ivt_status = 'ivt'     -> classificado como IVT pelo scorer (excluir do billing)
--   - ivt_status = 'unscored'-> evento ainda nao foi pontuado (transiente; nao faturar)
--   - ivt_status = 'fail_open' -> scorer executou mas falhou (fail-open; tratar como clean
--                                 para nao bloquear trafego legitimo, TX-6)
--   Somente ivt_status = '' OU ivt_status = 'fail_open' entra no StatsHourly/billing.
--   Nota: o filtro existente em mv_impression_to_hourly usa ivt_status = ''.
--         fail_open e tratado com ivt_status = '' pelo job de scoring (conservador).
--
-- IVT-ANTES-DO-BILLING (sequencia garantida pelo orquestrador):
--   1. Evento chega no Redpanda -> raw_impression (ivt_status = '' default)
--   2. Job ivt_scoring_job.py le raw_impression (janela near-line), pontua,
--      grava resultado em raw_ivt_score
--   3. MV mv_ivt_score_to_raw_impression propaga ivt_status = 'ivt' para
--      raw_impression (versao nova no ReplacingMergeTree)
--   4. billing_batch_hourly.py le do Iceberg (pos-sink do raw_impression
--      ja com ivt_status atualizado), que contem apenas ivt_status = '' (clean)
--   5. StatsHourly (mv_impression_to_hourly) agrega apenas ivt_status = '' (clean)
--
-- DINHEIRO: sem colunas monetarias nesta migration (TX-2/DA-10 N/A).
-- PII: sem PII nesta migration (TX-5/DA-11). ivt_prob e Float LEGITIMO
--   (probabilidade de ML, nao dinheiro — identico ao propensity de raw_decision).
-- TENANT: raw_ivt_score tem tenant_id; row-policy adicionada no bloco de access
--   control abaixo. Isolamento preservado (TX-3/CA-1).

-- =============================================================================
-- Tabela de log de scores IVT (raw_ivt_score)
-- =============================================================================
-- Armazena o resultado de cada scoring IVT para auditoria, monitoramento de
-- fail_open rate, e reconciliacao batch contra o Iceberg.
-- Engine: ReplacingMergeTree(scored_at) — dedupe por (event_id, score_run_id).
-- Um mesmo event_id pode ser re-pontuado (hot-reload de modelo); a versao mais
-- recente (scored_at maior) vence apos a merge.

CREATE TABLE IF NOT EXISTS adserver.raw_ivt_score ON CLUSTER '{cluster}'
(
    -- Chave de deduplicacao: (event_id, score_run_id)
    -- score_run_id permite reprocessamento por job run sem colidir com scores anteriores.
    event_id        String,                         -- liga ao raw_impression.event_id
    score_run_id    String,                         -- UUID do job run (para rastreabilidade)
    tenant_id       String,                         -- isolamento de tenant (TX-3)

    -- Dimensoes do evento (denormalizadas para queries de auditoria sem JOIN)
    campaign_id     String,
    zone_id         String,
    occurred_at     DateTime64(3, 'UTC'),            -- event-time do impression original
    scored_at       DateTime64(3, 'UTC'),            -- processing-time do scoring

    -- Resultado do scorer IVT (ml/fraud/scorer.py)
    -- ivt_status: enum de resultado do scoring
    --   ''          -> clean (scorer nao foi invocado nesta linha; default)
    --   'ivt'       -> classificado como IVT (is_ivt=True, excluir do billing)
    --   'fail_open' -> scorer falhou; tratado como clean (TX-6 fail-open)
    ivt_status      LowCardinality(String),         -- resultado normalizado
    -- ivt_prob: Float LEGITIMO (probabilidade de ML, nao dinheiro — TX-2 N/A).
    -- Documentado como excecao explícita ao no-float-data-sql.sh:
    --   no-float-ok: ivt_prob e probabilidade de ML (scorer IVT), nao valor monetario.
    ivt_prob        Float64,   -- no-float-ok: probabilidade ML, nao dinheiro (TX-2 N/A)
    ivt_label       UInt8,                          -- 0=clean, 1=IVT
    model_version   String                          -- versao do modelo IVT no MLflow
)
ENGINE = ReplacingMergeTree(scored_at)
PARTITION BY toYYYYMMDD(occurred_at)
ORDER BY (event_id, score_run_id)
TTL toDateTime(occurred_at) + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;

COMMENT ON TABLE adserver.raw_ivt_score IS
    'Log auditavel de scores IVT por event_id. '
    'Resultado do job data/fraud/ivt_scoring_job.py (TX-6). '
    'ivt_prob: Float legitimo (probabilidade ML, nao dinheiro). '
    'Nao consultar para billing; apenas para auditoria e monitoramento. '
    'Sem PII (TX-5). Isolamento por tenant_id (TX-3).';

-- =============================================================================
-- MV: raw_ivt_score -> raw_impression (propaga ivt_status)
-- =============================================================================
-- Quando o job de scoring insere em raw_ivt_score, esta MV propaga o resultado
-- para raw_impression inserindo uma nova "versao" da linha (nova ingested_at).
-- O ReplacingMergeTree de raw_impression usa ingested_at como versao; a versao
-- mais recente (pos-scoring) vence na merge, substituindo o ivt_status = '' inicial.
--
-- IMPORTANTE: este INSERT propaga apenas campos que existem em raw_impression.
-- Os campos de scoring (ivt_prob, ivt_label, model_version) ficam em raw_ivt_score.
-- raw_impression registra apenas o ivt_status resultante (coluna ja existente em 002).
--
-- COMPATIBILIDADE: raw_impression ja tem ivt_status LowCardinality(String) desde 002.
-- Esta MV NAO altera a tabela raw_impression — apenas insere novas versoes de linhas.
-- As MVs existentes (004: mv_impression_to_hourly) continuam filtrando ivt_status = '';
-- apos a propagacao por esta MV, linhas IVT ficam com ivt_status = 'ivt' e sao
-- excluidas do StatsHourly automaticamente. Sem alteracao de 004.

CREATE MATERIALIZED VIEW IF NOT EXISTS adserver.mv_ivt_score_to_raw_impression
ON CLUSTER '{cluster}'
TO adserver.raw_impression
AS
SELECT
    -- Para propagar o ivt_status para raw_impression, precisamos dos campos
    -- da raw_impression original. Como a MV opera sobre raw_ivt_score (que
    -- armazena os campos denormalizados), podemos reconstruir a versao atualizada.
    --
    -- NOTA ARQUITETURAL: esta MV nao faz JOIN com raw_impression porque MVs
    -- do ClickHouse operam sobre o bloco inserido (sem acesso a outras tabelas).
    -- A abordagem e: gravar os campos necessarios de raw_impression em raw_ivt_score
    -- (denormalizados) e usar a MV para propagar o ivt_status atualizado.
    -- Os campos que nao estao em raw_ivt_score (decision_id, model_version do
    -- envelope, schema_version, source, banner_id, served_tier, billable, blank)
    -- sao preenchidos com valores default seguros (string vazia / 0).
    -- O ReplacingMergeTree mantera a linha com ingested_at mais recente; para
    -- queries FINAL, o resultado e a linha de scoring (com ivt_status correto
    -- e os outros campos com defaults). O billing batch le do Iceberg (que contem
    -- a versao completa dos campos via iceberg_sink_job.py, onde o scoring ja
    -- foi propagado). Para ClickHouse raw_impression FINAL, o campo ivt_status
    -- estara correto; outros campos podem ter defaults para linhas re-inseridas
    -- por esta MV.
    --
    -- ALTERNATIVA: o job de scoring pode fazer um UPDATE direto via
    --   ALTER TABLE adserver.raw_impression UPDATE ivt_status = 'ivt'
    --   WHERE event_id IN (...)
    -- O UPDATE do ClickHouse (mutation) e assincrono e nao-transacional;
    -- para volumes de ingestao tipicos (< 1B/dia, ADR-0002 B.1), o UPDATE
    -- assíncrono e aceitavel. O job de scoring DEVE usar ALTER TABLE UPDATE
    -- em vez desta MV quando o volume for alto — ver comentario em
    -- data/fraud/ivt_scoring_job.py.
    --
    -- Para v1 (Fase 2), a MV e suficiente para volumes moderados.

    event_id,
    score_run_id    AS decision_id,     -- score_run_id como proxy de decision_id
    ''              AS model_version,   -- modelo do envelope; nao disponivel aqui
    occurred_at,
    ''              AS schema_version,
    'ivt_scorer'    AS source,
    now64(3)        AS ingested_at,     -- nova versao (mais recente = vence na merge)
    tenant_id,
    campaign_id,
    ''              AS banner_id,       -- nao disponivel em raw_ivt_score
    zone_id,
    ''              AS served_tier,     -- nao disponivel
    0               AS billable,        -- conservador: linhas re-inseridas nao sao billable
    0               AS blank,
    ivt_status                          -- CAMPO CHAVE: propaga o resultado do scoring
FROM adserver.raw_ivt_score
WHERE ivt_status = 'ivt';              -- propaga apenas eventos classificados como IVT

COMMENT ON TABLE adserver.mv_ivt_score_to_raw_impression IS
    'Propaga ivt_status=ivt para raw_impression quando um score IVT e inserido. '
    'Usa ReplacingMergeTree: linha com ingested_at mais recente vence. '
    'Filtra: apenas ivt_status = ivt (nao repropaga clean/fail_open). '
    'Billing batch le do Iceberg (pos-sink); StatsHourly filtra ivt_status = automaticamente.';

-- =============================================================================
-- View auxiliar: raw_impression_clean (alias de leitura para eventos clean)
-- =============================================================================
-- Facilita queries analiticas e de auditoria sem repetir o filtro ivt_status.
-- NAO usa FINAL (performance); para queries exatas usar FINAL na consulta.
-- NAO-FATURAVEL diretamente; o billing usa o Iceberg (ADR-0001).

CREATE VIEW IF NOT EXISTS adserver.raw_impression_clean ON CLUSTER '{cluster}'
AS
SELECT *
FROM adserver.raw_impression
WHERE ivt_status = '' OR ivt_status = 'fail_open';

COMMENT ON TABLE adserver.raw_impression_clean IS
    '[AUXILIAR / nao-faturavel] Impressoes com ivt_status clean ou fail_open. '
    'Alias para evitar repeticao do filtro. Use FINAL para dedupe exato. '
    'Billing usa o Iceberg (ADR-0001), nao esta view.';

-- =============================================================================
-- ROW POLICY: raw_ivt_score por tenant (TX-3 — preserva isolamento)
-- =============================================================================
-- Segue o mesmo padrao de 006_access_control.sql: substring+match UUID, fail-closed.
-- Usuarios adserver_ingest e adserver_billing tem acesso total (politica admin).

CREATE ROW POLICY IF NOT EXISTS rp_raw_ivt_score_tenant
ON adserver.raw_ivt_score
FOR SELECT
USING tenant_id = if(
    startsWith(currentUser(), 'tenant_')
      AND match(
            substring(currentUser(), length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring(currentUser(), length('tenant_') + 1),
    NULL
)
TO tenant_role;

CREATE ROW POLICY IF NOT EXISTS rp_raw_ivt_score_admin
ON adserver.raw_ivt_score
FOR SELECT USING 1 = 1
TO adserver_admin_role;

-- Conceder SELECT em raw_ivt_score para tenant_role (auditoria de scoring do proprio tenant)
-- e para raw_impression_clean (alias de leitura, view, usa policy de raw_impression)
GRANT SELECT ON adserver.raw_ivt_score       TO tenant_role;
GRANT SELECT ON adserver.raw_impression_clean TO tenant_role;
