// Package stub provides the StubInferencer — a placeholder inference engine
// for J1 (pre-model phase).
//
// J2 INTEGRATION POINT:
//   Replace StubInferencer with OnnxInferencer (see onnx.go.template alongside
//   this file). OnnxInferencer wraps github.com/yalue/onnxruntime_go and loads
//   the compiled .onnx artefact from the MLflow registry path.
//
//   Steps for J2:
//     1. Download the compiled .onnx from the MLflow registry:
//          mlflow artifacts download -r <run_id> -a model.onnx -d /opt/ranker/
//     2. Set RANKER_MODEL_PATH=/opt/ranker/model.onnx in the sidecar environment.
//     3. Ensure libonnxruntime.so.1 is present in LD_LIBRARY_PATH (or embed it).
//     4. Swap StubInferencer for OnnxInferencer in cmd/ranker-sidecar/main.go:
//          inferencer := onnx.New(modelPath) // build tag: onnx
//          server := sidecar.NewServer(socketPath, inferencer, modelVersion, logger)
//     5. The wire protocol (length-prefixed JSON over UDS) is unchanged.
//     6. Run internal/ranker/parity_test.go to confirm the vector contract is met.
//
// Why stub for J1:
//   - The ONNX model artefact does not exist yet (produced in J2).
//   - github.com/yalue/onnxruntime_go requires CGO and libonnxruntime.so,
//     which is not available in this build environment.
//   - The sidecar wire protocol and server loop are fully functional; only the
//     model inference is stubbed out.
//   - StubInferencer returns score=0 for all inputs. With the stable-sort in
//     internal/ranker/ranker.go, equal scores preserve the deterministic cascade
//     order — the net effect is IDENTICAL to the cascade pure order (J1 invariant).
package stub

// Inferencer is the interface that the sidecar server calls to score a
// feature vector.
//
// J2 provides OnnxInferencer implementing this interface.
// J1 ships StubInferencer.
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
// input. Used in J1 before the real ONNX model is available.
//
// With score=0 for all candidates, the sort in MLRanker.Rank is stable and
// preserves the deterministic cascade order — the net effect on Decision.Candidates
// is ZERO (same order, score=0 in every slot). This is correct and expected for J1.
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
