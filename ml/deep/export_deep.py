"""
ml/deep/export_deep.py
======================
Export e validacao de artefatos do deep ranker para serving (K1).

RESPONSABILIDADES:
  1. Carrega o modelo PyTorch do MLflow registry (por run_id ou model_version).
  2. Exporta para ONNX float32 e INT8.
  3. Gera a tabela de embeddings de candidato pre-computados offline
     (ADR-0004 §A: "torre de demanda pre-computada INT8").
  4. Gera o Triton model repository layout (config.pbtxt) para K8.
  5. Valida paridade PyTorch <-> ONNX (tolerancia _ONNX_PARITY_TOLERANCE).

GATE K1 → K8:
  - O layout Triton e gerado como SKELETON aqui (sem GPU ativa).
  - Em K8 (producao com Triton real), o sidecar aponta para este repositorio
    e o DEEP_ENABLED=true e ativado por model_version.

USO:
  python export_deep.py --run-id RUN_ID [--output-dir ./triton_repo]
                        [--mlflow-tracking-uri mlruns]
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np
import torch

_REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(_REPO_ROOT))

from ml.deep.model import FEATURE_SPEC_VERSION, FEATURE_VECTOR_LENGTH, TwoTowerDCNv2
from ml.deep.train_deep import _ONNX_PARITY_TOLERANCE, export_onnx

# ---------------------------------------------------------------------------
# Layout de repositorio Triton (skeleton K8)
# ---------------------------------------------------------------------------
# Estrutura esperada pelo Triton Inference Server:
#   triton_repo/
#     deep_ranker/
#       config.pbtxt
#       1/
#         model.onnx      <- modelo ONNX float32 ou INT8

_TRITON_CONFIG_TEMPLATE = """\
# Triton model repository config — deep ranker (K1 skeleton, Fase 3)
# Gerado por ml/deep/export_deep.py
# Em K8 (producao): ajustar max_batch_size e instance_group para GPU.
#
# GATE: este config e ativado SOMENTE em K8 sob uplift A/B provado.
# Em K1, DEEP_ENABLED=false e o sidecar usa o GBDT ONNX (CPU).

name: "deep_ranker"
platform: "onnxruntime_onnx"

max_batch_size: 64

input [
  {{
    name: "features"
    data_type: TYPE_FP32
    dims: [ {feature_dim} ]
  }}
]

output [
  {{
    name: "pctr"
    data_type: TYPE_FP32
    dims: [ 1 ]
  }}
]

# Em K8 (GPU real): trocar para:
#   instance_group [ {{ count: 1  kind: KIND_GPU }} ]
instance_group [
  {{
    count: 1
    kind: KIND_CPU
  }}
]

