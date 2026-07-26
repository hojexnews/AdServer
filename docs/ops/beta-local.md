# Perfil BETA local — um comando pra subir, um pra checar

> Ver [ADR-0005](../adr/0005-perfil-beta-dependencia-unica-postgres.md) para a decisão
> completa (contexto, alternativas, invariantes de merge). Este documento é o guia de
> uso do `make/beta.mk`, não repete a decisão.

## 1. Objetivo

Tornar o AdServer testável ponta-a-ponta **nesta máquina, sem Docker**, com o mínimo de
cerimônia: `make beta-up` sobe o hot path, `make beta-check` prova que ele funciona,
`make beta-down` derruba, `make beta-status`/`make beta-logs` diagnosticam. Nenhum passo
manual de exportar variável de ambiente é necessário para o hot path (decision +
collector) — o Makefile resolve o contrato de env sozinho.

## 2. Escopo — o que é e o que NÃO é

**É:**

- `decision` (`:8080`) + `collector` (`:8081`) rodando nativamente (binário Go
  compilado, sem `go run`, sem container), contra um Postgres 16 local já em execução.
- Frequency capping **de verdade** (não `NoOpCapper`): `CAPPING_BACKEND=memory` ativa um
  capper em memória de processo que reusa a mesma lógica de `internal/capping.Capper`
  (chave salgada, TTL, fail-safe) provada contra Redis.
- Persistência de telemetria em Postgres via `TELEMETRY_PG_DSN` (schema `stats`, tabela
  `stats.events_raw`) em vez de Redpanda/ClickHouse — dono: engenheiro do collector /
  data-platform. **`make beta-check` é o gate objetivo de "isso está de pé"**: se o
  schema ainda não existir no seu checkout, ele falha nomeando exatamente essa etapa
  (ver §5).
- Uma única dependência externa: Postgres. Nada de Redis, ClickHouse, Redpanda.

**NÃO é:**

1. **Fonte de faturamento.** DA-7 continua intacto: faturável reconcilia contra o
   lakehouse Iceberg, nunca contra este sink Postgres. `queryConsolidated` do BFF
   continua recusando (`PRECONDITION_FAILED`) neste perfil — não existe hoje, neste
   repo, nenhuma fonte "Consolidado ≤1h / faturável" fora de produção real. O console
   mostra **só a série "Ao vivo"** quando `BFF_PG_DSN` aponta para o banco de beta (ver
   §6) — e mesmo essa série tem lacunas conhecidas e documentadas em
   `bff/src/adapters/postgres-stats.ts` (sem contagem de `requests` pré-impressão, sem
   custo real — `totalCost` sai como placeholder `"0.00"`, nunca um valor calculado).
2. **Um substituto para `deploy/local/docker-compose.yml`.** O caminho Compose (Redis +
   Redpanda + ClickHouse + serviços) continua existindo e intocado para quem quiser
   exercitar o pipeline real de produção.
3. **Multi-réplica.** O capper em memória é single-process: não é compartilhado entre
   réplicas do `decision` e se perde a cada restart. Válido só para um nó.
4. **Geo funcional por padrão.** Sem `GEOIP_DB_PATH` apontando para um `.mmdb` real, o
   resolver de geo é `EmptyResolver` (log: `"geo: MaxMind dbPath is empty; falling back
   to EmptyResolver (DA-9)"`) e regra de entrega por país **não dispara**. Mais: mesmo
   com um `.mmdb` real, o caminho servido de produção (`POST /v1/decide` chamado
   diretamente pelo navegador via `/asyncjs`) **nunca preenche** `geo_country`/`geo_city`
   a partir do IP — isso é uma lacuna de wiring do próprio `services/decision`
   (documentada no cabeçalho de `DecideRequest.GeoCountry` em
   `services/decision/cmd/decision/main.go`), não uma limitação do perfil beta. A única
   forma de exercitar uma regra por país hoje é enviar `geo_country` explícito no corpo
   de `/v1/decide` — é exatamente como `deploy/local/smoke.sh` (reusado por
   `make beta-check`) exercita `SERVED_TIER_CONTRACT` para BR.
5. **Gestor do BFF/console.** `make beta-up`/`beta-down` NUNCA tocam nas portas `:3001`
   (BFF) / `:3005` (console) — ver §6.

## 3. Como subir (comando único)

```
make beta-up      # provisiona o banco (drop+recreate) + compila + sobe decision+collector, espera /healthz
make beta-check    # smoke E2E: decide → pixel → clique → evento em stats.events_raw
make beta-status   # o que está no ar, em que porta, com que backend
make beta-logs     # tail dos logs (FOLLOW=1 para -f)
make beta-down     # derruba só o que beta-up subiu (por PID file)
```

Nenhuma variável de ambiente precisa ser exportada manualmente — `BETA_DB` (default
`adserver_beta`) e os segredos de dev (`BETA_CAPPING_SALT`, `BETA_CK_HMAC_SECRET`) têm
default embutido no fragmento, overridáveis via `make beta-up BETA_DB=outro_banco` se
precisar isolar de outra sessão que já esteja usando `adserver_beta`.

