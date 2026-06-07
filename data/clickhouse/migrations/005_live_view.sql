-- data/clickhouse/migrations/005_live_view.sql
--
-- Visao "ao vivo" (live stats) — NAO-FATURAVEL, janela curta (segundos-minutos).
--
-- =============================================================================
-- AVISO NORMATIVO (ADR-0001):
-- =============================================================================
--   Esta VIEW representa dados NAO-FATURAVEIS, derivados diretamente das
--   tabelas raw_* (sem rollup horario).
--
--   REGRAS INVIOLAVEIS:
--     1. O BFF/frontend DEVE rotular estes dados como "ao vivo" (nao-consolidado).
--     2. Estes numeros NUNCA devem ser somados com stats_hourly (consolidado <=1h).
--     3. Estes numeros NUNCA devem ser usados para faturamento ou para reconciliacao
--        contabil. A fonte de verdade contabil e o lakehouse Iceberg (billing batch).
--     4. Frescor esperado: segundos a poucos minutos (latencia de ingestao do
--        Kafka engine + processamento da MV raw, sem Flink).
--     5. Podem divergir temporariamente do consolidado (ADR-0001 § consequencias):
--        aceito e sinalizado pela regra de UI.
--
--   O contrato de saida com o BFF (frontend-bff-engineer) exige que a resposta
--   da API diferencie claramente os dois contextos:
--     - "source": "live"         -> dados desta VIEW (ao_vivo)
--     - "source": "consolidated" -> dados de stats_hourly (faturavel)
--   E que a UI exiba um label visivel ("ao vivo" vs "consolidado <=1h").
-- =============================================================================
--
-- IMPLEMENTACAO:
--   VIEW simples sobre raw_impression, raw_click, raw_conversion, raw_ad_request.
--   Janela: ultimos N minutos configuravel (padrao: 30 minutos).
--   A VIEW nao usa AggregatingMergeTree nem estados de agregacao; conta
--   event_ids distintos diretamente das raw tables (com FINAL para dedupe).
--
--   Para dashboards de "ao vivo" em tempo real, o BFF consulta esta VIEW com
--   um filtro de janela (ex.: occurred_at >= now() - INTERVAL 30 MINUTE).
--   O BFF e responsavel por parametrizar a janela; a VIEW nao a fixa.
--
-- IMPORTANTE SOBRE 'FINAL':
--   O uso de FINAL garante dedupe antes da merge background do ReplacingMergeTree.
--   Em alto throughput, FINAL pode ser custoso; para latencia critica de UI,
--   o BFF pode omitir FINAL e aceitar contagem com duplicatas transientes
--   (valor superestimado brevemente, nao-faturavel de qualquer forma).
--   A decisao de usar/omitir FINAL e do BFF; a VIEW expoe ambas as formas
--   via parametro de query ou via duas VIEWs (live_stats_exact / live_stats_fast).

-- =============================================================================
-- live_stats_exact: com FINAL (dedupe garantido, mais lento)
-- =============================================================================
-- NAO-FATURAVEL. Ver aviso normativo acima.
CREATE VIEW IF NOT EXISTS adserver.live_stats_exact ON CLUSTER '{cluster}'
AS
SELECT
    toStartOfMinute(r.occurred_at)  AS minute_bucket,
    r.tenant_id,
    r.campaign_id,
    r.banner_id,
    r.zone_id,
    'live'                          AS data_source,  -- ROTULO OBRIGATORIO (ADR-0001)
    -- requests: contados da raw_ad_request (pre-decisao, sem campaign/banner)
    -- NOTA: requests sao da raw_ad_request; agrupamento por campaign/banner
    -- e aproximado aqui (usa a raw_impression para o contexto de campanha).
    count(DISTINCT req.event_id)    AS requests,
    -- impressions: clean, nao-blank, faturavel
    count(DISTINCT CASE
        WHEN r.blank = 0 AND r.ivt_status = '' AND r.billable = 1
        THEN r.event_id END)        AS impressions,
    -- clicks (da raw_click, LEFT JOIN por zone_id/campaign_id)
    count(DISTINCT ck.event_id)     AS clicks,
    -- conversions validas (nao-deduplicated)
    count(DISTINCT CASE
        WHEN cv.deduplicated = 0
        THEN cv.event_id END)       AS conversions,
    -- conversion_value: Decimal, nunca Float (TX-2)
    sumIf(cv.conversion_value_decimal, cv.deduplicated = 0)
                                    AS conversion_value,
    -- currency do primeiro asset_code de conversao (simplificado para UI)
    any(cv.conversion_value_asset_code) AS currency,
    -- inventory_loss ao vivo: requests - impressions validas
    (count(DISTINCT req.event_id)
     - count(DISTINCT CASE
        WHEN r.blank = 0 AND r.ivt_status = '' AND r.billable = 1
        THEN r.event_id END))       AS inventory_loss
