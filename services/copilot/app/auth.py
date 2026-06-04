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
"""

from __future__ import annotations

import hashlib
import hmac
import time
import uuid as _uuid_module
from typing import Annotated

from fastapi import Depends, Header, HTTPException, Request, status
from pydantic import BaseModel

from app.config import CopilotSettings, get_settings


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
    """
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

    NOTA: Em dev/CI, se SKIP_AUTH_DEV=true nas variáveis de ambiente,
    o HMAC é ignorado (apenas para testes locais; nunca em produção).
    """
    import os

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
