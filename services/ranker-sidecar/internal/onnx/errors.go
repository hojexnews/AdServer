// errors.go has NO build constraint: it is compiled in both variants of this
// package (onnx.go under `-tags onnx`, disabled.go under the default build)
// so that callers (cmd/ranker-sidecar/main.go) can reference onnx.ErrNotCompiled
// with errors.Is regardless of how the binary was built.
package onnx

import "errors"

// ErrNotCompiled is returned by New when the binary was built without the
// `onnx` build tag (see disabled.go). Build with `-tags onnx` (and provide
// libonnxruntime.so.*, e.g. via ONNXRUNTIME_SHARED_LIBRARY_PATH) to enable
// the real ONNX Runtime backend (onnx.go). The real New (onnx.go) never
// returns this sentinel.
var ErrNotCompiled = errors.New("onnx: runtime not compiled in (build with -tags onnx)")
