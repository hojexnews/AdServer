# ADR-0005 — Perfil BETA de dependência única (Postgres): tornar a plataforma testável ponta-a-ponta sem Docker/Redpanda/ClickHouse

> **Status:** Aceito · **Data:** 2026-07-25 · **Decisores:** Arquiteto-chefe / Tech Lead (+ `data-platform-engineer`, `decision-engine-engineer`, `frontend-bff-engineer`, `platform-infra-engineer` nas camadas afetadas)
> **Âncoras:** TX-2, TX-3, TX-5 · DA-6, DA-7, DA-11 · CA-1, CA-5, CA-6 · `docs/documentacao-tecnica.md` §4.7/§4.8 · `docs/stack-tecnologico.md` §2.2/§2.5 · ADR-0001 (ao vivo ≠ faturável) · ADR-0002 §C (build hermético) · `docs/plano-desenvolvimento-por-addon.md` §5 (G0 fechado, G1 gated)
> **Supersede:** — · **Substituído por:** —

## Contexto

O plano (§5) registra que **G0 está código-completo (7/7)** e que o próximo movimento
real é **G1 — cutover de infra, gated por aprovação humana**. O que esse registro não
dizia é o que se descobre ao tentar *usar* o produto: **a plataforma não era testável
em nível beta**, e a razão não é infra de nuvem — é código.

Medição de 1ª mão nesta máquina (2026-07-25), sem Docker, sem Redis, sem ClickHouse,
sem Redpanda, com Postgres 16.14 nativo:

- `make go-build` verde; `dev-db-setup` provisiona `config`+`ledger`+`asset_registry`+`vector` e os seeds, incluindo as 4 zonas reais do Hojex News (1001–1004).
- O **decision service sobe e serve anúncio real**: `/v1/decide` devolve `SERVED_TIER_CONTRACT` para BR e `SERVED_TIER_REMNANT` para US a partir do seed.
- O **collector serve a cadeia completa**: `/asyncjs` (ad tag), `/lg` (pixel 1×1, 200/image-gif) e `/ck` (302 com validação HMAC do token).
- **E nada disso deixa rastro.** Sem `REDPANDA_BROKERS`, o sink do collector é `noopSink` — impressão, clique e conversão são descartados em silêncio.
- O `/dashboard` responde `PRECONDITION_FAILED` (`UnconfiguredStatsAdapter`, 31ª onda), porque **não existe adapter de stats real no repo** — o backend é ClickHouse, item de infra.
- Sem `REDIS_ADDR`, o capper é `NoOpCapper`: **frequency cap configurado não é imposto**, silenciosamente (DA-6, CA-5).

Ou seja: três lacunas de **código** — não de nuvem — separavam "compila e serve" de
"um anunciante consegue rodar um beta". A regra de ouro do projeto (começar enxuto e
correto) aponta para a solução mínima: **a plataforma já depende de Postgres; nenhuma
das três lacunas exige uma segunda dependência.**

## Decisão

Adotamos um **perfil BETA de dependência única (Postgres)**: um conjunto de
implementações **atrás das interfaces que já existem**, ativadas por variável de
ambiente, que tornam a plataforma testável ponta-a-ponta com Postgres + Go + Node.

Nenhuma arquitetura nova. Cada peça é uma implementação de um contrato já definido:

| Peça | Contrato reusado | Chave de ativação |
|---|---|---|
| Sink de telemetria em Postgres (`stats.events_raw`, idempotente por `event_id`) | `EventSink` (collector) | `TELEMETRY_PG_DSN` |
| Capper em memória | `capping.RedisClient` — reusa 100% do `capping.Capper` (chave salgada, TTL, escopos, fail-safe) | `CAPPING_BACKEND=memory` |
| Stats "ao vivo" do Postgres no console | `StatsAdapter` (BFF) | `BFF_PG_DSN` |
| Um comando para subir e um para verificar | `make/beta.mk` | — |

