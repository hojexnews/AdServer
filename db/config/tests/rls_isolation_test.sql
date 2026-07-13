-- =============================================================================
-- rls_isolation_test.sql — Teste de isolamento RLS por tenant (TX-3 / §2.6)
--
-- PROPÓSITO
--   Provar que o RLS do schema config impede tenant A de ler dados de tenant B,
--   inclusive via tabela campaign_zones (defesa-em-profundidade — 0003_up).
--   Cobre também o caso fail-closed: sem adserver.tenant_id setado → 0 linhas.
--
-- DEPENDÊNCIAS
--   - Postgres 16 com migrations 0001, 0002 e 0003 do schema config aplicadas.
--   - Usuário de aplicação (não superuser): adserver_app
--     com GRANT SELECT, INSERT, UPDATE, DELETE em todas as tabelas do schema config.
--   - Superuser (adserver_admin) para setup inicial e criação do role de teste.
--
-- COMO RODAR
--   # 1. Aplicar migrations (uma vez):
--   make db-migrate-up
--
--   # 2. Criar o role de aplicação se não existir (rodar como superuser):
--   #    psql -U adserver_admin -d adserver -c "
--   #      CREATE ROLE adserver_app NOINHERIT LOGIN PASSWORD 'changeme';
--   #      GRANT USAGE ON SCHEMA config TO adserver_app;
--   #      GRANT SELECT, INSERT, UPDATE, DELETE
--   #        ON ALL TABLES IN SCHEMA config TO adserver_app;
--   #      GRANT USAGE ON ALL SEQUENCES IN SCHEMA config TO adserver_app;
--   #    "
--
--   # 3. Rodar o teste (como superuser — ele faz SET ROLE internamente):
--   psql -U adserver_admin -d adserver \
--        -v ON_ERROR_STOP=1 \
--        -f db/config/tests/rls_isolation_test.sql
--
--   Resultado esperado: linha "== RLS ISOLATION: ALL TESTS PASSED ==" ao final.
--   Qualquer ASSERT falho aborta com RAISE EXCEPTION (exit code != 0).
--
-- ALTERNATIVA pgTAP
--   Para integrar a um runner de testes unitários, instale a extensão pgTAP:
--     CREATE EXTENSION IF NOT EXISTS pgtap;
--   e adapte os blocos DO $$ abaixo usando ok(), is(), results_eq() etc.
--   Documentação: https://pgtap.org/
--   Dependência de pacote: postgresql-<ver>-pgtap (apt) ou pgtap (brew).
--
-- NOTAS
--   - O teste roda dentro de uma transação única e faz ROLLBACK ao final —
--     não persiste dados no banco (idempotente, seguro para rodar em dev/CI).
--   - SET LOCAL só afeta a transação corrente; sem necessidade de cleanup manual.
--   - O superuser bypassa RLS por padrão; por isso fazemos SET ROLE adserver_app
--     antes de cada bloco de verificação.
-- =============================================================================

BEGIN;

-- ---------------------------------------------------------------------------
-- SETUP: dados de fixtures para dois tenants distintos
-- ---------------------------------------------------------------------------

-- UUIDs determinísticos para os dois tenants de teste
-- (não colide com produção pois fazemos ROLLBACK ao final)
DO $$
DECLARE
    v_tenant_a UUID := 'aaaaaaaa-0000-0000-0000-000000000001';
    v_tenant_b UUID := 'bbbbbbbb-0000-0000-0000-000000000001';
    v_adv_a    BIGINT;
    v_adv_b    BIGINT;
    v_camp_a   BIGINT;
    v_camp_b   BIGINT;
    v_site_a   BIGINT;
    v_site_b   BIGINT;
    v_zone_a   BIGINT;
    v_zone_b   BIGINT;
    v_banner_a BIGINT;
    v_banner_b BIGINT;
    v_rset_a   BIGINT;
    v_rset_b   BIGINT;
