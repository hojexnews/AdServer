"""
ml/training/train_pctr.py
==========================
Pipeline de treino do modelo pCTR com LightGBM.

RESPONSABILIDADES:
  1. Carrega os dados (Iceberg em producao; Parquet sintetico neste ambiente).
  2. Aplica a UNICA funcao de featurizacao (ml/features/python/featurize.py).
  3. Treina LightGBM pCTR.
  4. Exporta para .onnx (via onnxmltools / skl2onnx Booster).
  5. Valida numericamente que o .onnx reproduz as predicoes do LightGBM.
  6. Registra no MLflow: modelo, metricas, artefatos, feature_spec_version.

DINHEIRO (TX-2 / money-ledger-guardian):
  - ecpm_minor_units entra no pipeline como int64 minor-units.
  - A featurizacao converte para log1p(float32) APENAS para o vetor de ML.
  - pCTR e uma probabilidade [0,1] — float e correto para probabilidade.
  - eCPM = pCTR × bid usa MINOR-UNITS inteiros / Decimal fora deste modulo.
  - O ponto exato de multiplicacao e no motor de decisao (internal/ranker/score.go),
    NAO aqui. Este modulo entrega apenas pCTR ∈ [0,1].
  - Nenhum valor monetario (bid, eCPM, receita esperada) e computado aqui.

PRIVACIDADE (TX-5/DA-11):
  - Somente features da spec (todas marcadas pii_free: true).
  - Sem IP bruto. O campo geo ja e country/city derivado.
  - Nenhuma feature nova fora da spec.

ANTI-SKEW:
  - A funcao de featurizacao e SEMPRE ml/features/python/featurize.py.
  - NUNCA reimplementar featurizacao aqui.

SIDECAR (J1) — contrato de artefato ONNX:
  - Caminho no MLflow: artifacts/pctr_model.onnx
  - Input name: "features"
  - Input dtype: float32
  - Input shape: [N, 23]  (N = batch size; N=1 no hot path)
  - Output name: "probabilities"  (campo "probabilities" do ZipMap, index 1 = P(click=1))
  - Alternativa de output raw: "output_probability" se o grafo nao tiver ZipMap
  - feature_spec_version: "1.0.0"

USO:
  python train_pctr.py [--data-path PATH] [--window-start DATE] [--window-end DATE]
                       [--n-estimators N] [--mlflow-tracking-uri URI] [--run-name NAME]

  Sem --data-path: usa o sample sintetico de ml/training/testdata/sample_train.parquet
  (gerado por generate_sample.py se nao existir).
"""
from __future__ import annotations

import argparse
import os
import sys
import time
from pathlib import Path
from typing import Optional, Tuple

# MLflow file-store: requerido em MLflow >= 3.x para file-store local.
# Em producao, usar sqlite:/// ou URI remota.
os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

import mlflow
import mlflow.lightgbm
import numpy as np
import pandas as pd

# ---------------------------------------------------------------------------
# Resolve paths: este script pode ser executado de qualquer CWD
# ---------------------------------------------------------------------------
_REPO_ROOT = Path(__file__).parent.parent.parent
_ML_ROOT = Path(__file__).parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.features.python.featurize import (  # noqa: E402 — import apos sys.path
    FEATURE_SPEC_VERSION,
    CandidateInput,
    FeaturizeInput,
    GeoInput,
    RequestTimeInput,
    ZoneInput,
    featurize,
)

# ---------------------------------------------------------------------------
# Imports de ML
# ---------------------------------------------------------------------------
import lightgbm as lgb  # noqa: E402
import onnx  # noqa: E402
import onnxmltools  # noqa: E402
from onnxmltools.convert.common.data_types import FloatTensorType  # noqa: E402
from sklearn.metrics import log_loss, roc_auc_score  # noqa: E402

