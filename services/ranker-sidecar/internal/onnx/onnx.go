//go:build onnx

// Package onnx implements the production OnnxInferencer for the ranker
// sidecar (G0/E11 — ONNX Runtime nativo).
//
// It wraps github.com/yalue/onnxruntime_go to load the compiled GBDT model
// (ml/registry/artifacts/pctr_model.onnx, LightGBM -> ONNX) and score feature
// vectors received over the sidecar's Unix-socket wire protocol
// (services/ranker-sidecar/internal/stub/server.go).
//
// # Build tag (ADR-0002 §C — hermetic default build)
//
// This file is compiled ONLY with `-tags onnx`. The default build
// (`go build ./...`, no tags) never imports onnxruntime_go — see
// disabled.go, which ships the CGO-free stand-in compiled with `!onnx`.
// Only operators who explicitly opt in with `-tags onnx` (and provide
// libonnxruntime.so.*, e.g. via ONNXRUNTIME_SHARED_LIBRARY_PATH, or a
// system-installed onnxruntime.so on the library search path) link this
// code path. The hot-path Go binary otherwise stays CGO-free and
// reproducible.
//
// # Model contract (ml/training/train_pctr.py export_onnx, G0/E11)
//
//   - Input:  "features", float32, shape [N, 23]  (N=1 in the hot path;
//     matches internal/ranker.FeatureVectorLength).
//   - Output: "probabilities", float32, shape [N, 2] — column 1 = P(click=1).
//
// The graph is exported with onnxmltools zipmap=False: LightGBM's default
// ONNX export attaches a ZipMap operator (output type
// seq(map(int64, tensor(float)))), which is hostile to embedded runtimes
// that consume flat tensors directly. zipmap=False keeps "probabilities" as
// a dense tensor so this Go client never has to parse a
// sequence-of-maps — see ml/training/train_pctr.py for the export code and
// the booster<->onnx numeric equivalence check.
//
// # Fail-open semantics (ADR-0003 §A / DA-3)
//
// A transient inference error (bad tensor, Run() failure, unexpected output
// shape) returns (0, nil) from Score — matching stub.Inferencer's documented
// contract. The caller (server.go, and ultimately internal/ranker.MLRanker)
// treats score=0 for all candidates as a no-op re-ranking, i.e., the pure
// cascade order is preserved. Never a blank impression for an ML failure.
//
// Only a hard failure to LOAD the model at construction time (corrupt file,
// missing input/output names, ORT environment init failure) returns an
// error from New() — the caller (cmd/ranker-sidecar/main.go) must fall back
// to stub.NewStub in that case.
package onnx

import (
	"fmt"
	"os"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/hojex/adserver/services/ranker-sidecar/internal/stub"
)

const (
	// inputName/outputName mirror the tensor names baked into the ONNX graph
	// by ml/training/train_pctr.py export_onnx (onnxmltools.convert_lightgbm
	// initial_types=[("features", ...)], zipmap=False).
	inputName  = "features"
	outputName = "probabilities"

	// numFeatures (the serving feature-vector length, =23) is defined in
	// features.go (no build tag) so features_contract_test.go can cross-check
	// it against internal/ranker.FeatureVectorLength in the default build.

	// numClasses is the width of the "probabilities" output tensor:
	// [P(click=0), P(click=1)].
	numClasses = 2
)

// ---------------------------------------------------------------------------
// Process-wide ORT environment (must be initialised exactly once)
// ---------------------------------------------------------------------------

var (
	envInitOnce sync.Once
	envInitErr  error
)

// initEnvironment initialises the onnxruntime C environment exactly once per
// process. If ONNXRUNTIME_SHARED_LIBRARY_PATH is set, it is used to locate
// libonnxruntime.so.* via dlopen (SetSharedLibraryPath); otherwise the
// library is looked up on the default search path ("onnxruntime.so").
func initEnvironment() error {
	envInitOnce.Do(func() {
		if p := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH"); p != "" {
			ort.SetSharedLibraryPath(p)
		}
		envInitErr = ort.InitializeEnvironment()
	})
	return envInitErr
}

// ---------------------------------------------------------------------------
// OnnxInferencer
// ---------------------------------------------------------------------------

