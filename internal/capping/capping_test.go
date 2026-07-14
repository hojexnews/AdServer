package capping_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hojex/adserver/internal/capping"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// fakeRedis — in-memory counter store; no network.
//
// Because go-redis returns concrete *redis.XCmd types (not interfaces), we
// wrap our state behind a struct that implements capping.RedisClient by
// building redis.NewXResult values (exported helpers in go-redis v9).
// ---------------------------------------------------------------------------

var errDown = errors.New("redis: connection refused")

type fakeRedis struct {
	counters map[string]int64
	ttls     map[string]time.Duration // key → TTL set via Expire
	down     bool
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{
		counters: make(map[string]int64),
		ttls:     make(map[string]time.Duration),
	}
}

func (f *fakeRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	if f.down {
		return redis.NewStringResult("", errDown)
	}
	v, ok := f.counters[key]
	if !ok {
		return redis.NewStringResult("", redis.Nil)
	}
	return redis.NewStringResult(fmt.Sprint(v), nil)
}

func (f *fakeRedis) Incr(ctx context.Context, key string) *redis.IntCmd {
	if f.down {
		return redis.NewIntResult(0, errDown)
	}
	f.counters[key]++
	return redis.NewIntResult(f.counters[key], nil)
}

func (f *fakeRedis) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	if f.down {
		return redis.NewBoolResult(false, errDown)
	}
	f.ttls[key] = expiration
	return redis.NewBoolResult(true, nil)
}

// Eval emulates the atomic INCR + (first-create) PEXPIRE script used by
// checkAndIncr.  args[0] is the TTL in milliseconds.  Increment and TTL happen
// together, mirroring the real script's atomicity (no untimed-key window).
func (f *fakeRedis) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	if f.down {
		return redis.NewCmdResult(nil, errDown)
	}
	key := keys[0]
	f.counters[key]++
	v := f.counters[key]
	if v == 1 && len(args) > 0 {
		var ms int64
		switch a := args[0].(type) {
		case int64:
			ms = a
		case int:
			ms = int64(a)
		}
		if ms > 0 {
			f.ttls[key] = time.Duration(ms) * time.Millisecond
		}
	}
	return redis.NewCmdResult(v, nil)
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func newCapper(r *fakeRedis) *capping.Capper {
	return capping.New(r, "test-salt")
}

func makeCamp(capTotal, capSession, capClock, clockWindowSec int32) *snapshot.Campaign {
	return &snapshot.Campaign{
		ID:                "camp-1",
		TenantID:          "t1",
		CapTotal:          capTotal,
		CapSession:        capSession,
		CapClock:          capClock,
		CapClockWindowSec: clockWindowSec,
	}
}

func makeBan(capTotal, capSession, capClock, clockWindowSec int32) *snapshot.Banner {
	return &snapshot.Banner{
		ID:                "ban-1",
		TenantID:          "t1",
		CampaignID:        "camp-1",
		CapTotal:          capTotal,
		CapSession:        capSession,
		CapClock:          capClock,
		CapClockWindowSec: clockWindowSec,
		Active:            true,
	}
}

func makeCampWithEnd(capTotal int32, endAt time.Time) *snapshot.Campaign {
	return &snapshot.Campaign{
		ID:       "camp-ttl",
		TenantID: "t1",
		CapTotal: capTotal,
		EndAt:    endAt,
	}
}

// ---------------------------------------------------------------------------
// CA-5 golden tests
// ---------------------------------------------------------------------------

// campaign_total cap limits delivery.
func TestCapper_CampaignTotal(t *testing.T) {
	r := newFakeRedis()
	c := newCapper(r)
	camp := makeCamp(2, 0, 0, 0) // total cap = 2
	ban := makeBan(0, 0, 0, 0)

	for i := 1; i <= 2; i++ {
		ok, err := c.Allowed("user-abc", camp, ban)
		if err != nil {
			t.Fatalf("impression %d: unexpected error: %v", i, err)
		}
		if !ok {
			t.Fatalf("impression %d: expected allowed, got denied", i)
		}
	}
	// Third: cap exhausted → denied.
	ok, err := c.Allowed("user-abc", camp, ban)
	if err != nil {
		t.Fatalf("impression 3: unexpected error: %v", err)
	}
	if ok {
		t.Fatal("impression 3: expected denied (cap exhausted), got allowed")
	}
}

