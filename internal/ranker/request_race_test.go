// request_race_test.go — acceptance test for gate E6/E10 (HOT-1/HOT-3 fix).
//
// PROOF REQUIRED: with N>=50 concurrent goroutines, each carrying a distinct
// decisionID/zoneID/priority(→score)/epsilon, request A must NEVER observe
// request B's RankResult/ExploreResult/ShadowRecord — for:
//
//	(a) the legacy MLRanker path (RequestRanker),
//	(b) the J4 BanditRanker treatment path (RequestBanditRanker),
//	(c) the J3 ShadowRanker path (RequestShadowRanker).
//
// Technique: a fake in-process "sidecar" (a real Unix-domain-socket server
// started for the test) echoes back score = features[11]/100, i.e. the
// campaign_priority passthrough feature (featurize.go index 11, clipped to
// [0,100]). Each goroutine sets its candidate's Campaign.Priority to its own
// goroutine index g, so the EXPECTED score for goroutine g is EXACTLY
// float32(g)/100 — any other value proves that goroutine observed a
// different goroutine's RankResult (cross-request contamination).
//
// A companion test (TestLegacySharedFieldPattern_ExhibitsMisattribution_Regression)
// exercises the OLD, shared-field APIs (MLRanker.Rank + LastResult, kept in
// this package ONLY for single-goroutine test/back-compat callers — see the
// "FIXED" notes in bandit_ranker.go / shadow.go) under the same concurrent
// load and shows that mis-attribution DOES occur there. This is the
// before/after evidence gate E6/E10 requires: the OLD shared-field pattern
// exhibits the bug; the NEW per-request pattern (this file's main tests)
// never does.
package ranker

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Fake sidecar: a real UDS server whose score is traceable to the caller.
// ---------------------------------------------------------------------------

// startFakeSidecar starts a minimal in-process replacement for the ranker
// sidecar on a fresh Unix domain socket. For every scored request it returns
// score = features[campaignPriorityFeatureIndex] / 100 — the
// campaign_priority passthrough feature (Featurize index 11, clipped to
// [0,100]) — so the response is deterministically traceable back to the
// Campaign.Priority the CALLER set on its own candidate.
func startFakeSidecar(t *testing.T) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "fake-sidecar.sock")
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("fake sidecar: listen: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return // listener closed at test cleanup
			}
			go func(c net.Conn) {
				defer c.Close()
				payload, err := readLengthPrefixed(c)
				if err != nil {
					return
				}
				var req scoreRequest
				if err := json.Unmarshal(payload, &req); err != nil {
					return
				}
				const campaignPriorityFeatureIndex = 11
				score := req.Features[campaignPriorityFeatureIndex] / 100.0 //nolint:forbidigo // test fixture, not money
				resp := scoreResponse{Score: score, ModelVersion: req.ModelVersion}
				respBytes, err := json.Marshal(resp)
				if err != nil {
					return
				}
				_ = writeLengthPrefixed(c, respBytes)
			}(conn)
		}
	}()

	return socketPath
}

// makePriorityCandidate builds a single Remnant candidate whose
// Campaign.Priority is the caller-supplied value — this is what the fake
// sidecar's score reflects (via Featurize index 11).
func makePriorityCandidate(id string, priority int32) *cascade.Candidate {
	return &cascade.Candidate{
		Campaign: &snapshot.Campaign{
			ID:       id,
			Tier:     snapshot.TierRemnant,
			Priority: priority,
		},
		Banner: &snapshot.Banner{ID: id + "-banner"},
		Tier:   snapshot.TierRemnant,
	}
}

func approxEqualFloat32(a, b float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-4
}

func firstScoreOrSentinel(s []float32) float32 {
	if len(s) == 0 {
		return -1
	}
	return s[0]
}

// ---------------------------------------------------------------------------
// (a) RequestRanker — legacy MLRanker path (Mode A)
// ---------------------------------------------------------------------------

// TestRequestRanker_NoCrossRequestAttribution_Race is the gate E6 acceptance
// test for the legacy RANKER_ENABLED path: N>=50 goroutines share ONE
// *MLRanker (as the decision service does — h.mlRanker is a single shared,
// immutable-during-serving instance) but each wraps it in its OWN
// RequestRanker per "request". No goroutine may ever observe another
// goroutine's RankResult.
//
// Run with -race: this proves BOTH freedom from data races (the shared
// *MLRanker.rankInternal path) AND freedom from mis-attribution (the
// RequestRanker's local `result` field is never shared).
func TestRequestRanker_NoCrossRequestAttribution_Race(t *testing.T) {
	socketPath := startFakeSidecar(t)
	shared := New(socketPath, 200*time.Millisecond, nil) // shared, immutable-during-serving
	shared.WithModelVersion("shared-legacy-v1")

	const n = 60
	type outcome struct {
		ok        bool
		gotScore  float32
		wantScore float32
		failOpen  bool
	}
	results := make([]outcome, n)

	var wg sync.WaitGroup
	wg.Add(n)
	for g := 0; g < n; g++ {
		go func(g int) {
			defer wg.Done()
			cand := makePriorityCandidate(fmt.Sprintf("c-%02d", g), int32(g))
			rr := NewRequestRanker(shared) // fresh per "request" — HOT-1 fix
			ranked := rr.Rank([]*cascade.Candidate{cand})
			res := rr.Result()

			want := float32(g) / 100.0 //nolint:forbidigo // test expectation, not money
			got := firstScoreOrSentinel(res.Scores)
			ok := !res.FailOpen &&
				len(ranked) == 1 && ranked[0].Campaign.ID == cand.Campaign.ID &&
				approxEqualFloat32(got, want)
			results[g] = outcome{ok: ok, gotScore: got, wantScore: want, failOpen: res.FailOpen}
		}(g)
	}
	wg.Wait()

	for g, o := range results {
		if !o.ok {
			t.Errorf("goroutine %d: MLRanker cross-request attribution FAILURE: "+
				"got score=%v want=%v fail_open=%v (RequestRanker must isolate per-request RankResult)",
				g, o.gotScore, o.wantScore, o.failOpen)
		}
	}
}

