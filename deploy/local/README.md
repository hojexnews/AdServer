# deploy/local — ambiente de integração local (I5)

Contraparte **local** de [`platform/`](../../platform/) (que mira EKS). Sobe as
dependências que o trabalho de cutover da Fase 1 precisa — Postgres, Redis,
Redpanda, ClickHouse — mais os dois serviços Go do hot path, **sem cloud**.

## Caminhos

| Caminho | Como | Estado |
|---|---|---|
| **Postgres local** (sem Docker) | `make dev-db-setup` → `make dev-it` / `make dev-decision-run` → `make dev-smoke` | **verificado** contra Postgres 16 local |
| **Docker Compose** | `make dev-up` (core) / `make dev-up-streaming` (+ streaming) | scaffold validado estaticamente; requer Docker engine |

> O stack Compose foi autorado como código e validado estaticamente
> (`make dev-validate`: YAML + shell). Boote-o localmente antes de confiar nele —
> o ambiente de autoria não tinha Docker. A camada de streaming (ClickHouse Kafka
> engine) é a mais sensível: o init renderiza o DDL para single-node (remove
> `ON CLUSTER`, substitui o broker, ignora `COMMENT ON`).

## Componentes

- `docker-compose.yml` — perfis `default` (core) e `streaming`.
- `Dockerfile` — build multi-stage distroless de um serviço Go (`--build-arg SERVICE=decision|collector`); context = raiz do repo (gen/ versionado → build hermético).
- `postgres/10-init.sh` — aplica migrations (`asset_registry`→`config`→`ledger`) + `db/seed/dev_roles.sql` + `db/seed/dev_seed.sql` + `db/seed/hojex_news_seed.sql` (zonas reais Hojex News 1001–1004).
- `redpanda/topics.sh` — cria os 5 tópicos (partições espelham `data/redpanda/topics.yaml`; RF=1 em dev).
- `clickhouse/10-ddl.sh` — renderiza e aplica `data/clickhouse/migrations/*.sql` em single-node.
- `smoke.sh` — E2E: o decision serve anúncio real do seed (`make dev-smoke`).

## Segredos

Os valores aqui são **defaults de DEV** (`*_dev_only`, `dev-*-change-me`). Em
produção, segredos vêm do OpenBao ([platform/secrets/openbao](../../platform/secrets/openbao)).

## Papéis Postgres (TX-3)

- **`adserver_loader`** — read-only, **BYPASSRLS**. Usado SÓ pelo loader de
  snapshot do decision engine (leitura cross-tenant para o snapshot global).
- **`adserver_app`** — read/write, **RLS imposta**. Usado pelo BFF/console
  (cada sessão faz `SET LOCAL adserver.tenant_id`).