**EMENDA (mesma sessão, antes do commit — o ADR ainda não era história arquivada).**
A barreira de guardiões mostrou que a tabela acima descrevia **4 peças** enquanto a
onda embarcou **8 mudanças**, e que as três não declaradas foram exatamente as três
que produziram achado de guardião. O que não cabe na declaração de escopo não recebe
revisão dirigida. As mudanças omitidas, agora declaradas:

| Peça omitida | Natureza | Por que importa |
|---|---|---|
| Geo derivado do IP em `POST /v1/decide` | **sempre-ligada**, não opt-in | Muda **qual anúncio é servido** quando há `.mmdb` (§4.6). Antes, `geo.NewMaxMindResolver` era construído e nunca consultado |
| Contagens ENTREGUES no `computeDeficit` | **sempre-ligada** onde o schema `stats` existir | Muda a ordenação intra-estrato Contract; destrava DA-4, que era inerte |
| `Promise.all` → `Promise.allSettled` no BFF | sempre-ligada | Impede que a recusa da fonte faturável derrube a série "ao vivo" |

**Correção de uma afirmação falsa deste ADR:** a versão original dizia "não muda nenhum
default de produção; todos os ramos são opt-in por env". Isso vale para o sink, o capper
e o adapter de stats — **não** para as três acima. Elas são incondicionais, e a primeira
só parecia inerte porque `GEOIP_DB_PATH` costuma estar ausente.

**Limitação de autenticidade que este ADR precisa declarar (invocar CA-1/TX-3 sem ela
induz o leitor ao erro):** `/lg` é **não autenticado** e lê `tid`/`cid` de query string
(só `/ck` tem HMAC). A policy `WITH CHECK` de `stats.events_raw` compara a linha contra
uma GUC preenchida **com o mesmo valor que o cliente forneceu** — ela garante
**autoconsistência, nunca autenticidade**. Protege contra bug de código, não contra
terceiro. Enquanto o sinal de impressão não for assinado, nenhum consumidor desses
números pode ter poder de **remover** entrega (ver §Gatilho).

**Consequência de processo (regra nova, derivada desta onda):** uma onda cruza a linha
*"muda qual anúncio é servido"* **somente** quando isso é o assunto declarado no título,
e nesse caso o `parity-golden-test-guardian` é gate de **entrada**, não de saída.

**Exposição de rede — aceite explícito de M-1/M-4 (`security-reviewer`):** o perfil beta
**não pode ser exposto à internet pública**. Escrever só a frase seria a mesma classe do
`//nolint` decorativo da 26ª onda, então ela vem com a propriedade que a sustenta:
`make/beta.mk` faz bind em **loopback** por default e exporta `TRUSTED_PROXY_DEPTH=0`
(sem proxy à frente, honrar `X-Forwarded-For` tornaria a segmentação por país
falsificável pelo próprio navegador). Quem quiser expor sobrescreve conscientemente.

**Fora do escopo desta decisão — o que o perfil BETA explicitamente NÃO é:**

1. **Não é fonte de faturamento.** DA-7 permanece intacto: faturável reconcilia contra
   o lakehouse **Iceberg**, nunca contra streaming nem contra este sink. O
   `queryConsolidated` do BFF **continua recusando** no perfil beta — o dashboard
   mostra apenas a série **"ao vivo"**, e a UI nunca soma as duas (CA-6).
2. **Não supersede o ADR-0001.** O princípio "ao vivo ≠ faturável" é preservado
   integralmente; muda apenas *quem serve o ao vivo* no perfil beta (Postgres em vez de
   ClickHouse). Em produção o caminho continua Redpanda → ClickHouse → Iceberg.
3. **Não muda nenhum default de produção.** Todos os ramos são **opt-in por env**: sem
   as chaves acima, o comportamento atual (Redpanda se configurado, senão noop;
   `NoOpCapper`; `UnconfiguredStatsAdapter`) fica bit-a-bit inalterado.
4. **Capping em memória é single-process.** Não é compartilhado entre réplicas e se
   perde no restart — válido para um beta de um nó, inválido para produção.
5. **Não toca a cascata (DA-3), o ledger, nem o schema de eventos (TX-1).** A autoridade
   de decisão, a contabilidade e o contrato Protobuf ficam exatamente como estão.
