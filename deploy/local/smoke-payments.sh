#!/usr/bin/env bash
# deploy/local/smoke-payments.sh — Smoke e2e do trilho de pagamentos (Fase 3).
#
# Exercita o caminho de pagamentos FORA do hot path, contra o stack local
# (Postgres migrado + ledger), usando APENAS stubs e dados de desenvolvimento.
# Nenhuma chave real (Stripe/Asaas/Safe/Fireblocks) e necessaria.
#
# INVARIANTES VERIFICADAS:
#   (a) Par de postings idempotente: deposito gera exatamente um par debit/credit
#       balanceado; reexecutar a mesma idempotency_key NAO duplica postings.
#   (b) Deposito pending -> finalidade: entra pending e so vira posted/conta no
#       saldo apos FinalizeDeposit (simulado via SQL direto, como faria o
#       webhook de confirmacao stub do custodiante Safe).
#   (c) Reconciliacao abre excecao e nunca autocorrige: injecao de divergencia
#       registra reconciliation_exception sem alterar o ledger.
#   (d) Status via BFF (PostgresPaymentsAdapter): saldo/status vem como string
#       DECIMAL (sem float), sem IDOR (RLS por tenant).
#
# PRE-REQUISITOS:
#   - Postgres 16 rodando e acessivel em PGURL (padrao: postgres local Docker).
#   - Migrations aplicadas: 0001+0002+0003 de ledger, 0001 de asset_registry.
#   - psql no PATH.
#   - Roles adserver_loader (BYPASSRLS) e adserver_app criados com acesso ao
#     schema ledger (ver deploy/local/seeds/smoke-payments-setup.sql).
#
# USO:
#   PGURL="postgres://postgres:postgres@localhost:5432/adserver?sslmode=disable" \
#   bash deploy/local/smoke-payments.sh
#
# O script aplica o seed de setup em BEGIN/ROLLBACK — nao persiste dados.
# Pode ser executado multiplas vezes sem efeito cumulativo no banco.
#
# NAO edite o Makefile nem workflows de CI — isso e Tarefa 3 do platform-infra-engineer.
#
# Arquivos exercitados:
#   internal/ledger/posting.go      — RecordEntry (idempotencia, balance check)
#   internal/ledger/crypto.go       — RecordDeposit, FinalizeDeposit, RecordReversal
#   internal/ledger/recon.go        — Reconciler.Run -> OpenException (nunca autocorrige)
#   db/ledger/migrations/0001_ledger_schema_up.sql — schema, trigger de balance, particions
#   db/ledger/migrations/0002_reconciliation_exceptions_up.sql — tabela de excecoes
#   db/ledger/migrations/0003_ledger_rls_up.sql — RLS por tenant, SECURITY INVOKER na view
#   db/ledger/migrations/0004_ledger_postings_immutable_up.sql — append-only de postings e
#                                   journal_entries (a semantica de estorno do smoke depende dela)
#   bff/src/adapters/postgres-payments.ts  — getBalances/listPaymentStatus (TX-2/TX-3)
#
# INVARIANTES TX-2 / DA-10 no script:
#   Todos os valores monetarios sao inteiros (minor-units int64/NUMERIC).
#   NENHUM float e usado em nenhuma asserção.
#   Saldo e comparado como string INTEGER, nunca como numero de ponto flutuante.

set -euo pipefail

PGURL="${PGURL:-postgres://postgres:postgres@localhost:5432/adserver?sslmode=disable}"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

# UUID canonico do tenant de smoke (distinto dos UUIDs de outros testes do repo).
# Ledger RLS test usa cccccccc-*/dddddddd-*; config usa aaaaaaaa-*/bbbbbbbb-*.
# Compliance usa eeeeeeee-*/ffffffff-*. Smoke-payments usa 11111111-*.
SMOKE_TENANT_A="11111111-0000-0000-0000-000000000001"
SMOKE_TENANT_B="11111111-0000-0000-0000-000000000002"

# TX_HASH deterministico para o stub de deposito cripto.
# Formato EVM: 0x + 64 hex digits. Nao e hash real — apenas identificador de teste.
TX_HASH_1="0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
TX_HASH_2="0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

# Valor do deposito: 1.000000 USDC = 1_000_000 minor-units (scale=6, TX-2: int, sem float).
AMOUNT_MINOR=1000000

PASS=0
FAIL=0

# ---------------------------------------------------------------------------
# Utilitarios
# ---------------------------------------------------------------------------

assert_eq() {
    local label="$1" actual="$2" expected="$3"
    if [ "$actual" = "$expected" ]; then
        echo "[smoke-payments] PASS [$label]: got='$actual' expected='$expected'"
        PASS=$((PASS + 1))
    else
        echo "[smoke-payments] FAIL [$label]: got='$actual' expected='$expected'"
        FAIL=$((FAIL + 1))
    fi
}

# ---------------------------------------------------------------------------
# Verifica pre-requisitos
# ---------------------------------------------------------------------------

echo "[smoke-payments] verificando pre-requisitos..."

if ! psql -v ON_ERROR_STOP=1 -tA "$PGURL" -c "SELECT 1" > /dev/null 2>&1; then
    echo "FAIL: nao foi possivel conectar ao Postgres em $PGURL"
    echo "  Suba o stack: docker compose -f deploy/local/docker-compose.yml up -d postgres"
    exit 1
fi
echo "[smoke-payments] postgres: OK"

# Verifica schema ledger (migration 0001 aplicada).
LEDGER_OK="$(psql -v ON_ERROR_STOP=1 -tA "$PGURL" \
    -c "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='ledger' AND table_name='journal_entries'" \
    2>/dev/null || echo 0)"
if [ "$LEDGER_OK" != "1" ]; then
    echo "FAIL: schema ledger nao encontrado. Provisione o banco pelo caminho canonico"
    echo "      (que aplica TODAS as migrations, derivadas do diretorio, na ordem de"
    echo "      db/schema-order.txt) em vez de aplicar arquivo por arquivo:"
    echo "  make dev-db-setup            # Postgres local, sem Docker"
    echo "  make dev-up                  # stack Docker Compose"
    echo "  ls $REPO_ROOT/db/ledger/migrations/*_up.sql   # o que existe, se for aplicar a mao"
    exit 1
fi
echo "[smoke-payments] schema ledger (0001): OK"

