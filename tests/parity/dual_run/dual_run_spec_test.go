// Tests for the dual-run spec — exercisable without infra.
package dual_run_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/hojex/adserver/tests/parity/dual_run"
)

var testHour = time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC)

func makeRecord(tenantID, campaignID, assetCode string, minorUnits int64, source string) dual_run.BillingRecord {
	return dual_run.BillingRecord{
		HourBucket:   testHour,
		TenantID:     tenantID,
		CampaignID:   campaignID,
		PricingModel: "CPM",
		Amount: dual_run.BillingAmount{
			MinorUnits: fmt.Sprintf("%d", minorUnits),
			AssetCode:  assetCode,
			Scale:      2,
		},
		Source: source,
	}
}

// TestReconcileHour_Agreement verifies that identical billing records produce
// zero divergence and approve cutover.
func TestReconcileHour_Agreement(t *testing.T) {
	goRecs := []dual_run.BillingRecord{
		makeRecord("t1", "camp-1", "BRL", 10000, "go"),
	}
	revRecs := []dual_run.BillingRecord{
		makeRecord("t1", "camp-1", "BRL", 10000, "revive"),
	}

	results, err := dual_run.ReconcileHour(testHour, goRecs, revRecs, dual_run.BillingTolerancePct)
	if err != nil {
		t.Fatalf("ReconcileHour error: %v", err)
	}

	for _, r := range results {
		if r.BlocksCutover {
			t.Errorf("identical billing: expected no blocking, got BlocksCutover=true for %s/%s (div=%.4f%%)",
				r.TenantID, r.CampaignID, r.DivergencePct*100)
		}
	}
}

// TestReconcileHour_WithinTolerance verifies that a 0.4% divergence (below 0.5% threshold)
// does NOT block cutover.
func TestReconcileHour_WithinTolerance(t *testing.T) {
	// Go: 10000, Revive: 10040 → diff=40 → 40/10040 ≈ 0.398% < 0.5%
	goRecs := []dual_run.BillingRecord{makeRecord("t1", "camp-1", "BRL", 10000, "go")}
	revRecs := []dual_run.BillingRecord{makeRecord("t1", "camp-1", "BRL", 10040, "revive")}

	results, err := dual_run.ReconcileHour(testHour, goRecs, revRecs, dual_run.BillingTolerancePct)
	if err != nil {
		t.Fatalf("ReconcileHour error: %v", err)
	}
	for _, r := range results {
		if r.BlocksCutover {
			t.Errorf("0.4%% divergence within tolerance: should NOT block, got BlocksCutover=true (div=%.4f%%)",
				r.DivergencePct*100)
		}
	}
}

// TestReconcileHour_AboveTolerance verifies that a 1% divergence (above 0.5% threshold)
// BLOCKS cutover.
func TestReconcileHour_AboveTolerance(t *testing.T) {
	// Go: 10000, Revive: 10100 → diff=100 → 100/10100 ≈ 0.99% > 0.5%
	goRecs := []dual_run.BillingRecord{makeRecord("t1", "camp-1", "BRL", 10000, "go")}
	revRecs := []dual_run.BillingRecord{makeRecord("t1", "camp-1", "BRL", 10100, "revive")}

	results, err := dual_run.ReconcileHour(testHour, goRecs, revRecs, dual_run.BillingTolerancePct)
	if err != nil {
		t.Fatalf("ReconcileHour error: %v", err)
	}
	blocked := false
	for _, r := range results {
		if r.BlocksCutover {
			blocked = true
			t.Logf("correctly blocked: %s/%s div=%.4f%% (Go=%s Rev=%s)",
				r.TenantID, r.CampaignID, r.DivergencePct*100, r.GoMinorUnits, r.ReviveMinorUnits)
		}
	}
	if !blocked {
		t.Error("1%% divergence above tolerance: should BLOCK cutover, but it did not")
	}
}

