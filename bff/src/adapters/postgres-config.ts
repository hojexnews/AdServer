/**
 * PostgresConfigAdapter — implementação real de ConfigAdapter (I4 → produção).
 *
 * Substitui o InMemoryConfigAdapter quando BFF_PG_DSN está configurado
 * (mesma seleção-por-config do PostgresPaymentsAdapter). As mutações escrevem
 * no schema `config` do Postgres — o MESMO que o motor de decisão (Go) lê no
 * seu snapshot — fechando o laço "administrar no console → servir no site".
 *
 * SEGURANÇA (TX-3 / CA-1):
 *   Toda query roda DENTRO de uma transação que primeiro injeta o tenant:
 *     SELECT set_config('adserver.tenant_id', $1, true)
 *   ativando o RLS de 0002_config_rls_up.sql. O tenantId vem SEMPRE do ctx
 *   da sessão (context.ts), NUNCA de parâmetros do cliente. INSERTs gravam
 *   tenant_id explícito (coluna NOT NULL) coincidindo com a policy WITH CHECK.
 *
 * Money (TX-2 / DA-10):
 *   campaign.rate é NUMERIC — lido como string (`rate::text`) e entregue via
 *   pgNumericToWire. NUNCA Number() para dinheiro.
 *
 * IDs (DA-10): BIGSERIAL chega do driver pg como string; mantido como string
 *   no fio (IdSchema). goal_target/limit_count (BIGINT) são contagens pequenas
 *   e são convertidas para number (contrato dos schemas).
 */

import { randomBytes, scryptSync } from "node:crypto";
import { TRPCError } from "@trpc/server";
import pg from "pg";
import type { Pool, PoolClient } from "pg";
import type { ConfigAdapter } from "./config-adapter.js";
import { pgNumericToWire } from "../schemas/money.js";
import type {
  Advertiser,
  CreateAdvertiserInput,
  UpdateAdvertiserInput,
  Campaign,
  CreateCampaignInput,
  UpdateCampaignInput,
  Banner,
  CreateBannerInput,
  UpdateBannerInput,
  Site,
  CreateSiteInput,
  UpdateSiteInput,
  Zone,
  CreateZoneInput,
  UpdateZoneInput,
  CampaignZone,
  DeliveryRule,
  CreateDeliveryRuleInput,
  UpdateDeliveryRuleInput,
  DeliveryRuleSet,
  Cap,
  CreateCapInput,
  UpdateCapInput,
} from "../schemas/config.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Cria um Pool a partir de um DSN (para uso standalone se necessário). */
export function createConfigPool(dsn: string): Pool {
  return new pg.Pool({ connectionString: dsn, max: 10 });
}

function notFound(entity: string, id: string): never {
  throw new TRPCError({ code: "NOT_FOUND", message: `${entity} #${id} não encontrado` });
}

/** TIMESTAMPTZ (Date do driver pg) → string ISO 8601 (contrato dos schemas). */
function toIso(v: Date | string): string {
  return v instanceof Date ? v.toISOString() : new Date(v).toISOString();
}

/** BIGINT (string do driver) → number; contagens pequenas (goal_target/limit_count). */
function toNum(v: string | number): number {
  return typeof v === "number" ? v : Number(v);
}
function toNumOrNull(v: string | number | null): number | null {
  return v === null ? null : toNum(v);
}

/**
 * Hash da senha do anunciante (login do painel). Sem dependência de bcrypt no
 * BFF: scrypt do core Node, formato `scrypt$<saltHex>$<hashHex>`. PII mínimo
 * (DA-11): só username + hash, nunca plaintext.
 */
function hashPassword(pw: string): string {
  const salt = randomBytes(16);
  const dk = scryptSync(pw, salt, 32);
  return `scrypt$${salt.toString("hex")}$${dk.toString("hex")}`;
}

/**
 * Monta o fragmento SET de um UPDATE parcial a partir de pares [coluna, valor].
 * Pula valores `undefined` (campo ausente); mantém `null` (limpar campo).
 * Placeholders começam em $startIdx (o $1 fica reservado para o id no WHERE).
 */
