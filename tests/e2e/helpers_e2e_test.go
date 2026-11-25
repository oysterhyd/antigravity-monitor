//go:build e2e

package e2e_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/quota"
	"github.com/oysterhyd/antigravity-monitor/internal/source"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

type e2eEnv struct {
	Fixture     *antigravityFixture
	Provider    *source.Provider
	History     *history.Service
	Quota       *quota.Service
	quotaSource *atomic.Pointer[quotaResult]
	HistoryPath string
	CachePath   string
	App         *desktop.App
	Tester      *ui.Tester
}

func seedRealisticHistoryRoot(t *testing.T) *antigravityFixture {
	t.Helper()
	fx := seedBaselineMultiRangeFixture(t)

	// Add one more running conversation + additional today/7D/30D telemetry for
	// Top 3 ranking, multi-model breakdown, and active multi-agent tree jumps.
	fx.InsertSummary(t, summarySpec{
		ConversationID:         "conv-sql",
		Title:                  "SQL 慢查询排查",
		StepCount:              8,
		LastModifiedTime:       anchorNow.Add(-10 * time.Minute),
		WorkspaceURIs:          `["file:///d%3A/work/sql-opt"]`,
		Status:                 "CASCADE_RUN_STATUS_IDLE",
		LastUserInputTime:      anchorNow.Add(-12 * time.Minute),
		LastUserInputStepIndex: 0,
	})
	fx.AppendWALGenRow(t, "conv-sql", genSpec{
		GenIndex:         0,
		StepIndices:      []int{1},
		LastStepIndex:    0,
		ModelSlug:        "claude-opus-5-5-high",
		ModelDisplay:     "Claude Opus 5.5 (High)",
		InputTokens:      32000,
		CacheReadTokens:  128000,
		CacheWriteTokens: 6000,
		OutputTokens:     4200,
		ContextUsed:      170200,
		ContextLimit:     200000,
		TTFT:             1600 * time.Millisecond,
		DecodeDuration:   5000 * time.Millisecond,
		Timestamp:        anchorNow.Add(-11 * time.Minute),
	})

	fx.InsertSummary(t, summarySpec{
		ConversationID:       "sub-browser",
		Title:                "",
		StepCount:            2,
		LastModifiedTime:     anchorNow.Add(-14 * time.Minute),
		WorkspaceURIs:        `["file:///d%3A/antigravity_monitor"]`,
		Status:               "CASCADE_RUN_STATUS_RUNNING",
		AgentName:            "browser",
		ParentConversationID: "conv-today-active",
		NestingDepth:         1,
		NotFullyIdle:         1,
	})
	fx.AppendWALGenRow(t, "sub-browser", genSpec{
		GenIndex:        0,
		StepIndices:     []int{1},
		LastStepIndex:   0,
		SubagentType:    "browser",
		RootCascadeID:   "conv-today-active",
		ParentCascadeID: "conv-today-active",
		ModelSlug:       "gemini-4-flash",
		ModelDisplay:    "Gemini 4 Flash",
		InputTokens:     2500,
		CacheReadTokens: 15000,
		OutputTokens:    450,
		ContextUsed:     17950,
		ContextLimit:    512000,
		TTFT:            600 * time.Millisecond,
		DecodeDuration:  900 * time.Millisecond,
		Timestamp:       anchorNow.Add(-14 * time.Minute),
	})

	return fx
}

