"""
services/ranker-sidecar/internal/triton/selector.py
====================================================
Logica de selecao de runtime do sidecar: ONNX-CPU (GBDT) vs. Triton/GPU (deep).

PRINCIPIO (ADR-0004 §A / ADR-0003 §B):
  O sidecar decide qual backend de inferencia usar com base no model_version
  do request. Este e o UNICO ponto onde a selecao ocorre — sem condicional
  espalhada pelo codigo.

  model_version prefixo "deep-*"  → TritonInferencer (K8, gated)
  qualquer outro                  → OnnxInferencer GBDT (producao atual)

GATE K1 → K8 (ADR-0004 §H):
  - Por padrao DEEP_ENABLED=false (var de ambiente).
  - Com DEEP_ENABLED=false, toda requisicao usa o GBDT independente do
    model_version — o selector simplesmente ignora o prefixo "deep-".
  - Com DEEP_ENABLED=true (K8, uplift A/B provado), o selector usa
    TritonInferencer para model_version com prefixo "deep-".

FAIL-OPEN (TX-4 / DA-3):
  - Se o TritonInferencer nao conseguir conectar ao servidor Triton
    (GPU nao disponivel, servidor nao iniciado), retorna score=0.
  - O cliente Go (internal/ranker/ipc.go) trata score=0 identico a
    fail-open: a cascata pura preserva a ordem deterministia.
  - Nunca ha impressao em branco por falha de ML.
"""
from __future__ import annotations

import os
from typing import Optional

# ---------------------------------------------------------------------------
# Flag de gate
# ---------------------------------------------------------------------------

def is_deep_enabled() -> bool:
    """
    Retorna True se DEEP_ENABLED=true (var de ambiente).

    DEFAULT: False (DEEP_ENABLED nao definida ou diferente de "true").
    Gate aberto: ativado em K8 por operador apos prova de uplift A/B.
    """
    return os.environ.get("DEEP_ENABLED", "false").lower() == "true"


def is_deep_model_version(model_version: str) -> bool:
    """
    Retorna True se o model_version indica um modelo deep.

    Convencao: model_version com prefixo "deep-" e um modelo deep ranker.
    Exemplos:
        "deep-1.0.0"         → True
        "deep-two-tower-v1"  → True
        "deep-k1-scaffold"   → True
        "stub-j1"            → False
        "pctr_lgb_v3"        → False
        ""                   → False
    """
    return model_version.startswith("deep-")


def should_use_deep(model_version: str) -> bool:
    """
    Retorna True se o sidecar deve usar o TritonInferencer para este request.

    Condicoes (AND):
      1. DEEP_ENABLED=true
      2. model_version tem prefixo "deep-"

    Com DEEP_ENABLED=false (default), SEMPRE retorna False independente do
    model_version — o GBDT ONNX e o backend de producao.
    """
    return is_deep_enabled() and is_deep_model_version(model_version)


# ---------------------------------------------------------------------------
# Inferencer interface (reusa stub.Inferencer da Fase 2)
# ---------------------------------------------------------------------------
# A interface stub.Inferencer define:
#   Score(features: list[float]) -> (float, error)
#   ModelVersion() -> str
#   Close() -> error
#
# TritonInferencer implementa esta mesma interface, permitindo que o
# servidor (stub.Server) use qualquer backend sem mudanca de codigo.


class TritonInferencer:
    """
    Inferencer que serve o deep ranker via Triton Inference Server (GPU).

    K1 SKELETON: sem Triton/GPU disponivel neste ambiente, este inferencer
    opera em modo stub (retorna score=0 com log de aviso).

    K8 (producao): substituir o stub por cliente grpc real do Triton.
    Instalar: pip install tritonclient[grpc]

    FAIL-OPEN (TX-4): qualquer erro de conexao/inferencia retorna 0.0
    (nao levanta excecao). O cliente Go trata 0 como fail-open.

    Args:
        triton_url: URL do servidor Triton (ex.: "localhost:8001" gRPC).
        model_name: Nome do modelo no repositorio Triton (ex.: "deep_ranker").
        model_version: Versao do modelo (ex.: "1").
        feature_spec_version: Versao da spec de features (anti-skew).
    """

    def __init__(
        self,
        triton_url: str = "localhost:8001",
        model_name: str = "deep_ranker",
        model_version: str = "1",
        feature_spec_version: str = "1.0.0",
    ) -> None:
        self._triton_url = triton_url
        self._model_name = model_name
        self._model_version = model_version
        self._feature_spec_version = feature_spec_version
        self._client: Optional[object] = None
        self._stub_mode = True  # K1: sem Triton real

        self._try_connect()

    def _try_connect(self) -> None:
        """
        Tenta conectar ao servidor Triton.
        Em K1 (sem Triton instalado/ativo), registra aviso e permanece em stub.
        """
        try:
            import tritonclient.grpc as grpcclient  # type: ignore
            client = grpcclient.InferenceServerClient(url=self._triton_url)
            # Verifica disponibilidade (pode falhar se Triton nao estiver ativo)
            if client.is_server_ready():
                self._client = client
                self._stub_mode = False
                print(
                    f"[TritonInferencer] Conectado ao servidor Triton em "
                    f"{self._triton_url} (modelo: {self._model_name})"
                )
            else:
                print(
                    f"[TritonInferencer] Triton em {self._triton_url} nao pronto — "
                    f"modo stub (fail-open). DEEP_ENABLED=true mas Triton inativo."
                )
        except ImportError:
            print(
                "[TritonInferencer] tritonclient nao instalado (K1 skeleton) — "
                "modo stub. Para K8: pip install tritonclient[grpc]"
            )
        except Exception as e:
            print(
                f"[TritonInferencer] Erro ao conectar ao Triton ({e}) — "
                f"modo stub (fail-open)."
            )

    def score(self, features: list) -> float:
        """
        Envia features para o Triton e retorna pCTR em [0, 1].

        K1 stub: retorna 0.0 (fail-open semantics).
        K8 producao: usa grpcclient.InferInput para invocar o modelo.

        Args:
            features: lista de 23 floats (spec 1.0.0).

        Returns:
            pCTR em [0, 1]. Retorna 0.0 em qualquer erro (fail-open).
        """
        if self._stub_mode or self._client is None:
            # K1: sem Triton real → fail-open
            return 0.0

        # K8 (Triton real): codigo abaixo e o caminho de producao
        try:
            import numpy as np
            import tritonclient.grpc as grpcclient  # type: ignore

            x = np.array([features], dtype=np.float32)  # [1, 23]
            infer_input = grpcclient.InferInput("features", x.shape, "FP32")
            infer_input.set_data_from_numpy(x)

            result = self._client.infer(  # type: ignore
                model_name=self._model_name,
                inputs=[infer_input],
                model_version=self._model_version,
                outputs=[grpcclient.InferRequestedOutput("pctr")],
            )
            pctr = float(result.as_numpy("pctr").flatten()[0])
            # Clamp defensivo [0, 1]
            return max(0.0, min(1.0, pctr))
        except Exception as e:
            # Fail-open: qualquer erro → 0.0 (cascata pura)
            print(f"[TritonInferencer] Erro de inferencia ({e}) — fail-open (score=0).")
            return 0.0

    def model_version(self) -> str:
        """Retorna a string de versao do modelo (prefixo 'deep-')."""
        return f"deep-{self._model_version}"

    def close(self) -> None:
        """Libera recursos do cliente Triton."""
        if self._client is not None:
            try:
                self._client.close()  # type: ignore
            except Exception:
                pass
            self._client = None
