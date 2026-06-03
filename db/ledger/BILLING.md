# Billing — Modelo de Postings por Pricing Model

> **Normativo:** `docs/stack-tecnologico.md` §2.6, §4.9; `docs/documentacao-tecnica.md` §4.9 (CA-7);
> `contracts/money/money-type.md` (TX-2); `contracts/money/asset-registry.md` (DA-10).
>
> **Escopo deste documento:** como cada evento faturável se torna um par de postings no ledger.
> O JOB BATCH que lê StatsHourly/Iceberg e gera as postings é responsabilidade do data-platform (I3),
> rodando em paralelo com este módulo. Este documento entrega o modelo de dados e o mapeamento
> evento→postings; NÃO descreve o job de leitura do lakehouse.

---

## 1. Invariantes antes de tudo

1. **Float proibido (TX-2/CA-7):** todo `amount` é `NUMERIC(40,18)` em minor units inteiros.
   `scale` vem do Asset Registry, nunca hardcoded.
2. **Multi-moeda sem conversão automática (DA-10):** cada `asset_code` é um ledger isolado.
   Câmbio só como par de postings explícito com taxa registrada — nunca derivada em runtime.
3. **Toda movimentação = par de postings idempotente:** `sum(debit_amount) = sum(credit_amount)`
   por `journal_entry_id + asset_code`. Garantido pelo constraint trigger `postings_balance_chk_trg`.
4. **Nenhuma captura grava saldo direto:** saldo é sempre derivado de `postings` via view
   `ledger.account_balances`. A coluna `balance` não existe em `accounts`.
5. **Idempotency key por captura:** `journal_entries.idempotency_key` é `UNIQUE (tenant_id, key)`.
   Reprocessamentos at-least-once do job batch não criam duplicatas.
6. **Faturamento reconcilia contra lakehouse Iceberg, NUNCA contra streaming:**
   o número faturável é o batch horário (DA-7), lido do Iceberg após o job de consolidação.
   Números "ao vivo" (ClickHouse) são informativos; nunca são a base de um posting.
