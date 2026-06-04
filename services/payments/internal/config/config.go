// Package config carrega a configuracao do servico de pagamentos a partir
// de variaveis de ambiente (K0/K4).
//
// # Segredos vs. configuracao
//
// Segredos (chaves de API, DSNs com credenciais) NAO estao neste pacote.
// Eles sao injetados em runtime pelo OpenBao/KMS (§2.7 / ADR-0004 §F).
// Este pacote carrega apenas configuracao nao-sensivel (enderecos de listen,
// flags de enable, timeouts, URLs base de provedores) lida de variaveis
// de ambiente sem prefixo de segredo.
//
// Variaveis de ambiente:
//
//	PAYMENTS_ENABLED           — "true" habilita o servico (default: false — K0).
//	PAYMENTS_LISTEN_ADDR       — endereco de escuta HTTP (default: ":8085").
//	PAYMENTS_ENV               — "development" | "staging" | "production".
//	PAYMENTS_PG_DSN            — DSN do Postgres do ledger (K3/K4).
//	STRIPE_BASE_URL            — URL base da API Stripe (default: "https://api.stripe.com").
//	ASAAS_BASE_URL             — URL base da API Asaas (default: "https://api.asaas.com/v3").
//	ASAAS_WEBHOOK_TOKEN        — token de verificacao de webhooks Asaas (nao e segredo de API).
//	MERCADOPAGO_BASE_URL       — URL base da API Mercado Pago (default: "https://api.mercadopago.com").
//	MERCADOPAGO_WEBHOOK_TOKEN  — token de verificacao de webhooks Mercado Pago.
//	STRIPE_TAX_ENABLED         — "true" habilita Stripe Tax (default: false).
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config agrupa a configuracao nao-sensivel do servico de pagamentos.
//
// Segredos (Stripe secret key, Asaas API key, Mercado Pago access token,
// webhook secrets) NAO aparecem aqui — chegam via OpenBao/KMS em runtime
// (ver package secrets).
type Config struct {
	// Enabled controla se o servico processa pagamentos.
	// Default false em K0: o servico nao esta ativo ate K4/K5.
	// Habilitar sem as integracoes vivas (K4/K5) resulta em health 503.
	Enabled bool

	// ListenAddr e o endereco HTTP (ex.: ":8085").
	ListenAddr string

	// Env e o ambiente de execucao: "development", "staging" ou "production".
	Env string

	// PgDSN e o DSN do Postgres do ledger (K3/K4).
	// Pode conter credenciais — lido de var de ambiente, nunca hardcoded.
	// Em producao, injetado pelo OpenBao como envar PAYMENTS_PG_DSN.
	PgDSN string

	// StripeBaseURL e a URL base da API Stripe.
	// Configuravel para apontar para o servidor de teste (mockado em CI).
	// Default: "https://api.stripe.com"
	StripeBaseURL string

	// AsaasBaseURL e a URL base da API Asaas.
	// Default: "https://api.asaas.com/v3" (sandbox: "https://sandbox.asaas.com/api/v3")
	AsaasBaseURL string

	// AsaasWebhookToken e o token de autenticacao para webhooks Asaas.
	// Nao e a API key do Asaas — e o token configurado no painel Asaas
	// para autenticar notificacoes. Lido de ASAAS_WEBHOOK_TOKEN.
	// NOTA: apesar do nome "token", ele e tratado como config nao-sensivel
	// aqui pois e o header de autenticacao do webhook, nao a chave de API.
	// Em producao, recomenda-se injetar via OpenBao tambem.
	AsaasWebhookToken string

	// MercadoPagoBaseURL e a URL base da API Mercado Pago.
	// Default: "https://api.mercadopago.com"
	MercadoPagoBaseURL string

	// MercadoPagoWebhookToken e o token de verificacao de webhooks Mercado Pago.
	// Lido de MERCADOPAGO_WEBHOOK_TOKEN.
	MercadoPagoWebhookToken string

	// StripeTaxEnabled habilita o Stripe Tax (calculo de impostos).
	// Default false: nao bloqueia o trilho se desabilitado.
	StripeTaxEnabled bool
}

// Load le a configuracao das variaveis de ambiente.
// Retorna erro apenas para valores invalidos (nunca para ausencia —
// os defaults sao seguros).
func Load() (Config, error) {
	cfg := Config{
		Enabled:                 parseBool(os.Getenv("PAYMENTS_ENABLED"), false),
		ListenAddr:              envOr("PAYMENTS_LISTEN_ADDR", ":8085"),
		Env:                     envOr("PAYMENTS_ENV", "development"),
		PgDSN:                   os.Getenv("PAYMENTS_PG_DSN"),
		StripeBaseURL:           envOr("STRIPE_BASE_URL", "https://api.stripe.com"),
		AsaasBaseURL:            envOr("ASAAS_BASE_URL", "https://api.asaas.com/v3"),
		AsaasWebhookToken:       os.Getenv("ASAAS_WEBHOOK_TOKEN"),
		MercadoPagoBaseURL:      envOr("MERCADOPAGO_BASE_URL", "https://api.mercadopago.com"),
		MercadoPagoWebhookToken: os.Getenv("MERCADOPAGO_WEBHOOK_TOKEN"),
		StripeTaxEnabled:        parseBool(os.Getenv("STRIPE_TAX_ENABLED"), false),
	}

	validEnvs := map[string]bool{"development": true, "staging": true, "production": true}
	if !validEnvs[cfg.Env] {
		return Config{}, fmt.Errorf("config: PAYMENTS_ENV invalido: %q (esperado: development|staging|production)", cfg.Env)
	}

	return cfg, nil
}

// IsProduction retorna true se o ambiente for "production".
func (c Config) IsProduction() bool {
	return c.Env == "production"
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseBool(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
