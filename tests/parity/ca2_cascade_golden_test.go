// Package parity contains the golden-test suite for the Go rewrite parity gate.
//
// CA-2: Cascata DA-3 — golden tests que travam a semântica legada bit a bit.
//
// Estes testes importam os pacotes internos e exercem o motor Go diretamente.
// Eles NÃO dependem de infra externa (Redis, Redpanda, Postgres, MaxMind).
//
// Critério de gate: todo teste aqui deve ser VERDE antes de qualquer cutover.
// Se a semântica mudar intencionalmente, exigir ADR do tech-lead-architect
// antes de relaxar qualquer caso.
package parity_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Golden file schema (CA-2)
// ---------------------------------------------------------------------------

type ca2GoldenInput struct {
	ZoneID      string            `json:"zone_id"`
	TenantID    string            `json:"tenant_id"`
	RequestTime string            `json:"request_time"`
	UserID      string            `json:"user_id"`
	Campaigns   []ca2CampaignSpec `json:"campaigns"`
	Banners     []ca2BannerSpec   `json:"banners"`
}

type ca2CampaignSpec struct {
	ID                   string  `json:"id"`
	TenantID             string  `json:"tenant_id"`
	Tier                 string  `json:"tier"`
	Priority             int32   `json:"priority"`
	Active               bool    `json:"active"`
	GoalImpressions      int64   `json:"goal_impressions"`
	DeliveredImpressions int64   `json:"delivered_impressions"`
	ECPMMinorUnits       int64   `json:"ecpm_minor_units"`
	EndAt                string  `json:"end_at"` // empty = no deadline
}

type ca2BannerSpec struct {
	ID         string `json:"id"`
	CampaignID string `json:"campaign_id"`
	Active     bool   `json:"active"`
	TenantID   string `json:"tenant_id"`
}

type ca2GoldenExpect struct {
	ServedTier string  `json:"served_tier"`
	CampaignID *string `json:"campaign_id"`
	BannerID   *string `json:"banner_id"`
	Blank      bool    `json:"blank"`
}

