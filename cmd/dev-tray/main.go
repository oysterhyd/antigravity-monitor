package main

import (
	"flag"
	"log"
	"time"

	"github.com/egoist/mygo"
	"github.com/oysterhyd/antigravity-monitor/internal/config"
	"github.com/oysterhyd/antigravity-monitor/internal/mock"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/tray"
)

// cmd/dev-tray is the standalone Track 3 harness for developing and tuning
// the Pulse-style frosted glass tray flyout, dot-matrix chart, multi-session
// switcher, and settings persistence against internal/mock.
func main() {
	interval := flag.Duration("interval", 2800*time.Millisecond, "mock request stream interval (0 disables auto stream)")
	cfgPath := flag.String("config", "", "optional override path for settings.json")
	showNow := flag.Bool("show", false, "immediately open the tray flyout on startup")
	exitAfter := flag.Duration("exit-after", 0, "optional duration after which the process exits cleanly (useful for smoke tests)")
	flag.Parse()

	store, err := config.NewStore(*cfgPath)
	if err != nil {
		log.Fatalf("[dev-tray] load settings: %v", err)
	}
	log.Printf("[dev-tray] settings loaded from %s", store.Path())

	provider := mock.New(*interval)
	defer func() { _ = provider.Close() }()

	ctl := tray.New(tray.Options{
		Provider:    provider,
		Store:       store,
		ShowOnStart: *showNow,
		Callbacks: tray.Callbacks{
			OnOpenDesktop: func() {
				log.Printf("[dev-tray] OnOpenDesktop triggered (standalone tray harness)")
			},
			OnFocusAgent: func(convID, agentID string) {
				log.Printf("[dev-tray] OnFocusAgent triggered: conv=%s agent=%s", convID, agentID)
			},
			OnSettingsChange: func(s model.Settings) {
				log.Printf("[dev-tray] settings saved: theme=%s auto_follow=%v warn=%.2f keep_in_tray=%v always_on_top=%v launch=%v",
					s.ThemeMode, s.AutoFollowActive, s.ContextWarnThreshold, s.KeepInTrayOnClose, s.AlwaysOnTop, s.LaunchAtLogin)
			},
			OnQuit: func() {
				log.Printf("[dev-tray] quitting")
				mygo.App.Quit()
			},
		},
	})

	mygo.App.SetName("Antigravity Monitor (Dev Tray)")
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	// Keep resident in the system tray after the flyout closes.
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(func() {
		if err := ctl.Start(); err != nil {
			log.Fatalf("[dev-tray] start tray: %v", err)
		}
		log.Printf("[dev-tray] tray icon ready — click the notification area icon to toggle the Pulse flyout")
		if *exitAfter > 0 {
			time.AfterFunc(*exitAfter, func() {
				mygo.App.Quit()
			})
		}
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
