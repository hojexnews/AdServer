/**
 * bff/src/routers/copilot.ts — Rota do copiloto no BFF (J5/Fase 2).
 *
 * RESPONSABILIDADES DO BFF (ADR-0003 §C / TX-3):
 *   1. Autenticar o usuário (sessão/JWT) — o BFF é a ÚNICA fronteira de ACL.
 *   2. Injetar X-Tenant-ID, X-Session-ID, X-Internal-Timestamp e X-Internal-Signature
 *      nos headers da chamada para services/copilot — NUNCA do payload do cliente.
 *   3. Fazer proxy SSE: recebe o stream do copiloto e encaminha ao front via SSE.
 *   4. Receber aprovações/rejeições HITL do front e encaminhar ao copiloto.
 *   5. Proteger a chave ANTHROPIC_API_KEY — ela fica no copiloto Python;
 *      este BFF NUNCA a lê ou expõe.
 *
 * O COPILOTO PYTHON (services/copilot:8001) NUNCA É EXPOSTO AO CLIENTE.
 * Todo acesso passa por este BFF.
 *
 * FLUXO COMPLETO:
 *   Console (Next.js)
 *     └─► POST /trpc/copilot.chat (tRPC, autenticado)
 *           └─► BFF injeta tenant_id + HMAC
 *                 └─► POST services/copilot/v1/chat (interno)
 *                       └─► SSE stream ao front via este BFF
 *
 *   Aprovação HITL:
 *   Console (Next.js) → POST /trpc/copilot.hitlApprove
 *     └─► BFF → POST services/copilot/v1/hitl/{thread_id}/approve
 *
 * SEGURANÇA (TX-3):
 *   - tenant_id extraído de ctx.tenantId (sessão autenticada), NUNCA do body.
 *   - HMAC com COPILOT_INTERNAL_SECRET garante que o copiloto Python só aceita
 *     requests do BFF autenticado.
 *   - O body do request do cliente é passado como-está ao copiloto, mas o
 *     copiloto ignora qualquer tenant_id/credencial no body (TX-3).
 *
 * ESPELHO C1 (defense-in-depth — M1/C1):
 *   - hitlApprove e hitlReject SEMPRE encaminham ctx.tenantId ao copiloto.
 *   - O BFF rejeita qualquer threadId que não atenda à validação UUID antes de
 *     encaminhar ao copiloto (early reject, evita round-trip desnecessário).
 *   - O check autoritativo de posse thread_id↔tenant é feito pelo copiloto (C1).
 *     O BFF não enfraquece esse check: nunca aceita tenant_id do body do cliente.
 *
 * CREDENCIAL NECESSÁRIA:
 *   - COPILOT_SERVICE_URL: URL interna do copiloto (ex.: http://copilot:8001)
 *   - COPILOT_INTERNAL_SECRET: segredo HMAC (mesmo do CopilotSettings Python)
 *     Ambos no OpenBao; nunca no código ou no front.
 */

import { z } from "zod";
import crypto from "crypto";
import { router, tenantProcedure } from "../lib/trpc.js";
import { TRPCError } from "@trpc/server";

// ---------------------------------------------------------------------------
// Configuração interna (lida de env — nunca do cliente)
// ---------------------------------------------------------------------------
const COPILOT_SERVICE_URL =
  process.env["COPILOT_SERVICE_URL"] ?? "http://localhost:8001";

const COPILOT_INTERNAL_SECRET = process.env["COPILOT_INTERNAL_SECRET"];

// ---------------------------------------------------------------------------
// Helper: gera headers HMAC para autenticação interna BFF→copilot (TX-3)
//
// Exportado (só) para ser testável diretamente por copilot.test.ts — sem
// isto, a assinatura HMAC BFF→copilot ficava sem NENHUM teste (achado #15).
// ---------------------------------------------------------------------------
export function makeInternalHeaders(tenantId: string): Record<string, string> {
  const timestamp = Math.floor(Date.now() / 1000).toString();

  if (!COPILOT_INTERNAL_SECRET) {
    // Em dev/CI sem segredo configurado: usar modo sem HMAC (SKIP_AUTH_DEV=true no copiloto)
    return {
      "X-Tenant-ID": tenantId,
      "X-Internal-Timestamp": timestamp,
      "X-Internal-Signature": "dev-skip",
    };
  }

  const message = `${tenantId}:${timestamp}`;
  const signature = crypto
    .createHmac("sha256", COPILOT_INTERNAL_SECRET)
    .update(message)
    .digest("hex");

  return {
    "X-Tenant-ID": tenantId,
    "X-Internal-Timestamp": timestamp,
    "X-Internal-Signature": signature,
  };
}

