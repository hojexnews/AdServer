/**
 * Adapter in-memory de configuração — STUB para Fase 1 (I4).
 *
 * Implementa ConfigAdapter com dados em memória para que typecheck/build
 * e testes de integração do BFF passem sem Postgres.
 *
 * ONDE PLUGAR O ADAPTER REAL:
 *   src/index.ts → substituir InMemoryConfigAdapter por PostgresConfigAdapter.
 *   O PostgresConfigAdapter deve:
 *     1. Abrir transação/conexão (BEGIN)
 *     2. Executar: await client.query("SELECT set_config('adserver.tenant_id', $1, true)", [tenantId])
 *        (NÃO usar `SET LOCAL ... = $1`: SET é utility statement e não aceita bind params)
 *     3. Executar as queries de dados (RLS do Postgres filtra por adserver.tenant_id)
 *     4. Encerrar transação (COMMIT/ROLLBACK)
 *
 * Money: rate é armazenado como string DECIMAL no stub.
 * Nunca use Number() para representar money.
 */

import { TRPCError } from "@trpc/server";
import type { ConfigAdapter } from "./config-adapter.js";
import type {
  Advertiser,
  Campaign,
  Banner,
  Site,
  Zone,
  CampaignZone,
  DeliveryRule,
  DeliveryRuleSet,
  Cap,
  CreateAdvertiserInput,
  UpdateAdvertiserInput,
  CreateCampaignInput,
  UpdateCampaignInput,
  CreateBannerInput,
  UpdateBannerInput,
  CreateSiteInput,
  UpdateSiteInput,
  CreateZoneInput,
  UpdateZoneInput,
  CreateDeliveryRuleInput,
  UpdateDeliveryRuleInput,
  CreateCapInput,
  UpdateCapInput,
} from "../schemas/config.js";

let seq = 1;
const nextId = (): string => String(seq++);
const now = (): string => new Date().toISOString();

// Stores segregados por tenant_id (chave = tenantId)
const stores = new Map<
  string,
  {
    advertisers: Advertiser[];
    campaigns: Campaign[];
    banners: Banner[];
    sites: Site[];
    zones: Zone[];
    campaignZones: CampaignZone[];
    deliveryRules: DeliveryRule[];
    deliveryRuleSets: DeliveryRuleSet[];
    caps: Cap[];
  }
>();

function getStore(tenantId: string) {
  if (!stores.has(tenantId)) {
    stores.set(tenantId, {
      advertisers: [],
      campaigns: [],
      banners: [],
      sites: [],
      zones: [],
      campaignZones: [],
      deliveryRules: [],
      deliveryRuleSets: [],
      caps: [],
    });
  }
  const store = stores.get(tenantId);
  if (!store) throw new Error("Invariant: store ausente após set");
  return store;
}

function notFound(entity: string, id: string): never {
  throw new TRPCError({
    code: "NOT_FOUND",
    message: `${entity} #${id} não encontrado`,
  });
}

export class InMemoryConfigAdapter implements ConfigAdapter {
  // ---------------------------------------------------------------------------
  // Advertiser
  // ---------------------------------------------------------------------------

  async listAdvertisers(tenantId: string): Promise<Advertiser[]> {
    return [...getStore(tenantId).advertisers];
  }

  async getAdvertiser(
    tenantId: string,
    id: string
  ): Promise<Advertiser | null> {
    return getStore(tenantId).advertisers.find((a) => a.id === id) ?? null;
  }

  async createAdvertiser(
    tenantId: string,
    input: CreateAdvertiserInput
  ): Promise<Advertiser> {
    const rec: Advertiser = {
      id: nextId(),
      name: input.name,
      loginUsername: input.loginUsername,
      isNetwork: input.isNetwork,
      active: true,
      createdAt: now(),
      updatedAt: now(),
    };
    getStore(tenantId).advertisers.push(rec);
    return rec;
  }

