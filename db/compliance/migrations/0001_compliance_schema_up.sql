-- =============================================================================
-- 0001_compliance_schema_up.sql
-- Cofre de compliance: schema PII/KYC por tenant (K6 — TX-3/DA-11/E.10).
--
-- POSICIONAMENTO DE SEGURANÇA
--   Este schema e o cofre de compliance da celula AML/KYC (ADR-0004 §F).
--   Toda PII/KYC fica AQUI, referenciada por tenant_id pseudonimo (UUID).
--   O ledger (db/ledger), a telemetria e o proto de eventos NAO recebem PII.
--
-- RLS (Row-Level Security)
--   Identico ao padrao canonico de db/config/migrations/0002_config_rls_up.sql:
--     - Funcao auxiliar compliance.current_tenant_id() le adserver.tenant_id
--       da sessao via SET LOCAL. Retorna NULL se ausente — fail-closed.
--     - ENABLE + FORCE ROW LEVEL SECURITY em cada tabela.
--     - Policy USING (tenant_id = compliance.current_tenant_id()) em cada tabela.
--     - O superusuario (migrations/admin) bypassa por padrao — documentado.
--     - O usuario de aplicacao (adserver_app) e um role nao-superuser.
--
-- INVARIANTES (gate privacy-compliance-auditor)
--   TX-3: PII isolada no cofre, referenciada por pseudonimo.
--   TX-5/DA-11: ledger e telemetria sem PII.
--   TX-2: NENHUMA coluna float. Valores monetarios em NUMERIC.
--
-- CIFRA EM REPOUSO (envelope KMS)
--   Colunas marcadas com o comentario 'PII — cifrar em repouso (KMS envelope)'
--   sao aptas a cifra simetrica via funcao de envelope KMS (ex.: pgcrypto
--   ou extensao de KMS externo). A cifra e aplicada na camada de aplicacao
--   (services/payments/internal/sumsub) antes do INSERT. O schema aceita
--   BYTEA ou TEXT cifrado — a coluna atual e TEXT para compatibilidade com
--   provedores KMS que retornam base64. Quando a cifra KMS for habilitada,
--   alterar o tipo para BYTEA (sem migracao de dados: so o KMS muda o envelope).
--   O cofre NAO armazena PII em claro em producao — este comentario e o
--   marcador que o auditor de privacidade usa para rastrear quais colunas
--   exigem cifra antes de promover para producao.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- Schema
-- ---------------------------------------------------------------------------
CREATE SCHEMA IF NOT EXISTS compliance;

COMMENT ON SCHEMA compliance IS
    'Cofre de compliance: PII/KYC pseudonimizada por tenant. Celula AML/KYC. '
    'RLS por adserver.tenant_id. Nunca expor este schema ao ledger ou telemetria.';

-- ---------------------------------------------------------------------------
-- Funcao auxiliar: le tenant_id da sessao corrente.
-- Identica ao padrao de config.current_tenant_id() (0002_config_rls_up.sql).
-- Retorna NULL se ausente -> policy rejeita toda linha (fail-closed).
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION compliance.current_tenant_id()
RETURNS UUID LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT NULLIF(current_setting('adserver.tenant_id', true), '')::UUID
$$;

COMMENT ON FUNCTION compliance.current_tenant_id() IS
    'Le tenant_id da sessao via SET LOCAL adserver.tenant_id. '
    'Retorna NULL se ausente — rejeita acesso (fail-closed). '
    'Padrao canonico: identico a config.current_tenant_id().';

