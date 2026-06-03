-- data/clickhouse/migrations/001_kafka_engines.sql
--
-- Tabelas de ingestao via Kafka engine (Redpanda API Kafka).
-- Uma tabela Kafka por topico; nenhum dado e armazenado aqui diretamente:
-- elas sao pontes de leitura que alimentam as MVs de ingestao (002_raw_tables.sql).
--
-- INVARIANTES:
--   - Formato: ProtobufSingle com schema do Buf Schema Registry (ou Avro-less
--     Protobuf rawbytes). Ajustar `kafka_format` ao deployment (ver nota abaixo).
--   - Particionamento: Redpanda particiona por hash(event_id) — producer key.
--     O Kafka engine consome todas as particoes de cada consumer group.
--   - Nenhum tipo FLOAT em colunas monetarias (TX-2/DA-10). Verificado pelo
--     script scripts/ci/no-float-sql.sh.
--   - Consumer group isolado por tabela para controle independente de lag.
--
-- NOTA SOBRE FORMATO PROTOBUF NO CLICKHOUSE:
--   ClickHouse suporta `kafka_format = 'Protobuf'` com schema path relativo
--   ao `format_schema_path` do servidor, ou `ProtobufSingle` para mensagens
--   length-delimited. Para deploy inicial sem schema registry HTTP, usar
--   'JSONEachRow' como formato de desenvolvimento e migrar para Protobuf
--   quando o schema path estiver configurado. As MVs sao independentes do
--   formato de ingestao.
--
-- REFERENCIA DO SCHEMA (campos mapeados do Protobuf -> ClickHouse):
--   adserver.common.v1.Envelope:
--     tenant_id       -> String
--     event_id        -> String  (ULID/UUID, chave de dedupe)
--     decision_id     -> String
--     model_version   -> String
--     occurred_at     -> DateTime64(3, 'UTC')  (Timestamp proto -> ms)
--     schema_version  -> String
--     source          -> String
--   Campos especificos por evento: ver cada tabela abaixo.

CREATE DATABASE IF NOT EXISTS adserver ON CLUSTER '{cluster}';

-- =============================================================================
-- Kafka Engine: adserver.ad-request.v1
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.kafka_ad_request ON CLUSTER '{cluster}'
(
    -- Envelope (adserver.common.v1.Envelope)
    tenant_id       String,
    event_id        String,
    decision_id     String,
    model_version   String,
    occurred_at     DateTime64(3, 'UTC'),
    schema_version  String,
    source          String,

    -- AdRequest-specific (adserver.telemetry.v1.AdRequest)
    zone_id         String,
    site_id         String,
    geo_country     String,          -- Geo.country (ISO 3166-1 alpha-2)
    geo_city        String,          -- Geo.city (derivado, sem PII, TX-5)
    -- PRIVACIDADE (HIGH / TX-5): campo proto `user_agent` (no 5, wire-locked BACKWARD,
    -- TX-1) carrega a CLASSE coarse de dispositivo/browser (ex.: "mobile/chrome",
    -- "desktop/firefox"), NAO o UA bruto reidentificavel. O collector classifica o UA
    -- bruto no servidor e emite apenas a classe dentro do campo proto `user_agent`; o
    -- UA bruto nunca sai do servidor.
    -- Mapeamento Protobuf->ClickHouse: kafka_format='Protobuf' mapeia por NOME do campo
    -- proto; portanto a coluna DEVE se chamar `user_agent` para receber o valor do campo
    -- no 5. A MV de ingestao (003) projeta `user_agent AS user_agent_class` ao gravar na
    -- tabela raw (002), onde o nome semantico `user_agent_class` e preservado.
    user_agent      String,          -- campo proto no 5; valor = classe coarse (nao UA bruto)
    -- referer_url sanitizado: apenas scheme+host+path (sem query string).
    -- O produtor Go remove a query string antes de emitir. Sem PII (TX-5).
    referer_url     String,          -- sanitizado: scheme+host+path somente
    cachebuster     String
    -- custom_vars: mapa opaco; armazenado como String JSON para flexibilidade.
    -- Nao e indexado; consultas sobre custom_vars usam JSONExtract().
)
ENGINE = Kafka
SETTINGS
    kafka_broker_list    = '${REDPANDA_BROKERS}',
    kafka_topic_list     = 'adserver.ad-request.v1',
    kafka_group_name     = 'clickhouse-ingest-ad-request',
    kafka_format         = 'JSONEachRow',
    kafka_num_consumers  = 4,
    kafka_skip_broken_messages = 0,
    kafka_commit_every_batch = 1;

COMMENT ON TABLE adserver.kafka_ad_request IS
    'Ponte Kafka->ClickHouse para adserver.ad-request.v1. Nao consultar diretamente.';

