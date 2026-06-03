# `platform/` — plataforma-base como código (Fase 0)

A Fase 0 lista a **plataforma base** (EKS + OpenTofu + Argo CD + Cilium + OTel +
OpenBao). Aplicá-la exige uma conta cloud; **autorá-la, não**. Esta árvore é o
**código** dessa plataforma — validável e revisável agora, pronto para `apply`
quando houver cloud. Segue o princípio condutor do design: **enxuto e correto**,
cresce sob medição (`docs/stack-tecnologico.md` §2.7; "evitar no início:
Crossplane e vCluster").

> **Status honesto:** isto é um **esqueleto verificado**, não um cluster pronto.
> Cada subpasta marca o que está autorado vs. o que é stub a crescer. Nada aqui
> cria recurso cloud sozinho.

## O que está autorado vs. o que precisa de cloud

| Componente | Autorado agora | Precisa de cloud p/ aplicar |
|------------|----------------|------------------------------|
| **OpenTofu** ([`tofu/`](tofu/)) | Pins de provider, backend (template), variáveis, células, plano de módulos; `tofu validate` passa | Conta AWS, bucket de state, módulos EKS reais |
| **GitOps/Argo CD** ([`gitops/`](gitops/)) | `AppProject` + app-of-apps (bootstrap) | Cluster + Argo CD instalado |
| **Kubernetes baseline** ([`k8s/`](k8s/)) | Namespaces por célula, **Cilium deny-all**, RBAC, Kyverno baseline | Cluster com Cilium + Kyverno |
| **Observabilidade** ([`observability/`](observability/)) | **OTel Collector com redação de PII** (TX-5) — config real | Coletor implantado + backends (VM/Loki/Tempo) |
| **Segredos** ([`secrets/openbao/`](secrets/openbao/)) | Políticas OpenBao + plano de auth k8s/Pod Identity | OpenBao implantado + KMS |

## Modelo de células (TX-3 / §2.7)

Isolamento por **células** com `deny-all` de rede (Cilium) e contas/escopo
mínimos. Os namespaces em [`k8s/namespaces.yaml`](k8s/namespaces.yaml) carregam
`label` de célula:

| Célula | Namespace | Conteúdo | Isolamento |
|--------|-----------|----------|------------|
| `platform` | `platform-system` | Argo CD, OTel, OpenBao, ingress | base |
| `delivery` | `delivery` | motor Go (hot path), collectors lg/ck/ct | deny-all + egress mínimo |
| `ml` | `ml` | serving GBDT (sidecar), MLflow, treino | deny-all |
| `data` | `data` | Redpanda, ClickHouse, conectores Iceberg | deny-all |
| **`pci`** | `pci` | trilho de pagamento fiat (escopo PCI SAQ-A) | **célula isolada**, conta/Cilium deny-all |
| **`aml`** | `aml` | KYC/Travel Rule/screening cripto | **célula isolada** |

> PCI e AML são **células de escopo mínimo** (§2.7). No alvo, vivem em **contas
> cloud separadas**; aqui ficam como namespaces rotulados para o baseline de rede.

## Ordem de aplicação (quando houver cloud)

1. `tofu/` — provisiona VPC/EKS/addons (e os buckets de state primeiro, fora deste root).
2. Instalar Cilium (deny-all), Argo CD, OpenBao, OTel Collector (via addons/Helm).
3. `gitops/bootstrap` — Argo CD assume o resto (app-of-apps) por GitOps.
4. Aplicar `k8s/` (namespaces, netpol, rbac, policy) e `observability/`,
   `secrets/` via Argo CD.

## Supply chain (resumo, §2.7)

`cosign` (assinatura) + **SBOM** + **Kyverno** (admission: imagens assinadas, sem
`:latest`, limites obrigatórios — [`k8s/policy/`](k8s/policy/)) + Trivy (scan) +
Falco (runtime). Os controles de admissão estão como baseline Kyverno; scanners
entram no CI de imagem (Fase 1, quando houver imagem).
