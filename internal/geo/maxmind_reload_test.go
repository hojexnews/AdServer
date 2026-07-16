package geo_test

// This file proves G0/E5: the GeoLite2 (.mmdb) reader hot-swaps in place,
// on the SAME *geo.MaxMindResolver instance, with no service restart, and
// does so safely under concurrent Resolve/Reload traffic (DA-9).
//
// Fixtures are generated at test time with github.com/maxmind/mmdbwriter
// (pure Go, no cgo) — a TEST-ONLY dependency. Production code never imports
// mmdbwriter; only maxminddb-golang (the reader) is linked into the
// collector binary (ADR-0002 §C hermetic build is unaffected).

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"

	"github.com/hojex/adserver/internal/geo"
)

// testIP is the single address baked into both fixtures below. It must be a
// globally-routable (non-reserved) address, since mmdbwriter refuses to
// insert into reserved networks (e.g. TEST-NET-3, 203.0.113.0/24) unless
// IncludeReservedNetworks is set.
const testIP = "8.8.8.8"

// buildFixture writes a minimal GeoLite2-City-shaped .mmdb file to dir/name
// that maps testIP to the given ISO country code. It fails the test on any
// writer error — these are build-time fixtures, not the code under test.
func buildFixture(t *testing.T, dir, name, country string) string {
	t.Helper()

	tree, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType: "GeoLite2-City",
		Languages:    []string{"en"},
	})
	if err != nil {
		t.Fatalf("mmdbwriter.New: %v", err)
	}

	_, network, err := net.ParseCIDR(testIP + "/32")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}

	record := mmdbtype.Map{
		"city": mmdbtype.Map{
			"names": mmdbtype.Map{
				"en": mmdbtype.String("Test City " + country),
			},
		},
		"country": mmdbtype.Map{
			"iso_code": mmdbtype.String(country),
		},
	}

	if err := tree.Insert(network, record); err != nil {
		t.Fatalf("tree.Insert: %v", err)
	}

	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("os.Create: %v", err)
	}
	defer f.Close()

	if _, err := tree.WriteTo(f); err != nil {
		t.Fatalf("tree.WriteTo: %v", err)
	}

	return path
}

// asMaxMind type-asserts a Resolver down to *geo.MaxMindResolver, failing the
// test if the constructor fell back to EmptyResolver (which would mean the
// fixture itself failed to open — a test setup bug, not the behaviour under
// test).
func asMaxMind(t *testing.T, r geo.Resolver) *geo.MaxMindResolver {
	t.Helper()
	mm, ok := r.(*geo.MaxMindResolver)
	if !ok {
		t.Fatalf("NewMaxMindResolver did not return *geo.MaxMindResolver (got %T) — fixture failed to open?", r)
	}
	return mm
}

// (a) Swap has effect on the SAME instance, no restart.
func TestMaxMindResolver_Reload_SwapsDatabaseInPlace(t *testing.T) {
	dir := t.TempDir()
	fixtureA := buildFixture(t, dir, "a.mmdb", "BR")
	fixtureB := buildFixture(t, dir, "b.mmdb", "US")

	mm := asMaxMind(t, geo.NewMaxMindResolver(fixtureA, nil))
	defer mm.Close()

	if got := mm.Resolve(testIP).GetCountry(); got != "BR" {
		t.Fatalf("before reload: country = %q, want BR", got)
	}

	if err := mm.Reload(fixtureB); err != nil {
		t.Fatalf("Reload(B): unexpected error: %v", err)
	}

	if got := mm.Resolve(testIP).GetCountry(); got != "US" {
		t.Fatalf("after reload: country = %q, want US (hot-swap had no effect)", got)
	}
}

// (b) A failed reload must retain the previously-loaded (working) database —
// DA-9 degradation policy: never rebase a working DB to empty on a bad
// reload.
func TestMaxMindResolver_Reload_FailureRetainsPreviousDatabase(t *testing.T) {
	dir := t.TempDir()
	fixtureA := buildFixture(t, dir, "a.mmdb", "BR")
	fixtureB := buildFixture(t, dir, "b.mmdb", "US")

	mm := asMaxMind(t, geo.NewMaxMindResolver(fixtureA, nil))
	defer mm.Close()

	if err := mm.Reload(fixtureB); err != nil {
		t.Fatalf("Reload(B): unexpected error: %v", err)
	}
	if got := mm.Resolve(testIP).GetCountry(); got != "US" {
		t.Fatalf("after Reload(B): country = %q, want US", got)
	}

	err := mm.Reload(filepath.Join(dir, "does-not-exist.mmdb"))
	if err == nil {
		t.Fatal("Reload with a nonexistent path: want error, got nil")
	}

	// The old (B) database must still be serving — a bad reload never
	// degrades a working DB to empty.
	if got := mm.Resolve(testIP).GetCountry(); got != "US" {
		t.Fatalf("after failed reload: country = %q, want US (old DB should still be live)", got)
	}
}

// (c) The central proof: concurrent Resolve + Reload must never data-race
// and must never panic (use-after-close/munmap). Run with `go test -race`.
func TestMaxMindResolver_Reload_ConcurrentResolveAndReload_NoRaceNoPanic(t *testing.T) {
	dir := t.TempDir()
	fixtureA := buildFixture(t, dir, "a.mmdb", "BR")
	fixtureB := buildFixture(t, dir, "b.mmdb", "US")

	mm := asMaxMind(t, geo.NewMaxMindResolver(fixtureA, nil))
	defer mm.Close()

	const (
		numResolvers = 50
		numReloaders = 4
		duration     = 200 * time.Millisecond
	)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Readers: hammer Resolve concurrently.
	for i := 0; i < numResolvers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					got := mm.Resolve(testIP).GetCountry()
					if got != "" && got != "BR" && got != "US" {
						// Any value other than empty/BR/US would indicate
						// memory corruption — fail loudly.
						t.Errorf("Resolve returned unexpected country %q (corruption?)", got)
						return
					}
				}
			}
		}()
	}

	// Writers: alternate Reload(A)/Reload(B) concurrently with the readers
	// above and with each other.
	for i := 0; i < numReloaders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			toggle := id%2 == 0
			for {
				select {
				case <-stop:
					return
				default:
					var err error
					if toggle {
						err = mm.Reload(fixtureA)
					} else {
						err = mm.Reload(fixtureB)
					}
					if err != nil {
						t.Errorf("Reload: unexpected error: %v", err)
						return
					}
					toggle = !toggle
				}
			}
		}(i)
	}

	time.Sleep(duration)
	close(stop)
	wg.Wait()

	// Sanity: the resolver must still serve one of the two valid values
	// after the storm — proves it wasn't left in a corrupted/closed state.
	got := mm.Resolve(testIP).GetCountry()
	if got != "BR" && got != "US" {
		t.Fatalf("final Resolve: country = %q, want BR or US", got)
	}
}
