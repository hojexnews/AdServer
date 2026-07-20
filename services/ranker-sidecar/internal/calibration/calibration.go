// Package calibration implements stage (b) of the sidecar's two-stage
// scoring design documented in ml/calibration/calibrate.py's module
// docstring ("ONDE A CALIBRACAO VIVE"):
//
//	(a) infer pCTR_raw with the compiled .onnx graph (internal/onnx)
//	(b) apply calibration_map.json (isotonic thresholds/calibrated_probs,
//	    linearly interpolated) -> pCTR_cal
//
// Closes the "calibration-never-applied-in-serving" finding: before this
// package existed, calibration_map.json was produced by
// ml/calibration/calibrate.py and versioned in the MLflow registry, but no
// code anywhere in the serving path (services/ranker-sidecar) ever read it —
// internal/onnx.OnnxInferencer.Score returned the raw ONNX graph output
// unmodified, while internal/ranker/score.go's doc comment and
// ml/training/pipeline.py both described the multiplicand as "the calibrated
// sidecar output". Production served pCTR_RAW; eCPM = pCTR_RAW × bid.
//
// This package is compiled in BOTH build-tag variants of the sidecar
// (ADR-0002 §C hermetic default build): calibration itself is pure Go/JSON/
// math and never requires `-tags onnx` or CGO — only the underlying
// Inferencer it wraps (typically internal/onnx.OnnxInferencer) might.
//
// ml/calibration/calibrate.py's apply_calibration is the NORMATIVE
// reference: Apply below must reproduce
// np.interp(raw, thresholds, calibrated_probs).astype(np.float32)
// for the served value. See
// services/ranker-sidecar/internal/wiring/calibration_parity_test.go
// (`-tags onnx`) for the end-to-end serving-parity proof against a golden
// fixture generated from the real compiled model + the real calibration map.
package calibration

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/hojex/adserver/services/ranker-sidecar/internal/stub"
)

// Map is the in-memory, validated representation of a calibration_map.json
// artefact (schema: ml/calibration/calibrate.py:save_calibration_map).
//
// Thresholds/CalibratedProbs are kept as float64 — matching
// apply_calibration's internal computation
// (np.asarray(..., dtype=np.float64) on both the map and the raw input,
// interpolated in float64, then cast to float32 only at the very end) — so
// Apply's rounding behaviour tracks the Python reference as closely as
// possible instead of accumulating extra float32 rounding at each step.
type Map struct {
	Method          string
	FeatureSpecVer  string
	Thresholds      []float64
	CalibratedProbs []float64
}

// jsonMap mirrors calibration_map.json on disk.
type jsonMap struct {
	CalibrationMethod  string    `json:"calibration_method"`
	FeatureSpecVersion string    `json:"feature_spec_version"`
	Thresholds         []float64 `json:"thresholds"`
	CalibratedProbs    []float64 `json:"calibrated_probs"`
}

// LoadMap reads and validates a calibration_map.json artefact.
//
// Validation is defensive: the sidecar must fail-open (serve the raw score,
// never crash or refuse to start) on a malformed or missing map, so every
// failure mode here returns a plain error for the caller
// (wiring.BuildInferencer) to log and degrade from, never a panic.
//
//   - thresholds and calibrated_probs must both be non-empty and the same
//     length.
//   - thresholds must be non-decreasing — the precondition
//     ml/calibration/test_calibrate.py enforces on the producer side
//     (test_fit_calibration_thresholds_e_probs_nao_decrescentes) and that
//     Apply's binary search requires.
func LoadMap(path string) (*Map, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("calibration: read %q: %w", path, err)
	}
	var jm jsonMap
	if err := json.Unmarshal(raw, &jm); err != nil {
		return nil, fmt.Errorf("calibration: unmarshal %q: %w", path, err)
	}
	if len(jm.Thresholds) == 0 || len(jm.CalibratedProbs) == 0 {
		return nil, fmt.Errorf("calibration: %q has empty thresholds/calibrated_probs", path)
	}
	if len(jm.Thresholds) != len(jm.CalibratedProbs) {
		return nil, fmt.Errorf("calibration: %q thresholds/calibrated_probs length mismatch (%d != %d)",
			path, len(jm.Thresholds), len(jm.CalibratedProbs))
	}
	for i := 1; i < len(jm.Thresholds); i++ {
		if jm.Thresholds[i] < jm.Thresholds[i-1] {
			return nil, fmt.Errorf("calibration: %q thresholds not non-decreasing at index %d (%.6f < %.6f)",
				path, i, jm.Thresholds[i], jm.Thresholds[i-1])
		}
	}
	return &Map{
		Method:          jm.CalibrationMethod,
		FeatureSpecVer:  jm.FeatureSpecVersion,
		Thresholds:      jm.Thresholds,
		CalibratedProbs: jm.CalibratedProbs,
	}, nil
}