-- ---------------------------------------------------------------------------
-- Tabela: compliance.kyc_subjects
--
-- Sujeito de KYC/KYB por tenant. A identidade real fica no Sumsub;
-- aqui ficam apenas referencias + status + PII minima cifravel.
--
-- PII minima: a coluna full_name e OPCIONAL e deve ser cifrada em repouso
-- via envelope KMS antes de promover para producao (marcador abaixo).
-- Preferencia: NAO armazenar — deixar o Sumsub como fonte de verdade
-- e consultar via API quando necessario.
-- ---------------------------------------------------------------------------
CREATE TABLE compliance.kyc_subjects (
    -- Chave primaria sintetica
    id                  BIGSERIAL PRIMARY KEY,

    -- Identificadores pseudonimos (TX-3 / DA-11)
    -- tenant_id: UUID pseudonimo do tenant — NUNCA o nome/CNPJ do cliente.
    tenant_id           UUID NOT NULL,
    -- subject_ref: identificador pseudonimo do sujeito dentro do tenant.
    -- Pode ser o advertiser_id (UUID) do config.advertisers — nunca CPF/email.
    subject_ref         UUID NOT NULL,

    -- Referencia ao Sumsub (fonte de verdade de documentos e PII)
    -- sumsub_applicant_id: ID do applicant no Sumsub. A PII de documentos
    -- (nome, CPF, passaporte, selfie) fica EXCLUSIVAMENTE no Sumsub.
    -- Nunca armazenar documento, numero de CPF ou foto aqui.
    sumsub_applicant_id TEXT NOT NULL,

    -- Nivel de verificacao: 'kyc' (pessoa fisica) ou 'kyb' (pessoa juridica).
    level               TEXT NOT NULL CHECK (level IN ('kyc', 'kyb')),

    -- Status do processo de verificacao (atualizado via webhook Sumsub).
    -- pending   — applicant criado, aguardando envio de documentos.
    -- approved  — verificacao aprovada.
    -- rejected  — verificacao reprovada (ver rejection_reason).
    -- on_hold   — em revisao manual.
    status              TEXT NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'approved', 'rejected', 'on_hold')),

    -- Motivo da rejeicao (quando status = 'rejected').
    -- NAO contem PII — apenas o codigo/categoria de rejeicao do Sumsub.
    rejection_reason    TEXT,

    -- PII MINIMA OPCIONAL — cifrar em repouso (KMS envelope)
    -- full_name: nome do sujeito. Armazenar SOMENTE se exigido por regulacao
    -- local (ex.: Travel Rule BACEN exige nome do originador).
    -- Em producao: cifrar via envelope KMS antes do INSERT.
    -- Em dev/staging: NULL (nao armazenar).
    -- Marcador para o auditor: 'PII — cifrar em repouso (KMS envelope)'
    full_name           TEXT,   -- PII — cifrar em repouso (KMS envelope)

    -- Idempotencia: um subject_ref nao pode ter dois applicants ativos
    -- para o mesmo nivel no mesmo tenant.
    -- Unicidade parcial: (tenant_id, subject_ref, level) unico.

    -- Auditoria
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Constrainte de unicidade: (tenant, sujeito, nivel) — um applicant por nivel.
    CONSTRAINT kyc_subjects_tenant_subject_level_uq UNIQUE (tenant_id, subject_ref, level)
);

COMMENT ON TABLE compliance.kyc_subjects IS
    'Sujeito de KYC/KYB por tenant. PII de documentos fica no Sumsub. '
    'Armazena apenas referencias pseudonimas + status. RLS por adserver.tenant_id.';
COMMENT ON COLUMN compliance.kyc_subjects.tenant_id IS
    'UUID pseudonimo do tenant. Nunca o nome/CNPJ real. TX-3.';
COMMENT ON COLUMN compliance.kyc_subjects.subject_ref IS
    'UUID pseudonimo do sujeito (ex.: advertiser_id). Nunca CPF/email. TX-3.';
COMMENT ON COLUMN compliance.kyc_subjects.sumsub_applicant_id IS
    'ID do applicant no Sumsub. A PII real (CPF, passaporte, selfie) fica so no Sumsub.';
COMMENT ON COLUMN compliance.kyc_subjects.full_name IS
    'PII MINIMA — cifrar em repouso via envelope KMS antes de producao. '
    'Armazenar SOMENTE se exigido por regulacao. Preferencia: NULL (Sumsub e a fonte).';

CREATE INDEX kyc_subjects_tenant_id_idx ON compliance.kyc_subjects (tenant_id);
CREATE INDEX kyc_subjects_sumsub_applicant_id_idx ON compliance.kyc_subjects (sumsub_applicant_id);

-- RLS: kyc_subjects
ALTER TABLE compliance.kyc_subjects ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance.kyc_subjects FORCE  ROW LEVEL SECURITY;

CREATE POLICY kyc_subjects_tenant_isolation ON compliance.kyc_subjects
    USING (tenant_id = compliance.current_tenant_id());

