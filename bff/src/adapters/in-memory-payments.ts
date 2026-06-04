/**
 * Adapter in-memory de pagamentos — STUB para K7 (Fase 3).
 *
 * Produz dados sintéticos determinísticos por tenant para que build/typecheck
 * e testes passem sem Postgres real (K3/K4/K5 pendem de infra).
 *
 * INVARIANTES (idênticas ao adapter real):
 *   TX-2: amount SEMPRE string DECIMAL, nunca number.
 *         Nenhuma aritmética monetária aqui.
 *   TX-3: tenantId vem SEMPRE do caller (ctx), nunca do cliente.
 *         O stub respeita o isolamento de tenant: cada tenant vê
 *         apenas seus próprios dados sintéticos.
 *
 * ONDE PLUGAR O ADAPTER REAL:
 *   src/index.ts → substituir InMemoryPaymentsAdapter por PostgresPaymentsAdapter.
 *   O adapter real deve:
 *     1. SET LOCAL adserver.tenant_id = $1 (ativa RLS K3).
 *     2. SELECT asset_code, balance_amount::text, scale
 *        FROM ledger.account_balances
 *        WHERE tenant_id = current_setting('adserver.tenant_id')::uuid
 *     3. SELECT je.id, pe.rail, je.status, je.amount::text, je.asset_code,
 *               je.created_at, pe.pix_end_to_end_id, pe.tx_hash
 *        FROM ledger.journal_entries je
 *        JOIN payments.payment_events pe ON pe.journal_entry_id = je.id
 *        WHERE je.tenant_id = current_setting('adserver.tenant_id')::uuid
 *        ORDER BY je.created_at DESC
 *        LIMIT $pageSize OFFSET $(page-1)*pageSize
 *
 * Segredos/PII NUNCA no payload:
 *   - Sem chaves Stripe/Asaas/Safe
 *   - Sem DSN
 *   - Sem PII de contraparte (nome, CPF, endereço on-chain de terceiros)
 */

import type { PaymentsAdapter } from "./payments-adapter.js";
import type { Balance, PaymentStatusItem, ListPaymentStatusInput } from "../schemas/payments.js";

// ---------------------------------------------------------------------------
// Dados sintéticos por tenant (determinísticos — seed baseado no UUID)
// ---------------------------------------------------------------------------

/**
 * Gera saldos sintéticos para um tenant.
 * Cada tenant tem saldos distintos via hash simples do UUID.
 * Valores como string DECIMAL — nunca number no payload.
 */
function fakeTenantSeed(tenantId: string): number {
  // Hash determinístico simples: soma dos charCodes dos primeiros 8 chars
  let seed = 0;
  for (let i = 0; i < Math.min(8, tenantId.length); i++) {
    seed = (seed + (tenantId.charCodeAt(i) ?? 0)) % 1000;
  }
  return seed;
}

function fakeBalances(tenantId: string): Balance[] {
  const seed = fakeTenantSeed(tenantId);
  // BRL: escala 2, USDC: escala 6
  // Importância: amount é string DECIMAL, nunca number (TX-2)
  const brlAmount = (1000 + seed * 7).toString() + ".00";
  const usdcAmount = (50 + seed).toString() + ".000000";

  return [
    {
      asset_code: "BRL",
      amount: brlAmount,
      scale: 2,
      label: "Real Brasileiro",
    },
    {
      asset_code: "USDC",
      amount: usdcAmount,
      scale: 6,
      label: "USD Coin",
    },
  ];
}

/**
 * Gera histórico sintético de pagamentos para um tenant.
 * Cobre todos os trilhos (fiat_stripe, fiat_asaas, fiat_mp, crypto_safe)
 * para que a UI possa exibir exemplos de todos os estados.
 */
