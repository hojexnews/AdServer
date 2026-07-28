# Observabilidade — OpenTelemetry (TX-5)

100% OpenTelemetry no alvo (`docs/stack-tecnologico.md` §2.7): **VictoriaMetrics/
Mimir** (métricas) + **Grafana** + **Loki** (logs) + **Tempo** (traces). Este
diretório entrega o artefato **bloqueante da Fase 0**: a config do **OTel
Collector com redação de PII** (TX-5).

## [`otel-collector.yaml`](otel-collector.yaml) — gate de privacidade (TX-5)

A regra de TX-5 é categórica: **"OTel Collector redige PII antes de qualquer
export"** e o **IP é descartado** após derivar geo. A config implementa a barreira
em profundidade com dois estágios sequenciais nos **três** pipelines de sinal —
traces, logs **e metrics** (achado `otel-metrics-pipeline-sem-redacao-e-sem-gate`,
30ª onda de auditoria adversarial: o pipeline metrics não tinha nenhuma redação
até então, e o exporter `prometheusremotewrite` promove atributos de datapoint a
LABELS exportados — exatamente o mesmo risco de vazamento que traces/logs):

### Estágio 1 — `transform/redact-pii` (delete por chave conhecida)

Remove explicitamente atributos que carregam identidade direta, cobrindo as mesmas
chaves em traces, logs e metrics (contexto `datapoint`):

| Atributo removido | Motivo |
| --- | --- |
| `client.address`, `http.client_ip`, `net.sock.peer.addr`, `net.peer.ip` | IP do cliente (PII direta) |
| `url.full` | querystring pode conter PII |
| `enduser.id`, `user.id`, `user.email` | identificadores diretos de pessoa |
| `capping.key` | hasheada (SHA-256) antes de sobreviver |

### Estágio 2 — `redaction/allowlist-traces` / `-logs` / `-metrics` (fail-closed)

**Postura idêntica** nos três pipelines: `allow_all_keys: false`. Qualquer chave
não listada — incluindo chaves futuras/inesperadas — é **descartada antes do
export**. Os `blocked_values` (IPv4 e e-mail por regex) mascaram valores PII
que eventualmente escapem como conteúdo de atributo permitido. A allowlist de
metrics é a mais restrita das três (só dimensões de agregação — sem
`decision_id`/`geo.city`), pois datapoints viram labels de série temporal de
longa retenção no VictoriaMetrics/Mimir.

#### Paridade traces vs. logs

A allowlist de logs é distinta da de traces porque logs têm body livre (o texto
estruturado do log) e seus atributos têm semântica diferente de spans. A tabela
abaixo mostra a cobertura e justifica cada chave como não-PII:

| Chave | Traces | Logs | Justificativa de não-PII |
| --- | :---: | :---: | --- |
| `service.name` | sim | sim | identidade do serviço, não de pessoa |
| `service.namespace` | sim | sim | namespace Kubernetes, não de pessoa |
| `tenant_id` | sim | sim | pseudônimo opaco (TX-3), sem ligação a pessoa natural |
| `decision_id` | sim | sim | UUID de decisão de ad-serving, sem ligação a indivíduo |
| `zone_id` / `site_id` | sim | sim | identificadores de inventário publicitário |
| `served_tier` / `model_version` | sim | sim | metadados de ML e entrega, sem PII |
| `geo.country` | sim | sim | granularidade de país, derivado de IP já descartado |
| `geo.city` | sim | não | city está na allowlist de traces; omitida em logs por precaução adicional (logs menos estruturados) |
| `http.request.method` | sim | sim | verbo HTTP (GET/POST/...), sem PII |
| `http.response.status_code` | sim | sim | código de resposta, sem PII |
| `otel.status_code` | sim | sim | código de status interno do OTel |
| `severity_text` / `severity_number` | não | sim | nível do log (INFO/WARN/...), específico de logs |
| `error.type` / `exception.type` | não | sim | categoria de erro, sem payload de dados de usuário |
| `code.function` / `code.namespace` | não | sim | localização do log no código-fonte |
| `log.file.name` | não | sim | arquivo de origem do log, sem PII |

> A ausência de `geo.city` na allowlist de logs é uma escolha deliberada de
> defesa-em-profundidade: logs têm menos estrutura garantida que spans e o risco
> de `geo.city` ser preenchido com valor mais granular (subdivisão, bairro) por
> instrumentação futura é maior.

### Ordem nos três pipelines (mesma sequência, allowlist específica por tipo)

