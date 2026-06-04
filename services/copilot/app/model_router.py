"""
model_router.py — Política de roteamento de modelos Claude (§2.4).

POLÍTICA DE ROTEAMENTO (3 tiers):

  Haiku 4.5  (claude-haiku-4-5-20251001)
    → inline/autocomplete: sugestões curtas de texto, completações de campo,
      verificações rápidas de sintaxe de regra.
    → Haiku-as-judge: avaliação de brand-safety, prompt-injection, claims.
      Haiku é usado como judge porque é rápido e barato; a decisão de escrita
      ainda passa pelo HITL humano de qualquer forma.
    → Tarefas de latência crítica onde o contexto é pequeno (< 8k tokens).

  Sonnet 4.6  (claude-sonnet-4-6, 1M ctx)
    → Padrão para raciocínio/tool-use: geração de sugestões de campanha,
      análise de segmentação, verbalizações de forecast, criação de criativos.
    → Contexto longo: catálogo de regras §4.6 + histórico de conversa cabe
      no 1M de contexto com prompt caching.
    → Toda chamada de ferramenta tipada (tool-use) padrão.

  Opus 4.8  (claude-opus-4-8)
    → Planejamento premium: análise complexa multi-campanha, estratégia de
      lançamento, reestruturação profunda de conta.
    → Usado SOMENTE sob requisição explícita do usuário ("análise detalhada",
      "estratégia completa") OU quando o Sonnet retornar resultado classificado
      como insuficiente pelo Haiku-as-judge em 2 tentativas.
    → Gating de custo: verificar orçamento do tenant antes de invocar Opus.

GATING DE CUSTO (§2.4):
  - Haiku: 1 unidade de custo
  - Sonnet: 10 unidades de custo
  - Opus: 50 unidades de custo
  O orçamento diário por tenant é verificado ANTES de cada chamada. Se excedido,
  degrada para o tier imediatamente abaixo (Opus→Sonnet→Haiku→erro explicativo).
  O rastreamento de uso é feito via Langfuse (tokens reais) com fallback em
  contador em memória (Redis em produção).

GATING DE REGRESSÃO DE QUALIDADE (§2.4):
  Antes de promover qualquer upgrade de modelo em produção, rodar o golden set
  de evals (observability/golden_set.py) e comparar métricas de qualidade
  (LLM-as-judge) E de custo/tokenização. Nenhuma promoção automática.
"""

from __future__ import annotations

import enum
from dataclasses import dataclass
from typing import Protocol

from app.config import CopilotSettings


class ModelTier(str, enum.Enum):
    """Tiers de modelo em ordem crescente de custo/capacidade."""
    HAIKU = "haiku"
    SONNET = "sonnet"
    OPUS = "opus"


# Custo relativo por tier (unidades abstratas para gating de orçamento)
TIER_COST_WEIGHT: dict[ModelTier, int] = {
    ModelTier.HAIKU: 1,
    ModelTier.SONNET: 10,
    ModelTier.OPUS: 50,
}


class TaskKind(str, enum.Enum):
    """Tipo de tarefa que determina o roteamento inicial de modelo."""
    # Haiku — inline/autocomplete
    INLINE_AUTOCOMPLETE = "inline_autocomplete"
    HAIKU_JUDGE = "haiku_judge"          # Haiku-as-judge: brand-safety, injection, claims

    # Sonnet — padrão
    TOOL_USE = "tool_use"                # qualquer chamada de ferramenta tipada
    FORECAST_VERBALIZATION = "forecast_verbalization"
    CREATIVE_SUGGESTION = "creative_suggestion"
    SEGMENTATION_ANALYSIS = "segmentation_analysis"
    RULE_SUGGESTION = "rule_suggestion"
    CAMPAIGN_EDIT = "campaign_edit"
    GENERAL_CHAT = "general_chat"

    # Opus — premium (explícito pelo usuário ou fallback após 2 falhas Sonnet)
    STRATEGIC_PLANNING = "strategic_planning"
    COMPLEX_ANALYSIS = "complex_analysis"


# Roteamento canônico task → tier (política declarativa)
TASK_ROUTING: dict[TaskKind, ModelTier] = {
    TaskKind.INLINE_AUTOCOMPLETE: ModelTier.HAIKU,
    TaskKind.HAIKU_JUDGE:         ModelTier.HAIKU,
    TaskKind.TOOL_USE:            ModelTier.SONNET,
    TaskKind.FORECAST_VERBALIZATION: ModelTier.SONNET,
    TaskKind.CREATIVE_SUGGESTION: ModelTier.SONNET,
    TaskKind.SEGMENTATION_ANALYSIS: ModelTier.SONNET,
    TaskKind.RULE_SUGGESTION:     ModelTier.SONNET,
    TaskKind.CAMPAIGN_EDIT:       ModelTier.SONNET,
    TaskKind.GENERAL_CHAT:        ModelTier.SONNET,
    TaskKind.STRATEGIC_PLANNING:  ModelTier.OPUS,
    TaskKind.COMPLEX_ANALYSIS:    ModelTier.OPUS,
}


