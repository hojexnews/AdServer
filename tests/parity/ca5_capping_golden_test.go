// CA-5 golden tests: frequency capping §4.8 + fail-safe DA-6.
//
// Cobre: campaign_total, session, clock; banner override campanha;
// fail-safe sem userID; fail-safe Redis down; isolamento de usuários;
// rotação de salt; panic em salt vazio.
//
// Dependências: nenhuma infra externa (fakeRedis em memória).
package parity_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/capping"
	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// fakeRedis — in-memory counter store (mirrors capping_test implementation)
// Kept separate in parity package to own the golden harness independently.
// ---------------------------------------------------------------------------

var errFakeRedisDown = errors.New("redis: connection refused (fake)")

type parityFakeRedis struct {
	counters map[string]int64
	down     bool
}

func newParityFakeRedis() *parityFakeRedis {
	return &parityFakeRedis{counters: make(map[string]int64)}
}

func (f *parityFakeRedis) Get(_ context.Context, key string) *redis.StringCmd {
	if f.down {
		return redis.NewStringResult("", errFakeRedisDown)
	}
	v, ok := f.counters[key]
	if !ok {
		return redis.NewStringResult("", redis.Nil)
	}
	return redis.NewStringResult(fmt.Sprint(v), nil)
}

func (f *parityFakeRedis) Incr(_ context.Context, key string) *redis.IntCmd {
	if f.down {
		return redis.NewIntResult(0, errFakeRedisDown)
	}
	f.counters[key]++
	return redis.NewIntResult(f.counters[key], nil)
}

func (f *parityFakeRedis) Expire(_ context.Context, _ string, _ time.Duration) *redis.BoolCmd {
	if f.down {
		return redis.NewBoolResult(false, errFakeRedisDown)
	}
	return redis.NewBoolResult(true, nil)
}

// ---------------------------------------------------------------------------
// Golden file schema (CA-5) — subset used for table-driven parametric cases
// ---------------------------------------------------------------------------

type ca5CampaignSpec struct {
	ID                string `json:"id"`
	CapTotal          int32  `json:"cap_total"`
	CapSession        int32  `json:"cap_session"`
	CapClock          int32  `json:"cap_clock"`
	CapClockWindowSec int32  `json:"cap_clock_window_sec"`
}

type ca5BannerSpec struct {
	ID                string `json:"id"`
	CapTotal          int32  `json:"cap_total"`
	CapSession        int32  `json:"cap_session"`
	CapClock          int32  `json:"cap_clock"`
	CapClockWindowSec int32  `json:"cap_clock_window_sec"`
}

// ca5SimpleCase is a simple parametric case for single-user scenarios.
type ca5SimpleCase struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	CA          string          `json:"ca"`
	Sub         string          `json:"sub"`
	Input       ca5SimpleInput  `json:"input"`
	Expect      ca5SimpleExpect `json:"expect"`
}

type ca5SimpleInput struct {
	UserID    string          `json:"user_id"`
	Campaign  ca5CampaignSpec `json:"campaign"`
	Banner    ca5BannerSpec   `json:"banner"`
	RedisDown bool            `json:"redis_down"`
	Calls     int             `json:"calls"`
}

type ca5SimpleExpect struct {
	Results []bool `json:"results"`
}

// ---------------------------------------------------------------------------
// CA-5 golden test runner — simple parametric cases
// ---------------------------------------------------------------------------

func TestCA5_CappingGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("golden", "ca5_capping.json"))
	if err != nil {
		t.Fatalf("cannot read golden file: %v", err)
	}

	// We parse all cases from the JSON but only run the ones that map to
	// ca5SimpleCase structure (sub-cases with single user, sequential calls).
	// Complex cases (user isolation, salt rotation, panic) are tested below.
	var rawCases []json.RawMessage
	if err := json.Unmarshal(data, &rawCases); err != nil {
		t.Fatalf("cannot parse golden file: %v", err)
	}

	if len(rawCases) == 0 {
		t.Fatal("golden file has zero cases — gate requires at least one case")
	}

	runnable := 0
	for _, rawCase := range rawCases {
		var gc ca5SimpleCase
		if err := json.Unmarshal(rawCase, &gc); err != nil {
			continue
		}
		// Only run cases with direct call-count semantics.
		if gc.Input.Calls == 0 {
			continue
		}

		t.Run(gc.ID+"/"+gc.Sub, func(t *testing.T) {
			r := newParityFakeRedis()
			r.down = gc.Input.RedisDown

			c := capping.New(r, "parity-test-salt")

			camp := &snapshot.Campaign{
				ID:                gc.Input.Campaign.ID,
				TenantID:          "t1",
				CapTotal:          gc.Input.Campaign.CapTotal,
				CapSession:        gc.Input.Campaign.CapSession,
				CapClock:          gc.Input.Campaign.CapClock,
				CapClockWindowSec: gc.Input.Campaign.CapClockWindowSec,
			}
			ban := &snapshot.Banner{
				ID:                gc.Input.Banner.ID,
				TenantID:          "t1",
				CampaignID:        gc.Input.Campaign.ID,
				CapTotal:          gc.Input.Banner.CapTotal,
				CapSession:        gc.Input.Banner.CapSession,
				CapClock:          gc.Input.Banner.CapClock,
				CapClockWindowSec: gc.Input.Banner.CapClockWindowSec,
				Active:            true,
			}

			for i := 0; i < gc.Input.Calls; i++ {
				ok, err := c.Allowed(gc.Input.UserID, camp, ban)
				if err != nil {
					t.Fatalf("[%s] call %d: unexpected error: %v", gc.ID, i+1, err)
				}
				if i < len(gc.Expect.Results) {
					want := gc.Expect.Results[i]
					if ok != want {
						t.Errorf("[%s] call %d: got allowed=%v, want %v — %s",
							gc.ID, i+1, ok, want, gc.Description)
					}
				}
			}
		})
		runnable++
	}
	t.Logf("CA-5 golden suite: %d parametric cases run", runnable)
}

