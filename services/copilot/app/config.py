"""
config.py — Configuração do copiloto via variáveis de ambiente.

SEGURANÇA (TX-3):
  - A chave Claude (ANTHROPIC_API_KEY) é lida APENAS pelo processo servidor.
  - Nunca exposta ao LLM, ao front-end nem ao payload de request.
  - O BFF injeta tenant_id via header X-Tenant-ID; este serviço NÃO aceita
    tenant_id do corpo do request (ignora instruções do payload — TX-3).

CREDENCIAIS NECESSÁRIAS PARA RODAR:
  - ANTHROPIC_API_KEY: chave da Anthropic (Claude API). Obtida em console.anthropic.com.
  - DATABASE_URL: DSN asyncpg do Postgres com pgvector habilitado.
  - LANGFUSE_PUBLIC_KEY / LANGFUSE_SECRET_KEY: instância self-hosted do Langfuse.
  - LANGFUSE_HOST: URL do Langfuse self-hosted (ex.: http://langfuse:3000).
  - EMBEDDING_PROVIDER: "voyage" ou "cohere" (padrão: "voyage").
  - VOYAGE_API_KEY ou COHERE_API_KEY: chave do provedor de embeddings.
  - ML_FORECAST_URL: URL interna do serviço de forecast/ML (services/ranker-sidecar ou stub).
  - COPILOT_INTERNAL_SECRET: segredo compartilhado com o BFF para auth interna.
"""

from pydantic_settings import BaseSettings, SettingsConfigDict
from pydantic import Field, SecretStr


class CopilotSettings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        case_sensitive=False,
    )

    # -------------------------------------------------------------------------
    # Claude / Anthropic
    # -------------------------------------------------------------------------
    anthropic_api_key: SecretStr = Field(
        ...,
        description="Chave API da Anthropic. NUNCA exposta ao LLM ou ao front.",
    )

    # IDs de modelo canônicos (§2.4 / stack-tecnologico.md)
    model_haiku: str = Field(
        default="claude-haiku-4-5-20251001",
        description="Haiku 4.5 — inline/autocomplete e Haiku-as-judge.",
    )
    model_sonnet: str = Field(
        default="claude-sonnet-4-6",
        description="Sonnet 4.6 (1M ctx) — cérebro padrão de raciocínio/tool-use.",
    )
    model_opus: str = Field(
        default="claude-opus-4-8",
        description="Opus 4.8 — planejamento premium.",
    )

    # Orçamento de tokens por tenant (gating de custo — §2.4)
    # Valores padrão: 100k input + 10k output por sessão/tenant/dia
    budget_input_tokens_per_tenant_day: int = Field(
        default=100_000,
        description="Teto de tokens de entrada por tenant por dia (gating de custo).",
    )
    budget_output_tokens_per_tenant_day: int = Field(
        default=10_000,
        description="Teto de tokens de saída por tenant por dia (gating de custo).",
    )

    # -------------------------------------------------------------------------
    # Banco de dados (pgvector para RAG)
    # -------------------------------------------------------------------------
    database_url: SecretStr = Field(
        ...,
        description="DSN asyncpg: postgresql+asyncpg://user:pass@host/db",
    )

    # -------------------------------------------------------------------------
    # Langfuse self-hosted (TX-5 / §2.4)
    # -------------------------------------------------------------------------
    langfuse_public_key: SecretStr | None = Field(
        default=None,
        description="Langfuse public key (instância self-hosted).",
    )
    langfuse_secret_key: SecretStr | None = Field(
        default=None,
        description="Langfuse secret key (instância self-hosted).",
    )
    langfuse_host: str = Field(
        default="http://localhost:3000",
        description="URL base do Langfuse self-hosted.",
    )
    langfuse_enabled: bool = Field(
        default=False,
        description="Habilita Langfuse. False se não houver instância (dev/CI).",
    )

    # -------------------------------------------------------------------------
    # Embeddings — Voyage / Cohere v4 multilíngue PT-BR (§2.4)
    # -------------------------------------------------------------------------
    embedding_provider: str = Field(
        default="voyage",
        description="Provedor de embeddings: 'voyage' ou 'cohere'.",
    )
    embedding_model_voyage: str = Field(
        default="voyage-multilingual-2",
        description="Modelo Voyage para embeddings multilíngue (PT-BR).",
    )
    embedding_model_cohere: str = Field(
        default="embed-multilingual-v3.0",
        description="Modelo Cohere v3/v4 para embeddings multilíngue (PT-BR).",
    )
    embedding_dimensions: int = Field(
        default=1024,
        description="Dimensão dos vetores de embedding (deve coincidir com a migration SQL).",
    )
    voyage_api_key: SecretStr | None = Field(
        default=None,
        description="Chave API do Voyage AI. Necessária se embedding_provider=voyage.",
    )
    cohere_api_key: SecretStr | None = Field(
        default=None,
        description="Chave API do Cohere. Necessária se embedding_provider=cohere.",
    )

    # -------------------------------------------------------------------------
    # Serviço de ML / Forecast (simulate_forecast — read-only)
    # -------------------------------------------------------------------------
    ml_forecast_url: str = Field(
        default="http://ranker-sidecar:8080",
        description="URL interna do serviço de ML/ranker para simulate_forecast.",
    )
    ml_forecast_timeout_s: float = Field(
        default=5.0,
        description="Timeout (segundos) para chamadas ao serviço de ML.",
    )

    # -------------------------------------------------------------------------
    # Auth interna BFF↔copilot (TX-3)
    # -------------------------------------------------------------------------
    copilot_internal_secret: SecretStr = Field(
        ...,
        description="Segredo HMAC compartilhado com o BFF para validar requests internos.",
    )

    # -------------------------------------------------------------------------
    # Servidor
    # -------------------------------------------------------------------------
    host: str = Field(default="0.0.0.0", description="Bind address do servidor FastAPI.")
    port: int = Field(default=8001, description="Porta do servidor FastAPI.")
    log_level: str = Field(default="info", description="Nível de log (structlog).")

    # -------------------------------------------------------------------------
    # HITL — checkpoints do LangGraph
    # -------------------------------------------------------------------------
    hitl_approval_timeout_s: int = Field(
        default=3600,
        description="Timeout em segundos para aprovação HITL. Após expirar, a escrita é cancelada.",
    )


def get_settings() -> CopilotSettings:
    """Retorna instância singleton de CopilotSettings."""
    return CopilotSettings()  # type: ignore[call-arg]