`beta-up` é idempotente: chama `beta-down` sozinho no início (para não tentar recriar o
banco com o processo antigo ainda conectado) e reusa `dev-db-setup` (`make/dev.mk`) sem
duplicar a lógica de provisionamento — mesmo schema, mesmos seeds (`db/seed/dev_seed.sql`
+ `db/seed/hojex_news_seed.sql`, incluindo as 4 zonas reais 1001–1004 do Hojex News).

## 4. Interfaces (portas, env, tabelas)

| Serviço | Porta | Binário | Log | PID file |
|---|---|---|---|---|
| decision | `:8080` | `.beta/bin/decision` | `.beta/decision.log` | `.beta/decision.pid` |
| collector | `:8081` | `.beta/bin/collector` | `.beta/collector.log` | `.beta/collector.pid` |

Env que `beta-up` resolve e injeta (ver `.beta/decision.env` / `.beta/collector.env`
para o snapshot exato da última subida — `beta-status` já mostra isso):

- `DATABASE_URL` / `TELEMETRY_PG_DSN` →
  `postgres://adserver_loader:loader_dev_only@localhost:5432/$(BETA_DB)?sslmode=disable`
  (papel BYPASSRLS, mesmo formato de `make/dev.mk`).
- `CAPPING_BACKEND=memory` — sem `REDIS_ADDR` (nunca setado pelo perfil beta).
- `CAPPING_SALT` / `CK_HMAC_SECRET` — valores de dev fixos (nunca reais; produção usa
  OpenBao).
- `SNAPSHOT_REFRESH_INTERVAL=10s` — mais agressivo que o default de produção (30s), para
  uma edição feita no console aparecer rápido no `decision` durante debug.
- `REDPANDA_BROKERS` / `GEOIP_DB_PATH` — deliberadamente **não setados** (ver §2.4).

Tabelas envolvidas no banco `$(BETA_DB)`:

- `config.*` — CRUD de anunciantes/campanhas/banners/sites/zonas (schema compartilhado
  entre `decision` via `DATABASE_URL` e BFF via `BFF_PG_DSN`, ver §6).
- `stats.events_raw` — eventos brutos gravados pelo collector via `TELEMETRY_PG_DSN`
  (contrato fixo; schema é posse do data-platform-engineer).
- `stats.live_kpis` — view agregada consumida pelo BFF (`PostgresStatsAdapter.queryLive`,
  `bff/src/adapters/postgres-stats.ts`) para a série "Ao vivo" do console.

## 5. Critérios de aceitação (o que `beta-check` prova)

`make beta-check` executa, nesta ordem, e para no primeiro passo que falhar dizendo
exatamente qual foi:

