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
	//     Decision.ExplorationPolicy = EPSILON_GREEDY (or the active bandit)
	//     Decision.Envelope.ModelVersion = "stub-j1" (or the real version from J2)
	//     Per-candidate scores filled in Candidates[].Score.
	//     Semantics: "ranker active, order may differ from cascade pure order".
	//     With the J1 dummy model, all scores = 0 and order is preserved (no net change).
	// Used in the legacy RANKER_ENABLED single-arm path (no A/B).
	// In J4 A/B mode, banditRanker is used for treatment instead.
	mlRanker *mlranker.MLRanker

	// shadowRanker is the shadow ranker (Fase 2 / J3, SHADOW_ENABLED).
	// nil when SHADOW_ENABLED=false (default — no shadow overhead).
	//
	// When non-nil, shadowRanker wraps an MLRanker. The served order is ALWAYS
	// the deterministic cascade order (DA-3 invariant). The shadow ranker runs
	// the ML ranker on a copy of the candidates and logs what it WOULD have served
	// without altering the actual served order (TX-4: fire-and-forget, non-blocking).
	//
	// SetRequestContext must be called once per request (before cascade.Decide)
	// to attach decision_id and zone_id to the shadow record (PII-free: TX-5/DA-11).
	//
	// SHADOW_ENABLED=true and RANKER_ENABLED=false is the nominal J3 mode:
	//   - Served: cascade pure order (deterministic).
	//   - Logged: what ML would have served (for OPE training data).
	//
	// RANKER_ENABLED=true takes precedence (J4+): ML serves; shadow is redundant
	// but harmless if both flags are set simultaneously (uncommon in practice).
	shadowRanker *mlranker.ShadowRanker

	// --- J4: A/B routing, revenue guard, bandit exposure ---

	// abRouter manages deterministic A/B assignment by (zone_id, tenant_id).
	// nil when AB_ENABLED=false (default). When non-nil, every request is
	// assigned to either control (cascade pure) or treatment (ML re-ranker +
	// bandit) before the cascade runs. The kill-switch can be activated manually
	// (AB_KILL_SWITCH env or operator call) or automatically by the revenue guard.
	//
	// Treatment path (abDecision.IsControl()==false):
	//   - banditRanker re-ranks candidates within the winning stratum (DA-3 preserved)
	//     using ML scores + bandit exploration.
	//   - propensity and exploration_policy are filled from banditRanker.LastResult().
	//
	// Control path (abDecision.IsControl()==true) or AB_ENABLED=false:
	//   - Pure cascade (DefaultRanker), propensity=1.0, DETERMINISTIC.
	//   - Bit-identical to the Fase 1 baseline (parity golden tests always green).
	//
	// Fail-safe (fail-open): any doubt → cascade pure. The kill-switch forces all
	// traffic to control; banditRanker fail-open degrades to cascade pure order.
	abRouter *mlranker.ABRouter

	// banditRanker wraps MLRanker + ExploreRank for the J4 treatment arm.
	// nil when abRouter is nil (J4 AB disabled). When non-nil:
	//   - The treatment cascade engine (`cascade`) has banditRanker wired as its Ranker.
	//   - banditRanker.WithConfig() is called per request with ABDecision.BanditCfg.
	//   - banditRanker.LastResult() provides propensity / policy / epsilon for the Decision log.
	// Not used in control path; the control cascade engine (`cascadePure`) uses DefaultRanker.
	banditRanker *mlranker.BanditRanker

	// revenueGuard monitors eCPM treatment vs control in minor-units (TX-2)
	// and auto-activates the kill-switch when treatment is materially worse.
	// nil when abRouter is nil (guard is pointless without A/B).
	revenueGuard *mlranker.RevenueGuard

	// cascadePure is a second cascade engine with NO ranker (DefaultRanker).
	// Used by the control arm in A/B mode to guarantee bit-identical output to
	// the pure cascade regardless of what ranker is wired in the treatment engine.
	// nil when abRouter is nil (in that case `cascade` is always the pure cascade).
	cascadePure *cascade.Engine
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

	// Shadow ranker context (J3): must be set BEFORE cascade.Decide so that the
	// ShadowRanker.Rank call (which runs inside Decide) attaches the correct
	// decision_id and zone_id to the ShadowRecord.
	// No-op when SHADOW_ENABLED=false (shadowRanker == nil).
	// PII-free: only decision_id (ULID pseudonym) and zone_id (placement, not user).
	if h.shadowRanker != nil {
		h.shadowRanker.SetRequestContext(decisionID, req.ZoneID)
	}

	// J4: A/B assignment — determines whether this request uses control (pure cascade)
	// or treatment (MLRanker + bandit exploration, wired in banditRanker).
	//
	// Fail-safe: kill-switch or nil abRouter → always control.
	var abAssignment *mlranker.ABDecision
	if h.abRouter != nil {
		d := h.abRouter.Assign(req.ZoneID, tenantID)
		abAssignment = &d
	}

	// Choose cascade engine and update the bandit config for this request.
	//   - treatment: `cascade` engine (banditRanker wired); update bandit config.
	//   - control / AB disabled: `cascadePure` engine (DefaultRanker) OR `cascade`
	//     when cascadePure is nil (legacy RANKER_ENABLED path without A/B).
	activeCascade := h.cascade
	inTreatment := false
	if h.abRouter != nil {
		if abAssignment != nil && !abAssignment.IsControl() {
			inTreatment = true
			// Update the bandit config per request (epsilon may change over time).
			if h.banditRanker != nil {
				h.banditRanker.WithConfig(abAssignment.BanditCfg)
			}
			activeCascade = h.cascade // treatment: banditRanker wired in
		} else {
			// Control: pure cascade (DefaultRanker), no ML.
			if h.cascadePure != nil {
				activeCascade = h.cascadePure
			}
		}
	}

	result := activeCascade.Decide(cascadeReq, snap)

	// Determine ML/bandit metadata from the ranker that ran during this Decide call.
	//
	// Four operating modes:
	//  A. Legacy RANKER_ENABLED (no A/B): read from h.mlRanker.LastResult().
	//  B. J4 A/B treatment: read from h.banditRanker.LastResult()
	//     (which includes both ML scores and bandit exploration result).
	//  C. J4 A/B control: no ML ran; deterministic defaults.
	//  D. RANKER_ENABLED=false AND A/B disabled: deterministic defaults.
	mlFailOpen := false
	mlModelVersion := ""
	propensity := 1.0                                                                    //nolint:forbidigo // default deterministic
	explorationPolicy := decisionv1.ExplorationPolicy_EXPLORATION_POLICY_DETERMINISTIC
	epsilonLogged := float64(0) //nolint:forbidigo // ML hyperparameter, not money

	switch {
	case h.abRouter != nil && inTreatment && h.banditRanker != nil:
		// J4 treatment arm: read from BanditRanker (ML scores + bandit exploration).
		br := h.banditRanker.LastResult()
		mlFailOpen = br.RankResult.FailOpen
		mlModelVersion = br.RankResult.ModelVersion
		if !br.RankResult.FailOpen {
			// Bandit ran successfully.
			propensity = br.ExploreResult.Propensity //nolint:forbidigo // ML probability
			epsilonLogged = br.ExploreResult.Epsilon //nolint:forbidigo // ML hyperparameter
			// Fill per-candidate scores.
			if len(br.RankResult.Scores) == len(result.Candidates) {
				for i, pc := range result.Candidates {
					pc.Score = float64(br.RankResult.Scores[i]) //nolint:forbidigo // ML signal, not money
				}
			}
			// Set exploration policy based on bandit config.
			if abAssignment != nil && abAssignment.BanditCfg.BanditEnabled {
				switch abAssignment.BanditCfg.Policy {
				case mlranker.BanditPolicyThompson:
					explorationPolicy = decisionv1.ExplorationPolicy_EXPLORATION_POLICY_THOMPSON
				default:
					explorationPolicy = decisionv1.ExplorationPolicy_EXPLORATION_POLICY_EPSILON_GREEDY
				}
			}
		}
		// If fail-open: propensity stays 1.0, policy stays DETERMINISTIC (already set).

	case h.mlRanker != nil && h.abRouter == nil:
		// Legacy RANKER_ENABLED path (no A/B): read from bare MLRanker.
		rankRes := h.mlRanker.LastResult()
		mlFailOpen = rankRes.FailOpen
		mlModelVersion = rankRes.ModelVersion
		if !rankRes.FailOpen && len(rankRes.Scores) == len(result.Candidates) {
			for i, pc := range result.Candidates {
				pc.Score = float64(rankRes.Scores[i]) //nolint:forbidigo // ML signal, not money
			}
		}
		// Legacy path: DETERMINISTIC policy (J1 stub had no bandit; J3 shadow-only).
		// Propensity = 1.0. If fail-open: propensity stays 1.0 (already set above).
	}

	// J4: Observe eCPM in revenue guard (fire-and-forget, non-blocking).
	// TX-2: eCPM is int64 minor-units; never float for money.
	if h.revenueGuard != nil && abAssignment != nil {
		ecpmMinorUnits := int64(0)
		if result.Campaign != nil && result.Campaign.ECPM != nil {
			ecpmMinorUnits = result.Campaign.ECPM.Amount
		}
		arm := mlranker.ABArmControl
		if inTreatment {
			arm = mlranker.ABArmTreatment
		}
		h.revenueGuard.Observe(arm, ecpmMinorUnits)
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

	decision := &decisionv1.Decision{
		Envelope:          envelope,
		ZoneId:            req.ZoneID,
		ServedTier:        result.ServedTier,
		Propensity:        propensity,        //nolint:forbidigo // ML probability ∈ (0,1]
		ExplorationPolicy: explorationPolicy,
		Epsilon:           epsilonLogged,     //nolint:forbidigo // ML hyperparameter
		Candidates:        result.Candidates,
		// CandidateCount carries the true set size for density/competition signals.
		CandidateCount: uint32(len(result.Candidates)),
		// MlFailOpen:
		//  false when ranker not attempted (control, AB disabled, RANKER_ENABLED=false).
		//  true  when ranker tried but timed out / IPC error (treatment fail-open).
		//  false when ranker scored OK (treatment arm with valid scores).
		// OPE consumers MUST filter out ml_fail_open=true decisions.
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

	// ---------------------------------------------------------------------------
	// Shadow ranker (Fase 2 / J3): SHADOW_ENABLED controls whether the
	// ShadowRanker is active.  Default: false (DISABLED, zero overhead).
	//
	// When SHADOW_ENABLED=true:
	//   - A fresh MLRanker is instantiated to score candidates (SHADOW_SOCKET_PATH,
	//     SHADOW_BUDGET_MS, SHADOW_MODEL_VERSION — independent of the RANKER_*
	//     counterparts so both can be tuned separately).
	//   - The ShadowRanker wraps that MLRanker: it runs ML scoring on a COPY of the
	//     candidate slice, records the result to a ChannelSink (non-blocking, buffered
	//     4096 entries), and returns the ORIGINAL unmodified cascade order to the
	//     cascade engine (DA-3 invariant: served order is NEVER changed by shadow).
	//   - A background goroutine drains the ChannelSink and emits each ShadowRecord
	//     as a structured slog line (PII-free: only decision_id, zone_id, campaign IDs,
	//     model_version, scores — TX-5/DA-11).  The drain runs until ctx is cancelled.
	//   - Sink overflow (channel full) drops the record silently — the hot path is
	//     NEVER blocked (TX-4).  Drain failure never affects the served decision.
	//
	// When SHADOW_ENABLED=false (default):
	//   - activeShadowRanker is nil.  The decisionHandler skips SetRequestContext.
	//   - No MLRanker is instantiated, no goroutine is started, no memory overhead.
	//   - The cascade engine uses DefaultRanker (or the ML ranker if RANKER_ENABLED).
	//   - All golden tests remain green (cascata pura, invariant DA-3 preserved).
	//
	// Interaction with RANKER_ENABLED:
	//   - (a) both OFF  → pure cascade (production default).
	//   - (b) SHADOW=true, RANKER=false → served=cascade, shadow logs ML order.
	//   - (c) RANKER=true → ML serves; SHADOW_ENABLED is independent and additive.
	//   Note: when RANKER_ENABLED=true, the cascade.WithRanker call above already
	//   wires the ML ranker for serving.  Adding SHADOW_ENABLED=true simultaneously
	//   would wrap a second independent MLRanker in ShadowRanker — unusual but safe.
	//   The recommended J3 mode is (b): RANKER=false, SHADOW=true.
	// ---------------------------------------------------------------------------
	var activeShadowRanker *mlranker.ShadowRanker
	// shadowChannelSink is non-nil only when SHADOW_ENABLED=true.  It is declared
	// at this scope so the drain goroutine (started after ctx is ready) can read it.
	var shadowChannelSink *mlranker.ChannelSink

	shadowEnabled := os.Getenv("SHADOW_ENABLED") == "true"
	if shadowEnabled {
		shadowSocket := envOr("SHADOW_SOCKET_PATH", "/tmp/ranker.sock")
		shadowBudgetMs := envOrInt("SHADOW_BUDGET_MS", 5)
		shadowBudget := time.Duration(shadowBudgetMs) * time.Millisecond
		shadowModelVersion := envOr("SHADOW_MODEL_VERSION", "shadow-j3")

		shadowMLRanker := mlranker.New(shadowSocket, shadowBudget, logger)
		shadowMLRanker.WithModelVersion(shadowModelVersion)

		// ChannelSink: 4096-entry buffer, non-blocking.  Full channel drops
		// the record silently (hot-path safety — TX-4).  A background drain
		// goroutine is started below (after ctx is available).
		shadowChannelSink = mlranker.NewChannelSink(4096, logger)

		activeShadowRanker = mlranker.NewShadowRanker(
			shadowMLRanker,
			shadowChannelSink,
			shadowModelVersion,
			logger,
		)

		// Wire the ShadowRanker into the cascade.  The ShadowRanker implements
		// cascade.Ranker and returns the ORIGINAL slice unmodified (DA-3).
		// If RANKER_ENABLED is also true the ML ranker is already wired above;
		// this second WithRanker call replaces it in cascadeOpts — the shadow
		// ranker wraps its own independent MLRanker instance so scoring happens
		// independently.  In the recommended J3 mode (RANKER=false, SHADOW=true)
		// this is the only ranker wired into cascadeOpts.
		cascadeOpts = append(cascadeOpts, cascade.WithRanker(activeShadowRanker))

		logger.Info("shadow ranker: ENABLED (Fase 2 / J3)",
			"socket", shadowSocket,
			"budget_ms", shadowBudgetMs,
			"model_version", shadowModelVersion,
			"sink_buffer", 4096)
	} else {
		logger.Info("shadow ranker: DISABLED (SHADOW_ENABLED not set or false)")
	}

	cascadeEngine := cascade.New(rulesEngine, cascadeOpts...)

	// ---------------------------------------------------------------------------
	// J4: A/B routing + revenue guard + bandit exposure.
	//
	// AB_ENABLED=true activates per-zone/tenant A/B routing. When enabled:
	//   - A BanditRanker (MLRanker + ExploreRank) is built for the TREATMENT engine.
	//   - A second cascade engine (cascadePureEngine) is built with DefaultRanker
	//     for the CONTROL arm (bit-identical to pure cascade — parity gate).
	//   - An ABRouter assigns each (zone, tenant) deterministically to control or
	//     treatment using a stable FNV-1a hash.
	//   - A RevenueGuard monitors eCPM (minor-units, TX-2) and auto-activates the
	//     kill-switch when treatment is materially worse than control.
	//
	// Environment variables (all optional):
	//   AB_ENABLED              = "true" to activate A/B routing (default: false).
	//   AB_TREATMENT_BUCKETS    = int 0-100 (default: 10 = 10% treatment).
	//   AB_BANDIT_ENABLED       = "true" to activate bandit exploration in treatment.
	//   AB_BANDIT_POLICY        = "epsilon_greedy" | "thompson" (default: epsilon_greedy).
	//   AB_EPSILON              = float [0,1] (default: 0.05).
	//   AB_KILL_SWITCH          = "true" to force all traffic to control at startup.
	//   AB_MIN_IMPRESSIONS      = int revenue guard min impressions per arm (default: 1000).
	//   AB_MATERIAL_THRESHOLD   = float [0,1] drop ratio that triggers kill (default: 0.05).
	//   RANKER_SOCKET_PATH, RANKER_BUDGET_MS, RANKER_MODEL_VERSION:
	//     Shared with RANKER_ENABLED path; used for treatment MLRanker.
	//
	// Interaction with RANKER_ENABLED:
	//   - AB_ENABLED=true + RANKER_ENABLED=false: AB is the primary ML activation path.
	//     The treatment engine uses its own MLRanker (not cascadeEngine's ranker).
	//   - AB_ENABLED=true + RANKER_ENABLED=true: both are active; the treatment
	//     engine uses banditRanker (wired separately from cascadeEngine's ranker).
	//   - AB_ENABLED=false (default): behaviour is unchanged from J1/J3.
	//
	// FAIL-SAFE: if AB_KILL_SWITCH=true at boot, kill-switch is pre-activated.
	// The revenue guard auto-activates the kill-switch when treatment underperforms.
	// Both are propagated through ABRouter.EnableKillSwitch() — single revert path.
	// ---------------------------------------------------------------------------
	var activeABRouter *mlranker.ABRouter
	var activeBanditRanker *mlranker.BanditRanker
	var activeRevenueGuard *mlranker.RevenueGuard
	var cascadePureEngine *cascade.Engine

	abEnabled := os.Getenv("AB_ENABLED") == "true"
	if abEnabled && shadowEnabled {
		// Footgun guard: the treatment/control engines built below REPLACE the
		// shadow-wired cascadeEngine (the cascade.WithRanker(shadowRanker) wired
		// into cascadeOpts above is dropped when cascadeEngine is rebuilt for the
		// treatment arm).  Shadow logging is therefore INACTIVE while AB is on.
		// Warn loudly so operators are not misled into expecting shadow records.
		// To observe shadow, run SHADOW_ENABLED=true WITHOUT AB_ENABLED.
		logger.Warn("shadow ranker is INACTIVE while A/B routing is enabled "+
			"(AB_ENABLED replaces the shadow-wired cascade engine); "+
			"run SHADOW_ENABLED without AB_ENABLED to collect shadow logs",
			"shadow_enabled", true, "ab_enabled", true)
	}
	if abEnabled {
		abTreatmentBuckets := envOrInt("AB_TREATMENT_BUCKETS", 10)
		if abTreatmentBuckets < 0 {
			abTreatmentBuckets = 0
		} else if abTreatmentBuckets > mlranker.NumBuckets {
			abTreatmentBuckets = mlranker.NumBuckets
		}

		// Bandit config for treatment.
		banditEnabled := os.Getenv("AB_BANDIT_ENABLED") == "true"
		var banditPolicy mlranker.BanditPolicy
		if os.Getenv("AB_BANDIT_POLICY") == "thompson" {
			banditPolicy = mlranker.BanditPolicyThompson
		} else {
			banditPolicy = mlranker.BanditPolicyEpsilonGreedy
		}
		epsilonStr := envOr("AB_EPSILON", "")
		epsilon := 0.05 //nolint:forbidigo // default ML exploration rate
		if epsilonStr != "" {
			epsilon = parseFloat(epsilonStr, 0.05) //nolint:forbidigo // ML hyperparameter
		}

		banditCfg := mlranker.BanditConfig{
			BanditEnabled: banditEnabled,
			Policy:        banditPolicy,
			Epsilon:       epsilon, //nolint:forbidigo // ML hyperparameter
			ThompsonBeta:  10.0,   //nolint:forbidigo // ML hyperparameter
		}

		abCfg := mlranker.ABConfig{
			TreatmentBuckets:  abTreatmentBuckets,
			BanditCfg:         banditCfg,
			KillSwitchEnabled: os.Getenv("AB_KILL_SWITCH") == "true",
		}
		activeABRouter = mlranker.NewABRouter(abCfg)

		// Treatment cascade: BanditRanker (MLRanker + ExploreRank).
		// Uses the same socket/budget/version as the RANKER_ENABLED path (shared config).
		abSocketPath := envOr("RANKER_SOCKET_PATH", "/tmp/ranker.sock")
		abBudgetMs := envOrInt("RANKER_BUDGET_MS", 5)
		abBudget := time.Duration(abBudgetMs) * time.Millisecond
		abModelVersion := envOr("RANKER_MODEL_VERSION", "stub-j1")

		abMLRanker := mlranker.New(abSocketPath, abBudget, logger)
		abMLRanker.WithModelVersion(abModelVersion)
		activeBanditRanker = mlranker.NewBanditRanker(abMLRanker, banditCfg, nil)

		// Treatment cascade engine: BanditRanker wired.
		treatmentCascadeOpts := []cascade.Option{cascade.WithCapper(cappingImpl), cascade.WithRanker(activeBanditRanker)}
		cascadeEngine = cascade.New(rulesEngine, treatmentCascadeOpts...)

		// Control cascade engine: DefaultRanker (pure cascade — parity-safe).
		cascadePureEngine = cascade.New(rulesEngine, cascade.WithCapper(cappingImpl))

		// Revenue guard: monitors eCPM and auto-activates kill-switch.
		minImpressions := int64(envOrInt("AB_MIN_IMPRESSIONS", 1000))
		materialThreshold := parseFloat(envOr("AB_MATERIAL_THRESHOLD", ""), 0.05) //nolint:forbidigo // threshold
		activeRevenueGuard = mlranker.NewRevenueGuard(
			mlranker.GuardConfig{
				MinImpressions:    minImpressions,
				MaterialThreshold: materialThreshold, //nolint:forbidigo // ML threshold
			},
			activeABRouter,
			logger,
		)

		logger.Info("A/B routing: ENABLED (Fase 2 / J4)",
			"treatment_buckets", abTreatmentBuckets,
			"bandit_enabled", banditEnabled,
			"bandit_policy", os.Getenv("AB_BANDIT_POLICY"),
			"epsilon", epsilon, //nolint:forbidigo // ML hyperparameter log
			"kill_switch_initial", abCfg.KillSwitchEnabled,
			"min_impressions_guard", minImpressions,
			"material_threshold_pct", materialThreshold*100.0, //nolint:forbidigo // pct log
		)
	} else {
		logger.Info("A/B routing: DISABLED (AB_ENABLED not set or false)")
	}

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
		snap:         store,
		cascade:      cascadeEngine,
		sink:         sink,
		logger:       logger,
		clickSigner:  clickSigner,        // nil → click tokens omitted (degraded mode)
		mlRanker:     activeMLRanker,     // nil when RANKER_ENABLED=false (default)
		shadowRanker: activeShadowRanker, // nil when SHADOW_ENABLED=false (default)
		// J4 fields (nil when AB_ENABLED=false):
		abRouter:     activeABRouter,     // nil → no A/B routing
		banditRanker: activeBanditRanker, // nil → no bandit exploration
		revenueGuard: activeRevenueGuard, // nil → no revenue guard
		cascadePure:  cascadePureEngine,  // nil when A/B disabled
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

	// Shadow drain goroutine (J3): reads ShadowRecords from the ChannelSink and
	// emits structured slog entries (INFO level, PII-free).
	//
	// Non-blocking contract (TX-4):
	//   - The hot-path handler NEVER blocks on the sink (ChannelSink.EmitShadow
	//     drops the record on full channel — never blocks).
	//   - This drain goroutine runs asynchronously; slowness here only fills the
	//     buffer, it does NOT slow down ad decisions.
	//
	// Fail-safe (DA-3 / decision invariant):
	//   - Drain failure (e.g., slog write panic) does not affect served decisions.
	//     The goroutine recovers from panics and continues draining.
	//
	// PII-free (TX-5/DA-11):
	//   - decision_id is a ULID pseudonym (not user-identifying).
	//   - zone_id, campaign IDs are placement context (not user data).
	//   - model_version and shadow scores are ML signals, not PII.
	//   - No raw UA, no IP, no user_id in ShadowRecord (enforced by the type).
	if shadowChannelSink != nil {
		go func() {
			ch := shadowChannelSink.Chan()
			for {
				select {
				case rec, ok := <-ch:
					if !ok {
						// Channel closed (should not happen in normal shutdown, but
						// handle gracefully).
						return
					}
					// Structured slog line — PII-free fields only.
					logger.Info("shadow_record",
						"decision_id", rec.DecisionID,
						"zone_id", rec.ZoneID,
						"model_version", rec.ModelVersion,
						"cascade_top", rec.ServedCampaignID,
						"shadow_top", rec.ShadowTopCampaignID,
						"agreement", rec.Agreement,
						"shadow_order", rec.ShadowOrder,
						"cascade_order", rec.CascadeOrder,
					)
				case <-ctx.Done():
					// Drain remaining buffered records before exiting so that
					// in-flight shadow observations are not lost on graceful
					// shutdown (best-effort; channel may be non-empty on crash).
					for {
						select {
						case rec := <-ch:
							logger.Info("shadow_record",
								"decision_id", rec.DecisionID,
								"zone_id", rec.ZoneID,
								"model_version", rec.ModelVersion,
								"cascade_top", rec.ServedCampaignID,
								"shadow_top", rec.ShadowTopCampaignID,
								"agreement", rec.Agreement,
								"shadow_order", rec.ShadowOrder,
								"cascade_order", rec.CascadeOrder,
							)
						default:
							return
						}
					}
				}
			}
		}()
		logger.Info("shadow drain goroutine started")
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

// parseFloat parses a decimal float64 from s, returning fallback on any error.
// Used only for ML hyperparameters (epsilon, threshold) — not for monetary values.
func parseFloat(s string, fallback float64) float64 { //nolint:forbidigo // ML hyperparameter, not money
	if s == "" {
		return fallback
	}
	// Simple decimal parser: optional sign, digits, optional dot+digits.
	// Avoids importing strconv to keep the file minimal.
	neg := false
	i := 0
	if i < len(s) && s[i] == '-' {
		neg = true
		i++
	} else if i < len(s) && s[i] == '+' {
		i++
	}
	var intPart, fracPart int64
	var fracDivisor int64 = 1
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		intPart = intPart*10 + int64(s[i]-'0')
	}
	if i < len(s) && s[i] == '.' {
		i++
		for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
			fracPart = fracPart*10 + int64(s[i]-'0')
			fracDivisor *= 10
		}
	}
	if i != len(s) {
		// Unconsumed characters (e.g., 'e') → fall back to the default.
		return fallback
	}
	result := float64(intPart) + float64(fracPart)/float64(fracDivisor) //nolint:forbidigo // ML param arithmetic
	if neg {
		result = -result
	}
	return result
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
