// export_test.go — helpers exportados apenas para testes do pacote mercadopago.
// Este arquivo so e compilado durante `go test` (sufixo _test.go).
// Nao adiciona dependencias ao binario de producao.
package mercadopago

// MPDecimalToCentavosStr expoe asaasDecimalToCentavos (local) para testes externos.
// Necessario para testar casos de erro de fracao/negativo (TX-2).
func MPDecimalToCentavosStr(s string) (int64, error) {
	return asaasDecimalToCentavos(s)
}
