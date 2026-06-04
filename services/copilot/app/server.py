"""
app/server.py — FastAPI server do copiloto (gateway LangGraph).

CONTRATO DE API (para o frontend-bff-engineer):
  Ver api_contract.md no mesmo diretório para documentação completa.

ENDPOINTS:
  POST /v1/chat
    Body: {"message": str, "model_tier": "haiku"|"sonnet"|"opus"|null}
    Headers obrigatórios: X-Tenant-ID, X-Session-ID, X-Internal-Timestamp, X-Internal-Signature
    Resposta: SSE stream com eventos:
      - {"event": "token", "data": {"text": str}}
      - {"event": "tool_call", "data": {"tool": str, "status": "running"|"done", "result": {...}}}
      - {"event": "hitl_required", "data": {"diff": WriteDiff, "message": str}}
      - {"event": "done", "data": {"session_id": str, "usage": {...}}}
      - {"event": "error", "data": {"message": str}}

  POST /v1/hitl/{thread_id}/approve
    Body: {"approved": bool, "reason": str}
    Headers: mesmos de /v1/chat
    Resposta: {"status": "resumed", "thread_id": str}
    Efeito: retoma o grafo pausado no nó hitl_approval_node.

  POST /v1/hitl/{thread_id}/reject
    Body: {"reason": str}
    Headers: mesmos de /v1/chat
    Resposta: {"status": "rejected", "thread_id": str}

  GET /v1/session/{session_id}/state
    Headers: mesmos de /v1/chat
    Resposta: estado atual da sessão (sem dados sensíveis).
    Usado pelo BFF para recuperar sessões após reconexão.

  GET /health
    Resposta: {"status": "ok", "service": "copilot"}
    Sem autenticação — usado pelo K8s readiness probe.

SEGURANÇA (TX-3):
  - tenant_id SEMPRE extraído de X-Tenant-ID (header), NUNCA do body.
  - HMAC valida que a request vem do BFF autenticado.
  - O LLM nunca recebe credencial — só vê o que as ferramentas retornam.
  - Todas as respostas passam pelo guardrail_node (Haiku-as-judge).
  - OTel Collector redige PII antes de exportar (TX-5).

OBSERVABILIDADE (TX-5/§2.4):
  - Cada request cria um trace Langfuse (se habilitado).
  - Tokens consumidos rastreados por tenant para gating de custo.
  - PII redagido via OTel antes de exportar.
"""

from __future__ import annotations

import asyncio
import json
import uuid
from typing import Any, AsyncIterator

import structlog
from fastapi import FastAPI, HTTPException, status
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import StreamingResponse
from langchain_core.messages import HumanMessage
from langgraph.types import Command
from pydantic import BaseModel, Field

from app.auth import RequireSession
from app.config import get_settings
from app.model_router import ModelRouter, InMemoryBudgetTracker, ModelTier
from graph.builder import build_graph
from observability.langfuse_setup import get_langfuse_client
from tools.gateway import ToolGateway

log = structlog.get_logger(__name__)

# ---------------------------------------------------------------------------
# Inicialização
# ---------------------------------------------------------------------------

settings = get_settings()
budget_tracker = InMemoryBudgetTracker()
model_router = ModelRouter(settings, budget_tracker)
gateway = ToolGateway(settings)
graph = build_graph(settings, gateway, model_router)

app = FastAPI(
    title="AdServer Copilot Gateway",
    description="Gateway LangGraph do copiloto de IA (J5/Fase 2). Interno — nunca exposto ao front.",
    version="0.1.0",
    docs_url="/docs",
    redoc_url=None,
)

# CORS restrito: apenas o BFF interno pode chamar
app.add_middleware(
    CORSMiddleware,
    allow_origins=["http://bff:3001", "http://localhost:3001"],
    allow_methods=["GET", "POST"],
    allow_headers=["*"],
)


# ---------------------------------------------------------------------------
# Modelos de request/response
# ---------------------------------------------------------------------------

