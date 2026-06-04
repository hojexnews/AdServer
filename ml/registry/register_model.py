"""
ml/registry/register_model.py
================================
Integracao MLflow: registra o modelo pCTR (ONNX + calibracao) no registry.

MODO DE OPERACAO:
  MLflow pode rodar em dois modos:
  a) LOCAL FILE-STORE (padrao neste ambiente):
       mlflow_tracking_uri = "file:./ml/registry/mlruns"
       O registry e armazenado localmente em ml/registry/mlruns/.
       Inspecionar com: mlflow ui --backend-store-uri file:./ml/registry/mlruns
  b) SERVIDOR REMOTO (producao):
       mlflow_tracking_uri = "http://mlflow-server:5000"
       Exige servidor MLflow rodando e object storage configurado.

POLITICA DE PROMOCAO (J4 — nao J2):
  A promocao de model_version para "Production" e MANUAL e gated (J4).
  Este modulo apenas REGISTRA e VERSIONA o modelo como "Staging".
  Promocao requer: calibracao monitorada OK + uplift A/B provado + kill-switch.
  Nunca promover automaticamente aqui.

CONTRATO DE ARTEFATO PARA O SIDECAR (services/ranker-sidecar):
  O sidecar carrega os artefatos via MLflow registry:
    - pctr_model.onnx        — modelo LightGBM convertido para ONNX
    - calibration_map.json   — mapa de calibracao isotonica versionado
    - feature_spec_version   — tag gravada no run e na model version
  O sidecar usa model_version (string do registry) para preencher
  Decision.model_version no proto (propensity-logging.md).

ESTRUTURA DOS RUNS MLflow:
  Experimento: "pctr_lgb"
  Run params:
    - model_type, n_estimators, val_fraction, seed
    - feature_spec_version ("1.0.0")
    - n_features (23)
    - data_source (synthetic_sample | iceberg)
    - window_start, window_end
    - n_train, n_val
  Run metrics:
    - val_auc, val_logloss
    - best_iteration
    - ece_before, ece_after, ece_improvement
    - onnx_max_abs_diff, onnx_mean_abs_diff
  Run tags:
    - feature_spec_version
    - model_type = "lightgbm_onnx"
    - calibration_status = "isotonic_calibrated" | "pending_isotonic"
  Artefatos (artifact_path):
    - model/pctr_model.onnx
    - model/pctr_booster.lgb
    - calibration/calibration_map.json
    - calibration/reliability_before.json
    - calibration/reliability_after.json
"""
from __future__ import annotations

import json
import os
import sys
from pathlib import Path
from typing import Optional

os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

import mlflow
import mlflow.lightgbm
from mlflow import MlflowClient

_REPO_ROOT = Path(__file__).parent.parent.parent
_ML_ROOT = Path(__file__).parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.features.python.featurize import FEATURE_SPEC_VERSION  # noqa: E402


# ---------------------------------------------------------------------------
# Registro do modelo no MLflow Model Registry
# ---------------------------------------------------------------------------

def register_pctr_model(
    run_id: str,
    model_name: str = "pctr_lgb",
    mlflow_tracking_uri: Optional[str] = None,
    description: Optional[str] = None,
) -> str:
    """
    Registra o modelo ONNX do run_id no MLflow Model Registry.

    Politica de promocao:
      - Versao criada com stage "None" (nao "Staging" nem "Production").
      - Promocao para "Staging" / "Production" e MANUAL via J4.
      - NUNCA promover automaticamente aqui.

    Retorna o model_version (string).
    """
    if mlflow_tracking_uri:
        mlflow.set_tracking_uri(mlflow_tracking_uri)

    client = MlflowClient()

    # Verifica se o experimento existe
    run = client.get_run(run_id)
    artifact_uri = run.info.artifact_uri
    model_uri = f"runs:/{run_id}/model"

    print(f"[register] Registrando run_id={run_id} como modelo '{model_name}'")
    print(f"[register] artifact_uri={artifact_uri}")

    # Cria ou recupera o modelo registrado
    try:
        client.create_registered_model(
            name=model_name,
            description=description or f"pCTR LightGBM ONNX — feature_spec_version={FEATURE_SPEC_VERSION}",
        )
        print(f"[register] Modelo '{model_name}' criado no registry.")
    except Exception:
        print(f"[register] Modelo '{model_name}' ja existe no registry. Criando nova versao.")

    # Cria nova versao
    mv = client.create_model_version(
        name=model_name,
        source=model_uri,
        run_id=run_id,
        description=(
            f"LightGBM ONNX pCTR. feature_spec_version={FEATURE_SPEC_VERSION}. "
            f"Promocao para Staging/Production requer J4 (A/B uplift + kill-switch)."
        ),
    )

    model_version = mv.version
    print(f"[register] Versao criada: {model_name} v{model_version}")

    # Adiciona tags na versao
    client.set_model_version_tag(model_name, model_version, "feature_spec_version", FEATURE_SPEC_VERSION)
    client.set_model_version_tag(model_name, model_version, "promotion_gate", "pending_J4_AB_test")
    client.set_model_version_tag(model_name, model_version, "calibration_status",
                                 run.data.tags.get("calibration_status", "unknown"))

    print(f"[register] Tags adicionadas. Versao {model_version} em stage 'None' (aguarda J4).")
    return model_version


