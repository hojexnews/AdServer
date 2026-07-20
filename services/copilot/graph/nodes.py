"""
graph/nodes.py — Nós do grafo LangGraph do copiloto.

ESTRUTURA DO GRAFO:
  START
    └─► agent_node (LLM call — Sonnet/Opus/Haiku por TaskKind)
          ├─► (tool_calls? não) → guardrail_node → END (stream ao BFF)
          └─► (tool_calls? sim)
                ├─► (ferramenta READ-ONLY) → tool_execute_node → agent_node
                └─► (ferramenta WRITE) → write_draft_node
                                            └─► hitl_approval_node ←── [BFF confirma]
                                                  ├─► (aprovado) → apply_write_node → guardrail_node → END
                                                  └─► (rejeitado) → reject_write_node → guardrail_node → END

HITL OBRIGATÓRIO:
  Todo nó de escrita para no hitl_approval_node com interrupt().
  O BFF recebe o WriteDiff via SSE, exibe o diff ao usuário, aguarda aprovação,
  e chama POST /hitl/{thread_id}/approve ou /hitl/{thread_id}/reject.
  O grafo só continua após a chamada do humano — não há escrita autônoma.

AUTORIZAÇÃO SERVER-SIDE (TX-3):
  tenant_id lido de state["tenant_id"] em TODOS os nós.
  Nunca de state["messages"] (payload do LLM).
  O agente não tem acesso a credenciais — chama apenas o ToolGateway.
"""

from __future__ import annotations

import json
from typing import Any

import structlog
from langchain_core.messages import AIMessage, ToolMessage, SystemMessage
from langgraph.types import interrupt

from graph.state import CopilotState
from tools.gateway import ToolGateway
from tools.schemas import (
    WriteDiff,
    SimulateForecastInput,
    ValidateSegmentationInput,
    ValidateCreativeInput,
    SearchSimilarCreativesInput,
    SearchHelpDocsInput,
    CreateCampaignDraftInput,
    UpdateCampaignDraftInput,
    CreateBannerDraftInput,
    UpdateBannerDraftInput,
    CreateDeliveryRuleDraftInput,
    CreateCapDraftInput,
    LinkCampaignZoneDraftInput,
    HaikuJudgeInput,
    CreativeType,
)
from app.model_router import ModelRouter, TaskKind, ModelTier

log = structlog.get_logger(__name__)

# Ferramentas de ESCRITA — exigem HITL antes de aplicar
WRITE_TOOL_NAMES = frozenset({
    "create_campaign_draft",
    "update_campaign_draft",
    "create_banner_draft",
    "update_banner_draft",
    "create_delivery_rule_draft",
    "create_cap_draft",
    "link_campaign_zone_draft",
})

# Ferramentas de LEITURA — não param no grafo
READ_TOOL_NAMES = frozenset({
    "simulate_forecast",
    "validate_segmentation",
    "validate_creative",
    "search_similar_creatives",
    "search_help_docs",
})


