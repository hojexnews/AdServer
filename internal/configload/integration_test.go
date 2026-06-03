package configload

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

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
