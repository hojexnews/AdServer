# Célula AML/KYC — Compliance, Travel Rule e Cofre PII/KYC (ADR-0004 §F / K6)

## Fronteira AML/KYC validável por auditor de privacidade e compliance

Esta célula é o **escopo mínimo de compliance AML/KYC/Travel Rule** do AdServer.
Ela isola:

- **Sumsub** (KYC/KYB): verificação de identidade de tenants/anunciantes.
- **Chainalysis** (KYT/screening on-chain): screening de endereços e transações
  cripto contra listas de sanções (OFAC, EU, BACEN PLD/FT).
- **Travel Rule**: troca de dados de originador/beneficiário entre VASPs (GAFI/FATF R.16).
- **Cofre de compliance** (`db/compliance`): PII/KYC pseudônimo com RLS por tenant.
- **`services/payments`** (trilho cripto): ChainConnector + Safe multisig, dentro desta
  célula. Fora do hot path de decisão (ADR-0004 §C).

### O que é PII/KYC e por que fica aqui — e apenas aqui

PII/KYC inclui: nome completo, CPF/CNPJ, data de nascimento, endereço, documentos
de identidade, selfie de liveness, endereços de carteira cripto vinculados ao tenant.
Todo esse dado é referenciado no restante do sistema **apenas pelo `tenant_id`
pseudônimo** — nunca pelo dado em claro.

**Esta célula é a única fronteira onde PII/KYC existe em claro.** O cofre de
compliance (`db/compliance`) tem Row-Level Security (RLS) por tenant e criptografia
em repouso com envelope KMS. Nenhum outro namespace, serviço ou pipeline de dados
tem acesso ao PII.

### O que NUNCA sai desta célula

| Dado | Destino bloqueado | Controle |
|---|---|---|
| PII/KYC (nome, CPF, documento, selfie) | Ledger, telemetria, outros namespaces | Cilium deny-all + Kyverno + RLS |
| Endereços de carteira cripto vinculados ao tenant | Logs, ClickHouse, Iceberg | OTel Collector redigit (TX-5) |
| API keys Sumsub / Chainalysis | Git, imagem, K8s Secret estático | Kyverno Enforce + Vault Agent |
| Chaves de custódia Safe (SAFE_OWNER_KEY) | Qualquer lugar fora do OpenBao/KMS | Kyverno Enforce + KMS envelope |
| Resultado de screening (status KYC) | Hot path de decisão (delivery) | Cilium deny-all (sem rota delivery↔aml-kyc) |

### Ledger e telemetria sem PII — invariante DA-11/TX-5

- O **ledger** (`db/ledger`) armazena apenas `tenant_id` pseudônimo, valores monetários
  (`NUMERIC`, sem float) e status de transações. Nenhum campo de PII.
- A **telemetria** (OTel Collector) redigita PII antes de qualquer export (TX-5):
  campos como `tenant_name`, `cpf`, `wallet_address` são substituídos por tokens
  antes de chegar ao Loki/Tempo/VictoriaMetrics. O gate TX-5 é validado pelo
  `privacy-compliance-auditor` no merge.
- O **ClickHouse** (analytics) e o **Iceberg** (lakehouse) recebem apenas dados
  agragados e pseudônimos — sem PII.

## Arquitetura de rede (validável por auditor)

