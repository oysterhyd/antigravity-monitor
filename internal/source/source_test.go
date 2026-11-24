package source

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

	"github.com/oysterhyd/antigravity-monitor/internal/model"
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
		`{"step_index":1,"type":"PLANNER_RESPONSE","input_tokens":100`
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
