# Política de lint anti-float (TX-2)

> **Status:** Fase 0 (Fundações) — bloqueante.
> **Normativo:** `docs/stack-tecnologico.md` §TX-2, §DA-10; `docs/documentacao-tecnica.md` §DA-10.
> **Relacionado:** [`../money/money-type.md`](../money/money-type.md)

**Regra:** `float` é **PROIBIDO em código monetário**, em CI (lint **e** teste), nas 4
linguagens da stack — Go, TypeScript, Python, SQL. Dinheiro usa `Money`/`NUMERIC`/`Decimal`/
`bigint` (ver contrato canônico). Float introduz a classe inteira de bug de precisão decimal.

## Escopo (importante)

O lint é **restrito aos diretórios/módulos financeiros** — **não** barra floats legítimos em
ML, telemetria de performance, ranking (`pCTR`/`pCVR`), métricas etc. Escopo sugerido:

```
# Diretórios/sufixos considerados "financeiros" (ajustar à árvore real do monorepo):
**/money/**            **/ledger/**           **/billing/**
**/payments/**         **/asset*registry*/**  **/*_money_*  **/*.money.*
**/chainconnector/**   (manipula Money no limite on-chain — Fase 3)
migrations/**          db/migrations/**       sql/**
```

Tudo fora desse escopo é ignorado pelos checks abaixo.

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
float intermediário de probabilidade carrega `//nolint:forbidigo // <motivo>`). Os arquivos ML
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
'*billing*/*.go' '*payments*/*.go' '*chainconnector*/*.go'`. Complementa o forbidigo
(lint _type-aware_) acima — o grep é um backstop de path independente do golangci-lint.

---

## 2. TypeScript (front / BFF)

Proibir aritmética monetária com `Number`/`parseFloat`; exigir `decimal.js` ou `bigint`;
usar o tipo **branded** `Money`. Via **ESLint** (`no-restricted-syntax` + restrição de
globals), aplicado por override de path:

```jsonc
// .eslintrc.json  (override só nos paths financeiros)
{
  "overrides": [
    {
      "files": [
        "**/money/**/*.ts", "**/ledger/**/*.ts",
        "**/billing/**/*.ts", "**/payments/**/*.ts"
      ],
      "rules": {
        "no-restricted-globals": [
          "error",
          { "name": "parseFloat", "message": "parseFloat proibido em dinheiro (TX-2): use decimal.js/bigint" }
        ],
        "no-restricted-syntax": [
          "error",
          {
            "selector": "TSNumberKeyword",
            "message": "tipo 'number' proibido em dinheiro (TX-2): use Money (decimal.js string) ou bigint"
          },
          {
            "selector": "CallExpression[callee.object.name='Number']",
            "message": "Number(...) proibido em dinheiro (TX-2): use decimal.js/bigint"
          },
          {
            "selector": "Literal[raw=/^[0-9]+\\.[0-9]+$/]",
            "message": "literal float proibido em dinheiro (TX-2): use string decimal + decimal.js"
          }
        ]
      }
    }
  ]
}
```

Reforço de revisão: o tipo `Money` é **branded** (ver `money-type.md` §7), o que impede
passar um `number` cru onde o contrato espera `Money`.

---

## 3. Python (ML/on-chain)

Proibir `float()` e literais float em módulos de dinheiro; exigir `decimal.Decimal` com
contexto fixo (`ROUND_HALF_EVEN`). Via **Ruff** (`flake8-forbidden`/regra de seleção) +
guard de CI por escopo:

```toml
# pyproject.toml  (Ruff aplicado a todo o repo; o check de float roda no escopo financeiro)
[tool.ruff.lint]
# AST de float legitimo em ML nao deve ser global — usar o grep de CI abaixo p/ escopo.
select = ["E", "F"]
```

```bash
# scripts/ci/no-float-py.sh  (escopo financeiro; falha o build)
set -euo pipefail
files=$(git ls-files '*money*/*.py' '*ledger*/*.py' '*billing*/*.py' '*payments*/*.py')
[ -z "$files" ] && exit 0
# proibe float(), literais float (1.0, .5, 1e3) e anotacoes : float
hits=$(grep -RInE '\bfloat\s*\(|:\s*float\b|[^A-Za-z0-9_.]([0-9]+\.[0-9]+|\.[0-9]+|[0-9]+[eE][+-]?[0-9]+)' $files || true)
[ -z "$hits" ] || { echo "float proibido em codigo monetario (Python/TX-2): use decimal.Decimal"; echo "$hits"; exit 1; }
```

---

## 4. SQL / migrations

Proibir `FLOAT`/`DOUBLE PRECISION`/`REAL`/`MONEY` em colunas monetárias; exigir `NUMERIC`.
Linter de migração por **grep/regex em CI** sobre os arquivos de migração:

```bash
# scripts/ci/no-float-sql.sh  (falha o build)
set -euo pipefail
files=$(git ls-files 'migrations/*.sql' 'db/migrations/*.sql' 'sql/*.sql')
[ -z "$files" ] && exit 0
# tipos de ponto flutuante e MONEY (que arredonda/locale-dependente) sao proibidos
hits=$(grep -RIniE '\b(float[0-9]*|double\s+precision|real|money)\b' $files || true)
[ -z "$hits" ] || { echo "tipo flutuante/MONEY proibido em coluna monetaria (SQL/TX-2): use NUMERIC"; echo "$hits"; exit 1; }
```

> `NUMERIC(precision, scale)` por ativo, com `scale` vindo do Asset Registry (ver
> `money-type.md` §5). `MONEY` é proibido por ser locale-dependente e de escala fixa.

O guard real (`scripts/ci/no-float-sql.sh`) é **comment-aware**: remove o comentário
inline (`-- …`) antes de testar o tipo, e o escaneamento cobre todas as migrations
(`db/*/migrations/*.sql`), não só caminhos financeiros — uma coluna monetária pode
viver em qualquer schema. Para uma coluna **não-monetária** que legitimamente precise
de `DOUBLE PRECISION`/`FLOAT` (ex.: `ctr` como taxa de ML/ranking ∈ [0,1], ou um score
de similaridade), use o marcador explícito **`no-float-ok`** no comentário inline da
própria linha, com a justificativa:

```sql
ctr DOUBLE PRECISION NOT NULL DEFAULT 0.0  -- no-float-ok: taxa de ML/ranking [0,1], não é dinheiro (TX-2)
```

O marcador é **greppável e auditável** (`git grep no-float-ok`) — mesma filosofia do
`//nolint` do guard Go. Nunca o use em coluna que carregue valor monetário.

---

## 5. Job de CI (GitHub Actions)

Roda os 4 checks; qualquer violação **falha o build**. Escopo financeiro embutido em cada script.

```yaml
# .github/workflows/no-float.yml
name: no-float (TX-2)
on:
  pull_request:
  push:
    branches: [main]
jobs:
  no-float:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Go - sem float em dinheiro
        run: bash scripts/ci/no-float-go.sh
      - name: TypeScript - lint financeiro (ESLint)
        run: npx eslint "**/{money,ledger,billing,payments}/**/*.ts"
      - name: Python - sem float em dinheiro
        run: bash scripts/ci/no-float-py.sh
      - name: SQL/migrations - sem FLOAT/MONEY
        run: bash scripts/ci/no-float-sql.sh
```

**Resumo:** float **proibido** em código financeiro nas 4 linguagens; escopo restrito a
`money/ledger/billing/payments/migrations` para **não** punir floats legítimos de ML e
telemetria. Coerente com o contrato `Money` e o Asset Registry desta pasta.
