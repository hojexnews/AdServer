"""
ml/pacing/controller.py
========================
Controlador proporcional de pacing (DA-4).

DECISAO DE ARQUITETURA — POR QUE PROPORCIONAL (NAO PID/RL/MPC):
  DA-4 e ADR-0003 §G J6 exigem "controlador proporcional com forecast de
  trafego leve". PID so sob oscilacao observada; RL/MPC sao proibidos no
  hot path (ADR-0003 §2.3 — opacidade e risco financeiro). Este modulo
  implementa APENAS o termo proporcional:

    pacing_adjustment = Kp * deficit_normalizado

  onde deficit_normalizado = (entrega_esperada - entrega_real) / meta_total.

  Ganho proporcional Kp = 1.0 por padrao: ajuste identico ao deficit.
  Sem termo integral (evita wind-up em campanhas iniciando fora do ritmo)
  e sem termo derivativo (amplifica ruido de contadores aproximados).

  Quando o deficit for zero ou negativo (campanha adiantada), o ajuste e
  zero (sem acelerador automatico — respeita DA-4: sem over-delivery).

  IMPORTANTE: este controlador e computacao OFFLINE/NEAR-LINE. A saida
  (pacing_deficit float [0,1]) e escrita no snapshot/Redis de pacing via
  o estado near-line de campanhas existente. Este modulo NAO cria store
  novo — apenas produz o numero que o motor de decisao ja sabe consumir
  (campo cascade.Candidate.PacingDeficit, feature_spec.yaml index 12).

FORECAST DE TRAFEGO LEVE:
  Para prever a entrega esperada ate o fim do voo de campanha, usamos um
  forecast de trafego baseado nos agregados de StatsHourly (ClickHouse,
  read-only). O modelo e simples: media movel ponderada das ultimas N horas
  de impressoes por campanha/zona como proxy de trafego. Nao requer treino
  de modelo — e baseline estatistico sobre `StatsHourly`.

  ADR-0003 §D: StatsHourly serve forecast leve e baseline Monte Carlo do
  copiloto — NUNCA como verdade de treino (sem somar "ao vivo" com faturavel).

DINHEIRO (TX-2):
  pacing_deficit e um ratio adimensional [0,1] derivado de contadores de
  impressoes (int64). Nenhum valor monetario (bid, eCPM, receita) e
  computado aqui. TX-2 nao se aplica a ratios de entrega.

PRIVACIDADE (TX-5/DA-11):
  Nenhuma feature de usuario entra neste controlador. Todas as entradas
  sao metricas de campanha (goal, delivered — agregados sem PII).
"""
from __future__ import annotations

import math
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Dict, List, Optional, Sequence


# ---------------------------------------------------------------------------
# Tipos de entrada/saida
# ---------------------------------------------------------------------------

@dataclass
class CampaignPacingState:
    """
    Estado de pacing de uma campanha no instante atual.

    Campos obrigatorios para o calculo proporcional:
      - campaign_id:          identificador da campanha
      - goal_impressions:     meta total de impressoes do voo (int64)
      - delivered_impressions: impressoes entregues ate agora (int64)
      - flight_start_utc:     inicio do voo (datetime UTC)
      - flight_end_utc:       fim do voo (datetime UTC)
      - now_utc:              instante atual (datetime UTC); padrao = utcnow()

    Campos opcionais para o forecast de trafego:
      - forecast_hourly_impressions: lista de impressoes por hora (historico
        recente, ordem cronologica). Obtida de StatsHourly (ClickHouse).
        Se None, o forecast degrada para linear uniforme (Kp puro).

    NOTA TX-2: goal_impressions e delivered_impressions sao int64 (contadores
    de impressoes). Nenhum campo monetario aqui.
    """
    campaign_id: str
    goal_impressions: int          # int64 — meta total de impressoes
    delivered_impressions: int     # int64 — entregas ate agora
    flight_start_utc: datetime
    flight_end_utc: datetime
    now_utc: Optional[datetime] = None
    # Historico horario de impressoes (StatsHourly, ordem cronologica)
    # Usado pelo forecast leve para projetar entrega futura
    forecast_hourly_impressions: Optional[List[int]] = None


