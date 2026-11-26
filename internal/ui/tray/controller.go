package tray

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/config"
	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/i18n"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/quota"
	appupdater "github.com/oysterhyd/antigravity-monitor/internal/updater"
)

// Callbacks connects tray actions to the host application shell (e.g. Track 2's
// desktop window once merged in cmd/monitor).
type Callbacks struct {
	OnOpenDesktop     func()
	OnOpenView        func(ViewMode)
	OnFocusAgent      func(convID, agentID string)
	OnSettingsChange  func(model.Settings)
	OnQuotaViewActive func(active bool)
	OnQuotaRefresh    func()
	OnHistoryViewOpen func()
	OnQuit            func()
}

// Options configures a tray Controller.
type Options struct {
	Provider    model.SnapshotProvider
	History     history.Aggregator
	Quota       quota.Tracker
	Store       *config.Store
	Now         func() time.Time
	Callbacks   Callbacks
	ShowOnStart bool
}

// Controller manages the system tray icon, right-click menu, Pulse frosted-glass
// flyout window, live snapshot subscription, and settings persistence.
// All UI mutations execute on the mygo main thread.
type Controller struct {
	opts      Options
	state     *State
	dotMatrix *dotMatrixRenderer

	tray       *mygo.Tray
	flyout     *mygo.Window
	menu       *mygo.Menu
	followItem *mygo.MenuItem
	toolTip    string
	title      string

	// Lightweight short-lived cache for tray History view queries.
	histCached      history.QueryResult
	histCachedAt    time.Time
	histCachedRng   history.Range
	histHasCache    bool
	quotaRefreshing atomic.Bool

	stopOnce sync.Once
	cancel   func()
	doneCh   chan struct{}
}

// New constructs a tray Controller. Call Start() inside mygo.App.WhenReady.
func New(opts Options) *Controller {
	settings := model.DefaultSettings()
	if opts.Store != nil {
		settings = opts.Store.Get()
	}
	var initial model.AppSnapshot
	if opts.Provider != nil {
		initial = opts.Provider.Snapshot()
	}
	return &Controller{
		opts:      opts,
		state:     NewState(settings, initial),
		dotMatrix: newDotMatrixRenderer(),
		doneCh:    make(chan struct{}),
	}
}

// Start creates the native system tray icon, registers menus and callbacks,
// applies the persisted theme, and subscribes to SnapshotProvider updates.
// Must be invoked from mygo.App.WhenReady (main thread).
func (ctl *Controller) Start() error {
	applyThemeSource(ctl.state.Settings.ThemeMode)

	darkIcon := mygo.Theme.IsDark()
	ctl.toolTip = TrayTooltipLang(ctl.state.Snapshot, ctl.state.SelectedConvID, ctl.state.Settings.Language)
	ctl.title = ctl.macTrayTitle()
	tr, err := mygo.NewTray(mygo.TrayOptions{
		ID:              "antigravity-monitor.tray",
		Icon:            TrayIconPNG(darkIcon),
		IconIsTemplate:  runtime.GOOS == "darwin",
		Title:           ctl.title,
		MonospacedTitle: true,
		ToolTip:         ctl.toolTip,
	})
	if err != nil {
		return fmt.Errorf("create system tray: %w", err)
	}
	ctl.tray = tr

	ctl.tray.OnAppearanceChange(func() {
		if ctl.tray != nil {
			_ = ctl.tray.SetIcon(TrayIconPNG(ctl.tray.Dark()), runtime.GOOS == "darwin")
		}
	})

	ctl.rebuildMenu()

	if runtime.GOOS == "linux" {
		ctl.tray.SetMenu(ctl.menu)
	} else {
		ctl.tray.OnClick(ctl.ToggleFlyout)
		ctl.tray.OnRightClick(func() {
			ctl.HideFlyout()
			ctl.rebuildMenu()
			ctl.tray.SetMenu(ctl.menu)
			defer ctl.tray.SetMenu(nil)
			ctl.tray.PopUpMenu()
		})
	}

	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		ctl.Close()
	})

	ctl.startSubscription()

	if ctl.opts.ShowOnStart {
		ctl.ShowFlyout()
	}
	return nil
}

