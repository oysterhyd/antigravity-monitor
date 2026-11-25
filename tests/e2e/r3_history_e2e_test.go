//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

// Feature #9: Usage History Header & Range Pill (Tier 1)
func TestR3HistoryHeader(t *testing.T) {
	t.Run("Default7DRange", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		mustHaveText(t, env.Tester, "Usage History")
		mustHaveText(t, env.Tester, "7D")
		mustHaveText(t, env.Tester, "Last 7 Days")
	})

	t.Run("SwitchTodayHourly", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "今日")
		mustHaveText(t, env.Tester, "今日分时段")
	})

	t.Run("Switch30D", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		clickMust(t, env.Tester, "30D")
		mustHaveText(t, env.Tester, "Last 30 Days")
	})

	t.Run("SwitchAllTime", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "全部")
		mustHaveText(t, env.Tester, "全部历史")
	})

	t.Run("BackToLiveButton", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		clickMust(t, env.Tester, "← Live View")
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("expected ViewLive, got %v", env.App.ActiveView())
		}
	})
}

// Feature #9: Usage History Header & Range Pill (Tier 2 Boundaries)
func TestR3HistoryHeaderBoundaries(t *testing.T) {
	t.Run("EmptyDBAllRanges", func(t *testing.T) {
		env := newE2EDesktopApp(t, nil, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		for _, r := range []string{"Today", "7D", "30D", "All"} {
			clickMust(t, env.Tester, r)
			mustHaveText(t, env.Tester, "$0.00")
			mustNotHaveText(t, env.Tester, "NaN")
		}
	})

	t.Run("DataOnlyInAllTimeRange", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-old-only",
			Title:            "90-Day Old Session",
			StepCount:        2,
			LastModifiedTime: anchorNow.Add(-90 * 24 * time.Hour),
		})
		fx.AppendWALGenRow(t, "conv-old-only", genSpec{
			GenIndex:        0,
			StepIndices:     []int{1},
			ModelSlug:       "gemini-4-neon",
			ModelDisplay:    "Gemini 4 Neon (High)",
			InputTokens:     10000,
			CacheReadTokens: 20000,
			OutputTokens:    1000,
			ContextUsed:     31000,
			ContextLimit:    512000,
			Timestamp:       anchorNow.Add(-90 * 24 * time.Hour),
		})

		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if svc.Query(history.Range30D, anchorNow).RequestCount != 0 {
			t.Fatalf("expected 0 requests in Range30D for 90-day-old session")
		}
		all := svc.Query(history.RangeAll, anchorNow)
		if all.RequestCount != 1 || all.TotalTokens == 0 {
			t.Fatalf("expected 1 request in RangeAll, got %+v", all)
		}
	})

	t.Run("MidnightBoundaryTodayVsYesterday", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		localNow := time.Date(2026, 11, 24, 10, 0, 0, 0, time.Local)
		midnight := time.Date(2026, 11, 24, 0, 0, 0, 0, time.Local)

		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-midnight",
			Title:            "Midnight Split",
			StepCount:        2,
			LastModifiedTime: localNow,
		})
		fx.AppendWALGenRow(t, "conv-midnight", genSpec{
			GenIndex:     0,
			StepIndices:  []int{1},
			ModelSlug:    "gemini-4-flash",
			InputTokens:  1000,
			OutputTokens: 100,
			Timestamp:    midnight.Add(-1 * time.Second), // yesterday 23:59:59
		})
		fx.AppendWALGenRow(t, "conv-midnight", genSpec{
			GenIndex:     1,
			StepIndices:  []int{2},
			ModelSlug:    "gemini-4-flash",
			InputTokens:  2000,
			OutputTokens: 200,
			Timestamp:    midnight.Add(1 * time.Second), // today 00:00:01
		})

		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		today := svc.Query(history.RangeToday, localNow)
		if today.RequestCount != 1 || today.InputTokens != 2000 {
			t.Fatalf("expected only today's 00:00:01 request in RangeToday, got %+v", today)
		}
	})

	t.Run("FutureTimestampClampedOrHandled", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-future",
			Title:            "Clock Skew",
			StepCount:        1,
			LastModifiedTime: anchorNow.Add(10 * time.Minute),
		})
		fx.AppendWALGenRow(t, "conv-future", genSpec{
			GenIndex:     0,
			StepIndices:  []int{1},
			ModelSlug:    "gemini-4-neon",
			InputTokens:  1500,
			OutputTokens: 300,
			Timestamp:    anchorNow.Add(10 * time.Minute),
		})
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		_ = svc.Query(history.RangeToday, anchorNow)
		_ = svc.Query(history.Range7D, anchorNow)
	})

	t.Run("InvalidRangeDefaultsGracefully", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		_ = svc.Refresh(context.Background())
		res := svc.Query(history.Range("invalid-range"), anchorNow)
		if res.RequestCount == 0 && len(res.Buckets) == 0 {
			t.Fatalf("expected graceful fallback query result for invalid range")
		}
	})
}

