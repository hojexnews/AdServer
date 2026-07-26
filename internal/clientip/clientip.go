// Package clientip is the SOLE, shared implementation of client-IP
// extraction and the TX-5/DA-11 "resolve geo then discard the IP"
// enforcement point.
//
// It exists so every service that ever sees a raw client IP —
// services/collector (browser-facing pixel/click/asyncjs endpoints) and
// services/decision (the ad tag's direct POST /v1/decide from the browser)
// — shares EXACTLY one implementation of "how do we trust X-Forwarded-For"
// and "how do we turn an IP into a Geo without ever letting the IP escape
// this call". Before this package existed, services/collector carried its
// own private copy of this logic; keeping a second, independently
// maintained copy in services/decision is the exact "synchronized by
// comment" defect class earlier waves of this repo stopped creating —
// see git history around the 30th wave.
//
// # Privacy contract (TX-5 / DA-11)
//
// ResolveAndDiscard is the ONLY function in this package — and, by
// convention, the intended SOLE call path in either service — that reads a
// raw client IP. The IP lives in a local variable for the duration of one
// function call and is never returned, logged, stored, or forwarded; only
// the derived *commonv1.Geo (country + city — no coordinates, no PII)
// leaves the function.
package clientip

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/geo"
)

// TrustedDepthFromEnv reads TRUSTED_PROXY_DEPTH the same way in every
// service that terminates a public-facing HTTP endpoint, so the trust
// boundary configuration can never silently diverge between them.
//
// Default: 1 (one trusted proxy hop — e.g. the ingress/Cilium boundary).
// TRUSTED_PROXY_DEPTH=0 disables X-Forwarded-For entirely (RemoteAddr only).
func TrustedDepthFromEnv() int {
	depth := 1
	if v := os.Getenv("TRUSTED_PROXY_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			depth = n
		}
	}
	return depth
}

// Extract extracts the real client IP from r.
//
// Security (trusted proxies): X-Forwarded-For is honoured ONLY from the
// configured number of trusted proxy hops (trustedDepth). Headers from
// untrusted positions in the XFF chain are ignored — a client cannot spoof
// its own geo/identity by injecting extra XFF entries.
//
//	trustedDepth=1 (default): the ingress proxy appends one entry to XFF.
//	  The rightmost entry is the most-recently-added (by the trusted proxy)
//	  and is taken as the client IP. The leftmost (client-claimed) entry is
//	  NOT used when trustedDepth=1 and len(parts)>1.
//	trustedDepth=0: XFF is ignored entirely; r.RemoteAddr is used.
//
// The returned string is transient: callers MUST NOT persist, log, or
// forward it — see ResolveAndDiscard below, the actual enforcement point.
func Extract(r *http.Request, trustedDepth int) string {
	depth := trustedDepth
	if depth < 0 {
		depth = 0
	}

	if depth > 0 {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			// The trusted proxy appends to the right. With depth=N, we trust
			// the Nth-from-right entry (index len-N) as the true client IP.
			idx := len(parts) - depth
			if idx < 0 {
				idx = 0
			}
			if ip := strings.TrimSpace(parts[idx]); ip != "" {
				return ip
			}
		}
	}

	// Fall back to RemoteAddr (strip port).
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ResolveAndDiscard is the SOLE enforcement point for TX-5/DA-11 shared by
// every caller: it extracts the client IP from r, resolves it to a Geo via
// resolver, and returns ONLY the Geo.
//
// The IP variable's scope ends inside this function — it is never returned,
// stored in a field, logged, or forwarded to any other component. resolver
// itself already degrades gracefully (geo.EmptyResolver / a MaxMindResolver
// with no match both return an empty *commonv1.Geo — see internal/geo), so
// this function never errors: a missing/invalid IP or missing database
// simply yields an empty Geo, and geo-targeting rules that require a
// specific value just won't match (DA-9 degradation policy — silence, never
// a hot-path failure).
func ResolveAndDiscard(r *http.Request, trustedDepth int, resolver geo.Resolver) *commonv1.Geo {
	clientIPTransient := Extract(r, trustedDepth) // IP consumed here, discarded on return
	return resolver.Resolve(clientIPTransient)
	// clientIPTransient is now out of scope; only the returned Geo escapes
	// this function (TX-5/DA-11).
}
