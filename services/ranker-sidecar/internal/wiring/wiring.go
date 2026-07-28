// Package wiring extracts the sidecar's Inferencer-selection logic out of
// cmd/ranker-sidecar/main.go into an exported, directly testable function.
//
// This exists so calibration_parity_test.go (`-tags onnx`) can exercise the
// EXACT production wiring — including the calibration.Wrap step that closes
// the "calibration-never-applied-in-serving" finding — rather than a
// reimplementation of it. A test that only calls calibration.Wrap directly
// would prove Apply's math is correct but would NOT catch a regression where
// main.go (or this package) stops calling Wrap at all; BuildInferencer is
// the single function both main.go and the test call, so removing the wrap
// step here fails the test, not just a hand-rolled check.
//
// No build tag: selecting between onnx.New's two variants (onnx.go
// `-tags onnx` / disabled.go default) and deciding whether to layer
// calibration.Wrap on top are both build-tag-neutral — same as
// cmd/ranker-sidecar/main.go itself (ADR-0002 §C hermetic default build).
package wiring

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/hojex/adserver/services/ranker-sidecar/internal/calibration"
	"github.com/hojex/adserver/services/ranker-sidecar/internal/onnx"
	"github.com/hojex/adserver/services/ranker-sidecar/internal/stub"
)

// Config mirrors the environment-variable inputs cmd/ranker-sidecar/main.go
// reads at startup (RANKER_MODEL_PATH, RANKER_MODEL_VERSION,
// RANKER_CALIBRATION_PATH).
type Config struct {
	// ModelPath is the path to the compiled .onnx model file. Empty means
	// "run in stub mode" (no model configured).
	ModelPath string

	// ModelVersion is the version tag embedded in score responses/logs.
	ModelVersion string

	// CalibrationPath is the path to calibration_map.json. If empty, it is
	// auto-derived as the sibling "calibration_map.json" in the same
	// directory as ModelPath (matching how ml/registry/artifacts/ ships
	// pctr_model.onnx and calibration_map.json side by side, both
	// versioned together in the MLflow registry — see
	// ml/calibration/calibrate.py's "CONTRATO COM O SIDECAR").
	CalibrationPath string
}

// Status reports whether the Inferencer BuildInferencer just returned
// actually applies the isotonic calibration map — the OBSERVABLE
// counterpart of the fail-open decision inside BuildInferencer.
//
// A missing/invalid calibration_map.json degrades on purpose to serving raw
// pCTR (DA-3: the hot path must never refuse to serve over a calibration
// artefact problem), but that degradation must not ALSO be invisible to
// monitoring — Mandato #3 requires calibration to stay "barata e critica
// ... monitorada". Before this type existed, the ONLY artefact of the
// degraded path was a single slog.Warn line; there was no queryable signal
// an alert could page on if a bad rollout left the sidecar silently serving
// uncalibrated pCTR indefinitely. cmd/ranker-sidecar/main.go exposes this
// struct via GET /healthz so an external prober/alert can do exactly that.
type Status struct {
	// Calibrated is true iff the returned Inferencer wraps every score
	// through calibration.Map.Apply (a *calibration.CalibratedInferencer).
	// false in every other case: stub mode, missing model file, ONNX
	// runtime unavailable/failed to load, or calibration map missing/invalid.
	Calibrated bool

	// Reason is a short, stable explanation for Calibrated=false (empty
	// when Calibrated=true). Derived only from path strings and Go error
	// text — never file contents or secrets.
	Reason string

	// CalibrationPath is the path BuildInferencer attempted to load the
	// calibration map from. Empty if that step was never reached (stub
	// mode, missing model file, or ONNX load failure).
	CalibrationPath string

	// Method and NPoints describe the loaded map when Calibrated=true
	// (e.g. "isotonic", 47 threshold points). Zero-value otherwise.
	Method  string
	NPoints int
}

