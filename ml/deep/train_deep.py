"""
ml/deep/train_deep.py
=====================
Pipeline de treino do deep ranker (two-tower DCN-v2/DLRM) — K1, Fase 3.

RESPONSABILIDADES:
  1. Carrega dados (Iceberg em producao; Parquet sintetico neste ambiente).
  2. Aplica a UNICA funcao de featurizacao (ml/features/python/featurize.py)
     via vetores pre-computados no sample sintetico — anti-skew garantido.
  3. Treina TwoTowerDCNv2 (PyTorch).
  4. Exporta ONNX float32 e INT8 (quantizacao dinamica).
  5. Valida que o ONNX reproduz as predicoes do modelo PyTorch (paridade).
  6. Registra no MLflow: modelo, metricas, artefatos, feature_spec_version,
     model_family="deep_two_tower_dcnv2".

GATE K1 → K8 (ADR-0004 §H):
  - Este pipeline e CONSTRUIVEL agora sobre sample sintetico.
  - Treino com dados Iceberg reais pende de infra (cutover Fase 2).
  - Promocao para producao aguarda K8 (uplift A/B sobre GBDT com trafego real).
  - O artefato gerado fica com stage=None no registry (nao promovido).

DINHEIRO (TX-2):
  - ecpm_minor_units entra como int64 no sample; a featurizacao converte
    para log1p(float32) apenas para o vetor de ML.
  - pCTR e probabilidade [0,1] — float e correto para probabilidade.
  - Nenhum valor monetario e computado aqui.

PRIVACIDADE (TX-5/DA-11):
  - Somente features da spec 1.0.0 (todas pii_free: true).
  - Sample sintetico nao tem dados reais.

USO:
  python train_deep.py [--data-path PATH] [--n-epochs N] [--batch-size N]
                       [--mlflow-tracking-uri URI] [--run-name NAME]
                       [--no-export-onnx]
"""
from __future__ import annotations

import argparse
import os
import sys
import time
from pathlib import Path
from typing import Optional, Tuple

import numpy as np
import pandas as pd
import torch
import torch.nn as nn
from torch.utils.data import DataLoader, TensorDataset

os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

# Caminho do repo
_REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(_REPO_ROOT))

from ml.deep.model import (
    FEATURE_SPEC_VERSION,
    FEATURE_VECTOR_LENGTH,
    TwoTowerDCNv2,
)
from ml.deep.generate_deep_sample import generate as _generate_sample

# ---------------------------------------------------------------------------
# Constantes
# ---------------------------------------------------------------------------

_DEFAULT_SAMPLE_PATH = Path(__file__).parent / "testdata" / "sample_deep.parquet"
_DEFAULT_ONNX_PATH = Path(__file__).parent / "testdata" / "deep_model.onnx"
_DEFAULT_ONNX_INT8_PATH = Path(__file__).parent / "testdata" / "deep_model_int8.onnx"

# Tolerancia de paridade PyTorch <-> ONNX (analogo ao tolerance da Fase 2)
_ONNX_PARITY_TOLERANCE = 1e-4  # maior que GBDT por float16/int8 effects


# ---------------------------------------------------------------------------
# Carregamento de dados
# ---------------------------------------------------------------------------

def load_data(data_path: Path) -> Tuple[np.ndarray, np.ndarray]:
    """
    Carrega features e labels do Parquet.

    Em producao: a fonte e Iceberg (pendencia de infra). O schema das colunas
    f0..f22 e label_click e identico ao do sample sintetico.

    Returns:
        X: float32 array [N, 23]
        y: float32 array [N] — label_click binario
    """
    if not data_path.exists():
        print(f"[train_deep] Sample nao encontrado em {data_path}. Gerando...")
        _generate_sample(n_rows=10_000, output_path=data_path)

    df = pd.read_parquet(data_path)
    feature_cols = [f"f{i}" for i in range(FEATURE_VECTOR_LENGTH)]
    missing = [c for c in feature_cols if c not in df.columns]
    if missing:
        raise ValueError(
            f"[train_deep] Colunas ausentes no Parquet: {missing}. "
            f"Esperado f0..f22 (spec {FEATURE_SPEC_VERSION})."
        )

    X = df[feature_cols].values.astype(np.float32)
    y = df["label_click"].values.astype(np.float32)
    return X, y


