package history

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/source"
	"google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite"
)

func TestDefaultCachePath(t *testing.T) {
	got := DefaultCachePath()
	if got == "" {
		t.Fatal("DefaultCachePath returned empty string")
	}
	if filepath.Base(got) != "usage_cache.json" {
		t.Fatalf("DefaultCachePath() = %q, want base usage_cache.json", got)
	}
}

func TestNewAggregatorDefaultsAndInMemoryCache(t *testing.T) {
	root := t.TempDir()
	svc := NewAggregator(root, "", nil)
	if svc.calc == nil {
		t.Fatal("expected non-nil default Calculator")
	}
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh on empty root: %v", err)
	}
	res := svc.Query(Range7D, time.Now())
	if res.RequestCount != 0 || res.SessionCount != 0 || len(res.Buckets) != 7 {
		t.Fatalf("unexpected empty 7d query result: %+v", res)
	}
	resAll := svc.Query(RangeAll, time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC))
	if len(resAll.Buckets) != 1 || resAll.Buckets[0].Label != "Nov" || resAll.AvgCostUSD != 0 {
		t.Fatalf("unexpected empty all query result: %+v", resAll)
	}
}

func TestOpenReadOnlySQLite(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.db")
	if _, err := source.OpenReadOnlySQLite(missing); !os.IsNotExist(err) {
		t.Fatalf("OpenReadOnlySQLite(missing) err = %v, want os.ErrNotExist", err)
	}

	root := t.TempDir()
	dbWriter := createTestWALConvDB(t, root, "ro-check")
	insertGenRow(t, dbWriter, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 100, 0, 0, 10, 0, 110, 512000, "", "", "", time.Now().UTC())
	defer dbWriter.Close()

	dbPath := filepath.Join(root, "conversations", "ro-check.db")
	ro, err := source.OpenReadOnlySQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnlySQLite(existing): %v", err)
	}
	defer ro.Close()

	var count int
	if err := ro.QueryRow(`SELECT COUNT(*) FROM gen_metadata`).Scan(&count); err != nil {
		t.Fatalf("select from WAL db: %v", err)
	}
	if count != 1 {
		t.Fatalf("gen_metadata count = %d, want 1", count)
	}
	if _, err := ro.Exec(`DELETE FROM gen_metadata`); err == nil {
		t.Fatal("expected write statement on read-only SQLite handle to fail")
	}
}

func TestWALUncommittedFramesCacheHitAndInvalidation(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "cache", "usage_cache.json")

	// Create summaries DB (with root conversation title).
	sumDB := createTestSummariesDB(t, root)
	now := time.Date(2026, 11, 24, 15, 30, 0, 0, time.UTC)
	insertSummaryRow(t, sumDB, "conv-a", "Session Alpha", "", "", "", "", now)
	insertSummaryRow(t, sumDB, "conv-b", "Session Beta", "", "", "", "", now)
	_ = sumDB.Close()

	// Open conv-a with WAL and wal_autocheckpoint=0, keeping writer open (uncheckpointed in -wal).
	dbA := createTestWALConvDB(t, root, "conv-a")
	defer dbA.Close()
	insertGenRow(t, dbA, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 9000, 0, 500, 200, 10500, 512000, "", "", "", now.Add(-2*time.Hour))

	dbB := createTestWALConvDB(t, root, "conv-b")
	insertGenRow(t, dbB, 0, 1, "claude-sonnet-4-5", "Claude 4.5 Sonnet", 2000, 10000, 1000, 400, 100, 13400, 200000, "", "", "", now.Add(-1*time.Hour))
	_ = dbB.Close()

	svc := NewAggregator(root, cachePath, nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh #1: %v", err)
	}
	st1 := svc.LastStats()
	if st1.Misses != 2 || st1.Hits != 0 || st1.Pruned != 0 {
		t.Fatalf("Refresh #1 stats = %+v, want Misses=2 Hits=0 Pruned=0", st1)
	}

	// Verify no leftover temp files in cache dir.
	entries, err := os.ReadDir(filepath.Dir(cachePath))
	if err != nil {
		t.Fatalf("readdir cache dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "usage_cache.json" {
		t.Fatalf("unexpected files in cache dir: %v", entries)
	}

	// Verify both uncommitted WAL rows were read.
	res1 := svc.Query(RangeToday, now)
	if res1.RequestCount != 2 || res1.SessionCount != 2 {
		t.Fatalf("Query today counts = req:%d sess:%d, want 2/2", res1.RequestCount, res1.SessionCount)
	}

	// Refresh #2 on same service -> 2 hits, 0 misses.
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh #2: %v", err)
	}
	st2 := svc.LastStats()
	if st2.Hits != 2 || st2.Misses != 0 || st2.Pruned != 0 {
		t.Fatalf("Refresh #2 stats = %+v, want Hits=2 Misses=0 Pruned=0", st2)
	}

	// Reload fresh Service from usage_cache.json -> 2 hits, 0 misses.
	svcReload := NewAggregator(root, cachePath, nil)
	if err := svcReload.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh reload: %v", err)
	}
	stReload := svcReload.LastStats()
	if stReload.Hits != 2 || stReload.Misses != 0 || stReload.Pruned != 0 {
		t.Fatalf("Refresh reload stats = %+v, want Hits=2 Misses=0 Pruned=0", stReload)
	}

	// Append a new frame to conv-a -wal WITHOUT checkpointing while dbA stays open.
	time.Sleep(25 * time.Millisecond) // ensure OS mtime granularity advances
	insertGenRow(t, dbA, 1, 2, "gemini-4-flash", "Gemini 4 Flash", 500, 0, 0, 250, 50, 750, 512000, "", "", "", now.Add(-10*time.Minute))

	if err := svcReload.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after WAL append: %v", err)
	}
	stWal := svcReload.LastStats()
	if stWal.Misses != 1 || stWal.Hits != 1 || stWal.Pruned != 0 {
		t.Fatalf("Refresh after WAL append stats = %+v, want Misses=1 Hits=1 Pruned=0", stWal)
	}
	resWal := svcReload.Query(RangeToday, now)
	if resWal.RequestCount != 3 {
		t.Fatalf("RequestCount after WAL append = %d, want 3", resWal.RequestCount)
	}
}

func TestCachePruningCorruptAndVersionMismatchRecovery(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	db1 := createTestWALConvDB(t, root, "conv-1")
	insertGenRow(t, db1, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 0, 0, 100, 0, 1100, 512000, "", "", "", now)
	_ = db1.Close()

	db2 := createTestWALConvDB(t, root, "conv-2")
	insertGenRow(t, db2, 0, 1, "gemini-4-flash", "Gemini 4 Flash", 2000, 0, 0, 200, 0, 2200, 512000, "", "", "", now)
	_ = db2.Close()

	svc := NewAggregator(root, cachePath, nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	if svc.LastStats().Misses != 2 {
		t.Fatalf("initial misses = %d, want 2", svc.LastStats().Misses)
	}

	// Delete conv-2 DB files -> Pruned == 1.
	convDir := filepath.Join(root, "conversations")
	for _, suf := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(convDir, "conv-2.db"+suf))
	}
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after delete: %v", err)
	}
	stPrune := svc.LastStats()
	if stPrune.Pruned != 1 || stPrune.Hits != 1 || stPrune.Misses != 0 {
		t.Fatalf("prune stats = %+v, want Pruned=1 Hits=1 Misses=0", stPrune)
	}
	if got := svc.Query(RangeAll, now).RequestCount; got != 1 {
		t.Fatalf("RequestCount after prune = %d, want 1", got)
	}

	// Corrupt JSON recovery.
	if err := os.WriteFile(cachePath, []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}
	svcCorrupt := NewAggregator(root, cachePath, nil)
	if err := svcCorrupt.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after corrupt cache: %v", err)
	}
	if st := svcCorrupt.LastStats(); st.Misses != 1 || st.Hits != 0 {
		t.Fatalf("corrupt rebuild stats = %+v, want Misses=1 Hits=0", st)
	}
	if got := svcCorrupt.Query(RangeAll, now).RequestCount; got != 1 {
		t.Fatalf("RequestCount after corrupt rebuild = %d, want 1", got)
	}

	// Version mismatch recovery.
	if err := os.WriteFile(cachePath, []byte(`{"version":999,"pricing_hash":"bad","conversations":{}}`), 0o600); err != nil {
		t.Fatalf("write version mismatch cache: %v", err)
	}
	svcVer := NewAggregator(root, cachePath, nil)
	if err := svcVer.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after version mismatch: %v", err)
	}
	if st := svcVer.LastStats(); st.Misses != 1 || st.Hits != 0 {
		t.Fatalf("version mismatch rebuild stats = %+v, want Misses=1 Hits=0", st)
	}

	// Pricing config change invalidates cache via PricingHash.
	customCfg := model.PricingConfig{
		Version: 99,
		DefaultRule: model.PricingRule{
			ModelPattern:    "*",
			InputPerMillion: 50,
		},
	}
	svcPricing := NewAggregator(root, cachePath, metrics.NewCalculatorFromConfig(customCfg))
	if err := svcPricing.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh with changed pricing: %v", err)
	}
	if st := svcPricing.LastStats(); st.Misses != 1 || st.Hits != 0 {
		t.Fatalf("pricing hash mismatch stats = %+v, want Misses=1 Hits=0", st)
	}
}

