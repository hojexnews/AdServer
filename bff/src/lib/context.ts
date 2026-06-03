/**
 * Contexto tRPC — resolve tenant_id server-side (CA-1 / TX-3).
 *
 * INVARIANTE DE SEGURANÇA:
 *   O tenant_id NUNCA vem de parâmetros do cliente (query string, body,
 *   header enviado pelo browser). Ele é resolvido exclusivamente a partir
 *   da sessão autenticada no servidor.
 *
 *   Nesta fase (stub de auth), o tenant_id é lido de um header de sessão
 *   assinado (X-Adserver-Session-Tenant). Em produção, substituir por:
 *     - cookie de sessão HTTP-only + verificação JWT/PASETO assinada
 *     - ou token de serviço M2M com claims de tenant
 *
 *   O procedimento de criação de contexto é o único lugar no código que
 *   lê o tenant_id. Os routers consomem ctx.tenantId e não aceitam
 *   parâmetros de tenant dos clientes.
 */

import { z } from "zod";

const SESSION_HEADER = "x-adserver-session-tenant";

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

/**
 * Cria o contexto tRPC a partir do request HTTP.
 *
 * Stub de auth: lê tenant_id do header de sessão X-Adserver-Session-Tenant.
 * Em produção, este header seria definido pelo middleware de autenticação
 * (Next.js middleware ou edge auth), nunca pelo cliente.
 *
 * Se o header estiver ausente ou inválido, lança 401.
 */
export function createContext(opts: {
  req: { headers: Record<string, string | string[] | undefined> };
}): TrpcContext {
  const rawTenant = opts.req.headers[SESSION_HEADER];
  const rawUser = opts.req.headers["x-adserver-session-user"];

  const tenantStr = Array.isArray(rawTenant) ? rawTenant[0] : rawTenant;
  const userStr = Array.isArray(rawUser) ? rawUser[0] : rawUser;

  const tenantParsed = UuidSchema.safeParse(tenantStr);
  if (!tenantParsed.success) {
    throw new Error(
      "UNAUTHORIZED: tenant_id ausente ou inválido na sessão. " +
        "Nenhum dado de tenant pode ser enviado pelo cliente."
    );
  }

  return {
    tenantId: tenantParsed.data,
    userId: userStr ?? "anonymous",
  };
}
