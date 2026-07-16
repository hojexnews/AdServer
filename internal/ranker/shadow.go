// shadow.go — ShadowRanker: records the decision a calibrated ML ranker
// WOULD have made, without altering the decision that is actually served.
//
// J3 invariants (ADR-0003 §G, linha J3):
//
//  1. The served order is NEVER changed by the shadow ranker.
//     The production cascade order flows through unmodified; the shadow
//     observation is a side-effect logged asynchronously.
//
//  2. The shadow log is PII-free (TX-5/DA-11):
//     - decision_id (pseudonym, ULID — not user-identifying)
//     - zone_id, campaign_id, banner_id (placement context, not user data)
//     - model_version, shadow_scores, shadow_order — ML signals, not PII
//     No raw UA, no IP, no user_id.
//
//  3. The shadow emit is FIRE-AND-FORGET (non-blocking).
//     A channel with a fixed-capacity buffer holds pending records; full
//     buffer drops the record (never blocks the hot path for a shadow log).
//
//  4. SHADOW_ENABLED=false (default) means this code is a no-op.
//     The serving path is completely unaffected when shadow is OFF.
//     Golden tests of Fase 1 are green because shadow==OFF produces
//     identical output to the pure cascade.
//
//  5. Bandit integration (J4): epsilon-greedy and Thompson Sampling code
//     is present in this package (bandit.go) but is NOT active in J3.
//     It is guarded by BanditEnabled and defaults to false.  The bandit
//     hook is also called from ShadowRanker.Rank so that J4 can activate
//     it here without touching the production cascade path.
package ranker

import (
	"log/slog"
	"sync"

	"github.com/hojex/adserver/internal/cascade"
)

// ---------------------------------------------------------------------------
// ShadowRecord — what the shadow ranker logs per decision
// ---------------------------------------------------------------------------

// ShadowRecord captures what the calibrated ranker WOULD have served,
// alongside the decision actually served by the cascade.
//
// This record is the OPE join target: matched by DecisionID against the
// Decision proto (which carries the logging propensity and served action).
//
// Privacy: no PII fields (TX-5/DA-11). DecisionID is a ULID pseudonym.
type ShadowRecord struct {
	// DecisionID links this shadow record to the Decision proto log entry.
	// Used for OPE join: shadow_order vs. cascade_order vs. reward.
	DecisionID string

	// ZoneID is the placement context (not user-identifying).
	ZoneID string

	// ModelVersion is the version of the model that produced ShadowScores.
	ModelVersion string

	// CascadeOrder is the serving order produced by the production cascade
	// (the list of campaign_ids in the deterministic cascade order).
	// This is what was actually served and billed.
	CascadeOrder []string

	// ShadowOrder is the order the ML ranker WOULD have produced.
	// Compared offline against CascadeOrder + reward to compute OPE.
	ShadowOrder []string

	// ShadowScores is the pCTR score the ML ranker gave each candidate,
	// in the SAME ORDER as ShadowOrder (index i → ShadowOrder[i]).
	// These are ML probabilities, NOT money (TX-2 does not apply here).
	ShadowScores []float32 //nolint:forbidigo // ML probability, not money

	// ShadowTopCampaignID is ShadowOrder[0] — the candidate the ML ranker
	// would have served first. Quick comparison field for OPE.
	ShadowTopCampaignID string

	// ServedCampaignID is the campaign that was actually served (from the cascade).
	ServedCampaignID string

	// Agreement is true when ShadowTopCampaignID == ServedCampaignID.
	// When true, both policies agreed on the top choice.
	Agreement bool
}

// ---------------------------------------------------------------------------
// ShadowSink — async sink for shadow records
// ---------------------------------------------------------------------------

// ShadowSink receives shadow records from the hot path.
// Implementations are fire-and-forget: the hot path MUST NOT block waiting
// for the sink to accept a record.
type ShadowSink interface {
	// EmitShadow enqueues a shadow record for async processing.
	// MUST NOT block. If the sink is full, the record is silently dropped.
	EmitShadow(rec ShadowRecord)
}

// ChannelSink is a ShadowSink backed by a buffered channel.
// Records are consumed by a background goroutine (see NewChannelSink).
// When the channel is full, new records are dropped (hot-path safety).
type ChannelSink struct {
	ch     chan ShadowRecord
	logger *slog.Logger
}