func TestIsEmptyFailureThinkingAndCacheWriteAccounting(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)

	db := createTestWALConvDB(t, root, "conv-math")
	// Gen 0: empty failure (f5 error, all token counts 0) -> MUST be skipped.
	insertGenRow(t, db, 0, 1, "claude-sonnet-4-5", "Claude 4.5 Sonnet", 0, 0, 0, 0, 0, 0, 200000, "canceled", "", "", now)
	// Gen 1: valid Claude Sonnet 4 request with CacheWrite=10_000 and Output=1_000 (Thinking=400 already inside Output).
	insertGenRow(t, db, 1, 2, "claude-sonnet-4-5", "Claude 4.5 Sonnet", 2000, 50000, 10000, 1000, 400, 63000, 200000, "", "", "", now)
	_ = db.Close()

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	res := svc.Query(RangeToday, now)
	if res.RequestCount != 1 {
		t.Fatalf("RequestCount = %d, want 1 (empty failure must be skipped)", res.RequestCount)
	}
	if res.CacheReadTokens != 50000 {
		t.Fatalf("CacheReadTokens = %d, want 50000", res.CacheReadTokens)
	}
	// InputTokens folds CacheWriteTokens (2000 + 10000 = 12000)
	if res.InputTokens != 12000 {
		t.Fatalf("InputTokens = %d, want 12000", res.InputTokens)
	}
	// OutputTokens is 1000 (NOT 1000 + 400 thinking)
	if res.OutputTokens != 1000 {
		t.Fatalf("OutputTokens = %d, want 1000", res.OutputTokens)
	}
	if res.TotalTokens != res.CacheReadTokens+res.InputTokens+res.OutputTokens {
		t.Fatalf("TotalTokens invariant failed: %d != %d + %d + %d", res.TotalTokens, res.CacheReadTokens, res.InputTokens, res.OutputTokens)
	}

	// Claude Sonnet 4 rates: in=$3.0/M, cache_read=$0.30/M, cache_write=$3.75/M, out=$15.0/M.
	wantCost := (2000*3.0 + 50000*0.30 + 10000*3.75 + 1000*15.0) / 1e6
	wantSaved := (50000 * (3.0 - 0.30)) / 1e6
	if math.Abs(res.TotalCostUSD-wantCost) > 1e-12 {
		t.Fatalf("TotalCostUSD = %.12f, want %.12f", res.TotalCostUSD, wantCost)
	}
	if math.Abs(res.CacheSavedUSD-wantSaved) > 1e-12 {
		t.Fatalf("CacheSavedUSD = %.12f, want %.12f", res.CacheSavedUSD, wantSaved)
	}
	// CacheHitRate = 50000 / (2000 + 50000 + 10000) * 100
	wantHit := (50000.0 / 62000.0) * 100.0
	if math.Abs(res.CacheHitRate-wantHit) > 1e-6 {
		t.Fatalf("CacheHitRate = %.6f, want %.6f", res.CacheHitRate, wantHit)
	}
}

func TestOrphanSubagentRollupAndTimestampPriority(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC)

	// Case A: conversation_summaries.db present with root + conversation_parents link for missing subagent summary.
	sumDB := createTestSummariesDB(t, root)
	insertSummaryRow(t, sumDB, "root-1", "Root Title", "", "", "", "", now.Add(-2*time.Hour))
	// sub-1 is NOT in conversation_summaries, only in conversation_parents!
	insertParentRow(t, sumDB, "sub-1", "root-1")
	// orphan-agent has parent "deleted-root" which doesn't exist anywhere, and empty Title -> falls back to AgentName.
	insertSummaryRow(t, sumDB, "orphan-agent", "", "", "", "Explorer Agent", "deleted-root", now.Add(-1*time.Hour))
	_ = sumDB.Close()

	// root-1 uses step metadata timestamp (Priority 1).
	dbRoot := createTestWALConvDB(t, root, "root-1")
	insertGenRow(t, dbRoot, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 0, 0, 100, 0, 1100, 512000, "", "", "", now.Add(-30*time.Minute))
	_ = dbRoot.Close()

	// sub-1 has NO steps.metadata timestamp -> falls back to transcript.jsonl (Priority 2).
	dbSub := createTestWALConvDB(t, root, "sub-1")
	insertGenRow(t, dbSub, 0, 5, "claude-opus-5-5-high", "Claude Opus 5.5 (High)", 1000, 0, 0, 500, 0, 1500, 840000, "", "root-1", "", time.Time{})
	_ = dbSub.Close()
	writeTranscriptJSONL(t, root, "sub-1", now.Add(-20*time.Minute), 5)

	// orphan-agent has NO steps.metadata and NO transcript.jsonl -> falls back to summary LastModifiedTime (Priority 3),
	// and to file mtime (Priority 4) when conversation_summaries.db is absent.
	dbOrphan := createTestWALConvDB(t, root, "orphan-agent")
	insertGenRow(t, dbOrphan, 0, 1, "gemini-4-flash", "Gemini 4 Flash", 1000, 0, 0, 100, 0, 1100, 512000, "", "", "", time.Time{})
	_ = dbOrphan.Close()
	orphanMtime := now.Add(-15 * time.Minute)
	if err := os.Chtimes(filepath.Join(root, "conversations", "orphan-agent.db"), orphanMtime, orphanMtime); err != nil {
		t.Fatalf("Chtimes orphan: %v", err)
	}

	// file-mtime-only DB has NO summary row, NO step timestamp, NO transcript -> falls back to file mtime (Priority 4).
	dbMtime := createTestWALConvDB(t, root, "12345678-aaaa-bbbb-cccc")
	insertGenRow(t, dbMtime, 0, 1, "gemini-4-flash", "Gemini 4 Flash", 1000, 0, 0, 100, 0, 1100, 512000, "", "", "", time.Time{})
	_ = dbMtime.Close()
	mtimeTarget := now.Add(-5 * time.Minute)
	if err := os.Chtimes(filepath.Join(root, "conversations", "12345678-aaaa-bbbb-cccc.db"), mtimeTarget, mtimeTarget); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	res := svc.Query(RangeToday, now)
	// 4 DBs, where sub-1 rolls into root-1 -> 3 distinct root sessions!
	if res.RequestCount != 4 || res.SessionCount != 3 {
		t.Fatalf("RequestCount=%d SessionCount=%d, want 4 / 3", res.RequestCount, res.SessionCount)
	}
	if len(res.TopSessions) != 3 {
		t.Fatalf("len(TopSessions) = %d, want 3", len(res.TopSessions))
	}

	// root-1 has highest cost (Claude Opus 5.5 subagent rolled up into root-1) -> PrimaryPool should be ClaudeGPT!
	top := res.TopSessions[0]
	if top.ConvID != "root-1" || top.Title != "Root Title" || top.PrimaryPool != metrics.PoolClaudeGPT {
		t.Fatalf("TopSessions[0] = %+v, want root-1 / Root Title / claude_gpt", top)
	}

	// Verify orphan-agent kept its AgentName title and 12345678-... kept ConvID[:8].
	titles := map[string]string{}
	for _, s := range res.TopSessions {
		titles[s.ConvID] = s.Title
	}
	if titles["orphan-agent"] != "Explorer Agent" {
		t.Fatalf("orphan-agent title = %q, want Explorer Agent", titles["orphan-agent"])
	}
	if titles["12345678-aaaa-bbbb-cccc"] != "12345678" {
		t.Fatalf("12345678-... title = %q, want 12345678", titles["12345678-aaaa-bbbb-cccc"])
	}

	// Also verify missing conversation_summaries.db still rolls up via gen_metadata RootCascadeID.
	_ = os.Remove(filepath.Join(root, "conversation_summaries.db"))
	svcNoSum := NewAggregator(root, "", nil)
	if err := svcNoSum.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh without summaries DB: %v", err)
	}
	resNoSum := svcNoSum.Query(RangeToday, now)
	if resNoSum.SessionCount != 3 || resNoSum.RequestCount != 4 {
		t.Fatalf("without summaries DB: RequestCount=%d SessionCount=%d, want 4 / 3", resNoSum.RequestCount, resNoSum.SessionCount)
	}
}

