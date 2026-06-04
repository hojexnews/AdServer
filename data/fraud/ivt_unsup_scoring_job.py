"""
data/fraud/ivt_unsup_scoring_job.py
=====================================
Extensao do job de scoring IVT para os modelos nao-supervisionados (K2 / Fase 3).

=============================================================================
PAPEL DESTE MODULO (TX-6 / K2 / ADR-0004 §B):
=============================================================================

  Complementa o job supervisionado (ivt_scoring_job.py) com os modelos
  nao-supervisionados (Isolation Forest + Autoencoder) da Fase 3.

  Fluxo estendido:
    Redpanda -> raw_impression (ivt_status = '' default)
                      |
                      v  [ivt_scoring_job.py — scorer supervisionado GBDT]
                IVTScorer.score_event()           -> sup_score
                      |
                      v  [este modulo — scorer nao-supervisionado (K2)]
                IVTUnsupScorer.score_event()      -> unsup_score
                      |
                      v  [combine_ivt_scores() — logica OR]
                is_ivt_final = sup_score.is_ivt OR unsup_score.is_unsup_ivt
                      |
                      v
                raw_ivt_score (ClickHouse) — score supervisionado (007, inalterado)
                raw_ivt_unsup_score (ClickHouse) — score nao-supervisionado (008, NOVO)
                      |
                      v  [MV 007: propaga ivt_status='ivt' para raw_impression]
                      |  [MV 008: propaga ivt_unsup_score para raw_impression]
                      |
                      v  [billing_batch_hourly.py, lê do Iceberg]
                Iceberg events_impression (apenas clean) -> billing

INVARIANTES (nao violar):
  TX-6:  scoring FORA do hot path (latencia de minutos aceitavel); fail-open.
  TX-2/DA-10: nenhuma aritmetica monetaria. if_score, ae_error sao floats
              de ML (nao dinheiro).
  TX-5/DA-11: IVTScoringInput e PII-free (reusa o mesmo contrato do supervisionado).
  CA-1/TX-3: tenant_id propagado; row-policy na tabela 008 (fail-closed).
  Migr 007: NAO alterada. Migr 008: NOVA coluna/tabela; nao edita 007.

SEMÂNTICA IVT-ANTES-DO-FATURAMENTO:
  A marcacao final de IVT (is_ivt_final) e gravada em raw_ivt_score (007) via
  o job supervisionado. Este modulo adiciona raw_ivt_unsup_score (008) como
  log auditavel do score nao-supervisionado. A MV 008 propaga a flag
  ivt_unsup_detected para raw_impression (para auditoria / calibracao futura).
  O filtro de faturamento continua sendo ivt_status='' na MV 007 (004 inalterada).

COMBINACAO DE SCORES:
  Logica OR (combine_ivt_scores de ml/fraud/unsup_scorer.py):
    is_ivt_final = is_ivt_sup OR is_unsup_ivt
  O job ESTENDIDO (run_ivt_scoring_combined) aplica os dois scorers e grava
  a decisao final em raw_ivt_score (via ivt_scoring_job.write_ivt_scores).
  O score nao-supervisionado e gravado separadamente em raw_ivt_unsup_score (008).

IDEMPOTENCIA:
  raw_ivt_unsup_score usa ReplacingMergeTree(scored_at) com ORDER BY
  (event_id, score_run_id). Reprocessamento com mesmo score_run_id e idempotente.

FAIL-OPEN (TX-6):
  Se IVTUnsupScorer falhar: is_unsup_ivt=False, model_version='fail_open'.
  A decisao final cai de volta para o score supervisionado puro.
  O job DEVE monitorar ivt_unsup_fail_open_rate.
"""
from __future__ import annotations

import logging
import os
import sys
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import List, Optional

