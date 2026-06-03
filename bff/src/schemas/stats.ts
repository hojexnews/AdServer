/**
 * Schemas de estatísticas — contrato de dados com o ClickHouse.
 *
 * INVARIANTE ADR-0001: "ao vivo" (live) e "consolidado ≤1h" (consolidated)
 * são fontes DISTINTAS. O BFF NUNCA soma as duas. Cada resposta carrega
 * source: 'consolidated' | 'live' para que a UI exiba o rótulo correto.
 *
 * Dinheiro: MoneySchema (string DECIMAL + currency). Nunca Number.
 */

import { z } from "zod";
import { MoneySchema } from "./money.js";
import { IdSchema } from "./config.js";

export const StatsSourceSchema = z.enum(["consolidated", "live"]);
export type StatsSource = z.infer<typeof StatsSourceSchema>;

// ---------------------------------------------------------------------------
// KPI snapshot — uma linha de métricas por (advertiser, intervalo, source)
// ---------------------------------------------------------------------------

export const KpiRowSchema = z.object({
  /** Início do bucket de tempo (ISO-8601). */
  periodStart: z.string().datetime(),
  /** Fim do bucket de tempo (ISO-8601). */
  periodEnd: z.string().datetime(),
  /**
   * Fonte dos dados — NUNCA misturar com a outra fonte.
   * consolidated = stats_hourly (ClickHouse, ≤1h, faturável)
   * live         = live_stats_* (ao vivo, não-faturável)
   */
  source: StatsSourceSchema,

  // Métricas brutas (inteiros — contagem sem float)
  requests: z.number().int().nonnegative(),
  impressions: z.number().int().nonnegative(),
  clicks: z.number().int().nonnegative(),
  conversions: z.number().int().nonnegative(),

  /**
   * Perda de inventário = requests - impressions (CA-6).
   * Calculada no BFF para evitar aritmética no cliente.
   * É contagem de inteiros, não dinheiro, portanto seguro calcular aqui.
   */
  inventoryLoss: z.number().int().nonnegative(),

  /** CTR = clicks / impressions — entregue como string "0.0523" (5 decimais). */
  ctr: z.string().regex(/^\d+\.\d+$/),

  /** Custo total no período (string DECIMAL + currency, TX-2). */
  totalCost: MoneySchema,
});

export type KpiRow = z.infer<typeof KpiRowSchema>;

// ---------------------------------------------------------------------------
// Resposta de dashboard por anunciante
// ---------------------------------------------------------------------------

export const DashboardResponseSchema = z.object({
  advertiserId: IdSchema,
  /**
   * Dados consolidados (stats_hourly, ≤1h). Podem estar ausentes se
   * não houver dados no período.
   */
  consolidated: z.array(KpiRowSchema),
  /**
   * Dados ao vivo (live_stats_*). Snapshot do momento atual.
   * NÃO DEVEM SER SOMADOS com consolidated (ADR-0001).
   */
  live: z.array(KpiRowSchema),
  /** Timestamp da última atualização consolidada (ISO-8601). */
  consolidatedAsOf: z.string().datetime().nullable(),
  /** Timestamp da última atualização ao vivo (ISO-8601). */
  liveAsOf: z.string().datetime().nullable(),
});

export type DashboardResponse = z.infer<typeof DashboardResponseSchema>;

export const DashboardQuerySchema = z.object({
  advertiserId: IdSchema,
  /** Início do período (ISO-8601). Aplicado ao consolidado. */
  from: z.string().datetime(),
  /** Fim do período (ISO-8601). */
  to: z.string().datetime(),
});

export type DashboardQuery = z.infer<typeof DashboardQuerySchema>;
