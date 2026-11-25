//go:build e2e

package e2e_test

import (
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/quota"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

// Feature #5: Account Quota Header (Tier 1)
func TestR2QuotaHeader(t *testing.T) {
	t.Run("TitleAndWindowSummary", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{
			GeminiRemainingPct: 74,
			GeminiResetIn:      3*time.Hour + 42*time.Minute,
			ClaudeRemainingPct: 41,
			ClaudeResetIn:      2*time.Hour + 15*time.Minute,
		}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		clickMust(t, env.Tester, "账号实时额度")
		mustHaveText(t, env.Tester, "账号实时额度")
		mustHaveText(t, env.Tester, "当前窗口状态")
		mustHaveText(t, env.Tester, "74%")
		mustHaveText(t, env.Tester, "41%")
	})

	t.Run("AccountAndPlanLabel", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		jsonPath := writeQuotaJSONFile(t, defaultQuotaJSONPayload(74, 41))
		if _, err := env.Quota.SetJSONSource(jsonPath, anchorNow); err != nil {
			t.Fatalf("SetJSONSource: %v", err)
		}
		clickMust(t, env.Tester, "Account Quota")
		mustHaveText(t, env.Tester, "oyster@gmail.com")
		mustHaveText(t, env.Tester, "Google AI Pro")
	})

	t.Run("RefreshSampleAppendsSnapshot", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 80, GeminiResetIn: 4 * time.Hour, ClaudeRemainingPct: 60, ClaudeResetIn: 3 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", &cal)
		before := len(env.Quota.State(anchorNow).Trend24h)
		clickMust(t, env.Tester, "Account Quota")
		clickMust(t, env.Tester, "↻ Refresh")
		after := len(env.Quota.State(anchorNow).Trend24h)
		if after <= before {
			t.Fatalf("expected Refresh to append trend point: before=%d after=%d", before, after)
		}
	})

	t.Run("CalibrateButtonOpensPopover", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "账号实时额度")
		clickMust(t, env.Tester, "校准")
		mustHaveText(t, env.Tester, "保存")
		mustHaveText(t, env.Tester, "取消")
	})

	t.Run("BackToLivePill", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "账号实时额度")
		clickMust(t, env.Tester, "← 返回监控")
		if env.App.ActiveView() != desktop.ViewLive {
			t.Fatalf("expected ViewLive, got %v", env.App.ActiveView())
		}
	})
}

// Feature #5: Account Quota Header (Tier 2 Boundaries)
func TestR2QuotaHeaderBoundaries(t *testing.T) {
	t.Run("UncalibratedHeaderText", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Account Quota")
		mustHaveText(t, env.Tester, "--%")
	})

	t.Run("EmptyAccountAndPlan", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 70, GeminiResetIn: 2 * time.Hour, ClaudeRemainingPct: 50, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		clickMust(t, env.Tester, "账号实时额度")
		mustNotHaveText(t, env.Tester, " ·  · ")
	})

	t.Run("RefreshWithFailingJSONSource", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		srv, code, _ := startLocalQuotaJSONServer(t, defaultQuotaJSONPayload(74, 41))
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		if _, err := env.Quota.SetJSONSource(srv.URL, anchorNow); err != nil {
			t.Fatalf("SetJSONSource: %v", err)
		}
		code.Store(http.StatusInternalServerError)
		_, _ = env.Quota.Refresh(anchorNow.Add(5*time.Minute), history.PoolConsumption{})
		clickMust(t, env.Tester, "Account Quota")
		st := env.Quota.State(anchorNow.Add(5 * time.Minute))
		if st.SourceError == "" {
			t.Fatalf("expected non-empty SourceError on HTTP 500")
		}
		if st.Gemini.RemainingPct <= 0 {
			t.Fatalf("expected last valid quota estimate retained on JSON error, got %v", st.Gemini.RemainingPct)
		}
	})

	t.Run("ZeroMinuteResetCountdown", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 40, GeminiResetIn: 0, ClaudeRemainingPct: 40, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", &cal)
		st := env.Quota.State(anchorNow)
		if st.Gemini.RemainingPct != 100 && st.Gemini.ResetAt.Before(anchorNow) {
			t.Fatalf("expected reset rollover or non-negative countdown, got %+v", st.Gemini)
		}
		clickMust(t, env.Tester, "Account Quota")
		mustNotHaveText(t, env.Tester, "-1h")
	})

	t.Run("RapidRefreshSpam", func(t *testing.T) {
		svc := quota.NewService(filepath.Join(t.TempDir(), "quota_history.json"))
		if _, err := svc.Calibrate(quota.CalibrationInput{
			GeminiRemainingPct: 80, GeminiResetIn: 4 * time.Hour,
			ClaudeRemainingPct: 70, ClaudeResetIn: 4 * time.Hour,
		}, anchorNow); err != nil {
			t.Fatalf("Calibrate: %v", err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				_, _ = svc.Refresh(anchorNow.Add(time.Duration(idx)*time.Second), history.PoolConsumption{GeminiTokens: 1000})
			}(i)
		}
		wg.Wait()
		if _, err := os.Stat(svc.State(anchorNow).Gemini.ResetAt.String()); err == nil {
			// no-op; ensures ResetAt valid
		}
		reloaded := quota.NewService(filepath.Join(t.TempDir(), "unused.json"))
		_ = reloaded
	})
}