// session cap limits delivery.
func TestCapper_SessionCap(t *testing.T) {
	r := newFakeRedis()
	c := newCapper(r)
	camp := makeCamp(0, 1, 0, 0) // session cap = 1
	ban := makeBan(0, 0, 0, 0)

	ok, err := c.Allowed("user-abc", camp, ban)
	if err != nil || !ok {
		t.Fatalf("session 1: expected allowed, got ok=%v err=%v", ok, err)
	}
	ok, err = c.Allowed("user-abc", camp, ban)
	if err != nil {
		t.Fatalf("session 2: unexpected error: %v", err)
	}
	if ok {
		t.Fatal("session 2: expected denied, got allowed")
	}
}

// clock cap limits delivery within rolling window.
func TestCapper_ClockCap(t *testing.T) {
	r := newFakeRedis()
	c := newCapper(r)
	camp := makeCamp(0, 0, 2, 3600) // clock cap = 2/hour
	ban := makeBan(0, 0, 0, 0)

	for i := 1; i <= 2; i++ {
		ok, err := c.Allowed("user-abc", camp, ban)
		if err != nil || !ok {
			t.Fatalf("clock %d: expected allowed, got ok=%v err=%v", i, ok, err)
		}
	}
	ok, _ := c.Allowed("user-abc", camp, ban)
	if ok {
		t.Fatal("clock 3: expected denied, got allowed")
	}
}

// DA-6: banner cap overrides campaign cap (stricter banner wins).
func TestCapper_BannerOverridesCampaign(t *testing.T) {
	r := newFakeRedis()
	c := newCapper(r)
	camp := makeCamp(10, 0, 0, 0) // campaign total = 10
	ban := makeBan(1, 0, 0, 0)    // banner total = 1 (stricter)

	ok, err := c.Allowed("user-abc", camp, ban)
	if err != nil || !ok {
		t.Fatalf("expected allowed, got ok=%v err=%v", ok, err)
	}
	ok, _ = c.Allowed("user-abc", camp, ban)
	if ok {
		t.Fatal("expected denied by banner override, got allowed")
	}
}

// DA-6: no stable user identifier → fail-safe silent abort.
func TestCapper_NoUserID_AbortsSilently(t *testing.T) {
	r := newFakeRedis()
	c := newCapper(r)
	camp := makeCamp(5, 0, 0, 0)
	ban := makeBan(0, 0, 0, 0)

	ok, err := c.Allowed("", camp, ban)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected deny for empty userID, got allow")
	}
}

// CA5-005b / DA-6: anonymous user (userID="") on an UNCAPPED campaign must be
// served. The fail-safe abort applies only to capped campaigns; the uncapped
// fast-path must short-circuit before the userID check so cookieless fill works.
func TestCapper_AnonymousUncapped_Allowed(t *testing.T) {
	r := newFakeRedis()
	c := newCapper(r)
	camp := makeCamp(0, 0, 0, 0) // no caps at all
	ban := makeBan(0, 0, 0, 0)

	ok, err := c.Allowed("", camp, ban)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected allow for anonymous user on uncapped campaign (cookieless fill), got deny — DA-6 fail-safe must not fire before uncapped fast-path")
	}
}

// DA-6: Redis down on a capped campaign → fail-safe deny.
func TestCapper_RedisDown_CappedCampaign_Denies(t *testing.T) {
	r := newFakeRedis()
	r.down = true
	c := newCapper(r)
	camp := makeCamp(5, 0, 0, 0)
	ban := makeBan(0, 0, 0, 0)

	ok, _ := c.Allowed("user-abc", camp, ban)
	if ok {
		t.Fatal("expected fail-safe deny when Redis is down on capped campaign")
	}
}

// Redis down on an uncapped campaign → allowed (fast path, no Redis call).
func TestCapper_RedisDown_UncappedCampaign_Allows(t *testing.T) {
	r := newFakeRedis()
	r.down = true
	c := newCapper(r)
	camp := makeCamp(0, 0, 0, 0)
	ban := makeBan(0, 0, 0, 0)

	ok, err := c.Allowed("user-abc", camp, ban)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected allow for uncapped campaign (no Redis touch), got deny")
	}
}

// Different users share no state.
func TestCapper_UserIsolation(t *testing.T) {
	r := newFakeRedis()
	c := newCapper(r)
	camp := makeCamp(1, 0, 0, 0)
	ban := makeBan(0, 0, 0, 0)

	ok, _ := c.Allowed("user-A", camp, ban)
	if !ok {
		t.Fatal("user-A impression 1: expected allowed")
	}
	// user-A is now capped; user-B is unaffected.
	ok, _ = c.Allowed("user-B", camp, ban)
	if !ok {
		t.Fatal("user-B impression 1: expected allowed (independent counter)")
	}
}

