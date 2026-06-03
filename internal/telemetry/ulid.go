// Package telemetry provides fire-and-forget batch production to Redpanda
// (Kafka-compatible API) with a durable local WAL and at-least-once delivery.
//
// # Architecture
//
//   - Producer: groups events into batches (linger + size) and publishes to
//     Redpanda using franz-go with idempotent producer settings.
//   - WAL: before any event is queued for the network, it is appended to a
//     local write-ahead log (append-only file).  On restart, un-acked entries
//     are replayed.  This gives at-least-once even when the process crashes
//     before the Redpanda ACK arrives.
//   - Dedupe: exactly-once delivery is the responsibility of the consumer
//     (ClickHouse ReplacingMergeTree keyed on event_id — see data/clickhouse/).
//     The producer guarantees idempotency at the Kafka level via
//     enable.idempotence=true.
//
// # Partitioning
//
// Keys are the raw event_id bytes (ULID/UUID).  hash(event_id) gives uniform
// distribution across partitions (topics.yaml, data/redpanda/).
//
// # Privacy
//
// No PII, no raw IP ever enters an event payload.  Geo is pre-derived by the
// collector; the IP is discarded before the event is built (TX-5/DA-11).
package telemetry

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// ULID generation (canonical format, replacing the provisional hex ID in I0)
// ---------------------------------------------------------------------------

// ulidSeq is an atomic counter used as a monotonic sub-millisecond component.
var ulidSeq atomic.Uint64

// NewULID generates a new ULID string.
//
// Format: 26 characters, Crockford base32.
// Time component: 48-bit Unix milliseconds (top 10 characters).
// Randomness: 80 bits (16 characters) — crypto/rand.
//
// This implementation is time-sortable and URL-safe without padding.
// It is used as event_id and decision_id for all events (TX-1).
func NewULID() string {
	now := uint64(time.Now().UnixMilli())

	// 10 bytes of randomness — crypto/rand for security.
	var randBytes [10]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		// Extremely unlikely; fall back to counter + time jitter.
		seq := ulidSeq.Add(1)
		binary.BigEndian.PutUint64(randBytes[:8], seq)
	}

	// Encode: 48-bit time + 80-bit random, Crockford base32.
	return encodeCrockford(now, randBytes)
}

// crockfordAlphabet is Crockford base32 encoding (no I, L, O, U).
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// encodeCrockford encodes time (ms) + random bytes into a 26-char ULID string.
func encodeCrockford(tsMs uint64, r [10]byte) string {
	// Build a 128-bit value: [48 bits ts][80 bits random].
	// Pack into 16 bytes: 6 bytes timestamp + 10 bytes random.
	// Pack: 6 bytes (ts, zero-padded to 48 bits) + 10 bytes random = 16 bytes.
	var buf [16]byte
	buf[0] = byte(tsMs >> 40)
	buf[1] = byte(tsMs >> 32)
	buf[2] = byte(tsMs >> 24)
	buf[3] = byte(tsMs >> 16)
	buf[4] = byte(tsMs >> 8)
	buf[5] = byte(tsMs)
	copy(buf[6:], r[:10])

	// Encode 128 bits into 26 Crockford base32 characters (5 bits each).
	n := new(big.Int).SetBytes(buf[:])
	b32 := new(big.Int).SetInt64(32)
	out := make([]byte, 26)
	rem := new(big.Int)
	for i := 25; i >= 0; i-- {
		n.DivMod(n, b32, rem)
		out[i] = crockfordAlphabet[rem.Int64()]
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Topic names — aligned with data/redpanda/topics.yaml
// ---------------------------------------------------------------------------

const (
	TopicAdRequest  = "adserver.ad-request.v1"
	TopicImpression = "adserver.impression.v1"
	TopicClick      = "adserver.click.v1"
	TopicConversion = "adserver.conversion.v1"
	TopicDecision   = "adserver.decision.v1"
)

// schemaVersion is the envelope schema_version for all events emitted by I2.
const schemaVersion = "1.0.0"

// modelVersionDeterministic is the model_version for pure-cascade decisions.
// An empty string signals "no ML ranker" per decision.proto comments.
const modelVersionDeterministic = ""

// newEnvelopeFields returns a fresh (eventID, occurredAt) pair.
// The caller fills in the remaining envelope fields (tenant_id, decision_id,
// model_version, source).
func newEventID() string {
	return NewULID()
}

// validateEnvelope panics in development if the invariants TX-1 would catch.
// In production the caller ensures correctness; this is a canary.
func validateEventID(id string) error {
	if id == "" {
		return fmt.Errorf("telemetry: event_id must not be empty (TX-1)")
	}
	return nil
}
