package configload

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/hojex/adserver/internal/snapshot"
)

// TestPostgresLoader_HojexZones_Integration is the E11 (AdServer-side) proof:
// the real first-party publisher zones provisioned by db/seed/hojex_news_seed.sql
// each resolve in the snapshot and SERVE a size-matched house creative through
// the cascade — the "cada nova zona provada (served_tier, geo)" acceptance gate.
//
// It shares CONFIGLOAD_TEST_DSN with TestPostgresLoader_Integration (skips when
// unset). `make dev-db-setup` applies BOTH dev_seed.sql (the demo tenant/zone)
// and hojex_news_seed.sql (these zones), so `make dev-it` runs both tests against
// the same database. The DSN authenticates as the BYPASSRLS adserver_loader role.
//
// The pinned zone IDs below are the SINGLE SOURCE OF TRUTH shared with the seed
// and with the site's ZONES registry (web/src/lib/ads/config.ts). If the seed
// changes an ID, this test fails loudly — which is the point.
func TestPostgresLoader_HojexZones_Integration(t *testing.T) {
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

	// The 4 real per-placement zones and the creative size each MUST serve.
	// Mirror of web/src/lib/ads/config.ts ZONES and db/seed/hojex_news_seed.sql.
	cases := []struct {
		zoneID       string
		placement    string
		width        int32
		height       int32
		wantImageURL string
	}{
		{"1001", "sidebar", 300, 250, "https://hojex.news/ad-house-300x250.svg"},
		{"1002", "in-article", 300, 250, "https://hojex.news/ad-house-300x250.svg"},
		{"1003", "leaderboard", 728, 90, "https://hojex.news/ad-house-728x90.svg"},
		{"1004", "listing", 300, 250, "https://hojex.news/ad-house-300x250.svg"},
	}

	for _, tc := range cases {
		t.Run(tc.placement, func(t *testing.T) {
			zone, ok := snap.Zones[tc.zoneID]
			if !ok || zone == nil {
				t.Fatalf("zone %q (%s) not in snapshot — hojex_news_seed.sql not applied?",
					tc.zoneID, tc.placement)
			}
			if zone.Width != tc.width || zone.Height != tc.height {
				t.Errorf("zone %s dims = %dx%d, want %dx%d",
					tc.placement, zone.Width, zone.Height, tc.width, tc.height)
			}
			if !zone.Active {
				t.Errorf("zone %s is not active", tc.placement)
			}

			// House inventory has NO geo rule → fills for every country. We prove
			// both that served_tier is REMNANT (first-party house serving) and that
			// the geo signal is threaded but does not wrongly filter the fill.
			for _, geo := range []string{"BR", "US"} {
				res := decideAgainst(snap, zone, geo)
				if res.Tier != snapshot.TierRemnant {
					t.Errorf("zone %s geo=%s served tier = %v, want Remnant",
						tc.placement, geo, res.Tier)
				}
				if res.Banner == nil {
					t.Fatalf("zone %s geo=%s served BLANK — house did not fill", tc.placement, geo)
				}
				// Size-matched creative: the cascade does not match banner↔zone
				// dimensions, so the seed's per-size campaign wiring is what makes
				// this hold. Guard it here.
				if res.Banner.Width != tc.width || res.Banner.Height != tc.height {
					t.Errorf("zone %s geo=%s served %dx%d creative, want %dx%d",
						tc.placement, geo, res.Banner.Width, res.Banner.Height, tc.width, tc.height)
				}
				if res.Banner.ImageURL != tc.wantImageURL {
					t.Errorf("zone %s geo=%s image = %q, want %q",
						tc.placement, geo, res.Banner.ImageURL, tc.wantImageURL)
				}
			}
		})
	}
}

// decideAgainst (drives the cascade for one zone+geo) and findZoneByName live
// in integration_test.go — same package (configload), shared by both tests.
