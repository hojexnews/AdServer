# Contrato canônico `Money` (TX-2 / DA-10)

> **Status:** Fase 0 (Fundações) — bloqueante.
> **Normativo:** `docs/stack-tecnologico.md` §TX-2, §2.5, §2.6, §DA-10; `docs/documentacao-tecnica.md` §DA-10.
> **Coerência de fio:** `proto/adserver/money/v1/money.proto` → `adserver.money.v1.Money` (criado por outro time; **apenas referenciado aqui, nunca editado**).

`Money` é o **único** tipo monetário do sistema. Ele atravessa **todas** as fronteiras —
evento → ledger → BFF → UI — sem nunca virar `float`/`Number` e **sem conversão cambial
automática**. Toda aritmética de dinheiro do produto é centralizada neste contrato; é o
ponto onde se mata a classe inteira de "bug financeiro clássico" (precisão decimal por
ativo).

---

## 1. Definição canônica

`Money` é uma tripla **imutável**:

| Campo        | Tipo lógico                | Significado                                                                                  |
|--------------|----------------------------|---------------------------------------------------------------------------------------------|
| `asset_code` | `string` (FK lógica)       | Código do ativo no **Asset Registry** (ex.: `BRL`, `USDC`). Determina `scale`, `kind`, etc. |
| `amount`     | inteiro (minor units)      | Quantia em **unidades menores** (centavos, micro-USDC, wei…). Inteiro com sinal.             |
| `scale`      | inteiro `>= 0`             | Casas decimais. **Autoritativo no Asset Registry; NUNCA inferido** do valor ou do contexto.  |

Regras estruturais:

- `scale` **DEVE** ser igual ao `scale` do `asset_code` no Asset Registry no momento da
  operação. O `scale` viaja junto do valor (no fio e no ledger) para auditoria e para
  travar inconsistências, mas a **fonte da verdade é o Registry** — divergência é erro.
- `amount` é **sempre inteiro**. Não existe `Money` com parte fracionária fora de `amount`.
- `Money` é **imutável**: operações retornam um novo `Money`; nunca mutam o original.
- Operações binárias (`+`, `-`, comparações) só são válidas entre dois `Money` de
  **mesmo `asset_code`** (e, portanto, mesmo `scale`). Misturar ativos é **erro de tipo**,
  não conversão.

---

## 2. Representação por fronteira

| Fronteira          | Representação                                                                                                  | Observações |
|--------------------|---------------------------------------------------------------------------------------------------------------|-------------|
| **No fio (Protobuf)** | `adserver.money.v1.Money { string asset_code; int64 amount; uint32 scale; }`                                | `int64` minor-units + `scale` explícito. Campo gerenciado por Buf (breaking-check BACKWARD). Para ativos de 18 casas (ERC-20), `int64` cobre faixas de produto realistas; valores que estourem `int64` viajam como `Money` decimal-string (ver abaixo) e nunca como float. |
| **No Postgres (ledger)** | Coluna `amount` `NUMERIC(precision, scale_do_ativo)` **+** coluna `asset_code`.                          | `scale` da coluna = `scale` do ativo no Registry. Nunca `FLOAT/DOUBLE/REAL/MONEY`. `precision` dimensionada por ativo (ver §5). |
| **BFF → UI**       | JSON `{ "asset_code": "BRL", "amount": "123.45", "scale": 2, "display": "R$ 123,45" }` — `amount` como **string DECIMAL** + rótulo de moeda. | O BFF entrega já-formatado **e** o decimal nu como string; o front **não** faz aritmética. |
| **No front**       | `decimal.js` / `Intl.NumberFormat` para fiat/stablecoin; `bigint` para cripto (18 dec).                        | **NUNCA `Number`**, **nunca aritmética monetária no cliente**, **nunca conversão automática.** O front só formata/exibe. |

> O nome de mensagem, pacote e numeração de campos do Protobuf são de responsabilidade do
> schema registry em `proto/`. Este contrato apenas **garante coerência semântica** dos três
> campos `asset_code` / `amount` / `scale`.

