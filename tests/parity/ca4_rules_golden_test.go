// CA-4 golden tests: regras de entrega §4.6 + anti-contradição.
//
// Cobre: Time-DayOfWeek, Site-URL, Geo-Country/City, Client-Useragent,
// Site-Variable; lógica AND/OR; anti-contradição (Impossible); RuleSet
// reutilizável em N banners.
//
// Dependências: nenhuma infra externa (sem Redis, Postgres, Redpanda).
package parity_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Golden file schema (CA-4)
// ---------------------------------------------------------------------------

type ca4Condition struct {
	Vector     string `json:"vector"`
	Operator   string `json:"operator"`
	Value      string `json:"value"`
	SiteVarKey string `json:"site_var_key"`
}

type ca4Context struct {
	Country     string            `json:"country"`
	City        string            `json:"city"`
	SiteURL     string            `json:"site_url"`
	UserAgent   string            `json:"user_agent"`
	RequestTime string            `json:"request_time"`
	SiteVars    map[string]string `json:"site_vars"`
}

type ca4GoldenInput struct {
	RuleLogic      string         `json:"rule_logic"`
	Conditions     []ca4Condition `json:"conditions"`
	Context        ca4Context     `json:"context"`
	RuleSetIDs     []string       `json:"rule_set_ids"`     // for "no IDs" or "unknown ID" cases
	ValidateFirst  bool           `json:"validate_first"`   // call rules.Validate() before Eligible()
}

type ca4GoldenExpect struct {
	Eligible               bool `json:"eligible"`
	ImpossibleAfterValidate bool `json:"impossible_after_validate"`
}

type ca4GoldenCase struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	CA          string          `json:"ca"`
	Sub         string          `json:"sub"`
	Input       ca4GoldenInput  `json:"input"`
	Expect      ca4GoldenExpect `json:"expect"`
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

func vectorFromStr(s string) snapshot.RuleVector {
	switch s {
	case "TimeDayOfWeek":
		return snapshot.VectorTimeDayOfWeek
	case "SiteURL":
		return snapshot.VectorSiteURL
	case "GeoCountry":
		return snapshot.VectorGeoCountry
	case "GeoCity":
		return snapshot.VectorGeoCity
	case "ClientUA":
		return snapshot.VectorClientUA
	case "SiteVariable":
		return snapshot.VectorSiteVariable
	default:
		return 0
	}
}

func operatorFromStr(s string) snapshot.RuleOperator {
	switch s {
	case "is":
		return snapshot.OpIs
	case "is_not":
		return snapshot.OpIsNot
	case "contains":
		return snapshot.OpContains
	default:
		return 0
	}
}

func logicFromStr(s string) snapshot.RuleLogic {
	switch s {
	case "AND":
		return snapshot.LogicAND
	case "OR":
		return snapshot.LogicOR
	default:
		return snapshot.LogicAND
	}
}

func buildRulesContext(c ca4Context) *rules.Context {
	rt := goldenNow
	if c.RequestTime != "" {
		if parsed, err := time.Parse(time.RFC3339, c.RequestTime); err == nil {
			rt = parsed
		}
	}
	ctx := &rules.Context{
		SiteURL:     c.SiteURL,
		UserAgent:   c.UserAgent,
		RequestTime: rt,
		SiteVars:    c.SiteVars,
	}
	if c.Country != "" || c.City != "" {
		ctx.Geo = &commonv1.Geo{
			Country: c.Country,
			City:    c.City,
		}
	}
	return ctx
}

// ---------------------------------------------------------------------------
// CA-4 golden test runner
// ---------------------------------------------------------------------------

