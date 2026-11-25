//go:build e2e

package e2e_test

import (
	"strings"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/quota"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

// Feature #1: Sidebar Footer Action Strip (Tier 1)
func TestR1SidebarFooterStrip(t *testing.T) {
	t.Run("ThreeButtonsOrder", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)

		rQuota, okQ := env.Tester.Find("账号实时额度")
		rHist, okH := env.Tester.Find("历史消耗统计")
		rSet, okS := env.Tester.Find("设置")
		if !okQ || !okH || !okS {
			t.Fatalf("expected Quota(%v), History(%v), Settings(%v) footer buttons", okQ, okH, okS)
		}
		if !(rQuota.X < rHist.X && rHist.X < rSet.X) {
			t.Fatalf("expected left-to-right order Quota -> History -> Settings, got X=(%v,%v,%v)", rQuota.X, rHist.X, rSet.X)
		}
	})

	t.Run("WorkspaceLeftActionsRight", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)

		mustHaveText(t, env.Tester, "d:/antigravity_monitor")
		rWS, okWS := env.Tester.Find("d:/antigravity_monitor")
		rQuota, okQ := env.Tester.Find("账号实时额度")
		if okWS && okQ && rWS.X+rWS.W > rQuota.X {
			t.Fatalf("workspace label (%v) overlaps Quota footer button (%v)", rWS, rQuota)
		}
	})

	t.Run("LocalizedTooltipsEN", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)

		if _, ok := env.Tester.Find("Account Quota"); !ok {
			t.Fatalf("expected English Account Quota footer button")
		}
		if _, ok := env.Tester.Find("Usage History"); !ok {
			t.Fatalf("expected English Usage History footer button")
		}
		if _, ok := env.Tester.Find("Settings"); !ok {
			t.Fatalf("expected English Settings footer button")
		}
	})

	t.Run("NoPrototypeCalloutRing", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		verifyPNGFrame(t, env.Tester, "r1_footer_light_zh.png")

		env.App.SetThemeMode(theme.ModeDark)
		stepUI(env.Tester, 250*time.Millisecond)
		verifyPNGFrame(t, env.Tester, "r1_footer_dark_zh.png")
	})

	t.Run("SettingsButtonPreserved", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)

		clickMust(t, env.Tester, "设置")
		mustHaveText(t, env.Tester, "语言")
		mustHaveText(t, env.Tester, "完成")
		clickMust(t, env.Tester, "完成")
	})
}

