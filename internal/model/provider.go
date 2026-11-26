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

// ConversationHydrator is an optional interface implemented by SnapshotProviders
// that support on-demand priority hydration and live transcript watching for
// UI-selected conversations. Implementations must be safe for concurrent use
// and must not block the caller.
type ConversationHydrator interface {
	// PinConversation binds a consumer slot (e.g., "desktop" or "tray") to convID,
	// replacing any previous pin held by that slot. Passing an empty convID
	// releases the slot's pin.
	PinConversation(slot, convID string)
}
