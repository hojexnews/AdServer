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
import { useTheme } from "@/components/theme-provider";
import { consolidatedSectionState } from "@/lib/dashboard-source-state";

// Período padrão do RECORTE CONSOLIDADO: últimas 24h.
//
// M-5 (onda "perfil BETA"): `from`/`to` só se aplicam à seção consolidada —
// `queryLive` (StatsAdapter, ver bff/src/adapters/stats-adapter.ts) NUNCA
// recebeu `from`/`to` na assinatura e o PostgresStatsAdapter agrega
// stats.live_kpis por INTEIRO (stats.events_raw não tem retenção). Rotular a
// seção "ao vivo" com esta mesma janela seria inventar uma precisão que a
// consulta não tem — por isso o rótulo de período abaixo é escopado
// explicitamente ao card consolidado, e a seção "ao vivo" descreve a si
// mesma como acumulado sem janela (ver texto da seção 2 abaixo).
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
      <h1 className="text-2xl font-bold text-foreground">Dashboard de KPIs</h1>
      <p className="mt-1 text-sm text-muted-foreground">
        Anunciante #{data.advertiserId}
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
            className="text-lg font-semibold text-foreground"
          >
            Dados consolidados
          </h2>
          <DataSourceBadge source="consolidated" asOf={data.consolidatedAsOf} />
        </div>
        <p className="text-sm text-muted-foreground">
          Fonte: stats_hourly — atualização a cada hora. Valores faturáveis.
          Período: últimas 24h.
        </p>

        {/*
          H-3 (onda "perfil BETA"): `consolidatedUnavailable` distingue "a
          fonte consolidada não pôde ser consultada" (fail-closed real, ver
          UnconfiguredStatsAdapter) de "a fonte respondeu e não há eventos no
          período" (CA-6 legítimo). `consolidatedSectionState` (lib/
          dashboard-source-state.ts) é a ÚNICA fonte da decisão — ver o teste
          de mutação em dashboard-source-state.test.ts. Renderizar
          "Sem dados consolidados no período" quando a fonte está
          indisponível seria uma meia-verdade fabricada: o anunciante leria
          "não houve faturamento" quando a verdade é "não sabemos, porque
          nenhum backend está configurado".
        */}
        {(() => {
          const state = consolidatedSectionState(data);
          switch (state.kind) {
            case "unavailable":
              return (
                <ErrorState
                  title="Fonte consolidada indisponível"
                  message={state.message}
                  retry={() => { void refetch(); }}
                />
              );
            case "empty":
              return (
                <EmptyState
                  title="Sem dados consolidados no período"
                  description="Os dados consolidados são gerados a cada hora pelo pipeline de dados."
                />
              );
            case "rows":
              return <KpiSection rows={data.consolidated} />;
          }
        })()}
      </section>

      {/* ================================================================
          Seção 2: Dados AO VIVO (não-faturáveis)
          SEPARADO do consolidado — NUNCA somar com a seção acima.
          ================================================================ */}
      <section
        aria-labelledby="live-heading"
        className="mt-10 border-t border-border pt-8"
      >
        <div className="flex items-center gap-3">
          <h2
            id="live-heading"
            className="text-lg font-semibold text-foreground"
          >
            Dados ao vivo
          </h2>
          <DataSourceBadge source="live" asOf={data.liveAsOf} />
        </div>
        <p className="text-sm text-muted-foreground">
          Fonte: live_stats_* — acumulado desde o início do registro de
          eventos, SEM janela de tempo (não é um recorte de &ldquo;últimas
          24h&rdquo;). NÃO faturável. NÃO somar com dados consolidados.
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
  /**
   * `null` = NÃO ATRIBUÍVEL nesta fonte, jamais "zero requisições".
   * Um ad request precede a escolha de campanha (não carrega campaign_id),
   * então a fonte "ao vivo" não o atribui a um anunciante. Exibimos "—".
   */
  requests: number | null;
  impressions: number;
  clicks: number;
  conversions: number;
  /** `null` quando `requests` é null — perda não é derivável sem o minuendo. */
  inventoryLoss: number | null;
  ctr: string;
  /**
   * `null` = esta fonte NÃO RASTREIA custo (não é "custo zero"). A série
   * "ao vivo" não tem ligação com o ledger; exibimos "—", nunca R$ 0,00
   * ao lado de impressões reais.
   */
  totalCost: { amount: string; currency: string } | null;
}