_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.fraud.scorer import IVTScorer, IVTScoringInput, IVTScore  # noqa: E402
from ml.fraud.unsup_scorer import (   # noqa: E402
    IVTUnsupScorer,
    IVTUnsupScore,
    combine_ivt_scores,
)
from data.fraud.ivt_scoring_job import (  # noqa: E402
    ImpressionRow,
    IVTScoreRow,
    IVT_STATUS_CLEAN,
    IVT_STATUS_IVT,
    IVT_STATUS_FAIL_OPEN,
    score_impression_batch,
    write_ivt_scores,
    write_ivt_scores_to_iceberg,
    read_unscored_impressions,
)

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------
log = logging.getLogger("ivt_unsup_scoring_job")


# ---------------------------------------------------------------------------
# Tipo de linha para raw_ivt_unsup_score (tabela 008)
# ---------------------------------------------------------------------------

@dataclass
class IVTUnsupScoreRow:
    """
    Linha a ser gravada em raw_ivt_unsup_score (migr 008).

    Campos de ML (float legitimos — probabilidade/metrica ML, nao dinheiro, TX-2):
      if_score:   score de anomalia do IsolationForest
      ae_error:   erro quadratico de reconstrucao do autoencoder
    """
    event_id:       str
    score_run_id:   str
    tenant_id:      str
    campaign_id:    str
    zone_id:        str
    occurred_at:    datetime
    scored_at:      datetime
    # Flags de deteccao
    is_unsup_ivt:   bool    # True se IF ou AE detectou anomalia
    # Scores de ML (float legitimos: metricas de ML, nao dinheiro, TX-2 N/A)
    if_label:       int     # 1=normal, -1=anomalia (IsolationForest)
    if_score:       float   # no-float-ok: score de anomalia ML, nao dinheiro
    ae_error:       float   # no-float-ok: erro de reconstrucao ML, nao dinheiro
    ae_threshold:   float   # no-float-ok: hiperparametro ML, nao dinheiro
    # Metadados do modelo
    model_version:  str
    # Decisao combinada final (OR entre supervisionado e nao-supervisionado)
    combined_is_ivt: bool


# ---------------------------------------------------------------------------
# Pontuacao de batch com combinacao de scores
# ---------------------------------------------------------------------------

