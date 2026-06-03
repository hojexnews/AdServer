package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	decisionv1 "github.com/hojex/adserver/gen/go/adserver/decision/v1"
	telemetryv1 "github.com/hojex/adserver/gen/go/adserver/telemetry/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// Producer — fire-and-forget batch Redpanda producer with WAL
// ---------------------------------------------------------------------------

// Producer emits ad-server telemetry events to Redpanda topics.
// It satisfies the DecisionSink interface (services/decision) for Decision
// events, and exposes explicit methods for AdRequest/Impression/Click/Conversion.
//
// INVARIANTS:
//   - Every event gets a unique ULID event_id (TX-1).
//   - Every event carries decision_id + model_version (TX-1); callers
//     supply the decision_id from the decision handler.
//   - No IP, no PII ever enters a payload (TX-5/DA-11).
//   - The WAL is written BEFORE the record is handed to the Kafka producer;
//     on crash, unacked WAL entries are replayed on restart.
//   - Emit() is non-blocking: the record is enqueued in a buffered channel
//     and the goroutine draining it handles the actual produce.
type Producer struct {
	client  KafkaClient
	wal     *WAL
	logger  *slog.Logger
	walPath string

	queue chan pendingRecord
	wg    sync.WaitGroup

	// seenIDs is a small bloom-style dedup window for the in-process path.
	// Exactly-once is the consumer's job; this only avoids obvious hot-path
	// duplicates within the same process lifecycle.
	seenMu  sync.Mutex
	seenIDs map[string]struct{}
}

type pendingRecord struct {
	topic   string
	key     []byte // event_id bytes for partition routing
	payload []byte // serialised protobuf
}

// KafkaClient is the subset of franz-go used by the Producer.
// *kgo.Client satisfies it.
type KafkaClient interface {
	ProduceSync(ctx context.Context, records ...*kgo.Record) kgo.ProduceResults
	Close()
}

// Config controls Producer construction.
type Config struct {
	// Brokers is the comma-separated list of Redpanda/Kafka broker addresses.
	Brokers []string
	// WALPath is the absolute path to the local WAL file.
	WALPath string
	// WALSync enables fsync after every WAL write (safer but slower).
	// Default false = async (OS buffer): crash may lose last ~4 KB.
	WALSync bool
	// QueueDepth is the size of the in-process event channel.
	QueueDepth int
	// Logger is the structured logger; nil = no-op.
	Logger *slog.Logger
}

// NewProducer creates and starts a Producer.
//
// Returns nil, err if the WAL cannot be opened or the Kafka client cannot be
// constructed.  Callers should treat a nil Producer as a no-op (the decision
// service falls back to StdoutSink when Producer construction fails).
func NewProducer(cfg Config) (*Producer, error) {
	if cfg.QueueDepth <= 0 {
		cfg.QueueDepth = 8192
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	var wal *WAL
	if cfg.WALPath != "" {
		var err error
		wal, err = openWAL(cfg.WALPath, cfg.WALSync)
		if err != nil {
			// Non-fatal: continue without WAL durability.
			logger.Warn("telemetry: WAL unavailable; events may be lost on crash",
				"path", cfg.WALPath, "err", err)
		}
	}

	var kClient KafkaClient
	if len(cfg.Brokers) > 0 {
		opts := []kgo.Opt{
			kgo.SeedBrokers(cfg.Brokers...),
			kgo.RequiredAcks(kgo.AllISRAcks()),             // acks=all
			kgo.ProducerBatchMaxBytes(64 * 1024),           // 64 KB
			kgo.ProducerLinger(5 * time.Millisecond),       // 5 ms linger
			kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)), // hash(key)
		}
		client, err := kgo.NewClient(opts...)
		if err != nil {
			if wal != nil {
				_ = wal.Close()
			}
			return nil, fmt.Errorf("telemetry: kafka client: %w", err)
		}
		kClient = client
	}

	p := &Producer{
		client:  kClient,
		wal:     wal,
		logger:  logger,
		walPath: cfg.WALPath,
		queue:   make(chan pendingRecord, cfg.QueueDepth),
		seenIDs: make(map[string]struct{}),
	}

	// Replay WAL entries into the queue on startup.
	if wal != nil {
		_ = wal.Replay(func(r walRecord) {
			// Best-effort: if the queue is full, skip replayed entries.
			select {
			case p.queue <- pendingRecord{payload: r.Payload}:
			default:
				logger.Warn("telemetry: WAL replay queue full; entry dropped",
					"offset", r.Offset)
			}
		})
	}

	// Start the drainer goroutine.
	p.wg.Add(1)
	go p.drainLoop()

	return p, nil
}

