// calibration_status_test.go has NO build constraint: it proves the
// Status{Calibrated: false, Reason: ...} signal fires whenever
// attemptCalibration cannot load/validate the calibration map, and that
// Status{Calibrated: true, ...} fires when it can — the exact branch inside
// BuildInferencer that only runs (via onnx.New) under `-tags onnx` in
// production, but that attemptCalibration exposes standalone against ANY
// stub.Inferencer (here, stub.NewStub), so this coverage holds on every
// ordinary `go test ./...` run (ADR-0002 §C hermetic default build) and does
// NOT depend on libonnxruntime.so being installed.
//
// This is the residual finding this file closes (30th-wave / 31st-wave):
// before Status existed, a bad or missing calibration_map.json degraded
// serving to raw pCTR with ONLY a slog.Warn line as evidence — no counter,
// gauge, or health field an alert could page on. Mandato #3 ("Calibracao
// isotonica monitorada ... barata e critica para eCPM") requires this
// degradation to be OBSERVABLE, not merely logged.
package wiring

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hojex/adserver/services/ranker-sidecar/internal/calibration"
	"github.com/hojex/adserver/services/ranker-sidecar/internal/stub"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q): %v", path, err)
	}
}

func TestAttemptCalibration_MapMissing_ReportsUncalibrated(t *testing.T) {
	inner := stub.NewStub("v-test")
	calPath := filepath.Join(t.TempDir(), "does-not-exist.json")

	inf, status := attemptCalibration(inner, calPath, nil)

	if status.Calibrated {
		t.Fatalf("Status.Calibrated = true with a missing calibration map, want false")
	}
	if status.Reason == "" {
		t.Errorf("Status.Reason is empty with a missing calibration map — the degradation must be OBSERVABLE (Mandato #3), not only a log line")
	}
	if status.CalibrationPath != calPath {
		t.Errorf("Status.CalibrationPath = %q, want %q", status.CalibrationPath, calPath)
	}
	// Fail-open: the caller must still get a working, unwrapped Inferencer —
	// never nil, never an error return.
	if _, ok := inf.(*calibration.CalibratedInferencer); ok {
		t.Fatalf("attemptCalibration wrapped the inferencer despite a missing map — fail-open must serve RAW unwrapped")
	}
	got, err := inf.Score([]float32{0, 0, 0})
	if err != nil {
		t.Fatalf("Score returned an error (must fail-open): %v", err)
	}
	if got != 0.0 {
		t.Errorf("Score() = %v, want the inner stub's raw 0.0 (unwrapped)", got)
	}
}

func TestAttemptCalibration_MapMalformed_ReportsUncalibrated(t *testing.T) {
	dir := t.TempDir()
	calPath := filepath.Join(dir, "calibration_map.json")
	writeFile(t, calPath, `{not valid json`)

	inner := stub.NewStub("v-test")
	inf, status := attemptCalibration(inner, calPath, nil)

	if status.Calibrated {
		t.Fatalf("Status.Calibrated = true with a malformed calibration map, want false")
	}
	if status.Reason == "" {
		t.Errorf("Status.Reason is empty with a malformed calibration map — must be observable")
	}
	if _, ok := inf.(*calibration.CalibratedInferencer); ok {
		t.Fatalf("attemptCalibration wrapped the inferencer despite a malformed map — fail-open must serve RAW unwrapped")
	}
}

func TestAttemptCalibration_MapValid_ReportsCalibrated(t *testing.T) {
	dir := t.TempDir()
	calPath := filepath.Join(dir, "calibration_map.json")
	writeFile(t, calPath, `{
		"calibration_method": "isotonic",
		"feature_spec_version": "1.0.0",
		"thresholds": [0.0, 1.0],
		"calibrated_probs": [0.9, 0.9]
	}`)

	// The stub always returns raw score 0.0 — pick a map whose calibrated
	// value at 0.0 differs from 0.0, so a passing test cannot be an accident
	// of an identity map (mirrors calibration_test.go's own discriminating-
	// power discipline).
	inner := stub.NewStub("v-test")
	inf, status := attemptCalibration(inner, calPath, nil)

	if !status.Calibrated {
		t.Fatalf("Status.Calibrated = false with a valid calibration map (Reason=%q), want true", status.Reason)
	}
	if status.Reason != "" {
		t.Errorf("Status.Reason = %q, want empty when Calibrated=true", status.Reason)
	}
	if status.Method != "isotonic" {
		t.Errorf("Status.Method = %q, want %q", status.Method, "isotonic")
	}
	if status.NPoints != 2 {
		t.Errorf("Status.NPoints = %d, want 2", status.NPoints)
	}
	if status.CalibrationPath != calPath {
		t.Errorf("Status.CalibrationPath = %q, want %q", status.CalibrationPath, calPath)
	}

	if _, ok := inf.(*calibration.CalibratedInferencer); !ok {
		t.Fatalf("attemptCalibration did not wrap the inferencer despite a valid map (got %T)", inf)
	}
	got, err := inf.Score([]float32{0, 0, 0})
	if err != nil {
		t.Fatalf("Score returned an error: %v", err)
	}
	if got == 0.0 {
		t.Errorf("Score() = %v, want the CALIBRATED value (0.9), not the inner stub's raw 0.0 — calibration was not applied", got)
	}
}
