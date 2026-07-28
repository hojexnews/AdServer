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
  /**
   * Requisições de anúncio no período, ou `null` quando a fonte não consegue
   * ATRIBUIR requests a este recorte.
   *
   * `null` não é "zero requisições": um ad request ocorre ANTES da escolha de
   * campanha (o collector emite `AdRequest` em `/asyncjs`, antes de qualquer
   * candidato existir), portanto ele nunca carrega `campaign_id`. Na fonte
   * "ao vivo" do perfil BETA (`stats.live_kpis`) esses eventos caem numa linha
   * própria por tenant com `campaign_id IS NULL` e não são atribuíveis a um
   * anunciante — atribuí-los seria invenção.
   *
   * O valor anterior (`requests = impressions`, que zerava `inventoryLoss`)
   * era um número fabricado exibido com rótulo de fato — a mesma classe que a
   * 31ª onda removeu do dashboard. Preferimos a lacuna visível.
   */
  requests: z.number().int().nonnegative().nullable(),
  impressions: z.number().int().nonnegative(),
  clicks: z.number().int().nonnegative(),
  conversions: z.number().int().nonnegative(),

  /**
   * Perda de inventário = requests - impressions (CA-6), ou `null` quando
   * `requests` é `null` (não derivável sem o minuendo).
   * Calculada no BFF para evitar aritmética no cliente.
   * É contagem de inteiros, não dinheiro, portanto seguro calcular aqui.
   */
  inventoryLoss: z.number().int().nonnegative().nullable(),

  /** CTR = clicks / impressions — entregue como string "0.0523" (5 decimais). */
  ctr: z.string().regex(/^\d+\.\d+$/),

  /**
   * Custo total no período (string DECIMAL + currency, TX-2), ou `null`
   * quando a fonte NÃO RASTREIA custo.
   *
   * `null` não é "custo zero". A série "ao vivo" do perfil BETA (ADR-0005)
   * não tem ligação alguma com o ledger: não há gasto a reportar. Emitir
   * `"0.00"` ali renderizava `R$ 0,00` ao lado de impressões e cliques
   * reais — indistinguível de "não gastei nada", e pior que as demais
   * lacunas por carregar símbolo de moeda. O `money-ledger-guardian`
   * bloqueou a onda "perfil BETA" exatamente neste ponto.
   */
  totalCost: MoneySchema.nullable(),
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
  /**
   * Mensagem do erro REAL quando a fonte consolidada não pôde ser consultada
   * (ex.: perfil BETA sem ClickHouse/Iceberg — ADR-0005). `null` quando a
   * consulta teve sucesso, inclusive quando retornou zero linhas.
   *
   * Existe porque `consolidated: []` sozinho é ambíguo: indistinguível de
   * "zero eventos faturáveis no período". Sem este campo, ausência de fonte
   * seria exibida como se fosse ausência de faturamento — a mesma classe de
   * meia-verdade que a 31ª onda removeu do dashboard (CA-6).
   */
  consolidatedUnavailable: z.string().nullable(),
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
