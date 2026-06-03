# `contracts/`

Contratos de **Fase 0 (Fundações)** que cruzam todas as fronteiras do ad server. Aqui vivem
os contratos de **dinheiro** e a **política de lint anti-float** — invariantes que valem para
o sistema inteiro, antes de qualquer ML ou hot path.

## Conteúdo

| Caminho | O que é |
|---------|---------|
| [`money/money-type.md`](money/money-type.md) | Contrato canônico do tipo **`Money`** (`asset_code` + `amount` inteiro + `scale`) atravessando evento → ledger → BFF → UI. Representação por fronteira, invariantes, arredondamento e mapa de tipos por linguagem (TX-2/DA-10). |
| [`money/asset-registry.md`](money/asset-registry.md) | **Asset Registry** plugável: schema, DDL Postgres, seed e os campos em aberto de AEV/BND. Fonte autoritativa de `scale`. |
| [`money/asset-registry.seed.csv`](money/asset-registry.seed.csv) | Seed legível por máquina do Asset Registry (mesmas colunas/enums do DDL). |
| [`lint/no-float.md`](lint/no-float.md) | Política de CI que **proíbe `float` em código monetário** (Go, TS, Python, SQL), com snippets e job de CI. |

## Relação com `proto/`

O tipo `Money` no fio é `adserver.money.v1.Money` (`int64 amount` + `uint32 scale` +
`asset_code`), definido no **schema registry** em `proto/` (gerenciado por outro time, com
breaking-check Buf). Os contratos desta pasta **referenciam** esse schema e garantem a
coerência semântica dos três campos — **nunca editam `proto/`**.

## Relação com os docs normativos

Estes contratos **implementam** decisões já aprovadas em `docs/`:

- `docs/stack-tecnologico.md` — **TX-2** (Money: tipo canônico único; float proibido em CI),
  **DA-10** (armazenamento monetário agnóstico a moeda), **§2.6** (ledger Postgres
  double-entry + Asset Registry plugável), **§3** (10 perguntas em aberto de AEV/BND).
- `docs/documentacao-tecnica.md` — **§DA-10** (decimal de ponto fixo; nunca `float`).

## Princípio

> **Float proibido. Multi-moeda sem conversão automática.**
> Cada ativo é um ledger isolado; `scale` é autoritativo no Asset Registry e nunca inferido;
> câmbio só existe como par de postings explícito com taxa registrada por humano; toda
> movimentação é par de postings idempotente com `sum(debit) = sum(credit)`.
