// export_test.go exposes internal telemetry types/functions for the _test
// package.  This file is compiled ONLY during tests (the `_test.go` suffix).
package telemetry

import "log/slog"

// WALRecord is the exported version of walRecord for use in tests.
type WALRecord = walRecord

// NewProducerForTest builds a Producer with an INJECTED KafkaClient and a WAL at
// walPath, running the same startup replay + drain as NewProducer.  Test-only —
// lets tests exercise the WAL→replay→produce→compact path with a fake client.
func NewProducerForTest(client KafkaClient, walPath string) (*Producer, error) {
	wal, err := openWAL(walPath, false)
	if err != nil {
		return nil, err
	}
	p := &Producer{
		client:  client,
		wal:     wal,
		logger:  slog.Default(),
		walPath: walPath,
		queue:   make(chan pendingRecord, 8192),
		seenIDs: make(map[string]struct{}),
	}
	_ = wal.Replay(func(r walRecord) {
		p.queue <- pendingRecord{topic: r.Topic, key: r.Key, payload: r.Payload, walEnd: r.End}
	})
	p.wg.Add(1)
	go p.drainLoop()
	return p, nil
}

// OpenWALForTest opens a WAL at path for testing.
// It is equivalent to openWAL but exported so the _test package can access it.
func OpenWALForTest(path string, syncWrites bool) (*WAL, error) {
	return openWAL(path, syncWrites)
}

// Expose Replay with the exported WALRecord type.
// (WAL.Replay already uses walRecord; the alias above makes it accessible.)