func TestQueryRangesByModelTopSessionsAndConsumptionSince(t *testing.T) {
	root := t.TempDir()
	loc := time.FixedZone("UTC+8", 8*3600)
	// 2026-11-24 10:30:00 +08:00 (11th elapsed hour: Hour()+1 == 11)
	now := time.Date(2026, 11, 24, 10, 30, 0, 0, loc)

	sumDB := createTestSummariesDB(t, root)
	insertSummaryRow(t, sumDB, "sess-1", "", "Preview Title", "", "", "", now)
	insertSummaryRow(t, sumDB, "sess-2", "", "", "D:\\workspaces\\cool-app", "", "", now)
	insertSummaryRow(t, sumDB, "sess-3", "Sess Three", "", "", "", "", now)
	insertSummaryRow(t, sumDB, "sess-4", "Sess Four", "", "", "", "", now)
	_ = sumDB.Close()

	// sess-1: today at 09:15 +08:00 (high cost Claude Opus 5.5) + 16 months ago (to trigger "Jan '06" in RangeAll)
	db1 := createTestWALConvDB(t, root, "sess-1")
	insertGenRow(t, db1, 0, 1, "claude-opus-5-5-high", "Claude Opus 5.5 (High)", 10000, 0, 0, 2000, 0, 12000, 840000, "", "", "", time.Date(2026, 11, 24, 9, 15, 0, 0, loc))
	insertGenRow(t, db1, 1, 2, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 0, 0, 100, 0, 1100, 512000, "", "", "", time.Date(2025, 7, 10, 12, 0, 0, 0, loc))
	_ = db1.Close()

	// sess-2: 3 days ago (in 7d and W4 of 30d)
	db2 := createTestWALConvDB(t, root, "sess-2")
	insertGenRow(t, db2, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 50000, 0, 0, 5000, 0, 55000, 512000, "", "", "", time.Date(2026, 11, 21, 18, 0, 0, 0, loc))
	_ = db2.Close()

	// sess-3: 15 days ago (in W2 of 30d, outside 7d)
	db3 := createTestWALConvDB(t, root, "sess-3")
	insertGenRow(t, db3, 0, 1, "gemini-4-flash", "Gemini 4 Flash", 100000, 0, 0, 10000, 0, 110000, 512000, "", "", "", time.Date(2026, 11, 9, 12, 0, 0, 0, loc))
	_ = db3.Close()

	// sess-4: 28 days ago (in W1 of 30d) + exact nanosecond timestamp for ConsumptionSince test
	exactTS := time.Date(2026, 11, 24, 8, 0, 0, 123456789, loc)
	db4 := createTestWALConvDB(t, root, "sess-4")
	insertGenRow(t, db4, 0, 1, "gpt-5.2-codex", "GPT-5.2 Codex", 4000, 0, 0, 400, 0, 4400, 400000, "", "", "", time.Date(2026, 10, 27, 8, 0, 0, 0, loc))
	insertGenRow(t, db4, 1, 2, "gemini-4-hydrogen", "Gemini 4 Hydrogen (High)", 2000, 0, 0, 200, 0, 2200, 512000, "", "", "", exactTS)
	_ = db4.Close()

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// Today: 24 hourly buckets, labels 00:00..23:00
	qToday := svc.Query(RangeToday, now)
	if len(qToday.Buckets) != 24 || qToday.Buckets[0].Label != "00:00" || qToday.Buckets[23].Label != "23:00" {
		t.Fatalf("unexpected today buckets: len=%d first=%q last=%q", len(qToday.Buckets), qToday.Buckets[0].Label, qToday.Buckets[len(qToday.Buckets)-1].Label)
	}
	if qToday.RequestCount != 2 {
		t.Fatalf("today RequestCount = %d, want 2", qToday.RequestCount)
	}
	if math.Abs(qToday.AvgCostUSD-qToday.TotalCostUSD/11.0) > 1e-12 {
		t.Fatalf("today AvgCostUSD = %.12f, want %.12f", qToday.AvgCostUSD, qToday.TotalCostUSD/11.0)
	}
	if qToday.Buckets[9].TotalTokens != 12000 || qToday.Buckets[8].TotalTokens != 2200 {
		t.Fatalf("today hourly buckets mismatch: h8=%d h9=%d", qToday.Buckets[8].TotalTokens, qToday.Buckets[9].TotalTokens)
	}

	// 7D: 7 daily buckets ending 11/24
	q7d := svc.Query(Range7D, now)
	if len(q7d.Buckets) != 7 || q7d.Buckets[0].Label != "11/18" || q7d.Buckets[6].Label != "11/24" {
		t.Fatalf("unexpected 7d buckets: len=%d first=%q last=%q", len(q7d.Buckets), q7d.Buckets[0].Label, q7d.Buckets[6].Label)
	}
	if math.Abs(q7d.AvgCostUSD-q7d.TotalCostUSD/7.0) > 1e-12 {
		t.Fatalf("7d AvgCostUSD = %.12f, want %.12f", q7d.AvgCostUSD, q7d.TotalCostUSD/7.0)
	}

	// 30D: 4 weekly buckets W1..W4, and W4 == 7D totals
	q30d := svc.Query(Range30D, now)
	if len(q30d.Buckets) != 4 || q30d.Buckets[0].Label != "W1" || q30d.Buckets[3].Label != "W4" {
		t.Fatalf("unexpected 30d buckets: %+v", q30d.Buckets)
	}
	if q30d.Buckets[3].TotalTokens != q7d.TotalTokens || math.Abs(q30d.Buckets[3].TotalCostUSD-q7d.TotalCostUSD) > 1e-12 {
		t.Fatalf("30d W4 (%d, %.6f) != 7d (%d, %.6f)", q30d.Buckets[3].TotalTokens, q30d.Buckets[3].TotalCostUSD, q7d.TotalTokens, q7d.TotalCostUSD)
	}
	if len(q30d.TopSessions) != 3 {
		t.Fatalf("len(TopSessions) = %d, want 3 (capped from 4)", len(q30d.TopSessions))
	}
	// Verify fallback titles in TopSessions (Preview Title / cool-app)
	foundSess2 := false
	for _, s := range q30d.TopSessions {
		if s.ConvID == "sess-2" && s.Title != "cool-app" {
			t.Fatalf("sess-2 title = %q, want cool-app", s.Title)
		}
		if s.ConvID == "sess-2" {
			foundSess2 = true
		}
	}
	if !foundSess2 {
		t.Fatalf("expected sess-2 in TopSessions: %+v", q30d.TopSessions)
	}

	// ByModel sorted CostUSD desc and Share sums to ~100
	var shareSum float64
	for i, m := range q30d.ByModel {
		shareSum += m.Share
		if i > 0 && q30d.ByModel[i-1].CostUSD < m.CostUSD {
			t.Fatalf("ByModel not sorted desc by CostUSD: %+v", q30d.ByModel)
		}
	}
	if math.Abs(shareSum-100.0) > 1e-6 {
		t.Fatalf("ByModel Share sum = %.6f, want 100", shareSum)
	}

	// All: >12 months -> "Jan '06" layout ("Jul '25" .. "Nov '26")
	qAll := svc.Query(RangeAll, now)
	if len(qAll.Buckets) != 17 || qAll.Buckets[0].Label != "Jul '25" || qAll.Buckets[16].Label != "Nov '26" {
		t.Fatalf("unexpected all buckets: len=%d first=%q last=%q", len(qAll.Buckets), qAll.Buckets[0].Label, qAll.Buckets[len(qAll.Buckets)-1].Label)
	}

	// ConsumptionSince inclusive boundary check at nanosecond precision
	cAt := svc.ConsumptionSince(exactTS)
	if cAt.GeminiTokens != 2200 || cAt.ClaudeTokens != 12000 {
		t.Fatalf("ConsumptionSince(exactTS) = %+v, want Gemini=2200 Claude=12000", cAt)
	}
	cAfter := svc.ConsumptionSince(exactTS.Add(1 * time.Nanosecond))
	if cAfter.GeminiTokens != 0 || cAfter.ClaudeTokens != 12000 {
		t.Fatalf("ConsumptionSince(exactTS+1ns) = %+v, want Gemini=0 Claude=12000", cAfter)
	}
}

