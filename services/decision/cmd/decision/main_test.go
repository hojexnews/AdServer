package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	decisionv1 "github.com/hojex/adserver/gen/go/adserver/decision/v1"
	"github.com/hojex/adserver/internal/cascade"
	mlranker "github.com/hojex/adserver/internal/ranker"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// captureSink records every Decision emitted (fire-and-forget, synchronous in tests).
type captureSink struct {
	mu        sync.Mutex
	decisions []*decisionv1.Decision
}

func (s *captureSink) Emit(_ context.Context, d *decisionv1.Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decisions = append(s.decisions, d)
}

func (s *captureSink) last() *decisionv1.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.decisions) == 0 {
		return nil
	}
	return s.decisions[len(s.decisions)-1]
}

// buildTestSnap builds a minimal snapshot with one zone, one campaign, one banner.
func buildTestSnap(tier snapshot.Tier) *snapshot.Snapshot {
	s := snapshot.EmptySnapshot()
	s.Zones["z1"] = &snapshot.Zone{ID: "z1", TenantID: "t1", Active: true}
	camp := &snapshot.Campaign{
		ID:           "c1",
		TenantID:     "t1",
		Tier:         tier,
		Priority:     10,
		BannerIDs:    []string{"b1"},
		ZoneIDs:      []string{"z1"},
		Active:       true,
		PricingModel: snapshot.PricingCPM,
	}
	if tier == snapshot.TierRemnant {
		camp.ECPM = &moneyv1.Money{AssetCode: "BRL", Amount: 200, Scale: 2}
	}
	if tier == snapshot.TierContract {
		camp.GoalImpressions = 1000
		camp.DeliveredImpressions = 500
	}
	s.Campaigns["c1"] = camp
	s.ZoneCampaigns["z1"] = []string{"c1"}
	s.Banners["b1"] = &snapshot.Banner{
		ID:         "b1",
		TenantID:   "t1",
		CampaignID: "c1",
		ImageURL:   "https://cdn.example.com/b1.jpg",
		ClickURL:   "https://click.example.com/b1",
		Active:     true,
	}
	return s
}

// testHandlerOption configures optional fields on the *decisionHandler built
// by newTestDecisionHandler, applied AFTER the base struct literal.  This
// keeps newTestDecisionHandler(snap, sink) backward-compatible (zero-value
// abRouter/banditML/etc, exactly as before) for every pre-existing caller,
// while letting new tests opt in to J4 A/B wiring without duplicating the
// handler construction boilerplate.
type testHandlerOption func(*decisionHandler)

// withTestABRouter wires an ABRouter + banditML into the test handler, the
// same two fields main() wires at boot when AB_ENABLED=true (see main.go's
// mux.Handle("POST /v1/decide", ...) literal). This is what lets a test
// actually drive the h.abRouter != nil branch inside ServeHTTP's per-request
// ranker-selection switch — no test in this package did this before (see
// TestJ4_ABSwitch_* below).
func withTestABRouter(router *mlranker.ABRouter, banditML *mlranker.MLRanker) testHandlerOption {
	return func(h *decisionHandler) {
		h.abRouter = router
		h.banditML = banditML
	}
}

