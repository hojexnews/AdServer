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
	mlranker "github.com/hojex/adserver/internal/ranker"
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

	// mlRanker is the ML re-ranker client (Fase 2 / J1).
	// nil when RANKER_ENABLED=false (default in J1 — flag is OFF by default).
	// When non-nil, the ranker is already wired into cascadeEngine via
	// cascade.WithRanker; mlRanker is also stored here to access LastResult()
	// after Decide returns (for filling Decision.MlFailOpen and per-candidate scores).
	//
	// Two states (from the J1 spec):
	//  1. RANKER_ENABLED=false (mlRanker==nil):
	//     Decision.MlFailOpen = false
	//     Decision.ExplorationPolicy = DETERMINISTIC
	//     Decision.Envelope.ModelVersion = ""
	//     Semantics: "ranker not attempted" — OPE baseline, pure cascade order.
	//
	//  2. RANKER_ENABLED=true, ranker attempted but failed (fail-open):
	//     Decision.MlFailOpen = true
	//     Decision.ExplorationPolicy = DETERMINISTIC  (cascade fallback)
	//     Decision.Envelope.ModelVersion = ""
	//     Semantics: "ranker tried, timed out / IPC error" — OPE must exclude.
	//
	//  3. RANKER_ENABLED=true, ranker scored successfully (even with dummy model):
	//     Decision.MlFailOpen = false
	//     Decision.ExplorationPolicy = EPSILON_GREEDY (or the active bandit; DETERMINISTIC for J1 stub)
	//     Decision.Envelope.ModelVersion = "stub-j1" (or the real version from J2)
	//     Per-candidate scores filled in Candidates[].Score.
	//     Semantics: "ranker active, order may differ from cascade pure order".
	//     With the J1 dummy model, all scores = 0 and order is preserved (no net change).
	mlRanker *mlranker.MLRanker
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

	// Determine ML ranker metadata AFTER the cascade result.
	//
	// When RANKER_ENABLED=true, the cascade already ran the ML re-ranker
	// (via cascade.WithRanker). We now read the RankResult from the ranker
	// to propagate ml_fail_open, model_version, and per-candidate scores.
	//
	// States (documented in the mlRanker field comment):
	//  - mlRanker == nil (RANKER_ENABLED=false): pure cascade, MlFailOpen=false.
	//  - mlRanker != nil, FailOpen=true: ranker attempted, timed out / IPC error.
	//  - mlRanker != nil, FailOpen=false: ranker scored successfully.
	//    With J1 dummy model: scores=0, order unchanged, but MlFailOpen=false.
	mlFailOpen := false
	mlModelVersion := ""
	if h.mlRanker != nil {
		rankRes := h.mlRanker.LastResult()
		mlFailOpen = rankRes.FailOpen
		mlModelVersion = rankRes.ModelVersion

		// Overwrite per-candidate Score with ML score (even if 0 for dummy model).
		// The score field in Candidates[] is the ML signal, not the deterministic key.
		// With the dummy model all scores are 0; with the real model (J2) they are pCTR.
		if !rankRes.FailOpen && len(rankRes.Scores) == len(result.Candidates) {
			for i, pc := range result.Candidates {
				pc.Score = float64(rankRes.Scores[i]) //nolint:forbidigo // ML ranking signal, not money
			}
		}
	}

	// Build the Decision log entry (TX-1).
	// model_version is "" for pure cascade; non-empty when ML ranker scored.
	// decision_id MUST be present on every decision — even blank (TX-1).
	envelope := &commonv1.Envelope{
		TenantId:      tenantID, // server-derived
		EventId:       decisionID,
		DecisionId:    decisionID,
		ModelVersion:  mlModelVersion,
		OccurredAt:    timestamppb.New(now),
		SchemaVersion: "1.0.0",
		Source:        "delivery-decision",
	}

	// Exploration policy:
	//  - RANKER_ENABLED=false OR fail-open: DETERMINISTIC (cascade pure order).
	//  - RANKER_ENABLED=true, scored: DETERMINISTIC for J1 stub (epsilon=0).
	//    J3+ will set EPSILON_GREEDY / THOMPSON / LINUCB and fill Epsilon.
	explorationPolicy := decisionv1.ExplorationPolicy_EXPLORATION_POLICY_DETERMINISTIC
	if h.mlRanker != nil && !mlFailOpen {
		// Ranker scored successfully. In J1 the stub always uses DETERMINISTIC
		// (no exploration — dummy scores are all equal, no real bandit yet).
		// J3 will change this to EPSILON_GREEDY / THOMPSON based on config.
		explorationPolicy = decisionv1.ExplorationPolicy_EXPLORATION_POLICY_DETERMINISTIC
	}

	decision := &decisionv1.Decision{
		Envelope:   envelope,
		ZoneId:     req.ZoneID,
		ServedTier: result.ServedTier,
		// Propensity = 1.0 under DETERMINISTIC policy (both pure cascade and J1 stub).
		// The cascade (or the dummy ranker with all-zero scores + stable sort)
		// deterministically selects the top-ranked eligible candidate, so
		// P(action | context, policy) = 1.0. Correct OPE baseline for J3.
		// J3+ will replace this with the bandit propensity when exploration is active.
		Propensity:        1.0,
		ExplorationPolicy: explorationPolicy,
		Epsilon:           0, // no random exploration in J1
		Candidates:        result.Candidates,
		// CandidateCount carries the true set size for density/competition signals
		// even if the Candidates slice is truncated in future increments.
		CandidateCount: uint32(len(result.Candidates)),
		// MlFailOpen:
		//  false when RANKER_ENABLED=false (state 1 — ranker not attempted).
		//  true  when RANKER_ENABLED=true but timed out / IPC error (state 2).
		//  false when RANKER_ENABLED=true and scored OK (state 3 — even dummy model).
		// OPE consumers MUST filter out ml_fail_open=true decisions (propensity is
		// meaningless when the ranker fell back to cascade order without scoring).
		MlFailOpen: mlFailOpen,
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
		ModelVersion: mlModelVersion,
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
	// Cascade engine with real capper + optional ML re-ranker (Fase 2 / J1).
	//
	// RANKER_ENABLED controls whether the ML re-ranker is active.
	// Default: false (DISABLED). The cascade uses DefaultRanker (deterministic).
	//
	// When RANKER_ENABLED=true:
	//   - RANKER_SOCKET_PATH: path to the ranker sidecar Unix socket.
	//     Default: /tmp/ranker.sock
	//   - RANKER_BUDGET_MS: hard TX-4 deadline for a full Rank call (ms).
	//     Default: 5 (ms). Must fit within the 5–8 ms p99 budget (TX-4).
	//
	// ML re-ranker semantics (J1 with stub/dummy model):
	//   - The sidecar returns score=0 for every candidate.
	//   - stable sort preserves the cascade deterministic order.
	//   - Net effect on decision: ZERO (order unchanged, scores=0).
	//   - Decision.ml_fail_open = false (ranker succeeded — just dummy scores).
	//   - Decision.model_version = "stub-j1" (or RANKER_MODEL_VERSION env var).
	//
	// J2 will swap in the real .onnx model; the flag and socket path are unchanged.
	// DA-3 invariant: the cascade stratum order is NEVER changed by the ranker.
	// TX-4 invariant: timeout fires → fail-open → cascade deterministic order.
	// ---------------------------------------------------------------------------
	rulesEngine := rules.New()
	var activeMLRanker *mlranker.MLRanker
	cascadeOpts := []cascade.Option{cascade.WithCapper(cappingImpl)}

	rankerEnabled := os.Getenv("RANKER_ENABLED") == "true"
	if rankerEnabled {
		socketPath := envOr("RANKER_SOCKET_PATH", "/tmp/ranker.sock")
		budgetMs := envOrInt("RANKER_BUDGET_MS", 5)
		budget := time.Duration(budgetMs) * time.Millisecond

		activeMLRanker = mlranker.New(socketPath, budget, logger)

		// Read model version from env (set by the sidecar operator at deploy time).
		// In J1 stub mode the sidecar reports "stub-j1" regardless; this env var
		// lets the operator override it without redeploying the decision service.
		modelVersion := envOr("RANKER_MODEL_VERSION", "stub-j1")
		activeMLRanker.WithModelVersion(modelVersion)

		cascadeOpts = append(cascadeOpts, cascade.WithRanker(activeMLRanker))

		logger.Info("ML ranker: ENABLED (Fase 2 / J1)",
			"socket", socketPath,
			"budget_ms", budgetMs,
			"model_version", modelVersion)
	} else {
		logger.Info("ML ranker: DISABLED (RANKER_ENABLED not set or false); using cascade DefaultRanker")
	}

	cascadeEngine := cascade.New(rulesEngine, cascadeOpts...)

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
		mlRanker:    activeMLRanker, // nil when RANKER_ENABLED=false (default)
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

// envOrInt reads an integer environment variable, falling back to def on
// absence or parse error.
func envOrInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n := 0
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return def
	}
	return n
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
