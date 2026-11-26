package source

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	_ "modernc.org/sqlite"
)

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

func createTestConvDB(t *testing.T, rootDir, convID string) *sql.DB {
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

func insertGenRow(t *testing.T, db *sql.DB, idx, stepIdx int, modelSlug, modelDisplay string, inTok, crTok, cwTok, outTok, ctxUsed, ctxLim int64, ttft, decodeDur time.Duration, ts time.Time, subType string) {
	t.Helper()
	var packedSteps []byte
	packedSteps = protowireAppendVarint(packedSteps, uint64(stepIdx))

	usage := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 2, uint64(inTok))
		b = appendVarintField(b, 3, uint64(outTok))
		if cwTok > 0 {
			b = appendVarintField(b, 4, uint64(cwTok))
		}
		if crTok > 0 {
			b = appendVarintField(b, 5, uint64(crTok))
		}
		b = appendVarintField(b, 9, uint64(outTok/2))
		b = appendVarintField(b, 10, uint64(outTok-outTok/2))
		return b
	})
	ctxWin := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, uint64(ctxUsed))
		b = appendVarintField(b, 4, uint64(ctxLim))
		return b
	})
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage)
		b = appendSubMsg(b, 9, appendSubMsg(nil, 10, ctxWin))
		b = appendDurationField(b, 11, ttft)
		b = appendDurationField(b, 12, decodeDur)
		b = appendStringField(b, 19, modelSlug)
		b = appendKV(b, 20, "last_step_index", fmt.Sprintf("%d", stepIdx-1))
		if subType != "" {
			b = appendKV(b, 20, "subagent_type", subType)
		}
		b = appendStringField(b, 21, modelDisplay)
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 2, packedSteps)
		b = appendSubMsg(b, 1, inner)
		return b
	})

	if _, err := db.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(?, ?, ?)`, idx, blob, len(blob)); err != nil {
		t.Fatalf("insert gen_metadata: %v", err)
	}

	tsMsg := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, uint64(ts.Unix()))
		b = appendVarintField(b, 2, uint64(ts.Nanosecond()))
		return b
	})
	stepMeta := appendSubMsg(nil, 1, tsMsg)
	if _, err := db.Exec(`INSERT OR REPLACE INTO steps(idx, step_type, metadata) VALUES(?, 15, ?)`, stepIdx, stepMeta); err != nil {
		t.Fatalf("insert step: %v", err)
	}
}

func protowireAppendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func writeTranscriptFile(t *testing.T, rootDir, convID, content string) string {
	t.Helper()
	p := TranscriptPath(rootDir, convID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return p
}

func TestProvider_MultiConvParallelAndParentChildTree(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	baseTime := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)

	// Insert 2 running conversations (conv-1 with 3 subagents, conv-2 standalone) + 1 idle conv-3 (L3 fallback only).
	insertSum := func(id, title, ws, status, agentName, parentID string, running int, mod, lastUser time.Time, lastUserStep int) {
		_, err := sumDB.Exec(
			`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, workspace_uris, status, agent_name, parent_conversation_id, not_fully_idle, last_user_input_time, last_user_input_step_index) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			id, title, mod.Format("2006-01-02 15:04:05+00:00"), ws, status, agentName, parentID, running, lastUser.Format("2006-01-02 15:04:05+00:00"), lastUserStep,
		)
		if err != nil {
			t.Fatalf("insert summary %s: %v", id, err)
		}
	}

	insertSum("conv-1", "Main Feature Work", `["file:///d%3A/antigravity_monitor"]`, "CASCADE_RUN_STATUS_RUNNING", "", "", 1, baseTime.Add(5*time.Minute), baseTime.Add(3*time.Minute), 10)
	insertSum("sub-research", "", `["file:///d%3A/antigravity_monitor"]`, "CASCADE_RUN_STATUS_IDLE", "research", "conv-1", 0, baseTime.Add(2*time.Minute), baseTime.Add(1*time.Minute), 0)
	insertSum("sub-browser-a", "", `["file:///d%3A/antigravity_monitor"]`, "CASCADE_RUN_STATUS_IDLE", "browser", "conv-1", 0, baseTime.Add(3*time.Minute), baseTime.Add(2*time.Minute), 0)
	insertSum("sub-browser-b", "", `["file:///d%3A/antigravity_monitor"]`, "CASCADE_RUN_STATUS_RUNNING", "browser", "conv-1", 1, baseTime.Add(4*time.Minute), baseTime.Add(3*time.Minute), 0)
	insertSum("conv-2", "Parallel Track", `["file:///C:/work/api"]`, "CASCADE_RUN_STATUS_RUNNING", "", "", 1, baseTime.Add(4*time.Minute+30*time.Second), baseTime.Add(4*time.Minute), 0)
	insertSum("conv-3", "L3 Fallback Session", `["file:///d%3A/notes"]`, "CASCADE_RUN_STATUS_IDLE", "", "", 0, baseTime.Add(-10*time.Minute), baseTime.Add(-11*time.Minute), 0)

	// Populate L2 DBs.
	c1DB := createTestConvDB(t, rootDir, "conv-1")
	defer c1DB.Close()
	// Turn 1 request (step 1) & Turn 2 request (step 11)
	insertGenRow(t, c1DB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 10000, 0, 0, 1000, 11000, 512000, 2*time.Second, 10*time.Second, baseTime.Add(10*time.Second), "")
	insertGenRow(t, c1DB, 1, 11, "gemini-4-neon", "Gemini 4 Neon (High)", 2000, 18000, 0, 1000, 21000, 512000, 1*time.Second, 5*time.Second, baseTime.Add(3*time.Minute+10*time.Second), "")

	subResDB := createTestConvDB(t, rootDir, "sub-research")
	defer subResDB.Close()
	insertGenRow(t, subResDB, 0, 1, "gemini-4-flash", "Gemini 4 Flash", 4000, 16000, 0, 500, 20500, 512000, 1*time.Second, 2*time.Second, baseTime.Add(1*time.Minute), "research")

	subBrADB := createTestConvDB(t, rootDir, "sub-browser-a")
	defer subBrADB.Close()
	insertGenRow(t, subBrADB, 0, 1, "claude-sonnet-4", "Claude 4.5 Sonnet", 5000, 145000, 0, 1000, 151000, 200000, 2*time.Second, 10*time.Second, baseTime.Add(3*time.Minute+20*time.Second), "browser")

	subBrBDB := createTestConvDB(t, rootDir, "sub-browser-b")
	defer subBrBDB.Close()
	insertGenRow(t, subBrBDB, 0, 1, "gemini-4-flash", "Gemini 4 Flash", 3000, 27000, 0, 600, 30600, 512000, 1*time.Second, 3*time.Second, baseTime.Add(4*time.Minute), "browser")

	c2DB := createTestConvDB(t, rootDir, "conv-2")
	defer c2DB.Close()
	insertGenRow(t, c2DB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 5000, 45000, 0, 800, 50800, 512000, 1500*time.Millisecond, 4*time.Second, baseTime.Add(4*time.Minute+30*time.Second), "")

	// Populate conv-1 transcript: 2 USER_INPUT turns + invoke_subagent Role for sub-research.
	conv1Transcript := "" +
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-24T14:00:00Z"}` + "\n" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-24T14:00:10Z","model":"gemini-4-neon","input_tokens":10000,"cache_read_tokens":0,"output_tokens":1000,"tool_calls":[{"name":"invoke_subagent","args":{"Role":"\"Codebase Researcher\"","TypeName":"\"research\""}}]}` + "\n" +
		`{"step_index":2,"type":"GENERIC","created_at":"2026-11-24T14:00:25Z","content":"Created the following subagent:\n{\n  \"conversationId\": \"sub-research\"\n}"}` + "\n" +
		`{"step_index":10,"type":"USER_INPUT","created_at":"2026-11-24T14:03:00Z"}` + "\n" +
		`{"step_index":11,"type":"PLANNER_RESPONSE","created_at":"2026-11-24T14:03:10Z","model":"gemini-4-neon","input_tokens":2000,"cache_read_tokens":18000,"output_tokens":1000}` + "\n"
	writeTranscriptFile(t, rootDir, "conv-1", conv1Transcript)

	// Populate conv-3 transcript ONLY (no L2 DB -> tests L3 fallback).
	conv3Transcript := "" +
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-24T13:49:00Z"}` + "\n" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-24T13:50:00Z","model":"gemini-4-flash","input_tokens":8000,"cache_read_tokens":12000,"output_tokens":400}` + "\n"
	writeTranscriptFile(t, rootDir, "conv-3", conv3Transcript)

	// Use MaxOpenDBs=2 across 5 DBs to verify LRU pool bound at the same time!
	prov, err := NewProvider(Options{
		RootDir:        rootDir,
		MaxOpenDBs:     2,
		DisableWatcher: true,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	if prov.OpenConvDBHandles() > 2 {
		t.Fatalf("LRU handle cap exceeded: got %d, max 2", prov.OpenConvDBHandles())
	}

	snap := prov.Snapshot()
	if snap.RunningCount != 2 {
		t.Fatalf("RunningCount = %d, want 2", snap.RunningCount)
	}
	if len(snap.Conversations) != 3 {
		t.Fatalf("Conversations len = %d, want 3", len(snap.Conversations))
	}
	if snap.ActiveConversationID != "conv-1" {
		t.Fatalf("ActiveConversationID = %q, want conv-1", snap.ActiveConversationID)
	}

	c1, ok := snap.FindConversation("conv-1")
	if !ok {
		t.Fatalf("conv-1 not found")
	}
	if c1.Workspace != "d:/antigravity_monitor" {
		t.Fatalf("Workspace = %q, want d:/antigravity_monitor", c1.Workspace)
	}
	if len(c1.Subagents) != 3 {
		t.Fatalf("Subagents len = %d, want 3", len(c1.Subagents))
	}

	// Verify 3-tier Subagent naming: Role ("Codebase Researcher") + duplicate numbering ("browser #1", "browser #2").
	nameByID := map[string]string{}
	for _, s := range c1.Subagents {
		nameByID[s.ID] = s.Name
	}
	if nameByID["sub-research"] != "Codebase Researcher" {
		t.Fatalf("sub-research name = %q, want Codebase Researcher", nameByID["sub-research"])
	}
	if nameByID["sub-browser-a"] != "browser #1" || nameByID["sub-browser-b"] != "browser #2" {
		t.Fatalf("unexpected duplicate browser names: %+v", nameByID)
	}

	// Verify active agent highlights the running subagent (sub-browser-b).
	if c1.ActiveAgentID != "sub-browser-b" {
		t.Fatalf("ActiveAgentID = %q, want sub-browser-b", c1.ActiveAgentID)
	}

	// Verify PeakContext picks sub-browser-a (151k / 200k = 75.5% > root 4.1%).
	if c1.PeakContext.AgentID != "sub-browser-a" || math.Abs(c1.PeakContext.Ratio-0.755) > 1e-4 {
		t.Fatalf("unexpected PeakContext: %+v", c1.PeakContext)
	}

	// Verify tree aggregation: 5 total requests across root (2) + 3 subagents (3).
	if c1.TreeMetrics.RequestCount != 5 {
		t.Fatalf("TreeMetrics.RequestCount = %d, want 5", c1.TreeMetrics.RequestCount)
	}
	// Turn 2 starts at 14:03:00Z -> conv-1 step 11 (14:03:10), sub-browser-a (14:03:20), sub-browser-b (14:04:00) = 3 turn reqs.
	if c1.TreeMetrics.TurnRequestCount != 3 {
		t.Fatalf("TreeMetrics.TurnRequestCount = %d, want 3", c1.TreeMetrics.TurnRequestCount)
	}
	if math.Abs((c1.Root.Metrics.SessionCostUSD+c1.SubagentsCostUSD)-c1.TreeMetrics.SessionCostUSD) > 1e-9 {
		t.Fatalf("root + sub cost mismatch: root=%f sub=%f tree=%f", c1.Root.Metrics.SessionCostUSD, c1.SubagentsCostUSD, c1.TreeMetrics.SessionCostUSD)
	}

	// Verify L3 fallback conversation (conv-3) produced 1 request from transcript.jsonl alone.
	c3, ok := snap.FindConversation("conv-3")
	if !ok || c3.TreeMetrics.RequestCount != 1 {
		t.Fatalf("conv-3 L3 fallback failed: ok=%v reqs=%d", ok, c3.TreeMetrics.RequestCount)
	}
	if c3.Requests[0].InputTokens != 8000 || c3.Requests[0].CacheReadTokens != 12000 || c3.Requests[0].OutputTokens != 400 {
		t.Fatalf("unexpected conv-3 fallback tokens: %+v", c3.Requests[0])
	}
}

func TestTranscriptTailer_IncrementalAndPartialLine(t *testing.T) {
	dir := t.TempDir()
	tp := filepath.Join(dir, "transcript.jsonl")

	// Write complete line 1 + partial line 2 (no trailing newline).
	part1 := `{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-24T14:00:00Z"}` + "\n" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","content":"` + strings.Repeat("x", 96*1024) + `","input_tokens":100`
	if err := os.WriteFile(tp, []byte(part1), 0o644); err != nil {
		t.Fatalf("write part1: %v", err)
	}

	tailer := NewTranscriptTailer(tp)
	changed, err := tailer.Sync()
	if err != nil || !changed {
		t.Fatalf("sync 1: changed=%v err=%v", changed, err)
	}
	snap1 := tailer.Snapshot()
	if len(snap1.UserInputSteps) != 1 || len(snap1.Planners) != 0 {
		t.Fatalf("unexpected snap1: %+v", snap1)
	}

	// Finish line 2 and append line 3 (second USER_INPUT).
	f, err := os.OpenFile(tp, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	_, _ = f.WriteString(`,"cache_read_tokens":900,"output_tokens":50,"created_at":"2026-11-24T14:00:05Z"}` + "\n" +
		`{"step_index":5,"type":"USER_INPUT","created_at":"2026-11-24T14:01:00Z"}` + "\n")
	_ = f.Close()

	changed, err = tailer.Sync()
	if err != nil || !changed {
		t.Fatalf("sync 2: changed=%v err=%v", changed, err)
	}
	snap2 := tailer.Snapshot()
	if len(snap2.UserInputSteps) != 2 || len(snap2.Planners) != 1 {
		t.Fatalf("unexpected snap2: %+v", snap2)
	}
	if snap2.Planners[0].InputTokens != 100 || snap2.Planners[0].CacheReadTokens != 900 {
		t.Fatalf("unexpected planner tokens: %+v", snap2.Planners[0])
	}
	turn1, inCur1 := snap2.TurnForStep(1, -1)
	turn2, inCur2 := snap2.TurnForStep(6, -1)
	if turn1 != 1 || inCur1 || turn2 != 2 || !inCur2 {
		t.Fatalf("unexpected turn alignment: step1=(%d,%v) step6=(%d,%v)", turn1, inCur1, turn2, inCur2)
	}
}

func TestProviderReusesUnchangedRequestsAndObservesWALUpdates(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()
	if _, err := summaries.Exec(`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES ('live', 'Original', ?)`, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	db := createTestConvDB(t, root, "live")
	defer db.Close()
	insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 100, 900, 0, 20, 1020, 512000, time.Second, 2*time.Second, now, "")

	prov, err := NewProvider(Options{RootDir: root, DisableWatcher: true})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()
	first, _ := prov.Snapshot().FindConversation("live")
	if len(first.Requests) != 1 {
		t.Fatalf("initial requests = %d", len(first.Requests))
	}

	refresh := func() model.ConversationSnapshot {
		t.Helper()
		if err := prov.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		conv, found := prov.Snapshot().FindConversation("live")
		if !found || len(conv.Requests) == 0 {
			t.Fatalf("missing live conversation or requests after refresh")
		}
		return conv
	}
	if unchanged := refresh(); &unchanged.Requests[0] != &first.Requests[0] {
		t.Fatal("unchanged refresh rebuilt request records")
	}
	if _, err := summaries.Exec(`UPDATE conversation_summaries SET title = 'Updated' WHERE conversation_id = 'live'`); err != nil {
		t.Fatal(err)
	}
	if renamed := refresh(); renamed.Title != "Updated" {
		t.Fatalf("title after summary WAL update = %q", renamed.Title)
	}
	insertGenRow(t, db, 1, 2, "gemini-4-neon", "Gemini 4 Neon (High)", 200, 900, 0, 40, 1140, 512000, time.Second, 2*time.Second, now.Add(time.Second), "")
	if updated := refresh(); len(updated.Requests) != 2 || updated.Requests[1].Seq != 2 {
		t.Fatalf("requests after conversation WAL update: %+v", updated.Requests)
	}
	if first.Title != "Original" || len(first.Requests) != 1 || first.Requests[0].Seq != 1 {
		t.Fatal("refresh mutated a previously published snapshot")
	}
}

func TestProvider_WatcherSubscribeAndConcurrency(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Now().UTC()
	_, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status, not_fully_idle, last_user_input_time, last_user_input_step_index) VALUES('live-1','Live Watch',?,'CASCADE_RUN_STATUS_RUNNING',1,?,0)`,
		now.Format("2006-01-02 15:04:05+00:00"),
		now.Format("2006-01-02 15:04:05+00:00"),
	)
	if err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	convDB := createTestConvDB(t, rootDir, "live-1")
	defer convDB.Close()
	insertGenRow(t, convDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 9000, 0, 200, 10200, 512000, time.Second, 2*time.Second, now, "")

	prov, err := NewProvider(Options{
		RootDir:      rootDir,
		MaxOpenDBs:   5,
		Debounce:     30 * time.Millisecond,
		PollInterval: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	ch, cancel := prov.Subscribe()
	defer cancel()

	// Drain initial snapshot.
	initial := <-ch
	if len(initial.Conversations) != 1 || initial.Conversations[0].TreeMetrics.RequestCount != 1 {
		t.Fatalf("unexpected initial snapshot: %+v", initial)
	}

	// Concurrent Snapshot readers to stress race detector.
	var wg sync.WaitGroup
	stopReaders := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					_ = prov.Snapshot()
				}
			}
		}()
	}

	// Append a new generation row in WAL mode; watcher/poll should pick it up incrementally.
	insertGenRow(t, convDB, 1, 5, "gemini-4-neon", "Gemini 4 Neon (High)", 1500, 10200, 0, 300, 12000, 512000, time.Second, 3*time.Second, now.Add(5*time.Second), "")

	deadline := time.After(3 * time.Second)
	gotUpdate := false
	for !gotUpdate {
		select {
		case s := <-ch:
			if c, ok := s.FindConversation("live-1"); ok && c.TreeMetrics.RequestCount == 2 {
				gotUpdate = true
			}
		case <-deadline:
			close(stopReaders)
			wg.Wait()
			t.Fatalf("timed out waiting for live incremental snapshot update")
		}
	}

	close(stopReaders)
	wg.Wait()

	// Explicit Refresh & idempotent Close check.
	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := prov.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := prov.Close(); err != nil {
		t.Fatalf("Close 2 (idempotent): %v", err)
	}
}

func TestLocalAntigravityRealDataAlignment(t *testing.T) {
	rootDir := DefaultRootDir()
	if _, err := os.Stat(filepath.Join(rootDir, "conversation_summaries.db")); err != nil {
		t.Skipf("local Antigravity data not found at %s; skipping real-data verification", rootDir)
	}

	prov, err := NewProvider(Options{
		RootDir:          rootDir,
		MaxOpenDBs:       5,
		MaxConversations: 15,
		MaxHydratedTrees: 10,
		DisableWatcher:   true,
	})
	if err != nil {
		t.Fatalf("NewProvider on local data: %v", err)
	}
	defer prov.Close()

	if prov.OpenConvDBHandles() > 5 {
		t.Fatalf("OpenConvDBHandles = %d, exceeds budget 5", prov.OpenConvDBHandles())
	}

	snap := prov.Snapshot()
	if len(snap.Conversations) == 0 {
		t.Fatalf("expected at least 1 local conversation")
	}

	matched := 0
	for _, conv := range snap.Conversations {
		byCID := make(map[string][]model.RequestRecord)
		for _, r := range conv.Requests {
			byCID[r.ConversationID] = append(byCID[r.ConversationID], r)
		}
		for cid, reqs := range byCID {
			tailer := NewTranscriptTailer(TranscriptPath(rootDir, cid))
			if _, err := tailer.Sync(); err != nil {
				continue
			}
			td := tailer.Snapshot()
			for _, r := range reqs {
				pl, ok := td.PlannersByStep[r.StepIndex]
				if !ok {
					continue
				}
				if r.InputTokens != pl.InputTokens ||
					r.CacheReadTokens != pl.CacheReadTokens ||
					r.OutputTokens != pl.OutputTokens {
					t.Errorf("conv=%s step=%d token mismatch: L2(%d,%d,%d) != L3(%d,%d,%d)",
						cid, r.StepIndex,
						r.InputTokens, r.CacheReadTokens, r.OutputTokens,
						pl.InputTokens, pl.CacheReadTokens, pl.OutputTokens,
					)
				}
				matched++
			}
		}
	}
	if matched == 0 {
		t.Fatalf("expected >0 L2<->L3 verified request matches on local data")
	}
	t.Logf("Verified %d real local requests across %d conversations with 0 token mismatches (open L2 handles=%d)",
		matched, len(snap.Conversations), prov.OpenConvDBHandles())
}

func TestProviderZeroBroadcastWhenIdle(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()
	if _, err := summaries.Exec(`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES ('idle-1', 'Idle Chat', ?)`, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	db := createTestConvDB(t, root, "idle-1")
	defer db.Close()
	insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 100, 900, 0, 20, 1020, 512000, time.Second, 2*time.Second, now, "")

	prov, err := NewProvider(Options{RootDir: root, DisableWatcher: true})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	ch, cancel := prov.Subscribe()
	defer cancel()
	<-ch // drain initial snapshot

	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case snap := <-ch:
		t.Fatalf("expected no broadcast on unchanged Refresh, got snapshot generated at %v", snap.GeneratedAt)
	default:
	}

	// Mutating WAL must trigger a broadcast on the next Refresh.
	insertGenRow(t, db, 1, 2, "gemini-4-neon", "Gemini 4 Neon (High)", 150, 900, 0, 30, 1080, 512000, time.Second, 2*time.Second, now.Add(time.Second), "")
	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case snap := <-ch:
		c, ok := snap.FindConversation("idle-1")
		if !ok || c.TreeMetrics.RequestCount != 2 {
			t.Fatalf("expected updated conversation with 2 requests, got %+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("expected broadcast after WAL mutation")
	}
}

func TestProviderAsyncInitHydratesTopOneThenBackground(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()
	for i, id := range []string{"c1", "c2", "c3"} {
		ts := now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339)
		if _, err := summaries.Exec(`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES (?, ?, ?)`, id, "Chat "+id, ts); err != nil {
			t.Fatal(err)
		}
		db := createTestConvDB(t, root, id)
		insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 100, 900, 0, 20, 1020, 512000, time.Second, 2*time.Second, now.Add(-time.Duration(i)*time.Minute), "")
		_ = db.Close()
	}

	prov, err := NewProvider(Options{
		RootDir:          root,
		MaxHydratedTrees: 3,
		AsyncInit:        true,
		PollInterval:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	// Top conversation must be hydrated immediately on first frame.
	initSnap := prov.Snapshot()
	if len(initSnap.Conversations) != 3 {
		t.Fatalf("expected 3 conversations, got %d", len(initSnap.Conversations))
	}
	if initSnap.Conversations[0].TreeMetrics.RequestCount != 1 {
		t.Fatalf("expected top conversation hydrated synchronously, got reqs=%d", initSnap.Conversations[0].TreeMetrics.RequestCount)
	}

	// Remaining conversations finish hydrating asynchronously in runLoop.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snap := prov.Snapshot()
		allHydrated := true
		for _, c := range snap.Conversations {
			if c.TreeMetrics.RequestCount != 1 {
				allHydrated = false
				break
			}
		}
		if allHydrated {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("background AsyncInit hydration did not complete in time: %+v", prov.Snapshot().Conversations)
}

func TestWatcherAttachesLateCreatedTranscriptDir(t *testing.T) {
	root := t.TempDir()
	events := make(chan watchEvent, 8)
	w, err := newDirWatcher(root, 20*time.Millisecond, func(ev watchEvent) {
		events <- ev
	})
	if err != nil {
		t.Fatalf("newDirWatcher: %v", err)
	}
	defer w.Close()

	convID := "conv-late-tr"
	ids := map[string]bool{convID: true}

	// Register before brain/<convID>/.system_generated/logs exists.
	w.WatchTranscriptDirs(ids)

	// Create the transcript log directory and re-register with the exact same ID set.
	trPath := TranscriptPath(root, convID)
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	w.WatchTranscriptDirs(ids)

	// Appending transcript.jsonl must trigger a real-time fsnotify watchEvent.
	if err := os.WriteFile(trPath, []byte(`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-25T10:00:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.ConvID == convID {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for fsnotify event on late-created transcript dir")
		}
	}
}