1. **Pré-condição** — `decision`/`collector` respondem `/healthz` (senão: "rode
   `make beta-up` primeiro").
2. **decide** — reusa `deploy/local/smoke.sh` (não reescrito): `POST /v1/decide` com
   `geo_country=BR` retorna `SERVED_TIER_CONTRACT` do seed; `geo_country=US` retorna
   `SERVED_TIER_REMNANT`.
3. **pixel (`/lg`)** — uma segunda chamada a `/v1/decide` (BR) extrai `click_tok` e
   `banner_id`; `GET /lg?...` precisa responder `HTTP 200` (`image/gif`).
4. **clique (`/ck`)** — `GET /ck?tok=...` com o token HMAC assinado pelo `decision`
   precisa responder `HTTP 302` (valida a cadeia completa de assinatura entre os dois
   processos — se `CK_HMAC_SECRET` divergir entre eles, esta etapa falha com 400).
5. **persistência (`stats.events_raw`)** — confirma via `psql` que a tabela existe
   (`to_regclass`) e que o número de linhas aumentou após os passos 3–4. Se o schema
   ainda não existir neste checkout, falha **nesta etapa nomeada**, não silenciosamente
   nem numa etapa anterior.

Ao final, `beta-check` imprime **uma linha só** com o resultado agregado, pensada para
colar num relatório, por exemplo:

```
RESULTADO beta-check: decide=PASS pixel(/lg)=PASS clique(/ck)=PASS evento-em-stats.events_raw=PASS(3 linhas)
```

ou, com o schema ainda pendente:

```
RESULTADO beta-check: decide=PASS pixel(/lg)=PASS clique(/ck)=PASS evento-em-stats.events_raw=PENDENTE(schema stats.events_raw ainda nao existe em adserver_beta — dono: collector/telemetry, contrato TELEMETRY_PG_DSN)
```

## 6. Console do anunciante (manual)

O anunciante não olha para `:8080`/`:8081` — ele usa o **console** (`web/console/`, Next.js)
por trás do **BFF** (`bff/`). Isto é **deliberadamente manual, fora de `beta-up`**: as
portas padrão `:3001` (BFF) e `:3005` (console) já estão em uso por outra sessão humana
nesta máquina, e automatizar o start/stop delas aqui derrubaria o trabalho de outra
pessoa. Se um dia alguém for "consertar" isso automatizando — não: suba em outra porta
(`PORT=`), nunca mate um processo que você não subiu.

Confirme antes se as portas de exemplo abaixo (`3011`/`3015`) estão livres
(`ss -ltn | grep -E ':(3011|3015)\b'`); se não estiverem, escolha outras.

**BFF**, apontando para o Postgres do beta:

```
cd bff && BFF_PG_DSN="postgres://adserver_loader:loader_dev_only@localhost:5432/adserver_beta?sslmode=disable" \
  ALLOWED_ORIGINS="http://localhost:3015" PORT=3011 npx tsx src/index.ts
```

- `BFF_PG_DSN` ativa `PostgresConfigAdapter` (CRUD real) + `PostgresPaymentsAdapter` +
  `PostgresStatsAdapter` (`queryLive` real, ver §2). Sem ele, tudo cai em stub
  in-memory/erro fail-closed — nenhum dado do beta apareceria.
- `ALLOWED_ORIGINS` precisa ser a origem do **console** (o browser bate no console, que
  faz proxy para o BFF preservando o header `Origin` — ver
  `web/console/src/app/api/trpc/[trpc]/route.ts`), não a porta do próprio BFF.
- Use `npx tsx` (sem `watch`) — `tsx watch` pode esgotar
  `fs.inotify.max_user_instances` se outros processos com watcher já estiverem rodando
  nesta máquina (achado prévio, `console-admin-dev-run`); `cat
  /proc/sys/fs/inotify/max_user_instances` para conferir o limite atual.

**Console**, apontando para esse BFF:

```
cd web/console && SESSION_COOKIE_NAME="betasess" ALLOWED_ORIGINS="http://localhost:3015" \
  BFF_INTERNAL_URL="http://localhost:3011" npm run dev -- -p 3015
```

- `SESSION_COOKIE_NAME` != `__Host-sess` (o default exige `Secure`/HTTPS) — qualquer
  nome sem o prefixo `__Host-` funciona em HTTP local.
- `NODE_ENV` não precisa ser setado manualmente: `next dev` já o define como
  `development`. **Medido nesta sessão** (`web/console/src/lib/session-guard.ts`,
  `sessionConfigError`): fora de produção, `SESSION_SECRET` é dispensável — o middleware
  aceita um token de sessão **sem assinatura** (`verifySessionToken`, dev-stub). Em
  produção (`NODE_ENV=production`) essa mesma ausência faz **toda rota autenticada
  devolver 500** (fail-closed de duas camadas, G0/frontend E9) — não é algo para
  contornar, é a proteção funcionando.
- Não existe página de login. Gere o cookie de sessão manualmente no DevTools do
  navegador, para o tenant real do seed Hojex News (`a0000000-0000-4000-8000-000000000001`,
  advertiser "Hojex House", zonas 1001–1004 — `db/seed/hojex_news_seed.sql`):

  ```
  node -e 'console.log(Buffer.from(JSON.stringify({tenantId:"a0000000-0000-4000-8000-000000000001",userId:"b0000000-0000-4000-8000-000000000002",exp:4102444800})).toString("base64url"))'
  ```

  No navegador, em `http://localhost:3015`:
  `document.cookie="betasess=<TOKEN>; path=/"` e recarregue.

**O que o anunciante consegue fazer no console, perfil beta (medido nesta sessão):**

- CRUD completo de anunciantes/campanhas/banners/sites/zonas
  (`bff/src/routers/config.ts`) persiste em `config.*` no MESMO banco `adserver_beta`
  que o `decision` lê via `DATABASE_URL`/`SNAPSHOT_REFRESH_INTERVAL=10s` — uma campanha
  criada no console aparece servida pelo `decision` em até 10s, sem restart.
- O dashboard mostra a série **"Ao vivo"** com números reais vindos de
  `stats.live_kpis` (`PostgresStatsAdapter.queryLive`) — quando `stats.events_raw`/
  `stats.live_kpis` já existirem no seu checkout (ver §5; se o schema ainda não
  existir, `queryLive` também falha, pelo mesmo motivo que `beta-check` falha).

**O que ele NÃO consegue:**

- Ver a série **"Consolidado ≤1h"** — `queryConsolidated` recusa sempre neste perfil
  (`PRECONDITION_FAILED`); não existe fonte faturável fora do pipeline Iceberg real
  (DA-7, ADR-0001). O console nunca soma "ao vivo" com "consolidado" nem apresenta o
  ao vivo como faturável (rótulo + `aria-label` de `DataSourceBadge`).
- Ver `requests` (pré-impressão) reais ou custo real na série ao vivo — são placeholders
  documentados (`inventoryLoss=0`, `totalCost.amount="0.00"`) enquanto o coletor não
  rastrear ad-requests e não houver pipeline de custo ligado ao ledger.
