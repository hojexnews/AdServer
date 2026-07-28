# Documentação Técnica — AdServer (modelo Revive Adserver)

> **Público-alvo:** setor de desenvolvimento.
> **Fonte normativa:** *Análise Arquitetural, Operacional e Estratégica do Revive Adserver — Funcionalidades, Telemetria e Implicações de Mercado* (documento existente, referido neste texto como **[DOC-BASE]**).
> **Versão de referência do produto:** Revive Adserver 6.0.x (linha V6, lançada em 22/10/2025; estável v6.0.6 em 18/03/2026).

---

## 1. Objetivo

Especificar, em nível de implementação, um servidor de veiculação de anúncios (*ad server*) que atua como **árbitro central entre a oferta de inventário dos editores (publishers) e a demanda das campanhas dos anunciantes**, executando, para cada requisição:

1. Decisão de qual criativo veicular, em tempo real (lógica de prioridade determinística);
2. Aplicação de segmentação granular (regras de entrega) e limitação de frequência (*frequency capping*);
3. Registro de telemetria volumétrica (requisição, impressão, clique, conversão);
4. Suporte a múltiplos modelos de precificação (CPM, CPC, CPA, *tenancy*).

Esta documentação orienta o time de desenvolvimento quanto às entidades de dados, ao motor de decisão, aos contratos de integração (ad tags, pixels, redirecionamentos) e aos critérios de aceitação verificáveis. As justificativas de cada decisão remetem ao **[DOC-BASE]**.

---

## 2. Escopo

### 2.1 Dentro do escopo

| Área | Itens |
|---|---|
| **Taxonomia de inventário** | Anunciantes, campanhas, banners (demanda); sites, zonas (oferta); vínculo N:N campanha↔zona |
| **Multi-tenancy** | Credenciais de login parametrizadas por anunciante; isolamento de visibilidade de estatísticas |
| **Motor de decisão** | Hierarquia determinística Override → Contract → Remnant → impressão em branco; *pacing* de contrato |
| **Pipeline de criativos** | Imagem estática (JPEG/PNG), pacote HTML5 (IAB), third-party ad tag (HTML/JS), vídeo (URL remota) |
| **Regras de entrega** | Tempo/dia-da-semana, URL, geo (país/cidade), useragent, variável customizada; lógica booleana AND/OR; Rule Sets reutilizáveis |
| **Frequency capping** | Por campanha (total), por sessão, por relógio; sobrescrita banner > campanha; *fail-safe* sem cookie |
| **Telemetria** | Request, Impression (pixel 1×1), Click (redirect via servidor), Conversion (pixel); agregação batch horária |
| **Precificação** | CPM, CPC, CPA, Tenancy; armazenamento monetário multi-moeda como decimal nu |
| **Geo** | Integração MaxMind GeoLite2 com chave de licença e auto-atualização (≥ v5.0.3) |
| **Privacidade** | *Privacy by Design*: sem centralização de PII; conformidade GDPR |
| **Notificação** | Plugin Mailer/SMTP para envio de relatórios e alertas (V6) |

### 2.2 Fora do escopo

- **RTB / Real-Time Bidding** com dashboards de atualização milissegundo a milissegundo. O design adotado é de **agregação batch horária** (ver §4.7 e [DOC-BASE], seção de Telemetria), por decisão arquitetural explícita.
- **Conversão cambial automática.** O sistema é agnóstico a moeda; a unificação contábil entre EUR/USD/BRL é responsabilidade humana/externa ([DOC-BASE], Modelagem Econométrica).
- **Plugins comerciais proprietários** (ex.: *VisualiX* para vídeo in-banner, $899) — fora da entrega base; integrável sob licença.
- **SLAs operacionais da Hosted Edition** (Aqua Platform: GeoDNS, backups 24/7, paridade de patch) — pertencem ao provedor gerenciado, não ao código da aplicação.

---

## 3. Decisões de arquitetura (com justificativa)

> Cada decisão (DA-n) cita a justificativa correspondente no **[DOC-BASE]**.

### DA-1 — Separação demanda/oferta em dois hemisférios lógicos
O modelo de dados isola **demanda** (anunciante → campanha → banner) de **oferta** (site → zona), interligados apenas pelo vínculo de veiculação.
**Justificativa:** [DOC-BASE], *Arquitetura de Inventário e Taxonomia Relacional* — "dois hemisférios logicamente isolados, mas operacionalmente interdependentes". Permite escalar anunciantes e sites de forma ilimitada e independente.

### DA-2 — Vínculo N:N campanha↔zona resolvido dinamicamente por requisição
A associação campanha↔zona é uma relação **muitos-para-muitos** materializada em tabela de vínculo; a seleção do banner ocorre a cada *request*, avaliando prioridades concorrentes.
**Justificativa:** [DOC-BASE] — "Esta relação de banco de dados de muitos-para-muitos é processada dinamicamente pelo servidor; para cada solicitação… seleciona automaticamente o banner mais otimizado".

