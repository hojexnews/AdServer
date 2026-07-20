// Command collector serves the measurement endpoints (DA-8, §4.7):
//
//   - GET  /asyncjs       — asynchronous JS ad tag loader (non-blocking)
//   - GET  /lg            — impression pixel 1×1 (measured at pixel load, CA-6)
//   - GET  /ck            — click redirect → HTTP 302 to validated dest_url (CA-6)
//   - GET  /ct            — conversion pixel (attribution, DA-8)
//   - GET  /vast          — VAST 4.x XML for video creatives (no VPAID)
//   - GET  /healthz       — liveness probe
//
// # Privacy (TX-5/DA-11)
//
// The collector is the ONLY component that ever sees the client's raw IP.
// It derives geo via geo.Resolver and then DISCARDS the IP immediately —
// the IP is never stored, forwarded, logged, or emitted in any event.
// See resolveAndDiscardIP() for the enforcement point.
//
// The raw User-Agent string is reduced to a coarse class (e.g. "chrome-desktop")
// via useragent.Classify() before any event is emitted.  The raw UA NEVER
// leaves this process in any event, log line, or forwarded field.
//
// # Click safety (SSRF/open-redirect guard — security #1)
//
// /ck does NOT accept a dest_url in clear-text query parameters.
// The decision service embeds the dest_url inside an HMAC-SHA256-signed
// token ("tok") at ad-serve time.  The collector validates the HMAC and
// derives the redirect target from the signed payload.  Unsigned or expired
// tokens are rejected with 400.
//
// Secret: CK_HMAC_SECRET environment variable (OpenBao in production).
// Fail-closed: absent at boot → /ck returns 503 on every request.
//
// # Impression accounting (CA-6)
//
// Impressions are counted ONLY when the pixel is loaded by the browser.
// The /asyncjs endpoint returns the creative JSON and does NOT count the
// impression — only /lg does (pixel-triggered measurement).
//
// # VAST 4.x (no VPAID)
//
// The /vast endpoint generates a minimal VAST 4.0 inline document for video
// creatives.  VPAID is not supported (DA-5: no third-party JS in VAST).
// The XML is built with encoding/xml marshalling — no Sprintf injection.
//
// # Trusted proxies (security #8)
//
// extractClientIP honours X-Forwarded-For only from the configured count
// of trusted proxy hops (TRUSTED_PROXY_DEPTH env; default 1).  Headers from
// untrusted positions in the XFF chain are ignored.
package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/clicktoken"
	"github.com/hojex/adserver/internal/geo"
	"github.com/hojex/adserver/internal/telemetry"
	"github.com/hojex/adserver/internal/telemetry/blank"
	"github.com/hojex/adserver/internal/useragent"
)

// ---------------------------------------------------------------------------
// Telemetry sink interface (injected; avoids circular import with producer)
// ---------------------------------------------------------------------------

// EventSink is the interface the collector uses to emit telemetry events.
// *telemetry.Producer satisfies it; NoOpSink is used when no Redpanda is
// configured.
type EventSink interface {
	EmitImpression(tenantID, campaignID, bannerID, zoneID string,
		servedTier commonv1.ServedTier, billable, blank bool,
		decisionID, modelVersion string)
	EmitClick(tenantID, campaignID, bannerID, zoneID, destURL string,
		decisionID, modelVersion string)
	EmitConversion(tenantID, campaignID, bannerID, attributionDecisionID,
		decisionID, modelVersion string)
	// uaClass is the coarse device/browser class — NOT the raw UA string.
	EmitAdRequest(tenantID, zoneID, siteID string, geo *commonv1.Geo,
		uaClass, refererURL, cachebuster string,
		customVars map[string]string, decisionID string)
}

// noopSink discards all events (used when Redpanda is unconfigured).
type noopSink struct{}

func (noopSink) EmitImpression(_, _, _, _ string, _ commonv1.ServedTier, _, _ bool, _, _ string) {
}
func (noopSink) EmitClick(_, _, _, _, _, _, _ string)   {}
func (noopSink) EmitConversion(_, _, _, _, _, _ string) {}
func (noopSink) EmitAdRequest(_, _, _ string, _ *commonv1.Geo, _, _, _ string, _ map[string]string, _ string) {
}

// ---------------------------------------------------------------------------
// producerAdapter wraps *telemetry.Producer to satisfy EventSink
// ---------------------------------------------------------------------------

type producerAdapter struct{ p *telemetry.Producer }

func (a *producerAdapter) EmitImpression(tenantID, campaignID, bannerID, zoneID string,
	servedTier commonv1.ServedTier, billable, blank bool, decisionID, modelVersion string) {
	a.p.EmitImpression(tenantID, campaignID, bannerID, zoneID, servedTier, billable, blank, decisionID, modelVersion)
}
func (a *producerAdapter) EmitClick(tenantID, campaignID, bannerID, zoneID, destURL, decisionID, modelVersion string) {
	a.p.EmitClick(tenantID, campaignID, bannerID, zoneID, destURL, decisionID, modelVersion)
}
func (a *producerAdapter) EmitConversion(tenantID, campaignID, bannerID, attributionDecisionID, decisionID, modelVersion string) {
	a.p.EmitConversion(tenantID, campaignID, bannerID, attributionDecisionID, decisionID, modelVersion)
}
func (a *producerAdapter) EmitAdRequest(tenantID, zoneID, siteID string, g *commonv1.Geo, uaClass, refererURL, cachebuster string, customVars map[string]string, decisionID string) {
	// uaClass is already the coarse class — never the raw UA.
	a.p.EmitAdRequest(tenantID, zoneID, siteID, g, uaClass, refererURL, cachebuster, customVars, decisionID)
}

// ---------------------------------------------------------------------------
// Handler registry
// ---------------------------------------------------------------------------

