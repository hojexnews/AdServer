# make/beta.mk — perfil BETA: UM comando pra subir o hot path, UM pra checar.
#
# Incluido automaticamente pelo root Makefile via -include make/*.mk. Prefixo
# beta-. Dono: plataforma (DevOps/SRE). NAO edite o root Makefile, make/dev.mk
# ou make/db.mk para mudar este fragmento — sao donos separados.
#
# O QUE E o perfil beta (ver docs/ops/beta-local.md para o detalhe completo):
#   decision (:8080) + collector (:8081) rodando NATIVAMENTE (sem Docker),
#   contra um Postgres 16 local ja em execucao, SEM Redis/ClickHouse/Redpanda.
#   BFF (:3001) e console (:3005) ficam FORA do escopo de beta-up: outra sessao
#   humana os mantem no ar nestas portas (ver docs/ops/beta-local.md, secao
#   "Console do anunciante (manual)") — beta-up nunca sobe nem derruba nada
#   nessas portas.
#
# Contrato de env fixo (outros fragmentos/servicos sao implementados contra
# estes nomes exatos — NAO renomeie sem atualizar services/decision e
# services/collector em conjunto):
#   TELEMETRY_PG_DSN   -> collector persiste eventos em Postgres em vez de
#                         Redpanda/ClickHouse (schema stats, tabela
#                         stats.events_raw). Dono: engenheiro do collector.
#   CAPPING_BACKEND=memory -> decision impoe frequency cap em memoria de
#                         processo, sem Redis. Dono: engenheiro do decision.
#   BFF_PG_DSN         -> BFF usa Postgres real para config+payments (fora do
#                         escopo deste arquivo — ver docs/ops/beta-local.md).
# Este fragmento SEMPRE exporta TELEMETRY_PG_DSN/CAPPING_BACKEND para os dois
# processos que sobe, mesmo antes de o outro lado estar pronto para
# consumi-los: quando o wiring aparecer no binario, passa a funcionar sem
# precisar mexer aqui.
#
# ARMADILHA DO REPO (achado da 24a onda — NAO regrida): o caminho deste
# repositorio pode conter espaco (".../Hojex News/..."). Toda variavel de
# path usada dentro de uma recipe FICA ENTRE ASPAS DUPLAS. Este arquivo segue
# essa regra em toda parte que toca BETA_DIR/CURDIR.

BETA_DB              ?= adserver_beta
BETA_DIR             := $(CURDIR)/.beta

# Bind em LOOPBACK por default (ADR-0005, aceite de M-1/M-4 do security-reviewer).
# ":8080" em Go significa TODAS as interfaces — o perfil beta ficaria exposto na
# rede local com endpoints nao autenticados (/lg aceita tid/cid crus). Escrever
# "nao exponha o beta" so no ADR seria a classe do //nolint decorativo da 26a
# onda: a garantia no comentario e o bind nunca a respeitando. Quem quiser
# expor sobrescreve conscientemente: make beta-up BETA_DECISION_ADDR=:8080
BETA_DECISION_ADDR   ?= 127.0.0.1:8080
BETA_COLLECTOR_ADDR  ?= 127.0.0.1:8081
# Porta = tudo depois do ULTIMO ':' — funciona tanto para ":8080" quanto para
# "127.0.0.1:8080". O patsubst ':%' anterior so casava a primeira forma e, ao
# trocarmos o default para loopback, produzia "http://localhost:127.0.0.1:8080"
# (pego de 1a mao: beta-up subiu e o healthz nunca respondeu).
BETA_DECISION_PORT   := $(lastword $(subst :, ,$(BETA_DECISION_ADDR)))
BETA_COLLECTOR_PORT  := $(lastword $(subst :, ,$(BETA_COLLECTOR_ADDR)))
BETA_DECISION_URL    := http://localhost:$(BETA_DECISION_PORT)
BETA_COLLECTOR_URL   := http://localhost:$(BETA_COLLECTOR_PORT)

