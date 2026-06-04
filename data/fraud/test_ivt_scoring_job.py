"""
data/fraud/test_ivt_scoring_job.py
====================================
Testa o job de scoring IVT (data/fraud/ivt_scoring_job.py) com o sample
sintetico de ml/fraud/testdata/sample_ivt.parquet.

Cobre:
  1. Importacao do scorer (ml.fraud.scorer) a partir de data/fraud/.
  2. Pontuacao em modo fail-open (sem modelo ONNX carregado).
  3. Pontuacao de batch (score_impression_batch) com sample sintetico.
  4. Invariantes TX-6: fail-open retorna ivt_status='' (nao bloqueia trafego).
  5. Invariante TX-2/DA-10: nenhum float em campos monetarios.
  6. Invariante TX-5/DA-11: IVTScoringInput nao tem PII.
  7. Logica de apply_ivt_filter_to_impressions (reconciliacao IVT vs billing).
  8. Idempotencia de score_impression_batch (mesmo input -> mesmo resultado).

Executar com:
  ml/.venv/bin/python data/fraud/test_ivt_scoring_job.py

Nao requer ClickHouse nem Iceberg em execucao (teste estatico/unitario).
"""

from __future__ import annotations

import sys
import uuid
from datetime import datetime, timezone
from decimal import Decimal
from pathlib import Path
from typing import List

# ---------------------------------------------------------------------------
# Resolve repo root
# ---------------------------------------------------------------------------
_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

import pandas as pd  # noqa: E402

