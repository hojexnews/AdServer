-- =============================================================================
-- 0003_campaign_zones_rls_up.sql
-- Defesa-em-profundidade: RLS para config.campaign_zones via subquery.
--
-- Contexto (TX-3 / §2.6):
--   campaign_zones é uma tabela de junção N:N (campaign_id, zone_id) sem
--   tenant_id próprio. A migration 0002 documentou que o isolamento ocorre
--   via JOIN obrigatório com campaigns/zones (que têm FORCE RLS), o que é
--   correto para o motor Go (que sempre faz o JOIN no snapshot).
--
--   Entretanto, confiar exclusivamente em invariante de aplicação cria uma
--   lacuna: acesso direto à tabela (DBA, ferramenta ad-hoc, query malformada
--   sem JOIN) expõe vínculos campaign↔zone de todos os tenants.
--
-- Decisão: policy via subquery EXISTS() — sem desnormalização de tenant_id.
--   USING (
--     EXISTS (
--       SELECT 1 FROM config.campaigns c
--       WHERE c.id = campaign_zones.campaign_id
--     )
--   )
--   Como campaigns tem FORCE RLS ativo, a subquery SELECT na campaigns só
--   devolve linha se campaign_id pertencer ao tenant_id da sessão corrente.
--   Resultado: campaign_zones filtra implicitamente pelo mesmo tenant_id,
--   sem duplicar a coluna e sem câmbio de schema.
--
-- Custo: um nested lookup por row avaliada pela policy (índice campaigns_pk
--   é O(log N)); aceitável para tabela de config lida em snapshot batch.
-- =============================================================================

ALTER TABLE config.campaign_zones ENABLE ROW LEVEL SECURITY;
ALTER TABLE config.campaign_zones FORCE  ROW LEVEL SECURITY;

-- Policy de leitura: visível somente se campaign_id pertencer ao tenant ativo.
-- A subquery herda a RLS de config.campaigns (FORCE ROW LEVEL SECURITY),
-- portanto config.current_tenant_id() é avaliada lá — sem duplicação de lógica.
CREATE POLICY campaign_zones_tenant_isolation ON config.campaign_zones
    USING (
        EXISTS (
            SELECT 1
            FROM   config.campaigns c
            WHERE  c.id = campaign_zones.campaign_id
        )
    );

COMMENT ON TABLE config.campaign_zones IS
    'Vínculo N:N campanha-zona. RLS via subquery EXISTS em campaigns (FORCE RLS). '
    'Sem tenant_id redundante — isolamento por JOIN aninhado (TX-3/§2.6).';