  async updateAdvertiser(
    tenantId: string,
    input: UpdateAdvertiserInput
  ): Promise<Advertiser> {
    const store = getStore(tenantId);
    const idx = store.advertisers.findIndex((a) => a.id === input.id);
    if (idx === -1) notFound("Advertiser", input.id);
    const existing = store.advertisers[idx];
    if (!existing) notFound("Advertiser", input.id);
    const updated: Advertiser = {
      ...existing,
      ...(input.name !== undefined && { name: input.name }),
      ...(input.loginUsername !== undefined && {
        loginUsername: input.loginUsername,
      }),
      ...(input.isNetwork !== undefined && { isNetwork: input.isNetwork }),
      ...(input.active !== undefined && { active: input.active }),
      updatedAt: now(),
    };
    store.advertisers[idx] = updated;
    return updated;
  }

  async deleteAdvertiser(tenantId: string, id: string): Promise<void> {
    const store = getStore(tenantId);
    const idx = store.advertisers.findIndex((a) => a.id === id);
    if (idx === -1) notFound("Advertiser", id);
    store.advertisers.splice(idx, 1);
  }

  // ---------------------------------------------------------------------------
  // Campaign
  // ---------------------------------------------------------------------------

  async listCampaigns(
    tenantId: string,
    opts?: { advertiserId?: string }
  ): Promise<Campaign[]> {
    let list = [...getStore(tenantId).campaigns];
    if (opts?.advertiserId) {
      list = list.filter((c) => c.advertiserId === opts.advertiserId);
    }
    return list;
  }

  async getCampaign(tenantId: string, id: string): Promise<Campaign | null> {
    return getStore(tenantId).campaigns.find((c) => c.id === id) ?? null;
  }

  async createCampaign(
    tenantId: string,
    input: CreateCampaignInput
  ): Promise<Campaign> {
    const rec: Campaign = {
      id: nextId(),
      advertiserId: input.advertiserId,
      name: input.name,
      type: input.type,
      priority: input.priority,
      goalTarget: input.goalTarget,
      goalMetric: input.goalMetric,
      startAt: input.startAt,
      endAt: input.endAt,
      pricingModel: input.pricingModel,
      rate: { amount: input.rateAmount, currency: input.rateCurrency },
      active: true,
      createdAt: now(),
      updatedAt: now(),
    };
    getStore(tenantId).campaigns.push(rec);
    return rec;
  }

  async updateCampaign(
    tenantId: string,
    input: UpdateCampaignInput
  ): Promise<Campaign> {
    const store = getStore(tenantId);
    const idx = store.campaigns.findIndex((c) => c.id === input.id);
    if (idx === -1) notFound("Campaign", input.id);
    const existing = store.campaigns[idx];
    if (!existing) notFound("Campaign", input.id);
    const updated: Campaign = {
      ...existing,
      ...(input.name !== undefined && { name: input.name }),
      ...(input.type !== undefined && { type: input.type }),
      ...(input.priority !== undefined && { priority: input.priority }),
      ...(input.goalTarget !== undefined && { goalTarget: input.goalTarget }),
      ...(input.goalMetric !== undefined && { goalMetric: input.goalMetric }),
      ...(input.startAt !== undefined && { startAt: input.startAt }),
      ...(input.endAt !== undefined && { endAt: input.endAt }),
      ...(input.pricingModel !== undefined && {
        pricingModel: input.pricingModel,
      }),
      ...(input.rateAmount !== undefined &&
        input.rateCurrency !== undefined && {
          rate: { amount: input.rateAmount, currency: input.rateCurrency },
        }),
      ...(input.active !== undefined && { active: input.active }),
      updatedAt: now(),
    };
    store.campaigns[idx] = updated;
    return updated;
  }

  async deleteCampaign(tenantId: string, id: string): Promise<void> {
    const store = getStore(tenantId);
    const idx = store.campaigns.findIndex((c) => c.id === id);
    if (idx === -1) notFound("Campaign", id);
    store.campaigns.splice(idx, 1);
  }

  // ---------------------------------------------------------------------------
  // Banner
  // ---------------------------------------------------------------------------

