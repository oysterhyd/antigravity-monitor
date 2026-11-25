//go:build e2e

package e2e_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/quota"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

// Tier 3-1: Quota -> History -> Top 3 Session Jump -> Live Monitor
func TestTier3_QuotaToHistoryToTop3JumpToLiveMonitor(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)

	clickMust(t, env.Tester, "账号实时额度")
	if env.App.ActiveView() != desktop.ViewQuota {
		t.Fatalf("expected ViewQuota, got %v", env.App.ActiveView())
	}

	clickMust(t, env.Tester, "历史消耗统计")
	if env.App.ActiveView() != desktop.ViewHistory {
		t.Fatalf("expected ViewHistory, got %v", env.App.ActiveView())
	}

	clickMust(t, env.Tester, "今日")
	clickMust(t, env.Tester, "7 天")
	clickMust(t, env.Tester, "SQL 慢查询排查")

	if env.App.ActiveView() != desktop.ViewLive {
		t.Fatalf("expected Top 3 click to return to ViewLive, got %v", env.App.ActiveView())
	}
	mustHaveText(t, env.Tester, "SQL 慢查询排查")
}

func TestTier3_QuotaRefreshResetTrendAndAmberDot(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)

	reading := quotaReading(anchorNow, 19, 60)
	reading.Gemini.FiveHour.ResetAt = anchorNow.Add(time.Hour)
	if got := env.sampleQuota(t, anchorNow, reading); !got.HasLowQuota {
		t.Fatalf("expected low quota, got %+v", got)
	}
	clickMust(t, env.Tester, "Account Quota")
	mustHaveText(t, env.Tester, "Quota Usage Trend")
	verifyPNGFrame(t, env.Tester, "tier3_low_quota_amber.png")

	reading.Gemini.FiveHour = quota.WindowQuota{RemainingPct: 100}
	afterReset := env.sampleQuota(t, anchorNow.Add(65*time.Minute), reading)
	if afterReset.Gemini.FiveHour.RemainingPct != 100 || afterReset.HasLowQuota {
		t.Fatalf("expected reported reset to restore Gemini quota, got %+v", afterReset)
	}
	hasReset := false
	for _, pt := range afterReset.FiveHourTrend.Last24h {
		if pt.ResetEvent {
			hasReset = true
			break
		}
	}
	if !hasReset {
		t.Fatalf("expected reset marker in the five-hour trend")
	}
}

// Tier 3-3: Theme & Locale Matrix across Live, Quota, and History
func TestTier3_ThemeAndLocaleMatrixAcrossAllThreeViews(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	reading := quotaReading(anchorNow, 74, 41)
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &reading)

	combos := []struct {
		mode string
		lang string
	}{
		{theme.ModeLight, "zh"},
		{theme.ModeDark, "zh"},
		{theme.ModeDark, "en"},
		{theme.ModeLight, "en"},
	}

	for _, c := range combos {
		env.App.SetThemeMode(c.mode)
		env.App.SetLanguage(c.lang)
		stepUI(env.Tester, 200*time.Millisecond)

		env.App.SetView(desktop.ViewLive)
		stepUI(env.Tester, 150*time.Millisecond)
		verifyPNGFrame(t, env.Tester, "")

		env.App.SetView(desktop.ViewQuota)
		stepUI(env.Tester, 150*time.Millisecond)
		if c.lang == "zh" {
			mustHaveText(t, env.Tester, "账号实时额度")
		} else {
			mustHaveText(t, env.Tester, "Account Quota")
		}

		env.App.SetView(desktop.ViewHistory)
		stepUI(env.Tester, 150*time.Millisecond)
		if c.lang == "zh" {
			mustHaveText(t, env.Tester, "历史消耗统计")
		} else {
			mustHaveText(t, env.Tester, "Usage History")
		}
	}
}

// Tier 3-4: History Range, KPI Tiles & Chart Metric Bidirectional Sync
func TestTier3_HistoryRangeKPIAndMetricBidirectionalSync(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)

	clickMust(t, env.Tester, "Usage History")

	// Select Cost via KPI tile, switch ranges, verify Cost chart title remains active.
	clickMust(t, env.Tester, "Total Cost")
	mustHaveText(t, env.Tester, "Cost by Model Family")

	for _, r := range []string{"Today", "30D", "All", "7D"} {
		clickMust(t, env.Tester, r)
		mustHaveText(t, env.Tester, "Cost by Model Family")
	}

	// Switch metric via chart pill -> Cache % -> Token.
	clickMust(t, env.Tester, "Cache %")
	mustHaveText(t, env.Tester, "Prompt Cache Hit Rate")
	clickMust(t, env.Tester, "Token")
	mustHaveText(t, env.Tester, "Token Breakdown")
}

