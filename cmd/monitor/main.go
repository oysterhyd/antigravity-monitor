package main

import (
	"flag"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/config"
	"github.com/oysterhyd/antigravity-monitor/internal/mock"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/tray"
)

// DesktopLauncher defines the minimal integration boundary between Track 3's
// application shell and Track 2's full Apple Liquid Glass desktop window.
// During final merge, replace newPlaceholderDesktop with Track 2's desktop window.
type DesktopLauncher interface {
	Show()
	FocusAgent(convID, agentID string)
	ApplySettings(s model.Settings)
	SetQuitting(quitting bool)
	Close()
}

func main() {
	useMock := flag.Bool("mock", true, "use simulated SnapshotProvider (will default to local Antigravity engine after Track 1 merge)")
	mockInterval := flag.Duration("mock-interval", 2800*time.Millisecond, "mock stream interval when -mock is enabled")
	cfgPath := flag.String("config", "", "optional override path for settings.json")
	startHidden := flag.Bool("tray-only", false, "start resident in system tray without opening the desktop window")
	flag.Parse()

	store, err := config.NewStore(*cfgPath)
	if err != nil {
		log.Fatalf("[monitor] load settings: %v", err)
	}
	settings := store.Get()

	provider := buildProvider(*useMock, *mockInterval, settings)
	defer func() { _ = provider.Close() }()

	var (
		desktop DesktopLauncher
		trayCtl *tray.Controller
	)

	trayCtl = tray.New(tray.Options{
		Provider: provider,
		Store:    store,
		Callbacks: tray.Callbacks{
			OnOpenDesktop: func() {
				if desktop != nil {
					desktop.Show()
				}
			},
			OnFocusAgent: func(convID, agentID string) {
				if desktop != nil {
					desktop.FocusAgent(convID, agentID)
				}
			},
			OnSettingsChange: func(s model.Settings) {
				if desktop != nil {
					desktop.ApplySettings(s)
				}
			},
			OnQuit: func() {
				if desktop != nil {
					desktop.SetQuitting(true)
				}
				mygo.App.Quit()
			},
		},
	})

	mygo.App.SetName("Antigravity Monitor")
	if *startHidden || settings.LaunchAtLogin {
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	}

	if !mygo.App.RequestSingleInstanceLock() {
		log.Printf("[monitor] another instance is already running; exiting")
		return
	}
	mygo.App.OnSecondInstance(func(_ []string, _ string) {
		if desktop != nil {
			desktop.Show()
		}
	})

	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		if desktop != nil {
			desktop.SetQuitting(true)
		}
	})

	// Closing the main window keeps the monitor resident in the system tray
	// unless KeepInTrayOnClose is disabled.
	mygo.App.OnWindowAllClosed(func() {
		if !store.Get().KeepInTrayOnClose {
			mygo.App.Quit()
		}
	})

	mygo.App.WhenReady(func() {
		// TODO(merge Track 2): swap newPlaceholderDesktop with desktop.New(provider, store, ...).
		desktop = newPlaceholderDesktop(provider, store, func(s model.Settings) {
			trayCtl.ApplySettings(s)
		})

		if err := trayCtl.Start(); err != nil {
			log.Fatalf("[monitor] start tray: %v", err)
		}
		if !*startHidden {
			desktop.Show()
		}
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	if desktop != nil {
		desktop.Close()
	}
}

func buildProvider(useMock bool, interval time.Duration, _ model.Settings) model.SnapshotProvider {
	if !useMock {
		// TODO(merge Track 1): return source.NewLocalProvider(settings.PricingOverridePath)
		log.Printf("[monitor] real data engine (Track 1) not yet merged; falling back to mock provider")
	}
	return mock.New(interval)
}

// placeholderDesktop provides a clean, working native desktop shell window
// before Track 2's full Liquid Glass desktop UI is merged. It exercises the
// full dual-form lifecycle: AlwaysOnTop, KeepInTrayOnClose, OnFocusAgent,
// and live snapshot updates.
type placeholderDesktop struct {
	provider   model.SnapshotProvider
	store      *config.Store
	onSettings func(model.Settings)

	win       *mygo.Window
	settings  model.Settings
	snap      model.AppSnapshot
	focusConv string
	focusAg   string
	quitting  bool

	stopOnce sync.Once
	cancel   func()
	doneCh   chan struct{}
}

func newPlaceholderDesktop(provider model.SnapshotProvider, store *config.Store, onSettings func(model.Settings)) *placeholderDesktop {
	s := store.Get()
	d := &placeholderDesktop{
		provider:   provider,
		store:      store,
		onSettings: onSettings,
		settings:   s,
		snap:       provider.Snapshot(),
		doneCh:     make(chan struct{}),
	}

	d.win = mygo.NewWindow(mygo.WindowOptions{
		Title:       "Antigravity Monitor",
		Width:       780,
		Height:      520,
		MinWidth:    560,
		MinHeight:   380,
		Hidden:      true,
		AlwaysOnTop: s.AlwaysOnTop,
		Vibrancy:    mygo.VibrancyWindow,
		Content:     ui.View(d.view),
	})

	d.win.OnClose(func(e *mygo.CloseEvent) {
		if !d.quitting && d.settings.KeepInTrayOnClose {
			e.PreventDefault()
			d.win.Hide()
		}
	})

	ch, cancel := provider.Subscribe()
	d.cancel = cancel
	go func() {
		for {
			select {
			case <-d.doneCh:
				return
			case snap, ok := <-ch:
				if !ok {
					return
				}
				mygo.RunOnMain(func() {
					d.snap = snap
					if d.win != nil && d.win.IsVisible() {
						d.win.Invalidate()
					}
				})
			}
		}
	}()

	return d
}

func (d *placeholderDesktop) Show() {
	if d.win == nil {
		return
	}
	d.win.Show()
	d.win.Focus()
	d.win.Invalidate()
}

func (d *placeholderDesktop) FocusAgent(convID, agentID string) {
	d.focusConv = convID
	d.focusAg = agentID
	d.Show()
}

func (d *placeholderDesktop) ApplySettings(s model.Settings) {
	d.settings = s
	if d.win != nil {
		d.win.SetAlwaysOnTop(s.AlwaysOnTop)
		d.win.Invalidate()
	}
}

func (d *placeholderDesktop) SetQuitting(q bool) {
	d.quitting = q
}

func (d *placeholderDesktop) Close() {
	d.stopOnce.Do(func() {
		if d.cancel != nil {
			d.cancel()
		}
		close(d.doneCh)
	})
}

func (d *placeholderDesktop) view(c *ui.Context) {
	t := c.Theme()
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	} else {
		c.Root().Background(t.Background)
	}

	conv, hasConv := d.snap.FindConversation(d.focusConv)
	scopeLabel := "整棵会话树"
	if hasConv && d.focusAg != "" {
		if ag, ok := conv.FindAgent(d.focusAg); ok {
			scopeLabel = fmt.Sprintf("%s · %s", conv.Title, ag.Name)
		}
	} else if hasConv {
		scopeLabel = conv.Title
	}

	ui.Column(c).Fill().Padding(28).Gap(16).Children(func() {
		ui.Text(c, "Antigravity Monitor · 主窗口壳层").FontSize(20).Bold()
		ui.Text(c, "Track 3 双形态生命周期壳层已就绪（合并 Track 2 后将无缝替换为完整桌面端 Liquid Glass 面板）。").
			FontSize(13).TextColor(t.TextMuted)

		ui.Column(c).Padding(16).Radius(14).Background(t.Surface).Border(1, t.Border).Gap(8).Children(func() {
			ui.Textf(c, "当前聚焦：%s", scopeLabel).FontSize(14).Bold()
			if hasConv {
				ui.Textf(c, "本轮费用 %s · 会话总计 %s · 加权命中 %s · 均速 %s t/s",
					tray.FormatUSD(conv.TreeMetrics.TurnCostUSD),
					tray.FormatUSD(conv.TreeMetrics.SessionCostUSD),
					tray.FormatPct(conv.TreeMetrics.WeightedCacheHitRate),
					tray.FormatTPS(conv.TreeMetrics.WeightedDecodeTPS),
				).FontSize(13).FontFeatures("tnum").TextColor(t.TextMuted)
			}
		})

		ui.Spacer(c)

		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			keep := d.settings.KeepInTrayOnClose
			if ui.Switch(c.Key("desk-keep"), &keep).Label("关闭退守托盘").Changed() {
				if next, err := d.store.Update(func(s *model.Settings) { s.KeepInTrayOnClose = keep }); err == nil {
					d.ApplySettings(next)
					if d.onSettings != nil {
						d.onSettings(next)
					}
				}
			}
			ui.Text(c, "关闭主窗时退守系统托盘").FontSize(12).TextColor(t.TextMuted)
			ui.Spacer(c)
			if ui.Button(c, "隐藏到托盘").Clicked() {
				d.win.Hide()
			}
		})
	})
}
