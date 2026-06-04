// Package ranker implements the ML re-ranker client (Fase 2 / J1).
//
// Architecture (ADR-0003 §A and §B):
//   - The re-ranker is a WITHIN-STRATUM re-orderer only (DA-3). It NEVER
//     changes the winning stratum; it only re-orders the candidates that
//     the cascade has already determined belong to the winning stratum.
//   - On any failure (IPC error, timeout, sidecar unavailable) the ranker
//     MUST return the input slice unmodified — fail-open to cascade order.
//   - The Featurize function is the SINGLE featurization implementation shared
//     between serving (this file) and training (ml/features/python/featurize.py).
//     Both must derive the same float32 vector for the same input — this is the
//     anti-skew invariant enforced by parity_test.go.
//
// Feature spec: ml/features/spec/feature_spec.yaml v1.0.0
// Hash canonical: murmur3.SeedSum32(uint32(seed), []byte(value)) % uint32(numBuckets)
//
// Privacy (TX-5/DA-11): no PII enters this package. Geo is already resolved to
// country+city strings (IP discarded upstream). Device class is a coarse string
// from useragent.Classify (raw UA discarded upstream).
//
// Money (TX-2): ECPMMinorUnits is int64 minor-units. log1p produces float32 for
// the ML vector only — financial comparisons always use money.CompareECPM (int64).
package ranker

import (
	"math"
	"time"

	"github.com/spaolacci/murmur3"
)

// FeatureSpecVersion is the version of the feature spec this implementation
// corresponds to. Must match the feature_spec_version in:
//   - ml/features/spec/feature_spec.yaml
//   - ml/features/testdata/parity_cases.json
const FeatureSpecVersion = "1.0.0"

// FeatureVectorLength is the number of online features in the serving vector.
// Matches metadata.vector_length_online in feature_spec.yaml.
const FeatureVectorLength = 23

// FeaturizeInput holds all inputs required to compute the feature vector for
// a single candidate in the context of an ad decision request.
//
// All sources are in-memory (TX-4: zero network calls in this function).
// The caller is responsible for supplying pre-resolved, PII-free inputs:
//   - Geo: country+city only, IP already discarded (TX-5)
//   - DeviceClass: coarse class from useragent.Classify, raw UA discarded (TX-5)
//   - ECPMMinorUnits: int64 minor-units, NOT float (TX-2)
type FeaturizeInput struct {
	// Zone dimensions (snapshot.Zone).
	ZoneWidth  int32
	ZoneHeight int32

	// Request time (UTC).
	RequestTime time.Time

	// Geo: country (ISO 3166-1 alpha-2) and city name. Empty = missing.
	// IP bruto NEVER present here (TX-5/DA-11).
	GeoCountry string
	GeoCity    string

	// DeviceClass is the coarse class from useragent.Classify.
	// Raw UA is never stored here (TX-5/DA-11).
	DeviceClass string

	// Candidate fields.
	CandidateTier    int // 1=Override, 2=Contract, 3=Remnant
	CampaignPriority int32
	PacingDeficit    float64 // [0,1]; 0 for non-Contract

	// ECPMMinorUnits is the eCPM in minor-units of the campaign asset (int64, TX-2).
	// 0 for Override/Contract.
	ECPMMinorUnits int64

	// Banner dimensions.
	BannerWidth  int32
	BannerHeight int32

	// CreativeType: "image", "html", "video", or "unknown".
	CreativeType string

	// CandidateCount is the total number of eligible candidates in the stratum.
	CandidateCount uint32

	// Campaign history (approximate counters from snapshot — not the authoritative ledger).
	CampaignDeliveredImpressions int64
	CampaignGoalImpressions      int64
	CampaignDeliveredClicks      int64
}