@dataclass
class PacingAdjustment:
    """
    Saida do controlador proporcional para uma campanha.

    pacing_deficit e o numero que alimenta o snapshot/Redis de pacing.
    O motor de decisao (cascade) le este campo como
    cascade.Candidate.PacingDeficit (feature_spec.yaml index 12).

    pacing_deficit em [0.0, 1.0]:
      0.0 = campanha no ritmo ou adiantada (sem urgencia)
      1.0 = campanha totalmente nao-entregue (maxima urgencia)

    NUNCA float para valores monetarios (TX-2). pacing_deficit e ratio
    adimensional de entrega, nao valor monetario.
    """
    campaign_id: str
    pacing_deficit: float          # [0.0, 1.0] — adimensional, nao monetario
    adjustment_factor: float       # multiplicador sugerido para bid modifier
    expected_delivery_fraction: float  # fracao esperada de entrega no tempo decorrido
    actual_delivery_fraction: float    # fracao real de entrega
    forecast_remaining_impressions: Optional[int]  # previsao de impressoes restantes
    computed_at_utc: str           # ISO 8601 do instante do calculo


# ---------------------------------------------------------------------------
# Forecast leve de trafego (sobre StatsHourly)
# ---------------------------------------------------------------------------

def forecast_remaining_impressions(
    hourly_history: Sequence[int],
    hours_remaining: float,
    window: int = 24,
) -> int:
    """
    Previsao simples de impressoes nas proximas `hours_remaining` horas
    com base no historico recente de StatsHourly.

    Metodo: media ponderada exponencial das ultimas `window` horas,
    com peso maior para as horas mais recentes (decaimento linear).

    Retorna: impressoes previstas (int, minimo 0).

    NOTA: NAO e modelo ML — e baseline estatistico sobre StatsHourly.
    Usado apenas para ajustar a entrega esperada no calculo de deficit.
    ADR-0003 §D: StatsHourly e fonte de forecast/baseline, nunca de treino.
    """
    if not hourly_history:
        return 0

    # Usa ultimas `window` observacoes
    obs = list(hourly_history[-window:])
    n = len(obs)
    if n == 0:
        return 0

    # Pesos lineares decrescentes (mais recente = peso maior)
    weights = [i + 1 for i in range(n)]  # [1, 2, ..., n]
    total_weight = sum(weights)
    weighted_mean = sum(w * v for w, v in zip(weights, obs)) / total_weight

    # Projeta para as horas restantes
    return max(0, int(round(weighted_mean * hours_remaining)))


# ---------------------------------------------------------------------------
# Controlador proporcional
# ---------------------------------------------------------------------------

