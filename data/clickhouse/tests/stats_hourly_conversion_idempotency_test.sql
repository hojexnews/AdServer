-- data/clickhouse/tests/stats_hourly_conversion_idempotency_test.sql
--
-- Teste de idempotencia do StatsHourly sob reentrega at-least-once do MESMO
-- event_id de conversao (achado #11, 28a onda / TX-1 / DA-7 / CA-6).
--
-- OBJETIVO:
--   Provar (ou, no estado atual, EXPOR de forma executavel) o comportamento
--   de adserver.stats_hourly quando o MESMO evento de conversao (event_id
--   identico) e reentregue pelo Redpanda em DOIS INSERTs/blocos SEPARADOS
--   (simulando o cenario at-least-once real: producer retry apos timeout de
--   ack, ou reprocessamento do Kafka engine apos rebalance de consumer group).
--
-- CONTEXTO / LACUNA DOCUMENTADA:
--   Ver o comentario completo acima de "CREATE MATERIALIZED VIEW IF NOT
--   EXISTS adserver.mv_conversion_to_hourly" em
--   data/clickhouse/migrations/004_stats_hourly.sql para o mecanismo exato e
--   o TODO de correcao (opcoes a/b/c, pendente de decisao arquitetural via
--   tech-lead-architect / ADR sucessor da ADR-0001).
--
--   RESUMO DA ASSERCAO DUPLA DESTE TESTE:
--     1. requests/impressions/clicks/conversions (contagens via uniqState)
--        JA SAO idempotentes sob esta reentrega — uniqMerge deduplica pelo
--        valor de event_id mesmo quando os dois estados vem de disparos de
--        MV separados. EXPECT: PASSA hoje.
--     2. conversion_value (soma via sumState) NAO E idempotente sob esta
--        reentrega — sumMerge apenas soma os dois estados parciais, sem
--        nocao de distinct-por-event_id. EXPECT-KNOWN-GAP: FALHA hoje (o
--        valor observado sera o DOBRO do valor correto). Este teste serve
--        para documentar a lacuna de forma executavel e para virar sinal de
--        regressao/correcao: quando o TODO (a) ou (b) for implementado, a
--        assercao 2 deve passar a vencer, e este teste deve ser promovido a
--        gate obrigatorio (ex.: make data-integration-test) em vez de
--        apenas documentar o gap.
--
-- COMO RODAR (quando houver cluster ClickHouse disponivel):
--   1. Aplique as migrations 001 a 008 em ordem (schema completo).
--   2. Execute este script como usuario com acesso de escrita as tabelas
--      raw_conversion e de leitura a stats_hourly (ex.: adserver_admin).
--   3. As duas secoes [SETUP: primeira entrega] e [SETUP: reentrega]
--      DEVEM ser executadas como STATEMENTS SEPARADOS (nao um unico INSERT
--      com 2 linhas) — o objetivo e disparar mv_conversion_to_hourly 2x,
--      simulando 2 blocos de ingestao distintos, nao deduplicacao
--      intra-bloco (que e um cenario mais facil e nao e o que este teste
--      cobre).
--   4. Rode a query de [VERIFICACAO] e compare com as anotacoes EXPECT.
--   5. Para CI automatizado sem cluster: ver make/data.mk (nao adiciona
--      alvo novo aqui — este teste, como tenant_isolation_test.sql, e
--      documentacao executavel para quando a infra ClickHouse existir;
--      run_verified=false ate la).
--
-- LIMPEZA: os identificadores de fixture usam o sufixo '-idem-' para nao
-- colidir com fixtures de outros testes (ex.: tenant_isolation_test.sql).

-- =============================================================================
-- [SETUP] Constantes de fixture
-- =============================================================================
-- TENANT_ID    = '33333333-3333-4333-a333-333333333333'
-- CAMPAIGN_ID  = 'camp-idem'
-- BANNER_ID    = 'banner-idem'
-- EVENT_ID     = 'evt-cvr-idem-001'   (identico nas duas entregas)
-- ASSET_CODE   = 'USD'
-- AMOUNT       = 5000 (minor units), SCALE = 2  ->  conversion_value_decimal = 50.00
-- HOUR_BUCKET  = toStartOfHour(now())  (ambas as entregas no MESMO hour_bucket,
--                para que a agregacao caia na MESMA linha de stats_hourly_state)

-- =============================================================================
-- [SETUP: primeira entrega] INSERT #1 em raw_conversion (dispara a MV 1x)
-- =============================================================================
INSERT INTO adserver.raw_conversion
    (tenant_id, event_id, decision_id, model_version, occurred_at,
     schema_version, source, campaign_id, banner_id,
     conversion_value_amount, conversion_value_asset_code, conversion_value_scale,
     attribution_decision_id, deduplicated)
VALUES
    ('33333333-3333-4333-a333-333333333333',
     'evt-cvr-idem-001', 'dec-idem-001', 'v1', now(),
     'v1', 'test', 'camp-idem', 'banner-idem',
     5000, 'USD', 2,
     'dec-idem-001', 0);

-- =============================================================================
-- [SETUP: reentrega] INSERT #2 em raw_conversion — MESMO event_id, MESMO
-- payload, EM UM STATEMENT SEPARADO (simula reentrega at-least-once do
-- Redpanda chegando em outro bloco/flush do Kafka engine). Dispara a MV
-- mv_conversion_to_hourly PELA SEGUNDA VEZ, independentemente da primeira.
-- =============================================================================
INSERT INTO adserver.raw_conversion
    (tenant_id, event_id, decision_id, model_version, occurred_at,
     schema_version, source, campaign_id, banner_id,
     conversion_value_amount, conversion_value_asset_code, conversion_value_scale,
     attribution_decision_id, deduplicated)
VALUES
    ('33333333-3333-4333-a333-333333333333',
     'evt-cvr-idem-001', 'dec-idem-001', 'v1', now(),
     'v1', 'test', 'camp-idem', 'banner-idem',
     5000, 'USD', 2,
     'dec-idem-001', 0);

-- =============================================================================
-- [CONTROLE] raw_conversion com FINAL: prova que a tabela RAW, quando
-- consultada com FINAL (o padrao de dedupe documentado em 002_raw_tables.sql
-- e usado por live_stats_exact), reporta 1 linha para o event_id — isto
-- ilustra que o problema NAO esta no dado bruto (que dedupe corretamente sob
-- FINAL), e sim no fato de que a MV incremental ja processou (e acumulou) os
-- 2 blocos ANTES de qualquer merge/FINAL acontecer.
-- =============================================================================
-- EXPECT: 1 linha (a tabela raw, pos-FINAL, nunca teve "2 conversoes" —
-- so a MV incremental, disparada 2x, acumulou o estado 2x).
SELECT count(*) AS raw_conversion_final_rows
FROM adserver.raw_conversion FINAL
WHERE event_id = 'evt-cvr-idem-001';

-- =============================================================================
-- [VERIFICACAO] stats_hourly para o hour_bucket/dimensoes da fixture
-- =============================================================================
SELECT
    conversions,
    conversion_value,
    currency
FROM adserver.stats_hourly
WHERE tenant_id   = '33333333-3333-4333-a333-333333333333'
  AND campaign_id = 'camp-idem'
  AND banner_id   = 'banner-idem'
  AND currency    = 'USD'
  AND hour_bucket = toStartOfHour(now());

-- EXPECT (INVARIANTE DESEJADA — ja vale hoje, uniq dedupe por event_id):
--   conversions = 1
--   (mesmo com a MV disparada 2x, uniqMerge conta o event_id UMA vez).
--
-- EXPECT-KNOWN-GAP (achado #11 — NAO corrigido nesta migration, ver TODO em
-- 004_stats_hourly.sql):
--   conversion_value CORRETO seria 50.00 (uma unica conversao de $50).
--   VALOR OBSERVADO HOJE: 100.00 (dobrado) — sumMerge apenas soma os 2
--   estados parciais sem nocao de distinct-por-event_id.
--   Se esta linha comecar a reportar 50.00 apos uma mudanca de arquitetura
--   (TODO a/b em 004_stats_hourly.sql), promova este teste para gate
--   obrigatorio de CI (ele deixou de documentar um gap e passou a provar um
--   invariante real).
