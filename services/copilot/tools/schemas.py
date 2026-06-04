"""
tools/schemas.py — Schemas Pydantic v2 para todas as ferramentas tipadas (TX-3).

INVARIANTE DE SEGURANÇA (TX-3):
  - tenant_id NUNCA vem do body/payload do LLM. É sempre injetado server-side
    pelo gateway antes de qualquer chamada de ferramenta.
  - Nenhum schema aqui contém campo 'credentials', 'api_key', 'password' ou
    similar — o LLM não recebe credencial de nenhuma forma.
  - Toda escrita (mutação) passa pelo HITL antes de ser aplicada.

FERRAMENTAS:
  READ-ONLY (não param no grafo para HITL):
    - SimulateForecastInput / SimulateForecastOutput
    - ValidateSegmentationInput / ValidateSegmentationOutput
    - SearchSimilarCreativesInput / SearchSimilarCreativesOutput
    - SearchHelpDocsInput / SearchHelpDocsOutput

  WRITE (param obrigatoriamente no grafo — HITL antes de aplicar):
    - CreateCampaignDraftInput
    - UpdateCampaignDraftInput
    - CreateBannerDraftInput
    - UpdateBannerDraftInput
    - CreateDeliveryRuleDraftInput
    - CreateCapDraftInput
    - LinkCampaignZoneDraftInput

  GUARDRAIL:
    - ValidateCreativeInput / ValidateCreativeOutput (C2PA/SynthID + PII)
    - HaikuJudgeInput / HaikuJudgeOutput
"""

from __future__ import annotations

import enum
from typing import Any

from pydantic import BaseModel, Field, field_validator, model_validator


# ---------------------------------------------------------------------------
# Tipos auxiliares
# ---------------------------------------------------------------------------

class CampaignType(str, enum.Enum):
    OVERRIDE = "override"
    CONTRACT = "contract"
    REMNANT = "remnant"


class PricingModel(str, enum.Enum):
    CPM = "cpm"
    CPC = "cpc"
    CPA = "cpa"
    TENANCY = "tenancy"


class GoalMetric(str, enum.Enum):
    IMPRESSIONS = "impressions"
    CLICKS = "clicks"
    CONVERSIONS = "conversions"


class CreativeType(str, enum.Enum):
    IMAGE = "image"
    HTML5 = "html5"
    THIRDPARTY_TAG = "thirdparty_tag"
    VIDEO = "video"


class OwnerEntity(str, enum.Enum):
    CAMPAIGN = "campaign"
    BANNER = "banner"


class CapScope(str, enum.Enum):
    CAMPAIGN_TOTAL = "campaign_total"
    SESSION = "session"
    CLOCK = "clock"


class DeliveryVector(str, enum.Enum):
    TIME_DAY_OF_WEEK = "Time - Day of Week"
    SITE_URL = "Site - URL"
    GEO_COUNTRY = "Geo - Country"
    GEO_CITY = "Geo - City"
    CLIENT_USERAGENT = "Client - Useragent"
    SITE_VARIABLE = "Site - Variable"


class DeliveryOperator(str, enum.Enum):
    IS = "is"
    IS_NOT = "is not"
    CONTAINS = "contains"
    DOES_NOT_CONTAIN = "does not contain"
    STARTS_WITH = "starts with"
    ENDS_WITH = "ends with"


# ---------------------------------------------------------------------------
# FERRAMENTA: simulate_forecast (READ-ONLY)
# ---------------------------------------------------------------------------

