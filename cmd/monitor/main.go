package main

import (
	"fmt"

	"github.com/oysterhyd/antigravity-monitor/internal/mock"
)

// Track 3 (feat/tray-shell) owns cmd/monitor.
// Final entry point wiring SnapshotProvider (real engine on merge, -mock flag for dev) + Desktop Window + Tray Flyout.
func main() {
	p := mock.New(0)
	defer p.Close()
	snap := p.Snapshot()
	fmt.Printf("antigravity-monitor scaffold ready — %d conversations\n", len(snap.Conversations))
}
