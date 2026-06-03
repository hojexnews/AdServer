/**
 * Interface do adapter de estatísticas — fronteira com o ClickHouse.
 *
 * Duas tabelas, NUNCA misturadas:
 *   stats_hourly   → source = "consolidated" (≤1h, faturável)
 *   live_stats_*   → source = "live"          (ao vivo, não-faturável)
 *
 * O adapter real usa o driver @clickhouse/client com filtros de tenant_id
 * explícitos em cada query (ClickHouse não tem RLS nativo — a filtragem
 * é responsabilidade do adapter).
 *
 * ADR-0001: o BFF retorna os dois arrays SEPARADOS; nunca os mescla.
 */

import type { KpiRow } from "../schemas/stats.js";

export interface StatsAdapter {
  /**
   * Busca métricas consolidadas do stats_hourly (ClickHouse).
   * source = "consolidated". Período: [from, to].
   * Agrega por hora. Filtra por tenantId e advertiserId.
   */
  queryConsolidated(opts: {
    tenantId: string;
    advertiserId: string;
    from: Date;
    to: Date;
  }): Promise<{ rows: KpiRow[]; asOf: Date | null }>;

  /**
   * Busca snapshot ao vivo de live_stats_* (ClickHouse).
   * source = "live". Retorna o estado mais recente.
   * NÃO DEVE SER SOMADO com queryConsolidated.
   */
  queryLive(opts: {
    tenantId: string;
    advertiserId: string;
  }): Promise<{ rows: KpiRow[]; asOf: Date | null }>;
}
