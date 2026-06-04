// Package config carrega a configuracao do servico de pagamentos a partir
// de variaveis de ambiente (K0/K4/K5).
//
// # Segredos vs. configuracao
//
// Segredos (chaves de API, DSNs com credenciais) NAO estao neste pacote.
// Eles sao injetados em runtime pelo OpenBao/KMS (§2.7 / ADR-0004 §F).
// Este pacote carrega apenas configuracao nao-sensivel (enderecos de listen,
// flags de enable, timeouts, URLs base de provedores) lida de variaveis
// de ambiente sem prefixo de segredo.
//
// Variaveis de ambiente (fiat K4):
//
//	PAYMENTS_ENABLED           — "true" habilita o servico (default: false — K0).
//	PAYMENTS_LISTEN_ADDR       — endereco de escuta HTTP (default: ":8085").
//	PAYMENTS_ENV               — "development" | "staging" | "production".
//	PAYMENTS_PG_DSN            — DSN do Postgres do ledger (K3/K4).
//	STRIPE_BASE_URL            — URL base da API Stripe (default: "https://api.stripe.com").
//	ASAAS_BASE_URL             — URL base da API Asaas (default: "https://api.asaas.com/v3").
//	ASAAS_WEBHOOK_TOKEN        — token de verificacao de webhooks Asaas.
//	MERCADOPAGO_BASE_URL       — URL base da API Mercado Pago (default: "https://api.mercadopago.com").
//	MERCADOPAGO_WEBHOOK_TOKEN  — token de verificacao de webhooks Mercado Pago.
//	STRIPE_TAX_ENABLED         — "true" habilita Stripe Tax (default: false).
//
// Variaveis de ambiente (cripto K5 — Safe multisig + USDC):
//
//	SAFE_RPC_URL               — endpoint JSON-RPC EVM para chain_id=1 (Ethereum).
//	                             Lido de OpenBao/env; NUNCA hardcode.
//	SAFE_ADDRESS               — endereco da carteira Safe multisig da plataforma.
//	SAFE_MIN_CONFIRMATIONS     — numero minimo de confirmacoes para finalidade (default: 12).
//	USDC_CONTRACT_ADDRESS      — endereco do contrato USDC na chain (default: mainnet Ethereum).
//	CRYPTO_ENABLED             — "true" habilita o trilho cripto Safe (default: false).
//
// Variaveis de ambiente (compliance K6 — Sumsub + Chainalysis + Travel Rule):
//
//	COMPLIANCE_ENABLED         — "true" habilita screening/KYC no trilho cripto (default: false).
//	CHAINALYSIS_BASE_URL       — URL base da API Chainalysis (default: https://api.chainalysis.com).
//	CHAINALYSIS_RISK_THRESHOLD — score de risco 0-100 acima do qual bloqueia (default: 70).
//	SUMSUB_BASE_URL            — URL base da API Sumsub (default: https://api.sumsub.com).
//	TRAVEL_RULE_THRESHOLD_USDC — limiar Travel Rule em minor-units USDC (default: 0 = sempre).
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

	// ---------------------------------------------------------------------------
	// K5 — Trilho cripto: Safe multisig + USDC
	// ---------------------------------------------------------------------------

	// CryptoEnabled habilita o trilho cripto Safe multisig (K5).
	// Default false: trilho inativo ate K5 estar completo com infra viva.
	CryptoEnabled bool

	// SafeRPCURL e o endpoint JSON-RPC EVM para chain_id=1 (Ethereum mainnet).
	// Lido de SAFE_RPC_URL (env/OpenBao). NUNCA hardcode — segredo de infra.
	// Em staging/testnet, apontar para Sepolia ou Goerli RPC.
	SafeRPCURL string

	// SafeAddress e o endereco da carteira Safe multisig da plataforma.
	// Lido de SAFE_ADDRESS. NAO e PII — e o endereco de custodia da plataforma.
	SafeAddress string

	// SafeMinConfirmations e o numero minimo de confirmacoes para finalidade.
	// Default 12 (Ethereum mainnet). Ajustar por chain/rede.
	// Lido de SAFE_MIN_CONFIRMATIONS (inteiro).
	SafeMinConfirmations uint32

	// USDCContractAddress e o endereco do contrato USDC na chain.
	// Default: 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48 (Ethereum mainnet).
	// Sobrescrever para testnet (ex.: Sepolia USDC).
	USDCContractAddress string

	// ---------------------------------------------------------------------------
	// K6 — Compliance: Sumsub + Chainalysis + Travel Rule
	// ---------------------------------------------------------------------------

	// ComplianceEnabled habilita o screening Chainalysis e KYC Sumsub no trilho cripto.
	// Default false: compliance inativo ate K6 estar completo com chaves vivas.
	// SEGURANCA: em producao, deve ser true quando CRYPTO_ENABLED=true.
	ComplianceEnabled bool

	// ChainalysisBaseURL e a URL base da API Chainalysis.
	// Default: "https://api.chainalysis.com"
	ChainalysisBaseURL string

	// ChainalysisRiskThreshold e o score de risco (0-100) acima do qual a operacao e bloqueada.
	// Default: 70. TX-2: sem float — int.
	ChainalysisRiskThreshold int

	// SumsubBaseURL e a URL base da API Sumsub.
	// Default: "https://api.sumsub.com"
	SumsubBaseURL string

	// TravelRuleThresholdUSDC e o limiar de Travel Rule em minor-units de USDC.
	// Default: 0 (registrar todas as transferencias — comportamento conservador).
	// Para USD 1.000 com USDC scale=6: 1_000_000_000 minor-units.
	// TX-2: int64, sem float.
	TravelRuleThresholdUSDC int64
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

		// K5 — cripto
		CryptoEnabled: parseBool(os.Getenv("CRYPTO_ENABLED"), false),
		SafeRPCURL:    os.Getenv("SAFE_RPC_URL"),
		SafeAddress:   os.Getenv("SAFE_ADDRESS"),
		SafeMinConfirmations: parseUint32Or(os.Getenv("SAFE_MIN_CONFIRMATIONS"), 12),
		USDCContractAddress: envOr(
			"USDC_CONTRACT_ADDRESS",
			"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", // Ethereum mainnet
		),

		// K6 — compliance
		ComplianceEnabled:        parseBool(os.Getenv("COMPLIANCE_ENABLED"), false),
		ChainalysisBaseURL:       envOr("CHAINALYSIS_BASE_URL", "https://api.chainalysis.com"),
		ChainalysisRiskThreshold: parseInt(os.Getenv("CHAINALYSIS_RISK_THRESHOLD"), 70),
		SumsubBaseURL:            envOr("SUMSUB_BASE_URL", "https://api.sumsub.com"),
		TravelRuleThresholdUSDC:  parseInt64(os.Getenv("TRAVEL_RULE_THRESHOLD_USDC"), 0),
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

// parseUint32Or converte uma string para uint32; retorna def se vazia ou invalida.
func parseUint32Or(s string, def uint32) uint32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	var n uint32
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return def
	}
	return n
}

// parseInt converte uma string para int; retorna def se vazia ou invalida.
// TX-2: usado apenas para thresholds de risco (0-100), nao para valores monetarios.
func parseInt(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return def
	}
	return n
}

// parseInt64 converte uma string para int64; retorna def se vazia ou invalida.
// TX-2: usado para limiares em minor-units (int64, sem float).
func parseInt64(s string, def int64) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return def
	}
	return n
}
