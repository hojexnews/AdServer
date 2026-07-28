// Package configload builds the in-memory decision Snapshot from the
// relational config store (db/config/, Postgres).
//
// It is the missing wire between I1 (the SQL schema) and I0 (the hot-path
// snapshot): the decision service consumes an immutable *snapshot.Snapshot
// already in memory, and this package is the only code that reads Postgres
// and translates rows into that structure.
//
// Layering: this package imports snapshot + rules + money (it may run the
// CA-4 anti-contradiction validator).  The snapshot package itself stays
// dependency-free for the hot path; putting the loader here avoids the
// snapshot→rules import cycle.
//
// # Tenancy (CA-1 / TX-3)
//
// The in-memory snapshot is intentionally GLOBAL (cross-tenant): one
// decision process serves every zone and derives the tenant SERVER-SIDE
// from the zone (snapshot.Zone.TenantID).  CA-1 isolation is enforced in the
// cascade (zone.TenantID must match the server-derived tenant), not by
// per-row RLS on the read path.  Because db/config tables use FORCE ROW
// LEVEL SECURITY, the loader connects with a dedicated read-only role that
// holds BYPASSRLS (adserver_loader) — see deploy/local and the package
// README.  Writes (BFF/console) still go through the per-tenant adserver_app
// role with RLS enforced.
package configload

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Raw rows — plain Go mirrors of db/config rows (no pgx types here so the
// assembly logic is unit-testable without a database).
// ---------------------------------------------------------------------------

// ZoneRow mirrors config.zones (joined to config.sites for site_id label).
type ZoneRow struct {
	ID       string
	TenantID string
	SiteID   string
	Name     string
	Width    int32
	Height   int32
	Active   bool
}

// CampaignRow mirrors config.campaigns with the rate already resolved to
// integer minor-units at the asset's scale (no float; see decimalToMinor).
type CampaignRow struct {
	ID           string
	TenantID     string
	Type         string // config.campaign_type: override|contract|remnant
	Priority     int32
	PricingModel string // config.pricing_model: cpm|cpc|cpa|tenancy
	Currency     string
	RateMinor    int64 // rate converted to minor-units of Currency
	Scale        int32 // asset scale used for RateMinor
	GoalTarget   int64
	GoalValid    bool   // false when goal_target IS NULL (tenancy)
	GoalMetric   string // config.goal_metric: impressions|clicks|conversions
	StartAt      time.Time
	EndAt        time.Time
	Active       bool
}

// BannerRow mirrors config.banners.
type BannerRow struct {
	ID           string
	TenantID     string
	CampaignID   string
	CreativeType string // config.creative_type: image|html5|thirdparty_tag|video
	AssetURL     string // empty when stored inline as a blob
	AssetBlob    string // inline creative (html5/thirdparty tag); empty otherwise
	DestURL      string
	Width        int32
	Height       int32
	Active       bool
}

// CapRow mirrors config.caps.
type CapRow struct {
	OwnerType     string // campaign|banner
	OwnerID       string
	Scope         string // campaign_total|session|clock
	LimitCount    int64
	ResetInterval int32
	ResetValid    bool
	Active        bool
}

// RuleRow mirrors config.delivery_rules.
//
// Only DIRECT rules (rule_set_id IS NULL) are consumed in this increment:
// they bind to a campaign or banner via (owner_type, owner_id).  Named,
// reusable rule sets (config.delivery_rule_sets) have no binding column to
// campaigns/banners in the I1 schema yet, so they are not wired here — that
// is a follow-up to the config schema, documented in the package README.
type RuleRow struct {
	OwnerType   string // campaign|banner
	OwnerID     string
	Vector      string
	Operator    string
	Value       string
	LogicalOp   string // AND|OR
	RuleSetID   string
	RuleSetHeld bool // true when rule_set_id IS NOT NULL (skipped this increment)
	Active      bool
}

// CampaignZoneRow mirrors config.campaign_zones (N:N, DA-2).
type CampaignZoneRow struct {
	CampaignID string
	ZoneID     string
}

// DeliveredRow is one per-campaign aggregate of delivered counts, sourced
// from the perfil BETA Postgres telemetry sink (stats.events_raw — ADR-0005,
// db/stats/migrations/0001_stats_schema_up.sql). impressions counts only
// billable=true rows (CA-6: a blank impression is never "delivered" for
// pacing purposes, same rule stats.live_kpis already applies). A campaign
// with no rows in the stats store simply has no DeliveredRow — see
// Assemble, which leaves snapshot.Campaign's Delivered* fields at their
// zero value for any campaign absent from this slice.
type DeliveredRow struct {
	CampaignID  string
	Impressions int64
	Clicks      int64
	Conversions int64
}

// RawConfig is the full set of rows pulled from db/config for one snapshot.
type RawConfig struct {
	Zones         []ZoneRow
	Campaigns     []CampaignRow
	Banners       []BannerRow
	Caps          []CapRow
	Rules         []RuleRow
	CampaignZones []CampaignZoneRow
	// Delivered is OPTIONAL: nil/empty when the stats store is unavailable
	// (see PostgresLoader.loadDelivered's degradation policy). Every
	// snapshot.Campaign.Delivered* field simply stays at its zero value in
	// that case — identical to this package's behavior before DeliveredRow
	// existed.
	Delivered []DeliveredRow
}

// ---------------------------------------------------------------------------
// Assemble — pure RawConfig → *snapshot.Snapshot
// ---------------------------------------------------------------------------

// Assemble translates raw config rows into an immutable Snapshot and runs the
// CA-4 anti-contradiction validator over the synthesized rule sets.
//
// version/now are stamped onto the snapshot for observability.  The function
// is pure (no I/O) and deterministic for a given input.
func Assemble(raw RawConfig, version string, now time.Time) *snapshot.Snapshot {
	snap := &snapshot.Snapshot{
		Version:       version,
		LoadedAt:      now,
		Campaigns:     make(map[string]*snapshot.Campaign, len(raw.Campaigns)),
		Banners:       make(map[string]*snapshot.Banner, len(raw.Banners)),
		Zones:         make(map[string]*snapshot.Zone, len(raw.Zones)),
		RuleSets:      make(map[string]*snapshot.RuleSet),
		ZoneCampaigns: make(map[string][]string),
	}

	// Zones.
	for _, z := range raw.Zones {
		snap.Zones[z.ID] = &snapshot.Zone{
			ID:       z.ID,
			TenantID: z.TenantID,
			SiteID:   z.SiteID,
			Name:     z.Name,
			Width:    z.Width,
			Height:   z.Height,
			Active:   z.Active,
		}
	}

	// Cap index: owner key ("campaign:<id>" / "banner:<id>") → caps.
	capsByOwner := make(map[string][]CapRow)
	for _, c := range raw.Caps {
		if !c.Active {
			continue
		}
		k := c.OwnerType + ":" + c.OwnerID
		capsByOwner[k] = append(capsByOwner[k], c)
	}

	// Synthesize rule sets from DIRECT rules, grouped by owner.
	// Returns owner key → []ruleSetID referenced, and fills snap.RuleSets.
	ownerRuleSets := synthesizeRuleSets(raw.Rules, snap)

	// Campaigns.
	for _, cr := range raw.Campaigns {
		camp := &snapshot.Campaign{
			ID:           cr.ID,
			TenantID:     cr.TenantID,
			Tier:         tierFromType(cr.Type),
			Priority:     cr.Priority,
			PricingModel: pricingFromModel(cr.PricingModel),
			ECPM: &moneyv1.Money{
				AssetCode: cr.Currency,
				Amount:    cr.RateMinor,
				Scale:     uint32(maxInt32(cr.Scale, 0)),
			},
			StartAt: cr.StartAt,
			EndAt:   cr.EndAt,
			Active:  cr.Active,
		}
		if cr.GoalValid {
			switch cr.GoalMetric {
			case "impressions":
				camp.GoalImpressions = cr.GoalTarget
			case "clicks":
				camp.GoalClicks = cr.GoalTarget
			case "conversions":
				camp.GoalConversions = cr.GoalTarget
			}
		}
		applyCaps(capsByOwner["campaign:"+cr.ID],
			&camp.CapTotal, &camp.CapSession, &camp.CapClock, &camp.CapClockWindowSec)
		snap.Campaigns[cr.ID] = camp
	}

	// Delivered counts (DA-4 pacing input; perfil BETA / ADR-0005 — see
	// DeliveredRow's doc). Matched onto the campaigns just built above; a
	// campaign_id present in raw.Delivered but absent from snap.Campaigns
	// (e.g. a since-deleted campaign with historical events) is silently
	// dropped — there is no snapshot.Campaign to attach it to, and this is
	// not a targeting/eligibility change (no campaign is added, removed, or
	// filtered by this loop — only an existing Campaign's counters are set).
	for _, d := range raw.Delivered {
		if camp, ok := snap.Campaigns[d.CampaignID]; ok {
			camp.DeliveredImpressions = d.Impressions
			camp.DeliveredClicks = d.Clicks
			camp.DeliveredConversions = d.Conversions
		}
	}

	// Banners — attach to campaigns and compute eligible rule sets.
	for _, br := range raw.Banners {
		ban := &snapshot.Banner{
			ID:         br.ID,
			TenantID:   br.TenantID,
			CampaignID: br.CampaignID,
			ClickURL:   br.DestURL,
			Width:      br.Width,
			Height:     br.Height,
			Active:     br.Active,
		}
		setCreative(ban, br)

		applyCaps(capsByOwner["banner:"+br.ID],
			&ban.CapTotal, &ban.CapSession, &ban.CapClock, &ban.CapClockWindowSec)

		// A banner must satisfy its campaign's rule sets AND its own (AND
		// across sets — the rules engine ANDs all referenced sets).
		ban.RuleSetIDs = append(ban.RuleSetIDs,
			ownerRuleSets["campaign:"+br.CampaignID]...)
		ban.RuleSetIDs = append(ban.RuleSetIDs,
			ownerRuleSets["banner:"+br.ID]...)

		snap.Banners[br.ID] = ban
		if camp, ok := snap.Campaigns[br.CampaignID]; ok {
			camp.BannerIDs = append(camp.BannerIDs, br.ID)
		}
	}

	// N:N campaign↔zone (DA-2): build both the zone→campaigns index used by
	// the hot path and the campaign.ZoneIDs back-reference.
	for _, cz := range raw.CampaignZones {
		snap.ZoneCampaigns[cz.ZoneID] = append(snap.ZoneCampaigns[cz.ZoneID], cz.CampaignID)
		if camp, ok := snap.Campaigns[cz.CampaignID]; ok {
			camp.ZoneIDs = append(camp.ZoneIDs, cz.ZoneID)
		}
	}

	// CA-4: mark logically impossible rule sets so referencing banners are
	// silenced permanently.  Runs once, before the snapshot is published.
	rules.Validate(snap)

	return snap
}

// ---------------------------------------------------------------------------
// Rule set synthesis
// ---------------------------------------------------------------------------

// synthesizeRuleSets turns DIRECT delivery rules into snapshot rule sets.
//
// For each owner (campaign or banner) the active direct rules are partitioned
// by their logical_op: AND-rules become one all-must-pass set, OR-rules become
// one any-may-pass set.  The rules engine ANDs all sets a banner references,
// yielding "(all AND-rules) AND (at least one OR-rule)" — the §4.6 semantics.
//
// Fail-closed: a rule whose vector/operator the engine cannot evaluate marks
// its synthesized set Impossible (the banner is silenced) rather than silently
// dropping a targeting restriction and over-serving.
//
// Returns owner key ("campaign:<id>" / "banner:<id>") → referenced set IDs and
// fills snap.RuleSets in place.
func synthesizeRuleSets(ruleRows []RuleRow, snap *snapshot.Snapshot) map[string][]string {
	type bucket struct {
		and []snapshot.Condition
		or  []snapshot.Condition
		bad string // non-empty when an unsupported vector/operator was seen
	}
	byOwner := make(map[string]*bucket)

	for _, rr := range ruleRows {
		if !rr.Active || rr.RuleSetHeld {
			// Skip inactive rules and members of named rule sets (no binding
			// column to owners in the I1 schema — see RuleRow doc).
			continue
		}
		ownerKey := rr.OwnerType + ":" + rr.OwnerID
		b := byOwner[ownerKey]
		if b == nil {
			b = &bucket{}
			byOwner[ownerKey] = b
		}

		cond, err := buildCondition(rr)
		if err != nil {
			if b.bad == "" {
				b.bad = err.Error()
			}
			continue
		}
		if strings.EqualFold(rr.LogicalOp, "OR") {
			b.or = append(b.or, cond)
		} else {
			b.and = append(b.and, cond)
		}
	}

	out := make(map[string][]string, len(byOwner))
	for ownerKey, b := range byOwner {
		ownerID := strings.SplitN(ownerKey, ":", 2)[1]
		prefix := ruleSetPrefix(ownerKey)

		if len(b.and) > 0 || b.bad != "" {
			id := prefix + ownerID + ":and"
			rs := &snapshot.RuleSet{ID: id, Logic: snapshot.LogicAND, Conditions: b.and}
			if b.bad != "" {
				rs.Impossible = true
				rs.ImpossibleWhy = "unsupported rule (fail-closed): " + b.bad
			}
			snap.RuleSets[id] = rs
			out[ownerKey] = append(out[ownerKey], id)
		}
		if len(b.or) > 0 {
			id := prefix + ownerID + ":or"
			snap.RuleSets[id] = &snapshot.RuleSet{ID: id, Logic: snapshot.LogicOR, Conditions: b.or}
			out[ownerKey] = append(out[ownerKey], id)
		}
	}
	return out
}

func ruleSetPrefix(ownerKey string) string {
	if strings.HasPrefix(ownerKey, "banner:") {
		return "b:"
	}
	return "c:"
}

// buildCondition maps a db rule row to a snapshot.Condition, returning an
// error for vectors/operators the rules engine cannot evaluate.
func buildCondition(rr RuleRow) (snapshot.Condition, error) {
	vec, ok := vectorFromString(rr.Vector)
	if !ok {
		return snapshot.Condition{}, fmt.Errorf("vector %q", rr.Vector)
	}
	op, ok := operatorFromString(rr.Operator)
	if !ok {
		return snapshot.Condition{}, fmt.Errorf("operator %q", rr.Operator)
	}
	cond := snapshot.Condition{Vector: vec, Operator: op, Value: rr.Value}
	// Site-Variable rules carry "key=value" in the value column (no dedicated
	// key column in the I1 schema); split the first '=' into key + value.
	if vec == snapshot.VectorSiteVariable {
		if k, v, found := strings.Cut(rr.Value, "="); found {
			cond.SiteVarKey = k
			cond.Value = v
		}
	}
	return cond, nil
}

// ---------------------------------------------------------------------------
// Enum / string mapping
// ---------------------------------------------------------------------------

func tierFromType(t string) snapshot.Tier {
	switch t {
	case "override":
		return snapshot.TierOverride
	case "contract":
		return snapshot.TierContract
	case "remnant":
		return snapshot.TierRemnant
	default:
		return snapshot.TierUnspecified
	}
}

func pricingFromModel(m string) snapshot.PricingModel {
	switch m {
	case "cpm":
		return snapshot.PricingCPM
	case "cpc":
		return snapshot.PricingCPC
	case "cpa":
		return snapshot.PricingCPA
	case "tenancy":
		return snapshot.PricingTenancy
	default:
		return 0
	}
}

// vectorFromString maps the db delivery_rules.vector label to a RuleVector.
func vectorFromString(v string) (snapshot.RuleVector, bool) {
	switch v {
	case "Time - Day of Week":
		return snapshot.VectorTimeDayOfWeek, true
	case "Site - URL":
		return snapshot.VectorSiteURL, true
	case "Geo - Country":
		return snapshot.VectorGeoCountry, true
	case "Geo - City":
		return snapshot.VectorGeoCity, true
	case "Client - Useragent":
		return snapshot.VectorClientUA, true
	case "Site - Variable":
		return snapshot.VectorSiteVariable, true
	default:
		return 0, false
	}
}

// operatorFromString maps the db operator label to a RuleOperator.
// The engine supports is/is not/contains; the other operators allowed by the
// schema CHECK (does not contain/starts with/ends with) are not evaluable and
// are reported as unsupported (fail-closed by the caller).
func operatorFromString(op string) (snapshot.RuleOperator, bool) {
	switch op {
	case "is":
		return snapshot.OpIs, true
	case "is not":
		return snapshot.OpIsNot, true
	case "contains":
		return snapshot.OpContains, true
	default:
		return 0, false
	}
}

// ---------------------------------------------------------------------------
// Creative + caps
// ---------------------------------------------------------------------------

// setCreative fills the creative payload field that matches the banner type.
func setCreative(ban *snapshot.Banner, br BannerRow) {
	switch br.CreativeType {
	case "image":
		ban.ImageURL = br.AssetURL
	case "video":
		ban.VideoURL = br.AssetURL
	case "html5", "thirdparty_tag":
		if br.AssetBlob != "" {
			ban.HTML = br.AssetBlob
		} else {
			ban.HTML = br.AssetURL
		}
	}
}

// applyCaps writes the cap rows onto the destination cap fields by scope.
func applyCaps(caps []CapRow, total, session, clock, clockWindow *int32) {
	for _, c := range caps {
		switch c.Scope {
		case "campaign_total":
			*total = clampInt32(c.LimitCount)
		case "session":
			*session = clampInt32(c.LimitCount)
		case "clock":
			*clock = clampInt32(c.LimitCount)
			if c.ResetValid {
				*clockWindow = c.ResetInterval
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Decimal → minor units (no float — TX-2)
// ---------------------------------------------------------------------------

// decimalToMinor converts a NUMERIC string ("12.50", "0.000001", "-3") to an
// integer count of minor units at the given asset scale, using banker's
// rounding (ROUND_HALF_EVEN) when the fractional part has more digits than the
// scale.  It is integer/string arithmetic only — no float, no big.Int.
//
// Returns an error on overflow of int64 or an unparseable input.
func decimalToMinor(s string, scale int32) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	intPart, fracPart, _ := strings.Cut(s, ".")
	if intPart == "" {
		intPart = "0"
	}
	for _, r := range intPart {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("decimalToMinor: bad integer part in %q", s)
		}
	}
	for _, r := range fracPart {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("decimalToMinor: bad fractional part in %q", s)
		}
	}
	if scale < 0 {
		scale = 0
	}

	// Pad or round the fractional part to exactly `scale` digits.
	var kept string
	if int(scale) <= len(fracPart) {
		kept = fracPart[:scale]
		roundUp := shouldRoundHalfEven(intPart, kept, fracPart[scale:])
		digits := intPart + kept
		mag, err := strconv.ParseInt(trimLeadingZeros(digits), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("decimalToMinor: overflow/parse %q: %w", s, err)
		}
		if roundUp {
			if mag == int64(^uint64(0)>>1) { // MaxInt64
				return 0, fmt.Errorf("decimalToMinor: overflow rounding %q", s)
			}
			mag++
		}
		return signed(mag, neg), nil
	}
	// Fewer fractional digits than scale: right-pad with zeros.
	kept = fracPart + strings.Repeat("0", int(scale)-len(fracPart))
	digits := intPart + kept
	mag, err := strconv.ParseInt(trimLeadingZeros(digits), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("decimalToMinor: overflow/parse %q: %w", s, err)
	}
	return signed(mag, neg), nil
}

// shouldRoundHalfEven decides whether to round the kept magnitude up given the
// dropped fractional digits, using round-half-to-even on the last kept digit.
func shouldRoundHalfEven(intPart, kept, dropped string) bool {
	if dropped == "" {
		return false
	}
	first := dropped[0]
	if first < '5' {
		return false
	}
	if first > '5' {
		return true
	}
	// Leading dropped digit is exactly 5: round up only if anything follows it
	// is non-zero, otherwise round to even.
	for i := 1; i < len(dropped); i++ {
		if dropped[i] != '0' {
			return true
		}
	}
	// Exactly .5 — round to even: up only if the last kept digit is odd.
	lastKept := byte('0')
	if kept != "" {
		lastKept = kept[len(kept)-1]
	} else if intPart != "" {
		lastKept = intPart[len(intPart)-1]
	}
	return (lastKept-'0')%2 == 1
}

func trimLeadingZeros(s string) string {
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0"
	}
	return s
}

func signed(mag int64, neg bool) int64 {
	if neg {
		return -mag
	}
	return mag
}

// ---------------------------------------------------------------------------
// small numeric helpers
// ---------------------------------------------------------------------------

func clampInt32(v int64) int32 {
	if v > int64(^uint32(0)>>1) { // MaxInt32
		return int32(^uint32(0) >> 1)
	}
	if v < 0 {
		return 0
	}
	return int32(v)
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
