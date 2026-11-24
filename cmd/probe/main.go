package main

import (
	"fmt"

	"github.com/oysterhyd/antigravity-monitor/internal/mock"
)

// Track 1 (feat/data-engine) owns cmd/probe.
// Replace this placeholder with the real local SQLite WAL + Protobuf CLI verifier.
func main() {
	p := mock.New(0)
	defer p.Close()
	snap := p.Snapshot()
	fmt.Printf("probe placeholder ready — %d mock conversations (%d running)\n", len(snap.Conversations), snap.RunningCount)
}