// ---------------------------------------------------------------------------
// CA-5: User isolation
// ---------------------------------------------------------------------------

func TestCA5_UserIsolation(t *testing.T) {
	r := newParityFakeRedis()
	c := capping.New(r, "parity-salt")

	camp := &snapshot.Campaign{ID: "camp-1", TenantID: "t1", CapTotal: 1}
	ban := &snapshot.Banner{ID: "ban-1", TenantID: "t1", CampaignID: "camp-1", Active: true}

	// user-A hits cap.
	okA, _ := c.Allowed("user-A", camp, ban)
	if !okA {
		t.Fatal("user-A impression 1: expected allowed")
	}

	// user-A is now capped; user-B must be unaffected.
	okB, _ := c.Allowed("user-B", camp, ban)
	if !okB {
		t.Fatal("user-B impression 1: expected allowed (independent counter per user)")
	}

	// Confirm user-A is now capped.
	okA2, _ := c.Allowed("user-A", camp, ban)
	if okA2 {
		t.Fatal("user-A impression 2: expected denied (cap exhausted)")
	}
}

// ---------------------------------------------------------------------------
// CA-5: Salt rotation opens new window
// ---------------------------------------------------------------------------

func TestCA5_SaltRotation_NewWindow(t *testing.T) {
	r := newParityFakeRedis()
	c := capping.New(r, "initial-salt")

	camp := &snapshot.Campaign{ID: "camp-1", TenantID: "t1", CapTotal: 1}
	ban := &snapshot.Banner{ID: "ban-1", TenantID: "t1", CampaignID: "camp-1", Active: true}

	// First impression: allowed.
	ok, _ := c.Allowed("user-abc", camp, ban)
	if !ok {
		t.Fatal("pre-rotation: expected allowed")
	}

	// Rotate salt: key prefix changes, counter resets (old keys expire via TTL).
	c.SetSalt("rotated-salt")

	// Same user, same campaign: now allowed again (new key space).
	ok, _ = c.Allowed("user-abc", camp, ban)
	if !ok {
		t.Fatal("post-rotation: expected allowed (new key window after salt rotation)")
	}
}

// ---------------------------------------------------------------------------
// CA-5: Empty salt panics (fail-closed security invariant)
// ---------------------------------------------------------------------------

func TestCA5_EmptySalt_Panics(t *testing.T) {
	r := newParityFakeRedis()
	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic for empty salt (fail-closed DA-6), got none")
		}
	}()
	_ = capping.New(r, "") // MUST panic
}

// ---------------------------------------------------------------------------
// CA-5: Cascade integration — capped user falls to BLANK
// ---------------------------------------------------------------------------

// TestCA5_CappedUser_CascadeFallsToBlank verifies the full path: a user with an
// exhausted cap causes the cascade to skip the candidate and return BLANK.
func TestCA5_CappedUser_CascadeFallsToBlank(t *testing.T) {
	r := newParityFakeRedis()
	capper := capping.New(r, "parity-salt")
	eng := cascade.New(rules.New(), cascade.WithCapper(capper))

	camp := &snapshot.Campaign{
		ID:           "rm1",
		TenantID:     "t1",
		Tier:         snapshot.TierRemnant,
		Priority:     1,
		CapTotal:     1, // only 1 impression allowed
		BannerIDs:    []string{"ban-rm1"},
		ZoneIDs:      []string{"z1"},
		Active:       true,
		PricingModel: snapshot.PricingCPM,
	}
	ban := &snapshot.Banner{
		ID:         "ban-rm1",
		TenantID:   "t1",
		CampaignID: "rm1",
		Active:     true,
	}

	snap := snapshot.EmptySnapshot()
	snap.Zones["z1"] = &snapshot.Zone{ID: "z1", TenantID: "t1", Active: true}
	snap.Campaigns["rm1"] = camp
	snap.ZoneCampaigns["z1"] = []string{"rm1"}
	snap.Banners["ban-rm1"] = ban

	req := makeGoldenRequest()
	req.UserID = "user-alpha"

	// First impression: allowed.
	r1 := eng.Decide(req, snap)
	if r1.ServedTier == commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Error("impression 1: expected served, got BLANK")
	}

	// Second impression: cap exhausted → BLANK.
	r2 := eng.Decide(req, snap)
	if r2.ServedTier != commonv1.ServedTier_SERVED_TIER_BLANK {
		t.Errorf("impression 2: expected BLANK (cap exhausted), got %v", r2.ServedTier)
	}
}