6. **Geo continua desligado sem `GEOIP_DB_PATH`** (`EmptyResolver`, DA-9): regra de
   entrega por país não dispara no beta a menos que o `.mmdb` seja provido. É limitação
   declarada, não bug.

Invariantes que o perfil beta **tem** de respeitar, e que são condição de merge:
`event_id` como PK com `ON CONFLICT DO NOTHING` (reentrega at-least-once, DA-7);
`FORCE RLS` com `USING` **e** `WITH CHECK` no schema novo (TX-3/CA-1); nenhum `float`
em qualquer superfície (TX-2); nenhum IP bruto, user-agent bruto ou PII persistido
(TX-5/DA-11); `billable` derivado da **função de produção** que já decide impressão em
branco, nunca reimplementado (CA-6); e I/O de banco **fora do caminho quente** de
`/lg` e `/ck` (TX-4).

## Gatilho de reabertura

- **G1 provisiona ClickHouse/Redpanda reais** → o perfil beta deixa de ser o caminho de
  teste e passa a ser apenas conveniência de desenvolvimento local. O `StatsAdapter`
  de produção volta a ser o ClickHouse.
- **Alguém quiser faturar o tráfego gerado no beta** → isso **não** é promoção do perfil;
  exige o caminho Iceberg do DA-7. Se essa demanda aparecer, abre-se ADR sucessor com o
  volume medido anexado, nunca uma exceção pontual.
- **Beta passar a rodar com mais de uma réplica do decision** → o capper em memória
  perde validade; requer Redis (ou ADR sucessor com outra estratégia).

## Alternativas consideradas

- **Exigir Docker Compose (caminho já documentado em `deploy/local/`)** — rejeitada por
  duas razões independentes: Docker não existe nesta máquina (medido), e mesmo onde
  existe, subir Redpanda + ClickHouse + 4 serviços para verificar uma impressão é
  exatamente a cerimônia que a regra de ouro manda evitar. O caminho Compose
  **permanece** disponível e intocado para quem quiser exercitar o pipeline real.
- **Manter o stub sintético (`InMemoryStatsAdapter`) ligado no beta** — rejeitada. A 31ª
  onda já provou o custo: números de `Math.random()` rotulados na UI como "Consolidado
  ≤1h / faturável". Ficção apresentada como base de cobrança é pior que ausência.
- **Embarcar ClickHouse (chDB/embedded) ou DuckDB** — rejeitada por over-engineering:
  segunda engine analítica, segundo dialeto e segundo caminho de migration para servir
  contagens que um `GROUP BY` em Postgres resolve no volume de um beta.
- **Deixar como está e chamar de "gated por infra"** — rejeitada por ser factualmente
  errado: as três lacunas são de código e não dependem de nuvem alguma.

## Consequências

- **Positivas:** a plataforma passa a ser testável ponta-a-ponta com uma dependência
  (Postgres) e dois comandos (`make beta-up`, `make beta-check`); o dashboard do
  anunciante mostra número real em vez de erro; o frequency cap passa a ser imposto de
  fato num beta de um nó; e o loop de desenvolvimento ganha um alvo verificável —
  "serve anúncio e o evento aparece" — que nenhum gate de lint substitui.
- **Negativas / custos aceitos:** mais uma implementação por interface para manter
  (três, todas pequenas e cobertas por teste); risco de alguém confundir o "ao vivo" do
  beta com faturável — mitigado por recusa explícita no `queryConsolidated`, por
  `COMMENT ON VIEW` e por esta seção; capping em memória com semântica mais fraca que
  Redis, declarada em log e em doc.
- **Impacto por fase do roadmap:** nenhum sobre Fases 0–3 (código inalterado nos
  caminhos de produção). Sobre §5: **não altera** o gating de G1 — o perfil beta não é
  cutover, não provisiona nada em nuvem e não dispensa aprovação humana para G1. O que
  ele muda é que **deixa de ser necessário esperar G1 para testar o produto**.