// Feature #1: Sidebar Footer Action Strip (Tier 2 Boundaries)
func TestR1SidebarFooterBoundaries(t *testing.T) {
	t.Run("LongWorkspacePath140Chars", func(t *testing.T) {
		longWS := "d:/workspaces/" + strings.Repeat("monorepo-telemetry-segment-", 5) + "tail"
		snap := model.AppSnapshot{
			GeneratedAt:          anchorNow,
			ActiveConversationID: "conv-long-ws",
			Conversations: []model.ConversationSnapshot{
				{
					ID:        "conv-long-ws",
					Title:     "Long WS",
					Workspace: longWS,
					Root: model.AgentNode{
						ID:   model.RootAgentID,
						Name: "主 Agent",
					},
				},
			},
		}
		app := desktop.New(&staticSnapshotProvider{snap: snap}, desktop.Options{ThemeMode: theme.ModeLight})
		defer app.Close()
		tt := newHeadlessTester(app)

		rQ, okQ := tt.Find("账号实时额度")
		rH, okH := tt.Find("历史消耗统计")
		rS, okS := tt.Find("设置")
		if !okQ || !okH || !okS {
			t.Fatalf("long workspace clipped footer buttons: Q=%v H=%v S=%v", okQ, okH, okS)
		}
		if rS.X+rS.W > 250 {
			t.Fatalf("settings button pushed outside 244px sidebar: %+v", rS)
		}
		_ = rQ
		_ = rH
		verifyPNGFrame(t, tt, "")
	})

	t.Run("EmptyWorkspacePath", func(t *testing.T) {
		snap := model.AppSnapshot{
			GeneratedAt:          anchorNow,
			ActiveConversationID: "conv-empty-ws",
			Conversations: []model.ConversationSnapshot{
				{
					ID:        "conv-empty-ws",
					Title:     "Empty WS",
					Workspace: "",
					Root:      model.AgentNode{ID: model.RootAgentID, Name: "主 Agent"},
				},
			},
		}
		app := desktop.New(&staticSnapshotProvider{snap: snap}, desktop.Options{ThemeMode: theme.ModeLight})
		defer app.Close()
		tt := newHeadlessTester(app)
		if _, ok := tt.Find("账号实时额度"); !ok {
			t.Fatalf("footer buttons missing when workspace is empty")
		}
	})

	t.Run("MinWindowHeightClipCheck", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		// Add 10 subagents to ensure sidebar scroll does not push footer off 590px window.
		for i := 0; i < 10; i++ {
			subID := "sub-many-" + string(rune('a'+i))
			fx.InsertSummary(t, summarySpec{
				ConversationID:       subID,
				ParentConversationID: "conv-today-active",
				AgentName:            "worker",
				NestingDepth:         1,
				LastModifiedTime:     anchorNow.Add(-time.Duration(i+1) * time.Minute),
			})
		}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		rS, ok := env.Tester.Find("设置")
		if !ok || rS.Y+rS.H > 590 {
			t.Fatalf("footer clipped below 590px: ok=%v rect=%+v", ok, rS)
		}
		clickMust(t, env.Tester, "历史消耗统计")
		if env.App.ActiveView() != desktop.ViewHistory {
			t.Fatalf("expected ViewHistory, got %v", env.App.ActiveView())
		}
	})

	t.Run("RapidSequentialFooterClicks", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)

		clickMust(t, env.Tester, "账号实时额度")
		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "设置")
		clickMust(t, env.Tester, "账号实时额度")
		if env.App.ActiveView() != desktop.ViewQuota {
			t.Fatalf("expected ViewQuota after rapid clicks, got %v", env.App.ActiveView())
		}
	})

	t.Run("UnicodeAndCJKWorkspaceName", func(t *testing.T) {
		snap := model.AppSnapshot{
			GeneratedAt:          anchorNow,
			ActiveConversationID: "conv-cjk-ws",
			Conversations: []model.ConversationSnapshot{
				{
					ID:        "conv-cjk-ws",
					Title:     "CJK Workspace",
					Workspace: "d:/项目/Antigravity 监控/核心工作区",
					Root:      model.AgentNode{ID: model.RootAgentID, Name: "主 Agent"},
				},
			},
		}
		app := desktop.New(&staticSnapshotProvider{snap: snap}, desktop.Options{ThemeMode: theme.ModeDark})
		defer app.Close()
		tt := newHeadlessTester(app)
		mustHaveText(t, tt, "Antigravity 监控")
		verifyPNGFrame(t, tt, "")
	})
}