type collectorHandler struct {
	geoResolver  geo.Resolver
	sink         EventSink
	decisionURL  string // base URL of the decision service for asyncjs
	logger       *slog.Logger
	clickSigner  *clicktoken.Signer // nil → /ck disabled (fail-closed)
	trustedDepth int                // number of trusted XFF proxy hops
}

// ---------------------------------------------------------------------------
// /asyncjs — asynchronous JS ad tag (DA-5)
//
// The browser loads this endpoint asynchronously (non-blocking).  It returns
// a JS snippet that:
//  1. Records the AdRequest event (top-of-funnel).
//  2. Calls the decision service to get the creative JSON.
//  3. Renders the creative in the publisher's ad slot.
//  4. Injects the impression pixel (lg) into the rendered creative.
//
// Query parameters:
//
//	zoneid   — required; identifies the ad placement.
//	cb       — cachebuster (random number injected by the tag).
//	c[key]   — custom first-party variables (e.g. cgender=male).
//	tid      — tenant_id.
//	did      — decision_id (pre-flight, optional).
// ---------------------------------------------------------------------------

func (h *collectorHandler) handleAsyncJS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	zoneID := q.Get("zoneid")
	tenantID := q.Get("tid")
	cachebuster := q.Get("cb")
	decisionID := q.Get("did")

	if zoneID == "" {
		http.Error(w, "missing zoneid", http.StatusBadRequest)
		return
	}

	// Derive geo from client IP and DISCARD the IP immediately (TX-5/DA-11).
	// See resolveAndDiscardIP for the enforcement point.
	derivedGeo := h.resolveAndDiscardIP(r)

	// Collect custom vars (prefixed with "c").
	customVars := map[string]string{}
	for k, vs := range q {
		if strings.HasPrefix(k, "c") && k != "cb" && len(vs) > 0 {
			customVars[k[1:]] = vs[0]
		}
	}

	// PRIVACY (TX-5/DA-11): reduce raw UA to coarse class HERE.
	// The raw UA string is consumed and never forwarded.
	uaClass := useragent.Classify(r.Header.Get("User-Agent"))

	// PRIVACY: sanitize referer — strip query string and fragment to avoid
	// leaking session tokens or identifiers embedded in URLs.
	refererURL := sanitizeReferer(r.Referer())

	// Record the ad-request event (top-of-funnel).
	// IP is already discarded above; derivedGeo carries only country+city.
	// uaClass is the coarse class, not the raw UA.
	h.sink.EmitAdRequest(
		tenantID, zoneID, "", // siteID resolved server-side from zone config
		derivedGeo,
		uaClass,
		refererURL,
		cachebuster,
		customVars,
		decisionID,
	)

	// Return a JS snippet that calls the decision service asynchronously.
	// The snippet is non-blocking: it uses async/await so publisher pages
	// are never blocked on ad loading (DA-5).
	lgBase := envOr("COLLECTOR_BASE_URL", "http://localhost:8081")
	decisionBase := h.decisionURL
	if decisionBase == "" {
		decisionBase = envOr("DECISION_BASE_URL", "http://localhost:8080")
	}

	js := buildAdTagJS(decisionBase, lgBase, zoneID, tenantID, cachebuster, decisionID)

	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// CSP for the /asyncjs response itself.
	// The ad tag JS is loaded as a script by the publisher page; this header
	// applies to the script resource, not to the publisher page.  The ingress
	// is responsible for setting the publisher-page-level CSP (see below).
	//
	// Ingress-level CSP enforcement (document this for [[platform-infra-engineer]]):
	//   The collector's /asyncjs and /lg endpoints must be served with these
	//   response headers to reinforce isolation:
	//
	//     X-Content-Type-Options: nosniff
	//     X-Frame-Options: DENY          (collector pages must not be framed)
	//     Referrer-Policy: no-referrer
	//
	//   For the srcdoc iframe rendered by buildAdTagJS, the BROWSER enforces
	//   the sandbox attribute — no server-side header is needed for the
	//   creative itself because it has no URL (srcdoc has a null origin).
	//   Publishers who wish to add a frame-ancestors CSP on their own pages
	//   should allow-list the collector domain, not the creative origin.
	//
	//   The ingress MUST NOT add "allow-same-origin" to the iframe via any
	//   header manipulation.  The null origin is the entire security boundary.
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	fmt.Fprint(w, js)
}

