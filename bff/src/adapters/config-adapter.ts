/**
 * Interface do adapter de configuração — fronteira entre o BFF e o Postgres.
 *
 * O adapter real (postgres-config-adapter.ts) usa pg/pgbouncer e injeta
 * SET LOCAL adserver.tenant_id = '<uuid>' antes de cada query para acionar
 * o RLS (Row-Level Security) definido em 0002_config_rls_up.sql.
 *
 * Nesta fase (I4), o stub in-memory implementa a mesma interface para que
 * typecheck/build/tests passem. O adapter real conecta substituindo a
 * importação em src/index.ts.
 */

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

/**
 * Contrato do adapter de configuração.
 *
 * Todos os métodos recebem tenantId como primeiro parâmetro.
 * O adapter real usa, DENTRO de uma transação:
 *   `await client.query("SELECT set_config('adserver.tenant_id', $1, true)", [tenantId])`
 * antes de qualquer query de dados — ativando o RLS do Postgres.
 * (NÃO usar `SET LOCAL ... = $1`: SET é utility statement e não aceita bind params.)
 *
 * NUNCA confiar em tenant_id vindo de parâmetros de UI.
 */
export interface ConfigAdapter {
  // Advertiser
  listAdvertisers(tenantId: string): Promise<Advertiser[]>;
  getAdvertiser(tenantId: string, id: string): Promise<Advertiser | null>;
  createAdvertiser(
    tenantId: string,
    input: CreateAdvertiserInput
  ): Promise<Advertiser>;
  updateAdvertiser(
    tenantId: string,
    input: UpdateAdvertiserInput
  ): Promise<Advertiser>;
  deleteAdvertiser(tenantId: string, id: string): Promise<void>;

  // Campaign
  listCampaigns(
    tenantId: string,
    opts?: { advertiserId?: string }
  ): Promise<Campaign[]>;
  getCampaign(tenantId: string, id: string): Promise<Campaign | null>;
  createCampaign(
    tenantId: string,
    input: CreateCampaignInput
  ): Promise<Campaign>;
  updateCampaign(
    tenantId: string,
    input: UpdateCampaignInput
  ): Promise<Campaign>;
  deleteCampaign(tenantId: string, id: string): Promise<void>;

  // Banner
  listBanners(
    tenantId: string,
    opts?: { campaignId?: string }
  ): Promise<Banner[]>;
  getBanner(tenantId: string, id: string): Promise<Banner | null>;
  createBanner(tenantId: string, input: CreateBannerInput): Promise<Banner>;
  updateBanner(tenantId: string, input: UpdateBannerInput): Promise<Banner>;
  deleteBanner(tenantId: string, id: string): Promise<void>;

  // Site
  listSites(tenantId: string): Promise<Site[]>;
  getSite(tenantId: string, id: string): Promise<Site | null>;
  createSite(tenantId: string, input: CreateSiteInput): Promise<Site>;
  updateSite(tenantId: string, input: UpdateSiteInput): Promise<Site>;
  deleteSite(tenantId: string, id: string): Promise<void>;

  // Zone
  listZones(tenantId: string, opts?: { siteId?: string }): Promise<Zone[]>;
  getZone(tenantId: string, id: string): Promise<Zone | null>;
  createZone(tenantId: string, input: CreateZoneInput): Promise<Zone>;
  updateZone(tenantId: string, input: UpdateZoneInput): Promise<Zone>;
  deleteZone(tenantId: string, id: string): Promise<void>;

  // CampaignZone — vínculo N:N (DA-2)
  listCampaignZones(
    tenantId: string,
    campaignId: string
  ): Promise<CampaignZone[]>;
  linkCampaignZones(
    tenantId: string,
    campaignId: string,
    zoneIds: string[]
  ): Promise<CampaignZone[]>;
  unlinkCampaignZones(
    tenantId: string,
    campaignId: string,
    zoneIds: string[]
  ): Promise<void>;

  // DeliveryRule (§4.6)
  listDeliveryRules(
    tenantId: string,
    opts: { ownerType: "campaign" | "banner"; ownerId: string }
  ): Promise<DeliveryRule[]>;
  createDeliveryRule(
    tenantId: string,
    input: CreateDeliveryRuleInput
  ): Promise<DeliveryRule>;
  updateDeliveryRule(
    tenantId: string,
    input: UpdateDeliveryRuleInput
  ): Promise<DeliveryRule>;
  deleteDeliveryRule(tenantId: string, id: string): Promise<void>;

  // DeliveryRuleSet (§4.6)
  listDeliveryRuleSets(tenantId: string): Promise<DeliveryRuleSet[]>;
  createDeliveryRuleSet(
    tenantId: string,
    input: { name: string; description: string | null }
  ): Promise<DeliveryRuleSet>;

  // Cap (§4.8)
  listCaps(
    tenantId: string,
    opts: { ownerType: "campaign" | "banner"; ownerId: string }
  ): Promise<Cap[]>;
  createCap(tenantId: string, input: CreateCapInput): Promise<Cap>;
  updateCap(tenantId: string, input: UpdateCapInput): Promise<Cap>;
  deleteCap(tenantId: string, id: string): Promise<void>;
}
