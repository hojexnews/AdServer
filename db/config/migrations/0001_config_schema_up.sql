-- =============================================================================
-- 0001_config_schema_up.sql
-- Schema de configuração — lido pelo snapshot do motor de decisão (§4.1).
--
-- Ferramenta de migração: golang-migrate
-- float PROIBIDO em qualquer coluna monetária (CA-7/TX-2/DA-10).
-- Colunas rate/budget usam NUMERIC(20,2) como mínimo; scale real vem do
-- Asset Registry em runtime — ver comentário em campaign.rate.
--
-- RLS por tenant_id: políticas definidas em 0003_config_rls_up.sql após
-- as tabelas existirem. Documentado no README.
-- =============================================================================

CREATE SCHEMA IF NOT EXISTS config;

-- ---------------------------------------------------------------------------
-- Tipo ENUM auxiliar (evita string mágica dispersa)
-- ---------------------------------------------------------------------------
CREATE TYPE config.campaign_type      AS ENUM ('override', 'contract', 'remnant');
CREATE TYPE config.goal_metric        AS ENUM ('impressions', 'clicks', 'conversions');
CREATE TYPE config.pricing_model      AS ENUM ('cpm', 'cpc', 'cpa', 'tenancy');
CREATE TYPE config.creative_type      AS ENUM ('image', 'html5', 'thirdparty_tag', 'video');
CREATE TYPE config.owner_entity       AS ENUM ('campaign', 'banner');
CREATE TYPE config.logical_op         AS ENUM ('AND', 'OR');
CREATE TYPE config.cap_scope          AS ENUM ('campaign_total', 'session', 'clock');

-- ---------------------------------------------------------------------------
-- Advertiser (DA-1 / CA-1)
-- Credenciais de login separadas por anunciante (multi-tenant).
-- tenant_id = anunciante isolado; is_network = true para ad networks.
-- ---------------------------------------------------------------------------
CREATE TABLE config.advertisers (
    id                  BIGSERIAL   NOT NULL,
    tenant_id           UUID        NOT NULL,           -- RLS: isolamento por tenant
    name                TEXT        NOT NULL,
    -- Credenciais de acesso ao painel (hash bcrypt, não plaintext).
    -- PII mínimo: apenas username/hash; sem CPF/dados pessoais (DA-11/TX-3).
    login_username      TEXT        NOT NULL UNIQUE,
    login_password_hash TEXT        NOT NULL,
    is_network          BOOLEAN     NOT NULL DEFAULT false,
    active              BOOLEAN     NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT advertisers_pk PRIMARY KEY (id)
);

CREATE INDEX advertisers_tenant_idx ON config.advertisers (tenant_id);

COMMENT ON TABLE  config.advertisers              IS 'Anunciantes (demanda). Multi-tenant via tenant_id (TX-3).';
COMMENT ON COLUMN config.advertisers.tenant_id    IS 'Isolamento RLS: cada anunciante vê apenas seus dados.';
COMMENT ON COLUMN config.advertisers.is_network   IS 'true = ad network; false = cliente direto.';

-- ---------------------------------------------------------------------------
-- Campaign (DA-1/DA-3/DA-4 / §4.2)
-- rate: NUMERIC(20,2) é o lower bound para fiat (BRL/USD/EUR scale=2).
-- Para USDC/USDT (scale=6) ou ERC-20 (scale=18) o sistema cria colunas
-- específicas — ver constraint abaixo e README.
-- currency é RÓTULO (DA-10): sem conversão automática.
-- ---------------------------------------------------------------------------
CREATE TABLE config.campaigns (
    id              BIGSERIAL              NOT NULL,
    tenant_id       UUID                   NOT NULL,
    advertiser_id   BIGINT                 NOT NULL,
    name            TEXT                   NOT NULL,
    type            config.campaign_type   NOT NULL,
    priority        SMALLINT               NOT NULL DEFAULT 5
                        CHECK (priority BETWEEN 1 AND 10),
    goal_target     BIGINT,                           -- NULL para tenancy (sem meta de volume)
    goal_metric     config.goal_metric,               -- NULL para tenancy
    start_at        TIMESTAMPTZ            NOT NULL,
    end_at          TIMESTAMPTZ            NOT NULL,
    pricing_model   config.pricing_model   NOT NULL,
    -- rate: valor da taxa de precificação em minor units inteiros do ativo.
    -- NUMERIC(26,6) cobre BRL (scale=2), USD (scale=2), USDC/USDT (scale=6).
    -- ERC-20 (scale=18) requer NUMERIC(40,18) — adicionado via ALTER em 0002.
    -- float PROIBIDO (CA-7/TX-2). Scale autoritativo no Asset Registry.
    rate            NUMERIC(26, 6)         NOT NULL    DEFAULT 0
                        CHECK (rate >= 0),
    -- currency é apenas um rótulo textual — sem conversão automática (DA-10).
    currency        TEXT                   NOT NULL    DEFAULT 'BRL',
    active          BOOLEAN                NOT NULL    DEFAULT true,
    created_at      TIMESTAMPTZ            NOT NULL    DEFAULT now(),
    updated_at      TIMESTAMPTZ            NOT NULL    DEFAULT now(),

    CONSTRAINT campaigns_pk              PRIMARY KEY (id),
    CONSTRAINT campaigns_advertiser_fk   FOREIGN KEY (advertiser_id) REFERENCES config.advertisers (id),
    CONSTRAINT campaigns_dates_chk       CHECK (end_at > start_at),
    -- Tenancy não tem meta de volume; outros tipos exigem goal_target + goal_metric.
    CONSTRAINT campaigns_goal_chk
        CHECK (
            (pricing_model = 'tenancy' AND goal_target IS NULL AND goal_metric IS NULL)
            OR
            (pricing_model <> 'tenancy' AND goal_target IS NOT NULL AND goal_metric IS NOT NULL)
        )
);