class ChatRequest(BaseModel):
    message: str = Field(min_length=1, max_length=10_000)
    model_tier: str | None = Field(
        default=None,
        description="Tier de modelo explícito: 'haiku', 'sonnet' ou 'opus'. "
                    "None = roteamento automático.",
    )


class HitlApproveRequest(BaseModel):
    approved: bool
    reason: str = Field(default="", max_length=500)


class HitlRejectRequest(BaseModel):
    reason: str = Field(max_length=500)


# ---------------------------------------------------------------------------
# Endpoints
# ---------------------------------------------------------------------------

@app.get("/health")
async def health() -> dict[str, str]:
    return {"status": "ok", "service": "copilot"}


@app.post("/v1/chat")
async def chat(
    body: ChatRequest,
    session: RequireSession,
) -> StreamingResponse:
    """
    Endpoint principal de chat com SSE streaming.

    O tenant_id vem de session.tenant_id (header X-Tenant-ID autenticado pelo BFF).
    NUNCA do body.

    O thread_id do checkpointing do LangGraph é derivado do X-Session-ID
    ou gerado se ausente. O BFF deve persistir o session_id para reconexão.
    """
    tenant_id = session.tenant_id
    thread_id = session.session_id or str(uuid.uuid4())

    log.info("chat.start", tenant_id=tenant_id, thread_id=thread_id)

    async def event_stream() -> AsyncIterator[str]:
        config = {
            "configurable": {
                "thread_id": thread_id,
            }
        }
        initial_state = {
            "tenant_id": tenant_id,
            "session_id": thread_id,
            "requested_model_tier": body.model_tier,
            "messages": [HumanMessage(content=body.message)],
            "pending_diff": None,
            "hitl_approved": None,
            "hitl_rejection_reason": None,
            "last_tool_result": None,
            "judge_result": None,
            "langfuse_trace_id": None,
            "input_tokens_used": 0,
            "output_tokens_used": 0,
            "next_action": None,
            "error_message": None,
        }

        try:
            # Inicia o trace Langfuse (se habilitado)
            lf = get_langfuse_client(settings)
            trace_id: str | None = None
            if lf:
                trace = lf.trace(
                    name="copilot_chat",
                    user_id=tenant_id,  # tenant como user (não PII — TX-5)
                    session_id=thread_id,
                    metadata={"model_tier": body.model_tier},
                )
                trace_id = trace.id
                initial_state["langfuse_trace_id"] = trace_id

            # Streaming do grafo LangGraph
            # O LangGraph emite eventos para cada nó executado
            async for event in graph.astream_events(initial_state, config, version="v2"):
                event_name: str = event.get("event", "")
                event_data: dict = event.get("data", {})

                # Token de texto do agente
                if event_name == "on_chat_model_stream":
                    chunk = event_data.get("chunk", {})
                    content = getattr(chunk, "content", "") or ""
                    if content:
                        yield _sse("token", {"text": content})

                # Ferramenta executada
                elif event_name == "on_tool_end":
                    yield _sse("tool_call", {
                        "tool": event_data.get("name", ""),
                        "status": "done",
                        "result": event_data.get("output", {}),
                    })

                # HITL necessário (grafo pausado)
                elif event_name == "on_interrupt":
                    interrupt_value = event_data.get("value", {})
                    if isinstance(interrupt_value, dict) and interrupt_value.get("event") == "hitl_required":
                        yield _sse("hitl_required", {
                            "thread_id": thread_id,
                            "diff": interrupt_value.get("diff", {}),
                            "message": interrupt_value.get("message", ""),
                        })
                        return  # Encerra o stream aqui; BFF retoma via /hitl endpoint

                # Erro no grafo
                elif event_name == "on_chain_error":
                    error = str(event_data.get("error", "Erro interno"))
                    yield _sse("error", {"message": error})
                    return

            # Fim do stream
            yield _sse("done", {
                "session_id": thread_id,
                "thread_id": thread_id,
                "usage": {
                    "input_tokens": initial_state.get("input_tokens_used", 0),
                    "output_tokens": initial_state.get("output_tokens_used", 0),
                },
            })

        except Exception as exc:
            log.error("chat.error", tenant_id=tenant_id, error=str(exc))
            yield _sse("error", {"message": "Erro interno no copiloto. Tente novamente."})

    return StreamingResponse(
        event_stream(),
        media_type="text/event-stream",
        headers={
            "Cache-Control": "no-cache",
            "X-Accel-Buffering": "no",
            "X-Thread-ID": thread_id,
        },
    )