-- =============================================================================
-- Kafka Engine: adserver.impression.v1
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.kafka_impression ON CLUSTER '{cluster}'
(
    -- Envelope
    tenant_id       String,
    event_id        String,
    decision_id     String,
    model_version   String,
    occurred_at     DateTime64(3, 'UTC'),
    schema_version  String,
    source          String,

    -- Impression-specific (adserver.telemetry.v1.Impression)
    campaign_id     String,
    banner_id       String,
    zone_id         String,
    served_tier     String,          -- enum ServedTier como string (ex.: "CONTRACT")
    billable        UInt8,           -- bool: 1=faturavel, 0=nao
    blank           UInt8,           -- bool: 1=impressao em branco
    ivt_status      String           -- "" = clean/nao avaliado; "bot", "datacenter" etc. (TX-6)
)
ENGINE = Kafka
SETTINGS
    kafka_broker_list    = '${REDPANDA_BROKERS}',
    kafka_topic_list     = 'adserver.impression.v1',
    kafka_group_name     = 'clickhouse-ingest-impression',
    kafka_format         = 'JSONEachRow',
    kafka_num_consumers  = 4,
    kafka_skip_broken_messages = 0,
    kafka_commit_every_batch = 1;

COMMENT ON TABLE adserver.kafka_impression IS
    'Ponte Kafka->ClickHouse para adserver.impression.v1. Nao consultar diretamente.';

-- =============================================================================
-- Kafka Engine: adserver.click.v1
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.kafka_click ON CLUSTER '{cluster}'
(
    -- Envelope
    tenant_id       String,
    event_id        String,
    decision_id     String,
    model_version   String,
    occurred_at     DateTime64(3, 'UTC'),
    schema_version  String,
    source          String,

    -- Click-specific (adserver.telemetry.v1.Click)
    campaign_id     String,
    banner_id       String,
    zone_id         String,
    dest_url        String
)
ENGINE = Kafka
SETTINGS
    kafka_broker_list    = '${REDPANDA_BROKERS}',
    kafka_topic_list     = 'adserver.click.v1',
    kafka_group_name     = 'clickhouse-ingest-click',
    kafka_format         = 'JSONEachRow',
    kafka_num_consumers  = 2,
    kafka_skip_broken_messages = 0,
    kafka_commit_every_batch = 1;

COMMENT ON TABLE adserver.kafka_click IS
    'Ponte Kafka->ClickHouse para adserver.click.v1. Nao consultar diretamente.';

-- =============================================================================
-- Kafka Engine: adserver.conversion.v1
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.kafka_conversion ON CLUSTER '{cluster}'
(
    -- Envelope
    tenant_id       String,
    event_id        String,
    decision_id     String,
    model_version   String,
    occurred_at     DateTime64(3, 'UTC'),
    schema_version  String,
    source          String,

    -- Conversion-specific (adserver.telemetry.v1.Conversion)
    campaign_id     String,
    banner_id       String,
    -- Money.amount como Int64 (minor units). Nunca Float (TX-2/DA-10).
    -- Money.asset_code e Money.scale viajam juntos para ser autocontido.
    conversion_value_amount     Int64,       -- minor units (int64, nunca float)
    conversion_value_asset_code String,      -- ex.: "BRL", "USD"
    conversion_value_scale      UInt32,      -- autoritativo no Asset Registry
    attribution_decision_id     String,      -- decision_id atribuido (pode diferir do envelope)
    deduplicated                UInt8        -- 1=duplicata, nao deve faturar/treinar
)
ENGINE = Kafka
SETTINGS
    kafka_broker_list    = '${REDPANDA_BROKERS}',
    kafka_topic_list     = 'adserver.conversion.v1',
    kafka_group_name     = 'clickhouse-ingest-conversion',
    kafka_format         = 'JSONEachRow',
    kafka_num_consumers  = 1,
    kafka_skip_broken_messages = 0,
    kafka_commit_every_batch = 1;

COMMENT ON TABLE adserver.kafka_conversion IS
    'Ponte Kafka->ClickHouse para adserver.conversion.v1. Nao consultar diretamente.';

-- =============================================================================
-- Kafka Engine: adserver.decision.v1
-- =============================================================================
CREATE TABLE IF NOT EXISTS adserver.kafka_decision ON CLUSTER '{cluster}'
(
    -- Envelope
    tenant_id       String,
    event_id        String,
    decision_id     String,
    model_version   String,
    occurred_at     DateTime64(3, 'UTC'),
    schema_version  String,
    source          String,

    -- Decision-specific (adserver.decision.v1.Decision)
    zone_id         String,
    campaign_id     String,
    banner_id       String,
    served_tier     String,
    -- propensity e score sao floats LEGITIMOS (probabilidade de ML, nao dinheiro)
    -- A proibicao de float (TX-2) vale so para dinheiro. Ver decision.proto.
    propensity      Float64,
    exploration_policy String,
    ml_fail_open    UInt8,
    candidate_count UInt32
)
ENGINE = Kafka
SETTINGS
    kafka_broker_list    = '${REDPANDA_BROKERS}',
    kafka_topic_list     = 'adserver.decision.v1',
    kafka_group_name     = 'clickhouse-ingest-decision',
    kafka_format         = 'JSONEachRow',
    kafka_num_consumers  = 4,
    kafka_skip_broken_messages = 0,
    kafka_commit_every_batch = 1;

COMMENT ON TABLE adserver.kafka_decision IS
    'Ponte Kafka->ClickHouse para adserver.decision.v1. Nao consultar diretamente.';