// Feature #10: 4 Historical KPI Glass Tiles (Tier 1)
func TestR3HistoryKPIs(t *testing.T) {
	t.Run("DisplaysAllFourTiles", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		mustHaveText(t, env.Tester, "Total Tokens")
		mustHaveText(t, env.Tester, "Total Cost")
		mustHaveText(t, env.Tester, "Cache Saved")
		mustHaveText(t, env.Tester, "Sessions")
	})

	t.Run("ClickTotalTokensSelectsMetric", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "总费用")
		clickMust(t, env.Tester, "总 Token")
		mustHaveText(t, env.Tester, "Token 消耗分布")
	})

	t.Run("ClickTotalCostSelectsMetric", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		clickMust(t, env.Tester, "Total Cost")
		mustHaveText(t, env.Tester, "Cost by Model Family")
	})

	t.Run("ClickCacheSavedSelectsMetric", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "缓存节省")
		mustHaveText(t, env.Tester, "Prompt 缓存命中率趋势")
	})

	t.Run("FourthTileNonMetricInfoOnly", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		clickMust(t, env.Tester, "Total Cost")
		mustHaveText(t, env.Tester, "Cost by Model Family")
		// Clicking Sessions info tile should not change active Cost chart title.
		_ = env.Tester.Click("Sessions")
		stepUI(env.Tester, 150*time.Millisecond)
		mustHaveText(t, env.Tester, "Cost by Model Family")
	})
}

// Feature #10: 4 Historical KPI Glass Tiles (Tier 2 Boundaries)
func TestR3HistoryKPIBoundaries(t *testing.T) {
	t.Run("ZeroRequestsZeroTotals", func(t *testing.T) {
		env := newE2EDesktopApp(t, nil, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		mustHaveText(t, env.Tester, "$0.00")
		mustNotHaveText(t, env.Tester, "NaN")
	})

	t.Run("ZeroCacheReadTokens", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-nocache",
			Title:            "Uncached Run",
			StepCount:        1,
			LastModifiedTime: anchorNow,
		})
		fx.AppendWALGenRow(t, "conv-nocache", genSpec{
			GenIndex:        0,
			StepIndices:     []int{1},
			ModelSlug:       "gemini-4-neon",
			InputTokens:     5000,
			CacheReadTokens: 0,
			OutputTokens:    800,
			Timestamp:       anchorNow.Add(-10 * time.Minute),
		})
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		_ = svc.Refresh(context.Background())
		res := svc.Query(history.Range7D, anchorNow)
		if res.CacheSavedUSD != 0 || res.CacheHitRate != 0 {
			t.Fatalf("expected 0 cache savings & 0 cache hit rate, got saved=%v hit=%v", res.CacheSavedUSD, res.CacheHitRate)
		}
	})

	t.Run("LargeNumbersFormatting100MPlus", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-huge",
			Title:            "120M Token Batch",
			StepCount:        1,
			LastModifiedTime: anchorNow,
		})
		fx.AppendWALGenRow(t, "conv-huge", genSpec{
			GenIndex:        0,
			StepIndices:     []int{1},
			ModelSlug:       "claude-opus-5-5-high",
			InputTokens:     25_000_000,
			CacheReadTokens: 95_000_000,
			OutputTokens:    500_000,
			ContextUsed:     120_500_000,
			Timestamp:       anchorNow.Add(-5 * time.Minute),
		})
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		mustHaveText(t, env.Tester, "M")
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("SubThousandTokensAndCounts", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-tiny",
			Title:            "Tiny Session",
			StepCount:        1,
			LastModifiedTime: anchorNow,
		})
		fx.AppendWALGenRow(t, "conv-tiny", genSpec{
			GenIndex:     0,
			StepIndices:  []int{1},
			ModelSlug:    "gemini-4-flash",
			InputTokens:  300,
			OutputTokens: 150,
			Timestamp:    anchorNow.Add(-5 * time.Minute),
		})
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		mustNotHaveText(t, env.Tester, "NaN")
	})

	t.Run("TodayShowsHourlyAvgNotDaily", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		clickMust(t, env.Tester, "Today")
		res := env.History.Query(history.RangeToday, anchorNow)
		if res.TotalCostUSD > 0 && res.AvgCostUSD <= 0 {
			t.Fatalf("expected non-zero hourly AvgCostUSD for RangeToday, got %+v", res)
		}
	})
}