// BuildInferencer selects and constructs the sidecar's Inferencer backend,
// and reports the calibration Status of the choice it made. This is the
// exact selection logic cmd/ranker-sidecar/main.go runs at startup:
//
//  1. cfg.ModelPath empty, or the file does not exist -> stub.NewStub
//     (fail-safe: the sidecar always starts, DA-3).
//  2. onnx.New(cfg.ModelPath, cfg.ModelVersion) fails for ANY reason
//     (including onnx.ErrNotCompiled in the default hermetic build,
//     ADR-0002 §C) -> stub.NewStub.
//  3. onnx.New succeeds -> attemptCalibration loads calibration.LoadMap at
//     cfg.CalibrationPath (or the auto-derived sibling path). On success,
//     wrap the OnnxInferencer with calibration.Wrap — stage (b) of the
//     two-stage design in ml/calibration/calibrate.py ("ONDE A CALIBRACAO
//     VIVE"), turning pCTR_raw into pCTR_cal before it is ever written to
//     the wire.
//     On ANY calibration load/validation error (missing file, malformed
//     JSON, non-monotonic thresholds), serve the RAW OnnxInferencer
//     UNWRAPPED and log a warning — a bad calibration artefact degrades to
//     uncalibrated pCTR, it never takes down the hot path (DA-3 fail-open;
//     this mirrors every other fail-open decision in this package: a
//     transient/config problem degrades the ranking signal, it never
//     produces a blank impression or a startup refusal). The returned
//     Status.Calibrated=false + Status.Reason make this degradation
//     observable instead of only a log line (Mandato #3).
//
// logger may be nil (all logging is guarded).
func BuildInferencer(cfg Config, logger *slog.Logger) (stub.Inferencer, Status) {
	if cfg.ModelPath == "" {
		if logger != nil {
			logger.Info("ranker-sidecar: RANKER_MODEL_PATH not set — running in stub mode",
				"model_version", cfg.ModelVersion)
		}
		return stub.NewStub(cfg.ModelVersion), Status{
			Calibrated: false,
			Reason:     "stub mode: RANKER_MODEL_PATH not set",
		}
	}

	if _, err := os.Stat(cfg.ModelPath); err != nil {
		if logger != nil {
			logger.Warn("ranker-sidecar: RANKER_MODEL_PATH set but file not found — using StubInferencer",
				"model_path", cfg.ModelPath,
				"err", err)
		}
		return stub.NewStub(cfg.ModelVersion), Status{
			Calibrated: false,
			Reason:     fmt.Sprintf("model file not found: %v", err),
		}
	}

	// Model file present: attempt the real OnnxInferencer (G0/E11).
	// onnx.New's behaviour depends on the build tag:
	//   - `-tags onnx`:  loads the model via onnxruntime (CGO, dlopen).
	//   - default build: always returns onnx.ErrNotCompiled (CGO-free).
	// Either way, ANY error here is a fail-SAFE (not fail-open) startup
	// fallback to StubInferencer — the sidecar always starts serving.
	onnxInf, onnxErr := onnx.New(cfg.ModelPath, cfg.ModelVersion)
	if onnxErr != nil {
		if logger != nil {
			if errors.Is(onnxErr, onnx.ErrNotCompiled) {
				logger.Warn("ranker-sidecar: RANKER_MODEL_PATH set but ONNX runtime not compiled in — "+
					"using StubInferencer. Build with -tags onnx and provide libonnxruntime.so to enable it.",
					"model_path", cfg.ModelPath,
					"model_version", cfg.ModelVersion)
			} else {
				logger.Warn("ranker-sidecar: onnx.New failed to load model — using StubInferencer",
					"model_path", cfg.ModelPath,
					"model_version", cfg.ModelVersion,
					"err", onnxErr)
			}
		}
		return stub.NewStub(cfg.ModelVersion), Status{
			Calibrated: false,
			Reason:     fmt.Sprintf("onnx runtime unavailable: %v", onnxErr),
		}
	}

	if logger != nil {
		logger.Info("ranker-sidecar: OnnxInferencer loaded",
			"model_path", cfg.ModelPath,
			"model_version", cfg.ModelVersion)
	}

	calPath := cfg.CalibrationPath
	if calPath == "" {
		calPath = filepath.Join(filepath.Dir(cfg.ModelPath), "calibration_map.json")
	}
	return attemptCalibration(onnxInf, calPath, logger)
}

// attemptCalibration is stage (b) of BuildInferencer's selection: given an
// Inferencer that is already scoring (in production, an *onnx.OnnxInferencer
// that just loaded successfully), try to load and apply the isotonic
// calibration map at calPath. Split out of BuildInferencer so this branch —
// otherwise reachable only when onnx.New succeeds, which requires
// `-tags onnx` + libonnxruntime.so (see calibration_parity_test.go) and is
// therefore NOT exercised by the default hermetic `go test ./...`
// (ADR-0002 §C) — can be exercised directly against any stub.Inferencer
// (including stub.StubInferencer) with no onnx dependency at all. This is
// what lets calibration_status_test.go prove the Status.Calibrated=false
// signal fires when the map is absent WITHOUT needing the onnx build tag.
//
// On ANY calibration load/validation error, fails OPEN to inner UNWRAPPED
// (DA-3) while reporting Calibrated=false + Reason in the returned Status —
// the degradation is observable (Mandato #3), not only a log line.
func attemptCalibration(inner stub.Inferencer, calPath string, logger *slog.Logger) (stub.Inferencer, Status) {
	calMap, calErr := calibration.LoadMap(calPath)
	if calErr != nil {
		if logger != nil {
			logger.Warn("ranker-sidecar: calibration map not loaded — serving RAW (uncalibrated) pCTR. "+
				"eCPM ranking uses pCTR_raw, not pCTR_cal, until this is fixed (see ml/calibration/calibrate.py "+
				"'ONDE A CALIBRACAO VIVE'). Fail-open: the hot path keeps serving.",
				"calibration_path", calPath,
				"err", calErr)
		}
		return inner, Status{
			Calibrated:      false,
			Reason:          calErr.Error(),
			CalibrationPath: calPath,
		}
	}

	if logger != nil {
		logger.Info("ranker-sidecar: calibration map loaded — serving CALIBRATED pCTR",
			"calibration_path", calPath,
			"calibration_method", calMap.Method,
			"n_points", len(calMap.Thresholds))
	}
	return calibration.Wrap(inner, calMap), Status{
		Calibrated:      true,
		CalibrationPath: calPath,
		Method:          calMap.Method,
		NPoints:         len(calMap.Thresholds),
	}
}
