/**
 * GET /api/copilot/stream/[sessionId]
 *
 * Rota HTTP raw (App Router) — proxy SSE do copiloto para o front.
 * Esta rota NAO é tRPC: SSE exige streaming de resposta que tRPC não suporta.
 *
 * FLUXO:
 *   Front (EventSource) → esta rota Next.js → BFF/serviço copiloto Python
 *   → LangGraph
 *
 * SEGURANÇA (TX-3 / CA-1 / H4 corrigido):
 *   - O middleware (src/middleware.ts) REMOVE todos os headers X-Adserver-* e X-Tenant-*
 *     que venham do browser ANTES de atingir esta rota (denylist).
 *   - O middleware injeta X-Adserver-Session-Tenant EXCLUSIVAMENTE a partir do
 *     cookie de sessão HttpOnly verificado criptograficamente.
 *   - Esta rota lê o tenant_id DE x-adserver-session-tenant que, neste ponto,
 *     é garantidamente server-side (middleware Edge Runtime, não o browser).
 *   - Dupla verificação: o header é re-validado como UUID aqui.
 *   - HMAC gerado com COPILOT_INTERNAL_SECRET (OpenBao) — NUNCA exposto ao front.
 *   - A chave ANTHROPIC_API_KEY fica no copiloto Python; este arquivo nunca a lê.
 *   - sessionId da URL é validado como UUID antes de repassar.
 *   - Cache-Control: no-cache + X-Accel-Buffering: no para garantir streaming.
 *
 * ENDPOINT UPSTREAM (H4 corrigido):
 *   O services/copilot expõe POST /v1/chat (SSE streaming).
 *   NÃO existe /v1/stream/:sessionId — endpoint corrigido para POST /v1/chat
 *   com X-Session-ID no header para checkpointing (retomar grafo pausado).
 *   Contrato confirmado em services/copilot/app/server.py.
 *
 * INVARIANTE:
 *   Esta rota apenas faz proxy do stream SSE. Não inicia escritas, não aprova HITL.
 *   A aprovação HITL é feita via tRPC copilot.hitlApprove (bff/src/routers/copilot.ts).
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
// Validação do sessionId — deve ser UUID
// ---------------------------------------------------------------------------
const UuidSchema = z.string().uuid();

// ---------------------------------------------------------------------------
// Helper HMAC — alinhado com bff/src/routers/copilot.ts makeInternalHeaders()
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
// Handler GET — proxy SSE
// ---------------------------------------------------------------------------
export async function GET(
  request: NextRequest,
  { params }: { params: Promise<{ sessionId: string }> }
): Promise<Response> {
  const { sessionId } = await params;

  // Valida sessionId
  const parsed = UuidSchema.safeParse(sessionId);
  if (!parsed.success) {
    return NextResponse.json({ error: "sessionId inválido" }, { status: 400 });
  }

  // ---------------------------------------------------------------------------
  // Extrai tenant_id do header injetado pelo middleware (server-side).
  //
  // GARANTIA (H4): o middleware (src/middleware.ts) executa antes desta rota e:
  //   a) Remove todos os headers X-Adserver-* que venham do browser (denylist).
  //   b) Injeta x-adserver-session-tenant EXCLUSIVAMENTE do cookie HttpOnly
  //      verificado por HMAC/JWT.
  // Portanto, este header não pode ser forjado pelo browser.
  // A re-validação UUID abaixo é defense-in-depth.
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
  // Lê a mensagem do usuário passada pelo BFF via query param ?message=.
  //
  // FLUXO (1º turno):
  //   1. Front chama tRPC copilot.chat com a mensagem do usuário.
  //   2. BFF constrói streamUrl = /api/copilot/stream/{sessionId}?message=<encoded>.
  //   3. Front abre EventSource para streamUrl.
  //   4. Esta rota extrai ?message= e encaminha ao POST /v1/chat do copiloto.
  //
  // O query param é a única forma de passar dados num GET/EventSource sem body.
  // O BFF (copilot.chat tRPC) é quem inclui o message na URL — o front nunca
  // constrói esta URL diretamente; recebe-a como opaque do BFF.
  //
  // ChatRequest (server.py) exige min_length=1 — garantido pela validação abaixo.
  // ---------------------------------------------------------------------------
  const rawMessage = request.nextUrl.searchParams.get("message");
  if (!rawMessage || rawMessage.trim().length === 0) {
    const errorBody = `event: error\ndata: ${JSON.stringify({ type: "error", message: "Parâmetro 'message' ausente ou vazio." })}\n\n`;
    return new Response(errorBody, { status: 200, headers: sseHeaders() });
  }
  // Limita a 10 000 chars (espelho do ChatInput.message.max no BFF/Zod e ChatRequest Python)
  const message = rawMessage.slice(0, 10_000);

  // ---------------------------------------------------------------------------
  // Abre o stream SSE com o copiloto Python.
  //
  // ENDPOINT: POST /v1/chat (ChatRequest: message obrigatório, min_length=1).
  // O header X-Session-ID carrega o thread_id para checkpointing LangGraph.
  // Contrato: services/copilot/app/server.py @app.post("/v1/chat")
  // ---------------------------------------------------------------------------
  let upstream: Response;
  try {
    upstream = await fetch(`${COPILOT_SERVICE_URL}/v1/chat`, {
      method: "POST",
      headers: {
        ...internalHeaders,
        "X-Session-ID": parsed.data,
        "Content-Type": "application/json",
        Accept: "text/event-stream",
        "Cache-Control": "no-cache",
      },
      body: JSON.stringify({ message, model_tier: null }),
      // Next.js fetch: desabilita cache para streaming
      // @ts-expect-error — duplex requerido pelo Node.js para streaming POST
      duplex: "half",
      cache: "no-store",
    });
  } catch {
    // Serviço indisponível — emite evento SSE de erro ao front
    const errorBody = `event: error\ndata: ${JSON.stringify({ type: "error", message: "Copiloto temporariamente indisponível." })}\n\n`;
    return new Response(errorBody, {
      status: 200, // SSE: status 200 mesmo para erros lógicos
      headers: sseHeaders(),
    });
  }

  if (!upstream.ok || !upstream.body) {
    const errorBody = `event: error\ndata: ${JSON.stringify({ type: "error", message: "Erro interno do copiloto." })}\n\n`;
    return new Response(errorBody, {
      status: 200,
      headers: sseHeaders(),
    });
  }

  // Faz proxy do stream diretamente — sem buffer
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
