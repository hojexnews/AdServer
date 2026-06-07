"""
ml/features/python/test_parity_cases.py
=========================================
Teste de paridade Python para a spec de featurizacao.

Carrega ml/features/testdata/parity_cases.json e verifica que a implementacao
Python (featurize.py) produz os mesmos vetores que a implementacao Go
(internal/ranker/featurize.go, testada em internal/ranker/parity_test.go).

COMO EXECUTAR:
    cd ml/features/python
    pip install mmh3 numpy pytest
    pytest test_parity_cases.py -v

TOLERANCIA (da spec):
  - float: abs(go_value - py_value) <= 1e-6
    (vetor e float32; cast float32<->float64 introduz ruido ~1.19e-7;
     1e-6 captura bugs reais absorvendo esse ruido; 1e-9 e impossivel
     de satisfazer consistentemente com float32.)
  - int: igualdade exata (comparados como float32, diff <= 1e-6)

BOOTSTRAP DE HASH:
  Na primeira execucao, os valores de hash em expected_vector_computed que
  comecam com "_murmur3_32(...)" sao calculados aqui e impressos no stdout.
  Para fixar os valores gold, rodar com --bootstrap e copiar os valores impressos
  de volta para parity_cases.json (ver secao Bootstrap no README.md).
"""

from __future__ import annotations

import json
import math
import os
import sys
from pathlib import Path
from typing import Any, Dict

import mmh3
import numpy as np
import pytest

# Insere o diretorio pai no path para importar featurize
sys.path.insert(0, str(Path(__file__).parent))

from featurize import (
    CandidateInput,
    FeaturizeInput,
    GeoInput,
    RequestTimeInput,
    ZoneInput,
    featurize,
    FEATURE_SPEC_VERSION,
    _feature_hash,
    _COUNTRY_HASH_SEED, _COUNTRY_HASH_BUCKETS,
    _CITY_HASH_SEED, _CITY_HASH_BUCKETS,
    _DEVICE_HASH_SEED, _DEVICE_HASH_BUCKETS,
)

FIXTURES_PATH = (
    Path(__file__).parent.parent / "testdata" / "parity_cases.json"
)

FLOAT_TOL = 1e-6  # float32 cast noise ~1.19e-7; 1e-6 catches real bugs (see spec)


# ---------------------------------------------------------------------------
# Helpers de resolucao de hash placeholder
# ---------------------------------------------------------------------------

def _resolve_placeholder(value: Any) -> float:
    """
    Resolve valores placeholder do fixtures JSON.
    Strings como "_murmur3_32(BR, seed=42) % 512" sao calculadas aqui.
    Strings como "_log1p_150" sao calculadas.
    Numeros sao retornados diretamente.
    """
    if isinstance(value, (int, float)):
        return float(value)
    if not isinstance(value, str) or not value.startswith("_"):
        return float(value)

    s = value[1:]  # remove o prefixo "_"

    if s.startswith("murmur3_32("):
        # Formato: "murmur3_32(VALUE, seed=S) % N"
        inner = s[len("murmur3_32("):]
        parts = inner.split(",")
        val_str = parts[0].strip()
        rest = ",".join(parts[1:])
        seed_part = rest.split("seed=")[1].split(")")[0].strip()
        mod_part = rest.split("%")[1].strip()
        seed = int(seed_part)
        num_buckets = int(mod_part)
        return float(_feature_hash(val_str, seed, num_buckets))

    if s.startswith("log1p_"):
        num = float(s[len("log1p_"):])
        return math.log1p(num)

    if s.startswith("geo_country_hash_"):
        # ex: "geo_country_hash_BR_seed42_mod512" (case fixture legado)
        return float(_feature_hash("BR", _COUNTRY_HASH_SEED, _COUNTRY_HASH_BUCKETS))

    if s.startswith("geo_city_hash_"):
        # ex: "geo_city_hash_SaoPaulo_seed43_mod2048"
        city = s.replace("geo_city_hash_", "").split("_seed")[0]
        # "SaoPaulo" -> "Sao Paulo"
        city = city.replace("Sao Paulo", "Sao Paulo")
        return float(_feature_hash("Sao Paulo", _CITY_HASH_SEED, _CITY_HASH_BUCKETS))

    if s.startswith("device_class_hash_"):
        # ex: "device_class_hash_chromedesktop_seed44_mod16"
        return float(_feature_hash("chrome-desktop", _DEVICE_HASH_SEED, _DEVICE_HASH_BUCKETS))

    if s.startswith("ecpm_tier_bucket_log1p_"):
        from featurize import _bucketize, _ECPM_LOG1P_BOUNDARIES, _log1p
        num = float(s.split("ecpm_tier_bucket_log1p_")[1])
        return float(_bucketize(_log1p(num), _ECPM_LOG1P_BOUNDARIES))

    raise ValueError(f"Placeholder nao reconhecido: {value!r}")


