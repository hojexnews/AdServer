"""
ml/calibration/test_calibrate.py
================================
Testes known-answer da calibracao isotonica + ECE (sem dados reais — sinteticos).

Fecha o falso-positivo detectado na auditoria pos-G0: o alvo `ml-calibration-test`
(dependencia do gate canonico `make ml-test`, citado no runbook §5 e no README/ADR-0003
J2 como "calibracao coberta/verde") rodava `pytest ml/calibration/` sobre um diretorio
SEM teste algum -> pytest exit 5 ("no tests collected") -> mascarado como OK. Estrutural-
mente nunca podia falhar sobre a logica de calibracao. Estes testes exercitam de fato
compute_ece / fit_calibration / apply_calibration / save_calibration_map / run_calibration
com valores esperados fixos, de modo que uma regressao (inversao de sinal, perda de
monotonicidade, off-by-one de bin, quebra do contrato do mapa) reprova o gate.

TX-2: pCTR e probabilidade [0,1] — float e correto aqui; nenhum valor monetario e computado.
"""
from __future__ import annotations

import json
import math

import numpy as np

from ml.calibration.calibrate import (
    apply_calibration,
    compute_ece,
    fit_calibration,
    run_calibration,
    save_calibration_map,
)


# ---------------------------------------------------------------------------
# compute_ece — known-answer (pega inversao de sinal / bin-weighting errado)
# ---------------------------------------------------------------------------

def test_ece_perfeitamente_calibrado_e_zero():
    # y_pred == y_true (0/1): bin 0 (pred=0, frac_pos=0) e bin 10 (pred=1, frac_pos=1)
    # -> avg_conf == frac_pos em cada bin -> ECE = 0.
    y_true = np.array([0, 0, 1, 1, 0, 1])
    y_pred = y_true.astype(float)
    assert compute_ece(y_true, y_pred) == 0.0


def test_ece_valor_hand_computed():
    # Todos em [0.5,0.6): avg_conf=0.5; 1 de 4 positivos -> frac_pos=0.25.
    # ECE = (4/4) * |0.5 - 0.25| = 0.25 exato.
    y_pred = np.array([0.5, 0.5, 0.5, 0.5])
    y_true = np.array([1, 0, 0, 0])
    assert math.isclose(compute_ece(y_true, y_pred), 0.25, abs_tol=1e-9)


def test_ece_usa_valor_absoluto_frac_pos_maior_que_conf():
    # frac_pos (1.0) > avg_conf (0.2): sem abs() daria negativo. Prova o abs().
    y_pred = np.array([0.2, 0.2])
    y_true = np.array([1, 1])
    assert math.isclose(compute_ece(y_true, y_pred), 0.8, abs_tol=1e-9)


def test_ece_pondera_por_tamanho_de_bin():
    # bin [0.1,0.2): 4 amostras, conf~0.15, frac_pos=0 -> contrib (4/5)*0.15=0.12
    # bin [0.9,1.0]: 1 amostra, conf~0.95, frac_pos=1 -> contrib (1/5)*0.05=0.01
    # Total = 0.13. Um bin-weighting uniforme (1/n_bins) daria valor diferente.
    y_pred = np.array([0.15, 0.15, 0.15, 0.15, 0.95])
    y_true = np.array([0, 0, 0, 0, 1])
    expected = (4 / 5) * abs(0.15 - 0.0) + (1 / 5) * abs(0.95 - 1.0)
    assert math.isclose(compute_ece(y_true, y_pred), expected, abs_tol=1e-9)


# ---------------------------------------------------------------------------
# fit_calibration — monotonicidade (pre-condicao do np.interp no sidecar)
# ---------------------------------------------------------------------------

def test_fit_calibration_thresholds_e_probs_nao_decrescentes():
    rng = np.random.default_rng(42)
    # Sinal monotono ruidoso: y correlaciona com raw, mas mal-calibrado.
    raw = rng.uniform(0.0, 1.0, size=2000)
    y = (rng.uniform(0.0, 1.0, size=2000) < (raw ** 2)).astype(int)  # sub-confiante

    cal = fit_calibration(raw, y)
    thr = np.asarray(cal["thresholds"])
    probs = np.asarray(cal["calibrated_probs"])

    # Contrato consumido pelo sidecar (np.interp exige thresholds ordenados).
    assert np.all(np.diff(thr) >= 0), "thresholds devem ser nao-decrescentes"
    # IsotonicRegression(increasing=True) -> calibrated_probs nao-decrescente.
    assert np.all(np.diff(probs) >= -1e-9), "calibrated_probs devem ser nao-decrescentes"
    assert len(thr) == len(probs)


