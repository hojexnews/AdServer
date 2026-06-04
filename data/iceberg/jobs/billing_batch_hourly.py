"""
data/iceberg/jobs/billing_batch_hourly.py

Esqueleto do job de billing batch horario (DA-7 / ADR-0002 B.7 / CA-7).

PAPEL DESTE JOB:
    Le os eventos de impressao, clique e conversao do lakehouse Iceberg
    (fonte de verdade contabil) e grava postings de billing na tabela
    adserver.billing_hourly, que e lida pelo money-ledger-guardian.

INVARIANTES (nao violar):
    1. NUNCA ler do streaming ClickHouse ("ao vivo") para calcular faturamento.
       A unica fonte valida e o Iceberg (ADR-0001 / ADR-0002 §D).
    2. Faturamento NUNCA em Float. Todas as aritmeticas monetarias em
       decimal.Decimal com contexto ROUND_HALF_EVEN (TX-2/DA-10).
    3. O job e IDEMPOTENTE: reprocessar o mesmo hour_bucket produz o mesmo
       resultado. Implementado via MERGE INTO (delete + insert) no Iceberg.
    4. Reconciliacao Iceberg <-> ClickHouse: divergencia ABRE EXCECAO (log
       + alerta), nunca autocorrige. Ver reconcile() abaixo.
    5. A janela de atribuicao (lookback_days) e PARAMETRO do job (ADR-0002 B.7),
       nao fato gravado no schema. Trocar a janela nao exige migracao.
    6. IVT excluido via reconciliacao batch contra Iceberg (TX-6 / J6):
       - O filtro IVT definitivo usa events_ivt_score (Iceberg), nao o
         ivt_status do ClickHouse.
       - Nunca faturar ivt_status='ivt' ou 'unscored'.
       - fail_open -> tratar como clean (nao bloquear trafego legitimo, TX-6).
       - Ver apply_ivt_filter_to_impressions() e read_ivt_scores_for_hour().
    7. Conversoes deduplicated=True sao excluidas do billing CPA.
    8. O scale de cada ativo vem do Asset Registry (db/ledger/); nunca
       inferido do payload. Divergencia de scale = excecao, nao correcao.

MODELO DE ATRIBUICAO (Fase 1):
    Last-click: a conversao e atribuida ao ultimo clique do mesmo campaign_id
    dentro da janela de lookback (lookback_days). O join e feito sobre
    events_click e events_conversion no Iceberg, por decision_id ou por
    (campaign_id, tenant_id, occurred_at dentro da janela).

DEPENDENCIAS EXTERNAS (fronteiras de modulo):
    - Iceberg catalog: URI em ICEBERG_CATALOG_URI (env).
    - Asset Registry: lido de db/ledger/ via contrato de leitura (dono:
      money-ledger-guardian). Este job NAO implementa o schema do ledger;
      apenas le o scale por asset_code.
    - money-ledger-guardian: le billing_hourly (status='pending') e grava
      journal_entries/postings no Postgres. Este job NAO escreve no ledger.

EXECUCAO:
    python billing_batch_hourly.py \
        --hour-bucket "2026-06-03T14:00:00Z" \
        --tenant-id  "all"                     # ou um tenant especifico \
        --lookback-days 7 \
        --dry-run

FRONTEIRA: este arquivo pertence a data/. NAO tocar em db/ (money-ledger-guardian).
"""

from __future__ import annotations

import argparse
import logging
import os
import sys
import uuid
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from decimal import ROUND_HALF_EVEN, Decimal, localcontext
from pathlib import Path

# Resolve repo root para importar do data/fraud/ (mesmo processo Python)
_REPO_ROOT = Path(__file__).parent.parent.parent.parent
sys.path.insert(0, str(_REPO_ROOT))

# Importa utilitários de reconciliação IVT do job de scoring (dono: data-platform)
# apply_ivt_filter_to_impressions: aplica filtro IVT reconciliado (Iceberg > ClickHouse)
from data.fraud.ivt_scoring_job import apply_ivt_filter_to_impressions  # noqa: E402

# ---------------------------------------------------------------------------
# Configuracao de logging
# ---------------------------------------------------------------------------
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s %(message)s",
)
log = logging.getLogger("billing_batch_hourly")

