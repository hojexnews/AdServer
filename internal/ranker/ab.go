// ab.go — A/B assignment and experiment config for J4 (ADR-0003 §G).
//
// DESIGN CONTRACT (J4):
//
//   Deterministic assignment: a request's (zone_id, tenant_id) is mapped to a
//   bucket [0, NumBuckets) using a stable FNV-1a hash over "zone_id:tenant_id".
//   FNV-1a was chosen because it is:
//     - collision-resistant enough for O(1 000) zones/tenants
//     - allocation-free (no strings.Builder, no crypto/rand)
//     - bit-stable across Go versions (pure arithmetic)
//
//   Bucket split: buckets [0, TreatmentBuckets) → treatment; rest → control.
//   TreatmentBuckets=0 → 100% control (AB DISABLED).
//   TreatmentBuckets=NumBuckets → 100% treatment (testing only).
//
//   Control path: MUST produce bit-identical output to the pure cascade
//   (DefaultRanker). No MLRanker is called, propensity=1.0, DETERMINISTIC.
//   This is the parity-safe invariant required by the parity-golden-test-guardian.
//
//   Treatment path: MLRanker re-ranks WITHIN the stratum (DA-3 preserved).
//   The bandit (ExploreRank) runs when BanditEnabled=true. propensity and
//   exploration_policy are filled from the bandit result.
//
// Note on dinheiro (TX-2): no monetary amount is computed here. Revenue guard
// comparisons are in guard.go using int64 minor-units only.
package ranker

import (
	"sync"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

// NumBuckets is the number of hash buckets used for A/B assignment.
// 100 buckets → each bucket ≈ 1% of traffic. Sufficient for zone/tenant-level
// splits where total distinct zones is O(10_000).
const NumBuckets = 100

// ---------------------------------------------------------------------------
// ABConfig holds the live A/B experiment configuration.
//
// Zero value is safe: all traffic goes to control (TreatmentBuckets=0).
// ---------------------------------------------------------------------------

// ABConfig is the A/B experiment configuration consumed by ABRouter.
// All fields are read-only after the router is created; live updates use
// ABRouter.UpdateConfig (atomic swap).
type ABConfig struct {
	// TreatmentBuckets is the number of buckets [0, TreatmentBuckets) that
	// receive ML treatment.
	//   0               → 100% control (AB disabled, default)
	//   1…NumBuckets-1  → partial treatment
	//   NumBuckets      → 100% treatment (testing only)
	TreatmentBuckets int

	// BanditCfg is the bandit configuration for the treatment arm.
	// BanditEnabled=false → treatment uses ML ranking but no bandit exploration.
	// BanditEnabled=true → treatment uses ExploreRank (epsilon-greedy/Thompson).
	BanditCfg BanditConfig

	// KillSwitchEnabled, when true, forces ALL traffic to control regardless
	// of bucket assignment. This is the MANUAL kill-switch (immediate revert).
	// The revenue guard sets this autonomously; operators may also set it.
	KillSwitchEnabled bool
}

// ---------------------------------------------------------------------------
// ABRouter
// ---------------------------------------------------------------------------

// ABRouter performs deterministic A/B assignment for (zone_id, tenant_id)
// pairs and provides the resolved ABDecision (control vs. treatment) for the
// decision handler.
//
// The router is safe for concurrent use. Config is atomically swappable via
// UpdateConfig without restarting the decision service.
type ABRouter struct {
	mu  sync.RWMutex
	cfg ABConfig
}

// NewABRouter creates an ABRouter with the given initial configuration.
// To start with 100% control (AB off), pass ABConfig{}.
func NewABRouter(cfg ABConfig) *ABRouter {
	return &ABRouter{cfg: cfg}
}

// Config returns the current A/B configuration (atomic read).
func (r *ABRouter) Config() ABConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// UpdateConfig atomically replaces the A/B configuration.
// Safe to call concurrently with Assign.
func (r *ABRouter) UpdateConfig(cfg ABConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
}

// EnableKillSwitch atomically activates the manual kill-switch (all traffic
// → control). This is the immediate revert path. Use DisableKillSwitch to
// resume treatment after the issue is resolved.
func (r *ABRouter) EnableKillSwitch() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg.KillSwitchEnabled = true
}

// DisableKillSwitch removes the manual kill-switch (restores bucket-based
// assignment). Only call after confirming the issue that triggered the
// kill-switch is resolved.
func (r *ABRouter) DisableKillSwitch() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg.KillSwitchEnabled = false
}

