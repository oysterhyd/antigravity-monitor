package e2e_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/source"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/desktop"
	"google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite"
)

// anchorNow is a deterministic reference clock (> 1,500,000,000 Unix seconds)
// used across baseline and E2E fixtures.
var anchorNow = time.Date(2026, 11, 24, 15, 30, 0, 0, time.UTC)

type summarySpec struct {
	ConversationID         string
	Title                  string
	Preview                string
	StepCount              int
	LastModifiedTime       time.Time
	WorkspaceURIs          string
	Status                 string
	AgentName              string
	ParentConversationID   string
	NestingDepth           int
	NotFullyIdle           int
	Killed                 int
	LastUserInputTime      time.Time
	LastUserInputStepIndex int
}

type genSpec struct {
	GenIndex         int
	StepIndices      []int
	LastStepIndex    int
	RequestID        string
	CascadeID        string
	ParentCascadeID  string
	RootCascadeID    string
	SubagentType     string
	Error            string
	ModelSlug        string
	ModelDisplay     string
	InputTokens      int64
	OutputTokens     int64
	CacheWriteTokens int64
	CacheReadTokens  int64
	ThinkingTokens   int64
	ResponseTokens   int64
	ContextUsed      int64
	ContextLimit     int64
	TTFT             time.Duration
	DecodeDuration   time.Duration
	Timestamp        time.Time
	ExtraLargeBlob   bool
	StepType         int
}

type antigravityFixture struct {
	RootDir     string
	SummariesDB *sql.DB
	ConvDBs     map[string]*sql.DB
}

func newAntigravityFixture(t *testing.T) *antigravityFixture {
	t.Helper()
	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "conversations"), 0o755); err != nil {
		t.Fatalf("mkdir conversations: %v", err)
	}

	sumPath := filepath.Join(rootDir, "conversation_summaries.db")
	sumDB, err := sql.Open("sqlite", sumPath)
	if err != nil {
		t.Fatalf("open summaries db: %v", err)
	}
	t.Cleanup(func() {
		_ = sumDB.Close()
	})

	schema := `
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=NORMAL;
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
	if _, err := sumDB.Exec(schema); err != nil {
		t.Fatalf("create summaries schema: %v", err)
	}

	return &antigravityFixture{
		RootDir:     rootDir,
		SummariesDB: sumDB,
		ConvDBs:     make(map[string]*sql.DB),
	}
}

func (f *antigravityFixture) InsertSummary(t *testing.T, s summarySpec) {
	t.Helper()
	modStr := ""
	if !s.LastModifiedTime.IsZero() {
		modStr = s.LastModifiedTime.UTC().Format("2006-01-02 15:04:05+00:00")
	}
	userStr := ""
	if !s.LastUserInputTime.IsZero() {
		userStr = s.LastUserInputTime.UTC().Format("2006-01-02 15:04:05+00:00")
	}
	_, err := f.SummariesDB.Exec(
		`INSERT OR REPLACE INTO conversation_summaries(
			conversation_id, title, preview, step_count, last_modified_time,
			workspace_uris, status, agent_name, parent_conversation_id,
			nesting_depth, not_fully_idle, killed, last_user_input_time, last_user_input_step_index
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ConversationID, s.Title, s.Preview, s.StepCount, modStr,
		s.WorkspaceURIs, s.Status, s.AgentName, s.ParentConversationID,
		s.NestingDepth, s.NotFullyIdle, s.Killed, userStr, s.LastUserInputStepIndex,
	)
	if err != nil {
		t.Fatalf("insert summary %s: %v", s.ConversationID, err)
	}
}

func (f *antigravityFixture) InsertParentLink(t *testing.T, childID, parentID string) {
	t.Helper()
	if _, err := f.SummariesDB.Exec(
		`INSERT OR REPLACE INTO conversation_parents(conversation_id, parent_id) VALUES(?, ?)`,
		childID, parentID,
	); err != nil {
		t.Fatalf("insert parent link %s -> %s: %v", childID, parentID, err)
	}
}

