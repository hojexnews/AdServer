package capping

// This file is `package capping` (in-package, not capping_test) SPECIFICALLY
// so it can reference the unexported `incrExpireLua` constant and drive
// MemoryClient.Eval with the REAL script text — the whole point of
// TestMemoryClient_Eval_MirrorsIncrExpireLuaContract below is to prove the
// hand-written mirror matches what capping.Capper actually sends, not a
// hand-copied stand-in that could silently drift. See internal/capping/
// capping_test.go (package capping_test) for the black-box Capper/RedisClient
// contract tests this complements.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// RedisClient contract — Get / Incr / Expire
// ---------------------------------------------------------------------------

func TestMemoryClient_Get_MissingKey_ReturnsRedisNil(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()

	_, err := m.Get(context.Background(), "missing").Result()
	if !errors.Is(err, redis.Nil) {
		t.Fatalf("expected redis.Nil for missing key, got %v", err)
	}
}

func TestMemoryClient_Incr_CreatesThenIncrements(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()
	ctx := context.Background()

	v, err := m.Incr(ctx, "k").Result()
	if err != nil || v != 1 {
		t.Fatalf("first Incr: want (1, nil), got (%d, %v)", v, err)
	}
	v, err = m.Incr(ctx, "k").Result()
	if err != nil || v != 2 {
		t.Fatalf("second Incr: want (2, nil), got (%d, %v)", v, err)
	}

	got, err := m.Get(ctx, "k").Result()
	if err != nil || got != "2" {
		t.Fatalf("Get after Incr: want (\"2\", nil), got (%q, %v)", got, err)
	}
}

func TestMemoryClient_Expire_NoOpOnMissingKey(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()

	ok, err := m.Expire(context.Background(), "never-created", time.Minute).Result()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("Expire on a missing key must return false and must NOT create it (mirrors real Redis EXPIRE)")
	}
	if m.Len() != 0 {
		t.Fatalf("Expire on a missing key must not create an entry; Len()=%d", m.Len())
	}
}

