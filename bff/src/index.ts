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
 */

import { createHTTPServer } from "@trpc/server/adapters/standalone";
import { router } from "./lib/trpc.js";
import { createContext } from "./lib/context.js";
import { createConfigRouter } from "./routers/config.js";
import { createStatsRouter } from "./routers/stats.js";
import { InMemoryConfigAdapter } from "./adapters/in-memory-config.js";
import { InMemoryStatsAdapter } from "./adapters/in-memory-stats.js";

// ---------------------------------------------------------------------------
// Adapters — substituir pelos reais em produção
// ---------------------------------------------------------------------------
const configAdapter = new InMemoryConfigAdapter();
const statsAdapter = new InMemoryStatsAdapter();

// ---------------------------------------------------------------------------
// App tRPC
// ---------------------------------------------------------------------------
export const appRouter = router({
  cfg: createConfigRouter(configAdapter),
  stats: createStatsRouter(statsAdapter),
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
    return createContext({ req: { headers } });
  },
});

server.listen(PORT);
console.info(`BFF tRPC escutando em http://localhost:${PORT}`);
