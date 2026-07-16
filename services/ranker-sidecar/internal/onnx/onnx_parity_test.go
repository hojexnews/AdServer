//go:build onnx

// onnx_parity_test.go proves that OnnxInferencer.Score (Go, via
// github.com/yalue/onnxruntime_go) reproduces the reference Python
// onnxruntime score for the SAME compiled model
// (ml/registry/artifacts/pctr_model.onnx) and the SAME canonical feature
// vectors used across the whole anti-skew fixture suite
// (ml/features/testdata/parity_cases.json).
//
// This is the inference-side counterpart to internal/ranker/parity_test.go
// (which only proves Featurize/featurize vector parity, not model scoring).
//
// # Portability (CI without libonnxruntime)
//
// This file is built ONLY with `-tags onnx`. Even then, TestOnnxScoreParity
// skips (not fails) if ONNXRUNTIME_SHARED_LIBRARY_PATH is unset or does not
// point at a loadable shared library, and if the compiled model artefact is
// missing — so `go test -tags onnx ./...` remains safe to run in
// environments that build with CGO but do not have the runtime library
// installed.
//
// # Regenerating the golden fixture (testdata/score_golden.json)
//
// The golden "features"/"expected_score" pairs are the SAME
// expected_vector_computed vectors from ml/features/testdata/parity_cases.json,
// scored by Python onnxruntime directly against pctr_model.onnx. Regenerate
// with (from the repo root, after re-exporting pctr_model.onnx):
//
//	ml/.venv/bin/python -c "
//	import json
//	import numpy as np
//	import onnxruntime as ort
//
//	with open('ml/features/testdata/parity_cases.json') as f:
//	    fixtures = json.load(f)
//	sess = ort.InferenceSession('ml/registry/artifacts/pctr_model.onnx',
//	                             providers=['CPUExecutionProvider'])
//	input_name = sess.get_inputs()[0].name
//	cases = []
//	for case in fixtures['cases']:
//	    vec = [0.0] * 23
//	    for key, value in case['expected_vector_computed'].items():
//	        if not key.startswith('index_'):
//	            continue
//	        vec[int(key.split('_')[1])] = float(value)
//	    X = np.array([vec], dtype=np.float32)
//	    probs = sess.run(None, {input_name: X})[1]
//	    cases.append({'id': case['id'], 'features': vec,
//	                  'expected_score': float(probs[0, 1])})
//	print(json.dumps({'feature_spec_version': fixtures['feature_spec_version'],
//	                   'model_path': 'ml/registry/artifacts/pctr_model.onnx',
//	                   'cases': cases}, indent=2))
//	"
package onnx

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// scoreGoldenCase mirrors one entry of testdata/score_golden.json.
type scoreGoldenCase struct {
	ID            string    `json:"id"`
	Features      []float32 `json:"features"`
	ExpectedScore float32   `json:"expected_score"`
}

type scoreGoldenFile struct {
	FeatureSpecVersion string            `json:"feature_spec_version"`
	ModelPath          string            `json:"model_path"` // relative to repo root
	Cases              []scoreGoldenCase `json:"cases"`
}

// repoRoot resolves the repository root from this test file's own location
// (services/ranker-sidecar/internal/onnx/onnx_parity_test.go), independent
// of the test runner's working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("onnx_parity_test: runtime.Caller failed to resolve source path")
	}
	// thisFile: <root>/services/ranker-sidecar/internal/onnx/onnx_parity_test.go
	dir := filepath.Dir(thisFile)
	root := filepath.Join(dir, "..", "..", "..", "..")
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("onnx_parity_test: resolve repo root: %v", err)
	}
	return abs
}

// scoreTolerance is the max absolute diff allowed between the Go
// OnnxInferencer score and the Python onnxruntime golden score. GBDT
// inference on the same graph should match near bit-for-bit (float32); a
// generous-but-tight 1e-5 absorbs float32 accumulation order differences
// between the two onnxruntime language bindings.
const scoreTolerance = 1e-5

func TestOnnxScoreParity(t *testing.T) {
	libPath := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH")
	if libPath == "" {
		t.Skip("ONNXRUNTIME_SHARED_LIBRARY_PATH not set — skipping (portable for CI without libonnxruntime)")
	}
	if _, err := os.Stat(libPath); err != nil {
		t.Skipf("ONNXRUNTIME_SHARED_LIBRARY_PATH=%q not found: %v — skipping", libPath, err)
	}

	root := repoRoot(t)

	goldenBytes, err := os.ReadFile(filepath.Join("testdata", "score_golden.json"))
	if err != nil {
		t.Fatalf("read testdata/score_golden.json: %v", err)
	}
	var golden scoreGoldenFile
	if err := json.Unmarshal(goldenBytes, &golden); err != nil {
		t.Fatalf("unmarshal score_golden.json: %v", err)
	}
	if len(golden.Cases) == 0 {
		t.Fatal("score_golden.json has no cases")
	}

	modelPath := filepath.Join(root, filepath.FromSlash(golden.ModelPath))
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("compiled model %q not found: %v — skipping (run ml/training/train_pctr.py or the re-export step first)", modelPath, err)
	}

	inf, err := New(modelPath, "onnx-parity-test")
	if err != nil {
		t.Fatalf("onnx.New(%q): %v", modelPath, err)
	}
	defer func() {
		if err := inf.Close(); err != nil {
			t.Logf("onnx.Close: %v", err)
		}
	}()

	for _, c := range golden.Cases {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			if len(c.Features) != numFeatures {
				t.Fatalf("golden case %q: expected %d features, got %d", c.ID, numFeatures, len(c.Features))
			}
			got, err := inf.Score(c.Features)
			if err != nil {
				t.Fatalf("Score(%q) returned an error (should always fail-open to (0,nil)): %v", c.ID, err)
			}
			diff := math.Abs(float64(got) - float64(c.ExpectedScore))
			if diff > scoreTolerance {
				t.Errorf("case %q: Go score=%.10f, Python golden=%.10f, diff=%.2e (tol=%.0e)",
					c.ID, got, c.ExpectedScore, diff, scoreTolerance)
			} else {
				t.Logf("case %q: Go score=%.10f, Python golden=%.10f, diff=%.2e OK",
					c.ID, got, c.ExpectedScore, diff)
			}
		})
	}
}

// TestOnnxScore_MalformedInput_FailsOpen proves the fail-open contract:
// a feature vector of the wrong length must never error the connection —
// it returns (0, nil), matching stub.Inferencer's documented semantics.
func TestOnnxScore_MalformedInput_FailsOpen(t *testing.T) {
	libPath := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH")
	if libPath == "" {
		t.Skip("ONNXRUNTIME_SHARED_LIBRARY_PATH not set — skipping")
	}
	if _, err := os.Stat(libPath); err != nil {
		t.Skipf("ONNXRUNTIME_SHARED_LIBRARY_PATH=%q not found: %v — skipping", libPath, err)
	}

	root := repoRoot(t)
	modelPath := filepath.Join(root, "ml", "registry", "artifacts", "pctr_model.onnx")
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("compiled model %q not found: %v — skipping", modelPath, err)
	}

	inf, err := New(modelPath, "onnx-parity-test")
	if err != nil {
		t.Fatalf("onnx.New(%q): %v", modelPath, err)
	}
	defer func() {
		_ = inf.Close()
	}()

	score, err := inf.Score([]float32{1, 2, 3}) // wrong length
	if err != nil {
		t.Fatalf("Score with malformed input must fail-open (0,nil), got error: %v", err)
	}
	if score != 0 {
		t.Errorf("Score with malformed input must fail-open to 0, got %v", score)
	}
}