func (f *antigravityFixture) OpenConvDB(t *testing.T, convID string) *sql.DB {
	t.Helper()
	if db, ok := f.ConvDBs[convID]; ok {
		return db
	}
	dbPath := filepath.Join(f.RootDir, "conversations", convID+".db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open conv db %s: %v", convID, err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	schema := `
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=NORMAL;
		PRAGMA wal_autocheckpoint=0;
		CREATE TABLE IF NOT EXISTS gen_metadata (
			idx integer PRIMARY KEY,
			data blob,
			size integer NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS steps (
			idx integer PRIMARY KEY,
			step_type integer NOT NULL DEFAULT 0,
			status integer NOT NULL DEFAULT 0,
			has_subtrajectory numeric NOT NULL DEFAULT 0,
			metadata blob
		);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create conv schema %s: %v", convID, err)
	}
	f.ConvDBs[convID] = db
	return db
}

func (f *antigravityFixture) AppendWALGenRow(t *testing.T, convID string, g genSpec) {
	t.Helper()
	db := f.OpenConvDB(t, convID)
	blob := encodeGenMetadataBlob(g)
	if _, err := db.Exec(
		`INSERT OR REPLACE INTO gen_metadata(idx, data, size) VALUES(?, ?, ?)`,
		g.GenIndex, blob, len(blob),
	); err != nil {
		t.Fatalf("insert gen_metadata %s[%d]: %v", convID, g.GenIndex, err)
	}

	if !g.Timestamp.IsZero() {
		stepIdx := g.GenIndex
		if len(g.StepIndices) > 0 {
			stepIdx = g.StepIndices[0]
		} else if g.LastStepIndex >= 0 {
			stepIdx = g.LastStepIndex + 1
		}
		stepType := g.StepType
		if stepType == 0 {
			stepType = 15
		}
		stepMeta := encodeStepMetadataTimestamp(g.Timestamp)
		if _, err := db.Exec(
			`INSERT OR REPLACE INTO steps(idx, step_type, metadata) VALUES(?, ?, ?)`,
			stepIdx, stepType, stepMeta,
		); err != nil {
			t.Fatalf("insert step %s[%d]: %v", convID, stepIdx, err)
		}
	}
}

func (f *antigravityFixture) AppendRawGenRow(t *testing.T, convID string, idx int, data []byte) {
	t.Helper()
	db := f.OpenConvDB(t, convID)
	if _, err := db.Exec(
		`INSERT OR REPLACE INTO gen_metadata(idx, data, size) VALUES(?, ?, ?)`,
		idx, data, len(data),
	); err != nil {
		t.Fatalf("insert raw gen_metadata %s[%d]: %v", convID, idx, err)
	}
}

func (f *antigravityFixture) Checkpoint(t *testing.T, convID string) {
	t.Helper()
	db := f.OpenConvDB(t, convID)
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		t.Fatalf("checkpoint %s: %v", convID, err)
	}
}

func (f *antigravityFixture) CheckpointSummaries(t *testing.T) {
	t.Helper()
	if _, err := f.SummariesDB.Exec(`PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		t.Fatalf("checkpoint summaries: %v", err)
	}
}

func (f *antigravityFixture) WriteTranscript(t *testing.T, convID, content string) {
	t.Helper()
	p := source.TranscriptPath(f.RootDir, convID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write transcript %s: %v", convID, err)
	}
}