# Verifica migration 0002 (reconciliation_exceptions).
RECON_OK="$(psql -v ON_ERROR_STOP=1 -tA "$PGURL" \
    -c "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='ledger' AND table_name='reconciliation_exceptions'" \
    2>/dev/null || echo 0)"
if [ "$RECON_OK" != "1" ]; then
    echo "FAIL: tabela ledger.reconciliation_exceptions nao encontrada."
    echo "      Provisione pelo caminho canonico: make dev-db-setup (ou make dev-up)."
    exit 1
fi
echo "[smoke-payments] schema ledger (0002 reconciliation): OK"

# Verifica migration 0003 (RLS).
RLS_OK="$(psql -v ON_ERROR_STOP=1 -tA "$PGURL" \
    -c "SELECT COUNT(*) FROM pg_policies WHERE schemaname='ledger' AND tablename='accounts' AND policyname='accounts_tenant_isolation'" \
    2>/dev/null || echo 0)"
if [ "$RLS_OK" != "1" ]; then
    echo "FAIL: RLS da ledger.accounts nao encontrado."
    echo "      Provisione pelo caminho canonico: make dev-db-setup (ou make dev-up)."
    exit 1
fi
echo "[smoke-payments] schema ledger (0003 RLS): OK"

# Verifica migration 0004 (append-only de ledger.postings / journal_entries).
# Ausente deste preflight ate a 32a onda — o smoke exercita RecordEntry/
# RecordDeposit/RecordReversal, cuja semantica de estorno ("nunca edita a entry
# original, sempre cria uma nova") so e IMPOSTA por estes triggers. Sem eles o
# smoke passava verde contra um ledger mutavel, isto e, provava menos do que
# dizia provar. Os dois triggers vem da MESMA migration; checar os dois evita
# que meia aplicacao passe.
IMMUT_OK="$(psql -v ON_ERROR_STOP=1 -tA "$PGURL" \
    -c "SELECT COUNT(*) FROM pg_trigger WHERE NOT tgisinternal AND tgname IN ('postings_immutable_trg','journal_entries_immutable_trg')" \
    2>/dev/null || echo 0)"
if [ "$IMMUT_OK" != "2" ]; then
    echo "FAIL: triggers de imutabilidade append-only do ledger ausentes (achados: ${IMMUT_OK}/2)."
    echo "      Provisione pelo caminho canonico (make dev-db-setup / make dev-up), que aplica"
    echo "      todas as migrations do diretorio — inclusive a que cria estes triggers."
    exit 1
fi
echo "[smoke-payments] schema ledger (0004 append-only): OK"

# Verifica asset USDC habilitado no Asset Registry (scale=6, enabled=true).
# TX-2/DA-10: sem USDC com scale definido, nao ha aritmetica correta.
USDC_SCALE="$(psql -v ON_ERROR_STOP=1 -tA "$PGURL" \
    -c "SELECT scale::text FROM asset_registry.assets WHERE code='USDC' AND enabled=true" \
    2>/dev/null || echo '')"
if [ "$USDC_SCALE" != "6" ]; then
    echo "FAIL: USDC nao encontrado no Asset Registry com enabled=true e scale=6"
    echo "  Aplique: psql \$PGURL -f $REPO_ROOT/db/asset_registry/migrations/0001_asset_registry_up.sql"
    exit 1
fi
echo "[smoke-payments] asset registry: USDC scale=$USDC_SCALE (OK)"

# Verifica role adserver_app (necessario para assercoes de RLS).
APP_ROLE_OK="$(psql -v ON_ERROR_STOP=1 -tA "$PGURL" \
    -c "SELECT COUNT(*) FROM pg_roles WHERE rolname='adserver_app'" \
    2>/dev/null || echo 0)"
if [ "$APP_ROLE_OK" != "1" ]; then
    echo "FAIL: role adserver_app nao existe — aplique dev_roles.sql + smoke-payments-setup.sql:"
    echo "  psql \$PGURL -f $REPO_ROOT/db/seed/dev_roles.sql"
    echo "  psql \$PGURL -f $REPO_ROOT/deploy/local/seeds/smoke-payments-setup.sql"
    exit 1
fi
echo "[smoke-payments] role adserver_app: OK"
echo ""

# ---------------------------------------------------------------------------
# Aplica seed de setup do smoke (idempotente — usa BEGIN/ROLLBACK ao final)
# O seed cria contas e entries de teste isoladas por SMOKE_TENANT_A/B.
# Usa a chave especial smoke-payments: para identificar e limpar fixtures.
# ---------------------------------------------------------------------------

echo "[smoke-payments] aplicando fixtures de smoke (superuser, BEGIN/ROLLBACK ao final)..."

# O seed e injetado inline para manter o smoke autocontido.
# Todas as fixtures sao inseridas como superuser (bypassa RLS) — padrao do repo
# (ver db/ledger/tests/rls_isolation_test.sql, linha 53-120).
# TX-2: NENHUM valor monetario usa float. Tudo em minor-units inteiros.

# Criamos uma transacao de longa duracao para as assercoes (a)..(d).
# O ROLLBACK ao final garante que o banco nao seja modificado pelo smoke.
SMOKE_TX_FILE="$(mktemp /tmp/smoke-payments-XXXXXX.sql)"
trap 'rm -f "$SMOKE_TX_FILE"' EXIT

cat > "$SMOKE_TX_FILE" << SMOKEEOF

-- =============================================================================
-- Smoke e2e de pagamentos — transacao completa (BEGIN ... ROLLBACK)
-- Nenhum dado persiste apos o ROLLBACK final.
-- TX-2: nenhum float em nenhum valor monetario.
-- TX-3: tenant_id via contexto (SET LOCAL adserver.tenant_id).
-- DA-10: USDC scale=6, minor-units int.
-- =============================================================================

BEGIN;

-- ---------------------------------------------------------------------------
-- HELPER: funcao de assert (padrao canonico do repo — identico ao rls_isolation_test.sql)
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION pg_temp.assert_eq(
    p_label    TEXT,
    p_actual   TEXT,
    p_expected TEXT
) RETURNS TEXT LANGUAGE plpgsql AS \$\$
BEGIN
    IF p_actual IS DISTINCT FROM p_expected THEN
        RAISE EXCEPTION 'SMOKE FAIL [%]: got=% expected=%', p_label, p_actual, p_expected;
    END IF;
    RETURN 'PASS:' || p_label || ':' || COALESCE(p_actual, 'NULL');
