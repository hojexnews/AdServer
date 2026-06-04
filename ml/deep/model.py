"""
ml/deep/model.py
================
Arquitetura two-tower com cross-network DCN-v2 (Deep & Cross Network v2)
para o deep ranker (K1, Fase 3 — ADR-0004 §A).

PRINCIPIOS ARQUITETURAIS (ADR-0004 §A / ADR-0003 §A):
  - O deep ranker e um RE-RANKER DENTRO DO ESTRATO (DA-3): substitui o GBDT
    atras do MESMO ponto de extensao internal/ranker / ranker-sidecar.
    Nao e um novo ponto de extensao, nao seleciona estrato, nao altera a cascata.
  - Budget TX-4: 5-8 ms p99. INT8 + torre de demanda pre-computada offline
    sao os mecanismos para caber no budget no serving. Se nao couber, fail-open.
  - Anti-skew: o vetor de entrada tem EXATAMENTE 23 features da spec 1.0.0
    (ml/features/spec/feature_spec.yaml). A funcao de featurizacao e
    ml/features/python/featurize.py — READ-ONLY, nunca reimplementar aqui.
  - pCTR como saida (igual ao GBDT): output em [0,1], float32.
    pCVR fora de escopo ate atribuicao confiavel fechada (ADR-0003).
  - Nenhum valor monetario computado aqui (TX-2). eCPM = pCTR x bid e feito
    em internal/ranker/score.go (Go) com minor-units int64.

ARQUITETURA:
  Two-tower:
    - Torre de contexto (zona/request): features 0-9 do vetor de serving.
    - Torre de candidato (campanha/creative): features 10-22.
    As torres produzem embeddings que sao concatenados e passados pelo
    cross-network DCN-v2, seguido por uma MLP de output.

  Torre de demanda (candidato) e PRE-COMPUTADA offline (ADR-0004 §A):
    O embedding do candidato pode ser pre-computado e indexado por
    campaign_id/banner_id, carregado pelo sidecar no startup sem GPU.
    No serving, apenas a torre de contexto roda online (latencia minima).
    Esta pre-computacao e responsabilidade do job de treino (train_deep.py).

  DCN-v2 (cross-network paralelo):
    Captura interacoes cruzadas de alta ordem entre features contexto e candidato
    sem explosao parametrica (cross layers parametrizadas por matriz W).

  DLRM-style interaction:
    Dot-product entre embedding de contexto e embedding de candidato como
    sinal complementar de compatibilidade (opcional, controlado por use_dlrm_dot).

GATE K1 → K8:
  O modelo e CONSTRUIVEL aqui (scaffolding + treino sobre sample sintetico).
  Promocao para producao aguarda K8 (prova de uplift A/B sobre GBDT com
  trafego real — cutover Fase 2 bloqueado por infra). Ver ADR-0004 §H.

DADOS REAIS:
  O treino real requer dados do lakehouse Iceberg (pendencia de infra).
  O scaffolding aqui usa sample sintetico (generate_deep_sample.py).
  Dados reais nao alteram a arquitetura do modelo — apenas o resultado do treino.
"""
from __future__ import annotations

from typing import Optional, Tuple

import torch
import torch.nn as nn

# ---------------------------------------------------------------------------
# Constantes que espelham ml/features/spec/feature_spec.yaml v1.0.0
# ---------------------------------------------------------------------------

FEATURE_SPEC_VERSION = "1.0.0"
FEATURE_VECTOR_LENGTH = 23  # indices 0..22 (online features)

# Indices de features por torre (conforme spec)
_CONTEXT_FEATURE_IDXS = list(range(0, 10))   # zona (0-2) + temporal (3-5) + geo (6-7) + device (8-9)
_CANDIDATE_FEATURE_IDXS = list(range(10, 23)) # tier/prio (10-11) + pacing (12-13) + ecpm (14-15) +
                                               # creative (16-19) + competition (20) + history (21-22)

_CONTEXT_DIM = len(_CONTEXT_FEATURE_IDXS)    # 10
_CANDIDATE_DIM = len(_CANDIDATE_FEATURE_IDXS) # 13


# ---------------------------------------------------------------------------
# Bloco DCN-v2 Cross Layer
# ---------------------------------------------------------------------------

