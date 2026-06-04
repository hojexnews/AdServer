"""
ml/ope/estimators.py
====================
Estimadores OPE honestos: IPS, SNIPS, DR.

Todos os estimadores trabalham sobre OPEDataset (que JA aplicou o filtro de
ml_fail_open e positividade). Os estimadores NUNCA recebem registros brutos;
sempre recebem um OPEDataset ja filtrado.

TEORIA:
  Queremos estimar o valor V(pi) de uma politica nova pi dado que os dados foram
  coletados sob uma politica de logging mu com propensidade p(a|x) = propensity.

  IPS: V_IPS(pi) = (1/N) sum_i [ pi(a_i|x_i) / p(a_i|x_i) ] * r_i
  onde pi(a_i|x_i) e a probabilidade que a politica NOVA daria a acao logada.

  SNIPS: V_SNIPS(pi) = sum_i [ w_i * r_i ] / sum_i [ w_i ]
  onde w_i = pi(a_i|x_i) / p(a_i|x_i)

  DR:   V_DR(pi) = V_DM + (1/N) sum_i [ w_i * (r_i - r_hat_i) ]
  onde r_hat_i e a predicao do modelo de recompensa direto (DM).

PROPENSIDADE DA POLITICA NOVA (target_propensity):
  Para avaliar um modelo novo (candidato), precisamos de pi(a_i|x_i) — a
  propensidade que ele teria dado a acao SERVIDA. Tres fontes possiveis:
    1. Shadow log: o ranker sombra registrou explicitamente sua propensidade
       para a acao servida. Ideal — e o caso do J3 shadow log.
    2. Deterministica: pi e deterministica (sempre escolhe a mesma acao top-1);
       pi(a_i|x_i) = 1 se a_i == pi_top1(x_i), 0 caso contrario.
       Neste caso, o estimador so usa registros onde pi concordou com mu.
    3. Modelo de score: pi(a_i|x_i) derivado do score do modelo novo.
       E o que usaremos quando o shadow log estiver disponivel.

  Para J3, o target_propensities vem do shadow log (campo shadow_propensity
  no OPERecord, ou passado explicitamente como array).

NOTA SOBRE DINHEIRO (TX-2):
  Se reward for em minor-units int64 (ex.: eCPM), os calculos intermediarios
  IPS/SNIPS/DR produzem floats (pesos * recompensa). Isso e aceitavel porque:
    1. O resultado do estimador e um SINAL DE COMPARACAO entre politicas,
       nao um valor contabil.
    2. A comparacao deve ser feita DENTRO da mesma moeda/tenant (DA-10).
    3. O valor contabilizavel retorna para int64 na decisao final de negocio.
  Nunca usar o valor estimado como valor faturavel diretamente.
  O marcador `# no-float-ok — OPE signal, not money` indica esses calculos.
"""
from __future__ import annotations

import warnings
from dataclasses import dataclass, field
from typing import List, Optional, Sequence

import numpy as np

from ml.ope.dataset import OPEDataset, OPERecord


# ---------------------------------------------------------------------------
# Excecao de positividade
# ---------------------------------------------------------------------------

class PositivityError(ValueError):
    """
    Levantada quando a violacao de positividade supera o limiar de rejeicao.

    O OPEDataset levanta esta excecao no from_records quando a fracao de
    registros com propensity invalido (0 ou >1) supera o limiar configurado.
    Tambem levantada pelos estimadores se encontrarem propensidade zero
    em registros que passaram o filtro (nao deveria acontecer, mas e uma
    guarda defensiva).
    """


# ---------------------------------------------------------------------------
# Resultado estruturado
# ---------------------------------------------------------------------------

