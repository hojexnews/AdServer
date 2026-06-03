package telemetry_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hojex/adserver/internal/telemetry"
)

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
		if _, err := wal.Append(p); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	_ = wal.Close()

	// Re-open and replay.
	wal2, err := telemetry.OpenWALForTest(path, false)
	if err != nil {
		t.Fatalf("reopen WAL: %v", err)
	}
	var replayed [][]byte
	if err := wal2.Replay(func(r telemetry.WALRecord) {
		replayed = append(replayed, r.Payload)
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	_ = wal2.Close()

	if len(replayed) != len(payloads) {
		t.Fatalf("replayed %d records, want %d", len(replayed), len(payloads))
	}
	for i, want := range payloads {
		if string(replayed[i]) != string(want) {
			t.Errorf("record %d: got %q, want %q", i, replayed[i], want)
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
	_, _ = wal.Append([]byte("rec-1"))
	off2, _ := wal.Append([]byte("rec-2"))
	_, _ = wal.Append([]byte("rec-3"))

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
	_, _ = wal.Append([]byte("good-record"))
	_ = wal.Close()

	// Inject corruption: flip bytes in the middle of the file.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) >= 6 {
		data[5] ^= 0xFF // corrupt payload byte
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