// SetSalt changes the key prefix so old and new salt windows don't collide.
func TestCapper_SaltRotation(t *testing.T) {
	r := newFakeRedis()
	c := newCapper(r)
	camp := makeCamp(1, 0, 0, 0)
	ban := makeBan(0, 0, 0, 0)

	ok, _ := c.Allowed("user-abc", camp, ban)
	if !ok {
		t.Fatal("pre-rotation: expected allowed")
	}
	// After salt rotation the counter key changes → allow again.
	c.SetSalt("new-salt")
	ok, _ = c.Allowed("user-abc", camp, ban)
	if !ok {
		t.Fatal("post-rotation: expected allowed (new key window)")
	}
}

// Security #4 / privacy: empty salt panics at construction (fail-closed).
// New() must never silently accept an empty salt.
func TestNew_EmptySalt_Panics(t *testing.T) {
	r := newFakeRedis()
	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic for empty salt (fail-closed), got none")
		}
	}()
	// This must panic — not silently use a default salt.
	_ = capping.New(r, "")
}

// DA-6 / TX-5: campaign_total counter must never be permanent (TTL=0).
// The key must receive a positive Expire after the first impression.
func TestCapper_CampaignTotal_TTLIsPositive(t *testing.T) {
	tests := []struct {
		name    string
		endAt   time.Time
		wantMin time.Duration
		wantMax time.Duration
	}{
		{
			name:    "EndAt zero — applies 90-day ceiling",
			endAt:   time.Time{},
			wantMin: 89 * 24 * time.Hour,
			wantMax: 91 * 24 * time.Hour,
		},
		{
			name:    "EndAt in 7 days — bounded by campaign end",
			endAt:   time.Now().Add(7 * 24 * time.Hour),
			wantMin: 6 * 24 * time.Hour,
			wantMax: 8 * 24 * time.Hour,
		},
		{
			name:    "EndAt in 180 days — clamped to 90-day ceiling",
			endAt:   time.Now().Add(180 * 24 * time.Hour),
			wantMin: 89 * 24 * time.Hour,
			wantMax: 91 * 24 * time.Hour,
		},
		{
			name:    "EndAt in the past — grace TTL (1h), never 0",
			endAt:   time.Now().Add(-24 * time.Hour),
			wantMin: 1 * time.Second, // strictly positive
			wantMax: 2 * time.Hour,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeRedis()
			c := capping.New(r, "test-salt")
			camp := makeCampWithEnd(1, tc.endAt)
			ban := makeBan(0, 0, 0, 0)

			ok, err := c.Allowed("user-ttl", camp, ban)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !ok {
				t.Fatal("expected allowed on first impression")
			}

			// Find the total key in the TTL map — it is the only key that
			// Expire should have been called on (no session/clock cap active).
			if len(r.ttls) == 0 {
				t.Fatal("Expire was never called — campaign_total key has no TTL (permanent counter — DA-6 violation)")
			}
			for key, ttl := range r.ttls {
				if ttl == 0 {
					t.Errorf("key %q has TTL=0 (permanent) — DA-6/TX-5 violation", key)
				}
				if ttl < tc.wantMin || ttl > tc.wantMax {
					t.Errorf("key %q: TTL=%v, want [%v, %v]", key, ttl, tc.wantMin, tc.wantMax)
				}
			}
		})
	}
}

// TestCapper_NoUntimedKey_AllScopes encodes the DA-6/TX-5/DA-11 invariant that a
// pseudonymous per-user counter key can NEVER exist without a TTL, across ALL
// active cap scopes.  The prior Incr-then-Expire path could leave a permanent
// key when Expire timed out within the shared 10 ms budget (error swallowed) or
// the process crashed between the two calls; the atomic INCR+PEXPIRE closes it.
func TestCapper_NoUntimedKey_AllScopes(t *testing.T) {
	r := newFakeRedis()
	c := capping.New(r, "test-salt")
	// All three scopes active → three distinct counter keys; each MUST be timed.
	camp := makeCamp(5, 5, 5, 3600)
	ban := makeBan(0, 0, 0, 0)

	if ok, err := c.Allowed("user-x", camp, ban); err != nil || !ok {
		t.Fatalf("first impression: ok=%v err=%v", ok, err)
	}

	if len(r.counters) == 0 {
		t.Fatal("no counter keys created for a capped campaign")
	}
	for key := range r.counters {
		ttl, ok := r.ttls[key]
		if !ok || ttl <= 0 {
			t.Errorf("counter key %q has no positive TTL (permanent per-user key — DA-6/TX-5/DA-11)", key)
		}
	}
}