// buildAdTagJS builds the asynchronous ad tag JavaScript snippet.
// The snippet is injected by publishers via a <script async> tag.
//
// Design: async/await, no blocking, impressions counted only via lg pixel.
// The click link uses the signed "tok" parameter — no plain dest_url in query.
//
// Security (C2 — SafeFrame isolation):
//
//	HTML5 creatives (d.html) are NEVER injected via innerHTML into the publisher
//	DOM.  Instead they are rendered inside a sandboxed <iframe srcdoc="…"> with:
//
//	  sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox"
//
//	Omitting allow-same-origin is the critical property: the iframe runs with a
//	null origin, which means the creative JS cannot read document.cookie,
//	localStorage, sessionStorage, or any DOM element of the publisher page.  The
//	creative is fully isolated even when it contains malicious script.
//
//	Click-through: the creative's click URL is pre-built by the parent JS and
//	injected as an <a> tag wrapping the entire iframe — the anchor lives in the
//	publisher DOM (safe) and opens in a new tab.  allow-popups lets the creative
//	itself also open links; allow-popups-to-escape-sandbox ensures those popups
//	are NOT sandboxed (they open as normal tabs).
//
//	Impression pixel: the lg pixel is fired from the PARENT JS (outside the
//	iframe) so tracking is completely unaffected by the sandbox.  decision_id and
//	model_version flow through the pixel URL unchanged (J0/J1 invariant).
//
//	Image-only creatives: the existing safe path (DOM API, no innerHTML) is
//	preserved — no iframe is inserted for img-only banners.
//
//	Expand / rich-media that requires same-origin: NOT supported by default.
//	If a tenant requires same-origin rich-media, the creative HTML must be
//	served-side sanitised and explicitly allow-listed (future feature).  The
//	default is fail-safe: sandbox without allow-same-origin.
//
// model_version flow (J0→J1):
//
//	The decision response carries `model_version` (empty in J0 / cascade-only;
//	non-empty in J1+ when the ML ranker is active).  The JS embeds it as the
//	"mv" parameter in both the /lg impression pixel URL and the /ck click URL
//	so the collector can forward it to EmitImpression/EmitClick.  This closes
//	the loop: every downstream event carries the ranker version that produced
//	the decision, enabling OPE attribution by model_version in J4.
func buildAdTagJS(decisionBase, lgBase, zoneID, tenantID, cachebuster, decisionID string) string {
	return fmt.Sprintf(`(async function(){
  "use strict";
  try {
    const resp = await fetch(%q, {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({
        zone_id:   %q,
        user_agent: navigator.userAgent,
        site_url:   location.href
      })
    });
    if (!resp.ok) return;
    const d = await resp.json();
    if (!d.banner_id) return; // blank impression: nothing to render
    const slot = document.currentScript && document.currentScript.parentElement;
    if (!slot) return;
    // model_version from decision response ("" in J0/cascade-only; non-empty in J1+).
    // Embedded in every downstream pixel/click URL so OPE can attribute by ranker version.
    const mv = d.model_version || "";
    // Build the click URL (used by both image and HTML creative paths).
    const ckURL = %q + "/ck?tok=" + encodeURIComponent(d.click_tok || "") +
                  "&did=" + encodeURIComponent(d.decision_id) +
                  "&bid=" + encodeURIComponent(d.banner_id) +
                  "&cid=" + encodeURIComponent(d.campaign_id) +
                  "&zid=" + encodeURIComponent(d.zone_id) +
                  "&tid=" + encodeURIComponent(d.tenant_id || "") +
                  "&mv="  + encodeURIComponent(mv);
    // Render creative.
    if (d.image_url) {
      // Safe path: image creative — DOM API only, no innerHTML.
      const a = document.createElement("a");
      // Use the signed click token from the decision response (no plain dest_url).
      a.href = ckURL;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      const img = document.createElement("img");
      img.src = d.image_url;
      img.width = d.width || 0;
      img.height = d.height || 0;
      img.alt = "";
      img.style.display = "block";
      a.appendChild(img);
      slot.appendChild(a);
    } else if (d.html) {
      // SECURITY (C2 — SafeFrame): HTML5 creative is rendered in a sandboxed
      // iframe with null origin.  We NEVER use innerHTML on the publisher DOM.
      //
      // sandbox attrs:
      //   allow-scripts              — creative JS runs (ads require it)
      //   allow-popups               — creative can open links in new tabs
      //   allow-popups-to-escape-sandbox — those new tabs are NOT sandboxed
      // Intentionally omitted:
      //   allow-same-origin          — null origin; no access to publisher
      //                                cookies, localStorage, or DOM
      //   allow-top-navigation       — cannot hijack the publisher page URL
      //   allow-forms                — no form submission from creative
      const iframe = document.createElement("iframe");
      iframe.sandbox = "allow-scripts allow-popups allow-popups-to-escape-sandbox";
      iframe.scrolling = "no";
      iframe.frameBorder = "0";
      iframe.width  = (d.width  || 0).toString();
      iframe.height = (d.height || 0).toString();
      iframe.style.border = "none";
      iframe.style.display = "block";
      // srcdoc assignment is safe: the value is the creative HTML string.
      // It is NOT set via innerHTML on the publisher DOM; it becomes the
      // inner document of the sandboxed iframe (null origin).
      iframe.srcdoc = d.html;
      // Wrap in a click-through anchor that lives in the publisher DOM.
      // The anchor is built with DOM API (no innerHTML) so the ckURL — which
      // contains only ASCII-safe query-param values — is never parsed as HTML.
      const a = document.createElement("a");
      a.href = ckURL;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      a.style.display = "inline-block";
      a.appendChild(iframe);
      slot.appendChild(a);
    }
    // Impression pixel — fired from the PARENT JS (outside the iframe) so the
    // sandbox cannot interfere with tracking (CA-6).
    // "mv" (model_version) flows from decision → lg → telemetry for OPE attribution.
    // decision_id and model_version are preserved (J0/J1 invariant).
    const lg = new Image(1, 1);
    lg.src = %q + "/lg?did=" + encodeURIComponent(d.decision_id) +
             "&bid=" + encodeURIComponent(d.banner_id) +
             "&cid=" + encodeURIComponent(d.campaign_id) +
             "&zid=" + encodeURIComponent(d.zone_id) +
             "&tid=" + encodeURIComponent(d.tenant_id || "") +
             "&tier=" + encodeURIComponent(d.served_tier || "") +
             "&mv="  + encodeURIComponent(mv) +
             "&cb="  + encodeURIComponent(%q);
  } catch(e) {
    // Silent fail: ad tag MUST NOT break the publisher page (DA-5).
  }
})();
`,
		decisionBase+"/v1/decide",
		zoneID,
		lgBase,
		lgBase,
		cachebuster,
	)
}

// ---------------------------------------------------------------------------
// /lg — Impression pixel (CA-6: measured at pixel load, not at decision time)
// ---------------------------------------------------------------------------

