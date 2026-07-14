package telemetry

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"
)

// ---------------------------------------------------------------------------
// WAL — Write-Ahead Log for durable fire-and-forget telemetry
//
// Design:
//   - Append-only file: each record is a length-prefixed, CRC-protected blob.
//   - On startup, un-acked records are re-enqueued for the Redpanda producer.
//   - Acknowledgement: the producer calls Ack(offset) after a successful
//     Redpanda ACK; compaction truncates acknowledged records on close/reopen.
//   - Durability: os.File.Sync() is called after each append (configurable).
//   - Best-effort: if the WAL itself fails (disk full, etc.), the producer
//     falls back to in-memory-only mode and logs an error metric.  The event
//     may be lost on crash, but the hot path is never blocked.
//
// Record format (binary, little-endian):
//
//	[4 bytes: uint32 topic length][topic bytes]
//	[4 bytes: uint32 key   length][key bytes]
//	[4 bytes: uint32 payload length][payload bytes (serialised protobuf)]
//	[4 bytes: CRC32 of (topic ++ key ++ payload)]
//
// The topic and routing key are persisted ALONGSIDE the payload: on replay the
// producer must reconstruct the FULL record (a Kafka produce with an empty topic
// is rejected — the events the WAL exists to protect would be silently lost).
//
// ---------------------------------------------------------------------------

// walRecord is an entry read back from the WAL.
// Fields are exported so the export_test.go alias exposes them to _test packages.
type walRecord struct {
	Offset  int64  // byte offset of the start of this record
	End     int64  // byte offset just past this record (Offset + on-disk size)
	Topic   string // Kafka topic (persisted so replay can re-produce correctly)
	Key     []byte // partition-routing key (event_id bytes)
	Payload []byte // raw protobuf bytes
}

// walRecordSize returns the on-disk byte size of a record with these fields.
func walRecordSize(topic string, key, payload []byte) int64 {
	return int64(4 + len(topic) + 4 + len(key) + 4 + len(payload) + 4)
}

// WAL is an append-only, crash-durable event log.
type WAL struct {
	mu   sync.Mutex
	f    *os.File
	pos  int64 // current write position
	path string
	sync bool // fsync after each write
}

// openWAL opens (or creates) the WAL file at path.
// If sync is true, os.File.Sync() is called after each write.
func openWAL(path string, syncWrites bool) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("wal: open %q: %w", path, err)
	}
	pos, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("wal: seek end %q: %w", path, err)
	}
	return &WAL{f: f, pos: pos, path: path, sync: syncWrites}, nil
}

// Append writes (topic, key, payload) to the WAL and returns the byte offset of
// the START of the record.  It is safe for concurrent use.
func (w *WAL) Append(topic string, key, payload []byte) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	offset := w.pos

	tb := []byte(topic)
	// [4 topicLen][topic][4 keyLen][key][4 payloadLen][payload][4 CRC]
	buf := make([]byte, walRecordSize(topic, key, payload))
	n := 0
	binary.LittleEndian.PutUint32(buf[n:], uint32(len(tb)))
	n += 4
	n += copy(buf[n:], tb)
	binary.LittleEndian.PutUint32(buf[n:], uint32(len(key)))
	n += 4
	n += copy(buf[n:], key)
	binary.LittleEndian.PutUint32(buf[n:], uint32(len(payload)))
	n += 4
	n += copy(buf[n:], payload)
	// CRC covers topic ++ key ++ payload (everything but the length prefixes).
	crc := crc32.NewIEEE()
	_, _ = crc.Write(tb)
	_, _ = crc.Write(key)
	_, _ = crc.Write(payload)
	binary.LittleEndian.PutUint32(buf[n:], crc.Sum32())

	if _, err := w.f.Write(buf); err != nil {
		return 0, fmt.Errorf("wal: write: %w", err)
	}
	if w.sync {
		if err := w.f.Sync(); err != nil {
			return 0, fmt.Errorf("wal: sync: %w", err)
		}
	}

	w.pos += int64(len(buf))
	return offset, nil
}

// Replay reads all records from the WAL file and calls fn for each one.
// Corrupt or truncated records are skipped (best-effort recovery).
// Replay is intended to be called once at startup before the producer starts.
func (w *WAL) Replay(fn func(r walRecord)) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("wal: replay seek: %w", err)
	}

	reader := bufio.NewReader(w.f)
	var offset int64
	// readField reads a [4-byte length][bytes] field; returns io.EOF-ish on a
	// truncated tail (caller breaks).
	readField := func() ([]byte, bool) {
		var n uint32
		if err := binary.Read(reader, binary.LittleEndian, &n); err != nil {
			return nil, false
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(reader, b); err != nil {
			return nil, false
		}
		return b, true
	}
	for {
		topicB, ok := readField()
		if !ok {
			break // clean EOF or truncated length/topic — stop.
		}
		keyB, ok := readField()
		if !ok {
			break
		}
		payload, ok := readField()
		if !ok {
			break
		}
		var storedCRC uint32
		if err := binary.Read(reader, binary.LittleEndian, &storedCRC); err != nil {
			break
		}

		recordSize := walRecordSize(string(topicB), keyB, payload)
		crc := crc32.NewIEEE()
		_, _ = crc.Write(topicB)
		_, _ = crc.Write(keyB)
		_, _ = crc.Write(payload)
		if crc.Sum32() != storedCRC {
			// Corrupt record — skip and continue.
			offset += recordSize
			continue
		}

		fn(walRecord{
			Offset:  offset,
			End:     offset + recordSize,
			Topic:   string(topicB),
			Key:     keyB,
			Payload: payload,
		})
		offset += recordSize
	}

	// Restore file position to end for subsequent appends.
	if _, err := w.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("wal: replay restore seek: %w", err)
	}
	return nil
}

// Close flushes and closes the WAL file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sync {
		_ = w.f.Sync()
	}
	return w.f.Close()
}

// Truncate removes acknowledged data up to (but not including) keepFromOffset
// by rewriting the file without the acknowledged prefix.
// This is called during compaction and is NOT on the hot path.
func (w *WAL) Truncate(keepFromOffset int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if keepFromOffset <= 0 {
		return nil
	}

	// Read tail from keepFromOffset.
	if _, err := w.f.Seek(keepFromOffset, io.SeekStart); err != nil {
		return fmt.Errorf("wal: truncate seek: %w", err)
	}
	tail, err := io.ReadAll(w.f)
	if err != nil {
		return fmt.Errorf("wal: truncate read tail: %w", err)
	}

	// Rewrite to a temp file then rename for atomicity.
	tmp := w.path + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("wal: truncate create tmp: %w", err)
	}
	if _, err := tf.Write(tail); err != nil {
		_ = tf.Close()
		return fmt.Errorf("wal: truncate write tmp: %w", err)
	}
	if err := tf.Sync(); err != nil {
		_ = tf.Close()
		return fmt.Errorf("wal: truncate sync tmp: %w", err)
	}
	_ = tf.Close()

	// Close, replace, reopen.
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("wal: truncate close: %w", err)
	}
	if err := os.Rename(tmp, w.path); err != nil {
		return fmt.Errorf("wal: truncate rename: %w", err)
	}

	f, err := os.OpenFile(w.path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("wal: truncate reopen: %w", err)
	}
	pos, _ := f.Seek(0, io.SeekEnd)
	w.f = f
	w.pos = pos
	return nil
}