def make_agent_node(
    anthropic_client: Any,
    model_router: ModelRouter,
    system_prompt: str,
    tools_spec: list[dict],
):
    """
    Fábrica do nó do agente.
    Retorna uma função de nó compatível com LangGraph.
    """

    async def agent_node(state: CopilotState) -> dict[str, Any]:
        """
        Nó principal do agente: chama o LLM com as ferramentas disponíveis.
        O LLM decide qual ferramenta chamar (ou responder diretamente).

        PROMPT DO SISTEMA:
          - Injetado server-side; o LLM não pode modificá-lo.
          - Inclui: identidade do copiloto, restrições (sem credenciais, sem
            escrita autônoma), catálogo de regras §4.6 com prompt caching.
          - NÃO inclui: tenant_id (o LLM não precisa saber; injetado nas ferramentas).

        IMPORTANT: o tenant_id é lido de state["tenant_id"], nunca das mensagens.
        """
        tenant_id: str = state["tenant_id"]
        session_id: str = state["session_id"]

        log.info("agent_node.start", tenant_id=tenant_id, session_id=session_id)

        # Determina TaskKind a partir da última mensagem do usuário
        task = _infer_task_kind(state["messages"])

        # Roteamento de modelo (aplica gating de custo)
        force_tier: ModelTier | None = None
        if state.get("requested_model_tier") == "opus":
            force_tier = ModelTier.OPUS

        routing = await model_router.route(task, tenant_id, force_tier=force_tier)

        if routing.degraded:
            log.warning(
                "agent_node.model_degraded",
                tenant_id=tenant_id,
                reason=routing.reason,
                tier=routing.tier,
            )

        # Monta mensagens com system prompt (prompt caching ativo)
        all_messages = [
            SystemMessage(content=system_prompt),
            *state["messages"],
        ]

        # Chamada ao LLM via Anthropic SDK
        # Em produção: usar langchain_anthropic.ChatAnthropic com as tools spec
        # Aqui fazemos a chamada de forma compatível com LangGraph
        try:
            # Stub de chamada — em produção:
            # response = await anthropic_client.messages.create(
            #     model=routing.model_id,
            #     messages=_to_anthropic_format(all_messages),
            #     tools=tools_spec,
            #     max_tokens=4096,
            # )
            # Para fins de estrutura do grafo, simulamos uma resposta sem tool_call
            ai_message = AIMessage(
                content="[Resposta do agente — integração Anthropic pendente de ANTHROPIC_API_KEY]",
                additional_kwargs={
                    "model": routing.model_id,
                    "routing_reason": routing.reason,
                },
            )
            # Rastreamento de uso (stub — em produção usar tokens reais da resposta)
            input_tokens_used = state.get("input_tokens_used", 0) + 100  # estimativa stub
            output_tokens_used = state.get("output_tokens_used", 0) + 50

            await model_router.record_usage(
                tenant_id,
                input_tokens=100,
                output_tokens=50,
                tier=routing.tier,
            )
        except Exception as exc:
            log.error("agent_node.llm_error", tenant_id=tenant_id, error=str(exc))
            return {
                "messages": [],
                "next_action": "error",
                "error_message": f"Erro na chamada ao modelo: {exc!s}",
            }

        # Decide próxima ação baseada na resposta
        has_tool_calls = bool(
            ai_message.additional_kwargs.get("tool_use")
            or getattr(ai_message, "tool_calls", None)
        )

        next_action = "call_tool" if has_tool_calls else "respond"

        log.info(
            "agent_node.done",
            tenant_id=tenant_id,
            model=routing.model_id,
            next_action=next_action,
        )

        return {
            "messages": [ai_message],
            "next_action": next_action,
            "input_tokens_used": input_tokens_used,
            "output_tokens_used": output_tokens_used,
        }

    return agent_node


def make_tool_execute_node(gateway: ToolGateway):
    """Nó que executa ferramentas READ-ONLY."""

    async def tool_execute_node(state: CopilotState) -> dict[str, Any]:
        """
        Executa ferramentas read-only. NÃO chama ferramentas de escrita (essas
        vão para write_draft_node).

        tenant_id injetado de state — nunca do payload da tool call.
        """
        tenant_id = state["tenant_id"]
        last_ai_msg = state["messages"][-1] if state["messages"] else None

        if not last_ai_msg:
            return {"next_action": "respond", "last_tool_result": None}

        # Em produção: extrair tool_calls da última AIMessage e executar
        # For now: stub
        tool_calls = getattr(last_ai_msg, "tool_calls", []) or []

        if not tool_calls:
            return {"next_action": "respond", "last_tool_result": None}

        results = []
        pending_diff: WriteDiff | None = None

        for tc in tool_calls:
            tool_name: str = tc.get("name", "") if isinstance(tc, dict) else tc.name
            tool_input: dict = tc.get("input", {}) if isinstance(tc, dict) else tc.args

            log.info("tool_execute_node.call", tenant_id=tenant_id, tool=tool_name)

            if tool_name in WRITE_TOOL_NAMES:
                # Rota para write_draft_node via flag — não executa aqui
                return {
                    "next_action": "call_write_tool",
                    "last_tool_result": {"write_tool": tool_name, "input": tool_input},
                }

            if tool_name == "simulate_forecast":
                inp = SimulateForecastInput(**tool_input)
                out = await gateway.simulate_forecast(tenant_id, inp)
                results.append({"tool": tool_name, "result": out.model_dump()})

            elif tool_name == "validate_segmentation":
                inp = ValidateSegmentationInput(**tool_input)
                out = await gateway.validate_segmentation(tenant_id, inp)
                results.append({"tool": tool_name, "result": out.model_dump()})

            elif tool_name == "validate_creative":
                inp = ValidateCreativeInput(**tool_input)
                out = await gateway.validate_creative(tenant_id, inp)
                results.append({"tool": tool_name, "result": out.model_dump()})

            elif tool_name == "search_similar_creatives":
                inp = SearchSimilarCreativesInput(**tool_input)
                out = await gateway.search_similar_creatives(tenant_id, inp)
                results.append({"tool": tool_name, "result": out.model_dump()})

            elif tool_name == "search_help_docs":
                inp = SearchHelpDocsInput(**tool_input)
                out = await gateway.search_help_docs(tenant_id, inp)
                results.append({"tool": tool_name, "result": out.model_dump()})

            else:
                results.append({"tool": tool_name, "result": {"error": f"Tool '{tool_name}' não reconhecida."}})

        # Adiciona resultados como ToolMessages no histórico
        tool_messages = [
            ToolMessage(
                content=json.dumps(r["result"], ensure_ascii=False),
                tool_call_id=str(i),
            )
            for i, r in enumerate(results)
        ]

        return {
            "messages": tool_messages,
            "next_action": "agent",   # volta ao agente para sintetizar
            "last_tool_result": results[-1]["result"] if results else None,
        }

    return tool_execute_node


