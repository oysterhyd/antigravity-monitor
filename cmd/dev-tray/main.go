package main

import (
	"fmt"

	"github.com/oysterhyd/antigravity-monitor/internal/mock"
)

// Track 3 (feat/tray-shell) owns cmd/dev-tray.
// Replace this placeholder with the standalone mygo Tray Flyout harness backed by internal/mock.
func main() {
	p := mock.New(0)
	defer p.Close()
	snap := p.Snapshot()
	fmt.Printf("dev-tray placeholder ready — running convs: %d\n", snap.RunningCount)
}
