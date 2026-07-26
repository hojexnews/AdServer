package clientip_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hojex/adserver/internal/clientip"
	"github.com/hojex/adserver/internal/geo"
)

// ---------------------------------------------------------------------------
// Extract — trusted-proxy-depth semantics (must match services/collector's
// pre-extraction behavior exactly; see collector's own extractClientIP
// tests in services/collector/cmd/collector/main_test.go, which now
// exercise this SAME function indirectly via delegation).
// ---------------------------------------------------------------------------

func TestExtract_TrustedDepth1_UsesRightmostXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.99:5555" // the trusted proxy's own socket
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1")

	got := clientip.Extract(req, 1)
	if got != "10.0.0.1" {
		t.Errorf("trustedDepth=1: expected rightmost entry '10.0.0.1', got %q", got)
	}
}

func TestExtract_TrustedDepth0_IgnoresXFF_UsesRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.5:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1")

	got := clientip.Extract(req, 0)
	if got != "192.168.1.5" {
		t.Errorf("trustedDepth=0: expected RemoteAddr '192.168.1.5', got %q", got)
	}
}

func TestExtract_NoXFF_FallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "8.8.8.8:9999"

	got := clientip.Extract(req, 1)
	if got != "8.8.8.8" {
		t.Errorf("expected RemoteAddr fallback '8.8.8.8', got %q", got)
	}
}

func TestTrustedDepthFromEnv_DefaultAndOverride(t *testing.T) {
	if got := clientip.TrustedDepthFromEnv(); got != 1 {
		t.Fatalf("default: want 1, got %d", got)
	}

	t.Setenv("TRUSTED_PROXY_DEPTH", "0")
	if got := clientip.TrustedDepthFromEnv(); got != 0 {
		t.Fatalf("override=0: want 0, got %d", got)
	}

	t.Setenv("TRUSTED_PROXY_DEPTH", "3")
	if got := clientip.TrustedDepthFromEnv(); got != 3 {
		t.Fatalf("override=3: want 3, got %d", got)
	}

	// Invalid/negative values fall back to the default rather than
	// disabling trust checks unexpectedly.
	t.Setenv("TRUSTED_PROXY_DEPTH", "-1")
	if got := clientip.TrustedDepthFromEnv(); got != 1 {
		t.Fatalf("invalid negative override: want default 1, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// ResolveAndDiscard — the TX-5/DA-11 enforcement point.
// ---------------------------------------------------------------------------

func TestResolveAndDiscard_UsesExtractedIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "8.8.8.8:9999"

	stub := geo.NewStubResolver("BR", "São Paulo")
	g := clientip.ResolveAndDiscard(req, 1, stub)
	if g.GetCountry() != "BR" || g.GetCity() != "São Paulo" {
		t.Fatalf("expected stub geo (BR, São Paulo), got (%q, %q)", g.GetCountry(), g.GetCity())
	}
}

// (b) A missing/unresolvable IP (private/loopback address, no matching
// database entry) must degrade to an empty Geo — never an error, never a
// panic (DA-9: silence, never a hot-path failure).
func TestResolveAndDiscard_EmptyResolver_NeverErrors(t *testing.T) {
	for _, remoteAddr := range []string{"127.0.0.1:1234", "10.0.0.5:80", "::1:1234"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remoteAddr

		g := clientip.ResolveAndDiscard(req, 1, geo.EmptyResolver{})
		if g == nil {
			t.Fatalf("remoteAddr=%q: expected non-nil empty Geo, got nil", remoteAddr)
		}
		if g.GetCountry() != "" || g.GetCity() != "" {
			t.Fatalf("remoteAddr=%q: expected empty Geo, got (%q, %q)", remoteAddr, g.GetCountry(), g.GetCity())
		}
	}
}