func newTestDecisionHandler(snap *snapshot.Snapshot, sink DecisionSink, opts ...testHandlerOption) *decisionHandler {
	store := snapshot.NewStore(snap)
	rulesEngine := rules.New()
	eng := cascade.New(rulesEngine)
	h := &decisionHandler{
		snap:        store,
		cascade:     eng,
		sink:        sink,
		logger:      nil,
		clickSigner: nil, // no click tokens in these unit tests
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func decideRequest(zoneID string) *http.Request {
	body, _ := json.Marshal(DecideRequest{ZoneID: zoneID})
	r := httptest.NewRequest(http.MethodPost, "/v1/decide", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// ---------------------------------------------------------------------------
// J0: propensity logging invariants (Fase 2 gate)
// ---------------------------------------------------------------------------

// TestJ0_Decision_Propensity1_DeterministicPolicy verifies that:
//   - Propensity = 1.0 (cascata pura DA-3 baseline for OPE)
//   - ExplorationPolicy = DETERMINISTIC (not UNSPECIFIED)
//   - Epsilon = 0 (no exploration in J0)
//   - MlFailOpen = false (no ML yet; J1 flips this on degradation)
//   - decision_id is non-empty and present in both the Decision envelope
//     and the HTTP response (TX-1)
//   - model_version = "" (cascade-only, distinguishable by J4 from ML traffic)
func TestJ0_Decision_Propensity1_DeterministicPolicy(t *testing.T) {
	sink := &captureSink{}
	h := newTestDecisionHandler(buildTestSnap(snapshot.TierRemnant), sink)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, decideRequest("z1"))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	d := sink.last()
	if d == nil {
		t.Fatal("no Decision emitted to sink")
	}

	// Propensity must be 1.0 — the deterministic cascade always serves the
	// top candidate with certainty given the context (OPE baseline).
	if d.GetPropensity() != 1.0 {
		t.Errorf("Propensity = %v, want 1.0 (deterministic cascade baseline)", d.GetPropensity())
	}

	// Exploration policy must be DETERMINISTIC (not UNSPECIFIED — J0 is
	// explicitly deterministic, not "unknown"; this matters for OPE filtering).
	wantPolicy := decisionv1.ExplorationPolicy_EXPLORATION_POLICY_DETERMINISTIC
	if d.GetExplorationPolicy() != wantPolicy {
		t.Errorf("ExplorationPolicy = %v, want DETERMINISTIC", d.GetExplorationPolicy())
	}

	// Epsilon = 0: no random exploration in cascade-only mode.
	if d.GetEpsilon() != 0 {
		t.Errorf("Epsilon = %v, want 0", d.GetEpsilon())
	}

	// MlFailOpen = false: no ML sidecar present, no fail-open occurred.
	// J1 extension point: when the ML ranker times out, set MlFailOpen=true.
	// The field is explicitly false here (not just proto3 zero-value default)
	// to signal "J0 state: no ML was attempted".
	if d.GetMlFailOpen() {
		t.Errorf("MlFailOpen = true, want false (no ML ranker in J0)")
	}
}

// TestJ0_DecisionID_NonEmpty_InEnvelopeAndResponse verifies TX-1:
// decision_id MUST appear in both the Decision envelope AND the HTTP response,
// for every decision including blank impressions.
func TestJ0_DecisionID_NonEmpty_InEnvelopeAndResponse(t *testing.T) {
	sink := &captureSink{}
	h := newTestDecisionHandler(buildTestSnap(snapshot.TierRemnant), sink)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, decideRequest("z1"))

	// Check HTTP response carries decision_id.
	var resp DecideResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.DecisionID == "" {
		t.Error("DecideResponse.DecisionID is empty — TX-1 violation")
	}
	if len(resp.DecisionID) != 26 {
		t.Errorf("DecisionID length = %d, want 26 (ULID format)", len(resp.DecisionID))
	}

	// Check the emitted Decision envelope carries the same decision_id.
	d := sink.last()
	if d == nil {
		t.Fatal("no Decision emitted")
	}
	envDecisionID := d.GetEnvelope().GetDecisionId()
	if envDecisionID == "" {
		t.Error("Decision.Envelope.DecisionId is empty — TX-1 violation")
	}
	if envDecisionID != resp.DecisionID {
		t.Errorf("decision_id mismatch: response=%q, envelope=%q", resp.DecisionID, envDecisionID)
	}
}

// TestJ0_ModelVersion_Empty_CascadeOnly verifies that model_version="" in both
// the emitted Decision and the HTTP response when no ML ranker is active.
// J4 uses model_version="" to identify pre-ML traffic for OPE segregation.
func TestJ0_ModelVersion_Empty_CascadeOnly(t *testing.T) {
	sink := &captureSink{}
	h := newTestDecisionHandler(buildTestSnap(snapshot.TierRemnant), sink)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, decideRequest("z1"))

	var resp DecideResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// model_version must be "" for cascade-only decisions (DA-3, no ML ranker).
	// Convention: empty string = "cascade-only" per decision.proto comment.
	// Non-empty model_version signals an ML ranker is (or was) active.
	if resp.ModelVersion != "" {
		t.Errorf("ModelVersion = %q, want \"\" (cascade-only convention)", resp.ModelVersion)
	}

	d := sink.last()
	if d == nil {
		t.Fatal("no Decision emitted")
	}
	if d.GetEnvelope().GetModelVersion() != "" {
		t.Errorf("Decision.Envelope.ModelVersion = %q, want \"\" (cascade-only)",
			d.GetEnvelope().GetModelVersion())
	}
}

// TestJ0_Candidates_PopulatedForWinningStratum verifies that Candidates[]
// contains the candidates from the winning stratum with:
//   - At least one candidate (the winner) with Served=true.
//   - All candidates having Propensity=1.0.
//   - CandidateCount matches len(Candidates).
func TestJ0_Candidates_PopulatedForWinningStratum(t *testing.T) {
	sink := &captureSink{}
	h := newTestDecisionHandler(buildTestSnap(snapshot.TierRemnant), sink)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, decideRequest("z1"))

	d := sink.last()
	if d == nil {
		t.Fatal("no Decision emitted")
	}

	if len(d.GetCandidates()) == 0 {
		t.Fatal("Candidates[] must be non-empty for a non-blank decision")
	}

	// CandidateCount must match the slice length (no truncation in J0).
	if d.GetCandidateCount() != uint32(len(d.GetCandidates())) {
		t.Errorf("CandidateCount=%d != len(Candidates)=%d",
			d.GetCandidateCount(), len(d.GetCandidates()))
	}

	// Exactly one candidate must be served.
	var servedCount int
	for _, c := range d.GetCandidates() {
		if c.GetServed() {
			servedCount++
		}
		// All must have Propensity=1.0.
		if c.GetPropensity() != 1.0 {
			t.Errorf("candidate %q: Propensity=%v, want 1.0", c.GetCampaignId(), c.GetPropensity())
		}
	}
	if servedCount != 1 {
		t.Errorf("exactly 1 candidate must have Served=true, got %d", servedCount)
	}
}

