"""
ml/ope — Off-Policy Evaluation estimators (J3, ADR-0003 §G).

Estimadores OPE honestos sobre o logging de propensao instrumentado pelo J0.

Invariantes nao-negociaveis (ADR-0003 §G, linha J3):
  - Decisoes com ml_fail_open=True DEVEM ser filtradas ANTES de qualquer estimador.
    Motivo: fail-open => exploration_policy=DETERMINISTIC + propensity=1.0
    A propensao dessas decisoes NAO reflete exploracao real — e a propensao
    da cascata pura deterministica. Inclui-las viciaria o estimador de uplift.
  - Positividade (overlap): propensity > 0 para toda acao servida.
    Violacoes devem ser reportadas; a estimativa RECUSA quando a positividade
    e violada em proporcao significativa.
  - ESS (Effective Sample Size) reportado para IPS/SNIPS como sinal de
    variancia / credibilidade do estimador.
  - DR (Doubly-Robust) combina modelo de recompensa + correcao IPS;
    e robusto quando um dos dois esta correto.
  - Dinheiro em minor-units int64 onde houver eCPM/reward monetario (TX-2).
    Floats somente para probabilidades, pesos e scores de ML.

Exportacoes publicas:
  IPSEstimator     — Importance-weighted Policy estimator
  SNIPSEstimator   — Self-Normalized IPS (reduz variancia)
  DREstimator      — Doubly-Robust estimator
  OPEDataset       — Container de registros de decisao + recompensas
  OPERecord        — Um registro de decisao (propensao, acao, recompensa)
  OPEReport        — Resultado estruturado da avaliacao
  PositivityError  — Excecao quando a positividade e violada
"""

from ml.ope.dataset import OPERecord, OPEDataset
from ml.ope.estimators import (
    IPSEstimator,
    SNIPSEstimator,
    DREstimator,
    OPEReport,
    PositivityError,
)

__all__ = [
    "OPERecord",
    "OPEDataset",
    "IPSEstimator",
    "SNIPSEstimator",
    "DREstimator",
    "OPEReport",
    "PositivityError",
]
