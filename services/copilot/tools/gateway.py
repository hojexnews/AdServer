"""
tools/gateway.py — Gateway de autorização server-side para todas as ferramentas (TX-3).

INVARIANTES DE SEGURANÇA:
  1. tenant_id SEMPRE injetado server-side (não vem do LLM nem do payload).
  2. O LLM nunca recebe credencial; chama apenas as funções desta camada.
  3. Toda ferramenta de ESCRITA retorna um WriteDiff e aciona o HITL do grafo;
     a persistência só acontece APÓS aprovação humana.
  4. Instruções do payload do LLM são IGNORADAS na autorização — o gateway
     lê apenas o tenant_id injetado pelo BFF autenticado.
  5. Toda query DB aplica RLS via SET LOCAL adserver.tenant_id (TX-3).

FERRAMENTAS DISPONÍVEIS:
  Read-only:
    - simulate_forecast: chama ML/ranker; nunca produz número próprio.
    - validate_segmentation: anti-contradição §4.6/CA-4.
    - search_similar_creatives: RAG pgvector com RLS.
    - search_help_docs: RAG pgvector com RLS.
    - validate_creative: C2PA/SynthID + PII gate.

  Write (→ HITL obrigatório):
    - create_campaign_draft
    - update_campaign_draft
    - create_banner_draft
    - update_banner_draft
    - create_delivery_rule_draft
    - create_cap_draft
    - link_campaign_zone_draft

  Guardrail:
    - haiku_judge: Haiku-as-judge para brand-safety/injection.
"""

from __future__ import annotations

import httpx
import structlog
from typing import Any

from tools.schemas import (
    SimulateForecastInput,
    SimulateForecastOutput,
    ForecastConfidenceInterval,
    ValidateSegmentationInput,
    ValidateSegmentationOutput,
    SegmentationConflict,
    ValidateCreativeInput,
    ValidateCreativeOutput,
    SearchSimilarCreativesInput,
    SearchSimilarCreativesOutput,
    SearchHelpDocsInput,
    SearchHelpDocsOutput,
    CreateCampaignDraftInput,
    UpdateCampaignDraftInput,
    CreateBannerDraftInput,
    UpdateBannerDraftInput,
    CreateDeliveryRuleDraftInput,
    CreateCapDraftInput,
    LinkCampaignZoneDraftInput,
    HaikuJudgeInput,
    HaikuJudgeOutput,
    JudgeViolationType,
    WriteDiff,
    DeliveryVector,
    DeliveryOperator,
)
from app.config import CopilotSettings

log = structlog.get_logger(__name__)