# ---------------------------------------------------------------------------
# Constante de contexto decimal (TX-2): NUNCA float, ROUND_HALF_EVEN.
# precision=60 cobre minor-units de ERC-20 (18 casas) com folga.
# ---------------------------------------------------------------------------
DECIMAL_CTX_PRECISION = 60
DECIMAL_ROUNDING = ROUND_HALF_EVEN

# ---------------------------------------------------------------------------
# Tipos auxiliares
# ---------------------------------------------------------------------------

@dataclass(frozen=True)
class AssetInfo:
    """Informacao de ativo do Asset Registry (lido do db/ledger/ via contrato)."""
    asset_code: str
    scale: int       # inteiro, nunca float

@dataclass
class BillingPosting:
    """Posting de billing a ser gravado em adserver.billing_hourly."""
    billing_id: str
    tenant_id: str
    hour_bucket: datetime
    campaign_id: str
    banner_id: str
    zone_id: str
    pricing_model: str          # CPM | CPC | CPA | TENANCY
    billable_units: int
    rate_amount: Decimal        # Decimal, nunca float
    rate_asset_code: str
    rate_scale: int
    billable_amount: Decimal    # Decimal, nunca float
    currency: str
    attribution_lookback_days: int
    attribution_model: str
    job_run_id: str
    job_run_at: datetime
    iceberg_snapshot_id: int
    status: str = "pending"

# ---------------------------------------------------------------------------
# Funcoes de leitura do Asset Registry
# (contrato de leitura com money-ledger-guardian)
# ---------------------------------------------------------------------------

def load_asset_registry(db_connection_uri: str) -> dict[str, AssetInfo]:
    """
    Le o Asset Registry do db/ledger/ (Postgres, dono: money-ledger-guardian).
    Retorna mapa asset_code -> AssetInfo.

    INVARIANTE: scale NUNCA inferido do payload; sempre do Registry.
    Divergencia entre scale do evento e scale do Registry = excecao.

    Este metodo NAO implementa o schema do ledger; apenas le via
    contrato de leitura (SELECT asset_code, scale FROM asset_registry).
    """
    # ESQUELETO: implementar com psycopg3 ou sqlalchemy.
    # Exemplo de query (nao executavel sem conexao real):
    #   SELECT asset_code, scale FROM asset_registry WHERE enabled = true;
    log.info("load_asset_registry: uri=%s", db_connection_uri[:20] + "...")
    # TODO(data-platform): implementar conexao Postgres e query real.
    # Placeholder para desenvolvimento:
    return {
        "BRL":  AssetInfo("BRL",  scale=2),
        "USD":  AssetInfo("USD",  scale=2),
        "EUR":  AssetInfo("EUR",  scale=2),
        "USDC": AssetInfo("USDC", scale=6),
        "USDT": AssetInfo("USDT", scale=6),
    }

# ---------------------------------------------------------------------------
# Funcoes de calculo de billing (aritmetica em Decimal)
# ---------------------------------------------------------------------------

def calc_cpm_amount(
    impressions: int,
    rate: Decimal,
    asset: AssetInfo,
) -> Decimal:
    """
    Calculo CPM: (impressions / 1000) * rate.
    Tudo em Decimal; nunca float (TX-2).
    ROUND_HALF_EVEN aplicado apenas na fronteira de faturamento.
    """
    with localcontext() as ctx:
        ctx.prec = DECIMAL_CTX_PRECISION
        ctx.rounding = DECIMAL_ROUNDING
        result = (Decimal(impressions) / Decimal(1000)) * rate
        # Arredondar para o scale do ativo (ROUND_HALF_EVEN, TX-2 §6)
        quantize_str = Decimal(10) ** -asset.scale
        return result.quantize(quantize_str, rounding=DECIMAL_ROUNDING)


def calc_cpc_amount(clicks: int, rate: Decimal, asset: AssetInfo) -> Decimal:
    """Calculo CPC: clicks * rate. Decimal, nunca float."""
    with localcontext() as ctx:
        ctx.prec = DECIMAL_CTX_PRECISION
        ctx.rounding = DECIMAL_ROUNDING
        result = Decimal(clicks) * rate
        quantize_str = Decimal(10) ** -asset.scale
        return result.quantize(quantize_str, rounding=DECIMAL_ROUNDING)


