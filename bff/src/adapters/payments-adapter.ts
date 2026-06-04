/**
 * Interface do adapter de pagamentos — K7 (Fase 3).
 *
 * Fronteira com o Postgres (ledger.account_balances + ledger.journal_entries).
 *
 * INVARIANTES:
 *   TX-2: todos os valores monetários são string DECIMAL, nunca number.
 *   TX-3: tenantId vem SEMPRE do ctx (sessão autenticada), nunca do cliente.
 *
 * O adapter real implementa:
 *   1. SET LOCAL adserver.tenant_id = $1 (RLS Postgres, K3).
 *   2. SELECT balance_amount::text, asset_code FROM ledger.account_balances
 *      WHERE tenant_id = current_setting('adserver.tenant_id')
 *   3. SELECT je.id, je.status, je.amount::text, je.asset_code, je.created_at,
 *             pe.rail, pe.pix_end_to_end_id, pe.tx_hash
 *      FROM ledger.journal_entries je
 *      JOIN payments.payment_events pe ON pe.journal_entry_id = je.id
 *      WHERE je.tenant_id = current_setting('adserver.tenant_id')
 *
 * SEGREDOS NUNCA no payload: chaves Stripe/Asaas/Safe, DSN.
 * PII NUNCA no payload: nome/CPF/endereço de contraparte.
 */

import type { Balance, PaymentStatusItem, ListPaymentStatusInput } from "../schemas/payments.js";

export interface PaymentsAdapter {
  /**
   * Retorna os saldos do tenant por ativo.
   * Deriva de ledger.account_balances (Postgres, K3).
   * @param tenantId — UUID do tenant, vem do ctx (sessão). NUNCA do cliente.
   */
  getBalances(tenantId: string): Promise<Balance[]>;

  /**
   * Lista o histórico de pagamentos do tenant com paginação e filtros.
   * Deriva de ledger.journal_entries + eventos K4/K5.
   * @param tenantId — UUID do tenant, vem do ctx (sessão). NUNCA do cliente.
   * @param input    — filtros de query (page, pageSize, rail, status, from, to).
   */
  listPaymentStatus(
    tenantId: string,
    input: ListPaymentStatusInput
  ): Promise<{ items: PaymentStatusItem[]; total: number }>;

  /**
   * Retorna o detalhe de UM pagamento do tenant, ou null se não existir
   * NO ESCOPO do tenant (não distingue "não existe" de "é de outro tenant"
   * — evita vazar existência de IDs alheios; sem IDOR).
   * O adapter real filtra por tenant_id (RLS) E id na própria query —
   * nunca varre uma página client-side.
   * @param tenantId  — UUID do tenant, vem do ctx (sessão). NUNCA do cliente.
   * @param paymentId — id do pagamento a detalhar.
   */
  getPaymentDetail(
    tenantId: string,
    paymentId: string
  ): Promise<PaymentStatusItem | null>;
}
