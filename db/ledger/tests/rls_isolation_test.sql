-- =============================================================================
-- rls_isolation_test.sql — Teste de isolamento RLS por tenant (TX-3 / DA-11)
--
-- PROPOSITO
--   Provar que o RLS habilitado em 0003_ledger_rls_up.sql impede tenant A de
--   ler dados de tenant B nas tres tabelas core do ledger (accounts,
--   journal_entries, postings) e na view derivada account_balances.
--   Inclui o caso fail-closed obrigatorio: sem adserver.tenant_id setado
--   -> 0 linhas em tudo (nunca "todas as linhas").
--
-- PADRAO
--   Identico ao db/config/tests/rls_isolation_test.sql e
--   db/compliance/tests/rls_isolation_test.sql (padrao canonico do repo).
--   Usa SET LOCAL ROLE adserver_app + SET LOCAL adserver.tenant_id por bloco.
--   Funcao helper de assert: pg_temp.assert_count() (mesma assinatura).
--
-- DEPENDENCIAS
--   - Postgres 16 com migrations 0001, 0002, 0003 do schema ledger aplicadas.
--   - Role adserver_app (nao-superuser) com GRANTs em ledger.*:
--       GRANT USAGE ON SCHEMA ledger TO adserver_app;
--       GRANT SELECT, INSERT, UPDATE, DELETE
--         ON ALL TABLES IN SCHEMA ledger TO adserver_app;
--       GRANT USAGE ON ALL SEQUENCES IN SCHEMA ledger TO adserver_app;
--   - ledger.current_tenant_id() disponivel (criada pela 0003_up).
--
-- COMO RODAR
--   psql -U adserver_admin -d adserver \
--        -v ON_ERROR_STOP=1 \
--        -f db/ledger/tests/rls_isolation_test.sql
--
--   Resultado esperado: "== LEDGER RLS ISOLATION: ALL TESTS PASSED ==" ao final.
--   Qualquer assert falho aborta com RAISE EXCEPTION (exit code != 0).
--
-- NOTAS
--   - Roda dentro de BEGIN...ROLLBACK: nao persiste dados (idempotente, seguro em CI).
--   - UUIDs de tenant distintos dos usados em config/compliance para evitar
--     colisao em ambiente compartilhado.
--   - Superusuario bypassa RLS (comportamento documentado e esperado).
--   - Fixtures inseridas como superuser (bypass RLS) para setup controlado.
--   - postings: tabela particionada por RANGE(posted_at). O RLS da mae se
--     propaga para todas as particoes (PG16 — documentado em 0003_up ~L73-76).
--     Fixtures usam '2026-06-15' para cair na particao postings_2026_06.
--   - TX-2 obrigatorio: NENHUM valor monetario usa float. Todos os valores
--     de debit_amount/credit_amount/balance sao NUMERIC inteiros (minor units).
--   - Caso (d) de INSERT/UPDATE com WITH CHECK nao e aplicavel: as policies
--     da 0003 usam apenas clausula USING sem WITH CHECK (ver nota no final).
-- =============================================================================

BEGIN;

