/**
 * Adapter in-memory de estatísticas — STUB para Fase 1 (I4).
 *
 * Produz dados sintéticos que respeitam o contrato:
 *   - source = "consolidated" para queryConsolidated
 *   - source = "live" para queryLive
 *   - Os dois arrays NUNCA são misturados ou somados
 *
 * ONDE PLUGAR O ADAPTER REAL:
 *   O adapter real de "consolidated" executa:
 *     SELECT toStartOfHour(occurred_at) AS period_start,
 *            countIf(event_type = 'request')    AS requests,
 *            countIf(event_type = 'impression') AS impressions,
 *            countIf(event_type = 'click')      AS clicks,
 *            countIf(event_type = 'conversion') AS conversions
 *     FROM stats_hourly
 *     WHERE tenant_id = {tenantId} AND advertiser_id = {advertiserId}
 *       AND period_start BETWEEN {from} AND {to}
 *     GROUP BY period_start ORDER BY period_start
 *
 *   O adapter real de "live" executa query similar em live_stats_*
 *   e NÃO COMBINA os resultados com o consolidado.
 *
 *   inventoryLoss = requests - impressions (calculado no BFF, não no cliente)
 *   totalCost: string DECIMAL vindo do ClickHouse (DECIMAL(26,6) no CH)
 */

import type { StatsAdapter } from "./stats-adapter.js";
import type { KpiRow } from "../schemas/stats.js";

function fakeConsolidatedRows(from: Date, to: Date): KpiRow[] {
  const rows: KpiRow[] = [];
  const cursor = new Date(from);
  cursor.setMinutes(0, 0, 0);

  while (cursor <= to) {
    const periodEnd = new Date(cursor.getTime() + 3600_000);
    const requests = 1000 + Math.floor(Math.random() * 500);
    const impressions = Math.floor(requests * 0.82);
    const clicks = Math.floor(impressions * 0.035);
    const conversions = Math.floor(clicks * 0.12);
    const inventoryLoss = requests - impressions;
    const ctr = impressions > 0 ? (clicks / impressions).toFixed(5) : "0.00000";
    // cost = clicks * 0.50 BRL (stub CPC), em DECIMAL string — nunca Number
    const costUnits = (clicks * 50).toString(); // em centavos (scale=2)
    const costFull = (clicks * 50 / 100).toFixed(2);

    rows.push({
      periodStart: cursor.toISOString(),
      periodEnd: periodEnd.toISOString(),
      source: "consolidated",
      requests,
      impressions,
      clicks,
      conversions,
      inventoryLoss,
      ctr,
      totalCost: { amount: costFull, currency: "BRL" },
    });

    void costUnits; // usado acima, silencia lint
    cursor.setTime(cursor.getTime() + 3600_000);
  }
  return rows;
}

function fakeLiveRows(): KpiRow[] {
  const now = new Date();
  const periodStart = new Date(now.getTime() - 300_000); // últimos 5 min
  const requests = 42;
  const impressions = 35;
  const clicks = 2;
  const conversions = 0;
  const inventoryLoss = requests - impressions;
  const ctr = impressions > 0 ? (clicks / impressions).toFixed(5) : "0.00000";
  const costFull = (clicks * 50 / 100).toFixed(2);

  return [
    {
      periodStart: periodStart.toISOString(),
      periodEnd: now.toISOString(),
      source: "live",
      requests,
      impressions,
      clicks,
      conversions,
      inventoryLoss,
      ctr,
      totalCost: { amount: costFull, currency: "BRL" },
    },
  ];
}

export class InMemoryStatsAdapter implements StatsAdapter {
  async queryConsolidated(opts: {
    tenantId: string;
    advertiserId: string;
    from: Date;
    to: Date;
  }): Promise<{ rows: KpiRow[]; asOf: Date | null }> {
    void opts.tenantId;
    void opts.advertiserId;
    const rows = fakeConsolidatedRows(opts.from, opts.to);
    return { rows, asOf: new Date() };
  }

  async queryLive(opts: {
    tenantId: string;
    advertiserId: string;
  }): Promise<{ rows: KpiRow[]; asOf: Date | null }> {
    void opts.tenantId;
    void opts.advertiserId;
    return { rows: fakeLiveRows(), asOf: new Date() };
  }
}