### DA-3 — Hierarquia de prioridade determinística (cascata/waterfall)
O motor avalia, nesta ordem estrita:
1. **Override** (precedência absoluta);
2. **Contract** (déficit de *pacing* vs. cronograma);
3. **Remnant** (backfill);
4. **Impressão em branco** registrada (sem interromper a página).

**Justificativa:** [DOC-BASE], *Tipologia de Campanhas* — "ciclo de decisão de um milissegundo… verifica primeiro a elegibilidade do nível de Sobreposição… avalia os déficits… recai sobre a camada Remanescente… registra uma impressão em branco". A impressão em branco é métrica forense de déficit de inventário.

### DA-4 — Pacing de contrato orientado a meta
Campanhas Contract têm volume absoluto (impressões/cliques/conversões) e janela fixa. O algoritmo calcula o ritmo necessário para cumprir a meta antes do término, priorizando entrega sobre inventário de menor valor.
**Justificativa:** [DOC-BASE], tabela de campanhas — "O algoritmo de alocação calcula ativamente o ritmo necessário para atingir o objetivo antes da data de término".

### DA-5 — Ad tags assíncronas e *stateless* no cliente
A renderização no navegador é feita por **códigos de invocação de zona** (ad tags) em JavaScript otimizado, que se comunicam de forma assíncrona com o motor de decisão.
**Justificativa:** [DOC-BASE], *Topologia de Sites, Zonas e Códigos de Invocação* — "conduítes de comunicação assíncrona em tempo real entre o cliente web e o motor de decisão".

### DA-6 — Capping baseado em cookie com comportamento *fail-safe*
A contagem de frequência persiste em cookie no cliente. Se o navegador recusar ou não suportar cookies (navegação anônima/sandbox), a **entrega de campanhas com limite é abortada** (silêncio preferido a estouro de contabilidade).
**Justificativa:** [DOC-BASE], *Mecanismos de Capping* — "reage abortando totalmente a entrega da campanha baseada em limites… O silêncio forçado é priorizado".

### DA-7 — Agregação estatística em batch horário (não-RTB)
Estatísticas são sumarizadas em processos batch e consolidadas nos painéis **uma vez por hora**. Decisão consciente que viabiliza operação em hardware acessível.
**Justificativa:** [DOC-BASE], *Telemetria* — "agregando massivamente os fluxos… a uma taxa metódica de exata uma vez por hora… propicia a viabilidade… em pacotes de hardware altamente acessíveis".

### DA-8 — Medição por pixel e clique por redirecionamento
- **Impressão:** contabilizada quando o payload do banner é carregado, via **pixel de medição 1×1** translúcido.
- **Clique:** o link passa **primeiro pelo servidor** (contabiliza a métrica) e então emite **redirect HTTP** para a landing page.
- **Conversão:** pixel de conversão instalado na página terminal do funil do anunciante.

**Justificativa:** [DOC-BASE], *Telemetria de Dados* — definições de Impression (pixel 1×1), Click (redirect loop pelo servidor) e Conversion (pixel terminal).

### DA-9 — Geotargeting via MaxMind GeoLite2 com auto-atualização
Resolução de país/cidade pelo IP do cliente usando GeoLite2; a aplicação aceita **chave de licença MaxMind** e baixa/valida/atualiza os arquivos de dados automaticamente (≥ v5.0.3, jan/2020).
**Justificativa:** [DOC-BASE], *Geotargeting e Roteamento de IP*.

### DA-10 — Armazenamento monetário agnóstico a moeda
Valores são tratados como **decimais nus** (sem API de câmbio, sem restrição de nomenclatura de moeda). Unificação contábil é externa.
**Justificativa:** [DOC-BASE], *Modelagem Econométrica* — "comportam-se como constructos em numerais decimais absolutos nus… cabendo sempre aos recursos paralelos humanos… o mapeamento contábil".
**Implicação de implementação:** usar tipo de ponto fixo (`DECIMAL/NUMERIC`); **nunca `float`** para dinheiro.

### DA-11 — Privacy by Design / GDPR
A aplicação **não centraliza PII** nem realiza transmissão inter-regional opaca de dados pessoais. First-party data (ex.: idade/gênero) é injetada pelo publisher via variável customizada na requisição, não armazenada como perfil central.
**Justificativa:** [DOC-BASE], *Legislação de Privacidade* — "repele e previne… a estocagem intencional massificada… de PIIs".