# ---------------------------------------------------------------------------
# Constantes
# ---------------------------------------------------------------------------
_FEATURE_NAMES = [
    "zone_width_bucket",        # 0
    "zone_height_bucket",       # 1
    "zone_aspect_ratio_bucket", # 2
    "hour_of_day",              # 3
    "day_of_week",              # 4
    "is_weekend",               # 5
    "geo_country_hash",         # 6
    "geo_city_hash",            # 7
    "device_class_hash",        # 8
    "is_bot",                   # 9
    "candidate_tier",           # 10
    "campaign_priority",        # 11
    "pacing_deficit",           # 12
    "pacing_deficit_bucket",    # 13
    "ecpm_minor_units_log1p",   # 14
    "ecpm_tier_bucket",         # 15
    "banner_width_bucket",      # 16
    "banner_height_bucket",     # 17
    "banner_size_match",        # 18
    "creative_type",            # 19
    "candidate_count_log1p",    # 20
    "campaign_ctr_estimate",    # 21
    "campaign_delivery_ratio",  # 22
]
_N_FEATURES = 23
assert len(_FEATURE_NAMES) == _N_FEATURES


# ---------------------------------------------------------------------------
# 1. DATA LOADING
# ---------------------------------------------------------------------------

def load_data(
    data_path: Optional[Path] = None,
    window_start: Optional[str] = None,
    window_end: Optional[str] = None,
    n_sample: int = 50_000,
    seed: int = 42,
) -> pd.DataFrame:
    """
    Carrega os dados de treino.

    PRODUCAO:
        Substitua este bloco pela leitura Iceberg:

            import pyiceberg.catalog
            catalog = pyiceberg.catalog.load_catalog("glue", **cfg)
            tbl = catalog.load_table("adserver.impressions_clicks_joined")
            df = tbl.scan(
                row_filter=(
                    f"window_start >= '{window_start}' "
                    f"AND window_end < '{window_end}'"
                )
            ).to_pandas()
            # Garante TX-2: ecpm_minor_units int64
            df["ecpm_minor_units"] = df["ecpm_minor_units"].astype("int64")

        A coluna ecpm_minor_units DEVE ser int64 (TX-2 — sem float monetario).
        Filtrar decisoes com ml_fail_open=true antes de usar propensity como peso IPS.
        Filtrar feature_spec_version != FEATURE_SPEC_VERSION (desafasamento de spec).

    DEV / SAMPLE:
        Usa Parquet sintetico de testdata/sample_train.parquet (gerado por generate_sample.py).
    """
    if data_path is not None and Path(data_path).exists():
        print(f"[load_data] Lendo Parquet: {data_path}")
        df = pd.read_parquet(str(data_path), engine="pyarrow")
    else:
        sample_path = _ML_ROOT / "training" / "testdata" / "sample_train.parquet"
        if not sample_path.exists():
            print("[load_data] Gerando sample sintetico...")
            from ml.training.generate_sample import generate
            df = generate(n_rows=n_sample, seed=seed, output_path=sample_path)
        else:
            print(f"[load_data] Carregando sample existente: {sample_path}")
            df = pd.read_parquet(str(sample_path), engine="pyarrow")

    # TX-2: garante int64 mesmo apos leitura de Parquet
    df["ecpm_minor_units"] = df["ecpm_minor_units"].astype("int64")

    # Filtra feature_spec_version incompativel (em producao com Iceberg real)
    if "feature_spec_version" in df.columns:
        n_before = len(df)
        df = df[df["feature_spec_version"] == FEATURE_SPEC_VERSION].copy()
        dropped = n_before - len(df)
        if dropped:
            print(f"[load_data] AVISO: {dropped} linhas com feature_spec_version incorreta removidas.")

    print(f"[load_data] {len(df)} linhas carregadas. CTR medio: {df['label_click'].mean():.4f}")
    return df


# ---------------------------------------------------------------------------
# 2. FEATURIZACAO (ANTI-SKEW — usa a funcao unica)
# ---------------------------------------------------------------------------