// Feature #11: Stacked Vertical Bar Chart + Inspector (Tier 1)
func TestR3HistoryBars(t *testing.T) {
	t.Run("TokenStackedSegments", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		mustHaveText(t, env.Tester, "Token Breakdown")
		mustHaveText(t, env.Tester, "Cache")
		mustHaveText(t, env.Tester, "Input")
		mustHaveText(t, env.Tester, "Output")
	})

	t.Run("CostStackedByPool", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		clickMust(t, env.Tester, "Cost")
		mustHaveText(t, env.Tester, "Cost by Model Family")
		mustHaveText(t, env.Tester, "Gemini")
		mustHaveText(t, env.Tester, "Claude & GPT")
	})

	t.Run("CacheHitBars", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "缓存率")
		mustHaveText(t, env.Tester, "Prompt 缓存命中率趋势")
		mustHaveText(t, env.Tester, "命中率 %")
	})

	t.Run("MetricPillSyncsKPITiles", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "费用")
		mustHaveText(t, env.Tester, "费用走势（按模型族）")
		clickMust(t, env.Tester, "缓存率")
		mustHaveText(t, env.Tester, "Prompt 缓存命中率趋势")
		clickMust(t, env.Tester, "Token")
		mustHaveText(t, env.Tester, "Token 消耗分布")
	})

	t.Run("BottomInspectorReadout", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		mustHaveText(t, env.Tester, "Hit")
		mustHaveText(t, env.Tester, "Cost")
	})
}

