package tray

import (
	"fmt"
	"log"
	"runtime"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/config"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	appupdater "github.com/oysterhyd/antigravity-monitor/internal/updater"
)

// Callbacks connects tray actions to the host application shell (e.g. Track 2's
// desktop window once merged in cmd/monitor).
type Callbacks struct {
	OnOpenDesktop    func()
	OnFocusAgent     func(convID, agentID string)
	OnSettingsChange func(model.Settings)
	OnQuit           func()
}

// Options configures a tray Controller.
type Options struct {
	Provider    model.SnapshotProvider
	Store       *config.Store
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
	tr, err := mygo.NewTray(mygo.TrayOptions{
		ID:              "antigravity-monitor.tray",
		Icon:            TrayIconPNG(darkIcon),
		IconIsTemplate:  runtime.GOOS == "darwin",
		Title:           ctl.macTrayTitle(),
		MonospacedTitle: true,
		ToolTip:         TrayTooltip(ctl.state.Snapshot, ctl.state.SelectedConvID),
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

	ctl.followItem = &mygo.MenuItem{
		ID:      "auto-follow",
		Label:   "自动跟随活跃对话",
		Type:    mygo.MenuItemCheckbox,
		Checked: ctl.state.Settings.AutoFollowActive,
		Click: func(it *mygo.MenuItem, _ *mygo.Window) {
			checked := it.IsChecked()
			ctl.updateSettings(func(s *model.Settings) {
				s.AutoFollowActive = checked
			})
		},
	}

	ctl.menu = mygo.NewMenu([]*mygo.MenuItem{
		{
			ID:    "toggle-flyout",
			Label: "打开实时弹层",
			Click: func(*mygo.MenuItem, *mygo.Window) { ctl.ToggleFlyout() },
		},
		{
			ID:    "open-desktop",
			Label: "打开主面板",
			Click: func(*mygo.MenuItem, *mygo.Window) { ctl.openDesktop() },
		},
		mygo.Separator(),
		ctl.followItem,
		appupdater.MenuItem(),
		mygo.Separator(),
		{
			ID:    "quit",
			Label: "退出 Antigravity Monitor",
			Click: func(*mygo.MenuItem, *mygo.Window) {
				if ctl.opts.Callbacks.OnQuit != nil {
					ctl.opts.Callbacks.OnQuit()
					return
				}
				mygo.App.Quit()
			},
		},
	})

	if runtime.GOOS == "linux" {
		ctl.tray.SetMenu(ctl.menu)
	} else {
		ctl.tray.OnClick(ctl.ToggleFlyout)
		ctl.tray.OnRightClick(func() {
			ctl.HideFlyout()
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
		ctl.state.SessionMenuOpen = false
		ctl.state.SettingsOpen = false
		ctl.state.HoverDotCol = -1
	})
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
		applyThemeSource(s.ThemeMode)
		if ctl.followItem != nil {
			ctl.followItem.SetChecked(s.AutoFollowActive)
		}
		ctl.dotMatrix.markDirty()
		ctl.syncTrayMetadata()
		if ctl.flyout != nil {
			ctl.flyout.Invalidate()
		}
	})
}

// Close unsubscribes from snapshots and destroys the system tray icon.
func (ctl *Controller) Close() {
	ctl.stopOnce.Do(func() {
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
					ctl.dotMatrix.markDirty()
					ctl.syncTrayMetadata()
					if ctl.flyout != nil {
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
	ctl.dotMatrix.markDirty()
	if ctl.flyout != nil {
		ctl.flyout.Invalidate()
	}
	if ctl.opts.Callbacks.OnSettingsChange != nil {
		ctl.opts.Callbacks.OnSettingsChange(next)
	}
}

func (ctl *Controller) openDesktop() {
	ctl.HideFlyout()
	if ctl.opts.Callbacks.OnOpenDesktop != nil {
		ctl.opts.Callbacks.OnOpenDesktop()
	}
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

func (ctl *Controller) syncTrayMetadata() {
	if ctl.tray == nil {
		return
	}
	ctl.tray.SetToolTip(TrayTooltip(ctl.state.Snapshot, ctl.state.SelectedConvID))
	if runtime.GOOS == "darwin" {
		ctl.tray.SetTitle(ctl.macTrayTitle())
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
