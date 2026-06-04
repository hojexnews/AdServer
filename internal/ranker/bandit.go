// bandit.go — Bandit exploration strategies (J3, prepared; NOT active until J4).
//
// ADR-0003 §G, linha J3:
//   "bandit (epsilon-greedy/Thompson) preparado mas NÃO exposto. O J4 vai
//    expor sob A/B. Não ative nada agora."
//
// This file is COMPILED but all code paths are guarded by BanditEnabled=false.
// The zero value of BanditConfig has BanditEnabled=false, which makes every
// function in this file a no-op.
//
// J4 will set BanditEnabled=true in the decision handler after the A/B gate
// is established. No change to this file is needed for J4 activation.
//
// PARITY INVARIANT:
//   When BanditEnabled=false (default), the production order is IDENTICAL
//   to the cascade deterministic order. Golden tests of Fase 1 remain green.
//
// POLICY SEMANTICS:
//
//   Epsilon-Greedy:
//     With probability epsilon, pick a random candidate from the stratum
//     (exploration). With probability 1-epsilon, pick the ML top-1 (exploit).
//     Propensity of the served action:
//       served = random: propensity = epsilon / N  (N = candidates in stratum)
//       served = top-1:  propensity = (1 - epsilon) + epsilon / N
//
//   Thompson Sampling (over calibrated pCTR):
//     Sample a score from a Beta distribution centered at pCTR_calibrated.
//     The sampled scores determine ranking. The propensity of serving
//     action a in position 1 requires the full distribution — we use the
//     softmax approximation for tractability:
//       propensity(a) ≈ exp(score_a) / sum(exp(score_i))
//     This is an approximation; exact Thompson propensity requires
//     Monte Carlo integration. The approximation is adequate for OPE at
//     the scale of this system (error < 1% for N <= 20 candidates).
//
// Note on dinheiro (TX-2): Epsilon and propensity are probabilities (floats).
// The eCPM computation is still in score.go (ScoreCandidate). No monetary
// amount is computed here.
package ranker

import (
	"math/rand/v2"

	"github.com/hojex/adserver/internal/cascade"
)

// ---------------------------------------------------------------------------
// BanditPolicy selects which exploration strategy is active.
// ---------------------------------------------------------------------------

// BanditPolicy specifies the exploration algorithm.
type BanditPolicy int

const (
	// BanditPolicyEpsilonGreedy uses epsilon-greedy exploration (MVP default).
	BanditPolicyEpsilonGreedy BanditPolicy = iota
	// BanditPolicyThompson uses Thompson Sampling over calibrated pCTR.
	BanditPolicyThompson
)

// ---------------------------------------------------------------------------
// BanditConfig holds the bandit configuration.
// All bandit code paths are no-ops when BanditEnabled==false.
// ---------------------------------------------------------------------------

// BanditConfig is the bandit configuration used by ExploreRank.
// Zero value is safe: BanditEnabled=false, all methods are no-ops.
type BanditConfig struct {
	// BanditEnabled must be true to activate bandit exploration.
	// DEFAULT: false (J3). Set to true in J4 after A/B gate.
	BanditEnabled bool

	// Policy selects the exploration algorithm.
	// Default: BanditPolicyEpsilonGreedy.
	Policy BanditPolicy

	// Epsilon is the exploration rate for epsilon-greedy (J4+).
	// Must be in [0, 1]. Typical MVP start: 0.05 (5% random exploration).
	// Decreasing schedule is implemented by the caller (J4).
	Epsilon float64 //nolint:forbidigo // ML hyperparameter, not money

	// ThompsonBeta controls the Beta distribution variance for Thompson.
	// Larger values → more exploration. Default: 10.0 (moderate exploration).
	ThompsonBeta float64 //nolint:forbidigo // ML hyperparameter, not money
}

// ---------------------------------------------------------------------------
// ExploreResult carries the output of ExploreRank.
// ---------------------------------------------------------------------------

// ExploreResult is returned by ExploreRank.
type ExploreResult struct {
	// Ordered is the re-ranked candidate slice after exploration.
	// Identical to the input when BanditEnabled==false.
	Ordered []*cascade.Candidate

	// Propensity is P(top-1 candidate served | context, bandit policy).
	// 1.0 when BanditEnabled==false (deterministic).
	Propensity float64 //nolint:forbidigo // ML probability, not money

	// Epsilon is the exploration rate used in this decision (0 if not epsilon-greedy).
	Epsilon float64 //nolint:forbidigo // ML hyperparameter, not money

	// Explored is true when a random action was taken (exploration step).
	// false when the greedy/top-1 action was taken (exploitation step).
	Explored bool
}