func TestAsyncInitEnforcesHardLimitWithMultipleRunningTrees(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	for i, id := range []string{"run-1", "run-2", "run-3"} {
		ts := now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339)
		if _, err := summaries.Exec(
			`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time, status, not_fully_idle) VALUES (?, ?, ?, 'CASCADE_RUN_STATUS_RUNNING', 1)`,
			id, "Running "+id, ts,
		); err != nil {
			t.Fatal(err)
		}
		db := createTestConvDB(t, root, id)
		insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 100, 900, 0, 20, 1020, 512000, time.Second, 2*time.Second, now.Add(-time.Duration(i)*time.Minute), "")
		_ = db.Close()
	}

	// Build Provider with DisableWatcher=true so we can inspect refreshInternal(..., maxHydratedOverride=1)
	// deterministically without racing runLoop.
	prov, err := NewProvider(Options{
		RootDir:          root,
		MaxHydratedTrees: 8,
		DisableWatcher:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	// Reset cached state and perform a strict budget-1 initial sync (matching AsyncInit first frame).
	prov.syncMu.Lock()
	prov.trees = nil
	prov.hasSnapshot = false
	prov.fullHydrated = false
	for _, rs := range prov.roots {
		rs.summaries.versionKnown = false
		rs.convPool.PruneCache(nil)
	}
	prov.syncMu.Unlock()

	if err := prov.refreshInternal(context.Background(), false, 1); err != nil {
		t.Fatalf("refreshInternal(budget=1): %v", err)
	}

	snap := prov.Snapshot()
	if snap.RunningCount != 3 {
		t.Fatalf("RunningCount = %d, want 3", snap.RunningCount)
	}
	hydrated := 0
	for _, c := range snap.Conversations {
		if c.TreeMetrics.RequestCount > 0 {
			hydrated++
		}
	}
	if hydrated != 1 {
		t.Fatalf("synchronously hydrated %d running trees on first frame, want hard cap 1", hydrated)
	}

	// Full background refresh must hydrate all 3 running trees.
	if err := prov.refreshInternal(context.Background(), false, 0); err != nil {
		t.Fatalf("refreshInternal(full): %v", err)
	}
	fullSnap := prov.Snapshot()
	fullHydrated := 0
	for _, c := range fullSnap.Conversations {
		if c.TreeMetrics.RequestCount == 1 {
			fullHydrated++
		}
	}
	if fullHydrated != 3 {
		t.Fatalf("hydrated %d running trees after full refresh, want 3", fullHydrated)
	}
}

func TestProviderHydratesAllDefaultConversationsAsync(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	// Create 12 idle conversations (> old cap of 8) to verify all listed sessions hydrate asynchronously.
	const total = 12
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("conv-%02d", i+1)
		ts := now.Add(-time.Duration(i) * time.Minute)
		if _, err := summaries.Exec(
			`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("Session %02d", i+1), ts.Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
		db := createTestConvDB(t, root, id)
		insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 9000, 0, 200, 10200, 512000, time.Second, 2*time.Second, ts, "")
		_ = db.Close()
	}

	prov, err := NewProvider(Options{
		RootDir:      root,
		AsyncInit:    true,
		PollInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	// First frame must already have the top conversation hydrated synchronously.
	// (Background runLoop may have already started hydrating remaining trees, so do not
	// assert an upper bound here; strict budget-1 is verified deterministically in
	// TestAsyncInitEnforcesHardLimitWithMultipleRunningTrees.)
	initSnap := prov.Snapshot()
	if len(initSnap.Conversations) != total {
		t.Fatalf("expected %d conversations, got %d", total, len(initSnap.Conversations))
	}
	if initSnap.Conversations[0].TreeMetrics.RequestCount != 1 {
		t.Fatalf("expected top conversation hydrated synchronously on AsyncInit, got reqs=%d", initSnap.Conversations[0].TreeMetrics.RequestCount)
	}

	// Background runLoop hydrates all 12 conversations (none stuck at $0.000 / Ctx 0%).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := prov.Snapshot()
		hydrated := 0
		for _, c := range snap.Conversations {
			if c.TreeMetrics.RequestCount == 1 && c.TreeMetrics.SessionCostUSD > 0 && c.Root.ContextRatio > 0 {
				hydrated++
			}
		}
		if hydrated == total {
			if prov.OpenConvDBHandles() > defaultMaxOpenDBs {
				t.Fatalf("open DB handles = %d, exceeds default LRU cap %d", prov.OpenConvDBHandles(), defaultMaxOpenDBs)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected all %d conversations hydrated asynchronously", total)
}

func TestProviderEnsureHydratedOnDemandBeyondCustomCap(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	const total = 10
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("cap-%02d", i+1)
		ts := now.Add(-time.Duration(i) * time.Minute)
		if _, err := summaries.Exec(
			`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("Cap Session %02d", i+1), ts.Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
		db := createTestConvDB(t, root, id)
		insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 2000, 18000, 0, 500, 20500, 512000, time.Second, 2*time.Second, ts, "")
		_ = db.Close()
	}

	// Explicit cap smaller than MaxConversations: initially only top 3 are hydrated.
	prov, err := NewProvider(Options{
		RootDir:          root,
		MaxConversations: total,
		MaxHydratedTrees: 3,
		PollInterval:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	before, ok := prov.Snapshot().FindConversation("cap-09")
	if !ok {
		t.Fatalf("cap-09 missing from snapshot")
	}
	if before.TreeMetrics.RequestCount != 0 {
		t.Fatalf("expected cap-09 initially unhydrated under cap=3, got reqs=%d", before.TreeMetrics.RequestCount)
	}

	// Focusing cap-09 triggers on-demand priority hydration and broadcasts its real telemetry.
	ch, cancel := prov.Subscribe()
	defer cancel()
	<-ch // drain initial snapshot

	prov.PinConversation("desktop", "cap-09")

	deadline := time.After(3 * time.Second)
	for {
		select {
		case snap := <-ch:
			if c, found := snap.FindConversation("cap-09"); found && c.TreeMetrics.RequestCount == 1 && c.TreeMetrics.SessionCostUSD > 0 && c.Root.ContextRatio > 0 {
				return
			}
		case <-deadline:
			after, _ := prov.Snapshot().FindConversation("cap-09")
			t.Fatalf("timed out waiting for on-demand hydration of cap-09; got %+v", after)
		}
	}
}

func TestProviderPinConversationSlotIsolationAndRelease(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	const total = 10
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("pin-%02d", i+1)
		ts := now.Add(-time.Duration(i) * time.Minute)
		if _, err := summaries.Exec(
			`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("Pin Session %02d", i+1), ts.Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
		db := createTestConvDB(t, root, id)
		insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1500, 8500, 0, 300, 10300, 512000, time.Second, 2*time.Second, ts, "")
		_ = db.Close()
	}

	prov, err := NewProvider(Options{
		RootDir:          root,
		MaxConversations: total,
		MaxHydratedTrees: 3,
		PollInterval:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	waitUntil := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", desc)
	}

	// Desktop pins pin-09, then Tray pins pin-10; both slots stay hydrated independently.
	prov.PinConversation("desktop", "pin-09")
	waitUntil("pin-09 hydrated", func() bool {
		c, ok := prov.Snapshot().FindConversation("pin-09")
		return ok && c.TreeMetrics.RequestCount == 1
	})

	prov.PinConversation("tray", "pin-10")
	waitUntil("both pin-09 and pin-10 hydrated", func() bool {
		snap := prov.Snapshot()
		c9, ok9 := snap.FindConversation("pin-09")
		c10, ok10 := snap.FindConversation("pin-10")
		return ok9 && ok10 && c9.TreeMetrics.RequestCount == 1 && c10.TreeMetrics.RequestCount == 1
	})

	// Desktop switches to pin-01 -> old desktop pin (pin-09) must be released cleanly,
	// while tray's pin (pin-10) stays hydrated.
	prov.PinConversation("desktop", "pin-01")
	waitUntil("pin-09 released while pin-10 remains hydrated", func() bool {
		snap := prov.Snapshot()
		c9, ok9 := snap.FindConversation("pin-09")
		c10, ok10 := snap.FindConversation("pin-10")
		return ok9 && ok10 && c9.TreeMetrics.RequestCount == 0 && c10.TreeMetrics.RequestCount == 1
	})

	// Releasing tray slot releases pin-10 as well.
	prov.PinConversation("tray", "")
	waitUntil("pin-10 released", func() bool {
		c10, ok10 := prov.Snapshot().FindConversation("pin-10")
		return ok10 && c10.TreeMetrics.RequestCount == 0
	})
}

func TestProviderPinConversationNonBlockingWithDisableWatcher(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	const total = 10
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("sync-%02d", i+1)
		ts := now.Add(-time.Duration(i) * time.Minute)
		if _, err := summaries.Exec(
			`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("Sync Session %02d", i+1), ts.Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
		db := createTestConvDB(t, root, id)
		insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1200, 8800, 0, 250, 10250, 512000, time.Second, 2*time.Second, ts, "")
		_ = db.Close()
	}

	prov, err := NewProvider(Options{
		RootDir:          root,
		MaxConversations: total,
		MaxHydratedTrees: 3,
		DisableWatcher:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	before, _ := prov.Snapshot().FindConversation("sync-09")
	if before.TreeMetrics.RequestCount != 0 {
		t.Fatalf("expected sync-09 initially unhydrated, got reqs=%d", before.TreeMetrics.RequestCount)
	}

	ch, cancel := prov.Subscribe()
	defer cancel()
	<-ch // drain initial snapshot

	// PinConversation must NEVER block the caller even when syncMu is held and DisableWatcher is true.
	prov.syncMu.Lock()
	done := make(chan struct{})
	go func() {
		prov.PinConversation("desktop", "sync-09")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		prov.syncMu.Unlock()
		t.Fatal("PinConversation blocked caller while syncMu was held under DisableWatcher")
	}
	prov.syncMu.Unlock()

	// Once syncMu is free, the one-shot on-demand worker hydrates sync-09 without requiring manual Refresh.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case snap := <-ch:
			if after, ok := snap.FindConversation("sync-09"); ok && after.TreeMetrics.RequestCount == 1 && after.TreeMetrics.SessionCostUSD > 0 {
				return
			}
		case <-deadline:
			after, _ := prov.Snapshot().FindConversation("sync-09")
			t.Fatalf("timed out waiting for async on-demand hydration under DisableWatcher; got %+v", after)
		}
	}
}

func TestProviderPinConversationAttachesTranscriptWatcherBeforeRead(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	// Create 10 idle conversations: all 10 are hydrated by default, while only the top 8
	// have transcript directories watched initially.
	const total = 10
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("watch-%02d", i+1)
		ts := now.Add(-time.Duration(i) * time.Minute)
		if _, err := summaries.Exec(
			`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("Watch Session %02d", i+1), ts.Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
		writeTranscriptFile(t, root, id, `{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-24T14:00:00Z"}`+"\n"+
			`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-24T14:00:05Z","model":"gemini-4-flash","input_tokens":1000,"cache_read_tokens":2000,"output_tokens":100}`+"\n")
	}

	prov, err := NewProvider(Options{
		RootDir:      root,
		Debounce:     25 * time.Millisecond,
		PollInterval: -1, // disable polling completely to prove fsnotify catches the append
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	c10, ok := prov.Snapshot().FindConversation("watch-10")
	if !ok || c10.TreeMetrics.RequestCount != 1 {
		t.Fatalf("expected watch-10 initially hydrated with 1 req, got ok=%v reqs=%d", ok, c10.TreeMetrics.RequestCount)
	}

	// Selecting watch-10 must register fsnotify BEFORE marking watchedSet[watch-10] true
	// and before reading L3, leaving zero watch/read race window.
	prov.PinConversation("desktop", "watch-10")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		prov.pinMu.Lock()
		watched := prov.watchedSet["watch-10"]
		prov.pinMu.Unlock()
		if watched {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	prov.pinMu.Lock()
	isWatched := prov.watchedSet["watch-10"]
	prov.pinMu.Unlock()
	if !isWatched {
		t.Fatal("watch-10 was not marked watched after PinConversation")
	}

	ch, cancel := prov.Subscribe()
	defer cancel()
	<-ch // drain current snapshot

	// Append a pure L3 transcript line to watch-10 (no L1/L2 writes, polling disabled).
	trPath := TranscriptPath(root, "watch-10")
	f, err := os.OpenFile(trPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript for append: %v", err)
	}
	_, err = f.WriteString(`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-24T14:01:00Z","model":"gemini-4-flash","input_tokens":1200,"cache_read_tokens":3000,"output_tokens":150}` + "\n")
	_ = f.Close()
	if err != nil {
		t.Fatalf("append transcript: %v", err)
	}

	waitDeadline := time.After(3 * time.Second)
	gotUpdate := false
	for !gotUpdate {
		select {
		case snap := <-ch:
			if c, found := snap.FindConversation("watch-10"); found && c.TreeMetrics.RequestCount == 2 {
				gotUpdate = true
			}
		case <-waitDeadline:
			after, _ := prov.Snapshot().FindConversation("watch-10")
			t.Fatalf("timed out waiting for real-time L3 update on pinned watch-10; reqs=%d", after.TreeMetrics.RequestCount)
		}
	}

	// Switching away from watch-10 releases its extra transcript watcher cleanly.
	prov.PinConversation("desktop", "watch-01")
	unwatchDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(unwatchDeadline) {
		prov.pinMu.Lock()
		stillWatched := prov.watchedSet["watch-10"]
		prov.pinMu.Unlock()
		if !stillWatched {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("expected watch-10 to be unwatched after switching desktop slot back to watch-01")
}

func TestProviderPinConversationRetriesLateCreatedTranscriptDir(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	const total = 10
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("late-%02d", i+1)
		ts := now.Add(-time.Duration(i) * time.Minute)
		if _, err := summaries.Exec(
			`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("Late Session %02d", i+1), ts.Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
		// Omit late-10's transcript directory initially so WatchTranscriptDirs queues it in pendingDirs.
		if id != "late-10" {
			writeTranscriptFile(t, root, id, `{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-24T14:00:00Z"}`+"\n")
		}
	}

	prov, err := NewProvider(Options{
		RootDir:      root,
		Debounce:     25 * time.Millisecond,
		PollInterval: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	// First selection before late-10's transcript dir exists: must NOT mark late-10 watched.
	prov.PinConversation("desktop", "late-10")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		prov.pinMu.Lock()
		applied := prov.appliedPinGen == prov.pinGen
		watched := prov.watchedSet["late-10"]
		prov.pinMu.Unlock()
		if applied {
			if watched {
				t.Fatal("late-10 was marked watched before its transcript directory existed")
			}
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	// Create late-10's transcript directory and initial file, then re-select the same conversation.
	writeTranscriptFile(t, root, "late-10", `{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-24T14:00:00Z"}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-24T14:00:05Z","model":"gemini-4-flash","input_tokens":1000,"cache_read_tokens":2000,"output_tokens":100}`+"\n")

	prov.PinConversation("desktop", "late-10")

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		prov.pinMu.Lock()
		watched := prov.watchedSet["late-10"]
		prov.pinMu.Unlock()
		if watched {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	prov.pinMu.Lock()
	watchedNow := prov.watchedSet["late-10"]
	prov.pinMu.Unlock()
	if !watchedNow {
		t.Fatal("re-selecting late-10 after transcript dir creation did not attach watcher")
	}

	ch, cancel := prov.Subscribe()
	defer cancel()
	<-ch

	// Append a new line to prove fsnotify is actively watching late-10 with polling disabled.
	trPath := TranscriptPath(root, "late-10")
	f, err := os.OpenFile(trPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open late-10 transcript: %v", err)
	}
	_, err = f.WriteString(`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-24T14:01:00Z","model":"gemini-4-flash","input_tokens":1200,"cache_read_tokens":3000,"output_tokens":150}` + "\n")
	_ = f.Close()
	if err != nil {
		t.Fatalf("append late-10 transcript: %v", err)
	}

	waitDeadline := time.After(3 * time.Second)
	for {
		select {
		case snap := <-ch:
			if c, ok := snap.FindConversation("late-10"); ok && c.TreeMetrics.RequestCount == 2 {
				return
			}
		case <-waitDeadline:
			after, _ := prov.Snapshot().FindConversation("late-10")
			t.Fatalf("timed out waiting for fsnotify event on late-created late-10 transcript; reqs=%d", after.TreeMetrics.RequestCount)
		}
	}
}

func TestProviderPinConversationRetriesAfterOnDemandTimeout(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	const total = 10
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("timeout-%02d", i+1)
		ts := now.Add(-time.Duration(i) * time.Minute)
		if _, err := summaries.Exec(
			`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("Timeout Session %02d", i+1), ts.Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
		db := createTestConvDB(t, root, id)
		insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1200, 8800, 0, 250, 10250, 512000, time.Second, 2*time.Second, ts, "")
		_ = db.Close()
	}

	prov, err := NewProvider(Options{
		RootDir:          root,
		MaxConversations: total,
		MaxHydratedTrees: 3,
		DisableWatcher:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	// Use a short on-demand timeout and hold syncMu past that timeout to force expiration.
	prov.onDemandTimeout = 80 * time.Millisecond
	prov.syncMu.Lock()
	prov.PinConversation("desktop", "timeout-09")
	time.Sleep(150 * time.Millisecond)
	prov.syncMu.Unlock()

	// Wait for the timed-out worker to exit.
	prov.onDemandWG.Wait()

	// Timed-out conversation must NOT be falsely marked hydrated.
	prov.pinMu.Lock()
	falselyHydrated := prov.hydratedSet["timeout-09"]
	prov.pinMu.Unlock()
	if falselyHydrated {
		t.Fatal("timed-out conversation timeout-09 was falsely marked hydrated")
	}
	if c, _ := prov.Snapshot().FindConversation("timeout-09"); c.TreeMetrics.RequestCount != 0 {
		t.Fatalf("expected timeout-09 to remain unhydrated after timeout, got reqs=%d", c.TreeMetrics.RequestCount)
	}

	// Re-selecting timeout-09 with normal timeout must retry and succeed without manual Refresh.
	prov.onDemandTimeout = 5 * time.Second
	prov.PinConversation("desktop", "timeout-09")
	prov.onDemandWG.Wait()

	after, ok := prov.Snapshot().FindConversation("timeout-09")
	if !ok || after.TreeMetrics.RequestCount != 1 || after.TreeMetrics.SessionCostUSD <= 0 {
		t.Fatalf("expected timeout-09 retry to succeed, got ok=%v conv=%+v", ok, after)
	}
}

func TestProviderPinConversationDoesNotDropNewSelectionAfterTimeout(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	const total = 10
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("switch-%02d", i+1)
		ts := now.Add(-time.Duration(i) * time.Minute)
		if _, err := summaries.Exec(
			`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("Switch Session %02d", i+1), ts.Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
		db := createTestConvDB(t, root, id)
		insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1200, 8800, 0, 250, 10250, 512000, time.Second, 2*time.Second, ts, "")
		_ = db.Close()
	}

	prov, err := NewProvider(Options{
		RootDir:          root,
		MaxConversations: total,
		MaxHydratedTrees: 3,
		DisableWatcher:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	// Select A while syncMu is held past timeout, then select B BEFORE A's worker can exit.
	prov.pinMu.Lock()
	prov.onDemandTimeout = 80 * time.Millisecond
	prov.pinMu.Unlock()
	prov.syncMu.Lock()
	prov.PinConversation("desktop", "switch-09")
	time.Sleep(150 * time.Millisecond)
	// Restore normal timeout for the follow-up attempt and switch to B while A is still blocked on syncMu.
	prov.pinMu.Lock()
	prov.onDemandTimeout = 5 * time.Second
	prov.pinMu.Unlock()
	prov.PinConversation("desktop", "switch-10")
	prov.syncMu.Unlock()

	prov.onDemandWG.Wait()

	afterB, ok := prov.Snapshot().FindConversation("switch-10")
	if !ok || afterB.TreeMetrics.RequestCount != 1 || afterB.TreeMetrics.SessionCostUSD <= 0 {
		t.Fatalf("expected switch-10 to be hydrated automatically after prior timeout, got ok=%v conv=%+v", ok, afterB)
	}
}

func TestProviderDBErrorDoesNotMaskTranscriptAndDoesNotBroadcastWhenIdle(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	if _, err := summaries.Exec(
		`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES ('broken-l2', 'Broken L2 Valid L3', ?)`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}

	// Create an L2 DB file missing the gen_metadata table (causes SQLite query error),
	// alongside a valid L3 transcript.jsonl with 1 request.
	convDir := filepath.Join(root, "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}
	badDB, err := sql.Open("sqlite", filepath.Join(convDir, "broken-l2.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badDB.Exec(`CREATE TABLE dummy (id integer PRIMARY KEY)`); err != nil {
		_ = badDB.Close()
		t.Fatal(err)
	}
	_ = badDB.Close()

	writeTranscriptFile(t, root, "broken-l2",
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-24T14:00:00Z"}`+"\n"+
			`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-24T14:00:05Z","model":"gemini-4-flash","input_tokens":4000,"cache_read_tokens":6000,"output_tokens":200}`+"\n")

	prov, err := NewProvider(Options{
		RootDir:        root,
		DisableWatcher: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	// 1. Available L3 transcript data must still be visible despite L2 DB failure.
	c, ok := prov.Snapshot().FindConversation("broken-l2")
	if !ok || c.TreeMetrics.RequestCount != 1 {
		t.Fatalf("expected L3 fallback request to be visible despite L2 DB error, got ok=%v reqs=%d", ok, c.TreeMetrics.RequestCount)
	}

	// 2. Retry state must remain enabled (not falsely marked hydrated).
	prov.pinMu.Lock()
	markedHydrated := prov.hydratedSet["broken-l2"]
	prov.pinMu.Unlock()
	if markedHydrated {
		t.Fatal("broken-l2 must stay unmarked in hydratedSet while L2 DB fails")
	}

	// 3. Repeated idle refreshes with persistent L2 error must NOT broadcast when nothing changed.
	ch, cancel := prov.Subscribe()
	defer cancel()
	<-ch // drain initial snapshot

	for i := 0; i < 3; i++ {
		if err := prov.Refresh(context.Background()); err != nil {
			t.Fatalf("idle refresh %d failed: %v", i, err)
		}
		select {
		case snap := <-ch:
			t.Fatalf("unexpected idle broadcast #%d on unchanged persistent DB error: %v", i, snap.GeneratedAt)
		default:
		}
	}

	// 4. Once L2 DB is repaired with gen_metadata, Refresh retries cleanly and upgrades data.
	repairedDB := createTestConvDB(t, root, "broken-l2")
	defer repairedDB.Close()
	insertGenRow(t, repairedDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 4000, 6000, 0, 200, 10200, 512000, time.Second, 2*time.Second, now, "")
	insertGenRow(t, repairedDB, 1, 2, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 9000, 0, 300, 10300, 512000, time.Second, 2*time.Second, now.Add(time.Second), "")

	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh after DB repair: %v", err)
	}
	select {
	case snap := <-ch:
		repaired, found := snap.FindConversation("broken-l2")
		if !found || repaired.TreeMetrics.RequestCount != 2 {
			t.Fatalf("expected 2 L2 requests after DB repair, got found=%v reqs=%d", found, repaired.TreeMetrics.RequestCount)
		}
	case <-time.After(time.Second):
		t.Fatal("expected broadcast after L2 DB repair")
	}
	prov.pinMu.Lock()
	hydratedAfterRepair := prov.hydratedSet["broken-l2"]
	prov.pinMu.Unlock()
	if !hydratedAfterRepair {
		t.Fatal("expected broken-l2 marked hydrated after L2 DB repair")
	}
}

func TestProviderPinConversationDoesNotAckFailedNewPinOnInFlightTimeout(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	const total = 10
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("inflight-%02d", i+1)
		ts := now.Add(-time.Duration(i) * time.Minute)
		if _, err := summaries.Exec(
			`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("InFlight Session %02d", i+1), ts.Format(time.RFC3339),
		); err != nil {
			t.Fatal(err)
		}
		db := createTestConvDB(t, root, id)
		insertGenRow(t, db, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1200, 8800, 0, 250, 10250, 512000, time.Second, 2*time.Second, ts, "")
		_ = db.Close()
	}

	if _, err := summaries.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatal(err)
	}

	prov, err := NewProvider(Options{
		RootDir:          root,
		MaxConversations: total,
		MaxHydratedTrees: 3,
		DisableWatcher:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	// Part 1: Deterministic check that buildSnapshotLocked with an expired ctx never
	// advances appliedPinGen when a pinned conversation fails to hydrate.
	prov.syncMu.Lock()
	prov.pinMu.Lock()
	prov.pinnedBySlot["desktop"] = "inflight-08"
	prov.pinGen++
	genBefore := prov.pinGen
	appliedBefore := prov.appliedPinGen
	prov.pinMu.Unlock()
	expiredCtx, cancelExpired := context.WithCancel(context.Background())
	cancelExpired()
	_, _ = prov.buildSnapshotLocked(expiredCtx, prov.opts.MaxHydratedTrees, false, false)
	prov.syncMu.Unlock()

	prov.pinMu.Lock()
	if prov.appliedPinGen >= genBefore || prov.appliedPinGen != appliedBefore {
		t.Fatalf("expired buildSnapshotLocked falsely advanced appliedPinGen from %d to %d (pinGen=%d)", appliedBefore, prov.appliedPinGen, genBefore)
	}
	if prov.hydratedSet["inflight-08"] {
		t.Fatal("expired buildSnapshotLocked falsely marked inflight-08 hydrated")
	}
	delete(prov.pinnedBySlot, "desktop")
	prov.appliedPinGen = prov.pinGen
	prov.pinMu.Unlock()

	// Part 2: With summaries DB in rollback DELETE journal mode, BEGIN EXCLUSIVE
	// deterministically blocks ReadChanges(ctx) past onDemandTimeout.
	lockConn, err := summaries.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}

	prov.pinMu.Lock()
	prov.onDemandTimeout = 80 * time.Millisecond
	prov.pinMu.Unlock()

	prov.PinConversation("desktop", "inflight-09")
	// Wait past the 80ms timeout while ReadChanges is blocked on the exclusive lock,
	// then switch to inflight-10 BEFORE releasing the lock (and before worker exits).
	time.Sleep(120 * time.Millisecond)
	prov.pinMu.Lock()
	prov.onDemandTimeout = 5 * time.Second
	prov.pinMu.Unlock()
	prov.PinConversation("desktop", "inflight-10")

	if _, err := lockConn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}

	prov.onDemandWG.Wait()

	afterA, _ := prov.Snapshot().FindConversation("inflight-09")
	if afterA.TreeMetrics.RequestCount != 0 {
		t.Fatalf("expected inflight-09 to have timed out and been replaced, got reqs=%d", afterA.TreeMetrics.RequestCount)
	}
	afterB, ok := prov.Snapshot().FindConversation("inflight-10")
	if !ok || afterB.TreeMetrics.RequestCount != 1 || afterB.TreeMetrics.SessionCostUSD <= 0 {
		t.Fatalf("expected inflight-10 hydrated after mid-read timeout on inflight-09, got ok=%v conv=%+v", ok, afterB)
	}
}

func TestProviderClearedHydratedFlagRetriesAfterMidLifecycleDBFailure(t *testing.T) {
	root := t.TempDir()
	summaries := createTestSummariesDB(t, root)
	defer summaries.Close()
	now := time.Now().UTC()

	if _, err := summaries.Exec(
		`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES ('flaky-db', 'Flaky L2 Session', ?)`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}

	convDB := createTestConvDB(t, root, "flaky-db")
	defer convDB.Close()
	insertGenRow(t, convDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 9000, 0, 200, 10200, 512000, time.Second, 2*time.Second, now, "")

	prov, err := NewProvider(Options{
		RootDir:        root,
		DisableWatcher: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	// 1. Initially hydrated with 1 request.
	c1, ok := prov.Snapshot().FindConversation("flaky-db")
	if !ok || c1.TreeMetrics.RequestCount != 1 {
		t.Fatalf("expected initial 1 request, got ok=%v reqs=%d", ok, c1.TreeMetrics.RequestCount)
	}
	prov.pinMu.Lock()
	initHydrated := prov.hydratedSet["flaky-db"]
	prov.pinMu.Unlock()
	if !initHydrated {
		t.Fatal("expected flaky-db marked hydrated initially")
	}

	// 2. Break gen_metadata temporarily so the next Refresh triggers an L2 read failure.
	if _, err := convDB.Exec(`ALTER TABLE gen_metadata RENAME TO gen_metadata_bak`); err != nil {
		t.Fatal(err)
	}
	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh during L2 failure: %v", err)
	}

	// Last good snapshot must remain visible, while hydratedSet clears so re-selection retries.
	cDuring, _ := prov.Snapshot().FindConversation("flaky-db")
	if cDuring.TreeMetrics.RequestCount != 1 {
		t.Fatalf("expected last good snapshot preserved during failure, got reqs=%d", cDuring.TreeMetrics.RequestCount)
	}
	prov.pinMu.Lock()
	hydratedAfterFail := prov.hydratedSet["flaky-db"]
	prov.pinMu.Unlock()
	if hydratedAfterFail {
		t.Fatal("expected hydratedSet[flaky-db] cleared after L2 read failure")
	}

	// 3. Restore gen_metadata and append a 2nd request; re-selecting via PinConversation
	// must retry and pick up request #2 without requiring an explicit Refresh().
	if _, err := convDB.Exec(`ALTER TABLE gen_metadata_bak RENAME TO gen_metadata`); err != nil {
		t.Fatal(err)
	}
	insertGenRow(t, convDB, 1, 2, "gemini-4-neon", "Gemini 4 Neon (High)", 1500, 9500, 0, 300, 11300, 512000, time.Second, 2*time.Second, now.Add(time.Second), "")

	prov.PinConversation("desktop", "flaky-db")
	prov.onDemandWG.Wait()

	cAfter, ok := prov.Snapshot().FindConversation("flaky-db")
	if !ok || cAfter.TreeMetrics.RequestCount != 2 {
		t.Fatalf("expected re-selection retry to load 2nd request, got ok=%v reqs=%d", ok, cAfter.TreeMetrics.RequestCount)
	}
	prov.pinMu.Lock()
	hydratedAfterRetry := prov.hydratedSet["flaky-db"]
	prov.pinMu.Unlock()
	if !hydratedAfterRetry {
		t.Fatal("expected hydratedSet[flaky-db] restored after successful retry")
	}
}

func TestProviderDeletedConversationPublishesAfterCancelledRefresh(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	sum1 := createTestSummariesDB(t, root1)
	defer sum1.Close()
	sum2 := createTestSummariesDB(t, root2)
	defer sum2.Close()

	// Put root2's summaries DB in rollback DELETE mode so BEGIN EXCLUSIVE blocks ReadChanges until timeout.
	if _, err := sum2.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if _, err := sum1.Exec(
		`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time) VALUES ('del-1', 'Soon Deleted', ?)`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}

	prov, err := NewProvider(Options{
		RootDirs:       []string{root1, root2},
		DisableWatcher: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prov.Close()

	if len(prov.Snapshot().Conversations) != 1 {
		t.Fatalf("expected initial 1 conversation, got %d", len(prov.Snapshot().Conversations))
	}

	// Delete del-1 from root1, then block root2 so Refresh observes root1's deletion
	// and updates p.trees before failing with DeadlineExceeded on root2.
	if _, err := sum1.Exec(`DELETE FROM conversation_summaries WHERE conversation_id = 'del-1'`); err != nil {
		t.Fatal(err)
	}
	lockConn, err := sum2.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}

	shortCtx, cancelShort := context.WithTimeout(context.Background(), 60*time.Millisecond)
	err = prov.Refresh(shortCtx)
	cancelShort()
	if err == nil {
		_, _ = lockConn.ExecContext(context.Background(), `ROLLBACK`)
		t.Fatal("expected cancelled/timed-out refresh while root2 was exclusively locked")
	}
	if _, err := lockConn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}

	// Pre-condition check: internal trees already dropped del-1, published snapshot still
	// had 1, and BOTH roots retain valid version caches so recovery Refresh gets changed=false
	// from every root and relies strictly on snapshotDirty to publish the deletion.
	if len(prov.Snapshot().Conversations) != 1 {
		t.Fatalf("expected stale pre-publish snapshot with 1 conversation before retry, got %d", len(prov.Snapshot().Conversations))
	}
	for i, rs := range prov.roots {
		rs.summaries.mu.Lock()
		vk := rs.summaries.versionKnown
		rs.summaries.mu.Unlock()
		if !vk {
			t.Fatalf("expected root[%d] summaries versionKnown=true so recovery relies strictly on snapshotDirty", i)
		}
	}

	ch, cancel := prov.Subscribe()
	defer cancel()
	<-ch // drain stale snapshot

	// Next successful Refresh must publish the pending deletion even though root1's data_version is unchanged.
	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("recovery refresh failed: %v", err)
	}
	if got := len(prov.Snapshot().Conversations); got != 0 {
		t.Fatalf("expected 0 conversations after recovery refresh, got %d", got)
	}
	select {
	case snap := <-ch:
		if len(snap.Conversations) != 0 {
			t.Fatalf("expected broadcast with 0 conversations, got %d", len(snap.Conversations))
		}
	case <-time.After(time.Second):
		t.Fatal("expected broadcast publishing pending deletion")
	}

	// Subsequent idle refreshes must not broadcast again.
	for i := 0; i < 2; i++ {
		if err := prov.Refresh(context.Background()); err != nil {
			t.Fatalf("idle refresh %d failed: %v", i, err)
		}
		select {
		case snap := <-ch:
			t.Fatalf("unexpected idle broadcast #%d after deletion published: %v", i, snap.GeneratedAt)
		default:
		}
	}
}

func TestProvider_ForkDedupAnnotationTitleAndMetadataRole(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	// Parent conversation with empty SQLite title -> should fall back to annotations/conv-parent.pbtxt.
	annDir := filepath.Join(rootDir, "annotations")
	if err := os.MkdirAll(annDir, 0o755); err != nil {
		t.Fatalf("mkdir annotations: %v", err)
	}
	if err := os.WriteFile(filepath.Join(annDir, "conv-parent.pbtxt"), []byte("title: \"\\346\\227\\245\\345\\277\\227\\345\\210\\206\\346\\236\\220\"\n"), 0o644); err != nil {
		t.Fatalf("write pbtxt: %v", err)
	}

	_, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status, agent_name, parent_conversation_id, not_fully_idle) VALUES
		('conv-parent', '', ?, 'CASCADE_RUN_STATUS_IDLE', '', '', 0),
		('sub-no-tr', '', ?, 'CASCADE_RUN_STATUS_IDLE', '', 'conv-parent', 0),
		('conv-fork', 'Forked Branch', ?, 'CASCADE_RUN_STATUS_IDLE', '', '', 0)`,
		now.Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339), now.Add(2*time.Minute).Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert summaries: %v", err)
	}

	insertWithCascade := func(db *sql.DB, idx, stepIdx int, cascadeID string, inTok, outTok int64, ts time.Time) {
		var packed []byte
		packed = protowireAppendVarint(packed, uint64(stepIdx))
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
			b = appendSubMsg(b, 2, packed)
			b = appendSubMsg(b, 1, inner)
			return b
		})
		if _, err := db.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(?, ?, ?)`, idx, blob, len(blob)); err != nil {
			t.Fatalf("insert gen: %v", err)
		}
		tsMsg := buildProtoMsg(func(b []byte) []byte {
			b = appendVarintField(b, 1, uint64(ts.Unix()))
			return b
		})
		if _, err := db.Exec(`INSERT OR REPLACE INTO steps(idx, step_type, metadata) VALUES(?, 15, ?)`, stepIdx, appendSubMsg(nil, 1, tsMsg)); err != nil {
			t.Fatalf("insert step: %v", err)
		}
	}

	parentDB := createTestConvDB(t, rootDir, "conv-parent")
	defer parentDB.Close()
	insertWithCascade(parentDB, 0, 1, "conv-parent", 5000, 500, now)

	// Subagent with NO transcript.jsonl and empty agent_name: role comes from trajectory_metadata_blob.
	subDB := createTestConvDB(t, rootDir, "sub-no-tr")
	defer subDB.Close()
	if _, err := subDB.Exec(`CREATE TABLE trajectory_metadata_blob (id text PRIMARY KEY, data blob)`); err != nil {
		t.Fatalf("create trajectory_metadata_blob: %v", err)
	}
	spec := buildProtoMsg(func(b []byte) []byte {
		b = appendStringField(b, 1, "research")
		b = appendStringField(b, 2, "Protocol Inspector")
		return b
	})
	metaBlob := buildProtoMsg(func(b []byte) []byte {
		b = appendStringField(b, 5, "conv-parent")
		b = appendStringField(b, 6, "conv-parent")
		b = appendSubMsg(b, 8, spec)
		return b
	})
	if _, err := subDB.Exec(`INSERT INTO trajectory_metadata_blob(id, data) VALUES('main', ?)`, metaBlob); err != nil {
		t.Fatalf("insert meta blob: %v", err)
	}
	insertWithCascade(subDB, 0, 1, "sub-no-tr", 2000, 200, now.Add(30*time.Second))

	// Forked conversation contains 1 cloned parent step (cascade_id=conv-parent) + 1 own step (cascade_id=conv-fork).
	forkDB := createTestConvDB(t, rootDir, "conv-fork")
	defer forkDB.Close()
	insertWithCascade(forkDB, 0, 1, "conv-parent", 5000, 500, now)
	insertWithCascade(forkDB, 1, 2, "conv-fork", 3000, 300, now.Add(2*time.Minute))

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	snap := prov.Snapshot()

	parent, ok := snap.FindConversation("conv-parent")
	if !ok {
		t.Fatal("conv-parent not found")
	}
	if parent.Title != "日志分析" {
		t.Fatalf("parent.Title = %q, want 日志分析 (from annotations/*.pbtxt)", parent.Title)
	}
	if len(parent.Subagents) != 1 || parent.Subagents[0].Name != "Protocol Inspector" {
		t.Fatalf("subagent metadata role fallback failed: %+v", parent.Subagents)
	}
	if parent.Root.ModelDisplay != "Gemini 4 Neon (XHigh)" {
		t.Fatalf("placeholder model display = %q, want Gemini 4 Neon (XHigh)", parent.Root.ModelDisplay)
	}

	fork, ok := snap.FindConversation("conv-fork")
	if !ok {
		t.Fatal("conv-fork not found")
	}
	if fork.TreeMetrics.RequestCount != 1 {
		t.Fatalf("fork RequestCount = %d, want 1 (cloned parent step must be filtered)", fork.TreeMetrics.RequestCount)
	}
	if fork.Requests[0].InputTokens != 3000 {
		t.Fatalf("fork kept wrong request: %+v", fork.Requests[0])
	}
}

func TestProvider_UnknownModelEnumPrefersTranscriptModel(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	if _, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES ('conv-enum', 'Enum Fallback', ?, 'CASCADE_RUN_STATUS_IDLE')`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	convDB := createTestConvDB(t, rootDir, "conv-enum")
	defer convDB.Close()
	var packed []byte
	packed = protowireAppendVarint(packed, 1)
	usage := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100) // 100 output tokens
		return b
	})
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 1999) // MODEL_PLACEHOLDER_M999 (unresolved)
		b = appendSubMsg(b, 4, usage)
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 2, packed)
		b = appendSubMsg(b, 1, inner)
		return b
	})
	if _, err := convDB.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blob, len(blob)); err != nil {
		t.Fatalf("insert gen_metadata: %v", err)
	}
	tsMsg := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, uint64(now.Unix()))
		return b
	})
	if _, err := convDB.Exec(`INSERT OR REPLACE INTO steps(idx, step_type, metadata) VALUES(1, 15, ?)`, appendSubMsg(nil, 1, tsMsg)); err != nil {
		t.Fatalf("insert step: %v", err)
	}

	trPath := TranscriptPath(rootDir, "conv-enum")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLine := `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T11:00:00Z","model":"claude-opus-5-5","output_tokens":100}` + "\n"
	if err := os.WriteFile(trPath, []byte(trLine), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	conv, ok := prov.Snapshot().FindConversation("conv-enum")
	if !ok || len(conv.Requests) != 1 {
		t.Fatalf("expected 1 request for conv-enum, got ok=%v len=%d", ok, len(conv.Requests))
	}
	req := conv.Requests[0]
	if req.ModelSlug != "claude-opus-5-5" {
		t.Fatalf("ModelSlug = %q, want claude-opus-5-5", req.ModelSlug)
	}
	wantCost := 100 * 75.0 / 1e6
	if diff := req.CostUSD - wantCost; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("CostUSD = %.8f, want %.8f", req.CostUSD, wantCost)
	}
}