### DA-12 — Pilha tecnológica e arquitetura de plugins
Stack alvo **LAMP/LEMP**: Linux + Apache/Nginx + **MySQL/MariaDB** + **PHP**. Funcionalidades extensíveis via **plugins** (diretório `/etc/plugins`); o Mailer (SMTP) é distribuído por padrão na V6.
**Justificativa:** [DOC-BASE], *Paradigmas da Soberania do Sistema (Download Edition)* e *Evolução Estável (V6)*.
**Nota operacional:** falha na instalação de plugins nativos pode esvaziar o menu de Rule Sets; a recuperação documentada é a descompressão manual dos plugins em `/etc/plugins`.

---

## 4. Interfaces / contratos

### 4.1 Modelo de dados (entidades e relações)

```
Advertiser (1) ──< Campaign (1) ──< Banner
                      │
                      └──< CampaignZone >── Zone >── Site   (vínculo N:N via CampaignZone)

Campaign / Banner ──< DeliveryRule        (segmentação)
DeliveryRuleSet   ──< DeliveryRule        (conjunto reutilizável, global)
Campaign / Banner ──< Cap                 (frequency capping)
(Request|Impression|Click|Conversion) ──> StatsHourly (agregação batch)
```

**Atributos mínimos por entidade:**

| Entidade | Campos-chave |
|---|---|
| `Advertiser` | `id`, `name`, `login_credentials` (multi-tenant), `is_network` (bool: cliente direto vs. ad network) |
| `Campaign` | `id`, `advertiser_id`, `type` (`override`\|`contract`\|`remnant`), `priority`, `goal_target`, `goal_metric` (`impressions`\|`clicks`\|`conversions`), `start_at`, `end_at`, `pricing_model`, `rate`, `currency` |
| `Banner` | `id`, `campaign_id`, `creative_type` (`image`\|`html5`\|`thirdparty_tag`\|`video`), `asset_url`/`asset_blob`, `dest_url`, `width`, `height` |
| `Site` | `id`, `name`, `url` |
| `Zone` | `id`, `site_id`, `name`, `width`, `height`, `type` |
| `CampaignZone` | `campaign_id`, `zone_id` (PK composta) |
| `DeliveryRule` | `id`, `owner_type` (`campaign`\|`banner`), `owner_id`, `vector`, `operator`, `value`, `logical_op` (`AND`\|`OR`), `rule_set_id?` |
| `DeliveryRuleSet` | `id`, `name` (global, em Preferences) |
| `Cap` | `id`, `owner_type`, `owner_id`, `scope` (`campaign_total`\|`session`\|`clock`), `limit`, `reset_interval` |
| `StatsHourly` | `hour_bucket`, `campaign_id`, `banner_id`, `zone_id`, `requests`, `impressions`, `clicks`, `conversions`, `conversion_value`, `currency` |

### 4.2 Tipos de campanha (contrato de prioridade)

| `type` | Mecânica | Precedência |
|---|---|---|
| `override` | Força precedência absoluta, ignora pacing concorrente | 1 (máxima) |
| `contract` | Volume absoluto + janela fixa, com pacing | 2 |
| `remnant` | Backfill, preenche o restante | 3 |
| *(nenhum elegível)* | Registra impressão em branco | — |

### 4.3 Tipos de criativo (pipeline de ingestão)

| `creative_type` | Entrada obrigatória | Observações |
|---|---|---|
| `image` | arquivo raster (JPEG/PNG) + `dest_url` | iteração mais leve |
| `html5` | pacote HTML5 (IAB) | substitui Flash; mídia rica/responsiva |
| `thirdparty_tag` | snippet HTML/JS do anunciante | servidor atua como árbitro mestre; dupla contagem de impressão |
| `video` | URL remota do arquivo + `dest_url` | in-banner avançado requer plugin VisualiX (fora do escopo base) |

### 4.4 Ad tag (código de invocação de zona)

Contrato de saída gerado pelo sistema, embutido no CMS do publisher. Dispara requisição assíncrona à zona:

```
GET /www/delivery/asyncjs.php?zoneid={ZONE_ID}&cb={CACHEBUSTER}[&{custom_vars}]
```

- `zoneid` — identifica a zona de oferta.
- `cb` — cachebuster (evita cache do navegador).
- **Variáveis customizadas (first-party data):** o desenvolvedor do publisher injeta pares chave/valor antes do disparo, p.ex. `document.write("&gender=male");`, anexados ao request e casados pela regra `Site - Variable`. Ver DA-11.

### 4.5 Resposta de decisão (contrato do request)

- **Entrada:** `zoneid`, cookies de capping, IP (geo), `User-Agent`, URL de origem, custom vars.
- **Processamento:** hierarquia DA-3 + filtros de regras (§4.6) + capping (§4.8).
- **Saída:** payload do criativo selecionado (markup do banner com pixel de impressão e link de clique embutidos) **ou** vazio (→ impressão em branco contabilizada).

### 4.6 Regras de entrega (vetores e operadores)

