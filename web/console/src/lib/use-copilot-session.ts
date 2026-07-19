/**
 * Hook de sessão do copiloto — máquina de estado SSE + HITL (J5/Fase 2).
 *
 * FLUXO:
 *   1. Chamar startChat(message) → mutation tRPC copilot.chat → obtém sessionId + streamUrl + threadId.
 *   2. Abrir EventSource para streamUrl; acumular tokens (aria-live).
 *   3. Ao receber "hitl_required": pausar stream (fechar EventSource), armazenar diff + threadId.
 *   4. Usuário clica "Aplicar" → approve(threadId) → mutation tRPC copilot.hitlApprove.
 *   5. Usuário clica "Cancelar" → reject(threadId, reason) → mutation tRPC copilot.hitlReject.
 *   6. Ao receber "done" ou "error": fechar EventSource, atualizar estado.
 *
 * INVARIANTES (TX-3 / security-reviewer):
 *   - A UI nunca envia tenant_id — resolvido server-side no BFF.
 *   - A UI nunca fala diretamente com services/copilot ou com a API da Anthropic.
 *   - O EventSource aponta para /api/copilot/stream/:sessionId (rota Next.js que faz proxy).
 *   - Nenhuma escrita ocorre sem clique humano (HITL obrigatório).
 *
 * INVARIANTE DE DINHEIRO (TX-2):
 *   - Valores monetários em WriteDiff são string DECIMAL via MoneyWire.
 *   - Este hook não faz aritmética monetária — apenas passa adiante para exibição.
 */

"use client";

import { useCallback, useReducer, useRef } from "react";
import { trpc } from "@/lib/trpc";
import { parseSseEvent } from "@/lib/copilot-schemas";
import type {
  SseHitlRequiredEvent,
  SseToolCallEvent,
  SseDoneEvent,
  WriteDiff,
} from "@/lib/copilot-schemas";
import type { RuleCandidate } from "@/lib/contradiction";
import { detectContradictions } from "@/lib/contradiction";

// ---------------------------------------------------------------------------
// Tipos de estado
// ---------------------------------------------------------------------------

export interface ChatMessage {
  role: "user" | "assistant";
  content: string;
  /** true enquanto o token ainda está chegando */
  streaming?: boolean;
}

export interface ToolCallState {
  tool: string;
  status: "running" | "done";
  result: unknown;
}

export interface HitlState {
  threadId: string;
  diff: WriteDiff;
  message: string;
  /** Aviso anti-contradição se o diff contém regras AND mutuamente exclusivas */
  contradictionWarning: string | null;
}

export type SessionStatus =
  | "idle"
  | "starting"
  | "streaming"
  | "hitl_pending"
  | "hitl_approving"
  | "hitl_rejecting"
  | "done"
  | "error";

export interface CopilotSessionState {
  status: SessionStatus;
  messages: ChatMessage[];
  activeTool: ToolCallState | null;
  hitl: HitlState | null;
  sessionId: string | null;
  threadId: string | null;
  usage: SseDoneEvent["usage"] | null;
  error: string | null;
}

// ---------------------------------------------------------------------------
// Reducer
// ---------------------------------------------------------------------------

type Action =
  | { type: "START_CHAT"; sessionId: string; threadId: string }
  | { type: "ADD_USER_MESSAGE"; content: string }
  | { type: "BEGIN_STREAMING" }
  | { type: "TOKEN"; text: string }
  | { type: "TOOL_CALL"; event: SseToolCallEvent }
  | { type: "HITL_REQUIRED"; event: SseHitlRequiredEvent; warning: string | null }
  | { type: "DONE"; event: SseDoneEvent }
  | { type: "ERROR"; message: string }
  | { type: "HITL_APPROVING" }
  | { type: "HITL_REJECTING" }
  | { type: "HITL_RESOLVED" }
  | { type: "RESET" };

const INITIAL_STATE: CopilotSessionState = {
  status: "idle",
  messages: [],
  activeTool: null,
  hitl: null,
  sessionId: null,
  threadId: null,
  usage: null,
  error: null,
};