func newE2EDesktopApp(t *testing.T, fx *antigravityFixture, themeMode, lang string, initialQuota *quota.Reading) *e2eEnv {
	t.Helper()
	var prov *source.Provider
	var err error
	if fx != nil {
		prov, err = source.NewProvider(source.Options{
			RootDir:        fx.RootDir,
			MaxOpenDBs:     5,
			DisableWatcher: true,
		})
		if err != nil {
			t.Fatalf("source.NewProvider: %v", err)
		}
		t.Cleanup(func() { _ = prov.Close() })
	}

	calc, err := metrics.NewDefaultCalculator()
	if err != nil {
		t.Fatalf("metrics.NewDefaultCalculator: %v", err)
	}

	tmpState := t.TempDir()
	cachePath := filepath.Join(tmpState, "usage_cache.json")
	histPath := filepath.Join(tmpState, "quota_history.json")

	dataRoot := filepath.Join(tmpState, "empty_root")
	if fx != nil {
		dataRoot = fx.RootDir
	}

	histSvc := history.NewAggregator(dataRoot, cachePath, calc)
	if err := histSvc.Refresh(context.Background()); err != nil {
		t.Fatalf("histSvc.Refresh: %v", err)
	}

	var quotaFeed atomic.Pointer[quotaResult]
	quotaSvc := quota.NewService(histPath, func(context.Context) (quota.Reading, error) {
		if result := quotaFeed.Load(); result != nil {
			return result.reading, result.err
		}
		return quota.Reading{}, quota.ErrCLIUsage
	})
	if initialQuota != nil {
		quotaFeed.Store(&quotaResult{reading: *initialQuota})
		if err := quotaSvc.Refresh(context.Background(), anchorNow); err != nil {
			t.Fatalf("seed quota: %v", err)
		}
	}

	settings := model.DefaultSettings()
	if themeMode != "" {
		settings.ThemeMode = themeMode
	}
	if lang != "" {
		settings.Language = lang
	}

	var snapProv model.SnapshotProvider
	if prov != nil {
		snapProv = prov
	}
	app := desktop.New(snapProv, desktop.Options{
		ThemeMode: settings.ThemeMode,
		Settings:  &settings,
		History:   histSvc,
		Quota:     quotaSvc,
		Now:       func() time.Time { return anchorNow },
	})
	t.Cleanup(func() { app.Close() })

	tt := newHeadlessTester(app)
	return &e2eEnv{
		Fixture:     fx,
		Provider:    prov,
		History:     histSvc,
		Quota:       quotaSvc,
		quotaSource: &quotaFeed,
		HistoryPath: histPath,
		CachePath:   cachePath,
		App:         app,
		Tester:      tt,
	}
}

type quotaResult struct {
	reading quota.Reading
	err     error
}

func quotaReading(now time.Time, gemini, claude float64) quota.Reading {
	return quota.Reading{
		Gemini: quota.PoolQuota{
			FiveHour: quota.WindowQuota{RemainingPct: gemini, ResetAt: now.Add(3*time.Hour + 42*time.Minute)},
			Weekly:   quota.WindowQuota{RemainingPct: 60, ResetAt: now.Add(6*24*time.Hour + 3*time.Hour)},
		},
		ClaudeGPT: quota.PoolQuota{
			FiveHour: quota.WindowQuota{RemainingPct: claude, ResetAt: now.Add(2*time.Hour + 15*time.Minute)},
			Weekly:   quota.WindowQuota{RemainingPct: 85},
		},
	}
}

func (env *e2eEnv) sampleQuota(t *testing.T, now time.Time, reading quota.Reading) quota.QuotaSnapshot {
	t.Helper()
	env.quotaSource.Store(&quotaResult{reading: reading})
	if err := env.Quota.Refresh(context.Background(), now); err != nil {
		t.Fatalf("refresh quota: %v", err)
	}
	return env.Quota.State(now)
}

func clickMust(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("click %q failed: %v; visible texts=%q", label, err, tt.Texts())
	}
	stepUI(tt, 200*time.Millisecond)
}

func mustHaveAnyText(t *testing.T, tt *ui.Tester, candidates ...string) {
	t.Helper()
	for _, c := range candidates {
		if tt.HasText(c) {
			return
		}
	}
	t.Fatalf("expected one of %v in view; got texts=%q", candidates, tt.Texts())
}

var _ = theme.ModeLight
