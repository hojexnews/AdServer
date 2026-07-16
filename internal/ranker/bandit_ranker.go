// bandit_ranker.go — BanditRanker: wraps MLRanker + ExploreRank into a single
// cascade.Ranker that the J4 treatment path uses.
//
// Architecture (J4, ADR-0003 §G):
//
//	The treatment arm in A/B mode wires a BanditRanker into the cascade engine
//	instead of a bare MLRanker. When cascade.Decide calls Rank:
//	  1. MLRanker scores candidates (pCTR from sidecar).
//	  2. ExploreRank applies the bandit policy (epsilon-greedy or Thompson).
//	  3. The bandit-ordered slice is returned to the cascade.
//	  4. The ExploreResult (propensity, policy, epsilon, explored) is stored
//	     in last and read by the decision handler via LastExploreResult().
//
//	The control arm uses the bare cascade engine (DefaultRanker) — no ML call.
//
//	Fail-open (TX-4):
//	  If the inner MLRanker fail-opens (timeout / IPC error), BanditRanker
//	  returns the original unmodified slice, stores FailOpen=true, and sets
//	  ExploreResult.Propensity=1.0 (deterministic fallback).
//
//	Thread safety / OPE attribution — FIXED (gate E6/E10, was HOT-1):
//	  A single shared BanditRanker instance (this type) is not attribution-safe
//	  under concurrent requests: the mutex prevents DATA races on `last`/`cfg`
//	  but does NOT make them atomic PER REQUEST — a concurrent request's Rank
//	  can overwrite `last` between another request's Decide() returning and
//	  its read of LastResult(), biasing OPE/IPS/DR (§2.3) and the
//	  model-promotion / kill-switch inputs.
//
//	  THE FIX (E6): the decision service hot path no longer wires a shared
//	  BanditRanker into the cascade engine at all. Instead, for each HTTP
//	  request it constructs a fresh ranker.RequestBanditRanker (see
//	  request.go) carrying that request's BanditConfig, and calls
//	  cascade.Engine.DecideWithRanker(req, snap, requestBanditRanker). The
//	  RankResult/ExploreResult are read from that local variable — never a
//	  shared field. rankAndExplore (below) contains the actual scoring +
//	  exploration logic and touches no shared state; both BanditRanker.Rank
//	  (kept for existing test callers that wire it via cascade.WithRanker on
//	  a single goroutine) and RequestBanditRanker.Rank delegate to it.
//
//	  BanditRanker + LastResult remain in this file ONLY for backward
//	  compatibility with tests that exercise a bare cascade.Engine (via
//	  cascade.WithRanker) on a single goroutine; they MUST NOT be wired into
//	  the decision service's shared, concurrently-invoked handler. Same fix
//	  shape applies to SHADOW (shadow.go: RequestShadowRanker, was HOT-3).
//
//	PII-free (TX-5/DA-11): BanditRanker sees only cascade.Candidate (no user
//	  identifiers). Propensity and epsilon are ML signals, not PII.
package ranker

import (
	"math/rand/v2"
	"sync"

	"github.com/hojex/adserver/internal/cascade"
)

// ---------------------------------------------------------------------------
// BanditRankerResult carries the per-Rank-call result from BanditRanker.
// ---------------------------------------------------------------------------

// BanditRankerResult combines the ML ranking result with the bandit exploration
// result. Read by the decision handler via BanditRanker.LastResult().
type BanditRankerResult struct {
	// RankResult is the inner MLRanker result (scores, model version, fail-open).
	RankResult RankResult

	// ExploreResult is the bandit exploration result (propensity, policy, epsilon).
	// When RankResult.FailOpen=true, ExploreResult.Propensity=1.0 (deterministic).
	ExploreResult ExploreResult
}

// ---------------------------------------------------------------------------
// BanditRanker
// ---------------------------------------------------------------------------

// BanditRanker wraps an MLRanker with a BanditConfig and implements
// cascade.Ranker. It applies ML scoring followed by bandit exploration in a
// single Rank call.
//
// Instantiate with NewBanditRanker. The zero value is not usable.
type BanditRanker struct {
	inner *MLRanker
	rng   *rand.Rand // nil → fresh source per call

	mu   sync.Mutex
	cfg  BanditConfig
	last BanditRankerResult
}

