"""
ml/ope/test_ope.py
==================
Suite de testes para os estimadores OPE (IPS/SNIPS/DR).

Cobertura obrigatoria (ADR-0003 §G, linha J3):
  1. Filtro de ml_fail_open — decisoes fail-open sao excluidas
  2. Positividade — propensidade <= 0 ou > 1 e recusada/avisada
  3. IPS ≈ SNIPS no caso degenerado (politica alvo == politica de logging)
  4. DR = componente DM + correcao IPS
  5. ESS reportado e valido
  6. Dataset vazio retorna NaN sem crash

Casos adicionais:
  7. Pesos de importancia corretos (razao propensidades)
  8. SNIPS e invariante a escala uniforme dos pesos
  9. PositivityError levantada quando violacao e massiva
  10. OPEReport.__str__ formata corretamente
"""
import math
import warnings

import numpy as np
import pytest

from ml.ope.dataset import OPEDataset, OPERecord
from ml.ope.estimators import (
    DREstimator,
    IPSEstimator,
    OPEReport,
    PositivityError,
    SNIPSEstimator,
    _compute_weights,
    _kish_ess,
)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

def make_record(
    decision_id: str,
    propensity: float,
    reward: float,
    ml_fail_open: bool = False,
    reward_model: float | None = None,
) -> OPERecord:
    return OPERecord(
        decision_id=decision_id,
        propensity=propensity,
        reward=reward,
        ml_fail_open=ml_fail_open,
        reward_model=reward_model,
    )


def uniform_records(n: int, propensity: float = 0.5, reward_rate: float = 0.05) -> list[OPERecord]:
    """Cria N registros com propensidade uniforme e rewards binarios deterministicos."""
    records = []
    for i in range(n):
        r = 1.0 if i % int(1 / reward_rate) == 0 else 0.0
        records.append(make_record(f"d{i}", propensity=propensity, reward=r))
    return records


# ---------------------------------------------------------------------------
# 1. Filtro de ml_fail_open
# ---------------------------------------------------------------------------

class TestFailOpenFilter:
    """Testa o invariante arquitetural de exclusao de decisoes fail-open."""

    def test_fail_open_records_excluded_entirely(self):
        """
        Decisoes com ml_fail_open=True NUNCA entram no OPEDataset.
        Este e o invariante mais critico do J3.
        """
        records = [
            make_record("ok1", propensity=0.3, reward=1.0, ml_fail_open=False),
            make_record("fail1", propensity=1.0, reward=0.0, ml_fail_open=True),
            make_record("ok2", propensity=0.6, reward=0.0, ml_fail_open=False),
            make_record("fail2", propensity=1.0, reward=1.0, ml_fail_open=True),
        ]
        ds = OPEDataset.from_records(records)

        # Apenas 2 registros validos (os fail-open foram removidos).
        assert ds.n_valid == 2
        assert ds.n_fail_open_filtered == 2
        assert ds.n_total == 4

        # Os records validos nao tem ml_fail_open=True.
        for r in ds.records:
            assert not r.ml_fail_open, f"Record {r.decision_id} passou o filtro com ml_fail_open=True"

    def test_all_fail_open_produces_empty_dataset(self):
        """Se todos os registros sao fail-open, o dataset fica vazio."""
        records = [
            make_record("f1", propensity=1.0, reward=0.0, ml_fail_open=True),
            make_record("f2", propensity=1.0, reward=1.0, ml_fail_open=True),
        ]
        ds = OPEDataset.from_records(records)
        assert ds.n_valid == 0
        assert ds.n_fail_open_filtered == 2
        assert len(ds.records) == 0

    def test_fail_open_with_propensity_10_excluded(self):
        """
        Decisoes fail-open tem propensity=1.0 por convencao (TX-4/DA-3).
        Mesmo com propensidade valida, sao excluidas pelo flag.
        """
        records = [
            make_record("x1", propensity=1.0, reward=0.0, ml_fail_open=True),
        ]
        ds = OPEDataset.from_records(records)
        assert ds.n_valid == 0
        assert ds.n_fail_open_filtered == 1

    def test_zero_fail_open_records_pass_through(self):
        """Sem registros fail-open, todos passam (exceto positividade)."""
        records = [
            make_record("a", propensity=0.3, reward=1.0, ml_fail_open=False),
            make_record("b", propensity=0.7, reward=0.0, ml_fail_open=False),
        ]
        ds = OPEDataset.from_records(records)
        assert ds.n_fail_open_filtered == 0
        assert ds.n_valid == 2

    def test_ips_estimate_with_mixed_fail_open(self):
        """
        Com registros fail-open misturados, o estimador IPS so ve os validos.
        Garante que o resultado nao e contaminado pelos fail-open.
        """
        # Cenario: 5 registros validos com propensidade=0.5, reward=1.0
        # + 100 registros fail-open com propensidade=1.0, reward=0.0
        # Sem o filtro, o estimador veria muitos zeros e o uplift ficaria falso.
        valid = [make_record(f"v{i}", propensity=0.5, reward=1.0) for i in range(5)]
        fail_open = [make_record(f"f{i}", propensity=1.0, reward=0.0, ml_fail_open=True) for i in range(100)]
        all_records = valid + fail_open
        np.random.shuffle(all_records)

        ds = OPEDataset.from_records(all_records)
        assert ds.n_valid == 5
        assert ds.n_fail_open_filtered == 100

        # Com politica alvo = logging (target_prop = 0.5), IPS = media de recompensa = 1.0
        ips = IPSEstimator()
        target = [0.5] * 5
        report = ips.estimate(ds, target)

        # IPS deve refletir apenas os 5 validos (reward=1.0 todos)
        assert report.n_valid == 5
        assert abs(report.estimate - 1.0) < 1e-9, f"IPS esperava 1.0, got {report.estimate}"