func TestConvDBPool_StandaloneTrajectoryMetadataWriteTriggersRefresh(t *testing.T) {
	rootDir := t.TempDir()
	convDB := createTestConvDB(t, rootDir, "sub-meta")
	defer convDB.Close()

	if _, err := convDB.Exec(`CREATE TABLE trajectory_metadata_blob (id text PRIMARY KEY, data blob)`); err != nil {
		t.Fatalf("create trajectory_metadata_blob: %v", err)
	}
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	insertGenRow(t, convDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 0, 0, 100, 1100, 512000, time.Second, time.Second, now, "")

	pool := NewConvDBPool(filepath.Join(rootDir, "conversations"), 5)
	defer pool.Close()

	// Initial sync: reads gen_metadata, no trajectory_metadata_blob row yet.
	changed, _, _, err := pool.Sync(context.Background(), "sub-meta")
	if err != nil {
		t.Fatalf("initial Sync: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on initial Sync")
	}
	if got := pool.cachedMeta("sub-meta").SubagentRole; got != "" {
		t.Fatalf("expected empty initial SubagentRole, got %q", got)
	}

	// Idle sync: unchanged.
	changed, _, _, err = pool.Sync(context.Background(), "sub-meta")
	if err != nil {
		t.Fatalf("idle Sync: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false on idle Sync")
	}

	// Write ONLY trajectory_metadata_blob without appending any gen_metadata row.
	buildMeta := func(role string) []byte {
		spec := buildProtoMsg(func(b []byte) []byte {
			b = appendStringField(b, 1, "research")
			b = appendStringField(b, 2, role)
			return b
		})
		return buildProtoMsg(func(b []byte) []byte {
			b = appendStringField(b, 5, "parent-1")
			b = appendSubMsg(b, 8, spec)
			return b
		})
	}
	if _, err := convDB.Exec(`INSERT INTO trajectory_metadata_blob(id, data) VALUES('main', ?)`, buildMeta("Security Auditor")); err != nil {
		t.Fatalf("insert trajectory_metadata_blob: %v", err)
	}

	changed, _, _, err = pool.Sync(context.Background(), "sub-meta")
	if err != nil {
		t.Fatalf("post-meta Sync: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true after standalone trajectory_metadata_blob write")
	}
	if got := pool.cachedMeta("sub-meta").SubagentRole; got != "Security Auditor" {
		t.Fatalf("SubagentRole = %q, want Security Auditor", got)
	}

	// Update role in-place without appending gen_metadata.
	if _, err := convDB.Exec(`UPDATE trajectory_metadata_blob SET data = ? WHERE id = 'main'`, buildMeta("Lead Auditor")); err != nil {
		t.Fatalf("update trajectory_metadata_blob: %v", err)
	}
	changed, _, _, err = pool.Sync(context.Background(), "sub-meta")
	if err != nil {
		t.Fatalf("post-meta-update Sync: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true after standalone trajectory_metadata_blob update")
	}
	if got := pool.cachedMeta("sub-meta").SubagentRole; got != "Lead Auditor" {
		t.Fatalf("SubagentRole = %q, want Lead Auditor", got)
	}

	// Subsequent idle sync must return changed=false.
	changed, _, _, err = pool.Sync(context.Background(), "sub-meta")
	if err != nil {
		t.Fatalf("final idle Sync: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false on final idle Sync")
	}
}

