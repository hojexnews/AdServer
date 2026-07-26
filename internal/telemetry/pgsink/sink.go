// Package pgsink implements a telemetry event sink backed by Postgres —
// the collector's PERSISTENCE dependency for the "perfil BETA" milestone
// (addon §3.3), used when Redpanda/ClickHouse/Iceberg are not yet live.
//
// # Why this exists
//
// services/collector/cmd/collector/main.go defaults to a no-op EventSink and
// only switches to the Redpanda telemetry.Producer when REDPANDA_BROKERS is
// set. Without either, every impression/click/conversion pixel is measured
// and immediately discarded — there is no data to show in the dashboard and
// nothing to reconcile for billing. PostgresSink closes that gap with a
// SINGLE additional dependency (Postgres, already required elsewhere in the
// stack) instead of standing up the full Redpanda/ClickHouse pipeline.
//
// # Scope (deliberately narrow)
//
//   - Persists AdRequest/Impression/Click/Conversion events to
//     stats.events_raw (db/stats/migrations/0001_stats_schema_up.sql).
//     AdRequest feeds the "requests" KPI (stats.live_kpis) that CA-6's
//     inventoryLoss = requests - impressions needs — without it, a BFF
//     reader has no honest source for that number (see the migration's
//     "AdRequest / requests" header note for why requests always lands on
//     its own per-tenant row with campaign_id IS NULL: AdRequest precedes
//     the decision that picks a campaign, so it structurally cannot be
//     attributed to one).
//   - EmitAdRequest persists ONLY tenant_id/zone_id/decision_id — geo, the
//     coarse user-agent class, the sanitized referer, cachebuster and the
//     publisher-supplied custom_vars map are DISCARDED, never reaching
//     Postgres (see EmitAdRequest's doc comment and package doc "Privacy").
//   - Does NOT replace the Redpanda/ClickHouse/Iceberg architecture (ADR-0001,
//     DA-7, mandate §2.2). stats.live_kpis is explicitly labelled "ao vivo"
//     and is NEVER the billing source of truth — billing reconciles only
//     against the Iceberg lakehouse.
//
// # Hot-path / durability model
//
// Emit* methods never block on I/O: each call builds a pendingEvent and does
// a non-blocking send into a buffered channel (mirrors the queue+drainLoop
// pattern of internal/telemetry.Producer). A single background goroutine
// drains the channel and performs the actual Postgres write. If the channel
// is full, the event is dropped and counted (DroppedCount) — the caller
// (collector HTTP handler) is never made to wait or fail the pixel response.
//
// Unlike telemetry.Producer, PostgresSink does NOT keep a separate
// file-based write-ahead log ahead of the queue: internal/telemetry's WAL
// type is unexported (no constructor outside package telemetry) and,
// architecturally, a committed Postgres transaction already IS a durable
// write-ahead log — there is nothing further to persist once tx.Commit
// succeeds. The accepted gap is the same class the Producer already has for
// its own network hop (the window between accepting the event and the
// broker ACK): a process crash while an event sits in the in-memory channel
// loses that one event. For a beta-scale rollout trading a small, honestly
// documented and counted loss window for a single-dependency design, this is
// judged acceptable; it is not silent (dropped events are counted and
// logged), and closing it fully (a file WAL ahead of the channel, matching
// telemetry.Producer) is a reasonable future addition if the beta traffic
// profile ever needs it.
//
// # Idempotency (DA-7, at-least-once)
//
// Every event gets a fresh ULID event_id via telemetry.NewULID() — the SAME
// generator internal/telemetry.Producer uses for the Redpanda path — and is
// written with `INSERT ... ON CONFLICT (event_id) DO NOTHING`
// (db/stats/migrations/0001_stats_schema_up.sql: events_raw_pk). Re-emitting
// the identical logical write path twice can never duplicate a row.
//
// # Multi-tenant RLS on the write path (TX-3)
//
// Each event is written in its OWN transaction: BEGIN; SELECT
// set_config('adserver.tenant_id', $1, true); INSERT ... ; COMMIT. This is
// deliberate, not accidental per-event overhead: stats.events_raw has
// FORCE ROW LEVEL SECURITY with a WITH CHECK policy comparing the row's
// tenant_id against the session GUC, and the collector fans in events from
// many different tenants on ONE shared worker goroutine — there is no single
// GUC value that is correct for a batch spanning tenants. Scoping the GUC to
// one transaction per event keeps the WITH CHECK guarantee meaningful (a
// bug that ever tried to write tenant B's row while the GUC says tenant A
// gets rejected by the database, not just by application code) at the cost
// of one extra round trip per event — acceptable at beta traffic volumes.
//
// # CA-6 (blank impressions are never billable)
//
// billable/blank arrive as parameters ALREADY computed by the single
// production function blank.ComputeBlankBillable (internal/telemetry/blank),
// called once in services/collector/cmd/collector/main.go:handleImpression
// before ANY sink (Redpanda producer or this one) is invoked. This package
// does not recompute that decision — doing so was exactly the class of bug
// the 30th wave found (a second, drifting reimplementation). EmitImpression
// only clamps the two booleans defensively if they ever arrive contradictory
// (see clampBillable) and stats.events_raw additionally enforces
// `CHECK (NOT (blank AND billable))` unconditionally in the database.
//
// # Privacy (TX-5/DA-11)
//
// The free-text fields that DO reach this sink (campaign_id, banner_id,
// zone_id, decision_id, model_version, dest_url, attribution_decision_id)
// come from unauthenticated, unvalidated collector query parameters
// (services/collector/cmd/collector/main.go: q.Get("cid") etc.) — an
// attacker can put arbitrary text there. Before every write, the background
// worker scans the concatenation of those fields with
// internal/privacyscan.ContainsIPLiteral and drops (never persists) any
// event where an IP-literal substring is found, counting it separately
// (IPLiteralDroppedCount) for observability. This runs off the HTTP hot path
// (in the background worker), so it never blocks /lg or /ck.
package pgsink

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/privacyscan"
	"github.com/hojex/adserver/internal/telemetry"
)