def score_impression_batch_combined(
    impressions: List[ImpressionRow],
    sup_scorer: IVTScorer,
    unsup_scorer: IVTUnsupScorer,
    score_run_id: str,
    clickhouse_uri: str,
) -> tuple:
    """
    Pontua um batch de impressoes com os dois scorers (supervisionado + nao-superv.)
    e combina os resultados (logica OR).

    Retorna:
      (combined_score_rows: List[IVTScoreRow],
       unsup_score_rows: List[IVTUnsupScoreRow])

    combined_score_rows: linhas para raw_ivt_score (007), com is_ivt_final.
    unsup_score_rows:    linhas para raw_ivt_unsup_score (008), com scores detalhados.

    FAIL-OPEN (TX-6):
      Se o scorer nao-supervisionado falhar: is_unsup_ivt=False -> a decisao
      final e determinada apenas pelo supervisionado (comportamento identico
      ao job original sem K2).
      Se o scorer supervisionado falhar: is_ivt=False -> a decisao final
      e determinada pelo nao-supervisionado (cobre gaps do GBDT).
    """
    scored_at = datetime.now(timezone.utc)

    # 1. Monta inputs (PII-free — identico ao job supervisionado)
    inputs: List[IVTScoringInput] = []
    for imp in impressions:
        inputs.append(IVTScoringInput(
            device_class=imp.user_agent_class or "unknown",
            hour_of_day=imp.occurred_at.hour,
            day_of_week=imp.occurred_at.weekday(),
            geo_country=imp.geo_country or "",
            geo_city=imp.geo_city or "",
            hourly_requests=imp.hourly_requests,
            hourly_impressions=imp.hourly_impressions,
            hourly_clicks=imp.hourly_clicks,
            recent_hourly_imps=imp.recent_hourly_imps,
        ))

    # 2. Pontua via scorer supervisionado (GBDT IVT)
    sup_scores: List[IVTScore] = sup_scorer.score_batch(inputs)

    # 3. Pontua via scorer nao-supervisionado (IF + AE)
    unsup_scores: List[IVTUnsupScore] = unsup_scorer.score_batch(inputs)

    # 4. Combina e gera linhas de resultado
    combined_rows: List[IVTScoreRow]       = []
    unsup_rows:    List[IVTUnsupScoreRow]  = []

    sup_fail_open   = 0
    unsup_fail_open = 0

    for imp, sup, unsup in zip(impressions, sup_scores, unsup_scores):
        # Conta fail-opens para monitoramento
        if sup.model_version == "fail_open":
            sup_fail_open += 1
        if unsup.model_version == "fail_open":
            unsup_fail_open += 1

        # Combinacao OR: IVT se supervisionado OU nao-supervisionado detectou
        is_ivt_final = combine_ivt_scores(sup, unsup)
        ivt_status   = IVT_STATUS_IVT if is_ivt_final else IVT_STATUS_CLEAN

        # Linha para raw_ivt_score (007) com decisao final
        combined_rows.append(IVTScoreRow(
            event_id=imp.event_id,
            score_run_id=score_run_id,
            tenant_id=imp.tenant_id,
            campaign_id=imp.campaign_id,
            zone_id=imp.zone_id,
            occurred_at=imp.occurred_at,
            scored_at=scored_at,
            ivt_status=ivt_status,
            ivt_prob=sup.ivt_prob,    # Float legitimo: prob supervisionado
            ivt_label=int(is_ivt_final),
            model_version=f"sup:{sup.model_version}+unsup:{unsup.model_version}",
        ))

        # Linha para raw_ivt_unsup_score (008) com scores detalhados
        unsup_rows.append(IVTUnsupScoreRow(
            event_id=imp.event_id,
            score_run_id=score_run_id,
            tenant_id=imp.tenant_id,
            campaign_id=imp.campaign_id,
            zone_id=imp.zone_id,
            occurred_at=imp.occurred_at,
            scored_at=scored_at,
            is_unsup_ivt=unsup.is_unsup_ivt,
            if_label=unsup.if_label,
            if_score=unsup.if_score,      # Float legitimo: score IF ML
            ae_error=unsup.ae_error,      # Float legitimo: erro AE ML
            ae_threshold=unsup.ae_threshold,  # Float legitimo: threshold AE ML
            model_version=unsup.model_version,
            combined_is_ivt=is_ivt_final,
        ))

    if sup_fail_open > 0:
        log.warning(
            "SUP_FAIL_OPEN: %d/%d eventos em fail-open (scorer supervisionado).",
            sup_fail_open, len(impressions),
        )
    if unsup_fail_open > 0:
        log.warning(
            "UNSUP_FAIL_OPEN: %d/%d eventos em fail-open (scorer nao-supervisionado). "
            "Decisao final baseada apenas no supervisionado.",
            unsup_fail_open, len(impressions),
        )

    return combined_rows, unsup_rows


# ---------------------------------------------------------------------------
# Escrita em raw_ivt_unsup_score (ClickHouse, migr 008)
# ---------------------------------------------------------------------------

