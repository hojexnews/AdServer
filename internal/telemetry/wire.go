package telemetry

import (
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	decisionv1 "github.com/hojex/adserver/gen/go/adserver/decision/v1"
	telemetryv1 "github.com/hojex/adserver/gen/go/adserver/telemetry/v1"
)

// ---------------------------------------------------------------------------
// Wire format — Protobuf (production) or JSONEachRow (local dev / ClickHouse)
// ---------------------------------------------------------------------------
//
// Production emits binary Protobuf (the canonical contract, TX-1).  The local
// dev stack runs the ClickHouse Kafka engine with kafka_format='JSONEachRow'
// (see data/clickhouse/migrations/001) because it needs no schema-registry
// wiring; for that path the producer emits flat JSON whose field names match
// the kafka_* engine columns EXACTLY.  Switching the format is a deployment
// concern, never a contract change — the .proto remains the source of truth.
//
// PRIVACY (TX-5/DA-11): the AdRequest JSON carries `user_agent` = the coarse
// device/browser CLASS produced server-side (e.g. "mobile/chrome"), never the
// raw UA.  ClickHouse projects this column to `user_agent_class` in the raw
// table (003 MV).  No IP, no PII ever appears on the wire.

// WireFormat selects the serialization used for Redpanda records.
type WireFormat int

const (
	// WireProtobuf is the canonical binary contract (default, production).
	WireProtobuf WireFormat = iota
	// WireJSON emits flat JSON matching the ClickHouse kafka_* columns
	// (kafka_format='JSONEachRow'); used by the local dev stack.
	WireJSON
)

// ParseWireFormat maps an env value to a WireFormat (default: protobuf).
func ParseWireFormat(s string) WireFormat {
	switch s {
	case "json", "JSON", "jsoneachrow", "JSONEachRow":
		return WireJSON
	default:
		return WireProtobuf
	}
}

// chTimeFormat is the ClickHouse DateTime64(3,'UTC') string layout.
const chTimeFormat = "2006-01-02 15:04:05.000"

// marshalWire serialises msg in the configured wire format.
func marshalWire(format WireFormat, msg proto.Message) ([]byte, error) {
	if format == WireJSON {
		return marshalJSON(msg)
	}
	return proto.Marshal(msg)
}

// marshalJSON builds a flat JSON object whose keys match the kafka_* engine
// column names (data/clickhouse/migrations/001) for the message's topic.
func marshalJSON(msg proto.Message) ([]byte, error) {
	switch m := msg.(type) {
	case *telemetryv1.AdRequest:
		row := envelopeFields(m.GetEnvelope())
		g := m.GetGeo()
		row["zone_id"] = m.GetZoneId()
		row["site_id"] = m.GetSiteId()
		row["geo_country"] = g.GetCountry()
		row["geo_city"] = g.GetCity()
		// `user_agent` carries the coarse class (never the raw UA) — the kafka
		// engine column is named `user_agent` to match the proto field name.
		row["user_agent"] = m.GetUserAgent()
		row["referer_url"] = m.GetRefererUrl()
		row["cachebuster"] = m.GetCachebuster()
		return json.Marshal(row)

	case *telemetryv1.Impression:
		row := envelopeFields(m.GetEnvelope())
		row["campaign_id"] = m.GetCampaignId()
		row["banner_id"] = m.GetBannerId()
		row["zone_id"] = m.GetZoneId()
		row["served_tier"] = m.GetServedTier().String()
		row["billable"] = boolToUint8(m.GetBillable())
		row["blank"] = boolToUint8(m.GetBlank())
		row["ivt_status"] = m.GetIvtStatus()
		return json.Marshal(row)

	case *telemetryv1.Click:
		row := envelopeFields(m.GetEnvelope())
		row["campaign_id"] = m.GetCampaignId()
		row["banner_id"] = m.GetBannerId()
		row["zone_id"] = m.GetZoneId()
		row["dest_url"] = m.GetDestUrl()
		return json.Marshal(row)

	case *telemetryv1.Conversion:
		row := envelopeFields(m.GetEnvelope())
		v := m.GetConversionValue()
		row["campaign_id"] = m.GetCampaignId()
		row["banner_id"] = m.GetBannerId()
		// Money stays integer minor-units + scale on the wire (TX-2; never float).
		row["conversion_value_amount"] = v.GetAmount()
		row["conversion_value_asset_code"] = v.GetAssetCode()
		row["conversion_value_scale"] = v.GetScale()
		row["attribution_decision_id"] = m.GetAttributionDecisionId()
		row["deduplicated"] = boolToUint8(m.GetDeduplicated())
		return json.Marshal(row)

	case *decisionv1.Decision:
		row := envelopeFields(m.GetEnvelope())
		row["zone_id"] = m.GetZoneId()
		row["campaign_id"] = m.GetCampaignId()
		row["banner_id"] = m.GetBannerId()
		row["served_tier"] = m.GetServedTier().String()
		row["propensity"] = m.GetPropensity()
		row["exploration_policy"] = m.GetExplorationPolicy().String()
		row["ml_fail_open"] = boolToUint8(m.GetMlFailOpen())
		row["candidate_count"] = m.GetCandidateCount()
		return json.Marshal(row)

	default:
		return nil, fmt.Errorf("telemetry: no JSON encoder for %T", msg)
	}
}

// envelopeFields flattens the common Envelope into the seven columns shared by
// every kafka_* engine table.
func envelopeFields(e *commonv1.Envelope) map[string]any {
	return map[string]any{
		"tenant_id":      e.GetTenantId(),
		"event_id":       e.GetEventId(),
		"decision_id":    e.GetDecisionId(),
		"model_version":  e.GetModelVersion(),
		"occurred_at":    formatOccurredAt(e.GetOccurredAt()),
		"schema_version": e.GetSchemaVersion(),
		"source":         e.GetSource(),
	}
}

// formatOccurredAt renders a proto Timestamp as a ClickHouse DateTime64(3,'UTC')
// string.  A nil/zero timestamp falls back to the Unix epoch so the column is
// always parseable.
func formatOccurredAt(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return time.Unix(0, 0).UTC().Format(chTimeFormat)
	}
	return ts.AsTime().UTC().Format(chTimeFormat)
}

func boolToUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