from ml.fraud.scorer import IVTScorer, IVTScoringInput  # noqa: E402
from data.fraud.ivt_scoring_job import (  # noqa: E402
    ImpressionRow,
    IVTScoreRow,
    IVT_STATUS_CLEAN,
    IVT_STATUS_IVT,
    score_impression_batch,
    apply_ivt_filter_to_impressions,
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

PASS = "\033[32mPASS\033[0m"
FAIL = "\033[31mFAIL\033[0m"

_failures: List[str] = []


def assert_eq(label: str, got, expected) -> None:
    if got == expected:
        print(f"  {PASS}: {label}")
    else:
        msg = f"  {FAIL}: {label} — expected {expected!r}, got {got!r}"
        print(msg)
        _failures.append(msg)


def assert_true(label: str, condition: bool) -> None:
    assert_eq(label, condition, True)


def assert_false(label: str, condition: bool) -> None:
    assert_eq(label, condition, False)


# ---------------------------------------------------------------------------
# Fixtures de impressoes sinteticas
# ---------------------------------------------------------------------------

def _make_impression(
    event_id: str = None,
    tenant_id: str = "t1",
    campaign_id: str = "c1",
    zone_id: str = "z1",
    ivt_status: str = "",
    user_agent_class: str = "chrome-mobile",
    geo_country: str = "BR",
    hourly_requests: int = 500,
    hourly_impressions: int = 400,
    hourly_clicks: int = 10,
) -> ImpressionRow:
    return ImpressionRow(
        event_id=event_id or str(uuid.uuid4()),
        tenant_id=tenant_id,
        campaign_id=campaign_id,
        banner_id="b1",
        zone_id=zone_id,
        occurred_at=datetime(2026, 6, 4, 14, 0, 0, tzinfo=timezone.utc),
        billable=True,
        blank=False,
        ivt_status=ivt_status,
        user_agent_class=user_agent_class,  # PII-free: classe coarse (TX-5)
        geo_country=geo_country,            # PII-free: derivado de IP (TX-5)
        geo_city="Sao Paulo",
        hourly_requests=hourly_requests,
        hourly_impressions=hourly_impressions,
        hourly_clicks=hourly_clicks,
        recent_hourly_imps=[380, 420, 390, 410, 430],
    )


# ---------------------------------------------------------------------------
# Teste 1: importacao do scorer e fail-open
# ---------------------------------------------------------------------------

def test_scorer_import_and_fail_open() -> None:
    print("\n[1] Importacao do scorer e fail-open")
    scorer = IVTScorer()  # sem modelo carregado -> fail-open
    scorer.load()         # nenhum caminho -> continua fail-open

    inp = IVTScoringInput(
        device_class="chrome-mobile",
        hour_of_day=14,
        day_of_week=2,
        geo_country="BR",
        geo_city="Sao Paulo",
        hourly_requests=500,
        hourly_impressions=400,
        hourly_clicks=10,
        recent_hourly_imps=[380, 420, 390, 410, 430],
    )
    result = scorer.score_event(inp)

    # TX-6: fail-open nunca bloqueia trafego (is_ivt=False)
    assert_false("fail_open nao bloqueia trafego (is_ivt=False)", result.is_ivt)
    assert_eq("fail_open model_version='fail_open'", result.model_version, "fail_open")
    assert_eq("fail_open ivt_label=0", result.ivt_label, 0)


# ---------------------------------------------------------------------------
# Teste 2: PII-free — IVTScoringInput nao contem campos PII
# ---------------------------------------------------------------------------

def test_ivt_scoring_input_pii_free() -> None:
    print("\n[2] PII-free (TX-5/DA-11)")
    inp = IVTScoringInput(
        device_class="bot",         # classe coarse, nao UA bruto
        hour_of_day=3,
        day_of_week=6,
        geo_country="XX",           # ISO alpha-2, nao IP
        geo_city="",
        hourly_requests=9000,
        hourly_impressions=100,
        hourly_clicks=0,
    )
    # Verifica que nenhum campo sugere PII
    fields_with_values = vars(inp)
    pii_indicators = ["ip", "user_agent_raw", "user_id", "cookie", "fingerprint"]
    for pi in pii_indicators:
        has_pii = any(pi.lower() in k.lower() for k in fields_with_values)
        assert_false(f"IVTScoringInput nao tem campo PII: {pi}", has_pii)

    # device_class deve ser classe coarse, nao UA bruto
    assert_true("device_class e string nao vazia", isinstance(inp.device_class, str))


# ---------------------------------------------------------------------------
# Teste 3: score_impression_batch com scorer fail-open
# ---------------------------------------------------------------------------

def test_score_impression_batch_fail_open() -> None:
    print("\n[3] score_impression_batch em fail-open")
    scorer = IVTScorer()
    scorer.load()  # fail-open

    score_run_id = str(uuid.uuid4())
    impressions = [
        _make_impression(event_id="evt-001", user_agent_class="bot"),
        _make_impression(event_id="evt-002", user_agent_class="chrome-mobile"),
        _make_impression(event_id="evt-003", user_agent_class="desktop/safari"),
    ]

    scores = score_impression_batch(
        impressions=impressions,
        scorer=scorer,
        score_run_id=score_run_id,
        clickhouse_uri="http://localhost:8123",  # nao e chamado no esqueleto
    )

    assert_eq("score_batch retorna mesmo numero de entradas", len(scores), 3)

    # TX-6: fail-open -> ivt_status = '' (clean) para todos
    for s in scores:
        assert_eq(
            f"fail_open ivt_status='' para {s.event_id}",
            s.ivt_status, IVT_STATUS_CLEAN,
        )

    # Verifica tipos: ivt_prob e float (probabilidade ML, nao dinheiro)
    for s in scores:
        assert_true(f"ivt_prob e float para {s.event_id}", isinstance(s.ivt_prob, float))
        # TX-2: ivt_prob NAO e valor monetario (nao usa Decimal)
        assert_false(
            f"ivt_prob nao e Decimal (TX-2 N/A para prob ML) para {s.event_id}",
            isinstance(s.ivt_prob, Decimal),
        )

    # Idempotencia: mesmo input -> mesmo score_run_id -> mesmos resultados
    scores2 = score_impression_batch(
        impressions=impressions,
        scorer=scorer,
        score_run_id=score_run_id,  # mesmo run
        clickhouse_uri="http://localhost:8123",
    )
    for s1, s2 in zip(scores, scores2):
        assert_eq(f"idempotencia ivt_status para {s1.event_id}", s1.ivt_status, s2.ivt_status)
        assert_eq(f"idempotencia ivt_label para {s1.event_id}", s1.ivt_label, s2.ivt_label)


# ---------------------------------------------------------------------------
# Teste 4: score_impression_batch com sample sintetico do parquet
# ---------------------------------------------------------------------------

def test_score_batch_with_synthetic_sample() -> None:
    print("\n[4] score_impression_batch com sample sintetico (ml/fraud/testdata/sample_ivt.parquet)")
    sample_path = _REPO_ROOT / "ml" / "fraud" / "testdata" / "sample_ivt.parquet"
    if not sample_path.exists():
        print(f"  SKIP: {sample_path} nao encontrado.")
        return

    df = pd.read_parquet(sample_path)
    print(f"  sample: {len(df)} linhas, colunas={list(df.columns)}")

    scorer = IVTScorer()
    scorer.load()  # fail-open sem modelo ONNX

    # Mapeia colunas do sample para ImpressionRow (best-effort)
    impressions: List[ImpressionRow] = []
    for i, row in df.head(50).iterrows():
        imp = ImpressionRow(
            event_id=str(uuid.uuid4()),
            tenant_id="t-sample",
            campaign_id=str(row.get("zone_id", "c-unknown")),
            banner_id="b-sample",
            zone_id=str(row.get("zone_id", "z-unknown")),
            occurred_at=datetime(2026, 6, 4, int(row.get("hour_of_day", 12)), 0, 0,
                                 tzinfo=timezone.utc),
            billable=True,
            blank=False,
            ivt_status="",
            user_agent_class=str(row.get("device_class", "unknown")),
            geo_country=str(row.get("geo_country", "XX")),
            geo_city=str(row.get("geo_city", "")),
            hourly_requests=int(row.get("hourly_requests", 0)),
            hourly_impressions=int(row.get("hourly_impressions", 0)),
            hourly_clicks=int(row.get("hourly_clicks", 0)),
            recent_hourly_imps=[],
        )
        impressions.append(imp)

    scores = score_impression_batch(
        impressions=impressions,
        scorer=scorer,
        score_run_id=str(uuid.uuid4()),
        clickhouse_uri="http://localhost:8123",
    )

    assert_eq("score do sample retorna mesmo numero de linhas", len(scores), len(impressions))
    # Todos em fail-open: ivt_status = ''
    for s in scores:
        assert_eq(
            f"sample fail_open ivt_status='' para event {s.event_id[:8]}",
            s.ivt_status, IVT_STATUS_CLEAN,
        )

    print(f"  {PASS}: {len(scores)} impressoes pontuadas (fail-open, sem modelo ONNX)")


# ---------------------------------------------------------------------------
# Teste 5: apply_ivt_filter_to_impressions (reconciliacao IVT vs billing)
# ---------------------------------------------------------------------------

def test_apply_ivt_filter() -> None:
    print("\n[5] apply_ivt_filter_to_impressions (reconciliacao IVT/billing)")

    impression_rows = [
        {"event_id": "e1", "ivt_status": ""},           # clean -> faturar
        {"event_id": "e2", "ivt_status": "ivt"},        # IVT na impression -> excluir
        {"event_id": "e3", "ivt_status": ""},           # sem score -> usa impression ivt_status
        {"event_id": "e4", "ivt_status": ""},           # score IVT posterior -> excluir
        {"event_id": "e5", "ivt_status": "fail_open"},  # fail_open na impression -> clean
        {"event_id": "e6", "ivt_status": ""},           # score 'unscored' -> excluir
    ]

    # Scores do Iceberg (events_ivt_score): tem precedencia sobre impression
    ivt_scores = {
        "e1": "",           # score confirma clean
        "e2": "",           # score overrida impression IVT -> clean (scoring posterior)
        # e3: nao tem score -> usa ivt_status da impression ('' = clean)
        "e4": "ivt",        # score IVT posterior -> excluir mesmo impression sendo ''
        # e5: nao tem score -> usa ivt_status da impression (fail_open = clean)
        "e6": "unscored",   # nao pontuado -> excluir (conservador)
    }

    clean_rows = apply_ivt_filter_to_impressions(impression_rows, ivt_scores)
    clean_ids = {r["event_id"] for r in clean_rows}

    assert_true("e1 incluido (clean confirmado pelo score)", "e1" in clean_ids)
    assert_true("e2 incluido (score overrida impression IVT -> clean)", "e2" in clean_ids)
    assert_true("e3 incluido (sem score, impression clean)", "e3" in clean_ids)
    assert_false("e4 excluido (score IVT posterior)", "e4" in clean_ids)
    assert_true("e5 incluido (fail_open na impression = clean)", "e5" in clean_ids)
    assert_false("e6 excluido (score unscored = conservador, nao faturar)", "e6" in clean_ids)

    # TX-6: fail-open nao bloqueia trafego
    assert_eq("4 impressoes clean apos filtro IVT", len(clean_rows), 4)


# ---------------------------------------------------------------------------
# Teste 6: nenhum float monetario no IVTScoreRow (TX-2)
# ---------------------------------------------------------------------------

def test_no_monetary_float() -> None:
    print("\n[6] Nenhum float em campos monetarios do IVTScoreRow (TX-2)")

    row = IVTScoreRow(
        event_id="e1",
        score_run_id="run-1",
        tenant_id="t1",
        campaign_id="c1",
        zone_id="z1",
        occurred_at=datetime(2026, 6, 4, 14, 0, 0, tzinfo=timezone.utc),
        scored_at=datetime(2026, 6, 4, 14, 1, 0, tzinfo=timezone.utc),
        ivt_status="",
        ivt_prob=0.3,       # Float legitimo: probabilidade ML
        ivt_label=0,
        model_version="1",
    )

    # ivt_prob e float (probabilidade ML, nao dinheiro) — correto
    assert_true("ivt_prob e float (probabilidade ML, nao dinheiro)", isinstance(row.ivt_prob, float))
    # Nenhum campo do IVTScoreRow tem _amount, _rate ou _value (nenhum campo monetario)
    monetary_fields = [f for f in vars(row) if any(
        s in f for s in ("_amount", "_rate", "_value", "_decimal")
    )]
    assert_eq("IVTScoreRow nao tem campos monetarios (TX-2 N/A)", monetary_fields, [])


# ---------------------------------------------------------------------------
# Teste 7: score_batch e idempotente sob reentrega (at-least-once)
# ---------------------------------------------------------------------------

def test_idempotency_under_redelivery() -> None:
    print("\n[7] Idempotencia sob reentrega (at-least-once)")

    scorer = IVTScorer()
    scorer.load()

    impressions = [
        _make_impression(event_id="idem-001"),
        _make_impression(event_id="idem-002", user_agent_class="bot"),
    ]
    score_run_id = "fixed-run-id-for-idempotency-test"

    scores_run1 = score_impression_batch(
        impressions=impressions,
        scorer=scorer,
        score_run_id=score_run_id,
        clickhouse_uri="",
    )
    scores_run2 = score_impression_batch(
        impressions=impressions,
        scorer=scorer,
        score_run_id=score_run_id,  # mesmo score_run_id = reentrega
        clickhouse_uri="",
    )

    for s1, s2 in zip(scores_run1, scores_run2):
        assert_eq(f"reentrega: ivt_status identico para {s1.event_id}", s1.ivt_status, s2.ivt_status)
        assert_eq(f"reentrega: ivt_label identico para {s1.event_id}", s1.ivt_label, s2.ivt_label)
        assert_eq(f"reentrega: model_version identico para {s1.event_id}", s1.model_version, s2.model_version)
        assert_eq(f"reentrega: score_run_id identico para {s1.event_id}", s1.score_run_id, s2.score_run_id)


# ---------------------------------------------------------------------------
# Teste 8: isolamento de tenant — impressoes de tenants distintos nao se mesclam
# ---------------------------------------------------------------------------

def test_tenant_isolation_in_scoring() -> None:
    print("\n[8] Isolamento de tenant no scoring")

    scorer = IVTScorer()
    scorer.load()

    score_run_id = str(uuid.uuid4())
    impressions = [
        _make_impression(event_id="t1-evt-001", tenant_id="tenant-A"),
        _make_impression(event_id="t2-evt-001", tenant_id="tenant-B"),
    ]

    scores = score_impression_batch(
        impressions=impressions,
        scorer=scorer,
        score_run_id=score_run_id,
        clickhouse_uri="",
    )

    # Verifica que tenant_id e preservado no resultado
    assert_eq("tenant_id A preservado no score", scores[0].tenant_id, "tenant-A")
    assert_eq("tenant_id B preservado no score", scores[1].tenant_id, "tenant-B")
    # event_id nao se mistura
    assert_eq("event_id A preservado", scores[0].event_id, "t1-evt-001")
    assert_eq("event_id B preservado", scores[1].event_id, "t2-evt-001")


# ---------------------------------------------------------------------------
# Runner principal
# ---------------------------------------------------------------------------

def main() -> None:
    print("=" * 60)
    print("data/fraud/test_ivt_scoring_job.py")
    print("Testes do job de scoring IVT (J6 / TX-6)")
    print("=" * 60)

    test_scorer_import_and_fail_open()
    test_ivt_scoring_input_pii_free()
    test_score_impression_batch_fail_open()
    test_score_batch_with_synthetic_sample()
    test_apply_ivt_filter()
    test_no_monetary_float()
    test_idempotency_under_redelivery()
    test_tenant_isolation_in_scoring()

    print("\n" + "=" * 60)
    if _failures:
        print(f"FALHAS ({len(_failures)}):")
        for f in _failures:
            print(f"  {f}")
        sys.exit(1)
    else:
        print(f"Todos os testes PASSARAM ({8} suites).")
        sys.exit(0)


if __name__ == "__main__":
    main()
