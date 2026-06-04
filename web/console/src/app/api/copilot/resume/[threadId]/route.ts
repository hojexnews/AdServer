/**
 * GET /api/copilot/resume/[threadId]
 *
 * Rota HTTP raw (App Router) — proxy SSE de RETOMADA pós-HITL.
 * Chamada EXCLUSIVAMENTE após o usuário aprovar ou rejeitar via
 * copilot.hitlApprove / copilot.hitlReject (tRPC), que já enviou
 * Command(resume=...) ao grafo LangGraph pelo copiloto Python.
 *
 * UPSTREAM CORRETO (contrato services/copilot/app/server.py):
 *   POST /v1/chat/{thread_id}/resume   — ResumeRequest (body vazio ou {model_tier})
 *   NÃO usa POST /v1/chat (violaria min_length=1 e reiniciaria o grafo do START).
 *
 * FLUXO PÓS-HITL (contrato de reconexão SSE):
 *   1. Front recebe evento "hitl_required" no stream de /api/copilot/stream/:sessionId
 *      e fecha o EventSource.
 *   2. Front exibe UI de aprovação.
 *   3. Usuário aprova/rejeita → hook chama tRPC copilot.hitlApprove/hitlReject.
 *      O copiloto envia Command(resume=...) ao grafo.
 *   4. Hook abre EventSource para /api/copilot/resume/:threadId (ESTA rota).
 *   5. Esta rota chama POST /v1/chat/{threadId}/resume no copiloto Python.
 *   6. O copiloto retoma o grafo do interrupt e emite eventos SSE da continuação.
 *
 * SEGURANÇA (TX-3 / CA-1 / H4 — NÃO regride):
 *   - O middleware (src/middleware.ts) REMOVE todos os headers X-Adserver-* e X-Tenant-*
 *     que venham do browser ANTES de atingir esta rota (denylist).
 *   - O middleware injeta x-adserver-session-tenant EXCLUSIVAMENTE do cookie HttpOnly
 *     verificado criptograficamente.
 *   - Esta rota lê o tenant_id de x-adserver-session-tenant (server-side, não forjável).
 *   - threadId validado como UUID antes de repassar (early reject).
 *   - HMAC gerado com COPILOT_INTERNAL_SECRET (OpenBao) — NUNCA exposto ao front.
 *   - O copiloto Python verifica posse do thread (C1): state.tenant_id == X-Tenant-ID.
 *     Se não coincidir, 403 — o cliente nunca pode retomar thread de outro tenant.
 *
 * CSRF (M1 — NÃO regride):
 *   - EventSource (GET) não é mutação de estado — apenas leitura de stream.
 *   - A mutação real (Command(resume=...)) já ocorreu via tRPC hitlApprove/hitlReject
 *     com verificação de Origin no middleware.
 *   - Esta rota apenas faz proxy do stream pós-aprovação; não aplica escritas.
 *
 * Variáveis de ambiente (OpenBao):
 *   COPILOT_SERVICE_URL      — URL interna do copiloto (ex.: http://copilot:8001)
 *   COPILOT_INTERNAL_SECRET  — segredo HMAC compartilhado com o copiloto Python
 */

import { type NextRequest, NextResponse } from "next/server";
import crypto from "crypto";
import { z } from "zod";

// ---------------------------------------------------------------------------
// Config — lida de env (server-side only); NUNCA exposta ao front
// ---------------------------------------------------------------------------
const COPILOT_SERVICE_URL =
  process.env["COPILOT_SERVICE_URL"] ?? "http://localhost:8001";

const COPILOT_INTERNAL_SECRET = process.env["COPILOT_INTERNAL_SECRET"];

// ---------------------------------------------------------------------------
// Validação do threadId — deve ser UUID
// ---------------------------------------------------------------------------
const UuidSchema = z.string().uuid();

