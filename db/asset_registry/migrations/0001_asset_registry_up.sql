-- =============================================================================
-- 0001_asset_registry_up.sql
-- Asset Registry — fonte autoritativa de scale e metadados por ativo (TX-2/§2.6).
--
-- Ferramenta de migração: golang-migrate (github.com/golang-migrate/migrate)
-- Convenção de arquivo: {version}_{title}.up.sql / {version}_{title}.down.sql
-- DATABASE_URL: postgres://user:pass@host:5432/adserver?sslmode=require
--
-- float PROIBIDO: nenhuma coluna monetária aqui; scale e metadados apenas.
-- Sem conversão cambial automática (DA-10).
-- =============================================================================

CREATE SCHEMA IF NOT EXISTS asset_registry;

-- ---------------------------------------------------------------------------
-- Tabela principal
-- ---------------------------------------------------------------------------
CREATE TABLE asset_registry.assets (
    code              TEXT        NOT NULL,
    name              TEXT        NOT NULL,
    -- scale é NULL apenas enquanto ativo está enabled=false (AEV/BND-TBD).
    -- Um ativo NÃO PODE ser habilitado sem scale definido (ver CHECK abaixo).
    scale             SMALLINT,
    kind              TEXT        NOT NULL,
    chain_id          BIGINT,                          -- NULL para fiat e chain não-EVM/TBD
    contract_address  TEXT,                            -- NULL para fiat/off-chain/TBD
    custody_mode      TEXT        NOT NULL DEFAULT 'none',
    price_source      TEXT        NOT NULL DEFAULT 'none',
    -- Obrigatório quando price_source = 'administered' (mitiga conflito de interesse, §3 q.4)
    price_governance  TEXT,
    enabled           BOOLEAN     NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT assets_pk
        PRIMARY KEY (code),

    CONSTRAINT assets_kind_chk
        CHECK (kind IN ('fiat', 'stablecoin', 'crypto', 'token')),

    CONSTRAINT assets_custody_chk
        CHECK (custody_mode IN ('none', 'safe_multisig', 'mpc_fireblocks', 'client_supply')),

    CONSTRAINT assets_price_source_chk
        CHECK (price_source IN ('none', 'administered', 'oracle_chainlink', 'oracle_pyth')),

    CONSTRAINT assets_scale_non_negative_chk
        CHECK (scale IS NULL OR scale >= 0),

    -- Ativo habilitado EXIGE scale definido (sem scale não há aritmética correta)
    CONSTRAINT assets_enabled_needs_scale_chk
        CHECK (enabled = false OR scale IS NOT NULL),

    -- Preço administrado EXIGE governança explícita (§3 q.4)
    CONSTRAINT assets_administered_needs_governance_chk
        CHECK (price_source <> 'administered' OR price_governance IS NOT NULL)
);

COMMENT ON TABLE  asset_registry.assets              IS 'Fonte autoritativa de scale e metadados por ativo (TX-2/§2.6). float PROIBIDO.';
COMMENT ON COLUMN asset_registry.assets.code         IS 'Código do ativo (ex.: BRL, USDC). PK e FK lógica de Money.asset_code.';
COMMENT ON COLUMN asset_registry.assets.scale        IS 'Casas decimais autoritativas (>= 0). NULL apenas enquanto ativo disabled e TBD. NUNCA inferido.';
COMMENT ON COLUMN asset_registry.assets.kind         IS 'fiat | stablecoin | crypto | token.';
COMMENT ON COLUMN asset_registry.assets.chain_id     IS 'EVM chain id (1=Ethereum). NULL para fiat ou chain própria não-EVM/TBD.';
COMMENT ON COLUMN asset_registry.assets.contract_address IS 'Endereço do contrato on-chain. NULL para fiat/off-chain/TBD.';
COMMENT ON COLUMN asset_registry.assets.custody_mode IS 'none | safe_multisig | mpc_fireblocks | client_supply.';
COMMENT ON COLUMN asset_registry.assets.price_source IS 'none | administered | oracle_chainlink | oracle_pyth.';
COMMENT ON COLUMN asset_registry.assets.price_governance IS 'Quem define o preço administrado (papel/comitê). Obrigatório quando price_source=administered.';
COMMENT ON COLUMN asset_registry.assets.enabled      IS 'Ativo habilitado para uso operacional. false bloqueia uso em Money/ledger/billing.';

-- Trigger de updated_at
CREATE OR REPLACE FUNCTION asset_registry.assets_set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER assets_updated_at_trg
    BEFORE UPDATE ON asset_registry.assets
    FOR EACH ROW EXECUTE FUNCTION asset_registry.assets_set_updated_at();

-- ---------------------------------------------------------------------------
-- Seed — fonte: contracts/money/asset-registry.seed.csv
-- Ativos TBD (AEV/BND): scale=NULL, enabled=false — não bloqueiam Fase 0/1.
-- ---------------------------------------------------------------------------
INSERT INTO asset_registry.assets
    (code, name, scale, kind, chain_id, contract_address, custody_mode, price_source, price_governance, enabled)
VALUES
    ('BRL',   'Real Brasileiro',  2,    'fiat',        NULL, NULL,                                           'none',          'none',         NULL,                    true),
    ('USD',   'US Dollar',        2,    'fiat',        NULL, NULL,                                           'none',          'none',         NULL,                    true),
    ('EUR',   'Euro',             2,    'fiat',        NULL, NULL,                                           'none',          'none',         NULL,                    true),
    ('USDC',  'USD Coin',         6,    'stablecoin',  1,    '0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48',  'safe_multisig', 'none',         NULL,                    true),
    ('USDT',  'Tether USD',       6,    'stablecoin',  1,    '0xdAC17F958D2ee523a2206206994597C13D831ec7',  'safe_multisig', 'none',         NULL,                    true),
    ('ERC20', 'ERC-20 generico',  18,   'crypto',      1,    NULL,                                           'safe_multisig', 'none',         NULL,                    true),
    -- AEV e BND: scale=TBD, desabilitados — entram como linhas para Fase 3.
    -- Não bloqueiam Fase 0/1. scale será definido pelo Comitê de Tesouraria.
    ('AEV',   'Aevum',            NULL, 'token',       NULL, NULL,                                           'client_supply', 'administered', 'Comite de Tesouraria',  false),
    ('BND',   'Bond (Aevum)',     NULL, 'token',       NULL, NULL,                                           'client_supply', 'administered', 'Comite de Tesouraria',  false)
ON CONFLICT (code) DO NOTHING;
