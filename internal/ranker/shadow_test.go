// shadow_test.go — Tests for ShadowRanker and bandit exploration (J3).
//
// Parity invariants verified here:
//  1. ShadowRanker.Rank returns the ORIGINAL cascade order (never shadow order).
//  2. With SHADOW_ENABLED=false (no ShadowRanker in the cascade), the output
//     is identical to the pure cascade — golden tests remain green.
//  3. With SHADOW_ENABLED=true (ShadowRanker wrapping MLRanker), the output
//     is still identical to the cascade deterministic order (shadow records
//     the ML order but does not serve it).
//  4. BanditEnabled=false: ExploreRank returns the original order with prop=1.0.
//  5. The ShadowSink receives the shadow record with the correct fields.
//  6. DiscardSink never blocks.
//  7. ChannelSink drops records when full (non-blocking on hot path).
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

func makeShadowCandidates(ids ...string) []*cascade.Candidate {
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

// captureSink captures shadow records in a slice for inspection.
type captureSink struct {
	records []ShadowRecord
}

func (c *captureSink) EmitShadow(rec ShadowRecord) {
	c.records = append(c.records, rec)
}

// ---------------------------------------------------------------------------
// 1. ShadowRanker returns original cascade order
// ---------------------------------------------------------------------------

// TestShadowRanker_ServesOriginalOrder is the PARITY INVARIANT test.
// The shadow ranker MUST return the cascade order unmodified.
// This is what makes shadow-ON safe: the served result is identical to
// running the cascade without any shadow ranker.
func TestShadowRanker_ServesOriginalOrder(t *testing.T) {
	// Use an unavailable sidecar (MLRanker will fail-open).
	// fail-open means inner ranker returns original order with all scores=0.
	inner := New("/tmp/shadow-test-noexist.sock", 5*time.Millisecond, nil)
	sink := &captureSink{}

	sr := NewShadowRanker(inner, sink, "test-model", nil)
	sr.SetRequestContext("decision-001", "zone-A")

	candidates := makeShadowCandidates("c1", "c2", "c3")
	original := []string{"c1", "c2", "c3"} // capture original order

	served := sr.Rank(candidates)

	// The served slice MUST have the same order as the input (cascade order).
	if len(served) != len(original) {
		t.Fatalf("served: got %d candidates, want %d", len(served), len(original))
	}
	for i, c := range served {
		if c.Campaign.ID != original[i] {
			t.Errorf("served[%d]: got %q want %q (cascade order must be preserved)",
				i, c.Campaign.ID, original[i])
		}
	}
}

// TestShadowRanker_DoesNotAlterTierOrCampaign verifies DA-3:
// the shadow ranker does not change the Tier or campaign assignment.
func TestShadowRanker_DoesNotAlterTierOrCampaign(t *testing.T) {
	inner := New("/tmp/shadow-test-noexist2.sock", 5*time.Millisecond, nil)
	sr := NewShadowRanker(inner, DiscardSink{}, "v0", nil)
	sr.SetRequestContext("d-002", "zone-B")

	candidates := makeShadowCandidates("override1", "override2")
	for _, c := range candidates {
		c.Tier = snapshot.TierOverride
		c.Campaign.Tier = snapshot.TierOverride
	}

	served := sr.Rank(candidates)

	for _, c := range served {
		if c.Tier != snapshot.TierOverride {
			t.Errorf("DA-3 violated: Tier changed to %v for %q", c.Tier, c.Campaign.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. ShadowRanker sends record to sink
// ---------------------------------------------------------------------------

// TestShadowRanker_EmitsToSink verifies that the shadow record is emitted
// with the correct fields after Rank.
func TestShadowRanker_EmitsToSink(t *testing.T) {
	inner := New("/tmp/shadow-test-noexist3.sock", 5*time.Millisecond, nil)
	sink := &captureSink{}
	sr := NewShadowRanker(inner, sink, "model-v1", nil)

	decisionID := "decision-xyz"
	zoneID := "zone-123"
	sr.SetRequestContext(decisionID, zoneID)

	candidates := makeShadowCandidates("camp-A", "camp-B")
	sr.Rank(candidates)

	if len(sink.records) != 1 {
		t.Fatalf("expected 1 shadow record, got %d", len(sink.records))
	}
	rec := sink.records[0]

	if rec.DecisionID != decisionID {
		t.Errorf("DecisionID: got %q want %q", rec.DecisionID, decisionID)
	}
	if rec.ZoneID != zoneID {
		t.Errorf("ZoneID: got %q want %q", rec.ZoneID, zoneID)
	}
	if rec.ModelVersion != "model-v1" {
		t.Errorf("ModelVersion: got %q want %q", rec.ModelVersion, "model-v1")
	}

	// CascadeOrder must reflect the original order.
	if len(rec.CascadeOrder) != 2 {
		t.Fatalf("CascadeOrder: expected 2 elements, got %d", len(rec.CascadeOrder))
	}
	if rec.CascadeOrder[0] != "camp-A" || rec.CascadeOrder[1] != "camp-B" {
		t.Errorf("CascadeOrder: got %v want [camp-A camp-B]", rec.CascadeOrder)
	}

	// ServedCampaignID is the first cascade candidate.
	if rec.ServedCampaignID != "camp-A" {
		t.Errorf("ServedCampaignID: got %q want %q", rec.ServedCampaignID, "camp-A")
	}
}

// ---------------------------------------------------------------------------
// 3. ShadowRanker.LastShadowRecord
// ---------------------------------------------------------------------------

func TestShadowRanker_LastShadowRecord(t *testing.T) {
	inner := New("/tmp/shadow-noexist4.sock", 5*time.Millisecond, nil)
	sr := NewShadowRanker(inner, DiscardSink{}, "v2", nil)
	sr.SetRequestContext("dec-999", "zone-Z")

	candidates := makeShadowCandidates("x", "y", "z")
	sr.Rank(candidates)

	last := sr.LastShadowRecord()
	if last.DecisionID != "dec-999" {
		t.Errorf("LastShadowRecord.DecisionID: got %q want %q", last.DecisionID, "dec-999")
	}
	if len(last.CascadeOrder) != 3 {
		t.Errorf("LastShadowRecord.CascadeOrder: expected 3 elements, got %d", len(last.CascadeOrder))
	}
}

// ---------------------------------------------------------------------------
// 4. Empty candidates — no crash
// ---------------------------------------------------------------------------

func TestShadowRanker_EmptyCandidates(t *testing.T) {
	inner := New("/tmp/shadow-noexist5.sock", 5*time.Millisecond, nil)
	sink := &captureSink{}
	sr := NewShadowRanker(inner, sink, "v0", nil)
	sr.SetRequestContext("d-empty", "z-empty")

	served := sr.Rank(nil)
	if served != nil {
		t.Error("expected nil for nil input")
	}
	// No shadow record should be emitted for empty input.
	if len(sink.records) != 0 {
		t.Errorf("expected 0 shadow records for empty input, got %d", len(sink.records))
	}
}

// ---------------------------------------------------------------------------
// 5. BanditEnabled=false is a no-op (parity with cascade)
// ---------------------------------------------------------------------------

// TestBandit_DisabledIsNoOp verifies the J3 parity invariant for bandits:
// when BanditEnabled=false, ExploreRank returns the original slice unchanged.
func TestBandit_DisabledIsNoOp(t *testing.T) {
	cfg := BanditConfig{BanditEnabled: false}
	candidates := makeShadowCandidates("a", "b", "c")
	scores := []float32{0.8, 0.5, 0.3}

	result := ExploreRank(cfg, candidates, scores, nil)

	// Order must be unchanged.
	if len(result.Ordered) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(result.Ordered))
	}
	for i, c := range result.Ordered {
		if c.Campaign.ID != candidates[i].Campaign.ID {
			t.Errorf("index %d: got %q want %q (order should not change when bandit disabled)",
				i, c.Campaign.ID, candidates[i].Campaign.ID)
		}
	}

	// Propensity must be 1.0 (deterministic).
	if result.Propensity != 1.0 {
		t.Errorf("propensity: got %v want 1.0 (bandit disabled)", result.Propensity)
	}

	// Epsilon must be 0.
	if result.Epsilon != 0 {
		t.Errorf("epsilon: got %v want 0 (bandit disabled)", result.Epsilon)
	}

	// Not explored.
	if result.Explored {
		t.Error("Explored must be false when bandit is disabled")
	}
}

// TestBandit_DisabledWithNilCandidates — no crash on nil input.
func TestBandit_DisabledWithNilCandidates(t *testing.T) {
	cfg := BanditConfig{BanditEnabled: false}
	result := ExploreRank(cfg, nil, nil, nil)
	if result.Ordered != nil {
		t.Error("expected nil ordered for nil input")
	}
	if result.Propensity != 1.0 {
		t.Errorf("propensity: got %v want 1.0", result.Propensity)
	}
}

// TestBandit_EnabledEpsilonGreedyExploit verifies that epsilon=0 (pure exploit)
// never shuffles.
func TestBandit_EpsilonGreedyExploit(t *testing.T) {
	cfg := BanditConfig{
		BanditEnabled: true,
		Policy:        BanditPolicyEpsilonGreedy,
		Epsilon:       0.0, // pure exploit
	}
	candidates := makeShadowCandidates("best", "second", "third")
	scores := []float32{0.9, 0.5, 0.2}

	// Run 100 times — must always return the original order.
	for i := 0; i < 100; i++ {
		result := ExploreRank(cfg, candidates, scores, nil)
		if result.Ordered[0].Campaign.ID != "best" {
			t.Errorf("iter %d: expected 'best' first with eps=0, got %q",
				i, result.Ordered[0].Campaign.ID)
			break
		}
		if result.Propensity != 1.0 {
			t.Errorf("iter %d: propensity with eps=0 must be 1.0, got %v", i, result.Propensity)
			break
		}
	}
}

// TestBandit_EpsilonGreedyPropensityRange verifies that propensity is in (0, 1].
func TestBandit_EpsilonGreedyPropensityRange(t *testing.T) {
	cfg := BanditConfig{
		BanditEnabled: true,
		Policy:        BanditPolicyEpsilonGreedy,
		Epsilon:       0.2,
	}
	candidates := makeShadowCandidates("a", "b", "c", "d")
	scores := []float32{0.7, 0.5, 0.3, 0.1}

	for i := 0; i < 200; i++ {
		result := ExploreRank(cfg, candidates, scores, nil)
		if result.Propensity <= 0 || result.Propensity > 1.0 {
			t.Errorf("iter %d: propensity out of (0,1]: got %v", i, result.Propensity)
			break
		}
	}
}

// TestBandit_ThompsonDisabledIsNoOp — when BanditEnabled=false even with
// Thompson policy, ExploreRank is a no-op.
func TestBandit_ThompsonDisabledIsNoOp(t *testing.T) {
	cfg := BanditConfig{
		BanditEnabled: false,
		Policy:        BanditPolicyThompson,
		ThompsonBeta:  10.0,
	}
	candidates := makeShadowCandidates("a", "b")
	scores := []float32{0.6, 0.4}

	result := ExploreRank(cfg, candidates, scores, nil)
	if result.Ordered[0].Campaign.ID != "a" {
		t.Error("expected original order when bandit disabled")
	}
	if result.Propensity != 1.0 {
		t.Errorf("propensity: got %v want 1.0", result.Propensity)
	}
}

// ---------------------------------------------------------------------------
// 6. DiscardSink never blocks
// ---------------------------------------------------------------------------

func TestDiscardSink_NeverBlocks(t *testing.T) {
	ds := DiscardSink{}
	// Emit many records — must not block or panic.
	for i := 0; i < 10000; i++ {
		ds.EmitShadow(ShadowRecord{DecisionID: "d"})
	}
}

// ---------------------------------------------------------------------------
// 7. ChannelSink drops when full (non-blocking)
// ---------------------------------------------------------------------------

func TestChannelSink_DropsWhenFull(t *testing.T) {
	// Buffer size 2 — fill it, then check that further emits don't block.
	sink := NewChannelSink(2, nil)

	// Fill the buffer.
	sink.EmitShadow(ShadowRecord{DecisionID: "r1"})
	sink.EmitShadow(ShadowRecord{DecisionID: "r2"})

	// This call must return immediately (non-blocking drop).
	done := make(chan struct{})
	go func() {
		sink.EmitShadow(ShadowRecord{DecisionID: "r3-dropped"})
		close(done)
	}()

	select {
	case <-done:
		// Good — returned immediately.
	case <-time.After(50 * time.Millisecond):
		t.Error("ChannelSink.EmitShadow blocked when buffer was full")
	}

	// The channel should have exactly 2 records (not 3).
	if len(sink.Chan()) != 2 {
		t.Errorf("expected 2 records in channel, got %d", len(sink.Chan()))
	}
}

// ---------------------------------------------------------------------------
// 8. Parity gate: ShadowRanker wrapper vs. direct cascade (shadow OFF ≡ shadow ON)
// ---------------------------------------------------------------------------

// TestShadowParityGate is the key parity gate for J3:
// running the cascade with a ShadowRanker wrapping the MLRanker produces
// the SAME served order as running the cascade with no ranker at all.
//
// This is equivalent to verifying that the shadow has zero effect on the
// billing-relevant output — which is what the parity-golden-test-guardian audits.
func TestShadowParityGate(t *testing.T) {
	candidates := makeShadowCandidates("camp1", "camp2", "camp3")

	// Scenario 1: pure cascade order (no shadow ranker).
	pureOrder := []string{"camp1", "camp2", "camp3"}

	// Scenario 2: ShadowRanker wrapping an unavailable sidecar (fail-open).
	inner := New("/tmp/shadow-parity-noexist.sock", 5*time.Millisecond, nil)
	sink := &captureSink{}
	sr := NewShadowRanker(inner, sink, "v0", nil)
	sr.SetRequestContext("parity-dec", "parity-zone")

	served := sr.Rank(candidates)

	// Verify that served order == pureOrder (cascade deterministic order).
	for i, c := range served {
		if c.Campaign.ID != pureOrder[i] {
			t.Errorf("parity gate: served[%d]=%q, want %q (shadow altered serving order!)",
				i, c.Campaign.ID, pureOrder[i])
		}
	}

	// And the shadow record was still emitted (shadow IS active, just doesn't serve).
	if len(sink.records) != 1 {
		t.Errorf("expected 1 shadow record to be emitted, got %d", len(sink.records))
	}
}

// ---------------------------------------------------------------------------
// Concurrency tests (Defeito 2 — race detector must report ZERO races)
// ---------------------------------------------------------------------------

// TestBanditRanker_ConcurrentRankAndLastResult spawns N goroutines that call
// Rank + LastResult + WithConfig in parallel on the same BanditRanker instance.
// Under -race, this would trigger a data race on .last and .cfg before the
// sync.Mutex fix.
func TestBanditRanker_ConcurrentRankAndLastResult(t *testing.T) {
	const goroutines = 20
	inner := New("/tmp/bandit-race-test-noexist.sock", 5*time.Millisecond, nil)
	br := NewBanditRanker(inner, BanditConfig{BanditEnabled: false}, nil)

	done := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			candidates := makeShadowCandidates("a", "b", "c")
			br.WithConfig(BanditConfig{BanditEnabled: false, Epsilon: float64(n) * 0.01})
			br.Rank(candidates)
			_ = br.LastResult()
		}(i)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
}