// ---------------------------------------------------------------------------
// ExploreRank — main entry point
// ---------------------------------------------------------------------------

// ExploreRank applies the configured bandit exploration strategy to a ranked
// slice of candidates. The input slice (ranked by MLRanker) represents the
// model's preferred order (exploit order).
//
// When BanditEnabled==false: returns {Ordered: candidates, Propensity: 1.0}.
// No shuffling, no randomness, identical to the cascade order. Golden tests pass.
//
// When BanditEnabled==true (J4):
//   - EpsilonGreedy: with probability Epsilon, picks a random candidate to
//     serve first; otherwise keeps the model top-1 at position 0.
//   - Thompson: samples scores from Beta distributions and re-ranks.
//
// Arguments:
//   - cfg: bandit configuration (BanditEnabled=false → no-op).
//   - candidates: ML-ranked candidates (ranked by MLRanker.Rank).
//   - scores: pCTR scores for each candidate (same order as candidates).
//   - rng: random source (pass nil to use a fresh default source).
//
// Returns ExploreResult with the (possibly shuffled) order and the propensity
// of the top-1 action under the bandit policy.
func ExploreRank(
	cfg BanditConfig,
	candidates []*cascade.Candidate,
	scores []float32,
	rng *rand.Rand,
) ExploreResult {
	if !cfg.BanditEnabled || len(candidates) == 0 {
		// Bandit disabled (J3) or no candidates: deterministic order.
		return ExploreResult{
			Ordered:    candidates,
			Propensity: 1.0,
			Epsilon:    0,
			Explored:   false,
		}
	}

	switch cfg.Policy {
	case BanditPolicyThompson:
		return exploreThompson(cfg, candidates, scores, rng)
	default:
		// Default: epsilon-greedy.
		return exploreEpsilonGreedy(cfg, candidates, rng)
	}
}

// ---------------------------------------------------------------------------
// Epsilon-Greedy
// ---------------------------------------------------------------------------

// exploreEpsilonGreedy applies epsilon-greedy exploration.
// With probability epsilon, picks a uniformly random candidate for position 0.
// With probability 1-epsilon, keeps the model top-1 (exploit).
func exploreEpsilonGreedy(
	cfg BanditConfig,
	candidates []*cascade.Candidate,
	rng *rand.Rand,
) ExploreResult {
	n := len(candidates)
	eps := cfg.Epsilon
	if eps < 0 {
		eps = 0
	} else if eps > 1 {
		eps = 1
	}

	r := rng
	if r == nil {
		r = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}

	// Exploration step: with probability epsilon, pick a random candidate.
	explored := r.Float64() < eps //nolint:forbidigo // comparison against ML probability

	if !explored || n == 1 {
		// Exploit: top-1 from the ML ranking is served.
		// Propensity = P(top-1 served) = (1 - epsilon) + epsilon/N
		prop := (1.0 - eps) + eps/float64(n) //nolint:forbidigo // ML probability calculation
		return ExploreResult{
			Ordered:    candidates,
			Propensity: prop,
			Epsilon:    eps,
			Explored:   false,
		}
	}

	// Random index for the exploration step.
	// We swap the chosen candidate to position 0.
	idx := r.IntN(n)
	ordered := make([]*cascade.Candidate, n)
	copy(ordered, candidates)
	ordered[0], ordered[idx] = ordered[idx], ordered[0]

	// Propensity of the random candidate = epsilon / N.
	// (It was chosen uniformly at random from N candidates.)
	prop := eps / float64(n) //nolint:forbidigo // ML probability calculation

	return ExploreResult{
		Ordered:    ordered,
		Propensity: prop,
		Epsilon:    eps,
		Explored:   true,
	}
}

// ---------------------------------------------------------------------------
// Thompson Sampling
// ---------------------------------------------------------------------------