type ca2GoldenCase struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	CA          string          `json:"ca"`
	Sub         string          `json:"sub"`
	Input       ca2GoldenInput  `json:"input"`
	Expect      ca2GoldenExpect `json:"expect"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var goldenNow = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

func parseTierStr(s string) snapshot.Tier {
	switch s {
	case "OVERRIDE":
		return snapshot.TierOverride
	case "CONTRACT":
		return snapshot.TierContract
	case "REMNANT":
		return snapshot.TierRemnant
	default:
		return snapshot.TierUnspecified
	}
}

func buildSnapFromCA2(gc ca2GoldenCase) *snapshot.Snapshot {
	snap := snapshot.EmptySnapshot()

	tenantID := gc.Input.TenantID

	// Campaigns always live on zone "z1" (canonical test zone).
	// The requested zone (gc.Input.ZoneID) is only registered in the snapshot
	// if campaigns are present AND the requested zone matches the canonical zone.
	// This allows CA2-011 (unknown zone) to exercise the fail-closed path:
	// the zone "nonexistent-zone" is NOT registered, but campaigns on "z1" are.
	const canonicalZone = "z1"
	if len(gc.Input.Campaigns) > 0 || gc.Input.ZoneID == canonicalZone {
		snap.Zones[canonicalZone] = &snapshot.Zone{
			ID:       canonicalZone,
			TenantID: tenantID,
			Active:   true,
		}
	}

	for _, cs := range gc.Input.Campaigns {
		campTenantID := cs.TenantID
		if campTenantID == "" {
			campTenantID = tenantID
		}
		camp := &snapshot.Campaign{
			ID:                   cs.ID,
			TenantID:             campTenantID,
			Tier:                 parseTierStr(cs.Tier),
			Priority:             cs.Priority,
			Active:               cs.Active,
			GoalImpressions:      cs.GoalImpressions,
			DeliveredImpressions: cs.DeliveredImpressions,
			BannerIDs:            []string{"ban-" + cs.ID},
			ZoneIDs:              []string{canonicalZone},
			PricingModel:         snapshot.PricingCPM,
		}
		if cs.ECPMMinorUnits > 0 {
			camp.ECPM = &moneyv1.Money{
				AssetCode: "BRL",
				Amount:    cs.ECPMMinorUnits,
				Scale:     2,
			}
		}
		if cs.EndAt != "" {
			t, err := time.Parse(time.RFC3339, cs.EndAt)
			if err == nil {
				camp.EndAt = t
			}
		}
		snap.Campaigns[cs.ID] = camp
		snap.ZoneCampaigns[canonicalZone] = append(snap.ZoneCampaigns[canonicalZone], cs.ID)
	}

	for _, bs := range gc.Input.Banners {
		banTenantID := bs.TenantID
		if banTenantID == "" {
			banTenantID = tenantID
		}
		snap.Banners[bs.ID] = &snapshot.Banner{
			ID:         bs.ID,
			TenantID:   banTenantID,
			CampaignID: bs.CampaignID,
			Active:     bs.Active,
			ImageURL:   "https://cdn.example.com/" + bs.ID + ".jpg",
			ClickURL:   "https://click.example.com/" + bs.ID,
		}
	}

	return snap
}

func servedTierProtoFromStr(s string) commonv1.ServedTier {
	switch s {
	case "SERVED_TIER_OVERRIDE":
		return commonv1.ServedTier_SERVED_TIER_OVERRIDE
	case "SERVED_TIER_CONTRACT":
		return commonv1.ServedTier_SERVED_TIER_CONTRACT
	case "SERVED_TIER_REMNANT":
		return commonv1.ServedTier_SERVED_TIER_REMNANT
	case "SERVED_TIER_BLANK":
		return commonv1.ServedTier_SERVED_TIER_BLANK
	default:
		return commonv1.ServedTier_SERVED_TIER_UNSPECIFIED
	}
}

// ---------------------------------------------------------------------------
// CA-2 golden test runner
// ---------------------------------------------------------------------------

func TestCA2_CascadeGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("golden", "ca2_cascade.json"))
	if err != nil {
		t.Fatalf("cannot read golden file: %v", err)
	}

	var cases []ca2GoldenCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("cannot parse golden file: %v", err)
	}

	if len(cases) == 0 {
		t.Fatal("golden file has zero cases — gate requires at least one case")
	}

	eng := cascade.New(rules.New())

	for _, gc := range cases {
		gc := gc
		t.Run(gc.ID+"/"+gc.Sub, func(t *testing.T) {
			snap := buildSnapFromCA2(gc)

			reqTime := goldenNow
			if gc.Input.RequestTime != "" {
				if parsed, err := time.Parse(time.RFC3339, gc.Input.RequestTime); err == nil {
					reqTime = parsed
				}
			}

			req := cascade.Request{
				ZoneID:      gc.Input.ZoneID,
				TenantID:    gc.Input.TenantID,
				Rules:       &rules.Context{RequestTime: reqTime},
				UserID:      gc.Input.UserID,
				RequestTime: reqTime,
			}

			result := eng.Decide(req, snap)

			// Assert served tier.
			wantTier := servedTierProtoFromStr(gc.Expect.ServedTier)
			if result.ServedTier != wantTier {
				t.Errorf("[%s] served_tier: got %v, want %v", gc.ID, result.ServedTier, wantTier)
			}

			// Assert blank invariant: when blank, campaign and banner must be nil.
			if gc.Expect.Blank {
				if result.Campaign != nil {
					t.Errorf("[%s] expected nil Campaign on BLANK, got %v", gc.ID, result.Campaign.ID)
				}
				if result.Banner != nil {
					t.Errorf("[%s] expected nil Banner on BLANK, got %v", gc.ID, result.Banner.ID)
				}
			} else {
				if result.Campaign == nil {
					t.Errorf("[%s] expected non-nil Campaign (not BLANK)", gc.ID)
				}
				if result.Banner == nil {
					t.Errorf("[%s] expected non-nil Banner (not BLANK)", gc.ID)
				}
			}

			// Assert campaign_id when specified.
			if gc.Expect.CampaignID != nil {
				wantCampID := *gc.Expect.CampaignID
				gotCampID := ""
				if result.Campaign != nil {
					gotCampID = result.Campaign.ID
				}
				if gotCampID != wantCampID {
					t.Errorf("[%s] campaign_id: got %q, want %q", gc.ID, gotCampID, wantCampID)
				}
			}
		})
	}

	t.Logf("CA-2 golden suite: %d cases evaluated", len(cases))
}

// ---------------------------------------------------------------------------
// Adversarial cases (not in the golden JSON — coded directly for clarity)
// ---------------------------------------------------------------------------

// TestCA2_Override_Priority_TieBreak verifies that when two Override campaigns
// have the same priority, the one with the lower ID wins (stable sort).
func TestCA2_Override_Priority_TieBreak(t *testing.T) {
	eng := cascade.New(rules.New())

	ov1 := makePGoldenCampaign("ov-a", snapshot.TierOverride, 10)
	ov2 := makePGoldenCampaign("ov-b", snapshot.TierOverride, 10) // same priority
	ban1 := makePGoldenBanner("ban-ov-a", "ov-a")
	ban2 := makePGoldenBanner("ban-ov-b", "ov-b")

	snap := buildGoldenSnap([]*snapshot.Campaign{ov1, ov2}, []*snapshot.Banner{ban1, ban2})
	req := makeGoldenRequest()
	result := eng.Decide(req, snap)

	// With equal priority and ID-based stable sort: "ov-a" < "ov-b" → ov-a wins.
	if result.Campaign == nil || result.Campaign.ID != "ov-a" {
		campID := "<nil>"
		if result.Campaign != nil {
			campID = result.Campaign.ID
		}
		t.Errorf("tie-break by ID: expected ov-a, got %s", campID)
	}
}

// TestCA2_AllCampaignsCapped_FallsToBlank verifies that when the cascade Capper
// denies every candidate, the result degrades to BLANK (page does not break).
func TestCA2_AllCampaignsCapped_FallsToBlank(t *testing.T) {
	alwaysDeny := &denyAllCapper{}
	eng := cascade.New(rules.New(), cascade.WithCapper(alwaysDeny))

	rm := makePGoldenCampaign("rm1", snapshot.TierRemnant, 1)
	ban := makePGoldenBanner("ban-rm1", "rm1")

	snap := buildGoldenSnap([]*snapshot.Campaign{rm}, []*snapshot.Banner{ban})
	req := makeGoldenRequest()
	req.UserID = "user-x" // non-empty so Capper is called

	result := eng.Decide(req, snap)
	if result.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("all-capped: expected BLANK, got %v", result.ServedTier)
	}
}

// denyAllCapper implements cascade.Capper and always denies (for testing).
type denyAllCapper struct{}

func (denyAllCapper) Allowed(_ string, _ *snapshot.Campaign, _ *snapshot.Banner) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------
// Local fixture helpers (mirrors cascade_test helpers, but lives in parity pkg)
// ---------------------------------------------------------------------------

func makePGoldenCampaign(id string, tier snapshot.Tier, priority int32) *snapshot.Campaign {
	return &snapshot.Campaign{
		ID:           id,
		TenantID:     "t1",
		Tier:         tier,
		Priority:     priority,
		BannerIDs:    []string{"ban-" + id},
		ZoneIDs:      []string{"z1"},
		Active:       true,
		PricingModel: snapshot.PricingCPM,
	}
}

func makePGoldenBanner(id, campaignID string) *snapshot.Banner {
	return &snapshot.Banner{
		ID:         id,
		TenantID:   "t1",
		CampaignID: campaignID,
		ImageURL:   "https://cdn.example.com/" + id + ".jpg",
		ClickURL:   "https://click.example.com/" + id,
		Active:     true,
	}
}

func buildGoldenSnap(campaigns []*snapshot.Campaign, banners []*snapshot.Banner) *snapshot.Snapshot {
	s := snapshot.EmptySnapshot()
	s.Zones["z1"] = &snapshot.Zone{ID: "z1", TenantID: "t1", Active: true}
	for _, c := range campaigns {
		s.Campaigns[c.ID] = c
		for _, z := range c.ZoneIDs {
			s.ZoneCampaigns[z] = append(s.ZoneCampaigns[z], c.ID)
		}
	}
	for _, b := range banners {
		s.Banners[b.ID] = b
	}
	return s
}

func makeGoldenRequest() cascade.Request {
	return cascade.Request{
		ZoneID:      "z1",
		TenantID:    "t1",
		Rules:       &rules.Context{RequestTime: goldenNow},
		RequestTime: goldenNow,
	}
}