// TestJ0_BlankDecision_HasDecisionID_NoCandidates verifies that blank
// impressions (no eligible candidates) still carry a valid decision_id (TX-1)
// but have an empty Candidates[].
func TestJ0_BlankDecision_HasDecisionID_NoCandidates(t *testing.T) {
	// Empty snapshot: all decisions will be BLANK.
	emptySnap := snapshot.EmptySnapshot()
	emptySnap.Zones["z1"] = &snapshot.Zone{ID: "z1", TenantID: "t1", Active: true}

	sink := &captureSink{}
	h := newTestDecisionHandler(emptySnap, sink)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, decideRequest("z1"))

	var resp DecideResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// TX-1: decision_id must be present even for blank.
	if resp.DecisionID == "" {
		t.Error("blank decision: DecisionID must not be empty (TX-1)")
	}
	if resp.ServedTier != "SERVED_TIER_BLANK" {
		t.Errorf("expected SERVED_TIER_BLANK, got %q", resp.ServedTier)
	}

	d := sink.last()
	if d == nil {
		t.Fatal("no Decision emitted for blank impression")
	}
	if len(d.GetCandidates()) != 0 {
		t.Errorf("blank decision must have empty Candidates[], got %d", len(d.GetCandidates()))
	}
}

// TestJ0_TenantID_ServerDerived verifies CA-1: tenant_id in the response
// is derived from the zone snapshot, NOT from any client-supplied value.
func TestJ0_TenantID_ServerDerived(t *testing.T) {
	sink := &captureSink{}
	h := newTestDecisionHandler(buildTestSnap(snapshot.TierRemnant), sink)

	// Attempt to inject a fake tenant_id by including it in the zone_id
	// (the handler derives tenant from the snapshot zone, not from client input).
	body, _ := json.Marshal(DecideRequest{ZoneID: "z1"})
	r := httptest.NewRequest(http.MethodPost, "/v1/decide", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	var resp DecideResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// TenantID must be the server-derived value from the snapshot zone.
	if resp.TenantID != "t1" {
		t.Errorf("TenantID = %q, want %q (server-derived from zone snapshot, CA-1)", resp.TenantID, "t1")
	}
}

// TestJ0_Override_Contract_Remnant_AllCarryCandidates verifies that
// the candidates logging works for all three non-blank tiers.
func TestJ0_Override_Contract_Remnant_AllCarryCandidates(t *testing.T) {
	tiers := []struct {
		name string
		tier snapshot.Tier
		want string
	}{
		{"override", snapshot.TierOverride, "SERVED_TIER_OVERRIDE"},
		{"contract", snapshot.TierContract, "SERVED_TIER_CONTRACT"},
		{"remnant", snapshot.TierRemnant, "SERVED_TIER_REMNANT"},
	}

	for _, tc := range tiers {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sink := &captureSink{}
			h := newTestDecisionHandler(buildTestSnap(tc.tier), sink)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, decideRequest("z1"))

			d := sink.last()
			if d == nil {
				t.Fatal("no Decision emitted")
			}
			if len(d.GetCandidates()) == 0 {
				t.Fatalf("%s: Candidates[] empty — must have at least 1 candidate", tc.name)
			}
			if d.GetPropensity() != 1.0 {
				t.Errorf("%s: Propensity=%v, want 1.0", tc.name, d.GetPropensity())
			}
			if d.GetExplorationPolicy() != decisionv1.ExplorationPolicy_EXPLORATION_POLICY_DETERMINISTIC {
				t.Errorf("%s: ExplorationPolicy=%v, want DETERMINISTIC", tc.name, d.GetExplorationPolicy())
			}
			if d.GetMlFailOpen() {
				t.Errorf("%s: MlFailOpen=true, want false (J0: no ML)", tc.name)
			}
			// Served tier in Decision must match expectation.
			if d.GetServedTier().String() != tc.want {
				t.Errorf("%s: ServedTier=%v, want %v", tc.name, d.GetServedTier(), tc.want)
			}
		})
	}
}

