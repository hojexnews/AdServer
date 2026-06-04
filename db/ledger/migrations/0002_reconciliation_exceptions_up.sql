-- =============================================================================
-- 0002_reconciliation_exceptions_up.sql
-- Tabela ledger.reconciliation_exceptions (BILLING.md §7.4 — "schema futuro, I3").
--
-- Antecipada no K3 porque o job de reconciliacao cripto ja produz excecoes.
-- Esta tabela e EXCLUSIVAMENTE para leitura/escrita pelo job de reconciliacao;
-- NUNCA e corrigida automaticamente (invariante §2.6 / BILLING.md §7 passo 5).
--
-- Regras inviolaveis:
--   - Divergencia -> abre excecao com status='open'. NUNCA autocorrige.
--   - Correcao exige posting de ajuste explicito aprovado por humano.
--   - Nenhuma coluna float (TX-2). Valores monetarios em NUMERIC(40,18).
--   - PII/KYC proibida (DA-11): apenas tenant_id pseudonimo.
--   - Fonte autoritativa: 'iceberg' (lakehouse). Nunca streaming/ClickHouse.
--
-- RLS: segue o padrao do schema config (FORCE ROW LEVEL SECURITY por tenant).
-- =============================================================================

-- ---------------------------------------------------------------------------
-- Enum de status de excecao
-- ---------------------------------------------------------------------------
CREATE TYPE ledger.exception_status AS ENUM (
    'open',      -- divergencia detectada, aguardando analise humana
    'resolved'   -- resolvida por posting de ajuste aprovado por humano
);

-- ---------------------------------------------------------------------------
-- Tabela principal
-- ---------------------------------------------------------------------------
CREATE TABLE ledger.reconciliation_exceptions (
    id                    BIGSERIAL           NOT NULL,

    -- Identificacao de contexto (sem PII — DA-11/TX-3)
    tenant_id             UUID                NOT NULL,
    asset_code            TEXT                NOT NULL,

    -- Periodo de reconciliacao (bucket horario alinhado ao StatsHourly)
    period_start          TIMESTAMPTZ         NOT NULL,
    period_end            TIMESTAMPTZ         NOT NULL,

    -- Valores comparados (NUMERIC, sem float — TX-2)
    -- expected_minor_units: valor calculado pela fonte autoritativa (Iceberg)
    -- ledger_minor_units:   valor apurado do ledger (SUM debit_amount)
    -- divergence_minor_units: expected - ledger (pode ser negativo)
    expected_minor_units  NUMERIC(40, 18)     NOT NULL,
    ledger_minor_units    NUMERIC(40, 18)     NOT NULL,
    divergence_minor_units NUMERIC(40, 18)    NOT NULL
        GENERATED ALWAYS AS (expected_minor_units - ledger_minor_units) STORED,

    -- Fonte autoritativa que produziu o valor esperado
    -- Valor unico valido hoje: 'iceberg' (BILLING.md §7 / ADR-0004 §B)
    -- Nunca 'clickhouse' ou qualquer fonte de streaming.
    source                TEXT                NOT NULL DEFAULT 'iceberg',

    -- Estado da excecao
    status                ledger.exception_status NOT NULL DEFAULT 'open',

    -- Auditoria
    created_at            TIMESTAMPTZ         NOT NULL DEFAULT now(),
    resolved_at           TIMESTAMPTZ,
    resolution_note       TEXT,

    -- Quem resolveu (papel/identidade do humano que aprovou o ajuste)
    -- Sem PII: apenas identificador de papel/sistema (ex.: 'desk-ops', 'batch-adj-2026-06-04')
    resolved_by           TEXT,

    -- Referencia ao journal_entry_id do posting de ajuste (quando resolvido)
    -- NULL enquanto status = 'open'
    adjustment_entry_id   BIGINT,

    CONSTRAINT reconciliation_exceptions_pk
        PRIMARY KEY (id),

    -- Uma excecao por (tenant, asset, periodo). Reabertura por periodo novo.
    CONSTRAINT reconciliation_exceptions_uq
        UNIQUE (tenant_id, asset_code, period_start, period_end),

    CONSTRAINT reconciliation_exceptions_source_chk
        CHECK (source IN ('iceberg')),

    CONSTRAINT reconciliation_exceptions_period_chk
        CHECK (period_end > period_start),

    CONSTRAINT reconciliation_exceptions_resolved_chk
        CHECK (
            status <> 'resolved'
            OR (resolved_at IS NOT NULL AND resolution_note IS NOT NULL)
        )
);

CREATE INDEX reconciliation_exceptions_tenant_idx
    ON ledger.reconciliation_exceptions (tenant_id);

CREATE INDEX reconciliation_exceptions_asset_idx
    ON ledger.reconciliation_exceptions (asset_code);

CREATE INDEX reconciliation_exceptions_period_idx
    ON ledger.reconciliation_exceptions (period_start, period_end);

CREATE INDEX reconciliation_exceptions_status_idx
    ON ledger.reconciliation_exceptions (status)
    WHERE status = 'open';

COMMENT ON TABLE ledger.reconciliation_exceptions IS
    'Excecoes de reconciliacao periodica (BILLING.md §7). NUNCA autocorrigida. '
    'Correcao exige posting de ajuste humano. Fonte: iceberg (lakehouse), nunca streaming.';

COMMENT ON COLUMN ledger.reconciliation_exceptions.expected_minor_units IS
    'Valor esperado calculado pela fonte autoritativa (Iceberg). NUMERIC(40,18). float PROIBIDO.';

COMMENT ON COLUMN ledger.reconciliation_exceptions.ledger_minor_units IS
    'Valor apurado do ledger (SUM debit_amount de postings). NUMERIC(40,18). float PROIBIDO.';

COMMENT ON COLUMN ledger.reconciliation_exceptions.divergence_minor_units IS
    'expected - ledger. Coluna gerada (STORED). Negativo = ledger maior que esperado.';

COMMENT ON COLUMN ledger.reconciliation_exceptions.source IS
    'Fonte autoritativa do valor esperado. Somente ''iceberg'' e valido (DA-7/BILLING.md §7).';

COMMENT ON COLUMN ledger.reconciliation_exceptions.status IS
    'open: aguardando analise; resolved: resolvida por posting de ajuste humano.';

-- ---------------------------------------------------------------------------
-- RLS — Row Level Security por tenant_id (mesmo padrao do schema config)
-- ---------------------------------------------------------------------------
ALTER TABLE ledger.reconciliation_exceptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger.reconciliation_exceptions FORCE ROW LEVEL SECURITY;

-- Politica: cada role so ve registros do proprio tenant.
-- O role adserver_app precisa ter a variavel de sessao app.current_tenant_id
-- definida antes de qualquer query (mesma convencao do schema config).
CREATE POLICY reconciliation_exceptions_tenant_policy
    ON ledger.reconciliation_exceptions
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- Role de servico (adserver_loader/admin) bypassa RLS para reconciliacao batch.
-- O BYPASSRLS e concedido ao role adserver_loader (mesmo padrao do config).