func TestConcurrentRefreshAndQueryRace(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
	now := time.Now().UTC()

	db := createTestWALConvDB(t, root, "race-conv")
	insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 5000, 0, 500, 0, 6500, 512000, "", "", "", now)
	defer db.Close()

	svc := NewAggregator(root, cachePath, nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}

	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				if id%2 == 0 {
					_ = svc.Refresh(context.Background())
				} else {
					_ = svc.Query(Range7D, now)
					_ = svc.ConsumptionSince(now.Add(-time.Hour))
					_ = svc.LastStats()
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestRefreshRetryAfterCacheSaveFailurePreservesSummariesAndParents(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	sumDB := createTestSummariesDB(t, root)
	insertSummaryRow(t, sumDB, "root-1", "Parent Session Title", "", "", "", "", now)
	insertSummaryRow(t, sumDB, "sub-1", "", "", "", "Researcher", "root-1", now)
	_ = sumDB.Close()

	rootDB := createTestWALConvDB(t, root, "root-1")
	insertGenRow(t, rootDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 0, 0, 200, 0, 1200, 512000, "", "", "", now)
	_ = rootDB.Close()

	subDB := createTestWALConvDB(t, root, "sub-1")
	// Intentionally omit RootCascadeID/ParentCascadeID from protobuf so parent rollup relies on summaries DB.
	insertGenRow(t, subDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 500, 0, 0, 100, 0, 600, 512000, "", "", "", now)
	_ = subDB.Close()

	// Block cachePath with a non-empty directory so saveCacheAtomic fails on the first Refresh.
	cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
	if err := os.MkdirAll(filepath.Join(cachePath, "blocker"), 0o755); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}

	svc := NewAggregator(root, cachePath, nil)
	if err := svc.Refresh(context.Background()); err == nil {
		t.Fatalf("expected first Refresh to fail when cachePath is a non-empty directory")
	}

	// Remove the blocker directory and retry Refresh; summaries & parent-child rollup must be committed.
	if err := os.RemoveAll(cachePath); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("retry Refresh failed: %v", err)
	}

	res := svc.Query(RangeToday, now)
	if res.RequestCount != 2 {
		t.Fatalf("RequestCount = %d, want 2", res.RequestCount)
	}
	if res.SessionCount != 1 {
		t.Fatalf("SessionCount = %d, want 1 (sub-1 rolled up into root-1)", res.SessionCount)
	}
	if len(res.TopSessions) != 1 || res.TopSessions[0].ConvID != "root-1" || res.TopSessions[0].Title != "Parent Session Title" {
		t.Fatalf("unexpected TopSessions after retry: %+v", res.TopSessions)
	}
}

// --- Local test fixture & protowire helpers ---

func createTestSummariesDB(t *testing.T, rootDir string) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(rootDir, "conversation_summaries.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open summaries db: %v", err)
	}
	schema := `
		PRAGMA journal_mode=WAL;
		CREATE TABLE conversation_summaries (
			conversation_id text PRIMARY KEY,
			title text NOT NULL DEFAULT "",
			preview text NOT NULL DEFAULT "",
			step_count integer NOT NULL DEFAULT 0,
			last_modified_time datetime NOT NULL,
			workspace_uris text NOT NULL DEFAULT "",
			status text NOT NULL DEFAULT "",
			agent_name text NOT NULL DEFAULT "",
			parent_conversation_id text NOT NULL DEFAULT "",
			nesting_depth integer NOT NULL DEFAULT 0,
			not_fully_idle numeric NOT NULL DEFAULT 0,
			killed numeric NOT NULL DEFAULT 0,
			last_user_input_time datetime NOT NULL DEFAULT "",
			last_user_input_step_index integer NOT NULL DEFAULT -1
		);
		CREATE TABLE conversation_parents (
			conversation_id TEXT PRIMARY KEY,
			parent_id TEXT NOT NULL
		) WITHOUT ROWID;
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create summaries schema: %v", err)
	}
	return db
}

func insertSummaryRow(t *testing.T, db *sql.DB, convID, title, preview, workspace, agentName, parentID string, modTime time.Time) {
	t.Helper()
	uri := ""
	if workspace != "" {
		uri = workspace
	}
	_, err := db.Exec(
		`INSERT INTO conversation_summaries (conversation_id, title, preview, last_modified_time, workspace_uris, agent_name, parent_conversation_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		convID, title, preview, modTime.UTC().Format(time.RFC3339Nano), uri, agentName, parentID,
	)
	if err != nil {
		t.Fatalf("insert summary %s: %v", convID, err)
	}
}

func insertParentRow(t *testing.T, db *sql.DB, childID, parentID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO conversation_parents (conversation_id, parent_id) VALUES (?, ?)`, childID, parentID); err != nil {
		t.Fatalf("insert parent (%s -> %s): %v", childID, parentID, err)
	}
}

func createTestWALConvDB(t *testing.T, rootDir, convID string) *sql.DB {
	t.Helper()
	convDir := filepath.Join(rootDir, "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatalf("mkdir conversations: %v", err)
	}
	dbPath := filepath.Join(convDir, convID+".db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open conv db %s: %v", convID, err)
	}
	schema := `
		PRAGMA journal_mode=WAL;
		PRAGMA wal_autocheckpoint=0;
		CREATE TABLE gen_metadata (
			idx integer PRIMARY KEY,
			data blob,
			size integer NOT NULL DEFAULT 0
		);
		CREATE TABLE steps (
			idx integer PRIMARY KEY,
			step_type integer NOT NULL DEFAULT 0,
			metadata blob
		);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create conv schema: %v", err)
	}
	return db
}

