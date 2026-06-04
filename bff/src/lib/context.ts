/**
 * Contexto tRPC — resolve tenant_id server-side (CA-1 / TX-3).
 *
 * INVARIANTE DE SEGURANÇA (TX-3):
 *   O tenant_id NUNCA vem de parâmetros do cliente (query string, body,
 *   header enviado pelo browser). Ele é resolvido exclusivamente a partir
 *   da sessão autenticada no servidor.
 *
 *   Nesta fase (stub de auth), o tenant_id é lido de um header de sessão
 *   assinado (X-Adserver-Session-Tenant). Em produção, este header é injetado
 *   EXCLUSIVAMENTE pelo middleware Next.js (web/console/src/middleware.ts) que
 *   o deriva do cookie HttpOnly verificado criptograficamente.
 *   O middleware também remove (denylist) qualquer X-Adserver-* / X-Tenant-*
 *   enviado pelo browser antes que o BFF receba o request.
 *
 * CSRF (M1 — corrigido):
 *   O BFF verifica o header Origin nas mutations (tRPC usa POST).
 *   Mutations sem Origin ou com Origin fora da allow-list retornam 403.
 *   Isso previne CSRF em hitlApprove/hitlReject/chat quando a sessão é por cookie.
 *   A allow-list é configurada via ALLOWED_ORIGINS (CSV) — mesma do middleware.
 *
 *   Adicionalmente, o cliente tRPC (lib/trpc.ts) envia o header
 *   X-Requested-With: XMLHttpRequest em todas as chamadas (ver lib/trpc.ts).
 *   O BFF exige esse header como segunda linha de defesa CSRF.
 */

import { z } from "zod";
import { TRPCError } from "@trpc/server";

const SESSION_HEADER = "x-adserver-session-tenant";

// ---------------------------------------------------------------------------
// CSRF allow-list (M1)
// ---------------------------------------------------------------------------

/**
 * Origens permitidas para mutations tRPC.
 * Em produção: definir ALLOWED_ORIGINS como CSV no OpenBao.
 * Deve ser idêntica à allow-list do middleware Next.js.
 */
const ALLOWED_ORIGINS: ReadonlySet<string> = new Set(
  (process.env["ALLOWED_ORIGINS"] ?? "http://localhost:3000")
    .split(",")
    .map((o) => o.trim())
    .filter(Boolean)
);

/**
 * Headers HTTP que indicam requests mutation (POST do tRPC).
 * tRPC standalone usa POST para mutations e GET para queries.
 * Queries (GET) não são state-changing, portanto CSRF check só em POST.
 */
function isMutationRequest(method: string | undefined): boolean {
  return method?.toUpperCase() === "POST";
}

// ---------------------------------------------------------------------------
// Tipos
// ---------------------------------------------------------------------------

/**
 * Forma do contexto injetado em todos os procedimentos tRPC.
 */
export interface TrpcContext {
  /**
   * Tenant UUID resolvido da sessão autenticada.
   * Injetado nas queries de dados (SET LOCAL adserver.tenant_id = ...).
   * NUNCA lido do corpo do request ou parâmetros do cliente.
   */
  tenantId: string;
  /**
   * ID do usuário autenticado (para logs de auditoria).
   */
  userId: string;
}

const UuidSchema = z.string().uuid();

// ---------------------------------------------------------------------------
// createContext
// ---------------------------------------------------------------------------

/**
 * Cria o contexto tRPC a partir do request HTTP.
 *
 * Stub de auth: lê tenant_id do header de sessão X-Adserver-Session-Tenant.
 * Em produção, este header é definido EXCLUSIVAMENTE pelo middleware Next.js
 * (web/console/src/middleware.ts) após verificação criptográfica do cookie.
 * O middleware remove (denylist) qualquer header X-Adserver-* do browser.
 *
 * CSRF (M1): mutations POST verificam Origin contra ALLOWED_ORIGINS e exigem
 * X-Requested-With para impedir CSRF cross-site.
 */
export function createContext(opts: {
  req: {
    headers: Record<string, string | string[] | undefined>;
    method?: string;
  };
}): TrpcContext {
  const { headers, method } = opts.req;

  // ---- CSRF check (M1) — mutations apenas ----
  if (isMutationRequest(method)) {
    const origin = getHeader(headers, "origin");
    const requestedWith = getHeader(headers, "x-requested-with");

    // Se Origin está presente (sempre presente em requests de outros domínios),
    // deve estar na allow-list.
    if (origin !== undefined && !ALLOWED_ORIGINS.has(origin)) {
      throw new TRPCError({
        code: "FORBIDDEN",
        message:
          "CSRF: Origin não permitida. Acesso negado.",
      });
    }

    // X-Requested-With: segunda linha de defesa CSRF.
    // O cliente tRPC envia este header; browsers cross-site não enviam
    // headers customizados sem preflight (CORS).
    if (requestedWith?.toLowerCase() !== "xmlhttprequest") {
      throw new TRPCError({
        code: "FORBIDDEN",
        message:
          "CSRF: header X-Requested-With ausente ou inválido.",
      });
    }
  }

  // ---- Resolve tenant_id da sessão ----
  const rawTenant = headers[SESSION_HEADER];
  const rawUser = headers["x-adserver-session-user"];

  const tenantStr = Array.isArray(rawTenant) ? rawTenant[0] : rawTenant;
  const userStr = Array.isArray(rawUser) ? rawUser[0] : rawUser;

  const tenantParsed = UuidSchema.safeParse(tenantStr);
  if (!tenantParsed.success) {
    throw new TRPCError({
      code: "UNAUTHORIZED",
      message:
        "UNAUTHORIZED: tenant_id ausente ou inválido na sessão. " +
        "Nenhum dado de tenant pode ser enviado pelo cliente.",
    });
  }

  return {
    tenantId: tenantParsed.data,
    userId: userStr ?? "anonymous",
  };
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

function getHeader(
  headers: Record<string, string | string[] | undefined>,
  name: string
): string | undefined {
  const val = headers[name];
  return Array.isArray(val) ? val[0] : val;
}
