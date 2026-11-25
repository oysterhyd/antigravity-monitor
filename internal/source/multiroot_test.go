package source

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func createACPTestSummariesDB(t *testing.T, rootDir string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("mkdir rootDir: %v", err)
	}
	dbPath := filepath.Join(rootDir, "conversation_summaries.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open acp summaries db: %v", err)
	}
	// Matches T3 Code antigravity-acp schema: no conversation_parents table.
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
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create acp summaries schema: %v", err)
	}
	return db
}

func TestDiscoverRootDirsFromHome(t *testing.T) {
	home := t.TempDir()
	nativeRoot := filepath.Join(home, ".gemini", "antigravity")
	cliRoot := filepath.Join(home, ".gemini", "antigravity-cli")
	acpRootA := filepath.Join(home, ".t3", "userdata", "providers", "antigravity", "aaa111", "antigravity-acp")
	acpRootB := filepath.Join(home, ".t3", "userdata", "providers", "antigravity", "bbb222", "antigravity-acp")
	emptyProvider := filepath.Join(home, ".t3", "userdata", "providers", "antigravity", "ccc333", "config")

	for _, dir := range []string{
		filepath.Join(cliRoot, "conversations"),
		acpRootA,
		acpRootB,
		emptyProvider,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	got := discoverRootDirsFromHome(home)
	want := []string{nativeRoot, cliRoot, acpRootA, acpRootB}
	if len(got) != len(want) {
		t.Fatalf("discoverRootDirsFromHome = %v, want %v", got, want)
	}
	for i := range want {
		if normalizeRootKey(got[i]) != normalizeRootKey(want[i]) {
			t.Fatalf("root[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSummariesReaderCachesWithoutConversationParentsTable(t *testing.T) {
	acpRoot := t.TempDir()
	db := createACPTestSummariesDB(t, acpRoot)
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.Exec(`
		INSERT INTO conversation_summaries (conversation_id, title, step_count, last_modified_time, status)
		VALUES (?, ?, ?, ?, ?)
	`, "conv-acp-1", "ACP Session", 10, now.Format(time.RFC3339Nano), "CASCADE_RUN_STATUS_IDLE"); err != nil {
		t.Fatalf("insert acp summary: %v", err)
	}
	defer db.Close()

	reader := NewSummariesReader(filepath.Join(acpRoot, "conversation_summaries.db"))
	defer reader.Close()

	rows, changed, err := reader.readChanges(context.Background())
	if err != nil {
		t.Fatalf("first readChanges: %v", err)
	}
	if !changed || len(rows) != 1 {
		t.Fatalf("first readChanges = (len=%d, changed=%v), want (1, true)", len(rows), changed)
	}

	// Second read without DB mutations must hit PRAGMA data_version cache even without conversation_parents table.
	_, changed2, err := reader.readChanges(context.Background())
	if err != nil {
		t.Fatalf("second readChanges: %v", err)
	}
	if changed2 {
		t.Fatalf("expected second readChanges to hit data_version cache (changed=false)")
	}
}

func TestProviderMultiRootMergeCloudStubAndFilterT3Helper(t *testing.T) {
	nativeRoot := t.TempDir()
	acpRoot := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	// Native root has conv-native with real local L2 DB.
	nativeSum := createTestSummariesDB(t, nativeRoot)
	if _, err := nativeSum.Exec(`
		INSERT INTO conversation_summaries (conversation_id, title, step_count, last_modified_time, workspace_uris, status, not_fully_idle)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "conv-native", "Native Session", 20, now.Add(-10*time.Minute).Format(time.RFC3339Nano), `["file:///D:/repo-native"]`, "CASCADE_RUN_STATUS_IDLE", 0); err != nil {
		t.Fatalf("insert native summary: %v", err)
	}
	_ = nativeSum.Close()

	nativeConv := createTestConvDB(t, nativeRoot, "conv-native")
	insertGenRow(t, nativeConv, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 1200, 8000, 0, 400, 9600, 512000, 500*time.Millisecond, 2*time.Second, now.Add(-10*time.Minute), "")
	_ = nativeConv.Close()

	// ACP root (without conversation_parents table) has:
	// 1) conv-acp (running, local DB)
	// 2) conv-native (cloud-synced summary stub ONLY — no local DB, even with a newer summary timestamp!)
	// 3) conv-helper (T3 internal helper workspace t3-antigravity-text-*, should be hidden in Live)
	acpSum := createACPTestSummariesDB(t, acpRoot)
	if _, err := acpSum.Exec(`
		INSERT INTO conversation_summaries (conversation_id, title, step_count, last_modified_time, workspace_uris, status, not_fully_idle)
		VALUES
			(?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?)
	`,
		"conv-acp", "T3 ACP Session", 34, now.Format(time.RFC3339Nano), `["file:///D:/antigravity_monitor"]`, "CASCADE_RUN_STATUS_RUNNING", 1,
		"conv-native", "Native Session", 20, now.Add(-5*time.Minute).Format(time.RFC3339Nano), `["file:///D:/repo-native"]`, "", 0,
		"conv-helper", "{", 2, now.Add(-1*time.Minute).Format(time.RFC3339Nano), `["file:///C:/Users/oyster/AppData/Local/Temp/t3-antigravity-text-MCjXXp"]`, "CASCADE_RUN_STATUS_IDLE", 0,
	); err != nil {
		t.Fatalf("insert acp summaries: %v", err)
	}
	_ = acpSum.Close()

	acpConv := createTestConvDB(t, acpRoot, "conv-acp")
	insertGenRow(t, acpConv, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 2200, 120000, 0, 1100, 123300, 512000, 800*time.Millisecond, 3*time.Second, now, "")
	_ = acpConv.Close()

	helperConv := createTestConvDB(t, acpRoot, "conv-helper")
	insertGenRow(t, helperConv, 0, 1, "gemini-4-neon", "Gemini 4 Neon (High)", 100, 0, 0, 20, 120, 512000, 200*time.Millisecond, 500*time.Millisecond, now.Add(-1*time.Minute), "")
	_ = helperConv.Close()

	p, err := NewProvider(Options{
		RootDirs:       []string{nativeRoot, acpRoot},
		DisableWatcher: true,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer p.Close()

	snap := p.Snapshot()
	if len(snap.Conversations) != 2 {
		t.Fatalf("len(Conversations) = %d, want 2 (helper filtered, duplicate merged): %+v", len(snap.Conversations), snap.Conversations)
	}
	if snap.RunningCount != 1 {
		t.Fatalf("RunningCount = %d, want 1", snap.RunningCount)
	}
	if snap.Conversations[0].ID != "conv-acp" || !snap.Conversations[0].Running {
		t.Fatalf("top conversation = (%s, running=%v), want (conv-acp, true)", snap.Conversations[0].ID, snap.Conversations[0].Running)
	}
	if snap.Conversations[0].TreeMetrics.RequestCount != 1 || snap.Conversations[0].Root.ContextUsed != 123300 {
		t.Fatalf("conv-acp telemetry missing: reqs=%d ctx=%d", snap.Conversations[0].TreeMetrics.RequestCount, snap.Conversations[0].Root.ContextUsed)
	}

	// Verify conv-native routed to nativeRoot and hydrated its L2 metrics despite cloud stub in acpRoot.
	nativeSnap, ok := snap.FindConversation("conv-native")
	if !ok || nativeSnap.ID != "conv-native" {
		t.Fatalf("conv-native not found in merged snapshot")
	}
	if nativeSnap.TreeMetrics.RequestCount != 1 || nativeSnap.Root.ContextUsed != 9600 {
		t.Fatalf("conv-native lost L2 telemetry due to cloud stub: reqs=%d ctx=%d", nativeSnap.TreeMetrics.RequestCount, nativeSnap.Root.ContextUsed)
	}
}

func TestProviderDynamicRootDiscovery(t *testing.T) {
	nativeRoot := t.TempDir()
	acpRoot := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	roots := []string{nativeRoot}
	p, err := NewProvider(Options{
		DiscoverRoots: func() []string {
			cp := make([]string, len(roots))
			copy(cp, roots)
			return cp
		},
		DisableWatcher: true,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer p.Close()

	if len(p.Snapshot().Conversations) != 0 {
		t.Fatalf("expected 0 conversations initially, got %d", len(p.Snapshot().Conversations))
	}

	// Simulate T3 Code creating a new provider hash directory after Monitor started.
	acpSum := createACPTestSummariesDB(t, acpRoot)
	if _, err := acpSum.Exec(`
		INSERT INTO conversation_summaries (conversation_id, title, step_count, last_modified_time, workspace_uris, status, not_fully_idle)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "conv-new-acp", "Newly Discovered ACP", 4, now.Format(time.RFC3339Nano), `["file:///D:/antigravity_monitor"]`, "CASCADE_RUN_STATUS_RUNNING", 1); err != nil {
		t.Fatalf("insert new acp summary: %v", err)
	}
	_ = acpSum.Close()

	roots = []string{nativeRoot, acpRoot}
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after new root: %v", err)
	}

	snap := p.Snapshot()
	if len(snap.Conversations) != 1 || snap.Conversations[0].ID != "conv-new-acp" {
		t.Fatalf("expected newly discovered conv-new-acp, got %+v", snap.Conversations)
	}
}
