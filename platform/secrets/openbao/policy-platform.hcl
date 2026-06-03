# Política OpenBao — célula platform/delivery/ml/data.
# Menor privilégio: lê só os segredos do próprio escopo e gera credenciais
# DINÂMICAS de banco (sem segredo estático em imagem/git — §2.7).

# Segredos estáticos da plataforma (KV v2), só leitura.
path "secret/data/platform/*" {
  capabilities = ["read"]
}

# Credenciais dinâmicas de Postgres (TTL curto, rotação automática).
path "database/creds/adserver-app" {
  capabilities = ["read"]
}

# Renovar/revogar o próprio lease (não outros).
path "sys/leases/renew" {
  capabilities = ["update"]
}
path "sys/leases/revoke" {
  capabilities = ["update"]
}

# NEGADO por ausência: qualquer path de pci/* e aml/* (isolamento de célula).
