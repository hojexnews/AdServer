# Política de lint anti-float (TX-2)

> **Status:** Fase 0 (Fundações) — bloqueante.
> **Normativo:** `docs/stack-tecnologico.md` §TX-2, §DA-10; `docs/documentacao-tecnica.md` §DA-10.
> **Relacionado:** [`../money/money-type.md`](../money/money-type.md)

**Regra:** `float` é **PROIBIDO em código monetário**, em CI (lint **e** teste), nas 4
linguagens da stack — Go, TypeScript, Python, SQL. Dinheiro usa `Money`/`NUMERIC`/`Decimal`/
`bigint` (ver contrato canônico). Float introduz a classe inteira de bug de precisão decimal.

## Escopo (importante)

O objetivo é barrar float em **dinheiro** sem barrar floats legítimos de ML, telemetria de
performance, ranking (`pCTR`/`pCVR`), métricas etc. A **28ª onda** aprendeu que restringir o
gate a diretórios/nomes "financeiros" é um ponto-cego ativo (um campo monetário podia ser
adicionado fora de `money/`/`payments/`, ou um arquivo de dinheiro com nome não-convencional
passava sem lint). Por isso o escopo real hoje é **abrangente com exceção explícita**, por linguagem:

Esta seção descreve a **regra de seleção** de cada guard — não um censo de arquivos. O
conjunto de arquivos muda a cada commit; a regra, não. Cada linha abaixo é **re-derivável
hoje** rodando o comando indicado; o script em `scripts/ci/` é a fonte única da verdade, e
se ele divergir desta descrição, **o script vence** — corrija esta seção, não o script.

```text
# Escopo REAL por linguagem — regra de selecao vigente (ver §1–§5 e scripts/ci/):

Proto  → DEFAULT-DENY sobre TODO o Schema Registry.
         Seleção: git ls-files 'proto/adserver/**/*.proto'
         Barra double/float em qualquer mensagem; libera apenas o que estiver na
         ALLOWLIST de scripts/ci/no-float-proto.sh, chaveada por 4 elementos —
         (arquivo, MENSAGEM, campo, tag). O nome da MENSAGEM entra na chave porque
         em proto3 a numeração de tag é POR-mensagem: sem ele, uma mensagem NOVA que
         colidisse por acaso com um (campo, tag) já revisado seria liberada em
         silêncio. Hoje a allowlist tem 4 entradas, todas em decision.proto
         (Candidate.score/propensity, Decision.propensity/epsilon).

Go     → escopo POSITIVO explícito (float nunca é legítimo nesses caminhos).
         Seleção do backstop grep (scripts/ci/no-float-go.sh):
           git ls-files '*money*/*.go' '*ledger*/*.go' '*billing*/*.go' \
                        '*payments*/*.go' '*chainconnector*/*.go'
           + internal/configload/{loader,assemble}.go (nomeados, 29ª onda #1)
           + internal/ranker/score.go (só o check de LITERAL decimal)
         Note que os globs casam um COMPONENTE DE DIRETÓRIO (qualquer pasta
         .../billing/... entra, venha de onde vier), não o nome do arquivo.
         Barra token float32/float64 E literal decimal implícito. O lint
         type-aware (forbidigo) roda sobre o path-except de .golangci.yml.

TS/TSX → duas camadas; a de baixo é default-deny por CONTEÚDO.
         (a) ESLint por NOME de arquivo, no step 3 de no-float.yml:
             git ls-files 'bff/src/**/*.ts' | grep -E '(money|ledger|billing|payments)'
         (b) backstop por CONTEÚDO (scripts/ci/no-float-ts.sh), independente do nome:
             git ls-files 'bff/src/**/*.ts' 'web/console/src/**/*.ts' \
                          'web/console/src/**/*.tsx'   (menos *.test.* e gen/)
             Reprova a CO-OCORRÊNCIA de identificador de dinheiro + parseFloat/
             Number(/literal decimal dentro do mesmo SEGMENTO LÓGICO (não linha
             física — ver §2), com cap de 6 linhas.
         (c) console: web/console/eslint.config.mjs cobre arquivos de dinheiro por
             nome MAIS todas as páginas do App Router (src/app/**/*.tsx), em
             default-deny — MONEY_TSX_EXCLUDE está vazio (29ª onda #2).

Python → escopo POSITIVO por diretório + os jobs de faturamento, via dois guards.
         Seleção de scripts/ci/no-float-py.sh:
           git ls-files '*money*/*.py' '*ledger*/*.py' '*billing*/*.py' \
                        '*payments*/*.py' 'ml/fraud/*.py' 'ml/pacing/*.py'
           (hoje os 4 primeiros globs casam ZERO arquivo — não há pacote Python de
           dinheiro sob essa convenção; o que de fato é varrido é ml/fraud + ml/pacing.)
         Motor canônico de faturamento (data/iceberg/jobs/*.py, CPM/CPC/CPA) é
         varrido pelo bloco Python string-aware de scripts/ci/no-float-data-sql.sh
         (29ª onda #3), NÃO por no-float-py.sh.
         Ambos exigem CO-OCORRÊNCIA com nome monetário — float de feature ML segue
         livre. A janela do no-float-py.sh é a LINHA LÓGICA do tokenizer CPython
         (não a linha física), então uma quebra do Black não derrota a conjunção.

SQL    → DEFAULT-DENY sobre TODO .sql rastreado no repositório.
         Seleção de scripts/ci/no-float-sql.sh (modo sem argumento, o usado por
         `make no-float` e pela CI):
           git ls-files '*.sql' | grep -v -E '^(data/clickhouse/|data/iceberg/)'
         NÃO é mais uma allowlist de padrões de migration: db/*/tests/*.sql,
         db/seed/*.sql e deploy/local/seeds/*.sql — que a allowlist antiga nunca
         escaneou — entram por construção (30ª onda). A ÚNICA exclusão é por
         DIRETÓRIO (data/clickhouse/, data/iceberg/), delegada ao script irmão
         no-float-data-sql.sh, que tem exceções conscientes de NOME DE COLUNA para
         floats de ML. Opt-out por linha: marcador `no-float-ok` no comentário.
```

