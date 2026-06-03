/**
 * MoneyDisplay — exibe um valor monetário (TX-2 / DA-10).
 *
 * Recebe MoneyWire { amount: string, currency: string } do BFF.
 * Usa formatMoney() (decimal.js + Intl.NumberFormat) para exibição.
 * NUNCA usa Number() ou parseFloat() para aritmética monetária.
 */

import { formatMoney, type MoneyWire } from "@/lib/money";

interface MoneyDisplayProps {
  money: MoneyWire;
  locale?: string;
  className?: string;
}

export function MoneyDisplay({
  money,
  locale = "pt-BR",
  className,
}: MoneyDisplayProps) {
  const formatted = formatMoney(money, locale);

  return (
    <span
      className={className}
      // Dados brutos no aria-label para leitores de tela
      aria-label={`${money.amount} ${money.currency}`}
      // Dados estruturados para scraping/testes
      data-amount={money.amount}
      data-currency={money.currency}
    >
      {formatted}
    </span>
  );
}
