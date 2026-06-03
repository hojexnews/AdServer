# `contracts/telemetry/`

Contratos de **Fase 0** do **loop de atribuição** — a instrumentação que precisa
existir **antes** de qualquer ML (TX-1). Aqui vive o contrato de **logging de
propensão** que liga a decisão de veiculação às recompensas (clique/conversão) e
habilita avaliação off-policy honesta.

## Conteúdo

| Caminho | O que é |
|---------|---------|
| [`propensity-logging.md`](propensity-logging.md) | Contrato do **decision log** + propensão: modelo de dois registros (`Decision` ⋈ lg/ck/ct por `decision_id`), semântica de propensão (positividade, fail-open), o que cada endpoint deve preservar, e o que habilita (IPS/SNIPS/DR). Divide Fase 0 (schema/contrato) de Fase 1 (instrumentação Go). |

## Relação com `proto/`

- O **decision log** no fio é `adserver.decision.v1.Decision` (+ `Candidate`,
  `ExplorationPolicy`), em `proto/adserver/decision/v1/decision.proto`.
- O `decision_id`/`model_version` que costuram tudo vivem no
  `adserver.common.v1.Envelope`.
- Os eventos de recompensa são `adserver.telemetry.v1.{Impression,Click,Conversion}`.

## Princípio

> **Sem propensão logada no instante da decisão, não há OPE honesto.**
> `decision_id` fecha a atribuição; **propensão** torna a avaliação off-policy
> defensável. Ambos têm de existir desde a Fase 1 — não se reconstrói propensão
> depois do fato.
