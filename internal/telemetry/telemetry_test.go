package telemetry_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/hojex/adserver/internal/telemetry"
)

// fakeKafka records produced records and rejects any with an empty topic
// (mirroring Redpanda: "record has no topic").
type fakeKafka struct {
	mu      sync.Mutex
	records []*kgo.Record
}

func (f *fakeKafka) ProduceSync(_ context.Context, records ...*kgo.Record) kgo.ProduceResults {
	f.mu.Lock()
	defer f.mu.Unlock()
	var res kgo.ProduceResults
	for _, r := range records {
		var err error
		if r.Topic == "" {
			err = errors.New("record has no topic")
		}
		f.records = append(f.records, r)
		res = append(res, kgo.ProduceResult{Record: r, Err: err})
	}
	return res
}

func (f *fakeKafka) Close() {}

// TestProducer_ReplayProducesWithTopic proves the HOT-2 fix end-to-end: a WAL
// record replayed on startup is produced with its REAL topic (not the empty
// topic that Redpanda rejects), and the WAL is compacted after a successful
// produce so a normal restart does not re-replay the whole history.
func TestProducer_ReplayProducesWithTopic(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "replay.wal")

	// Seed the WAL as if the process had buffered two events before a crash.
	wal, err := telemetry.OpenWALForTest(walPath, false)
	if err != nil {
		t.Fatalf("openWAL: %v", err)
	}
	if _, err := wal.Append("impressions", []byte("evt-1"), []byte("payload-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := wal.Append("clicks", []byte("evt-2"), []byte("payload-2")); err != nil {
		t.Fatal(err)
	}
	_ = wal.Close()

	// A new producer replays the WAL and drains to the fake Kafka client.
	fk := &fakeKafka{}
	p, err := telemetry.NewProducerForTest(fk, walPath)
	if err != nil {
		t.Fatalf("NewProducerForTest: %v", err)
	}
	p.Close() // closes the queue, waits for the drain, compacts the WAL

	fk.mu.Lock()
	defer fk.mu.Unlock()
	if len(fk.records) != 2 {
		t.Fatalf("produced %d records, want 2", len(fk.records))
	}
	topics := map[string]bool{}
	for _, r := range fk.records {
		if r.Topic == "" {
			t.Errorf("replayed record produced with EMPTY topic (HOT-2 regression → Redpanda rejects → data loss)")
		}
		topics[r.Topic] = true
	}
	if !topics["impressions"] || !topics["clicks"] {
		t.Errorf("produced topics=%v, want impressions+clicks", topics)
	}

	// Compaction: after a successful produce + Close, the WAL is truncated, so a
	// restart does not re-replay (and re-produce) the entire history.
	wal2, err := telemetry.OpenWALForTest(walPath, false)
	if err != nil {
		t.Fatalf("reopen after compaction: %v", err)
	}
	var remaining int
	_ = wal2.Replay(func(telemetry.WALRecord) { remaining++ })
	_ = wal2.Close()
	if remaining != 0 {
		t.Errorf("WAL not compacted after successful produce: %d records remain", remaining)
	}
}

// ---------------------------------------------------------------------------
// ULID tests
// ---------------------------------------------------------------------------

