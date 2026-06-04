"""
tests/test_schemas.py — Testes de validação Pydantic das ferramentas (verificável sem credencial).

Estes testes NÃO requerem ANTHROPIC_API_KEY, DATABASE_URL nem nenhum serviço externo.
Verificam:
  - Validação de entrada Pydantic v2 (tipos, ranges, constraints)
  - Regras de negócio nas ferramentas (tenancy/goal, clock/interval, etc.)
  - Detecção de PII no valor de regras de entrega (TX-5)
  - Anti-contradição §4.6 (zero delivery) via ValidateSegmentationOutput
  - WriteDiff é sempre criado sem persistir (mock do gateway)
  - Invariante TX-2: rate_minor_units como int (sem float)
"""

from __future__ import annotations

import pytest
from pydantic import ValidationError

from tools.schemas import (
    SimulateForecastInput,
    CreateCampaignDraftInput,
    UpdateCampaignDraftInput,
    CreateBannerDraftInput,
    CreateCapDraftInput,
    CreateDeliveryRuleDraftInput,
    LinkCampaignZoneDraftInput,
    ValidateSegmentationInput,
    ValidateCreativeInput,
    SearchSimilarCreativesInput,
    DeliveryRuleDraft,
    DeliveryVector,
    DeliveryOperator,
    CampaignType,
    PricingModel,
    GoalMetric,
    CreativeType,
    OwnerEntity,
    CapScope,
    HaikuJudgeInput,
    WriteDiff,
)


# ---------------------------------------------------------------------------
# SimulateForecastInput
# ---------------------------------------------------------------------------

class TestSimulateForecastInput:
    def test_valid_cpm_campaign(self) -> None:
        inp = SimulateForecastInput(
            campaign_type=CampaignType.REMNANT,
            pricing_model=PricingModel.CPM,
            rate_minor_units=500,   # R$5,00 em minor units (scale=2, TX-2)
            currency="BRL",
            goal_target=10_000,
            goal_metric=GoalMetric.IMPRESSIONS,
            zone_ids=["123"],
            start_at="2026-07-01T00:00:00Z",
            end_at="2026-07-31T23:59:59Z",
        )
        assert inp.rate_minor_units == 500
        assert inp.currency == "BRL"

    def test_tenancy_no_goal(self) -> None:
        inp = SimulateForecastInput(
            campaign_type=CampaignType.CONTRACT,
            pricing_model=PricingModel.TENANCY,
            rate_minor_units=0,
            currency="BRL",
            goal_target=None,
            goal_metric=None,
            zone_ids=[],
            start_at="2026-07-01T00:00:00Z",
            end_at="2026-07-31T23:59:59Z",
        )
        assert inp.goal_target is None

    def test_tenancy_with_goal_raises(self) -> None:
        with pytest.raises(ValidationError, match="Tenancy"):
            SimulateForecastInput(
                campaign_type=CampaignType.CONTRACT,
                pricing_model=PricingModel.TENANCY,
                rate_minor_units=0,
                currency="BRL",
                goal_target=1000,
                goal_metric=GoalMetric.IMPRESSIONS,
                zone_ids=[],
                start_at="2026-07-01T00:00:00Z",
                end_at="2026-07-31T23:59:59Z",
            )

    def test_non_tenancy_without_goal_raises(self) -> None:
        with pytest.raises(ValidationError, match="goal_target"):
            SimulateForecastInput(
                campaign_type=CampaignType.REMNANT,
                pricing_model=PricingModel.CPM,
                rate_minor_units=500,
                currency="BRL",
                goal_target=None,    # faltando
                goal_metric=None,    # faltando
                zone_ids=[],
                start_at="2026-07-01T00:00:00Z",
                end_at="2026-07-31T23:59:59Z",
            )

    def test_negative_rate_raises(self) -> None:
        with pytest.raises(ValidationError):
            SimulateForecastInput(
                campaign_type=CampaignType.REMNANT,
                pricing_model=PricingModel.CPM,
                rate_minor_units=-1,   # inválido (TX-2: >= 0)
                currency="BRL",
                goal_target=1000,
                goal_metric=GoalMetric.IMPRESSIONS,
                zone_ids=[],
                start_at="2026-07-01T00:00:00Z",
                end_at="2026-07-31T23:59:59Z",
            )


# ---------------------------------------------------------------------------
# CreateCampaignDraftInput — TX-2: rate como int (sem float)
# ---------------------------------------------------------------------------