func TestSummariesReader_StandaloneAnnotationChangesTriggerRefresh(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 13, 0, 0, 0, time.UTC)
	if _, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time) VALUES ('child', '', ?)`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	reader := NewSummariesReader(filepath.Join(rootDir, "conversation_summaries.db"))
	defer reader.Close()

	// 1. Initial read: empty title, no annotation file yet.
	rows, changed, err := reader.ReadChanges(context.Background())
	if err != nil || !changed || len(rows) != 1 || rows[0].Title != "" {
		t.Fatalf("initial ReadChanges: rows=%+v changed=%v err=%v", rows, changed, err)
	}

	// 2. Idle read: unchanged.
	if _, changed, err = reader.ReadChanges(context.Background()); err != nil || changed {
		t.Fatalf("idle ReadChanges: changed=%v err=%v", changed, err)
	}

	// 3. Create annotations/child.pbtxt without modifying conversation_summaries.db.
	annPath := filepath.Join(rootDir, "annotations", "child.pbtxt")
	if err := os.MkdirAll(filepath.Dir(annPath), 0o755); err != nil {
		t.Fatalf("mkdir annotations: %v", err)
	}
	if err := os.WriteFile(annPath, []byte("title: \"Child Real Title\"\n"), 0o600); err != nil {
		t.Fatalf("write annotation: %v", err)
	}

	rows, changed, err = reader.ReadChanges(context.Background())
	if err != nil || !changed || len(rows) != 1 || rows[0].Title != "Child Real Title" {
		t.Fatalf("post-create ReadChanges: rows=%+v changed=%v err=%v", rows, changed, err)
	}

	// 4. Idle read after picking up annotation title: unchanged.
	if _, changed, err = reader.ReadChanges(context.Background()); err != nil || changed {
		t.Fatalf("post-create idle ReadChanges: changed=%v err=%v", changed, err)
	}

	// 5. Modify annotations/child.pbtxt in-place (same byte length as before) without touching DB.
	if err := os.WriteFile(annPath, []byte("title: \"Child Next Title\"\n"), 0o600); err != nil {
		t.Fatalf("update annotation: %v", err)
	}
	rows, changed, err = reader.ReadChanges(context.Background())
	if err != nil || !changed || len(rows) != 1 || rows[0].Title != "Child Next Title" {
		t.Fatalf("post-update ReadChanges: rows=%+v changed=%v err=%v", rows, changed, err)
	}

	// 6. Delete annotations/child.pbtxt: title reverts to empty and triggers refresh.
	if err := os.Remove(annPath); err != nil {
		t.Fatalf("remove annotation: %v", err)
	}
	rows, changed, err = reader.ReadChanges(context.Background())
	if err != nil || !changed || len(rows) != 1 || rows[0].Title != "" {
		t.Fatalf("post-delete ReadChanges: rows=%+v changed=%v err=%v", rows, changed, err)
	}
}

func TestProvider_ForkedBranchMatchesHistoryWithClonedDBStepAndOwnTranscriptStep(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 14, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		id, title string
	}{
		{"conv-parent", "Parent Session"},
		{"conv-fork-own-tr", "Fork Own Transcript"},
		{"conv-fork-empty", "Fork Empty"},
	} {
		if _, err := sumDB.Exec(
			`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES (?, ?, ?, 'CASCADE_RUN_STATUS_IDLE')`,
			row.id, row.title, now.Format(time.RFC3339),
		); err != nil {
			t.Fatalf("insert summary %s: %v", row.id, err)
		}
	}

	insertClonedGen := func(db *sql.DB, idx, stepIdx int, cascadeID string, inTok, outTok int64, ts time.Time) {
		var packed []byte
		packed = protowireAppendVarint(packed, uint64(stepIdx))
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
		if _, err := db.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(?, ?, ?)`, idx, blob, len(blob)); err != nil {
			t.Fatalf("insert gen_metadata: %v", err)
		}
		tsMsg := buildProtoMsg(func(b []byte) []byte {
			b = appendVarintField(b, 1, uint64(ts.Unix()))
			return b
		})
		if _, err := db.Exec(`INSERT OR REPLACE INTO steps(idx, step_type, metadata) VALUES(?, 15, ?)`, stepIdx, appendSubMsg(nil, 1, tsMsg)); err != nil {
			t.Fatalf("insert step: %v", err)
		}
	}

	parentDB := createTestConvDB(t, rootDir, "conv-parent")
	defer parentDB.Close()
	insertClonedGen(parentDB, 0, 1, "conv-parent", 10_000, 1_000, now.Add(-10*time.Minute))

	// Fork with ONLY cloned parent step 1 in DB, plus cloned step 1 and own step 2 in transcript.jsonl.
	forkOwnDB := createTestConvDB(t, rootDir, "conv-fork-own-tr")
	defer forkOwnDB.Close()
	insertClonedGen(forkOwnDB, 0, 1, "conv-parent", 10_000, 1_000, now.Add(-10*time.Minute))

	trLine1 := `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T13:50:00Z","model":"gemini-4-neon","input_tokens":10000,"output_tokens":1000}` + "\n"
	trLine2 := `{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T13:55:00Z","model":"gemini-4-neon","input_tokens":2000,"output_tokens":200}` + "\n"
	trPathOwn := TranscriptPath(rootDir, "conv-fork-own-tr")
	if err := os.MkdirAll(filepath.Dir(trPathOwn), 0o755); err != nil {
		t.Fatalf("mkdir transcript own: %v", err)
	}
	if err := os.WriteFile(trPathOwn, []byte(trLine1+trLine2), 0o600); err != nil {
		t.Fatalf("write transcript own: %v", err)
	}

	// Fork with ONLY cloned parent step 1 in DB and ONLY cloned step 1 in transcript.jsonl.
	forkEmptyDB := createTestConvDB(t, rootDir, "conv-fork-empty")
	defer forkEmptyDB.Close()
	insertClonedGen(forkEmptyDB, 0, 1, "conv-parent", 10_000, 1_000, now.Add(-10*time.Minute))

	trPathEmpty := TranscriptPath(rootDir, "conv-fork-empty")
	if err := os.MkdirAll(filepath.Dir(trPathEmpty), 0o755); err != nil {
		t.Fatalf("mkdir transcript empty: %v", err)
	}
	if err := os.WriteFile(trPathEmpty, []byte(trLine1), 0o600); err != nil {
		t.Fatalf("write transcript empty: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	snap := prov.Snapshot()

	forkOwn, ok := snap.FindConversation("conv-fork-own-tr")
	if !ok {
		t.Fatal("conv-fork-own-tr not found")
	}
	if forkOwn.TreeMetrics.RequestCount != 1 || len(forkOwn.Requests) != 1 {
		t.Fatalf("conv-fork-own-tr RequestCount = %d (len=%d), want 1 (must exclude cloned step 1 and keep own transcript step 2)", forkOwn.TreeMetrics.RequestCount, len(forkOwn.Requests))
	}
	if forkOwn.Requests[0].StepIndex != 2 || forkOwn.Requests[0].InputTokens != 2000 || forkOwn.Requests[0].OutputTokens != 200 {
		t.Fatalf("conv-fork-own-tr kept wrong request: %+v", forkOwn.Requests[0])
	}

	forkEmpty, ok := snap.FindConversation("conv-fork-empty")
	if !ok {
		t.Fatal("conv-fork-empty not found")
	}
	if forkEmpty.TreeMetrics.RequestCount != 0 || len(forkEmpty.Requests) != 0 {
		t.Fatalf("conv-fork-empty RequestCount = %d (len=%d), want 0 (cloned parent step must not be re-added)", forkEmpty.TreeMetrics.RequestCount, len(forkEmpty.Requests))
	}
}

func TestConvDBPool_DeletingTrajectoryMetadataRowClearsCachedRole(t *testing.T) {
	rootDir := t.TempDir()
	convDB := createTestConvDB(t, rootDir, "sub-del")
	defer convDB.Close()

	if _, err := convDB.Exec(`CREATE TABLE trajectory_metadata_blob (id text PRIMARY KEY, data blob)`); err != nil {
		t.Fatalf("create trajectory_metadata_blob: %v", err)
	}
	now := time.Date(2026, 11, 25, 12, 30, 0, 0, time.UTC)
	insertGenRow(t, convDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1000, 0, 0, 100, 1100, 512000, time.Second, time.Second, now, "")

	spec := buildProtoMsg(func(b []byte) []byte {
		b = appendStringField(b, 1, "review")
		b = appendStringField(b, 2, "Reviewer")
		return b
	})
	metaBlob := buildProtoMsg(func(b []byte) []byte {
		b = appendStringField(b, 5, "parent-1")
		b = appendSubMsg(b, 8, spec)
		return b
	})
	if _, err := convDB.Exec(`INSERT INTO trajectory_metadata_blob(id, data) VALUES('main', ?)`, metaBlob); err != nil {
		t.Fatalf("insert trajectory_metadata_blob: %v", err)
	}

	pool := NewConvDBPool(filepath.Join(rootDir, "conversations"), 5)
	defer pool.Close()

	// 1. Initial sync reads Reviewer role.
	changed, _, _, err := pool.Sync(context.Background(), "sub-del")
	if err != nil || !changed {
		t.Fatalf("initial Sync: changed=%v err=%v", changed, err)
	}
	if got := pool.cachedMeta("sub-del").SubagentRole; got != "Reviewer" {
		t.Fatalf("initial SubagentRole = %q, want Reviewer", got)
	}

	// 2. Transient/canceled context error on readTrajectoryMetadata must be distinguished from confirmed deletion.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, readErr := readTrajectoryMetadata(canceledCtx, convDB); readErr == nil {
		t.Fatal("expected readTrajectoryMetadata with canceled context to return an error")
	}

	// 3. Delete the 'main' row from trajectory_metadata_blob -> Sync must clear cached role and report changed=true.
	if _, err := convDB.Exec(`DELETE FROM trajectory_metadata_blob WHERE id = 'main'`); err != nil {
		t.Fatalf("delete trajectory_metadata_blob: %v", err)
	}
	changed, _, _, err = pool.Sync(context.Background(), "sub-del")
	if err != nil {
		t.Fatalf("post-delete Sync: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true after deleting trajectory_metadata_blob main row")
	}
	if got := pool.cachedMeta("sub-del").SubagentRole; got != "" {
		t.Fatalf("post-delete SubagentRole = %q, want empty", got)
	}
	if got := pool.cachedMeta("sub-del"); got != (TrajectoryMetadata{}) {
		t.Fatalf("post-delete TrajectoryMetadata = %+v, want zero value", got)
	}

	// 4. Subsequent idle sync must return changed=false.
	changed, _, _, err = pool.Sync(context.Background(), "sub-del")
	if err != nil || changed {
		t.Fatalf("post-delete idle Sync: changed=%v err=%v", changed, err)
	}
}