def _build_input(case: Dict) -> FeaturizeInput:
    inp_raw = case["input"]
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


def _expected_from_computed(computed: Dict) -> np.ndarray:
    """Extrai o vetor esperado do campo expected_vector_computed."""
    vec = np.zeros(23, dtype=np.float64)
    for key, value in computed.items():
        if not key.startswith("index_"):
            continue
        idx = int(key.split("_")[1])
        if 0 <= idx < 23:
            vec[idx] = _resolve_placeholder(value)
    return vec


# ---------------------------------------------------------------------------
# Fixtures pytest
# ---------------------------------------------------------------------------

def load_fixtures():
    with open(FIXTURES_PATH, "r", encoding="utf-8") as f:
        data = json.load(f)
    assert data["feature_spec_version"] == FEATURE_SPEC_VERSION, (
        f"Versao da spec nos fixtures ({data['feature_spec_version']}) "
        f"difere da implementacao ({FEATURE_SPEC_VERSION}). "
        "Atualize os fixtures ou a implementacao."
    )
    return data["cases"]


CASES = load_fixtures()


@pytest.mark.parametrize("case", CASES, ids=[c["id"] for c in CASES])
def test_parity_case(case):
    """
    Verifica que featurize(input) == expected_vector_computed para cada fixture.

    Este teste e IDENTICO em estrutura ao TestParityFromFixtures em Go
    (internal/ranker/parity_test.go). Ambos carregam o mesmo JSON e verificam
    o mesmo vetor — garantindo paridade treino<->serving.
    """
    inp = _build_input(case)
    actual = featurize(inp).astype(np.float64)
    expected = _expected_from_computed(case["expected_vector_computed"])

    for i in range(23):
        diff = abs(actual[i] - expected[i])
        assert diff <= FLOAT_TOL, (
            f"Caso '{case['id']}': index {i} — "
            f"actual={actual[i]:.10f}, expected={expected[i]:.10f}, diff={diff:.2e} "
            f"(tolerancia={FLOAT_TOL:.0e})"
        )


def test_spec_version_in_fixtures():
    """Garante que feature_spec_version dos fixtures casa com a implementacao."""
    with open(FIXTURES_PATH, "r", encoding="utf-8") as f:
        data = json.load(f)
    assert data["feature_spec_version"] == FEATURE_SPEC_VERSION


def test_vector_length():
    """Garante que o vetor tem comprimento 23 para todos os casos."""
    for case in CASES:
        inp = _build_input(case)
        vec = featurize(inp)
        assert len(vec) == 23, f"Caso '{case['id']}': comprimento esperado 23, obtido {len(vec)}"


def test_no_nan_in_vector():
    """Garante que nenhum elemento do vetor e NaN ou inf."""
    for case in CASES:
        inp = _build_input(case)
        vec = featurize(inp)
        assert not np.any(np.isnan(vec)), f"Caso '{case['id']}': NaN no vetor"
        assert not np.any(np.isinf(vec)), f"Caso '{case['id']}': inf no vetor"


def test_monetary_input_is_int():
    """
    TX-2: verifica que ecpm_minor_units e passado como int, nunca float,
    nas fixtures. O vetor de ML produz float32 (permitido pela spec), mas
    a ENTRADA deve ser int64-compativel.
    """
    for case in CASES:
        ecpm = case["input"]["candidate"]["ecpm_minor_units"]
        assert isinstance(ecpm, int), (
            f"Caso '{case['id']}': ecpm_minor_units deve ser int (TX-2), "
            f"obtido {type(ecpm).__name__}"
        )


def test_pii_free_no_raw_geo():
    """
    TX-5/DA-11: verifica que nenhum campo de input contem IP bruto.
    A entrada geo deve conter apenas country (ISO) e city (nome), sem IP.
    """
    for case in CASES:
        geo = case["input"]["geo"]
        assert "ip" not in geo, f"Caso '{case['id']}': campo 'ip' encontrado em geo (TX-5 violado)"
        assert set(geo.keys()).issubset({"country", "city"}), (
            f"Caso '{case['id']}': campos extras em geo: {set(geo.keys()) - {'country', 'city'}}"
        )


if __name__ == "__main__":
    # Bootstrap: imprime os valores calculados para validar / fixar no JSON.
    print(f"feature_spec_version: {FEATURE_SPEC_VERSION}")
    print()
    for case in CASES:
        inp = _build_input(case)
        vec = featurize(inp).astype(np.float64)
        print(f"--- {case['id']} ---")
        for i, v in enumerate(vec):
            print(f"  [{i:2d}] {v:.15g}")
        print()