def make_write_draft_node(gateway: ToolGateway):
    """
    Nó que prepara o WriteDiff para escrita.
    NÃO persiste — apenas cria o draft e aciona HITL.

    SEGURANÇA (M2):
      Para operações de banner (create/update), roda validate_creative ANTES
      de gerar o WriteDiff. O resultado da validação é anexado ao diff.
      Se gate_passed=False, o diff ainda é gerado mas marcado como inválido
      (permite ao humano ver o problema no HITL antes de bloquear).
    """

    async def _run_creative_validation(
        tenant_id: str,
        tool_name: str,
        tool_input: dict,
    ) -> dict | None:
        """
        Executa validate_creative para operações de banner.
        Retorna o modelo dump do ValidateCreativeOutput ou None se não aplicável.
        """
        banner_ops = {"create_banner_draft", "update_banner_draft"}
        if tool_name not in banner_ops:
            return None

        # Extrai campos relevantes do input para validação criativa
        creative_type_str = tool_input.get("creative_type", "image")
        try:
            creative_type = CreativeType(creative_type_str)
        except ValueError:
            creative_type = CreativeType.IMAGE

        val_inp = ValidateCreativeInput(
            creative_type=creative_type,
            asset_url=tool_input.get("asset_url"),
            html_content=tool_input.get("html_content"),
            is_ai_generated=tool_input.get("is_ai_generated", False),
            ai_generation_tool=tool_input.get("ai_generation_tool"),
            dest_url=tool_input.get("dest_url"),
        )
        val_result = await gateway.validate_creative(tenant_id, val_inp)
        return val_result.model_dump()

    async def write_draft_node(state: CopilotState) -> dict[str, Any]:
        """
        Processa tool_calls de escrita: cria draft, valida schema Pydantic,
        e coloca o WriteDiff em state["pending_diff"] para o HITL.

        M2: validate_creative roda ANTES de gerar WriteDiff para banners.
        O validation_result é anexado ao diff para visibilidade no HITL.
        """
        tenant_id = state["tenant_id"]
        last_tool_info = state.get("last_tool_result", {}) or {}
        tool_name = last_tool_info.get("write_tool", "")
        tool_input = last_tool_info.get("input", {})

        log.info("write_draft_node.start", tenant_id=tenant_id, tool=tool_name)

        try:
            # M2: valida criativo ANTES de criar o draft (para banner ops)
            creative_validation = await _run_creative_validation(
                tenant_id, tool_name, tool_input
            )

            if tool_name == "create_campaign_draft":
                inp = CreateCampaignDraftInput(**tool_input)
                diff = await gateway.create_campaign_draft(tenant_id, inp)

            elif tool_name == "update_campaign_draft":
                inp = UpdateCampaignDraftInput(**tool_input)
                diff = await gateway.update_campaign_draft(tenant_id, inp)

            elif tool_name == "create_banner_draft":
                inp = CreateBannerDraftInput(**tool_input)
                diff = await gateway.create_banner_draft(tenant_id, inp)

            elif tool_name == "update_banner_draft":
                inp = UpdateBannerDraftInput(**tool_input)
                diff = await gateway.update_banner_draft(tenant_id, inp)

            elif tool_name == "create_delivery_rule_draft":
                inp = CreateDeliveryRuleDraftInput(**tool_input)
                diff = await gateway.create_delivery_rule_draft(tenant_id, inp)

            elif tool_name == "create_cap_draft":
                inp = CreateCapDraftInput(**tool_input)
                diff = await gateway.create_cap_draft(tenant_id, inp)

            elif tool_name == "link_campaign_zone_draft":
                inp = LinkCampaignZoneDraftInput(**tool_input)
                diff = await gateway.link_campaign_zone_draft(tenant_id, inp)

            else:
                raise ValueError(f"Ferramenta de escrita desconhecida: '{tool_name}'")

            # M2: anexa validation_result ao diff se for operação de banner
            if creative_validation is not None:
                # Preserva validation_result existente (ex.: segmentation) ou substitui
                diff = diff.model_copy(update={"validation_result": creative_validation})
                gate_passed = creative_validation.get("gate_passed", True)
                if not gate_passed:
                    log.warning(
                        "write_draft_node.creative_gate_failed",
                        tenant_id=tenant_id,
                        tool=tool_name,
                        violations=creative_validation.get("violations", []),
                    )
                    # Ainda emite o HITL mas marcado como inválido (humano decide)

        except Exception as exc:
            log.error("write_draft_node.validation_error", tenant_id=tenant_id, error=str(exc))
            return {
                "next_action": "error",
                "error_message": "Validação do draft falhou. Verifique os dados informados.",
                "pending_diff": None,
            }

        log.info(
            "write_draft_node.draft_ready",
            tenant_id=tenant_id,
            operation=diff.operation,
        )

        return {
            "pending_diff": diff,
            "hitl_approved": None,
            "next_action": "await_hitl",
        }

    return write_draft_node