def write_ivt_unsup_scores(
    clickhouse_uri: str,
    unsup_scores: List[IVTUnsupScoreRow],
    dry_run: bool = False,
) -> None:
    """
    Grava os scores nao-supervisionados em adserver.raw_ivt_unsup_score (migr 008).

    SEMANTICA:
      Tabela de log auditavel do scoring nao-supervisionado.
      Permite: auditoria de quais eventos foram marcados por IF vs AE vs combinado,
      calibracao futura de limiares (ae_threshold, if_threshold),
      rastreamento de model_version por evento.

    IDEMPOTENCIA: ReplacingMergeTree(scored_at) com ORDER BY (event_id, score_run_id).

    NAO altera raw_ivt_score (007) nem mv_impression_to_hourly (004).
    A propagacao de ivt_status para raw_impression continua sendo feita
    pela MV 007 (mv_ivt_score_to_raw_impression) sobre raw_ivt_score.
    """
    if dry_run:
        unsup_count = sum(1 for s in unsup_scores if s.is_unsup_ivt)
        log.info(
            "DRY RUN: %d unsup scores (%d unsup-IVT) — nao gravando no ClickHouse.",
            len(unsup_scores), unsup_count,
        )
        return

    if not unsup_scores:
        log.info("write_ivt_unsup_scores: nenhum score para gravar.")
        return

    unsup_count = sum(1 for s in unsup_scores if s.is_unsup_ivt)
    log.info(
        "write_ivt_unsup_scores: %d scores (%d unsup-IVT, %d clean)",
        len(unsup_scores), unsup_count, len(unsup_scores) - unsup_count,
    )

    # TODO(data-platform): implementar INSERT em raw_ivt_unsup_score.
    # rows = [
    #     {
    #         "event_id":        s.event_id,
    #         "score_run_id":    s.score_run_id,
    #         "tenant_id":       s.tenant_id,
    #         "campaign_id":     s.campaign_id,
    #         "zone_id":         s.zone_id,
    #         "occurred_at":     s.occurred_at.isoformat(),
    #         "scored_at":       s.scored_at.isoformat(),
    #         "is_unsup_ivt":    int(s.is_unsup_ivt),
    #         "if_label":        s.if_label,
    #         "if_score":        s.if_score,       # Float legitimo: score ML
    #         "ae_error":        s.ae_error,       # Float legitimo: erro ML
    #         "ae_threshold":    s.ae_threshold,   # Float legitimo: threshold ML
    #         "model_version":   s.model_version,
    #         "combined_is_ivt": int(s.combined_is_ivt),
    #     }
    #     for s in unsup_scores
    # ]
    # client.insert("adserver.raw_ivt_unsup_score", rows, column_names=list(rows[0].keys()))


# ---------------------------------------------------------------------------
# Orquestrador combinado (K2): supervisionado + nao-supervisionado
# ---------------------------------------------------------------------------