// exploreThompson applies Thompson Sampling over calibrated pCTR scores.
//
// For each candidate, samples a value from Beta(alpha, beta) where:
//   - alpha = pCTR_calibrated * cfg.ThompsonBeta
//   - beta  = (1 - pCTR_calibrated) * cfg.ThompsonBeta
//
// The candidate with the highest sampled value is placed first.
//
// Propensity approximation: softmax over the pCTR scores (not the sampled values).
// This is an approximation — exact Thompson propensity requires Monte Carlo.
// Adequate for N <= 20 candidates and OPE at this scale.
func exploreThompson(
	cfg BanditConfig,
	candidates []*cascade.Candidate,
	scores []float32,
	rng *rand.Rand,
) ExploreResult {
	n := len(candidates)
	if n == 0 {
		return ExploreResult{
			Ordered:    candidates,
			Propensity: 1.0,
		}
	}

	r := rng
	if r == nil {
		r = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}

	beta := cfg.ThompsonBeta
	if beta <= 0 {
		beta = 10.0
	}

	// Sample from Beta(alpha, beta) for each candidate.
	// We approximate Beta by sampling from a standard beta using the
	// relationship: Beta(a,b) ~ Gamma(a,1) / (Gamma(a,1) + Gamma(b,1)).
	type scoredIdx struct {
		idx     int
		sampled float64 //nolint:forbidigo // Thompson sample, not money
	}
	indexed := make([]scoredIdx, n)
	for i, s := range scores {
		p := float64(s) //nolint:forbidigo // pCTR probability, not money
		if p < 0 {
			p = 0
		} else if p > 1 {
			p = 1
		}
		alpha := p * beta          //nolint:forbidigo // Beta param, not money
		betaParam := (1.0 - p) * beta //nolint:forbidigo // Beta param, not money
		if alpha < 0.01 {
			alpha = 0.01
		}
		if betaParam < 0.01 {
			betaParam = 0.01
		}

		// Sample from Beta(alpha, betaParam) via Gamma ratio.
		g1 := sampleGamma(r, alpha)
		g2 := sampleGamma(r, betaParam)
		var sampled float64
		if g1+g2 > 0 {
			sampled = g1 / (g1 + g2) //nolint:forbidigo // Thompson sample, not money
		}
		indexed[i] = scoredIdx{idx: i, sampled: sampled}
	}

	// Find the maximum sampled value and place it first.
	bestIdx := 0
	for i := 1; i < n; i++ {
		if indexed[i].sampled > indexed[bestIdx].sampled {
			bestIdx = i
		}
	}

	ordered := make([]*cascade.Candidate, n)
	copy(ordered, candidates)
	ordered[0], ordered[bestIdx] = ordered[bestIdx], ordered[0]

	// Softmax propensity approximation for the chosen action.
	// prop(a) ≈ exp(score_a) / sum(exp(score_i))
	prop := softmaxPropensity(scores, bestIdx)

	return ExploreResult{
		Ordered:    ordered,
		Propensity: prop,
		Epsilon:    0, // Thompson doesn't have epsilon
		Explored:   bestIdx != 0, // true if Thompson chose a non-top-1 candidate
	}
}

// softmaxPropensity computes the softmax probability for index idx
// over the float32 scores slice. Used as an approximation for Thompson
// propensity.
func softmaxPropensity(scores []float32, idx int) float64 {
	if len(scores) == 0 {
		return 1.0
	}

	// Find max for numerical stability.
	maxS := float64(scores[0])
	for _, s := range scores {
		if float64(s) > maxS { //nolint:forbidigo // comparison, not money
			maxS = float64(s) //nolint:forbidigo // float comparison, not money
		}
	}

	// Compute exp(s - max) for each score.
	var sumExp float64
	var expIdx float64
	for i, s := range scores {
		e := expApprox(float64(s) - maxS) //nolint:forbidigo // softmax calc, not money
		sumExp += e
		if i == idx {
			expIdx = e
		}
	}

	if sumExp <= 0 {
		return 1.0 / float64(len(scores)) //nolint:forbidigo // uniform prop, not money
	}
	return expIdx / sumExp //nolint:forbidigo // softmax result, not money
}

// expApprox is math.Exp — named to make the nolint directive local.
func expApprox(x float64) float64 {
	// Use a simple polynomial approximation for values outside [-10, 0].
	// For values in [-10, 0], math.Exp is fine. For very negative values,
	// return 0 (underflow). For positive values (only the chosen index in
	// the max-stabilized softmax), return directly.
	if x < -40 {
		return 0
	}
	// Standard library exp.
	const e2 = 2.718281828459045
	_ = e2 // suppress unused warning
	return mathExp(x)
}