function reducer(state: CopilotSessionState, action: Action): CopilotSessionState {
  switch (action.type) {
    case "RESET":
      return { ...INITIAL_STATE };

    case "ADD_USER_MESSAGE":
      return {
        ...state,
        messages: [...state.messages, { role: "user", content: action.content }],
      };

    case "START_CHAT":
      return {
        ...state,
        status: "starting",
        sessionId: action.sessionId,
        threadId: action.threadId,
        error: null,
        hitl: null,
        activeTool: null,
      };

    case "BEGIN_STREAMING":
      return {
        ...state,
        status: "streaming",
        // Abre slot para a resposta do assistente que vai ser preenchida por tokens
        messages: [...state.messages, { role: "assistant", content: "", streaming: true }],
        activeTool: null,
      };

    case "TOKEN": {
      const msgs = [...state.messages];
      const last = msgs[msgs.length - 1];
      if (last?.role === "assistant") {
        msgs[msgs.length - 1] = {
          ...last,
          content: last.content + action.text,
          streaming: true,
        };
      }
      return { ...state, messages: msgs };
    }

    case "TOOL_CALL":
      return {
        ...state,
        activeTool: {
          tool: action.event.tool,
          status: action.event.status,
          result: action.event.result,
        },
      };

    case "HITL_REQUIRED": {
      // Finaliza o streaming da mensagem atual
      const msgs = state.messages.map((m) =>
        m.role === "assistant" && m.streaming ? { ...m, streaming: false } : m
      );
      return {
        ...state,
        status: "hitl_pending",
        messages: msgs,
        activeTool: null,
        hitl: {
          threadId: action.event.thread_id,
          diff: action.event.diff,
          message: action.event.message,
          contradictionWarning: action.warning,
        },
      };
    }

    case "DONE": {
      const msgs = state.messages.map((m) =>
        m.role === "assistant" && m.streaming ? { ...m, streaming: false } : m
      );
      return {
        ...state,
        status: "done",
        messages: msgs,
        activeTool: null,
        hitl: null,
        usage: action.event.usage,
      };
    }

    case "ERROR": {
      const msgs = state.messages.map((m) =>
        m.role === "assistant" && m.streaming ? { ...m, streaming: false } : m
      );
      return {
        ...state,
        status: "error",
        messages: msgs,
        activeTool: null,
        error: action.message,
      };
    }

    case "HITL_APPROVING":
      return { ...state, status: "hitl_approving" };

    case "HITL_REJECTING":
      return { ...state, status: "hitl_rejecting" };

    case "HITL_RESOLVED":
      return {
        ...state,
        status: "streaming",
        hitl: null,
        // Abre novo slot para resposta pós-HITL
        messages: [...state.messages, { role: "assistant", content: "", streaming: true }],
      };

    default:
      return state;
  }
}

// ---------------------------------------------------------------------------
// Validação anti-contradição sobre sugestões de regras (CA-4)
//
// Reutiliza de fato detectContradictions() de lib/contradiction.ts — o MESMO
// algoritmo usado pelo builder de segmentação (app/rules/page.tsx) — contra o
// conjunto real [regras JÁ EXISTENTES do owner (ownerType/ownerId) + candidato
// sugerido pela IA]. Sem as regras existentes, um candidato isolado nunca pode
// contradizer a si mesmo (par precisa de 2+ elementos), então buscamos as
// regras persistidas via trpc.cfg.deliveryRule.list antes de decidir.
//
// FIX #17 (achado da 27ª onda): a versão anterior comparava o candidato
// consigo mesmo (`detectContradictions([candidate, candidate])`), um
// self-compare que NUNCA detecta nada real, e descartava o resultado
// (`void selfCheck`) — o aviso exibido era um heurístico textual solto,
// não a validação anti-contradição que a doc/mandato prometiam. Corrigido
// para buscar o contexto real e rodar a validação de verdade.
// ---------------------------------------------------------------------------

/**
 * Verifica se o WriteDiff de uma regra sugerida contradiz o conjunto de
 * regras existentes fornecido. Função pura e síncrona — testável sem
 * trpc/React (ver use-copilot-session.test.ts).
 *
 * @param diff - WriteDiff recebido no evento SSE hitl_required.
 * @param existingRules - Regras JÁ SALVAS do mesmo owner (ownerType/ownerId
 *   do diff), tipicamente obtidas via trpc.cfg.deliveryRule.list.fetch().
 *   Passe [] se a busca não for possível — o resultado será conservador
 *   (nenhum falso-positivo, mas também nenhuma detecção sem contexto).
 */
