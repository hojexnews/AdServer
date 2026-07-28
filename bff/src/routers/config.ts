/**
 * Router de configuração — CRUD das entidades (§4.1).
 * Fronteira de ACL: tenant_id vem de ctx.tenantId (sessão), NUNCA do cliente.
 */

import { z } from "zod";
import { TRPCError } from "@trpc/server";
import { router, tenantProcedure } from "../lib/trpc.js";
import type { ConfigAdapter } from "../adapters/config-adapter.js";
import { detectContradictions, type RuleCandidate } from "../lib/contradiction.js";
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

/**
 * Backstop CA-4 compartilhado por TODOS os mutadores de DeliveryRule que
 * podem produzir um estado ativo novo (create, update — nunca delete: remover
 * uma regra só pode ELIMINAR uma contradição, nunca introduzir uma).
 *
 * DEFAULT-DENY (wave 30 remediação, achado #3 — "bypass trivial do controle
 * recém-adicionado"): antes desta correção, só deliveryRule.create chamava
 * detectContradictions(); deliveryRule.update escrevia o novo vector/operator/
 * value/logicalOp direto no adapter sem checagem alguma — bastava criar a
 * regra sem contradição e depois dar update para o estado contraditório.
 * bff/src/routers/config.test.ts prova, por REFLEXÃO sobre os mutadores
 * REAIS do sub-router deliveryRule (não uma lista manual), que todo mutador
 * não isento roda este guard — um mutador novo (ex.: um futuro "reorder" ou
 * "bulkUpdate") que grave vector/operator/value/logicalOp sem passar por
 * aqui quebra aquele teste em vez de escapar silenciosamente.
 *
 * `excludeRuleId` (update) evita comparar a regra contra ela mesma quando
 * ela já está entre as regras existentes do owner.
 */
async function assertNoContradiction(
  adapter: ConfigAdapter,
  tenantId: string,
  params: {
    ownerType: "campaign" | "banner";
    ownerId: string;
    candidate: RuleCandidate;
    excludeRuleId?: string;
    acknowledgeContradiction: boolean;
  }
): Promise<void> {
  const existing = await adapter.listDeliveryRules(tenantId, {
    ownerType: params.ownerType,
    ownerId: params.ownerId,
  });
  const existingCandidates: RuleCandidate[] = existing
    .filter((r) => r.active && r.id !== params.excludeRuleId)
    .map((r) => ({
      vector: r.vector,
      operator: r.operator,
      value: r.value,
      logicalOp: r.logicalOp,
    }));
  const result = detectContradictions([
    ...existingCandidates,
    params.candidate,
  ]);
  // Só os conflitos que envolvem O CANDIDATO sugerido importam aqui
  // (identidade de referência: params.candidate é o mesmo objeto inserido
  // acima — detectContradictions não clona as entradas). Conflitos
  // pré-existentes só entre regras já salvas não bloqueiam esta mutação
  // específica.
  const relevant = result.conflicts.filter(
    (c) => c.ruleA === params.candidate || c.ruleB === params.candidate
  );
  if (relevant.length > 0 && !params.acknowledgeContradiction) {
    throw new TRPCError({
      code: "CONFLICT",
      message:
        `Contradição detectada (CA-4): a regra entra em conflito com ` +
        `${relevant.length} regra(s) já existente(s). ` +
        relevant.map((c) => c.reason).join(" ") +
        ` Reenvie com acknowledgeContradiction=true para salvar mesmo assim.`,
    });
  }
}

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
        .mutation(async ({ ctx, input }) => {
          // CA-4 backstop server-side (wave 30, achado
          // contradiction-vector-allowlist-gap): a UI (app/rules/page.tsx)
          // já roda esta MESMA validação antes de enviar a mutação, mas a
          // UI sozinha não é fronteira de confiança — um cliente que chame
          // este procedimento diretamente (bypassando o builder) não pode
          // persistir uma regra AND mutuamente exclusiva sem confirmação
          // explícita.
          const candidate: RuleCandidate = {
            vector: input.vector,
            operator: input.operator,
            value: input.value,
            logicalOp: input.logicalOp,
          };
          await assertNoContradiction(adapter, ctx.tenantId, {
            ownerType: input.ownerType,
            ownerId: input.ownerId,
            candidate,
            acknowledgeContradiction: input.acknowledgeContradiction,
          });
          return adapter.createDeliveryRule(ctx.tenantId, input);
        }),

      // CA-4 backstop server-side também em update (wave 30 remediação,
      // achado #3 — "bypass trivial do controle recém-adicionado": antes
      // desta correção, deliveryRule.update NÃO rodava detectContradictions,
      // então bastava criar a regra sem contradição e depois dar update para
      // o estado contraditório). UpdateDeliveryRuleInputSchema só carrega
      // `id` + campos PARCIAIS opcionais, então buscamos o estado ATUAL via
      // adapter.getDeliveryRule para saber ownerType/ownerId e montar o
      // candidato pós-update (campos não enviados mantêm o valor existente).
      update: tenantProcedure
        .input(UpdateDeliveryRuleInputSchema)
        .mutation(async ({ ctx, input }) => {
          const existingRule = await adapter.getDeliveryRule(
            ctx.tenantId,
            input.id
          );
          // Regra inexistente (ou de outro tenant sob RLS): sem o registro
          // atual não há ownerType/ownerId para checar — deixa o adapter
          // recusar com NOT_FOUND como já fazia antes desta correção.
          if (existingRule === null) {
            return adapter.updateDeliveryRule(ctx.tenantId, input);
          }
          const finalActive = input.active ?? existingRule.active;
          // Desativar uma regra (ou mantê-la inativa) nunca introduz uma
          // contradição NOVA — só regras ativas entram no cálculo de CA-4
          // (mesma regra que rege as regras "irmãs" em assertNoContradiction
          // e no builder). Pular o guard aqui evita bloquear o caso comum de
          // "desativar uma regra problemática" atrás de um CONFLICT confuso.
          if (finalActive) {
            const candidate: RuleCandidate = {
              vector: input.vector ?? existingRule.vector,
              operator: input.operator ?? existingRule.operator,
              value: input.value ?? existingRule.value,
              logicalOp: input.logicalOp ?? existingRule.logicalOp,
            };
            await assertNoContradiction(adapter, ctx.tenantId, {
              ownerType: existingRule.ownerType,
              ownerId: existingRule.ownerId,
              candidate,
              excludeRuleId: existingRule.id,
              acknowledgeContradiction: input.acknowledgeContradiction,
            });
          }
          return adapter.updateDeliveryRule(ctx.tenantId, input);
        }),

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