// Feature #11: Stacked Vertical Bar Chart + Inspector (Tier 2 Boundaries)
func TestR3HistoryBarsBoundaries(t *testing.T) {
	t.Run("AllZeroBucketsMaxValZero", func(t *testing.T) {
		env := newE2EDesktopApp(t, nil, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		for _, m := range []string{"Token", "Cost", "Cache %"} {
			clickMust(t, env.Tester, m)
			mustNotHaveText(t, env.Tester, "NaN")
			verifyPNGFrame(t, env.Tester, "")
		}
	})

	t.Run("CacheHitBelow70Pct", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-lowhit",
			Title:            "45% Cache Hit",
			StepCount:        1,
			LastModifiedTime: anchorNow,
		})
		fx.AppendWALGenRow(t, "conv-lowhit", genSpec{
			GenIndex:        0,
			StepIndices:     []int{1},
			ModelSlug:       "gemini-4-neon",
			InputTokens:     5500,
			CacheReadTokens: 4500, // 45% < 70%
			OutputTokens:    500,
			Timestamp:       anchorNow.Add(-1 * time.Hour),
		})
		env := newE2EDesktopApp(t, fx, theme.ModeDark, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		clickMust(t, env.Tester, "Cache %")
		mustNotHaveText(t, env.Tester, "NaN")
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("CacheHit70And100Boundaries", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-hit-bounds",
			Title:            "70 and 100 Cache Hit",
			StepCount:        2,
			LastModifiedTime: anchorNow,
		})
		fx.AppendWALGenRow(t, "conv-hit-bounds", genSpec{
			GenIndex:        0,
			StepIndices:     []int{1},
			ModelSlug:       "gemini-4-neon",
			InputTokens:     3000,
			CacheReadTokens: 7000, // exact 70%
			OutputTokens:    200,
			Timestamp:       anchorNow.Add(-25 * time.Hour),
		})
		fx.AppendWALGenRow(t, "conv-hit-bounds", genSpec{
			GenIndex:        1,
			StepIndices:     []int{2},
			ModelSlug:       "gemini-4-neon",
			InputTokens:     0,
			CacheReadTokens: 10000, // exact 100%
			OutputTokens:    200,
			Timestamp:       anchorNow.Add(-1 * time.Hour),
		})
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		clickMust(t, env.Tester, "Cache %")
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("SingleNonZeroBucketOutOfSeven", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-spike",
			Title:            "Single Spike Day",
			StepCount:        1,
			LastModifiedTime: anchorNow,
		})
		fx.AppendWALGenRow(t, "conv-spike", genSpec{
			GenIndex:        0,
			StepIndices:     []int{1},
			ModelSlug:       "claude-sonnet-4",
			InputTokens:     50000,
			CacheReadTokens: 150000,
			OutputTokens:    5000,
			Timestamp:       anchorNow.Add(-2 * time.Hour),
		})
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		mustNotHaveText(t, env.Tester, "NaN")
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("OutputOnlyBucketNoInputOrCache", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-outonly",
			Title:            "Output Only",
			StepCount:        1,
			LastModifiedTime: anchorNow,
		})
		fx.AppendWALGenRow(t, "conv-outonly", genSpec{
			GenIndex:     0,
			StepIndices:  []int{1},
			ModelSlug:    "gemini-4-flash",
			OutputTokens: 5000,
			Timestamp:    anchorNow.Add(-30 * time.Minute),
		})
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		mustNotHaveText(t, env.Tester, "NaN")
		verifyPNGFrame(t, env.Tester, "")
	})
}

// Feature #12: Bottom 2-Column Split (By Model & Top 3 Sessions) (Tier 1)
func TestR3HistoryBottom(t *testing.T) {
	t.Run("UsageByModelPercentagesAndCosts", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		_ = svc.Refresh(context.Background())

		res := svc.Query(history.RangeAll, anchorNow)
		if len(res.ByModel) == 0 {
			t.Fatalf("expected non-empty ByModel breakdown")
		}
		var shareSum float64
		for _, m := range res.ByModel {
			shareSum += m.Share
		}
		if math.Abs(shareSum-100.0) > 1.0 {
			t.Fatalf("expected ByModel shares to sum to ~100%%, got %.2f%%", shareSum)
		}
	})

	t.Run("Top3SortedByCostDesc", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		_ = svc.Refresh(context.Background())

		res := svc.Query(history.RangeAll, anchorNow)
		if len(res.TopSessions) != 3 {
			t.Fatalf("expected exactly 3 TopSessions, got %d", len(res.TopSessions))
		}
		for i := 1; i < len(res.TopSessions); i++ {
			if res.TopSessions[i].CostUSD > res.TopSessions[i-1].CostUSD {
				t.Fatalf("TopSessions not sorted by CostUSD desc: %+v", res.TopSessions)
			}
		}
	})

	t.Run("Top3ShowsTitleTokensCost", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		clickMust(t, env.Tester, "All")
		mustHaveText(t, env.Tester, "Usage by Model")
		mustHaveText(t, env.Tester, "Top 3 Sessions")
	})

	t.Run("ClickTopSessionJumpsToLiveMonitor", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "SQL 慢查询排查")
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("expected ViewLive after clicking Top 3 session row, got %v", env.App.ActiveView())
		}
		mustHaveText(t, env.Tester, "SQL 慢查询排查")
	})

	t.Run("PoolDotColorMatchesPrimaryPool", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		_ = svc.Refresh(context.Background())
		res := svc.Query(history.RangeAll, anchorNow)
		foundClaude := false
		for _, s := range res.TopSessions {
			if s.ConvID == "conv-sql" && s.PrimaryPool == metrics.PoolClaudeGPT {
				foundClaude = true
			}
		}
		if !foundClaude {
			t.Fatalf("expected conv-sql PrimaryPool == PoolClaudeGPT, got %+v", res.TopSessions)
		}
	})
}

