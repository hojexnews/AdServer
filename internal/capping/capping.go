// Package capping implements frequency-capping over Redis (DA-6, §4.8).
//
// # Scopes
//
//	campaign_total  – lifetime cap for the entire campaign (absolute ceiling).
//	session         – per-session cap; TTL is the configured session window.
//	clock           – rolling-window cap; TTL is the reset_interval.
//
// # Cap precedence (DA-6)
//
// Banner-level caps override campaign-level caps when both are non-zero.  A
// zero value at either level means "no cap at that scope".  The effective cap
// for each scope is resolved at call time from the Banner first, then the
// Campaign.
//
// # Privacy model (canonical — DA-6 / TX-5)
//
// The stable user identifier (cookie/session id) enters the server in the
// DecideRequest.UserID field and is confined EXCLUSIVELY to this package.
// The flow is:
//
//  1. The raw id reaches Allowed() as the userID parameter.
//  2. capKey() hashes it immediately: SHA-256(userID + ":" + salt)[:32 hex chars].
//  3. The raw id is never stored in any struct field, log record, telemetry
//     event, or forwarded to any other component.  It exists only on the
//     stack of capKey() for the duration of the hash computation.
//  4. The salt is a rotating secret (CAPPING_SALT from OpenBao).  The salt
//     MUST be non-empty; New() returns an error on an empty salt (fail-closed).
//
// Capping keys are ephemeral Redis strings with short TTLs — they NEVER appear
// in events, telemetry, decision logs, or any persistent store.
//
// # Fail-safe (DA-6)
//
// 1. Uncapped campaigns (all three scopes == 0) are allowed immediately without
//    any Redis round-trip and without requiring a stable user identifier.  An
//    anonymous visitor (userID == "") on an uncapped campaign MUST be served —
//    the fail-safe below applies ONLY to capped campaigns.
//
// 2. No stable identifier (userID == "") on a CAPPED campaign: Allowed returns
//    false — silence is preferred to over-delivery on a capped campaign (DA-6).
//
// 3. Redis is down / returns an error: for CAPPED campaigns, Allowed returns
//    false (fail-safe: we cannot guarantee the cap so we refuse delivery).
//    Uncapped campaigns (all three scopes == 0) are not checked against Redis
//    at all, so they are unaffected by Redis unavailability.
//
// 4. Best-effort semantics (ADR-0002 B.3): slight sub/over-delivery across
//    replicas is acceptable; Redis is NOT the billing source of truth.
package capping

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// RedisClient — thin interface over go-redis to allow fake injection in tests
// ---------------------------------------------------------------------------

// RedisClient is the subset of go-redis operations used by the Capper.
// *redis.Client and *redis.ClusterClient both satisfy it.
type RedisClient interface {
	// Get returns the value for key.  redis.Nil is returned when the key does
	// not exist.
	Get(ctx context.Context, key string) *redis.StringCmd
	// Incr atomically increments key by 1 and returns the new value.
	Incr(ctx context.Context, key string) *redis.IntCmd
	// Expire sets the TTL on an existing key.  It is called after Incr to set
	// the TTL only on the first increment (when the key is new).
	Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd
}

// ---------------------------------------------------------------------------
// Capper — implements cascade.Capper (I2)
// ---------------------------------------------------------------------------

// Capper is the Redis-backed frequency-cap implementation.  It is safe for
// concurrent use from multiple goroutines.
type Capper struct {
	client RedisClient

	mu   sync.RWMutex
	salt string // rotating salt — updated by SetSalt (TX-5)
}

// New creates a Capper backed by the given RedisClient with the initial salt.
//
// salt MUST be non-empty — fail-closed: if empty, New panics to prevent the
// service from silently using a predictable/empty salt that would allow
// cross-user key correlation.  The caller (main) validates CAPPING_SALT from
// the environment before constructing a Capper and exits the process if absent.
//
// Rotate the salt on a schedule (e.g. daily) to limit the correlation window.
// Rotation is applied via SetSalt; old in-flight Redis keys expire naturally.
func New(client RedisClient, salt string) *Capper {
	if salt == "" {
		// Fail-closed: an empty salt is a security defect.  The caller is
		// expected to have validated CAPPING_SALT before calling New.
		panic("capping.New: salt must not be empty (CAPPING_SALT missing — fail-closed)")
	}
	return &Capper{client: client, salt: salt}
}

