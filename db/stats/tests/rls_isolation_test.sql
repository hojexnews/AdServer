-- =============================================================================
-- rls_isolation_test.sql — Teste de isolamento RLS por tenant do schema
-- stats (ADR-0005 "perfil BETA" / TX-3 / §2.6)
--
-- PROPÓSITO (achado M-2, security-reviewer)
--   Prova o lado USING (LEITURA) da policy stats.events_raw_tenant_isolation:
--   que tenant A não consegue SELECT em eventos de tenant B, nem em
--   stats.events_raw nem através da view stats.live_kpis. Antes deste
--   arquivo, o lado WITH CHECK (ESCRITA) já estava provado por
--   internal/telemetry/pgsink/pgsink_integration_test.go
--   (TestIntegration_RLS_StatsWriterRoleRejectsCrossTenant, com
--   `SET ROLE adserver_stats_writer` exigindo SQLSTATE 42501) — mas nenhum
--   teste executável do repo tentava um SELECT cross-tenant. É exatamente a
--   classe de lacuna que o WITH CHECK NÃO cobre: SELECT nunca passa por
--   WITH CHECK, só por USING.
--
--   Cobre também o caso fail-closed (sem adserver.tenant_id setado → 0
--   linhas) e o caso do superuser (bypassa RLS por padrão — migrations
--   rodam como superuser e precisam continuar vendo tudo).
--
-- MOLDE
--   Estrutura idêntica a db/config/tests/rls_isolation_test.sql (mesmos
--   nomes de bloco, mesmo helper pg_temp.assert_count, mesma convenção de
--   BEGIN...ROLLBACK) — ver aquele arquivo para o padrão canônico do repo.
--
-- DEPENDÊNCIAS
--   - Postgres 16 com db/stats/migrations/0001_stats_schema_up.sql aplicada.
--   - adserver_app (não-superuser) com GRANT USAGE ON SCHEMA stats,
--     GRANT SELECT ON stats.events_raw, GRANT SELECT ON stats.live_kpis e
--     GRANT EXECUTE ON FUNCTION stats.current_tenant_id() — GRANTs
--     comentados na própria migration (achado H-2), executados por
--     make/dev.mk::dev-db-setup ou .github/workflows/db.yml, sempre DEPOIS
--     de db/seed/dev_roles.sql garantir que adserver_app existe.
--   - Superuser (adserver_admin, ou quem quer que seja o dono de
--     DATABASE_URL) para o setup e para o Bloco 5.
--
-- COMO RODAR
--   make db-migrate-up   # ou: aplicar db/stats/migrations/0001 diretamente
--   # garantir que adserver_app tem os GRANTs acima (make dev-db-setup faz
--   # isso; make db-test-all também)
--   make db-test-stats
--
--   Resultado esperado: linha "== STATS RLS ISOLATION: ALL TESTS PASSED =="
--   ao final. Qualquer ASSERT falho aborta com RAISE EXCEPTION (exit != 0).
--
-- PROVA POR MUTAÇÃO (executada de 1ª mão contra Postgres 16.14 real, num
--   scratch DB isolado — não é suposição)
--   Trocar `USING (tenant_id = stats.current_tenant_id())` por
--   `USING (true)` na policy events_raw_tenant_isolation
--   (0001_stats_schema_up.sql) e rodar este arquivo de novo: os Blocos 1-4
--   ficam VERMELHOS — MEDIDO: tenant A passa a enxergar as 8 linhas de A+B
--   em vez de 4 (`obtido=8` no Bloco 1a), e o fail-closed do Bloco 4
--   devolveria linhas em vez de zero. Esta é a mutação que importa para
--   segurança: uma policy permissiva de leitura que ainda escreve certo
--   (WITH CHECK intacto) é um vazamento SILENCIOSO — sem este teste, nada
--   mais no repo pega isso (o WITH CHECK write-side em
--   pgsink_integration_test.go continua verde).
--
--   CORREÇÃO A UMA SUPOSIÇÃO INICIAL (medida, não suposta): remover a
--   cláusula USING inteira — deixando só WITH CHECK — NÃO é equivalente a
--   `USING (true)`, nem reusa a expressão de WITH CHECK como fallback (isso
--   só acontece na direção OPOSTA — WITH CHECK ausente reusando USING, ver
--   BLOCO 5.5 de db/config/tests/rls_isolation_test.sql). Medido de 1ª mão:
--   uma policy só-com-WITH-CHECK deixa `pg_policy.polqual` NULL e SELECT sob
--   essa policy devolve ZERO linhas para QUALQUER tenant — mais restritiva
--   que o fail-closed, não mais permissiva. Este Bloco 1 pega esse caso
--   também (obtido=0 em vez de 4), só que como uma quebra óbvia (dashboard
--   vazio para todo mundo) em vez de um vazamento silencioso — por isso o
--   caso perigoso a testar de propósito é `USING (true)`, não a omissão.
--
-- NOTAS
--   - O teste roda dentro de uma transação única e faz ROLLBACK ao final —
--     não persiste dados no banco (idempotente, seguro para dev/CI).
--   - SET LOCAL só afeta a transação corrente; sem necessidade de cleanup.
--   - O superuser bypassa RLS por padrão; por isso fazemos SET ROLE
--     adserver_app antes de cada bloco de verificação (mesmo idioma de
--     db/config/tests/rls_isolation_test.sql).
-- =============================================================================

