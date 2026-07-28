// healthz_test.go proves GET /healthz surfaces wiring.Status.Calibrated —
// the residual finding this file closes (30th/31st wave): before this
// endpoint existed, a bad or missing calibration_map.json degraded serving
// to raw pCTR with NO signal an external prober/alert could consume, only a
// slog.Warn line (Mandato #3: "Calibracao isotonica monitorada").
//
// This test asserts on the JSON body, not just that the handler returns
// 200 — a mutation that stops copying calStatus.Calibrated into the
// response (e.g. always writing Calibrated: true, or dropping the field
// entirely) must fail this test, not just "some field was 2xx".
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hojex/adserver/services/ranker-sidecar/internal/wiring"
)

func TestHealthzHandler_ReportsUncalibrated(t *testing.T) {
	status := wiring.Status{
		Calibrated: false,
		Reason:     "calibration: read \"/nonexistent/calibration_map.json\": no such file or directory",
	}
	h := newHealthzHandler("stub-j1", status)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	// Liveness (HTTP 200) must hold even when uncalibrated — DA-3 fail-open:
	// an uncalibrated-but-serving sidecar is healthy, not down. Alerting
	// keys off the JSON body's "calibrated" field, never the status code.
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d (uncalibrated must still report healthy/live)", rec.Code, http.StatusOK)
	}

	var resp healthzResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", rec.Body.String(), err)
	}
	if resp.Calibrated {
		t.Errorf("resp.Calibrated = true, want false (calibration map was never loaded)")
	}
	if resp.CalibrationReason == "" {
		t.Errorf("resp.CalibrationReason is empty — the degradation must be OBSERVABLE via /healthz, not silent")
	}
	if resp.ModelVersion != "stub-j1" {
		t.Errorf("resp.ModelVersion = %q, want %q", resp.ModelVersion, "stub-j1")
	}
}

func TestHealthzHandler_ReportsCalibrated(t *testing.T) {
	status := wiring.Status{
		Calibrated:      true,
		CalibrationPath: "/opt/ranker/calibration_map.json",
		Method:          "isotonic",
		NPoints:         47,
	}
	h := newHealthzHandler("pctr-lgb-v3", status)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp healthzResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", rec.Body.String(), err)
	}
	if !resp.Calibrated {
		t.Errorf("resp.Calibrated = false, want true")
	}
	if resp.CalibrationReason != "" {
		t.Errorf("resp.CalibrationReason = %q, want empty when Calibrated=true", resp.CalibrationReason)
	}
	if resp.CalibrationMethod != "isotonic" {
		t.Errorf("resp.CalibrationMethod = %q, want %q", resp.CalibrationMethod, "isotonic")
	}
	if resp.CalibrationNPoints != 47 {
		t.Errorf("resp.CalibrationNPoints = %d, want 47", resp.CalibrationNPoints)
	}
}
