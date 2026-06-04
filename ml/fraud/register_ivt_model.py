"""
ml/fraud/register_ivt_model.py
================================
Registro do modelo IVT no MLflow Model Registry sob nome 'ivt_classifier'.

POLITICA:
  - Modelo registrado como stage 'None' (nao promovido automaticamente).
  - Promocao para 'Staging'/'Production' e MANUAL apos validacao qualidade/custo.
  - NUNCA usar nome 'pctr_lgb' — ivt_classifier e modelo distinto.
  - Tags de TX-6 compliance e PII-free gravadas para auditoria.
"""
from __future__ import annotations

import os
import sys
from pathlib import Path
from typing import Optional

os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

import mlflow
from mlflow import MlflowClient

_REPO_ROOT = Path(__file__).parent.parent.parent
_ML_ROOT = Path(__file__).parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.fraud.features import IVT_FEATURE_VERSION  # noqa: E402
from ml.fraud.train_ivt import IVT_MODEL_NAME  # noqa: E402

IVT_MODEL_NAME_REGISTRY = IVT_MODEL_NAME  # "ivt_classifier"


def register_ivt_model(
    run_id: str,
    mlflow_tracking_uri: Optional[str] = None,
    description: Optional[str] = None,
) -> str:
    """
    Registra o modelo IVT do run_id no MLflow registry como 'ivt_classifier'.

    Retorna o model_version (string).

    NUNCA sobrepoem artefatos 'pctr_lgb' — modelos com nomes distintos sao
    completamente independentes no registry do MLflow.
    """
    if mlflow_tracking_uri:
        mlflow.set_tracking_uri(mlflow_tracking_uri)

    client = MlflowClient()
    run = client.get_run(run_id)
    model_uri = f"runs:/{run_id}/model"

    print(f"[register_ivt] Registrando run_id={run_id} como '{IVT_MODEL_NAME_REGISTRY}'")

    try:
        client.create_registered_model(
            name=IVT_MODEL_NAME_REGISTRY,
            description=description or (
                f"Classificador IVT supervisionado (TX-6). "
                f"LightGBM ONNX. ivt_feature_version={IVT_FEATURE_VERSION}. "
                f"Fraude fora do hot path. PII-free (TX-5/DA-11)."
            ),
        )
        print(f"[register_ivt] Modelo '{IVT_MODEL_NAME_REGISTRY}' criado no registry.")
    except Exception:
        print(f"[register_ivt] Modelo '{IVT_MODEL_NAME_REGISTRY}' ja existe. Nova versao.")

    mv = client.create_model_version(
        name=IVT_MODEL_NAME_REGISTRY,
        source=model_uri,
        run_id=run_id,
        description=(
            f"IVT LightGBM ONNX. ivt_feature_version={IVT_FEATURE_VERSION}. "
            f"TX-6: fraude fora do hot path. TX-5: PII-free. "
            f"Promocao manual apos validacao de qualidade/custo."
        ),
    )

    model_version = mv.version
    client.set_model_version_tag(
        IVT_MODEL_NAME_REGISTRY, model_version,
        "ivt_feature_version", IVT_FEATURE_VERSION,
    )
    client.set_model_version_tag(
        IVT_MODEL_NAME_REGISTRY, model_version,
        "promotion_gate", "pending_quality_cost_validation",
    )
    client.set_model_version_tag(
        IVT_MODEL_NAME_REGISTRY, model_version,
        "tx6_compliant", "true",
    )
    client.set_model_version_tag(
        IVT_MODEL_NAME_REGISTRY, model_version,
        "pii_free", "true",
    )
    client.set_model_version_tag(
        IVT_MODEL_NAME_REGISTRY, model_version,
        "model_type", run.data.tags.get("model_type", "lightgbm_ivt_onnx"),
    )

    print(f"[register_ivt] Versao {model_version} registrada. "
          f"Stage: None (aguarda promocao manual).")
    return model_version


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Registra modelo IVT no MLflow")
    parser.add_argument("--run-id", required=True)
    parser.add_argument(
        "--mlflow-tracking-uri",
        default="file:" + str(_ML_ROOT / "registry" / "mlruns"),
    )
    args = parser.parse_args()

    version = register_ivt_model(
        run_id=args.run_id,
        mlflow_tracking_uri=args.mlflow_tracking_uri,
    )
    print(f"[done] ivt_classifier v{version} registrado.")
