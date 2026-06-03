output "name" {
  description = "Nome base dos recursos (cluster-ambiente)."
  value       = local.name
}

output "region" {
  description = "Região AWS."
  value       = var.region
}

output "cell_namespaces" {
  description = "Mapa célula → namespace Kubernetes."
  value       = local.cell_namespaces
}

output "compliance_cells" {
  description = "Células de compliance (isolamento reforçado)."
  value       = local.compliance_cells
}
