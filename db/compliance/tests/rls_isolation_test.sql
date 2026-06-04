-- =============================================================================
-- rls_isolation_test.sql — Teste de isolamento RLS por tenant (K6 / TX-3 / DA-11)
--
-- PROPÓSITO
--   Provar que o RLS do schema compliance impede tenant A de ler dados de
--   tenant B nas tres tabelas do cofre (kyc_subjects, screening_results,
--   travel_rule_records). Inclui o caso fail-closed obrigatorio:
--   sem adserver.tenant_id setado -> 0 linhas em tudo.
--
-- PADRÃO
--   Identico ao db/config/tests/rls_isolation_test.sql (padrao canonico).
--   Usa config.current_tenant_id() via adserver.tenant_id (SET LOCAL).
--
-- DEPENDÊNCIAS
--   - Postgres 16 com migration 0001_compliance_schema_up.sql aplicada.
--   - Role adserver_app (nao-superuser) com GRANTs no schema compliance.
--   - config.current_tenant_id() disponivel (0002_config_rls_up.sql aplicada).
--
-- COMO RODAR
--   psql -U adserver_admin -d adserver \
--        -v ON_ERROR_STOP=1 \
--        -f db/compliance/tests/rls_isolation_test.sql
--
--   Resultado esperado: "== COMPLIANCE RLS ISOLATION: ALL TESTS PASSED ==" ao final.
--   Qualquer assert falho aborta com RAISE EXCEPTION (exit code != 0).
--
-- NOTAS
--   - Roda dentro de ROLLBACK — nao persiste dados (idempotente, seguro em CI).
--   - O superusuario bypassa RLS (documentado e esperado — migrations rodam
--     como superuser).
--   - UUIDs de tenant determinísticos e distintos dos usados em outros testes
--     para evitar colisao em ambiente compartilhado.
-- =============================================================================

BEGIN;

-- ---------------------------------------------------------------------------
-- SETUP: fixtures para dois tenants distintos
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    v_tenant_a UUID := 'eeeeeeee-0000-0000-0000-000000000001';
    v_tenant_b UUID := 'ffffffff-0000-0000-0000-000000000001';
BEGIN
    -- Garante que adserver_app existe
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'adserver_app') THEN
        RAISE EXCEPTION
            'Role adserver_app nao existe. Crie-a antes de rodar este teste. '
            'Ver db/config/tests/rls_isolation_test.sql para instrucoes.';
    END IF;

    -- kyc_subjects: um registro por tenant (como superuser — bypass RLS)
    INSERT INTO compliance.kyc_subjects
        (tenant_id, subject_ref, sumsub_applicant_id, level, status)
    VALUES
        (v_tenant_a, '11111111-0000-0000-0000-000000000001',
         'sumsub_applicant_a_001', 'kyc', 'approved'),
        (v_tenant_b, '22222222-0000-0000-0000-000000000001',
         'sumsub_applicant_b_001', 'kyc', 'pending');

    -- screening_results: um registro por tenant
    -- risk_score e INTEGER 0-100 (TX-2: sem float)
    INSERT INTO compliance.screening_results
        (tenant_id, address, direction, risk_score, sanctions_hit, category, tx_ref)
    VALUES
        (v_tenant_a, '0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
         'deposit', 10, false, 'none', 'tx_a_001'),
        (v_tenant_b, '0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB',
         'deposit', 100, true, 'sanctions', 'tx_b_001');

    -- travel_rule_records: um registro por tenant
    -- amount_minor_units NUMERIC(20,0) — TX-2: sem float
    INSERT INTO compliance.travel_rule_records
        (tenant_id, tx_ref, originator_vasp, beneficiary_vasp,
         threshold_currency, amount_minor_units, status)
    VALUES
        (v_tenant_a, 'payout_a_001', 'SAFE_PLATFORM', 'EXCHANGE_ABC',
         'USDC', 1000000000, 'pending'),
        (v_tenant_b, 'payout_b_001', 'SAFE_PLATFORM', 'EXCHANGE_XYZ',
         'USDC', 5000000000, 'sent');

    RAISE NOTICE 'Setup concluido: tenant_a=% tenant_b=%', v_tenant_a, v_tenant_b;
END;
$$;


-- ---------------------------------------------------------------------------
-- HELPER: funcao de assert local
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
-- BLOCO 1 — Tenant A enxerga apenas seus proprios dados (0 linhas de B)
-- ===========================================================================

DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'eeeeeeee-0000-0000-0000-000000000001';

    -- 1a. kyc_subjects: tenant A ve exatamente 1 registro
    SELECT COUNT(*) INTO v_count FROM compliance.kyc_subjects;
    PERFORM pg_temp.assert_count('kyc_subjects: tenant_a ve 1', v_count, 1);

    -- 1b. screening_results: tenant A ve exatamente 1 resultado
    SELECT COUNT(*) INTO v_count FROM compliance.screening_results;
    PERFORM pg_temp.assert_count('screening_results: tenant_a ve 1', v_count, 1);

    -- 1c. travel_rule_records: tenant A ve exatamente 1 registro
    SELECT COUNT(*) INTO v_count FROM compliance.travel_rule_records;
    PERFORM pg_temp.assert_count('travel_rule_records: tenant_a ve 1', v_count, 1);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 2 — Tenant B enxerga apenas seus proprios dados (0 linhas de A)
-- ===========================================================================

DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'ffffffff-0000-0000-0000-000000000001';

    -- 2a. kyc_subjects: tenant B ve exatamente 1 registro
    SELECT COUNT(*) INTO v_count FROM compliance.kyc_subjects;
    PERFORM pg_temp.assert_count('kyc_subjects: tenant_b ve 1', v_count, 1);

    -- 2b. screening_results: tenant B ve exatamente 1 resultado
    SELECT COUNT(*) INTO v_count FROM compliance.screening_results;
    PERFORM pg_temp.assert_count('screening_results: tenant_b ve 1', v_count, 1);

    -- 2c. travel_rule_records: tenant B ve exatamente 1 registro
    SELECT COUNT(*) INTO v_count FROM compliance.travel_rule_records;
    PERFORM pg_temp.assert_count('travel_rule_records: tenant_b ve 1', v_count, 1);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 3 — Cross-tenant: tenant A nao le linhas de tenant B
-- ===========================================================================

DO $$
DECLARE
    v_tenant_b UUID := 'ffffffff-0000-0000-0000-000000000001';
    v_count    BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'eeeeeeee-0000-0000-0000-000000000001';

    -- 3a. kyc_subjects: A nao ve registros de B, mesmo filtrando por tenant_id de B
    SELECT COUNT(*) INTO v_count
    FROM compliance.kyc_subjects
    WHERE tenant_id = v_tenant_b;
    PERFORM pg_temp.assert_count('cross-tenant kyc_subjects: A nao ve B', v_count, 0);

    -- 3b. screening_results: A nao ve resultados de B
    SELECT COUNT(*) INTO v_count
    FROM compliance.screening_results
    WHERE tenant_id = v_tenant_b;
    PERFORM pg_temp.assert_count('cross-tenant screening_results: A nao ve B', v_count, 0);

    -- 3c. travel_rule_records: A nao ve registros de B
    SELECT COUNT(*) INTO v_count
    FROM compliance.travel_rule_records
    WHERE tenant_id = v_tenant_b;
    PERFORM pg_temp.assert_count('cross-tenant travel_rule_records: A nao ve B', v_count, 0);

    -- 3d. Screening com sanctions_hit=TRUE de B nao e visivel para A
    --     (prova que A nao pode verificar o bloqueio de sancoes de B)
    SELECT COUNT(*) INTO v_count
    FROM compliance.screening_results
    WHERE tenant_id = v_tenant_b AND sanctions_hit = true;
    PERFORM pg_temp.assert_count('cross-tenant sanctions_hit: A nao ve bloqueio de B', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 4 — Fail-closed: sem adserver.tenant_id setado -> 0 linhas em tudo
--   compliance.current_tenant_id() retorna NULL quando GUC nao esta definido.
--   NULL = compliance.current_tenant_id() e sempre FALSE -> policy rejeita.
-- ===========================================================================

DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    -- Intencionalmente NAO setamos adserver.tenant_id
    SET LOCAL adserver.tenant_id = '';

    -- 4a. kyc_subjects: 0 linhas (fail-closed)
    SELECT COUNT(*) INTO v_count FROM compliance.kyc_subjects;
    PERFORM pg_temp.assert_count('fail-closed kyc_subjects (tenant_id vazio): 0', v_count, 0);

    -- 4b. screening_results: 0 linhas (fail-closed)
    SELECT COUNT(*) INTO v_count FROM compliance.screening_results;
    PERFORM pg_temp.assert_count('fail-closed screening_results (tenant_id vazio): 0', v_count, 0);

    -- 4c. travel_rule_records: 0 linhas (fail-closed)
    SELECT COUNT(*) INTO v_count FROM compliance.travel_rule_records;
    PERFORM pg_temp.assert_count('fail-closed travel_rule_records (tenant_id vazio): 0', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 5 — Superuser bypassa RLS (comportamento documentado e esperado)
-- ===========================================================================

DO $$
DECLARE
    v_count BIGINT;
BEGIN
    -- Sem SET ROLE: continua como adserver_admin (superuser) — bypassa RLS.

    -- 5a. kyc_subjects: superuser ve 2 registros (A + B)
    SELECT COUNT(*) INTO v_count FROM compliance.kyc_subjects
    WHERE tenant_id IN (
        'eeeeeeee-0000-0000-0000-000000000001',
        'ffffffff-0000-0000-0000-000000000001'
    );
    PERFORM pg_temp.assert_count('superuser ve ambos kyc_subjects (bypass RLS)', v_count, 2);

    -- 5b. screening_results: superuser ve 2 registros
    SELECT COUNT(*) INTO v_count FROM compliance.screening_results
    WHERE tenant_id IN (
        'eeeeeeee-0000-0000-0000-000000000001',
        'ffffffff-0000-0000-0000-000000000001'
    );
    PERFORM pg_temp.assert_count('superuser ve ambos screening_results (bypass RLS)', v_count, 2);

    -- 5c. travel_rule_records: superuser ve 2 registros
    SELECT COUNT(*) INTO v_count FROM compliance.travel_rule_records
    WHERE tenant_id IN (
        'eeeeeeee-0000-0000-0000-000000000001',
        'ffffffff-0000-0000-0000-000000000001'
    );
    PERFORM pg_temp.assert_count('superuser ve ambos travel_rule_records (bypass RLS)', v_count, 2);
END;
$$;


-- ===========================================================================
-- ROLLBACK: nao persiste nenhum dado de teste
-- ===========================================================================

ROLLBACK;

\echo '== COMPLIANCE RLS ISOLATION: ALL TESTS PASSED =='
