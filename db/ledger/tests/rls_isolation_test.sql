-- =============================================================================
-- rls_isolation_test.sql — Teste de isolamento RLS por tenant (TX-3 / DA-11)
--
-- PROPOSITO
--   Provar que o RLS habilitado em 0003_ledger_rls_up.sql impede tenant A de
--   ler dados de tenant B nas tres tabelas core do ledger (accounts,
--   journal_entries, postings) e na view derivada account_balances, MAIS a
--   tabela tenant ledger.reconciliation_exceptions (0002 — divergencias
--   financeiras por tenant, BLOCO 9/9.5). Inclui o caso fail-closed
--   obrigatorio: sem adserver.tenant_id setado -> 0 linhas em tudo (nunca
--   "todas as linhas"). O BLOCO 6.5 introspecta o catalogo em modo default-deny:
--   TODA policy tenant FORCE-RLS do schema tem WITH CHECK explicito (pega
--   tabelas tenant futuras automaticamente, sem allowlist hardcoded).
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
--   - Caso (d) de INSERT/UPDATE com WITH CHECK: coberto no BLOCO 7 apos os
--     blocos de leitura. Prova que o banco rejeita INSERT e UPDATE cross-tenant
--     com check_violation / insufficient_privilege.
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
-- BLOCO 6.5 — Anti-tautologia DEFAULT-DENY: TODA policy sobre tabela FORCE-RLS
--   com coluna tenant_id no schema ledger tem WITH CHECK EXPLICITO no catalogo
--   pg_policy, INDEPENDENTE do valor de USING.
--
-- POR QUE ESTE BLOCO E NECESSARIO
--   As policies deste schema sao FOR ALL com USING === WITH CHECK (mesma
--   expressao textual). Quando uma policy FOR ALL OMITE WITH CHECK, o Postgres
--   reusa USING como verificacao de escrita EM TEMPO DE EXECUCAO (o mesmo
--   SQLSTATE 42501 observado no Bloco 7 abaixo) — mas NAO grava nada em
--   pg_policy.polwithcheck (verificado empiricamente contra Postgres 16.14
--   nativo: `CREATE POLICY ... USING (...)` sem WITH CHECK produz
--   polwithcheck IS NULL no catalogo, mesmo a policy continuando a rejeitar
--   INSERT/UPDATE cross-tenant via o fallback de USING). Ou seja: o Bloco 7
--   sozinho passaria IDENTICO mesmo se o WITH CHECK fosse removido da
--   migration — e tautologico em relacao a presenca do WITH CHECK. Este bloco
--   fecha a lacuna introspectando o catalogo diretamente, sem depender do
--   comportamento de USING.
--
-- POR QUE INTROSPECCAO DEFAULT-DENY (e nao allowlist hardcoded)
--   Uma allowlist fixa de policies (o padrao antigo, 3 nomes) DEIXA ESCAPAR
--   qualquer tabela tenant nova cuja policy nao seja adicionada a lista —
--   foi exatamente o buraco de reconciliation_exceptions_tenant_policy (FP #5,
--   29a onda): FORCE RLS + tenant_id, mas USING-only e fora da allowlist de 3,
--   passando 100% desapercebida. Aqui derivamos o conjunto de policies do
--   PROPRIO catalogo: toda tabela do schema ledger com relforcerowsecurity=true
--   E coluna tenant_id DEVE ter todas as suas policies com polwithcheck NOT NULL.
--   Uma tabela tenant futura sem WITH CHECK e pega automaticamente (fail-closed
--   por construcao), sem editar este teste.
-- ===========================================================================

DO $$
DECLARE
    v_rec     RECORD;
    v_missing TEXT := '';
    v_found   INT  := 0;