func (h *collectorHandler) handleImpression(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	decisionID := q.Get("did")
	bannerID := q.Get("bid")
	campaignID := q.Get("cid")
	zoneID := q.Get("zid")
	tenantID := q.Get("tid")
	servedTierStr := q.Get("tier")

	// Impression is counted HERE — at pixel load — not at ad-request time (CA-6).
	servedTier := commonv1.ServedTier_SERVED_TIER_UNSPECIFIED
	if v, ok := commonv1.ServedTier_value[servedTierStr]; ok {
		servedTier = commonv1.ServedTier(v)
	}

	// CA-6/CA-2 billing invariant: blank must never be billable. This decision
	// is made by the single production function blank.ComputeBlankBillable —
	// the CA-6 parity golden test (tests/parity/ca6_telemetry_golden_test.go)
	// calls the exact same function, so a regression here fails that gate too.
	isBlank, billable := blank.ComputeBlankBillable(servedTier, bannerID)

	// model_version is forwarded from the JS tag (passed as "mv" query param).
	// In J0 (cascade-only) this is always "" (modelVersionDeterministic).
	// When J1 plugs in the ML sidecar, the decision response will carry a
	// non-empty model_version, the JS tag will embed it in the pixel URL, and
	// it will flow here unchanged — enabling OPE attribution by ranker version.
	modelVersion := q.Get("mv")

	h.sink.EmitImpression(
		tenantID, campaignID, bannerID, zoneID,
		servedTier, billable, isBlank,
		decisionID, modelVersion,
	)

	// Return 1×1 transparent GIF pixel.
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Decision-ID", decisionID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(transparentGIF)
}

// transparentGIF is the minimal 1×1 transparent GIF pixel (43 bytes).
var transparentGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, // GIF89a
	0x01, 0x00, 0x01, 0x00, // 1×1
	0x80, 0x00, 0x00, // global color table flag, etc.
	0xFF, 0xFF, 0xFF, 0x00, 0x00, 0x00, // color table: white, black
	0x21, 0xF9, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, // graphic control
	0x2C, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, // image descriptor
	0x02, 0x02, 0x44, 0x01, 0x00, // image data
	0x3B, // GIF trailer
}

// ---------------------------------------------------------------------------
// /ck — Click redirect (CA-6: server-side 302; HMAC-validated token)
//
// Security (security #1 — open-redirect fix):
//
//   The dest_url is NEVER read from a plain query parameter.
//   The "tok" parameter carries an HMAC-SHA256-signed payload produced by the
//   decision service at ad-serve time.  The payload encodes decision_id,
//   banner_id, dest_url (from banner config), and an expiry timestamp.
//
//   Validation: HMAC checked → expiry checked → validateDestURL (defence-in-depth).
//   Any failure → 400 Bad Request (no redirect).
//
//   Fail-closed: if CK_HMAC_SECRET was absent at boot, clickSigner is nil
//   and every /ck request returns 503 Service Unavailable.
// ---------------------------------------------------------------------------

func (h *collectorHandler) handleClick(w http.ResponseWriter, r *http.Request) {
	// Fail-closed: if the HMAC secret was absent at boot, refuse all clicks.
	if h.clickSigner == nil {
		if h.logger != nil {
			h.logger.Error("click: CK_HMAC_SECRET not configured — refusing click (fail-closed)")
		}
		http.Error(w, "click service unavailable", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	tok := q.Get("tok")
	decisionID := q.Get("did")
	bannerID := q.Get("bid")
	campaignID := q.Get("cid")
	zoneID := q.Get("zid")
	tenantID := q.Get("tid")

	if tok == "" {
		http.Error(w, "missing click token", http.StatusBadRequest)
		return
	}

	// Validate the HMAC token and extract the signed dest_url.
	signedDecisionID, signedBannerID, destURL, err := h.clickSigner.Validate(tok, time.Now())
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("click: invalid or expired token rejected",
				"reason", err,
				"decision_id", decisionID)
		}
		http.Error(w, "invalid or expired click token", http.StatusBadRequest)
		return
	}

	// Consistency check: token fields must match the query params supplied
	// by the JS tag (defence-in-depth against token reuse across banners).
	if decisionID != "" && signedDecisionID != decisionID {
		if h.logger != nil {
			h.logger.Warn("click: token decision_id mismatch",
				"token_did", signedDecisionID, "query_did", decisionID)
		}
		http.Error(w, "click token mismatch", http.StatusBadRequest)
		return
	}
	if bannerID != "" && signedBannerID != bannerID {
		if h.logger != nil {
			h.logger.Warn("click: token banner_id mismatch",
				"token_bid", signedBannerID, "query_bid", bannerID)
		}
		http.Error(w, "click token mismatch", http.StatusBadRequest)
		return
	}

	// Defence-in-depth: validate the URL extracted from the signed payload.
	// This guards against misconfigured banners in the config store.
	if err := validateDestURL(destURL); err != nil {
		if h.logger != nil {
			h.logger.Warn("click: signed dest_url failed validation (misconfigured banner)",
				"reason", err, "decision_id", signedDecisionID)
		}
		http.Error(w, "invalid destination", http.StatusBadRequest)
		return
	}

	// Use the validated decision_id from the token as the authoritative one.
	effectiveDecisionID := signedDecisionID

	// model_version is forwarded from the JS tag (passed as "mv" query param).
	// In J0 this is always ""; J1 will carry the active ranker version here.
	modelVersion := q.Get("mv")

	// Record the click event (fire-and-forget).
	h.sink.EmitClick(tenantID, campaignID, bannerID, zoneID, destURL, effectiveDecisionID, modelVersion)

	// Server-side 302 redirect to the advertiser landing page (CA-6).
	http.Redirect(w, r, destURL, http.StatusFound)
}

