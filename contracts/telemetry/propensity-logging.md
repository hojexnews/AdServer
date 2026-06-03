# Contrato de logging de propensão & loop de atribuição (TX-1)

> **Status:** Fase 0 (Fundações) — bloqueante (parte de "loop de atribuição **antes** de qualquer ML").
> **Normativo:** `docs/stack-tecnologico.md` §TX-1, §2.3, §4 (Fase 0/2); `docs/documentacao-tecnica.md` §4.7, DA-8.
> **Fio:** `proto/adserver/decision/v1/decision.proto` (`Decision`, `Candidate`, `ExplorationPolicy`) + `proto/adserver/common/v1/envelope.proto`.
> **Decisão relacionada:** [ADR-0001](../../docs/adr/0001-near-real-time-nao-e-requisito-v1.md) (atribuição "ao vivo" vs faturável).

## 1. Por que existe

TX-1 é categórico: `decision_id` + `model_version` **fecham o loop de atribuição**
e **"sem isso não há ML"**. Mas atribuição fechada não basta para **avaliação
off-policy honesta** (OPE): é preciso a **propensão** — a probabilidade com que a
política de logging serviu a ação escolhida. Sem propensão logada **no momento da
decisão**, estimadores IPS/DR ficam enviesados e qualquer "uplift" de ML é
indefensável. Por isso o roadmap lista, já na **Fase 0**, "instrumentação de
`decision_id` + `model_version` + **propensão** nos logs `lg`/`ck`/`ct`".

Este contrato define **o que** logar, **onde**, e **como** os quatro endpoints se
ligam. A **implementação** (motor Go que emite `Decision`; collectors que
preservam o envelope) é **Fase 1** — este documento é o contrato que a Fase 1
implementa.

## 2. Modelo de dois registros (normalizado)

```
                          (hot path, no instante da decisão)
  AdRequest ──► Motor de decisão ──► emite  Decision{decision_id, propensity, candidatos, model_version}
                                              │
                                              ▼  (mesmo decision_id viaja no Envelope)
  lg.php  (Impression) ─┐
  ck.php  (Click)       ├─► cada um carrega Envelope.decision_id  ──► JOIN  Decision  (recompensa ⋈ ação+propensão)
  ct.php  (Conversion) ─┘                                              por decision_id
```

- **`Decision`** (novo, `adserver.decision.v1`) é logado **uma vez por decisão**,
  no hot path, no mesmo span que gera o `decision_id`. É a **fonte de verdade da
  propensão** e do conjunto de candidatos.
- **`Impression`/`Click`/`Conversion`** (`adserver.telemetry.v1`) **não duplicam**
  a propensão: carregam o `Envelope.decision_id` e fazem **join** no `Decision`.
  Normalizado de propósito (propensão pertence à decisão, não ao evento de
  recompensa).

> **Por que não stampar propensão direto em lg/ck/ct?** Propensão é propriedade da
> **decisão**; replicá-la em cada evento downstream convida a divergência. O join
> por `decision_id` é barato no ClickHouse/Iceberg e mantém um único ponto de
> verdade. (Se um consumidor exigir denormalização, ela é derivada do join, nunca
> origem.)

## 3. Obrigações por endpoint (o que a Fase 1 NÃO pode perder)

| Endpoint | Evento | Obrigação de instrumentação |
|----------|--------|------------------------------|
| (decisão) | `Decision` | Emitir com `propensity` ∈ (0,1], `exploration_policy`, `model_version`, `served_tier`, `candidates[]` e `candidate_count`. `event_id` próprio para dedupe (TX-1). |
| `lg.php`  | `Impression`  | Preservar `Envelope.decision_id` **e** `model_version` ponta-a-ponta (vindos da decisão que escolheu o banner). Nunca regenerar/zerar. |
| `ck.php`  | `Click`       | Idem — o `decision_id` é o **mesmo** da impressão originadora (atribuição). |
| `ct.php`  | `Conversion`  | Preservar o `decision_id` do **próprio** evento **e** resolver `attribution_decision_id` (decisão atribuída, dentro da janela) — é o elo que fecha o pCVR. |

