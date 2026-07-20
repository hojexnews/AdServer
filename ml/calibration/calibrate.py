"""
ml/calibration/calibrate.py
=============================
Calibracao isotonica do pCTR + monitor de ECE (Expected Calibration Error).

ONDE A CALIBRACAO VIVE (decisao de arquitetura):
  A calibracao e aplicada como um MAPA VERSIONADO separado do .onnx principal.
  Justificativa:
    1. O .onnx do LightGBM e compilado/frozen. Adicionar a calibracao dentro do
       mesmo grafo ONNX exigiria recompilar cada vez que se recalibra.
    2. A recalibracao pode acontecer com frequencia (novo conjunto de validacao,
       deriva de distribuicao) sem re-treinar o modelo.
    3. O sidecar (services/ranker-sidecar) aplica a calibracao em duas etapas:
         (a) infere pCTR_raw com o .onnx do LightGBM
             (services/ranker-sidecar/internal/onnx/onnx.go)
         (b) aplica isotonic_map (tabela de lookup interpolada) -> pCTR_cal
             (services/ranker-sidecar/internal/calibration/calibration.go,
             LoadMap + Map.Apply — a mesma interpolacao linear de
             apply_calibration abaixo, provada bit-a-bit em
             services/ranker-sidecar/internal/wiring/calibration_parity_test.go)
       A calibracao como mapa separado permite hot-reload independente.
       services/ranker-sidecar/internal/wiring/wiring.go (BuildInferencer)
       carrega o mapa e decide o wrap; se o mapa faltar ou for invalido,
       o sidecar serve pCTR_raw (fail-open, DA-3) e loga um aviso — nunca
       recusa iniciar nem derruba o hot path.

CONTRATO COM O SIDECAR (J1 / services/ranker-sidecar):
  Artefato: calibration_map.json (versionado no MLflow junto com o .onnx)
  Schema:
    {
      "calibration_method": "isotonic",
      "feature_spec_version": "1.0.0",
      "mlflow_run_id": "...",
      "calibrated_at": "2026-06-04T...",
      "thresholds": [t0, t1, ..., tN],   # pontos de calibracao (float32)
      "calibrated_probs": [p0, p1, ..., pN]  # pCTR calibrado por faixa (float32)
    }
  O sidecar interpola linearmente entre os pontos de (thresholds, calibrated_probs)
  para obter pCTR_cal dado pCTR_raw.

ECE (Expected Calibration Error):
  ECE = sum_b |B_b| / N * |avg_conf(B_b) - accuracy(B_b)|
  Metrica de qualidade de calibracao. Alvo: ECE < 0.05 no conjunto de validacao.
  ECE e monitorado por MLflow run e comparado entre versoes de modelo.

DINHEIRO (TX-2 / money-ledger-guardian):
  - pCTR e uma probabilidade [0,1]. Float e CORRETO para probabilidade.
  - O ponto de multiplicacao eCPM = pCTR_cal × bid_minor_units e feito
    FORA deste modulo, no motor de decisao (internal/ranker/score.go).
  - O bid e SEMPRE int64 minor-units (TX-2). A multiplicacao retorna
    um valor em minor-units (Decimal com quantizacao, nao float binario).
  - Nenhuma computacao monetaria ocorre aqui.

USO:
  from ml.calibration.calibrate import fit_calibration, compute_ece, save_calibration_map

  # Apos treinar o modelo:
  cal_map = fit_calibration(booster, X_cal, y_cal)
  ece = compute_ece(y_cal, cal_map["calibrated_probs_on_val"])
  save_calibration_map(cal_map, output_path)
"""
from __future__ import annotations

import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

import numpy as np

# Resolve path para importar featurize
_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

# ---------------------------------------------------------------------------
# ECE — Expected Calibration Error
# ---------------------------------------------------------------------------

def compute_ece(
    y_true: np.ndarray,
    y_pred: np.ndarray,
    n_bins: int = 10,
) -> float:
    """
    Calcula ECE (Expected Calibration Error) com equal-width bins.

    ECE = sum_b (|B_b| / N) * |avg_conf(B_b) - frac_pos(B_b)|

    Args:
        y_true: labels binarios (0/1), shape (N,)
        y_pred: probabilidades preditas [0,1], shape (N,)
        n_bins: numero de bins de confianca (padrao 10)

    Returns:
        ECE em [0, 1]. Quanto menor, melhor calibrado.
    """
    y_true = np.asarray(y_true, dtype=np.float64)
    y_pred = np.asarray(y_pred, dtype=np.float64)
    assert y_true.shape == y_pred.shape, "y_true e y_pred devem ter o mesmo shape"

    n = len(y_true)
    bins = np.linspace(0.0, 1.0, n_bins + 1)
    ece = 0.0

    for i in range(n_bins):
        lo, hi = bins[i], bins[i + 1]
        mask = (y_pred >= lo) & (y_pred < hi)
        if i == n_bins - 1:
            mask = (y_pred >= lo) & (y_pred <= hi)  # inclui 1.0 no ultimo bin
        n_bin = mask.sum()
        if n_bin == 0:
            continue
        avg_conf = y_pred[mask].mean()
        frac_pos = y_true[mask].mean()
        ece += (n_bin / n) * abs(avg_conf - frac_pos)

    return float(ece)


