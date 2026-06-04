"""
tests/test_model_router.py — Testes da política de roteamento de modelos (verificável sem credencial).

Verifica:
  - Roteamento canônico task→tier
  - Gating de custo: downgrade quando orçamento esgotado
  - IDs de modelo corretos (§2.4)
  - Não promove Opus quando budget está baixo
"""

from __future__ import annotations

import pytest

from app.model_router import (
    ModelRouter,
    ModelTier,
    TaskKind,
    InMemoryBudgetTracker,
)
from app.config import CopilotSettings
from unittest.mock import MagicMock


def make_settings(**overrides) -> CopilotSettings:
    """Cria settings mínimos para teste sem precisar de .env."""
    defaults = {
        "anthropic_api_key": "sk-test-fake-key",
        "database_url": "postgresql+asyncpg://test:test@localhost/test",
        "copilot_internal_secret": "test-secret-for-hmac-not-real",
        "langfuse_enabled": False,
    }
    defaults.update(overrides)
    return CopilotSettings(**defaults)  # type: ignore[call-arg]


@pytest.mark.asyncio
class TestModelRouterCanonical:
    async def test_haiku_tasks(self) -> None:
        settings = make_settings()
        router = ModelRouter(settings)

        for task in [TaskKind.INLINE_AUTOCOMPLETE, TaskKind.HAIKU_JUDGE]:
            decision = await router.route(task, "tenant-test")
            assert decision.tier == ModelTier.HAIKU, f"Task {task} deve ser Haiku"
            assert decision.model_id == settings.model_haiku

    async def test_sonnet_tasks(self) -> None:
        settings = make_settings()
        router = ModelRouter(settings)

        sonnet_tasks = [
            TaskKind.TOOL_USE,
            TaskKind.FORECAST_VERBALIZATION,
            TaskKind.CREATIVE_SUGGESTION,
            TaskKind.SEGMENTATION_ANALYSIS,
            TaskKind.RULE_SUGGESTION,
            TaskKind.CAMPAIGN_EDIT,
            TaskKind.GENERAL_CHAT,
        ]
        for task in sonnet_tasks:
            decision = await router.route(task, "tenant-test")
            assert decision.tier == ModelTier.SONNET, f"Task {task} deve ser Sonnet"
            assert decision.model_id == settings.model_sonnet

    async def test_opus_tasks(self) -> None:
        settings = make_settings()
        router = ModelRouter(settings)

        for task in [TaskKind.STRATEGIC_PLANNING, TaskKind.COMPLEX_ANALYSIS]:
            decision = await router.route(task, "tenant-test")
            assert decision.tier == ModelTier.OPUS, f"Task {task} deve ser Opus"
            assert decision.model_id == settings.model_opus

    async def test_model_ids_correct(self) -> None:
        """§2.4: IDs de modelo canônicos (não podem mudar sem gating de qualidade/custo)."""
        settings = make_settings()
        router = ModelRouter(settings)

        assert router.model_id_for_tier(ModelTier.HAIKU) == "claude-haiku-4-5-20251001"
        assert router.model_id_for_tier(ModelTier.SONNET) == "claude-sonnet-4-6"
        assert router.model_id_for_tier(ModelTier.OPUS) == "claude-opus-4-8"


@pytest.mark.asyncio
class TestModelRouterBudgetGating:
    async def test_opus_degrades_to_sonnet_when_budget_exhausted(self) -> None:
        """Gating de custo: Opus→Sonnet quando orçamento diário esgotado."""
        settings = make_settings(
            budget_input_tokens_per_tenant_day=100_000,
            budget_output_tokens_per_tenant_day=10_000,
        )
        budget = InMemoryBudgetTracker()
        router = ModelRouter(settings, budget)
        tenant = "tenant-with-exhausted-budget"

        # Simula orçamento esgotado
        await budget.record_usage(tenant, 100_001, 0, ModelTier.SONNET)

        decision = await router.route(TaskKind.COMPLEX_ANALYSIS, tenant)
        assert decision.tier == ModelTier.SONNET
        assert decision.degraded is True
        assert "downgrade" in decision.reason.lower()

    async def test_sonnet_degrades_to_haiku_when_budget_exhausted(self) -> None:
        """Gating de custo: Sonnet→Haiku quando orçamento total esgotado."""
        settings = make_settings(
            budget_input_tokens_per_tenant_day=100_000,
        )
        budget = InMemoryBudgetTracker()
        router = ModelRouter(settings, budget)
        tenant = "tenant-all-budget-gone"

        await budget.record_usage(tenant, 200_000, 0, ModelTier.SONNET)  # muito acima do limite

        decision = await router.route(TaskKind.CAMPAIGN_EDIT, tenant)
        assert decision.tier == ModelTier.HAIKU
        assert decision.degraded is True

    async def test_no_degradation_within_budget(self) -> None:
        """Sem degradação se o tenant ainda tem orçamento."""
        settings = make_settings()
        budget = InMemoryBudgetTracker()
        router = ModelRouter(settings, budget)
        tenant = "tenant-with-budget"

        # Uso baixo — bem dentro do orçamento
        await budget.record_usage(tenant, 1_000, 100, ModelTier.SONNET)

        decision = await router.route(TaskKind.COMPLEX_ANALYSIS, tenant)
        assert decision.tier == ModelTier.OPUS
        assert decision.degraded is False

    async def test_force_tier_still_gates_cost(self) -> None:
        """Force tier (usuário pediu Opus) ainda passa pelo gating de custo."""
        settings = make_settings(
            budget_input_tokens_per_tenant_day=100_000,
        )
        budget = InMemoryBudgetTracker()
        router = ModelRouter(settings, budget)
        tenant = "tenant-over-budget"

        await budget.record_usage(tenant, 150_000, 0, ModelTier.OPUS)

        decision = await router.route(
            TaskKind.GENERAL_CHAT, tenant, force_tier=ModelTier.OPUS
        )
        # Deve degradar mesmo com force_tier
        assert decision.tier != ModelTier.OPUS
        assert decision.degraded is True

    async def test_record_usage_accumulates(self) -> None:
        """Verifica que record_usage acumula corretamente."""
        budget = InMemoryBudgetTracker()
        tenant = "tenant-acc"
        await budget.record_usage(tenant, 1000, 100, ModelTier.SONNET)
        await budget.record_usage(tenant, 500, 50, ModelTier.HAIKU)

        usage = await budget.get_daily_usage(tenant)
        assert usage["input_tokens"] == 1500
        assert usage["output_tokens"] == 150
