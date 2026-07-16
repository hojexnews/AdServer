// server.go — Unix domain socket server for the ranker sidecar.
//
// The server accepts connections on a Unix socket, reads length-prefixed JSON
// requests (one per connection), calls the Inferencer, and writes a length-
// prefixed JSON response.
//
// Wire protocol (same as internal/ranker/ipc.go):
//
//	Request:  4-byte big-endian uint32 (len) || JSON
//	Response: 4-byte big-endian uint32 (len) || JSON
//
// Request JSON:
//
//	{"features": [f0,...,f22], "model_version": "stub-j1"}
//
// Response JSON:
//
//	{"score": 0.0, "model_version": "stub-j1"}
//
// Each connection serves exactly ONE request then is closed. This keeps the
// hot path simple and avoids request pipelining issues. The client (ScoreClient
// in internal/ranker/ipc.go) dials a fresh connection per scoring call.
//
// Hot-reload (future work, not implemented yet):
//
//	A SIGHUP handler could call Inferencer.Close() on the old model and load
//	a new OnnxInferencer from an updated RANKER_MODEL_PATH, updating
//	modelVersion without a socket rebind. Today the sidecar process must be
//	restarted to pick up a new model (see cmd/ranker-sidecar/main.go).
package stub

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
)

// Server is the Unix socket server that wraps an Inferencer.
type Server struct {
	socketPath string
	inferencer Inferencer
	logger     *slog.Logger
}

// NewServer creates a Server.
//   - socketPath: filesystem path for the Unix domain socket (created on Serve).
//   - inferencer: the inference backend (StubInferencer or OnnxInferencer,
//     selected by cmd/ranker-sidecar/main.go — see internal/onnx).
//   - logger: may be nil.
func NewServer(socketPath string, inferencer Inferencer, logger *slog.Logger) *Server {
	return &Server{
		socketPath: socketPath,
		inferencer: inferencer,
		logger:     logger,
	}
}

// Serve binds the Unix socket and accepts connections until ctx is cancelled.
// It removes any stale socket file before binding.
//
// Each connection is handled in a new goroutine (one request per connection).
func (s *Server) Serve(stop <-chan struct{}) error {
	// Remove stale socket from a previous run.
	_ = os.Remove(s.socketPath)

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	defer func() {
		ln.Close()
		os.Remove(s.socketPath) //nolint:errcheck // best-effort cleanup
	}()

	if s.logger != nil {
		s.logger.Info("ranker-sidecar: listening",
			"socket", s.socketPath,
			"model_version", s.inferencer.ModelVersion())
	}

	// Accept loop. We stop when stop is closed.
	go func() {
		<-stop
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			// ln.Close() from the stop goroutine will cause this.
			select {
			case <-stop:
				return nil
			default:
			}
			if s.logger != nil {
				s.logger.Error("ranker-sidecar: accept error", "err", err)
			}
			return err
		}
		go s.handleConn(conn)
	}
}

// ---------------------------------------------------------------------------
// Per-connection handler
// ---------------------------------------------------------------------------

// scoreReq mirrors internal/ranker/ipc.go scoreRequest.
type scoreReq struct {
	Features     []float32 `json:"features"`
	ModelVersion string    `json:"model_version"`
}

// scoreResp mirrors internal/ranker/ipc.go scoreResponse.
type scoreResp struct {
	Score        float32 `json:"score"`
	ModelVersion string  `json:"model_version"`
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	// Read length-prefixed request.
	reqBytes, err := readLengthPrefixed(conn)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("ranker-sidecar: read request error", "err", err)
		}
		return
	}

	var req scoreReq
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		if s.logger != nil {
			s.logger.Warn("ranker-sidecar: unmarshal request error", "err", err)
		}
		return
	}

	// Call the inferencer.
	score, err := s.inferencer.Score(req.Features)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("ranker-sidecar: inference error", "err", err)
		}
		score = 0 // fail gracefully — client will treat this as fail-open
	}

	resp := scoreResp{
		Score:        score,
		ModelVersion: s.inferencer.ModelVersion(),
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("ranker-sidecar: marshal response error", "err", err)
		}
		return
	}

	if err := writeLengthPrefixed(conn, respBytes); err != nil {
		if s.logger != nil {
			s.logger.Warn("ranker-sidecar: write response error", "err", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Wire helpers (4-byte big-endian length prefix)
// ---------------------------------------------------------------------------

const maxPayload = 65536

func readLengthPrefixed(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > maxPayload {
		return nil, io.ErrUnexpectedEOF
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeLengthPrefixed(w io.Writer, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