// Feature #12: Bottom 2-Column Split (Tier 2 Boundaries)
func TestR3HistoryBottomBoundaries(t *testing.T) {
	t.Run("ZeroSessionsAndZeroModels", func(t *testing.T) {
		env := newE2EDesktopApp(t, nil, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Usage History")
		res := env.History.Query(history.Range7D, anchorNow)
		if len(res.TopSessions) != 0 || len(res.ByModel) != 0 {
			t.Fatalf("expected 0 TopSessions & ByModel for empty DB")
		}
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("OneAndTwoSessionsLessThan3", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-only-1",
			Title:            "Solo Session",
			StepCount:        1,
			LastModifiedTime: anchorNow,
		})
		fx.AppendWALGenRow(t, "conv-only-1", genSpec{
			GenIndex: 0, StepIndices: []int{1}, ModelSlug: "gemini-4-neon",
			InputTokens: 2000, OutputTokens: 400, Timestamp: anchorNow.Add(-10 * time.Minute),
		})
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		_ = svc.Refresh(context.Background())
		if n := len(svc.Query(history.Range7D, anchorNow).TopSessions); n != 1 {
			t.Fatalf("expected 1 TopSession, got %d", n)
		}

		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-only-2",
			Title:            "Second Session",
			StepCount:        1,
			LastModifiedTime: anchorNow.Add(-5 * time.Minute),
		})
		fx.AppendWALGenRow(t, "conv-only-2", genSpec{
			GenIndex: 0, StepIndices: []int{1}, ModelSlug: "claude-sonnet-4",
			InputTokens: 3000, OutputTokens: 500, Timestamp: anchorNow.Add(-5 * time.Minute),
		})
		_ = svc.Refresh(context.Background())
		if n := len(svc.Query(history.Range7D, anchorNow).TopSessions); n != 2 {
			t.Fatalf("expected 2 TopSessions, got %d", n)
		}
	})

	t.Run("LongSessionTitle120PlusChars", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "历史消耗统计")
		clickMust(t, env.Tester, "7 天")
		verifyPNGFrame(t, env.Tester, "r3_long_title_truncation.png")
	})

	t.Run("UnknownModelSlugFallback", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID:   "conv-unk",
			Title:            "Unknown Model",
			StepCount:        1,
			LastModifiedTime: anchorNow,
		})
		fx.AppendWALGenRow(t, "conv-unk", genSpec{
			GenIndex: 0, StepIndices: []int{1}, ModelSlug: "custom-unknown-model",
			InputTokens: 4000, OutputTokens: 500, Timestamp: anchorNow.Add(-10 * time.Minute),
		})
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		_ = svc.Refresh(context.Background())
		res := svc.Query(history.Range7D, anchorNow)
		if len(res.ByModel) == 0 || strings.TrimSpace(res.ByModel[0].Name) == "" {
			t.Fatalf("expected non-empty fallback model group name, got %+v", res.ByModel)
		}
	})

	t.Run("TieBreakCostDescThenTokensDesc", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		for _, spec := range []struct {
			id     string
			tokens int64
		}{
			{"conv-tie-low", 1000},
			{"conv-tie-high", 9000},
		} {
			fx.InsertSummary(t, summarySpec{
				ConversationID:   spec.id,
				Title:            spec.id,
				StepCount:        1,
				LastModifiedTime: anchorNow,
			})
			fx.AppendWALGenRow(t, spec.id, genSpec{
				GenIndex: 0, StepIndices: []int{1}, ModelSlug: "gemini-4-flash",
				InputTokens: 0, CacheReadTokens: spec.tokens, OutputTokens: 1000,
				Timestamp: anchorNow.Add(-5 * time.Minute),
			})
		}
		// Give identical pricing by using equal cost or check deterministic ordering.
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		_ = svc.Refresh(context.Background())
		res := svc.Query(history.Range7D, anchorNow)
		if len(res.TopSessions) != 2 {
			t.Fatalf("expected 2 TopSessions, got %d", len(res.TopSessions))
		}
		if res.TopSessions[0].TotalTokens < res.TopSessions[1].TotalTokens {
			t.Fatalf("expected higher-token session ranked first, got %+v", res.TopSessions)
		}
	})
}

