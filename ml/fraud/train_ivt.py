"""
ml/fraud/train_ivt.py
======================
Pipeline de treino do classificador IVT supervisionado (LightGBM).

RESPONSABILIDADES:
  1. Carrega dados de treino (Iceberg em producao; Parquet sintetico aqui).
  2. Featuriza via featurize_ivt() de ml/fraud/features.py (ANTI-SKEW).
  3. Treina LightGBM binario (ivt_label 0=clean, 1=IVT).
  4. Exporta para .onnx via onnxmltools.
  5. Valida numericamente ONNX vs. LightGBM.
  6. Registra no MLflow sob nome DISTINTO: 'ivt_classifier' (NAO 'pctr_lgb').

TX-6 (fraude fora do hot path):
  Este modelo e invocado na INGESTAO (batch/near-line), antes do
  StatsHourly e do faturamento. NAO e invocado no hot path de decisao.
  Latencia de minutos e aceitavel (ADR-0001/TX-6).

TX-2 (dinheiro em minor-units int64):
  Nenhum valor monetario e computado neste pipeline. ivt_label e uma
  classificacao binaria; nenhum campo monetario entra nas features.

TX-5/DA-11 (PII-free):
  Features IVT sao PII-free (ver ml/fraud/features.py para justificativas).
  Sem IP bruto, sem ID de usuario, sem UA completo.

NOME DO MODELO NO MLflow: 'ivt_classifier'
  NUNCA usar 'pctr_lgb' ou qualquer nome que colida com o modelo de ranking.
  O registry distingue modelos por nome — ivt_classifier e pctr_lgb sao
  modelos independentes com semanticas distintas.

USO:
  python train_ivt.py [--data-path PATH] [--n-estimators N]
                      [--mlflow-tracking-uri URI] [--run-name NAME]
"""
from __future__ import annotations

import argparse
import os
import sys
import time
from pathlib import Path
from typing import Optional

os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

import mlflow
import numpy as np
import pandas as pd

_REPO_ROOT = Path(__file__).parent.parent.parent
_ML_ROOT = Path(__file__).parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.fraud.features import (  # noqa: E402
    IVT_FEATURE_NAMES,
    IVT_FEATURE_VERSION,
    IVT_VECTOR_LENGTH,
    IVTEventInput,
    featurize_ivt,
)
from ml.features.python.featurize import GeoInput, RequestTimeInput  # noqa: E402

import lightgbm as lgb  # noqa: E402
import onnx  # noqa: E402
import onnxmltools  # noqa: E402
from onnxmltools.convert.common.data_types import FloatTensorType  # noqa: E402
from sklearn.metrics import f1_score, log_loss, roc_auc_score  # noqa: E402
from sklearn.model_selection import train_test_split  # noqa: E402

# MLflow model name — DISTINTO de pctr_lgb (nunca sobrescrever artefatos pCTR)
IVT_MODEL_NAME = "ivt_classifier"


# ---------------------------------------------------------------------------
# 1. DATA LOADING
# ---------------------------------------------------------------------------

def load_ivt_data(
    data_path: Optional[Path] = None,
    n_sample: int = 30_000,
    seed: int = 42,
) -> pd.DataFrame:
    """
    Carrega o dataset de treino IVT.

    PRODUCAO:
        import pyiceberg.catalog
        catalog = pyiceberg.catalog.load_catalog("glue", **cfg)
        tbl = catalog.load_table("adserver.raw_impressions_with_context")
        df = tbl.scan(
            row_filter="hour_bucket >= '...' AND hour_bucket < '...'"
        ).to_pandas()
        # A tabela deve ter a coluna ivt_status (label) derivada de regras
        # de ingestao (is_bot, listas negras, etc.) ja aplicadas.
        # Reconciliacao e batch (TX-6); nao streaming.

    DEV / SAMPLE:
        Usa Parquet sintetico de testdata/sample_ivt.parquet.
    """
    if data_path is not None and Path(data_path).exists():
        print(f"[load_ivt_data] Lendo: {data_path}")
        df = pd.read_parquet(str(data_path), engine="pyarrow")
    else:
        sample_path = Path(__file__).parent / "testdata" / "sample_ivt.parquet"
        if not sample_path.exists():
            print("[load_ivt_data] Gerando sample IVT sintetico...")
            from ml.fraud.generate_ivt_sample import generate_ivt_sample
            df = generate_ivt_sample(n_rows=n_sample, seed=seed,
                                      output_path=sample_path)
        else:
            print(f"[load_ivt_data] Carregando sample existente: {sample_path}")
            df = pd.read_parquet(str(sample_path), engine="pyarrow")

    print(f"[load_ivt_data] {len(df)} linhas. "
          f"IVT rate: {df['ivt_label'].mean():.3f}")
    return df


