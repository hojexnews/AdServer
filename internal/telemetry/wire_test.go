package telemetry

import (
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	decisionv1 "github.com/hojex/adserver/gen/go/adserver/decision/v1"
	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
	telemetryv1 "github.com/hojex/adserver/gen/go/adserver/telemetry/v1"
)

func decodeJSON(t *testing.T, msg proto.Message) map[string]any {
	t.Helper()
	b, err := marshalWire(WireJSON, msg)
	if err != nil {
		t.Fatalf("marshalWire JSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// assertKeys fails if the row's key set does not equal want exactly — the
// ClickHouse JSONEachRow ingestion requires field names to match the kafka_*
// engine columns and rejects unknown fields.
func assertKeys(t *testing.T, row map[string]any, want []string) {
	t.Helper()
	if len(row) != len(want) {
		t.Errorf("key count = %d, want %d (%v)", len(row), len(want), keys(row))
	}
	for _, k := range want {
		if _, ok := row[k]; !ok {
			t.Errorf("missing column %q (have %v)", k, keys(row))
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func testEnv() *commonv1.Envelope {
	return &commonv1.Envelope{
		TenantId:      "tenant-1",
		EventId:       "evt-1",
		DecisionId:    "dec-1",
		ModelVersion:  "",
		OccurredAt:    timestamppb.New(time.Date(2026, 6, 3, 12, 30, 45, 123_000_000, time.UTC)),
		SchemaVersion: "1.0.0",
		Source:        "test",
	}
}

var envelopeCols = []string{
	"tenant_id", "event_id", "decision_id", "model_version",
	"occurred_at", "schema_version", "source",
}

func TestJSON_AdRequest_ColumnsMatchKafkaEngine(t *testing.T) {
	row := decodeJSON(t, &telemetryv1.AdRequest{
		Envelope:    testEnv(),
		ZoneId:      "z1",
		SiteId:      "s1",
		Geo:         &commonv1.Geo{Country: "BR", City: "Sao Paulo"},
		UserAgent:   "mobile/chrome", // coarse class, never raw UA
		RefererUrl:  "https://pub.example/page",
		Cachebuster: "42",
	})
	assertKeys(t, row, append(append([]string{}, envelopeCols...),
		"zone_id", "site_id", "geo_country", "geo_city",
		"user_agent", "referer_url", "cachebuster"))

	if row["occurred_at"] != "2026-06-03 12:30:45.123" {
		t.Errorf("occurred_at = %v, want ClickHouse DateTime64 string", row["occurred_at"])
	}
	if row["user_agent"] != "mobile/chrome" {
		t.Errorf("user_agent must carry the coarse class, got %v", row["user_agent"])
	}
}

func TestJSON_Impression_Columns(t *testing.T) {
	row := decodeJSON(t, &telemetryv1.Impression{
		Envelope:   testEnv(),
		CampaignId: "c1", BannerId: "b1", ZoneId: "z1",
		ServedTier: commonv1.ServedTier_SERVED_TIER_CONTRACT,
		Billable:   true, Blank: false, IvtStatus: "",
	})
	assertKeys(t, row, append(append([]string{}, envelopeCols...),
		"campaign_id", "banner_id", "zone_id", "served_tier",
		"billable", "blank", "ivt_status"))
	if row["served_tier"] != "SERVED_TIER_CONTRACT" {
		t.Errorf("served_tier = %v", row["served_tier"])
	}
	if row["billable"] != float64(1) || row["blank"] != float64(0) {
		t.Errorf("billable/blank must be UInt8 0/1, got %v/%v", row["billable"], row["blank"])
	}
}

func TestJSON_Conversion_MoneyStaysInteger(t *testing.T) {
	row := decodeJSON(t, &telemetryv1.Conversion{
		Envelope:        testEnv(),
		CampaignId:      "c1",
		BannerId:        "b1",
		ConversionValue: &moneyv1.Money{AssetCode: "BRL", Amount: 1999, Scale: 2},
		Deduplicated:    false,
	})
	assertKeys(t, row, append(append([]string{}, envelopeCols...),
		"campaign_id", "banner_id",
		"conversion_value_amount", "conversion_value_asset_code",
		"conversion_value_scale", "attribution_decision_id", "deduplicated"))
	if row["conversion_value_amount"] != float64(1999) {
		t.Errorf("amount = %v, want 1999 (minor units, never float dollars)", row["conversion_value_amount"])
	}
	if row["conversion_value_asset_code"] != "BRL" {
		t.Errorf("asset_code = %v", row["conversion_value_asset_code"])
	}
}

func TestJSON_Click_Columns(t *testing.T) {
	row := decodeJSON(t, &telemetryv1.Click{
		Envelope:   testEnv(),
		CampaignId: "c1", BannerId: "b1", ZoneId: "z1",
		DestUrl: "https://adv.example/lp",
	})
	assertKeys(t, row, append(append([]string{}, envelopeCols...),
		"campaign_id", "banner_id", "zone_id", "dest_url"))
}

func TestJSON_Decision_Columns(t *testing.T) {
	row := decodeJSON(t, &decisionv1.Decision{
		Envelope:          testEnv(),
		ZoneId:            "z1",
		CampaignId:        "c1",
		BannerId:          "b1",
		ServedTier:        commonv1.ServedTier_SERVED_TIER_REMNANT,
		Propensity:        1.0,
		ExplorationPolicy: decisionv1.ExplorationPolicy_EXPLORATION_POLICY_DETERMINISTIC,
		MlFailOpen:        false,
		CandidateCount:    3,
	})
	assertKeys(t, row, append(append([]string{}, envelopeCols...),
		"zone_id", "campaign_id", "banner_id", "served_tier",
		"propensity", "exploration_policy", "ml_fail_open", "candidate_count"))
	if row["exploration_policy"] != "EXPLORATION_POLICY_DETERMINISTIC" {
		t.Errorf("exploration_policy = %v", row["exploration_policy"])
	}
}

func TestProtobufStillDefault(t *testing.T) {
	msg := &telemetryv1.Click{Envelope: testEnv(), CampaignId: "c1"}
	b, err := marshalWire(WireProtobuf, msg)
	if err != nil {
		t.Fatalf("protobuf marshal: %v", err)
	}
	// Round-trips as protobuf, not JSON.
	var got telemetryv1.Click
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("protobuf unmarshal: %v", err)
	}
	if got.GetCampaignId() != "c1" {
		t.Errorf("protobuf round-trip lost data: %q", got.GetCampaignId())
	}
}

func TestParseWireFormat(t *testing.T) {
	if ParseWireFormat("json") != WireJSON {
		t.Error(`"json" should map to WireJSON`)
	}
	if ParseWireFormat("") != WireProtobuf || ParseWireFormat("protobuf") != WireProtobuf {
		t.Error("default/protobuf should map to WireProtobuf")
	}
}
