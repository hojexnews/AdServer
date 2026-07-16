// Package stub provides the StubInferencer — a placeholder inference engine
// used whenever no compiled ONNX model is available or loadable.
//
// PRODUCTION INTEGRATION (G0/E11 — see services/ranker-sidecar/internal/onnx):
//
//	The real OnnxInferencer lives in internal/onnx (onnx.go, `//go:build onnx`)
//	and wraps github.com/yalue/onnxruntime_go to load the compiled .onnx
//	artefact from the MLflow registry path (ml/registry/artifacts/pctr_model.onnx).
//	cmd/ranker-sidecar/main.go instantiates onnx.New(modelPath, modelVersion)
//	whenever RANKER_MODEL_PATH is set and the file exists, and falls back to
//	StubInferencer (this package) on ANY error — including onnx.ErrNotCompiled,
//	returned by the default (`-tags onnx`-less) build of internal/onnx.
//
//	To enable the real backend at build time:
//	  1. Download the compiled .onnx from the MLflow registry:
//	       mlflow artifacts download -r <run_id> -a model.onnx -d /opt/ranker/
//	  2. Set RANKER_MODEL_PATH=/opt/ranker/model.onnx in the sidecar environment.
//	  3. Build with `-tags onnx` (CGO_ENABLED=1) and ensure libonnxruntime.so.*
//	     is loadable — set ONNXRUNTIME_SHARED_LIBRARY_PATH or place it on the
//	     default dynamic linker search path.
//	  4. The wire protocol (length-prefixed JSON over UDS) is unchanged.
//	  5. Run internal/ranker/parity_test.go (featurization) and
//	     internal/onnx/onnx_parity_test.go (`-tags onnx`, inference) to confirm
//	     the vector and scoring contracts are met.
//
// Why the stub still exists as the default backend:
//   - The default build (ADR-0002 §C: hermetic, no CGO) never links
//     onnxruntime_go — see internal/onnx/disabled.go.
//   - It is also the correct fail-SAFE fallback at startup (bad/missing model
//     file, onnxruntime load failure) so the sidecar never refuses to start.
//   - The sidecar wire protocol and server loop are fully functional; only the
//     model inference is stubbed out in this mode.
//   - StubInferencer returns score=0 for all inputs. With the stable-sort in
//     internal/ranker/ranker.go, equal scores preserve the deterministic cascade
//     order — the net effect is IDENTICAL to the cascade pure order.
package stub

// Inferencer is the interface that the sidecar server calls to score a
// feature vector.
//
// internal/onnx.OnnxInferencer (G0/E11, `-tags onnx`) and this package's
// StubInferencer (default build / fail-safe fallback) both implement it.
type Inferencer interface {
	// Score takes a float32 feature vector and returns a pCTR score in [0,1].
	// Returns an error only on unrecoverable failures (e.g., model corrupt).
	// Transient errors should be absorbed and return (0, nil) to allow
	// fail-open at the client side.
	Score(features []float32) (float32, error)

	// ModelVersion returns the version string of the loaded model.
	// Used to populate the model_version field in the score response.
	ModelVersion() string

	// Close releases any resources held by the inferencer.
	Close() error
}

// StubInferencer is a no-op inference engine that returns score=0 for every
// input. Used as the default backend and as the fail-safe fallback when no
// ONNX model is available or loadable (see cmd/ranker-sidecar/main.go).
//
// With score=0 for all candidates, the sort in MLRanker.Rank is stable and
// preserves the deterministic cascade order — the net effect on Decision.Candidates
// is ZERO (same order, score=0 in every slot). This is correct and expected
// whenever the stub is the active backend.
type StubInferencer struct {
	version string
}

// NewStub creates a StubInferencer with the given model version string.
// Use "stub-j1" as the version to clearly identify pre-model traffic in the
// Decision log (ml_fail_open=false, model_version="stub-j1", score=0).
func NewStub(version string) *StubInferencer {
	return &StubInferencer{version: version}
}

// Score always returns 0.0, nil.
func (s *StubInferencer) Score(_ []float32) (float32, error) {
	return 0.0, nil
}

// ModelVersion returns the configured version string (e.g., "stub-j1").
func (s *StubInferencer) ModelVersion() string {
	return s.version
}

// Close is a no-op for the stub.
func (s *StubInferencer) Close() error {
	return nil
}