function buildSet(
  entries: ReadonlyArray<readonly [string, unknown]>,
  startIdx: number
): { sql: string; vals: unknown[] } {
  const parts: string[] = [];
  const vals: unknown[] = [];
  let i = startIdx;
  for (const [col, val] of entries) {
    if (val !== undefined) {
      parts.push(`${col} = $${i}`);
      vals.push(val);
      i += 1;
    }
  }
  return { sql: parts.join(", "), vals };
}

// ---------------------------------------------------------------------------
// Row shapes (snake_case do Postgres) + mappers para o contrato camelCase
// ---------------------------------------------------------------------------

interface AdvertiserRow {
  id: string; name: string; login_username: string;
  is_network: boolean; active: boolean; created_at: Date; updated_at: Date;
}
const ADVERTISER_COLS =
  "id, name, login_username, is_network, active, created_at, updated_at";
function mapAdvertiser(r: AdvertiserRow): Advertiser {
  return {
    id: r.id, name: r.name, loginUsername: r.login_username,
    isNetwork: r.is_network, active: r.active,
    createdAt: toIso(r.created_at), updatedAt: toIso(r.updated_at),
  };
}

interface CampaignRow {
  id: string; advertiser_id: string; name: string; type: string;
  priority: number; goal_target: string | null; goal_metric: string | null;
  start_at: Date; end_at: Date; pricing_model: string; rate: string;
  currency: string; active: boolean; created_at: Date; updated_at: Date;
}
const CAMPAIGN_COLS =
  "id, advertiser_id, name, type, priority, goal_target, goal_metric, " +
  "start_at, end_at, pricing_model, rate::text AS rate, currency, active, created_at, updated_at";
function mapCampaign(r: CampaignRow): Campaign {
  return {
    id: r.id, advertiserId: r.advertiser_id, name: r.name,
    type: r.type as Campaign["type"], priority: toNum(r.priority),
    goalTarget: toNumOrNull(r.goal_target),
    goalMetric: r.goal_metric as Campaign["goalMetric"],
    startAt: toIso(r.start_at), endAt: toIso(r.end_at),
    pricingModel: r.pricing_model as Campaign["pricingModel"],
    rate: pgNumericToWire(r.rate, r.currency),
    active: r.active, createdAt: toIso(r.created_at), updatedAt: toIso(r.updated_at),
  };
}

interface BannerRow {
  id: string; campaign_id: string; name: string; creative_type: string;
  asset_url: string | null; dest_url: string | null; width: number; height: number;
  active: boolean; created_at: Date; updated_at: Date;
}
const BANNER_COLS =
  "id, campaign_id, name, creative_type, asset_url, dest_url, width, height, active, created_at, updated_at";
function mapBanner(r: BannerRow): Banner {
  return {
    id: r.id, campaignId: r.campaign_id, name: r.name,
    creativeType: r.creative_type as Banner["creativeType"],
    assetUrl: r.asset_url, destUrl: r.dest_url,
    width: toNum(r.width), height: toNum(r.height),
    active: r.active, createdAt: toIso(r.created_at), updatedAt: toIso(r.updated_at),
  };
}

interface SiteRow {
  id: string; name: string; url: string; active: boolean;
  created_at: Date; updated_at: Date;
}
const SITE_COLS = "id, name, url, active, created_at, updated_at";
function mapSite(r: SiteRow): Site {
  return {
    id: r.id, name: r.name, url: r.url, active: r.active,
    createdAt: toIso(r.created_at), updatedAt: toIso(r.updated_at),
  };
}

interface ZoneRow {
  id: string; site_id: string; name: string; width: number; height: number;
  type: string; active: boolean; created_at: Date; updated_at: Date;
}
const ZONE_COLS = "id, site_id, name, width, height, type, active, created_at, updated_at";
function mapZone(r: ZoneRow): Zone {
  return {
    id: r.id, siteId: r.site_id, name: r.name,
    width: toNum(r.width), height: toNum(r.height), type: r.type,
    active: r.active, createdAt: toIso(r.created_at), updatedAt: toIso(r.updated_at),
  };
}

interface CampaignZoneRow { campaign_id: string; zone_id: string; created_at: Date; }
function mapCampaignZone(r: CampaignZoneRow): CampaignZone {
  return { campaignId: r.campaign_id, zoneId: r.zone_id, createdAt: toIso(r.created_at) };
}