func TestCA4_RulesGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("golden", "ca4_rules.json"))
	if err != nil {
		t.Fatalf("cannot read golden file: %v", err)
	}

	var cases []ca4GoldenCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("cannot parse golden file: %v", err)
	}

	if len(cases) == 0 {
		t.Fatal("golden file has zero cases — gate requires at least one case")
	}

	eng := rules.New()

	for _, gc := range cases {
		gc := gc
		t.Run(gc.ID+"/"+gc.Sub, func(t *testing.T) {
			// Build snapshot and RuleSet from golden input.
			snap := snapshot.EmptySnapshot()
			ctx := buildRulesContext(gc.Input.Context)

			var ruleSetIDs []string

			// "no IDs" case: use the raw RuleSetIDs from the golden file.
			if gc.Input.RuleSetIDs != nil {
				ruleSetIDs = gc.Input.RuleSetIDs
				// Register any legitimate (non-existent) IDs would not be in snap.
				// This exercises the "unknown rule set" fail-closed path.
			} else if gc.Input.RuleLogic != "" || len(gc.Input.Conditions) > 0 {
				// Build a RuleSet from the golden conditions.
				rsID := gc.ID + "-rs"
				ruleSetIDs = []string{rsID}

				conds := make([]snapshot.Condition, 0, len(gc.Input.Conditions))
				for _, c := range gc.Input.Conditions {
					conds = append(conds, snapshot.Condition{
						Vector:     vectorFromStr(c.Vector),
						Operator:   operatorFromStr(c.Operator),
						Value:      c.Value,
						SiteVarKey: c.SiteVarKey,
					})
				}

				rs := &snapshot.RuleSet{
					ID:         rsID,
					TenantID:   "t1",
					Logic:      logicFromStr(gc.Input.RuleLogic),
					Conditions: conds,
				}
				snap.RuleSets[rsID] = rs

				// Run Validate() when: (a) explicitly requested, or (b) the
				// golden case expects the RuleSet to be flagged Impossible —
				// since Impossible can only be set by Validate().
				shouldValidate := gc.Input.ValidateFirst || gc.Expect.ImpossibleAfterValidate
				if shouldValidate {
					rules.Validate(snap)
				}

				// Check Impossible flag if the golden case asserts it.
				if gc.Expect.ImpossibleAfterValidate {
					if !rs.Impossible {
						t.Errorf("[%s] expected RuleSet to be flagged Impossible after Validate(), but it was not", gc.ID)
					}
				} else if shouldValidate {
					// When we validated but did NOT expect Impossible, verify it's NOT flagged.
					if rs.Impossible {
						t.Errorf("[%s] expected RuleSet NOT flagged Impossible, but it was: %s", gc.ID, rs.ImpossibleWhy)
					}
				}
			}
			// If both RuleSetIDs and RuleLogic are absent: ruleSetIDs stays nil
			// → Eligible(ctx, nil, snap) → always eligible (no targeting restriction).

			got := eng.Eligible(ctx, ruleSetIDs, snap)
			if got != gc.Expect.Eligible {
				t.Errorf("[%s] Eligible(): got %v, want %v — %s", gc.ID, got, gc.Expect.Eligible, gc.Description)
			}
		})
	}

	t.Logf("CA-4 golden suite: %d cases evaluated", len(cases))
}

// ---------------------------------------------------------------------------
// CA-4: RuleSet reutilizável em N banners (golden case CA4-RU-001)
// ---------------------------------------------------------------------------

// TestCA4_RuleSet_Reusable_AcrossBanners verifies that a single RuleSet can be
// referenced by multiple banners and evaluated independently for each.
func TestCA4_RuleSet_Reusable_AcrossBanners(t *testing.T) {
	eng := rules.New()
	snap := snapshot.EmptySnapshot()

	// Create a shared RuleSet: only Brazil is eligible.
	rs := &snapshot.RuleSet{
		ID:       "shared-geo-br",
		TenantID: "t1",
		Logic:    snapshot.LogicAND,
		Conditions: []snapshot.Condition{
			{Vector: snapshot.VectorGeoCountry, Operator: snapshot.OpIs, Value: "BR"},
		},
	}
	snap.RuleSets[rs.ID] = rs

	ctxBR := &rules.Context{
		Geo:         &commonv1.Geo{Country: "BR"},
		RequestTime: goldenNow,
	}
	ctxUS := &rules.Context{
		Geo:         &commonv1.Geo{Country: "US"},
		RequestTime: goldenNow,
	}

	// Banner A references the shared rule set.
	bannerARuleSetIDs := []string{"shared-geo-br"}
	// Banner B also references the same shared rule set.
	bannerBRuleSetIDs := []string{"shared-geo-br"}

	// Both banners eligible for BR request.
	if !eng.Eligible(ctxBR, bannerARuleSetIDs, snap) {
		t.Error("banner A: expected eligible for BR request via shared rule set")
	}
	if !eng.Eligible(ctxBR, bannerBRuleSetIDs, snap) {
		t.Error("banner B: expected eligible for BR request via shared rule set")
	}

	// Both banners ineligible for US request.
	if eng.Eligible(ctxUS, bannerARuleSetIDs, snap) {
		t.Error("banner A: expected ineligible for US request via shared rule set")
	}
	if eng.Eligible(ctxUS, bannerBRuleSetIDs, snap) {
		t.Error("banner B: expected ineligible for US request via shared rule set")
	}
}

