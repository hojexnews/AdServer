-- data/clickhouse/migrations/008_ivt_unsup_scoring.sql
--
-- IVT Scoring Nao-Supervisionado (K2 / Fase 3 / ADR-0004 §B).
--
-- OBJETIVO:
--   Adicionar tabela de log auditavel para os scores dos modelos nao-supervisionados
--   (Isolation Forest + Autoencoder) introduzidos no K2 da Fase 3.
--   Complementa a tabela raw_ivt_score (007), que registra a decisao supervisionada.
--
-- O QUE ESTA MIGRATION CRIA (apenas ADICIONA — NAO edita a 007):
--   (1) raw_ivt_unsup_score: log auditavel dos scores nao-supervisionados por evento.
--       Permite calibracao de limiares (ae_threshold, if_threshold) em producao,
--       auditoria de qual modelo detectou anomalia, e monitoramento de fail_open.
--
-- O QUE NAO MUDA EM RELACAO AO ESTADO ATUAL:
--   - raw_impression:         INALTERADA (nao adiciona colunas, nao altera schema).
--   - raw_ivt_score (007):    INALTERADA (tabela e MV existentes preservadas).
--   - mv_impression_to_hourly (004): INALTERADA.
--   - StatsHourly:            INALTERADO.
--   - Semantica de ivt_status: INALTERADA ('' = clean, 'ivt' = IVT).
--
-- SEMANTICA IVT-ANTES-DO-FATURAMENTO (TX-6):
--   A decisao final de IVT continua sendo gravada em raw_ivt_score (007)
--   pela combinacao OR (supervisionado OR nao-supervisionado) no job K2.
--   A MV 007 (mv_ivt_score_to_raw_impression) continua propagando ivt_status='ivt'
--   para raw_impression. O StatsHourly e o faturamento NAO sao afetados por esta
--   migration — o filtro mv_impression_to_hourly usa ivt_status='' (004, inalterado).
--
-- COMBINACAO DE SCORES (logica OR, data/fraud/ivt_unsup_scoring_job.py):
--   is_ivt_final = is_ivt_supervisionado OR is_unsup_ivt
--   A decisao final e gravada em raw_ivt_score (model_version inclui ambos os scorers).
--   Este arquivo registra os scores detalhados do nao-supervisionado para auditoria.
--
-- DINHEIRO (TX-2/DA-10):
--   Sem colunas monetarias nesta migration.
--   if_score, ae_error, ae_threshold sao floats de ML (metricas de modelo,
--   nao valores monetarios) — mesma excecao documentada de ivt_prob em 007.
--
-- PII (TX-5/DA-11):
--   Sem PII nesta migration. Os campos replicam os da raw_ivt_score (007):
--   tenant_id (TX-3), event_id, campaign_id, zone_id — todos anonimizados.
--
-- TENANT (TX-3/CA-1):
--   row-policy por tenant_id, identica ao padrao de 006 e 007.
--   adserver_ingest e adserver_billing tem acesso total (politica admin).

-- =============================================================================
-- Tabela de log de scores nao-supervisionados (raw_ivt_unsup_score)
-- =============================================================================
-- Armazena o resultado detalhado do scoring nao-supervisionado (IF + AE) por evento.
-- Usado para: auditoria, calibracao de limiares, monitoramento de fail_open rate.
--
-- Engine: ReplacingMergeTree(scored_at) — dedupe por (event_id, score_run_id).
-- Um mesmo event_id pode ser re-pontuado (hot-reload de modelo); a versao mais
-- recente (scored_at maior) vence apos a merge.
--
-- NAO usar para billing; usar raw_ivt_score (007) para a decisao final de IVT.

CREATE TABLE IF NOT EXISTS adserver.raw_ivt_unsup_score ON CLUSTER '{cluster}'
(
    -- Chave de deduplicacao: (event_id, score_run_id)
    event_id        String,                         -- liga ao raw_impression.event_id
    score_run_id    String,                         -- UUID do job run (rastreabilidade)
    tenant_id       String,                         -- isolamento de tenant (TX-3)

    -- Dimensoes do evento (denormalizadas para queries de auditoria sem JOIN)
    campaign_id     String,
    zone_id         String,
    occurred_at     DateTime64(3, 'UTC'),            -- event-time do impression original
    scored_at       DateTime64(3, 'UTC'),            -- processing-time do scoring

    -- Resultado do scorer nao-supervisionado (ml/fraud/unsup_scorer.py)
    is_unsup_ivt    UInt8,                          -- 1=anomalia detectada por IF ou AE
    if_label        Int8,                           -- 1=normal, -1=anomalia (IsolationForest)

    -- Scores de ML (Floats LEGITIMOS: metricas de modelo ML, nao dinheiro — TX-2 N/A).
    -- Documentados como excecao ao no-float-data-sql.sh:
    --   no-float-ok: if_score e ae_error sao metricas de ML, nao valores monetarios.
    if_score        Float64,   -- no-float-ok: anomaly score do IsolationForest
    ae_error        Float64,   -- no-float-ok: erro MSE do autoencoder
    ae_threshold    Float64,   -- no-float-ok: limiar de anomalia do autoencoder

    -- Metadados do modelo
    model_version   String,                         -- versao do modelo nao-supervisionado

    -- Decisao combinada (supervisionado OR nao-supervisionado)
    combined_is_ivt UInt8                           -- 1=IVT final (OR dos dois scorers)
)
ENGINE = ReplacingMergeTree(scored_at)
PARTITION BY toYYYYMMDD(occurred_at)
ORDER BY (event_id, score_run_id)
TTL toDateTime(occurred_at) + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;

COMMENT ON TABLE adserver.raw_ivt_unsup_score IS
    'Log auditavel de scores nao-supervisionados (IsolationForest + Autoencoder) por event_id. '
    'Resultado do job data/fraud/ivt_unsup_scoring_job.py (K2 / Fase 3 / TX-6). '
    'if_score, ae_error, ae_threshold: Floats legitimos (metricas ML, nao dinheiro). '
    'NAO usar para billing; usar raw_ivt_score (007) para decisao final de IVT. '
    'Sem PII (TX-5). Isolamento por tenant_id (TX-3). '
    'GNN (futuro): tabela comporta model_version com prefixo "gnn:" quando ativado.';

-- =============================================================================
-- ROW POLICY: raw_ivt_unsup_score por tenant (TX-3 — preserva isolamento)
-- =============================================================================
-- Segue identicamente o padrao de 006_access_control.sql e 007_ivt_scoring.sql:
-- substring + match UUID, fail-closed para usuarios que nao sao tenant_{uuid}.
-- Usuarios adserver_ingest e adserver_billing tem acesso total (politica admin).

CREATE ROW POLICY IF NOT EXISTS rp_raw_ivt_unsup_score_tenant
ON adserver.raw_ivt_unsup_score
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

CREATE ROW POLICY IF NOT EXISTS rp_raw_ivt_unsup_score_admin
ON adserver.raw_ivt_unsup_score
FOR SELECT USING 1 = 1
TO adserver_admin_role;

-- Conceder SELECT em raw_ivt_unsup_score para tenant_role
-- (permite auditoria do scoring nao-supervisionado do proprio tenant)
GRANT SELECT ON adserver.raw_ivt_unsup_score TO tenant_role;