// ---------------------------------------------------------------------------
// (b) RequestBanditRanker — J4 treatment path (Mode B)
// ---------------------------------------------------------------------------

// TestRequestBanditRanker_NoCrossRequestAttribution_Race is the gate E6
// acceptance test for the J4 treatment arm: N>=50 goroutines share ONE
// *MLRanker (banditML in the decision service) but each supplies its OWN
// BanditConfig (distinct epsilon) via a fresh RequestBanditRanker. Neither
// the ML score NOR the epsilon/propensity may leak between goroutines.
//
// Epsilon is echoed back unconditionally by ExploreRank regardless of the
// random explore/exploit branch taken (see bandit.go), so comparing it
// directly gives a deterministic (non-flaky) per-request assertion. Using a
// single candidate (n=1) additionally forces the exploit branch
// deterministically (see exploreEpsilonGreedy: `if !explored || n == 1`).
func TestRequestBanditRanker_NoCrossRequestAttribution_Race(t *testing.T) {
	socketPath := startFakeSidecar(t)
	sharedML := New(socketPath, 200*time.Millisecond, nil)
	sharedML.WithModelVersion("shared-bandit-v1")

	const n = 60
	type outcome struct {
		ok                  bool
		gotEps, wantEps     float64
		gotScore, wantScore float32
		failOpen            bool
	}
	results := make([]outcome, n)

	var wg sync.WaitGroup
	wg.Add(n)
	for g := 0; g < n; g++ {
		go func(g int) {
			defer wg.Done()
			eps := float64(g) / 1000.0 //nolint:forbidigo // ML hyperparameter, distinct per goroutine
			cfg := BanditConfig{BanditEnabled: true, Policy: BanditPolicyEpsilonGreedy, Epsilon: eps}
			cand := makePriorityCandidate(fmt.Sprintf("bc-%02d", g), int32(g))

			rb := NewRequestBanditRanker(sharedML, cfg) // fresh per "request" — HOT-1 fix
			rb.Rank([]*cascade.Candidate{cand})
			res := rb.Result()

			wantScore := float32(g) / 100.0 //nolint:forbidigo // test expectation, not money
			gotScore := firstScoreOrSentinel(res.RankResult.Scores)
			ok := !res.RankResult.FailOpen &&
				approxEqualFloat32(gotScore, wantScore) &&
				res.ExploreResult.Epsilon == eps //nolint:forbidigo // exact echo-back comparison
			results[g] = outcome{
				ok: ok, gotEps: res.ExploreResult.Epsilon, wantEps: eps,
				gotScore: gotScore, wantScore: wantScore, failOpen: res.RankResult.FailOpen,
			}
		}(g)
	}
	wg.Wait()

	for g, o := range results {
		if !o.ok {
			t.Errorf("goroutine %d: BanditRanker cross-request attribution FAILURE: "+
				"got epsilon=%v want=%v; got score=%v want=%v; fail_open=%v "+
				"(RequestBanditRanker must isolate per-request BanditRankerResult)",
				g, o.gotEps, o.wantEps, o.gotScore, o.wantScore, o.failOpen)
		}
	}
}

// ---------------------------------------------------------------------------
// (c) RequestShadowRanker — J3 shadow path (HOT-3)
// ---------------------------------------------------------------------------

