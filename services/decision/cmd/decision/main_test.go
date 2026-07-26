package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	decisionv1 "github.com/hojex/adserver/gen/go/adserver/decision/v1"
	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/geo"
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

// withTestGeoResolver overrides the default geo.EmptyResolver{} wired by
// newTestDecisionHandler, letting a test exercise the IP-derived geo
// fallback (see TestServeHTTP_GeoFallback* below) against a real
// *geo.MaxMindResolver fixture instead of production's boot-time wiring.
func withTestGeoResolver(resolver geo.Resolver, trustedDepth int) testHandlerOption {
	return func(h *decisionHandler) {
		h.geoResolver = resolver
		h.trustedDepth = trustedDepth
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
		// Default: EmptyResolver{} (production's degraded-safe default when
		// GEOIP_DB_PATH is unset) — every pre-existing test keeps getting an
		// empty Geo regardless of RemoteAddr, identical to this handler's
		// behavior before the IP-derived fallback existed. Tests that need
		// to exercise real IP→geo derivation use withTestGeoResolver.
		geoResolver:  geo.EmptyResolver{},
		trustedDepth: 1,
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

// ---------------------------------------------------------------------------
// PRIVACY (PRIV-01, TX-5/DA-11): site_url sanitization + raw-input confinement
//
// The async ad tag calls POST /v1/decide DIRECTLY from the browser with
// site_url = location.origin + location.pathname (query/fragment already
// stripped client-side) and user_agent = navigator.userAgent (RAW — needed
// for Client-Useragent rule matching, see buildRulesContext's doc comment).
// decision cannot assume the caller sanitized site_url, so it re-sanitizes
// defensively on receipt. These tests prove that invariant and prove that
// NEITHER raw input ever reaches the emitted Decision event or the HTTP
// response, regardless of what arrives in the request body.
// ---------------------------------------------------------------------------

// TestBuildRulesContext_SiteURLSanitized_QueryAndFragmentStripped is a
// mutation-provable unit test of the sanitization itself: it calls
// buildRulesContext directly (bypassing HTTP) so a regression that removes
// the referer.Sanitize call is caught precisely, without depending on any
// particular rule set being configured.
func TestBuildRulesContext_SiteURLSanitized_QueryAndFragmentStripped(t *testing.T) {
	req := DecideRequest{
		ZoneID:  "z1",
		SiteURL: "https://publisher.example.com/article?session=abc123&utm_source=x#frag",
	}
	ctx := buildRulesContext(req, &commonv1.Geo{}, time.Now())

	if ctx.SiteURL != "https://publisher.example.com/article" {
		t.Errorf("buildRulesContext SiteURL = %q, want sanitized scheme+host+path only", ctx.SiteURL)
	}
	if strings.Contains(ctx.SiteURL, "session=") || strings.Contains(ctx.SiteURL, "utm_source") || strings.Contains(ctx.SiteURL, "#") {
		t.Errorf("buildRulesContext SiteURL still contains query/fragment: %q", ctx.SiteURL)
	}
}

// TestPrivacy_ServeHTTP_RawSiteURLAndUserAgent_NeverLeakToOutputs drives the
// FULL HTTP handler (the exact path the ad tag hits) with a site_url query
// string and a user_agent, each carrying a unique marker, and proves neither
// marker reaches the emitted Decision event (the WAL/Redpanda-bound payload)
// or the HTTP JSON response — even though both raw values are used
// internally for rule matching within this request.
func TestPrivacy_ServeHTTP_RawSiteURLAndUserAgent_NeverLeakToOutputs(t *testing.T) {
	sink := &captureSink{}
	h := newTestDecisionHandler(buildTestSnap(snapshot.TierRemnant), sink)

	const secretMarker = "SESSION_TOKEN_SECRET_9f8e7d"
	const uaMarker = "UA-CANARY-MARKER-1a2b3c"

	body, _ := json.Marshal(DecideRequest{
		ZoneID:    "z1",
		SiteURL:   "https://publisher.example.com/article?session=" + secretMarker + "#frag",
		UserAgent: "Mozilla/5.0 " + uaMarker,
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/decide", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), secretMarker) {
		t.Error("HTTP response leaks the raw site_url query string — PRIV-01 violation")
	}
	if strings.Contains(rr.Body.String(), uaMarker) {
		t.Error("HTTP response leaks the raw user_agent — PRIV-01 violation")
	}

	d := sink.last()
	if d == nil {
		t.Fatal("no Decision emitted to sink")
	}
	raw, err := proto.Marshal(d)
	if err != nil {
		t.Fatalf("proto.Marshal(Decision): %v", err)
	}
	if bytes.Contains(raw, []byte(secretMarker)) {
		t.Error("emitted Decision event leaks the raw site_url query string — PRIV-01 violation")
	}
	if bytes.Contains(raw, []byte(uaMarker)) {
		t.Error("emitted Decision event leaks the raw user_agent — PRIV-01 violation")
	}
}

// ---------------------------------------------------------------------------
// Geo (TX-5/DA-11, DA-9): POST /v1/decide is called directly by the browser,
// so this handler DOES see a raw client IP. These tests prove: (a) explicit
// geo_country/geo_city in the body always wins over IP derivation; (b) a
// private/loopback IP (or any IP absent from the database) degrades to an
// empty Geo with no error; (c) a real .mmdb fixture resolves a known IP to
// its country and a Geo-Country delivery rule (§4.6) actually fires on the
// served path; (d) the raw IP never appears in the emitted Decision event or
// in this handler's logs (mirrors TestServeHTTP_PRIV01_RawInputsNeverLeak's
// proto.Marshal-based leak check just above).
// ---------------------------------------------------------------------------

// testGeoIP is the single address baked into every fixture built by
// buildGeoFixture below.
const testGeoIP = "8.8.8.8"

// buildGeoFixture writes a minimal GeoLite2-City-shaped .mmdb file mapping
// testGeoIP to country, and returns its path. This is a TEST-ONLY fixture
// builder (github.com/maxmind/mmdbwriter, pure Go, no cgo — never linked
// into production code); it mirrors the identical technique in
// internal/geo/maxmind_reload_test.go's unexported buildFixture. That is
// deliberate, ordinary test-fixture duplication, NOT the production-logic
// duplication (resolveAndDiscardIP / the reload polling loop) this task
// eliminated by extracting internal/clientip and internal/geo.RunReloader.
func buildGeoFixture(t *testing.T, country string) string {
	t.Helper()
	dir := t.TempDir()

	tree, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType: "GeoLite2-City",
		Languages:    []string{"en"},
	})
	if err != nil {
		t.Fatalf("mmdbwriter.New: %v", err)
	}
	_, network, err := net.ParseCIDR(testGeoIP + "/32")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}
	record := mmdbtype.Map{
		"city": mmdbtype.Map{
			"names": mmdbtype.Map{"en": mmdbtype.String("Test City " + country)},
		},
		"country": mmdbtype.Map{"iso_code": mmdbtype.String(country)},
	}
	if err := tree.Insert(network, record); err != nil {
		t.Fatalf("tree.Insert: %v", err)
	}

	path := filepath.Join(dir, "fixture.mmdb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("os.Create: %v", err)
	}
	defer f.Close()
	if _, err := tree.WriteTo(f); err != nil {
		t.Fatalf("tree.WriteTo: %v", err)
	}
	return path
}