  async listBanners(
    tenantId: string,
    opts?: { campaignId?: string }
  ): Promise<Banner[]> {
    let list = [...getStore(tenantId).banners];
    if (opts?.campaignId) {
      list = list.filter((b) => b.campaignId === opts.campaignId);
    }
    return list;
  }

  async getBanner(tenantId: string, id: string): Promise<Banner | null> {
    return getStore(tenantId).banners.find((b) => b.id === id) ?? null;
  }

  async createBanner(
    tenantId: string,
    input: CreateBannerInput
  ): Promise<Banner> {
    const rec: Banner = {
      id: nextId(),
      campaignId: input.campaignId,
      name: input.name,
      creativeType: input.creativeType,
      assetUrl: input.assetUrl,
      destUrl: input.destUrl,
      width: input.width,
      height: input.height,
      active: true,
      createdAt: now(),
      updatedAt: now(),
    };
    getStore(tenantId).banners.push(rec);
    return rec;
  }

  async updateBanner(
    tenantId: string,
    input: UpdateBannerInput
  ): Promise<Banner> {
    const store = getStore(tenantId);
    const idx = store.banners.findIndex((b) => b.id === input.id);
    if (idx === -1) notFound("Banner", input.id);
    const existing = store.banners[idx];
    if (!existing) notFound("Banner", input.id);
    const updated: Banner = {
      ...existing,
      ...(input.name !== undefined && { name: input.name }),
      ...(input.assetUrl !== undefined && { assetUrl: input.assetUrl }),
      ...(input.destUrl !== undefined && { destUrl: input.destUrl }),
      ...(input.active !== undefined && { active: input.active }),
      updatedAt: now(),
    };
    store.banners[idx] = updated;
    return updated;
  }

  async deleteBanner(tenantId: string, id: string): Promise<void> {
    const store = getStore(tenantId);
    const idx = store.banners.findIndex((b) => b.id === id);
    if (idx === -1) notFound("Banner", id);
    store.banners.splice(idx, 1);
  }

  // ---------------------------------------------------------------------------
  // Site
  // ---------------------------------------------------------------------------

  async listSites(tenantId: string): Promise<Site[]> {
    return [...getStore(tenantId).sites];
  }

  async getSite(tenantId: string, id: string): Promise<Site | null> {
    return getStore(tenantId).sites.find((s) => s.id === id) ?? null;
  }

  async createSite(tenantId: string, input: CreateSiteInput): Promise<Site> {
    const rec: Site = {
      id: nextId(),
      name: input.name,
      url: input.url,
      active: true,
      createdAt: now(),
      updatedAt: now(),
    };
    getStore(tenantId).sites.push(rec);
    return rec;
  }

  async updateSite(tenantId: string, input: UpdateSiteInput): Promise<Site> {
    const store = getStore(tenantId);
    const idx = store.sites.findIndex((s) => s.id === input.id);
    if (idx === -1) notFound("Site", input.id);
    const existing = store.sites[idx];
    if (!existing) notFound("Site", input.id);
    const updated: Site = {
      ...existing,
      ...(input.name !== undefined && { name: input.name }),
      ...(input.url !== undefined && { url: input.url }),
      ...(input.active !== undefined && { active: input.active }),
      updatedAt: now(),
    };
    store.sites[idx] = updated;
    return updated;
  }

  async deleteSite(tenantId: string, id: string): Promise<void> {
    const store = getStore(tenantId);
    const idx = store.sites.findIndex((s) => s.id === id);
    if (idx === -1) notFound("Site", id);
    store.sites.splice(idx, 1);
  }

  // ---------------------------------------------------------------------------
  // Zone
  // ---------------------------------------------------------------------------

  async listZones(
    tenantId: string,
    opts?: { siteId?: string }
  ): Promise<Zone[]> {
    let list = [...getStore(tenantId).zones];
    if (opts?.siteId) {
      list = list.filter((z) => z.siteId === opts.siteId);
    }
    return list;
  }