func TestProvider_PartialDBWriteMergesRemainingTranscriptSteps(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)
	if _, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES ('conv-fork-partial', 'Fork Partial', ?, 'CASCADE_RUN_STATUS_RUNNING')`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	insertWithCascade := func(db *sql.DB, idx, stepIdx int, cascadeID string, inTok, outTok int64, ts time.Time) {
		var packed []byte
		packed = protowireAppendVarint(packed, uint64(stepIdx))
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
		if _, err := db.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(?, ?, ?)`, idx, blob, len(blob)); err != nil {
			t.Fatalf("insert gen_metadata: %v", err)
		}
		tsMsg := buildProtoMsg(func(b []byte) []byte {
			b = appendVarintField(b, 1, uint64(ts.Unix()))
			return b
		})
		if _, err := db.Exec(`INSERT OR REPLACE INTO steps(idx, step_type, metadata) VALUES(?, 15, ?)`, stepIdx, appendSubMsg(nil, 1, tsMsg)); err != nil {
			t.Fatalf("insert step: %v", err)
		}
	}

	// Initially DB has ONLY cloned parent step 1; transcript has cloned step 1 + own steps 2 and 3.
	forkDB := createTestConvDB(t, rootDir, "conv-fork-partial")
	defer forkDB.Close()
	insertWithCascade(forkDB, 0, 1, "conv-parent", 10_000, 1_000, now.Add(-10*time.Minute))

	trPath := TranscriptPath(rootDir, "conv-fork-partial")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trContent := "" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T14:50:00Z","model":"gemini-4-neon","input_tokens":10000,"output_tokens":1000}` + "\n" +
		`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T14:55:00Z","model":"gemini-4-neon","input_tokens":2000,"output_tokens":200}` + "\n" +
		`{"step_index":3,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T14:58:00Z","model":"gemini-4-neon","input_tokens":3000,"output_tokens":300}` + "\n"
	if err := os.WriteFile(trPath, []byte(trContent), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	conv1, ok := prov.Snapshot().FindConversation("conv-fork-partial")
	if !ok || conv1.TreeMetrics.RequestCount != 2 {
		t.Fatalf("initial RequestCount = %d (ok=%v), want 2 (steps 2 and 3 from transcript)", conv1.TreeMetrics.RequestCount, ok)
	}

	// Persist ONLY step 2 into DB while step 3 remains only in transcript.
	insertWithCascade(forkDB, 1, 2, "conv-fork-partial", 2_000, 200, now.Add(-5*time.Minute))
	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after partial DB write: %v", err)
	}

	conv2, ok := prov.Snapshot().FindConversation("conv-fork-partial")
	if !ok || conv2.TreeMetrics.RequestCount != 2 || len(conv2.Requests) != 2 {
		t.Fatalf("after partial DB write: RequestCount=%d len=%d (ok=%v), want 2 (must keep step 3 from transcript without dropping to 1)", conv2.TreeMetrics.RequestCount, len(conv2.Requests), ok)
	}
	if conv2.Requests[0].StepIndex != 2 || conv2.Requests[1].StepIndex != 3 {
		t.Fatalf("unexpected merged steps: [%d, %d], want [2, 3]", conv2.Requests[0].StepIndex, conv2.Requests[1].StepIndex)
	}

	// Persist step 3 into DB as well: must still be 2 requests (no duplication).
	insertWithCascade(forkDB, 2, 3, "conv-fork-partial", 3_000, 300, now.Add(-2*time.Minute))
	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after full DB write: %v", err)
	}
	conv3, ok := prov.Snapshot().FindConversation("conv-fork-partial")
	if !ok || conv3.TreeMetrics.RequestCount != 2 || len(conv3.Requests) != 2 {
		t.Fatalf("after full DB write: RequestCount=%d len=%d (ok=%v), want 2", conv3.TreeMetrics.RequestCount, len(conv3.Requests), ok)
	}
}

