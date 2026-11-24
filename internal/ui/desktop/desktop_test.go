package desktop_test

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/mock"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

func TestDesktopTreeAndSingleNodeSemantics(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	app := desktop.New(p, desktop.Options{
		ThemeMode:      theme.ModeLight,
		ShowDevToolbar: true,
		OnTriggerBurst: p.TriggerBurst,
	})
	defer app.Close()

	tt := ui.NewTester(app.View, 980, 700)
	tt.Advance(250 * time.Millisecond)

	// 1. Default view is Tree View on multi-agent conversation.
	mustHaveText(t, tt, "整棵会话树")
	mustHaveText(t, tt, "上下文窗口")
	mustHaveText(t, tt, "最高水位")
	mustHaveText(t, tt, "全树本轮")
	mustHaveText(t, tt, "全树命中")
	mustHaveText(t, tt, "全树均 TTFT")
	mustHaveText(t, tt, "全树均速")
	mustHaveText(t, tt, "纯吐字速度 · 逐次请求")
	mustHaveText(t, tt, "2 进行中")
	savePreview(t, tt, "01_light_tree_curve.png")

	// 2. Click a KPI tile (TTFT) -> morphs chart heading.
	if err := tt.Click("全树均 TTFT"); err != nil {
		t.Fatalf("click TTFT tile: %v", err)
	}
	tt.Advance(350 * time.Millisecond)
	mustHaveText(t, tt, "首字时长 TTFT · 逐次请求")

	// Step scrubber backward via ‹ button.
	if err := tt.Click("上一次请求"); err != nil {
		t.Fatalf("click prev req button: %v", err)
	}
	tt.Advance(200 * time.Millisecond)
	savePreview(t, tt, "02_light_ttft_scrub.png")

	// 3. Focus a single subagent -> switches to unambiguous Single-Node semantics.
	app.FocusAgent("sub-browser")
	tt.Frame()
	tt.Advance(300 * time.Millisecond)

	if tt.HasText("最高水位") {
		t.Fatalf("single-node view should not show 最高水位; texts=%q", tt.Texts())
	}
	mustHaveText(t, tt, "本轮费用")
	mustHaveText(t, tt, "缓存命中")
	mustHaveText(t, tt, "首字时长")
	mustHaveText(t, tt, "吐字速度")

	// 4. Switch bottom card to "逐次" list view and expand a row.
	if err := tt.Click("逐次"); err != nil {
		t.Fatalf("click 逐次 segment: %v", err)
	}
	tt.Advance(300 * time.Millisecond)
	mustHaveText(t, tt, "请求 / 节点")
	mustHaveText(t, tt, "吐字 TPS")

	// Switch back to whole tree in list view so there are plenty of rows, then expand latest row.
	app.FocusAgent("all")
	tt.Frame()
	tt.Advance(250 * time.Millisecond)

	// Expand latest request (#16) by clicking its row.
	snap := p.Snapshot()
	lastReq := snap.Conversations[0].Requests[len(snap.Conversations[0].Requests)-1]
	rowLabel := "#" + itoa(lastReq.Seq) + " " + lastReq.AgentName
	if err := tt.Click(rowLabel); err != nil {
		t.Fatalf("click request row %q: %v", rowLabel, err)
	}
	tt.Advance(350 * time.Millisecond)
	savePreview(t, tt, "03_light_list_expanded.png")

	// 5. Switch to dark glass mode and inspect tree curve view.
	if err := tt.Click("曲线"); err != nil {
		t.Fatalf("click 曲线 segment: %v", err)
	}
	if err := tt.Click("浅色玻璃"); err != nil {
		t.Fatalf("click theme toggle: %v", err)
	}
	app.SelectMetric(desktop.MetricTPS)
	tt.Frame()
	tt.Advance(350 * time.Millisecond)
	mustHaveText(t, tt, "深色玻璃")
	savePreview(t, tt, "04_dark_tree_curve.png")

	// 6. Open session switcher popover and switch to single-agent idle conversation.
	if err := tt.Click("切换对话"); err != nil {
		t.Fatalf("click session switcher: %v", err)
	}
	tt.Advance(200 * time.Millisecond)
	mustHaveText(t, tt, "自动跟随活跃")
	mustHaveText(t, tt, "SQL 慢查询排查")
	savePreview(t, tt, "05_dark_session_popover.png")

	if err := tt.Click("SQL 慢查询排查"); err != nil {
		t.Fatalf("click SQL conversation: %v", err)
	}
	tt.Advance(300 * time.Millisecond)

	if tt.HasText("整棵会话树") {
		t.Fatalf("single-agent conversation should hide 整棵会话树; texts=%q", tt.Texts())
	}
	mustHaveText(t, tt, "仅主 Agent")
	mustHaveText(t, tt, "已锁定当前对话")
}