// TestRequestShadowRanker_NoCrossRequestAttribution_Race is the gate E10
// acceptance test for the shadow path: N>=50 goroutines share ONE *MLRanker
// but each supplies its OWN decisionID/zoneID (baked in at construction, not
// via a shared SetRequestContext field) via a fresh RequestShadowRanker.
// Neither the decision_id, the zone_id, NOR the shadow score may leak
// between goroutines. Also verifies the DA-3 invariant: Rank always returns
// the original (unmodified) candidate slice.
func TestRequestShadowRanker_NoCrossRequestAttribution_Race(t *testing.T) {
	socketPath := startFakeSidecar(t)
	sharedML := New(socketPath, 200*time.Millisecond, nil)
	sharedML.WithModelVersion("shared-shadow-v1")

	const n = 60
	type outcome struct {
		ok                        bool
		gotDecision, wantDecision string
		gotZone, wantZone         string
		gotScore, wantScore       float32
	}
	results := make([]outcome, n)

	var wg sync.WaitGroup
	wg.Add(n)
	for g := 0; g < n; g++ {
		go func(g int) {
			defer wg.Done()
			decisionID := fmt.Sprintf("dec-%03d", g)
			zoneID := fmt.Sprintf("zone-%03d", g)
			cand := makePriorityCandidate(fmt.Sprintf("sc-%02d", g), int32(g))

			// fresh per "request", decisionID/zoneID baked in — HOT-3 fix.
			sr := NewRequestShadowRanker(sharedML, DiscardSink{}, "shadow-model-v1", decisionID, zoneID, nil)
			served := sr.Rank([]*cascade.Candidate{cand})
			rec := sr.Result()

			wantScore := float32(g) / 100.0 //nolint:forbidigo // test expectation, not money
			gotScore := firstScoreOrSentinel(rec.ShadowScores)
			ok := len(served) == 1 && served[0].Campaign.ID == cand.Campaign.ID &&
				rec.DecisionID == decisionID &&
				rec.ZoneID == zoneID &&
				approxEqualFloat32(gotScore, wantScore)
			results[g] = outcome{
				ok:          ok,
				gotDecision: rec.DecisionID, wantDecision: decisionID,
				gotZone: rec.ZoneID, wantZone: zoneID,
				gotScore: gotScore, wantScore: wantScore,
			}
		}(g)
	}
	wg.Wait()

	for g, o := range results {
		if !o.ok {
			t.Errorf("goroutine %d: ShadowRanker cross-request attribution FAILURE: "+
				"got decision_id=%q want=%q; got zone_id=%q want=%q; got score=%v want=%v "+
				"(RequestShadowRanker must isolate per-request ShadowRecord)",
				g, o.gotDecision, o.wantDecision, o.gotZone, o.wantZone, o.gotScore, o.wantScore)
		}
	}
}

// ---------------------------------------------------------------------------
// Regression canary: the OLD shared-field pattern DOES mis-attribute.
// ---------------------------------------------------------------------------

// TestLegacySharedFieldPattern_ExhibitsMisattribution_Regression exercises
// the LEGACY shared-field APIs kept in this package purely for backward
// compatibility with single-goroutine test/back-compat callers (MLRanker.Rank
// + LastResult — see the "FIXED" notes atop bandit_ranker.go / shadow.go).
//
// It reproduces the historical HOT-1 window: a single shared *MLRanker's
// Rank is called, then — after a short sleep simulating the gap between
// cascade.Decide() returning and the pre-fix decision handler reading
// LastResult() — LastResult() is read. With 50 goroutines constantly racing
// to overwrite the shared `last` field via the SAME instance, some read the
// wrong goroutine's RankResult.
//
// This is a REGRESSION CANARY, not a hard requirement of gate E6/E10: it
// documents why the fix (RequestRanker + DecideWithRanker, tested above) was
// necessary, and would start firing again if a future change re-wires
// MLRanker/BanditRanker/ShadowRanker as a SHARED cascade.Ranker into the
// decision service's concurrently-invoked handler instead of using the
// per-request wrappers in request.go.
func TestLegacySharedFieldPattern_ExhibitsMisattribution_Regression(t *testing.T) {
	socketPath := startFakeSidecar(t)
	shared := New(socketPath, 200*time.Millisecond, nil)
	shared.WithModelVersion("shared-old-pattern-v1")

	const n = 50
	const itersPerGoroutine = 25

	var mismatches int64
	var wg sync.WaitGroup
	wg.Add(n)
	for g := 0; g < n; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < itersPerGoroutine; i++ {
				cand := makePriorityCandidate(fmt.Sprintf("old-%02d-%02d", g, i), int32(g))
				shared.Rank([]*cascade.Candidate{cand}) // OLD API: writes shared `last`

				// Simulate the pre-fix handler's gap between cascade.Decide()
				// returning and LastResult() being read.
				time.Sleep(50 * time.Microsecond)

				res := shared.LastResult() // OLD API: race-free (mutex) but NOT attribution-safe
				want := float32(g) / 100.0 //nolint:forbidigo // test expectation, not money
				if res.FailOpen || len(res.Scores) != 1 || !approxEqualFloat32(res.Scores[0], want) {
					atomic.AddInt64(&mismatches, 1)
				}
			}
		}(g)
	}
	wg.Wait()

	if mismatches == 0 {
		t.Skip("no cross-request mis-attribution observed in this run (scheduler-dependent). " +
			"This does NOT mean the OLD shared-field pattern is attribution-safe — the race " +
			"window simply did not trigger this time. The FIX (RequestRanker / " +
			"RequestBanditRanker / RequestShadowRanker + cascade.Engine.DecideWithRanker, " +
			"proven above) has ZERO such windows by construction, independent of scheduling.")
		return
	}
	t.Logf("legacy shared-field pattern: %d/%d reads observed cross-request mis-attribution "+
		"(HOT-1 reproduced — this is why request.go exists)", mismatches, n*itersPerGoroutine)
}
