package quota

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshStoresBothWindowsAndRetainsLastReadingOnFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "quota_history.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"calibrated":true,"gemini":{"remaining_pct":80}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reading := Reading{
		Gemini: PoolQuota{
			FiveHour: WindowQuota{RemainingPct: 78, ResetAt: now.Add(time.Hour)},
			Weekly:   WindowQuota{RemainingPct: 19, ResetAt: now.AddDate(0, 0, 6)},
		},
		ClaudeGPT: PoolQuota{FiveHour: WindowQuota{RemainingPct: 100}, Weekly: WindowQuota{RemainingPct: 100}},
	}
	var readErr error
	service := NewService(path, func(context.Context) (Reading, error) { return reading, readErr })
	if got := service.State(now); !got.UpdatedAt.IsZero() || got.HasLowQuota {
		t.Fatalf("legacy history treated as current: %+v", got)
	}
	if err := service.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	afterReset := now.Add(2 * time.Hour)
	readErr = ErrCLIUsage
	if err := service.Refresh(ctx, afterReset); !errors.Is(err, readErr) {
		t.Fatalf("refresh error = %v", err)
	}
	stale := service.State(afterReset)
	if stale.Reading != reading || !stale.HasLowQuota || !errors.Is(stale.SourceError, readErr) ||
		len(stale.FiveHourTrend.Last24h) != 1 || len(stale.WeeklyTrend.Last24h) != 1 {
		t.Fatalf("last successful sample was changed: %+v", stale)
	}

	readErr = nil
	reading.Gemini.FiveHour = WindowQuota{RemainingPct: 100}
	reading.Gemini.Weekly.RemainingPct = 18
	if err := service.Refresh(ctx, afterReset); err != nil {
		t.Fatal(err)
	}
	reloaded := NewService(path, nil).State(afterReset)
	if reloaded.Reading != reading || reloaded.SourceError != nil || !reloaded.HasLowQuota {
		t.Fatalf("unexpected reloaded state: %+v", reloaded)
	}
	if samples := reloaded.FiveHourTrend.Last24h; len(samples) != 2 || !samples[1].ResetEvent || samples[1].GeminiPct != 100 {
		t.Fatalf("five-hour samples = %+v", samples)
	}
	if samples := reloaded.WeeklyTrend.Last24h; len(samples) != 2 || samples[1].ResetEvent || samples[1].GeminiPct != 18 {
		t.Fatalf("weekly samples = %+v", samples)
	}
	if days := reloaded.FiveHourTrend.Last7d; len(days) != 1 || days[0].GeminiPct != 78 {
		t.Fatalf("daily low samples = %+v", days)
	}
}

func TestFrequentRefreshPreservesSevenDaysOfTrend(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	reading := Reading{Gemini: PoolQuota{FiveHour: WindowQuota{RemainingPct: 85}}}
	service := NewService("", func(context.Context) (Reading, error) { return reading, nil })

	for now := start; now.Before(start.Add(trendRetention)); now = now.Add(30 * time.Second) {
		if err := service.Refresh(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	now := start.Add(trendRetention - 30*time.Second)
	reading.Gemini.FiveHour.RemainingPct = 70
	if err := service.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	snapshot := service.State(now)
	if snapshot.Gemini.FiveHour.RemainingPct != 70 || !snapshot.UpdatedAt.Equal(now) {
		t.Fatalf("latest reading was not applied: %+v", snapshot.Reading)
	}
	for _, trend := range []Trend{snapshot.FiveHourTrend, snapshot.WeeklyTrend} {
		if len(trend.Last24h) != int(24*time.Hour/trendSampleInterval)+1 || len(trend.Last7d) != 7 {
			t.Fatalf("retained trend contains %d points and %d days", len(trend.Last24h), len(trend.Last7d))
		}
	}
}
