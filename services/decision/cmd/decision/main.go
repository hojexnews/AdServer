// Command decision is the HTTP ad-decision service (hot path).
//
// Endpoints:
//
//	POST /v1/decide   — ad selection request; returns JSON creative or blank.
//	GET  /healthz     — liveness probe.
//
// Design constraints (invariants):
//   - stdlib net/http only (no fasthttp, no framework).
//   - No synchronous network call in the hot path beyond Redis capping
//     (best-effort + fail-safe DA-6).
//   - Every response includes a decision_id and model_version (even blank — TX-1).
//   - The IP address is resolved to geo and then immediately discarded (TX-5/DA-11).
//   - tenant_id is DERIVED SERVER-SIDE from the zone_id via snapshot (CA-1).
//     Any tenant_id supplied by the client is IGNORED.
//   - DecisionSink (I2): Redpanda producer with WAL + at-least-once + dedupe.
//   - Capper (I2): Redis-backed with fail-safe — no id → abort capped campaigns.
//   - GeoResolver (I2): MaxMind GeoLite2 in memory; degraded to empty on miss.
//   - Ranker (Fase 2 extension point): DefaultRanker in I0/I2; ML ranker in I4+.
//   - Click tokens: HMAC-SHA256 signed at serve time (CK_HMAC_SECRET, fail-closed).
//   - Capping salt: CAPPING_SALT required at boot; fail-closed if absent.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	decisionv1 "github.com/hojex/adserver/gen/go/adserver/decision/v1"
	"github.com/hojex/adserver/internal/capping"
	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/clicktoken"
	"github.com/hojex/adserver/internal/configload"
	"github.com/hojex/adserver/internal/geo"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
	"github.com/hojex/adserver/internal/telemetry"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// Decision sink extension point (I2: Redpanda producer)
// ---------------------------------------------------------------------------

// DecisionSink receives a Decision after it is built.  Implementations are
// fire-and-forget: they MUST NOT block the hot path.
type DecisionSink interface {
	// Emit sends the decision asynchronously.  Any error is logged by the
	// implementation; callers do not check the return value.
	Emit(ctx context.Context, d *decisionv1.Decision)
}

// StdoutSink writes decisions as JSON lines to stdout (debug / fallback).
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

// producerSink wraps *telemetry.Producer to satisfy DecisionSink.
type producerSink struct{ p *telemetry.Producer }

func (s *producerSink) Emit(ctx context.Context, d *decisionv1.Decision) {
	s.p.Emit(ctx, d)
}

// ---------------------------------------------------------------------------
// HTTP request / response types
// ---------------------------------------------------------------------------

// DecideRequest is the JSON body of POST /v1/decide.
type DecideRequest struct {
	// ZoneID is the publisher placement requesting an ad.
	// The tenant_id is derived SERVER-SIDE from this zone_id via the snapshot (CA-1).
	// Any "tenant_id" field sent by the client is IGNORED.
	ZoneID string `json:"zone_id"`
	// Geo fields: pre-derived by the collector before reaching this endpoint.
	// The collector discards the IP after deriving these (TX-5/DA-11).
	// When called from the asyncjs JS tag, the collector derives geo from
	// the client IP and passes only country+city here.
	GeoCountry string `json:"geo_country,omitempty"`
	GeoCity    string `json:"geo_city,omitempty"`
	// UserAgent of the end-user browser (coarse class only — never raw UA).
	UserAgent string `json:"user_agent,omitempty"`
	// SiteURL is the referer / page URL (sanitized: scheme+host+path only).
	SiteURL string `json:"site_url,omitempty"`
	// SiteVars are first-party custom variables from the ad tag.
	SiteVars map[string]string `json:"site_vars,omitempty"`
	// UserID is the stable identifier for frequency capping (DA-6).
	// It is hashed+salted SERVER-SIDE within the capping subsystem ONLY.
	// It is NEVER logged, emitted to telemetry, or forwarded to any other
	// component.  Empty = no stable identifier; all capped campaigns are skipped.
	//
	// Privacy model (canonical, single definition):
	//   The id enters the server and is immediately hashed with a rotating
	//   salt (CAPPING_SALT, from OpenBao) inside capping.Capper.Allowed().
	//   The raw id is confined to the hot-path stack frame of Allowed() only.
	//   It is never stored in any struct field, log record, telemetry event,
	//   or decision payload.  The Redis key carries only the salted hash.
	UserID string `json:"user_id,omitempty"`
}

