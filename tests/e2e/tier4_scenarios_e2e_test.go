//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/quota"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

// Tier 4-1: Multi-Day Developer Workweek & Top 3 Jump (Light / ZH)
func TestTier4_Scenario_MultiDayDeveloperWorkweek_LightZH(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
	writeQuotaHistoryFixture(t, env.HistoryPath, anchorNow, 74, 41, true)
	env.Quota = quota.NewService(env.HistoryPath)
	// Recreate desktop app bound to populated quota service so UI reads seeded trend/reset points.
	app := desktop.New(env.Provider, desktop.Options{
		ThemeMode: theme.ModeLight,
		History:   env.History,
		Quota:     env.Quota,
		Now:       func() time.Time { return anchorNow },
	})
	defer app.Close()
	tt := newHeadlessTester(app)

	mustHaveText(t, tt, "实时架构与 WAL 监控联调")
	verifyPNGFrame(t, tt, "t4_s1_live_light_zh.png")

	clickMust(t, tt, "账号实时额度")
	mustHaveText(t, tt, "74%")
	mustHaveText(t, tt, "41%")
	verifyPNGFrame(t, tt, "t4_s1_quota_light_zh.png")

	clickMust(t, tt, "7d")
	clickMust(t, tt, "Claude & GPT")

	clickMust(t, tt, "历史消耗统计")
	mustHaveText(t, tt, "Token 消耗分布")
	verifyPNGFrame(t, tt, "t4_s1_history_light_zh.png")

	clickMust(t, tt, "费用")
	mustHaveText(t, tt, "费用走势（按模型族）")

	clickMust(t, tt, "SQL 慢查询排查")
	if app.ActiveView() != desktop.ViewLive {
		t.Fatalf("expected Top 3 jump to return to ViewLive, got %v", app.ActiveView())
	}
	mustHaveText(t, tt, "SQL 慢查询排查")
}

// Tier 4-2: Low-Quota Crunch (19%) & Calibration Flow (Dark / EN)
func TestTier4_Scenario_LowQuotaCrunchAndCalibrationFlow_DarkEN(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	env := newE2EDesktopApp(t, fx, theme.ModeDark, "en", nil)

	clickMust(t, env.Tester, "Account Quota")
	mustHaveText(t, env.Tester, "--%")
	clickMust(t, env.Tester, "Calibrate")
	verifyPNGFrame(t, env.Tester, "t4_s2_calibrate_dark_en.png")

	if _, err := env.Quota.Calibrate(quota.CalibrationInput{
		GeminiRemainingPct: 19,
		GeminiResetIn:      2*time.Hour + 10*time.Minute,
		ClaudeRemainingPct: 52,
		ClaudeResetIn:      3 * time.Hour,
	}, anchorNow); err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	clickMust(t, env.Tester, "Cancel")
	stepUI(env.Tester, 200*time.Millisecond)
	if !env.Quota.State(anchorNow).HasLowQuota {
		t.Fatalf("expected HasLowQuota=true at 19%%")
	}
	mustHaveText(t, env.Tester, "19%")
	verifyPNGFrame(t, env.Tester, "t4_s2_lowquota_dark_en.png")

	clickMust(t, env.Tester, "Usage History")
	clickMust(t, env.Tester, "30D")
	clickMust(t, env.Tester, "Cache %")
	mustHaveText(t, env.Tester, "Prompt Cache Hit Rate")
	verifyPNGFrame(t, env.Tester, "t4_s2_history_dark_en.png")
}

// Tier 4-3: First-Run Empty Workspace (Light / EN)
func TestTier4_Scenario_FirstRunEmptyWorkspace_LightEN(t *testing.T) {
	env := newE2EDesktopApp(t, nil, theme.ModeLight, "en", nil)

	mustHaveText(t, env.Tester, "Waiting")
	verifyPNGFrame(t, env.Tester, "t4_s3_empty_live_light_en.png")

	clickMust(t, env.Tester, "Account Quota")
	mustHaveText(t, env.Tester, "--%")
	mustHaveText(t, env.Tester, "Calibrate Now")
	verifyPNGFrame(t, env.Tester, "t4_s3_empty_quota_light_en.png")

	clickMust(t, env.Tester, "Usage History")
	for _, r := range []string{"Today", "7D", "30D", "All"} {
		clickMust(t, env.Tester, r)
		mustHaveText(t, env.Tester, "$0.00")
		mustNotHaveText(t, env.Tester, "NaN")
	}
	verifyPNGFrame(t, env.Tester, "t4_s3_empty_history_light_en.png")
}