// TestReconcileHour_GoZeroReviveNonZero verifies that a Go billing of zero
// against a non-zero Revive amount blocks cutover (100% divergence).
func TestReconcileHour_GoZeroReviveNonZero(t *testing.T) {
	goRecs := []dual_run.BillingRecord{}
	revRecs := []dual_run.BillingRecord{makeRecord("t1", "camp-1", "BRL", 5000, "revive")}

	results, err := dual_run.ReconcileHour(testHour, goRecs, revRecs, dual_run.BillingTolerancePct)
	if err != nil {
		t.Fatalf("ReconcileHour error: %v", err)
	}
	blocked := false
	for _, r := range results {
		if r.BlocksCutover {
			blocked = true
		}
	}
	if !blocked {
		t.Error("Go=0, Revive=5000: should BLOCK cutover (100% divergence)")
	}
}

// TestBuildReport_AllGreen verifies that all-within-tolerance hours approve cutover.
func TestBuildReport_AllGreen(t *testing.T) {
	now := time.Now()
	results := [][]dual_run.ReconcileResult{
		{
			{HourBucket: now, TenantID: "t1", CampaignID: "c1", DivergencePct: 0.003, BlocksCutover: false},
		},
		{
			{HourBucket: now.Add(time.Hour), TenantID: "t1", CampaignID: "c1", DivergencePct: 0.001, BlocksCutover: false},
		},
	}

	report := dual_run.BuildReport(results, now, now.Add(2*time.Hour))
	if !report.CanCutover {
		t.Errorf("all-green report: expected CanCutover=true, got false. Reasons: %v", report.BlockingReasons)
	}
	if report.HoursBlocking != 0 {
		t.Errorf("expected 0 blocking hours, got %d", report.HoursBlocking)
	}
}

// TestBuildReport_OneBlockingHour verifies that a single blocking hour denies cutover.
func TestBuildReport_OneBlockingHour(t *testing.T) {
	now := time.Now()
	results := [][]dual_run.ReconcileResult{
		{
			{HourBucket: now, TenantID: "t1", CampaignID: "c1", DivergencePct: 0.001, BlocksCutover: false},
		},
		{
			{HourBucket: now.Add(time.Hour), TenantID: "t1", CampaignID: "c2",
				DivergencePct: 0.012, BlocksCutover: true,
				GoMinorUnits: "10000", ReviveMinorUnits: "11200", AssetCode: "BRL"},
		},
	}

	report := dual_run.BuildReport(results, now, now.Add(2*time.Hour))
	if report.CanCutover {
		t.Error("one blocking hour: expected CanCutover=false, got true")
	}
	if report.HoursBlocking != 1 {
		t.Errorf("expected 1 blocking hour, got %d", report.HoursBlocking)
	}
	if len(report.BlockingReasons) == 0 {
		t.Error("blocking report must include reasons")
	}
}

// TestBillingTolerancePct_Declared verifies the tolerance constant is within
// an expected range (catches accidental changes to a billing-critical value).
func TestBillingTolerancePct_Declared(t *testing.T) {
	tol := dual_run.BillingTolerancePct
	if tol <= 0 {
		t.Errorf("BillingTolerancePct must be positive, got %f", tol)
	}
	if tol >= 0.05 {
		t.Errorf("BillingTolerancePct too loose (>= 5%%): %f — requires ADR to change", tol)
	}
	t.Logf("BillingTolerancePct = %.2f%% (declared, requires ADR to change)", tol*100)
}

// TestReconcileHour_InvalidTolerance verifies that invalid tolerance returns an error.
func TestReconcileHour_InvalidTolerance(t *testing.T) {
	_, err := dual_run.ReconcileHour(testHour, nil, nil, 0)
	if err == nil {
		t.Error("tolerance=0 should return an error")
	}
	_, err = dual_run.ReconcileHour(testHour, nil, nil, 1.5)
	if err == nil {
		t.Error("tolerance=1.5 should return an error")
	}
}