BEGIN
    FOR v_rec IN
        SELECT pol.polname,
               cls.relname AS relname,
               pol.polwithcheck
        FROM pg_policy    pol
        JOIN pg_class     cls ON cls.oid = pol.polrelid
        JOIN pg_namespace nsp ON nsp.oid = cls.relnamespace
        WHERE nsp.nspname = 'ledger'
          AND cls.relforcerowsecurity = true          -- FORCE ROW LEVEL SECURITY
          AND EXISTS (                                  -- tabela tenant (tem tenant_id)
                SELECT 1 FROM pg_attribute att
                WHERE att.attrelid = cls.oid
                  AND att.attname  = 'tenant_id'
                  AND att.attnum   > 0
                  AND NOT att.attisdropped
              )
    LOOP
        v_found := v_found + 1;
        IF v_rec.polwithcheck IS NULL THEN
            v_missing := v_missing || format('%s.%s ', v_rec.relname, v_rec.polname);
        END IF;
    END LOOP;

    -- Guarda contra falso-positivo por vacuidade: se as policies/tabelas fossem
    -- renomeadas/removidas, a query acima retornaria 0 linhas e o loop
    -- "passaria" sem checar nada. Exige encontrar AO MENOS as 4 policies tenant
    -- conhecidas (accounts, journal_entries, postings, reconciliation_exceptions).
    -- >= (nao ==) para que uma tabela tenant futura CORRETAMENTE configurada
    -- (com WITH CHECK) nao quebre este teste — mas uma SEM WITH CHECK cai no
    -- v_missing acima e aborta.
    IF v_found < 4 THEN
        RAISE EXCEPTION
            'ASSERT FALHOU [introspeccao default-deny vacua]: esperava >= 4 policies tenant FORCE-RLS no schema ledger, encontrou %',
            v_found;
    END IF;

    IF v_missing <> '' THEN
        RAISE EXCEPTION
            'ASSERT FALHOU [WITH CHECK ausente no catalogo pg_policy, independente de USING]: %',
            v_missing;
    END IF;

    RAISE NOTICE 'PASS [default-deny: todas as % policies tenant FORCE-RLS do ledger tem polwithcheck NOT NULL — WITH CHECK explicito, independente de USING]', v_found;
END;
$$;


