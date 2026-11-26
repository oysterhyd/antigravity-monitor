package tray

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/mock"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

func TestTrayAgentTreeLayoutAndCopy(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	snap := p.Snapshot()
	// Clone enough subagents (12) to verify bounded scroll keeps footer visible
	// and agent rows stay cleanly full-width aligned.
	conv := snap.Conversations[0]
	baseSubs := append([]model.AgentNode(nil), conv.Subagents...)
	for len(conv.Subagents) < 12 {
		src := baseSubs[len(conv.Subagents)%len(baseSubs)]
		src.ID = fmt.Sprintf("sub-extra-%d", len(conv.Subagents))
		src.Name = fmt.Sprintf("Codebase Explorer %d", len(conv.Subagents)+1)
		conv.Subagents = append(conv.Subagents, src)
	}
	conv.Root.Metrics.SessionCostUSD = 29.353
	conv.SubagentsCostUSD = 14.697
	conv.TreeMetrics.SessionCostUSD = 44.050
	snap.Conversations[0] = conv

	ctl := New(Options{})
	ctl.state = NewState(model.DefaultSettings(), snap)
	ctl.state.SetTab(TabAgents)

	tt := ui.NewTester(ctl.buildView, flyoutWidth, 540)
	tt.Advance(350 * time.Millisecond)

	// Hero labels: "主 Agent" (67%) & "子 Agent" (33%), no "子 Agent 合计" or "总 $...".
	if !tt.HasText("主 Agent") || !tt.HasText("子 Agent") {
		t.Fatalf("expected 主 Agent and 子 Agent hero labels; got %q", tt.Texts())
	}
	if tt.HasText("子 Agent 合计") || tt.HasText("总 $") {
		t.Fatalf("unexpected legacy sub-agent total copy; got %q", tt.Texts())
	}
	if !tt.HasText("67%") || !tt.HasText("33%") {
		t.Fatalf("expected 67%% and 33%% cost shares; got %q", tt.Texts())
	}

	// Clean section headers without parenthetical subtitles.
	if !tt.HasText("费用结构") || !tt.HasText("节点明细") {
		t.Fatalf("expected clean section headers; got %q", tt.Texts())
	}
	if tt.HasText("主 vs 子") || tt.HasText("点击联动主窗") {
		t.Fatalf("parenthetical small text should be removed; got %q", tt.Texts())
	}

	// Footer button must remain visible inside the bounded flyout even with 12+ subagents.
	btnRect, ok := tt.Find("打开主面板")
	if !ok {
		t.Fatalf("expected footer button 打开主面板 to remain visible; got %q", tt.Texts())
	}
	if btnRect.Y+btnRect.H > 540 {
		t.Fatalf("footer button pushed out of flyout bounds: %+v", btnRect)
	}

	// Verify agent row spans near full width (not centered/squished).
	if dir := os.Getenv("TRAY_UI_PREVIEW_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir preview dir: %v", err)
		}
		img := tt.Image()
		f, err := os.Create(filepath.Join(dir, "tray_agents_light_zh.png"))
		if err != nil {
			t.Fatalf("create preview: %v", err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatalf("encode preview: %v", err)
		}
	}
}

func TestTraySessionPopoverAlignment(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	snap := p.Snapshot()
	// Give conversations distinct title lengths to verify consistent left/right flush alignment.
	titles := []string{
		"Agent 界面 UI 优化",
		"Antigravity Token 实时统计面板联调",
		"Data Engine Track 1 Dev",
	}
	for i := range snap.Conversations {
		if i < len(titles) {
			snap.Conversations[i].Title = titles[i]
		}
	}

	ctl := New(Options{})
	ctl.state = NewState(model.DefaultSettings(), snap)
	ctl.state.SessionMenuOpen = true

	tt := ui.NewTester(ctl.buildView, flyoutWidth, 540)
	tt.Advance(250 * time.Millisecond)

	var firstX float32 = -1
	for i, title := range titles {
		if i >= len(snap.Conversations) {
			break
		}
		r, ok := tt.Find(title)
		if !ok {
			t.Fatalf("expected conversation title %q in tray popover; texts=%q", title, tt.Texts())
		}
		if firstX < 0 {
			firstX = r.X
		} else if dx := r.X - firstX; dx < -2 || dx > 2 {
			t.Fatalf("conversation %q misaligned horizontally: x=%.1f vs firstX=%.1f", title, r.X, firstX)
		}
	}

	if dir := os.Getenv("TRAY_UI_PREVIEW_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir preview dir: %v", err)
		}
		img := tt.Image()
		f, err := os.Create(filepath.Join(dir, "tray_popover_light_zh.png"))
		if err != nil {
			t.Fatalf("create preview: %v", err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatalf("encode preview: %v", err)
		}
	}
}