# ---------------------------------------------------------------------------
# 2. Positividade
# ---------------------------------------------------------------------------

class TestPositivity:
    """Testa a checagem de positividade da propensidade de logging."""

    def test_zero_propensity_filtered(self):
        """Registros com propensidade = 0 sao filtrados (violacao de positividade)."""
        # Use 9 valid + 1 bad = 10% violation (abaixo do limiar de rejeicao de 50%)
        # para que o aviso seja emitido mas nao PositivityError.
        records = [make_record(f"ok{i}", propensity=0.5, reward=float(i % 2)) for i in range(9)]
        records.append(make_record("bad", propensity=0.0, reward=0.0))

        with warnings.catch_warnings(record=True) as w:
            warnings.simplefilter("always")
            ds = OPEDataset.from_records(records, positivity_warn_threshold=0.0)
        # Aviso esperado sobre positividade violada.
        positivity_warnings = [x for x in w if issubclass(x.category, UserWarning)]
        assert len(positivity_warnings) >= 1

        assert ds.n_valid == 9
        assert ds.n_positivity_filtered == 1

    def test_negative_propensity_filtered(self):
        """Propensidade negativa e invalida e deve ser filtrada."""
        records = [make_record(f"ok{i}", propensity=0.3, reward=0.0) for i in range(9)]
        records.append(make_record("neg", propensity=-0.1, reward=1.0))

        ds = OPEDataset.from_records(records, positivity_warn_threshold=0.0)
        assert ds.n_valid == 9
        assert ds.n_positivity_filtered == 1

    def test_propensity_above_one_filtered(self):
        """Propensidade > 1 e invalida."""
        records = [make_record(f"ok{i}", propensity=0.5, reward=1.0) for i in range(9)]
        records.append(make_record("big", propensity=1.5, reward=0.0))

        ds = OPEDataset.from_records(records, positivity_warn_threshold=0.0)
        assert ds.n_valid == 9
        assert ds.n_positivity_filtered == 1

    def test_positivity_rejection_threshold(self):
        """Quando a fracao de violacoes supera o limiar de rejeicao, PositivityError."""
        # 60% de registros com propensidade invalida -> excede 50% de limiar padrao
        records = [
            make_record("ok1", propensity=0.4, reward=0.0),
            make_record("ok2", propensity=0.6, reward=1.0),
            make_record("bad1", propensity=0.0, reward=0.0),
            make_record("bad2", propensity=0.0, reward=0.0),
            make_record("bad3", propensity=-0.5, reward=0.0),
        ]
        with pytest.raises(PositivityError, match="Positividade violada"):
            OPEDataset.from_records(records, positivity_reject_threshold=0.5)

    def test_propensity_exactly_one_is_valid(self):
        """propensity=1.0 e valido (cascata deterministica pura sem fail-open)."""
        records = [make_record("d1", propensity=1.0, reward=0.0, ml_fail_open=False)]
        ds = OPEDataset.from_records(records)
        assert ds.n_valid == 1
        assert ds.n_positivity_filtered == 0


