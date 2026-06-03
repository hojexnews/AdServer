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
	down     bool
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{counters: make(map[string]int64)}
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
	return redis.NewBoolResult(true, nil)
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
