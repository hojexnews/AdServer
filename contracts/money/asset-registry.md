# Asset Registry (TX-2 / §2.6)

> **Status:** Fase 0 (Fundações) — bloqueante.
> **Normativo:** `docs/stack-tecnologico.md` §TX-2, §2.6, §3; §DA-10.
> **Relacionado:** [`./money-type.md`](./money-type.md) · seed: [`./asset-registry.seed.csv`](./asset-registry.seed.csv)

## 1. Propósito

O Asset Registry é a **fonte autoritativa** de `scale` e dos metadados de cada ativo.
**Sem ele não há aritmética monetária correta**: o `scale` de `Money` é lido daqui, nunca
inferido (TX-2). É **plugável** — novos ativos (incluindo AEV/BND) entram como **linhas**,
sem migração de schema (`docs/stack-tecnologico.md` §2.6, §4 Fase 3).

Cada ativo é, contabilmente, um **ledger isolado** (DA-10): não há conversão automática
entre ativos. Câmbio só existe como par de postings explícito com taxa registrada por humano.

---

## 2. Schema

| Coluna              | Tipo                | Nulo? | Descrição |
|---------------------|---------------------|-------|-----------|
| `code`              | `TEXT` (PK)         | não   | Código do ativo (ex.: `BRL`, `USDC`). Casa com `Money.asset_code`. |
| `name`              | `TEXT`              | não   | Nome legível. |
| `scale`             | `SMALLINT`          | não*  | Casas decimais (`>= 0`). **Autoritativo.** `NULL` só enquanto **TBD** (AEV/BND). |
| `kind`              | `TEXT` (enum)       | não   | `fiat` \| `stablecoin` \| `crypto` \| `token`. |
| `chain_id`          | `BIGINT`            | sim   | EVM chain id (ex.: 1 = Ethereum). `NULL` para fiat e para chain própria não-EVM/TBD. |
| `contract_address`  | `TEXT`              | sim   | Endereço do contrato on-chain. `NULL` para fiat/off-chain/TBD. |
| `custody_mode`      | `TEXT` (enum)       | não   | `none` \| `safe_multisig` \| `mpc_fireblocks` \| `client_supply`. |
| `price_source`      | `TEXT` (enum)       | não   | `none` \| `administered` \| `oracle_chainlink` \| `oracle_pyth`. |
| `price_governance`  | `TEXT`              | sim   | **Quem** define o preço administrado (papel/comitê). Obrigatório quando `price_source = administered` — mitiga conflito de interesse (§2.6, §3 q.4). |
| `enabled`           | `BOOLEAN`           | não   | Ativo habilitado para uso operacional. |
| `created_at`        | `TIMESTAMPTZ`       | não   | Criação do registro. |
| `updated_at`        | `TIMESTAMPTZ`       | não   | Última atualização. |

\* `scale` é `NULL` **apenas** enquanto o ativo está `enabled = false` (caso AEV/BND-TBD).
Um ativo **não pode** ser habilitado sem `scale` definido — ver CHECK no DDL.

---

## 3. DDL Postgres

```sql
-- Asset Registry (TX-2 / §2.6). Fonte autoritativa de scale + metadados.
-- float PROIBIDO: valores monetários são NUMERIC nas tabelas de ledger;
-- aqui só vivem scale e metadados.
CREATE TABLE asset_registry (
    code              TEXT        PRIMARY KEY,
    name              TEXT        NOT NULL,
    scale             SMALLINT,                 -- NULL só enquanto TBD (ver CHECK)
    kind              TEXT        NOT NULL,
    chain_id          BIGINT,
    contract_address  TEXT,
    custody_mode      TEXT        NOT NULL DEFAULT 'none',
    price_source      TEXT        NOT NULL DEFAULT 'none',
    price_governance  TEXT,
    enabled           BOOLEAN     NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT asset_registry_kind_chk
        CHECK (kind IN ('fiat', 'stablecoin', 'crypto', 'token')),
    CONSTRAINT asset_registry_custody_chk
        CHECK (custody_mode IN ('none', 'safe_multisig', 'mpc_fireblocks', 'client_supply')),
    CONSTRAINT asset_registry_price_source_chk
        CHECK (price_source IN ('none', 'administered', 'oracle_chainlink', 'oracle_pyth')),
    CONSTRAINT asset_registry_scale_chk
        CHECK (scale IS NULL OR scale >= 0),
    -- ativo habilitado EXIGE scale definido (sem scale não há aritmética correta)
    CONSTRAINT asset_registry_enabled_needs_scale_chk
        CHECK (enabled = false OR scale IS NOT NULL),
    -- preço administrado EXIGE governança explícita (mitiga conflito de interesse)
    CONSTRAINT asset_registry_administered_needs_governance_chk
        CHECK (price_source <> 'administered' OR price_governance IS NOT NULL)
);

COMMENT ON TABLE  asset_registry        IS 'Fonte autoritativa de scale e metadados por ativo (TX-2/§2.6).';
COMMENT ON COLUMN asset_registry.scale  IS 'Casas decimais autoritativas; NUNCA inferido pelo Money.';
```