// DecideResponse is the JSON response from POST /v1/decide.
// Empty creative fields mean a blank impression (CA-2).
type DecideResponse struct {
	// DecisionID must always be present (even for blank impressions — TX-1).
	DecisionID   string `json:"decision_id"`
	ModelVersion string `json:"model_version"`
	// TenantID is the server-derived tenant (from zone snapshot) — not client-supplied.
	TenantID string `json:"tenant_id"`
	ZoneID   string `json:"zone_id"`
	// ServedTier is OVERRIDE / CONTRACT / REMNANT / BLANK.
	ServedTier string `json:"served_tier"`
	// Creative fields — empty on blank.
	CampaignID string `json:"campaign_id,omitempty"`
	BannerID   string `json:"banner_id,omitempty"`
	// ClickTok is the HMAC-signed click token.  The collector validates this
	// token before issuing a redirect.  No plain dest_url is ever returned.
	ClickTok string `json:"click_tok,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	HTML     string `json:"html,omitempty"`
	VideoURL string `json:"video_url,omitempty"`
	Width    int32  `json:"width,omitempty"`
	Height   int32  `json:"height,omitempty"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

type decisionHandler struct {
	snap        *snapshot.Store
	cascade     *cascade.Engine
	sink        DecisionSink
	logger      *slog.Logger
	clickSigner *clicktoken.Signer // nil → click tokens omitted (degraded)
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

	// Security (CA-1): derive tenant_id SERVER-SIDE from zone_id.
	// ANY tenant_id field sent by the client is SILENTLY IGNORED.
	snap := h.snap.Snapshot()

	zone, zoneExists := snap.Zones[req.ZoneID]
	if !zoneExists || zone == nil {
		// Zone unknown in snapshot → fail-closed: BLANK impression.
		// Do not reveal whether the zone exists (information leak).
		if h.logger != nil {
			h.logger.Warn("decide: zone not found in snapshot — returning blank (fail-closed)",
				"zone_id", req.ZoneID)
		}
		decisionID := telemetry.NewULID()
		writeBlankResponse(w, decisionID, "", req.ZoneID)
		return
	}

	// tenantID is authoritative from snapshot — never from client input.
	tenantID := zone.TenantID

	// Geo is passed in already-derived by the collector (TX-5/DA-11):
	// no IP is present or needed in this handler.
	g := &commonv1.Geo{
		Country: req.GeoCountry,
		City:    req.GeoCity,
	}

	now := time.Now().UTC()

	// decision_id is the ULID for this decision — emitted on EVERY response
	// including blank (TX-1 invariant).
	decisionID := telemetry.NewULID()

	rulesCtx := &rules.Context{
		Geo:         g,
		SiteURL:     req.SiteURL,
		UserAgent:   req.UserAgent,
		RequestTime: now,
		SiteVars:    req.SiteVars,
	}

	cascadeReq := cascade.Request{
		ZoneID:      req.ZoneID,
		TenantID:    tenantID, // server-derived, not from client
		Rules:       rulesCtx,
		UserID:      req.UserID, // confined to capping subsystem only
		RequestTime: now,
	}

	result := h.cascade.Decide(cascadeReq, snap)

	// Build the Decision log entry (TX-1).
	// model_version is "" for cascata pura (DETERMINISTIC per decision.proto).
	// decision_id MUST be present on every decision — even blank (TX-1).
	envelope := &commonv1.Envelope{
		TenantId:      tenantID, // server-derived
		EventId:       decisionID,
		DecisionId:    decisionID,
		ModelVersion:  "", // cascata pura — no ML ranker in I0/I2
		OccurredAt:    timestamppb.New(now),
		SchemaVersion: "1.0.0",
		Source:        "delivery-decision",
	}

	// J0 (Fase 2, cascade-only): all propensity / exploration fields set here.
	//
	// J1 EXTENSION POINT — when the ML sidecar is wired via cascade.WithRanker():
	//   1. Propensity: replace 1.0 with the ranker's P(chosen | context, policy).
	//   2. ExplorationPolicy: set to EPSILON_GREEDY / THOMPSON / LINUCB as active.
	//   3. Epsilon: set to the current exploration rate (schedule from config).
	//   4. MlFailOpen: set to TRUE when the ranker exceeded TX-4 budget and the
	//      cascade fell back to deterministic order — signals OPE to exclude this
	//      decision from ML training data.
	//   5. model_version: set Envelope.ModelVersion to the deployed ranker tag.
	//   The cascade stratum order (Override > Contract > Remnant) is NEVER changed
	//   by the ML ranker; it only re-ranks within the winning stratum (DA-3).
	decision := &decisionv1.Decision{
		Envelope:   envelope,
		ZoneId:     req.ZoneID,
		ServedTier: result.ServedTier,
		// Propensity = 1.0: cascata pura (DETERMINISTIC) — the top-ranked
		// eligible candidate is chosen with certainty given the context.
		// This is the correct OPE baseline (J3); never set to 0 for a served ad.
		Propensity: 1.0,
		// ExplorationPolicy = DETERMINISTIC (not _UNSPECIFIED): J0 is explicitly
		// deterministic.  UNSPECIFIED would be ambiguous and break OPE filters.
		ExplorationPolicy: decisionv1.ExplorationPolicy_EXPLORATION_POLICY_DETERMINISTIC,
		Epsilon:    0, // no random exploration in cascade-only mode
		Candidates: result.Candidates,
		// CandidateCount = len(Candidates) in J0 (no truncation).
		// J1 may truncate the slice for large stratums; CandidateCount then
		// carries the true set size for density/competition signals.
		CandidateCount: uint32(len(result.Candidates)),
		// MlFailOpen = false: no ML sidecar attempted, so no fail-open occurred.
		// J1 sets this to true when the ranker times out (TX-4 budget exceeded)
		// and the cascade falls back to DefaultRanker.  Consumers filter these
		// out when building training datasets to avoid contamination.
		MlFailOpen: false,
	}

	if result.Campaign != nil {
		decision.CampaignId = result.Campaign.ID
	}
	if result.Banner != nil {
		decision.BannerId = result.Banner.ID
	}

	// Fire-and-forget emission.  The Redpanda producer (I2) is non-blocking.
	// NOTE: UserID is NOT included in the Decision proto — it never leaves
	// the capping subsystem (privacy model — DA-6).
	h.sink.Emit(r.Context(), decision)

	// Build HTTP response.
	resp := DecideResponse{
		DecisionID:   decisionID,
		ModelVersion: "",
		TenantID:     tenantID, // server-derived
		ZoneID:       req.ZoneID,
		ServedTier:   result.ServedTier.String(),
	}
	if result.Campaign != nil {
		resp.CampaignID = result.Campaign.ID
	}
	if result.Banner != nil {
		ban := result.Banner
		resp.BannerID = ban.ID
		resp.ImageURL = ban.ImageURL
		resp.HTML = ban.HTML
		resp.VideoURL = ban.VideoURL
		resp.Width = ban.Width
		resp.Height = ban.Height

		// Sign a click token embedding the dest_url from banner config.
		// The plain dest_url is NEVER returned to the client.
		// The collector validates the HMAC token before redirecting.
		if ban.ClickURL != "" && h.clickSigner != nil {
			expiry := now.Add(clicktoken.DefaultTTL)
			resp.ClickTok = h.clickSigner.Sign(decisionID, ban.ID, ban.ClickURL, expiry)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Decision-ID", decisionID)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Error("encode response", "err", err)
	}
}

// writeBlankResponse writes a minimal blank decision response (no creative).
// Used for fail-closed paths (unknown zone).
func writeBlankResponse(w http.ResponseWriter, decisionID, tenantID, zoneID string) {
	resp := DecideResponse{
		DecisionID:   decisionID,
		ModelVersion: "",
		TenantID:     tenantID,
		ZoneID:       zoneID,
		ServedTier:   "SERVED_TIER_BLANK",
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Decision-ID", decisionID)
	_ = json.NewEncoder(w).Encode(resp)
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// ---------------------------------------------------------------------------
	// Fail-closed guards: critical secrets must be present at boot.
	// ---------------------------------------------------------------------------

	// CAPPING_SALT: fail-closed (security #4 / privacy).
	// Remove fallback default — empty salt is predictable and correlatable.
	cappingSalt := os.Getenv("CAPPING_SALT")
	if cappingSalt == "" {
		logger.Error("FATAL: CAPPING_SALT environment variable is not set. " +
			"Capping requires a non-empty rotating salt (from OpenBao). " +
			"Refusing to start with a predictable default (fail-closed).")
		os.Exit(1)
	}

	// CK_HMAC_SECRET: fail-closed for click signing.
	// If absent, click tokens are not signed; /ck on the collector is disabled.
	ckSecret := os.Getenv("CK_HMAC_SECRET")
	var clickSigner *clicktoken.Signer
	if ckSecret == "" {
		logger.Error("CRITICAL: CK_HMAC_SECRET is not set — click signing disabled. " +
			"The collector's /ck endpoint will refuse all clicks (fail-closed). " +
			"Set CK_HMAC_SECRET to enable click tracking.")
	} else {
		var err error
		clickSigner, err = clicktoken.New(ckSecret, clicktoken.DefaultTTL)
		if err != nil {
			logger.Error("CRITICAL: failed to initialise click signer", "err", err)
			os.Exit(1)
		}
		logger.Info("click signer: HMAC-SHA256 token signing active")
	}

	// ---------------------------------------------------------------------------
	// Snapshot store — config is pulled from db/config (Postgres) via the
	// configload PostgresLoader when DATABASE_URL is set, and refreshed
	// periodically (SNAPSHOT_REFRESH_INTERVAL, default 30s).  Without
	// DATABASE_URL the service serves an EMPTY snapshot: every decision is
	// BLANK (safe default — no real ads until config is wired).
	//
	// Tenancy: the loader reads config cross-tenant (the in-memory snapshot is
	// global; CA-1 isolation is enforced in the cascade from the zone's
	// server-derived tenant).  The DSN must use the read-only BYPASSRLS role
	// (adserver_loader) — see internal/configload package doc.
	// ---------------------------------------------------------------------------
	store := snapshot.NewStore(snapshot.EmptySnapshot())
	var cfgLoader *configload.PostgresLoader
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		cl, err := configload.NewPostgresLoader(context.Background(), dsn, logger)
		if err != nil {
			logger.Error("config loader init failed; serving EMPTY snapshot (all BLANK)", "err", err)
		} else {
			cfgLoader = cl
			defer cl.Close()
			if loaded, err := cl.Load(context.Background()); err != nil {
				logger.Error("initial snapshot load failed; serving EMPTY until refresh", "err", err)
			} else {
				store.Replace(loaded)
			}
		}
	} else {
		logger.Warn("DATABASE_URL not set; serving EMPTY snapshot (every decision is BLANK). " +
			"Set DATABASE_URL to load ad config from db/config.")
	}

	// ---------------------------------------------------------------------------
	// Geo resolver (I2): MaxMind GeoLite2 in memory.
	// NOTE: The decision service itself does NOT see raw IPs — geo is derived
	// by the collector and passed as pre-resolved fields in the JSON body (TX-5).
	// The geo resolver is kept here for any future direct-client paths.
	// ---------------------------------------------------------------------------
	geoDBPath := envOr("GEOIP_DB_PATH", "")
	_ = geo.NewMaxMindResolver(geoDBPath, logger) // wired; unused in I2 direct path

	// ---------------------------------------------------------------------------
	// Capper (I2): Redis-backed with fail-safe (DA-6).
	// Falls back to NoOpCapper if Redis is not configured.
	// CAPPING_SALT is validated above; no default fallback here.
	// ---------------------------------------------------------------------------
	var cappingImpl cascade.Capper = cascade.NoOpCapper{}
	redisAddr := envOr("REDIS_ADDR", "")
	if redisAddr != "" {
		rdb := redis.NewClient(&redis.Options{
			Addr:         redisAddr,
			DialTimeout:  10 * time.Millisecond,
			ReadTimeout:  10 * time.Millisecond,
			WriteTimeout: 10 * time.Millisecond,
		})
		// cappingSalt is already validated non-empty above.
		cappingImpl = capping.New(rdb, cappingSalt)
		logger.Info("capping: Redis-backed capper active", "addr", redisAddr)
	} else {
		logger.Warn("capping: REDIS_ADDR not set; using NoOpCapper (no cap enforcement)")
	}

	// ---------------------------------------------------------------------------
	// Cascade engine with real capper + default (no-op) ranker.
	//
	// J1 EXTENSION POINT (TX-4 / Fase 2 ML ranker):
	//   Replace DefaultRanker with the ML sidecar ranker:
	//     mlRanker := mlsidecar.New(timeout=7ms, failOpen=cascade.DefaultRanker{})
	//     cascadeEngine = cascade.New(rulesEngine,
	//         cascade.WithCapper(cappingImpl),
	//         cascade.WithRanker(mlRanker))   // ← plug here
	//   The mlRanker MUST honour the TX-4 hard deadline (5–8ms p99):
	//   - On timeout: return the input slice unmodified (fail-open to deterministic).
	//   - On fail-open: the handler sets MlFailOpen=true and ExplorationPolicy=DETERMINISTIC.
	//   The cascade stratum order is NEVER changed by the ranker (DA-3).
	// ---------------------------------------------------------------------------
	rulesEngine := rules.New()
	cascadeEngine := cascade.New(rulesEngine, cascade.WithCapper(cappingImpl))

	// ---------------------------------------------------------------------------
	// Decision sink (I2): Redpanda producer with WAL + at-least-once + dedupe.
	// Falls back to StdoutSink if brokers are not configured.
	// ---------------------------------------------------------------------------
	var sink DecisionSink = &StdoutSink{logger: logger}
	brokers := envOr("REDPANDA_BROKERS", "")
	if brokers != "" {
		walPath := envOr("TELEMETRY_WAL_PATH", "/tmp/decision.wal")
		p, err := telemetry.NewProducer(telemetry.Config{
			Brokers:    strings.Split(brokers, ","),
			WALPath:    walPath,
			WALSync:    envOr("TELEMETRY_WAL_SYNC", "") == "true",
			QueueDepth: 8192,
			Logger:     logger,
			WireFormat: telemetry.ParseWireFormat(envOr("TELEMETRY_WIRE_FORMAT", "")),
		})
		if err != nil {
			logger.Warn("telemetry: producer init failed; using StdoutSink", "err", err)
		} else {
			sink = &producerSink{p: p}
			logger.Info("telemetry: Redpanda producer active", "brokers", brokers, "wal", walPath)
			defer p.Close()
		}
	} else {
		logger.Warn("telemetry: REDPANDA_BROKERS not set; using StdoutSink")
	}

	// ---------------------------------------------------------------------------
	// HTTP server
	// ---------------------------------------------------------------------------
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
		snap:        store,
		cascade:     cascadeEngine,
		sink:        sink,
		logger:      logger,
		clickSigner: clickSigner, // nil → click tokens omitted (degraded mode)
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

	// Start the periodic config refresher (no-op when DATABASE_URL is unset).
	if cfgLoader != nil {
		interval := snapshotRefreshInterval()
		refresher := snapshot.NewRefresher(cfgLoader, store, interval)
		go refresher.Start(ctx)
		logger.Info("config snapshot refresher started", "interval", interval.String())
	}

	go func() {
		logger.Info("decision service listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down decision service")

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

// snapshotRefreshInterval reads SNAPSHOT_REFRESH_INTERVAL (a Go duration like
// "30s") and falls back to 30s on absence or parse error.
func snapshotRefreshInterval() time.Duration {
	const def = 30 * time.Second
	v := os.Getenv("SNAPSHOT_REFRESH_INTERVAL")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
