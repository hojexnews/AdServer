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

	// ---------------------------------------------------------------------------
	// Inferencer selection:
	//   J1: RANKER_MODEL_PATH is empty or the file does not exist → StubInferencer.
	//   J2: RANKER_MODEL_PATH points to a valid .onnx file → OnnxInferencer.
	//       (See comment block in package doc above for swap instructions.)
	// ---------------------------------------------------------------------------
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