// NewChannelSink creates a ChannelSink with the given buffer capacity.
// The caller must start a consumer goroutine (e.g., drain to a log file
// or Redpanda topic) to prevent the channel from filling up.
//
// Typical buffer: 4096 records (one per busy second of traffic).
// When full, records are dropped with a debug log.
func NewChannelSink(bufSize int, logger *slog.Logger) *ChannelSink {
	if bufSize <= 0 {
		bufSize = 4096
	}
	return &ChannelSink{
		ch:     make(chan ShadowRecord, bufSize),
		logger: logger,
	}
}

// EmitShadow enqueues a shadow record non-blocking.
func (s *ChannelSink) EmitShadow(rec ShadowRecord) {
	select {
	case s.ch <- rec:
		// enqueued successfully
	default:
		// channel full — drop the record (hot-path safety invariant)
		if s.logger != nil {
			s.logger.Debug("shadow: channel full, record dropped",
				"decision_id", rec.DecisionID)
		}
	}
}

// Chan returns the receive end of the channel for the consumer goroutine.
func (s *ChannelSink) Chan() <-chan ShadowRecord {
	return s.ch
}

// DiscardSink is a ShadowSink that silently discards all records.
// Used in tests and when SHADOW_ENABLED=false (no allocation overhead).
type DiscardSink struct{}

func (DiscardSink) EmitShadow(_ ShadowRecord) {}

// ---------------------------------------------------------------------------
// ShadowRanker
// ---------------------------------------------------------------------------

// ShadowRanker wraps an inner Ranker (the ML ranker) and:
//  1. Runs the inner ranker to compute the shadow order.
//  2. Records the shadow result to the ShadowSink (non-blocking).
//  3. Returns the ORIGINAL unmodified cascade order to the caller.
//
// The served order is NEVER altered (J3 invariant §1).
//
// Usage:
//
//	sr := NewShadowRanker(mlRanker, sink, "model-v1", logger)
//	cascadeOpts = append(cascadeOpts, cascade.WithRanker(sr))
//
// When SHADOW_ENABLED=false, use the MLRanker directly (not wrapped in
// ShadowRanker) OR use DiscardSink — same effect.
//
// State: shadowDecisionID and shadowZoneID must be set per-request via
// SetRequestContext BEFORE calling Rank.
//
// FIXED (gate E6/E10, was HOT-3): a single shared ShadowRanker is NOT
// attribution-safe across concurrent HTTP requests — SetRequestContext writes
// shadowDecisionID/shadowZoneID on a field shared with every other in-flight
// request, so a concurrent request B can overwrite A's context between A's
// SetRequestContext call and the Rank call inside A's cascade.Decide,
// poisoning A's ShadowRecord with B's decision_id/zone_id.
//
// The decision service hot path no longer wires a shared ShadowRanker into
// the cascade engine. Instead, for each HTTP request it constructs a fresh
// ranker.RequestShadowRanker (see request.go) carrying that request's
// decisionID/zoneID baked in at construction time, and calls
// cascade.Engine.DecideWithRanker(req, snap, requestShadowRanker). The
// ShadowRecord is read from that local variable — never a shared field.
// rankShadow/buildShadowRecord (below) contain the actual scoring/record
// logic and touch no shared state; both ShadowRanker.Rank (kept for existing
// test callers that wire it via cascade.WithRanker on a single goroutine)
// and RequestShadowRanker.Rank delegate to them.
type ShadowRanker struct {
	inner        *MLRanker
	sink         ShadowSink
	modelVersion string
	logger       *slog.Logger

	mu               sync.Mutex
	shadowDecisionID string
	shadowZoneID     string

	// last shadow result, accessible via LastShadowRecord after Rank.
	lastShadow ShadowRecord
}

// NewShadowRanker creates a ShadowRanker.
//
//   - inner: the calibrated ML ranker that produces shadow scores.
//   - sink: async sink for shadow records (DiscardSink if not needed).
//   - modelVersion: model version string logged in ShadowRecord.ModelVersion.
//   - logger: may be nil.
func NewShadowRanker(
	inner *MLRanker,
	sink ShadowSink,
	modelVersion string,
	logger *slog.Logger,
) *ShadowRanker {
	return &ShadowRanker{
		inner:        inner,
		sink:         sink,
		modelVersion: modelVersion,
		logger:       logger,
	}
}

// SetRequestContext must be called by the decision handler before each
// cascade.Decide call when shadow is enabled. It sets the per-request
// metadata that will be included in the ShadowRecord.
//
// Context fields:
//   - decisionID: the ULID of the current decision (for OPE join).
//   - zoneID: the zone being served (placement context).
//
// Test-only / single-goroutine usage: see the FIXED note on the ShadowRanker
// type doc above. The decision service hot path uses
// ranker.RequestShadowRanker instead (per-request, no shared context field).
func (sr *ShadowRanker) SetRequestContext(decisionID, zoneID string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.shadowDecisionID = decisionID
	sr.shadowZoneID = zoneID
}

