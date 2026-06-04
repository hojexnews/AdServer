"use client";

/**
 * Página de faturamento/pagamentos — K7 (Fase 3).
 *
 * Self-service: saldo por ativo + histórico de pagamentos do tenant.
 *
 * INVARIANTES INEGOCIÁVEIS:
 *
 *   TX-2 — Money como string DECIMAL, nunca Number:
 *     Todo valor monetário vem do BFF como { amount: string, currency: string }.
 *     Formatado exclusivamente via <MoneyDisplay> (decimal.js + Intl.NumberFormat).
 *     NENHUM Number(), parseFloat(), +, * sobre valores monetários aqui.
 *
 *   TX-3 — ACL server-side:
 *     O cliente não envia tenant_id. O BFF injeta do ctx (sessão).
 *     A UI não consegue ver dados de outro tenant.
 *
 *   Cripto FORA do cliente (ADR-0004 §C / §E.9):
 *     Sem wagmi, viem, WalletConnect, ethers.js ou qualquer lib de carteira.
 *     A UI exibe apenas o status de transações cripto (tx_hash opaco).
 *     Assinatura on-chain NÃO é responsabilidade do front (K5 cuida no BFF/Safe).
 *
 *   Sem PII de terceiros:
 *     pix_end_to_end_id e tx_hash são identificadores opacos.
 *     Não exibimos nome/CPF/endereço de contrapartes.
 */

import { useState } from "react";
import { trpc } from "@/lib/trpc";
import { MoneyDisplay } from "@/components/ui/money-display";
import {
  PaymentStatusBadge,
  PaymentRailBadge,
} from "@/components/ui/payment-status-badge";
import {
  LoadingState,
  ErrorState,
  EmptyState,
} from "@/components/ui/empty-state";

// ---------------------------------------------------------------------------
// Tipos locais espelhando os schemas do BFF (sem importar runtime do BFF)
// ---------------------------------------------------------------------------

type PaymentRail = "fiat_stripe" | "fiat_asaas" | "fiat_mp" | "crypto_safe";
type PaymentStatus = "pending" | "posted" | "void";

interface MoneyWireLocal {
  amount: string;   // string DECIMAL — NUNCA Number (TX-2)
  currency: string;
}

interface BalanceItem {
  asset_code: string;
  amount: string;   // string DECIMAL — NUNCA Number (TX-2)
  scale: number;
  label: string;
}

interface PaymentStatusItem {
  payment_id: string;
  rail: PaymentRail;
  status: PaymentStatus;
  amount: MoneyWireLocal;
  created_at: string;
  pix_end_to_end_id?: string;
  tx_hash?: string;
}

// ---------------------------------------------------------------------------
// Componente principal
// ---------------------------------------------------------------------------

