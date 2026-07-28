// calibration_test.go has NO build constraint: LoadMap/Apply/Wrap are pure
// Go/JSON/math (no CGO, no onnxruntime), so this suite runs in the DEFAULT
// hermetic build (`go test ./...`, no `-tags onnx`) — unlike
// internal/wiring/calibration_parity_test.go (`-tags onnx`), which requires
// libonnxruntime.so and is not currently exercised by CI (see
// .github/workflows/ml.yml, which is Python-only). This file is therefore
// the ONLY calibration coverage that actually runs on every ordinary
// `go build`/`go test`, and must independently catch a broken or bypassed
// calibration step.
package calibration

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// LoadMap
// ---------------------------------------------------------------------------

func writeMapFile(t *testing.T, dir string, body string) string {
	t.Helper()
	path := filepath.Join(dir, "calibration_map.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writeMapFile: %v", err)
	}
	return path
}

func TestLoadMap_MissingFile(t *testing.T) {
	_, err := LoadMap(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("LoadMap on a missing file must return an error (fail-open decision belongs to the caller)")
	}
}

func TestLoadMap_MalformedJSON(t *testing.T) {
	path := writeMapFile(t, t.TempDir(), `{not valid json`)
	if _, err := LoadMap(path); err == nil {
		t.Fatal("LoadMap on malformed JSON must return an error")
	}
}

func TestLoadMap_RejectsEmptyThresholds(t *testing.T) {
	path := writeMapFile(t, t.TempDir(), `{"calibration_method":"isotonic","thresholds":[],"calibrated_probs":[]}`)
	if _, err := LoadMap(path); err == nil {
		t.Fatal("LoadMap must reject an empty calibration map")
	}
}

func TestLoadMap_RejectsLengthMismatch(t *testing.T) {
	path := writeMapFile(t, t.TempDir(),
		`{"calibration_method":"isotonic","thresholds":[0.0,1.0,2.0],"calibrated_probs":[0.0,1.0]}`)
	if _, err := LoadMap(path); err == nil {
		t.Fatal("LoadMap must reject mismatched thresholds/calibrated_probs lengths")
	}
}

func TestLoadMap_RejectsNonMonotonicThresholds(t *testing.T) {
	// Mirrors the mutation the dossier's receipt applies on the Python side
	// (inverting the map) but expressed directly on the on-disk artefact:
	// a decreasing thresholds array must be rejected, not silently accepted
	// and fed into a binary search that assumes sorted order.
	path := writeMapFile(t, t.TempDir(),
		`{"calibration_method":"isotonic","thresholds":[0.0,0.5,0.2],"calibrated_probs":[0.0,0.5,1.0]}`)
	if _, err := LoadMap(path); err == nil {
		t.Fatal("LoadMap must reject non-monotonic (decreasing) thresholds")
	}
}

func TestLoadMap_ValidRoundTrip(t *testing.T) {
	path := writeMapFile(t, t.TempDir(),
		`{"calibration_method":"isotonic","feature_spec_version":"1.0.0","thresholds":[0.0,1.0,2.0],"calibrated_probs":[0.0,0.5,1.0]}`)
	m, err := LoadMap(path)
	if err != nil {
		t.Fatalf("LoadMap on a valid map must succeed: %v", err)
	}
	if m.Method != "isotonic" {
		t.Errorf("Method = %q, want %q", m.Method, "isotonic")
	}
	if len(m.Thresholds) != 3 || len(m.CalibratedProbs) != 3 {
		t.Errorf("unexpected map length: %+v", m)
	}
}

// ---------------------------------------------------------------------------
// Apply — must reproduce ml/calibration/calibrate.py:apply_calibration
// (np.interp, out_of_bounds="clip") bit-for-bit-tolerant.
// ---------------------------------------------------------------------------

func closeEnough(a, b float32) bool {
	return math.Abs(float64(a)-float64(b)) < 1e-6
}