| `vector` | Operadores típicos | Exemplo de `value` | Uso |
|---|---|---|---|
| `Time - Day of Week` | is / is not | `Mon..Fri` | B2B exclui fim de semana |
| `Site - URL` (contextual) | contains | `/business/` | confina à seção do site |
| `Geo - Country` / `Geo - City` | is / is not | `Canada` | confina por jurisdição |
| `Client - Useragent` | contains | `chrome` | confina por navegador/dispositivo |
| `Site - Variable` (custom) | is / contains | `gender=male` | first-party data do publisher |

**Lógica booleana:**
- `AND` — todas as proposições verdadeiras simultaneamente.
- `OR` — qualquer proposição satisfaz.
- ⚠️ Contradições em `AND` (ex.: dia = segunda **AND** dia = terça) tornam a condição impossível e **silenciam o banner permanentemente** — validar na UI.

**Delivery Rule Sets:** conjuntos nomeados, globais (em Preferences), reutilizáveis em N banners; encapsulam combinações `(A OR B OR C) AND (S1 OR S2…)` em objeto único de implantação. Reduzem erro de digitação e fadiga operacional.

### 4.7 Telemetria — contratos de medição

| Métrica | Mecanismo | Endpoint/artefato |
|---|---|---|
| **Request** | disparo do JS de invocação | `asyncjs.php?zoneid=…` |
| **Impression** | pixel 1×1 translúcido carregado com o banner | `GET /www/delivery/lg.php?…` (log) |
| **Click** | redirect via servidor → `dest_url` | `GET /www/delivery/ck.php?…` → `302` |
| **Conversion** | pixel de conversão na página terminal do anunciante | `GET /www/delivery/ct.php?…` |

**Agregação:** eventos são consolidados em `StatsHourly` por processo batch **horário** (DA-7). Painéis refletem dados com defasagem ≤ 1 hora.

### 4.8 Frequency capping (contrato de limitação)

| `scope` | Semântica | Reset |
|---|---|---|
| `campaign_total` | teto de exibições por usuário durante toda a campanha | fim da campanha |
| `session` | teto por sessão de navegação | fechamento do navegador/dispositivo |
| `clock` | teto por janela cronométrica (`reset_interval` em h/m/s) | tempo fixo, independe do usuário |

- **Sobrescrita:** cap no **banner** anula cap genérico na **campanha** quando divergentes (DA-6).
- **Fail-safe:** sem cookie disponível → abortar entrega capeada (DA-6).

### 4.9 Modelos de precificação (contrato de faturamento)

| `pricing_model` | Evento faturável | Observação |
|---|---|---|
| `CPM` | a cada 1.000 impressões renderizadas | brand awareness |
| `CPC` | por clique validado | isenta impressões |
| `CPA` | por conversão (pixel terminal) | requer DA-8 (conversão) |
| `Tenancy` | tarifa fixa por período (mês civil) | independe de volume |

Campo `currency` é rótulo; **sem conversão automática** (DA-10).

### 4.10 Configurações de integração

- **MaxMind:** `maxmind_license_key` (ir.config); job de auto-atualização dos arquivos GeoLite2 (DA-9).
- **Multi-tenancy:** geração de credenciais por anunciante; ACL restringe visibilidade de estatísticas ao próprio anunciante.
- **Mailer (V6):** configuração SMTP (host, porta, credenciais/token) para envio de relatórios batch — evita rotinas de mail PHP nativas e blacklist antispam.

---

## 5. Critérios de aceitação

> Formato verificável (checklist + Given/When/Then). Cada CA referencia a decisão/seção correspondente.

### 5.0 Adjudicação de canonicidade e legenda (tech-lead, 2026-07-19)

**Este §5 é canônico** — é a única fonte normativa do que os `CA-n` exigem **e** do status
de cada um. O `README.md` descreve o estado de implementação por incremento; ele **não** é
fonte de verdade sobre `CA-n`. Onde os dois divergirem, prevalece este §5.

**Regra de marcação (inviolável):** um item só é marcado como provado se puder ser amarrado
a um **gate executável hoje**, citado nominalmente no próprio item. Golden verde em suíte
vizinha, "está implementado" ou inspeção visual **não** contam. Itens sem gate permanecem
desmarcados — subrepresentar é aceitável, superrepresentar não é.

| Marca | Significado |
|---|---|
| `[x]` | Provado por gate executável, citado no item. |
| `[~]` | **Parcialmente** provado — o gate cobre parte do critério; a parte descoberta está declarada no item. |
| `[ ]` | **Não** provado por gate executável (pode estar implementado — mas não há gate que o assine). |
| `N/A-legado` | Critério herdado do **Revive legado** (PHP/MySQL) que a reescrita Go **não satisfará por construção**. Não é dívida; é escopo revogado. |

