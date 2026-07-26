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
 *   - Foco gerenciado: ao montar, o foco vai para o primeiro botão de ação;
 *     ao desmontar, o foco volta ao elemento que abriu o modal (SC 2.4.3).
 *   - FOCUS-TRAP real: enquanto o foco está dentro do diálogo, Tab/Shift+Tab
 *     ciclam entre os focáveis e nunca escapam para o fundo (aria-modal honrado);
 *     Escape = cancelar (sem armadilha permanente, SC 2.1.2). Verificado
 *     mecanicamente em `make web-a11y` (puppeteer simula Tab e afirma que o foco
 *     permanece no diálogo — axe estático não detecta ausência de trap).
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
  const dialogRef = useRef<HTMLDivElement>(null);
  const applyBtnRef = useRef<HTMLButtonElement>(null);
  const cancelBtnRef = useRef<HTMLButtonElement>(null);

  // Restauração de foco (SC 2.4.3): guarda quem tinha o foco ao montar e o
  // devolve ao desmontar (ex.: o campo do chat que disparou a proposta).
  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null;
    return () => previouslyFocused?.focus?.();
  }, []);

  // Foco inicial: ao montar, o foco vai para "Aplicar" (ou "Cancelar" se contradição).
  useEffect(() => {
    if (contradictionWarning) {
      cancelBtnRef.current?.focus();
    } else {
      applyBtnRef.current?.focus();
    }
  }, [contradictionWarning]);

  // FOCUS-TRAP (aria-modal, SC 2.4.3): enquanto o foco está DENTRO do diálogo,
  // Tab/Shift+Tab ciclam entre os focáveis e nunca escapam para o fundo. O guard
  // `contains` faz o trap atuar SÓ quando o foco já está no diálogo — não "puxa"
  // foco de fora, então é correto tanto no app real (modal isolado) quanto no
  // harness do a11y-ci (onde o modal coexiste com outros componentes). Escape
  // continua saindo (sem armadilha permanente, SC 2.1.2).
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Tab") return;
      if (!dialog.contains(document.activeElement)) return;
      const focusables = dialog.querySelectorAll<HTMLElement>(
        'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
      );
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      if (!first || !last) {
        e.preventDefault();
        return;
      }
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

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
      ref={dialogRef}
      role="dialog"
      aria-modal="true"
      aria-labelledby="hitl-diff-title"
      aria-describedby="hitl-diff-desc"
      className="rounded-xl border-2 border-brand-300 bg-card p-6 shadow-lg"
    >
      {/* Header */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2
            id="hitl-diff-title"
            className="text-base font-semibold text-foreground"
          >
            Revisao obrigatoria — acao proposta pelo copiloto
          </h2>
          <p
            id="hitl-diff-desc"
            className="mt-1 text-sm text-muted-foreground"
          >
            {/* Conteúdo do LLM renderizado como texto puro — sem dangerouslySetInnerHTML */}
            {message}
          </p>
        </div>
        <span
          className="inline-flex shrink-0 items-center rounded-full bg-amber-100 px-2.5 py-0.5 text-xs font-medium text-amber-800 ring-1 ring-amber-300 dark:bg-amber-500/15 dark:text-amber-200 dark:ring-amber-500/30"
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
          className="mt-4 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-800 dark:border-red-500/25 dark:bg-red-500/10 dark:text-red-300"
        >
          <strong>Contradição detectada (CA-4):</strong> {contradictionWarning}
        </div>
      )}

      {/* Aviso geral: nada foi aplicado ainda */}
      <div
        role="note"
        className="mt-4 rounded-md border border-blue-200 bg-blue-50 p-3 text-sm text-blue-800 dark:border-blue-500/25 dark:bg-blue-500/10 dark:text-blue-300"
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
            "text-foreground ring-border bg-card hover:bg-muted",
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
            // amber-600/amber-700 com texto branco reprova WCAG 2.2 AA
            // (contraste ~3.19:1, precisa 4.5:1) — amber-700/amber-800
            // passa (achado do axe-core, make web-a11y, G0/frontend E10).
            contradictionWarning
              ? "bg-amber-700 text-white hover:bg-amber-800"
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
      className="rounded-lg border border-border bg-muted p-4 text-sm font-mono"
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
    create: "bg-green-100 text-green-800 ring-green-300 dark:bg-green-500/15 dark:text-green-200 dark:ring-green-500/30",
    update: "bg-blue-100 text-blue-800 ring-blue-300 dark:bg-blue-500/15 dark:text-blue-200 dark:ring-blue-500/30",
    delete: "bg-red-100 text-red-800 ring-red-300 dark:bg-red-500/15 dark:text-red-200 dark:ring-red-500/30",
  };
  const kindLabels: Record<string, string> = {
    campaign: "Campanha",
    banner: "Banner",
    rule: "Regra de segmentacao",
    cap: "Cap de entrega",
    zone_link: "Vinculo campanha-zona",
  };
  const color = actionColors[action] ?? "bg-muted text-foreground ring-border";
  return (
    <span
      className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ${color}`}
    >
      {kindLabels[kind] ?? kind} — {action.toUpperCase()}
    </span>
  );
}

// DiffRow/MoneyRow são filhos diretos de <dl> (ver CampaignDiff etc. abaixo).
// axe-core (regra "definition-list"/WCAG 1.3.1) exige que <dl> só contenha
// <dt>/<dd> (opcionalmente agrupados num <div>) — não <span> soltos. O <div>
// aqui é o agrupamento permitido; <dt>/<dd> preservam a semântica de lista de
// definição, o layout flex lado-a-lado é preservado via className.
function DiffRow({ label, value }: { label: string; value: string | undefined | null }) {
  if (value == null) return null;
  return (
    <div className="flex gap-2">
      <dt className="w-32 shrink-0 text-muted-foreground">{label}:</dt>
      {/* Conteúdo escapado pelo React — sem dangerouslySetInnerHTML */}
      <dd className="m-0 text-foreground">{value}</dd>
    </div>
  );
}

function MoneyRow({ label, money }: { label: string; money: MoneyWire | null | undefined }) {
  if (!money) return null;
  // TX-2: formata a partir de string DECIMAL, nunca de Number
  return (
    <div className="flex gap-2">
      <dt className="w-32 shrink-0 text-muted-foreground">{label}:</dt>
      <dd className="m-0 text-foreground">{formatMoney(money)}</dd>
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
