package cascade_test

import (
	"testing"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

var (
	now     = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	forever = time.Time{} // zero = no deadline
)

func makeBanner(id, campaignID string) *snapshot.Banner {
	return &snapshot.Banner{
		ID:         id,
		TenantID:   "t1",
		CampaignID: campaignID,
		ImageURL:   "https://cdn.example.com/" + id + ".jpg",
		ClickURL:   "https://click.example.com/" + id,
		Active:     true,
	}
}

func makeCampaign(id string, tier snapshot.Tier, priority int32) *snapshot.Campaign {
	return &snapshot.Campaign{
		ID:         id,
		TenantID:   "t1",
		Tier:       tier,
		Priority:   priority,
		StartAt:    forever,
		EndAt:      forever,
		BannerIDs:  []string{"ban-" + id},
		ZoneIDs:    []string{"z1"},
		Active:     true,
		PricingModel: snapshot.PricingCPM,
	}
}

func makeSnap(campaigns []*snapshot.Campaign, banners []*snapshot.Banner) *snapshot.Snapshot {
	s := snapshot.EmptySnapshot()
	for _, c := range campaigns {
		s.Campaigns[c.ID] = c
	}
	for _, b := range banners {
		s.Banners[b.ID] = b
	}
	// Wire ZoneCampaigns index and populate Zones so the cascade's zone-based
	// tenant derivation (CA-1 fail-closed check) can resolve correctly.
	// All test campaigns use zone "z1" and tenant "t1".
	s.Zones["z1"] = &snapshot.Zone{
		ID:       "z1",
		TenantID: "t1",
		Active:   true,
	}
	for _, c := range campaigns {
		for _, z := range c.ZoneIDs {
			s.ZoneCampaigns[z] = append(s.ZoneCampaigns[z], c.ID)
			// Ensure each zone in the campaigns has an entry in Zones with the
			// correct tenant so the cascade doesn't fail-closed on every test.
			if _, ok := s.Zones[z]; !ok {
				s.Zones[z] = &snapshot.Zone{
					ID:       z,
					TenantID: c.TenantID,
					Active:   true,
				}
			}
		}
	}
	return s
}

func defaultRequest() cascade.Request {
	return cascade.Request{
		ZoneID:      "z1",
		TenantID:    "t1",
		Rules:       &rules.Context{RequestTime: now},
		RequestTime: now,
	}
}

func newEngine() *cascade.Engine {
	return cascade.New(rules.New())
}

// ---------------------------------------------------------------------------
// Golden tests — CA-2: cascade semantics
// ---------------------------------------------------------------------------

// Test: Override always wins, even when Contract and Remnant are present.
func TestCascade_OverrideWins(t *testing.T) {
	override := makeCampaign("ov1", snapshot.TierOverride, 10)
	contract := makeCampaign("ct1", snapshot.TierContract, 5)
	remnant  := makeCampaign("rm1", snapshot.TierRemnant, 1)

	banOv  := makeBanner("ban-ov1", "ov1")
	banCt  := makeBanner("ban-ct1", "ct1")
	banRem := makeBanner("ban-rm1", "rm1")

	snap := makeSnap(
		[]*snapshot.Campaign{override, contract, remnant},
		[]*snapshot.Banner{banOv, banCt, banRem},
	)

	result := newEngine().Decide(defaultRequest(), snap)

	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_OVERRIDE {
		t.Errorf("expected OVERRIDE, got %v", result.ServedTier)
	}
	if result.Campaign.ID != "ov1" {
		t.Errorf("expected campaign ov1, got %v", result.Campaign.ID)
	}
}

// Test: Contract wins over Remnant when Override is absent.
func TestCascade_ContractBeatsRemnant(t *testing.T) {
	contract := makeCampaign("ct1", snapshot.TierContract, 5)
	remnant  := makeCampaign("rm1", snapshot.TierRemnant, 1)

	banCt  := makeBanner("ban-ct1", "ct1")
	banRem := makeBanner("ban-rm1", "rm1")

	snap := makeSnap(
		[]*snapshot.Campaign{contract, remnant},
		[]*snapshot.Banner{banCt, banRem},
	)

	result := newEngine().Decide(defaultRequest(), snap)

	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_CONTRACT {
		t.Errorf("expected CONTRACT, got %v", result.ServedTier)
	}
	if result.Campaign.ID != "ct1" {
		t.Errorf("expected campaign ct1, got %v", result.Campaign.ID)
	}
}

// Test: Contract with highest pacing deficit is selected first (DA-4).
func TestCascade_ContractDeficitOrdering(t *testing.T) {
	// ct-low delivered 90% of goal → deficit 10%.
	ctLow := makeCampaign("ct-low", snapshot.TierContract, 5)
	ctLow.GoalImpressions = 1000
	ctLow.DeliveredImpressions = 900

	// ct-high delivered 10% of goal → deficit 90%.
	ctHigh := makeCampaign("ct-high", snapshot.TierContract, 5)
	ctHigh.GoalImpressions = 1000
	ctHigh.DeliveredImpressions = 100

	banLow  := makeBanner("ban-ct-low", "ct-low")
	banHigh := makeBanner("ban-ct-high", "ct-high")

	snap := makeSnap(
		[]*snapshot.Campaign{ctLow, ctHigh},
		[]*snapshot.Banner{banLow, banHigh},
	)

	result := newEngine().Decide(defaultRequest(), snap)

	if result.Campaign.ID != "ct-high" {
		t.Errorf("expected ct-high (highest deficit) to win, got %v", result.Campaign.ID)
	}
}

// Test: Remnant fills when no Override/Contract is available.
func TestCascade_RemnantFills(t *testing.T) {
	remnant := makeCampaign("rm1", snapshot.TierRemnant, 1)
	ban     := makeBanner("ban-rm1", "rm1")

	snap := makeSnap(
		[]*snapshot.Campaign{remnant},
		[]*snapshot.Banner{ban},
	)

	result := newEngine().Decide(defaultRequest(), snap)

	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_REMNANT {
		t.Errorf("expected REMNANT, got %v", result.ServedTier)
	}
}

// Test: Remnant is ordered by eCPM descending (same asset).
func TestCascade_RemnantECPMOrdering(t *testing.T) {
	rmLow := makeCampaign("rm-low", snapshot.TierRemnant, 1)
	rmLow.ECPM = &moneyv1.Money{AssetCode: "BRL", Amount: 100, Scale: 2} // R$1.00

	rmHigh := makeCampaign("rm-high", snapshot.TierRemnant, 1)
	rmHigh.ECPM = &moneyv1.Money{AssetCode: "BRL", Amount: 500, Scale: 2} // R$5.00

	banLow  := makeBanner("ban-rm-low", "rm-low")
	banHigh := makeBanner("ban-rm-high", "rm-high")

	snap := makeSnap(
		[]*snapshot.Campaign{rmLow, rmHigh},
		[]*snapshot.Banner{banLow, banHigh},
	)

	result := newEngine().Decide(defaultRequest(), snap)

	if result.Campaign.ID != "rm-high" {
		t.Errorf("expected rm-high (higher eCPM) to win, got %v", result.Campaign.ID)
	}
}

// Test (CA-2): No eligible candidate in any tier → BLANK impression.
func TestCascade_BlankWhenNoEligible(t *testing.T) {
	snap := makeSnap(nil, nil)

	result := newEngine().Decide(defaultRequest(), snap)

	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("expected BLANK, got %v", result.ServedTier)
	}
	if result.Campaign != nil {
		t.Errorf("expected nil Campaign on BLANK, got %v", result.Campaign)
	}
	if result.Banner != nil {
		t.Errorf("expected nil Banner on BLANK, got %v", result.Banner)
	}
}