export function checkDiffForContradictions(
  diff: WriteDiff,
  existingRules: RuleCandidate[] = []
): string | null {
  if (diff.kind !== "rule") return null;
  if (diff.action === "delete") return null;
  if (!diff.vector || !diff.operator || !diff.value || !diff.logicalOp) return null;

  const candidate: RuleCandidate = {
    vector: diff.vector,
    operator: diff.operator,
    value: diff.value,
    logicalOp: diff.logicalOp as "AND" | "OR",
  };

  // Validação REAL: mesmo detectContradictions() do builder, sobre
  // [regras existentes do owner + candidato sugerido].
  const result = detectContradictions([...existingRules, candidate]);
  if (!result.hasContradiction) return null;

  // Reporta apenas os conflitos que envolvem o candidato sugerido — conflitos
  // pré-existentes só entre regras já salvas não são responsabilidade desta
  // sugestão específica (identidade de referência: candidate é o mesmo objeto
  // inserido no array acima, detectContradictions não clona as entradas).
  const relevant = result.conflicts.filter(
    (c) => c.ruleA === candidate || c.ruleB === candidate
  );
  if (relevant.length === 0) return null;

  return (
    `Contradição detectada (CA-4): a regra sugerida entra em conflito com ` +
    `${relevant.length} regra(s) já existente(s). ` +
    relevant.map((c) => c.reason).join(" ")
  );
}

/**
 * Busca as regras existentes do owner do diff (quando aplicável) e roda
 * checkDiffForContradictions() com o contexto real.
 *
 * `listExistingRules` é injetado (em vez de chamar trpc diretamente) para
 * manter esta função pura/testável com um fetcher fake — a produção passa
 * um wrapper sobre trpc.cfg.deliveryRule.list.fetch() (ver useCopilotSession).
 */
export async function resolveContradictionWarning(
  diff: WriteDiff,
  listExistingRules: (
    ownerType: "campaign" | "banner",
    ownerId: string
  ) => Promise<RuleCandidate[]>
): Promise<string | null> {
  let existingRules: RuleCandidate[] = [];

  if (diff.kind === "rule" && diff.ownerType && diff.ownerId) {
    try {
      existingRules = await listExistingRules(diff.ownerType, diff.ownerId);
    } catch {
      // Falha ao buscar regras existentes não deve travar o fluxo HITL —
      // apenas seguimos sem esse contexto (conservador: não inventa contradição).
      existingRules = [];
    }
  }

  return checkDiffForContradictions(diff, existingRules);
}

// ---------------------------------------------------------------------------
// Hook principal
// ---------------------------------------------------------------------------

