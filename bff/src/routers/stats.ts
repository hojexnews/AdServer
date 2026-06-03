/**
 * Router de estatísticas — dashboards por anunciante.
 *
 * INVARIANTE ADR-0001:
 *   O BFF retorna { consolidated: [...], live: [...] } SEPARADOS.
 *   NUNCA mescla ou soma as duas fontes.
 *   Cada row carrega source: "consolidated" | "live".
 *
 * A UI é responsável por exibir o rótulo correto e NUNCA somar as fontes.
 *
 * Money: totalCost como { amount: string, currency: string } (TX-2).
 */

import { router, tenantProcedure } from "../lib/trpc.js";
import type { StatsAdapter } from "../adapters/stats-adapter.js";
import { DashboardQuerySchema } from "../schemas/stats.js";

export function createStatsRouter(adapter: StatsAdapter) {
  return router({
    /**
     * dashboard: retorna KPIs consolidados (≤1h) e ao vivo, SEPARADOS.
     *
     * Contrato de resposta:
     *   consolidated: KpiRow[] — fonte stats_hourly, source="consolidated"
     *   live:         KpiRow[] — fonte live_stats_*, source="live"
     *
     * A UI NUNCA deve somar consolidated + live.
     * inventoryLoss = requests - impressions, calculado no BFF.
     */
    dashboard: tenantProcedure
      .input(DashboardQuerySchema)
      .query(async ({ ctx, input }) => {
        const [consolidatedResult, liveResult] = await Promise.all([
          adapter.queryConsolidated({
            tenantId: ctx.tenantId,
            advertiserId: input.advertiserId,
            from: new Date(input.from),
            to: new Date(input.to),
          }),
          adapter.queryLive({
            tenantId: ctx.tenantId,
            advertiserId: input.advertiserId,
          }),
        ]);

        return {
          advertiserId: input.advertiserId,
          // Fontes estritamente separadas — ADR-0001
          consolidated: consolidatedResult.rows,
          live: liveResult.rows,
          consolidatedAsOf: consolidatedResult.asOf?.toISOString() ?? null,
          liveAsOf: liveResult.asOf?.toISOString() ?? null,
        };
      }),
  });
}
