"""
observability/langfuse_setup.py — Configuração do Langfuse self-hosted (TX-5/§2.4).

LANGFUSE SELF-HOSTED:
  - Instância própria do Langfuse (não cloud) para conformidade com TX-5.
  - Rastreia: traces de conversa, tool calls, latências, tokens por tenant,
    resultados do Haiku-as-judge.
  - NÃO rastreia: PII, credenciais, conteúdo sensível (redagido antes de exportar).

CREDENCIAIS NECESSÁRIAS:
  - LANGFUSE_PUBLIC_KEY: chave pública da instância self-hosted.
  - LANGFUSE_SECRET_KEY: chave secreta da instância self-hosted.
  - LANGFUSE_HOST: URL base (ex.: http://langfuse:3000).
  - LANGFUSE_ENABLED: false por padrão; true em produção.

PRIVACIDADE (TX-5/DA-11):
  - PII é redagido ANTES de qualquer export via OTel ou Langfuse.
  - Os "user_id" nos traces são tenant_id (UUID pseudônimo), não PII real.
  - Conteúdo de mensagens pode ser truncado/redagido na configuração.

GOLDEN SET DE EVALS (§2.4):
  Ver observability/golden_set.py para o conjunto de avaliação LLM-as-judge.
  Gating de qualidade E de custo/tokenização antes de promover modelo.
"""

from __future__ import annotations

import structlog
from typing import Any

log = structlog.get_logger(__name__)


def get_langfuse_client(settings: Any) -> Any | None:
    """
    Retorna o cliente Langfuse se habilitado e configurado.
    Retorna None se desabilitado (dev/CI) — a aplicação funciona sem Langfuse.

    CREDENCIAIS: LANGFUSE_PUBLIC_KEY + LANGFUSE_SECRET_KEY no OpenBao.
    A chave NUNCA é exposta ao LLM, ao front ou ao payload de request.
    """
    if not getattr(settings, "langfuse_enabled", False):
        return None

    pub_key = getattr(settings, "langfuse_public_key", None)
    sec_key = getattr(settings, "langfuse_secret_key", None)

    if not pub_key or not sec_key:
        log.warning(
            "langfuse.disabled",
            reason="LANGFUSE_PUBLIC_KEY ou LANGFUSE_SECRET_KEY não configurados.",
        )
        return None

    try:
        from langfuse import Langfuse  # type: ignore[import]

        client = Langfuse(
            public_key=pub_key.get_secret_value() if hasattr(pub_key, "get_secret_value") else pub_key,
            secret_key=sec_key.get_secret_value() if hasattr(sec_key, "get_secret_value") else sec_key,
            host=settings.langfuse_host,
            # Redação de PII: não incluir conteúdo bruto de mensagens do usuário
            # Em produção configurar Langfuse para mascarar campos sensíveis
        )
        log.info("langfuse.initialized", host=settings.langfuse_host)
        return client

    except ImportError:
        log.warning("langfuse.import_error", reason="langfuse não instalado. Observabilidade desabilitada.")
        return None
    except Exception as exc:
        log.error("langfuse.init_error", error=str(exc))
        return None


class TenantUsageTracker:
    """
    Rastreia uso de tokens por tenant para gating de custo (§2.4).
    Fonte de dados para o ModelRouter.BudgetTracker.

    Em produção:
    - Persiste contadores em Redis com TTL=dia (EXPIRE adserver:copilot:usage:{tenant_id}:{date} 86400)
    - Exporta métricas via OTel (sem PII nos labels)
    - Expõe via Grafana/VictoriaMetrics

    Exemplo de métrica OTel (sem PII):
      copilot_tokens_total{tenant_id="<uuid>", model_tier="sonnet", direction="input"} 12345
    """

    def record_otel_usage(
        self,
        tenant_id: str,
        input_tokens: int,
        output_tokens: int,
        model_tier: str,
        trace_id: str | None = None,
    ) -> None:
        """
        Emite métricas OTel de uso de tokens.
        PII: tenant_id é UUID pseudônimo — não é PII direto.
        """
        try:
            from opentelemetry import metrics  # type: ignore[import]
            meter = metrics.get_meter("adserver.copilot")
            counter = meter.create_counter(
                name="copilot_tokens_total",
                description="Total de tokens consumidos pelo copiloto",
            )
            counter.add(
                input_tokens,
                {"tenant_id": tenant_id, "model_tier": model_tier, "direction": "input"},
            )
            counter.add(
                output_tokens,
                {"tenant_id": tenant_id, "model_tier": model_tier, "direction": "output"},
            )
        except Exception:
            pass  # Observabilidade é best-effort; não quebra o fluxo principal