// ---------------------------------------------------------------------------
// ABDecision — outcome of Assign for a single request
// ---------------------------------------------------------------------------

// ABArm is the arm (control or treatment) a request is assigned to.
type ABArm int

const (
	// ABArmControl means the request uses the pure cascade (no ML).
	// propensity = 1.0, ExplorationPolicy = DETERMINISTIC, ml_fail_open = false.
	ABArmControl ABArm = iota
	// ABArmTreatment means the request uses the ML re-ranker (within the stratum).
	// propensity and policy are filled by the ML ranker / bandit.
	ABArmTreatment
)

// ABDecision is the result of Assign for a single (zone_id, tenant_id) pair.
type ABDecision struct {
	// Arm is the assigned arm.
	Arm ABArm
	// Bucket is the hash bucket [0, NumBuckets) used for assignment.
	Bucket int
	// KillSwitchActive is true when the kill-switch forced this to control.
	KillSwitchActive bool
	// BanditCfg is the bandit configuration to use in treatment.
	// Zero value (BanditEnabled=false) when Arm==ABArmControl.
	BanditCfg BanditConfig
}

// IsControl returns true when this request should use the pure cascade.
// This is the parity-safe path: bit-identical to Fase 1 behaviour.
func (d ABDecision) IsControl() bool {
	return d.Arm == ABArmControl
}

// Assign returns the ABDecision for a (zone_id, tenant_id) pair.
//
// Assignment is deterministic: same inputs always produce the same bucket and
// arm. This is critical for OPE (the logging policy must be stable for a given
// context) and for the parity golden tests (control ≡ cascade pure).
//
// If the kill-switch is active, always returns ABArmControl with
// KillSwitchActive=true.
//
// If TreatmentBuckets == 0, always returns ABArmControl.
func (r *ABRouter) Assign(zoneID, tenantID string) ABDecision {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	// Kill-switch: fail-safe → control.
	if cfg.KillSwitchEnabled {
		return ABDecision{
			Arm:              ABArmControl,
			Bucket:           hashBucket(zoneID, tenantID),
			KillSwitchActive: true,
		}
	}

	// No treatment configured → control (AB disabled).
	if cfg.TreatmentBuckets <= 0 {
		return ABDecision{
			Arm:    ABArmControl,
			Bucket: hashBucket(zoneID, tenantID),
		}
	}

	bucket := hashBucket(zoneID, tenantID)
	if bucket < cfg.TreatmentBuckets {
		return ABDecision{
			Arm:       ABArmTreatment,
			Bucket:    bucket,
			BanditCfg: cfg.BanditCfg,
		}
	}
	return ABDecision{
		Arm:    ABArmControl,
		Bucket: bucket,
	}
}

// ---------------------------------------------------------------------------
// hashBucket — deterministic, allocation-free FNV-1a hash
// ---------------------------------------------------------------------------

// hashBucket computes a stable bucket index in [0, NumBuckets) for a
// (zone_id, tenant_id) pair using FNV-1a.
//
// The separator byte 0x00 between zone_id and tenant_id prevents collisions
// like zone="ab", tenant="c" vs. zone="a", tenant="bc".
// FNV offset and prime are the standard 64-bit values.
func hashBucket(zoneID, tenantID string) int {
	const (
		fnvOffset64 = 14695981039346656037
		fnvPrime64  = 1099511628211
	)
	h := uint64(fnvOffset64)
	for i := 0; i < len(zoneID); i++ {
		h ^= uint64(zoneID[i])
		h *= fnvPrime64
	}
	h ^= 0x00 // separator
	h *= fnvPrime64
	for i := 0; i < len(tenantID); i++ {
		h ^= uint64(tenantID[i])
		h *= fnvPrime64
	}
	return int(h % uint64(NumBuckets))
}

// ---------------------------------------------------------------------------
// atomicKillSwitch — standalone atomic kill-switch usable outside ABRouter
// (e.g., for the revenue guard to flip without holding the AB lock).
// ---------------------------------------------------------------------------

// AtomicKillSwitch is a standalone atomic boolean used by the revenue guard
// to force-activate the kill-switch when eCPM treatment diverges materially.
// It is a separate struct so the revenue guard can be unit-tested independently.
type AtomicKillSwitch struct {
	v atomic.Bool
}

// Set activates (true) or deactivates (false) the kill-switch.
func (k *AtomicKillSwitch) Set(active bool) { k.v.Store(active) }

// Active returns whether the kill-switch is currently active.
func (k *AtomicKillSwitch) Active() bool { return k.v.Load() }
