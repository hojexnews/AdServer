// Package money provides helpers for the Money type defined in
// gen/go/adserver/money/v1 (TX-2 / DA-10).
//
// RULES:
//   - Never use float32 or float64 for monetary values (TX-2).
//   - Comparisons are valid ONLY within the same asset_code and the same
//     tenant (DA-10).  Cross-currency comparisons panic to make the
//     programming error loud rather than silent.
//   - eCPM arithmetic stays in integer/fixed-point (int64 minor-units).
//
// The no-float lint guard (contracts/lint/no-float.md) is scoped to
// money/ directories and will fail CI if float32/float64 appear here.
package money

import (
	"errors"
	"fmt"

	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

// ErrCurrencyMismatch is returned when two Money values with different
// asset_codes are compared.  The hot path should treat this as a
// programming error and log + drop the mismatched candidate.
var ErrCurrencyMismatch = errors.New("money: cross-currency comparison is forbidden (DA-10)")

// Compare returns -1, 0, or 1 for a < b, a == b, a > b respectively.
// It normalises both values to the same scale before comparing.
// Returns an error if the asset codes differ.
//
// NOTE: scale normalisation is done in integer arithmetic only — no float.
func Compare(a, b *moneyv1.Money) (int, error) {
	if a.GetAssetCode() != b.GetAssetCode() {
		return 0, fmt.Errorf("%w: %q vs %q", ErrCurrencyMismatch,
			a.GetAssetCode(), b.GetAssetCode())
	}
	// Normalise to the higher scale to avoid truncation.
	aAmt, bAmt := a.GetAmount(), b.GetAmount()
	aScale, bScale := a.GetScale(), b.GetScale()
	if aScale < bScale {
		aAmt = scaleUp(aAmt, bScale-aScale)
	} else if bScale < aScale {
		bAmt = scaleUp(bAmt, aScale-bScale)
	}
	switch {
	case aAmt < bAmt:
		return -1, nil
	case aAmt > bAmt:
		return 1, nil
	default:
		return 0, nil
	}
}

// MustCompare is like Compare but panics on currency mismatch.
// Use only when the caller has already verified the asset codes are the same.
func MustCompare(a, b *moneyv1.Money) int {
	cmp, err := Compare(a, b)
	if err != nil {
		panic(err)
	}
	return cmp
}

// IsZero reports whether m represents zero minor-units.
func IsZero(m *moneyv1.Money) bool {
	return m.GetAmount() == 0
}

// IsPositive reports whether m.Amount > 0.
func IsPositive(m *moneyv1.Money) bool {
	return m.GetAmount() > 0
}

// GreaterThan returns true when a > b (same asset_code, same tenant assumed).
func GreaterThan(a, b *moneyv1.Money) (bool, error) {
	cmp, err := Compare(a, b)
	return cmp > 0, err
}

// ---------------------------------------------------------------------------
// eCPM helper (fixed-point, no float)
// ---------------------------------------------------------------------------

// ECPMMinorUnits computes eCPM in minor-units of the campaign's asset_code.
//
//	eCPM = (revenue_minor_units * 1000) / impressions
//
// This is integer arithmetic throughout.  The result is in the same
// scale as the input revenue Money — so 1 BRL minor-unit (centavo) of
// eCPM means scale=2, eCPM=1 centavo per mille.
//
// Returns 0 when impressions == 0 to avoid division by zero.
// The caller must ensure revenue and the denominator share the same asset.
func ECPMMinorUnits(revenueAmount int64, impressions int64) int64 {
	if impressions == 0 {
		return 0
	}
	return (revenueAmount * 1000) / impressions
}

// ECPMFromMoney wraps ECPMMinorUnits for a *moneyv1.Money revenue value.
// The returned *moneyv1.Money carries the same asset_code and scale.
func ECPMFromMoney(revenue *moneyv1.Money, impressions int64) *moneyv1.Money {
	if revenue == nil {
		return &moneyv1.Money{}
	}
	return &moneyv1.Money{
		AssetCode: revenue.GetAssetCode(),
		Amount:    ECPMMinorUnits(revenue.GetAmount(), impressions),
		Scale:     revenue.GetScale(),
	}
}

// ---------------------------------------------------------------------------
// internal helpers — integer only, no float
// ---------------------------------------------------------------------------

// scaleUp multiplies amount by 10^diff to bring it to a higher scale.
// diff must be >= 0.  Overflow is not protected — callers must ensure
// scale differences are reasonable (Asset Registry enforces max scale = 18).
func scaleUp(amount int64, diff uint32) int64 {
	for i := uint32(0); i < diff; i++ {
		amount *= 10
	}
	return amount
}
