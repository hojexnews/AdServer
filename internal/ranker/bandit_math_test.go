// bandit_math_test.go — numeric correctness tests for mathExp, mathSqrt, mathLog.
//
// Defeito 3: the previous Taylor-series mathExp diverged catastrophically for
// |x| > ~4.5.  These tests prove that the stdlib-backed implementations match
// math.Exp / math.Sqrt / math.Log within floating-point machine precision.
package ranker

import (
	"math"
	"testing"
)

const mathTolerance = 1e-9

// TestMathExp_MatchesStdlib verifies mathExp(x) == math.Exp(x) for x in [-10, 10].
// Before the fix, the Taylor series diverged severely for |x| > 4.5:
//   mathExp(-10) ≈ large negative number; math.Exp(-10) ≈ 4.54e-5.
func TestMathExp_MatchesStdlib(t *testing.T) {
	cases := []float64{
		-10, -9, -8, -7, -6, -5.5, -5, -4.5, -4, -3, -2, -1,
		-0.5, 0, 0.5, 1, 2, 3, 4, 4.5, 5, 6, 7, 8, 9, 10,
	}
	for _, x := range cases {
		got := mathExp(x)
		want := math.Exp(x)
		if math.IsInf(got, 0) || math.IsNaN(got) {
			t.Errorf("mathExp(%v) = %v (Inf/NaN); want %v", x, got, want)
			continue
		}
		// Relative tolerance, except near zero where we use absolute.
		var err float64
		if math.Abs(want) > 1e-15 {
			err = math.Abs((got - want) / want)
		} else {
			err = math.Abs(got - want)
		}
		if err > mathTolerance {
			t.Errorf("mathExp(%v) = %.15g; want %.15g (rel err %.2e > tol %.2e)",
				x, got, want, err, mathTolerance)
		}
	}
}

// TestMathExp_CriticalRange confirms the specific failure range |x| > 4.5
// that the Taylor series got catastrophically wrong.
func TestMathExp_CriticalRange(t *testing.T) {
	criticals := []struct {
		x    float64
		desc string
	}{
		{-10, "softmax denominator floor"},
		{-7, "typical softmax stabilized input"},
		{-5, "boundary of old divergence"},
		{5, "positive side"},
	}
	for _, tc := range criticals {
		got := mathExp(tc.x)
		want := math.Exp(tc.x)
		if got < 0 {
			t.Errorf("mathExp(%v) [%s] returned negative %v; math.Exp returns %v",
				tc.x, tc.desc, got, want)
		}
		rel := math.Abs((got - want) / want)
		if rel > mathTolerance {
			t.Errorf("mathExp(%v) [%s] = %.15g; want %.15g (rel err %.2e)",
				tc.x, tc.desc, got, want, rel)
		}
	}
}

// TestMathSqrt_MatchesStdlib verifies mathSqrt(x) == math.Sqrt(x).
func TestMathSqrt_MatchesStdlib(t *testing.T) {
	cases := []float64{0, 0.01, 0.25, 1, 2, 4, 9, 100, 9.0 * 100}
	for _, x := range cases {
		got := mathSqrt(x)
		want := math.Sqrt(x)
		var err float64
		if want > 1e-15 {
			err = math.Abs((got - want) / want)
		} else {
			err = math.Abs(got - want)
		}
		if err > mathTolerance {
			t.Errorf("mathSqrt(%v) = %.15g; want %.15g (rel err %.2e)", x, got, want, err)
		}
	}
}

// TestMathLog_MatchesStdlib verifies mathLog(x) == math.Log(x).
func TestMathLog_MatchesStdlib(t *testing.T) {
	cases := []float64{0.01, 0.1, 0.5, 1, 2, math.E, 10, 100, 1000}
	for _, x := range cases {
		got := mathLog(x)
		want := math.Log(x)
		var err float64
		if math.Abs(want) > 1e-15 {
			err = math.Abs((got - want) / want)
		} else {
			err = math.Abs(got - want)
		}
		if err > mathTolerance {
			t.Errorf("mathLog(%v) = %.15g; want %.15g (rel err %.2e)", x, got, want, err)
		}
	}
}

// TestMathLog_NegativeAndZero verifies edge-case behaviour of mathLog.
func TestMathLog_NegativeAndZero(t *testing.T) {
	// math.Log(0) == -Inf; math.Log(<0) == NaN
	logZero := mathLog(0)
	if !math.IsInf(logZero, -1) {
		t.Errorf("mathLog(0) = %v; want -Inf", logZero)
	}
	logNeg := mathLog(-1)
	if !math.IsNaN(logNeg) {
		t.Errorf("mathLog(-1) = %v; want NaN", logNeg)
	}
}