function KpiSection({ rows }: { rows: KpiRow[] }) {
  const { resolved } = useTheme();
  const isDark = resolved === "dark";

  // Totais por seção (soma dentro da MESMA fonte — ok).
  //
  // requests/inventoryLoss podem vir `null` (não atribuíveis nesta fonte). NÃO
  // os coagimos para 0: `null + 0 === 0` em JS exibiria "0" com cara de
  // medição. Somamos só o que existe e mantemos `null` quando NENHUMA linha
  // trouxe valor — o card então mostra "—".
  const sumNullable = (get: (r: KpiRow) => number | null): number | null => {
    const present = rows.map(get).filter((v): v is number => v !== null);
    return present.length === 0
      ? null
      : present.reduce((a, b) => a + b, 0);
  };

  const totals = {
    requests: sumNullable((r) => r.requests),
    impressions: rows.reduce((a, r) => a + r.impressions, 0),
    clicks: rows.reduce((a, r) => a + r.clicks, 0),
    conversions: rows.reduce((a, r) => a + r.conversions, 0),
    inventoryLoss: sumNullable((r) => r.inventoryLoss),
  };

  /** "—" para métrica não atribuível; número formatado quando existe. */
  const formatCountOrDash = (v: number | null) =>
    v === null ? "—" : formatCount(v);

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
    // Sem coerção `?? 0`: passamos o null adiante.
    //
    // A versão anterior coagia null→0 "porque uma barra ausente não pode ser
    // desenhada". O privacy-compliance-auditor verificou no código do Recharts
    // (Rectangle retorna null quando height===0 OU quando o valor não é
    // numérico) e a justificativa era factualmente errada: 0 e null desenham
    // exatamente a mesma coisa — nada. A coerção não comprava nada visualmente
    // e só injetava um número fabricado no tooltip ("Requests: 0"), enquanto o
    // card ao lado, corretamente, mostra "—".
    Requests: r.requests,
    Impressões: r.impressions,
    Cliques: r.clicks,
    "Perda inventário": r.inventoryLoss,
  }));

  return (
    <div className="mt-4">
      {/* KPI cards */}
      <dl className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
        <KpiCard
          label="Requests"
          value={formatCountOrDash(totals.requests)}
          aria-description={
            totals.requests === null
              ? "Não atribuível nesta fonte: um ad request precede a escolha de campanha"
              : undefined
          }
        />
        <KpiCard label="Impressões" value={formatCount(totals.impressions)} />
        <KpiCard label="Cliques" value={formatCount(totals.clicks)} />
        <KpiCard label="Conversões" value={formatCount(totals.conversions)} />
        <KpiCard
          label="Perda inventário"
          value={formatCountOrDash(totals.inventoryLoss)}
          highlight="warn"
          aria-description={
            totals.inventoryLoss === null
              ? "Não atribuível nesta fonte: depende de requests por anunciante"
              : "Requests sem impressão (CA-6)"
          }
        />
      </dl>

      {/* CTR e custo */}
      <dl className="mt-4 flex gap-6">
        <div>
          <dt className="text-sm text-muted-foreground">CTR médio</dt>
          <dd className="font-semibold text-foreground">{avgCtr}</dd>
        </div>
        {firstRow && (
          <div>
            <dt className="text-sm text-muted-foreground">Custo total</dt>
            <dd className="font-semibold text-foreground">
              {firstRow.totalCost === null ? (
                <span
                  aria-description="Custo não rastreado nesta fonte: a série ao vivo não tem ligação com o ledger"
                >
                  —
                </span>
              ) : (
                <MoneyDisplay money={firstRow.totalCost} />
              )}
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
            <CartesianGrid
              strokeDasharray="3 3"
              stroke={isDark ? "#334155" : "#e5e7eb"}
            />
            <XAxis
              dataKey="period"
              tick={{ fontSize: 11, fill: isDark ? "#94a3b8" : "#6b7280" }}
              tickLine={false}
            />
            <YAxis
              tick={{ fontSize: 11, fill: isDark ? "#94a3b8" : "#6b7280" }}
              tickLine={false}
            />
            <Tooltip
              contentStyle={{
                fontSize: 12,
                borderRadius: 6,
                backgroundColor: isDark ? "#1e293b" : "#ffffff",
                border: isDark ? "1px solid #334155" : "1px solid #e5e7eb",
                color: isDark ? "#e2e8f0" : "#111827",
              }}
            />
            <Legend
              wrapperStyle={{
                fontSize: 12,
                color: isDark ? "#e2e8f0" : "#111827",
              }}
            />
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
          ? "border-amber-200 bg-amber-50 dark:border-amber-500/25 dark:bg-amber-500/10"
          : "border-border bg-card",
      ].join(" ")}
    >
      <dt className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
        {label}
      </dt>
      {ariaDesc && <span className="sr-only">{ariaDesc}</span>}
      <dd className="mt-1 text-2xl font-bold text-foreground">{value}</dd>
    </div>
  );
}