class ProportionalPacingController:
    """
    Controlador proporcional de pacing (DA-4).

    Calcula o pacing_deficit de cada campanha no instante atual
    com base no deficit entre entrega esperada e real no tempo
    decorrido do voo.

    IMPLEMENTACAO INTENCIONAL COMO PROPORCIONAL (nao PID/RL/MPC):
      - Termo P: ajuste = Kp * deficit_normalizado
      - Sem termo I: evita wind-up em campanhas com inicio fora do ritmo
      - Sem termo D: reduz amplificacao de ruido de contadores aproximados
      - PID so seria adicionado sob oscilacao observada (DA-4, ADR-0003)
      - RL/MPC sao proibidos no hot path por opacidade e risco financeiro

    O controlador e executado OFFLINE/NEAR-LINE (nao no hot path de decisao).
    A saida e escrita no snapshot/Redis de pacing que o motor de decisao ja
    consome — este modulo nao cria store novo.
    """

    def __init__(self, kp: float = 1.0, max_adjustment: float = 2.0):
        """
        Args:
            kp:             Ganho proporcional (padrao 1.0). Aumentar para
                            reagir mais rapido ao deficit; diminuir para
                            resposta mais suave. Nao alterar sem medicao de
                            oscilacao.
            max_adjustment: Teto do fator de ajuste (bid modifier). Evita
                            over-delivery agressivo. Padrao 2.0 = doublar o
                            bid max em caso de deficit total.
        """
        if kp <= 0:
            raise ValueError(f"kp deve ser positivo; recebido: {kp}")
        if max_adjustment <= 1.0:
            raise ValueError(f"max_adjustment deve ser > 1.0; recebido: {max_adjustment}")
        self.kp = kp
        self.max_adjustment = max_adjustment

    def compute(self, state: CampaignPacingState) -> PacingAdjustment:
        """
        Calcula o pacing_deficit e o fator de ajuste para uma campanha.

        Algoritmo proporcional:
          1. Calcula fracao de tempo decorrida no voo: elapsed_fraction
          2. Calcula entrega esperada ate agora: expected = goal * elapsed_fraction
          3. Calcula deficit: deficit = (expected - delivered) / goal
             Se deficit <= 0 (adiantada): deficit = 0.0
          4. pacing_deficit = clip(deficit, 0.0, 1.0)
          5. adjustment_factor = 1.0 + Kp * pacing_deficit
             Capeado em max_adjustment.

        Com forecast disponivel (hourly_history):
          - Projeta impressoes restantes nas horas restantes
          - Se a projecao + delivered < goal, aumenta o deficit
            proportionalmente (pressao adicional de urgencia)

        Retorna PacingAdjustment com pacing_deficit em [0, 1].
        """
        now = state.now_utc or datetime.now(timezone.utc)

        # Normaliza datetimes para UTC se necessario
        start = _ensure_utc(state.flight_start_utc)
        end = _ensure_utc(state.flight_end_utc)
        now_tz = _ensure_utc(now)

        flight_duration_s = (end - start).total_seconds()
        if flight_duration_s <= 0:
            # Voo invalido ou expirado: sem ajuste
            return _zero_adjustment(state.campaign_id)

        elapsed_s = (now_tz - start).total_seconds()
        elapsed_fraction = max(0.0, min(1.0, elapsed_s / flight_duration_s))

        goal = max(1, state.goal_impressions)
        delivered = max(0, state.delivered_impressions)

        # Fracao de entrega real e esperada
        actual_fraction = delivered / goal
        expected_fraction = elapsed_fraction  # baseline linear uniforme

        # Deficit bruto (quanto falta em relacao ao ritmo esperado)
        raw_deficit = expected_fraction - actual_fraction

        # Ajuste com forecast de trafego (se disponivel)
        hours_remaining = max(0.0, (end - now_tz).total_seconds() / 3600.0)
        forecast_remaining: Optional[int] = None
        if state.forecast_hourly_impressions and hours_remaining > 0:
            forecast_remaining = forecast_remaining_impressions(
                state.forecast_hourly_impressions,
                hours_remaining,
            )
            # Se a projecao indica sub-entrega total: amplifica deficit
            projected_total = delivered + forecast_remaining
            if projected_total < goal:
                # Pressao de urgencia: deficit extra proporcional ao gap projetado
                projected_deficit = (goal - projected_total) / goal
                # Media ponderada: 60% deficit corrente + 40% deficit projetado
                raw_deficit = 0.6 * raw_deficit + 0.4 * projected_deficit

        # pacing_deficit: ratio adimensional [0, 1] (nao monetario — TX-2 n/a)
        pacing_deficit = float(max(0.0, min(1.0, raw_deficit)))

        # Fator de ajuste proporcional (bid modifier sugerido)
        # INTENCIONAL: sem integral, sem derivativo (ver docstring da classe)
        adjustment_factor = min(self.max_adjustment, 1.0 + self.kp * pacing_deficit)
        if pacing_deficit == 0.0:
            adjustment_factor = 1.0  # sem urgencia: sem ajuste

        return PacingAdjustment(
            campaign_id=state.campaign_id,
            pacing_deficit=pacing_deficit,
            adjustment_factor=adjustment_factor,
            expected_delivery_fraction=expected_fraction,
            actual_delivery_fraction=actual_fraction,
            forecast_remaining_impressions=forecast_remaining,
            computed_at_utc=now_tz.isoformat(),
        )

    def compute_batch(
        self,
        states: Sequence[CampaignPacingState],
    ) -> Dict[str, PacingAdjustment]:
        """
        Calcula pacing_deficit para uma lista de campanhas.

        Retorna dict campaign_id -> PacingAdjustment.
        Uso tipico: chamado pelo job near-line que atualiza o snapshot/Redis.
        """
        return {s.campaign_id: self.compute(s) for s in states}


# ---------------------------------------------------------------------------
# Funcoes auxiliares
# ---------------------------------------------------------------------------

def _ensure_utc(dt: datetime) -> datetime:
    """Garante que um datetime e timezone-aware (UTC). Se naive, assume UTC."""
    if dt.tzinfo is None:
        return dt.replace(tzinfo=timezone.utc)
    return dt