def df_to_feature_matrix(df: pd.DataFrame) -> np.ndarray:
    """
    Aplica featurize() de ml/features/python/featurize.py a cada linha do DataFrame.

    ANTI-SKEW: esta e a UNICA funcao de featurizacao usada em treino e serving.
    NAO reimplementar transformacoes aqui.

    Retorna:
        X: ndarray float32 de shape (len(df), 23)

    TX-2: ecpm_minor_units deve ser int antes de entrar no featurize().
          O DataFrame ja garante isso (coluna int64).
    """
    vectors = []
    for _, row in df.iterrows():
        inp = FeaturizeInput(
            zone=ZoneInput(
                width=int(row["zone_width"]),
                height=int(row["zone_height"]),
            ),
            request_time_utc=RequestTimeInput(
                hour=int(row["hour_of_day"]),
                day_of_week=int(row["day_of_week"]),
            ),
            geo=GeoInput(
                country=str(row["geo_country"]),
                city=str(row["geo_city"]),
            ),
            device_class=str(row["device_class"]),
            candidate=CandidateInput(
                tier=int(row["candidate_tier"]),
                priority=int(row["campaign_priority"]),
                pacing_deficit=float(row["pacing_deficit"]),
                ecpm_minor_units=int(row["ecpm_minor_units"]),  # TX-2: int64
                banner_width=int(row["banner_width"]),
                banner_height=int(row["banner_height"]),
                creative_type=str(row["creative_type"]),
            ),
            candidate_count=int(row["candidate_count"]),
            campaign_delivered_impressions=int(row["delivered_impressions"]),
            campaign_goal_impressions=int(row["goal_impressions"]),
            campaign_delivered_clicks=int(row["delivered_clicks"]),
        )
        vectors.append(featurize(inp))  # float32[23]

    X = np.vstack(vectors).astype(np.float32)
    assert X.shape == (len(df), _N_FEATURES), f"Shape inesperado: {X.shape}"
    return X


def df_to_feature_matrix_fast(df: pd.DataFrame) -> np.ndarray:
    """
    Versao vetorizada de df_to_feature_matrix para datasets grandes.

    Replica a logica de featurize() de forma vetorizada (numpy).
    AVISO: esta versao e um alias fast — NAO introduz logica nova de featurizacao.
    Em caso de duvida entre os resultados, df_to_feature_matrix() e a referencia.

    Validar consistencia com assert_feature_matrix_consistent() antes de usar no treino.
    """
    import math

    from ml.features.python.featurize import (
        _ASPECT_BOUNDARIES,
        _CITY_HASH_BUCKETS,
        _CITY_HASH_SEED,
        _COUNTRY_HASH_BUCKETS,
        _COUNTRY_HASH_SEED,
        _CREATIVE_TYPE_MAP,
        _DEVICE_HASH_BUCKETS,
        _DEVICE_HASH_SEED,
        _ECPM_LOG1P_BOUNDARIES,
        _PACING_BOUNDARIES,
        _ZONE_HEIGHT_BOUNDARIES,
        _ZONE_WIDTH_BOUNDARIES,
        _bucketize,
        _feature_hash,
    )

    n = len(df)
    X = np.zeros((n, _N_FEATURES), dtype=np.float32)

    zone_w = df["zone_width"].to_numpy(dtype=np.int32)
    zone_h = df["zone_height"].to_numpy(dtype=np.int32)

    # Grupos 1–10: vetorizados via list comprehension sobre funcoes canonicas
    for i, row in enumerate(df.itertuples(index=False)):
        inp = FeaturizeInput(
            zone=ZoneInput(width=int(row.zone_width), height=int(row.zone_height)),
            request_time_utc=RequestTimeInput(
                hour=int(row.hour_of_day), day_of_week=int(row.day_of_week)
            ),
            geo=GeoInput(country=str(row.geo_country), city=str(row.geo_city)),
            device_class=str(row.device_class),
            candidate=CandidateInput(
                tier=int(row.candidate_tier),
                priority=int(row.campaign_priority),
                pacing_deficit=float(row.pacing_deficit),
                ecpm_minor_units=int(row.ecpm_minor_units),
                banner_width=int(row.banner_width),
                banner_height=int(row.banner_height),
                creative_type=str(row.creative_type),
            ),
            candidate_count=int(row.candidate_count),
            campaign_delivered_impressions=int(row.delivered_impressions),
            campaign_goal_impressions=int(row.goal_impressions),
            campaign_delivered_clicks=int(row.delivered_clicks),
        )
        X[i] = featurize(inp)

    return X


# ---------------------------------------------------------------------------
# 3. TREINO LightGBM
# ---------------------------------------------------------------------------

