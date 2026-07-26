package configload

import (
	"testing"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// decimalToMinor — no-float NUMERIC → minor units with ROUND_HALF_EVEN
// ---------------------------------------------------------------------------

func TestDecimalToMinor(t *testing.T) {
	cases := []struct {
		in    string
		scale int32
		want  int64
	}{
		{"12.50", 2, 1250},
		{"12", 2, 1200},
		{"0", 2, 0},
		{"", 2, 0},
		{"0.01", 2, 1},
		{"1.999", 2, 200},   // rounds up (9 > 5)
		{"9.005", 2, 900},   // exactly .5, last kept 0 even → down
		{"9.015", 2, 902},   // exactly .5, last kept 1 odd → up
		{"12.345", 2, 1234}, // .5 dropped, last kept 4 even → down
		{"-3.50", 2, -350},  // sign preserved
		{"12.5", 0, 12},     // half-even to even (2)
		{"13.5", 0, 14},     // half-even to even (4)
		{"0.000001", 6, 1},  // 6-decimal asset (USDC)
		{"1.5", 6, 1500000}, // pad to scale
	}
	for _, c := range cases {
		got, err := decimalToMinor(c.in, c.scale)
		if err != nil {
			t.Fatalf("decimalToMinor(%q, %d): unexpected error %v", c.in, c.scale, err)
		}
		if got != c.want {
			t.Errorf("decimalToMinor(%q, %d) = %d, want %d", c.in, c.scale, got, c.want)
		}
	}
}

func TestDecimalToMinor_BadInput(t *testing.T) {
	if _, err := decimalToMinor("1.2x3", 2); err == nil {
		t.Error("expected error for non-numeric fractional part")
	}
}

// ---------------------------------------------------------------------------
// enum / string mapping
// ---------------------------------------------------------------------------

func TestTierAndPricingMapping(t *testing.T) {
	if tierFromType("override") != snapshot.TierOverride ||
		tierFromType("contract") != snapshot.TierContract ||
		tierFromType("remnant") != snapshot.TierRemnant ||
		tierFromType("???") != snapshot.TierUnspecified {
		t.Error("tierFromType mapping wrong")
	}
	if pricingFromModel("cpm") != snapshot.PricingCPM ||
		pricingFromModel("cpc") != snapshot.PricingCPC ||
		pricingFromModel("cpa") != snapshot.PricingCPA ||
		pricingFromModel("tenancy") != snapshot.PricingTenancy {
		t.Error("pricingFromModel mapping wrong")
	}
}

func TestVectorOperatorMapping(t *testing.T) {
	if v, ok := vectorFromString("Geo - Country"); !ok || v != snapshot.VectorGeoCountry {
		t.Error("Geo - Country mapping wrong")
	}
	if _, ok := vectorFromString("Nonexistent"); ok {
		t.Error("unknown vector should not map")
	}
	if op, ok := operatorFromString("is not"); !ok || op != snapshot.OpIsNot {
		t.Error("is not mapping wrong")
	}
	if _, ok := operatorFromString("starts with"); ok {
		t.Error("unsupported operator must report not-ok (fail-closed)")
	}
}

// ---------------------------------------------------------------------------
// Assemble + cascade end-to-end (the mapping serves a real ad)
// ---------------------------------------------------------------------------

const tenantA = "11111111-1111-1111-1111-111111111111"

func demoRaw() RawConfig {
	return RawConfig{
		Zones: []ZoneRow{
			{ID: "10", TenantID: tenantA, SiteID: "1", Name: "z", Width: 300, Height: 250, Active: true},
		},
		Campaigns: []CampaignRow{
			{ID: "20", TenantID: tenantA, Type: "remnant", Priority: 5, PricingModel: "cpm",
				Currency: "BRL", RateMinor: 1250, Scale: 2, Active: true},
		},
		Banners: []BannerRow{
			{ID: "30", TenantID: tenantA, CampaignID: "20", CreativeType: "image",
				AssetURL: "https://cdn.example/x.png", DestURL: "https://adv.example/lp",
				Width: 300, Height: 250, Active: true},
		},
		CampaignZones: []CampaignZoneRow{{CampaignID: "20", ZoneID: "10"}},
	}
}

func decide(t *testing.T, snap *snapshot.Snapshot, zoneID, country string) cascade.Result {
	t.Helper()
	eng := cascade.New(rules.New())
	now := time.Now().UTC()
	return eng.Decide(cascade.Request{
		ZoneID:   zoneID,
		TenantID: tenantA,
		Rules: &rules.Context{
			Geo:         &commonv1.Geo{Country: country},
			RequestTime: now,
		},
		RequestTime: now,
	}, snap)
}

func TestAssemble_ServesRemnant(t *testing.T) {
	snap := Assemble(demoRaw(), "v1", time.Now().UTC())

	if len(snap.Zones) != 1 || len(snap.Campaigns) != 1 || len(snap.Banners) != 1 {
		t.Fatalf("snapshot index sizes wrong: %+v", snap)
	}
	camp := snap.Campaigns["20"]
	if camp.Tier != snapshot.TierRemnant {
		t.Errorf("tier = %v, want Remnant", camp.Tier)
	}
	if camp.ECPM.GetAmount() != 1250 || camp.ECPM.GetAssetCode() != "BRL" || camp.ECPM.GetScale() != 2 {
		t.Errorf("ECPM = %+v, want BRL/1250/2", camp.ECPM)
	}
	if got := snap.ZoneCampaigns["10"]; len(got) != 1 || got[0] != "20" {
		t.Errorf("ZoneCampaigns[10] = %v, want [20]", got)
	}

	res := decide(t, snap, "10", "BR")
	if res.Banner == nil || res.Banner.ID != "30" {
		t.Fatalf("expected banner 30 served, got %+v", res)
	}
	if res.Banner.ImageURL != "https://cdn.example/x.png" || res.Banner.ClickURL != "https://adv.example/lp" {
		t.Errorf("creative fields not mapped: %+v", res.Banner)
	}
}

func TestAssemble_DirectRuleTargeting(t *testing.T) {
	raw := demoRaw()
	// Banner 30 only eligible when Geo-Country IS BR.
	raw.Rules = []RuleRow{
		{OwnerType: "banner", OwnerID: "30", Vector: "Geo - Country",
			Operator: "is", Value: "BR", LogicalOp: "AND", Active: true},
	}
	snap := Assemble(raw, "v1", time.Now().UTC())

	if got := decide(t, snap, "10", "BR"); got.Banner == nil {
		t.Error("BR request should serve the banner")
	}
	if got := decide(t, snap, "10", "US"); got.Banner != nil {
		t.Errorf("US request should be blank, got %+v", got.Banner)
	}
}

func TestAssemble_OrGroupTargeting(t *testing.T) {
	raw := demoRaw()
	raw.Rules = []RuleRow{
		{OwnerType: "banner", OwnerID: "30", Vector: "Geo - Country",
			Operator: "is", Value: "BR", LogicalOp: "OR", Active: true},
		{OwnerType: "banner", OwnerID: "30", Vector: "Geo - Country",
			Operator: "is", Value: "US", LogicalOp: "OR", Active: true},
	}
	snap := Assemble(raw, "v1", time.Now().UTC())

	for _, country := range []string{"BR", "US"} {
		if got := decide(t, snap, "10", country); got.Banner == nil {
			t.Errorf("%s should match the OR group", country)
		}
	}
	if got := decide(t, snap, "10", "FR"); got.Banner != nil {
		t.Error("FR should not match the OR group")
	}
}

func TestAssemble_UnsupportedOperatorFailsClosed(t *testing.T) {
	raw := demoRaw()
	raw.Rules = []RuleRow{
		{OwnerType: "banner", OwnerID: "30", Vector: "Site - URL",
			Operator: "starts with", Value: "https://", LogicalOp: "AND", Active: true},
	}
	snap := Assemble(raw, "v1", time.Now().UTC())

	rs, ok := snap.RuleSets["b:30:and"]
	if !ok || !rs.Impossible {
		t.Fatalf("unsupported operator must mark the set Impossible (fail-closed): %+v", rs)
	}
	if got := decide(t, snap, "10", "BR"); got.Banner != nil {
		t.Error("banner with an unevaluable rule must be silenced")
	}
}

func TestAssemble_CampaignRulesApplyToAllBanners(t *testing.T) {
	raw := demoRaw()
	raw.Banners = append(raw.Banners, BannerRow{
		ID: "31", TenantID: tenantA, CampaignID: "20", CreativeType: "image",
		AssetURL: "https://cdn.example/y.png", DestURL: "https://adv.example/lp2",
		Width: 300, Height: 250, Active: true,
	})
	// Campaign-level rule: only serve in BR. Should gate BOTH banners.
	raw.Rules = []RuleRow{
		{OwnerType: "campaign", OwnerID: "20", Vector: "Geo - Country",
			Operator: "is", Value: "BR", LogicalOp: "AND", Active: true},
	}
	snap := Assemble(raw, "v1", time.Now().UTC())

	for _, bid := range []string{"30", "31"} {
		ban := snap.Banners[bid]
		found := false
		for _, rsID := range ban.RuleSetIDs {
			if rsID == "c:20:and" {
				found = true
			}
		}
		if !found {
			t.Errorf("banner %s should reference campaign rule set c:20:and; has %v", bid, ban.RuleSetIDs)
		}
	}
	if got := decide(t, snap, "10", "US"); got.Banner != nil {
		t.Error("US blocked by campaign rule")
	}
	if got := decide(t, snap, "10", "BR"); got.Banner == nil {
		t.Error("BR allowed by campaign rule")
	}
}

func TestAssemble_Caps(t *testing.T) {
	raw := demoRaw()
	raw.Caps = []CapRow{
		{OwnerType: "campaign", OwnerID: "20", Scope: "campaign_total", LimitCount: 100, Active: true},
		{OwnerType: "campaign", OwnerID: "20", Scope: "clock", LimitCount: 5,
			ResetInterval: 3600, ResetValid: true, Active: true},
		{OwnerType: "banner", OwnerID: "30", Scope: "session", LimitCount: 3, Active: true},
		{OwnerType: "banner", OwnerID: "30", Scope: "campaign_total", LimitCount: 7, Active: false}, // inactive: ignored
	}
	snap := Assemble(raw, "v1", time.Now().UTC())
	camp := snap.Campaigns["20"]
	if camp.CapTotal != 100 || camp.CapClock != 5 || camp.CapClockWindowSec != 3600 {
		t.Errorf("campaign caps wrong: %+v", camp)
	}
	ban := snap.Banners["30"]
	if ban.CapSession != 3 {
		t.Errorf("banner session cap = %d, want 3", ban.CapSession)
	}
	if ban.CapTotal != 0 {
		t.Errorf("inactive cap should be ignored, got %d", ban.CapTotal)
	}
}

func TestAssemble_SiteVariableKeyValue(t *testing.T) {
	raw := demoRaw()
	raw.Rules = []RuleRow{
		{OwnerType: "banner", OwnerID: "30", Vector: "Site - Variable",
			Operator: "is", Value: "gender=male", LogicalOp: "AND", Active: true},
	}
	snap := Assemble(raw, "v1", time.Now().UTC())
	rs := snap.RuleSets["b:30:and"]
	if len(rs.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(rs.Conditions))
	}
	c := rs.Conditions[0]
	if c.SiteVarKey != "gender" || c.Value != "male" {
		t.Errorf("site var parse wrong: key=%q value=%q", c.SiteVarKey, c.Value)
	}
}