Fora desses escopos, o float não é verificado por estes guards — o que **não** é uma
licença: é o motivo de o escopo ser default-deny onde dinheiro pode aparecer.

---

## 1. Go (hot path)

Proibir `float32`/`float64` em pacotes/tipos de dinheiro; usar `Money` (int64+scale) ou
`github.com/shopspring/decimal` no batch. Via **golangci-lint / `forbidigo`** — **LIGADO** em
[`.golangci.yml`](../../.golangci.yml) (schema v2) + `make go-lint` + o step _lint_ do workflow
`go.yml` (Go 1.26). Antes deste wiring o `.golangci.yml` **não existia**: a metade "lint" da
enforcement era só aspiracional — os ~105 `//nolint:forbidigo` do hot path não opt-avam de gate
algum. (Falso-positivo detectado e corrigido: a doc afirmava "em CI — lint E teste", mas só o
grep + os testes rodavam.)

**Escopo** (via `path-except` no `.golangci.yml`): os pacotes **puro-dinheiro**
(`internal/money`, `internal/ledger`, `services/payments`, `internal/chainconnector` — onde float
NUNCA é legítimo) **mais** o único arquivo do **money-point eCPM** (`internal/ranker/score.go`, cujo
float intermediário de probabilidade carrega `//nolint:forbidigo // <motivo>`) **mais**
`internal/configload/{loader,assemble}.go` (29ª onda #1 — `decimalToMinor` NUMERIC→minor-units e a
montagem do `moneyv1.Money.Amount` autoritativo; o nome do diretório não casava os globs financeiros,
o ponto-cego da lição-mãe da 28ª onda). Os arquivos ML
mistos (`ranker/bandit`, `ranker/featurize`, `cascade`) têm float legítimo pervasivo (pCTR,
propensity, feature vector) e ficam **fora** do escopo — forbidigo proíbe o _tipo_ float por atacado
e seria ruído lá; esses caminhos são cobertos por testes known-answer (ex.: `TestScoreCandidateECPM_*`).

