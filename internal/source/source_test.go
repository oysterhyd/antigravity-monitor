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