# ---------------------------------------------------------------------------
# 3. IPS ≈ SNIPS no caso degenerado
# ---------------------------------------------------------------------------

class TestIPSSnipsDegenerate:
    """
    Quando a politica alvo == politica de logging (target = logging propensities),
    todos os pesos de importancia sao 1.0 e IPS == SNIPS == media de recompensa.

    Este e o caso de teste "degenerado" mencionado no ADR J3.
    """

    def test_ips_equals_mean_reward_when_policies_equal(self):
        """IPS com target==logging deve retornar a media de recompensa."""
        records = uniform_records(100, propensity=0.4, reward_rate=0.1)
        ds = OPEDataset.from_records(records)
        target = [r.propensity for r in ds.records]  # target == logging

        ips = IPSEstimator()
        report = ips.estimate(ds, target)

        baseline = report.baseline
        assert abs(report.estimate - baseline) < 1e-9, (
            f"IPS com target==logging deve ser igual a baseline. "
            f"IPS={report.estimate}, baseline={baseline}"
        )
        assert abs(report.uplift) < 1e-9
        assert report.ess_fraction > 0.99, f"ESS deve ser ~N quando pesos sao iguais, got {report.ess_fraction:.2%}"

    def test_snips_equals_mean_reward_when_policies_equal(self):
        """SNIPS com target==logging deve retornar a media de recompensa."""
        records = uniform_records(100, propensity=0.3, reward_rate=0.05)
        ds = OPEDataset.from_records(records)
        target = [r.propensity for r in ds.records]

        snips = SNIPSEstimator()
        report = snips.estimate(ds, target)

        baseline = report.baseline
        assert abs(report.estimate - baseline) < 1e-9, (
            f"SNIPS com target==logging deve ser igual a baseline. "
            f"SNIPS={report.estimate}, baseline={baseline}"
        )

    def test_ips_approximately_equals_snips_when_policies_equal(self):
        """
        IPS ≈ SNIPS no caso degenerado (pesos uniformes).
        Quando os pesos sao todos 1, IPS e SNIPS sao identicos.
        """
        records = uniform_records(200, propensity=0.5, reward_rate=0.08)
        ds = OPEDataset.from_records(records)
        target = [r.propensity for r in ds.records]

        ips = IPSEstimator()
        snips = SNIPSEstimator()
        r_ips = ips.estimate(ds, target)
        r_snips = snips.estimate(ds, target)

        assert abs(r_ips.estimate - r_snips.estimate) < 1e-9, (
            f"IPS ({r_ips.estimate}) deve ser == SNIPS ({r_snips.estimate}) "
            f"quando target==logging"
        )

    def test_ips_snips_differ_when_policies_differ(self):
        """IPS e SNIPS diferem quando a politica alvo difere da de logging."""
        # Politica de logging: propensidade uniforme 0.5
        # Politica alvo: propensidade mais alta (0.8) para os mesmos candidatos
        records = uniform_records(100, propensity=0.5, reward_rate=0.1)
        ds = OPEDataset.from_records(records)
        # Target: 0.8 para todos (diferente de 0.5)
        target = [0.8] * ds.n_valid

        ips = IPSEstimator()
        snips = SNIPSEstimator()
        r_ips = ips.estimate(ds, target)
        r_snips = snips.estimate(ds, target)

        # Os pesos sao 0.8/0.5 = 1.6 uniformes.
        # IPS = 1.6 * mean(reward); SNIPS = sum(1.6*r) / sum(1.6) = mean(reward)
        # Neste caso, pesos uniformes => IPS != baseline mas SNIPS == baseline.
        # Mas como os pesos sao todos iguais: IPS = 1.6 * baseline, SNIPS = baseline.
        assert r_ips.estimate != pytest.approx(r_snips.estimate, abs=1e-9), (
            "IPS e SNIPS devem diferir quando pesos != 1"
        )


# ---------------------------------------------------------------------------
# 4. DR = DM + IPS_correction
# ---------------------------------------------------------------------------