7. **Arredondamento:** quando necessário (ex.: CPM — divisão inteira de impressões),
   use `ROUND_HALF_EVEN` (banker's rounding) no nível da aplicação antes de inserir o posting.
   Restos de arredondamento viram posting explícito na conta `rounding:{asset_code}` — nunca
   ajuste silencioso. Postings em si são exatos em minor units; sem arredondamento interno ao ledger.
8. **Tráfego IVT excluído (TX-6/CA-7):** somente eventos marcados como válidos pelo pipeline
   de IVT (Fase 2) entram como faturáveis. O job batch filtra por `ivt_flag = false`.

---

## 2. Plano de contas (codes canônicos)

| Código (pattern)            | Kind      | Significado |
|-----------------------------|-----------|-------------|
| `adv:{advertiser_id}:{asset}` | liability | Saldo pré-pago do anunciante (crédito a consumir) |
| `platform:revenue:{asset}`  | revenue   | Receita da plataforma por veiculação |
| `platform:receivable:{asset}`| asset    | Contas a receber (Tenancy — fatura emitida, não paga) |
| `rounding:{asset}`          | expense   | Restos de arredondamento ROUND_HALF_EVEN (conta de dízima) |
| `tax:deducted:{asset}`      | liability | Retenções tributárias (ISS, IR, PIS/COFINS — Fase futura) |

`{asset}` = `asset_code` do Asset Registry (ex.: `BRL`, `USDC`).
Cada conta existe por ativo — ledgers isolados (DA-10).

---

## 3. Formato do idempotency_key por tipo

| Pricing model | Padrão da chave                                  | Exemplo |
|---------------|--------------------------------------------------|---------|
| CPM           | `cpm:{campaign_id}:{hour_bucket}:{asset_code}`  | `cpm:42:2026-06-03T14:00Z:BRL` |
| CPC           | `cpc:{click_event_id}`                           | `cpc:evt-abc-123` |
| CPA           | `cpa:{conversion_event_id}`                      | `cpa:evt-xyz-456` |
| Tenancy       | `tenancy:{campaign_id}:{period_start}`           | `tenancy:99:2026-06-01` |
| Câmbio        | `fx:{journal_entry_id_origem}:{pair}`            | `fx:5001:BRL-USDC` |
| Estorno       | `void:{original_entry_idempotency_key}`          | `void:cpc:evt-abc-123` |
| Arredondamento| `rounding:{base_idempotency_key}`                | `rounding:cpm:42:2026-06-03T14:00Z:BRL` |

---

## 4. Mapeamento evento → postings por pricing_model

### 4.1 CPM — Cost per Mille (a cada 1.000 impressões)

**Evento faturável:** 1.000 impressões válidas (pós-IVT) acumuladas em `StatsHourly` para
a combinação `(campaign_id, asset_code, hour_bucket)`.

**Quem produz:** job batch (I3, data-platform) que lê `StatsHourly` do Iceberg ao fechar
cada hora. O job agrupa por campanha, calcula `mille = floor(impressions / 1000)` e gera
uma `journal_entry` por múltiplo completo de 1.000.

**Fórmula:**
```
billing_amount = mille × rate_minor_units
```
onde `rate` vem de `config.campaigns.rate` (já em minor units do ativo, sem float).

**Par de postings:**
```
journal_entry:
  idempotency_key = 'cpm:{campaign_id}:{hour_bucket}:{asset_code}'
  description     = 'CPM billing — {mille}k impressões — campanha {campaign_id}'
  effective_at    = fim do hour_bucket
  ref_type        = 'campaign'
  ref_id          = campaign_id

postings:
  DÉBITO   adv:{advertiser_id}:{asset}   billing_amount   (consome saldo do anunciante)
  CRÉDITO  platform:revenue:{asset}      billing_amount   (gera receita da plataforma)

  -- Se houver resto de arredondamento (impressões não múltiplas de 1.000):
  -- O resto fica para o próximo bucket. Não há posting parcial de CPM incompleto.
  -- Se billing_amount tiver fração de minor unit (improvável, mas possível com
  -- taxas fracionárias), o resto vai para conta de arredondamento:
  DÉBITO   adv:{advertiser_id}:{asset}   remainder
  CRÉDITO  rounding:{asset}              remainder
```

**Nota de escala:** `mille × rate` nunca usa float. A multiplicação é inteira (BIGINT ou
NUMERIC sem casas). Qualquer fração de `rate` deve ser arredondada antes do posting usando
ROUND_HALF_EVEN ao `scale` do ativo.

---

### 4.2 CPC — Cost per Click (por clique validado)

**Evento faturável:** um único evento de clique (via `ck.php`, TX-1 envelope) marcado como
válido (não IVT, não bot, não self-click). Atribuído ao `campaign_id` + `banner_id`.

**Quem produz:** job batch lê eventos de clique validados do Iceberg. Cada evento gera exatamente
uma `journal_entry` (idempotência por `click_event_id`).

**Fórmula:**
```
billing_amount = rate_minor_units  (valor por clique, fixo da campanha)
```

**Par de postings:**
```
journal_entry:
  idempotency_key = 'cpc:{click_event_id}'
  description     = 'CPC billing — clique validado — campanha {campaign_id}'
  effective_at    = timestamp do clique (do evento TX-1)
  ref_type        = 'campaign'
  ref_id          = campaign_id

postings:
  DÉBITO   adv:{advertiser_id}:{asset}   rate_minor_units
  CRÉDITO  platform:revenue:{asset}      rate_minor_units
```

**Nota:** CPC não fatura impressões, apenas cliques validados. A validação ocorre no pipeline
de IVT (Fase 2), não no ledger.

---

### 4.3 CPA — Cost per Acquisition/Conversion (por conversão)

**Evento faturável:** evento de conversão (via `ct.php`, TX-1 envelope) atribuído ao
`campaign_id` por janela de lookback definida na campanha.

**Quem produz:** job batch lê eventos de conversão do Iceberg. Cada `conversion_event_id`
único gera uma `journal_entry`.

**Fórmula:**
```
billing_amount = rate_minor_units  (valor por conversão, fixo da campanha)
```

**Par de postings:**
```
journal_entry:
  idempotency_key = 'cpa:{conversion_event_id}'
  description     = 'CPA billing — conversao atribuida — campanha {campaign_id}'
  effective_at    = timestamp da conversão
  ref_type        = 'campaign'
  ref_id          = campaign_id

postings:
  DÉBITO   adv:{advertiser_id}:{asset}   rate_minor_units
  CRÉDITO  platform:revenue:{asset}      rate_minor_units
```

**Nota:** atribuição é last-click por padrão (v1). Multi-touch (Fase 2) exige splitting do
`rate_minor_units` entre os eventos do funil — cada split vira posting próprio com idempotency
derivado do `conversion_event_id + touch_id`.

---

### 4.4 Tenancy — tarifa fixa por período

**Evento faturável:** início de período (mês civil ou período contratado). Independe de volume
de impressões/cliques.

**Quem produz:** job batch ou job de faturamento periódico. Dispara no início de cada período
contratado. Gera uma `journal_entry` por período + campanha.

**Fórmula:**
```
billing_amount = rate_minor_units  (tarifa fixa do período)
```

**Par de postings — dois cenários:**

**Cenário A — Pré-pago (saldo existe em `adv:…`):**
```
journal_entry:
  idempotency_key = 'tenancy:{campaign_id}:{period_start}'
  description     = 'Tenancy billing — período {period_start}/{period_end} — campanha {campaign_id}'
  effective_at    = inicio do periodo (period_start)
  ref_type        = 'campaign'
  ref_id          = campaign_id

postings:
  DÉBITO   adv:{advertiser_id}:{asset}   rate_minor_units
  CRÉDITO  platform:revenue:{asset}      rate_minor_units
```

**Cenário B — Pós-pago / fatura (contas a receber):**
```
journal_entry:
  idempotency_key = 'tenancy:{campaign_id}:{period_start}'
  description     = 'Tenancy — emissao de fatura — periodo {period_start}'
  effective_at    = data de emissão da fatura

postings:
  DÉBITO   platform:receivable:{asset}   rate_minor_units   (contas a receber)
  CRÉDITO  platform:revenue:{asset}      rate_minor_units   (receita reconhecida)

-- Quando o pagamento é recebido (Stripe webhook / PIX / cripto):
journal_entry:
  idempotency_key = 'payment:{payment_id}'
  description     = 'Recebimento de pagamento — fatura tenancy {campaign_id}'

postings:
  DÉBITO   platform:cash:{asset}          rate_minor_units   (caixa recebido)
  CRÉDITO  platform:receivable:{asset}    rate_minor_units   (baixa do a receber)
```

**Nota:** Tenancy não usa `goal_metric`; o campo `rate` é a tarifa total do período.
`Pix Automático` (Asaas) e `Stripe Billing` são os mecanismos de cobrança — o ledger
apenas registra o fato contábil, não aciona o gateway.

---

## 5. Câmbio explícito (DA-10) — par de postings para troca entre ativos

Câmbio **nunca é automático**. Só ocorre como decisão humana/desk, registrada explicitamente.

```
journal_entry:
  idempotency_key = 'fx:{reference_id}:{asset_from}-{asset_to}'
  description     = 'Câmbio manual BRL -> USDC @ taxa {rate_applied} — aprovado por {approver}'
  metadata        = { "rate_applied": "5.123456", "approver": "desk-fulano", "rate_source": "manual" }

postings:
  DÉBITO   platform:cash:BRL              amount_brl_minor_units
  CRÉDITO  platform:cash:USDC             amount_usdc_minor_units

  -- O balanço SÃO DOIS PARES SEPARADOS POR ATIVO, não um único par cross-ativo.
  -- sum(debit:BRL) = sum(credit:BRL) dentro das postings BRL da entry.
  -- sum(debit:USDC) = sum(credit:USDC) dentro das postings USDC da entry.
  -- A taxa de câmbio está no metadata; o ledger não faz aritmética de conversão.
```

---

## 6. Estorno (void)

Postings nunca são editados ou deletados. Estorno = novo par de postings com sinais invertidos,
com `idempotency_key` prefixado por `void:`.

```
journal_entry:
  idempotency_key = 'void:{original_idempotency_key}'
  description     = 'Estorno de: {original_description}'
  ref_type        = 'journal_entry'
  ref_id          = original_entry_id

postings (inversos do original):
  DÉBITO   platform:revenue:{asset}      original_amount   (estorna receita)
  CRÉDITO  adv:{advertiser_id}:{asset}   original_amount   (devolve ao saldo do anunciante)
```

A `journal_entry` original não tem seu `status` alterado para `void` — isso seria uma escrita
destrutiva. Em vez disso, a entry de estorno referencia a original via `ref_id`. Relatórios
de reconciliação filtram `void:` para mostrar saldo líquido.

---

## 7. Reconciliação

**Periodicidade:** batch horário (alinhado ao `StatsHourly` do ClickHouse/Iceberg — DA-7).

**Fonte autoritativa:** Iceberg (lakehouse). Nunca streaming (ClickHouse "ao vivo").

**Processo:**
1. Job de reconciliação lê `StatsHourly` consolidado do Iceberg para o período.
2. Calcula o valor esperado de postings por campanha/asset/período.
3. Compara com `SUM(debit_amount)` nas postings correspondentes do ledger.
4. Divergência → abre exceção em tabela `ledger.reconciliation_exceptions` (schema futuro, I3).
5. Nunca autocorrige: correção exige posting de ajuste explícito aprovado por humano.

**Nota sobre dupla contagem (third-party tags):** banners `thirdparty_tag` disparam contagem
de impressão própria do anunciante além do pixel da plataforma (§4.3). A reconciliação deve
usar o número da plataforma como autoritativo; divergência com o tracker do anunciante é
documentada, não corrigida automaticamente.

---

## 8. Casos de borda

| Caso | Tratamento |
|------|------------|
| Campanha Tenancy com 0 impressões | Posting gerado normalmente — Tenancy é tarifa fixa, não por volume |
| CPA sem conversão no período | Nenhum posting — evento faturável não ocorreu |
| CPM < 1.000 impressões no período final | Sem posting CPM pelo período incompleto; acumulado vai para próximo bucket |
| Clique IVT (bot detectado) | Excluído pelo pipeline IVT antes do job batch; não entra no ledger |
| Anunciante sem saldo pré-pago | Job batch falha o posting com erro; não gera posting parcial; sistema aciona alerta de cobrança |
| Arredondamento em `rate × mille` | Posting de arredondamento em `rounding:{asset}`, ROUND_HALF_EVEN, explícito |
| AEV/BND (disabled) | Não são aceitos em novas `journal_entries` enquanto `enabled=false` no Asset Registry |
