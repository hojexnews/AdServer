/**
 * Schemas Zod das entidades de configuração do AdServer (§4.1).
 * Fonte única de verdade para inputs/outputs do BFF (CA-1).
 *
 * Derivados do schema SQL em db/config/migrations/0001_config_schema_up.sql.
 * O BFF garante que tenant_id NUNCA vem do cliente — é injetado server-side.
 */

import { z } from "zod";
import { MoneySchema } from "./money.js";

// ---------------------------------------------------------------------------
// Enums — espelham os tipos PostgreSQL do schema de config
// ---------------------------------------------------------------------------

export const CampaignTypeSchema = z.enum(["override", "contract", "remnant"]);
export const GoalMetricSchema = z.enum(["impressions", "clicks", "conversions"]);
export const PricingModelSchema = z.enum(["cpm", "cpc", "cpa", "tenancy"]);
export const CreativeTypeSchema = z.enum([
  "image",
  "html5",
  "thirdparty_tag",
  "video",
]);
export const OwnerEntitySchema = z.enum(["campaign", "banner"]);
export const LogicalOpSchema = z.enum(["AND", "OR"]);
export const CapScopeSchema = z.enum(["campaign_total", "session", "clock"]);

export const DeliveryRuleVectorSchema = z.enum([
  "Time - Day of Week",
  "Site - URL",
  "Geo - Country",
  "Geo - City",
  "Client - Useragent",
  "Site - Variable",
]);

export const DeliveryRuleOperatorSchema = z.enum([
  "is",
  "is not",
  "contains",
  "does not contain",
  "starts with",
  "ends with",
]);

// ---------------------------------------------------------------------------
// ID scalar — bigint do Postgres vira string no fio para evitar perda de
// precisão com Number JS (máx. ~9 quadrilhões vs int64)
// ---------------------------------------------------------------------------
export const IdSchema = z
  .string()
  .regex(/^\d+$/, "ID deve ser string numérica");

// ---------------------------------------------------------------------------
// Advertiser
// ---------------------------------------------------------------------------

export const AdvertiserSchema = z.object({
  id: IdSchema,
  name: z.string().min(1).max(200),
  loginUsername: z.string().min(1).max(100),
  isNetwork: z.boolean(),
  active: z.boolean(),
  createdAt: z.string().datetime(),
  updatedAt: z.string().datetime(),
});

export const CreateAdvertiserInputSchema = z.object({
  name: z.string().min(1).max(200),
  loginUsername: z.string().min(1).max(100),
  loginPassword: z
    .string()
    .min(8, "senha mínimo 8 caracteres")
    .max(128),
  isNetwork: z.boolean().default(false),
});

export const UpdateAdvertiserInputSchema = z.object({
  id: IdSchema,
  name: z.string().min(1).max(200).optional(),
  loginUsername: z.string().min(1).max(100).optional(),
  loginPassword: z.string().min(8).max(128).optional(),
  isNetwork: z.boolean().optional(),
  active: z.boolean().optional(),
});

export type Advertiser = z.infer<typeof AdvertiserSchema>;
export type CreateAdvertiserInput = z.infer<typeof CreateAdvertiserInputSchema>;
export type UpdateAdvertiserInput = z.infer<typeof UpdateAdvertiserInputSchema>;

// ---------------------------------------------------------------------------
// Campaign
// ---------------------------------------------------------------------------

export const CampaignSchema = z.object({
  id: IdSchema,
  advertiserId: IdSchema,
  name: z.string().min(1).max(300),
  type: CampaignTypeSchema,
  priority: z.number().int().min(1).max(10),
  goalTarget: z.number().int().positive().nullable(),
  goalMetric: GoalMetricSchema.nullable(),
  startAt: z.string().datetime(),
  endAt: z.string().datetime(),
  pricingModel: PricingModelSchema,
  // rate entregue como string DECIMAL + currency (TX-2)
  rate: MoneySchema,
  active: z.boolean(),
  createdAt: z.string().datetime(),
  updatedAt: z.string().datetime(),
});

export const CreateCampaignInputSchema = z
  .object({
    advertiserId: IdSchema,
    name: z.string().min(1).max(300),
    type: CampaignTypeSchema,
    priority: z.number().int().min(1).max(10).default(5),
    goalTarget: z.number().int().positive().nullable().default(null),
    goalMetric: GoalMetricSchema.nullable().default(null),
    startAt: z.string().datetime(),
    endAt: z.string().datetime(),
    pricingModel: PricingModelSchema,
    rateAmount: z
      .string()
      .regex(/^\d+(\.\d+)?$/, "rate deve ser string DECIMAL positiva"),
    rateCurrency: z
      .string()
      .min(1)
      .max(10)
      .regex(/^[A-Z0-9]+$/),
  })
  .refine((d) => d.endAt > d.startAt, {
    message: "endAt deve ser posterior a startAt",
    path: ["endAt"],
  })
  .refine(
    (d) => {
      if (d.pricingModel === "tenancy") {
        return d.goalTarget === null && d.goalMetric === null;
      }
      return d.goalTarget !== null && d.goalMetric !== null;
    },
    {
      message:
        "Tenancy não tem meta de volume; outros modelos exigem goalTarget e goalMetric",
    }
  );