// Event-type discriminators — MUST match stats.events_raw.events_raw_type_chk
// (db/stats/migrations/0001_stats_schema_up.sql).
const (
	eventTypeAdRequest  = "ad_request"
	eventTypeImpression = "impression"
	eventTypeClick      = "click"
	eventTypeConversion = "conversion"
)

// insertEventSQL is the single write path for every persisted event.
// ON CONFLICT (event_id) DO NOTHING is the idempotency mechanism (DA-7):
// re-emitting the same logical write twice is a safe no-op, never a
// duplicate row. See db/stats/migrations/0001_stats_schema_up.sql for the
// column definitions and events_raw_pk.
const insertEventSQL = `
	INSERT INTO stats.events_raw
		(event_id, event_type, tenant_id, campaign_id, banner_id, zone_id,
		 decision_id, model_version, served_tier, billable, blank,
		 dest_url, attribution_decision_id, occurred_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	ON CONFLICT (event_id) DO NOTHING
`

// setTenantGUCSQL scopes the RLS WITH CHECK comparison to this transaction
// only (SET LOCAL semantics via set_config's third argument — see package
// doc "Multi-tenant RLS on the write path").
const setTenantGUCSQL = `SELECT set_config('adserver.tenant_id', $1, true)`

// pendingEvent is the internal, sink-agnostic representation enqueued by
// every Emit* method and consumed by the background worker.
type pendingEvent struct {
	eventID   string
	eventType string
	tenantID  string
	// campaignID: *int64, not string — see db/stats/migrations/
	// 0001_stats_schema_up.sql header "TIPOS" for why the column is BIGINT
	// (JOIN-compatible with config.campaigns.id, which the BFF's read path
	// requires). nil means "no campaign" — either genuinely inapplicable
	// (AdRequest, which precedes campaign selection) or an unparsable cid
	// query parameter (parseCampaignID). A row with campaignID=nil is still
	// persisted (audit trail), never silently dropped.
	campaignID *int64
	bannerID   string
	zoneID                string
	decisionID            string
	modelVersion          string
	servedTier            string
	billable              bool
	blank                 bool
	destURL               string
	attributionDecisionID string
	occurredAt            time.Time
}

