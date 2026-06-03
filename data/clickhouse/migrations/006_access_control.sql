-- data/clickhouse/migrations/006_access_control.sql
--
-- Row-policies e quotas por tenant_id (TX-3).
--
-- MODELO DE ISOLAMENTO (TX-3):
--   Cada tenant acessa apenas suas proprias linhas. O middleware do BFF (CA-1)
--   resolve o tenant_id e propaga em cada query via SET parameter ou via
--   usuario ClickHouse dedicado por tenant.
--
--   Estrategia adotada: ROW POLICY por tabela/view filtrando tenant_id.
--   O BFF nao precisa adicionar WHERE tenant_id = ? em toda query; a
--   row-policy garante o filtro no engine do ClickHouse, server-side.
--
--   USUARIO PADRAO: um usuario 'tenant_{id}' por tenant com row-policy fixada.
--   Usuarios de plataforma (ingestao, billing batch) usam usuario admin com
--   row-policy permissiva ('1 = 1').
--
-- QUOTAS: limitam consumo de recursos por tenant (CPU/memory/rows_read/tempo).
--   Evitam que um tenant com query pesada degrade outros (multi-tenant fairness).
--
-- NOTA: este arquivo usa placeholders {tenant_id}. Em producao, gerar um
-- script por tenant via tooling IaC (OpenTofu/Argo CD), ou usar o mecanismo
-- de user-defined functions do ClickHouse para row-policy dinamica.
-- A row-policy dinamica (via funcao) e preferida em escala: 1 policy por
-- tabela em vez de N policies por tenant.

