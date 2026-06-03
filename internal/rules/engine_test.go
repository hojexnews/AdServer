package rules_test

import (
	"testing"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

var testNow = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC) // Wednesday = 3

func makeCtx() *rules.Context {
	return &rules.Context{
		Geo:         &commonv1.Geo{Country: "BR", City: "Sao Paulo"},
		SiteURL:     "https://news.example.com/article/123",
		UserAgent:   "Mozilla/5.0 (Linux; Android 14)",
		RequestTime: testNow,
		SiteVars:    map[string]string{"section": "sports", "lang": "pt"},
	}
}

func makeSnap(rulesets ...*snapshot.RuleSet) *snapshot.Snapshot {
	s := snapshot.EmptySnapshot()
	for _, rs := range rulesets {
		s.RuleSets[rs.ID] = rs
	}
	return s
}

func eng() *rules.Engine { return rules.New() }

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func rs(id string, logic snapshot.RuleLogic, conds ...snapshot.Condition) *snapshot.RuleSet {
	return &snapshot.RuleSet{
		ID:         id,
		TenantID:   "t1",
		Logic:      logic,
		Conditions: conds,
	}
}

func cond(vec snapshot.RuleVector, op snapshot.RuleOperator, val string) snapshot.Condition {
	return snapshot.Condition{Vector: vec, Operator: op, Value: val}
}

func condVar(key, val string, op snapshot.RuleOperator) snapshot.Condition {
	return snapshot.Condition{
		Vector:     snapshot.VectorSiteVariable,
		Operator:   op,
		Value:      val,
		SiteVarKey: key,
	}
}

// ---------------------------------------------------------------------------
// Vector / operator golden tests (CA-4)
// ---------------------------------------------------------------------------

func TestRules_GeoCountry_Is(t *testing.T) {
	set := rs("r1", snapshot.LogicAND, cond(snapshot.VectorGeoCountry, snapshot.OpIs, "BR"))
	snap := makeSnap(set)
	if !eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected eligible for country BR")
	}
}

func TestRules_GeoCountry_IsNot(t *testing.T) {
	set := rs("r1", snapshot.LogicAND, cond(snapshot.VectorGeoCountry, snapshot.OpIsNot, "US"))
	snap := makeSnap(set)
	if !eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected eligible: country is BR, not US")
	}
}

func TestRules_GeoCountry_IsNot_Fails(t *testing.T) {
	set := rs("r1", snapshot.LogicAND, cond(snapshot.VectorGeoCountry, snapshot.OpIsNot, "BR"))
	snap := makeSnap(set)
	if eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected ineligible: country IS BR and rule says IS-NOT BR")
	}
}

func TestRules_GeoCity_Contains(t *testing.T) {
	set := rs("r1", snapshot.LogicAND, cond(snapshot.VectorGeoCity, snapshot.OpContains, "paulo"))
	snap := makeSnap(set)
	if !eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected eligible: city contains 'paulo'")
	}
}

func TestRules_SiteURL_Contains(t *testing.T) {
	set := rs("r1", snapshot.LogicAND, cond(snapshot.VectorSiteURL, snapshot.OpContains, "news.example.com"))
	snap := makeSnap(set)
	if !eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected eligible: URL contains 'news.example.com'")
	}
}

func TestRules_UserAgent_Contains(t *testing.T) {
	set := rs("r1", snapshot.LogicAND, cond(snapshot.VectorClientUA, snapshot.OpContains, "android"))
	snap := makeSnap(set)
	if !eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected eligible: UA contains 'android'")
	}
}

func TestRules_DayOfWeek_Is_Wednesday(t *testing.T) {
	// testNow is Wednesday = 3.
	set := rs("r1", snapshot.LogicAND, cond(snapshot.VectorTimeDayOfWeek, snapshot.OpIs, "3"))
	snap := makeSnap(set)
	if !eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected eligible: today is Wednesday (3)")
	}
}

func TestRules_DayOfWeek_IsNot_Wednesday(t *testing.T) {
	set := rs("r1", snapshot.LogicAND, cond(snapshot.VectorTimeDayOfWeek, snapshot.OpIsNot, "3"))
	snap := makeSnap(set)
	if eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected ineligible: today IS Wednesday and rule says IS-NOT 3")
	}
}

func TestRules_SiteVariable_Is(t *testing.T) {
	set := rs("r1", snapshot.LogicAND, condVar("section", "sports", snapshot.OpIs))
	snap := makeSnap(set)
	if !eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected eligible: section == sports")
	}
}

func TestRules_SiteVariable_IsNot(t *testing.T) {
	set := rs("r1", snapshot.LogicAND, condVar("section", "finance", snapshot.OpIsNot))
	snap := makeSnap(set)
	if !eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected eligible: section IS NOT finance")
	}
}

// ---------------------------------------------------------------------------
// AND / OR logic
// ---------------------------------------------------------------------------

func TestRules_AND_AllMustPass(t *testing.T) {
	set := rs("r1", snapshot.LogicAND,
		cond(snapshot.VectorGeoCountry, snapshot.OpIs, "BR"),
		cond(snapshot.VectorGeoCity, snapshot.OpIs, "Rio de Janeiro"), // does not match
	)
	snap := makeSnap(set)
	if eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected ineligible: city is Sao Paulo not Rio")
	}
}

