# ml/features — Spec unica de featurizacao (anti-skew J1<->J2)

**Versao atual da spec:** `1.0.0`
**Objetivo do modelo:** `pCTR`
**ADR de referencia:** `docs/adr/0003-fase-2-sequenciamento-ml-copiloto.md` secoes B e D

---

## Por que este diretorio existe

O risco tecnico numero 1 da Fase 2 e o skew treino-serving: a funcao de featurizacao
atravessa duas linguagens — Python (treino, `ml/training`) e Go (serving in-process,
`internal/ranker`). Implementacoes paralelas sao a fonte classica de bug silencioso onde
`eCPM = pCTR_calibrado x rate` diverge entre o modelo treinado e o modelo servido,
causando erros financeiros.

Este diretorio contem o **contrato compartilhado** que J1 (ranker Go) e J2 (treino
Python) implementam contra a mesma definicao — garantindo que a mesma entrada produza
o mesmo vetor nas duas linguagens.

---

## Arvore de arquivos

```text
ml/features/
  spec/
    feature_spec.yaml        # FONTE DE VERDADE: spec declarativa versionada
  testdata/
    parity_cases.json        # Fixtures canonicos de entrada/saida esperada
  python/
    __init__.py
    featurize.py             # Implementacao Python (J2 consome este modulo)
    test_parity_cases.py     # Teste de paridade Python (pytest)
  go/
    parity_contract.go       # Documentacao da interface Go esperada (J1)
  README.md                  # Este arquivo
```

---

## A spec (`spec/feature_spec.yaml`)

O YAML define cada feature com:

| Campo | Significado |
| --- | --- |
| `name` | Nome canonico (snake_case) |
| `serving_index` | Posicao no vetor de float32 (0-based, imutavel dentro de uma versao MAJOR) |
| `type` | `int` ou `float` |
| `availability` | `online` (safe no hot path) ou `offline` (somente treino) |
| `source` | Campo do snapshot / evento / geo de onde o valor vem |
| `transformation` | Transformacao aplicada (bucketize, feature_hash, log1p, ratio, etc.) |
| `missing` | Valor padrao se a fonte for nula/ausente |
| `pii_free` | `true` — obrigatorio para todos os campos; gate do privacy-compliance-auditor |
| `pii_note` | Justificativa de ausencia de PII |
| `money_note` | Documento de conformidade TX-2 para features monetarias |

### Features online (23 features, indices 0..22)

| Index | Nome | Grupo | Fonte |
| --- | --- | --- | --- |
| 0 | zone_width_bucket | Zona | snapshot.Zone.Width |
| 1 | zone_height_bucket | Zona | snapshot.Zone.Height |
| 2 | zone_aspect_ratio_bucket | Zona | derived W/H |
| 3 | hour_of_day | Temporal | RequestTime UTC hour |
| 4 | day_of_week | Temporal | RequestTime Weekday |
| 5 | is_weekend | Temporal | derived day_of_week |
| 6 | geo_country_hash | Geo | commonv1.Geo.Country |
| 7 | geo_city_hash | Geo | commonv1.Geo.City |
| 8 | device_class_hash | Device | useragent.Classify() |
| 9 | is_bot | Device | device_class == "bot" |
| 10 | candidate_tier | Candidato | cascade.Candidate.Tier |
| 11 | campaign_priority | Candidato | snapshot.Campaign.Priority |
| 12 | pacing_deficit | Pacing | cascade.Candidate.PacingDeficit |
| 13 | pacing_deficit_bucket | Pacing | derived pacing_deficit |
| 14 | ecpm_minor_units_log1p | eCPM | ECPMMinorUnits (int64) via log1p |
| 15 | ecpm_tier_bucket | eCPM | derived log1p(ecpm) |
| 16 | banner_width_bucket | Banner | snapshot.Banner.Width |
| 17 | banner_height_bucket | Banner | snapshot.Banner.Height |
| 18 | banner_size_match | Banner | derived zone/banner match |
| 19 | creative_type | Banner | snapshot.Banner type |
| 20 | candidate_count_log1p | Competicao | Decision.candidate_count via log1p |
| 21 | campaign_ctr_estimate | Historico | DeliveredClicks/DeliveredImpressions |
| 22 | campaign_delivery_ratio | Historico | DeliveredImpressions/GoalImpressions |

### Features offline-only (somente treino, NAO entram no vetor de serving)

| Nome | Motivo de exclusao do serving |
| --- | --- |
| label_click | Ocorre apos a decisao; disponivel apenas no Iceberg via JOIN |
| propensity_logged | Campo do registro de decisao passado; nao e contexto futuro |
| model_version_at_decision | Metadado de auditoria, nao feature de contexto |
| feature_spec_version_at_decision | Metadado de auditoria |
| raw_site_url | Alta cardinalidade, nao esta no snapshot; exigiria lookup extra (viola TX-4) |
| site_id_hash | Nao exposto diretamente no hot path (avaliar promover em versao MINOR futura) |
| custom_vars_json | Esquema variavel por publisher; exigiria transmissao do mapa completo (viola TX-4) |

