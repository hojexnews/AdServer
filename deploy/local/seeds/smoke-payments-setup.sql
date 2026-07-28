-- =============================================================================
-- deploy/local/seeds/smoke-payments-setup.sql
-- Grants de desenvolvimento para o smoke-payments.sh.
--
-- Este script e IDEMPOTENTE (usa IF NOT EXISTS / DO NOTHING).
-- Deve ser aplicado APOS o provisionamento do banco (make dev-db-setup ou
-- make dev-up), que aplica TODAS as migrations de db/, derivadas do diretorio
-- e na ordem de db/schema-order.txt, e APOS db/seed/dev_roles.sql (que cria
-- adserver_app e adserver_loader).
--
-- 32a onda: ate aqui este cabecalho dizia que "o 10-init.sh do Docker Compose
-- nao aplica as migrations 0002/0003 do ledger" e que este seed "preenchia
-- essa lacuna". As duas metades eram falsas de formas diferentes: o 10-init.sh
-- de fato nao as aplicava (aplicava so a 0001 — corrigido nesta onda, agora
-- deriva por glob), e este arquivo NUNCA as aplicou — ele apenas verifica a
-- presenca e aborta. Um seed que "preenche a lacuna" e um seed que so falha
-- ruidosamente sao coisas diferentes, e a diferenca importa para quem le isto
-- as 3h da manha. O que este bloco faz, de fato, e PREFLIGHT.
--
-- SENHAS: apenas valores de DEV. Nunca use em staging/producao.
-- Segredos de producao chegam do OpenBao (platform/secrets/openbao).
--
-- Referencia: db/ledger/tests/rls_isolation_test.sql:18-29 (dependencias do teste RLS).
-- =============================================================================

-- PREFLIGHT do schema ledger: verifica (nao aplica) que o banco tem o que o
-- smoke exercita. Cobre as tres migrations que criam estrutura observavel
-- alem da 0001 (schema base, cuja ausencia o smoke-payments.sh ja checa).

\echo '[smoke-payments-setup] preflight: migrations do ledger presentes?'

DO $$
BEGIN
    -- 0002: reconciliation_exceptions (tabela)
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'ledger' AND table_name = 'reconciliation_exceptions'
    ) THEN
        RAISE NOTICE 'ledger.reconciliation_exceptions ausente — provisione pelo caminho canonico:';
        RAISE NOTICE '  make dev-db-setup   (ou make dev-up), que aplica todas as migrations do diretorio';
        RAISE EXCEPTION 'migration ledger 0002 ausente — smoke nao pode prosseguir';
    END IF;

    -- 0003: RLS (policies)
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE schemaname = 'ledger' AND tablename = 'accounts'
          AND policyname = 'accounts_tenant_isolation'
    ) THEN
        RAISE NOTICE 'RLS de ledger.accounts ausente — provisione pelo caminho canonico:';
        RAISE NOTICE '  make dev-db-setup   (ou make dev-up), que aplica todas as migrations do diretorio';
        RAISE EXCEPTION 'migration ledger 0003 (RLS) ausente — smoke nao pode prosseguir';
    END IF;

    -- 0004: append-only (triggers de imutabilidade de postings e journal_entries).
    -- Ausente deste preflight ate a 32a onda. O smoke exercita RecordEntry /
    -- RecordDeposit / RecordReversal, e a semantica de estorno ("nunca edita a
    -- entry original, sempre cria uma nova") so e IMPOSTA por estes triggers —
    -- sem eles o smoke passava verde contra um ledger mutavel, provando menos
    -- do que dizia provar. Os dois triggers vem da MESMA migration; checar os
    -- dois evita que meia aplicacao passe.
    IF (
        SELECT COUNT(*) FROM pg_trigger
        WHERE NOT tgisinternal
          AND tgname IN ('postings_immutable_trg', 'journal_entries_immutable_trg')
    ) <> 2 THEN
        RAISE NOTICE 'triggers append-only do ledger ausentes — provisione pelo caminho canonico:';
        RAISE NOTICE '  make dev-db-setup   (ou make dev-up), que aplica todas as migrations do diretorio';
        RAISE EXCEPTION 'migration ledger 0004 (append-only) ausente — smoke nao pode prosseguir';
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- Grants no schema ledger para adserver_app (DEV ONLY).
--
-- Em producao, estes grants sao gerenciados pelo OpenBao/Terraform.
-- Aqui aplicamos apenas em ambiente de desenvolvimento local.
--
-- Espelha o padrao documentado em db/README.md:121-124 e
-- referenciado em db/ledger/tests/rls_isolation_test.sql:18-24.
-- ---------------------------------------------------------------------------

\echo '[smoke-payments-setup] concedendo acesso ao schema ledger para adserver_app...'

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_app') THEN
        RAISE EXCEPTION
            'Role adserver_app nao existe. '
            'Aplique db/seed/dev_roles.sql primeiro.';
    END IF;
END;
$$;

GRANT USAGE  ON SCHEMA ledger TO adserver_app;

-- SELECT/INSERT/UPDATE/DELETE nas tabelas de ledger (RLS filtra por tenant).
-- A tabela postings e particionada — o GRANT na tabela mae propaga para particoes (PG16).
GRANT SELECT, INSERT, UPDATE, DELETE
    ON ledger.accounts TO adserver_app;
GRANT SELECT, INSERT, UPDATE, DELETE
    ON ledger.journal_entries TO adserver_app;
GRANT SELECT, INSERT, UPDATE, DELETE
    ON ledger.postings TO adserver_app;
GRANT SELECT, INSERT, UPDATE, DELETE
    ON ledger.reconciliation_exceptions TO adserver_app;

-- Sequencias: necessarias para BIGSERIAL (INSERT precisa de USAGE na sequencia).
GRANT USAGE ON ALL SEQUENCES IN SCHEMA ledger TO adserver_app;

-- Funcao helper de RLS (criada pela 0003).
GRANT EXECUTE ON FUNCTION ledger.current_tenant_id() TO adserver_app;

-- View account_balances: SECURITY INVOKER — SELECT como adserver_app aciona RLS
-- das tabelas-base (accounts + postings) via a funcao ledger.current_tenant_id().
GRANT SELECT ON ledger.account_balances TO adserver_app;

\echo '[smoke-payments-setup] grants aplicados com sucesso.'
\echo '[smoke-payments-setup] stack pronto para smoke-payments.sh'