---

## 3. Invariantes (CI deve falhar se violadas)

1. **`float` proibido** em qualquer código monetário, em todas as linguagens (Go, Python,
   TS, SQL). Política acionável em [`../lint/no-float.md`](../lint/no-float.md).
2. **Sem conversão cambial automática (DA-10).** Não existe API de câmbio embutida. Cada
   ativo é um **ledger isolado**.
3. **Câmbio só como par de postings explícito**, com **taxa registrada por humano/desk** —
   nunca derivada em runtime por um feed implícito.
4. **eCPM e quaisquer comparações** ocorrem **dentro da mesma moeda/tenant**. Nunca se
   compara candidatos por câmbio implícito. (`eCPM = p × rate`, ambos no mesmo ativo.)
5. **Toda movimentação de ledger = par de postings idempotente** com
   `sum(debit) = sum(credit)`; nenhuma captura grava saldo direto; **idempotency key por
   captura**. Reconciliação **abre exceção e nunca autocorrige**.
6. **`scale` nunca é inferido.** Sempre lido do Asset Registry. Divergência entre o `scale`
   do payload e o do Registry é rejeitada.
7. **Sem PII** no ledger e na telemetria (TX-3/DA-11). `Money` carrega `tenant_id` por
   referência (no envelope), nunca dados pessoais.

---

## 4. Conversão `amount` ↔ decimal

A relação é puramente posicional, **sem float**:

```
decimal = amount / 10^scale            (apresentação)
amount  = round(decimal × 10^scale)    (entrada → minor units, ver §6)
```

### Exemplos

| Ativo            | `scale` | `amount` (minor units) | Decimal      | Exibição          | Tipo na borda |
|------------------|---------|------------------------|--------------|-------------------|---------------|
| BRL (`fiat`)     | 2       | `12345`                | `123.45`     | `R$ 123,45`  | `int64` / `NUMERIC(_,2)` |
| BRL              | 2       | `1`                    | `0.01`       | `R$ 0,01`    | menor unidade representável |
| USDC (`stablecoin`) | 6    | `1500000`              | `1.500000`   | `1,50 USDC`  | `int64` / `NUMERIC(_,6)` |
| ERC-20 genérico  | 18      | `1000000000000000000`  | `1.000000000000000000` | `1,00 TKN` | **`bigint`** (estoura precisão de `Number`) |
| ERC-20 genérico  | 18      | `250000000000000000`   | `0.25`       | `0,25 TKN`   | `bigint` |

Para ERC-20 (18 casas), `0.1 + 0.2` em `float` **nunca** dá `0.3`; em minor-units inteiras,
`100000000000000000 + 200000000000000000 = 300000000000000000` — exato. Esse é o motivo de
o tipo existir.

---

## 5. Dimensionamento de `precision` (Postgres)

`NUMERIC(precision, scale)` por ativo. `scale` vem do Registry; `precision` é folgada o
bastante para o teto de negócio do ativo **mais** o `scale`:

| Ativo            | Coluna sugerida   | Justificativa |
|------------------|-------------------|---------------|
| BRL/USD/EUR      | `NUMERIC(20, 2)`  | até 10^18 em unidades inteiras + 2 casas. |
| USDC/USDT        | `NUMERIC(26, 6)`  | grandes saldos de stablecoin + 6 casas. |
| ERC-20 (18)      | `NUMERIC(40, 18)` | wei-scale; sem risco de overflow. |
| AEV/BND          | **TBD**           | depende do `scale` definido (ver Asset Registry). |

---

## 6. Regras de arredondamento

