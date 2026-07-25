// healthz.go exposes a lightweight HTTP observability endpoint for the
// ranker-sidecar process, following the same GET /healthz convention already
// used by services/decision, services/collector, and services/payments
// (net/http stdlib only, JSON body, no new dependency).
//
// This endpoint is NOT the scoring hot path (that stays the length-prefixed
// JSON-over-Unix-socket protocol in internal/stub/server.go, under TX-4's
// 5-8ms budget). It exists purely so an external prober/alert can observe
// the wiring.Status the sidecar's Inferencer was built with — in particular
// whether isotonic calibration is actually being applied (Mandato #3:
// "Calibracao isotonica monitorada ... barata e critica para eCPM"). Before
// this endpoint existed, a missing/invalid calibration_map.json degraded
// serving to raw pCTR with ONLY a slog.Warn line as evidence — no
// queryable/alertable signal existed anywhere in the process (residual
// finding carried from the 30th wave into this one).
//
// Binding this HTTP listener never blocks or gates the scoring socket: see
// main()'s startHealthzServer call — a bind failure (e.g. port already in
// use) logs an error and the sidecar keeps serving scores on the Unix
// socket. The healthz endpoint is observability-only, never part of the
// fail-open contract itself.
package main

import (
	"encoding/json"
	"net/http"

	"github.com/hojex/adserver/services/ranker-sidecar/internal/wiring"
)

// healthzResponse is the JSON body served on GET /healthz.
type healthzResponse struct {
	Status       string `json:"status"`
	ModelVersion string `json:"model_version"`

	// Calibrated mirrors wiring.Status.Calibrated: whether the Inferencer
	// currently serving scores applies the isotonic calibration map.
	// Calibrated=false is a VALID steady state in stub mode
	// (RANKER_MODEL_PATH unset) — an alert should key off Calibrated=false
	// persisting past the expected model-rollout window, not off a single
	// scrape.
	Calibrated bool `json:"calibrated"`
	// CalibrationReason explains Calibrated=false; empty when Calibrated=true.
	CalibrationReason string `json:"calibration_reason,omitempty"`
	// CalibrationPath is the calibration_map.json path BuildInferencer
	// attempted (empty if calibration was never attempted — stub mode or
	// ONNX model load failure).
	CalibrationPath string `json:"calibration_path,omitempty"`
	// CalibrationMethod/CalibrationNPoints describe the loaded map when
	// Calibrated=true (e.g. "isotonic", 47 threshold points).
	CalibrationMethod  string `json:"calibration_method,omitempty"`
	CalibrationNPoints int    `json:"calibration_n_points,omitempty"`
}

// newHealthzHandler returns the GET /healthz handler. modelVersion and
// calStatus are captured once at startup and baked into the closure: the
// sidecar has no hot-reload (see the module doc comment in main.go, "Hot-
// reload (future work)"), so this snapshot cannot go stale without a
// process restart, which is also the only way the underlying Inferencer
// itself ever changes.
//
// Always returns HTTP 200: liveness (the process is up and answering) is
// independent of calibration status — an uncalibrated-but-serving sidecar
// is fail-open and healthy by DA-3's definition, not down. Alerting must key
// off the "calibrated" JSON field, never off the HTTP status code.
func newHealthzHandler(modelVersion string, calStatus wiring.Status) http.Handler {
	resp := healthzResponse{
		Status:             "ok",
		ModelVersion:       modelVersion,
		Calibrated:         calStatus.Calibrated,
		CalibrationReason:  calStatus.Reason,
		CalibrationPath:    calStatus.CalibrationPath,
		CalibrationMethod:  calStatus.Method,
		CalibrationNPoints: calStatus.NPoints,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp) //nolint:errcheck // best-effort; client-side write error, nothing to recover
	})
}
