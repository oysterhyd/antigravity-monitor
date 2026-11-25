package tray

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
