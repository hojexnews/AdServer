// score.go — eCPM scoring function for the ML re-ranker.
//
// Architecture role (DA-3 / TX-2):
//
//	ScoreCandidate produces the ranking signal used by the Remnant stratum
//	re-ranker.  It is the SINGLE point where pCTR × bid is computed, as
//	documented in ml/training/pipeline.py:15 and ml/calibration/calibrate.py:42.
//
// This function is a RANKING SIGNAL, not a ledger entry:
//
//	The returned value is used only to order candidates within the Remnant
//	stratum before the cascade selects the first eligible one.  It is never
//	persisted as a financial amount, never compared across asset_codes
//	(DA-10), and never passed to the money ledger.  Float is legitimately
//	used here only for the intermediate probability × integer multiplication;
//	the final result is int64 minor-units.
//
// Rounding choice — math.Round (ROUND_HALF_AWAY_FROM_ZERO):
//
//	We use math.Round rather than math.RoundToEven (banker's rounding /
//	ROUND_HALF_EVEN) for the following reasons:
//	  1. This is a RANKING SIGNAL.  Systematic bias from ROUND_HALF_AWAY
//	     is negligible when the objective is relative ordering, not
//	     accounting reconciliation.  ROUND_HALF_EVEN matters for ledger
//	     entries where accumulated half-cent bias propagates to balances;
//	     that does not apply here.
//	  2. math.Round is a single standard-library call with no import
//	     overhead, whereas ROUND_HALF_EVEN requires checking the parity of
//	     the integer part — adding code without benefit for a signal that
//	     is discarded after the sort.
//	  3. Consistency with pipeline.py:15 comment:
//	       int64(math.Round(float64(pCTR_cal) * float64(bid_minor_units)))
//	     Both sides agree on ROUND_HALF_AWAY_FROM_ZERO.
//	If a ledger path ever reuses this function (it should not), it must be
//	replaced with ROUND_HALF_EVEN and reviewed by [[money-ledger-guardian]].
//
// Overflow guard (int64 / TX-2):
//
//	For most ad networks (bid in BRL minor-units, i.e., centavos):
//	  pCTR ∈ [0,1], bid ∈ [0, ~10_000_00] (R$100k floor) →
//	  product ≤ 10_000_00 ≪ math.MaxInt64 (≈9.2×10^18).
//
//	Risk arises with high-scale asset codes:
//	  - USDC (scale=6):   bid up to ~10^13 minor-units (safe).
//	  - ERC-20 (scale=18): bid up to ~10^18 — within int64 range only
//	    if pCTR < ~9.2, which it always is ([0,1]), so:
//	    max product = 1.0 × 9_223_372_036_854_775_807 ≈ 9.2×10^18 (safe).
//
//	The ONLY overflow path is pCTR > 1.0 (malformed sidecar output) combined
//	with bid near math.MaxInt64.  The guard below clips pCTR to [0,1] before
//	multiplication.  If bid itself overflows int64 upstream that is a bug in
//	the cascade's ECPMMinorUnits field, not fixable here.
//
//	RESTRICTION: bid must be non-negative.  Negative bids are economically
//	nonsensical; the guard returns 0 for bid < 0.
package ranker

import "math"

// ScoreCandidate computes the expected eCPM for a candidate as a ranking
// signal for the Remnant stratum.
//
// Formula:
//
//	eCPM_minor_units = int64(math.Round(float64(pCTR) * float64(bidMinorUnits)))
//
// Parameters:
//   - pCTR: predicted click-through rate from the calibrated sidecar output,
//     in [0, 1].  This is a probability — float32 is correct here (TX-2 does
//     not apply to probabilities, only to monetary amounts).
//   - bidMinorUnits: the campaign's bid/floor in minor-units of its asset
//     (int64, TX-2).  Must be >= 0.
//
// Returns the expected eCPM in minor-units (int64).  For ranking only; never
// persist or compare across asset_codes (DA-10).
//
// Overflow guard: pCTR is clipped to [0, 1] before multiplication.  Negative
// bidMinorUnits returns 0.  See package-level doc for the full overflow
// analysis.
//
// Stub behaviour (J1): the sidecar returns pCTR = 0 for all candidates.
// ScoreCandidate(0, anyBid) = 0 for every candidate, so sort.SliceStable
// preserves the deterministic cascade order (eCPM-sorted by the cascade
// itself).  The J1 golden tests remain green because: 0 × bid = 0 for all →
// stable sort is a no-op → order identical to cascade input.
func ScoreCandidate(pCTR float32, bidMinorUnits int64) int64 { //nolint:forbidigo // pCTR is a probability [0,1], not money (bid stays int64 minor-units)
	if bidMinorUnits < 0 {
		return 0
	}
	// Clip pCTR to [0, 1] to guard against malformed sidecar output.
	p := float64(pCTR) //nolint:forbidigo // intermediate probability, not money
	if p < 0 {
		p = 0
	} else if p > 1 {
		p = 1
	}
	// Intermediate float64 product; the result is immediately rounded to int64.
	// This is the only permitted float here (ranking signal arithmetic, TX-2).
	product := p * float64(bidMinorUnits) //nolint:forbidigo // ranking signal arithmetic, not money
	return int64(math.Round(product))
}
