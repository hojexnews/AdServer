"""
data/fraud/ivt_scoring_job.py
==============================
Job near-line de scoring IVT: lê impressões raw, pontua via ml.fraud.scorer,
e grava os resultados em adserver.raw_ivt_score (ClickHouse) e no Iceberg
(para reconciliação batch de faturamento).

=============================================================================
PAPEL DESTE JOB (TX-6 / J6 / ADR-0001):
=============================================================================

  Redpanda -> raw_impression (ivt_status = '' default)
                    |
                    v  [este job — near-line, latência de minutos]
              IVTScorer.score_event()
                    |
                    v
              raw_ivt_score (ClickHouse)  <- log auditável
                    |
                    v  [MV mv_ivt_score_to_raw_impression — 007_ivt_scoring.sql]
              raw_impression (ivt_status atualizado)
                    |
                    v  [billing_batch_hourly.py, lê do Iceberg]
              Iceberg events_impression (apenas clean) -> billing

INVARIANTES (nao violar):
  TX-6: scoring FORA do hot path (latência de minutos aceitável); fail-open
        não bloqueia tráfego limpo (is_ivt=False em qualquer falha do scorer).
  TX-2/DA-10: nenhuma aritmética monetária neste job. Todos os campos são
              contadores inteiros ou probabilidades float (ML, não dinheiro).
  TX-5/DA-11: IVTScoringInput é PII-free (classe coarse de UA, geo de cidade,
              agregados horários — sem IP bruto, sem ID de usuário).
  CA-1/TX-3:  tenant_id propagado para raw_ivt_score; row-policy no ClickHouse
              garante isolamento (006_access_control.sql + 007_ivt_scoring.sql).

IDEMPOTÊNCIA:
  O job usa score_run_id (UUID) para cada execução. ReplacingMergeTree de
  raw_ivt_score usa (event_id, score_run_id) como ORDER BY; reprocessamento
  com o mesmo score_run_id produz o mesmo resultado (dedupe lazy).
  Reprocessamento com score_run_id diferente produz nova linha; a versão
  mais recente (scored_at maior) vence na merge do ReplacingMergeTree.

FAIL-OPEN (TX-6):
  Se scorer.score_event() lançar exceção ou se o modelo não estiver carregado:
    -> is_ivt=False, model_version='fail_open', ivt_status=''
  O evento passa como clean — conservador para não bloquear tráfego legítimo.
  O job DEVE monitorar a taxa de fail_open (métrica 'ivt_fail_open_rate').

RECONCILIAÇÃO BATCH (contra Iceberg, nunca contra streaming):
  Este job grava o resultado do scoring em raw_ivt_score (ClickHouse).
  A reconciliação final de faturamento é feita pelo billing_batch_hourly.py,
  que lê do Iceberg (fonte de verdade contábil), não deste job diretamente.
  O iceberg_sink_job.py exporta raw_impression (com ivt_status propagado)
  para o Iceberg antes do billing batch ser executado.

EXECUÇÃO (near-line, ex: a cada 5 minutos via cron/Airflow):
  python ivt_scoring_job.py \\
      --since "2026-06-03T14:00:00Z" \\
      --until "2026-06-03T14:05:00Z" \\
      --onnx-path "/path/to/ivt_model.onnx" \\
      --model-version "1" \\
      --dry-run

  Ou com MLflow registry:
  python ivt_scoring_job.py \\
      --since "2026-06-03T14:00:00Z" \\
      --until "2026-06-03T14:05:00Z" \\
      --mlflow-tracking-uri "http://mlflow:5000" \\
      --model-stage "Staging"

FRONTEIRA: este arquivo pertence a data/fraud/.
  NÃO toca em ml/ (apenas importa ml.fraud.scorer).
  NÃO toca em db/ (money-ledger-guardian).
  NÃO toca em services/ (decision-engine-engineer).
"""

from __future__ import annotations

import argparse
import logging
import os
import sys
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterator, List, Optional

# ---------------------------------------------------------------------------
# Resolve repo root para importar ml.fraud.scorer
# ---------------------------------------------------------------------------
_REPO_ROOT = Path(__file__).parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