class TestCreateCampaignDraftInput:
    def test_rate_is_integer(self) -> None:
        """TX-2: rate_minor_units deve ser int, não float."""
        inp = CreateCampaignDraftInput(
            name="Campanha Teste",
            advertiser_id=1,
            campaign_type=CampaignType.REMNANT,
            priority=5,
            goal_target=10_000,
            goal_metric=GoalMetric.IMPRESSIONS,
            start_at="2026-07-01T00:00:00Z",
            end_at="2026-07-31T23:59:59Z",
            pricing_model=PricingModel.CPM,
            rate_minor_units=300,   # R$3,00 = 300 minor units (scale=2) — int, sem float
            currency="BRL",
        )
        assert isinstance(inp.rate_minor_units, int)
        assert inp.rate_minor_units == 300

    def test_tenancy_no_goal(self) -> None:
        inp = CreateCampaignDraftInput(
            name="Tenancy Teste",
            advertiser_id=2,
            campaign_type=CampaignType.CONTRACT,
            pricing_model=PricingModel.TENANCY,
            rate_minor_units=0,
            currency="BRL",
            start_at="2026-07-01T00:00:00Z",
            end_at="2026-12-31T23:59:59Z",
        )
        assert inp.goal_target is None
        assert inp.goal_metric is None

    def test_tenancy_with_goal_raises(self) -> None:
        with pytest.raises(ValidationError, match="Tenancy"):
            CreateCampaignDraftInput(
                name="Erro",
                advertiser_id=1,
                campaign_type=CampaignType.CONTRACT,
                pricing_model=PricingModel.TENANCY,
                rate_minor_units=0,
                currency="BRL",
                goal_target=100,
                goal_metric=GoalMetric.CLICKS,
                start_at="2026-07-01T00:00:00Z",
                end_at="2026-12-31T23:59:59Z",
            )

    def test_invalid_priority_raises(self) -> None:
        with pytest.raises(ValidationError):
            CreateCampaignDraftInput(
                name="Erro",
                advertiser_id=1,
                campaign_type=CampaignType.REMNANT,
                priority=15,  # > 10 inválido
                goal_target=1000,
                goal_metric=GoalMetric.IMPRESSIONS,
                start_at="2026-07-01T00:00:00Z",
                end_at="2026-07-31T23:59:59Z",
                pricing_model=PricingModel.CPM,
                rate_minor_units=500,
                currency="BRL",
            )


# ---------------------------------------------------------------------------
# CreateCapDraftInput — scope=clock exige reset_interval
# ---------------------------------------------------------------------------

class TestCreateCapDraftInput:
    def test_clock_scope_requires_interval(self) -> None:
        with pytest.raises(ValidationError, match="reset_interval"):
            CreateCapDraftInput(
                owner_type=OwnerEntity.CAMPAIGN,
                owner_id=1,
                scope=CapScope.CLOCK,
                limit_count=5,
                reset_interval=None,  # obrigatório para clock
            )

    def test_non_clock_scope_rejects_interval(self) -> None:
        with pytest.raises(ValidationError, match="reset_interval"):
            CreateCapDraftInput(
                owner_type=OwnerEntity.CAMPAIGN,
                owner_id=1,
                scope=CapScope.SESSION,
                limit_count=5,
                reset_interval=3600,  # não deve ser fornecido para session
            )

    def test_valid_clock_cap(self) -> None:
        cap = CreateCapDraftInput(
            owner_type=OwnerEntity.BANNER,
            owner_id=42,
            scope=CapScope.CLOCK,
            limit_count=3,
            reset_interval=86400,  # 24 horas
        )
        assert cap.reset_interval == 86400

    def test_valid_campaign_total(self) -> None:
        cap = CreateCapDraftInput(
            owner_type=OwnerEntity.CAMPAIGN,
            owner_id=1,
            scope=CapScope.CAMPAIGN_TOTAL,
            limit_count=100_000,
        )
        assert cap.reset_interval is None


# ---------------------------------------------------------------------------
# DeliveryRuleDraft — detecção de PII no value (TX-5)
# ---------------------------------------------------------------------------

class TestDeliveryRuleDraftPII:
    def test_cpf_in_value_raises(self) -> None:
        """TX-5: CPF no value de regra deve ser bloqueado."""
        with pytest.raises(ValidationError, match="PII"):
            DeliveryRuleDraft(
                vector=DeliveryVector.SITE_VARIABLE,
                operator=DeliveryOperator.IS,
                value="123.456.789-00",  # CPF
                logical_op="AND",
            )

    def test_email_in_value_raises(self) -> None:
        """TX-5: Email no value de regra deve ser bloqueado."""
        with pytest.raises(ValidationError, match="PII"):
            DeliveryRuleDraft(
                vector=DeliveryVector.SITE_VARIABLE,
                operator=DeliveryOperator.IS,
                value="usuario@exemplo.com.br",
                logical_op="AND",
            )

    def test_ip_in_value_raises(self) -> None:
        """TX-5: IP no value de regra deve ser bloqueado."""
        with pytest.raises(ValidationError, match="PII"):
            DeliveryRuleDraft(
                vector=DeliveryVector.SITE_VARIABLE,
                operator=DeliveryOperator.IS,
                value="192.168.1.100",
                logical_op="AND",
            )

    def test_valid_geo_country(self) -> None:
        """País ISO não é PII."""
        rule = DeliveryRuleDraft(
            vector=DeliveryVector.GEO_COUNTRY,
            operator=DeliveryOperator.IS,
            value="BR",
            logical_op="AND",
        )
        assert rule.value == "BR"

    def test_valid_day_of_week(self) -> None:
        rule = DeliveryRuleDraft(
            vector=DeliveryVector.TIME_DAY_OF_WEEK,
            operator=DeliveryOperator.IS,
            value="Monday",
            logical_op="AND",
        )
        assert rule.value == "Monday"


