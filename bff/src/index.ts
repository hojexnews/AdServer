/**
 * BFF — entrypoint (Fase 1, I4).
 *
 * Monta o app tRPC com os adapters stub in-memory.
 * Em produção, substituir pelos adapters reais:
 *   - InMemoryConfigAdapter → PostgresConfigAdapter
 *   - InMemoryStatsAdapter  → ClickHouseStatsAdapter
 *
 * SEGURANÇA (CA-1 / TX-3):
 *   O tenant_id é resolvido em createContext() a partir da sessão,
 *   NUNCA de parâmetros do cliente. Cada query injeta o tenant_id
 *   via SET LOCAL adserver.tenant_id no Postgres (RLS) ou filtro
 *   explícito no ClickHouse.
 *
 * CSRF (M1):
 *   createContext recebe o método HTTP para verificar Origin/X-Requested-With
 *   em mutations POST (hitlApprove, hitlReject, chat).
 */

import { createHTTPServer } from "@trpc/server/adapters/standalone";
import { router } from "./lib/trpc.js";
import { createContext } from "./lib/context.js";
import { createConfigRouter } from "./routers/config.js";
import { createStatsRouter } from "./routers/stats.js";
import { createCopilotRouter } from "./routers/copilot.js";
import { createPaymentsRouter } from "./routers/payments.js";
import { InMemoryConfigAdapter } from "./adapters/in-memory-config.js";
import { InMemoryStatsAdapter } from "./adapters/in-memory-stats.js";
import { InMemoryPaymentsAdapter } from "./adapters/in-memory-payments.js";

// ---------------------------------------------------------------------------
// Adapters — substituir pelos reais em produção
// ---------------------------------------------------------------------------
const configAdapter = new InMemoryConfigAdapter();
const statsAdapter = new InMemoryStatsAdapter();
/**
 * K7: adapter de pagamentos in-memory.
 * Em produção: substituir por PostgresPaymentsAdapter que faz
 * SET LOCAL adserver.tenant_id e lê ledger.account_balances + journal_entries.
 */
const paymentsAdapter = new InMemoryPaymentsAdapter();

// ---------------------------------------------------------------------------
// App tRPC
// ---------------------------------------------------------------------------
export const appRouter = router({
  cfg: createConfigRouter(configAdapter),
  stats: createStatsRouter(statsAdapter),
  /**
   * Rota do copiloto (J5/Fase 2).
   * Protege a chave Claude, injeta tenant_id, faz proxy SSE para services/copilot.
   * ADR-0003 §C / TX-3 / §2.4.
   */
  copilot: createCopilotRouter(),
  /**
   * Rota de pagamentos — K7 (Fase 3).
   * Status/saldo somente leitura via BFF.
   * tenant_id injetado por ctx; sem cripto no cliente; sem segredos no payload.
   * ADR-0004 §C / §E.9 / §H / TX-2 / TX-3.
   */
  payments: createPaymentsRouter(paymentsAdapter),
});

export type AppRouter = typeof appRouter;

// ---------------------------------------------------------------------------
// HTTP server (standalone — em produção usar Next.js API route ou Fastify)
// ---------------------------------------------------------------------------
const PORT = parseInt(process.env["PORT"] ?? "3001", 10);

const server = createHTTPServer({
  router: appRouter,
  createContext: ({ req }) => {
    const headers: Record<string, string | string[] | undefined> = {};
    for (const [key, value] of Object.entries(req.headers)) {
      headers[key] = value as string | string[] | undefined;
    }
    // Passa o método HTTP para que createContext possa verificar CSRF em mutations (M1).
    // req.method pode ser undefined (Node.js IncomingMessage); passamos condicionalmente
    // para satisfazer exactOptionalPropertyTypes: method só está presente se não-undefined.
    const ctxReq: { headers: Record<string, string | string[] | undefined>; method?: string } = {
      headers,
    };
    if (req.method !== undefined) {
      ctxReq.method = req.method;
    }
    return createContext({ req: ctxReq });
  },
});

server.listen(PORT);
console.info(`BFF tRPC escutando em http://localhost:${PORT}`);
