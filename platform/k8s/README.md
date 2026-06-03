# Kubernetes baseline (TX-3 / §2.7)

Manifests de **isolamento e admissão** aplicados sobre o cluster (via Argo CD).
Esqueleto verificado da Fase 0 — cresce por app na Fase 1.

| Caminho | O que é | Âncora |
|---------|---------|--------|
| [`namespaces.yaml`](namespaces.yaml) | Namespaces por **célula** (platform/delivery/ml/data/**pci**/**aml**), rotulados + Pod Security `restricted`. | TX-3, §2.7 |
| [`netpol/cilium-default-deny.yaml`](netpol/cilium-default-deny.yaml) | **Cilium deny-all** por célula de carga e compliance. | TX-3 ("Cilium deny-all") |
| [`netpol/allow-dns.yaml`](netpol/allow-dns.yaml) | Egress mínimo de DNS sobre o deny-all (senão nada resolve). | TX-3 |
| [`policy/kyverno-baseline.yaml`](policy/kyverno-baseline.yaml) | Admissão: imagens **assinadas (cosign)**, sem `:latest`, **limites obrigatórios**. | §2.7 (supply chain) |
| [`rbac/baseline.yaml`](rbac/baseline.yaml) | RBAC menor-privilégio (Role `viewer` por célula). | TX-3 |

## Como o deny-all funciona

Uma `CiliumNetworkPolicy` com `endpointSelector: {}` e `ingress: []`/`egress: []`
seleciona **todos** os endpoints do namespace e nega tudo. A partir daí, cada
fluxo legítimo é aberto por uma policy de **allow explícito** (começando pelo
DNS). É o oposto do default-allow do Kubernetes puro — **fail-closed de rede**.

## A crescer (Fase 1)

- Allow-policies por app (delivery↔Redpanda, ml↔delivery via socket, etc.).
- NetworkPolicies de saída para CDN/cofre/custodiante nas células pci/aml.
- Gateway API (Envoy Gateway) para ingress norte-sul.
- Validação `kubeconform`/`kyverno test` no CI (requer schemas das CRDs).
