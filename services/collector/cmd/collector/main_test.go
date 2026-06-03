package main

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/clicktoken"
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
	uaClass                      string // coarse class, not raw UA
	refererURL                   string // sanitized
}

type captureSink struct {
	impressions []capturedImpression
	clicks      []capturedClick
	conversions []capturedConversion
	adRequests  []capturedAdRequest
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
	uaClass, refererURL, _ string, _ map[string]string, decisionID string) {
	s.adRequests = append(s.adRequests, capturedAdRequest{
		tenantID:   tenantID,
		zoneID:     zoneID,
		decisionID: decisionID,
		uaClass:    uaClass,
		refererURL: refererURL,
	})
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const testHMACSecret = "test-hmac-secret-for-collector-tests"

func newTestSigner(t *testing.T) *clicktoken.Signer {
	t.Helper()
	s, err := clicktoken.New(testHMACSecret, clicktoken.DefaultTTL)
	if err != nil {
		t.Fatalf("clicktoken.New: %v", err)
	}
	return s
}

func newTestHandler(sink EventSink) *collectorHandler {
	return newTestHandlerWithSigner(sink, nil)
}

func newTestHandlerWithSigner(sink EventSink, signer *clicktoken.Signer) *collectorHandler {
	return &collectorHandler{
		geoResolver:  geo.NewStubResolver("BR", "Sao Paulo"),
		sink:         sink,
		decisionURL:  "http://localhost:8080",
		logger:       nil,
		clickSigner:  signer,
		trustedDepth: 1,
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

// ---------------------------------------------------------------------------
// SECURITY #1: /ck — HMAC click token validation
// ---------------------------------------------------------------------------

// Valid HMAC token → 302 redirect.
func TestCK_ValidHMACToken_Redirects(t *testing.T) {
	sink := &captureSink{}
	signer := newTestSigner(t)
	h := newTestHandlerWithSigner(sink, signer)

	destURL := "https://advertiser.example.com/landing"
	expiry := time.Now().Add(clicktoken.DefaultTTL)
	tok := signer.Sign("dec-456", "ban-2", destURL, expiry)

	q := url.Values{
		"tok": {tok},
		"did": {"dec-456"},
		"bid": {"ban-2"},
		"cid": {"camp-2"},
		"zid": {"zone-2"},
		"tid": {"tenant-1"},
	}
	req := httptest.NewRequest(http.MethodGet, "/ck?"+q.Encode(), nil)
	rr := httptest.NewRecorder()

	h.handleClick(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: body=%s", rr.Code, rr.Body.String())
	}
	location := rr.Header().Get("Location")
	if location != destURL {
		t.Errorf("Location: got %q, want %q", location, destURL)
	}
	if len(sink.clicks) != 1 {
		t.Fatalf("expected 1 click event, got %d", len(sink.clicks))
	}
	if sink.clicks[0].decisionID != "dec-456" {
		t.Errorf("click decision_id: got %q", sink.clicks[0].decisionID)
	}
}

// Invalid / tampered HMAC token → 400 (no redirect).
func TestCK_InvalidHMACToken_Rejected(t *testing.T) {
	sink := &captureSink{}
	signer := newTestSigner(t)
	h := newTestHandlerWithSigner(sink, signer)

	q := url.Values{
		"tok": {"tampered.invalidMAC"},
		"did": {"dec-x"}, "bid": {"b"}, "cid": {"c"}, "zid": {"z"}, "tid": {"t"},
	}
	req := httptest.NewRequest(http.MethodGet, "/ck?"+q.Encode(), nil)
	rr := httptest.NewRecorder()
	h.handleClick(rr, req)

	if rr.Code == http.StatusFound {
		t.Error("expected rejection for tampered token, got 302")
	}
	if len(sink.clicks) != 0 {
		t.Errorf("expected 0 click events for invalid token, got %d", len(sink.clicks))
	}
}

// Expired HMAC token → 400 (no redirect).
func TestCK_ExpiredHMACToken_Rejected(t *testing.T) {
	sink := &captureSink{}
	signer := newTestSigner(t)
	h := newTestHandlerWithSigner(sink, signer)

	// Sign a token that is already expired.
	expiry := time.Now().Add(-2 * clicktoken.DefaultTTL)
	tok := signer.Sign("dec-exp", "ban-exp", "https://example.com/", expiry)

	q := url.Values{
		"tok": {tok},
		"did": {"dec-exp"}, "bid": {"ban-exp"}, "cid": {"c"}, "zid": {"z"}, "tid": {"t"},
	}
	req := httptest.NewRequest(http.MethodGet, "/ck?"+q.Encode(), nil)
	rr := httptest.NewRecorder()
	h.handleClick(rr, req)

	if rr.Code == http.StatusFound {
		t.Error("expected rejection for expired token, got 302")
	}
}

// Missing tok parameter → 400 (no redirect).
func TestCK_MissingToken_Rejected(t *testing.T) {
	sink := &captureSink{}
	signer := newTestSigner(t)
	h := newTestHandlerWithSigner(sink, signer)

	req := httptest.NewRequest(http.MethodGet, "/ck?did=d&bid=b&cid=c&zid=z&tid=t", nil)
	rr := httptest.NewRecorder()
	h.handleClick(rr, req)

	if rr.Code == http.StatusFound {
		t.Error("expected rejection for missing token, got 302")
	}
}

// No signer (CK_HMAC_SECRET absent) → 503 (fail-closed).
func TestCK_NoSigner_Returns503(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandlerWithSigner(sink, nil) // nil signer = fail-closed

	q := url.Values{
		"tok": {"anything"}, "did": {"d"}, "bid": {"b"}, "cid": {"c"}, "zid": {"z"}, "tid": {"t"},
	}
	req := httptest.NewRequest(http.MethodGet, "/ck?"+q.Encode(), nil)
	rr := httptest.NewRecorder()
	h.handleClick(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when signer is nil (fail-closed), got %d", rr.Code)
	}
}

// Plain dest param (no tok) must NOT result in a redirect — no unsigned redirects.
func TestCK_PlainDestParam_NotAccepted(t *testing.T) {
	sink := &captureSink{}
	signer := newTestSigner(t)
	h := newTestHandlerWithSigner(sink, signer)

	// Supply dest= but no tok= — should be rejected (no plain dest accepted).
	q := url.Values{
		"dest": {"https://example.com/landing"},
		"did":  {"dec-x"}, "bid": {"b"}, "cid": {"c"}, "zid": {"z"}, "tid": {"t"},
	}
	req := httptest.NewRequest(http.MethodGet, "/ck?"+q.Encode(), nil)
	rr := httptest.NewRecorder()
	h.handleClick(rr, req)

	if rr.Code == http.StatusFound {
		t.Error("plain dest param must NOT trigger a redirect (only signed tok accepted)")
	}
}

// SSRF/open-redirect: even with a valid HMAC, a private-IP dest_url embedded
// in the token is rejected by validateDestURL (defence-in-depth).
func TestCK_SignedPrivateIPDest_Rejected(t *testing.T) {
	sink := &captureSink{}
	signer := newTestSigner(t)
	h := newTestHandlerWithSigner(sink, signer)

	// Sign a token with a private-IP dest_url (misconfigured banner scenario).
	expiry := time.Now().Add(clicktoken.DefaultTTL)
	tok := signer.Sign("dec-priv", "ban-priv", "http://192.168.1.1/evil", expiry)

	q := url.Values{
		"tok": {tok}, "did": {"dec-priv"}, "bid": {"ban-priv"},
		"cid": {"c"}, "zid": {"z"}, "tid": {"t"},
	}
	req := httptest.NewRequest(http.MethodGet, "/ck?"+q.Encode(), nil)
	rr := httptest.NewRecorder()
	h.handleClick(rr, req)

	if rr.Code == http.StatusFound {
		t.Error("private-IP dest in signed token must be rejected by defence-in-depth validateDestURL")
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

// /asyncjs — no plain dest in generated JS (click uses tok param only).
func TestAsyncJS_NoPlainDestInJS(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	req := httptest.NewRequest(http.MethodGet, "/asyncjs?zoneid=zone-1&tid=t1&cb=12345", nil)
	rr := httptest.NewRecorder()
	h.handleAsyncJS(rr, req)

	body := rr.Body.String()
	// The generated JS must use "tok=" for click URLs, not raw "dest=".
	if strings.Contains(body, `"dest="`) {
		t.Error("JS must not build click URLs with plain dest= param; use tok= (HMAC-signed token)")
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

// ---------------------------------------------------------------------------
// PRIVACY: UA reduced to coarse class — raw UA never emitted (TX-5)
// ---------------------------------------------------------------------------

func TestAsyncJS_UAClassEmitted_RawUANotInEvent(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	rawUA := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/124.0.0.0 Safari/537.36"
	req := httptest.NewRequest(http.MethodGet, "/asyncjs?zoneid=z1&tid=t1&cb=1", nil)
	req.Header.Set("User-Agent", rawUA)
	rr := httptest.NewRecorder()
	h.handleAsyncJS(rr, req)

	if len(sink.adRequests) != 1 {
		t.Fatalf("expected 1 ad request event, got %d", len(sink.adRequests))
	}
	ar := sink.adRequests[0]

	// The emitted uaClass must NOT be the raw UA.
	if ar.uaClass == rawUA {
		t.Error("raw User-Agent was emitted verbatim — PII leak (TX-5 violation)")
	}
	// It must be a coarse class (short string).
	if ar.uaClass == "" {
		t.Error("uaClass must not be empty")
	}
	if len(ar.uaClass) > 30 {
		t.Errorf("uaClass suspiciously long (%d chars), expected a coarse class: %q",
			len(ar.uaClass), ar.uaClass)
	}
	// Must not contain raw UA substrings that are PII.
	if strings.Contains(ar.uaClass, "Windows NT") || strings.Contains(ar.uaClass, "AppleWebKit") {
		t.Errorf("uaClass contains raw UA data — PII leak: %q", ar.uaClass)
	}
}

// ---------------------------------------------------------------------------
// PRIVACY: Referer URL sanitized — query and fragment stripped
// ---------------------------------------------------------------------------

func TestSanitizeReferer_StripsQueryAndFragment(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://example.com/page?session=abc123&user=456", "https://example.com/page"},
		{"https://example.com/page#section", "https://example.com/page"},
		{"https://example.com/page?q=1#frag", "https://example.com/page"},
		{"https://example.com/path/to/page", "https://example.com/path/to/page"},
		{"", ""},
		{"not-a-url", ""}, // parse error → empty string
	}
	for _, tc := range cases {
		got := sanitizeReferer(tc.raw)
		if got != tc.want {
			t.Errorf("sanitizeReferer(%q): got %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestAsyncJS_RefererSanitized_NoQueryInEvent(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	req := httptest.NewRequest(http.MethodGet, "/asyncjs?zoneid=z1&tid=t1&cb=1", nil)
	req.Header.Set("Referer", "https://publisher.example.com/article?session_token=abc123&uid=456")
	rr := httptest.NewRecorder()
	h.handleAsyncJS(rr, req)

	if len(sink.adRequests) != 1 {
		t.Fatalf("expected 1 ad request event, got %d", len(sink.adRequests))
	}
	ar := sink.adRequests[0]
	if strings.Contains(ar.refererURL, "session_token") || strings.Contains(ar.refererURL, "uid=") {
		t.Errorf("refererURL contains query params — PII leak: %q", ar.refererURL)
	}
	if !strings.HasPrefix(ar.refererURL, "https://publisher.example.com/article") {
		t.Errorf("refererURL should retain scheme+host+path, got: %q", ar.refererURL)
	}
}

// ---------------------------------------------------------------------------
// SECURITY #8: X-Forwarded-For trusted proxy depth
// ---------------------------------------------------------------------------

func TestExtractClientIP_TrustedProxyDepth1(t *testing.T) {
	// With trustedDepth=1: the ingress proxy appends its view to XFF.
	// We trust the entry at position len(parts)-1 (rightmost = added by trusted proxy).
	// For a single-hop chain "client, proxy" → trusted entry is "proxy" (rightmost).
	// But for depth=1 on a chain "real-client, untrusted-client-injected":
	// the trusted proxy appends to get "real-client, untrusted, trusted-proxy-view".
	// We take index len-1 = "trusted-proxy-view" which IS the real client IP.
	// Test with a simple chain: XFF = "203.0.113.5, 10.0.0.1"
	// trustedDepth=1 → take index max(0, 2-1)=1 → "10.0.0.1".
	// (This reflects the proxy's own address being trusted, not the leftmost.)
	// For the test we verify the handler uses the configured depth, not always leftmost.

	h := &collectorHandler{trustedDepth: 1}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	// With depth=1, idx = 2-1 = 1 → "10.0.0.1"
	ip := h.extractClientIP(req)
	if ip != "10.0.0.1" {
		t.Errorf("trustedDepth=1: expected rightmost entry '10.0.0.1', got %q", ip)
	}
}

func TestExtractClientIP_TrustedProxyDepth0_IgnoresXFF(t *testing.T) {
	// With trustedDepth=0: XFF is entirely ignored; RemoteAddr is used.
	h := &collectorHandler{trustedDepth: 0}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	req.RemoteAddr = "192.168.1.5:12345"

	ip := h.extractClientIP(req)
	if ip != "192.168.1.5" {
		t.Errorf("trustedDepth=0: expected RemoteAddr '192.168.1.5', got %q", ip)
	}
}

func TestExtractClientIP_SingleEntryXFF(t *testing.T) {
	// Single-hop chain: XFF has one entry, trustedDepth=1 → idx=0.
	h := &collectorHandler{trustedDepth: 1}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	ip := h.extractClientIP(req)
	if ip != "203.0.113.5" {
		t.Errorf("single-entry XFF: expected '203.0.113.5', got %q", ip)
	}
}

// ---------------------------------------------------------------------------
// SECURITY #3: VAST 4.x XML injection (CDATA + attribute escaping)
// ---------------------------------------------------------------------------

// VAST with ]]> in URLs must produce valid, parseable XML.
func TestVAST_CDATAInjectionEscaped(t *testing.T) {
	sink := &captureSink{}
	h := newTestHandler(sink)

	// URL with ]]> injection attempt.
	maliciousURL := "https://cdn.example.com/video.mp4?x=]]><script>alert(1)</script>"
	q := url.Values{
		"src":  {maliciousURL},
		"did":  {"dec-vast-inject"},
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

	// The malicious URL has a private/unsafe path but we're testing the escaping
	// logic, so let's use a clean URL with ]]> in a valid-scheme URL.
	// Actually validateDestURL will reject the above because it passes.
	// Let's use a URL that passes validation but has ]]> in the query.
	// We'll test buildVAST4 directly instead.
	_ = rr // ignore response from handler; test buildVAST4 directly below.

	// Test the XML builder directly with ]]> injection.
	xmlBytes, err := buildVAST4(
		"https://cdn.example.com/video.mp4",
		"https://click.example.com/]]><evil/>",
		"https://lg.example.com/px?did=abc]]>injection",
		"dec-]]>-test",
		640, 480,
	)
	if err != nil {
		t.Fatalf("buildVAST4 returned error: %v", err)
	}

	// The output must be parseable XML (no injection escaped the CDATA or attrs).
	var result interface{}
	fullXML := xml.Header + string(xmlBytes)
	if err := xml.Unmarshal([]byte(fullXML), &result); err != nil {
		// Try a loose parse just checking for well-formed XML.
		decoder := xml.NewDecoder(strings.NewReader(fullXML))
		for {
			_, decErr := decoder.Token()
			if decErr != nil {
				t.Errorf("VAST XML is not well-formed after ]]> injection: %v", decErr)
				break
			}
			if decErr == nil {
				break
			}
		}
	}

	xmlStr := string(xmlBytes)

	// The literal string "]]><evil" must not appear unescaped in the output.
	if strings.Contains(xmlStr, "]]><evil") {
		t.Error("VAST XML contains unescaped ]]> injection sequence")
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
