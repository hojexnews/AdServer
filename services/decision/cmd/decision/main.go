// Command decision is the HTTP ad-decision service (hot path).
//
// Endpoints:
//
//	POST /v1/decide   — ad selection request; returns JSON creative or blank.
//	GET  /healthz     — liveness probe.
//
// Design constraints:
//   - stdlib net/http only (no fasthttp, no framework).
//   - No synchronous network call in the hot path beyond Redis capping
//     (capping is I2; here the NoOpCapper is wired).
//   - Every response includes a decision_id and model_version (even blank).
//   - The IP address is resolved to geo and then immediately discarded (TX-5).
//   - tenant_id is propagated through every Decision struct.
//   - The DecisionSink interface is a no-op in I0; the Redpanda producer is I2.
package main

import (
	cryptorand "crypto/rand"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	decisionv1 "github.com/hojex/adserver/gen/go/adserver/decision/v1"
	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// Decision sink extension point (I2: Redpanda producer)
// ---------------------------------------------------------------------------

// DecisionSink receives a Decision after it is built.  Implementations are
// fire-and-forget: they MUST NOT block the hot path.
//
// The Redpanda-backed implementation with WAL + at-least-once + dedupe by
// event_id arrives in I2 (internal/telemetry).
type DecisionSink interface {
	// Emit sends the decision asynchronously.  Any error is logged by the
	// implementation; callers do not check the return value.
	Emit(ctx context.Context, d *decisionv1.Decision)
}

// StdoutSink writes decisions as JSON lines to stdout (I0 / debug).
type StdoutSink struct{ logger *slog.Logger }

func (s *StdoutSink) Emit(_ context.Context, d *decisionv1.Decision) {
	b, _ := json.Marshal(map[string]any{
		"decision_id":   d.GetEnvelope().GetDecisionId(),
		"model_version": d.GetEnvelope().GetModelVersion(),
		"tenant_id":     d.GetEnvelope().GetTenantId(),
		"zone_id":       d.GetZoneId(),
		"campaign_id":   d.GetCampaignId(),
		"banner_id":     d.GetBannerId(),
		"served_tier":   d.GetServedTier().String(),
	})
	s.logger.Info("decision", "payload", string(b))
}

// ---------------------------------------------------------------------------
// HTTP request / response types
// ---------------------------------------------------------------------------

// DecideRequest is the JSON body of POST /v1/decide.
type DecideRequest struct {
	// TenantID identifies the tenant owning this zone (server-side isolation).
	TenantID string `json:"tenant_id"`
	// ZoneID is the publisher placement requesting an ad.
	ZoneID string `json:"zone_id"`
	// Geo is the pre-derived geo.  The IP was already discarded by the
	// caller before reaching this endpoint (TX-5 / DA-11).
	GeoCountry string `json:"geo_country,omitempty"`
	GeoCity    string `json:"geo_city,omitempty"`
	// UserAgent of the end-user browser.
	UserAgent string `json:"user_agent,omitempty"`
	// SiteURL is the referer / page URL.
	SiteURL string `json:"site_url,omitempty"`
	// SiteVars are first-party custom variables from the ad tag.
	SiteVars map[string]string `json:"site_vars,omitempty"`
	// UserID is the hashed+salted stable identifier for frequency capping
	// (DA-6).  Empty = no stable identifier; all capped campaigns are skipped.
	UserID string `json:"user_id,omitempty"`
}

// DecideResponse is the JSON response from POST /v1/decide.
// Empty creative fields mean a blank impression (CA-2).
type DecideResponse struct {
	// DecisionID must always be present (even for blank impressions).
	DecisionID   string `json:"decision_id"`
	ModelVersion string `json:"model_version"`
	TenantID     string `json:"tenant_id"`
	ZoneID       string `json:"zone_id"`
	// ServedTier is OVERRIDE / CONTRACT / REMNANT / BLANK.
	ServedTier string `json:"served_tier"`
	// Creative fields — empty on blank.
	CampaignID string `json:"campaign_id,omitempty"`
	BannerID   string `json:"banner_id,omitempty"`
	ClickURL   string `json:"click_url,omitempty"`
	ImageURL   string `json:"image_url,omitempty"`
	HTML       string `json:"html,omitempty"`
	VideoURL   string `json:"video_url,omitempty"`
	Width      int32  `json:"width,omitempty"`
	Height     int32  `json:"height,omitempty"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

type decisionHandler struct {
	snap    *snapshot.Store
	cascade *cascade.Engine
	sink    DecisionSink
	logger  *slog.Logger
}

func (h *decisionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req DecideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Geo is passed in already-derived by the caller.
	// No IP is present in this handler (TX-5).
	g := &commonv1.Geo{
		Country: req.GeoCountry,
		City:    req.GeoCity,
	}

	now := time.Now().UTC()
	decisionID := newDecisionID(now)

	snap := h.snap.Snapshot()

	rulesCtx := &rules.Context{
		Geo:         g,
		SiteURL:     req.SiteURL,
		UserAgent:   req.UserAgent,
		RequestTime: now,
		SiteVars:    req.SiteVars,
	}

	cascadeReq := cascade.Request{
		ZoneID:      req.ZoneID,
		TenantID:    req.TenantID,
		Rules:       rulesCtx,
		UserID:      req.UserID,
		RequestTime: now,
	}

	result := h.cascade.Decide(cascadeReq, snap)

	// Build the Decision log entry (TX-1).
	// model_version is "" for cascata pura (DETERMINISTIC per decision.proto).
	// decision_id MUST be present on every decision — even blank.
	envelope := &commonv1.Envelope{
		TenantId:      req.TenantID,
		EventId:       decisionID, // event_id == decision_id for the root event
		DecisionId:    decisionID,
		ModelVersion:  "", // cascata pura — no ML ranker in I0
		OccurredAt:    timestamppb.New(now),
		SchemaVersion: "1.0.0",
		Source:        "delivery-decision",
	}

	decision := &decisionv1.Decision{
		Envelope:          envelope,
		ZoneId:            req.ZoneID,
		ServedTier:        result.ServedTier,
		Propensity:        1.0, // cascata pura = 1.0 always
		ExplorationPolicy: decisionv1.ExplorationPolicy_EXPLORATION_POLICY_DETERMINISTIC,
		Epsilon:           0,
		Candidates:        result.Candidates,
		CandidateCount:    uint32(len(result.Candidates)),
		MlFailOpen:        false,
	}

	if result.Campaign != nil {
		decision.CampaignId = result.Campaign.ID
	}
	if result.Banner != nil {
		decision.BannerId = result.Banner.ID
	}

	// Fire-and-forget emission.
	// I2 will replace StdoutSink with the Redpanda WAL producer.
	h.sink.Emit(r.Context(), decision)

	// Build HTTP response.
	resp := DecideResponse{
		DecisionID:   decisionID,
		ModelVersion: "",
		TenantID:     req.TenantID,
		ZoneID:       req.ZoneID,
		ServedTier:   result.ServedTier.String(),
	}
	if result.Campaign != nil {
		resp.CampaignID = result.Campaign.ID
	}
	if result.Banner != nil {
		ban := result.Banner
		resp.BannerID = ban.ID
		resp.ClickURL = ban.ClickURL
		resp.ImageURL = ban.ImageURL
		resp.HTML = ban.HTML
		resp.VideoURL = ban.VideoURL
		resp.Width = ban.Width
		resp.Height = ban.Height
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Decision-ID", decisionID)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Error("encode response", "err", err)
	}
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Stub snapshot — I1 will wire the real Loader from db/config/.
	snap := snapshot.EmptySnapshot()
	store := snapshot.NewStore(snap)

	// Cascade engine with no-op ranker and no-op capper (I0).
	rulesEngine := rules.New()
	cascadeEngine := cascade.New(rulesEngine)

	sink := &StdoutSink{logger: logger}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		s := store.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":           "ok",
			"snapshot_version": s.Version,
		})
	})

	mux.Handle("POST /v1/decide", &decisionHandler{
		snap:    store,
		cascade: cascadeEngine,
		sink:    sink,
		logger:  logger,
	})

	addr := envOr("DECISION_ADDR", ":8080")
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
		logger.Info("decision service listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

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

// newDecisionID generates a time-sortable unique ID for the decision.
// Format: <RFC3339Nano timestamp>-<8 random bytes hex>
// I2 may replace this with a proper ULID library if canonical format is needed.
func newDecisionID(t time.Time) string {
	b := make([]byte, 8)
	if _, err := cryptorand.Read(b); err != nil {
		// Extremely unlikely; fall back to timestamp-only.
		return t.Format("20060102T150405.999999999Z")
	}
	return t.Format("20060102T150405.999999999Z") + "-" + hex.EncodeToString(b)
}
