"use client";

/**
 * Dashboard de KPIs por anunciante.
 *
 * INVARIANTE ADR-0001:
 *   - "Consolidado ≤1h" e "Ao vivo" são exibidos em seções SEPARADAS.
 *   - NUNCA somamos as duas fontes.
 *   - Cada seção tem rótulo visual + DataSourceBadge.
 *   - inventoryLoss = requests - impressions (calculado no BFF, CA-6).
 *
 * Money: totalCost vem como MoneyWire (string DECIMAL + currency).
 * Formatado com MoneyDisplay (Intl.NumberFormat via decimal.js).
 * NUNCA Number() para dinheiro.
 */

import { useState } from "react";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import { trpc } from "@/lib/trpc";
import { DataSourceBadge, DataSourceDisclaimer } from "@/components/ui/status-badge";
import { MoneyDisplay } from "@/components/ui/money-display";
import { LoadingState, ErrorState, EmptyState } from "@/components/ui/empty-state";
import { formatCount, formatCtr } from "@/lib/money";

// Período padrão: últimas 24h
const DEFAULT_FROM = new Date(Date.now() - 86400_000).toISOString();
const DEFAULT_TO = new Date().toISOString();

// Advertiser ID stub — em produção vem do contexto da sessão
const STUB_ADVERTISER_ID = "1";