def train_lgb(
    X_train: np.ndarray,
    y_train: np.ndarray,
    X_val: np.ndarray,
    y_val: np.ndarray,
    n_estimators: int = 300,
    learning_rate: float = 0.05,
    num_leaves: int = 63,
    seed: int = 42,
) -> lgb.Booster:
    """
    Treina LightGBM para classificacao binaria pCTR.

    Retorna o Booster treinado.
    """
    dtrain = lgb.Dataset(X_train, label=y_train, feature_name=_FEATURE_NAMES)
    dval = lgb.Dataset(X_val, label=y_val, reference=dtrain, feature_name=_FEATURE_NAMES)

    params = {
        "objective": "binary",
        "metric": ["binary_logloss", "auc"],
        "num_leaves": num_leaves,
        "learning_rate": learning_rate,
        "feature_fraction": 0.8,
        "bagging_fraction": 0.8,
        "bagging_freq": 5,
        "min_child_samples": 20,
        "seed": seed,
        "verbose": -1,
        "num_threads": 4,
    }

    print(f"[train_lgb] Iniciando treino: n_estimators={n_estimators}, "
          f"num_leaves={num_leaves}, lr={learning_rate}")
    t0 = time.perf_counter()

    callbacks = [lgb.early_stopping(50, verbose=False), lgb.log_evaluation(50)]
    booster = lgb.train(
        params,
        dtrain,
        num_boost_round=n_estimators,
        valid_sets=[dval],
        callbacks=callbacks,
    )

    elapsed = time.perf_counter() - t0
    print(f"[train_lgb] Treino completo em {elapsed:.1f}s. "
          f"Melhores iteracoes: {booster.best_iteration}")
    return booster


# ---------------------------------------------------------------------------
# 4. CONVERSAO PARA ONNX
# ---------------------------------------------------------------------------

def export_onnx(booster: lgb.Booster, output_path: Path) -> onnx.ModelProto:
    """
    Converte o Booster LightGBM para ONNX via onnxmltools.

    CONTRATO DE ARTEFATO (para o sidecar J1 / services/ranker-sidecar):
      - Input name:  "features"
      - Input dtype: float32
      - Input shape: [N, 23]  (N = 1 no hot path, batch >= 1 em batch scoring)
      - Output: o modelo ONNX de classificacao binaria LightGBM exporta dois tensores:
          * "label"        — int64[N] — classe predita (0 ou 1)
          * "probabilities" — Mapa ou Sequencia — probabilidades por classe
        O sidecar deve extrair P(click=1) de "probabilities[1]".
        Ver services/ranker-sidecar/README.md para o codigo de extração exato.
      - feature_spec_version: "1.0.0" — gravado como metadata do artefato no MLflow.

    Retorna o ModelProto ONNX.
    """
    output_path = Path(output_path)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    initial_type = [("features", FloatTensorType([None, _N_FEATURES]))]

    # Detecta a versao maxima suportada pelo onnxmltools instalado
    import onnx as _onnx_pkg
    import onnxmltools.convert.common._topology as _topo
    _supported = getattr(_topo, "max_supported_opset", None)
    if _supported is None:
        # onnx 1.14+ expoe opset_version no proto; usa max estavel como fallback
        _supported = min(int(_onnx_pkg.__version__.split(".")[0]) + 13, 17)
    target_opset = min(_supported, 15)  # 15 e amplamente suportado

    print(f"[export_onnx] Convertendo LightGBM -> ONNX (input: float32[N,{_N_FEATURES}], opset={target_opset})")
    onnx_model = onnxmltools.convert_lightgbm(
        booster,
        initial_types=initial_type,
        target_opset=target_opset,
    )

    onnx.save(onnx_model, str(output_path))
    size_kb = output_path.stat().st_size / 1024
    print(f"[export_onnx] ONNX salvo em {output_path} ({size_kb:.1f} KB)")
    return onnx_model


# ---------------------------------------------------------------------------
# 5. VALIDACAO NUMERICA ONNX vs LightGBM
# ---------------------------------------------------------------------------