BEGIN;

-- ---------------------------------------------------------------------------
-- SETUP: fixtures para dois tenants distintos, cobrindo os quatro
-- event_type (ad_request tem campaign_id NULL por construção — ver
-- cabeçalho de 0001_stats_schema_up.sql, seção "AdRequest / requests").
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    v_tenant_a UUID := 'aaaaaaaa-5747-0000-0000-000000000001';
    v_tenant_b UUID := 'bbbbbbbb-5747-0000-0000-000000000001';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'adserver_app') THEN
        RAISE EXCEPTION
            'Role adserver_app não existe. Crie-a antes de rodar este teste '
            '(db/seed/dev_roles.sql) e conceda os GRANTs de leitura em stats '
            '(ver cabeçalho deste arquivo, seção DEPENDÊNCIAS).';
    END IF;

    -- Tenant A: um ad_request (campaign_id NULL) + uma campanha completa
    -- (impressão billable + click + conversion, campaign_id = 1001).
    INSERT INTO stats.events_raw
        (event_id, event_type, tenant_id, campaign_id, billable, blank, occurred_at)
    VALUES
        ('stats-rls-a-adreq', 'ad_request', v_tenant_a, NULL,  false, false, now()),
        ('stats-rls-a-imp',   'impression', v_tenant_a, 1001,  true,  false, now()),
        ('stats-rls-a-click', 'click',      v_tenant_a, 1001,  false, false, now()),
        ('stats-rls-a-conv',  'conversion', v_tenant_a, 1001,  false, false, now());

    -- Tenant B: mesma forma, campaign_id = 2001.
    INSERT INTO stats.events_raw
        (event_id, event_type, tenant_id, campaign_id, billable, blank, occurred_at)
    VALUES
        ('stats-rls-b-adreq', 'ad_request', v_tenant_b, NULL,  false, false, now()),
        ('stats-rls-b-imp',   'impression', v_tenant_b, 2001,  true,  false, now()),
        ('stats-rls-b-click', 'click',      v_tenant_b, 2001,  false, false, now()),
        ('stats-rls-b-conv',  'conversion', v_tenant_b, 2001,  false, false, now());

    RAISE NOTICE 'Setup concluído: tenant_a=% tenant_b=% (4 eventos cada, 2 linhas em live_kpis cada)',
        v_tenant_a, v_tenant_b;
END;
$$;

