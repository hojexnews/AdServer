package capping

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// MemoryClient — single-process, in-memory RedisClient (ADR-0005: perfil
// BETA de dependência única)
// ---------------------------------------------------------------------------
//
// MemoryClient exists so frequency capping (DA-6, §4.8, CA-5) can be
// GENUINELY enforced on a single-node beta deployment that has no Redis
// instance available, instead of silently degrading to cascade.NoOpCapper
// (unbounded delivery). It implements RedisClient and is handed to the SAME
// capping.Capper used in production — every invariant already proven for
// the Redis-backed path (salted key derivation, TTL bounds, scope
// precedence, fail-safe on error) is reused unchanged. This file adds a
// storage backend, NOT new capping logic.
//
// Activation is opt-in only: services/decision/cmd/decision/main.go wires
// this backend ONLY when CAPPING_BACKEND=memory is set explicitly (ADR-0005).
// Absent that variable, behavior is bit-for-bit unchanged from before this
// file existed (Redis if REDIS_ADDR is set, else NoOpCapper).
//
// # Guarantees and limitations — read before deploying
//
//  1. Single-process only. Counters live in this process's heap. Running more
//     than one decision replica behind a load balancer with this backend
//     silently UNDER-enforces caps: each replica only sees its own share of
//     traffic, so e.g. a "3/hour" cap effectively becomes "3/hour/replica".
//     This is a documented beta limitation (ADR-0005 §4), not a bug to fix
//     here — the fix, if ever needed, is Redis.
//  2. Lost on restart. There is no persistence; every counter resets to zero
//     when the process restarts. Acceptable under best-effort semantics
//     (ADR-0002 B.3: capping is not the billing source of truth) but NOT a
//     substitute for Redis outside a single-node beta.
//  3. Same privacy contract as the Redis path (TX-5): this type never sees a
//     raw user identifier. capping.Capper.Allowed hashes+salts the caller's
//     userID via capKey() BEFORE any RedisClient method is invoked, so the
//     only strings MemoryClient ever stores as keys are of the form
//     "cap:<sha256-hex-prefix>:<scope>:<campaignID>:<bannerID>[:<windowSec>]".
//     campaignID/bannerID are config identifiers, not PII.
//
// # Concurrency
//
// MemoryClient is safe for concurrent use by multiple goroutines: all state
// is guarded by a single mutex. The decision hot path calls into this type
// from many concurrent request goroutines.
//
// # TTL model
//
// Real Redis keys expire on their own; a map has no such background actor by
// default, so MemoryClient implements TWO expiry mechanisms:
//
//   - Lazy expiration on read: every Get/Incr/Expire/Eval call first checks
//     whether the entry it touches is past its expiresAt and, if so, treats
//     it as absent and deletes it. This guarantees correctness (an expired
//     counter is NEVER observed as live) even if the sweep below is delayed
//     or disabled.
//   - Bounded periodic sweep: a background goroutine wakes up every
//     sweepInterval and deletes expired entries, capped at maxSweepPerTick
//     entries per wake-up so a large key set cannot hold the lock for an
//     unbounded stretch. This exists ONLY to reclaim memory for keys that are
//     never read again after they expire (lazy expiration alone would leak
//     them forever) — it is not required for correctness, only for bounded
//     memory.
type MemoryClient struct {
	mu   sync.Mutex
	data map[string]*memEntry

	stopSweep chan struct{}
	closeOnce sync.Once
}

// memEntry is one counter and its optional expiry.
type memEntry struct {
	val       int64
	expiresAt time.Time // zero value == no TTL (never expires)
}

// expired reports whether e carries a TTL that has passed as of now.
func (e *memEntry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && !now.Before(e.expiresAt)
}

const (
	// defaultSweepInterval is the production default cadence for the
	// background reclamation sweep. It is deliberately loose — correctness
	// never depends on it (lazy expiration on read is what Capper relies on)
	// — it only bounds how long a never-read-again expired key lingers in
	// memory.
	defaultSweepInterval = 1 * time.Minute

	// maxSweepPerTick bounds how many map entries a single sweep tick will
	// inspect/delete, so the mutex is never held for an unbounded scan on a
	// large key set. Remaining expired entries are picked up on later ticks
	// (or lazily, on next read).
	maxSweepPerTick = 10000
)