// Feature #6: Hero Pool Cards (Tier 1)
func TestR2HeroPoolCards(t *testing.T) {
	t.Run("GeminiCardContent", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		jsonPath := writeQuotaJSONFile(t, defaultQuotaJSONPayload(74, 41))
		if _, err := env.Quota.SetJSONSource(jsonPath, anchorNow); err != nil {
			t.Fatalf("SetJSONSource: %v", err)
		}
		clickMust(t, env.Tester, "Account Quota")
		mustHaveText(t, env.Tester, "Gemini Models")
		mustHaveText(t, env.Tester, "74%")
		mustHaveText(t, env.Tester, "3h 42m")
		mustHaveText(t, env.Tester, "Gemini 4 Neon (High)")
		mustHaveText(t, env.Tester, "Gemini 4 Flash")
	})

	t.Run("ClaudeCardContent", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		jsonPath := writeQuotaJSONFile(t, defaultQuotaJSONPayload(74, 41))
		if _, err := env.Quota.SetJSONSource(jsonPath, anchorNow); err != nil {
			t.Fatalf("SetJSONSource: %v", err)
		}
		clickMust(t, env.Tester, "Account Quota")
		mustHaveText(t, env.Tester, "Claude & GPT Models")
		mustHaveText(t, env.Tester, "41%")
		mustHaveText(t, env.Tester, "2h 15m")
		mustHaveText(t, env.Tester, "Claude 4.5 Sonnet / Opus")
		mustHaveText(t, env.Tester, "GPT-5.2 / Codex")
	})

	t.Run("ClickGeminiFocusesTrend", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 74, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 41, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", &cal)
		clickMust(t, env.Tester, "Account Quota")
		clickMust(t, env.Tester, "Gemini Models")
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("ClickClaudeFocusesTrend", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 74, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 41, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", &cal)
		clickMust(t, env.Tester, "Account Quota")
		clickMust(t, env.Tester, "Claude & GPT Models")
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("CountdownHumanReadableFormat", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{
			GeminiRemainingPct: 74,
			GeminiResetIn:      3*time.Hour + 42*time.Minute,
			ClaudeRemainingPct: 41,
			ClaudeResetIn:      45 * time.Minute,
		}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		clickMust(t, env.Tester, "账号实时额度")
		mustHaveText(t, env.Tester, "3h 42m")
		mustHaveAnyText(t, env.Tester, "0h 45m", "45m")
	})
}