// Apply maps a raw pCTR (the .onnx graph's raw P(click=1) output) to the
// calibrated pCTR via linear interpolation over (Thresholds,
// CalibratedProbs) — the same algorithm and out-of-bounds semantics as
// ml/calibration/calibrate.py:apply_calibration (np.interp,
// out_of_bounds="clip": values outside [Thresholds[0], Thresholds[n-1]]
// clip to the nearest endpoint's calibrated value instead of extrapolating).
//
// O(log n) via sort.Search (binary search) rather than a linear scan — TX-4:
// the sidecar's 5-8ms p99 hot-path budget has no room for an O(n) scan on
// every scored candidate, even over a small (~50-point) map.
func (m *Map) Apply(raw float32) float32 {
	n := len(m.Thresholds)
	x := float64(raw)

	if x <= m.Thresholds[0] {
		return float32(m.CalibratedProbs[0])
	}
	if x >= m.Thresholds[n-1] {
		return float32(m.CalibratedProbs[n-1])
	}

	// Smallest i such that Thresholds[i] >= x. Because x is strictly
	// between Thresholds[0] and Thresholds[n-1] here, i lands in [1, n-1]
	// and the bracket (i-1, i) straddles x.
	i := sort.Search(n, func(i int) bool { return m.Thresholds[i] >= x })
	if m.Thresholds[i] == x {
		return float32(m.CalibratedProbs[i])
	}

	lo, hi := i-1, i
	x0, x1 := m.Thresholds[lo], m.Thresholds[hi]
	y0, y1 := m.CalibratedProbs[lo], m.CalibratedProbs[hi]
	if x1 == x0 {
		// Degenerate (duplicate-threshold) segment: no slope. Does not
		// occur in the production isotonic map (LoadMap only requires
		// non-decreasing, but PAVA breakpoints are distinct in practice),
		// but must not divide by zero if it ever did.
		return float32(y0)
	}
	t := (x - x0) / (x1 - x0)
	return float32(y0 + t*(y1-y0))
}

// ---------------------------------------------------------------------------
// CalibratedInferencer — wraps any stub.Inferencer with stage (b)
// ---------------------------------------------------------------------------

// CalibratedInferencer wraps a base stub.Inferencer (in production,
// internal/onnx.OnnxInferencer) and applies the isotonic calibration map to
// its raw score before returning it — stage (b) of the two-stage design
// documented in ml/calibration/calibrate.py.
//
// Fail-open (DA-3): if the base Inferencer's Score call itself fails open
// (returns 0, nil — e.g. malformed input, transient ORT error, or the
// process is running in stub mode), Apply is still invoked on that 0. Since
// every candidate that fails open in a given process receives the exact
// same raw value, Apply(0) produces the exact same constant for all of
// them — the stable-sort/no-op-preserves-cascade-order property documented
// in stub.StubInferencer is unaffected by calibration.
type CalibratedInferencer struct {
	inner stub.Inferencer
	m     *Map
}

// Wrap constructs a CalibratedInferencer.
//
// Callers (wiring.BuildInferencer) own the fail-open DECISION of whether to
// wrap at all: if calibration.LoadMap failed, the caller should serve inner
// UNWRAPPED (raw pCTR) rather than call Wrap — Wrap itself assumes m is
// valid and non-nil, and never re-validates it.
func Wrap(inner stub.Inferencer, m *Map) *CalibratedInferencer {
	return &CalibratedInferencer{inner: inner, m: m}
}

// Score runs the wrapped Inferencer then applies the calibration map to its
// result.
func (c *CalibratedInferencer) Score(features []float32) (float32, error) {
	raw, err := c.inner.Score(features)
	if err != nil {
		// stub.Inferencer's documented contract is that Score never returns
		// a non-nil error for transient failures (it fails open to (0,nil)
		// instead) — but defensively, never calibrate an error result.
		return raw, err
	}
	return c.m.Apply(raw), nil
}

// ModelVersion delegates to the wrapped Inferencer.
func (c *CalibratedInferencer) ModelVersion() string {
	return c.inner.ModelVersion()
}

// Close delegates to the wrapped Inferencer.
func (c *CalibratedInferencer) Close() error {
	return c.inner.Close()
}
