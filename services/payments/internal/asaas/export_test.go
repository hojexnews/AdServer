// export_test.go — helpers exportados apenas para testes do pacote asaas.
// Este arquivo so e compilado durante `go test` (sufixo _test.go).
// Nao adiciona dependencias ao binario de producao.
package asaas

import (
	"context"

	"github.com/hojex/adserver/services/payments/internal/fiat"
)

// CreatePixChargeRaw e um helper de teste que invoca CreatePixCharge com
// os campos minimos necessarios para verificar a conversao decimal->centavos.
// Evita importar o pacote fiat nos testes externos do pacote asaas.
func (p *Provider) CreatePixChargeRaw(amountMinor int64, tenantID, advertiserID, description string) (fiat.PixChargeResult, error) {
	return p.CreatePixCharge(context.Background(), fiat.PixChargeRequest{
		TenantID:       tenantID,
		AdvertiserID:   advertiserID,
		AmountMinor:    amountMinor,
		Scale:          2,
		IdempotencyKey: "test:" + advertiserID,
		Description:    description,
	})
}

// AsaasDecimalToCentavos expoe asaasDecimalToCentavos para testes externos.
// Necessario para testar casos de erro de fracao/negativo (TX-2).
func AsaasDecimalToCentavos(s string) (int64, error) {
	return asaasDecimalToCentavos(s)
}
