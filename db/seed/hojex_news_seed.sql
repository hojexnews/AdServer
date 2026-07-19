-- =============================================================================
-- db/seed/hojex_news_seed.sql — PROVISIONAMENTO REAL do publisher Hojex News.
--
-- Diferente de dev_seed.sql (demo genérica de UM anunciante): este ficheiro
-- define o INVENTÁRIO first-party do site Hojex.News — as ZONAS reais por
-- colocação editorial (E11, lado-AdServer) que o registo central do site
-- (`web/src/lib/ads/config.ts` → `ZONES`) referencia por `zoneId`.
--
-- FONTE ÚNICA DA VERDADE dos IDs de zona (contrato site↔AdServer):
--   1001 = sidebar     (300x250 · MREC no rail lateral)
--   1002 = in-article  (300x250 · intercalado no corpo do artigo)
--   1003 = leaderboard (728x90  · topo de secção/listagem)
--   1004 = listing     (300x250 · dentro de um feed/listagem)
-- Os IDs são FIXADOS explicitamente (não serial) porque o site os embute como
-- strings literais em `ZONES`. Ao (re)provisionar, mantenha estes IDs — é o
-- que torna o acoplamento site↔AdServer determinístico e reproduzível entre
-- ambientes (dev/staging/prod). O `setval` no fim protege inserts serial
-- futuros (zonas criadas por anunciantes no console) contra colisão.
--
-- INVENTÁRIO = HOUSE (first-party honesto): à partida o serving first-party é
-- inventário próprio (house/self-promo, tier REMNANT sempre-preenchido). Campanhas
-- pagas reais (contract/override) entram pela demanda/console mais tarde — não é
-- escopo do E11. O `served_tier` provado aqui é REMNANT; o `geo` é encaminhado
-- mas NÃO filtra (house não tem regra de geo → preenche em qualquer país).
--
-- IMPORTANTE (cascata, verificado em internal/cascade/cascade.go):
--   `eligibleBanners` NÃO casa dimensão banner↔zona — seleciona o 1.º banner
--   elegível da campanha. Por isso há UMA campanha house POR TAMANHO, ligada
--   apenas às zonas do seu tamanho, garantindo que cada zona serve um criativo
--   do tamanho certo:
--     - "Hojex House MREC 300x250"  → zonas 1001, 1002, 1004
--     - "Hojex House Leaderboard"   → zona  1003
--
-- Aplicação: como superutilizador/admin (bypass RLS), DEPOIS das migrations do
-- schema config (0001..0003) e de dev_roles.sql. Assume schema recém-migrado
-- (mesmo contrato que dev_seed.sql). float PROIBIDO em colunas monetárias
-- (CA-7/TX-2): rate house = 0 (sem receita).
--
-- Âncora: Plano AdServer §2 E11; Espec §4.6; web/src/lib/ads/config.ts (ZONES).
-- =============================================================================

\set hojex_tenant 'a0000000-0000-4000-8000-000000000001'

BEGIN;

-- ---------------------------------------------------------------------------
-- Publisher first-party (supply) + house advertiser (demand própria).
-- ---------------------------------------------------------------------------
INSERT INTO config.advertisers (tenant_id, name, login_username, login_password_hash)
VALUES (:'hojex_tenant', 'Hojex House', 'hojex-house',
        '$2y$10$housePLACEHOLDERhashhousePLACEHOLDERhashhou');

INSERT INTO config.sites (tenant_id, name, url)
VALUES (:'hojex_tenant', 'Hojex News', 'https://hojex.news');

-- ---------------------------------------------------------------------------
-- Zonas reais por-placement — IDs FIXADOS (contrato com o site).
-- ---------------------------------------------------------------------------
INSERT INTO config.zones (id, tenant_id, site_id, name, width, height, type)
SELECT z.id, :'hojex_tenant', s.id, z.name, z.width, z.height, 'standard'
FROM   config.sites s
CROSS  JOIN (VALUES
        (1001::bigint, 'Hojex Sidebar 300x250',     300, 250),
        (1002::bigint, 'Hojex In-Article 300x250',  300, 250),
        (1003::bigint, 'Hojex Leaderboard 728x90',   728,  90),
        (1004::bigint, 'Hojex Listing 300x250',      300, 250)
       ) AS z(id, name, width, height)
