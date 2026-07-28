#!/usr/bin/env python3
"""
services/ranker-sidecar/scripts/gen_calibration_fixtures.py
=============================================================
Deterministic model + calibration-map generator for the ranker-sidecar's
`-tags onnx` Go parity tests:

  - services/ranker-sidecar/internal/onnx/onnx_parity_test.go
    (TestOnnxScoreParity)
  - services/ranker-sidecar/internal/wiring/calibration_parity_test.go
    (TestCalibratedServingParity — the HIGH "calibration-never-applied-in-
    serving" gate: proves wiring.BuildInferencer actually calls
    calibration.Wrap in the exact production wiring, not a reimplementation)

WHY THIS EXISTS (context: 30th-wave remediation)
--------------------------------------------------
ml/registry/artifacts/{pctr_model.onnx,calibration_map.json} are gitignored
build outputs (ADR-0003 §F: "modelos compilados nao sao versionados no git
como blobs") — a fresh CI checkout never has them. Both parity tests above
`t.Skip` cleanly when the model/calibration files are absent, which is
exactly how they silently never ran in any CI job before this script: no
workflow ever generated the artifacts, so the `-tags onnx` build was never
even attempted, let alone exercised end-to-end.

This script trains a TINY synthetic LightGBM booster (milliseconds, not the
full production ~40k-row pipeline in ml/training/train_pctr.py `run()`) and
runs it through the REAL production export/calibration code paths
(`export_onnx`, `run_calibration` — unmodified, imported directly from
ml/training/train_pctr.py and ml/calibration/calibrate.py) so the artifacts
it produces are structurally identical to what production ships, just fast
and reproducible enough to run on every CI invocation.

DETERMINISM CONTRACT
---------------------
`--seed` is fixed (see SEED below) and LightGBM is trained with
`deterministic=True, force_row_wise=True, num_threads=1` — verified by
running this script twice and diffing the resulting
testdata/*_golden.json score files (see the regeneration note in
onnx_parity_test.go / calibration_parity_test.go): the SCORES are
bit-identical across runs (the .onnx/calibration_map.json file BYTES are
not, because onnxmltools/save_calibration_map embed a wall-clock
timestamp — that does not affect the numeric graph or the interpolation
table, only metadata fields no test reads).

TWO MODES
---------
1. Default (CI mode) — regenerates ONLY the runtime artifacts
   (ml/registry/artifacts/pctr_model.onnx, calibration_map.json). Does NOT
   touch the committed golden JSON fixtures. This is what CI runs before
   `go test -tags onnx`: the freshly generated artifacts reproduce the
   SAME scores the committed goldens were captured from (see determinism
   contract above), so the comparison in the Go tests passes.

2. `--write-golden` (manual/regeneration mode, NOT run by CI) — additionally
   overwrites the two committed golden fixtures
   (internal/onnx/testdata/score_golden.json,
   internal/wiring/testdata/calibrated_score_golden.json) with scores
   computed from the artifacts this same invocation just produced. Run this
   only when intentionally changing the fixture recipe (e.g. bumping SEED,
   changing the synthetic training signal) — commit the regenerated goldens
   alongside the recipe change so they never drift apart.

USAGE
-----
  PYTHONPATH=. ml/.venv/bin/python \\
    services/ranker-sidecar/scripts/gen_calibration_fixtures.py

  # after intentionally changing the recipe below:
  PYTHONPATH=. ml/.venv/bin/python \\
    services/ranker-sidecar/scripts/gen_calibration_fixtures.py --write-golden
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np

_REPO_ROOT = Path(__file__).resolve().parent.parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

# SEED is part of the determinism contract above — changing it changes every
# score in both golden fixtures; regenerate with --write-golden if you do.
SEED = 20260719

_ONNX_GOLDEN_PATH = _REPO_ROOT / "services" / "ranker-sidecar" / "internal" / "onnx" / "testdata" / "score_golden.json"
_CAL_GOLDEN_PATH = _REPO_ROOT / "services" / "ranker-sidecar" / "internal" / "wiring" / "testdata" / "calibrated_score_golden.json"
_PARITY_CASES_PATH = _REPO_ROOT / "ml" / "features" / "testdata" / "parity_cases.json"
_ARTIFACTS_DIR = _REPO_ROOT / "ml" / "registry" / "artifacts"


def _train_tiny_booster(seed: int, n_features: int):
    import lightgbm as lgb

    rng = np.random.default_rng(seed)
    n = 2000
    X = rng.random((n, n_features)).astype(np.float32)
    # Learnable (non-degenerate) synthetic signal — enough for the booster to
    # produce a non-trivial isotonic calibration curve. The FEATURE SEMANTICS
    # are irrelevant here: these tests prove serving PLUMBING (does the
    # wire-format Go score match Python's independent computation over the
    # SAME artifact), not model quality.
    logit = 2.0 * X[:, 0] + 1.5 * X[:, 1] - 1.0 * X[:, 2] - 1.25
    y = (logit + rng.normal(0, 0.1, size=n) > 0).astype(np.int64)
    dtrain = lgb.Dataset(X, label=y, feature_name=[f"f{i}" for i in range(n_features)])
    params = {
        "objective": "binary",
        "num_leaves": 7,
        "min_data_in_leaf": 5,
        "learning_rate": 0.1,
        "verbose": -1,
        "seed": seed,
        "deterministic": True,
        "force_row_wise": True,
        "num_threads": 1,
    }
    booster = lgb.train(params, dtrain, num_boost_round=25)
    return booster, X, y


def generate_artifacts(seed: int = SEED) -> tuple[Path, Path]:
    """Trains the tiny booster and writes pctr_model.onnx + calibration_map.json
    to ml/registry/artifacts/ via the REAL production export_onnx/run_calibration
    functions. Returns (onnx_path, calibration_path)."""
    from ml.training.train_pctr import export_onnx, _N_FEATURES
    from ml.calibration.calibrate import run_calibration

    booster, X, y = _train_tiny_booster(seed, _N_FEATURES)

    _ARTIFACTS_DIR.mkdir(parents=True, exist_ok=True)
    onnx_path = _ARTIFACTS_DIR / "pctr_model.onnx"
    export_onnx(booster, onnx_path)

    cal_path = _ARTIFACTS_DIR / "calibration_map.json"
    cal_out = run_calibration(
        booster, X, y,
        output_path=cal_path,
        mlflow_run_id="ci-synthetic-ranker-sidecar-parity",
        feature_spec_version="1.0.0",
    )
    print(f"[gen_calibration_fixtures] artifacts written: {onnx_path}, {cal_path} "
          f"(ece_before={cal_out['ece_before']:.6f}, ece_after={cal_out['ece_after']:.6f})")
    return onnx_path, cal_path


def _write_golden(onnx_path: Path, cal_path: Path) -> None:
    import onnxruntime as ort
    from ml.training.train_pctr import _N_FEATURES
    from ml.calibration.calibrate import apply_calibration

    with open(_PARITY_CASES_PATH) as f:
        fixtures = json.load(f)
    with open(cal_path) as f:
        cal_map_json = json.load(f)
    cal_result = {"thresholds": cal_map_json["thresholds"], "calibrated_probs": cal_map_json["calibrated_probs"]}

    sess = ort.InferenceSession(str(onnx_path), providers=["CPUExecutionProvider"])
    input_name = sess.get_inputs()[0].name

    onnx_cases = []
    cal_cases = []
    min_gap = None
    for case in fixtures["cases"]:
        vec = [0.0] * _N_FEATURES
        for key, value in case["expected_vector_computed"].items():
            if not key.startswith("index_"):
                continue
            vec[int(key.split("_")[1])] = float(value)
        X_in = np.array([vec], dtype=np.float32)
        raw = float(sess.run(None, {input_name: X_in})[1][0, 1])
        cal = float(apply_calibration(np.array([raw], dtype=np.float64), cal_result)[0])
        gap = abs(cal - raw)
        min_gap = gap if min_gap is None else min(min_gap, gap)
        onnx_cases.append({"id": case["id"], "features": vec, "expected_score": raw})
        cal_cases.append({
            "id": case["id"], "features": vec,
            "expected_raw_score": raw, "expected_calibrated_score": cal,
        })

    # Mirrors calibration_parity_test.go's minRawCalGap self-check: a golden
    # this test can't discriminate calibrated-vs-raw with is worse than no
    # golden at all.
    assert min_gap is not None and min_gap >= 1e-3, (
        f"min |raw-calibrated| gap across golden cases = {min_gap!r} < 1e-3 — "
        "this recipe cannot discriminate a calibrated serve from an uncalibrated "
        "one; change SEED or the synthetic training signal before writing goldens."
    )

    onnx_golden = {
        "feature_spec_version": fixtures["feature_spec_version"],
        "model_path": "ml/registry/artifacts/pctr_model.onnx",
        "cases": onnx_cases,
    }
    cal_golden = {
        "feature_spec_version": fixtures["feature_spec_version"],
        "model_path": "ml/registry/artifacts/pctr_model.onnx",
        "calibration_path": "ml/registry/artifacts/calibration_map.json",
        "cases": cal_cases,
    }

    _ONNX_GOLDEN_PATH.write_text(json.dumps(onnx_golden, indent=2) + "\n")
    _CAL_GOLDEN_PATH.write_text(json.dumps(cal_golden, indent=2) + "\n")
    print(f"[gen_calibration_fixtures] wrote {_ONNX_GOLDEN_PATH}")
    print(f"[gen_calibration_fixtures] wrote {_CAL_GOLDEN_PATH}")
    print(f"[gen_calibration_fixtures] min |raw-calibrated| gap = {min_gap:.4e}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seed", type=int, default=SEED)
    parser.add_argument("--write-golden", action="store_true",
                         help="Also overwrite the committed golden JSON fixtures "
                              "(manual regeneration only — CI never passes this flag).")
    args = parser.parse_args()

    onnx_path, cal_path = generate_artifacts(args.seed)
    if args.write_golden:
        _write_golden(onnx_path, cal_path)


if __name__ == "__main__":
    main()
