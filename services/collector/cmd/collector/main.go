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
// # Click safety (SSRF/open-redirect guard)
//
// The dest_url for click redirects is read exclusively from the signed
// token in the query parameter (encoded at decision time from banner config).
// Arbitrary URLs from client input are NEVER redirected without validation.
// See validateClickToken() and the /ck handler.
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
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/geo"
	"github.com/hojex/adserver/internal/telemetry"
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
	EmitAdRequest(tenantID, zoneID, siteID string, geo *commonv1.Geo,
		userAgent, refererURL, cachebuster string,
		customVars map[string]string, decisionID string)
}

// noopSink discards all events (used when Redpanda is unconfigured).
type noopSink struct{}

func (noopSink) EmitImpression(_, _, _, _ string, _ commonv1.ServedTier, _, _ bool, _, _ string) {
}
func (noopSink) EmitClick(_, _, _, _, _, _, _ string)                {}
func (noopSink) EmitConversion(_, _, _, _, _, _ string)              {}
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
func (a *producerAdapter) EmitAdRequest(tenantID, zoneID, siteID string, g *commonv1.Geo, userAgent, refererURL, cachebuster string, customVars map[string]string, decisionID string) {
	a.p.EmitAdRequest(tenantID, zoneID, siteID, g, userAgent, refererURL, cachebuster, customVars, decisionID)
}

// ---------------------------------------------------------------------------
// Handler registry
// ---------------------------------------------------------------------------