// ---------------------------------------------------------------------------
// CA-4: Integrated cascade test — rule blocks banner, falls to BLANK
// ---------------------------------------------------------------------------

// TestCA4_RuleBlocksBanner_FallsToBlank verifies the end-to-end path where a
// banner's rule set does not match, causing the cascade to fall through to BLANK.
func TestCA4_RuleBlocksBanner_FallsToBlank(t *testing.T) {
	snap := snapshot.EmptySnapshot()

	// RuleSet: only Saturdays (weekday=6).
	rs := &snapshot.RuleSet{
		ID:       "saturday-only",
		TenantID: "t1",
		Logic:    snapshot.LogicAND,
		Conditions: []snapshot.Condition{
			{Vector: snapshot.VectorTimeDayOfWeek, Operator: snapshot.OpIs, Value: "6"},
		},
	}
	snap.RuleSets[rs.ID] = rs

	snap.Zones["z1"] = &snapshot.Zone{ID: "z1", TenantID: "t1", Active: true}

	camp := makePGoldenCampaign("rm1", snapshot.TierRemnant, 1)
	snap.Campaigns["rm1"] = camp
	snap.ZoneCampaigns["z1"] = []string{"rm1"}

	ban := makePGoldenBanner("ban-rm1", "rm1")
	ban.RuleSetIDs = []string{"saturday-only"} // only Saturdays
	snap.Banners["ban-rm1"] = ban

	eng := cascade.New(rules.New())

	// Wednesday request (goldenNow = 2026-06-03, Wednesday=3) → rule fails → BLANK.
	req := makeGoldenRequest()
	result := eng.Decide(req, snap)

	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("expected BLANK (rule blocks banner on Wednesday), got %v", result.ServedTier)
	}
}

// TestCA4_AntiContradiction_SilencesBanner verifies that a banner whose rule set
// is flagged Impossible by Validate() is permanently silenced in the cascade.
func TestCA4_AntiContradiction_SilencesBanner(t *testing.T) {
	snap := snapshot.EmptySnapshot()

	// Contradictory rule set: GeoCountry IS "BR" AND GeoCountry IS "US"
	rs := &snapshot.RuleSet{
		ID:       "impossible-rs",
		TenantID: "t1",
		Logic:    snapshot.LogicAND,
		Conditions: []snapshot.Condition{
			{Vector: snapshot.VectorGeoCountry, Operator: snapshot.OpIs, Value: "BR"},
			{Vector: snapshot.VectorGeoCountry, Operator: snapshot.OpIs, Value: "US"},
		},
	}
	snap.RuleSets[rs.ID] = rs

	// Run anti-contradiction validation (done once after snapshot build).
	rules.Validate(snap)

	if !rs.Impossible {
		t.Fatal("prerequisite: RuleSet should be flagged Impossible by Validate()")
	}

	snap.Zones["z1"] = &snapshot.Zone{ID: "z1", TenantID: "t1", Active: true}

	camp := makePGoldenCampaign("rm1", snapshot.TierRemnant, 1)
	snap.Campaigns["rm1"] = camp
	snap.ZoneCampaigns["z1"] = []string{"rm1"}

	ban := makePGoldenBanner("ban-rm1", "rm1")
	ban.RuleSetIDs = []string{"impossible-rs"}
	snap.Banners["ban-rm1"] = ban

	eng := cascade.New(rules.New())

	req := makeGoldenRequest()
	req.Rules = &rules.Context{
		Geo:         &commonv1.Geo{Country: "BR"},
		RequestTime: goldenNow,
	}
	result := eng.Decide(req, snap)

	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("expected BLANK (impossible rule set silences banner, CA-4), got %v", result.ServedTier)
	}
}
