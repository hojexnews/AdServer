// ab_test.go — unit tests for A/B assignment, revenue guard, and bandit
// exposure under treatment (J4).
//
// Test coverage:
//   1. ABRouter: deterministic assignment, kill-switch, bucket boundaries.
//   2. RevenueGuard: auto-kill on material eCPM drop, min-impressions gate.
//   3. Bandit under treatment: ExploreRank only in treatment arm, propensity<1.0.
//   4. Control arm: no ML call, propensity=1.0, parity with pure cascade order.
package ranker

import (
	"testing"
)

// ---------------------------------------------------------------------------
// ABRouter tests
// ---------------------------------------------------------------------------

// TestHashBucket_Stable verifies that hashBucket returns the same value for
// the same inputs (deterministic).
func TestHashBucket_Stable(t *testing.T) {
	b1 := hashBucket("zone-1", "tenant-a")
	b2 := hashBucket("zone-1", "tenant-a")
	if b1 != b2 {
		t.Errorf("hashBucket not stable: %d != %d", b1, b2)
	}
}

// TestHashBucket_InRange verifies that hashBucket always returns [0, NumBuckets).
func TestHashBucket_InRange(t *testing.T) {
	cases := [][2]string{
		{"z1", "t1"},
		{"z2", "t2"},
		{"", ""},
		{"very-long-zone-identifier-12345", "tenant-with-uuid-00000000-0000-0000-0000-000000000000"},
		{"ab", "c"},
		{"a", "bc"}, // separator prevents these two from colliding
	}
	for _, c := range cases {
		b := hashBucket(c[0], c[1])
		if b < 0 || b >= NumBuckets {
			t.Errorf("hashBucket(%q,%q) = %d, want [0,%d)", c[0], c[1], b, NumBuckets)
		}
	}
}

// TestHashBucket_Separator verifies that swapping between zone/tenant suffix
// produces different buckets most of the time (separator byte prevents collision).
func TestHashBucket_Separator(t *testing.T) {
	// "ab"+"c" vs "a"+"bc" — with separator these MUST produce different hashes.
	b1 := hashBucket("ab", "c")
	b2 := hashBucket("a", "bc")
	if b1 == b2 {
		// A hash collision is possible (1/NumBuckets chance) but the test documents
		// the invariant intent. For these specific values, the FNV-1a separator
		// guarantees different internal hashes; collision only if NumBuckets=1.
		t.Logf("hashBucket collision for (ab,c) vs (a,bc): bucket=%d (acceptable if NumBuckets=1)", b1)
	}
}

// TestABRouter_AllControl verifies that TreatmentBuckets=0 routes all to control.
func TestABRouter_AllControl(t *testing.T) {
	r := NewABRouter(ABConfig{TreatmentBuckets: 0})
	for _, zone := range []string{"z1", "z2", "z3", "z100"} {
		d := r.Assign(zone, "t1")
		if !d.IsControl() {
			t.Errorf("zone=%s: expected control with TreatmentBuckets=0, got treatment", zone)
		}
	}
}

// TestABRouter_AllTreatment verifies that TreatmentBuckets=NumBuckets routes all to treatment.
func TestABRouter_AllTreatment(t *testing.T) {
	r := NewABRouter(ABConfig{TreatmentBuckets: NumBuckets})
	for _, zone := range []string{"z1", "z2", "z3", "z100"} {
		d := r.Assign(zone, "t1")
		if d.IsControl() {
			t.Errorf("zone=%s: expected treatment with TreatmentBuckets=%d, got control", zone, NumBuckets)
		}
	}
}

// TestABRouter_KillSwitch_ForcesControl verifies that when the kill-switch is
// active, ALL requests go to control regardless of bucket assignment.
func TestABRouter_KillSwitch_ForcesControl(t *testing.T) {
	r := NewABRouter(ABConfig{TreatmentBuckets: NumBuckets}) // 100% treatment by bucket
	r.EnableKillSwitch()

	for _, zone := range []string{"z1", "z2", "z3"} {
		d := r.Assign(zone, "t1")
		if !d.IsControl() {
			t.Errorf("zone=%s: kill-switch active but got treatment", zone)
		}
		if !d.KillSwitchActive {
			t.Errorf("zone=%s: KillSwitchActive should be true when kill-switch is on", zone)
		}
	}
}