// Feature #13: Read-Only SQLite Aggregation + Incremental Cache (Tier 1)
func TestR3SQLiteAggregator(t *testing.T) {
	t.Run("AggregatesRealSQLiteAndProtobuf", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, cachePath, calc)
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		res := svc.Query(history.RangeAll, anchorNow)
		if res.RequestCount < 9 || res.TotalTokens <= 0 || res.TotalCostUSD <= 0 {
			t.Fatalf("unexpected RangeAll aggregation: %+v", res)
		}
	})

	t.Run("IncrementalCacheSkipsUnchangedDBs", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, cachePath, calc)
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh 1: %v", err)
		}
		raw1, err := os.ReadFile(cachePath)
		if err != nil || len(raw1) == 0 {
			t.Fatalf("expected usage_cache.json written on Refresh 1: err=%v", err)
		}
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh 2: %v", err)
		}
		res1 := svc.Query(history.RangeAll, anchorNow)
		svc2 := history.NewAggregator(fx.RootDir, cachePath, calc)
		if err := svc2.Refresh(context.Background()); err != nil {
			t.Fatalf("svc2.Refresh: %v", err)
		}
		res2 := svc2.Query(history.RangeAll, anchorNow)
		if res1.TotalTokens != res2.TotalTokens || res1.RequestCount != res2.RequestCount {
			t.Fatalf("cache mismatch across reload: %+v vs %+v", res1, res2)
		}
	})

	t.Run("CacheInvalidatesOnMtimeOrSizeChange", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, cachePath, calc)
		_ = svc.Refresh(context.Background())
		before := svc.Query(history.RangeAll, anchorNow)

		fx.AppendWALGenRow(t, "conv-today-active", genSpec{
			GenIndex: 10, StepIndices: []int{20}, ModelSlug: "gemini-4-neon",
			InputTokens: 5000, CacheReadTokens: 15000, OutputTokens: 600,
			Timestamp: anchorNow.Add(-2 * time.Minute),
		})
		fx.Checkpoint(t, "conv-today-active")
		_ = svc.Refresh(context.Background())
		after := svc.Query(history.RangeAll, anchorNow)
		if after.RequestCount != before.RequestCount+1 || after.TotalTokens <= before.TotalTokens {
			t.Fatalf("expected updated totals after DB change: before=%+v after=%+v", before, after)
		}
	})

	t.Run("ConsumptionSinceWindowFilter", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		_ = svc.Refresh(context.Background())

		recent := svc.ConsumptionSince(anchorNow.Add(-35 * time.Minute))
		if recent.GeminiTokens <= 0 || recent.ClaudeTokens <= 0 {
			t.Fatalf("expected both Gemini & Claude recent consumption, got %+v", recent)
		}
		empty := svc.ConsumptionSince(anchorNow.Add(1 * time.Hour))
		if empty.GeminiTokens != 0 || empty.ClaudeTokens != 0 {
			t.Fatalf("expected 0 consumption for future window, got %+v", empty)
		}
	})

	t.Run("PoolAndSubModelClassification", func(t *testing.T) {
		cases := []struct {
			slug    string
			display string
			pool    metrics.ModelPool
		}{
			{"gemini-4-neon", "Gemini 4 Neon (High)", metrics.PoolGemini},
			{"gemini-4-flash", "Gemini 4 Flash", metrics.PoolGemini},
			{"gemini-pro-orchestrator", "Orchestrator (Carbon)", metrics.PoolGemini},
			{"claude-sonnet-4", "Claude 4.5 Sonnet", metrics.PoolClaudeGPT},
			{"gpt-5", "GPT-5.2 / Codex", metrics.PoolClaudeGPT},
		}
		for _, tc := range cases {
			if got := metrics.ClassifyPool(tc.slug, tc.display); got != tc.pool {
				t.Fatalf("ClassifyPool(%q, %q) = %q, want %q", tc.slug, tc.display, got, tc.pool)
			}
			if sub := metrics.ClassifySubModel(tc.slug, tc.display); strings.TrimSpace(sub) == "" {
				t.Fatalf("ClassifySubModel(%q, %q) returned empty string", tc.slug, tc.display)
			}
		}
	})
}

