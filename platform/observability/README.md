# Observabilidade — OpenTelemetry (TX-5)

100% OpenTelemetry no alvo (`docs/stack-tecnologico.md` §2.7): **VictoriaMetrics/
Mimir** (métricas) + **Grafana** + **Loki** (logs) + **Tempo** (traces). Este
diretório entrega o artefato **bloqueante da Fase 0**: a config do **OTel
Collector com redação de PII** (TX-5).

## [`otel-collector.yaml`](otel-collector.yaml) — gate de privacidade (TX-5)

A regra de TX-5 é categórica: **"OTel Collector redige PII antes de qualquer
export"** e o **IP é descartado** após derivar geo. A config implementa a barreira
em profundidade com dois estágios sequenciais em todos os pipelines com atributos
livres (traces e logs):

### Estágio 1 — `transform/redact-pii` (delete por chave conhecida)

Remove explicitamente atributos que carregam identidade direta, cobrindo as mesmas
chaves em traces e logs:

| Atributo removido | Motivo |
| --- | --- |
| `client.address`, `http.client_ip`, `net.sock.peer.addr`, `net.peer.ip` | IP do cliente (PII direta) |
| `url.full` | querystring pode conter PII |
| `enduser.id`, `user.id`, `user.email` | identificadores diretos de pessoa |
| `capping.key` | hasheada (SHA-256) antes de sobreviver |

### Estágio 2 — `redaction/allowlist-traces` / `redaction/allowlist-logs` (fail-closed)

**Postura idêntica** nos dois pipelines: `allow_all_keys: false`. Qualquer chave
não listada — incluindo chaves futuras/inesperadas — é **descartada antes do
export**. Os `blocked_values` (IPv4 e e-mail por regex) mascaram valores PII
que eventualmente escapem como conteúdo de atributo permitido.

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

### Ordem no pipeline de logs

```text
memory_limiter -> transform/redact-pii -> redaction/allowlist-logs -> batch -> loki
```

`transform/redact-pii` primeiro (elimina por chave conhecida, rápido); depois
`redaction/allowlist-logs` (falha-fechado — qualquer chave residual não listada
é descartada). O `batch` fica depois de toda redação, nunca antes.

### Métricas — sem allowlist de atributo

O pipeline de métricas não usa `redaction/allowlist` porque métricas OTLP têm
atributos controlados pelos exporters da aplicação (labels de séries temporais),
cujo conjunto é fechado e revisado no código. Nenhuma métrica carrega payload
livre. Se isso mudar, a allowlist deve ser adicionada.

## A crescer (Fase 1+)

- Manifests de implantação do collector (DaemonSet de borda + Deployment de
  gateway) via Argo CD.
- Backends (VictoriaMetrics, Loki, Tempo, Grafana) como `Application`s GitOps.
- `otelcol validate --config` no CI de imagem do collector (valida semântica
  completa contra a distro empacotada).
- Langfuse **self-hosted** para observabilidade do copiloto (TX-5), separado deste
  pipeline de infra/app.
