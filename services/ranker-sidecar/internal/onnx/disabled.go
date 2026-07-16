//go:build !onnx

// Package onnx (disabled variant) ships the CGO-free stand-in for New when
// the sidecar is built WITHOUT the `onnx` build tag — the default build
// (ADR-0002 §C: `go build ./...` must be hermetic, no CGO, no
// libonnxruntime dependency).
//
// New always returns ErrNotCompiled. The caller
// (cmd/ranker-sidecar/main.go) treats this exactly like any other model-load
// failure: log a warning and fall back to stub.NewStub — the sidecar never
// refuses to start, and the cascade is never starved of a fail-open score=0
// backend (DA-3).
package onnx

import (
	"github.com/hojex/adserver/services/ranker-sidecar/internal/stub"
)

// New always fails in the default (CGO-free) build. See onnx.go (built
// under `-tags onnx`) for the real implementation. ErrNotCompiled is defined
// in errors.go (no build constraint) so it is available under both variants.
func New(_ string, _ string) (stub.Inferencer, error) {
	return nil, ErrNotCompiled
}