from ml.fraud.scorer import (  # noqa: E402
    IVTScorer,
    IVTScoringInput,
    IVTScore,
    load_scorer_from_registry,
)

# ---------------------------------------------------------------------------
# Configuração de logging
# ---------------------------------------------------------------------------
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s %(message)s",
)
log = logging.getLogger("ivt_scoring_job")

# ---------------------------------------------------------------------------
# Constantes
# ---------------------------------------------------------------------------

# Janela de lookback para contexto horário (StatsHourly para os agregados)
_STATS_LOOKBACK_HOURS = 1

# Batch size para processar impressões em micro-batches (memory pressure)
_BATCH_SIZE = 1_000

# ivt_status values (enum controlado)
IVT_STATUS_CLEAN     = ""          # clean ou não avaliado
IVT_STATUS_IVT       = "ivt"       # classificado como IVT
IVT_STATUS_FAIL_OPEN = ""          # fail-open -> tratar como clean (conservador, TX-6)
IVT_STATUS_UNSCORED  = "unscored"  # ainda não pontuado (transiente; não faturar)


# ---------------------------------------------------------------------------
# Tipos auxiliares
# ---------------------------------------------------------------------------

@dataclass
class ImpressionRow:
    """
    Linha de raw_impression lida do ClickHouse para scoring.
    Todos os campos são PII-free (TX-5/DA-11):
      - user_agent_class: classe coarse (ex.: "bot", "chrome-mobile"), não UA bruto
      - geo_country/geo_city: derivados de IP via GeoLite2 (IP bruto descartado)
    """
    event_id: str
    tenant_id: str
    campaign_id: str
    banner_id: str
    zone_id: str
    occurred_at: datetime
    billable: bool
    blank: bool
    ivt_status: str
    # Campos de contexto (de raw_ad_request ou StatsHourly — PII-free)
    user_agent_class: str = ""      # classe coarse do UA (não UA bruto, TX-5)
    geo_country: str = ""           # ISO 3166-1 alpha-2 (sem IP, TX-5)
    geo_city: str = ""              # cidade derivada (sem coordenadas, TX-5)
    # Contexto horário (agregados de StatsHourly — sem PII)
    hourly_requests: int = 0
    hourly_impressions: int = 0
    hourly_clicks: int = 0
    recent_hourly_imps: List[int] = field(default_factory=list)


@dataclass
class IVTScoreRow:
    """
    Linha a ser gravada em raw_ivt_score (ClickHouse).
    """
    event_id: str
    score_run_id: str
    tenant_id: str
    campaign_id: str
    zone_id: str
    occurred_at: datetime
    scored_at: datetime
    ivt_status: str
    ivt_prob: float     # Float legítimo: probabilidade de ML, não dinheiro (TX-2 N/A)
    ivt_label: int
    model_version: str


# ---------------------------------------------------------------------------
# Funções de leitura do ClickHouse (esqueleto com clickhouse-connect)
# ---------------------------------------------------------------------------