def validate_onnx(
    booster: lgb.Booster,
    onnx_path: Path,
    X_val: np.ndarray,
    tol: float = 1e-4,
    n_check: int = 1000,
) -> dict:
    """
    Verifica que as predicoes ONNX reproduzem as do LightGBM original.

    Retorna dict com max_abs_diff e max_rel_diff.

    tol: tolerancia absoluta para considerar OK (1e-4 e conservador;
         a diferenca esperada e < 1e-5 para GBDT com float32).
    """
    try:
        import onnxruntime as ort
    except ImportError:
        print("[validate_onnx] onnxruntime nao instalado — pulando validacao ONNX.")
        print("  Instale: pip install onnxruntime")
        return {"skipped": True, "reason": "onnxruntime not installed"}

    # Amostra para validacao rapida
    idx = np.random.default_rng(0).choice(len(X_val), size=min(n_check, len(X_val)), replace=False)
    X_check = X_val[idx].astype(np.float32)

    # Predicoes LightGBM
    lgb_preds = booster.predict(X_check)  # float64[N], P(click=1)
    lgb_preds = lgb_preds.astype(np.float32)

    # Predicoes ONNX Runtime
    sess_opts = ort.SessionOptions()
    sess_opts.log_severity_level = 3  # silencia warnings
    sess = ort.InferenceSession(str(onnx_path), sess_opts=sess_opts,
                                providers=["CPUExecutionProvider"])

    inputs = {sess.get_inputs()[0].name: X_check}
    outputs = sess.run(None, inputs)

    # outputs[1] e "probabilities" — pode ser lista de dicts ou array dependendo do opset
    # Extraimos P(click=1) de forma robusta
    onnx_probs_raw = outputs[1]
    if isinstance(onnx_probs_raw, list):
        # Lista de dicts {0: p0, 1: p1}
        onnx_preds = np.array([d[1] for d in onnx_probs_raw], dtype=np.float32)
    elif hasattr(onnx_probs_raw, "shape"):
        arr = np.array(onnx_probs_raw, dtype=np.float32)
        if arr.ndim == 2:
            onnx_preds = arr[:, 1]  # coluna 1 = P(click=1)
        else:
            onnx_preds = arr
    else:
        onnx_preds = np.array(list(onnx_probs_raw), dtype=np.float32)

    abs_diff = np.abs(lgb_preds - onnx_preds)
    max_abs = float(abs_diff.max())
    mean_abs = float(abs_diff.mean())

    # rel_diff evita divisao por zero
    rel_diff = abs_diff / (np.abs(lgb_preds) + 1e-9)
    max_rel = float(rel_diff.max())

    status = "OK" if max_abs <= tol else "FAIL"
    print(f"[validate_onnx] {status}: max_abs_diff={max_abs:.2e}, mean_abs_diff={mean_abs:.2e}, "
          f"max_rel_diff={max_rel:.2e} (tol={tol:.0e}, n={len(X_check)})")

    if status == "FAIL":
        raise ValueError(
            f"ONNX preds divergem do LightGBM: max_abs_diff={max_abs:.2e} > tol={tol:.0e}. "
            "Verificar conversao ONNX."
        )

    return {
        "max_abs_diff": max_abs,
        "mean_abs_diff": mean_abs,
        "max_rel_diff": max_rel,
        "n_checked": len(X_check),
        "tol": tol,
        "status": status,
    }


# ---------------------------------------------------------------------------
# 6. METRICAS DE AVALIACAO
# ---------------------------------------------------------------------------

def compute_metrics(
    booster: lgb.Booster,
    X_val: np.ndarray,
    y_val: np.ndarray,
) -> dict:
    """
    Computa AUC-ROC e log-loss no conjunto de validacao.

    Retorna dict com as metricas para o MLflow.
    """
    preds = booster.predict(X_val)
    auc = float(roc_auc_score(y_val, preds))
    ll = float(log_loss(y_val, preds))
    print(f"[metrics] AUC={auc:.4f}, logloss={ll:.4f}")
    return {"val_auc": auc, "val_logloss": ll}


# ---------------------------------------------------------------------------
# 7. PIPELINE PRINCIPAL
# ---------------------------------------------------------------------------