# DSN do loader (papel BYPASSRLS) contra BETA_DB — mesmo formato/role de
# make/dev.mk, mas aponta para o banco DE BETA (nunca adserver_dev), para nao
# colidir com a sessao humana que ja usa o BFF/console em :3001/:3005.
BETA_LOADER_DSN := postgres://adserver_loader:loader_dev_only@localhost:5432/$(BETA_DB)?sslmode=disable

# DSN do ESCRITOR de telemetria (pgsink do collector). Papel dedicado
# adserver_stats_writer, criado pela propria migration db/stats/0001:
# INSERT/SELECT so em stats.events_raw e NOBYPASSRLS — menor privilegio.
#
# NAO reuse BETA_LOADER_DSN aqui: alem de ser privilegio a mais (o loader e
# BYPASSRLS e enxerga todos os tenants), o loader nao tem GRANT de escrita em
# stats, e o sintoma e "permission denied for schema stats" so na etapa de
# persistencia do beta-check — depois de decide/pixel/clique passarem.
BETA_STATS_WRITER_DSN := postgres://adserver_stats_writer:stats_writer_dev_only@localhost:5432/$(BETA_DB)?sslmode=disable

# Segredos SOMENTE de dev local — nunca reais (producao usa OpenBao). Os dois
# servicos falham no boot (fail-closed) sem ALGUM valor; overridaveis por env
# se quiser reusar um valor especifico entre execucoes.
BETA_CAPPING_SALT    ?= beta-dev-salt-only
BETA_CK_HMAC_SECRET  ?= beta-dev-ck-secret-only

# Refresh mais agressivo que o default de producao (30s) para que uma edicao
# feita no console (BFF_PG_DSN, fora deste arquivo) apareca rapido no decision
# durante uma sessao de debug beta.
BETA_SNAPSHOT_REFRESH ?= 10s

BETA_PID_DECISION    := $(BETA_DIR)/decision.pid
BETA_PID_COLLECTOR   := $(BETA_DIR)/collector.pid
BETA_LOG_DECISION    := $(BETA_DIR)/decision.log
BETA_LOG_COLLECTOR   := $(BETA_DIR)/collector.log
BETA_ENV_DECISION    := $(BETA_DIR)/decision.env
BETA_ENV_COLLECTOR   := $(BETA_DIR)/collector.env
BETA_BIN_DECISION    := $(BETA_DIR)/bin/decision
BETA_BIN_COLLECTOR   := $(BETA_DIR)/bin/collector

BETA_HEALTH_TIMEOUT_S ?= 30

.PHONY: beta-up beta-down beta-status beta-check beta-logs

