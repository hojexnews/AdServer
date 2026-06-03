// Package snapshot defines the in-memory config snapshot consumed by the
// decision hot path.  All fields are immutable after construction; the
// Snapshot value itself is replaced atomically on every refresh cycle.
//
// The Loader interface is the only coupling point to the outside world
// (Postgres / db/config/).  Nothing in this package touches the network;
// the hot path reads from a pre-built *Snapshot already in memory.
package snapshot

import (
	"time"

	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

// ---------------------------------------------------------------------------
// Campaign tier / priority
// ---------------------------------------------------------------------------

// Tier mirrors the ServedTier enum from commonv1 but is kept as a plain Go
// type so internal packages can switch on it without importing the proto
// package everywhere.
type Tier int8

const (
	TierUnspecified Tier = 0
	TierOverride    Tier = 1
	TierContract    Tier = 2
	TierRemnant     Tier = 3
)

func (t Tier) String() string {
	switch t {
	case TierOverride:
		return "OVERRIDE"
	case TierContract:
		return "CONTRACT"
	case TierRemnant:
		return "REMNANT"
	default:
		return "UNSPECIFIED"
	}
}

// ---------------------------------------------------------------------------
// Pricing model
// ---------------------------------------------------------------------------

// PricingModel is the billing type of a campaign.
type PricingModel int8

const (
	PricingCPM     PricingModel = 1
	PricingCPC     PricingModel = 2
	PricingCPA     PricingModel = 3
	PricingTenancy PricingModel = 4
)

// ---------------------------------------------------------------------------
// Delivery rule model (§4.6)
// ---------------------------------------------------------------------------

// RuleVector identifies which request attribute a condition matches against.
type RuleVector int8

const (
	VectorTimeDayOfWeek  RuleVector = 1 // 0=Sun … 6=Sat (time.Weekday)
	VectorSiteURL        RuleVector = 2
	VectorGeoCountry     RuleVector = 3
	VectorGeoCity        RuleVector = 4
	VectorClientUA       RuleVector = 5
	VectorSiteVariable   RuleVector = 6 // first-party custom var
)

// RuleOperator is the comparison operator for a condition.
type RuleOperator int8

const (
	OpIs       RuleOperator = 1
	OpIsNot    RuleOperator = 2
	OpContains RuleOperator = 3
)

// RuleLogic is the boolean combinator for a RuleSet.
type RuleLogic int8

const (
	LogicAND RuleLogic = 1
	LogicOR  RuleLogic = 2
)

// Condition is a single predicate: vector op value.
type Condition struct {
	Vector   RuleVector
	Operator RuleOperator
	// Value is always a string; time conditions use "0"…"6" (weekday).
	Value string
	// SiteVarKey is populated only when Vector == VectorSiteVariable.
	SiteVarKey string
}

// RuleSet is a reusable named group of conditions with a combinator.
// A nil/empty Conditions slice is always satisfied (open rule).
type RuleSet struct {
	ID           string
	TenantID     string
	Logic        RuleLogic
	Conditions   []Condition
	// Impossible is set by the anti-contradiction checker (CA-4).
	// When true the rule set will never match any request; banners
	// that reference it are silenced permanently.
	Impossible   bool
	ImpossibleWhy string // human-readable explanation for ops
}

// ---------------------------------------------------------------------------
// Banner / creative
// ---------------------------------------------------------------------------

// Banner is one creative variant belonging to a campaign.
type Banner struct {
	ID         string
	TenantID   string
	CampaignID string

	// Creative payload — only one of these is non-empty.
	ImageURL   string // static image
	HTML       string // HTML5 / third-party tag
	VideoURL   string // VAST source
	ClickURL   string

	Width  int32
	Height int32

	// Frequency capping overrides at banner level (DA-6).
	// Zero value means "use campaign cap".  Banner cap takes precedence.
	CapTotal   int32 // total impressions per user (0 = no cap)
	CapSession int32 // impressions per session (0 = no cap)
	CapClock   int32 // impressions per clock window (0 = no cap)
	CapClockWindowSec int32

	// RuleSetIDs lists the RuleSets that ALL must pass for this banner
	// to be eligible (AND across sets, OR within each set per its own
	// Logic field).
	RuleSetIDs []string

	Active bool
}

// ---------------------------------------------------------------------------
// Campaign
// ---------------------------------------------------------------------------

// Campaign is one advertising campaign.  It holds the pacing state needed
// by the cascade to decide whether a Contract campaign is in deficit.
type Campaign struct {
	ID       string
	TenantID string
	Name     string

	Tier        Tier
	Priority    int32 // higher wins within the same tier (used by re-ranker)
	PricingModel PricingModel

	// eCPM stored as Money (int64 minor-units, no float).  Used only for
	// same-asset comparisons inside money.CompareECPM.
	ECPM *moneyv1.Money

	StartAt time.Time
	EndAt   time.Time

	// Contract pacing fields (DA-4).  Ignored for Override/Remnant.
	GoalImpressions int64  // absolute target for the contract window
	GoalClicks      int64
	GoalConversions int64

	// Delivered counts loaded from the stats store at snapshot build time.
	// These are APPROXIMATE; the authoritative source is the batch ledger.
	DeliveredImpressions int64
	DeliveredClicks      int64
	DeliveredConversions int64

	// Frequency capping at campaign level (DA-6).
	CapTotal   int32
	CapSession int32
	CapClock   int32
	CapClockWindowSec int32

	// BannerIDs that belong to this campaign (resolved at snapshot build time).
	BannerIDs []string

	// ZoneIDs this campaign is linked to (N:N, DA-2).
	ZoneIDs []string

	Active bool
}

// ---------------------------------------------------------------------------
// Zone (placement / inventory)
// ---------------------------------------------------------------------------

// Zone is a publisher placement that accepts ads.
type Zone struct {
	ID       string
	TenantID string
	SiteID   string
	Name     string
	Width    int32
	Height   int32
	Active   bool
}

// ---------------------------------------------------------------------------
// Snapshot (root)
// ---------------------------------------------------------------------------

// Snapshot is an immutable, versioned view of all ad-serving config.
// It is replaced atomically in memory on each refresh; the hot path reads
// from it without any synchronisation on the individual fields.
type Snapshot struct {
	// Version is a monotonically increasing counter or a timestamp string
	// (e.g. RFC3339).  Used for observability, not for correctness.
	Version string
	// LoadedAt is when this snapshot was built from the backing store.
	LoadedAt time.Time

	// Primary indexes — all maps are read-only after construction.
	Campaigns map[string]*Campaign // campaign_id → Campaign
	Banners   map[string]*Banner   // banner_id   → Banner
	Zones     map[string]*Zone     // zone_id     → Zone
	RuleSets  map[string]*RuleSet  // ruleset_id  → RuleSet

	// ZoneCampaigns maps zone_id → []campaign_id eligible for that zone.
	// Pre-computed at snapshot build time to make the hot path O(1).
	ZoneCampaigns map[string][]string
}
