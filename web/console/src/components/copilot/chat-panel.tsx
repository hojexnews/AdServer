/**
 * ChatPanel — painel de chat com o copiloto do anunciante (J5/Fase 2).
 *
 * FLUXO:
 *   1. Usuário digita mensagem e envia.
 *   2. startChat() → mutation tRPC copilot.chat → sessionId + streamUrl.
 *   3. SSE via EventSource: tokens acumulados em aria-live="polite".
 *   4. tool_call: ToolCallIndicator em andamento.
 *   5. hitl_required: stream parado → HitlDiffPreview com botoes Aplicar/Cancelar.
 *   6. done/error: estado final.
 *
 * INVARIANTES (TX-3 / security-reviewer):
 *   - A UI nunca envia tenant_id — resolvido server-side no BFF.
 *   - Nenhuma escrita sem aprovação HITL explícita do usuário.
 *   - Conteúdo do LLM renderizado como texto puro (escapado pelo React).
 *   - Sem dangerouslySetInnerHTML em nenhum ponto.
 *
 * WCAG 2.2 AA:
 *   - aria-live="polite" para tokens em streaming.
 *   - aria-label no campo de entrada + botão.
 *   - Foco gerenciado: após envio, foco retorna ao input.
 *   - Contraste: classes Tailwind usam tokens de design system (brand-600, gray-900 etc.).
 *   - role="log" no histórico de mensagens (atualizações incrementais).
 *   - Estados de loading/error/empty tratados.
 */

"use client";

import { useRef, useEffect, FormEvent, useState } from "react";
import { useCopilotSession } from "@/lib/use-copilot-session";
import { HitlDiffPreview } from "./hitl-diff-preview";
import { ToolCallIndicator } from "./tool-call-indicator";

