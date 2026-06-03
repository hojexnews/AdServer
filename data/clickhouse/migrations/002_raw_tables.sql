-- data/clickhouse/migrations/002_raw_tables.sql
--
-- Tabelas raw de ingestao (ReplacingMergeTree) para cada tipo de evento.
-- Estas sao o destino das Materialized Views que consomem os Kafka engines.
--
-- ESTRATEGIA DE DEDUPE (TX-1 / at-least-once -> exactly-once):
--   Engine: ReplacingMergeTree(ingested_at)
--   Chave de ordenacao: (event_id, ...) com event_id como primeiro elemento.
--   Semantica: o Redpanda entrega at-least-once (producer acks=all).
--     O ReplacingMergeTree garante que, apos a merge (lazy/background), apenas
--     a ultima versao de cada event_id sobrevive. Para leituras antes da merge,
--     usar FINAL ou a clausula WHERE para filtar duplicatas transientes:
--       SELECT ... FROM adserver.raw_impression FINAL WHERE ...
--     O job de billing batch sobre Iceberg usa o snapshot consolidado (FINAL),
--     portanto a dedupe e efetiva antes do faturamento.
--   Alternativa considerada: dedupe no consumidor (consumer-side). Rejeitada
--     porque exigiria estado externo (Redis/Bloom filter) no consumer ClickHouse,
--     enquanto o ReplacingMergeTree oferece dedupe nativa, lazy e sem overhead
--     no hot path de ingestao.
--
-- PARTICIONAMENTO ClickHouse: toYYYYMMDD(occurred_at) para:
--   - Expurgo eficiente por data (TTL operacional).
--   - Queries analiticas por intervalo de data sem full scan.
--   - Granularidade de particao compativel com o batch horario (DA-7).
--
-- CAMPO ingested_at: timestamp de ingestao (processamento time). Diferente
--   de occurred_at (event time). Usado pelo ReplacingMergeTree como versao
--   e para diagnosticar lag de ingestao.
--
-- PRIVACIDADE (TX-5/DA-11): nenhuma coluna armazena IP bruto nem PII.
--   Geo e derivado (country/city).
--   user_agent_class: CLASSE de dispositivo/browser (ex.: "mobile/chrome"),
--     NAO o UA bruto reidentificavel. Usada para sinais IVT (TX-6) e para
--     segmentacao coarse. Descartada do StatsHourly (nao entra em billing).
--   referer_url: sanitizado pelo produtor Go (scheme+host+path; sem querystring).
--     Sem dados de sessao ou parametros rastreadores. Sem PII (TX-5).