func TestMemoryClient_Expire_SetsTTLOnExistingKey_ThenItExpires(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()
	ctx := context.Background()

	if _, err := m.Incr(ctx, "k").Result(); err != nil {
		t.Fatalf("Incr: %v", err)
	}
	ok, err := m.Expire(ctx, "k", 30*time.Millisecond).Result()
	if err != nil || !ok {
		t.Fatalf("Expire on existing key: want (true, nil), got (%v, %v)", ok, err)
	}

	// Still alive immediately after.
	if _, err := m.Get(ctx, "k").Result(); err != nil {
		t.Fatalf("expected key still alive right after Expire, got %v", err)
	}

	time.Sleep(80 * time.Millisecond)

	_, err = m.Get(ctx, "k").Result()
	if !errors.Is(err, redis.Nil) {
		t.Fatalf("expected redis.Nil after TTL elapsed (lazy expiration), got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Eval — mirror of incrExpireLua (the ONLY method capping.Capper actually
// calls on its hot path).
// ---------------------------------------------------------------------------

func TestMemoryClient_Eval_MirrorsIncrExpireLuaContract(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()
	ctx := context.Background()
	key := "cap:deadbeef:clock:camp-1:ban-1:3600"

	// First call: v == 1 → TTL must be set (100ms, generous margin below).
	v, err := m.Eval(ctx, incrExpireLua, []string{key}, int64(100)).Int64()
	if err != nil || v != 1 {
		t.Fatalf("first Eval: want (1, nil), got (%d, %v)", v, err)
	}

	m.mu.Lock()
	firstExpiry := m.data[key].expiresAt
	m.mu.Unlock()
	if firstExpiry.IsZero() {
		t.Fatal("DA-6/TX-5/DA-11 invariant violated: key exists with v==1 but no TTL was set on creation")
	}

	// Second call, shortly after: v == 2 → TTL must NOT be reset/extended.
	// (Fixed window, not sliding — matches the `v == 1` guard in the Lua
	// script exactly.)
	time.Sleep(5 * time.Millisecond)
	v, err = m.Eval(ctx, incrExpireLua, []string{key}, int64(100)).Int64()
	if err != nil || v != 2 {
		t.Fatalf("second Eval: want (2, nil), got (%d, %v)", v, err)
	}
	m.mu.Lock()
	secondExpiry := m.data[key].expiresAt
	m.mu.Unlock()
	if !secondExpiry.Equal(firstExpiry) {
		t.Fatalf("TTL must be fixed at creation, not extended on later increments: first=%v second=%v",
			firstExpiry, secondExpiry)
	}

	// After the fixed TTL elapses, the counter must be gone — the NEXT Eval
	// starts a fresh window at v==1 again.
	time.Sleep(120 * time.Millisecond)
	v, err = m.Eval(ctx, incrExpireLua, []string{key}, int64(100)).Int64()
	if err != nil || v != 1 {
		t.Fatalf("Eval after TTL expiry: want fresh window (1, nil), got (%d, %v)", v, err)
	}
}

func TestMemoryClient_Eval_UnsupportedScript_HardFails(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()

	_, err := m.Eval(context.Background(), "return 1", []string{"k"}).Result()
	if err == nil {
		t.Fatal("expected a hard error for an unrecognized script (silent bypass of DA-6 must never happen)")
	}
	if !strings.Contains(err.Error(), "unsupported script") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

func TestMemoryClient_Eval_WrongKeyCount_ReturnsError(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()

	_, err := m.Eval(context.Background(), incrExpireLua, []string{"a", "b"}, int64(1000)).Result()
	if err == nil {
		t.Fatal("expected error for a non-1 KEYS count")
	}
}

// TestMemoryClient_Eval_ConcurrentIncrements_NoLostUpdates drives many
// goroutines against the SAME key concurrently. Run with -race; it also
// proves atomicity by checking the final count against the exact number of
// callers (a lost update would show up as count < n).
func TestMemoryClient_Eval_ConcurrentIncrements_NoLostUpdates(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()
	ctx := context.Background()
	const key = "cap:race-key:total:camp-1:ban-1"
	const n = 200

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := m.Eval(ctx, incrExpireLua, []string{key}, int64(60000)).Int64(); err != nil {
				t.Errorf("concurrent Eval: %v", err)
			}
		}()
	}
	wg.Wait()

	got, err := m.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != strconv.Itoa(n) {
		t.Fatalf("expected counter == %d after %d concurrent increments, got %s (lost update)", n, n, got)
	}
}

// ---------------------------------------------------------------------------
// TTL reclamation — lazy expiration + bounded periodic sweep.
// ---------------------------------------------------------------------------

func TestMemoryClient_LazyExpiration_ReclaimsOnRead(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()
	ctx := context.Background()

	if _, err := m.Eval(ctx, incrExpireLua, []string{"k"}, int64(20)).Int64(); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if m.Len() != 1 {
		t.Fatalf("expected 1 entry right after creation, got %d", m.Len())
	}

	time.Sleep(60 * time.Millisecond)

	// A read (Get) must lazily reclaim the expired entry.
	if _, err := m.Get(ctx, "k").Result(); !errors.Is(err, redis.Nil) {
		t.Fatalf("expected redis.Nil, got %v", err)
	}
	if m.Len() != 0 {
		t.Fatalf("lazy expiration on read must delete the entry; Len()=%d", m.Len())
	}
}

func TestMemoryClient_PeriodicSweep_ReclaimsWithoutAnyRead(t *testing.T) {
	// Sweep interval much shorter than default so the test doesn't wait
	// a full minute for the production cadence.
	m := NewMemoryClientWithSweepInterval(15 * time.Millisecond)
	defer m.Close()
	ctx := context.Background()

	if _, err := m.Eval(ctx, incrExpireLua, []string{"k"}, int64(10)).Int64(); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if m.Len() != 1 {
		t.Fatalf("expected 1 entry right after creation, got %d", m.Len())
	}

	// Wait for the TTL (10ms) to elapse AND at least one sweep tick (15ms)
	// to run, with generous margin — WITHOUT ever calling Get/Incr/Eval
	// again, so only the background sweep (not lazy expiration) can be
	// responsible for reclamation.
	deadline := time.Now().Add(2 * time.Second)
	for m.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.Len() != 0 {
		t.Fatalf("periodic sweep did not reclaim the expired entry without a read; Len()=%d", m.Len())
	}
}

// ---------------------------------------------------------------------------
// Privacy (TX-5): no raw identifier ever appears in a stored key.
// ---------------------------------------------------------------------------

func TestMemoryClient_NoRawUserID_InStoredKeys(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()
	c := New(m, "test-salt")

	const rawUserID = "raw-user-super-secret-42"
	camp := &snapshot.Campaign{ID: "camp-priv", TenantID: "t1", CapClock: 3, CapClockWindowSec: 3600}
	ban := &snapshot.Banner{ID: "ban-priv", TenantID: "t1", CampaignID: "camp-priv", Active: true}

	if _, err := c.Allowed(rawUserID, camp, ban); err != nil {
		t.Fatalf("Allowed: %v", err)
	}

	keys := m.Keys()
	if len(keys) == 0 {
		t.Fatal("expected at least one stored key after a capped Allowed() call")
	}
	for _, k := range keys {
		if strings.Contains(k, rawUserID) {
			t.Fatalf("PRIVACY VIOLATION (TX-5): raw userID found verbatim in stored key %q", k)
		}
		if !strings.HasPrefix(k, "cap:") {
			t.Fatalf("unexpected key shape (want cap:<hash>:...): %q", k)
		}
	}
}

// ---------------------------------------------------------------------------
// End-to-end: MemoryClient reused by the REAL capping.Capper (not a fake).
// Mirrors the seed's "Contract BR" cap (clock scope, 3/hour) — see
// db/seed/dev_seed.sql.
// ---------------------------------------------------------------------------

func TestMemoryClient_Capper_ClockCap_ThirdAllowedFourthDenied(t *testing.T) {
	m := NewMemoryClient()
	defer m.Close()
	c := New(m, "test-salt")

	camp := &snapshot.Campaign{
		ID:                "camp-contract-br",
		TenantID:          "t1",
		CapClock:          3,
		CapClockWindowSec: 3600,
	}
	ban := &snapshot.Banner{ID: "ban-contract-br", TenantID: "t1", CampaignID: camp.ID, Active: true}

	for i := 1; i <= 3; i++ {
		ok, err := c.Allowed("user-beta", camp, ban)
		if err != nil {
			t.Fatalf("impression %d: unexpected error: %v", i, err)
		}
		if !ok {
			t.Fatalf("impression %d: expected allowed (cap=3), got denied", i)
		}
	}

	ok, err := c.Allowed("user-beta", camp, ban)
	if err != nil {
		t.Fatalf("impression 4: unexpected error: %v", err)
	}
	if ok {
		t.Fatal("impression 4: expected denied (cap of 3/hour exhausted), got allowed — capping not enforced")
	}
}

func TestMemoryClient_ImplementsRedisClient(t *testing.T) {
	var _ RedisClient = (*MemoryClient)(nil)
}

func TestMemoryClient_Close_IsIdempotent(t *testing.T) {
	m := NewMemoryClient()
	m.Close()
	m.Close() // must not panic
}
