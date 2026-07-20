-- =============================================================================
-- 0004_campaign_zones_with_check_down.sql
-- Reverte o WITH CHECK explícito adicionado em 0004_up, devolvendo a policy
-- ao estado USING-only produzido por 0003_campaign_zones_rls_up.sql.
--
-- `ALTER POLICY` não sabe REMOVER uma expressão WITH CHECK já gravada
-- (não há sintaxe para "voltar a NULL"), portanto a reversão é
-- DROP + CREATE com o texto de USING idêntico ao de 0003.
-- =============================================================================

DROP POLICY IF EXISTS campaign_zones_tenant_isolation ON config.campaign_zones;

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