func encodeGenMetadataBlob(g genSpec) []byte {
	var packedSteps []byte
	for _, s := range g.StepIndices {
		packedSteps = protowire.AppendVarint(packedSteps, uint64(s))
	}

	usage := buildProto(func(b []byte) []byte {
		if g.InputTokens > 0 {
			b = appendVarint(b, 2, uint64(g.InputTokens))
		}
		if g.OutputTokens > 0 {
			b = appendVarint(b, 3, uint64(g.OutputTokens))
		}
		if g.CacheWriteTokens > 0 {
			b = appendVarint(b, 4, uint64(g.CacheWriteTokens))
		}
		if g.CacheReadTokens > 0 {
			b = appendVarint(b, 5, uint64(g.CacheReadTokens))
		}
		if g.ThinkingTokens > 0 {
			b = appendVarint(b, 9, uint64(g.ThinkingTokens))
		}
		if g.ResponseTokens > 0 {
			b = appendVarint(b, 10, uint64(g.ResponseTokens))
		}
		return b
	})

	ctxWin := buildProto(func(b []byte) []byte {
		if g.ContextUsed > 0 {
			b = appendVarint(b, 1, uint64(g.ContextUsed))
		}
		if g.ContextLimit > 0 {
			b = appendVarint(b, 4, uint64(g.ContextLimit))
		}
		return b
	})

	inner := buildProto(func(b []byte) []byte {
		if len(usage) > 0 {
			b = appendBytesField(b, 4, usage)
		}
		if len(ctxWin) > 0 {
			b = appendBytesField(b, 9, appendBytesField(nil, 10, ctxWin))
		}
		if g.TTFT > 0 {
			b = appendDuration(b, 11, g.TTFT)
		}
		if g.DecodeDuration > 0 {
			b = appendDuration(b, 12, g.DecodeDuration)
		}
		if g.ModelSlug != "" {
			b = appendString(b, 19, g.ModelSlug)
		}
		if g.LastStepIndex >= 0 {
			b = appendKVField(b, 20, "last_step_index", fmt.Sprintf("%d", g.LastStepIndex))
		}
		if g.RequestID != "" {
			b = appendKVField(b, 20, "request_id", g.RequestID)
		}
		if g.CascadeID != "" {
			b = appendKVField(b, 20, "cascade_id", g.CascadeID)
		}
		if g.ParentCascadeID != "" {
			b = appendKVField(b, 20, "parent_cascade_id", g.ParentCascadeID)
		}
		if g.RootCascadeID != "" {
			b = appendKVField(b, 20, "root_cascade_id", g.RootCascadeID)
		}
		if g.SubagentType != "" {
			b = appendKVField(b, 20, "subagent_type", g.SubagentType)
		}
		if g.ModelDisplay != "" {
			b = appendString(b, 21, g.ModelDisplay)
		}
		if g.ExtraLargeBlob {
			b = appendBytesField(b, 99, bytes.Repeat([]byte("X"), 6144))
		}
		return b
	})

	return buildProto(func(b []byte) []byte {
		if len(packedSteps) > 0 {
			b = appendBytesField(b, 2, packedSteps)
		}
		if g.Error != "" {
			b = appendString(b, 5, g.Error)
		}
		if g.ExtraLargeBlob {
			b = appendBytesField(b, 8, bytes.Repeat([]byte{0xAB}, 6144))
		}
		if len(inner) > 0 {
			b = appendBytesField(b, 1, inner)
		}
		return b
	})
}

func encodeStepMetadataTimestamp(ts time.Time) []byte {
	tsMsg := buildProto(func(b []byte) []byte {
		b = appendVarint(b, 1, uint64(ts.UTC().Unix()))
		b = appendVarint(b, 2, uint64(ts.UTC().Nanosecond()))
		return b
	})
	return appendBytesField(nil, 1, tsMsg)
}

func buildProto(fn func(b []byte) []byte) []byte {
	return fn(nil)
}

func appendBytesField(b []byte, num protowire.Number, payload []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, payload)
}

