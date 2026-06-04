"""
ml/training/pipeline.py
========================
Pipeline integrado J2: treino + calibracao + registro MLflow.

Orquestra em sequencia:
  1. train_pctr.run()         — treino LightGBM, exporta ONNX, registra run MLflow
  2. calibrate.run_calibration() — calibracao isotonica, calcula ECE, salva mapa JSON
  3. register_model.*         — registra artefatos de calibracao no run MLflow
                               — registra versao no Model Registry (stage "None", aguarda J4)

PONTO EXATO DE eCPM = pCTR × bid (para o money-ledger-guardian):
  Este pipeline NAO computa eCPM. O ponto de multiplicacao e:
    internal/ranker/score.go — funcao ScoreCandidate():
      ecpm_minor_units = int64(math.Round(float64(pCTR_cal) * float64(bid_minor_units)))
  Onde:
    - pCTR_cal: float32 [0,1] — saida calibrada do sidecar ONNX
    - bid_minor_units: int64 — valor do lance em minor-units (TX-2)
    - ecpm_minor_units: int64 — resultado em minor-units (nunca float)
  O guardiao money-ledger-guardian audita internal/ranker/score.go, nao este modulo.
  Nenhum float monetario e gerado aqui.

USO:
  python pipeline.py [--n-estimators N] [--mlflow-tracking-uri URI]
"""
from __future__ import annotations

import os
import sys
from pathlib import Path

# MLflow file-store mode: requerido em MLflow >= 3.x para usar file-store local.
# Em producao, substituir por mlflow_tracking_uri="sqlite:///mlflow.db" ou URI remota.
# Ver ml/registry/register_model.py para documentacao de modo de operacao.
os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

_REPO_ROOT = Path(__file__).parent.parent.parent
_ML_ROOT = Path(__file__).parent.parent
sys.path.insert(0, str(_REPO_ROOT))


def run_full_pipeline(
    n_estimators: int = 300,
    seed: int = 42,
    mlflow_tracking_uri: str | None = None,
    run_name: str = "pctr_lgb_v1",
    register: bool = True,
) -> dict:
    """
    Pipeline completo J2:
      treino → calibracao → registro MLflow
    """
    if mlflow_tracking_uri is None:
        mlflow_tracking_uri = "file:" + str(_ML_ROOT / "registry" / "mlruns")

    # --- PASSO 1: Treino + exporta ONNX ---
    from ml.training.train_pctr import run as train_run, load_data, df_to_feature_matrix_fast
    import numpy as np

    result = train_run(
        n_estimators=n_estimators,
        seed=seed,
        mlflow_tracking_uri=mlflow_tracking_uri,
        run_name=run_name,
    )
    run_id = result["run_id"]
    lgb_path = result["lgb_path"]
    onnx_path = result["onnx_path"]

    # --- PASSO 2: Calibracao isotonica ---
    import lightgbm as lgb
    import mlflow
    mlflow.set_tracking_uri(mlflow_tracking_uri)

    booster = lgb.Booster(model_file=lgb_path)

    # Gera conjunto de calibracao separado (diferente do val do treino)
    # Em producao: usar um conjunto de calibracao Iceberg distinto do treino/val
    df_cal = load_data(seed=seed + 1)  # seed diferente para conjunto separado
    from sklearn.model_selection import train_test_split
    _, df_cal_split = train_test_split(df_cal, test_size=0.2, random_state=seed + 1,
                                       stratify=df_cal["label_click"])

    X_cal = df_to_feature_matrix_fast(df_cal_split)
    y_cal = df_cal_split["label_click"].to_numpy(dtype=np.float32)

    from ml.calibration.calibrate import run_calibration
    from ml.features.python.featurize import FEATURE_SPEC_VERSION

    cal_artifacts_dir = _ML_ROOT / "registry" / "artifacts"
    cal_result = run_calibration(
        booster=booster,
        X_cal=X_cal,
        y_cal=y_cal,
        output_path=cal_artifacts_dir / "calibration_map.json",
        mlflow_run_id=run_id,
        feature_spec_version=FEATURE_SPEC_VERSION,
    )

    print(f"\n[pipeline] ECE antes={cal_result['ece_before']:.4f}, "
          f"ECE apos={cal_result['ece_after']:.4f}")

    # --- PASSO 3: Registra calibracao no run MLflow ---
    from ml.registry.register_model import log_calibration_artifacts, register_pctr_model

    log_calibration_artifacts(
        run_id=run_id,
        calibration_result=cal_result,
        mlflow_tracking_uri=mlflow_tracking_uri,
    )

    # Log artefato ONNX adicional com tag de calibracao atualizada
    import mlflow as _mlflow
    _mlflow.set_tracking_uri(mlflow_tracking_uri)
    from mlflow import MlflowClient
    client = MlflowClient()
    client.log_artifact(run_id, str(cal_artifacts_dir / "calibration_map.json"),
                        artifact_path="calibration")

    # --- PASSO 4: Registra versao no Model Registry (stage "None") ---
    model_version = None
    if register:
        model_version = register_pctr_model(
            run_id=run_id,
            mlflow_tracking_uri=mlflow_tracking_uri,
        )

    print("\n" + "=" * 60)
    print("[pipeline] J2 COMPLETO")
    print(f"  run_id:          {run_id}")
    print(f"  model_version:   {model_version or 'nao registrado'}")
    print(f"  AUC:             {result['metrics']['val_auc']:.4f}")
    print(f"  logloss:         {result['metrics']['val_logloss']:.4f}")
    print(f"  ECE antes:       {cal_result['ece_before']:.4f}")
    print(f"  ECE apos:        {cal_result['ece_after']:.4f}")
    print(f"  ONNX:            {onnx_path}")
    print(f"  calibration_map: {cal_result['output_path']}")
    print("=" * 60)
    print("PROXIMOS PASSOS:")
    print("  J3: OPE shadow (ml/ope/) — requer J1 (ranker sidecar ativo)")
    print("  J4: A/B uplift + kill-switch — UNICO gate de promocao para Production")
    print("  Promocao: MANUAL via mlflow ui ou register_model.py --promote")
    print("=" * 60)

    return {
        "run_id": run_id,
        "model_version": model_version,
        "metrics": result["metrics"],
        "ece_before": cal_result["ece_before"],
        "ece_after": cal_result["ece_after"],
        "onnx_path": onnx_path,
        "calibration_map_path": cal_result["output_path"],
        "feature_spec_version": FEATURE_SPEC_VERSION,
    }


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Pipeline J2: treino pCTR + calibracao + MLflow")
    parser.add_argument("--n-estimators", type=int, default=300)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument(
        "--mlflow-tracking-uri",
        default=None,
        help="URI do MLflow. Padrao: file-store local em ml/registry/mlruns",
    )
    parser.add_argument("--run-name", default="pctr_lgb_v1")
    parser.add_argument("--no-register", action="store_true",
                        help="Pula registro no Model Registry (apenas tracking run)")
    args = parser.parse_args()

    run_full_pipeline(
        n_estimators=args.n_estimators,
        seed=args.seed,
        mlflow_tracking_uri=args.mlflow_tracking_uri,
        run_name=args.run_name,
        register=not args.no_register,
    )
