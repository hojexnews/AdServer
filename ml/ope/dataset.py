"""
ml/ope/dataset.py
==================
Estruturas de dados para OPE.

OPERecord     — um registro de decisao (propensao logada, acao, recompensa, flags).
OPEDataset    — colecao de registros com filtragem e validacao.

REGRA CRITICA — filtro de ml_fail_open:
  Decisoes com ml_fail_open=True TEM propensity=1.0 E
  exploration_policy=DETERMINISTIC. Elas nao representam exploracao real:
  o ranker nao chegou a executar (timeout/falha de IPC). Inclui-las no
  estimador IPS/SNIPS/DR e errado porque:
    1. A propensao=1.0 nao e estimada pelo bandit — e o fallback determinisitco.
    2. Incluir essas decisoes fingindo propensao=1.0 infla artificialmente o
       denominador do SNIPS e o numerador do IPS quando a politica avaliada
       concorda com a acao servida.
    3. O uplift estimado seria invalido (undecidable sample bias).
  Este modulo APLICA O FILTRO ANTES DE QUALQUER ESTIMATIVA. Nao ha opcao
  de desabilitar este filtro — e um invariante arquitetural (ADR-0003 §G).

PRIVACIDADE (TX-5/DA-11):
  OPERecord carrega apenas: decision_id (pseudonimo), campaign_id, banner_id,
  zone_id, model_version, propensity, served_tier e reward.
  NENHUM campo de PII, IP, user_id, geo bruto ou fingerprint.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import List, Optional, Sequence


@dataclass
class OPERecord:
    """
    Um registro de decisao para OPE.

    Campos obrigatorios:
      decision_id    — ULID/UUID da decisao (chave de join com rewards).
      propensity     — P(acao_servida | contexto) sob a politica de logging.
                       Deve estar em (0, 1]. Propensity=0 e uma violacao de
                       positividade e indica dado corrompido.
      reward         — recompensa observada: 1 se houve clique atribuido dentro
                       da janela last-click 7d, 0 caso contrario.
                       Valores monetarios (eCPM) em minor-units int64 podem
                       ser usados como reward DESDE QUE comparados dentro da
                       mesma moeda/tenant (DA-10) e NUNCA convertidos para float
                       fora deste modulo (TX-2).

    Campos de contexto (para DR e filtragem):
      campaign_id    — campanha servida.
      banner_id      — banner/criativo servido.
      zone_id        — zona de publicacao.
      served_tier    — OVERRIDE/CONTRACT/REMNANT/BLANK.
      model_version  — versao do ranker que gerou a decisao.

    Flags de filtragem:
      ml_fail_open   — True se a decisao degradou para cascata deterministica
                       por timeout/falha do ML. DECISOES COM ml_fail_open=True
                       SAO EXCLUIDAS AUTOMATICAMENTE do OPE (ver OPEDataset).

    Campo para DR:
      reward_model   — recompensa predita por um modelo de recompensa separado
                       (para DR). Pode ser None quando DR usa regressao interna.
    """
    decision_id: str
    propensity: float       # P(a|x) sob a politica de logging; em (0, 1]
    reward: float           # recompensa observada; 0 ou 1 para CTR; float para eCPM

    # Campos de contexto (opcionais para compatibilidade com dados parciais)
    campaign_id: str = ""
    banner_id: str = ""
    zone_id: str = ""
    served_tier: str = ""
    model_version: str = ""

    # Flag de fail-open: decisoes com ml_fail_open=True sao excluidas do OPE
    ml_fail_open: bool = False

    # Para DR: predicao do modelo de recompensa (None = calcular internamente)
    reward_model: Optional[float] = None

    def __post_init__(self) -> None:
        # Validacoes de tipo basicas para detectar bugs de integracao cedo.
        if not isinstance(self.decision_id, str) or not self.decision_id:
            raise ValueError(f"OPERecord.decision_id deve ser string nao-vazia, got: {self.decision_id!r}")
        if not isinstance(self.propensity, (int, float)):
            raise TypeError(f"OPERecord.propensity deve ser float, got: {type(self.propensity)}")
        if not isinstance(self.reward, (int, float)):
            raise TypeError(f"OPERecord.reward deve ser float/int, got: {type(self.reward)}")


@dataclass
class OPEDataset:
    """
    Colecao de OPERecords com pre-processamento, filtragem e validacao.

    Uso padrao:
      ds = OPEDataset.from_records(records)
      # records FAIL-OPEN ja foram filtrados automaticamente
      print(f"N={ds.n_total} original, N={ds.n_valid} validos para OPE")

    FILTRAGEM AUTOMATICA:
      1. ml_fail_open=True  — removidos (ver OPERecord)
      2. propensity <= 0    — removidos (violacao de positividade)
      3. propensity > 1     — removidos (invalido)

    INVARIANTE DE POSITIVIDADE:
      Depois do filtro, todos os registros restantes tem propensity em (0, 1].
      Se a fracao filtrada for > positivity_violation_threshold (padrao 5%),
      um aviso e emitido. Se for > positivity_rejection_threshold (padrao 50%),
      OPEDataset levanta PositivityViolationWarning.
    """
    records: List[OPERecord] = field(default_factory=list)

    # Contadores de filtragem (preenchidos por from_records)
    n_total: int = 0
    n_fail_open_filtered: int = 0
    n_positivity_filtered: int = 0
    n_valid: int = 0

    @classmethod
    def from_records(
        cls,
        records: Sequence[OPERecord],
        positivity_warn_threshold: float = 0.05,
        positivity_reject_threshold: float = 0.50,
    ) -> "OPEDataset":
        """
        Constroi um OPEDataset a partir de uma sequencia de OPERecords.

        Aplica os filtros obrigatorios:
          1. Remove decisoes com ml_fail_open=True.
          2. Remove decisoes com propensity <= 0 ou > 1 (violacao de positividade).

        Emite aviso se a fracao filtrada superar positivity_warn_threshold.
        Levanta PositivityViolationError se superar positivity_reject_threshold.

        Args:
          records: sequencia de OPERecord (pode incluir fail-open e invalidos).
          positivity_warn_threshold: fracao de registros filtrados que gera aviso.
          positivity_reject_threshold: fracao que gera PositivityViolationError.

        Returns:
          OPEDataset com apenas registros validos para estimativa.
        """
        import warnings

        n_total = len(records)
        n_fail_open = 0
        n_positivity = 0
        valid: List[OPERecord] = []

        for r in records:
            # FILTRO 1 (invariante arquitetural): ml_fail_open
            # Decisoes que degradaram para cascata deterministica nao representam
            # exploracao real. propensity=1.0 nestas decisoes e o valor do
            # fallback, nao estimado pelo bandit. Nunca incluir no OPE.
            if r.ml_fail_open:
                n_fail_open += 1
                continue

            # FILTRO 2: positividade — propensity deve estar em (0, 1]
            # propensity=0 significa que a acao nunca seria tomada sob a politica,
            # o que e uma contradicao para algo que foi servido. E dado corrompido.
            # propensity > 1 e impossivel para uma probabilidade. Filtrar ambos.
            if not (0 < r.propensity <= 1.0):
                n_positivity += 1
                continue

            valid.append(r)

        n_valid = len(valid)
        n_filtered_total = n_fail_open + n_positivity

        # Aviso/rejeicao por violacao de positividade (nao conta fail-open,
        # que e comportamento esperado e correto quando o sidecar falha).
        if n_total > 0 and n_positivity > 0:
            frac_positivity_violated = n_positivity / n_total
            if frac_positivity_violated >= positivity_reject_threshold:
                from ml.ope.estimators import PositivityError
                raise PositivityError(
                    f"Positividade violada em {frac_positivity_violated:.1%} dos registros "
                    f"({n_positivity}/{n_total}). Limiar de rejeicao: {positivity_reject_threshold:.0%}. "
                    f"Verifique a instrumentacao de propensao (propensity-logging.md §4)."
                )
            if frac_positivity_violated >= positivity_warn_threshold:
                warnings.warn(
                    f"OPE: {frac_positivity_violated:.1%} dos registros ({n_positivity}/{n_total}) "
                    f"tem propensity invalido (0 ou >1). Esses registros foram filtrados. "
                    f"Se a fracao crescer, verifique o logging de propensao.",
                    UserWarning,
                    stacklevel=2,
                )

        ds = cls(
            records=valid,
            n_total=n_total,
            n_fail_open_filtered=n_fail_open,
            n_positivity_filtered=n_positivity,
            n_valid=n_valid,
        )
        return ds

    def summary(self) -> dict:
        """Retorna um dicionario de estatisticas do dataset."""
        import numpy as np
        if not self.records:
            return {
                "n_total": self.n_total,
                "n_fail_open_filtered": self.n_fail_open_filtered,
                "n_positivity_filtered": self.n_positivity_filtered,
                "n_valid": self.n_valid,
                "frac_fail_open": self.n_fail_open_filtered / max(1, self.n_total),
                "mean_propensity": float("nan"),
                "mean_reward": float("nan"),
                "reward_rate": float("nan"),
            }
        props = np.array([r.propensity for r in self.records], dtype=np.float64)
        rewards = np.array([r.reward for r in self.records], dtype=np.float64)
        return {
            "n_total": self.n_total,
            "n_fail_open_filtered": self.n_fail_open_filtered,
            "n_positivity_filtered": self.n_positivity_filtered,
            "n_valid": self.n_valid,
            "frac_fail_open": self.n_fail_open_filtered / max(1, self.n_total),
            "mean_propensity": float(np.mean(props)),
            "min_propensity": float(np.min(props)),
            "max_propensity": float(np.max(props)),
            "mean_reward": float(np.mean(rewards)),
            "reward_rate": float(np.mean(rewards > 0)),
        }