CREATE INDEX campaigns_tenant_idx       ON config.campaigns (tenant_id);
CREATE INDEX campaigns_advertiser_idx   ON config.campaigns (advertiser_id);
CREATE INDEX campaigns_active_idx       ON config.campaigns (active, start_at, end_at);

COMMENT ON TABLE  config.campaigns            IS 'Campanhas de anúncio. Tipo+prioridade formam a cascata de decisão (DA-3).';
COMMENT ON COLUMN config.campaigns.rate       IS 'Taxa de precificação em NUMERIC; float PROIBIDO (CA-7/TX-2). Scale do Asset Registry.';
COMMENT ON COLUMN config.campaigns.currency   IS 'Rótulo de moeda — sem conversão automática (DA-10).';
COMMENT ON COLUMN config.campaigns.priority   IS '1 (maior) a 10 (menor). Desempate dentro do mesmo type.';

-- ---------------------------------------------------------------------------
-- Banner / Criativo (DA-1 / §4.3)
-- ---------------------------------------------------------------------------
CREATE TABLE config.banners (
    id              BIGSERIAL             NOT NULL,
    tenant_id       UUID                  NOT NULL,
    campaign_id     BIGINT                NOT NULL,
    name            TEXT                  NOT NULL,
    creative_type   config.creative_type  NOT NULL,
    -- asset_url: URL externa ou path interno do asset de mídia.
    -- asset_blob: armazenamento inline de arquivos pequenos (image < 2 MB).
    -- Exatamente um dos dois deve ser preenchido.
    asset_url       TEXT,
    asset_blob      BYTEA,
    dest_url        TEXT,                             -- obrigatório para image/html5/video
    width           INTEGER               NOT NULL    CHECK (width  > 0),
    height          INTEGER               NOT NULL    CHECK (height > 0),
    active          BOOLEAN               NOT NULL    DEFAULT true,
    created_at      TIMESTAMPTZ           NOT NULL    DEFAULT now(),
    updated_at      TIMESTAMPTZ           NOT NULL    DEFAULT now(),

    CONSTRAINT banners_pk            PRIMARY KEY (id),
    CONSTRAINT banners_campaign_fk   FOREIGN KEY (campaign_id) REFERENCES config.campaigns (id),
    -- image, html5 e video exigem dest_url; thirdparty_tag dispensa.
    CONSTRAINT banners_dest_url_chk
        CHECK (
            creative_type = 'thirdparty_tag'
            OR dest_url IS NOT NULL
        ),
    -- Exatamente uma das representações de asset deve estar presente.
    CONSTRAINT banners_asset_xor_chk
        CHECK (
            (asset_url IS NOT NULL AND asset_blob IS NULL)
            OR
            (asset_url IS NULL AND asset_blob IS NOT NULL)
        )
);

CREATE INDEX banners_campaign_idx ON config.banners (campaign_id);
CREATE INDEX banners_tenant_idx   ON config.banners (tenant_id);

COMMENT ON TABLE config.banners IS 'Criativos vinculados a campanhas. Tipos: image, html5, thirdparty_tag, video (§4.3).';