// validateDestURL ensures the destination URL is safe to redirect to.
//
// Security invariants (SSRF/open-redirect guard — defence-in-depth):
//  1. Scheme must be "https" or "http" — no file://, ftp://, etc.
//  2. Userinfo (user@host) is rejected; it is an SSRF bypass vector.
//  3. Host must not be empty.
//  4. Host is extracted via u.Hostname() which correctly handles [::1] and
//     host:port forms — no manual string splitting.
//  5. Numeric IP literals in alternate bases (hex 0x7f000001, decimal
//     2130706433, octal 017700000001) are normalised to canonical IPv4 before
//     the range check so they cannot bypass the loopback/private test.
//  6. Private, loopback, link-local, ULA, and unspecified IP ranges are blocked
//     for both IPv4 and IPv6.
//  7. Hostnames that are well-known loopback aliases are blocked
//     (localhost, *.local, *.internal).
//
// This is a defence-in-depth check against misconfigured banners.
// The primary open-redirect protection is the HMAC-signed click token (/ck).
func validateDestURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty URL")
	}

	// Use net/url.Parse — never manual string splitting.
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("unparseable URL: %w", err)
	}

	// Require an explicit http/https scheme.
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return fmt.Errorf("disallowed scheme %q (only https/http)", scheme)
	}

	// Reject userinfo (user:pass@host or user@host).
	// "https://evil.com@127.0.0.1/x" — the effective host becomes 127.0.0.1.
	if u.User != nil {
		return fmt.Errorf("userinfo in URL rejected (SSRF bypass vector)")
	}

	// u.Hostname() strips brackets from [::1] and the port from host:port.
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("empty host")
	}

	// Attempt to resolve the host to a canonical net.IP, handling alternate
	// numeric representations that net.ParseIP does not recognise but that
	// real HTTP clients and resolvers do accept:
	//   hex:     0x7f000001   → 127.0.0.1
	//   decimal: 2130706433   → 127.0.0.1
	//   octal:   017700000001 → 127.0.0.1
	ip := net.ParseIP(host)
	if ip == nil {
		ip = parseAlternateNumericIP(host)
	}

	if ip != nil {
		// Normalise to 16-byte form so IPv4-mapped IPv6 addresses are handled.
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
		if isBlockedIP(ip) {
			return fmt.Errorf("private/reserved IP %q rejected", ip)
		}
		return nil
	}

	// Host is a DNS name — reject well-known loopback aliases.
	lc := strings.ToLower(host)
	if lc == "localhost" || strings.HasSuffix(lc, ".local") || strings.HasSuffix(lc, ".internal") {
		return fmt.Errorf("private hostname %q rejected", host)
	}
	return nil
}

// parseAlternateNumericIP attempts to parse a host string as a 32-bit integer
// written in hex (0x…), octal (0…), or decimal, and converts it to an IPv4
// address.  Returns nil when the host is not a recognisable numeric literal.
//
// This covers SSRF bypass vectors that net.ParseIP ignores but that browsers
// and Go's net/http stack resolve correctly:
//
//	0x7f000001  → 127.0.0.1
//	2130706433  → 127.0.0.1
//	017700000001 → 127.0.0.1
func parseAlternateNumericIP(host string) net.IP {
	if host == "" {
		return nil
	}
	// Only attempt numeric parsing when the string could plausibly be a number.
	// This avoids expensive parsing on ordinary hostnames.
	first := host[0]
	if !((first >= '0' && first <= '9') || first == 'x' || first == 'X') {
		return nil
	}

	// strconv.ParseUint understands 0x prefix (base 0 auto-detects hex/octal/decimal).
	n, err := strconv.ParseUint(host, 0, 32)
	if err != nil {
		return nil
	}
	// Convert 32-bit integer to 4-byte IPv4.
	return net.IPv4(
		byte(n>>24),
		byte(n>>16),
		byte(n>>8),
		byte(n),
	)
}

// isBlockedIP reports whether ip falls in any range that must not be a redirect
// destination: loopback, private (RFC 1918 / ULA), link-local, or unspecified.
// Covers both IPv4 and IPv6.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

// ---------------------------------------------------------------------------
// /ct — Conversion pixel (DA-8)
// ---------------------------------------------------------------------------