// NewMemoryClient constructs a MemoryClient with the production-default
// sweep interval and starts its background reclamation goroutine. Call
// Close when the client is no longer needed (e.g. in tests) to stop that
// goroutine; production callers (main.go) hold it for the process lifetime
// and do not need to call Close.
func NewMemoryClient() *MemoryClient {
	return NewMemoryClientWithSweepInterval(defaultSweepInterval)
}

// NewMemoryClientWithSweepInterval is NewMemoryClient with an explicit sweep
// cadence, exposed for tests that need to observe reclamation without
// waiting a full minute. Production code should use NewMemoryClient.
func NewMemoryClientWithSweepInterval(interval time.Duration) *MemoryClient {
	m := &MemoryClient{
		data:      make(map[string]*memEntry),
		stopSweep: make(chan struct{}),
	}
	go m.sweepLoop(interval)
	return m
}

// Close stops the background sweep goroutine. Idempotent; safe to call from
// multiple goroutines and safe to call more than once.
func (m *MemoryClient) Close() {
	m.closeOnce.Do(func() { close(m.stopSweep) })
}

// Len reports the current number of stored entries, INCLUDING any that are
// expired but not yet reclaimed by lazy expiration or the sweep. It exists
// for tests/observability only — never called from the capping hot path.
func (m *MemoryClient) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.data)
}

// Keys returns a snapshot of currently stored key strings. Exists for
// tests/observability only (e.g. to prove no raw identifier ever appears in
// a stored key — TX-5). Never called from the capping hot path.
func (m *MemoryClient) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.data))
	for k := range m.data {
		out = append(out, k)
	}
	return out
}

func (m *MemoryClient) sweepLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			m.sweepExpired()
		case <-m.stopSweep:
			return
		}
	}
}

// sweepExpired deletes up to maxSweepPerTick expired entries. Map iteration
// order in Go is randomized per call, so repeated ticks sample different
// entries even when the map exceeds the per-tick cap — a large expired set
// is reclaimed over a small number of ticks rather than in one unbounded
// pass.
func (m *MemoryClient) sweepExpired() {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	visited := 0
	for k, e := range m.data {
		if visited >= maxSweepPerTick {
			break
		}
		visited++
		if e.expired(now) {
			delete(m.data, k)
		}
	}
}

// getLocked returns the live (non-expired) entry for key, or nil if absent
// or expired — deleting the entry in the latter case (lazy expiration).
// Caller MUST hold m.mu.
func (m *MemoryClient) getLocked(key string, now time.Time) *memEntry {
	e, ok := m.data[key]
	if !ok {
		return nil
	}
	if e.expired(now) {
		delete(m.data, key)
		return nil
	}
	return e
}

// ---------------------------------------------------------------------------
// RedisClient implementation
// ---------------------------------------------------------------------------

// Get returns the value for key, or redis.Nil if the key does not exist (or
// has expired) — capping.Capper does not currently call Get on its hot path
// (checkAndIncr uses Eval exclusively) but the RedisClient contract requires
// it, and this mirrors real Redis GET semantics exactly, including the
// redis.Nil sentinel error that callers are documented to depend on.
func (m *MemoryClient) Get(_ context.Context, key string) *redis.StringCmd {
	now := time.Now()
	m.mu.Lock()
	e := m.getLocked(key, now)
	m.mu.Unlock()
	if e == nil {
		return redis.NewStringResult("", redis.Nil)
	}
	return redis.NewStringResult(strconv.FormatInt(e.val, 10), nil)
}

// Incr atomically increments key by 1, creating it at 1 (with no TTL) if
// absent, and returns the new value — mirroring real Redis INCR. Not called
// by capping.Capper's hot path today (Eval does INCR+PEXPIRE atomically) but
// implemented faithfully to satisfy the RedisClient contract for any caller
// that does use it directly (e.g. tests, future diagnostics).
func (m *MemoryClient) Incr(_ context.Context, key string) *redis.IntCmd {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.getLocked(key, now)
	if e == nil {
		e = &memEntry{}
		m.data[key] = e
	}
	e.val++
	return redis.NewIntResult(e.val, nil)
}

// Expire sets a TTL on an EXISTING key only, mirroring real Redis EXPIRE
// (returns false, no-op, when the key does not exist — it never CREATES a
// key). A non-positive expiration deletes the key immediately, matching
// Redis's "EXPIRE with ttl<=0 deletes the key" behavior. Not called by
// capping.Capper's hot path today (Eval sets TTL atomically with the
// increment) but implemented faithfully for the same reason as Incr above.
func (m *MemoryClient) Expire(_ context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.getLocked(key, now)
	if e == nil {
		return redis.NewBoolResult(false, nil)
	}
	if expiration <= 0 {
		delete(m.data, key)
		return redis.NewBoolResult(true, nil)
	}
	e.expiresAt = now.Add(expiration)
	return redis.NewBoolResult(true, nil)
}

