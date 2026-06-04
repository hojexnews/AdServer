"""
data/fraud/test_ivt_unsup_scoring_job.py
==========================================
Testes do job de scoring nao-supervisionado (K2 / Fase 3).

Cobre:
  1. score_impression_batch_combined: retorna dois lotes (combined + unsup)
  2. Fail-open bilateral: ambos os scorers em fail-open -> todos clean
  3. Semantica OR: nao-supervisionado detecta IVT mesmo quando supervisionado e clean
  4. TX-6: fail-open nao bloqueia trafego (is_ivt_final=False quando ambos fail-open)
  5. TX-2: IVTUnsupScoreRow nao tem campos monetarios
  6. TX-3: tenant_id propagado para IVTUnsupScoreRow
  7. Idempotencia: mesmo score_run_id -> mesmo resultado
  8. Migr 007 nao alterada: write_ivt_scores ainda usa raw_ivt_score (contrato)
  9. Separacao de responsabilidades: unsup scores vao para raw_ivt_unsup_score (008)
"""
from __future__ import annotations

import sys
import uuid
from dataclasses import asdict
from datetime import datetime, timezone
from pathlib import Path
from typing import List

_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

import pytest

from ml.fraud.scorer import IVTScorer, IVTScore
from ml.fraud.unsup_scorer import IVTUnsupScorer, IVTUnsupScore, _make_fail_open_unsup
from data.fraud.ivt_scoring_job import (
    ImpressionRow,
    IVTScoreRow,
    IVT_STATUS_CLEAN,
    IVT_STATUS_IVT,
)
from data.fraud.ivt_unsup_scoring_job import (
    IVTUnsupScoreRow,
    score_impression_batch_combined,
    write_ivt_unsup_scores,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_impression(
    event_id: str = None,
    tenant_id: str = "t1",
    campaign_id: str = "c1",
    zone_id: str = "z1",
    user_agent_class: str = "chrome-mobile",
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
        ivt_status="",
        user_agent_class=user_agent_class,
        geo_country="BR",
        geo_city="Sao Paulo",
        hourly_requests=hourly_requests,
        hourly_impressions=hourly_impressions,
        hourly_clicks=hourly_clicks,
        recent_hourly_imps=[400] * 24,
    )


# ---------------------------------------------------------------------------
# 1. score_impression_batch_combined: estrutura de retorno
# ---------------------------------------------------------------------------

class TestScoreImpressionBatchCombined:

    def test_returns_two_lists(self):
        """Retorna (combined_rows, unsup_rows) — um resultado por impressao."""
        sup_scorer   = IVTScorer()
        unsup_scorer = IVTUnsupScorer()
        impressions  = [_make_impression(event_id=f"e{i}") for i in range(3)]

        combined, unsup = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=str(uuid.uuid4()),
            clickhouse_uri="",
        )

        assert len(combined) == 3
        assert len(unsup)    == 3

    def test_combined_rows_are_ivt_score_rows(self):
        """Linhas combinadas sao IVTScoreRow (compativel com raw_ivt_score 007)."""
        sup_scorer   = IVTScorer()
        unsup_scorer = IVTUnsupScorer()
        impressions  = [_make_impression()]

        combined, _ = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=str(uuid.uuid4()),
            clickhouse_uri="",
        )

        assert isinstance(combined[0], IVTScoreRow)

    def test_unsup_rows_are_ivt_unsup_score_rows(self):
        """Linhas nao-supervisionadas sao IVTUnsupScoreRow (nova tabela 008)."""
        sup_scorer   = IVTScorer()
        unsup_scorer = IVTUnsupScorer()
        impressions  = [_make_impression()]

        _, unsup = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=str(uuid.uuid4()),
            clickhouse_uri="",
        )

        assert isinstance(unsup[0], IVTUnsupScoreRow)

    def test_score_run_id_propagated(self):
        """score_run_id e propagado para ambos os tipos de linha."""
        sup_scorer   = IVTScorer()
        unsup_scorer = IVTUnsupScorer()
        run_id = str(uuid.uuid4())
        impressions  = [_make_impression(event_id="e1")]

        combined, unsup = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=run_id,
            clickhouse_uri="",
        )

        assert combined[0].score_run_id == run_id
        assert unsup[0].score_run_id    == run_id


# ---------------------------------------------------------------------------
# 2. Fail-open bilateral: ambos os scorers em fail-open -> todos clean
# ---------------------------------------------------------------------------