// Feature #2: Gauge Icon + Low-Quota Amber Dot (Tier 1)
func TestR1GaugeAmberDot(t *testing.T) {
	t.Run("HealthyQuotaHidesDot", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{
			GeminiRemainingPct: 80,
			GeminiResetIn:      4 * time.Hour,
			ClaudeRemainingPct: 60,
			ClaudeResetIn:      3 * time.Hour,
		}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		snap := env.Quota.State(anchorNow)
		if snap.HasLowQuota {
			t.Fatalf("expected HasLowQuota=false for 80%%/60%%, got true")
		}
	})

	t.Run("GeminiBelow25TriggersDot", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{
			GeminiRemainingPct: 18,
			GeminiResetIn:      2 * time.Hour,
			ClaudeRemainingPct: 70,
			ClaudeResetIn:      3 * time.Hour,
		}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		if !env.Quota.State(anchorNow).HasLowQuota {
			t.Fatalf("expected HasLowQuota=true when Gemini=18%%")
		}
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("ClaudeBelow25TriggersDot", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{
			GeminiRemainingPct: 85,
			GeminiResetIn:      4 * time.Hour,
			ClaudeRemainingPct: 12,
			ClaudeResetIn:      1 * time.Hour,
		}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		if !env.Quota.State(anchorNow).HasLowQuota {
			t.Fatalf("expected HasLowQuota=true when Claude=12%%")
		}
	})

	t.Run("BothBelow25TriggersDot", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{
			GeminiRemainingPct: 10,
			GeminiResetIn:      1 * time.Hour,
			ClaudeRemainingPct: 5,
			ClaudeResetIn:      1 * time.Hour,
		}
		env := newE2EDesktopApp(t, fx, theme.ModeDark, "en", &cal)
		if !env.Quota.State(anchorNow).HasLowQuota {
			t.Fatalf("expected HasLowQuota=true when both pools < 25%%")
		}
	})

	t.Run("BurnCrossesThresholdUpdatesDot", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{
			GeminiRemainingPct: 26,
			GeminiResetIn:      3 * time.Hour,
			ClaudeRemainingPct: 70,
			ClaudeResetIn:      3 * time.Hour,
		}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		if env.Quota.State(anchorNow).HasLowQuota {
			t.Fatalf("expected HasLowQuota=false at 26%%")
		}
		after := env.Quota.SampleNow(anchorNow.Add(10*time.Minute), history.PoolConsumption{
			GeminiTokens:  600000,
			GeminiCostUSD: 4.5,
		})
		if after.Gemini.RemainingPct >= 25 {
			t.Fatalf("expected significant Gemini burn to cross <25%%, got %.2f%%", after.Gemini.RemainingPct)
		}
		if !after.HasLowQuota {
			t.Fatalf("expected HasLowQuota=true after burn crossed below 25%%")
		}
	})
}

// Feature #2: Gauge Icon + Low-Quota Amber Dot (Tier 2 Boundaries)
func TestR1AmberDotBoundaries(t *testing.T) {
	t.Run("Exact24PctShowsDot", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 24, GeminiResetIn: time.Hour, ClaudeRemainingPct: 80, ClaudeResetIn: time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		if !env.Quota.State(anchorNow).HasLowQuota {
			t.Fatalf("24%% < 25%% must trigger HasLowQuota=true")
		}
	})

	t.Run("Exact25PctHidesDot", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 25, GeminiResetIn: time.Hour, ClaudeRemainingPct: 25, ClaudeResetIn: time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		if env.Quota.State(anchorNow).HasLowQuota {
			t.Fatalf("exact 25%% must NOT trigger HasLowQuota (strict < 25%%)")
		}
	})

	t.Run("Fractional24Point9PctShowsDot", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 24.9, GeminiResetIn: time.Hour, ClaudeRemainingPct: 50, ClaudeResetIn: time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		if !env.Quota.State(anchorNow).HasLowQuota {
			t.Fatalf("24.9%% < 25.0%% must trigger HasLowQuota=true")
		}
	})

	t.Run("UncalibratedZeroPctHidesDot", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		st := env.Quota.State(anchorNow)
		if st.Calibrated {
			t.Fatalf("expected Calibrated=false initially")
		}
		if st.HasLowQuota {
			t.Fatalf("uncalibrated state must never trigger HasLowQuota false alarm")
		}
	})

	t.Run("ZeroPctBoundaryCalibrated", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 0, GeminiResetIn: time.Hour, ClaudeRemainingPct: 50, ClaudeResetIn: time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		st := env.Quota.SampleNow(anchorNow.Add(5*time.Minute), history.PoolConsumption{
			GeminiTokens:  1000000,
			GeminiCostUSD: 10.0,
		})
		if st.Gemini.RemainingPct != 0 {
			t.Fatalf("expected RemainingPct clamped to 0, got %v", st.Gemini.RemainingPct)
		}
		if !st.HasLowQuota {
			t.Fatalf("calibrated 0%% must trigger HasLowQuota=true")
		}
	})
}

