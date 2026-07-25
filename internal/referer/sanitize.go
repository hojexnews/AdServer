// Package referer sanitizes a page-URL / referer string for use in ad-serving
// telemetry and rule matching (TX-5/DA-11).
//
// Query strings and fragments frequently carry session tokens, user
// identifiers, UTM parameters, and other identifying information. Sanitize
// retains only the structural page location (scheme + host + path) and is
// the single shared implementation used by:
//
//   - the collector's /asyncjs handler, before the AdRequest event is built
//     (services/collector/cmd/collector/main.go);
//   - the decision service, applied defensively to the site_url field of
//     POST /v1/decide — the async ad tag calls decision directly with
//     location.href-derived data and MUST NOT be trusted to have sanitized
//     it client-side (services/decision/cmd/decision/main.go).
package referer

import (
	"net/url"
	"strings"
)

// Sanitize strips the query string and fragment from a page URL, retaining
// only scheme + host + path.
//
// If the URL cannot be parsed, does not have an absolute http/https scheme,
// or has no host, the empty string is returned — emit nothing rather than
// risk leaking a token buried in a malformed value.
func Sanitize(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// Require an absolute URL with a recognised scheme and a non-empty host.
	// Relative URLs, opaque URIs, and anything without http/https are discarded.
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" {
		return ""
	}
	// Reconstruct with only scheme + host + path (no query, no fragment).
	clean := &url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   u.Path,
	}
	return clean.String()
}