func insertGenRow(
	t *testing.T,
	db *sql.DB,
	idx, stepIdx int,
	modelSlug, modelDisplay string,
	inTok, crTok, cwTok, outTok, thinkTok, ctxUsed, ctxLim int64,
	errStr, rootCascadeID, parentCascadeID string,
	ts time.Time,
) {
	t.Helper()
	var packedSteps []byte
	packedSteps = protowire.AppendVarint(packedSteps, uint64(stepIdx))

	usage := buildProtoMsg(func(b []byte) []byte {
		if inTok > 0 {
			b = appendVarintField(b, 2, uint64(inTok))
		}
		if outTok > 0 {
			b = appendVarintField(b, 3, uint64(outTok))
		}
		if cwTok > 0 {
			b = appendVarintField(b, 4, uint64(cwTok))
		}
		if crTok > 0 {
			b = appendVarintField(b, 5, uint64(crTok))
		}
		if thinkTok > 0 {
			b = appendVarintField(b, 9, uint64(thinkTok))
		}
		return b
	})
	ctxWin := buildProtoMsg(func(b []byte) []byte {
		if ctxUsed > 0 {
			b = appendVarintField(b, 1, uint64(ctxUsed))
		}
		if ctxLim > 0 {
			b = appendVarintField(b, 4, uint64(ctxLim))
		}
		return b
	})
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage)
		b = appendSubMsg(b, 9, appendSubMsg(nil, 10, ctxWin))
		b = appendStringField(b, 19, modelSlug)
		b = appendKV(b, 20, "last_step_index", fmt.Sprintf("%d", stepIdx-1))
		if rootCascadeID != "" {
			b = appendKV(b, 20, "root_cascade_id", rootCascadeID)
		}
		if parentCascadeID != "" {
			b = appendKV(b, 20, "parent_cascade_id", parentCascadeID)
		}
		b = appendStringField(b, 21, modelDisplay)
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, inner)
		b = appendSubMsg(b, 2, packedSteps)
		if errStr != "" {
			b = appendStringField(b, 5, errStr)
		}
		return b
	})

	if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (?, ?, ?)`, idx, blob, len(blob)); err != nil {
		t.Fatalf("insert gen_metadata %d: %v", idx, err)
	}
	if !ts.IsZero() {
		tsMsg := buildProtoMsg(func(b []byte) []byte {
			b = appendVarintField(b, 1, uint64(ts.Unix()))
			b = appendVarintField(b, 2, uint64(ts.Nanosecond()))
			return b
		})
		stepMeta := appendSubMsg(nil, 1, tsMsg)
		if _, err := db.Exec(`INSERT OR REPLACE INTO steps (idx, step_type, metadata) VALUES (?, 1, ?)`, stepIdx, stepMeta); err != nil {
			t.Fatalf("insert step %d: %v", stepIdx, err)
		}
	}
}

func writeTranscriptJSONL(t *testing.T, rootDir, convID string, ts time.Time, stepIdx int) {
	t.Helper()
	p := filepath.Join(rootDir, "brain", convID, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	line := fmt.Sprintf(`{"step_index":%d,"type":"PLANNER_RESPONSE","created_at":"%s"}`+"\n", stepIdx, ts.UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func buildProtoMsg(fn func([]byte) []byte) []byte {
	return fn(nil)
}

func appendSubMsg(b []byte, num protowire.Number, payload []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, payload)
}

func appendStringField(b []byte, num protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendVarintField(b []byte, num protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func appendKV(b []byte, num protowire.Number, k, v string) []byte {
	entry := appendStringField(nil, 1, k)
	entry = appendStringField(entry, 2, v)
	return appendSubMsg(b, num, entry)
}

func TestForkedAndSideQuestionClonedStepsNotDoubleBilled(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 25, 14, 0, 0, 0, time.UTC)

	insertGenWithCascade := func(db *sql.DB, idx, stepIdx int, cascadeID string, inTok, outTok int64, ts time.Time) {
		var packed []byte
		packed = protowire.AppendVarint(packed, uint64(stepIdx))
		usage := buildProtoMsg(func(b []byte) []byte {
			b = appendVarintField(b, 2, uint64(inTok))
			b = appendVarintField(b, 3, uint64(outTok))
			return b
		})
		inner := buildProtoMsg(func(b []byte) []byte {
			b = appendVarintField(b, 3, 1535) // MODEL_PLACEHOLDER_M535 -> gemini-4-neon-xhigh
			b = appendSubMsg(b, 4, usage)
			b = appendKV(b, 20, "cascade_id", cascadeID)
			return b
		})
		blob := buildProtoMsg(func(b []byte) []byte {
			b = appendSubMsg(b, 1, inner)
			b = appendSubMsg(b, 2, packed)
			return b
		})
		if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (?, ?, ?)`, idx, blob, len(blob)); err != nil {
			t.Fatalf("insert gen_metadata: %v", err)
		}
		tsMsg := buildProtoMsg(func(b []byte) []byte {
			b = appendVarintField(b, 1, uint64(ts.Unix()))
			return b
		})
		if _, err := db.Exec(`INSERT OR REPLACE INTO steps (idx, step_type, metadata) VALUES (?, 1, ?)`, stepIdx, appendSubMsg(nil, 1, tsMsg)); err != nil {
			t.Fatalf("insert step: %v", err)
		}
	}

	parentDB := createTestWALConvDB(t, root, "conv-parent")
	insertGenWithCascade(parentDB, 0, 1, "conv-parent", 10_000, 1_000, now.Add(-10*time.Minute))
	_ = parentDB.Close()

	// Forked DB clones parent's gen 0 (cascade_id = "conv-parent") and adds its own gen 1 (cascade_id = "conv-fork").
	forkDB := createTestWALConvDB(t, root, "conv-fork")
	insertGenWithCascade(forkDB, 0, 1, "conv-parent", 10_000, 1_000, now.Add(-10*time.Minute))
	insertGenWithCascade(forkDB, 1, 2, "conv-fork", 4_000, 400, now.Add(-5*time.Minute))
	_ = forkDB.Close()

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	res := svc.Query(RangeToday, now)
	if res.RequestCount != 2 {
		t.Fatalf("RequestCount = %d, want 2 (cloned parent generation must not be double-billed)", res.RequestCount)
	}
	wantTokens := int64(10_000 + 1_000 + 4_000 + 400)
	if res.TotalTokens != wantTokens {
		t.Fatalf("TotalTokens = %d, want %d", res.TotalTokens, wantTokens)
	}
	if len(res.ByModel) != 1 || res.ByModel[0].Name != metrics.SubModelGeminiNeon {
		t.Fatalf("expected placeholder resolved to %q, got %+v", metrics.SubModelGeminiNeon, res.ByModel)
	}
}