export const UpdateCampaignInputSchema = z.object({
  id: IdSchema,
  name: z.string().min(1).max(300).optional(),
  type: CampaignTypeSchema.optional(),
  priority: z.number().int().min(1).max(10).optional(),
  goalTarget: z.number().int().positive().nullable().optional(),
  goalMetric: GoalMetricSchema.nullable().optional(),
  startAt: z.string().datetime().optional(),
  endAt: z.string().datetime().optional(),
  pricingModel: PricingModelSchema.optional(),
  rateAmount: z
    .string()
    .regex(/^\d+(\.\d+)?$/)
    .optional(),
  rateCurrency: z
    .string()
    .min(1)
    .max(10)
    .regex(/^[A-Z0-9]+$/)
    .optional(),
  active: z.boolean().optional(),
});

export type Campaign = z.infer<typeof CampaignSchema>;
export type CreateCampaignInput = z.infer<typeof CreateCampaignInputSchema>;
export type UpdateCampaignInput = z.infer<typeof UpdateCampaignInputSchema>;

// ---------------------------------------------------------------------------
// Banner / Criativo
// ---------------------------------------------------------------------------

export const BannerSchema = z.object({
  id: IdSchema,
  campaignId: IdSchema,
  name: z.string().min(1).max(300),
  creativeType: CreativeTypeSchema,
  assetUrl: z.string().url().nullable(),
  destUrl: z.string().url().nullable(),
  width: z.number().int().positive(),
  height: z.number().int().positive(),
  active: z.boolean(),
  createdAt: z.string().datetime(),
  updatedAt: z.string().datetime(),
});

export const CreateBannerInputSchema = z
  .object({
    campaignId: IdSchema,
    name: z.string().min(1).max(300),
    creativeType: CreativeTypeSchema,
    assetUrl: z.string().url().nullable().default(null),
    destUrl: z.string().url().nullable().default(null),
    width: z.number().int().positive(),
    height: z.number().int().positive(),
  })
  .refine(
    (d) => {
      if (d.creativeType !== "thirdparty_tag") return d.destUrl !== null;
      return true;
    },
    {
      message: "destUrl obrigatório para image, html5 e video",
      path: ["destUrl"],
    }
  )
  .refine((d) => d.assetUrl !== null, {
    message: "assetUrl obrigatório (upload via URL pré-assinada)",
    path: ["assetUrl"],
  });

// assetUrl/destUrl NÃO são nullable no update: este caminho não gere asset_blob
// nem creative_type, então gravar NULL violaria banners_asset_xor_chk /
// banners_dest_url_chk (0001) — 23514 em runtime. O update só SUBSTITUI a URL;
// limpar/alternar a representação do criativo é recriar o banner. Espelha os
// .refine() de CreateBannerInputSchema.
export const UpdateBannerInputSchema = z.object({
  id: IdSchema,
  name: z.string().min(1).max(300).optional(),
  assetUrl: z.string().url().optional(),
  destUrl: z.string().url().optional(),
  active: z.boolean().optional(),
});

export type Banner = z.infer<typeof BannerSchema>;
export type CreateBannerInput = z.infer<typeof CreateBannerInputSchema>;
export type UpdateBannerInput = z.infer<typeof UpdateBannerInputSchema>;

// ---------------------------------------------------------------------------
// Site
// ---------------------------------------------------------------------------

export const SiteSchema = z.object({
  id: IdSchema,
  name: z.string().min(1).max(200),
  url: z.string().url(),
  active: z.boolean(),
  createdAt: z.string().datetime(),
  updatedAt: z.string().datetime(),
});

export const CreateSiteInputSchema = z.object({
  name: z.string().min(1).max(200),
  url: z.string().url(),
});

export const UpdateSiteInputSchema = z.object({
  id: IdSchema,
  name: z.string().min(1).max(200).optional(),
  url: z.string().url().optional(),
  active: z.boolean().optional(),
});

export type Site = z.infer<typeof SiteSchema>;
export type CreateSiteInput = z.infer<typeof CreateSiteInputSchema>;
export type UpdateSiteInput = z.infer<typeof UpdateSiteInputSchema>;

// ---------------------------------------------------------------------------
// Zone
// ---------------------------------------------------------------------------

export const ZoneSchema = z.object({
  id: IdSchema,
  siteId: IdSchema,
  name: z.string().min(1).max(200),
  width: z.number().int().positive(),
  height: z.number().int().positive(),
  type: z.string().min(1).max(50).default("standard"),
  active: z.boolean(),
  createdAt: z.string().datetime(),
  updatedAt: z.string().datetime(),
});

export const CreateZoneInputSchema = z.object({
  siteId: IdSchema,
  name: z.string().min(1).max(200),
  width: z.number().int().positive(),
  height: z.number().int().positive(),
  type: z.string().min(1).max(50).default("standard"),
});