class SimulateForecastInput(BaseModel):
    """
    Entrada para simulate_forecast.
    tenant_id é injetado server-side — nunca fornecido pelo LLM.

    O LLM NUNCA produz o número do forecast. Esta ferramenta chama o serviço
    de ML (ranker/J1-J2) ou o baseline Monte Carlo sobre StatsHourly.
    O LLM só verbaliza o resultado com faixa de incerteza.
    """

    # tenant_id: injetado server-side pelo gateway (não é campo do LLM)
    campaign_type: CampaignType = Field(
        description="Tipo da campanha para estimativa de alcance."
    )
    pricing_model: PricingModel = Field(
        description="Modelo de precificação da campanha."
    )
    rate_minor_units: int = Field(
        ge=0,
        description=(
            "Taxa em minor units inteiros do ativo (TX-2: sem float). "
            "Ex.: BRL 5,00 = 500 (scale=2)."
        ),
    )
    currency: str = Field(
        max_length=10,
        description="Código da moeda (rótulo, sem conversão automática — DA-10).",
    )
    goal_target: int | None = Field(
        default=None,
        ge=1,
        description="Meta de volume (impressões/cliques/conversões). None para tenancy.",
    )
    goal_metric: GoalMetric | None = Field(
        default=None,
        description="Métrica da meta. None para tenancy.",
    )
    zone_ids: list[str] = Field(
        default_factory=list,
        description="IDs das zonas alvo para o forecast.",
    )
    start_at: str = Field(
        description="ISO 8601 — data/hora de início da campanha.",
    )
    end_at: str = Field(
        description="ISO 8601 — data/hora de fim da campanha.",
    )

    @model_validator(mode="after")
    def _validate_tenancy_goal(self) -> "SimulateForecastInput":
        if self.pricing_model == PricingModel.TENANCY:
            if self.goal_target is not None or self.goal_metric is not None:
                raise ValueError(
                    "Tenancy não tem meta de volume: goal_target e goal_metric devem ser None."
                )
        else:
            if self.goal_target is None or self.goal_metric is None:
                raise ValueError(
                    "Campanhas não-tenancy exigem goal_target e goal_metric."
                )
        return self


class ForecastConfidenceInterval(BaseModel):
    p10: float = Field(description="Percentil 10 (otimista baixo).")
    p50: float = Field(description="Mediana.")
    p90: float = Field(description="Percentil 90 (pessimista).")


class SimulateForecastOutput(BaseModel):
    """
    Saída do simulate_forecast.
    O número vem APENAS do serviço de ML — o LLM nunca o produz (§2.4).
    """

    estimated_impressions: ForecastConfidenceInterval = Field(
        description="Estimativa de impressões [p10, p50, p90]."
    )
    estimated_clicks: ForecastConfidenceInterval | None = Field(
        default=None,
        description="Estimativa de cliques, se disponível.",
    )
    estimated_ctr: ForecastConfidenceInterval | None = Field(
        default=None,
        description="Estimativa de pCTR [p10, p50, p90].",
    )
    model_version: str = Field(
        description="Versão do modelo de ML usado (ou 'monte_carlo_baseline')."
    )
    is_baseline: bool = Field(
        default=False,
        description="True = Monte Carlo sobre StatsHourly (serviço de ML ainda não disponível).",
    )
    uncertainty_note: str = Field(
        description="Nota de incerteza para verbalização pelo LLM.",
    )


# ---------------------------------------------------------------------------
# FERRAMENTA: validate_segmentation (READ-ONLY — validação anti-contradição §4.6/CA-4)
# ---------------------------------------------------------------------------

class DeliveryRuleDraft(BaseModel):
    vector: DeliveryVector
    operator: DeliveryOperator
    value: str = Field(min_length=1, max_length=500)
    logical_op: str = Field(default="AND", pattern="^(AND|OR)$")

    @field_validator("value")
    @classmethod
    def _no_pii_in_value(cls, v: str) -> str:
        """
        Bloqueia PII óbvio no valor da regra (TX-5/DA-11).
        Validação leve: padrões de CPF, email, IP são rejeitados.
        Em produção complementar com validação mais robusta.
        """
        import re
        pii_patterns = [
            r"\d{3}\.\d{3}\.\d{3}-\d{2}",   # CPF
            r"[a-zA-Z0-9_.+-]+@[a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+",  # email
            r"\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b",  # IP
        ]
        for pat in pii_patterns:
            if re.search(pat, v):
                raise ValueError(
                    f"Valor de regra contém padrão de PII (TX-5). Remova: '{v[:20]}...'"
                )
        return v


