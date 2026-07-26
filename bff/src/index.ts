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
import type { Pool as PgPool } from "pg";
import { router } from "./lib/trpc.js";
import { createContext } from "./lib/context.js";
import { createConfigRouter } from "./routers/config.js";
import { createStatsRouter } from "./routers/stats.js";
import { createCopilotRouter } from "./routers/copilot.js";
import { createPaymentsRouter } from "./routers/payments.js";
import { InMemoryConfigAdapter } from "./adapters/in-memory-config.js";
import { PostgresConfigAdapter } from "./adapters/postgres-config.js";
import type { ConfigAdapter } from "./adapters/config-adapter.js";
import { InMemoryStatsAdapter } from "./adapters/in-memory-stats.js";
import { PostgresStatsAdapter } from "./adapters/postgres-stats.js";
import type { StatsAdapter } from "./adapters/stats-adapter.js";
import {
  UnconfiguredStatsAdapter,
  syntheticStatsAllowed,
} from "./adapters/unconfigured-stats.js";
import { InMemoryPaymentsAdapter } from "./adapters/in-memory-payments.js";
import {
  PostgresPaymentsAdapter,
  createPgPool,
} from "./adapters/postgres-payments.js";
import type { PaymentsAdapter } from "./adapters/payments-adapter.js";

// ---------------------------------------------------------------------------
// Adapters — substituir pelos reais em produção
// ---------------------------------------------------------------------------
// pgPool compartilhado: com BFF_PG_DSN definido, config + payments usam o
// Postgres real (o MESMO schema `config` que o motor de decisão lê no snapshot),
// fechando o laço console → decisão. Sem DSN, caem no stub in-memory (dev/CI).
const pgPool = createPgPool();

const configAdapter: ConfigAdapter = pgPool
  ? new PostgresConfigAdapter(pgPool)
  : new InMemoryConfigAdapter();
/**
 * Stats: com BFF_PG_DSN configurado, lê dados REAIS de stats.live_kpis no
 * Postgres (onda "perfil beta" — sem ClickHouse ainda; queryConsolidated
 * continua recusando, sem fonte faturável neste perfil — ver
 * adapters/postgres-stats.ts). Sem BFF_PG_DSN, comportamento intacto: NÃO
 * podemos servir `Math.random()` rotulado como "Consolidado ≤1h /
 * faturável" na UI — em produção, sem opt-in explícito, o dashboard falha
 * de forma visível em vez de fabricar número de cobrança. Ver
 * adapters/unconfigured-stats.ts para o racional completo (31ª onda).
 *
 * Extraída como função pura (em vez de inline) para ser testável por
 * import direto sem subir o servidor HTTP — ver
 * adapters/postgres-stats.test.ts ("Fiação em bff/src/index.ts").
 */
export function pickStatsAdapter(
  pool: PgPool | undefined,
  nodeEnv: string | undefined,
  allowSyntheticStats: string | undefined
): StatsAdapter {
  if (pool) return new PostgresStatsAdapter(pool);
  return syntheticStatsAllowed(nodeEnv, allowSyntheticStats)
    ? new InMemoryStatsAdapter()
    : new UnconfiguredStatsAdapter();
}

const statsAdapter: StatsAdapter = pickStatsAdapter(
  pgPool,
  process.env["NODE_ENV"],
  process.env["ALLOW_SYNTHETIC_STATS"]
);

/**
 * K7: adapter de pagamentos — Postgres real quando BFF_PG_DSN configurado;
 * InMemory como fallback de dev/teste (sem Postgres disponível).
 *
 * Seleção por config (padrão do repo):
 *   - BFF_PG_DSN presente → PostgresPaymentsAdapter (produção / staging).
 *   - BFF_PG_DSN ausente  → InMemoryPaymentsAdapter (dev / CI sem banco).
 *
 * O adapter Postgres:
 *   - SELECT set_config('adserver.tenant_id', $tenant, true) dentro da transação
 *     (ativa RLS K3/0003; SET LOCAL = $1 não vale — SET não aceita bind params).
 *   - Lê ledger.account_balances + journal_entries + postings.
 *   - TX-2: NUMERIC → string DECIMAL (sem Number, sem aritmética monetária).
 *   - TX-3: tenantId SEMPRE do ctx (sessão), nunca do cliente.
 *   - DA-11: DSN nunca exposto no payload; PII descartada.
 */
const paymentsAdapter: PaymentsAdapter = pgPool
  ? new PostgresPaymentsAdapter(pgPool)
  : new InMemoryPaymentsAdapter();

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