export default function BillingPage() {
  const [page, setPage] = useState(1);
  const PAGE_SIZE = 10;

  // Saldos do tenant — TX-3: sem tenant_id no input (BFF injeta do ctx)
  const balancesQuery = trpc.payments.balances.useQuery(undefined, {
    refetchInterval: 60_000, // revalida a cada 1 min
  });

  // Histórico de pagamentos — TX-3: idem
  // placeholderData mantém dados anteriores durante re-fetch (TanStack Query v5)
  const statusQuery = trpc.payments.paymentStatus.useQuery(
    { page, pageSize: PAGE_SIZE }
  );

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900">
        Faturamento e Pagamentos
      </h1>
      <p className="mt-1 text-sm text-gray-500">
        Saldo atual e histórico de transacoes do seu tenant.
      </p>

      {/* ================================================================
          Seção 1: Saldos por ativo
          amount: string DECIMAL via <MoneyDisplay> — NUNCA Number (TX-2)
          ================================================================ */}
      <section aria-labelledby="balances-heading" className="mt-8">
        <h2
          id="balances-heading"
          className="text-lg font-semibold text-gray-900"
        >
          Saldo atual
        </h2>
        <p className="text-sm text-gray-500">
          Valores em tempo real do ledger do seu tenant.
        </p>

        {balancesQuery.isLoading && (
          <LoadingState label="Carregando saldos..." />
        )}

        {balancesQuery.error && (
          <ErrorState
            message={balancesQuery.error.message}
            retry={() => { void balancesQuery.refetch(); }}
          />
        )}

        {balancesQuery.data && balancesQuery.data.length === 0 && (
          <EmptyState
            title="Nenhum saldo disponivel"
            description="Realize um deposito para comecar a usar o AdServer."
          />
        )}

        {balancesQuery.data && balancesQuery.data.length > 0 && (
          <BalanceCards balances={balancesQuery.data as BalanceItem[]} />
        )}
      </section>

      {/* ================================================================
          Seção 2: Histórico de pagamentos
          Sem aritmética no cliente. Status via PaymentStatusBadge.
          ================================================================ */}
      <section
        aria-labelledby="history-heading"
        className="mt-10 border-t border-gray-200 pt-8"
      >
        <h2
          id="history-heading"
          className="text-lg font-semibold text-gray-900"
        >
          Historico de pagamentos
        </h2>
        <p className="text-sm text-gray-500">
          Transacoes confirmadas, pendentes e canceladas do seu tenant.
        </p>

        {statusQuery.isLoading && (
          <LoadingState label="Carregando historico..." />
        )}

        {statusQuery.error && (
          <ErrorState
            message={statusQuery.error.message}
            retry={() => { void statusQuery.refetch(); }}
          />
        )}

        {statusQuery.data && statusQuery.data.items.length === 0 && (
          <EmptyState
            title="Nenhuma transacao encontrada"
            description="O historico de pagamentos aparecera aqui."
          />
        )}

        {statusQuery.data && statusQuery.data.items.length > 0 && (
          <>
            <PaymentTable
              items={statusQuery.data.items as PaymentStatusItem[]}
            />
            {/* Paginação */}
            <Pagination
              page={page}
              hasMore={statusQuery.data.hasMore}
              total={statusQuery.data.total}
              pageSize={PAGE_SIZE}
              onPrev={() => setPage((p) => Math.max(1, p - 1))}
              onNext={() => setPage((p) => p + 1)}
            />
          </>
        )}
      </section>
    </div>
  );
}

// ---------------------------------------------------------------------------
// BalanceCards — exibe saldos por ativo
// Usa MoneyDisplay para formatar: NUNCA Number() ou parseFloat() (TX-2)
// ---------------------------------------------------------------------------

function BalanceCards({ balances }: { balances: BalanceItem[] }) {
  return (
    <dl
      className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3"
      aria-label="Saldos por ativo"
    >
      {balances.map((bal) => (
        <div
          key={bal.asset_code}
          className="rounded-lg border border-gray-200 bg-white p-5"
        >
          <dt className="flex items-center gap-2">
            <span className="text-xs font-semibold uppercase tracking-wide text-gray-500">
              {bal.asset_code}
            </span>
            <span className="text-xs text-gray-400">{bal.label}</span>
          </dt>
          <dd className="mt-2 text-2xl font-bold text-gray-900">
            {/*
              MoneyDisplay recebe { amount: string, currency: string }.
              amount é string DECIMAL — NUNCA Number (TX-2).
              formatMoney usa decimal.js + Intl.NumberFormat para exibição.
            */}
            <MoneyDisplay
              money={{ amount: bal.amount, currency: bal.asset_code }}
            />
          </dd>
          <p className="mt-1 text-xs text-gray-400">
            {bal.scale} casas decimais
          </p>
        </div>
      ))}
    </dl>
  );
}

// ---------------------------------------------------------------------------
// PaymentTable — tabela de histórico de pagamentos
// Sem aritmética monetária. MoneyDisplay para valores. Badges para status/rail.
// Sem PII de terceiros — apenas pix_end_to_end_id e tx_hash opacos.
// ---------------------------------------------------------------------------