-- ---------------------------------------------------------------------------
-- Tabela: compliance.screening_results
--
-- Resultado de screening Chainalysis de endereco on-chain.
-- Enderecos de custodia da plataforma (Safe) nao sao PII.
-- Enderecos de contraparte do anunciante podem ser sensiveis — RLS protege.
-- NAO armazena nome, identidade ou qualquer PII do titular do endereco.
-- ---------------------------------------------------------------------------
CREATE TABLE compliance.screening_results (
    id                  BIGSERIAL PRIMARY KEY,

    -- Identificadores pseudonimos
    tenant_id           UUID NOT NULL,

    -- Endereco on-chain screened (EVM checksummed ou lowercase).
    -- NAO e PII de anunciante — e um endereco de blockchain.
    -- A associacao endereco <-> identidade fica EXCLUSIVAMENTE no cofre
    -- kyc_subjects (via subject_ref), nunca nesta tabela.
    address             TEXT NOT NULL,

    -- Direcao: 'deposit' (origem de deposito) ou 'payout' (destino de payout).
    direction           TEXT NOT NULL CHECK (direction IN ('deposit', 'payout')),

    -- Resultado do screening Chainalysis
    -- risk_score: escala inteira 0-100 (TX-2: sem float/NUMERIC com casas decimais).
    -- 0 = risco minimo; 100 = risco maximo (sanctioned/severe).
    -- Mapeamento: low=10, medium=50, high=80, severe=100.
    risk_score          INTEGER,             -- score de risco 0-100, sem float (TX-2)

    -- sanctions_hit: TRUE se o Chainalysis retornou match de sancoes OFAC/ONU.
    sanctions_hit       BOOLEAN NOT NULL DEFAULT false,

    -- category: categoria de risco retornada pelo Chainalysis
    -- (ex.: 'sanctions', 'darknet_market', 'scam', 'none').
    category            TEXT,

    -- tx_ref: referencia a transacao que gerou o screening (hash on-chain ou
    -- idempotency_key do payout). Usado para correlacao de auditoria.
    tx_ref              TEXT,

    -- Auditoria
    screened_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Idempotencia: (tenant_id, address, direction, tx_ref) unico por screening.
    CONSTRAINT screening_results_idem_uq
        UNIQUE (tenant_id, address, direction, tx_ref)
);

COMMENT ON TABLE compliance.screening_results IS
    'Resultado de screening Chainalysis por endereco on-chain. '
    'NAO armazena PII de identidade. RLS por adserver.tenant_id. '
    'sanctions_hit=TRUE bloqueia o pagamento — nunca toca a decisao de veiculacao.';
COMMENT ON COLUMN compliance.screening_results.risk_score IS
    'Score de risco Chainalysis INTEGER 0-100 (TX-2: sem float). low=10, medium=50, high=80, severe=100.';
COMMENT ON COLUMN compliance.screening_results.sanctions_hit IS
    'TRUE = match de sancoes OFAC/ONU. Bloqueia deposito ou payout imediatamente.';

CREATE INDEX screening_results_tenant_id_idx ON compliance.screening_results (tenant_id);
CREATE INDEX screening_results_address_direction_idx
    ON compliance.screening_results (address, direction);

-- RLS: screening_results
ALTER TABLE compliance.screening_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance.screening_results FORCE  ROW LEVEL SECURITY;

CREATE POLICY screening_results_tenant_isolation ON compliance.screening_results
    USING (tenant_id = compliance.current_tenant_id());