---

## 4. Seed inicial

Fonte de máquina: [`./asset-registry.seed.csv`](./asset-registry.seed.csv) (mesma ordem de
colunas do DDL, exceto `created_at`/`updated_at`, preenchidos por `DEFAULT now()`).

| code   | name              | scale   | kind        | chain_id | contract_address | custody_mode  | price_source | price_governance        | enabled |
|--------|-------------------|---------|-------------|----------|------------------|---------------|--------------|-------------------------|---------|
| BRL    | Real Brasileiro   | 2       | fiat        | —        | —                | none          | none         | —                       | true    |
| USD    | US Dollar         | 2       | fiat        | —        | —                | none          | none         | —                       | true    |
| EUR    | Euro              | 2       | fiat        | —        | —                | none          | none         | —                       | true    |
| USDC   | USD Coin          | 6       | stablecoin  | 1        | 0xA0b8…eB48      | safe_multisig | none         | —                       | true    |
| USDT   | Tether USD        | 6       | stablecoin  | 1        | 0xdAC1…1ec7      | safe_multisig | none         | —                       | true    |
| ERC20  | ERC-20 genérico   | 18      | crypto      | 1        | (por instância)  | safe_multisig | none         | —                       | true    |
| **AEV**| **Aevum**         | **TBD** | token       | TBD      | TBD              | client_supply | administered | _Comitê de Tesouraria_  | **false** |
| **BND**| **Bond (Aevum)**  | **TBD** | token       | TBD      | TBD              | client_supply | administered | _Comitê de Tesouraria_  | **false** |

> O `contract_address` real de USDC/USDT por chain é definido na migração de dados; os
> prefixos acima são apenas ilustrativos da forma. `ERC20` é um **gabarito** de classe; cada
> token concreto vira sua própria linha com seu `contract_address`.

---

## 5. AEV / BND — campos em aberto (NÃO bloqueiam Fase 0/1)

AEV e BND entram como **linhas** no Registry (`enabled = false`, `scale = NULL`,
`price_source = administered`). **Não inventamos a spec** (`docs/stack-tecnologico.md` §3):
pagamentos ficam 100% **fora do hot path**; apenas budgets pré-computados influenciam pacing.
Portanto **AEV/BND bloqueiam só a Fase 3** (cripto/tokens), nunca a Fase 0/1.

Campos a preencher quando as respostas chegarem — mapeados às **10 perguntas da §3** do stack doc:

| Campo do Registry           | Pergunta §3 | O que falta decidir |
|-----------------------------|-------------|---------------------|
| `chain_id`, `contract_address` | §3 q.1  | **On-chain ou off-chain?** EVM (ERC-20-like) vs. chain própria não-EVM → `viem`/Fireblocks vs. `ChainConnector` nativo. |
| **`scale`**                 | §3 q.2      | **`scale`/decimals de cada token** — o dado **mais crítico**; sem ele não há aritmética correta no `Money`/ledger. |
| `kind`                      | §3 q.3      | **Classificação regulatória** (pagamento/utility/stablecoin/security) → MiCA/BACEN/CVM, KYC/KYB, custódia, contabilidade. Hoje provisório como `token`. |
| `price_source`, `price_governance` | §3 q.4 | **Como o preço é determinado** (feed/oráculo vs. administrado). Se administrado, **qual a governança**. v1: `administered` + governança explícita. |
| `custody_mode`              | §3 q.5      | **Custódia**: self-custody (Safe/multisig), MPC (Fireblocks), ou cliente controla o supply (`client_supply`). Quem detém as chaves. |
| _(operacional, fora da tabela)_ | §3 q.6  | **Liquidez / on-off ramp**: há desk/exchange AEV/BND ↔ fiat/stablecoin? Sem ramp, token vira **crédito fechado**. |
| _(modelagem de ledger)_     | §3 q.7      | **Mecânica de contrato** (rebasing, taxa de transferência, pause, blocklist, upgradability). **BND = "Bond"** ⇒ pode exigir **accrual/maturidade/cupom** no ledger. |
| _(fluxo de confirmação)_    | §3 q.8      | **Finalidade/confirmações por chain** e comportamento de **reorg** (`pending` → definitivo). |
| _(fluxo de uso)_            | §3 q.9      | **Finalidade**: pagar campanhas (entrada), payout a publishers (saída), ou ambos; anunciante assina on-chain vs. plataforma custodia. |
| _(compliance)_              | §3 q.10     | **Travel Rule / screening / KYC** específicos para AEV/BND e jurisdições (Brasil/BACEN + global). |

> **BND** carrega a hipótese de "Bond": se confirmado rendimento/maturidade/cupom (§3 q.7),
> o ledger precisa modelar **accruals** — outra razão para `scale`/classificação serem
> resolvidos antes de habilitar.