def compute_reliability_diagram(
    y_true: np.ndarray,
    y_pred: np.ndarray,
    n_bins: int = 10,
) -> dict:
    """
    Computa os dados para o diagrama de reliability (calibration curve).

    Retorna dict com:
      - bin_centers: centro de cada bin
      - frac_pos: fracao de positivos por bin
      - avg_conf: confianca media por bin
      - n_per_bin: contagem por bin
      - ece: ECE global
    """
    y_true = np.asarray(y_true, dtype=np.float64)
    y_pred = np.asarray(y_pred, dtype=np.float64)

    bins = np.linspace(0.0, 1.0, n_bins + 1)
    bin_centers = []
    frac_pos_list = []
    avg_conf_list = []
    n_per_bin = []

    for i in range(n_bins):
        lo, hi = bins[i], bins[i + 1]
        mask = (y_pred >= lo) & (y_pred < hi)
        if i == n_bins - 1:
            mask = (y_pred >= lo) & (y_pred <= hi)
        n_bin = int(mask.sum())
        if n_bin == 0:
            continue
        bin_centers.append(float((lo + hi) / 2))
        frac_pos_list.append(float(y_true[mask].mean()))
        avg_conf_list.append(float(y_pred[mask].mean()))
        n_per_bin.append(n_bin)

    ece = compute_ece(y_true, y_pred, n_bins=n_bins)

    return {
        "bin_centers": bin_centers,
        "frac_pos": frac_pos_list,
        "avg_conf": avg_conf_list,
        "n_per_bin": n_per_bin,
        "ece": ece,
        "n_bins": n_bins,
        "n_total": len(y_true),
    }


# ---------------------------------------------------------------------------
# Calibracao isotonica
# ---------------------------------------------------------------------------

def fit_calibration(
    raw_probs: np.ndarray,
    y_true: np.ndarray,
) -> dict:
    """
    Ajusta regressao isotonica sobre as probabilidades brutas do modelo.

    Usa sklearn.isotonic.IsotonicRegression (increasing=True, out_of_bounds="clip").

    Args:
        raw_probs: pCTR_raw do modelo (nao calibrado), shape (N,)
        y_true:    labels binarios (0/1), shape (N,)

    Retorna dict com:
      - isotonic_regressor: objeto sklearn ajustado
      - thresholds:         array de pontos de calibracao (X da regressao)
      - calibrated_probs:   array de pCTR calibrado por threshold
      - ece_before:         ECE antes da calibracao
      - ece_after:          ECE apos a calibracao (nos dados de calibracao)
      - reliability_before: diagrama de reliability antes
      - reliability_after:  diagrama de reliability apos
    """
    from sklearn.isotonic import IsotonicRegression

    raw_probs = np.asarray(raw_probs, dtype=np.float64)
    y_true = np.asarray(y_true, dtype=np.float64)

    # ECE antes da calibracao
    ece_before = compute_ece(y_true, raw_probs)
    rel_before = compute_reliability_diagram(y_true, raw_probs)
    print(f"[calibration] ECE antes: {ece_before:.4f}")

    # Ajusta isotonica
    iso = IsotonicRegression(increasing=True, out_of_bounds="clip")
    iso.fit(raw_probs, y_true)
    cal_probs = iso.predict(raw_probs).astype(np.float64)

    # ECE apos calibracao
    ece_after = compute_ece(y_true, cal_probs)
    rel_after = compute_reliability_diagram(y_true, cal_probs)
    print(f"[calibration] ECE apos:  {ece_after:.4f} (melhora: {ece_before - ece_after:.4f})")

    # Pontos canonicos de calibracao (os X/y da isotonica para lookup no sidecar)
    # sklearn expoe X_thresholds_ apos fit
    thresholds = iso.X_thresholds_.tolist()
    cal_vals = iso.y_thresholds_.tolist()

    return {
        "isotonic_regressor": iso,
        "thresholds": thresholds,
        "calibrated_probs": cal_vals,
        "ece_before": ece_before,
        "ece_after": ece_after,
        "reliability_before": rel_before,
        "reliability_after": rel_after,
    }