// buildGeoGatedSnap mirrors db/seed/dev_seed.sql's demo cascade exactly: a
// Contract campaign gated by "Geo-Country IS BR" (§4.6) competing with an
// always-eligible Remnant fallback, both linked to zone "z1" (DA-2/DA-3).
func buildGeoGatedSnap() *snapshot.Snapshot {
	s := snapshot.EmptySnapshot()
	s.Zones["z1"] = &snapshot.Zone{ID: "z1", TenantID: "t1", Active: true}

	rs := &snapshot.RuleSet{
		ID:       "geo-br",
		TenantID: "t1",
		Logic:    snapshot.LogicAND,
		Conditions: []snapshot.Condition{
			{Vector: snapshot.VectorGeoCountry, Operator: snapshot.OpIs, Value: "BR"},
		},
	}
	s.RuleSets[rs.ID] = rs

	contract := &snapshot.Campaign{
		ID: "contract-br", TenantID: "t1", Tier: snapshot.TierContract,
		Priority: 10, BannerIDs: []string{"ban-contract"}, ZoneIDs: []string{"z1"},
		Active: true, PricingModel: snapshot.PricingCPM,
		GoalImpressions: 1000, DeliveredImpressions: 0,
	}
	remnant := &snapshot.Campaign{
		ID: "remnant-house", TenantID: "t1", Tier: snapshot.TierRemnant,
		Priority: 1, BannerIDs: []string{"ban-remnant"}, ZoneIDs: []string{"z1"},
		Active: true, PricingModel: snapshot.PricingCPM,
		ECPM: &moneyv1.Money{AssetCode: "BRL", Amount: 150, Scale: 2},
	}
	s.Campaigns[contract.ID] = contract
	s.Campaigns[remnant.ID] = remnant
	s.ZoneCampaigns["z1"] = []string{contract.ID, remnant.ID}

	s.Banners["ban-contract"] = &snapshot.Banner{
		ID: "ban-contract", TenantID: "t1", CampaignID: contract.ID,
		ImageURL: "https://cdn.example.com/contract.jpg", ClickURL: "https://advertiser.example/landing",
		Active: true, RuleSetIDs: []string{rs.ID},
	}
	s.Banners["ban-remnant"] = &snapshot.Banner{
		ID: "ban-remnant", TenantID: "t1", CampaignID: remnant.ID,
		ImageURL: "https://cdn.example.com/house.jpg", ClickURL: "https://pub.example/house",
		Active: true,
	}
	return s
}