def make_hitl_approval_node():
    """
    Nó HITL — interrompe o grafo e aguarda aprovação humana.

    LangGraph interrupt() pausa a execução. O BFF recebe o WriteDiff
    via SSE event 'hitl_required', exibe o diff ao usuário, e chama
    POST /hitl/{thread_id}/approve com {"approved": bool, "reason": str}.
    O grafo retoma quando o BFF faz `graph.invoke(command, config)`.
    """

    async def hitl_approval_node(state: CopilotState) -> dict[str, Any]:
        diff = state.get("pending_diff")
        if diff is None:
            log.error("hitl_approval_node: pending_diff é None — estado inconsistente")
            return {"next_action": "error", "error_message": "HITL sem diff pendente."}

        log.info(
            "hitl_approval_node.waiting",
            tenant_id=state["tenant_id"],
            operation=diff.operation,
        )

        # interrupt() pausa o grafo aqui. O valor passado é enviado ao BFF.
        # O BFF envia um SSE event com este payload para a UI.
        human_decision = interrupt({
            "event": "hitl_required",
            "diff": diff.model_dump(),
            "message": (
                f"Aprovação necessária para '{diff.operation}' em {diff.entity_type}. "
                f"Revise o diff e confirme para aplicar."
            ),
        })

        # Quando o grafo retoma, human_decision contém {"approved": bool, "reason": str}
        approved: bool = human_decision.get("approved", False)
        reason: str = human_decision.get("reason", "")

        log.info(
            "hitl_approval_node.decision",
            tenant_id=state["tenant_id"],
            approved=approved,
            reason=reason[:100] if reason else None,
        )

        return {
            "hitl_approved": approved,
            "hitl_rejection_reason": reason if not approved else None,
            "next_action": "apply_write" if approved else "reject_write",
        }

    return hitl_approval_node


