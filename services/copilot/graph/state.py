"""
graph/state.py — Definição do estado do grafo LangGraph.

O estado persiste entre nós via checkpointing do LangGraph.
tenant_id é armazenado no estado do thread (injetado pelo BFF no início
da sessão) e NUNCA é sobrescrito pelo payload do LLM (TX-3).

INVARIANTE HITL: pending_diff != None significa que há uma escrita aguardando
aprovação humana. O grafo fica parado no nó hitl_approval_node até que:
  - O humano aprove (approved=True): o grafo avança para apply_write_node.
  - O humano rejeite (approved=False): o grafo avança para reject_write_node.
  - O timeout expire: hitl_approval_timeout_s → cancelamento automático.
"""

from __future__ import annotations

from typing import Annotated, Any
from typing_extensions import TypedDict
import operator

from langchain_core.messages import BaseMessage

from tools.schemas import WriteDiff, HaikuJudgeOutput


class CopilotState(TypedDict):
    """Estado completo do grafo do copiloto."""

    # -------------------------------------------------------------------------
    # Identidade da sessão (injetada pelo BFF — imutável durante a sessão)
    # -------------------------------------------------------------------------
    tenant_id: str
    session_id: str
    # Tier de modelo solicitado para esta sessão (None = usar roteamento padrão)
    requested_model_tier: str | None

    # -------------------------------------------------------------------------
    # Histórico de mensagens (acumulado com operador de append)
    # -------------------------------------------------------------------------
    messages: Annotated[list[BaseMessage], operator.add]

    # -------------------------------------------------------------------------
    # HITL — escrita pendente de aprovação humana
    # -------------------------------------------------------------------------
    # None = nenhuma escrita pendente; WriteDiff = aguardando aprovação
    pending_diff: WriteDiff | None
    # Preenchido pelo nó de aprovação HITL
    hitl_approved: bool | None          # None = ainda não decidido
    hitl_rejection_reason: str | None   # Preenchido se rejeitado

    # -------------------------------------------------------------------------
    # Resultado da última ferramenta chamada (read-only ou write draft)
    # -------------------------------------------------------------------------
    last_tool_result: dict[str, Any] | None

    # -------------------------------------------------------------------------
    # Guardrail — resultado do Haiku-as-judge
    # -------------------------------------------------------------------------
    judge_result: HaikuJudgeOutput | None

    # -------------------------------------------------------------------------
    # Metadados de rastreamento (Langfuse)
    # -------------------------------------------------------------------------
    langfuse_trace_id: str | None
    # Uso de tokens acumulado nesta sessão (para gating de custo)
    input_tokens_used: int
    output_tokens_used: int

    # -------------------------------------------------------------------------
    # Rota de controle do grafo
    # -------------------------------------------------------------------------
    # "respond" | "call_tool" | "await_hitl" | "apply_write" | "error"
    next_action: str | None
    error_message: str | None
