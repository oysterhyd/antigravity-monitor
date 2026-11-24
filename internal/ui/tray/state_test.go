package tray

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/mock"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

func TestStateAutoFollowAndManualPin(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	snap := p.Snapshot()
	st := NewState(model.DefaultSettings(), snap)
	if st.SelectedConvID != snap.ActiveConversationID {
		t.Fatalf("expected initial selection %q, got %q", snap.ActiveConversationID, st.SelectedConvID)
	}

	// Manual switch pins conversation and disables auto-follow.
	secondID := snap.Conversations[1].ID
	st.SelectConversation(secondID, true)
	if st.SelectedConvID != secondID {
		t.Fatalf("expected %q, got %q", secondID, st.SelectedConvID)
	}
	if st.Settings.AutoFollowActive {
		t.Fatalf("manual selection must disable AutoFollowActive")
	}

	// Incoming snapshot with another ActiveConversationID must NOT steal focus when pinned.
	snap2 := snap
	snap2.ActiveConversationID = snap.Conversations[0].ID
	st.ApplySnapshot(snap2)
	if st.SelectedConvID != secondID {
		t.Fatalf("pinned conversation should stay %q, got %q", secondID, st.SelectedConvID)
	}

	// Re-enabling AutoFollowActive follows ActiveConversationID again.
	nextSettings := st.Settings
	nextSettings.AutoFollowActive = true
	st.ApplySettings(nextSettings)
	if st.SelectedConvID != snap2.ActiveConversationID {
		t.Fatalf("expected auto-follow to jump to %q, got %q", snap2.ActiveConversationID, st.SelectedConvID)
	}
}

func TestDotSeriesAllTabsAndHoverSummary(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	snap := p.Snapshot()
	st := NewState(model.DefaultSettings(), snap)

	conv, ok := st.ActiveConversation()
	if !ok {
		t.Fatalf("expected active conversation")
	}

	for _, tab := range []Tab{TabOverview, TabPerf, TabAgents} {
		a, b, reqs := ComputeDotSeries(conv, tab)
		nonZero := 0
		for c := 0; c < DotCols; c++ {
			if a[c] < 0 || a[c] > DotRows || b[c] < 0 || b[c] > DotRows {
				t.Fatalf("tab %d col %d out of bounds: a=%v b=%v", tab, c, a[c], b[c])
			}
			if a[c] > 0 || b[c] > 0 {
				nonZero++
			}
		}
		if nonZero == 0 {
			t.Fatalf("tab %d expected active dot columns", tab)
		}
		if reqs[DotCols-1] == nil {
			t.Fatalf("tab %d expected latest request aligned to rightmost column", tab)
		}
	}

	// Hovering rightmost column produces formatted pill matching prototype.html.
	req, label := st.HoverSummary(DotCols - 1)
	if req == nil {
		t.Fatalf("expected request at rightmost column")
	}
	if !strings.Contains(label, "命中") || !strings.Contains(label, "t/s") || !strings.HasPrefix(label, "#") {
		t.Fatalf("unexpected hover pill format: %q", label)
	}

	// Hovering empty column before data returns nil.
	if r, txt := st.HoverSummary(0); r != nil || txt != "" {
		t.Fatalf("expected empty hover summary at col 0, got %v %q", r, txt)
	}
}

func TestStepDotMatrixConvergesToIdle(t *testing.T) {
	p := mock.New(0)
	defer p.Close()

	st := NewState(model.DefaultSettings(), p.Snapshot())
	st.SetTab(TabPerf)
	// Force delta to test animation convergence.
	st.DotLevelsA = [DotCols]float32{}
	st.DotLevelsB = [DotCols]float32{}

	animating := true
	for i := 0; i < 240 && animating; i++ {
		animating = st.StepDotMatrix(1.0 / 60.0)
	}
	if animating {
		t.Fatalf("expected dot-matrix smoothing to settle within 4 seconds")
	}
	if st.DotLevelsA[DotCols-1] != st.DotTargetsA[DotCols-1] {
		t.Fatalf("expected settled value %v, got %v", st.DotTargetsA[DotCols-1], st.DotLevelsA[DotCols-1])
	}
}

func TestHoverColumnMapping(t *testing.T) {
	if got := HoverColumn(-1, 300); got != -1 {
		t.Fatalf("expected -1 for negative x, got %d", got)
	}
	if got := HoverColumn(0, 300); got != 0 {
		t.Fatalf("expected 0 at left edge, got %d", got)
	}
	if got := HoverColumn(299.9, 300); got != DotCols-1 {
		t.Fatalf("expected %d at right edge, got %d", DotCols-1, got)
	}
	if got := HoverColumn(300, 300); got != -1 {
		t.Fatalf("expected -1 at width boundary, got %d", got)
	}
}

func TestTrayIconPNGIsValid32x32(t *testing.T) {
	for _, dark := range []bool{false, true} {
		raw := TrayIconPNG(dark)
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("TrayIconPNG(dark=%v) invalid PNG: %v", dark, err)
		}
		b := img.Bounds()
		if b.Dx() != 32 || b.Dy() != 32 {
			t.Fatalf("expected 32x32 icon, got %dx%d", b.Dx(), b.Dy())
		}
	}
}

func TestFormattersAndContextWarn(t *testing.T) {
	if got := FormatUSD(0.0484); got != "$0.048" {
		t.Fatalf("FormatUSD got %q", got)
	}
	if got := FormatTokensK(48200); got != "48.2K" {
		t.Fatalf("FormatTokensK got %q", got)
	}
	if got := FormatTokensK(1250000); got != "1.2M" {
		t.Fatalf("FormatTokensK M got %q", got)
	}
	if got := FormatPct(0.942); got != "94.2%" {
		t.Fatalf("FormatPct got %q", got)
	}
	if got := FormatSec(1620 * time.Millisecond); got != "1.62s" {
		t.Fatalf("FormatSec got %q", got)
	}
	if got := ContextWarn(0.82, 0.80); got != ContextWarning {
		t.Fatalf("expected ContextWarning, got %v", got)
	}
	if got := ContextWarn(0.65, 0.80); got != ContextElevated {
		t.Fatalf("expected ContextElevated, got %v", got)
	}
	if got := ContextWarn(0.90, 0); got != ContextNormal {
		t.Fatalf("expected disabled threshold (0) to return ContextNormal, got %v", got)
	}
}