@dataclass
class OPEReport:
    """
    Resultado de uma estimativa OPE.

    Campos principais:
      estimator:    nome do estimador ("IPS", "SNIPS", "DR")
      estimate:     V_hat(pi) — valor estimado da politica nova
      baseline:     V_mu — media de recompensa sob a politica de logging
      uplift:       estimate - baseline (ganho relativo: uplift / baseline)
      uplift_pct:   uplift como percentual do baseline (None se baseline==0)
      ess:          Effective Sample Size (para IPS/SNIPS; quanto do N e util)
      ess_fraction: ess / n_valid (fracao do dataset que contribui efetivamente)
      n_valid:      numero de registros validos usados na estimativa
      n_total:      numero total de registros antes do filtro
      n_fail_open:  numero de registros filtrados por ml_fail_open
      warnings:     lista de avisos gerados durante a estimativa

    Para IPS/SNIPS:
      ESS = (sum w_i)^2 / sum(w_i^2) — metrica de Kish.
      ESS baixo (< N/10) indica alta variancia: o uplift pode nao ser confiavel.

    Para DR:
      dm_estimate:  estimativa do modelo direto (DM) — componente de bias do DR
      ips_correction: correcao IPS do DR
    """
    estimator: str
    estimate: float           # V_hat(pi) — valor estimado
    baseline: float           # V_mu — recompensa media sob a politica de logging
    uplift: float             # estimate - baseline
    uplift_pct: Optional[float]  # uplift / baseline * 100, ou None se baseline==0

    ess: float                # Effective Sample Size (IPS: Kish; DR: ver doc)
    ess_fraction: float       # ess / n_valid

    n_valid: int              # registros usados
    n_total: int              # total antes do filtro
    n_fail_open: int          # filtrados por ml_fail_open

    warnings: List[str] = field(default_factory=list)

    # Campos opcionais para DR
    dm_estimate: Optional[float] = None
    ips_correction: Optional[float] = None

    def __str__(self) -> str:
        lines = [
            f"OPEReport({self.estimator})",
            f"  n_valid={self.n_valid} / n_total={self.n_total} "
            f"(fail_open filtrado: {self.n_fail_open})",
            f"  baseline (mu)  = {self.baseline:.6f}",
            f"  estimate (pi)  = {self.estimate:.6f}",
            f"  uplift         = {self.uplift:+.6f}",
            f"  uplift_pct     = {self.uplift_pct:.2f}%" if self.uplift_pct is not None else "  uplift_pct     = N/A",
            f"  ESS            = {self.ess:.1f} ({self.ess_fraction:.1%} do dataset)",
        ]
        if self.dm_estimate is not None:
            lines.append(f"  DM estimate    = {self.dm_estimate:.6f}")
            lines.append(f"  IPS correction = {self.ips_correction:+.6f}")
        for w in self.warnings:
            lines.append(f"  AVISO: {w}")
        return "\n".join(lines)


# ---------------------------------------------------------------------------
# Utilitario: computa pesos de importancia
# ---------------------------------------------------------------------------

def _compute_weights(
    propensities_logging: np.ndarray,   # p(a|x) sob politica de logging
    propensities_target: np.ndarray,    # pi(a|x) sob politica nova
) -> np.ndarray:
    """
    Computa pesos de importancia w_i = pi(a_i|x_i) / p(a_i|x_i).

    Args:
      propensities_logging: p(a|x) logado, shape (N,), todos em (0, 1].
      propensities_target:  pi(a|x) para a politica nova, shape (N,).

    Returns:
      weights: array de pesos, shape (N,).

    Nota: divisao por zero e impossivel aqui porque OPEDataset ja filtrou
    propensity <= 0. Se acontecer, e um bug interno — levanta ValueError.
    """
    if np.any(propensities_logging <= 0):
        raise PositivityError(
            "Propensidade de logging zero encontrada nos pesos. "
            "OPEDataset deveria ter filtrado esses registros."
        )
    # no-float-ok — OPE signal: importance weights, not money
    weights = propensities_target / propensities_logging  # type: ignore[operator]
    return weights


def _kish_ess(weights: np.ndarray) -> float:
    """
    Kish Effective Sample Size: ESS = (sum w)^2 / sum(w^2).

    Mede quantas amostras independentes e equivalente ao conjunto ponderado.
    ESS = N quando todos os pesos sao iguais (politica alvo == logging).
    ESS << N indica alta variancia — o estimador pode nao ser confiavel.

    Retorna ESS em [1, N].
    """
    # no-float-ok — OPE signal: ESS metric, not money
    w_sum = float(np.sum(weights))
    w_sum_sq = float(np.sum(weights ** 2))
    if w_sum_sq == 0:
        return 0.0
    return (w_sum ** 2) / w_sum_sq  # no-float-ok — ESS calculation