-- ---------------------------------------------------------------------------
-- Tabela: compliance.travel_rule_records
--
-- Travel Rule por transferencia cripto acima do limiar.
-- Armazena dados minimos exigidos (originador/beneficiario VASP + tx_ref).
-- PII do originador/beneficiario e tratada como dado sensivel do cofre —
-- nunca no ledger, nunca em logs/telemetria.
--
-- TX-2: amount_minor_units em NUMERIC (nunca float).
-- ---------------------------------------------------------------------------
CREATE TABLE compliance.travel_rule_records (
    id                      BIGSERIAL PRIMARY KEY,

    -- Identificadores pseudonimos
    tenant_id               UUID NOT NULL,

    -- Referencia a transacao on-chain ou idempotency_key do payout.
    -- Usado para correlacao com o ledger (sem PII).
    tx_ref                  TEXT NOT NULL,

    -- VASP de origem e destino (codigo/identificador do provedor)
    -- originator_vasp: ex. "SAFE_MULTISIG_PLATFORM" ou LEI/BIC do VASP.
    -- beneficiary_vasp: ex. "EXCHANGE_XYZ" ou LEI/BIC do VASP receptor.
    originator_vasp         TEXT NOT NULL,
    beneficiary_vasp        TEXT NOT NULL,

    -- Dados minimos de Travel Rule (FATF Recommendation 16)
    -- PII — cifrar em repouso (KMS envelope)
    -- originator_name: nome do originador. PII — cifrar antes de INSERT em producao.
    originator_name         TEXT,   -- PII — cifrar em repouso (KMS envelope)
    -- beneficiary_name: nome do beneficiario. PII — cifrar antes de INSERT em producao.
    beneficiary_name        TEXT,   -- PII — cifrar em repouso (KMS envelope)

    -- Valor da transferencia (TX-2: NUMERIC, nunca float)
    -- threshold_currency: codigo do ativo (ex.: 'USDC', 'BRL').
    threshold_currency      TEXT NOT NULL,
    -- amount_minor_units: valor em minor-units do ativo. NUMERIC sem casas decimais
    -- para evitar qualquer arredondamento. O scale e definido pelo Asset Registry.
    -- TX-2: usar NUMERIC sem float. INT64 equivalente em SQL = NUMERIC(20,0).
    amount_minor_units      NUMERIC(20,0) NOT NULL
                                CHECK (amount_minor_units >= 0),

    -- Status do registro de Travel Rule
    -- pending    — criado, aguardando envio ao provedor de Travel Rule.
    -- sent       — enviado ao provedor (ex.: Notabene/Sygna).
    -- confirmed  — confirmado pelo VASP receptor.
    -- failed     — falha no envio (bloqueia o payout ate resolucao).
    status                  TEXT NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending', 'sent', 'confirmed', 'failed')),

    -- Provedor de Travel Rule utilizado (ex.: 'notabene', 'sygna', 'stub').
    provider                TEXT NOT NULL DEFAULT 'stub',

    -- Auditoria
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Idempotencia: (tenant_id, tx_ref) unico — uma entrada por transferencia.
    CONSTRAINT travel_rule_records_idem_uq UNIQUE (tenant_id, tx_ref)
);

COMMENT ON TABLE compliance.travel_rule_records IS
    'Travel Rule por transferencia cripto acima do limiar. '
    'PII de originador/beneficiario cifrada em repouso via KMS envelope. '
    'RLS por adserver.tenant_id. status=failed bloqueia o payout.';
COMMENT ON COLUMN compliance.travel_rule_records.amount_minor_units IS
    'Valor em minor-units NUMERIC(20,0). TX-2: nunca float.';
COMMENT ON COLUMN compliance.travel_rule_records.originator_name IS
    'PII — cifrar em repouso (KMS envelope) antes de producao. FATF Rec. 16.';
COMMENT ON COLUMN compliance.travel_rule_records.beneficiary_name IS
    'PII — cifrar em repouso (KMS envelope) antes de producao. FATF Rec. 16.';

CREATE INDEX travel_rule_records_tenant_id_idx ON compliance.travel_rule_records (tenant_id);
CREATE INDEX travel_rule_records_tx_ref_idx ON compliance.travel_rule_records (tx_ref);

-- RLS: travel_rule_records
ALTER TABLE compliance.travel_rule_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance.travel_rule_records FORCE  ROW LEVEL SECURITY;

CREATE POLICY travel_rule_records_tenant_isolation ON compliance.travel_rule_records
    USING (tenant_id = compliance.current_tenant_id());

-- ---------------------------------------------------------------------------
-- Funcao auxiliar: atualiza updated_at automaticamente
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION compliance.set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

CREATE TRIGGER kyc_subjects_updated_at
    BEFORE UPDATE ON compliance.kyc_subjects
    FOR EACH ROW EXECUTE FUNCTION compliance.set_updated_at();

CREATE TRIGGER travel_rule_records_updated_at
    BEFORE UPDATE ON compliance.travel_rule_records
    FOR EACH ROW EXECUTE FUNCTION compliance.set_updated_at();
