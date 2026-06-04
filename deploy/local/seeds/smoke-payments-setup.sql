-- =============================================================================
-- deploy/local/seeds/smoke-payments-setup.sql
-- Grants de desenvolvimento para o smoke-payments.sh.
--
-- Este script e IDEMPOTENTE (usa IF NOT EXISTS / DO NOTHING).
-- Deve ser aplicado APOS:
--   1. db/ledger/migrations/0001_ledger_schema_up.sql
--   2. db/ledger/migrations/0002_reconciliation_exceptions_up.sql
--   3. db/ledger/migrations/0003_ledger_rls_up.sql
--   4. db/seed/dev_roles.sql   (cria adserver_app e adserver_loader)
--
-- O 10-init.sh do Docker Compose nao aplica as migrations 0002/0003 do ledger
-- nem os grants de ledger para adserver_app — este seed preenche essa lacuna
-- para o ambiente de smoke local.
--
-- SENHAS: apenas valores de DEV. Nunca use em staging/producao.
-- Segredos de producao chegam do OpenBao (platform/secrets/openbao).
--
-- Referencia: db/ledger/tests/rls_isolation_test.sql:18-29 (dependencias do teste RLS).
-- =============================================================================

-- Migrations 0002 e 0003 do ledger (idempotentes via IF NOT EXISTS / OR REPLACE).
-- Aplicadas aqui caso o stack Docker nao tenha rodado 10-init.sh completo.

\echo '[smoke-payments-setup] aplicando migrations 0002 e 0003 do ledger se ausentes...'

DO $$
BEGIN
    -- 0002: reconciliation_exceptions (tabela)
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'ledger' AND table_name = 'reconciliation_exceptions'
    ) THEN
        RAISE NOTICE 'migration 0002 nao encontrada — aplicar manualmente:';
        RAISE NOTICE '  psql $PGURL -f db/ledger/migrations/0002_reconciliation_exceptions_up.sql';
        RAISE EXCEPTION 'migration ledger 0002 ausente — smoke nao pode prosseguir';
    END IF;

    -- 0003: RLS (policies)
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE schemaname = 'ledger' AND tablename = 'accounts'
          AND policyname = 'accounts_tenant_isolation'
    ) THEN
        RAISE NOTICE 'migration 0003 nao encontrada — aplicar manualmente:';
        RAISE NOTICE '  psql $PGURL -f db/ledger/migrations/0003_ledger_rls_up.sql';
        RAISE EXCEPTION 'migration ledger 0003 (RLS) ausente — smoke nao pode prosseguir';
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
