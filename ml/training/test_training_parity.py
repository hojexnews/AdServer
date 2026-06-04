"""
ml/training/test_training_parity.py
=====================================
Teste de paridade entre o pipeline de treino e os fixtures gold.

Prova que:
  1. O vetor produzido pelo pipeline de treino (df_to_feature_matrix_fast)
     e IDENTICO ao vetor gold dos parity_cases.json (tolerancia 1e-6).
  2. ecpm_minor_units no DataFrame de treino e sempre int64 (TX-2).
  3. Nenhum vetor do sample contem NaN ou inf.
  4. feature_spec_version do pipeline == versao dos fixtures.

Estes testes SAO GATE DE MERGE do J2: o pipeline de treino NAO pode
divergir dos fixtures que o sidecar (J1) tambem valida.

COMO EXECUTAR:
  cd /home/agencia/Hojex News/AdServer
  ./ml/.venv/bin/python -m pytest ml/training/test_training_parity.py -v
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

import numpy as np
import pytest

_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.features.python.featurize import (
    FEATURE_SPEC_VERSION,
    CandidateInput,
    FeaturizeInput,
    GeoInput,
    RequestTimeInput,
    ZoneInput,
    featurize,
)
from ml.training.train_pctr import df_to_feature_matrix_fast

FIXTURES_PATH = _REPO_ROOT / "ml" / "features" / "testdata" / "parity_cases.json"
FLOAT_TOL = 1e-6


def _load_fixtures():
    with open(FIXTURES_PATH) as f:
        data = json.load(f)
    return data


def _fixture_input_to_featurizeinput(inp_raw: dict) -> FeaturizeInput:
    return FeaturizeInput(
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


def _expected_vector(computed: dict) -> np.ndarray:
    """Extrai vetor esperado do campo expected_vector_computed dos fixtures."""
    vec = np.zeros(23, dtype=np.float64)
    for key, value in computed.items():
        if not key.startswith("index_"):
            continue
        idx = int(key.split("_")[1])
        if 0 <= idx < 23:
            vec[idx] = float(value)
    return vec


def _fixture_to_dataframe_row(inp_raw: dict) -> dict:
    """
    Converte um input de fixture para o formato de linha do DataFrame de treino.
    Permite usar df_to_feature_matrix_fast nos casos de fixture.
    """
    return {
        "zone_width": inp_raw["zone"]["width"],
        "zone_height": inp_raw["zone"]["height"],
        "hour_of_day": inp_raw["request_time_utc"]["hour"],
        "day_of_week": inp_raw["request_time_utc"]["day_of_week"],
        "geo_country": inp_raw["geo"]["country"],
        "geo_city": inp_raw["geo"]["city"],
        "device_class": inp_raw["device_class"],
        "candidate_tier": inp_raw["candidate"]["tier"],
        "campaign_priority": inp_raw["candidate"]["priority"],
        "pacing_deficit": inp_raw["candidate"]["pacing_deficit"],
        "ecpm_minor_units": inp_raw["candidate"]["ecpm_minor_units"],  # int64, TX-2
        "banner_width": inp_raw["candidate"]["banner_width"],
        "banner_height": inp_raw["candidate"]["banner_height"],
        "creative_type": inp_raw["candidate"]["creative_type"],
        "candidate_count": inp_raw["candidate_count"],
        "delivered_impressions": inp_raw.get("campaign_delivered_impressions", 0),
        "goal_impressions": inp_raw.get("campaign_goal_impressions", 0),
        "delivered_clicks": inp_raw.get("campaign_delivered_clicks", 0),
        # campos dummy necessarios para o DataFrame (nao usados na featurizacao)
        "label_click": 0,
        "propensity": 1.0,
        "model_version": "DETERMINISTIC",
        "feature_spec_version": "1.0.0",
        "decision_id": "fixture-test",
    }


# ---------------------------------------------------------------------------
# TESTE 1: spec_version dos fixtures == spec_version da implementacao
# ---------------------------------------------------------------------------

def test_spec_version_match():
    """feature_spec_version dos fixtures deve casar com a implementacao."""
    data = _load_fixtures()
    assert data["feature_spec_version"] == FEATURE_SPEC_VERSION, (
        f"Fixtures: {data['feature_spec_version']}, Impl: {FEATURE_SPEC_VERSION}"
    )


# ---------------------------------------------------------------------------
# TESTE 2: vetor featurize() == vetor gold (via funcao direta)
# ---------------------------------------------------------------------------

@pytest.fixture
def fixtures():
    return _load_fixtures()


@pytest.mark.parametrize(
    "case_id",
    [
        "remnant_brl_desktop_br_weekday",
        "contract_deficit_mobile_us_weekend",
        "override_high_priority_video_missing_geo",
        "remnant_bot_traffic",
        "contract_full_delivery_no_deficit",
    ],
)
def test_featurize_equals_gold(case_id, fixtures):
    """
    Vetor featurize() == vetor gold dos fixtures.
    Replica test_parity_cases.py para provar que o modulo de treino
    usa a mesma implementacao.
    """
    case = next(c for c in fixtures["cases"] if c["id"] == case_id)
    inp = _fixture_input_to_featurizeinput(case["input"])
    actual = featurize(inp).astype(np.float64)
    expected = _expected_vector(case["expected_vector_computed"])

    for i in range(23):
        diff = abs(actual[i] - expected[i])
        assert diff <= FLOAT_TOL, (
            f"Caso '{case_id}' index {i}: "
            f"actual={actual[i]:.10f}, expected={expected[i]:.10f}, diff={diff:.2e}"
        )


# ---------------------------------------------------------------------------
# TESTE 3: df_to_feature_matrix_fast() == featurize() para casos de fixture
#          (prova que o pipeline de treino usa a funcao unica)
# ---------------------------------------------------------------------------

@pytest.mark.parametrize(
    "case_id",
    [
        "remnant_brl_desktop_br_weekday",
        "contract_deficit_mobile_us_weekend",
        "override_high_priority_video_missing_geo",
        "remnant_bot_traffic",
        "contract_full_delivery_no_deficit",
    ],
)
def test_training_pipeline_vector_equals_gold(case_id, fixtures):
    """
    TESTE PRINCIPAL de J2:
    O vetor produzido pelo pipeline de treino (df_to_feature_matrix_fast)
    deve ser IDENTICO ao vetor gold dos fixtures.

    Prova que:
    - O treino usa exatamente a mesma featurizacao que o sidecar (J1) valida.
    - Nao ha skew treino<->serving introduzido pelo pipeline.
    """
    import pandas as pd

    case = next(c for c in fixtures["cases"] if c["id"] == case_id)
    expected = _expected_vector(case["expected_vector_computed"])

    # Constroi DataFrame com a linha do fixture
    row = _fixture_to_dataframe_row(case["input"])
    df = pd.DataFrame([row])
    # TX-2: garante int64
    df["ecpm_minor_units"] = df["ecpm_minor_units"].astype("int64")

    # Aplica o pipeline de treino
    X = df_to_feature_matrix_fast(df)
    assert X.shape == (1, 23), f"Shape esperado (1,23), obtido {X.shape}"
    actual = X[0].astype(np.float64)

    for i in range(23):
        diff = abs(actual[i] - expected[i])
        assert diff <= FLOAT_TOL, (
            f"TRAINING PARITY FAIL — caso '{case_id}' index {i}: "
            f"pipeline={actual[i]:.10f}, gold={expected[i]:.10f}, diff={diff:.2e} "
            f"(tol={FLOAT_TOL:.0e})"
        )


# ---------------------------------------------------------------------------
# TESTE 4: TX-2 — ecpm_minor_units e int nos fixtures e no DataFrame
# ---------------------------------------------------------------------------

def test_ecpm_minor_units_is_int_in_fixtures(fixtures):
    """TX-2: ecpm_minor_units deve ser int nos fixtures (nunca float)."""
    for case in fixtures["cases"]:
        ecpm = case["input"]["candidate"]["ecpm_minor_units"]
        assert isinstance(ecpm, int), (
            f"Caso '{case['id']}': ecpm_minor_units deve ser int (TX-2), "
            f"obtido {type(ecpm).__name__}"
        )


def test_ecpm_minor_units_int64_in_dataframe():
    """TX-2: ecpm_minor_units deve ser int64 no DataFrame de treino."""
    import pandas as pd
    from ml.training.generate_sample import generate

    df = generate(n_rows=100, seed=99)
    assert df["ecpm_minor_units"].dtype == "int64", (
        f"ecpm_minor_units dtype={df['ecpm_minor_units'].dtype}, esperado int64 (TX-2)"
    )
    # Garante que nenhum valor e float (NaN seria float64)
    assert not df["ecpm_minor_units"].isna().any(), "ecpm_minor_units contem NaN (TX-2 violado)"


# ---------------------------------------------------------------------------
# TESTE 5: sem NaN / inf no vetor de treino
# ---------------------------------------------------------------------------

def test_no_nan_inf_in_training_vectors():
    """Nenhum elemento do vetor de features pode ser NaN ou inf."""
    import pandas as pd
    from ml.training.generate_sample import generate

    df = generate(n_rows=500, seed=7)
    X = df_to_feature_matrix_fast(df)
    assert not np.any(np.isnan(X)), "Vetor de treino contem NaN"
    assert not np.any(np.isinf(X)), "Vetor de treino contem inf"


# ---------------------------------------------------------------------------
# TESTE 6: TX-5/DA-11 — sem PII no DataFrame de treino
# ---------------------------------------------------------------------------

def test_no_pii_in_training_dataframe():
    """TX-5/DA-11: sem campos de IP bruto ou identificador cross-site no DataFrame."""
    from ml.training.generate_sample import generate

    df = generate(n_rows=100, seed=13)
    # Campos proibidos
    forbidden = {"ip", "user_id", "cookie_id", "fingerprint_id", "cross_site_id"}
    cols = set(df.columns.str.lower())
    overlap = forbidden & cols
    assert not overlap, f"Campos PII encontrados no DataFrame: {overlap} (TX-5 violado)"
    # Geo deve ser apenas country e city (sem IP)
    assert "geo_country" in cols, "geo_country ausente"
    assert "geo_city" in cols, "geo_city ausente"


if __name__ == "__main__":
    import pytest as _pytest
    _pytest.main([__file__, "-v"])