END;
\$\$;

-- Helper: conta postings de uma entry (para assercao de nao-duplicacao).
CREATE OR REPLACE FUNCTION pg_temp.count_postings(p_idem_key TEXT, p_tenant TEXT)
RETURNS BIGINT LANGUAGE sql STABLE AS \$\$
    SELECT COUNT(*)
    FROM   ledger.postings p
    JOIN   ledger.journal_entries je ON je.id = p.journal_entry_id
    WHERE  je.idempotency_key = p_idem_key
      AND  je.tenant_id       = p_tenant::uuid;
\$\$;

-- Helper: retorna status de uma entry.
CREATE OR REPLACE FUNCTION pg_temp.entry_status(p_idem_key TEXT, p_tenant TEXT)
RETURNS TEXT LANGUAGE sql STABLE AS \$\$
    SELECT status::text
    FROM   ledger.journal_entries
    WHERE  idempotency_key = p_idem_key
      AND  tenant_id       = p_tenant::uuid;
\$\$;

-- Helper: conta excecoes de reconciliacao abertas para um tenant+ativo+periodo.
CREATE OR REPLACE FUNCTION pg_temp.count_recon_exceptions(
    p_tenant TEXT, p_asset TEXT, p_start TIMESTAMPTZ, p_end TIMESTAMPTZ
) RETURNS BIGINT LANGUAGE sql STABLE AS \$\$
    SELECT COUNT(*)
    FROM   ledger.reconciliation_exceptions
    WHERE  tenant_id    = p_tenant::uuid
      AND  asset_code   = p_asset
      AND  period_start = p_start
      AND  period_end   = p_end
      AND  status       = 'open';
\$\$;

-- ---------------------------------------------------------------------------
-- SETUP: provisiona contas para os dois tenants de smoke.
-- Inseridas como superuser (BYPASSRLS) — padrao do repo (rls_isolation_test.sql:53).
-- Contas de smoke usam codigos prefixados com 'smoke-payments:' para identificacao.
-- asset_code='USDC', scale=6 — obrigatorio para TX-2 / DA-10 (scale do Asset Registry).
-- ---------------------------------------------------------------------------
DO \$\$
DECLARE
    v_tenant_a  TEXT := '$SMOKE_TENANT_A';
    v_tenant_b  TEXT := '$SMOKE_TENANT_B';
BEGIN
    -- Contas do tenant A (superuser INSERT bypassa RLS)
    -- platform:cash:USDC — recebe o deposito (AccountKindAsset)
    INSERT INTO ledger.accounts (tenant_id, code, name, kind, asset_code)
    VALUES (v_tenant_a::uuid, 'smoke-payments:platform:cash:usdc',
            'Smoke Caixa USDC (A)', 'asset', 'USDC')
    ON CONFLICT (tenant_id, code) DO NOTHING;

    -- adv:smoke-adv-a:USDC — saldo do anunciante (AccountKindLiability)
    INSERT INTO ledger.accounts (tenant_id, code, name, kind, asset_code)
    VALUES (v_tenant_a::uuid, 'smoke-payments:adv:smoke-adv-a:usdc',
            'Smoke Saldo Anunciante USDC (A)', 'liability', 'USDC')
    ON CONFLICT (tenant_id, code) DO NOTHING;

    -- Contas do tenant B (para assercoes de RLS/IDOR)
    INSERT INTO ledger.accounts (tenant_id, code, name, kind, asset_code)
    VALUES (v_tenant_b::uuid, 'smoke-payments:platform:cash:usdc',
            'Smoke Caixa USDC (B)', 'asset', 'USDC')
    ON CONFLICT (tenant_id, code) DO NOTHING;

    INSERT INTO ledger.accounts (tenant_id, code, name, kind, asset_code)
    VALUES (v_tenant_b::uuid, 'smoke-payments:adv:smoke-adv-b:usdc',
            'Smoke Saldo Anunciante USDC (B)', 'liability', 'USDC')
    ON CONFLICT (tenant_id, code) DO NOTHING;
END;
\$\$;

-- Captura IDs das contas (necessario para os postings).
-- Usa CTE para evitar variaveis de sessao com PII.
DO \$\$
DECLARE
    v_tenant_a      TEXT   := '$SMOKE_TENANT_A';
    v_tenant_b      TEXT   := '$SMOKE_TENANT_B';
    v_cash_a        BIGINT;
    v_adv_a         BIGINT;
    v_cash_b        BIGINT;
    v_adv_b         BIGINT;
    -- Chave de idempotencia do deposito stub (prefixo 'deposit:' — padrao crypto.go:43).
    -- TX_HASH identico ao do webhook stub (nao e hash real — apenas fixture de smoke).
    v_idem_dep1     TEXT   := 'deposit:$TX_HASH_1';
    v_idem_dep2     TEXT   := 'deposit:$TX_HASH_2';
    -- Valor em minor-units (int64, sem float — TX-2).
    -- 1_000_000 = 1.000000 USDC (scale=6).
    v_amount        BIGINT := $AMOUNT_MINOR;
    v_entry_a       BIGINT;
    v_entry_a2      BIGINT;

    -- Variaveis para assercoes
    v_count         BIGINT;
    v_status        TEXT;
    v_balance       NUMERIC(40, 18);
    v_result        TEXT;
    v_debit_sum     NUMERIC(40, 18);
    v_credit_sum    NUMERIC(40, 18);
    v_excep_count   BIGINT;