// Test: Inactive campaign is skipped → BLANK.
func TestCascade_InactiveCampaignSkipped(t *testing.T) {
	camp := makeCampaign("c1", snapshot.TierRemnant, 1)
	camp.Active = false
	ban := makeBanner("ban-c1", "c1")

	snap := makeSnap([]*snapshot.Campaign{camp}, []*snapshot.Banner{ban})

	result := newEngine().Decide(defaultRequest(), snap)

	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("expected BLANK for inactive campaign, got %v", result.ServedTier)
	}
}

// Test: Campaign outside its flight dates is skipped.
func TestCascade_ExpiredCampaignSkipped(t *testing.T) {
	camp := makeCampaign("c1", snapshot.TierRemnant, 1)
	camp.EndAt = now.Add(-1 * time.Hour) // already ended
	ban := makeBanner("ban-c1", "c1")

	snap := makeSnap([]*snapshot.Campaign{camp}, []*snapshot.Banner{ban})

	result := newEngine().Decide(defaultRequest(), snap)

	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("expected BLANK for expired campaign, got %v", result.ServedTier)
	}
}

// Test: Tenant isolation — campaign from different tenant is invisible.
func TestCascade_TenantIsolation(t *testing.T) {
	camp := makeCampaign("c1", snapshot.TierRemnant, 1)
	camp.TenantID = "other-tenant"
	ban := makeBanner("ban-c1", "c1")

	snap := makeSnap([]*snapshot.Campaign{camp}, []*snapshot.Banner{ban})

	req := defaultRequest()
	req.TenantID = "t1" // our tenant

	result := newEngine().Decide(req, snap)

	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("expected BLANK (tenant mismatch), got %v", result.ServedTier)
	}
}

