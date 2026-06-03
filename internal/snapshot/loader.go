package snapshot

import (
	"context"
	"sync/atomic"
	"time"
)

// Loader is the interface the snapshot package uses to fetch config from the
// backing store (Postgres / db/config/).  The hot path never calls Loader
// directly; it consumes the in-memory *Snapshot already built by the Store.
//
// Implementors live in I1 (db/config/ schema owner: money-ledger-guardian).
// The stub below is the I0 in-memory implementation.
type Loader interface {
	// Load fetches a fresh Snapshot.  Implementations MUST NOT return nil
	// without a non-nil error.
	Load(ctx context.Context) (*Snapshot, error)
}

// ---------------------------------------------------------------------------
// Store — atomic snapshot holder used by the hot path
// ---------------------------------------------------------------------------

// Store wraps a *Snapshot behind an atomic pointer so that the refresh
// goroutine can replace it without a lock while readers get a consistent
// pointer with a single load.
type Store struct {
	current atomic.Pointer[Snapshot]
}

// NewStore creates a Store pre-loaded with an initial snapshot.
func NewStore(initial *Snapshot) *Store {
	s := &Store{}
	s.current.Store(initial)
	return s
}

// Snapshot returns the current snapshot.  Never returns nil (guaranteed by
// the constructor and the refresh loop).
func (s *Store) Snapshot() *Snapshot {
	return s.current.Load()
}

// Replace atomically swaps the snapshot.  Called only by the refresh loop.
func (s *Store) Replace(snap *Snapshot) {
	s.current.Store(snap)
}

// ---------------------------------------------------------------------------
// Refresher — periodic pull loop
// ---------------------------------------------------------------------------

// Refresher calls Loader.Load on a fixed interval and replaces the Store.
// It is intentionally simple: no jitter, no back-off in I0.  Error handling
// keeps the last good snapshot.
type Refresher struct {
	loader   Loader
	store    *Store
	interval time.Duration
}

// NewRefresher builds a Refresher but does not start it.
func NewRefresher(loader Loader, store *Store, interval time.Duration) *Refresher {
	return &Refresher{loader: loader, store: store, interval: interval}
}

// Start runs the refresh loop until ctx is cancelled.  It performs an
// immediate load before entering the ticker loop.
func (r *Refresher) Start(ctx context.Context) {
	r.tryLoad(ctx)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tryLoad(ctx)
		}
	}
}

func (r *Refresher) tryLoad(ctx context.Context) {
	snap, err := r.loader.Load(ctx)
	if err != nil {
		// Keep the stale snapshot; log via caller's observability layer.
		// In I0 the stub never errors.
		return
	}
	r.store.Replace(snap)
}

// ---------------------------------------------------------------------------
// StubLoader — in-memory implementation for I0 / tests
// ---------------------------------------------------------------------------

// StubLoader implements Loader with a static snapshot.  Replace the snapshot
// between tests to simulate refresh cycles.
type StubLoader struct {
	snap *Snapshot
}

// NewStubLoader wraps a pre-built snapshot.
func NewStubLoader(snap *Snapshot) *StubLoader {
	return &StubLoader{snap: snap}
}

func (l *StubLoader) Load(_ context.Context) (*Snapshot, error) {
	return l.snap, nil
}

// EmptySnapshot returns a valid but empty snapshot for bootstrap / tests.
func EmptySnapshot() *Snapshot {
	return &Snapshot{
		Version:       "stub-0",
		LoadedAt:      time.Now(),
		Campaigns:     make(map[string]*Campaign),
		Banners:       make(map[string]*Banner),
		Zones:         make(map[string]*Zone),
		RuleSets:      make(map[string]*RuleSet),
		ZoneCampaigns: make(map[string][]string),
	}
}