# Achado remediação copilot-honestidade #2 (30ª onda): allowlist ESTREITA e
# EXPLÍCITA de `entity_type` para os quais um WriteDiff sem
# `validation_result` é um caminho LEGÍTIMO — não um buraco fail-open geral.
#
# Critério: a entidade não carrega conteúdo de criativo (C2PA/SynthID/
# disclosure/PII — EU AI Act Art. 50, ver `validate_creative`) nem regras de
# entrega sujeitas a anti-contradição (§4.6/CA-4, ver `validate_segmentation`).
#   - "campaign": metadados de campanha (nome/orçamento/datas) — sem asset.
#   - "cap": limite numérico de frequência/orçamento — sem texto publicável.
#   - "campaign_zone": vínculo campanha↔zona (só IDs) — sem texto publicável.
#
# "banner" (contém asset_url/dest_url/creative_type — é o próprio criativo)
# e "delivery_rule" (regras §4.6) NÃO estão aqui: um WriteDiff desses tipos
# sem validation_result é bloqueado (fail-closed), nunca silenciosamente
# aplicado. Isto é intencional — hoje `create_banner_draft` ainda não invoca
# `validate_creative` (dispatch pendente de G1); até que invoque, escritas de
# banner via copiloto ficam corretamente bloqueadas em vez de vazar o gate de
# proveniência.
_ENTITY_TYPES_EXEMPT_FROM_VALIDATION_GATE: frozenset[str] = frozenset({
    "campaign",
    "cap",
    "campaign_zone",
})


