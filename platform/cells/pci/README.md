# Célula PCI — Stripe SAQ-A (ADR-0004 §F / K4)

## Fronteira PCI validável por QSA

Esta célula é o **escopo mínimo SAQ-A** do AdServer. O Stripe Self-Assessment
Questionnaire A (SAQ-A) cobre merchants que **delegam toda a captura de dados
de cartão ao Stripe** via Elements/Checkout. Isso significa:

**O número de cartão (PAN) e os dados sensíveis de autenticação (SAD) NUNCA
transitam pelo backend do AdServer.** O fluxo é:

```
Navegador do anunciante
  |
  |-- Stripe.js / Elements (JS do Stripe, carregado de js.stripe.com)
  |       Captura PAN/CVV diretamente no iframe do Stripe (domínio Stripe)
  |       Cria PaymentMethod token (pm_*) no Stripe
  |
  |-- Frontend AdServer (BFF)
  |       Recebe apenas: pm_* ou PaymentIntent client_secret
  |       Nunca vê PAN, CVV, dados do cartão
  |
  |-- services/payments (namespace pci, este arquivo)
          Recebe apenas: PaymentMethodId (pm_*) ou PaymentIntentId (pi_*)
          Confirma PaymentIntent via api.stripe.com com STRIPE_SECRET_KEY
          Nunca vê PAN — conforms com SAQ-A Req. 2.1
```

### O que está em escopo PCI (dentro desta célula)

| Componente | Localização | Justificativa |
|---|---|---|
| `services/payments` | namespace `pci` | Tem acesso à STRIPE_SECRET_KEY; faz chamadas para `api.stripe.com` |
| `STRIPE_SECRET_KEY` | OpenBao `pci/stripe/secret_key` | Chave de API do Stripe; controla criação/captura de PaymentIntents |
| `STRIPE_WEBHOOK_SECRET` | OpenBao `pci/stripe/webhook_secret` | Valida assinatura HMAC dos webhooks do Stripe |
| Endpoint `/webhooks/stripe` | Gateway API (HTTPRoute) | Recebe eventos Stripe; validado por HMAC antes de processar |

### O que está FORA de escopo PCI

| Componente | Motivo |
|---|---|
| Frontend / BFF | Não armazena nem transmite PAN; usa Stripe.js (iframe Stripe) |
| `services/decision` (hot path) | Nunca chamado pela célula PCI; sem dependência de pagamento |
| `namespace delivery` | Completamente isolado; Cilium deny-all bloqueia qualquer rota |
| `namespace aml-kyc` | Célula separada; sem rota direta com `pci` |
| Ledger / banco de dados | Armazena apenas PaymentIntent IDs e status — não PAN |
| Telemetria / logs | PII/PAN redigido pelo OTel Collector (TX-5) antes de qualquer export |

## Arquitetura de rede (validável por QSA)

```
Internet / Cloudflare (TLS)
  |
  v
Envoy Gateway (namespace: platform-system)
  | HTTPRoute: POST /webhooks/stripe apenas
  v
[namespace: pci] ---- Cilium deny-all (default-deny.yaml)
  |
  +-- Egress permitido (CiliumNetworkPolicy allow-egress-stripe.yaml):
  |     api.stripe.com:443       (Payment Intents, Billing, Tax)
  |     hooks.stripe.com:443     (verificação SDK)
  |     files.stripe.com:443     (Tax/Billing PDFs)
  |
  +-- Egress permitido (allow-egress-openbao.yaml):
  |     platform-system/openbao:8200 (busca STRIPE_SECRET_KEY via Vault Agent)
  |
  +-- DNS (allow-dns.yaml, K0):
  |     kube-system/kube-dns:53
  |
  +-- Ingress permitido (allow-ingress-stripe-webhook.yaml):
        platform-system/envoy-gateway -> payments:8080

Bloqueado por deny-all (sem exceção):
  - delivery -> pci          (hot path nunca chama PCI)
  - pci -> delivery          (PCI não influencia decisão)
  - pci -> aml-kyc           (células separadas, sem rota direta)
  - pci -> internet (outros) (sem egress genérico)
```

