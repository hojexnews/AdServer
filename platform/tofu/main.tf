# Root module da plataforma-base. ESQUELETO: provider + locais + plano de
# módulos. Os recursos cloud reais (VPC/EKS/addons) entram nos módulos de
# `modules/` na Fase 1 — mantidos fora daqui para o root permanecer
# `tofu validate`-limpo sem credenciais.

provider "aws" {
  region = var.region

  default_tags {
    tags = merge(var.tags, { environment = var.environment })
  }
}

locals {
  name = "${var.cluster_name}-${var.environment}"

  # Namespaces por célula (espelha platform/k8s/namespaces.yaml).
  cell_namespaces = {
    platform = "platform-system"
    delivery = "delivery"
    ml       = "ml"
    data     = "data"
    pci      = "pci"
    aml-kyc  = "aml-kyc"
  }

  # Células de compliance recebem isolamento reforçado (conta separada no alvo).
  compliance_cells = ["pci", "aml-kyc"]
}

# ----------------------------------------------------------------------------
# PLANO DE MÓDULOS (a implementar na Fase 1, quando houver cloud):
#
#   module "network" {            # VPC, subnets, NAT, flow logs
#     source = "./modules/network"
#   }
#   module "eks" {                # cluster EKS, node groups, IRSA/Pod Identity
#     source             = "./modules/eks"
#     cluster_name       = local.name
#     kubernetes_version = var.kubernetes_version
#     vpc_id             = module.network.vpc_id
#   }
#   module "addons" {             # Cilium, Argo CD, Kyverno, OpenBao, OTel
#     source       = "./modules/addons"
#     cluster_name = module.eks.cluster_name
#   }
#
# Sugestão: usar terraform-aws-modules/{vpc,eks}/aws como base dos dois
# primeiros; addons via provider helm. Cilium substitui o CNI default
# (deny-all — TX-3). Ver modules/README.md.
# ----------------------------------------------------------------------------
