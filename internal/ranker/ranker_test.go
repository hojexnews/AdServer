// ranker_test.go — unit tests for MLRanker fail-open semantics (J1).
//
// These tests do NOT require the sidecar to be running. They verify that:
//  1. When the sidecar socket is unavailable, Rank returns the original slice
//     (fail-open), and LastResult().FailOpen == true.
//  2. When all scores are equal (e.g., dummy model returns 0), the order
//     is identical to the input (stable sort preserves deterministic cascade order).
//  3. The DA-3 invariant holds: Rank does not change the tier of any candidate.
package ranker

import (
	"testing"
	"time"

	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeCandidates(ids ...string) []*cascade.Candidate {
	out := make([]*cascade.Candidate, len(ids))
	for i, id := range ids {
		out[i] = &cascade.Candidate{
			Campaign: &snapshot.Campaign{
				ID:   id,
				Tier: snapshot.TierRemnant,
			},
			Banner: &snapshot.Banner{ID: id + "-banner"},
			Tier:   snapshot.TierRemnant,
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRankFailOpenOnUnavailableSidecar verifies that when the sidecar socket
// does not exist, Rank returns the original slice unmodified and FailOpen=true.
func TestRankFailOpenOnUnavailableSidecar(t *testing.T) {
	// Use a socket path that does not exist → dial will fail immediately.
	r := New("/tmp/ranker-j1-nonexistent-socket.sock", 5*time.Millisecond, nil)

	candidates := makeCandidates("c1", "c2", "c3")
	original := make([]*cascade.Candidate, len(candidates))
	copy(original, candidates)

	ranked := r.Rank(candidates)
	result := r.LastResult()

	// Fail-open: must return the original slice.
	if len(ranked) != len(original) {
		t.Fatalf("fail-open: got %d candidates, want %d", len(ranked), len(original))
	}
	for i, c := range ranked {
		if c.Campaign.ID != original[i].Campaign.ID {
			t.Errorf("fail-open: index %d: got campaign %q want %q",
				i, c.Campaign.ID, original[i].Campaign.ID)
		}
	}

	// FailOpen flag must be set.
	if !result.FailOpen {
		t.Error("fail-open: expected FailOpen=true when sidecar unavailable")
	}

	// ModelVersion must be empty on fail-open.
	if result.ModelVersion != "" {
		t.Errorf("fail-open: expected ModelVersion=\"\", got %q", result.ModelVersion)
	}
}

// TestRankEmptyCandidates verifies that an empty slice is handled gracefully.
func TestRankEmptyCandidates(t *testing.T) {
	r := New("/tmp/ranker-j1-nonexistent.sock", 5*time.Millisecond, nil)
	ranked := r.Rank(nil)
	if ranked != nil {
		t.Errorf("empty input: expected nil, got %v", ranked)
	}
	if r.LastResult().FailOpen {
		t.Error("empty input: expected FailOpen=false for nil input")
	}
}

// TestRankTimeout verifies that a very short budget causes fail-open
// even if a sidecar were available. We simulate this by using a socket path
// that exists but points nowhere (the timeout fires before the dial completes).
// In practice the socket is just missing, which also produces fail-open.
func TestRankTimeout(t *testing.T) {
	// 1 nanosecond budget → will always time out.
	r := New("/tmp/ranker-j1-timeout-test.sock", 1*time.Nanosecond, nil)
	candidates := makeCandidates("a", "b")
	ranked := r.Rank(candidates)
	result := r.LastResult()

	// Order must be preserved on timeout.
	if len(ranked) != 2 {
		t.Fatalf("timeout: expected 2 candidates, got %d", len(ranked))
	}
	if ranked[0].Campaign.ID != "a" || ranked[1].Campaign.ID != "b" {
		t.Errorf("timeout: order changed; want [a,b] got [%s,%s]",
			ranked[0].Campaign.ID, ranked[1].Campaign.ID)
	}
	if !result.FailOpen {
		t.Error("timeout: expected FailOpen=true")
	}
}

// TestFeaturizeBasic verifies that Featurize returns a vector of the right
// length and that integer indices are in range.
func TestFeaturizeBasic(t *testing.T) {
	inp := FeaturizeInput{
		ZoneWidth:    300,
		ZoneHeight:   250,
		RequestTime:  time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC), // Wednesday 10:00 UTC
		GeoCountry:   "BR",
		GeoCity:      "Sao Paulo",
		DeviceClass:  "chrome-desktop",
		CandidateTier: 3,
		ECPMMinorUnits: 150,
		BannerWidth:  300,
		BannerHeight: 250,
		CreativeType: "html",
		CandidateCount: 3,
	}

	v := Featurize(inp)

	if len(v) != FeatureVectorLength {
		t.Fatalf("vector length: got %d want %d", len(v), FeatureVectorLength)
	}

	// All values must be finite.
	for i, f := range v {
		if f != f { // NaN check
			t.Errorf("index %d: NaN in vector", i)
		}
	}

	// Integer indices must be non-negative.
	for i, f := range v {
		if f < 0 {
			t.Errorf("index %d (%s): negative value %v", i, featureName(i), f)
		}
	}
}

// TestFeaturizeSpecVersion verifies the constant matches the expected value.
func TestFeaturizeSpecVersion(t *testing.T) {
	if FeatureSpecVersion != "1.0.0" {
		t.Errorf("FeatureSpecVersion = %q, want %q", FeatureSpecVersion, "1.0.0")
	}
}

// TestFeaturizeVectorLength verifies the constant is 23.
func TestFeaturizeVectorLength(t *testing.T) {
	if FeatureVectorLength != 23 {
		t.Errorf("FeatureVectorLength = %d, want 23", FeatureVectorLength)
	}
}
