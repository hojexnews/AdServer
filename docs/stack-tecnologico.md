# Stack Tecnológico — AdServer com IA/Deep Learning, Copiloto e Pagamentos Multi-trilho

> **Público-alvo:** setor de desenvolvimento / liderança técnica.
> **Relação com o restante:** estende a [documentação técnica base](documentacao-tecnica.md) (entidades, motor de decisão, DA-1…DA-12). As referências `DA-n`/`CA-n` apontam para aquele documento.
> **Origem:** síntese de um design multiagente com revisão adversarial por dimensão + consolidação por "arquiteto-chefe".
> **Princípio condutor:** começar **enxuto e correto**; cada tecnologia "pesada" entra **sob medição**, nunca por aspiração de escala.

---

## 0. TL;DR — Stack recomendado

| Camada | Escolha primária (MVP) | Escala futura (sob prova) |
|---|---|---|
| **Motor de decisão (hot path)** | **Go** (net/http stdlib) | Rust+Axum só para componentes de cauda extrema medida |
| **Estado de config** | **Postgres 16** + snapshot em memória (pull periódico) | CDC/outbox se config crescer |
| **Capping/pacing (estado mutável)** | **Redis Cluster** (TTL) | DragonflyDB → Aerospike só em TB de estado |
| **Backbone de eventos** | **Redpanda** (API Kafka), 1 bus só | Kafka gerenciado se já houver operação |
| **Analytics/OLAP** | **ClickHouse** (rollups, materializa `StatsHourly`) | StarRocks/Pinot se exigir JOINs/cauda |
| **Fonte de verdade contábil/ML** | **Apache Iceberg + Parquet** (object storage) | — |
| **Stream processing** | _(nenhum no v1)_ | **Flink** incremental só p/ atribuição longa e fraude streaming |
| **ML de otimização (serving)** | **LightGBM** via ONNX/Treelite **in-process/sidecar (CPU)** | Two-tower DCN-v2/DLRM em **Triton (GPU)** só sob uplift A/B |
| **LLM do copiloto** | **Claude** roteado: **Haiku 4.5** inline · **Sonnet 4.6** padrão · **Opus 4.8** premium | — |
| **Orquestração do copiloto** | **LangGraph** (checkpointing + HITL obrigatório) | MCP quando houver multi-cliente |
| **Front-end** | **Next.js 16 + React 19 + TS strict** | — |
| **Ledger/dinheiro** | **Postgres double-entry** + tipo `Money(asset,integer,scale)` | **TigerBeetle** só sob gargalo de escrita provado |
| **Pagamento fiat** | **Stripe** (global, SAQ-A) + **Asaas/PIX** (Brasil) | Adyen/MercadoPago redundância |
| **Cripto/custódia** | **Safe (multisig)** + automação | **Fireblocks (MPC)** quando AUM justificar |
| **Conector on-chain** | **viem** (TS) + interface `ChainConnector` única | SDK nativo se chain própria não-EVM |
| **Infra** | **EKS** (control plane/ML/batch) + **borda Go em PoPs/CDN Anycast** | multi-região por célula |
| **Observabilidade** | **OpenTelemetry** + VictoriaMetrics/Grafana/Loki/Tempo | — |
| **Segredos / IaC / GitOps** | **OpenBao(Vault)** · **OpenTofu** · **Argo CD** · **Cilium eBPF** | — |

> **A regra de ouro do design:** o caminho quente de decisão (Go + Postgres-config + Redis-capping + 1 bus + ClickHouse) substitui o `PHP+MySQL` do Revive. **PHP fica só no admin/painel/batch.** Tudo além disso é incremental.

---

## 1. Decisões transversais (valem para todas as camadas)

Estas são as decisões que mantêm o sistema **coerente** entre serviços poliglotas (Go no hot path, Java no streaming, Python no ML, TS no front).

### TX-1 — Contrato de eventos único (schema registry)
- **Protobuf-first** com **Buf** (breaking-change detection na CI, compatibilidade **BACKWARD** obrigatória). Descartar Avro do contrato canônico.
- Todo evento carrega um **envelope** com `tenant_id`, `event_id` (chave de dedupe/idempotência), `decision_id` e `model_version`.
- `decision_id` + `model_version` **fecham o loop de atribuição** (treino de pCVR e avaliação off-policy). **Sem isso, não há ML.**