# ---------------------------------------------------------------------------
# IPS (Importance-Weighted Policy Estimator)
# ---------------------------------------------------------------------------

class IPSEstimator:
    """
    Estimador IPS (Inverse Propensity Score).

    V_IPS(pi) = (1/N) sum_i [ pi(a_i|x_i) / p(a_i|x_i) ] * r_i

    Pros:  consistente se a positividade for satisfeita.
    Contras: alta variancia quando pi diverge muito de mu (pesos grandes).

    Para mitigar variancia, o SNIPS (auto-normalizado) e preferido quando os
    pesos sao muito heterogeneos (ver SNIPSEstimator).
    """

    _LOW_ESS_FRACTION_THRESHOLD = 0.1  # aviso se ESS < 10% do N valido
    _HIGH_WEIGHT_CLIP = 100.0          # clip de pesos extremos (opcao defensiva)

    def estimate(
        self,
        dataset: OPEDataset,
        target_propensities: Sequence[float],
        clip_weights: bool = False,
    ) -> OPEReport:
        """
        Estima V(pi) usando IPS.

        Args:
          dataset:              OPEDataset JA filtrado (fail-open removido).
          target_propensities:  pi(a_i|x_i) para cada registro no dataset.records
                                na MESMA ORDEM. Shape deve ser len(dataset.records).
          clip_weights:         se True, clipa pesos em self._HIGH_WEIGHT_CLIP
                                (reduz variancia, introduz small bias).

        Returns:
          OPEReport com estimate, baseline, uplift, ESS.

        Levanta:
          ValueError se target_propensities tiver comprimento diferente de dataset.n_valid.
          PositivityError se algum propensity de logging for zero (bug interno).
        """
        records = dataset.records
        n = len(records)
        warn_list: List[str] = []

        if len(target_propensities) != n:
            raise ValueError(
                f"target_propensities tem {len(target_propensities)} elementos, "
                f"mas dataset.records tem {n}."
            )

        if n == 0:
            return OPEReport(
                estimator="IPS",
                estimate=float("nan"),
                baseline=float("nan"),
                uplift=float("nan"),
                uplift_pct=None,
                ess=0.0,
                ess_fraction=0.0,
                n_valid=0,
                n_total=dataset.n_total,
                n_fail_open=dataset.n_fail_open_filtered,
                warnings=["Dataset vazio apos filtragem. Estimativa invalida."],
            )

        # Extrai arrays
        p_log = np.array([r.propensity for r in records], dtype=np.float64)
        p_tgt = np.array(target_propensities, dtype=np.float64)
        rewards = np.array([r.reward for r in records], dtype=np.float64)

        # Valida target_propensities: permite 0 (acao nunca tomada pela nova politica)
        # mas nao negativo ou > 1.
        if np.any(p_tgt < 0) or np.any(p_tgt > 1.0 + 1e-9):
            n_bad = int(np.sum((p_tgt < 0) | (p_tgt > 1.0 + 1e-9)))
            warn_list.append(
                f"{n_bad} target_propensities fora de [0,1]. Clipando para [0,1]."
            )
            p_tgt = np.clip(p_tgt, 0.0, 1.0)

        # Pesos de importancia
        weights = _compute_weights(p_log, p_tgt)  # no-float-ok — importance weights

        if clip_weights:
            weights = np.clip(weights, 0.0, self._HIGH_WEIGHT_CLIP)
            warn_list.append(
                f"Pesos clipados em {self._HIGH_WEIGHT_CLIP}. "
                f"Estimativa tem small bias mas menor variancia."
            )

        # ESS — Kish
        ess = _kish_ess(weights)
        ess_frac = ess / n

        if ess_frac < self._LOW_ESS_FRACTION_THRESHOLD:
            warn_list.append(
                f"ESS baixo: {ess:.1f} ({ess_frac:.1%} do dataset valido). "
                f"Alta variancia — uplift pode nao ser confiavel. "
                f"Considere mais exploracao ou politica alvo mais proxima de mu."
            )

        # IPS estimate
        # no-float-ok — OPE signal: weighted reward, not money
        ips_estimate = float(np.mean(weights * rewards))
        baseline = float(np.mean(rewards))
        uplift = ips_estimate - baseline
        uplift_pct = (uplift / baseline * 100.0) if baseline != 0 else None

        return OPEReport(
            estimator="IPS",
            estimate=ips_estimate,
            baseline=baseline,
            uplift=uplift,
            uplift_pct=uplift_pct,
            ess=ess,
            ess_fraction=ess_frac,
            n_valid=n,
            n_total=dataset.n_total,
            n_fail_open=dataset.n_fail_open_filtered,
            warnings=warn_list,
        )