// Config controls PostgresSink construction.
type Config struct {
	// DSN is the Postgres connection string (TELEMETRY_PG_DSN env at the
	// collector's call site). Required.
	DSN string
	// QueueDepth is the size of the in-process, non-blocking event channel.
	// Default 8192 (matches internal/telemetry.Config.QueueDepth).
	QueueDepth int
	// WriteTimeout bounds the per-event BEGIN..COMMIT transaction. Default 3s.
	WriteTimeout time.Duration
	// Logger is the structured logger; nil = slog.Default().
	Logger *slog.Logger
}

// PostgresSink persists telemetry events to Postgres (stats.events_raw).
// It satisfies the collector's EventSink interface (services/collector/
// cmd/collector/main.go) structurally — same method set as
// internal/telemetry.Producer's Emit* methods, so no adapter is required at
// the call site.
type PostgresSink struct {
	pool         *pgxpool.Pool
	logger       *slog.Logger
	writeTimeout time.Duration

	queue chan pendingEvent
	wg    sync.WaitGroup

	// Observability counters (requirement: "drop-contabilizado no overflow").
	dropped          atomic.Uint64 // channel-full drops (hot-path backpressure)
	ipLiteralDropped atomic.Uint64 // TX-5/DA-11 privacyscan drops
	writeErrors      atomic.Uint64 // Postgres write failures (event lost, not a duplicate)
	ca6Clamped       atomic.Uint64 // defensive blank&&billable clamps (should stay 0)
	skippedNoTenant  atomic.Uint64 // ad_request sem tenant (caso NORMAL — ver EmitAdRequest)

	// logSkipOnce garante UM log para o descarte de ad_request sem tenant.
	// Sem isso seria um WARN por requisicao no evento mais frequente do funil.
	logSkipOnce sync.Once
}

// New opens the Postgres pool, verifies connectivity (fail fast at
// construction rather than silently dropping every event later — mirrors
// the requirement to log explicitly which sink is active), and starts the
// background drain worker.
func New(cfg Config) (*PostgresSink, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("pgsink: DSN is required")
	}
	if cfg.QueueDepth <= 0 {
		cfg.QueueDepth = 8192
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 3 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	pool, err := pgxpool.New(context.Background(), cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("pgsink: connect: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgsink: ping: %w", err)
	}

	s := &PostgresSink{
		pool:         pool,
		logger:       logger,
		writeTimeout: cfg.WriteTimeout,
		queue:        make(chan pendingEvent, cfg.QueueDepth),
	}

	s.wg.Add(1)
	go s.drainLoop()

	return s, nil
}

// Close drains the queue (best-effort: already-enqueued events are still
// written) and closes the Postgres pool.
func (s *PostgresSink) Close() {
	close(s.queue)
	s.wg.Wait()
	s.pool.Close()
}

// DroppedCount returns the number of events dropped due to a full queue
// (hot-path backpressure — the worker could not keep up).
func (s *PostgresSink) DroppedCount() uint64 { return s.dropped.Load() }

// IPLiteralDroppedCount returns the number of events dropped because
// internal/privacyscan found an IP-literal substring in a free-text field
// (TX-5/DA-11).
func (s *PostgresSink) IPLiteralDroppedCount() uint64 { return s.ipLiteralDropped.Load() }

// WriteErrorCount returns the number of events that failed to write to
// Postgres (connection error, timeout, etc. — the event is lost, not
// duplicated: ON CONFLICT only governs successful re-delivery).
func (s *PostgresSink) WriteErrorCount() uint64 { return s.writeErrors.Load() }

// CA6ClampedCount returns the number of times EmitImpression received a
// contradictory (blank=true, billable=true) pair from its caller and forced
// billable=false. This should always be zero in a healthy system — a
// non-zero value means the upstream blank.ComputeBlankBillable call site
// (services/collector/cmd/collector/main.go) regressed; see package doc
// "CA-6".
func (s *PostgresSink) CA6ClampedCount() uint64 { return s.ca6Clamped.Load() }