export function useCopilotSession() {
  const [state, dispatch] = useReducer(reducer, INITIAL_STATE);
  const esRef = useRef<EventSource | null>(null);
  const pendingMessageRef = useRef<string>("");

  const chatMutation = trpc.copilot.chat.useMutation();
  const approveMutation = trpc.copilot.hitlApprove.useMutation();
  const rejectMutation = trpc.copilot.hitlReject.useMutation();
  const utils = trpc.useUtils();

  // ---------------------------------------------------------------------------
  // listExistingRules — busca as regras JÁ SALVAS do owner via o MESMO
  // procedimento tRPC que o builder de segmentação usa (trpc.cfg.deliveryRule),
  // para alimentar checkDiffForContradictions()/resolveContradictionWarning()
  // com o contexto real (CA-4 / FIX #17).
  // ---------------------------------------------------------------------------
  const listExistingRules = useCallback(
    async (
      ownerType: "campaign" | "banner",
      ownerId: string
    ): Promise<RuleCandidate[]> => {
      const rules = await utils.cfg.deliveryRule.list.fetch({ ownerType, ownerId });
      return rules
        .filter((r) => r.active)
        .map((r) => ({
          vector: r.vector,
          operator: r.operator,
          value: r.value,
          logicalOp: r.logicalOp,
        }));
    },
    [utils]
  );

  // ---------------------------------------------------------------------------
  // Abre o EventSource SSE contra a rota Next.js (proxy BFF→copilot)
  // ---------------------------------------------------------------------------
  const openStream = useCallback(
    (streamUrl: string) => {
      // Fecha qualquer stream anterior
      if (esRef.current) {
        esRef.current.close();
        esRef.current = null;
      }

      dispatch({ type: "BEGIN_STREAMING" });

      const es = new EventSource(streamUrl);
      esRef.current = es;

      const closeEs = () => {
        es.close();
        esRef.current = null;
      };

      es.addEventListener("token", (e) => {
        const ev = parseSseEvent(e.data);
        if (ev?.type === "token") {
          dispatch({ type: "TOKEN", text: ev.text });
        }
      });

      es.addEventListener("tool_call", (e) => {
        const ev = parseSseEvent(e.data);
        if (ev?.type === "tool_call") {
          dispatch({ type: "TOOL_CALL", event: ev });
        }
      });

      es.addEventListener("hitl_required", (e) => {
        const ev = parseSseEvent(e.data);
        if (ev?.type === "hitl_required") {
          // Para o stream — o usuário deve aprovar antes de continuar
          closeEs();
          // CA-4: busca as regras existentes do owner e roda a validação
          // anti-contradição REAL (FIX #17) antes de exibir o diff ao usuário.
          void resolveContradictionWarning(ev.diff, listExistingRules).then(
            (warning) => {
              dispatch({ type: "HITL_REQUIRED", event: ev, warning });
            }
          );
        }
      });

      es.addEventListener("done", (e) => {
        const ev = parseSseEvent(e.data);
        if (ev?.type === "done") {
          dispatch({ type: "DONE", event: ev });
        }
        closeEs();
      });

      es.addEventListener("error", (e) => {
        // Dois casos: erro de rede (e.data vazio) ou erro do copiloto (e.data com JSON)
        if (e instanceof MessageEvent && e.data) {
          const ev = parseSseEvent(e.data);
          if (ev?.type === "error") {
            dispatch({ type: "ERROR", message: ev.message });
            closeEs();
            return;
          }
        }
        // Erro de rede/conexão
        dispatch({ type: "ERROR", message: "Falha na conexão com o copiloto. Tente novamente." });
        closeEs();
      });
    },
    [listExistingRules]
  );

  // ---------------------------------------------------------------------------
  // startChat — inicia uma nova mensagem
  // ---------------------------------------------------------------------------
  const startChat = useCallback(
    async (message: string, sessionId?: string) => {
      pendingMessageRef.current = message;
      dispatch({ type: "ADD_USER_MESSAGE", content: message });

      let result: { sessionId: string; streamUrl: string; threadId: string };
      try {
        result = await chatMutation.mutateAsync({
          message,
          sessionId: sessionId ?? null,
          modelTier: null,
        });
      } catch (err) {
        const msg = err instanceof Error ? err.message : "Erro ao iniciar sessão com o copiloto.";
        dispatch({ type: "ERROR", message: msg });
        return;
      }

      dispatch({
        type: "START_CHAT",
        sessionId: result.sessionId,
        threadId: result.threadId,
      });

      openStream(result.streamUrl);
    },
    [chatMutation, openStream]
  );

  // ---------------------------------------------------------------------------
  // approve — aprova o HITL (mutation tRPC copilot.hitlApprove)
  // Nenhuma escrita ocorre sem esta chamada explícita (§2.4 / HITL obrigatório)
  //
  // RETOMADA SSE PÓS-HITL (contrato services/copilot/app/server.py):
  //   Após a aprovação, o re-stream usa /api/copilot/resume/:threadId, que faz
  //   proxy de POST /v1/chat/{thread_id}/resume no copiloto Python.
  //   NÃO reabre /api/copilot/stream/:sessionId com message:"" — isso violaria
  //   min_length=1 de ChatRequest e reiniciaria o grafo do START.
  // ---------------------------------------------------------------------------
  const approve = useCallback(
    async (threadId: string) => {
      dispatch({ type: "HITL_APPROVING" });
      try {
        await approveMutation.mutateAsync({ threadId, approved: true, reason: "" });
        dispatch({ type: "HITL_RESOLVED" });
        // Re-stream via endpoint de RETOMADA — nunca via /stream/:sessionId com body vazio.
        openStream(`/api/copilot/resume/${threadId}`);
      } catch (err) {
        const msg = err instanceof Error ? err.message : "Falha ao aprovar a operação.";
        dispatch({ type: "ERROR", message: msg });
      }
    },
    [approveMutation, openStream]
  );

  // ---------------------------------------------------------------------------
  // reject — rejeita o HITL
  //
  // RETOMADA SSE PÓS-REJEIÇÃO:
  //   Idem ao approve: usa /api/copilot/resume/:threadId para receber a resposta
  //   final do copiloto (mensagem de confirmação da rejeição), não /stream/:sessionId.
  // ---------------------------------------------------------------------------
  const reject = useCallback(
    async (threadId: string, reason: string) => {
      dispatch({ type: "HITL_REJECTING" });
      try {
        await rejectMutation.mutateAsync({ threadId, reason });
        dispatch({ type: "HITL_RESOLVED" });
        // Re-stream via endpoint de RETOMADA — nunca via /stream/:sessionId com body vazio.
        openStream(`/api/copilot/resume/${threadId}`);
      } catch (err) {
        const msg = err instanceof Error ? err.message : "Falha ao cancelar a operação.";
        dispatch({ type: "ERROR", message: msg });
      }
    },
    [rejectMutation, openStream]
  );

  const reset = useCallback(() => {
    if (esRef.current) {
      esRef.current.close();
      esRef.current = null;
    }
    dispatch({ type: "RESET" });
  }, []);

  return {
    state,
    startChat,
    approve,
    reject,
    reset,
    isStarting: chatMutation.isPending,
  };
}