def test_fit_calibration_melhora_ou_mantem_ece():
    rng = np.random.default_rng(7)
    raw = rng.uniform(0.0, 1.0, size=3000)
    y = (rng.uniform(0.0, 1.0, size=3000) < (raw ** 2)).astype(int)
    cal = fit_calibration(raw, y)
    # Calibracao no proprio conjunto nunca deve piorar o ECE.
    assert cal["ece_after"] <= cal["ece_before"] + 1e-9


# ---------------------------------------------------------------------------
# apply_calibration — interpolacao linear known-answer
# ---------------------------------------------------------------------------

def test_apply_calibration_interpola_linearmente():
    cal = {"thresholds": [0.0, 0.5, 1.0], "calibrated_probs": [0.0, 0.2, 0.8]}
    out = apply_calibration(np.array([0.0, 0.25, 0.5, 0.75, 1.0]), cal)
    # interp: 0->0 ; 0.25->0.1 ; 0.5->0.2 ; 0.75->0.5 ; 1.0->0.8
    np.testing.assert_allclose(out, [0.0, 0.1, 0.2, 0.5, 0.8], atol=1e-6)


def test_apply_calibration_clipa_fora_dos_limites():
    cal = {"thresholds": [0.2, 0.8], "calibrated_probs": [0.1, 0.9]}
    out = apply_calibration(np.array([0.0, 1.0]), cal)
    # np.interp faz clip nas bordas: <0.2 -> 0.1 ; >0.8 -> 0.9
    np.testing.assert_allclose(out, [0.1, 0.9], atol=1e-6)


# ---------------------------------------------------------------------------
# save_calibration_map — round-trip + contrato de schema (mapa "invertido")
# ---------------------------------------------------------------------------

def test_save_calibration_map_roundtrip_schema(tmp_path):
    cal_result = {
        "thresholds": [0.0, 0.3333333, 1.0],
        "calibrated_probs": [0.0, 0.25, 0.75],
        "ece_before": 0.2,
        "ece_after": 0.05,
    }
    out = tmp_path / "calibration_map.json"
    saved = save_calibration_map(cal_result, out, mlflow_run_id="run-xyz")

    reloaded = json.loads(out.read_text())
    assert reloaded["calibration_method"] == "isotonic"
    assert reloaded["mlflow_run_id"] == "run-xyz"
    assert set(reloaded) >= {
        "calibration_method", "feature_spec_version", "mlflow_run_id",
        "calibrated_at", "ece_before", "ece_after", "thresholds", "calibrated_probs",
    }
    # Contrato consumido pelo sidecar: thresholds ordenados e mesmo comprimento.
    assert len(reloaded["thresholds"]) == len(reloaded["calibrated_probs"])
    assert reloaded["thresholds"] == sorted(reloaded["thresholds"])
    # float32 truncation aplicada e consistente com o retorno.
    assert reloaded["thresholds"] == saved["thresholds"]


# ---------------------------------------------------------------------------
# run_calibration — pipeline completo (usa o invariante interno de round-trip)
# ---------------------------------------------------------------------------

class _DummyBooster:
    """Booster falso: .predict devolve raw_probs fixos (sem treinar LightGBM real)."""

    def __init__(self, raw_probs: np.ndarray):
        self._raw = np.asarray(raw_probs, dtype=np.float64)

    def predict(self, X):  # noqa: N803 (assinatura do lgb.Booster)
        return self._raw


def test_run_calibration_pipeline_round_trip(tmp_path):
    rng = np.random.default_rng(123)
    raw = rng.uniform(0.0, 1.0, size=1500)
    y = (rng.uniform(0.0, 1.0, size=1500) < (raw ** 2)).astype(int)
    X = np.zeros((len(raw), 23), dtype=np.float32)  # ignorado pelo dummy

    out = tmp_path / "artifacts" / "calibration_map.json"
    # run_calibration contem `assert abs(ece_final - ece_after) < 1e-4` internamente;
    # se apply_calibration divergir do fit, o pipeline levanta AssertionError e reprova.
    result = run_calibration(_DummyBooster(raw), X, y, output_path=out)

    assert out.exists()
    assert result["ece_after"] <= result["ece_before"] + 1e-9
    assert result["n_calibration_points"] == len(result["calibration_map"]["thresholds"])
    assert result["ece_improvement"] == result["ece_before"] - result["ece_after"]
