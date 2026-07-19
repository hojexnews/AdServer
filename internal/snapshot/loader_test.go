// loader_test.go — concurrency and correctness tests for the hot-swappable
// config Store/Refresher (Mandato item 6: "Config em memoria: ...snapshot
// versionado carregado do Postgres por pull periodico. Avaliacao O(1), sem
// ida a rede no hot path").
//
// Before this file, internal/snapshot had ZERO test files (`go test` reports
// "[no test files]"), so the hot-swap mechanism that underlies the entire
// decision hot path (loader.go:25-27: "Store wraps a *Snapshot behind an
// atomic pointer so that the refresh goroutine can replace it without a lock
// while readers get a consistent pointer") was completely unverified. In
// particular there was no coverage proving Store.Replace/Store.Snapshot are
// safe under concurrent use — the exact property the doc comment claims.
package snapshot_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Store: basic store/load (single-threaded sanity).
// ---------------------------------------------------------------------------

func TestStore_StoresAndReturnsInitialSnapshot(t *testing.T) {
	initial := snapshot.EmptySnapshot()
	initial.Version = "v0"

	s := snapshot.NewStore(initial)

	got := s.Snapshot()
	if got == nil {
		t.Fatal("Snapshot() returned nil for a store constructed with a non-nil initial snapshot")
	}
	if got.Version != "v0" {
		t.Errorf("Snapshot().Version = %q, want %q", got.Version, "v0")
	}
	if got != initial {
		t.Error("Snapshot() did not return the exact initial snapshot pointer")
	}
}

func TestStore_Replace_SwapsToNewSnapshot(t *testing.T) {
	initial := snapshot.EmptySnapshot()
	initial.Version = "v0"
	s := snapshot.NewStore(initial)

	next := snapshot.EmptySnapshot()
	next.Version = "v1"
	s.Replace(next)

	got := s.Snapshot()
	if got.Version != "v1" {
		t.Errorf("after Replace, Snapshot().Version = %q, want %q", got.Version, "v1")
	}
	if got != next {
		t.Error("after Replace, Snapshot() did not return the exact replaced pointer")
	}
}

// ---------------------------------------------------------------------------
// Store: concurrent read/write safety — the actual invariant under test.
//
// This is the test the HOLLOW-gap mutation (atomic.Pointer[Snapshot] ->
// plain *Snapshot field with direct assignment) is designed to break:
// `go test -race` MUST flag a data race when Replace and Snapshot run
// concurrently without atomicity.
// ---------------------------------------------------------------------------

// buildVersionedSnapshot builds a fully self-consistent snapshot where the
// Version string and a Campaigns map entry keyed by that same Version agree
// with each other. A reader that ever observed a "torn"/partial snapshot
// (mixing fields from two different generations) would find
// campaigns[Version] missing — this cannot happen with a correct atomic
// pointer swap, but it gives the concurrent test something concrete to
// assert beyond "did not crash".
func buildVersionedSnapshot(n int) *snapshot.Snapshot {
	snap := snapshot.EmptySnapshot()
	v := fmt.Sprintf("v%d", n)
	snap.Version = v
	snap.Campaigns[v] = &snapshot.Campaign{ID: v}
	return snap
}

// TestStore_ConcurrentReplaceAndSnapshot_NoRace hammers Replace (single
// writer) and Snapshot (many readers) concurrently. Run with `go test
// -race`: any unsynchronized access to the underlying pointer is caught by
// the race detector. It also asserts every value observed by a reader is
// internally self-consistent (never a torn/partial snapshot) and never nil.
func TestStore_ConcurrentReplaceAndSnapshot_NoRace(t *testing.T) {
	const iterations = 500
	const readers = 8

	initial := buildVersionedSnapshot(0)
	s := snapshot.NewStore(initial)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var sawNil atomic.Bool
	var sawTorn atomic.Bool

	// Readers: hammer Snapshot() concurrently with the writer below.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := s.Snapshot()
				if snap == nil {
					sawNil.Store(true)
					continue
				}
				if _, ok := snap.Campaigns[snap.Version]; !ok {
					sawTorn.Store(true)
				}
			}
		}()
	}

	// Writer: replaces the snapshot repeatedly — the same concurrent
	// mutation path exercised by the real Refresher loop.
	for i := 1; i <= iterations; i++ {
		s.Replace(buildVersionedSnapshot(i))
	}
	close(stop)
	wg.Wait()

	if sawNil.Load() {
		t.Error("a reader observed a nil snapshot during concurrent Replace")
	}
	if sawTorn.Load() {
		t.Error("a reader observed a torn/partial snapshot (Version without matching Campaigns entry) during concurrent Replace")
	}

	final := s.Snapshot()
	if want := fmt.Sprintf("v%d", iterations); final.Version != want {
		t.Errorf("final Snapshot().Version = %q, want %q", final.Version, want)
	}
}

// ---------------------------------------------------------------------------
// Refresher: periodic pull loop replaces the Store under concurrent readers.
// ---------------------------------------------------------------------------

// countingLoader returns a fresh, versioned snapshot on every Load call and
// counts how many times it was called.
type countingLoader struct {
	calls atomic.Int64
}

func (l *countingLoader) Load(_ context.Context) (*snapshot.Snapshot, error) {
	n := l.calls.Add(1)
	return buildVersionedSnapshot(int(n)), nil
}

// TestRefresher_ConcurrentReaders_NoRace starts a real Refresher loop
// (Start) against a Store while multiple reader goroutines call Snapshot()
// concurrently, then lets the context expire. Run with -race: the refresh
// goroutine's writes via Store.Replace must never race with concurrent reads
// via Store.Snapshot — this exercises the exact production wiring (a single
// refresh goroutine calling Replace while N request-handling goroutines call
// Snapshot), not just the Store in isolation.
func TestRefresher_ConcurrentReaders_NoRace(t *testing.T) {
	loader := &countingLoader{}
	s := snapshot.NewStore(snapshot.EmptySnapshot())
	r := snapshot.NewRefresher(loader, s, 1*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if snap := s.Snapshot(); snap == nil {
					t.Error("reader observed nil snapshot during Refresher.Start")
					return
				}
			}
		}()
	}

	r.Start(ctx) // blocks until ctx is done (immediate load + ticker loop)
	close(stop)
	wg.Wait()

	if loader.calls.Load() < 2 {
		t.Errorf("expected the refresh loop to call Load at least twice in 50ms at a 1ms interval, got %d calls", loader.calls.Load())
	}
}
