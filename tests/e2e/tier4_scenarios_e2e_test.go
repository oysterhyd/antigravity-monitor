//go:build e2e

package e2e_test

import (
	"context"
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
	env.sampleQuota(t, anchorNow.Add(-time.Hour), quotaReading(anchorNow, 91, 56))
	env.sampleQuota(t, anchorNow, quotaReading(anchorNow, 74, 41))
	tt := env.Tester

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
	if env.App.ActiveView() != desktop.ViewLive {
		t.Fatalf("expected Top 3 jump to return to ViewLive, got %v", env.App.ActiveView())
	}
	mustHaveText(t, tt, "SQL 慢查询排查")
}

func TestTier4_Scenario_LowQuotaAfterFirstReading_DarkEN(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	env := newE2EDesktopApp(t, fx, theme.ModeDark, "en", nil)

	clickMust(t, env.Tester, "Account Quota")
	mustHaveText(t, env.Tester, "--%")
	env.sampleQuota(t, anchorNow, quotaReading(anchorNow, 19, 52))
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
	reading := quotaReading(anchorNow, 80, 75)
	env := newE2EDesktopApp(t, fx, theme.ModeDark, "zh", &reading)
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

	if snapshot := env.Quota.State(tStream.Add(10 * time.Minute)); snapshot.Reading != reading {
		t.Fatalf("local usage changed reported quota: %+v", snapshot)
	}

	clickMust(t, env.Tester, "历史消耗统计")
	mustHaveText(t, env.Tester, "历史消耗统计")
	verifyPNGFrame(t, env.Tester, "t4_s4_wal_history_dark_zh.png")
}

func TestTier4_Scenario_QuotaRefreshAndOfflineFallback_LightZH(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	reading := quotaReading(anchorNow, 74, 41)
	env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &reading)

	clickMust(t, env.Tester, "账号实时额度")
	mustHaveText(t, env.Tester, "账号实时额度")
	verifyPNGFrame(t, env.Tester, "t4_s5_quota_ok_light_zh.png")

	env.quotaSource.Store(&quotaResult{err: quota.ErrCLIUsage})
	if err := env.Quota.Refresh(context.Background(), anchorNow); err == nil {
		t.Fatal("expected refresh to fail")
	}
	stepUI(env.Tester, 200*time.Millisecond)
	mustHaveText(t, env.Tester, "刷新失败，显示上次读取的额度")
	mustHaveText(t, env.Tester, "74%")
	verifyPNGFrame(t, env.Tester, "t4_s5_quota_err_light_zh.png")
}

// Tier 4-6: Full 4-Corner Visual Parity Matrix (Light/Dark × ZH/EN × 4 screens = 16 PNGs at 900×590)
func TestTier4_Scenario_FullVisualParityMatrix_900x590(t *testing.T) {
	fx := seedRealisticHistoryRoot(t)
	reading := quotaReading(anchorNow, 74, 41)

	corners := []struct {
		mode       string
		lang       string
		quotaBtn   string
		historyBtn string
		liveMarker string
		quotaTitle string
		histTitle  string
	}{
		{theme.ModeLight, "zh", "账号实时额度", "历史消耗统计", "上下文窗口", "额度变化趋势", "历史消耗统计"},
		{theme.ModeLight, "en", "Account Quota", "Usage History", "Context Window", "Quota Usage Trend", "Usage History"},
		{theme.ModeDark, "zh", "账号实时额度", "历史消耗统计", "上下文窗口", "额度变化趋势", "历史消耗统计"},
		{theme.ModeDark, "en", "Account Quota", "Usage History", "Context Window", "Quota Usage Trend", "Usage History"},
	}

	for _, c := range corners {
		env := newE2EDesktopApp(t, fx, c.mode, c.lang, &reading)
		prefix := "matrix_" + c.mode + "_" + c.lang

		// 1) Live Monitor + Sidebar 3-icon footer
		mustHaveText(t, env.Tester, c.liveMarker)
		verifyPNGFrame(t, env.Tester, prefix+"_01_live.png")

		// 2) Account Quota (Cards + 24h Trend)
		clickMust(t, env.Tester, c.quotaBtn)
		mustHaveText(t, env.Tester, c.quotaTitle)
		verifyPNGFrame(t, env.Tester, prefix+"_02_quota.png")

		// 3) Account Quota with the seven-day trend selected.
		clickMust(t, env.Tester, "7d")
		verifyPNGFrame(t, env.Tester, prefix+"_03_seven_day.png")

		// 4) Usage History (7D Token Stacked Bars + Top 3)
		clickMust(t, env.Tester, c.historyBtn)
		mustHaveText(t, env.Tester, c.histTitle)
		verifyPNGFrame(t, env.Tester, prefix+"_04_history.png")
	}
}
