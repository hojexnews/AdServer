-- =============================================================================
-- postings_immutability_test.sql — Prova de append-only no BANCO (achado
-- HIGH #6 do money-ledger-guardian).
--
-- PROPOSITO
--   Provar que, apos a migration 0004_ledger_postings_immutable_up.sql:
--     (a) INSERT de par balanceado em ledger.postings -> ACEITO (controle
--         positivo — sem isto, um bloqueio universal do banco tambem faria
--         os casos negativos abaixo "passarem" por acidente).
--     (b) UPDATE de um posting ja lancado -> REJEITADO (trigger
--         postings_immutable_trg), mesmo como SUPERUSUARIO (o trigger nao e
--         contornado por privilegio de escrita nem por BYPASSRLS — RLS e
--         GRANT sao ortogonais ao mecanismo de trigger).
--     (c) DELETE de um posting ja lancado -> REJEITADO (idem).
--     (d) UPDATE direto numa PARTICAO (nao na tabela-mae) -> REJEITADO
--         (prova que o trigger propaga para particoes, nao so para o
--         acesso via ledger.postings).
--     (e) ledger.journal_entries: transicao legitima pending -> posted
--         (espelha FinalizeEntry) -> ACEITA.
--     (f) ledger.journal_entries: uma vez status='posted', UPDATE/DELETE
--         -> REJEITADO (imutavel apos finalizada).
--
-- COMO RODAR
--   psql -U adserver_admin -d adserver \
--        -v ON_ERROR_STOP=1 \
--        -f db/ledger/tests/postings_immutability_test.sql
--
--   Resultado esperado: "== LEDGER POSTINGS IMMUTABILITY: ALL TESTS PASSED ==".
--   Qualquer assert falho aborta com RAISE EXCEPTION (exit code != 0).
--
-- DEPENDENCIAS
--   - Postgres 16 com migrations 0001..0004 do schema ledger aplicadas.
--   - Roda como SUPERUSUARIO (ex.: adserver_admin) propositalmente: o
--     objetivo e provar que o BANCO (trigger) bloqueia a mutacao
--     independentemente de RLS/GRANT — nao apenas para o role de aplicacao
--     (adserver_app), que ja nao teria o GRANT de UPDATE/DELETE em
--     ledger.postings apos make/db.mk estreitar o grant (least-privilege
--     complementar). Superusuario bypassa RLS e GRANT, mas NAO bypassa
--     trigger — esse e o ponto do teste.
--
-- NOTAS
--   - Roda dentro de BEGIN...ROLLBACK: nao persiste dados (idempotente).
--   - Usa tenant/UUIDs proprios (12121212-*) para nao colidir com
--     rls_isolation_test.sql (cccccccc-*/dddddddd-*).
-- =============================================================================

BEGIN;

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
-- SETUP: conta + entry + par balanceado de postings (como superusuario)
-- ===========================================================================
DO $$
DECLARE
    v_tenant  UUID   := '12121212-0000-0000-0000-000000000001';
    v_acct    BIGINT;
    v_entry   BIGINT;
BEGIN
    INSERT INTO ledger.accounts (tenant_id, code, name, kind, asset_code)
    VALUES (v_tenant, 'immutable-test:revenue:brl', 'Conta Teste Imutabilidade', 'revenue', 'BRL')
    RETURNING id INTO v_acct;

    INSERT INTO ledger.journal_entries
        (tenant_id, idempotency_key, description, status, effective_at)
    VALUES
        (v_tenant, 'immutable-test-idem-001', 'Entrada de teste imutabilidade', 'posted', now())
    RETURNING id INTO v_entry;

    -- -----------------------------------------------------------------------
    -- (a) CONTROLE POSITIVO: INSERT de par balanceado -> ACEITO.
    -- -----------------------------------------------------------------------
    INSERT INTO ledger.postings
        (journal_entry_id, tenant_id, account_id, asset_code, scale,
         debit_amount, credit_amount, posted_at)
    VALUES
        (v_entry, v_tenant, v_acct, 'BRL', 2, 100000, 0, '2026-06-15 08:00:00+00'),
        (v_entry, v_tenant, v_acct, 'BRL', 2, 0, 100000, '2026-06-15 08:00:00+00');

    SET CONSTRAINTS ALL IMMEDIATE;  -- dispara a trigger de balanco (0001) agora
    PERFORM pg_temp.assert_count(
        'setup: par balanceado INSERT aceito', 1::bigint, 1::bigint);
    RAISE NOTICE 'PASS [(a) INSERT par balanceado ACEITO]: entry_id=% acct_id=%', v_entry, v_acct;
    SET CONSTRAINTS ALL DEFERRED;
END;
$$;


-- ===========================================================================
-- (b) UPDATE de posting ja lancado -> REJEITADO (postings_immutable_trg)
-- ===========================================================================
DO $$
DECLARE
    v_tenant     UUID   := '12121212-0000-0000-0000-000000000001';
    v_posting_id BIGINT;
BEGIN
    SELECT p.id INTO v_posting_id
    FROM ledger.postings p
    WHERE p.tenant_id = v_tenant
      AND p.posted_at = '2026-06-15 08:00:00+00'
      AND p.debit_amount = 100000
    LIMIT 1;

    BEGIN
        UPDATE ledger.postings
        SET debit_amount = 999999
        WHERE id = v_posting_id AND posted_at = '2026-06-15 08:00:00+00';
        RAISE EXCEPTION
            'ASSERT FALHOU [(b) UPDATE posting lancado]: banco ACEITOU — trigger de imutabilidade ausente ou ineficaz';
    EXCEPTION WHEN raise_exception THEN
        -- Nota anti-tautologia: o texto da mensagem ASSERT FALHOU acima
        -- NAO pode conter a substring 'append-only' (usada pelo trigger
        -- real em ledger.check_postings_immutable()) — senao o proprio
        -- RAISE EXCEPTION de falha do teste seria capturado aqui e
        -- reportado como falso PASS. Por isso o fallback usa
        -- 'trigger de imutabilidade' em vez de citar 'append-only'.
        IF SQLERRM LIKE '%append-only%' THEN
            RAISE NOTICE 'PASS [(b) UPDATE de posting lancado REJEITADO pelo trigger append-only]: %', SQLERRM;
        ELSE
            RAISE;
        END IF;
    END;
END;
$$;


-- ===========================================================================
-- (c) DELETE de posting ja lancado -> REJEITADO (postings_immutable_trg)
-- ===========================================================================
DO $$
DECLARE
    v_tenant     UUID   := '12121212-0000-0000-0000-000000000001';
    v_posting_id BIGINT;
BEGIN
    SELECT p.id INTO v_posting_id
    FROM ledger.postings p
    WHERE p.tenant_id = v_tenant
      AND p.posted_at = '2026-06-15 08:00:00+00'
      AND p.credit_amount = 100000
    LIMIT 1;

    BEGIN
        DELETE FROM ledger.postings
        WHERE id = v_posting_id AND posted_at = '2026-06-15 08:00:00+00';
        RAISE EXCEPTION
            'ASSERT FALHOU [(c) DELETE posting lancado]: banco ACEITOU — trigger de imutabilidade ausente ou ineficaz';
    EXCEPTION WHEN raise_exception THEN
        -- Ver nota anti-tautologia em (b) acima: a mensagem de falha do
        -- teste nao pode conter 'append-only'.
        IF SQLERRM LIKE '%append-only%' THEN
            RAISE NOTICE 'PASS [(c) DELETE de posting lancado REJEITADO pelo trigger append-only]: %', SQLERRM;
        ELSE
            RAISE;
        END IF;
    END;
END;
$$;


-- ===========================================================================
-- (d) UPDATE direto numa PARTICAO (bypassa a tabela-mae) -> REJEITADO
--   Prova que o trigger da tabela-mae particionada propaga para a particao
--   (postings_2026_06 — as fixtures usam '2026-06-15', ver 0001_up ~L170-177).
-- ===========================================================================
DO $$
DECLARE
    v_tenant     UUID   := '12121212-0000-0000-0000-000000000001';
    v_posting_id BIGINT;
BEGIN
    SELECT p.id INTO v_posting_id
    FROM ledger.postings p
    WHERE p.tenant_id = v_tenant
      AND p.posted_at = '2026-06-15 08:00:00+00'
      AND p.debit_amount = 100000
    LIMIT 1;

    BEGIN
        UPDATE ledger.postings_2026_06
        SET debit_amount = 1
        WHERE id = v_posting_id AND posted_at = '2026-06-15 08:00:00+00';
        RAISE EXCEPTION
            'ASSERT FALHOU [(d) UPDATE direto na particao]: banco ACEITOU — trigger nao propagou para a particao';
    EXCEPTION WHEN raise_exception THEN
        IF SQLERRM LIKE '%append-only%' THEN
            RAISE NOTICE 'PASS [(d) UPDATE direto na particao postings_2026_06 REJEITADO (trigger propaga)]: %', SQLERRM;
        ELSE
            RAISE;
        END IF;
    END;
END;
$$;


-- ===========================================================================
-- (e) journal_entries: transicao legitima pending -> posted -> ACEITA
--   Espelha FinalizeEntry (internal/ledger/posting.go ~L138-168).
-- ===========================================================================
DO $$
DECLARE
    v_tenant  UUID   := '12121212-0000-0000-0000-000000000001';
    v_entry   BIGINT;
    v_status  TEXT;
BEGIN
    INSERT INTO ledger.journal_entries
        (tenant_id, idempotency_key, description, status, effective_at)
    VALUES
        (v_tenant, 'immutable-test-idem-pending', 'Entrada pending para finalizar', 'pending', now())
    RETURNING id INTO v_entry;

    UPDATE ledger.journal_entries
    SET status = 'posted'
    WHERE id = v_entry AND status = 'pending';

    SELECT status::text INTO v_status FROM ledger.journal_entries WHERE id = v_entry;
    PERFORM pg_temp.assert_count(
        'journal_entries: transicao pending->posted aceita',
        (v_status = 'posted')::int::bigint, 1::bigint);
    RAISE NOTICE 'PASS [(e) journal_entries pending->posted ACEITA]: entry_id=% status=%', v_entry, v_status;
END;
$$;


-- ===========================================================================
-- (f) journal_entries: uma vez 'posted', UPDATE/DELETE -> REJEITADO
--   (imutavel apos finalizada — estorno e sempre nova entry via RecordReversal)
-- ===========================================================================
DO $$
DECLARE
    v_tenant  UUID   := '12121212-0000-0000-0000-000000000001';
    v_entry   BIGINT;
BEGIN
    SELECT id INTO v_entry
    FROM ledger.journal_entries
    WHERE tenant_id = v_tenant AND idempotency_key = 'immutable-test-idem-pending';

    -- (f1) UPDATE de campo qualquer (nao so status) -> rejeitado
    BEGIN
        UPDATE ledger.journal_entries
        SET description = 'tentativa de edicao pos-posted'
        WHERE id = v_entry;
        RAISE EXCEPTION
            'ASSERT FALHOU [(f1) UPDATE de entry posted]: banco ACEITOU — trigger de imutabilidade ausente ou ineficaz';
    EXCEPTION WHEN raise_exception THEN
        IF SQLERRM LIKE '%imutável%' OR SQLERRM LIKE '%imutavel%' THEN
            RAISE NOTICE 'PASS [(f1) UPDATE de journal_entry posted REJEITADO]: %', SQLERRM;
        ELSE
            RAISE;
        END IF;
    END;

    -- (f2) DELETE -> rejeitado
    BEGIN
        DELETE FROM ledger.journal_entries WHERE id = v_entry;
        RAISE EXCEPTION
            'ASSERT FALHOU [(f2) DELETE de entry posted]: banco ACEITOU — trigger de imutabilidade ausente ou ineficaz';
    EXCEPTION WHEN raise_exception THEN
        IF SQLERRM LIKE '%imutável%' OR SQLERRM LIKE '%imutavel%' THEN
            RAISE NOTICE 'PASS [(f2) DELETE de journal_entry posted REJEITADO]: %', SQLERRM;
        ELSE
            RAISE;
        END IF;
    END;
END;
$$;


-- ===========================================================================
-- ROLLBACK: nao persiste nenhum dado de teste
-- ===========================================================================

ROLLBACK;

\echo '== LEDGER POSTINGS IMMUTABILITY: ALL TESTS PASSED =='
