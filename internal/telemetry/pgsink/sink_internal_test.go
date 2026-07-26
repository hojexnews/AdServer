// sink_internal_test.go — white-box unit tests (package pgsink, not
// pgsink_test): exercises unexported mapping logic (clampBillable,
// parseCampaignID, freeTextBlob, enqueue/drop-counting) without any
// Postgres connection. The DB-backed proof of idempotency/RLS lives in
// pgsink_integration_test.go (//go:build integration).
package pgsink

import (
	"log/slog"
	"testing"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/privacyscan"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func newTestSink(queueDepth int) *PostgresSink {
	return &PostgresSink{
		logger: discardLogger(),
		queue:  make(chan pendingEvent, queueDepth),
	}
}

// ---------------------------------------------------------------------------
// clampBillable — CA-6 pure invariant (mirrors the matrix in
// internal/telemetry/blank/blank_test.go, but this is a DIFFERENT function:
// clampBillable does not recompute blank/billable from servedTier/bannerID,
// it only clamps an already-computed pair — see package doc "CA-6").
// ---------------------------------------------------------------------------

func TestClampBillable_Matrix(t *testing.T) {
	cases := []struct {
		name          string
		blank         bool
		billable      bool
		wantClamped   bool
		wantViolation bool
	}{
		{"blank-not-billable", true, false, false, false},
		{"not-blank-billable", false, true, true, false},
		{"neither", false, false, false, false},
		{"contradictory-blank-and-billable", true, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clamped, violated := clampBillable(tc.blank, tc.billable)
			if clamped != tc.wantClamped {
				t.Errorf("clamped: got %v, want %v", clamped, tc.wantClamped)
			}
			if violated != tc.wantViolation {
				t.Errorf("violated: got %v, want %v", violated, tc.wantViolation)
			}
			// CA-6 invariant, unconditionally: the clamp must never hand back
			// a (blank=true, clamped=true) pair.
			if tc.blank && clamped {
				t.Fatalf("CRITICAL: CA-6 invariant violated by clampBillable itself: blank=true and clamped(billable)=true")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseCampaignID — string query param -> *int64 BIGINT column.
// ---------------------------------------------------------------------------

func TestParseCampaignID_Matrix(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  *int64
	}{
		{"empty", "", nil},
		{"non-numeric-garbage", "not-a-number", nil},
		{"non-numeric-with-digits", "camp-1", nil},
		{"valid-positive", "42", ptrInt64(42)},
		{"valid-zero", "0", ptrInt64(0)},
		{"negative-still-parses", "-1", ptrInt64(-1)}, // parsing is type-level, not a business-rule check
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCampaignID(tc.input)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("parseCampaignID(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if got != nil && *got != *tc.want {
				t.Fatalf("parseCampaignID(%q) = %d, want %d", tc.input, *got, *tc.want)
			}
		})
	}
}

func ptrInt64(v int64) *int64 { return &v }

// ---------------------------------------------------------------------------
// EmitImpression — mapping test: exercises the REAL exported method,
// reading the enqueued pendingEvent back off the channel (drainLoop is never
// started in this test — s.pool is nil, so nothing attempts real I/O).
// ---------------------------------------------------------------------------

func TestEmitImpression_MapsFieldsAndClampsCA6(t *testing.T) {
	s := newTestSink(4)

	s.EmitImpression("tenant-1", "42", "ban-1", "zone-1",
		commonv1.ServedTier_SERVED_TIER_BLANK, true /* billable (bad input) */, true, /* blank */
		"decision-1", "model-1")

	select {
	case ev := <-s.queue:
		if ev.eventType != eventTypeImpression {
			t.Errorf("eventType = %q, want %q", ev.eventType, eventTypeImpression)
		}
		if ev.tenantID != "tenant-1" || ev.bannerID != "ban-1" || ev.zoneID != "zone-1" {
			t.Errorf("unexpected id mapping: %+v", ev)
		}
		if ev.campaignID == nil || *ev.campaignID != 42 {
			t.Errorf("campaignID = %v, want *42", ev.campaignID)
		}
		if ev.servedTier != commonv1.ServedTier_SERVED_TIER_BLANK.String() {
			t.Errorf("servedTier = %q, want %q", ev.servedTier, commonv1.ServedTier_SERVED_TIER_BLANK.String())
		}
		if !ev.blank {
			t.Error("blank should stay true (never altered by the clamp)")
		}
		if ev.billable {
			t.Error("billable should have been clamped to false (CA-6: blank && billable is contradictory)")
		}
		if ev.eventID == "" {
			t.Error("eventID must be a freshly-minted ULID, got empty string")
		}
	default:
		t.Fatal("expected exactly one event enqueued")
	}

	if got := s.CA6ClampedCount(); got != 1 {
		t.Errorf("CA6ClampedCount() = %d, want 1", got)
	}
}

func TestEmitImpression_HealthyPathDoesNotClamp(t *testing.T) {
	s := newTestSink(4)
	s.EmitImpression("tenant-1", "42", "ban-1", "zone-1",
		commonv1.ServedTier_SERVED_TIER_REMNANT, true, false, "decision-1", "")

	ev := <-s.queue
	if !ev.billable || ev.blank {
		t.Errorf("healthy remnant impression should be billable=true blank=false, got %+v", ev)
	}
	if got := s.CA6ClampedCount(); got != 0 {
		t.Errorf("CA6ClampedCount() = %d, want 0 for a non-contradictory input", got)
	}
}

// A malformed "cid" (not a valid campaign id) must not crash mapping — the
// event is still enqueued with campaignID=nil (see parseCampaignID).
func TestEmitImpression_MalformedCampaignIDBecomesNil(t *testing.T) {
	s := newTestSink(4)
	s.EmitImpression("tenant-1", "not-a-number", "ban-1", "zone-1",
		commonv1.ServedTier_SERVED_TIER_REMNANT, true, false, "decision-1", "")

	ev := <-s.queue
	if ev.campaignID != nil {
		t.Errorf("campaignID = %v, want nil for an unparsable cid", *ev.campaignID)
	}
}

func TestEmitClick_MapsFields(t *testing.T) {
	s := newTestSink(4)
	s.EmitClick("tenant-1", "42", "ban-1", "zone-1", "https://advertiser.example/landing",
		"decision-1", "model-1")

	ev := <-s.queue
	if ev.eventType != eventTypeClick {
		t.Errorf("eventType = %q, want %q", ev.eventType, eventTypeClick)
	}
	if ev.destURL != "https://advertiser.example/landing" {
		t.Errorf("destURL = %q, unexpected", ev.destURL)
	}
	if ev.campaignID == nil || *ev.campaignID != 42 {
		t.Errorf("campaignID = %v, want *42", ev.campaignID)
	}
}

func TestEmitConversion_MapsFieldsAndOmitsMoney(t *testing.T) {
	s := newTestSink(4)
	s.EmitConversion("tenant-1", "42", "ban-1", "attribution-decision-1", "decision-1", "model-1")

	ev := <-s.queue
	if ev.eventType != eventTypeConversion {
		t.Errorf("eventType = %q, want %q", ev.eventType, eventTypeConversion)
	}
	if ev.attributionDecisionID != "attribution-decision-1" {
		t.Errorf("attributionDecisionID = %q, unexpected", ev.attributionDecisionID)
	}
	if ev.campaignID == nil || *ev.campaignID != 42 {
		t.Errorf("campaignID = %v, want *42", ev.campaignID)
	}
}

// EmitAdRequest DOES persist (contract adjustment — see package doc
// "Scope"), but ONLY tenant_id/zone_id/decision_id. campaign_id must always
// be nil (AdRequest precedes campaign selection) and none of geo/uaClass/
// refererURL/cachebuster/customVars have any field to land in at all
// (pendingEvent structurally has no such fields) — this test proves both
// the presence of the narrow mapping and the absence of anything wider.
func TestEmitAdRequest_PersistsNarrowMappingOnly(t *testing.T) {
	s := newTestSink(4)
	s.EmitAdRequest("tenant-1", "zone-1", "site-1", &commonv1.Geo{Country: "BR"},
		"chrome-desktop", "https://publisher.example/", "cb123",
		map[string]string{"gender": "male"}, "decision-1")

	if n := len(s.queue); n != 1 {
		t.Fatalf("EmitAdRequest enqueued %d event(s), want exactly 1", n)
	}
	ev := <-s.queue
	if ev.eventType != eventTypeAdRequest {
		t.Errorf("eventType = %q, want %q", ev.eventType, eventTypeAdRequest)
	}
	if ev.tenantID != "tenant-1" {
		t.Errorf("tenantID = %q, want %q", ev.tenantID, "tenant-1")
	}
	if ev.zoneID != "zone-1" {
		t.Errorf("zoneID = %q, want %q", ev.zoneID, "zone-1")
	}
	if ev.decisionID != "decision-1" {
		t.Errorf("decisionID = %q, want %q", ev.decisionID, "decision-1")
	}
	if ev.campaignID != nil {
		t.Errorf("campaignID = %v, want nil (AdRequest precedes campaign selection)", *ev.campaignID)
	}
	// pendingEvent has no field for geo/uaClass/refererURL/cachebuster/
	// customVars/siteID at all — there is nowhere for them to have landed;
	// this is a structural guarantee, not just a runtime check. The
	// remaining fields should be at their zero values.
	if ev.bannerID != "" || ev.modelVersion != "" || ev.servedTier != "" ||
		ev.destURL != "" || ev.attributionDecisionID != "" {
		t.Errorf("unexpected non-zero field(s) on an AdRequest pendingEvent: %+v", ev)
	}
	if got := s.DroppedCount(); got != 0 {
		t.Errorf("DroppedCount() = %d, want 0", got)
	}
}

// TestEmitAdRequest_EmptyTenantIsSkippedAndCounted guards the behaviour found
// by running the real stack: GET /asyncjs?zoneid=1001 (the canonical ad tag,
// which carries no `?tid=`) produced tenantID == "" and the insert failed with
//
//	ERROR: invalid input syntax for type uuid: "" (SQLSTATE 22P02)
//
// once PER REQUEST — on the highest-volume event of the funnel.
//
// The contract is: discard early, count it, never enqueue a row that cannot
// be inserted. Deleting the empty-tenant guard in EmitAdRequest turns this
// test red on the queue-length assertion.
func TestEmitAdRequest_EmptyTenantIsSkippedAndCounted(t *testing.T) {
	s := newTestSink(4)

	s.EmitAdRequest("", "zone-1", "site-1", &commonv1.Geo{Country: "BR"},
		"chrome-desktop", "https://publisher.example/", "cb123",
		map[string]string{"gender": "male"}, "decision-1")

	if n := len(s.queue); n != 0 {
		t.Fatalf("EmitAdRequest with empty tenant enqueued %d event(s), want 0 — "+
			"tenant_id is NOT NULL uuid under FORCE RLS, so the row can never be inserted", n)
	}
	if got := s.SkippedNoTenantCount(); got != 1 {
		t.Errorf("SkippedNoTenantCount() = %d, want 1 — the gap must be observable, not silent", got)
	}
	// Not a queue-overflow drop: the two counters must stay distinguishable.
	if got := s.DroppedCount(); got != 0 {
		t.Errorf("DroppedCount() = %d, want 0 (an unattributable ad_request is not backpressure)", got)
	}

	// A tag that DOES carry ?tid= keeps working, unchanged.
	s.EmitAdRequest("tenant-1", "zone-1", "", nil, "", "", "", nil, "decision-2")
	if n := len(s.queue); n != 1 {
		t.Fatalf("EmitAdRequest with a tenant enqueued %d event(s), want 1", n)
	}
	if got := s.SkippedNoTenantCount(); got != 1 {
		t.Errorf("SkippedNoTenantCount() = %d after a valid emit, want 1 (unchanged)", got)
	}
}

// ---------------------------------------------------------------------------
// enqueue — non-blocking hot path: overflow must drop and COUNT, never block.
// ---------------------------------------------------------------------------

func TestEnqueue_DropsAndCountsOnFullQueue(t *testing.T) {
	s := newTestSink(1) // depth 1: second enqueue must overflow

	s.enqueue(pendingEvent{eventID: "e1", eventType: eventTypeImpression, occurredAt: time.Now()})
	s.enqueue(pendingEvent{eventID: "e2", eventType: eventTypeImpression, occurredAt: time.Now()})
	s.enqueue(pendingEvent{eventID: "e3", eventType: eventTypeImpression, occurredAt: time.Now()})

	if got := s.DroppedCount(); got != 2 {
		t.Fatalf("DroppedCount() = %d, want 2 (queue depth 1, three enqueue calls)", got)
	}
	if n := len(s.queue); n != 1 {
		t.Fatalf("queue length = %d, want 1 (the first successfully-enqueued event)", n)
	}
	kept := <-s.queue
	if kept.eventID != "e1" {
		t.Errorf("kept event id = %q, want %q (first writer wins on overflow)", kept.eventID, "e1")
	}
}

// This overflow behaviour applies uniformly to EVERY event type, including
// ad_request — the highest-volume type (every page load, not just every
// filled impression). There is no separate/priority queue for
// impression/click/conversion vs. ad_request; a sustained ad_request burst
// shares the SAME channel and could crowd out the lower-volume, higher-value
// event types under sustained overload. Documented, not silent: both would
// increment the SAME DroppedCount counter, which is exported for monitoring.
func TestEnqueue_AdRequestSharesQueueAndDropCounterWithOtherTypes(t *testing.T) {
	s := newTestSink(1)
	s.EmitAdRequest("tenant-1", "zone-1", "site-1", nil, "", "", "", nil, "")
	s.EmitImpression("tenant-1", "42", "ban-1", "zone-1",
		commonv1.ServedTier_SERVED_TIER_REMNANT, true, false, "decision-1", "")

	if got := s.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount() = %d, want 1 (second Emit* call overflowed the shared queue)", got)
	}
}

