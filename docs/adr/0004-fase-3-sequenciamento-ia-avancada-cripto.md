# ADR-0004 — Sequenciamento da Fase 3 (IA avançada + cripto/tokens AEV/BND), decisões de produto cripto e gates de promoção

> **Status:** Aceito · **Data:** 2026-06-04 · **Decisores:** Arquiteto-chefe / Tech Lead (+ `payments-crypto-engineer`, `money-ledger-guardian`, `ml-optimization-engineer`, `platform-infra-engineer` nas camadas afetadas)
> **Âncoras:** TX-1, TX-2, TX-3, TX-4, TX-5, TX-6 · DA-3, DA-4, DA-7, DA-10, DA-11 · CA-1…CA-9 · `docs/stack-tecnologico.md` §2.3, §2.6, §2.7, §3 (perguntas em aberto AEV/BND), §4 (roadmap, linha "Fase 3"), §5 (riscos: over-engineering, TigerBeetle só sob gargalo) · `docs/documentacao-tecnica.md` §4.1/§4.9 · ADR-0001, ADR-0002, ADR-0003
> **Supersede:** — · **Substituído por:** —

## Contexto

As Fases 0, 1 e 2 estão **código-completas na `main`** com todos os gates verdes
(`security`/`privacy`/`parity`/`money`). O que resta nelas é **não-código** e está
bloqueado neste ambiente: aplicar `platform/` em cloud, ONNX Runtime nativo no
sidecar, dados reais no Iceberg, shadow-traffic + dual-run contábil, chaves vivas.
O próximo passo **executável de código** é abrir a **Fase 3 — IA avançada +
cripto/tokens** (`docs/stack-tecnologico.md §4`, linha "Fase 3").

A Fase 3 entrega, segundo o roadmap e §2.3/§2.6/§2.7/§3:

- **Deep ranking sob uplift A/B** (§2.3): two-tower **DCN-v2/DLRM** em **Triton
  (GPU)**, torre de demanda **pré-computada/INT8**, servido **atrás do mesmo
  `internal/ranker`/sidecar** da Fase 2 — **só se o A/B provar uplift** sobre o GBDT.
  Reusa o arcabouço já construído: `internal/ranker/{ab,guard,shadow}.go`, `ml/ope`,
  `ml/registry/promote_model.py`.
- **Fraude não-supervisionada** (§2.3): autoencoder / Isolation Forest / GNN
  **complementando** o GBDT supervisionado de IVT da Fase 2 (`ml/fraud`), ainda
  **fora do hot path**, ainda marcando IVT **antes** do `StatsHourly`/faturamento
  (TX-6) — nunca decidindo veiculação.
- **Trilhos de pagamento multi-trilho plugáveis, fora do hot path** (§2.6): fiat
  (**Stripe** SAQ-A + **Asaas/PIX**) e cripto (**Safe multisig** → **Fireblocks sob
  AUM**, **USDC** como ramp) atrás de uma **interface `ChainConnector` única**;
  **Asset Registry recebe AEV/BND** como linhas (**sem migração de schema** — as
  colunas já existem na Fase 1); ledger double-entry idempotente (par de postings,
  nada grava saldo direto, depósito `pending` até finalidade); **célula PCI** +
  **célula AML/KYC/Travel Rule** (Sumsub KYC/KYB + Chainalysis screening).
- **Tokens Aevum (AEV) / Bond (BND) plugáveis** (§3): integrados como ativos
  customizados, **sem inventar specs**, atrás do `ChainConnector` e como linhas do
  Asset Registry.

Forças em jogo que **exigem um gate antes do fan-out** (mesma razão dos ADR-0002/0003):

- **A cascata é a autoridade final (DA-3) e o deep ranking não pode arranhá-la.** O
  deep model entra **substituindo o GBDT atrás do mesmo ponto de extensão** já
  ratificado no ADR-0003 §A: re-ranker **dentro** do estrato vencedor, **timeout duro
  + fail-open determinístico** para a cascata pura. O deep **não** ganha exceção de
  budget nem de autoridade por ser "mais inteligente". Qualquer design que mova o deep
  para fora do `internal/ranker`/sidecar, que o coloque no caminho-padrão sem
  fail-open, ou que o deixe selecionar estrato, está errado e corrompe a semântica
  contábil que os golden tests protegem.
- **O deep ranking é gated por dados que ainda não temos.** A prova de uplift A/B
  exige **tráfego real**, que hoje é **pendência de infra da Fase 2** (cutover
  bloqueado neste ambiente). Logo, **o código de serving deep é construível agora**
  (scaffolding atrás de flag, desligado por padrão), mas a **promoção do deep é um
  gate aberto** até o A/B em tráfego real provar uplift sobre o GBDT. Isto precisa
  ficar explícito no sequenciamento para não confundir "código pronto" com "promovido".
- **Cripto/AEV/BND traz blast radius financeiro e regulatório, não de latência.** O
  pagamento **nunca entra no hot path** (§3: "pagamentos ficam 100% fora do hot path;
  só budgets pré-computados influenciam pacing"). O risco é **financeiro** (precisão
  decimal por ativo, dupla contagem, custódia, finalidade on-chain) e **regulatório**
  (PCI, Travel Rule, KYC, jurisdição). Sem um gate que trave a **interface
  `ChainConnector`**, o **modelo de custódia**, a **resolução das 10 perguntas abertas
  de §3** e a **segregação em células**, `payments-crypto-engineer`,
  `money-ledger-guardian` e `platform-infra-engineer` colidem em fronteiras de
  contabilidade, segredos e escopo de conformidade.
- **As 10 perguntas abertas de §3 bloqueiam a Fase 3 cripto** — explicitamente: "que
  precisam de resposta antes da Fase 3 (cripto)". Em modo de **execução autônoma**,
  cada uma recebe **opção recomendada + gatilho de reabertura mensurável** (estilo
  ADR-0002 §B / ADR-0003 §E); onde a pergunta for um **bloqueio de produto que código
  nenhum resolve** (ex.: quem detém as chaves do supply AEV/BND, `scale`/decimals
  oficiais, classificação regulatória), registra-se a **premissa adotada** e o que a
  **reabriria** — mas **não se para o sequenciamento** do que é construível.
