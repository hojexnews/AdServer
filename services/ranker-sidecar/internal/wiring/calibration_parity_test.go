//go:build onnx

// calibration_parity_test.go proves that the SERVED score — the actual
// output of wiring.BuildInferencer's Inferencer.Score, i.e. what
// cmd/ranker-sidecar/main.go writes to the wire in production — has passed
// through the isotonic calibration map, not just the raw .onnx graph
// output.
//
// This is the gate that "calibration-never-applied-in-serving" found
// missing: ml/calibration/calibrate.py:test_calibrate.py already proved
// apply_calibration's MATH is correct in isolation, but nothing proved the
// SIDECAR actually calls it. Before internal/calibration and internal/wiring
// existed, this test would have been impossible to write meaningfully,
// because there was no calibration step anywhere in the Go serving path —
// see internal/onnx/onnx.go's Score, which returned data[1] (the raw ONNX
// output) directly.
//
// Two regressions this test MUST catch (protocol step 5 — verified by
// mutation, not just written):
//
//  1. "Remove the application" — if wiring.BuildInferencer stops calling
//     calibration.Wrap (e.g., someone reverts to returning onnxInf
//     unwrapped), Score returns pCTR_raw. The golden's
//     expected_calibrated_score differs from expected_raw_score by a wide
//     margin for every fixture case (see calibrated_score_golden.json),
//     so the numeric comparison below fails, AND the explicit type
//     assertion on the returned Inferencer fails first with a clearer
//     message.
//  2. "Invert the map" — if ml/registry/artifacts/calibration_map.json is
//     corrupted/inverted on disk (the calibration.Map this test's
//     wiring.BuildInferencer call loads FRESH from disk), the computed
//     score diverges from the FROZEN golden expected_calibrated_score
//     (which was captured once, from the correct map, at fixture-generation
//     time) — exactly mirroring how onnx_parity_test.go's score_golden.json
//     catches a corrupted .onnx artefact.
//
// # Portability (local dev without libonnxruntime) / CI wiring
//
// Built ONLY with `-tags onnx`. TestCalibratedServingParity skips (not
// fails) if ONNXRUNTIME_SHARED_LIBRARY_PATH is unset/unloadable, or if the
// compiled model or calibration map artefacts are missing — mirroring
// internal/onnx/onnx_parity_test.go so `go test -tags onnx ./...` stays safe
// on a bare local checkout without the native runtime library installed.
//
// This is a graceful-degradation path for local dev ONLY. In CI, the
// go-onnx-parity job (.github/workflows/go.yml) both provisions
// ONNXRUNTIME_SHARED_LIBRARY_PATH (the native .so the pip `onnxruntime`
// wheel already embeds) and runs `make ranker-onnx-fixtures` to generate the
// model/calibration artefacts before this test runs, then greps the `-v`
// output for `--- SKIP` and fails the job if this test (or any other) ever
// skips there — the skip path above must never be silently exercised by a
// green CI run. (30th-wave remediation of HIGH
// "calibration-never-applied-in-serving": before this job existed, no CI
// workflow ever compiled with `-tags onnx` at all, so this test only ever
// ran the skip path, everywhere.)
//
// # Regenerating the golden fixture (testdata/calibrated_score_golden.json)
//
// Since the 30th-wave remediation of "calibration-never-applied-in-serving"
// (go-onnx-parity CI job, .github/workflows/go.yml), this golden is captured
// from a small DETERMINISTIC synthetic model (fixed seed, trains in
// milliseconds) rather than the full production training pipeline — see
// services/ranker-sidecar/scripts/gen_calibration_fixtures.py's module
// docstring for the determinism contract (verified by running the generator
// twice and diffing the resulting golden byte-for-byte). CI runs this same
// script (via `make ranker-onnx-fixtures`) before `go test -tags onnx`, so
// the artifacts it regenerates every run reproduce the exact scores below.
//
// Regenerate (from the repo root, only after intentionally changing the
// recipe in gen_calibration_fixtures.py — e.g. the seed or the synthetic
// training signal):
//
//	PYTHONPATH=. ml/.venv/bin/python \
//	  services/ranker-sidecar/scripts/gen_calibration_fixtures.py --write-golden
//
// This also regenerates internal/onnx/testdata/score_golden.json (same
// underlying model) in one invocation — the two files must never drift
// apart, since both fixtures describe raw/calibrated scores of the SAME
// artifact.
package wiring

import (
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hojex/adserver/services/ranker-sidecar/internal/calibration"
)

type calibratedGoldenCase struct {
	ID                      string    `json:"id"`
	Features                []float32 `json:"features"`
	ExpectedRawScore        float32   `json:"expected_raw_score"`
	ExpectedCalibratedScore float32   `json:"expected_calibrated_score"`
}

type calibratedGoldenFile struct {
	FeatureSpecVersion string                 `json:"feature_spec_version"`
	ModelPath          string                 `json:"model_path"`       // relative to repo root
	CalibrationPath    string                 `json:"calibration_path"` // relative to repo root
	Cases              []calibratedGoldenCase `json:"cases"`
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("calibration_parity_test: runtime.Caller failed to resolve source path")
	}
	// thisFile: <root>/services/ranker-sidecar/internal/wiring/calibration_parity_test.go
	dir := filepath.Dir(thisFile)
	root := filepath.Join(dir, "..", "..", "..", "..")
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("calibration_parity_test: resolve repo root: %v", err)
	}
	return abs
}

