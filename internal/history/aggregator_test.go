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