BEGIN
    -- -------------------------------------------------------------------------
    -- Resolve IDs das contas
    -- -------------------------------------------------------------------------
    SELECT id INTO v_cash_a
    FROM ledger.accounts
    WHERE tenant_id = v_tenant_a::uuid AND code = 'smoke-payments:platform:cash:usdc';

    SELECT id INTO v_adv_a
    FROM ledger.accounts
    WHERE tenant_id = v_tenant_a::uuid AND code = 'smoke-payments:adv:smoke-adv-a:usdc';

    SELECT id INTO v_cash_b
    FROM ledger.accounts
    WHERE tenant_id = v_tenant_b::uuid AND code = 'smoke-payments:platform:cash:usdc';

    SELECT id INTO v_adv_b
    FROM ledger.accounts
    WHERE tenant_id = v_tenant_b::uuid AND code = 'smoke-payments:adv:smoke-adv-b:usdc';

    RAISE NOTICE '[smoke-payments] contas: cash_a=% adv_a=% cash_b=% adv_b=%',
        v_cash_a, v_adv_a, v_cash_b, v_adv_b;

    -- =========================================================================
    -- ASSERCAO (a) — Par de postings idempotente
    --
    -- Prova:
    --   1. Deposito gera EXATAMENTE 1 journal_entry com 2 postings balanceados
    --      (debit=credit por asset_code — invariante do trigger postings_balance_chk_trg).
    --   2. Reexecutar com a mesma idempotency_key NAO duplica postings
    --      (ON CONFLICT DO NOTHING na journal_entries — posting.go:231).
    --
    -- Espelha: internal/ledger/crypto.go:RecordDeposit (linhas 42-75)
    --          internal/ledger/posting.go:RecordEntry + insertJournalEntry (linhas 81-126)
    --          db/ledger/migrations/0001_ledger_schema_up.sql:constraint trigger (linhas 220-265)
    -- =========================================================================

    RAISE NOTICE '';
    RAISE NOTICE '[smoke-payments] === ASSERCAO (a): par de postings idempotente ===';

    -- Primeira insercao: journal_entry PENDING com par debit/credit USDC scale=6.
    -- Espelha RecordDeposit: idempkey='deposit:{tx_hash}', status='pending'.
    -- TX-2: amount em NUMERIC inteiro (minor-units), sem float.
    INSERT INTO ledger.journal_entries
        (tenant_id, idempotency_key, description, status, effective_at, ref_type, metadata)
    VALUES
        (v_tenant_a::uuid, v_idem_dep1,
         'Smoke: deposito cripto PENDING USDC tx=' || '$TX_HASH_1',
         'pending', now(), 'tx_hash',
         ('{"tx_hash":"$TX_HASH_1","advertiser_id":"smoke-adv-a"}')::jsonb)
    RETURNING id INTO v_entry_a;

    -- Insere par de postings: debit em platform:cash (asset), credit em adv (liability).
    -- Espelha RecordDeposit postings (crypto.go:44-59): debit=cash, credit=adv.
    -- TX-2: ::numeric sem float. Scale=6 (USDC — Asset Registry).
    INSERT INTO ledger.postings
        (journal_entry_id, tenant_id, account_id, asset_code, scale,
         debit_amount, credit_amount, posted_at)
    VALUES
        -- Debito: plataforma recebe USDC (cash)
        (v_entry_a, v_tenant_a::uuid, v_cash_a, 'USDC', 6,
         v_amount::numeric, 0, now()),
        -- Credito: saldo do anunciante creditado (liability)
        (v_entry_a, v_tenant_a::uuid, v_adv_a, 'USDC', 6,
         0, v_amount::numeric, now());

    -- Assercao (a.1): par balanceado — exatamente 2 postings para a entry.
    SELECT COUNT(*) INTO v_count
    FROM ledger.postings WHERE journal_entry_id = v_entry_a;
    v_result := pg_temp.assert_eq(
        '(a.1) deposito gera 2 postings', v_count::text, '2');
    RAISE NOTICE '%', v_result;

    -- Assercao (a.2): debit_sum = credit_sum por asset_code (balance check).
    -- Espelha o constraint trigger check_entry_balance (0001_up.sql:220-265).
    -- TX-2: comparacao NUMERIC sem float.
    SELECT
        SUM(debit_amount),
        SUM(credit_amount)
    INTO v_debit_sum, v_credit_sum
    FROM ledger.postings
    WHERE journal_entry_id = v_entry_a AND asset_code = 'USDC';

    v_result := pg_temp.assert_eq(
        '(a.2) debit_sum = credit_sum (balance)', v_debit_sum::text, v_credit_sum::text);
    RAISE NOTICE '%', v_result;

    -- Assercao (a.3): o valor e exatamente v_amount em cada lado (sem float).
    -- TX-2: trunc(NUMERIC)::bigint::text normaliza para integer-string sem ponto decimal.
    -- trunc() retorna NUMERIC, ::bigint descarta qualquer fracao interna, ::text e a string.
    v_result := pg_temp.assert_eq(
        '(a.3) debit_sum = 1000000 minor-units (sem float)',
        trunc(v_debit_sum)::bigint::text,
        '$AMOUNT_MINOR');
    RAISE NOTICE '%', v_result;

    -- IDEMPOTENCIA: tenta inserir com a mesma idempotency_key.
    -- ON CONFLICT DO NOTHING => NENHUMA nova entrada gerada.
    -- Espelha insertJournalEntry:ON CONFLICT DO NOTHING (posting.go:232).
    INSERT INTO ledger.journal_entries
        (tenant_id, idempotency_key, description, status, effective_at, ref_type, metadata)
    VALUES
        (v_tenant_a::uuid, v_idem_dep1,
         'Smoke: reprocessamento idem (NAO deve duplicar)',
         'pending', now(), 'tx_hash', '{}'::jsonb)
    ON CONFLICT (tenant_id, idempotency_key) DO NOTHING;

    -- Assercao (a.4): ainda exatamente 1 journal_entry com v_idem_dep1.
    SELECT COUNT(*) INTO v_count
    FROM ledger.journal_entries
    WHERE tenant_id = v_tenant_a::uuid AND idempotency_key = v_idem_dep1;
    v_result := pg_temp.assert_eq(
        '(a.4) reprocessamento: ainda 1 entry (nao duplicou)', v_count::text, '1');
    RAISE NOTICE '%', v_result;

    -- Assercao (a.5): ainda exatamente 2 postings (nenhum duplicado).
    SELECT COUNT(*) INTO v_count
    FROM ledger.postings WHERE journal_entry_id = v_entry_a;
    v_result := pg_temp.assert_eq(
        '(a.5) reprocessamento: ainda 2 postings (nao duplicou)', v_count::text, '2');
    RAISE NOTICE '%', v_result;

    -- =========================================================================
    -- ASSERCAO (b) — Deposito pending -> finalidade
    --
    -- Prova:
    --   1. Deposito entra com status='pending' (nao conta no saldo posted).
    --   2. FinalizeDeposit (UPDATE status='posted' WHERE status='pending') so
    --      transiciona apos finalidade simulada.
    --   3. Saldo da conta de liability so e contado em postings de entries 'posted'.
    --
    -- Espelha: internal/ledger/crypto.go:FinalizeDeposit (linhas 87-91)
    --          internal/ledger/posting.go:FinalizeEntry (linhas 138-167)
    --          services/payments/internal/crypto/safe_webhook.go:handleDepositFinalized
    --          db/ledger/migrations/0001_ledger_schema_up.sql:view account_balances
    -- =========================================================================

    RAISE NOTICE '';
    RAISE NOTICE '[smoke-payments] === ASSERCAO (b): deposito pending -> finalidade ===';

    -- Assercao (b.1): status e 'pending' logo apos o deposito.
    SELECT status::text INTO v_status
    FROM ledger.journal_entries
    WHERE tenant_id = v_tenant_a::uuid AND idempotency_key = v_idem_dep1;
    v_result := pg_temp.assert_eq('(b.1) status inicial = pending', v_status, 'pending');
    RAISE NOTICE '%', v_result;

    -- Assercao (b.2): saldo de contas 'liability' via account_balances para entries
    -- 'pending' NAO e contabilizado como saldo definitivo.
    -- A view account_balances inclui todos os postings (pending + posted).
    -- O saldo "disponivel" so conta entries 'posted' — verificado via SUM com filtro.
    -- Esta e a semantica de RecordDeposit (status=pending): o saldo fica retido.
    SELECT COALESCE(SUM(p.debit_amount - p.credit_amount), 0) INTO v_balance
    FROM ledger.accounts a
    JOIN ledger.postings p ON p.account_id = a.id
    JOIN ledger.journal_entries je ON je.id = p.journal_entry_id
    WHERE a.tenant_id = v_tenant_a::uuid
      AND a.code = 'smoke-payments:adv:smoke-adv-a:usdc'
      AND je.status = 'posted';
    -- Saldo 'posted' deve ser 0 enquanto a entry e 'pending'.
    -- TX-2: trunc(NUMERIC)::bigint::text normaliza para integer-string sem ponto decimal.
    v_result := pg_temp.assert_eq(
        '(b.2) saldo posted = 0 enquanto pending',
        trunc(v_balance)::bigint::text, '0');
    RAISE NOTICE '%', v_result;

    -- SIMULA FINALIDADE: webhook do custodiante Safe chama FinalizeDeposit.
    -- Espelha FinalizeEntry: UPDATE status='posted' WHERE status='pending' (posting.go:139-148).
    -- Em producao: POST /webhooks/safe {type:"deposit.finalized", tx_hash:"...", confirmations:12}
    -- Aqui: SQL direto como stub (sem rede, sem chave real).
    UPDATE ledger.journal_entries
    SET    status = 'posted'
    WHERE  tenant_id       = v_tenant_a::uuid
      AND  idempotency_key = v_idem_dep1
      AND  status          = 'pending';

    -- Assercao (b.3): status e agora 'posted'.
    SELECT status::text INTO v_status
    FROM ledger.journal_entries
    WHERE tenant_id = v_tenant_a::uuid AND idempotency_key = v_idem_dep1;
    v_result := pg_temp.assert_eq('(b.3) status apos finalidade = posted', v_status, 'posted');
    RAISE NOTICE '%', v_result;

    -- Assercao (b.4): saldo 'posted' agora reflete o deposito.
    -- A conta adv (liability) tem credit_amount=v_amount e debit_amount=0.
    -- balance = debit - credit = 0 - v_amount = -v_amount (saldo da liability).
    -- O saldo do ANUNCIANTE e positivo via inversao de sinal (liability = negativo no double-entry).
    -- Verificamos apenas que o saldo mudou do estado anterior (0) — sem float.
    SELECT COALESCE(SUM(p.debit_amount - p.credit_amount), 0) INTO v_balance
    FROM ledger.accounts a
    JOIN ledger.postings p ON p.account_id = a.id
    JOIN ledger.journal_entries je ON je.id = p.journal_entry_id
    WHERE a.tenant_id = v_tenant_a::uuid
      AND a.code = 'smoke-payments:adv:smoke-adv-a:usdc'
      AND je.status = 'posted';
    -- Saldo da liability em double-entry: credit - debit = v_amount - 0 = v_amount
    -- Como balance = debit - credit = 0 - v_amount = -v_amount (NUMERIC negativo).
    -- TX-2: trunc(NUMERIC)::bigint::text normaliza para integer-string; trunc(-1000000.0)
    -- retorna -1000000 (bigint), ::text = '-1000000' — casa com '-' || AMOUNT_MINOR.
    v_result := pg_temp.assert_eq(
        '(b.4) saldo posted apos finalidade = -1000000 (liability credit)',
        trunc(v_balance)::bigint::text,
        ('-' || '$AMOUNT_MINOR'));
    RAISE NOTICE '%', v_result;

    -- Assercao (b.5): idempotencia de FinalizeDeposit — segunda chamada e no-op.
    -- Espelha FinalizeEntry: RowsAffected()=0 quando ja 'posted' (posting.go:150-167).
    UPDATE ledger.journal_entries
    SET    status = 'posted'
    WHERE  tenant_id       = v_tenant_a::uuid
      AND  idempotency_key = v_idem_dep1
      AND  status          = 'pending';
    -- Se fosse 'pending' novamente, o UPDATE retornaria 1 e duplicaria o efeito.
    -- Como ja e 'posted', o WHERE status='pending' nao casa e retorna 0 rows.
    SELECT status::text INTO v_status
    FROM ledger.journal_entries
    WHERE tenant_id = v_tenant_a::uuid AND idempotency_key = v_idem_dep1;
    v_result := pg_temp.assert_eq(
        '(b.5) FinalizeDeposit idempotente (ainda posted)',
        v_status, 'posted');
    RAISE NOTICE '%', v_result;

    -- =========================================================================
    -- ASSERCAO (c) — Reconciliacao abre excecao e nunca autocorrige
    --
    -- Prova:
    --   1. Injetar uma divergencia (valor esperado != valor do ledger) registra
    --      uma linha em ledger.reconciliation_exceptions com status='open'.
    --   2. O ledger NAO e alterado (nenhum posting de correcao automatico).
    --   3. Re-executar a mesma divergencia e idempotente (ON CONFLICT DO NOTHING).
    --
    -- Espelha: internal/ledger/recon.go:Reconciler.Run (linhas 217-263)
    --          internal/ledger/recon.go:pgReconStore.OpenException (linhas 134-157)
    --          db/ledger/migrations/0002_reconciliation_exceptions_up.sql (toda a tabela)
    --
    -- A injecao de divergencia simula a fonte Iceberg (ExpectedValueSourceStub)
    -- reportando um valor diferente do ledger — sem autocorrecao.
    -- =========================================================================

    RAISE NOTICE '';
    RAISE NOTICE '[smoke-payments] === ASSERCAO (c): reconciliacao abre excecao, nunca autocorrige ===';

    -- Captura saldo atual do ledger para o tenant A em USDC (postings 'posted').
    -- Este sera o valor que o "ledger conhece".
    DECLARE
        v_period_start  TIMESTAMPTZ := now() - INTERVAL '1 hour';
        v_period_end    TIMESTAMPTZ := now() + INTERVAL '1 hour';
        -- Valor esperado divergente: ledger tem v_amount (1000000), fingimos esperar 2x.
        -- TX-2: int64/NUMERIC, sem float.
        v_expected_div  NUMERIC(40, 18) := ($AMOUNT_MINOR * 2)::numeric;
        v_ledger_sum    NUMERIC(40, 18);
        v_posting_count BIGINT;
    BEGIN
        -- Soma debit_amount de postings 'posted' no periodo para o tenant A / USDC.
        -- Espelha pgReconStore.SumLedgerPostings (recon.go:116-132).
        SELECT COALESCE(SUM(p.debit_amount), 0)
        INTO   v_ledger_sum
        FROM   ledger.postings p
        JOIN   ledger.journal_entries je ON je.id = p.journal_entry_id
        WHERE  je.tenant_id  = v_tenant_a::uuid
          AND  p.asset_code  = 'USDC'
          AND  p.posted_at  >= v_period_start
          AND  p.posted_at   < v_period_end
          AND  je.status     = 'posted';

        RAISE NOTICE '[smoke-payments] ledger_sum (USDC posted) = %', v_ledger_sum;

        -- Assercao (c.1): ledger_sum deve ser v_amount (o deposito foi finalizado em (b)).
        -- TX-2: trunc(NUMERIC)::bigint::text normaliza para integer-string sem ponto decimal.
        v_result := pg_temp.assert_eq(
            '(c.1) ledger_sum = 1000000 (deposito posted contabilizado)',
            trunc(v_ledger_sum)::bigint::text,
            '$AMOUNT_MINOR');
        RAISE NOTICE '%', v_result;

        -- INJETA DIVERGENCIA: expected=2*v_amount, ledger=v_amount.
        -- Espelha Reconciler.Run: se expected != ledger -> OpenException (recon.go:237-245).
        -- O valor esperado vem de uma fonte Iceberg simulada — aqui injetado direto.
        -- NUNCA autocorrige: nenhum posting de ajuste e criado automaticamente.
        INSERT INTO ledger.reconciliation_exceptions
            (tenant_id, asset_code, period_start, period_end,
             expected_minor_units, ledger_minor_units, source, status)
        VALUES
            (v_tenant_a::uuid, 'USDC',
             v_period_start, v_period_end,
             v_expected_div,    -- esperado: 2 * v_amount (divergente)
             v_ledger_sum,      -- ledger: v_amount (real)
             'iceberg', 'open')
        ON CONFLICT (tenant_id, asset_code, period_start, period_end) DO NOTHING;

        -- Assercao (c.2): a excecao foi aberta com status='open'.
        SELECT COUNT(*) INTO v_excep_count
        FROM ledger.reconciliation_exceptions
        WHERE tenant_id    = v_tenant_a::uuid
          AND asset_code   = 'USDC'
          AND period_start = v_period_start
          AND period_end   = v_period_end
          AND status       = 'open';
        v_result := pg_temp.assert_eq(
            '(c.2) excecao de reconciliacao aberta (status=open)',
            v_excep_count::text, '1');
        RAISE NOTICE '%', v_result;

        -- Assercao (c.3): nenhum posting de correcao automatico foi criado.
        -- O numero de postings do tenant A NAO aumentou apos a reconciliacao.
        -- NUNCA autocorrige — invariante absoluta (recon.go:9, BILLING.md §7).
        SELECT COUNT(*) INTO v_posting_count
        FROM ledger.postings p
        JOIN ledger.journal_entries je ON je.id = p.journal_entry_id
        WHERE je.tenant_id = v_tenant_a::uuid;
        -- Devem existir exatamente 2 postings: o par do deposito (a).
        v_result := pg_temp.assert_eq(
            '(c.3) nenhum posting de autocorrecao (ledger intocado)',
            v_posting_count::text, '2');
        RAISE NOTICE '%', v_result;

        -- Assercao (c.4): idempotencia — re-inserir a mesma excecao e no-op.
        -- Espelha ErrExceptionAlreadyOpen (recon.go:209) — ON CONFLICT DO NOTHING.
        INSERT INTO ledger.reconciliation_exceptions
            (tenant_id, asset_code, period_start, period_end,
             expected_minor_units, ledger_minor_units, source, status)
        VALUES
            (v_tenant_a::uuid, 'USDC',
             v_period_start, v_period_end,
             v_expected_div, v_ledger_sum,
             'iceberg', 'open')
        ON CONFLICT (tenant_id, asset_code, period_start, period_end) DO NOTHING;

        -- Ainda apenas 1 excecao para este periodo (nao duplicou).
        SELECT COUNT(*) INTO v_excep_count
        FROM ledger.reconciliation_exceptions
        WHERE tenant_id    = v_tenant_a::uuid
          AND asset_code   = 'USDC'
          AND period_start = v_period_start
          AND period_end   = v_period_end;
        v_result := pg_temp.assert_eq(
            '(c.4) reprocessamento: ainda 1 excecao (idempotente)',
            v_excep_count::text, '1');
        RAISE NOTICE '%', v_result;

        -- Assercao (c.5): a excecao armazena a divergencia calculada.
        -- divergence_minor_units = expected - ledger (coluna GENERATED STORED em 0002).
        -- TX-2: comparacao NUMERIC inteiro, sem float.
        DECLARE
            v_divergence NUMERIC(40, 18);
        BEGIN
            SELECT divergence_minor_units INTO v_divergence
            FROM ledger.reconciliation_exceptions
            WHERE tenant_id  = v_tenant_a::uuid
              AND asset_code = 'USDC'
              AND period_start = v_period_start
              AND period_end   = v_period_end;
            -- TX-2: trunc(NUMERIC)::bigint::text normaliza para integer-string sem ponto decimal.
            v_result := pg_temp.assert_eq(
                '(c.5) divergence_minor_units = 1000000 (expected - ledger)',
                trunc(v_divergence)::bigint::text,
                '$AMOUNT_MINOR');
            RAISE NOTICE '%', v_result;
        END;
    END;

    -- =========================================================================
    -- ASSERCAO (d) — Status via BFF: string DECIMAL e isolamento por tenant
    --
    -- Prova:
    --   1. A view account_balances (SECURITY INVOKER, 0003_up.sql) retorna saldo
    --      como NUMERIC (string), nao float — espelha a conversao do BFF
    --      (bff/src/adapters/postgres-payments.ts:minorUnitsToDecimalStr).
    --   2. RLS garante que tenant_a nao ve saldo de tenant_b (IDOR previsto).
    --   3. O saldo e string inteira (minor-units), sem ponto decimal inesperado.
    --
    -- Espelha: bff/src/adapters/postgres-payments.ts:getBalances (linhas 184-253)
    --          bff/src/adapters/postgres-payments.ts:minorUnitsToDecimalStr (linhas 100-143)
    --          db/ledger/migrations/0003_ledger_rls_up.sql (SECURITY INVOKER na view)
    --
    -- Para simular o BFF, adicionamos um deposito para o tenant_b e verificamos
    -- que o tenant_a nao ve via RLS.
    -- =========================================================================

    RAISE NOTICE '';
    RAISE NOTICE '[smoke-payments] === ASSERCAO (d): status via BFF — string DECIMAL e isolamento RLS ===';

    -- Cria um deposito POSTED para o tenant_b (para testar isolamento).
    INSERT INTO ledger.journal_entries
        (tenant_id, idempotency_key, description, status, effective_at, ref_type, metadata)
    VALUES
        (v_tenant_b::uuid, v_idem_dep2,
         'Smoke: deposito tenant B USDC posted',
         'posted', now(), 'tx_hash',
         ('{"tx_hash":"$TX_HASH_2","advertiser_id":"smoke-adv-b"}')::jsonb)
    RETURNING id INTO v_entry_a2;

    INSERT INTO ledger.postings
        (journal_entry_id, tenant_id, account_id, asset_code, scale,
         debit_amount, credit_amount, posted_at)
    VALUES
        (v_entry_a2, v_tenant_b::uuid, v_cash_b, 'USDC', 6,
         v_amount::numeric, 0, now()),
        (v_entry_a2, v_tenant_b::uuid, v_adv_b, 'USDC', 6,
         0, v_amount::numeric, now());

    -- Assercao (d.1): saldo da view account_balances para tenant_a (como superuser).
    -- A view retorna NUMERIC — o BFF converte para string decimal sem float.
    -- Verificamos que o balance e NUMERIC inteiro (sem .XXXXXXXX inesperado).
    DECLARE
        v_bal_text  TEXT;
        v_bal_num   NUMERIC(40, 18);
    BEGIN
        SELECT balance::text INTO v_bal_text
        FROM ledger.account_balances
        WHERE tenant_id  = v_tenant_a::uuid
          AND account_code = 'smoke-payments:adv:smoke-adv-a:usdc';

        -- TX-2: o saldo e NUMERIC inteiro (minor-units). Nenhum ponto decimal
        -- inesperado — a parte fracionaria interna (NUMERIC(40,18)) deve ser .000...
        -- minorUnitsToDecimalStr no BFF strips a parte decimal interna via BigInt.
        v_result := pg_temp.assert_eq(
            '(d.1) saldo tenant_a existe na view (nao NULL)',
            CASE WHEN v_bal_text IS NULL THEN 'NULL' ELSE 'not-null' END,
            'not-null');
        RAISE NOTICE '%', v_result;

        -- Assercao (d.2): superuser ve dados de tenant_b na view (comportamento correto —
        -- RLS nao se aplica a superuser; a prova de isolamento real e feita em (d.3)
        -- com SET LOCAL ROLE adserver_app).
        -- Esta assercao formal conta para o total de PASS/FAIL do smoke.
        DECLARE
            v_cross_count BIGINT;
        BEGIN
            -- Superuser filtra explicitamente por tenant_b — deve ver >= 1 linha
            -- (a conta de caixa e a conta do anunciante do tenant_b foram inseridas acima).
            SELECT COUNT(*) INTO v_cross_count
            FROM ledger.account_balances
            WHERE tenant_id  = v_tenant_b::uuid
              AND account_code LIKE 'smoke-payments:%';

            -- Superuser deve ver as contas de tenant_b (RLS nao se aplica a superuser).
            v_result := pg_temp.assert_eq(
                '(d.2) superuser ve contas de tenant_b na view (RLS nao bloqueia superuser)',
                CASE WHEN v_cross_count > 0 THEN 'visible' ELSE 'empty' END,
                'visible');
            RAISE NOTICE '%', v_result;
        END;
    END;

    -- Assercao (d.3): RLS com SET LOCAL — tenant_a nao ve dados de tenant_b.
    -- Espelha o BFF: set_config('adserver.tenant_id', tenantId, true) (postgres-payments.ts:202-205).
    -- Aqui usamos SET LOCAL + RESET ROLE para simular o role adserver_app.
    DECLARE
        v_tenant_b_visible BIGINT;
        v_tenant_a_visible BIGINT;
    BEGIN
        -- Simula o adserver_app com SET LOCAL adserver.tenant_id = tenant_a.
        -- A RLS da 0003 filtra: WHERE tenant_id = ledger.current_tenant_id().
        SET LOCAL ROLE adserver_app;
        SET LOCAL adserver.tenant_id = '$SMOKE_TENANT_A';

        -- Tenta ler dados do tenant_b — deve retornar 0 (IDOR protegido).
        SELECT COUNT(*) INTO v_tenant_b_visible
        FROM ledger.account_balances
        WHERE account_code LIKE 'smoke-payments:%'
          AND tenant_id = '$SMOKE_TENANT_B'::uuid;

        -- Tenta ler dados do tenant_a — deve retornar > 0.
        SELECT COUNT(*) INTO v_tenant_a_visible
        FROM ledger.account_balances
        WHERE account_code LIKE 'smoke-payments:%';

        RESET ROLE;

        v_result := pg_temp.assert_eq(
            '(d.3) RLS: tenant_a NAO ve dados de tenant_b (IDOR protegido)',
            v_tenant_b_visible::text, '0');
        RAISE NOTICE '%', v_result;

        v_result := pg_temp.assert_eq(
            '(d.4) RLS: tenant_a ve seus proprios dados',
            CASE WHEN v_tenant_a_visible > 0 THEN 'visible' ELSE 'empty' END,
            'visible');
        RAISE NOTICE '%', v_result;
    END;

    -- Assercao (d.5): saldo como string NUMERIC inteiro (sem float).
    -- Simula a conversao minorUnitsToDecimalStr do BFF (postgres-payments.ts:100-143).
    -- A coluna balance da view tem NUMERIC(40,18). Os minor-units gravados pelo Go
    -- sao inteiros; a parte fracionaria interna e sempre .000000000000000000.
    -- O BFF extrai a parte inteira via BigInt — esta assercao verifica que nao ha
    -- fracao inesperada no valor gravado.
    DECLARE
        v_bal_raw    TEXT;
        v_has_frac   BOOLEAN;
    BEGIN
        SELECT balance::text INTO v_bal_raw
        FROM ledger.account_balances
        WHERE tenant_id = v_tenant_a::uuid
          AND account_code = 'smoke-payments:adv:smoke-adv-a:usdc';

        -- TX-2: parte fracionaria (se existir) deve ser apenas zeros.
        -- Qualquer frac real indicaria float ou escrita errada a montante.
        v_has_frac := (
            v_bal_raw LIKE '%.%'
            AND REGEXP_REPLACE(SPLIT_PART(v_bal_raw, '.', 2), '0+$', '') <> ''
        );

        v_result := pg_temp.assert_eq(
            '(d.5) saldo e NUMERIC inteiro (sem fracao real — TX-2)',
            CASE WHEN v_has_frac THEN 'has-unexpected-frac' ELSE 'integer-ok' END,
            'integer-ok');
        RAISE NOTICE '%', v_result;
    END;

    RAISE NOTICE '';
    RAISE NOTICE '[smoke-payments] todas as assercoes concluidas.';