-- =============================================================================
-- raw_ad_request
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.raw_ad_request ON CLUSTER '{cluster}'
(
    -- Envelope
    tenant_id       String,
    event_id        String,
    decision_id     String,
    model_version   String,
    occurred_at     DateTime64(3, 'UTC'),
    schema_version  String,
    source          String,
    ingested_at     DateTime64(3, 'UTC') DEFAULT now64(3),

    -- AdRequest-specific
    zone_id         String,
    site_id         String,
    geo_country     LowCardinality(String),
    geo_city        String,
    -- PRIVACIDADE (HIGH): classe de dispositivo/browser emitida pelo produtor Go,
    -- NAO o UA bruto. Ex.: "mobile/chrome", "desktop/safari", "bot/crawler".
    -- Usado como sinal de IVT (TX-6). Nunca reidentificavel individualmente.
    user_agent_class LowCardinality(String),
    -- referer_url sanitizado: scheme+host+path sem querystring (produtor Go).
    -- Sem parametros de rastreamento ou dados de sessao. Sem PII (TX-5).
    referer_url     String,
    cachebuster     String
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMMDD(occurred_at)
ORDER BY (event_id, occurred_at)
TTL toDateTime(occurred_at) + INTERVAL 30 DAY DELETE
SETTINGS index_granularity = 8192;

COMMENT ON TABLE adserver.raw_ad_request IS
    'Eventos AdRequest deduplicados por event_id (ReplacingMergeTree). '
    'Fonte para StatsHourly (requests). Sem PII (TX-5). '
    'user_agent_class: classe coarse device/browser (nao UA bruto). '
    'referer_url: sanitizado scheme+host+path (sem querystring).';

-- =============================================================================
-- raw_impression
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.raw_impression ON CLUSTER '{cluster}'
(
    -- Envelope
    tenant_id       String,
    event_id        String,
    decision_id     String,
    model_version   String,
    occurred_at     DateTime64(3, 'UTC'),
    schema_version  String,
    source          String,
    ingested_at     DateTime64(3, 'UTC') DEFAULT now64(3),

    -- Impression-specific
    campaign_id     String,
    banner_id       String,
    zone_id         String,
    served_tier     LowCardinality(String),
    billable        UInt8,
    blank           UInt8,
    ivt_status      LowCardinality(String)  -- "" = clean; "bot", "datacenter", "dup"
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMMDD(occurred_at)
ORDER BY (event_id, occurred_at)
TTL toDateTime(occurred_at) + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;

COMMENT ON TABLE adserver.raw_impression IS
    'Eventos Impression deduplicados por event_id. Inclui ivt_status (TX-6). '
    'Fonte para StatsHourly (impressions) e para Iceberg billing. Sem PII.';

-- =============================================================================
-- raw_click
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.raw_click ON CLUSTER '{cluster}'
(
    -- Envelope
    tenant_id       String,
    event_id        String,
    decision_id     String,
    model_version   String,
    occurred_at     DateTime64(3, 'UTC'),
    schema_version  String,
    source          String,
    ingested_at     DateTime64(3, 'UTC') DEFAULT now64(3),

    -- Click-specific
    campaign_id     String,
    banner_id       String,
    zone_id         String,
    dest_url        String
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMMDD(occurred_at)
ORDER BY (event_id, occurred_at)
TTL toDateTime(occurred_at) + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;

COMMENT ON TABLE adserver.raw_click IS
    'Eventos Click deduplicados por event_id. Fonte para StatsHourly (clicks). Sem PII.';

-- =============================================================================
-- raw_conversion
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.raw_conversion ON CLUSTER '{cluster}'
(
    -- Envelope
    tenant_id       String,
    event_id        String,
    decision_id     String,
    model_version   String,
    occurred_at     DateTime64(3, 'UTC'),
    schema_version  String,
    source          String,
    ingested_at     DateTime64(3, 'UTC') DEFAULT now64(3),

    -- Conversion-specific
    campaign_id     String,
    banner_id       String,
    -- DINHEIRO: Decimal, nunca Float (TX-2/DA-10).
    -- Armazenamos amount (minor units) como Int64 e scale como UInt32.
    -- A coluna conversion_value_decimal e a representacao Decimal(38,18)
    -- derivada para queries analiticas. O scale autoritativo vem do
    -- Asset Registry; aqui guardamos o scale do evento para auditoria.
    conversion_value_amount     Int64,
    conversion_value_asset_code LowCardinality(String),
    conversion_value_scale      UInt32,
    -- Decimal derivado: amount / 10^scale. Calculado na ingestao para
    -- facilitar somas analiticas. Decimal(38,18) cobre todos os ativos
    -- incluindo ERC-20 (scale=18). Nunca Float.
    conversion_value_decimal    Decimal(38, 18)
        MATERIALIZED toDecimal256(conversion_value_amount, 0)
                     / pow(10, conversion_value_scale),
    attribution_decision_id     String,
    deduplicated                UInt8
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMMDD(occurred_at)
ORDER BY (event_id, occurred_at)
TTL toDateTime(occurred_at) + INTERVAL 365 DAY DELETE
SETTINGS index_granularity = 8192;

COMMENT ON TABLE adserver.raw_conversion IS
    'Eventos Conversion deduplicados por event_id. conversion_value em Decimal '
    '(nunca Float, TX-2). Fonte para StatsHourly (conversions/conversion_value) '
    'e para Iceberg billing CPA. Sem PII.';

-- =============================================================================
-- raw_decision
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.raw_decision ON CLUSTER '{cluster}'
(
    -- Envelope
    tenant_id       String,
    event_id        String,
    decision_id     String,
    model_version   String,
    occurred_at     DateTime64(3, 'UTC'),
    schema_version  String,
    source          String,
    ingested_at     DateTime64(3, 'UTC') DEFAULT now64(3),

    -- Decision-specific
    zone_id         String,
    campaign_id     String,
    banner_id       String,
    served_tier     LowCardinality(String),
    -- propensity e Float LEGITIMO: probabilidade de ML, nao dinheiro (TX-2).
    propensity      Float64,
    exploration_policy LowCardinality(String),
    ml_fail_open    UInt8,
    candidate_count UInt32
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMMDD(occurred_at)
ORDER BY (decision_id, occurred_at)
TTL toDateTime(occurred_at) + INTERVAL 30 DAY DELETE
SETTINGS index_granularity = 8192;

COMMENT ON TABLE adserver.raw_decision IS
    'Decision log deduplicado por decision_id. Fecha o loop de atribuicao '
    '(TX-1). propensity eh Float legitimo (ML, nao dinheiro). Sem PII.';