def calc_cpa_amount(conversions: int, rate: Decimal, asset: AssetInfo) -> Decimal:
    """Calculo CPA: conversions * rate. Decimal, nunca float."""
    with localcontext() as ctx:
        ctx.prec = DECIMAL_CTX_PRECISION
        ctx.rounding = DECIMAL_ROUNDING
        result = Decimal(conversions) * rate
        quantize_str = Decimal(10) ** -asset.scale
        return result.quantize(quantize_str, rounding=DECIMAL_ROUNDING)

# ---------------------------------------------------------------------------
# Leitura do Iceberg (esqueleto com PyIceberg)
# ---------------------------------------------------------------------------

def read_ivt_scores_for_hour(
    catalog,
    hour_bucket: datetime,
    tenant_id: str | None,
) -> dict[str, str]:
    """
    Le os scores IVT do Iceberg (adserver.events_ivt_score) para a hora
    especificada e retorna um mapa event_id -> ivt_status.

    INVARIANTE (TX-6 / ADR-0001):
      O billing NUNCA usa o ivt_status do ClickHouse diretamente.
      A fonte de verdade para o ivt_status de billing e SEMPRE o Iceberg.
      Este metodo le os scores IVT persistidos pelo ivt_scoring_job.py.

    POLITICA DE FALLBACK:
      Se um event_id nao tiver score em events_ivt_score:
        -> ivt_status = 'unscored' (nao faturar — conservador)
      Se o score for model_version='fail_open':
        -> ivt_status = '' (clean — fail-open conservador, TX-6)

    RECONCILIACAO POS-FATO:
      Se o job de scoring rodar APOS o billing (late scoring), o proximo
      billing run (reprocessamento da mesma hora) captara o score correto
      via time-travel do Iceberg (snapshot_id fixo no billing_posting).
      Divergencia detectada por reconcile_with_clickhouse() -> excecao.

    Retorna dict: {event_id: ivt_status_final}
    """
    hour_end = hour_bucket + timedelta(hours=1)
    log.info(
        "read_ivt_scores_for_hour: hour=%s tenant=%s",
        hour_bucket.isoformat(), tenant_id or "all",
    )
    # TODO(data-platform): implementar scan PyIceberg.
    # table = catalog.load_table("adserver.events_ivt_score")
    # scan = table.scan(
    #     row_filter=(
    #         (col("occurred_at") >= lit(hour_bucket)) &
    #         (col("occurred_at") <  lit(hour_end)) &
    #         (col("tenant_id")   == lit(tenant_id) if tenant_id else lit(True))
    #     ),
    #     selected_fields=("event_id", "ivt_status", "model_version"),
    # )
    # rows = scan.to_arrow().to_pylist()
    # Politica de resolucao: score mais recente (scored_at maior) por event_id.
    # Se model_version='fail_open': ivt_status = '' (clean, TX-6 fail-open).
    # Retornar {event_id: ivt_status_final}.
    return {}  # placeholder: mapa vazio (sem scores -> billing usa ivt_status da impression)