// Feature #3: 3-Bar Chart Icon (Tier 1)
func TestR1HistoryIcon(t *testing.T) {
	t.Run("IconPresentInLiveView", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		if _, ok := env.Tester.Find("历史消耗统计"); !ok {
			t.Fatalf("History icon button missing in Live Monitor")
		}
	})

	t.Run("ClickOpensHistoryView", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		if env.App.ActiveView() != desktop.ViewHistory {
			t.Fatalf("expected ViewHistory, got %v", env.App.ActiveView())
		}
		mustHaveText(t, env.Tester, "历史消耗统计")
	})

	t.Run("ActiveAccentHighlight", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		verifyPNGFrame(t, env.Tester, "r1_history_active_light.png")
	})

	t.Run("VisibleAcrossThemes", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		verifyPNGFrame(t, env.Tester, "")
		env.App.SetThemeMode(theme.ModeDark)
		stepUI(env.Tester, 250*time.Millisecond)
		if _, ok := env.Tester.Find("Usage History"); !ok {
			t.Fatalf("History icon missing in Dark mode")
		}
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("PreservesLiveSelectionOnReturn", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		env.App.FocusConversation("conv-sql", true)
		stepUI(env.Tester, 200*time.Millisecond)

		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "历史消耗统计")
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("expected ViewLive on toggle back, got %v", env.App.ActiveView())
		}
		mustHaveText(t, env.Tester, "SQL 慢查询排查")
	})
}

// Feature #3: 3-Bar Chart Icon (Tier 2 Boundaries)
func TestR1HistoryIconBoundaries(t *testing.T) {
	t.Run("NilHistoryServiceSafeRender", func(t *testing.T) {
		app := desktop.New(nil, desktop.Options{ThemeMode: theme.ModeLight, History: nil, Quota: nil})
		defer app.Close()
		tt := newHeadlessTester(app)
		clickMust(t, tt, "历史消耗统计")
		if app.ActiveView() != desktop.ViewHistory {
			t.Fatalf("expected ViewHistory even with nil History service")
		}
		verifyPNGFrame(t, tt, "")
	})

	t.Run("DoubleClickIdempotency", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		env.App.ToggleView(desktop.ViewHistory)
		env.App.ToggleView(desktop.ViewHistory)
		stepUI(env.Tester, 200*time.Millisecond)
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("expected ToggleView x2 to return to ViewLive, got %v", env.App.ActiveView())
		}
	})

	t.Run("OpensWithPopoverOpen", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "设置")
		mustHaveText(t, env.Tester, "完成")
		clickMust(t, env.Tester, "历史消耗统计")
		if env.App.ActiveView() != desktop.ViewHistory {
			t.Fatalf("expected ViewHistory")
		}
		mustNotHaveText(t, env.Tester, "开机驻留托盘")
	})

	t.Run("EmptySnapshotLiveMonitor", func(t *testing.T) {
		sp := &staticSnapshotProvider{snap: model.AppSnapshot{GeneratedAt: anchorNow}}
		app := desktop.New(sp, desktop.Options{ThemeMode: theme.ModeLight})
		defer app.Close()
		tt := newHeadlessTester(app)
		clickMust(t, tt, "历史消耗统计")
		if app.ActiveView() != desktop.ViewHistory {
			t.Fatalf("expected ViewHistory from empty Live Monitor")
		}
	})

	t.Run("TooltipInBothLocales", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		mustHaveText(t, env.Tester, "历史消耗统计")
		env.App.SetLanguage("en")
		stepUI(env.Tester, 200*time.Millisecond)
		mustHaveText(t, env.Tester, "Usage History")
	})
}