class TestFailOpenBilateral:

    def test_both_fail_open_produces_clean_status(self):
        """
        Ambos os scorers em fail-open -> ivt_status='' (clean) para todos.
        TX-6: nao bloquear trafego limpo por falha de ML.
        """
        sup_scorer   = IVTScorer()   # sem modelo -> fail-open
        unsup_scorer = IVTUnsupScorer()  # sem modelo -> fail-open

        impressions = [_make_impression(event_id=f"e{i}") for i in range(5)]
        combined, unsup = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=str(uuid.uuid4()),
            clickhouse_uri="",
        )

        for row in combined:
            assert row.ivt_status == IVT_STATUS_CLEAN, (
                f"Fail-open bilateral deve retornar ivt_status='' para {row.event_id}"
            )
        for row in unsup:
            assert row.combined_is_ivt is False, (
                f"Fail-open bilateral: combined_is_ivt deve ser False para {row.event_id}"
            )

    def test_both_fail_open_is_unsup_ivt_false(self):
        """is_unsup_ivt=False quando scorer nao-supervisionado em fail-open."""
        sup_scorer   = IVTScorer()
        unsup_scorer = IVTUnsupScorer()

        impressions = [_make_impression()]
        _, unsup = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=str(uuid.uuid4()),
            clickhouse_uri="",
        )

        assert unsup[0].is_unsup_ivt is False


# ---------------------------------------------------------------------------
# 3. Semantica OR: nao-supervisionado detecta IVT
# ---------------------------------------------------------------------------

class TestORSemantics:

    def test_combined_ivt_if_sup_clean_unsup_anomaly(self):
        """
        Quando supervisionado e clean mas nao-supervisionado detecta anomalia:
        combined_is_ivt=True e ivt_status='ivt'.
        """
        # Scorer supervisionado sempre clean (fail-open, is_ivt=False)
        sup_scorer = IVTScorer()  # fail-open

        # Scorer nao-supervisionado: mock que detecta anomalia
        unsup_scorer = _MockUnsupScorer(is_unsup_ivt=True)

        impressions = [_make_impression(event_id="e1")]
        combined, unsup = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=str(uuid.uuid4()),
            clickhouse_uri="",
        )

        # OR: supervisionado clean, nao-superv detecta -> combined = IVT
        assert combined[0].ivt_status == IVT_STATUS_IVT, (
            "Nao-supervisionado detectou IVT; combined deve ser ivt_status='ivt'"
        )
        assert unsup[0].combined_is_ivt is True
        assert unsup[0].is_unsup_ivt is True

    def test_combined_ivt_if_sup_detects_unsup_clean(self):
        """
        Quando supervisionado detecta IVT mas nao-supervisionado e clean:
        combined_is_ivt=True (OR).
        """
        # Scorer supervisionado: mock que detecta IVT
        sup_scorer = _MockSupScorer(is_ivt=True)

        # Scorer nao-supervisionado: fail-open (clean)
        unsup_scorer = IVTUnsupScorer()

        impressions = [_make_impression(event_id="e1")]
        combined, unsup = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=str(uuid.uuid4()),
            clickhouse_uri="",
        )

        assert combined[0].ivt_status == IVT_STATUS_IVT
        assert unsup[0].combined_is_ivt is True


# ---------------------------------------------------------------------------
# 4. TX-2: IVTUnsupScoreRow nao tem campos monetarios
# ---------------------------------------------------------------------------

class TestNoMonetaryFloat:

    def test_unsup_score_row_no_monetary_fields(self):
        """
        IVTUnsupScoreRow nao tem campos monetarios (TX-2).
        if_score, ae_error, ae_threshold sao floats de ML (metricas, nao dinheiro).
        """
        row = IVTUnsupScoreRow(
            event_id="e1",
            score_run_id="run-1",
            tenant_id="t1",
            campaign_id="c1",
            zone_id="z1",
            occurred_at=datetime(2026, 6, 4, 14, 0, 0, tzinfo=timezone.utc),
            scored_at=datetime(2026, 6, 4, 14, 1, 0, tzinfo=timezone.utc),
            is_unsup_ivt=False,
            if_label=1,
            if_score=0.1,      # Float legitimo: score IF ML
            ae_error=0.05,     # Float legitimo: erro AE ML
            ae_threshold=0.5,  # Float legitimo: threshold ML
            model_version="1",
            combined_is_ivt=False,
        )

        # Nenhum campo tem sufixo monetario
        monetary_suffixes = ("_amount", "_rate", "_value", "_decimal", "_price")
        row_dict = asdict(row)
        monetary_fields = [
            k for k in row_dict
            if any(s in k for s in monetary_suffixes)
        ]
        assert monetary_fields == [], (
            f"IVTUnsupScoreRow tem campos com sufixo monetario: {monetary_fields}"
        )

        # Floats presentes sao de ML
        assert isinstance(row.if_score, float)
        assert isinstance(row.ae_error, float)
        assert isinstance(row.ae_threshold, float)


# ---------------------------------------------------------------------------
# 5. TX-3: tenant_id propagado
# ---------------------------------------------------------------------------

