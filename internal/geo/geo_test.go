package geo_test

import (
	"testing"

	"github.com/hojex/adserver/internal/geo"
)

// StubResolver tests

func TestStubResolver_ReturnsFixedGeo(t *testing.T) {
	r := geo.NewStubResolver("BR", "Sao Paulo")
	g := r.Resolve("192.0.2.1")
	if g.GetCountry() != "BR" {
		t.Errorf("country: got %q, want %q", g.GetCountry(), "BR")
	}
	if g.GetCity() != "Sao Paulo" {
		t.Errorf("city: got %q, want %q", g.GetCity(), "Sao Paulo")
	}
}

func TestStubResolver_IPDiscardedImplicitly(t *testing.T) {
	// The StubResolver ignores the ip argument entirely — the IP is never
	// stored or examined (TX-5).  Different IPs return the same stub result.
	r := geo.NewStubResolver("US", "New York")
	g1 := r.Resolve("1.2.3.4")
	g2 := r.Resolve("99.99.99.99")
	if g1.GetCountry() != g2.GetCountry() || g1.GetCity() != g2.GetCity() {
		t.Error("StubResolver should return the same geo regardless of IP")
	}
}

func TestEmptyResolver_ReturnsEmpty(t *testing.T) {
	r := geo.EmptyResolver{}
	g := r.Resolve("10.0.0.1")
	if g.GetCountry() != "" || g.GetCity() != "" {
		t.Errorf("EmptyResolver: want empty geo, got country=%q city=%q",
			g.GetCountry(), g.GetCity())
	}
}

// MaxMindResolver — no .mmdb file in test environment.
// Verifies graceful degradation to EmptyResolver.

func TestNewMaxMindResolver_EmptyPath_FallsBackGracefully(t *testing.T) {
	r := geo.NewMaxMindResolver("", nil)
	if r == nil {
		t.Fatal("NewMaxMindResolver returned nil; want a non-nil fallback resolver")
	}
	// Should return empty geo without panicking.
	g := r.Resolve("8.8.8.8")
	if g == nil {
		t.Fatal("Resolve returned nil geo")
	}
}

func TestNewMaxMindResolver_MissingFile_FallsBackGracefully(t *testing.T) {
	r := geo.NewMaxMindResolver("/nonexistent/GeoLite2-City.mmdb", nil)
	if r == nil {
		t.Fatal("expected a fallback resolver, got nil")
	}
	g := r.Resolve("1.1.1.1")
	if g == nil {
		t.Fatal("Resolve returned nil geo")
	}
	// Fallback returns empty geo — targeting rules simply won't match.
	if g.GetCountry() != "" || g.GetCity() != "" {
		t.Errorf("expected empty geo from fallback, got country=%q city=%q",
			g.GetCountry(), g.GetCity())
	}
}
