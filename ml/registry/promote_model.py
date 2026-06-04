"""
ml/registry/promote_model.py
============================
Promocao gated de modelo no MLflow registry (J4, ADR-0003 §G).

POLITICA DE PROMOCAO (invariante arquitetural J4):
  A promocao de Staging → Production REQUER prova de uplift A/B anexada.
  Prova de uplift ausente ou invalida: promocao RECUSADA pelo codigo.
  Isso nao e apenas convencao — o codigo lanca UpliftProofRequired se a
  prova nao estiver presente e valida.

  Requisitos da prova de uplift:
    1. estimator: "IPS", "SNIPS" ou "DR".
    2. uplift_pct: numero (positive = melhora; pode ser negativo — rejeita).
    3. n_valid: >= MIN_VALID_DECISIONS (default: 1000).
    4. ess_fraction: >= MIN_ESS_FRACTION (default: 0.05).
    5. model_version: deve bater com a versao que esta sendo promovida.
    6. window_start / window_end: datas do experimento (ISO 8601).

  A promocao registra a prova como artefato no run do modelo e adiciona tags
  de auditoria na versao do modelo.

NOTA SOBRE PROMOCAO REAL (ADR-0003 §G, J4):
  Este modulo entrega o MECANISMO gated. Promocao real aguarda trafego A/B
  real — nao finja um numero de uplift. Dados sinteticos podem ser usados
  em testes, mas promocao de producao requer dados reais de OPE do J3/J4.

NOTA SOBRE DINHEIRO (TX-2):
  Se uplift_pct vier de recompensa em minor-units (eCPM), o calculo
  intermediario IPS/SNIPS/DR usa float (ver ml/ope/estimators.py).
  Este modulo nao recalcula uplift — apenas valida a prova fornecida.

EXEMPLO DE USO (CLI):
  ml/.venv/bin/python -m ml.registry.promote_model \\
    --model-name pctr_lgb \\
    --model-version 3 \\
    --target-stage Production \\
    --uplift-proof '{"estimator":"SNIPS","uplift_pct":2.3,"n_valid":5000,
                     "ess_fraction":0.12,"model_version":"3",
                     "window_start":"2026-06-01","window_end":"2026-06-04"}'
"""
from __future__ import annotations

import json
import os
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

os.environ.setdefault("MLFLOW_ALLOW_FILE_STORE", "true")

import mlflow
from mlflow import MlflowClient

_REPO_ROOT = Path(__file__).parent.parent.parent
_ML_ROOT = Path(__file__).parent.parent
sys.path.insert(0, str(_REPO_ROOT))


# ---------------------------------------------------------------------------
# Constants — thresholds for uplift proof validation
# ---------------------------------------------------------------------------

# Minimum number of valid OPE decisions required for a trustworthy uplift estimate.
# Below this, the sample is too small to be statistically meaningful.
MIN_VALID_DECISIONS: int = 1_000

# Minimum ESS fraction (effective sample size / n_valid).
# Below this, the IPS/SNIPS weights are too heterogeneous — high variance.
MIN_ESS_FRACTION: float = 0.05  # 5% of n_valid

# Valid uplift estimator names (from ml/ope/estimators.py).
VALID_ESTIMATORS = {"IPS", "SNIPS", "DR"}

# Valid target stages for promotion.
VALID_STAGES = {"Staging", "Production"}


# ---------------------------------------------------------------------------
# Exception hierarchy
# ---------------------------------------------------------------------------

class UpliftProofRequired(ValueError):
    """
    Levantada quando a prova de uplift A/B e ausente ou invalida.

    Esta e a guarda de promocao central de J4: nao ha override via flag,
    nao ha modo de compatibilidade. Se a prova nao estiver presente e valida,
    a promocao e recusada.
    """


class UpliftProofInvalid(UpliftProofRequired):
    """
    Especializacao para prova presente mas com campos invalidos.
    """


class NegativeUplift(UpliftProofRequired):
    """
    Levantada quando a prova mostra uplift negativo (modelo pior que baseline).
    A promocao de um modelo pior que o baseline e recusada automaticamente.
    """


# ---------------------------------------------------------------------------
# UpliftProof dataclass
# ---------------------------------------------------------------------------

