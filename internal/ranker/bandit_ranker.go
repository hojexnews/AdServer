// bandit_ranker.go — BanditRanker: wraps MLRanker + ExploreRank into a single
// cascade.Ranker that the J4 treatment path uses.
//
// Architecture (J4, ADR-0003 §G):
//
//   The treatment arm in A/B mode wires a BanditRanker into the cascade engine
//   instead of a bare MLRanker. When cascade.Decide calls Rank:
//     1. MLRanker scores candidates (pCTR from sidecar).
//     2. ExploreRank applies the bandit policy (epsilon-greedy or Thompson).
//     3. The bandit-ordered slice is returned to the cascade.
//     4. The ExploreResult (propensity, policy, epsilon, explored) is stored
//        in last and read by the decision handler via LastExploreResult().
//
//   The control arm uses the bare cascade engine (DefaultRanker) — no ML call.
//
//   Fail-open (TX-4):
//     If the inner MLRanker fail-opens (timeout / IPC error), BanditRanker
//     returns the original unmodified slice, stores FailOpen=true, and sets
//     ExploreResult.Propensity=1.0 (deterministic fallback).
//
//   Thread safety / OPE attribution — KNOWN LIMITATION, gate: E4/J4 (HOT-1):
//     A SINGLE shared BanditRanker is wired into the cascade engine, and
//     net/http invokes the decision handler CONCURRENTLY (the handler struct IS
//     reused across goroutines — the earlier "not reused" note was wrong). The
//     mutex below makes Rank/LastResult free of DATA races, but does NOT make
//     them atomic PER REQUEST: the handler reads LastResult() AFTER
//     cascade.Decide() returns, so a concurrent request's Rank can overwrite
//     `last` in between — request A then logs request B's propensity /
//     model_version / per-candidate scores, biasing OPE/IPS/DR (§2.3) and the
//     model-promotion / kill-switch inputs. INERT today: AB_ENABLED /
//     RANKER_ENABLED default OFF (not promoted until E4 under real traffic).
//     BEFORE flipping those flags the per-request RankResult MUST flow back FROM
//     Decide() (return it, or a per-request ranker) instead of via shared
//     `last`. Same root cause for SHADOW: shadow.go SetRequestContext (HOT-3).
//     See README "Pendente da Fase 3".
//
//   PII-free (TX-5/DA-11): BanditRanker sees only cascade.Candidate (no user
//     identifiers). Propensity and epsilon are ML signals, not PII.
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
	inner  *MLRanker
	rng    *rand.Rand // nil → fresh source per call

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
// Scoring: delegates to inner.Rank (MLRanker).
// Exploration: applies ExploreRank with the current BanditConfig.
// Fail-open: if inner.Rank fails, returns the original slice (TX-4).
func (b *BanditRanker) Rank(candidates []*cascade.Candidate) []*cascade.Candidate {
	// Step 1: read cfg under lock before releasing it for the (possibly slow)
	// ML scoring call. This prevents a concurrent WithConfig from racing with
	// the cfg read inside ExploreRank.
	b.mu.Lock()
	cfg := b.cfg
	b.mu.Unlock()

	// Step 2: ML scoring (inner.Rank is independently safe via its own mutex).
	ranked := b.inner.Rank(candidates)
	rankResult := b.inner.LastResult()

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
		b.mu.Lock()
		b.last = res
		b.mu.Unlock()
		return candidates
	}

	// Step 3: Bandit exploration over the ML-ranked candidates.
	// ExploreRank is a no-op when cfg.BanditEnabled=false (control / disabled).
	exploreRes := ExploreRank(cfg, ranked, rankResult.Scores, b.rng)

	res := BanditRankerResult{
		RankResult:    rankResult,
		ExploreResult: exploreRes,
	}
	b.mu.Lock()
	b.last = res
	b.mu.Unlock()

	return exploreRes.Ordered
}

// LastResult returns the BanditRankerResult from the most recent Rank call.
// Safe for concurrent callers: the result is copied under the mutex.
func (b *BanditRanker) LastResult() BanditRankerResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}