# ---------------------------------------------------------------------------
# 2. FEATURIZACAO IVT (anti-skew: funcao unica)
# ---------------------------------------------------------------------------

def df_to_ivt_feature_matrix(df: pd.DataFrame) -> np.ndarray:
    """
    Featuriza o DataFrame IVT via featurize_ivt() de ml/fraud/features.py.

    ANTI-SKEW: esta e a UNICA funcao de featurizacao IVT.
    NAO reimplementar transformacoes aqui.

    Retorna ndarray float32 de shape (len(df), 10).
    """
    # Extrai colunas de historico recente (h00..h23)
    recent_cols = sorted([c for c in df.columns if c.startswith("recent_h")])

    vectors = []
    for _, row in df.iterrows():
        recent = [int(row[c]) for c in recent_cols] if recent_cols else None
        inp = IVTEventInput(
            device_class=str(row["device_class"]),
            request_time_utc=RequestTimeInput(
                hour=int(row["hour_of_day"]),
                day_of_week=int(row["day_of_week"]),
            ),
            geo=GeoInput(
                country=str(row["geo_country"]),
                city=str(row.get("geo_city", "")),
            ),
            hourly_requests=int(row.get("hourly_requests", 0)),
            hourly_impressions=int(row.get("hourly_impressions", 0)),
            hourly_clicks=int(row.get("hourly_clicks", 0)),
            recent_hourly_impressions=recent,
        )
        vectors.append(featurize_ivt(inp))

    X = np.vstack(vectors).astype(np.float32)
    assert X.shape == (len(df), IVT_VECTOR_LENGTH), f"Shape inesperado: {X.shape}"
    return X


# ---------------------------------------------------------------------------
# 3. TREINO LightGBM IVT
# ---------------------------------------------------------------------------

def train_ivt_lgb(
    X_train: np.ndarray,
    y_train: np.ndarray,
    X_val: np.ndarray,
    y_val: np.ndarray,
    n_estimators: int = 200,
    learning_rate: float = 0.05,
    num_leaves: int = 31,
    seed: int = 42,
    class_weight: str = "balanced",
) -> lgb.Booster:
    """
    Treina LightGBM para classificacao binaria IVT.

    class_weight='balanced': IVT e evento raro na maioria dos contextos reais;
    balancear via is_unbalance ou scale_pos_weight melhora recall de fraude.
    """
    # Calcula scale_pos_weight para classes desbalanceadas
    n_neg = int((y_train == 0).sum())
    n_pos = int((y_train == 1).sum())
    spw = n_neg / max(1, n_pos)
    print(f"[train_ivt_lgb] n_pos={n_pos}, n_neg={n_neg}, scale_pos_weight={spw:.2f}")

    dtrain = lgb.Dataset(X_train, label=y_train, feature_name=IVT_FEATURE_NAMES)
    dval = lgb.Dataset(X_val, label=y_val, reference=dtrain,
                       feature_name=IVT_FEATURE_NAMES)

    params = {
        "objective": "binary",
        "metric": ["binary_logloss", "auc"],
        "num_leaves": num_leaves,
        "learning_rate": learning_rate,
        "feature_fraction": 0.8,
        "bagging_fraction": 0.8,
        "bagging_freq": 5,
        "min_child_samples": 10,
        "scale_pos_weight": spw if class_weight == "balanced" else 1.0,
        "seed": seed,
        "verbose": -1,
        "num_threads": 4,
    }

    t0 = time.perf_counter()
    callbacks = [lgb.early_stopping(30, verbose=False), lgb.log_evaluation(50)]
    booster = lgb.train(
        params,
        dtrain,
        num_boost_round=n_estimators,
        valid_sets=[dval],
        callbacks=callbacks,
    )
    elapsed = time.perf_counter() - t0
    print(f"[train_ivt_lgb] Treino completo em {elapsed:.1f}s. "
          f"Melhores iteracoes: {booster.best_iteration}")
    return booster


# ---------------------------------------------------------------------------
# 4. CONVERSAO ONNX (mesmo padrao de train_pctr.py)
# ---------------------------------------------------------------------------

