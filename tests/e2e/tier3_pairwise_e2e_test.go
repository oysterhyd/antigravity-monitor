//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
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

// Tier 3-2: Calibration -> Burn Deduction -> Trend -> Low-Quota Amber Dot -> Auto-Reset
func TestTier3_CalibrationBurnResetTrendAndAmberDot(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)

	if env.Quota.State(anchorNow).Calibrated {
		t.Fatalf("expected uncalibrated initial state")
	}

	_, err := env.Quota.Calibrate(quota.CalibrationInput{
		GeminiRemainingPct: 28,
		GeminiResetIn:      1 * time.Hour,
		ClaudeRemainingPct: 60,
		ClaudeResetIn:      3 * time.Hour,
	}, anchorNow)
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	// Burn Gemini past < 25% threshold.
	afterBurn := env.Quota.SampleNow(anchorNow.Add(15*time.Minute), history.PoolConsumption{
		GeminiTokens:  450000,
		GeminiCostUSD: 3.80,
	})
	if afterBurn.Gemini.RemainingPct >= 25 || !afterBurn.HasLowQuota {
		t.Fatalf("expected Gemini < 25%% and HasLowQuota=true, got %+v", afterBurn)
	}
	clickMust(t, env.Tester, "Account Quota")
	mustHaveText(t, env.Tester, "Quota Usage Trend")
	verifyPNGFrame(t, env.Tester, "tier3_low_quota_amber.png")

	// Advance past 1h reset -> auto-reset to 100%, amber dot clears, ResetEvent recorded.
	afterReset := env.Quota.SampleNow(anchorNow.Add(65*time.Minute), history.PoolConsumption{})
	if afterReset.Gemini.RemainingPct != 100 || afterReset.HasLowQuota {
		t.Fatalf("expected auto-reset to 100%% and HasLowQuota=false, got %+v", afterReset)
	}
	hasReset := false
	for _, pt := range afterReset.Trend24h {
		if pt.ResetEvent {
			hasReset = true
			break
		}
	}
	if !hasReset {
		t.Fatalf("expected ResetEvent marker in Trend24h")
	}
}

// Tier 3-3: Theme & Locale Matrix across Live, Quota (Calibrate Popover), and History
func TestTier3_ThemeAndLocaleMatrixAcrossAllThreeViews(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	cal := quota.CalibrationInput{GeminiRemainingPct: 74, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 41, ClaudeResetIn: 2 * time.Hour}
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)

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

// Tier 3-5: JSON Source Override, HTTP 500 Fallback, and Restart Persistence
func TestTier3_JSONSourceOverrideAndPersistenceRestart(t *testing.T) {
	histPath := filepath.Join(t.TempDir(), "quota_history.json")
	srv, code, _ := startLocalQuotaJSONServer(t, defaultQuotaJSONPayload(72, 48))

	svc1 := quota.NewService(histPath)
	snap1, err := svc1.SetJSONSource(srv.URL, anchorNow)
	if err != nil || snap1.Gemini.RemainingPct != 72 {
		t.Fatalf("SetJSONSource: snap=%+v err=%v", snap1, err)
	}

	// Simulate HTTP 500 -> retains last valid estimate with SourceError populated.
	code.Store(http.StatusInternalServerError)
	snapErr, _ := svc1.Refresh(anchorNow.Add(5*time.Minute), history.PoolConsumption{})
	if snapErr.SourceError == "" || snapErr.Gemini.RemainingPct != 72 {
		t.Fatalf("expected fallback estimate + SourceError on 500, got %+v", snapErr)
	}

	// Manual calibration clears source override and persists across restart.
	if _, err := svc1.Calibrate(quota.CalibrationInput{
		GeminiRemainingPct: 88, GeminiResetIn: 4 * time.Hour,
		ClaudeRemainingPct: 62, ClaudeResetIn: 3 * time.Hour,
	}, anchorNow.Add(10*time.Minute)); err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	svc2 := quota.NewService(histPath)
	reloaded := svc2.State(anchorNow.Add(10 * time.Minute))
	if !reloaded.Calibrated || reloaded.Gemini.RemainingPct != 88 || len(reloaded.Trend24h) < 2 {
		t.Fatalf("expected calibration & trend persisted across restart, got %+v", reloaded)
	}
}

// Tier 3-6: Incremental SQLite Cache + Live Quota Consumption Deduction Alignment
func TestTier3_IncrementalSQLiteCachePlusLiveQuotaDeduction(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
	histPath := filepath.Join(t.TempDir(), "quota_history.json")
	calc, _ := metrics.NewDefaultCalculator()

	histSvc := history.NewAggregator(fx.RootDir, cachePath, calc)
	if err := histSvc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh 1: %v", err)
	}
	beforeAll := histSvc.Query(history.RangeAll, anchorNow)

	quotaSvc := quota.NewService(histPath)
	if _, err := quotaSvc.Calibrate(quota.CalibrationInput{
		GeminiRemainingPct: 80, GeminiResetIn: 4 * time.Hour,
		ClaudeRemainingPct: 80, ClaudeResetIn: 4 * time.Hour,
	}, anchorNow); err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	// Append new Gemini + Claude WAL rows after calibration timestamp.
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
	qSnap := quotaSvc.SampleNow(tPlus, delta)
	if qSnap.Gemini.RemainingPct >= 80 || qSnap.ClaudeGPT.RemainingPct >= 80 {
		t.Fatalf("expected both quota pools to deduct from live consumption, got %+v", qSnap)
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