@dataclass
class UpliftProof:
    """
    Prova de uplift A/B/OPE para gating de promocao.

    Campos obrigatorios:
      estimator     — "IPS", "SNIPS" ou "DR"
      uplift_pct    — uplift percentual (positivo = melhora sobre baseline)
      n_valid       — numero de decisoes validas usadas na estimativa
      ess_fraction  — fracao do dataset com ESS efetivo (>= MIN_ESS_FRACTION)
      model_version — versao do modelo avaliado (deve bater com o registry)
      window_start  — inicio do experimento A/B (ISO 8601, ex: "2026-06-01")
      window_end    — fim do experimento A/B (ISO 8601, ex: "2026-06-04")

    Campo opcional:
      notes         — descricao livre do experimento
    """
    estimator: str
    uplift_pct: float
    n_valid: int
    ess_fraction: float
    model_version: str
    window_start: str
    window_end: str
    notes: str = ""

    @classmethod
    def from_dict(cls, d: dict) -> "UpliftProof":
        """Constroi um UpliftProof a partir de um dicionario (ex.: JSON parseado)."""
        required = ["estimator", "uplift_pct", "n_valid", "ess_fraction",
                    "model_version", "window_start", "window_end"]
        missing = [k for k in required if k not in d]
        if missing:
            raise UpliftProofInvalid(
                f"Prova de uplift incompleta. Campos ausentes: {missing}. "
                f"Promocao recusada. Veja a documentacao de UpliftProof."
            )
        try:
            return cls(
                estimator=str(d["estimator"]),
                uplift_pct=float(d["uplift_pct"]),
                n_valid=int(d["n_valid"]),
                ess_fraction=float(d["ess_fraction"]),
                model_version=str(d["model_version"]),
                window_start=str(d["window_start"]),
                window_end=str(d["window_end"]),
                notes=str(d.get("notes", "")),
            )
        except (TypeError, ValueError) as e:
            raise UpliftProofInvalid(
                f"Tipo de campo invalido na prova de uplift: {e}. "
                f"Promocao recusada."
            ) from e

    @classmethod
    def from_json_str(cls, s: str) -> "UpliftProof":
        """Constroi a partir de uma string JSON."""
        try:
            d = json.loads(s)
        except json.JSONDecodeError as e:
            raise UpliftProofInvalid(
                f"JSON invalido na prova de uplift: {e}. Promocao recusada."
            ) from e
        return cls.from_dict(d)

    def to_dict(self) -> dict:
        return {
            "estimator": self.estimator,
            "uplift_pct": self.uplift_pct,
            "n_valid": self.n_valid,
            "ess_fraction": self.ess_fraction,
            "model_version": self.model_version,
            "window_start": self.window_start,
            "window_end": self.window_end,
            "notes": self.notes,
        }


# ---------------------------------------------------------------------------
# Validacao da prova de uplift
# ---------------------------------------------------------------------------