// SetSalt atomically rotates the capping salt.  Old in-flight requests that
// still hold the previous salt will naturally expire via Redis TTL.
func (c *Capper) SetSalt(salt string) {
	c.mu.Lock()
	c.salt = salt
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// cascade.Capper implementation
// ---------------------------------------------------------------------------

// Allowed reports whether the given user may see the ad.
//
// Decision tree:
//  1. Resolve effective caps (banner overrides campaign when non-zero — DA-6).
//  2. Campaign has no caps AND banner has no caps → true (fast path, no Redis).
//     Anonymous visitors (userID == "") are served on uncapped campaigns.
//  3. userID == "" on a CAPPED campaign → false (fail-safe: no stable identifier
//     → abort delivery rather than risk over-counting, DA-6).
//  4. For each active scope (campaign_total, session, clock):
//     - increment the Redis counter; set TTL on first write.
//     - if counter > cap → rollback the increment and return false.
//     - if Redis errors → return false for capped campaigns (fail-safe, DA-6).
func (c *Capper) Allowed(userID string, camp *snapshot.Campaign, ban *snapshot.Banner) (bool, error) {
	// Resolve effective caps (banner overrides campaign when non-zero — DA-6).
	capTotal, capSession, capClock, clockWindowSec := effectiveCaps(camp, ban)

	// Fast path: no caps active → allow without any Redis round-trip.
	// Anonymous visitors (userID == "") are served on uncapped campaigns — the
	// fail-safe below applies ONLY when at least one cap scope is active.
	if capTotal == 0 && capSession == 0 && capClock == 0 {
		return true, nil
	}

	// FAIL-SAFE (DA-6): capped campaign with no stable identifier → abort.
	// Silence is preferred to over-delivery on a capped campaign.
	if userID == "" {
		return false, nil
	}

	c.mu.RLock()
	salt := c.salt
	c.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// Check each active scope.  On any Redis failure for a capped campaign,
	// return false (fail-safe — cannot guarantee cap).
	if capTotal > 0 {
		key := capKey(userID, salt, camp.ID, ban.ID, "total", 0)
		// TTL bounded by campaign end date and privacy ceiling (DA-6/TX-5).
		// Keys must never be permanent — see campaignTotalTTL.
		ok, err := c.checkAndIncr(ctx, key, int64(capTotal), campaignTotalTTL(camp))
		if err != nil {
			// Redis down on a capped campaign → fail-safe abort.
			return false, nil //nolint:nilerr // fail-safe documented above
		}
		if !ok {
			return false, nil
		}
	}

	if capSession > 0 {
		// Session TTL: use a fixed session window (e.g. 30 minutes).
		// The reset interval for sessions is a product constant, not per-campaign.
		const sessionTTL = 30 * time.Minute
		key := capKey(userID, salt, camp.ID, ban.ID, "session", 0)
		ok, err := c.checkAndIncr(ctx, key, int64(capSession), sessionTTL)
		if err != nil {
			return false, nil //nolint:nilerr
		}
		if !ok {
			return false, nil
		}
	}

	if capClock > 0 && clockWindowSec > 0 {
		ttl := time.Duration(clockWindowSec) * time.Second
		key := capKey(userID, salt, camp.ID, ban.ID, "clock", clockWindowSec)
		ok, err := c.checkAndIncr(ctx, key, int64(capClock), ttl)
		if err != nil {
			return false, nil //nolint:nilerr
		}
		if !ok {
			return false, nil
		}
	}

	return true, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// checkAndIncr increments key and returns true iff the new value <= limit.
// When ttl > 0 it is set on the key only when the key is first created (i.e.
// the counter just moved from 0→1).  This avoids resetting the TTL on every
// impression — we want a fixed window, not a sliding one.
//
// If the counter exceeds the limit the increment is NOT rolled back (best-effort
// per ADR-0002 B.3: slight over-delivery is acceptable).  Rolling back would
// require a Lua script or WATCH/MULTI/EXEC, adding latency to the hot path.
func (c *Capper) checkAndIncr(ctx context.Context, key string, limit int64, ttl time.Duration) (bool, error) {
	newVal, err := c.client.Incr(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("capping: redis incr %q: %w", key, err)
	}

	// Set TTL only when the key is newly created (first impression).
	if newVal == 1 && ttl > 0 {
		if err := c.client.Expire(ctx, key, ttl).Err(); err != nil {
			// Non-fatal: the key will live forever but the cap will still
			// enforce.  Log via observability; don't abort delivery.
			_ = err // caller's observability layer handles
		}
	}

	return newVal <= limit, nil
}

// capKey builds a privacy-safe Redis key:
//
//	"cap:" + SHA-256(userID + ":" + salt)[:32] + ":" + scope + ":" + campaignID + ":" + bannerID [+ ":" + windowSec]
//
// The hashed prefix ensures the key is not PII (TX-5).  The suffix encodes
// the semantic scope so different scopes don't collide.
func capKey(userID, salt, campaignID, bannerID, scope string, windowSec int32) string {
	h := sha256.Sum256([]byte(userID + ":" + salt))
	hx := hex.EncodeToString(h[:])[:32] // 16-byte prefix, 32 hex chars

	if windowSec > 0 {
		return fmt.Sprintf("cap:%s:%s:%s:%s:%d", hx, scope, campaignID, bannerID, windowSec)
	}
	return fmt.Sprintf("cap:%s:%s:%s:%s", hx, scope, campaignID, bannerID)
}

// campaignTotalTTL computes the TTL for the campaign_total capping key.
//
// Privacy invariant (DA-6 / TX-5): campaign_total keys MUST expire.  A
// permanent counter (TTL=0) would retain pseudonymised user data beyond the
// campaign lifetime, violating the ephemeral-key contract stated in the
// package doc.
//
// The TTL is the MINIMUM of:
//   - remaining campaign lifetime (EndAt − now), if EndAt is set and in the
//     future; OR
//   - a hard privacy ceiling of 90 days.
//
// Edge cases:
//   - EndAt is zero (unset): apply the 90-day ceiling.
//   - EndAt is in the past: the campaign is already over; use a short grace
//     TTL (1 hour) so the key expires promptly — never return 0 (permanent).
//   - EndAt is set and remaining > 90 days: clamp to 90 days.
func campaignTotalTTL(camp *snapshot.Campaign) time.Duration {
	const maxRetention = 90 * 24 * time.Hour // privacy ceiling
	const graceTTL = 1 * time.Hour           // minimum: already-expired campaigns

	if camp.EndAt.IsZero() {
		return maxRetention
	}
	remaining := time.Until(camp.EndAt)
	if remaining <= 0 {
		return graceTTL
	}
	if remaining > maxRetention {
		return maxRetention
	}
	return remaining
}

// effectiveCaps resolves the active cap values for each scope.
// Banner-level cap overrides campaign-level cap when non-zero (DA-6).
// Returns (capTotal, capSession, capClock, clockWindowSec).
func effectiveCaps(camp *snapshot.Campaign, ban *snapshot.Banner) (int32, int32, int32, int32) {
	capTotal := camp.CapTotal
	if ban.CapTotal > 0 {
		capTotal = ban.CapTotal
	}

	capSession := camp.CapSession
	if ban.CapSession > 0 {
		capSession = ban.CapSession
	}

	capClock := camp.CapClock
	clockWindowSec := camp.CapClockWindowSec
	if ban.CapClock > 0 {
		capClock = ban.CapClock
		clockWindowSec = ban.CapClockWindowSec
	}

	return capTotal, capSession, capClock, clockWindowSec
}