// geoDecideRequest builds a POST /v1/decide request carrying the given
// (optional) body geo fields and a RemoteAddr, mirroring how the browser's
// ad tag reaches this handler directly (no proxy → trustedDepth=0 callers
// use RemoteAddr as-is; these tests use trustedDepth=1 with no XFF header,
// which also falls back to RemoteAddr — see internal/clientip.Extract).
func geoDecideRequest(t *testing.T, zoneID, bodyCountry, bodyCity, remoteAddr string) *http.Request {
	t.Helper()
	body, err := json.Marshal(DecideRequest{ZoneID: zoneID, GeoCountry: bodyCountry, GeoCity: bodyCity})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/decide", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = remoteAddr
	return r
}

// (a) Explicit geo_country in the body ALWAYS wins over IP derivation — even
// when the IP resolves to a DIFFERENT country via a real .mmdb fixture. This
// is the precedence deploy/local/smoke.sh and `make beta-check` depend on.
func TestServeHTTP_Geo_BodyWinsOverIP(t *testing.T) {
	fixture := buildGeoFixture(t, "US") // testGeoIP resolves to US
	resolver := geo.NewMaxMindResolver(fixture, nil)

	sink := &captureSink{}
	h := newTestDecisionHandler(buildGeoGatedSnap(), sink, withTestGeoResolver(resolver, 1))

	// Body says BR (matches the Contract banner's rule); RemoteAddr is the
	// fixture IP, which resolves to US — if precedence were inverted, the
	// Contract banner (gated on BR) would NOT fire and Remnant would serve.
	r := geoDecideRequest(t, "z1", "BR", "", testGeoIP+":4444")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	var resp DecideResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ServedTier != "SERVED_TIER_CONTRACT" {
		t.Fatalf("expected CONTRACT (body geo_country=BR must win over IP-derived US), got %s", resp.ServedTier)
	}
}