BEGIN
    -- Garante que adserver_app existe (idempotente)
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'adserver_app') THEN
        RAISE EXCEPTION
            'Role adserver_app não existe. Crie-a antes de rodar este teste. '
            'Ver cabeçalho do arquivo para instruções.';
    END IF;

    -- Inserir advertisers (tenant A e B) — como superuser (bypass RLS).
    -- Dois INSERTs de UMA linha cada: `RETURNING id INTO <scalar>` exige que a
    -- instrução retorne exatamente uma linha (um INSERT multi-VALUES aborta com
    -- "query returned more than one row"). Mesmo padrão de sites/zones/campaigns.
    INSERT INTO config.advertisers
        (tenant_id, name, login_username, login_password_hash)
    VALUES (v_tenant_a, 'Advertiser A', 'adv_a_test_rls', '$2b$12$placeholder_hash_a')
    RETURNING id INTO v_adv_a;

    INSERT INTO config.advertisers
        (tenant_id, name, login_username, login_password_hash)
    VALUES (v_tenant_b, 'Advertiser B', 'adv_b_test_rls', '$2b$12$placeholder_hash_b')
    RETURNING id INTO v_adv_b;

    -- Inserir sites (tenant A e B)
    INSERT INTO config.sites (tenant_id, name, url)
    VALUES (v_tenant_a, 'Site A', 'https://site-a.example.com')
    RETURNING id INTO v_site_a;

    INSERT INTO config.sites (tenant_id, name, url)
    VALUES (v_tenant_b, 'Site B', 'https://site-b.example.com')
    RETURNING id INTO v_site_b;

    -- Inserir zones (tenant A e B)
    INSERT INTO config.zones (tenant_id, site_id, name, width, height)
    VALUES (v_tenant_a, v_site_a, 'Zone A 728x90', 728, 90)
    RETURNING id INTO v_zone_a;

    INSERT INTO config.zones (tenant_id, site_id, name, width, height)
    VALUES (v_tenant_b, v_site_b, 'Zone B 300x250', 300, 250)
    RETURNING id INTO v_zone_b;

    -- Inserir campaigns (tenant A e B)
    -- rate = NUMERIC(26,6): sem float (CA-7/TX-2)
    INSERT INTO config.campaigns
        (tenant_id, advertiser_id, name, type, priority,
         goal_target, goal_metric, start_at, end_at,
         pricing_model, rate, currency)
    VALUES
        (v_tenant_a, v_adv_a, 'Campaign A CPM', 'remnant', 5,
         10000, 'impressions',
         now(), now() + interval '30 days',
         'cpm', 5.000000, 'BRL')
    RETURNING id INTO v_camp_a;

    INSERT INTO config.campaigns
        (tenant_id, advertiser_id, name, type, priority,
         goal_target, goal_metric, start_at, end_at,
         pricing_model, rate, currency)
    VALUES
        (v_tenant_b, v_adv_b, 'Campaign B CPC', 'remnant', 5,
         500, 'clicks',
         now(), now() + interval '30 days',
         'cpc', 1.500000, 'BRL')
    RETURNING id INTO v_camp_b;

    -- Inserir banners (tenant A e B)
    INSERT INTO config.banners
        (tenant_id, campaign_id, name, creative_type, asset_url, dest_url, width, height)
    VALUES
        (v_tenant_a, v_camp_a, 'Banner A', 'image',
         'https://cdn.example.com/banner_a.png',
         'https://anunciante-a.com', 728, 90)
    RETURNING id INTO v_banner_a;

    INSERT INTO config.banners
        (tenant_id, campaign_id, name, creative_type, asset_url, dest_url, width, height)
    VALUES
        (v_tenant_b, v_camp_b, 'Banner B', 'image',
         'https://cdn.example.com/banner_b.png',
         'https://anunciante-b.com', 300, 250)
    RETURNING id INTO v_banner_b;

    -- Inserir campaign_zones (vínculos N:N — sem tenant_id próprio)
    INSERT INTO config.campaign_zones (campaign_id, zone_id)
    VALUES (v_camp_a, v_zone_a);

    INSERT INTO config.campaign_zones (campaign_id, zone_id)
    VALUES (v_camp_b, v_zone_b);

    -- Inserir delivery_rule_sets (tenant A e B)
    INSERT INTO config.delivery_rule_sets (tenant_id, name)
    VALUES (v_tenant_a, 'RuleSet A')
    RETURNING id INTO v_rset_a;

    INSERT INTO config.delivery_rule_sets (tenant_id, name)
    VALUES (v_tenant_b, 'RuleSet B')
    RETURNING id INTO v_rset_b;

    -- Inserir delivery_rules (tenant A e B — regras diretas via rule_set_id)
    INSERT INTO config.delivery_rules
        (tenant_id, owner_type, owner_id, vector, operator, value,
         logical_op, rule_set_id)
    VALUES
        (v_tenant_a, 'campaign', v_camp_a, 'Geo - Country', 'is', 'BR',
         'AND', v_rset_a),
        (v_tenant_b, 'campaign', v_camp_b, 'Geo - Country', 'is', 'US',
         'AND', v_rset_b);

    -- Inserir caps (tenant A e B)
    INSERT INTO config.caps
        (tenant_id, owner_type, owner_id, scope, limit_count)
    VALUES
        (v_tenant_a, 'campaign', v_camp_a, 'campaign_total', 10000),
        (v_tenant_b, 'campaign', v_camp_b, 'campaign_total', 5000);

    RAISE NOTICE 'Setup concluído: tenant_a=% tenant_b=% camp_a=% camp_b=% zone_a=% zone_b=%',
        v_tenant_a, v_tenant_b, v_camp_a, v_camp_b, v_zone_a, v_zone_b;