  async getZone(tenantId: string, id: string): Promise<Zone | null> {
    return getStore(tenantId).zones.find((z) => z.id === id) ?? null;
  }

  async createZone(tenantId: string, input: CreateZoneInput): Promise<Zone> {
    const rec: Zone = {
      id: nextId(),
      siteId: input.siteId,
      name: input.name,
      width: input.width,
      height: input.height,
      type: input.type,
      active: true,
      createdAt: now(),
      updatedAt: now(),
    };
    getStore(tenantId).zones.push(rec);
    return rec;
  }

  async updateZone(tenantId: string, input: UpdateZoneInput): Promise<Zone> {
    const store = getStore(tenantId);
    const idx = store.zones.findIndex((z) => z.id === input.id);
    if (idx === -1) notFound("Zone", input.id);
    const existing = store.zones[idx];
    if (!existing) notFound("Zone", input.id);
    const updated: Zone = {
      ...existing,
      ...(input.name !== undefined && { name: input.name }),
      ...(input.width !== undefined && { width: input.width }),
      ...(input.height !== undefined && { height: input.height }),
      ...(input.type !== undefined && { type: input.type }),
      ...(input.active !== undefined && { active: input.active }),
      updatedAt: now(),
    };
    store.zones[idx] = updated;
    return updated;
  }

  async deleteZone(tenantId: string, id: string): Promise<void> {
    const store = getStore(tenantId);
    const idx = store.zones.findIndex((z) => z.id === id);
    if (idx === -1) notFound("Zone", id);
    store.zones.splice(idx, 1);
  }

  // ---------------------------------------------------------------------------
  // CampaignZone — vínculo N:N (DA-2)
  // ---------------------------------------------------------------------------

  async listCampaignZones(
    tenantId: string,
    campaignId: string
  ): Promise<CampaignZone[]> {
    return getStore(tenantId).campaignZones.filter(
      (cz) => cz.campaignId === campaignId
    );
  }

  async linkCampaignZones(
    tenantId: string,
    campaignId: string,
    zoneIds: string[]
  ): Promise<CampaignZone[]> {
    const store = getStore(tenantId);
    const added: CampaignZone[] = [];
    for (const zoneId of zoneIds) {
      const exists = store.campaignZones.some(
        (cz) => cz.campaignId === campaignId && cz.zoneId === zoneId
      );
      if (!exists) {
        const rec: CampaignZone = { campaignId, zoneId, createdAt: now() };
        store.campaignZones.push(rec);
        added.push(rec);
      }
    }
    return added;
  }

  async unlinkCampaignZones(
    tenantId: string,
    campaignId: string,
    zoneIds: string[]
  ): Promise<void> {
    const store = getStore(tenantId);
    store.campaignZones = store.campaignZones.filter(
      (cz) =>
        !(cz.campaignId === campaignId && zoneIds.includes(cz.zoneId))
    );
  }

  // ---------------------------------------------------------------------------
  // DeliveryRule (§4.6)
  // ---------------------------------------------------------------------------

  async listDeliveryRules(
    tenantId: string,
    opts: { ownerType: "campaign" | "banner"; ownerId: string }
  ): Promise<DeliveryRule[]> {
    return getStore(tenantId).deliveryRules.filter(
      (r) => r.ownerType === opts.ownerType && r.ownerId === opts.ownerId
    );
  }

  async getDeliveryRule(
    tenantId: string,
    id: string
  ): Promise<DeliveryRule | null> {
    return getStore(tenantId).deliveryRules.find((r) => r.id === id) ?? null;
  }

  async createDeliveryRule(
    tenantId: string,
    input: CreateDeliveryRuleInput
  ): Promise<DeliveryRule> {
    const rec: DeliveryRule = {
      id: nextId(),
      ownerType: input.ownerType,
      ownerId: input.ownerId,
      vector: input.vector,
      operator: input.operator,
      value: input.value,
      logicalOp: input.logicalOp,
      ruleSetId: input.ruleSetId,
      orderSeq: input.orderSeq,
      active: true,
      createdAt: now(),
      updatedAt: now(),
    };
    getStore(tenantId).deliveryRules.push(rec);
    return rec;
  }