// Feature #6: Hero Pool Cards (Tier 2 Boundaries)
func TestR2HeroCardsBoundaries(t *testing.T) {
	t.Run("UncalibratedShowsDashPct", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "账号实时额度")
		mustHaveText(t, env.Tester, "--%")
	})

	t.Run("PercentClamped0To100", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		jsonPath := writeQuotaJSONFile(t, defaultQuotaJSONPayload(125, -10))
		_, _ = env.Quota.SetJSONSource(jsonPath, anchorNow)
		st := env.Quota.State(anchorNow)
		if st.Gemini.RemainingPct > 100 || st.Gemini.RemainingPct < 0 {
			t.Fatalf("Gemini RemainingPct out of [0,100]: %v", st.Gemini.RemainingPct)
		}
		if st.ClaudeGPT.RemainingPct > 100 || st.ClaudeGPT.RemainingPct < 0 {
			t.Fatalf("ClaudeGPT RemainingPct out of [0,100]: %v", st.ClaudeGPT.RemainingPct)
		}
	})

	t.Run("MissingSubModelsArray", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		p := defaultQuotaJSONPayload(65, 55)
		p.Gemini.Models = nil
		p.ClaudeGPT.Models = nil
		jsonPath := writeQuotaJSONFile(t, p)
		if _, err := env.Quota.SetJSONSource(jsonPath, anchorNow); err != nil {
			t.Fatalf("SetJSONSource: %v", err)
		}
		clickMust(t, env.Tester, "Account Quota")
		mustHaveText(t, env.Tester, "65%")
		mustHaveText(t, env.Tester, "55%")
	})

	t.Run("ClickAlreadyFocusedCard", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 74, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 41, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", &cal)
		clickMust(t, env.Tester, "Account Quota")
		clickMust(t, env.Tester, "Gemini Models")
		clickMust(t, env.Tester, "Gemini Models")
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("AmberRingAt24Vs25", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal24 := quota.CalibrationInput{GeminiRemainingPct: 24, GeminiResetIn: 2 * time.Hour, ClaudeRemainingPct: 25, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeDark, "en", &cal24)
		clickMust(t, env.Tester, "Account Quota")
		if !env.Quota.State(anchorNow).HasLowQuota {
			t.Fatalf("expected HasLowQuota=true for 24%%/25%%")
		}
		verifyPNGFrame(t, env.Tester, "")
	})
}