func TestDesktopBurstAndThemeSwitch(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	app := desktop.New(p, desktop.Options{
		ThemeMode:      theme.ModeLight,
		ShowDevToolbar: true,
		OnTriggerBurst: p.TriggerBurst,
	})
	defer app.Close()

	tt := ui.NewTester(app.View, 980, 700)
	beforeReqs := len(p.Snapshot().Conversations[0].Requests)

	if err := tt.Click("模拟请求"); err != nil {
		t.Fatalf("click 模拟请求: %v", err)
	}
	app.SyncSnapshot()
	tt.Frame()
	tt.Advance(900 * time.Millisecond)

	afterSnap := p.Snapshot()
	totalReqs := 0
	for _, c := range afterSnap.Conversations {
		totalReqs += len(c.Requests)
	}
	if totalReqs <= beforeReqs {
		t.Fatalf("expected burst to add a request, totalReqs=%d", totalReqs)
	}

	if err := tt.Click("浅色玻璃"); err != nil {
		t.Fatalf("click theme toggle: %v", err)
	}
	tt.Advance(200 * time.Millisecond)
	mustHaveText(t, tt, "深色玻璃")
}

func TestDesktopIdleRender(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	app := desktop.New(p, desktop.Options{
		ThemeMode: theme.ModeDark,
	})
	defer app.Close()

	app.FocusConversation("conv-sql", true)
	tt := ui.NewTester(app.View, 960, 680)
	tt.Advance(1200 * time.Millisecond)

	img := tt.Image()
	if img == nil || img.Bounds().Empty() {
		t.Fatal("expected non-empty rendered frame image")
	}
}

func TestApplySettingsAndFocusConversationAgent(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	emitted := 0
	initSettings := model.DefaultSettings()
	initSettings.ContextWarnThreshold = 0 // intentionally disabled
	app := desktop.New(p, desktop.Options{
		Settings: &initSettings,
		OnSettingsChange: func(model.Settings) {
			emitted++
		},
	})
	defer app.Close()

	if app.Settings().ContextWarnThreshold != 0 {
		t.Fatalf("expected disabled warning threshold 0 to be preserved, got %v", app.Settings().ContextWarnThreshold)
	}

	// External ApplySettings must not re-emit OnSettingsChange (prevents tray<->desktop loop).
	updated := model.DefaultSettings()
	updated.ThemeMode = theme.ModeDark
	updated.AlwaysOnTop = true
	updated.ContextWarnThreshold = 0
	app.ApplySettings(updated)
	if emitted != 0 {
		t.Fatalf("ApplySettings should not emit OnSettingsChange, got %d", emitted)
	}
	if app.Settings().ThemeMode != theme.ModeDark || !app.Settings().AlwaysOnTop || app.Settings().ContextWarnThreshold != 0 {
		t.Fatalf("unexpected applied settings: %+v", app.Settings())
	}

	// FocusConversationAgent switches conversation and pins subagent cleanly.
	app.FocusConversationAgent("conv-payments", "sub-browser-2")
	tt := ui.NewTester(app.View, 960, 680)
	tt.Advance(200 * time.Millisecond)
	mustHaveText(t, tt, "browser")
}

type staticProvider struct {
	snap model.AppSnapshot
}