func (h *collectorHandler) handleConversion(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	decisionID := q.Get("did")
	bannerID := q.Get("bid")
	campaignID := q.Get("cid")
	tenantID := q.Get("tid")
	// attribution_decision_id is the decision_id of the impression/click being
	// attributed.  It links this conversion to the ad that drove it (TX-1).
	attributionDecisionID := q.Get("adid")

	// model_version forwarded from the conversion pixel URL ("mv" param).
	// In J0 always ""; propagated from the decision that originated the ad.
	modelVersion := q.Get("mv")

	h.sink.EmitConversion(
		tenantID, campaignID, bannerID,
		attributionDecisionID,
		decisionID, modelVersion,
	)

	// Return 1×1 transparent GIF (same as impression pixel).
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("X-Decision-ID", decisionID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(transparentGIF)
}

// ---------------------------------------------------------------------------
// /vast — VAST 4.x XML for video creatives (no VPAID)
//
// Security (security #3 — CDATA/XML injection fix):
//
//   The VAST document is generated using encoding/xml struct marshalling.
//   No Sprintf with user-controlled values is used in the XML body.
//   All string values are XML-escaped by the marshaller.
// ---------------------------------------------------------------------------

func (h *collectorHandler) handleVAST(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	videoURL := q.Get("src")
	decisionID := q.Get("did")
	campaignID := q.Get("cid")
	bannerID := q.Get("bid")
	tenantID := q.Get("tid")
	zoneID := q.Get("zid")
	clickURL := q.Get("dest")
	widthStr := q.Get("w")
	heightStr := q.Get("h")

	if videoURL == "" {
		http.Error(w, "missing src", http.StatusBadRequest)
		return
	}

	// SSRF guard: the video URL comes from banner config (server-side).
	// Validate scheme to prevent embedding arbitrary local resources.
	if err := validateDestURL(videoURL); err != nil {
		if h.logger != nil {
			h.logger.Warn("vast: invalid video src rejected", "err", err)
		}
		http.Error(w, "invalid src", http.StatusBadRequest)
		return
	}

	// clickURL in VAST is also validated (may be empty for non-clickable video).
	if clickURL != "" {
		if err := validateDestURL(clickURL); err != nil {
			if h.logger != nil {
				h.logger.Warn("vast: invalid click dest rejected", "err", err)
			}
			http.Error(w, "invalid dest", http.StatusBadRequest)
			return
		}
	}

	lgBase := envOr("COLLECTOR_BASE_URL", "http://localhost:8081")
	impressionURL := fmt.Sprintf(
		"%s/lg?did=%s&bid=%s&cid=%s&zid=%s&tid=%s&tier=SERVED_TIER_UNSPECIFIED",
		lgBase,
		url.QueryEscape(decisionID),
		url.QueryEscape(bannerID),
		url.QueryEscape(campaignID),
		url.QueryEscape(zoneID),
		url.QueryEscape(tenantID),
	)

	var width, height int
	if widthStr != "" {
		width, _ = strconv.Atoi(widthStr)
	}
	if heightStr != "" {
		height, _ = strconv.Atoi(heightStr)
	}

	xmlBytes, err := buildVAST4(videoURL, clickURL, impressionURL, decisionID, width, height)
	if err != nil {
		if h.logger != nil {
			h.logger.Error("vast: failed to marshal VAST XML", "err", err)
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(xmlBytes)
}

// ---------------------------------------------------------------------------
// VAST 4.x XML structs — generated via encoding/xml (security #3 fix)
//
// All string values are XML-escaped by the marshaller.
// No Sprintf/string-concat is used for XML content.
// CDATA sections use the xmlCDATA type which escapes ]]> correctly.
// ---------------------------------------------------------------------------

// xmlCDATA wraps a string so that encoding/xml emits it as <![CDATA[...]]>.
// The standard library does not have a built-in CDATA type, so we implement
// MarshalXML to emit the CDATA wrapper with proper escaping of "]]>".
type xmlCDATA struct {
	Value string
}

func (c xmlCDATA) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	// Escape the ]]> sequence within CDATA by splitting the section.
	// "]]>" → "]]]]><![CDATA[>" is the standard CDATA escape.
	escaped := strings.ReplaceAll(c.Value, "]]>", "]]]]><![CDATA[>")
	type cdataInner struct {
		Inner string `xml:",innerxml"`
	}
	return e.EncodeElement(cdataInner{Inner: "<![CDATA[" + escaped + "]]>"}, start)
}

// vastRoot is the <VAST> document root.
type vastRoot struct {
	XMLName   xml.Name `xml:"VAST"`
	Version   string   `xml:"version,attr"`
	Namespace string   `xml:"xmlns:xs,attr"`
	Ad        vastAd   `xml:"Ad"`
}

type vastAd struct {
	ID     string     `xml:"id,attr"`
	Inline vastInline `xml:"InLine"`
}

type vastInline struct {
	AdSystem   vastAdSystem   `xml:"AdSystem"`
	AdTitle    string         `xml:"AdTitle"`
	Impression vastImpression `xml:"Impression"`
	Creatives  vastCreatives  `xml:"Creatives"`
}

type vastAdSystem struct {
	Version string `xml:"version,attr"`
	Name    string `xml:",chardata"`
}

type vastImpression struct {
	ID  string   `xml:"id,attr"`
	URL xmlCDATA `xml:",any"`
}

type vastCreatives struct {
	Creative vastCreative `xml:"Creative"`
}

type vastCreative struct {
	ID     string     `xml:"id,attr"`
	Linear vastLinear `xml:"Linear"`
}

type vastLinear struct {
	MediaFiles  vastMediaFiles   `xml:"MediaFiles"`
	VideoClicks *vastVideoClicks `xml:"VideoClicks,omitempty"`
}

type vastMediaFiles struct {
	MediaFile vastMediaFile `xml:"MediaFile"`
}

type vastMediaFile struct {
	Type     string   `xml:"type,attr"`
	Delivery string   `xml:"delivery,attr"`
	Width    string   `xml:"width,attr,omitempty"`
	Height   string   `xml:"height,attr,omitempty"`
	URL      xmlCDATA `xml:",any"`
}

type vastVideoClicks struct {
	ClickThrough vastClickThrough `xml:"ClickThrough"`
}

type vastClickThrough struct {
	ID  string   `xml:"id,attr"`
	URL xmlCDATA `xml:",any"`
}

// buildVAST4 generates a minimal VAST 4.0 Inline document using encoding/xml.
// All values are XML-escaped by the marshaller; no string injection is possible.
// Returns the marshalled XML bytes (without the <?xml?> header — caller adds it).
func buildVAST4(videoURL, clickURL, impressionURL, decisionID string, width, height int) ([]byte, error) {
	widthStr := ""
	heightStr := ""
	if width > 0 {
		widthStr = strconv.Itoa(width)
	}
	if height > 0 {
		heightStr = strconv.Itoa(height)
	}

	var videoClicks *vastVideoClicks
	if clickURL != "" {
		videoClicks = &vastVideoClicks{
			ClickThrough: vastClickThrough{
				ID:  "ck-" + decisionID,
				URL: xmlCDATA{Value: clickURL},
			},
		}
	}

	doc := vastRoot{
		Version:   "4.0",
		Namespace: "http://www.w3.org/2001/XMLSchema",
		Ad: vastAd{
			ID: decisionID,
			Inline: vastInline{
				AdSystem: vastAdSystem{Version: "1.0", Name: "HojexAdServer"},
				AdTitle:  "Ad",
				Impression: vastImpression{
					ID:  "imp-" + decisionID,
					URL: xmlCDATA{Value: impressionURL},
				},
				Creatives: vastCreatives{
					Creative: vastCreative{
						ID: "cr-" + decisionID,
						Linear: vastLinear{
							MediaFiles: vastMediaFiles{
								MediaFile: vastMediaFile{
									Type:     "video/mp4",
									Delivery: "progressive",
									Width:    widthStr,
									Height:   heightStr,
									URL:      xmlCDATA{Value: videoURL},
								},
							},
							VideoClicks: videoClicks,
						},
					},
				},
			},
		},
	}

	return xml.MarshalIndent(doc, "", "  ")
}

// ---------------------------------------------------------------------------
// Privacy enforcement: resolveAndDiscardIP (TX-5/DA-11)
//
// This function is the SOLE location where the client IP is read and used.
// It resolves the IP to a Geo and returns ONLY the Geo — the IP is never
// returned, stored in a variable that outlives this function, logged, or
// forwarded anywhere.
//
// The IP variable has a deliberately narrow scope: it is a parameter to
// geo.Resolver.Resolve() and goes out of scope immediately after the call.
// ---------------------------------------------------------------------------

// resolveAndDiscardIP derives geo from the request and discards the IP.
//
// PRIVACY (TX-5/DA-11): the raw IP is used solely as input to geo.Resolve.
// After this function returns, the IP is NOT stored anywhere in the process.
// The returned *commonv1.Geo carries only country and city — no coordinates,
// no postal code, no re-identifiable data.
func (h *collectorHandler) resolveAndDiscardIP(r *http.Request) *commonv1.Geo {
	// Step 1: extract the real client IP (X-Forwarded-For or RemoteAddr).
	// The IP is stored in a local variable with scope limited to this function.
	clientIP := h.extractClientIP(r) // IP consumed here, not propagated further

	// Step 2: derive Geo.  The resolver reads the IP and returns only country+city.
	derivedGeo := h.geoResolver.Resolve(clientIP) // IP is the input, Geo is the output

	// Step 3: IP is now out of scope — it is NEVER returned, logged, stored,
	// or forwarded.  Only derivedGeo (country + city) leaves this function.
	// (TX-5/DA-11 enforcement point — verified by privacy-compliance-auditor)
	return derivedGeo
}

// extractClientIP extracts the real client IP from the request.
//
// Security (security #8 — trusted proxies):
//
//	X-Forwarded-For is honoured ONLY when the request arrives through the
//	configured number of trusted proxy hops (h.trustedDepth).
//	The Cilium/ingress layer is the boundary of trust; IPs injected by the
//	client in the XFF chain are ignored.
//
//	With trustedDepth=1 (default): the ingress proxy appends one entry to XFF.
//	The rightmost entry is the most-recently-added (by the trusted proxy) and
//	is taken as the client IP.  The leftmost entry (claimed by the client) is
//	NOT used when trustedDepth=1 and len(parts)>1.
//
//	With trustedDepth=0: XFF is ignored entirely; RemoteAddr is used.
//
// The returned string is transient: callers must NOT persist it.
func (h *collectorHandler) extractClientIP(r *http.Request) string {
	depth := h.trustedDepth
	if depth < 0 {
		depth = 0
	}

	if depth > 0 {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			// The trusted proxy appends to the right.  With depth=N, we trust
			// the Nth-from-right entry (index len-N) as the true client IP.
			// With depth=1 and a single proxy, that is parts[len-1] (rightmost).
			// With a direct connection through one proxy, parts[0] is the client.
			// For depth=1 we take the entry just before the trusted proxy's own
			// addition, i.e. index max(0, len(parts)-depth).
			idx := len(parts) - depth
			if idx < 0 {
				idx = 0
			}
			if ip := strings.TrimSpace(parts[idx]); ip != "" {
				return ip
			}
		}
	}

	// Fall back to RemoteAddr (strip port).
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sanitizeReferer strips the query string and fragment from a referer URL,
// retaining only scheme + host + path.
//
// PRIVACY: query strings and fragments frequently carry session tokens,
// user identifiers, UTM parameters, and other identifying information.
// We retain only the structural page location (scheme + host + path).
//
// If the URL cannot be parsed, does not have an absolute scheme (http/https),
// or has no host, the empty string is returned — emit nothing rather than
// risk leaking a token buried in a malformed value.
func sanitizeReferer(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// Require an absolute URL with a recognised scheme and a non-empty host.
	// Relative URLs, opaque URIs, and anything without http/https are discarded.
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" {
		return ""
	}
	// Reconstruct with only scheme + host + path (no query, no fragment).
	clean := &url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   u.Path,
	}
	return clean.String()
}

