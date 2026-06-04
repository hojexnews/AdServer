// ipc.go — Unix domain socket IPC client for the ranker sidecar.
//
// Protocol (wire format over UDS):
//
//	Request:  4-byte big-endian uint32 (payload length) || JSON payload
//	Response: 4-byte big-endian uint32 (payload length) || JSON payload
//
// Request JSON:
//
//	{
//	  "features": [f0, f1, ..., f22],   // float32 as JSON numbers
//	  "model_version": "1.0.0"          // from sidecar config; echoed back
//	}
//
// Response JSON:
//
//	{
//	  "score": 0.42,                    // pCTR float32 in [0,1]
//	  "model_version": "1.0.0"
//	}
//
// Error semantics:
//   - Any dial/write/read/parse error → fail-open (score = 0, no re-ranking).
//   - Context deadline exceeded (TX-4 budget) → fail-open.
//   - The caller (Ranker.Rank) treats score=0 for all candidates as a no-op
//     re-ranking (equivalent to returning the original slice — deterministic).
//
// Thread safety: ScoreClient is safe for concurrent use; each call dials a
// fresh connection. The UDS is SOCK_STREAM; the sidecar must accept concurrent
// connections. This is intentional to avoid head-of-line blocking within the
// TX-4 budget.
package ranker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

// scoreRequest is the JSON request sent to the ranker sidecar over UDS.
type scoreRequest struct {
	Features     [FeatureVectorLength]float32 `json:"features"`
	ModelVersion string                       `json:"model_version"`
}

// scoreResponse is the JSON response from the ranker sidecar.
type scoreResponse struct {
	Score        float32 `json:"score"`
	ModelVersion string  `json:"model_version"`
}

// ---------------------------------------------------------------------------
// ScoreClient
// ---------------------------------------------------------------------------

// ScoreClient sends a feature vector to the ranker sidecar over a Unix domain
// socket and returns the predicted pCTR score.
//
// On any error (dial, I/O, parse, or timeout) it returns (0, err). The caller
// (MLRanker.Rank) treats an error as fail-open: it keeps the deterministic
// order from the cascade.
type ScoreClient struct {
	// SocketPath is the filesystem path of the Unix domain socket served by
	// the ranker sidecar (services/ranker-sidecar).
	SocketPath string

	// Timeout is the hard deadline for a round-trip (connect + write + read).
	// This must fit within the TX-4 budget (5-8 ms p99 total, including
	// featurization). Recommended: 3-5 ms to leave headroom for the caller.
	// Default (zero): 5ms.
	Timeout time.Duration
}

// effectiveTimeout returns the configured timeout or the default.
func (c *ScoreClient) effectiveTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 5 * time.Millisecond
}

// Score sends features to the sidecar and returns the pCTR score.
//
// The ctx passed by the caller already carries the TX-4 deadline; Score
// additionally enforces its own timeout via effectiveTimeout. The stricter
// of the two applies.
func (c *ScoreClient) Score(ctx context.Context, features [FeatureVectorLength]float32, modelVersion string) (float32, error) {
	deadline := time.Now().Add(c.effectiveTimeout())
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	// Dial the Unix socket with a deadline.
	var dialer net.Dialer
	dialCtx, dialCancel := context.WithDeadline(ctx, deadline)
	defer dialCancel()

	conn, err := dialer.DialContext(dialCtx, "unix", c.SocketPath)
	if err != nil {
		return 0, fmt.Errorf("ranker IPC dial %q: %w", c.SocketPath, err)
	}
	defer conn.Close()

	// Set the overall I/O deadline on the connection.
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, fmt.Errorf("ranker IPC set deadline: %w", err)
	}

	// Marshal request.
	req := scoreRequest{
		Features:     features,
		ModelVersion: modelVersion,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("ranker IPC marshal: %w", err)
	}

	// Write: 4-byte length prefix + payload.
	if err := writeLengthPrefixed(conn, reqBytes); err != nil {
		return 0, fmt.Errorf("ranker IPC write: %w", err)
	}

	// Read: 4-byte length prefix + payload.
	respBytes, err := readLengthPrefixed(conn)
	if err != nil {
		return 0, fmt.Errorf("ranker IPC read: %w", err)
	}

	var resp scoreResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return 0, fmt.Errorf("ranker IPC unmarshal: %w", err)
	}

	return resp.Score, nil
}

// ---------------------------------------------------------------------------
// Wire helpers: 4-byte big-endian length-prefixed framing
// ---------------------------------------------------------------------------

// maxPayloadBytes is the maximum JSON payload size we accept from the sidecar.
// A score response is tiny; this guard prevents unbounded reads.
const maxPayloadBytes = 4096

func writeLengthPrefixed(w io.Writer, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readLengthPrefixed(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > maxPayloadBytes {
		return nil, fmt.Errorf("ranker IPC: response length %d out of range [1, %d]", n, maxPayloadBytes)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