**Sobre `N/A-legado`:** este documento foi derivado do Revive 6.x, e alguns critérios
descrevem a *plataforma de execução do Revive*, não o comportamento do produto. O alvo desta
reescrita é Go + Postgres + Redis + Redpanda + ClickHouse (ver `docs/stack-tecnologico.md`).
Um critério que exija PHP/MySQL ou layout de plugins do Revive é **inaplicável por decisão
arquitetural**, não uma pendência a cumprir. Ele fica registrado, marcado e justificado —
nunca silenciosamente apagado.

Gates citados abaixo (todos executados de 1a mão em 2026-07-19, verdes salvo nota):
`make parity-golden` (`tests/parity/**`, goldens em `tests/parity/golden/*.json`),
`make verify` (buf TX-1 + 6 guards no-float TX-2), `make go-test`, `make data-billing-test`,
`make data-validate`, `make platform-validate`, `make bff-ci`, `make web-ci`, e os testes SQL
`db/*/tests/*.sql` aplicados contra Postgres 16 nativo.

### CA-1 — Taxonomia e multi-tenancy (DA-1, DA-2, §4.10)
- [ ] É possível cadastrar número ilimitado de anunciantes e sites sem teto artificial. — *sem gate: é uma afirmação de ausência de limite, não asserida por nenhum teste. Não há teto no schema, mas isso não está provado.*
- [x] **Dado** um anunciante com credencial própria, **quando** autentica no painel, **então** vê apenas as estatísticas das suas campanhas (isolamento verificado). — **Gate:** `db/config/tests/rls_isolation_test.sql` (RLS por tenant, incl. BLOCO 5.5 de introspecção `pg_policy.polwithcheck` default-deny) + `bff/src/lib/trpc.test.ts` (ACL `tenantProcedure` server-side, via `make bff-ci`). ACL é server-side, nunca no cliente.
- [x] Um vínculo campanha↔zona N:N é persistido e avaliado por requisição. — **Gate:** `db/config/migrations/0003+0004` (`config.campaign_zones` + policy com `WITH CHECK` explícito) e `internal/configload/` (loader→snapshot) via `make go-test`.

### CA-2 — Motor de decisão / cascata (DA-3, DA-4, §4.2)
> **Gate desta seção:** `make parity-golden` → `tests/parity/ca2_cascade_golden_test.go` sobre `tests/parity/golden/ca2_cascade.json`.

- [x] **Dado** Override elegível, **quando** chega o request, **então** Override é servido, ignorando Contract/Remnant. — **Gate:** caso golden `CA2-001` + `TestCA2_Override_Priority_TieBreak`.
- [x] **Dado** nenhum Override e Contract com déficit de pacing, **então** Contract é priorizado sobre Remnant. — **Gate:** casos golden `CA2-002`/`CA2-003` (`ct-low`/`ct-high`).
- [x] **Dado** Contract adiantado ou sem segmentação correspondente, **então** Remnant preenche. — **Gate:** casos golden `CA2-002`/`CA2-003` (`rm1`).
- [x] **Dado** nenhum criativo elegível em qualquer estrato, **então** a página **não quebra** e uma **impressão em branco** é registrada. — **Gate:** `TestCA2_AllCampaignsCapped_FallsToBlank` + `TestCA4_RuleBlocksBanner_FallsToBlank` + caso `CA6-005` (`blank=true`, `billable=false`). Este é o piso da autoridade da cascata (DA-3).

### CA-3 — Criativos (§4.3)
> **Gate desta seção:** `make parity-golden` → `tests/parity/ca3_creatives_golden_test.go` sobre `tests/parity/golden/ca3_creatives.json` (7 casos). **Este é o CA menos coberto** — o golden cobre o *mapeamento* de criativo→banner (plumbing) e a ligação server-side de `dest_url`, mas **não** cobre renderização, upload nem VAST. `make parity-status` já reporta CA-3 como PARCIAL; este §5 concorda.

- [ ] Upload de imagem exige `dest_url`; rejeita criativo sem destino. — **NÃO satisfeito, e o golden documenta isso explicitamente:** o caso `CA3-002` (`image-without-dest-still-selected-no-rejection`) registra que um criativo de imagem **sem `dest_url` NÃO é rejeitado** na camada Go — o loader o seleciona normalmente. A validação de upload é responsabilidade do console/BFF e ainda não existe como gate. **Não marcar sem um teste que reprove o criativo sem destino.**
- [ ] Pacote HTML5 renderiza responsivamente em desktop e mobile. — *sem gate: o golden `CA3-003`/`CA3-006` cobre apenas o plumbing (blob inline → `Banner.HTML`, com fallback para `AssetURL`). Renderização responsiva exige navegador real; nenhum teste a exerce.*
- [~] Third-party tag é servido e dispara contagem de impressão própria (dupla verificação). — **Gate parcial:** `CA3-004` prova que a tag de terceiro é servida pelo mesmo caminho de HTML. A **dupla verificação** (contagem própria do terceiro conferida contra a nossa) **não** tem gate.
- [x] Vídeo aceita URL remota + `dest_url`. — **Gate:** caso golden `CA3-005` (`video-remote-url-plus-dest`). Nota: `TestCA6_VAST_NoVPAID` cobre a ausência de VPAID no wrapper VAST, não o playback.

