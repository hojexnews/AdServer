/**
 * Router de configuração — CRUD das entidades (§4.1).
 * Fronteira de ACL: tenant_id vem de ctx.tenantId (sessão), NUNCA do cliente.
 */

import { z } from "zod";
import { TRPCError } from "@trpc/server";
import { router, tenantProcedure } from "../lib/trpc.js";
import type { ConfigAdapter } from "../adapters/config-adapter.js";
import {
  IdSchema,
  CreateAdvertiserInputSchema,
  UpdateAdvertiserInputSchema,
  CreateCampaignInputSchema,
  UpdateCampaignInputSchema,
  CreateBannerInputSchema,
  UpdateBannerInputSchema,
  CreateSiteInputSchema,
  UpdateSiteInputSchema,
  CreateZoneInputSchema,
  UpdateZoneInputSchema,
  LinkCampaignZoneInputSchema,
  UnlinkCampaignZoneInputSchema,
  CreateDeliveryRuleInputSchema,
  UpdateDeliveryRuleInputSchema,
  CreateDeliveryRuleSetInputSchema,
  CreateCapInputSchema,
  UpdateCapInputSchema,
} from "../schemas/config.js";

export function createConfigRouter(adapter: ConfigAdapter) {
  return router({
    // -------------------------------------------------------------------------
    // Advertiser
    // -------------------------------------------------------------------------
    advertiser: router({
      list: tenantProcedure.query(({ ctx }) =>
        adapter.listAdvertisers(ctx.tenantId)
      ),

      get: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .query(async ({ ctx, input }) => {
          const rec = await adapter.getAdvertiser(ctx.tenantId, input.id);
          if (!rec)
            throw new TRPCError({
              code: "NOT_FOUND",
              message: `Advertiser #${input.id} não encontrado`,
            });
          return rec;
        }),

      create: tenantProcedure
        .input(CreateAdvertiserInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.createAdvertiser(ctx.tenantId, input)
        ),

      update: tenantProcedure
        .input(UpdateAdvertiserInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.updateAdvertiser(ctx.tenantId, input)
        ),

      delete: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .mutation(({ ctx, input }) =>
          adapter.deleteAdvertiser(ctx.tenantId, input.id)
        ),
    }),

    // -------------------------------------------------------------------------
    // Campaign
    // -------------------------------------------------------------------------
    campaign: router({
      list: tenantProcedure
        .input(z.object({ advertiserId: IdSchema.optional() }))
        .query(({ ctx, input }) =>
          adapter.listCampaigns(
            ctx.tenantId,
            input.advertiserId !== undefined
              ? { advertiserId: input.advertiserId }
              : {}
          )
        ),

      get: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .query(async ({ ctx, input }) => {
          const rec = await adapter.getCampaign(ctx.tenantId, input.id);
          if (!rec)
            throw new TRPCError({
              code: "NOT_FOUND",
              message: `Campaign #${input.id} não encontrada`,
            });
          return rec;
        }),

      create: tenantProcedure
        .input(CreateCampaignInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.createCampaign(ctx.tenantId, input)
        ),

      update: tenantProcedure
        .input(UpdateCampaignInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.updateCampaign(ctx.tenantId, input)
        ),

      delete: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .mutation(({ ctx, input }) =>
          adapter.deleteCampaign(ctx.tenantId, input.id)
        ),
    }),

    // -------------------------------------------------------------------------
    // Banner
    // -------------------------------------------------------------------------
    banner: router({
      list: tenantProcedure
        .input(z.object({ campaignId: IdSchema.optional() }))
        .query(({ ctx, input }) =>
          adapter.listBanners(
            ctx.tenantId,
            input.campaignId !== undefined
              ? { campaignId: input.campaignId }
              : {}
          )
        ),

      get: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .query(async ({ ctx, input }) => {
          const rec = await adapter.getBanner(ctx.tenantId, input.id);
          if (!rec)
            throw new TRPCError({
              code: "NOT_FOUND",
              message: `Banner #${input.id} não encontrado`,
            });
          return rec;
        }),

      create: tenantProcedure
        .input(CreateBannerInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.createBanner(ctx.tenantId, input)
        ),

      update: tenantProcedure
        .input(UpdateBannerInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.updateBanner(ctx.tenantId, input)
        ),

      delete: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .mutation(({ ctx, input }) =>
          adapter.deleteBanner(ctx.tenantId, input.id)
        ),
    }),

    // -------------------------------------------------------------------------
    // Site
    // -------------------------------------------------------------------------
    site: router({
      list: tenantProcedure.query(({ ctx }) =>
        adapter.listSites(ctx.tenantId)
      ),

      get: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .query(async ({ ctx, input }) => {
          const rec = await adapter.getSite(ctx.tenantId, input.id);
          if (!rec)
            throw new TRPCError({
              code: "NOT_FOUND",
              message: `Site #${input.id} não encontrado`,
            });
          return rec;
        }),

      create: tenantProcedure
        .input(CreateSiteInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.createSite(ctx.tenantId, input)
        ),

      update: tenantProcedure
        .input(UpdateSiteInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.updateSite(ctx.tenantId, input)
        ),

      delete: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .mutation(({ ctx, input }) =>
          adapter.deleteSite(ctx.tenantId, input.id)
        ),
    }),

    // -------------------------------------------------------------------------
    // Zone
    // -------------------------------------------------------------------------
    zone: router({
      list: tenantProcedure
        .input(z.object({ siteId: IdSchema.optional() }))
        .query(({ ctx, input }) =>
          adapter.listZones(
            ctx.tenantId,
            input.siteId !== undefined ? { siteId: input.siteId } : {}
          )
        ),

      get: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .query(async ({ ctx, input }) => {
          const rec = await adapter.getZone(ctx.tenantId, input.id);
          if (!rec)
            throw new TRPCError({
              code: "NOT_FOUND",
              message: `Zone #${input.id} não encontrada`,
            });
          return rec;
        }),

      create: tenantProcedure
        .input(CreateZoneInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.createZone(ctx.tenantId, input)
        ),

      update: tenantProcedure
        .input(UpdateZoneInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.updateZone(ctx.tenantId, input)
        ),

      delete: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .mutation(({ ctx, input }) =>
          adapter.deleteZone(ctx.tenantId, input.id)
        ),
    }),

    // -------------------------------------------------------------------------
    // CampaignZone — vínculo N:N (DA-2 / CA-1)
    // -------------------------------------------------------------------------
    campaignZone: router({
      list: tenantProcedure
        .input(z.object({ campaignId: IdSchema }))
        .query(({ ctx, input }) =>
          adapter.listCampaignZones(ctx.tenantId, input.campaignId)
        ),

      link: tenantProcedure
        .input(LinkCampaignZoneInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.linkCampaignZones(
            ctx.tenantId,
            input.campaignId,
            input.zoneIds
          )
        ),

      unlink: tenantProcedure
        .input(UnlinkCampaignZoneInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.unlinkCampaignZones(
            ctx.tenantId,
            input.campaignId,
            input.zoneIds
          )
        ),
    }),

    // -------------------------------------------------------------------------
    // DeliveryRule (§4.6)
    // -------------------------------------------------------------------------
    deliveryRule: router({
      list: tenantProcedure
        .input(
          z.object({
            ownerType: z.enum(["campaign", "banner"]),
            ownerId: IdSchema,
          })
        )
        .query(({ ctx, input }) =>
          adapter.listDeliveryRules(ctx.tenantId, input)
        ),

      create: tenantProcedure
        .input(CreateDeliveryRuleInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.createDeliveryRule(ctx.tenantId, input)
        ),

      update: tenantProcedure
        .input(UpdateDeliveryRuleInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.updateDeliveryRule(ctx.tenantId, input)
        ),

      delete: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .mutation(({ ctx, input }) =>
          adapter.deleteDeliveryRule(ctx.tenantId, input.id)
        ),
    }),

    // -------------------------------------------------------------------------
    // DeliveryRuleSet (§4.6)
    // -------------------------------------------------------------------------
    deliveryRuleSet: router({
      list: tenantProcedure.query(({ ctx }) =>
        adapter.listDeliveryRuleSets(ctx.tenantId)
      ),

      create: tenantProcedure
        .input(CreateDeliveryRuleSetInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.createDeliveryRuleSet(ctx.tenantId, input)
        ),
    }),

    // -------------------------------------------------------------------------
    // Cap (§4.8)
    // -------------------------------------------------------------------------
    cap: router({
      list: tenantProcedure
        .input(
          z.object({
            ownerType: z.enum(["campaign", "banner"]),
            ownerId: IdSchema,
          })
        )
        .query(({ ctx, input }) => adapter.listCaps(ctx.tenantId, input)),

      create: tenantProcedure
        .input(CreateCapInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.createCap(ctx.tenantId, input)
        ),

      update: tenantProcedure
        .input(UpdateCapInputSchema)
        .mutation(({ ctx, input }) =>
          adapter.updateCap(ctx.tenantId, input)
        ),

      delete: tenantProcedure
        .input(z.object({ id: IdSchema }))
        .mutation(({ ctx, input }) =>
          adapter.deleteCap(ctx.tenantId, input.id)
        ),
    }),
  });
}