END;
$$;


-- ---------------------------------------------------------------------------
-- HELPER: função de assert para uso local nesta sessão de teste
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION pg_temp.assert_count(
    p_label    TEXT,
    p_actual   BIGINT,
    p_expected BIGINT
) RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    IF p_actual <> p_expected THEN
        RAISE EXCEPTION 'ASSERT FALHOU [%]: esperado=% obtido=%',
            p_label, p_expected, p_actual;
    END IF;
    RAISE NOTICE 'PASS [%]: count=% (esperado=%)', p_label, p_actual, p_expected;
END;
$$;


-- ===========================================================================
-- BLOCO 1 — Tenant A enxerga apenas seus próprios dados (0 linhas de B)
-- ===========================================================================

DO $$
DECLARE
    v_tenant_a UUID := 'aaaaaaaa-0000-0000-0000-000000000001';
    v_count    BIGINT;
BEGIN
    -- Ativa RLS como usuário de aplicação para este bloco
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'aaaaaaaa-0000-0000-0000-000000000001';

    -- 1a. advertisers: tenant A vê exatamente 1 advertiser (o seu)
    SELECT COUNT(*) INTO v_count FROM config.advertisers;
    PERFORM pg_temp.assert_count('advertisers: tenant_a vê 1', v_count, 1);

    -- 1b. campaigns: tenant A vê exatamente 1 campanha
    SELECT COUNT(*) INTO v_count FROM config.campaigns;
    PERFORM pg_temp.assert_count('campaigns: tenant_a vê 1', v_count, 1);

    -- 1c. banners: tenant A vê exatamente 1 banner
    SELECT COUNT(*) INTO v_count FROM config.banners;
    PERFORM pg_temp.assert_count('banners: tenant_a vê 1', v_count, 1);

    -- 1d. sites: tenant A vê exatamente 1 site
    SELECT COUNT(*) INTO v_count FROM config.sites;
    PERFORM pg_temp.assert_count('sites: tenant_a vê 1', v_count, 1);

    -- 1e. zones: tenant A vê exatamente 1 zona
    SELECT COUNT(*) INTO v_count FROM config.zones;
    PERFORM pg_temp.assert_count('zones: tenant_a vê 1', v_count, 1);

    -- 1f. caps: tenant A vê exatamente 1 cap
    SELECT COUNT(*) INTO v_count FROM config.caps;
    PERFORM pg_temp.assert_count('caps: tenant_a vê 1', v_count, 1);

    -- 1g. delivery_rule_sets: tenant A vê exatamente 1 rule set
    SELECT COUNT(*) INTO v_count FROM config.delivery_rule_sets;
    PERFORM pg_temp.assert_count('delivery_rule_sets: tenant_a vê 1', v_count, 1);

    -- 1h. delivery_rules: tenant A vê exatamente 1 regra
    SELECT COUNT(*) INTO v_count FROM config.delivery_rules;
    PERFORM pg_temp.assert_count('delivery_rules: tenant_a vê 1', v_count, 1);

    -- 1i. campaign_zones via JOIN (forma canônica do motor Go):
    --     tenant A vê 1 vínculo (campaign_a ↔ zone_a), não vê o de B
    SELECT COUNT(*) INTO v_count
    FROM   config.campaign_zones cz
    JOIN   config.campaigns      c  ON c.id  = cz.campaign_id
    JOIN   config.zones          z  ON z.id  = cz.zone_id;
    PERFORM pg_temp.assert_count('campaign_zones JOIN: tenant_a vê 1', v_count, 1);

    -- 1j. campaign_zones acesso direto (defesa-em-profundidade — 0003_up):
    --     RLS via subquery EXISTS em campaigns filtra pelo tenant_id da sessão.
    --     Tenant A só deve ver 1 linha (o vínculo da própria campanha).
    SELECT COUNT(*) INTO v_count FROM config.campaign_zones;
    PERFORM pg_temp.assert_count('campaign_zones direto (RLS 0003): tenant_a vê 1', v_count, 1);

    -- Retorna ao role superuser para os próximos blocos
    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 2 — Tenant B enxerga apenas seus próprios dados (0 linhas de A)