def read_unscored_impressions(
    clickhouse_uri: str,
    since: datetime,
    until: datetime,
    batch_size: int = _BATCH_SIZE,
) -> Iterator[List[ImpressionRow]]:
    """
    Lê impressões ainda não pontuadas (ivt_status = '') de raw_impression.
    Faz JOIN com raw_ad_request para obter user_agent_class, geo_country, geo_city.
    Retorna micro-batches de tamanho batch_size.

    FILTROS:
      - occurred_at em [since, until)
      - ivt_status = '' (ainda não pontuado) — evita re-pontuar
      - blank = 0 (impressões em branco não precisam de scoring IVT)
      - billable = 1 (apenas impressões faturáveis precisam de scoring)

    PII: raw_ad_request tem user_agent_class (classe coarse, não UA bruto, TX-5)
         e geo_country/geo_city (derivados de IP, TX-5). Nenhum IP bruto.

    ESQUELETO: implementar com clickhouse-connect ou httpx.
    """
    log.info(
        "read_unscored_impressions: since=%s until=%s",
        since.isoformat(), until.isoformat(),
    )
    # TODO(data-platform): implementar com clickhouse-connect.
    # query = '''
    #   SELECT
    #       imp.event_id,
    #       imp.tenant_id,
    #       imp.campaign_id,
    #       imp.banner_id,
    #       imp.zone_id,
    #       imp.occurred_at,
    #       imp.billable,
    #       imp.blank,
    #       imp.ivt_status,
    #       -- JOIN com raw_ad_request para contexto PII-free (TX-5)
    #       coalesce(req.user_agent_class, '')  AS user_agent_class,
    #       coalesce(req.geo_country, '')       AS geo_country,
    #       coalesce(req.geo_city, '')          AS geo_city
    #   FROM adserver.raw_impression FINAL AS imp
    #   LEFT JOIN adserver.raw_ad_request FINAL AS req
    #       ON imp.zone_id    = req.zone_id
    #       AND imp.tenant_id = req.tenant_id
    #       AND toStartOfMinute(imp.occurred_at) = toStartOfMinute(req.occurred_at)
    #   WHERE imp.occurred_at >= %(since)s
    #     AND imp.occurred_at <  %(until)s
    #     AND imp.ivt_status  = ''
    #     AND imp.billable    = 1
    #     AND imp.blank       = 0
    #   ORDER BY imp.occurred_at, imp.event_id
    #   LIMIT %(batch_size)s OFFSET %(offset)s
    # '''
    # Implementar com paginação via OFFSET ou cursor por occurred_at.
    yield []  # placeholder: gerador vazio


def read_hourly_context(
    clickhouse_uri: str,
    zone_id: str,
    campaign_id: str,
    tenant_id: str,
    hour_bucket: datetime,
) -> dict:
    """
    Lê contexto horário agregado de stats_hourly para uso como features IVT.
    Retorna: {hourly_requests, hourly_impressions, hourly_clicks, recent_hourly_imps}.

    FONTE: stats_hourly (VIEW sobre stats_hourly_state) — dados de até 1h atrás.
    PII-free: stats_hourly é agregado por campanha/zona/hora, sem usuário individual.

    ESQUELETO: implementar com clickhouse-connect.
    """
    # TODO(data-platform): implementar query a stats_hourly.
    # query = '''
    #   SELECT
    #       requests,
    #       impressions,
    #       clicks
    #   FROM adserver.stats_hourly
    #   WHERE hour_bucket = toStartOfHour(%(hour_bucket)s)
    #     AND zone_id     = %(zone_id)s
    #     AND campaign_id = %(campaign_id)s
    #     AND tenant_id   = %(tenant_id)s
    #   LIMIT 1
    # '''
    # Para recent_hourly_imps (últimas 24h):
    # query_24h = '''
    #   SELECT groupArray(impressions)
    #   FROM adserver.stats_hourly
    #   WHERE hour_bucket >= toStartOfHour(%(hour_bucket)s) - INTERVAL 24 HOUR
    #     AND hour_bucket <  toStartOfHour(%(hour_bucket)s)
    #     AND zone_id     = %(zone_id)s
    #     AND campaign_id = %(campaign_id)s
    #     AND tenant_id   = %(tenant_id)s
    #   ORDER BY hour_bucket
    # '''
    return {
        "hourly_requests":    0,
        "hourly_impressions": 0,
        "hourly_clicks":      0,
        "recent_hourly_imps": [],
    }


# ---------------------------------------------------------------------------
# Função de escrita em raw_ivt_score (ClickHouse)
# ---------------------------------------------------------------------------

