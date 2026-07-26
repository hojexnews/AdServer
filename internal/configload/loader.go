package configload

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hojex/adserver/internal/snapshot"
)

// PostgresLoader implements snapshot.Loader by reading db/config from Postgres.
//
// It is read-only.  Because the config tables use FORCE ROW LEVEL SECURITY and
// the snapshot is intentionally cross-tenant (see package doc), the DSN must
// authenticate as a role holding BYPASSRLS (adserver_loader) — otherwise every
// query returns zero rows and the snapshot is empty.
type PostgresLoader struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPostgresLoader opens a connection pool and verifies connectivity.
func NewPostgresLoader(ctx context.Context, dsn string, logger *slog.Logger) (*PostgresLoader, error) {
	if logger == nil {
		logger = slog.Default()
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("configload: parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("configload: open pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("configload: ping: %w", err)
	}
	return &PostgresLoader{pool: pool, logger: logger}, nil
}

// Close releases the connection pool.
func (l *PostgresLoader) Close() {
	if l.pool != nil {
		l.pool.Close()
	}
}

// Load reads the full config and assembles an immutable Snapshot.
// It never returns a nil snapshot without a non-nil error (snapshot.Loader
// contract).
func (l *PostgresLoader) Load(ctx context.Context) (*snapshot.Snapshot, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	scales := l.loadAssetScales(queryCtx)

	raw := RawConfig{}
	var err error
	if raw.Zones, err = l.loadZones(queryCtx); err != nil {
		return nil, err
	}
	if raw.Campaigns, err = l.loadCampaigns(queryCtx, scales); err != nil {
		return nil, err
	}
	if raw.Banners, err = l.loadBanners(queryCtx); err != nil {
		return nil, err
	}
	if raw.Caps, err = l.loadCaps(queryCtx); err != nil {
		return nil, err
	}
	if raw.Rules, err = l.loadRules(queryCtx); err != nil {
		return nil, err
	}
	if raw.CampaignZones, err = l.loadCampaignZones(queryCtx); err != nil {
		return nil, err
	}
	raw.Delivered = l.loadDelivered(queryCtx)

	version := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	snap := Assemble(raw, version, time.Now().UTC())
	l.logger.Info("configload: snapshot built",
		"version", version,
		"zones", len(snap.Zones),
		"campaigns", len(snap.Campaigns),
		"banners", len(snap.Banners),
		"rule_sets", len(snap.RuleSets))
	return snap, nil
}

// loadAssetScales returns currency code → scale.  A missing/disabled Asset
// Registry is non-fatal: callers default to scale 2 (fiat minor units).
func (l *PostgresLoader) loadAssetScales(ctx context.Context) map[string]int32 {
	out := map[string]int32{}
	rows, err := l.pool.Query(ctx,
		`SELECT code, COALESCE(scale, 2)::int FROM asset_registry.assets`)
	if err != nil {
		l.logger.Warn("configload: asset_registry unavailable; defaulting scale=2", "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var scale int32
		if err := rows.Scan(&code, &scale); err != nil {
			l.logger.Warn("configload: scan asset scale", "err", err)
			continue
		}
		out[code] = scale
	}
	return out
}

// loadDelivered aggregates delivered counts per campaign from the perfil
// BETA Postgres telemetry sink (stats.events_raw — ADR-0005,
// db/stats/migrations/0001_stats_schema_up.sql; written by
// internal/telemetry/pgsink). See DeliveredRow's doc for what each column
// means and how it is matched onto campaigns in Assemble.
//
// Cost (DA-4/TX-4): this runs ONCE per snapshot build — i.e. on
// SNAPSHOT_REFRESH_INTERVAL (default 30s), never per decision request. It
// is a single aggregate query: one pass over stats.events_raw, filtered to
// campaign_id IS NOT NULL and grouped by campaign_id.
// events_raw_tenant_campaign_type_idx (tenant_id, campaign_id, event_type)
// covers the campaign_id/event_type access; the billable filter on
// impressions is not covered by that index, so rows still need a heap
// fetch for that check — acceptable at beta volume (this is explicitly a
// bridge schema, not the ClickHouse destination — see the migration's
// header), not a hot-path cost.
//
// Degradation (DA-6/DA-9 — silence, never a hot-path failure): the stats
// schema may not exist at all (a deployment without the Postgres telemetry
// sink wired, or an older database predating it), or this loader's role
// (adserver_loader) may not yet hold a GRANT on it. EITHER failure mode
// degrades to a nil/empty result with a WARN log — exactly the same
// non-fatal pattern as loadAssetScales above — and NEVER fails Load() or
// the snapshot build. Every snapshot.Campaign.Delivered* field then simply
// stays at its zero value, identical to this package's behavior before
// this method existed. Consequence documented for the caller: DA-4 pacing
// (computeDeficit) reads a permanent 1.0 deficit for every campaign until
// this degrades to zero rows, that pacing behavior is UNCHANGED by this
// method — this method only supplies the input that was previously always
// absent.
func (l *PostgresLoader) loadDelivered(ctx context.Context) []DeliveredRow {
	rows, err := l.pool.Query(ctx, `
		SELECT campaign_id::text,
		       COUNT(*) FILTER (WHERE event_type = 'impression' AND billable),
		       COUNT(*) FILTER (WHERE event_type = 'click'),
		       COUNT(*) FILTER (WHERE event_type = 'conversion')
		FROM   stats.events_raw
		WHERE  campaign_id IS NOT NULL
		GROUP BY campaign_id`)
	if err != nil {
		l.logger.Warn("configload: stats.events_raw unavailable; delivered counts default to 0 "+
			"(DA-4 pacing degrades to its existing permanent-deficit behavior — non-fatal, "+
			"snapshot build proceeds)", "err", err)
		return nil
	}
	defer rows.Close()
	var out []DeliveredRow
	for rows.Next() {
		var d DeliveredRow
		if err := rows.Scan(&d.CampaignID, &d.Impressions, &d.Clicks, &d.Conversions); err != nil {
			l.logger.Warn("configload: scan delivered row", "err", err)
			continue
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		l.logger.Warn("configload: iterate delivered rows", "err", err)
	}
	return out
}

func (l *PostgresLoader) loadZones(ctx context.Context) ([]ZoneRow, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT id::text, tenant_id::text, site_id::text, name, width, height, active
		FROM   config.zones`)
	if err != nil {
		return nil, fmt.Errorf("configload: query zones: %w", err)
	}
	defer rows.Close()
	var out []ZoneRow
	for rows.Next() {
		var z ZoneRow
		if err := rows.Scan(&z.ID, &z.TenantID, &z.SiteID, &z.Name, &z.Width, &z.Height, &z.Active); err != nil {
			return nil, fmt.Errorf("configload: scan zone: %w", err)
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

func (l *PostgresLoader) loadCampaigns(ctx context.Context, scales map[string]int32) ([]CampaignRow, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT id::text, tenant_id::text, type::text, priority, pricing_model::text,
		       currency, rate::text, goal_target, goal_metric::text, start_at, end_at, active
		FROM   config.campaigns`)
	if err != nil {
		return nil, fmt.Errorf("configload: query campaigns: %w", err)
	}
	defer rows.Close()
	var out []CampaignRow
	for rows.Next() {
		var (
			c          CampaignRow
			rateText   string
			goalTarget *int64
			goalMetric *string
		)
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Type, &c.Priority, &c.PricingModel,
			&c.Currency, &rateText, &goalTarget, &goalMetric, &c.StartAt, &c.EndAt, &c.Active); err != nil {
			return nil, fmt.Errorf("configload: scan campaign: %w", err)
		}
		scale, ok := scales[c.Currency]
		if !ok {
			scale = 2
		}
		c.Scale = scale
		minor, err := decimalToMinor(rateText, scale)
		if err != nil {
			l.logger.Warn("configload: bad campaign rate; using 0",
				"campaign", c.ID, "rate", rateText, "err", err)
			minor = 0
		}
		c.RateMinor = minor
		if goalTarget != nil {
			c.GoalTarget = *goalTarget
			c.GoalValid = true
		}
		if goalMetric != nil {
			c.GoalMetric = *goalMetric
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (l *PostgresLoader) loadBanners(ctx context.Context) ([]BannerRow, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT id::text, tenant_id::text, campaign_id::text, creative_type::text,
		       COALESCE(asset_url, ''),
		       CASE WHEN asset_blob IS NOT NULL THEN convert_from(asset_blob, 'UTF8') ELSE '' END,
		       COALESCE(dest_url, ''),
		       width, height, active
		FROM   config.banners`)
	if err != nil {
		return nil, fmt.Errorf("configload: query banners: %w", err)
	}
	defer rows.Close()
	var out []BannerRow
	for rows.Next() {
		var b BannerRow
		if err := rows.Scan(&b.ID, &b.TenantID, &b.CampaignID, &b.CreativeType,
			&b.AssetURL, &b.AssetBlob, &b.DestURL, &b.Width, &b.Height, &b.Active); err != nil {
			return nil, fmt.Errorf("configload: scan banner: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (l *PostgresLoader) loadCaps(ctx context.Context) ([]CapRow, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT owner_type::text, owner_id::text, scope::text, limit_count, reset_interval, active
		FROM   config.caps`)
	if err != nil {
		return nil, fmt.Errorf("configload: query caps: %w", err)
	}
	defer rows.Close()
	var out []CapRow
	for rows.Next() {
		var (
			c     CapRow
			reset *int32
		)
		if err := rows.Scan(&c.OwnerType, &c.OwnerID, &c.Scope, &c.LimitCount, &reset, &c.Active); err != nil {
			return nil, fmt.Errorf("configload: scan cap: %w", err)
		}
		if reset != nil {
			c.ResetInterval = *reset
			c.ResetValid = true
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (l *PostgresLoader) loadRules(ctx context.Context) ([]RuleRow, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT COALESCE(owner_type::text, ''), COALESCE(owner_id::text, ''),
		       vector, operator, value, logical_op::text,
		       COALESCE(rule_set_id::text, ''), rule_set_id IS NOT NULL, active
		FROM   config.delivery_rules`)
	if err != nil {
		return nil, fmt.Errorf("configload: query rules: %w", err)
	}
	defer rows.Close()
	var out []RuleRow
	for rows.Next() {
		var r RuleRow
		if err := rows.Scan(&r.OwnerType, &r.OwnerID, &r.Vector, &r.Operator,
			&r.Value, &r.LogicalOp, &r.RuleSetID, &r.RuleSetHeld, &r.Active); err != nil {
			return nil, fmt.Errorf("configload: scan rule: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (l *PostgresLoader) loadCampaignZones(ctx context.Context) ([]CampaignZoneRow, error) {
	rows, err := l.pool.Query(ctx,
		`SELECT campaign_id::text, zone_id::text FROM config.campaign_zones`)
	if err != nil {
		return nil, fmt.Errorf("configload: query campaign_zones: %w", err)
	}
	defer rows.Close()
	var out []CampaignZoneRow
	for rows.Next() {
		var cz CampaignZoneRow
		if err := rows.Scan(&cz.CampaignID, &cz.ZoneID); err != nil {
			return nil, fmt.Errorf("configload: scan campaign_zone: %w", err)
		}
		out = append(out, cz)
	}
	return out, rows.Err()
}

// compile-time assertion: PostgresLoader satisfies snapshot.Loader.
var _ snapshot.Loader = (*PostgresLoader)(nil)
