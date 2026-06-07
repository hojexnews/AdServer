-- =============================================================================
-- 0001_ledger_schema_up.sql
-- Ledger double-entry (§2.6/TX-2) — Postgres 16.
--
-- Ferramenta de migração: golang-migrate
-- float PROIBIDO: toda coluna monetária usa NUMERIC(p,s) conforme Asset Registry.
-- Multi-moeda sem conversão automática (DA-10): cada ativo é ledger isolado.
-- Câmbio só como par de postings explícito com taxa registrada.
--
-- Invariantes garantidas:
--   1. sum(debit_amount) = sum(credit_amount) por journal_entry (constraint trigger).
--   2. Nenhuma captura grava saldo direto (sem UPDATE em accounts.balance).
--   3. Idempotency key por captura (UNIQUE em journal_entries.idempotency_key).
--   4. Reconciliação abre exceção, NUNCA autocorrige.
--   5. Faturamento reconcilia contra lakehouse Iceberg, NUNCA contra streaming.
-- =============================================================================

CREATE SCHEMA IF NOT EXISTS ledger;

-- ---------------------------------------------------------------------------
-- Tipo ENUM
-- ---------------------------------------------------------------------------
CREATE TYPE ledger.account_kind AS ENUM (
    'asset',        -- recursos que a plataforma possui (ex.: caixa)
    'liability',    -- obrigações (ex.: saldo de anunciante pré-pago)
    'revenue',      -- receita de veiculação (CPM/CPC/CPA/Tenancy)
    'expense',      -- custos operacionais
    'equity'        -- patrimônio
);

CREATE TYPE ledger.entry_status AS ENUM (
    'pending',      -- depósito cripto aguardando finalidade (N confirmações)
    'posted',       -- confirmado e imutável
    'void'          -- estornado (via novo par de postings, nunca edição)
);

-- ---------------------------------------------------------------------------
-- accounts — plano de contas (uma conta por ativo por entidade)
-- Saldo não é armazenado aqui (sem UPDATE direto em balance).
-- O saldo corrente é derivado de: SELECT sum(debit_amount - credit_amount) FROM postings.
-- ---------------------------------------------------------------------------
CREATE TABLE ledger.accounts (
    id              BIGSERIAL            NOT NULL,
    tenant_id       UUID                 NOT NULL,
    -- Código legível: ex. 'adv:123:BRL', 'platform:revenue:BRL', 'rounding:BRL'
    code            TEXT                 NOT NULL,
    name            TEXT                 NOT NULL,
    kind            ledger.account_kind  NOT NULL,
    -- asset_code = FK lógica para asset_registry.assets.code
    -- Cada conta é associada a UM único ativo (ledger isolado por ativo — DA-10).
    asset_code      TEXT                 NOT NULL,
    -- Accounts só podem ter movimentações em ativos habilitados.
    -- A verificação em runtime ocorre no nível de aplicação (não FK direta
    -- porque asset_registry está em schema separado e pode ser deploy separado).
    active          BOOLEAN              NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ          NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ          NOT NULL DEFAULT now(),

    CONSTRAINT accounts_pk           PRIMARY KEY (id),
    CONSTRAINT accounts_code_uq      UNIQUE (tenant_id, code),
    CONSTRAINT accounts_code_fmt_chk CHECK (code ~ '^[a-z0-9:_\-]+$')
);

CREATE INDEX accounts_tenant_idx     ON ledger.accounts (tenant_id);
CREATE INDEX accounts_asset_idx      ON ledger.accounts (asset_code);
CREATE INDEX accounts_kind_idx       ON ledger.accounts (kind);

COMMENT ON TABLE  ledger.accounts            IS 'Plano de contas. Saldo derivado de postings (nunca armazenado direto).';
COMMENT ON COLUMN ledger.accounts.asset_code IS 'FK lógica para asset_registry.assets.code. Uma conta por ativo (DA-10).';
COMMENT ON COLUMN ledger.accounts.code       IS 'Código legível: ex. adv:123:BRL, platform:revenue:BRL, rounding:BRL.';

