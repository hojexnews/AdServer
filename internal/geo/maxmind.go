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
// Hot-reload (G0/E5, DA-9):
//
//	The mmdb file is loaded at startup and kept in memory.  It CAN be
//	refreshed without a service restart via Reload(dbPath) — the caller
//	(services/collector) polls the file's mtime on a ticker and calls
//	Reload whenever the auto-updater job (§4.10) atomically replaces the
//	.mmdb file on disk.
//
//	Concurrency design: the underlying *maxminddb.Reader is backed by an
//	mmap'd file; Close() munmaps it.  We deliberately use a sync.RWMutex
//	(NOT a bare atomic.Pointer) to guard the reader field:
//	  - Resolve takes RLock for the duration of the Lookup call.
//	  - Reload takes Lock to swap in the new reader, then releases the lock
//	    and only THEN closes the old one. The close is safe even though it
//	    runs after Unlock: the swap of r.db happened under Lock, so once
//	    Lock() was granted every in-flight RLock (every in-flight Lookup on
//	    the old reader) had already returned, and no RLock taken after the
//	    swap can observe the old reader — it reads the new r.db. Hence no
//	    Lookup can still be using `old` when Close(old) runs.
//	  An atomic.Pointer alone would let a Lookup in flight keep dereferencing
//	  a Reader whose backing mmap a racing Reload just munmap'd — a classic
//	  use-after-free/munmap segfault. RWMutex rules that out by construction:
//	  Lock() cannot be granted while any RLock is outstanding, so close of
//	  the old reader can never race a Lookup that is still using it.
//	On Reload failure (bad path, corrupt file, etc.) the OLD reader is kept
//	serving unchanged — a bad refresh must never degrade a working DB to
//	empty (DA-9).

import (
	"log/slog"
	"net"
	"sync"

	"github.com/oschwald/maxminddb-golang"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
)

// MaxMindResolver resolves IPs using a MaxMind GeoLite2 mmdb database.
//
// The db field is guarded by mu: Resolve takes RLock, Reload/Close take
// Lock.  See the package-level "Hot-reload" doc above for why RWMutex
// (not atomic.Pointer) is the correct primitive here.
type MaxMindResolver struct {
	mu sync.RWMutex
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

	// RLock guards against a concurrent Reload closing (munmap'ing) the
	// reader mid-Lookup.  Multiple readers may hold RLock simultaneously;
	// Reload's Lock() blocks until all of them have released it.
	r.mu.RLock()
	db := r.db
	var record geoRecord
	var lookupErr error
	if db != nil {
		lookupErr = db.Lookup(parsed, &record)
	}
	r.mu.RUnlock()

	if db == nil || lookupErr != nil {
		// No DB loaded, or lookup failure → empty geo.
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

// Reload atomically swaps the in-memory database for the .mmdb file at
// dbPath, WITHOUT restarting the service (G0/E5, DA-9).
//
// On success, the new reader starts serving all subsequent Resolve calls
// and the old reader is closed (munmap'd) only after every Resolve call
// already in flight against it has returned (see the RWMutex design note
// at the top of this file).
//
// On failure (file missing, corrupt, wrong format, …) Reload returns the
// error and leaves the CURRENT reader serving unchanged — a bad refresh
// must never degrade a working database to empty (DA-9 degradation
// policy). The caller (typically a periodic mtime-poll loop) should log
// the error and retry on the next tick.
//
// Reload does not accept or touch any client IP; it only opens a file path
// (TX-5/DA-11 unaffected).
func (r *MaxMindResolver) Reload(dbPath string) error {
	newDB, err := maxminddb.Open(dbPath)
	if err != nil {
		return err
	}

	r.mu.Lock()
	old := r.db
	r.db = newDB
	r.mu.Unlock()

	if old != nil {
		// Safe: Lock() above only returned once every in-flight RLock
		// (every in-flight Lookup against `old`) had already released.
		// The swap has already succeeded from the caller's point of view
		// (new reader is live), so a failure to close the old mmap handle
		// is a resource-cleanup warning, not a Reload failure.
		_ = old.Close()
	}
	return nil
}

// Close releases the mmdb file handle.  Call during graceful shutdown.
func (r *MaxMindResolver) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		err := r.db.Close()
		r.db = nil
		return err
	}
	return nil
}
