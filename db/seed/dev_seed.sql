-- =============================================================================
-- db/seed/dev_seed.sql — DEV/LOCAL demo config for one tenant.
--
-- Gives the decision engine something real to serve so the local stack and
-- the integration smoke return a non-blank ad.  FKs are wired by natural key
-- (name) via SELECT so the script does not depend on serial id values.
--
-- Demo cascade for zone "Sidebar 300x250":
--   - "Contract BR"  (contract tier) — banner gated by Geo-Country IS BR.
--   - "Remnant House" (remnant tier)  — always eligible (house fallback).
-- A BR request serves the contract banner (contract tier wins); a non-BR
-- request falls through to the remnant house banner.  This exercises the
-- cascade (DA-3), the N:N campaign↔zone link (DA-2), and a delivery rule (§4.6).
--
-- Idempotent-ish: assumes a freshly migrated, empty config schema.
-- =============================================================================

\set tenant '11111111-1111-1111-1111-111111111111'

BEGIN;

INSERT INTO config.advertisers (tenant_id, name, login_username, login_password_hash)
VALUES (:'tenant', 'Demo Advertiser', 'demo', '$2y$10$devplaceholderhashdevplaceholderhashdevpl');

INSERT INTO config.sites (tenant_id, name, url)
VALUES (:'tenant', 'Demo Site', 'https://pub.example');

INSERT INTO config.zones (tenant_id, site_id, name, width, height, type)
SELECT :'tenant', s.id, 'Sidebar 300x250', 300, 250, 'standard'
FROM   config.sites s
WHERE  s.name = 'Demo Site';

-- Contract campaign (geo-targeted to BR).
INSERT INTO config.campaigns
    (tenant_id, advertiser_id, name, type, priority, goal_target, goal_metric,
     start_at, end_at, pricing_model, rate, currency)
SELECT :'tenant', a.id, 'Contract BR', 'contract', 3, 100000, 'impressions',
       now() - INTERVAL '1 day', now() + INTERVAL '30 days', 'cpm', 20.00, 'BRL'
FROM   config.advertisers a
WHERE  a.name = 'Demo Advertiser';

-- Remnant house campaign (always-on fallback).
INSERT INTO config.campaigns
    (tenant_id, advertiser_id, name, type, priority, goal_target, goal_metric,
     start_at, end_at, pricing_model, rate, currency)
SELECT :'tenant', a.id, 'Remnant House', 'remnant', 8, 1000000, 'impressions',
       now() - INTERVAL '1 day', now() + INTERVAL '365 days', 'cpm', 1.50, 'BRL'
FROM   config.advertisers a
WHERE  a.name = 'Demo Advertiser';

-- Banners.
INSERT INTO config.banners
    (tenant_id, campaign_id, name, creative_type, asset_url, dest_url, width, height)
SELECT :'tenant', c.id, 'Contract Banner', 'image',
       'https://cdn.example/contract-300x250.png', 'https://advertiser.example/landing',
       300, 250
FROM   config.campaigns c
WHERE  c.name = 'Contract BR';

INSERT INTO config.banners
    (tenant_id, campaign_id, name, creative_type, asset_url, dest_url, width, height)
SELECT :'tenant', c.id, 'House Banner', 'image',
       'https://cdn.example/house-300x250.png', 'https://pub.example/house',
       300, 250
FROM   config.campaigns c
WHERE  c.name = 'Remnant House';

-- N:N link: both campaigns compete for the sidebar zone.
INSERT INTO config.campaign_zones (campaign_id, zone_id)
SELECT c.id, z.id
FROM   config.campaigns c
JOIN   config.zones z ON z.name = 'Sidebar 300x250'
WHERE  c.name IN ('Contract BR', 'Remnant House');

-- Delivery rule (§4.6): the contract banner only serves in BR.
INSERT INTO config.delivery_rules
    (tenant_id, owner_type, owner_id, vector, operator, value, logical_op)
SELECT :'tenant', 'banner', b.id, 'Geo - Country', 'is', 'BR', 'AND'
FROM   config.banners b
WHERE  b.name = 'Contract Banner';

-- Frequency cap (§4.8 / DA-6): 3 impressions per user per hour on the contract.
INSERT INTO config.caps
    (tenant_id, owner_type, owner_id, scope, limit_count, reset_interval)
SELECT :'tenant', 'campaign', c.id, 'clock', 3, 3600
FROM   config.campaigns c
WHERE  c.name = 'Contract BR';

COMMIT;