# ---------------------------------------------------------------------------
# SNIPS (Self-Normalized IPS)
# ---------------------------------------------------------------------------

class SNIPSEstimator:
    """
    Estimador SNIPS (Self-Normalized Importance Sampling).

    V_SNIPS(pi) = sum_i [ w_i * r_i ] / sum_i [ w_i ]

    onde w_i = pi(a_i|x_i) / p(a_i|x_i).

    O SNIPS e auto-normalizado: divide pelo total dos pesos em vez de 1/N.
    Isso garante que a estimativa seja invariante a escala dos pesos e reduce
    a variancia do IPS, ao custo de um pequeno vies O(1/N).

    Para N grande (o caso de producao), o vies e negligivel.
    ESS do SNIPS e identico ao do IPS (mesmos pesos).

    Propriedade especial: quando pi == mu (nova politica identica a logging),
    w_i = 1 para todos e SNIPS(pi) == IPS(pi) == media de recompensa.
    Este e o caso de teste "degenerado" do J3: propensidades iguais.
    """

    _LOW_ESS_FRACTION_THRESHOLD = 0.1

    def estimate(
        self,
        dataset: OPEDataset,
        target_propensities: Sequence[float],
    ) -> OPEReport:
        """
        Estima V(pi) usando SNIPS.

        Args:
          dataset:              OPEDataset JA filtrado.
          target_propensities:  pi(a_i|x_i) para cada registro.

        Returns:
          OPEReport com estimativa SNIPS.
        """
        records = dataset.records
        n = len(records)
        warn_list: List[str] = []

        if len(target_propensities) != n:
            raise ValueError(
                f"target_propensities tem {len(target_propensities)} elementos, "
                f"mas dataset.records tem {n}."
            )

        if n == 0:
            return OPEReport(
                estimator="SNIPS",
                estimate=float("nan"),
                baseline=float("nan"),
                uplift=float("nan"),
                uplift_pct=None,
                ess=0.0,
                ess_fraction=0.0,
                n_valid=0,
                n_total=dataset.n_total,
                n_fail_open=dataset.n_fail_open_filtered,
                warnings=["Dataset vazio apos filtragem. Estimativa invalida."],
            )

        p_log = np.array([r.propensity for r in records], dtype=np.float64)
        p_tgt = np.array(target_propensities, dtype=np.float64)
        rewards = np.array([r.reward for r in records], dtype=np.float64)

        if np.any(p_tgt < 0) or np.any(p_tgt > 1.0 + 1e-9):
            n_bad = int(np.sum((p_tgt < 0) | (p_tgt > 1.0 + 1e-9)))
            warn_list.append(
                f"{n_bad} target_propensities fora de [0,1]. Clipando para [0,1]."
            )
            p_tgt = np.clip(p_tgt, 0.0, 1.0)

        weights = _compute_weights(p_log, p_tgt)  # no-float-ok — importance weights

        ess = _kish_ess(weights)
        ess_frac = ess / n

        if ess_frac < self._LOW_ESS_FRACTION_THRESHOLD:
            warn_list.append(
                f"ESS baixo: {ess:.1f} ({ess_frac:.1%}). Alta variancia no SNIPS."
            )

        # SNIPS: normaliza pelos pesos em vez de 1/N
        w_sum = float(np.sum(weights))  # no-float-ok — OPE normalization sum
        if w_sum == 0:
            # Politica nova nunca escolheria nenhuma das acoes logadas.
            return OPEReport(
                estimator="SNIPS",
                estimate=0.0,
                baseline=float(np.mean(rewards)),
                uplift=0.0 - float(np.mean(rewards)),
                uplift_pct=None,
                ess=0.0,
                ess_fraction=0.0,
                n_valid=n,
                n_total=dataset.n_total,
                n_fail_open=dataset.n_fail_open_filtered,
                warnings=warn_list + ["Soma dos pesos = 0. Politica nova nunca escolhe as acoes logadas."],
            )

        # no-float-ok — OPE signal: SNIPS estimate, not money
        snips_estimate = float(np.sum(weights * rewards)) / w_sum
        baseline = float(np.mean(rewards))
        uplift = snips_estimate - baseline
        uplift_pct = (uplift / baseline * 100.0) if baseline != 0 else None

        return OPEReport(
            estimator="SNIPS",
            estimate=snips_estimate,
            baseline=baseline,
            uplift=uplift,
            uplift_pct=uplift_pct,
            ess=ess,
            ess_fraction=ess_frac,
            n_valid=n,
            n_total=dataset.n_total,
            n_fail_open=dataset.n_fail_open_filtered,
            warnings=warn_list,
        )


