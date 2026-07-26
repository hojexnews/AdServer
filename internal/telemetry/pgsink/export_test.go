// export_test.go exposes internal pgsink types/functions for the black-box
// _test package. Compiled ONLY during tests. Mirrors the EXACT convention
// already established in internal/telemetry/export_test.go (WALRecord alias,
// NewProducerForTest) — same repo, same pattern, not invented here.
package pgsink

import "time"

// PendingEvent is the exported alias of the internal pendingEvent type, for
// white-box-via-export integration tests (pgsink_integration_test.go) that
// need to call write() directly with a FIXED event_id — something the
// public Emit* methods cannot do, since each mints a fresh ULID per call by
// design (see package doc "Idempotency"). Proving `ON CONFLICT (event_id)
// DO NOTHING` requires writing the SAME event_id twice.
type PendingEvent = pendingEvent

// NewPendingEventForTest builds a PendingEvent with every field settable.
func NewPendingEventForTest(
	eventID, eventType, tenantID string,
	campaignID *int64,
	bannerID, zoneID, decisionID, modelVersion, servedTier string,
	billable, blank bool,
	destURL, attributionDecisionID string,
	occurredAt time.Time,
) PendingEvent {
	return pendingEvent{
		eventID:               eventID,
		eventType:             eventType,
		tenantID:              tenantID,
		campaignID:            campaignID,
		bannerID:              bannerID,
		zoneID:                zoneID,
		decisionID:            decisionID,
		modelVersion:          modelVersion,
		servedTier:            servedTier,
		billable:              billable,
		blank:                 blank,
		destURL:               destURL,
		attributionDecisionID: attributionDecisionID,
		occurredAt:            occurredAt,
	}
}

// WriteForTest calls the PRODUCTION write() method directly — the exact
// same code path the background drainLoop uses (BEGIN; scope the RLS tenant
// GUC; INSERT ... ON CONFLICT DO NOTHING; COMMIT) — so integration tests
// exercise real production logic, never a reimplementation.
func (s *PostgresSink) WriteForTest(ev PendingEvent) { s.write(ev) }
