# make/db.mk — alvos de banco de dados (Fase 1+3).
# Incluído automaticamente pelo root Makefile via: -include make/*.mk
# NÃO edite o root Makefile para adicionar alvos deste fragmento.
#
# Dois modos de operação:
#
#   1. Postgres externo (DATABASE_URL):
#      db-migrate-up / db-migrate-down / db-migrate-status
#      db-test / db-test-compliance / db-test-ledger / db-test-vector
#      Requer: golang-migrate CLI no PATH e DATABASE_URL exportado.
#
#   2. Postgres efêmero via Docker (db-test-all):
#      Sobe um container pgvector/pgvector:pg16 (postgres:16 + extensão
#      pgvector — necessário porque a migration db/vector/0001 faz
#      `CREATE EXTENSION vector`; a imagem stock postgres:16 não a traz e a
#      migration abortaria antes de qualquer RLS test rodar — alinhado com
#      .github/workflows/db.yml, que já usa pgvector/pgvector:pg16 por este
#      mesmo motivo), aplica TODAS as migrations de todos os schemas em
#      ordem, roda TODOS os rls_isolation_test.sql (+ postings_immutability_test.sql
#      do ledger), aplica os _down e confirma reversão. Derruba o container
#      ao fim.
#      Requer: docker no PATH.
#      Nenhuma credencial externa necessária — completamente efêmero.
#
# Ordem de aplicação obrigatória das migrations (dependências de schema):
#   asset_registry → config (depende de asset_registry) →
#   ledger (independente mas antes de compliance) →
#   vector (independente) →
#   compliance (depende de config para current_tenant_id())
#
# DENTRO de cada schema, a ordem das migrations NUNCA é enumerada à mão.
# db-test-all (FASE 2/FASE 5 abaixo) deriva a lista a partir do conteúdo
# real de db/<schema>/migrations/ via glob + sort:
#   _up:   ls db/<schema>/migrations/*_up.sql   | sort    (0001 → 0002 → ...)
#   _down: ls db/<schema>/migrations/*_down.sql | sort -r (mais recente primeiro)
# Convenção obrigatória: prefixo numérico de 4 dígitos zero-padded (0001,
# 0002, ...) — é isso que garante que a ordenação lexicográfica (sort)
# coincida com a ordem numérica até 9999 migrations por schema. Uma nova
# migration (ex.: 0005_foo_{up,down}.sql) é pega automaticamente por este
# glob; NADA nestes runners precisa ser editado. Isto vale tanto para
# make/db.mk (este arquivo) quanto para .github/workflows/db.yml — os dois
# runners derivam do mesmo diretório, nunca de uma lista mantida à mão.
# Se um diretório de migrations não tiver nenhum arquivo *_up.sql/*_down.sql
# (schema vazio ou mal formado), o runner FALHA explicitamente em vez de
# silenciosamente pular o schema (sentinela anti-vazio).
#
# Duas sentinelas adicionais fecham os pontos-cegos abertos pelo glob acima
# (achados MEDIUM da 30ª onda, guardiões security/tech-lead/privacy/money):
#
#   db-check-migration-pairing: o glob por si só não garante que uma
#     migration tenha os DOIS lados. Um `NNNN_foo_up.sql` sem o
#     `NNNN_foo_down.sql` correspondente (ou vice-versa) é aplicado
#     normalmente por db-migrate-up/db-test-all e só quebra no rollback,
#     quando já é tarde. Este alvo compara os dois conjuntos por schema e
#     FALHA explicitamente na divergência.
#
#   db-check-schema-list: o glob resolve a ordem DENTRO de cada schema, mas
#     DB_SCHEMAS/DB_SCHEMAS_REV abaixo continuam uma lista mantida à mão —
#     um schema novo (diretório com subpasta migrations/) que não seja
#     acrescentado a DB_SCHEMAS fica invisível para todo runner (mesma
#     classe de orfandade da migration sem par, um nível acima: schema em
#     vez de migration). DB_SCHEMAS NÃO é derivado automaticamente do
#     glob porque a ORDEM ENTRE schemas é uma dependência real de negócio
#     (ver acima) que um `ls | sort` alfabético não reproduziria (ex.:
#     "compliance" ordena antes de "ledger" alfabeticamente, mas depende de
#     config e tem que vir por último) — a sentinela detecta a divergência
#     sem assumir a responsabilidade de inferir a ordem.
#
# Ambas rodam como pré-requisito de db-test-all (fail-fast, antes de subir
# o container) e como step próprio em .github/workflows/db.yml.