type stubTrayHistory struct{}

func (s *stubTrayHistory) Refresh(_ context.Context) error { return nil }
func (s *stubTrayHistory) ConsumptionSince(_ time.Time) history.PoolConsumption {
	return history.PoolConsumption{}
}
func (s *stubTrayHistory) Query(r history.Range, _ time.Time) history.QueryResult {
	return history.QueryResult{
		Range:           r,
		TotalTokens:     751_800_000,
		OutputTokens:    5_900_000,
		InputTokens:     42_000_000,
		CacheReadTokens: 703_900_000,
		TotalCostUSD:    224.16,
		AvgCostUSD:      32.02,
		CacheSavedUSD:   926.47,
		CacheHitRate:    92.5,
		SessionCount:    37,
		RequestCount:    3744,
		Buckets: []history.BucketSummary{
			{Label: "11/18", CacheReadTokens: 15_000_000, InputTokens: 1_200_000, OutputTokens: 300_000, TotalTokens: 16_500_000, TotalCostUSD: 8.10, CacheHitPct: 91.2},
			{Label: "11/19", CacheReadTokens: 52_000_000, InputTokens: 3_400_000, OutputTokens: 700_000, TotalTokens: 56_100_000, TotalCostUSD: 19.40, CacheHitPct: 92.4},
			{Label: "11/20", CacheReadTokens: 98_000_000, InputTokens: 6_200_000, OutputTokens: 1_100_000, TotalTokens: 105_300_000, TotalCostUSD: 34.80, CacheHitPct: 93.1},
			{Label: "11/21", CacheReadTokens: 82_000_000, InputTokens: 5_100_000, OutputTokens: 900_000, TotalTokens: 88_000_000, TotalCostUSD: 28.50, CacheHitPct: 92.0},
			{Label: "11/22", CacheReadTokens: 145_000_000, InputTokens: 8_900_000, OutputTokens: 1_300_000, TotalTokens: 155_200_000, TotalCostUSD: 44.10, CacheHitPct: 93.5},
			{Label: "11/23", CacheReadTokens: 126_000_000, InputTokens: 7_800_000, OutputTokens: 1_100_000, TotalTokens: 134_900_000, TotalCostUSD: 33.53, CacheHitPct: 92.8},
			{Label: "11/24", CacheReadTokens: 185_200_000, InputTokens: 12_200_000, OutputTokens: 2_100_000, TotalTokens: 199_500_000, TotalCostUSD: 55.73, CacheHitPct: 93.8},
		},
		TopSessions: []history.SessionSummary{
			{ConvID: "conv-monitor", Title: "Antigravity Token 实时统计面板", PrimaryPool: metrics.PoolGemini, TotalTokens: 171_600_000, CostUSD: 44.05},
			{ConvID: "conv-payments", Title: "Project Overview And Improvement", PrimaryPool: metrics.PoolClaudeGPT, TotalTokens: 124_200_000, CostUSD: 24.17},
			{ConvID: "conv-sql", Title: "作业内容梳理与分析", PrimaryPool: metrics.PoolGemini, TotalTokens: 46_700_000, CostUSD: 16.42},
		},
	}
}