WHERE  s.name = 'Hojex News'
ON CONFLICT (id) DO NOTHING;

-- Sequência à frente dos IDs fixados: inserts serial futuros (zonas do console)
-- não colidem com o bloco reservado 1001..1004.
SELECT setval(pg_get_serial_sequence('config.zones', 'id'), 1004, true);

-- ---------------------------------------------------------------------------
-- Campanhas house (REMNANT, sempre-on) — uma por tamanho de criativo.
-- ---------------------------------------------------------------------------
INSERT INTO config.campaigns
    (tenant_id, advertiser_id, name, type, priority, goal_target, goal_metric,
     start_at, end_at, pricing_model, rate, currency)
SELECT :'hojex_tenant', a.id, 'Hojex House MREC 300x250', 'remnant', 10,
       1000000000, 'impressions',
       now() - INTERVAL '1 day', now() + INTERVAL '3650 days', 'cpm', 0.00, 'BRL'
FROM   config.advertisers a
WHERE  a.name = 'Hojex House';

INSERT INTO config.campaigns
    (tenant_id, advertiser_id, name, type, priority, goal_target, goal_metric,
     start_at, end_at, pricing_model, rate, currency)
SELECT :'hojex_tenant', a.id, 'Hojex House Leaderboard 728x90', 'remnant', 10,
       1000000000, 'impressions',
       now() - INTERVAL '1 day', now() + INTERVAL '3650 days', 'cpm', 0.00, 'BRL'
FROM   config.advertisers a
WHERE  a.name = 'Hojex House';

-- ---------------------------------------------------------------------------
-- Criativos house (image) — um por tamanho, apontam para o próprio site.
-- ---------------------------------------------------------------------------
INSERT INTO config.banners
    (tenant_id, campaign_id, name, creative_type, asset_url, dest_url, width, height)
SELECT :'hojex_tenant', c.id, 'House MREC 300x250', 'image',
       'https://hojex.news/ad-house-300x250.svg', 'https://hojex.news',
       300, 250
FROM   config.campaigns c
WHERE  c.name = 'Hojex House MREC 300x250';

INSERT INTO config.banners
    (tenant_id, campaign_id, name, creative_type, asset_url, dest_url, width, height)
SELECT :'hojex_tenant', c.id, 'House Leaderboard 728x90', 'image',
       'https://hojex.news/ad-house-728x90.svg', 'https://hojex.news',
       728, 90
FROM   config.campaigns c
WHERE  c.name = 'Hojex House Leaderboard 728x90';

-- ---------------------------------------------------------------------------
-- Vínculos N:N campanha↔zona (DA-2). Cada zona recebe a campanha house do SEU
-- tamanho — a cascata seleciona o 1.º banner elegível (sem casar dimensão), por
-- isso o casamento tamanho↔zona é garantido AQUI, não na cascata.
-- ---------------------------------------------------------------------------
-- MREC 300x250 → sidebar (1001), in-article (1002), listing (1004).
INSERT INTO config.campaign_zones (campaign_id, zone_id)
SELECT c.id, z.id
FROM   config.campaigns c
JOIN   config.zones     z ON z.id IN (1001, 1002, 1004)
WHERE  c.name = 'Hojex House MREC 300x250';

-- Leaderboard 728x90 → leaderboard (1003).
INSERT INTO config.campaign_zones (campaign_id, zone_id)
SELECT c.id, z.id
FROM   config.campaigns c
JOIN   config.zones     z ON z.id = 1003
WHERE  c.name = 'Hojex House Leaderboard 728x90';

COMMIT;