-- ---------------------------------------------------------------------------
-- SETUP: fixtures para dois tenants distintos
-- Inseridas como superuser (adserver_admin) que bypassa RLS.
-- UUIDs escolhidos para nao colidir com outros testes do repo:
--   compliance usa eeeeeeee-*/ffffffff-*; config usa aaaaaaaa-*/bbbbbbbb-*
--   ledger usa cccccccc-* / dddddddd-*
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    v_tenant_a  UUID   := 'cccccccc-0000-0000-0000-000000000001';
    v_tenant_b  UUID   := 'dddddddd-0000-0000-0000-000000000001';
    v_acct_a    BIGINT;
    v_acct_b    BIGINT;
    v_entry_a   BIGINT;
    v_entry_b   BIGINT;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'adserver_app') THEN
        RAISE EXCEPTION
            'Role adserver_app nao existe. Crie-a antes de rodar este teste. '
            'Ver cabecalho do arquivo para instrucoes.';
    END IF;

    -- accounts: uma conta por tenant
    -- kind='revenue' nao exige FK externa. asset_code='BRL'.
    INSERT INTO ledger.accounts (tenant_id, code, name, kind, asset_code)
    VALUES (v_tenant_a, 'rls-test:revenue:brl', 'Conta Teste Tenant A', 'revenue', 'BRL')
    RETURNING id INTO v_acct_a;

    INSERT INTO ledger.accounts (tenant_id, code, name, kind, asset_code)
    VALUES (v_tenant_b, 'rls-test:revenue:brl', 'Conta Teste Tenant B', 'revenue', 'BRL')
    RETURNING id INTO v_acct_b;

    -- journal_entries: um cabecalho por tenant
    INSERT INTO ledger.journal_entries
        (tenant_id, idempotency_key, description, status, effective_at)
    VALUES
        (v_tenant_a, 'rls-test-idem-a-001', 'Entrada de teste tenant A', 'posted', now())
    RETURNING id INTO v_entry_a;

    INSERT INTO ledger.journal_entries
        (tenant_id, idempotency_key, description, status, effective_at)
    VALUES
        (v_tenant_b, 'rls-test-idem-b-001', 'Entrada de teste tenant B', 'posted', now())
    RETURNING id INTO v_entry_b;

    -- postings: um par balanceado por tenant (debit=credito, TX-2: NUMERIC sem float).
    -- posted_at='2026-06-15': cai na particao postings_2026_06 (criada em 0001).
    -- Um posting de debito + um de credito na mesma entry satisfaz o constraint trigger
    -- postings_balance_chk_trg (DEFERRABLE INITIALLY DEFERRED: verifica no COMMIT).
    -- Valores em minor units (inteiros): 100000 = 1000,00 BRL (scale=2).
    INSERT INTO ledger.postings
        (journal_entry_id, tenant_id, account_id, asset_code, scale,
         debit_amount, credit_amount, posted_at)
    VALUES
        -- Tenant A: debito na conta A
        (v_entry_a, v_tenant_a, v_acct_a, 'BRL', 2,
         100000, 0, '2026-06-15 10:00:00+00'),
        -- Tenant A: credito na conta A (mesmo account_id, entry balanceada)
        (v_entry_a, v_tenant_a, v_acct_a, 'BRL', 2,
         0, 100000, '2026-06-15 10:00:00+00'),
        -- Tenant B: debito na conta B
        (v_entry_b, v_tenant_b, v_acct_b, 'BRL', 2,
         500000, 0, '2026-06-15 11:00:00+00'),
        -- Tenant B: credito na conta B
        (v_entry_b, v_tenant_b, v_acct_b, 'BRL', 2,
         0, 500000, '2026-06-15 11:00:00+00');

    RAISE NOTICE 'Setup concluido: tenant_a=% tenant_b=% acct_a=% acct_b=% entry_a=% entry_b=%',
        v_tenant_a, v_tenant_b, v_acct_a, v_acct_b, v_entry_a, v_entry_b;
END;
$$;


-- ---------------------------------------------------------------------------
-- HELPER: funcao de assert local (padrao canonico do repo)
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
-- Caso (a): isolamento de leitura por tenant.
-- ===========================================================================

DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'cccccccc-0000-0000-0000-000000000001';

    -- 1a. accounts: tenant A ve exatamente 1 conta (a sua)
    SELECT COUNT(*) INTO v_count
    FROM ledger.accounts
    WHERE code LIKE 'rls-test:%';
    PERFORM pg_temp.assert_count('accounts: tenant_a ve 1', v_count, 1);

    -- 1b. journal_entries: tenant A ve exatamente 1 entrada
    SELECT COUNT(*) INTO v_count
    FROM ledger.journal_entries
    WHERE idempotency_key LIKE 'rls-test-%';
    PERFORM pg_temp.assert_count('journal_entries: tenant_a ve 1', v_count, 1);

    -- 1c. postings: tenant A ve exatamente 2 postings (debito + credito da entry A)
    SELECT COUNT(*) INTO v_count
    FROM ledger.postings
    WHERE posted_at >= '2026-06-15' AND posted_at < '2026-06-16';
    PERFORM pg_temp.assert_count('postings: tenant_a ve 2', v_count, 2);

    -- 1d. account_balances (view SECURITY INVOKER): tenant A ve 1 linha de saldo
    --     Caso (c): a view herda o RLS de accounts e postings via security_invoker.
    SELECT COUNT(*) INTO v_count
    FROM ledger.account_balances
    WHERE account_code LIKE 'rls-test:%';
    PERFORM pg_temp.assert_count('account_balances (view): tenant_a ve 1', v_count, 1);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 2 — Tenant B enxerga apenas seus proprios dados (0 linhas de A)