// ---------------------------------------------------------------------------
// Helper HMAC — idêntico ao de bff/src/routers/copilot.ts makeInternalHeaders()
// e ao de /api/copilot/stream/[sessionId]/route.ts — mantém paridade de assinatura.
// ---------------------------------------------------------------------------
function makeInternalHeaders(tenantId: string): Record<string, string> {
  const timestamp = Math.floor(Date.now() / 1000).toString();

  if (!COPILOT_INTERNAL_SECRET) {
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
// Handler GET — proxy SSE de retomada pós-HITL
// ---------------------------------------------------------------------------
export async function GET(
  request: NextRequest,
  { params }: { params: Promise<{ threadId: string }> }
): Promise<Response> {
  const { threadId } = await params;

  // Valida threadId — UUID obrigatório (espelho C1 do BFF)
  const parsed = UuidSchema.safeParse(threadId);
  if (!parsed.success) {
    return NextResponse.json({ error: "threadId inválido" }, { status: 400 });
  }

  // ---------------------------------------------------------------------------
  // Extrai tenant_id do header injetado pelo middleware (server-side).
  //
  // GARANTIA (H4): o middleware (src/middleware.ts) executa antes desta rota e:
  //   a) Remove todos os headers X-Adserver-* que venham do browser (denylist).
  //   b) Injeta x-adserver-session-tenant EXCLUSIVAMENTE do cookie HttpOnly
  //      verificado por HMAC/JWT.
  // Portanto, este header não pode ser forjado pelo browser.
  // ---------------------------------------------------------------------------
  const rawTenant = request.headers.get("x-adserver-session-tenant");
  if (!rawTenant) {
    return NextResponse.json(
      { error: "Sessão não autenticada" },
      { status: 401 }
    );
  }

  const tenantParsed = UuidSchema.safeParse(rawTenant);
  if (!tenantParsed.success) {
    return NextResponse.json(
      { error: "tenant_id inválido na sessão" },
      { status: 401 }
    );
  }

  const tenantId = tenantParsed.data;
  const internalHeaders = makeInternalHeaders(tenantId);

  // ---------------------------------------------------------------------------
  // Chama POST /v1/chat/{thread_id}/resume no copiloto Python.
  //
  // CONTRATO (services/copilot/app/server.py @app.post("/v1/chat/{thread_id}/resume")):
  //   - Body: ResumeRequest — vazio ou {model_tier: str|null}
  //   - NÃO exige "message" — é retomada do grafo pausado, não novo turno.
  //   - O grafo já recebeu Command(resume=...) via /v1/hitl/{thread_id}/approve.
  //   - O copiloto verifica state.tenant_id == X-Tenant-ID antes de transmitir (C1).
  //
  // X-Session-ID = threadId: permite ao LangGraph associar o request ao checkpoint.
  // ---------------------------------------------------------------------------
  let upstream: Response;
  try {
    upstream = await fetch(
      `${COPILOT_SERVICE_URL}/v1/chat/${encodeURIComponent(parsed.data)}/resume`,
      {
        method: "POST",
        headers: {
          ...internalHeaders,
          "X-Session-ID": parsed.data,
          "Content-Type": "application/json",
          Accept: "text/event-stream",
          "Cache-Control": "no-cache",
        },
        // ResumeRequest body: sem "message" — não viola min_length=1 de ChatRequest.
        // model_tier null = roteamento automático para o trecho pós-aprovação.
        body: JSON.stringify({ model_tier: null }),
        // @ts-expect-error — duplex requerido pelo Node.js para streaming POST
        duplex: "half",
        cache: "no-store",
      }
    );
  } catch {
    const errorBody = `event: error\ndata: ${JSON.stringify({
      type: "error",
      message: "Copiloto temporariamente indisponível ao retomar sessão.",
    })}\n\n`;
    return new Response(errorBody, {
      status: 200, // SSE: status 200 mesmo para erros lógicos
      headers: sseHeaders(),
    });
  }

  // Erro 403: thread não pertence ao tenant (C1 — posse verificada pelo copiloto)
  if (upstream.status === 403) {
    const errorBody = `event: error\ndata: ${JSON.stringify({
      type: "error",
      message: "Acesso negado ao thread da sessão.",
    })}\n\n`;
    return new Response(errorBody, {
      status: 200,
      headers: sseHeaders(),
    });
  }

  // Erro 404: thread não encontrado (sessão expirada ou threadId incorreto)
  if (upstream.status === 404) {
    const errorBody = `event: error\ndata: ${JSON.stringify({
      type: "error",
      message: "Sessão não encontrada. Inicie uma nova conversa.",
    })}\n\n`;
    return new Response(errorBody, {
      status: 200,
      headers: sseHeaders(),
    });
  }

  if (!upstream.ok || !upstream.body) {
    const errorBody = `event: error\ndata: ${JSON.stringify({
      type: "error",
      message: "Erro interno ao retomar o copiloto.",
    })}\n\n`;
    return new Response(errorBody, {
      status: 200,
      headers: sseHeaders(),
    });
  }

  // Faz proxy do stream de retomada diretamente — sem buffer
  return new Response(upstream.body, {
    status: 200,
    headers: sseHeaders(),
  });
}

function sseHeaders(): Record<string, string> {
  return {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache, no-transform",
    Connection: "keep-alive",
    // Nginx: desabilita buffer para que os eventos cheguem imediatamente
    "X-Accel-Buffering": "no",
    // Segurança
    "X-Content-Type-Options": "nosniff",
  };
}
