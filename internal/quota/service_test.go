package quota

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/history"
)

func TestCalibratePersistBurnAndAutoReset(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	histPath := filepath.Join(t.TempDir(), "quota_history.json")
	svc := NewService(histPath)

	if svc.State(now).Calibrated {
		t.Fatal("new service should start uncalibrated")
	}

	snap, err := svc.Calibrate(CalibrationInput{
		GeminiRemainingPct: 80,
		GeminiResetIn:      2 * time.Hour,
		ClaudeRemainingPct: 24,
		ClaudeResetIn:      4 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	if !snap.Calibrated || !snap.HasLowQuota || snap.Gemini.RemainingPct != 80 || snap.ClaudeGPT.RemainingPct != 24 {
		t.Fatalf("unexpected calibrated snap: %+v", snap)
	}
	if len(snap.Trend24h) != 1 {
		t.Fatalf("expected 1 trend point, got %d", len(snap.Trend24h))
	}

	// Reload from disk verifies atomic persistence.
	reloaded := NewService(histPath)
	rs := reloaded.State(now)
	if !rs.Calibrated || rs.Gemini.RemainingPct != 80 || rs.ClaudeGPT.RemainingPct != 24 {
		t.Fatalf("reloaded state mismatch: %+v", rs)
	}

	// Local rolling burn only deducts Gemini when only Gemini tokens burned.
	burned := reloaded.SampleNow(now.Add(30*time.Minute), history.PoolConsumption{
		GeminiTokens:  300000,
		GeminiCostUSD: 1.50,
	})
	if burned.Gemini.RemainingPct >= 80 {
		t.Fatalf("expected Gemini %% to drop after burn, got %v", burned.Gemini.RemainingPct)
	}
	if burned.ClaudeGPT.RemainingPct != 24 {
		t.Fatalf("expected Claude %% unchanged, got %v", burned.ClaudeGPT.RemainingPct)
	}

	// Auto-reset after Gemini window expires records ResetEvent marker.
	resetSnap := reloaded.SampleNow(now.Add(130*time.Minute), history.PoolConsumption{})
	if resetSnap.Gemini.RemainingPct != 100 {
		t.Fatalf("expected Gemini auto-reset to 100%%, got %v", resetSnap.Gemini.RemainingPct)
	}
	foundReset := false
	for _, pt := range resetSnap.Trend24h {
		if pt.ResetEvent {
			foundReset = true
		}
	}
	if !foundReset {
		t.Fatal("expected ResetEvent marker in Trend24h")
	}
}

func TestValidationAndJSONSources(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	svc := NewService(filepath.Join(t.TempDir(), "quota_history.json"))

	for _, bad := range []CalibrationInput{
		{GeminiRemainingPct: -1, ClaudeRemainingPct: 50},
		{GeminiRemainingPct: 101, ClaudeRemainingPct: 50},
		{GeminiRemainingPct: math.NaN(), ClaudeRemainingPct: 50},
		{GeminiRemainingPct: 50, ClaudeRemainingPct: math.Inf(1)},
		{GeminiRemainingPct: 50, GeminiResetIn: -time.Minute, ClaudeRemainingPct: 50},
	} {
		if _, err := svc.Calibrate(bad, now); err == nil {
			t.Fatalf("expected validation error for %+v", bad)
		}
	}

	// Reject remote non-loopback HTTP URLs.
	if _, err := svc.SetJSONSource("https://example.com/quota.json", now); err == nil {
		t.Fatal("expected ErrUnsafeJSONSource for remote URL")
	}

	// Valid local JSON file.
	jsonPath := filepath.Join(t.TempDir(), "quota.json")
	payload := `{"account":"oyster@gmail.com","plan":"Google AI Pro","gemini":{"remaining_pct":125,"resets_in_sec":7200},"claude_gpt":{"remaining_pct":-5,"resets_in_sec":3600}}`
	if err := os.WriteFile(jsonPath, []byte(payload), 0o644); err != nil {
		t.Fatalf("write json: %v", err)
	}
	snap, err := svc.SetJSONSource(jsonPath, now)
	if err != nil {
		t.Fatalf("SetJSONSource file: %v", err)
	}
	if snap.AccountLabel != "oyster@gmail.com" || snap.Gemini.RemainingPct != 100 || snap.ClaudeGPT.RemainingPct != 0 {
		t.Fatalf("expected clamped JSON values, got %+v", snap)
	}

	// Valid loopback HTTP endpoint + graceful fallback on 500.
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code != http.StatusOK {
			http.Error(w, "err", code)
			return
		}
		_, _ = w.Write([]byte(`{"account":"oyster@gmail.com","gemini":{"remaining_pct":72,"resets_in_sec":3600},"claude_gpt":{"remaining_pct":48,"resets_in_sec":3600}}`))
	}))
	defer srv.Close()

	snapHTTP, err := svc.SetJSONSource(srv.URL, now.Add(5*time.Minute))
	if err != nil || snapHTTP.Gemini.RemainingPct != 72 {
		t.Fatalf("SetJSONSource loopback HTTP: snap=%+v err=%v", snapHTTP, err)
	}

	code = http.StatusInternalServerError
	fallback, err := svc.Refresh(now.Add(10*time.Minute), history.PoolConsumption{})
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
	if fallback.SourceError == "" || fallback.Gemini.RemainingPct != 72 {
		t.Fatalf("expected fallback to preserve last valid estimate with SourceError, got %+v", fallback)
	}
}