- **A regra de ouro vale dobrado aqui.** A Fase 3 é onde mora a tecnologia "pesada"
  do roadmap: Triton/GPU, Fireblocks, TigerBeetle. Cada uma entra **sob gatilho
  mensurável**, nunca por aspiração: **Triton/GPU só sob uplift A/B provado** (Safe:
  GBDT-CPU primeiro), **Fireblocks só quando o AUM justificar** (Safe multisig
  primeiro — não pagar US$10–50k/mês antes da 1ª transação, §2.6), **TigerBeetle só
  sob gargalo de escrita financeira provado** (Postgres double-entry primeiro, §2.6/§5).

Este ADR é **pré-requisito do fan-out da Fase 3**: trava o ponto de extensão do deep
na cascata (reuso do ADR-0003 §A, sem reabrir budget), a interface `ChainConnector` e
o modelo de custódia, a resolução das 10 perguntas de §3 com defaults + gatilhos, a
segregação em células e os gates de promoção (uplift A/B para o deep; finalidade +
reconciliação para cripto) — **antes da primeira linha de modelo deep, conector
on-chain ou trilho de pagamento**. Não escreve modelo, conector nem trilho linha a
linha.

## Decisão

### A. Deep ranking — substitui o GBDT ATRÁS DO MESMO ponto de extensão, gated por uplift A/B

**Adotamos o deep ranking (two-tower DCN-v2/DLRM em Triton/GPU) como uma nova versão
de modelo servida ATRÁS DO MESMO `internal/ranker`/sidecar ratificado no ADR-0003 §A
e §B, promovida exclusivamente pelo mesmo arcabouço de A/B + kill-switch + OPE da
Fase 2 — nunca como novo ponto de extensão, nunca com exceção de budget ou de
autoridade.** Invariantes não-negociáveis:

- **DA-3 / TX-4 — a fronteira da Fase 2 é reusada, não reaberta.** O deep continua
  sendo **re-ranker dentro do estrato vencedor** (a cascata pura resolve a autoridade
  **primeiro**; o deep re-ordena candidatos do **mesmo** `Candidate.tier`). O
  **timeout duro + fail-open determinístico** do ADR-0003 §A vale igual: se o deep
  estourar/falhar, degrada para a cascata pura, marca `ml_fail_open = true`,
  `exploration_policy = DETERMINISTIC`, `propensity = 1.0`. **O budget de 5–8 ms p99
  do ADR-0002 §B.2 NÃO é ampliado por causa do deep** — ou o deep cabe (INT8 + torre
  de demanda pré-computada offline), ou serve um modelo mais barato, ou degrada com
  mais frequência. Reabrir o budget exige ADR sucessor com número medido (ver Gatilho).
- **Mesmo arcabouço de promoção, sem atalho.** O deep é registrado e promovido por
  `ml/registry/promote_model.py` (MLflow), avaliado por `ml/ope` (IPS/SNIPS/DR
  filtrando `ml_fail_open`), servido primeiro em **shadow** (`internal/ranker/shadow.go`)
  e só ativado em **A/B por zona/tenant com guarda de receita + kill-switch**
  (`internal/ranker/{ab,guard}.go`). **Nada de deep promovido sem prova de uplift A/B
  sobre o GBDT da Fase 2** — esta é a tradução literal de "só se A/B provar uplift"
  (§2.3/§4) e da regra de ouro (§5: Triton/GPU é upgrade justificado por dados).
- **Triton/GPU é justificado pelo deep, não pelo deep ser desejável.** O sidecar
  ganha um **runtime Triton (GPU)** como **alvo de serving alternativo** ao
  Treelite/ONNX-CPU do GBDT, selecionado por `model_version`. A escolha de runtime
  (Triton vs. ONNX-GPU) fica com o `ml-optimization-engineer` — é escolha de runtime,
  não de arquitetura (precedente ADR-0003 §B). **GPU só existe se o deep estiver em
  A/B ativo**; em produção sem deep promovido, o hot path roda **só CPU** (GBDT).
- **Semântica contábil intacta.** O deep **não** altera o que é faturável nem a
  semântica da cascata. **Os golden tests da Fase 1/2 continuam verdes** com o deep
  desligado, em shadow, e em fail-open (a degradação reproduz a cascata pura
  bit-a-bit). O `parity-golden-test-guardian` é o dono dessa verificação.

**Por que reusar o ponto de extensão e não criar um "deep path":** o ADR-0003
Consequências/Fase 3 já antecipou literalmente — "deep ranking entra **substituindo o
GBDT** atrás do **mesmo** `internal/ranker`/sidecar (agora Triton/GPU) **só sob uplift
A/B**". `ml/` e o ponto de extensão já comportam isso **sem reabrir o layout**. Criar
um caminho paralelo duplicaria fail-open, OPE e gates — dívida operacional sem ganho.

### B. Fraude não-supervisionada — complementa o GBDT supervisionado, ainda FORA do hot path (TX-6)

**Adotamos os modelos não-supervisionados de fraude (autoencoder / Isolation Forest /
GNN) como complemento ao GBDT supervisionado de IVT da Fase 2 em `ml/fraud`, rodando
na ingestão antes do `StatsHourly`/faturamento, marcando IVT — nunca decidindo
veiculação nem entrando no hot path.** Invariantes:

- **TX-6 literal.** A fraude roda **na ingestão** (latência de minutos aceitável,
  ADR-0001), **antes** do `StatsHourly` e do faturamento, e **marca eventos IVT** para
  que CPM/CPC/CPA faturem **só tráfego válido**. O não-supervisionado **complementa** o
  supervisionado: cobre padrões novos (anomalia, grafos de coordenação) que o GBDT
  rotulado não pega. **Nenhum modelo de fraude toca o `internal/ranker` nem o motor de
  decisão.**
- **Reconciliação contra o lakehouse, nunca contra o streaming.** A marcação de IVT
  alimenta o faturamento, que reconcilia contra **Iceberg** (invariante normativa) —
  o ClickHouse "ao vivo" nunca soma com o faturável.