// ToggleFlyout opens the Pulse tray flyout anchored to the system tray icon,
// or closes it if currently open.
func (ctl *Controller) ToggleFlyout() {
	if ctl.flyout != nil {
		ctl.HideFlyout()
		return
	}
	ctl.ShowFlyout()
}

// ShowFlyout opens the frosted-glass flyout anchored next to the tray icon.
func (ctl *Controller) ShowFlyout() {
	if ctl.tray == nil {
		return
	}
	if ctl.flyout != nil {
		ctl.flyout.Show()
		ctl.flyout.Focus()
		return
	}
	ctl.dotMatrix.markDirty()
	ctl.histHasCache = false
	ctl.flyout = mygo.NewFlyout(mygo.FlyoutOptions{
		Tray:         ctl.tray,
		Placement:    mygo.PlacementBottom,
		Gap:          6,
		Width:        flyoutWidth,
		Height:       416,
		FitContent:   true,
		MinWidth:     flyoutWidth,
		MaxWidth:     flyoutWidth,
		MaxHeight:    580,
		Focusable:    true,
		Shadow:       true,
		Popover:      true,
		Vibrancy:     mygo.VibrancyGlass,
		CornerRadius: flyoutRadius,
		Content:      ui.View(ctl.buildView),
	})
	ctl.flyout.OnClosed(func() {
		ctl.flyout = nil
		ctl.dotMatrix.release()
		ctl.state.SessionMenuOpen = false
		ctl.state.SettingsOpen = false
		ctl.state.HoverDotCol = -1
		ctl.state.HoverHistIdx = -1
		ctl.notifyViewActivity()
	})
	ctl.notifyViewActivity()
}

// InvalidateFlyout requests a repaint of the tray flyout if it is currently open.
func (ctl *Controller) InvalidateFlyout() {
	mygo.RunOnMain(func() {
		if ctl.flyout != nil {
			ctl.flyout.Invalidate()
		}
	})
}

const trayPinSlot = "tray"

func (ctl *Controller) pinConversation(convID string) {
	if ctl.opts.Provider == nil {
		return
	}
	if h, ok := ctl.opts.Provider.(model.ConversationHydrator); ok {
		h.PinConversation(trayPinSlot, convID)
	}
}

func (ctl *Controller) notifyViewActivity() {
	quotaActive := ctl.flyout != nil && ctl.state.View == ViewQuota
	if ctl.opts.Callbacks.OnQuotaViewActive != nil {
		ctl.opts.Callbacks.OnQuotaViewActive(quotaActive)
	}
	if ctl.flyout != nil && ctl.state.View == ViewHistory && ctl.opts.Callbacks.OnHistoryViewOpen != nil {
		ctl.opts.Callbacks.OnHistoryViewOpen()
	}
}

// HideFlyout closes the active flyout window if open.
func (ctl *Controller) HideFlyout() {
	if ctl.flyout != nil {
		ctl.flyout.Close()
	}
}

// ApplySettings synchronizes external settings updates (e.g. from the desktop
// window) into the tray flyout without rewriting disk.
func (ctl *Controller) ApplySettings(s model.Settings) {
	mygo.RunOnMain(func() {
		ctl.state.ApplySettings(s)
		if s.AutoFollowActive {
			ctl.pinConversation("")
		}
		applyThemeSource(s.ThemeMode)
		if ctl.followItem != nil {
			ctl.followItem.SetChecked(s.AutoFollowActive)
		}
		if runtime.GOOS == "linux" && ctl.tray != nil {
			ctl.rebuildMenu()
			ctl.tray.SetMenu(ctl.menu)
		}
		if ctl.flyout != nil {
			ctl.dotMatrix.markDirty()
			ctl.flyout.Invalidate()
		}
		ctl.syncTrayMetadata()
	})
}