def _zero_adjustment(campaign_id: str) -> PacingAdjustment:
    """Retorna ajuste zero para campanha sem voo valido."""
    return PacingAdjustment(
        campaign_id=campaign_id,
        pacing_deficit=0.0,
        adjustment_factor=1.0,
        expected_delivery_fraction=0.0,
        actual_delivery_fraction=0.0,
        forecast_remaining_impressions=None,
        computed_at_utc=datetime.now(timezone.utc).isoformat(),
    )


# ---------------------------------------------------------------------------
# Leitor de StatsHourly (ClickHouse, read-only)
# ---------------------------------------------------------------------------

def load_hourly_history_from_stats(
    campaign_id: str,
    clickhouse_client,  # qualquer cliente compativel com .execute()
    lookback_hours: int = 48,
    zone_id: Optional[str] = None,
) -> List[int]:
    """
    Le o historico horario de impressoes de uma campanha do StatsHourly
    (ClickHouse, read-only).

    Retorna lista de impressoes por hora em ordem cronologica.
    Se o ClickHouse nao estiver disponivel, retorna [] (fail-open:
    o controlador usa baseline linear).

    NOTA (ADR-0003 §D): StatsHourly e APENAS para forecast/baseline.
    Nunca usar como verdade de treino — a verdade de treino e o Iceberg.

    Args:
        campaign_id:      ID da campanha
        clickhouse_client: cliente ClickHouse (ex: clickhouse_driver.Client)
        lookback_hours:   janela de historico para o forecast (padrao 48h)
        zone_id:          se fornecido, filtra por zona especifica

    Retorna:
        Lista de int (impressoes por hora, ordem cronologica).
    """
    try:
        # SQL injection defense (gate de segurança): TODO os valores vão por
        # binding parametrizado, inclusive zone_id. Nunca interpolar valor de
        # entrada via f-string no SQL. lookback_hours é forçado a int.
        params = {"campaign_id": campaign_id, "lookback_hours": int(lookback_hours)}
        zone_filter = ""
        if zone_id:
            zone_filter = "AND zone_id = %(zone_id)s"
            params["zone_id"] = zone_id
        sql = f"""
            SELECT
                toStartOfHour(hour_bucket) AS h,
                SUM(impressions) AS imps
            FROM adserver.stats_hourly
            WHERE campaign_id = %(campaign_id)s
              AND hour_bucket >= now() - INTERVAL %(lookback_hours)s HOUR
              {zone_filter}
            GROUP BY h
            ORDER BY h ASC
        """
        rows = clickhouse_client.execute(sql, params)
        return [int(r[1]) for r in rows]
    except Exception:
        # Fail-open: sem historico, o controlador usa baseline linear
        return []


# ---------------------------------------------------------------------------
# Snapshot writer: escreve pacing_deficit no Redis (interface near-line)
# ---------------------------------------------------------------------------

PACING_REDIS_KEY_PREFIX = "pacing:deficit:"


def write_pacing_deficits_to_redis(
    adjustments: Dict[str, PacingAdjustment],
    redis_client,  # qualquer cliente Redis compativel com .set()
    ttl_seconds: int = 300,
) -> None:
    """
    Escreve os pacing_deficits calculados no Redis de pacing near-line.

    CONTRATO DE ESCRITA:
      Chave:  pacing:deficit:{campaign_id}
      Valor:  string representando float [0.0, 1.0] com 6 casas decimais
      TTL:    ttl_seconds (padrao 5 min — recalculado a cada ciclo near-line)

    O motor de decisao (internal/ranker, cascade) le este valor para
    popular cascade.Candidate.PacingDeficit (feature_spec.yaml index 12).
    ESTE MODULO NAO CRIA STORE NOVO — apenas escreve no Redis existente.

    Fail-open: se o Redis nao estiver disponivel, loga o erro e prossegue.
    O motor de decisao usa o valor anterior (TTL garante expiracao segura).
    """
    for campaign_id, adj in adjustments.items():
        key = f"{PACING_REDIS_KEY_PREFIX}{campaign_id}"
        value = f"{adj.pacing_deficit:.6f}"
        try:
            redis_client.set(key, value, ex=ttl_seconds)
        except Exception as exc:
            # Fail-open: nao interrompe o loop por falha de Redis
            print(f"[pacing] AVISO: falha ao escrever Redis key={key}: {exc}")
