// request.go — per-request Ranker wrappers (gate E6/E10 fix for HOT-1/HOT-3).
//
// Problem (HOT-1 / HOT-3, see bandit_ranker.go / shadow.go package docs):
//
//	MLRanker, BanditRanker and ShadowRanker each carry per-Rank-call metadata
//	(RankResult / BanditRankerResult / ShadowRecord) or per-request input
//	(BanditConfig / decisionID / zoneID) on a field SHARED by every goroutine
//	that uses the same instance. When a single instance is wired into a
//	cascade.Engine that is shared across concurrent HTTP requests
//	(net/http reuses the decision handler), the decision handler's read of
//	that shared field AFTER cascade.Decide returns is not atomic with
//	respect to a concurrent request's Rank call — request A can observe
//	request B's propensity / model_version / decision_id / zone_id / scores.
//	This silently poisons OPE/IPS/DR and the shadow OPE join.
//
// Fix (Design A — per-request ranker, ADR/plano G0 E6+E10):
//
//	The decision handler constructs a FRESH, small Ranker value for EACH
//	HTTP request and passes it to cascade.Engine.DecideWithRanker (instead
//	of relying on a Ranker wired once into the Engine via WithRanker). The
//	per-request wrapper:
//	  - holds ONLY this request's inputs (BanditConfig, decisionID, zoneID)
//	    and a reference to the shared, IMMUTABLE-during-serving dependencies
//	    (the *MLRanker's ScoreClient/socket/budget/modelVersion, the
//	    ShadowSink) — no allocation of a new socket, no new Engine;
//	  - stores its RankResult/BanditRankerResult/ShadowRecord on ITS OWN
//	    field, never a field shared with other requests;
//	  - is read via Result() from the SAME local variable the handler
//	    passed to DecideWithRanker, in the same goroutine, after
//	    DecideWithRanker returns.
//
// Because there is no field shared between concurrent requests, there is
// nothing for a concurrent Rank call to race on or overwrite — request A's
// RankResult physically cannot become visible to request B (and vice versa).
//
// Cost (TX-4): allocating one of these wrapper structs per request is a
// single small heap allocation (comparable to allocating the Result/Decision
// structs the handler already allocates per request) — it does NOT open a
// new socket, spawn a goroutine, or make any network call. The shared
// *MLRanker.rankInternal (scoring) and ExploreRank (exploration) logic is
// unchanged; only the RESULT STORAGE moved from a shared field to a
// per-request one.
package ranker

import (
	"log/slog"
	"math/rand/v2"

	"github.com/hojex/adserver/internal/cascade"
)

// ---------------------------------------------------------------------------
// RequestRanker — per-request wrapper for the legacy MLRanker path (Mode A).
// ---------------------------------------------------------------------------

// RequestRanker wraps a shared *MLRanker for use in a SINGLE cascade
// Decide/DecideWithRanker call. Construct one per HTTP request:
//
//	rr := ranker.NewRequestRanker(h.mlRanker)
//	result := h.cascade.DecideWithRanker(cascadeReq, snap, rr)
//	rankResult := rr.Result() // per-request, race-free
//
// RequestRanker does NOT touch MLRanker's shared `last` field (it calls the
// unexported rankInternal directly), so concurrent requests using the same
// underlying *MLRanker never observe each other's RankResult.
type RequestRanker struct {
	ml     *MLRanker
	result RankResult
}

// NewRequestRanker creates a RequestRanker wrapping the shared *MLRanker ml.
// ml must not be nil. Cheap: no socket, no goroutine — one small struct.
func NewRequestRanker(ml *MLRanker) *RequestRanker {
	return &RequestRanker{ml: ml}
}

// Rank implements cascade.Ranker. Safe to call at most once per RequestRanker
// (one per HTTP request / one per cascade stratum attempt within that
// request); the result of the LAST call (i.e. the winning stratum's, since
// the cascade stops at the first winning stratum) is what Result() returns.
func (rr *RequestRanker) Rank(candidates []*cascade.Candidate) []*cascade.Candidate {
	rr.result = rr.ml.rankInternal(candidates)
	return rr.result.Ordered
}

// Result returns the RankResult from the most recent Rank call on THIS
// RequestRanker instance only. Race-free by construction: this field is
// never written by any other request's goroutine.
func (rr *RequestRanker) Result() RankResult {
	return rr.result
}

// ---------------------------------------------------------------------------
// RequestBanditRanker — per-request wrapper for the J4 treatment arm (Mode B).
// ---------------------------------------------------------------------------

// RequestBanditRanker wraps a shared *MLRanker plus a per-request
// BanditConfig for use in a SINGLE cascade Decide/DecideWithRanker call.
// Construct one per HTTP request:
//
//	rb := ranker.NewRequestBanditRanker(h.banditML, abAssignment.BanditCfg)
//	result := h.cascade.DecideWithRanker(cascadeReq, snap, rb)
//	banditResult := rb.Result() // per-request, race-free
//
// The BanditConfig (including epsilon, which may vary per A/B assignment) is
// baked in at construction time instead of being pushed onto a shared
// WithConfig field — eliminating the HOT-1 mis-attribution window where a
// concurrent request's WithConfig call could change the epsilon/policy used
// by another in-flight request.
type RequestBanditRanker struct {
	ml     *MLRanker
	cfg    BanditConfig
	rng    *rand.Rand // nil is safe: ExploreRank creates a fresh source per call.
	result BanditRankerResult
}