# ---------------------------------------------------------------------------
# DR (Doubly-Robust)
# ---------------------------------------------------------------------------

class DREstimator:
    """
    Estimador DR (Doubly-Robust).

    V_DR(pi) = V_DM + (1/N) sum_i [ w_i * (r_i - r_hat_i) ]

    onde:
      V_DM   = (1/N) sum_i r_hat_i            (estimativa direta do modelo)
      w_i    = pi(a_i|x_i) / p(a_i|x_i)      (pesos de importancia)
      r_hat  = predicao do modelo de recompensa

    O DR e robusto: se o modelo de recompensa (r_hat) for bom, a correcao IPS
    e pequena e a variancia e baixa. Se o modelo for ruim mas a propensidade
    for correta, o IPS corrige o vies.

    MODELO DE RECOMPENSA (reward_model):
      Se r_hat estiver disponivel nos registros (reward_model campo do OPERecord),
      usa diretamente. Caso contrario, treina uma regressao logistica simples
      sobre os registros para prever reward dado o contexto (propensidade, tier).

      NOTA: No J3, o modelo de recompensa e simplificado (sem features ricas).
      O J4 pode usar o pCTR calibrado do sidecar como reward_model, o que e
      o uso ideal do DR em producao.
    """

    _LOW_ESS_FRACTION_THRESHOLD = 0.1

    def estimate(
        self,
        dataset: OPEDataset,
        target_propensities: Sequence[float],
        reward_model_predictions: Optional[Sequence[float]] = None,
    ) -> OPEReport:
        """
        Estima V(pi) usando DR.

        Args:
          dataset:                   OPEDataset JA filtrado.
          target_propensities:       pi(a_i|x_i) para cada registro.
          reward_model_predictions:  r_hat_i para cada registro (None = modelo interno).

        Returns:
          OPEReport com estimativa DR e campos dm_estimate / ips_correction.
        """
        records = dataset.records
        n = len(records)
        warn_list: List[str] = []

        if len(target_propensities) != n:
            raise ValueError(
                f"target_propensities tem {len(target_propensities)} elementos, "
                f"mas dataset.records tem {n}."
            )

        if n == 0:
            return OPEReport(
                estimator="DR",
                estimate=float("nan"),
                baseline=float("nan"),
                uplift=float("nan"),
                uplift_pct=None,
                ess=0.0,
                ess_fraction=0.0,
                n_valid=0,
                n_total=dataset.n_total,
                n_fail_open=dataset.n_fail_open_filtered,
                warnings=["Dataset vazio apos filtragem. Estimativa invalida."],
            )

        p_log = np.array([r.propensity for r in records], dtype=np.float64)
        p_tgt = np.array(target_propensities, dtype=np.float64)
        rewards = np.array([r.reward for r in records], dtype=np.float64)

        if np.any(p_tgt < 0) or np.any(p_tgt > 1.0 + 1e-9):
            n_bad = int(np.sum((p_tgt < 0) | (p_tgt > 1.0 + 1e-9)))
            warn_list.append(
                f"{n_bad} target_propensities fora de [0,1]. Clipando para [0,1]."
            )
            p_tgt = np.clip(p_tgt, 0.0, 1.0)

        # Modelo de recompensa r_hat
        if reward_model_predictions is not None:
            if len(reward_model_predictions) != n:
                raise ValueError(
                    f"reward_model_predictions tem {len(reward_model_predictions)} elementos, "
                    f"mas dataset.records tem {n}."
                )
            r_hat = np.array(reward_model_predictions, dtype=np.float64)
        else:
            # Usa reward_model do campo OPERecord se disponivel
            r_hat_from_records = [r.reward_model for r in records]
            if all(v is not None for v in r_hat_from_records):
                r_hat = np.array(r_hat_from_records, dtype=np.float64)
            else:
                # Modelo interno simplificado: regressao logistica sobre propensidade
                # (modelo fraco, mas cobre o caso de teste sem infra de predicao externa).
                # Em producao: usar pCTR calibrado do sidecar como r_hat.
                r_hat = self._fit_reward_model(p_log, rewards)
                warn_list.append(
                    "Usando modelo de recompensa interno simplificado (regressao logistica "
                    "sobre propensidade de logging). Para DR otimo em producao, fornecer "
                    "reward_model_predictions com pCTR calibrado do sidecar."
                )

        # Pesos de importancia
        weights = _compute_weights(p_log, p_tgt)  # no-float-ok — importance weights

        ess = _kish_ess(weights)
        ess_frac = ess / n

        if ess_frac < self._LOW_ESS_FRACTION_THRESHOLD:
            warn_list.append(
                f"ESS baixo: {ess:.1f} ({ess_frac:.1%}). Alta variancia no componente IPS do DR."
            )

        # Componente DM (modelo direto): media de r_hat
        # no-float-ok — OPE signal: DM estimate, not money
        dm_estimate = float(np.mean(r_hat))

        # Componente IPS do DR: (1/N) sum_i w_i * (r_i - r_hat_i)
        # no-float-ok — OPE signal: DR IPS correction, not money
        ips_correction = float(np.mean(weights * (rewards - r_hat)))

        # DR estimate = DM + IPS_correction
        dr_estimate = dm_estimate + ips_correction  # no-float-ok — OPE signal

        baseline = float(np.mean(rewards))
        uplift = dr_estimate - baseline
        uplift_pct = (uplift / baseline * 100.0) if baseline != 0 else None

        return OPEReport(
            estimator="DR",
            estimate=dr_estimate,
            baseline=baseline,
            uplift=uplift,
            uplift_pct=uplift_pct,
            ess=ess,
            ess_fraction=ess_frac,
            n_valid=n,
            n_total=dataset.n_total,
            n_fail_open=dataset.n_fail_open_filtered,
            warnings=warn_list,
            dm_estimate=dm_estimate,
            ips_correction=ips_correction,
        )

    @staticmethod
    def _fit_reward_model(
        propensities: np.ndarray,
        rewards: np.ndarray,
    ) -> np.ndarray:
        """
        Modelo de recompensa interno simplificado: regressao logistica 1D.
        Prediz reward em funcao da propensidade de logging.

        Este e um modelo fraco (apenas usa propensidade), adequado para testes
        e como fallback. Em producao, o ideal e usar o pCTR calibrado do sidecar.
        """
        from sklearn.linear_model import LogisticRegression

        X = propensities.reshape(-1, 1)
        y = (rewards > 0).astype(np.int32)

        # Se todos os labels sao iguais, retorna a media (modelo trivial).
        if np.all(y == 0):
            return np.zeros(len(rewards), dtype=np.float64)
        if np.all(y == 1):
            return np.ones(len(rewards), dtype=np.float64)

        try:
            clf = LogisticRegression(max_iter=200, solver="lbfgs")
            clf.fit(X, y)
            return clf.predict_proba(X)[:, 1].astype(np.float64)
        except Exception:
            # Fallback: media constante
            return np.full(len(rewards), float(np.mean(rewards)), dtype=np.float64)