**Invariante de propagação:** `decision_id` e `model_version` são **imutáveis** ao
longo de request → impressão → clique → conversão. Um collector que os descarte
**quebra o loop** e deve falhar o teste de contrato da Fase 1.

## 4. Semântica da propensão

- `propensity = P(ação_servida | contexto)` sob a **política de logging** vigente.
- Domínio **(0,1]**. **Positividade** (`> 0`) é obrigatória para toda ação servida
  — é o requisito de *overlap* do OPE. Propensão 0 para algo que foi servido é
  contradição e deve ser rejeitada na validação.
- **Cascata pura (DA-3, sem re-ranker):** `exploration_policy = DETERMINISTIC`,
  `propensity = 1.0` — a ação foi servida com certeza dado o contexto.
- **Fail-open (TX-4):** se o ML estoura o orçamento de 5–8 ms e degrada para a
  cascata, marca-se `ml_fail_open = true`, `DETERMINISTIC`, `propensity = 1.0`.
  Decisões degradadas **não** devem contaminar o treino (filtrar por essa flag).
- `Candidate.propensity` dá a distribuição sobre o estrato elegível; a do
  candidato `served = true` casa com `Decision.propensity`.

## 5. O que isso habilita (OPE)

Com `(decision_id, propensity, model_version)` no `Decision` e a recompensa em
`Click`/`Conversion`, o valor de uma **política nova** `π` é estimável **offline**,
sem A/B ao vivo:

- **IPS** (importance sampling): `V̂(π) = (1/N) Σ (π(a|x)/p(a|x)) · r`, onde
  `p(a|x)` é a propensão logada e `r` a recompensa atribuída.
- **SNIPS** (self-normalized): reduz variância normalizando pelos pesos.
- **DR** (doubly-robust): combina IPS com um modelo de recompensa; robusto se um
  dos dois estiver correto.

Tudo isso é **Fase 2** (`stack §2.3`: "OPE (IPS/DR) + interleaving … dependentes
de logging de propensão"). Este contrato garante que os **dados existam desde a
Fase 1** — não dá para reconstruir propensão depois.

## 6. Privacidade (TX-5 / DA-11)

- `Decision` **não carrega PII** nem IP bruto. `tenant_id` é pseudônimo (envelope);
  `candidates[]` são `campaign_id`/`banner_id`, nunca dados de usuário.
- Contexto de features usado pelo ranker **não** vai cru no log (reservado
  `feature_vector_ref` para uma referência/hash, se necessário) — sem
  reidentificação.
- Vale a redação do OTel Collector antes de qualquer export (TX-5).

## 7. Idempotência & ordenação

- Cada `Decision` tem `event_id` próprio (dedupe TX-1). Reprocessar o mesmo
  `Decision` é efeito único.
- O join é por `decision_id` (chave de negócio), independente de ordem de chegada
  (impressão pode chegar antes ou depois do `Decision` no bus; o sink resolve por
  chave, não por tempo).
- Faturamento reconcilia contra o **lakehouse (Iceberg)**, nunca contra o stream
  (regra de §2.2 / ADR-0001).

## 8. Divisão Fase 0 / Fase 1

| Item | Fase | Onde |
|------|------|------|
| Schema `Decision`/`Candidate`/`ExplorationPolicy` + este contrato | **0** | `proto/adserver/decision/v1/`, este arquivo |
| `decision_id`/`model_version` no Envelope (já existem) | **0** | `proto/adserver/common/v1/envelope.proto` |
| Motor Go emitir `Decision` no hot path | **1** | serviço de delivery |
| Collectors lg/ck/ct preservarem o envelope ponta-a-ponta | **1** | collectors |
| `StatsHourly` + visão "ao vivo" (ClickHouse) sobre o join | **1** | ClickHouse MVs (ADR-0001) |
| Estimadores OPE (IPS/SNIPS/DR) | **2** | pipeline de avaliação ML |