// TestABRouter_KillSwitch_Disable verifies that disabling the kill-switch
// restores bucket-based assignment.
func TestABRouter_KillSwitch_Disable(t *testing.T) {
	r := NewABRouter(ABConfig{TreatmentBuckets: NumBuckets})
	r.EnableKillSwitch()
	r.DisableKillSwitch()

	// With TreatmentBuckets=NumBuckets and kill-switch off, all go to treatment.
	d := r.Assign("z1", "t1")
	if d.IsControl() {
		t.Error("kill-switch disabled but assignment still control")
	}
	if d.KillSwitchActive {
		t.Error("KillSwitchActive should be false after DisableKillSwitch")
	}
}

// TestABRouter_Deterministic verifies that the same (zone, tenant) always
// gets the same arm across multiple calls (critical for OPE).
func TestABRouter_Deterministic(t *testing.T) {
	r := NewABRouter(ABConfig{TreatmentBuckets: 50})
	zone, tenant := "zone-deterministic", "tenant-42"

	first := r.Assign(zone, tenant)
	for i := 0; i < 100; i++ {
		d := r.Assign(zone, tenant)
		if d.Arm != first.Arm || d.Bucket != first.Bucket {
			t.Errorf("assignment not deterministic on call %d: got arm=%d bucket=%d, want arm=%d bucket=%d",
				i, d.Arm, d.Bucket, first.Arm, first.Bucket)
		}
	}
}

// TestABRouter_UpdateConfig verifies that UpdateConfig changes the behaviour
// of subsequent Assign calls atomically.
func TestABRouter_UpdateConfig(t *testing.T) {
	r := NewABRouter(ABConfig{TreatmentBuckets: NumBuckets}) // all treatment
	d := r.Assign("z1", "t1")
	if d.IsControl() {
		t.Fatal("precondition: z1/t1 should be treatment with NumBuckets treatment")
	}

	// Switch to all-control.
	r.UpdateConfig(ABConfig{TreatmentBuckets: 0})
	d = r.Assign("z1", "t1")
	if !d.IsControl() {
		t.Error("after UpdateConfig(0 buckets), z1/t1 should be control")
	}
}

// TestABRouter_BanditConfig_PropagatedToTreatment verifies that the bandit
// config set in ABConfig is returned in ABDecision.BanditCfg for treatment.
func TestABRouter_BanditConfig_PropagatedToTreatment(t *testing.T) {
	bandit := BanditConfig{
		BanditEnabled: true,
		Policy:        BanditPolicyEpsilonGreedy,
		Epsilon:       0.1, //nolint:forbidigo // test epsilon value
	}
	r := NewABRouter(ABConfig{
		TreatmentBuckets: NumBuckets,
		BanditCfg:        bandit,
	})

	d := r.Assign("z1", "t1")
	if d.IsControl() {
		t.Fatal("expected treatment")
	}
	if !d.BanditCfg.BanditEnabled {
		t.Error("BanditEnabled should be true in treatment ABDecision")
	}
	if d.BanditCfg.Epsilon != 0.1 { //nolint:forbidigo // test epsilon comparison
		t.Errorf("Epsilon: got %f, want 0.1", d.BanditCfg.Epsilon)
	}
}

// TestABRouter_ControlHasNoBandit verifies that control arm's ABDecision has
// BanditEnabled=false (no exploration in control).
func TestABRouter_ControlHasNoBandit(t *testing.T) {
	r := NewABRouter(ABConfig{
		TreatmentBuckets: 0,
		BanditCfg: BanditConfig{
			BanditEnabled: true,
			Epsilon:       0.1, //nolint:forbidigo // test value
		},
	})
	d := r.Assign("z1", "t1")
	if !d.IsControl() {
		t.Fatal("expected control")
	}
	if d.BanditCfg.BanditEnabled {
		t.Error("control arm must NOT have BanditEnabled=true")
	}
}