class CrossLayerV2(nn.Module):
    """
    Uma camada do cross-network DCN-v2 (Wang et al. 2021).

    Implementa a formula: x_l+1 = x_0 * (W_l x_l + b_l) + x_l
    onde W_l e uma matriz de baixo rank (rank << dim) para eficiencia parametrica.

    Referencia: https://arxiv.org/abs/2008.13535
    """

    def __init__(self, input_dim: int, low_rank: int = 16) -> None:
        super().__init__()
        self.W_low = nn.Linear(input_dim, low_rank, bias=False)
        self.W_high = nn.Linear(low_rank, input_dim, bias=False)
        self.bias = nn.Parameter(torch.zeros(input_dim))

    def forward(self, x0: torch.Tensor, xl: torch.Tensor) -> torch.Tensor:
        # cross: x0 * (W xl) + xl
        cross = x0 * (self.W_high(self.W_low(xl)) + self.bias)
        return cross + xl


# ---------------------------------------------------------------------------
# Torre (sub-rede de embedding)
# ---------------------------------------------------------------------------

class Tower(nn.Module):
    """
    Torre de projecao: [input_dim] -> [hidden] -> [embedding_dim].

    Usada para a torre de contexto (zona+temporal+geo+device) e
    para a torre de candidato (campanha+creative). Camadas simples
    com BatchNorm1d e ReLU. Dropout leve para regularizacao.
    """

    def __init__(
        self,
        input_dim: int,
        hidden_dim: int,
        embedding_dim: int,
        dropout: float = 0.1,
    ) -> None:
        super().__init__()
        self.net = nn.Sequential(
            nn.Linear(input_dim, hidden_dim),
            nn.BatchNorm1d(hidden_dim),
            nn.ReLU(),
            nn.Dropout(dropout),
            nn.Linear(hidden_dim, embedding_dim),
            nn.ReLU(),
        )

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        return self.net(x)


# ---------------------------------------------------------------------------
# Modelo principal: TwoTowerDCNv2
# ---------------------------------------------------------------------------

