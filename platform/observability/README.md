# Observabilidade — OpenTelemetry (TX-5)

100% OpenTelemetry no alvo (`docs/stack-tecnologico.md` §2.7): **VictoriaMetrics/
Mimir** (métricas) + **Grafana** + **Loki** (logs) + **Tempo** (traces). Este
diretório entrega o artefato **bloqueante da Fase 0**: a config do **OTel
Collector com redação de PII** (TX-5).

## [`otel-collector.yaml`](otel-collector.yaml) — gate de privacidade (TX-5)

A regra de TX-5 é categórica: **"OTel Collector redige PII antes de qualquer
export"** e o **IP é descartado** após derivar geo. A config implementa a barreira
em profundidade:

1. `transform/redact-pii` — **deleta** atributos de IP (`client.address`,
   `http.client_ip`, `net.peer.ip`, …), identificadores de pessoa
   (`enduser.id`, `user.email`, …) e **hasheia** `capping.key` (efêmera, TX-5).
2. `redaction/allowlist` — **fail-closed**: só uma allow-list de atributos
   não-PII sobrevive (`tenant_id` pseudônimo, `decision_id`, `geo.country/city`
   mínimos, etc.). Qualquer chave nova é bloqueada por padrão.
3. `blocked_values` — mascara valores que casem IPv4/e-mail caso vazem como valor.

> O IP bruto **não deveria** chegar ao collector (o serviço o descarta após o
> GeoLite2); a redação aqui é a **segunda linha** — privacy by design + defense in
> depth.

## A crescer (Fase 1+)

- Manifests de implantação do collector (DaemonSet de borda + Deployment de
  gateway) via Argo CD.
- Backends (VictoriaMetrics, Loki, Tempo, Grafana) como `Application`s GitOps.
- `otelcol validate --config` no CI de imagem do collector (valida semântica
  completa contra a distro empacotada).
- Langfuse **self-hosted** para observabilidade do copiloto (TX-5), separado deste
  pipeline de infra/app.
