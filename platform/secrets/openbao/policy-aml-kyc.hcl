# Política OpenBao — célula AML/KYC/Travel Rule (escopo mínimo, §2.7,
# ADR-0004 §F / §E.10 / K6). Espelha o estilo de policy-pci.hcl: menor
# privilégio, um path por finalidade, DENY por omissão documentado.
#
# Acesso EXCLUSIVO aos segredos do trilho cripto/compliance (Sumsub KYC/KYB,
# Chainalysis KYT, custódia Safe multisig, cofre de compliance/PII
# pseudônimo). Nenhum cruzamento com a célula PCI nem com a plataforma
# geral. Reconcilia o gap citado em
# platform/cells/aml-kyc/secrets/openbao-auth.yaml (linha "policy-aml-kyc.hcl
# (platform/secrets/openbao/)") com os paths reais documentados naquele
# arquivo, refinados aqui em blocos granulares (em vez do wildcard
# informal "aml-kyc/*" mencionado como nota naquele comentário).

# --- KV v2 do mount dedicado "aml-kyc" (só leitura) ---
# Mount próprio da célula (distinto do "secret" usado por pci/platform) —
# isolamento físico do segredo, não só lógico.

# Sumsub (KYC/KYB): SUMSUB_API_KEY + SUMSUB_WEBHOOK_SECRET.
path "aml-kyc/data/sumsub/*" {
  capabilities = ["read"]
}

# Chainalysis (screening on-chain / KYT): CHAINALYSIS_API_KEY.
path "aml-kyc/data/chainalysis/*" {
  capabilities = ["read"]
}

# Custódia Safe multisig: SAFE_OWNER_KEY (envelope KMS/HSM — nunca em
# claro, §2.7) + SAFE_RPC_URL. Leitura apenas — a assinatura acontece em
# hardware (KMS/HSM), o valor lido já é o material de assinatura
# envelopado, não a chave em claro.
path "aml-kyc/data/custody/*" {
  capabilities = ["read"]
}

# --- Credenciais dinâmicas de banco (cofre de compliance) ---
# Mount de database secrets engine dedicado à célula ("aml-kyc/db"),
# isolado do mount "database" usado por platform/pci. Path documentado em
# openbao-auth.yaml (DB_COMPLIANCE_DSN) — lease renovável, TTL curto,
# usuário/senha gerados por rotação (nunca estático).
path "aml-kyc/db/compliance" {
  capabilities = ["read"]
}

# --- Transit: envelope encryption de PII/custódia (escopo aml-kyc) ---
# Cifra em repouso o cofre de compliance (PII de KYC pseudônimo) e o
# material de custódia antes de persistir — mesma finalidade do transit/pci,
# escopo isolado por célula.
path "transit/encrypt/aml-kyc" {
  capabilities = ["update"]
}
path "transit/decrypt/aml-kyc" {
  capabilities = ["update"]
}

# --- Renovar/revogar o PRÓPRIO lease (não o de outras identidades) ---
path "sys/leases/renew" {
  capabilities = ["update"]
}
path "sys/leases/revoke" {
  capabilities = ["update"]
}

# NEGADO por ausência (isolamento total de célula, ADR-0004 §F):
#   - secret/data/pci/*, database/creds/pci-payments, transit/{encrypt,decrypt}/pci
#     (trilho fiat da célula PCI — fora do escopo AML/KYC).
#   - secret/data/platform/*, database/creds/adserver-app
#     (segredos da plataforma geral — fora do escopo de célula).
#   - qualquer path fora de aml-kyc/* (nenhum outro mount é enxergado).
# A célula AML/KYC não enxerga segredo de nenhuma outra célula nem da
# plataforma. O auditor de privacidade valida esta policy antes do go-live.