// ---------------------------------------------------------------------------
// freeTextBlob / TX-5 privacy scan wiring — proves the SAME reused
// internal/privacyscan detector actually sees an IP literal embedded in an
// attacker-controlled free-text field (dest_url here), before write() ever
// reaches Postgres. The write() call itself is exercised only in the
// integration test (needs a live pool); this test proves the detector input
// construction is correct.
// ---------------------------------------------------------------------------

func TestFreeTextBlob_CarriesIPLiteralForScanning(t *testing.T) {
	clean := pendingEvent{
		tenantID: "tenant-1", bannerID: "ban-1", zoneID: "zone-1",
		decisionID: "decision-1", modelVersion: "", destURL: "https://advertiser.example/landing",
	}
	dirty := clean
	// TEST-NET-3 (RFC 5737), never a real client — safe placeholder embedded
	// in an attacker-reachable field (dest_url comes straight from banner
	// config in production, but the collector's other free-text fields are
	// unvalidated query params — see package doc "Privacy").
	dirty.destURL = "https://advertiser.example/landing?x=203.0.113.5"

	if privacyscan.ContainsIPLiteral(freeTextBlob(clean)) {
		t.Error("clean event unexpectedly flagged as containing an IP literal")
	}
	if !privacyscan.ContainsIPLiteral(freeTextBlob(dirty)) {
		t.Error("dirty event (embedded IP literal in dest_url) was NOT flagged — TX-5/DA-11 gap")
	}
}
