// export_test.go exposes internal telemetry types/functions for the _test
// package.  This file is compiled ONLY during tests (the `_test.go` suffix).
package telemetry

// WALRecord is the exported version of walRecord for use in tests.
type WALRecord = walRecord

// OpenWALForTest opens a WAL at path for testing.
// It is equivalent to openWAL but exported so the _test package can access it.
func OpenWALForTest(path string, syncWrites bool) (*WAL, error) {
	return openWAL(path, syncWrites)
}

// Expose Replay with the exported WALRecord type.
// (WAL.Replay already uses walRecord; the alias above makes it accessible.)