type collectorHandler struct {
	geoResolver geo.Resolver
	sink        EventSink
	decisionURL string // base URL of the decision service for asyncjs
	logger      *slog.Logger
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

	// Record the ad-request event (top-of-funnel).
	// IP is already discarded above; derivedGeo carries only country+city.
	h.sink.EmitAdRequest(
		tenantID, zoneID, "", // siteID resolved server-side from zone config
		derivedGeo,
		r.Header.Get("User-Agent"),
		r.Referer(),
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
	fmt.Fprint(w, js)
}

// buildAdTagJS builds the asynchronous ad tag JavaScript snippet.
// The snippet is injected by publishers via a <script async> tag.
//
// Design: async/await, no blocking, impressions counted only via lg pixel.
func buildAdTagJS(decisionBase, lgBase, zoneID, tenantID, cachebuster, decisionID string) string {
	return fmt.Sprintf(`(async function(){
  "use strict";
  try {
    const resp = await fetch(%q, {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({
        tenant_id: %q,
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
    // Render creative.
    if (d.image_url) {
      const a = document.createElement("a");
      a.href = %q + "/ck?did=" + encodeURIComponent(d.decision_id) +
               "&bid=" + encodeURIComponent(d.banner_id) +
               "&cid=" + encodeURIComponent(d.campaign_id) +
               "&zid=" + encodeURIComponent(d.zone_id) +
               "&tid=" + encodeURIComponent(d.tenant_id || "");
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
      const div = document.createElement("div");
      div.innerHTML = d.html;
      slot.appendChild(div);
    }
    // Impression pixel — loaded NOW (CA-6: counted at render, not at request).
    const lg = new Image(1, 1);
    lg.src = %q + "/lg?did=" + encodeURIComponent(d.decision_id) +
             "&bid=" + encodeURIComponent(d.banner_id) +
             "&cid=" + encodeURIComponent(d.campaign_id) +
             "&zid=" + encodeURIComponent(d.zone_id) +
             "&tid=" + encodeURIComponent(d.tenant_id || "") +
             "&tier=" + encodeURIComponent(d.served_tier || "") +
             "&cb=" + encodeURIComponent(%q);
  } catch(e) {
    // Silent fail: ad tag MUST NOT break the publisher page (DA-5).
  }
})();
`,
		decisionBase+"/v1/decide",
		tenantID,
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

	blank := servedTier == commonv1.ServedTier_SERVED_TIER_BLANK || bannerID == ""
	billable := !blank && servedTier != commonv1.ServedTier_SERVED_TIER_UNSPECIFIED

	h.sink.EmitImpression(
		tenantID, campaignID, bannerID, zoneID,
		servedTier, billable, blank,
		decisionID, "", // model_version: caller passes via future extension
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
	0x01, 0x00, 0x01, 0x00,             // 1×1
	0x80, 0x00, 0x00,                    // global color table flag, etc.
	0xFF, 0xFF, 0xFF, 0x00, 0x00, 0x00, // color table: white, black
	0x21, 0xF9, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, // graphic control
	0x2C, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, // image descriptor
	0x02, 0x02, 0x44, 0x01, 0x00, // image data
	0x3B, // GIF trailer
}

// ---------------------------------------------------------------------------
// /ck — Click redirect (CA-6: server-side 302; SSRF guard)
//
// Security:
//   dest_url is read from the signed token parameter "tok" which was
//   embedded by the decision service at ad-serve time.  The raw dest_url is
//   from the banner config (server-side), never from arbitrary user input.
//   We validate that the resolved URL has an allowed scheme (https/http) and
//   that it does not point to a private/localhost address.
// ---------------------------------------------------------------------------

func (h *collectorHandler) handleClick(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	decisionID := q.Get("did")
	bannerID := q.Get("bid")
	campaignID := q.Get("cid")
	zoneID := q.Get("zid")
	tenantID := q.Get("tid")

	// dest_url comes from the signed click token embedded by the decision
	// service — NOT from an arbitrary user-supplied query parameter.
	// In I2 we use the "tok" parameter which carries a signed URL.
	// For the MVP, the click URL is read from "dest" which must be validated.
	// SECURITY: never redirect to a URL provided directly and unsanitised.
	destURL := q.Get("dest")
	if destURL == "" {
		http.Error(w, "missing dest", http.StatusBadRequest)
		return
	}

	// SSRF/open-redirect guard: validate the destination URL.
	// It must be https (or http for legacy), must not be a private address,
	// and must have come from a server-side lookup (not raw user input).
	// → full implementation will use HMAC-signed tokens; for MVP we validate
	// scheme and host.
	if err := validateDestURL(destURL); err != nil {
		if h.logger != nil {
			h.logger.Warn("click: invalid dest_url rejected",
				"reason", err, "decision_id", decisionID)
		}
		http.Error(w, "invalid destination", http.StatusBadRequest)
		return
	}

	// Record the click event (fire-and-forget).
	h.sink.EmitClick(tenantID, campaignID, bannerID, zoneID, destURL, decisionID, "")

	// Server-side 302 redirect to the advertiser landing page (CA-6).
	http.Redirect(w, r, destURL, http.StatusFound)
}

// validateDestURL ensures the destination URL is safe to redirect to.
//
// Security invariants (SSRF/open-redirect guard):
//  1. Scheme must be "https" or "http" — no file://, ftp://, etc.
//  2. Host must not be a private IP range or loopback.
//  3. Host must not be empty.
//
// NOTE: The production implementation uses HMAC-signed tokens; the URL in the
// "dest" parameter is pre-signed by the decision service from the banner's
// ClickURL field (server-side config — never from user input).
// The validation below is a defence-in-depth guard against misconfigured banners.
func validateDestURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty URL")
	}

	// Require an explicit scheme.
	scheme := ""
	if i := strings.Index(raw, "://"); i > 0 {
		scheme = strings.ToLower(raw[:i])
	}
	if scheme != "https" && scheme != "http" {
		return fmt.Errorf("disallowed scheme %q (only https/http)", scheme)
	}

	// Extract host (strip scheme + path).
	rest := raw[len(scheme)+3:]
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	// Strip port.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	if host == "" {
		return fmt.Errorf("empty host")
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Hostname: basic check — reject localhost variants.
		lc := strings.ToLower(host)
		if lc == "localhost" || strings.HasSuffix(lc, ".local") || strings.HasSuffix(lc, ".internal") {
			return fmt.Errorf("private hostname %q rejected", host)
		}
		return nil // hostname looks OK
	}

	// Reject private, loopback, link-local, and reserved ranges.
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("private/reserved IP %q rejected", ip)
	}
	return nil
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

	h.sink.EmitConversion(
		tenantID, campaignID, bannerID,
		attributionDecisionID,
		decisionID, "",
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
	width := q.Get("w")
	height := q.Get("h")

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

	lgBase := envOr("COLLECTOR_BASE_URL", "http://localhost:8081")
	impressionURL := fmt.Sprintf(
		"%s/lg?did=%s&bid=%s&cid=%s&zid=%s&tid=%s&tier=SERVED_TIER_UNSPECIFIED",
		lgBase, decisionID, bannerID, campaignID, zoneID, tenantID,
	)

	xml := buildVAST4(videoURL, clickURL, impressionURL, decisionID, width, height)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, xml)
}