// servingScoreTolerance mirrors internal/onnx/onnx_parity_test.go's
// scoreTolerance: GBDT inference + float64 linear interpolation on the same
// data should match the Python reference near bit-for-bit; 1e-5 absorbs
// float32/float64 accumulation-order differences between the two languages'
// arithmetic, while being far tighter than the raw-vs-calibrated gap in the
// golden fixture (>1e-3 for every case), so it cannot mask a missing or
// inverted calibration step.
const servingScoreTolerance = 1e-5

// minRawCalGap is the minimum |raw - calibrated| a golden case must exhibit
// for the numeric comparison below to be capable of catching "calibration
// removed" (Score falls back to pCTR_raw). If a future fixture regeneration
// ever produced a case where raw happens to ~equal calibrated, this sanity
// check fails LOUDLY rather than silently losing coverage.
const minRawCalGap = 1e-3

// TestCalibratedServingParity proves the score actually written to the wire
// by the sidecar (wiring.BuildInferencer's Inferencer, constructed exactly
// as cmd/ranker-sidecar/main.go constructs it) matches
// ml/calibration/calibrate.py:apply_calibration applied to the real ONNX
// model's raw output — i.e., that calibration is not just implemented, but
// actually wired into serving.
func TestCalibratedServingParity(t *testing.T) {
	libPath := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH")
	if libPath == "" {
		t.Skip("ONNXRUNTIME_SHARED_LIBRARY_PATH not set — skipping (portable for CI without libonnxruntime)")
	}
	if _, err := os.Stat(libPath); err != nil {
		t.Skipf("ONNXRUNTIME_SHARED_LIBRARY_PATH=%q not found: %v — skipping", libPath, err)
	}

	root := repoRoot(t)

	goldenBytes, err := os.ReadFile(filepath.Join("testdata", "calibrated_score_golden.json"))
	if err != nil {
		t.Fatalf("read testdata/calibrated_score_golden.json: %v", err)
	}
	var golden calibratedGoldenFile
	if err := json.Unmarshal(goldenBytes, &golden); err != nil {
		t.Fatalf("unmarshal calibrated_score_golden.json: %v", err)
	}
	if len(golden.Cases) == 0 {
		t.Fatal("calibrated_score_golden.json has no cases")
	}

	modelPath := filepath.Join(root, filepath.FromSlash(golden.ModelPath))
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("compiled model %q not found: %v — skipping (run ml/training/train_pctr.py or the re-export step first)", modelPath, err)
	}
	calPath := filepath.Join(root, filepath.FromSlash(golden.CalibrationPath))
	if _, err := os.Stat(calPath); err != nil {
		t.Skipf("calibration map %q not found: %v — skipping (run ml/calibration/calibrate.py's run_calibration first)", calPath, err)
	}

	// Build the Inferencer via the EXACT production wiring — not a
	// reimplementation. If BuildInferencer (or calibration.Wrap inside it)
	// is ever changed to skip calibration, this call's return value stops
	// applying it and every comparison below fails.
	inf := BuildInferencer(Config{
		ModelPath:       modelPath,
		ModelVersion:    "calibration-parity-test",
		CalibrationPath: calPath,
	}, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer func() {
		if err := inf.Close(); err != nil {
			t.Logf("inf.Close: %v", err)
		}
	}()

	if _, ok := inf.(*calibration.CalibratedInferencer); !ok {
		t.Fatalf("wiring.BuildInferencer did not return a *calibration.CalibratedInferencer (got %T) — "+
			"calibration was not wired into serving despite a valid model and calibration map being present",
			inf)
	}

	for _, c := range golden.Cases {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			gap := math.Abs(float64(c.ExpectedCalibratedScore) - float64(c.ExpectedRawScore))
			if gap < minRawCalGap {
				t.Fatalf("golden case %q: |raw-calibrated|=%.2e is below minRawCalGap=%.0e — "+
					"this fixture case cannot distinguish a calibrated serve from an uncalibrated one; "+
					"regenerate the golden with a case where the isotonic map actually moves the score",
					c.ID, gap, minRawCalGap)
			}

			got, err := inf.Score(c.Features)
			if err != nil {
				t.Fatalf("Score(%q) returned an error (should always fail-open to (0,nil)): %v", c.ID, err)
			}
			diff := math.Abs(float64(got) - float64(c.ExpectedCalibratedScore))
			if diff > servingScoreTolerance {
				t.Errorf("case %q: served score=%.10f, Python apply_calibration golden=%.10f, diff=%.2e (tol=%.0e) — "+
					"raw (uncalibrated) golden was %.10f: if served==raw, calibration was not applied",
					c.ID, got, c.ExpectedCalibratedScore, diff, servingScoreTolerance, c.ExpectedRawScore)
			} else {
				t.Logf("case %q: served score=%.10f, Python golden=%.10f, diff=%.2e OK (raw was %.10f)",
					c.ID, got, c.ExpectedCalibratedScore, diff, c.ExpectedRawScore)
			}
		})
	}
}