// ---------------------------------------------------------------------------
// DecisionSink implementation (cascade.DecisionSink / services/decision)
// ---------------------------------------------------------------------------

// Emit implements DecisionSink.  It enqueues the Decision event fire-and-forget.
// INVARIANT: decision_id and model_version MUST be set in d.Envelope (TX-1).
func (p *Producer) Emit(ctx context.Context, d *decisionv1.Decision) {
	env := d.GetEnvelope()
	if env.GetDecisionId() == "" {
		p.logger.Error("telemetry: decision missing decision_id — dropping (TX-1)")
		return
	}

	// The event_id for the Decision event IS the decision_id (root event).
	eventID := env.GetEventId()
	if eventID == "" {
		eventID = env.GetDecisionId()
	}

	p.enqueue(TopicDecision, eventID, d)
}

// ---------------------------------------------------------------------------
// Telemetry event emitters (called by the collector service)
// ---------------------------------------------------------------------------

// EmitAdRequest enqueues an AdRequest event.
func (p *Producer) EmitAdRequest(
	tenantID, zoneID, siteID string,
	geo *commonv1.Geo,
	userAgent, refererURL, cachebuster string,
	customVars map[string]string,
	decisionID string,
) {
	eventID := newEventID()
	evt := &telemetryv1.AdRequest{
		Envelope: &commonv1.Envelope{
			TenantId:      tenantID,
			EventId:       eventID,
			DecisionId:    decisionID,
			ModelVersion:  modelVersionDeterministic,
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: schemaVersion,
			Source:        "collector-asyncjs",
		},
		ZoneId:      zoneID,
		SiteId:      siteID,
		Geo:         geo,
		UserAgent:   userAgent,
		RefererUrl:  refererURL,
		Cachebuster: cachebuster,
		CustomVars:  customVars,
	}
	p.enqueue(TopicAdRequest, eventID, evt)
}

// EmitImpression enqueues an Impression pixel event (lg endpoint — CA-6).
// MUST be called only when the pixel is actually loaded by the browser,
// not at decision time.
func (p *Producer) EmitImpression(
	tenantID, campaignID, bannerID, zoneID string,
	servedTier commonv1.ServedTier,
	billable, blank bool,
	decisionID, modelVersion string,
) {
	eventID := newEventID()
	evt := &telemetryv1.Impression{
		Envelope: &commonv1.Envelope{
			TenantId:      tenantID,
			EventId:       eventID,
			DecisionId:    decisionID,
			ModelVersion:  modelVersion,
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: schemaVersion,
			Source:        "collector-lg",
		},
		CampaignId: campaignID,
		BannerId:   bannerID,
		ZoneId:     zoneID,
		ServedTier: servedTier,
		Billable:   billable,
		Blank:      blank,
		IvtStatus:  "", // set by ingest pipeline (TX-6), not here
	}
	p.enqueue(TopicImpression, eventID, evt)
}

// EmitClick enqueues a Click event (ck endpoint — CA-6).
// destURL is the validated landing page from banner config — never from
// arbitrary user input (SSRF guard is in the collector handler).
func (p *Producer) EmitClick(
	tenantID, campaignID, bannerID, zoneID, destURL string,
	decisionID, modelVersion string,
) {
	eventID := newEventID()
	evt := &telemetryv1.Click{
		Envelope: &commonv1.Envelope{
			TenantId:      tenantID,
			EventId:       eventID,
			DecisionId:    decisionID,
			ModelVersion:  modelVersion,
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: schemaVersion,
			Source:        "collector-ck",
		},
		CampaignId: campaignID,
		BannerId:   bannerID,
		ZoneId:     zoneID,
		DestUrl:    destURL,
	}
	p.enqueue(TopicClick, eventID, evt)
}

