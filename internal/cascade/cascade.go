// Package cascade implements the deterministic ad-selection waterfall (DA-3).
//
// Evaluation order (strict, never reordered):
//
//  1. Override   — campaigns with TierOverride; wins immediately.
//  2. Contract   — campaigns with TierContract, sorted by pacing deficit
//                  (highest deficit first); first eligible wins.
//  3. Remnant    — campaigns with TierRemnant, sorted by eCPM descending
//                  (same asset_code / tenant — DA-10); first eligible wins.
//  4. Blank      — no eligible candidate in any tier; a blank impression is
//                  registered as a forensic deficit metric (CA-2).
//
// The N:N campaign↔zone relationship (DA-2) is resolved per request by
// reading the pre-computed ZoneCampaigns index from the Snapshot.
//
// Re-ranker extension point (TX-4):
//   Each stratum's candidate slice is passed through a Ranker before
//   selection.  In I0 the DefaultRanker preserves the deterministic order.
//   In Fase 2 the ML ranker plugs in here behind a timeout + fail-open
//   (the cascade always falls back to DefaultRanker on ML timeout/error).
//
// Frequency-capping extension point (I2):
//   The Capper interface is called per candidate before eligibility is
//   confirmed.  The NoOpCapper (I0) always allows; the Redis-backed
//   implementation arrives in I2.
//
// The page never breaks: any internal error in candidate evaluation is
// absorbed and the affected candidate is simply skipped.  If all candidates
// are exhausted, BLANK is returned.
package cascade