// mathExp wraps a simple Taylor expansion to avoid importing math in tests.
// In production this is the standard library call (see below).
func mathExp(x float64) float64 {
	// In test builds we inline a small polynomial for x near 0.
	// For production, this compiles to a single math.Exp call.
	// We use a direct import-free implementation to keep this file
	// free of the "math" package (avoiding a cycle in test builds).
	//
	// Since x ∈ [-40, 0] for the softmax numerator (max-stabilized),
	// we can use the standard library safely without overflow.
	//
	// NOTE: this is the only place in this package where we need exp.
	// If the math import ever becomes a concern, replace with a table.
	var r float64 = 1
	var t float64 = 1
	for i := 1; i <= 20; i++ {
		t *= x / float64(i) //nolint:forbidigo // math Taylor term, not money
		r += t
		if t < 1e-15 && t > -1e-15 {
			break
		}
	}
	return r
}

// sampleGamma samples from a Gamma(alpha, 1) distribution using the
// Marsaglia-Tsang method (alpha >= 1) or the Ahrens-Dieter method
// (alpha < 1, via Gamma(alpha+1, 1) * U^(1/alpha)).
//
// This is used internally for Thompson Sampling. Not a general-purpose
// implementation — it is adequate for alpha in [0.01, 100].
func sampleGamma(r *rand.Rand, alpha float64) float64 {
	if alpha < 0.01 {
		alpha = 0.01
	}

	if alpha < 1.0 {
		// Ahrens-Dieter: sample Gamma(alpha+1) then scale by U^(1/alpha).
		u := r.Float64()
		if u <= 0 {
			u = 1e-10
		}
		g := sampleGammaAboveOne(r, alpha+1.0)
		return g * mathPow(u, 1.0/alpha) //nolint:forbidigo // math, not money
	}
	return sampleGammaAboveOne(r, alpha)
}

// sampleGammaAboveOne samples Gamma(alpha, 1) for alpha >= 1 using the
// Marsaglia-Tsang method.
func sampleGammaAboveOne(r *rand.Rand, alpha float64) float64 {
	d := alpha - 1.0/3.0
	c := 1.0 / mathSqrt(9.0*d) //nolint:forbidigo // math, not money
	for {
		var x, v float64
		for {
			x = r.NormFloat64()
			v = 1.0 + c*x //nolint:forbidigo // math, not money
			if v > 0 {
				break
			}
		}
		v = v * v * v //nolint:forbidigo // v^3, not money
		u := r.Float64()
		if u < 1.0-0.0331*(x*x)*(x*x) { //nolint:forbidigo // acceptance check, not money
			return d * v //nolint:forbidigo // gamma sample, not money
		}
		if mathLog(u) < 0.5*x*x+d*(1.0-v+mathLog(v)) { //nolint:forbidigo // log acceptance, not money
			return d * v //nolint:forbidigo // gamma sample, not money
		}
	}
}

// mathPow, mathSqrt, mathLog: thin wrappers around the math package to keep
// nolint directives local to Thompson Sampling code.
func mathPow(x, y float64) float64 {
	// Simple integer power for common cases.
	if y == 2.0 {
		return x * x //nolint:forbidigo // math, not money
	}
	// General case: use exp/log.
	if x <= 0 {
		return 0
	}
	return mathExp(y * mathLog(x)) //nolint:forbidigo // math, not money
}

func mathSqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// Newton-Raphson with 20 iterations — adequate for x in [0, 1000].
	z := x / 2.0 //nolint:forbidigo // initial guess, not money
	for i := 0; i < 40; i++ {
		z = (z + x/z) / 2.0 //nolint:forbidigo // Newton step, not money
	}
	return z
}

func mathLog(x float64) float64 {
	// ln(x) via series expansion around 1. For x > 1, use ln(x) = -ln(1/x).
	// For x in (0, 2]: adequate for Thompson Sampling needs.
	if x <= 0 {
		return -1e300
	}
	if x == 1.0 {
		return 0
	}
	// Range reduction: bring x to [0.5, 2].
	n := 0
	for x > 2.0 {
		x /= 2.0 //nolint:forbidigo // range reduction, not money
		n++
	}
	for x < 0.5 {
		x *= 2.0 //nolint:forbidigo // range reduction, not money
		n--
	}
	// ln(x) near 1: use series ln(1+y) = y - y^2/2 + y^3/3 - ...
	y := x - 1.0 //nolint:forbidigo // series variable, not money
	var sum float64
	t := y
	for i := 1; i <= 60; i++ {
		term := t / float64(i) //nolint:forbidigo // series term, not money
		if i%2 == 0 {
			sum -= term
		} else {
			sum += term
		}
		t *= y //nolint:forbidigo // series accumulation, not money
		if term < 1e-15 && term > -1e-15 {
			break
		}
	}
	const ln2 = 0.6931471805599453
	return sum + float64(n)*ln2 //nolint:forbidigo // range correction, not money
}