def apply_calibration(raw_probs: np.ndarray, cal_result: dict) -> np.ndarray:
    """
    Aplica o mapa de calibracao isotonica a um array de probabilidades brutas.

    Usa interpolacao linear entre os pontos (thresholds, calibrated_probs).
    Equivalente ao que o sidecar faz em producao.
    """
    thresholds = np.asarray(cal_result["thresholds"], dtype=np.float64)
    cal_vals = np.asarray(cal_result["calibrated_probs"], dtype=np.float64)
    raw = np.asarray(raw_probs, dtype=np.float64)
    return np.interp(raw, thresholds, cal_vals).astype(np.float32)


def save_calibration_map(
    cal_result: dict,
    output_path: Path,
    mlflow_run_id: str = "",
    feature_spec_version: str = "1.0.0",
) -> dict:
    """
    Salva o mapa de calibracao como JSON versionado.

    O sidecar (services/ranker-sidecar) carrega este arquivo para aplicar
    a calibracao em tempo real sem re-compilar o ONNX.

    Schema do arquivo:
      {
        "calibration_method": "isotonic",
        "feature_spec_version": "1.0.0",
        "mlflow_run_id": "...",
        "calibrated_at": "ISO8601",
        "ece_before": float,
        "ece_after": float,
        "thresholds": [...],      # list<float32>
        "calibrated_probs": [...] # list<float32>
      }
    """
    output_path = Path(output_path)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    cal_map = {
        "calibration_method": "isotonic",
        "feature_spec_version": feature_spec_version,
        "mlflow_run_id": mlflow_run_id,
        "calibrated_at": datetime.now(timezone.utc).isoformat(),
        "ece_before": float(cal_result["ece_before"]),
        "ece_after": float(cal_result["ece_after"]),
        # Converte para float32 para consistencia com o vetor de serving
        "thresholds": [float(np.float32(x)) for x in cal_result["thresholds"]],
        "calibrated_probs": [float(np.float32(x)) for x in cal_result["calibrated_probs"]],
    }

    with open(output_path, "w") as f:
        json.dump(cal_map, f, indent=2)

    print(f"[calibration] Mapa salvo em {output_path} "
          f"({len(cal_map['thresholds'])} pontos)")
    return cal_map


def run_calibration(
    booster,
    X_cal: np.ndarray,
    y_cal: np.ndarray,
    output_path: Optional[Path] = None,
    mlflow_run_id: str = "",
    feature_spec_version: str = "1.0.0",
) -> dict:
    """
    Pipeline completo de calibracao:
    1. Gera pCTR_raw do booster LightGBM
    2. Ajusta isotonica
    3. Salva mapa JSON
    4. Retorna dict com metricas e mapa

    Args:
        booster:  lgb.Booster treinado
        X_cal:    matriz de features do conjunto de calibracao (float32[N,23])
        y_cal:    labels binarios do conjunto de calibracao (int8/float32[N])
        output_path: onde salvar o calibration_map.json
        mlflow_run_id: run_id do MLflow para rastreabilidade
        feature_spec_version: versao da spec de features

    NOTA TX-2:
        pCTR e uma probabilidade — float e correto aqui.
        Nenhum valor monetario (bid/eCPM) e computado neste modulo.
        A multiplicacao eCPM = pCTR_cal × bid ocorre no motor de decisao
        com bid em int64 minor-units. O guardiao money-ledger-guardian
        audita o ponto de multiplicacao em internal/ranker/score.go, nao aqui.
    """
    _ML_ROOT_local = Path(__file__).parent.parent
    if output_path is None:
        output_path = _ML_ROOT_local / "registry" / "artifacts" / "calibration_map.json"

    # Predicoes brutas do LightGBM
    raw_probs = booster.predict(X_cal).astype(np.float64)
    y_cal_arr = np.asarray(y_cal, dtype=np.float64)

    # Calibracao isotonica
    cal_result = fit_calibration(raw_probs, y_cal_arr)

    # Salva mapa
    cal_map = save_calibration_map(
        cal_result,
        output_path=output_path,
        mlflow_run_id=mlflow_run_id,
        feature_spec_version=feature_spec_version,
    )

    # ECE no conjunto de calibracao com o mapa salvo
    cal_probs_applied = apply_calibration(raw_probs, cal_result)
    ece_final = compute_ece(y_cal_arr, cal_probs_applied.astype(np.float64))
    assert abs(ece_final - cal_result["ece_after"]) < 1e-4, (
        f"ECE pos-apply ({ece_final:.4f}) diverge do ECE pos-fit ({cal_result['ece_after']:.4f})"
    )

    return {
        "calibration_map": cal_map,
        "ece_before": cal_result["ece_before"],
        "ece_after": cal_result["ece_after"],
        "ece_improvement": cal_result["ece_before"] - cal_result["ece_after"],
        "n_calibration_points": len(cal_result["thresholds"]),
        "output_path": str(output_path),
        "reliability_before": cal_result["reliability_before"],
        "reliability_after": cal_result["reliability_after"],
    }