def write_ivt_scores(
    clickhouse_uri: str,
    scores: List[IVTScoreRow],
    dry_run: bool = False,
) -> None:
    """
    Grava os resultados de scoring em adserver.raw_ivt_score.
    A MV mv_ivt_score_to_raw_impression propaga ivt_status='ivt' para
    raw_impression automaticamente (007_ivt_scoring.sql).

    IDEMPOTÊNCIA: raw_ivt_score usa ReplacingMergeTree(scored_at);
    inserções com o mesmo (event_id, score_run_id) são deduplicadas na merge.

    ALTERNATIVA DE UPDATE DIRETO:
    Para volumes altos, é mais eficiente fazer ALTER TABLE UPDATE diretamente:
      ALTER TABLE adserver.raw_impression
        UPDATE ivt_status = 'ivt'
        WHERE event_id IN (list_of_ivt_event_ids)
          AND occurred_at >= since AND occurred_at < until
    O UPDATE é assíncrono (mutation); para volumes < 1B/dia (ADR-0002 B.1),
    o INSERT em raw_ivt_score + MV é aceitável na Fase 2.
    Para escala maior, migrar para ALTER TABLE UPDATE (sem Flink, ADR-0001).
    """
    if dry_run:
        ivt_count = sum(1 for s in scores if s.ivt_status == IVT_STATUS_IVT)
        log.info(
            "DRY RUN: %d scores (%d IVT) — nao gravando no ClickHouse.",
            len(scores), ivt_count,
        )
        return

    if not scores:
        log.info("write_ivt_scores: nenhum score para gravar.")
        return

    ivt_count = sum(1 for s in scores if s.ivt_status == IVT_STATUS_IVT)
    log.info(
        "write_ivt_scores: %d scores (%d IVT, %d clean/fail_open)",
        len(scores), ivt_count, len(scores) - ivt_count,
    )

    # TODO(data-platform): implementar INSERT em raw_ivt_score com clickhouse-connect.
    # rows = [
    #     {
    #         "event_id":      s.event_id,
    #         "score_run_id":  s.score_run_id,
    #         "tenant_id":     s.tenant_id,
    #         "campaign_id":   s.campaign_id,
    #         "zone_id":       s.zone_id,
    #         "occurred_at":   s.occurred_at.isoformat(),
    #         "scored_at":     s.scored_at.isoformat(),
    #         "ivt_status":    s.ivt_status,
    #         "ivt_prob":      s.ivt_prob,   # Float legitimo (probabilidade ML)
    #         "ivt_label":     s.ivt_label,
    #         "model_version": s.model_version,
    #     }
    #     for s in scores
    # ]
    # client.insert("adserver.raw_ivt_score", rows, column_names=list(rows[0].keys()))


# ---------------------------------------------------------------------------
# Função de escrita no Iceberg (ivt_scores para reconciliação batch)
# ---------------------------------------------------------------------------

def write_ivt_scores_to_iceberg(
    iceberg_catalog_uri: str,
    scores: List[IVTScoreRow],
    dry_run: bool = False,
) -> None:
    """
    Persiste os scores IVT no Iceberg (tabela adserver.events_ivt_score).
    Necessário para reconciliação batch de faturamento: o billing_batch_hourly.py
    verifica o ivt_status diretamente do Iceberg, não do ClickHouse (ADR-0001).

    RECONCILIAÇÃO:
    O billing_batch_hourly.py lê events_impression JOIN events_ivt_score do Iceberg
    para determinar quais impressões são clean. Isso garante que ajustes de IVT
    pós-fato (re-scoring, mudança de modelo) sejam capturados no próximo billing run
    sem re-ingestão de ClickHouse.

    IDEMPOTÊNCIA: MERGE INTO por event_id no Iceberg.
    """
    if dry_run:
        log.info("DRY RUN: %d scores — nao gravando no Iceberg.", len(scores))
        return

    if not scores:
        log.info("write_ivt_scores_to_iceberg: nenhum score para gravar.")
        return

    log.info("write_ivt_scores_to_iceberg: %d scores -> Iceberg.", len(scores))
    # TODO(data-platform): implementar com PyIceberg.
    # catalog = load_catalog("adserver", **{"uri": iceberg_catalog_uri})
    # table = catalog.load_table("adserver.events_ivt_score")
    # Fazer MERGE INTO (upsert) por event_id.


# ---------------------------------------------------------------------------
# Função de pontuação de um batch de impressões
# ---------------------------------------------------------------------------