class ValidateSegmentationInput(BaseModel):
    """
    Valida conjunto de regras de segmentação contra anti-contradição §4.6/CA-4.
    READ-ONLY: não modifica o banco — apenas simula e reporta conflitos.
    """

    rules: list[DeliveryRuleDraft] = Field(
        min_length=1,
        description="Lista de regras a validar.",
    )
    owner_type: OwnerEntity
    # Contexto das regras existentes (não editadas) para detectar conflito com existentes
    existing_rule_ids: list[int] = Field(
        default_factory=list,
        description="IDs de regras já salvas para checar contradição global.",
    )


class SegmentationConflict(BaseModel):
    rule_index_a: int
    rule_index_b: int
    description: str


class ValidateSegmentationOutput(BaseModel):
    is_valid: bool
    conflicts: list[SegmentationConflict] = Field(default_factory=list)
    warning: str | None = Field(
        default=None,
        description="Aviso se o conjunto de regras AND resultar em zero delivery.",
    )


# ---------------------------------------------------------------------------
# FERRAMENTA: validate_creative (gate de publicação — C2PA/SynthID + PII)
# ---------------------------------------------------------------------------

class ValidateCreativeInput(BaseModel):
    """
    Valida criativo gerado por IA antes de publicação.
    Aplica/verifica C2PA/SynthID + disclosure "gerado por IA" (EU AI Act Art. 50).
    Também verifica ausência de PII (TX-5).
    """

    creative_type: CreativeType
    asset_url: str | None = Field(default=None, description="URL do asset (para imagem/vídeo).")
    html_content: str | None = Field(
        default=None,
        max_length=500_000,
        description="Conteúdo HTML5 (para html5 creatives).",
    )
    is_ai_generated: bool = Field(
        default=False,
        description="True se o master visual ou texto foi gerado por IA.",
    )
    ai_generation_tool: str | None = Field(
        default=None,
        description="Ferramenta usada para geração (ex.: 'firefly', 'veo3', 'claude').",
    )
    dest_url: str | None = Field(default=None, description="URL de destino do clique.")


class ValidateCreativeOutput(BaseModel):
    is_valid: bool
    c2pa_manifest_attached: bool = Field(
        description="True se o manifesto C2PA foi anexado/verificado no asset."
    )
    syntid_watermark_confirmed: bool = Field(
        description="True se o SynthID watermark foi verificado (assets IA)."
    )
    disclosure_embedded: bool = Field(
        description="True se o disclosure 'gerado por IA' está presente (EU AI Act Art. 50)."
    )
    pii_detected: bool = Field(
        default=False,
        description="True se PII foi detectado no criativo (bloqueia publicação — TX-5).",
    )
    violations: list[str] = Field(
        default_factory=list,
        description="Lista de violações que impedem publicação.",
    )
    gate_passed: bool = Field(
        description="True = criativo pode prosseguir para HITL/publicação."
    )


# ---------------------------------------------------------------------------
# FERRAMENTAS RAG (READ-ONLY)
# ---------------------------------------------------------------------------

class SearchSimilarCreativesInput(BaseModel):
    """
    Busca criativos similares por CTR no pgvector (RAG escopado — §2.4).
    RLS por tenant_id aplicado server-side na query vetorial (TX-3).
    """

    query_text: str = Field(
        min_length=3,
        max_length=500,
        description="Texto de busca (ex.: descrição do criativo ou objetivo da campanha).",
    )
    creative_type: CreativeType | None = Field(
        default=None,
        description="Filtro opcional por tipo de criativo.",
    )
    top_k: int = Field(
        default=5,
        ge=1,
        le=20,
        description="Número de resultados a retornar.",
    )
    min_ctr: float | None = Field(
        default=None,
        ge=0.0,
        le=1.0,
        description="CTR mínimo para filtrar resultados.",
    )


