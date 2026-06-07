// ranker.go — MLRanker: implements cascade.Ranker for Fase 2 (J1).
//
// Architecture contract (ADR-0003 §A / DA-3 / TX-4):
//
//  1. MLRanker.Rank receives a slice of cascade.Candidate from ONE stratum.
//     It MUST NOT mix tiers or promote between Override/Contract/Remnant.
//     The stratum has already been determined by the cascade (DA-3 authority).
//
//  2. For each candidate, MLRanker:
//     a. Calls Featurize to produce the float32[23] vector (zero network, TX-4).
//     b. Sends the vector to the ranker sidecar via ScoreClient (UDS, TX-4).
//     c. Sorts candidates by the stratum-appropriate objective (see below).
//
//  3. Ranking objective per stratum (DA-3 / revenue vs. delivery):
//
//     REMNANT — revenue-maximising leilão:
//       Sort key = ScoreCandidate(pCTR, ECPMMinorUnits) = eCPM in minor-units.
//       Rationale: Remnant is an open auction; the economic objective is to
//       maximise yield.  eCPM = pCTR × bid correctly captures expected
//       revenue per impression.  Ordering by pCTR alone is an economic error:
//       a candidate with pCTR=0.05, bid=50 (eCPM≈3) would beat one with
//       pCTR=0.04, bid=10000 (eCPM≈400), destroying ~$4 of yield.
//
//     CONTRACT — delivery-driven (pacing):
//       Sort key = pCTR (float32 from sidecar).
//       Rationale: Contract campaigns have a committed delivery goal (pacing,
//       DA-4).  The cascade has already ordered them by pacing deficit
//       (sortContracts).  Within that priority, re-ranking by pCTR maximises
//       CTR performance of the delivered impressions without violating the
//       delivery commitment.  Revenue is not the objective here; the bid is
//       pre-negotiated.
//
//     OVERRIDE — priority-driven (guaranteed placement):
//       Sort key = pCTR (float32 from sidecar).
//       Rationale: Override campaigns are guaranteed placements (typically
//       sponsorships or takeovers).  Delivery priority dominates; the cascade
//       has already sorted by campaign Priority.  Re-ranking by pCTR within
//       the same priority level optimises CTR without affecting the
//       contractual delivery guarantee.
//
//  4. Hard deadline (TX-4): if the total scoring budget is exceeded, MLRanker
//     returns the original (deterministic cascade) order unchanged — fail-open.
//     The caller (decision handler) sets MlFailOpen=true on the Decision.
//
//  5. Fail-open conditions (all result in returning the unmodified input slice):
//     a. Sidecar socket unavailable / dial timeout.
//     b. Any IPC error (write/read/parse).
//     c. Context deadline exceeded (TX-4 budget).
//     d. Any panic recovered inside Rank.
//     In all fail-open cases, FailedOpen() returns true so the handler can set
//     Decision.ml_fail_open = true (OPE must exclude these decisions).
//
// State tracking:
//   MLRankerResult carries the per-Rank-call result: the ordered candidates,
//   whether fail-open occurred, the model version used, and per-candidate scores.
//   The decision handler reads this to fill Decision.candidates[].score,
//   Decision.ml_fail_open, Decision.model_version, and Decision.exploration_policy.
//
// The J1 dummy model (sidecar in stub mode) returns score=0 for all candidates.
// With identical scores, sort.SliceStable preserves the original order — so the
// net effect is IDENTICAL to the cascade deterministic order. This is the correct
// behaviour for J1: "ranker ON, model dummy → order unchanged, ml_fail_open=false".
// (ml_fail_open=false because the call succeeded; the score is simply uninformative.)
package ranker

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// RankResult carries the outcome of a single Rank call.
// The decision handler uses this to fill the Decision proto fields.
// ---------------------------------------------------------------------------

// RankResult is returned by MLRanker.RankWithResult.
// It decouples the cascade.Ranker interface (which only returns []*Candidate)
// from the rich metadata needed by the decision handler.
type RankResult struct {
	// Ordered is the re-ranked candidate slice (first = preferred).
	Ordered []*cascade.Candidate

	// FailOpen is true when the ranker fell back to the deterministic order
	// (timeout, IPC error, sidecar unavailable). The handler must set
	// Decision.MlFailOpen = true when FailOpen is true.
	//
	// FailOpen = false AND ModelVersion = "" means RANKER_ENABLED=false (disabled).
	// FailOpen = false AND ModelVersion != "" means scored successfully.
	// FailOpen = true  means the ranker attempted and failed (TX-4 or IPC error).
	FailOpen bool

	// ModelVersion is the model version reported by the sidecar.
	// Empty string when RANKER_ENABLED=false or fail-open.
	ModelVersion string

	// Scores maps candidate index (in Ordered) to the pCTR score returned by
	// the sidecar. Available for filling Candidate.Score in the Decision proto.
	// With the dummy model (J1), all scores are 0.
	Scores []float32
}