def score_impression_batch(
    impressions: List[ImpressionRow],
    scorer: IVTScorer,
    score_run_id: str,
    clickhouse_uri: str,
) -> List[IVTScoreRow]:
    """
    Pontua um batch de impressões via IVTScorer.score_batch().

    Para cada impressão:
      1. Lê contexto horário (agregados de stats_hourly — PII-free).
      2. Monta IVTScoringInput com campos PII-free.
      3. Chama scorer.score_event().
      4. Normaliza o resultado para IVTScoreRow.

    FAIL-OPEN (TX-6):
      is_ivt=False (model_version='fail_open') -> ivt_status = '' (clean)
      Nunca bloqueia tráfego limpo por falha de ML.

    MONITORAMENTO:
      Registra contagem de fail_open para alertas de taxa de falha do scorer.
    """
    scored_at = datetime.now(timezone.utc)
    score_rows: List[IVTScoreRow] = []
    fail_open_count = 0

    # Montar inputs em lote para score_batch (mais eficiente)
    inputs: List[IVTScoringInput] = []
    contexts: List[dict] = []

    for imp in impressions:
        # Lê contexto horário para as features IVT (agregados PII-free)
        ctx = read_hourly_context(
            clickhouse_uri=clickhouse_uri,
            zone_id=imp.zone_id,
            campaign_id=imp.campaign_id,
            tenant_id=imp.tenant_id,
            hour_bucket=imp.occurred_at,
        )
        contexts.append(ctx)

        inp = IVTScoringInput(
            # PII-free: classe coarse de UA (TX-5/DA-11)
            device_class=imp.user_agent_class or "unknown",
            # Derivados de tempo (não PII)
            hour_of_day=imp.occurred_at.hour,
            day_of_week=imp.occurred_at.weekday(),  # 0=seg..6=dom (Python)
            # Geo derivado de IP, IP descartado (TX-5)
            geo_country=imp.geo_country or "",
            geo_city=imp.geo_city or "",
            # Agregados horários de StatsHourly (sem PII)
            hourly_requests=ctx["hourly_requests"],
            hourly_impressions=ctx["hourly_impressions"],
            hourly_clicks=ctx["hourly_clicks"],
            recent_hourly_imps=ctx["recent_hourly_imps"],
        )
        inputs.append(inp)

    # score_batch: fail-open individual — falha em um não propaga para outros
    scores: List[IVTScore] = scorer.score_batch(inputs)

    for imp, score in zip(impressions, scores):
        if score.model_version == "fail_open":
            fail_open_count += 1
            # Fail-open: tratar como clean (TX-6 — não bloquear tráfego legítimo)
            ivt_status = IVT_STATUS_CLEAN
        elif score.is_ivt:
            ivt_status = IVT_STATUS_IVT
        else:
            ivt_status = IVT_STATUS_CLEAN

        score_rows.append(IVTScoreRow(
            event_id=imp.event_id,
            score_run_id=score_run_id,
            tenant_id=imp.tenant_id,
            campaign_id=imp.campaign_id,
            zone_id=imp.zone_id,
            occurred_at=imp.occurred_at,
            scored_at=scored_at,
            ivt_status=ivt_status,
            ivt_prob=score.ivt_prob,    # Float legítimo: prob ML, não dinheiro
            ivt_label=score.ivt_label,
            model_version=score.model_version,
        ))

    if fail_open_count > 0:
        log.warning(
            "FAIL_OPEN: %d/%d eventos pontuados em fail-open (modelo não carregado "
            "ou exceção). ivt_status=''. Monitorar taxa de fail_open.",
            fail_open_count, len(impressions),
        )

    return score_rows


# ---------------------------------------------------------------------------
# Utilitário de reconciliação IVT para billing (importável por billing_batch_hourly.py)
# ---------------------------------------------------------------------------