## beta-up: provisiona $(BETA_DB) e sobe decision(:8080)+collector(:8081) nativos em background (sem Docker)
beta-up:
	@if ! command -v go >/dev/null 2>&1; then echo "ERRO: go nao encontrado no PATH."; exit 1; fi
	@if ! command -v psql >/dev/null 2>&1; then echo "ERRO: psql nao encontrado no PATH."; exit 1; fi
	@if ! command -v curl >/dev/null 2>&1; then echo "ERRO: curl nao encontrado no PATH."; exit 1; fi
	@mkdir -p "$(BETA_DIR)/bin"
	@echo "== beta-up: garantindo estado limpo antes de subir (idempotencia) =="
	@$(MAKE) --no-print-directory beta-down
	@echo "== beta-up: provisionando banco $(BETA_DB) (reusa dev-db-setup, sem duplicar logica) =="
	@$(MAKE) --no-print-directory dev-db-setup DEV_DB="$(BETA_DB)"
	@echo "== beta-up: compilando decision + collector (go build, nao go run — go run cria um PID de wrapper diferente do processo real, o que quebra o kill-by-pid de beta-down) =="
	@go build -o "$(BETA_BIN_DECISION)" ./services/decision/cmd/decision/ \
	  || { echo "ERRO: go build de services/decision falhou — ver saida acima."; exit 1; }
	@go build -o "$(BETA_BIN_COLLECTOR)" ./services/collector/cmd/collector/ \
	  || { echo "ERRO: go build de services/collector falhou — ver saida acima."; exit 1; }
	@echo "== beta-up: subindo decision em $(BETA_DECISION_URL) =="
	@{ \
	  DATABASE_URL="$(BETA_LOADER_DSN)" \
	  CAPPING_SALT="$(BETA_CAPPING_SALT)" \
	  CK_HMAC_SECRET="$(BETA_CK_HMAC_SECRET)" \
	  CAPPING_BACKEND="memory" \
	  TRUSTED_PROXY_DEPTH="0" \
	  DECISION_ADDR="$(BETA_DECISION_ADDR)" \
	  SNAPSHOT_REFRESH_INTERVAL="$(BETA_SNAPSHOT_REFRESH)" \
	  nohup "$(BETA_BIN_DECISION)" >"$(BETA_LOG_DECISION)" 2>&1 < /dev/null & \
	  echo $$! > "$(BETA_PID_DECISION)"; \
	}
	@{ \
	  printf 'DATABASE_URL=%s\n'               "$(BETA_LOADER_DSN)"; \
	  printf 'CAPPING_SALT=%s\n'                "(dev-only, nao mostrado)"; \
	  printf 'CK_HMAC_SECRET=%s\n'              "(dev-only, nao mostrado)"; \
	  printf 'CAPPING_BACKEND=%s\n'             "memory"; \
	  printf 'DECISION_ADDR=%s\n'               "$(BETA_DECISION_ADDR)"; \
	  printf 'SNAPSHOT_REFRESH_INTERVAL=%s\n'   "$(BETA_SNAPSHOT_REFRESH)"; \
	  printf 'REDIS_ADDR=%s\n'                  "(nao setado — sem Redis no perfil beta)"; \
	  printf 'GEOIP_DB_PATH=%s\n'               "(nao setado — geo desligado, ver docs/ops/beta-local.md)"; \
	} > "$(BETA_ENV_DECISION)"
	@echo "== beta-up: subindo collector em $(BETA_COLLECTOR_URL) =="
	@{ \
	  CK_HMAC_SECRET="$(BETA_CK_HMAC_SECRET)" \
	  DECISION_BASE_URL="$(BETA_DECISION_URL)" \
	  COLLECTOR_BASE_URL="$(BETA_COLLECTOR_URL)" \
	  COLLECTOR_ADDR="$(BETA_COLLECTOR_ADDR)" \
	  TELEMETRY_PG_DSN="$(BETA_STATS_WRITER_DSN)" \
	  TRUSTED_PROXY_DEPTH="0" \
	  nohup "$(BETA_BIN_COLLECTOR)" >"$(BETA_LOG_COLLECTOR)" 2>&1 < /dev/null & \
	  echo $$! > "$(BETA_PID_COLLECTOR)"; \
	}
	@{ \
	  printf 'CK_HMAC_SECRET=%s\n'      "(dev-only, nao mostrado)"; \
	  printf 'DECISION_BASE_URL=%s\n'   "$(BETA_DECISION_URL)"; \
	  printf 'COLLECTOR_BASE_URL=%s\n'  "$(BETA_COLLECTOR_URL)"; \
	  printf 'COLLECTOR_ADDR=%s\n'      "$(BETA_COLLECTOR_ADDR)"; \
	  printf 'TELEMETRY_PG_DSN=%s\n'    "$(BETA_STATS_WRITER_DSN)"; \
	  printf 'REDPANDA_BROKERS=%s\n'    "(nao setado — sem Redpanda no perfil beta)"; \
	  printf 'GEOIP_DB_PATH=%s\n'       "(nao setado — geo desligado, ver docs/ops/beta-local.md)"; \
	} > "$(BETA_ENV_COLLECTOR)"
	@echo "== beta-up: aguardando /healthz (timeout $(BETA_HEALTH_TIMEOUT_S)s) =="
	@FAIL=0; \
	 for pair in "decision|$(BETA_DECISION_URL)|$(BETA_LOG_DECISION)|$(BETA_PID_DECISION)" \
	             "collector|$(BETA_COLLECTOR_URL)|$(BETA_LOG_COLLECTOR)|$(BETA_PID_COLLECTOR)"; do \
	   name=$$(echo "$$pair" | cut -d'|' -f1); \
	   url=$$(echo "$$pair" | cut -d'|' -f2); \
	   log=$$(echo "$$pair" | cut -d'|' -f3); \
	   pidfile=$$(echo "$$pair" | cut -d'|' -f4); \
	   ok=0; \
	   for i in $$(seq 1 $(BETA_HEALTH_TIMEOUT_S)); do \
	     pid=$$(cat "$$pidfile" 2>/dev/null); \
	     if [ -n "$$pid" ] && ! kill -0 "$$pid" 2>/dev/null; then \
	       echo "ERRO: $$name morreu durante o boot (pid $$pid nao existe mais) — ver $$log"; \
	       FAIL=1; break; \
	     fi; \
	     if curl -sf --max-time 2 "$$url/healthz" >/dev/null 2>&1; then ok=1; break; fi; \
	     sleep 1; \
	   done; \
	   if [ "$$ok" = "1" ]; then \
	     echo "  $$name: /healthz OK ($$url)"; \
	   elif [ "$$FAIL" != "1" ]; then \
	     echo "ERRO: $$name nao respondeu /healthz em $(BETA_HEALTH_TIMEOUT_S)s ($$url) — ver $$log"; \
	     FAIL=1; \
	   fi; \
	 done; \
	 if [ "$$FAIL" = "1" ]; then \
	   echo "== beta-up: FALHOU — ver mensagens acima e os logs em $(BETA_DIR)/ (make beta-logs) =="; \
	   exit 1; \
	 fi
	@echo ""
	@echo "== beta-up: OK =="
	@$(MAKE) --no-print-directory beta-status

