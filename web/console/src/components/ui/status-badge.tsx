/**
 * StatusBadge — indica a fonte dos dados (ADR-0001).
 * Exibe visualmente "Consolidado ≤1h" vs "Ao vivo" com cores distintas.
 * WCAG 2.2 AA: não usa apenas cor como diferenciador (texto + ícone).
 */

import { cn } from "@/lib/utils";

interface StatusBadgeProps {
  source: "consolidated" | "live";
  asOf?: string | null;
}

export function DataSourceBadge({ source, asOf }: StatusBadgeProps) {
  const isLive = source === "live";

  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium",
        isLive
          ? "bg-amber-100 text-amber-800 ring-1 ring-amber-300"
          : "bg-green-100 text-green-800 ring-1 ring-green-300",
      )}
      title={
        isLive
          ? "Dados ao vivo — NÃO faturáveis, podem diferir do consolidado"
          : `Dados consolidados (≤1h) — faturáveis${asOf ? `. Atualizado em ${new Date(asOf).toLocaleTimeString("pt-BR")}` : ""}`
      }
      aria-label={isLive ? "Fonte: ao vivo (não faturável)" : "Fonte: consolidado ≤1h (faturável)"}
    >
      {/* Indicador visual não-colorístico */}
      <span aria-hidden="true">{isLive ? "⚡" : "●"}</span>
      {isLive ? "Ao vivo" : "Consolidado ≤1h"}
    </span>
  );
}

/**
 * Aviso de separação de fontes — exibido no topo do dashboard.
 * Reforça que ao vivo + consolidado NUNCA devem ser somados (ADR-0001).
 */
export function DataSourceDisclaimer() {
  return (
    <div
      role="note"
      aria-label="Aviso sobre fontes de dados"
      className="rounded-md border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800"
    >
      <strong>Atenção:</strong> os dados{" "}
      <span className="font-semibold">Consolidados ≤1h</span> e{" "}
      <span className="font-semibold">Ao vivo</span> são fontes distintas e
      NÃO devem ser somadas. Apenas os dados consolidados são faturáveis.
    </div>
  );
}