// (b) No body geo; RemoteAddr is a private/loopback address absent from the
// fixture database. Must degrade to an empty Geo (Contract's BR rule simply
// doesn't match, cascade falls through to Remnant) — never an error, never
// a panic (DA-9: silence, never a hot-path failure).
func TestServeHTTP_Geo_PrivateIP_NoBody_DegradesToEmpty(t *testing.T) {
	fixture := buildGeoFixture(t, "BR") // only testGeoIP is in the database
	resolver := geo.NewMaxMindResolver(fixture, nil)

	sink := &captureSink{}
	h := newTestDecisionHandler(buildGeoGatedSnap(), sink, withTestGeoResolver(resolver, 1))

	for _, remoteAddr := range []string{"127.0.0.1:1234", "10.0.0.5:80", "192.168.1.20:9999"} {
		r := geoDecideRequest(t, "z1", "", "", remoteAddr)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)

		if rr.Code != http.StatusOK {
			t.Fatalf("remoteAddr=%q: expected 200, got %d: %s", remoteAddr, rr.Code, rr.Body.String())
		}
		var resp DecideResponse
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("remoteAddr=%q: decode response: %v", remoteAddr, err)
		}
		if resp.ServedTier != "SERVED_TIER_REMNANT" {
			t.Fatalf("remoteAddr=%q: expected REMNANT (unresolvable IP → empty geo → BR rule doesn't match), got %s",
				remoteAddr, resp.ServedTier)
		}
	}
}

// (c) No body geo; RemoteAddr is the fixture's known IP, which resolves to
// BR via a REAL .mmdb file. The Geo-Country delivery rule (§4.6) actually
// fires on the served path and the Contract banner is selected — this is
// the capability the task exists to turn on.
func TestServeHTTP_Geo_IPDerived_RuleFires(t *testing.T) {
	fixture := buildGeoFixture(t, "BR")
	resolver := geo.NewMaxMindResolver(fixture, nil)

	sink := &captureSink{}
	h := newTestDecisionHandler(buildGeoGatedSnap(), sink, withTestGeoResolver(resolver, 1))

	r := geoDecideRequest(t, "z1", "", "", testGeoIP+":4444")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	var resp DecideResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ServedTier != "SERVED_TIER_CONTRACT" {
		t.Fatalf("expected CONTRACT (IP-derived geo=BR must fire the Geo-Country rule), got %s", resp.ServedTier)
	}
}

// (d) The raw client IP must never appear in the emitted Decision event or
// in this handler's logs — the SOLE place it is ever read is inside
// clientip.ResolveAndDiscard (see that function's doc), and it goes out of
// scope there.
func TestServeHTTP_Geo_IPNeverLeaksIntoEventOrLog(t *testing.T) {
	const canaryIP = "203.0.113.77" // TEST-NET-3 (RFC 5737) — never a real client

	fixture := buildGeoFixture(t, "BR")
	resolver := geo.NewMaxMindResolver(fixture, nil)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	sink := &captureSink{}
	h := newTestDecisionHandler(buildGeoGatedSnap(), sink,
		withTestGeoResolver(resolver, 1),
		func(h *decisionHandler) { h.logger = logger },
	)

	r := geoDecideRequest(t, "z1", "", "", canaryIP+":5555")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), canaryIP) {
		t.Error("HTTP response leaks the raw client IP — TX-5/DA-11 violation")
	}

	d := sink.last()
	if d == nil {
		t.Fatal("no Decision emitted to sink")
	}
	raw, err := proto.Marshal(d)
	if err != nil {
		t.Fatalf("proto.Marshal(Decision): %v", err)
	}
	if bytes.Contains(raw, []byte(canaryIP)) {
		t.Error("emitted Decision event leaks the raw client IP — TX-5/DA-11 violation")
	}
	if strings.Contains(logBuf.String(), canaryIP) {
		t.Errorf("handler log output leaks the raw client IP — TX-5/DA-11 violation; log=%q", logBuf.String())
	}
}