function PaymentTable({ items }: { items: PaymentStatusItem[] }) {
  return (
    <div className="mt-4 overflow-x-auto rounded-lg border border-gray-200 bg-white">
      <table
        className="w-full border-collapse text-sm"
        aria-label="Historico de pagamentos"
      >
        <thead>
          <tr className="border-b border-gray-200 bg-gray-50">
            <th
              scope="col"
              className="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wide text-gray-600"
            >
              ID
            </th>
            <th
              scope="col"
              className="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wide text-gray-600"
            >
              Data
            </th>
            <th
              scope="col"
              className="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wide text-gray-600"
            >
              Trilho
            </th>
            <th
              scope="col"
              className="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wide text-gray-600"
            >
              Status
            </th>
            <th
              scope="col"
              className="px-4 py-3 text-right text-xs font-semibold uppercase tracking-wide text-gray-600"
            >
              Valor
            </th>
            <th
              scope="col"
              className="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wide text-gray-600"
            >
              Referencia
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-100">
          {items.map((item) => (
            <tr key={item.payment_id} className="hover:bg-gray-50">
              {/* ID opaco — não revela PII */}
              <td className="px-4 py-3 font-mono text-xs text-gray-400">
                {item.payment_id}
              </td>

              {/* Data formatada via Intl — sem Number() para dinheiro */}
              <td className="whitespace-nowrap px-4 py-3 text-xs text-gray-600">
                {new Date(item.created_at).toLocaleString("pt-BR", {
                  dateStyle: "short",
                  timeStyle: "short",
                })}
              </td>

              {/* Trilho de pagamento */}
              <td className="px-4 py-3">
                <PaymentRailBadge rail={item.rail} />
              </td>

              {/* Status do pagamento */}
              <td className="px-4 py-3">
                <PaymentStatusBadge status={item.status} />
              </td>

              {/* Valor: MoneyDisplay — string DECIMAL + currency, NUNCA Number (TX-2) */}
              <td className="px-4 py-3 text-right font-semibold tabular-nums text-gray-900">
                <MoneyDisplay money={item.amount} />
              </td>

              {/* Referência opaca: PIX E2E ID ou tx_hash cripto — sem PII */}
              <td className="px-4 py-3">
                <OpaqueRef
                  pix={item.pix_end_to_end_id}
                  txHash={item.tx_hash}
                />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ---------------------------------------------------------------------------
// OpaqueRef — exibe referência opaca (PIX E2E ou tx_hash) de forma truncada.
// Sem PII. Apenas identifica a transação para suporte/auditoria do tenant.
// ---------------------------------------------------------------------------

function OpaqueRef({
  pix,
  txHash,
}: {
  pix?: string;
  txHash?: string;
}) {
  if (pix) {
    const short = pix.length > 12 ? `${pix.slice(0, 8)}...${pix.slice(-4)}` : pix;
    return (
      <span
        className="font-mono text-xs text-gray-500"
        title={`PIX E2E: ${pix}`}
        aria-label={`Identificador PIX: ${pix}`}
      >
        PIX {short}
      </span>
    );
  }

  if (txHash) {
    const short =
      txHash.length > 14
        ? `${txHash.slice(0, 8)}...${txHash.slice(-6)}`
        : txHash;
    return (
      <span
        className="font-mono text-xs text-gray-500"
        title={`TX: ${txHash}`}
        aria-label={`Hash da transacao on-chain: ${txHash}`}
      >
        {short}
      </span>
    );
  }

  return <span className="text-xs text-gray-400" aria-label="Sem referencia">—</span>;
}

// ---------------------------------------------------------------------------
// Paginação simples
// ---------------------------------------------------------------------------

interface PaginationProps {
  page: number;
  hasMore: boolean;
  total: number;
  pageSize: number;
  onPrev: () => void;
  onNext: () => void;
}

function Pagination({
  page,
  hasMore,
  total,
  pageSize,
  onPrev,
  onNext,
}: PaginationProps) {
  const start = (page - 1) * pageSize + 1;
  const end = Math.min(page * pageSize, total);

  return (
    <nav
      className="mt-4 flex items-center justify-between text-sm text-gray-600"
      aria-label="Paginacao do historico de pagamentos"
    >
      <span>
        Exibindo {start}–{end} de {total}
      </span>
      <div className="flex gap-2">
        <button
          onClick={onPrev}
          disabled={page === 1}
          className="rounded-md border border-gray-300 px-3 py-1.5 text-sm font-medium hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-2 focus-visible:ring-brand-500"
          aria-label="Pagina anterior"
        >
          Anterior
        </button>
        <button
          onClick={onNext}
          disabled={!hasMore}
          className="rounded-md border border-gray-300 px-3 py-1.5 text-sm font-medium hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-2 focus-visible:ring-brand-500"
          aria-label="Proxima pagina"
        >
          Proxima
        </button>
      </div>
    </nav>
  );
}