```yaml
# .golangci.yml (v2) — trecho normativo; ver o arquivo real para o path-except completo.
linters:
  default: none
  enable: [forbidigo]
  settings:
    forbidigo:
      forbid:
        - pattern: '\bfloat32\b'
        - pattern: '\bfloat64\b'
      analyze-types: true
```

Backstop grep complementar (falha o build), rodado no workflow `no-float.yml` —
[`scripts/ci/no-float-go.sh`](../../scripts/ci/no-float-go.sh): tokenizer Go (remove
comentários/strings antes de testar `\bfloat(32|64)\b`, evitando falso-positivo em comentários
que documentam a regra) sobre o escopo `git ls-files '*money*/*.go' '*ledger*/*.go'
'*billing*/*.go' '*payments*/*.go' '*chainconnector*/*.go'` **mais** os arquivos nomeados
`internal/configload/{loader,assemble}.go` (29ª onda #1). `internal/ranker/score.go` entra
numa lista separada, submetida **apenas** ao check de **literal decimal** (não ao de token
`float32`/`float64`, que ali seria ruído) — é o forbidigo do `.golangci.yml`, com os
`//nolint` justificados, que cobre o tipo nesse arquivo. Note que os globs casam um
**componente de diretório** (`*billing*/`), não o nome do arquivo: uma pasta `billing/`
criada em qualquer lugar do repo entra no escopo automaticamente. Complementa o forbidigo
(lint _type-aware_) acima — o grep é um backstop de path independente do golangci-lint.

---

## 2. TypeScript (front / BFF)

Proibir aritmética monetária com `Number`/`parseFloat`; exigir `decimal.js` ou `bigint`;
usar o tipo **branded** `Money`. Duas camadas: **ESLint** por glob (AST) e um **backstop
textual** por conteúdo.

As configs reais são **flat config** (ESLint 9+) — [`eslint.config.mjs`](../../eslint.config.mjs)
na raiz (escopo BFF, usado pelo step 3 do `no-float.yml`), `bff/eslint.config.js` e
[`web/console/eslint.config.mjs`](../../web/console/eslint.config.mjs). **Não existe
`.eslintrc.json` neste repo**: versões anteriores desta seção exibiam um bloco
`.eslintrc.json` com `overrides` por diretório (`**/money/**/*.ts`…), formato que o repo
abandonou e globs de diretório que não casam arquivo real algum — leia as configs reais.

Os 3 seletores impostos, nos arquivos selecionados por glob:

| Regra ESLint | Alvo | Por quê |
| --- | --- | --- |
| `no-restricted-globals` | `parseFloat` | float binário em dinheiro |
| `no-restricted-syntax` → `CallExpression[callee.name='Number']` | `Number(x)` | coerção para float |
| `no-restricted-syntax` → `Literal[raw=/^[0-9]+\.[0-9]+$/]` | `12.34` | literal decimal em dinheiro |

O tipo `number` cru **não** é barrado por seletor: um `TSNumberKeyword` marcaria toda
anotação `number` do arquivo (ex.: `formatCount(count: number)`) e seria impraticável.
Esse invariante é de **compile-time**, via tipo branded `Money`.

Portanto, descreva o console como **3 seletores de lint + tipo branded** (`money-type.md`
§7) — nunca como "4 regras de lint" (29ª onda #14: o 4º seletor nunca existiu em config
alguma).

**Backstop por conteúdo** — [`scripts/ci/no-float-ts.sh`](../../scripts/ci/no-float-ts.sh),
que roda mesmo onde nenhum glob de nome casa (escopo em §Escopo). Ele reprova a
**co-ocorrência** de um identificador de dinheiro (casado por palavra dentro do
identificador, snake_case ou camelCase — `costume` não casa `cost`) com
`parseFloat(`/`Number(`/literal decimal. Duas propriedades a conhecer antes de confiar
nele:

- **Janela = segmento lógico, não linha física** (30ª onda). Antes, qualquer quebra do
  Prettier (`const rateAmount =\n  12.50;`) separava o identificador do literal e derrotava
  a conjunção. Agora linhas são agrupadas por profundidade de parênteses/colchetes/chaves e
  por operador de continuação à direita, com **cap de 6 linhas** (`MAX_SEGMENT_LINES`) para
  que um `return (<JSX gigante>)` não vire um segmento único e exploda em falsos-positivos.
- **`.tsx` tem guarda extra**: um literal decimal isolado só dispara se a mesma linha tiver
  sinal independente de código (`=`, `;`, `{`, `+`, `-`, `*`, `return`) — texto de UI
  (`ex.: 5.00`) não carrega nenhum. `parseFloat(`/`Number(` disparam sem essa guarda. A
  contrapartida honesta: um literal nu em property-line de `.tsx` sem sinal de código passa
  pelo backstop — é o ESLint AST (que cobre `src/app/**/*.tsx` em default-deny) que fecha
  esse caso.

---

## 3. Python (ML/on-chain)

Proibir `float()` e literais float em módulos de dinheiro; exigir `decimal.Decimal` com
contexto fixo (`ROUND_HALF_EVEN`) ou `int` em minor-units.

**Quem impõe isso hoje:** apenas os guards de CI — [`scripts/ci/no-float-py.sh`](../../scripts/ci/no-float-py.sh)
(escopo em §Escopo) e o bloco Python de [`scripts/ci/no-float-data-sql.sh`](../../scripts/ci/no-float-data-sql.sh)
(que cobre o motor de faturamento em `data/iceberg/jobs/`). Ambos falham o build.

> **Não há linter Python de TX-2.** Versões anteriores desta seção afirmavam enforcement
> "via **Ruff** (`flake8-forbidden`)" e exibiam um `pyproject.toml` de raiz — que **não
> existe**. O único `pyproject.toml` do repo é `services/copilot/pyproject.toml`, cujo
> `[tool.ruff]` seleciona `E,F,W,I,N,UP,B,S,A` (estilo/segurança, nada sobre float) e
> **não é executado por nenhum alvo `make` nem workflow** (`grep -rn ruff make/ Makefile
> .github/` retorna vazio). Era a mesma classe do doc-lie do `forbidigo` corrigido na 26ª
> onda: prometer um linter que nunca roda. Se o gate de lint Python for desejado, ele
> precisa ser ligado e citado aqui — até lá, esta seção declara a lacuna em vez de
> inventar cobertura.

**Como o guard decide** (evita punir float legítimo de feature ML): exige
**co-ocorrência**, na mesma _linha lógica_ Python, de (a) um identificador monetário
(`amount`, `price`, `cpm/cpc/cpa`, `bid`, `budget`, `revenue`, `cost`, `spend`, `payout`,
`charge`, `minor_units`, `money`…, com fronteira `_`-aware para pegar `total_cost`) e (b)
`float(` ou um literal de ponto flutuante. A janela é a **linha lógica do tokenizer
CPython**, não a linha física — uma quebra do Black (`payout_amount = (\n 12.50\n)`) não
derrota mais a conjunção (30ª onda). Comentários e strings (inclusive docstrings) são
removidos antes do teste.

---

## 4. SQL (todo `.sql` rastreado — não só migrations)

Proibir `FLOAT`/`DOUBLE PRECISION`/`REAL`/`MONEY` como tipo de coluna; exigir `NUMERIC`.
Imposto por [`scripts/ci/no-float-sql.sh`](../../scripts/ci/no-float-sql.sh), que falha o
build. O script é a fonte da verdade — esta seção o descreve, não o duplica.

**Escopo: DEFAULT-DENY.** No modo sem argumento (o usado por `make no-float` e pela CI), o
guard escaneia **todo `.sql` rastreado pelo git**, com uma única exclusão, por
**diretório**: `data/clickhouse/` e `data/iceberg/`, delegados ao script irmão
`no-float-data-sql.sh` (que tem exceções conscientes de nome de coluna para floats de ML e
reprovaria em falso sob o matcher genérico). Reproduza a lista exata com:

```bash
git ls-files '*.sql' | grep -v -E '^(data/clickhouse/|data/iceberg/)'
```

> **Mudança da 30ª onda.** Até então o escopo era uma **allowlist de padrões de caminho**
> (`'migrations/*.sql' 'db/migrations/*.sql' 'sql/*.sql' 'db/*/migrations/*.sql'`), e tudo
> fora da convenção escapava: `db/*/tests/*.sql`, `db/seed/*.sql` (incluindo
> `hojex_news_seed.sql`, que insere em `ledger`/`config` — o de maior risco monetário
> real) e `deploy/local/seeds/*.sql` **nunca foram escaneados**, nem pela CI nem por
> `make no-float`. Era a lição-mãe da 28ª onda reincidindo: escopar por convenção de
> caminho é ponto-cego ativo. O modo com argumento (`no-float-sql.sh <dir>`) segue
> existindo para uso pontual e varre o diretório recursivamente por `find`.

**Tipo correto:** `NUMERIC(precision, scale)` por ativo, com `scale` vindo do Asset
Registry (ver `money-type.md` §5). `MONEY` é proibido por ser locale-dependente e de
escala fixa.

O guard é **comment-aware e string-aware**: remove strings literais (`'…'`) e o comentário
inline (`-- …`) antes de testar o tipo, e pula linhas de comentário puro e `COMMENT ON` —
assim `'Real Brasileiro'` dentro de uma string não dispara, mas `REAL` como tipo DDL
dispara. Para uma coluna **não-monetária** que legitimamente precise
de `DOUBLE PRECISION`/`FLOAT` (ex.: `ctr` como taxa de ML/ranking ∈ [0,1], ou um score
de similaridade), use o marcador explícito **`no-float-ok`** no comentário inline da
própria linha, com a justificativa:

```sql
ctr DOUBLE PRECISION NOT NULL DEFAULT 0.0  -- no-float-ok: taxa de ML/ranking [0,1], não é dinheiro (TX-2)
```

O marcador é **greppável e auditável** (`git grep no-float-ok`) — mesma filosofia do
`//nolint` do guard Go. Nunca o use em coluna que carregue valor monetário.

**Inventário honesto do guard `data/` (pós-correção da 30ª onda/4ª rodada) — o que cobre
hoje e o que comprovadamente ainda escapa.** `scripts/ci/no-float-data-sql.sh` tem 3
braços sobre `data/clickhouse/` e `data/iceberg/` (o 4º, Python, cobre
`data/iceberg/jobs/` e está descrito em §3). Os 3 braços seguem hoje **default-deny
sobre o TIPO**, cada um liberado só por exceção nomeada — vocabulário de ML/probabilidade
(`propensity|score|epsilon|pct|probability`, mais `prob` no braço YAML — cobre `ivt_prob`,
o único campo `double` hoje no corpus Iceberg) ou o marcador por-linha `no-float-ok`:

- **ClickHouse** (`check_sql_no_monetary_float`): até a 4ª rodada da 30ª onda, este braço
  decidia "isto é dinheiro" por uma **denylist de nomes enumerada à mão**
  (`value|amount|rate|decimal|revenue|cost|budget|price|cpm|cpc|cpa|bid|spend|payout|
  charge|billing|money|minor_units`) — uma coluna monetária com nome fora do vocabulário
  (ex.: `gross_earnings`) escapava **por construção**, a mesma classe de scope-blindspot
  que as ondas 28ª/29ª já tinham declarado lição-mãe. Provado por mutação nesta rodada:
  renomear a coluna `propensity Float64` (migration `002_raw_tables.sql`) para
  `gross_earnings Float64` deixava o guard **verde**. Corrigido invertendo a forma: **toda**
  `Float32`/`Float64` no escopo dispara por padrão; passa só com exceção de nome ML ou
  marcador `no-float-ok` (já presente nas migrations reais: `ivt_prob`, `if_score`,
  `ae_error`, `ae_threshold`).
- **YAML (specs Iceberg)**: até a 4ª rodada, este braço só casava `type: float` — mas o
  tipo de ponto flutuante de 64 bits do Iceberg chama-se **`double`** (a spec de tipos
  primitivos Iceberg define exatamente dois tipos de ponto flutuante, `float` de 32 bits e
  `double` de 64 bits; não existe `float32`/`float64` como nome de tipo). Provado por
  mutação pelo tech lead: `rate_amount: decimal(38, 18)` → `type: double` em
  `billing_hourly.yaml` (a taxa contratual de faturamento) saía com `no-float-data-sql: ok`
  e `EXIT=0`. Corrigido casando `float` **e** `double`, também invertido para default-deny
  por exceção de nome (mesmo mecanismo do braço ClickHouse).
- **Python** (`data/iceberg/jobs/*.py`): já era default-deny por co-ocorrência desde a 29ª
  onda (§ acima) — sem mudança nesta rodada.

Cada correção acima foi provada por mutação (código limpo → verde; mutação → vermelho;
reversão → verde) — não é uma alegação de cobertura sem teste.

**Lacuna conhecida que permanece, verificada de 1ª mão (2026-07-19) — não é cobertura, é
aviso.** Um `ALTER TABLE … ADD COLUMN <nome> Float64;` **dentro de `data/clickhouse/` ou
`data/iceberg/`** ainda escapa dos dois guards, mesmo pós-correção: `no-float-sql.sh`
exclui essas árvores por diretório, e `no-float-data-sql.sh` (nos dois braços acima) só
casa **linhas de definição de coluna indentadas** (`^\s+nome Tipo`), forma que um
`ALTER TABLE` de coluna única não tem — o default-deny por tipo não ajuda aqui porque a
linha nunca entra no match de "definição de coluna" para começo de conversa. Reprovado
injetando `ALTER TABLE adserver.stats_hourly ADD COLUMN sonda_revenue Float64;` em
`data/clickhouse/migrations/004_stats_hourly.sql` **depois** da correção desta rodada:
ambos os guards seguem verdes (a mesma coluna, escrita como definição indentada dentro do
`CREATE TABLE`, é corretamente reprovada — o gap é especificamente a forma `ALTER TABLE`).
Até que `no-float-data-sql.sh` reconheça essa forma, toda alteração de coluna monetária
via `ALTER TABLE` nessas árvores depende de revisão humana — não confie no gate para isso.

---

## 5. Job de CI (GitHub Actions)

Roda os checks (proto + 4 linguagens); qualquer violação **falha o build**. Escopo
financeiro embutido em cada script. O workflow real é a única fonte de verdade —
[`.github/workflows/no-float.yml`](../../.github/workflows/no-float.yml). Esta seção
descreve os jobs sem duplicar o YAML verbatim: um bloco copiado já dessincronizou 2
vezes (a mais recente quando o backstop TS foi estendido ao console, 29ª onda) porque
nada impede o YAML de mudar sem esta doc acompanhar. Preferimos apontar para o
arquivo real a manter uma cópia sujeita a drift silencioso.

O job único `no-float` roda, em sequência, um step por linguagem — todos falham o
build em caso de violação:

1. **Proto** — `bash scripts/ci/no-float-proto.sh`: varre **todo**
   `proto/adserver/**/*.proto` (não só `money/`/`payments/`) com default-deny +
   allowlist explícita, chaveada por `(arquivo, mensagem, campo, tag)`, para os
   poucos campos `double` legítimos de ML/ranking (`decision.proto`) — ver o
   cabeçalho do script para o porquê.
2. **Go** — `bash scripts/ci/no-float-go.sh`: backstop de path (ver §1).
3. **TypeScript (lint financeiro, BFF)** — roda ESLint só nos arquivos cujo NOME
   casa a convenção de dinheiro (`*money*.ts`, `*ledger*.ts`, `*billing*.ts`,
   `*payments*.ts` dentro de `bff/src/`), não por **diretório**
   (`**/{money,...}/**/*.ts`) — o glob de diretório não casa nenhum arquivo real
   deste repo (a convenção do BFF é nome de arquivo, não pasta dedicada) e faria o
   step passar silenciosamente sem lintar nada.
4. **TypeScript/TSX (backstop por CONTEÚDO)** — `bash scripts/ci/no-float-ts.sh`:
   fecha a lacuna complementar do step 3 — varre **todo** `bff/src` E
   `web/console/src` (não só os nomes convencionais) e reprova
   `parseFloat`/`Number(`/literal-decimal em **segmentos lógicos** que toquem
   identificadores de dinheiro (ver §2), mesmo em arquivos cujo nome foge da
   convenção (achado nº 2 da 28ª onda — ex.: `refunds-cash.ts`) ou no console
   (29ª onda #2).
5. **Python** — `bash scripts/ci/no-float-py.sh`: escopo em §Escopo/§3.
6. **SQL** — `bash scripts/ci/no-float-sql.sh`: **default-deny** sobre todo `.sql`
   rastreado, menos `data/clickhouse/` e `data/iceberg/` (ver §4). O nome do step no
   YAML ainda diz "SQL/migrations", mas o escopo **não** é mais só migrations.

**O 6º guard não está neste workflow.** `scripts/ci/no-float-data-sql.sh` (DDL de
`data/clickhouse/`, specs de `data/iceberg/` e os jobs Python de faturamento em
`data/iceberg/jobs/`) roda em [`.github/workflows/data.yml`](../../.github/workflows/data.yml)
via `make data-validate`. Localmente, `make no-float` executa os **6** guards
(`scripts/ci/no-float-*.sh`) sob a sentinela anti-skip `NO_FLOAT_SCRIPTS_EXPECTED := 6`
do `Makefile`, que **falha** se o glob deixar de casar algum script — logo `make no-float`
é estritamente mais abrangente que o workflow `no-float.yml` sozinho.

Se a lista de steps acima (nomes, ordem, escopo) e o `.github/workflows/no-float.yml`
real divergirem, o arquivo real vence — corrija esta seção para descrevê-lo, não o
contrário.

**Resumo:** float **proibido** em código financeiro nas linguagens do contrato (Proto,
Go, TypeScript, Python, SQL). O escopo **não** é uniforme, e a diferença importa:

- **Default-deny sobre o corpus inteiro** — Proto (allowlist de 4 elementos), SQL (todo
  `.sql` rastreado), e o console em `src/app/**/*.tsx`.
- **Default-deny sobre o corpus, com filtro de co-ocorrência** — o backstop TS/TSX (todo
  `bff/src` + `web/console/src`) e o guard Python: varrem tudo, mas só reprovam quando
  float e identificador monetário aparecem juntos. É o que permite não punir float
  legítimo de ML/telemetria sem re-estreitar o escopo por diretório.
- **Escopo positivo por caminho** — Go (globs de componente de diretório + arquivos
  nomeados) e o step ESLint por nome do BFF.

Onde o escopo ainda é positivo, ele é a **superfície de risco conhecida**: dinheiro novo
fora desses caminhos escapa até alguém estendê-los, e foi essa a classe dominante de
falso-positivo das ondas 27ª–30ª. Ao adicionar código monetário fora da convenção,
estenda o guard **no mesmo PR**. Coerente com o contrato `Money` e o Asset Registry desta
pasta.