// Test: Inactive banner is skipped; campaign with only inactive banners → BLANK.
func TestCascade_InactiveBannerSkipped(t *testing.T) {
	camp := makeCampaign("c1", snapshot.TierRemnant, 1)
	ban := makeBanner("ban-c1", "c1")
	ban.Active = false

	snap := makeSnap([]*snapshot.Campaign{camp}, []*snapshot.Banner{ban})

	result := newEngine().Decide(defaultRequest(), snap)

	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("expected BLANK (inactive banner), got %v", result.ServedTier)
	}
}

// Test: Decision log candidates are populated.
func TestCascade_CandidatesPopulated(t *testing.T) {
	rm1 := makeCampaign("rm1", snapshot.TierRemnant, 1)
	rm2 := makeCampaign("rm2", snapshot.TierRemnant, 1)
	ban1 := makeBanner("ban-rm1", "rm1")
	ban2 := makeBanner("ban-rm2", "rm2")
	rm2.ZoneIDs = []string{"z1"}
	rm2.BannerIDs = []string{"ban-rm2"}

	snap := makeSnap(
		[]*snapshot.Campaign{rm1, rm2},
		[]*snapshot.Banner{ban1, ban2},
	)

	result := newEngine().Decide(defaultRequest(), snap)

	if len(result.Candidates) < 1 {
		t.Errorf("expected at least 1 candidate in log, got %d", len(result.Candidates))
	}
}

// ---------------------------------------------------------------------------
// Security (CA-1): tenant_id derived server-side; zone must exist in snapshot
// ---------------------------------------------------------------------------

// Test: zone not in snapshot → BLANK (fail-closed, CA-1).
func TestCascade_UnknownZone_Blank(t *testing.T) {
	rem := makeCampaign("rm1", snapshot.TierRemnant, 1)
	ban := makeBanner("ban-rm1", "rm1")
	snap := makeSnap([]*snapshot.Campaign{rem}, []*snapshot.Banner{ban})

	req := defaultRequest()
	req.ZoneID = "nonexistent-zone" // not in snapshot
	req.TenantID = "t1"

	result := newEngine().Decide(req, snap)
	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("unknown zone: expected BLANK (fail-closed), got %v", result.ServedTier)
	}
}

// Test: zone exists but TenantID mismatch → BLANK (CA-1 defence-in-depth).
func TestCascade_ZoneTenantMismatch_Blank(t *testing.T) {
	rem := makeCampaign("rm1", snapshot.TierRemnant, 1)
	ban := makeBanner("ban-rm1", "rm1")
	snap := makeSnap([]*snapshot.Campaign{rem}, []*snapshot.Banner{ban})

	// Override zone's tenant to differ from request.TenantID.
	snap.Zones["z1"] = &snapshot.Zone{
		ID:       "z1",
		TenantID: "correct-tenant",
		Active:   true,
	}

	req := defaultRequest()
	req.TenantID = "wrong-tenant" // mismatch with zone.TenantID

	result := newEngine().Decide(req, snap)
	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("tenant mismatch: expected BLANK (fail-closed CA-1), got %v", result.ServedTier)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks — goal: near-zero allocations in hot path
// ---------------------------------------------------------------------------

func BenchmarkCascade_Decide(b *testing.B) {
	override := makeCampaign("ov1", snapshot.TierOverride, 10)
	contract := makeCampaign("ct1", snapshot.TierContract, 5)
	contract.GoalImpressions = 10000
	contract.DeliveredImpressions = 5000
	remnant := makeCampaign("rm1", snapshot.TierRemnant, 1)
	remnant.ECPM = &moneyv1.Money{AssetCode: "BRL", Amount: 200, Scale: 2}

	banOv  := makeBanner("ban-ov1", "ov1")
	banCt  := makeBanner("ban-ct1", "ct1")
	banRem := makeBanner("ban-rm1", "rm1")

	snap := makeSnap(
		[]*snapshot.Campaign{override, contract, remnant},
		[]*snapshot.Banner{banOv, banCt, banRem},
	)

	engine := newEngine()
	req := defaultRequest()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = engine.Decide(req, snap)
	}
}