export default function DashboardPage() {
  const [from] = useState(DEFAULT_FROM);
  const [to] = useState(DEFAULT_TO);

  const { data, isLoading, error, refetch } = trpc.stats.dashboard.useQuery({
    advertiserId: STUB_ADVERTISER_ID,
    from,
    to,
  });

  if (isLoading) return <LoadingState label="Carregando dashboard..." />;
  if (error)
    return (
      <ErrorState
        message={error.message}
        retry={() => { void refetch(); }}
      />
    );
  if (!data) return null;

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900">Dashboard de KPIs</h1>
      <p className="mt-1 text-sm text-gray-500">
        Anunciante #{data.advertiserId} — últimas 24h
      </p>

      {/* Aviso de separação de fontes — ADR-0001 */}
      <div className="mt-4">
        <DataSourceDisclaimer />
      </div>

      {/* ================================================================
          Seção 1: Dados CONSOLIDADOS (≤1h, faturáveis)
          NÃO misturar com a seção live abaixo.
          ================================================================ */}
      <section aria-labelledby="consolidated-heading" className="mt-8">
        <div className="flex items-center gap-3">
          <h2
            id="consolidated-heading"
            className="text-lg font-semibold text-gray-900"
          >
            Dados consolidados
          </h2>
          <DataSourceBadge source="consolidated" asOf={data.consolidatedAsOf} />
        </div>
        <p className="text-sm text-gray-500">
          Fonte: stats_hourly — atualização a cada hora. Valores faturáveis.
        </p>

        {data.consolidated.length === 0 ? (
          <EmptyState
            title="Sem dados consolidados no período"
            description="Os dados consolidados são gerados a cada hora pelo pipeline de dados."
          />
        ) : (
          <KpiSection rows={data.consolidated} />
        )}
      </section>

      {/* ================================================================
          Seção 2: Dados AO VIVO (não-faturáveis)
          SEPARADO do consolidado — NUNCA somar com a seção acima.
          ================================================================ */}
      <section
        aria-labelledby="live-heading"
        className="mt-10 border-t border-gray-200 pt-8"
      >
        <div className="flex items-center gap-3">
          <h2
            id="live-heading"
            className="text-lg font-semibold text-gray-900"
          >
            Dados ao vivo
          </h2>
          <DataSourceBadge source="live" asOf={data.liveAsOf} />
        </div>
        <p className="text-sm text-gray-500">
          Fonte: live_stats_* — snapshot em tempo real. NÃO faturável.
          NÃO somar com dados consolidados.
        </p>

        {data.live.length === 0 ? (
          <EmptyState
            title="Sem dados ao vivo disponíveis"
            description="O snapshot ao vivo atualiza a cada minuto."
          />
        ) : (
          <KpiSection rows={data.live} />
        )}
      </section>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Componente de seção de KPIs — usado para ambas as fontes (separadamente)
// ---------------------------------------------------------------------------

interface KpiRow {
  periodStart: string;
  periodEnd: string;
  source: "consolidated" | "live";
  requests: number;
  impressions: number;
  clicks: number;
  conversions: number;
  inventoryLoss: number;
  ctr: string;
  totalCost: { amount: string; currency: string };
}

function KpiSection({ rows }: { rows: KpiRow[] }) {
  // Totais por seção (soma dentro da MESMA fonte — ok)
  const totals = rows.reduce(
    (acc, row) => ({
      requests: acc.requests + row.requests,
      impressions: acc.impressions + row.impressions,
      clicks: acc.clicks + row.clicks,
      conversions: acc.conversions + row.conversions,
      inventoryLoss: acc.inventoryLoss + row.inventoryLoss,
    }),
    { requests: 0, impressions: 0, clicks: 0, conversions: 0, inventoryLoss: 0 }
  );

  // CTR médio (cálculo de display, não faturamento)
  const avgCtr =
    totals.impressions > 0
      ? formatCtr(String(totals.clicks / totals.impressions))
      : "0,00%";

  // Custo total da primeira row (stub — em produção somamos strings via decimal.js no BFF)
  const firstRow = rows[0];

  const chartData = rows.map((r) => ({
    period: new Date(r.periodStart).toLocaleTimeString("pt-BR", {
      hour: "2-digit",
      minute: "2-digit",
    }),
    Requests: r.requests,
    Impressões: r.impressions,
    Cliques: r.clicks,
    "Perda inventário": r.inventoryLoss,
  }));

  return (
    <div className="mt-4">
      {/* KPI cards */}
      <dl className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
        <KpiCard label="Requests" value={formatCount(totals.requests)} />
        <KpiCard label="Impressões" value={formatCount(totals.impressions)} />
        <KpiCard label="Cliques" value={formatCount(totals.clicks)} />
        <KpiCard label="Conversões" value={formatCount(totals.conversions)} />
        <KpiCard
          label="Perda inventário"
          value={formatCount(totals.inventoryLoss)}
          highlight="warn"
          aria-description="Requests sem impressão (CA-6)"
        />
      </dl>

      {/* CTR e custo */}
      <dl className="mt-4 flex gap-6">
        <div>
          <dt className="text-sm text-gray-500">CTR médio</dt>
          <dd className="font-semibold text-gray-900">{avgCtr}</dd>
        </div>
        {firstRow && (
          <div>
            <dt className="text-sm text-gray-500">
              Custo total (primeira hora — stub)
            </dt>
            <dd className="font-semibold text-gray-900">
              <MoneyDisplay money={firstRow.totalCost} />
            </dd>
          </div>
        )}
      </dl>

      {/* Gráfico de barras (Recharts) */}
      <div
        className="mt-6 h-64 w-full"
        role="img"
        aria-label="Gráfico de barras: requests, impressões, cliques e perda de inventário por período"
      >
        <ResponsiveContainer width="100%" height="100%">
          <BarChart
            data={chartData}
            margin={{ top: 0, right: 16, left: 0, bottom: 0 }}
          >
            <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
            <XAxis
              dataKey="period"
              tick={{ fontSize: 11, fill: "#6b7280" }}
              tickLine={false}
            />
            <YAxis tick={{ fontSize: 11, fill: "#6b7280" }} tickLine={false} />
            <Tooltip
              contentStyle={{
                fontSize: 12,
                borderRadius: 6,
                border: "1px solid #e5e7eb",
              }}
            />
            <Legend wrapperStyle={{ fontSize: 12 }} />
            <Bar dataKey="Requests" fill="#93c5fd" radius={[2, 2, 0, 0]} />
            <Bar dataKey="Impressões" fill="#6ee7b7" radius={[2, 2, 0, 0]} />
            <Bar dataKey="Cliques" fill="#fcd34d" radius={[2, 2, 0, 0]} />
            <Bar
              dataKey="Perda inventário"
              fill="#fca5a5"
              radius={[2, 2, 0, 0]}
            />
          </BarChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}

interface KpiCardProps {
  label: string;
  value: string;
  highlight?: "warn" | "ok";
  "aria-description"?: string;
}

function KpiCard({
  label,
  value,
  highlight,
  "aria-description": ariaDesc,
}: KpiCardProps) {
  return (
    <div
      className={[
        "rounded-lg border p-4",
        highlight === "warn"
          ? "border-amber-200 bg-amber-50"
          : "border-gray-200 bg-white",
      ].join(" ")}
    >
      <dt className="text-xs font-medium text-gray-500 uppercase tracking-wide">
        {label}
      </dt>
      {ariaDesc && <span className="sr-only">{ariaDesc}</span>}
      <dd className="mt-1 text-2xl font-bold text-gray-900">{value}</dd>
    </div>
  );
}