// Feature #13: Read-Only SQLite Aggregation + Incremental Cache (Tier 2 Boundaries)
func TestR3SQLiteBoundaries(t *testing.T) {
	t.Run("EmptyAndMissingDirectories", func(t *testing.T) {
		calc, _ := metrics.NewDefaultCalculator()
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		svc := history.NewAggregator(missing, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh on missing dir should not fail fatally: %v", err)
		}
		res := svc.Query(history.RangeAll, anchorNow)
		if res.RequestCount != 0 || res.TotalTokens != 0 {
			t.Fatalf("expected empty result for missing dir, got %+v", res)
		}
	})

	t.Run("WALActiveConcurrentWriter", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		// Writer handle remains open with uncheckpointed WAL rows.
		walPath := filepath.Join(fx.RootDir, "conversations", "conv-today-active.db-wal")
		if info, err := os.Stat(walPath); err != nil || info.Size() == 0 {
			t.Fatalf("expected non-empty WAL file %s", walPath)
		}
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh over active WAL: %v", err)
		}
		if res := svc.Query(history.RangeToday, anchorNow); res.RequestCount == 0 {
			t.Fatalf("expected active WAL rows read cleanly")
		}
	})

	t.Run("OrphanSubagentDBWithoutSummary", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		_ = svc.Refresh(context.Background())

		// Unlisted DB (unlisted-gpt-1) + orphan-sub-1 must be included in totals.
		today := svc.Query(history.RangeToday, anchorNow)
		if today.RequestCount < 5 {
			t.Fatalf("expected root + orphan subagent + unlisted DB requests included, got %d", today.RequestCount)
		}
	})

	t.Run("IsEmptyFailureAndCorruptProtobufRows", func(t *testing.T) {
		fx := newAntigravityFixture(t)
		fx.InsertSummary(t, summarySpec{
			ConversationID: "conv-resilient", Title: "Resilient DB", StepCount: 3, LastModifiedTime: anchorNow,
		})
		fx.AppendWALGenRow(t, "conv-resilient", genSpec{GenIndex: 0, StepIndices: []int{1}, Error: "canceled"})
		fx.AppendRawGenRow(t, "conv-resilient", 1, []byte{0xff, 0xaa, 0x00})
		fx.AppendWALGenRow(t, "conv-resilient", genSpec{
			GenIndex: 2, StepIndices: []int{2}, ModelSlug: "gemini-4-neon",
			InputTokens: 3000, OutputTokens: 300, Timestamp: anchorNow.Add(-5 * time.Minute),
		})

		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, filepath.Join(t.TempDir(), "usage_cache.json"), calc)
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if res := svc.Query(history.Range7D, anchorNow); res.RequestCount != 1 {
			t.Fatalf("expected 1 valid request skipping failure & corrupt rows, got %d", res.RequestCount)
		}
	})

	t.Run("CorruptUsageCacheJSONRecovery", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
		if err := os.WriteFile(cachePath, []byte("{broken json"), 0o644); err != nil {
			t.Fatalf("write corrupt cache: %v", err)
		}
		calc, _ := metrics.NewDefaultCalculator()
		svc := history.NewAggregator(fx.RootDir, cachePath, calc)
		if err := svc.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh recovery from corrupt cache: %v", err)
		}
		raw, err := os.ReadFile(cachePath)
		if err != nil || !json.Valid(raw) {
			t.Fatalf("expected valid usage_cache.json rewritten after recovery, err=%v", err)
		}
	})
}