### CA-4 — Regras de entrega (DA-9, §4.6)
> **Gate desta seção:** `make parity-golden` → `tests/parity/ca4_rules_golden_test.go` sobre `tests/parity/golden/ca4_rules.json` (23 casos). Os valores concretos do golden diferem dos exemplos deste §5 (ex.: `Country IS 'BR'` em vez de `Canada`, `DayOfWeek IS '3'` em vez de `Mon..Fri`) — o que está provado é o **operador/mecanismo**, que é o que o critério exige; os literais são ilustrativos.

- [x] Regra `Time - Day of Week = Mon..Fri` suprime entrega no fim de semana. — **Gate:** casos `CA4-010`/`CA4-011` (`Time-DayOfWeek` IS / IS-NOT, match e no-match).
- [x] Regra `Site - URL contains "/business/"` veicula só na seção alvo. — **Gate:** casos `CA4-006`/`CA4-007` (`Site-URL CONTAINS`, incl. o literal `/business/` no caso de no-match).
- [x] Regra `Geo - Country = Canada` não vaza inventário para IPs fora do país. — **Gate:** casos `CA4-001`…`CA4-004` (`Geo-Country` IS / IS-NOT, match e no-match) + `CA4-005` (`Geo-City CONTAINS`). Complementar: `TestCA6_GeoResolver_IPNotLeaked` prova que o IP bruto não vaza para o evento (TX-5/DA-11).
- [x] Regra `Client - Useragent contains "chrome"` restringe a navegadores Chromium. — **Gate:** casos `CA4-008`/`CA4-009` (`Client-UserAgent CONTAINS`, com o literal `chrome` no caso de no-match).
- [~] Custom var `&gender=male` injetada via `document.write` casa a regra `Site - Variable`. — **Gate parcial:** casos `CA4-012`/`CA4-013` provam o casamento de `Site-Variable` no motor. A **injeção via `document.write`** no ad tag (caminho JS do browser) **não** tem gate.
- [x] **Dado** `AND` com condições mutuamente exclusivas, **então** a UI **alerta** (anti-contradição) antes de salvar. — **Gate:** motor — casos `CA4-019`…`CA4-021` (dois `IS` no mesmo vetor; `IS 'BR'` + `IS-NOT 'BR'`; 7 dias excluídos) + `CA4-023` e `TestCA4_AntiContradiction_SilencesBanner`; `CA4-022` prova que `OR` **não** é marcado como impossível (não-tautologia). UI/BFF — `web/console/src/lib/contradiction.test.ts` (`make web-ci`) e `bff/src/lib/contradiction.test.ts` (`make bff-ci`).
- [x] Um Rule Set criado em Preferences é reaplicável em ≥ 2 banners sem redigitar condições. — **Gate:** `TestCA4_RuleSet_Reusable_AcrossBanners`. Casos `CA4-016`…`CA4-018` cobrem ruleset vazio (open), ausência de ruleset e ID desconhecido (**fail-closed**).

### CA-5 — Frequency capping (DA-6, §4.8)
> **Gate desta seção:** `make parity-golden` → `tests/parity/ca5_capping_golden_test.go` sobre `tests/parity/golden/ca5_capping.json` (11 casos). É o CA mais bem coberto.

- [x] Cap `campaign_total` limita exibições por usuário ao teto durante a campanha. — **Gate:** caso `CA5-001` (cap=2: permite 2, bloqueia a 3ª) + `CA5-008` (usuários distintos não compartilham estado).
- [~] Cap `session` zera ao fechar o navegador. — **Gate parcial:** o caso `CA5-002` prova a semântica do cap de escopo *sessão* (cap=1: permite 1, bloqueia a 2ª na mesma sessão). A **expiração ao fechar o navegador** depende do ciclo de vida do cookie de sessão no cliente e **não** tem gate server-side.
- [x] Cap `clock` reseta no `reset_interval` configurado, independente do usuário. — **Gate:** caso `CA5-003` (cap=2/hora: permite 2, bloqueia a 3ª na janela) + `CA5-009` (rotação de salt abre nova janela).
- [x] Cap no banner sobrescreve cap divergente na campanha. — **Gate:** caso `CA5-004` (`banner-overrides-campaign`).
- [x] **Dado** navegador sem cookies, **quando** há cap ativo, **então** a entrega capeada é **abortada** (fail-safe), não estourada. — **Gate:** caso `CA5-005` (sem `user_id` + campanha capeada → aborta) com o par de não-tautologia `CA5-005b` (sem `user_id` + campanha **sem** cap → serve; o fail-safe não é um "nega tudo"). `CA5-006`/`CA5-007` estendem o mesmo fail-safe ao Redis indisponível (DA-6), e `CA5-010` prova `panic` fail-closed com salt vazio.

