package e2e_test

import (
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/source"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

func TestBaseline_LiveMonitor900x590_LightDarkZHEn(t *testing.T) {
	fx := seedBaselineMultiRangeFixture(t)
	prov, err := source.NewProvider(source.Options{
		RootDir:        fx.RootDir,
		MaxOpenDBs:     4,
		DisableWatcher: true,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	app := desktop.New(prov, desktop.Options{
		ThemeMode: theme.ModeLight,
	})
	defer app.Close()

	tt := newHeadlessTester(app)

	// 1. Light / ZH Live Monitor
	mustHaveText(t, tt, "实时架构与 WAL 监控联调")
	mustHaveText(t, tt, "上下文窗口")
	verifyPNGFrame(t, tt, "baseline_live_light_zh.png")

	// 2. Open Settings popover via footer gear and toggle EN
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click settings gear: %v", err)
	}
	stepUI(tt, 200*time.Millisecond)
	mustHaveText(t, tt, "语言")
	mustHaveText(t, tt, "深色")
	if err := tt.Click("EN"); err != nil {
		t.Fatalf("click EN language pill: %v", err)
	}
	stepUI(tt, 250*time.Millisecond)
	mustHaveText(t, tt, "Settings")
	if err := tt.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	stepUI(tt, 200*time.Millisecond)
	mustHaveText(t, tt, "Context Window")
	verifyPNGFrame(t, tt, "baseline_live_light_en.png")

	// 3. Switch to Dark / EN and Dark / ZH
	app.SetThemeMode(theme.ModeDark)
	stepUI(tt, 250*time.Millisecond)
	mustHaveText(t, tt, "Context Window")
	verifyPNGFrame(t, tt, "baseline_live_dark_en.png")

	app.SetLanguage("zh")
	stepUI(tt, 250*time.Millisecond)
	mustHaveText(t, tt, "上下文窗口")
	verifyPNGFrame(t, tt, "baseline_live_dark_zh.png")
}
