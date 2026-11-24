package model

// SnapshotProvider is the FROZEN boundary between Track 1 (data engine) and Tracks 2 & 3 (native UI).
// Implementations must be safe for concurrent use by multiple goroutines.
type SnapshotProvider interface {
	// Snapshot returns the latest immutable AppSnapshot without blocking on I/O.
	Snapshot() AppSnapshot

	// Subscribe registers a listener that receives updated snapshots whenever local data changes.
	// The returned cancel function must be called when the consumer exits.
	// Slow consumers may have intermediate snapshots coalesced (latest-wins semantics).
	Subscribe() (ch <-chan AppSnapshot, cancel func())

	// Close stops background watchers and releases read-only handles.
	Close() error
}
