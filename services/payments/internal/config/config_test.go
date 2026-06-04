package config_test

import (
	"os"
	"testing"

	"github.com/hojex/adserver/services/payments/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	t.Parallel()
	// Limpa variaveis de ambiente para garantir defaults.
	os.Unsetenv("PAYMENTS_ENABLED")
	os.Unsetenv("PAYMENTS_LISTEN_ADDR")
	os.Unsetenv("PAYMENTS_ENV")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Enabled {
		t.Error("Enabled deve ser false por default em K0")
	}
	if cfg.ListenAddr != ":8085" {
		t.Errorf("ListenAddr: quero :8085, got %q", cfg.ListenAddr)
	}
	if cfg.Env != "development" {
		t.Errorf("Env: quero development, got %q", cfg.Env)
	}
}

func TestLoad_EnabledTrue(t *testing.T) {
	t.Setenv("PAYMENTS_ENABLED", "true")
	t.Setenv("PAYMENTS_ENV", "staging")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Enabled {
		t.Error("Enabled deve ser true")
	}
	if cfg.Env != "staging" {
		t.Errorf("Env: quero staging, got %q", cfg.Env)
	}
}

func TestLoad_InvalidEnv(t *testing.T) {
	t.Setenv("PAYMENTS_ENV", "nao-existe")
	_, err := config.Load()
	if err == nil {
		t.Fatal("esperava erro para PAYMENTS_ENV invalido")
	}
}

func TestLoad_IsProduction(t *testing.T) {
	t.Setenv("PAYMENTS_ENV", "production")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.IsProduction() {
		t.Error("IsProduction deve ser true")
	}
}

func TestLoad_EnabledFalseVariants(t *testing.T) {
	// t.Setenv nao pode ser usado com t.Parallel no parent — subtests
	// herdam o contexto de cleanup de variaveis de ambiente.
	for _, v := range []string{"false", "0", "no", "off", "FALSE"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("PAYMENTS_ENABLED", v)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load(%q): %v", v, err)
			}
			if cfg.Enabled {
				t.Errorf("Enabled deveria ser false para %q", v)
			}
		})
	}
}