func appendVarint(b []byte, num protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func appendString(b []byte, num protowire.Number, s string) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendDuration(b []byte, num protowire.Number, d time.Duration) []byte {
	secs := int64(d / time.Second)
	nanos := int64(d % time.Second)
	var sub []byte
	if secs > 0 {
		sub = appendVarint(sub, 1, uint64(secs))
	}
	if nanos > 0 {
		sub = appendVarint(sub, 2, uint64(nanos))
	}
	return appendBytesField(b, num, sub)
}

func appendKVField(b []byte, num protowire.Number, k, v string) []byte {
	var entry []byte
	entry = appendString(entry, 1, k)
	entry = appendString(entry, 2, v)
	return appendBytesField(b, num, entry)
}

// seedBaselineMultiRangeFixture populates a realistic multi-day/multi-model fixture
// covering WAL-active today rows, >200k long context, orphan subagents in
// conversation_parents, unlisted DBs, IsEmptyFailure() rows, long CJK/ASCII titles,
// 7D, 30D, and Multi-Month ranges.
func seedBaselineMultiRangeFixture(t *testing.T) *antigravityFixture {
	t.Helper()
	fx := newAntigravityFixture(t)

	// 1. Today active WAL-mode root conversation
	fx.InsertSummary(t, summarySpec{
		ConversationID:         "conv-today-active",
		Title:                  "实时架构与 WAL 监控联调",
		StepCount:              14,
		LastModifiedTime:       anchorNow.Add(-15 * time.Minute),
		WorkspaceURIs:          `["file:///d%3A/antigravity_monitor"]`,
		Status:                 "CASCADE_RUN_STATUS_RUNNING",
		NotFullyIdle:           1,
		LastUserInputTime:      anchorNow.Add(-50 * time.Minute),
		LastUserInputStepIndex: 0,
	})
	fx.AppendWALGenRow(t, "conv-today-active", genSpec{
		GenIndex:        0,
		StepIndices:     []int{1},
		LastStepIndex:   0,
		ModelSlug:       "gemini-4-neon",
		ModelDisplay:    "Gemini 4 Neon (High)",
		InputTokens:     12000,
		CacheReadTokens: 233000,
		OutputTokens:    2400,
		ThinkingTokens:  900,
		ResponseTokens:  1500,
		ContextUsed:     247400, // > 200k long-context tier
		ContextLimit:    512000,
		TTFT:            1200 * time.Millisecond,
		DecodeDuration:  4000 * time.Millisecond,
		Timestamp:       anchorNow.Add(-45 * time.Minute),
		ExtraLargeBlob:  true,
	})
	fx.AppendWALGenRow(t, "conv-today-active", genSpec{
		GenIndex:         1,
		StepIndices:      []int{2},
		LastStepIndex:    1,
		ModelSlug:        "claude-sonnet-4",
		ModelDisplay:     "Claude 4.5 Sonnet",
		InputTokens:      8000,
		CacheReadTokens:  42000,
		CacheWriteTokens: 3000,
		OutputTokens:     1100,
		ContextUsed:      54100,
		ContextLimit:     200000,
		TTFT:             900 * time.Millisecond,
		DecodeDuration:   2200 * time.Millisecond,
		Timestamp:        anchorNow.Add(-20 * time.Minute),
	})
	// Empty failure row must be ignored without distorting metrics
	fx.AppendWALGenRow(t, "conv-today-active", genSpec{
		GenIndex:      2,
		StepIndices:   []int{3},
		LastStepIndex: 2,
		Error:         "context canceled by user",
		ModelDisplay:  "Gemini 4 Neon (High)",
		Timestamp:     anchorNow.Add(-16 * time.Minute),
	})

	// 2. Orphan subagent: present in conversation_parents and conversations/*.db, absent from conversation_summaries
	fx.InsertParentLink(t, "orphan-sub-1", "conv-today-active")
	fx.AppendWALGenRow(t, "orphan-sub-1", genSpec{
		GenIndex:        0,
		StepIndices:     []int{1},
		LastStepIndex:   0,
		SubagentType:    "research",
		RootCascadeID:   "conv-today-active",
		ParentCascadeID: "conv-today-active",
		ModelSlug:       "gemini-4-flash",
		ModelDisplay:    "Gemini 4 Flash",
		InputTokens:     3500,
		CacheReadTokens: 21500,
		OutputTokens:    750,
		ContextUsed:     25750,
		ContextLimit:    512000,
		TTFT:            500 * time.Millisecond,
		DecodeDuration:  1200 * time.Millisecond,
		Timestamp:       anchorNow.Add(-30 * time.Minute),
	})

	// 3. Unlisted DB: neither in conversation_summaries nor conversation_parents
	fx.AppendWALGenRow(t, "unlisted-gpt-1", genSpec{
		GenIndex:        0,
		StepIndices:     []int{1},
		LastStepIndex:   0,
		ModelSlug:       "gpt-5",
		ModelDisplay:    "GPT-5.2 / Codex",
		InputTokens:     6000,
		CacheReadTokens: 18000,
		OutputTokens:    900,
		ContextUsed:     24900,
		ContextLimit:    400000,
		TTFT:            800 * time.Millisecond,
		DecodeDuration:  1500 * time.Millisecond,
		Timestamp:       anchorNow.Add(-3 * time.Hour),
	})

	// 4. 7D conversation with long 140+ char CJK/ASCII title and slug-vs-display pool check
	longTitle := "MultiAgentTelemetry-RefactorPipeline-Phase3-Verification-" +
		"超长会话标题边界截断验证包含中英文混合字符以及长路径检查-" +
		"EndToEndTruncationCheck-0123456789"
	fx.InsertSummary(t, summarySpec{
		ConversationID:         "conv-7d-claude",
		Title:                  longTitle,
		StepCount:              10,
		LastModifiedTime:       anchorNow.Add(-48 * time.Hour),
		WorkspaceURIs:          `["file:///d%3A/work/core-api"]`,
		Status:                 "CASCADE_RUN_STATUS_IDLE",
		LastUserInputTime:      anchorNow.Add(-5 * 24 * time.Hour),
		LastUserInputStepIndex: 0,
	})
	fx.AppendWALGenRow(t, "conv-7d-claude", genSpec{
		GenIndex:         0,
		StepIndices:      []int{1},
		LastStepIndex:    0,
		ModelSlug:        "claude-opus-5-5-high",
		ModelDisplay:     "Claude Opus 5.5 (High)",
		InputTokens:      15000,
		CacheReadTokens:  85000,
		CacheWriteTokens: 5000,
		OutputTokens:     3200,
		ContextUsed:      108200,
		ContextLimit:     200000,
		TTFT:             1800 * time.Millisecond,
		DecodeDuration:   6000 * time.Millisecond,
		Timestamp:        anchorNow.Add(-5 * 24 * time.Hour),
	})
	fx.AppendWALGenRow(t, "conv-7d-claude", genSpec{
		GenIndex:        1,
		StepIndices:     []int{2},
		LastStepIndex:   1,
		ModelSlug:       "gemini-pro-orchestrator",
		ModelDisplay:    "Orchestrator (Carbon)",
		InputTokens:     4000,
		CacheReadTokens: 36000,
		OutputTokens:    800,
		ContextUsed:     40800,
		ContextLimit:    512000,
		TTFT:            1000 * time.Millisecond,
		DecodeDuration:  2000 * time.Millisecond,
		Timestamp:       anchorNow.Add(-48 * time.Hour),
	})

	// 5. 30D conversation
	fx.InsertSummary(t, summarySpec{
		ConversationID:   "conv-30d-mixed",
		Title:            "30-Day Indexer & SQLite Migration",
		StepCount:        6,
		LastModifiedTime: anchorNow.Add(-14 * 24 * time.Hour),
		WorkspaceURIs:    `["file:///d%3A/work/indexer"]`,
		Status:           "CASCADE_RUN_STATUS_IDLE",
	})
	fx.AppendWALGenRow(t, "conv-30d-mixed", genSpec{
		GenIndex:        0,
		StepIndices:     []int{1},
		LastStepIndex:   0,
		ModelSlug:       "gemini-4-hydrogen",
		ModelDisplay:    "Gemini 4 Hydrogen",
		InputTokens:     25000,
		CacheReadTokens: 75000,
		OutputTokens:    1600,
		ContextUsed:     101600,
		ContextLimit:    512000,
		TTFT:            1100 * time.Millisecond,
		DecodeDuration:  3000 * time.Millisecond,
		Timestamp:       anchorNow.Add(-25 * 24 * time.Hour),
	})
	fx.AppendWALGenRow(t, "conv-30d-mixed", genSpec{
		GenIndex:        1,
		StepIndices:     []int{2},
		LastStepIndex:   1,
		ModelSlug:       "gpt-5",
		ModelDisplay:    "GPT-5.2 / Codex",
		InputTokens:     9000,
		CacheReadTokens: 31000,
		OutputTokens:    1200,
		ContextUsed:     41200,
		ContextLimit:    400000,
		TTFT:            950 * time.Millisecond,
		DecodeDuration:  2400 * time.Millisecond,
		Timestamp:       anchorNow.Add(-14 * 24 * time.Hour),
	})

	// 6. Multi-month conversation (> 30d old)
	fx.InsertSummary(t, summarySpec{
		ConversationID:   "conv-multimonth-1",
		Title:            "Historical Bootstrap Session",
		StepCount:        4,
		LastModifiedTime: anchorNow.Add(-48 * 24 * time.Hour),
		WorkspaceURIs:    `["file:///d%3A/work/legacy"]`,
		Status:           "CASCADE_RUN_STATUS_IDLE",
	})
	fx.AppendWALGenRow(t, "conv-multimonth-1", genSpec{
		GenIndex:        0,
		StepIndices:     []int{1},
		LastStepIndex:   0,
		ModelSlug:       "gemini-4-neon",
		ModelDisplay:    "Gemini 4 Neon (High)",
		InputTokens:     7000,
		CacheReadTokens: 28000,
		OutputTokens:    900,
		ContextUsed:     35900,
		ContextLimit:    512000,
		TTFT:            1000 * time.Millisecond,
		DecodeDuration:  2000 * time.Millisecond,
		Timestamp:       anchorNow.Add(-95 * 24 * time.Hour),
	})
	fx.AppendWALGenRow(t, "conv-multimonth-1", genSpec{
		GenIndex:        1,
		StepIndices:     []int{2},
		LastStepIndex:   1,
		ModelSlug:       "claude-opus-4",
		ModelDisplay:    "Claude 4 Opus",
		InputTokens:     11000,
		CacheReadTokens: 44000,
		OutputTokens:    1500,
		ContextUsed:     56500,
		ContextLimit:    200000,
		TTFT:            1300 * time.Millisecond,
		DecodeDuration:  3500 * time.Millisecond,
		Timestamp:       anchorNow.Add(-48 * 24 * time.Hour),
	})

	return fx
}

func newHeadlessTester(app *desktop.App) *ui.Tester {
	tt := ui.NewTester(app.View, 900, 590)
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	return tt
}

func stepUI(tt *ui.Tester, d time.Duration) {
	tt.Frame()
	if d > 0 {
		tt.Advance(d)
	}
}

func mustHaveText(t *testing.T, tt *ui.Tester, want string) {
	t.Helper()
	if !tt.HasText(want) {
		t.Fatalf("expected text %q in view; got texts=%q", want, tt.Texts())
	}
}

func mustNotHaveText(t *testing.T, tt *ui.Tester, unwanted string) {
	t.Helper()
	if tt.HasText(unwanted) {
		t.Fatalf("did not expect text %q in view; got texts=%q", unwanted, tt.Texts())
	}
}

func verifyPNGFrame(t *testing.T, tt *ui.Tester, previewName string) {
	t.Helper()
	img := tt.Image()
	if img == nil {
		t.Fatalf("nil rendered frame for %s", previewName)
	}
	b := img.Bounds()
	if b.Dx() != 900 || b.Dy() != 590 {
		t.Fatalf("unexpected frame size for %s: got %dx%d, want 900x590", previewName, b.Dx(), b.Dy())
	}

	// Verify meaningful RGBA pixel variance (not blank/flat single color).
	first := img.RGBAAt(b.Min.X, b.Min.Y)
	diffCount := 0
	for y := b.Min.Y; y < b.Max.Y; y += 6 {
		for x := b.Min.X; x < b.Max.X; x += 6 {
			px := img.RGBAAt(x, y)
			if px != first {
				diffCount++
			}
		}
	}
	if diffCount < 100 {
		t.Fatalf("frame %s appears blank or flat (only %d sampled pixels differ from origin %+v)", previewName, diffCount, first)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode PNG %s: %v", previewName, err)
	}
	if buf.Len() < 1024 {
		t.Fatalf("PNG output suspiciously small for %s: %d bytes", previewName, buf.Len())
	}
	if _, err := png.Decode(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("roundtrip PNG decode failed for %s: %v", previewName, err)
	}

	if dir := os.Getenv("E2E_UI_PREVIEW_DIR"); dir != "" && previewName != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir E2E_UI_PREVIEW_DIR: %v", err)
		}
		outPath := filepath.Join(dir, previewName)
		if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("write PNG preview %s: %v", outPath, err)
		}
	}
}

var _ = image.Rect(0, 0, 1, 1)

type staticSnapshotProvider struct {
	snap model.AppSnapshot
}

func (s *staticSnapshotProvider) Snapshot() model.AppSnapshot { return s.snap }
func (s *staticSnapshotProvider) Subscribe() (<-chan model.AppSnapshot, func()) {
	ch := make(chan model.AppSnapshot)
	return ch, func() { close(ch) }
}
func (s *staticSnapshotProvider) Close() error { return nil }

func sha256File(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s for sha256: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod from %s", dir)
		}
		dir = parent
	}
}