class TestDRDecomposition:
    """Testa a decomposicao DR = DM + correcao IPS."""

    def test_dr_equals_dm_plus_ips_correction(self):
        """V_DR = V_DM + (1/N) sum w_i * (r_i - r_hat_i)"""
        records = uniform_records(50, propensity=0.4, reward_rate=0.1)
        ds = OPEDataset.from_records(records)
        n = ds.n_valid
        target = [0.6] * n  # politica alvo diferente da de logging

        # r_hat constante (predicao simplificada)
        r_hat = [0.05] * n

        dr = DREstimator()
        report = dr.estimate(ds, target, reward_model_predictions=r_hat)

        assert report.dm_estimate is not None
        assert report.ips_correction is not None

        # Verifica: DR = DM + IPS_correction
        expected = report.dm_estimate + report.ips_correction  # no-float-ok — test assertion
        assert abs(report.estimate - expected) < 1e-9, (
            f"DR ({report.estimate}) deve ser DM ({report.dm_estimate}) + "
            f"IPS_corr ({report.ips_correction}) = {expected}"
        )

    def test_dr_with_perfect_reward_model_reduces_variance(self):
        """
        Com modelo de recompensa perfeito (r_hat = reward exato),
        o componente IPS do DR se cancela e DR = DM = media de recompensa.
        """
        # Recompensa deterministica: record i tem reward=1 se i%10==0, else 0
        n = 100
        records = []
        for i in range(n):
            r = 1.0 if i % 10 == 0 else 0.0
            records.append(make_record(f"d{i}", propensity=0.5, reward=r))
        ds = OPEDataset.from_records(records)

        target = [0.5] * ds.n_valid  # target == logging
        # r_hat perfeito: o modelo prediz exatamente o reward
        r_hat_perfect = [r.reward for r in ds.records]

        dr = DREstimator()
        report = dr.estimate(ds, target, reward_model_predictions=r_hat_perfect)

        # Com target==logging e r_hat perfeito:
        # IPS_correction = (1/N) sum 1 * (r - r_hat) = 0 (pesos=1, residuos=0)
        # DR = DM = media(r_hat) = media(reward)
        assert abs(report.ips_correction) < 1e-9, (
            f"Correcao IPS deve ser ~0 com r_hat perfeito e target==logging, "
            f"got {report.ips_correction}"
        )
        assert abs(report.estimate - report.baseline) < 1e-9

    def test_dr_with_internal_reward_model_runs(self):
        """DR sem reward_model_predictions externo deve rodar sem erro."""
        records = uniform_records(30, propensity=0.5, reward_rate=0.1)
        ds = OPEDataset.from_records(records)
        target = [0.4] * ds.n_valid

        dr = DREstimator()
        report = dr.estimate(ds, target)  # sem r_hat externo

        assert not math.isnan(report.estimate)
        assert report.dm_estimate is not None
        assert "modelo de recompensa interno" in " ".join(report.warnings).lower()


# ---------------------------------------------------------------------------
# 5. ESS reportado e valido
# ---------------------------------------------------------------------------

class TestESS:
    """Testa o Effective Sample Size (ESS) de Kish."""

    def test_ess_equals_n_when_weights_uniform(self):
        """ESS = N quando todos os pesos sao 1 (target == logging)."""
        n = 50
        records = uniform_records(n, propensity=0.5)
        ds = OPEDataset.from_records(records)
        target = [r.propensity for r in ds.records]  # todos os pesos = 1

        ips = IPSEstimator()
        report = ips.estimate(ds, target)

        assert abs(report.ess - n) < 0.1, f"ESS esperado ={n}, got {report.ess}"
        assert abs(report.ess_fraction - 1.0) < 0.01

    def test_ess_low_with_extreme_weights(self):
        """ESS baixo quando um unico registro tem peso muito alto."""
        n = 50
        records = []
        for i in range(n):
            r = 1.0 if i == 0 else 0.0
            records.append(make_record(f"d{i}", propensity=0.5, reward=r))
        ds = OPEDataset.from_records(records)

        # Politica alvo: alta probabilidade so para o primeiro registro
        target = [0.99] + [0.01] * (n - 1)  # pesos muito assimetricos

        ips = IPSEstimator()
        report = ips.estimate(ds, target)

        # ESS deve ser muito menor que N
        assert report.ess < n * 0.5, f"ESS esperado < {n*0.5}, got {report.ess}"

    def test_kish_ess_formula(self):
        """Testa a formula de ESS de Kish diretamente."""
        # Pesos uniformes: ESS = N
        w_uniform = np.ones(10)
        ess = _kish_ess(w_uniform)
        assert abs(ess - 10.0) < 1e-9

        # Um peso dominante: ESS << N
        w_skewed = np.array([100.0, 1.0, 1.0, 1.0, 1.0])
        ess_skewed = _kish_ess(w_skewed)
        assert ess_skewed < 5.0

    def test_importance_weights_formula(self):
        """Testa que w_i = target / logging."""
        p_log = np.array([0.4, 0.6, 0.3])
        p_tgt = np.array([0.8, 0.3, 0.6])
        expected = p_tgt / p_log  # no-float-ok — test assertion
        w = _compute_weights(p_log, p_tgt)
        np.testing.assert_allclose(w, expected, rtol=1e-9)