def make_apply_write_node(gateway: ToolGateway):
    """
    Nó que aplica o WriteDiff APROVADO pelo humano.
    É o único ponto onde há mutação real no banco.

    SEGURANÇA (C1):
      Verifica que o tenant_id do estado (dono do diff) coincide com o
      tenant_id da sessão atual antes de aplicar. Se não coincidirem,
      retorna erro (proteção dupla — a primeira está no endpoint HITL).

    SEGURANÇA (Achado #18 — gate de proveniência/anti-contradição é GATE,
    não opção, §6/§7 do mandato + CA-4):
      diff.validation_result (proveniência C2PA/SynthID/PII de validate_creative,
      ou anti-contradição §4.6/CA-4 de validate_segmentation) é verificado
      ANTES de qualquer chamada a gateway.apply_write. Se gate_passed=False
      ou is_valid=False, a escrita é RECUSADA — nunca persiste, mesmo que o
      humano tenha clicado em aprovar no HITL (a UI deve impedir aprovação de
      diffs reprovados, mas o backend não pode confiar só nisso: fail-closed
      em profundidade). Nenhuma exceção — retorna next_action='error'.

    SEGURANÇA (Achado remediação copilot-honestidade #2, 30ª onda —
    FAIL-CLOSED quando validation_result é None):
      Um WriteDiff SEM validation_result (None) só é aplicado se
      diff.entity_type estiver na allowlist estreita e explícita
      `_ENTITY_TYPES_EXEMPT_FROM_VALIDATION_GATE` (entidades que
      estruturalmente não carregam criativo nem regra de entrega). Para
      qualquer outro entity_type (ex.: "banner"), a AUSÊNCIA de
      validation_result é tratada como reprovação — o gate de conformidade
      do EU AI Act Art. 50 (vigor 02/08/2026) não pode ser contornado por
      omissão. Antes desta remediação, validation_result=None pulava o gate
      inteiro para QUALQUER entity_type — um WriteDiff de banner sem
      validação alguma persistia sem checagem nenhuma.
    """

    async def apply_write_node(state: CopilotState) -> dict[str, Any]:
        tenant_id = state["tenant_id"]
        diff = state.get("pending_diff")

        if diff is None or not state.get("hitl_approved"):
            log.error("apply_write_node: estado inválido", tenant_id=tenant_id)
            return {"next_action": "error", "error_message": "apply_write sem aprovação HITL."}

        # C1: verifica que o tenant do diff coincide com o tenant do estado
        # (defesa em profundidade — o endpoint HITL já fez esta verificação)
        diff_tenant = (diff.after or {}).get("tenant_id") if diff.after else None
        if diff_tenant is not None and diff_tenant != tenant_id:
            log.error(
                "apply_write_node.tenant_mismatch",
                state_tenant_id=tenant_id,
                diff_tenant_id=diff_tenant,
                operation=diff.operation,
            )
            return {
                "next_action": "error",
                "error_message": "apply_write: divergência de tenant entre diff e estado.",
                "pending_diff": None,
            }

        # Achado #18: gate de proveniência (C2PA/SynthID/PII) e anti-contradição
        # (CA-4) NÃO são informativos — bloqueiam a persistência. Um diff cujo
        # validation_result reprovou (gate_passed=False e/ou is_valid=False)
        # NUNCA chega a gateway.apply_write, independentemente de hitl_approved.
        validation = diff.validation_result
        if validation is None:
            # Achado remediação copilot-honestidade #2: FAIL-CLOSED por padrão.
            # Só é legítimo pular o gate se entity_type estiver na allowlist
            # estreita e explícita (entidades sem criativo/regra de entrega).
            if diff.entity_type not in _ENTITY_TYPES_EXEMPT_FROM_VALIDATION_GATE:
                log.error(
                    "apply_write_node.validation_result_missing",
                    tenant_id=tenant_id,
                    operation=diff.operation,
                    entity_type=diff.entity_type,
                )
                return {
                    "next_action": "error",
                    "error_message": (
                        "Escrita bloqueada: nenhum resultado de validação de "
                        "proveniência (C2PA/SynthID/PII) ou anti-contradição "
                        "(§4.6/CA-4) foi anexado a este diff. Gere um novo "
                        "draft com validação antes de aplicar."
                    ),
                    "pending_diff": None,
                    "hitl_approved": None,
                }
        else:
            gate_passed = validation.get("gate_passed", True)
            is_valid = validation.get("is_valid", True)
            if gate_passed is False or is_valid is False:
                violations = (
                    validation.get("violations")
                    or [c.get("description", str(c)) for c in validation.get("conflicts", [])]
                    or ["Validação reprovada (proveniência/PII/anti-contradição CA-4)."]
                )
                log.error(
                    "apply_write_node.validation_gate_blocked",
                    tenant_id=tenant_id,
                    operation=diff.operation,
                    gate_passed=gate_passed,
                    is_valid=is_valid,
                    violations=violations,
                )
                return {
                    "next_action": "error",
                    "error_message": (
                        "Escrita bloqueada: validação de proveniência (C2PA/SynthID/PII) "
                        "ou anti-contradição (§4.6/CA-4) reprovou este diff. "
                        "Corrija as violações e gere um novo draft antes de aplicar."
                    ),
                    "pending_diff": None,
                    "hitl_approved": None,
                }

        log.info(
            "apply_write_node.start",
            tenant_id=tenant_id,
            operation=diff.operation,
        )

        try:
            result = await gateway.apply_write(tenant_id, diff)
            # C1/L2: não incluir tenant_id no resultado retornado ao LLM
            safe_result = {k: v for k, v in result.items() if k != "tenant_id"}
            # Achado remediação copilot-honestidade #3 (30ª onda): o status
            # relatado ao LLM/humano é o que gateway.apply_write REALMENTE
            # retornou (ex.: "pending_dispatch" enquanto o dispatch por
            # operação não existir) — nunca um "applied" hardcoded que
            # afirmaria uma persistência que pode não ter ocorrido.
            tool_msg = ToolMessage(
                content=json.dumps({
                    "status": safe_result.get("status", "unknown"),
                    "operation": diff.operation,
                    "result": safe_result,
                }, ensure_ascii=False),
                tool_call_id="apply_write",
            )
            return {
                "messages": [tool_msg],
                "pending_diff": None,
                "hitl_approved": None,
                "last_tool_result": safe_result,
                "next_action": "respond",
            }
        except Exception as exc:
            log.error("apply_write_node.error", tenant_id=tenant_id, error=str(exc))
            return {
                "next_action": "error",
                "error_message": "Falha ao aplicar escrita. Tente novamente.",
                "pending_diff": None,
            }

    return apply_write_node


def make_reject_write_node():
    """Nó que processa a rejeição do HITL."""

    async def reject_write_node(state: CopilotState) -> dict[str, Any]:
        diff = state.get("pending_diff")
        reason = state.get("hitl_rejection_reason", "Não informado")
        tenant_id = state["tenant_id"]

        log.info(
            "reject_write_node",
            tenant_id=tenant_id,
            operation=diff.operation if diff else "unknown",
            reason=reason,
        )

        tool_msg = ToolMessage(
            content=json.dumps({
                "status": "rejected",
                "reason": reason,
                "operation": diff.operation if diff else None,
            }, ensure_ascii=False),
            tool_call_id="reject_write",
        )

        return {
            "messages": [tool_msg],
            "pending_diff": None,
            "hitl_approved": None,
            "hitl_rejection_reason": None,
            "next_action": "respond",
        }

    return reject_write_node


