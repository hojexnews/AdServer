// guard.go — Revenue guard and kill-switch for J4 (ADR-0003 §G).
//
// DESIGN CONTRACT (J4):
//
//   The revenue guard compares eCPM between treatment and control arms using
//   ONLY int64 minor-units (TX-2). No float is used for monetary comparison;
//   floating-point arithmetic is used ONLY for the relative threshold check
//   (treatment_avg / control_avg) which is a dimensionless ratio, not money.
//
//   Guard logic:
//     1. Both arms accumulate (impression_count, ecpm_sum_minor_units) in
//        lock-protected counters.
//     2. On each Observe call, if both arms have at least MinImpressions,
//        the guard checks:
//          treatment_avg_minor = ecpm_sum_treatment / count_treatment
//          control_avg_minor   = ecpm_sum_control   / count_control
//          gap_ratio = (control_avg - treatment_avg) / control_avg
//          if gap_ratio > MaterialThreshold → auto-activate kill-switch
//     3. The kill-switch propagates to the ABRouter via the provided callback.
//
//   Fail-safe semantics:
//     Any doubt → control. The guard trips only when treatment is materially
//     WORSE than control, never to promote treatment above what A/B earns it.
//
//   TX-2 note: ecpm_sum_minor_units is int64; the counter can overflow if a
//   single tenant accumulates > 9.2×10^18 minor-units (BRL centavos = ~92 PB
//   of revenue). In practice safe; documented for completeness.
//
//   PII-free (TX-5/DA-11): the guard receives only zone_id, arm enum, and eCPM
//   minor-units. No user identifiers.
package ranker

import (
	"log/slog"
	"sync"
)

// ---------------------------------------------------------------------------
// GuardConfig
// ---------------------------------------------------------------------------

// GuardConfig is the revenue guard configuration.
// Zero value enables the guard with conservative defaults.
type GuardConfig struct {
	// MinImpressions is the minimum number of impressions each arm must
	// accumulate before the guard starts comparing. Prevents premature
	// kill-switch on very sparse data.
	// Default: 1000.
	MinImpressions int64

	// MaterialThreshold is the relative eCPM gap that triggers the auto
	// kill-switch.
	// Formula: (control_avg - treatment_avg) / control_avg > threshold
	// Example: 0.05 = 5% drop triggers kill.
	// Default: 0.05 (5%).
	//
	// nolint:forbidigo — dimensionless ratio, not money
	MaterialThreshold float64

	// MaxImbalanceRatio is the maximum ratio of impression counts between
	// arms before the guard warns about imbalance.
	// Example: 10.0 = if one arm has 10x more impressions than the other,
	// emit a warning. Guard still operates (imbalance ≠ kill-switch).
	// Default: 10.0.
	//
	// nolint:forbidigo — dimensionless ratio, not money
	MaxImbalanceRatio float64
}

func defaultGuardConfig() GuardConfig {
	return GuardConfig{
		MinImpressions:    1000,
		MaterialThreshold: 0.05, //nolint:forbidigo // dimensionless ratio
		MaxImbalanceRatio: 10.0, //nolint:forbidigo // dimensionless ratio
	}
}

// ---------------------------------------------------------------------------
// ArmStats holds per-arm accumulated statistics.
// ---------------------------------------------------------------------------

// ArmStats accumulates eCPM statistics for one A/B arm.
type ArmStats struct {
	// Impressions is the total number of decisions observed for this arm.
	Impressions int64
	// ECPMSumMinorUnits is the sum of eCPM minor-units across all impressions.
	// TX-2: int64, never float.
	ECPMSumMinorUnits int64
}

// AvgECPMMinorUnits returns the mean eCPM in minor-units (int64 division).
// Returns 0 when Impressions == 0.
func (s ArmStats) AvgECPMMinorUnits() int64 {
	if s.Impressions == 0 {
		return 0
	}
	return s.ECPMSumMinorUnits / s.Impressions
}

// ---------------------------------------------------------------------------
// RevenueGuard
// ---------------------------------------------------------------------------

// RevenueGuard monitors eCPM between treatment and control arms and
// activates the kill-switch when treatment is materially worse.
//
// Usage:
//
//	guard := NewRevenueGuard(cfg, router, logger)
//	// In the decision handler, after building the Decision:
//	guard.Observe(arm, ecpmMinorUnits)
type RevenueGuard struct {
	cfg    GuardConfig
	router interface {
		EnableKillSwitch()
	}
	logger *slog.Logger

	mu        sync.Mutex
	control   ArmStats
	treatment ArmStats

	// tripped is true after the guard has auto-activated the kill-switch.
	// Once tripped, the guard keeps reporting but does not flip the switch again.
	tripped bool
}

