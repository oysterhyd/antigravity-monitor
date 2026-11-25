//go:build e2e

package e2e_test

import (
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/quota"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

func TestR2QuotaCardsShowBothWindows(t *testing.T) {
	reading := quotaReading(anchorNow, 78, 100)
	env := newE2EDesktopApp(t, seedRealisticHistoryRoot(t), theme.ModeLight, "en", &reading)
	clickMust(t, env.Tester, "Account Quota")
	for _, text := range []string{
		"Gemini Models", "Claude & GPT Models", "5-hour limit", "Weekly limit",
		"78%", "60%", "85%", "100%", "Resets in 3h 42m", "Resets in 6d 3h", "Reset time unknown",
	} {
		mustHaveText(t, env.Tester, text)
	}
	mustNotHaveText(t, env.Tester, "Calibrate")
	mustNotHaveText(t, env.Tester, "Antigravity CLI")
	for _, label := range []string{"5-hour limit", "Weekly limit"} {
		if got := countQuotaText(env, label); got != 2 {
			t.Fatalf("expected %q only in the two cards, got %d occurrences", label, got)
		}
	}
	verifyPNGFrame(t, env.Tester, "quota-two-windows.png")

	clickMust(t, env.Tester, "7d")
	clickMust(t, env.Tester, "Claude & GPT Models")
	mustHaveText(t, env.Tester, "Quota Usage Trend")
	verifyPNGFrame(t, env.Tester, "quota-seven-day-trend.png")

	clickMust(t, env.Tester, "← Live View")
	if env.App.ActiveView() != desktop.ViewLive {
		t.Fatal("back button did not return to Live")
	}
}

func countQuotaText(env *e2eEnv, text string) int {
	count := 0
	for _, visible := range env.Tester.Texts() {
		if visible == text {
			count++
		}
	}
	return count
}

func TestR2QuotaRefreshAndUnavailableStatus(t *testing.T) {
	env := newE2EDesktopApp(t, seedRealisticHistoryRoot(t), theme.ModeDark, "en", nil)
	clickMust(t, env.Tester, "Account Quota")
	mustHaveText(t, env.Tester, "--%")
	mustHaveText(t, env.Tester, "No quota samples yet")

	env.quotaSource.Store(&quotaResult{err: quota.ErrCLISetup})
	clickMust(t, env.Tester, "↻ Refresh")
	waitForQuotaFrame(t, env, "Could not prepare quota reading. Check your connection and refresh.")

	reading := quotaReading(anchorNow, 74, 41)
	env.quotaSource.Store(&quotaResult{reading: reading})
	clickMust(t, env.Tester, "↻ Refresh")
	waitForQuotaFrame(t, env, "74%")
	snapshot := env.Quota.State(anchorNow)
	if snapshot.Reading != reading || len(snapshot.WeeklyTrend.Last24h) != 1 || len(snapshot.FiveHourTrend.Last24h) != 1 {
		t.Fatalf("expected both trends after refresh: %+v", snapshot)
	}

	env.quotaSource.Store(&quotaResult{err: quota.ErrCLIUsage})
	clickMust(t, env.Tester, "↻ Refresh")
	waitForQuotaFrame(t, env, "Refresh failed. Showing the last quota reading.")
	mustHaveText(t, env.Tester, "74%")
}

func TestR2QuotaTrendPlotsWeeklySamplesAndFiltersByModel(t *testing.T) {
	env := newE2EDesktopApp(t, seedRealisticHistoryRoot(t), theme.ModeLight, "en", nil)
	older := quotaReading(anchorNow, 78, 100)
	older.Gemini.Weekly.RemainingPct = 92
	older.ClaudeGPT.Weekly.RemainingPct = 63
	env.sampleQuota(t, anchorNow.Add(-48*time.Hour), older)
	env.sampleQuota(t, anchorNow.Add(-2*time.Hour), older)

	latest := older
	latest.Gemini.Weekly.RemainingPct = 55.637
	latest.ClaudeGPT.Weekly.RemainingPct = 43.287
	env.sampleQuota(t, anchorNow, latest)

	clickMust(t, env.Tester, "Account Quota")
	mustHaveText(t, env.Tester, "Weekly")
	for _, label := range []string{"56%", "43%"} {
		if got := countQuotaText(env, label); got != 2 {
			t.Fatalf("24h: expected %s in the card and trend, got %d occurrences", label, got)
		}
	}
	verifyPNGFrame(t, env.Tester, "quota-weekly-series.png")

	clickMust(t, env.Tester, "7d")
	mustHaveText(t, env.Tester, "Daily lowest remaining")
	for _, label := range []string{"56%", "43%"} {
		if got := countQuotaText(env, label); got != 2 {
			t.Fatalf("7d: expected %s in the card and trend, got %d occurrences", label, got)
		}
	}
	clickMust(t, env.Tester, "Gemini Models")
	if got := countQuotaText(env, "56%"); got != 2 {
		t.Fatalf("expected Gemini weekly quota in the card and trend, got %d occurrences", got)
	}
	if got := countQuotaText(env, "43%"); got != 1 {
		t.Fatalf("expected Claude/GPT weekly quota only in the card, got %d occurrences", got)
	}
}

func waitForQuotaFrame(t *testing.T, env *e2eEnv, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stepUI(env.Tester, 0)
		if env.Tester.HasText(want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	mustHaveText(t, env.Tester, want)
}
