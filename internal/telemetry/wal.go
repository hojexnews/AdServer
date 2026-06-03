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
//	[4 bytes: uint32 payload length]
//	[N bytes: payload (serialised protobuf)]
//	[4 bytes: CRC32 of payload]
//
// ---------------------------------------------------------------------------

// walRecord is an entry read back from the WAL.
// Fields are exported so the export_test.go alias exposes them to _test packages.
type walRecord struct {
	Offset  int64  // byte offset of the start of this record
	Payload []byte // raw protobuf bytes
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

// Append writes payload to the WAL and returns the byte offset of the record.
// It is safe for concurrent use.
func (w *WAL) Append(payload []byte) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	offset := w.pos

	// [4 bytes length][payload][4 bytes CRC]
	buf := make([]byte, 4+len(payload)+4)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(payload)))
	copy(buf[4:], payload)
	crc := crc32.ChecksumIEEE(payload)
	binary.LittleEndian.PutUint32(buf[4+len(payload):], crc)

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
	for {
		var length uint32
		if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return fmt.Errorf("wal: replay read length: %w", err)
		}

		payload := make([]byte, length)
		if _, err := io.ReadFull(reader, payload); err != nil {
			// Truncated record — stop here.
			break
		}

		var storedCRC uint32
		if err := binary.Read(reader, binary.LittleEndian, &storedCRC); err != nil {
			break
		}

		computedCRC := crc32.ChecksumIEEE(payload)
		recordSize := int64(4 + length + 4)
		if computedCRC != storedCRC {
			// Corrupt record — skip and continue.
			offset += recordSize
			continue
		}

		fn(walRecord{Offset: offset, Payload: payload})
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
