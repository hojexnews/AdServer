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
//     c. Sorts candidates by descending score (higher pCTR first).
//
//  3. Hard deadline (TX-4): if the total scoring budget is exceeded, MLRanker
//     returns the original (deterministic cascade) order unchanged — fail-open.
//     The caller (decision handler) sets MlFailOpen=true on the Decision.
//
//  4. Fail-open conditions (all result in returning the unmodified input slice):
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

	// last is the most recent RankResult from the last Rank call.
	// This is a simple field (not channel) because Rank is called serially
	// within a single hot-path request. It is NOT safe for concurrent calls to
	// the same MLRanker instance; each request must use its own instance or
	// the caller must serialize.
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
	r.last = result
	return result.Ordered
}

// LastResult returns the RankResult from the most recent Rank call.
// Must be called on the same goroutine immediately after Rank returns.
func (r *MLRanker) LastResult() RankResult {
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

	// Sort candidates by descending score. sort.SliceStable preserves the
	// deterministic cascade order for equal scores (the J1 dummy model returns
	// all 0s → order is fully preserved, which is the correct J1 behaviour).
	type indexed struct {
		idx   int
		score float32
	}
	order := make([]indexed, len(candidates))
	for i, s := range scores {
		order[i] = indexed{idx: i, score: s}
	}
	sort.SliceStable(order, func(a, b int) bool {
		return order[a].score > order[b].score
	})

	reranked := make([]*cascade.Candidate, len(candidates))
	rerankedScores := make([]float32, len(candidates))
	for newPos, item := range order {
		reranked[newPos] = candidates[item.idx]
		rerankedScores[newPos] = item.score
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