## beta-down: derruba SOMENTE o que beta-up subiu, por PID file (nunca pkill largo)
beta-down:
	@mkdir -p "$(BETA_DIR)"
	@for svc in decision collector; do \
	  pidfile="$(BETA_DIR)/$$svc.pid"; \
	  if [ -f "$$pidfile" ]; then \
	    pid=$$(cat "$$pidfile" 2>/dev/null); \
	    if [ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null; then \
	      echo "beta-down: parando $$svc (pid $$pid) ..."; \
	      kill -TERM "$$pid" 2>/dev/null; \
	      stopped=0; \
	      for i in $$(seq 1 20); do \
	        kill -0 "$$pid" 2>/dev/null || { stopped=1; break; }; \
	        sleep 0.5; \
	      done; \
	      if [ "$$stopped" = "1" ]; then \
	        echo "beta-down: $$svc (pid $$pid) parado"; \
	      else \
	        echo "beta-down: $$svc (pid $$pid) nao respondeu a SIGTERM em 10s — enviando SIGKILL"; \
	        kill -KILL "$$pid" 2>/dev/null || true; \
	      fi; \
	    else \
	      echo "beta-down: $$svc: pid file presente mas processo $$pid ja nao existia (stale) — limpando"; \
	    fi; \
	    rm -f "$$pidfile"; \
	  else \
	    echo "beta-down: $$svc: nao estava rodando (sem pid file)"; \
	  fi; \
	done

## beta-status: o que esta no ar, em que porta, com que backend (Postgres/memoria/noop)
beta-status:
	@echo "== beta-status =="
	@echo "banco: $(BETA_DB)"
	@if psql "$(BETA_LOADER_DSN)" -tAc "select 1" >/dev/null 2>&1; then \
	  echo "  postgres: alcancavel ($(BETA_LOADER_DSN))"; \
	else \
	  echo "  postgres: INALCANCAVEL ($(BETA_LOADER_DSN)) — rode 'make beta-up' ou verifique o Postgres local"; \
	fi
	@for pair in "decision|$(BETA_DECISION_URL)" "collector|$(BETA_COLLECTOR_URL)"; do \
	  name=$$(echo "$$pair" | cut -d'|' -f1); \
	  url=$$(echo "$$pair" | cut -d'|' -f2); \
	  pidfile="$(BETA_DIR)/$$name.pid"; \
	  envfile="$(BETA_DIR)/$$name.env"; \
	  echo ""; \
	  echo "-- $$name ($$url) --"; \
	  if [ -f "$$pidfile" ]; then \
	    pid=$$(cat "$$pidfile" 2>/dev/null); \
	    if [ -n "$$pid" ] && kill -0 "$$pid" 2>/dev/null; then \
	      echo "  processo: rodando (pid $$pid)"; \
	    else \
	      echo "  processo: PID FILE ORFAO (pid $$pid nao existe mais) — rode 'make beta-down' para limpar"; \
	    fi; \
	  else \
	    echo "  processo: nao rodando (sem pid file)"; \
	  fi; \
	  health=$$(curl -sf --max-time 2 "$$url/healthz" 2>/dev/null || echo ""); \
	  if [ -n "$$health" ]; then \
	    echo "  /healthz: $$health"; \
	  else \
	    echo "  /healthz: sem resposta"; \
	  fi; \
	  if [ -f "$$envfile" ]; then \
	    echo "  config resolvida (env.$$name):"; \
	    sed 's/^/    /' "$$envfile"; \
	  fi; \
	done
	@echo ""
	@echo "NOTA: BFF(:3001)/console(:3005) NAO sao geridos por beta-up/beta-down —"
	@echo "  ver docs/ops/beta-local.md, secao 'Console do anunciante (manual)'."

## beta-logs: tail dos logs de decision+collector (FOLLOW=1 para -f, senao mostra as ultimas linhas)
beta-logs:
	@if [ ! -f "$(BETA_LOG_DECISION)" ] && [ ! -f "$(BETA_LOG_COLLECTOR)" ]; then \
	  echo "beta-logs: nenhum log em $(BETA_DIR)/ ainda — rode 'make beta-up' primeiro."; \
	  exit 1; \
	fi
	@if [ "$(FOLLOW)" = "1" ]; then \
	  echo "== beta-logs: seguindo (Ctrl-C para sair) =="; \
	  tail -n 20 -f "$(BETA_LOG_DECISION)" "$(BETA_LOG_COLLECTOR)" 2>/dev/null; \
	else \
	  echo "== beta-logs: ultimas 40 linhas (use FOLLOW=1 para -f) =="; \
	  for f in "$(BETA_LOG_DECISION)" "$(BETA_LOG_COLLECTOR)"; do \
	    if [ -f "$$f" ]; then \
	      echo ""; echo "---- $$f ----"; \
	      tail -n 40 "$$f"; \
	    fi; \
	  done; \
	fi

## beta-check: smoke E2E do perfil beta — /v1/decide -> /lg -> /ck -> evento em stats.events_raw
beta-check:
	@if ! command -v psql >/dev/null 2>&1; then echo "ERRO: psql nao encontrado no PATH."; exit 1; fi
	@if ! command -v curl >/dev/null 2>&1; then echo "ERRO: curl nao encontrado no PATH."; exit 1; fi
	@if ! command -v jq   >/dev/null 2>&1; then echo "ERRO: jq nao encontrado no PATH."; exit 1; fi
	@echo "== beta-check: pre-condicao — decision + collector no ar =="
	@if ! curl -sf --max-time 2 "$(BETA_DECISION_URL)/healthz" >/dev/null 2>&1; then \
	  echo "FALHOU na etapa: pre-condicao (decision) — $(BETA_DECISION_URL)/healthz nao responde. Rode 'make beta-up' primeiro."; \
	  exit 1; \
	fi
	@if ! curl -sf --max-time 2 "$(BETA_COLLECTOR_URL)/healthz" >/dev/null 2>&1; then \
	  echo "FALHOU na etapa: pre-condicao (collector) — $(BETA_COLLECTOR_URL)/healthz nao responde. Rode 'make beta-up' primeiro."; \
	  exit 1; \
	fi
	@echo "== beta-check: etapa decide (reusa deploy/local/smoke.sh — cobre BR=CONTRACT / US=REMNANT) =="
	@DECISION_URL="$(BETA_DECISION_URL)" PGURL="$(BETA_LOADER_DSN)" bash deploy/local/smoke.sh \
	  || { echo "FALHOU na etapa: decide (deploy/local/smoke.sh) — ver saida acima."; exit 1; }
	@echo "== beta-check: etapa pixel+clique (/lg -> /ck, smoke.sh nao cobre isso) =="
	@zone=$$(psql "$(BETA_LOADER_DSN)" -tAc "select id from config.zones where name='Sidebar 300x250' limit 1" | tr -d '[:space:]'); \
	 if [ -z "$$zone" ]; then \
	   echo "FALHOU na etapa: resolucao de zona — seed 'Sidebar 300x250' nao encontrado em config.zones ($(BETA_DB))."; \
	   exit 1; \
	 fi; \
	 uid="beta-check-$$(date +%s)-$$RANDOM"; \
	 resp=$$(curl -sf --max-time 5 -X POST "$(BETA_DECISION_URL)/v1/decide" \
	   -H 'content-type: application/json' \
	   -d "{\"zone_id\":\"$$zone\",\"geo_country\":\"BR\",\"user_id\":\"$$uid\"}") \
	   || { echo "FALHOU na etapa: decide (POST /v1/decide para /lg+/ck) — decision nao respondeu."; exit 1; }; \
	 did=$$(echo "$$resp"  | jq -r '.decision_id // empty'); \
	 bid=$$(echo "$$resp"  | jq -r '.banner_id   // empty'); \
	 cid=$$(echo "$$resp"  | jq -r '.campaign_id // empty'); \
	 tid=$$(echo "$$resp"  | jq -r '.tenant_id   // empty'); \
	 tier=$$(echo "$$resp" | jq -r '.served_tier // empty'); \
	 tok=$$(echo "$$resp"  | jq -r '.click_tok   // empty'); \
	 if [ -z "$$did" ] || [ -z "$$bid" ] || [ -z "$$tok" ]; then \
	   echo "FALHOU na etapa: decide nao retornou banner_id/click_tok para BR — resposta: $$resp"; \
	   exit 1; \
	 fi; \
	 echo "  decide: decision_id=$$did banner_id=$$bid tier=$$tier"; \
	 lg_code=$$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
	   "$(BETA_COLLECTOR_URL)/lg?did=$$did&bid=$$bid&cid=$$cid&zid=$$zone&tid=$$tid&tier=$$tier"); \
	 if [ "$$lg_code" != "200" ]; then \
	   echo "FALHOU na etapa: pixel (/lg) — esperava HTTP 200, recebeu $$lg_code"; \
	   exit 1; \
	 fi; \
	 echo "  pixel (/lg): HTTP 200"; \
	 ck_code=$$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
	   "$(BETA_COLLECTOR_URL)/ck?tok=$$(printf '%s' "$$tok" | jq -sRr @uri)&did=$$did&bid=$$bid&cid=$$cid&zid=$$zone&tid=$$tid"); \
	 if [ "$$ck_code" != "302" ]; then \
	   echo "FALHOU na etapa: clique (/ck) — esperava HTTP 302, recebeu $$ck_code (token HMAC invalido/expirado ou CK_HMAC_SECRET divergente entre decision e collector)"; \
	   exit 1; \
	 fi; \
	 echo "  clique (/ck): HTTP 302"; \
	 echo "== beta-check: etapa persistencia (stats.events_raw) =="; \
	 exists_out=$$(psql "$(BETA_LOADER_DSN)" -tAc "select to_regclass('stats.events_raw')" 2>&1); exists_rc=$$?; \
	 if [ $$exists_rc -ne 0 ]; then \
	   echo "RESULTADO beta-check: decide=PASS pixel(/lg)=PASS clique(/ck)=PASS evento-em-stats.events_raw=FALHOU(erro de consulta)"; \
	   echo "FALHOU na etapa: persistencia (stats.events_raw) — psql retornou erro: $$exists_out"; \
	   exit 1; \
	 fi; \
	 exists=$$(echo "$$exists_out" | tr -d '[:space:]'); \
	 if [ -z "$$exists" ]; then \
	   echo "RESULTADO beta-check: decide=PASS pixel(/lg)=PASS clique(/ck)=PASS evento-em-stats.events_raw=PENDENTE(schema stats.events_raw ainda nao existe em $(BETA_DB) — dono: collector/telemetry, contrato TELEMETRY_PG_DSN)"; \
	   echo "FALHOU na etapa: persistencia (stats.events_raw) — tabela nao existe ainda."; \
	   exit 1; \
	 fi; \
	 sleep 1; \
	 : "ANTI-TAUTOLOGIA (achado do tech-lead na barreira da onda 'perfil BETA'):"; \
	 : "a versao anterior fazia 'select count(*) from stats.events_raw' SEM filtro e"; \
	 : "passava se o total fosse > 0. Provado tautologico: com REVOKE INSERT na role"; \
	 : "do writer, /lg e /ck respondiam 200/302, ZERO linhas novas eram gravadas, e o"; \
	 : "gate ainda dizia PASS(2 linhas) — ele media linhas de execucoes ANTERIORES."; \
	 : "Agora asserimos o decision_id DESTA execucao. Nenhuma linha antiga satisfaz."; \
	 : "Alem disso lemos o DSN que o processo NO AR realmente usa (.beta/collector.env),"; \
	 : "porque o gate antigo, apontado para outro banco, acusava 'collector nao grava'"; \
	 : "quando o collector gravava perfeitamente — em outro lugar."; \
	 check_dsn="$(BETA_LOADER_DSN)"; \
	 if [ -f "$(BETA_ENV_COLLECTOR)" ]; then \
	   run_dsn=$$(sed -n 's/^TELEMETRY_PG_DSN=//p' "$(BETA_ENV_COLLECTOR)" | head -1); \
	   run_db=$$(printf '%s' "$$run_dsn" | sed -n 's#.*/\([^/?]*\)?.*#\1#p'); \
	   if [ -n "$$run_db" ] && [ "$$run_db" != "$(BETA_DB)" ]; then \
	     echo "FALHOU na etapa: persistencia — o collector NO AR grava em '$$run_db', mas beta-check consulta '$(BETA_DB)'."; \
	     echo "  Reconcilie: 'make beta-up BETA_DB=$$run_db' ou 'make beta-check BETA_DB=$$run_db'."; \
	     exit 1; \
	   fi; \
	 fi; \
	 q="select count(*) from stats.events_raw where decision_id = '"$$did"'"; \
	 count_out=$$(psql "$$check_dsn" -tAc "$$q" 2>&1); count_rc=$$?; \
	 if [ $$count_rc -ne 0 ]; then \
	   echo "RESULTADO beta-check: decide=PASS pixel(/lg)=PASS clique(/ck)=PASS evento-em-stats.events_raw=FALHOU(erro de leitura)"; \
	   echo "FALHOU na etapa: persistencia (stats.events_raw) — erro ao consultar: $$count_out"; \
	   exit 1; \
	 fi; \
	 count=$$(echo "$$count_out" | tr -d '[:space:]'); \
	 if [ -z "$$count" ] || [ "$$count" = "0" ]; then \
	   echo "RESULTADO beta-check: decide=PASS pixel(/lg)=PASS clique(/ck)=PASS evento-em-stats.events_raw=FALHOU(0 linhas para decision_id=$$did)"; \
	   echo "FALHOU na etapa: persistencia — /lg e /ck responderam, mas NENHUM evento desta execucao chegou a stats.events_raw."; \
	   echo "  Causa provavel: grant/role do writer, DSN divergente, ou fila do pgsink descartando. Veja: make beta-logs"; \
	   exit 1; \
	 fi; \
	 echo "  stats.events_raw: $$count linha(s) com decision_id=$$did — OK"; \
	 echo "RESULTADO beta-check: decide=PASS pixel(/lg)=PASS clique(/ck)=PASS evento-em-stats.events_raw=PASS($$count linhas DESTA execucao)"; \
	 echo "== beta-check: PASS =="