def validate_uplift_proof(
    proof: UpliftProof,
    target_model_version: str,
    min_valid: int = MIN_VALID_DECISIONS,
    min_ess_fraction: float = MIN_ESS_FRACTION,
) -> None:
    """
    Valida a prova de uplift para promocao gated.

    Levanta:
      UpliftProofInvalid — se a prova for invalida (campos fora de range,
                           estimador desconhecido, etc.).
      NegativeUplift     — se uplift_pct <= 0 (modelo nao melhora baseline).
      UpliftProofRequired — genericamente se a prova for nula.

    Invariante: NUNCA retorna sem levantar se a prova for invalida.
    O chamador (promote_model_version) depende disso.
    """
    if proof is None:
        raise UpliftProofRequired(
            "Prova de uplift ausente. Promocao requer prova de uplift A/B. "
            "Faca um experimento A/B com OPE (ml/ope/) e forneca a prova. "
            "Promocao sem prova recusada por design (ADR-0003 §G, J4)."
        )

    # 1. Estimador valido.
    if proof.estimator not in VALID_ESTIMATORS:
        raise UpliftProofInvalid(
            f"Estimador '{proof.estimator}' invalido. "
            f"Estimadores validos: {VALID_ESTIMATORS}. "
            f"Use ml/ope/estimators.py para gerar a prova."
        )

    # 2. Uplift positivo (modelo melhora sobre baseline).
    if proof.uplift_pct <= 0:
        raise NegativeUplift(
            f"Uplift negativo ou zero: uplift_pct={proof.uplift_pct:.4f}%. "
            f"Modelo candidato nao melhora o baseline sob o estimador {proof.estimator}. "
            f"Promocao recusada (modelo pior que baseline nao deve ir para producao)."
        )

    # 3. Tamanho de amostra suficiente.
    if proof.n_valid < min_valid:
        raise UpliftProofInvalid(
            f"Amostra insuficiente: n_valid={proof.n_valid} < {min_valid} (minimo). "
            f"Colete mais dados de OPE antes de promover."
        )

    # 4. ESS suficiente.
    if proof.ess_fraction < min_ess_fraction:
        raise UpliftProofInvalid(
            f"ESS insuficiente: ess_fraction={proof.ess_fraction:.4f} < {min_ess_fraction:.4f}. "
            f"Variancia do estimador muito alta. Aumente a exploracao ou colete mais dados."
        )

    # 5. Versao do modelo na prova bate com a versao sendo promovida.
    if proof.model_version != target_model_version:
        raise UpliftProofInvalid(
            f"Versao do modelo na prova ({proof.model_version!r}) "
            f"nao bate com a versao sendo promovida ({target_model_version!r}). "
            f"A prova deve ser gerada para o modelo especifico sendo promovido."
        )

    # 6. Janela temporal presente.
    if not proof.window_start or not proof.window_end:
        raise UpliftProofInvalid(
            f"Janela temporal ausente (window_start={proof.window_start!r}, "
            f"window_end={proof.window_end!r}). "
            f"Informe as datas do experimento A/B."
        )


# ---------------------------------------------------------------------------
# Funcao de promocao gated
# ---------------------------------------------------------------------------