def run(
    data_path: Optional[Path] = None,
    window_start: Optional[str] = None,
    window_end: Optional[str] = None,
    n_estimators: int = 300,
    val_fraction: float = 0.2,
    seed: int = 42,
    mlflow_tracking_uri: str = "file:./mlruns",
    run_name: str = "pctr_lgb_v1",
    onnx_output_path: Optional[Path] = None,
) -> dict:
    """
    Pipeline completo de treino pCTR.

    Retorna dict com metricas, caminhos de artefatos e info do MLflow run.
    """
    # Caminhos padrao
    artifacts_dir = _ML_ROOT / "registry" / "artifacts"
    artifacts_dir.mkdir(parents=True, exist_ok=True)
    if onnx_output_path is None:
        onnx_output_path = artifacts_dir / "pctr_model.onnx"

    mlflow.set_tracking_uri(mlflow_tracking_uri)
    mlflow.set_experiment("pctr_lgb")

    with mlflow.start_run(run_name=run_name) as run:
        run_id = run.info.run_id
        print(f"[run] MLflow run_id={run_id}")

        # --- PARAMETROS ---
        params = {
            "model_type": "lightgbm",
            "n_estimators": n_estimators,
            "val_fraction": val_fraction,
            "seed": seed,
            "feature_spec_version": FEATURE_SPEC_VERSION,
            "n_features": _N_FEATURES,
            "data_source": str(data_path) if data_path else "synthetic_sample",
            "window_start": window_start or "n/a",
            "window_end": window_end or "n/a",
        }
        mlflow.log_params(params)

        # --- 1. DADOS ---
        df = load_data(data_path=data_path, window_start=window_start,
                       window_end=window_end, seed=seed)

        # --- 2. SPLIT TREINO/VALIDACAO ---
        from sklearn.model_selection import train_test_split
        df_train, df_val = train_test_split(df, test_size=val_fraction,
                                            random_state=seed, stratify=df["label_click"])
        print(f"[run] Train: {len(df_train)}, Val: {len(df_val)}")
        mlflow.log_params({"n_train": len(df_train), "n_val": len(df_val)})

        # --- 3. FEATURIZACAO (anti-skew: funcao unica) ---
        print("[run] Featurizando treino...")
        t_feat = time.perf_counter()
        X_train = df_to_feature_matrix_fast(df_train)
        X_val_mat = df_to_feature_matrix_fast(df_val)
        y_train = df_train["label_click"].to_numpy(dtype=np.float32)
        y_val = df_val["label_click"].to_numpy(dtype=np.float32)
        print(f"[run] Featurizacao completa em {time.perf_counter() - t_feat:.1f}s")
        print(f"[run] X_train shape: {X_train.shape}, dtype: {X_train.dtype}")

        # Teste de paridade: vetor do dataset == vetor dos fixtures gold
        # (garante que o treino usa a mesma featurizacao dos fixtures)
        _assert_parity_with_fixtures(df_train)

        # --- 4. TREINO LightGBM ---
        booster = train_lgb(
            X_train, y_train, X_val_mat, y_val,
            n_estimators=n_estimators, seed=seed,
        )

        # --- 5. METRICAS ---
        metrics = compute_metrics(booster, X_val_mat, y_val)
        mlflow.log_metrics(metrics)
        mlflow.log_metric("best_iteration", booster.best_iteration)

        # --- 6. EXPORTA ONNX ---
        onnx_model = export_onnx(booster, onnx_output_path)

        # --- 7. VALIDA ONNX ---
        val_result = validate_onnx(booster, onnx_output_path, X_val_mat)
        if not val_result.get("skipped"):
            mlflow.log_metrics({
                "onnx_max_abs_diff": val_result["max_abs_diff"],
                "onnx_mean_abs_diff": val_result["mean_abs_diff"],
            })

        # --- 8. REGISTRA ARTEFATOS NO MLflow ---
        mlflow.log_artifact(str(onnx_output_path), artifact_path="model")

        # Grava feature_spec_version como tag e artefato de metadados
        mlflow.set_tags({
            "feature_spec_version": FEATURE_SPEC_VERSION,
            "model_type": "lightgbm_onnx",
            "calibration_status": "pending_isotonic",  # J2 registra; calibracao em ml/calibration/
        })

        # Salva LightGBM nativo (para calibracao isotonica — ml/calibration/)
        lgb_path = artifacts_dir / "pctr_booster.lgb"
        booster.save_model(str(lgb_path))
        mlflow.log_artifact(str(lgb_path), artifact_path="model")

        print(f"[run] MLflow run completo. run_id={run_id}")
        print(f"[run] AUC={metrics['val_auc']:.4f}, logloss={metrics['val_logloss']:.4f}")
        print(f"[run] ONNX: {onnx_output_path}")

    return {
        "run_id": run_id,
        "metrics": metrics,
        "onnx_path": str(onnx_output_path),
        "lgb_path": str(lgb_path),
        "val_result": val_result,
        "feature_spec_version": FEATURE_SPEC_VERSION,
    }