-- ---------------------------------------------------------------------------
-- Site (oferta — DA-1)
-- ---------------------------------------------------------------------------
CREATE TABLE config.sites (
    id          BIGSERIAL   NOT NULL,
    tenant_id   UUID        NOT NULL,
    name        TEXT        NOT NULL,
    url         TEXT        NOT NULL,
    active      BOOLEAN     NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT sites_pk PRIMARY KEY (id)
);

CREATE INDEX sites_tenant_idx ON config.sites (tenant_id);

COMMENT ON TABLE config.sites IS 'Sites (oferta). Hemisferio de supply (DA-1).';

-- ---------------------------------------------------------------------------
-- Zone (oferta — DA-1 / §4.1)
-- ---------------------------------------------------------------------------
CREATE TABLE config.zones (
    id          BIGSERIAL   NOT NULL,
    tenant_id   UUID        NOT NULL,
    site_id     BIGINT      NOT NULL,
    name        TEXT        NOT NULL,
    width       INTEGER     NOT NULL CHECK (width  > 0),
    height      INTEGER     NOT NULL CHECK (height > 0),
    type        TEXT        NOT NULL DEFAULT 'standard',
    active      BOOLEAN     NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT zones_pk      PRIMARY KEY (id),
    CONSTRAINT zones_site_fk FOREIGN KEY (site_id) REFERENCES config.sites (id)
);

CREATE INDEX zones_site_idx   ON config.zones (site_id);
CREATE INDEX zones_tenant_idx ON config.zones (tenant_id);

COMMENT ON TABLE config.zones IS 'Zonas de inventário. Cada zona pertence a um site (DA-1).';

-- ---------------------------------------------------------------------------
-- CampaignZone — vínculo N:N campanha↔zona (DA-2)
-- ---------------------------------------------------------------------------
CREATE TABLE config.campaign_zones (
    campaign_id BIGINT      NOT NULL,
    zone_id     BIGINT      NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT campaign_zones_pk          PRIMARY KEY (campaign_id, zone_id),
    CONSTRAINT campaign_zones_campaign_fk FOREIGN KEY (campaign_id) REFERENCES config.campaigns (id),
    CONSTRAINT campaign_zones_zone_fk     FOREIGN KEY (zone_id)     REFERENCES config.zones (id)
);

COMMENT ON TABLE config.campaign_zones IS 'Vinculo N:N campanha-zona. Avaliado dinamicamente por request (DA-2).';

-- ---------------------------------------------------------------------------
-- DeliveryRuleSet — conjunto nomeado e reutilizável (§4.6)
-- ---------------------------------------------------------------------------
CREATE TABLE config.delivery_rule_sets (
    id          BIGSERIAL   NOT NULL,
    tenant_id   UUID        NOT NULL,
    name        TEXT        NOT NULL,
    description TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT delivery_rule_sets_pk PRIMARY KEY (id)
);

CREATE INDEX delivery_rule_sets_tenant_idx ON config.delivery_rule_sets (tenant_id);

COMMENT ON TABLE config.delivery_rule_sets IS 'Conjuntos reutilizáveis de regras de entrega (§4.6). Encapsulam combinações AND/OR.';

-- ---------------------------------------------------------------------------
-- DeliveryRule — segmentação granular (§4.6)
-- Uma regra pertence a uma campanha/banner OU a um rule_set (não ambos).
-- ---------------------------------------------------------------------------
CREATE TABLE config.delivery_rules (
    id          BIGSERIAL            NOT NULL,
    tenant_id   UUID                 NOT NULL,
    owner_type  config.owner_entity  NOT NULL,   -- 'campaign' ou 'banner'
    owner_id    BIGINT               NOT NULL,   -- FK polimórfica por owner_type
    -- vector: Time - Day of Week | Site - URL | Geo - Country | Geo - City |
    --         Client - Useragent | Site - Variable
    vector      TEXT                 NOT NULL,
    -- operator: is | is not | contains | does not contain | starts with | ends with
    operator    TEXT                 NOT NULL,
    value       TEXT                 NOT NULL,
    logical_op  config.logical_op    NOT NULL DEFAULT 'AND',
    -- rule_set_id: NULL = regra direta; NOT NULL = pertence ao rule set
    rule_set_id BIGINT,
    -- order_seq: ordem de avaliação dentro do conjunto de regras do owner
    order_seq   INTEGER              NOT NULL DEFAULT 0,
    active      BOOLEAN              NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ          NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ          NOT NULL DEFAULT now(),

    CONSTRAINT delivery_rules_pk          PRIMARY KEY (id),
    CONSTRAINT delivery_rules_ruleset_fk
        FOREIGN KEY (rule_set_id) REFERENCES config.delivery_rule_sets (id),
    -- Regra direta XOR pertence a rule_set (não ambos)
    CONSTRAINT delivery_rules_owner_xor_ruleset_chk
        CHECK (
            (rule_set_id IS NULL AND owner_type IS NOT NULL AND owner_id IS NOT NULL)
            OR
            (rule_set_id IS NOT NULL)
        ),
    CONSTRAINT delivery_rules_vector_chk
        CHECK (vector IN (
            'Time - Day of Week',
            'Site - URL',
            'Geo - Country',
            'Geo - City',
            'Client - Useragent',
            'Site - Variable'
        )),
    CONSTRAINT delivery_rules_operator_chk
        CHECK (operator IN ('is', 'is not', 'contains', 'does not contain', 'starts with', 'ends with'))
);