// Close unsubscribes from snapshots and destroys the system tray icon.
func (ctl *Controller) Close() {
	ctl.stopOnce.Do(func() {
		ctl.pinConversation("")
		if ctl.cancel != nil {
			ctl.cancel()
		}
		close(ctl.doneCh)
		if ctl.flyout != nil {
			ctl.flyout.Close()
			ctl.flyout = nil
		}
		if ctl.tray != nil {
			ctl.tray.Destroy()
			ctl.tray = nil
		}
	})
}

func (ctl *Controller) startSubscription() {
	if ctl.opts.Provider == nil {
		return
	}
	ch, cancel := ctl.opts.Provider.Subscribe()
	ctl.cancel = cancel
	go func() {
		for {
			select {
			case <-ctl.doneCh:
				return
			case snap, ok := <-ch:
				if !ok {
					return
				}
				mygo.RunOnMain(func() {
					ctl.state.ApplySnapshot(snap)
					ctl.syncTrayMetadata()
					if ctl.flyout != nil {
						ctl.dotMatrix.markDirty()
						ctl.flyout.Invalidate()
					}
				})
			}
		}
	}()
}

func (ctl *Controller) updateSettings(mut func(*model.Settings)) {
	var next model.Settings
	if ctl.opts.Store != nil {
		var err error
		next, err = ctl.opts.Store.Update(mut)
		if err != nil {
			log.Printf("[tray] save settings failed: %v", err)
			return
		}
	} else {
		next = ctl.state.Settings
		if mut != nil {
			mut(&next)
		}
		next = config.Normalize(next)
	}

	prevLaunch := ctl.state.Settings.LaunchAtLogin
	ctl.state.ApplySettings(next)
	if next.AutoFollowActive {
		ctl.pinConversation("")
	}
	applyThemeSource(next.ThemeMode)

	if next.LaunchAtLogin != prevLaunch {
		if err := mygo.App.SetOpenAtLogin(next.LaunchAtLogin); err != nil {
			// Unpackaged dev runs may not support login item registration; keep saved preference.
			log.Printf("[tray] SetOpenAtLogin(%v): %v", next.LaunchAtLogin, err)
		}
	}

	if ctl.followItem != nil {
		ctl.followItem.SetChecked(next.AutoFollowActive)
	}
	if runtime.GOOS == "linux" && ctl.tray != nil {
		ctl.rebuildMenu()
		ctl.tray.SetMenu(ctl.menu)
	}
	ctl.dotMatrix.markDirty()
	ctl.syncTrayMetadata()
	if ctl.flyout != nil {
		ctl.flyout.Invalidate()
	}
	if ctl.opts.Callbacks.OnSettingsChange != nil {
		ctl.opts.Callbacks.OnSettingsChange(next)
	}
}

func (ctl *Controller) openDesktop() {
	view := ctl.state.View
	ctl.HideFlyout()
	if ctl.opts.Callbacks.OnOpenView != nil {
		ctl.opts.Callbacks.OnOpenView(view)
		return
	}
	if ctl.opts.Callbacks.OnOpenDesktop != nil {
		ctl.opts.Callbacks.OnOpenDesktop()
	}
}

func (ctl *Controller) now(fallback time.Time) time.Time {
	if ctl.opts.Now != nil {
		return ctl.opts.Now()
	}
	return fallback
}

func (ctl *Controller) queryHistory(now time.Time) history.QueryResult {
	if ctl.opts.History == nil {
		return history.QueryResult{Range: ctl.state.HistoryRange}
	}
	if ctl.histHasCache && ctl.histCachedRng == ctl.state.HistoryRange && now.Sub(ctl.histCachedAt) < 2*time.Second {
		return ctl.histCached
	}
	res := ctl.opts.History.Query(ctl.state.HistoryRange, now)
	ctl.histCached = res
	ctl.histCachedRng = ctl.state.HistoryRange
	ctl.histCachedAt = now
	ctl.histHasCache = true
	return res
}

