//go:build integration

// Package pgsink_test — integration tests (perfil BETA onda, mirroring
// internal/ledger/posting_integration_test.go as the model this file was
// asked to follow) that exercise pgsink.PostgresSink against a REAL
// Postgres 16, not a reimplementation and not a mock.
//
// TWO DISTINCT CONCERNS, TWO DISTINCT PRIVILEGE LEVELS (same split as
// internal/ledger/posting_integration_test.go's own header comment):
//
//   - Application correctness (idempotency, CA-6 DB-level defence, column
//     mapping): exercised via pgsink.New(Config{DSN: DATABASE_URL}) and
//     PostgresSink.WriteForTest — the SAME production write() method the
//     background worker calls. Runs as whatever role DATABASE_URL is
//     (superuser in CI), which is fine for this half: RLS doesn't apply to
//     superusers/BYPASSRLS roles regardless, so it cannot mask an
//     idempotency or CHECK-constraint bug.
//
//   - RLS isolation on the write path (TX-3): a superuser would bypass FORCE
//     ROW LEVEL SECURITY, so this half opens its OWN raw pgx connection and
//     `SET ROLE adserver_stats_writer` (idempotent role created inline by
//     db/stats/migrations/0001_stats_schema_up.sql) — mirroring the
//     established `SET ROLE adserver_app` idiom already used by
//     db/config/tests/rls_isolation_test.sql. No db/stats/tests/*.sql file
//     exists (out of this package's file-ownership scope for this wave) —
//     this Go test is the RLS proof instead.
//
// HOW TO RUN
//
//	export DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable
//	# with db/stats/migrations/0001_stats_schema_up.sql already applied
//	go test -tags integration -count=1 -race ./internal/telemetry/pgsink/...
//
// Or via `make go-test-integration` (escopo derivado por
// `//go:build integration` — este arquivo entra automaticamente, sem editar
// make/go.mk; ver seu comentário de topo). In CI: .github/workflows/db.yml
// applies db/stats/migrations/0001 before running this package.
package pgsink_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hojex/adserver/internal/telemetry/pgsink"
)

// integrationDSN reads DATABASE_URL. Fails LOUD (t.Fatal, never t.Skip) if
// absent — a `//go:build integration` test that quietly no-ops without live
// infra is exactly the "no-op mascarado de validacao real" make/go.mk's
// go-test-integration exists to prevent. Same idiom as internal/ledger/
// posting_integration_test.go's integrationPool.
func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Fatal("DATABASE_URL nao definido — este teste (//go:build integration) exige um " +
			"Postgres 16 real com db/stats/migrations/0001_stats_schema_up.sql aplicada. " +
			"export DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable")
	}
	return dsn
}

// newTestLogger returns a slog.Logger that discards output — tests assert
// on exported counters (WriteErrorCount etc.), not log lines, and the
// CA-6/RLS-rejection tests deliberately trigger Postgres errors that would
// otherwise be noisy at the default Warn/Error level.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// itPool opens an INDEPENDENT connection pool against the same DATABASE_URL
// for test-side assertions/setup — separate from the pool pgsink.New opens
// internally for the sink under test. Two pools against the same database
// is the standard split between "system under test" and "test assertions".
func itPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New(%q): %v", dsn, err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool.Ping: %v (Postgres real acessivel em DATABASE_URL, "+
			"db/stats/migrations/0001_stats_schema_up.sql aplicada?)", err)
	}
	return pool
}