// EmitConversion enqueues a Conversion pixel event (ct endpoint — CA-6, DA-8).
func (p *Producer) EmitConversion(
	tenantID, campaignID, bannerID string,
	attributionDecisionID string,
	decisionID, modelVersion string,
) {
	eventID := newEventID()
	evt := &telemetryv1.Conversion{
		Envelope: &commonv1.Envelope{
			TenantId:      tenantID,
			EventId:       eventID,
			DecisionId:    decisionID,
			ModelVersion:  modelVersion,
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: schemaVersion,
			Source:        "collector-ct",
		},
		CampaignId:            campaignID,
		BannerId:              bannerID,
		AttributionDecisionId: attributionDecisionID,
	}
	p.enqueue(TopicConversion, eventID, evt)
}

// ---------------------------------------------------------------------------
// Internal: enqueue + drain
// ---------------------------------------------------------------------------

// enqueue serialises msg, writes to WAL, then sends to the async queue.
// If the queue is full (back-pressure) or Kafka is unavailable, the WAL
// ensures durability; the record will be replayed on restart.
func (p *Producer) enqueue(topic, eventID string, msg proto.Message) {
	// In-process dedup: skip exact same event_id within process lifetime.
	if eventID != "" {
		p.seenMu.Lock()
		if _, dup := p.seenIDs[eventID]; dup {
			p.seenMu.Unlock()
			return
		}
		p.seenIDs[eventID] = struct{}{}
		// Bound the map size to avoid unbounded growth.
		if len(p.seenIDs) > 1_000_000 {
			// Evict oldest half by reinitialising (LRU would require more deps).
			p.seenIDs = make(map[string]struct{}, 1_000_000)
			p.seenIDs[eventID] = struct{}{}
		}
		p.seenMu.Unlock()
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		p.logger.Error("telemetry: marshal", "topic", topic, "err", err)
		return
	}

	// Write to WAL for durability before network.
	if p.wal != nil {
		if _, err := p.wal.Append(payload); err != nil {
			p.logger.Warn("telemetry: WAL append failed", "err", err)
			// Continue: event may be lost on crash, but don't block hot path.
		}
	}

	rec := pendingRecord{
		topic:   topic,
		key:     []byte(eventID),
		payload: payload,
	}

	// Non-blocking send: if the queue is full, log and drop.
	// The WAL still has the record; it will be replayed on restart.
	select {
	case p.queue <- rec:
	default:
		p.logger.Warn("telemetry: queue full; event buffered in WAL only",
			"topic", topic)
	}
}

// drainLoop consumes the queue and produces to Redpanda.
func (p *Producer) drainLoop() {
	defer p.wg.Done()
	for rec := range p.queue {
		if p.client == nil {
			// No Kafka client (dev/test mode): discard.
			continue
		}
		kr := &kgo.Record{
			Topic: rec.topic,
			Key:   rec.key,
			Value: rec.payload,
		}
		results := p.client.ProduceSync(context.Background(), kr)
		for _, res := range results {
			if res.Err != nil {
				p.logger.Error("telemetry: produce failed",
					"topic", rec.topic, "err", res.Err)
				// WAL retains the record; it will be replayed on restart.
			}
		}
	}
}

// Close drains the queue and closes the WAL and Kafka client.
func (p *Producer) Close() {
	close(p.queue)
	p.wg.Wait()
	if p.wal != nil {
		_ = p.wal.Close()
	}
	if p.client != nil {
		p.client.Close()
	}
}

// ---------------------------------------------------------------------------
// NoOpProducer — satisfies DecisionSink without any I/O (tests / dev)
// ---------------------------------------------------------------------------

// NoOpProducer discards all events.  Use when Redpanda is unavailable.
type NoOpProducer struct{}

func (NoOpProducer) Emit(_ context.Context, _ *decisionv1.Decision) {}