class BudgetTracker(Protocol):
    """Interface de rastreamento de orçamento por tenant (implementar com Redis/Langfuse)."""

    async def get_daily_usage(self, tenant_id: str) -> dict[str, int]:
        """Retorna {'input_tokens': N, 'output_tokens': N} consumidos hoje pelo tenant."""
        ...

    async def record_usage(
        self,
        tenant_id: str,
        input_tokens: int,
        output_tokens: int,
        model_tier: ModelTier,
    ) -> None:
        """Registra consumo de tokens após a chamada."""
        ...


class InMemoryBudgetTracker:
    """
    Implementação in-memory do BudgetTracker para dev/CI.
    Em produção substituir por RedisBudgetTracker que persiste contadores TTL=dia.
    """

    def __init__(self) -> None:
        self._usage: dict[str, dict[str, int]] = {}

    async def get_daily_usage(self, tenant_id: str) -> dict[str, int]:
        return self._usage.get(tenant_id, {"input_tokens": 0, "output_tokens": 0})

    async def record_usage(
        self,
        tenant_id: str,
        input_tokens: int,
        output_tokens: int,
        model_tier: ModelTier,
    ) -> None:
        if tenant_id not in self._usage:
            self._usage[tenant_id] = {"input_tokens": 0, "output_tokens": 0}
        self._usage[tenant_id]["input_tokens"] += input_tokens
        self._usage[tenant_id]["output_tokens"] += output_tokens


@dataclass
class RoutingDecision:
    """Resultado do roteamento: modelo a usar + razão."""
    model_id: str
    tier: ModelTier
    reason: str
    degraded: bool = False  # True se houve downgrade por orçamento


class ModelRouter:
    """
    Roteia tarefas para o tier de modelo correto e aplica gating de custo.

    Regras invioláveis:
    - Nunca promove modelo sem gating de qualidade E de custo (§2.4).
    - Degrada graciosamente se o orçamento do tenant estiver esgotado.
    - Os IDs de modelo são lidos de CopilotSettings (não hardcodados aqui).
    """

    def __init__(self, settings: CopilotSettings, budget: BudgetTracker | None = None) -> None:
        self._settings = settings
        self._budget = budget or InMemoryBudgetTracker()
        self._model_ids: dict[ModelTier, str] = {
            ModelTier.HAIKU:  settings.model_haiku,
            ModelTier.SONNET: settings.model_sonnet,
            ModelTier.OPUS:   settings.model_opus,
        }

    def model_id_for_tier(self, tier: ModelTier) -> str:
        return self._model_ids[tier]

    async def route(
        self,
        task: TaskKind,
        tenant_id: str,
        force_tier: ModelTier | None = None,
    ) -> RoutingDecision:
        """
        Resolve o modelo para a task, aplicando gating de custo.

        Args:
            task: tipo de tarefa.
            tenant_id: identificador do tenant (injetado server-side — TX-3).
            force_tier: sobrescreve o roteamento canônico (ex.: usuário pediu Opus).
                        Ainda passa pelo gating de custo.
        """
        desired_tier = force_tier if force_tier is not None else TASK_ROUTING[task]
        usage = await self._budget.get_daily_usage(tenant_id)
        input_used = usage.get("input_tokens", 0)
        output_used = usage.get("output_tokens", 0)

        # Gating de custo: se excedeu orçamento, tenta downgrade em cascata
        tier = desired_tier
        degraded = False
        reason = f"roteamento canônico para {task.value}"

        input_limit = self._settings.budget_input_tokens_per_tenant_day
        output_limit = self._settings.budget_output_tokens_per_tenant_day

        if input_used >= input_limit or output_used >= output_limit:
            # Orçamento Opus esgotado → Sonnet
            if tier == ModelTier.OPUS:
                tier = ModelTier.SONNET
                degraded = True
                reason = "downgrade Opus→Sonnet: orçamento diário do tenant esgotado"
            # Orçamento Sonnet também esgotado → Haiku (somente leitura/sugestão curta)
            # Em produção pode-se bloquear completamente em vez de degradar.
            elif tier == ModelTier.SONNET:
                tier = ModelTier.HAIKU
                degraded = True
                reason = "downgrade Sonnet→Haiku: orçamento diário do tenant esgotado"

        return RoutingDecision(
            model_id=self._model_ids[tier],
            tier=tier,
            reason=reason,
            degraded=degraded,
        )

    async def record_usage(
        self,
        tenant_id: str,
        input_tokens: int,
        output_tokens: int,
        tier: ModelTier,
    ) -> None:
        """Registra uso após uma chamada bem-sucedida."""
        await self._budget.record_usage(tenant_id, input_tokens, output_tokens, tier)