### TX-2 — Dinheiro: tipo canônico único
- Tipo `Money(asset_code, integer_amount, scale)` atravessa **todas** as fronteiras (evento → ledger → BFF → UI).
- No fio: `int64` minor-units + `scale` explícito. No Postgres: `NUMERIC` por ativo. No front: `decimal.js`/`Intl.NumberFormat` ou `bigint` (cripto 18 dec).
- **`float` PROIBIDO em CI** (lint/teste) em todas as linguagens.
- **Multi-moeda sem conversão automática (DA-10):** cada ativo é um ledger isolado; câmbio só existe como **par de postings explícito** com taxa registrada por humano/desk. `scale` por ativo no **Asset Registry** (BRL=2, USDC=6, ERC-20=18, **AEV/BND=a definir**).
- eCPM compara candidatos **dentro da mesma moeda/tenant** — nunca por câmbio implícito.

### TX-3 — Identidade, auth e multi-tenancy
- `tenant_id` resolvido no middleware do Next.js (white-label) e propagado a cada chamada ao **BFF**, que é a fronteira de ACL **server-side** (CA-1).
- Isolamento em camadas: namespace+RBAC+NetworkPolicy (Cilium deny-all) no K8s; **RLS por `tenant_id`** no Postgres/pgvector; **row-policies + quotas** no ClickHouse.
- **O LLM nunca recebe credencial:** atua só por ferramentas tipadas via gateway que injeta `tenant_id` e segredos. RAG sempre filtrado por `tenant_id` + teste de isolamento entre tenants.
- PII/KYC isolado em **cofre de compliance** referenciado por `tenant_id` pseudônimo. **Ledger e telemetria sem PII** (DA-11).

### TX-4 — Orçamento de latência da inferência (a regra que protege o p99)
- Alocação rígida de **5–8 ms p99** só para o bloco de ML dentro da decisão.
- **Timeout duro + fail-open determinístico OBRIGATÓRIO:** se o ML estourar/falhar, degrada para a **cascata pura** (DA-3) — nunca impressão em branco por falha de ML.
- **A IA é re-ranker DENTRO de cada estrato elegível.** Jamais fura a cascata `Override > Contract > Remnant` (autoridade final é a cascata).
- Modelo embarcado in-process/sidecar via Unix socket; embeddings de demanda pré-computados offline.

### TX-5 — Privacy by Design como gate de aceitação
- IP **descartado no collector** após derivar geo (GeoLite2). Sem store de perfil persistente.
- Chaves de capping efêmeras/hasheadas com salt rotativo + TTL curto.
- OTel Collector **redige PII** antes de qualquer export. Observabilidade do copiloto (Langfuse) **self-hosted**.
- Proveniência **C2PA/SynthID** + disclosure "gerado por IA" nos criativos — **EU AI Act Art. 50** (vigor 02/08/2026).

### TX-6 — Fraude/IVT roda na ingestão (fora do hot path)
- Regras + GBDT supervisionado + agregações de grafo simples, **antes** do `StatsHourly` e do faturamento. Marca eventos IVT para que CPM/CPC/CPA faturem só tráfego válido.

---

## 2. Camada por camada