```
Internet / Cloudflare (TLS)
  |
  v
Envoy Gateway (namespace: platform-system)
  | HTTPRoute: POST /webhooks/sumsub apenas
  v
[namespace: aml-kyc] ---- Cilium deny-all (netpol/default-deny.yaml)
  |
  +-- Egress permitido (allow-egress-sumsub.yaml):
  |     api.sumsub.com:443                  (KYC/KYB API + SDK tokens)
  |
  +-- Egress permitido (allow-egress-chainalysis.yaml):
  |     api.chainalysis.com:443             (KYT screening + sanções)
  |     data.chainalysis.com:443            (feeds de sanções — ver PLACEHOLDER)
  |
  +-- Egress permitido (allow-egress-travel-rule.yaml):
  |     travel-rule-provider.example.com:443  (PLACEHOLDER — ver nota)
  |
  +-- Egress permitido (allow-egress-openbao.yaml):
  |     platform-system/openbao:8200/8201   (Vault Agent: segredos + leases)
  |
  +-- Egress permitido (allow-egress-postgres-compliance.yaml):
  |     db-compliance/postgres-compliance:5432  (cofre PII/KYC — Opção A)
  |     ou FQDN RDS/Cloud SQL                   (Opção B — ver PLACEHOLDER)
  |
  +-- DNS (allow-dns.yaml, K0):
  |     kube-system/kube-dns:53
  |
  +-- Ingress permitido (allow-ingress-sumsub-webhook.yaml):
        platform-system/envoy-gateway -> payments:8080

Bloqueado por deny-all (sem exceção):
  - delivery -> aml-kyc       (hot path nunca chama compliance)
  - aml-kyc -> delivery       (compliance não influencia decisão em tempo real)
  - aml-kyc -> pci            (células separadas, sem rota direta)
  - pci -> aml-kyc            (células separadas, sem rota direta)
  - aml-kyc -> ml / data      (compliance não expõe PII para ML/analytics)
  - aml-kyc -> internet (outros FQDNs)  (sem egress genérico)
```

## Segredos — onde vivem e como chegam ao Pod

**NENHUM SEGREDO EM GIT, IMAGEM OU KUBERNETES SECRET ESTÁTICO.**

| Segredo | Path no OpenBao | Como chega ao Pod |
|---|---|---|
| `SUMSUB_API_KEY` | `aml-kyc/sumsub/api_key` | Vault Agent → `/vault/secrets/sumsub-api-key` |
| `SUMSUB_WEBHOOK_SECRET` | `aml-kyc/sumsub/webhook_secret` | Vault Agent → `/vault/secrets/sumsub-webhook-secret` |
| `CHAINALYSIS_API_KEY` | `aml-kyc/chainalysis/api_key` | Vault Agent → `/vault/secrets/chainalysis-api-key` |
| `SAFE_OWNER_KEY` | `aml-kyc/custody/safe_owner_key` (+ KMS envelope) | Vault Agent → `/vault/secrets/safe-owner-key` |
| `SAFE_RPC_URL` | `aml-kyc/custody/safe_rpc_url` | Vault Agent → `/vault/secrets/safe-rpc-url` |
| `DB_COMPLIANCE_DSN` | `aml-kyc/db/compliance` (dynamic, lease TTL curto) | Vault Agent → `/vault/secrets/db-compliance-dsn` |

O Pod `payments` usa o ServiceAccount `payments` (namespace `aml-kyc`), mapeado
ao role `aml-kyc-payments` no OpenBao via Kubernetes Auth Method. A policy
`policy-aml-kyc.hcl` restringe leitura a `aml-kyc/*` apenas — sem acesso
a `pci/*` nem a outros namespaces.

**Envelope KMS para chaves de custódia e PII em repouso:**
- `SAFE_OWNER_KEY` usa KMS envelope (AWS KMS / GCP KMS / HSM FIPS 140-2 Nível 3).
- O Postgres do cofre (`db/compliance`) usa criptografia de armazenamento com
  chave KMS gerenciada pelo cloud provider.
- As chaves KMS nunca são armazenadas em YAML nem no git — gerenciadas pelo
  provider de cloud e referenciadas na configuração do OpenBao.

## Controles Kyverno na célula (policy/kyverno-aml-kyc.yaml)

| Policy | Tipo | Efeito |
|---|---|---|
| `proibir-secret-estatico-aml-kyc` | Enforce | Rejeita Pod com `envFrom.secretRef` ou `env.valueFrom.secretKeyRef` |
| `exigir-vault-agent-aml-kyc` | Enforce | Rejeita Pod sem `vault.hashicorp.com/agent-inject: "true"` |
| `proibir-label-delivery-em-aml-kyc` | Enforce | Rejeita Pod com label `adserver.hojex/cell=delivery` |
| `proibir-label-pci-em-aml-kyc` | Enforce | Rejeita Pod com label `adserver.hojex/cell=pci` |

