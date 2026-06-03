// Tests for the shadow harness — exercisable without infra.
package shadow_test

import (
	"testing"
	"time"

	"github.com/hojex/adserver/tests/parity/shadow"
)

// TestCompareDecisions_Agreement verifies that identical outputs produce no divergences.
func TestCompareDecisions_Agreement(t *testing.T) {
	legacy := shadow.DecisionOutput{
		ServedTier: "REMNANT",
		CampaignID: "camp-1",
		BannerID:   "ban-1",
		IsBlank:    false,
		MotorName:  "revive",
	}
	go_ := shadow.DecisionOutput{
		ServedTier: "REMNANT",
		CampaignID: "camp-1",
		BannerID:   "ban-1",
		IsBlank:    false,
		MotorName:  "go",
	}

	divs := shadow.CompareDecisions(legacy, go_)
	if len(divs) != 0 {
		t.Errorf("expected 0 divergences for identical decisions, got %d: %+v", len(divs), divs)
	}
}

// TestCompareDecisions_TierMismatch detects tier divergence as billing-critical.
func TestCompareDecisions_TierMismatch(t *testing.T) {
	legacy := shadow.DecisionOutput{ServedTier: "CONTRACT", CampaignID: "camp-1", IsBlank: false, MotorName: "revive"}
	go_    := shadow.DecisionOutput{ServedTier: "REMNANT",  CampaignID: "camp-1", IsBlank: false, MotorName: "go"}

	divs := shadow.CompareDecisions(legacy, go_)
	if len(divs) == 0 {
		t.Error("expected tier_mismatch divergence, got none")
	}
	found := false
	for _, d := range divs {
		if d.Kind == shadow.DivergenceTierMismatch {
			found = true
		}
	}
	if !found {
		t.Errorf("expected DivergenceTierMismatch, got %+v", divs)
	}
}

// TestCompareDecisions_CampaignMismatch detects campaign divergence (billing-critical).
func TestCompareDecisions_CampaignMismatch(t *testing.T) {
	legacy := shadow.DecisionOutput{ServedTier: "REMNANT", CampaignID: "camp-A", IsBlank: false, MotorName: "revive"}
	go_    := shadow.DecisionOutput{ServedTier: "REMNANT", CampaignID: "camp-B", IsBlank: false, MotorName: "go"}

	divs := shadow.CompareDecisions(legacy, go_)
	found := false
	for _, d := range divs {
		if d.Kind == shadow.DivergenceCampaignMismatch {
			found = true
		}
	}
	if !found {
		t.Errorf("expected DivergenceCampaignMismatch, got %+v", divs)
	}
}

// TestCompareDecisions_BlankMismatch is billing-critical: one serves, other is BLANK.
func TestCompareDecisions_BlankMismatch(t *testing.T) {
	legacy := shadow.DecisionOutput{ServedTier: "REMNANT", CampaignID: "camp-1", IsBlank: false, MotorName: "revive"}
	go_    := shadow.DecisionOutput{ServedTier: "BLANK", IsBlank: true, MotorName: "go"}

	divs := shadow.CompareDecisions(legacy, go_)
	found := false
	for _, d := range divs {
		if d.Kind == shadow.DivergenceBlankMismatch {
			found = true
		}
	}
	if !found {
		t.Errorf("expected DivergenceBlankMismatch, got %+v", divs)
	}
}

// TestCompareDecisions_BannerOnlyMismatch is informational (not billing-critical alone).
func TestCompareDecisions_BannerOnlyMismatch(t *testing.T) {
	legacy := shadow.DecisionOutput{ServedTier: "REMNANT", CampaignID: "camp-1", BannerID: "ban-A", IsBlank: false, MotorName: "revive"}
	go_    := shadow.DecisionOutput{ServedTier: "REMNANT", CampaignID: "camp-1", BannerID: "ban-B", IsBlank: false, MotorName: "go"}

	divs := shadow.CompareDecisions(legacy, go_)
	onlyBannerDiff := true
	for _, d := range divs {
		if d.Kind != shadow.DivergenceBannerMismatch {
			onlyBannerDiff = false
		}
	}
	if !onlyBannerDiff {
		t.Errorf("expected only banner_mismatch, got %+v", divs)
	}
}