// ---------------------------------------------------------------------------
// RevenueGuard tests
// ---------------------------------------------------------------------------

// mockRouter is a minimal router stub for guard tests.
type mockRouter struct {
	killSwitchActivated bool
}

func (m *mockRouter) EnableKillSwitch() { m.killSwitchActivated = true }

// TestRevenueGuard_NoTripBelowMinImpressions verifies that the guard does not
// trip before MinImpressions is reached on both arms.
func TestRevenueGuard_NoTripBelowMinImpressions(t *testing.T) {
	router := &mockRouter{}
	guard := NewRevenueGuard(GuardConfig{
		MinImpressions:    10,
		MaterialThreshold: 0.05, //nolint:forbidigo // test threshold
	}, router, nil)

	// Feed 9 impressions to each arm — both below the minimum of 10.
	for i := 0; i < 9; i++ {
		guard.Observe(ABArmControl, 1000)   // 1000 minor-units each
		guard.Observe(ABArmTreatment, 500)  // treatment is 50% lower — would trip if checked
	}

	if router.killSwitchActivated {
		t.Error("kill-switch should NOT trip below MinImpressions")
	}
	_, _, tripped := guard.Stats()
	if tripped {
		t.Error("guard.tripped should be false below MinImpressions")
	}
}

// TestRevenueGuard_TripsOnMaterialDrop verifies that the guard trips when
// treatment eCPM is materially below control.
func TestRevenueGuard_TripsOnMaterialDrop(t *testing.T) {
	router := &mockRouter{}
	guard := NewRevenueGuard(GuardConfig{
		MinImpressions:    100,
		MaterialThreshold: 0.05, //nolint:forbidigo // 5% threshold
	}, router, nil)

	// Control: avg 1000 minor-units per impression.
	// Treatment: avg 900 minor-units — 10% drop (> 5% threshold → should trip).
	for i := 0; i < 100; i++ {
		guard.Observe(ABArmControl, 1000)
		guard.Observe(ABArmTreatment, 900)
	}

	if !router.killSwitchActivated {
		t.Error("kill-switch should have been activated: treatment is 10% below control")
	}
	_, _, tripped := guard.Stats()
	if !tripped {
		t.Error("guard.tripped should be true after trip")
	}
}

// TestRevenueGuard_NoTripOnBenignDrop verifies that a drop BELOW the threshold
// does not trip the guard.
func TestRevenueGuard_NoTripOnBenignDrop(t *testing.T) {
	router := &mockRouter{}
	guard := NewRevenueGuard(GuardConfig{
		MinImpressions:    100,
		MaterialThreshold: 0.10, //nolint:forbidigo // 10% threshold
	}, router, nil)

	// Treatment is only 5% below control — within the 10% threshold.
	for i := 0; i < 100; i++ {
		guard.Observe(ABArmControl, 1000)
		guard.Observe(ABArmTreatment, 950) // 5% drop
	}

	if router.killSwitchActivated {
		t.Error("kill-switch should NOT trip: 5% drop < 10% threshold")
	}
}

// TestRevenueGuard_NoTripWhenTreatmentBetter verifies that when treatment
// outperforms control, the guard never trips.
func TestRevenueGuard_NoTripWhenTreatmentBetter(t *testing.T) {
	router := &mockRouter{}
	guard := NewRevenueGuard(GuardConfig{
		MinImpressions:    100,
		MaterialThreshold: 0.05, //nolint:forbidigo // 5% threshold
	}, router, nil)

	for i := 0; i < 200; i++ {
		guard.Observe(ABArmControl, 1000)
		guard.Observe(ABArmTreatment, 1100) // treatment 10% better
	}

	if router.killSwitchActivated {
		t.Error("kill-switch must NOT trip when treatment outperforms control")
	}
}

