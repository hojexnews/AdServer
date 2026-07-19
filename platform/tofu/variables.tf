variable "region" {
  description = "Região AWS do control plane (EKS/ML/batch)."
  type        = string
  default     = "us-east-1"
}

variable "environment" {
  description = "Ambiente (dev|staging|prod)."
  type        = string
  default     = "dev"

  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "environment deve ser dev, staging ou prod."
  }
}

variable "cluster_name" {
  description = "Nome do cluster EKS."
  type        = string
  default     = "hojex-adserver"
}

variable "kubernetes_version" {
  description = "Versão do control plane EKS."
  type        = string
  default     = "1.30"
}

variable "cells" {
  description = "Células de isolamento (TX-3/§2.7). PCI e AML/KYC são de escopo mínimo."
  type        = list(string)
  default     = ["platform", "delivery", "ml", "data", "pci", "aml-kyc"]
}

variable "tags" {
  description = "Tags padrão aplicadas a todo recurso."
  type        = map(string)
  default = {
    "app"        = "hojex-adserver"
    "managed-by" = "opentofu"
  }
}
