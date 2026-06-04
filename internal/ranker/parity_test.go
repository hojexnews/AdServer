// Package ranker — parity test against the gold fixtures.
//
// This test is the anti-skew gate for J1: it verifies that the Go featurization
// (internal/ranker/featurize.go) produces the same float32 vector as the Python
// implementation (ml/features/python/featurize.py) for every canonical test case
// in ml/features/testdata/parity_cases.json.
//
// Gate criteria (from feature_spec.yaml parity_test_tolerance):
//   - Floats:   abs(go_value - py_value) <= 1e-6
//   - Integers: exact equality (diff == 0)
//
// This test MUST pass before any model is promoted to production (ADR-0003 §G, J2 gate).
package ranker

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// JSON fixture types (mirrors ml/features/testdata/parity_cases.json schema)
// ---------------------------------------------------------------------------

type parityFixtures struct {
	FeatureSpecVersion string        `json:"feature_spec_version"`
	Cases              []parityCase  `json:"cases"`
}

type parityCase struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	Input       parityCaseInput `json:"input"`
	Expected    expectedVector `json:"expected_vector_computed"`
}

type parityCaseInput struct {
	Zone struct {
		Width  int32 `json:"width"`
		Height int32 `json:"height"`
	} `json:"zone"`
	RequestTimeUTC struct {
		Hour      int `json:"hour"`
		DayOfWeek int `json:"day_of_week"`
	} `json:"request_time_utc"`
	Geo struct {
		Country string `json:"country"`
		City    string `json:"city"`
	} `json:"geo"`
	DeviceClass string `json:"device_class"`
	Candidate   struct {
		Tier          int     `json:"tier"`
		Priority      int32   `json:"priority"`
		PacingDeficit float64 `json:"pacing_deficit"`
		ECPMMinorUnits int64  `json:"ecpm_minor_units"`
		BannerWidth   int32   `json:"banner_width"`
		BannerHeight  int32   `json:"banner_height"`
		CreativeType  string  `json:"creative_type"`
	} `json:"candidate"`
	CandidateCount                uint32 `json:"candidate_count"`
	CampaignDeliveredImpressions  int64  `json:"campaign_delivered_impressions"`
	CampaignGoalImpressions       int64  `json:"campaign_goal_impressions"`
	CampaignDeliveredClicks       int64  `json:"campaign_delivered_clicks"`
}

// expectedVector uses json.Number to handle both int and float values faithfully.
type expectedVector struct {
	// We unmarshal the whole expected_vector_computed as a map so we can iterate
	// over indices generically. Fields starting with "index_N_" map to position N.
	Index0  json.Number `json:"index_0_zone_width_bucket"`
	Index1  json.Number `json:"index_1_zone_height_bucket"`
	Index2  json.Number `json:"index_2_zone_aspect_ratio_bucket"`
	Index3  json.Number `json:"index_3_hour_of_day"`
	Index4  json.Number `json:"index_4_day_of_week"`
	Index5  json.Number `json:"index_5_is_weekend"`
	Index6  json.Number `json:"index_6_geo_country_hash"`
	Index7  json.Number `json:"index_7_geo_city_hash"`
	Index8  json.Number `json:"index_8_device_class_hash"`
	Index9  json.Number `json:"index_9_is_bot"`
	Index10 json.Number `json:"index_10_candidate_tier"`
	Index11 json.Number `json:"index_11_campaign_priority"`
	Index12 json.Number `json:"index_12_pacing_deficit"`
	Index13 json.Number `json:"index_13_pacing_deficit_bucket"`
	Index14 json.Number `json:"index_14_ecpm_minor_units_log1p"`
	Index15 json.Number `json:"index_15_ecpm_tier_bucket"`
	Index16 json.Number `json:"index_16_banner_width_bucket"`
	Index17 json.Number `json:"index_17_banner_height_bucket"`
	Index18 json.Number `json:"index_18_banner_size_match"`
	Index19 json.Number `json:"index_19_creative_type"`
	Index20 json.Number `json:"index_20_candidate_count_log1p"`
	Index21 json.Number `json:"index_21_campaign_ctr_estimate"`
	Index22 json.Number `json:"index_22_campaign_delivery_ratio"`
}

// toSlice converts the expectedVector struct into an ordered []float64 slice.
func (e expectedVector) toSlice() [FeatureVectorLength]float64 {
	nums := [FeatureVectorLength]json.Number{
		e.Index0, e.Index1, e.Index2, e.Index3, e.Index4,
		e.Index5, e.Index6, e.Index7, e.Index8, e.Index9,
		e.Index10, e.Index11, e.Index12, e.Index13, e.Index14,
		e.Index15, e.Index16, e.Index17, e.Index18, e.Index19,
		e.Index20, e.Index21, e.Index22,
	}
	var out [FeatureVectorLength]float64
	for i, n := range nums {
		f, err := n.Float64()
		if err != nil {
			// Should never happen with well-formed fixtures.
			panic("parity_test: bad json.Number at index " + string(rune('0'+i)) + ": " + err.Error())
		}
		out[i] = f
	}
	return out
}

// ---------------------------------------------------------------------------
// Test
// ---------------------------------------------------------------------------