func TestApply_InterpolatesLinearly(t *testing.T) {
	m := &Map{Thresholds: []float64{0, 1, 2}, CalibratedProbs: []float64{0, 0.5, 1.0}}

	cases := []struct {
		raw  float32
		want float32
	}{
		{0.0, 0.0},
		{1.0, 0.5}, // exact threshold hit
		{2.0, 1.0},
		{0.5, 0.25}, // midpoint of [0,1] segment
		{1.5, 0.75}, // midpoint of [1,2] segment
	}
	for _, c := range cases {
		got := m.Apply(c.raw)
		if !closeEnough(got, c.want) {
			t.Errorf("Apply(%v) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestApply_ClipsOutOfBounds(t *testing.T) {
	// out_of_bounds="clip" (sklearn.IsotonicRegression default, mirrored by
	// ml/calibration/calibrate.py:fit_calibration): values outside the
	// thresholds range clip to the nearest endpoint's calibrated value
	// rather than extrapolating.
	m := &Map{Thresholds: []float64{0.1, 0.5, 0.9}, CalibratedProbs: []float64{0.2, 0.5, 0.8}}

	if got := m.Apply(-5.0); !closeEnough(got, 0.2) {
		t.Errorf("Apply(-5.0) = %v, want clip to 0.2 (below-range)", got)
	}
	if got := m.Apply(5.0); !closeEnough(got, 0.8) {
		t.Errorf("Apply(5.0) = %v, want clip to 0.8 (above-range)", got)
	}
}

// TestApply_InvertedMapDivergesFromCorrect proves the discriminating power
// this package's parity gate relies on: applying the SAME raw score through
// a correct map versus an inverted one must produce meaningfully different
// results, otherwise a gate comparing served output to a golden could not
// distinguish "calibrated correctly" from "calibrated backwards".
func TestApply_InvertedMapDivergesFromCorrect(t *testing.T) {
	thresholds := []float64{0.0, 0.25, 0.5, 0.75, 1.0}
	correct := []float64{0.05, 0.10, 0.30, 0.65, 0.95}
	inverted := make([]float64, len(correct))
	for i, v := range correct {
		inverted[len(correct)-1-i] = v
	}

	mCorrect := &Map{Thresholds: thresholds, CalibratedProbs: correct}
	mInverted := &Map{Thresholds: thresholds, CalibratedProbs: inverted}

	raw := float32(0.9)
	got := mCorrect.Apply(raw)
	inv := mInverted.Apply(raw)
	if math.Abs(float64(got)-float64(inv)) < 0.1 {
		t.Fatalf("correct=%v and inverted=%v map outputs too close — this fixture cannot discriminate a flipped map", got, inv)
	}
}

// ---------------------------------------------------------------------------
// CalibratedInferencer — proves Wrap actually routes Score through Apply.
// ---------------------------------------------------------------------------

// fakeInferencer is a minimal stub.Inferencer test double that always
// returns a fixed raw score, so CalibratedInferencer's behaviour can be
// tested in isolation from the real ONNX runtime.
type fakeInferencer struct {
	score   float32
	err     error
	version string
}

func (f *fakeInferencer) Score(_ []float32) (float32, error) { return f.score, f.err }
func (f *fakeInferencer) ModelVersion() string               { return f.version }
func (f *fakeInferencer) Close() error                       { return nil }

func TestCalibratedInferencer_AppliesMapToInnerScore(t *testing.T) {
	inner := &fakeInferencer{score: 0.5, version: "fake-v1"}
	m := &Map{Thresholds: []float64{0, 1}, CalibratedProbs: []float64{0, 1}}
	// Identity map at 0.5 would be 0.5 — use a non-identity map so the test
	// cannot pass by accident if Apply is bypassed.
	m2 := &Map{Thresholds: []float64{0, 1}, CalibratedProbs: []float64{0.9, 0.9}}

	wrapped := Wrap(inner, m2)
	got, err := wrapped.Score(nil)
	if err != nil {
		t.Fatalf("Score returned error: %v", err)
	}
	if !closeEnough(got, 0.9) {
		t.Errorf("Score() = %v, want 0.9 (the calibrated value, not inner.score=%v)", got, inner.score)
	}
	if got == inner.score {
		t.Errorf("Score() == inner raw score (%v) — calibration was not applied", inner.score)
	}

	if wrapped.ModelVersion() != "fake-v1" {
		t.Errorf("ModelVersion() = %q, want delegation to inner (%q)", wrapped.ModelVersion(), "fake-v1")
	}

	_ = m // silence unused in case identity map isn't needed above
}

func TestCalibratedInferencer_PropagatesInnerError(t *testing.T) {
	innerErr := errors.New("boom")
	inner := &fakeInferencer{score: 0, err: innerErr}
	m := &Map{Thresholds: []float64{0, 1}, CalibratedProbs: []float64{0, 1}}

	wrapped := Wrap(inner, m)
	_, err := wrapped.Score(nil)
	if !errors.Is(err, innerErr) {
		t.Errorf("Score() error = %v, want propagation of inner error %v", err, innerErr)
	}
}
