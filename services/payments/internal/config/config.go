// Package config carrega a configuracao do servico de pagamentos a partir
// de variaveis de ambiente (K0).
//
// # Segredos vs. configuracao
//
// Segredos (chaves de API, DSNs com credenciais) NAO estao neste pacote.
// Eles sao injetados em runtime pelo OpenBao/KMS (§2.7 / ADR-0004 §F).
// Este pacote carrega apenas configuracao nao-sensivel (enderecos de listen,
// flags de enable, timeouts) lida de variaveis de ambiente sem prefixo de
// segredo.
//
// Variaveis de ambiente:
//
//	PAYMENTS_ENABLED      — "true" habilita o servico (default: false — K0).
//	PAYMENTS_LISTEN_ADDR  — endereco de escuta HTTP (default: ":8085").
//	PAYMENTS_ENV          — "development" | "staging" | "production".
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config agrupa a configuracao nao-sensivel do servico de pagamentos.
//
// Segredos (Stripe key, Asaas key, Safe RPC, DSN do ledger) NAO aparecem
// aqui — chegam via OpenBao/KMS em runtime.
type Config struct {
	// Enabled controla se o servico processa pagamentos.
	// Default false em K0: o servico nao esta ativo ate K4/K5.
	// Habilitar sem as integracoes vivas (K4/K5) resulta em health 503.
	Enabled bool

	// ListenAddr e o endereco HTTP (ex.: ":8085").
	ListenAddr string

	// Env e o ambiente de execucao: "development", "staging" ou "production".
	Env string
}

// Load le a configuracao das variaveis de ambiente.
// Retorna erro apenas para valores invalidos (nunca para ausencia —
// os defaults sao seguros).
func Load() (Config, error) {
	cfg := Config{
		Enabled:    parseBool(os.Getenv("PAYMENTS_ENABLED"), false),
		ListenAddr: envOr("PAYMENTS_LISTEN_ADDR", ":8085"),
		Env:        envOr("PAYMENTS_ENV", "development"),
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
