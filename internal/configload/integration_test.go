package configload

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
)

// TestPostgresLoader_Integration loads the demo seed (db/seed/dev_seed.sql)
// from a real Postgres and drives the cascade end-to-end.
//
// It is skipped unless CONFIGLOAD_TEST_DSN points at a database that has had
// the config migrations + dev_roles.sql + dev_seed.sql applied.  The DSN
// should authenticate as the BYPASSRLS adserver_loader role.  See make/dev.mk
// (db-it) for the one-shot setup used in CI/local verification.
func TestPostgresLoader_Integration(t *testing.T) {
	dsn := os.Getenv("CONFIGLOAD_TEST_DSN")
	if dsn == "" {
		t.Skip("set CONFIGLOAD_TEST_DSN to run the Postgres integration test")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	loader, err := NewPostgresLoader(context.Background(), dsn, logger)
	if err != nil {
		t.Fatalf("NewPostgresLoader: %v", err)
	}
	defer loader.Close()

	snap, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(snap.Zones) == 0 || len(snap.Campaigns) == 0 || len(snap.Banners) == 0 {
		t.Fatalf("empty snapshot from seed: zones=%d campaigns=%d banners=%d",
			len(snap.Zones), len(snap.Campaigns), len(snap.Banners))
	}

	zone := findZoneByName(snap, "Sidebar 300x250")
	if zone == nil {
		t.Fatal("seed zone 'Sidebar 300x250' not found")
	}

	// BR request → contract tier wins (banner gated to BR).
	br := decideAgainst(snap, zone, "BR")
	if br.Tier != snapshot.TierContract {
		t.Errorf("BR served tier = %v, want Contract", br.Tier)
	}
	if br.Banner == nil || br.Banner.ImageURL != "https://cdn.example/contract-300x250.png" {
		t.Errorf("BR should serve the contract banner, got %+v", br.Banner)
	}

	// US request → contract banner silenced by the BR rule → remnant house.
	us := decideAgainst(snap, zone, "US")
	if us.Tier != snapshot.TierRemnant {
		t.Errorf("US served tier = %v, want Remnant", us.Tier)
	}
	if us.Banner == nil || us.Banner.ImageURL != "https://cdn.example/house-300x250.png" {
		t.Errorf("US should fall through to the house banner, got %+v", us.Banner)
	}

	// Cap from the seed loaded onto the contract campaign.
	if c := br.Campaign; c == nil || c.CapClock != 3 || c.CapClockWindowSec != 3600 {
		t.Errorf("contract clock cap = %+v, want CapClock=3 window=3600", br.Campaign)
	}
	// Rate 20.00 BRL → 2000 minor units at scale 2.
	if c := br.Campaign; c.ECPM.GetAmount() != 2000 || c.ECPM.GetScale() != 2 {
		t.Errorf("contract eCPM = %+v, want 2000/scale 2", c.ECPM)
	}
}

func findZoneByName(snap *snapshot.Snapshot, name string) *snapshot.Zone {
	for _, z := range snap.Zones {
		if z.Name == name {
			return z
		}
	}
	return nil
}

func decideAgainst(snap *snapshot.Snapshot, zone *snapshot.Zone, country string) cascade.Result {
	eng := cascade.New(rules.New())
	now := time.Now().UTC()
	return eng.Decide(cascade.Request{
		ZoneID:   zone.ID,
		TenantID: zone.TenantID,
		Rules: &rules.Context{
			Geo:         &commonv1.Geo{Country: country},
			RequestTime: now,
		},
		RequestTime: now,
	}, snap)
}

// ---------------------------------------------------------------------------
// Delivered counts (DA-4 pacing input; perfil BETA / ADR-0005) — real
// Postgres end-to-end proof.
// ---------------------------------------------------------------------------

// TestPostgresLoader_Integration_DeliveredCounts proves, against a REAL
// Postgres, that after impressions/clicks/conversions are recorded in
// stats.events_raw (the perfil BETA Postgres telemetry sink written by
// internal/telemetry/pgsink — ADR-0005, db/stats/migrations/
// 0001_stats_schema_up.sql), the snapshot PostgresLoader.Load builds carries
// DeliveredImpressions/Clicks/Conversions > 0 for the campaign that received
// events, and exactly 0 for a sibling campaign that received none — closing
// the gap the coordinator identified (DA-4 pacing was permanently inert
// because nothing in production ever populated these fields).
//
// Gated by TWO env vars — this is deliberately independent of
// TestPostgresLoader_Integration's single-DSN gate, because seeding
// synthetic stats.events_raw rows needs a role that can bypass RLS/tenant
// scoping, which the read-only adserver_loader role must NOT have:
//
//	CONFIGLOAD_TEST_DSN       — same adserver_loader (read-only, BYPASSRLS)
//	                            DSN as TestPostgresLoader_Integration.
//	CONFIGLOAD_STATS_SEED_DSN — an ADMIN/superuser DSN used ONLY by this test
//	                            to seed synthetic stats.events_raw rows
//	                            directly, mirroring make/dev.mk's
//	                            DEV_ADMIN_DSN. This is a TEST fixture writer,
//	                            never internal/telemetry/pgsink's production
//	                            write path (that role, adserver_stats_writer,
//	                            is intentionally NOBYPASSRLS/INSERT-only).
//
// Both must be set or the test is skipped: this is the "pending infra" case
// the task explicitly allows for — the CODE is production-ready
// (loadDelivered, Assemble's matching loop, this test); whether it actually
// RUNS depends on a given database having db/stats/migrations/0001 applied
// AND adserver_loader granted USAGE/SELECT on the stats schema (see this
// package's README / make/dev.mk's adserver_app grant block, which needs
// the analogous adserver_loader grant added for this path to work outside
// a developer's ad hoc `GRANT ... TO adserver_loader`).
func TestPostgresLoader_Integration_DeliveredCounts(t *testing.T) {
	loaderDSN := os.Getenv("CONFIGLOAD_TEST_DSN")
	seedDSN := os.Getenv("CONFIGLOAD_STATS_SEED_DSN")
	if loaderDSN == "" || seedDSN == "" {
		t.Skip("set CONFIGLOAD_TEST_DSN and CONFIGLOAD_STATS_SEED_DSN to run the delivered-counts integration test " +
			"(pending infra: requires db/stats/migrations/0001 applied + adserver_loader granted SELECT on stats.*)")
	}

	ctx := context.Background()

	admin, err := pgxpool.New(ctx, seedDSN)
	if err != nil {
		t.Fatalf("connect CONFIGLOAD_STATS_SEED_DSN: %v", err)
	}
	// NOTE (real bug caught while verifying this test): t.Cleanup callbacks
	// run LIFO, AFTER the enclosing function returns — which is AFTER any
	// plain `defer` in this function body has already fired. A naive
	// `defer admin.Close()` here would close the pool BEFORE the delete
	// cleanup below ran against it, so the delete would silently fail
	// (discarded error) and leave rows behind across runs. Registering
	// admin.Close as a t.Cleanup FIRST — so it runs LAST, per LIFO — fixes
	// the ordering: the delete cleanup (registered second, runs first) still
	// has a live pool to work with.
	t.Cleanup(func() { admin.Close() })

	// Two real campaigns from the seed (db/seed/dev_seed.sql) — one gets
	// events, the other stays untouched, proving no cross-campaign leakage.
	var campaignID, otherCampaignID int64
	if err := admin.QueryRow(ctx, `SELECT id FROM config.campaigns WHERE name = 'Contract BR'`).Scan(&campaignID); err != nil {
		t.Fatalf("find seed campaign 'Contract BR' (did you run make dev-db-setup / beta-up?): %v", err)
	}
	if err := admin.QueryRow(ctx, `SELECT id FROM config.campaigns WHERE name = 'Remnant House'`).Scan(&otherCampaignID); err != nil {
		t.Fatalf("find seed campaign 'Remnant House': %v", err)
	}
	var tenantID string
	if err := admin.QueryRow(ctx,
		`SELECT tenant_id::text FROM config.campaigns WHERE id = $1`, campaignID).Scan(&tenantID); err != nil {
		t.Fatalf("read tenant_id: %v", err)
	}

	cleanup := func() {
		if _, err := admin.Exec(ctx, `DELETE FROM stats.events_raw WHERE campaign_id = $1`, campaignID); err != nil {
			t.Errorf("cleanup: delete seeded stats.events_raw rows: %v", err)
		}
	}
	cleanup() // clean slate for idempotent re-runs
	t.Cleanup(cleanup)

	// Seed 3 BILLABLE impressions (must count), 1 BLANK impression (must NOT
	// count — CA-6), 2 clicks, 1 conversion, all for campaignID.
	const insertSQL = `INSERT INTO stats.events_raw
		(event_id, event_type, tenant_id, campaign_id, billable, blank, occurred_at)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, now())`
	seedEvents := []struct {
		id       string
		evType   string
		billable bool
		blank    bool
	}{
		{"test-delivered-imp-1", "impression", true, false},
		{"test-delivered-imp-2", "impression", true, false},
		{"test-delivered-imp-3", "impression", true, false},
		{"test-delivered-imp-blank", "impression", false, true}, // CA-6: never billable
		{"test-delivered-click-1", "click", false, false},
		{"test-delivered-click-2", "click", false, false},
		{"test-delivered-conv-1", "conversion", false, false},
	}
	for _, e := range seedEvents {
		if _, err := admin.Exec(ctx, insertSQL, e.id, e.evType, tenantID, campaignID, e.billable, e.blank); err != nil {
			t.Fatalf("seed event %s: %v", e.id, err)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	loader, err := NewPostgresLoader(ctx, loaderDSN, logger)
	if err != nil {
		t.Fatalf("NewPostgresLoader: %v", err)
	}
	defer loader.Close()

	snap, err := loader.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	camp, ok := snap.Campaigns[fmt.Sprint(campaignID)]
	if !ok {
		t.Fatalf("campaign %d not found in snapshot", campaignID)
	}
	if camp.DeliveredImpressions != 3 {
		t.Errorf("DeliveredImpressions = %d, want 3 (the blank impression must NOT count — CA-6)",
			camp.DeliveredImpressions)
	}
	if camp.DeliveredClicks != 2 {
		t.Errorf("DeliveredClicks = %d, want 2", camp.DeliveredClicks)
	}
	if camp.DeliveredConversions != 1 {
		t.Errorf("DeliveredConversions = %d, want 1", camp.DeliveredConversions)
	}

	other, ok := snap.Campaigns[fmt.Sprint(otherCampaignID)]
	if !ok {
		t.Fatalf("campaign %d not found in snapshot", otherCampaignID)
	}
	if other.DeliveredImpressions != 0 || other.DeliveredClicks != 0 || other.DeliveredConversions != 0 {
		t.Errorf("campaign %d (no seeded events) should show 0 delivered, got impressions=%d clicks=%d conversions=%d",
			otherCampaignID, other.DeliveredImpressions, other.DeliveredClicks, other.DeliveredConversions)
	}
}