def log_calibration_artifacts(
    run_id: str,
    calibration_result: dict,
    mlflow_tracking_uri: Optional[str] = None,
) -> None:
    """
    Registra os artefatos de calibracao em um run MLflow existente.

    Deve ser chamado apos run_calibration() de ml/calibration/calibrate.py.
    Atualiza o run com:
      - metricas ece_before, ece_after, ece_improvement
      - artefatos calibration_map.json, reliability_*.json
      - tag calibration_status = "isotonic_calibrated"
    """
    if mlflow_tracking_uri:
        mlflow.set_tracking_uri(mlflow_tracking_uri)

    client = MlflowClient()

    # Log metricas de calibracao no run
    client.log_metric(run_id, "ece_before", calibration_result["ece_before"])
    client.log_metric(run_id, "ece_after", calibration_result["ece_after"])
    client.log_metric(run_id, "ece_improvement", calibration_result["ece_improvement"])
    client.log_metric(run_id, "n_calibration_points", calibration_result["n_calibration_points"])

    # Atualiza tag de status de calibracao
    client.set_tag(run_id, "calibration_status", "isotonic_calibrated")

    # Log artefato calibration_map.json
    cal_map_path = calibration_result.get("output_path")
    if cal_map_path and Path(cal_map_path).exists():
        client.log_artifact(run_id, cal_map_path, artifact_path="calibration")
        print(f"[register] Artefato calibration_map.json registrado no run {run_id}")

    # Log reliability diagrams como JSON
    for key in ("reliability_before", "reliability_after"):
        rel = calibration_result.get(key)
        if rel:
            tmp_path = _ML_ROOT / "registry" / "artifacts" / f"{key}.json"
            tmp_path.parent.mkdir(parents=True, exist_ok=True)
            with open(tmp_path, "w") as f:
                json.dump(rel, f, indent=2)
            client.log_artifact(run_id, str(tmp_path), artifact_path="calibration")

    print(f"[register] Calibracao registrada. ECE={calibration_result['ece_after']:.4f}")


def get_latest_model_info(
    model_name: str = "pctr_lgb",
    mlflow_tracking_uri: Optional[str] = None,
) -> Optional[dict]:
    """
    Retorna informacoes da versao mais recente do modelo no registry.

    Retorna None se nenhum modelo estiver registrado ainda.
    """
    if mlflow_tracking_uri:
        mlflow.set_tracking_uri(mlflow_tracking_uri)

    client = MlflowClient()

    try:
        versions = client.search_model_versions(f"name='{model_name}'")
        if not versions:
            return None
        # Ordena por versao (maior = mais recente)
        latest = sorted(versions, key=lambda v: int(v.version), reverse=True)[0]
        return {
            "model_name": model_name,
            "model_version": latest.version,
            "run_id": latest.run_id,
            "current_stage": latest.current_stage,
            "feature_spec_version": latest.tags.get("feature_spec_version", "unknown"),
            "promotion_gate": latest.tags.get("promotion_gate", "unknown"),
            "calibration_status": latest.tags.get("calibration_status", "unknown"),
        }
    except Exception as e:
        print(f"[registry] Erro ao consultar modelo '{model_name}': {e}")
        return None


# ---------------------------------------------------------------------------
# CLI de registro
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Registra modelo pCTR no MLflow registry")
    parser.add_argument("--run-id", required=True, help="MLflow run_id do treino")
    parser.add_argument("--model-name", default="pctr_lgb")
    parser.add_argument(
        "--mlflow-tracking-uri",
        default="file:" + str(_ML_ROOT / "registry" / "mlruns"),
    )
    args = parser.parse_args()

    version = register_pctr_model(
        run_id=args.run_id,
        model_name=args.model_name,
        mlflow_tracking_uri=args.mlflow_tracking_uri,
    )
    print(f"[done] Modelo registrado como versao {version}")
    print("LEMBRETE: promocao para Staging/Production requer gate J4 (A/B uplift + kill-switch).")