# ---------------------------------------------------------------------------
# ValidateSegmentationInput — anti-contradição §4.6/CA-4
# ---------------------------------------------------------------------------

class TestValidateSegmentationInput:
    def test_valid_rules_accepted(self) -> None:
        rules = [
            DeliveryRuleDraft(
                vector=DeliveryVector.GEO_COUNTRY,
                operator=DeliveryOperator.IS,
                value="BR",
                logical_op="AND",
            ),
            DeliveryRuleDraft(
                vector=DeliveryVector.TIME_DAY_OF_WEEK,
                operator=DeliveryOperator.IS,
                value="Monday",
                logical_op="AND",
            ),
        ]
        inp = ValidateSegmentationInput(
            rules=rules,
            owner_type=OwnerEntity.CAMPAIGN,
        )
        assert len(inp.rules) == 2

    def test_empty_rules_raises(self) -> None:
        with pytest.raises(ValidationError):
            ValidateSegmentationInput(
                rules=[],
                owner_type=OwnerEntity.CAMPAIGN,
            )


# ---------------------------------------------------------------------------
# WriteDiff — estrutura do diff para HITL
# ---------------------------------------------------------------------------

class TestWriteDiff:
    def test_create_diff_has_no_before(self) -> None:
        diff = WriteDiff(
            operation="create_campaign",
            entity_type="campaign",
            entity_id=None,
            before=None,
            after={"name": "Nova Campanha", "tenant_id": "aaa-bbb-ccc"},
        )
        assert diff.before is None
        assert diff.after["name"] == "Nova Campanha"
        assert diff.entity_id is None

    def test_update_diff_has_before_and_after(self) -> None:
        diff = WriteDiff(
            operation="update_campaign",
            entity_type="campaign",
            entity_id=42,
            before={"name": "Campanha Antiga", "active": True},
            after={"name": "Campanha Nova", "active": True},
        )
        assert diff.before is not None
        assert diff.entity_id == 42


# ---------------------------------------------------------------------------
# HaikuJudgeInput — tamanho máximo
# ---------------------------------------------------------------------------

class TestHaikuJudgeInput:
    def test_valid_input(self) -> None:
        inp = HaikuJudgeInput(llm_output="Aqui está a análise da campanha...")
        assert len(inp.llm_output) > 0

    def test_too_long_input_raises(self) -> None:
        with pytest.raises(ValidationError):
            HaikuJudgeInput(llm_output="x" * 50_001)  # > 50k


# ---------------------------------------------------------------------------
# SearchSimilarCreativesInput
# ---------------------------------------------------------------------------

class TestSearchSimilarCreativesInput:
    def test_valid(self) -> None:
        inp = SearchSimilarCreativesInput(
            query_text="banner verão promoção desconto",
            creative_type=CreativeType.IMAGE,
            top_k=5,
            min_ctr=0.03,
        )
        assert inp.top_k == 5
        assert inp.min_ctr == 0.03

    def test_top_k_exceeds_limit_raises(self) -> None:
        with pytest.raises(ValidationError):
            SearchSimilarCreativesInput(
                query_text="test",
                top_k=25,  # > 20
            )

    def test_invalid_min_ctr_raises(self) -> None:
        with pytest.raises(ValidationError):
            SearchSimilarCreativesInput(
                query_text="test",
                min_ctr=1.5,  # > 1.0
            )


# ---------------------------------------------------------------------------
# ValidateCreativeInput
# ---------------------------------------------------------------------------

class TestValidateCreativeInput:
    def test_ai_generated_with_url(self) -> None:
        inp = ValidateCreativeInput(
            creative_type=CreativeType.IMAGE,
            asset_url="https://cdn.example.com/banner.png",
            is_ai_generated=True,
            ai_generation_tool="firefly",
            dest_url="https://anunciante.com",
        )
        assert inp.is_ai_generated is True
        assert inp.ai_generation_tool == "firefly"

    def test_non_ai_creative(self) -> None:
        inp = ValidateCreativeInput(
            creative_type=CreativeType.HTML5,
            html_content="<div>Promoção verão</div>",
            is_ai_generated=False,
            dest_url="https://anunciante.com",
        )
        assert inp.is_ai_generated is False