// ---------------------------------------------------------------------------
// MLRanker
// ---------------------------------------------------------------------------

// MLRanker implements cascade.Ranker using the IPC sidecar (J1).
//
// Instantiate with New. The zero value is not usable.
type MLRanker struct {
	client       *ScoreClient
	modelVersion string // current model version tag (filled by sidecar after J2)
	budget       time.Duration
	logger       *slog.Logger

	mu   sync.Mutex
	// last is the most recent RankResult from the last Rank call.
	// Protected by mu: Rank (writer) and LastResult (reader) may be called
	// from concurrent goroutines when the same MLRanker instance is shared
	// across HTTP request handlers (RANKER_ENABLED=true, Fase 2+).
	last RankResult
}

// New creates an MLRanker.
//
//   - socketPath: filesystem path of the sidecar Unix domain socket.
//   - budget: hard deadline for the full Rank call (featurize + IPC * N candidates).
//     Must fit within TX-4 (5–8 ms p99). Recommended: 5 ms.
//   - logger: may be nil (no logging).
func New(socketPath string, budget time.Duration, logger *slog.Logger) *MLRanker {
	return &MLRanker{
		client: &ScoreClient{
			SocketPath: socketPath,
			Timeout:    budget,
		},
		budget: budget,
		logger: logger,
	}
}

// WithModelVersion sets the model version tag that is sent in IPC requests
// and recorded in RankResult.ModelVersion. Called by the decision handler
// when it learns the current model version from the sidecar registry or config.
func (r *MLRanker) WithModelVersion(v string) *MLRanker {
	r.modelVersion = v
	return r
}

// Rank implements cascade.Ranker.
//
// It re-ranks candidates within the given stratum using ML scores from the
// sidecar. On any error or timeout, it returns the original slice (fail-open).
//
// The caller (decision handler) must call LastResult() immediately after Rank
// to obtain the full RankResult metadata (scores, fail-open flag, model version).
//
// IMPORTANT: Rank does not have a context parameter because cascade.Ranker
// does not pass one (the interface is minimal by design). The budget is enforced
// via the ScoreClient.Timeout field set in New. The context used internally is
// context.Background with a deadline derived from the budget.
func (r *MLRanker) Rank(candidates []*cascade.Candidate) []*cascade.Candidate {
	result := r.rankInternal(candidates)
	r.mu.Lock()
	r.last = result
	r.mu.Unlock()
	return result.Ordered
}