// ---------------------------------------------------------------------------
// Schemas Zod — validação no BFF antes de encaminhar (TX-3)
// ---------------------------------------------------------------------------

const ChatInputSchema = z.object({
  message: z.string().min(1).max(10_000),
  /**
   * Tier de modelo explícito solicitado pelo usuário.
   * O BFF repassa ao copiloto, que ainda aplica o gating de custo.
   * null = roteamento automático.
   */
  modelTier: z.enum(["haiku", "sonnet", "opus"]).nullish(),
  /**
   * ID de sessão para checkpointing. Se null, o copiloto gera um novo.
   * O BFF deve persistir o sessionId retornado para reconexão SSE.
   */
  sessionId: z.string().uuid().nullish(),
});

const HitlApproveInputSchema = z.object({
  /**
   * threadId deve ser UUID (alinhado com o sessionId gerado pelo BFF).
   * Validação UUID aqui é o pré-check de posse do BFF (espelho C1):
   * rejeita formatos inválidos antes de consultar o copiloto.
   */
  threadId: z.string().uuid(),
  approved: z.boolean(),
  reason: z.string().max(500).default(""),
});

const HitlRejectInputSchema = z.object({
  /** Mesma validação UUID que hitlApprove (espelho C1). */
  threadId: z.string().uuid(),
  reason: z.string().max(500),
});

const SessionStateInputSchema = z.object({
  sessionId: z.string().uuid(),
});

// ---------------------------------------------------------------------------
// Router tRPC do copiloto
// ---------------------------------------------------------------------------