### CA-6 — Telemetria (DA-7, DA-8, §4.7)
> **Gate desta seção:** `make parity-golden` → `tests/parity/ca6_telemetry_golden_test.go` sobre `tests/parity/golden/ca6_telemetry.json` (10 casos), mais `make data-validate` (`scripts/ci/data-schema-invariants.py`) para o contrato de agregação.

- [x] Impressão só é contabilizada após carga do pixel 1×1 (não no disparo do request). — **Gate:** casos `CA6-001` (contabiliza em `/lg`, não em `/asyncjs`) e `CA6-002` (`/asyncjs` apenas enfileira `AdRequest`) + `TestCA6_ImpressionOnlyAtPixelLoad`.
- [x] Clique passa pelo servidor (contabiliza) e então emite `302` para `dest_url`. — **Gate:** caso `CA6-003` (token HMAC válido → `302` + evento), com os pares de não-tautologia `CA6-006` (token inválido → `400`, sem evento) e `CA6-007` (sem `CK_HMAC_SECRET` → `503` fail-closed) + `TestCA6_ClickToken_ValidThenExpired` / `TestCA6_ClickToken_TamperedRejected`.
- [x] Conversão é atribuída quando o pixel terminal dispara. — **Gate:** caso `CA6-004` (`/ct` → `200` + evento). Idempotência sob reentrega: caso `CA6-008` + `TestCA6_Dedupe_IdempotentByEventID`.
- [~] Painéis consolidam estatísticas em batch **horário** (defasagem ≤ 1h); sem atualização milissegundo a milissegundo. — **Gate parcial (lado dados):** `scripts/ci/data-schema-invariants.py` (via `make data-validate`) exige o statement real `CREATE VIEW adserver.stats_hourly` com as colunas do contrato StatsHourly, `stats_hourly_state` com `ENGINE = AggregatingMergeTree` (DA-7), e `COMMENT ON TABLE` marcando **cada** view "ao vivo" como `NAO-FATURAVEL` (ADR-0001) — escopado por statement, não por substring no corpus. **Sem gate (lado UI):** a exigência de que o console **rotule** "≤1h" vs "ao vivo" e **nunca some** as duas fontes não é asserida por nenhum teste de front hoje.
- [x] Diferença Request − Impression é exposta como indicador de perda/escassez de inventário. — **Gate:** `TestCA6_RequestMinusImpression_Metric`.

### CA-7 — Precificação e moeda (DA-10, §4.9)
- [~] CPM fatura a cada 1.000 impressões; CPC por clique; CPA por conversão; Tenancy por período fixo. — **Gate parcial:** `make data-billing-test` (`data/iceberg/jobs/test_billing_batch_hourly.py`) cobre **apenas CPM** — 3 testes: semântica de FLOOR, ausência de cobrança de milhar parcial, e USDC `scale=6`. **CPC, CPA e Tenancy não têm teste executável** no motor canônico de faturamento. Não marcar como completo até existirem.
- [x] Valores monetários usam tipo decimal de ponto fixo (`NUMERIC`); **nenhum** uso de `float`. — **Gate:** `make verify` → 6 guards `scripts/ci/no-float-{proto,go,ts,py,sql,data-sql}.sh` com sentinela anti-skip `NO_FLOAT_SCRIPTS_EXPECTED := 6`, mais `make go-lint` (`.golangci.yml`/`forbidigo`) e o ESLint de dinheiro do BFF/console. Escopo **default-deny com allowlist explícita** — ver `contracts/lint/no-float.md` §Escopo. Complemento no banco: `db/ledger/tests/postings_immutability_test.sql` (append-only) e a dupla-entrada `sum(debit)=sum(credit)` em `internal/ledger` (`TestCheckBalance_PerAssetImbalanceDetected`, via `CheckBalanceForTest` — a função de produção, não uma reimplementação).
- [x] O sistema aceita múltiplas moedas como rótulo, **sem** conversão cambial automática. — **Gate:** `internal/ledger` — `TestFXExchange_TwoPairsIsolated` e `TestFXExchange_CrossAssetAmountsNeedNotMatch` (câmbio só como **par de postings explícito**, DA-10), `TestCompare_CrossCurrencyError` e `TestCompare_DifferentScale` (comparação entre moedas/escalas distintas é **erro**, nunca conversão implícita), `TestScaleMismatch_Rejected` e `TestAssetNotFound_Rejected`.