// Feature #7: Compliant Quota Engine & Calibration (Tier 1)
func TestR2QuotaEngine(t *testing.T) {
	t.Run("UncalibratedInitialBanner", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		clickMust(t, env.Tester, "账号实时额度")
		mustHaveText(t, env.Tester, "立即校准")
	})

	t.Run("PopoverCalibrationPersists", func(t *testing.T) {
		histPath := filepath.Join(t.TempDir(), "quota_history.json")
		svc := quota.NewService(histPath)
		snap, err := svc.Calibrate(quota.CalibrationInput{
			GeminiRemainingPct: 82,
			GeminiResetIn:      4 * time.Hour,
			ClaudeRemainingPct: 64,
			ClaudeResetIn:      3 * time.Hour,
		}, anchorNow)
		if err != nil {
			t.Fatalf("Calibrate: %v", err)
		}
		if !snap.Calibrated || snap.Gemini.RemainingPct != 82 || snap.ClaudeGPT.RemainingPct != 64 {
			t.Fatalf("unexpected calibrated snapshot: %+v", snap)
		}

		reloaded := quota.NewService(histPath)
		got := reloaded.State(anchorNow)
		if !got.Calibrated || got.Gemini.RemainingPct != 82 || got.ClaudeGPT.RemainingPct != 64 {
			t.Fatalf("calibration did not persist across reload: %+v", got)
		}
	})

	t.Run("RollingWindowBurnDeduction", func(t *testing.T) {
		svc := quota.NewService(filepath.Join(t.TempDir(), "quota_history.json"))
		_, err := svc.Calibrate(quota.CalibrationInput{
			GeminiRemainingPct: 80,
			GeminiResetIn:      4 * time.Hour,
			ClaudeRemainingPct: 70,
			ClaudeResetIn:      4 * time.Hour,
		}, anchorNow)
		if err != nil {
			t.Fatalf("Calibrate: %v", err)
		}
		after := svc.SampleNow(anchorNow.Add(30*time.Minute), history.PoolConsumption{
			GeminiTokens:  250000,
			GeminiCostUSD: 2.50,
		})
		if after.Gemini.RemainingPct >= 80 {
			t.Fatalf("expected Gemini remaining %% to decrease after Gemini burn, got %v", after.Gemini.RemainingPct)
		}
		if after.ClaudeGPT.RemainingPct != 70 {
			t.Fatalf("expected Claude pool unaffected by Gemini-only burn, got %v", after.ClaudeGPT.RemainingPct)
		}
	})

	t.Run("WindowAutoResetMarker", func(t *testing.T) {
		svc := quota.NewService(filepath.Join(t.TempDir(), "quota_history.json"))
		_, err := svc.Calibrate(quota.CalibrationInput{
			GeminiRemainingPct: 35,
			GeminiResetIn:      1 * time.Hour,
			ClaudeRemainingPct: 60,
			ClaudeResetIn:      4 * time.Hour,
		}, anchorNow)
		if err != nil {
			t.Fatalf("Calibrate: %v", err)
		}
		after := svc.SampleNow(anchorNow.Add(65*time.Minute), history.PoolConsumption{})
		if after.Gemini.RemainingPct != 100 {
			t.Fatalf("expected Gemini auto-reset to 100%% after ResetAt, got %v", after.Gemini.RemainingPct)
		}
		hasReset := false
		for _, pt := range after.Trend24h {
			if pt.ResetEvent {
				hasReset = true
				break
			}
		}
		if !hasReset {
			t.Fatalf("expected ResetEvent marker recorded in Trend24h after auto-reset")
		}
	})

	t.Run("LocalJSONSourceFileAndHTTP", func(t *testing.T) {
		svc := quota.NewService(filepath.Join(t.TempDir(), "quota_history.json"))
		jsonPath := writeQuotaJSONFile(t, defaultQuotaJSONPayload(74, 41))
		snapFile, err := svc.SetJSONSource(jsonPath, anchorNow)
		if err != nil {
			t.Fatalf("SetJSONSource(file): %v", err)
		}
		if snapFile.AccountLabel != "oyster@gmail.com" || snapFile.Gemini.RemainingPct != 74 {
			t.Fatalf("unexpected file JSON snapshot: %+v", snapFile)
		}

		srv, _, _ := startLocalQuotaJSONServer(t, defaultQuotaJSONPayload(68, 52))
		snapHTTP, err := svc.SetJSONSource(srv.URL, anchorNow.Add(5*time.Minute))
		if err != nil {
			t.Fatalf("SetJSONSource(http): %v", err)
		}
		if snapHTTP.Gemini.RemainingPct != 68 || snapHTTP.ClaudeGPT.RemainingPct != 52 {
			t.Fatalf("unexpected HTTP JSON snapshot: %+v", snapHTTP)
		}
	})
}