MIGRATE := $(shell command -v migrate 2>/dev/null || echo migrate)

# Schemas e ORDEM ENTRE schemas. Esta, sim, é uma dependência real de schema
# (asset_registry antes de config, compliance por último) e por isso precisa
# ser DECLARADA — mas declarada UMA vez, em db/schema-order.txt, e derivada
# aqui. A ordem DENTRO de cada schema não vem daqui: vem do glob descrito
# acima.
#
# Antes da 32ª onda esta lista era um literal, e havia mais três cópias
# independentes dela (make/dev.mk::dev-db-setup, deploy/local/postgres/
# 10-init.sh e .github/workflows/db.yml). As cópias divergiram: os dois
# provisionadores locais enumeravam as migrations À MÃO e pararam em
# config/0003 — deixando config.campaign_zones_tenant_isolation sem WITH
# CHECK no catálogo — e o initdb do Docker aplicava só ledger/0001, sem RLS
# (0003) nem imutabilidade append-only (0004). É a 4ª reincidência da classe
# "enumeração mantida à mão"; a correção é da FORMA (derivação + gate
# default-deny em scripts/ci/db-provisioner-check.py), não da instância.
# `override` e deliberado: sem ele, `make DB_SCHEMAS="asset_registry" db-migrate-up`
# venceria a atribuicao do arquivo (definicao de linha de comando tem precedencia
# maxima no GNU Make) e pularia config/ledger/stats/vector/compliance em silencio —
# a sentinela abaixo so pega lista VAZIA, nao subconjunto arbitrario. Achado do
# parity-golden-test-guardian na barreira da 32a onda.
DB_SCHEMA_ORDER_FILE := db/schema-order.txt
override DB_SCHEMAS     := $(shell sed -e 's/#.*//' -e '/^[[:space:]]*$$/d' $(DB_SCHEMA_ORDER_FILE) 2>/dev/null)
override DB_SCHEMAS_REV := $(shell sed -e 's/#.*//' -e '/^[[:space:]]*$$/d' $(DB_SCHEMA_ORDER_FILE) 2>/dev/null | tac)

# Sentinela anti-vazio: se o arquivo de ordem sumir ou ficar ilegível, TODO
# runner de banco viraria um no-op verde (loop sobre lista vazia = zero
# migrations aplicadas, zero testes rodados, exit 0). Falhar aqui, no parse
# do Makefile, é ruidoso de propósito — a alternativa é falso-verde.
ifeq ($(strip $(DB_SCHEMAS)),)
$(error db/schema-order.txt vazio ou ausente — sem ele DB_SCHEMAS fica vazio e todo alvo de banco vira no-op verde. Restaure o arquivo antes de rodar qualquer coisa.)
endif