-- ===========================================================================

DO $$
DECLARE
    v_tenant_b UUID := 'bbbbbbbb-0000-0000-0000-000000000001';
    v_count    BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'bbbbbbbb-0000-0000-0000-000000000001';

    -- 2a. advertisers
    SELECT COUNT(*) INTO v_count FROM config.advertisers;
    PERFORM pg_temp.assert_count('advertisers: tenant_b vê 1', v_count, 1);

    -- 2b. campaigns
    SELECT COUNT(*) INTO v_count FROM config.campaigns;
    PERFORM pg_temp.assert_count('campaigns: tenant_b vê 1', v_count, 1);

    -- 2c. banners
    SELECT COUNT(*) INTO v_count FROM config.banners;
    PERFORM pg_temp.assert_count('banners: tenant_b vê 1', v_count, 1);

    -- 2d. sites
    SELECT COUNT(*) INTO v_count FROM config.sites;
    PERFORM pg_temp.assert_count('sites: tenant_b vê 1', v_count, 1);

    -- 2e. zones
    SELECT COUNT(*) INTO v_count FROM config.zones;
    PERFORM pg_temp.assert_count('zones: tenant_b vê 1', v_count, 1);

    -- 2f. caps
    SELECT COUNT(*) INTO v_count FROM config.caps;
    PERFORM pg_temp.assert_count('caps: tenant_b vê 1', v_count, 1);

    -- 2g. delivery_rule_sets
    SELECT COUNT(*) INTO v_count FROM config.delivery_rule_sets;
    PERFORM pg_temp.assert_count('delivery_rule_sets: tenant_b vê 1', v_count, 1);

    -- 2h. delivery_rules
    SELECT COUNT(*) INTO v_count FROM config.delivery_rules;
    PERFORM pg_temp.assert_count('delivery_rules: tenant_b vê 1', v_count, 1);

    -- 2i. campaign_zones via JOIN
    SELECT COUNT(*) INTO v_count
    FROM   config.campaign_zones cz
    JOIN   config.campaigns      c  ON c.id  = cz.campaign_id
    JOIN   config.zones          z  ON z.id  = cz.zone_id;
    PERFORM pg_temp.assert_count('campaign_zones JOIN: tenant_b vê 1', v_count, 1);

    -- 2j. campaign_zones acesso direto (RLS 0003)
    SELECT COUNT(*) INTO v_count FROM config.campaign_zones;
    PERFORM pg_temp.assert_count('campaign_zones direto (RLS 0003): tenant_b vê 1', v_count, 1);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 3 — Cross-tenant: tenant A não lê linhas cujo tenant_id = B
--   (prova explícita de isolamento, não apenas contagem total)
-- ===========================================================================