// errUnmirroredScript is returned when Eval is called with any script other
// than incrExpireLua — see the Eval doc comment below for why this is a
// hard error rather than a silent no-op.
var errUnmirroredScript = errors.New("capping: MemoryClient.Eval: unsupported script (only incrExpireLua is mirrored)")

// Eval is a HAND-WRITTEN MIRROR of incrExpireLua (capping.go), NOT a Lua
// interpreter. capping.Capper.checkAndIncr is the ONLY production caller of
// RedisClient.Eval, and it always invokes it as:
//
//	Eval(ctx, incrExpireLua, []string{key}, ttl.Milliseconds())
//
// incrExpireLua's contract, reproduced here so the two can be compared at a
// glance:
//
//	local v = redis.call('INCR', KEYS[1])
//	if v == 1 and tonumber(ARGV[1]) > 0 then
//	  redis.call('PEXPIRE', KEYS[1], ARGV[1])
//	end
//	return v
//
// i.e.: increment KEYS[1]; if — and ONLY if — this increment just CREATED
// the key (v == 1) and a positive TTL was requested, set that TTL; return
// the new value either way. Crucially, a later increment (v > 1) does NOT
// touch the TTL — the window is fixed, not sliding, and this mirror
// reproduces that exactly (see the `v == 1` guard below).
//
// # Why hard-fail on any other script instead of trying to interpret it
//
// Silently no-op'ing (or worse, guessing) on an unrecognized script would
// convert a programming error in THIS package into a silent capping bypass
// — the exact failure mode DA-6 exists to prevent. Because MemoryClient is
// only ever constructed and driven by this package's own code (main.go
// passes it straight into capping.New), the only way Eval ever sees a
// different `script` argument is if incrExpireLua itself is edited without
// updating this function, or a caller entirely outside this package's
// intended use misuses the RedisClient interface directly. Either way, a
// loud error is strictly safer than an unverified guess.
//
// # Maintenance invariant — READ THIS BEFORE EDITING incrExpireLua
//
// If incrExpireLua's Lua source ever changes, THIS FUNCTION MUST CHANGE TO
// MATCH. Nothing enforces that pairing at compile time; the two are coupled
// only by this comment and by TestMemoryClient_Eval_MirrorsCapperContract
// (memory_test.go), which drives capping.Capper end-to-end against a
// MemoryClient and would fail if the mirror drifted from the script's
// observable behavior.
//
// # Atomicity
//
// The whole operation — read-or-create, increment, conditional TTL set — runs
// under m.mu as one critical section, so concurrent callers observe it as
// indivisible, matching the atomicity a single-threaded Redis gives the real
// Lua script. In particular the counter is NEVER visible to another
// goroutine in a state where it was just created (v == 1) but its TTL has
// not yet been applied — the DA-6/TX-5/DA-11 "no counter without a TTL"
// invariant holds here exactly as it does against real Redis.
func (m *MemoryClient) Eval(_ context.Context, script string, keys []string, args ...any) *redis.Cmd {
	if script != incrExpireLua {
		return redis.NewCmdResult(nil, errUnmirroredScript)
	}
	if len(keys) != 1 {
		return redis.NewCmdResult(nil, fmt.Errorf(
			"capping: MemoryClient.Eval: incrExpireLua expects exactly 1 key, got %d", len(keys)))
	}

	var ttlMillis int64
	if len(args) > 0 {
		switch a := args[0].(type) {
		case int64:
			ttlMillis = a
		case int:
			ttlMillis = int64(a)
		default:
			return redis.NewCmdResult(nil, fmt.Errorf(
				"capping: MemoryClient.Eval: unsupported ARGV[1] type %T (want int64 milliseconds)", args[0]))
		}
	}

	key := keys[0]
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	e := m.getLocked(key, now)
	if e == nil {
		e = &memEntry{}
		m.data[key] = e
	}
	e.val++
	v := e.val
	if v == 1 && ttlMillis > 0 {
		e.expiresAt = now.Add(time.Duration(ttlMillis) * time.Millisecond)
	}
	return redis.NewCmdResult(v, nil)
}

// compile-time assertion: MemoryClient satisfies RedisClient.
var _ RedisClient = (*MemoryClient)(nil)