// Rank implements cascade.Ranker.
//
// It runs the inner ML ranker to get the shadow order, records it to the
// sink, and returns the ORIGINAL unmodified slice to the cascade.
//
// Serving invariant: the cascade always receives the original slice back.
// The shadow ordering is a side-effect only — never influences what is served.
func (sr *ShadowRanker) Rank(candidates []*cascade.Candidate) []*cascade.Candidate {
	if len(candidates) == 0 {
		return candidates
	}

	// Get current request context (set by decision handler).
	sr.mu.Lock()
	decisionID := sr.shadowDecisionID
	zoneID := sr.shadowZoneID
	sr.mu.Unlock()

	shadowRanked, innerResult := rankShadow(sr.inner, candidates)
	rec := buildShadowRecord(candidates, shadowRanked, innerResult, sr.modelVersion, decisionID, zoneID)

	sr.mu.Lock()
	sr.lastShadow = rec
	sr.mu.Unlock()

	// Emit to sink non-blocking (fire-and-forget).
	sr.sink.EmitShadow(rec)

	if sr.logger != nil && !rec.Agreement {
		sr.logger.Debug("shadow: disagreement between ML and cascade",
			"decision_id", decisionID,
			"cascade_top", rec.ServedCampaignID,
			"shadow_top", rec.ShadowTopCampaignID,
			"model_version", sr.modelVersion,
		)
	}

	// INVARIANT: return the ORIGINAL cascade order — never shadow order.
	return candidates
}

// LastShadowRecord returns the shadow record from the most recent Rank call.
// Must be called on the same goroutine immediately after the cascade.Decide
// that invoked Rank.
//
// Test-only / single-goroutine usage: see the FIXED note on the ShadowRanker
// type doc above. The decision service hot path uses
// ranker.RequestShadowRanker.Result() instead (per-request, no sharing).
func (sr *ShadowRanker) LastShadowRecord() ShadowRecord {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.lastShadow
}

// ---------------------------------------------------------------------------
// rankShadow / buildShadowRecord — shared scoring + record logic
// (no shared state; used by both ShadowRanker and RequestShadowRanker).
// ---------------------------------------------------------------------------

// rankShadow runs the inner ML ranker on a COPY of candidates (so the inner
// ranker cannot mutate the slice the cascade will keep serving) and returns
// the shadow-ranked order plus the raw RankResult (for scores/model version).
//
// Touches no shared state of its own: ml.rankInternal has no shared state
// (see MLRanker doc), and the copy is allocated fresh on each call.
func rankShadow(ml *MLRanker, candidates []*cascade.Candidate) ([]*cascade.Candidate, RankResult) {
	shadowCandidates := make([]*cascade.Candidate, len(candidates))
	copy(shadowCandidates, candidates)
	innerResult := ml.rankInternal(shadowCandidates)
	return innerResult.Ordered, innerResult
}

// buildShadowRecord assembles a ShadowRecord from the served (cascade) order,
// the shadow-ranked order, and the RankResult. Pure function — no shared
// state, no I/O.
func buildShadowRecord(
	cascadeCandidates []*cascade.Candidate,
	shadowRanked []*cascade.Candidate,
	innerResult RankResult,
	modelVersion, decisionID, zoneID string,
) ShadowRecord {
	cascadeOrder := make([]string, len(cascadeCandidates))
	for i, c := range cascadeCandidates {
		if c.Campaign != nil {
			cascadeOrder[i] = c.Campaign.ID
		}
	}

	shadowOrder := make([]string, len(shadowRanked))
	for i, c := range shadowRanked {
		if c.Campaign != nil {
			shadowOrder[i] = c.Campaign.ID
		}
	}

	servedCampaignID := ""
	if len(cascadeOrder) > 0 {
		servedCampaignID = cascadeOrder[0]
	}

	shadowTopID := ""
	if len(shadowOrder) > 0 {
		shadowTopID = shadowOrder[0]
	}

	return ShadowRecord{
		DecisionID:          decisionID,
		ZoneID:              zoneID,
		ModelVersion:        modelVersion,
		CascadeOrder:        cascadeOrder,
		ShadowOrder:         shadowOrder,
		ShadowScores:        innerResult.Scores,
		ShadowTopCampaignID: shadowTopID,
		ServedCampaignID:    servedCampaignID,
		Agreement:           shadowTopID == servedCampaignID,
	}
}
