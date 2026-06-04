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

// Handler retorna um http.Handler para o endpoint /healthz.
//
// Recebe a Config para refletir o estado real do servico.
// Nao faz chamadas de rede — resposta puramente em memoria (K0).
func Handler(cfg config.Config) http.Handler {
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

		// K0: scaffolding — nenhum trilho ativo ainda.
		// K4 adiciona "stripe", "asaas_pix"; K5 adiciona "safe_multisig".
		resp.Status = "ok"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})
}