interface DeliveryRuleRow {
  id: string; owner_type: string; owner_id: string; vector: string;
  operator: string; value: string; logical_op: string; rule_set_id: string | null;
  order_seq: number; active: boolean; created_at: Date; updated_at: Date;
}
const DELIVERY_RULE_COLS =
  "id, owner_type, owner_id, vector, operator, value, logical_op, rule_set_id, order_seq, active, created_at, updated_at";
function mapDeliveryRule(r: DeliveryRuleRow): DeliveryRule {
  return {
    id: r.id, ownerType: r.owner_type as DeliveryRule["ownerType"], ownerId: r.owner_id,
    vector: r.vector as DeliveryRule["vector"], operator: r.operator as DeliveryRule["operator"],
    value: r.value, logicalOp: r.logical_op as DeliveryRule["logicalOp"],
    ruleSetId: r.rule_set_id, orderSeq: toNum(r.order_seq), active: r.active,
    createdAt: toIso(r.created_at), updatedAt: toIso(r.updated_at),
  };
}

interface DeliveryRuleSetRow {
  id: string; name: string; description: string | null; created_at: Date; updated_at: Date;
}
const RULE_SET_COLS = "id, name, description, created_at, updated_at";
function mapDeliveryRuleSet(r: DeliveryRuleSetRow): DeliveryRuleSet {
  return {
    id: r.id, name: r.name, description: r.description,
    createdAt: toIso(r.created_at), updatedAt: toIso(r.updated_at),
  };
}

interface CapRow {
  id: string; owner_type: string; owner_id: string; scope: string;
  limit_count: string; reset_interval: number | null; active: boolean;
  created_at: Date; updated_at: Date;
}
const CAP_COLS =
  "id, owner_type, owner_id, scope, limit_count, reset_interval, active, created_at, updated_at";
function mapCap(r: CapRow): Cap {
  return {
    id: r.id, ownerType: r.owner_type as Cap["ownerType"], ownerId: r.owner_id,
    scope: r.scope as Cap["scope"], limitCount: toNum(r.limit_count),
    resetInterval: toNumOrNull(r.reset_interval), active: r.active,
    createdAt: toIso(r.created_at), updatedAt: toIso(r.updated_at),
  };
}

// ---------------------------------------------------------------------------
// PostgresConfigAdapter
// ---------------------------------------------------------------------------

export class PostgresConfigAdapter implements ConfigAdapter {
  private readonly pool: Pool;

  constructor(pool: Pool) {
    this.pool = pool;
  }

  /**
   * Executa `fn` numa transação com o tenant injetado (RLS ativo). O tenantId
   * NUNCA vem do cliente — é o ctx da sessão. set_config(..., true) = escopo
   * de transação (SET LOCAL parametrizável).
   */
  private async withTenant<T>(
    tenantId: string,
    fn: (c: PoolClient) => Promise<T>
  ): Promise<T> {
    const client = await this.pool.connect();
    try {
      await client.query("BEGIN");
      await client.query("SELECT set_config('adserver.tenant_id', $1, true)", [tenantId]);
      const out = await fn(client);
      await client.query("COMMIT");
      return out;
    } catch (err) {
      await client.query("ROLLBACK").catch(() => undefined);
      throw err;
    } finally {
      client.release();
    }
  }

  /**
   * Fecha escrita cross-tenant por referência a um PAI forjado. O RLS garante
   * que a LINHA gravada tem o tenant_id do ctx (WITH CHECK), mas NÃO restringe
   * os owner_id/parent_id que ela referencia: caps/rules têm owner polimórfico
   * SEM FK, e a FK de banner/zone/campaign IGNORA o RLS. Sem esta guarda, o
   * tenant A poderia gravar cap/banner/rule apontando para a campanha/zona do
   * tenant B, e o loader BYPASSRLS (internal/configload) aplicaria a config à
   * vítima (ex.: cap total=1 → DoS silencioso). Roda DENTRO da transação
   * withTenant (RLS ativo) e espelha o INSERT...SELECT filtrado de
   * linkCampaignZones. `table` é SEMPRE um literal de código (nunca entrada do
   * cliente) — sem superfície de injeção.
   */
  private async assertParentVisible(
    c: PoolClient,
    table: "advertisers" | "campaigns" | "sites" | "banners" | "delivery_rule_sets",
    id: string,
    label: string
  ): Promise<void> {
    const r = await c.query(`SELECT 1 FROM config.${table} WHERE id = $1`, [id]);
    if ((r.rowCount ?? 0) === 0) {
      throw new TRPCError({
        code: "NOT_FOUND",
        message: `${label} #${id} não encontrado (inexistente ou de outro tenant)`,
      });
    }
  }