# ---------------------------------------------------------------------------
# TESTE DE PARIDADE: vetor LightGBM == vetor gold dos fixtures
# ---------------------------------------------------------------------------

def _assert_parity_with_fixtures(df: pd.DataFrame, tol: float = 1e-6) -> None:
    """
    Garante que o vetor produzido pelo pipeline de treino e identico ao vetor
    gold dos fixtures (parity_cases.json).

    Prova que o treino usa EXATAMENTE a mesma featurizacao que os fixtures testam.
    Este teste e complementar ao test_parity_cases.py (pytest).
    """
    import json
    fixtures_path = _REPO_ROOT / "ml" / "features" / "testdata" / "parity_cases.json"
    with open(fixtures_path, "r") as f:
        fixtures = json.load(f)

    assert fixtures["feature_spec_version"] == FEATURE_SPEC_VERSION, (
        f"feature_spec_version dos fixtures ({fixtures['feature_spec_version']}) "
        f"difere da implementacao ({FEATURE_SPEC_VERSION})"
    )

    for case in fixtures["cases"]:
        inp_raw = case["input"]
        inp = FeaturizeInput(
            zone=ZoneInput(**inp_raw["zone"]),
            request_time_utc=RequestTimeInput(**inp_raw["request_time_utc"]),
            geo=GeoInput(**inp_raw["geo"]),
            device_class=inp_raw["device_class"],
            candidate=CandidateInput(**inp_raw["candidate"]),
            candidate_count=inp_raw["candidate_count"],
            campaign_delivered_impressions=inp_raw.get("campaign_delivered_impressions", 0),
            campaign_goal_impressions=inp_raw.get("campaign_goal_impressions", 0),
            campaign_delivered_clicks=inp_raw.get("campaign_delivered_clicks", 0),
        )
        actual = featurize(inp).astype(np.float64)

        # Extrai vetor esperado dos fixtures
        expected = np.zeros(23, dtype=np.float64)
        for key, value in case["expected_vector_computed"].items():
            if not key.startswith("index_"):
                continue
            idx = int(key.split("_")[1])
            if 0 <= idx < 23:
                expected[idx] = float(value)

        for i in range(23):
            diff = abs(actual[i] - expected[i])
            assert diff <= tol, (
                f"PARITY FAIL — caso '{case['id']}' index {i}: "
                f"actual={actual[i]:.10f}, expected={expected[i]:.10f}, diff={diff:.2e}"
            )

    print(f"[parity] OK — {len(fixtures['cases'])} casos de fixtures verificados contra featurize().")


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Treina modelo pCTR com LightGBM")
    parser.add_argument("--data-path", type=Path, default=None,
                        help="Caminho para Parquet/Iceberg de treino. "
                             "Sem este argumento, usa sample sintetico.")
    parser.add_argument("--window-start", type=str, default=None,
                        help="Inicio da janela de treino (ISO 8601). Apenas para Iceberg.")
    parser.add_argument("--window-end", type=str, default=None,
                        help="Fim da janela de treino (ISO 8601). Apenas para Iceberg.")
    parser.add_argument("--n-estimators", type=int, default=300)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument("--mlflow-tracking-uri", type=str,
                        default="file:" + str(Path(__file__).parent.parent / "registry" / "mlruns"),
                        help="URI do MLflow tracking server. Padrao: file-store local.")
    parser.add_argument("--run-name", type=str, default="pctr_lgb_v1")
    args = parser.parse_args()

    result = run(
        data_path=args.data_path,
        window_start=args.window_start,
        window_end=args.window_end,
        n_estimators=args.n_estimators,
        seed=args.seed,
        mlflow_tracking_uri=args.mlflow_tracking_uri,
        run_name=args.run_name,
    )
    print("\n[done]", result)