## db-check-migration-pairing: sentinela — todo *_up.sql precisa ter o
##   *_down.sql correspondente (mesmo schema, mesmo prefixo) e vice-versa.
##   Sem isto, uma migration com só um dos lados é aplicada normalmente e
##   só quebra na reversão (db-migrate-down / FASE 5 de db-test-all), tarde
##   demais. Roda ANTES de subir qualquer container (fail-fast).
db-check-migration-pairing:
	@echo "== db-check-migration-pairing: verificando pareamento _up/_down por schema =="
	@FAIL=0; \
	 for schema in $(DB_SCHEMAS); do \
	   dir="db/$$schema/migrations"; \
	   [ -d "$$dir" ] || continue; \
	   for up in "$$dir"/*_up.sql; do \
	     [ -e "$$up" ] || continue; \
	     base=$$(basename "$$up" _up.sql); \
	     down="$$dir/$${base}_down.sql"; \
	     if [ ! -e "$$down" ]; then \
	       echo "ERRO: $$schema/$${base}_up.sql não tem par $$schema/$${base}_down.sql"; \
	       FAIL=1; \
	     fi; \
	   done; \
	   for down in "$$dir"/*_down.sql; do \
	     [ -e "$$down" ] || continue; \
	     base=$$(basename "$$down" _down.sql); \
	     up="$$dir/$${base}_up.sql"; \
	     if [ ! -e "$$up" ]; then \
	       echo "ERRO: $$schema/$${base}_down.sql não tem par $$schema/$${base}_up.sql"; \
	       FAIL=1; \
	     fi; \
	   done; \
	 done; \
	 if [ "$$FAIL" = "1" ]; then \
	   echo "ERRO: pareamento _up/_down quebrado — ver mensagens acima."; \
	   exit 1; \
	 fi
	@echo "== db-check-migration-pairing: ok =="

## db-check-schema-list: sentinela — compara os diretórios reais de db/
##   (os que têm subpasta migrations/) contra DB_SCHEMAS acima. Um schema
##   novo nasce invisível para db-migrate-*/db-test-all se não for
##   acrescentado a DB_SCHEMAS/DB_SCHEMAS_REV; esta sentinela FALHA
##   explicitamente na divergência em vez de deixar o schema órfão em
##   silêncio. Não deriva DB_SCHEMAS automaticamente porque a ORDEM entre
##   schemas é uma dependência real (ver comentário no topo do arquivo).
db-check-schema-list:
	@echo "== db-check-schema-list: comparando diretórios de db/ com DB_SCHEMAS =="
	@real=$$(for d in db/*/; do n=$$(basename "$$d"); [ -d "db/$$n/migrations" ] && echo "$$n"; done | LC_ALL=C sort); \
	 expected=$$(printf '%s\n' $(DB_SCHEMAS) | LC_ALL=C sort); \
	 expected_rev_sorted=$$(printf '%s\n' $(DB_SCHEMAS_REV) | LC_ALL=C sort); \
	 if [ "$$real" != "$$expected" ]; then \
	   echo "ERRO: divergência entre diretórios reais de schema em db/ e DB_SCHEMAS (make/db.mk)."; \
	   echo "  diretórios reais (com migrations/): $$(echo $$real)"; \
	   echo "  DB_SCHEMAS declarado:                $$(echo $$expected)"; \
	   echo "  Um schema novo (ou removido) precisa ser refletido em DB_SCHEMAS e DB_SCHEMAS_REV — a ordem entre schemas não pode ser derivada por glob."; \
	   exit 1; \
	 fi; \
	 if [ "$$expected" != "$$expected_rev_sorted" ]; then \
	   echo "ERRO: DB_SCHEMAS e DB_SCHEMAS_REV (make/db.mk) não contêm o mesmo conjunto de schemas."; \
	   exit 1; \
	 fi
	@echo "== db-check-schema-list: ok =="

## db-check-provisioners: sentinela — nenhum provisionador (make/dev.mk,
##   deploy/local/postgres/10-init.sh, .github/workflows/db.yml, este arquivo)
##   pode enumerar migration à mão, e nenhuma instrução operacional pode
##   enumerar uma lista PARCIAL. É a irmã comportamental das duas sentinelas
##   acima: elas garantem que a lista derivada esteja bem-formada; esta
##   garante que ninguém contorne a derivação. Ver o cabeçalho de
##   scripts/ci/db-provisioner-check.py para as 4 reincidências que a
##   motivaram. Roda também em .github/workflows/repo-gates.yml (sem filtro
##   de path — a superfície é espalhada por construção).
db-check-provisioners:
	@python3 scripts/ci/db-provisioner-check.py

# Container efêmero para db-test-all.
_DB_TEST_CONTAINER := adserver-db-test-ephemeral
_DB_TEST_PORT      := 15432
_DB_TEST_USER      := postgres
_DB_TEST_PASS      := postgres_test_only
_DB_TEST_DB        := adserver_test
_DB_TEST_DSN       := postgres://$(_DB_TEST_USER):$(_DB_TEST_PASS)@localhost:$(_DB_TEST_PORT)/$(_DB_TEST_DB)?sslmode=disable

## db-lint: roda o guard anti-float (TX-2) sobre todos os arquivos SQL em db/
db-lint:
	@echo "== db-lint: verificando tipos flutuantes em db/ (TX-2) =="
	@bash scripts/ci/no-float-sql.sh db/
	@echo "== db-lint: ok =="

## db-migrate-up: aplica todas as migrations em ordem (asset_registry → config → ledger → vector → compliance)
db-migrate-up: _db-check-url
	@for schema in $(DB_SCHEMAS); do \
	  echo "-- migrate up: $$schema"; \
	  "$(MIGRATE)" -database "$(DATABASE_URL)" -path "db/$$schema/migrations" up || exit 1; \
	done
	@echo "== db-migrate-up: concluído =="

## db-migrate-down: reverte UM passo de cada schema (em ordem inversa: compliance → vector → ledger → config → asset_registry)
db-migrate-down: _db-check-url
	@for schema in $(DB_SCHEMAS_REV); do \
	  echo "-- migrate down 1: $$schema"; \
	  "$(MIGRATE)" -database "$(DATABASE_URL)" -path "db/$$schema/migrations" down 1 || exit 1; \
	done
	@echo "== db-migrate-down: concluído =="

## db-migrate-status: exibe versão corrente de cada schema
db-migrate-status: _db-check-url
	@for schema in $(DB_SCHEMAS); do \
	  echo "-- status: $$schema"; \
	  "$(MIGRATE)" -database "$(DATABASE_URL)" -path "db/$$schema/migrations" version; \
	done

## db-test: executa o teste de isolamento RLS (TX-3) do schema config contra Postgres externo.
##   Requer DATABASE_URL e que as migrations do schema config já tenham sido aplicadas.
##   O teste roda dentro de ROLLBACK — não persiste dados.
db-test: _db-check-url
	@echo "== db-test: isolamento RLS por tenant — schema config (TX-3) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/config/tests/rls_isolation_test.sql
	@echo "== db-test: concluído =="

## db-test-compliance: executa o teste de isolamento RLS do cofre de compliance (K6 / TX-3).
db-test-compliance: _db-check-url
	@echo "== db-test-compliance: isolamento RLS cofre compliance (K6 / TX-3) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/compliance/tests/rls_isolation_test.sql
	@echo "== db-test-compliance: concluído =="

## db-test-ledger: executa o teste de isolamento RLS do ledger (Fase 3 / TX-3 / DA-11).
##   Valida isolamento de accounts, journal_entries, postings e account_balances (view).
##   Requer DATABASE_URL e que as migrations 0001/0002/0003 do schema ledger tenham sido aplicadas.
##   O teste roda dentro de ROLLBACK — não persiste dados.
db-test-ledger: _db-check-url
	@echo "== db-test-ledger: isolamento RLS ledger (Fase 3 / TX-3 / DA-11) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/ledger/tests/rls_isolation_test.sql
	@echo "== db-test-ledger: concluído =="

## db-test-ledger-immutability: prova append-only de ledger.postings e imutabilidade
##   de ledger.journal_entries pos-posted/void (achado HIGH #6 / migration 0004).
##   Requer DATABASE_URL e que as migrations 0001..0004 do schema ledger tenham sido
##   aplicadas. Roda dentro de ROLLBACK — não persiste dados.
db-test-ledger-immutability: _db-check-url
	@echo "== db-test-ledger-immutability: append-only postings + journal_entries (HIGH #6) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/ledger/tests/postings_immutability_test.sql
	@echo "== db-test-ledger-immutability: concluído =="

## db-test-vector: executa o teste de isolamento RLS do schema vector_store.
##   Requer DATABASE_URL e que as migrations do schema vector tenham sido aplicadas.
##   O teste roda dentro de ROLLBACK — não persiste dados.
db-test-vector: _db-check-url
	@echo "== db-test-vector: isolamento RLS embeddings (TX-3) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/vector/tests/vector_rls_isolation_test.sql
	@echo "== db-test-vector: concluído =="

## db-test-stats: executa o teste de isolamento RLS de leitura (USING) do
##   schema stats — persistência do collector via Postgres (ADR-0005 "perfil
##   BETA" / TX-3 / achado M-2, security-reviewer). Requer DATABASE_URL, que
##   db/stats/migrations/0001_stats_schema_up.sql já tenha sido aplicada, e
##   que adserver_app tenha os GRANTs de leitura em stats (ver
##   make/dev.mk::dev-db-setup ou .github/workflows/db.yml, step de grants —
##   comentados na própria migration, achado H-2). O lado WITH CHECK
##   (escrita) já é provado por
##   internal/telemetry/pgsink/pgsink_integration_test.go
##   (//go:build integration); este alvo cobre o lado USING (leitura), que
##   nenhum outro teste do repo exercitava. O teste roda dentro de
##   ROLLBACK — não persiste dados.
db-test-stats: _db-check-url
	@echo "== db-test-stats: isolamento RLS stats.events_raw / live_kpis (ADR-0005 / TX-3) =="
	@psql "$(DATABASE_URL)" \
	      -v ON_ERROR_STOP=1 \
	      -f db/stats/tests/rls_isolation_test.sql
	@echo "== db-test-stats: concluído =="

## db-test-all: sobe Postgres efêmero (Docker), aplica TODAS as migrations,
##   roda TODOS os rls_isolation_test.sql (config, ledger, vector, compliance)
##   + postings_immutability_test.sql (ledger), aplica _down e confirma
##   reversão. Derruba o container ao fim.
##   Sem DATABASE_URL externo — completamente efêmero e sem credenciais.
##   Requer: docker no PATH.
##   Imagem: pgvector/pgvector:pg16 (postgres:16 + extensão pgvector — a
##   stock postgres:16 não traz a extensão que db/vector/0001 exige via
##   `CREATE EXTENSION vector`; alinhado com .github/workflows/db.yml).
db-test-all: db-check-schema-list db-check-migration-pairing db-check-provisioners
	@echo "== db-test-all: iniciando Postgres efêmero =="
	@if ! command -v docker >/dev/null 2>&1; then \
	  echo "ERRO: docker não encontrado no PATH. db-test-all requer Docker."; \
	  exit 1; \
	fi
	@if ! command -v psql >/dev/null 2>&1; then \
	  echo "ERRO: psql não encontrado no PATH. db-test-all requer psql (cliente PostgreSQL)."; \
	  exit 1; \
	fi
	@# Garante que não há container residual de execução anterior.
	@docker rm -f $(_DB_TEST_CONTAINER) >/dev/null 2>&1 || true
	@echo "-- subindo pgvector/pgvector:pg16 na porta $(_DB_TEST_PORT) ..."
	@docker run -d \
	  --name $(_DB_TEST_CONTAINER) \
	  -e POSTGRES_USER=$(_DB_TEST_USER) \
	  -e POSTGRES_PASSWORD=$(_DB_TEST_PASS) \
	  -e POSTGRES_DB=$(_DB_TEST_DB) \
	  -p $(_DB_TEST_PORT):5432 \
	  pgvector/pgvector:pg16 >/dev/null
	@echo "-- aguardando Postgres ficar pronto ..."
	@_DSN="$(_DB_TEST_DSN)"; \
	 for i in $$(seq 1 30); do \
	   psql "$$_DSN" -c "SELECT 1" >/dev/null 2>&1 && break; \
	   [ "$$i" = "30" ] && { echo "ERRO: Postgres não ficou pronto em 30s"; docker rm -f $(_DB_TEST_CONTAINER); exit 1; }; \
	   sleep 1; \
	 done
	@echo "-- Postgres pronto."
	@# --------------------------------------------------------------------------
	@# FASE 1: aplicar roles de dev (adserver_loader BYPASSRLS / adserver_app RLS)
	@# DEVE rodar antes das migrations que criam GRANTs e dependem dos roles.
	@# --------------------------------------------------------------------------
	@echo "-- criando roles de aplicação (adserver_loader / adserver_app) ..."
	@psql "$(_DB_TEST_DSN)" -v ON_ERROR_STOP=1 -q \
	  -c "DO \$$\$$ BEGIN \
	       IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_loader') THEN \
	         CREATE ROLE adserver_loader LOGIN PASSWORD 'loader_dev_only' BYPASSRLS; \
	       END IF; \
	       IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'adserver_app') THEN \
	         CREATE ROLE adserver_app LOGIN PASSWORD 'app_dev_only'; \
	       END IF; \
	     END \$$\$$;"
	@# --------------------------------------------------------------------------
	@# FASE 2: aplicar migrations _up em ordem obrigatória
	@# --------------------------------------------------------------------------
	@echo "-- aplicando migrations _up (lista derivada do diretório, não enumerada à mão) ..."
	@_DSN="$(_DB_TEST_DSN)"; FAIL=0; \
	 for schema in $(DB_SCHEMAS); do \
	   dir="db/$$schema/migrations"; \
	   files=$$(ls "$$dir"/*_up.sql 2>/dev/null | LC_ALL=C sort); \
	   if [ -z "$$files" ]; then \
	     echo "ERRO: nenhuma migration *_up.sql encontrada em $$dir (schema '$$schema' vazio ou mal formado) — sentinela anti-vazio."; \
	     FAIL=1; \
	     break; \
	   fi; \
	   for fpath in $$files; do \
	     base=$$(basename "$$fpath" _up.sql); \
	     echo "  up: $$schema/$$base"; \
	     psql "$$_DSN" -v ON_ERROR_STOP=1 -q -f "$$fpath" || FAIL=1; \
	   done; \
	 done; \
	 if [ "$$FAIL" = "1" ]; then \
	   echo "ERRO: migrations _up falharam."; docker rm -f $(_DB_TEST_CONTAINER); exit 1; \
	 fi
	@# --------------------------------------------------------------------------
	@# FASE 3: aplicar grants sobre os schemas criados pelas migrations.
	@# dev_roles.sql cobre config + asset_registry + adserver_stats_writer
	@# (achado H-2: o role de escrita do schema stats agora é criado AQUI, não
	@# mais pela migration 0001_stats_schema_up.sql); adicionar
	@# ledger/vector/compliance/stats abaixo.
	@# Least-privilege (achado HIGH #6): adserver_app nunca faz UPDATE/DELETE em
	@# ledger.postings (internal/ledger/posting.go só faz INSERT/SELECT) — o REVOKE
	@# abaixo estreita o grant amplo de ALL TABLES para esta tabela especificamente.
	@# Mudança de comportamento ZERO para a aplicação; a garantia primária de
	@# append-only é o trigger postings_immutable_trg (migration 0004), que vale
	@# também para superusuário e para acesso direto a uma partição.
	@# --------------------------------------------------------------------------
	@echo "-- aplicando grants sobre os schemas ..."
	@psql "$(_DB_TEST_DSN)" -v ON_ERROR_STOP=1 -q -f db/seed/dev_roles.sql
	@psql "$(_DB_TEST_DSN)" -v ON_ERROR_STOP=1 -q \
	  -c "GRANT USAGE ON SCHEMA ledger TO adserver_loader, adserver_app;" \
	  -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ledger TO adserver_app;" \
	  -c "REVOKE UPDATE, DELETE ON ledger.postings FROM adserver_app;" \
	  -c "GRANT SELECT ON ALL TABLES IN SCHEMA ledger TO adserver_loader;" \
	  -c "GRANT USAGE ON ALL SEQUENCES IN SCHEMA ledger TO adserver_app;" \
	  -c "GRANT EXECUTE ON FUNCTION ledger.current_tenant_id() TO adserver_app, adserver_loader;" \
	  -c "GRANT USAGE ON SCHEMA vector_store TO adserver_loader, adserver_app;" \
	  -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA vector_store TO adserver_app;" \
	  -c "GRANT SELECT ON ALL TABLES IN SCHEMA vector_store TO adserver_loader;" \
	  -c "GRANT USAGE ON SCHEMA compliance TO adserver_loader, adserver_app;" \
	  -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA compliance TO adserver_app;" \
	  -c "GRANT SELECT ON ALL TABLES IN SCHEMA compliance TO adserver_loader;" \
	  -c "GRANT USAGE ON SCHEMA stats TO adserver_loader, adserver_app;" \
	  -c "GRANT SELECT ON stats.events_raw, stats.live_kpis TO adserver_loader, adserver_app;" \
	  -c "GRANT EXECUTE ON FUNCTION stats.current_tenant_id() TO adserver_loader, adserver_app;" \
	  -c "GRANT USAGE ON SCHEMA stats TO adserver_stats_writer;" \
	  -c "GRANT SELECT, INSERT ON stats.events_raw TO adserver_stats_writer;" \
	  -c "GRANT EXECUTE ON FUNCTION stats.current_tenant_id() TO adserver_stats_writer;"
	@# --------------------------------------------------------------------------
	@# FASE 4: rodar todos os testes de isolamento RLS
	@# --------------------------------------------------------------------------
	@echo "== db-test-all: rodando testes de isolamento RLS =="
	@_DSN="$(_DB_TEST_DSN)"; FAIL=0; \
	 for schema_test in \
	   "config:db/config/tests/rls_isolation_test.sql" \
	   "ledger:db/ledger/tests/rls_isolation_test.sql" \
	   "ledger-immutability:db/ledger/tests/postings_immutability_test.sql" \
	   "stats:db/stats/tests/rls_isolation_test.sql" \
	   "vector:db/vector/tests/vector_rls_isolation_test.sql" \
	   "compliance:db/compliance/tests/rls_isolation_test.sql"; do \
	   schema=$$(echo "$$schema_test" | cut -d: -f1); \
	   testfile=$$(echo "$$schema_test" | cut -d: -f2); \
	   echo "-- RLS test: $$schema"; \
	   if psql "$$_DSN" -v ON_ERROR_STOP=1 -f "$$testfile" 2>&1; then \
	     echo "   PASS: $$schema"; \
	   else \
	     echo "   FAIL: $$schema"; \
	     FAIL=1; \
	   fi; \
	 done; \
	 echo ""; \
	 if [ "$$FAIL" = "1" ]; then \
	   echo "== db-test-all: ALGUM TESTE FALHOU =="; \
	   docker rm -f $(_DB_TEST_CONTAINER) >/dev/null 2>&1; \
	   exit 1; \
	 fi; \
	 echo "== db-test-all: todos os RLS tests PASSARAM =="
	@# --------------------------------------------------------------------------
	@# FASE 5: aplicar migrations _down em ordem inversa e confirmar reversão
	@# --------------------------------------------------------------------------
	@echo "-- aplicando migrations _down (reversão, lista derivada do diretório) ..."
	@_DSN="$(_DB_TEST_DSN)"; FAIL=0; \
	 for schema in $(DB_SCHEMAS_REV); do \
	   dir="db/$$schema/migrations"; \
	   files=$$(ls "$$dir"/*_down.sql 2>/dev/null | LC_ALL=C sort -r); \
	   if [ -z "$$files" ]; then \
	     echo "ERRO: nenhuma migration *_down.sql encontrada em $$dir (schema '$$schema' vazio ou mal formado) — sentinela anti-vazio."; \
	     FAIL=1; \
	     break; \
	   fi; \
	   for fpath in $$files; do \
	     base=$$(basename "$$fpath" _down.sql); \
	     echo "  down: $$schema/$$base"; \
	     psql "$$_DSN" -v ON_ERROR_STOP=1 -q -f "$$fpath" || FAIL=1; \
	   done; \
	 done; \
	 if [ "$$FAIL" = "1" ]; then \
	   echo "ERRO: migrations _down falharam."; docker rm -f $(_DB_TEST_CONTAINER); exit 1; \
	 fi; \
	 echo "-- reversão concluída."
	@# --------------------------------------------------------------------------
	@# FASE 6: derrubar container efêmero
	@# --------------------------------------------------------------------------
	@docker rm -f $(_DB_TEST_CONTAINER) >/dev/null
	@echo "== db-test-all: PASS — up + RLS tests + down OK. Container removido. =="

# Alvo interno: verifica se DATABASE_URL está definido
_db-check-url:
	@if [ -z "$(DATABASE_URL)" ]; then \
	  echo "ERRO: DATABASE_URL não está definido."; \
	  echo "  export DATABASE_URL=postgres://user:pass@host:5432/adserver?sslmode=require"; \
	  exit 1; \
	fi

.PHONY: db-lint db-migrate-up db-migrate-down db-migrate-status \
        db-test db-test-compliance db-test-ledger db-test-ledger-immutability \
        db-test-vector db-test-stats db-test-all \
        db-check-migration-pairing db-check-schema-list db-check-provisioners \
        _db-check-url