// ---------------------------------------------------------------------------
// DecisionResponse mirrors the decision service's JSON response (for asyncjs).
// We re-declare it here to avoid importing the decision service package.
// ---------------------------------------------------------------------------

type decisionResponse struct {
	DecisionID   string `json:"decision_id"`
	ModelVersion string `json:"model_version"`
	TenantID     string `json:"tenant_id"`
	ZoneID       string `json:"zone_id"`
	ServedTier   string `json:"served_tier"`
	CampaignID   string `json:"campaign_id,omitempty"`
	BannerID     string `json:"banner_id,omitempty"`
	ClickTok     string `json:"click_tok,omitempty"` // signed HMAC token; no plain dest_url
	ImageURL     string `json:"image_url,omitempty"`
	HTML         string `json:"html,omitempty"`
	VideoURL     string `json:"video_url,omitempty"`
	Width        int32  `json:"width,omitempty"`
	Height       int32  `json:"height,omitempty"`
}

// ---------------------------------------------------------------------------
// /healthz
// ---------------------------------------------------------------------------

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// ---------------------------------------------------------------------------
	// Click HMAC signer — fail-closed: absent secret → /ck disabled.
	// Secret: CK_HMAC_SECRET from env/OpenBao (never hardcoded).
	// ---------------------------------------------------------------------------
	ckSecret := os.Getenv("CK_HMAC_SECRET")
	var signer *clicktoken.Signer
	if ckSecret == "" {
		// Fail-closed: log a loud error but continue serving other endpoints.
		// /ck handler checks for nil signer and returns 503.
		logger.Error("CRITICAL: CK_HMAC_SECRET is not set — /ck endpoint is DISABLED (fail-closed). Set CK_HMAC_SECRET to enable click tracking.")
	} else {
		var err error
		signer, err = clicktoken.New(ckSecret, clicktoken.DefaultTTL)
		if err != nil {
			logger.Error("CRITICAL: failed to initialise click signer — /ck disabled", "err", err)
		} else {
			logger.Info("click signer: HMAC-SHA256 token validation active")
		}
	}

	// Trusted proxy depth for X-Forwarded-For (security #8).
	trustedDepth := 1 // default: one trusted proxy (ingress/Cilium)
	if v := os.Getenv("TRUSTED_PROXY_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			trustedDepth = n
		}
	}

	// Geo resolver: MaxMind if configured, stub otherwise.
	geoDBPath := envOr("GEOIP_DB_PATH", "")
	geoResolver := geo.NewMaxMindResolver(geoDBPath, logger)

	// Telemetry producer: Redpanda if configured, no-op otherwise.
	brokers := envOr("REDPANDA_BROKERS", "")
	walPath := envOr("TELEMETRY_WAL_PATH", "")

	var sink EventSink = noopSink{}
	if brokers != "" {
		p, err := telemetry.NewProducer(telemetry.Config{
			Brokers:    strings.Split(brokers, ","),
			WALPath:    walPath,
			WALSync:    envOr("TELEMETRY_WAL_SYNC", "") == "true",
			QueueDepth: 8192,
			Logger:     logger,
			WireFormat: telemetry.ParseWireFormat(envOr("TELEMETRY_WIRE_FORMAT", "")),
		})
		if err != nil {
			logger.Warn("telemetry: producer init failed; using no-op sink", "err", err)
		} else {
			sink = &producerAdapter{p: p}
			defer p.Close()
		}
	}

	h := &collectorHandler{
		geoResolver:  geoResolver,
		sink:         sink,
		decisionURL:  envOr("DECISION_BASE_URL", "http://localhost:8080"),
		logger:       logger,
		clickSigner:  signer, // nil → fail-closed in handleClick
		trustedDepth: trustedDepth,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /asyncjs", h.handleAsyncJS)
	mux.HandleFunc("GET /lg", h.handleImpression)
	mux.HandleFunc("GET /ck", h.handleClick)
	mux.HandleFunc("GET /ct", h.handleConversion)
	mux.HandleFunc("GET /vast", h.handleVAST)
	mux.HandleFunc("GET /healthz", handleHealthz)

	addr := envOr("COLLECTOR_ADDR", ":8081")
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext: func(_ net.Listener) context.Context {
			return context.Background()
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Geo hot-reload (G0/E5, DA-9): poll the .mmdb file's mtime and hot-swap
	// the in-memory reader — without restarting this process — whenever the
	// auto-update job (§4.10) atomically replaces the file on disk.
	// Only wired when a real *geo.MaxMindResolver is active (StubResolver /
	// EmptyResolver never reload). Bound to ctx so it stops at shutdown.
	if geoDBPath != "" {
		if mm, ok := geoResolver.(*geo.MaxMindResolver); ok {
			go runGeoReloader(ctx, mm, geoDBPath, geoReloadInterval(), logger)
		}
	}

	go func() {
		logger.Info("collector service listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down collector")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Error("shutdown", "err", err)
	}
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Geo hot-reload (G0/E5, DA-9)
// ---------------------------------------------------------------------------

// geoReloadInterval reads GEOIP_RELOAD_INTERVAL (a Go duration like "1h")
// and falls back to 1h on absence or parse error.
func geoReloadInterval() time.Duration {
	const def = time.Hour
	v := os.Getenv("GEOIP_RELOAD_INTERVAL")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// runGeoReloader polls dbPath's mtime on a fixed interval and hot-swaps the
// in-memory GeoLite2 database (via mm.Reload) whenever the file has been
// atomically replaced on disk by the auto-update job (§4.10). This is the
// substrate DA-9 relies on to keep GeoLite2 fresh without manual
// intervention (CA-9) and without a service restart (G0/E5).
//
// mm.Reload already serialises against concurrent Resolve calls internally
// (RWMutex) — this loop only decides *when* to call it, based on mtime.
// On a failed reload the previous database keeps serving (see Reload's
// doc); the next tick retries since lastMod is only advanced on success.
//
// This function never sees or logs a client IP; it only stats/opens a
// config file path on local disk (TX-5/DA-11 unaffected).
func runGeoReloader(ctx context.Context, mm *geo.MaxMindResolver, dbPath string, interval time.Duration, logger *slog.Logger) {
	var lastMod time.Time
	if fi, err := os.Stat(dbPath); err == nil {
		lastMod = fi.ModTime()
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fi, err := os.Stat(dbPath)
			if err != nil {
				logger.Warn("geo: reload check skipped — cannot stat mmdb file", "path", dbPath, "err", err)
				continue
			}
			if !fi.ModTime().After(lastMod) {
				continue // unchanged since the last successful reload
			}
			if err := mm.Reload(dbPath); err != nil {
				logger.Warn("geo: hot-reload failed; keeping previous database in memory (DA-9)",
					"path", dbPath, "err", err)
				continue // mtime still stale here, so the next tick retries
			}
			lastMod = fi.ModTime()
			logger.Info("geo: hot-reloaded GeoLite2 database (no restart)", "path", dbPath)
		}
	}
}
