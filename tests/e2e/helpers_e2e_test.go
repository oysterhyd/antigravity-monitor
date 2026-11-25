//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func newE2EDesktopApp(t *testing.T, fx *antigravityFixture, themeMode, lang string, preCalibrate *quota.CalibrationInput) *e2eEnv {
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

	quotaSvc := quota.NewService(histPath)
	if preCalibrate != nil {
		if _, err := quotaSvc.Calibrate(*preCalibrate, anchorNow); err != nil {
			t.Fatalf("preCalibrate: %v", err)
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
		HistoryPath: histPath,
		CachePath:   cachePath,
		App:         app,
		Tester:      tt,
	}
}

type quotaJSONPayload struct {
	Account   string        `json:"account"`
	Plan      string        `json:"plan"`
	UpdatedAt string        `json:"updated_at"`
	Gemini    quotaJSONPool `json:"gemini"`
	ClaudeGPT quotaJSONPool `json:"claude_gpt"`
}

type quotaJSONPool struct {
	RemainingPct float64          `json:"remaining_pct"`
	ResetsInSec  int64            `json:"resets_in_sec"`
	ResetsAt     string           `json:"resets_at"`
	Models       []quotaJSONModel `json:"models,omitempty"`
}

type quotaJSONModel struct {
	Name         string  `json:"name"`
	RemainingPct float64 `json:"remaining_pct"`
}

func defaultQuotaJSONPayload(geminiPct, claudePct float64) quotaJSONPayload {
	return quotaJSONPayload{
		Account:   "oyster@gmail.com",
		Plan:      "Google AI Pro",
		UpdatedAt: anchorNow.Format(time.RFC3339),
		Gemini: quotaJSONPool{
			RemainingPct: geminiPct,
			ResetsInSec:  13320,
			ResetsAt:     anchorNow.Add(13320 * time.Second).Format(time.RFC3339),
			Models: []quotaJSONModel{
				{Name: "Gemini 4 Neon (High)", RemainingPct: geminiPct - 3},
				{Name: "Gemini 4 Flash", RemainingPct: 89},
			},
		},
		ClaudeGPT: quotaJSONPool{
			RemainingPct: claudePct,
			ResetsInSec:  8100,
			ResetsAt:     anchorNow.Add(8100 * time.Second).Format(time.RFC3339),
			Models: []quotaJSONModel{
				{Name: "Claude 4.5 Sonnet / Opus", RemainingPct: claudePct - 3},
				{Name: "GPT-5.2 / Codex", RemainingPct: 54},
			},
		},
	}
}

func writeQuotaJSONFile(t *testing.T, payload quotaJSONPayload) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "quota_source.json")
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal quota JSON: %v", err)
	}
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatalf("write quota JSON %s: %v", p, err)
	}
	return p
}

func startLocalQuotaJSONServer(t *testing.T, initial quotaJSONPayload) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var statusCode atomic.Int32
	statusCode.Store(http.StatusOK)

	var bodyVal atomic.Value
	raw, err := json.Marshal(initial)
	if err != nil {
		t.Fatalf("marshal initial quota JSON: %v", err)
	}
	bodyVal.Store(string(raw))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := int(statusCode.Load())
		if code != http.StatusOK {
			http.Error(w, "simulated source error", code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyVal.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	return srv, &statusCode, &bodyVal
}

func writeQuotaHistoryFixture(t *testing.T, historyPath string, now time.Time, geminiPct, claudePct float64, withReset bool) *quota.Service {
	t.Helper()
	svc := quota.NewService(historyPath)
	_, err := svc.Calibrate(quota.CalibrationInput{
		GeminiRemainingPct: 92,
		GeminiResetIn:      5 * time.Hour,
		ClaudeRemainingPct: 78,
		ClaudeResetIn:      4 * time.Hour,
	}, now.Add(-6*time.Hour))
	if err != nil {
		t.Fatalf("seed initial calibrate: %v", err)
	}
	svc.SampleNow(now.Add(-4*time.Hour), history.PoolConsumption{
		GeminiTokens:  120000,
		GeminiCostUSD: 1.5,
		ClaudeTokens:  80000,
		ClaudeCostUSD: 2.0,
	})
	if withReset {
		// Advance past reset window to generate a reset marker, then calibrate to target levels.
		svc.SampleNow(now.Add(-30*time.Minute), history.PoolConsumption{})
	}
	if _, err := svc.Calibrate(quota.CalibrationInput{
		GeminiRemainingPct: geminiPct,
		GeminiResetIn:      3*time.Hour + 42*time.Minute,
		ClaudeRemainingPct: claudePct,
		ClaudeResetIn:      2*time.Hour + 15*time.Minute,
	}, now); err != nil {
		t.Fatalf("seed target calibrate: %v", err)
	}
	return svc
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
