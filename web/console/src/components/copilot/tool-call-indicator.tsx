/**
 * ToolCallIndicator — exibe o estado de uma tool call em andamento ou concluída.
 *
 * WCAG 2.2 AA:
 *   - aria-live="polite" para atualizações de status de ferramenta.
 *   - role="status" para o indicador.
 *   - Não usa apenas cor como diferenciador.
 */

"use client";

import type { ToolCallState } from "@/lib/use-copilot-session";

const TOOL_LABELS: Record<string, string> = {
  get_campaigns: "Buscando campanhas...",
  get_banners: "Buscando banners...",
  get_zones: "Buscando zonas...",
  get_stats: "Consultando estatísticas...",
  simulate_forecast: "Simulando forecast de entrega...",
  validate_segmentation: "Validando regras de segmentação...",
  validate_creative: "Validando criativo (C2PA/SynthID)...",
  create_campaign: "Preparando criação de campanha...",
  update_campaign: "Preparando atualização de campanha...",
  create_banner: "Preparando criação de banner...",
  update_banner: "Preparando atualização de banner...",
  create_rule: "Preparando regra de segmentação...",
  create_cap: "Preparando cap de entrega...",
  link_zone: "Preparando vínculo campanha-zona...",
};

interface ToolCallIndicatorProps {
  tool: ToolCallState;
}

export function ToolCallIndicator({ tool }: ToolCallIndicatorProps) {
  const label = TOOL_LABELS[tool.tool] ?? `Executando: ${tool.tool}`;
  const isDone = tool.status === "done";

  return (
    <div
      role="status"
      aria-live="polite"
      aria-label={isDone ? `${label} — concluído` : label}
      className="flex items-center gap-2 rounded-md border border-blue-200 bg-blue-50 px-3 py-2 text-sm text-blue-800 dark:border-blue-500/25 dark:bg-blue-500/10 dark:text-blue-200"
    >
      {isDone ? (
        <span aria-hidden="true" className="text-green-600 dark:text-green-400">
          {/* Checkmark não-colorístico */}
          [OK]
        </span>
      ) : (
        <span
          className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-blue-300 border-t-blue-600 dark:border-blue-500/30 dark:border-t-blue-400"
          aria-hidden="true"
        />
      )}
      <span>{label}</span>
      {isDone && (
        <span className="ml-auto text-xs text-green-700 dark:text-green-300">Concluído</span>
      )}
    </div>
  );
}
