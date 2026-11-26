package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/config"
	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/mock"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/source"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/tray"
	appupdater "github.com/oysterhyd/antigravity-monitor/internal/updater"
)

const githubRepo = "oysterhyd/antigravity-monitor"

func tuneRuntimeMemory() {
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(65)
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(96 << 20)
	}
}

func cleanupLegacyQuotaCache() {
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		return
	}
	legacyDir := filepath.Join(cacheDir, "antigravity-monitor", "quota-reader")
	if _, err := os.Stat(legacyDir); err != nil {
		return
	}
	_ = os.RemoveAll(legacyDir)
	_ = os.Remove(filepath.Join(cacheDir, "antigravity-monitor"))
}

func main() {
	tuneRuntimeMemory()
	cleanupLegacyQuotaCache()
	appupdater.Register(githubRepo)
	useMock := flag.Bool("mock", false, "use simulated SnapshotProvider instead of reading local Antigravity records")
	mockInterval := flag.Duration("mock-interval", 2800*time.Millisecond, "mock stream interval when -mock is enabled")
	dataDir := flag.String("data-dir", "", "optional single data root override (defaults to auto-discovering ~/.gemini/antigravity and T3 Code ACP roots)")
	cfgPath := flag.String("config", "", "optional override path for settings.json")
	startHidden := flag.Bool("tray-only", false, "start resident in system tray without opening the desktop window")
	flag.Parse()

	mygo.App.SetName("Antigravity Monitor")
	if *startHidden {
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	}
	if !mygo.App.RequestSingleInstanceLock() {
		log.Printf("[monitor] another instance is already running; exiting")
		return
	}

	store, err := config.NewStore(*cfgPath)
	if err != nil {
		log.Fatalf("[monitor] load settings: %v", err)
	}
	settings := store.Get()

	// Initialize the Usage History reader.
	var histSvc history.Aggregator
	var provider model.SnapshotProvider
	wakeHistoryCh := make(chan struct{}, 1)
	wakeHistory := func() {
		select {
		case wakeHistoryCh <- struct{}{}:
		default:
		}
	}
	if *useMock {
		provider = mock.New(*mockInterval)
	} else {
		var calc *metrics.Calculator
		provider, calc = buildProvider(*dataDir, settings)
		cfgDir := filepath.Dir(store.Path())
		histCachePath := filepath.Join(cfgDir, "usage_cache.json")
		hist := history.NewAggregator(*dataDir, histCachePath, calc)
		defer func() { _ = hist.Close() }()
		histSvc = hist

		// Defer initial history warm-up briefly so cold startup stays instant;
		// wakes immediately if the user opens Usage History.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go refreshHistory(ctx, hist, wakeHistoryCh)
	}
	defer func() { _ = provider.Close() }()

	var (
		deskHost *desktopHost
		trayCtl  *tray.Controller
	)

	trayCtl = tray.New(tray.Options{
		Provider: provider,
		History:  histSvc,
		Store:    store,
		Callbacks: tray.Callbacks{
			OnOpenDesktop: func() {
				if deskHost != nil {
					deskHost.Show()
				}
			},
			OnOpenView: func(view tray.ViewMode) {
				if deskHost == nil {
					return
				}
				switch view {
				case tray.ViewHistory:
					deskHost.ShowView(desktop.ViewHistory)
				default:
					deskHost.ShowView(desktop.ViewLive)
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
			OnHistoryViewOpen: wakeHistory,
			OnQuit: func() {
				if deskHost != nil {
					deskHost.SetQuitting(true)
				}
				mygo.App.Quit()
			},
		},
	})

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

	// Keep the process alive in the system tray when windows close; exiting on
	// desktop window close (when KeepInTrayOnClose is false) is handled directly
	// in desktopHost's window OnClosed callback so closing the tray flyout never quits.
	mygo.App.OnWindowAllClosed(func() {})

	mygo.App.WhenReady(func() {
		deskHost = newDesktopHost(
			provider,
			histSvc,
			store,
			func(s model.Settings) { trayCtl.ApplySettings(s) },
			wakeHistory,
		)

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

func buildProvider(dataDir string, settings model.Settings) (*source.Provider, *metrics.Calculator) {
	calc, err := metrics.NewCalculator(settings.PricingOverridePath)
	if err != nil {
		log.Printf("[monitor] failed to initialize local Antigravity engine (%v); retrying without pricing override", err)
		calc, err = metrics.NewDefaultCalculator()
	}
	if err != nil {
		log.Fatalf("[monitor] initialize local Antigravity data engine: %v", err)
	}
	provider, err := source.NewProvider(source.Options{
		RootDir:    dataDir,
		Calculator: calc,
		AsyncInit:  true,
	})
	if err != nil {
		log.Fatalf("[monitor] initialize local Antigravity data engine: %v", err)
	}
	return provider, calc
}

func refreshHistory(ctx context.Context, hist history.Aggregator, wakeCh <-chan struct{}) {
	// Wait briefly after startup so cold launch stays responsive, unless History is opened first.
	initDelay := time.NewTimer(3500 * time.Millisecond)
	select {
	case <-ctx.Done():
		initDelay.Stop()
		return
	case <-initDelay.C:
	case <-wakeCh:
		if !initDelay.Stop() {
			select {
			case <-initDelay.C:
			default:
			}
		}
	}

	run := func(timeout time.Duration) {
		refresh, cancel := context.WithTimeout(ctx, timeout)
		_ = hist.Refresh(refresh)
		cancel()
	}

	run(20 * time.Second)
	lastRun := time.Now()

	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wakeCh:
			if time.Since(lastRun) < 15*time.Second {
				continue
			}
			run(15 * time.Second)
			lastRun = time.Now()
		case <-ticker.C:
			run(15 * time.Second)
			lastRun = time.Now()
		}
	}
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

func newDesktopHost(
	provider model.SnapshotProvider,
	histSvc history.Aggregator,
	store *config.Store,
	onSettings func(model.Settings),
	onHistoryOpen func(),
) *desktopHost {
	s := store.Get()
	h := &desktopHost{
		store:      store,
		onSettings: onSettings,
		settings:   s,
	}

	h.app = desktop.New(provider, desktop.Options{
		Settings:          &s,
		History:           histSvc,
		OnHistoryViewOpen: onHistoryOpen,
		OnSettingsChange: func(next model.Settings) {
			next = config.Normalize(next)
			if err := h.store.Save(next); err != nil {
				log.Printf("[monitor] save settings failed: %v", err)
			}
			h.mu.Lock()
			prevLaunch := h.settings.LaunchAtLogin
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
	return h
}

func (h *desktopHost) show() {
	if h.win == nil {
		h.createWindow()
	}
	if h.win.IsMinimized() {
		h.win.Restore()
	}
	h.win.Show()
	h.win.Focus()
	h.app.SetWindowVisible(true)
	h.win.Invalidate()
}

func (h *desktopHost) createWindow() {
	h.mu.Lock()
	s := h.settings
	h.mu.Unlock()
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:          "Antigravity Monitor",
		StateKey:       "main",
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
	h.win = win
	h.app.SetWindow(win)

	// Allow the native window to actually close so mygo releases its D3D11
	// renderer and shared GPU device while resident in the system tray.
	win.OnClosed(func() {
		if h.win == win {
			h.win = nil
			h.app.SetWindow(nil)
		}
		h.app.SetWindowVisible(false)

		h.mu.Lock()
		shouldQuit := !h.quitting && !h.settings.KeepInTrayOnClose
		h.mu.Unlock()

		if shouldQuit {
			mygo.App.Quit()
			return
		}
		time.AfterFunc(250*time.Millisecond, func() {
			debug.FreeOSMemory()
		})
	})
}

func (h *desktopHost) Show() {
	mygo.RunOnMain(h.show)
}

func (h *desktopHost) ShowView(v desktop.ActiveView) {
	mygo.RunOnMain(func() {
		if h.app != nil {
			h.app.SetActiveView(v)
		}
		h.show()
	})
}

func (h *desktopHost) FocusAgent(convID, agentID string) {
	mygo.RunOnMain(func() {
		h.app.FocusConversationAgent(convID, agentID)
		h.show()
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