func TestProvider_CanceledZeroTokenDBStepNotResurrectedByTranscript(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	if _, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES ('conv-canceled', 'Canceled Request', ?, 'CASCADE_RUN_STATUS_IDLE')`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	convDB := createTestConvDB(t, rootDir, "conv-canceled")
	defer convDB.Close()

	// Insert confirmed empty failure (f5="context canceled", all tokens 0) for step 1.
	var packed []byte
	packed = protowireAppendVarint(packed, 1)
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendStringField(b, 19, "gemini-4-neon")
		return b
	})
	blob := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, inner)
		b = appendSubMsg(b, 2, packed)
		b = appendStringField(b, 5, "context canceled")
		return b
	})
	if _, err := convDB.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blob, len(blob)); err != nil {
		t.Fatalf("insert canceled gen_metadata: %v", err)
	}
	tsMsg := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, uint64(now.Unix()))
		return b
	})
	if _, err := convDB.Exec(`INSERT OR REPLACE INTO steps(idx, step_type, metadata) VALUES(1, 15, ?)`, appendSubMsg(nil, 1, tsMsg)); err != nil {
		t.Fatalf("insert step: %v", err)
	}

	// Same step 1 appears in transcript.jsonl as a zero-token PLANNER_RESPONSE.
	trPath := TranscriptPath(rootDir, "conv-canceled")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLine := `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T16:00:00Z","model":"gemini-4-neon","input_tokens":0,"output_tokens":0}` + "\n"
	if err := os.WriteFile(trPath, []byte(trLine), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	conv, ok := prov.Snapshot().FindConversation("conv-canceled")
	if !ok {
		t.Fatal("conv-canceled not found")
	}
	if conv.TreeMetrics.RequestCount != 0 || len(conv.Requests) != 0 {
		t.Fatalf("canceled zero-token step was resurrected: RequestCount=%d requests=%+v, want 0", conv.TreeMetrics.RequestCount, conv.Requests)
	}
}

func TestProvider_MissingStepIndexAndMultiStepGenerationUsage(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 21, 0, 0, 0, time.UTC)
	for _, row := range []struct{ id, title string }{
		{"conv-no-step", "No Step Index"},
		{"conv-multi-step", "Multi Step Gen"},
	} {
		if _, err := sumDB.Exec(
			`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES (?, ?, ?, 'CASCADE_RUN_STATUS_IDLE')`,
			row.id, row.title, now.Format(time.RFC3339),
		); err != nil {
			t.Fatalf("insert summary %s: %v", row.id, err)
		}
	}

	// 1. conv-no-step: DB gen 0 has NO StepIndices and NO last_step_index, 100 output tokens;
	// transcript has same request at step 7 -> must stay 1 request, 100 tokens.
	dbNoStep := createTestConvDB(t, rootDir, "conv-no-step")
	defer dbNoStep.Close()
	usage100 := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100)
		return b
	})
	innerNoStep := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage100)
		b = appendStringField(b, 19, "gemini-4-neon")
		return b
	})
	blobNoStep := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, innerNoStep)
		return b
	})
	if _, err := dbNoStep.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blobNoStep, len(blobNoStep)); err != nil {
		t.Fatalf("insert conv-no-step gen: %v", err)
	}
	trNoStep := TranscriptPath(rootDir, "conv-no-step")
	if err := os.MkdirAll(filepath.Dir(trNoStep), 0o755); err != nil {
		t.Fatalf("mkdir trNoStep: %v", err)
	}
	if err := os.WriteFile(trNoStep, []byte(`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T21:00:00Z","model":"gemini-4-neon","output_tokens":100}`+"\n"), 0o600); err != nil {
		t.Fatalf("write trNoStep: %v", err)
	}

	// 2. conv-multi-step: DB gen 0 covers [6, 7] with 0 tokens; transcript provides 100 tokens on step 7 ->
	// must resolve to 1 request, 100 tokens.
	dbMulti := createTestConvDB(t, rootDir, "conv-multi-step")
	defer dbMulti.Close()
	var packed []byte
	packed = protowireAppendVarint(packed, 6)
	packed = protowireAppendVarint(packed, 7)
	innerMulti := buildProtoMsg(func(b []byte) []byte {
		b = appendStringField(b, 19, "gemini-4-neon")
		return b
	})
	blobMulti := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, innerMulti)
		b = appendSubMsg(b, 2, packed)
		return b
	})
	if _, err := dbMulti.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blobMulti, len(blobMulti)); err != nil {
		t.Fatalf("insert conv-multi-step gen: %v", err)
	}
	trMulti := TranscriptPath(rootDir, "conv-multi-step")
	if err := os.MkdirAll(filepath.Dir(trMulti), 0o755); err != nil {
		t.Fatalf("mkdir trMulti: %v", err)
	}
	if err := os.WriteFile(trMulti, []byte(`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T21:00:00Z","model":"gemini-4-neon","output_tokens":100}`+"\n"), 0o600); err != nil {
		t.Fatalf("write trMulti: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	snap := prov.Snapshot()

	noStep, ok := snap.FindConversation("conv-no-step")
	if !ok {
		t.Fatal("conv-no-step not found")
	}
	if noStep.TreeMetrics.RequestCount != 1 || noStep.TreeMetrics.TotalOutputTokens != 100 {
		t.Fatalf("conv-no-step got RequestCount=%d Output=%d, want 1 / 100", noStep.TreeMetrics.RequestCount, noStep.TreeMetrics.TotalOutputTokens)
	}

	multi, ok := snap.FindConversation("conv-multi-step")
	if !ok {
		t.Fatal("conv-multi-step not found")
	}
	if multi.TreeMetrics.RequestCount != 1 || multi.TreeMetrics.TotalOutputTokens != 100 {
		t.Fatalf("conv-multi-step got RequestCount=%d Output=%d, want 1 / 100", multi.TreeMetrics.RequestCount, multi.TreeMetrics.TotalOutputTokens)
	}
}

func TestProvider_TranscriptDeleteAndSameLengthReplaceRefresh(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 22, 0, 0, 0, time.UTC)
	if _, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES ('conv-tr-mut', 'Transcript Mutate', ?, 'CASCADE_RUN_STATUS_RUNNING')`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	// DB has step 1 with 10 output tokens.
	convDB := createTestConvDB(t, rootDir, "conv-tr-mut")
	defer convDB.Close()
	insertGenRow(t, convDB, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 0, 0, 0, 10, 10, 512000, time.Second, time.Second, now, "")

	// Transcript initially has step 1 (10 output) + step 2 (100 output) -> total 2 requests, 110 output.
	trPath := TranscriptPath(rootDir, "conv-tr-mut")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trV1 := "" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T22:00:00Z","model":"gemini-4-neon","output_tokens":10}` + "\n" +
		`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T22:01:00Z","model":"gemini-4-neon","output_tokens":100}` + "\n"
	trV2 := "" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T22:00:00Z","model":"gemini-4-neon","output_tokens":10}` + "\n" +
		`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T22:01:00Z","model":"gemini-4-neon","output_tokens":200}` + "\n"
	if len(trV1) != len(trV2) {
		t.Fatalf("test setup error: trV1 len (%d) != trV2 len (%d)", len(trV1), len(trV2))
	}
	if err := os.WriteFile(trPath, []byte(trV1), 0o600); err != nil {
		t.Fatalf("write trV1: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	conv1, ok := prov.Snapshot().FindConversation("conv-tr-mut")
	if !ok || conv1.TreeMetrics.RequestCount != 2 || conv1.TreeMetrics.TotalOutputTokens != 110 {
		t.Fatalf("initial snapshot got RequestCount=%d Output=%d (ok=%v), want 2 / 110", conv1.TreeMetrics.RequestCount, conv1.TreeMetrics.TotalOutputTokens, ok)
	}

	// 1. Replace transcript in-place with an IDENTICAL byte-length file (step 2 output: 100 -> 200).
	if err := os.WriteFile(trPath, []byte(trV2), 0o600); err != nil {
		t.Fatalf("write trV2: %v", err)
	}
	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after same-length replace: %v", err)
	}
	conv2, ok := prov.Snapshot().FindConversation("conv-tr-mut")
	if !ok || conv2.TreeMetrics.RequestCount != 2 || conv2.TreeMetrics.TotalOutputTokens != 210 {
		t.Fatalf("after same-length replace got RequestCount=%d Output=%d (ok=%v), want 2 / 210", conv2.TreeMetrics.RequestCount, conv2.TreeMetrics.TotalOutputTokens, ok)
	}

	// 2. Delete transcript.jsonl -> Refresh must clear cached transcript state and leave only DB step 1 (1 request, 10 output).
	if err := os.Remove(trPath); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}
	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after transcript delete: %v", err)
	}
	conv3, ok := prov.Snapshot().FindConversation("conv-tr-mut")
	if !ok || conv3.TreeMetrics.RequestCount != 1 || conv3.TreeMetrics.TotalOutputTokens != 10 {
		t.Fatalf("after transcript delete got RequestCount=%d Output=%d (ok=%v), want 1 / 10", conv3.TreeMetrics.RequestCount, conv3.TreeMetrics.TotalOutputTokens, ok)
	}
}

