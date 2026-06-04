"""
app/auth.py — Autenticação e extração segura de tenant_id (TX-3).

FLUXO DE AUTORIZAÇÃO SERVER-SIDE:
  1. O BFF autentica o usuário (sessão/JWT) e injeta X-Tenant-ID no header.
  2. Este middleware valida que o header existe e é um UUID válido.
  3. O HMAC com COPILOT_INTERNAL_SECRET garante que a request vem do BFF
     autenticado — não de um cliente externo que tentou forjar o header.
  4. tenant_id é então passado para o estado do grafo — NUNCA extraído do body.

INVARIANTE (TX-3):
  - tenant_id NUNCA vem do corpo do request nem do prompt do LLM.
  - O BFF é a única fonte confiável de tenant_id.
  - Requests sem HMAC válido são rejeitadas com 401.
  - Requests sem X-Tenant-ID são rejeitadas com 400.
  - Instruções do payload são ignoradas na autorização (não há lógica que
    leia tenant_id do corpo para autorizar).

SEGURANÇA (H1):
  - SKIP_AUTH_DEV=true é PROIBIDO em produção (APP_ENV=production → RuntimeError no boot).
  - A string sentinela "dev-skip" NUNCA é aceita como assinatura válida.
  - COPILOT_INTERNAL_SECRET ausente em produção → RuntimeError no boot (fail-closed).
  - Em dev/CI com SKIP_AUTH_DEV=true: UUID ainda é obrigatório e validado; apenas
    a verificação HMAC é pulada.
"""

from __future__ import annotations

import hashlib
import hmac
import os
import time
import uuid as _uuid_module
from typing import Annotated

import structlog
from fastapi import Depends, Header, HTTPException, Request, status
from pydantic import BaseModel

from app.config import CopilotSettings, get_settings

log = structlog.get_logger(__name__)

# ---------------------------------------------------------------------------
# Validação de startup (H1: fail-closed)
# ---------------------------------------------------------------------------

def check_auth_config_on_startup(settings: CopilotSettings) -> None:
    """
    Valida a configuração de autenticação no startup.
    Chamado durante a inicialização da aplicação.

    Regras (H1):
      1. Se APP_ENV=production, SKIP_AUTH_DEV=true é PROIBIDO → RuntimeError.
      2. Se APP_ENV=production e COPILOT_INTERNAL_SECRET não configurado → RuntimeError.
    """
    app_env = os.getenv("APP_ENV", "development").lower()
    skip_auth = os.getenv("SKIP_AUTH_DEV", "false").lower() == "true"

    if app_env == "production" and skip_auth:
        raise RuntimeError(
            "FATAL: SKIP_AUTH_DEV=true é PROIBIDO em APP_ENV=production. "
            "Remova SKIP_AUTH_DEV ou defina-a como false antes de iniciar em produção."
        )

    if app_env == "production":
        try:
            secret_val = settings.copilot_internal_secret.get_secret_value()
            if not secret_val:
                raise RuntimeError(
                    "FATAL: COPILOT_INTERNAL_SECRET está vazio em APP_ENV=production. "
                    "Configure um secret HMAC forte antes de iniciar."
                )
        except Exception as exc:
            if isinstance(exc, RuntimeError):
                raise
            raise RuntimeError(
                "FATAL: COPILOT_INTERNAL_SECRET não configurado em APP_ENV=production. "
                f"Detalhe: {exc}"
            ) from exc

    log.info(
        "auth.startup_check",
        app_env=app_env,
        skip_auth_dev=skip_auth,
        hmac_configured=True,
    )


class AuthorizedSession(BaseModel):
    """Contexto de sessão autorizada — disponível em todos os handlers."""
    tenant_id: str
    session_id: str | None = None


def _validate_uuid(value: str, field_name: str) -> str:
    """Valida que o valor é um UUID bem-formado."""
    try:
        _uuid_module.UUID(value)
        return value
    except ValueError:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail=f"Campo '{field_name}' deve ser um UUID válido.",
        )