func saveTrayPreview(t *testing.T, tt *ui.Tester, filename string) {
	t.Helper()
	dir := os.Getenv("TRAY_UI_PREVIEW_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir preview dir: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		t.Fatalf("create preview %s: %v", filename, err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatalf("encode preview %s: %v", filename, err)
	}
}

func TestTrayHistoryView(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	now := time.Date(2026, 11, 24, 15, 30, 0, 0, time.Local)

	var openedView ViewMode
	openedCalled := false
	var focusedConv string

	ctl := New(Options{
		Provider: p,
		History:  &stubTrayHistory{},
		Now:      func() time.Time { return now },
		Callbacks: Callbacks{
			OnOpenView: func(v ViewMode) {
				openedView = v
				openedCalled = true
			},
			OnFocusAgent: func(convID, _ string) {
				focusedConv = convID
			},
		},
	})

	tt := ui.NewTester(ctl.buildView, flyoutWidth, 510)
	tt.Advance(250 * time.Millisecond)

	// 1. Switch to History view via footer BarChart icon.
	if err := tt.Click("历史消耗统计"); err != nil {
		t.Fatalf("click tray history nav: %v", err)
	}
	tt.Advance(250 * time.Millisecond)
	if ctl.state.View != ViewHistory {
		t.Fatalf("expected ViewHistory, got %v", ctl.state.View)
	}
	for _, want := range []string{"历史消耗统计", "751.8M", "$224.16", "$926.47", "高消耗会话 Top 3", "Antigravity Token 实时统计面板"} {
		if !tt.HasText(want) {
			t.Fatalf("history view missing %q; got %q", want, tt.Texts())
		}
	}
	saveTrayPreview(t, tt, "tray_history_light_zh.png")

	// Clicking "打开主面板" while in History view forwards ViewHistory to OnOpenView.
	if err := tt.Click("打开主面板"); err != nil {
		t.Fatalf("click open desktop: %v", err)
	}
	if !openedCalled || openedView != ViewHistory {
		t.Fatalf("expected OnOpenView(ViewHistory), got called=%v view=%v", openedCalled, openedView)
	}

	// Switch range to "今日" and verify hourly average label.
	if err := tt.Click("今日"); err != nil {
		t.Fatalf("click 今日 range: %v", err)
	}
	tt.Advance(200 * time.Millisecond)
	if ctl.state.HistoryRange != history.RangeToday {
		t.Fatalf("expected RangeToday, got %v", ctl.state.HistoryRange)
	}
	if !tt.HasText("时均") {
		t.Fatalf("expected hourly avg label 时均; got %q", tt.Texts())
	}

	// Clicking a Top 3 session calls OnFocusAgent with that conversation ID.
	if err := tt.Click("作业内容梳理与分析"); err != nil {
		t.Fatalf("click top session row: %v", err)
	}
	if focusedConv != "conv-sql" {
		t.Fatalf("expected focus conv-sql, got %q", focusedConv)
	}

	// 2. Dark EN visual check for History.
	ctl.state.Settings.ThemeMode = "dark"
	ctl.state.Settings.Language = "en"
	ctl.state.SetHistoryRange(history.Range7D)
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	if !tt.HasText("Usage History") || !tt.HasText("Top 3 Sessions") {
		t.Fatalf("expected EN history labels; got %q", tt.Texts())
	}
	saveTrayPreview(t, tt, "tray_history_dark_en.png")

	// Clicking the header Back button returns cleanly to Live view.
	if err := tt.Click("← Live View"); err != nil {
		t.Fatalf("click back to live: %v", err)
	}
	tt.Advance(150 * time.Millisecond)
	if ctl.state.View != ViewLive {
		t.Fatalf("expected back button to return to ViewLive, got %v", ctl.state.View)
	}
	// ToggleView idempotency check.
	ctl.state.ToggleView(ViewHistory)
	ctl.state.ToggleView(ViewHistory)
	if ctl.state.View != ViewLive {
		t.Fatalf("expected ToggleView x2 to return to ViewLive, got %v", ctl.state.View)
	}
}
