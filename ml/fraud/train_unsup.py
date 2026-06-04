"""
ml/fraud/train_unsup.py
========================
Pipeline de treino dos modelos nao-supervisionados de deteccao de fraude (K2 / Fase 3).

MODELOS TREINADOS:
  1. Isolation Forest (IsolationForest, sklearn) — detecta anomalias por isolamento de
     amostras raras na arvore. Exportado para ONNX via skl2onnx.
  2. Autoencoder de reconstrucao (MLPRegressor, sklearn) — treina para reconstruir o
     vetor de features IVT; alto erro de reconstrucao = anomalia/fraude nova.
     Exportado para ONNX via abordagem de scoring de anomalia (ver abaixo).

PRINCIPIO (ADR-0004 §B):
  Modelos nao-supervisionados COMPLEMENTAM o GBDT supervisionado (ivt_classifier):
  cobrem padroes de fraude NOVOS / nao-rotulados que o GBDT nao pega.
  Nunca substituem o supervisionado; a decisao final de IVT e uma combinacao de
  ambos os scores (logica OR sobre limiares — ver unsup_scorer.py).

GNN (Grafos de Coordenacao):
  Deixado como FUTURO (documentado em docs/adr/0004). Requer construcao de grafo
  de coordenacao (edges por shared device_class/geo_country/hora) e biblioteca de
  GNN (DGL/PyG) — indisponivel no ambiente atual. Gatilho: quando volume de trafego
  real revelar padroes de coordenacao nao capturados por IF + autoencoder.

TX-6 (fraude fora do hot path):
  Este treino roda OFFLINE sobre sample sintetico (ou Iceberg em producao).
  Os modelos sao invocados na INGESTAO (near-line), nunca no hot path de decisao.

TX-5/DA-11 (PII-free):
  Reusa as mesmas features de ml/fraud/features.py (featurize_ivt) — sem PII,
  sem IP bruto, sem UA completo. NUNCA edita ml/features (anti-skew).

TX-2 (dinheiro em minor-units):
  Nenhum valor monetario e computado neste pipeline.

NOME DOS MODELOS NO MLflow:
  - 'ivt_unsup_isolation_forest' — Isolation Forest
  - 'ivt_unsup_autoencoder'      — Autoencoder de reconstrucao
  NUNCA colidir com 'ivt_classifier' (GBDT supervisionado) nem 'pctr_lgb'.

USO:
  python train_unsup.py [--data-path PATH] [--n-estimators N]
                        [--mlflow-tracking-uri URI] [--run-name NAME]
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
import warnings
from pathlib import Path
from typing import Optional

os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")
warnings.filterwarnings("ignore", category=UserWarning)

import mlflow
import numpy as np
import pandas as pd

_REPO_ROOT = Path(__file__).parent.parent.parent
_ML_ROOT = Path(__file__).parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.fraud.features import IVT_FEATURE_NAMES, IVT_VECTOR_LENGTH  # noqa: E402
from ml.fraud.train_ivt import df_to_ivt_feature_matrix, load_ivt_data  # noqa: E402

# ---------------------------------------------------------------------------
# Nomes de modelo no MLflow — DISTINTOS de ivt_classifier e pctr_lgb
# ---------------------------------------------------------------------------

UNSUP_IF_MODEL_NAME = "ivt_unsup_isolation_forest"
UNSUP_AE_MODEL_NAME = "ivt_unsup_autoencoder"
UNSUP_MODEL_VERSION  = "1.0.0"


# ---------------------------------------------------------------------------
# 1. ISOLATION FOREST — treino e export ONNX
# ---------------------------------------------------------------------------

def train_isolation_forest(
    X: np.ndarray,
    n_estimators: int = 200,
    contamination: float = 0.15,
    seed: int = 42,
) -> object:
    """
    Treina IsolationForest sobre o vetor de features IVT (sem labels).

    contamination=0.15: estimativa de fracao de anomalias no dataset.
    Em producao real, ajustar via calibracao sobre dados rotulados (ver docs).
    """
    from sklearn.ensemble import IsolationForest

    clf = IsolationForest(
        n_estimators=n_estimators,
        contamination=contamination,
        max_features=1.0,
        max_samples="auto",
        random_state=seed,
        n_jobs=-1,
    )
    t0 = time.perf_counter()
    clf.fit(X)
    elapsed = time.perf_counter() - t0
    print(f"[train_isolation_forest] Treino completo em {elapsed:.1f}s. "
          f"n_samples={len(X)}, n_features={X.shape[1]}, "
          f"n_estimators={n_estimators}")
    return clf


def export_isolation_forest_onnx(clf, output_path: Path) -> bytes:
    """
    Exporta IsolationForest para ONNX via skl2onnx.

    Output do ONNX:
      'label':  int64[N, 1]  — 1=inlier (normal), -1=outlier (anomalia)
      'scores': float32[N, 1] — anomaly score (mais negativo = mais anomalo)

    NOTA sobre semantica do score:
      IsolationForest.score_samples() retorna valores em [-0.5, 0.5] com
      mais negativos indicando anomalias. No ONNX, 'scores' tem a mesma
      convencao. No unsup_scorer.py, convertemos para [0,1] via normalizacao.
    """
    from skl2onnx import convert_sklearn
    from skl2onnx.common.data_types import FloatTensorType

    output_path = Path(output_path)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    initial_type = [("float_input", FloatTensorType([None, IVT_VECTOR_LENGTH]))]
    onnx_model = convert_sklearn(
        clf,
        initial_types=initial_type,
        target_opset={"": 15, "ai.onnx.ml": 3},
    )
    onnx_bytes = onnx_model.SerializeToString()
    with open(output_path, "wb") as f:
        f.write(onnx_bytes)

    size_kb = output_path.stat().st_size / 1024
    print(f"[export_isolation_forest_onnx] ONNX salvo em {output_path} ({size_kb:.1f} KB)")
    return onnx_bytes


def validate_isolation_forest_onnx(
    clf,
    onnx_path: Path,
    X: np.ndarray,
    n_check: int = 200,
) -> dict:
    """Valida que o ONNX do IsolationForest concorda com sklearn."""
    try:
        import onnxruntime as ort
    except ImportError:
        return {"skipped": True, "reason": "onnxruntime not installed"}

    idx = np.random.default_rng(0).choice(len(X), size=min(n_check, len(X)), replace=False)
    X_check = X[idx].astype(np.float32)

    # Sklearn: predict retorna 1=normal, -1=anomalia
    sk_labels = clf.predict(X_check)
    sk_scores  = clf.score_samples(X_check)

    sess_opts = ort.SessionOptions()
    sess_opts.log_severity_level = 3
    sess = ort.InferenceSession(
        str(onnx_path), sess_opts=sess_opts, providers=["CPUExecutionProvider"]
    )
    input_name = sess.get_inputs()[0].name
    outputs = sess.run(None, {input_name: X_check})

    onnx_labels = outputs[0].flatten().astype(np.int64)
    onnx_scores = outputs[1].flatten().astype(np.float64)

    label_agree = float(np.mean(sk_labels == onnx_labels))
    score_diff  = float(np.abs(sk_scores - onnx_scores).max())

    status = "OK" if label_agree >= 0.99 and score_diff < 1e-4 else "WARN"
    print(f"[validate_if_onnx] {status}: label_agreement={label_agree:.4f}, "
          f"max_score_diff={score_diff:.2e}")
    return {
        "label_agreement": label_agree,
        "max_score_diff": score_diff,
        "status": status,
    }


# ---------------------------------------------------------------------------
# 2. AUTOENCODER — treino (MLPRegressor sklearn) e export de pesos JSON
# ---------------------------------------------------------------------------
#
# ESTRATEGIA DE EXPORT:
#   skl2onnx nao preserva a forma de saida multi-output do MLPRegressor corretamente
#   (achata para [N*F, 1]). Em vez de ONNX direto, exportamos os pesos da rede em
#   JSON e implementamos o forward pass em numpy no unsup_scorer.py.
#   O scorer carrega os pesos e computa o erro de reconstrucao diretamente.
#   Esta abordagem e:
#     - Auditavel (pesos legibles em JSON)
#     - Sem dependencia de PyTorch (requisito MVP, ADR-0004 §A)
#     - Identica em resultado ao ONNX (mesma algebra linear)
#     - Substituivel por ONNX real quando PyTorch estiver disponivel (Fase 3 futura)
#
# GNN FUTURO:
#   GNN (Graph Neural Network) para deteccao de fraude por coordenacao de botnet
#   requer DGL/PyG (nao disponivel no ambiente MVP) e construcao de grafo de
#   coordenacao (edges por shared device_class/geo_country/hora nas ultimas 24h).
#   Gatilho de ativacao: volume de trafego real revelar padroes de coordenacao
#   nao capturados por IF + autoencoder (medir em A/B com dados reais).
# ---------------------------------------------------------------------------

def train_autoencoder(
    X: np.ndarray,
    hidden_size: int = 6,
    seed: int = 42,
    max_iter: int = 500,
) -> object:
    """
    Treina autoencoder de reconstrucao usando MLPRegressor (sklearn).

    Arquitetura: n_features -> hidden_size -> n_features (encoder-decoder simples).
    Target = input: a rede aprende a reconstruir o vetor de features normal.
    Anomalias (fraude nova) tem erro de reconstrucao mais alto.

    hidden_size=6 para n_features=10: bottleneck leve, suficiente para capturar
    a estrutura de trafego legítimo sem memorizar outliers.
    """
    from sklearn.neural_network import MLPRegressor
    from sklearn.preprocessing import StandardScaler

    # Normaliza features antes do autoencoder (independente do IVT_VECTOR_LENGTH)
    scaler = StandardScaler()
    X_scaled = scaler.fit_transform(X)

    ae = MLPRegressor(
        hidden_layer_sizes=(hidden_size,),
        activation="relu",
        solver="adam",
        max_iter=max_iter,
        random_state=seed,
        early_stopping=True,
        validation_fraction=0.1,
        n_iter_no_change=20,
        verbose=False,
    )
    t0 = time.perf_counter()
    ae.fit(X_scaled, X_scaled)  # target = input (auto-encoder)
    elapsed = time.perf_counter() - t0
    print(f"[train_autoencoder] Treino completo em {elapsed:.1f}s. "
          f"n_iter={ae.n_iter_}, hidden={hidden_size}, "
          f"loss={ae.loss_:.4f}")

    return ae, scaler


def export_autoencoder_weights(
    ae, scaler, output_path: Path
) -> dict:
    """
    Exporta os pesos do autoencoder em JSON para forward pass em numpy.

    Formato:
      {
        "scaler_mean":   list[float],  # StandardScaler.mean_
        "scaler_scale":  list[float],  # StandardScaler.scale_
        "weights":       list[list[float]],  # coefs_ de cada camada
        "biases":        list[list[float]],  # intercepts_ de cada camada
        "activation":    "relu",
        "hidden_size":   int,
        "n_features":    int,
        "model_version": str,
      }
    """
    output_path = Path(output_path)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    weights_data = {
        "scaler_mean":   scaler.mean_.tolist(),
        "scaler_scale":  scaler.scale_.tolist(),
        "weights":       [w.tolist() for w in ae.coefs_],
        "biases":        [b.tolist() for b in ae.intercepts_],
        "activation":    ae.activation,
        "hidden_size":   ae.hidden_layer_sizes[0] if hasattr(ae.hidden_layer_sizes, '__getitem__') else ae.hidden_layer_sizes,
        "n_features":    IVT_VECTOR_LENGTH,
        "model_version": UNSUP_MODEL_VERSION,
    }

    with open(output_path, "w", encoding="utf-8") as f:
        json.dump(weights_data, f, indent=2)

    size_kb = output_path.stat().st_size / 1024
    print(f"[export_autoencoder_weights] Pesos exportados em {output_path} ({size_kb:.1f} KB)")
    return weights_data


def compute_autoencoder_threshold(
    ae, scaler, X: np.ndarray, percentile: float = 95.0
) -> float:
    """
    Calcula o limiar de erro de reconstrucao para classificar anomalias.

    Estrategia: percentil 95 do erro de reconstrucao no conjunto de treino.
    Eventos com erro > threshold sao considerados anomalias.

    Em producao real: calibrar com dados rotulados (IVT/clean) e ajustar o
    percentile para o trade-off de precisao/recall desejado.
    """
    X_scaled = scaler.transform(X)
    X_rec    = ae.predict(X_scaled)
    errors   = np.mean((X_scaled - X_rec) ** 2, axis=1)
    threshold = float(np.percentile(errors, percentile))
    print(f"[compute_ae_threshold] Limiar p{percentile:.0f}={threshold:.4f}, "
          f"mean_error={float(errors.mean()):.4f}, "
          f"max_error={float(errors.max()):.4f}")
    return threshold


# ---------------------------------------------------------------------------
# 3. METRICAS (semi-supervisionadas: usa labels do sample sintetico)
# ---------------------------------------------------------------------------

def compute_unsup_metrics(
    clf_if,
    ae, scaler,
    ae_threshold: float,
    X: np.ndarray,
    y: np.ndarray,
) -> dict:
    """
    Computa metricas dos modelos nao-supervisionados usando labels sinteticos.

    NOTA: labels sinteticos do sample de dev nao representam a distribuicao real.
    Em producao, avaliar com OPE/IPS sobre dados reais rotulados.

    Metricas:
      - IF precision/recall/F1 (label -1=anomalia -> IVT=1)
      - AE precision/recall/F1 (erro > threshold -> IVT=1)
    """
    from sklearn.metrics import precision_score, recall_score, f1_score

    # Isolation Forest: -1=anomalia -> 1=IVT
    if_preds = clf_if.predict(X)
    if_ivt   = (if_preds == -1).astype(int)

    if_prec = float(precision_score(y, if_ivt, zero_division=0))
    if_rec  = float(recall_score(y, if_ivt, zero_division=0))
    if_f1   = float(f1_score(y, if_ivt, zero_division=0))

    # Autoencoder: erro > threshold -> IVT=1
    X_scaled = scaler.transform(X)
    X_rec    = ae.predict(X_scaled)
    ae_errors = np.mean((X_scaled - X_rec) ** 2, axis=1)
    ae_ivt   = (ae_errors > ae_threshold).astype(int)

    ae_prec = float(precision_score(y, ae_ivt, zero_division=0))
    ae_rec  = float(recall_score(y, ae_ivt, zero_division=0))
    ae_f1   = float(f1_score(y, ae_ivt, zero_division=0))

    # Combinacao OR: IVT se IF OU AE detectou anomalia
    combined_ivt = ((if_ivt == 1) | (ae_ivt == 1)).astype(int)
    comb_prec = float(precision_score(y, combined_ivt, zero_division=0))
    comb_rec  = float(recall_score(y, combined_ivt, zero_division=0))
    comb_f1   = float(f1_score(y, combined_ivt, zero_division=0))

    print(f"[unsup_metrics] IF:   prec={if_prec:.4f}, rec={if_rec:.4f}, f1={if_f1:.4f}")
    print(f"[unsup_metrics] AE:   prec={ae_prec:.4f}, rec={ae_rec:.4f}, f1={ae_f1:.4f}")
    print(f"[unsup_metrics] OR:   prec={comb_prec:.4f}, rec={comb_rec:.4f}, f1={comb_f1:.4f}")

    return {
        "if_precision":   if_prec,
        "if_recall":      if_rec,
        "if_f1":          if_f1,
        "ae_precision":   ae_prec,
        "ae_recall":      ae_rec,
        "ae_f1":          ae_f1,
        "combined_precision": comb_prec,
        "combined_recall":    comb_rec,
        "combined_f1":        comb_f1,
    }


# ---------------------------------------------------------------------------
# 4. PIPELINE PRINCIPAL
# ---------------------------------------------------------------------------

def run(
    data_path: Optional[Path] = None,
    n_estimators_if: int = 200,
    contamination: float = 0.15,
    ae_hidden: int = 6,
    ae_max_iter: int = 500,
    ae_threshold_percentile: float = 95.0,
    val_fraction: float = 0.2,
    seed: int = 42,
    mlflow_tracking_uri: str = "file:./mlruns",
    run_name: str = "ivt_unsup_v1",
    artifacts_dir: Optional[Path] = None,
) -> dict:
    """
    Pipeline completo de treino dos modelos nao-supervisionados de IVT.

    Registra no MLflow como experimento 'ivt_unsup' com dois runs internos
    (um por modelo) linkados ao run principal.
    """
    if artifacts_dir is None:
        artifacts_dir = _ML_ROOT / "registry" / "artifacts"
    artifacts_dir = Path(artifacts_dir)
    artifacts_dir.mkdir(parents=True, exist_ok=True)

    if_onnx_path = artifacts_dir / "ivt_unsup_if.onnx"
    ae_weights_path = artifacts_dir / "ivt_unsup_ae_weights.json"

    mlflow.set_tracking_uri(mlflow_tracking_uri)
    mlflow.set_experiment("ivt_unsup")  # experimento distinto de ivt_classifier e pctr_lgb

    with mlflow.start_run(run_name=run_name) as run:
        run_id = run.info.run_id
        print(f"[run] MLflow run_id={run_id} (experimento: ivt_unsup)")

        params = {
            "model_type_if":    "isolation_forest",
            "model_type_ae":    "mlp_autoencoder",
            "n_estimators_if":  n_estimators_if,
            "contamination":    contamination,
            "ae_hidden":        ae_hidden,
            "ae_max_iter":      ae_max_iter,
            "ae_threshold_pct": ae_threshold_percentile,
            "val_fraction":     val_fraction,
            "seed":             seed,
            "n_features":       IVT_VECTOR_LENGTH,
            "data_source":      str(data_path) if data_path else "synthetic_sample",
            "unsup_model_version": UNSUP_MODEL_VERSION,
            "gnn_status":       "future_work",  # GNN documentado como futuro
        }
        mlflow.log_params(params)

        # 1. DADOS (reusa loader do treino supervisionado — mesma featurizacao)
        df = load_ivt_data(data_path=data_path, seed=seed)
        from sklearn.model_selection import train_test_split
        df_train, df_val = train_test_split(
            df, test_size=val_fraction, random_state=seed,
            stratify=df["ivt_label"],
        )
        print(f"[run] Train: {len(df_train)}, Val: {len(df_val)}")
        mlflow.log_params({"n_train": len(df_train), "n_val": len(df_val)})

        # 2. FEATURIZACAO (anti-skew: funcao unica de ml/fraud/features.py via train_ivt)
        print("[run] Featurizando IVT (via df_to_ivt_feature_matrix)...")
        X_train = df_to_ivt_feature_matrix(df_train)
        X_val   = df_to_ivt_feature_matrix(df_val)
        y_val   = df_val["ivt_label"].to_numpy(dtype=np.int32)

        # Treino nao-supervisionado usa apenas X (sem labels)
        # Mas: para avaliar, usamos y_val do sample sintetico
        print(f"[run] X_train shape: {X_train.shape}, X_val shape: {X_val.shape}")

        # 3. ISOLATION FOREST
        print("[run] Treinando Isolation Forest...")
        clf_if = train_isolation_forest(
            X_train, n_estimators=n_estimators_if,
            contamination=contamination, seed=seed,
        )

        # 4. AUTOENCODER
        print("[run] Treinando Autoencoder de reconstrucao...")
        ae, scaler = train_autoencoder(
            X_train, hidden_size=ae_hidden, seed=seed, max_iter=ae_max_iter,
        )
        ae_threshold = compute_autoencoder_threshold(
            ae, scaler, X_train, percentile=ae_threshold_percentile,
        )

        # 5. METRICAS (semi-supervisionadas)
        metrics = compute_unsup_metrics(
            clf_if, ae, scaler, ae_threshold, X_val, y_val,
        )
        mlflow.log_metrics(metrics)
        mlflow.log_metric("ae_threshold", ae_threshold)

        # 6. EXPORTA ARTEFATOS
        if_onnx_bytes = export_isolation_forest_onnx(clf_if, if_onnx_path)
        ae_weights   = export_autoencoder_weights(ae, scaler, ae_weights_path)

        # Persiste threshold junto com os pesos do AE
        ae_meta_path = artifacts_dir / "ivt_unsup_ae_meta.json"
        with open(ae_meta_path, "w") as f:
            json.dump({
                "ae_threshold":            ae_threshold,
                "ae_threshold_percentile": ae_threshold_percentile,
                "n_features":              IVT_VECTOR_LENGTH,
                "feature_names":           IVT_FEATURE_NAMES,
                "unsup_model_version":     UNSUP_MODEL_VERSION,
                "gnn_status":              "future_work",
                "combination_policy":      "OR_over_thresholds",
            }, f, indent=2)

        # 7. VALIDA ONNX DO IF
        val_if = validate_isolation_forest_onnx(clf_if, if_onnx_path, X_val)
        if not val_if.get("skipped"):
            mlflow.log_metrics({
                "if_onnx_label_agreement": val_if.get("label_agreement", 0.0),
                "if_onnx_max_score_diff":  val_if.get("max_score_diff", 0.0),
            })

        # 8. REGISTRA ARTEFATOS NO MLFLOW
        mlflow.log_artifact(str(if_onnx_path),     artifact_path="models")
        mlflow.log_artifact(str(ae_weights_path),  artifact_path="models")
        mlflow.log_artifact(str(ae_meta_path),     artifact_path="models")

        mlflow.set_tags({
            "unsup_model_version": UNSUP_MODEL_VERSION,
            "model_if_name":       UNSUP_IF_MODEL_NAME,
            "model_ae_name":       UNSUP_AE_MODEL_NAME,
            "tx6_compliant":       "true",   # fraude fora do hot path
            "pii_free":            "true",   # TX-5/DA-11
            "gnn_status":          "future_work",
            "combination_policy":  "OR_over_thresholds",
        })

        print(f"[run] Treino nao-supervisionado concluido. run_id={run_id}")
        print(f"[run] IF F1={metrics['if_f1']:.4f}, AE F1={metrics['ae_f1']:.4f}, "
              f"Combined F1={metrics['combined_f1']:.4f}")

    return {
        "run_id":         run_id,
        "if_model_name":  UNSUP_IF_MODEL_NAME,
        "ae_model_name":  UNSUP_AE_MODEL_NAME,
        "metrics":        metrics,
        "ae_threshold":   ae_threshold,
        "if_onnx_path":   str(if_onnx_path),
        "ae_weights_path": str(ae_weights_path),
        "ae_meta_path":   str(ae_meta_path),
        "val_if":         val_if,
    }


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Treina modelos nao-supervisionados de IVT (K2 / TX-6)"
    )
    parser.add_argument("--data-path", type=Path, default=None)
    parser.add_argument("--n-estimators-if", type=int, default=200)
    parser.add_argument("--contamination", type=float, default=0.15)
    parser.add_argument("--ae-hidden", type=int, default=6)
    parser.add_argument("--ae-max-iter", type=int, default=500)
    parser.add_argument("--ae-threshold-percentile", type=float, default=95.0)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument(
        "--mlflow-tracking-uri",
        default="file:" + str(Path(__file__).parent.parent / "registry" / "mlruns"),
    )
    parser.add_argument("--run-name", type=str, default="ivt_unsup_v1")
    args = parser.parse_args()

    result = run(
        data_path=args.data_path,
        n_estimators_if=args.n_estimators_if,
        contamination=args.contamination,
        ae_hidden=args.ae_hidden,
        ae_max_iter=args.ae_max_iter,
        ae_threshold_percentile=args.ae_threshold_percentile,
        seed=args.seed,
        mlflow_tracking_uri=args.mlflow_tracking_uri,
        run_name=args.run_name,
    )
    print("\n[done]", result)