def _verify_hmac(
    tenant_id: str,
    timestamp: str,
    received_sig: str,
    secret: str,
) -> bool:
    """
    Verifica HMAC-SHA256 do header de autorização interna.
    Mensagem: "{tenant_id}:{timestamp}"
    Proteção contra replay: timestamp deve ser dentro de ±60 segundos.

    SEGURANÇA (H1):
      - A string sentinela "dev-skip" é EXPLICITAMENTE rejeitada.
      - Fail-closed: qualquer exceção → False.
    """
    # H1: nunca aceitar a sentinela "dev-skip" como assinatura
    if received_sig == "dev-skip":
        log.warning("auth.hmac_sentinel_rejected", reason="dev-skip sentinela rejeitada")
        return False

    try:
        ts = int(timestamp)
    except ValueError:
        return False

    now = int(time.time())
    if abs(now - ts) > 60:
        return False  # Replay

    message = f"{tenant_id}:{timestamp}".encode()
    expected = hmac.new(
        secret.encode(),
        message,
        hashlib.sha256,
    ).hexdigest()

    return hmac.compare_digest(expected, received_sig)


async def get_authorized_session(
    request: Request,
    settings: CopilotSettings = Depends(get_settings),
    x_tenant_id: Annotated[str | None, Header(alias="X-Tenant-ID")] = None,
    x_session_id: Annotated[str | None, Header(alias="X-Session-ID")] = None,
    x_internal_timestamp: Annotated[str | None, Header(alias="X-Internal-Timestamp")] = None,
    x_internal_signature: Annotated[str | None, Header(alias="X-Internal-Signature")] = None,
) -> AuthorizedSession:
    """
    Dependência FastAPI que extrai e valida tenant_id dos headers.

    Headers requeridos:
      X-Tenant-ID: UUID do tenant (injetado pelo BFF autenticado)
      X-Session-ID: ID da sessão do copiloto (opcional — para checkpointing)
      X-Internal-Timestamp: Unix timestamp da request (anti-replay)
      X-Internal-Signature: HMAC-SHA256(tenant_id:timestamp, COPILOT_INTERNAL_SECRET)

    Rejeita se:
      - X-Tenant-ID ausente ou inválido (400)
      - HMAC inválido ou expirado (401)
      - HMAC ausente (401)

    SKIP_AUTH_DEV=true desativa a verificação HMAC APENAS em dev/CI.
    É PROIBIDO em APP_ENV=production (check_auth_config_on_startup).
    A sentinela "dev-skip" é SEMPRE rejeitada independentemente do env.
    """
    skip_auth = os.getenv("SKIP_AUTH_DEV", "false").lower() == "true"

    if x_tenant_id is None:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="Header X-Tenant-ID é obrigatório.",
        )

    validated_tenant_id = _validate_uuid(x_tenant_id, "X-Tenant-ID")

    if not skip_auth:
        if x_internal_timestamp is None or x_internal_signature is None:
            raise HTTPException(
                status_code=status.HTTP_401_UNAUTHORIZED,
                detail="Headers X-Internal-Timestamp e X-Internal-Signature são obrigatórios.",
            )

        # H1: "dev-skip" é rejeitada no _verify_hmac mesmo com secret presente
        secret = settings.copilot_internal_secret.get_secret_value()
        if not _verify_hmac(
            validated_tenant_id,
            x_internal_timestamp,
            x_internal_signature,
            secret,
        ):
            raise HTTPException(
                status_code=status.HTTP_401_UNAUTHORIZED,
                detail="Assinatura interna inválida ou expirada.",
            )

    return AuthorizedSession(
        tenant_id=validated_tenant_id,
        session_id=x_session_id,
    )


# Dependência de atalho para uso nos handlers
RequireSession = Annotated[AuthorizedSession, Depends(get_authorized_session)]