-- ===========================================================================
-- BLOCO 7 — WITH CHECK: banco rejeita INSERT/UPDATE cross-tenant (caso d)
--
-- Prova que, com adserver.tenant_id = tenant_A, qualquer tentativa de:
--   (a) INSERT com tenant_id = tenant_B  -> check_violation (23514)
--   (b) UPDATE mudando tenant_id de linha visivel para tenant_B -> check_violation
--
-- Para cada tabela usamos um sub-bloco com EXCEPTION WHEN check_violation OR
-- insufficient_privilege para capturar o erro sem abortar a transacao externa.
-- O sub-bloco RAISE NOTICE 'PASS' confirma a rejeicao. Se o INSERT/UPDATE
-- for bem-sucedido (ausencia de erro), RAISE EXCEPTION aborta o teste.
--
-- Nota: postings exige posted_at para o particionamento; usamos '2026-06-15'
-- (mesma particao das fixtures). O constraint trigger de balance e DEFERRABLE
-- INITIALLY DEFERRED — o check_violation de RLS dispara ANTES do trigger de
-- balance (a policy e avaliada linha a linha no momento do INSERT/UPDATE),
-- portanto o teste nao precisa montar um par balanceado.
--
-- Nota (migration 0004 — imutabilidade, achado HIGH #6): desde 0004,
-- ledger.postings e BEFORE UPDATE/DELETE incondicionalmente bloqueado
-- (append-only) e ledger.journal_entries com status IN ('posted','void') e
-- imutavel. Esses triggers BEFORE disparam ANTES da avaliacao do WITH CHECK
-- de RLS (a NEW row so e checada contra a policy DEPOIS dos triggers BEFORE
-- rodarem — comportamento documentado do Postgres). Para nao confundir o
-- teste de RLS (7d) com o teste de imutabilidade (ja coberto em
-- db/ledger/tests/postings_immutability_test.sql), o UPDATE de 7d usa uma
-- entry PENDING dedicada (unica transicao em que UPDATE de journal_entries
-- ainda e permitido pelo trigger de imutabilidade) — assim a rejeicao
-- observada e garantidamente a de RLS (WITH CHECK), nao a de imutabilidade.
-- Para postings (7f), NAO ha transicao equivalente (append-only e
-- incondicional, sem excecao de status) — o sub-bloco aceita tanto o
-- check_violation/insufficient_privilege de RLS quanto o raise_exception do
-- trigger de imutabilidade, documentando que a garantia de "UPDATE
-- cross-tenant em postings nunca e aceito" hoje e feita por um mecanismo
-- ainda mais forte (bloqueio incondicional), que subsume o WITH CHECK.
-- ===========================================================================

DO $$
DECLARE
    v_tenant_a  UUID   := 'cccccccc-0000-0000-0000-000000000001';
    v_tenant_b  UUID   := 'dddddddd-0000-0000-0000-000000000001';
    v_acct_a_id BIGINT;
    v_entry_a_id BIGINT;
    v_entry_a_pending_id BIGINT;
    v_posting_a_id BIGINT;
BEGIN
    -- Obtemos IDs de tenant_a como superuser (antes de SET ROLE)
    SELECT id INTO v_acct_a_id
    FROM ledger.accounts WHERE tenant_id = v_tenant_a AND code = 'rls-test:revenue:brl';

    SELECT id INTO v_entry_a_id
    FROM ledger.journal_entries WHERE tenant_id = v_tenant_a AND idempotency_key = 'rls-test-idem-a-001';

    SELECT id INTO v_posting_a_id
    FROM ledger.postings WHERE tenant_id = v_tenant_a AND posted_at = '2026-06-15 10:00:00+00' LIMIT 1;

    -- Entry PENDING dedicada para 7d (ver nota acima): isola o teste de RLS
    -- WITH CHECK do teste (separado) de imutabilidade pos-posted.
    INSERT INTO ledger.journal_entries
        (tenant_id, idempotency_key, description, status, effective_at)
    VALUES
        (v_tenant_a, 'rls-test-idem-a-pending-7d', 'Entrada pending p/ teste RLS 7d', 'pending', now())
    RETURNING id INTO v_entry_a_pending_id;

    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'cccccccc-0000-0000-0000-000000000001';

    -- -----------------------------------------------------------------------
    -- 7a. accounts: INSERT com tenant_id = B deve falhar (check_violation)
    -- -----------------------------------------------------------------------
    BEGIN
        INSERT INTO ledger.accounts (tenant_id, code, name, kind, asset_code)
        VALUES (v_tenant_b, 'rls-test:forge:7a', 'Forjado cross-tenant', 'revenue', 'BRL');
        -- Chegou aqui: nao foi rejeitado -> falha do teste
        RAISE EXCEPTION 'ASSERT FALHOU [7a accounts INSERT cross-tenant]: banco NAO rejeitou — WITH CHECK ausente ou ineficaz';
    EXCEPTION
        WHEN check_violation OR insufficient_privilege THEN
            RAISE NOTICE 'PASS [7a accounts: INSERT cross-tenant rejeitado pelo banco (check_violation)]';
    END;

    -- -----------------------------------------------------------------------
    -- 7b. accounts: UPDATE mudando tenant_id da linha propria para B -> falha
    -- -----------------------------------------------------------------------
    BEGIN
        UPDATE ledger.accounts
        SET tenant_id = v_tenant_b
        WHERE id = v_acct_a_id;
        RAISE EXCEPTION 'ASSERT FALHOU [7b accounts UPDATE tenant_id cross-tenant]: banco NAO rejeitou — WITH CHECK ausente ou ineficaz';
    EXCEPTION
        WHEN check_violation OR insufficient_privilege THEN
            RAISE NOTICE 'PASS [7b accounts: UPDATE tenant_id cross-tenant rejeitado pelo banco (check_violation)]';
    END;

    -- -----------------------------------------------------------------------
    -- 7c. journal_entries: INSERT com tenant_id = B deve falhar
    -- -----------------------------------------------------------------------
    BEGIN
        INSERT INTO ledger.journal_entries (tenant_id, idempotency_key, description, status, effective_at)
        VALUES (v_tenant_b, 'rls-test-forge-7c', 'Forjado cross-tenant', 'posted', now());
        RAISE EXCEPTION 'ASSERT FALHOU [7c journal_entries INSERT cross-tenant]: banco NAO rejeitou';
    EXCEPTION
        WHEN check_violation OR insufficient_privilege THEN
            RAISE NOTICE 'PASS [7c journal_entries: INSERT cross-tenant rejeitado pelo banco (check_violation)]';
    END;

    -- -----------------------------------------------------------------------
    -- 7d. journal_entries: UPDATE mudando tenant_id para B -> falha
    --     Usa a entry PENDING dedicada (v_entry_a_pending_id) para que o
    --     trigger de imutabilidade (0004) NAO intercepte antes do WITH CHECK
    --     de RLS — pending e a unica transicao onde UPDATE ainda alcanca a
    --     avaliacao da policy (ver nota no cabecalho do BLOCO 7).
    -- -----------------------------------------------------------------------
    BEGIN
        UPDATE ledger.journal_entries
        SET tenant_id = v_tenant_b
        WHERE id = v_entry_a_pending_id;
        RAISE EXCEPTION 'ASSERT FALHOU [7d journal_entries UPDATE tenant_id cross-tenant]: banco NAO rejeitou';
    EXCEPTION
        WHEN check_violation OR insufficient_privilege THEN
            RAISE NOTICE 'PASS [7d journal_entries: UPDATE tenant_id cross-tenant rejeitado pelo banco (check_violation)]';
    END;

    -- -----------------------------------------------------------------------
    -- 7e. postings: INSERT com tenant_id = B deve falhar
    -- (journal_entry_id e account_id sao do tenant A — o check_violation de
    --  RLS dispara antes do trigger de balance DEFERRED)
    -- -----------------------------------------------------------------------
    BEGIN
        INSERT INTO ledger.postings
            (journal_entry_id, tenant_id, account_id, asset_code, scale,
             debit_amount, credit_amount, posted_at)
        VALUES
            (v_entry_a_id, v_tenant_b, v_acct_a_id, 'BRL', 2,
             1, 0, '2026-06-15 12:00:00+00');
        RAISE EXCEPTION 'ASSERT FALHOU [7e postings INSERT cross-tenant]: banco NAO rejeitou';
    EXCEPTION
        WHEN check_violation OR insufficient_privilege THEN
            RAISE NOTICE 'PASS [7e postings: INSERT cross-tenant rejeitado pelo banco (check_violation)]';
    END;

    -- -----------------------------------------------------------------------
    -- 7f. postings: UPDATE mudando tenant_id para B -> falha
    --     Desde a migration 0004 (achado HIGH #6), ledger.postings e
    --     append-only INCONDICIONAL: (i) o GRANT de adserver_app em
    --     ledger.postings foi estreitado para SELECT, INSERT (sem
    --     UPDATE/DELETE — ver make/db.mk), entao este UPDATE hoje e
    --     rejeitado JA no permission-check (insufficient_privilege), antes
    --     de RLS ou do trigger rodarem; e (ii) mesmo que o GRANT fosse
    --     alargado, o trigger postings_immutable_trg dispara ANTES do WITH
    --     CHECK de RLS e bloqueia QUALQUER UPDATE, cross-tenant ou nao.
    --     Aceitamos aqui as tres classes de rejeicao possiveis
    --     (insufficient_privilege do GRANT, check_violation/insufficient_privilege
    --     de RLS, ou raise_exception do trigger de imutabilidade) — em
    --     qualquer caso o INVARIANTE testado ("UPDATE cross-tenant em
    --     postings nunca e aceito") se mantem, agora garantido por DUAS
    --     camadas independentes mais fortes que subsumem o WITH CHECK.
    --     Cobertura dedicada da imutabilidade em si:
    --     db/ledger/tests/postings_immutability_test.sql.
    -- -----------------------------------------------------------------------
    BEGIN
        UPDATE ledger.postings
        SET tenant_id = v_tenant_b
        WHERE id = v_posting_a_id
          AND posted_at = '2026-06-15 10:00:00+00';
        RAISE EXCEPTION 'ASSERT FALHOU [7f postings UPDATE tenant_id cross-tenant]: banco NAO rejeitou';
    EXCEPTION
        WHEN check_violation OR insufficient_privilege THEN
            RAISE NOTICE 'PASS [7f postings: UPDATE tenant_id cross-tenant rejeitado pelo banco (check_violation)]';
        WHEN raise_exception THEN
            IF SQLERRM LIKE '%append-only%' THEN
                RAISE NOTICE 'PASS [7f postings: UPDATE cross-tenant rejeitado pelo trigger de imutabilidade (subsume WITH CHECK)]: %', SQLERRM;
            ELSE
                RAISE;
            END IF;
    END;

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- NOTA SOBRE CASO (d) — WITH CHECK em INSERT/UPDATE (STATUS: COBERTO)
--
-- As policies em 0003_ledger_rls_up.sql agora incluem WITH CHECK:
--
--   CREATE POLICY accounts_tenant_isolation ON ledger.accounts
--       USING      (tenant_id = ledger.current_tenant_id())
--       WITH CHECK (tenant_id = ledger.current_tenant_id());
--
-- Comportamento resultante (banco garante, independente da camada de app):
--   - SELECT: RLS filtra por tenant (blocos 1-6).
--   - INSERT com tenant_id errado: banco rejeita com check_violation (bloco 7).
--   - UPDATE mudando tenant_id para outro tenant: banco rejeita com
--     check_violation (bloco 7).
--   - UPDATE de linha de outro tenant: linha nao visivel (USING), UPDATE
--     retorna 0 linhas sem erro — comportamento correto e esperado.
--
-- Remediacao fecha os 3 achados CRITICAL do money-ledger-guardian (TX-3).
-- ===========================================================================


-- ===========================================================================
-- BLOCO 8 — Double-entry: o banco rejeita postings DESBALANCEADOS (caminho
--   NEGATIVO da constraint trigger postings_balance_chk_trg).
--
-- Invariante 1 do schema (0001_ledger_schema_up.sql:10-11 / checklist item 5):
--   sum(debit_amount) = sum(credit_amount) por journal_entry+asset_code.
-- Os BLOCOS 1-7 so exercitam RLS; as fixtures do SETUP sao PROPOSITALMENTE
-- balanceadas (linhas 97-117), entao a trigger de balanco NUNCA e exercitada no
-- caminho negativo. Este bloco fecha a lacuna: prova que um posting unilateral
-- (debito sem credito, ou credito sem debito) e REJEITADO pelo banco.
--
-- DEFERRABLE INITIALLY DEFERRED: a trigger dispara no COMMIT, nao na linha. Como
-- o teste roda dentro de BEGIN...ROLLBACK (nunca faz COMMIT), forcamos a
-- verificacao com `SET CONSTRAINTS ALL IMMEDIATE` dentro de um sub-bloco (que
-- estabelece um savepoint): a trigger dispara ali, a rejeicao esperada e
-- capturada pelo EXCEPTION handler e o savepoint reverte o INSERT invalido sem
-- derrubar a transacao de teste. Rodamos como superuser (sem SET ROLE) para que
-- o RLS nao mascare a verificacao — aqui provamos a TRIGGER DE BALANCO, nao o RLS.
-- ===========================================================================

DO $$
DECLARE
    v_tenant_a UUID := 'cccccccc-0000-0000-0000-000000000001';
    v_acct_a   BIGINT;
    v_entry    BIGINT;
BEGIN
    -- Conta auxiliar reutilizavel (accounts nao tem a trigger de balanco).
    INSERT INTO ledger.accounts (tenant_id, code, name, kind, asset_code)
    VALUES (v_tenant_a, 'bal-test:revenue:brl', 'Conta Balanco A', 'revenue', 'BRL')
    RETURNING id INTO v_acct_a;

    -- ISOLAMENTO DAS SONDAS: `SET CONSTRAINTS ALL IMMEDIATE` dispara TODOS os
    -- eventos de trigger DEFERRED pendentes — inclusive as 4 fixtures BALANCEADAS
    -- do SETUP. Se elas ficassem pendentes, cada sonda negativa abaixo veria a
    -- excecao das fixtures misturada com a sua. Validamos+esvaziamos a fila de
    -- eventos das fixtures AGORA (sob a trigger correta elas passam) e voltamos
    -- ao modo DEFERRED — assim cada sub-bloco 8a-8d so tem a SUA propria entry
    -- pendente quando chama SET CONSTRAINTS IMMEDIATE. (Sob a trigger MUTADA/
    -- invertida, este flush ja falha aqui nas fixtures balanceadas -> teste
    -- VERMELHO; sob a trigger CORRETA passa e cada sonda fica limpa.)
    SET CONSTRAINTS ALL IMMEDIATE;
    SET CONSTRAINTS ALL DEFERRED;

    -- -----------------------------------------------------------------------
    -- 8a. Posting unilateral (SO debito) -> entry desbalanceada -> rejeitada
    -- -----------------------------------------------------------------------
    BEGIN
        INSERT INTO ledger.journal_entries (tenant_id, idempotency_key, description)
        VALUES (v_tenant_a, 'bal-test-idem-debit-only', 'debito sem credito')
        RETURNING id INTO v_entry;

        INSERT INTO ledger.postings
            (journal_entry_id, tenant_id, account_id, asset_code, scale,
             debit_amount, credit_amount, posted_at)
        VALUES (v_entry, v_tenant_a, v_acct_a, 'BRL', 2,
                100000, 0, '2026-06-15 09:00:00+00');   -- debito=1000,00 sem credito

        SET CONSTRAINTS ALL IMMEDIATE;   -- forca a trigger DEFERRED a disparar agora
        RAISE EXCEPTION
            'ASSERT FALHOU [8a debito-so]: banco ACEITOU entry desbalanceada — trigger de balanco ausente ou ineficaz';
    EXCEPTION WHEN raise_exception THEN
        IF SQLERRM LIKE '%desbalanceado%' THEN
            RAISE NOTICE 'PASS [8a: posting debito-so rejeitado pela trigger de balanco]: %', SQLERRM;
        ELSE
            RAISE;   -- re-lanca o ASSERT FALHOU (a trigger NAO rejeitou)
        END IF;
    END;

    -- -----------------------------------------------------------------------
    -- 8b. Posting unilateral (SO credito) -> entry desbalanceada -> rejeitada
    --     (simetria: prova que nao e so o lado do debito que e verificado)
    -- -----------------------------------------------------------------------
    BEGIN
        INSERT INTO ledger.journal_entries (tenant_id, idempotency_key, description)
        VALUES (v_tenant_a, 'bal-test-idem-credit-only', 'credito sem debito')
        RETURNING id INTO v_entry;

        INSERT INTO ledger.postings
            (journal_entry_id, tenant_id, account_id, asset_code, scale,
             debit_amount, credit_amount, posted_at)
        VALUES (v_entry, v_tenant_a, v_acct_a, 'BRL', 2,
                0, 250000, '2026-06-15 09:30:00+00');   -- credito=2500,00 sem debito

        SET CONSTRAINTS ALL IMMEDIATE;
        RAISE EXCEPTION
            'ASSERT FALHOU [8b credito-so]: banco ACEITOU entry desbalanceada — trigger de balanco ausente ou ineficaz';
    EXCEPTION WHEN raise_exception THEN
        IF SQLERRM LIKE '%desbalanceado%' THEN
            RAISE NOTICE 'PASS [8b: posting credito-so rejeitado pela trigger de balanco]: %', SQLERRM;
        ELSE
            RAISE;
        END IF;
    END;

    -- -----------------------------------------------------------------------
    -- 8c. Par desbalanceado (debito 100000 vs credito 99999) -> rejeitado
    --     Prova que a verificacao e por VALOR (soma), nao so por presenca de
    --     ambos os lados. Um centavo de diferenca ja quebra o double-entry.
    -- -----------------------------------------------------------------------
    BEGIN
        INSERT INTO ledger.journal_entries (tenant_id, idempotency_key, description)
        VALUES (v_tenant_a, 'bal-test-idem-off-by-one', 'par off-by-one')
        RETURNING id INTO v_entry;

        INSERT INTO ledger.postings
            (journal_entry_id, tenant_id, account_id, asset_code, scale,
             debit_amount, credit_amount, posted_at)
        VALUES
            (v_entry, v_tenant_a, v_acct_a, 'BRL', 2, 100000, 0, '2026-06-15 09:45:00+00'),
            (v_entry, v_tenant_a, v_acct_a, 'BRL', 2, 0, 99999, '2026-06-15 09:45:00+00');

        SET CONSTRAINTS ALL IMMEDIATE;
        RAISE EXCEPTION
            'ASSERT FALHOU [8c off-by-one]: banco ACEITOU par desbalanceado (100000 vs 99999)';
    EXCEPTION WHEN raise_exception THEN
        IF SQLERRM LIKE '%desbalanceado%' THEN
            RAISE NOTICE 'PASS [8c: par off-by-one rejeitado pela trigger de balanco]: %', SQLERRM;
        ELSE
            RAISE;
        END IF;
    END;

    -- -----------------------------------------------------------------------
    -- 8d. CONTROLE POSITIVO: par BALANCEADO (100000 = 100000) -> ACEITO.
    --     Garante que a trigger nao rejeita indiscriminadamente (senao 8a-8c
    --     seriam tautologicos: um bloqueio universal tambem os faria "passar").
    -- -----------------------------------------------------------------------
    INSERT INTO ledger.journal_entries (tenant_id, idempotency_key, description)
    VALUES (v_tenant_a, 'bal-test-idem-balanced', 'par balanceado')
    RETURNING id INTO v_entry;

    INSERT INTO ledger.postings
        (journal_entry_id, tenant_id, account_id, asset_code, scale,
         debit_amount, credit_amount, posted_at)
    VALUES
        (v_entry, v_tenant_a, v_acct_a, 'BRL', 2, 100000, 0, '2026-06-15 10:15:00+00'),
        (v_entry, v_tenant_a, v_acct_a, 'BRL', 2, 0, 100000, '2026-06-15 10:15:00+00');

    SET CONSTRAINTS ALL IMMEDIATE;   -- entry balanceada: NAO deve disparar excecao
    RAISE NOTICE 'PASS [8d: par balanceado (100000=100000) ACEITO pela trigger de balanco]';

    -- Volta as constraints ao modo diferido para nao alterar o comportamento
    -- de eventuais blocos futuros nesta transacao (defensivo; o teste termina
    -- em ROLLBACK logo abaixo de qualquer forma).
    SET CONSTRAINTS ALL DEFERRED;
END;
$$;


-- ===========================================================================
-- BLOCO 9 — reconciliation_exceptions: isolamento RLS por tenant (FP #5, 29a).
--
-- POR QUE ESTE BLOCO E NECESSARIO
--   ledger.reconciliation_exceptions (migration 0002) e uma tabela tenant
--   (tenant_id NOT NULL, ENABLE + FORCE RLS) que armazena DIVERGENCIAS
--   FINANCEIRAS por tenant. Ate a 29a onda NENHUM teste a tocava: os BLOCOS
--   1-8 so cobrem accounts/journal_entries/postings, e o BLOCO 6.5 (antes)
--   hardcodava exatamente 3 policies — a 4a policy escapava 100%. Um
--   `USING (true)` (vazamento cross-tenant total das divergencias) passaria
--   despercebido. Este bloco fecha a lacuna nos tres eixos: leitura isolada,
--   fail-closed e WITH CHECK na escrita.
--
-- Fixtures inseridas como superuser (bypassa RLS). 1 excecao por tenant.
-- divergence_minor_units e GENERATED ALWAYS (nao inserida). status default 'open'.
-- ===========================================================================

DO $$
DECLARE
    v_tenant_a UUID   := 'cccccccc-0000-0000-0000-000000000001';
    v_tenant_b UUID   := 'dddddddd-0000-0000-0000-000000000001';
    v_count    BIGINT;
BEGIN
    -- Fixtures como superuser (antes de SET ROLE): uma divergencia por tenant.
    -- Valores em minor units NUMERIC (TX-2: sem float).
    INSERT INTO ledger.reconciliation_exceptions
        (tenant_id, asset_code, period_start, period_end,
         expected_minor_units, ledger_minor_units)
    VALUES
        (v_tenant_a, 'USDC', '2026-06-15 00:00:00+00', '2026-06-15 01:00:00+00',
         1000000, 900000),
        (v_tenant_b, 'USDC', '2026-06-15 00:00:00+00', '2026-06-15 01:00:00+00',
         5000000, 4000000);

    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'cccccccc-0000-0000-0000-000000000001';

    -- 9a. isolamento de leitura: tenant A ve exatamente 1 (a sua excecao)
    SELECT COUNT(*) INTO v_count FROM ledger.reconciliation_exceptions;
    PERFORM pg_temp.assert_count('recon: tenant_a ve 1 excecao (a sua)', v_count, 1);

    -- 9b. cross-tenant: A NAO ve a excecao de B, mesmo filtrando por tenant_id de B
    SELECT COUNT(*) INTO v_count
    FROM ledger.reconciliation_exceptions
    WHERE tenant_id = v_tenant_b;
    PERFORM pg_temp.assert_count('cross-tenant recon: A nao ve divergencia de B', v_count, 0);

    -- 9c. fail-closed: sem adserver.tenant_id (vazio) -> NULLIF NULL -> 0 linhas.
    --     Invariante TX-3/DA-11: falha fecha, nunca abre.
    SET LOCAL adserver.tenant_id = '';
    SELECT COUNT(*) INTO v_count FROM ledger.reconciliation_exceptions;
    PERFORM pg_temp.assert_count('fail-closed recon (tenant_id vazio): 0', v_count, 0);

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- BLOCO 9.5 — reconciliation_exceptions: WITH CHECK rejeita INSERT cross-tenant.
--
-- Com adserver.tenant_id = A, um INSERT com tenant_id = B deve abortar com
-- SQLSTATE 42501 (new row violates row-level security policy). Isso PROVA que
-- a policy tem WITH CHECK (nao apenas USING) — sem WITH CHECK o banco reusaria
-- USING como check de escrita, mas o teste do BLOCO 6.5 ja garante o
-- polwithcheck NOT NULL no catalogo; aqui provamos o EFEITO em runtime.
--
-- Trap #28: para o INSERT ALCANCAR o WITH CHECK (e nao morrer antes num
-- permission-check), adserver_app precisa de INSERT na tabela E USAGE na
-- sequence do BIGSERIAL id (ambos concedidos em make/db.mk FASE 3:
-- GRANT SELECT,INSERT ... e GRANT USAGE ON ALL SEQUENCES). Por isso o handler
-- so aceita a rejeicao como PASS se a mensagem for de RLS (row-level security);
-- uma "permission denied for sequence" (grant ausente) NAO satisfaz e re-lanca,
-- evitando o falso-positivo do trap #28.
-- ===========================================================================

DO $$
DECLARE
    v_tenant_b UUID := 'dddddddd-0000-0000-0000-000000000001';
BEGIN
    SET LOCAL ROLE adserver_app;
    SET LOCAL adserver.tenant_id = 'cccccccc-0000-0000-0000-000000000001';

    BEGIN
        INSERT INTO ledger.reconciliation_exceptions
            (tenant_id, asset_code, period_start, period_end,
             expected_minor_units, ledger_minor_units)
        VALUES
            (v_tenant_b, 'USDC', '2026-06-16 00:00:00+00', '2026-06-16 01:00:00+00',
             1, 0);
        -- Chegou aqui: nao foi rejeitado -> falha do teste (WITH CHECK ausente/ineficaz)
        RAISE EXCEPTION 'ASSERT FALHOU [9.5 recon INSERT cross-tenant]: banco NAO rejeitou — WITH CHECK ausente ou ineficaz';
    EXCEPTION
        WHEN check_violation OR insufficient_privilege THEN
            IF SQLERRM LIKE '%row-level security%' THEN
                RAISE NOTICE 'PASS [9.5 recon: INSERT cross-tenant rejeitado pelo WITH CHECK (RLS 42501)]: %', SQLERRM;
            ELSE
                -- Rejeicao por OUTRO motivo (ex.: permission denied for sequence —
                -- trap #28): NAO prova o WITH CHECK. Re-lanca para falhar alto.
                RAISE;
            END IF;
    END;

    RESET ROLE;
END;
$$;


-- ===========================================================================
-- ROLLBACK: nao persiste nenhum dado de teste
-- ===========================================================================

ROLLBACK;

\echo '== LEDGER RLS ISOLATION: ALL TESTS PASSED =='
