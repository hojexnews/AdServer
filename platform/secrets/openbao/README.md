# Segredos — OpenBao/Vault (§2.7, TX-3)

**Nada estático em imagem ou git.** Segredos vêm do OpenBao em runtime, preferindo
credenciais **dinâmicas** (DB com TTL curto + rotação) e **Pod Identity**. Chaves
de pagamento usam **KMS/HSM**, não KV.

## Políticas (menor privilégio por célula)

| Arquivo | Célula | Escopo |
|---------|--------|--------|
| [`policy-platform.hcl`](policy-platform.hcl) | platform/delivery/ml/data | KV `secret/platform/*` (read) + DB dinâmico `adserver-app`. Sem acesso a pci/aml. |
| [`policy-pci.hcl`](policy-pci.hcl) | **pci** | Só `secret/pci/payments/*` + DB `pci-payments` + Transit `pci`. Isolado de tudo. |
| [`policy-aml-kyc.hcl`](policy-aml-kyc.hcl) | **aml-kyc** | KV mount `aml-kyc/data/{sumsub,chainalysis,custody}/*` (read) + DB dinâmico `aml-kyc/db/compliance` + Transit `aml-kyc`. Isolado de tudo (E8). |

## Auth Kubernetes + Pod Identity (plano)

No alvo, cada ServiceAccount de célula autentica no OpenBao via **k8s auth** e
recebe **apenas** a política da sua célula:

```
# Exemplo (configurar no apply, não versionar tokens):
bao auth enable kubernetes
bao write auth/kubernetes/role/delivery \
    bound_service_account_names=delivery-app \
    bound_service_account_namespaces=delivery \
    policies=platform ttl=15m
bao write auth/kubernetes/role/pci-payments \
    bound_service_account_names=pci-payments \
    bound_service_account_namespaces=pci \
    policies=pci ttl=10m
```

## A crescer (Fase 1)

- Backends `database/` (Postgres) e `transit/` provisionados via Tofu/Helm.
- Rotação de chave de capping (salt rotativo, TX-5) — segredo dinâmico curto.
- Integração KMS/HSM para chaves de pagamento (fora do KV).

> `policy-aml-kyc.hcl` (célula KYC/Travel Rule, análoga à PCI) foi escrita no
> E8 (mandato item 4) — ver tabela acima e
> [`platform/cells/aml-kyc/secrets/openbao-auth.yaml`](../../cells/aml-kyc/secrets/openbao-auth.yaml)
> para o contrato de autenticação (Vault Agent/Kubernetes Auth).