### 2.1 Motor de decisão e veiculação (hot path)
- **Go** (net/http; sem fasthttp salvo necessidade comprovada). Sem GC-pauses problemáticos no padrão de carga esperado; curva de equipe menor que Rust e paridade mais fácil com a semântica legada via **golden tests**. **Rust+Axum** fica como escape hatch para componentes comprovadamente de cauda extrema (ex.: collector de borda).
- **Cascata + regras + capping em memória**: config (campanhas, banners, zonas, regras §4.6, caps) como **snapshot versionado** carregado do Postgres com refresh periódico (pull). Avaliação O(1), sem ida à rede no hot path.
- **Capping** em **Redis Cluster** (TTL para session/clock), **best-effort** + **fail-safe** que aborta entrega de campanhas capeadas sem identificador estável (DA-6). Tolerância de sub/sobre-entrega declarada como aceitável (padrão adtech).
- **Ad tag**: loader JS assíncrono não-bloqueante → endpoint JSON (criativo ou vazio→impressão em branco), preservando `cb`, custom vars first-party, pixel 1×1 (impressão), redirect 302 server-side (clique), pixel de conversão (DA-5/DA-8). **VAST 4.x** para vídeo (sem VPAID).
- **Borda**: **uma** CDN Anycast gerenciada (Cloudflare/Fastly) → TLS, geo de país via header, rate limiting. Geo de cidade/precisão via **MaxMind GeoLite2 em memória** no motor (autoritativo). **Não** empilhar Envoy+Pingora+multi-CDN.
- **Telemetria**: produtor **fire-and-forget em lote** → **Redpanda**, com **WAL local durável + at-least-once + dedupe idempotente por `event_id`** para proteger faturamento.

### 2.2 Plataforma de dados, telemetria e analytics
- **Redpanda** como backbone único; particionar por **hash de `event_id`/`zone_id`** (não por tenant — evita hot partitions).
- **ClickHouse**: ingestão direta (Kafka engine), `AggregatingMergeTree`/`SummingMergeTree` com `uniqState`/HLL para rollups; **materializa a visão `StatsHourly`** preservando o contrato do admin (DA-7/CA-6). Row-policies + quotas por `tenant_id`.
- **Apache Iceberg + Parquet** = **fonte de verdade** contábil e de treino (time-travel → reprodutibilidade e atribuição fechada). **Faturamento reconcilia contra o lakehouse, nunca contra o streaming.**
- **Atribuição dupla**: número **ao vivo** (janela curta, ClickHouse) ≠ número **fechado/faturável** (batch sobre Iceberg, lookback completo). A UI **rotula "consolidado ≤1h" vs "ao vivo"** e **nunca soma** as duas fontes.
- **Adiar para quando houver volume provado:** Flink stateful (só atribuição longa near-real-time + fraude streaming), Feast online store, camada semântica BI.
- ⚠️ **Pendência bloqueadora (Fase 0):** confirmar com o dono do produto se **frescor near-real-time (1–5s) é mesmo requisito**, dado que DA-7 (batch horário) e "não-RTB" são normativos. Todo o eixo streaming depende dessa resposta.

### 2.3 IA / Deep Learning para otimização
- **Treino**: PyTorch 2.x disponível, mas o **modelo de produção é GBDT (LightGBM; CatBoost p/ alta cardinalidade)**. Deep é **Fase 2 sob prova de uplift A/B**.
- **Serving (hot path)**: GBDT compilado com **Treelite/ONNX Runtime** embarcado/sidecar via Unix socket — **sem Triton, sem GPU no MVP** (cabe no orçamento de 5–8 ms, TX-4).
- **Modelos**: `pCTR` primeiro; `pCVR` só após atribuição confiável. **Calibração isotônica** monitorada (ECE/reliability) — barata e crítica para `eCPM = p × rate`.
- **Exploração/yield**: **Thompson Sampling**/LinUCB sobre o ranker calibrado (epsilon-greedy decrescente no MVP). Vowpal Wabbit só se aprendizado online incremental for requisito provado.
- **Pacing (DA-4)**: **controlador proporcional** por déficit vs. cronograma alimentado por forecast de tráfego leve. PID só sob oscilação observada; **RL/MPC descartados no hot path** (opacidade + risco financeiro).
- **MLOps**: **MLflow** (tracking + registry com promoção auditável) desde o início. **Ray só quando houver treino distribuído de deep real**. **Uma única função de featurização** compartilhada treino/serving (anti-skew); Feast/Tecton só se features online crescerem.
- **Experimentação**: shadow + A/B por zona/tenant com guarda de receita e **kill-switch**. OPE (IPS/DR) + interleaving na Fase 2, dependentes de **logging de propensão**.
- **Fase 2/3 (sob uplift)**: deep ranking **two-tower DCN-v2/DLRM** em **Triton** (torre de demanda pré-computada, INT8). Fraude não-supervisionada (autoencoder/Isolation Forest/GNN).

