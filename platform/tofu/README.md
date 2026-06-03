# OpenTofu — IaC da plataforma-base (§2.7)

Root module **enxuto e validável**: pins de provider, backend remoto (template),
variáveis, células e o **plano de módulos**. Os recursos cloud reais (VPC/EKS/
addons) entram em [`modules/`](modules/) na Fase 1 — fora do root para que
`tofu validate` rode **sem credenciais**.

## Validar (offline, sem cloud)

```bash
cd platform/tofu
tofu fmt -check -recursive          # formatação canônica
tofu init -backend=false            # baixa providers; pula o backend S3
tofu validate                       # valida a configuração
```

## Aplicar (com cloud)

```bash
# State primeiro (bucket S3 + tabela DynamoDB de lock) — fora deste root.
tofu init -backend-config=bucket=hojex-adserver-tfstate \
          -backend-config=key=platform/terraform.tfstate \
          -backend-config=region=us-east-1 \
          -backend-config=dynamodb_table=hojex-adserver-tflock
tofu plan  -var environment=dev
tofu apply -var environment=dev
```

## Arquivos

| Arquivo | O que é |
|---------|---------|
| `versions.tf`  | `required_version` + pins de provider (aws, kubernetes, helm). |
| `backend.tf`   | Backend S3 + lock DynamoDB (valores via `-backend-config`). |
| `variables.tf` | região, ambiente, cluster, versão k8s, células, tags. |
| `main.tf`      | provider AWS, locais (células→namespaces) e o plano de módulos. |
| `outputs.tf`   | nome base, região, mapa de células, células de compliance. |
| `modules/`     | plano dos módulos `network`/`eks`/`addons` (Fase 1). |

> **State nunca vai pro git** (`.gitignore` cobre `*.tfstate*`, `.terraform/`).