- **Treinável agora sobre sample sintético.** O não-supervisionado é **construível
  como código agora** (reusa `ml/fraud/generate_ivt_sample.py` e a featurização de
  fraude da Fase 2), com **dados reais pendentes de infra** para validação final —
  igual ao deep, o **código é construível**, a **eficácia em produção é gated** por
  tráfego real.

### C. Interface `ChainConnector` única + modelo de custódia — Safe multisig primeiro, Fireblocks sob AUM

**Adotamos uma interface `ChainConnector` única (`watchDeposits`, `getBalance`,
`buildPayout`, `confirmations`) como ÚNICO ponto de contato com qualquer chain, com
custódia inicial em Safe multisig e Fireblocks (MPC) como upgrade sob AUM.** O cripto
vive **inteiramente fora do hot path** (§3). Invariantes:

- **`ChainConnector` é a fronteira plugável.** Conforme §2.6: `viem` (TS) +
  `web3.py` (lado ML); para AEV/BND **EVM** (decisão E.1), `viem`/Fireblocks direto;
  para uma eventual **chain própria não-EVM**, implementa-se `ChainConnector` com SDK
  nativo (único cenário que justifica signer/indexer/confirmações próprios). **Todo
  conector implementa a mesma interface** — trocar Safe→Fireblocks ou EVM→não-EVM é
  trocar implementação, não reescrever o trilho.
- **Custódia: Safe multisig primeiro (§2.6 / regra de ouro / §5).** O início usa
  **Safe (multisig)** + automação (OpenZeppelin Defender/Gelato). **Fireblocks (MPC) é
  UPGRADE sob AUM** — não se paga US$10–50k/mês antes da 1ª transação. O `custody_mode`
  do Asset Registry já modela isso (`safe_multisig` | `mpc_fireblocks` |
  `client_supply` | `none`, confirmado na migration da Fase 1) — a troca é mudança de
  linha + conector, **sem migração de schema**.
- **Finalidade antes de saldo.** Depósito cripto fica **`pending` até finalidade**
  (N confirmações); preferir **webhook do custodiante** a lógica de reorg própria
  (§2.6). O estado `pending` já existe no ledger da Fase 1 (enum `ledger.entry_status`).
  Reorg → **estorno auditável** (novo par de postings, nunca edição).
- **Pagamento fora do hot path, só budget pré-computado influencia pacing.** O
  `ChainConnector`, os webhooks de custódia e a reconciliação rodam em
  `services/payments` (E.0/layout), **nunca** no caminho de decisão. O pacing (DA-4) lê
  **budgets pré-computados**, não consulta saldo on-chain em tempo de decisão.
- **Cripto fora do cliente (§2.5).** A UI só consome **status via BFF de pagamentos**;
  `wagmi/viem/WalletConnect` no front **apenas se** a spec de AEV/BND exigir assinatura
  on-chain pelo anunciante (E.9). Nunca cripto no cliente por padrão.

**Gatilho de reabertura (Fireblocks):** migra-se Safe→Fireblocks **só** quando o **AUM
sob custódia** ultrapassar um piso que justifique o custo MPC (alvo de referência a
calibrar com o cliente; medir AUM real em produção antes de fixar — referência:
quando o custo anual de Fireblocks for **< 1% do AUM** e o volume de payouts exigir
política MPC de assinatura) — com o número de AUM medido anexado ao ADR sucessor.

### D. Ledger e Asset Registry — Postgres double-entry idempotente recebe AEV/BND SEM migração de schema; TigerBeetle só sob gargalo provado

**Adotamos o ledger Postgres double-entry da Fase 1 como única fonte da verdade
contábil também para AEV/BND, recebendo os tokens como linhas do Asset Registry sem
migração de schema, com TigerBeetle deferido até gargalo de escrita financeira
provado.** Invariantes:

- **Sem migração de schema (verificado).** As colunas que AEV/BND exigem **já existem**
  na migration da Fase 1: `code`, `scale` (NULL-até-`enabled`), `kind`
  (`fiat`|`stablecoin`|`crypto`|`token`), `chain_id`, `contract_address`,
  `custody_mode`, `price_source`, `price_governance`. **As linhas AEV/BND já estão
  seedadas** na Fase 1 (`enabled=false`, `scale=NULL`, `custody_mode='client_supply'`,
  `price_source='administered'`, `price_governance='Comite de Tesouraria'`). Habilitar
  AEV/BND é **definir `scale` e virar `enabled=true`** (E.2) + cablear o trilho, não
  alterar o schema — exatamente o que o roadmap promete ("Asset Registry recebe AEV/BND
  sem migração de schema"). O CHECK `assets_enabled_needs_scale_chk` **proíbe** habilitar
  um ativo sem `scale` (barreira estrutural contra E.2); o CHECK
  `assets_administered_needs_governance_chk` **exige** `price_governance` quando o preço
  é administrado (E.4).
- **TX-2 / DA-10 inquebrantáveis.** Todo valor é `Money(asset, integer, scale)`;
  **float proibido em CI** (`money-ledger-guardian` é gate). **Multi-moeda sem conversão
  automática (DA-10):** cada ativo é ledger isolado; câmbio AEV/BND↔fiat/USDC só como
  **par de postings explícito** com taxa registrada por humano/desk — **jamais** câmbio
  implícito. eCPM compara candidatos **dentro da mesma moeda/tenant**.
- **Invariantes contábeis da Fase 1 valem para cripto.** Par de postings idempotente
  (`idempotency_key` por captura); **nenhuma captura grava saldo direto** (saldo é
  derivado de `postings`); `sum(debit)=sum(credit)` por `journal_entry`; reconciliação
  periódica **abre exceções e nunca autocorrige** (§2.6). Depósito cripto `pending` até
  finalidade (C).
- **BND com accruals: modelado SE e somente SE a spec exigir (E.7).** "Bond" **pode**
  implicar rendimento/maturidade/cupom. Se a resposta de produto confirmar
  rendimento, o ledger modela **accruals como pares de postings periódicos** (juro
  provisionado = par débito/crédito datado), **sem** UPDATE de saldo — coerente com o
  double-entry. Até a confirmação, **BND é tratado como token simples** (premissa
  adotada, ver E.7).

**Gatilho de reabertura (TigerBeetle):** migra-se o ledger para TigerBeetle **só** sob
**gargalo de escrita financeira PROVADO** (§2.6/§5) — alvo de referência: latência de
escrita do par de postings no Postgres **> orçamento contábil aceitável de forma
sustentada** sob carga real de produção (medir TPS de postings e p99 de commit antes de
fixar o piso). Nunca por "1M tps" aspiracional — reconciliar dois motores contábeis é
custo que só se paga sob gargalo medido.

### E. Resolução das 10 perguntas abertas de §3 (AEV/BND) — defaults recomendados + gatilhos

Cada pergunta de §3 recebe **opção recomendada + gatilho de reabertura** (autonomia).
Onde for bloqueio de produto que código nenhum resolve, registra-se **premissa
adotada** + o que a reabriria, **sem parar** o que é construível.

#### E.0 — Onde vive o trilho de pagamento: `services/payments` (Go) + `ChainConnector` em TS/ML, fora do hot path
Novo serviço `services/payments` orquestra fiat (Stripe/Asaas), cripto (Safe→Fireblocks
via `ChainConnector`) e reconciliação contra o ledger Postgres — **fora do hot path**,
na **célula AML/KYC** (F). O BFF expõe **só status** ao cliente.

#### E.1 — On-chain ou off-chain; EVM vs. não-EVM → **token ERC-20 em chain EVM**
**Default:** AEV/BND **on-chain, ERC-20 em chain EVM** → `viem`/Fireblocks direto, sem
signer/indexer/oráculo nativos (§3 q.1; §2.6). **Gatilho de reabertura:** se a spec de
produto exigir **chain própria não-EVM**, implementa-se `ChainConnector` com SDK nativo
(o único cenário que justifica infra própria) — a interface já isola essa troca (C).

#### E.2 — `scale`/decimals → **premissa: 18 decimais (padrão ERC-20), TBD oficial; BLOQUEIO de produto travado por CHECK**
**Premissa adotada:** `scale = 18` (padrão ERC-20) até a spec oficial. **Este é o dado
mais crítico (§3 q.2): sem ele não há aritmética correta.** O schema **já protege**:
`assets_enabled_needs_scale_chk` **impede habilitar AEV/BND sem `scale` definido**. O
que reabriria: a **spec oficial** de decimais — bloqueio de produto, não de código.
Até lá, AEV/BND ficam `enabled = false` (sem aritmética em produção); o código de
trilho é construível e testado contra a premissa de 18 decimais.

#### E.3 — Classificação regulatória → **premissa: utility/payment token; pipeline de compliance construído como se security-adjacent (conservador)**
**Premissa adotada:** AEV/BND como **utility/payment token** (não-security) até parecer
jurídico. **Default de engenharia conservador:** o pipeline de compliance (Sumsub
KYC/KYB + Chainalysis + Travel Rule) é construído **como se** houvesse exigência
security-adjacent — desligar é barato; ligar tarde é caro. **Bloqueio de produto:** a
classificação oficial (MiCA/BACEN/CVM) reabre KYC/custódia/contabilidade (§3 q.3).
O que reabriria: parecer regulatório formal.

#### E.4 — Preço → **administrado/governado no Asset Registry; oráculo só sob ativo líquido**
**Default:** `price_source = 'administered'` com **`price_governance` obrigatório**
(CHECK `assets_administered_needs_governance_chk` já existe) — mitiga conflito de interesse de quem
define o preço (§3 q.4 / §2.6). **Gatilho de reabertura:** Chainlink/Pyth (`oracle_*`)
**só quando houver ativo líquido com feed real** — troca de linha `price_source`, sem
migração.

#### E.5 — Custódia → **Safe multisig; Fireblocks sob AUM (ver C); supply é BLOQUEIO de produto**
**Default:** **Safe multisig** (`custody_mode = 'safe_multisig'`), Fireblocks sob AUM
(gatilho em C). **Bloqueio de produto (§3 q.5):** **quem detém as chaves do supply
AEV/BND** (cliente controla mint/burn = `client_supply`?) é decisão de produto que
código nenhum resolve. **Premissa adotada:** plataforma **não** controla o supply
(cliente custodia o supply; plataforma custodia só os saldos operacionais via Safe).
O que reabriria: definição contratual de quem detém as chaves de mint/burn.

#### E.6 — Liquidez / on-off ramp → **USDC (Circle Mint) como ramp; sem ramp = crédito fechado (premissa)**
**Default:** **USDC** como stablecoin/ramp (USDT por alcance), §2.6. **Premissa
adotada:** enquanto **não houver desk/exchange** que troque AEV/BND↔fiat/stablecoin, o
faturamento em AEV/BND é **crédito fechado no ecossistema** (§3 q.6) — o ledger modela
isso naturalmente (saldo em ativo isolado, sem câmbio implícito — DA-10). O que
reabriria: contrato com desk/exchange → câmbio vira par de postings com taxa registrada.

#### E.7 — Mecânica especial (rebasing/fee/pause/blocklist/upgrade); BND = cupom? → **premissa: token simples; accruals SE confirmado (ver D)**
**Premissa adotada:** AEV/BND como **token ERC-20 simples** (sem rebasing, sem
transfer-fee, sem rebase de saldo) até a spec. **BND = "Bond" pode implicar
rendimento/maturidade/cupom (§3 q.7):** se confirmado, o ledger modela **accruals como
pares de postings periódicos** (D). **Bloqueio de produto:** a mecânica final do
contrato. O que reabriria: a spec do contrato (ABI + comportamento de transferência).
**Risco travado:** o `ChainConnector` lê `confirmations`/`getBalance` da chain (verdade
on-chain), **nunca** assume mecânica não-documentada.

#### E.8 — Finalidade/confirmações/reorg → **`pending` até N confirmações via webhook do custodiante (ver C)**
**Default:** depósito `pending` até **N confirmações** (N por chain, config no Asset
Registry/conector); finalidade via **webhook do custodiante** (preferir a lógica de
reorg própria — §2.6); reorg → estorno auditável (par de postings). O que reabriria:
chain própria não-EVM com modelo de finalidade distinto (acopla a E.1).

#### E.9 — Fluxo de uso (entrada/payout; anunciante assina vs. plataforma custodia) → **plataforma custodia e move (default); assinatura on-chain só sob spec**
**Default:** **plataforma custodia** (Safe) e move por conta do anunciante — entrada
(pagar campanhas) **e** saída (payout a publishers). Assinatura on-chain pelo
anunciante (`wagmi/viem/WalletConnect` no front) **só se** a spec exigir (§3 q.9 / §2.5)
— mantém **cripto fora do cliente** por padrão. O que reabriria: requisito de
self-custody do anunciante.

#### E.10 — Travel Rule / screening / KYC → **Sumsub (KYC/KYB) + Chainalysis (screening) + Travel Rule no trilho cripto; Brasil/BACEN + global**
**Default:** **Sumsub** (KYC/KYB, forte no Brasil) + **Chainalysis** (screening
on-chain) + **Travel Rule** e screening de sanções no trilho cripto (§2.6 / §3 q.10),
isolados na **célula AML/KYC** (F). PII/KYC em **cofre de compliance** referenciado por
`tenant_id` pseudônimo (TX-3) — **ledger e telemetria sem PII** (DA-11/TX-5). O que
reabriria: nova jurisdição com exigência de Travel Rule distinta.

### F. Segregação em células — célula PCI de escopo mínimo + célula AML/KYC/Travel Rule

**Adotamos duas células de segregação adicionais na Fase 3 (§2.7), de escopo mínimo,
com fronteiras de rede rígidas validáveis por QSA.** Invariantes:

- **Célula PCI** (Stripe SAQ-A): conta cloud separada, Cilium deny-all, escopo mínimo
  (tokenização client-side via Elements/Checkout — o cartão **nunca** transita pelo
  nosso backend; SAQ-A). O `security-reviewer` valida que o PCI **não escapa da célula**
  (risco §5).
- **Célula AML/KYC/Travel Rule** (cripto): isola Sumsub/Chainalysis, o cofre de
  compliance (PII/KYC pseudônimo) e o `services/payments` cripto. **Cilium deny-all**;
  segredos (chaves de custódia, API keys) em **OpenBao/Vault** + KMS/HSM — nada estático
  em imagem/git (§2.7/TX-3).
- **Hot path intocado.** As células são control-plane/fora-do-hot-path; o motor de
  decisão na borda **não** ganha dependência de PCI/AML. O `platform-infra-engineer` é
  dono da segregação; `security-reviewer`/`privacy-compliance-auditor` são gate de merge.

### G. Política de layout da Fase 3 — diretórios novos (estende a árvore do ADR-0002/0003)

A Fase 3 cria os diretórios abaixo, respeitando as fronteiras já ratificadas
(`proto/` = fonte de eventos; `db/` = relacional; `data/` = analítico; **módulo Go
único**; `services/` = binários; `internal/` = pacotes Go compartilhados; `ml/` =
Python, fora do go.mod). O que já existe está marcado *(existe)*.

```text
.
├── internal/
│   └── ranker/                 # (existe — Fase 2) — Fase 3 NÃO cria ponto novo:
│                               # o deep é servido por model_version atrás do MESMO
│                               # cliente; ab.go/guard.go/shadow.go REUSADOS.
│
├── services/
│   ├── decision/               # (existe) — INALTERADO (deep entra por model_version)
│   ├── collector/              # (existe) — Fase 3 PLUGA fraude não-superv. na ingestão
│   ├── ranker-sidecar/         # (existe — Fase 2) — Fase 3 ADICIONA runtime Triton/GPU
│   │   └── ...                 # como alvo alternativo ao ONNX-CPU; seleção por model_version
│   ├── copilot/                # (existe) — INALTERADO
│   └── payments/               # NOVO (Go) — trilho multi-trilho FORA DO HOT PATH:
│       └── ...                 # fiat (Stripe/Asaas), cripto (ChainConnector), webhooks
│                               # de custódia, reconciliação contra o ledger. Célula AML/KYC.
│
├── internal/
│   └── chainconnector/         # NOVO (Go) — interface ChainConnector ÚNICA
│       └── ...                 # (watchDeposits/getBalance/buildPayout/confirmations);
│                               # impls EVM (Safe/Fireblocks). FORA do hot path.
│
├── ml/                         # (existe — Fase 2)
│   ├── deep/                   # NOVO (Python) — two-tower DCN-v2/DLRM (treino PyTorch,
│   │                           # export INT8/ONNX→Triton); REUSA ml/features (anti-skew),
│   │                           # ml/ope, ml/registry/promote_model.py. Gated por uplift A/B.
│   └── fraud/                  # (existe) — Fase 3 ADICIONA autoencoder/IsolationForest/GNN
│       └── ...                 # não-superv., complementando o GBDT de IVT (TX-6, ingestão)
│
├── db/
│   ├── ledger/                 # (existe — Fase 1) — SEM migração nova p/ AEV/BND;
│   │                           # Fase 3 popula contas/postings cripto + accruals BND (se spec)
│   ├── asset_registry/         # (existe — Fase 1) — SEM migração; Fase 3 POPULA linhas AEV/BND
│   │                           # (scale TBD, enabled=false até spec oficial — CHECK protege)
│   └── compliance/             # NOVO — cofre de compliance (PII/KYC pseudônimo, RLS),
│       └── migrations/         # referenciado por tenant_id pseudônimo. Célula AML/KYC.
│
├── proto/
│   └── adserver/
│       └── payments/v1/        # NOVO — eventos de pagamento/custódia (BACKWARD-compat,
│           └── *.proto         # buf TX-1); Money via proto/adserver/money/v1 REUSADO.
│
├── platform/
│   ├── cells/pci/              # NOVO — célula PCI (conta separada, Cilium deny-all)
│   └── cells/aml-kyc/          # NOVO — célula AML/KYC/Travel Rule
│
└── bff/
    └── src/routers/payments.ts # NOVO — status de pagamentos via BFF (NUNCA cripto no cliente)
```

**Princípios de fronteira da Fase 3 (herdados e estendidos):**

- **Cripto NUNCA toca o hot path.** `services/payments` + `internal/chainconnector`
  são control-plane; o motor de decisão e o `internal/ranker` **não** ganham dependência
  de pagamento/custódia. O pacing lê **budget pré-computado**, não saldo on-chain.
- **Deep NÃO cria ponto de extensão novo.** Entra por `model_version` atrás do
  `internal/ranker`/sidecar existente; `ab.go`/`guard.go`/`shadow.go` reusados.
- **AEV/BND sem migração de schema.** São **linhas** no Asset Registry existente; o
  CHECK `assets_enabled_needs_scale_chk` é a barreira estrutural contra E.2.
- **PII/KYC isolado em `db/compliance` (célula AML/KYC), referenciado por `tenant_id`
  pseudônimo.** Ledger e telemetria **sem PII** (DA-11/TX-5). Qualquer `.proto` novo é
  **BACKWARD-compat** (TX-1, gate buf) e reusa `Money` (TX-2).

### Fora do escopo deste ADR

- **Reabertura do budget de 5–8 ms p99** (ADR-0002 §B.2) — o deep cabe ou degrada; só
  sob gatilho medido (abaixo).
- **Reabertura do ponto de extensão do ML na cascata** (ADR-0003 §A) — o deep reusa.
- **Near-real-time / Flink** — deferido sob o gatilho do ADR-0001 (não confirmado).
- **Multi-touch attribution** — sob o gatilho do ADR-0002 §B.7; mantém-se last-click 7d.
- **Spec oficial de AEV/BND** (decimais, classificação regulatória, mecânica de
  contrato, quem detém o supply) — **bloqueios de produto** registrados em E com
  premissa + o que reabre; **não** resolvidos por este ADR (código nenhum os resolve).
- Não escreve modelo deep, conector on-chain, trilho de pagamento nem migração de
  compliance **linha a linha** — delegado aos engenheiros de camada no fan-out seguinte.

### H. Sequenciamento da Fase 3 em incrementos (K0…K8)

A Fase 3 é construída em **9 incrementos** com dependências explícitas. Cada incremento
lista o que **fecha** (verde só após validação do guardião dono) e os **invariantes**
que preserva. **Dois eixos independentes rodam em paralelo:** o **eixo IA** (K1 deep,
K2 fraude) e o **eixo cripto/pagamentos** (K3→K8), unidos só por gates de merge comuns
(buf/no-float/security/privacy). **K0 é pré-requisito do eixo cripto** (interface +
células antes de qualquer trilho). A coluna "Construível agora vs. gated" deixa
explícito o que é código vs. o que aguarda tráfego real (uplift A/B) ou spec de produto.

| Inc | Tema | Depende de | Escopo | Invariantes | Subagentes | Construível agora vs. gated |
|---|---|---|---|---|---|---|
| **K0** | Fundações cripto: `ChainConnector` + células + Asset Registry AEV/BND + proto pagamentos | — (Fases 0–2 completas) | `internal/chainconnector` (interface única, impl EVM stub); `services/payments` esqueleto; `proto/adserver/payments/v1` (BACKWARD-compat); **AEV/BND no Asset Registry** já seedados na Fase 1 (`enabled=false`, `scale` TBD) — K0 valida e mantém disabled; `platform/cells/{pci,aml-kyc}` (Cilium deny-all). | TX-1 (buf BACKWARD); TX-2/DA-10 (Money, sem float, sem câmbio implícito); §3 q.2 travada por CHECK; cripto **fora do hot path** (C). | `payments-crypto-engineer` + `schema-contracts-steward` + `platform-infra-engineer` (+ `money-ledger-guardian` no Asset Registry) | **Construível agora** (interface, scaffolding, linhas disabled, células). |
| **K1** | Scaffolding de serving deep gated por flag | — (reusa Fase 2) | `ml/deep` (two-tower DCN-v2/DLRM, treino PyTorch sobre Iceberg, export INT8/ONNX→Triton, **reusa `ml/features`**); runtime **Triton/GPU** no `ranker-sidecar` como alvo por `model_version`, **atrás de flag, desligado por padrão**. | DA-3/TX-4 (re-rank dentro do estrato, **timeout duro + fail-open**, budget 5–8 ms **não** ampliado); anti-skew (função única, teste de paridade). | `ml-optimization-engineer` (+ `decision-engine-engineer` na fiação do `internal/ranker`) | **Construível agora** o código; treino real e GPU pendem de infra. |
| **K2** | Fraude não-supervisionada (autoencoder/IsolationForest/GNN) na ingestão | — (reusa `ml/fraud` da Fase 2) | `ml/fraud` ganha modelos não-superv. **complementando** o GBDT de IVT; treináveis sobre **sample sintético** (`generate_ivt_sample.py`); marcação IVT **antes** do `StatsHourly`/faturamento. | TX-6 (fora do hot path, marca IVT); reconciliação contra **Iceberg**, nunca streaming. | `ml-optimization-engineer` + `data-platform-engineer` | **Construível agora** sobre sample sintético; eficácia gated por tráfego real. |
| **K3** | Ledger cripto + Asset Registry vivo + reconciliação | K0 | Contas/postings cripto no **ledger Postgres existente** (sem migração); par de postings idempotente, `pending` até finalidade, saldo derivado; reconciliação periódica (**abre exceções, nunca autocorrige**); câmbio AEV/BND↔USDC só como **par de postings explícito**. | TX-2/DA-10 (float proibido; sem conversão automática); invariantes contábeis Fase 1; reconcilia contra ledger/lakehouse. | `money-ledger-guardian` + `payments-crypto-engineer` | **Construível agora** (schema existe); valores reais pendem de spec `scale`. |
| **K4** | Trilho fiat: Stripe SAQ-A + Asaas/PIX | K0 | `services/payments` integra **Stripe** (Payment Intents/Billing/Tax, tokenização client-side SAQ-A) na **célula PCI**; **Asaas/PIX** (QR dinâmico, Pix Automático, conciliação txid/E2E); Mercado Pago failover. | PCI **não escapa da célula** (F); par de postings idempotente no ledger (K3); sem PII fora do cofre. | `payments-crypto-engineer` + `platform-infra-engineer` (+ `security-reviewer` gate PCI) | **Construível agora** (sandbox Stripe/Asaas); chaves vivas pendem de infra. |
| **K5** | Trilho cripto: Safe multisig + USDC via `ChainConnector` | K0, K3 | Impl EVM real do `ChainConnector` (viem) com **Safe multisig**; **USDC** como ramp; depósito `pending` até **N confirmações via webhook** do custodiante; reorg → estorno auditável; **Fireblocks deferido sob AUM** (gatilho C). | Cripto **fora do hot path** (C); finalidade antes de saldo (E.8); custódia Safe (E.5). | `payments-crypto-engineer` + `money-ledger-guardian` | **Construível agora** (testnet/Safe); AEV/BND reais gated por `scale` (E.2). |
| **K6** | Compliance: célula AML/KYC + Sumsub + Chainalysis + Travel Rule | K0, K5 | `db/compliance` (cofre PII/KYC pseudônimo, RLS); **Sumsub** (KYC/KYB); **Chainalysis** (screening on-chain); **Travel Rule** no trilho cripto; tudo na **célula AML/KYC**. | TX-3 (PII isolada, pseudônimo); TX-5/DA-11 (ledger/telemetria sem PII); Travel Rule/sanções (E.10). | `payments-crypto-engineer` + `platform-infra-engineer` + `privacy-compliance-auditor` (gate) | **Construível agora** (sandbox Sumsub/Chainalysis); jurisdição final gated por produto. |
| **K7** | BFF + UI de pagamentos (status), cripto fora do cliente | K4, K5 | `bff/src/routers/payments.ts` (status via BFF); UI self-service de faturamento/saldo; **Money como string DECIMAL + rótulo**, nunca `Number`/aritmética no cliente; **sem cripto no front** (wagmi só sob E.9). | TX-2 (sem aritmética monetária no cliente); TX-3 (BFF injeta tenant, protege segredos); cripto fora do cliente (C/§2.5). | `frontend-bff-engineer` (+ `security-reviewer` gate) | **Construível agora.** |
| **K8** | Promoção do deep sob uplift A/B (GATE) | K1 + tráfego real (infra Fase 2) | `ml/ope` (IPS/SNIPS/DR filtrando `ml_fail_open`); **shadow** do deep; **A/B por zona/tenant + guarda de receita + kill-switch**; promoção de `model_version` (Triton) via MLflow **só com uplift provado** sobre o GBDT. | **Nada promovido sem uplift A/B + kill-switch**; DA-3 (cascata autoridade final); golden tests verdes (deep não muda faturável); budget 5–8 ms (degrada se não couber). | `ml-optimization-engineer` + `parity-golden-test-guardian` (gate) | **GATED** por tráfego real (cutover Fase 2) — código pronto em K1; promoção espera o número. |

**Regras de sequenciamento e pontos de junção:**

- **Dois eixos paralelos disjuntos.** Eixo **IA** (K1, K2) e eixo **cripto/pagamentos**
  (K0→K3→{K4,K5}→{K6,K7}) não compartilham arquivos (Python ML / Go-decision vs.
  Go-payments / db-ledger / proto-payments / cells). Rodam em paralelo após as fases
  anteriores, unidos só pelos gates de merge comuns.
- **K0 é gate do eixo cripto:** nenhum trilho (K4/K5) nem ledger cripto (K3) começa
  antes da interface `ChainConnector`, das células e das linhas AEV/BND. K3 depende de
  K0 (Asset Registry vivo); K4/K5 dependem de K0 (interface/células) e K5 de K3 (ledger).
- **K8 é o gate de produção do deep:** o deep **só serve tráfego real em K8**, e **só**
  com uplift A/B provado sobre o GBDT da Fase 2. K1 entrega o **código** (flag desligada);
  K8 entrega a **promoção** — separados de propósito, porque o uplift depende de tráfego
  real (pendência de infra da Fase 2, declarada como gate aberto).
- **Gate de paridade contínuo:** os golden tests das Fases 1/2 rodam em K1 e K8 — o
  `parity-golden-test-guardian` confirma que **o deep não alterou a semântica contábil
  da cascata** (cascata pura ≡ fail-open; faturável inalterado).
- **Gates de merge (todos os incrementos):** `make verify` verde (buf TX-1 +
  no-float TX-2); `security-reviewer` + `privacy-compliance-auditor` sem CRITICAL/HIGH
  (K4 exige PCI fora da célula; K5/K6 exigem segredos OpenBao + Travel Rule + PII
  isolada); `money-ledger-guardian` em **todo** incremento com valor monetário
  (K0/K3/K4/K5). **Nenhum deep promovido sem uplift A/B + kill-switch (K8); nenhum ativo
  AEV/BND habilitado sem `scale` oficial (CHECK estrutural, E.2); nenhuma captura grava
  saldo direto (ledger, K3).**

## Gatilho de reabertura

Cada decisão A…F carrega seu **próprio gatilho de reversão mensurável** acima
(Fireblocks por AUM em C; TigerBeetle por gargalo de escrita em D; oráculo de preço por
liquidez em E.4; chain não-EVM em E.1). Globalmente, este ADR é reaberto se:

- **O deep não couber no budget de 5–8 ms p99** (ADR-0002 §B.2) de forma sustentada com
  o modelo de produção em A/B (medido) — sintoma que reabre a discussão de serving
  (poda/INT8 mais agressivo, ou degradar para o GBDT/cascata com mais frequência), com
  o número medido anexado. **O budget não se amplia por aspiração** — ou o deep cabe,
  ou não é promovido (K8).
- **Uma das premissas de produto E.2/E.3/E.5/E.7** (decimais, classificação, supply,
  mecânica do contrato) for resolvida pela spec oficial de AEV/BND — reabre as decisões
  de ledger/custódia/contabilidade que dependem dela, com a spec anexada.

## Alternativas consideradas

- **Deep ranking como novo ponto de extensão / "deep path" paralelo ao GBDT.**
  Rejeitada: duplica fail-open, OPE, gates e budget; o ADR-0003 §A já antecipou o deep
  **atrás do mesmo** `internal/ranker`/sidecar por `model_version`. Caminho paralelo é
  dívida operacional sem ganho.
- **Triton/GPU adotado já em K1 em produção (sem uplift A/B).** Rejeitada
  categoricamente: viola a regra de ouro (§5, Triton é upgrade justificado por dados) e
  o roadmap ("só se A/B provar uplift", §2.3/§4). GPU só existe sob deep em A/B ativo;
  produção sem deep promovido roda só CPU. K1 entrega código gated; K8 promove sob prova.
- **Fireblocks (MPC) desde o início.** Rejeitada: US$10–50k/mês antes da 1ª transação
  (§2.6) é over-engineering. Safe multisig primeiro; Fireblocks sob AUM (gatilho C). A
  interface `ChainConnector` torna a troca uma mudança de implementação, não reescrita.
- **TigerBeetle como ledger desde a Fase 3.** Rejeitada: reconciliar dois motores
  contábeis é custo que só se paga sob **gargalo de escrita PROVADO** (§2.6/§5); o
  Postgres double-entry da Fase 1 atende e já tem `pending`/idempotência/`NUMERIC`.
  Gatilho de migração documentado (D).
- **Migrar o schema do Asset Registry/ledger para receber AEV/BND.** Rejeitada por
  desnecessária: as colunas (`scale`, `kind`, `chain_id`, `contract_address`,
  `custody_mode`, `price_source`, `price_governance`) e o estado `pending` **já existem**
  desde a Fase 1. AEV/BND entram como **linhas** — o roadmap promete isso e o schema
  cumpre. O CHECK `assets_enabled_needs_scale_chk` protege contra habilitar sem `scale`.
- **Resolver as 10 perguntas de §3 antes de qualquer código (esperar a spec).**
  Rejeitada: paralisaria a Fase 3 indefinidamente. Em modo autônomo, cada pergunta
  recebe default + gatilho (E); os **bloqueios genuínos de produto** (decimais,
  classificação, supply, mecânica) viram **premissa adotada + o que reabre**, sem parar
  o que é construível (interface, scaffolding, linhas disabled, compliance, ledger).
- **Cripto no cliente (wagmi/viem/WalletConnect por padrão).** Rejeitada: §2.5 manda
  cripto **fora do cliente**; a UI consome só status via BFF. Assinatura on-chain pelo
  anunciante **só se** a spec exigir (E.9) — nunca por padrão.

## Consequências

- **Positivas:**
  - **Desbloqueia o fan-out paralelo** da Fase 3 em **dois eixos disjuntos** (IA:
    `ml-optimization-engineer`; cripto/pagamentos: `payments-crypto-engineer` +
    `money-ledger-guardian` + `platform-infra-engineer`) com fronteiras de diretório e
    linguagem explícitas e zero colisão estrutural (`internal/chainconnector`,
    `services/payments`, `ml/deep`, `db/compliance`, `platform/cells/*`,
    `proto/adserver/payments/v1`, `bff/.../payments.ts` ratificados).
  - **Protege o p99 e a autoridade da cascata por construção:** o deep reusa o ponto de
    extensão e o fail-open da Fase 2; o budget não se amplia; cripto **nunca** toca o
    hot path. DA-3/TX-4 viram invariantes do layout, não promessas.
  - **A regra de ouro fica auditável:** Triton/GPU só sob uplift (K8); Fireblocks só sob
    AUM (C); TigerBeetle só sob gargalo (D) — cada um com gatilho mensurável. Nenhuma
    tecnologia pesada entra por aspiração.
  - **AEV/BND entram sem migração de schema** (verificado: colunas já existem); o CHECK
    `assets_enabled_needs_scale_chk` é barreira estrutural contra o bug financeiro
    clássico (habilitar ativo sem `scale`).
  - **Os bloqueios de produto saem do limbo:** cada pergunta de §3 tem default + gatilho
    ou premissa + o que reabre — orquestrável sem esperar a spec.
- **Negativas / custos aceitos:**
  - **O deep é construível mas não promovível agora:** o uplift A/B depende de tráfego
    real (pendência de infra da Fase 2). Aceito — K1 entrega código gated; K8 promove
    sob prova. Não se promove por aspiração.
  - **AEV/BND ficam `enabled=false` até a spec oficial de `scale`/classificação/supply:**
    o trilho é construível e testado contra premissas; produção real espera o produto.
  - **Duas células novas (PCI, AML/KYC) adicionam custo operacional e de auditoria:**
    aceito como exigência de conformidade (§2.7/§5), de escopo mínimo, validável por QSA.
  - **GPU é custo só sob deep em A/B:** produção sem deep promovido roda só CPU — sem
    custo de GPU por aspiração.
- **Impacto por fase do roadmap:**
  - **Fases 1/2:** inalteradas — o deep **pluga** por `model_version` atrás do
    `internal/ranker` existente; o cripto é control-plane fora do hot path; golden tests
    permanecem o contrato de não-regressão.
  - **Fase 3:** `ml/deep` + Triton/GPU (gated K8), fraude não-supervisionada (K2),
    `services/payments` + `internal/chainconnector` + trilhos fiat/cripto (K3–K7),
    células PCI/AML-KYC (F), AEV/BND como linhas do Asset Registry — tudo sob os gates
    de promoção e conformidade.
  - **Pós-Fase 3:** Fireblocks (AUM), TigerBeetle (gargalo), oráculo de preço
    (liquidez), chain não-EVM (spec) entram como ADRs sucessores sob seus gatilhos
    medidos — o layout e as interfaces já comportam sem reabrir a arquitetura.