Mais o ClusterPolicy baseline (`platform/k8s/policy/kyverno-baseline.yaml`):
imagens assinadas (cosign), sem tag `:latest`, limites de recurso obrigatórios.

## Como o auditor de privacidade valida esta fronteira

1. **Rede:** `kubectl get ciliumnetworkpolicies -n aml-kyc` lista apenas as
   políticas de allowlist explícitas. Nenhuma abre egress genérico. A ausência de
   política `aml-kyc -> delivery` (ou vice-versa) confirma o isolamento do hot path.

2. **Segredos:** `kubectl get secrets -n aml-kyc` retorna zero Kubernetes Secrets
   (Kyverno impede criação; Vault Agent não cria K8s Secrets).

3. **PII na telemetria:** `kubectl get configmap otel-collector-config -n platform-observability -o yaml`
   confirma regras de redação de PII ativas (campos: `tenant_name`, `cpf`, `wallet_address`,
   `document_number` → substituídos por `[REDACTED]`). Validado por `privacy-compliance-auditor`.

4. **Ledger sem PII:** `\d ledger.postings` no `db/ledger` confirma ausência de
   colunas de PII — apenas `tenant_id UUID`, valores monetários e status.

5. **Isolamento de célula:** `kubectl get ciliumnetworkpolicies -n delivery` confirma
   que não existe nenhuma regra de ingress/egress para o namespace `aml-kyc`.

6. **Cofre com RLS:** `\d compliance.kyc_profiles` confirma coluna `tenant_id` e
   policy RLS ativa (`CREATE POLICY ... USING (tenant_id = current_setting(...))`).

7. **Supply chain:** `cosign verify ghcr.io/hojex/payments:<tag>` confirma assinatura;
   Trivy SBOM no CI confirma ausência de CVE crítico na imagem do workload payments.

## RBAC — quem acessa esta célula

| Identidade | Role | Justificativa |
|---|---|---|
| `hojex:payments-compliance` (PLACEHOLDER) | `aml-kyc-viewer` | Time de compliance/pagamentos; debugging e auditoria |
| ServiceAccount `payments` (namespace `aml-kyc`) | `aml-kyc-payments-workload` | Leitura do ConfigMap de referência Vault Agent |
| SRE geral (`hojex:sre`) | **Sem binding** | PII presente; acesso somente via break-glass com aprovação |
| Outros namespaces (delivery, ml, data, pci) | **Sem binding** | Isolamento total; violação de fronteira se existir |

## Dependências e o que é placeholder vs. completo

| Item | Status |
|---|---|
| NetworkPolicies Cilium (deny-all, DNS, egress-sumsub, egress-chainalysis, egress-openbao, egress-postgres-compliance, ingress-sumsub-webhook) | Completo |
| NetworkPolicy egress-travel-rule | Completo em estrutura; FQDN do provedor é PLACEHOLDER |
| HTTPRoute Gateway API (sumsub-webhook) | Completo; hostname `compliance.hojexnews.com.br` é PLACEHOLDER |
| ServiceAccount `payments` + ConfigMap Vault Agent ref | Completo |
| Políticas Kyverno (4 policies) | Completo |
| RBAC | Completo; grupo OIDC `hojex:payments-compliance` é PLACEHOLDER |
| OpenBao policy (`policy-aml-kyc.hcl`) | Referenciado; arquivo em `platform/secrets/openbao/` |
| Namespace (K0) | Já existente (fase-3-k0) |
| Deployment `payments` real | Fora de escopo desta célula (`services/payments` — outro engenheiro) |
| Cofre PII (`db/compliance`) schema | Fora de escopo desta célula (`db/compliance/migrations/` — outro engenheiro) |
| Provedor Travel Rule (FQDN real) | PLACEHOLDER — substituir antes do go-live |
| Endpoint Chainalysis `data.chainalysis.com` | PLACEHOLDER — confirmar com contrato Chainalysis |
| Chaves vivas no OpenBao / KMS real | Pende de conta cloud e onboarding Sumsub/Chainalysis |
| Conta cloud separada para AML/KYC | Pende de provisionamento (ADR-0004 §F mencionado para PCI; AML/KYC usa namespace isolado no mesmo cluster até necessidade de conta separada ser provada) |