class TwoTowerDCNv2(nn.Module):
    """
    Two-tower com cross-network DCN-v2 para pCTR.

    Fluxo:
      1. Divide o vetor de 23 features em contexto (10) e candidato (13).
      2. Cada torre projeta em embedding_dim.
      3. Concatena [context_emb | candidate_emb] -> combined (2*embedding_dim).
      4. Passa por num_cross_layers de CrossLayerV2 (interacoes cruzadas).
      5. Concatena saida cross com saida de uma MLP profunda (parallel structure).
      6. Camada de output logistica -> pCTR em [0,1].

    Opcional: DLRM-style dot product (use_dlrm_dot=True) soma
    dot(context_emb, candidate_emb) como feature escalar adicional.

    Args:
        tower_hidden_dim: dimensao oculta das torres de projecao.
        embedding_dim: dimensao dos embeddings de contexto e candidato.
        cross_layers: numero de cross layers DCN-v2.
        deep_hidden_dim: dimensao da MLP profunda paralela.
        deep_layers: numero de camadas da MLP profunda.
        dropout: taxa de dropout (treino).
        use_dlrm_dot: adiciona dot-product DLRM-style como feature extra.
        cross_low_rank: rank das matrizes de cross layers (eficiencia parametrica).
    """

    def __init__(
        self,
        tower_hidden_dim: int = 64,
        embedding_dim: int = 32,
        cross_layers: int = 3,
        deep_hidden_dim: int = 64,
        deep_layers: int = 2,
        dropout: float = 0.1,
        use_dlrm_dot: bool = True,
        cross_low_rank: int = 16,
    ) -> None:
        super().__init__()

        self.feature_spec_version = FEATURE_SPEC_VERSION

        # --- Torres ---
        self.context_tower = Tower(_CONTEXT_DIM, tower_hidden_dim, embedding_dim, dropout)
        self.candidate_tower = Tower(_CANDIDATE_DIM, tower_hidden_dim, embedding_dim, dropout)

        # --- DCN-v2 cross layers (parallel com deep) ---
        combined_dim = 2 * embedding_dim + (1 if use_dlrm_dot else 0)
        self.cross_net = nn.ModuleList([
            CrossLayerV2(combined_dim, cross_low_rank)
            for _ in range(cross_layers)
        ])

        # --- MLP profunda (parallel ao cross) ---
        deep_layers_list: list[nn.Module] = []
        in_dim = combined_dim
        for _ in range(deep_layers):
            deep_layers_list.extend([
                nn.Linear(in_dim, deep_hidden_dim),
                nn.BatchNorm1d(deep_hidden_dim),
                nn.ReLU(),
                nn.Dropout(dropout),
            ])
            in_dim = deep_hidden_dim
        self.deep_net = nn.Sequential(*deep_layers_list)

        # --- Output: concatena cross + deep -> logit ---
        self.output_layer = nn.Linear(combined_dim + deep_hidden_dim, 1)
        self.use_dlrm_dot = use_dlrm_dot

    def forward(self, features: torch.Tensor) -> torch.Tensor:
        """
        Args:
            features: Tensor float32 de shape [N, 23].
                      Deve ser o vetor produzido por ml/features/python/featurize.py
                      (spec 1.0.0 — anti-skew).

        Returns:
            pCTR: Tensor float32 de shape [N, 1], valores em [0, 1].
        """
        if features.shape[-1] != FEATURE_VECTOR_LENGTH:
            raise ValueError(
                f"TwoTowerDCNv2.forward: esperado vetor de {FEATURE_VECTOR_LENGTH} features "
                f"(spec {FEATURE_SPEC_VERSION}), recebido {features.shape[-1]}."
            )

        # 1. Dividir em torres
        ctx = features[:, _CONTEXT_FEATURE_IDXS]       # [N, 10]
        cand = features[:, _CANDIDATE_FEATURE_IDXS]    # [N, 13]

        # 2. Embeddings das torres
        ctx_emb = self.context_tower(ctx)               # [N, embedding_dim]
        cand_emb = self.candidate_tower(cand)           # [N, embedding_dim]

        # 3. Concatenar (com DLRM dot opcional)
        if self.use_dlrm_dot:
            dot = (ctx_emb * cand_emb).sum(dim=-1, keepdim=True)  # [N, 1]
            combined = torch.cat([ctx_emb, cand_emb, dot], dim=-1) # [N, 2*emb+1]
        else:
            combined = torch.cat([ctx_emb, cand_emb], dim=-1)      # [N, 2*emb]

        # 4. Cross network (DCN-v2 paralelo)
        x_cross = combined
        for layer in self.cross_net:
            x_cross = layer(combined, x_cross)                     # [N, combined_dim]

        # 5. Deep network (MLP paralela)
        x_deep = self.deep_net(combined)                           # [N, deep_hidden_dim]

        # 6. Concatenar cross + deep -> logit -> sigmoid
        x_out = torch.cat([x_cross, x_deep], dim=-1)              # [N, combined+deep]
        logit = self.output_layer(x_out).squeeze(-1)               # [N]
        pctr = torch.sigmoid(logit)                                # [N] em [0,1]
        return pctr.unsqueeze(-1)                                  # [N, 1]

    # ------------------------------------------------------------------
    # Utilidades para serving
    # ------------------------------------------------------------------

    def compute_candidate_embedding(self, candidate_features: torch.Tensor) -> torch.Tensor:
        """
        Pre-computa o embedding da torre de candidato para armazenamento offline.

        Chamado pelo job de export (export_deep.py) para gerar a tabela de
        embeddings de demanda que o sidecar carrega no startup (ADR-0004 §A:
        "torre de demanda pre-computada offline").

        Args:
            candidate_features: Tensor [N, 13] com as features de candidato.

        Returns:
            Tensor [N, embedding_dim].
        """
        with torch.no_grad():
            return self.candidate_tower(candidate_features)

    def compute_context_embedding(self, context_features: torch.Tensor) -> torch.Tensor:
        """
        Computa o embedding da torre de contexto em tempo de serving (online).

        Esta e a unica parte do modelo que roda no hot path quando os embeddings
        de candidato estao pre-computados.

        Args:
            context_features: Tensor [N, 10] com as features de contexto.

        Returns:
            Tensor [N, embedding_dim].
        """
        with torch.no_grad():
            return self.context_tower(context_features)