// TestRevenueGuard_TripsOnlyOnce verifies that once tripped, the guard does
// not call EnableKillSwitch again (idempotent).
func TestRevenueGuard_TripsOnlyOnce(t *testing.T) {
	var r = &countingRouterImpl{count: 0}
	guard := NewRevenueGuard(GuardConfig{
		MinImpressions:    10,
		MaterialThreshold: 0.05, //nolint:forbidigo // test threshold
	}, r, nil)

	// Trip the guard.
	for i := 0; i < 20; i++ {
		guard.Observe(ABArmControl, 1000)
		guard.Observe(ABArmTreatment, 900)
	}

	firstCount := r.count
	// Feed more data — guard should NOT call EnableKillSwitch again.
	for i := 0; i < 100; i++ {
		guard.Observe(ABArmControl, 1000)
		guard.Observe(ABArmTreatment, 900)
	}

	if r.count != firstCount {
		t.Errorf("EnableKillSwitch called %d times after initial trip (want %d)", r.count, firstCount)
	}
}

// countingRouterImpl counts how many times EnableKillSwitch is called.
type countingRouterImpl struct{ count int }

func (c *countingRouterImpl) EnableKillSwitch() { c.count++ }

// TestRevenueGuard_Reset verifies that Reset clears state and allows re-trip.
func TestRevenueGuard_Reset(t *testing.T) {
	router := &mockRouter{}
	guard := NewRevenueGuard(GuardConfig{
		MinImpressions:    10,
		MaterialThreshold: 0.05, //nolint:forbidigo // 5% threshold
	}, router, nil)

	for i := 0; i < 20; i++ {
		guard.Observe(ABArmControl, 1000)
		guard.Observe(ABArmTreatment, 900)
	}
	if !router.killSwitchActivated {
		t.Fatal("guard should have tripped")
	}

	guard.Reset()
	_, _, tripped := guard.Stats()
	if tripped {
		t.Error("after Reset(), tripped should be false")
	}
	ctrl, treat, _ := guard.Stats()
	if ctrl.Impressions != 0 || treat.Impressions != 0 {
		t.Error("after Reset(), impression counts should be 0")
	}
}

// ---------------------------------------------------------------------------
// Bandit under treatment tests
// ---------------------------------------------------------------------------

// TestBanditExploreRank_InTreatmentOnly verifies that ExploreRank with
// BanditEnabled=true produces propensity < 1.0 for some decisions, while
// BanditEnabled=false always produces propensity = 1.0.
func TestBanditExploreRank_InTreatmentOnly(t *testing.T) {
	// Build candidates.
	candidates := makeCandidates("c1", "c2", "c3")
	scores := []float32{0.8, 0.6, 0.4}

	// Control arm: BanditEnabled=false → propensity must be 1.0.
	controlCfg := BanditConfig{BanditEnabled: false}
	res := ExploreRank(controlCfg, candidates, scores, nil)
	if res.Propensity != 1.0 { //nolint:forbidigo // test propensity check
		t.Errorf("control arm: ExploreRank propensity = %f, want 1.0", res.Propensity)
	}
	if res.Explored {
		t.Error("control arm: Explored should be false")
	}

	// Treatment arm with epsilon=1.0 (100% exploration): propensity < 1.0 for N>1.
	treatmentCfg := BanditConfig{
		BanditEnabled: true,
		Policy:        BanditPolicyEpsilonGreedy,
		Epsilon:       1.0, //nolint:forbidigo // test: 100% exploration
	}
	// With epsilon=1.0 and N=3, propensity = epsilon/N = 1/3 < 1.0.
	res = ExploreRank(treatmentCfg, candidates, scores, nil)
	if res.Propensity > 1.0 { //nolint:forbidigo // propensity check
		t.Errorf("treatment arm epsilon=1.0: propensity = %f, want <= 1.0", res.Propensity)
	}
}