class SimilarCreative(BaseModel):
    banner_id: str
    campaign_id: str
    creative_type: str
    ctr: float
    similarity_score: float
    description_snippet: str = Field(description="Trecho do texto do criativo (sem PII).")


class SearchSimilarCreativesOutput(BaseModel):
    results: list[SimilarCreative] = Field(default_factory=list)
    total_searched: int


class SearchHelpDocsInput(BaseModel):
    """
    Busca documentação de ajuda no pgvector.
    Não contém dados de campanha — apenas docs públicos do sistema.
    """

    query: str = Field(min_length=3, max_length=500)
    top_k: int = Field(default=3, ge=1, le=10)


class HelpDoc(BaseModel):
    doc_id: str
    title: str
    snippet: str
    relevance_score: float


class SearchHelpDocsOutput(BaseModel):
    results: list[HelpDoc] = Field(default_factory=list)


# ---------------------------------------------------------------------------
# FERRAMENTAS DE ESCRITA (gated por HITL — nunca aplicadas direto)
# ---------------------------------------------------------------------------
# Estas entradas são criadas pelo LLM como "draft" e passam pelo nó
# HITL do LangGraph antes de qualquer PATCH no banco.

class CreateCampaignDraftInput(BaseModel):
    """Draft de criação de campanha. Requer aprovação HITL antes de persistir."""

    name: str = Field(min_length=1, max_length=200)
    advertiser_id: int = Field(ge=1)
    campaign_type: CampaignType
    priority: int = Field(ge=1, le=10, default=5)
    goal_target: int | None = Field(default=None, ge=1)
    goal_metric: GoalMetric | None = None
    start_at: str = Field(description="ISO 8601")
    end_at: str = Field(description="ISO 8601")
    pricing_model: PricingModel
    rate_minor_units: int = Field(
        ge=0,
        description="Rate em minor units inteiros (TX-2: sem float).",
    )
    currency: str = Field(max_length=10, default="BRL")

    @model_validator(mode="after")
    def _validate_goal(self) -> "CreateCampaignDraftInput":
        if self.pricing_model == PricingModel.TENANCY:
            if self.goal_target is not None or self.goal_metric is not None:
                raise ValueError("Tenancy não usa goal_target nem goal_metric.")
        else:
            if self.goal_target is None or self.goal_metric is None:
                raise ValueError("Campanhas não-tenancy exigem goal_target e goal_metric.")
        return self


class UpdateCampaignDraftInput(BaseModel):
    """Draft de atualização de campanha. Requer aprovação HITL antes de persistir."""

    campaign_id: int = Field(ge=1)
    name: str | None = Field(default=None, min_length=1, max_length=200)
    priority: int | None = Field(default=None, ge=1, le=10)
    goal_target: int | None = Field(default=None, ge=1)
    end_at: str | None = Field(default=None, description="ISO 8601")
    rate_minor_units: int | None = Field(default=None, ge=0)
    active: bool | None = None


class CreateBannerDraftInput(BaseModel):
    """Draft de criação de banner/criativo. Requer aprovação HITL antes de persistir."""

    campaign_id: int = Field(ge=1)
    name: str = Field(min_length=1, max_length=200)
    creative_type: CreativeType
    asset_url: str | None = None
    dest_url: str | None = None
    width: int = Field(ge=1)
    height: int = Field(ge=1)
    is_ai_generated: bool = Field(default=False)


class UpdateBannerDraftInput(BaseModel):
    banner_id: int = Field(ge=1)
    name: str | None = Field(default=None, min_length=1, max_length=200)
    dest_url: str | None = None
    active: bool | None = None


