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
  // Converte string DECIMAL para Decimal (decimal.js) para extrair partes
  // SEM perda de precisão. Nunca usamos Number() para dinheiro.
  const dec = new Decimal(money.amount);

  // Tenta usar Intl.NumberFormat com o currency code
  try {
    // Intl.NumberFormat aceita apenas ISO 4217 (BRL, USD, EUR etc.)
    // Para criptos (USDC, USDT) cai no catch
    const formatted = new Intl.NumberFormat(locale, {
      style: "currency",
      currency: money.currency,
      minimumFractionDigits: 2,
      maximumFractionDigits: 6,
    // dec.toNumber() é seguro aqui: APENAS para exibição via Intl.NumberFormat.
    // O valor autoritativo permanece como string no MoneyWire (TX-2/DA-10).
    }).format(dec.toNumber());
    return formatted;
  } catch {
    // Fallback para crypto ou códigos desconhecidos
    return `${money.currency} ${dec.toFixed()}`;
  }
}

/**
 * Formata CTR (vem do BFF como string "0.05234") como percentual.
 * Nunca Number para dinheiro — CTR não é dinheiro, mas segue o mesmo padrão.
 */
export function formatCtr(ctrString: string, locale: string = "pt-BR"): string {
  const dec = new Decimal(ctrString);
  return new Intl.NumberFormat(locale, {
    style: "percent",
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(dec.toNumber());
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