// NewRequestBanditRanker creates a RequestBanditRanker wrapping the shared
// *MLRanker ml with the given per-request BanditConfig. ml must not be nil.
// rng defaults to nil (a fresh rand source is created per call inside
// ExploreRank — safe under concurrency because it is never shared).
func NewRequestBanditRanker(ml *MLRanker, cfg BanditConfig) *RequestBanditRanker {
	return &RequestBanditRanker{ml: ml, cfg: cfg}
}

// Rank implements cascade.Ranker. See RequestRanker.Rank for the
// per-stratum-attempt semantics (last call wins == winning stratum).
func (rb *RequestBanditRanker) Rank(candidates []*cascade.Candidate) []*cascade.Candidate {
	ordered, res := rankAndExplore(rb.ml, rb.cfg, rb.rng, candidates)
	rb.result = res
	return ordered
}

// Result returns the BanditRankerResult from the most recent Rank call on
// THIS RequestBanditRanker instance only. Race-free by construction.
func (rb *RequestBanditRanker) Result() BanditRankerResult {
	return rb.result
}

// ---------------------------------------------------------------------------
// RequestShadowRanker — per-request wrapper for SHADOW_ENABLED (J3).
// ---------------------------------------------------------------------------

// RequestShadowRanker wraps a shared *MLRanker plus a per-request
// decisionID/zoneID for use in a SINGLE cascade Decide/DecideWithRanker call.
// Construct one per HTTP request:
//
//	sr := ranker.NewRequestShadowRanker(h.shadowML, h.shadowSink, h.shadowModelVersion, decisionID, zoneID, h.logger)
//	result := h.cascade.DecideWithRanker(cascadeReq, snap, sr)
//	rec := sr.Result() // per-request, race-free (also already sent to sink)
//
// decisionID/zoneID are baked in at construction time instead of being
// pushed via SetRequestContext onto a shared field — eliminating the HOT-3
// mis-attribution window where a concurrent request's SetRequestContext call
// could overwrite the decisionID/zoneID another in-flight request's Rank
// call is about to read.
//
// Serving invariant (DA-3, unchanged): Rank ALWAYS returns the ORIGINAL
// unmodified candidate slice — the shadow ranking never influences what is
// served.
type RequestShadowRanker struct {
	ml           *MLRanker
	sink         ShadowSink
	modelVersion string
	decisionID   string
	zoneID       string
	logger       *slog.Logger

	result ShadowRecord
}

// NewRequestShadowRanker creates a RequestShadowRanker.
//
//   - ml: shared inner ML ranker used to compute the shadow order.
//   - sink: async, non-blocking sink (fire-and-forget); may be DiscardSink{}.
//   - modelVersion: the model version to record in ShadowRecord.
//   - decisionID, zoneID: THIS request's identifiers (PII-free: ULID pseudonym
//     and placement id — TX-5/DA-11).
//   - logger: may be nil.
func NewRequestShadowRanker(
	ml *MLRanker,
	sink ShadowSink,
	modelVersion string,
	decisionID, zoneID string,
	logger *slog.Logger,
) *RequestShadowRanker {
	return &RequestShadowRanker{
		ml:           ml,
		sink:         sink,
		modelVersion: modelVersion,
		decisionID:   decisionID,
		zoneID:       zoneID,
		logger:       logger,
	}
}

// Rank implements cascade.Ranker. Always returns candidates unmodified
// (DA-3: shadow never alters the served order). See RequestRanker.Rank for
// the per-stratum-attempt semantics (last call wins == winning stratum).
func (sr *RequestShadowRanker) Rank(candidates []*cascade.Candidate) []*cascade.Candidate {
	if len(candidates) == 0 {
		return candidates
	}

	shadowRanked, innerResult := rankShadow(sr.ml, candidates)
	rec := buildShadowRecord(candidates, shadowRanked, innerResult, sr.modelVersion, sr.decisionID, sr.zoneID)
	sr.result = rec

	// Emit to sink non-blocking (fire-and-forget, TX-4).
	sr.sink.EmitShadow(rec)

	if sr.logger != nil && !rec.Agreement {
		sr.logger.Debug("shadow: disagreement between ML and cascade",
			"decision_id", sr.decisionID,
			"cascade_top", rec.ServedCampaignID,
			"shadow_top", rec.ShadowTopCampaignID,
			"model_version", sr.modelVersion,
		)
	}

	return candidates
}

// Result returns the ShadowRecord from the most recent Rank call on THIS
// RequestShadowRanker instance only. Race-free by construction.
func (sr *RequestShadowRanker) Result() ShadowRecord {
	return sr.result
}