-- Caso (a): isolamento de leitura por tenant (simetria).
-- ===========================================================================

DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'dddddddd-0000-0000-0000-000000000001';

    -- 2a. accounts: tenant B ve exatamente 1 conta
    SELECT COUNT(*) INTO v_count
    FROM ledger.accounts
    WHERE code LIKE 'rls-test:%';
    PERFORM pg_temp.assert_count('accounts: tenant_b ve 1', v_count, 1);

    -- 2b. journal_entries: tenant B ve exatamente 1 entrada
    SELECT COUNT(*) INTO v_count
    FROM ledger.journal_entries
    WHERE idempotency_key LIKE 'rls-test-%';
    PERFORM pg_temp.assert_count('journal_entries: tenant_b ve 1', v_count, 1);

    -- 2c. postings: tenant B ve exatamente 2 postings (debito + credito da entry B)
    SELECT COUNT(*) INTO v_count
    FROM ledger.postings
    WHERE posted_at >= '2026-06-15' AND posted_at < '2026-06-16';
    PERFORM pg_temp.assert_count('postings: tenant_b ve 2', v_count, 2);

    -- 2d. account_balances (view): tenant B ve 1 linha de saldo
    SELECT COUNT(*) INTO v_count
    FROM ledger.account_balances
    WHERE account_code LIKE 'rls-test:%';
    PERFORM pg_temp.assert_count('account_balances (view): tenant_b ve 1', v_count, 1);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 3 — Cross-tenant: tenant A nao le linhas cujo tenant_id = B
-- Caso (a): prova explicita de isolamento (nao apenas contagem total).
-- ===========================================================================