```text
traces:  memory_limiter -> transform/redact-pii -> redaction/allowlist-traces  -> batch -> tempo
logs:    memory_limiter -> transform/redact-pii -> redaction/allowlist-logs    -> batch -> loki
metrics: memory_limiter -> transform/redact-pii -> redaction/allowlist-metrics -> batch -> prometheusremotewrite
```

`transform/redact-pii` primeiro (elimina por chave conhecida, rápido); depois a
allowlist específica do pipeline (falha-fechado — qualquer chave residual não
listada é descartada). O `batch` fica depois de toda redação, nunca antes. O
gate `make platform-otel-validate` (via
[`otel-pipeline-redaction-check.py`](otel-pipeline-redaction-check.py)) é
**DEFAULT-DENY**: enumera todo `service.pipelines` da config real (não uma
lista hardcoded) e reprova qualquer pipeline — deste ou de qualquer nome
futuro, incluindo `<tipo>/<id>` como `traces/raw` — que não tenha as duas
barreiras cabeadas em `.processors`.

### Métricas — allowlist própria (achado corrigido na 30ª onda)

O pipeline de métricas **passou a ter** `transform/redact-pii` (contexto
`datapoint`) + `redaction/allowlist-metrics` (achado
`otel-metrics-pipeline-sem-redacao-e-sem-gate`, 30ª onda). A justificativa
anterior ("métricas têm atributos controlados pelo código, sem payload livre")
não se sustentava: o meter de tokens do copiloto
(`services/copilot/observability/langfuse_setup.py::record_otel_usage`) já
CHAMA `counter.add(...)` com um dict de atributos que pode receber chaves
novas sem revisão de PII — mesmo essa chamada sendo hoje um no-op (nenhum
`MeterProvider` com exporter OTLP está instalado; ver ressalva acima), o
código-fonte já contém o padrão de risco, e o dia em que alguém instalar
o provider é o dia em que o vazamento passa a valer sem aviso — o mesmo
risco de traces/logs, agora coberto pela mesma barreira antes que isso
aconteça.

## A crescer (Fase 1+)

- Manifests de implantação do collector (DaemonSet de borda + Deployment de
  gateway) via Argo CD.
- Backends (VictoriaMetrics, Loki, Tempo, Grafana) como `Application`s GitOps.
- `otelcol validate --config` no CI de imagem do collector (valida semântica
  completa contra a distro empacotada).
- **Gap arquitetural honesto — sinal por sinal (achados
  `otel-redacao-orfa-do-caminho-real-de-logs`, 30ª onda, e
  `otel-doc-lie-traces-metrics-vigentes`, 31ª onda de remediação):** os
  TRÊS pipelines de redação acima são barreiras estruturais PRONTAS, mas
  NENHUM dos três tem hoje um emissor real cabeado neste repositório —
  reverificado de 1ª mão por grep (sem resultado) por
  `OTLPLogExporter`/`LoggerProvider`/`OTLPSpanExporter`/
  `OTLPMetricExporter`/`TracerProvider`/`MeterProvider`/`otlptrace`/
  `otlpmetric`/`sdktrace`/`OTEL_EXPORTER_OTLP` em `services/`, `internal/`,
  `bff/src`, `web/console/src` e `go.mod`:
  - **logs**: structlog/slog escrevem em stdout; o collector só tem
    `receivers.otlp` (sem `filelog`) — nenhum log de aplicação chega aqui.
  - **traces**: nenhum serviço instancia um `TracerProvider` com exporter
    OTLP. `services/copilot/pyproject.toml` lista
    `opentelemetry-api/-sdk/-exporter-otlp` como dependência, mas a
    dependência nunca é chamada — nada invoca `set_tracer_provider`.
  - **metrics**: `services/copilot/observability/langfuse_setup.py::
    record_otel_usage` chama `opentelemetry.metrics.get_meter(...)` mas
    nunca configura um `MeterProvider` com `OTLPMetricExporter` — sem
    provider, a API usa o meter no-op global por padrão e `counter.add(...)`
    não exporta nada.

  Ou seja, a proteção existe na config para os três sinais, mas nenhum
  está no caminho real de nenhum log/trace/métrica de aplicação hoje.
  Cabear os serviços (instalar `TracerProvider`/`MeterProvider` com
  exporter OTLP apontando para este collector; e um receiver `filelog` ou
  emissor de log OTLP para logs) é trabalho pendente para os três — ver
  caveat idêntico no cabeçalho de [`otel-collector.yaml`](otel-collector.yaml).
  Nenhum dos três pipelines deve ser lido como "vigente em produção" só
  por esta config existir.
- Langfuse **self-hosted** para observabilidade do copiloto (TX-5), separado deste
  pipeline de infra/app.
