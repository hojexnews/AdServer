/**
 * Money schema — fonte única de verdade (TX-2 / DA-10).
 *
 * O BFF NUNCA entrega Number para dinheiro. Toda quantia monetária
 * é representada como string DECIMAL + rótulo de asset_code.
 *
 * Contrato com o cliente (UI):
 *   { amount: "12.34", currency: "BRL" }
 *
 * A UI usa decimal.js / Intl.NumberFormat para exibir.
 * NUNCA aritmética monetária no cliente.
 * NUNCA conversão automática de moeda.
 */

import { z } from "zod";

/**
 * Representa dinheiro no fio BFF → cliente.
 * amount: string DECIMAL (ex.: "12.340000" para USDC)
 * currency: rótulo opaco (ex.: "BRL", "USD", "USDC")
 */
export const MoneySchema = z.object({
  amount: z
    .string()
    .regex(/^-?\d+(\.\d+)?$/, "amount deve ser string DECIMAL válida"),
  currency: z
    .string()
    .min(1)
    .max(10)
    .regex(/^[A-Z0-9]+$/, "currency deve ser código de ativo em maiúsculas"),
});

export type Money = z.infer<typeof MoneySchema>;

/**
 * Converte o tipo Money do protobuf (int64 amount + uint32 scale) para
 * o contrato de fio do BFF (string DECIMAL). Operação realizada APENAS
 * no BFF, nunca no cliente.
 *
 * Exemplos:
 *   protoMoneyToWire({ assetCode: "BRL", amount: 1234n, scale: 2 })
 *   → { amount: "12.34", currency: "BRL" }
 *
 *   protoMoneyToWire({ assetCode: "USDC", amount: 1500000n, scale: 6 })
 *   → { amount: "1.500000", currency: "USDC" }
 */
export function protoMoneyToWire(proto: {
  assetCode: string;
  amount: bigint;
  scale: number;
}): Money {
  const { assetCode, amount, scale } = proto;
  const negative = amount < 0n;
  const abs = negative ? -amount : amount;
  const absStr = abs.toString();

  let decimal: string;
  if (scale === 0) {
    decimal = absStr;
  } else {
    const padded = absStr.padStart(scale + 1, "0");
    const intPart = padded.slice(0, padded.length - scale) || "0";
    const fracPart = padded.slice(padded.length - scale);
    decimal = `${intPart}.${fracPart}`;
  }

  return {
    amount: negative ? `-${decimal}` : decimal,
    currency: assetCode,
  };
}

/**
 * Converte NUMERIC do Postgres (string) para o contrato de fio.
 * O Postgres retorna NUMERIC como string — nunca como número JS.
 */
export function pgNumericToWire(pgValue: string, currency: string): Money {
  // Valida que não virou float por acidente
  if (typeof pgValue !== "string") {
    throw new TypeError(
      `pgNumericToWire: esperava string, recebeu ${typeof pgValue}. ` +
        "Money nunca deve ser Number (TX-2/DA-10)."
    );
  }
  return MoneySchema.parse({ amount: pgValue, currency });
}
