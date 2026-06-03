package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/geo"
)

// ---------------------------------------------------------------------------
// capturesSink — records emitted events for assertion
// ---------------------------------------------------------------------------

type capturedImpression struct {
	tenantID, campaignID, bannerID, zoneID string
	servedTier                              commonv1.ServedTier
	billable, blank                         bool
	decisionID                              string
}

type capturedClick struct {
	tenantID, campaignID, bannerID, zoneID, destURL, decisionID string
}

type capturedConversion struct {
	tenantID, campaignID, bannerID, attributionDecisionID, decisionID string
}

type capturedAdRequest struct {
	tenantID, zoneID, decisionID string
}

type captureSink struct {
	impressions  []capturedImpression
	clicks       []capturedClick
	conversions  []capturedConversion
	adRequests   []capturedAdRequest
}

func (s *captureSink) EmitImpression(tenantID, campaignID, bannerID, zoneID string,
	servedTier commonv1.ServedTier, billable, blank bool, decisionID, _ string) {
	s.impressions = append(s.impressions, capturedImpression{
		tenantID: tenantID, campaignID: campaignID, bannerID: bannerID,
		zoneID: zoneID, servedTier: servedTier, billable: billable,
		blank: blank, decisionID: decisionID,
	})
}

func (s *captureSink) EmitClick(tenantID, campaignID, bannerID, zoneID, destURL, decisionID, _ string) {
	s.clicks = append(s.clicks, capturedClick{
		tenantID: tenantID, campaignID: campaignID, bannerID: bannerID,
		zoneID: zoneID, destURL: destURL, decisionID: decisionID,
	})
}

func (s *captureSink) EmitConversion(tenantID, campaignID, bannerID, attributionDecisionID, decisionID, _ string) {
	s.conversions = append(s.conversions, capturedConversion{
		tenantID: tenantID, campaignID: campaignID, bannerID: bannerID,
		attributionDecisionID: attributionDecisionID, decisionID: decisionID,
	})
}

func (s *captureSink) EmitAdRequest(tenantID, zoneID, _ string, _ *commonv1.Geo,
	_, _, _ string, _ map[string]string, decisionID string) {
	s.adRequests = append(s.adRequests, capturedAdRequest{
		tenantID: tenantID, zoneID: zoneID, decisionID: decisionID,
	})
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func newTestHandler(sink EventSink) *collectorHandler {
	return &collectorHandler{
		geoResolver: geo.NewStubResolver("BR", "Sao Paulo"),
		sink:        sink,
		decisionURL: "http://localhost:8080",
		logger:      nil,
	}
}

// ---------------------------------------------------------------------------
// CA-6: /lg — impression counted at pixel load only
// ---------------------------------------------------------------------------

func TestLG_ImpressionCountedAtPixelLoad(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	q := url.Values{
		"did":  {"dec-123"},
		"bid":  {"ban-1"},
		"cid":  {"camp-1"},
		"zid":  {"zone-1"},
		"tid":  {"tenant-1"},
		"tier": {"SERVED_TIER_REMNANT"},
	}
	req := httptest.NewRequest(http.MethodGet, "/lg?"+q.Encode(), nil)
	rr := httptest.NewRecorder()

	h.handleImpression(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if len(sink.impressions) != 1 {
		t.Fatalf("expected 1 impression event, got %d", len(sink.impressions))
	}
	imp := sink.impressions[0]
	if imp.decisionID != "dec-123" {
		t.Errorf("decision_id: got %q, want %q", imp.decisionID, "dec-123")
	}
	if imp.bannerID != "ban-1" {
		t.Errorf("banner_id: got %q, want %q", imp.bannerID, "ban-1")
	}
	if imp.blank {
		t.Error("expected non-blank impression")
	}
	if !imp.billable {
		t.Error("expected billable impression")
	}
	// Pixel must be the 1×1 GIF.
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "image/gif") {
		t.Errorf("Content-Type: got %q, want image/gif", ct)
	}
}

// CA-6: /ck — click emits event and returns 302 to validated URL.
func TestCK_ClickEmitsEventAndRedirects(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	q := url.Values{
		"did":  {"dec-456"},
		"bid":  {"ban-2"},
		"cid":  {"camp-2"},
		"zid":  {"zone-2"},
		"tid":  {"tenant-1"},
		"dest": {"https://example.com/landing"},
	}
	req := httptest.NewRequest(http.MethodGet, "/ck?"+q.Encode(), nil)
	rr := httptest.NewRecorder()

	h.handleClick(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rr.Code)
	}
	location := rr.Header().Get("Location")
	if location != "https://example.com/landing" {
		t.Errorf("Location: got %q, want %q", location, "https://example.com/landing")
	}
	if len(sink.clicks) != 1 {
		t.Fatalf("expected 1 click event, got %d", len(sink.clicks))
	}
	if sink.clicks[0].decisionID != "dec-456" {
		t.Errorf("click decision_id: got %q", sink.clicks[0].decisionID)
	}
}