export function ChatPanel() {
  const { state, startChat, approve, reject, reset, isStarting } = useCopilotSession();
  const [input, setInput] = useState("");
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const messagesEndRef = useRef<HTMLDivElement>(null);

  // Auto-scroll para o fim do chat
  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [state.messages, state.activeTool, state.hitl]);

  // Devolve o foco ao input após o envio (quando não está em HITL)
  useEffect(() => {
    if (state.status === "idle" || state.status === "done" || state.status === "error") {
      inputRef.current?.focus();
    }
  }, [state.status]);

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = input.trim();
    if (!trimmed) return;
    setInput("");
    void startChat(trimmed, state.sessionId ?? undefined);
  };

  const isBusy =
    state.status === "starting" ||
    state.status === "streaming" ||
    state.status === "hitl_pending" ||
    state.status === "hitl_approving" ||
    state.status === "hitl_rejecting" ||
    isStarting;

  return (
    <div className="flex h-full flex-col">
      {/* Cabeçalho do painel */}
      <div className="flex items-center justify-between border-b border-gray-200 px-4 py-3">
        <div>
          <h2 className="text-sm font-semibold text-gray-900">
            Copiloto do anunciante
          </h2>
          <p className="text-xs text-gray-500">
            Assistente de IA — toda escrita requer aprovacao humana (HITL)
          </p>
        </div>
        {state.status !== "idle" && (
          <button
            type="button"
            onClick={reset}
            disabled={isBusy && state.status !== "hitl_pending"}
            className="text-xs text-gray-500 underline hover:text-gray-700 focus-visible:ring-2 focus-visible:ring-brand-500 disabled:opacity-40"
            aria-label="Iniciar nova sessao"
          >
            Nova sessao
          </button>
        )}
      </div>

      {/* Histórico de mensagens */}
      <div
        role="log"
        aria-label="Historico de mensagens do copiloto"
        aria-live="polite"
        aria-atomic="false"
        className="flex-1 overflow-y-auto px-4 py-4 space-y-4"
      >
        {state.messages.length === 0 && (
          <div className="flex flex-col items-center justify-center h-full py-12 text-center">
            <p className="text-sm font-medium text-gray-900">
              Bem-vindo ao copiloto
            </p>
            <p className="mt-1 text-xs text-gray-500 max-w-xs">
              Pergunte sobre suas campanhas, solicite analises ou pecas
              sugestoes de otimizacao. Toda acao de escrita requer sua
              aprovacao antes de ser aplicada.
            </p>
          </div>
        )}

        {state.messages.map((msg, i) => (
          <MessageBubble
            key={i}
            role={msg.role}
            content={msg.content}
            streaming={msg.streaming}
          />
        ))}

        {/* Tool call em andamento */}
        {state.activeTool && (
          <div className="mt-2">
            <ToolCallIndicator tool={state.activeTool} />
          </div>
        )}

        {/* HITL: pausa o stream e exibe o diff para aprovacao */}
        {state.hitl && (
          state.status === "hitl_pending" ||
          state.status === "hitl_approving" ||
          state.status === "hitl_rejecting"
        ) && state.hitl && (
          <div className="mt-4" aria-atomic="true">
            <HitlDiffPreview
              threadId={state.hitl.threadId}
              diff={state.hitl.diff}
              message={state.hitl.message}
              contradictionWarning={state.hitl.contradictionWarning}
              isApproving={state.status === "hitl_approving"}
              isRejecting={state.status === "hitl_rejecting"}
              onApprove={(tid) => void approve(tid)}
              onReject={(tid, reason) => void reject(tid, reason)}
            />
          </div>
        )}

        {/* Estado de aprovacao/rejeicao em progresso */}
        {(state.status === "hitl_approving" || state.status === "hitl_rejecting") && (
          <div
            role="status"
            aria-live="polite"
            className="flex items-center gap-2 rounded-md border border-blue-200 bg-blue-50 px-3 py-2 text-sm text-blue-800"
          >
            <span
              className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-blue-300 border-t-blue-600"
              aria-hidden="true"
            />
            {state.status === "hitl_approving" ? "Aplicando alteração..." : "Cancelando..."}
          </div>
        )}

        {/* Erro */}
        {state.status === "error" && state.error && (
          <div
            role="alert"
            aria-live="assertive"
            className="rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-800"
          >
            <strong>Erro:</strong> {state.error}
          </div>
        )}

        {/* Uso de tokens (ao terminar) */}
        {state.status === "done" && state.usage && (
          <div
            role="status"
            className="text-xs text-gray-400 text-right"
          >
            Tokens usados: {state.usage.input_tokens} entrada /
            {state.usage.output_tokens} saida
          </div>
        )}

        <div ref={messagesEndRef} aria-hidden="true" />
      </div>

      {/* Input de mensagem */}
      <div className="border-t border-gray-200 px-4 py-3">
        <form
          onSubmit={handleSubmit}
          aria-label="Enviar mensagem ao copiloto"
          className="flex gap-2"
        >
          <label htmlFor="copilot-input" className="sr-only">
            Mensagem para o copiloto
          </label>
          <textarea
            id="copilot-input"
            ref={inputRef}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              // Enter sem Shift envia a mensagem
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                if (!isBusy && input.trim()) {
                  handleSubmit(e as unknown as FormEvent);
                }
              }
            }}
            disabled={isBusy}
            placeholder={
              state.status === "hitl_pending"
                ? "Aguardando sua aprovacao no diff acima..."
                : "Pergunte ao copiloto... (Enter para enviar)"
            }
            rows={2}
            maxLength={10000}
            aria-describedby="copilot-input-hint"
            className={[
              "flex-1 resize-none rounded-lg border px-3 py-2 text-sm",
              "border-gray-300 bg-white text-gray-900 placeholder-gray-400",
              "focus-visible:ring-2 focus-visible:ring-brand-500 focus-visible:outline-none",
              "disabled:bg-gray-50 disabled:text-gray-400",
            ].join(" ")}
          />
          <p id="copilot-input-hint" className="sr-only">
            Pressione Enter para enviar, Shift+Enter para nova linha.
            Toda acao de escrita requer sua aprovacao antes de ser aplicada.
          </p>
          <button
            type="submit"
            disabled={isBusy || !input.trim()}
            aria-label="Enviar mensagem"
            aria-busy={state.status === "starting" || isStarting}
            className={[
              "shrink-0 self-end rounded-lg px-4 py-2 text-sm font-semibold",
              "bg-brand-600 text-white hover:bg-brand-700",
              "focus-visible:ring-2 focus-visible:ring-brand-500",
              "disabled:opacity-50",
            ].join(" ")}
          >
            {state.status === "starting" || isStarting ? "..." : "Enviar"}
          </button>
        </form>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// MessageBubble — exibe uma mensagem do usuário ou do assistente
// SEGURANÇA: conteúdo renderizado como texto (React escapa automaticamente).
// WCAG: role e aria corretos, streaming sinalizado via aria-live no log pai.
// ---------------------------------------------------------------------------

interface MessageBubbleProps {
  role: "user" | "assistant";
  content: string;
  streaming?: boolean;
}

function MessageBubble({ role, content, streaming }: MessageBubbleProps) {
  const isUser = role === "user";

  return (
    <div
      className={["flex", isUser ? "justify-end" : "justify-start"].join(" ")}
    >
      <div
        className={[
          "max-w-prose rounded-2xl px-4 py-2.5 text-sm",
          isUser
            ? "bg-brand-600 text-white"
            : "bg-gray-100 text-gray-900",
          streaming ? "animate-pulse" : "",
        ].join(" ")}
        aria-label={isUser ? "Sua mensagem" : "Resposta do copiloto"}
      >
        {/* Conteúdo renderizado como whitespace-pre-wrap — React escapa automaticamente */}
        <p className="whitespace-pre-wrap break-words">{content}</p>
        {streaming && (
          <span
            aria-hidden="true"
            className="mt-1 inline-block h-3 w-1 animate-pulse bg-current opacity-70"
          />
        )}
      </div>
    </div>
  );
}