func (s *staticProvider) Snapshot() model.AppSnapshot { return s.snap }
func (s *staticProvider) Subscribe() (<-chan model.AppSnapshot, func()) {
	ch := make(chan model.AppSnapshot)
	return ch, func() { close(ch) }
}
func (s *staticProvider) Close() error { return nil }

func TestSessionPopoverManyConversations(t *testing.T) {
	p := mock.New(0)
	defer p.Close()
	base := p.Snapshot()

	// Clone conversations up to 12 entries to verify bounded scroll & zero bleed-through at 900x590.
	convs := make([]model.ConversationSnapshot, 0, 12)
	for i := 0; i < 12; i++ {
		c := base.Conversations[i%len(base.Conversations)]
		c.ID = "conv-" + itoa(i+1)
		c.Title = "对话 #" + itoa(i+1) + " · " + c.Title
		convs = append(convs, c)
	}
	base.Conversations = convs
	base.ActiveConversationID = convs[0].ID

	sp := &staticProvider{snap: base}
	app := desktop.New(sp, desktop.Options{ThemeMode: theme.ModeLight})
	defer app.Close()

	tt := ui.NewTester(app.View, 900, 590)
	tt.Advance(200 * time.Millisecond)

	if err := tt.Click("切换对话"); err != nil {
		t.Fatalf("click session switcher: %v", err)
	}
	tt.Advance(200 * time.Millisecond)
	mustHaveText(t, tt, "自动跟随活跃")
	mustHaveText(t, tt, "· 12 个对话")
	savePreview(t, tt, "06_light_many_sessions_popover.png")
}

func TestContextBreakdownAndEnglishToggle(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	app := desktop.New(p, desktop.Options{ThemeMode: theme.ModeLight})
	defer app.Close()

	tt := ui.NewTester(app.View, 900, 590)
	tt.Advance(250 * time.Millisecond)

	// Focus root agent to inspect Single-Node 5-segment macOS Storage bar in Chinese.
	app.FocusAgent(model.RootAgentID)
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	mustHaveText(t, tt, "系统")
	mustHaveText(t, tt, "工具返回")
	mustHaveText(t, tt, "对话")
	mustHaveText(t, tt, "摘要")
	savePreview(t, tt, "07_light_single_ctx_breakdown_zh.png")

	// Open settings popover and switch language to English via the EN pill.
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click settings gear: %v", err)
	}
	tt.Advance(150 * time.Millisecond)
	mustHaveText(t, tt, "语言")
	if err := tt.Click("EN"); err != nil {
		t.Fatalf("click EN pill: %v", err)
	}
	tt.Advance(250 * time.Millisecond)
	mustHaveText(t, tt, "Settings")
	mustHaveText(t, tt, "Language")
	mustHaveText(t, tt, "Context Window")
	mustHaveText(t, tt, "Tool Out")

	// Close settings and switch to Tree View in English.
	if err := tt.Click("Done"); err != nil {
		t.Fatalf("click Done: %v", err)
	}
	app.FocusAgent("all")
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	mustHaveText(t, tt, "Session Tree")
	mustHaveText(t, tt, "Pure Decode TPS · Per Request")
	savePreview(t, tt, "08_light_tree_en.png")

	// Also render Dark Mode single-node English view.
	app.SetThemeMode(theme.ModeDark)
	app.FocusAgent(model.RootAgentID)
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	mustHaveText(t, tt, "Main Agent")
	savePreview(t, tt, "09_dark_single_en.png")
}

func savePreview(t *testing.T, tt *ui.Tester, filename string) {
	t.Helper()
	dir := os.Getenv("DESKTOP_UI_PREVIEW_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir preview dir: %v", err)
	}
	img := tt.Image()
	if img == nil {
		t.Fatalf("nil image for %s", filename)
	}
	f, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		t.Fatalf("create preview %s: %v", filename, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode preview %s: %v", filename, err)
	}
}

func mustHaveText(t *testing.T, tt *ui.Tester, want string) {
	t.Helper()
	if !tt.HasText(want) {
		t.Fatalf("expected text %q in view; got texts=%q", want, tt.Texts())
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
