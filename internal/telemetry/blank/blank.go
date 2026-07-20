// Package blank holds a small, pure delivery-invariant helper extracted out
// of the collector's HTTP handlers so the exact production decision can be
// exercised both by the running binary (services/collector) and by the
// CA-6/CA-2 parity golden tests (tests/parity) — without the tests
// reimplementing the logic against themselves.
//
// It lives under internal/ (ADR-0002 §A: pacotes Go compartilhados entre
// binários vivem em internal/, não sob services/<svc>/cmd/<bin>/) because it
// is imported from OUTSIDE the collector binary's own module tree (the
// tests/parity golden suite). It is named "blank", not "billing", to avoid
// colliding with the money-ledger/billing domain (db/ledger, data/iceberg
// billing batch, DA-7): this package classifies an impression event as
// blank/billable for the telemetry envelope emitted at the /lg pixel — it
// does not compute money, does not touch the ledger, and is not the
// reconciled source of truth for revenue (that is the Iceberg billing batch,
// per ADR-0002 §A "o billing reconcilia contra Iceberg, nunca contra o
// streaming").
//
// This package is intentionally tiny and dependency-free: it must remain
// trivially auditable, because it enforces the single most important
// billing invariant in the system (stack §5, risk #1): a blank impression
// (no creative actually rendered) is NEVER billable.
package blank

import commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"

// ComputeBlankBillable implements the CA-6 (§4.7) / CA-2 (DA-3 cascade)
// billing invariant for the /lg impression pixel.
//
//   - blank is true when nothing was actually rendered: the served tier is
//     explicitly BLANK, or no banner was selected (bannerID == "").
//   - billable is true only when the impression is NOT blank AND the served
//     tier is a known, specific tier (an UNSPECIFIED tier means the "tier"
//     query param was missing or malformed — we cannot attest what, if
//     anything, was served, so it must never be billed either).
//
// This is the single source of truth for the invariant: both the /lg
// handler (handleImpression, in services/collector/cmd/collector) and the
// CA-6 parity golden test (tests/parity/ca6_telemetry_golden_test.go) call
// this exact function. There must be no second implementation anywhere.
func ComputeBlankBillable(servedTier commonv1.ServedTier, bannerID string) (blank, billable bool) {
	blank = servedTier == commonv1.ServedTier_SERVED_TIER_BLANK || bannerID == ""
	billable = !blank && servedTier != commonv1.ServedTier_SERVED_TIER_UNSPECIFIED
	return blank, billable
}