// NewBanditRanker creates a BanditRanker.
//
//   - inner: the ML ranker for scoring (must not be nil).
//   - cfg: bandit configuration. BanditEnabled=false → ExploreRank is a no-op
//     (propensity=1.0, order unchanged). Safe for control or AB-disabled paths.
//   - rng: random source for exploration. Pass nil to use a fresh source per call.
func NewBanditRanker(inner *MLRanker, cfg BanditConfig, rng *rand.Rand) *BanditRanker {
	return &BanditRanker{
		inner: inner,
		cfg:   cfg,
		rng:   rng,
	}
}

// WithConfig replaces the bandit configuration under the mutex.
// Used by the decision handler to update epsilon from the A/B config each request.
func (b *BanditRanker) WithConfig(cfg BanditConfig) {
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
}

// Rank implements cascade.Ranker.
//
// Scoring: delegates to inner.rankInternal (MLRanker scoring logic).
// Exploration: applies ExploreRank with the current BanditConfig.
// Fail-open: if scoring fails, returns the original slice (TX-4).
//
// NOTE (HOT-1, gate E6): this method still funnels its result through the
// shared `last` field for backward compatibility with existing callers/tests
// that wire a single BanditRanker into a cascade.Engine via WithRanker and
// read LastResult() afterwards on a SERIAL, single-goroutine test path. It
// MUST NOT be used this way in the decision service hot path anymore — the
// handler now uses a fresh ranker.RequestBanditRanker per HTTP request (see
// request.go), which calls rankAndExplore directly and stores the result on
// a local, non-shared field. See cascade.Engine.DecideWithRanker.
func (b *BanditRanker) Rank(candidates []*cascade.Candidate) []*cascade.Candidate {
	// Step 1: read cfg under lock before releasing it for the (possibly slow)
	// ML scoring call. This prevents a concurrent WithConfig from racing with
	// the cfg read inside ExploreRank.
	b.mu.Lock()
	cfg := b.cfg
	b.mu.Unlock()

	ordered, res := rankAndExplore(b.inner, cfg, b.rng, candidates)

	b.mu.Lock()
	b.last = res
	b.mu.Unlock()

	return ordered
}

// LastResult returns the BanditRankerResult from the most recent Rank call.
// Safe for concurrent callers: the result is copied under the mutex.
//
// KNOWN LIMITATION (HOT-1): this reflects mis-attribution risk under
// concurrent requests sharing one BanditRanker — see the package doc above.
// Test-only / single-goroutine callers only; the decision service hot path
// uses ranker.RequestBanditRanker.Result() instead (per-request, no sharing).
func (b *BanditRanker) LastResult() BanditRankerResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

// ---------------------------------------------------------------------------
// rankAndExplore — shared scoring + exploration logic (no shared state).
// ---------------------------------------------------------------------------

// rankAndExplore performs ML scoring (via ml.rankInternal) followed by bandit
// exploration (via ExploreRank), and returns the served order plus the full
// BanditRankerResult. It touches NO shared mutable state of its own — cfg and
// rng are passed by the caller — so it is safe to call concurrently from
// multiple goroutines as long as ml, cfg and rng are each safe to use
// concurrently (ml.rankInternal has no shared state; cfg is a value type;
// rng may be nil, in which case ExploreRank creates a fresh source per call).
//
// This is the function both BanditRanker.Rank (shared, test-only usage) and
// RequestBanditRanker.Rank (per-request, production hot path) delegate to,
// so the scoring/exploration semantics can never drift between the two.
func rankAndExplore(
	ml *MLRanker,
	cfg BanditConfig,
	rng *rand.Rand,
	candidates []*cascade.Candidate,
) ([]*cascade.Candidate, BanditRankerResult) {
	rankResult := ml.rankInternal(candidates)

	if rankResult.FailOpen {
		// Sidecar unavailable — fail-open: return the original unmodified slice.
		// ExploreResult.Propensity = 1.0 (deterministic fallback).
		res := BanditRankerResult{
			RankResult: rankResult,
			ExploreResult: ExploreResult{
				Ordered:    candidates,
				Propensity: 1.0, //nolint:forbidigo // fail-open deterministic propensity
				Epsilon:    0,
				Explored:   false,
			},
		}
		return candidates, res
	}

	// Bandit exploration over the ML-ranked candidates.
	// ExploreRank is a no-op when cfg.BanditEnabled=false (control / disabled).
	exploreRes := ExploreRank(cfg, rankResult.Ordered, rankResult.Scores, rng)

	res := BanditRankerResult{
		RankResult:    rankResult,
		ExploreResult: exploreRes,
	}
	return exploreRes.Ordered, res
}