DO $$
DECLARE
    v_tenant_b_uuid UUID    := 'bbbbbbbb-0000-0000-0000-000000000001';
    v_count         BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'aaaaaaaa-0000-0000-0000-000000000001';

    -- 3a. Tenant A não consegue ver advertisers de tenant B diretamente
    SELECT COUNT(*) INTO v_count
    FROM config.advertisers
    WHERE tenant_id = v_tenant_b_uuid;
    PERFORM pg_temp.assert_count('cross-tenant advertisers: A não vê B', v_count, 0);

    -- 3b. Tenant A não consegue ver campaigns de tenant B
    SELECT COUNT(*) INTO v_count
    FROM config.campaigns
    WHERE tenant_id = v_tenant_b_uuid;
    PERFORM pg_temp.assert_count('cross-tenant campaigns: A não vê B', v_count, 0);

    -- 3c. Tenant A não consegue ver zones de tenant B
    SELECT COUNT(*) INTO v_count
    FROM config.zones
    WHERE tenant_id = v_tenant_b_uuid;
    PERFORM pg_temp.assert_count('cross-tenant zones: A não vê B', v_count, 0);

    -- 3d. Campaign_zones: mesmo filtrando pelo campaign_id de B, A não vê
    --     (a subquery EXISTS em campaigns descarta a linha de B por RLS)
    SELECT COUNT(*) INTO v_count
    FROM config.campaign_zones cz
    WHERE EXISTS (
        SELECT 1 FROM config.campaigns c
        WHERE  c.id = cz.campaign_id
          AND  c.tenant_id = v_tenant_b_uuid
    );
    PERFORM pg_temp.assert_count('cross-tenant campaign_zones: A não vê vínculos de B', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 4 — Fail-closed: sem adserver.tenant_id setado → 0 linhas em tudo
--   current_tenant_id() retorna NULL quando GUC não está definido.
--   NULL = config.current_tenant_id() é sempre FALSE → policy rejeita toda linha.
-- ===========================================================================

DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    -- Intencionalmente NÃO setamos adserver.tenant_id
    -- (reset explícito caso outra sessão tenha setado)
    SET LOCAL adserver.tenant_id = '';

    -- 4a. advertisers: 0 linhas
    SELECT COUNT(*) INTO v_count FROM config.advertisers;
    PERFORM pg_temp.assert_count('fail-closed advertisers (tenant_id vazio): 0', v_count, 0);

    -- 4b. campaigns: 0 linhas
    SELECT COUNT(*) INTO v_count FROM config.campaigns;
    PERFORM pg_temp.assert_count('fail-closed campaigns (tenant_id vazio): 0', v_count, 0);

    -- 4c. banners: 0 linhas
    SELECT COUNT(*) INTO v_count FROM config.banners;
    PERFORM pg_temp.assert_count('fail-closed banners (tenant_id vazio): 0', v_count, 0);

    -- 4d. sites: 0 linhas
    SELECT COUNT(*) INTO v_count FROM config.sites;
    PERFORM pg_temp.assert_count('fail-closed sites (tenant_id vazio): 0', v_count, 0);

    -- 4e. zones: 0 linhas
    SELECT COUNT(*) INTO v_count FROM config.zones;
    PERFORM pg_temp.assert_count('fail-closed zones (tenant_id vazio): 0', v_count, 0);

    -- 4f. caps: 0 linhas
    SELECT COUNT(*) INTO v_count FROM config.caps;
    PERFORM pg_temp.assert_count('fail-closed caps (tenant_id vazio): 0', v_count, 0);

    -- 4g. delivery_rule_sets: 0 linhas
    SELECT COUNT(*) INTO v_count FROM config.delivery_rule_sets;
    PERFORM pg_temp.assert_count('fail-closed delivery_rule_sets (tenant_id vazio): 0', v_count, 0);

    -- 4h. delivery_rules: 0 linhas
    SELECT COUNT(*) INTO v_count FROM config.delivery_rules;
    PERFORM pg_temp.assert_count('fail-closed delivery_rules (tenant_id vazio): 0', v_count, 0);

    -- 4i. campaign_zones direto: 0 linhas (policy EXISTS em campaigns retorna 0)
    SELECT COUNT(*) INTO v_count FROM config.campaign_zones;
    PERFORM pg_temp.assert_count('fail-closed campaign_zones (tenant_id vazio): 0', v_count, 0);

    -- 4j. campaign_zones via JOIN: também 0 (campaigns retorna 0 linhas)
    SELECT COUNT(*) INTO v_count
    FROM   config.campaign_zones cz
    JOIN   config.campaigns      c ON c.id = cz.campaign_id;
    PERFORM pg_temp.assert_count('fail-closed campaign_zones JOIN (tenant_id vazio): 0', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 5 — Superuser bypassa RLS (comportamento documentado e esperado)
--   Migrations rodam como superuser; devem ver TODOS os dados.
-- ===========================================================================

DO $$
DECLARE
    v_count BIGINT;
BEGIN
    -- Sem SET ROLE: continua como adserver_admin (superuser)
    -- Superuser bypassa FORCE ROW LEVEL SECURITY por padrão no PG.

    -- 5a. Total de advertisers deve ser 2 (A + B)
    SELECT COUNT(*) INTO v_count FROM config.advertisers
    WHERE tenant_id IN (
        'aaaaaaaa-0000-0000-0000-000000000001',
        'bbbbbbbb-0000-0000-0000-000000000001'
    );
    PERFORM pg_temp.assert_count('superuser vê ambos advertisers (bypass RLS)', v_count, 2);

    -- 5b. campaign_zones: superuser vê 2 vínculos
    SELECT COUNT(*) INTO v_count
    FROM config.campaign_zones cz
    JOIN config.campaigns c ON c.id = cz.campaign_id
    WHERE c.tenant_id IN (
        'aaaaaaaa-0000-0000-0000-000000000001',
        'bbbbbbbb-0000-0000-0000-000000000001'
    );
    PERFORM pg_temp.assert_count('superuser vê ambos campaign_zones (bypass RLS)', v_count, 2);
END;
$$;


-- ===========================================================================
-- BLOCO 6 — WITH CHECK: o banco REJEITA escrita cross-tenant (INSERT/UPDATE)
--   Os BLOCOS 1–4 provam só a LEITURA. Este bloco prova a ESCRITA — espelha o
--   Bloco 7 do rls_isolation_test.sql do ledger. Como adserver_app com o tenant
--   A na sessão, toda tentativa de gravar linha do tenant B deve abortar com
--   SQLSTATE 42501 (new row violates row-level security policy). Cada tentativa
--   roda num sub-bloco BEGIN/EXCEPTION (savepoint), então uma rejeição esperada
--   não aborta a transação de teste; uma ACEITAÇÃO inesperada faz RAISE (falha).
-- ===========================================================================

DO $$
DECLARE
    v_tenant_b UUID := 'bbbbbbbb-0000-0000-0000-000000000001';
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'aaaaaaaa-0000-0000-0000-000000000001';

    -- 6a. INSERT com tenant_id forjado (B) enquanto a sessão é o tenant A.
    BEGIN
        INSERT INTO config.sites (tenant_id, name, url)
        VALUES (v_tenant_b, 'Site Forjado', 'https://forjado.example');
        RAISE EXCEPTION
            'ASSERT FALHOU [WITH CHECK INSERT]: INSERT cross-tenant ACEITO (esperado 42501)';
    EXCEPTION WHEN insufficient_privilege THEN
        RAISE NOTICE 'PASS [WITH CHECK: INSERT forjado (tenant B) rejeitado]: SQLSTATE=%', SQLSTATE;
    END;

    -- 6b. UPDATE tentando mover uma linha do tenant A para o tenant B.
    BEGIN
        UPDATE config.sites SET tenant_id = v_tenant_b WHERE name = 'Site A';
        RAISE EXCEPTION
            'ASSERT FALHOU [WITH CHECK UPDATE]: UPDATE tenant-flip A→B ACEITO (esperado 42501)';
    EXCEPTION WHEN insufficient_privilege THEN
        RAISE NOTICE 'PASS [WITH CHECK: UPDATE tenant-flip (A→B) rejeitado]: SQLSTATE=%', SQLSTATE;
    END;

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- ROLLBACK: não persiste nenhum dado de teste no banco
-- ===========================================================================

ROLLBACK;

-- Se chegou aqui sem RAISE EXCEPTION: todos os asserts passaram.
\echo '== RLS ISOLATION: ALL TESTS PASSED =='
