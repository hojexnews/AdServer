// Command ranker-sidecar serves pCTR scores over a Unix domain socket.
//
// The sidecar is co-located with the decision service (same pod/host).
// It loads a model and serves score requests from internal/ranker/ipc.go
// (the Go client in the hot path).
//
// # Configuration (environment variables)
//
//	RANKER_SOCKET_PATH   Path to the Unix socket file.
//	                     Default: /tmp/ranker.sock
//
//	RANKER_MODEL_PATH    Path to the compiled .onnx model file (G0/E11 OnnxInferencer).
//	                     If empty, or the file does not exist, or the sidecar was built
//	                     without the `onnx` tag, the sidecar falls back to StubInferencer
//	                     and returns score=0 for all inputs (fail-safe — see below).
//	                     Default: "" (stub mode)
//
//	RANKER_MODEL_VERSION Version tag to embed in score responses and log entries.
//	                     Used by the decision handler to populate Decision.model_version.
//	                     In stub mode, use "stub-j1" (or leave empty for "stub-j1").
//	                     Default: "stub-j1"
//
//	RANKER_CALIBRATION_PATH  Path to the isotonic calibration_map.json artefact
//	                     (ml/calibration/calibrate.py:save_calibration_map, versioned
//	                     in the MLflow registry alongside the .onnx model). Applied as
//	                     stage (b) of the two-stage design ("ONDE A CALIBRACAO VIVE" in
//	                     ml/calibration/calibrate.py's module docstring): (a) infer
//	                     pCTR_raw with the .onnx graph, (b) interpolate pCTR_raw through
//	                     this map -> pCTR_cal, which is what the sidecar actually writes
//	                     to the wire. Only consulted when an OnnxInferencer loaded
//	                     successfully (stub mode never calibrates — score is always 0).
//	                     Default: "" (auto-derives the sibling "calibration_map.json" in
//	                     the same directory as RANKER_MODEL_PATH). If the file is missing
//	                     or fails validation, the sidecar logs a warning and serves the
//	                     RAW (uncalibrated) score instead of refusing to start or
//	                     erroring the hot path (DA-3 fail-open). See
//	                     internal/calibration and internal/wiring.
//
//	DEEP_ENABLED         K1/Fase 3 gate: enables the deep ranker Triton/GPU backend.
//	                     Default: "false" (GBDT ONNX-CPU is always the production backend
//	                     until K8 promotes the deep model under A/B uplift proof).
//	                     Set to "true" only in K8 after A/B uplift is proven.
//	                     With DEEP_ENABLED=false, the sidecar ALWAYS uses the GBDT
//	                     backend regardless of model_version in the request.
//	                     With DEEP_ENABLED=true, model_version with prefix "deep-"
//	                     routes to the Triton backend (Python selector); all others
//	                     continue to use the GBDT ONNX backend.
//
//	TRITON_URL           URL of the Triton Inference Server (gRPC).
//	                     Used when DEEP_ENABLED=true.
//	                     Default: "localhost:8001"
//	                     In K1 (no GPU/Triton active): fail-open (score=0) if
//	                     the Triton server is unreachable — the cascade pure order
//	                     is preserved. Never a blank impression for ML failure.
//
// # ONNX Runtime nativo (G0/E11)
//
// The sidecar now instantiates onnx.New(modelPath, modelVersion) whenever
// RANKER_MODEL_PATH is set and the file exists (see the Inferencer selection
// block below). onnx.New is implemented in two variants, selected by Go
// build tag (ADR-0002 §C — hermetic default build):
//
//   - Default build (no tags): services/ranker-sidecar/internal/onnx/disabled.go
//     (`//go:build !onnx`). New always returns onnx.ErrNotCompiled. No CGO,
//     no libonnxruntime dependency — `go build ./...` stays hermetic.
//   - `-tags onnx` build: services/ranker-sidecar/internal/onnx/onnx.go
//     (`//go:build onnx`). New loads the compiled GBDT .onnx artefact via
//     github.com/yalue/onnxruntime_go (CGO, dlopen of libonnxruntime.so.*;
//     see ONNXRUNTIME_SHARED_LIBRARY_PATH below).
//
// Either way, an error from onnx.New (including ErrNotCompiled) is logged
// and the sidecar falls back to stub.NewStub — the sidecar never refuses to
// start, and the cascade is never starved of a fail-open score=0 backend
// (DA-3). The Inferencer interface (stub.Inferencer) is implemented by both
// StubInferencer and OnnxInferencer — no other code changes needed.
//
//	ONNXRUNTIME_SHARED_LIBRARY_PATH  Path to libonnxruntime.so.* for dlopen.
//	                                 Only read by the `-tags onnx` build.
//	                                 Default: "" (searches "onnxruntime.so"
//	                                 on the default dynamic linker path).
//
// # K1/Fase 3 Deep Ranker Integration Point (DEEP_ENABLED=false by default)
//
// The sidecar gained a deep ranker routing path in K1. The routing logic lives
// in services/ranker-sidecar/internal/triton/selector.py (Python) and is invoked
// by the Python sidecar process. The Go sidecar binary itself is NOT changed in
// K1 beyond this documentation and the DEEP_ENABLED env-var awareness.
//
// For K8 (deep in production under A/B uplift):
//  1. Set DEEP_ENABLED=true in the sidecar environment.
//  2. Set TRITON_URL to the Triton gRPC endpoint.
//  3. Deploy Triton with the deep_ranker model repository (ml/deep/testdata/triton_repo/).
//  4. Run A/B under internal/ranker/{ab,guard,shadow}.go as for the GBDT.
//  5. The deep model replaces the GBDT BEHIND THE SAME extension point (ADR-0004 §A).
//     No new cascade path is created.
//
// # Hot-reload (future work)
//
// On SIGHUP, the sidecar could:
//  1. Load a new OnnxInferencer from the updated RANKER_MODEL_PATH.
//  2. Close the old inferencer.
//  3. Update the model version in the server.
//     This would allow model version rollout without socket rebind or decision
//     service restart. NOT implemented yet (neither stub nor onnx have state to
//     reload today) — the sidecar process is restarted to pick up a new model.
//
// # Wire protocol
//
// See services/ranker-sidecar/internal/stub/server.go for the protocol definition.
// Summary: 4-byte big-endian length prefix + JSON body, one request per connection.
package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hojex/adserver/services/ranker-sidecar/internal/stub"
	"github.com/hojex/adserver/services/ranker-sidecar/internal/wiring"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	socketPath := envOr("RANKER_SOCKET_PATH", "/tmp/ranker.sock")
	modelVersion := envOr("RANKER_MODEL_VERSION", "stub-j1")
	modelPath := os.Getenv("RANKER_MODEL_PATH")
	deepEnabled := os.Getenv("DEEP_ENABLED") == "true"
	tritonURL := envOr("TRITON_URL", "localhost:8001")

	// ---------------------------------------------------------------------------
	// Inferencer selection (K1/Fase 3 extended):
	//
	//   DEEP_ENABLED=false (default, K1):
	//     Always uses StubInferencer (J1) or OnnxInferencer (J2+) for GBDT.
	//     The deep Triton path is NOT activated regardless of model_version.
	//     This ensures golden tests remain green and the cascade is unchanged.
	//
	//   DEEP_ENABLED=true (K8, uplift A/B proven):
	//     model_version prefix "deep-" → routes to TritonInferencer (GPU).
	//     Any other model_version → GBDT ONNX-CPU path (unchanged).
	//     Triton connection failure → fail-open (score=0, cascade pure order).
	//     The deep replaces GBDT BEHIND THE SAME extension point (ADR-0004 §A).
	//     No new cascade path. Budget TX-4 (5-8 ms p99) is NOT expanded.
	//
	//   RANKER_MODEL_PATH empty or file does not exist → StubInferencer.
	//   RANKER_MODEL_PATH points to a valid .onnx file → onnx.New is attempted;
	//     on ANY error (including onnx.ErrNotCompiled in the default,
	//     `-tags onnx`-less build) the sidecar logs a warning and falls back
	//     to StubInferencer. This is a fail-safe path, not fail-open: it
	//     happens once at startup, never mid-request.
	//   onnx.New succeeds → the sidecar additionally loads
	//     RANKER_CALIBRATION_PATH (calibration_map.json — default: the
	//     sibling "calibration_map.json" next to RANKER_MODEL_PATH) and, on
	//     success, wraps the OnnxInferencer with the isotonic calibration
	//     map (internal/calibration) so the score written to the wire is
	//     pCTR_cal, not pCTR_raw — closing the gap where
	//     ml/calibration/calibrate.py produced calibration_map.json but
	//     nothing in serving ever read it. A missing/invalid calibration
	//     artefact fails OPEN to the raw (uncalibrated) OnnxInferencer, with
	//     a warning logged — it never blocks startup or the hot path.
	//     See internal/wiring.BuildInferencer for the actual selection code
	//     (this comment documents it; wiring.go is the single source of
	//     truth, also exercised directly by
	//     internal/wiring/calibration_parity_test.go).
	// ---------------------------------------------------------------------------
	if deepEnabled {
		logger.Info("ranker-sidecar: DEEP_ENABLED=true (K8 gate open)",
			"triton_url", tritonURL,
			"model_version", modelVersion,
			"note", "deep model_version prefix 'deep-' routes to Triton; "+
				"all others use GBDT ONNX-CPU. Fail-open if Triton unreachable.")
	} else {
		logger.Info("ranker-sidecar: DEEP_ENABLED=false (default) — deep ranker gated off",
			"model_version", modelVersion,
			"note", "GBDT ONNX-CPU is the production backend. "+
				"Set DEEP_ENABLED=true only after K8 uplift A/B proof.")
	}

	inf := wiring.BuildInferencer(wiring.Config{
		ModelPath:       modelPath,
		ModelVersion:    modelVersion,
		CalibrationPath: os.Getenv("RANKER_CALIBRATION_PATH"),
	}, logger)
	defer inf.Close() //nolint:errcheck // best-effort

	// Consume deepEnabled and tritonURL to avoid "declared and not used" errors.
	// In K8, these will be used to instantiate TritonInferencer when
	// model_version has prefix "deep-". The selection logic in the Python sidecar
	// (services/ranker-sidecar/internal/triton/selector.py) mirrors this behaviour.
	_ = deepEnabled
	_ = tritonURL

	srv := stub.NewServer(socketPath, inf, logger)

	// Signal handling: SIGINT/SIGTERM → graceful shutdown.
	stopCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		logger.Info("ranker-sidecar: shutdown signal received", "signal", sig.String())
		close(stopCh)
	}()

	logger.Info("ranker-sidecar: starting",
		"socket", socketPath,
		"model_version", modelVersion)

	if err := srv.Serve(stopCh); err != nil {
		logger.Error("ranker-sidecar: server error", "err", err)
		os.Exit(1)
	}

	logger.Info("ranker-sidecar: stopped cleanly")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
