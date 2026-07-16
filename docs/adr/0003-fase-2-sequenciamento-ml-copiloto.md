# ADR-0003 — Sequenciamento da Fase 2 (otimização por ML + copiloto), layout e perguntas abertas remanescentes

> **Status:** Aceito · **Data:** 2026-06-04 · **Decisores:** Arquiteto-chefe / Tech Lead
> **Âncoras:** TX-1, TX-2, TX-3, TX-4, TX-5 · DA-3, DA-4, DA-6, DA-7, DA-11 · CA-1…CA-9 · `docs/stack-tecnologico.md` §2.2, §2.3, §2.4, §2.5, §4 (roadmap, linha "Fase 2"), §6 (q.5) · `contracts/telemetry/propensity-logging.md` · ADR-0001, ADR-0002
> **Supersede:** — · **Substituído por:** —

## Contexto

A Fase 1 (MVP de paridade) está **código-completa** na `main` (I0…I4 + gate de
paridade; gates `security`/`privacy` verdes). O que resta da Fase 1 é **não-código**
(aplicar `platform/` em cloud, Docker/MaxMind, shadow-traffic + dual-run contábil
contra o Revive legado) e está bloqueado neste ambiente. O próximo passo
**executável** é abrir a **Fase 2 — Otimização por ML + copiloto**
(`docs/stack-tecnologico.md §4`, linha "Fase 2").

A Fase 2 entrega, segundo o roadmap e §2.2/§2.3/§2.4:

- **Ranking por ML** (§2.3): `pCTR` em **LightGBM** (CatBoost p/ alta cardinalidade),
  servido **in-process/sidecar** via **Treelite/ONNX Runtime (CPU)** como
  **re-ranker dentro do estrato** (DA-3/TX-4), com **calibração isotônica**
  monitorada (ECE) porque `eCPM = p × rate`; **bandits** (Thompson/LinUCB sobre
  o ranker calibrado, epsilon-greedy decrescente no MVP); `pCVR` **só após
  atribuição confiável**.
- **Pacing por controle clássico** (DA-4, §2.3): **controlador proporcional** por
  déficit vs. cronograma com forecast de tráfego leve. PID só sob oscilação
  observada; RL/MPC **descartados** no hot path.
- **Fraude/IVT supervisionada na ingestão** (TX-6): regras + GBDT supervisionado
  antes do `StatsHourly` e do faturamento (latência de minutos é aceitável; ADR-0001).
- **Copiloto do anunciante** (§2.4): **Claude** roteado (Haiku 4.5 inline /
  Sonnet 4.6 padrão / Opus 4.8 premium) via **LangGraph** com **HITL obrigatório
  em toda escrita**, ferramentas **tipadas server-side**, **RAG pgvector com RLS
  por tenant** + teste de isolamento, **proveniência C2PA/SynthID**, **Langfuse
  self-hosted**, **chave Claude protegida pelo BFF**.
- **MLOps** (§2.3): **MLflow** (tracking + registry com promoção auditável),
  **uma única função de featurização** treino↔serving (anti-skew), **shadow + A/B
  por zona/tenant com guarda de receita e kill-switch**, **OPE (IPS/DR)** sobre o
  logging de propensão.

Forças em jogo que **exigem um gate antes do fan-out** (mesma razão do ADR-0002):

- **A cascata é a autoridade final (DA-3).** O ML entra **dentro** do hot path que a
  Fase 1 já travou. Sem um ponto de extensão ratificado, o `ml-optimization-engineer`
  e o `decision-engine-engineer` colidem na assinatura do re-ranker, no orçamento de
  latência e na fronteira fail-open. Qualquer design que deixe o ML **atravessar
  estratos** ou virar caminho-padrão (sem fail-open) está errado e corrompe a
  semântica contábil que os golden tests da Fase 1 protegem.
- **O loop de atribuição já foi instrumentado na Fase 0/1.** `decision.proto`
  (`Decision`/`Candidate`/`ExplorationPolicy`) e `contracts/telemetry/propensity-logging.md`
  **já existem e estão verdes**: `propensity` ∈ (0,1], `exploration_policy`,
  `model_version`, `ml_fail_open`. **O ML não começa sem esse logging instrumentado
  no hot path** — é pré-requisito de OPE/IPS/DR (sem propensão logada no instante da
  decisão, "uplift" é indefensável). A Fase 2 **consome** esse contrato; não o
  reabre.
