-- data/clickhouse/migrations/004_stats_hourly.sql
--
-- StatsHourly: rollup faturavel horario (DA-7 / CA-6 / CA-7).
--
-- CONTRATO DA ADMIN (§4.1 da documentacao tecnica):
--   Colunas obrigatorias: hour_bucket, campaign_id, banner_id, zone_id,
--   requests, impressions, clicks, conversions, conversion_value, currency.
--   Defasagem maxima: <= 1 hora (CA-6).
--   NUNCA somar com o numero "ao vivo" (ADR-0001).
--   Esta e a UNICA fonte de verdade para billing (junto do Iceberg).
--
-- ARQUITETURA:
--   stats_hourly_state   — AggregatingMergeTree, acumula estados de agregacao
--                          (uniqState, sumState). Nao consultar diretamente.
--   mv_raw_*_to_hourly   — MVs que alimentam stats_hourly_state a cada INSERT
--                          nas raw_* (pipeline incremental, sem Flink).
--   stats_hourly         — VIEW de leitura que finaliza os estados (uniqMerge,
--                          sumMerge) e expoe o contrato do admin.
--
-- DINHEIRO: conversion_value em Decimal(38,18), nunca Float (TX-2/DA-10).
--   A soma de conversion_value usa sumState sobre o Decimal da raw_conversion
--   (coluna conversion_value_decimal, derivada de Int64 amount / 10^scale).
--   A coluna 'currency' e o asset_code do evento (rotulo; sem conversao
--   automatica, DA-10). Para campanhas multi-moeda numa mesma zona,
--   a GROUP BY inclui currency para manter ledgers isolados.
--
-- IVT (TX-6): impressions e conversions SOMENTE de eventos com ivt_status = ''
--   (clean). Eventos IVT sao excluidos do StatsHourly e portanto do faturamento.
--   O campo ivt_status e populado na ingestao (raw_impression) antes desta MV.
--
-- INDICADOR DE INVENTARIO (CA-6): Request - Impression = perda/escassez.
--   Exposto na VIEW stats_hourly como 'inventory_loss'.
--   Inclui impressoes em branco (blank=1) no denominador do requests,
--   pois uma impressao em branco e um request sem banner servido.
--
-- ROW-POLICIES (TX-3): ver 006_access_control.sql.
--   As row-policies sao aplicadas sobre stats_hourly_state e sobre a VIEW
--   stats_hourly, filtrando por tenant_id automaticamente.