func TestProvider_MissingStepIndexBackfillsAndMergesSubsequentTranscriptRequest(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 22, 30, 0, 0, time.UTC)
	for _, row := range []struct{ id, title string }{
		{"conv-est-backfill", "Est Backfill"},
		{"conv-est-merge", "Est Merge"},
	} {
		if _, err := sumDB.Exec(
			`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES (?, ?, ?, 'CASCADE_RUN_STATUS_IDLE')`,
			row.id, row.title, now.Format(time.RFC3339),
		); err != nil {
			t.Fatalf("insert summary %s: %v", row.id, err)
		}
	}

	// 1. Unknown placeholder + 0 tokens + no step index in DB -> backfill Claude Opus 100 tokens & $0.0075.
	dbBackfill := createTestConvDB(t, rootDir, "conv-est-backfill")
	defer dbBackfill.Close()
	innerBackfill := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 1999) // MODEL_PLACEHOLDER_M999
		return b
	})
	blobBackfill := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, innerBackfill)
		return b
	})
	if _, err := dbBackfill.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blobBackfill, len(blobBackfill)); err != nil {
		t.Fatalf("insert conv-est-backfill gen: %v", err)
	}
	trBackfill := TranscriptPath(rootDir, "conv-est-backfill")
	if err := os.MkdirAll(filepath.Dir(trBackfill), 0o755); err != nil {
		t.Fatalf("mkdir trBackfill: %v", err)
	}
	if err := os.WriteFile(trBackfill, []byte(`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T22:30:00Z","model":"claude-opus-5-5","output_tokens":100}`+"\n"), 0o600); err != nil {
		t.Fatalf("write trBackfill: %v", err)
	}

	// 2. Claude 100 tokens with no step index in DB; transcript has Claude 100 (step 7) + Gemini 200 (step 9).
	dbMerge := createTestConvDB(t, rootDir, "conv-est-merge")
	defer dbMerge.Close()
	usage100 := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100)
		return b
	})
	innerMerge := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage100)
		b = appendStringField(b, 19, "claude-opus-5-5")
		return b
	})
	blobMerge := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, innerMerge)
		return b
	})
	if _, err := dbMerge.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blobMerge, len(blobMerge)); err != nil {
		t.Fatalf("insert conv-est-merge gen: %v", err)
	}
	trMerge := TranscriptPath(rootDir, "conv-est-merge")
	if err := os.MkdirAll(filepath.Dir(trMerge), 0o755); err != nil {
		t.Fatalf("mkdir trMerge: %v", err)
	}
	trMergeLines := "" +
		`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T22:30:00Z","model":"claude-opus-5-5","output_tokens":100}` + "\n" +
		`{"step_index":9,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T22:31:00Z","model":"gemini-4-neon","output_tokens":200}` + "\n"
	if err := os.WriteFile(trMerge, []byte(trMergeLines), 0o600); err != nil {
		t.Fatalf("write trMerge: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	snap := prov.Snapshot()

	bf, ok := snap.FindConversation("conv-est-backfill")
	if !ok {
		t.Fatal("conv-est-backfill not found")
	}
	wantCost := 100 * 75.0 / 1e6
	if bf.TreeMetrics.RequestCount != 1 || bf.TreeMetrics.TotalOutputTokens != 100 || bf.TreeMetrics.SessionCostUSD < wantCost-1e-12 || bf.TreeMetrics.SessionCostUSD > wantCost+1e-12 {
		t.Fatalf("conv-est-backfill got RequestCount=%d Output=%d Cost=%.8f, want 1 / 100 / %.8f", bf.TreeMetrics.RequestCount, bf.TreeMetrics.TotalOutputTokens, bf.TreeMetrics.SessionCostUSD, wantCost)
	}

	mg, ok := snap.FindConversation("conv-est-merge")
	if !ok {
		t.Fatal("conv-est-merge not found")
	}
	if mg.TreeMetrics.RequestCount != 2 || mg.TreeMetrics.TotalOutputTokens != 300 {
		t.Fatalf("conv-est-merge got RequestCount=%d Output=%d, want 2 / 300", mg.TreeMetrics.RequestCount, mg.TreeMetrics.TotalOutputTokens)
	}
}

func TestProvider_TranscriptInPlaceRewriteGrowingReplacesOldRecords(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 23, 0, 0, 0, time.UTC)
	if _, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES ('conv-tr-grow', 'Transcript Grow Rewrite', ?, 'CASCADE_RUN_STATUS_RUNNING')`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	trPath := TranscriptPath(rootDir, "conv-tr-grow")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	v1 := `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T23:00:00Z","model":"gemini-4-neon","output_tokens":50}` + "\n"
	if err := os.WriteFile(trPath, []byte(v1), 0o600); err != nil {
		t.Fatalf("write v1: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	c1, ok := prov.Snapshot().FindConversation("conv-tr-grow")
	if !ok || c1.TreeMetrics.RequestCount != 1 || len(c1.Requests) != 1 || c1.Requests[0].StepIndex != 1 {
		t.Fatalf("initial snapshot got ok=%v count=%d reqs=%+v, want [step 1]", ok, c1.TreeMetrics.RequestCount, c1.Requests)
	}

	// Rewrite transcript.jsonl IN-PLACE so it replaces step 1 with steps 2 & 3 (strictly longer file).
	v2 := "" +
		`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T23:01:00Z","model":"gemini-4-neon","output_tokens":100}` + "\n" +
		`{"step_index":3,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T23:02:00Z","model":"gemini-4-neon","output_tokens":200}` + "\n"
	if len(v2) <= len(v1) {
		t.Fatalf("test setup error: v2 len (%d) must be > v1 len (%d)", len(v2), len(v1))
	}
	if err := os.WriteFile(trPath, []byte(v2), 0o600); err != nil {
		t.Fatalf("write v2: %v", err)
	}

	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after growing rewrite: %v", err)
	}
	c2, ok := prov.Snapshot().FindConversation("conv-tr-grow")
	if !ok || c2.TreeMetrics.RequestCount != 2 || len(c2.Requests) != 2 {
		t.Fatalf("post-rewrite snapshot got ok=%v count=%d reqs=%+v, want [step 2, step 3]", ok, c2.TreeMetrics.RequestCount, c2.Requests)
	}
	if c2.Requests[0].StepIndex != 2 || c2.Requests[1].StepIndex != 3 || c2.TreeMetrics.TotalOutputTokens != 300 {
		t.Fatalf("post-rewrite got steps=[%d, %d] output=%d, want [2, 3] / 300", c2.Requests[0].StepIndex, c2.Requests[1].StepIndex, c2.TreeMetrics.TotalOutputTokens)
	}
}

func TestMatchPlanners_DistinctModelsPriorityOrderAndForeignEmptyRow(t *testing.T) {
	calc, err := metrics.NewDefaultCalculator()
	if err != nil {
		t.Fatalf("NewDefaultCalculator: %v", err)
	}

	// 1. Distinct models sharing a display bucket (gpt-5.4 vs gpt-5.5) with identical 100 output tokens
	// must NOT be merged into one request.
	trGPT := TranscriptData{
		Planners: []TranscriptPlanner{
			{StepIndex: 7, Model: "gpt-5.5", OutputTokens: 100},
		},
	}
	resGPT := trGPT.MatchPlanners([]GenMetadata{
		{GenIndex: 0, StepIndex: 0, StepIndexEstimated: true, ModelSlug: "gpt-5.4", OutputTokens: 100},
	}, "conv-gpt", calc)
	if resGPT.MatchedByGen[0] != nil {
		t.Fatalf("gpt-5.4 must not match gpt-5.5, got %+v", resGPT.MatchedByGen[0])
	}
	if len(resGPT.UnclaimedPlanners) != 1 || resGPT.UnclaimedPlanners[0].Model != "gpt-5.5" {
		t.Fatalf("expected gpt-5.5 to remain unclaimed, got %+v", resGPT.UnclaimedPlanners)
	}

	// 2. Unknown placeholder BEFORE known Claude in DB, with Claude + Gemini in transcript (all 100 output):
	// Claude must claim Claude first so unknown placeholder gets Gemini -> 2 total requests, not 3.
	trPriority := TranscriptData{
		Planners: []TranscriptPlanner{
			{StepIndex: 5, Model: "claude-opus-5-5", OutputTokens: 100},
			{StepIndex: 7, Model: "gemini-4-neon", OutputTokens: 100},
		},
	}
	resPriority := trPriority.MatchPlanners([]GenMetadata{
		{GenIndex: 0, StepIndex: 0, StepIndexEstimated: true, ModelSlug: "MODEL_PLACEHOLDER_M999", OutputTokens: 100},
		{GenIndex: 1, StepIndex: 1, StepIndexEstimated: true, ModelSlug: "claude-opus-5-5", OutputTokens: 100},
	}, "conv-prio", calc)
	if resPriority.MatchedByGen[1] == nil || resPriority.MatchedByGen[1].Model != "claude-opus-5-5" {
		t.Fatalf("expected DB Claude to match transcript Claude, got %+v", resPriority.MatchedByGen[1])
	}
	if resPriority.MatchedByGen[0] == nil || resPriority.MatchedByGen[0].Model != "gemini-4-neon" {
		t.Fatalf("expected DB unknown placeholder to match transcript Gemini, got %+v", resPriority.MatchedByGen[0])
	}
	if len(resPriority.UnclaimedPlanners) != 0 {
		t.Fatalf("expected 0 unclaimed planners (2 requests total), got %+v", resPriority.UnclaimedPlanners)
	}

	// 3. Uncorroborated empty cloned parent record (no step index, no model, 0 tokens) must NOT
	// swallow a child's 100-token Claude transcript request.
	trChild := TranscriptData{
		Planners: []TranscriptPlanner{
			{StepIndex: 3, Model: "claude-opus-5-5", OutputTokens: 100},
		},
	}
	resChild := trChild.MatchPlanners([]GenMetadata{
		{GenIndex: 0, StepIndex: 0, StepIndexEstimated: true, CascadeID: "conv-parent"},
	}, "conv-child", calc)
	if len(resChild.UnclaimedPlanners) != 1 || resChild.UnclaimedPlanners[0].OutputTokens != 100 {
		t.Fatalf("empty parent row swallowed child request: unclaimed=%+v", resChild.UnclaimedPlanners)
	}
}

func TestProvider_SmallTranscriptMidFileRewriteAndAppendUpdatesUsage(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 23, 30, 0, 0, time.UTC)
	if _, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES ('conv-tr-mid', 'Mid Transcript Rewrite', ?, 'CASCADE_RUN_STATUS_RUNNING')`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	trPath := TranscriptPath(rootDir, "conv-tr-mid")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}

	// Build a <=64 KiB transcript whose first 4 KiB and last 4 KiB stay identical while
	// a middle PLANNER_RESPONSE changes output_tokens from 100 -> 200 and a new step (+300) is appended.
	padHead := `{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-25T23:30:00Z","content":"` + strings.Repeat("H", 16*1024) + `"}` + "\n"
	midV1 := `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T23:30:10Z","model":"gemini-4-neon","output_tokens":100}` + "\n"
	midV2 := `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T23:30:10Z","model":"gemini-4-neon","output_tokens":200}` + "\n"
	padTail := `{"step_index":2,"type":"GENERIC","created_at":"2026-11-25T23:30:20Z","content":"` + strings.Repeat("T", 16*1024) + `"}` + "\n"
	appended := `{"step_index":3,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T23:30:30Z","model":"gemini-4-neon","output_tokens":300}` + "\n"

	v1 := padHead + midV1 + padTail
	if err := os.WriteFile(trPath, []byte(v1), 0o600); err != nil {
		t.Fatalf("write v1: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	c1, ok := prov.Snapshot().FindConversation("conv-tr-mid")
	if !ok || c1.TreeMetrics.RequestCount != 1 || c1.TreeMetrics.TotalOutputTokens != 100 {
		t.Fatalf("initial snapshot got ok=%v count=%d output=%d, want 1 / 100", ok, c1.TreeMetrics.RequestCount, c1.TreeMetrics.TotalOutputTokens)
	}

	v2 := padHead + midV2 + padTail + appended
	if err := os.WriteFile(trPath, []byte(v2), 0o600); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after mid-file rewrite + append: %v", err)
	}
	c2, ok := prov.Snapshot().FindConversation("conv-tr-mid")
	if !ok || c2.TreeMetrics.RequestCount != 2 || c2.TreeMetrics.TotalOutputTokens != 500 {
		t.Fatalf("post-rewrite snapshot got ok=%v count=%d output=%d, want 2 / 500", ok, c2.TreeMetrics.RequestCount, c2.TreeMetrics.TotalOutputTokens)
	}
}

func TestProvider_DisplayNameVersionDistinctionAndPlaceholderDisplayFallback(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 25, 23, 45, 0, 0, time.UTC)
	for _, row := range []struct{ id, title string }{
		{"conv-disp-distinct", "Display Distinct Version"},
		{"conv-placeholder-disp", "Placeholder With Display"},
	} {
		if _, err := sumDB.Exec(
			`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES (?, ?, ?, 'CASCADE_RUN_STATUS_IDLE')`,
			row.id, row.title, now.Format(time.RFC3339),
		); err != nil {
			t.Fatalf("insert summary %s: %v", row.id, err)
		}
	}

	// 1. DB has ONLY display name "GPT-5.4" (no slug, no step index), 100 output;
	// transcript has "gpt-5.5", 100 output -> must stay 2 requests / 200 tokens.
	dbDist := createTestConvDB(t, rootDir, "conv-disp-distinct")
	defer dbDist.Close()
	usage100 := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100)
		return b
	})
	innerDist := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage100)
		b = appendStringField(b, 21, "GPT-5.4")
		return b
	})
	blobDist := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, innerDist)
		return b
	})
	if _, err := dbDist.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blobDist, len(blobDist)); err != nil {
		t.Fatalf("insert conv-disp-distinct: %v", err)
	}
	trDist := TranscriptPath(rootDir, "conv-disp-distinct")
	if err := os.MkdirAll(filepath.Dir(trDist), 0o755); err != nil {
		t.Fatalf("mkdir trDist: %v", err)
	}
	if err := os.WriteFile(trDist, []byte(`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T23:45:00Z","model":"gpt-5.5","output_tokens":100}`+"\n"), 0o600); err != nil {
		t.Fatalf("write trDist: %v", err)
	}

	// 2. DB has unresolved placeholder MODEL_PLACEHOLDER_M999 + display name "GPT-5.4" (no step index), 100 output;
	// transcript has "gpt-5.4", 100 output -> must match as 1 request / 100 tokens.
	dbPh := createTestConvDB(t, rootDir, "conv-placeholder-disp")
	defer dbPh.Close()
	innerPh := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 1999) // MODEL_PLACEHOLDER_M999
		b = appendSubMsg(b, 4, usage100)
		b = appendStringField(b, 21, "GPT-5.4")
		return b
	})
	blobPh := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, innerPh)
		return b
	})
	if _, err := dbPh.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blobPh, len(blobPh)); err != nil {
		t.Fatalf("insert conv-placeholder-disp: %v", err)
	}
	trPh := TranscriptPath(rootDir, "conv-placeholder-disp")
	if err := os.MkdirAll(filepath.Dir(trPh), 0o755); err != nil {
		t.Fatalf("mkdir trPh: %v", err)
	}
	if err := os.WriteFile(trPh, []byte(`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"2026-11-25T23:45:00Z","model":"gpt-5.4","output_tokens":100}`+"\n"), 0o600); err != nil {
		t.Fatalf("write trPh: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	snap := prov.Snapshot()

	dist, ok := snap.FindConversation("conv-disp-distinct")
	if !ok {
		t.Fatal("conv-disp-distinct not found")
	}
	if dist.TreeMetrics.RequestCount != 2 || dist.TreeMetrics.TotalOutputTokens != 200 {
		t.Fatalf("conv-disp-distinct got RequestCount=%d Output=%d, want 2 / 200", dist.TreeMetrics.RequestCount, dist.TreeMetrics.TotalOutputTokens)
	}

	ph, ok := snap.FindConversation("conv-placeholder-disp")
	if !ok {
		t.Fatal("conv-placeholder-disp not found")
	}
	if ph.TreeMetrics.RequestCount != 1 || ph.TreeMetrics.TotalOutputTokens != 100 {
		t.Fatalf("conv-placeholder-disp got RequestCount=%d Output=%d, want 1 / 100", ph.TreeMetrics.RequestCount, ph.TreeMetrics.TotalOutputTokens)
	}
}

func TestMatchPlanners_LinearScaling512EstimatedGens(t *testing.T) {
	calc, err := metrics.NewDefaultCalculator()
	if err != nil {
		t.Fatalf("NewDefaultCalculator: %v", err)
	}
	const n = 512
	gens := make([]GenMetadata, n)
	planners := make([]TranscriptPlanner, n)
	for i := 0; i < n; i++ {
		out := int64(100 + i)
		gens[i] = GenMetadata{
			GenIndex:           i,
			StepIndex:          i,
			StepIndexEstimated: true,
			ModelSlug:          "claude-opus-5-5",
			OutputTokens:       out,
		}
		planners[i] = TranscriptPlanner{
			StepIndex:    i*2 + 1,
			Model:        "claude-opus-5-5",
			OutputTokens: out,
		}
	}
	tr := TranscriptData{Planners: planners}

	start := time.Now()
	res := tr.MatchPlanners(gens, "conv-scale", calc)
	elapsed := time.Since(start)
	if len(res.UnclaimedPlanners) != 0 {
		t.Fatalf("expected 0 unclaimed planners, got %d", len(res.UnclaimedPlanners))
	}
	for i := 0; i < n; i++ {
		if res.MatchedByGen[i] == nil || res.MatchedByGen[i].OutputTokens != int64(100+i) {
			t.Fatalf("gen %d matched wrong planner: %+v", i, res.MatchedByGen[i])
		}
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("MatchPlanners(%d) took %v, expected linear sub-250ms execution", n, elapsed)
	}
}

func TestTranscriptTailer_DensePlannerLogAppendIsStrictOOfDelta(t *testing.T) {
	trPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	now := time.Date(2026, 11, 26, 5, 0, 0, 0, time.UTC)

	// Build a ~4 MiB log consisting ENTIRELY of PLANNER_RESPONSE lines (~30,000 lines).
	var sb strings.Builder
	sb.Grow(4 * 1024 * 1024)
	const initialCount = 30000
	for step := 1; step <= initialCount; step++ {
		fmt.Fprintf(&sb, `{"step_index":%d,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T05:00:00Z","model":"gemini-4-neon","output_tokens":100}`+"\n", step)
	}
	if err := os.WriteFile(trPath, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write dense planner log: %v", err)
	}

	tailer := NewTranscriptTailer(trPath)
	changed, err := tailer.Sync()
	if err != nil || !changed {
		t.Fatalf("initial Sync: changed=%v err=%v", changed, err)
	}
	if got := len(tailer.Snapshot().Planners); got != initialCount {
		t.Fatalf("initial planners = %d, want %d", got, initialCount)
	}

	// Append a single line and verify Sync is strictly O(delta) (<25ms) even on 30,000 PLANNER_RESPONSE lines.
	f, err := os.OpenFile(trPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	appended := fmt.Sprintf(`{"step_index":%d,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","output_tokens":300}`+"\n", initialCount+1, now.Add(time.Minute).UTC().Format(time.RFC3339))
	if _, err := f.WriteString(appended); err != nil {
		_ = f.Close()
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	start := time.Now()
	changed, err = tailer.Sync()
	elapsed := time.Since(start)
	if err != nil || !changed {
		t.Fatalf("append Sync: changed=%v err=%v", changed, err)
	}
	if snap := tailer.Snapshot(); len(snap.Planners) != initialCount+1 || snap.Planners[initialCount].OutputTokens != 300 {
		t.Fatalf("post-append snapshot len=%d last=%+v", len(snap.Planners), snap.Planners[len(snap.Planners)-1])
	}
	if elapsed > 25*time.Millisecond {
		t.Fatalf("dense planner log append Sync took %v, expected strict O(delta) <25ms", elapsed)
	}

	// Full-file rewrite of a large log (changing head/tail boundaries) must still reset cleanly.
	rewritten := `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T05:05:00Z","model":"gemini-4-neon","output_tokens":999}` + "\n" +
		`{"step_index":2,"type":"GENERIC","content":"` + strings.Repeat("R", 5*1024*1024) + `"}` + "\n"
	if err := os.WriteFile(trPath, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("rewrite large log: %v", err)
	}
	changed, err = tailer.Sync()
	if err != nil || !changed {
		t.Fatalf("post-rewrite Sync: changed=%v err=%v", changed, err)
	}
	if snap := tailer.Snapshot(); len(snap.Planners) != 1 || snap.Planners[0].OutputTokens != 999 {
		t.Fatalf("post-rewrite planners = %+v, want [999]", snap.Planners)
	}
}

func TestProvider_CatalogDisplayAliasMatchesTranscriptModel(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 26, 1, 0, 0, 0, time.UTC)
	cases := []struct {
		id      string
		display string
		trModel string
	}{
		{"conv-orch-carbon", "Orchestrator (Carbon)", "gemini-pro-orchestrator"},
		{"conv-orch-worker", "Orchestrator Worker (Hydrogen)", "gemini-flash-worker-agy"},
	}

	usage100 := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100)
		return b
	})

	for _, tc := range cases {
		if _, err := sumDB.Exec(
			`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES (?, ?, ?, 'CASCADE_RUN_STATUS_IDLE')`,
			tc.id, tc.display, now.Format(time.RFC3339),
		); err != nil {
			t.Fatalf("insert summary %s: %v", tc.id, err)
		}

		db := createTestConvDB(t, rootDir, tc.id)
		inner := buildProtoMsg(func(b []byte) []byte {
			b = appendSubMsg(b, 4, usage100)
			b = appendStringField(b, 21, tc.display)
			return b
		})
		blob := buildProtoMsg(func(b []byte) []byte {
			b = appendSubMsg(b, 1, inner)
			return b
		})
		if _, err := db.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blob, len(blob)); err != nil {
			_ = db.Close()
			t.Fatalf("insert gen %s: %v", tc.id, err)
		}
		_ = db.Close()

		trPath := TranscriptPath(rootDir, tc.id)
		if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
			t.Fatalf("mkdir transcript %s: %v", tc.id, err)
		}
		line := fmt.Sprintf(`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T01:00:00Z","model":%q,"output_tokens":100}`+"\n", tc.trModel)
		if err := os.WriteFile(trPath, []byte(line), 0o600); err != nil {
			t.Fatalf("write transcript %s: %v", tc.id, err)
		}
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	snap := prov.Snapshot()
	for _, tc := range cases {
		conv, ok := snap.FindConversation(tc.id)
		if !ok {
			t.Fatalf("%s not found", tc.id)
		}
		if conv.TreeMetrics.RequestCount != 1 || conv.TreeMetrics.TotalOutputTokens != 100 {
			t.Fatalf("%s (%s <-> %s) got RequestCount=%d Output=%d, want 1 / 100", tc.id, tc.display, tc.trModel, conv.TreeMetrics.RequestCount, conv.TreeMetrics.TotalOutputTokens)
		}
	}
}