// TestDivergenceCollector_Rate verifies the billing-critical divergence rate calculation.
func TestDivergenceCollector_Rate(t *testing.T) {
	col := shadow.NewDivergenceCollector()

	// Simulate 1000 decisions: 999 agree, 1 diverges.
	for i := 0; i < 999; i++ {
		col.Record(nil)
	}
	// One billing-critical divergence (campaign mismatch).
	col.Record([]shadow.Divergence{
		{Kind: shadow.DivergenceCampaignMismatch, DetectedAt: time.Now()},
	})

	rate := col.DivergenceRate()
	// 1/1000 = 0.001 which is exactly at the tolerance boundary.
	// The gate uses >, so 0.001 is NOT blocked (it's the boundary).
	if rate != 0.001 {
		t.Errorf("expected divergence rate 0.001, got %f", rate)
	}
}

// TestCutoverGate_BlocksOnInsufficientSample verifies the gate refuses approval
// when the sample size is below the minimum.
func TestCutoverGate_BlocksOnInsufficientSample(t *testing.T) {
	col := shadow.NewDivergenceCollector()
	// Only 10 decisions — far below 100,000 minimum.
	for i := 0; i < 10; i++ {
		col.Record(nil)
	}

	gate := shadow.NewCutoverGate(col)
	// Override StartedAt to simulate 72h elapsed (well past the 48h minimum).
	gate.StartedAt = time.Now().Add(-72 * time.Hour)

	decision := gate.Evaluate()
	if decision.Approved {
		t.Error("gate approved with insufficient sample — MUST be blocked")
	}
	if len(decision.Reasons) == 0 {
		t.Error("gate must provide reasons for rejection, got none")
	}
	t.Logf("rejection reasons: %v", decision.Reasons)
}

// TestCutoverGate_BlocksOnDivergenceAboveTolerance verifies the gate blocks
// when divergence rate exceeds the declared tolerance.
func TestCutoverGate_BlocksOnDivergenceAboveTolerance(t *testing.T) {
	col := shadow.NewDivergenceCollector()
	// 100,000 decisions with 200 divergences = 0.2% > 0.1% threshold.
	for i := 0; i < 99_800; i++ {
		col.Record(nil)
	}
	for i := 0; i < 200; i++ {
		col.Record([]shadow.Divergence{
			{Kind: shadow.DivergenceTierMismatch, DetectedAt: time.Now()},
		})
	}

	gate := shadow.NewCutoverGate(col)
	gate.StartedAt = time.Now().Add(-72 * time.Hour) // 72h elapsed

	decision := gate.Evaluate()
	if decision.Approved {
		t.Error("gate approved with divergence above tolerance (0.2% > 0.1%) — MUST be blocked")
	}
	t.Logf("rejection reasons: %v", decision.Reasons)
}

// TestCutoverGate_CriteriaDocumented verifies the declared tolerance values
// are within expected ranges (catches accidental changes).
func TestCutoverGate_CriteriaDocumented(t *testing.T) {
	c := shadow.CutoverCriteria

	if c.MaxDecisionDivergenceRate <= 0 || c.MaxDecisionDivergenceRate >= 0.01 {
		t.Errorf("MaxDecisionDivergenceRate out of expected range [0, 0.01]: %f", c.MaxDecisionDivergenceRate)
	}
	if c.MaxBillingDivergencePct <= 0 || c.MaxBillingDivergencePct >= 0.05 {
		t.Errorf("MaxBillingDivergencePct out of expected range [0, 0.05]: %f", c.MaxBillingDivergencePct)
	}
	if c.MinShadowHours < 24 {
		t.Errorf("MinShadowHours too low: %d (minimum 24)", c.MinShadowHours)
	}
	if c.MinShadowDecisions < 10_000 {
		t.Errorf("MinShadowDecisions too low: %d (minimum 10,000)", c.MinShadowDecisions)
	}
}
