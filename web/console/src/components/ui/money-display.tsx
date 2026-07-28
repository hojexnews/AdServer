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

  // FIX (31ª onda): o valor bruto vinha em `aria-label` num <span> nu. Um
  // <span> sem role mapeia para `generic`, cujo nome acessível é PROIBIDO —
  // o aria-label era descartado pelo leitor de tela (regra
  // `aria-prohibited-attr` do axe). Não era um problema real de conteúdo,
  // porém: o texto formatado sempre foi lido normalmente.
  //
  // A primeira tentativa desta onda substituiu o aria-label por sr-only com a
  // string CRUA e escondeu o texto formatado com aria-hidden. A revisão
  // adversarial do próprio diff derrubou: isso PIORA a experiência — o usuário
  // de leitor de tela passava a ouvir "12.34 BRL" (separador e código ISO em
  // formato de máquina) no lugar de "R$ 12,34" corretamente localizado.
  //
  // Solução final: apenas remover o atributo inerte. O texto formatado é o
  // conteúdo acessível, exatamente como para quem enxerga. Os data-attributes
  // seguem disponíveis para teste/scraping, sem interferir na leitura.
  return (
    <span
      className={className}
      // Dados estruturados para scraping/testes
      data-amount={money.amount}
      data-currency={money.currency}
    >
      {formatted}
    </span>
  );
}
