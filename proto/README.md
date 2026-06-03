# Schema Registry Protobuf — AdServer (Fase 0 / TX-1)

Contrato de eventos **unico** do ad server. Protobuf-first, gerenciado com
[Buf]. Esta arvore (`proto/`) e a fonte de verdade dos eventos que cruzam
collectors, delivery-edge, agregacao (StatsHourly) e front.

## Por que existe (TX-1)

Um unico contrato versionado elimina drift entre produtores e consumidores
e habilita o loop de atribuicao do ML. A CI roda checagem de
**breaking-change** com **compatibilidade BACKWARD obrigatoria**: consumidor
antigo sempre consegue ler mensagem nova. Avro foi descartado.

Regras de evolucao compativel:

- NUNCA reutilize numeros de campo; ao remover, mova para `reserved`.
- NUNCA mude o tipo de um campo existente.
- Adicionar campos com numeros novos e sempre permitido.

## O envelope universal

Todo evento embute `adserver.common.v1.Envelope` como **campo 1**. Quatro
campos sao criticos:

| Campo           | Papel                                                                 |
| --------------- | --------------------------------------------------------------------- |
| `tenant_id`     | Particiona dados e isola ledgers; pseudonimo, sem PII.                |
| `event_id`      | Chave de **dedupe/idempotencia** (ULID/UUID).                         |
| `decision_id`   | Liga o evento a decisao de veiculacao; **fecha o loop de atribuicao**.|
| `model_version` | Versao do ranker (vazio quando a decisao veio da cascata pura).       |

`decision_id` + `model_version` viabilizam treino de pCVR e avaliacao
off-policy. **Sem isso, nao ha ML.**

## Privacidade (TX-5 / DA-11)

Nenhum evento carrega **IP bruto** nem **PII**. O collector deriva geo
(pais/cidade) via GeoLite2 e **descarta o IP**. Nao ha perfil persistente;
chaves de capping sao efemeras/hasheadas e nao aparecem no contrato.
`custom_vars` no `AdRequest` e um mapa **opaco** de first-party data do
publisher (DA-11).

## Pacotes e mensagens

- **`adserver.common.v1`** (`adserver/common/v1/envelope.proto`)
  - `Envelope`, `Geo`
  - enum `ServedTier` (cascata DA-3: `UNSPECIFIED`, `OVERRIDE`, `CONTRACT`,
    `REMNANT`, `BLANK`)
- **`adserver.money.v1`** (`adserver/money/v1/money.proto`)
  - `Money` (`asset_code`, `int64 amount`, `uint32 scale`) — TX-2: float
    proibido, sem conversao cambial automatica (DA-10).
- **`adserver.telemetry.v1`** (`adserver/telemetry/v1/events.proto`)
  - `AdRequest` (asyncjs.php), `Impression` (lg.php), `Click` (ck.php -> 302),
    `Conversion` (ct.php) — DA-8.

## Comandos (rode a partir de `proto/`)

```bash
# A partir da RAIZ do repositorio (o .git fica na raiz, e proto/ e um subdir):

# Lint do contrato (STANDARD + COMMENTS)
buf lint proto

# Formatacao canonica (-w reescreve; --diff so mostra)
buf format -w proto

# Checagem de breaking-change contra a main (compat BACKWARD — TX-1)
buf breaking proto --against '.git#branch=main,subdir=proto'

# Gerar codigo (Go no hot path + TS para o front) — requer rede p/ plugins remotos
cd proto && buf generate
```

> Atalho: `make proto-lint` / `make proto-breaking` / `make proto-gen` na raiz
> (ver `Makefile`). A CI roda o equivalente em `.github/workflows/buf.yml`.

A geracao (`buf.gen.yaml`) produz Go em `gen/go` (paths=source_relative) e
TypeScript em `gen/ts`.

[Buf]: https://buf.build