export function createCopilotRouter() {
  return router({
    /**
     * copilot.chat — Inicia ou continua uma sessão de chat com o copiloto.
     *
     * RETORNO: Este endpoint retorna uma URL de SSE que o front deve consumir.
     * O streaming real acontece via GET /api/copilot/stream/{sessionId}.
     *
     * ALTERNATIVA: usar uma subscription tRPC se o BFF evoluir para WebSocket.
     * Por ora SSE é o canal único (§2.5).
     *
     * NOTA PARA O FRONTEND-BFF-ENGINEER:
     *   O front deve:
     *   1. Chamar este mutation para obter o sessionId e threadId.
     *   2. Abrir um EventSource para /api/copilot/stream/{sessionId}.
     *   3. Ao receber evento "hitl_required", exibir o diff ao usuário.
     *   4. Chamar copilot.hitlApprove ou copilot.hitlReject com threadId.
     */
    chat: tenantProcedure
      .input(ChatInputSchema)
      .mutation(async ({ ctx, input }) => {
        const tenantId = ctx.tenantId; // SEMPRE da sessão, nunca do input
        const sessionId = input.sessionId ?? crypto.randomUUID();

        const internalHeaders = makeInternalHeaders(tenantId);

        try {
          // Validação prévia: o copiloto está up?
          const healthResp = await fetch(`${COPILOT_SERVICE_URL}/health`, {
            headers: internalHeaders,
          });

          if (!healthResp.ok) {
            throw new TRPCError({
              code: "SERVICE_UNAVAILABLE",
              message: "Copiloto temporariamente indisponível. Tente novamente.",
            });
          }
        } catch (err) {
          if (err instanceof TRPCError) throw err;
          throw new TRPCError({
            code: "SERVICE_UNAVAILABLE",
            message: "Não foi possível conectar ao copiloto.",
          });
        }

        // A mensagem é passada como query param porque EventSource não aceita body.
        // A rota SSE extrai ?message= e encaminha ao POST /v1/chat do copiloto
        // (ChatRequest exige min_length=1 — body vazio causaria 422).
        const streamUrl = `/api/copilot/stream/${sessionId}?message=${encodeURIComponent(input.message)}`;

        return {
          sessionId,
          // O front usa esta URL para abrir o EventSource SSE
          streamUrl,
          // O copiloto Python usa este como thread_id para o checkpointing
          threadId: sessionId,
        };
      }),

    /**
     * copilot.hitlApprove — Aprova a escrita pendente de HITL.
     *
     * Chamado pelo front quando o usuário clica em "Aplicar" no diff review.
     * O BFF encaminha para o copiloto Python que retoma o grafo pausado.
     *
     * INVARIANTE (TX-3 / espelho C1):
     *   - tenant_id vem de ctx.tenantId (sessão), NUNCA do body.
     *   - threadId é validado como UUID pelo schema Zod (pré-check do BFF).
     *   - O BFF SEMPRE encaminha ctx.tenantId ao copiloto via X-Tenant-ID (HMAC).
     *   - O check autoritativo de posse thread_id↔tenant é feito pelo copiloto (C1).
     *     O BFF não enfraquece esse check; apenas rejeita UUIDs malformados cedo.
     *   - Em produção: adicionar verificação no Redis/checkpointer que
     *     thread_id.tenant == tenantId antes de chamar o copiloto.
     */
    hitlApprove: tenantProcedure
      .input(HitlApproveInputSchema)
      .mutation(async ({ ctx, input }) => {
        // ESPELHO C1: tenantId vem EXCLUSIVAMENTE da sessão autenticada.
        // Nunca do input do cliente. O copiloto recebe tenantId via HMAC (X-Tenant-ID).
        const tenantId = ctx.tenantId;

        const headers = {
          ...makeInternalHeaders(tenantId), // injeta X-Tenant-ID com HMAC
          "Content-Type": "application/json",
          "X-Session-ID": input.threadId,
        };

        const resp = await fetch(
          `${COPILOT_SERVICE_URL}/v1/hitl/${encodeURIComponent(input.threadId)}/approve`,
          {
            method: "POST",
            headers,
            body: JSON.stringify({
              approved: input.approved,
              reason: input.reason,
            }),
          }
        );

        if (!resp.ok) {
          // Drena o corpo sem usá-lo: o undici mantém a conexão presa até o
          // body ser consumido ou cancelado. Ao parar de logar o corpo (fix de
          // privacidade desta onda) a leitura sumiu junto e vazava socket a
          // cada erro upstream — regressão pega pela revisão do próprio diff
          // (COPILOT-BODY-NOT-DRAINED).
          await resp.body?.cancel().catch(() => undefined);
          const corrId = crypto.randomUUID();
          // FIX (31ª onda, bff-hitl-upstream-body-log-unredacted): o corpo cru
          // do upstream era logado inteiro. Numa falha do copiloto esse corpo
          // pode ecoar o WriteDiff (nomes de campanha, valores monetários) e a
          // mensagem do anunciante — dado de tenant indo parar em log
          // centralizado sem redação nem retenção controlada (TX-5/DA-11).
          // Logamos só o que é acionável para diagnóstico: status e correlação.
          // O corpo, quando necessário, sai do log do próprio serviço copiloto,
          // que já aplica a redação dele.
          console.error("hitlApprove.upstream_error", {
            corrId,
            status: resp.status,
          });
          throw new TRPCError({
            code: "INTERNAL_SERVER_ERROR",
            message: `Falha ao processar aprovação HITL. ID: ${corrId}`,
          });
        }

        const data = (await resp.json()) as {
          status: string;
          thread_id: string;
          decision: string;
        };

        return {
          status: data.status,
          threadId: data.thread_id,
          decision: data.decision,
        };
      }),

    /**
     * copilot.hitlReject — Rejeita a escrita pendente de HITL.
     *
     * INVARIANTE (TX-3 / espelho C1): idêntico ao hitlApprove.
     *   - tenant_id SEMPRE de ctx.tenantId; NUNCA do body.
     *   - threadId validado como UUID pelo schema.
     *   - X-Tenant-ID encaminhado ao copiloto via HMAC.
     */
    hitlReject: tenantProcedure
      .input(HitlRejectInputSchema)
      .mutation(async ({ ctx, input }) => {
        // ESPELHO C1: tenantId vem EXCLUSIVAMENTE da sessão autenticada.
        const tenantId = ctx.tenantId;

        const headers = {
          ...makeInternalHeaders(tenantId), // injeta X-Tenant-ID com HMAC
          "Content-Type": "application/json",
          "X-Session-ID": input.threadId,
        };

        const resp = await fetch(
          `${COPILOT_SERVICE_URL}/v1/hitl/${encodeURIComponent(input.threadId)}/reject`,
          {
            method: "POST",
            headers,
            body: JSON.stringify({ reason: input.reason }),
          }
        );

        if (!resp.ok) {
          throw new TRPCError({
            code: "INTERNAL_SERVER_ERROR",
            message: "Falha ao rejeitar a operação HITL.",
          });
        }

        return { status: "rejected", threadId: input.threadId };
      }),

    /**
     * copilot.sessionState — Retorna o estado público da sessão.
     * Usado pelo front para recuperar o estado após reconexão SSE.
     */
    sessionState: tenantProcedure
      .input(SessionStateInputSchema)
      .query(async ({ ctx, input }) => {
        const tenantId = ctx.tenantId;
        const headers = {
          ...makeInternalHeaders(tenantId),
          "X-Session-ID": input.sessionId,
        };

        const resp = await fetch(
          `${COPILOT_SERVICE_URL}/v1/session/${encodeURIComponent(input.sessionId)}/state`,
          { headers }
        );

        if (resp.status === 404) {
          throw new TRPCError({
            code: "NOT_FOUND",
            message: `Sessão '${input.sessionId}' não encontrada.`,
          });
        }

        if (!resp.ok) {
          throw new TRPCError({
            code: "INTERNAL_SERVER_ERROR",
            message: "Erro ao recuperar estado da sessão.",
          });
        }

        return (await resp.json()) as {
          sessionId: string;
          tenantId: string;
          pendingDiff: unknown | null;
          hitlApproved: boolean | null;
          nextAction: string | null;
          inputTokensUsed: number;
          outputTokensUsed: number;
        };
      }),
  });
}