func TestForkedBranchOnlyClonedStepNotReaddedByTranscriptFallback(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)

	insertGenWithCascade := func(db *sql.DB, idx, stepIdx int, cascadeID string, inTok, outTok int64, ts time.Time) {
		var packed []byte
		packed = protowire.AppendVarint(packed, uint64(stepIdx))
		usage := buildProtoMsg(func(b []byte) []byte {
			b = appendVarintField(b, 2, uint64(inTok))
			b = appendVarintField(b, 3, uint64(outTok))
			return b
		})
		inner := buildProtoMsg(func(b []byte) []byte {
			b = appendVarintField(b, 3, 1535) // MODEL_PLACEHOLDER_M535
			b = appendSubMsg(b, 4, usage)
			b = appendKV(b, 20, "cascade_id", cascadeID)
			return b
		})
		blob := buildProtoMsg(func(b []byte) []byte {
			b = appendSubMsg(b, 1, inner)
			b = appendSubMsg(b, 2, packed)
			return b
		})
		if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (?, ?, ?)`, idx, blob, len(blob)); err != nil {
			t.Fatalf("insert gen_metadata: %v", err)
		}
		tsMsg := buildProtoMsg(func(b []byte) []byte {
			b = appendVarintField(b, 1, uint64(ts.Unix()))
			return b
		})
		if _, err := db.Exec(`INSERT OR REPLACE INTO steps (idx, step_type, metadata) VALUES (?, 1, ?)`, stepIdx, appendSubMsg(nil, 1, tsMsg)); err != nil {
			t.Fatalf("insert step: %v", err)
		}
	}

	parentDB := createTestWALConvDB(t, root, "conv-parent")
	insertGenWithCascade(parentDB, 0, 1, "conv-parent", 10_000, 1_000, now.Add(-10*time.Minute))
	_ = parentDB.Close()

	// Fork with ONLY a cloned parent generation in DB and the same cloned step in transcript.jsonl.
	forkOnlyClonedDB := createTestWALConvDB(t, root, "conv-fork-empty")
	insertGenWithCascade(forkOnlyClonedDB, 0, 1, "conv-parent", 10_000, 1_000, now.Add(-10*time.Minute))
	_ = forkOnlyClonedDB.Close()

	trPath := filepath.Join(root, "brain", "conv-fork-empty", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLine := fmt.Sprintf(
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","input_tokens":10000,"output_tokens":1000}`+"\n",
		now.Add(-10*time.Minute).UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(trPath, []byte(trLine), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// Fork with cloned parent step 1 in DB, plus cloned step 1 and new own step 2 in transcript.jsonl.
	forkWithOwnTrDB := createTestWALConvDB(t, root, "conv-fork-own-tr")
	insertGenWithCascade(forkWithOwnTrDB, 0, 1, "conv-parent", 10_000, 1_000, now.Add(-10*time.Minute))
	_ = forkWithOwnTrDB.Close()

	trPath2 := filepath.Join(root, "brain", "conv-fork-own-tr", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trPath2), 0o755); err != nil {
		t.Fatalf("mkdir transcript 2: %v", err)
	}
	trLines2 := trLine + fmt.Sprintf(
		`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","input_tokens":2000,"output_tokens":200}`+"\n",
		now.Add(-5*time.Minute).UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(trPath2, []byte(trLines2), 0o600); err != nil {
		t.Fatalf("write transcript 2: %v", err)
	}

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	res := svc.Query(RangeToday, now)
	// Expected: 1 from conv-parent + 0 from conv-fork-empty + 1 (step 2 only) from conv-fork-own-tr = 2.
	if res.RequestCount != 2 {
		t.Fatalf("RequestCount = %d, want 2 (cloned parent steps must be excluded from transcript fallback)", res.RequestCount)
	}
	wantTokens := int64(10_000 + 1_000 + 2_000 + 200)
	if res.TotalTokens != wantTokens {
		t.Fatalf("TotalTokens = %d, want %d", res.TotalTokens, wantTokens)
	}
}

func TestUnknownModelEnumPrefersRecognizedTranscriptModel(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)

	db := createTestWALConvDB(t, root, "conv-unknown-enum")
	var packed []byte
	packed = protowire.AppendVarint(packed, 1)
	usage := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100) // 100 output tokens
		return b
	})
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 1999) // MODEL_PLACEHOLDER_M999 (unknown placeholder)
		b = appendSubMsg(b, 4, usage)
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, inner)
		b = appendSubMsg(b, 2, packed)
		return b
	})
	if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (0, ?, ?)`, blob, len(blob)); err != nil {
		t.Fatalf("insert gen_metadata: %v", err)
	}
	tsMsg := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, uint64(now.Unix()))
		return b
	})
	if _, err := db.Exec(`INSERT OR REPLACE INTO steps (idx, step_type, metadata) VALUES (1, 1, ?)`, appendSubMsg(nil, 1, tsMsg)); err != nil {
		t.Fatalf("insert step: %v", err)
	}
	_ = db.Close()

	trPath := filepath.Join(root, "brain", "conv-unknown-enum", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLine := fmt.Sprintf(
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"%s","model":"claude-opus-5-5","output_tokens":100}`+"\n",
		now.UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(trPath, []byte(trLine), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	res := svc.Query(RangeToday, now)
	if res.RequestCount != 1 {
		t.Fatalf("RequestCount = %d, want 1", res.RequestCount)
	}
	// claude-opus-5 output rate is $75/M -> 100 output tokens = $0.0075 (default rule would be $0.0005).
	wantCost := 100 * 75.0 / 1e6
	if math.Abs(res.TotalCostUSD-wantCost) > 1e-12 {
		t.Fatalf("TotalCostUSD = %.8f, want %.8f (claude-opus-5-5 should override unresolved MODEL_PLACEHOLDER_M999)", res.TotalCostUSD, wantCost)
	}
	if len(res.ByModel) != 1 || res.ByModel[0].Name != metrics.SubModelClaudeOpus55 {
		t.Fatalf("expected model %q, got %+v", metrics.SubModelClaudeOpus55, res.ByModel)
	}
}

func TestTranscriptWrittenAfterInitialScanRecomputesHistoricalCost(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
	now := time.Date(2026, 11, 25, 16, 30, 0, 0, time.UTC)

	db := createTestWALConvDB(t, root, "conv-late-tr")
	var packed []byte
	packed = protowire.AppendVarint(packed, 1)
	usage := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100) // 100 output tokens
		return b
	})
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 1999) // MODEL_PLACEHOLDER_M999 (unknown)
		b = appendSubMsg(b, 4, usage)
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, inner)
		b = appendSubMsg(b, 2, packed)
		return b
	})
	if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (0, ?, ?)`, blob, len(blob)); err != nil {
		t.Fatalf("insert gen_metadata: %v", err)
	}
	tsMsg := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, uint64(now.Unix()))
		return b
	})
	if _, err := db.Exec(`INSERT OR REPLACE INTO steps (idx, step_type, metadata) VALUES (1, 1, ?)`, appendSubMsg(nil, 1, tsMsg)); err != nil {
		t.Fatalf("insert step: %v", err)
	}
	_ = db.Close()

	// 1. Initial scan before transcript.jsonl exists: falls back to default rule ($5/M output -> $0.0005).
	svc := NewAggregator(root, cachePath, nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	res1 := svc.Query(RangeToday, now)
	wantDefaultCost := 100 * 5.0 / 1e6
	if math.Abs(res1.TotalCostUSD-wantDefaultCost) > 1e-12 {
		t.Fatalf("initial TotalCostUSD = %.8f, want %.8f", res1.TotalCostUSD, wantDefaultCost)
	}

	// Idle refresh hits cache.
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("idle Refresh: %v", err)
	}
	if st := svc.LastStats(); st.Hits != 1 || st.Misses != 0 {
		t.Fatalf("idle Refresh stats = %+v, want Hits=1 Misses=0", st)
	}

	// 2. Write transcript.jsonl later WITHOUT modifying the conversation DB or WAL.
	trPath := filepath.Join(root, "brain", "conv-late-tr", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLine := fmt.Sprintf(
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"%s","model":"claude-opus-5-5","output_tokens":100}`+"\n",
		now.UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(trPath, []byte(trLine), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// 3. Refresh must invalidate cache on transcript change and recompute cost ($75/M output -> $0.0075).
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("post-transcript Refresh: %v", err)
	}
	if st := svc.LastStats(); st.Misses != 1 || st.Hits != 0 {
		t.Fatalf("post-transcript Refresh stats = %+v, want Misses=1 Hits=0", st)
	}
	res2 := svc.Query(RangeToday, now)
	wantOpusCost := 100 * 75.0 / 1e6
	if math.Abs(res2.TotalCostUSD-wantOpusCost) > 1e-12 {
		t.Fatalf("post-transcript TotalCostUSD = %.8f, want %.8f", res2.TotalCostUSD, wantOpusCost)
	}
	if len(res2.ByModel) != 1 || res2.ByModel[0].Name != metrics.SubModelClaudeOpus55 {
		t.Fatalf("post-transcript ByModel = %+v, want %q", res2.ByModel, metrics.SubModelClaudeOpus55)
	}

	// 4. Fresh Service reloaded from disk cache hits cache and preserves recomputed cost.
	svcReload := NewAggregator(root, cachePath, nil)
	if err := svcReload.Refresh(context.Background()); err != nil {
		t.Fatalf("reload Refresh: %v", err)
	}
	if st := svcReload.LastStats(); st.Hits != 1 || st.Misses != 0 {
		t.Fatalf("reload Refresh stats = %+v, want Hits=1 Misses=0", st)
	}
	resReload := svcReload.Query(RangeToday, now)
	if math.Abs(resReload.TotalCostUSD-wantOpusCost) > 1e-12 {
		t.Fatalf("reload TotalCostUSD = %.8f, want %.8f", resReload.TotalCostUSD, wantOpusCost)
	}
}