def split_data(X: np.ndarray, y: np.ndarray, val_frac: float = 0.1) -> Tuple:
    n = len(X)
    n_val = max(1, int(n * val_frac))
    idx = np.random.default_rng(42).permutation(n)
    X_train, y_train = X[idx[n_val:]], y[idx[n_val:]]
    X_val, y_val = X[idx[:n_val]], y[idx[:n_val]]
    return X_train, y_train, X_val, y_val


# ---------------------------------------------------------------------------
# Treino
# ---------------------------------------------------------------------------

def train(
    data_path: Path,
    n_epochs: int = 5,
    batch_size: int = 256,
    lr: float = 1e-3,
    device: str = "cpu",
) -> Tuple[TwoTowerDCNv2, dict]:
    """
    Treina o TwoTowerDCNv2 e retorna o modelo e as metricas.

    Em producao (K8): device="cuda" com dados Iceberg reais.
    No K1 (scaffolding): device="cpu" com sample sintetico.
    """
    print(f"[train_deep] Carregando dados de {data_path}")
    X, y = load_data(data_path)
    X_train, y_train, X_val, y_val = split_data(X, y)
    print(f"[train_deep] Treino: {len(X_train)} | Validacao: {len(X_val)}")

    dev = torch.device(device)
    model = TwoTowerDCNv2().to(dev)
    optimizer = torch.optim.Adam(model.parameters(), lr=lr)
    criterion = nn.BCELoss()

    # DataLoaders
    ds_train = TensorDataset(
        torch.tensor(X_train, dtype=torch.float32),
        torch.tensor(y_train, dtype=torch.float32),
    )
    ds_val = TensorDataset(
        torch.tensor(X_val, dtype=torch.float32),
        torch.tensor(y_val, dtype=torch.float32),
    )
    dl_train = DataLoader(ds_train, batch_size=batch_size, shuffle=True)
    dl_val = DataLoader(ds_val, batch_size=batch_size, shuffle=False)

    metrics: dict = {}
    t0 = time.time()
    for epoch in range(1, n_epochs + 1):
        model.train()
        epoch_loss = 0.0
        for xb, yb in dl_train:
            xb, yb = xb.to(dev), yb.to(dev)
            optimizer.zero_grad()
            pred = model(xb).squeeze(-1)
            loss = criterion(pred, yb)
            loss.backward()
            optimizer.step()
            epoch_loss += loss.item() * len(xb)

        epoch_loss /= len(ds_train)

        # Validacao
        model.eval()
        val_loss = 0.0
        with torch.no_grad():
            for xb, yb in dl_val:
                xb, yb = xb.to(dev), yb.to(dev)
                pred = model(xb).squeeze(-1)
                val_loss += criterion(pred, yb).item() * len(xb)
        val_loss /= len(ds_val)

        print(f"[train_deep] Epoch {epoch}/{n_epochs} "
              f"train_loss={epoch_loss:.4f} val_loss={val_loss:.4f}")

    elapsed = time.time() - t0
    metrics = {
        "train_loss_last_epoch": epoch_loss,
        "val_loss_last_epoch": val_loss,
        "n_train": len(X_train),
        "n_val": len(X_val),
        "n_epochs": n_epochs,
        "training_time_s": elapsed,
        "feature_spec_version": FEATURE_SPEC_VERSION,
        "model_family": "deep_two_tower_dcnv2",
    }
    return model, metrics


# ---------------------------------------------------------------------------
# Export ONNX + INT8
# ---------------------------------------------------------------------------