/**
 * NOTA PARA O FRONTEND-BFF-ENGINEER:
 *
 * ROTAS SSE (HTTP raw — não tRPC):
 *
 *   GET /api/copilot/stream/:sessionId
 *     Inicia o stream SSE de uma nova sessão de chat.
 *     Upstream: POST /v1/chat (copiloto Python) com a mensagem do usuário.
 *     Implementado em web/console/src/app/api/copilot/stream/[sessionId]/route.ts.
 *
 *   GET /api/copilot/resume/:threadId
 *     Retoma o stream SSE pós-HITL (após copilot.hitlApprove / copilot.hitlReject).
 *     Upstream: POST /v1/chat/{thread_id}/resume (copiloto Python) — sem "message".
 *     NÃO usa /v1/chat com body vazio (violaria min_length=1 e reiniciaria o grafo).
 *     Implementado em web/console/src/app/api/copilot/resume/[threadId]/route.ts.
 *
 * UPSTREAM CORRETO (H4 — não regride):
 *   POST /v1/chat         → stream inicial (ChatRequest: message obrigatório, min_length=1)
 *   POST /v1/chat/{id}/resume → re-stream pós-HITL (ResumeRequest: sem message)
 *   O header X-Session-ID carrega o thread_id para checkpointing LangGraph.
 *
 * EVENTOS SSE EMITIDOS PELO COPILOTO (a UI deve consumir):
 *   event: token     → {"text": "..."}  (streaming de texto do agente)
 *   event: tool_call → {"tool": "...", "status": "running"|"done", "result": {...}}
 *   event: hitl_required → {"thread_id": "...", "diff": WriteDiff, "message": "..."}
 *   event: done      → {"session_id": "...", "usage": {"input_tokens": N, "output_tokens": N}}
 *   event: error     → {"message": "..."}
 *
 * FLUXO COMPLETO PÓS-HITL:
 *   1. Pausar o stream (fechar o EventSource ao receber "hitl_required")
 *   2. Exibir o diff ao usuário com botões "Aplicar" e "Cancelar"
 *   3. Chamar copilot.hitlApprove ou copilot.hitlReject (tRPC)
 *   4. Reabrir EventSource para /api/copilot/resume/:threadId (NÃO para /stream/:sessionId)
 */
