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
 * Parseia um evento SSE bruto com segurança. NUNCA lança — o caller decide o
 * que fazer com null.
 *
 * @param raw       o corpo do campo `data:` do frame SSE.
 * @param eventName o nome do evento SSE (campo `event:` do frame), disponível
 *   em cada `es.addEventListener("<nome>", ...)`. OBRIGATÓRIO desde a 31ª onda.
 *
 * ACHADO CRÍTICO (31ª onda, hitl-diff-schema-mismatch-drops-event, ampliado
 * pela revisão adversarial do próprio diff):
 *
 * O contrato do serviço copiloto põe o tipo no CAMPO `event:` do frame SSE, e
 * o payload de `data:` NÃO tem campo `type` —
 * `services/copilot/app/server.py:633 _sse(event, data)` monta
 * `event: <nome>\ndata: <json-sem-type>`. Exemplos reais emitidos:
 *   token         -> {"text": "..."}
 *   tool_call     -> {"tool": "...", "status": "...", "result": ...}
 *   hitl_required -> {"thread_id": "...", "diff": {...}, "message": "..."}
 *   done          -> {"session_id": "...", "thread_id": "...", "usage": {...}}
 *
 * Este parser, porém, validava com uma `discriminatedUnion("type", ...)` sobre
 * o payload — que EXIGE `type` dentro do data. Ou seja: TODO evento falhava o
 * parse e devolvia null, sempre, para todos os tipos. Como cada listener em
 * use-copilot-session.ts fazia `if (ev?.type === "...")`, o efeito era a
 * interface inteira do copiloto não fazer nada: nenhum token renderizado,
 * nenhuma ferramenta indicada, nenhum diálogo de aprovação HITL, nenhum
 * encerramento. Não era um caso de borda — era o caminho normal.
 *
 * A correção é reconstruir o tipo a partir do NOME DO EVENTO (a informação
 * que o SSE de fato carrega) antes de validar, mantendo os schemas Zod como
 * validação real da FORMA do payload.
 */
export function parseSseEvent(
  raw: string,
  eventName: string
): SseEvent | null {
  try {
    const parsed: unknown = JSON.parse(raw);
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
      return null;
    }
    // O tipo vem do frame SSE, não do corpo. Se o corpo trouxer um `type`
    // (contrato futuro), o nome do evento continua sendo a autoridade —
    // é ele que o navegador usou para despachar este listener.
    const withType = { ...(parsed as Record<string, unknown>), type: eventName };
    const result = SseEventSchema.safeParse(withType);
    return result.success ? result.data : null;
  } catch {
    return null;
  }
}