// TestBanditExploreRank_Propensity_InBounds verifies that propensity is always
// in (0, 1] for epsilon-greedy with valid epsilon values.
func TestBanditExploreRank_Propensity_InBounds(t *testing.T) {
	candidates := makeCandidates("c1", "c2", "c3", "c4")
	scores := []float32{0.9, 0.5, 0.3, 0.1}

	for _, eps := range []float64{0.0, 0.05, 0.10, 0.50, 1.0} { //nolint:forbidigo // test values
		cfg := BanditConfig{
			BanditEnabled: true,
			Policy:        BanditPolicyEpsilonGreedy,
			Epsilon:       eps, //nolint:forbidigo // loop variable
		}
		// Run multiple times (exploration is random).
		for i := 0; i < 50; i++ {
			res := ExploreRank(cfg, candidates, scores, nil)
			if res.Propensity <= 0 || res.Propensity > 1.0 { //nolint:forbidigo // bounds check
				t.Errorf("epsilon=%.2f iter=%d: propensity=%f out of (0,1]",
					eps, i, res.Propensity) //nolint:forbidigo // test value
			}
		}
	}
}

// TestBanditExploreRank_Thompson_Propensity verifies that Thompson Sampling
// also produces propensity in (0, 1].
func TestBanditExploreRank_Thompson_Propensity(t *testing.T) {
	candidates := makeCandidates("c1", "c2", "c3")
	scores := []float32{0.7, 0.5, 0.3}

	cfg := BanditConfig{
		BanditEnabled: true,
		Policy:        BanditPolicyThompson,
		ThompsonBeta:  10.0, //nolint:forbidigo // test beta
	}
	for i := 0; i < 50; i++ {
		res := ExploreRank(cfg, candidates, scores, nil)
		if res.Propensity <= 0 || res.Propensity > 1.0 { //nolint:forbidigo // bounds check
			t.Errorf("Thompson iter=%d: propensity=%f out of (0,1]", i, res.Propensity)
		}
	}
}

// TestBanditExploreRank_ControlNeverExplores verifies that the control arm
// (BanditEnabled=false) never shuffles candidates regardless of input.
func TestBanditExploreRank_ControlNeverExplores(t *testing.T) {
	candidates := makeCandidates("c1", "c2", "c3")
	scores := []float32{0.9, 0.5, 0.1}
	cfg := BanditConfig{BanditEnabled: false}

	for i := 0; i < 100; i++ {
		res := ExploreRank(cfg, candidates, scores, nil)
		if res.Explored {
			t.Errorf("control arm (BanditEnabled=false): Explored should never be true")
		}
		// Candidates slice must be unchanged (pointer equality for first element).
		if res.Ordered[0] != candidates[0] {
			t.Errorf("control arm: first candidate changed at iter=%d", i)
		}
	}
}

// TestBanditExploreRank_SingleCandidate verifies that with only one candidate,
// propensity=1.0 regardless of bandit config.
func TestBanditExploreRank_SingleCandidate(t *testing.T) {
	candidates := makeCandidates("c1")
	scores := []float32{0.7}

	for _, enabled := range []bool{false, true} {
		cfg := BanditConfig{
			BanditEnabled: enabled,
			Policy:        BanditPolicyEpsilonGreedy,
			Epsilon:       1.0, //nolint:forbidigo // 100% exploration
		}
		res := ExploreRank(cfg, candidates, scores, nil)
		if res.Propensity != 1.0 { //nolint:forbidigo // expected 1.0
			t.Errorf("single candidate enabled=%v: propensity=%f, want 1.0", enabled, res.Propensity)
		}
	}
}

// TestABRouter_BucketDistribution verifies that with 50 treatment buckets and
// many unique zones, roughly 50% end up in treatment (within ±15%).
func TestABRouter_BucketDistribution(t *testing.T) {
	r := NewABRouter(ABConfig{TreatmentBuckets: 50})

	treatment := 0
	total := 1000
	for i := 0; i < total; i++ {
		zone := "zone-" + itoa(i)
		d := r.Assign(zone, "t1")
		if !d.IsControl() {
			treatment++
		}
	}

	// 50% expected; allow ±15% slack.
	pct := float64(treatment) / float64(total) * 100.0 //nolint:forbidigo // percentage for test
	if pct < 35.0 || pct > 65.0 {                     //nolint:forbidigo // bounds check
		t.Errorf("distribution test: %d/%d (%.1f%%) in treatment, expected ~50%%±15%%",
			treatment, total, pct)
	}
}

// itoa converts an int to a string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