def apply_ivt_filter_to_impressions(
    impression_rows: list,
    ivt_scores: dict,
) -> list:
    """
    Aplica o filtro IVT reconciliado sobre impressões de billing.
    Usado pelo billing_batch_hourly.py para determinar quais impressões são
    faturáveis após reconciliação com os scores do Iceberg (events_ivt_score).

    Para cada impressão:
      1. Busca ivt_status no mapa de scores Iceberg (tem precedência).
      2. Se não encontrado: usa ivt_status da events_impression.
      3. Exclui: ivt_status == 'ivt' OU ivt_status == 'unscored'.
      4. Inclui: ivt_status == '' (clean) OU ivt_status == 'fail_open'.

    INVARIANTE (TX-6):
      Nunca faturar tráfego marcado como IVT ou não pontuado (conservador).
      fail_open -> tratar como clean (não bloquear tráfego legítimo, TX-6).

    INVARIANTE (ADR-0001):
      Os scores em ivt_scores DEVEM vir do Iceberg (events_ivt_score),
      nunca do ClickHouse streaming. O billing é sempre baseado no Iceberg.

    Args:
      impression_rows: lista de dicts com pelo menos {"event_id", "ivt_status"}.
      ivt_scores: dict event_id -> ivt_status do Iceberg (events_ivt_score).

    Retorna lista de impressões clean para billing.
    """
    import logging as _logging
    _log = _logging.getLogger("ivt_scoring_job.apply_ivt_filter")

    clean_rows = []
    ivt_excluded = 0
    unscored_excluded = 0

    for row in impression_rows:
        event_id = row.get("event_id", "")
        # Score do Iceberg tem precedência sobre o status da impression
        ivt_status_final = ivt_scores.get(
            event_id,
            row.get("ivt_status", ""),  # fallback: ivt_status da events_impression
        )

        if ivt_status_final == IVT_STATUS_IVT:
            ivt_excluded += 1
            continue
        elif ivt_status_final == IVT_STATUS_UNSCORED:
            unscored_excluded += 1
            continue
        # '' (clean) e 'fail_open' passam como clean (TX-6 fail-open)
        clean_rows.append(row)

    if ivt_excluded > 0 or unscored_excluded > 0:
        _log.info(
            "apply_ivt_filter: excluidas %d impressoes IVT + %d nao-pontuadas de %d totais.",
            ivt_excluded, unscored_excluded, len(impression_rows),
        )

    return clean_rows


# ---------------------------------------------------------------------------
# Orquestrador principal
# ---------------------------------------------------------------------------

def run_ivt_scoring(
    since: datetime,
    until: datetime,
    scorer: IVTScorer,
    clickhouse_uri: str,
    iceberg_catalog_uri: str,
    batch_size: int = _BATCH_SIZE,
    dry_run: bool = False,
) -> dict:
    """
    Orquestrador do job de scoring IVT.

    Fluxo:
      1. Gera score_run_id (UUID) para rastreabilidade.
      2. Lê impressões não pontuadas de raw_impression em micro-batches.
      3. Pontua via IVTScorer.score_batch() (fail-open por evento).
      4. Grava scores em raw_ivt_score (ClickHouse) — MV propaga para raw_impression.
      5. Persiste scores no Iceberg (para reconciliação batch do billing).

    Retorna estatísticas do run para monitoramento.
    """
    score_run_id = str(uuid.uuid4())
    log.info(
        "run_ivt_scoring: since=%s until=%s score_run_id=%s dry_run=%s "
        "scorer_loaded=%s",
        since.isoformat(), until.isoformat(), score_run_id,
        dry_run, scorer.is_loaded,
    )

    total_scored = 0
    total_ivt = 0
    total_fail_open = 0
    all_scores: List[IVTScoreRow] = []

    # Processa em micro-batches para controlar memory pressure
    for batch in read_unscored_impressions(
        clickhouse_uri=clickhouse_uri,
        since=since,
        until=until,
        batch_size=batch_size,
    ):
        if not batch:
            continue

        scores = score_impression_batch(
            impressions=batch,
            scorer=scorer,
            score_run_id=score_run_id,
            clickhouse_uri=clickhouse_uri,
        )

        # Estatísticas do batch
        batch_ivt = sum(1 for s in scores if s.ivt_status == IVT_STATUS_IVT)
        batch_fail_open = sum(1 for s in scores if s.model_version == "fail_open")
        total_scored += len(scores)
        total_ivt += batch_ivt
        total_fail_open += batch_fail_open

        # Escreve scores no ClickHouse (MV propaga para raw_impression)
        write_ivt_scores(
            clickhouse_uri=clickhouse_uri,
            scores=scores,
            dry_run=dry_run,
        )

        # Acumula para escrita em lote no Iceberg
        all_scores.extend(scores)

    # Persiste todos os scores no Iceberg (fonte de verdade para billing batch)
    # Escrita em lote ao final do job para eficiência (não por micro-batch)
    write_ivt_scores_to_iceberg(
        iceberg_catalog_uri=iceberg_catalog_uri,
        scores=all_scores,
        dry_run=dry_run,
    )

    stats = {
        "score_run_id":   score_run_id,
        "since":          since.isoformat(),
        "until":          until.isoformat(),
        "total_scored":   total_scored,
        "total_ivt":      total_ivt,
        "total_clean":    total_scored - total_ivt,
        "total_fail_open": total_fail_open,
        "fail_open_rate":  (total_fail_open / max(1, total_scored)),
        "ivt_rate":        (total_ivt / max(1, total_scored)),
        "model_version":   scorer._model_version if scorer.is_loaded else "fail_open",
    }

    log.info(
        "run_ivt_scoring: COMPLETO. scored=%d ivt=%d clean=%d fail_open=%d "
        "(fail_open_rate=%.2f%% ivt_rate=%.2f%%)",
        stats["total_scored"], stats["total_ivt"], stats["total_clean"],
        stats["total_fail_open"],
        stats["fail_open_rate"] * 100,
        stats["ivt_rate"] * 100,
    )

    return stats


