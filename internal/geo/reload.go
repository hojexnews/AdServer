package geo

// This file is the SOLE implementation of the GeoLite2 hot-reload polling
// loop (G0/E5, DA-9). It originally lived as a private copy inside
// services/collector/cmd/collector/main.go; it is extracted here so that
// every service wiring a *MaxMindResolver (collector, decision) calls this
// SAME function instead of maintaining an independent, "synchronized by
// comment" copy — the defect class earlier waves of this repo (30th wave)
// stopped creating.

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// ReloadIntervalFromEnv reads GEOIP_RELOAD_INTERVAL (a Go duration string,
// e.g. "1h") and falls back to 1 hour on absence or parse error. Shared by
// every service that hot-reloads a MaxMindResolver so the cadence is
// configured identically everywhere.
func ReloadIntervalFromEnv() time.Duration {
	const def = time.Hour
	v := os.Getenv("GEOIP_RELOAD_INTERVAL")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// RunReloader polls dbPath's mtime on a fixed interval and hot-swaps the
// in-memory GeoLite2 database (via mm.Reload) whenever the file has been
// atomically replaced on disk by the auto-update job (§4.10). This is the
// substrate DA-9 relies on to keep GeoLite2 fresh without manual
// intervention and without a service restart (G0/E5).
//
// mm.Reload already serialises against concurrent Resolve calls internally
// (RWMutex) — this loop only decides *when* to call it, based on mtime. On
// a failed reload the previous database keeps serving (see Reload's doc);
// the next tick retries since lastMod is only advanced on success.
//
// RunReloader blocks until ctx is cancelled; callers run it in its own
// goroutine (`go geo.RunReloader(...)`).
//
// This function never sees or logs a client IP; it only stats/opens a
// config file path on local disk (TX-5/DA-11 unaffected).
func RunReloader(ctx context.Context, mm *MaxMindResolver, dbPath string, interval time.Duration, logger *slog.Logger) {
	var lastMod time.Time
	if fi, err := os.Stat(dbPath); err == nil {
		lastMod = fi.ModTime()
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fi, err := os.Stat(dbPath)
			if err != nil {
				logger.Warn("geo: reload check skipped — cannot stat mmdb file", "path", dbPath, "err", err)
				continue
			}
			if !fi.ModTime().After(lastMod) {
				continue // unchanged since the last successful reload
			}
			if err := mm.Reload(dbPath); err != nil {
				logger.Warn("geo: hot-reload failed; keeping previous database in memory (DA-9)",
					"path", dbPath, "err", err)
				continue // mtime still stale here, so the next tick retries
			}
			lastMod = fi.ModTime()
			logger.Info("geo: hot-reloaded GeoLite2 database (no restart)", "path", dbPath)
		}
	}
}
