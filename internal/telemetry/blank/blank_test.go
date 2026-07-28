package blank

import (
	"testing"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
)

// TestComputeBlankBillable exercises the PRODUCTION function directly
// (not a reimplementation) with the blank/billable matrix required by
// CA-6/CA-2: a blank impression must never be billable.
func TestComputeBlankBillable(t *testing.T) {
	cases := []struct {
		name       string
		servedTier commonv1.ServedTier
		bannerID   string
		wantBlank  bool
		wantBill   bool
	}{
		{
			name:       "blank-tier-no-banner",
			servedTier: commonv1.ServedTier_SERVED_TIER_BLANK,
			bannerID:   "",
			wantBlank:  true,
			wantBill:   false,
		},
		{
			name:       "blank-tier-with-banner-id",
			servedTier: commonv1.ServedTier_SERVED_TIER_BLANK,
			bannerID:   "ban-x",
			wantBlank:  true,
			wantBill:   false,
		},
		{
			name:       "remnant-with-banner-billable",
			servedTier: commonv1.ServedTier_SERVED_TIER_REMNANT,
			bannerID:   "ban-1",
			wantBlank:  false,
			wantBill:   true,
		},
		{
			name:       "no-banner-id-is-blank",
			servedTier: commonv1.ServedTier_SERVED_TIER_REMNANT,
			bannerID:   "",
			wantBlank:  true,
			wantBill:   false,
		},
		{
			name:       "unspecified-tier-never-billable",
			servedTier: commonv1.ServedTier_SERVED_TIER_UNSPECIFIED,
			bannerID:   "ban-1",
			wantBlank:  false,
			wantBill:   false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			blank, billable := ComputeBlankBillable(tc.servedTier, tc.bannerID)
			if blank != tc.wantBlank {
				t.Errorf("blank: got %v, want %v", blank, tc.wantBlank)
			}
			if billable != tc.wantBill {
				t.Errorf("billable: got %v, want %v", billable, tc.wantBill)
			}
			if blank && billable {
				t.Fatalf("CRITICAL billing invariant violated: blank=true and billable=true simultaneously (case %q)", tc.name)
			}
		})
	}
}