// Tier 4-4: Active WAL Streaming & Incremental Refresh (Dark / ZH)
func TestTier4_Scenario_ActiveWALStreamingAndIncrementalRefresh_DarkZH(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	cal := quota.CalibrationInput{
		GeminiRemainingPct: 80, GeminiResetIn: 4 * time.Hour,
		ClaudeRemainingPct: 75, ClaudeResetIn: 4 * time.Hour,
	}
	env := newE2EDesktopApp(t, fx, theme.ModeDark, "zh", &cal)
	before := env.History.Query(history.RangeAll, anchorNow)

	// Append 5 uncheckpointed WAL rows while writer handle remains open.
	tStream := anchorNow.Add(5 * time.Minute)
	for i := 0; i < 5; i++ {
		fx.AppendWALGenRow(t, "conv-today-active", genSpec{
			GenIndex:        50 + i,
			StepIndices:     []int{60 + i},
			ModelSlug:       "gemini-4-neon",
			ModelDisplay:    "Gemini 4 Neon (High)",
			InputTokens:     4000,
			CacheReadTokens: 16000,
			OutputTokens:    500,
			ContextUsed:     20500,
			ContextLimit:    512000,
			Timestamp:       tStream.Add(time.Duration(i) * time.Minute),
		})
	}

	if err := env.History.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh over active WAL: %v", err)
	}
	after := env.History.Query(history.RangeAll, tStream.Add(10*time.Minute))
	if after.RequestCount != before.RequestCount+5 {
		t.Fatalf("expected +5 streamed WAL requests, before=%d after=%d", before.RequestCount, after.RequestCount)
	}

	delta := env.History.ConsumptionSince(anchorNow.Add(1 * time.Second))
	qSnap := env.Quota.SampleNow(tStream.Add(10*time.Minute), delta)
	if qSnap.Gemini.RemainingPct >= 80 {
		t.Fatalf("expected Gemini remaining %% to drop after WAL consumption, got %v", qSnap.Gemini.RemainingPct)
	}

	clickMust(t, env.Tester, "历史消耗统计")
	mustHaveText(t, env.Tester, "历史消耗统计")
	verifyPNGFrame(t, env.Tester, "t4_s4_wal_history_dark_zh.png")
}

// Tier 4-5: Local JSON Endpoint Integration & Offline Fallback (Light / ZH)
func TestTier4_Scenario_LocalJSONEndpointIntegration_LightZH(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	srv, code, _ := startLocalQuotaJSONServer(t, defaultQuotaJSONPayload(74, 41))
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)

	snap, err := env.Quota.SetJSONSource(srv.URL, anchorNow)
	if err != nil {
		t.Fatalf("SetJSONSource: %v", err)
	}
	if snap.Gemini.RemainingPct != 74 || snap.ClaudeGPT.RemainingPct != 41 {
		t.Fatalf("unexpected JSON endpoint values: %+v", snap)
	}

	clickMust(t, env.Tester, "账号实时额度")
	mustHaveText(t, env.Tester, "oyster@gmail.com")
	mustHaveText(t, env.Tester, "Gemini 4 Neon (High)")
	mustHaveText(t, env.Tester, "Claude 4.5 Sonnet / Opus")
	verifyPNGFrame(t, env.Tester, "t4_s5_json_ok_light_zh.png")

	// Switch server offline (503) and refresh -> graceful inline fallback.
	code.Store(http.StatusServiceUnavailable)
	_, _ = env.Quota.Refresh(anchorNow.Add(5*time.Minute), history.PoolConsumption{})
	clickMust(t, env.Tester, "↻ 刷新采样")
	st := env.Quota.State(anchorNow.Add(5 * time.Minute))
	if st.SourceError == "" {
		t.Fatalf("expected non-empty SourceError when JSON endpoint returns 503")
	}
	mustHaveText(t, env.Tester, "74%")
	verifyPNGFrame(t, env.Tester, "t4_s5_json_err_light_zh.png")
}

// Tier 4-6: Full 4-Corner Visual Parity Matrix (Light/Dark × ZH/EN × 4 screens = 16 PNGs at 900×590)
func TestTier4_Scenario_FullVisualParityMatrix_900x590(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	cal := quota.CalibrationInput{
		GeminiRemainingPct: 74,
		GeminiResetIn:      3*time.Hour + 42*time.Minute,
		ClaudeRemainingPct: 41,
		ClaudeResetIn:      2*time.Hour + 15*time.Minute,
	}

	corners := []struct {
		mode       string
		lang       string
		quotaBtn   string
		historyBtn string
		calBtn     string
		liveMarker string
		quotaTitle string
		histTitle  string
	}{
		{theme.ModeLight, "zh", "账号实时额度", "历史消耗统计", "校准", "上下文窗口", "额度变化趋势", "历史消耗统计"},
		{theme.ModeLight, "en", "Account Quota", "Usage History", "Calibrate", "Context Window", "Quota Usage Trend", "Usage History"},
		{theme.ModeDark, "zh", "账号实时额度", "历史消耗统计", "校准", "上下文窗口", "额度变化趋势", "历史消耗统计"},
		{theme.ModeDark, "en", "Account Quota", "Usage History", "Calibrate", "Context Window", "Quota Usage Trend", "Usage History"},
	}

	for _, c := range corners {
		env := newE2EDesktopApp(t, fx, c.mode, c.lang, &cal)
		prefix := "matrix_" + c.mode + "_" + c.lang

		// 1) Live Monitor + Sidebar 3-icon footer
		mustHaveText(t, env.Tester, c.liveMarker)
		verifyPNGFrame(t, env.Tester, prefix+"_01_live.png")

		// 2) Account Quota (Cards + 24h Trend)
		clickMust(t, env.Tester, c.quotaBtn)
		mustHaveText(t, env.Tester, c.quotaTitle)
		verifyPNGFrame(t, env.Tester, prefix+"_02_quota.png")

		// 3) Account Quota (Calibration Popover open)
		clickMust(t, env.Tester, c.calBtn)
		verifyPNGFrame(t, env.Tester, prefix+"_03_calibrate.png")

		// 4) Usage History (7D Token Stacked Bars + Top 3)
		clickMust(t, env.Tester, c.historyBtn)
		mustHaveText(t, env.Tester, c.histTitle)
		verifyPNGFrame(t, env.Tester, prefix+"_04_history.png")
	}

	_ = filepath.Join("", "")
}