def read_impressions_for_billing(
    catalog,
    hour_bucket: datetime,
    tenant_id: str | None,
) -> list[dict]:
    """
    Le impressoes faturadas para a hora especificada.
    Filtra primario: billable=True AND blank=False (da events_impression).
    O filtro IVT DEFINITIVO e aplicado por apply_ivt_filter_to_impressions()
    usando os scores de events_ivt_score (fonte de verdade IVT no Iceberg).

    NOTA: o ivt_status em events_impression e o status na hora da ingestao.
    O status reconciliado pos-scoring (events_ivt_score) pode diferir se o
    scoring rodar apos o sink. O billing usa SEMPRE o score de events_ivt_score
    quando disponivel (apply_ivt_filter_to_impressions).

    Retorna lista de dicts: {tenant_id, campaign_id, banner_id, zone_id,
                             event_id, ivt_status, count}.

    ESQUELETO: usar PyIceberg scan com filtros de predicado (partition pruning).
    """
    hour_end = hour_bucket + timedelta(hours=1)
    log.info(
        "read_impressions_for_billing: hour=%s tenant=%s",
        hour_bucket.isoformat(), tenant_id or "all",
    )
    # TODO(data-platform): implementar scan real com PyIceberg.
    # Exemplo:
    #   table = catalog.load_table("adserver.events_impression")
    #   scan = table.scan(
    #       row_filter=(
    #           (col("occurred_at") >= lit(hour_bucket)) &
    #           (col("occurred_at") <  lit(hour_end)) &
    #           (col("billable")    == lit(True)) &
    #           (col("blank")       == lit(False)) &
    #           (col("tenant_id")   == lit(tenant_id) if tenant_id else lit(True))
    #       ),
    #       selected_fields=(
    #           "tenant_id","campaign_id","banner_id","zone_id",
    #           "event_id","ivt_status",
    #       ),
    #       snapshot_id=snapshot_id,   # time-travel: snapshot fixo para reproducibilidade
    #   )
    #   rows = scan.to_arrow().to_pylist()
    #   # NAO filtrar ivt_status aqui: o filtro IVT reconciliado vem de
    #   # apply_ivt_filter_to_impressions() com os scores de events_ivt_score.
    #   # Retornar todas as impressoes billable=True, blank=False para o filtro
    #   # IVT ser aplicado com a fonte de verdade (events_ivt_score).
    #   return rows
    return []  # placeholder


def read_clicks_for_billing(
    catalog,
    hour_bucket: datetime,
    tenant_id: str | None,
) -> list[dict]:
    """
    Le cliques para billing CPC na hora especificada.
    Retorna lista de dicts: {tenant_id, campaign_id, banner_id, zone_id, count}.
    """
    log.info(
        "read_clicks_for_billing: hour=%s tenant=%s",
        hour_bucket.isoformat(), tenant_id or "all",
    )
    # TODO(data-platform): implementar scan PyIceberg (similar a impressions).
    return []  # placeholder


def read_conversions_for_billing(
    catalog,
    hour_bucket: datetime,
    tenant_id: str | None,
    lookback_days: int,
    attribution_model: str,
) -> list[dict]:
    """
    Le conversoes atribuidas para billing CPA.
    Atribuicao last-click: join entre events_conversion e events_click
    dentro da janela de lookback (lookback_days).

    MODELO DE ATRIBUICAO (ADR-0002 B.7):
        - Fase 1: last_click com lookback de 7 dias.
        - A janela e PARAMETRO deste metodo, nao fato no schema.
        - O join e feito aqui, sobre o Iceberg, pos-FINAL (dados completos).
        - Conversoes deduplicated=True sao excluidas.

    Retorna lista de dicts: {tenant_id, campaign_id, banner_id, sum_conversion_value,
                              asset_code, scale, count}.
    """
    log.info(
        "read_conversions_for_billing: hour=%s tenant=%s lookback=%dd model=%s",
        hour_bucket.isoformat(), tenant_id or "all", lookback_days, attribution_model,
    )
    # TODO(data-platform): implementar scan + join no Iceberg.
    # Logica de atribuicao last-click:
    #   conversion_window_start = hour_bucket - timedelta(days=lookback_days)
    #   1. Ler events_conversion onde occurred_at in [hour_bucket, hour_bucket+1h)
    #      AND deduplicated = False.
    #   2. Para cada conversao, buscar o ultimo clique em events_click onde
    #      campaign_id = conversao.campaign_id AND
    #      occurred_at in [conversion.occurred_at - lookback_days, conversion.occurred_at]
    #      ORDER BY occurred_at DESC LIMIT 1.
    #   3. Se encontrar clique: atribuir a campanha/banner do clique.
    #      Se nao encontrar: conversao sem atribuicao (excluir do CPA billing).
    #   4. Agregar por (tenant_id, campaign_id, banner_id, asset_code).
    return []  # placeholder


def get_iceberg_snapshot_id(catalog, table_name: str) -> int:
    """
    Retorna o snapshot_id atual da tabela Iceberg.
    Usado para gravar a referencia de reproducibilidade no billing_posting.
    """
    # TODO(data-platform): catalog.load_table(table_name).current_snapshot().snapshot_id
    return -1  # placeholder

# ---------------------------------------------------------------------------
# Escrita no Iceberg (billing_hourly)
# ---------------------------------------------------------------------------