// OnnxInferencer scores feature vectors using a compiled GBDT ONNX graph via
// onnxruntime. The session is loaded once at construction (New) and reused
// for every Score call.
//
// Concurrency: onnxruntime sessions support concurrent Run() calls from
// multiple goroutines as long as each call owns its own input/output
// tensors (the documented onnxruntime threading model — session state is
// read-only during inference). Score allocates a fresh input/output tensor
// pair per call and destroys them before returning, so OnnxInferencer is
// safe for concurrent use by the sidecar's per-connection goroutines
// (services/ranker-sidecar/internal/stub/server.go).
type OnnxInferencer struct {
	session *ort.DynamicAdvancedSession
	version string
}

// New loads the ONNX model at modelPath and returns an Inferencer backed by
// onnxruntime.
//
// Returns an error if:
//   - the ORT environment cannot be initialised (e.g., libonnxruntime.so.*
//     not found/loadable via dlopen), or
//   - the model fails to load (corrupt file, input/output name mismatch).
//
// This is a LOAD-time error path only — it is not fail-open. The caller
// (cmd/ranker-sidecar/main.go) is responsible for falling back to
// stub.NewStub when New returns an error, so the sidecar never refuses to
// start because of a bad model artefact.
func New(modelPath, modelVersion string) (stub.Inferencer, error) {
	if modelPath == "" {
		return nil, fmt.Errorf("onnx: empty model path")
	}
	if err := initEnvironment(); err != nil {
		return nil, fmt.Errorf("onnx: initialize ORT environment: %w", err)
	}

	session, err := ort.NewDynamicAdvancedSession(
		modelPath,
		[]string{inputName},
		[]string{outputName},
		nil, // default SessionOptions
	)
	if err != nil {
		return nil, fmt.Errorf("onnx: load model %q: %w", modelPath, err)
	}

	return &OnnxInferencer{session: session, version: modelVersion}, nil
}

// Score runs the ONNX session on a single feature vector and returns
// P(click=1) as a float32 clipped to [0,1].
//
// Fail-open (ADR-0003 §A / DA-3): any error building tensors, running the
// session, or an unexpected output shape is treated as TRANSIENT and
// returns (0, nil) — matching stub.Inferencer's documented contract. This
// function never returns a non-nil error; the signature keeps the error
// return only to satisfy the stub.Inferencer interface.
func (o *OnnxInferencer) Score(features []float32) (float32, error) {
	if len(features) != numFeatures {
		// Malformed input (anti-skew violation upstream) — fail-open rather
		// than erroring the connection; the caller degrades to cascade order.
		return 0, nil
	}

	inputShape := ort.NewShape(1, int64(numFeatures))
	inputTensor, err := ort.NewTensor(inputShape, features)
	if err != nil {
		return 0, nil // fail-open
	}
	defer inputTensor.Destroy() //nolint:errcheck // best-effort cleanup

	outputShape := ort.NewShape(1, int64(numClasses))
	outputTensor, err := ort.NewEmptyTensor[float32](outputShape)
	if err != nil {
		return 0, nil // fail-open
	}
	defer outputTensor.Destroy() //nolint:errcheck // best-effort cleanup

	if err := o.session.Run(
		[]ort.Value{inputTensor},
		[]ort.Value{outputTensor},
	); err != nil {
		return 0, nil // fail-open
	}

	data := outputTensor.GetData()
	if len(data) != numClasses {
		return 0, nil // fail-open — unexpected graph shape
	}

	score := data[1] // P(click=1); column 0 is P(click=0)
	switch {
	case score < 0:
		score = 0
	case score > 1:
		score = 1
	}
	return score, nil
}

// ModelVersion returns the configured model version string (as passed to
// New — typically the MLflow registry version tag).
func (o *OnnxInferencer) ModelVersion() string {
	return o.version
}

// Close releases the onnxruntime session. Safe to call once; subsequent
// calls are a no-op error from the underlying session (best-effort cleanup
// by callers, matching stub.StubInferencer.Close's no-op contract).
func (o *OnnxInferencer) Close() error {
	if o.session == nil {
		return nil
	}
	return o.session.Destroy()
}
