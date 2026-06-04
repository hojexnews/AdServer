"""
ml/pacing/test_pacing.py
========================
Testes do controlador proporcional de pacing (DA-4).

Cobre:
  1. Convergencia para deficit zero: campanha no ritmo -> pacing_deficit=0
  2. Campanha atrasada: pacing_deficit proporcional ao atraso
  3. Campanha adiantada: pacing_deficit=0 (sem negativo)
  4. Voo invalido (duração zero): ajuste zero seguro
  5. Forecast de trafego: media ponderada converge para a media do historico
  6. Batch: compute_batch retorna um resultado por campanha
  7. PII-free: nenhum campo de usuario no CampaignPacingState
  8. Invariante TX-2: pacing_deficit e adimensional (nao monetario)
"""
from __future__ import annotations

import math
from datetime import datetime, timedelta, timezone

import pytest

from ml.pacing.controller import (
    CampaignPacingState,
    ProportionalPacingController,
    forecast_remaining_impressions,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _now() -> datetime:
    return datetime.now(timezone.utc)


def _state(
    goal: int,
    delivered: int,
    elapsed_fraction: float,
    history=None,
    campaign_id: str = "camp-001",
) -> CampaignPacingState:
    """
    Constrói um CampaignPacingState com elapsed_fraction ja aplicado.
    Voo de 24h; now = start + elapsed_fraction * 24h.
    """
    start = datetime(2026, 6, 4, 0, 0, 0, tzinfo=timezone.utc)
    end = start + timedelta(hours=24)
    now = start + timedelta(hours=24 * elapsed_fraction)
    return CampaignPacingState(
        campaign_id=campaign_id,
        goal_impressions=goal,
        delivered_impressions=delivered,
        flight_start_utc=start,
        flight_end_utc=end,
        now_utc=now,
        forecast_hourly_impressions=history,
    )


# ---------------------------------------------------------------------------
# Testes do controlador proporcional
# ---------------------------------------------------------------------------

class TestProportionalController:

    def test_no_deficit_on_pace(self):
        """Campanha entregando exatamente no ritmo: pacing_deficit == 0."""
        ctrl = ProportionalPacingController(kp=1.0)
        # 50% do tempo decorrido, 50% da meta entregue
        s = _state(goal=100_000, delivered=50_000, elapsed_fraction=0.5)
        adj = ctrl.compute(s)
        assert adj.pacing_deficit == 0.0, (
            f"Campanha no ritmo deve ter deficit=0; got {adj.pacing_deficit}"
        )
        assert adj.adjustment_factor == 1.0

    def test_deficit_behind_schedule(self):
        """Campanha atrasada: pacing_deficit proporcional ao atraso."""
        ctrl = ProportionalPacingController(kp=1.0)
        # 50% do tempo, apenas 25% entregue -> deficit = 0.5 - 0.25 = 0.25
        s = _state(goal=100_000, delivered=25_000, elapsed_fraction=0.5)
        adj = ctrl.compute(s)
        assert abs(adj.pacing_deficit - 0.25) < 1e-6, (
            f"deficit esperado 0.25; got {adj.pacing_deficit}"
        )
        assert adj.adjustment_factor > 1.0

    def test_no_negative_deficit_ahead_of_schedule(self):
        """Campanha adiantada nao produz deficit negativo."""
        ctrl = ProportionalPacingController(kp=1.0)
        # 20% do tempo, 80% entregue -> deficit bruto negativo -> clip em 0
        s = _state(goal=100_000, delivered=80_000, elapsed_fraction=0.2)
        adj = ctrl.compute(s)
        assert adj.pacing_deficit == 0.0, (
            f"Campanha adiantada deve ter deficit=0; got {adj.pacing_deficit}"
        )
        assert adj.adjustment_factor == 1.0

    def test_max_deficit_at_start_no_delivery(self):
        """Campanha com 50% do tempo e zero entregas: deficit = 0.5."""
        ctrl = ProportionalPacingController(kp=1.0)
        s = _state(goal=100_000, delivered=0, elapsed_fraction=0.5)
        adj = ctrl.compute(s)
        assert abs(adj.pacing_deficit - 0.5) < 1e-6, (
            f"deficit esperado 0.5; got {adj.pacing_deficit}"
        )

    def test_full_deficit_zero_delivery_full_time(self):
        """Campanha com 100% do tempo e zero entregas: deficit = 1.0."""
        ctrl = ProportionalPacingController(kp=1.0)
        s = _state(goal=100_000, delivered=0, elapsed_fraction=1.0)
        adj = ctrl.compute(s)
        assert abs(adj.pacing_deficit - 1.0) < 1e-6

    def test_invalid_flight_zero_duration(self):
        """Voo de duracao zero retorna ajuste zero (sem crash)."""
        ctrl = ProportionalPacingController(kp=1.0)
        now = _now()
        s = CampaignPacingState(
            campaign_id="zero-duration",
            goal_impressions=50_000,
            delivered_impressions=0,
            flight_start_utc=now,
            flight_end_utc=now,  # start == end -> duracao zero
            now_utc=now,
        )
        adj = ctrl.compute(s)
        assert adj.pacing_deficit == 0.0
        assert adj.adjustment_factor == 1.0

    def test_deficit_capped_at_one(self):
        """pacing_deficit nunca ultrapassa 1.0."""
        ctrl = ProportionalPacingController(kp=1.0)
        # 100% do tempo, zero entregas
        s = _state(goal=100_000, delivered=0, elapsed_fraction=1.0)
        adj = ctrl.compute(s)
        assert adj.pacing_deficit <= 1.0

    def test_adjustment_factor_capped(self):
        """adjustment_factor respeita max_adjustment."""
        ctrl = ProportionalPacingController(kp=1.0, max_adjustment=1.5)
        s = _state(goal=100_000, delivered=0, elapsed_fraction=1.0)
        adj = ctrl.compute(s)
        assert adj.adjustment_factor <= 1.5

    def test_kp_scales_adjustment(self):
        """Kp maior produz adjustment_factor maior para mesmo deficit."""
        ctrl1 = ProportionalPacingController(kp=0.5)
        ctrl2 = ProportionalPacingController(kp=2.0)
        s = _state(goal=100_000, delivered=25_000, elapsed_fraction=0.5)
        adj1 = ctrl1.compute(s)
        adj2 = ctrl2.compute(s)
        assert adj2.adjustment_factor > adj1.adjustment_factor, (
            "Kp maior deve produzir adjustment_factor maior"
        )
        assert adj1.pacing_deficit == adj2.pacing_deficit, (
            "pacing_deficit deve ser identico (nao depende de Kp)"
        )

    def test_convergence_over_time(self):
        """
        Simula multiplos ciclos de pacing: campanha no ritmo em cada passo
        deve ter deficit zero em todos os pontos.

        Este teste valida que o controlador proporcional nao acumula
        erro (sem integral) em campanhas que seguem o cronograma.
        """
        ctrl = ProportionalPacingController(kp=1.0)
        goal = 1_000_000
        for frac in [0.1, 0.25, 0.5, 0.75, 0.9]:
            delivered = int(goal * frac)
            s = _state(goal=goal, delivered=delivered, elapsed_fraction=frac)
            adj = ctrl.compute(s)
            assert adj.pacing_deficit < 1e-6, (
                f"frac={frac}: campanha no ritmo deve ter deficit=0; "
                f"got {adj.pacing_deficit}"
            )

    def test_batch_returns_one_per_campaign(self):
        """compute_batch retorna um resultado por campanha."""
        ctrl = ProportionalPacingController(kp=1.0)
        states = [
            _state(goal=100_000, delivered=40_000, elapsed_fraction=0.5, campaign_id=f"camp-{i}")
            for i in range(5)
        ]
        results = ctrl.compute_batch(states)
        assert len(results) == 5
        for s in states:
            assert s.campaign_id in results

    def test_pii_free_fields(self):
        """CampaignPacingState nao tem campos de usuario (PII-free, TX-5/DA-11)."""
        import dataclasses
        field_names = {f.name for f in dataclasses.fields(CampaignPacingState)}
        # Campos proibidos: nao pode ter ip, user_id, cookie, ua
        for forbidden in ("ip", "user_id", "cookie", "user_agent", "email"):
            assert forbidden not in field_names, (
                f"Campo PII '{forbidden}' encontrado em CampaignPacingState (TX-5/DA-11)"
            )

    def test_tx2_pacing_deficit_is_not_monetary(self):
        """
        pacing_deficit e ratio adimensional [0,1] — nao monetario.
        TX-2 nao se aplica a ratios de entrega, mas verificamos que
        o campo nunca e int (nao e minor-units) e fica em [0,1].
        """
        ctrl = ProportionalPacingController(kp=1.0)
        s = _state(goal=100_000, delivered=30_000, elapsed_fraction=0.6)
        adj = ctrl.compute(s)
        assert isinstance(adj.pacing_deficit, float), (
            "pacing_deficit deve ser float (ratio adimensional)"
        )
        assert 0.0 <= adj.pacing_deficit <= 1.0


# ---------------------------------------------------------------------------
# Testes do forecast de trafego leve
# ---------------------------------------------------------------------------

class TestForecastRemainingImpressions:

    def test_empty_history_returns_zero(self):
        assert forecast_remaining_impressions([], hours_remaining=10) == 0

    def test_constant_history_projects_linearly(self):
        """Historico constante: projecao = media * horas restantes."""
        history = [100] * 24
        result = forecast_remaining_impressions(history, hours_remaining=5.0)
        assert result == 500, f"Esperado 500; got {result}"

    def test_recent_hours_have_more_weight(self):
        """Historico crescente: media ponderada deve ser maior que media simples."""
        history = list(range(1, 25))  # [1, 2, ..., 24]
        result = forecast_remaining_impressions(history, hours_remaining=1.0)
        simple_mean = sum(history) / len(history)  # 12.5
        assert result >= simple_mean, (
            f"Media ponderada ({result}) deve ser >= media simples ({simple_mean:.1f})"
        )

    def test_zero_hours_remaining(self):
        """Zero horas restantes -> zero impressoes projetadas."""
        history = [100] * 10
        assert forecast_remaining_impressions(history, hours_remaining=0.0) == 0

    def test_window_limits_history(self):
        """window=5 usa apenas as ultimas 5 observacoes."""
        # Historico com pico inicial (descartado) e constante recente
        history = [10000] * 20 + [100] * 5
        result = forecast_remaining_impressions(history, hours_remaining=2.0, window=5)
        # Com window=5, usa apenas as ultimas 5 (100 cada)
        assert result == 200, f"Esperado 200; got {result}"