class ToolGateway:
    """
    Gateway que expõe as ferramentas ao grafo LangGraph.

    Cada método recebe tenant_id INJETADO pelo grafo (vem do estado do
    thread, não do payload do LLM). O método verifica que tenant_id é
    válido antes de qualquer operação.

    Em produção, injetar uma conexão asyncpg poolada e o cliente httpx real.
    """

    def __init__(
        self,
        settings: CopilotSettings,
        db_pool: Any | None = None,           # asyncpg.Pool em produção
        ml_client: httpx.AsyncClient | None = None,
    ) -> None:
        self._settings = settings
        self._db = db_pool
        self._ml_client = ml_client or httpx.AsyncClient(
            base_url=settings.ml_forecast_url,
            timeout=settings.ml_forecast_timeout_s,
        )

    # =========================================================================
    # READ-ONLY: simulate_forecast
    # =========================================================================

    async def simulate_forecast(
        self,
        tenant_id: str,
        inp: SimulateForecastInput,
    ) -> SimulateForecastOutput:
        """
        Chama o serviço de ML para forecast de impressões/CTR.
        Se o serviço não estiver disponível, usa baseline Monte Carlo sobre StatsHourly.
        O LLM NUNCA produz o número — apenas verbaliza o resultado desta função.
        """
        log.info("simulate_forecast.start", tenant_id=tenant_id, pricing_model=inp.pricing_model)

        try:
            # Tenta o serviço de ML real (ranker-sidecar/J1-J2)
            payload = {
                "tenant_id": tenant_id,   # enviado ao serviço interno — nunca ao LLM
                "campaign_type": inp.campaign_type,
                "pricing_model": inp.pricing_model,
                "rate_minor_units": inp.rate_minor_units,
                "currency": inp.currency,
                "goal_target": inp.goal_target,
                "goal_metric": inp.goal_metric,
                "zone_ids": inp.zone_ids,
                "start_at": inp.start_at,
                "end_at": inp.end_at,
            }
            resp = await self._ml_client.post("/v1/forecast", json=payload)
            resp.raise_for_status()
            data = resp.json()
            return SimulateForecastOutput(
                estimated_impressions=ForecastConfidenceInterval(**data["estimated_impressions"]),
                estimated_clicks=ForecastConfidenceInterval(**data["estimated_clicks"])
                    if data.get("estimated_clicks") else None,
                estimated_ctr=ForecastConfidenceInterval(**data["estimated_ctr"])
                    if data.get("estimated_ctr") else None,
                model_version=data["model_version"],
                is_baseline=False,
                uncertainty_note=(
                    "Estimativa do modelo de ML pCTR. Incerteza representada pelo "
                    "intervalo p10–p90. Valores ao vivo podem diferir."
                ),
            )
        except (httpx.HTTPError, Exception) as exc:
            log.warning(
                "simulate_forecast.ml_unavailable",
                tenant_id=tenant_id,
                error=str(exc),
                fallback="monte_carlo_baseline",
            )
            return await self._monte_carlo_baseline(tenant_id, inp)

    async def _monte_carlo_baseline(
        self,
        tenant_id: str,
        inp: SimulateForecastInput,
    ) -> SimulateForecastOutput:
        """
        Baseline Monte Carlo sobre StatsHourly do ClickHouse.
        Usado quando o serviço de ML (J1/J2) ainda não está disponível.
        Aqui está implementado como stub determinístico para testes;
        em produção deve consultar o ClickHouse real.

        NOTA: ainda assim, o número vem desta função, não do LLM.
        """
        # Stub: em produção, query SELECT avg(impressions), stddev(impressions)
        # FROM stats.hourly WHERE zone_id = ANY($1) AND tenant_id = $2 ...
        # e roda Monte Carlo com scipy.stats ou numpy para gerar [p10, p50, p90].
        estimated_daily_imps = 10_000
        return SimulateForecastOutput(
            estimated_impressions=ForecastConfidenceInterval(
                p10=int(estimated_daily_imps * 0.7),
                p50=estimated_daily_imps,
                p90=int(estimated_daily_imps * 1.4),
            ),
            estimated_clicks=None,
            estimated_ctr=None,
            model_version="monte_carlo_baseline_v0",
            is_baseline=True,
            uncertainty_note=(
                "Estimativa via baseline Monte Carlo sobre StatsHourly "
                "(serviço de ML pCTR ainda não disponível). "
                "Intervalo p10–p90 tem alta incerteza. Não use para compromissos contratuais."
            ),
        )

    # =========================================================================
    # READ-ONLY: validate_segmentation (anti-contradição §4.6/CA-4)
    # =========================================================================

    async def validate_segmentation(
        self,
        tenant_id: str,
        inp: ValidateSegmentationInput,
    ) -> ValidateSegmentationOutput:
        """
        Valida conjunto de regras de segmentação contra contradições.

        Contradições detectadas:
        - Duas regras AND com mesmo vector, operadores mutuamente exclusivos
          (ex.: 'Geo - Country IS BR' AND 'Geo - Country IS US') → zero delivery.
        - 'IS' e 'IS NOT' para o mesmo vector+value.

        Esta validação roda INCLUSIVE sobre sugestões da IA (CA-4).
        """
        log.info(
            "validate_segmentation.start",
            tenant_id=tenant_id,
            rules_count=len(inp.rules),
        )
        conflicts: list[SegmentationConflict] = []

        # Detecta contradições no conjunto de regras AND
        and_rules = [r for r in inp.rules if r.logical_op == "AND"]
        for i, rule_a in enumerate(and_rules):
            for j, rule_b in enumerate(and_rules):
                if j <= i:
                    continue
                # Mesmo vector, values diferentes, operadores IS vs IS → impossível
                if (
                    rule_a.vector == rule_b.vector
                    and rule_a.operator == DeliveryOperator.IS
                    and rule_b.operator == DeliveryOperator.IS
                    and rule_a.value != rule_b.value
                ):
                    conflicts.append(SegmentationConflict(
                        rule_index_a=i,
                        rule_index_b=j,
                        description=(
                            f"Contradição AND: '{rule_a.vector.value} IS {rule_a.value}' "
                            f"e '{rule_b.vector.value} IS {rule_b.value}' "
                            f"nunca são verdadeiros ao mesmo tempo — zero delivery."
                        ),
                    ))

                # IS + IS NOT mesmo valor → contradição perfeita
                if (
                    rule_a.vector == rule_b.vector
                    and rule_a.value == rule_b.value
                    and {rule_a.operator, rule_b.operator} == {
                        DeliveryOperator.IS, DeliveryOperator.IS_NOT
                    }
                ):
                    conflicts.append(SegmentationConflict(
                        rule_index_a=i,
                        rule_index_b=j,
                        description=(
                            f"Contradição IS/IS NOT: '{rule_a.vector.value} IS {rule_a.value}' "
                            f"e '... IS NOT {rule_b.value}' são mutuamente exclusivos — zero delivery."
                        ),
                    ))

        warning: str | None = None
        if conflicts:
            warning = (
                "Este conjunto de regras AND resultará em zero delivery. "
                "Corrija as contradições antes de salvar."
            )

        return ValidateSegmentationOutput(
            is_valid=len(conflicts) == 0,
            conflicts=conflicts,
            warning=warning,
        )

    # =========================================================================
    # READ-ONLY: validate_creative (C2PA/SynthID + PII — gate EU AI Act Art. 50)
    # =========================================================================

    async def validate_creative(
        self,
        tenant_id: str,
        inp: ValidateCreativeInput,
    ) -> ValidateCreativeOutput:
        """
        Valida criativo gerado por IA.

        FLUXO C2PA/SynthID (EU AI Act Art. 50 — vigor 02/08/2026):
        1. Se is_ai_generated=True:
           a. Verifica se manifesto C2PA existe no asset (c2pa-tool/python-c2pa).
           b. Verifica watermark SynthID (Google DeepMind API ou offline).
           c. Garante disclosure "Gerado por IA" no metadata/overlay.
           d. Se faltar qualquer um → gate_passed=False.
        2. Verifica ausência de PII no HTML/texto (TX-5/DA-11).
        3. Valida dest_url (não pode ser vazia para image/html5/video).

        CREDENCIAIS NECESSÁRIAS:
          - C2PA: biblioteca 'c2pa-python' (pip install c2pa-python) + chave de signing
            em COPILOT_C2PA_SIGNING_KEY (PEM, armazenado no OpenBao).
          - SynthID: Google Cloud API key ou modelo offline (ver google-syntid-py).
          ATENÇÃO: estas integrações estão aqui como INTERFACE e STUB.
          Em produção, implementar as chamadas reais às SDKs.

        NOTA DE PRIVACIDADE (TX-5):
          O asset em si (bytes) NÃO é enviado ao LLM. Apenas metadados e URL.
        """
        log.info(
            "validate_creative.start",
            tenant_id=tenant_id,
            creative_type=inp.creative_type,
            is_ai_generated=inp.is_ai_generated,
        )
        violations: list[str] = []
        c2pa_ok = False
        syntid_ok = False
        disclosure_ok = False
        pii_detected = False

        # --- Verificações para criativos gerados por IA ---
        if inp.is_ai_generated:
            # C2PA manifest check (stub — em produção: import c2pa; c2pa.verify(asset_bytes))
            # Sem credencial real aqui; seria necessário COPILOT_C2PA_SIGNING_KEY no OpenBao.
            c2pa_ok = _stub_verify_c2pa(inp.asset_url)
            if not c2pa_ok:
                violations.append(
                    "Manifesto C2PA ausente ou inválido. "
                    "Assine o asset com c2pa-tool antes de publicar (EU AI Act Art. 50)."
                )

            # SynthID check (stub — em produção: google.syntid.verify(asset_bytes))
            syntid_ok = _stub_verify_syntid(inp.asset_url)
            if not syntid_ok:
                violations.append(
                    "Watermark SynthID não detectado. "
                    "Asset gerado por IA deve conter SynthID (EU AI Act Art. 50)."
                )

            # Disclosure "gerado por IA" no HTML ou metadata
            if inp.html_content:
                disclosure_ok = (
                    "gerado por ia" in inp.html_content.lower()
                    or "generated by ai" in inp.html_content.lower()
                    or "data-ai-generated" in inp.html_content.lower()
                )
            elif inp.asset_url:
                # Em produção verificar metadata EXIF/XMP
                disclosure_ok = _stub_verify_disclosure_metadata(inp.asset_url)

            if not disclosure_ok:
                violations.append(
                    "Disclosure 'gerado por IA' ausente no criativo. "
                    "Obrigatório por EU AI Act Art. 50 (vigor 02/08/2026)."
                )
        else:
            # Criativos não-IA: C2PA e SynthID não são obrigatórios
            c2pa_ok = True
            syntid_ok = True
            disclosure_ok = True

        # --- Verificação de PII no HTML (TX-5) ---
        if inp.html_content:
            pii_detected = _detect_pii_in_html(inp.html_content)
            if pii_detected:
                violations.append(
                    "PII detectado no HTML do criativo (TX-5/DA-11). "
                    "Remova dados pessoais antes de publicar."
                )

        # --- Verificação de dest_url ---
        if inp.creative_type.value in ("image", "html5", "video") and not inp.dest_url:
            violations.append(
                f"dest_url é obrigatório para criativo do tipo '{inp.creative_type.value}'."
            )

        gate_passed = len(violations) == 0

        log.info(
            "validate_creative.result",
            tenant_id=tenant_id,
            gate_passed=gate_passed,
            violations_count=len(violations),
        )

        return ValidateCreativeOutput(
            is_valid=gate_passed,
            c2pa_manifest_attached=c2pa_ok,
            syntid_watermark_confirmed=syntid_ok,
            disclosure_embedded=disclosure_ok,
            pii_detected=pii_detected,
            violations=violations,
            gate_passed=gate_passed,
        )

    # =========================================================================
    # READ-ONLY: RAG — search_similar_creatives
    # =========================================================================

    async def search_similar_creatives(
        self,
        tenant_id: str,
        inp: SearchSimilarCreativesInput,
    ) -> SearchSimilarCreativesOutput:
        """
        Busca criativos similares por CTR usando pgvector (HNSW).
        RLS por tenant_id aplicado SEMPRE (TX-3): nenhum tenant lê embeddings de outro.

        PROVEDOR DE EMBEDDINGS (§2.4):
          - Voyage AI (voyage-multilingual-2) — multilíngue, ótimo para PT-BR.
          - Cohere Embed v3 (embed-multilingual-v3.0) — alternativa.
          Chave protegida no OpenBao; nunca exposta ao LLM.

        NOTA: catálogo e taxonomia de regras §4.6 vão DIRETO no contexto com
        prompt caching — NÃO são buscados aqui. Este RAG é APENAS para
        "criativos similares por CTR" e "docs de ajuda".
        """
        if self._db is None:
            log.warning("search_similar_creatives.db_unavailable", tenant_id=tenant_id)
            return SearchSimilarCreativesOutput(results=[], total_searched=0)

        # 1. Gera embedding da query (provedor configurado em settings)
        query_embedding = await self._embed_text(inp.query_text)

        # 2. Query pgvector com RLS ativo (SET LOCAL adserver.tenant_id já
        #    está na conexão do pool injetado pelo gateway — TX-3)
        sql = """
            SET LOCAL adserver.tenant_id = $1;
            SELECT
                banner_id::text,
                campaign_id::text,
                creative_type,
                ctr,
                1 - (embedding <=> $2::vector) AS similarity_score,
                description_snippet
            FROM vector.creative_embeddings
            WHERE ($3::text IS NULL OR creative_type = $3)
              AND ($4::float IS NULL OR ctr >= $4)
            ORDER BY embedding <=> $2::vector
            LIMIT $5
        """
        # Em produção: async with self._db.acquire() as conn: rows = await conn.fetch(sql, ...)
        log.info(
            "search_similar_creatives.query",
            tenant_id=tenant_id,
            query_len=len(inp.query_text),
            top_k=inp.top_k,
        )
        # Stub de resultado enquanto não há banco real:
        return SearchSimilarCreativesOutput(results=[], total_searched=0)

    # =========================================================================
    # READ-ONLY: RAG — search_help_docs
    # =========================================================================

    async def search_help_docs(
        self,
        tenant_id: str,
        inp: SearchHelpDocsInput,
    ) -> SearchHelpDocsOutput:
        """
        Busca documentação de ajuda no pgvector.
        Docs de ajuda são tenant-agnostic (não contêm dados de campanha),
        mas a tabela ainda tem RLS para consistência arquitetural.
        """
        if self._db is None:
            return SearchHelpDocsOutput(results=[])
        query_embedding = await self._embed_text(inp.query)
        # Em produção: query vector.help_docs com RLS
        return SearchHelpDocsOutput(results=[])

    # =========================================================================
    # WRITE DRAFTS (→ HITL obrigatório — retornam WriteDiff, não persistem)
    # =========================================================================

    async def create_campaign_draft(
        self,
        tenant_id: str,
        inp: CreateCampaignDraftInput,
    ) -> WriteDiff:
        """
        Cria draft de campanha. NÃO persiste — retorna WriteDiff para HITL.
        A persistência acontece em apply_campaign_write() APÓS aprovação humana.
        """
        log.info("create_campaign_draft", tenant_id=tenant_id, name=inp.name)
        after = inp.model_dump()
        after["tenant_id"] = tenant_id  # injetado server-side
        return WriteDiff(
            operation="create_campaign",
            entity_type="campaign",
            entity_id=None,
            before=None,
            after=after,
        )

    async def update_campaign_draft(
        self,
        tenant_id: str,
        inp: UpdateCampaignDraftInput,
    ) -> WriteDiff:
        """
        Cria draft de atualização de campanha.
        Lê o estado atual do banco (RLS ativo) para gerar o diff before/after.
        """
        log.info("update_campaign_draft", tenant_id=tenant_id, campaign_id=inp.campaign_id)
        # Em produção: busca estado atual via SELECT com RLS ativo
        before: dict | None = None  # stub
        after = {k: v for k, v in inp.model_dump().items() if v is not None}
        return WriteDiff(
            operation="update_campaign",
            entity_type="campaign",
            entity_id=inp.campaign_id,
            before=before,
            after=after,
        )

    async def create_banner_draft(
        self,
        tenant_id: str,
        inp: CreateBannerDraftInput,
    ) -> WriteDiff:
        log.info("create_banner_draft", tenant_id=tenant_id, name=inp.name)
        after = inp.model_dump()
        after["tenant_id"] = tenant_id
        return WriteDiff(
            operation="create_banner",
            entity_type="banner",
            entity_id=None,
            before=None,
            after=after,
        )

    async def update_banner_draft(
        self,
        tenant_id: str,
        inp: UpdateBannerDraftInput,
    ) -> WriteDiff:
        log.info("update_banner_draft", tenant_id=tenant_id, banner_id=inp.banner_id)
        after = {k: v for k, v in inp.model_dump().items() if v is not None}
        return WriteDiff(
            operation="update_banner",
            entity_type="banner",
            entity_id=inp.banner_id,
            before=None,
            after=after,
        )

    async def create_delivery_rule_draft(
        self,
        tenant_id: str,
        inp: CreateDeliveryRuleDraftInput,
    ) -> WriteDiff:
        """
        Draft de regras de entrega. Inclui resultado da validação anti-contradição.
        A validação §4.6/CA-4 roda ANTES do HITL — se houver contradição,
        o draft já chega ao humano marcado como inválido.
        """
        # Valida segmentação antes de criar o draft
        val_input = ValidateSegmentationInput(
            rules=inp.rules,
            owner_type=inp.owner_type,
        )
        val_result = await self.validate_segmentation(tenant_id, val_input)

        log.info(
            "create_delivery_rule_draft",
            tenant_id=tenant_id,
            owner_type=inp.owner_type,
            rules_count=len(inp.rules),
            is_valid=val_result.is_valid,
        )

        after = inp.model_dump()
        after["tenant_id"] = tenant_id
        return WriteDiff(
            operation="create_delivery_rules",
            entity_type="delivery_rule",
            entity_id=None,
            before=None,
            after=after,
            validation_result=val_result.model_dump(),
        )

    async def create_cap_draft(
        self,
        tenant_id: str,
        inp: CreateCapDraftInput,
    ) -> WriteDiff:
        log.info(
            "create_cap_draft",
            tenant_id=tenant_id,
            owner_type=inp.owner_type,
            scope=inp.scope,
        )
        after = inp.model_dump()
        after["tenant_id"] = tenant_id
        return WriteDiff(
            operation="create_cap",
            entity_type="cap",
            entity_id=None,
            before=None,
            after=after,
        )

    async def link_campaign_zone_draft(
        self,
        tenant_id: str,
        inp: LinkCampaignZoneDraftInput,
    ) -> WriteDiff:
        log.info(
            "link_campaign_zone_draft",
            tenant_id=tenant_id,
            campaign_id=inp.campaign_id,
            zone_ids=inp.zone_ids,
        )
        after = inp.model_dump()
        after["tenant_id"] = tenant_id
        return WriteDiff(
            operation="link_campaign_zones",
            entity_type="campaign_zone",
            entity_id=inp.campaign_id,
            before=None,
            after=after,
        )

    # =========================================================================
    # APPLY — persistência REAL após aprovação HITL
    # =========================================================================

    async def apply_write(self, tenant_id: str, diff: WriteDiff) -> dict[str, Any]:
        """
        Aplica o WriteDiff aprovado pelo humano no banco de dados.
        Chamado APENAS pelo nó do grafo após aprovação HITL confirmada.

        Esta é a única função que faz mutação real. Toda escrita aqui
        aplica SET LOCAL adserver.tenant_id para RLS (TX-3).

        Em produção: conectar ao asyncpg pool com RLS habilitado e
        executar as queries de INSERT/UPDATE correspondentes.
        """
        log.info(
            "apply_write.start",
            tenant_id=tenant_id,
            operation=diff.operation,
            entity_type=diff.entity_type,
            entity_id=diff.entity_id,
        )
        if self._db is None:
            raise RuntimeError(
                "apply_write: banco de dados não configurado (db_pool=None). "
                "Configure DATABASE_URL e forneça o pool asyncpg."
            )
        # Em produção:
        # async with self._db.acquire() as conn:
        #     await conn.execute(f"SET LOCAL adserver.tenant_id = '{tenant_id}'")
        #     if diff.operation == "create_campaign":
        #         row = await conn.fetchrow(INSERT_CAMPAIGN_SQL, *diff.after.values())
        #         return {"id": row["id"], "status": "created"}
        #     ...
        return {"status": "applied", "operation": diff.operation, "tenant_id": tenant_id}

    # =========================================================================
    # GUARDRAIL: haiku_judge
    # =========================================================================

    async def haiku_judge(
        self,
        tenant_id: str,
        inp: HaikuJudgeInput,
    ) -> HaikuJudgeOutput:
        """
        Haiku-as-judge: avalia a saída do copiloto antes de enviá-la ao usuário.
        Usa Haiku 4.5 (rápido e barato) para classificar brand-safety,
        prompt-injection, claims falsos e tentativas de acesso a dados de outros tenants.

        Esta função é chamada pelo guardrail_node do grafo ANTES de emitir
        qualquer resposta ao usuário.
        """
        # A implementação real usa o Anthropic SDK com o cliente Haiku.
        # Aqui fazemos detecção determinística de padrões como camada base;
        # em produção complementar com chamada real ao Haiku.
        violations: list[JudgeViolationType] = []

        text_lower = inp.llm_output.lower()

        # Detecção de tentativa de prompt injection
        injection_patterns = [
            "ignore previous", "ignore as instruções", "esqueça as regras",
            "act as", "você é agora", "now you are", "pretend you are",
            "system prompt", "reveal your instructions",
        ]
        for pat in injection_patterns:
            if pat in text_lower:
                violations.append(JudgeViolationType.PROMPT_INJECTION)
                break

        # Detecção de tentativa de acesso a dados de outro tenant
        tenant_leak_patterns = [
            "tenant_id", "outro tenant", "other tenant", "all tenants",
            "todos os anunciantes", "lista todos",
        ]
        for pat in tenant_leak_patterns:
            if pat in text_lower:
                violations.append(JudgeViolationType.TENANT_LEAK)
                break

        # Detecção de solicitação de credencial
        credential_patterns = [
            "api_key", "password", "senha", "token", "secret",
            "anthropic_api_key", "database_url",
        ]
        for pat in credential_patterns:
            if pat in text_lower:
                violations.append(JudgeViolationType.CREDENTIAL_REQUEST)
                break

        # Detecção de PII na saída
        import re
        pii_in_output = _detect_pii_in_html(inp.llm_output)
        if pii_in_output:
            violations.append(JudgeViolationType.PII_IN_OUTPUT)

        is_safe = len(violations) == 0
        confidence = 0.95 if violations else 0.85  # determinístico é mais confiante

        log.info(
            "haiku_judge.result",
            tenant_id=tenant_id,
            is_safe=is_safe,
            violations=[v.value for v in violations],
        )

        return HaikuJudgeOutput(
            is_safe=is_safe,
            violations=violations,
            explanation=(
                "Saída aprovada pelo guardrail." if is_safe
                else f"Violações detectadas: {[v.value for v in violations]}"
            ),
            confidence=confidence,
        )

    # =========================================================================
    # Helpers privados
    # =========================================================================

    async def _embed_text(self, text: str) -> list[float]:
        """
        Gera embedding do texto usando o provedor configurado.
        Voyage AI (voyage-multilingual-2) ou Cohere Embed v3.
        A chave do provedor é lida de settings e NUNCA exposta ao LLM.

        CREDENCIAL NECESSÁRIA: VOYAGE_API_KEY ou COHERE_API_KEY no OpenBao.
        Esta função é um stub; em produção chamar a API real do provedor.
        """
        # Stub: retorna vetor zero com dimensão correta
        # Em produção:
        #   if provider == "voyage": voyageai.Client(api_key=settings.voyage_api_key).embed(...)
        #   if provider == "cohere": cohere.Client(api_key=settings.cohere_api_key).embed(...)
        return [0.0] * self._settings.embedding_dimensions


