// wiring_test.go has NO build constraint: it exercises BuildInferencer in
// the default hermetic build (ADR-0002 §C — no CGO, no libonnxruntime), so
// it runs on every ordinary `go test ./...`, unlike
// calibration_parity_test.go (`-tags onnx`) which needs the native runtime
// library and is not currently wired into CI.
//
// In the default build, onnx.New always returns onnx.ErrNotCompiled (see
// internal/onnx/disabled.go), so BuildInferencer's onnx-success branch
// (calibration.LoadMap + calibration.Wrap) is NOT exercised here — that
// path is covered by calibration_parity_test.go (`-tags onnx`, real model +
// real calibration map) and by calibration_test.go's
// TestCalibratedInferencer_AppliesMapToInnerScore (pure unit test of the
// same wrapping logic against a fake Inferencer). This file instead covers
// the fail-safe fallback paths that must hold regardless of build tag.
package wiring

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hojex/adserver/services/ranker-sidecar/internal/stub"
)

func TestBuildInferencer_EmptyModelPath_ReturnsStub(t *testing.T) {
	inf := BuildInferencer(Config{ModelVersion: "v-test"}, nil)
	defer inf.Close() //nolint:errcheck
	if _, ok := inf.(*stub.StubInferencer); !ok {
		t.Fatalf("BuildInferencer with empty ModelPath = %T, want *stub.StubInferencer", inf)
	}
	if inf.ModelVersion() != "v-test" {
		t.Errorf("ModelVersion() = %q, want %q", inf.ModelVersion(), "v-test")
	}
}

func TestBuildInferencer_MissingModelFile_FailsSafeToStub(t *testing.T) {
	inf := BuildInferencer(Config{
		ModelPath:    filepath.Join(t.TempDir(), "does-not-exist.onnx"),
		ModelVersion: "v-test",
	}, nil)
	defer inf.Close() //nolint:errcheck
	if _, ok := inf.(*stub.StubInferencer); !ok {
		t.Fatalf("BuildInferencer with a missing model file = %T, want *stub.StubInferencer", inf)
	}
}

func TestBuildInferencer_ModelPresentButRuntimeNotCompiled_FailsSafeToStub(t *testing.T) {
	// In the default (`!onnx`) build, onnx.New always returns
	// onnx.ErrNotCompiled regardless of file contents — any existing file
	// exercises this fallback path.
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "model.onnx")
	if err := os.WriteFile(modelPath, []byte("not a real onnx model"), 0o600); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	inf := BuildInferencer(Config{
		ModelPath:    modelPath,
		ModelVersion: "v-test",
	}, nil)
	defer inf.Close() //nolint:errcheck
	if _, ok := inf.(*stub.StubInferencer); !ok {
		t.Fatalf("BuildInferencer without -tags onnx = %T, want *stub.StubInferencer (onnx.ErrNotCompiled fallback)", inf)
	}
}
