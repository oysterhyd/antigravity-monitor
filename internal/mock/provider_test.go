package mock

import (
	"testing"
	"time"
)

func TestMockProviderSnapshotAndSubscribe(t *testing.T) {
	p := New(0)
	defer p.Close()

	snap := p.Snapshot()
	if len(snap.Conversations) != 3 {
		t.Fatalf("expected 3 seeded conversations, got %d", len(snap.Conversations))
	}
	if snap.RunningCount != 2 {
		t.Fatalf("expected 2 running conversations, got %d", snap.RunningCount)
	}
	if len(snap.Conversations[0].Subagents) != 3 {
		t.Fatalf("expected 3 subagents on primary conv, got %d", len(snap.Conversations[0].Subagents))
	}

	ch, cancel := p.Subscribe()
	defer cancel()

	// Initial snapshot delivered immediately
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial snapshot")
	}

	p.TriggerBurst()
	select {
	case updated := <-ch:
		if updated.GeneratedAt.IsZero() {
			t.Fatal("expected non-zero GeneratedAt")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for burst snapshot")
	}
}