-- ---------------------------------------------------------------------------
-- journal_entries — cabeçalho de cada transação contábil
-- idempotency_key garante que um evento faturável não gere dupla entrada.
-- ---------------------------------------------------------------------------
CREATE TABLE ledger.journal_entries (
    id               BIGSERIAL           NOT NULL,
    tenant_id        UUID                NOT NULL,
    -- idempotency_key: derivado do event_id do envelope TX-1 + tipo de operação.
    -- Ex.: 'imp:evt-uuid-...' para CPM, 'clk:evt-uuid-...' para CPC.
    -- UNIQUE garante que reprocessamentos (at-least-once) não criem duplicatas.
    idempotency_key  TEXT                NOT NULL,
    description      TEXT                NOT NULL,
    status           ledger.entry_status NOT NULL DEFAULT 'posted',
    -- effective_at: data/hora de competência do fato (diferente de created_at).
    effective_at     TIMESTAMPTZ         NOT NULL DEFAULT now(),
    -- ref_type / ref_id: referência ao objeto de origem (campaign, impression, etc.)
    ref_type         TEXT,
    ref_id           BIGINT,
    -- metadata JSON para contexto extra (sem PII — DA-11/TX-3).
    metadata         JSONB,
    created_at       TIMESTAMPTZ         NOT NULL DEFAULT now(),

    CONSTRAINT journal_entries_pk             PRIMARY KEY (id),
    CONSTRAINT journal_entries_idempotency_uq UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX journal_entries_tenant_idx      ON ledger.journal_entries (tenant_id);
CREATE INDEX journal_entries_effective_idx   ON ledger.journal_entries (effective_at);
CREATE INDEX journal_entries_ref_idx         ON ledger.journal_entries (ref_type, ref_id);
CREATE INDEX journal_entries_status_idx      ON ledger.journal_entries (status);

COMMENT ON TABLE  ledger.journal_entries                  IS 'Cabeçalho de transação contábil double-entry.';
COMMENT ON COLUMN ledger.journal_entries.idempotency_key  IS 'Chave única por captura (event_id TX-1). Reprocessamentos at-least-once são idempotentes.';
COMMENT ON COLUMN ledger.journal_entries.effective_at     IS 'Data de competência do fato contábil (diferente de created_at).';

-- ---------------------------------------------------------------------------
-- postings — linhas de débito/crédito, particionadas por mês
--
-- Particionamento: RANGE em posted_at (timestamp de postagem) por mês.
-- Razão: postings são append-only; queries de reconciliação e relatórios
-- filtram por período; partição mensal é a granularidade mínima útil.
--
-- amount: NUMERIC(40, 18) cobre o pior caso (ERC-20, scale=18, valores grandes).
-- Precision de 40 dígitos previne overflow para qualquer ativo atual.
-- scale é armazenado junto para auditoria e verificação de divergência com Registry.
--
-- Invariante sum(debit_amount) = sum(credit_amount) por entry:
-- Implementada como CONSTRAINT TRIGGER (ver abaixo) em vez de CHECK puro
-- porque CHECK de row-level não pode fazer aggregate cross-row.
-- Trigger dispara AFTER INSERT OR UPDATE para cada statement que afeta a entry.
-- ---------------------------------------------------------------------------
CREATE TABLE ledger.postings (
    id               BIGSERIAL       NOT NULL,
    journal_entry_id BIGINT          NOT NULL,
    tenant_id        UUID            NOT NULL,
    account_id       BIGINT          NOT NULL,
    asset_code       TEXT            NOT NULL,   -- desnormalizado p/ queries sem JOIN
    -- scale: casas decimais do ativo no momento da postagem (do Asset Registry).
    -- Viaja junto para auditoria — divergência com Registry é erro detectável.
    scale            SMALLINT        NOT NULL    CHECK (scale >= 0),
    -- debit_amount e credit_amount em minor units (inteiros via NUMERIC inteiro-like).
    -- NUMERIC(40,18) = maior precision necessária (ERC-20 scale=18, sem float).
    -- Apenas UM dos dois deve ser > 0 por linha (o outro = 0).
    debit_amount     NUMERIC(40, 18) NOT NULL    DEFAULT 0 CHECK (debit_amount  >= 0),
    credit_amount    NUMERIC(40, 18) NOT NULL    DEFAULT 0 CHECK (credit_amount >= 0),
    -- Exatamente um dos dois deve ser positivo por posting
    -- (ambos = 0 é inválido; ambos > 0 é erro de modelo)
    posted_at        TIMESTAMPTZ     NOT NULL    DEFAULT now(),
    -- Partition key e chave de range
    CONSTRAINT postings_pk            PRIMARY KEY (id, posted_at),
    CONSTRAINT postings_entry_fk      FOREIGN KEY (journal_entry_id)
                                        REFERENCES ledger.journal_entries (id),
    CONSTRAINT postings_account_fk    FOREIGN KEY (account_id)
                                        REFERENCES ledger.accounts (id),
    CONSTRAINT postings_debit_credit_xor_chk
        CHECK (
            (debit_amount > 0 AND credit_amount = 0)
            OR
            (debit_amount = 0 AND credit_amount > 0)
        )
) PARTITION BY RANGE (posted_at);

-- Índices na tabela mãe (propagam para partições em PG16)
CREATE INDEX postings_entry_idx   ON ledger.postings (journal_entry_id);
CREATE INDEX postings_account_idx ON ledger.postings (account_id);
CREATE INDEX postings_tenant_idx  ON ledger.postings (tenant_id);
CREATE INDEX postings_asset_idx   ON ledger.postings (asset_code);
CREATE INDEX postings_posted_idx  ON ledger.postings (posted_at);

COMMENT ON TABLE  ledger.postings              IS 'Linhas de débito/crédito. Particionadas por posted_at (mensal). float PROIBIDO.';
COMMENT ON COLUMN ledger.postings.debit_amount  IS 'Débito em minor units. NUMERIC(40,18). float PROIBIDO.';
COMMENT ON COLUMN ledger.postings.credit_amount IS 'Crédito em minor units. NUMERIC(40,18). float PROIBIDO.';
COMMENT ON COLUMN ledger.postings.scale        IS 'Casas decimais do ativo (Asset Registry) no momento da postagem. Para auditoria.';

-- ---------------------------------------------------------------------------
-- Partições mensais iniciais (2026-01 a 2026-12 + catch-all futuro)
-- Novas partições são criadas via job mensal (pg_partman ou script externo).
-- ---------------------------------------------------------------------------
CREATE TABLE ledger.postings_2026_01 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');

CREATE TABLE ledger.postings_2026_02 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-02-01') TO ('2026-03-01');

CREATE TABLE ledger.postings_2026_03 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');

CREATE TABLE ledger.postings_2026_04 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');

CREATE TABLE ledger.postings_2026_05 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');

CREATE TABLE ledger.postings_2026_06 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE TABLE ledger.postings_2026_07 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

CREATE TABLE ledger.postings_2026_08 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');

CREATE TABLE ledger.postings_2026_09 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');

CREATE TABLE ledger.postings_2026_10 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');

CREATE TABLE ledger.postings_2026_11 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');

CREATE TABLE ledger.postings_2026_12 PARTITION OF ledger.postings
    FOR VALUES FROM ('2026-12-01') TO ('2027-01-01');

-- Partição futura (catch-all para datas além do previsioned)
CREATE TABLE ledger.postings_future PARTITION OF ledger.postings
    FOR VALUES FROM ('2027-01-01') TO (MAXVALUE);

-- ---------------------------------------------------------------------------
-- Constraint trigger: sum(debit_amount) = sum(credit_amount) por journal_entry
--
-- Por que CONSTRAINT TRIGGER e não CHECK?
-- CHECK de linha não pode fazer aggregate cross-row (é por row, não por entry).
-- Uma alternativa seria uma view materializada + check, mas isso quebraria
-- a atomicidade. O constraint trigger DEFERRED dispara ao final da transação,
-- depois que TODOS os postings da entry foram inseridos — garantia forte e
-- compatível com inserção em lote (INSERT dos dois lados do par na mesma TX).
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION ledger.check_entry_balance()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    v_entry_id   BIGINT;
    v_asset_code TEXT;
    v_debit_sum  NUMERIC(40, 18);
    v_credit_sum NUMERIC(40, 18);
BEGIN
    -- Identifica qual entry foi modificada
    IF TG_OP = 'DELETE' THEN
        v_entry_id   := OLD.journal_entry_id;
        v_asset_code := OLD.asset_code;
    ELSE
        v_entry_id   := NEW.journal_entry_id;
        v_asset_code := NEW.asset_code;
    END IF;

    -- Verifica balanço para CADA asset_code dentro da entry
    -- (uma entry multi-ativo = par de postings explícito de câmbio, ex. BRL→USDC)
    SELECT
        COALESCE(SUM(debit_amount),  0),
        COALESCE(SUM(credit_amount), 0)
    INTO v_debit_sum, v_credit_sum
    FROM ledger.postings
    WHERE journal_entry_id = v_entry_id
      AND asset_code = v_asset_code;

    -- Falha se débito e crédito não baterem (double-entry). O trigger é
    -- DEFERRABLE INITIALLY DEFERRED: dispara no COMMIT (sem estado intermediário),
    -- então ambos-zero (sem postings do ativo) passa, mas débito-só ou crédito-só
    -- (posting unilateral) dispara — fechando o backstop de balanço.
    IF v_debit_sum <> v_credit_sum THEN
        RAISE EXCEPTION
            'Ledger desbalanceado: entry_id=% asset=% debit=% credit=%',
            v_entry_id, v_asset_code, v_debit_sum, v_credit_sum;
    END IF;

    RETURN NULL; -- constraint trigger, retorno ignorado
END;
$$;

-- DEFERRABLE INITIALLY DEFERRED: dispara no COMMIT, não na linha —
-- permite inserir débito e crédito em qualquer ordem na mesma transação.
CREATE CONSTRAINT TRIGGER postings_balance_chk_trg
    AFTER INSERT OR UPDATE OR DELETE
    ON ledger.postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger.check_entry_balance();

COMMENT ON FUNCTION ledger.check_entry_balance() IS
    'Verifica sum(debit)=sum(credit) por journal_entry+asset_code. DEFERRABLE INITIALLY DEFERRED — dispara no COMMIT.';

-- ---------------------------------------------------------------------------
-- View auxiliar: saldo corrente por conta (derivado de postings)
-- Nunca armazenado em coluna — veja COMMENT abaixo.
-- ---------------------------------------------------------------------------
CREATE VIEW ledger.account_balances AS
SELECT
    a.id               AS account_id,
    a.tenant_id,
    a.code             AS account_code,
    a.name             AS account_name,
    a.kind,
    a.asset_code,
    -- Saldo = sum(débitos) - sum(créditos), em minor units NUMERIC
    COALESCE(SUM(p.debit_amount - p.credit_amount), 0) AS balance,
    COUNT(p.id)        AS posting_count
FROM ledger.accounts a
LEFT JOIN ledger.postings p ON p.account_id = a.id
GROUP BY a.id, a.tenant_id, a.code, a.name, a.kind, a.asset_code;

COMMENT ON VIEW ledger.account_balances IS
    'Saldo derivado de postings (não armazenado). Use para leitura; nunca faça UPDATE baseado nisso.';

-- ---------------------------------------------------------------------------
-- Trigger de updated_at para accounts
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION ledger.set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER accounts_updated_at_trg
    BEFORE UPDATE ON ledger.accounts
    FOR EACH ROW EXECUTE FUNCTION ledger.set_updated_at();