def promote_model_version(
    model_name: str,
    model_version: str,
    target_stage: str,
    uplift_proof: UpliftProof,
    mlflow_tracking_uri: Optional[str] = None,
    min_valid: int = MIN_VALID_DECISIONS,
    min_ess_fraction: float = MIN_ESS_FRACTION,
) -> str:
    """
    Promove model_version para target_stage no MLflow registry.

    GATED: lanca UpliftProofRequired (ou subclasse) se a prova for invalida.
    Apenas retorna (com sucesso) se a promocao foi feita.

    Args:
      model_name:           nome do modelo no registry (ex.: "pctr_lgb").
      model_version:        versao a promover (string, ex.: "3").
      target_stage:         "Staging" ou "Production".
      uplift_proof:         prova de uplift A/B (nunca None — levanta se None).
      mlflow_tracking_uri:  URI do tracking server (None = file-store local).
      min_valid:            minimo de decisoes validas (gate de amostra).
      min_ess_fraction:     minimo de ESS fraction (gate de variancia).

    Returns:
      model_version (string) — a versao promovida.

    Levanta:
      UpliftProofRequired — se a prova for ausente ou invalida (qualquer razao).
      ValueError          — se target_stage invalido.
    """
    if target_stage not in VALID_STAGES:
        raise ValueError(
            f"Stage invalido: {target_stage!r}. Validos: {VALID_STAGES}."
        )

    # GATE: validacao da prova antes de qualquer chamada ao registry.
    # Nao ha modo de pular esta validacao via flag ou parametro.
    validate_uplift_proof(
        proof=uplift_proof,
        target_model_version=model_version,
        min_valid=min_valid,
        min_ess_fraction=min_ess_fraction,
    )

    # A partir daqui: prova valida. Prosseguir com a promocao.
    if mlflow_tracking_uri:
        mlflow.set_tracking_uri(mlflow_tracking_uri)

    client = MlflowClient()

    # Verifica que a versao existe.
    mv = client.get_model_version(model_name, model_version)
    if mv is None:
        raise ValueError(
            f"Versao {model_version!r} do modelo {model_name!r} nao encontrada no registry."
        )

    # Serializa a prova como artefato para auditabilidade.
    proof_path = _ML_ROOT / "registry" / "artifacts" / f"uplift_proof_v{model_version}.json"
    proof_path.parent.mkdir(parents=True, exist_ok=True)
    with open(proof_path, "w") as f:
        json.dump(proof.to_dict(), f, indent=2)

    # Registra o artefato no run do modelo.
    try:
        client.log_artifact(mv.run_id, str(proof_path), artifact_path="promotion_proofs")
    except Exception as e:
        # Falha no log do artefato nao bloqueia a promocao — apenas logamos o aviso.
        print(f"[promote] AVISO: nao foi possivel logar artefato no run: {e}")

    # Tags de auditoria na versao do modelo.
    client.set_model_version_tag(model_name, model_version, "promotion_stage", target_stage)
    client.set_model_version_tag(model_name, model_version, "promotion_gate", "passed_J4_AB_test")
    client.set_model_version_tag(model_name, model_version, "uplift_estimator", proof.estimator)
    client.set_model_version_tag(model_name, model_version, "uplift_pct",
                                 f"{proof.uplift_pct:.4f}")
    client.set_model_version_tag(model_name, model_version, "uplift_n_valid", str(proof.n_valid))
    client.set_model_version_tag(model_name, model_version, "uplift_window",
                                 f"{proof.window_start}_{proof.window_end}")

    # Promocao de stage.
    client.transition_model_version_stage(
        name=model_name,
        version=model_version,
        stage=target_stage,
        archive_existing_versions=True,
    )

    print(f"[promote] Versao {model_version} do modelo {model_name!r} promovida para {target_stage}.")
    print(f"[promote] Prova: {proof.estimator} uplift={proof.uplift_pct:.4f}% "
          f"n_valid={proof.n_valid} ess_fraction={proof.ess_fraction:.4f}")
    print(f"[promote] Janela: {proof.window_start} → {proof.window_end}")
    if proof.notes:
        print(f"[promote] Notas: {proof.notes}")

    return model_version


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(
        description=(
            "Promocao gated de modelo pCTR no MLflow registry (J4). "
            "Requer prova de uplift A/B. Recusa sem prova valida."
        )
    )
    parser.add_argument("--model-name", default="pctr_lgb", help="Nome do modelo no registry.")
    parser.add_argument("--model-version", required=True, help="Versao a promover (ex.: '3').")
    parser.add_argument(
        "--target-stage",
        default="Production",
        choices=["Staging", "Production"],
        help="Stage de destino.",
    )
    parser.add_argument(
        "--uplift-proof",
        required=True,
        help=(
            "JSON com a prova de uplift A/B. Campos obrigatorios: "
            "estimator, uplift_pct, n_valid, ess_fraction, model_version, "
            "window_start, window_end. "
            "Exemplo: '{\"estimator\":\"SNIPS\",\"uplift_pct\":2.3,"
            "\"n_valid\":5000,\"ess_fraction\":0.12,"
            "\"model_version\":\"3\","
            "\"window_start\":\"2026-06-01\",\"window_end\":\"2026-06-04\"}'"
        ),
    )
    parser.add_argument(
        "--mlflow-tracking-uri",
        default="file:" + str(_ML_ROOT / "registry" / "mlruns"),
        help="URI do MLflow tracking server.",
    )
    parser.add_argument(
        "--min-valid",
        type=int,
        default=MIN_VALID_DECISIONS,
        help=f"Minimo de decisoes validas (default: {MIN_VALID_DECISIONS}).",
    )
    parser.add_argument(
        "--min-ess-fraction",
        type=float,
        default=MIN_ESS_FRACTION,
        help=f"Minimo de ESS fraction (default: {MIN_ESS_FRACTION}).",
    )
    args = parser.parse_args()

    try:
        proof = UpliftProof.from_json_str(args.uplift_proof)
    except UpliftProofRequired as e:
        print(f"[promote] RECUSADO: {e}", file=sys.stderr)
        sys.exit(2)

    try:
        promote_model_version(
            model_name=args.model_name,
            model_version=args.model_version,
            target_stage=args.target_stage,
            uplift_proof=proof,
            mlflow_tracking_uri=args.mlflow_tracking_uri,
            min_valid=args.min_valid,
            min_ess_fraction=args.min_ess_fraction,
        )
    except UpliftProofRequired as e:
        print(f"[promote] RECUSADO: {e}", file=sys.stderr)
        sys.exit(2)
    except Exception as e:
        print(f"[promote] ERRO: {e}", file=sys.stderr)
        sys.exit(1)

    print("[done] Promocao concluida.")
    print(
        "LEMBRETE: O sidecar deve ser reiniciado para carregar a nova versao. "
        "RANKER_MODEL_VERSION deve refletir a versao promovida."
    )