  // ---- Advertiser -----------------------------------------------------------

  listAdvertisers(tenantId: string): Promise<Advertiser[]> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<AdvertiserRow>(
        `SELECT ${ADVERTISER_COLS} FROM config.advertisers ORDER BY id`
      );
      return r.rows.map(mapAdvertiser);
    });
  }

  getAdvertiser(tenantId: string, id: string): Promise<Advertiser | null> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<AdvertiserRow>(
        `SELECT ${ADVERTISER_COLS} FROM config.advertisers WHERE id = $1`, [id]
      );
      return r.rows[0] ? mapAdvertiser(r.rows[0]) : null;
    });
  }

  createAdvertiser(tenantId: string, input: CreateAdvertiserInput): Promise<Advertiser> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<AdvertiserRow>(
        `INSERT INTO config.advertisers
           (tenant_id, name, login_username, login_password_hash, is_network)
         VALUES ($1, $2, $3, $4, $5)
         RETURNING ${ADVERTISER_COLS}`,
        [tenantId, input.name, input.loginUsername, hashPassword(input.loginPassword), input.isNetwork]
      );
      return mapAdvertiser(r.rows[0]!);
    });
  }

  updateAdvertiser(tenantId: string, input: UpdateAdvertiserInput): Promise<Advertiser> {
    return this.withTenant(tenantId, async (c) => {
      const set = buildSet(
        [
          ["name", input.name],
          ["login_username", input.loginUsername],
          ["login_password_hash", input.loginPassword === undefined ? undefined : hashPassword(input.loginPassword)],
          ["is_network", input.isNetwork],
          ["active", input.active],
        ],
        2
      );
      const assignments = set.sql ? `${set.sql}, updated_at = now()` : "updated_at = now()";
      const r = await c.query<AdvertiserRow>(
        `UPDATE config.advertisers SET ${assignments} WHERE id = $1 RETURNING ${ADVERTISER_COLS}`,
        [input.id, ...set.vals]
      );
      if ((r.rowCount ?? 0) === 0) notFound("Advertiser", input.id);
      return mapAdvertiser(r.rows[0]!);
    });
  }

  deleteAdvertiser(tenantId: string, id: string): Promise<void> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query(`DELETE FROM config.advertisers WHERE id = $1`, [id]);
      if ((r.rowCount ?? 0) === 0) notFound("Advertiser", id);
    });
  }

  // ---- Campaign -------------------------------------------------------------

  listCampaigns(tenantId: string, opts?: { advertiserId?: string }): Promise<Campaign[]> {
    return this.withTenant(tenantId, async (c) => {
      const where = opts?.advertiserId ? "WHERE advertiser_id = $1" : "";
      const params = opts?.advertiserId ? [opts.advertiserId] : [];
      const r = await c.query<CampaignRow>(
        `SELECT ${CAMPAIGN_COLS} FROM config.campaigns ${where} ORDER BY id`, params
      );
      return r.rows.map(mapCampaign);
    });
  }

  getCampaign(tenantId: string, id: string): Promise<Campaign | null> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<CampaignRow>(
        `SELECT ${CAMPAIGN_COLS} FROM config.campaigns WHERE id = $1`, [id]
      );
      return r.rows[0] ? mapCampaign(r.rows[0]) : null;
    });
  }

  createCampaign(tenantId: string, input: CreateCampaignInput): Promise<Campaign> {
    return this.withTenant(tenantId, async (c) => {
      await this.assertParentVisible(c, "advertisers", input.advertiserId, "Advertiser");
      const r = await c.query<CampaignRow>(
        `INSERT INTO config.campaigns
           (tenant_id, advertiser_id, name, type, priority, goal_target, goal_metric,
            start_at, end_at, pricing_model, rate, currency)
         VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
         RETURNING ${CAMPAIGN_COLS}`,
        [
          tenantId, input.advertiserId, input.name, input.type, input.priority,
          input.goalTarget, input.goalMetric, input.startAt, input.endAt,
          input.pricingModel, input.rateAmount, input.rateCurrency,
        ]
      );
      return mapCampaign(r.rows[0]!);
    });
  }

  updateCampaign(tenantId: string, input: UpdateCampaignInput): Promise<Campaign> {
    return this.withTenant(tenantId, async (c) => {
      // rate + currency são atualizados em par (money atômico — TX-2/DA-10).
      const bothRate = input.rateAmount !== undefined && input.rateCurrency !== undefined;
      const set = buildSet(
        [
          ["name", input.name],
          ["type", input.type],
          ["priority", input.priority],
          ["goal_target", input.goalTarget],
          ["goal_metric", input.goalMetric],
          ["start_at", input.startAt],
          ["end_at", input.endAt],
          ["pricing_model", input.pricingModel],
          ["rate", bothRate ? input.rateAmount : undefined],
          ["currency", bothRate ? input.rateCurrency : undefined],
          ["active", input.active],
        ],
        2
      );
      const assignments = set.sql ? `${set.sql}, updated_at = now()` : "updated_at = now()";
      const r = await c.query<CampaignRow>(
        `UPDATE config.campaigns SET ${assignments} WHERE id = $1 RETURNING ${CAMPAIGN_COLS}`,
        [input.id, ...set.vals]
      );
      if ((r.rowCount ?? 0) === 0) notFound("Campaign", input.id);
      return mapCampaign(r.rows[0]!);
    });
  }

  deleteCampaign(tenantId: string, id: string): Promise<void> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query(`DELETE FROM config.campaigns WHERE id = $1`, [id]);
      if ((r.rowCount ?? 0) === 0) notFound("Campaign", id);
    });
  }

  // ---- Banner ---------------------------------------------------------------

  listBanners(tenantId: string, opts?: { campaignId?: string }): Promise<Banner[]> {
    return this.withTenant(tenantId, async (c) => {
      const where = opts?.campaignId ? "WHERE campaign_id = $1" : "";
      const params = opts?.campaignId ? [opts.campaignId] : [];
      const r = await c.query<BannerRow>(
        `SELECT ${BANNER_COLS} FROM config.banners ${where} ORDER BY id`, params
      );
      return r.rows.map(mapBanner);
    });
  }

  getBanner(tenantId: string, id: string): Promise<Banner | null> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<BannerRow>(
        `SELECT ${BANNER_COLS} FROM config.banners WHERE id = $1`, [id]
      );
      return r.rows[0] ? mapBanner(r.rows[0]) : null;
    });
  }

  createBanner(tenantId: string, input: CreateBannerInput): Promise<Banner> {
    return this.withTenant(tenantId, async (c) => {
      await this.assertParentVisible(c, "campaigns", input.campaignId, "Campaign");
      const r = await c.query<BannerRow>(
        `INSERT INTO config.banners
           (tenant_id, campaign_id, name, creative_type, asset_url, dest_url, width, height)
         VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
         RETURNING ${BANNER_COLS}`,
        [
          tenantId, input.campaignId, input.name, input.creativeType,
          input.assetUrl, input.destUrl, input.width, input.height,
        ]
      );
      return mapBanner(r.rows[0]!);
    });
  }

  updateBanner(tenantId: string, input: UpdateBannerInput): Promise<Banner> {
    return this.withTenant(tenantId, async (c) => {
      const set = buildSet(
        [
          ["name", input.name],
          ["asset_url", input.assetUrl],
          ["dest_url", input.destUrl],
          ["active", input.active],
        ],
        2
      );
      const assignments = set.sql ? `${set.sql}, updated_at = now()` : "updated_at = now()";
      const r = await c.query<BannerRow>(
        `UPDATE config.banners SET ${assignments} WHERE id = $1 RETURNING ${BANNER_COLS}`,
        [input.id, ...set.vals]
      );
      if ((r.rowCount ?? 0) === 0) notFound("Banner", input.id);
      return mapBanner(r.rows[0]!);
    });
  }

  deleteBanner(tenantId: string, id: string): Promise<void> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query(`DELETE FROM config.banners WHERE id = $1`, [id]);
      if ((r.rowCount ?? 0) === 0) notFound("Banner", id);
    });
  }

  // ---- Site -----------------------------------------------------------------

  listSites(tenantId: string): Promise<Site[]> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<SiteRow>(`SELECT ${SITE_COLS} FROM config.sites ORDER BY id`);
      return r.rows.map(mapSite);
    });
  }

  getSite(tenantId: string, id: string): Promise<Site | null> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<SiteRow>(`SELECT ${SITE_COLS} FROM config.sites WHERE id = $1`, [id]);
      return r.rows[0] ? mapSite(r.rows[0]) : null;
    });
  }

  createSite(tenantId: string, input: CreateSiteInput): Promise<Site> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<SiteRow>(
        `INSERT INTO config.sites (tenant_id, name, url) VALUES ($1,$2,$3) RETURNING ${SITE_COLS}`,
        [tenantId, input.name, input.url]
      );
      return mapSite(r.rows[0]!);
    });
  }

  updateSite(tenantId: string, input: UpdateSiteInput): Promise<Site> {
    return this.withTenant(tenantId, async (c) => {
      const set = buildSet(
        [["name", input.name], ["url", input.url], ["active", input.active]], 2
      );
      const assignments = set.sql ? `${set.sql}, updated_at = now()` : "updated_at = now()";
      const r = await c.query<SiteRow>(
        `UPDATE config.sites SET ${assignments} WHERE id = $1 RETURNING ${SITE_COLS}`,
        [input.id, ...set.vals]
      );
      if ((r.rowCount ?? 0) === 0) notFound("Site", input.id);
      return mapSite(r.rows[0]!);
    });
  }

  deleteSite(tenantId: string, id: string): Promise<void> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query(`DELETE FROM config.sites WHERE id = $1`, [id]);
      if ((r.rowCount ?? 0) === 0) notFound("Site", id);
    });
  }

  // ---- Zone -----------------------------------------------------------------

  listZones(tenantId: string, opts?: { siteId?: string }): Promise<Zone[]> {
    return this.withTenant(tenantId, async (c) => {
      const where = opts?.siteId ? "WHERE site_id = $1" : "";
      const params = opts?.siteId ? [opts.siteId] : [];
      const r = await c.query<ZoneRow>(
        `SELECT ${ZONE_COLS} FROM config.zones ${where} ORDER BY id`, params
      );
      return r.rows.map(mapZone);
    });
  }

  getZone(tenantId: string, id: string): Promise<Zone | null> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<ZoneRow>(`SELECT ${ZONE_COLS} FROM config.zones WHERE id = $1`, [id]);
      return r.rows[0] ? mapZone(r.rows[0]) : null;
    });
  }

  createZone(tenantId: string, input: CreateZoneInput): Promise<Zone> {
    return this.withTenant(tenantId, async (c) => {
      await this.assertParentVisible(c, "sites", input.siteId, "Site");
      const r = await c.query<ZoneRow>(
        `INSERT INTO config.zones (tenant_id, site_id, name, width, height, type)
         VALUES ($1,$2,$3,$4,$5,$6) RETURNING ${ZONE_COLS}`,
        [tenantId, input.siteId, input.name, input.width, input.height, input.type]
      );
      return mapZone(r.rows[0]!);
    });
  }

  updateZone(tenantId: string, input: UpdateZoneInput): Promise<Zone> {
    return this.withTenant(tenantId, async (c) => {
      const set = buildSet(
        [
          ["name", input.name], ["width", input.width], ["height", input.height],
          ["type", input.type], ["active", input.active],
        ],
        2
      );
      const assignments = set.sql ? `${set.sql}, updated_at = now()` : "updated_at = now()";
      const r = await c.query<ZoneRow>(
        `UPDATE config.zones SET ${assignments} WHERE id = $1 RETURNING ${ZONE_COLS}`,
        [input.id, ...set.vals]
      );
      if ((r.rowCount ?? 0) === 0) notFound("Zone", input.id);
      return mapZone(r.rows[0]!);
    });
  }

  deleteZone(tenantId: string, id: string): Promise<void> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query(`DELETE FROM config.zones WHERE id = $1`, [id]);
      if ((r.rowCount ?? 0) === 0) notFound("Zone", id);
    });
  }

  // ---- CampaignZone (N:N — DA-2) --------------------------------------------
  // campaign_zones não tem RLS própria: garantimos o isolamento vinculando só
  // campanhas/zonas que o RLS de config.campaigns/config.zones deixa ver.

  listCampaignZones(tenantId: string, campaignId: string): Promise<CampaignZone[]> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<CampaignZoneRow>(
        `SELECT cz.campaign_id, cz.zone_id, cz.created_at
           FROM config.campaign_zones cz
          WHERE cz.campaign_id = $1
            AND cz.campaign_id IN (SELECT id FROM config.campaigns)
          ORDER BY cz.zone_id`,
        [campaignId]
      );
      return r.rows.map(mapCampaignZone);
    });
  }

  linkCampaignZones(tenantId: string, campaignId: string, zoneIds: string[]): Promise<CampaignZone[]> {
    return this.withTenant(tenantId, async (c) => {
      // INSERT...SELECT filtrado pelo RLS: só resolve campanha/zonas do tenant.
      const r = await c.query<CampaignZoneRow>(
        `INSERT INTO config.campaign_zones (campaign_id, zone_id)
         SELECT c.id, z.id
           FROM config.campaigns c
           JOIN config.zones z ON z.id = ANY($2::bigint[])
          WHERE c.id = $1
         ON CONFLICT (campaign_id, zone_id) DO NOTHING
         RETURNING campaign_id, zone_id, created_at`,
        [campaignId, zoneIds]
      );
      return r.rows.map(mapCampaignZone);
    });
  }

  unlinkCampaignZones(tenantId: string, campaignId: string, zoneIds: string[]): Promise<void> {
    return this.withTenant(tenantId, async (c) => {
      await c.query(
        `DELETE FROM config.campaign_zones
          WHERE campaign_id = $1 AND zone_id = ANY($2::bigint[])
            AND campaign_id IN (SELECT id FROM config.campaigns)`,
        [campaignId, zoneIds]
      );
    });
  }

  // ---- DeliveryRule (§4.6) --------------------------------------------------

  listDeliveryRules(
    tenantId: string,
    opts: { ownerType: "campaign" | "banner"; ownerId: string }
  ): Promise<DeliveryRule[]> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<DeliveryRuleRow>(
        `SELECT ${DELIVERY_RULE_COLS} FROM config.delivery_rules
          WHERE owner_type = $1 AND owner_id = $2 ORDER BY order_seq, id`,
        [opts.ownerType, opts.ownerId]
      );
      return r.rows.map(mapDeliveryRule);
    });
  }

  getDeliveryRule(tenantId: string, id: string): Promise<DeliveryRule | null> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<DeliveryRuleRow>(
        `SELECT ${DELIVERY_RULE_COLS} FROM config.delivery_rules WHERE id = $1`, [id]
      );
      return r.rows[0] ? mapDeliveryRule(r.rows[0]) : null;
    });
  }

  createDeliveryRule(tenantId: string, input: CreateDeliveryRuleInput): Promise<DeliveryRule> {
    return this.withTenant(tenantId, async (c) => {
      // owner polimórfico (campaign|banner) SEM FK; rule_set_id tem FK que ignora
      // o RLS — ambos precisam ser visíveis ao tenant corrente.
      await this.assertParentVisible(
        c,
        input.ownerType === "campaign" ? "campaigns" : "banners",
        input.ownerId,
        input.ownerType === "campaign" ? "Campaign" : "Banner"
      );
      if (input.ruleSetId !== null && input.ruleSetId !== undefined) {
        await this.assertParentVisible(c, "delivery_rule_sets", input.ruleSetId, "DeliveryRuleSet");
      }
      const r = await c.query<DeliveryRuleRow>(
        `INSERT INTO config.delivery_rules
           (tenant_id, owner_type, owner_id, vector, operator, value, logical_op, rule_set_id, order_seq)
         VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
         RETURNING ${DELIVERY_RULE_COLS}`,
        [
          tenantId, input.ownerType, input.ownerId, input.vector, input.operator,
          input.value, input.logicalOp, input.ruleSetId, input.orderSeq,
        ]
      );
      return mapDeliveryRule(r.rows[0]!);
    });
  }

  updateDeliveryRule(tenantId: string, input: UpdateDeliveryRuleInput): Promise<DeliveryRule> {
    return this.withTenant(tenantId, async (c) => {
      const set = buildSet(
        [
          ["vector", input.vector], ["operator", input.operator], ["value", input.value],
          ["logical_op", input.logicalOp], ["order_seq", input.orderSeq], ["active", input.active],
        ],
        2
      );
      const assignments = set.sql ? `${set.sql}, updated_at = now()` : "updated_at = now()";
      const r = await c.query<DeliveryRuleRow>(
        `UPDATE config.delivery_rules SET ${assignments} WHERE id = $1 RETURNING ${DELIVERY_RULE_COLS}`,
        [input.id, ...set.vals]
      );
      if ((r.rowCount ?? 0) === 0) notFound("DeliveryRule", input.id);
      return mapDeliveryRule(r.rows[0]!);
    });
  }

  deleteDeliveryRule(tenantId: string, id: string): Promise<void> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query(`DELETE FROM config.delivery_rules WHERE id = $1`, [id]);
      if ((r.rowCount ?? 0) === 0) notFound("DeliveryRule", id);
    });
  }

  // ---- DeliveryRuleSet (§4.6) -----------------------------------------------

  listDeliveryRuleSets(tenantId: string): Promise<DeliveryRuleSet[]> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<DeliveryRuleSetRow>(
        `SELECT ${RULE_SET_COLS} FROM config.delivery_rule_sets ORDER BY id`
      );
      return r.rows.map(mapDeliveryRuleSet);
    });
  }

  createDeliveryRuleSet(
    tenantId: string,
    input: { name: string; description: string | null }
  ): Promise<DeliveryRuleSet> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<DeliveryRuleSetRow>(
        `INSERT INTO config.delivery_rule_sets (tenant_id, name, description)
         VALUES ($1,$2,$3) RETURNING ${RULE_SET_COLS}`,
        [tenantId, input.name, input.description]
      );
      return mapDeliveryRuleSet(r.rows[0]!);
    });
  }

  // ---- Cap (§4.8) -----------------------------------------------------------

  listCaps(
    tenantId: string,
    opts: { ownerType: "campaign" | "banner"; ownerId: string }
  ): Promise<Cap[]> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query<CapRow>(
        `SELECT ${CAP_COLS} FROM config.caps
          WHERE owner_type = $1 AND owner_id = $2 ORDER BY id`,
        [opts.ownerType, opts.ownerId]
      );
      return r.rows.map(mapCap);
    });
  }

  createCap(tenantId: string, input: CreateCapInput): Promise<Cap> {
    return this.withTenant(tenantId, async (c) => {
      // owner polimórfico (campaign|banner) SEM FK — a guarda é a única defesa.
      await this.assertParentVisible(
        c,
        input.ownerType === "campaign" ? "campaigns" : "banners",
        input.ownerId,
        input.ownerType === "campaign" ? "Campaign" : "Banner"
      );
      const r = await c.query<CapRow>(
        `INSERT INTO config.caps
           (tenant_id, owner_type, owner_id, scope, limit_count, reset_interval)
         VALUES ($1,$2,$3,$4,$5,$6) RETURNING ${CAP_COLS}`,
        [tenantId, input.ownerType, input.ownerId, input.scope, input.limitCount, input.resetInterval]
      );
      return mapCap(r.rows[0]!);
    });
  }

  updateCap(tenantId: string, input: UpdateCapInput): Promise<Cap> {
    return this.withTenant(tenantId, async (c) => {
      const set = buildSet(
        [
          ["limit_count", input.limitCount],
          ["reset_interval", input.resetInterval],
          ["active", input.active],
        ],
        2
      );
      const assignments = set.sql ? `${set.sql}, updated_at = now()` : "updated_at = now()";
      const r = await c.query<CapRow>(
        `UPDATE config.caps SET ${assignments} WHERE id = $1 RETURNING ${CAP_COLS}`,
        [input.id, ...set.vals]
      );
      if ((r.rowCount ?? 0) === 0) notFound("Cap", input.id);
      return mapCap(r.rows[0]!);
    });
  }

  deleteCap(tenantId: string, id: string): Promise<void> {
    return this.withTenant(tenantId, async (c) => {
      const r = await c.query(`DELETE FROM config.caps WHERE id = $1`, [id]);
      if ((r.rowCount ?? 0) === 0) notFound("Cap", id);
    });
  }
}