// TestParityFromFixtures loads ml/features/testdata/parity_cases.json and
// verifies that Featurize produces the same vector as the Python gold values.
//
// This is the gate that enforces anti-skew between Go serving and Python training.
// Float tolerance: 1e-6 (per feature_spec.yaml parity_test_tolerance).
// Integer/bucket indices: exact equality.
func TestParityFromFixtures(t *testing.T) {
	t.Helper()

	data, err := os.ReadFile("../../ml/features/testdata/parity_cases.json")
	if err != nil {
		t.Fatalf("cannot read parity fixtures: %v", err)
	}

	var fixtures parityFixtures
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("cannot parse parity fixtures JSON: %v", err)
	}

	// Verify the fixture spec version matches this implementation.
	if fixtures.FeatureSpecVersion != FeatureSpecVersion {
		t.Fatalf("feature_spec_version mismatch: fixture=%q impl=%q — "+
			"update the featurization or the fixtures, then re-run",
			fixtures.FeatureSpecVersion, FeatureSpecVersion)
	}

	if len(fixtures.Cases) == 0 {
		t.Fatal("parity fixtures contain 0 cases — fixture file is malformed")
	}

	// Float tolerance from feature_spec.yaml parity_test_tolerance.float_abs.
	const floatTol = 1e-6

	// Indices that are integer/bucket types (must be exact, per spec).
	// All others are float (log1p, ratio, pacing_deficit).
	intIndices := map[int]bool{
		0: true, 1: true, 2: true, 3: true, 4: true, 5: true,
		6: true, 7: true, 8: true, 9: true, 10: true, 11: true,
		13: true, 15: true, 16: true, 17: true, 18: true, 19: true,
	}

	for _, tc := range fixtures.Cases {
		tc := tc // capture
		t.Run(tc.ID, func(t *testing.T) {
			inp := buildFeaturizeInput(tc)
			actual := Featurize(inp)
			expected := tc.Expected.toSlice()

			for i := 0; i < FeatureVectorLength; i++ {
				got := float64(actual[i])
				want := expected[i]
				diff := math.Abs(got - want)

				if intIndices[i] {
					// Integer/bucket: exact equality.
					if diff != 0 {
						t.Errorf("case %q index %d (%s): got %v want %v (exact int/bucket mismatch)",
							tc.ID, i, featureName(i), actual[i], want)
					}
				} else {
					// Float: tolerance 1e-6.
					if diff > floatTol {
						t.Errorf("case %q index %d (%s): got %.10f want %.10f diff=%.2e (tolerance=1e-6)",
							tc.ID, i, featureName(i), got, want, diff)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Input builder — maps JSON fixture fields to FeaturizeInput
// ---------------------------------------------------------------------------

// buildFeaturizeInput converts a parityCase's input into a FeaturizeInput.
// The RequestTime is constructed from the fixture's hour and day_of_week.
// We use a Monday of a known week as base and add the day offset.
func buildFeaturizeInput(tc parityCase) FeaturizeInput {
	inp := tc.Input

	// Build a time.Time in UTC with the given hour and day_of_week.
	// Use the epoch week starting Monday 1970-01-05 (which is a Monday).
	// day_of_week: 0=Sunday, 1=Monday, ..., 6=Saturday (time.Weekday).
	//
	// We need to find a date where Weekday() == inp.RequestTimeUTC.DayOfWeek.
	// Epoch 1970-01-04 is a Sunday (time.Sunday == 0).
	// So adding DayOfWeek days to 1970-01-04 gives the right weekday.
	baseDate := time.Date(1970, 1, 4, 0, 0, 0, 0, time.UTC) // Sunday
	requestTime := baseDate.
		AddDate(0, 0, inp.RequestTimeUTC.DayOfWeek).
		Add(time.Duration(inp.RequestTimeUTC.Hour) * time.Hour)

	return FeaturizeInput{
		ZoneWidth:  inp.Zone.Width,
		ZoneHeight: inp.Zone.Height,

		RequestTime: requestTime,

		GeoCountry:  inp.Geo.Country,
		GeoCity:     inp.Geo.City,
		DeviceClass: inp.DeviceClass,

		CandidateTier:    inp.Candidate.Tier,
		CampaignPriority: inp.Candidate.Priority,
		PacingDeficit:    inp.Candidate.PacingDeficit,
		ECPMMinorUnits:   inp.Candidate.ECPMMinorUnits,
		BannerWidth:      inp.Candidate.BannerWidth,
		BannerHeight:     inp.Candidate.BannerHeight,
		CreativeType:     inp.Candidate.CreativeType,

		CandidateCount:                inp.CandidateCount,
		CampaignDeliveredImpressions:  inp.CampaignDeliveredImpressions,
		CampaignGoalImpressions:       inp.CampaignGoalImpressions,
		CampaignDeliveredClicks:       inp.CampaignDeliveredClicks,
	}
}

// featureName maps a vector index to a human-readable name for error messages.
func featureName(i int) string {
	names := [FeatureVectorLength]string{
		"zone_width_bucket",
		"zone_height_bucket",
		"zone_aspect_ratio_bucket",
		"hour_of_day",
		"day_of_week",
		"is_weekend",
		"geo_country_hash",
		"geo_city_hash",
		"device_class_hash",
		"is_bot",
		"candidate_tier",
		"campaign_priority",
		"pacing_deficit",
		"pacing_deficit_bucket",
		"ecpm_minor_units_log1p",
		"ecpm_tier_bucket",
		"banner_width_bucket",
		"banner_height_bucket",
		"banner_size_match",
		"creative_type",
		"candidate_count_log1p",
		"campaign_ctr_estimate",
		"campaign_delivery_ratio",
	}
	if i >= 0 && i < FeatureVectorLength {
		return names[i]
	}
	return "unknown"
}
