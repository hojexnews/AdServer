# ADR-0001 — Near-real-time (1–5s) não é requisito de v1/v2; frescor "ao vivo" vem do ClickHouse, faturável continua batch horário

> **Status:** Aceito · **Data:** 2026-06-03 · **Decisores:** Arquitetura + Produto
> **Âncoras:** DA-7 (batch horário / não-RTB), DA-4 (pacing), TX-1, TX-6; `docs/stack-tecnologico.md` §2.2, §2.3, §5, §6 (q.4)
> **Supersede:** — · **Substituído por:** —

## Contexto

A Fase 0 registrou uma **pendência bloqueadora**: *frescor near-real-time (1–5s)
é mesmo requisito?* (`docs/stack-tecnologico.md` §2.2, §5, §6 q.4). Ela trava o
**eixo de stream processing** inteiro — adotar ou não **Flink stateful** — e,
por arrasto, decisões sobre fraude streaming, atribuição longa near-real-time e
o custo operacional do v1.

Forças em jogo:

- **DA-7 é normativo:** "agregação estatística em **batch horário** (não-RTB)",
  escolha consciente que viabiliza operação em hardware acessível. **CA-6**
  exige defasagem ≤ 1h e proíbe atualização "milissegundo a milissegundo".
- **Princípio condutor do design:** "começar enxuto e correto; cada tecnologia
  pesada entra **sob medição**, nunca por aspiração de escala". Flink, no próprio
  stack doc, está na coluna "escala futura (sob prova)".
- **Confusão a desfazer:** "dashboard fresco" e "stream processing stateful" são
  **coisas diferentes**. Tratá-las como sinônimo é o que tornava a pergunta
  bloqueante. Esta ADR as separa.
- **Atribuição dupla (já decidida, §2.2):** número **ao vivo** (janela curta) ≠
  número **fechado/faturável** (batch sobre Iceberg, lookback completo). A UI
  rotula "consolidado ≤1h" vs "ao vivo" e **nunca soma** as duas fontes.

## Decisão

**Adotamos que near-real-time stateful (Flink, frescor 1–5s) NÃO é requisito de
v1 nem de v2.** A pergunta é resolvida desmembrando-a em dois eixos:

### 1. Frescor de dashboard ("ao vivo") — atendido SEM Flink

O número "ao vivo" é servido por **rollups incrementais do ClickHouse**
(`AggregatingMergeTree`/`SummingMergeTree` com `uniqState`/HLL, materializados na
ingestão direta via Kafka engine a partir do Redpanda). Isso entrega frescor de
**segundos a poucos minutos** nos painéis como **subproduto da ingestão** — sem
nenhum processador de stream stateful (Flink) e sem violar DA-7, porque:

- O número "ao vivo" é **explicitamente não-faturável**, rotulado como tal na UI.
- O número **faturável/consolidado** continua sendo o **batch horário** (DA-7),
  reconciliado contra o **lakehouse (Iceberg)**, nunca contra o streaming.
- A UI **nunca soma** "ao vivo" + "consolidado" (regra de §2.2 / risco §5).

### 2. Stream processing stateful (Flink) — DEFERIDO, com gatilho explícito

Flink (atribuição longa near-real-time + fraude streaming) fica **fora de v1 e
v2**. Fraude/IVT (TX-6) roda na **ingestão**, antes do `StatsHourly` e do
faturamento — latência de minutos é aceitável porque o faturamento é horário.
Pacing (DA-4) é **controlador proporcional** por déficit vs. cronograma,
alimentado por forecast de tráfego leve; **tolera latência de minutos** e não
exige telemetria de 1–5s.

### Fora do escopo desta decisão

Não decide sobre RTB/bidding (permanece fora de escopo, DA-7 §2.2 da doc
técnica), nem sobre janelas de atribuição click→conversão (ADR futuro, §6 q.7).

## Gatilho de reabertura

Reabrir esta decisão (e então justificar Flink) **somente** quando **qualquer**
condição mensurável abaixo for observada em produção — não por aspiração:

1. **SLO de frescor contratual** que exija sinal **mais fresco que a latência das
   MVs do ClickHouse** (alvo de referência: < 5 s ponta-a-ponta) **e** que o
   atraso atual cause prejuízo material — p.ex. pacing (DA-4) **estourar orçamento
   de forma material dentro da janela de lag** do ClickHouse, comprovado por dados.
2. **Atribuição de janela longa** (click→conversão multi-dia) em que refazer o
   join como **batch sobre Iceberg** fique comprovadamente **mais caro/lento** que
   um join stateful incremental (medir custo do re-batch antes de adotar Flink).
3. **Fraude/IVT que precise bloquear dentro do ciclo de vida do request**
   (e não na ingestão horária), com perda de receita demonstrada pelo atraso atual.

Cada gatilho exige **número medido** anexado ao ADR sucessor. Sem isso, mantém-se
o caminho sem Flink.

## Alternativas consideradas

- **Adotar Flink já no v1 ("near-real-time por garantia").** Rejeitada: contraria
  DA-7 e o princípio "sob medição"; adiciona um motor stateful (operação, custo,
  reconciliação dupla) sem requisito comprovado. É o anti-padrão de
  over-engineering listado no risco §5.
- **Negar qualquer frescor abaixo de 1h (batch horário puro, sem "ao vivo").**
  Rejeitada: o design já prevê o número "ao vivo" via ClickHouse (§2.2) e a UI
  dual-rotulada; negar isso piora a UX do anunciante sem ganho (o frescor do
  ClickHouse é praticamente gratuito, vem da ingestão).
- **Deixar a pendência em aberto até "o dono do produto decidir".** Rejeitada: era
  exatamente o que travava o eixo streaming. A decisão é derivável das normas já
  aprovadas (DA-7) + do design de atribuição dupla; deixá-la aberta bloqueava
  Fase 1 sem necessidade.

## Consequências

- **Positivas:**
  - **Desbloqueia a Fase 1** sem esperar decisão externa: o pipeline de telemetria
    é `collector → Redpanda → ClickHouse (MV rollups: StatsHourly + visão "ao
    vivo") → Iceberg (verdade contábil, billing batch)`. **Sem Flink.**
  - Mantém o v1 **enxuto** e aderente a DA-7/CA-6; hardware acessível preservado.
  - Dá um **critério objetivo** (gatilho mensurável) para reintroduzir Flink,
    eliminando discussões recorrentes "precisamos ou não de streaming?".
- **Negativas / custos aceitos:**
  - Atribuição de janela longa fica em **batch** (sobre Iceberg) no v1/v2; se uma
    necessidade real de near-real-time surgir, há retrabalho para introduzir Flink
    (mitigado: o contrato de eventos TX-1 já carrega `decision_id`/`occurred_at`,
    então o pipeline streaming futuro consome o **mesmo** schema sem migração).
  - O número "ao vivo" pode divergir temporariamente do consolidado — **aceito e
    sinalizado** pela regra de UI (rotular e nunca somar).
- **Impacto por fase do roadmap:**
  - **Fase 1:** entrega ClickHouse com MVs (StatsHourly + "ao vivo"), Iceberg como
    fonte de verdade, billing batch. Risco §5 "Near-real-time vs DA-7" → **fechado**.
  - **Fase 2:** a linha "Flink incremental **se** near-real-time confirmado" passa a
    ler: **não confirmado**; só entra sob o gatilho acima.
  - **Fase 3:** fraude não-supervisionada e OPE continuam batch/ingestão salvo
    gatilho. Streaming permanece upgrade justificado por dados.
