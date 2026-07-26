-- =============================================================================
-- 0001_stats_schema_up.sql
-- Schema stats — persistência do collector via Postgres (addon §3.3,
-- "perfil BETA": dependência única = Postgres, sem Redpanda/ClickHouse/Iceberg
-- ainda vivos). Escrito por internal/telemetry/pgsink; lido pelo BFF via
-- stats.live_kpis (colunas tenant_id, campaign_id, requests, impressions,
-- clicks, conversions, as_of — contrato fixo com o BFF).
--
-- POSICIONAMENTO NO MANDATO (DA-7 / §2.2)
--   Este schema é uma PONTE TEMPORÁRIA, não o "ao vivo" definitivo do mandato
--   (que é ClickHouse via Kafka engine + AggregatingMergeTree, ADR-0001) nem o
--   faturável (Iceberg, batch horário). stats.live_kpis é rotulada "ao vivo"
--   explicitamente (ver COMMENT ON VIEW abaixo) e NUNCA deve ser tratada como
--   fonte de faturamento — faturamento reconcilia exclusivamente contra o
--   lakehouse Iceberg, nunca contra streaming nem contra este schema.
--
-- TIPOS DE tenant_id/campaign_id — CORRIGIDO (achado do BFF, pré-fechamento):
--   A primeira versão desta migration usava tenant_id TEXT e campaign_id TEXT
--   ("robustez" contra query params não validados no /lg,/ck,/ct). Isso
--   quebrava o contrato de leitura já escrito por bff/src/adapters/
--   postgres-stats.ts, que faz:
--     JOIN config.campaigns c ON c.id = lk.campaign_id             (c.id é BIGINT)
--     WHERE lk.tenant_id = NULLIF(current_setting(...), '')::uuid  (compara contra UUID)
--   Postgres não tem operador "=" implícito entre bigint/text nem entre
--   text/uuid — as duas comparações acima teriam falhado em runtime real
--   (mascarado porque a suíte do BFF testa contra um Pool mockado, não contra
--   Postgres real — ver postgres-stats.test.ts, seção PENDÊNCIA). Corrigido:
--   tenant_id é UUID e campaign_id é BIGINT, idênticos a
--   config.advertisers.tenant_id e config.campaigns.id.
--
--   Consequência para tenant_id/campaign_id malformados vindos de query
--   string não validada (tid/cid do collector): o INSERT falha com erro de
--   cast (ex.: "invalid input syntax for type uuid") em vez de ser
--   silenciosamente aceito. Isto é SEGURO aqui porque cada evento é gravado
--   em SUA PRÓPRIA transação (ver internal/telemetry/pgsink, "Multi-tenant
--   RLS no caminho de escrita") — uma falha de cast aborta só a transação
--   daquele evento (contabilizada por PostgresSink.WriteErrorCount), nunca
--   trava o worker nem derruba outros eventos. E é estritamente melhor que
--   aceitar o valor: um tenant_id/campaign_id que não bate com o tipo das
--   tabelas config.* nunca seria lido de volta pelo BFF de qualquer forma
--   (a query dele SEMPRE compara via ::uuid / JOIN bigint) — persistir uma
--   linha inconsultável seria dado morto disfarçado de dado gravado.
--
-- IDEMPOTÊNCIA (DA-7, at-least-once)
--   PK event_id (TEXT, ULID mintado por internal/telemetry.NewULID — mesmo
--   gerador do Producer Redpanda) + INSERT ... ON CONFLICT (event_id) DO
--   NOTHING no pgsink. Reentrega nunca duplica uma linha.
--
-- CA-6 (impressão em branco nunca é faturável)
--   billable/blank chegam ao pgsink já computados por blank.ComputeBlankBillable
--   (internal/telemetry/blank, chamado uma única vez em
--   services/collector/cmd/collector/main.go:handleImpression) — este schema
--   NÃO reimplementa a decisão. O CHECK events_raw_ca6_chk abaixo é uma
--   segunda linha de defesa NO BANCO: a mesma invariante testada em
--   internal/telemetry/blank/blank_test.go, aplicada agora
--   incondicionalmente a QUALQUER caminho de escrita (inclusive uma futura
--   migração/backfill manual), não apenas ao pgsink.
--
-- AdRequest / "requests" (ajuste de contrato, ver relatório da onda) —
--   PERSISTIDO, ao contrário da primeira versão desta migration. Sem ele o
--   BFF não tinha nenhuma fonte para o KPI `requests`/`inventoryLoss` (CA-6:
--   perda de inventário = requests - impressions) e recorria a
--   `requests = impressions` — um número FABRICADO com rótulo de fato, a
--   mesma classe de bug que a 31ª onda removeu do dashboard.
--
--   ATENÇÃO — limite semântico real, não apenas de escopo: AdRequest ocorre
--   ANTES da decisão de veiculação (services/collector/cmd/collector/
--   main.go:handleAsyncJS roda antes de qualquer campanha ser escolhida) —
--   portanto NUNCA carrega campaign_id. events_raw.campaign_id é NULL para
--   event_type='ad_request', por construção, não por omissão. Consequência
--   em stats.live_kpis (GROUP BY tenant_id, campaign_id): as linhas de
--   'ad_request' caem no grupo (tenant_id, campaign_id IS NULL) — a MESMA
--   linha por tenant nunca se funde com as linhas por-campanha (que têm
--   campaign_id preenchido). A query atual de bff/src/adapters/
--   postgres-stats.ts faz `JOIN config.campaigns c ON c.id = lk.campaign_id`
--   (INNER JOIN), que por definição NUNCA casa com campaign_id IS NULL — ou
--   seja, o adapter atual do BFF, sem uma mudança própria (fora do escopo
--   de arquivos desta onda — bff/src/adapters/postgres-stats.ts não é
--   arquivo do data-platform-engineer), não vê `requests` através do JOIN
--   existente. A leitura correta do total por tenant é uma consulta à parte:
--     SELECT requests FROM stats.live_kpis
--     WHERE tenant_id = <tenant> AND campaign_id IS NULL
--   Isto não é um placeholder nem uma lacuna escondida — é a única
--   atribuição HONESTA possível para um evento que precede a escolha de
--   campanha (qualquer tentativa de "distribuir" ad requests entre
--   campanhas exigiria inventar uma atribuição, exatamente o que este
--   sistema recusa a fazer). Documentado aqui E no relatório da onda para
--   o data-platform-engineer/frontend-bff-engineer fecharem o fio.
--
-- TX-5/DA-11 (sem PII)
--   AdRequest é o evento com os campos de maior risco de PII/IP embutido
--   (geo, classe de user-agent, referer, custom_vars do publisher) — NENHUM
--   deles é persistido aqui. internal/telemetry/pgsink.EmitAdRequest grava
--   apenas tenant_id/zone_id/decision_id (colunas já existentes no schema,
--   nenhuma coluna nova foi criada para AdRequest). As colunas de texto que
--   chegam de query string não validada (campaign_id/banner_id/zone_id/
--   decision_id/dest_url/attribution_decision_id) são varridas por
--   internal/privacyscan (FindIPLiterals) no worker do pgsink antes do
--   INSERT — defesa em profundidade contra parâmetros adversariais.
--
-- TX-2 (sem float)
--   Nenhuma coluna monetária. Nenhum tipo FLOAT/REAL/DOUBLE/MONEY em lugar
--   algum deste arquivo (scripts/ci/no-float-sql.sh cobre este diretório).
--
-- Nunca particionar por tenant_id (mandato §2.2): este schema não particiona
-- a tabela (volume de beta é modesto; quando isto migrar para ClickHouse via
-- Redpanda, o particionamento real é por hash de event_id/zone_id no tópico,
-- não aqui). ATENÇÃO DE VOLUME: ad_request é ordens de magnitude mais
-- frequente que impressão/clique/conversão (todo carregamento de página
-- gera um ad_request; só uma fração vira impressão) — ver
-- internal/telemetry/pgsink sobre a fila bufferizada compartilhada e
-- drop-contabilizado sob overflow, e a nota de retenção abaixo.
--
-- RETENÇÃO (fora de escopo desta onda, documentado para não surpreender):
--   Esta tabela não tem TTL/partição/purga automática. Dado o volume de
--   ad_request, ela cresce sem limite em um ambiente de beta prolongado.
--   Isto é aceitável para validar o funil "ao vivo" agora; um job de
--   retenção (ou a migração para ClickHouse, que É o destino de longo
--   prazo) precisa endereçar isso antes de um beta de longa duração.
-- =============================================================================

CREATE SCHEMA IF NOT EXISTS stats;

COMMENT ON SCHEMA stats IS
    'Persistência via Postgres do collector para o perfil BETA (addon §3.3). '
    'Ponte temporária — o mandato de longo prazo é ClickHouse (ADR-0001). '
    'stats.live_kpis é "ao vivo", nunca faturável (DA-7).';

-- ---------------------------------------------------------------------------
-- Função auxiliar: lê tenant_id da sessão corrente. RETURNS UUID — mesmo
-- padrão canônico de config.current_tenant_id() / ledger.current_tenant_id()
-- / compliance.current_tenant_id(). Ver nota de cabeçalho "TIPOS" acima
-- sobre por que a primeira versão desta função (RETURNS TEXT) foi corrigida.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION stats.current_tenant_id()
RETURNS UUID LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT NULLIF(current_setting('adserver.tenant_id', true), '')::UUID
$$;

COMMENT ON FUNCTION stats.current_tenant_id() IS
    'Lê tenant_id da sessão via SET LOCAL adserver.tenant_id. Retorna NULL se '
    'ausente (fail-closed — nenhuma linha visível). Um valor presente mas '
    'não-UUID lança exceção — aborta APENAS a transação do evento corrente '
    '(uma transação por evento no pgsink), nunca o processo. Padrão canônico: '
    'idêntico a config.current_tenant_id()/ledger.current_tenant_id().';

-- ---------------------------------------------------------------------------
-- stats.events_raw — evento bruto de telemetria
-- (ad_request | impression | click | conversion)
-- ---------------------------------------------------------------------------
CREATE TABLE stats.events_raw (
    -- ULID (internal/telemetry.NewULID) — mesmo gerador do Producer Redpanda.
    -- PK: chave de idempotência (ON CONFLICT DO NOTHING no pgsink).
    event_id                TEXT        NOT NULL,
    event_type              TEXT        NOT NULL,
    -- tenant_id: UUID — mesmo tipo de config.advertisers.tenant_id. Ver nota
    -- de cabeçalho "TIPOS".
    tenant_id                UUID        NOT NULL,
    -- campaign_id: BIGINT — mesmo tipo de config.campaigns.id (FK lógica,
    -- não física: este schema não referencia config diretamente, mesmo
    -- espírito de "FK lógica, não física" já usado entre config/ledger no
    -- restante do repositório). NULL para event_type='ad_request' (PRECEDE
    -- a escolha de campanha — ver nota "AdRequest / requests" acima) e para
    -- qualquer linha cujo cid de origem não seja um inteiro válido.
    campaign_id               BIGINT,
    banner_id                  TEXT        NOT NULL DEFAULT '',
    zone_id                     TEXT        NOT NULL DEFAULT '',
    decision_id                  TEXT        NOT NULL DEFAULT '',
    model_version                 TEXT        NOT NULL DEFAULT '',
    -- served_tier: string do enum commonv1.ServedTier (ex.: "SERVED_TIER_REMNANT").
    -- Preenchido só para event_type='impression'.
    served_tier                    TEXT        NOT NULL DEFAULT '',
    -- billable/blank: computados UMA VEZ upstream por blank.ComputeBlankBillable
    -- (CA-6) — o pgsink apenas persiste o que já veio calculado. Ver
    -- events_raw_ca6_chk abaixo para a segunda linha de defesa no banco.
    billable                        BOOLEAN     NOT NULL DEFAULT false,
    blank                            BOOLEAN     NOT NULL DEFAULT false,
    -- ivt_status: SEMPRE '' vindo deste sink — marcação de fraude/IVT (TX-6) é
    -- responsabilidade de um estágio de ingestão posterior (fora de escopo
    -- desta onda; a coluna existe para compatibilidade futura de schema).
    ivt_status                        TEXT        NOT NULL DEFAULT '',
    -- dest_url: preenchido só para event_type='click'.
    dest_url                           TEXT        NOT NULL DEFAULT '',
    -- attribution_decision_id: preenchido só para event_type='conversion'.
    attribution_decision_id             TEXT        NOT NULL DEFAULT '',
    -- occurred_at: instante em que o evento OCORREU (event-time), não o de
    -- ingestão — mesma semântica de adserver.common.v1.Envelope.occurred_at.
    occurred_at                          TIMESTAMPTZ NOT NULL,
    ingested_at                           TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT events_raw_pk           PRIMARY KEY (event_id),
    CONSTRAINT events_raw_type_chk     CHECK (event_type IN ('ad_request', 'impression', 'click', 'conversion')),
    -- CA-6, segunda linha de defesa no banco (ver nota de cabeçalho): uma
    -- impressão em branco NUNCA pode estar marcada como faturável, incondi-
    -- cionalmente — não depende do caminho de escrita ter chamado
    -- blank.ComputeBlankBillable corretamente.
    CONSTRAINT events_raw_ca6_chk      CHECK (NOT (blank AND billable))
);

CREATE INDEX events_raw_tenant_campaign_type_idx
    ON stats.events_raw (tenant_id, campaign_id, event_type);
CREATE INDEX events_raw_occurred_at_idx
    ON stats.events_raw (occurred_at);

COMMENT ON TABLE  stats.events_raw IS
    'Evento bruto de telemetria (ad_request|impression|click|conversion) '
    'persistido pelo collector via internal/telemetry/pgsink — dependência '
    'única Postgres do perfil BETA. PK event_id = idempotência at-least-once '
    '(DA-7). RLS por tenant_id (TX-3). campaign_id é NULL para ad_request '
    '(precede a decisão de campanha — ver cabeçalho da migration).';
COMMENT ON COLUMN stats.events_raw.campaign_id IS
    'BIGINT, mesmo tipo de config.campaigns.id (FK lógica). NULL para '
    'event_type=''ad_request'' (nenhuma campanha foi escolhida ainda) e para '
    'qualquer cid de origem que não seja um inteiro válido.';
COMMENT ON COLUMN stats.events_raw.billable IS
    'CA-6: computado por blank.ComputeBlankBillable ANTES de chegar aqui '
    '(services/collector/cmd/collector/main.go:handleImpression) — nunca '
    'recomputado neste schema. events_raw_ca6_chk impõe NOT (blank AND billable) '
    'incondicionalmente no banco.';
COMMENT ON COLUMN stats.events_raw.ivt_status IS
    'Sempre vazio ('''') vindo deste sink. Marcação de fraude/IVT (TX-6) é '
    'responsabilidade de um estágio de ingestão à parte — fora de escopo desta '
    'onda. stats.live_kpis NÃO filtra por ivt_status (consequência: os números '
    '"ao vivo" podem incluir tráfego que viria a ser marcado como IVT — mais um '
    'motivo pelo qual esta view nunca é faturável, ver DA-7).';

-- ---------------------------------------------------------------------------
-- RLS (TX-3) — idêntico em forma ao padrão canônico de
-- db/config/migrations/0002_config_rls_up.sql (USING + WITH CHECK).
-- ---------------------------------------------------------------------------
ALTER TABLE stats.events_raw ENABLE ROW LEVEL SECURITY;
ALTER TABLE stats.events_raw FORCE  ROW LEVEL SECURITY;

CREATE POLICY events_raw_tenant_isolation ON stats.events_raw
    USING      (tenant_id = stats.current_tenant_id())
    WITH CHECK (tenant_id = stats.current_tenant_id());

COMMENT ON POLICY events_raw_tenant_isolation ON stats.events_raw IS
    'Isola eventos por tenant. Fail-closed: sem adserver.tenant_id setado, '
    'nenhuma linha visível. WITH CHECK: banco rejeita INSERT/UPDATE com '
    'tenant_id diferente do tenant corrente da sessão (TX-3).';

-- ---------------------------------------------------------------------------
-- adserver_stats_writer — role dedicada, criada de forma auto-contida NESTA
-- migration (diferente de adserver_app/adserver_loader/adserver_copilot, que
-- são centralizados em db/seed/dev_roles.sql — fora do escopo de arquivos
-- desta onda). Padrão de criação idempotente idêntico ao de dev_roles.sql.
--
-- Least-privilege: INSERT em events_raw. NÃO tem UPDATE/DELETE — o pgsink
-- nunca apaga nem modifica o que grava (append-only, mesmo espírito do
-- REVOKE UPDATE,DELETE em ledger.postings). NOBYPASSRLS explícito: o pgsink
-- grava com SET LOCAL adserver.tenant_id por-evento (uma transação por
-- evento) e depende da policy WITH CHECK para nunca gravar tenant_id
-- divergente — não porque confia no valor recebido, mas para que a MESMA
-- garantia de banco valha tanto para o pgsink quanto para qualquer outro
-- escritor futuro.
--
-- GRANT SELECT (medido, não assumido): o `INSERT ... ON CONFLICT (event_id)
-- DO NOTHING` exigido pela idempotência (DA-7) FALHA com "permission denied"
-- para um role sem SELECT na tabela — confirmado empiricamente contra
-- Postgres 16 real (o detector de conflito da cláusula ON CONFLICT precisa
-- ler o índice/linha existente mesmo no ramo DO NOTHING). Sem este GRANT o
-- sink não é write-only "puro", mas SELECT aqui não é um risco de
-- confidencialidade: RLS + FORCE ROW LEVEL SECURITY continuam valendo para
-- este role (NOBYPASSRLS), então mesmo com SELECT ele só enxergaria linhas
-- do tenant corrente da própria transação — nunca cross-tenant.
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_stats_writer') THEN
        CREATE ROLE adserver_stats_writer LOGIN PASSWORD 'stats_writer_dev_only' NOBYPASSRLS;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA stats TO adserver_stats_writer;
GRANT SELECT, INSERT ON stats.events_raw TO adserver_stats_writer;
GRANT EXECUTE ON FUNCTION stats.current_tenant_id() TO adserver_stats_writer;

-- ---------------------------------------------------------------------------
-- stats.live_kpis — "ao vivo" (DA-7). Contrato fixo com o BFF: colunas
-- tenant_id, campaign_id, requests, impressions, clicks, conversions, as_of.
--
-- security_invoker = true (mesmo padrão de ledger.account_balances,
-- 0003_ledger_rls_up.sql): a RLS de stats.events_raw se aplica através da
-- view — sem isto a view seria SECURITY DEFINER implícito e um bypass
-- silencioso de RLS. Consequência: o role que consulta esta view precisa de
-- GRANT SELECT tanto na view QUANTO na tabela-base (ver grants comentados
-- abaixo).
--
-- impressions conta só billable=true (CA-6: impressão em branco nunca
-- aparece como "impressão" faturável, nem mesmo na leitura "ao vivo").
--
-- requests conta event_type='ad_request'. IMPORTANTE (ver cabeçalho da
-- migration, seção "AdRequest / requests"): essas linhas têm campaign_id
-- NULL por construção (precedem a escolha de campanha) e portanto caem em
-- uma linha PRÓPRIA por tenant (tenant_id, campaign_id IS NULL) nesta view —
-- NUNCA na mesma linha que impressions/clicks/conversions de uma campanha
-- específica. Não há atribuição inventada de ad requests a campanhas.
--
-- as_of = now() da CONSULTA (view simples, não materializada — sempre
-- reflete o commit mais recente). Isto NÃO é o "consolidado ≤1h" do
-- StatsHourly/Iceberg — ver COMMENT ON VIEW.
-- ---------------------------------------------------------------------------
CREATE VIEW stats.live_kpis
    WITH (security_invoker = true)
AS
SELECT
    tenant_id,
    campaign_id,
    COUNT(*) FILTER (WHERE event_type = 'ad_request')               AS requests,
    COUNT(*) FILTER (WHERE event_type = 'impression' AND billable)  AS impressions,
    COUNT(*) FILTER (WHERE event_type = 'click')                    AS clicks,
    COUNT(*) FILTER (WHERE event_type = 'conversion')                AS conversions,
    now()                                                             AS as_of
FROM stats.events_raw
GROUP BY tenant_id, campaign_id;

COMMENT ON VIEW stats.live_kpis IS
    '"AO VIVO" — NUNCA "consolidado/faturável" (DA-7 / mandato §2.2). Agregação '
    'incremental de curto prazo sobre stats.events_raw (janela = todo o '
    'histórico retido nesta tabela-ponte do perfil BETA; quando migrar para '
    'ClickHouse, a janela real será segundos-minutos via MV incremental). '
    'Faturamento reconcilia EXCLUSIVAMENTE contra o lakehouse Iceberg (batch '
    'horário via StatsHourly) — nunca contra este view, nunca contra qualquer '
    'caminho de streaming. A UI deve rotular esta fonte como "ao vivo", nunca '
    'como "consolidado ≤1h" (contrato de saída coordenado com o BFF/frontend). '
    'impressions conta só billable=true (CA-6). requests (event_type='
    '''ad_request'') aparece numa linha própria por tenant com campaign_id '
    'IS NULL — ad requests precedem a escolha de campanha, nunca são '
    'atribuídos a uma campanha específica; ler o total por tenant exige '
    'filtrar explicitamente campaign_id IS NULL, um INNER JOIN contra '
    'config.campaigns nunca verá essa linha. as_of = instante da consulta.';

-- Grants para o leitor da aplicação (adserver_app — BFF). Comentado aqui
-- deliberadamente (mesma convenção de db/vector/migrations/0002_vector_rls_up.sql):
-- esta migration não deve falhar em um banco onde adserver_app ainda não
-- existe. A execução real (descomentada) roda em make/dev.mk::dev-db-setup,
-- DEPOIS de db/seed/dev_roles.sql garantir que o role existe.
--
-- GRANT USAGE ON SCHEMA stats TO adserver_app;
-- GRANT SELECT ON stats.events_raw TO adserver_app;
-- GRANT SELECT ON stats.live_kpis TO adserver_app;
-- GRANT EXECUTE ON FUNCTION stats.current_tenant_id() TO adserver_app;