def make_guardrail_node(gateway: ToolGateway):
    """
    Nó de guardrail — Haiku-as-judge.
    Avalia a última resposta do LLM antes de enviar ao usuário.
    Se detectar violação, substitui a resposta por uma mensagem de erro genérica
    e loga o incidente (sem expor detalhes da violação ao usuário).
    """

    async def guardrail_node(state: CopilotState) -> dict[str, Any]:
        tenant_id = state["tenant_id"]
        last_msg = state["messages"][-1] if state["messages"] else None

        if last_msg is None or not isinstance(last_msg, AIMessage):
            return {"judge_result": None}

        llm_output = last_msg.content if isinstance(last_msg.content, str) else ""
        inp = HaikuJudgeInput(llm_output=llm_output)

        judge = await gateway.haiku_judge(tenant_id, inp)

        if not judge.is_safe:
            log.warning(
                "guardrail_node.unsafe_output",
                tenant_id=tenant_id,
                violations=[v.value for v in judge.violations],
                confidence=judge.confidence,
            )
            # Substitui a mensagem insegura por uma genérica
            safe_msg = AIMessage(
                content=(
                    "Não foi possível processar esta solicitação. "
                    "Por favor, reformule sua pergunta."
                )
            )
            return {
                "messages": [safe_msg],  # substitui a última mensagem
                "judge_result": judge,
                "next_action": "end",
            }

        return {"judge_result": judge, "next_action": "end"}

    return guardrail_node


# ---------------------------------------------------------------------------
# Roteador condicional do grafo
# ---------------------------------------------------------------------------

def route_after_agent(state: CopilotState) -> str:
    """Decide qual nó chamar após o agente."""
    action = state.get("next_action", "respond")
    if action == "call_tool":
        return "tool_execute"
    if action == "call_write_tool":
        return "write_draft"
    if action == "respond":
        return "guardrail"
    return "guardrail"


def route_after_tool(state: CopilotState) -> str:
    """Decide qual nó chamar após execução de ferramenta."""
    action = state.get("next_action", "agent")
    if action == "agent":
        return "agent"
    if action == "call_write_tool":
        return "write_draft"
    return "guardrail"


def route_after_hitl(state: CopilotState) -> str:
    """Decide qual nó chamar após o HITL."""
    action = state.get("next_action", "reject_write")
    if action == "apply_write":
        return "apply_write"
    return "reject_write"


def route_after_write(state: CopilotState) -> str:
    """Decide qual nó chamar após aplicar ou rejeitar escrita."""
    return "agent"  # volta ao agente para sintetizar resposta


# ---------------------------------------------------------------------------
# Helper: inferir TaskKind da conversa
# ---------------------------------------------------------------------------

def _infer_task_kind(messages: list[Any]) -> TaskKind:
    """
    Infere o TaskKind a partir da última mensagem do usuário.
    Lógica simples de palavra-chave — em produção pode usar classificador leve.
    """
    from langchain_core.messages import HumanMessage

    last_human = next(
        (m for m in reversed(messages) if isinstance(m, HumanMessage)),
        None,
    )
    if last_human is None:
        return TaskKind.GENERAL_CHAT

    text = (
        last_human.content.lower()
        if isinstance(last_human.content, str)
        else ""
    )

    strategic_keywords = ["estratégia", "strategy", "plano completo", "análise detalhada", "reestrutura"]
    for kw in strategic_keywords:
        if kw in text:
            return TaskKind.STRATEGIC_PLANNING

    forecast_keywords = ["forecast", "estimativa", "projeção", "quantos", "how many"]
    for kw in forecast_keywords:
        if kw in text:
            return TaskKind.FORECAST_VERBALIZATION

    creative_keywords = ["criativo", "banner", "creative", "imagem", "html"]
    for kw in creative_keywords:
        if kw in text:
            return TaskKind.CREATIVE_SUGGESTION

    rule_keywords = ["regra", "rule", "segmentação", "segmentation", "targeting"]
    for kw in rule_keywords:
        if kw in text:
            return TaskKind.RULE_SUGGESTION

    campaign_keywords = ["campanha", "campaign", "criar", "editar", "atualizar", "criar campanha"]
    for kw in campaign_keywords:
        if kw in text:
            return TaskKind.CAMPAIGN_EDIT

    return TaskKind.GENERAL_CHAT