func TestStandaloneAnnotationUpdatesRefreshHistoryTopSessions(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)

	sumDB := createTestSummariesDB(t, root)
	insertSummaryRow(t, sumDB, "child", "", "preview", "", "", "", now)
	_ = sumDB.Close()

	convDB := createTestWALConvDB(t, root, "child")
	insertGenRow(t, convDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 0, 0, 200, 0, 1200, 512000, "", "", "", now)
	_ = convDB.Close()

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	res1 := svc.Query(RangeToday, now)
	if len(res1.TopSessions) != 1 || res1.TopSessions[0].Title != "preview" {
		t.Fatalf("initial TopSessions = %+v, want title %q", res1.TopSessions, "preview")
	}

	// Create annotations/child.pbtxt WITHOUT touching conversation_summaries.db.
	annPath := filepath.Join(root, "annotations", "child.pbtxt")
	if err := os.MkdirAll(filepath.Dir(annPath), 0o755); err != nil {
		t.Fatalf("mkdir annotations: %v", err)
	}
	if err := os.WriteFile(annPath, []byte("title: \"Child Real Title\"\n"), 0o600); err != nil {
		t.Fatalf("write annotation: %v", err)
	}

	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("post-create Refresh: %v", err)
	}
	res2 := svc.Query(RangeToday, now)
	if len(res2.TopSessions) != 1 || res2.TopSessions[0].Title != "Child Real Title" {
		t.Fatalf("post-create TopSessions = %+v, want title %q", res2.TopSessions, "Child Real Title")
	}

	// Modify annotation in-place with identical byte length without touching DB.
	if err := os.WriteFile(annPath, []byte("title: \"Child Next Title\"\n"), 0o600); err != nil {
		t.Fatalf("update annotation: %v", err)
	}
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("post-update Refresh: %v", err)
	}
	res3 := svc.Query(RangeToday, now)
	if len(res3.TopSessions) != 1 || res3.TopSessions[0].Title != "Child Next Title" {
		t.Fatalf("post-update TopSessions = %+v, want title %q", res3.TopSessions, "Child Next Title")
	}

	// Delete annotation: history ranking must fall back cleanly to DB preview.
	if err := os.Remove(annPath); err != nil {
		t.Fatalf("remove annotation: %v", err)
	}
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("post-delete Refresh: %v", err)
	}
	res4 := svc.Query(RangeToday, now)
	if len(res4.TopSessions) != 1 || res4.TopSessions[0].Title != "preview" {
		t.Fatalf("post-delete TopSessions = %+v, want fallback title %q", res4.TopSessions, "preview")
	}
}

func TestMultiRootOldReplicaTranscriptUpdateDoesNotSwitchToOldDB(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
	now := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)

	// Old replica DB has only 1 request and an older DB mtime.
	oldDB := createTestWALConvDB(t, oldRoot, "conv-shared")
	insertGenRow(t, oldDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 0, 0, 100, 0, 1100, 512000, "", "", "", now.Add(-2*time.Hour))
	_ = oldDB.Close()
	oldTime := now.Add(-2 * time.Hour)
	oldDBPath := filepath.Join(oldRoot, "conversations", "conv-shared.db")
	if err := os.Chtimes(oldDBPath, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes oldDB: %v", err)
	}

	// New replica DB has 2 requests and a newer DB mtime.
	newDB := createTestWALConvDB(t, newRoot, "conv-shared")
	insertGenRow(t, newDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 0, 0, 100, 0, 1100, 512000, "", "", "", now.Add(-2*time.Hour))
	insertGenRow(t, newDB, 1, 2, "gemini-4-neon", "Gemini 4 Neon (High)", 2000, 0, 0, 200, 0, 2200, 512000, "", "", "", now.Add(-1*time.Hour))
	_ = newDB.Close()
	newTime := now.Add(-1 * time.Hour)
	newDBPath := filepath.Join(newRoot, "conversations", "conv-shared.db")
	if err := os.Chtimes(newDBPath, newTime, newTime); err != nil {
		t.Fatalf("Chtimes newDB: %v", err)
	}

	svc := NewMultiRootAggregator([]string{oldRoot, newRoot}, cachePath, nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	res1 := svc.Query(RangeToday, now)
	if res1.RequestCount != 2 {
		t.Fatalf("initial RequestCount = %d, want 2 (from newer replica)", res1.RequestCount)
	}

	// Update ONLY the old replica's transcript.jsonl with a newer mtime than newDB.
	oldTrPath := filepath.Join(oldRoot, "brain", "conv-shared", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(oldTrPath), 0o755); err != nil {
		t.Fatalf("mkdir old transcript: %v", err)
	}
	oldTrLine := fmt.Sprintf(
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","input_tokens":1000,"output_tokens":100}`+"\n",
		now.Add(-2*time.Hour).UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(oldTrPath, []byte(oldTrLine), 0o600); err != nil {
		t.Fatalf("write old transcript: %v", err)
	}
	if err := os.Chtimes(oldTrPath, now, now); err != nil {
		t.Fatalf("Chtimes old transcript: %v", err)
	}

	// Refresh must stay on the newer DB replica and preserve RequestCount == 2.
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("post-old-transcript Refresh: %v", err)
	}
	if st := svc.LastStats(); st.Hits != 1 || st.Misses != 0 {
		t.Fatalf("post-old-transcript stats = %+v, want Hits=1 Misses=0 (selected replica unchanged)", st)
	}
	res2 := svc.Query(RangeToday, now)
	if res2.RequestCount != 2 {
		t.Fatalf("post-old-transcript RequestCount = %d, want 2 (must not switch to older DB)", res2.RequestCount)
	}
}

func TestTranscriptTemporarilyLockedRetriesAndRecomputesCost(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(t.TempDir(), "usage_cache.json")
	now := time.Date(2026, 11, 25, 19, 0, 0, 0, time.UTC)

	db := createTestWALConvDB(t, root, "conv-locked-tr")
	var packed []byte
	packed = protowire.AppendVarint(packed, 1)
	usage := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100) // 100 output tokens
		return b
	})
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 1999) // MODEL_PLACEHOLDER_M999 (unknown)
		b = appendSubMsg(b, 4, usage)
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, inner)
		b = appendSubMsg(b, 2, packed)
		return b
	})
	if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (0, ?, ?)`, blob, len(blob)); err != nil {
		t.Fatalf("insert gen_metadata: %v", err)
	}
	tsMsg := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, uint64(now.Unix()))
		return b
	})
	if _, err := db.Exec(`INSERT OR REPLACE INTO steps (idx, step_type, metadata) VALUES (1, 1, ?)`, appendSubMsg(nil, 1, tsMsg)); err != nil {
		t.Fatalf("insert step: %v", err)
	}
	_ = db.Close()

	trPath := filepath.Join(root, "brain", "conv-locked-tr", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLine := fmt.Sprintf(
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"%s","model":"claude-opus-5-5","output_tokens":100}`+"\n",
		now.UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(trPath, []byte(trLine), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// 1. Lock transcript exclusively during the first Refresh: falls back to default pricing ($0.0005).
	unlock := lockFileExclusively(t, trPath)

	svc := NewAggregator(root, cachePath, nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("locked Refresh: %v", err)
	}
	resLocked := svc.Query(RangeToday, now)
	wantDefaultCost := 100 * 5.0 / 1e6
	if math.Abs(resLocked.TotalCostUSD-wantDefaultCost) > 1e-12 {
		t.Fatalf("locked TotalCostUSD = %.8f, want %.8f", resLocked.TotalCostUSD, wantDefaultCost)
	}

	// 2. Release lock and verify a fresh Service reloaded from disk cache ALSO retries and recovers $0.0075.
	unlock()

	svcReload := NewAggregator(root, cachePath, nil)
	if err := svcReload.Refresh(context.Background()); err != nil {
		t.Fatalf("unlocked reload Refresh: %v", err)
	}
	if st := svcReload.LastStats(); st.Misses != 1 || st.Hits != 0 {
		t.Fatalf("unlocked reload stats = %+v, want Misses=1 Hits=0", st)
	}
	resUnlocked := svcReload.Query(RangeToday, now)
	wantOpusCost := 100 * 75.0 / 1e6
	if math.Abs(resUnlocked.TotalCostUSD-wantOpusCost) > 1e-12 {
		t.Fatalf("unlocked TotalCostUSD = %.8f, want %.8f", resUnlocked.TotalCostUSD, wantOpusCost)
	}
	if len(resUnlocked.ByModel) != 1 || resUnlocked.ByModel[0].Name != metrics.SubModelClaudeOpus55 {
		t.Fatalf("unlocked ByModel = %+v, want %q", resUnlocked.ByModel, metrics.SubModelClaudeOpus55)
	}

	// 3. Same Service also recovers on its next Refresh and hits cache thereafter.
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("unlocked in-memory Refresh: %v", err)
	}
	resSame := svc.Query(RangeToday, now)
	if math.Abs(resSame.TotalCostUSD-wantOpusCost) > 1e-12 {
		t.Fatalf("in-memory unlocked TotalCostUSD = %.8f, want %.8f", resSame.TotalCostUSD, wantOpusCost)
	}
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("post-recovery idle Refresh: %v", err)
	}
	if st := svc.LastStats(); st.Hits != 1 || st.Misses != 0 {
		t.Fatalf("post-recovery idle stats = %+v, want Hits=1 Misses=0", st)
	}
}

