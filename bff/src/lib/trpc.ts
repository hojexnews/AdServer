/**
 * Inicialização do tRPC v11 — instância compartilhada.
 */

import { initTRPC, TRPCError } from "@trpc/server";
import type { TrpcContext } from "./context.js";

const t = initTRPC.context<TrpcContext>().create();

export const router = t.router;
export const publicProcedure = t.procedure;

/**
 * Middleware que garante que o contexto de tenant está presente.
 * Todos os procedimentos de dados usam tenantProcedure.
 */
const ensureTenant = t.middleware(({ ctx, next }) => {
  if (!ctx.tenantId) {
    throw new TRPCError({
      code: "UNAUTHORIZED",
      message: "tenant_id ausente na sessão. Acesso negado.",
    });
  }
  return next({ ctx });
});

export const tenantProcedure = t.procedure.use(ensureTenant);
