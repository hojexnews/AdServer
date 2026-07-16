// features.go has NO build constraint: it is compiled in BOTH variants of
// this package (onnx.go under `-tags onnx`, disabled.go under the default
// build). numFeatures therefore exists in the default (CGO-free) build too,
// so features_contract_test.go can assert — with no CGO/onnxruntime needed —
// that it stays in lockstep with internal/ranker.FeatureVectorLength.
package onnx

// numFeatures is the length of the serving feature vector — the anti-skew
// contract shared with internal/ranker.FeatureVectorLength (=23) and the
// ONNX graph's "features" input width (ml/training/train_pctr.py:_N_FEATURES).
//
// It is DUPLICATED here rather than imported from internal/ranker so the
// sidecar's non-test dependency direction stays one-way (cmd/ranker-sidecar ->
// internal/{stub,onnx}, never the reverse into the decision-service module —
// see the tech-lead gate note on E11). features_contract_test.go is the
// cross-check that makes the duplication safe against silent drift; it runs in
// the DEFAULT build (no build tag), so a mismatch fails ordinary `go test`,
// not only the `-tags onnx` path that skips without libonnxruntime.
const numFeatures = 23