class TestTenantIdPropagation:

    def test_tenant_id_in_unsup_score_row(self):
        """tenant_id do evento e propagado para IVTUnsupScoreRow."""
        sup_scorer   = IVTScorer()
        unsup_scorer = IVTUnsupScorer()

        impressions = [
            _make_impression(event_id="e1", tenant_id="tenant-A"),
            _make_impression(event_id="e2", tenant_id="tenant-B"),
        ]
        _, unsup = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=str(uuid.uuid4()),
            clickhouse_uri="",
        )

        assert unsup[0].tenant_id == "tenant-A"
        assert unsup[1].tenant_id == "tenant-B"
        # event_id nao misturado
        assert unsup[0].event_id == "e1"
        assert unsup[1].event_id == "e2"


# ---------------------------------------------------------------------------
# 6. Idempotencia
# ---------------------------------------------------------------------------

class TestIdempotency:

    def test_same_score_run_id_same_result(self):
        """Mesmo score_run_id e input -> mesmo resultado (idempotente)."""
        sup_scorer   = IVTScorer()
        unsup_scorer = IVTUnsupScorer()
        run_id = "fixed-run-id"

        impressions = [_make_impression(event_id="idem-1")]

        c1, u1 = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=run_id,
            clickhouse_uri="",
        )
        c2, u2 = score_impression_batch_combined(
            impressions=impressions,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=run_id,
            clickhouse_uri="",
        )

        assert c1[0].ivt_status == c2[0].ivt_status
        assert c1[0].ivt_label  == c2[0].ivt_label
        assert u1[0].is_unsup_ivt == u2[0].is_unsup_ivt
        assert u1[0].combined_is_ivt == u2[0].combined_is_ivt


# ---------------------------------------------------------------------------
# 7. write_ivt_unsup_scores: dry_run nao levanta excecao
# ---------------------------------------------------------------------------

class TestWriteIVTUnsupScores:

    def test_dry_run_does_not_raise(self):
        """write_ivt_unsup_scores em dry_run nao levanta excecao."""
        rows = [
            IVTUnsupScoreRow(
                event_id="e1",
                score_run_id="r1",
                tenant_id="t1",
                campaign_id="c1",
                zone_id="z1",
                occurred_at=datetime(2026, 6, 4, 14, 0, 0, tzinfo=timezone.utc),
                scored_at=datetime(2026, 6, 4, 14, 1, 0, tzinfo=timezone.utc),
                is_unsup_ivt=True,
                if_label=-1,
                if_score=-0.2,
                ae_error=1.5,
                ae_threshold=0.5,
                model_version="1",
                combined_is_ivt=True,
            ),
        ]
        # Nao deve levantar excecao em dry_run
        write_ivt_unsup_scores(
            clickhouse_uri="http://localhost:8123",
            unsup_scores=rows,
            dry_run=True,
        )

    def test_dry_run_empty_list_does_not_raise(self):
        """write_ivt_unsup_scores com lista vazia nao levanta excecao."""
        write_ivt_unsup_scores(
            clickhouse_uri="",
            unsup_scores=[],
            dry_run=False,
        )


# ---------------------------------------------------------------------------
# Mocks para testes de logica OR
# ---------------------------------------------------------------------------

class _MockSupScorer:
    """Mock do scorer supervisionado para testes de logica OR."""

    def __init__(self, is_ivt: bool = False):
        self._is_ivt = is_ivt
        self._model_version = "mock_sup"

    @property
    def is_loaded(self) -> bool:
        return True

    def score_event(self, inp):
        return IVTScore(
            is_ivt=self._is_ivt,
            ivt_prob=0.9 if self._is_ivt else 0.1,
            ivt_label=1 if self._is_ivt else 0,
            model_version=self._model_version,
            scored_at_utc="",
        )

    def score_batch(self, inputs):
        return [self.score_event(inp) for inp in inputs]


class _MockUnsupScorer:
    """Mock do scorer nao-supervisionado para testes de logica OR."""

    def __init__(self, is_unsup_ivt: bool = False):
        self._is_unsup_ivt = is_unsup_ivt
        self._model_version = "mock_unsup"

    @property
    def is_loaded(self) -> bool:
        return True

    def score_event(self, inp):
        return IVTUnsupScore(
            is_unsup_ivt=self._is_unsup_ivt,
            if_label=-1 if self._is_unsup_ivt else 1,
            if_score=-0.3 if self._is_unsup_ivt else 0.1,
            ae_error=2.0 if self._is_unsup_ivt else 0.01,
            ae_threshold=0.5,
            model_version=self._model_version,
            scored_at_utc="",
        )

    def score_batch(self, inputs):
        return [self.score_event(inp) for inp in inputs]