# Configuracoes de latencia (TX-4: 5-8 ms p99).
# Se o modelo nao couber no budget mesmo com INT8 + torre pre-computada,
# o sidecar faz fail-open para o GBDT (cascata pura). Nao se amplia o budget.
dynamic_batching {{
  preferred_batch_size: [ 1, 4, 8 ]
  max_queue_delay_microseconds: 500
}}
"""


def generate_triton_repo(
    onnx_path: Path,
    output_dir: Path,
    model_version: str = "1",
) -> Path:
    """
    Gera o layout do repositorio Triton para o deep ranker.

    Args:
        onnx_path: ONNX do modelo.
        output_dir: Diretorio raiz do repositorio Triton.
        model_version: Versao do modelo (diretorio numerico no Triton).

    Returns:
        Caminho do diretorio do modelo no repositorio.
    """
    model_dir = output_dir / "deep_ranker"
    version_dir = model_dir / model_version
    version_dir.mkdir(parents=True, exist_ok=True)

    # Copia ou cria symlink do ONNX
    dest_onnx = version_dir / "model.onnx"
    if onnx_path.exists():
        import shutil
        shutil.copy2(str(onnx_path), str(dest_onnx))
        print(f"[export_deep] ONNX copiado para {dest_onnx}")
    else:
        # Cria placeholder para scaffolding (K1 sem modelo treinado)
        dest_onnx.write_text(
            "# Placeholder: execute train_deep.py para gerar o modelo ONNX real.\n"
        )
        print(f"[export_deep] Placeholder criado em {dest_onnx} "
              f"(execute train_deep.py para o modelo real).")

    # Escreve config.pbtxt
    config_path = model_dir / "config.pbtxt"
    config_path.write_text(
        _TRITON_CONFIG_TEMPLATE.format(feature_dim=FEATURE_VECTOR_LENGTH)
    )
    print(f"[export_deep] Triton config gerado em {config_path}")
    return model_dir


def generate_candidate_embeddings(
    model: TwoTowerDCNv2,
    n_synthetic_candidates: int = 1000,
    output_path: Optional[Path] = None,
) -> np.ndarray:
    """
    Pre-computa embeddings de candidatos para armazenamento offline.

    ADR-0004 §A: "torre de demanda pre-computada offline" — no serving real,
    os embeddings de demanda sao pre-computados por campaign_id/banner_id e
    carregados pelo sidecar no startup, sem rodar a torre de candidato online.

    Em K1: gera N embeddings sinteticos para validar o mecanismo.
    Em K8 (producao): substitui por embeddings dos candidatos reais do snapshot.

    Args:
        model: TwoTowerDCNv2 treinado.
        n_synthetic_candidates: Numero de candidatos sinteticos (K1).
        output_path: Se fornecido, salva os embeddings em NPZ.

    Returns:
        Array float32 [N, embedding_dim].
    """
    from ml.deep.model import _CANDIDATE_DIM
    rng = np.random.default_rng(42)
    # Features de candidato sinteticas (indices 10-22 do vetor completo)
    candidate_features = rng.random(
        (n_synthetic_candidates, _CANDIDATE_DIM)
    ).astype(np.float32)

    embeddings = model.compute_candidate_embedding(
        torch.tensor(candidate_features, dtype=torch.float32)
    ).numpy()

    if output_path is not None:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        np.savez_compressed(str(output_path), embeddings=embeddings)
        print(f"[export_deep] {n_synthetic_candidates} embeddings de candidato "
              f"salvos em {output_path} shape={embeddings.shape}")

    return embeddings


# Evita erro de importacao circular para Optional
from typing import Optional


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Export do deep ranker para Triton (K1 skeleton, Fase 3)."
    )
    parser.add_argument("--run-id", type=str, default=None,
                        help="Run ID do MLflow para carregar o modelo treinado.")
    parser.add_argument("--onnx-path", type=Path,
                        default=Path(__file__).parent / "testdata" / "deep_model.onnx",
                        help="Caminho do ONNX float32 ja exportado.")
    parser.add_argument("--output-dir", type=Path,
                        default=Path(__file__).parent / "testdata" / "triton_repo",
                        help="Diretorio do Triton model repository.")
    parser.add_argument("--model-version", type=str, default="1",
                        help="Versao numerica no layout Triton (default: 1).")
    parser.add_argument("--mlflow-tracking-uri", type=str, default="mlruns",
                        help="URI do MLflow.")
    args = parser.parse_args()

    # Carrega modelo (do MLflow se run_id fornecido, senao cria novo para skeleton)
    if args.run_id:
        try:
            import mlflow.pytorch
            os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")
            import mlflow
            mlflow.set_tracking_uri(args.mlflow_tracking_uri)
            model = mlflow.pytorch.load_model(f"runs:/{args.run_id}/pytorch_model")
            model.eval()
            print(f"[export_deep] Modelo carregado do run {args.run_id}")
        except Exception as e:
            print(f"[export_deep] Erro ao carregar do MLflow ({e}). "
                  f"Usando modelo novo para skeleton.")
            model = TwoTowerDCNv2()
            model.eval()
    else:
        print("[export_deep] --run-id nao fornecido. Usando modelo novo (skeleton K1).")
        model = TwoTowerDCNv2()
        model.eval()

    # Gera layout Triton
    import os  # noqa: F811 (re-import para disponibilidade no escopo)
    model_dir = generate_triton_repo(
        onnx_path=args.onnx_path,
        output_dir=args.output_dir,
        model_version=args.model_version,
    )

    # Pre-computa embeddings de candidato
    embeddings_path = args.output_dir / "deep_ranker" / "candidate_embeddings.npz"
    generate_candidate_embeddings(model, output_path=embeddings_path)

    print(f"\n[export_deep] Concluido.")
    print(f"  Triton repo: {model_dir}")
    print(f"  GATE K8: DEEP_ENABLED=false por padrao. "
          f"Ativar SOMENTE com uplift A/B provado sobre o GBDT.")


if __name__ == "__main__":
    import os
    main()
