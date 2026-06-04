// deep_flag_test.go — Testes da fiacao de flag do deep ranker (K1).
//
// Invariantes verificados:
//  1. IsDeepModelVersion detecta prefixo "deep-" corretamente.
//  2. IsDeepEnabled le DEEP_ENABLED do ambiente (default false).
//  3. ShouldUseDeep combina ambos (AND).
//  4. Com DEEP_ENABLED=false, ShouldUseDeep SEMPRE retorna false
//     (golden tests da Fase 1/2 continuam verdes — deep-off ≡ cascata pura).
//  5. Strings vazias e versoes GBDT nao sao reconhecidas como deep.
package ranker

import (
	"os"
	"testing"
)

// ---------------------------------------------------------------------------
// IsDeepModelVersion
// ---------------------------------------------------------------------------

func TestIsDeepModelVersion_Deep(t *testing.T) {
	deepVersions := []string{
		"deep-1.0.0",
		"deep-two-tower-v1",
		"deep-k1-scaffold",
		"deep-",
		"deep-int8-prod",
	}
	for _, v := range deepVersions {
		if !IsDeepModelVersion(v) {
			t.Errorf("IsDeepModelVersion(%q) = false; esperado true", v)
		}
	}
}

func TestIsDeepModelVersion_NotDeep(t *testing.T) {
	notDeepVersions := []string{
		"stub-j1",
		"pctr_lgb_v3",
		"1.0.0",
		"",
		"DEEP-1.0.0",  // case-sensitive: 'DEEP-' nao e 'deep-'
		"xdeep-1.0.0", // sem prefixo exato
	}
	for _, v := range notDeepVersions {
		if IsDeepModelVersion(v) {
			t.Errorf("IsDeepModelVersion(%q) = true; esperado false", v)
		}
	}
}

// ---------------------------------------------------------------------------
// IsDeepEnabled
// ---------------------------------------------------------------------------

func TestIsDeepEnabled_DefaultFalse(t *testing.T) {
	// Garante que DEEP_ENABLED nao esta definida antes do teste.
	t.Setenv("DEEP_ENABLED", "")
	if IsDeepEnabled() {
		t.Error("IsDeepEnabled() = true com DEEP_ENABLED vazio; esperado false")
	}
}

func TestIsDeepEnabled_False(t *testing.T) {
	for _, v := range []string{"false", "False", "FALSE", "0", "off", "no"} {
		t.Setenv("DEEP_ENABLED", v)
		if IsDeepEnabled() {
			t.Errorf("IsDeepEnabled() = true com DEEP_ENABLED=%q; esperado false", v)
		}
	}
}

func TestIsDeepEnabled_True(t *testing.T) {
	t.Setenv("DEEP_ENABLED", "true")
	if !IsDeepEnabled() {
		t.Error("IsDeepEnabled() = false com DEEP_ENABLED=true; esperado true")
	}
}

func TestIsDeepEnabled_UnsetMeansDisabled(t *testing.T) {
	// Remove a variavel de ambiente completamente.
	prev := os.Getenv("DEEP_ENABLED")
	os.Unsetenv("DEEP_ENABLED") //nolint:errcheck
	defer func() {
		if prev != "" {
			os.Setenv("DEEP_ENABLED", prev) //nolint:errcheck
		}
	}()
	if IsDeepEnabled() {
		t.Error("IsDeepEnabled() = true com DEEP_ENABLED nao definida; esperado false")
	}
}

// ---------------------------------------------------------------------------
// ShouldUseDeep
// ---------------------------------------------------------------------------

func TestShouldUseDeep_FalseWhenDisabled(t *testing.T) {
	// DEEP_ENABLED=false → ShouldUseDeep SEMPRE false, independente do model_version.
	// Este e o invariante critico: deep-off ≡ cascata pura (golden tests verdes).
	t.Setenv("DEEP_ENABLED", "false")
	versions := []string{
		"deep-1.0.0",
		"deep-two-tower-v1",
		"stub-j1",
		"pctr_lgb_v3",
		"",
	}
	for _, v := range versions {
		if ShouldUseDeep(v) {
			t.Errorf("ShouldUseDeep(%q) = true com DEEP_ENABLED=false; esperado false", v)
		}
	}
}

func TestShouldUseDeep_TrueWhenEnabledAndDeepVersion(t *testing.T) {
	t.Setenv("DEEP_ENABLED", "true")
	deepVersions := []string{
		"deep-1.0.0",
		"deep-k1-scaffold",
	}
	for _, v := range deepVersions {
		if !ShouldUseDeep(v) {
			t.Errorf("ShouldUseDeep(%q) = false com DEEP_ENABLED=true; esperado true", v)
		}
	}
}

func TestShouldUseDeep_FalseWhenEnabledButGBDTVersion(t *testing.T) {
	t.Setenv("DEEP_ENABLED", "true")
	gbdtVersions := []string{
		"stub-j1",
		"pctr_lgb_v3",
		"1.0.0",
		"",
	}
	for _, v := range gbdtVersions {
		if ShouldUseDeep(v) {
			t.Errorf("ShouldUseDeep(%q) = true com DEEP_ENABLED=true; esperado false (versao GBDT)", v)
		}
	}
}

// ---------------------------------------------------------------------------
// Invariante de golden tests (deep-off ≡ cascata pura)
// ---------------------------------------------------------------------------

// TestDeepOffIsEquivalentToCascade verifica o invariante central do K1:
// com DEEP_ENABLED=false, nenhum codigo de deep e ativado — o caminho
// de decisao e identico ao da Fase 1/2 (cascata pura).
//
// Esta verificacao e simbolica no nivel da flag; os golden tests da Fase 1/2
// (parity_test.go, ranker_test.go) verificam o comportamento real de scoring.
func TestDeepOffIsEquivalentToCascade(t *testing.T) {
	t.Setenv("DEEP_ENABLED", "false")

	// Qualquer model_version — incluindo "deep-*" — nao ativa o deep.
	for _, v := range []string{"deep-1.0.0", "stub-j1", "pctr_lgb_v3", ""} {
		if ShouldUseDeep(v) {
			t.Fatalf(
				"INVARIANTE VIOLADA: ShouldUseDeep(%q)=true com DEEP_ENABLED=false. "+
					"Deep-off deve ser identico a cascata pura (ADR-0004 §A).",
				v,
			)
		}
	}
}
