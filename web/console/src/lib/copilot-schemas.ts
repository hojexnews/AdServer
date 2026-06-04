/**
 * Schemas Zod para os eventos SSE e estruturas do copiloto (J5/Fase 2).
 *
 * INVARIANTES (TX-3 / security-reviewer):
 *   - Nenhum schema aqui contém tenant_id, ANTHROPIC_API_KEY nem HMAC.
 *   - Esses campos são server-side no BFF; a UI nunca os vê.
 *   - Dinheiro em WriteDiff vem como string DECIMAL + currency (TX-2/DA-10).
 *   - Conteúdo do LLM é tratado como texto não-confiável (sem dangerouslySetInnerHTML).
 *
 * Fonte de verdade: bff/src/routers/copilot.ts (contrato SSE documentado inline).
 */

import { z } from "zod";

// ---------------------------------------------------------------------------
// MoneyWire — alinhado com lib/money.ts (TX-2/DA-10)
// ---------------------------------------------------------------------------
export const MoneyWireSchema = z.object({
  amount: z.string().regex(/^-?\d+(\.\d+)?$/, "Valor monetário deve ser string decimal"),
  currency: z.string().min(1).max(10),
});

export type MoneyWire = z.infer<typeof MoneyWireSchema>;

// ---------------------------------------------------------------------------
// WriteDiff — representa o que o copiloto propõe criar/alterar
// O BFF/copiloto Python emite este objeto no evento hitl_required.
// A UI exibe para aprovação humana. NUNCA aplica autonomamente.
// ---------------------------------------------------------------------------

export const WriteDiffCampaignSchema = z.object({
  kind: z.literal("campaign"),
  action: z.enum(["create", "update", "delete"]),
  id: z.string().nullish(),
  name: z.string().nullish(),
  advertiserId: z.string().nullish(),
  dailyBudget: MoneyWireSchema.nullish(),
  totalBudget: MoneyWireSchema.nullish(),
  status: z.enum(["active", "paused", "archived"]).nullish(),
});

export const WriteDiffBannerSchema = z.object({
  kind: z.literal("banner"),
  action: z.enum(["create", "update", "delete"]),
  id: z.string().nullish(),
  name: z.string().nullish(),
  campaignId: z.string().nullish(),
  width: z.number().int().positive().nullish(),
  height: z.number().int().positive().nullish(),
  url: z.string().url().nullish(),
  status: z.enum(["active", "paused", "archived"]).nullish(),
});

export const WriteDiffRuleSchema = z.object({
  kind: z.literal("rule"),
  action: z.enum(["create", "update", "delete"]),
  id: z.string().nullish(),
  ownerType: z.enum(["campaign", "banner"]).nullish(),
  ownerId: z.string().nullish(),
  vector: z.string().nullish(),
  operator: z.string().nullish(),
  value: z.string().nullish(),
  logicalOp: z.enum(["AND", "OR"]).nullish(),
});

export const WriteDiffCapSchema = z.object({
  kind: z.literal("cap"),
  action: z.enum(["create", "update", "delete"]),
  id: z.string().nullish(),
  ownerType: z.enum(["campaign", "banner"]).nullish(),
  ownerId: z.string().nullish(),
  capType: z.enum(["daily_impressions", "total_impressions", "daily_budget", "total_budget"]).nullish(),
  value: z.union([z.string(), z.number()]).nullish(),
  /** Quando capType é budget, o valor deve ser MoneyWire */
  valueMoney: MoneyWireSchema.nullish(),
});

export const WriteDiffZoneLinkSchema = z.object({
  kind: z.literal("zone_link"),
  action: z.enum(["create", "delete"]),
  campaignId: z.string(),
  zoneId: z.string(),
});

// Union discriminada — suporta todos os tipos de escrita do copiloto
export const WriteDiffSchema = z.discriminatedUnion("kind", [
  WriteDiffCampaignSchema,
  WriteDiffBannerSchema,
  WriteDiffRuleSchema,
  WriteDiffCapSchema,
  WriteDiffZoneLinkSchema,
]);

export type WriteDiff = z.infer<typeof WriteDiffSchema>;
export type WriteDiffKind = WriteDiff["kind"];

// ---------------------------------------------------------------------------
// Eventos SSE emitidos pelo copiloto (via BFF proxy)
// Fonte: bff/src/routers/copilot.ts comentário "EVENTOS SSE"
// ---------------------------------------------------------------------------

export const SseTokenEventSchema = z.object({
  type: z.literal("token"),
  text: z.string(),
});

export const SseToolCallEventSchema = z.object({
  type: z.literal("tool_call"),
  tool: z.string(),
  status: z.enum(["running", "done"]),
  result: z.unknown().nullish(),
});

export const SseHitlRequiredEventSchema = z.object({
  type: z.literal("hitl_required"),
  thread_id: z.string(),
  diff: WriteDiffSchema,
  message: z.string(),
});

export const SseDoneEventSchema = z.object({
  type: z.literal("done"),
  session_id: z.string(),
  usage: z.object({
    input_tokens: z.number().int(),
    output_tokens: z.number().int(),
  }),
});

export const SseErrorEventSchema = z.object({
  type: z.literal("error"),
  message: z.string(),
});

export const SseEventSchema = z.discriminatedUnion("type", [
  SseTokenEventSchema,
  SseToolCallEventSchema,
  SseHitlRequiredEventSchema,
  SseDoneEventSchema,
  SseErrorEventSchema,
]);

export type SseEvent = z.infer<typeof SseEventSchema>;
export type SseTokenEvent = z.infer<typeof SseTokenEventSchema>;
export type SseToolCallEvent = z.infer<typeof SseToolCallEventSchema>;
export type SseHitlRequiredEvent = z.infer<typeof SseHitlRequiredEventSchema>;
export type SseDoneEvent = z.infer<typeof SseDoneEventSchema>;
export type SseErrorEvent = z.infer<typeof SseErrorEventSchema>;

/**
 * Parseia um evento SSE bruto (data: <json>) com segurança.
 * Retorna null se o JSON for inválido ou o tipo desconhecido.
 * NUNCA lança — o caller decide o que fazer com null.
 */
export function parseSseEvent(raw: string): SseEvent | null {
  try {
    const parsed: unknown = JSON.parse(raw);
    const result = SseEventSchema.safeParse(parsed);
    if (result.success) return result.data;
    return null;
  } catch {
    return null;
  }
}