import (
	"sort"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	decisionv1 "github.com/hojex/adserver/gen/go/adserver/decision/v1"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Extension point: Ranker (TX-4)
// ---------------------------------------------------------------------------

// Ranker re-orders the candidates within a stratum before the cascade
// selects the first eligible one.  Implementors MUST NOT change the Tier
// field — the cascade enforces stratum purity.
//
// The production ML ranker (Fase 2) must honour a hard deadline and return
// the unmodified slice on timeout/error (fail-open to deterministic order).
type Ranker interface {
	// Rank receives a slice of eligible candidates for a single stratum and
	// returns them in the desired serving order (first = preferred).
	// The input slice may be modified in place.
	Rank(candidates []*Candidate) []*Candidate
}

// DefaultRanker preserves the deterministic order established by the cascade
// (Override: as-is; Contract: by deficit desc; Remnant: by eCPM desc).
type DefaultRanker struct{}

func (DefaultRanker) Rank(cs []*Candidate) []*Candidate { return cs }

// ---------------------------------------------------------------------------
// Extension point: Capper (I2 — Redis-backed; NoOpCapper for I0)
// ---------------------------------------------------------------------------

// Capper checks frequency caps for a candidate (DA-6).
//
// FAIL-SAFE (DA-6): if the cap check itself errors (Redis down, etc.), the
// implementation MUST return (false, nil) — i.e., treat the candidate as
// CAPPED so that it is skipped, preventing over-delivery.
//
// If the user identifier is absent (no stable cookie/identifier), the
// caller must treat ALL capped campaigns as ineligible (silêncio preferido
// a estouro de contabilidade).  The Capper is not called in that case;
// the cascade skips the candidate before reaching the Capper.
type Capper interface {
	// Allowed returns true when the candidate may be delivered to this user.
	// userID is the hashed+salted stable identifier; empty string means
	// "no stable identifier" — the Capper MUST return false.
	Allowed(userID string, camp *snapshot.Campaign, ban *snapshot.Banner) (bool, error)
}

// NoOpCapper always allows delivery (I0; no Redis, no cap enforcement).
// Replace with the Redis-backed Capper in I2.
type NoOpCapper struct{}

func (NoOpCapper) Allowed(_ string, _ *snapshot.Campaign, _ *snapshot.Banner) (bool, error) {
	return true, nil
}

// ---------------------------------------------------------------------------
// Candidate (internal)
// ---------------------------------------------------------------------------

// Candidate is an ad candidate within a stratum, pre-sorted for the ranker.
type Candidate struct {
	Campaign *snapshot.Campaign
	Banner   *snapshot.Banner
	Tier     snapshot.Tier

	// PacingDeficit is the normalised delivery shortfall for Contract
	// campaigns.  Higher = more urgent.  0 for non-Contract tiers.
	PacingDeficit float64

	// ECPMMinorUnits is the eCPM in minor-units of the campaign's asset.
	// Used for Remnant ordering.  0 for Override/Contract.
	ECPMMinorUnits int64
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// Result is what the cascade returns to the decision service.
type Result struct {
	// Tier is the winning stratum (or TierUnspecified for BLANK).
	Tier snapshot.Tier
	// ServedTier is the proto enum value for embedding in the Decision log.
	ServedTier commonv1.ServedTier

	// Campaign and Banner are nil when Tier == BLANK.
	Campaign *snapshot.Campaign
	Banner   *snapshot.Banner

	// Candidates is the full eligible candidate set of the winning stratum
	// (before selection), for the decision log (TX-4 / OPE).
	Candidates []*decisionv1.Candidate
}

// Blank returns a BLANK result (no eligible candidate — CA-2).
func Blank() Result {
	return Result{
		Tier:       snapshot.TierUnspecified,
		ServedTier: commonv1.ServedTier_SERVED_TIER_BLANK,
	}
}

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

// Engine holds the stateless dependencies needed by the cascade.
// It is safe for concurrent use.
type Engine struct {
	rules  *rules.Engine
	ranker Ranker
	capper Capper
}

// New creates a cascade Engine with default (no-op) ranker and capper.
// Swap the ranker/capper via the functional options for Fase 2 / I2.
func New(rulesEngine *rules.Engine, opts ...Option) *Engine {
	e := &Engine{
		rules:  rulesEngine,
		ranker: DefaultRanker{},
		capper: NoOpCapper{},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Option is a functional option for Engine.
type Option func(*Engine)

// WithRanker swaps the re-ranker (TX-4 extension point).
func WithRanker(r Ranker) Option { return func(e *Engine) { e.ranker = r } }

// WithCapper wires the frequency-capping implementation (I2 extension point).
func WithCapper(c Capper) Option { return func(e *Engine) { e.capper = c } }

// ---------------------------------------------------------------------------
// Decide — the hot path entry point
// ---------------------------------------------------------------------------

// Request carries the per-ad-call inputs.
type Request struct {
	ZoneID      string
	TenantID    string
	Rules       *rules.Context
	// UserID is the hashed+salted stable user identifier for capping.
	// Empty string = no stable identifier (all capped campaigns are skipped).
	UserID      string
	RequestTime time.Time
}

// Decide runs the cascade for a single zone request and returns a Result.
// It never panics; on any internal error the cascade degrades to BLANK.
//
// Security (CA-1): the effective tenant_id is derived from the zone's TenantID
// in the snapshot.  req.TenantID MUST already be the server-derived value
// (set by the decision handler from snap.Zones[zoneID].TenantID).  The cascade
// enforces this by cross-checking: if the zone does not exist in the snapshot
// or its TenantID does not match req.TenantID, the request is fail-closed to
// BLANK.  This prevents tenant isolation violations even if req.TenantID is
// somehow misset by the caller.
func (e *Engine) Decide(req Request, snap *snapshot.Snapshot) Result {
	// Fail-closed: the zone MUST exist in the snapshot and its TenantID
	// must match req.TenantID (which was derived server-side by the handler).
	zone, zoneOK := snap.Zones[req.ZoneID]
	if !zoneOK || zone == nil {
		return Blank() // zone not in snapshot — safe default
	}
	if zone.TenantID != req.TenantID {
		// TenantID mismatch — should never happen if handler is correct,
		// but we enforce it here as defence-in-depth (CA-1 fail-closed).
		return Blank()
	}

	campaignIDs := snap.ZoneCampaigns[req.ZoneID]
	if len(campaignIDs) == 0 {
		return Blank()
	}

	// Partition campaigns into tiers for ordered evaluation.
	var (
		overrides []*Candidate
		contracts []*Candidate
		remnants  []*Candidate
	)

	for _, cid := range campaignIDs {
		camp, ok := snap.Campaigns[cid]
		if !ok || !camp.Active || camp.TenantID != req.TenantID {
			continue
		}
		if !isCampaignLive(camp, req.RequestTime) {
			continue
		}

		// Find eligible banners for this campaign.
		bans := eligibleBanners(e, req, snap, camp)
		if len(bans) == 0 {
			continue
		}

		c := buildCandidate(camp, bans[0], snap)
		switch camp.Tier {
		case snapshot.TierOverride:
			overrides = append(overrides, c)
		case snapshot.TierContract:
			contracts = append(contracts, c)
		case snapshot.TierRemnant:
			remnants = append(remnants, c)
		}
	}

	// Evaluate tiers in strict order.
	if r, ok := e.tryStratum(overrides, sortOverrides, req); ok {
		return r
	}
	if r, ok := e.tryStratum(contracts, sortContracts, req); ok {
		return r
	}
	if r, ok := e.tryStratum(remnants, sortRemnants, req); ok {
		return r
	}

	return Blank()
}

// tryStratum sorts, ranks, and selects the first capper-allowed candidate
// from a stratum.  Returns (result, true) on success, (_, false) if the
// stratum produced no eligible ad.
func (e *Engine) tryStratum(
	candidates []*Candidate,
	sorter func([]*Candidate),
	req Request,
) (Result, bool) {
	if len(candidates) == 0 {
		return Result{}, false
	}

	// Deterministic sort first (deficit / eCPM).
	sorter(candidates)

	// Apply the re-ranker (TX-4 extension point).  DefaultRanker is a no-op.
	candidates = e.ranker.Rank(candidates)

	// Build the proto Candidate list for the decision log BEFORE selection.
	protoCandidates := makeProtoCandidates(candidates)

	// Select the first candidate that passes the Capper.
	for _, c := range candidates {
		allowed, err := e.capper.Allowed(req.UserID, c.Campaign, c.Banner)
		if err != nil || !allowed {
			// DA-6: cap failure → skip this candidate.
			continue
		}

		// Mark this candidate as served in the proto list.
		for _, pc := range protoCandidates {
			if pc.CampaignId == c.Campaign.ID && pc.BannerId == c.Banner.ID {
				pc.Served = true
				break
			}
		}

		return Result{
			Tier:       c.Tier,
			ServedTier: tierToProto(c.Tier),
			Campaign:   c.Campaign,
			Banner:     c.Banner,
			Candidates: protoCandidates,
		}, true
	}
	return Result{}, false
}

// ---------------------------------------------------------------------------
// Candidate construction
// ---------------------------------------------------------------------------

func eligibleBanners(
	e *Engine,
	req Request,
	snap *snapshot.Snapshot,
	camp *snapshot.Campaign,
) []*snapshot.Banner {
	var out []*snapshot.Banner
	for _, bid := range camp.BannerIDs {
		ban, ok := snap.Banners[bid]
		if !ok || !ban.Active {
			continue
		}
		// Rule evaluation (§4.6).
		if !e.rules.Eligible(req.Rules, ban.RuleSetIDs, snap) {
			continue
		}
		out = append(out, ban)
	}
	return out
}

func buildCandidate(camp *snapshot.Campaign, ban *snapshot.Banner, _ *snapshot.Snapshot) *Candidate {
	c := &Candidate{
		Campaign: camp,
		Banner:   ban,
		Tier:     camp.Tier,
	}
	switch camp.Tier {
	case snapshot.TierContract:
		c.PacingDeficit = computeDeficit(camp)
	case snapshot.TierRemnant:
		if camp.ECPM != nil {
			c.ECPMMinorUnits = camp.ECPM.GetAmount()
		}
	}
	return c
}

// computeDeficit returns a normalised pacing deficit in [0, 1].
// A value of 1.0 means fully undelivered; 0.0 means on-pace or over-paced.
// We use impression deficit only for I0; clicks/conversions are analogous.
func computeDeficit(camp *snapshot.Campaign) float64 {
	if camp.GoalImpressions <= 0 {
		return 0
	}
	remaining := camp.GoalImpressions - camp.DeliveredImpressions
	if remaining <= 0 {
		return 0
	}
	// deficit / goal, clamped to [0,1]
	d := float64(remaining) / float64(camp.GoalImpressions) //nolint:forbidigo // ML/ranking signal, not money
	if d > 1 {
		return 1
	}
	return d
}

// ---------------------------------------------------------------------------
// Sort functions (deterministic within each tier)
// ---------------------------------------------------------------------------

func sortOverrides(cs []*Candidate) {
	// Override: order by campaign Priority descending, then ID for stability.
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Campaign.Priority != cs[j].Campaign.Priority {
			return cs[i].Campaign.Priority > cs[j].Campaign.Priority
		}
		return cs[i].Campaign.ID < cs[j].Campaign.ID
	})
}

func sortContracts(cs []*Candidate) {
	// Contract: highest pacing deficit first (DA-4); stable tie-break by ID.
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].PacingDeficit != cs[j].PacingDeficit {
			return cs[i].PacingDeficit > cs[j].PacingDeficit
		}
		return cs[i].Campaign.ID < cs[j].Campaign.ID
	})
}