-- ---------------------------------------------------------------------------
-- HELPER: função de assert para uso local nesta sessão de teste (idêntica
-- em forma a db/config/tests/rls_isolation_test.sql).
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
-- BLOCO 1 — Tenant A enxerga só seus próprios eventos (USING, leitura)
-- ===========================================================================
DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'aaaaaaaa-5747-0000-0000-000000000001';

    -- 1a. events_raw: tenant A vê exatamente os 4 eventos próprios.
    SELECT COUNT(*) INTO v_count FROM stats.events_raw;
    PERFORM pg_temp.assert_count('events_raw: tenant_a vê 4 (não vê os 4 de B)', v_count, 4);

    -- 1b. live_kpis: tenant A vê exatamente 2 linhas — (tenant_a, NULL) do
    --     ad_request e (tenant_a, 1001) da campanha.
    SELECT COUNT(*) INTO v_count FROM stats.live_kpis;
    PERFORM pg_temp.assert_count('live_kpis: tenant_a vê 2 linhas de grupo', v_count, 2);

    -- 1c. live_kpis: a linha por-campanha reflete os KPIs corretos e não
    --     vaza nada da campanha 2001 de B.
    PERFORM pg_temp.assert_count('live_kpis campaign 1001: impressions',
        (SELECT impressions FROM stats.live_kpis WHERE tenant_id = 'aaaaaaaa-5747-0000-0000-000000000001' AND campaign_id = 1001), 1);
    PERFORM pg_temp.assert_count('live_kpis campaign 1001: clicks',
        (SELECT clicks FROM stats.live_kpis WHERE tenant_id = 'aaaaaaaa-5747-0000-0000-000000000001' AND campaign_id = 1001), 1);
    PERFORM pg_temp.assert_count('live_kpis campaign 1001: conversions',
        (SELECT conversions FROM stats.live_kpis WHERE tenant_id = 'aaaaaaaa-5747-0000-0000-000000000001' AND campaign_id = 1001), 1);

    -- 1d. live_kpis: a linha de requests (campaign_id IS NULL) existe e vale 1.
    PERFORM pg_temp.assert_count('live_kpis requests (campaign_id IS NULL)',
        (SELECT requests FROM stats.live_kpis WHERE tenant_id = 'aaaaaaaa-5747-0000-0000-000000000001' AND campaign_id IS NULL), 1);

    -- 1e. Tenant A não vê a campanha 2001 (é de B) mesmo perguntando direto.
    SELECT COUNT(*) INTO v_count FROM stats.live_kpis WHERE campaign_id = 2001;
    PERFORM pg_temp.assert_count('live_kpis: tenant_a não vê campaign_id=2001 (de B)', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 2 — Tenant B enxerga só seus próprios eventos (USING, leitura)
-- ===========================================================================
DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'bbbbbbbb-5747-0000-0000-000000000001';

    SELECT COUNT(*) INTO v_count FROM stats.events_raw;
    PERFORM pg_temp.assert_count('events_raw: tenant_b vê 4 (não vê os 4 de A)', v_count, 4);

    SELECT COUNT(*) INTO v_count FROM stats.live_kpis;
    PERFORM pg_temp.assert_count('live_kpis: tenant_b vê 2 linhas de grupo', v_count, 2);

    PERFORM pg_temp.assert_count('live_kpis campaign 2001: impressions',
        (SELECT impressions FROM stats.live_kpis WHERE tenant_id = 'bbbbbbbb-5747-0000-0000-000000000001' AND campaign_id = 2001), 1);

    -- Tenant B não vê a campanha 1001 (é de A).
    SELECT COUNT(*) INTO v_count FROM stats.live_kpis WHERE campaign_id = 1001;
    PERFORM pg_temp.assert_count('live_kpis: tenant_b não vê campaign_id=1001 (de A)', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 3 — Cross-tenant explícito: tenant A não lê linhas com
--   tenant_id = B mesmo perguntando por elas diretamente (prova mais forte
--   que uma simples contagem total — descarta "a policy filtra por acidente
--   por causa da minha própria query", força a policy a agir mesmo com um
--   WHERE que tenta contornar).
-- ===========================================================================
DO $$
DECLARE
    v_tenant_b_uuid UUID := 'bbbbbbbb-5747-0000-0000-000000000001';
    v_count         BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'aaaaaaaa-5747-0000-0000-000000000001';

    SELECT COUNT(*) INTO v_count FROM stats.events_raw WHERE tenant_id = v_tenant_b_uuid;
    PERFORM pg_temp.assert_count('cross-tenant events_raw: A não vê B via WHERE explícito', v_count, 0);

    SELECT COUNT(*) INTO v_count FROM stats.live_kpis WHERE tenant_id = v_tenant_b_uuid;
    PERFORM pg_temp.assert_count('cross-tenant live_kpis: A não vê B via WHERE explícito', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 4 — Fail-closed: sem adserver.tenant_id setado → 0 linhas em tudo.
--   stats.current_tenant_id() retorna NULL quando o GUC está ausente;
--   `tenant_id = NULL` é sempre NULL (nunca TRUE) → a policy rejeita
--   toda linha (idêntico ao comportamento provado para config/ledger/vector/
--   compliance).
-- ===========================================================================
DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    -- Intencionalmente NÃO setamos adserver.tenant_id (string vazia =
    -- current_setting(..., true) devolve '' → NULLIF(...,'') → NULL).
    SET LOCAL adserver.tenant_id = '';

    SELECT COUNT(*) INTO v_count FROM stats.events_raw;
    PERFORM pg_temp.assert_count('fail-closed events_raw (tenant_id vazio): 0', v_count, 0);

    SELECT COUNT(*) INTO v_count FROM stats.live_kpis;
    PERFORM pg_temp.assert_count('fail-closed live_kpis (tenant_id vazio): 0', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 5 — Superuser bypassa RLS (comportamento documentado e esperado —
--   migrations e a role dedicada de escrita adserver_stats_writer também
--   dependem disto continuar valendo para ferramentas administrativas).
-- ===========================================================================
DO $$
DECLARE
    v_count BIGINT;
BEGIN
    -- Sem SET ROLE: continua como o role de DATABASE_URL (superuser em
    -- dev/CI). Superuser bypassa FORCE ROW LEVEL SECURITY por padrão no PG.
    SELECT COUNT(*) INTO v_count FROM stats.events_raw
    WHERE tenant_id IN ('aaaaaaaa-5747-0000-0000-000000000001', 'bbbbbbbb-5747-0000-0000-000000000001');
    PERFORM pg_temp.assert_count('superuser vê os 8 eventos de A+B (bypass RLS)', v_count, 8);

    SELECT COUNT(*) INTO v_count FROM stats.live_kpis
    WHERE tenant_id IN ('aaaaaaaa-5747-0000-0000-000000000001', 'bbbbbbbb-5747-0000-0000-000000000001');
    PERFORM pg_temp.assert_count('superuser vê as 4 linhas de live_kpis de A+B (bypass RLS)', v_count, 4);
END;
$$;


-- ===========================================================================
-- BLOCO 5.5 — Anti-tautologia: confirma WITH CHECK explícito no catálogo
--   pg_policy para a policy de stats.events_raw (mesmo padrão de
--   db/config/tests/rls_isolation_test.sql BLOCO 5.5). Não é o foco deste
--   arquivo (o WITH CHECK já é provado em pgsink_integration_test.go), mas
--   é uma checagem barata que fecha o mesmo ponto-cego de catálogo aqui.
-- ===========================================================================
DO $$
DECLARE
    v_rec     RECORD;
    v_missing TEXT := '';
    v_found   INT  := 0;
BEGIN
    FOR v_rec IN
        SELECT p.polname,
               p.polrelid::regclass::text AS relname,
               p.polwithcheck
        FROM   pg_policy   p
        JOIN   pg_class    c ON c.oid = p.polrelid
        JOIN   pg_namespace n ON n.oid = c.relnamespace
        WHERE  n.nspname = 'stats'
    LOOP
        v_found := v_found + 1;
        IF v_rec.polwithcheck IS NULL THEN
            v_missing := v_missing || format('%s.%s ', v_rec.relname, v_rec.polname);
        END IF;
    END LOOP;

    IF v_found < 1 THEN
        RAISE EXCEPTION
            'ASSERT FALHOU [pg_policy vacuo]: schema stats tem 0 policies (schema não migrado?)';
    END IF;

    IF v_missing <> '' THEN
        RAISE EXCEPTION
            'ASSERT FALHOU [WITH CHECK ausente no catálogo pg_policy]: %', v_missing;
    END IF;

    RAISE NOTICE 'PASS [pg_policy.polwithcheck NOT NULL em todas as % policies do schema stats]', v_found;
END;
$$;


-- ===========================================================================
-- ROLLBACK: não persiste nenhum dado de teste no banco
-- ===========================================================================
ROLLBACK;

\echo '== STATS RLS ISOLATION: ALL TESTS PASSED =='