---

## Hash canonico — ponto critico de paridade

O hashing de categoricos de alta cardinalidade usa **MurmurHash3 (32-bit) com seed fixo
por feature**. A implementacao DEVE ser identica nas duas linguagens:

```python
# Python (mmh3)
mmh3.hash(value: str, seed: int, signed=False) % num_buckets

# Go (github.com/twmb/murmur3)
# murmur3.SeedSum32(uint32(seed), []byte(value)) % uint32(num_buckets)
```

Seeds por feature (definidos na spec, nao alterar sem versao MAJOR):

| Feature | Seed | num_buckets |
| --- | --- | --- |
| geo_country_hash | 42 | 512 |
| geo_city_hash | 43 | 2048 |
| device_class_hash | 44 | 16 |

---

## Dinheiro e privacidade

**TX-2 (sem float monetario):** `ecpm_minor_units` entra na featurizacao como `int64`.
A transformacao `log1p` produz `float32` apenas para o vetor de ML. O vencedor financeiro
e sempre determinado via `money.CompareECPM` (int64, DA-10). Nenhuma feature monetaria e
armazenada ou comparada como float.

**TX-5/DA-11 (sem PII):** O IP bruto e descartado pelo `geo.Resolver` antes de chegar ao
ranker. O vetor de features contem apenas `country` (ISO) e `city` (nome) derivados.
Nenhuma feature contem ou deriva de IP, cookie cross-site, ou identificador persistente
de usuario. Gate do `privacy-compliance-auditor` obrigatorio antes do merge de J2/J5.

---

## Politica de versao da spec

**Tolerancia de paridade:** `float_abs = 1e-6`. O vetor e float32 (ONNX/Treelite); o
ruido de cast float32->float64 e ~1.19e-7. A tolerancia 1e-6 captura bugs reais (log1p
errado, seed errado, bucket trocado) sem falso-positivo de arredondamento. Inteiros:
igualdade exata.

| Tipo de mudanca | Bump | Impacto em modelos promovidos |
| --- | --- | --- |
| Correcao de documentacao, ajuste de bucket sem alterar fronteiras | PATCH (x.y.z+1) | Modelos existentes continuam validos |
| Adicao de features novas ao final do vetor (append-only) | MINOR (x.y+1.0) | Re-treino obrigatorio antes da proxima promocao |
| Remocao, renomeacao ou reordenacao de features | MAJOR (x+1.0.0) | Re-treino + nova campanha A/B (J4) obrigatorios |

O campo `feature_spec_version` e gravado junto com `model_version` no `Decision` e no
Iceberg. J3/J4 detectam desfasamento comparando o campo do registro com a versao da spec
que treinou o modelo.

---

## Como rodar o teste de paridade Python

```bash
cd ml/features/python
pip install mmh3 numpy pytest
pytest test_parity_cases.py -v
```

Saida esperada: todos os testes verdes. Qualquer falha indica divergencia entre a
implementacao Python e os fixtures — investigar antes de prosseguir com J2.

---

## Como J1 (Go) pluga no teste de paridade

1. Implementar `internal/ranker/featurize.go` com a assinatura documentada em
   `ml/features/go/parity_contract.go`.
2. Criar `internal/ranker/parity_test.go` que:
   - Le `../../ml/features/testdata/parity_cases.json`
   - Verifica `FeatureSpecVersion == fixtures.feature_spec_version`
   - Para cada caso: chama `Featurize(input)` e compara com `expected_vector_computed`
     usando tolerancia `1e-6` para floats, igualdade exata para ints.
3. Adicionar `github.com/twmb/murmur3` ao `go.mod`.
4. O teste de paridade Go e **gate de merge de J1** — nao pode ser pulado.

---

## Bootstrap de fixtures de hash

Os valores numericos concretos em `parity_cases.json` foram calculados pelo bootstrap
Python em 2026-06-04. Para regenerar apos uma mudanca de spec:

```bash
cd ml/features/python
python test_parity_cases.py   # modo bootstrap: imprime vetores calculados
```

Copiar os valores impressos de volta para os campos `expected_vector_computed`
em `testdata/parity_cases.json`. Apos esse passo, rodar `pytest` para confirmar
que os testes passam. Commitar o JSON — os valores se tornam o gold standard
compartilhado entre Go e Python.

---

## Gate de promocao de modelo (J4)

Nenhuma versao de modelo e promovida em producao sem:

1. Teste de paridade Python verde (`pytest test_parity_cases.py`).
2. Teste de paridade Go verde (`go test ./internal/ranker/... -run TestParityFromFixtures`).
3. `feature_spec_version` no modelo = versao atual da spec.
4. Calibracao isotonica monitorada (ECE aceitavel — `ml/calibration/`).
5. Prova de uplift A/B com guarda de receita e kill-switch (J4 — `ml-optimization-engineer`).
