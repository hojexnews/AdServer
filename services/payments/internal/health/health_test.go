package health_test

import (
	"encoding/json"
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

	health.Handler(cfg).ServeHTTP(rec, req)

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

func TestHandler_Enabled(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Enabled: true, ListenAddr: ":8085", Env: "staging"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	health.Handler(cfg).ServeHTTP(rec, req)

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
	// K0: Rails deve ser nil/ausente (nenhum trilho vivo).
	if rails, ok := body["rails"]; ok && rails != nil {
		t.Errorf("K0: rails deve ser nil/omitido, got %v", rails)
	}
}

func TestHandler_ContentType(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{true, false} {
		cfg := config.Config{Enabled: enabled, ListenAddr: ":8085", Env: "development"}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		health.Handler(cfg).ServeHTTP(rec, req)
		ct := rec.Header().Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("enabled=%v: Content-Type quero application/json, got %q", enabled, ct)
		}
	}
}
