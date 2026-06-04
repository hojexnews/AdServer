/**
 * HitlDiffPreview — exibe o diff proposto pelo copiloto para aprovação humana (HITL).
 *
 * INVARIANTE CRÍTICA (§2.4 / TX-3):
 *   - Nenhuma escrita acontece até o usuário clicar em "Aplicar".
 *   - Este componente é read-only; não faz mutations diretamente.
 *   - Valores monetários exibidos via formatMoney (string DECIMAL → display).
 *   - Conteúdo do LLM renderizado como texto puro (sem dangerouslySetInnerHTML).
 *   - Anti-XSS: todo conteúdo do diff é renderizado via React (texto escapado).
 *
 * WCAG 2.2 AA:
 *   - role="dialog" + aria-modal + aria-labelledby + aria-describedby.
 *   - Foco gerenciado: ao montar, o foco vai para o primeiro botão de ação.
 *   - Navegação por teclado: Tab/Shift+Tab entre botões, Escape = cancelar.
 */

"use client";

import { useEffect, useRef } from "react";
import type { WriteDiff, MoneyWire } from "@/lib/copilot-schemas";
import { formatMoney } from "@/lib/money";

interface HitlDiffPreviewProps {
  threadId: string;
  diff: WriteDiff;
  message: string;
  /** Aviso anti-contradição (CA-4) — null se não houver */
  contradictionWarning: string | null;
  isApproving: boolean;
  isRejecting: boolean;
  onApprove: (threadId: string) => void;
  onReject: (threadId: string, reason: string) => void;
}

