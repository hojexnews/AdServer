package geo

// MaxMindResolver resolves IPs to Geo using MaxMind GeoLite2 City database
// loaded entirely in memory (DA-9).
//
// PRIVACY CONTRACT (TX-5 / DA-11):
//   The IP address is consumed by Resolve and MUST be discarded by the caller
//   immediately after the call returns.  Only the derived *commonv1.Geo is
//   propagated; the IP is never stored, logged, or forwarded.
//
// Degradation policy:
//   - If the .mmdb file is absent or fails to open: the constructor falls back
//     to EmptyResolver and logs the error.  Targeting rules that require geo
//     simply won't match (open rule semantics for missing geo).
//   - If an IP cannot be parsed or is not in the database: Resolve returns an
//     empty Geo (country="" city="").  The cascade continues; geo-targeting
//     rules that require a specific value will not match.
//
// The mmdb file is loaded once at startup and kept in memory.  Refreshing
// (e.g. weekly) requires restarting the service or hot-reloading via an
// os.Signal handler (not implemented in I2; acceptable for MVP).

import (
	"log/slog"
	"net"

	"github.com/oschwald/maxminddb-golang"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
)

// MaxMindResolver resolves IPs using a MaxMind GeoLite2 mmdb database.
type MaxMindResolver struct {
	db *maxminddb.Reader
}

// geoRecord is the struct decoded from a GeoLite2-City database lookup.
// We only decode the minimal fields needed for targeting (country + city).
type geoRecord struct {
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

// NewMaxMindResolver opens the GeoLite2 mmdb file at dbPath and loads it
// into memory.
//
// If dbPath is empty or the file cannot be opened, the function returns an
// EmptyResolver (not an error), so callers can proceed without geo data.
// The returned logger warning is sufficient for ops observability.
func NewMaxMindResolver(dbPath string, logger *slog.Logger) Resolver {
	if dbPath == "" {
		if logger != nil {
			logger.Warn("geo: MaxMind dbPath is empty; falling back to EmptyResolver (DA-9)")
		}
		return EmptyResolver{}
	}

	db, err := maxminddb.Open(dbPath)
	if err != nil {
		if logger != nil {
			logger.Warn("geo: cannot open MaxMind database; falling back to EmptyResolver (DA-9)",
				"path", dbPath, "err", err)
		}
		return EmptyResolver{}
	}

	return &MaxMindResolver{db: db}
}

// Resolve implements Resolver.
//
// PRIVACY (TX-5/DA-11): the ip argument is used only to look up the geo.
// It is NOT stored, forwarded, or logged anywhere in this function.
// The caller MUST discard ip immediately after this call returns.
func (r *MaxMindResolver) Resolve(ip string) *commonv1.Geo {
	// IP is consumed here and NEVER propagated further (TX-5).
	// Do NOT log, store, or forward ip.

	parsed := net.ParseIP(ip)
	if parsed == nil {
		// Invalid or missing IP → return empty geo (degraded gracefully).
		return &commonv1.Geo{}
	}

	var record geoRecord
	if err := r.db.Lookup(parsed, &record); err != nil {
		// DB lookup failure → empty geo.
		return &commonv1.Geo{}
	}

	city := record.City.Names["en"] // English name; fallback to empty string
	country := record.Country.ISOCode

	// Return only country + city — no coordinates, no postal code, no PII (TX-5).
	return &commonv1.Geo{
		Country: country,
		City:    city,
	}
	// IP is now out of scope and will be garbage-collected. It is never
	// written to any field, log, event, or persistent store. (TX-5/DA-11)
}

// Close releases the mmdb file handle.  Call during graceful shutdown.
func (r *MaxMindResolver) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}