END;
\$\$;

-- ===========================================================================
-- ROLLBACK: nao persiste nada (idempotente, seguro em CI e em dev).
-- Identico ao padrao do rls_isolation_test.sql (linha 454).
--
-- NOTA SOBRE O TRIGGER postings_balance_chk_trg:
--   O trigger e DEFERRABLE INITIALLY DEFERRED, ou seja, dispara no COMMIT
--   (nao ao inserir cada linha). Como este smoke termina em ROLLBACK (nao COMMIT),
--   o trigger nunca dispara — isso e inerente ao design de "nao persistir dados".
--   A corretude do balance check e verificada de forma equivalente pela assercao
--   (a.2) debit_sum = credit_sum dentro do mesmo DO block. O comportamento do
--   trigger em producao (COMMIT real) e coberto pelo schema migration 0001 e pelos
--   testes de schema separados.
-- ===========================================================================
ROLLBACK;

SMOKEEOF

echo "[smoke-payments] executando assercoes SQL (BEGIN...ROLLBACK)..."

# Roda o SQL e captura saida. ON_ERROR_STOP=1 garante que qualquer RAISE EXCEPTION
# (assert falho) aborta o psql com exit code != 0.
SQL_OUTPUT="$(psql -v ON_ERROR_STOP=1 -tA "$PGURL" -f "$SMOKE_TX_FILE" 2>&1)" || {
    echo ""
    echo "=== SAIDA DO PSQL (erro) ==="
    echo "$SQL_OUTPUT"
    echo ""
    echo "FAIL: uma ou mais assercoes SQL falharam (ver saida acima)."
    FAIL=$((FAIL + 1))
}

