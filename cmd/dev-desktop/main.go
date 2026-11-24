package main

import (
	"fmt"

	"github.com/oysterhyd/antigravity-monitor/internal/mock"
)

// Track 2 (feat/desktop-ui) owns cmd/dev-desktop.
// Replace this placeholder with the standalone mygo Native UI desktop window harness backed by internal/mock.
func main() {
	p := mock.New(0)
	defer p.Close()
	snap := p.Snapshot()
	fmt.Printf("dev-desktop placeholder ready — active conv: %s\n", snap.ActiveConversationID)
}