func (ctl *Controller) currentQuotaSnapshot(now time.Time) quota.QuotaSnapshot {
	if ctl.opts.Quota == nil {
		return quota.QuotaSnapshot{}
	}
	return ctl.opts.Quota.State(now)
}

func (ctl *Controller) refreshQuotaAsync() {
	if ctl.opts.Callbacks.OnQuotaRefresh != nil {
		ctl.opts.Callbacks.OnQuotaRefresh()
		return
	}
	if ctl.opts.Quota == nil {
		return
	}
	if !ctl.quotaRefreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer ctl.quotaRefreshing.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = ctl.opts.Quota.Refresh(ctx, ctl.now(time.Now()))
		mygo.RunOnMain(func() {
			if ctl.flyout != nil {
				ctl.flyout.Invalidate()
			}
		})
	}()
}

func (ctl *Controller) focusAgent(convID, agentID string) {
	ctl.HideFlyout()
	if ctl.opts.Callbacks.OnFocusAgent != nil {
		ctl.opts.Callbacks.OnFocusAgent(convID, agentID)
		return
	}
	if ctl.opts.Callbacks.OnOpenDesktop != nil {
		ctl.opts.Callbacks.OnOpenDesktop()
	}
}

func (ctl *Controller) rebuildMenu() {
	tr := i18n.GetPtr(ctl.state.Settings.Language)
	ctl.followItem = &mygo.MenuItem{
		ID:      "auto-follow",
		Label:   tr.MenuAutoFollow,
		Type:    mygo.MenuItemCheckbox,
		Checked: ctl.state.Settings.AutoFollowActive,
		Click: func(it *mygo.MenuItem, _ *mygo.Window) {
			checked := it.IsChecked()
			ctl.updateSettings(func(s *model.Settings) {
				s.AutoFollowActive = checked
			})
		},
	}

	updItem := appupdater.MenuItem()
	if updItem != nil && i18n.Normalize(ctl.state.Settings.Language) == i18n.LangEN {
		updItem.Label = tr.CheckUpdates
	}

	ctl.menu = mygo.NewMenu([]*mygo.MenuItem{
		{
			ID:    "toggle-flyout",
			Label: tr.MenuToggleFlyout,
			Click: func(*mygo.MenuItem, *mygo.Window) { ctl.ToggleFlyout() },
		},
		{
			ID:    "open-desktop",
			Label: tr.MenuOpenDesktop,
			Click: func(*mygo.MenuItem, *mygo.Window) { ctl.openDesktop() },
		},
		mygo.Separator(),
		ctl.followItem,
		updItem,
		mygo.Separator(),
		{
			ID:    "quit",
			Label: tr.MenuQuit,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				if ctl.opts.Callbacks.OnQuit != nil {
					ctl.opts.Callbacks.OnQuit()
					return
				}
				mygo.App.Quit()
			},
		},
	})
}

func (ctl *Controller) syncTrayMetadata() {
	if ctl.tray == nil {
		return
	}
	if tip := TrayTooltipLang(ctl.state.Snapshot, ctl.state.SelectedConvID, ctl.state.Settings.Language); tip != ctl.toolTip {
		ctl.tray.SetToolTip(tip)
		ctl.toolTip = tip
	}
	if runtime.GOOS == "darwin" {
		if title := ctl.macTrayTitle(); title != ctl.title {
			ctl.tray.SetTitle(title)
			ctl.title = title
		}
	}
}

func (ctl *Controller) macTrayTitle() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	return TrayTitle(ctl.state.Snapshot, ctl.state.SelectedConvID)
}

func applyThemeSource(mode string) {
	switch mode {
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}