def export_onnx(
    model: TwoTowerDCNv2,
    onnx_path: Path,
    int8_path: Optional[Path] = None,
    batch_size: int = 1,
) -> dict:
    """
    Exporta o modelo PyTorch para ONNX float32 e INT8.

    Alvo de serving: Triton Inference Server com ONNX backend (K8).
    No K1 (sem Triton ativo), o ONNX e validado localmente com onnxruntime (CPU).

    INT8: quantizacao dinamica via onnxruntime.quantization.
    Destino de producao para caber no budget TX-4 (5-8 ms p99):
      - Torre de demanda pre-computada offline.
      - INT8 reduz compute e memoria do modelo.
      - Se nao couber no budget mesmo com INT8, fail-open (cascata pura).

    Returns:
        dict com caminhos e resultado da validacao de paridade.
    """
    import onnx
    import onnxruntime as ort

    model.eval()
    dummy = torch.zeros(batch_size, FEATURE_VECTOR_LENGTH, dtype=torch.float32)

    onnx_path.parent.mkdir(parents=True, exist_ok=True)

    # Export float32
    torch.onnx.export(
        model,
        dummy,
        str(onnx_path),
        input_names=["features"],
        output_names=["pctr"],
        dynamic_axes={"features": {0: "batch_size"}, "pctr": {0: "batch_size"}},
        opset_version=17,
        do_constant_folding=True,
    )

    # Valida ONNX float32
    onnx_model = onnx.load(str(onnx_path))
    onnx.checker.check_model(onnx_model)

    # Paridade PyTorch <-> ONNX float32
    test_input = torch.rand(8, FEATURE_VECTOR_LENGTH, dtype=torch.float32)
    with torch.no_grad():
        torch_out = model(test_input).numpy()
    ort_sess = ort.InferenceSession(str(onnx_path), providers=["CPUExecutionProvider"])
    ort_out = ort_sess.run(["pctr"], {"features": test_input.numpy()})[0]

    max_diff = float(np.abs(torch_out - ort_out).max())
    parity_ok = max_diff <= _ONNX_PARITY_TOLERANCE
    print(f"[export_onnx] float32 max_diff PyTorch<->ONNX: {max_diff:.2e} "
          f"({'OK' if parity_ok else 'FALHOU — acima de ' + str(_ONNX_PARITY_TOLERANCE)})")

    result = {
        "onnx_path": str(onnx_path),
        "onnx_parity_max_diff": max_diff,
        "onnx_parity_ok": parity_ok,
        "feature_spec_version": FEATURE_SPEC_VERSION,
    }

    # INT8 quantizacao dinamica (CPU-friendly, precursor do INT8 Triton)
    if int8_path is not None:
        try:
            import tempfile
            from onnxruntime.quantization import quantize_dynamic, QuantType
            from onnxruntime.quantization.shape_inference import quant_pre_process

            # Fase de pre-processamento: resolve shape inference para BatchNorm1d
            # (batch dim dinamica vs. batch dim estatica compilada pelo torch.onnx).
            with tempfile.NamedTemporaryFile(suffix=".onnx", delete=False) as tmp:
                tmp_path = tmp.name

            try:
                quant_pre_process(str(onnx_path), tmp_path, skip_optimization=False)
                quantize_dynamic(
                    tmp_path,
                    str(int8_path),
                    weight_type=QuantType.QInt8,
                )
            finally:
                import os as _os
                if _os.path.exists(tmp_path):
                    _os.unlink(tmp_path)

            # Valida INT8
            ort_int8 = ort.InferenceSession(str(int8_path), providers=["CPUExecutionProvider"])
            int8_out = ort_int8.run(["pctr"], {"features": test_input.numpy()})[0]
            int8_diff = float(np.abs(torch_out - int8_out).max())
            print(f"[export_onnx] INT8 max_diff PyTorch<->INT8_ONNX: {int8_diff:.2e}")
            result["int8_path"] = str(int8_path)
            result["int8_parity_max_diff"] = int8_diff
        except ImportError:
            print("[export_onnx] onnxruntime.quantization nao disponivel — INT8 ignorado.")
            result["int8_path"] = None
        except Exception as e:
            # INT8 e um artefato de otimizacao; falha nao bloqueia o pipeline.
            # O float32 ONNX e o artefato primario de serving.
            print(f"[export_onnx] INT8 quantizacao falhou ({e}) — continuando sem INT8.")
            result["int8_path"] = None

    return result


# ---------------------------------------------------------------------------
# MLflow registry
# ---------------------------------------------------------------------------

