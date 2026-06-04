// Package secrets carrega os segredos de pagamento em runtime.
//
// # Modelo de seguranca
//
// NENHUM segredo aparece neste arquivo como literal, constante ou default.
// Em producao, os valores chegam via variaveis de ambiente injetadas pelo
// OpenBao/KMS no pod (ADR-0004 §F / §2.7). Em desenvolvimento/sandbox, o
// operador define as mesmas variaveis localmente.
//
// A funcao Load retorna erro imediato se qualquer segredo obrigatorio estiver
// ausente — o servico nao sobe com segredos faltando.
//
// # Variaveis de ambiente esperadas
//
//   - STRIPE_SECRET_KEY         — chave secreta Stripe (sk_live_... ou sk_test_...)
//   - STRIPE_WEBHOOK_SECRET     — segredo de assinatura de webhook Stripe (whsec_...)
//   - ASAAS_API_KEY             — chave de API Asaas ($aact_...)
//   - MERCADOPAGO_ACCESS_TOKEN  — access token Mercado Pago (APP_USR-...)
//
// # O que NAO entra aqui
//
//   - PAYMENTS_PG_DSN (DSN do Postgres) — fica em config separado de infra
//   - Chaves de custódia Safe/Fireblocks — K5
//   - KYC/Chainalysis — K6
//
// AUDITORIA PCI: STRIPE_SECRET_KEY NUNCA pode ser logada, serializada em
// struct publico, ou retornada em resposta HTTP. O campo e privado (minusculo)
// e so acessivel via getter. O getter nao deve ser chamado em handlers que
// processam requests de usuario.
package secrets

import (
	"errors"
	"fmt"
	"os"
)

// Secrets agrupa os segredos do servico de pagamentos.
//
// Os campos sao todos privados para evitar acesso inadvertido por reflexao,
// logging estruturado (slog/zap) ou serialização JSON acidental.
// Acesse via getters explicitos.
type Secrets struct {
	stripeSecretKey        string // NUNCA loge; NUNCA exponha em resposta HTTP
	stripeWebhookSecret    string // necessario para verificar assinatura de webhook Stripe
	asaasAPIKey            string // chave Asaas/PIX
	mercadoPagoAccessToken string // access token Mercado Pago (failover)
}

// ErrMissingSecret e retornado quando um segredo obrigatorio esta ausente.
var ErrMissingSecret = errors.New("secrets: segredo obrigatorio ausente")

// Load le os segredos das variaveis de ambiente.
//
// Retorna ErrMissingSecret se qualquer segredo obrigatorio estiver ausente.
// O servico NAO deve inicializar com segredos faltando.
func Load() (Secrets, error) {
	var missing []string

	get := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	s := Secrets{
		stripeSecretKey:        get("STRIPE_SECRET_KEY"),
		stripeWebhookSecret:    get("STRIPE_WEBHOOK_SECRET"),
		asaasAPIKey:            get("ASAAS_API_KEY"),
		mercadoPagoAccessToken: get("MERCADOPAGO_ACCESS_TOKEN"),
	}

	if len(missing) > 0 {
		return Secrets{}, fmt.Errorf("%w: %v", ErrMissingSecret, missing)
	}

	return s, nil
}

// StripeSecretKey retorna a chave secreta Stripe.
//
// NUNCA logue este valor. NUNCA inclua em resposta HTTP, trace ou metrica.
// Use APENAS para autenticar requests a API Stripe no backend (server-side).
func (s Secrets) StripeSecretKey() string { return s.stripeSecretKey }

// StripeWebhookSecret retorna o segredo de assinatura de webhook Stripe.
//
// Usado exclusivamente para verificar a assinatura Stripe-Signature em
// webhooks recebidos. NUNCA exponha este valor.
func (s Secrets) StripeWebhookSecret() string { return s.stripeWebhookSecret }

// AsaasAPIKey retorna a chave de API Asaas.
//
// NUNCA logue. Use apenas para autenticar requests a API Asaas.
func (s Secrets) AsaasAPIKey() string { return s.asaasAPIKey }

// MercadoPagoAccessToken retorna o access token do Mercado Pago.
//
// NUNCA logue. Use apenas para autenticar requests a API Mercado Pago.
func (s Secrets) MercadoPagoAccessToken() string { return s.mercadoPagoAccessToken }
