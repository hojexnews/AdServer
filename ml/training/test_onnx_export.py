"""
ml/training/test_onnx_export.py
================================
Gate de CI para o contrato de artefato ONNX do sidecar (G0/E11).

Prova, com um booster sintético minúsculo (segundos, sem dados reais), que
`export_onnx` produz o grafo que o sidecar Go (services/ranker-sidecar/
internal/onnx) exige:

  1. O output "probabilities" e um TENSOR PLANO rank-2 [N, 2] float32 —
     NAO um ZipMap seq(map(int64, tensor(float))). (Uma regressao acidental
     de volta ao ZipMap quebraria o binding Go silenciosamente; hoje o unico
     outro sinal e o parity test `-tags onnx`, que da t.Skip em CI sem a lib.)
  2. A coluna 1 do output ("probabilities"[:, 1]) == booster.predict (P(click=1))
     dentro da tolerancia de serving (1e-5).
  3. O input e "features" float32 [None, 23].

Este teste roda no `make ml-training-test` (CI normal, so onnxruntime Python —
NAO precisa de libonnxruntime.so nativa nem do build tag onnx do Go). E o
co-gate green-path recomendado pelos gates tech-lead-architect e
parity-golden-test-guardian ao aprovar E11.

COMO EXECUTAR:
  cd /home/agencia/Hojex News/AdServer
  PYTHONPATH=. ./ml/.venv/bin/python -m pytest ml/training/test_onnx_export.py -v
"""
from __future__ import annotations

import sys
from pathlib import Path

import numpy as np
import pytest

_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

# Skip cleanly (não falha) se o ambiente não tiver as libs de export/serving.
lgb = pytest.importorskip("lightgbm")
ort = pytest.importorskip("onnxruntime")
pytest.importorskip("onnxmltools")

from ml.training.train_pctr import _N_FEATURES, export_onnx

_SERVING_TOL = 1e-5


def _train_tiny_booster(seed: int = 0) -> "lgb.Booster":
    """Booster binário sintético — 23 features, sinal separável, treino em ms."""
    rng = np.random.default_rng(seed)
    n = 256
    X = rng.random((n, _N_FEATURES)).astype(np.float32)
    # Rótulo aprendível (não-degenerado) para o booster não colapsar em constante.
    logit = 2.0 * X[:, 0] + 1.5 * X[:, 1] - 1.0 * X[:, 2] - 1.25
    y = (logit + rng.normal(0, 0.1, size=n) > 0).astype(np.int64)
    dtrain = lgb.Dataset(
        X,
        label=y,
        feature_name=[f"f{i}" for i in range(_N_FEATURES)],
    )
    params = {
        "objective": "binary",
        "num_leaves": 7,
        "min_data_in_leaf": 5,
        "learning_rate": 0.1,
        "verbose": -1,
        "seed": seed,
        "deterministic": True,
    }
    return lgb.train(params, dtrain, num_boost_round=25)


def test_export_onnx_probabilities_is_flat_tensor_not_zipmap(tmp_path):
    booster = _train_tiny_booster()
    out = tmp_path / "pctr_model.onnx"
    export_onnx(booster, out)

    sess = ort.InferenceSession(str(out), providers=["CPUExecutionProvider"])

    # (3) Input contract: "features" float32 [None, 23].
    inp = sess.get_inputs()[0]
    assert inp.name == "features", f"input name = {inp.name!r}, esperado 'features'"
    assert inp.shape[-1] == _N_FEATURES, f"input width = {inp.shape[-1]}, esperado {_N_FEATURES}"
    assert "float" in inp.type, f"input type = {inp.type!r}, esperado tensor(float)"

    # (1) "probabilities" DEVE ser tensor plano — NÃO ZipMap seq(map(...)).
    prob_out = next(o for o in sess.get_outputs() if o.name == "probabilities")
    assert prob_out.type == "tensor(float)", (
        f"REGRESSÃO ZipMap: 'probabilities' type = {prob_out.type!r} — "
        f"esperado 'tensor(float)' (export_onnx deve usar zipmap=False). "
        f"seq(map(...)) quebra o binding Go do sidecar (yalue/onnxruntime_go)."
    )
    assert prob_out.shape[-1] == 2, f"'probabilities' width = {prob_out.shape[-1]}, esperado 2 (P0,P1)"


def test_export_onnx_column1_matches_booster_predict(tmp_path):
    booster = _train_tiny_booster()
    out = tmp_path / "pctr_model.onnx"
    export_onnx(booster, out)

    sess = ort.InferenceSession(str(out), providers=["CPUExecutionProvider"])

    rng = np.random.default_rng(123)
    X = rng.random((64, _N_FEATURES)).astype(np.float32)

    # ONNX: coluna 1 de "probabilities" = P(click=1) — o mesmo que o sidecar lê (data[1]).
    probs = np.asarray(sess.run(["probabilities"], {"features": X})[0])
    assert probs.ndim == 2 and probs.shape[1] == 2, f"probabilities shape = {probs.shape}, esperado [N,2]"
    onnx_p1 = probs[:, 1]

    ref = booster.predict(X)  # float64[N] = P(click=1)

    max_diff = float(np.max(np.abs(onnx_p1 - ref)))
    assert max_diff < _SERVING_TOL, (
        f"booster.predict != ONNX probabilities[:,1]: max_abs_diff={max_diff:.2e} "
        f">= tol={_SERVING_TOL:.0e} — o artefato de serving diverge do booster."
    )
