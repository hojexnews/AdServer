-- =============================================================================
-- 0004_ledger_postings_immutable_up.sql
-- Imutabilidade de ledger.postings (append-only) e de ledger.journal_entries
-- finalizadas — reforçada no BANCO, não apenas em documentação/GRANT (achado
-- HIGH #6 do money-ledger-guardian).
--
-- CONTEXTO DO ACHADO
--   BILLING.md §6, db/README.md (invariante 4) e ledger_test.go afirmam que
--   "postings são append-only" e que estorno é sempre um NOVO par de postings
--   (RecordReversal — internal/ledger/crypto.go), nunca UPDATE/DELETE do
--   posting original. Até esta migration, NADA no banco impedia um UPDATE ou
--   DELETE em ledger.postings — o único trigger existente
--   (postings_balance_chk_trg, 0001_ledger_schema_up.sql) verifica apenas o
--   BALANÇO agregado por entry+asset_code, e passa a validar até uma linha
--   editada/apagada silenciosamente enquanto o balanço se mantiver correto
--   (ex.: UPDATE que troca debit_amount e credit_amount na mesma linha, ou
--   DELETE de um par completo). O grant em make/db.mk concedia
--   UPDATE/DELETE em TODAS as tabelas do schema ledger ao role de aplicação
--   (adserver_app), mesmo o código Go (internal/ledger/posting.go) NUNCA
--   fazendo UPDATE/DELETE em postings.
--
-- FIX (defesa em profundidade — banco é a última linha, não a aplicação)
--   1. ledger.postings: trigger BEFORE UPDATE OR DELETE incondicional.
--      Nenhuma linha de posting já inserida pode ser editada ou apagada,
--      nem mesmo por superusuário ou por um bug futuro na aplicação —
--      correções SEMPRE via RecordReversal (novo par invertido).
--      O trigger é FOR EACH ROW na tabela-mãe particionada; em Postgres 16
--      triggers de linha em tabela particionada propagam para TODAS as
--      partições (existentes e futuras) e bloqueiam tanto o acesso via
--      ledger.postings quanto o acesso direto a uma partição
--      (ex.: ledger.postings_2026_06) — verificado empiricamente contra
--      Postgres 16.14 nativo.
--   2. ledger.journal_entries: trigger BEFORE UPDATE OR DELETE que permite
--      a ÚNICA transição legítima hoje (pending -> posted, usada por
--      FinalizeEntry em internal/ledger/posting.go), mas rejeita qualquer
--      UPDATE ou DELETE de uma entry cujo status JÁ seja 'posted' ou 'void'
--      (imutável após finalizada — estorno é sempre uma NOVA entry via
--      RecordReversal, nunca reabertura da original).
--
-- COMPLEMENTAR (least-privilege — ver make/db.mk):
--   O GRANT de adserver_app em ledger.postings é estreitado para
--   SELECT, INSERT apenas (sem UPDATE/DELETE) — mudança de comportamento
--   ZERO para o código Go (posting.go nunca executa UPDATE/DELETE em
--   postings), mas reduz a superfície de ataque independentemente do
--   trigger. O trigger permanece a garantia primária: é o único mecanismo
--   que também vale para superusuário, para adserver_loader (BYPASSRLS)
--   e para acesso direto a uma partição.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- ledger.postings: append-only incondicional
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION ledger.check_postings_immutable()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION
        'ledger.postings é append-only; use ledger.RecordReversal para estornar (novo par de postings), nunca UPDATE/DELETE. op=% entry_id=%',
        TG_OP,
        COALESCE(OLD.journal_entry_id, NULL);
    RETURN NULL; -- nunca alcançado (RAISE EXCEPTION aborta a statement)
END;
$$;

COMMENT ON FUNCTION ledger.check_postings_immutable() IS
    'Bloqueia INCONDICIONALMENTE UPDATE/DELETE em ledger.postings. '
    'Append-only: correções sempre via RecordReversal (novo par invertido).';

-- BEFORE (não AFTER): aborta antes de qualquer efeito, sem precisar reverter.
-- FOR EACH ROW na tabela-mãe particionada: propaga para todas as partições
-- (0001_ledger_schema_up.sql cria 2026-01..2026-12 + postings_future) e para
-- quaisquer partições futuras criadas via pg_partman/script mensal.
CREATE TRIGGER postings_immutable_trg
    BEFORE UPDATE OR DELETE
    ON ledger.postings
    FOR EACH ROW EXECUTE FUNCTION ledger.check_postings_immutable();

COMMENT ON TRIGGER postings_immutable_trg ON ledger.postings IS
    'Append-only (achado HIGH #6). Nenhum UPDATE/DELETE é aceito, mesmo por superusuário.';

-- ---------------------------------------------------------------------------
-- ledger.journal_entries: imutável após 'posted' ou 'void'
--
-- Única transição legítima: pending -> posted (FinalizeEntry,
-- internal/ledger/posting.go ~L138-168, ao receber confirmação on-chain).
-- Uma vez posted (ou void, que hoje só existe como estado terminal), a
-- entry não pode mais ser alterada nem apagada — estorno é sempre uma NOVA
-- entry (RecordReversal), nunca reabertura da original.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION ledger.check_journal_entry_immutable()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status IN ('posted', 'void') THEN
        RAISE EXCEPTION
            'ledger.journal_entries id=% já está status=% (imutável); estorno é sempre uma NOVA entry via RecordReversal, nunca edição/exclusão da original. op=%',
            OLD.id, OLD.status, TG_OP;
    END IF;

    -- OLD.status = 'pending': permite a transição legítima pending -> posted
    -- (FinalizeEntry) e qualquer ajuste ainda em 'pending'.
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION ledger.check_journal_entry_immutable() IS
    'Bloqueia UPDATE/DELETE em ledger.journal_entries cujo status já seja posted/void. '
    'Permite a transição legítima pending->posted (FinalizeEntry).';

CREATE TRIGGER journal_entries_immutable_trg
    BEFORE UPDATE OR DELETE
    ON ledger.journal_entries
    FOR EACH ROW EXECUTE FUNCTION ledger.check_journal_entry_immutable();

COMMENT ON TRIGGER journal_entries_immutable_trg ON ledger.journal_entries IS
    'Imutável após status=posted/void (achado HIGH #6, complementar). '
    'Estorno sempre via RecordReversal (nova entry), nunca edição da original.';
