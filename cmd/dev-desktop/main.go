package main

import (
	"flag"
	"log"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/mock"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

func main() {
	themeFlag := flag.String("theme", theme.ModeSystem, "initial theme mode: system, light, or dark")
	tickFlag := flag.Duration("tick", 2800*time.Millisecond, "mock telemetry stream interval (0 to disable)")
	flag.Parse()

	provider := mock.New(*tickFlag)
	defer provider.Close()

	view := desktop.New(provider, desktop.Options{
		ThemeMode:      *themeFlag,
		ShowDevToolbar: true,
		LiveRunning:    *tickFlag > 0,
		OnTriggerBurst: provider.TriggerBurst,
	})
	defer view.Close()

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:          "Antigravity Monitor",
			Width:          980,
			Height:         700,
			MinWidth:       780,
			MinHeight:      580,
			TitleBarStyle:  mygo.TitleBarHidden,
			TitleBarHeight: 36,
			Vibrancy:       mygo.VibrancyGlass,
			Content:        ui.View(view.View),
		})
		view.SetWindow(win)
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