- **Onde o ML, o copiloto e o RAG vivem no monorepo** não está decidido. O ADR-0002
  **reservou** 5–8 ms p99 para ML (B.2) e antecipou "ML entra como `internal/` +
  sidecar dentro do budget" (Consequências, Fase 2), mas não ratificou a árvore. Sem
  isso, três engenheiros (`ml-optimization-engineer`, `copilot-llm-engineer`,
  `frontend-bff-engineer`) colidem em fronteiras de diretório/linguagem (Python de ML
  vs. Go do hot path vs. TS do BFF; onde fica o sidecar; onde vive o LangGraph).
- **Uma pergunta de §6 ainda bloqueia a Fase 2:** q.5 — **identidade cookieless sob
  GDPR+LGPD sem PII central**, que impacta o fail-safe DA-6 (fill rate) e a base de
  features do ranker. Em modo autônomo, recebe **default recomendado + gatilho de
  reversão mensurável** (estilo ADR-0002 §B).

Este ADR é **pré-requisito do fan-out da Fase 2**: trava o ponto de extensão do ML
na cascata, o layout dos novos diretórios, a resolução de q.5 + sub-decisões de
estrutura, e os gates de promoção — **antes da primeira linha de modelo, sidecar ou
grafo de copiloto**. Não escreve hot path, modelo nem copiloto linha a linha.

## Decisão

### A. Ponto de extensão do ML na cascata — re-ranker DENTRO do estrato, fail-open por padrão

**Adotamos o ML como re-ranker invocado pelo motor de decisão DENTRO do estrato
vencedor da cascata (DA-3), nunca como seletor de estrato.** A ordem de autoridade
`Override > Contract > Remnant > impressão em branco` é resolvida **primeiro**, pela
cascata pura da Fase 1; só **depois** o ML re-ordena os candidatos **daquele estrato**
(o `Candidate.tier` já fixa que o re-ranking só compara candidatos do **mesmo**
estrato — `decision.proto`). Invariantes não-negociáveis:

- **TX-4 — orçamento:** o re-ranker roda dentro da **reserva de 5–8 ms p99** já
  declarada no ADR-0002 §B.2 (a reserva **existe**; este ADR não reabre o budget).
  **Timeout duro + fail-open determinístico:** se o ML estourar/falhar, a decisão
  **degrada para a cascata pura** (resultado idêntico ao da Fase 1), marca
  `ml_fail_open = true`, `exploration_policy = DETERMINISTIC`, `propensity = 1.0`.
  **Nunca** impressão em branco por falha de ML.
- **DA-3 — autoridade final:** o ML **jamais** muda o estrato vencedor nem promove
  um Remnant sobre um Contract. O contrato de extensão entre `internal/cascade` e o
  ranker é: a cascata entrega o **conjunto elegível do estrato vencedor**; o ranker
  devolve uma **ordenação** desse conjunto; se a ordenação for inválida/atrasada,
  ignora-se e usa-se a ordem determinística da cascata.
- **Semântica contábil intacta:** o ML **não** altera o que é faturável nem a
  semântica da cascata. **Os golden tests da Fase 1 continuam verdes** com o ML
  desligado E com o ML ligado em modo fail-open (a degradação reproduz a cascata
  pura bit-a-bit). O `parity-golden-test-guardian` é o dono dessa verificação.
- **Logging de propensão é pré-requisito, não entregável novo:** o contrato
  `propensity-logging.md` + `decision.proto` **já existem** (Fase 0). A Fase 2
  apenas **instrumenta** o motor para preencher `propensity`/`exploration_policy`/
  `epsilon`/`candidates[]` quando o re-ranker estiver ativo — antes de treinar
  qualquer modelo com OPE.

### B. Estrutura interna do ML — `internal/ranker` (cliente Go) + sidecar CPU via Unix socket; treino em `ml/`