function fakePaymentHistory(tenantId: string): PaymentStatusItem[] {
  const seed = fakeTenantSeed(tenantId);
  const now = new Date();

  // Datas relativas ao momento atual (retroativas)
  const isoMinus = (hours: number): string =>
    new Date(now.getTime() - hours * 3_600_000).toISOString();

  // Montante como string DECIMAL (TX-2 — nunca number)
  const brl = (cents: number): string => {
    const major = Math.floor(cents / 100);
    const minor = cents % 100;
    return `${major}.${minor.toString().padStart(2, "0")}`;
  };
  const usdc = (units: number, micro: number): string =>
    `${units}.${micro.toString().padStart(6, "0")}`;

  return [
    {
      payment_id: `pay-stripe-${seed}-1`,
      rail: "fiat_stripe",
      status: "posted",
      amount: { amount: brl(50000 + seed * 100), currency: "BRL" },
      created_at: isoMinus(72),
    },
    {
      payment_id: `pay-asaas-${seed}-2`,
      rail: "fiat_asaas",
      status: "posted",
      amount: { amount: brl(25000 + seed * 50), currency: "BRL" },
      created_at: isoMinus(48),
      pix_end_to_end_id: `E${seed}00000000000000000000001`,
    },
    {
      payment_id: `pay-mp-${seed}-3`,
      rail: "fiat_mp",
      status: "pending",
      amount: { amount: brl(12500 + seed * 25), currency: "BRL" },
      created_at: isoMinus(24),
    },
    {
      payment_id: `pay-safe-${seed}-4`,
      rail: "crypto_safe",
      status: "posted",
      amount: { amount: usdc(10 + seed, seed * 100), currency: "USDC" },
      created_at: isoMinus(12),
      tx_hash: `0x${seed.toString(16).padStart(4, "0")}abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789`,
    },
    {
      payment_id: `pay-stripe-${seed}-5`,
      rail: "fiat_stripe",
      status: "void",
      amount: { amount: brl(5000 + seed * 10), currency: "BRL" },
      created_at: isoMinus(6),
    },
  ];
}

// ---------------------------------------------------------------------------
// Implementação in-memory
// ---------------------------------------------------------------------------

export class InMemoryPaymentsAdapter implements PaymentsAdapter {
  /**
   * Retorna saldos sintéticos do tenant.
   * tenantId: SEMPRE do ctx, nunca do cliente (TX-3).
   */
  async getBalances(tenantId: string): Promise<Balance[]> {
    return fakeBalances(tenantId);
  }

  /**
   * Lista histórico sintético de pagamentos com paginação e filtros.
   * tenantId: SEMPRE do ctx, nunca do cliente (TX-3).
   */
  async listPaymentStatus(
    tenantId: string,
    input: ListPaymentStatusInput
  ): Promise<{ items: PaymentStatusItem[]; total: number }> {
    let all = fakePaymentHistory(tenantId);

    // Filtros opcionais
    if (input.rail !== undefined) {
      all = all.filter((p) => p.rail === input.rail);
    }
    if (input.status !== undefined) {
      all = all.filter((p) => p.status === input.status);
    }
    if (input.from !== undefined) {
      const fromMs = new Date(input.from).getTime();
      all = all.filter((p) => new Date(p.created_at).getTime() >= fromMs);
    }
    if (input.to !== undefined) {
      const toMs = new Date(input.to).getTime();
      all = all.filter((p) => new Date(p.created_at).getTime() <= toMs);
    }

    const total = all.length;
    const { page, pageSize } = input;
    const offset = (page - 1) * pageSize;
    const items = all.slice(offset, offset + pageSize);

    return { items, total };
  }

  /**
   * Detalhe de um pagamento no escopo do tenant. Retorna null se o id não
   * pertencer ao tenant — sem distinguir de "não existe" (sem IDOR).
   * tenantId: SEMPRE do ctx, nunca do cliente (TX-3). O escopo por tenant
   * vem de fakePaymentHistory(tenantId), espelhando o WHERE tenant_id do
   * adapter real (que filtra por id na query, sem varrer página).
   */
  async getPaymentDetail(
    tenantId: string,
    paymentId: string
  ): Promise<PaymentStatusItem | null> {
    const all = fakePaymentHistory(tenantId);
    return all.find((p) => p.payment_id === paymentId) ?? null;
  }
}