func TestAssemble_HTMLCreativeFromBlob(t *testing.T) {
	raw := demoRaw()
	raw.Banners = []BannerRow{
		{ID: "30", TenantID: tenantA, CampaignID: "20", CreativeType: "html5",
			AssetBlob: "<div>ad</div>", DestURL: "https://adv.example/lp",
			Width: 300, Height: 250, Active: true},
	}
	snap := Assemble(raw, "v1", time.Now().UTC())
	if snap.Banners["30"].HTML != "<div>ad</div>" {
		t.Errorf("html5 blob not mapped to HTML: %q", snap.Banners["30"].HTML)
	}
}

// ---------------------------------------------------------------------------
// Delivered counts (DA-4 pacing input; perfil BETA / ADR-0005) — see
// DeliveredRow's doc.
// ---------------------------------------------------------------------------

// TestAssemble_DeliveredCounts_MatchedByCampaignID proves the exact defect
// the coordinator flagged: before this test existed, NOTHING in production
// ever set snapshot.Campaign.Delivered*, so DA-4's computeDeficit always saw
// a permanent 1.0 deficit. This drives Assemble (the real production
// assembly function, not a reimplementation) with a populated raw.Delivered
// and asserts the counts land on the right campaign — and stay at zero for
// a campaign absent from the stats result (the documented degrade path).
func TestAssemble_DeliveredCounts_MatchedByCampaignID(t *testing.T) {
	raw := demoRaw()
	// A second campaign that will have NO matching DeliveredRow — proves
	// unmatched campaigns are left at their zero value, not accidentally
	// populated from another campaign's row (no cross-campaign leakage).
	raw.Campaigns = append(raw.Campaigns, CampaignRow{
		ID: "21", TenantID: tenantA, Type: "remnant", Priority: 5, PricingModel: "cpm",
		Currency: "BRL", RateMinor: 1250, Scale: 2, Active: true,
	})
	raw.Delivered = []DeliveredRow{
		{CampaignID: "20", Impressions: 4200, Clicks: 137, Conversions: 9},
	}

	snap := Assemble(raw, "v1", time.Now().UTC())

	camp20 := snap.Campaigns["20"]
	if camp20.DeliveredImpressions != 4200 || camp20.DeliveredClicks != 137 || camp20.DeliveredConversions != 9 {
		t.Fatalf("campaign 20 delivered counts wrong: impressions=%d clicks=%d conversions=%d, want 4200/137/9",
			camp20.DeliveredImpressions, camp20.DeliveredClicks, camp20.DeliveredConversions)
	}

	camp21 := snap.Campaigns["21"]
	if camp21.DeliveredImpressions != 0 || camp21.DeliveredClicks != 0 || camp21.DeliveredConversions != 0 {
		t.Fatalf("campaign 21 (no matching DeliveredRow) should stay at zero, got impressions=%d clicks=%d conversions=%d",
			camp21.DeliveredImpressions, camp21.DeliveredClicks, camp21.DeliveredConversions)
	}
}

// TestAssemble_DeliveredCounts_EmptyDegradesToZero proves the degrade path:
// raw.Delivered == nil (the stats store unavailable — see loadDelivered's
// doc) must NOT panic and must leave every campaign's Delivered* at zero,
// bit-identical to this package's behavior before DeliveredRow existed.
func TestAssemble_DeliveredCounts_EmptyDegradesToZero(t *testing.T) {
	raw := demoRaw()
	raw.Delivered = nil // explicit: the degrade-path value

	snap := Assemble(raw, "v1", time.Now().UTC())

	camp := snap.Campaigns["20"]
	if camp.DeliveredImpressions != 0 || camp.DeliveredClicks != 0 || camp.DeliveredConversions != 0 {
		t.Fatalf("expected zero delivered counts with raw.Delivered=nil, got impressions=%d clicks=%d conversions=%d",
			camp.DeliveredImpressions, camp.DeliveredClicks, camp.DeliveredConversions)
	}
}