## Segredos — onde vivem e como chegam ao Pod

**NENHUM SEGREDO EM GIT, IMAGEM OU KUBERNETES SECRET ESTÁTICO.**

| Segredo | Path no OpenBao | Como chega ao Pod |
|---|---|---|
| `STRIPE_SECRET_KEY` | `pci/stripe/secret_key` | Vault Agent Sidecar → `/vault/secrets/stripe-secret-key` |
| `STRIPE_WEBHOOK_SECRET` | `pci/stripe/webhook_secret` | Vault Agent Sidecar → `/vault/secrets/stripe-webhook-secret` |

O Pod `payments` usa o ServiceAccount `payments` (namespace `pci`), que é
mapeado para o role `pci-payments` no OpenBao via Kubernetes Auth Method.
O OpenBao policy `policy-pci.hcl` restringe leitura a `pci/stripe/*` apenas.
O backend KV do OpenBao usa envelope KMS (AWS KMS / GCP KMS) para criptografia
em repouso (§2.7 — chaves de pagamento em KMS/HSM).

A policy Kyverno `exigir-vault-agent-pci` rejeita no admission qualquer Pod
sem a anotação `vault.hashicorp.com/agent-inject: "true"`, tornando o
Vault Agent obrigatório estruturalmente.

## Controles Kyverno na célula (platform/cells/pci/policy/)

| Policy | Tipo | Efeito |
|---|---|---|
| `proibir-secret-estatico-pci` | Enforce | Rejeita Pod com `envFrom.secretRef` ou `env.valueFrom.secretKeyRef` |
| `exigir-vault-agent-pci` | Enforce | Rejeita Pod sem anotação `vault.hashicorp.com/agent-inject: "true"` |
| `proibir-label-delivery-em-pci` | Enforce | Rejeita Pod com label `adserver.hojex/cell=delivery` (anticollision) |

Mais o ClusterPolicy baseline (platform/k8s/policy/kyverno-baseline.yaml):
imagens assinadas (cosign), sem tag `:latest`, limites de recurso obrigatórios.

## Como o QSA valida esta fronteira

1. **Rede:** `kubectl get ciliumnetworkpolicies -n pci` → lista apenas as
   políticas de allowlist explícitas (allow-egress-stripe, allow-ingress-stripe-webhook,
   allow-egress-openbao, allow-dns). Nenhuma abre egress genérico.

2. **Segredos:** `kubectl get secrets -n pci` → zero Kubernetes Secrets (a
   política Kyverno impede criação; o Vault Agent não cria K8s Secrets).

3. **Fluxo de dados:** penetration test no endpoint do webhook confirma que
   o servidor responde apenas a `POST /webhooks/stripe` com header
   `Stripe-Signature` válido. Qualquer outro path retorna 404/403 via Envoy.

4. **Isolamento do hot path:** `kubectl get ciliumnetworkpolicies -n delivery`
   confirma que não existe nenhuma regra de ingress/egress para o namespace `pci`.

5. **Supply chain:** `cosign verify ghcr.io/hojex/payments:<tag>` confirma
   assinatura; Trivy SBOM no CI confirma ausência de CVE crítico.

## Dependências e o que é placeholder vs. completo

| Item | Status |
|---|---|
| NetworkPolicies Cilium (deny-all, DNS, egress-stripe, ingress-webhook, egress-openbao) | Completo |
| HTTPRoute Gateway API | Completo; hostname `payments.hojexnews.com.br` é PLACEHOLDER |
| ServiceAccount `payments` | Completo |
| ConfigMap de referência Vault Agent | Completo (documentação de contrato) |
| Políticas Kyverno | Completo |
| RBAC | Completo; grupo OIDC `hojex:payments-compliance` é PLACEHOLDER |
| OpenBao policies (`policy-pci.hcl`) | Existente desde K0 (platform/secrets/openbao/) |
| Deployment `payments` real | Fora de escopo (services/payments — outro engenheiro) |
| Chaves vivas no OpenBao | Pende de conta cloud real (sandbox Stripe funciona) |
| Conta cloud separada para a célula PCI | Pende de provisionamento (ADR-0004 §F: "conta cloud separada") |
