// Package health expoe o endpoint /healthz do servico de pagamentos (K0).
//
// O health check retorna:
//   - 200 OK + {"status":"ok",...}   — servico habilitado e pronto.
//   - 503 Service Unavailable        — servico desabilitado (PAYMENTS_ENABLED=false)
//     ou dependencias indisponiveis (K4/K5+).
//
// Nenhuma informacao sensivel (chaves, DSNs, PII) e exposta pelo endpoint.
package health

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/hojex/adserver/services/payments/internal/config"
)

// response e o payload JSON do health check.
type response struct {
	Status    string `json:"status"`
	Enabled   bool   `json:"enabled"`
	Env       string `json:"env"`
	Timestamp string `json:"timestamp"`
	// Trilhos que estao vivos (K4/K5+). Vazio em K0 (scaffolding).
	Rails []string `json:"rails,omitempty"`
}

// RailChecker e uma funcao que verifica se um trilho esta saudavel.
// Retorna nil se o trilho esta operacional.
type RailChecker func() error

// Handler retorna um http.Handler para o endpoint /healthz.
//
// railCheckers e um mapa opcional de nome-do-trilho -> funcao de health check.
// K4 injeta: "stripe" -> stripe.Healthy, "asaas_pix" -> asaas.Healthy,
// "mercadopago" -> mp.Healthy (todos no formato func() error).
// Se railCheckers for nil (K0 scaffolding), a resposta indica servico sem trilhos.
func Handler(cfg config.Config, railCheckers map[string]RailChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := response{
			Enabled:   cfg.Enabled,
			Env:       cfg.Env,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}

		if !cfg.Enabled {
			resp.Status = "disabled"
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Verifica trilhos registrados (K4+).
		for name, check := range railCheckers {
			if check() == nil {
				resp.Rails = append(resp.Rails, name)
			}
		}

		resp.Status = "ok"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})
}