CREATE INDEX delivery_rules_owner_idx    ON config.delivery_rules (owner_type, owner_id);
CREATE INDEX delivery_rules_ruleset_idx  ON config.delivery_rules (rule_set_id);
CREATE INDEX delivery_rules_tenant_idx   ON config.delivery_rules (tenant_id);

COMMENT ON TABLE config.delivery_rules IS 'Regras de entrega por vetor (§4.6). FK polimórfica por owner_type+owner_id.';

-- ---------------------------------------------------------------------------
-- Cap — frequency capping (§4.8 / DA-6)
-- ---------------------------------------------------------------------------
CREATE TABLE config.caps (
    id              BIGSERIAL          NOT NULL,
    tenant_id       UUID               NOT NULL,
    owner_type      config.owner_entity NOT NULL,
    owner_id        BIGINT             NOT NULL,
    scope           config.cap_scope   NOT NULL,
    -- limit_count: teto de exibições (sem float — contagem inteira)
    limit_count     BIGINT             NOT NULL CHECK (limit_count > 0),
    -- reset_interval: duração em segundos para scope=clock (NULL para outros)
    reset_interval  INTEGER,
    active          BOOLEAN            NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ        NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ        NOT NULL DEFAULT now(),

    CONSTRAINT caps_pk                  PRIMARY KEY (id),
    CONSTRAINT caps_clock_interval_chk
        CHECK (
            (scope = 'clock' AND reset_interval IS NOT NULL AND reset_interval > 0)
            OR
            (scope <> 'clock' AND reset_interval IS NULL)
        )
);

CREATE INDEX caps_owner_idx  ON config.caps (owner_type, owner_id);
CREATE INDEX caps_tenant_idx ON config.caps (tenant_id);

COMMENT ON TABLE config.caps IS 'Frequency capping por scope: campaign_total | session | clock (§4.8/DA-6).';

-- ---------------------------------------------------------------------------
-- Triggers de updated_at — todos os objetos mutáveis
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION config.set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER advertisers_updated_at_trg
    BEFORE UPDATE ON config.advertisers
    FOR EACH ROW EXECUTE FUNCTION config.set_updated_at();

CREATE TRIGGER campaigns_updated_at_trg
    BEFORE UPDATE ON config.campaigns
    FOR EACH ROW EXECUTE FUNCTION config.set_updated_at();

CREATE TRIGGER banners_updated_at_trg
    BEFORE UPDATE ON config.banners
    FOR EACH ROW EXECUTE FUNCTION config.set_updated_at();

CREATE TRIGGER sites_updated_at_trg
    BEFORE UPDATE ON config.sites
    FOR EACH ROW EXECUTE FUNCTION config.set_updated_at();

CREATE TRIGGER zones_updated_at_trg
    BEFORE UPDATE ON config.zones
    FOR EACH ROW EXECUTE FUNCTION config.set_updated_at();

CREATE TRIGGER delivery_rule_sets_updated_at_trg
    BEFORE UPDATE ON config.delivery_rule_sets
    FOR EACH ROW EXECUTE FUNCTION config.set_updated_at();

CREATE TRIGGER delivery_rules_updated_at_trg
    BEFORE UPDATE ON config.delivery_rules
    FOR EACH ROW EXECUTE FUNCTION config.set_updated_at();

CREATE TRIGGER caps_updated_at_trg
    BEFORE UPDATE ON config.caps
    FOR EACH ROW EXECUTE FUNCTION config.set_updated_at();