func TestTier3_IncrementalSQLiteCacheAndIndependentQuota(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
	calc, _ := metrics.NewDefaultCalculator()

	histSvc := history.NewAggregator(fx.RootDir, cachePath, calc)
	if err := histSvc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh 1: %v", err)
	}
	beforeAll := histSvc.Query(history.RangeAll, anchorNow)

	reading := quotaReading(anchorNow, 80, 80)
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", &reading)

	// Append new Gemini and Claude WAL rows after the quota sample.
	tPlus := anchorNow.Add(10 * time.Minute)
	fx.AppendWALGenRow(t, "conv-today-active", genSpec{
		GenIndex: 20, StepIndices: []int{30}, ModelSlug: "gemini-4-neon",
		InputTokens: 20000, CacheReadTokens: 80000, OutputTokens: 2000,
		Timestamp: tPlus,
	})
	fx.AppendWALGenRow(t, "conv-sql", genSpec{
		GenIndex: 20, StepIndices: []int{30}, ModelSlug: "claude-opus-5-5-high",
		InputTokens: 15000, CacheReadTokens: 45000, OutputTokens: 1500,
		Timestamp: tPlus,
	})
	fx.Checkpoint(t, "conv-today-active")
	fx.Checkpoint(t, "conv-sql")

	if err := histSvc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh 2: %v", err)
	}
	afterAll := histSvc.Query(history.RangeAll, tPlus)
	if afterAll.RequestCount != beforeAll.RequestCount+2 {
		t.Fatalf("expected +2 requests after incremental refresh, before=%d after=%d", beforeAll.RequestCount, afterAll.RequestCount)
	}

	delta := histSvc.ConsumptionSince(anchorNow.Add(1 * time.Second))
	if delta.GeminiTokens <= 0 || delta.ClaudeTokens <= 0 {
		t.Fatalf("expected positive Gemini & Claude deltas, got %+v", delta)
	}
	if got := env.Quota.State(tPlus); got.Reading != reading {
		t.Fatalf("local usage changed reported quota: %+v", got)
	}
}

// Tier 3-7: Sidebar Agent Node, Combined Tree & Session Picker Clicks from Sub-Views
func TestTier3_SidebarAgentNodeClickFromSubViews(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)

	// Subagent click from ViewQuota
	env.App.SetView(desktop.ViewQuota)
	stepUI(env.Tester, 150*time.Millisecond)
	clickMust(t, env.Tester, "browser")
	if env.App.ActiveView() != desktop.ViewLive {
		t.Fatalf("expected ViewLive after clicking subagent in ViewQuota")
	}

	// Combined tree click from ViewHistory
	env.App.SetView(desktop.ViewHistory)
	stepUI(env.Tester, 150*time.Millisecond)
	clickMust(t, env.Tester, "整棵会话树")
	if env.App.ActiveView() != desktop.ViewLive {
		t.Fatalf("expected ViewLive after clicking 整棵会话树 in ViewHistory")
	}

	// Session picker selection from ViewQuota
	env.App.SetView(desktop.ViewQuota)
	stepUI(env.Tester, 150*time.Millisecond)
	clickMust(t, env.Tester, "切换对话")
	clickMust(t, env.Tester, "SQL 慢查询排查")
	if env.App.ActiveView() != desktop.ViewLive {
		t.Fatalf("expected ViewLive after session picker click from ViewQuota")
	}
	mustHaveText(t, env.Tester, "SQL 慢查询排查")
}

// Tier 3-8: Compliance & Read-Only Guarantee with history.Service + source.Provider
func TestTier3_ComplianceAndReadOnlyGuarantee(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	fx.CheckpointSummaries(t)
	fx.Checkpoint(t, "conv-today-active")
	fx.Checkpoint(t, "conv-sql")

	targets := []string{
		filepath.Join(fx.RootDir, "conversation_summaries.db"),
		filepath.Join(fx.RootDir, "conversations", "conv-today-active.db"),
		filepath.Join(fx.RootDir, "conversations", "conv-sql.db"),
	}
	before := make(map[string]string, len(targets))
	for _, p := range targets {
		before[p] = sha256File(t, p)
	}

	calc, _ := metrics.NewDefaultCalculator()
	histSvc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
	if err := histSvc.Refresh(context.Background()); err != nil {
		t.Fatalf("histSvc.Refresh: %v", err)
	}
	_ = histSvc.Query(history.RangeAll, anchorNow)

	for _, p := range targets {
		if after := sha256File(t, p); after != before[p] {
			t.Fatalf("history.Aggregator mutated %s: before=%s after=%s", p, before[p], after)
		}
	}
}
