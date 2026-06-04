/**
 * /copilot — Painel do copiloto do anunciante (J5/Fase 2).
 *
 * Layout: ChatPanel à esquerda + dica de uso à direita.
 * O ChatPanel encapsula todo o estado SSE + HITL (ver components/copilot/chat-panel.tsx).
 *
 * WCAG 2.2 AA: h1 descritivo, skip-nav, aria-labels no layout.
 */

"use client";

import { ChatPanel } from "@/components/copilot/chat-panel";

export default function CopilotPage() {
  return (
    <div className="flex h-full flex-col">
      <div className="border-b border-gray-200 pb-4">
        <h1 className="text-2xl font-bold text-gray-900">
          Copiloto do anunciante
        </h1>
        <p className="mt-1 text-sm text-gray-500">
          Assistente de IA para otimizacao de campanhas. Toda acao de escrita
          (criar, editar, vincular) requer sua aprovacao explicita antes de ser
          aplicada — nunca ocorre de forma autonoma.
        </p>
      </div>

      <div className="mt-4 flex flex-1 gap-6 overflow-hidden">
        {/* Painel principal de chat */}
        <section
          aria-label="Chat com o copiloto"
          className="flex flex-1 flex-col overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm"
          style={{ minHeight: "500px" }}
        >
          <ChatPanel />
        </section>

        {/* Painel lateral: dicas de uso */}
        <aside
          aria-label="Dicas de uso do copiloto"
          className="hidden w-72 shrink-0 xl:block"
        >
          <div className="rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
            <h2 className="text-sm font-semibold text-gray-900">
              O que voce pode perguntar
            </h2>
            <ul
              className="mt-3 space-y-2 text-sm text-gray-600"
              role="list"
            >
              {EXAMPLE_PROMPTS.map((p, i) => (
                <li
                  key={i}
                  className="rounded-md bg-gray-50 px-3 py-2 text-xs leading-relaxed"
                >
                  &ldquo;{p}&rdquo;
                </li>
              ))}
            </ul>

            <div
              role="note"
              className="mt-5 rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800"
            >
              <strong>Seguranca:</strong> o copiloto nao tem acesso a suas
              credenciais. Toda acao de escrita exige sua aprovacao (HITL).
              Forecasts sao estimativas — nunca valores faturaveis.
            </div>
          </div>
        </aside>
      </div>
    </div>
  );
}

const EXAMPLE_PROMPTS = [
  "Qual campanha teve mais cliques nos ultimos 7 dias?",
  "Sugira um ajuste de orcamento para a campanha #42.",
  "Crie uma regra de segmentacao para Brasil, dias uteis, periodo da tarde.",
  "Qual o impacto previsto se eu pausar o banner #10?",
  "Mostre os banners com CTR abaixo de 0,5%.",
  "Vincule a campanha #5 a zona #12.",
];
