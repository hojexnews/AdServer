# `modules/` — plano de módulos OpenTofu (a implementar na Fase 1)

Os módulos reais ficam aqui quando houver cloud. Mantidos como **plano** para o
root (`../`) permanecer `tofu validate`-limpo sem credenciais.

| Módulo | Responsabilidade | Base sugerida |
|--------|------------------|---------------|
| `network/` | VPC, subnets pública/privada, NAT, VPC flow logs. Conta/VPC **separada** para a célula PCI. | `terraform-aws-modules/vpc/aws` |
| `eks/`     | Cluster EKS, node groups (control plane/ML/batch), IRSA/**Pod Identity**, OIDC. | `terraform-aws-modules/eks/aws` |
| `addons/`  | **Cilium** (substitui o CNI, deny-all — TX-3), **Argo CD**, **Kyverno**, **OpenBao**, **OTel Collector**, Gateway API (Envoy Gateway), cert-manager. | provider `helm` |

## Princípios (§2.7)

- **Hot path na borda:** o motor Go roda em VMs/PoPs atrás de CDN Anycast
  (Cloudflare), **não** no EKS. O EKS é control plane / ML / batch.
- **Evitar no início:** Crossplane e vCluster (over-engineering para o estágio).
- **Células de compliance** (`pci`, `aml`) → no alvo, **contas AWS separadas**;
  o módulo `network` recebe um root/workspace próprio por célula.
- **Cilium deny-all** é configurado no addon e reforçado pelas
  `CiliumNetworkPolicy` em [`../../k8s/netpol/`](../../k8s/netpol/).