// NewRevenueGuard creates a RevenueGuard.
//
//   - cfg: guard configuration; zero value uses conservative defaults.
//   - router: receives EnableKillSwitch() when the guard trips.
//     Must not be nil (the guard is fail-safe: it MUST be able to activate
//     the kill-switch).
//   - logger: may be nil.
func NewRevenueGuard(cfg GuardConfig, router interface{ EnableKillSwitch() }, logger *slog.Logger) *RevenueGuard {
	// Fill in defaults.
	if cfg.MinImpressions <= 0 {
		cfg.MinImpressions = defaultGuardConfig().MinImpressions
	}
	if cfg.MaterialThreshold <= 0 { //nolint:forbidigo // float comparison for threshold setup
		cfg.MaterialThreshold = defaultGuardConfig().MaterialThreshold //nolint:forbidigo // copying default
	}
	if cfg.MaxImbalanceRatio <= 0 { //nolint:forbidigo // float comparison for ratio setup
		cfg.MaxImbalanceRatio = defaultGuardConfig().MaxImbalanceRatio //nolint:forbidigo // copying default
	}
	return &RevenueGuard{
		cfg:    cfg,
		router: router,
		logger: logger,
	}
}

// Observe records a decision's eCPM for the given arm and checks the guard.
// Must be called after every decision (fire-and-forget; always returns quickly).
//
// ecpmMinorUnits must be the eCPM of the decision in minor-units (int64, TX-2).
// Use 0 for BLANK decisions.
//
// arm: ABArmControl or ABArmTreatment.
func (g *RevenueGuard) Observe(arm ABArm, ecpmMinorUnits int64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch arm {
	case ABArmControl:
		g.control.Impressions++
		g.control.ECPMSumMinorUnits += ecpmMinorUnits
	case ABArmTreatment:
		g.treatment.Impressions++
		g.treatment.ECPMSumMinorUnits += ecpmMinorUnits
	}

	// Only check after both arms have minimum impressions.
	if g.control.Impressions < g.cfg.MinImpressions ||
		g.treatment.Impressions < g.cfg.MinImpressions {
		return
	}

	// Log imbalance warning (does NOT trigger kill-switch).
	if g.control.Impressions > 0 && g.treatment.Impressions > 0 {
		var ratio float64 //nolint:forbidigo // dimensionless ratio, not money
		if g.treatment.Impressions > g.control.Impressions {
			ratio = float64(g.treatment.Impressions) / float64(g.control.Impressions) //nolint:forbidigo // impression ratio
		} else {
			ratio = float64(g.control.Impressions) / float64(g.treatment.Impressions) //nolint:forbidigo // impression ratio
		}
		if ratio > g.cfg.MaxImbalanceRatio && g.logger != nil { //nolint:forbidigo // ratio comparison
			g.logger.Warn("revenue_guard: arm impression imbalance",
				"control_impressions", g.control.Impressions,
				"treatment_impressions", g.treatment.Impressions,
				"ratio", ratio,
			)
		}
	}

	// Skip if already tripped (kill-switch already active).
	if g.tripped {
		return
	}

	controlAvg := g.control.AvgECPMMinorUnits()
	treatmentAvg := g.treatment.AvgECPMMinorUnits()

	// Only check when control has positive eCPM (avoid div-by-zero).
	if controlAvg <= 0 {
		return
	}

	// gap_ratio = (control_avg - treatment_avg) / control_avg
	// All intermediate values stay in int64 until we compute the ratio.
	// TX-2: controlAvg and treatmentAvg are int64 minor-units.
	// The ratio itself is dimensionless (float64 is correct here).
	gap := controlAvg - treatmentAvg // int64 minor-units difference
	// nolint:forbidigo — computing dimensionless ratio from minor-unit integers
	gapRatio := float64(gap) / float64(controlAvg) //nolint:forbidigo // dimensionless ratio

	if gapRatio > g.cfg.MaterialThreshold { //nolint:forbidigo // ratio comparison
		g.tripped = true
		if g.logger != nil {
			g.logger.Warn("revenue_guard: treatment eCPM materially below control — ACTIVATING KILL-SWITCH",
				"control_avg_minor_units", controlAvg,
				"treatment_avg_minor_units", treatmentAvg,
				"gap_ratio_pct", gapRatio*100.0, //nolint:forbidigo // logging percentage
				"threshold_pct", g.cfg.MaterialThreshold*100.0, //nolint:forbidigo // logging percentage
				"control_impressions", g.control.Impressions,
				"treatment_impressions", g.treatment.Impressions,
			)
		}
		g.router.EnableKillSwitch()
	}
}

// Stats returns a snapshot of the current arm statistics (for monitoring/logging).
// Callers must not modify the returned structs.
func (g *RevenueGuard) Stats() (control, treatment ArmStats, tripped bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.control, g.treatment, g.tripped
}

// Reset clears all accumulated statistics and un-trips the guard.
// Used in tests; do not call in production unless an operator explicitly
// wants to reset the guard after resolving a kill-switch incident.
func (g *RevenueGuard) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.control = ArmStats{}
	g.treatment = ArmStats{}
	g.tripped = false
}