# ---------------------------------------------------------------------------
# 6. Dataset vazio retorna NaN sem crash
# ---------------------------------------------------------------------------

class TestEmptyDataset:
    """Testa o comportamento com dataset vazio (todos filtrados)."""

    def test_ips_on_empty_dataset(self):
        """IPS com dataset vazio deve retornar NaN sem crash."""
        ds = OPEDataset.from_records([])
        ips = IPSEstimator()
        report = ips.estimate(ds, [])
        assert math.isnan(report.estimate)
        assert math.isnan(report.baseline)
        assert report.n_valid == 0
        assert len(report.warnings) > 0

    def test_snips_on_empty_dataset(self):
        """SNIPS com dataset vazio deve retornar NaN sem crash."""
        ds = OPEDataset.from_records([])
        snips = SNIPSEstimator()
        report = snips.estimate(ds, [])
        assert math.isnan(report.estimate)

    def test_dr_on_empty_dataset(self):
        """DR com dataset vazio deve retornar NaN sem crash."""
        ds = OPEDataset.from_records([])
        dr = DREstimator()
        report = dr.estimate(ds, [])
        assert math.isnan(report.estimate)

    def test_empty_after_fail_open_filter(self):
        """Se todos os registros sao fail-open, estimadores recebem dataset vazio."""
        records = [make_record(f"f{i}", propensity=1.0, reward=0.0, ml_fail_open=True) for i in range(20)]
        ds = OPEDataset.from_records(records)

        ips = IPSEstimator()
        report = ips.estimate(ds, [])
        assert math.isnan(report.estimate)
        assert report.n_fail_open == 20


# ---------------------------------------------------------------------------
# 7. Pesos de importancia corretos
# ---------------------------------------------------------------------------

class TestWeightCorrectness:
    """Testa que os pesos de importancia sao calculados corretamente."""

    def test_ips_scales_with_weight(self):
        """
        IPS de um registro com propensidade 2x maior deve ter reward 2x maior
        no numerador.
        """
        # Um registro com propensidade de logging=0.5 e reward=1
        records = [make_record("d1", propensity=0.5, reward=1.0)]
        ds = OPEDataset.from_records(records)

        ips = IPSEstimator()
        # target = 1.0: peso = 1.0/0.5 = 2 -> IPS = 2 * 1.0 = 2.0
        report_high = ips.estimate(ds, [1.0])
        assert abs(report_high.estimate - 2.0) < 1e-9, f"Esperado 2.0, got {report_high.estimate}"

        # target = 0.25: peso = 0.25/0.5 = 0.5 -> IPS = 0.5 * 1.0 = 0.5
        report_low = ips.estimate(ds, [0.25])
        assert abs(report_low.estimate - 0.5) < 1e-9, f"Esperado 0.5, got {report_low.estimate}"

    def test_snips_with_uniform_weights_equals_mean(self):
        """
        SNIPS com pesos uniformes (target/logging = constante k para todos)
        deve retornar a media de recompensa (o k cancela na normalizacao).
        """
        n = 10
        records = [make_record(f"d{i}", propensity=0.4, reward=float(i % 2)) for i in range(n)]
        ds = OPEDataset.from_records(records)

        # target = 0.8 para todos -> pesos = 0.8/0.4 = 2.0 uniformes
        target = [0.8] * n
        snips = SNIPSEstimator()
        report = snips.estimate(ds, target)

        expected_mean = sum(i % 2 for i in range(n)) / n  # no-float-ok — test assertion
        assert abs(report.estimate - expected_mean) < 1e-9, (
            f"SNIPS com pesos uniformes deve = media de recompensa ({expected_mean}), "
            f"got {report.estimate}"
        )


