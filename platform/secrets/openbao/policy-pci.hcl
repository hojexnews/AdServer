# Política OpenBao — célula PCI (escopo mínimo, §2.7).
# Acesso EXCLUSIVO ao trilho de pagamento fiat. Nenhum cruzamento com as
# demais células. As chaves de pagamento usam KMS/HSM (não KV) — esta
# política cobre só os segredos de aplicação do trilho PCI.

# Segredos do trilho de pagamento (Stripe/Asaas tokens via KV v2), só leitura.
path "secret/data/pci/payments/*" {
  capabilities = ["read"]
}

# Credenciais dinâmicas do banco da célula PCI (isolado do ledger geral).
path "database/creds/pci-payments" {
  capabilities = ["read"]
}

# Transit para criptografia de dados em repouso do trilho (envelope encryption).
path "transit/encrypt/pci" {
  capabilities = ["update"]
}
path "transit/decrypt/pci" {
  capabilities = ["update"]
}

# NEGADO por ausência: secret/data/platform/*, database/creds/adserver-app,
# e tudo de aml/*. A célula PCI não enxerga segredo de nenhuma outra.