func itCountByEventID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stats.events_raw WHERE event_id = $1`, eventID).Scan(&n); err != nil {
		t.Fatalf("contar events_raw por event_id: %v", err)
	}
	return n
}

func itDeleteByEventIDPrefix(t *testing.T, ctx context.Context, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	// Cleanup runs as whatever DATABASE_URL's role is (superuser in CI,
	// bypasses RLS/FORCE — fine, this is teardown, not the thing under test).
	if _, err := pool.Exec(ctx, `DELETE FROM stats.events_raw WHERE event_id LIKE $1`, prefix+"%"); err != nil {
		t.Logf("cleanup DELETE (non-fatal): %v", err)
	}
}

// ---------------------------------------------------------------------------
// (a) Idempotencia REAL: ON CONFLICT (event_id) DO NOTHING via write()
// ---------------------------------------------------------------------------

func TestIntegration_Write_IdempotentOnDuplicateEventID(t *testing.T) {
	dsn := integrationDSN(t)
	ctx := context.Background()
	pool := itPool(t, dsn)

	sink, err := pgsink.New(pgsink.Config{DSN: dsn, Logger: newTestLogger()})
	if err != nil {
		t.Fatalf("pgsink.New: %v", err)
	}
	defer sink.Close()

	eventID := "it-idem-" + uniqueSuffix(t)
	t.Cleanup(func() { itDeleteByEventIDPrefix(t, context.Background(), pool, eventID) })

	tenant := "aaaaaaaa-0000-0000-0000-0000000000aa"
	campaignID := int64(1001)
	ev := pgsink.NewPendingEventForTest(
		eventID, "impression", tenant, &campaignID,
		"ban-1", "zone-1", "decision-1", "", "SERVED_TIER_REMNANT",
		true, false, "", "", time.Now().UTC(),
	)

	// Calls the REAL production write() method (via WriteForTest) TWICE with
	// the IDENTICAL event_id — something the public Emit* methods cannot do
	// (each mints a fresh ULID per call by design). This is the SQL-level
	// proof of `ON CONFLICT (event_id) DO NOTHING` (events_raw_pk).
	sink.WriteForTest(ev)
	sink.WriteForTest(ev)

	if got := itCountByEventID(t, ctx, pool, eventID); got != 1 {
		t.Fatalf("esperava exatamente 1 linha apos 2 write() com o MESMO event_id, encontrou %d", got)
	}
	if got := sink.WriteErrorCount(); got != 0 {
		t.Fatalf("WriteErrorCount() = %d, want 0 (ON CONFLICT DO NOTHING nao e um erro)", got)
	}
}

// ---------------------------------------------------------------------------
// (b) CA-6, segunda linha de defesa NO BANCO: events_raw_ca6_chk rejeita
//     blank=true AND billable=true mesmo escrevendo via write() diretamente
//     (contornando o clamp de EmitImpression) — prova que a garantia não
//     depende só do código Go.
// ---------------------------------------------------------------------------

func TestIntegration_Write_CA6CheckConstraintRejectsBlankAndBillable(t *testing.T) {
	dsn := integrationDSN(t)
	ctx := context.Background()
	pool := itPool(t, dsn)

	sink, err := pgsink.New(pgsink.Config{DSN: dsn, Logger: newTestLogger()})
	if err != nil {
		t.Fatalf("pgsink.New: %v", err)
	}
	defer sink.Close()

	eventID := "it-ca6-" + uniqueSuffix(t)
	t.Cleanup(func() { itDeleteByEventIDPrefix(t, context.Background(), pool, eventID) })

	tenant := "aaaaaaaa-0000-0000-0000-0000000000aa"
	campaignID := int64(1001)
	// blank=true AND billable=true: a contradictory pair that EmitImpression's
	// clampBillable would normally never let through. WriteForTest bypasses
	// that Go-level clamp entirely to prove the SEPARATE database-level
	// defence (events_raw_ca6_chk) also holds.
	ev := pgsink.NewPendingEventForTest(
		eventID, "impression", tenant, &campaignID,
		"ban-1", "zone-1", "decision-1", "", "SERVED_TIER_BLANK",
		true, true, "", "", time.Now().UTC(),
	)

	before := sink.WriteErrorCount()
	sink.WriteForTest(ev)

	if got := itCountByEventID(t, ctx, pool, eventID); got != 0 {
		t.Fatalf("events_raw_ca6_chk deveria ter rejeitado a linha (blank=true billable=true); encontrou %d linha(s)", got)
	}
	if got := sink.WriteErrorCount(); got != before+1 {
		t.Fatalf("WriteErrorCount() = %d, want %d (o CHECK constraint deveria ter sido contado como erro de escrita)", got, before+1)
	}
}

// ---------------------------------------------------------------------------
// (c) requests aparece em stats.live_kpis com campaign_id NULL para
//     event_type='ad_request' (nunca fundido com uma linha de campanha).
// ---------------------------------------------------------------------------

func TestIntegration_Write_AdRequestLandsOnNullCampaignRowInLiveKpis(t *testing.T) {
	dsn := integrationDSN(t)
	ctx := context.Background()
	pool := itPool(t, dsn)

	sink, err := pgsink.New(pgsink.Config{DSN: dsn, Logger: newTestLogger()})
	if err != nil {
		t.Fatalf("pgsink.New: %v", err)
	}

	// tenant is randomised (not just the event_id) so the cleanup below can
	// target this test's row precisely: EmitAdRequest mints its OWN ULID
	// internally (the public API cannot be handed a fixed event_id — see
	// package doc "Idempotency"), so an event_id-prefix cleanup (used by the
	// other tests in this file, which write via WriteForTest with a chosen
	// id) cannot reach it.
	tenant := "cccccccc-0000-0000-0000-" + uniqueSuffix(t)[:12]
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM stats.events_raw WHERE tenant_id = $1::uuid`, tenant); err != nil {
			t.Logf("cleanup DELETE (non-fatal): %v", err)
		}
	})

	sink.EmitAdRequest(tenant, "zone-1", "site-1", nil, "", "", "", nil, "")
	// Close() drains the queue (waits for the background worker to finish
	// writing everything already enqueued) before returning — see package
	// doc "Hot-path / durability model". No separate `defer sink.Close()`:
	// calling Close twice panics (close of an already-closed channel), and
	// the whole point here is to synchronize on the drain before querying.
	sink.Close()

	var requests, impressions int64
	var campaignIsNull bool
	err = pool.QueryRow(ctx,
		`SELECT requests, impressions, campaign_id IS NULL
		   FROM stats.live_kpis
		  WHERE tenant_id = $1::uuid AND campaign_id IS NULL`,
		tenant,
	).Scan(&requests, &impressions, &campaignIsNull)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("esperava uma linha (tenant, campaign_id IS NULL) em stats.live_kpis apos EmitAdRequest — nao encontrada")
	}
	if err != nil {
		t.Fatalf("consultar stats.live_kpis: %v", err)
	}
	if !campaignIsNull {
		t.Fatal("campaign_id deveria ser NULL para a linha de ad_request")
	}
	if requests < 1 {
		t.Fatalf("requests = %d, want >= 1", requests)
	}
	if impressions != 0 {
		t.Fatalf("impressions = %d, want 0 (nenhuma impressao emitida nesta linha de teste)", impressions)
	}
}