export function HitlDiffPreview({
  threadId,
  diff,
  message,
  contradictionWarning,
  isApproving,
  isRejecting,
  onApprove,
  onReject,
}: HitlDiffPreviewProps) {
  const applyBtnRef = useRef<HTMLButtonElement>(null);
  const cancelBtnRef = useRef<HTMLButtonElement>(null);

  // Gerencia foco: ao montar, o foco vai para "Aplicar" (ou "Cancelar" se contradição)
  useEffect(() => {
    if (contradictionWarning) {
      cancelBtnRef.current?.focus();
    } else {
      applyBtnRef.current?.focus();
    }
  }, [contradictionWarning]);

  // Escape fecha / rejeita
  useEffect(() => {
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !isApproving && !isRejecting) {
        onReject(threadId, "Cancelado pelo usuário via teclado.");
      }
    };
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [threadId, onReject, isApproving, isRejecting]);

  const busy = isApproving || isRejecting;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="hitl-diff-title"
      aria-describedby="hitl-diff-desc"
      className="rounded-xl border-2 border-brand-300 bg-white p-6 shadow-lg"
    >
      {/* Header */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2
            id="hitl-diff-title"
            className="text-base font-semibold text-gray-900"
          >
            Revisao obrigatoria — acao proposta pelo copiloto
          </h2>
          <p
            id="hitl-diff-desc"
            className="mt-1 text-sm text-gray-600"
          >
            {/* Conteúdo do LLM renderizado como texto puro — sem dangerouslySetInnerHTML */}
            {message}
          </p>
        </div>
        <span
          className="inline-flex shrink-0 items-center rounded-full bg-amber-100 px-2.5 py-0.5 text-xs font-medium text-amber-800 ring-1 ring-amber-300"
          aria-label="Acao pendente de aprovacao humana"
        >
          Pendente de aprovacao
        </span>
      </div>

      {/* Aviso anti-contradição CA-4 */}
      {contradictionWarning && (
        <div
          role="alert"
          aria-live="assertive"
          className="mt-4 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-800"
        >
          <strong>Contradição detectada (CA-4):</strong> {contradictionWarning}
        </div>
      )}

      {/* Aviso geral: nada foi aplicado ainda */}
      <div
        role="note"
        className="mt-4 rounded-md border border-blue-200 bg-blue-50 p-3 text-sm text-blue-800"
      >
        <strong>Nada foi aplicado ainda.</strong> Revise o que sera alterado abaixo
        e clique em <strong>Aplicar</strong> para confirmar ou <strong>Cancelar</strong> para descartar.
      </div>

      {/* Corpo do diff */}
      <div className="mt-5">
        <DiffBody diff={diff} />
      </div>

      {/* Botoes de acao */}
      <div className="mt-6 flex flex-wrap gap-3">
        <button
          ref={cancelBtnRef}
          type="button"
          onClick={() => onReject(threadId, "Cancelado pelo usuário.")}
          disabled={busy}
          className={[
            "rounded-md px-4 py-2 text-sm font-medium ring-1",
            "text-gray-700 ring-gray-300 bg-white hover:bg-gray-50",
            "focus-visible:ring-2 focus-visible:ring-brand-500",
            "disabled:opacity-50",
          ].join(" ")}
          aria-label="Cancelar operacao proposta pelo copiloto"
        >
          {isRejecting ? "Cancelando..." : "Cancelar"}
        </button>

        <button
          ref={applyBtnRef}
          type="button"
          onClick={() => onApprove(threadId)}
          disabled={busy}
          className={[
            "rounded-md px-4 py-2 text-sm font-semibold",
            contradictionWarning
              ? "bg-amber-600 text-white hover:bg-amber-700"
              : "bg-brand-600 text-white hover:bg-brand-700",
            "focus-visible:ring-2 focus-visible:ring-brand-500",
            "disabled:opacity-50",
          ].join(" ")}
          aria-label={
            contradictionWarning
              ? "Aplicar operacao mesmo com aviso de contradicao"
              : "Aplicar operacao proposta pelo copiloto"
          }
          aria-busy={isApproving}
        >
          {isApproving
            ? "Aplicando..."
            : contradictionWarning
            ? "Aplicar (ciente do aviso)"
            : "Aplicar"}
        </button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// DiffBody — renderiza o conteúdo do diff de forma legível e segura
// ---------------------------------------------------------------------------

function DiffBody({ diff }: { diff: WriteDiff }) {
  return (
    <div
      className="rounded-lg border border-gray-200 bg-gray-50 p-4 text-sm font-mono"
      aria-label="Detalhes da operacao proposta"
    >
      <div className="mb-3 flex items-center gap-2">
        <DiffKindBadge kind={diff.kind} action={diff.action} />
      </div>

      {diff.kind === "campaign" && <CampaignDiff diff={diff} />}
      {diff.kind === "banner" && <BannerDiff diff={diff} />}
      {diff.kind === "rule" && <RuleDiff diff={diff} />}
      {diff.kind === "cap" && <CapDiff diff={diff} />}
      {diff.kind === "zone_link" && <ZoneLinkDiff diff={diff} />}
    </div>
  );
}

function DiffKindBadge({ kind, action }: { kind: string; action: string }) {
  const actionColors: Record<string, string> = {
    create: "bg-green-100 text-green-800 ring-green-300",
    update: "bg-blue-100 text-blue-800 ring-blue-300",
    delete: "bg-red-100 text-red-800 ring-red-300",
  };
  const kindLabels: Record<string, string> = {
    campaign: "Campanha",
    banner: "Banner",
    rule: "Regra de segmentacao",
    cap: "Cap de entrega",
    zone_link: "Vinculo campanha-zona",
  };
  const color = actionColors[action] ?? "bg-gray-100 text-gray-800 ring-gray-300";
  return (
    <span
      className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ${color}`}
    >
      {kindLabels[kind] ?? kind} — {action.toUpperCase()}
    </span>
  );
}

function DiffRow({ label, value }: { label: string; value: string | undefined | null }) {
  if (value == null) return null;
  return (
    <div className="flex gap-2">
      <span className="w-32 shrink-0 text-gray-500">{label}:</span>
      {/* Conteúdo escapado pelo React — sem dangerouslySetInnerHTML */}
      <span className="text-gray-900">{value}</span>
    </div>
  );
}

function MoneyRow({ label, money }: { label: string; money: MoneyWire | null | undefined }) {
  if (!money) return null;
  // TX-2: formata a partir de string DECIMAL, nunca de Number
  return (
    <div className="flex gap-2">
      <span className="w-32 shrink-0 text-gray-500">{label}:</span>
      <span className="text-gray-900">{formatMoney(money)}</span>
    </div>
  );
}

function CampaignDiff({ diff }: { diff: Extract<WriteDiff, { kind: "campaign" }> }) {
  return (
    <dl className="space-y-1">
      <DiffRow label="ID" value={diff.id} />
      <DiffRow label="Nome" value={diff.name} />
      <DiffRow label="Anunciante" value={diff.advertiserId} />
      <DiffRow label="Status" value={diff.status} />
      <MoneyRow label="Orcamento diario" money={diff.dailyBudget} />
      <MoneyRow label="Orcamento total" money={diff.totalBudget} />
    </dl>
  );
}

function BannerDiff({ diff }: { diff: Extract<WriteDiff, { kind: "banner" }> }) {
  return (
    <dl className="space-y-1">
      <DiffRow label="ID" value={diff.id} />
      <DiffRow label="Nome" value={diff.name} />
      <DiffRow label="Campanha" value={diff.campaignId} />
      <DiffRow
        label="Tamanho"
        value={diff.width && diff.height ? `${diff.width}x${diff.height}` : undefined}
      />
      <DiffRow label="URL de destino" value={diff.url} />
      <DiffRow label="Status" value={diff.status} />
    </dl>
  );
}

function RuleDiff({ diff }: { diff: Extract<WriteDiff, { kind: "rule" }> }) {
  return (
    <dl className="space-y-1">
      <DiffRow label="ID" value={diff.id} />
      <DiffRow label="Tipo de dono" value={diff.ownerType} />
      <DiffRow label="ID do dono" value={diff.ownerId} />
      <DiffRow label="Vetor" value={diff.vector} />
      <DiffRow label="Operador" value={diff.operator} />
      <DiffRow label="Valor" value={diff.value} />
      <DiffRow label="Logica" value={diff.logicalOp} />
    </dl>
  );
}

function CapDiff({ diff }: { diff: Extract<WriteDiff, { kind: "cap" }> }) {
  const valueStr =
    diff.valueMoney
      ? undefined // renderizado por MoneyRow
      : diff.value != null
      ? String(diff.value)
      : undefined;

  return (
    <dl className="space-y-1">
      <DiffRow label="ID" value={diff.id} />
      <DiffRow label="Tipo de dono" value={diff.ownerType} />
      <DiffRow label="ID do dono" value={diff.ownerId} />
      <DiffRow label="Tipo de cap" value={diff.capType} />
      <DiffRow label="Valor" value={valueStr} />
      <MoneyRow label="Valor (monetario)" money={diff.valueMoney} />
    </dl>
  );
}

function ZoneLinkDiff({ diff }: { diff: Extract<WriteDiff, { kind: "zone_link" }> }) {
  return (
    <dl className="space-y-1">
      <DiffRow label="Campanha" value={diff.campaignId} />
      <DiffRow label="Zona" value={diff.zoneId} />
    </dl>
  );
}