Ratificamos o layout antecipado no ADR-0002 (Consequências, Fase 2: "ML entra como
`internal/` + sidecar dentro do budget B.2"):

- **Cliente in-process Go:** `internal/ranker/` — o ponto de extensão que
  `services/decision` chama. É **Go puro** (sem CGO no hot path), responsável por:
  montar o vetor de features (via a **função de featurização única**, ver D),
  conversar com o sidecar de serving e aplicar **timeout duro + fail-open** (A).
  Vive em `internal/` porque é compartilhado e **não-exportável** — coerente com a
  fronteira do ADR-0002.
- **Sidecar de serving (CPU):** processo **co-localizado** (mesmo pod/host) que carrega
  o GBDT compilado (**Treelite** nativo ou **ONNX Runtime**) e responde por **Unix
  domain socket** (não TCP de rede) — latência previsível dentro de 5–8 ms, **sem
  Triton, sem GPU** (deep é Fase 3 sob uplift A/B). O sidecar é o **único** processo
  não-Go no hot path e existe para isolar o runtime do modelo do binário Go. Decisão
  Treelite-vs-ONNX fica com o `ml-optimization-engineer` (ambos cabem no budget; é
  escolha de runtime, não de arquitetura — não precisa de ADR).
- **Treino / featurização / OPE / MLflow:** novo diretório de topo **`ml/`**
  (Python), irmão de `data/` e fora do módulo Go. Coerente com o precedente já no
  repo: o billing batch Python vive em `data/iceberg/jobs/`; o ML ganha seu próprio
  topo porque é um domínio distinto (treino, calibração, OPE, registry) e não é
  pipeline de dados puro.

**Por que sidecar via Unix socket e não in-process puro (CGO/Treelite linkado):**
isola o runtime do modelo (crash/leak do ONNX não derruba o motor Go), permite
**hot-reload de versão de modelo** sem redeploy do hot path, e mantém o binário Go
livre de CGO (build hermético do ADR-0002 §C preservado). Custo aceito: um salto
de IPC por decisão — medido e contido no budget de 5–8 ms (UDS local é da ordem de
dezenas de µs).
- **Gatilho de reversão:** se o salto de IPC pelo socket consumir parcela
  **material** do budget (alvo de referência: **> 2 ms p99** só no transporte
  ranker↔sidecar, medido) de forma sustentada, abre-se ADR para avaliar **Treelite
  linkado in-process** (CGO) **apenas para o ranker**, medindo o impacto no build
  hermético antes de adotar. Sem o número medido, mantém-se o sidecar.

### C. Onde vive o copiloto — gateway Python LangGraph (`services/copilot`) atrás do BFF TS

**Adotamos um serviço Python dedicado para o copiloto (`services/copilot/`,
LangGraph), com o BFF Node/TS (ADR-0002 §B.6) como única fronteira voltada ao
cliente.** O contorno é:

```
console (Next.js, Vercel AI SDK)  ──►  bff (TS, tRPC)  ──►  services/copilot (Python, LangGraph)
                                         │                         │
                                  protege a chave Claude     orquestra grafo + HITL +
                                  injeta tenant_id           ferramentas tipadas server-side
                                  (gateway de ACL)           RAG pgvector (RLS) · Langfuse
```

- **O BFF protege a chave Claude e injeta `tenant_id`** (TX-3): a chave **nunca**
  chega ao front nem ao LLM; o BFF é o **gateway de autorização server-side** que
  ignora instruções do payload e injeta segredos/tenant. O front (Vercel AI SDK v5,
  §2.5) faz streaming SSE **através do BFF**, nunca direto à API da Anthropic.
- **`services/copilot` (Python) orquestra o LangGraph:** checkpointing, durable
  execution, **HITL obrigatório em toda escrita** ("aplicar 1-clique" = PATCH
  validado por Zod no BFF + preview de diff + aprovação humana — nada publicado
  autonomamente), ferramentas **tipadas** (Pydantic/JSON Schema). O LangGraph é um
  ecossistema Python; reescrevê-lo em TS seria atrito sem ganho. **O LLM nunca recebe
  credencial** — atua só por ferramentas server-side.
- **`simulate_forecast` é read-only e chama o ML** (`services/copilot` →
  `internal/ranker`/serviço de ML), **única fonte de verdade do número**; o LLM
  **nunca produz o número**, só verbaliza com faixa de incerteza. Baseline Monte
  Carlo sobre `StatsHourly` enquanto o serviço de ML não existir.
- **RAG pgvector com RLS por tenant + teste de isolamento** (TX-3): catálogo e
  taxonomia de regras §4.6 vão **direto no contexto** com prompt caching (não
  precisam de busca vetorial); pgvector é **só** para "criativos similares por CTR"
  e docs de ajuda.

**Por que Python separado e não `bff/copilot` em TS:** mantém a coerência poliglota
(TS no front/BFF, Python no ML/IA), reusa o ecossistema LangGraph/Langfuse/Pydantic
maduro em Python, e mantém o BFF **enxuto como fronteira de ACL** (não vira monólito
de orquestração). O BFF continua sendo a **única** porta voltada ao cliente — o
copiloto Python **não** é exposto à internet.
- **Gatilho de reversão:** se o salto extra BFF→copilot (rede interna) adicionar
  latência **percebida pelo usuário** que prejudique a UX de chat (alvo de
  referência: **> 300 ms p95** só no hop interno, medido) **ou** se a orquestração
  ficar trivial a ponto de o ecossistema Python não se pagar, abre-se ADR para
  **colapsar o copiloto no BFF TS** (LangGraph.js) — com o número medido anexado.

### D. Feature store / featurização — função única, online em Redis, offline em Iceberg; SEM Feast na Fase 2

**Adotamos uma única função de featurização compartilhada treino↔serving (anti-skew),
materializada em duas camadas, sem Feast/Tecton.**

- **Função única (anti-skew):** a transformação de contexto → vetor de features é
  **uma única implementação** usada por (1) o treino offline (Python, `ml/`) e (2) o
  serving online (`internal/ranker` em Go). Implementações paralelas em duas
  linguagens são a fonte clássica de skew treino↔serving. **Decisão de fronteira:**
  a **especificação** da função (lista de features, transformações, ordem) é um
  **contrato versionado em `ml/features/`** e validada por um **teste de paridade
  treino↔serving** (mesma entrada → mesmo vetor em Python e Go, dentro de tolerância
  numérica). Esse teste é gate de promoção (E).
- **Online (serving):** features de baixa latência vêm do **snapshot in-process**
  (config, já existente na Fase 1) + estado mutável em **Redis** (caps/pacing já
  existentes; o ranker reusa, não cria store novo). Embeddings de demanda
  **pré-computados offline** e carregados pelo sidecar/snapshot. **Sem ida à rede
  adicional** no hot path além do que a Fase 1 já tem.
- **Offline (treino):** a fonte de verdade de treino é **Iceberg** (já é a verdade
  contábil/treino — ADR-0001/§2.2), com `decision_id`/`model_version`/`propensity`
  fechando o loop. O `StatsHourly` do **ClickHouse** serve agregações para o forecast
  leve de pacing e baseline Monte Carlo do copiloto, **nunca** como verdade de treino
  (não somar "ao vivo" com faturável — invariante).
- **MLflow** (tracking + registry) materializa em object storage; o **registry é a
  fonte de verdade da versão de modelo** que o sidecar carrega e que vai no
  `model_version` do `Decision`.

**Por que não Feast/Tecton na Fase 2:** o ADR-0002 §B.1 fixa a premissa de volume
(≤ 5.000 req/s, ≤ 50.000 campanhas) em que o snapshot in-process + Redis atendem com
folga; Feast adiciona um online store e um plano de servir features sem requisito que
o justifique (anti-padrão de over-engineering, risco §5).
- **Gatilho de reversão:** Feast/Tecton entram **só** quando o conjunto de features
  online **não couber** no snapshot+Redis sem prejudicar o budget (alvo de
  referência: estado de features online **> 1 TB** OU > 2 ms p99 só na materialização
  de features no hot path), com o número medido anexado a um ADR sucessor.

### E. Resolução da pergunta aberta remanescente (stack §6 q.5) — identidade cookieless

#### E.5 — Identidade cookieless: **first-party/edge efêmero, sem PII central; fail-safe DA-6 mantido**

**Adotamos identidade baseada em sinal first-party/edge efêmero, sem criar nenhum
identificador de usuário central nem PII (TX-5/DA-11), preservando o fail-safe DA-6.**
Concretamente:

- O capping/pacing usa uma **chave efêmera hasheada com salt rotativo + TTL curto**
  (já implementado na Fase 1, `internal/capping`), derivada de sinal **first-party**
  disponível na borda (cookie first-party de primeira-parte do publisher quando
  consentido, ou fingerprint de sessão efêmero **não-persistente**) — **nunca** um
  ID global cross-site, **nunca** PII, **nunca** IP bruto (descartado pós-geo).
- **Fail-safe DA-6 mantido literal:** campanha capeada **sem identificador estável**
  tem a entrega **abortada** (silêncio preferido a estouro). O cookieless **não**
  relaxa isso — ele define a **qualidade** do identificador efêmero, não a regra de
  abort.
- **Features do ranker** usam **contexto** (zona, geo de cidade via GeoLite2,
  device-class, hora, taxonomia de regras §4.6) e **agregados não-identificáveis** —
  **não** dependem de um ID de usuário persistente. O `Decision` reserva
  `feature_vector_ref` (hash/referência), **nunca** features cruas/reidentificáveis
  (contrato §6 de `propensity-logging.md`).
- **Conformidade:** sem store de perfil persistente; consentimento respeitado na
  borda; redação OTel antes de qualquer export. O `privacy-compliance-auditor` é
  **gate de merge** desta decisão.

**Impacto no fill rate (o trade-off real do risco §5):** sem cookie estável, a
fração de requests com identificador confiável cai, e o fail-safe DA-6 abate
**parte** do inventário capeado. Aceitamos isso como **correto por design** (silêncio
> estouro contábil) e o medimos.
- **Gatilho de reversão:** se o **fill rate de campanhas capeadas** cair abaixo de um
  piso comercial (alvo de referência a calibrar com o cliente; medir baseline no
  shadow/A-B antes de fixar) **de forma material**, abre-se ADR para avaliar
  identificadores **conformes** adicionais (ex.: integração com framework de
  identidade do publisher, conjuntos de IDs first-party) — **sempre** sem PII central
  e revisado pelo `privacy-compliance-auditor`, com o número de fill rate medido
  anexado. Nunca se adota um ID cross-site persistente por aspiração de fill rate.

### F. Política de layout da Fase 2 — diretórios novos (estende a árvore do ADR-0002)

A Fase 2 cria os diretórios abaixo, respeitando as fronteiras já ratificadas no
ADR-0002 (`proto/` = fonte de eventos; `db/` = relacional; `data/` = analítico;
`gen/` versionado; **módulo Go único**; `services/` = binários; `internal/` = pacotes
Go compartilhados). O que já existe está marcado *(existe — Fase 1)*.

```text
.
├── internal/
│   └── ranker/                 # NOVO — cliente Go in-process do re-ranker (DA-3/TX-4):
│                               # featurização (chama spec de ml/features), IPC com o
│                               # sidecar via Unix socket, TIMEOUT DURO + FAIL-OPEN.
│                               # Ponto de extensão chamado por services/decision.
│
├── services/
│   ├── decision/               # (existe) — Fase 2 PLUGA a chamada a internal/ranker
│   ├── collector/              # (existe)
│   ├── ranker-sidecar/         # NOVO — sidecar de serving CPU (Treelite/ONNX) por
│   │   └── ...                 # Unix socket; SEM Triton/GPU. Hot-reload de versão.
│   │                           # Runtime do modelo isolado num processo sidecar co-
│   │                           # localizado (ADR-0002 §C: hot path Go CGO-free).
│   │                           # Reconciliação G0/E11: a OPÇÃO ONNX Runtime usa um
│   │                           # binding Go-nativo (github.com/yalue/onnxruntime_go)
│   │                           # que ENTRA no go.mod, porém SÓ sob `//go:build onnx`
│   │                           # — o build default (`go build ./...`) segue CGO-free
│   │                           # e não linka a lib. O invariante real ("runtime do
│   │                           # modelo isolado; hot path Go hermético") é preservado;
│   │                           # a antiga nota "fora do go.mod" valia p/ a opção
│   │                           # Treelite/lib-nativa, não p/ o binding Go tagueado.
│   └── copilot/                # NOVO — gateway Python LangGraph: grafo + HITL +
│       └── ...                 # ferramentas tipadas server-side + RAG pgvector (RLS) +
│                               # Langfuse. ATRÁS do BFF; nunca exposto ao cliente.
│
├── ml/                         # NOVO (topo, Python) — domínio de ML, fora do módulo Go
│   ├── features/               # FUNÇÃO DE FEATURIZAÇÃO ÚNICA (spec versionada) +
│   │                           # teste de paridade treino↔serving (anti-skew, gate E)
│   ├── training/               # treino LightGBM/CatBoost (pCTR; pCVR após atribuição)
│   ├── calibration/            # calibração isotônica + monitor ECE/reliability
│   ├── ope/                    # estimadores OPE (IPS/SNIPS/DR) sobre propensão logada
│   ├── pacing/                 # forecast de tráfego leve + controlador proporcional (DA-4)
│   ├── fraud/                  # GBDT supervisionado de IVT (ingestão, TX-6) — fora do hot path
│   └── registry/               # integração MLflow (tracking + registry de versão)
│
├── db/
│   └── vector/                 # NOVO — migrations pgvector (HNSW) + RLS por tenant
│       └── migrations/         # para o RAG do copiloto (criativos similares + docs)
│
└── bff/                        # (existe) — Fase 2 ESTENDE: rota de copiloto que protege
    └── src/routers/copilot.ts  # a chave Claude, injeta tenant_id, faz proxy SSE p/ services/copilot
```

**Princípios de fronteira da Fase 2 (herdados e estendidos):**

- **`ml/` é Python e NÃO entra no módulo Go.** O hot path Go fala com o modelo
  **só** pelo sidecar (UDS) — `internal/ranker` é o único ponto de contato. Nenhum
  import cruzado Go↔Python.
- **Modelos compilados (ONNX/Treelite) NÃO são versionados no git como blobs:** são
  artefatos do **MLflow registry** (object storage), carregados pelo sidecar por
  versão. O git versiona **código** (`ml/`, `internal/ranker`, `services/*`) e a
  **spec** de features (`ml/features/`), não pesos. O `model_version` no `Decision`
  amarra a versão servida ao registry.
- **pgvector é um schema relacional novo** (`db/vector/`) com **RLS por `tenant_id`**
  — coerente com a fronteira "`db/` = relacional" do ADR-0002 e com TX-3.
- **O copiloto Python (`services/copilot`) só é alcançável pelo BFF** (rede interna);
  a chave Claude e os segredos vivem no BFF/OpenBao, nunca no front nem no LLM.

### Fora do escopo deste ADR

- **Deep ranking** (two-tower / DCN-v2 / DLRM), **Triton/GPU** — Fase 3, **só sob
  prova de uplift A/B** sobre o GBDT da Fase 2.
- **Fraude não-supervisionada avançada** (autoencoder / Isolation Forest / GNN) —
  Fase 3. A Fase 2 entrega **só** fraude/IVT **supervisionada** na ingestão (TX-6).
- **Cripto / AEV / BND / pagamentos multi-trilho** — Fase 3, fora do hot path.
- **Near-real-time / Flink** — deferido sob o gatilho do **ADR-0001** (não confirmado).
  A linha "Flink incremental **se** near-real-time confirmado" da §4 lê-se: **não
  confirmado**; só sob o gatilho do ADR-0001.
- **Multi-touch attribution** — sob o gatilho do ADR-0002 §B.7 (reusa
  `decision_id`/`occurred_at` sem migração); a Fase 2 mantém last-click 7d.
- Não escreve hot path, modelo, sidecar nem grafo de copiloto **linha a linha** —
  delegado aos engenheiros de camada no fan-out seguinte.

### G. Sequenciamento da Fase 2 em incrementos (J0…J6)

A Fase 2 é construída em **7 incrementos** com dependências explícitas. Cada
incremento lista o que **fecha** (verde só após validação do guardião dono) e os
**invariantes** que preserva. **J0 é pré-requisito absoluto** (nada de ML antes do
logging de propensão instrumentado no hot path). **J1, J2 e J5 rodam em paralelo**
após J0 (não compartilham arquivos: ranker Go+sidecar vs. pipeline Python de treino
vs. copiloto Python/BFF). **J3 e J4 são pontos de junção** (ligam o ranker treinado
ao hot path sob A/B). **J6 fecha o ciclo** (pacing + fraude na ingestão).

| Inc | Nome | Depende de | Entrega | Fecha / invariantes | Subagente-dono |
|---|---|---|---|---|---|
| **J0** | Instrumentação de propensão no hot path | — (Fase 1 completa) | `services/decision` preenche `Decision.propensity`/`exploration_policy`/`epsilon`/`candidates[]`/`ml_fail_open` conforme `propensity-logging.md`; `lg/ck/ct` preservam `decision_id`+`model_version` ponta-a-ponta; `Decision` flui p/ Iceberg. **Sem modelo ainda** (`DETERMINISTIC`, `propensity=1.0`). | Loop de atribuição instrumentado (TX-1); golden tests da Fase 1 **continuam verdes** (cascata pura inalterada). Pré-requisito de OPE. | `decision-engine-engineer` (+ `schema-contracts-steward`) |
| **J1** | Esqueleto do re-ranker + sidecar fail-open | J0 | `internal/ranker` (featurização, IPC UDS, **timeout duro + fail-open**); `services/ranker-sidecar` carregando um **modelo dummy/identidade**; ponto de extensão plugado em `services/decision` **atrás de flag, desligado por padrão**. | TX-4 (5–8 ms p99 + fail-open); DA-3 (re-rank só **dentro** do estrato, ordem da cascata preservada no fail-open). Golden tests verdes com ranker ON-fail-open ≡ cascata pura. | `ml-optimization-engineer` + `decision-engine-engineer` |
| **J2** | Pipeline de treino pCTR + featurização única + MLflow | J0 | `ml/features` (spec única + **teste de paridade treino↔serving**); `ml/training` (LightGBM pCTR sobre Iceberg); `ml/calibration` (isotônica + ECE); `ml/registry` (MLflow); compilação Treelite/ONNX p/ o sidecar. | Anti-skew (função única); calibração monitorada (eCPM = p×rate); **dinheiro nunca em float** (TX-2) onde houver valor monetário. | `ml-optimization-engineer` (+ `money-ledger-guardian` no eCPM) |
| **J3** | OPE + shadow do ranker calibrado | J1, J2 | `ml/ope` (IPS/SNIPS/DR sobre propensão logada, **filtrando `ml_fail_open`**); ranker calibrado servido em **shadow** (loga decisão sombra, não serve); bandit (epsilon-greedy/Thompson) preparado mas **não exposto**. | OPE honesto (overlap/positividade da propensão); shadow **não** altera o que é servido (semântica contábil intacta). | `ml-optimization-engineer` |
| **J4** | A/B por zona/tenant + kill-switch + promoção | J3 | Ativação do re-ranker em **A/B por zona/tenant** com **guarda de receita + kill-switch**; promoção de `model_version` via MLflow registry; bandit exposto sob A/B. | **Nada promovido sem prova de uplift A/B + kill-switch**; cascata segue autoridade final (DA-3); golden tests verdes (ML não muda faturável). | `ml-optimization-engineer` (+ `parity-golden-test-guardian` no gate) |
| **J5** | Copiloto LangGraph + RAG + HITL + BFF | J0 | `services/copilot` (LangGraph + HITL + ferramentas tipadas server-side); `db/vector` (pgvector HNSW + **RLS por tenant** + teste de isolamento); `bff/src/routers/copilot.ts` (protege chave Claude, injeta `tenant_id`, proxy SSE); Langfuse self-hosted; proveniência C2PA/SynthID no `validate_creative`; `simulate_forecast` read-only chamando o ML. | TX-3 (LLM nunca recebe credencial; RLS + teste de isolamento; HITL em toda escrita); TX-5/DA-11 (sem PII; redação OTel); EU AI Act Art. 50 (proveniência). | `copilot-llm-engineer` + `frontend-bff-engineer` |
| **J6** | Pacing proporcional + fraude/IVT na ingestão | J0 (pacing); J2 (fraude reusa featurização) | `ml/pacing` (controlador proporcional DA-4 + forecast leve sobre `StatsHourly`); `ml/fraud` (GBDT supervisionado de IVT na ingestão, **antes** do `StatsHourly`/faturamento). | DA-4 (proporcional, não RL/MPC no hot path); TX-6 (fraude fora do hot path, marca IVT p/ faturar só tráfego válido); reconciliação contra Iceberg (não streaming). | `ml-optimization-engineer` + `data-platform-engineer` |

**Regras de sequenciamento e pontos de junção:**

- **J0 é gate duro:** nenhum modelo treina antes da propensão estar instrumentada e
  fluindo para Iceberg (sem isso, OPE é inválido — `propensity-logging.md §5`).
- **Paralelismo:** J1 ∥ J2 ∥ J5 após J0 (fronteiras de diretório/linguagem disjuntas).
  J6-pacing pode iniciar com J1 (não depende de modelo treinado); J6-fraude depende de
  J2 (reusa featurização).
- **Pontos de junção:** **J3** une J1 (serving) + J2 (modelo treinado) em shadow; **J4**
  é o gate de produção (A/B + kill-switch). O ranker **só serve tráfego real em J4**,
  e **só** com uplift A/B provado.
- **Gate de paridade contínuo:** os golden tests da Fase 1 rodam em J0, J1 e J4 — o
  `parity-golden-test-guardian` confirma que **o ML não alterou a semântica contábil
  da cascata** (cascata pura ≡ fail-open; faturável inalterado).
- **Gates de merge (todos os incrementos):** `make verify` verde (buf TX-1 +
  no-float TX-2); `security-reviewer` + `privacy-compliance-auditor` sem CRITICAL/HIGH
  (J5 exige isolamento de tenant no RAG + prompt-injection); `money-ledger-guardian`
  onde houver eCPM/valor monetário (J2/J4). **Nenhuma versão de modelo é promovida sem
  prova de uplift A/B + kill-switch (J4); HITL é obrigatório em toda escrita do
  copiloto (J5).**

## Gatilho de reabertura

Cada decisão B…E carrega seu **próprio gatilho de reversão mensurável** acima.
Globalmente, este ADR é reaberto se **o re-ranker não couber no budget de 5–8 ms p99
declarado no ADR-0002 §B.2** de forma sustentada com o modelo de produção (medido em
shadow/A-B) — sintoma que reabriria a discussão de serving (sidecar vs. in-process,
ou poda/INT8 do modelo) — com o número medido anexado ao ADR sucessor. Não se
amplia o budget total da decisão por aspiração; ou o modelo cabe, ou a Fase 2 serve
um modelo mais barato, ou degrada para a cascata (fail-open) com mais frequência.

## Alternativas consideradas

- **ML como seletor de estrato (re-rank cross-cascata).** Rejeitada categoricamente:
  fura DA-3 (autoridade final da cascata) e corrompe a semântica contábil — o ML
  promoveria Remnant sobre Contract. O re-ranking é **estritamente dentro** do estrato
  vencedor (`Candidate.tier`).
- **Serving in-process puro (Treelite linkado via CGO).** Rejeitada por ora: quebra o
  build hermético sem-CGO do ADR-0002 §C e acopla o crash do runtime do modelo ao
  motor Go. Fica como upgrade sob o gatilho de B (IPS > 2 ms p99 medido). O sidecar
  via UDS isola o runtime e habilita hot-reload de versão.
- **Triton / GPU já na Fase 2.** Rejeitada: o modelo de produção é GBDT (CPU) que cabe
  no budget; Triton/GPU é Fase 3 **só sob uplift A/B** de deep ranking — adotá-lo agora
  é over-engineering (risco §5) sem deep model que o justifique.
- **Copiloto colapsado no BFF TS (LangGraph.js).** Rejeitada por ora: o ecossistema
  LangGraph/Langfuse/Pydantic é mais maduro em Python e a orquestração com HITL +
  ferramentas tipadas não é trivial; manter Python separado preserva a coerência
  poliglota e o BFF enxuto. Fica como upgrade sob o gatilho de C (hop interno
  > 300 ms p95).
- **Feast/Tecton como feature store na Fase 2.** Rejeitada: a premissa de volume do
  ADR-0002 §B.1 cabe em snapshot in-process + Redis; Feast adiciona um online store
  sem requisito. Fica sob o gatilho de D (features online > 1 TB ou > 2 ms p99).
- **Adotar um ID de usuário cross-site persistente para salvar fill rate.** Rejeitada:
  cria PII/identidade central, viola TX-5/DA-11 e o fail-safe DA-6. O cookieless
  efêmero first-party é a escolha; fill rate baixo aciona avaliação de identificadores
  **conformes** (E.5), nunca um ID persistente por aspiração.

## Consequências

- **Positivas:**
  - **Desbloqueia o fan-out paralelo** da Fase 2 (`ml-optimization-engineer`,
    `copilot-llm-engineer`, `frontend-bff-engineer`) com fronteiras de diretório e
    linguagem explícitas e zero colisão estrutural (`internal/ranker`, `services/ranker-sidecar`,
    `services/copilot`, `ml/`, `db/vector/` ratificados).
  - **Protege o p99 e a autoridade da cascata por construção:** o ML entra como
    re-ranker dentro do estrato, dentro da reserva já declarada, com timeout duro +
    fail-open — DA-3 e TX-4 viram invariantes do layout, não promessas.
  - **OPE honesto desde o início:** J0 garante propensão instrumentada antes de
    qualquer treino; nenhum "uplift" indefensável.
  - q.5 (cookieless) sai do limbo com **default + gatilho mensurável** ligado ao fill
    rate, sem violar privacidade.
- **Negativas / custos aceitos:**
  - Um salto de IPC (UDS) por decisão quando o ranker está ativo — medido e contido no
    budget; gatilho de reversão documentado (B).
  - Um hop interno BFF→copilot na latência de chat — aceito (não está no hot path de
    decisão); gatilho documentado (C).
  - Skew treino↔serving é um risco real entre Python e Go — mitigado pela **função
    única + teste de paridade** como gate de promoção (D/E do sequenciamento).
  - Fill rate pode cair sem cookie estável — **aceito por design** (silêncio >
    estouro); medido, com gatilho para reavaliar identificadores conformes (E.5).
- **Impacto por fase do roadmap:**
  - **Fase 1:** inalterada — a Fase 2 **pluga** em `services/decision` atrás de flag;
    golden tests permanecem o contrato de não-regressão.
  - **Fase 2:** `internal/ranker`, `services/ranker-sidecar`, `services/copilot`,
    `ml/`, `db/vector/`, rota de copiloto no BFF; ML re-ranker + bandits + pacing +
    fraude/IVT + copiloto, tudo sob os gates de promoção.
  - **Fase 3:** deep ranking entra **substituindo o GBDT** atrás do **mesmo**
    `internal/ranker`/sidecar (agora Triton/GPU) **só sob uplift A/B**; fraude
    não-supervisionada e cripto entram fora do hot path. `ml/` e o ponto de extensão
    já comportam isso sem reabrir o layout.