// Feature #4: View Switching & Toggle Behavior (Tier 1)
func TestR1ViewSwitching(t *testing.T) {
	t.Run("ToggleQuotaBackToLive", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "账号实时额度")
		if env.App.ActiveView() != desktop.ViewQuota {
			t.Fatalf("want ViewQuota, got %v", env.App.ActiveView())
		}
		clickMust(t, env.Tester, "账号实时额度")
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("want ViewLive, got %v", env.App.ActiveView())
		}
	})

	t.Run("ToggleHistoryBackToLive", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		if env.App.ActiveView() != desktop.ViewHistory {
			t.Fatalf("want ViewHistory, got %v", env.App.ActiveView())
		}
		clickMust(t, env.Tester, "历史消耗统计")
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("want ViewLive, got %v", env.App.ActiveView())
		}
	})

	t.Run("DirectQuotaToHistorySwitch", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "账号实时额度")
		clickMust(t, env.Tester, "历史消耗统计")
		if env.App.ActiveView() != desktop.ViewHistory {
			t.Fatalf("want ViewHistory, got %v", env.App.ActiveView())
		}
	})

	t.Run("ClickAgentNodeReturnsLive", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "账号实时额度")
		clickMust(t, env.Tester, "主 Agent")
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("clicking Agent node from Quota must return to ViewLive, got %v", env.App.ActiveView())
		}
	})

	t.Run("ClickSessionPickerReturnsLive", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "切换对话")
		clickMust(t, env.Tester, "SQL 慢查询排查")
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("picking session from History must return to ViewLive, got %v", env.App.ActiveView())
		}
		mustHaveText(t, env.Tester, "SQL 慢查询排查")
	})
}

// Feature #4: View Switching & Toggle Behavior (Tier 2 Boundaries)
func TestR1ViewSwitchBoundaries(t *testing.T) {
	t.Run("JumpToDeletedOrMissingConvID", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		env.App.SetView(desktop.ViewHistory)
		env.App.FocusConversation("non-existent-id", true)
		stepUI(env.Tester, 200*time.Millisecond)
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("FocusConversation must switch back to ViewLive even for missing ID")
		}
		mustHaveText(t, env.Tester, "上下文窗口")
	})

	t.Run("ClickActiveNodeWhileInQuota", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		env.App.FocusAgent(model.RootAgentID)
		stepUI(env.Tester, 150*time.Millisecond)

		env.App.SetView(desktop.ViewQuota)
		stepUI(env.Tester, 150*time.Millisecond)
		if env.App.ActiveView() != desktop.ViewQuota {
			t.Fatalf("expected ViewQuota")
		}

		// Calling FocusAgent with the ALREADY active scope must still return to ViewLive.
		env.App.FocusAgent(model.RootAgentID)
		stepUI(env.Tester, 150*time.Millisecond)
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("FocusAgent(already active scope) from Quota failed to return to ViewLive: got %v", env.App.ActiveView())
		}
	})

	t.Run("ClickCombinedTreeWhileInHistory", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		env.App.SetView(desktop.ViewHistory)
		stepUI(env.Tester, 150*time.Millisecond)
		clickMust(t, env.Tester, "整棵会话树")
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("clicking 整棵会话树 from History must return to ViewLive")
		}
	})

	t.Run("ClosePopoversOnQuotaToLive", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "账号实时额度")
		clickMust(t, env.Tester, "校准")
		clickMust(t, env.Tester, "← 返回监控")
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("expected ViewLive")
		}
		mustNotHaveText(t, env.Tester, "本地 JSON 源")
	})

	t.Run("InvalidActiveViewFallback", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		env.App.SetView(desktop.ActiveView("invalid-view-mode"))
		stepUI(env.Tester, 200*time.Millisecond)
		// Should safely render Live Monitor fallback without panic.
		mustHaveText(t, env.Tester, "上下文窗口")
	})
}