// Featurize transforms a FeaturizeInput into a float32 vector of length
// FeatureVectorLength, in the canonical order defined by feature_spec.yaml v1.0.0.
//
// This is the SINGLE source of truth for the serving-side featurization.
// The Python implementation in ml/features/python/featurize.py must produce
// the same vector (within float tolerance 1e-6) for the same input.
// The parity gate is enforced by internal/ranker/parity_test.go.
//
// Invariants:
//   - No network calls (TX-4).
//   - No PII in inputs or outputs (TX-5/DA-11).
//   - ECPMMinorUnits is int64; log1p output is float32 for ML only (TX-2).
//   - All hash seeds are fixed per-feature and must match the Python mmh3 seeds.
func Featurize(inp FeaturizeInput) [FeatureVectorLength]float32 {
	var v [FeatureVectorLength]float32

	// -------------------------------------------------------------------------
	// Index 0: zone_width_bucket
	// Boundaries (IAB standard): [120, 160, 200, 234, 250, 300, 320, 336, 468, 728, 970]
	// -------------------------------------------------------------------------
	v[0] = float32(bucketize(int64(inp.ZoneWidth),
		[]int64{120, 160, 200, 234, 250, 300, 320, 336, 468, 728, 970}))

	// -------------------------------------------------------------------------
	// Index 1: zone_height_bucket
	// Boundaries: [50, 60, 90, 100, 250, 280, 600, 1024]
	// -------------------------------------------------------------------------
	v[1] = float32(bucketize(int64(inp.ZoneHeight),
		[]int64{50, 60, 90, 100, 250, 280, 600, 1024}))

	// -------------------------------------------------------------------------
	// Index 2: zone_aspect_ratio_bucket
	// ratio = width*100 / height (integer division); missing default = 3
	// Boundaries: [50, 80, 100, 133, 200, 400, 800]
	// -------------------------------------------------------------------------
	{
		aspectBounds := []int64{50, 80, 100, 133, 200, 400, 800}
		if inp.ZoneHeight > 0 {
			aspectRatio := int64(inp.ZoneWidth) * 100 / int64(inp.ZoneHeight)
			v[2] = float32(bucketize(aspectRatio, aspectBounds))
		} else {
			v[2] = 3 // missing default per spec
		}
	}

	// -------------------------------------------------------------------------
	// Index 3: hour_of_day — UTC hour [0, 23]
	// -------------------------------------------------------------------------
	v[3] = float32(inp.RequestTime.UTC().Hour())

	// -------------------------------------------------------------------------
	// Index 4: day_of_week — 0=Sunday ... 6=Saturday
	// -------------------------------------------------------------------------
	dow := int(inp.RequestTime.UTC().Weekday()) // time.Sunday=0 ... time.Saturday=6
	v[4] = float32(dow)

	// -------------------------------------------------------------------------
	// Index 5: is_weekend — 1 if day_of_week in {0 (Sun), 6 (Sat)}, else 0
	// -------------------------------------------------------------------------
	if dow == 0 || dow == 6 {
		v[5] = 1
	} else {
		v[5] = 0
	}

	// -------------------------------------------------------------------------
	// Index 6: geo_country_hash
	// murmur3_32(country, seed=42) % 512; empty → missing=0
	// -------------------------------------------------------------------------
	if inp.GeoCountry != "" {
		v[6] = float32(featureHash(inp.GeoCountry, 42, 512))
	} else {
		v[6] = 0 // missing
	}

	// -------------------------------------------------------------------------
	// Index 7: geo_city_hash
	// murmur3_32(city, seed=43) % 2048; empty → missing=0
	// -------------------------------------------------------------------------
	if inp.GeoCity != "" {
		v[7] = float32(featureHash(inp.GeoCity, 43, 2048))
	} else {
		v[7] = 0 // missing
	}

	// -------------------------------------------------------------------------
	// Index 8: device_class_hash
	// murmur3_32(device_class, seed=44) % 16; empty → missing=0
	// -------------------------------------------------------------------------
	if inp.DeviceClass != "" {
		v[8] = float32(featureHash(inp.DeviceClass, 44, 16))
	} else {
		v[8] = 0 // missing
	}

	// -------------------------------------------------------------------------
	// Index 9: is_bot — 1 if device_class == "bot", else 0
	// -------------------------------------------------------------------------
	if inp.DeviceClass == "bot" {
		v[9] = 1
	} else {
		v[9] = 0
	}

	// -------------------------------------------------------------------------
	// Index 10: candidate_tier — passthrough int (1/2/3)
	// -------------------------------------------------------------------------
	v[10] = float32(inp.CandidateTier)

	// -------------------------------------------------------------------------
	// Index 11: campaign_priority — clipped to [0, 100]
	// -------------------------------------------------------------------------
	prio := inp.CampaignPriority
	if prio < 0 {
		prio = 0
	} else if prio > 100 {
		prio = 100
	}
	v[11] = float32(prio)

	// -------------------------------------------------------------------------
	// Index 12: pacing_deficit — passthrough float32 [0, 1]
	// -------------------------------------------------------------------------
	v[12] = float32(inp.PacingDeficit)

	// -------------------------------------------------------------------------
	// Index 13: pacing_deficit_bucket
	// Boundaries: [0.1, 0.25, 0.5, 0.75, 0.9]
	// -------------------------------------------------------------------------
	v[13] = float32(bucketizeFloat(inp.PacingDeficit, []float64{0.1, 0.25, 0.5, 0.75, 0.9}))

	// -------------------------------------------------------------------------
	// Index 14: ecpm_minor_units_log1p
	// Input: int64 minor-units (TX-2: never float for money).
	// log1p(x) = ln(1+x); for x=0 → 0.0.
	// -------------------------------------------------------------------------
	ecpmLog := math.Log1p(float64(inp.ECPMMinorUnits)) //nolint:forbidigo // ML feature, not money comparison
	v[14] = float32(ecpmLog)

	// -------------------------------------------------------------------------
	// Index 15: ecpm_tier_bucket
	// Applied over log1p(ecpm_minor_units).
	// Boundaries: [1.0, 2.0, 3.5, 5.0, 7.0, 9.0, 11.0]
	// -------------------------------------------------------------------------
	v[15] = float32(bucketizeFloat(ecpmLog, []float64{1.0, 2.0, 3.5, 5.0, 7.0, 9.0, 11.0}))

	// -------------------------------------------------------------------------
	// Index 16: banner_width_bucket — same boundaries as zone_width_bucket
	// -------------------------------------------------------------------------
	v[16] = float32(bucketize(int64(inp.BannerWidth),
		[]int64{120, 160, 200, 234, 250, 300, 320, 336, 468, 728, 970}))

	// -------------------------------------------------------------------------
	// Index 17: banner_height_bucket — same boundaries as zone_height_bucket
	// -------------------------------------------------------------------------
	v[17] = float32(bucketize(int64(inp.BannerHeight),
		[]int64{50, 60, 90, 100, 250, 280, 600, 1024}))

	// -------------------------------------------------------------------------
	// Index 18: banner_size_match
	// 1 if banner_width_bucket == zone_width_bucket AND banner_height_bucket == zone_height_bucket
	// -------------------------------------------------------------------------
	if v[16] == v[0] && v[17] == v[1] {
		v[18] = 1
	} else {
		v[18] = 0
	}

	// -------------------------------------------------------------------------
	// Index 19: creative_type — enum int (image=1, html=2, video=3, unknown=0)
	// -------------------------------------------------------------------------
	switch inp.CreativeType {
	case "image":
		v[19] = 1
	case "html":
		v[19] = 2
	case "video":
		v[19] = 3
	default:
		v[19] = 0
	}

	// -------------------------------------------------------------------------
	// Index 20: candidate_count_log1p
	// log1p(candidate_count); 0 if count==0.
	// -------------------------------------------------------------------------
	v[20] = float32(math.Log1p(float64(inp.CandidateCount))) //nolint:forbidigo // ML feature, not money

	// -------------------------------------------------------------------------
	// Index 21: campaign_ctr_estimate
	// DeliveredClicks / DeliveredImpressions; 0 if impressions == 0. Clipped [0,1].
	// -------------------------------------------------------------------------
	if inp.CampaignDeliveredImpressions > 0 {
		ctr := float64(inp.CampaignDeliveredClicks) / float64(inp.CampaignDeliveredImpressions) //nolint:forbidigo // ML feature
		if ctr < 0 {
			ctr = 0
		} else if ctr > 1 {
			ctr = 1
		}
		v[21] = float32(ctr)
	} else {
		v[21] = 0
	}

	// -------------------------------------------------------------------------
	// Index 22: campaign_delivery_ratio
	// DeliveredImpressions / GoalImpressions; 0 if goal == 0. Clipped [0,2].
	// -------------------------------------------------------------------------
	if inp.CampaignGoalImpressions > 0 {
		ratio := float64(inp.CampaignDeliveredImpressions) / float64(inp.CampaignGoalImpressions) //nolint:forbidigo // ML feature
		if ratio < 0 {
			ratio = 0
		} else if ratio > 2 {
			ratio = 2
		}
		v[22] = float32(ratio)
	} else {
		v[22] = 0
	}

	return v
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// featureHash computes murmur3_32(value, seed) % numBuckets.
// This is the canonical hash function for feature hashing in this spec.
//
// Go canonical: murmur3.Sum32WithSeed([]byte(value), uint32(seed)) % uint32(numBuckets)
// Python canonical: mmh3.hash(value, seed, signed=False) % num_buckets
//
// Both produce identical uint32 results for the same (value, seed).
// Verified by parity_test.go against the gold fixtures in parity_cases.json.
func featureHash(value string, seed uint32, numBuckets uint32) uint32 {
	h := murmur3.Sum32WithSeed([]byte(value), seed)
	return h % numBuckets
}

// bucketize returns the bucket index for an integer value against sorted
// boundary values. The result is in [0, len(boundaries)]:
//
//	value < boundaries[0]    → 0
//	boundaries[k] <= value < boundaries[k+1] → k+1  (for k in 0..n-2)
//	value >= boundaries[n-1] → n
//
// This is equivalent to bisect_right in Python / numpy.digitize.
func bucketize(value int64, boundaries []int64) int {
	for i, b := range boundaries {
		if value < b {
			return i
		}
	}
	return len(boundaries)
}

// bucketizeFloat is the float64 variant of bucketize, used for
// pacing_deficit_bucket and ecpm_tier_bucket.
func bucketizeFloat(value float64, boundaries []float64) int {
	for i, b := range boundaries {
		if value < b {
			return i
		}
	}
	return len(boundaries)
}