-- =============================================================================
-- ROW POLICIES — politica dinamica via currentUser() (recomendado)
-- =============================================================================
-- Estrategia: o usuario ClickHouse tem o nome "tenant_{tenant_id}".
-- A row-policy extrai o tenant_id do nome do usuario atual removendo
-- APENAS o prefixo "tenant_" e validando que o restante e um UUID valido.
--
-- CORRECAO DE SEGURANÇA (MEDIUM #5):
--   ANTES (errado): replaceAll(currentUser(), 'tenant_', '')
--     - replaceAll remove TODAS as ocorrencias da substring 'tenant_' no nome,
--       incluindo ocorrencias no meio ou no sufixo. Um usuario chamado
--       'tenant_ab_tenant_cd' seria resolvido como 'ab_cd', podendo casar
--       linhas de um tenant inesperado (vazamento cross-tenant).
--
--   AGORA (corrigido): remocao de prefixo + validacao de UUID (fail-closed)
--     - Usa substring() para remover somente o prefixo exato "tenant_".
--     - Valida que o restante casa com o padrao UUID v4 via match().
--     - Se o nome do usuario nao tiver o prefixo "tenant_" OU o sufixo
--       nao for um UUID valido, a expressao retorna NULL e a policy NAO
--       casa nenhuma linha (fail-closed: zero rows, zero vazamento).
--     - Nomes adversariais como "tenant_ab_tenant_cd" sao rejeitados porque
--       "ab_tenant_cd" nao e um UUID valido.
--
-- ALTERNATIVA CONSIDERADA: getSetting() / currentRoles() para injetar
--   o tenant_id via parametro de sessao, em vez de parsear o nome do usuario.
--   Preferida em escala por ser mais robusta, mas exige que o BFF configure
--   SET custom_setting_tenant_id = '...' na sessao apos autenticacao.
--   Adiado para v2: requer mudanca no driver de conexao do BFF e no schema
--   de usuarios do ClickHouse. Documentado como proximo passo (TX-3-v2).
--   Por ora, a abordagem de prefixo+UUID e adotada com fail-closed.
--
-- EXPRESSAO DE EXTRACAO (reutilizada em todas as policies abaixo):
--   if(
--     startsWith(currentUser(), 'tenant_')
--       AND match(substring(currentUser(), length('tenant_') + 1),
--                 '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
--     substring(currentUser(), length('tenant_') + 1),
--     NULL
--   )
--   - Se usuario = 'tenant_550e8400-e29b-41d4-a716-446655440000':
--       sufixo = '550e8400-e29b-41d4-a716-446655440000' -> UUID valido -> casa.
--   - Se usuario = 'tenant_ab_tenant_cd':
--       sufixo = 'ab_tenant_cd' -> nao e UUID -> NULL -> zero rows (fail-closed).
--   - Se usuario = 'adserver_admin':
--       startsWith() = false -> NULL -> zero rows (nao tem tenant_role, mas
--       mesmo se tivesse: fail-closed).

-- Politica para stats_hourly_state (tabela base dos rollups)
CREATE ROW POLICY IF NOT EXISTS rp_stats_hourly_state_tenant
ON adserver.stats_hourly_state
FOR SELECT
USING tenant_id = if(
    startsWith(currentUser(), 'tenant_')
      AND match(
            substring(currentUser(), length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring(currentUser(), length('tenant_') + 1),
    NULL
)
TO tenant_role;

-- Politica para raw_ad_request
CREATE ROW POLICY IF NOT EXISTS rp_raw_ad_request_tenant
ON adserver.raw_ad_request
FOR SELECT
USING tenant_id = if(
    startsWith(currentUser(), 'tenant_')
      AND match(
            substring(currentUser(), length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring(currentUser(), length('tenant_') + 1),
    NULL
)
TO tenant_role;

-- Politica para raw_impression
CREATE ROW POLICY IF NOT EXISTS rp_raw_impression_tenant
ON adserver.raw_impression
FOR SELECT
USING tenant_id = if(
    startsWith(currentUser(), 'tenant_')
      AND match(
            substring(currentUser(), length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring(currentUser(), length('tenant_') + 1),
    NULL
)
TO tenant_role;

-- Politica para raw_click
CREATE ROW POLICY IF NOT EXISTS rp_raw_click_tenant
ON adserver.raw_click
FOR SELECT
USING tenant_id = if(
    startsWith(currentUser(), 'tenant_')
      AND match(
            substring(currentUser(), length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring(currentUser(), length('tenant_') + 1),
    NULL
)
TO tenant_role;

-- Politica para raw_conversion
CREATE ROW POLICY IF NOT EXISTS rp_raw_conversion_tenant
ON adserver.raw_conversion
FOR SELECT
USING tenant_id = if(
    startsWith(currentUser(), 'tenant_')
      AND match(
            substring(currentUser(), length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring(currentUser(), length('tenant_') + 1),
    NULL
)
TO tenant_role;

-- Politica para raw_decision (apenas propensity/model; sem PII)
CREATE ROW POLICY IF NOT EXISTS rp_raw_decision_tenant
ON adserver.raw_decision
FOR SELECT
USING tenant_id = if(
    startsWith(currentUser(), 'tenant_')
      AND match(
            substring(currentUser(), length('tenant_') + 1),
            '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
          ),
    substring(currentUser(), length('tenant_') + 1),
    NULL
)
TO tenant_role;

-- =============================================================================
-- ROW POLICY — administrador (bypass para ingestao e billing batch)
-- =============================================================================
-- O usuario 'adserver_ingest' (Kafka engine consumer) e 'adserver_billing'
-- (job de billing batch) precisam acessar todos os tenants.

CREATE ROW POLICY IF NOT EXISTS rp_stats_hourly_state_admin
ON adserver.stats_hourly_state
FOR SELECT USING 1 = 1
TO adserver_ingest, adserver_billing, adserver_admin;

CREATE ROW POLICY IF NOT EXISTS rp_raw_impression_admin
ON adserver.raw_impression
FOR SELECT USING 1 = 1
TO adserver_ingest, adserver_billing, adserver_admin;

CREATE ROW POLICY IF NOT EXISTS rp_raw_conversion_admin
ON adserver.raw_conversion
FOR SELECT USING 1 = 1
TO adserver_ingest, adserver_billing, adserver_admin;

-- =============================================================================
-- ROLE: tenant_role (SELECT em tabelas analiticas; sem INSERT/ALTER/DROP)
-- =============================================================================
CREATE ROLE IF NOT EXISTS tenant_role ON CLUSTER '{cluster}';

GRANT SELECT ON adserver.stats_hourly        TO tenant_role;
GRANT SELECT ON adserver.live_stats_exact    TO tenant_role;
GRANT SELECT ON adserver.live_stats_fast     TO tenant_role;
GRANT SELECT ON adserver.raw_impression      TO tenant_role;
GRANT SELECT ON adserver.raw_click           TO tenant_role;
GRANT SELECT ON adserver.raw_conversion      TO tenant_role;
-- Nao conceder SELECT em raw_decision para tenants (propensity e dado interno de ML)

-- =============================================================================
-- ROLE: adserver_admin (acesso total, para manutencao e billing)
-- =============================================================================
CREATE ROLE IF NOT EXISTS adserver_admin_role ON CLUSTER '{cluster}';
GRANT ALL ON adserver.* TO adserver_admin_role;

-- =============================================================================
-- QUOTAS por tenant (fairness multi-tenant)
-- =============================================================================
-- Quota padrao por tenant: limita queries longas e leituras massivas.
-- Ajustar 'max_execution_time', 'max_rows_to_read' e 'max_result_rows'
-- conforme SLA contratual por tier de plano.
--
-- INTERVALO: 3600s (por hora). Reinicia a cada hora.

CREATE QUOTA IF NOT EXISTS quota_tenant_default ON CLUSTER '{cluster}'
KEYED BY user_name
FOR INTERVAL 3600 SECOND
    MAX queries = 1000,
    MAX read_rows = 1000000000,         -- 1B rows/h por tenant
    MAX result_rows = 10000000,          -- 10M rows de resultado/h
    MAX execution_time = 300             -- max 5min por query
TO tenant_role;

-- Quota para adserver_billing (sem limite operacional; billing e batch controlado)
CREATE QUOTA IF NOT EXISTS quota_billing_unlimited ON CLUSTER '{cluster}'
KEYED BY user_name
FOR INTERVAL 3600 SECOND
    MAX queries = 0,                     -- sem limite
    MAX read_rows = 0,
    MAX result_rows = 0,
    MAX execution_time = 0
TO adserver_billing;

-- =============================================================================
-- USUARIOS: criacao de placeholder (em producao, via IaC/Argo CD)
-- =============================================================================
-- Criar usuario de ingestao e billing com senhas via secrets manager.
-- Nao armazenar senhas neste arquivo; usar variavel de ambiente ou OpenBao.
--
-- Exemplo (parametrizado pelo IaC):
--   CREATE USER adserver_ingest IDENTIFIED BY '${INGEST_PASSWORD}';
--   GRANT adserver_admin_role TO adserver_ingest;
--
--   CREATE USER adserver_billing IDENTIFIED BY '${BILLING_PASSWORD}';
--   GRANT adserver_admin_role TO adserver_billing;
--
--   -- Por tenant (gerado pelo IaC para cada tenant_id):
--   CREATE USER tenant_{id} IDENTIFIED BY '${TENANT_PASSWORD}';
--   GRANT tenant_role TO tenant_{id};
--
-- REFERENCIA: secrets em OpenBao (ver platform/); rotacao via Kubernetes Operator.