// SkippedNoTenantCount returns the number of ad_request events discarded
// because no tenant_id was available. This is the EXPECTED path for the
// canonical ad tag (`/asyncjs?zoneid=NNNN`, no `?tid=`) — a non-zero value is
// normal, not an incident. It exists so the gap is observable instead of
// silent, and so a reader can tell "requests = 0 because nothing was
// attributable" from "requests = 0 because nothing happened".
func (s *PostgresSink) SkippedNoTenantCount() uint64 { return s.skippedNoTenant.Load() }

// ---------------------------------------------------------------------------
// EventSink methods — signatures match services/collector/cmd/collector/
// main.go's EventSink interface exactly (structural typing, no adapter
// needed at the call site).
// ---------------------------------------------------------------------------

// EmitImpression persists an Impression event (lg endpoint — CA-6).
//
// billable/blank are NOT recomputed here — see package doc "CA-6". This
// method only defends against the two arriving contradictory.
func (s *PostgresSink) EmitImpression(
	tenantID, campaignID, bannerID, zoneID string,
	servedTier commonv1.ServedTier,
	billable, blank bool,
	decisionID, modelVersion string,
) {
	clamped, violated := clampBillable(blank, billable)
	if violated {
		s.ca6Clamped.Add(1)
		s.logger.Error("pgsink: CA-6 invariant violated upstream (blank && billable) — forcing billable=false",
			"tenant_id", tenantID, "campaign_id", campaignID, "decision_id", decisionID)
	}
	s.enqueue(pendingEvent{
		eventID:      telemetry.NewULID(),
		eventType:    eventTypeImpression,
		tenantID:     tenantID,
		campaignID:   parseCampaignID(campaignID),
		bannerID:     bannerID,
		zoneID:       zoneID,
		decisionID:   decisionID,
		modelVersion: modelVersion,
		servedTier:   servedTier.String(),
		billable:     clamped,
		blank:        blank,
		occurredAt:   time.Now().UTC(),
	})
}

// EmitClick persists a Click event (ck endpoint — CA-6).
func (s *PostgresSink) EmitClick(
	tenantID, campaignID, bannerID, zoneID, destURL string,
	decisionID, modelVersion string,
) {
	s.enqueue(pendingEvent{
		eventID:      telemetry.NewULID(),
		eventType:    eventTypeClick,
		tenantID:     tenantID,
		campaignID:   parseCampaignID(campaignID),
		bannerID:     bannerID,
		zoneID:       zoneID,
		decisionID:   decisionID,
		modelVersion: modelVersion,
		destURL:      destURL,
		occurredAt:   time.Now().UTC(),
	})
}

// EmitConversion persists a Conversion event (ct endpoint — CA-6, DA-8).
//
// conversion_value (Money) is deliberately NOT accepted/persisted by this
// sink's interface — TX-2 forbids float, and this table carries no monetary
// column at all (see db/stats/migrations/0001_stats_schema_up.sql header:
// "prefira NÃO ter dinheiro nesta tabela"). Money flows through
// internal/ledger, never through stats.
func (s *PostgresSink) EmitConversion(
	tenantID, campaignID, bannerID string,
	attributionDecisionID string,
	decisionID, modelVersion string,
) {
	s.enqueue(pendingEvent{
		eventID:               telemetry.NewULID(),
		eventType:             eventTypeConversion,
		tenantID:              tenantID,
		campaignID:            parseCampaignID(campaignID),
		bannerID:              bannerID,
		decisionID:            decisionID,
		modelVersion:          modelVersion,
		attributionDecisionID: attributionDecisionID,
		occurredAt:            time.Now().UTC(),
	})
}