// ---------------------------------------------------------------------------
// (d) RLS WITH CHECK, role real (nao superuser): grava OK dentro do proprio
//     tenant, REJEITA cross-tenant. Mesmo idioma de SET ROLE de
//     db/config/tests/rls_isolation_test.sql.
// ---------------------------------------------------------------------------

func TestIntegration_RLS_StatsWriterRoleRejectsCrossTenant(t *testing.T) {
	dsn := integrationDSN(t)
	ctx := context.Background()
	pool := itPool(t, dsn)

	tenantA := "aaaaaaaa-0000-0000-0000-0000000000a1"
	tenantB := "bbbbbbbb-0000-0000-0000-0000000000b1"
	eventOK := "it-rls-ok-" + uniqueSuffix(t)
	eventReject := "it-rls-rej-" + uniqueSuffix(t)
	t.Cleanup(func() {
		ctx := context.Background()
		itDeleteByEventIDPrefix(t, ctx, pool, "it-rls-ok-")
		itDeleteByEventIDPrefix(t, ctx, pool, "it-rls-rej-")
	})

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("pool.Acquire: %v", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// adserver_stats_writer is created idempotently by db/stats/migrations/
	// 0001_stats_schema_up.sql itself — no dependency on db/seed/dev_roles.sql.
	if _, err := tx.Exec(ctx, "SET ROLE adserver_stats_writer"); err != nil {
		t.Fatalf(`SET ROLE adserver_stats_writer: %v (a migration 0001_stats_schema_up.sql criou o role?)`, err)
	}
	// set_config(..., true), not a literal `SET LOCAL adserver.tenant_id = $1`:
	// Postgres' SET command does not accept bind parameters at all (syntax
	// error) — set_config() is the parameterizable equivalent and is the
	// EXACT statement production write() uses (setTenantGUCSQL in sink.go).
	if _, err := tx.Exec(ctx, "SELECT set_config('adserver.tenant_id', $1, true)", tenantA); err != nil {
		t.Fatalf("set_config(adserver.tenant_id): %v", err)
	}

	// Matching tenant: WITH CHECK must ACCEPT.
	if _, err := tx.Exec(ctx,
		`INSERT INTO stats.events_raw (event_id, event_type, tenant_id, occurred_at)
		 VALUES ($1, 'ad_request', $2::uuid, now())`,
		eventOK, tenantA,
	); err != nil {
		t.Fatalf("INSERT com tenant_id correspondente ao GUC deveria ter sido aceito: %v", err)
	}

	// Cross-tenant: WITH CHECK must REJECT (42501).
	_, err = tx.Exec(ctx,
		`INSERT INTO stats.events_raw (event_id, event_type, tenant_id, occurred_at)
		 VALUES ($1, 'ad_request', $2::uuid, now())`,
		eventReject, tenantB,
	)
	if err == nil {
		t.Fatal("INSERT cross-tenant deveria ter sido REJEITADO pela policy WITH CHECK, mas foi aceito — " +
			"regressao de TX-3 (RLS)")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("esperava um erro do Postgres (row-level security), got: %v", err)
	}
	if pgErr.Code != "42501" {
		t.Fatalf("esperava SQLSTATE 42501 (insufficient_privilege / RLS), got %q: %v", pgErr.Code, err)
	}

	// Rollback (deferred) discards eventOK too — cleanup via prefix above is
	// belt-and-suspenders in case a future edit changes this to Commit.
}

// uniqueSuffix mirrors internal/ledger/posting_integration_test.go's own
// helper: crypto/rand, not a timestamp, so reruns against a persistent
// Postgres never collide on event_id even at high call rates within the
// same test process.
func uniqueSuffix(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("crypto/rand: %v", err)
	}
	return hex.EncodeToString(buf)
}