func TestProvider_LargeTranscriptAtomicReplaceMatchesFreshReload(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
	if _, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES ('conv-64-planners', '64 Planners Large Log', ?, 'CASCADE_RUN_STATUS_RUNNING')`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	trPath := TranscriptPath(rootDir, "conv-64-planners")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}

	buildLog := func(step10Out int64, includeStep65 bool) string {
		var sb strings.Builder
		pad := strings.Repeat("X", 9*1024)
		for step := 1; step <= 64; step++ {
			out := int64(100)
			if step == 10 {
				out = step10Out
			}
			ts := now.Add(time.Duration(step) * time.Second).UTC().Format(time.RFC3339)
			fmt.Fprintf(&sb, `{"step_index":%d,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","output_tokens":%d}`+"\n", step, ts, out)
			fmt.Fprintf(&sb, `{"step_index":%d,"type":"GENERIC","created_at":"%s","content":"%s"}`+"\n", step*100, ts, pad)
		}
		if includeStep65 {
			ts := now.Add(65 * time.Second).UTC().Format(time.RFC3339)
			fmt.Fprintf(&sb, `{"step_index":65,"type":"PLANNER_RESPONSE","created_at":"%s","model":"gemini-4-neon","output_tokens":100}`+"\n", ts)
		}
		return sb.String()
	}

	v1 := buildLog(100, false)
	if len(v1) <= 512*1024 {
		t.Fatalf("test setup error: v1 len (%d) must exceed 512 KiB", len(v1))
	}
	if err := os.WriteFile(trPath, []byte(v1), 0o600); err != nil {
		t.Fatalf("write v1: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	c1, ok := prov.Snapshot().FindConversation("conv-64-planners")
	if !ok || c1.TreeMetrics.RequestCount != 64 || c1.TreeMetrics.TotalOutputTokens != 6400 {
		t.Fatalf("initial got ok=%v count=%d output=%d, want 64 / 6400", ok, c1.TreeMetrics.RequestCount, c1.TreeMetrics.TotalOutputTokens)
	}

	// Atomically replace the >512 KiB file (step 10: 100 -> 200, plus step 65: +100).
	tmpPath := trPath + ".tmp"
	v2 := buildLog(200, true)
	if err := os.WriteFile(tmpPath, []byte(v2), 0o600); err != nil {
		t.Fatalf("write tmp v2: %v", err)
	}
	if err := os.Rename(tmpPath, trPath); err != nil {
		t.Fatalf("rename tmp v2: %v", err)
	}
	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	cLive, ok := prov.Snapshot().FindConversation("conv-64-planners")
	if !ok {
		t.Fatal("conv-64-planners not found after refresh")
	}

	fresh, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("fresh NewProvider: %v", err)
	}
	defer fresh.Close()

	cFresh, ok := fresh.Snapshot().FindConversation("conv-64-planners")
	if !ok {
		t.Fatal("conv-64-planners not found in fresh provider")
	}

	if cLive.TreeMetrics.RequestCount != 65 || cLive.TreeMetrics.TotalOutputTokens != 6600 {
		t.Fatalf("live refresh got count=%d output=%d, want 65 / 6600 (fresh=%d / %d)", cLive.TreeMetrics.RequestCount, cLive.TreeMetrics.TotalOutputTokens, cFresh.TreeMetrics.RequestCount, cFresh.TreeMetrics.TotalOutputTokens)
	}
	if cLive.TreeMetrics.TotalOutputTokens != cFresh.TreeMetrics.TotalOutputTokens {
		t.Fatalf("live output (%d) != fresh reload output (%d)", cLive.TreeMetrics.TotalOutputTokens, cFresh.TreeMetrics.TotalOutputTokens)
	}
}

func TestProvider_ClaudeHaikuDisplayAndOrchestratorWorkerPricing(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	for _, row := range []struct{ id, title string }{
		{"conv-haiku-disp", "Haiku Display"},
		{"conv-worker-pricing", "Worker Display Pricing"},
	} {
		if _, err := sumDB.Exec(
			`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES (?, ?, ?, 'CASCADE_RUN_STATUS_IDLE')`,
			row.id, row.title, now.Format(time.RFC3339),
		); err != nil {
			t.Fatalf("insert summary %s: %v", row.id, err)
		}
	}

	usage100 := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 100)
		return b
	})

	// 1. DB has display "Claude Haiku 5.5" (no slug, no step index), transcript has "claude-haiku-5-5-high" -> 1 request / 100 tokens.
	dbHaiku := createTestConvDB(t, rootDir, "conv-haiku-disp")
	innerHaiku := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage100)
		b = appendStringField(b, 21, "Claude Haiku 5.5")
		return b
	})
	blobHaiku := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, innerHaiku)
		return b
	})
	if _, err := dbHaiku.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blobHaiku, len(blobHaiku)); err != nil {
		_ = dbHaiku.Close()
		t.Fatalf("insert conv-haiku-disp: %v", err)
	}
	_ = dbHaiku.Close()

	trHaiku := TranscriptPath(rootDir, "conv-haiku-disp")
	if err := os.MkdirAll(filepath.Dir(trHaiku), 0o755); err != nil {
		t.Fatalf("mkdir trHaiku: %v", err)
	}
	if err := os.WriteFile(trHaiku, []byte(`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T03:00:00Z","model":"claude-haiku-5-5-high","output_tokens":100}`+"\n"), 0o600); err != nil {
		t.Fatalf("write trHaiku: %v", err)
	}

	// 2. DB has ONLY display "Orchestrator Worker (Hydrogen)" and 100 output tokens, NO transcript -> cost must be $0.000250.
	dbWorker := createTestConvDB(t, rootDir, "conv-worker-pricing")
	innerWorker := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage100)
		b = appendStringField(b, 21, "Orchestrator Worker (Hydrogen)")
		return b
	})
	blobWorker := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, innerWorker)
		return b
	})
	if _, err := dbWorker.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, ?)`, blobWorker, len(blobWorker)); err != nil {
		_ = dbWorker.Close()
		t.Fatalf("insert conv-worker-pricing: %v", err)
	}
	_ = dbWorker.Close()

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	snap := prov.Snapshot()

	haiku, ok := snap.FindConversation("conv-haiku-disp")
	if !ok {
		t.Fatal("conv-haiku-disp not found")
	}
	if haiku.TreeMetrics.RequestCount != 1 || haiku.TreeMetrics.TotalOutputTokens != 100 {
		t.Fatalf("conv-haiku-disp got RequestCount=%d Output=%d, want 1 / 100", haiku.TreeMetrics.RequestCount, haiku.TreeMetrics.TotalOutputTokens)
	}

	worker, ok := snap.FindConversation("conv-worker-pricing")
	if !ok {
		t.Fatal("conv-worker-pricing not found")
	}
	wantCost := 100 * 2.50 / 1e6
	if worker.TreeMetrics.RequestCount != 1 || math.Abs(worker.TreeMetrics.SessionCostUSD-wantCost) > 1e-12 {
		t.Fatalf("conv-worker-pricing got count=%d cost=%.6f, want 1 / %.6f", worker.TreeMetrics.RequestCount, worker.TreeMetrics.SessionCostUSD, wantCost)
	}
}

func TestProvider_TranscriptAliasPricingMatchesDisplayName(t *testing.T) {
	rootDir := t.TempDir()
	sumDB := createTestSummariesDB(t, rootDir)
	defer sumDB.Close()

	now := time.Date(2026, 11, 26, 5, 30, 0, 0, time.UTC)
	if _, err := sumDB.Exec(
		`INSERT INTO conversation_summaries(conversation_id, title, last_modified_time, status) VALUES ('conv-tr-alias', 'Transcript Alias Pricing', ?, 'CASCADE_RUN_STATUS_IDLE')`,
		now.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert summary: %v", err)
	}

	trPath := TranscriptPath(rootDir, "conv-tr-alias")
	if err := os.MkdirAll(filepath.Dir(trPath), 0o755); err != nil {
		t.Fatalf("mkdir transcript: %v", err)
	}
	trLine := `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T05:30:00Z","model":"Orchestrator Worker (Hydrogen)","output_tokens":100}` + "\n"
	if err := os.WriteFile(trPath, []byte(trLine), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	prov, err := NewProvider(Options{RootDir: rootDir, DisableWatcher: true})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer prov.Close()

	conv, ok := prov.Snapshot().FindConversation("conv-tr-alias")
	if !ok {
		t.Fatal("conv-tr-alias not found")
	}
	wantCost := 100 * 2.50 / 1e6
	if conv.TreeMetrics.RequestCount != 1 || math.Abs(conv.TreeMetrics.SessionCostUSD-wantCost) > 1e-12 {
		t.Fatalf("conv-tr-alias got count=%d cost=%.6f, want 1 / %.6f", conv.TreeMetrics.RequestCount, conv.TreeMetrics.SessionCostUSD, wantCost)
	}
}