// EmitAdRequest persists an AdRequest event (top-of-funnel, /asyncjs) as the
// stats.live_kpis "requests" KPI — CA-6's inventoryLoss = requests -
// impressions needs a real source for requests; without it a BFF reader has
// no honest number (see db/stats/migrations/0001_stats_schema_up.sql
// header "AdRequest / requests").
//
// DELIBERATELY NARROW persistence: only tenant_id, zone_id and decision_id
// are stored. geo, the coarse user-agent class, the sanitized referer,
// cachebuster and the publisher-supplied custom_vars map are DISCARDED
// here — none of them reach Postgres, and no new column exists for any of
// them (internal/privacyscan's own package doc singles out custom_vars as
// the primary risk vector for an embedded IP literal; the simplest, most
// auditable defence is to never store the map at all). siteID is also
// dropped: stats.events_raw has no site_id column, and adding one only to
// carry this single field was judged not worth the extra surface for what
// this sink exists to serve.
//
// campaign_id is always NULL for this event type: AdRequest precedes the
// decision that picks a campaign (services/collector/cmd/collector/
// main.go:handleAsyncJS runs before any campaign is chosen) — there is
// nothing to parse or attribute here, unlike EmitImpression/EmitClick/
// EmitConversion (see parseCampaignID).
// TENANT DESCONHECIDO — o caso NORMAL, não excepcional. handleAsyncJS lê o
// tenant de `?tid=` na query (main.go:165), e a ad tag canônica que o próprio
// collector gera é `<script src=".../asyncjs?zoneid=NNNN">` — sem `tid`. O
// tenant só é resolvido server-side pelo decision, DEPOIS, a partir da zona
// (CA-1: tenant nunca vem do cliente). Logo, na esmagadora maioria dos ad
// requests, tenantID == "".
//
// stats.events_raw.tenant_id é NOT NULL uuid sob FORCE RLS: tentar inserir ""
// falha com SQLSTATE 22P02 ("invalid input syntax for type uuid"). Medido de
// 1ª mão: um GET /asyncjs?zoneid=1001 produzia exatamente esse erro, e um WARN
// POR REQUISIÇÃO — no volume de ad request (ordens de magnitude acima de
// impressão) isso é uma enxurrada de log garantida.
//
// Descartamos o evento cedo, contabilizado em SkippedNoTenantCount, e logamos
// UMA vez. Consequência honesta e assumida: `requests` em stats.live_kpis fica
// 0 para publishers cuja tag não carrega `tid` — que é por que o BFF devolve
// `null` (não atribuível) em requests/inventoryLoss em vez de um número. Uma
// tag que inclua `tid` passa a alimentar o KPI sem mudança alguma aqui.
func (s *PostgresSink) EmitAdRequest(
	tenantID, zoneID, _ string,
	_ *commonv1.Geo,
	_, _, _ string,
	_ map[string]string,
	decisionID string,
) {
	if tenantID == "" {
		s.skippedNoTenant.Add(1)
		s.logSkipOnce.Do(func() {
			s.logger.Info("pgsink: ad_request sem tenant_id — descartado (a ad tag canônica não carrega ?tid=; "+
				"o tenant é resolvido server-side pelo decision, depois). requests fica 0 nesta fonte; "+
				"o BFF reporta null (não atribuível). Log emitido uma única vez.",
				"zone_id", zoneID)
		})
		return
	}
	s.enqueue(pendingEvent{
		eventID:    telemetry.NewULID(),
		eventType:  eventTypeAdRequest,
		tenantID:   tenantID,
		zoneID:     zoneID,
		decisionID: decisionID,
		occurredAt: time.Now().UTC(),
	})
}

// ---------------------------------------------------------------------------
// clampBillable — CA-6 defensive clamp (pure, unit-testable without a DB).
// ---------------------------------------------------------------------------

// clampBillable enforces "a blank impression is never billable" as a pure
// clamp over an ALREADY-computed (blank, billable) pair. It does not
// recompute the decision from servedTier/bannerID — that logic lives
// exclusively in internal/telemetry/blank.ComputeBlankBillable. violated is
// true only when the caller handed this function a contradictory pair,
// which should never happen in a correctly wired system (see
// PostgresSink.CA6ClampedCount).
func clampBillable(blank, billable bool) (clamped bool, violated bool) {
	if blank && billable {
		return false, true
	}
	return billable, false
}

// ---------------------------------------------------------------------------
// parseCampaignID — string (query param) -> *int64 (BIGINT column).
// ---------------------------------------------------------------------------