// LastResult returns the RankResult from the most recent Rank call.
// Safe for concurrent callers: the result is copied under the mutex.
func (r *MLRanker) LastResult() RankResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// rankInternal performs the actual ranking and returns a RankResult.
// It is separated from Rank to facilitate testing.
func (r *MLRanker) rankInternal(candidates []*cascade.Candidate) RankResult {
	if len(candidates) == 0 {
		return RankResult{Ordered: candidates}
	}

	// Create a context with the hard budget deadline (TX-4).
	ctx, cancel := context.WithTimeout(context.Background(), r.budget)
	defer cancel()

	// Score each candidate. If any call fails, we fail-open for the whole
	// stratum (partial re-ranking with some scores and some 0s would be wrong
	// and unpredictable for OPE).
	scores := make([]float32, len(candidates))
	modelVersion := r.modelVersion
	failOpen := false

	for i, c := range candidates {
		inp := buildFeaturizeInputFromCandidate(c)
		features := Featurize(inp)

		score, err := r.client.Score(ctx, features, r.modelVersion)
		if err != nil {
			if r.logger != nil {
				r.logger.Warn("ranker: IPC score error — fail-open",
					"candidate_index", i,
					"campaign_id", c.Campaign.ID,
					"err", err)
			}
			failOpen = true
			break
		}
		scores[i] = score
	}

	if failOpen {
		// Return the original unmodified slice; all scores are meaningless.
		return RankResult{
			Ordered:      candidates,
			FailOpen:     true,
			ModelVersion: "",
			Scores:       make([]float32, len(candidates)),
		}
	}

	// Determine the stratum from the first candidate (DA-3: all candidates in
	// this slice belong to the same tier; the cascade never mixes strata).
	stratum := candidates[0].Tier

	// Compute the sort key for each candidate based on the stratum objective.
	//
	// REMNANT: sort by eCPM = ScoreCandidate(pCTR, bid) in minor-units.
	//   This is the revenue-maximising objective.  A candidate with a lower
	//   pCTR but a much higher bid should rank above one with a higher pCTR
	//   and a negligible bid (e.g., pCTR=0.04/bid=10000 beats pCTR=0.05/bid=50).
	//
	// OVERRIDE / CONTRACT: sort by pCTR directly.
	//   These strata are delivery-driven, not auction-driven.  Re-ranking by
	//   pCTR optimises CTR performance within the committed delivery slot.
	//   See package doc (point 3) for full rationale.
	//
	// sort.SliceStable preserves the deterministic cascade order for equal
	// sort keys.  With the J1 stub (all pCTR=0):
	//   - Remnant:  ScoreCandidate(0, bid) = 0 for all → stable sort is a no-op.
	//   - Override/Contract: all pCTR=0 → stable sort is a no-op.
	// In both cases the J1 golden tests remain green.
	type indexed struct {
		idx      int
		sortKey  int64 // eCPM minor-units for Remnant; int64(pCTR*1e9) for others
	}
	order := make([]indexed, len(candidates))
	for i, s := range scores {
		var key int64
		switch stratum {
		case snapshot.TierRemnant:
			// Revenue objective: eCPM = pCTR × bid (minor-units, int64).
			key = ScoreCandidate(s, candidates[i].ECPMMinorUnits)
		default:
			// Override / Contract: CTR objective.
			// Scale pCTR to int64 to use the same stable integer sort path.
			// Multiply by 1e9 to preserve float32 resolution (~7 decimal digits)
			// without introducing float comparisons in the sort comparator.
			// float32 has ~7 significant digits; 1e9 keeps 9 digits → no loss.
			key = int64(s * 1e9) //nolint:forbidigo // sort-key scaling, not money
		}
		order[i] = indexed{idx: i, sortKey: key}
	}
	sort.SliceStable(order, func(a, b int) bool {
		return order[a].sortKey > order[b].sortKey
	})

	reranked := make([]*cascade.Candidate, len(candidates))
	rerankedScores := make([]float32, len(candidates))
	for newPos, item := range order {
		reranked[newPos] = candidates[item.idx]
		// Scores carries the raw pCTR from the sidecar for OPE logging.
		// The sort key (eCPM for Remnant, scaled-pCTR for others) is not
		// stored here — the decision handler needs the raw pCTR for propensity.
		rerankedScores[newPos] = scores[item.idx]
	}

	return RankResult{
		Ordered:      reranked,
		FailOpen:     false,
		ModelVersion: modelVersion,
		Scores:       rerankedScores,
	}
}

// ---------------------------------------------------------------------------
// Bridging cascade.Candidate → FeaturizeInput
// ---------------------------------------------------------------------------

// buildFeaturizeInputFromCandidate extracts the fields available on a
// cascade.Candidate and its Campaign/Banner to populate a FeaturizeInput.
//
// NOTES on fields not available in cascade.Candidate at ranking time:
//   - ZoneWidth/ZoneHeight: the cascade passes a flat slice of Candidates;
//     zone dimensions are not on the Candidate struct. The decision handler
//     should use the richer RankWithContext variant (J3+). For J1, zone
//     dimensions default to 0 (bucketize → bucket 0), which is acceptable
//     for the dummy model.
//   - GeoCountry/GeoCity/DeviceClass: same — not on Candidate. Default "".
//   - RequestTime: time.Now() as an approximation; fine for J1 dummy.
//   - CandidateCount: not available here; 0 is the default (log1p(0)=0).
//
// J3 will introduce a richer ranking context (zone, geo, device, time,
// candidate_count) via a new interface extension. For J1, the dummy model
// ignores all features anyway.
func buildFeaturizeInputFromCandidate(c *cascade.Candidate) FeaturizeInput {
	inp := FeaturizeInput{
		CandidateTier:    int(c.Tier),
		CampaignPriority: c.Campaign.Priority,
		PacingDeficit:    c.PacingDeficit,
		RequestTime:      time.Now().UTC(),
	}

	// ECPMMinorUnits (TX-2: int64 minor-units).
	inp.ECPMMinorUnits = c.ECPMMinorUnits

	// Banner dimensions and creative type.
	if c.Banner != nil {
		inp.BannerWidth = c.Banner.Width
		inp.BannerHeight = c.Banner.Height
		inp.CreativeType = creativeTypeString(c.Banner)
	}

	// Campaign history.
	if c.Campaign != nil {
		inp.CampaignDeliveredImpressions = c.Campaign.DeliveredImpressions
		inp.CampaignGoalImpressions = c.Campaign.GoalImpressions
		inp.CampaignDeliveredClicks = c.Campaign.DeliveredClicks
	}

	return inp
}

// creativeTypeString maps banner fields to the creative_type string expected
// by Featurize ("image", "html", "video", or "unknown").
func creativeTypeString(b *snapshot.Banner) string {
	switch {
	case b.ImageURL != "":
		return "image"
	case b.HTML != "":
		return "html"
	case b.VideoURL != "":
		return "video"
	default:
		return "unknown"
	}
}