### 2.4 Copiloto de IA para anunciantes
- **Roteamento por dificuldade** atrás de interface de modelo abstrata: **Haiku 4.5** (`claude-haiku-4-5-20251001`) inline/autocomplete · **Sonnet 4.6** (`claude-sonnet-4-6`, 1M ctx) cérebro padrão de raciocínio/tool-use · **Opus 4.8** (`claude-opus-4-8`) planejamento premium. **Prompt caching agressivo + Batch API (-50%)** + rate-limit/orçamento por tenant.
- **Orquestração**: **LangGraph** (checkpointing + durable execution) com **HITL obrigatório em toda escrita** de campanha/banner/regra. Nada publicado autonomamente.
- **Bridge seguro**: gateway de autorização **server-side** que injeta `tenant_id` e segredos; ferramentas **tipadas** (Pydantic/JSON Schema). MCP é evolução opcional, não pré-requisito.
- **Forecast**: ferramenta `simulate_forecast` **read-only** chamando os modelos de pCTR/pCVR (única fonte de verdade). **O LLM nunca produz o número** — só verbaliza com faixa de incerteza. Baseline Monte Carlo sobre `StatsHourly` se o serviço de ML ainda não existir.
- **Geração de criativo**: caminho primário é **template HTML5 + camada de texto vetorial determinística** (preço/CTA corretos, editáveis, legíveis); modelo generativo (Firefly por indenização de copyright / Nano Banana Pro) só para o **master visual**. Vídeo (**Veo 3.1**) **assíncrono/batch**, fora da latência do chat, com revisão humana.
- **RAG escopado**: pgvector (HNSW) + embeddings (Voyage/Cohere v4 multilíngue p/ PT-BR) **só** para "criativos similares por CTR" e docs de ajuda. Catálogo e taxonomia de regras (§4.6) vão **direto no contexto** com prompt caching (cabem em 1M tokens) — **não** precisam de busca vetorial.
- **Guardrails enxutos (2 camadas)**: (a) validação estrutural determinística (Pydantic/JSON Schema + specs IAB/HTML5 + ausência de PII) como gate de publicação; (b) **Haiku-as-judge** para brand-safety/claims/prompt-injection. Sem framework pesado.
- **Proveniência** (gate, não opção): C2PA/SynthID + disclosure embutidos no `validate_creative` (EU AI Act Art. 50).
- **Observabilidade/evals**: **Langfuse self-hosted** + golden set com LLM-as-judge; **gating de regressão de qualidade E de custo/tokenização** antes de promover qualquer upgrade de modelo.

### 2.5 Front-end self-service do anunciante
- **Next.js 16 (App Router) + React 19.2 + TypeScript strict** (Turbopack); middleware para tenant/white-label.
- **Design system**: **shadcn/ui** sobre primitives desacoplados (**Base UI**/React Aria), **Tailwind v4** (tokens CSS-first p/ white-label runtime). **WCAG 2.2 AA** com axe/Playwright em CI.
- **Estado**: **TanStack Query v5** (server state + optimistic update p/ "aplicar sugestão 1-clique") + **Zustand** + **Zod** + **React Hook Form**. Sem XState por padrão.
- **Contrato de dados**: **BFF dedicado** (fronteira rígida contra o motor poliglota). **tRPC v11** se o BFF for Node/TS; senão **OpenAPI + cliente gerado**. Schemas **Zod** como fonte única.
- **Dinheiro na UI**: BFF entrega **string DECIMAL + rótulo de moeda**; front formata com `decimal.js`/`Intl.NumberFormat` (ou `bigint` p/ cripto 18 dec). **Nunca `Number`, nunca aritmética monetária no cliente, nunca conversão automática.**
- **Dashboards**: **uma** lib de gráficos (Recharts/Tremor cobre — volume modesto, escopo por anunciante). Separar visualmente "consolidado ≤1h" de "ao vivo".
- **Builder de segmentação**: RHF + Zod + **react-querybuilder** para AND/OR, com **validação anti-contradição** §4.6/CA-4 rodando inclusive sobre sugestões da IA.
- **Copiloto na UI**: **Vercel AI SDK v5** (streaming SSE, tool-calling tipado) sobre o BFF que protege a chave Claude. "Aplicar 1-clique" = **PATCH validado por Zod + preview de diff + mutation otimista** (human-in-the-loop).
- **Real-time**: **SSE** como canal único (deltas de KPI + streaming de IA). WebSocket só sob requisito bidirecional real.
- **Cripto fora do cliente**: a UI só consome status via BFF de pagamentos; `wagmi/viem/WalletConnect` no front **apenas se** a spec de AEV/BND exigir assinatura on-chain pelo anunciante.

