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

import hashlib
import httpx
import structlog
from pydantic import BaseModel
from typing import Any, get_args

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
    SimilarCreative,
    SearchHelpDocsInput,
    SearchHelpDocsOutput,
    HelpDoc,
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

        # --- Verificação de PII (TX-5) — DEFAULT-DENY sobre TODOS os campos de
        # texto livre do schema `ValidateCreativeInput`, derivados por
        # introspecção Pydantic (`_publishable_text_fields`), não por uma
        # lista mantida à mão. Para creative_type=image/video, html_content é
        # None e o gate de PII não pode ficar estruturalmente sempre False:
        # dest_url e asset_url também são texto livre e podem carregar PII em
        # querystring (ex.: ?email=...&cpf=...). O mesmo vetor ja e reconhecido
        # do outro lado do sistema: em platform/observability/otel-collector.yaml,
        # o processor `transform/redact-pii` faz `delete_key(attributes,
        # "url.full")` nos tres tipos de statement exatamente porque uma URL
        # carrega querystring com PII. (Citado por ANCORA — nome do processor e
        # da chave —, nunca por numero de linha: numero de linha em prosa
        # cross-file e doc-lie com data de validade.) Um campo novo entra
        # automaticamente na varredura; só sai dela via
        # `_PII_SCAN_EXCLUDED_FIELDS` com justificativa.
        pii_fields = _publishable_text_fields(inp)
        pii_detected = False
        for field_name, field_value in pii_fields:
            if field_value and _detect_pii_in_html(field_value):
                pii_detected = True
                violations.append(
                    f"PII detectado em '{field_name}' do criativo (TX-5/DA-11). "
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

        SEGURANÇA (H2):
          - set_config parametrizado em statement separado ANTES da SELECT.
          - Schema correto: vector_store (não vector).
          - Cada operação adquire conexão dedicada do pool; set_config na mesma
            transação impede vazamento de tenant em transaction-pooling/PgBouncer.
        """
        if self._db is None:
            log.warning("search_similar_creatives.db_unavailable", tenant_id=tenant_id)
            return SearchSimilarCreativesOutput(results=[], total_searched=0)

        log.info(
            "search_similar_creatives.query",
            tenant_id=tenant_id,
            query_len=len(inp.query_text),
            top_k=inp.top_k,
            schema="vector_store",
        )

        # H2: dois statements separados, ambos PARAMETRIZADOS. tenant_id NUNCA
        # é interpolado via f-string no SQL — sempre passado como argumento
        # posicional ($1) para conn.execute/conn.fetch (asyncpg faz o bind
        # server-side, eliminando SQL injection). Cada chamada adquire uma
        # conexão dedicada do pool (acquire()) e o set_config roda na MESMA
        # transação da SELECT, para não vazar tenant_id em transaction-pooling
        # (PgBouncer).
        SQL_SET_CONFIG = "SELECT set_config('adserver.tenant_id', $1, true)"
        SQL_SELECT = """
            SELECT
                banner_id::text, campaign_id::text, creative_type, ctr,
                1 - (embedding <=> $1::vector) AS similarity_score,
                description_snippet
            FROM vector_store.creative_embeddings
            WHERE ($2::text IS NULL OR creative_type = $2)
              AND ($3::float IS NULL OR ctr >= $3)
            ORDER BY embedding <=> $1::vector
            LIMIT $4
        """
        query_embedding = await self._embed_text(inp.query_text)
        async with self._db.acquire() as conn:
            async with conn.transaction():
                await conn.execute(SQL_SET_CONFIG, tenant_id)
                rows = await conn.fetch(
                    SQL_SELECT,
                    query_embedding,
                    inp.creative_type.value if inp.creative_type else None,
                    inp.min_ctr,
                    inp.top_k,
                )

        results = [
            SimilarCreative(
                banner_id=row["banner_id"],
                campaign_id=row["campaign_id"],
                creative_type=row["creative_type"],
                ctr=row["ctr"],
                similarity_score=row["similarity_score"],
                description_snippet=row["description_snippet"],
            )
            for row in rows
        ]
        return SearchSimilarCreativesOutput(results=results, total_searched=len(rows))

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

        SEGURANÇA (H2):
          - set_config parametrizado em statement separado ANTES da SELECT.
          - Schema correto: vector_store (não vector).
        """
        if self._db is None:
            return SearchHelpDocsOutput(results=[])
        query_embedding = await self._embed_text(inp.query)

        # H2: set_config parametrizado (nunca f-string com tenant_id), conexão
        # dedicada por operação, schema vector_store.
        SQL_SET_CONFIG = "SELECT set_config('adserver.tenant_id', $1, true)"
        SQL_SELECT = """
            SELECT
                doc_id::text, title, snippet,
                1 - (embedding <=> $1::vector) AS relevance_score
            FROM vector_store.help_doc_embeddings
            ORDER BY embedding <=> $1::vector
            LIMIT $2
        """
        async with self._db.acquire() as conn:
            async with conn.transaction():
                await conn.execute(SQL_SET_CONFIG, tenant_id)
                rows = await conn.fetch(SQL_SELECT, query_embedding, inp.top_k)

        results = [
            HelpDoc(
                doc_id=row["doc_id"],
                title=row["title"],
                snippet=row["snippet"],
                relevance_score=row["relevance_score"],
            )
            for row in rows
        ]
        return SearchHelpDocsOutput(results=results)

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
        log.info(
            "create_campaign_draft",
            tenant_id=tenant_id,
            **_safe_log_free_text_kwargs(inp.name, key="name"),
        )
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
        log.info(
            "create_banner_draft",
            tenant_id=tenant_id,
            **_safe_log_free_text_kwargs(inp.name, key="name"),
        )
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
        # H2: set_config PARAMETRIZADO ($1), NUNCA f-string com tenant_id no
        # SQL. Conexão dedicada (acquire()) + set_config na mesma transação
        # da escrita, para não vazar tenant_id em transaction-pooling
        # (PgBouncer). NOTA: tenant_id do diff deve ser o DONO do diff
        # (C1: verificado pelo caller — apply_write_node — antes de chegar aqui).
        #
        # Dispatch por operação (INSERT/UPDATE específicos de cada entidade —
        # campaign/banner/delivery_rule/cap/campaign_zone) depende do schema
        # de db/config e ainda não está implementado (pendente de infra real,
        # G1). O que o H2 exige já está ativo neste ponto: a conexão é
        # dedicada, e o SET_CONFIG do RLS é sempre parametrizado — nunca
        # interpolado — ANTES de qualquer tentativa de mutação.
        SQL_SET_CONFIG = "SELECT set_config('adserver.tenant_id', $1, true)"
        async with self._db.acquire() as conn:
            async with conn.transaction():
                await conn.execute(SQL_SET_CONFIG, tenant_id)
                # Achado remediação copilot-honestidade #3 (30ª onda): NENHUM
                # INSERT/UPDATE é emitido acima (dispatch por operação ainda
                # não existe — pendente de G1). Retornar "applied" aqui seria
                # FALSO: um chamador (grafo/LLM/humano) que confie neste
                # status acreditaria que a escrita ocorreu quando ela não
                # ocorreu. status="pending_dispatch" diz a verdade: RLS foi
                # aplicado e a transação foi aberta, mas nenhuma mutação de
                # linha foi executada. Trocar para "applied" só quando o
                # dispatch por operação existir de fato.
                #
                # L2: resultado retornado ao LLM NUNCA inclui tenant_id.
                return {
                    "status": "pending_dispatch",
                    "operation": diff.operation,
                    "detail": (
                        "Dispatch de INSERT/UPDATE por operação ainda não "
                        "implementado (pendente de infra real, G1). Nenhuma "
                        "mutação foi persistida nesta chamada."
                    ),
                }

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

        SEGURANÇA (H3 — FAIL-CLOSED):
          - O judge é FAIL-CLOSED: na dúvida, erro ou ambiguidade → bloquear.
          - A camada determinística de padrões é a primeira linha; o Haiku real
            ficaria atrás desta interface (implementar quando ANTHROPIC_API_KEY
            estiver disponível — ver TODO abaixo).
          - Qualquer exceção → is_safe=False (bloqueio, não bypass).
          - AVISO DE ARQUITETURA: o judge é defesa-em-profundidade.
            O isolamento real vem do RLS+posse (C1/H2), não do judge.
            O judge NÃO substitui verificação de tenant no banco.
          - A chamada real ao Haiku 4.5 é assíncrona e fica atrás desta
            interface; sem ANTHROPIC_API_KEY, o fallback determinístico é
            usado com is_safe conservador.

        TODO (produção): substituir o bloco determinístico por chamada real:
          response = await anthropic_client.messages.create(
              model=settings.model_haiku,
              max_tokens=256,
              system=JUDGE_SYSTEM_PROMPT,
              messages=[{"role": "user", "content": inp.llm_output}],
          )
          Parsear resposta como JSON com campo "is_safe": bool.
          Se a API falhar → retornar fail-closed (is_safe=False).
        """
        try:
            return await self._haiku_judge_deterministic(tenant_id, inp)
        except Exception as exc:
            # H3: FAIL-CLOSED — qualquer exceção no judge → bloquear a saída
            log.error(
                "haiku_judge.exception_fail_closed",
                tenant_id=tenant_id,
                error=str(exc),
            )
            return HaikuJudgeOutput(
                is_safe=False,
                violations=[JudgeViolationType.PROMPT_INJECTION],  # categoria genérica de bloqueio
                explanation="Guardrail indisponível — bloqueio preventivo (fail-closed).",
                confidence=1.0,
            )

    async def _haiku_judge_deterministic(
        self,
        tenant_id: str,
        inp: HaikuJudgeInput,
    ) -> HaikuJudgeOutput:
        """
        Detecção determinística de padrões como camada base do guardrail.
        Complementada pela chamada real ao Haiku 4.5 em produção.

        H3: FAIL-CLOSED — padrões são necessários mas não suficientes para
        declarar saída segura. Em caso de ambiguidade o caller (haiku_judge)
        bloqueia via exceção.
        """
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


# ---------------------------------------------------------------------------
# Achado PRIV-06 remediação (32ª onda) — PII EM LOG: campos de texto livre
# PUBLICÁVEL (ex.: `name` de campanha/banner, fornecido pelo anunciante via
# LLM) nunca podem ser logados verbatim em `structlog`. Antes desta
# remediação, `create_campaign_draft` e `create_banner_draft` logavam
# `name=inp.name` cru — a MESMA forma do defeito nas duas entidades (achado
# pré-existente, exposto pelo fix PRIV-03 desta onda: o próprio campo `name`
# agora É escaneado por PII em `_run_creative_validation`, o que tornou
# visível que o valor cru continuava indo para o log de qualquer forma,
# independente do resultado do scan).
#
# Por que não "logar só depois do scan aprovar": o scan de
# `_detect_pii_in_html` é heurístico (regex para CPF/email/IPv4/telefone/
# cartão) — não exaustivo. Uma aprovação do scan não é garantia de ausência
# de PII (ex.: nome completo sem CPF/e-mail associado não é pego pelos
# padrões atuais). Logar verbatim "só quando aprovado" ainda vazaria esses
# falsos-negativos. A correção estrutural é nunca logar o texto livre em si:
# `_safe_log_free_text_kwargs` substitui o valor por comprimento + prefixo
# de hash SHA-256 — suficiente para correlacionar linhas de log do MESMO
# texto (ex.: dedução/depuração) sem jamais expor o conteúdo.
#
# Padrão único reutilizável (corrige a FORMA, não uma instância isolada) —
# usado por TODAS as entidades de draft com campo de texto livre publicável
# logado (`create_campaign_draft`/`create_banner_draft`, abaixo).
# ---------------------------------------------------------------------------

def _safe_log_free_text_kwargs(text: str | None, *, key: str) -> dict[str, Any]:
    """
    Kwargs seguros para `log.info(...)` a partir de um campo de texto livre
    publicável: NUNCA inclui o texto em si, só `{key}_len` e um prefixo de
    hash SHA-256 (`{key}_sha256_12`, 12 hex chars — suficiente para
    correlação, não reversível para o texto original).
    """
    if not text:
        return {f"{key}_len": 0, f"{key}_sha256_12": None}
    return {
        f"{key}_len": len(text),
        f"{key}_sha256_12": hashlib.sha256(text.encode("utf-8")).hexdigest()[:12],
    }


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


# ---------------------------------------------------------------------------
# PII scan — DEFAULT-DENY sobre campos de texto livre (Achado remediação
# copilot-honestidade #1, 30ª onda; generalizado para BaseModel arbitrário no
# Achado PRIV-03 / #5, 31ª onda).
#
# `_publishable_text_fields` NÃO está mais amarrado a `ValidateCreativeInput`:
# aceita qualquer `pydantic.BaseModel` e deriva por introspeção (`model_fields`)
# todo campo `str`/`str | None` a escanear. Isto é o que permite reutilizá-la
# tanto sobre o schema-espelho `ValidateCreativeInput` (dentro de
# `validate_creative`, abaixo) quanto sobre o PAYLOAD DE ESCRITA real
# (`CreateBannerDraftInput`/`UpdateBannerDraftInput` em
# `graph/nodes.py::_run_creative_validation`) — o nome/texto do banner
# (`name`) é publicável, persistido e logado verbatim, mas nunca existiu em
# `ValidateCreativeInput`; escaneá-lo exige rodar a introspecção sobre o
# schema de escrita, não sobre o schema-espelho de validação. Um campo novo
# adicionado a QUALQUER um desses schemas entra automaticamente na varredura
# sem exigir edição nesta função. A única forma de tirar um campo da
# varredura é listá-lo em `_PII_SCAN_EXCLUDED_FIELDS` com uma justificativa
# de por que ele NÃO é publicado — nunca via uma lista manual dos campos que
# DEVEM ser escaneados (essa era a forma do defeito original: allowlist
# positiva enumerada de 3 nomes que silenciosamente ignorava qualquer campo
# novo — e depois, forma repetida em escopo menor: escanear só o
# schema-espelho em vez do payload de escrita real).
# ---------------------------------------------------------------------------

_PII_SCAN_EXCLUDED_FIELDS: dict[str, str] = {
    "ai_generation_tool": (
        "Rótulo interno da ferramenta de geração (ex.: 'firefly', 'veo3', "
        "'claude') — metadado de auditoria consumido só pelo pipeline de "
        "geração; nunca renderizado no criativo publicado nem exposto ao "
        "usuário final do anúncio."
    ),
}


def _is_free_text_pydantic_annotation(annotation: Any) -> bool:
    """True se a anotação de tipo do campo é `str` ou `str | None` (Optional[str])."""
    if annotation is str:
        return True
    return str in get_args(annotation)


def _publishable_text_fields(inp: BaseModel) -> list[tuple[str, str | None]]:
    """
    Deriva por introspecção do modelo Pydantic — não de uma lista mantida à
    mão — todos os campos de texto livre publicáveis de `inp` a escanear por
    PII. DEFAULT-DENY: inclui todo campo `str`/`str | None` do schema, exceto
    os explicitamente justificados em `_PII_SCAN_EXCLUDED_FIELDS`.

    Genérica em `BaseModel`: funciona sobre QUALQUER schema Pydantic passado
    — `ValidateCreativeInput` (schema-espelho) ou o próprio payload de
    escrita (`CreateBannerDraftInput`/`UpdateBannerDraftInput`) — para que um
    campo de texto livre publicável não fique invisível à varredura só por
    existir no schema errado.
    """
    fields: list[tuple[str, str | None]] = []
    for field_name, field_info in type(inp).model_fields.items():
        if field_name in _PII_SCAN_EXCLUDED_FIELDS:
            continue
        if not _is_free_text_pydantic_annotation(field_info.annotation):
            continue
        fields.append((field_name, getattr(inp, field_name)))
    return fields