# ---------------------------------------------------------------------------
# Entrypoint CLI
# ---------------------------------------------------------------------------

def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="Job de scoring IVT near-line (TX-6 / ADR-0001).",
    )
    p.add_argument(
        "--since",
        required=True,
        help="ISO 8601 UTC início da janela de scoring (ex.: 2026-06-03T14:00:00Z).",
    )
    p.add_argument(
        "--until",
        required=True,
        help="ISO 8601 UTC fim da janela de scoring (ex.: 2026-06-03T14:05:00Z).",
    )
    p.add_argument(
        "--onnx-path",
        default=None,
        help="Caminho para o artefato ivt_model.onnx. "
             "Se não fornecido, tenta carregar do MLflow registry.",
    )
    p.add_argument(
        "--model-version",
        default="1",
        help="Versão do modelo IVT no MLflow (default: '1').",
    )
    p.add_argument(
        "--mlflow-tracking-uri",
        default=os.environ.get("MLFLOW_TRACKING_URI", ""),
        help="URI do MLflow tracking server.",
    )
    p.add_argument(
        "--model-stage",
        default="Staging",
        choices=["Staging", "Production", "None"],
        help="Stage do modelo no MLflow registry (default: Staging).",
    )
    p.add_argument(
        "--batch-size",
        type=int,
        default=_BATCH_SIZE,
        help=f"Tamanho do micro-batch de impressões (default: {_BATCH_SIZE}).",
    )
    p.add_argument("--dry-run", action="store_true", help="Simula sem gravar.")
    p.add_argument(
        "--clickhouse-uri",
        default=os.environ.get("CLICKHOUSE_URI", "http://localhost:8123"),
    )
    p.add_argument(
        "--catalog-uri",
        default=os.environ.get("ICEBERG_CATALOG_URI", "http://localhost:8181"),
    )
    return p.parse_args()


def main() -> None:
    args = parse_args()

    since = datetime.fromisoformat(args.since.replace("Z", "+00:00"))
    until = datetime.fromisoformat(args.until.replace("Z", "+00:00"))

    # Carrega o scorer IVT
    if args.onnx_path:
        # Caminho direto para o artefato ONNX (dev/CI)
        scorer = IVTScorer(
            onnx_path=Path(args.onnx_path),
            model_version=args.model_version,
        )
        scorer.load()
    elif args.mlflow_tracking_uri:
        # Carrega do MLflow registry (produção)
        scorer = load_scorer_from_registry(
            mlflow_tracking_uri=args.mlflow_tracking_uri,
            model_name="ivt_classifier",
            model_stage=args.model_stage,
        )
    else:
        # Sem modelo configurado: fail-open (scorer sem model carregado)
        log.warning(
            "Nenhum --onnx-path nem --mlflow-tracking-uri configurado. "
            "Scorer operará em fail-open (todos os eventos = clean). "
            "Configure o modelo para scoring efetivo (TX-6)."
        )
        scorer = IVTScorer()  # fail-open

    run_ivt_scoring(
        since=since,
        until=until,
        scorer=scorer,
        clickhouse_uri=args.clickhouse_uri,
        iceberg_catalog_uri=args.catalog_uri,
        batch_size=args.batch_size,
        dry_run=args.dry_run,
    )


if __name__ == "__main__":
    main()