def write_billing_postings(
    catalog,
    postings: list[BillingPosting],
    hour_bucket: datetime,
    dry_run: bool = False,
) -> None:
    """
    Grava postings na tabela adserver.billing_hourly.
    IDEMPOTENTE: deleta as linhas da hora antes de inserir (MERGE INTO / DELETE+INSERT).
    Se dry_run=True, apenas loga os postings sem gravar.
    """
    if dry_run:
        for p in postings:
            log.info("DRY RUN posting: %s", p)
        return

    if not postings:
        log.info("write_billing_postings: nenhum posting para gravar.")
        return

    log.info(
        "write_billing_postings: %d postings para %s",
        len(postings), hour_bucket.isoformat(),
    )
    # TODO(data-platform): implementar via PyIceberg MERGE INTO ou DELETE + INSERT.
    # Garantia de idempotencia: delete WHERE hour_bucket = ? AND tenant_id = ?
    # antes do INSERT. Isso garante que reprocessamento e seguro.
    #
    # INVARIANTE: billable_amount deve ser Decimal, nunca float.
    # Verificar antes do INSERT:
    for p in postings:
        assert isinstance(p.billable_amount, Decimal), (
            f"billing invariant violated: billable_amount must be Decimal, got {type(p.billable_amount)}"
        )
        assert isinstance(p.rate_amount, Decimal), (
            f"billing invariant violated: rate_amount must be Decimal, got {type(p.rate_amount)}"
        )


# ---------------------------------------------------------------------------
# Reconciliacao Iceberg <-> ClickHouse (invariante: divergencia abre excecao)
# ---------------------------------------------------------------------------

def reconcile_with_clickhouse(
    iceberg_totals: dict,
    clickhouse_totals: dict,
    tolerance_pct: Decimal = Decimal("0.01"),  # 1% de tolerancia
) -> None:
    """
    Compara totais de impressoes/cliques/conversoes entre Iceberg e ClickHouse.
    Divergencia acima da tolerancia ABRE EXCECAO (log + alerta).
    NUNCA autocorrige; a excecao e tratada manualmente.

    INVARIANTE (ADR-0001): o billing nao usa o numero do ClickHouse.
    Esta funcao e APENAS de verificacao/monitoramento. O billing continua
    baseado no Iceberg independente do resultado da reconciliacao.

    tolerance_pct: tolerancia de divergencia aceita (Decimal, nunca float).
    """
    log.info("reconcile_with_clickhouse: verificando divergencias...")
    # TODO(data-platform): implementar query ao ClickHouse stats_hourly
    # e comparar com os totais do Iceberg.
    # Logica:
    #   for metric in ("impressions", "clicks", "conversions"):
    #       iceberg_val = Decimal(iceberg_totals.get(metric, 0))
    #       ch_val = Decimal(clickhouse_totals.get(metric, 0))
    #       if iceberg_val == 0:
    #           continue
    #       diff_pct = abs(iceberg_val - ch_val) / iceberg_val
    #       if diff_pct > tolerance_pct:
    #           log.error(
    #               "RECONCILIACAO DIVERGENCIA: %s iceberg=%s clickhouse=%s diff=%.2f%%",
    #               metric, iceberg_val, ch_val, float(diff_pct * 100),
    #           )
    #           # Abrir excecao: notificar PagerDuty/Slack; NAO autocorrigir.
    #           raise ReconciliationException(metric, iceberg_val, ch_val, diff_pct)
    log.info("reconcile_with_clickhouse: ok (esqueleto)")


class ReconciliationException(Exception):
    """Divergencia entre Iceberg e ClickHouse acima da tolerancia."""
    def __init__(self, metric: str, iceberg_val: Decimal, ch_val: Decimal, diff_pct: Decimal):
        super().__init__(
            f"Reconciliacao: {metric} diverge {diff_pct:.2%} "
            f"(iceberg={iceberg_val}, clickhouse={ch_val}). "
            "Abrir incidente; NAO autocorrigir."
        )
        self.metric = metric
        self.iceberg_val = iceberg_val
        self.ch_val = ch_val
        self.diff_pct = diff_pct


# ---------------------------------------------------------------------------
# Orquestrador principal do job
# ---------------------------------------------------------------------------