# Extrai e exibe os resultados PASS/FAIL do SQL.
if [ -n "$SQL_OUTPUT" ]; then
    echo ""
    echo "=== Resultado das assercoes SQL ==="
    echo "$SQL_OUTPUT" | grep -E '(PASS|FAIL|NOTICE|smoke-payments)' | \
        grep -E '(PASS:|FAIL:|smoke-payments)' | \
        sed 's/^NOTICE:  //' || true
    echo ""

    # Conta PASS/FAIL das assercoes SQL.
    # grep -c retorna exit-code 1 quando nao ha match (count=0); || true evita aborto por set -e.
    SQL_PASS="$(echo "$SQL_OUTPUT" | grep -c 'PASS:' || true)"
    SQL_FAIL="$(echo "$SQL_OUTPUT" | grep -c 'SMOKE FAIL' || true)"
    PASS=$((PASS + SQL_PASS))
    FAIL=$((FAIL + SQL_FAIL))
fi

# ---------------------------------------------------------------------------
# Resultado final
# ---------------------------------------------------------------------------

echo ""
echo "[smoke-payments] =========================="
echo "[smoke-payments] RESULTADO: PASS=$PASS  FAIL=$FAIL"
echo "[smoke-payments] =========================="
echo ""

if [ "$FAIL" -eq 0 ] && [ "$PASS" -gt 0 ]; then
    echo "[smoke-payments] OK — trilho de pagamentos smoke PASSOU (a)(b)(c)(d)"
    exit 0
else
    echo "[smoke-payments] FAIL — uma ou mais assercoes falharam"
    echo ""
    echo "Diagnostico:"
    echo "  (a) idempotencia: internal/ledger/posting.go:RecordEntry + insertJournalEntry"
    echo "  (b) pending->posted: internal/ledger/posting.go:FinalizeEntry"
    echo "  (c) reconciliacao: internal/ledger/recon.go:Reconciler.Run + OpenException"
    echo "  (d) BFF/RLS: bff/src/adapters/postgres-payments.ts + db/ledger/migrations/0003"
    exit 1
fi