### CA-8 — Privacidade e conformidade (DA-11)
- [x] Nenhum PII é persistido em perfil central; first-party data trafega só na requisição. — **Gate:** `TestCA6_GeoResolver_IPNotLeaked` (IP bruto nunca chega ao evento), casos `CA6-009` (UA reduzido a classe grossa antes de emitir) e `CA6-010` (referer sanitizado — query string e fragment removidos), mais `make platform-validate` → `platform-otel-validate`, que exige `transform/redact-pii` + `redaction/allowlist-<tipo>` em **todos** os pipelines de `service.pipelines` e `allow_all_keys: false` em default-deny **case-insensitive** (TX-5). Capping usa identificador efêmero com salt rotativo (`CA5-009`), não perfil persistente.
- [ ] Inspeção do fluxo confirma ausência de transmissão inter-regional opaca de dados pessoais. — *sem gate: exige inspeção de tráfego em infra viva (multi-região) que não existe neste ambiente. A separação de instância da célula AML/KYC e as Cilium egress policies são a mitigação de projeto, mas nenhuma delas é asserida comportamentalmente offline (ver `docs/ops/go-live-runbook.md` §9 L-1).*

### CA-9 — Plataforma e operação (DA-12, §4.10)

> **Adjudicação (tech-lead, 2026-07-19):** este CA é o mais contaminado pela herança do
> Revive. Dois dos quatro itens descrevem a *plataforma de execução do Revive legado*
> (PHP/MySQL, layout de plugins) e são **inaplicáveis por construção** ao alvo desta
> reescrita — não são dívida técnica nem pendência de roadmap; são escopo **revogado** por
> decisão arquitetural. Ficam registrados e marcados `N/A-legado`, com o critério sucessor
> apontado quando existe. Os outros dois itens continuam válidos e são avaliados normalmente.

- **N/A-legado** — ~~Aplicação instala em stack LAMP/LEMP (PHP + MySQL/MariaDB).~~ **Inaplicável por construção.** O alvo é Go (hot path) + Postgres + Redis + Redpanda + ClickHouse, empacotado em contêiner e implantado em Kubernetes (ver `docs/stack-tecnologico.md` e `docs/adr/0002-fase-1-sequenciamento-e-layout.md`). Não existe PHP nem MySQL/MariaDB no repositório, e não passará a existir. **Critério sucessor:** `make platform-validate` (6 checks: tofu, kubeconform, kyverno test, otel-validate, openbao-policy-check, cell-consistency) + o procedimento de `docs/ops/go-live-runbook.md` §2 (ordem de migrações por schema).
- **N/A-legado** — ~~Plugins residem em `/etc/plugins`; ausência deles não deve esvaziar silenciosamente o menu de Rule Sets sem diagnóstico.~~ **Inaplicável por construção.** A reescrita não tem arquitetura de plugins carregados de `/etc/plugins`; as regras de entrega são código Go de primeira classe (`internal/cascade`, `internal/rules`), não plugins opcionais. **A preocupação de fundo — "ausência silenciosa não deve esvaziar o menu sem diagnóstico" — sobrevive e está satisfeita**, mas sob CA-4: o caso golden `CA4-018` prova que um `rule_set_id` desconhecido é **fail-closed** (inelegível), e não silenciosamente ignorado.
- [~] MaxMind: chave de licença aceita e arquivos GeoLite2 auto-atualizam sem intervenção manual. — **Gate parcial:** `internal/geo/maxmind_reload_test.go` (via `make go-test`) prova o **hot-reload** do `.mmdb` sem restart do processo (fechado na 18ª onda). O **job de auto-atualização** que baixa periodicamente o GeoLite2 da MaxMind (e a aceitação da chave de licença real) **não** tem gate — depende de credencial e rede externas. Ver runbook §3.
- [ ] Mailer/SMTP entrega relatórios batch sem cair em blacklist (sem uso do mail PHP nativo). — **NÃO implementado.** `grep -rniE 'smtp|mailer'` sobre `internal/`, `services/`, `bff/src` e `web/console/src` retorna **zero** ocorrências: não existe camada de e-mail no produto hoje. A cláusula "sem uso do mail PHP nativo" é satisfeita trivialmente (não há PHP), mas o critério **positivo** — entregar relatórios batch — está por fazer. Não marcar.

---

### Anexo A — Glossário rápido

| Termo | Definição |
|---|---|
| **Ad tag / código de invocação** | Snippet JS embutido no site que chama a zona |
| **Zona** | Espaço de exibição dentro de um site |
| **Pacing** | Ritmo de entrega calculado para cumprir meta de Contract |
| **Waterfall** | Cascata de prioridade Override → Contract → Remnant |
| **Impressão em branco** | Evento registrado quando nenhum criativo é elegível |
| **Fill rate** | Taxa de preenchimento de inventário (alvo ~100% com Remnant) |
| **Capping** | Limite de frequência de exibição por usuário |
| **Cachebuster** | Parâmetro aleatório que impede cache do request |

---

*Documento derivado do **[DOC-BASE]**. Atualizar em conjunto com mudanças de versão da linha V6.*