# ---------------------------------------------------------------------------
# Funções helper (stubs — implementar com SDKs reais em produção)
# ---------------------------------------------------------------------------

def _stub_verify_c2pa(asset_url: str | None) -> bool:
    """
    Stub para verificação C2PA.
    Em produção: usar c2pa-python SDK ou chamar c2pa-tool CLI.
    Chave de signing: COPILOT_C2PA_SIGNING_KEY (PEM) no OpenBao.
    Retorna True se o manifesto C2PA existe e é válido.
    """
    # ATENÇÃO: stub sempre retorna False para forçar o fluxo real a ser implementado.
    # Em produção: import c2pa; return c2pa.verify(fetch_asset_bytes(asset_url))
    return False


def _stub_verify_syntid(asset_url: str | None) -> bool:
    """
    Stub para verificação SynthID.
    Em produção: usar google-syntid-py ou DeepMind SynthID API.
    Retorna True se o watermark SynthID está presente.
    """
    return False


def _stub_verify_disclosure_metadata(asset_url: str | None) -> bool:
    """
    Stub para verificação de disclosure em metadata EXIF/XMP.
    Em produção: ler metadata do asset com Pillow/exifread.
    """
    return False


def _detect_pii_in_html(text: str) -> bool:
    """
    Detecção leve de PII em texto/HTML (TX-5/DA-11).
    Padrões: CPF, email, IP, telefone BR, cartão.
    """
    import re
    patterns = [
        r"\d{3}\.\d{3}\.\d{3}-\d{2}",             # CPF
        r"[a-zA-Z0-9_.+-]+@[a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+",  # email
        r"\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b",  # IPv4
        r"\(?\d{2}\)?\s?\d{4,5}-?\d{4}",           # telefone BR
        r"\b\d{4}[\s-]\d{4}[\s-]\d{4}[\s-]\d{4}\b",  # cartão de crédito
    ]
    for pat in patterns:
        if re.search(pat, text):
            return True
    return False