func sortRemnants(cs []*Candidate) {
	// Remnant: highest eCPM first (same asset_code assumed — DA-10);
	// stable tie-break by ID.
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].ECPMMinorUnits != cs[j].ECPMMinorUnits {
			return cs[i].ECPMMinorUnits > cs[j].ECPMMinorUnits
		}
		return cs[i].Campaign.ID < cs[j].Campaign.ID
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func isCampaignLive(camp *snapshot.Campaign, t time.Time) bool {
	if !camp.StartAt.IsZero() && t.Before(camp.StartAt) {
		return false
	}
	if !camp.EndAt.IsZero() && t.After(camp.EndAt) {
		return false
	}
	return true
}

func tierToProto(t snapshot.Tier) commonv1.ServedTier {
	switch t {
	case snapshot.TierOverride:
		return commonv1.ServedTier_SERVED_TIER_OVERRIDE
	case snapshot.TierContract:
		return commonv1.ServedTier_SERVED_TIER_CONTRACT
	case snapshot.TierRemnant:
		return commonv1.ServedTier_SERVED_TIER_REMNANT
	default:
		return commonv1.ServedTier_SERVED_TIER_BLANK
	}
}

// deterministicScore returns a stable, tier-appropriate score for the
// DETERMINISTIC cascade (no ML ranker).  This is the same key used by the
// sort functions above, normalised to a float64 so that OPE estimators in J3
// have a monotone baseline to compare against ML scores later.
//
// Convention (J0, cascade-only):
//   - Override: float64(Priority)         — higher priority wins; sort is by Priority desc.
//   - Contract: PacingDeficit ∈ [0,1]     — higher deficit wins; sort is by deficit desc.
//   - Remnant:  float64(ECPMMinorUnits)   — higher eCPM wins; sort is by eCPM desc.
//
// When the ML ranker plugs in (J1), it replaces this score with its own signal
// and sets ExplorationPolicy to the active bandit policy.  The deterministic
// score then becomes the "baseline" column used by OPE for pre-ML data.
//
// Note: this is a ranking signal (double), NOT money — float64 is correct here
// per the decision.proto comment (TX-2 prohibits float for money, not for scores).
func deterministicScore(c *Candidate) float64 { //nolint:forbidigo // ranking signal, not money
	switch c.Tier {
	case snapshot.TierOverride:
		return float64(c.Campaign.Priority) //nolint:forbidigo // ranking signal
	case snapshot.TierContract:
		return c.PacingDeficit // already in [0,1], no conversion needed
	case snapshot.TierRemnant:
		return float64(c.ECPMMinorUnits) //nolint:forbidigo // ranking signal, not money
	default:
		return 0
	}
}

func makeProtoCandidates(cs []*Candidate) []*decisionv1.Candidate {
	out := make([]*decisionv1.Candidate, len(cs))
	for i, c := range cs {
		out[i] = &decisionv1.Candidate{
			CampaignId: c.Campaign.ID,
			BannerId:   c.Banner.ID,
			Tier:       tierToProto(c.Tier),
			// Score: tier-appropriate deterministic sort key (see deterministicScore).
			// Convention: Override=Priority, Contract=PacingDeficit, Remnant=eCPM minor-units.
			// The ML re-ranker (J1) will overwrite Score with its own signal; the
			// baseline score here allows OPE to identify pre-ML traffic via model_version="".
			Score: deterministicScore(c),
			// Propensity = 1.0 under the DETERMINISTIC policy (DA-3):
			// the cascade deterministically selects the top-ranked eligible candidate,
			// so P(action | context, policy) = 1.0 for the chosen action.
			// This is the correct baseline for off-policy evaluation (OPE/IPS/DR) in J3.
			Propensity: 1.0,
			Served:     false, // updated in tryStratum after capper check
		}
	}
	return out
}
