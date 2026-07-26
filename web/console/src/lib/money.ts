/**
 * Utilitários de formatação de dinheiro no cliente (TX-2 / DA-10).
 *
 * REGRAS INVIOLÁVEIS:
 *   - NUNCA usar Number() ou parseFloat() para dinheiro.
 *   - NUNCA fazer aritmética monetária no cliente.
 *   - NUNCA converter moedas automaticamente.
 *   - Toda quantia vem do BFF como string DECIMAL + currency.
 *   - Formatar com Intl.NumberFormat (display apenas).
 *
 * decimal.js está disponível como dependência caso a UI precise exibir
 * derivações (ex.: "taxa × volume" para preview). NÃO usar para faturamento.
 */

import Decimal from "decimal.js";

/**
 * Intl.NumberFormat que aceita STRING decimal.
 *
 * A spec do ECMA-402 v3 (ES2023) estende `format()` para aceitar uma string
 * decimal e formatá-la SEM converter para double — é justamente o que permite
 * exibir dinheiro sem passar por float (TX-2/DA-10). O `lib` do TypeScript
 * ainda declara apenas `format(value: number | bigint)`, então a chamada
 * correta não typecheca sem esta ponte.
 *
 * Cast localizado e documentado, em vez de `declare global` (que estenderia o
 * tipo para todo o projeto e esconderia o motivo). Runtime alvo: Node 24 e
 * navegadores modernos — ambos implementam Intl v3.
 */
interface DecimalStringNumberFormat {
  format(value: string): string;
}

function decimalFormatter(
  locale: string,
  options: Intl.NumberFormatOptions
): DecimalStringNumberFormat {
  return new Intl.NumberFormat(
    locale,
    options
  ) as unknown as DecimalStringNumberFormat;
}

export interface MoneyWire {
  amount: string;   // string DECIMAL ex.: "12.34"
  currency: string; // rótulo ex.: "BRL"
}

/**
 * Formata um MoneyWire para exibição usando Intl.NumberFormat.
 * Nunca converte a string para Number diretamente para aritmética.
 *
 * Exemplos:
 *   formatMoney({ amount: "12.34", currency: "BRL" }, "pt-BR")
 *   → "R$ 12,34"
 *
 *   formatMoney({ amount: "1.500000", currency: "USDC" }, "en-US")
 *   → "USDC 1.500000"  (sem símbolo ISO reconhecido → fallback)
 */
export function formatMoney(
  money: MoneyWire,
  locale: string = "pt-BR"
): string {
  // Normaliza via decimal.js (valida a string e remove zeros à direita) SEM
  // converter para float em momento algum.
  const dec = new Decimal(money.amount);

  try {
    // FIX (31ª onda, TX-2): esta chamada era
    // `.format(dec.toNumber())` — um float64 no meio do caminho do dinheiro.
    //
    // O comentário anterior dizia que era "seguro porque é apenas exibição",
    // apoiado na premissa de que só moedas ISO 4217 chegariam aqui e que
    // cripto cairia no catch. A premissa é FALSA: Intl.NumberFormat NÃO valida
    // contra a lista ISO 4217 — aceita QUALQUER código bem-formado de 3
    // letras. Só códigos de 4+ letras (USDC, USDT) lançam. Ou seja, AEV e BND
    // — os tokens do próprio projeto (ADR-0004) — passavam direto pelo
    // toNumber(). Medido: "9007199254740993.000001" era exibido como
    // "9,007,199,254,740,994.00" — valor errado na tela do anunciante.
    //
    // Intl.NumberFormat (spec v3, ES2023) aceita STRING decimal diretamente e
    // formata a partir do decimal exato. Mesmo resultado visual, sem float.
    return decimalFormatter(locale, {
      style: "currency",
      currency: money.currency,
      minimumFractionDigits: 2,
      maximumFractionDigits: 6,
    }).format(dec.toFixed());
  } catch {
    // Código de 4+ letras (USDC/USDT) — Intl lança em `currency`.
    // Imprime a string DECIMAL integral, sem arredondar.
    return `${money.currency} ${dec.toFixed()}`;
  }
}

/**
 * Formata CTR (vem do BFF como string "0.05234") como percentual.
 * Nunca Number para dinheiro — CTR não é dinheiro, mas segue o mesmo padrão.
 */
export function formatCtr(ctrString: string, locale: string = "pt-BR"): string {
  // String decimal direto no Intl (spec v3), pelo mesmo motivo de formatMoney:
  // nenhuma travessia por float, mesmo onde a precisão não seria crítica.
  const dec = new Decimal(ctrString);
  return decimalFormatter(locale, {
    style: "percent",
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(dec.toFixed());
}

/**
 * Formata número inteiro de eventos (requests, impressions, clicks, conversions).
 * Estes são contagens, não dinheiro — Number() é seguro para inteiros pequenos.
 */
export function formatCount(count: number, locale: string = "pt-BR"): string {
  return new Intl.NumberFormat(locale, {
    notation: count >= 1_000_000 ? "compact" : "standard",
    maximumFractionDigits: 1,
  }).format(count);
}