-- =============================================================================
-- Tabela de estado: stats_hourly_state (AggregatingMergeTree)
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.stats_hourly_state ON CLUSTER '{cluster}'
(
    -- Dimensoes (GROUP BY da agregacao)
    hour_bucket     DateTime,                    -- toStartOfHour(occurred_at)
    tenant_id       String,
    campaign_id     String,
    banner_id       String,
    zone_id         String,
    currency        LowCardinality(String),      -- asset_code da conversao (rotulo)

    -- Metricas: estados de agregacao (AggregatingMergeTree)
    requests_state      AggregateFunction(uniq,  String),  -- uniq(event_id) de ad_request
    impressions_state   AggregateFunction(uniq,  String),  -- uniq(event_id) de impression clean
    clicks_state        AggregateFunction(uniq,  String),  -- uniq(event_id) de click
    conversions_state   AggregateFunction(uniq,  String),  -- uniq(event_id) de conversion valida
    -- Soma de conversion_value como Decimal(38,18).
    -- sumState sobre Decimal e suportado no ClickHouse >= 23.x.
    -- Alternativa para versoes mais antigas: armazenar como Int64 (minor units)
    -- e o scale separado, somando em Int64. Aqui usamos Decimal direto.
    conversion_value_state AggregateFunction(sum, Decimal(38, 18))
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(hour_bucket)
ORDER BY (hour_bucket, tenant_id, campaign_id, banner_id, zone_id, currency)
SETTINGS index_granularity = 8192;

COMMENT ON TABLE adserver.stats_hourly_state IS
    'Estado de agregacao horario (AggregatingMergeTree). Nao consultar diretamente: '
    'use a VIEW stats_hourly. Faturavel (DA-7/CA-6). IVT excluido (TX-6).';

-- =============================================================================
-- MV: raw_ad_request -> stats_hourly_state (requests)
-- =============================================================================
-- AdRequest nao tem campaign_id/banner_id (e pre-decisao); agrega por zone_id.
-- campaign_id/banner_id ficam como '' para requests (preenchidos nas outras MVs).
-- currency = '' para requests (sem valor monetario).
CREATE MATERIALIZED VIEW IF NOT EXISTS adserver.mv_ad_request_to_hourly
ON CLUSTER '{cluster}'
TO adserver.stats_hourly_state
AS
SELECT
    toStartOfHour(occurred_at)     AS hour_bucket,
    tenant_id,
    ''                             AS campaign_id,
    ''                             AS banner_id,
    zone_id,
    ''                             AS currency,
    uniqState(event_id)            AS requests_state,
    -- impressions/clicks/conversions nao se originam de ad_request:
    -- estado zero via uniqStateIf com condicao impossivel (1=0 nunca e verdadeiro);
    -- produz AggregateFunction(uniq, String) com contagem zero (sem +1 fantasma).
    uniqStateIf(event_id, 1 = 0)  AS impressions_state,
    uniqStateIf(event_id, 1 = 0)  AS clicks_state,
    uniqStateIf(event_id, 1 = 0)  AS conversions_state,
    sumState(toDecimal128(0, 18))  AS conversion_value_state
FROM adserver.raw_ad_request
GROUP BY hour_bucket, tenant_id, campaign_id, banner_id, zone_id, currency;

-- =============================================================================
-- MV: raw_impression -> stats_hourly_state (impressions, inclui blank como request)
-- =============================================================================
-- Impressoes em branco (blank=1) contribuem para requests mas NAO para impressions.
-- IVT excluido: ivt_status = '' (clean). Impressoes IVT nao faturadas (TX-6).
CREATE MATERIALIZED VIEW IF NOT EXISTS adserver.mv_impression_to_hourly
ON CLUSTER '{cluster}'
TO adserver.stats_hourly_state
AS
SELECT
    toStartOfHour(occurred_at)     AS hour_bucket,
    tenant_id,
    campaign_id,
    banner_id,
    zone_id,
    ''                             AS currency,
    uniqStateIf(event_id, blank = 1 OR blank = 0)  AS requests_state,  -- todos os eventos
    -- Impressoes validas: nao-blank E clean (sem IVT)
    uniqStateIf(event_id, blank = 0 AND ivt_status = '' AND billable = 1)
                                   AS impressions_state,
    uniqStateIf(event_id, 1 = 0)  AS clicks_state,
    uniqStateIf(event_id, 1 = 0)  AS conversions_state,
    sumState(toDecimal128(0, 18))  AS conversion_value_state
FROM adserver.raw_impression
GROUP BY hour_bucket, tenant_id, campaign_id, banner_id, zone_id, currency;

-- =============================================================================
-- MV: raw_click -> stats_hourly_state (clicks)
-- =============================================================================
CREATE MATERIALIZED VIEW IF NOT EXISTS adserver.mv_click_to_hourly
ON CLUSTER '{cluster}'
TO adserver.stats_hourly_state
AS
SELECT
    toStartOfHour(occurred_at)     AS hour_bucket,
    tenant_id,
    campaign_id,
    banner_id,
    zone_id,
    ''                             AS currency,
    uniqStateIf(event_id, 1 = 0)  AS requests_state,
    uniqStateIf(event_id, 1 = 0)  AS impressions_state,
    uniqState(event_id)            AS clicks_state,
    uniqStateIf(event_id, 1 = 0)  AS conversions_state,
    sumState(toDecimal128(0, 18))  AS conversion_value_state
FROM adserver.raw_click
GROUP BY hour_bucket, tenant_id, campaign_id, banner_id, zone_id, currency;

-- =============================================================================
-- MV: raw_conversion -> stats_hourly_state (conversions + conversion_value)
-- =============================================================================
-- Exclui conversions deduplicated=1 (duplicatas marcadas na ingestao).
-- currency = conversion_value_asset_code (ledger por ativo, DA-10).
CREATE MATERIALIZED VIEW IF NOT EXISTS adserver.mv_conversion_to_hourly
ON CLUSTER '{cluster}'
TO adserver.stats_hourly_state
AS
SELECT
    toStartOfHour(occurred_at)         AS hour_bucket,
    tenant_id,
    campaign_id,
    banner_id,
    ''                                 AS zone_id,      -- zone_id nao existe em Conversion
    conversion_value_asset_code        AS currency,
    uniqStateIf(event_id, 1 = 0)      AS requests_state,
    uniqStateIf(event_id, 1 = 0)      AS impressions_state,
    uniqStateIf(event_id, 1 = 0)      AS clicks_state,
    -- Apenas conversions validas (nao-deduplicated)
    uniqStateIf(event_id, deduplicated = 0)
                                       AS conversions_state,
    -- Soma apenas conversions validas. Decimal(38,18) (nunca Float, TX-2).
    sumStateIf(conversion_value_decimal, deduplicated = 0)
                                       AS conversion_value_state
FROM adserver.raw_conversion
GROUP BY hour_bucket, tenant_id, campaign_id, banner_id, zone_id, currency;

-- =============================================================================
-- VIEW de leitura: stats_hourly (contrato do admin, §4.1)
-- =============================================================================
-- Esta VIEW e a interface publica do StatsHourly. Finaliza os estados de
-- agregacao e expoe o contrato exato do admin (DA-7/CA-6).
-- Row-policies por tenant_id sao aplicadas aqui (ver 006_access_control.sql).
--
-- ROTULO OBRIGATORIO (ADR-0001):
--   Esta VIEW representa dados FATURAVEIS, consolidados em batch horario.
--   O BFF/frontend DEVE exibir "consolidado <=1h" e NUNCA somar com "ao vivo".
--   Ver 005_live_view.sql para a visao "ao vivo" (NAO-FATURAVEL).
--
-- INDICADOR DE INVENTARIO (CA-6):
--   inventory_loss = requests - impressions
--   Inclui impressoes em branco no requests; subtrai apenas impressoes validas.
--   Valor positivo indica escassez de inventario (demand > supply valido).
CREATE VIEW IF NOT EXISTS adserver.stats_hourly ON CLUSTER '{cluster}'
AS
SELECT
    hour_bucket,
    tenant_id,
    campaign_id,
    banner_id,
    zone_id,
    -- Finaliza os estados de agregacao (uniqMerge = exato para uniq; HLL-equivalente)
    uniqMerge(requests_state)           AS requests,
    uniqMerge(impressions_state)        AS impressions,
    uniqMerge(clicks_state)             AS clicks,
    uniqMerge(conversions_state)        AS conversions,
    -- conversion_value como Decimal(38,18). Nunca Float (TX-2/DA-10).
    sumMerge(conversion_value_state)    AS conversion_value,
    currency,
    -- Indicador de escassez de inventario (CA-6): requests - impressions.
    -- Valor positivo = oportunidades perdidas (sem banner elegivel ou IVT).
    (uniqMerge(requests_state) - uniqMerge(impressions_state))
                                        AS inventory_loss
FROM adserver.stats_hourly_state
GROUP BY
    hour_bucket, tenant_id, campaign_id, banner_id, zone_id, currency;

COMMENT ON TABLE adserver.stats_hourly IS
    '[FATURAVEL / consolidado <=1h] VIEW de leitura do StatsHourly. '
    'Contrato do admin (§4.1, DA-7/CA-6). IVT excluido. NUNCA somar com ao_vivo. '
    'Row-policies por tenant_id (TX-3) aplicadas via 006_access_control.sql.';