# ---------------------------------------------------------------------------
# 8. OPEReport.__str__ formata sem crash
# ---------------------------------------------------------------------------

class TestOPEReportStr:
    """Testa que o OPEReport pode ser convertido para string sem erro."""

    def test_str_with_valid_report(self):
        records = uniform_records(20, propensity=0.5, reward_rate=0.1)
        ds = OPEDataset.from_records(records)
        target = [r.propensity for r in ds.records]

        ips = IPSEstimator()
        report = ips.estimate(ds, target)
        s = str(report)

        assert "IPS" in s
        assert "baseline" in s.lower()
        assert "uplift" in s.lower()

    def test_str_dr_includes_dm(self):
        records = uniform_records(20, propensity=0.5, reward_rate=0.1)
        ds = OPEDataset.from_records(records)
        target = [r.propensity for r in ds.records]
        r_hat = [0.1] * ds.n_valid

        dr = DREstimator()
        report = dr.estimate(ds, target, r_hat)
        s = str(report)

        assert "DM estimate" in s
        assert "IPS correction" in s


# ---------------------------------------------------------------------------
# 9. OPEDataset.summary()
# ---------------------------------------------------------------------------

class TestDatasetSummary:
    """Testa o metodo summary() do OPEDataset."""

    def test_summary_with_valid_records(self):
        records = uniform_records(50, propensity=0.5, reward_rate=0.1)
        ds = OPEDataset.from_records(records)
        s = ds.summary()

        assert s["n_total"] == 50
        assert s["n_valid"] == 50
        assert s["n_fail_open_filtered"] == 0
        assert abs(s["mean_propensity"] - 0.5) < 1e-9
        assert 0 <= s["mean_reward"] <= 1

    def test_summary_with_fail_open_records(self):
        valid = uniform_records(30, propensity=0.4)
        fail = [make_record(f"f{i}", propensity=1.0, reward=0.0, ml_fail_open=True) for i in range(10)]
        ds = OPEDataset.from_records(valid + fail)
        s = ds.summary()

        assert s["n_fail_open_filtered"] == 10
        assert s["n_valid"] == 30
        assert abs(s["frac_fail_open"] - 10 / 40) < 1e-9

    def test_summary_empty_dataset(self):
        ds = OPEDataset.from_records([])
        s = ds.summary()
        assert math.isnan(s["mean_propensity"])
        assert s["n_valid"] == 0


# ---------------------------------------------------------------------------
# 10. OPERecord validacao
# ---------------------------------------------------------------------------

class TestOPERecordValidation:
    """Testa as validacoes do OPERecord."""

    def test_invalid_decision_id_raises(self):
        with pytest.raises(ValueError, match="decision_id"):
            OPERecord(decision_id="", propensity=0.5, reward=1.0)

    def test_non_float_propensity_raises(self):
        with pytest.raises(TypeError, match="propensity"):
            OPERecord(decision_id="d1", propensity="0.5", reward=1.0)  # type: ignore

    def test_valid_record_ok(self):
        r = OPERecord(decision_id="d1", propensity=0.5, reward=1.0)
        assert r.decision_id == "d1"
        assert r.propensity == 0.5
        assert r.reward == 1.0
        assert not r.ml_fail_open


# ---------------------------------------------------------------------------
# 11. Mistura de fail-open + positividade
# ---------------------------------------------------------------------------

class TestMixedFilters:
    """Testa filtragem combinada (ml_fail_open + positividade)."""

    def test_both_filters_applied_independently(self):
        records = [
            make_record("ok", propensity=0.5, reward=1.0),        # valido
            make_record("fo", propensity=1.0, reward=0.0, ml_fail_open=True),  # fail-open
            make_record("zero_prop", propensity=0.0, reward=1.0),   # positividade
            make_record("ok2", propensity=0.3, reward=0.0),         # valido
        ]
        with warnings.catch_warnings(record=True):
            warnings.simplefilter("always")
            ds = OPEDataset.from_records(records, positivity_warn_threshold=0.0)

        assert ds.n_total == 4
        assert ds.n_fail_open_filtered == 1
        assert ds.n_positivity_filtered == 1
        assert ds.n_valid == 2


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