def run_billing_batch(
    hour_bucket: datetime,
    tenant_id: str | None,
    lookback_days: int,
    attribution_model: str,
    dry_run: bool,
    catalog_uri: str,
    db_uri: str,
) -> None:
    """
    Orquestrador do billing batch horario.

    Fluxo:
        1. Carrega Asset Registry (scale por ativo).
        2. Abre o catalogo Iceberg.
        3. Le impressoes, cliques, conversoes do Iceberg (com snapshot_id fixo).
        4. Calcula postings de billing por pricing_model (CPM/CPC/CPA/Tenancy).
        5. Grava postings em billing_hourly (idempotente).
        6. Reconcilia totais com ClickHouse (apenas monitoramento; nao altera billing).

    Invariantes verificadas:
        - Todos os valores monetarios sao Decimal (assert antes de gravar).
        - Postings com status='pending' para o guardian processar.
        - Snapshot Iceberg gravado no posting (reproducibilidade).
    """
    log.info(
        "run_billing_batch: hour=%s tenant=%s lookback=%dd attribution=%s dry_run=%s",
        hour_bucket.isoformat(), tenant_id or "all",
        lookback_days, attribution_model, dry_run,
    )

    job_run_id = str(uuid.uuid7() if hasattr(uuid, "uuid7") else uuid.uuid4())
    job_run_at = datetime.now(timezone.utc)

    # 1. Asset Registry
    asset_registry = load_asset_registry(db_uri)

    # 2. Catalogo Iceberg
    # TODO(data-platform): from pyiceberg.catalog import load_catalog
    # catalog = load_catalog("adserver", **{"uri": catalog_uri})
    catalog = None  # placeholder

    # 3. Snapshot IDs para reproducibilidade (time-travel)
    impression_snapshot_id = get_iceberg_snapshot_id(catalog, "adserver.events_impression")
    click_snapshot_id      = get_iceberg_snapshot_id(catalog, "adserver.events_click")
    conversion_snapshot_id = get_iceberg_snapshot_id(catalog, "adserver.events_conversion")

    # 4. Leitura dos eventos
    #
    # 4a. Carrega scores IVT do Iceberg (events_ivt_score) para reconciliacao.
    #     INVARIANTE (TX-6 / ADR-0001): o filtro IVT de billing usa SEMPRE
    #     os scores do Iceberg, nao o ivt_status do ClickHouse.
    ivt_scores = read_ivt_scores_for_hour(catalog, hour_bucket, tenant_id)
    log.info(
        "run_billing_batch: %d scores IVT carregados do Iceberg para hora %s.",
        len(ivt_scores), hour_bucket.isoformat(),
    )

    # 4b. Le impressoes (billable=True, blank=False) do Iceberg.
    #     O filtro IVT definitivo e aplicado em seguida (4c).
    impression_rows_raw = read_impressions_for_billing(catalog, hour_bucket, tenant_id)

    # 4c. Aplica filtro IVT reconciliado (events_ivt_score > events_impression).
    #     Exclui: ivt_status='ivt' (classificado) e 'unscored' (nao pontuado).
    #     Inclui: '' (clean) e 'fail_open' (fail-open conservador, TX-6).
    #     NUNCA faturar IVT ou trafego nao avaliado.
    impression_rows = apply_ivt_filter_to_impressions(impression_rows_raw, ivt_scores)

    log.info(
        "run_billing_batch: impressoes apos filtro IVT: %d/%d (excluidas: %d).",
        len(impression_rows), len(impression_rows_raw),
        len(impression_rows_raw) - len(impression_rows),
    )

    click_rows      = read_clicks_for_billing(catalog, hour_bucket, tenant_id)
    conversion_rows = read_conversions_for_billing(
        catalog, hour_bucket, tenant_id, lookback_days, attribution_model,
    )

    # 5. Calculo dos postings
    postings: list[BillingPosting] = []

    # CPM: billing por impressoes (1 posting por campanha/banner/zona/hora)
    # Nota: impression_rows ja esta filtrado por IVT (apenas clean, TX-6).
    for row in impression_rows:
        # TODO: buscar rate da campanha (contrato com money-ledger-guardian via db/)
        # rate = fetch_campaign_rate(row["campaign_id"], "CPM", db_uri)
        asset_code = row.get("currency", "BRL")  # placeholder
        asset = asset_registry.get(asset_code)
        if not asset:
            log.error("asset_code desconhecido no Asset Registry: %s", asset_code)
            continue
        rate = Decimal("10.00")  # placeholder: buscar do db/ via contrato
        billable_units = int(row.get("count", 0))
        billable_amount = calc_cpm_amount(billable_units, rate, asset)
        postings.append(BillingPosting(
            billing_id=str(uuid.uuid4()),
            tenant_id=row["tenant_id"],
            hour_bucket=hour_bucket,
            campaign_id=row["campaign_id"],
            banner_id=row["banner_id"],
            zone_id=row.get("zone_id", ""),
            pricing_model="CPM",
            billable_units=billable_units,
            rate_amount=rate,
            rate_asset_code=asset_code,
            rate_scale=asset.scale,
            billable_amount=billable_amount,
            currency=asset_code,
            attribution_lookback_days=lookback_days,
            attribution_model=attribution_model,
            job_run_id=job_run_id,
            job_run_at=job_run_at,
            iceberg_snapshot_id=impression_snapshot_id,
        ))

    # CPC: billing por cliques (esqueleto identico ao CPM; omitido por brevidade)
    # CPA: billing por conversoes atribuidas (esqueleto identico; omitido)
    # TENANCY: tarifa fixa por periodo — nao baseado em eventos; calculado separadamente

    # 6. Grava postings (idempotente)
    write_billing_postings(catalog, postings, hour_bucket, dry_run=dry_run)

    # 7. Reconciliacao (apenas monitoramento)
    # TODO: buscar totais do ClickHouse stats_hourly para comparacao
    iceberg_totals = {"impressions": sum(r.get("count", 0) for r in impression_rows)}
    clickhouse_totals = {}  # placeholder: buscar do ClickHouse
    try:
        reconcile_with_clickhouse(iceberg_totals, clickhouse_totals)
    except ReconciliationException as exc:
        # Logar e alertar; NAO abortar o billing (billing ja foi gravado no Iceberg).
        log.error("RECONCILIACAO DIVERGENCIA (nao bloqueia billing): %s", exc)

    log.info(
        "run_billing_batch: COMPLETO. %d postings. job_run_id=%s",
        len(postings), job_run_id,
    )