// TestJ0_DecisionEmittedWithinDeadline verifies that the decision handler
// completes well within the TX-4 latency budget when no Redis/ML is involved.
// This is a coarse smoke test — true p99 measurement happens in benchmarks.
func TestJ0_DecisionEmittedWithinDeadline(t *testing.T) {
	sink := &captureSink{}
	h := newTestDecisionHandler(buildTestSnap(snapshot.TierRemnant), sink)

	const budget = 50 * time.Millisecond // very conservative; hot path target is <1ms
	start := time.Now()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, decideRequest("z1"))
	elapsed := time.Since(start)

	if elapsed > budget {
		t.Errorf("decision took %v, exceeds %v budget — hot path regression", elapsed, budget)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// J4: A/B routing switch — handler-level coverage (audit fix)
//
// Prior to these tests, NOTHING in this repository constructed a
// *decisionHandler with a non-nil abRouter/banditML and drove it through
// ServeHTTP. tests/parity/ab_parity_test.go exercises the ranker package's
// cascade + MLRanker wiring directly (cascade.WithRanker), which never
// touches the actual "inTreatment = !d.IsControl()" branch in ServeHTTP
// (main.go, decisionHandler.ServeHTTP, the per-request ranker-selection
// switch). newTestDecisionHandler's only constructor never set abRouter, so
// h.abRouter was always nil in every prior test in this package — the `case
// h.abRouter != nil && inTreatment && h.banditML != nil:` /
// `case h.abRouter != nil:` arms of that switch were dead code as far as the
// test suite was concerned.
//
// These two tests close that gap: they build a real *decisionHandler with
// abRouter/banditML wired (via the new withTestABRouter option) and drive it
// through the actual HTTP entry point (ServeHTTP), then inspect the Decision
// the handler emits to the sink (the ONLY place Propensity/ExplorationPolicy/
// MlFailOpen are observable — DecideResponse, the JSON body, does not carry
// them).
//
// Mutation-tested (go test -overlay, tree untouched): flipping
// "inTreatment = !d.IsControl()" to "inTreatment = d.IsControl()" turns both
// tests red. See the audit note in this comment block's sibling commit for
// the exact go test invocations and exit codes.
// ---------------------------------------------------------------------------

// TestJ4_ABSwitch_AllControl_MatchesCascadePure drives decisionHandler.ServeHTTP
// with an ABRouter configured for 100% control (TreatmentBuckets: 0) and a
// banditML pointing at a socket that does not exist. If the ranker-selection
// switch ever mis-routes this all-control config into the treatment branch,
// banditML.Rank is invoked, dials the nonexistent socket, and fails open —
// which is externally observable via MlFailOpen flipping to true. That is
// the assertion this test relies on to be non-tautological (see mutation
// note above): the other invariants (propensity/model_version/policy/tier)
// are unfortunately ALSO satisfied by the fail-open path (fail-open returns
// the unmodified candidate order and leaves propensity/policy at their
// pre-switch defaults), so MlFailOpen is the one field that actually
// distinguishes "control never called ML" from "treatment called ML and
// failed open".
func TestJ4_ABSwitch_AllControl_MatchesCascadePure(t *testing.T) {
	snap := buildTestSnap(snapshot.TierRemnant)

	// Pure-cascade baseline: a handler with NO abRouter/banditML at all (the
	// same construction every other test in this file already uses).
	pureSink := &captureSink{}
	pureHandler := newTestDecisionHandler(snap, pureSink)
	pureRR := httptest.NewRecorder()
	pureHandler.ServeHTTP(pureRR, decideRequest("z1"))
	pureDecision := pureSink.last()
	if pureDecision == nil {
		t.Fatal("pure cascade baseline: no Decision emitted")
	}

	// AB handler: 100% control + a banditML wired to a socket that will
	// never exist in a test environment (no infra, deterministic fail-open
	// if — and only if — it is ever called).
	router := mlranker.NewABRouter(mlranker.ABConfig{TreatmentBuckets: 0})
	banditML := mlranker.New("/tmp/nonexistent-j4-allcontrol.sock", 5*time.Millisecond, nil)
	sink := &captureSink{}
	h := newTestDecisionHandler(snap, sink, withTestABRouter(router, banditML))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, decideRequest("z1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	d := sink.last()
	if d == nil {
		t.Fatal("no Decision emitted")
	}

	if d.GetPropensity() != 1.0 {
		t.Errorf("control: Propensity = %v, want 1.0", d.GetPropensity())
	}
	if d.GetEnvelope().GetModelVersion() != "" {
		t.Errorf("control: Envelope.ModelVersion = %q, want \"\"", d.GetEnvelope().GetModelVersion())
	}
	wantPolicy := decisionv1.ExplorationPolicy_EXPLORATION_POLICY_DETERMINISTIC
	if d.GetExplorationPolicy() != wantPolicy {
		t.Errorf("control: ExplorationPolicy = %v, want DETERMINISTIC", d.GetExplorationPolicy())
	}
	// THE non-tautological assertion (see func doc): control must never
	// attempt an ML call. If the switch mis-routes control traffic into the
	// treatment arm, banditML dials the nonexistent socket and fails open,
	// which flips MlFailOpen to true.
	if d.GetMlFailOpen() {
		t.Error("control: MlFailOpen = true, want false — ML must NOT be attempted on the control arm " +
			"(if this fired, the A/B switch routed control traffic into the treatment branch)")
	}
	if d.GetCampaignId() != pureDecision.GetCampaignId() {
		t.Errorf("control: campaign_id = %q, want cascade-pure %q", d.GetCampaignId(), pureDecision.GetCampaignId())
	}
	if d.GetServedTier() != pureDecision.GetServedTier() {
		t.Errorf("control: served_tier = %v, want cascade-pure %v", d.GetServedTier(), pureDecision.GetServedTier())
	}
}

// TestJ4_ABSwitch_AllTreatment_PreservesTierAndFailsOpen drives
// decisionHandler.ServeHTTP with an ABRouter configured for 100% treatment
// (TreatmentBuckets: mlranker.NumBuckets) and a banditML pointing at a
// nonexistent socket — a deterministic fail-open with no live sidecar
// required (this test has no network/infra dependency, per TX-4 fail-open
// semantics). It is the companion to
// TestJ4_ABSwitch_AllControl_MatchesCascadePure: together they bracket the
// A/B switch from both sides.
//
// Non-tautological assertion: MlFailOpen must be true, because the
// treatment arm DOES attempt the ML call (and, with no sidecar present,
// fails open). If the switch mis-routes all-treatment config into the
// control branch, banditML.Rank is never invoked and MlFailOpen stays false
// — this flips under the same mutation described in the sibling test's doc.
//
// DA-3 is also checked directly: even though this is the treatment arm, the
// served tier/campaign must be IDENTICAL to the pure cascade order, because
// fail-open returns candidates unmodified (no live model to re-rank with).
func TestJ4_ABSwitch_AllTreatment_PreservesTierAndFailsOpen(t *testing.T) {
	snap := buildTestSnap(snapshot.TierRemnant)

	pureSink := &captureSink{}
	pureHandler := newTestDecisionHandler(snap, pureSink)
	pureRR := httptest.NewRecorder()
	pureHandler.ServeHTTP(pureRR, decideRequest("z1"))
	pureDecision := pureSink.last()
	if pureDecision == nil {
		t.Fatal("pure cascade baseline: no Decision emitted")
	}

	router := mlranker.NewABRouter(mlranker.ABConfig{TreatmentBuckets: mlranker.NumBuckets})
	banditML := mlranker.New("/tmp/nonexistent-j4-alltreatment.sock", 5*time.Millisecond, nil)
	sink := &captureSink{}
	h := newTestDecisionHandler(snap, sink, withTestABRouter(router, banditML))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, decideRequest("z1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	d := sink.last()
	if d == nil {
		t.Fatal("no Decision emitted")
	}

	// THE non-tautological assertion (see func doc): treatment must attempt
	// the ML call. With no sidecar present it fails open, so MlFailOpen must
	// be true — the mirror image of the control test's assertion.
	if !d.GetMlFailOpen() {
		t.Error("treatment: MlFailOpen = false, want true — ML must be attempted on the treatment arm " +
			"(if this fired, the A/B switch routed treatment traffic into the control branch)")
	}
	// DA-3: fail-open degrades treatment to the cascade pure order — tier and
	// campaign must be unchanged, never blank, never a different stratum.
	if d.GetServedTier() != pureDecision.GetServedTier() {
		t.Errorf("treatment: served_tier = %v, want cascade-pure %v (DA-3)", d.GetServedTier(), pureDecision.GetServedTier())
	}
	if d.GetCampaignId() != pureDecision.GetCampaignId() {
		t.Errorf("treatment: campaign_id = %q, want cascade-pure %q", d.GetCampaignId(), pureDecision.GetCampaignId())
	}
}
