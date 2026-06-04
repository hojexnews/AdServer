/**
 * GET /api/copilot/stream/[sessionId]
 *
 * Rota HTTP raw (App Router) — proxy SSE do BFF para o front.
 * Esta rota NAO é tRPC: SSE exige streaming de resposta que tRPC nao suporta nativamente.
 *
 * FLUXO:
 *   Front (EventSource) → esta rota Next.js → BFF standalone (POST /v1/chat ou SSE)
 *   → services/copilot Python → LangGraph
 *
 * SEGURANÇA (TX-3 / security-reviewer):
 *   - tenant_id extraído do header de sessão X-Adserver-Session-Tenant (server-side).
 *   - NUNCA lido de query string, body ou cookie enviado pelo cliente.
 *   - HMAC gerado com COPILOT_INTERNAL_SECRET (OpenBao) — NUNCA exposto ao front.
 *   - A chave ANTHROPIC_API_KEY fica no copiloto Python; este arquivo nunca a lê.
 *   - sessionId da URL é validado como UUID antes de repassar.
 *   - Cache-Control: no-cache + X-Accel-Buffering: no para garantir streaming.
 *
 * INVARIANTE:
 *   Esta rota apenas faz proxy do stream SSE. Nao inicia escritas, nao aprova HITL.
 *   A aprovação HITL é feita via tRPC copilot.hitlApprove (bff/src/routers/copilot.ts).
 *
 * Para backend vivo:
 *   - COPILOT_SERVICE_URL: URL interna do copiloto (ex.: http://copilot:8001)
 *   - COPILOT_INTERNAL_SECRET: segredo HMAC compartilhado com o copiloto Python
 *   Ambos via OpenBao; ausentes em dev → modo dev-skip (SKIP_AUTH_DEV=true no copiloto).
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

  // Extrai tenant_id do header de sessão (server-side — NUNCA do cliente)
  // Em produção: middleware de auth injeta X-Adserver-Session-Tenant
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

  // Abre o stream SSE com o copiloto Python via BFF
  // O copiloto usa X-Session-ID para retomar o grafo pausado (checkpointing)
  let upstream: Response;
  try {
    upstream = await fetch(`${COPILOT_SERVICE_URL}/v1/stream/${parsed.data}`, {
      method: "GET",
      headers: {
        ...internalHeaders,
        "X-Session-ID": parsed.data,
        Accept: "text/event-stream",
        "Cache-Control": "no-cache",
      },
      // Next.js fetch: desabilita cache para streaming
      cache: "no-store",
    });
  } catch {
    // Serviço indisponível — emite evento SSE de erro ao front
    const errorBody =
      `event: error\ndata: ${JSON.stringify({ type: "error", message: "Copiloto temporariamente indisponível." })}\n\n`;
    return new Response(errorBody, {
      status: 200, // SSE: status 200 mesmo para erros lógicos
      headers: sseHeaders(),
    });
  }

  if (!upstream.ok || !upstream.body) {
    const errorBody =
      `event: error\ndata: ${JSON.stringify({ type: "error", message: "Erro interno do copiloto." })}\n\n`;
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
