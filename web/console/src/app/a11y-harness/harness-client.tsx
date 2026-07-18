"use client";

/**
 * A11yHarness — monta os componentes REAIS do design system com
 * props-fixture, para o gate `make web-a11y` (axe-core + Chrome do
 * sistema via puppeteer-core, G0/frontend E10).
 *
 * REGRAS DO HARNESS:
 *   - Client-only, render estático — nenhuma chamada de rede dispara no
 *     mount (ver comentário em cada seção sobre por que é seguro).
 *   - Usa os componentes de produção diretamente (não duplica markup) —
 *     um achado do axe aqui é um achado real no app.
 *   - NÃO interage (não clica, não envia formulário) — só monta e audita
 *     o DOM estático inicial. Comportamento dinâmico (ex.: focus-trap de
 *     modal sob Tab) está fora do alcance do axe-core (análise estática de
 *     DOM) e é responsabilidade de teste manual/Playwright, não deste gate.
 */

import { useState } from "react";
import { QueryBuilder, type RuleGroupType, type Field } from "react-querybuilder";
import "react-querybuilder/dist/query-builder.css";

import { DataSourceBadge, DataSourceDisclaimer } from "@/components/ui/status-badge";
import { EmptyState, LoadingState, ErrorState } from "@/components/ui/empty-state";
import { MoneyDisplay } from "@/components/ui/money-display";
import { PaymentStatusBadge, PaymentRailBadge } from "@/components/ui/payment-status-badge";
import { HitlDiffPreview } from "@/components/copilot/hitl-diff-preview";
import { ChatPanel } from "@/components/copilot/chat-panel";
import type { WriteDiff, MoneyWire } from "@/lib/copilot-schemas";

// ---------------------------------------------------------------------------
// Fixtures — dados estáticos, nunca vindos de rede
// ---------------------------------------------------------------------------

const MONEY_FIAT: MoneyWire = { amount: "1234.56", currency: "BRL" };
const MONEY_CRYPTO: MoneyWire = { amount: "1500.000000", currency: "USDC" };

const HITL_DIFF: WriteDiff = {
  kind: "campaign",
  action: "update",
  id: "42",
  name: "Campanha de inverno",
  advertiserId: "7",
  status: "active",
  dailyBudget: { amount: "500.00", currency: "BRL" },
  totalBudget: { amount: "15000.00", currency: "BRL" },
};

const QUERYBUILDER_FIELDS: Field[] = [
  { name: "Time - Day of Week", label: "Dia da semana" },
  { name: "Site - URL", label: "URL do site" },
  { name: "Geo - Country", label: "País" },
  { name: "Geo - City", label: "Cidade" },
  { name: "Client - Useragent", label: "User-agent" },
  { name: "Site - Variable", label: "Variável do site" },
];

const QUERYBUILDER_QUERY: RuleGroupType = {
  combinator: "and",
  rules: [
    { field: "Geo - Country", operator: "=", value: "BR" },
    { field: "Time - Day of Week", operator: "=", value: "monday" },
  ],
};

// ---------------------------------------------------------------------------
// no-op — o harness nunca aciona escrita; só existe para satisfazer a prop
// ---------------------------------------------------------------------------
function noopApprove() {}
function noopReject() {}