def register_with_mlflow(
    model: TwoTowerDCNv2,
    metrics: dict,
    export_result: dict,
    mlflow_uri: str = "mlruns",
    run_name: str = "deep_two_tower_dcnv2_k1",
    model_name: str = "deep_two_tower_dcnv2",
) -> str:
    """
    Registra o modelo e artefatos no MLflow.

    POLITICA DE PROMOCAO (ADR-0004 §A / §H):
      O modelo e registrado com stage=None (nao promovido).
      Promocao para Staging/Production aguarda K8 (uplift A/B com trafego real).
      Nao se promove modelo deep sem prova de uplift sobre o GBDT.

    Returns:
        run_id do MLflow.
    """
    try:
        import mlflow
        import mlflow.pytorch
    except ImportError:
        print("[register_with_mlflow] mlflow nao disponivel — pulando registro.")
        return ""

    mlflow.set_tracking_uri(mlflow_uri)
    mlflow.set_experiment("deep_ranker_k1")

    with mlflow.start_run(run_name=run_name) as run:
        # Tags de auditoria
        mlflow.set_tags({
            "feature_spec_version": FEATURE_SPEC_VERSION,
            "model_family": "deep_two_tower_dcnv2",
            "phase": "fase3_k1",
            "promotion_gate": "K8_uplift_AB_required",
            "deep_enabled_default": "false",
            "data_source": "synthetic_sample",  # muda para "iceberg" em producao
        })

        # Metricas de treino
        mlflow.log_metrics({k: v for k, v in metrics.items()
                           if isinstance(v, (int, float))})

        # Artefatos ONNX
        if export_result.get("onnx_path"):
            mlflow.log_artifact(export_result["onnx_path"], artifact_path="model")
        if export_result.get("int8_path"):
            mlflow.log_artifact(export_result["int8_path"], artifact_path="model")

        # Modelo PyTorch (para re-export futuro)
        mlflow.pytorch.log_model(
            model,
            artifact_path="pytorch_model",
            registered_model_name=model_name,
        )

        run_id = run.info.run_id
        print(f"[register_with_mlflow] run_id={run_id} model={model_name} "
              f"(stage=None — nao promovido, aguarda K8)")
        return run_id


# ---------------------------------------------------------------------------
# CLI principal
# ---------------------------------------------------------------------------

def main() -> None:
    parser = argparse.ArgumentParser(
        description="Treina o deep ranker two-tower DCN-v2 (K1, Fase 3)."
    )
    parser.add_argument("--data-path", type=Path, default=_DEFAULT_SAMPLE_PATH,
                        help="Caminho do Parquet de treino (default: sample sintetico).")
    parser.add_argument("--n-epochs", type=int, default=5,
                        help="Epocas de treino (default: 5).")
    parser.add_argument("--batch-size", type=int, default=256,
                        help="Batch size (default: 256).")
    parser.add_argument("--lr", type=float, default=1e-3,
                        help="Learning rate Adam (default: 1e-3).")
    parser.add_argument("--device", type=str, default="cpu",
                        help="Dispositivo PyTorch: 'cpu' ou 'cuda' (default: cpu).")
    parser.add_argument("--onnx-path", type=Path, default=_DEFAULT_ONNX_PATH,
                        help="Caminho de saida do ONNX float32.")
    parser.add_argument("--int8-path", type=Path, default=_DEFAULT_ONNX_INT8_PATH,
                        help="Caminho de saida do ONNX INT8.")
    parser.add_argument("--no-export-onnx", action="store_true",
                        help="Pula o export ONNX (util para teste rapido).")
    parser.add_argument("--mlflow-tracking-uri", type=str, default="mlruns",
                        help="URI do MLflow tracking server.")
    parser.add_argument("--run-name", type=str, default="deep_two_tower_dcnv2_k1",
                        help="Nome do run no MLflow.")
    args = parser.parse_args()

    # 1. Treino
    model, metrics = train(
        data_path=args.data_path,
        n_epochs=args.n_epochs,
        batch_size=args.batch_size,
        lr=args.lr,
        device=args.device,
    )

    # 2. Export ONNX
    export_result: dict = {}
    if not args.no_export_onnx:
        export_result = export_onnx(
            model,
            onnx_path=args.onnx_path,
            int8_path=args.int8_path,
        )
        if not export_result.get("onnx_parity_ok", True):
            print("[train_deep] ERRO: paridade PyTorch<->ONNX falhou. "
                  "Verificar arquitetura e export.")
            sys.exit(1)
    else:
        print("[train_deep] Export ONNX pulado (--no-export-onnx).")

    # 3. MLflow
    register_with_mlflow(
        model,
        metrics,
        export_result,
        mlflow_uri=args.mlflow_tracking_uri,
        run_name=args.run_name,
    )

    print("[train_deep] Concluido. Modelo registrado (stage=None — nao promovido).")
    print("  Promocao aguarda K8 (uplift A/B com trafego real — ADR-0004 §H).")


if __name__ == "__main__":
    main()
