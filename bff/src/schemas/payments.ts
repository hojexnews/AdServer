/**
 * Schemas de pagamentos — K7 (Fase 3).
 *
 * Invariantes:
 *   TX-2: amount SEMPRE string DECIMAL, nunca number. Nenhuma aritmética aqui.
 *   TX-3: tenant_id nunca aparece em inputs de cliente — vem do ctx.
 *
 * Espelha adserver.payments.v1 (PaymentStatus / PaymentRail).
 * Balance deriva de ledger.account_balances (Postgres, K3).
 * PaymentStatusItem deriva de ledger.journal_entries + eventos K4/K5.
 */

import { z } from "zod";
import { MoneySchema } from "./money.js";

// ---------------------------------------------------------------------------
// Enums — espelham proto adserver.payments.v1
// ---------------------------------------------------------------------------

/**
 * Trilho de pagamento.
 * fiat_stripe   = Stripe (K4)
 * fiat_asaas    = Asaas/PIX (K4)
 * fiat_mp       = Mercado Pago failover (K4)
 * crypto_safe   = Safe multisig USDC (K5)
 */
export const PaymentRailSchema = z.enum([
  "fiat_stripe",
  "fiat_asaas",
  "fiat_mp",
  "crypto_safe",
]);
export type PaymentRail = z.infer<typeof PaymentRailSchema>;

/**
 * Status do pagamento — espelha ledger.journal_entries.status (K3).
 * pending = aguardando confirmação
 * posted  = confirmado e lançado no ledger
 * void    = cancelado/estornado
 */
export const PaymentStatusEnum = z.enum(["pending", "posted", "void"]);
export type PaymentStatus = z.infer<typeof PaymentStatusEnum>;

// ---------------------------------------------------------------------------
// Balance — saldo por ativo do tenant (ledger.account_balances)
// ---------------------------------------------------------------------------

/**
 * Saldo de um ativo do tenant.
 * amount: string DECIMAL (nunca number) — vem de NUMERIC do Postgres via pgNumericToWire.
 * asset_code: código do ativo (BRL, USD, USDC, etc.)
 * label: rótulo localizado para exibição.
 * scale: número de casas decimais do ativo (informativo).
 */
export const BalanceSchema = z.object({
  /** Código do ativo (ex.: "BRL", "USDC"). */
  asset_code: z.string().min(1).max(10).regex(/^[A-Z0-9]+$/),
  /** Saldo como string DECIMAL — NUNCA number (TX-2). */
  amount: MoneySchema.shape.amount,
  /** Casas decimais do ativo (ex.: 2 para BRL, 6 para USDC). Informativo. */
  scale: z.number().int().nonnegative(),
  /** Rótulo localizado (ex.: "Real Brasileiro", "USD Coin"). */
  label: z.string().min(1),
});
export type Balance = z.infer<typeof BalanceSchema>;

// ---------------------------------------------------------------------------
// PaymentStatusItem — linha de histórico de pagamento
// ---------------------------------------------------------------------------

/**
 * Um evento/transação no histórico de pagamentos do tenant.
 * Campos sensíveis (chaves, DSN, PII de terceiros) são NUNCA incluídos.
 *
 * pix_end_to_end_id: ID E2E PIX (Asaas/K4) — opaque, sem PII.
 * tx_hash:           hash da tx on-chain (Safe/K5) — opaque, sem PII de contraparte.
 */
export const PaymentStatusItemSchema = z.object({
  /** ID interno do pagamento (journal_entry id ou id do evento). */
  payment_id: z.string().min(1),
  /** Trilho usado. */
  rail: PaymentRailSchema,
  /** Status atual. */
  status: PaymentStatusEnum,
  /** Valor como MoneyWire — string DECIMAL + currency (TX-2). */
  amount: MoneySchema,
  /** Data/hora de criação (ISO-8601). */
  created_at: z.string().datetime(),
  /** ID E2E PIX, presente apenas para trilho fiat_asaas. Opaque. */
  pix_end_to_end_id: z.string().optional(),
  /** Hash da transação on-chain, presente apenas para trilho crypto_safe. Opaque. */
  tx_hash: z.string().optional(),
});
export type PaymentStatusItem = z.infer<typeof PaymentStatusItemSchema>;

// ---------------------------------------------------------------------------
// Inputs de query (cliente → BFF)
// Nota: tenant_id NUNCA está em nenhum input — vem de ctx.tenantId (TX-3).
// ---------------------------------------------------------------------------

/** Filtros de listagem de status de pagamentos. Sem tenant_id (TX-3). */
export const ListPaymentStatusInputSchema = z.object({
  /** Página (1-indexada). */
  page: z.number().int().positive().default(1),
  /** Itens por página (máx. 100). */
  pageSize: z.number().int().positive().max(100).default(20),
  /** Filtrar por rail específico. */
  rail: PaymentRailSchema.optional(),
  /** Filtrar por status. */
  status: PaymentStatusEnum.optional(),
  /** Filtrar a partir de (ISO-8601). */
  from: z.string().datetime().optional(),
  /** Filtrar até (ISO-8601). */
  to: z.string().datetime().optional(),
});
export type ListPaymentStatusInput = z.infer<typeof ListPaymentStatusInputSchema>;

/** Resposta paginada de status de pagamentos. */
export const PaymentStatusPageSchema = z.object({
  items: z.array(PaymentStatusItemSchema),
  total: z.number().int().nonnegative(),
  page: z.number().int().positive(),
  pageSize: z.number().int().positive(),
  hasMore: z.boolean(),
});
export type PaymentStatusPage = z.infer<typeof PaymentStatusPageSchema>;