### 2.6 Pagamentos: fiat + cripto + tokens Aevum/Bond
- **Ledger (v1)**: **Postgres 16** com schema **double-entry** próprio (`accounts`/`journal_entries`/`postings`, constraint `sum(debit)=sum(credit)`, `NUMERIC` por ativo, particionamento temporal, **idempotency key** por captura). **Uma fonte da verdade.** **TigerBeetle só sob gargalo de escrita financeira PROVADO** — nunca por "1M tps" aspiracional (evita reconciliar dois motores).
- **Asset Registry plugável** + tipo `Money` (TX-2). AEV/BND entram como **linhas**: `(code, scale, kind, chain_id, contract, custody_mode, price_source)`.
- **Fiat global**: **Stripe** (Payment Intents + Billing + Tax), tokenização client-side (Elements/Checkout), escopo **PCI SAQ-A**.
- **Fiat Brasil**: **Asaas** como PIX primário (QR dinâmico, **Pix Automático** para Tenancy, conciliação por txid/E2E); Mercado Pago como failover.
- **Cripto/custódia (início)**: **Safe (multisig)** + automação (OpenZeppelin Defender/Gelato). **Fireblocks (MPC) é UPGRADE** quando o AUM justificar (não pagar US$10–50k/mês antes da 1ª transação). **USDC** (Circle Mint) como stablecoin/ramp; USDT por alcance.
- **Conector on-chain**: **viem** (TS) + `web3.py` no lado ML; **interface `ChainConnector` única** (`watchDeposits`, `getBalance`, `buildPayout`, `confirmations`). Para AEV/BND **EVM**: viem/Fireblocks direto. Para **chain própria não-EVM**: implementar `ChainConnector` com SDK nativo (único cenário que justifica signer/indexer/confirmações próprios).
- **Preço AEV/BND (v1)**: **administrado/manual** no Asset Registry com **governança explícita** de quem define (mitiga conflito de interesse). Chainlink/Pyth só quando houver ativo líquido com feed real.
- **Compliance**: **Sumsub** (KYC/KYB, forte no Brasil) + **Chainalysis** (screening on-chain). **Travel Rule** e screening de sanções no trilho cripto.
- **Invariante**: toda movimentação = par de postings idempotente; nenhuma captura grava saldo direto; reconciliação periódica **abre exceções e nunca autocorrige**; depósito cripto fica **`pending` até finalidade** (preferir webhook do custodiante a lógica de reorg própria).

### 2.7 Infra, segurança, observabilidade e conformidade
- **EKS** para control plane/ML/batch; **hot path na borda** (Go em VMs/PoPs + cache local de modelos) atrás de **CDN Anycast (Cloudflare)**.
- **IaC OpenTofu** + **Argo CD** (GitOps); **Cilium eBPF** (deny-all) + **Gateway API (Envoy Gateway)**; **cosign + SBOM + Kyverno + Trivy + Falco**.
- **Observabilidade 100% OpenTelemetry**: VictoriaMetrics/Mimir + Grafana + Loki + Tempo.
- **Segredos**: **OpenBao/Vault** (dynamic secrets + Pod Identity), nada estático em imagem/git; KMS/HSM para chaves de pagamento.
- **Segregação em células**: **célula PCI** de escopo mínimo (conta cloud separada, Cilium deny-all) + **célula AML/KYC/Travel Rule** para cripto.
- **Evitar no início**: Crossplane e vCluster (excesso para o estágio atual).

---

## 3. Aevum (AEV) e Bond (BND) — abordagem plugável

> **Premissa:** não temos a especificação pública dos tokens. O design os trata como **ativos customizados** integrados de forma **plugável**, sem inventar specs. Eles entram como **linhas no Asset Registry** e, se on-chain, por trás da interface `ChainConnector`. Nada disso bloqueia o motor de decisão (pagamentos ficam 100% fora do hot path; só budgets pré-computados influenciam pacing).

