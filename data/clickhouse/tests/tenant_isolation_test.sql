-- data/clickhouse/tests/tenant_isolation_test.sql
--
-- Teste de isolamento cross-tenant (TX-3 / security).
--
-- OBJETIVO:
--   Provar que um usuario do tenant A consultando qualquer tabela/view analitica
--   retorna ZERO linhas do tenant B. Cobre os casos normais e o caso adversarial
--   de nome de usuario malicioso.
--
-- COMO RODAR (quando houver cluster ClickHouse disponivel):
--   1. Aplique as migrations 001 a 006 em ordem.
--   2. Execute este script como usuario 'adserver_admin' ou 'adserver_ingest'
--      (acesso total; necessario para popular dados de fixture e criar usuarios).
--   3. Verifique que todas as queries rotuladas "EXPECT: 0 rows" retornam 0 linhas
--      e as rotuladas "EXPECT: >=1 rows" retornam pelo menos 1 linha.
--   4. Para CI automatizado sem cluster: marcar como @integration e rodar
--      apenas no ambiente de staging com ClickHouse real.
--      Ver: make data-validate (estatico) vs make data-integration-test (requer CH).
--
-- NOTA SOBRE EXECUCAO:
--   As secoes marcadas com -- [SETUP] requerem privilegios de admin e devem
--   ser executadas antes das queries de verificacao. Em CI, encapsular num
--   script wrapper que:
--     a) Cria as fixtures como admin.
--     b) Executa as queries de verificacao impersonando cada usuario tenant
--        via clickhouse-client --user tenant_<uuid>.
--     c) Verifica que o count() retorna 0 para o tenant errado e >0 para o correto.
--
-- EXECUCAO AUTOMATIZADA (achado #10, 31a onda):
--   make data-integration-test (scripts/ci/data-integration-test.py) executa
--   ESTE arquivo statement-a-statement contra um ClickHouse real, TROCANDO de
--   usuario e AFIRMANDO cada resultado — nao apenas roda o arquivo e confere
--   ausencia de erro SQL (o que "passaria" mesmo com isolamento quebrado, ja
--   que um SELECT count() por si so nao afirma nada). As diretivas abaixo sao
--   comentarios SQL adicionais, ignorados por qualquer client SQL comum, e
--   lidos pelo runner:
--     -- @user: <username-literal-do-CREATE-USER-acima> | admin
--         Troca o usuario de impersonacao para os statements seguintes ate a
--         proxima diretiva @user. 'admin' = sem --user/--password (usa o
--         usuario default/admin do cliente).
--     -- @expect: eq0 | ge1 | eq:'<valor>' | eq:NULL
--         Afirma o resultado do PROXIMO SELECT (statement imediatamente
--         seguinte). Falha alto (exit != 0) se o valor retornado nao bater.
--   Sem ClickHouse/clickhouse-client disponiveis, o runner FALHA alto — nunca
--   pula em silencio (ver make/data.mk::data-integration-test).

-- =============================================================================
-- [SETUP] Definicao de constantes de fixture
-- =============================================================================
-- @user: admin
-- Tenant A: UUID canonico, usuario valido
-- Tenant B: UUID canonico, usuario valido
-- Usuario adversarial: nome com 'tenant_' no meio (nao e UUID valido no sufixo)

-- TENANT_A_ID = '11111111-1111-4111-a111-111111111111'
-- TENANT_B_ID = '22222222-2222-4222-a222-222222222222'
-- ADVERSARIAL_USER = 'tenant_ab_tenant_cd'
--   (sufixo 'ab_tenant_cd' nao e UUID -> row-policy retorna NULL -> 0 rows)

-- =============================================================================
-- [SETUP] Criar usuarios de teste (rodar como admin)
-- =============================================================================

-- Usuario tenant A (formato correto: prefixo + UUID)
CREATE USER IF NOT EXISTS `tenant_11111111-1111-4111-a111-111111111111`
    IDENTIFIED BY 'test_password_tenant_a_fixture'
    DEFAULT ROLE tenant_role;

-- Usuario tenant B (formato correto: prefixo + UUID)
CREATE USER IF NOT EXISTS `tenant_22222222-2222-4222-a222-222222222222`
    IDENTIFIED BY 'test_password_tenant_b_fixture'
    DEFAULT ROLE tenant_role;

-- Usuario adversarial: nome que continha 'tenant_' em posicao nao-prefixo.
-- O replaceAll() antigo produziria sufixo 'ab_cd' (incorreto).
-- A correcao atual: sufixo 'ab_tenant_cd' nao e UUID -> NULL -> 0 rows (fail-closed).
CREATE USER IF NOT EXISTS `tenant_ab_tenant_cd`
    IDENTIFIED BY 'test_password_adversarial_fixture'
    DEFAULT ROLE tenant_role;

-- =============================================================================
-- [SETUP] Inserir dados de fixture (rodar como admin)
-- =============================================================================
-- Duas linhas: uma para tenant A, uma para tenant B.
-- Inseridas em cada tabela relevante.

INSERT INTO adserver.stats_hourly_state
    (hour_bucket, tenant_id, campaign_id, banner_id, zone_id, currency,
     requests_state, impressions_state, clicks_state, conversions_state,
     conversion_value_state)
VALUES
    -- Tenant A
    (toStartOfHour(now()), '11111111-1111-4111-a111-111111111111',
     'camp-a', 'banner-a', 'zone-a', 'BRL',
     uniqState('evt-req-a'), uniqState('evt-imp-a'), uniqState('evt-clk-a'),
     uniqState('evt-cvr-a'), sumState(toDecimal128(100, 18))),
    -- Tenant B
    (toStartOfHour(now()), '22222222-2222-4222-a222-222222222222',
     'camp-b', 'banner-b', 'zone-b', 'USD',
     uniqState('evt-req-b'), uniqState('evt-imp-b'), uniqState('evt-clk-b'),
     uniqState('evt-cvr-b'), sumState(toDecimal128(200, 18)));

INSERT INTO adserver.raw_impression
    (tenant_id, event_id, decision_id, model_version, occurred_at,
     schema_version, source, campaign_id, banner_id, zone_id,
     served_tier, billable, blank, ivt_status)
VALUES
    -- Tenant A
    ('11111111-1111-4111-a111-111111111111',
     'evt-imp-a-001', 'dec-a-001', 'v1', now(), 'v1', 'test',
     'camp-a', 'banner-a', 'zone-a', 'CONTRACT', 1, 0, ''),
    -- Tenant B
    ('22222222-2222-4222-a222-222222222222',
     'evt-imp-b-001', 'dec-b-001', 'v1', now(), 'v1', 'test',
     'camp-b', 'banner-b', 'zone-b', 'CONTRACT', 1, 0, '');

INSERT INTO adserver.raw_ad_request
    (tenant_id, event_id, decision_id, model_version, occurred_at,
     schema_version, source, zone_id, site_id, geo_country, geo_city,
     user_agent_class, referer_url, cachebuster)
VALUES
    -- Tenant A
    ('11111111-1111-4111-a111-111111111111',
     'evt-req-a-001', '', 'v1', now(), 'v1', 'test',
     'zone-a', 'site-a', 'BR', 'Sao Paulo', 'mobile/chrome', 'https://example.com/page', 'cb1'),
    -- Tenant B
    ('22222222-2222-4222-a222-222222222222',
     'evt-req-b-001', '', 'v1', now(), 'v1', 'test',
     'zone-b', 'site-b', 'US', 'New York', 'desktop/firefox', 'https://other.com/page', 'cb2');

INSERT INTO adserver.raw_ivt_score
    (event_id, score_run_id, tenant_id, campaign_id, zone_id,
     occurred_at, scored_at, ivt_status, ivt_prob, ivt_label, model_version)
VALUES
    -- Tenant A
    ('evt-ivt-a-001', 'run-a-001', '11111111-1111-4111-a111-111111111111',
     'camp-a', 'zone-a', now(), now(), 'ivt', 0.95, 1, 'ivt-v1'),
    -- Tenant B
    ('evt-ivt-b-001', 'run-b-001', '22222222-2222-4222-a222-222222222222',
     'camp-b', 'zone-b', now(), now(), 'ivt', 0.91, 1, 'ivt-v1');

INSERT INTO adserver.raw_click
    (tenant_id, event_id, decision_id, model_version, occurred_at,
     schema_version, source, campaign_id, banner_id, zone_id, dest_url)
VALUES
    ('11111111-1111-4111-a111-111111111111',
     'evt-clk-a-001', 'dec-a-001', 'v1', now(), 'v1', 'test',
     'camp-a', 'banner-a', 'zone-a', 'https://advertiser-a.com/lp'),
    ('22222222-2222-4222-a222-222222222222',
     'evt-clk-b-001', 'dec-b-001', 'v1', now(), 'v1', 'test',
     'camp-b', 'banner-b', 'zone-b', 'https://advertiser-b.com/lp');

INSERT INTO adserver.raw_conversion
    (tenant_id, event_id, decision_id, model_version, occurred_at,
     schema_version, source, campaign_id, banner_id,
     conversion_value_amount, conversion_value_asset_code,
     conversion_value_scale, attribution_decision_id, deduplicated)
VALUES
    ('11111111-1111-4111-a111-111111111111',
     'evt-cvr-a-001', 'dec-a-001', 'v1', now(), 'v1', 'test',
     'camp-a', 'banner-a', 5000, 'BRL', 2, 'dec-a-001', 0),
    ('22222222-2222-4222-a222-222222222222',
     'evt-cvr-b-001', 'dec-b-001', 'v1', now(), 'v1', 'test',
     'camp-b', 'banner-b', 9900, 'USD', 2, 'dec-b-001', 0);

-- =============================================================================
-- BLOCO 1: Tenant A consultando dados de Tenant B
-- =============================================================================
-- Impersonar o usuario tenant_11111111-1111-4111-a111-111111111111.
-- Em clickhouse-client: executar com --user tenant_11111111-1111-4111-a111-111111111111
-- @user: tenant_11111111-1111-4111-a111-111111111111

-- TESTE 1a: stats_hourly — tenant A nao ve linhas de tenant B
-- EXPECT: 0 rows (count = 0)
-- @expect: eq0
SELECT count() AS cross_tenant_leak_hourly
FROM adserver.stats_hourly
WHERE tenant_id = '22222222-2222-4222-a222-222222222222';
-- Resultado esperado: 0
-- Se retornar > 0: FALHA DE ISOLAMENTO — vazamento cross-tenant em stats_hourly.

-- TESTE 1b: raw_impression — tenant A nao ve linhas de tenant B
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS cross_tenant_leak_impression
FROM adserver.raw_impression FINAL
WHERE tenant_id = '22222222-2222-4222-a222-222222222222';
-- Resultado esperado: 0

-- TESTE 1c: raw_ad_request — tenant A nao ve linhas de tenant B
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS cross_tenant_leak_ad_request
FROM adserver.raw_ad_request FINAL
WHERE tenant_id = '22222222-2222-4222-a222-222222222222';
-- Resultado esperado: 0

-- TESTE 1d: raw_click — tenant A nao ve linhas de tenant B
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS cross_tenant_leak_click
FROM adserver.raw_click FINAL
WHERE tenant_id = '22222222-2222-4222-a222-222222222222';
-- Resultado esperado: 0

-- TESTE 1e: raw_conversion — tenant A nao ve linhas de tenant B
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS cross_tenant_leak_conversion
FROM adserver.raw_conversion FINAL
WHERE tenant_id = '22222222-2222-4222-a222-222222222222';
-- Resultado esperado: 0

-- TESTE 1e2: raw_ivt_score — tenant A nao ve linhas de tenant B
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS cross_tenant_leak_ivt_score
FROM adserver.raw_ivt_score FINAL
WHERE tenant_id = '22222222-2222-4222-a222-222222222222';
-- Resultado esperado: 0
-- Se retornar > 0: FALHA DE ISOLAMENTO — vazamento cross-tenant em raw_ivt_score.

-- TESTE 1f: live_stats_exact — tenant A nao ve linhas de tenant B
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS cross_tenant_leak_live_exact
FROM adserver.live_stats_exact
WHERE tenant_id = '22222222-2222-4222-a222-222222222222';
-- Resultado esperado: 0

-- TESTE 1g: live_stats_fast — tenant A nao ve linhas de tenant B
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS cross_tenant_leak_live_fast
FROM adserver.live_stats_fast
WHERE tenant_id = '22222222-2222-4222-a222-222222222222';
-- Resultado esperado: 0

-- =============================================================================
-- BLOCO 2: Tenant A consultando seus proprios dados (sanidade)
-- =============================================================================
-- Ainda impersonando tenant_11111111-1111-4111-a111-111111111111.
-- @user: tenant_11111111-1111-4111-a111-111111111111

-- TESTE 2a: tenant A ve suas proprias linhas em stats_hourly
-- EXPECT: >= 1 rows
-- @expect: ge1
SELECT count() AS own_rows_hourly
FROM adserver.stats_hourly
WHERE tenant_id = '11111111-1111-4111-a111-111111111111';
-- Resultado esperado: >= 1
-- Se retornar 0: row-policy demasiado restritiva (falso negativo).

-- TESTE 2b: tenant A ve suas proprias impressoes
-- EXPECT: >= 1 rows
-- @expect: ge1
SELECT count() AS own_rows_impression
FROM adserver.raw_impression FINAL
WHERE tenant_id = '11111111-1111-4111-a111-111111111111';
-- Resultado esperado: >= 1

-- TESTE 2c: tenant A ve seus proprios scores IVT
-- EXPECT: >= 1 rows
-- @expect: ge1
SELECT count() AS own_rows_ivt_score
FROM adserver.raw_ivt_score FINAL
WHERE tenant_id = '11111111-1111-4111-a111-111111111111';
-- Resultado esperado: >= 1
-- Se retornar 0: row-policy demasiado restritiva (falso negativo).

-- =============================================================================
-- BLOCO 3: Caso adversarial — usuario "tenant_ab_tenant_cd"
-- =============================================================================
-- Este nome foi criado para demonstrar a vulnerabilidade do replaceAll() antigo.
-- Com replaceAll(currentUser(), 'tenant_', ''):
--   'tenant_ab_tenant_cd' -> 'ab_cd' (errado; poderia casar tenant_id='ab_cd').
-- Com a correcao (substring + match UUID):
--   sufixo = 'ab_tenant_cd' -> nao e UUID valido -> NULL -> 0 rows (fail-closed).
--
-- Impersonar o usuario tenant_ab_tenant_cd.
-- @user: tenant_ab_tenant_cd

-- TESTE 3a: usuario adversarial nao ve NENHUMA linha em stats_hourly (fail-closed)
-- EXPECT: 0 rows (a row-policy retorna NULL para sufixo nao-UUID -> sem match)
-- @expect: eq0
SELECT count() AS adversarial_leak_hourly
FROM adserver.stats_hourly;
-- Resultado esperado: 0
-- Se retornar > 0: row-policy FALHOU em rejeitar nome de usuario invalido.

-- TESTE 3b: usuario adversarial nao ve nenhuma linha em raw_impression
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS adversarial_leak_impression
FROM adserver.raw_impression FINAL;
-- Resultado esperado: 0

-- TESTE 3c: usuario adversarial nao ve nenhuma linha em raw_ad_request
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS adversarial_leak_ad_request
FROM adserver.raw_ad_request FINAL;
-- Resultado esperado: 0

-- TESTE 3d: usuario adversarial nao ve nenhuma linha em raw_click
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS adversarial_leak_click
FROM adserver.raw_click FINAL;
-- Resultado esperado: 0

-- TESTE 3e: usuario adversarial nao ve nenhuma linha em raw_conversion
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS adversarial_leak_conversion
FROM adserver.raw_conversion FINAL;
-- Resultado esperado: 0

-- TESTE 3f: usuario adversarial nao ve nenhuma linha em raw_ivt_score (fail-closed)
-- EXPECT: 0 rows
-- @expect: eq0
SELECT count() AS adversarial_leak_ivt_score
FROM adserver.raw_ivt_score FINAL;
-- Resultado esperado: 0
-- Se retornar > 0: row-policy FALHOU em rejeitar nome de usuario invalido para raw_ivt_score.

-- =============================================================================
-- BLOCO 4: Verificacao da expressao de extracao de tenant_id (logica interna)
-- =============================================================================
-- Estas queries verificam a logica da expressao de row-policy diretamente,
-- sem depender de impersonacao de usuario. Podem ser rodadas como admin.
-- @user: admin

-- TESTE 4a: prefixo correto + UUID valido -> extrai UUID
-- EXPECT: '550e8400-e29b-41d4-a716-446655440000'
-- @expect: eq:'550e8400-e29b-41d4-a716-446655440000'
SELECT if(
    startsWith('tenant_550e8400-e29b-41d4-a716-446655440000', 'tenant_')
      AND match(
            substring('tenant_550e8400-e29b-41d4-a716-446655440000', length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring('tenant_550e8400-e29b-41d4-a716-446655440000', length('tenant_') + 1),
    NULL
) AS extracted_tenant_id;
-- Resultado esperado: '550e8400-e29b-41d4-a716-446655440000'

-- TESTE 4b: nome adversarial 'tenant_ab_tenant_cd' -> NULL (fail-closed)
-- EXPECT: NULL
-- @expect: eq:NULL
SELECT if(
    startsWith('tenant_ab_tenant_cd', 'tenant_')
      AND match(
            substring('tenant_ab_tenant_cd', length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring('tenant_ab_tenant_cd', length('tenant_') + 1),
    NULL
) AS extracted_tenant_id;
-- Resultado esperado: NULL
-- Com replaceAll() antigo: retornaria 'ab_cd' (INCORRETO, vazamento potencial).

-- TESTE 4c: usuario sem prefixo 'tenant_' (ex.: 'adserver_admin') -> NULL
-- EXPECT: NULL
-- @expect: eq:NULL
SELECT if(
    startsWith('adserver_admin', 'tenant_')
      AND match(
            substring('adserver_admin', length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring('adserver_admin', length('tenant_') + 1),
    NULL
) AS extracted_tenant_id;
-- Resultado esperado: NULL

-- TESTE 4d: 'tenant_' sem UUID (so o prefixo) -> NULL
-- EXPECT: NULL
-- @expect: eq:NULL
SELECT if(
    startsWith('tenant_', 'tenant_')
      AND match(
            substring('tenant_', length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring('tenant_', length('tenant_') + 1),
    NULL
) AS extracted_tenant_id;
-- Resultado esperado: NULL

-- TESTE 4e: UUID em maiusculas (nao casa com regex lowercase) -> NULL (fail-closed)
-- EXPECT: NULL
-- Isso e intencional: os UUIDs de tenants sao sempre lowercase no sistema.
-- Se necessario suportar maiusculas, adicionar toLower() no match.
-- @expect: eq:NULL
SELECT if(
    startsWith('tenant_550E8400-E29B-41D4-A716-446655440000', 'tenant_')
      AND match(
            substring('tenant_550E8400-E29B-41D4-A716-446655440000', length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring('tenant_550E8400-E29B-41D4-A716-446655440000', length('tenant_') + 1),
    NULL
) AS extracted_tenant_id;
-- Resultado esperado: NULL (UUIDs de tenant sao lowercase; maiusculas = invalido)

-- =============================================================================
-- [TEARDOWN] Remover fixtures de teste (rodar como admin apos verificacao)
-- =============================================================================
-- @user: admin

DROP USER IF EXISTS `tenant_11111111-1111-4111-a111-111111111111`;
DROP USER IF EXISTS `tenant_22222222-2222-4222-a222-222222222222`;
DROP USER IF EXISTS `tenant_ab_tenant_cd`;

-- Remover linhas de fixture (identificadas pelos event_ids de teste)
ALTER TABLE adserver.raw_impression DELETE
WHERE event_id IN ('evt-imp-a-001', 'evt-imp-b-001');

ALTER TABLE adserver.raw_ad_request DELETE
WHERE event_id IN ('evt-req-a-001', 'evt-req-b-001');

ALTER TABLE adserver.raw_click DELETE
WHERE event_id IN ('evt-clk-a-001', 'evt-clk-b-001');

ALTER TABLE adserver.raw_ivt_score DELETE
WHERE event_id IN ('evt-ivt-a-001', 'evt-ivt-b-001');

ALTER TABLE adserver.raw_conversion DELETE
WHERE event_id IN ('evt-cvr-a-001', 'evt-cvr-b-001');

-- stats_hourly_state: nao tem DELETE direto (AggregatingMergeTree);
-- as linhas expiram pelo TTL ou podem ser removidas via OPTIMIZE + filtro.
-- Para teardown de teste: deletar por tenant_id e campaign_id de fixture.
ALTER TABLE adserver.stats_hourly_state DELETE
WHERE tenant_id IN (
    '11111111-1111-4111-a111-111111111111',
    '22222222-2222-4222-a222-222222222222'
)
AND campaign_id IN ('camp-a', 'camp-b');