def run_ivt_scoring_combined(
    since: datetime,
    until: datetime,
    sup_scorer: IVTScorer,
    unsup_scorer: IVTUnsupScorer,
    clickhouse_uri: str,
    iceberg_catalog_uri: str,
    batch_size: int = 1_000,
    dry_run: bool = False,
) -> dict:
    """
    Orquestrador K2: aplica os dois scorers (supervisionado + nao-supervisionado)
    e combina os resultados com logica OR.

    Fluxo:
      1. Gera score_run_id (UUID) para rastreabilidade.
      2. Le impressoes nao pontuadas de raw_impression em micro-batches.
      3. Pontua via IVTScorer (GBDT supervisionado) + IVTUnsupScorer (IF+AE).
      4. Combina com logica OR: is_ivt_final = is_ivt_sup OR is_unsup_ivt.
      5. Grava decisao final em raw_ivt_score (007) via write_ivt_scores.
      6. Grava scores detalhados em raw_ivt_unsup_score (008).
      7. Persiste todos os scores no Iceberg (para reconciliacao batch do billing).

    Se unsup_scorer estiver em fail-open:
      O job degrada para o comportamento do job supervisionado puro (J6).
      A decisao final e determinada apenas pelo GBDT supervisionado.
      Nunca bloqueia o billing por falha do scorer nao-supervisionado.

    Retorna estatisticas do run para monitoramento.
    """
    score_run_id = str(uuid.uuid4())
    log.info(
        "run_ivt_scoring_combined: since=%s until=%s score_run_id=%s "
        "dry_run=%s sup_loaded=%s unsup_loaded=%s",
        since.isoformat(), until.isoformat(), score_run_id,
        dry_run, sup_scorer.is_loaded, unsup_scorer.is_loaded,
    )

    total_scored   = 0
    total_ivt      = 0
    total_unsup_ivt = 0
    total_combined_ivt = 0
    total_sup_fail  = 0
    total_unsup_fail = 0
    all_combined:   List[IVTScoreRow]      = []
    all_unsup:      List[IVTUnsupScoreRow] = []

    for batch in read_unscored_impressions(
        clickhouse_uri=clickhouse_uri,
        since=since,
        until=until,
        batch_size=batch_size,
    ):
        if not batch:
            continue

        combined_rows, unsup_rows = score_impression_batch_combined(
            impressions=batch,
            sup_scorer=sup_scorer,
            unsup_scorer=unsup_scorer,
            score_run_id=score_run_id,
            clickhouse_uri=clickhouse_uri,
        )

        # Estatisticas do batch
        batch_ivt      = sum(1 for s in combined_rows if s.ivt_status == IVT_STATUS_IVT)
        batch_unsup    = sum(1 for s in unsup_rows if s.is_unsup_ivt)
        batch_combined = sum(1 for s in unsup_rows if s.combined_is_ivt)
        batch_sup_fail = sum(1 for s in combined_rows if "fail_open" in s.model_version.split("+")[0])
        batch_unsup_fail = sum(1 for s in unsup_rows if s.model_version == "fail_open")

        total_scored      += len(batch)
        total_ivt         += batch_ivt
        total_unsup_ivt   += batch_unsup
        total_combined_ivt += batch_combined
        total_sup_fail    += batch_sup_fail
        total_unsup_fail  += batch_unsup_fail

        # Escreve decisoes finais em raw_ivt_score (007)
        write_ivt_scores(
            clickhouse_uri=clickhouse_uri,
            scores=combined_rows,
            dry_run=dry_run,
        )

        # Escreve scores detalhados em raw_ivt_unsup_score (008)
        write_ivt_unsup_scores(
            clickhouse_uri=clickhouse_uri,
            unsup_scores=unsup_rows,
            dry_run=dry_run,
        )

        all_combined.extend(combined_rows)
        all_unsup.extend(unsup_rows)

    # Persiste no Iceberg (para reconciliacao batch do billing)
    write_ivt_scores_to_iceberg(
        iceberg_catalog_uri=iceberg_catalog_uri,
        scores=all_combined,
        dry_run=dry_run,
    )

    stats = {
        "score_run_id":         score_run_id,
        "since":                since.isoformat(),
        "until":                until.isoformat(),
        "total_scored":         total_scored,
        "total_ivt_final":      total_combined_ivt,
        "total_sup_only_ivt":   total_ivt,
        "total_unsup_detected": total_unsup_ivt,
        "total_sup_fail_open":  total_sup_fail,
        "total_unsup_fail_open": total_unsup_fail,
        "sup_fail_open_rate":   total_sup_fail  / max(1, total_scored),
        "unsup_fail_open_rate": total_unsup_fail / max(1, total_scored),
        "combined_ivt_rate":    total_combined_ivt / max(1, total_scored),
        "sup_model_version":    sup_scorer._model_version if sup_scorer.is_loaded else "fail_open",
        "unsup_model_version":  unsup_scorer._model_version if unsup_scorer.is_loaded else "fail_open",
    }

    log.info(
        "run_ivt_scoring_combined: COMPLETO. scored=%d combined_ivt=%d "
        "unsup_detected=%d sup_fail=%.1f%% unsup_fail=%.1f%%",
        stats["total_scored"], stats["total_ivt_final"],
        stats["total_unsup_detected"],
        stats["sup_fail_open_rate"] * 100,
        stats["unsup_fail_open_rate"] * 100,
    )

    return stats