func TestRules_OR_AnyPasses(t *testing.T) {
	set := rs("r1", snapshot.LogicOR,
		cond(snapshot.VectorGeoCountry, snapshot.OpIs, "US"), // false
		cond(snapshot.VectorGeoCity, snapshot.OpIs, "Sao Paulo"), // true
	)
	snap := makeSnap(set)
	if !eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected eligible: OR with one true condition")
	}
}

func TestRules_EmptyRuleSet_AlwaysEligible(t *testing.T) {
	set := rs("r1", snapshot.LogicAND) // no conditions
	snap := makeSnap(set)
	if !eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("expected eligible: empty rule set is open (no targeting)")
	}
}

func TestRules_NoRuleSets_AlwaysEligible(t *testing.T) {
	snap := makeSnap()
	if !eng().Eligible(makeCtx(), nil, snap) {
		t.Error("expected eligible: nil rule set IDs means unrestricted")
	}
}

func TestRules_UnknownRuleSetID_Ineligible(t *testing.T) {
	snap := makeSnap()
	if eng().Eligible(makeCtx(), []string{"nonexistent"}, snap) {
		t.Error("expected ineligible: unknown rule set ID → fail closed")
	}
}

// ---------------------------------------------------------------------------
// Anti-contradiction (CA-4)
// ---------------------------------------------------------------------------

func TestRules_Validate_DetectsImpossible_TwoIsOnSameVector(t *testing.T) {
	set := rs("r1", snapshot.LogicAND,
		cond(snapshot.VectorGeoCountry, snapshot.OpIs, "BR"),
		cond(snapshot.VectorGeoCountry, snapshot.OpIs, "US"), // contradicts
	)
	snap := makeSnap(set)
	rules.Validate(snap)

	if !snap.RuleSets["r1"].Impossible {
		t.Error("expected CA-4: AND with two IS on same vector should be Impossible")
	}
}

func TestRules_Validate_DetectsImpossible_IsAndIsNotSameValue(t *testing.T) {
	set := rs("r1", snapshot.LogicAND,
		cond(snapshot.VectorGeoCountry, snapshot.OpIs, "BR"),
		cond(snapshot.VectorGeoCountry, snapshot.OpIsNot, "BR"), // x IS BR AND x IS NOT BR
	)
	snap := makeSnap(set)
	rules.Validate(snap)

	if !snap.RuleSets["r1"].Impossible {
		t.Error("expected CA-4: IS BR AND IS-NOT BR should be Impossible")
	}
}

func TestRules_Validate_DetectsImpossible_AllDaysExcluded(t *testing.T) {
	conditions := make([]snapshot.Condition, 7)
	for i := 0; i < 7; i++ {
		conditions[i] = cond(snapshot.VectorTimeDayOfWeek, snapshot.OpIsNot, string(rune('0'+i)))
	}
	set := &snapshot.RuleSet{
		ID:         "r1",
		TenantID:   "t1",
		Logic:      snapshot.LogicAND,
		Conditions: conditions,
	}
	snap := makeSnap(set)
	rules.Validate(snap)

	if !snap.RuleSets["r1"].Impossible {
		t.Error("expected CA-4: excluding all 7 weekdays should be Impossible")
	}
}

func TestRules_Validate_OR_NotFlagged(t *testing.T) {
	// Under OR we cannot prove impossibility without further analysis.
	set := rs("r1", snapshot.LogicOR,
		cond(snapshot.VectorGeoCountry, snapshot.OpIs, "BR"),
		cond(snapshot.VectorGeoCountry, snapshot.OpIs, "US"),
	)
	snap := makeSnap(set)
	rules.Validate(snap)

	if snap.RuleSets["r1"].Impossible {
		t.Error("OR with two IS conditions should NOT be flagged as Impossible")
	}
}

func TestRules_Validate_ImpossibleRuleSet_BlocksBanner(t *testing.T) {
	set := rs("r1", snapshot.LogicAND,
		cond(snapshot.VectorGeoCountry, snapshot.OpIs, "BR"),
		cond(snapshot.VectorGeoCountry, snapshot.OpIs, "US"),
	)
	snap := makeSnap(set)
	rules.Validate(snap)

	// After validation the Eligible call must return false.
	if eng().Eligible(makeCtx(), []string{"r1"}, snap) {
		t.Error("CA-4: Eligible must return false for an Impossible rule set")
	}
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkRules_Eligible(b *testing.B) {
	set := rs("r1", snapshot.LogicAND,
		cond(snapshot.VectorGeoCountry, snapshot.OpIs, "BR"),
		cond(snapshot.VectorGeoCity, snapshot.OpContains, "paulo"),
		cond(snapshot.VectorClientUA, snapshot.OpContains, "android"),
		cond(snapshot.VectorSiteURL, snapshot.OpContains, "news.example.com"),
	)
	snap := makeSnap(set)
	e := eng()
	ctx := makeCtx()
	ids := []string{"r1"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.Eligible(ctx, ids, snap)
	}
}