FROM (SELECT * FROM adserver.raw_impression FINAL) r
LEFT JOIN (SELECT * FROM adserver.raw_ad_request FINAL) req
    ON r.zone_id = req.zone_id
    AND toStartOfMinute(r.occurred_at) = toStartOfMinute(req.occurred_at)
    AND r.tenant_id = req.tenant_id
LEFT JOIN (SELECT * FROM adserver.raw_click FINAL) ck
    ON r.campaign_id = ck.campaign_id
    AND r.banner_id  = ck.banner_id
    AND r.zone_id    = ck.zone_id
    AND toStartOfMinute(r.occurred_at) = toStartOfMinute(ck.occurred_at)
    AND r.tenant_id = ck.tenant_id
LEFT JOIN (
    -- Projecao explicita: SELECT * exclui colunas MATERIALIZED no CH 24.x (Code 47).
    -- conversion_value_decimal e MATERIALIZED em raw_conversion; deve ser listada.
    SELECT
        tenant_id, event_id, decision_id, model_version, occurred_at,
        schema_version, source, ingested_at,
        campaign_id, banner_id,
        conversion_value_amount, conversion_value_asset_code,
        conversion_value_scale,
        conversion_value_decimal,
        attribution_decision_id, deduplicated
    FROM adserver.raw_conversion FINAL
) cv
    ON r.campaign_id = cv.campaign_id
    AND r.banner_id  = cv.banner_id
    AND toStartOfMinute(r.occurred_at) = toStartOfMinute(cv.occurred_at)
    AND r.tenant_id = cv.tenant_id
GROUP BY
    minute_bucket, r.tenant_id, r.campaign_id, r.banner_id, r.zone_id;

COMMENT ON TABLE adserver.live_stats_exact IS
    '[NAO-FATURAVEL / ao vivo] Contagens ao vivo por minuto com dedupe (FINAL). '
    'Rotulo obrigatorio: data_source=live. NUNCA somar com stats_hourly. '
    'ADR-0001: frescor segundos-minutos; pode divergir do consolidado.';

-- =============================================================================
-- live_stats_fast: sem FINAL (mais rapido, duplicatas transientes possiveis)
-- =============================================================================
-- Para dashboards que priorizam latencia de query sobre precisao de curto prazo.
-- NAO-FATURAVEL. A divergencia transiente e aceita e documentada (ADR-0001).
CREATE VIEW IF NOT EXISTS adserver.live_stats_fast ON CLUSTER '{cluster}'
AS
SELECT
    toStartOfMinute(occurred_at)    AS minute_bucket,
    tenant_id,
    campaign_id,
    banner_id,
    zone_id,
    'live'                          AS data_source,  -- ROTULO OBRIGATORIO
    -- Sem JOIN com ad_request para performance; requests aproximado
    countIf(blank = 1 OR blank = 0) AS requests,
    countIf(blank = 0 AND ivt_status = '' AND billable = 1)
                                    AS impressions,
    0                               AS clicks,       -- requer JOIN separado
    0                               AS conversions,  -- requer JOIN separado
    toDecimal256(0, 18)             AS conversion_value,
    ''                              AS currency,
    countIf(blank = 0 AND ivt_status = '') - countIf(blank = 0 AND ivt_status = '' AND billable = 1)
                                    AS inventory_loss
FROM adserver.raw_impression       -- SEM FINAL: duplicatas transientes possiveis
GROUP BY
    minute_bucket, tenant_id, campaign_id, banner_id, zone_id;

COMMENT ON TABLE adserver.live_stats_fast IS
    '[NAO-FATURAVEL / ao vivo / aproximado] Contagens ao vivo por minuto SEM dedupe. '
    'Mais rapido que live_stats_exact; duplicatas transientes possiveis. '
    'Apenas impressions; clicks/conversions requerem JOIN separado. '
    'NUNCA usar para faturamento. ADR-0001.';