// parseCampaignID converts the collector's "cid" query parameter into the
// *int64 stats.events_raw.campaign_id expects (see db/stats/migrations/
// 0001_stats_schema_up.sql header "TIPOS": campaign_id is BIGINT, matching
// config.campaigns.id, so the BFF's JOIN against config.campaigns works).
// Returns nil — never an error — for an empty or non-numeric input: the
// event is still persisted (audit trail) with campaign_id NULL rather than
// being dropped over a malformed/adversarial query parameter. This mirrors
// the collector's existing tolerance elsewhere (e.g. handleImpression's
// unmatched "tier" falls back to SERVED_TIER_UNSPECIFIED instead of
// rejecting the request).
func parseCampaignID(s string) *int64 {
	if s == "" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// ---------------------------------------------------------------------------
// enqueue — non-blocking hand-off to the background worker.
// ---------------------------------------------------------------------------

// enqueue never blocks: on a full queue the event is dropped and counted
// (DroppedCount), never allowed to stall the HTTP hot path (/lg, /ck, /ct).
func (s *PostgresSink) enqueue(ev pendingEvent) {
	select {
	case s.queue <- ev:
	default:
		s.dropped.Add(1)
		s.logger.Warn("pgsink: queue full; event dropped",
			"event_type", ev.eventType, "dropped_total", s.dropped.Load())
	}
}

// freeTextBlob concatenates every attacker-reachable free-text field of an
// event, NUL-separated (a byte that cannot appear in these fields — they are
// URL query values — so no field boundary can accidentally form a spurious
// IP-literal match across two fields). Passed whole to
// internal/privacyscan.ContainsIPLiteral before every write — see package
// doc "Privacy".
//
// campaignID is deliberately NOT included: it is now a typed *int64 (see
// parseCampaignID), not a string — either nil or a value that has already
// been proven to parse as a plain decimal integer, which cannot contain the
// '.'/':' characters an IP literal requires. The type itself closes that
// vector; scanning it would be a no-op.
func freeTextBlob(ev pendingEvent) []byte {
	return []byte(strings.Join([]string{
		ev.tenantID, ev.bannerID, ev.zoneID,
		ev.decisionID, ev.modelVersion, ev.destURL, ev.attributionDecisionID,
	}, "\x00"))
}

// ---------------------------------------------------------------------------
// Background worker — the ONLY goroutine performing Postgres I/O.
// ---------------------------------------------------------------------------

func (s *PostgresSink) drainLoop() {
	defer s.wg.Done()
	for ev := range s.queue {
		s.write(ev)
	}
}

// write persists a single event in its own transaction: BEGIN; scope the
// RLS tenant GUC; INSERT ... ON CONFLICT DO NOTHING; COMMIT. See package doc
// "Multi-tenant RLS on the write path" for why this is one transaction per
// event rather than a batch.
func (s *PostgresSink) write(ev pendingEvent) {
	if privacyscan.ContainsIPLiteral(freeTextBlob(ev)) {
		s.ipLiteralDropped.Add(1)
		s.logger.Error("pgsink: TX-5/DA-11 — event dropped, IP literal found in free-text field",
			"event_type", ev.eventType, "event_id", ev.eventID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.writeTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		s.writeErrors.Add(1)
		s.logger.Warn("pgsink: begin tx failed", "err", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort; no-op after Commit

	if _, err := tx.Exec(ctx, setTenantGUCSQL, ev.tenantID); err != nil {
		s.writeErrors.Add(1)
		s.logger.Warn("pgsink: set tenant GUC failed", "err", err)
		return
	}

	if _, err := tx.Exec(ctx, insertEventSQL,
		ev.eventID, ev.eventType, ev.tenantID, ev.campaignID, ev.bannerID, ev.zoneID,
		ev.decisionID, ev.modelVersion, ev.servedTier, ev.billable, ev.blank,
		ev.destURL, ev.attributionDecisionID, ev.occurredAt,
	); err != nil {
		s.writeErrors.Add(1)
		s.logger.Warn("pgsink: insert failed", "event_type", ev.eventType, "event_id", ev.eventID, "err", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		s.writeErrors.Add(1)
		s.logger.Warn("pgsink: commit failed", "err", err)
		return
	}
}