func TestHistory_CanceledStepExcludedAndPartialDBMergedWithTranscript(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 25, 19, 30, 0, 0, time.UTC)

	// Case A: canceled zero-token DB row + matching zero-token transcript step must count as 0.
	dbCanceled := createTestWALConvDB(t, root, "conv-canceled")
	insertGenRow(t, dbCanceled, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 0, 0, 0, 0, 0, 0, 512000, "context canceled", "", "", now)
	_ = dbCanceled.Close()

	trCanceled := filepath.Join(root, "brain", "conv-canceled", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trCanceled), 0o755); err != nil {
		t.Fatalf("mkdir canceled transcript: %v", err)
	}
	if err := os.WriteFile(trCanceled, []byte(fmt.Sprintf(
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","input_tokens":0,"output_tokens":0}`+"\n",
		now.UTC().Format(time.RFC3339Nano),
	)), 0o600); err != nil {
		t.Fatalf("write canceled transcript: %v", err)
	}

	// Case B: partial DB write (step 2 persisted, step 3 still only in transcript).
	dbPartial := createTestWALConvDB(t, root, "conv-partial")
	insertGenRow(t, dbPartial, 0, 2, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 0, 0, 100, 0, 1100, 512000, "", "", "", now)
	_ = dbPartial.Close()

	trPartial := filepath.Join(root, "brain", "conv-partial", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trPartial), 0o755); err != nil {
		t.Fatalf("mkdir partial transcript: %v", err)
	}
	if err := os.WriteFile(trPartial, []byte(
		fmt.Sprintf(`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","input_tokens":1000,"output_tokens":100}`+"\n", now.UTC().Format(time.RFC3339Nano))+
			fmt.Sprintf(`{"step_index":3,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","input_tokens":2000,"output_tokens":200}`+"\n", now.Add(time.Minute).UTC().Format(time.RFC3339Nano)),
	), 0o600); err != nil {
		t.Fatalf("write partial transcript: %v", err)
	}

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	res := svc.Query(RangeToday, now)
	if res.RequestCount != 2 {
		t.Fatalf("RequestCount = %d, want 2 (0 from canceled + 2 from partial DB+transcript merge)", res.RequestCount)
	}
	if res.TotalTokens != 3300 {
		t.Fatalf("TotalTokens = %d, want 3300", res.TotalTokens)
	}
}

func TestMissingStepIndexDoesNotDoubleCountWithTranscript(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC)

	// DB row idx=0 has NO StepIndices and NO last_step_index, only 100 output tokens.
	db := createTestWALConvDB(t, root, "conv-no-step")
	usage := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100)
		return b
	})
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage)
		b = appendStringField(b, 19, "gemini-4-neon")
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, inner)
		return b
	})
	if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (0, ?, ?)`, blob, len(blob)); err != nil {
		t.Fatalf("insert gen_metadata: %v", err)
	}
	_ = db.Close()
	dbPath := filepath.Join(root, "conversations", "conv-no-step.db")
	if err := os.Chtimes(dbPath, now, now); err != nil {
		t.Fatalf("Chtimes db: %v", err)
	}

	// Same request appears in transcript.jsonl at step 7 with 100 output tokens.
	trPath := filepath.Join(root, "brain", "conv-no-step", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLine := fmt.Sprintf(
		`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","output_tokens":100}`+"\n",
		now.UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(trPath, []byte(trLine), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	res := svc.Query(RangeToday, now)
	if res.RequestCount != 1 || res.TotalTokens != 100 {
		t.Fatalf("got RequestCount=%d TotalTokens=%d, want 1 / 100 (must not double-count when DB lacks explicit step index)", res.RequestCount, res.TotalTokens)
	}
}

func TestMultiStepGenerationBackfillsUsageFromLaterStep(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 25, 20, 30, 0, 0, time.UTC)

	// DB row covers steps [6, 7] with zero token telemetry in DB.
	db := createTestWALConvDB(t, root, "conv-multi-step")
	var packed []byte
	packed = protowire.AppendVarint(packed, 6)
	packed = protowire.AppendVarint(packed, 7)
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendStringField(b, 19, "gemini-4-neon")
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, inner)
		b = appendSubMsg(b, 2, packed)
		return b
	})
	if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (0, ?, ?)`, blob, len(blob)); err != nil {
		t.Fatalf("insert gen_metadata: %v", err)
	}
	_ = db.Close()

	// Transcript records PLANNER_RESPONSE with 100 output tokens on step 7 (not step 6).
	trPath := filepath.Join(root, "brain", "conv-multi-step", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLine := fmt.Sprintf(
		`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","output_tokens":100}`+"\n",
		now.UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(trPath, []byte(trLine), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	res := svc.Query(RangeToday, now)
	if res.RequestCount != 1 || res.TotalTokens != 100 {
		t.Fatalf("got RequestCount=%d TotalTokens=%d, want 1 / 100 (must look up transcript planner across all covered steps [6, 7])", res.RequestCount, res.TotalTokens)
	}
}

func TestMissingStepIndexUnknownModelZeroTokensBackfillsUsageAndCost(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 25, 21, 0, 0, 0, time.UTC)

	// DB has ONLY an unknown model placeholder (MODEL_PLACEHOLDER_M999), 0 tokens, and NO step index.
	db := createTestWALConvDB(t, root, "conv-est-backfill")
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 1999) // MODEL_PLACEHOLDER_M999
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, inner)
		return b
	})
	if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (0, ?, ?)`, blob, len(blob)); err != nil {
		t.Fatalf("insert gen_metadata: %v", err)
	}
	_ = db.Close()

	// Transcript has a single PLANNER_RESPONSE (step 7) with Claude Opus 5.5 and 100 output tokens.
	trPath := filepath.Join(root, "brain", "conv-est-backfill", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLine := fmt.Sprintf(
		`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"%s","model":"claude-opus-5-5","output_tokens":100}`+"\n",
		now.UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(trPath, []byte(trLine), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	res := svc.Query(RangeToday, now)
	wantCost := 100 * 75.0 / 1e6
	if res.RequestCount != 1 || res.TotalTokens != 100 || math.Abs(res.TotalCostUSD-wantCost) > 1e-12 {
		t.Fatalf("got RequestCount=%d TotalTokens=%d TotalCostUSD=%.8f, want 1 / 100 / %.8f", res.RequestCount, res.TotalTokens, res.TotalCostUSD, wantCost)
	}
	if len(res.ByModel) != 1 || res.ByModel[0].Name != metrics.SubModelClaudeOpus55 {
		t.Fatalf("ByModel = %+v, want %q", res.ByModel, metrics.SubModelClaudeOpus55)
	}
}

func TestMissingStepIndexConfinesMatchToSingleGenAndMergesDistinctTranscriptRequest(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 25, 21, 30, 0, 0, time.UTC)

	// DB has 1 row with NO step index: Claude Opus 5.5 with 100 output tokens.
	db := createTestWALConvDB(t, root, "conv-est-merge")
	usage := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100)
		return b
	})
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage)
		b = appendStringField(b, 19, "claude-opus-5-5")
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, inner)
		return b
	})
	if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (0, ?, ?)`, blob, len(blob)); err != nil {
		t.Fatalf("insert gen_metadata: %v", err)
	}
	_ = db.Close()

	// Transcript has that Claude request (step 7, 100 output) plus a subsequent Gemini request (step 9, 200 output).
	trPath := filepath.Join(root, "brain", "conv-est-merge", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLines := fmt.Sprintf(
		`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"%s","model":"claude-opus-5-5","output_tokens":100}`+"\n"+
			`{"step_index":9,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","output_tokens":200}`+"\n",
		now.UTC().Format(time.RFC3339Nano),
		now.Add(time.Minute).UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(trPath, []byte(trLines), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	svc := NewAggregator(root, "", nil)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	res := svc.Query(RangeToday, now)
	if res.RequestCount != 2 || res.TotalTokens != 300 {
		t.Fatalf("got RequestCount=%d TotalTokens=%d, want 2 / 300 (must match Claude gen and merge subsequent Gemini request)", res.RequestCount, res.TotalTokens)
	}
}
