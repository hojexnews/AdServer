-- =============================================================================
-- 0002_config_rls_up.sql
-- RLS (Row-Level Security) por tenant_id no schema config (TX-3/§2.6).
--
-- Estratégia: cada tabela habilita RLS; a policy usa current_setting()
-- para ler o tenant_id injetado pelo pool de conexões (PgBouncer) ou pelo
-- middleware da aplicação via SET LOCAL adserver.tenant_id = '<uuid>'.
--
-- Invariante TX-3: USING **e** WITH CHECK em toda policy — idêntico ao ledger
-- (0003_ledger_rls_up.sql). USING filtra as linhas visíveis (SELECT/UPDATE/
-- DELETE); WITH CHECK rejeita INSERT/UPDATE cuja nova linha tenha tenant_id ≠
-- do tenant corrente. Para uma policy permissiva FOR ALL, o Postgres já usa a
-- expressão de USING como WITH CHECK por omissão (verificado: INSERT/UPDATE
-- cross-tenant abortam com 42501) — o WITH CHECK explícito torna a proteção
-- robusta a futuras policies por-comando e alinha o par de migrations RLS.
--
-- O motor de decisão (Go) injeta o tenant_id na sessão antes de qualquer
-- query de snapshot. O BFF faz o mesmo. Nunca é enviado pelo cliente.
--
-- Para o superusuário (migrations/admin): FORCE ROW LEVEL SECURITY não se
-- aplica a superusers por default; rodar migrations como superuser contorna
-- o RLS — comportamento esperado e necessário.
--
-- Para o usuário de aplicação (adserver_app): deve ser um usuário não-super
-- com GRANT SELECT/INSERT/UPDATE/DELETE nas tabelas abaixo.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- Função auxiliar: lê o tenant_id da sessão corrente.
-- Retorna NULL se não definido (causa rejeição pela policy).
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION config.current_tenant_id()
RETURNS UUID LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT NULLIF(current_setting('adserver.tenant_id', true), '')::UUID
$$;

COMMENT ON FUNCTION config.current_tenant_id() IS
    'Lê tenant_id da sessão via SET LOCAL adserver.tenant_id. Retorna NULL se ausente (rejeita acesso).';

-- ---------------------------------------------------------------------------
-- Habilitar RLS + policies em cada tabela com tenant_id
-- ---------------------------------------------------------------------------

-- advertisers
ALTER TABLE config.advertisers ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.advertisers FORCE  ROW LEVEL SECURITY;

CREATE POLICY advertisers_tenant_isolation ON config.advertisers
    USING      (tenant_id = config.current_tenant_id())
    WITH CHECK (tenant_id = config.current_tenant_id());

-- campaigns
ALTER TABLE config.campaigns ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.campaigns FORCE  ROW LEVEL SECURITY;

CREATE POLICY campaigns_tenant_isolation ON config.campaigns
    USING      (tenant_id = config.current_tenant_id())
    WITH CHECK (tenant_id = config.current_tenant_id());

-- banners
ALTER TABLE config.banners ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.banners FORCE  ROW LEVEL SECURITY;

CREATE POLICY banners_tenant_isolation ON config.banners
    USING      (tenant_id = config.current_tenant_id())
    WITH CHECK (tenant_id = config.current_tenant_id());

-- sites
ALTER TABLE config.sites ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.sites FORCE  ROW LEVEL SECURITY;

CREATE POLICY sites_tenant_isolation ON config.sites
    USING      (tenant_id = config.current_tenant_id())
    WITH CHECK (tenant_id = config.current_tenant_id());

-- zones
ALTER TABLE config.zones ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.zones FORCE  ROW LEVEL SECURITY;

CREATE POLICY zones_tenant_isolation ON config.zones
    USING      (tenant_id = config.current_tenant_id())
    WITH CHECK (tenant_id = config.current_tenant_id());

-- delivery_rule_sets
ALTER TABLE config.delivery_rule_sets ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.delivery_rule_sets FORCE  ROW LEVEL SECURITY;

CREATE POLICY delivery_rule_sets_tenant_isolation ON config.delivery_rule_sets
    USING      (tenant_id = config.current_tenant_id())
    WITH CHECK (tenant_id = config.current_tenant_id());

-- delivery_rules
ALTER TABLE config.delivery_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.delivery_rules FORCE  ROW LEVEL SECURITY;

CREATE POLICY delivery_rules_tenant_isolation ON config.delivery_rules
    USING      (tenant_id = config.current_tenant_id())
    WITH CHECK (tenant_id = config.current_tenant_id());

-- caps
ALTER TABLE config.caps ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.caps FORCE  ROW LEVEL SECURITY;

CREATE POLICY caps_tenant_isolation ON config.caps
    USING      (tenant_id = config.current_tenant_id())
    WITH CHECK (tenant_id = config.current_tenant_id());

-- campaign_zones não tem tenant_id próprio; o acesso é controlado via
-- campaigns e zones que já têm RLS. Sem policy direta — documentado no README.