**Perguntas em aberto que precisam de resposta antes da Fase 3 (cripto):**

1. **On-chain ou off-chain?** Se on-chain: **chain EVM** (token tipo ERC-20) ou **chain própria não-EVM**? → decide entre `viem`/Fireblocks direto vs. `ChainConnector`/signer/indexer/oráculo nativos.
2. **`scale`/decimals de cada token?** É o dado **mais crítico** — sem ele não há aritmética correta no `Money`/ledger.
3. **Classificação regulatória** (pagamento, utility, stablecoin, security)? → muda MiCA/BACEN/CVM, KYC/KYB, custódia e contabilidade.
4. **Como o preço é determinado** (feed/oráculo vs. **administrado** pelo cliente)? Se administrado, **qual a governança** de quem define o preço?
5. **Modelo de custódia**: self-custody (Safe/multisig), MPC (Fireblocks), ou o cliente controla o supply (mint/burn)? Quem detém as chaves?
6. **Liquidez / on-off ramp**: há desk/exchange que troca AEV/BND ↔ fiat/stablecoin? Sem ramp, faturamento em token vira **crédito fechado** no ecossistema.
7. **Mecânica especial no contrato** (rebasing, taxa de transferência, pause, blocklist, upgradability)? **BND = "Bond"** implica rendimento/maturidade/cupom? Se sim, o ledger precisa modelar **accruals**.
8. **Finalidade/confirmações por chain** e comportamento de **reorg** (define `pending`→definitivo).
9. **Fluxo de uso**: pagar campanhas (entrada), payout a publishers (saída), ou ambos? O anunciante **assina on-chain** (exige wagmi/viem/WalletConnect no front) ou a **plataforma custodia** e move por ele?
10. **Travel Rule / screening / KYC** específicos para AEV/BND e em quais jurisdições (Brasil/BACEN + global)?

---

## 4. Roadmap em fases

| Fase | Escopo | Entregas-chave |
|---|---|---|
| **0 — Fundações** _(bloqueante)_ | Contratos, observabilidade, loop de atribuição **antes** de qualquer ML | Schema Registry Protobuf/Buf (envelope c/ `tenant_id`/`event_id`/`decision_id`/`model_version`); tipo `Money` + lint anti-float; instrumentação de `decision_id`+`model_version`+propensão nos logs `lg/ck/ct`; plataforma base (EKS+OpenTofu+ArgoCD+Cilium+OTel+OpenBao); **decisão de produto: near-real-time é requisito?** |
| **1 — MVP de paridade** | Substituir o hot path do Revive; telemetria volumétrica; billing por agregação horária. **Sem ML síncrono, sem cripto.** | Decisão em **Go** (cascata em memória, regras §4.6, capping Redis + fail-safe DA-6); ad tag JS + pixel/redirect/conversão + VAST 4.x; collector + Redpanda + ClickHouse (`StatsHourly`) + Iceberg; **golden tests + shadow-traffic + dual-run contábil** antes do cutover; ledger Postgres + Asset Registry + billing CPM/CPC/CPA/Tenancy; console Next.js + BFF (CRUD, vínculo N:N, dashboards ≤1h, ACL); privacidade (IP descartado, capping efêmero, redação OTel) |
| **2 — Otimização por ML + copiloto** | Ranking por ML, pacing por controle clássico, copiloto do anunciante — cascata como autoridade, fail-open | pCTR LightGBM in-process (re-ranker, 5–8 ms p99 + fail-open); bandit (Thompson) sobre ranker calibrado; pacing proporcional; fraude/IVT na ingestão; copiloto Claude (Haiku/Sonnet/Opus) via LangGraph + HITL + ferramentas tipadas + RAG pgvector com RLS + proveniência C2PA; MLflow + feature func única + shadow/A-B com kill-switch; pCVR após atribuição; Flink incremental **se** near-real-time confirmado |
| **3 — IA avançada + cripto/tokens** | Deep ranking sob uplift; fraude não-supervisionada; trilhos cripto/AEV/BND plugáveis fora do hot path | Two-tower DCN-v2/DLRM em Triton **só se A/B provar uplift**; fraude não-supervisionada + OPE/interleaving; fiat (Stripe + Asaas/PIX) e cripto (Safe→Fireblocks, USDC) atrás de `ChainConnector`; **Asset Registry recebe AEV/BND** (sem migração de schema); célula PCI + célula AML/KYC/Travel Rule (Sumsub + Chainalysis); depósito `pending` até N confirmações, reorg via estorno auditável; preço AEV/BND administrado até existir feed; TigerBeetle **só sob gargalo provado** |

