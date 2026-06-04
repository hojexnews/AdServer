package health_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hojex/adserver/services/payments/internal/config"
	"github.com/hojex/adserver/services/payments/internal/health"
)

func TestHandler_Disabled(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Enabled: false, ListenAddr: ":8085", Env: "development"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	health.Handler(cfg, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("quero 503, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "disabled" {
		t.Errorf("status: quero disabled, got %v", body["status"])
	}
	if body["enabled"] != false {
		t.Errorf("enabled: quero false, got %v", body["enabled"])
	}
}

func TestHandler_Enabled_NoRails(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Enabled: true, ListenAddr: ":8085", Env: "staging"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	// nil = sem trilhos registrados (K0 scaffolding).
	health.Handler(cfg, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("quero 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status: quero ok, got %v", body["status"])
	}
	if body["enabled"] != true {
		t.Errorf("enabled: quero true, got %v", body["enabled"])
	}
	if body["env"] != "staging" {
		t.Errorf("env: quero staging, got %v", body["env"])
	}
	// Sem trilhos registrados: rails deve ser nil/ausente.
	if rails, ok := body["rails"]; ok && rails != nil {
		t.Errorf("sem trilhos: rails deve ser nil/omitido, got %v", rails)
	}
}

func TestHandler_WithRailCheckers(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Enabled: true, ListenAddr: ":8085", Env: "development"}

	checkers := map[string]health.RailChecker{
		"stripe":      func() error { return nil },               // saudavel
		"asaas_pix":   func() error { return nil },               // saudavel
		"mercadopago": func() error { return errors.New("down") }, // inativo
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	health.Handler(cfg, checkers).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("quero 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Apenas trilhos saudaveis devem aparecer.
	rails, ok := body["rails"]
	if !ok {
		t.Fatal("esperava campo rails na resposta")
	}
	railsSlice, ok := rails.([]interface{})
	if !ok {
		t.Fatalf("rails deve ser array, got %T", rails)
	}
	// Devem aparecer stripe e asaas_pix (saudaveis); mercadopago nao.
	if len(railsSlice) != 2 {
		t.Errorf("esperado 2 trilhos saudaveis, got %d: %v", len(railsSlice), railsSlice)
	}
	for _, r := range railsSlice {
		if r == "mercadopago" {
			t.Error("mercadopago (down) nao deve aparecer nos rails saudaveis")
		}
	}
}

func TestHandler_ContentType(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{true, false} {
		cfg := config.Config{Enabled: enabled, ListenAddr: ":8085", Env: "development"}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		health.Handler(cfg, nil).ServeHTTP(rec, req)
		ct := rec.Header().Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("enabled=%v: Content-Type quero application/json, got %q", enabled, ct)
		}
	}
}