  async updateDeliveryRule(
    tenantId: string,
    input: UpdateDeliveryRuleInput
  ): Promise<DeliveryRule> {
    const store = getStore(tenantId);
    const idx = store.deliveryRules.findIndex((r) => r.id === input.id);
    if (idx === -1) notFound("DeliveryRule", input.id);
    const existing = store.deliveryRules[idx];
    if (!existing) notFound("DeliveryRule", input.id);
    const updated: DeliveryRule = {
      ...existing,
      ...(input.vector !== undefined && { vector: input.vector }),
      ...(input.operator !== undefined && { operator: input.operator }),
      ...(input.value !== undefined && { value: input.value }),
      ...(input.logicalOp !== undefined && { logicalOp: input.logicalOp }),
      ...(input.orderSeq !== undefined && { orderSeq: input.orderSeq }),
      ...(input.active !== undefined && { active: input.active }),
      updatedAt: now(),
    };
    store.deliveryRules[idx] = updated;
    return updated;
  }

  async deleteDeliveryRule(tenantId: string, id: string): Promise<void> {
    const store = getStore(tenantId);
    const idx = store.deliveryRules.findIndex((r) => r.id === id);
    if (idx === -1) notFound("DeliveryRule", id);
    store.deliveryRules.splice(idx, 1);
  }

  // ---------------------------------------------------------------------------
  // DeliveryRuleSet (§4.6)
  // ---------------------------------------------------------------------------

  async listDeliveryRuleSets(tenantId: string): Promise<DeliveryRuleSet[]> {
    return [...getStore(tenantId).deliveryRuleSets];
  }

  async createDeliveryRuleSet(
    tenantId: string,
    input: { name: string; description: string | null }
  ): Promise<DeliveryRuleSet> {
    const rec: DeliveryRuleSet = {
      id: nextId(),
      name: input.name,
      description: input.description,
      createdAt: now(),
      updatedAt: now(),
    };
    getStore(tenantId).deliveryRuleSets.push(rec);
    return rec;
  }

  // ---------------------------------------------------------------------------
  // Cap (§4.8)
  // ---------------------------------------------------------------------------

  async listCaps(
    tenantId: string,
    opts: { ownerType: "campaign" | "banner"; ownerId: string }
  ): Promise<Cap[]> {
    return getStore(tenantId).caps.filter(
      (c) => c.ownerType === opts.ownerType && c.ownerId === opts.ownerId
    );
  }

  async createCap(tenantId: string, input: CreateCapInput): Promise<Cap> {
    const rec: Cap = {
      id: nextId(),
      ownerType: input.ownerType,
      ownerId: input.ownerId,
      scope: input.scope,
      limitCount: input.limitCount,
      resetInterval: input.resetInterval,
      active: true,
      createdAt: now(),
      updatedAt: now(),
    };
    getStore(tenantId).caps.push(rec);
    return rec;
  }

  async updateCap(tenantId: string, input: UpdateCapInput): Promise<Cap> {
    const store = getStore(tenantId);
    const idx = store.caps.findIndex((c) => c.id === input.id);
    if (idx === -1) notFound("Cap", input.id);
    const existing = store.caps[idx];
    if (!existing) notFound("Cap", input.id);
    const updated: Cap = {
      ...existing,
      ...(input.limitCount !== undefined && { limitCount: input.limitCount }),
      ...(input.resetInterval !== undefined && {
        resetInterval: input.resetInterval,
      }),
      ...(input.active !== undefined && { active: input.active }),
      updatedAt: now(),
    };
    store.caps[idx] = updated;
    return updated;
  }

  async deleteCap(tenantId: string, id: string): Promise<void> {
    const store = getStore(tenantId);
    const idx = store.caps.findIndex((c) => c.id === id);
    if (idx === -1) notFound("Cap", id);
    store.caps.splice(idx, 1);
  }
}