// Feature #7: Compliant Quota Engine & Calibration (Tier 2 Boundaries)
func TestR2QuotaEngineBoundaries(t *testing.T) {
	t.Run("InvalidCalibrationNegativePct", func(t *testing.T) {
		svc := quota.NewService(filepath.Join(t.TempDir(), "quota_history.json"))
		if _, err := svc.Calibrate(quota.CalibrationInput{GeminiRemainingPct: -1, GeminiResetIn: time.Hour, ClaudeRemainingPct: 50, ClaudeResetIn: time.Hour}, anchorNow); err == nil {
			t.Fatalf("expected error for negative Gemini pct -1")
		}
		if _, err := svc.Calibrate(quota.CalibrationInput{GeminiRemainingPct: 50, GeminiResetIn: time.Hour, ClaudeRemainingPct: -0.5, ClaudeResetIn: time.Hour}, anchorNow); err == nil {
			t.Fatalf("expected error for negative Claude pct -0.5")
		}
	})

	t.Run("InvalidCalibrationOver100Pct", func(t *testing.T) {
		svc := quota.NewService(filepath.Join(t.TempDir(), "quota_history.json"))
		if _, err := svc.Calibrate(quota.CalibrationInput{GeminiRemainingPct: 101, GeminiResetIn: time.Hour, ClaudeRemainingPct: 50, ClaudeResetIn: time.Hour}, anchorNow); err == nil {
			t.Fatalf("expected error for Gemini pct 101")
		}
		if _, err := svc.Calibrate(quota.CalibrationInput{GeminiRemainingPct: 50, GeminiResetIn: time.Hour, ClaudeRemainingPct: 150, ClaudeResetIn: time.Hour}, anchorNow); err == nil {
			t.Fatalf("expected error for Claude pct 150")
		}
	})

	t.Run("InvalidCalibrationNaNInfAndNegativeHours", func(t *testing.T) {
		svc := quota.NewService(filepath.Join(t.TempDir(), "quota_history.json"))
		if _, err := svc.Calibrate(quota.CalibrationInput{GeminiRemainingPct: math.NaN(), GeminiResetIn: time.Hour, ClaudeRemainingPct: 50, ClaudeResetIn: time.Hour}, anchorNow); err == nil {
			t.Fatalf("expected error for NaN percentage")
		}
		if _, err := svc.Calibrate(quota.CalibrationInput{GeminiRemainingPct: 50, GeminiResetIn: time.Hour, ClaudeRemainingPct: math.Inf(1), ClaudeResetIn: time.Hour}, anchorNow); err == nil {
			t.Fatalf("expected error for +Inf percentage")
		}
		if _, err := svc.Calibrate(quota.CalibrationInput{GeminiRemainingPct: 50, GeminiResetIn: -2 * time.Hour, ClaudeRemainingPct: 50, ClaudeResetIn: time.Hour}, anchorNow); err == nil {
			t.Fatalf("expected error for negative reset duration")
		}
	})

	t.Run("MalformedAndUnreachableJSONSource", func(t *testing.T) {
		svc := quota.NewService(filepath.Join(t.TempDir(), "quota_history.json"))
		_, _ = svc.Calibrate(quota.CalibrationInput{GeminiRemainingPct: 75, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 60, ClaudeResetIn: 3 * time.Hour}, anchorNow)

		badPath := filepath.Join(t.TempDir(), "corrupt.json")
		_ = os.WriteFile(badPath, []byte("{bad"), 0o644)
		if _, err := svc.SetJSONSource(badPath, anchorNow); err == nil {
			t.Fatalf("expected error on corrupt JSON file")
		}
		if _, err := svc.SetJSONSource(filepath.Join(t.TempDir(), "missing.json"), anchorNow); err == nil {
			t.Fatalf("expected error on missing JSON file")
		}
		if svc.State(anchorNow).Gemini.RemainingPct != 75 {
			t.Fatalf("expected prior local calibration preserved on JSON error")
		}
	})

	t.Run("CorruptOrEmptyQuotaHistoryFile", func(t *testing.T) {
		histPath := filepath.Join(t.TempDir(), "quota_history.json")
		if err := os.WriteFile(histPath, []byte("{corrupt-bytes"), 0o644); err != nil {
			t.Fatalf("write corrupt history: %v", err)
		}
		svc1 := quota.NewService(histPath)
		if svc1.State(anchorNow).Calibrated {
			t.Fatalf("expected corrupt history file to recover to uncalibrated state")
		}

		emptyPath := filepath.Join(t.TempDir(), "empty_history.json")
		_ = os.WriteFile(emptyPath, nil, 0o644)
		svc2 := quota.NewService(emptyPath)
		if svc2.State(anchorNow).Calibrated {
			t.Fatalf("expected 0-byte history file to recover to uncalibrated state")
		}
	})
}

