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
 *
 * FIX (onda "perfil beta" — correção de escopo do coordenador): `dashboard`
 * usava `Promise.all([queryConsolidated(...), queryLive(...)])`. No perfil
 * beta, `queryConsolidated` REJEITA sempre (não existe fonte consolidada/
 * faturável — ver adapters/unconfigured-stats.ts e adapters/postgres-
 * stats.ts). Com `Promise.all`, essa rejeição fazia o `Promise.all` inteiro
 * rejeitar, e o "ao vivo" — que agora É real — nunca chegava à tela: o
 * dashboard continuava quebrado mesmo depois de existir dado ao vivo de
 * verdade.
 *
 * `Promise.allSettled` trata as duas fontes de forma independente:
 *   - consolidada indisponível -> `consolidated: []` + `consolidatedUnavailable`
 *     (mensagem do erro real, propagada — nunca uma cópia hardcoded do texto
 *     de UnconfiguredStatsAdapter). `consolidated: []` sozinho SERIA um
 *     meia-verdade (indistinguível de "zero eventos faturáveis no período" —
 *     CA-6); o campo `consolidatedUnavailable` não-nulo é o que torna a
 *     ausência de dado auditável em vez de silenciosa.
 *   - "ao vivo" falhando de verdade (erro de conexão/query real, não a
 *     recusa deliberada) CONTINUA propagando como erro do procedimento —
 *     nunca mascaramos uma falha real de banco como "indisponível".
 */

import { router, tenantProcedure } from "../lib/trpc.js";
import type { StatsAdapter } from "../adapters/stats-adapter.js";
import { DashboardQuerySchema } from "../schemas/stats.js";

/** Mensagem de erro legível a partir de um `reason` de Promise rejeitada. */
function messageOf(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason);
}

export function createStatsRouter(adapter: StatsAdapter) {
  return router({
    /**
     * dashboard: retorna KPIs consolidados (≤1h) e ao vivo, SEPARADOS.
     *
     * Contrato de resposta:
     *   consolidated: KpiRow[] — fonte stats_hourly, source="consolidated".
     *     Array vazio quando a fonte está indisponível OU quando não há
     *     eventos no período — os dois casos NÃO são o mesmo evento: quando
     *     indisponível, `consolidatedUnavailable` vem preenchido.
     *   live: KpiRow[] — fonte live_stats_* / stats.live_kpis, source="live".
     *   consolidatedUnavailable: string | null — mensagem (reusada do erro
     *     real lançado pelo adapter, nunca copiada) quando a fonte
     *     consolidada não pôde ser consultada. `null` quando a consulta foi
     *     bem-sucedida (mesmo que tenha retornado zero linhas).
     *
     * A UI NUNCA deve somar consolidated + live.
     * inventoryLoss = requests - impressions, calculado no BFF.
     *
     * `queryLive` falhando de verdade (não é o caso beta — é erro de
     * infraestrutura) propaga como erro do procedimento; não é capturado
     * aqui como "indisponível".
     */
    dashboard: tenantProcedure
      .input(DashboardQuerySchema)
      .query(async ({ ctx, input }) => {
        const [consolidatedResult, liveResult] = await Promise.allSettled([
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

        // "ao vivo" falhando de verdade DEVE propagar — nunca mascarar uma
        // falha real de banco/conexão como "indisponível" (só a fonte
        // consolidada tem uma degradação documentada e esperada no perfil
        // beta).
        if (liveResult.status === "rejected") {
          throw liveResult.reason;
        }

        const consolidated =
          consolidatedResult.status === "fulfilled"
            ? consolidatedResult.value.rows
            : [];
        const consolidatedAsOf =
          consolidatedResult.status === "fulfilled"
            ? (consolidatedResult.value.asOf?.toISOString() ?? null)
            : null;
        // Estruturalmente DIFERENTE de "consolidated: [] porque zero linhas
        // no período" (CA-6) — array vazio + este campo não-nulo é o único
        // jeito de o cliente distinguir as duas situações.
        const consolidatedUnavailable =
          consolidatedResult.status === "rejected"
            ? messageOf(consolidatedResult.reason)
            : null;

        return {
          advertiserId: input.advertiserId,
          // Fontes estritamente separadas — ADR-0001
          consolidated,
          consolidatedUnavailable,
          live: liveResult.value.rows,
          consolidatedAsOf,
          liveAsOf: liveResult.value.asOf?.toISOString() ?? null,
        };
      }),
  });
}