**Modo único e global: `ROUND_HALF_EVEN` (banker's rounding).**

- **Por quê:** elimina o viés sistemático do `HALF_UP` em grandes volumes de impressões/
  faturamento (CPM/CPC/CPA acumulam milhões de micro-arredondamentos; `HALF_EVEN` não
  enviesa a receita para cima nem para baixo) e é o default de `decimal.Decimal` (Python) e
  o modo recomendado por `decimal.js`/`shopspring/decimal`.
- **Onde é PERMITIDO arredondar:** **somente** em fronteiras de **apresentação** (UI) e de
  **faturamento/captura** (ex.: converter um eCPM acumulado em fração de centavo para a
  fatura final no `scale` do ativo).
- **Onde é PROIBIDO arredondar:** **nunca** em **postings do ledger**. Postings são exatos
  em minor units; a soma de débitos e créditos tem de bater **ao centavo (ao minor unit)**.
  Qualquer "resto" de arredondamento de faturamento vira um **posting explícito** próprio
  (ex.: conta de arredondamento/dízimas), nunca um ajuste silencioso.
- O `scale` do arredondamento é **sempre** o `scale` do ativo no Registry — jamais um número
  mágico no código.

---

## 7. Mapeamento de tipos por linguagem

| Linguagem | Tipo canônico de transporte/ledger                            | Tipo de aritmética                                  | Proibido |
|-----------|---------------------------------------------------------------|-----------------------------------------------------|----------|
| **Go** (hot path) | `Money` struct `{ AssetCode string; Amount int64; Scale uint32 }` | aritmética em `int64` minor-units; em batch/relatórios, `github.com/shopspring/decimal` com escala do Registry | `float32`, `float64` |
| **Python** (ML/on-chain) | `decimal.Decimal` com **contexto fixo** (`prec` amplo, `rounding=ROUND_HALF_EVEN`) | `decimal.Decimal`; minor-units como `int` quando inteiro | `float`, literais float (`1.0`), `float(...)` |
| **TypeScript** (front/BFF) | tipo **branded** `Money` (string decimal + `asset_code` + `scale`) | `decimal.js` (fiat/stablecoin) ou `bigint` (cripto) | `Number` em aritmética monetária, `parseFloat`, `+`/`*` sobre `number` de dinheiro |
| **SQL** (Postgres) | coluna `NUMERIC(precision, scale_do_ativo)` + `asset_code`      | operadores nativos de `NUMERIC`                     | `FLOAT`, `DOUBLE PRECISION`, `REAL`, `MONEY` |

### Esqueleto Go (referência, não compilável aqui)

```go
// Package money — tipo canônico Money (TX-2). float PROIBIDO neste pacote.
type Money struct {
    AssetCode string // FK lógica p/ asset_registry.code
    Amount    int64  // minor units
    Scale     uint32 // autoritativo no Asset Registry; carregado p/ auditoria
}

// Add soma dois Money do MESMO ativo. Erro se asset_code/scale divergirem.
func (m Money) Add(o Money) (Money, error) { /* checa asset_code; soma int64 */ }
```

### Esqueleto Python (referência)

```python
from decimal import Decimal, Context, ROUND_HALF_EVEN
MONEY_CTX = Context(prec=60, rounding=ROUND_HALF_EVEN)  # contexto fixo, sem float
```

### Esqueleto TypeScript (referência)

```ts
// Branded Money: impede misturar com number cru.
export type AssetCode = string;
export interface Money { readonly assetCode: AssetCode; readonly amount: string; readonly scale: number; }
// fiat/stablecoin -> Decimal.js; cripto 18 dec -> bigint. NUNCA Number.
```

---

## 8. Relações

- **Asset Registry** (fonte de `scale`/metadados): [`./asset-registry.md`](./asset-registry.md),
  seed em [`./asset-registry.seed.csv`](./asset-registry.seed.csv).
- **Lint anti-float** (TX-2, 4 linguagens): [`../lint/no-float.md`](../lint/no-float.md).
- **Fio:** `adserver.money.v1.Money` em `proto/` (outro time).
- **Ledger v1:** Postgres 16 double-entry — `accounts` / `journal_entries` / `postings`,
  `sum(debit)=sum(credit)`, `NUMERIC` por ativo, particionamento temporal, idempotency key
  por captura (`docs/stack-tecnologico.md` §2.6).
