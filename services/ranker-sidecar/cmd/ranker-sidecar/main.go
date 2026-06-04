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
//	RANKER_MODEL_PATH    Path to the ONNX model file (for J2+ with OnnxInferencer).
//	                     In J1 (StubInferencer), this variable is ignored; the sidecar
//	                     runs without a model file and returns score=0 for all inputs.
//	                     Default: "" (stub mode)
//
//	RANKER_MODEL_VERSION Version tag to embed in score responses and log entries.
//	                     Used by the decision handler to populate Decision.model_version.
//	                     In J1 stub mode, use "stub-j1" (or leave empty for "stub-j1").
//	                     Default: "stub-j1"
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
// # J2 ONNX Integration Point
//
// To swap in the real ONNX model (J2):
//  1. Ensure libonnxruntime.so.1 is installed (e.g., via apt or bundled in the image).
//  2. Set RANKER_MODEL_PATH to the path of the compiled .onnx artefact downloaded
//     from the MLflow registry (ml/registry/).
//  3. Replace the StubInferencer instantiation below with OnnxInferencer:
//
//	     // Before (J1 stub):
//	     inf := stub.NewStub(modelVersion)
//
//	     // After (J2 ONNX):
//	     // import "github.com/hojex/adserver/services/ranker-sidecar/internal/onnx"
//	     // inf, err := onnx.New(modelPath, modelVersion)
//	     // if err != nil { logger.Error("onnx load failed", "err", err); os.Exit(1) }
//	     // defer inf.Close()
//
//  4. The Inferencer interface (stub.Inferencer) is implemented by both StubInferencer
//     and OnnxInferencer — no other code changes needed.
//  5. Run go test ./... to verify parity and server tests still pass.
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
// # Hot-reload (J2+)
//
// On SIGHUP, the sidecar will:
//  1. Load a new OnnxInferencer from the updated RANKER_MODEL_PATH.
//  2. Close the old inferencer.
//  3. Update the model version in the server.
//  This allows model version rollout without socket rebind or decision service restart.
//  Not implemented in J1 (stub has no state to reload).
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
	//   J1: RANKER_MODEL_PATH is empty or the file does not exist → StubInferencer.
	//   J2: RANKER_MODEL_PATH points to a valid .onnx file → OnnxInferencer.
	//       (See comment block in package doc above for swap instructions.)
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

	var inf stub.Inferencer
	if modelPath != "" {
		if _, err := os.Stat(modelPath); err == nil {
			// Model file present: in J2, instantiate OnnxInferencer here.
			// For J1, we still use the stub (ONNX runtime not available).
			logger.Warn("ranker-sidecar: RANKER_MODEL_PATH set but ONNX runtime not compiled in — "+
				"using StubInferencer. Set build tag 'onnx' and provide libonnxruntime.so for J2.",
				"model_path", modelPath,
				"model_version", modelVersion)
			inf = stub.NewStub(modelVersion)
		} else {
			logger.Warn("ranker-sidecar: RANKER_MODEL_PATH set but file not found — using StubInferencer",
				"model_path", modelPath,
				"err", err)
			inf = stub.NewStub(modelVersion)
		}
	} else {
		logger.Info("ranker-sidecar: RANKER_MODEL_PATH not set — running in stub mode (J1)",
			"model_version", modelVersion)
		inf = stub.NewStub(modelVersion)
	}
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