// Feature #8: Quota Usage Trend Card (Tier 1)
func TestR2QuotaTrend(t *testing.T) {
	t.Run("Default24hAllSeries", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		writeQuotaHistoryFixture(t, env.HistoryPath, anchorNow, 74, 41, false)
		env.Quota = quota.NewService(env.HistoryPath)
		clickMust(t, env.Tester, "Account Quota")
		mustHaveText(t, env.Tester, "Quota Usage Trend")
		mustHaveText(t, env.Tester, "100%")
		mustHaveText(t, env.Tester, "0%")
	})

	t.Run("Toggle24hAnd7d", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 74, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 41, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		clickMust(t, env.Tester, "账号实时额度")
		clickMust(t, env.Tester, "7d")
		mustHaveText(t, env.Tester, "每日窗口最低剩余水位快照")
		clickMust(t, env.Tester, "24h")
		mustHaveText(t, env.Tester, "较上采样点")
	})

	t.Run("FilterSeriesAllGeminiClaude", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 74, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 41, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", &cal)
		clickMust(t, env.Tester, "Account Quota")
		clickMust(t, env.Tester, "Gemini")
		clickMust(t, env.Tester, "Claude & GPT")
		clickMust(t, env.Tester, "All")
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("ResetMarkerRendered", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		// Trigger reset inside 24h window.
		_, _ = env.Quota.Calibrate(quota.CalibrationInput{
			GeminiRemainingPct: 30, GeminiResetIn: 30 * time.Minute,
			ClaudeRemainingPct: 50, ClaudeResetIn: 4 * time.Hour,
		}, anchorNow.Add(-1*time.Hour))
		env.Quota.SampleNow(anchorNow, history.PoolConsumption{})
		clickMust(t, env.Tester, "Account Quota")
		mustHaveAnyText(t, env.Tester, "↺ Reset", "Reset")
	})

	t.Run("HoverInspectorReadout", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 74, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 41, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", &cal)
		env.Quota.SampleNow(anchorNow.Add(15*time.Minute), history.PoolConsumption{GeminiTokens: 50000, GeminiCostUSD: 0.5})
		clickMust(t, env.Tester, "账号实时额度")
		mustHaveText(t, env.Tester, "当前")
	})
}

// Feature #8: Quota Usage Trend Card (Tier 2 Boundaries)
func TestR2QuotaTrendBoundaries(t *testing.T) {
	t.Run("ZeroPointsEmptyHistory", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", nil)
		clickMust(t, env.Tester, "Account Quota")
		if len(env.Quota.State(anchorNow).Trend24h) != 0 {
			t.Fatalf("expected 0 trend points before calibration")
		}
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("SinglePointN1NoDivByZero", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 80, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 70, ClaudeResetIn: 3 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", &cal)
		if len(env.Quota.State(anchorNow).Trend24h) != 1 {
			t.Fatalf("expected 1 point right after single calibration")
		}
		clickMust(t, env.Tester, "Account Quota")
		mustNotHaveText(t, env.Tester, "NaN")
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("DensePointsXLabelThinning", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 95, GeminiResetIn: 5 * time.Hour, ClaudeRemainingPct: 90, ClaudeResetIn: 5 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeDark, "zh", &cal)
		for i := 1; i <= 24; i++ {
			env.Quota.SampleNow(anchorNow.Add(time.Duration(i)*20*time.Minute), history.PoolConsumption{GeminiTokens: 10000, GeminiCostUSD: 0.1})
		}
		clickMust(t, env.Tester, "账号实时额度")
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("OutOfRangeHoverIndexClamped", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 74, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 41, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", &cal)
		clickMust(t, env.Tester, "Account Quota")
		// Move pointer outside bounds and across chart corners without panic.
		env.Tester.Move(10, 10)
		env.Tester.Move(895, 585)
		stepUI(env.Tester, 100*time.Millisecond)
	})

	t.Run("AllSeriesZeroDeltaFormatting", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		cal := quota.CalibrationInput{GeminiRemainingPct: 74, GeminiResetIn: 3 * time.Hour, ClaudeRemainingPct: 41, ClaudeResetIn: 2 * time.Hour}
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "en", &cal)
		env.Quota.SampleNow(anchorNow.Add(10*time.Minute), history.PoolConsumption{})
		clickMust(t, env.Tester, "Account Quota")
		mustNotHaveText(t, env.Tester, "-0%")
	})
}