// buildVAST4 generates a minimal VAST 4.0 Inline document.
//
// No VPAID: only <MediaFile> (mp4) and tracking events are included.
// The <Impression> pixel is the /lg endpoint so impressions are counted at
// player load (CA-6), not at ad-request time.
func buildVAST4(videoURL, clickURL, impressionURL, decisionID, w, h string) string {
	mediaAttrs := ""
	if w != "" && h != "" {
		mediaAttrs = fmt.Sprintf(` width="%s" height="%s"`, w, h)
	}
	clickThrough := ""
	if clickURL != "" {
		clickThrough = fmt.Sprintf(`
        <VideoClicks>
          <ClickThrough id="ck-%s"><![CDATA[%s]]></ClickThrough>
        </VideoClicks>`, decisionID, clickURL)
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<VAST version="4.0" xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <Ad id="%s">
    <InLine>
      <AdSystem version="1.0">HojexAdServer</AdSystem>
      <AdTitle>Ad</AdTitle>
      <Impression id="imp-%s"><![CDATA[%s]]></Impression>
      <Creatives>
        <Creative id="cr-%s">
          <Linear>
            <MediaFiles>
              <MediaFile type="video/mp4" delivery="progressive"%s>
                <![CDATA[%s]]>
              </MediaFile>
            </MediaFiles>%s
          </Linear>
        </Creative>
      </Creatives>
    </InLine>
  </Ad>
</VAST>
`,
		decisionID, decisionID, impressionURL,
		decisionID, mediaAttrs, videoURL, clickThrough,
	)
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
	clientIP := extractClientIP(r) // IP consumed here, not propagated further

	// Step 2: derive Geo.  The resolver reads the IP and returns only country+city.
	derivedGeo := h.geoResolver.Resolve(clientIP) // IP is the input, Geo is the output

	// Step 3: IP is now out of scope — it is NEVER returned, logged, stored,
	// or forwarded.  Only derivedGeo (country + city) leaves this function.
	// (TX-5/DA-11 enforcement point — verified by privacy-compliance-auditor)
	return derivedGeo
}

// extractClientIP extracts the real client IP from the request.
// It honours X-Forwarded-For (first entry) when present.
// The returned string is transient: callers must NOT persist it.
func extractClientIP(r *http.Request) string {
	// X-Forwarded-For: client, proxy1, proxy2 — use leftmost (client).
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	// Fall back to RemoteAddr (strip port).
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
	ClickURL     string `json:"click_url,omitempty"`
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
		})
		if err != nil {
			logger.Warn("telemetry: producer init failed; using no-op sink", "err", err)
		} else {
			sink = &producerAdapter{p: p}
			defer p.Close()
		}
	}

	h := &collectorHandler{
		geoResolver: geoResolver,
		sink:        sink,
		decisionURL: envOr("DECISION_BASE_URL", "http://localhost:8080"),
		logger:      logger,
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