export function A11yHarness() {
  // Estado local do react-querybuilder — controlado, sem rede.
  const [query, setQuery] = useState<RuleGroupType>(QUERYBUILDER_QUERY);

  return (
    <div className="space-y-10">
      <div>
        <h1 className="text-2xl font-bold text-gray-900">
          Harness de acessibilidade (CI-only)
        </h1>
        <p className="mt-1 text-sm text-gray-500">
          Rota gated por <code>A11Y_HARNESS=1</code> — nunca acessível em
          produção. Monta os componentes reais do design system com dados
          fixos, sem BFF/tRPC vivo, para o gate <code>make web-a11y</code>{" "}
          (axe-core WCAG 2.2 AA).
        </p>
      </div>

      <section aria-labelledby="h-datasource">
        <h2 id="h-datasource" className="text-lg font-semibold text-gray-900">
          Fonte de dados (ADR-0001)
        </h2>
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <DataSourceBadge source="consolidated" asOf="2026-07-17T12:00:00Z" />
          <DataSourceBadge source="live" />
        </div>
        <div className="mt-3">
          <DataSourceDisclaimer />
        </div>
      </section>

      <section aria-labelledby="h-states">
        <h2 id="h-states" className="text-lg font-semibold text-gray-900">
          Estados de empty / loading / error
        </h2>
        <div className="mt-3 space-y-4">
          <EmptyState
            title="Nenhuma campanha encontrada"
            description="Crie sua primeira campanha para começar."
          />
          <LoadingState label="Carregando campanhas..." />
          <ErrorState
            title="Erro ao carregar"
            message="Não foi possível carregar os dados. Tente novamente."
            retry={() => {}}
          />
        </div>
      </section>

      <section aria-labelledby="h-money">
        <h2 id="h-money" className="text-lg font-semibold text-gray-900">
          Dinheiro (TX-2/DA-10)
        </h2>
        <div className="mt-3 flex flex-wrap items-center gap-4 text-sm text-gray-900">
          <MoneyDisplay money={MONEY_FIAT} />
          <MoneyDisplay money={MONEY_CRYPTO} locale="en-US" />
        </div>
      </section>

      <section aria-labelledby="h-payment">
        <h2 id="h-payment" className="text-lg font-semibold text-gray-900">
          Status e trilho de pagamento (K7)
        </h2>
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <PaymentStatusBadge status="pending" />
          <PaymentStatusBadge status="posted" />
          <PaymentStatusBadge status="void" />
          <PaymentRailBadge rail="fiat_stripe" />
          <PaymentRailBadge rail="fiat_asaas" />
          <PaymentRailBadge rail="fiat_mp" />
          <PaymentRailBadge rail="crypto_safe" />
        </div>
      </section>

      <section aria-labelledby="h-hitl">
        <h2 id="h-hitl" className="text-lg font-semibold text-gray-900">
          Diff HITL do copiloto (TX-3/CA-4)
        </h2>
        <p className="mt-1 text-xs text-gray-500">
          NOTA (security-reviewer): este modal não implementa focus-trap —
          Tab/Shift+Tab pode sair do diálogo. Sinalizado, não corrigido aqui
          (gate HITL/segurança, fora do escopo cirúrgico deste E10).
        </p>
        <div className="mt-3">
          <HitlDiffPreview
            threadId="thread-fixture-1"
            diff={HITL_DIFF}
            message="Sugiro aumentar o orçamento diário para acelerar a entrega."
            contradictionWarning='Aviso: a regra sugerida usa AND + "is not" no mesmo vetor de uma regra AND "is" existente — combinação mutuamente exclusiva (CA-4).'
            isApproving={false}
            isRejecting={false}
            onApprove={noopApprove}
            onReject={noopReject}
          />
        </div>
      </section>

      <section aria-labelledby="h-querybuilder">
        <h2 id="h-querybuilder" className="text-lg font-semibold text-gray-900">
          Builder de segmentação (§4.6/CA-4) — react-querybuilder
        </h2>
        <div className="mt-3">
          <QueryBuilder fields={QUERYBUILDER_FIELDS} query={query} onQueryChange={setQuery} />
        </div>
      </section>

      <section aria-labelledby="h-copilot">
        <h2 id="h-copilot" className="text-lg font-semibold text-gray-900">
          Shell do chat do copiloto (J5)
        </h2>
        <p className="mt-1 text-xs text-gray-500">
          Estado idle — nenhuma sessão iniciada, nenhuma chamada de rede
          (startChat só dispara em submit explícito do formulário, que o
          harness não simula).
        </p>
        <div className="mt-3 h-96 rounded-lg border border-gray-200">
          <ChatPanel />
        </div>
      </section>
    </div>
  );
}
