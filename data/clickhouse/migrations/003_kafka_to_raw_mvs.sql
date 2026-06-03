-- data/clickhouse/migrations/003_kafka_to_raw_mvs.sql
--
-- Materialized Views que movem dados do Kafka engine -> tabelas raw.
-- Este e o primeiro estagio do pipeline de ingestao; as MVs executam
-- AUTOMATICAMENTE a cada batch que o Kafka engine consome do Redpanda.
--
-- ARQUITETURA DO PIPELINE (sem Flink, ADR-0001):
--
--   Redpanda (adserver.*.v1)
--       |
--       v
--   kafka_* (Kafka engine — leitura transiente, sem storage)
--       |
--       v  [MV abaixo — INSERT atomico]
--   raw_* (ReplacingMergeTree — dedupe lazy por event_id)
--       |
--       v  [MVs em 004_stats_hourly.sql]
--   stats_hourly_state (AggregatingMergeTree — rollup faturavel <=1h)
--       |
--       v  [VIEW em 005_live_view.sql]
--   live_stats (view sobre raw_* — NÃO FATURÁVEL, rotulada)
--
-- IDEMPOTENCIA: a MV faz INSERT na tabela ReplacingMergeTree. Reentregas
-- do mesmo event_id (at-least-once do Redpanda) produzem linhas duplicadas
-- que o ReplacingMergeTree remove na merge. Antes da merge, queries com
-- FINAL ou GROUP BY event_id retornam o valor correto.
-- O faturamento (billing batch sobre Iceberg) usa dados pos-FINAL; portanto
-- a dedupe e efetiva antes de qualquer cobranca.

-- =============================================================================
-- MV: kafka_ad_request -> raw_ad_request
-- =============================================================================
CREATE MATERIALIZED VIEW IF NOT EXISTS adserver.mv_kafka_to_raw_ad_request
ON CLUSTER '{cluster}'
TO adserver.raw_ad_request
AS
SELECT
    tenant_id,
    event_id,
    decision_id,
    model_version,
    occurred_at,
    schema_version,
    source,
    now64(3)        AS ingested_at,
    zone_id,
    site_id,
    geo_country,
    geo_city,
    -- Campo proto `user_agent` (no 5, wire-locked) carrega a classe coarse.
    -- A kafka-engine (001) expoe a coluna como `user_agent` para casar o nome do campo
    -- proto no mapeamento Protobuf->ClickHouse. Aqui projetamos para `user_agent_class`
    -- conforme o nome semantico da tabela raw (002). Os dois nomes NUNCA se somam nem
    -- se confundem: `user_agent` existe so na kafka-engine (ponte transiente, sem storage).
    user_agent AS user_agent_class,
    referer_url,       -- sanitizado: scheme+host+path sem querystring
    cachebuster
FROM adserver.kafka_ad_request;

-- =============================================================================
-- MV: kafka_impression -> raw_impression
-- =============================================================================
CREATE MATERIALIZED VIEW IF NOT EXISTS adserver.mv_kafka_to_raw_impression
ON CLUSTER '{cluster}'
TO adserver.raw_impression
AS
SELECT
    tenant_id,
    event_id,
    decision_id,
    model_version,
    occurred_at,
    schema_version,
    source,
    now64(3)        AS ingested_at,
    campaign_id,
    banner_id,
    zone_id,
    served_tier,
    billable,
    blank,
    ivt_status
FROM adserver.kafka_impression;

-- =============================================================================
-- MV: kafka_click -> raw_click
-- =============================================================================
CREATE MATERIALIZED VIEW IF NOT EXISTS adserver.mv_kafka_to_raw_click
ON CLUSTER '{cluster}'
TO adserver.raw_click
AS
SELECT
    tenant_id,
    event_id,
    decision_id,
    model_version,
    occurred_at,
    schema_version,
    source,
    now64(3)        AS ingested_at,
    campaign_id,
    banner_id,
    zone_id,
    dest_url
FROM adserver.kafka_click;

-- =============================================================================
-- MV: kafka_conversion -> raw_conversion
-- =============================================================================
CREATE MATERIALIZED VIEW IF NOT EXISTS adserver.mv_kafka_to_raw_conversion
ON CLUSTER '{cluster}'
TO adserver.raw_conversion
AS
SELECT
    tenant_id,
    event_id,
    decision_id,
    model_version,
    occurred_at,
    schema_version,
    source,
    now64(3)        AS ingested_at,
    campaign_id,
    banner_id,
    conversion_value_amount,
    conversion_value_asset_code,
    conversion_value_scale,
    -- conversion_value_decimal e coluna MATERIALIZED na tabela destino;
    -- nao precisa ser calculada aqui (o INSERT omite colunas MATERIALIZED).
    attribution_decision_id,
    deduplicated
FROM adserver.kafka_conversion;

-- =============================================================================
-- MV: kafka_decision -> raw_decision
-- =============================================================================
CREATE MATERIALIZED VIEW IF NOT EXISTS adserver.mv_kafka_to_raw_decision
ON CLUSTER '{cluster}'
TO adserver.raw_decision
AS
SELECT
    tenant_id,
    event_id,
    decision_id,
    model_version,
    occurred_at,
    schema_version,
    source,
    now64(3)        AS ingested_at,
    zone_id,
    campaign_id,
    banner_id,
    served_tier,
    propensity,
    exploration_policy,
    ml_fail_open,
    candidate_count
FROM adserver.kafka_decision;
