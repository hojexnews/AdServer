// features_contract_test.go has NO build constraint — it runs in the DEFAULT
// build (no CGO, no libonnxruntime), so the anti-skew cross-check below guards
// every ordinary `go test` run, not only the `-tags onnx` path (which skips
// when the runtime library is absent).
package onnx

import (
	"testing"

	"github.com/hojex/adserver/internal/ranker"
)

// TestNumFeaturesMatchesRankerContract guards the anti-skew invariant that the
// sidecar's ONNX input-tensor width (numFeatures, features.go) equals the
// decision-service featurizer's output length
// (internal/ranker.FeatureVectorLength). onnx.go deliberately duplicates the
// constant to keep the sidecar's non-test dependency direction one-way (it
// never imports internal/ranker outside tests); this test is the cross-check
// that makes the duplication safe — if the featurizer's vector length ever
// changes without updating numFeatures in lockstep, the ONNX input tensor
// would be the wrong shape and every Score would silently fail-open to 0. The
// parity gate (E11) required this check to exist, not merely be claimed.
func TestNumFeaturesMatchesRankerContract(t *testing.T) {
	if numFeatures != ranker.FeatureVectorLength {
		t.Fatalf("feature-vector contract drift: onnx.numFeatures=%d != ranker.FeatureVectorLength=%d — "+
			"the sidecar ONNX input-tensor width and the featurizer output length MUST match (anti-skew). "+
			"Update numFeatures in services/ranker-sidecar/internal/onnx/features.go.",
			numFeatures, ranker.FeatureVectorLength)
	}
}