// SSRF/open-redirect: /ck must reject private-IP destinations.
func TestCK_PrivateIPDestRejected(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	cases := []string{
		"http://127.0.0.1/evil",
		"http://192.168.1.1/evil",
		"http://10.0.0.1/evil",
		"file:///etc/passwd",
		"ftp://example.com/file",
		"http://localhost/evil",
	}
	for _, dest := range cases {
		q := url.Values{
			"did": {"dec-x"}, "bid": {"b"}, "cid": {"c"},
			"zid": {"z"}, "tid": {"t"}, "dest": {dest},
		}
		req := httptest.NewRequest(http.MethodGet, "/ck?"+q.Encode(), nil)
		rr := httptest.NewRecorder()
		h.handleClick(rr, req)
		if rr.Code == http.StatusFound {
			t.Errorf("dest %q: expected rejection, got 302", dest)
		}
	}
	// No click events should have been emitted for invalid destinations.
	if len(sink.clicks) != 0 {
		t.Errorf("expected 0 click events for invalid dests, got %d", len(sink.clicks))
	}
}

// CA-6: /ct — conversion pixel returns 200 with pixel body.
func TestCT_ConversionPixelEmitsEvent(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	q := url.Values{
		"did":  {"dec-789"},
		"bid":  {"ban-3"},
		"cid":  {"camp-3"},
		"tid":  {"tenant-1"},
		"adid": {"origin-dec-001"},
	}
	req := httptest.NewRequest(http.MethodGet, "/ct?"+q.Encode(), nil)
	rr := httptest.NewRecorder()

	h.handleConversion(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if len(sink.conversions) != 1 {
		t.Fatalf("expected 1 conversion event, got %d", len(sink.conversions))
	}
	conv := sink.conversions[0]
	if conv.decisionID != "dec-789" {
		t.Errorf("conversion decision_id: got %q", conv.decisionID)
	}
	if conv.attributionDecisionID != "origin-dec-001" {
		t.Errorf("attribution_decision_id: got %q", conv.attributionDecisionID)
	}
}

// /asyncjs — returns JavaScript with correct content-type.
func TestAsyncJS_ReturnsJavaScript(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	req := httptest.NewRequest(http.MethodGet, "/asyncjs?zoneid=zone-1&tid=t1&cb=12345", nil)
	rr := httptest.NewRecorder()

	h.handleAsyncJS(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("Content-Type: got %q, want application/javascript", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "async function") {
		t.Error("expected async function in JS body")
	}
}

// /asyncjs — missing zoneid → 400.
func TestAsyncJS_MissingZoneID(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)
	req := httptest.NewRequest(http.MethodGet, "/asyncjs?tid=t1", nil)
	rr := httptest.NewRecorder()
	h.handleAsyncJS(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// VAST 4.x endpoint returns valid XML.
func TestVAST_ReturnsXML(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	q := url.Values{
		"src":  {"https://cdn.example.com/video.mp4"},
		"did":  {"dec-vast-1"},
		"bid":  {"ban-v"},
		"cid":  {"camp-v"},
		"zid":  {"zone-v"},
		"tid":  {"t1"},
		"dest": {"https://example.com/landing"},
		"w":    {"640"},
		"h":    {"480"},
	}
	req := httptest.NewRequest(http.MethodGet, "/vast?"+q.Encode(), nil)
	rr := httptest.NewRecorder()

	h.handleVAST(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/xml") {
		t.Errorf("Content-Type: got %q, want application/xml", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `version="4.0"`) {
		t.Error("expected VAST 4.0 in response body")
	}
	if strings.Contains(body, "VPAID") {
		t.Error("VAST must NOT contain VPAID (DA-5)")
	}
	if !strings.Contains(body, "https://cdn.example.com/video.mp4") {
		t.Error("expected video URL in VAST body")
	}
}

// VAST 4.x with invalid src → rejected.
func TestVAST_PrivateSrcRejected(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	q := url.Values{
		"src": {"file:///etc/hosts"},
		"did": {"dec-x"}, "bid": {"b"}, "cid": {"c"},
		"zid": {"z"}, "tid": {"t"},
	}
	req := httptest.NewRequest(http.MethodGet, "/vast?"+q.Encode(), nil)
	rr := httptest.NewRecorder()
	h.handleVAST(rr, req)
	if rr.Code == http.StatusOK {
		t.Error("expected rejection of file:// scheme, got 200")
	}
}

// IP discard: resolveAndDiscardIP returns Geo without exposing IP.
func TestResolveAndDiscardIP_NoPIILeaks(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	req := httptest.NewRequest(http.MethodGet, "/asyncjs?zoneid=z1&tid=t1&cb=1", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.5") // test IP

	g := h.resolveAndDiscardIP(req)
	// The stub resolver always returns BR/Sao Paulo regardless of IP.
	if g == nil {
		t.Fatal("resolveAndDiscardIP returned nil")
	}
	// Crucially: no IP appears in the returned Geo struct.
	if g.GetCountry() == "203.0.113.5" || g.GetCity() == "203.0.113.5" {
		t.Error("IP leaked into Geo struct (TX-5 violation)")
	}
}

// validateDestURL table tests.
func TestValidateDestURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool // true = valid
	}{
		{"https://example.com/path?q=1", true},
		{"http://example.com", true},
		{"https://example.com:443/path", true},
		{"file:///etc/passwd", false},
		{"ftp://example.com/file", false},
		{"http://127.0.0.1/x", false},
		{"http://192.168.1.10/x", false},
		{"http://10.0.0.1/x", false},
		{"http://localhost/x", false},
		{"http://my-service.internal/x", false},
		{"", false},
		{"javascript:alert(1)", false},
	}
	for _, tc := range cases {
		err := validateDestURL(tc.url)
		got := err == nil
		if got != tc.want {
			t.Errorf("validateDestURL(%q): got valid=%v, want valid=%v (err=%v)",
				tc.url, got, tc.want, err)
		}
	}
}