func TestNewULID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := telemetry.NewULID()
		if id == "" {
			t.Fatal("NewULID returned empty string")
		}
		if len(id) != 26 {
			t.Fatalf("ULID length mismatch: want 26, got %d (id=%q)", len(id), id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("ULID collision at iteration %d: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

// ---------------------------------------------------------------------------
// WAL tests
// ---------------------------------------------------------------------------

func TestWAL_AppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.wal")

	wal, err := telemetry.OpenWALForTest(path, false)
	if err != nil {
		t.Fatalf("openWAL: %v", err)
	}

	payloads := [][]byte{
		[]byte("event-1"),
		[]byte("event-2"),
		[]byte("event-3"),
	}
	for _, p := range payloads {
		if _, err := wal.Append("impressions", []byte("key-"+string(p)), p); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	_ = wal.Close()

	// Re-open and replay.
	wal2, err := telemetry.OpenWALForTest(path, false)
	if err != nil {
		t.Fatalf("reopen WAL: %v", err)
	}
	var replayed []telemetry.WALRecord
	if err := wal2.Replay(func(r telemetry.WALRecord) {
		replayed = append(replayed, r)
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	_ = wal2.Close()

	if len(replayed) != len(payloads) {
		t.Fatalf("replayed %d records, want %d", len(replayed), len(payloads))
	}
	for i, want := range payloads {
		// HOT-2: topic AND key must survive replay (empty topic → Kafka rejects).
		if replayed[i].Topic != "impressions" {
			t.Errorf("record %d: topic=%q, want %q", i, replayed[i].Topic, "impressions")
		}
		if string(replayed[i].Key) != "key-"+string(want) {
			t.Errorf("record %d: key=%q, want %q", i, replayed[i].Key, "key-"+string(want))
		}
		if string(replayed[i].Payload) != string(want) {
			t.Errorf("record %d: payload=%q, want %q", i, replayed[i].Payload, want)
		}
	}
}

func TestWAL_Truncate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trunc.wal")

	wal, err := telemetry.OpenWALForTest(path, false)
	if err != nil {
		t.Fatalf("openWAL: %v", err)
	}

	// Append 3 records; track offset of record 2 (keep from there).
	_, _ = wal.Append("t", nil, []byte("rec-1"))
	off2, _ := wal.Append("t", nil, []byte("rec-2"))
	_, _ = wal.Append("t", nil, []byte("rec-3"))

	// Truncate: keep from off2.
	if err := wal.Truncate(off2); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	_ = wal.Close()

	// Replay: should see only rec-2 and rec-3.
	wal2, err := telemetry.OpenWALForTest(path, false)
	if err != nil {
		t.Fatalf("reopen after truncate: %v", err)
	}
	var replayed [][]byte
	_ = wal2.Replay(func(r telemetry.WALRecord) {
		replayed = append(replayed, r.Payload)
	})
	_ = wal2.Close()

	if len(replayed) != 2 {
		t.Fatalf("after truncate replayed %d records, want 2", len(replayed))
	}
	if string(replayed[0]) != "rec-2" {
		t.Errorf("record 0: got %q, want %q", replayed[0], "rec-2")
	}
	if string(replayed[1]) != "rec-3" {
		t.Errorf("record 1: got %q, want %q", replayed[1], "rec-3")
	}
}

func TestWAL_CorruptRecordSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.wal")

	wal, err := telemetry.OpenWALForTest(path, false)
	if err != nil {
		t.Fatalf("openWAL: %v", err)
	}
	_, _ = wal.Append("t", nil, []byte("good-record"))
	_ = wal.Close()

	// Inject corruption: flip a payload byte (just before the 4-byte CRC) so the
	// record fails the CRC check and is skipped.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) >= 6 {
		data[len(data)-6] ^= 0xFF // corrupt a payload byte (before CRC)
	}
	_ = os.WriteFile(path, data, 0o600)

	wal2, err := telemetry.OpenWALForTest(path, false)
	if err != nil {
		t.Fatalf("reopen corrupt WAL: %v", err)
	}
	var replayed [][]byte
	_ = wal2.Replay(func(r telemetry.WALRecord) {
		replayed = append(replayed, r.Payload)
	})
	_ = wal2.Close()

	// Corrupt record is skipped; we expect 0 valid records replayed.
	if len(replayed) != 0 {
		t.Errorf("expected 0 replayed after corruption, got %d", len(replayed))
	}
}

// ---------------------------------------------------------------------------
// NoOpProducer — does not block and does not error
// ---------------------------------------------------------------------------

func TestNoOpProducer_DoesNotBlock(t *testing.T) {
	noop := telemetry.NoOpProducer{}
	// Must not block or panic.
	noop.Emit(context.Background(), nil)
}
