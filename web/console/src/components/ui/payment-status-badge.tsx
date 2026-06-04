/**
 * PaymentStatusBadge — exibe status de pagamento (K7 / TX-2).
 *
 * WCAG 2.2 AA: não usa cor como único diferenciador (texto + aria-label).
 * Status: pending | posted | void — espelha ledger.journal_entries.status (K3).
 * Rail: fiat_stripe | fiat_asaas | fiat_mp | crypto_safe — espelha proto K4/K5.
 */

type PaymentStatus = "pending" | "posted" | "void";
type PaymentRail = "fiat_stripe" | "fiat_asaas" | "fiat_mp" | "crypto_safe";

// ---------------------------------------------------------------------------
// Status badge
// ---------------------------------------------------------------------------

interface PaymentStatusBadgeProps {
  status: PaymentStatus;
  className?: string;
}

const STATUS_CONFIG: Record<
  PaymentStatus,
  { label: string; classes: string; ariaLabel: string }
> = {
  pending: {
    label: "Pendente",
    classes: "bg-amber-100 text-amber-800 ring-1 ring-amber-300",
    ariaLabel: "Status: pagamento pendente de confirmacao",
  },
  posted: {
    label: "Confirmado",
    classes: "bg-green-100 text-green-800 ring-1 ring-green-300",
    ariaLabel: "Status: pagamento confirmado e lancado no ledger",
  },
  void: {
    label: "Cancelado",
    classes: "bg-red-100 text-red-800 ring-1 ring-red-300",
    ariaLabel: "Status: pagamento cancelado ou estornado",
  },
};

export function PaymentStatusBadge({
  status,
  className,
}: PaymentStatusBadgeProps) {
  const config = STATUS_CONFIG[status];

  return (
    <span
      className={[
        "inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium",
        config.classes,
        className ?? "",
      ]
        .join(" ")
        .trim()}
      aria-label={config.ariaLabel}
    >
      {config.label}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Rail badge — indica o trilho de pagamento
// ---------------------------------------------------------------------------

interface PaymentRailBadgeProps {
  rail: PaymentRail;
  className?: string;
}

const RAIL_CONFIG: Record<
  PaymentRail,
  { label: string; classes: string; ariaLabel: string }
> = {
  fiat_stripe: {
    label: "Stripe",
    classes: "bg-indigo-100 text-indigo-800 ring-1 ring-indigo-200",
    ariaLabel: "Trilho: Stripe (cartao/fiat)",
  },
  fiat_asaas: {
    label: "PIX / Asaas",
    classes: "bg-blue-100 text-blue-800 ring-1 ring-blue-200",
    ariaLabel: "Trilho: PIX via Asaas (fiat BRL)",
  },
  fiat_mp: {
    label: "Mercado Pago",
    classes: "bg-sky-100 text-sky-800 ring-1 ring-sky-200",
    ariaLabel: "Trilho: Mercado Pago (failover fiat)",
  },
  crypto_safe: {
    label: "USDC / Safe",
    classes: "bg-violet-100 text-violet-800 ring-1 ring-violet-200",
    ariaLabel: "Trilho: USDC via Safe multisig (cripto)",
  },
};

export function PaymentRailBadge({
  rail,
  className,
}: PaymentRailBadgeProps) {
  const config = RAIL_CONFIG[rail];

  return (
    <span
      className={[
        "inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium",
        config.classes,
        className ?? "",
      ]
        .join(" ")
        .trim()}
      aria-label={config.ariaLabel}
    >
      {config.label}
    </span>
  );
}