def export_ivt_onnx(booster: lgb.Booster, output_path: Path) -> onnx.ModelProto:
    """
    Converte o classificador IVT LightGBM para ONNX.

    CONTRATO DE ARTEFATO (para o scorer de ingestao):
      Input name:  "features"
      Input dtype: float32
      Input shape: [N, 10]  (N = 1 em scoring de evento; batch >= 1 em batch)
      Output "label":         int64[N] — 0=clean, 1=IVT
      Output "probabilities": mapa/array — P(IVT=1) em index 1
    """
    output_path = Path(output_path)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    initial_type = [("features", FloatTensorType([None, IVT_VECTOR_LENGTH]))]

    import onnx as _onnx_pkg
    import onnxmltools.convert.common._topology as _topo
    _supported = getattr(_topo, "max_supported_opset", None)
    if _supported is None:
        _supported = min(int(_onnx_pkg.__version__.split(".")[0]) + 13, 17)
    target_opset = min(_supported, 15)

    print(f"[export_ivt_onnx] Convertendo IVT LightGBM -> ONNX "
          f"(input: float32[N,{IVT_VECTOR_LENGTH}], opset={target_opset})")
    onnx_model = onnxmltools.convert_lightgbm(
        booster,
        initial_types=initial_type,
        target_opset=target_opset,
    )
    onnx.save(onnx_model, str(output_path))
    size_kb = output_path.stat().st_size / 1024
    print(f"[export_ivt_onnx] ONNX salvo em {output_path} ({size_kb:.1f} KB)")
    return onnx_model


# ---------------------------------------------------------------------------
# 5. VALIDACAO NUMERICA ONNX vs LightGBM
# ---------------------------------------------------------------------------

def validate_ivt_onnx(
    booster: lgb.Booster,
    onnx_path: Path,
    X_val: np.ndarray,
    tol: float = 1e-4,
    n_check: int = 500,
) -> dict:
    """Verifica que as predicoes ONNX reproduzem as do LightGBM IVT."""
    try:
        import onnxruntime as ort
    except ImportError:
        return {"skipped": True, "reason": "onnxruntime not installed"}

    idx = np.random.default_rng(0).choice(
        len(X_val), size=min(n_check, len(X_val)), replace=False
    )
    X_check = X_val[idx].astype(np.float32)

    lgb_preds = booster.predict(X_check).astype(np.float32)

    sess_opts = ort.SessionOptions()
    sess_opts.log_severity_level = 3
    sess = ort.InferenceSession(
        str(onnx_path), sess_opts=sess_opts, providers=["CPUExecutionProvider"]
    )
    outputs = sess.run(None, {sess.get_inputs()[0].name: X_check})

    onnx_probs_raw = outputs[1]
    if isinstance(onnx_probs_raw, list):
        onnx_preds = np.array([d[1] for d in onnx_probs_raw], dtype=np.float32)
    elif hasattr(onnx_probs_raw, "shape"):
        arr = np.array(onnx_probs_raw, dtype=np.float32)
        onnx_preds = arr[:, 1] if arr.ndim == 2 else arr
    else:
        onnx_preds = np.array(list(onnx_probs_raw), dtype=np.float32)

    abs_diff = np.abs(lgb_preds - onnx_preds)
    max_abs = float(abs_diff.max())
    mean_abs = float(abs_diff.mean())
    status = "OK" if max_abs <= tol else "FAIL"
    print(f"[validate_ivt_onnx] {status}: max_abs_diff={max_abs:.2e}, tol={tol:.0e}")
    if status == "FAIL":
        raise ValueError(
            f"IVT ONNX preds divergem do LightGBM: max_abs_diff={max_abs:.2e}"
        )
    return {"max_abs_diff": max_abs, "mean_abs_diff": mean_abs, "status": status}


# ---------------------------------------------------------------------------
# 6. METRICAS
# ---------------------------------------------------------------------------

def compute_ivt_metrics(
    booster: lgb.Booster, X_val: np.ndarray, y_val: np.ndarray
) -> dict:
    """AUC-ROC, logloss e F1 no conjunto de validacao IVT."""
    preds_prob = booster.predict(X_val)
    preds_label = (preds_prob >= 0.5).astype(int)
    auc = float(roc_auc_score(y_val, preds_prob))
    ll = float(log_loss(y_val, preds_prob))
    f1 = float(f1_score(y_val, preds_label, zero_division=0))
    print(f"[ivt_metrics] AUC={auc:.4f}, logloss={ll:.4f}, F1={f1:.4f}")
    return {"val_auc": auc, "val_logloss": ll, "val_f1": f1}


# ---------------------------------------------------------------------------
# 7. PIPELINE PRINCIPAL
# ---------------------------------------------------------------------------