# ---------------------------------------------------------------------------
# Entrypoint CLI
# ---------------------------------------------------------------------------

def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="Billing batch horario (DA-7).")
    p.add_argument(
        "--hour-bucket",
        required=True,
        help="Hora de billing no formato ISO 8601 UTC (ex.: 2026-06-03T14:00:00Z).",
    )
    p.add_argument(
        "--tenant-id",
        default=None,
        help="Tenant especifico ou omitir para processar todos os tenants.",
    )
    p.add_argument(
        "--lookback-days",
        type=int,
        default=7,
        help="Janela de lookback para atribuicao CPA (default: 7 dias, ADR-0002 B.7).",
    )
    p.add_argument(
        "--attribution-model",
        default="last_click",
        choices=["last_click"],
        help="Modelo de atribuicao (Fase 1 = last_click).",
    )
    p.add_argument(
        "--dry-run",
        action="store_true",
        help="Simula o billing sem gravar no Iceberg.",
    )
    p.add_argument(
        "--catalog-uri",
        default=os.environ.get("ICEBERG_CATALOG_URI", "http://localhost:8181"),
    )
    p.add_argument(
        "--db-uri",
        default=os.environ.get("DATABASE_URL", "postgresql://localhost/adserver"),
        help="URI do Postgres (db/ledger/ — money-ledger-guardian).",
    )
    return p.parse_args()


def main() -> None:
    args = parse_args()
    hour_bucket = datetime.fromisoformat(
        args.hour_bucket.replace("Z", "+00:00")
    ).replace(minute=0, second=0, microsecond=0)

    run_billing_batch(
        hour_bucket=hour_bucket,
        tenant_id=args.tenant_id,
        lookback_days=args.lookback_days,
        attribution_model=args.attribution_model,
        dry_run=args.dry_run,
        catalog_uri=args.catalog_uri,
        db_uri=args.db_uri,
    )


if __name__ == "__main__":
    main()
