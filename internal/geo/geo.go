// Package geo resolves an IP address to a geographic location (country +
// city) using MaxMind GeoLite2 (DA-9).
//
// PRIVACY CONTRACT (TX-5 / DA-11):
//   The IP address is an input to Resolve and MUST be discarded by the
//   caller immediately after the call returns.  The engine receives only
//   the derived *commonv1.Geo — no IP is ever stored, logged, or forwarded
//   down the decision pipeline.  The HTTP handler in services/decision is
//   responsible for this discard; this package enforces the contract by
//   accepting a plain string and returning only the derived struct.
//
// Stub implementation is provided for I0 (no .mmdb file required).
// The production implementation wraps oschwald/geoip2-golang and is wired
// in I2 by the platform-infra-engineer.
package geo

import (
	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
)

// Resolver resolves a raw IP string to a Geo value.
// The IP is consumed here and MUST NOT be propagated further (TX-5).
type Resolver interface {
	// Resolve returns the Geo derived from ip.
	// On any error (parse failure, DB miss) it returns an empty Geo —
	// callers treat a missing geo as "unknown" and proceed; targeting
	// rules that require a specific geo simply won't match.
	Resolve(ip string) *commonv1.Geo
}

// ---------------------------------------------------------------------------
// StubResolver — returns a configurable fixed Geo (I0 / tests)
// ---------------------------------------------------------------------------

// StubResolver is a deterministic, zero-dependency implementation of
// Resolver suitable for golden tests and the I0 service stub.
type StubResolver struct {
	country string
	city    string
}

// NewStubResolver creates a resolver that always returns the given country
// and city codes.  Pass empty strings to simulate a geo-lookup failure.
func NewStubResolver(country, city string) *StubResolver {
	return &StubResolver{country: country, city: city}
}

// Resolve implements Resolver.  The ip argument is intentionally ignored —
// emphasising that the IP is discarded immediately (TX-5).
func (r *StubResolver) Resolve(_ string) *commonv1.Geo {
	// IP is NOT stored, forwarded, or logged.
	return &commonv1.Geo{
		Country: r.country,
		City:    r.city,
	}
}

// EmptyResolver always returns an empty Geo.  Use when no geo DB is
// available and all targeting rules that require geo should simply not fire.
type EmptyResolver struct{}

func (EmptyResolver) Resolve(_ string) *commonv1.Geo {
	return &commonv1.Geo{}
}
