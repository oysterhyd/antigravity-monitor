package main

import (
	"context"
	"flag"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/config"
	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/mock"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/quota"
	"github.com/oysterhyd/antigravity-monitor/internal/source"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/tray"
	appupdater "github.com/oysterhyd/antigravity-monitor/internal/updater"
)

const githubRepo = "oysterhyd/antigravity-monitor"

func main() {
	appupdater.Register(githubRepo)
	useMock := flag.Bool("mock", false, "use simulated SnapshotProvider instead of reading local Antigravity records")
	mockInterval := flag.Duration("mock-interval", 2800*time.Millisecond, "mock stream interval when -mock is enabled")
	dataDir := flag.String("data-dir", "", "optional override path for ~/.gemini/antigravity")
	cfgPath := flag.String("config", "", "optional override path for settings.json")
	startHidden := flag.Bool("tray-only", false, "start resident in system tray without opening the desktop window")
	flag.Parse()

	store, err := config.NewStore(*cfgPath)
	if err != nil {
		log.Fatalf("[monitor] load settings: %v", err)
	}
	settings := store.Get()

	provider := buildProvider(*useMock, *mockInterval, *dataDir, settings)
	defer func() { _ = provider.Close() }()

	// Initialize the Usage History and Account Quota readers.
	var histSvc history.Aggregator
	var quotaSvc quota.Tracker
	if !*useMock {
		calc, calcErr := metrics.NewCalculator(settings.PricingOverridePath)
		if calcErr != nil {
			calc, _ = metrics.NewDefaultCalculator()
		}
		cfgDir := filepath.Dir(store.Path())
		histCachePath := filepath.Join(cfgDir, "usage_cache.json")
		quotaHistPath := filepath.Join(cfgDir, "quota_history.json")
		hist := history.NewAggregator(*dataDir, histCachePath, calc)
		histSvc = hist
		quotaSvc = quota.NewService(quotaHistPath, quota.ReadCLI)

		// Warm up history index asynchronously so startup stays instant, then refresh periodically.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			_ = hist.Refresh(ctx)
			cancel()

			ticker := time.NewTicker(45 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				rCtx, rCancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = hist.Refresh(rCtx)
				rCancel()
			}
		}()
	}

	var (
		deskHost *desktopHost
		trayCtl  *tray.Controller
	)

	trayCtl = tray.New(tray.Options{
		Provider: provider,
		Store:    store,
		Callbacks: tray.Callbacks{
			OnOpenDesktop: func() {
				if deskHost != nil {
					deskHost.Show()
				}
			},
			OnFocusAgent: func(convID, agentID string) {
				if deskHost != nil {
					deskHost.FocusAgent(convID, agentID)
				}
			},
			OnSettingsChange: func(s model.Settings) {
				if deskHost != nil {
					deskHost.ApplySettings(s)
				}
			},
			OnQuit: func() {
				if deskHost != nil {
					deskHost.SetQuitting(true)
				}
				mygo.App.Quit()
			},
		},
	})

	mygo.App.SetName("Antigravity Monitor")
	if *startHidden {
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	}

	if !mygo.App.RequestSingleInstanceLock() {
		log.Printf("[monitor] another instance is already running; exiting")
		return
	}
	mygo.App.OnSecondInstance(func(_ []string, _ string) {
		if deskHost != nil {
			deskHost.Show()
		}
	})

	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		if deskHost != nil {
			deskHost.SetQuitting(true)
		}
	})

	mygo.App.OnWindowAllClosed(func() {
		if !store.Get().KeepInTrayOnClose {
			mygo.App.Quit()
		}
	})

	mygo.App.WhenReady(func() {
		deskHost = newDesktopHost(provider, histSvc, quotaSvc, store, func(s model.Settings) {
			trayCtl.ApplySettings(s)
		})

		if err := trayCtl.Start(); err != nil {
			log.Fatalf("[monitor] start tray: %v", err)
		}
		if !*startHidden {
			deskHost.Show()
		}
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	if deskHost != nil {
		deskHost.Close()
	}
}

func buildProvider(useMock bool, interval time.Duration, dataDir string, settings model.Settings) model.SnapshotProvider {
	if useMock {
		return mock.New(interval)
	}
	p, err := source.NewProvider(source.Options{
		RootDir:             dataDir,
		PricingOverridePath: settings.PricingOverridePath,
	})
	if err != nil {
		log.Printf("[monitor] failed to initialize local Antigravity engine (%v); retrying without pricing override", err)
		p, err = source.NewProvider(source.Options{RootDir: dataDir})
		if err != nil {
			log.Fatalf("[monitor] initialize local Antigravity data engine: %v", err)
		}
	}
	return p
}

// desktopHost manages the native Apple Liquid Glass desktop window lifecycle,
// hide-to-tray close interception, and bidirectional settings synchronization.
type desktopHost struct {
	app        *desktop.App
	win        *mygo.Window
	store      *config.Store
	onSettings func(model.Settings)

	mu       sync.Mutex
	settings model.Settings
	quitting bool
}

func newDesktopHost(provider model.SnapshotProvider, histSvc history.Aggregator, quotaSvc quota.Tracker, store *config.Store, onSettings func(model.Settings)) *desktopHost {
	s := store.Get()
	h := &desktopHost{
		store:      store,
		onSettings: onSettings,
		settings:   s,
	}

	h.app = desktop.New(provider, desktop.Options{
		Settings:      &s,
		History:       histSvc,
		Quota:         quotaSvc,
		QuotaInterval: 30 * time.Second,
		OnSettingsChange: func(next model.Settings) {
			prevLaunch := h.settings.LaunchAtLogin
			next = config.Normalize(next)
			if err := h.store.Save(next); err != nil {
				log.Printf("[monitor] save settings failed: %v", err)
			}
			h.mu.Lock()
			h.settings = next
			h.mu.Unlock()

			if next.LaunchAtLogin != prevLaunch {
				if err := mygo.App.SetOpenAtLogin(next.LaunchAtLogin); err != nil {
					log.Printf("[monitor] SetOpenAtLogin(%v): %v", next.LaunchAtLogin, err)
				}
			}
			if h.onSettings != nil {
				h.onSettings(next)
			}
		},
	})

	h.win = mygo.NewWindow(mygo.WindowOptions{
		Title:          "Antigravity Monitor",
		Width:          900,
		Height:         590,
		MinWidth:       760,
		MinHeight:      520,
		Hidden:         true,
		AlwaysOnTop:    s.AlwaysOnTop,
		TitleBarStyle:  mygo.TitleBarHidden,
		TitleBarHeight: 32,
		Vibrancy:       mygo.VibrancyGlass,
		Content:        ui.View(h.app.View),
	})
	h.app.SetWindow(h.win)

	h.win.OnClose(func(e *mygo.CloseEvent) {
		h.mu.Lock()
		keep := !h.quitting && h.settings.KeepInTrayOnClose
		h.mu.Unlock()
		if keep {
			e.PreventDefault()
			h.win.Hide()
		}
	})

	return h
}

func (h *desktopHost) Show() {
	mygo.RunOnMain(func() {
		if h.win == nil {
			return
		}
		if h.win.IsMinimized() {
			h.win.Restore()
		}
		h.win.Show()
		h.win.Focus()
		h.win.Invalidate()
	})
}

func (h *desktopHost) FocusAgent(convID, agentID string) {
	mygo.RunOnMain(func() {
		if h.app != nil {
			h.app.FocusConversationAgent(convID, agentID)
		}
		if h.win != nil {
			if h.win.IsMinimized() {
				h.win.Restore()
			}
			h.win.Show()
			h.win.Focus()
			h.win.Invalidate()
		}
	})
}

func (h *desktopHost) ApplySettings(s model.Settings) {
	mygo.RunOnMain(func() {
		h.mu.Lock()
		h.settings = s
		h.mu.Unlock()
		if h.app != nil {
			h.app.ApplySettings(s)
		}
	})
}

func (h *desktopHost) SetQuitting(q bool) {
	h.mu.Lock()
	h.quitting = q
	h.mu.Unlock()
}

func (h *desktopHost) Close() {
	if h.app != nil {
		h.app.Close()
	}
}