@app.post("/v1/hitl/{thread_id}/approve")
async def hitl_approve(
    thread_id: str,
    body: HitlApproveRequest,
    session: RequireSession,
) -> dict[str, str]:
    """
    Aprova ou rejeita a escrita pendente de HITL.

    O BFF chama este endpoint após o usuário clicar em "Aplicar" ou "Cancelar"
    na UI. O grafo retoma a partir do nó hitl_approval_node com a decisão.

    INVARIANTE: o tenant_id da sessão deve coincidir com o thread_id.
    O BFF garante isso antes de chamar este endpoint.
    """
    tenant_id = session.tenant_id
    log.info(
        "hitl_approve",
        tenant_id=tenant_id,
        thread_id=thread_id,
        approved=body.approved,
    )

    config = {"configurable": {"thread_id": thread_id}}

    try:
        await graph.ainvoke(
            Command(resume={"approved": body.approved, "reason": body.reason}),
            config,
        )
        return {
            "status": "resumed",
            "thread_id": thread_id,
            "decision": "approved" if body.approved else "rejected",
        }
    except Exception as exc:
        log.error("hitl_approve.error", tenant_id=tenant_id, error=str(exc))
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Falha ao retomar o grafo: {exc!s}",
        )


@app.post("/v1/hitl/{thread_id}/reject")
async def hitl_reject(
    thread_id: str,
    body: HitlRejectRequest,
    session: RequireSession,
) -> dict[str, str]:
    """Rejeita a escrita pendente. Atalho semântico para approve com approved=False."""
    return await hitl_approve(
        thread_id,
        HitlApproveRequest(approved=False, reason=body.reason),
        session,
    )


@app.get("/v1/session/{session_id}/state")
async def get_session_state(
    session_id: str,
    session: RequireSession,
) -> dict[str, Any]:
    """
    Retorna o estado público da sessão (sem dados sensíveis).
    Usado pelo BFF para recuperar sessões após reconexão SSE.

    Filtra campos sensíveis do estado antes de retornar.
    """
    config = {"configurable": {"thread_id": session_id}}
    try:
        state = await graph.aget_state(config)
        if state is None:
            raise HTTPException(
                status_code=status.HTTP_404_NOT_FOUND,
                detail=f"Sessão '{session_id}' não encontrada.",
            )

        values = state.values if hasattr(state, "values") else {}

        # Filtra campos internos/sensíveis antes de expor
        safe_state = {
            "session_id": session_id,
            "tenant_id": session.tenant_id,  # usa o da sessão autenticada, não o do estado
            "pending_diff": values.get("pending_diff"),
            "hitl_approved": values.get("hitl_approved"),
            "next_action": values.get("next_action"),
            "input_tokens_used": values.get("input_tokens_used", 0),
            "output_tokens_used": values.get("output_tokens_used", 0),
            # NÃO expõe: judge_result, langfuse_trace_id, error_message completa
        }
        return safe_state

    except HTTPException:
        raise
    except Exception as exc:
        log.error("get_session_state.error", error=str(exc))
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail="Erro ao recuperar estado da sessão.",
        )


# ---------------------------------------------------------------------------
# Helper SSE
# ---------------------------------------------------------------------------

def _sse(event: str, data: dict[str, Any]) -> str:
    """Formata um evento SSE no padrão text/event-stream."""
    payload = json.dumps(data, ensure_ascii=False, default=str)
    return f"event: {event}\ndata: {payload}\n\n"