DO $$
DECLARE
    v_tenant_b UUID   := 'dddddddd-0000-0000-0000-000000000001';
    v_count    BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'cccccccc-0000-0000-0000-000000000001';

    -- 3a. accounts: A nao ve registros de B, mesmo filtrando por tenant_id de B
    SELECT COUNT(*) INTO v_count
    FROM ledger.accounts
    WHERE tenant_id = v_tenant_b;
    PERFORM pg_temp.assert_count('cross-tenant accounts: A nao ve B', v_count, 0);

    -- 3b. journal_entries: A nao ve entradas de B
    SELECT COUNT(*) INTO v_count
    FROM ledger.journal_entries
    WHERE tenant_id = v_tenant_b;
    PERFORM pg_temp.assert_count('cross-tenant journal_entries: A nao ve B', v_count, 0);

    -- 3c. postings: A nao ve postings de B
    SELECT COUNT(*) INTO v_count
    FROM ledger.postings
    WHERE tenant_id = v_tenant_b;
    PERFORM pg_temp.assert_count('cross-tenant postings: A nao ve B', v_count, 0);

    -- 3d. account_balances via view: A nao ve saldos de B
    --     Caso (c): confirma que security_invoker propaga o RLS de accounts
    --     e postings atraves da view — nao ha bypass silencioso.
    SELECT COUNT(*) INTO v_count
    FROM ledger.account_balances
    WHERE tenant_id = v_tenant_b;
    PERFORM pg_temp.assert_count('cross-tenant account_balances (view): A nao ve B', v_count, 0);

    -- 3e. JOIN entre tabelas: mesmo cruzando journal_entries com postings,
    --     A so ve as suas proprias linhas em ambas as tabelas.
    SELECT COUNT(*) INTO v_count
    FROM ledger.journal_entries je
    JOIN ledger.postings p ON p.journal_entry_id = je.id
    WHERE je.tenant_id = v_tenant_b;
    PERFORM pg_temp.assert_count('cross-tenant JOIN je+postings: A nao ve B', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 4 — Fail-closed: sem adserver.tenant_id setado -> 0 linhas em tudo
-- Caso (b): sem variavel de sessao, ledger.current_tenant_id() retorna NULL.
--   NULL = ledger.current_tenant_id() e sempre FALSE -> policy rejeita toda linha.
--   Invariante TX-3 / DA-11: nao ha "modo aberto" — falha fecha, nunca abre.
-- ===========================================================================

DO $$
DECLARE
    v_count BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    -- Intencionalmente NAO setamos adserver.tenant_id.
    -- Reset explicito para garantir que valor de bloco anterior nao vaze.
    SET LOCAL adserver.tenant_id = '';

    -- 4a. accounts: 0 linhas (fail-closed)
    SELECT COUNT(*) INTO v_count FROM ledger.accounts;
    PERFORM pg_temp.assert_count('fail-closed accounts (tenant_id vazio): 0', v_count, 0);

    -- 4b. journal_entries: 0 linhas (fail-closed)
    SELECT COUNT(*) INTO v_count FROM ledger.journal_entries;
    PERFORM pg_temp.assert_count('fail-closed journal_entries (tenant_id vazio): 0', v_count, 0);

    -- 4c. postings: 0 linhas (fail-closed)
    SELECT COUNT(*) INTO v_count FROM ledger.postings;
    PERFORM pg_temp.assert_count('fail-closed postings (tenant_id vazio): 0', v_count, 0);

    -- 4d. account_balances (view): 0 linhas (fail-closed via security_invoker)
    SELECT COUNT(*) INTO v_count FROM ledger.account_balances;
    PERFORM pg_temp.assert_count('fail-closed account_balances (tenant_id vazio): 0', v_count, 0);

    -- 4e. Variante: tenant_id com UUID inexistente (nunca cadastrado)
    --     Garante que um valor invalido nao "abre" acesso.
    SET LOCAL adserver.tenant_id = '00000000-dead-beef-0000-000000000000';

    SELECT COUNT(*) INTO v_count FROM ledger.accounts;
    PERFORM pg_temp.assert_count('fail-closed accounts (UUID inexistente): 0', v_count, 0);

    SELECT COUNT(*) INTO v_count FROM ledger.journal_entries;
    PERFORM pg_temp.assert_count('fail-closed journal_entries (UUID inexistente): 0', v_count, 0);

    SELECT COUNT(*) INTO v_count FROM ledger.postings;
    PERFORM pg_temp.assert_count('fail-closed postings (UUID inexistente): 0', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 5 — Superuser bypassa RLS (comportamento documentado e esperado)
-- Migrations e jobs de reconciliacao rodam como superuser — devem ver tudo.
-- ===========================================================================

DO $$
DECLARE
    v_count BIGINT;
BEGIN
    -- Sem SET ROLE: continua como adserver_admin (superuser, bypassa RLS).

    -- 5a. accounts: superuser ve 2 contas (A + B)
    SELECT COUNT(*) INTO v_count
    FROM ledger.accounts
    WHERE tenant_id IN (
        'cccccccc-0000-0000-0000-000000000001',
        'dddddddd-0000-0000-0000-000000000001'
    );
    PERFORM pg_temp.assert_count('superuser ve ambas accounts (bypass RLS)', v_count, 2);

    -- 5b. journal_entries: superuser ve 2 entradas
    SELECT COUNT(*) INTO v_count
    FROM ledger.journal_entries
    WHERE tenant_id IN (
        'cccccccc-0000-0000-0000-000000000001',
        'dddddddd-0000-0000-0000-000000000001'
    );
    PERFORM pg_temp.assert_count('superuser ve ambas journal_entries (bypass RLS)', v_count, 2);

    -- 5c. postings: superuser ve 4 postings (2 de A + 2 de B)
    SELECT COUNT(*) INTO v_count
    FROM ledger.postings
    WHERE tenant_id IN (
        'cccccccc-0000-0000-0000-000000000001',
        'dddddddd-0000-0000-0000-000000000001'
    );
    PERFORM pg_temp.assert_count('superuser ve todos os postings (bypass RLS)', v_count, 4);

    -- 5d. account_balances: superuser ve 2 linhas de saldo (A + B)
    SELECT COUNT(*) INTO v_count
    FROM ledger.account_balances
    WHERE tenant_id IN (
        'cccccccc-0000-0000-0000-000000000001',
        'dddddddd-0000-0000-0000-000000000001'
    );
    PERFORM pg_temp.assert_count('superuser ve ambos saldos na view (bypass RLS)', v_count, 2);
END;
$$;


-- ===========================================================================
-- BLOCO 6 — account_balances: saldo via view herda RLS (caso c aprofundado)
-- Prova que o valor do saldo retornado reflete apenas postings do proprio tenant.
-- TX-2: comparacao de saldo em NUMERIC (minor units inteiros, sem float).
-- ===========================================================================

DO $$
DECLARE
    v_balance  NUMERIC(40, 18);
    v_count    BIGINT;
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'cccccccc-0000-0000-0000-000000000001';

    -- Tenant A tem debito=100000 e credito=100000 na mesma conta -> balance = 0.
    -- (par balanceado: debit - credit = 100000 - 100000 = 0)
    SELECT balance INTO v_balance
    FROM ledger.account_balances
    WHERE account_code = 'rls-test:revenue:brl';

    IF v_balance IS NULL THEN
        RAISE EXCEPTION 'ASSERT FALHOU [account_balances balance tenant_a]: NULL inesperado';
    END IF;
    IF v_balance <> 0 THEN
        RAISE EXCEPTION 'ASSERT FALHOU [account_balances balance tenant_a]: esperado=0 obtido=%',
            v_balance;
    END IF;
    RAISE NOTICE 'PASS [account_balances balance tenant_a]: balance=% (esperado=0)', v_balance;

    -- Confirma que a view nao inclui postings de B no calculo de saldo de A.
    -- Se incluisse, o saldo seria diferente de 0 (os montantes de B sao 500000).
    SELECT COUNT(*) INTO v_count
    FROM ledger.account_balances
    WHERE account_code = 'rls-test:revenue:brl'
      AND posting_count = 2;     -- exatamente 2 postings do tenant A, nao 4
    PERFORM pg_temp.assert_count('account_balances: posting_count=2 (somente A, nao 4 com B)', v_count, 1);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- NOTA SOBRE CASO (d) — WITH CHECK em INSERT/UPDATE
--
-- As policies criadas em 0003_ledger_rls_up.sql usam exclusivamente a
-- clausula USING sem WITH CHECK:
--
--   CREATE POLICY accounts_tenant_isolation ON ledger.accounts
--       USING (tenant_id = ledger.current_tenant_id());
--
-- Em Postgres, uma policy sem WITH CHECK explicito aplica a clausula USING
-- tambem como filtro de escrita (para UPDATE) — mas apenas como filtro de
-- visibilidade das linhas existentes, nao como rejeicao de INSERT com tenant
-- diferente do setado. Portanto:
--   - SELECT: RLS filtra por tenant (provado nos blocos 1-6).
--   - UPDATE: RLS impede que adserver_app altere linhas de outro tenant
--     (as linhas nao sao visiveis, logo nao sao atualizaveis).
--   - INSERT com tenant_id errado: NAO e barrado pela policy USING sozinha
--     (seria necessario WITH CHECK para isso).
--
-- Esta e uma limitacao conhecida da migracao 0003. O controle de escrita
-- cross-tenant e responsabilidade da camada de aplicacao (middleware que
-- injeta tenant_id via JWT antes de qualquer operacao DML).
--
-- Para adicionar WITH CHECK: alterar 0003_up com:
--   CREATE POLICY accounts_tenant_isolation ON ledger.accounts
--       USING      (tenant_id = ledger.current_tenant_id())
--       WITH CHECK (tenant_id = ledger.current_tenant_id());
-- (requer DROP POLICY + recreate ou ALTER POLICY, idempotente com 0003_down).
-- ===========================================================================


-- ===========================================================================
-- ROLLBACK: nao persiste nenhum dado de teste
-- ===========================================================================

ROLLBACK;

\echo '== LEDGER RLS ISOLATION: ALL TESTS PASSED =='