export const UpdateZoneInputSchema = z.object({
  id: IdSchema,
  name: z.string().min(1).max(200).optional(),
  width: z.number().int().positive().optional(),
  height: z.number().int().positive().optional(),
  type: z.string().min(1).max(50).optional(),
  active: z.boolean().optional(),
});

export type Zone = z.infer<typeof ZoneSchema>;
export type CreateZoneInput = z.infer<typeof CreateZoneInputSchema>;
export type UpdateZoneInput = z.infer<typeof UpdateZoneInputSchema>;

// ---------------------------------------------------------------------------
// CampaignZone — vínculo N:N (DA-2)
// ---------------------------------------------------------------------------

export const CampaignZoneSchema = z.object({
  campaignId: IdSchema,
  zoneId: IdSchema,
  createdAt: z.string().datetime(),
});

export const LinkCampaignZoneInputSchema = z.object({
  campaignId: IdSchema,
  zoneIds: z.array(IdSchema).min(1).max(500),
});

export const UnlinkCampaignZoneInputSchema = z.object({
  campaignId: IdSchema,
  zoneIds: z.array(IdSchema).min(1).max(500),
});

export type CampaignZone = z.infer<typeof CampaignZoneSchema>;

// ---------------------------------------------------------------------------
// DeliveryRule (§4.6)
// ---------------------------------------------------------------------------

export const DeliveryRuleSchema = z.object({
  id: IdSchema,
  ownerType: OwnerEntitySchema,
  ownerId: IdSchema,
  vector: DeliveryRuleVectorSchema,
  operator: DeliveryRuleOperatorSchema,
  value: z.string().min(1).max(1000),
  logicalOp: LogicalOpSchema,
  ruleSetId: IdSchema.nullable(),
  orderSeq: z.number().int().min(0),
  active: z.boolean(),
  createdAt: z.string().datetime(),
  updatedAt: z.string().datetime(),
});

export const CreateDeliveryRuleInputSchema = z.object({
  ownerType: OwnerEntitySchema,
  ownerId: IdSchema,
  vector: DeliveryRuleVectorSchema,
  operator: DeliveryRuleOperatorSchema,
  value: z.string().min(1).max(1000),
  logicalOp: LogicalOpSchema.default("AND"),
  ruleSetId: IdSchema.nullable().default(null),
  orderSeq: z.number().int().min(0).default(0),
});

export const UpdateDeliveryRuleInputSchema = z.object({
  id: IdSchema,
  vector: DeliveryRuleVectorSchema.optional(),
  operator: DeliveryRuleOperatorSchema.optional(),
  value: z.string().min(1).max(1000).optional(),
  logicalOp: LogicalOpSchema.optional(),
  orderSeq: z.number().int().min(0).optional(),
  active: z.boolean().optional(),
});

export type DeliveryRule = z.infer<typeof DeliveryRuleSchema>;
export type CreateDeliveryRuleInput = z.infer<
  typeof CreateDeliveryRuleInputSchema
>;
export type UpdateDeliveryRuleInput = z.infer<
  typeof UpdateDeliveryRuleInputSchema
>;

// ---------------------------------------------------------------------------
// DeliveryRuleSet (§4.6)
// ---------------------------------------------------------------------------

export const DeliveryRuleSetSchema = z.object({
  id: IdSchema,
  name: z.string().min(1).max(200),
  description: z.string().max(1000).nullable(),
  createdAt: z.string().datetime(),
  updatedAt: z.string().datetime(),
});

export const CreateDeliveryRuleSetInputSchema = z.object({
  name: z.string().min(1).max(200),
  description: z.string().max(1000).nullable().default(null),
});

export type DeliveryRuleSet = z.infer<typeof DeliveryRuleSetSchema>;

// ---------------------------------------------------------------------------
// Cap — frequency capping (§4.8)
// ---------------------------------------------------------------------------

export const CapSchema = z.object({
  id: IdSchema,
  ownerType: OwnerEntitySchema,
  ownerId: IdSchema,
  scope: CapScopeSchema,
  limitCount: z.number().int().positive(),
  resetInterval: z.number().int().positive().nullable(),
  active: z.boolean(),
  createdAt: z.string().datetime(),
  updatedAt: z.string().datetime(),
});

export const CreateCapInputSchema = z
  .object({
    ownerType: OwnerEntitySchema,
    ownerId: IdSchema,
    scope: CapScopeSchema,
    limitCount: z.number().int().positive(),
    resetInterval: z.number().int().positive().nullable().default(null),
  })
  .refine(
    (d) => {
      if (d.scope === "clock") return d.resetInterval !== null;
      return d.resetInterval === null;
    },
    {
      message: "resetInterval obrigatório para scope=clock; nulo para os demais",
      path: ["resetInterval"],
    }
  );

export const UpdateCapInputSchema = z.object({
  id: IdSchema,
  limitCount: z.number().int().positive().optional(),
  resetInterval: z.number().int().positive().nullable().optional(),
  active: z.boolean().optional(),
});

export type Cap = z.infer<typeof CapSchema>;
export type CreateCapInput = z.infer<typeof CreateCapInputSchema>;
export type UpdateCapInput = z.infer<typeof UpdateCapInputSchema>;
