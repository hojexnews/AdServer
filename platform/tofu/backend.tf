# State remoto em S3 com lock em DynamoDB. NUNCA versionar state (ver
# .gitignore). Os valores reais entram via `-backend-config` no apply
# (bucket/região por ambiente); aqui só a forma.
#
# Validação offline: `tofu init -backend=false && tofu validate`
# (pula o backend, não exige credenciais).
terraform {
  backend "s3" {
    # bucket         = "hojex-adserver-tfstate"      # via -backend-config
    # key            = "platform/terraform.tfstate"
    # region         = "us-east-1"
    # dynamodb_table = "hojex-adserver-tflock"
    encrypt = true
  }
}