class CreateDeliveryRuleDraftInput(BaseModel):
    """Draft de criação de regra de entrega §4.6. Requer validação anti-contradição + HITL."""

    owner_type: OwnerEntity
    owner_id: int = Field(ge=1)
    rules: list[DeliveryRuleDraft] = Field(min_length=1)


class CreateCapDraftInput(BaseModel):
    """Draft de criação de cap. Requer aprovação HITL antes de persistir."""

    owner_type: OwnerEntity
    owner_id: int = Field(ge=1)
    scope: CapScope
    limit_count: int = Field(ge=1)
    reset_interval: int | None = Field(
        default=None,
        ge=1,
        description="Segundos. Obrigatório para scope=clock.",
    )

    @model_validator(mode="after")
    def _validate_clock_interval(self) -> "CreateCapDraftInput":
        if self.scope == CapScope.CLOCK and self.reset_interval is None:
            raise ValueError("Cap com scope=clock exige reset_interval.")
        if self.scope != CapScope.CLOCK and self.reset_interval is not None:
            raise ValueError("reset_interval só é válido para scope=clock.")
        return self


class LinkCampaignZoneDraftInput(BaseModel):
    """Draft de vínculo campanha↔zona N:N. Requer aprovação HITL antes de persistir."""

    campaign_id: int = Field(ge=1)
    zone_ids: list[int] = Field(min_length=1)


# ---------------------------------------------------------------------------
# GUARDRAIL: HaikuJudgeInput / HaikuJudgeOutput (Haiku-as-judge)
# ---------------------------------------------------------------------------

class JudgeViolationType(str, enum.Enum):
    PROMPT_INJECTION = "prompt_injection"
    TENANT_LEAK = "tenant_leak"       # instrução de acessar dados de outro tenant
    BRAND_SAFETY = "brand_safety"
    FALSE_CLAIM = "false_claim"
    MALICIOUS_INSTRUCTION = "malicious_instruction"
    PII_IN_OUTPUT = "pii_in_output"
    CREDENTIAL_REQUEST = "credential_request"  # LLM pedindo credencial


class HaikuJudgeInput(BaseModel):
    """
    Entrada para o Haiku-as-judge (§2.4).
    Avalia a saída do copiloto antes de enviá-la ao usuário.
    """

    llm_output: str = Field(
        max_length=50_000,
        description="Saída do LLM a avaliar.",
    )
    # Contexto de verificação (nunca inclui credenciais ou dados de outros tenants)
    context_hint: str | None = Field(
        default=None,
        max_length=2000,
        description="Dica de contexto para o judge (ex.: 'contexto: campanha de produto X').",
    )


class HaikuJudgeOutput(BaseModel):
    is_safe: bool
    violations: list[JudgeViolationType] = Field(default_factory=list)
    explanation: str = Field(
        description="Explicação breve do veredito (em PT-BR, sem dados sensíveis)."
    )
    # Score de confiança do judge (0.0 = incerto, 1.0 = certeza)
    confidence: float = Field(ge=0.0, le=1.0)


# ---------------------------------------------------------------------------
# Envelope de diff para HITL — o que o usuário aprova/rejeita
# ---------------------------------------------------------------------------

class WriteDiff(BaseModel):
    """
    Preview de diff apresentado ao humano para aprovação (HITL).
    O BFF renderiza este diff na UI antes do 1-clique "Aplicar".
    """

    operation: str = Field(
        description="Operação: 'create_campaign', 'update_banner', etc."
    )
    entity_type: str
    entity_id: int | None = Field(
        default=None,
        description="ID da entidade a modificar (None para criações).",
    )
    # Representação do estado antes/depois para diff visual
    before: dict[str, Any] | None = Field(
        default=None,
        description="Estado atual da entidade (None para criações).",
    )
    after: dict[str, Any] = Field(
        description="Estado proposto pela IA.",
    )
    validation_result: dict[str, Any] | None = Field(
        default=None,
        description="Resultado da validação Pydantic/anti-contradição.",
    )
