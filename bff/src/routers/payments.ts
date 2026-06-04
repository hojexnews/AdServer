/**
 * Router de pagamentos — K7 (Fase 3).
 *
 * Somente leitura (queries): sem mutations de iniciação de pagamento.
 * K7 = status/saldo via BFF. Pagamentos são iniciados por K4/K5 (fora do scope).
 *
 * INVARIANTES DE SEGURANÇA (security-reviewer vai auditar):
 *
 *   TX-3 — ACL server-side:
 *     tenant_id SEMPRE de ctx.tenantId (sessão autenticada pelo middleware).
 *     NUNCA aceito de input do cliente.
 *     Toda query passa ctx.tenantId ao adapter → RLS Postgres filtra.
 *
 *   TX-2 — Money como string DECIMAL:
 *     O BFF repassa amount como string (MoneySchema).
 *     Sem aritmética monetária. Sem Number().
 *
 *   Segredos NUNCA no payload:
 *     Sem chaves Stripe/Asaas/Safe, sem DSN, sem PII de terceiros.
 *     pix_end_to_end_id e tx_hash são identificadores opacos do próprio tenant.
 *
 *   Read-only:
 *     Sem mutations. Sem endpoints que movam dinheiro.
 *
 *   IDOR prevenido:
 *     O cliente não consegue ler saldo/status de outro tenant porque
 *     o tenant_id é fixado pelo ctx (não parametrizado pelo cliente).
 */

import { router, tenantProcedure } from "../lib/trpc.js";
import type { PaymentsAdapter } from "../adapters/payments-adapter.js";
import {
  BalanceSchema,
  ListPaymentStatusInputSchema,
  PaymentStatusItemSchema,
  PaymentStatusPageSchema,
} from "../schemas/payments.js";
import { z } from "zod";

export function createPaymentsRouter(adapter: PaymentsAdapter) {
  return router({
    /**
     * balances — saldos do tenant por ativo.
     *
     * Deriva de ledger.account_balances (Postgres, K3).
     * tenant_id: ctx.tenantId — NUNCA do input (TX-3).
     * amount: string DECIMAL — NUNCA number (TX-2).
     */
    balances: tenantProcedure
      .output(z.array(BalanceSchema))
      .query(async ({ ctx }) => {
        // TX-3: tenant_id do ctx, nunca de parâmetros do cliente.
        const balances = await adapter.getBalances(ctx.tenantId);
        return balances;
      }),

    /**
     * paymentStatus — histórico paginado de pagamentos do tenant.
     *
     * Deriva de ledger.journal_entries + eventos K4/K5.
     * tenant_id: ctx.tenantId — NUNCA do input (TX-3).
     * amount: MoneySchema (string DECIMAL + currency) — NUNCA number (TX-2).
     *
     * Sem segredos no payload: sem chaves de provedor, sem DSN.
     * Sem PII de terceiros: sem nome/CPF/endereço de contraparte.
     * pix_end_to_end_id e tx_hash são opacos (apenas do próprio tenant).
     */
    paymentStatus: tenantProcedure
      .input(ListPaymentStatusInputSchema)
      .output(PaymentStatusPageSchema)
      .query(async ({ ctx, input }) => {
        // TX-3: tenant_id do ctx, nunca de parâmetros do cliente.
        const { items, total } = await adapter.listPaymentStatus(
          ctx.tenantId,
          input
        );

        return {
          items,
          total,
          page: input.page,
          pageSize: input.pageSize,
          hasMore: input.page * input.pageSize < total,
        };
      }),

    /**
     * paymentDetail — detalhe de um pagamento específico do tenant.
     *
     * Garante que o payment_id pertence ao tenant (sem IDOR):
     * o adapter filtra por tenant_id via RLS antes de verificar o id.
     */
    paymentDetail: tenantProcedure
      .input(z.object({ paymentId: z.string().min(1) }))
      .output(PaymentStatusItemSchema.nullable())
      .query(async ({ ctx, input }) => {
        // TX-3: tenant_id do ctx. O adapter filtra por tenant_id (RLS) E id
        // na própria query — sem varrer página, sem IDOR. Retorna null (não
        // 404) para não vazar existência de IDs de outros tenants.
        return adapter.getPaymentDetail(ctx.tenantId, input.paymentId);
      }),
  });
}
