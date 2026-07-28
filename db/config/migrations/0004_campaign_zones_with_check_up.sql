-- =============================================================================
-- 0004_campaign_zones_with_check_up.sql
-- WITH CHECK EXPLÍCITO na policy config.campaign_zones_tenant_isolation
-- (achado HIGH `campaign-zones-with-check-write-path-untested`, 30ª onda).
--
-- CONTEXTO DO ACHADO
--   A migration 0003 criou `campaign_zones_tenant_isolation` como policy
--   FOR ALL **USING-only**. Em tempo de execução isso funciona: quando uma
--   policy FOR ALL omite WITH CHECK, o Postgres reusa a expressão de USING
--   como verificação de escrita. MAS o catálogo `pg_policy.polwithcheck`
--   fica NULL — ou seja, a garantia existe apenas por FALLBACK implícito,
--   nada no repositório a fixa.
--
--   Consequência: se alguém "documentasse" a policy adicionando um
--   `WITH CHECK (true)` (erro plausível de quem não entende o fallback), o
--   isolamento de ESCRITA cairia silenciosamente — todos os testes de
--   LEITURA (BLOCOS 1–5 de db/config/tests/rls_isolation_test.sql)
--   continuariam 100% verdes, porque WITH CHECK não afeta SELECT.
--
-- FIX
--   1. WITH CHECK explícito e gravado no catálogo (polwithcheck NOT NULL),
--      de modo que o BLOCO 5.5 do teste de isolamento (introspecção de
--      pg_policy, agora DEFAULT-DENY sobre TODAS as policies do schema
--      config) passe a cobrir também esta policy.
--   2. O WITH CHECK é ESTRITAMENTE MAIS FORTE que o USING herdado: além do
--      lado `campaign_id` (já coberto por 0003), exige que o `zone_id`
--      também pertença ao tenant da sessão. `config.zones` tem FORCE ROW
--      LEVEL SECURITY (0002_config_rls_up.sql), portanto o EXISTS na zones
--      só devolve linha se a zona for do tenant corrente.
--
--      Sem esta segunda perna, um advertiser do tenant A poderia vincular a
--      SUA campanha a uma zona de OUTRO tenant (o USING de 0003 só valida o
--      lado campanha) — veiculando criativo em inventário alheio e
--      corrompendo faturamento. Este é um endurecimento real, não cosmético.
--
--   Nenhuma mudança em LEITURA: `USING` permanece byte-a-byte o de 0003
--   (ALTER POLICY ... WITH CHECK só substitui a expressão de escrita).
--   O caminho de escrita legítimo (mesmo tenant nos dois lados) segue
--   permitido — provado pelo controle POSITIVO do BLOCO 6.5 do teste.
--
-- NOTA DE ROLLOUT
--   Esta migration é NOVA (0003 não foi editada). Os runners de migration
--   (make/db.mk alvo db-test-all, e .github/workflows/db.yml) NÃO enumeram
--   migrations à mão: cada um deriva a lista _up/_down de
--   db/config/migrations/ via glob + sort (ver cabeçalho de make/db.mk) —
--   esta migration foi pega automaticamente por já seguir a convenção de
--   nome `NNNN_nome_{up,down}.sql`. NÃO edite make/db.mk nem db.yml para
--   "adicionar" esta migration a alguma lista — não existe lista para
--   editar, e reintroduzir uma seria regredir a orfandade que o glob
--   eliminou. Os únicos requisitos reais para uma migration nova ser
--   pega automaticamente são: (a) o prefixo numérico de 4 dígitos
--   zero-padded, e (b) o par _up.sql/_down.sql completo — verificado pela
--   sentinela `db-check-migration-pairing` (make/db.mk).
-- =============================================================================

ALTER POLICY campaign_zones_tenant_isolation ON config.campaign_zones
    WITH CHECK (
        EXISTS (
            SELECT 1
            FROM   config.campaigns c
            WHERE  c.id = campaign_zones.campaign_id
        )
        AND EXISTS (
            SELECT 1
            FROM   config.zones z
            WHERE  z.id = campaign_zones.zone_id
        )
    );

COMMENT ON TABLE config.campaign_zones IS
    'Vínculo N:N campanha-zona. RLS via subquery EXISTS em campaigns (FORCE RLS) '
    'para LEITURA (USING, 0003) e em campaigns AND zones para ESCRITA '
    '(WITH CHECK explícito, 0004). Sem tenant_id redundante — isolamento por '
    'JOIN aninhado (TX-3/§2.6).';
