package history

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMultiRootAggregatorMergesAndIncludesT3HelperCost(t *testing.T) {
	nativeRoot := t.TempDir()
	acpRoot := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	// Native root: conv-native
	sumNative := createTestSummariesDB(t, nativeRoot)
	insertSummaryRow(t, sumNative, "conv-native", "Native Session", "", "", "", "", now.Add(-2*time.Hour))
	_ = sumNative.Close()

	dbNative := createTestWALConvDB(t, nativeRoot, "conv-native")
	insertGenRow(t, dbNative, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 9000, 0, 500, 200, 10500, 512000, "", "", "", now.Add(-2*time.Hour))
	_ = dbNative.Close()

	// ACP root: conv-acp + conv-helper (T3 temp helper workspace, MUST be counted in history!)
	sumACP := createTestSummariesDB(t, acpRoot)
	insertSummaryRow(t, sumACP, "conv-acp", "T3 ACP Session", "", "", "", "", now.Add(-1*time.Hour))
	insertSummaryRow(t, sumACP, "conv-helper", "{", "", "", "", "", now.Add(-30*time.Minute))
	_ = sumACP.Close()

	dbACP := createTestWALConvDB(t, acpRoot, "conv-acp")
	insertGenRow(t, dbACP, 0, 1, "claude-sonnet-4-5", "Claude 4.5 Sonnet", 2000, 10000, 1000, 400, 100, 13400, 200000, "", "", "", now.Add(-1*time.Hour))
	_ = dbACP.Close()

	dbHelper := createTestWALConvDB(t, acpRoot, "conv-helper")
	insertGenRow(t, dbHelper, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 300, 0, 0, 50, 0, 350, 512000, "", "", "", now.Add(-30*time.Minute))
	_ = dbHelper.Close()

	svc := NewMultiRootAggregator([]string{nativeRoot, acpRoot}, cachePath, nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh #1: %v", err)
	}
	st1 := svc.LastStats()
	if st1.Misses != 3 || st1.Hits != 0 {
		t.Fatalf("Refresh #1 stats = %+v, want Misses=3 Hits=0", st1)
	}

	res := svc.Query(RangeToday, now)
	if res.RequestCount != 3 || res.SessionCount != 3 {
		t.Fatalf("Query(today) reqs=%d sessions=%d, want 3 and 3", res.RequestCount, res.SessionCount)
	}
	wantTotalTokens := int64((1000 + 9000 + 500) + (2000 + 10000 + 1000 + 400) + (300 + 50))
	if res.TotalTokens != wantTotalTokens {
		t.Fatalf("TotalTokens = %d, want %d", res.TotalTokens, wantTotalTokens)
	}

	// Second refresh must hit incremental cache for all 3 conversations across both roots.
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh #2: %v", err)
	}
	st2 := svc.LastStats()
	if st2.Hits != 3 || st2.Misses != 0 || st2.Pruned != 0 {
		t.Fatalf("Refresh #2 stats = %+v, want Hits=3 Misses=0 Pruned=0", st2)
	}
}