---

## 5. Maiores riscos (e mitigação)

| Risco | Mitigação |
|---|---|
| **Over-engineering** (stack poliglota pesado p/ time pequeno) | Fase 1 sem ML/cripto/Flink; escalar **por camada sob medição**; Flink/Triton/TigerBeetle/Aerospike/Fireblocks são **upgrades justificados por dados**, não defaults |
| **Reescrita Go divergir da semântica legada** → corromper faturamento | **Golden tests** (CA-2/4/5) + **shadow-traffic** + **dual-run contábil** dentro de tolerância antes de qualquer cutover |
| **Perda/dupla contagem de eventos** quebra CPM/CPC/CPA | WAL local + at-least-once + **dedupe idempotente por `event_id`** + sinks idempotentes; faturar contra o **lakehouse**, nunca contra streaming |
| **IA estoura o p99 da decisão** | GBDT in-process + embeddings pré-computados + **timeout duro + fail-open**; INT8; deep só na Fase 3 sob uplift |
| **Near-real-time vs. DA-7** (batch horário normativo) | Resolver na **Fase 0** com o dono do produto; UI rotula "≤1h" vs "ao vivo" e **nunca soma** |
| **Fail-safe sem cookie** em mundo cookieless derruba fill rate (DA-6) | Avaliar identificadores **first-party/edge** conformes GDPR+LGPD **sem criar PII central** |
| **Conformidade** (PCI escapar da célula, Travel Rule, EU AI Act Art. 50, residência de dados) | Fronteiras de rede rígidas validadas por QSA; células regionais auditadas; proveniência de criativos como gate |
| **Precisão decimal por ativo** (bug financeiro clássico) | Centralizar **toda** aritmética em `Money(asset,integer,scale)`; **proibir float em CI**; `scale` autoritativo no Asset Registry |
| **Custo de LLM/vídeo** | Roteamento Haiku→Sonnet→Opus; prompt caching; Batch API; orçamento por tenant; **gating de custo** antes de promover versão |
| **Prompt injection / vazamento entre tenants** | Autorização **server-side** (ignora instruções do payload); **RLS + filtro por tenant** em toda query vetorial + teste de isolamento; HITL em toda escrita; forecast sempre via ferramenta |

---

## 6. Perguntas que destravam decisões (responder cedo)

1. **Volume-alvo concreto** (pico de ad requests/s, nº de tenants, campanhas/regras ativas)? → dimensiona Go-vs-Rust, Redis-vs-Aerospike, estado in-process vs remoto.
2. **Orçamento de latência ponta-a-ponta** (p50/p99/p99.9) e fração reservada à IA? → ML síncrono vs pré-computado.
3. **Modelo de consistência de capping/pacing** (estrito vs eventual)? Há implicação contratual de sobre-entrega?
4. **Near-real-time é requisito** (1–5s) ou agregação horária basta? → destrava ou não o eixo Flink/streaming.
5. **Identidade cookieless** permitida sob GDPR+LGPD sem PII central? → impacto real do fail-safe no fill rate.
6. **BFF é Node/TS** (viabiliza tRPC) ou poliglota (OpenAPI/codegen)?
7. **Janelas de atribuição** (lookback click→conversion) e modelo (last-click vs multi-touch)?
8. **Specs de Aevum/Bond** (seção 3) — bloqueiam só a Fase 3, não o resto.

---

*Documento derivado de design multiagente com revisão adversarial. Atualizar conforme as respostas às perguntas em aberto (seções 3 e 6) forem chegando — especialmente as specs de Aevum/Bond.*