def run(
    data_path: Optional[Path] = None,
    n_estimators: int = 200,
    val_fraction: float = 0.2,
    seed: int = 42,
    mlflow_tracking_uri: str = "file:./mlruns",
    run_name: str = "ivt_lgb_v1",
    onnx_output_path: Optional[Path] = None,
) -> dict:
    """
    Pipeline completo de treino IVT.

    Registra no MLflow sob 'ivt_classifier' — NUNCA sobrepoem artefatos 'pctr_lgb'.
    """
    if onnx_output_path is None:
        artifacts_dir = _ML_ROOT / "registry" / "artifacts"
        artifacts_dir.mkdir(parents=True, exist_ok=True)
        onnx_output_path = artifacts_dir / "ivt_model.onnx"
    else:
        onnx_output_path = Path(onnx_output_path)
        artifacts_dir = onnx_output_path.parent
        artifacts_dir.mkdir(parents=True, exist_ok=True)

    mlflow.set_tracking_uri(mlflow_tracking_uri)
    mlflow.set_experiment("ivt_classifier")  # Experimento distinto de 'pctr_lgb'

    with mlflow.start_run(run_name=run_name) as run:
        run_id = run.info.run_id
        print(f"[run] MLflow run_id={run_id} (experimento: ivt_classifier)")

        params = {
            "model_type": "lightgbm_ivt",
            "n_estimators": n_estimators,
            "val_fraction": val_fraction,
            "seed": seed,
            "ivt_feature_version": IVT_FEATURE_VERSION,
            "n_features": IVT_VECTOR_LENGTH,
            "data_source": str(data_path) if data_path else "synthetic_sample",
            "model_name": IVT_MODEL_NAME,
        }
        mlflow.log_params(params)

        # 1. DADOS
        df = load_ivt_data(data_path=data_path, seed=seed)

        # 2. SPLIT
        df_train, df_val = train_test_split(
            df, test_size=val_fraction, random_state=seed,
            stratify=df["ivt_label"],
        )
        print(f"[run] Train: {len(df_train)}, Val: {len(df_val)}")
        mlflow.log_params({"n_train": len(df_train), "n_val": len(df_val)})

        # 3. FEATURIZACAO (anti-skew: funcao unica IVT)
        print("[run] Featurizando IVT...")
        t_feat = time.perf_counter()
        X_train = df_to_ivt_feature_matrix(df_train)
        X_val_mat = df_to_ivt_feature_matrix(df_val)
        y_train = df_train["ivt_label"].to_numpy(dtype=np.float32)
        y_val = df_val["ivt_label"].to_numpy(dtype=np.float32)
        print(f"[run] Featurizacao completa em {time.perf_counter() - t_feat:.1f}s")
        print(f"[run] X_train shape: {X_train.shape}, dtype: {X_train.dtype}")

        # 4. TREINO
        booster = train_ivt_lgb(
            X_train, y_train, X_val_mat, y_val,
            n_estimators=n_estimators, seed=seed,
        )

        # 5. METRICAS
        metrics = compute_ivt_metrics(booster, X_val_mat, y_val)
        mlflow.log_metrics(metrics)
        mlflow.log_metric("best_iteration", booster.best_iteration)

        # 6. EXPORTA ONNX
        onnx_model = export_ivt_onnx(booster, onnx_output_path)

        # 7. VALIDA ONNX
        val_result = validate_ivt_onnx(booster, onnx_output_path, X_val_mat)
        if not val_result.get("skipped"):
            mlflow.log_metrics({
                "onnx_max_abs_diff": val_result["max_abs_diff"],
                "onnx_mean_abs_diff": val_result["mean_abs_diff"],
            })

        # 8. REGISTRA ARTEFATOS
        mlflow.log_artifact(str(onnx_output_path), artifact_path="model")
        lgb_path = artifacts_dir / "ivt_booster.lgb"
        booster.save_model(str(lgb_path))
        mlflow.log_artifact(str(lgb_path), artifact_path="model")

        mlflow.set_tags({
            "ivt_feature_version": IVT_FEATURE_VERSION,
            "model_type": "lightgbm_ivt_onnx",
            "model_name": IVT_MODEL_NAME,
            "tx6_compliant": "true",       # fraude fora do hot path
            "pii_free": "true",            # TX-5/DA-11
        })

        print(f"[run] Treino IVT concluido. run_id={run_id}")
        print(f"[run] AUC={metrics['val_auc']:.4f}, F1={metrics['val_f1']:.4f}")

    return {
        "run_id": run_id,
        "model_name": IVT_MODEL_NAME,
        "metrics": metrics,
        "onnx_path": str(onnx_output_path),
        "lgb_path": str(lgb_path),
        "val_result": val_result,
        "ivt_feature_version": IVT_FEATURE_VERSION,
    }


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Treina classificador IVT (TX-6)")
    parser.add_argument("--data-path", type=Path, default=None)
    parser.add_argument("--n-estimators", type=int, default=200)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument(
        "--mlflow-tracking-uri",
        default="file:" + str(Path(__file__).parent.parent / "registry" / "mlruns"),
    )
    parser.add_argument("--run-name", type=str, default="ivt_lgb_v1")
    args = parser.parse_args()

    result = run(
        data_path=args.data_path,
        n_estimators=args.n_estimators,
        seed=args.seed,
        mlflow_tracking_uri=args.mlflow_tracking_uri,
        run_name=args.run_name,
    )
    print("\n[done]", result)
