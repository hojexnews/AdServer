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

### CA-1 — Taxonomia e multi-tenancy (DA-1, DA-2, §4.10)
- [ ] É possível cadastrar número ilimitado de anunciantes e sites sem teto artificial.
- [ ] **Dado** um anunciante com credencial própria, **quando** autentica no painel, **então** vê apenas as estatísticas das suas campanhas (isolamento verificado).
- [ ] Um vínculo campanha↔zona N:N é persistido e avaliado por requisição.

### CA-2 — Motor de decisão / cascata (DA-3, DA-4, §4.2)
- [ ] **Dado** Override elegível, **quando** chega o request, **então** Override é servido, ignorando Contract/Remnant.
- [ ] **Dado** nenhum Override e Contract com déficit de pacing, **então** Contract é priorizado sobre Remnant.
- [ ] **Dado** Contract adiantado ou sem segmentação correspondente, **então** Remnant preenche.
- [ ] **Dado** nenhum criativo elegível em qualquer estrato, **então** a página **não quebra** e uma **impressão em branco** é registrada.

### CA-3 — Criativos (§4.3)
- [ ] Upload de imagem exige `dest_url`; rejeita criativo sem destino.
- [ ] Pacote HTML5 renderiza responsivamente em desktop e mobile.
- [ ] Third-party tag é servido e dispara contagem de impressão própria (dupla verificação).
- [ ] Vídeo aceita URL remota + `dest_url`.

### CA-4 — Regras de entrega (DA-9, §4.6)
- [ ] Regra `Time - Day of Week = Mon..Fri` suprime entrega no fim de semana.
- [ ] Regra `Site - URL contains "/business/"` veicula só na seção alvo.
- [ ] Regra `Geo - Country = Canada` não vaza inventário para IPs fora do país.
- [ ] Regra `Client - Useragent contains "chrome"` restringe a navegadores Chromium.
- [ ] Custom var `&gender=male` injetada via `document.write` casa a regra `Site - Variable`.
- [ ] **Dado** `AND` com condições mutuamente exclusivas, **então** a UI **alerta** (anti-contradição) antes de salvar.
- [ ] Um Rule Set criado em Preferences é reaplicável em ≥ 2 banners sem redigitar condições.

### CA-5 — Frequency capping (DA-6, §4.8)
- [ ] Cap `campaign_total` limita exibições por usuário ao teto durante a campanha.
- [ ] Cap `session` zera ao fechar o navegador.
- [ ] Cap `clock` reseta no `reset_interval` configurado, independente do usuário.
- [ ] Cap no banner sobrescreve cap divergente na campanha.
- [ ] **Dado** navegador sem cookies, **quando** há cap ativo, **então** a entrega capeada é **abortada** (fail-safe), não estourada.

### CA-6 — Telemetria (DA-7, DA-8, §4.7)
- [ ] Impressão só é contabilizada após carga do pixel 1×1 (não no disparo do request).
- [ ] Clique passa pelo servidor (contabiliza) e então emite `302` para `dest_url`.
- [ ] Conversão é atribuída quando o pixel terminal dispara.
- [ ] Painéis consolidam estatísticas em batch **horário** (defasagem ≤ 1h); sem atualização milissegundo a milissegundo.
- [ ] Diferença Request − Impression é exposta como indicador de perda/escassez de inventário.

### CA-7 — Precificação e moeda (DA-10, §4.9)
- [ ] CPM fatura a cada 1.000 impressões; CPC por clique; CPA por conversão; Tenancy por período fixo.
- [ ] Valores monetários usam tipo decimal de ponto fixo (`NUMERIC`); **nenhum** uso de `float`.
- [ ] O sistema aceita múltiplas moedas como rótulo, **sem** conversão cambial automática.

### CA-8 — Privacidade e conformidade (DA-11)
- [ ] Nenhum PII é persistido em perfil central; first-party data trafega só na requisição.
- [ ] Inspeção do fluxo confirma ausência de transmissão inter-regional opaca de dados pessoais.

### CA-9 — Plataforma e operação (DA-12, §4.10)
- [ ] Aplicação instala em stack LAMP/LEMP (PHP + MySQL/MariaDB).
- [ ] Plugins residem em `/etc/plugins`; ausência deles não deve esvaziar silenciosamente o menu de Rule Sets sem diagnóstico.
- [ ] MaxMind: chave de licença aceita e arquivos GeoLite2 auto-atualizam sem intervenção manual.
- [ ] Mailer/SMTP entrega relatórios batch sem cair em blacklist (sem uso do mail PHP nativo).

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
