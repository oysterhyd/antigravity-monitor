package source

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SummaryRow represents a single conversation entry from L1 conversation_summaries.db.
type SummaryRow struct {
	ConversationID         string
	Title                  string
	Preview                string
	StepCount              int
	LastModifiedTime       time.Time
	WorkspaceURIs          []string
	Workspace              string
	Status                 string
	AgentName              string
	ParentConversationID   string
	NestingDepth           int
	NotFullyIdle           bool
	Killed                 bool
	LastUserInputTime      time.Time
	LastUserInputStepIndex int
}

// IsRunning reports whether this conversation is actively running (and not killed).
func (s SummaryRow) IsRunning() bool {
	if s.Killed {
		return false
	}
	return s.NotFullyIdle || strings.EqualFold(s.Status, "CASCADE_RUN_STATUS_RUNNING")
}

// DisplayTitle returns the best non-empty title for UI presentation.
func (s SummaryRow) DisplayTitle() string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	if p := strings.TrimSpace(s.Preview); p != "" {
		return p
	}
	if s.Workspace != "" {
		return filepath.Base(s.Workspace)
	}
	if len(s.ConversationID) >= 8 {
		return s.ConversationID[:8]
	}
	return s.ConversationID
}

// ConversationTree groups a top-level root conversation with all of its descendant subagents.
type ConversationTree struct {
	Root     SummaryRow
	Children []SummaryRow
}

// SummariesReader maintains a single read-only WAL handle to conversation_summaries.db.
type SummariesReader struct {
	mu           sync.Mutex
	dbPath       string
	db           *sql.DB
	version      int64
	versionKnown bool
}

// NewSummariesReader creates a read-only L1 reader for conversation_summaries.db.
func NewSummariesReader(dbPath string) *SummariesReader {
	return &SummariesReader{dbPath: dbPath}
}

// Close releases the underlying read-only SQLite handle.
func (r *SummariesReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		err := r.db.Close()
		r.db = nil
		r.versionKnown = false
		return err
	}
	return nil
}

func (r *SummariesReader) ensureOpenLocked() (*sql.DB, error) {
	if r.db != nil {
		return r.db, nil
	}
	if _, err := os.Stat(r.dbPath); err != nil {
		return nil, err
	}
	db, err := openReadOnlySQLite(r.dbPath)
	if err != nil {
		return nil, err
	}
	r.db = db
	return db, nil
}

// ReadAll reads all conversation summaries and resolves parent-child relationships
// using both conversation_summaries.parent_conversation_id and the conversation_parents table.
func (r *SummariesReader) ReadAll(ctx context.Context) ([]SummaryRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	db, err := r.ensureOpenLocked()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	rows, _, err := r.readAllLocked(ctx, db)
	return rows, err
}

func (r *SummariesReader) readChanges(ctx context.Context) ([]SummaryRow, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	db, err := r.ensureOpenLocked()
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}

	var version int64
	err = db.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&version)
	if err == nil && r.versionKnown && r.version == version {
		return nil, false, nil
	}
	r.versionKnown = false
	rows, parentsRead, readErr := r.readAllLocked(ctx, db)
	if readErr != nil {
		return nil, false, readErr
	}
	if err == nil && ctx.Err() == nil && parentsRead {
		r.version, r.versionKnown = version, true
	}
	return rows, true, nil
}

func (r *SummariesReader) readAllLocked(ctx context.Context, db *sql.DB) ([]SummaryRow, bool, error) {
	const q = `
		SELECT
			conversation_id,
			COALESCE(title, ''),
			COALESCE(preview, ''),
			COALESCE(step_count, 0),
			COALESCE(CAST(last_modified_time AS TEXT), ''),
			COALESCE(workspace_uris, ''),
			COALESCE(status, ''),
			COALESCE(agent_name, ''),
			COALESCE(parent_conversation_id, ''),
			COALESCE(nesting_depth, 0),
			COALESCE(not_fully_idle, 0),
			COALESCE(killed, 0),
			COALESCE(CAST(last_user_input_time AS TEXT), ''),
			COALESCE(last_user_input_step_index, -1)
		FROM conversation_summaries
		ORDER BY last_modified_time DESC
	`

	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		// Reopen on next attempt in case the file was replaced.
		_ = r.db.Close()
		r.db = nil
		r.versionKnown = false
		return nil, false, fmt.Errorf("query conversation_summaries: %w", err)
	}
	defer rows.Close()

	var result []SummaryRow
	indexByID := make(map[string]int, 64)

	for rows.Next() {
		var (
			row             SummaryRow
			modRaw          string
			urisRaw         string
			lastUserRaw     string
			notFullyIdleVal any
			killedVal       any
		)
		if err := rows.Scan(
			&row.ConversationID,
			&row.Title,
			&row.Preview,
			&row.StepCount,
			&modRaw,
			&urisRaw,
			&row.Status,
			&row.AgentName,
			&row.ParentConversationID,
			&row.NestingDepth,
			&notFullyIdleVal,
			&killedVal,
			&lastUserRaw,
			&row.LastUserInputStepIndex,
		); err != nil {
			return nil, false, fmt.Errorf("scan conversation_summaries: %w", err)
		}
		row.LastModifiedTime = parseSQLiteTime(modRaw)
		row.LastUserInputTime = parseSQLiteTime(lastUserRaw)
		row.NotFullyIdle = parseSQLiteBool(notFullyIdleVal)
		row.Killed = parseSQLiteBool(killedVal)
		row.WorkspaceURIs, row.Workspace = parseWorkspaceURIs(urisRaw)

		indexByID[row.ConversationID] = len(result)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate conversation_summaries: %w", err)
	}

	// Best-effort backfill from conversation_parents if the table exists.
	parentsRead := r.mergeConversationParentsLocked(ctx, db, &result, indexByID)
	return result, parentsRead, nil
}

func (r *SummariesReader) mergeConversationParentsLocked(ctx context.Context, db *sql.DB, rows *[]SummaryRow, indexByID map[string]int) bool {
	pRows, err := db.QueryContext(ctx, `SELECT conversation_id, parent_id FROM conversation_parents`)
	if err != nil {
		return false
	}
	defer pRows.Close()

	for pRows.Next() {
		var childID, parentID string
		if err := pRows.Scan(&childID, &parentID); err != nil || childID == "" || parentID == "" {
			continue
		}
		if idx, ok := indexByID[childID]; ok {
			if (*rows)[idx].ParentConversationID == "" {
				(*rows)[idx].ParentConversationID = parentID
			}
		} else {
			// Child exists in conversation_parents even if absent from conversation_summaries.
			indexByID[childID] = len(*rows)
			*rows = append(*rows, SummaryRow{
				ConversationID:       childID,
				ParentConversationID: parentID,
				NestingDepth:         1,
			})
		}
	}
	return pRows.Err() == nil
}

// BuildConversationTrees organizes flat SummaryRows into top-level conversation trees
// sorted by running status (running first) then LastModifiedTime descending.
func BuildConversationTrees(rows []SummaryRow) []ConversationTree {
	if len(rows) == 0 {
		return nil
	}

	byID := make(map[string]SummaryRow, len(rows))
	childrenByParent := make(map[string][]string, len(rows))
	for _, r := range rows {
		byID[r.ConversationID] = r
	}

	var rootIDs []string
	for _, r := range rows {
		if r.ParentConversationID == "" || r.ParentConversationID == r.ConversationID {
			rootIDs = append(rootIDs, r.ConversationID)
			continue
		}
		if _, parentExists := byID[r.ParentConversationID]; !parentExists {
			// Orphan child whose parent summary is gone: treat as top-level so telemetry is not lost.
			rootIDs = append(rootIDs, r.ConversationID)
			continue
		}
		childrenByParent[r.ParentConversationID] = append(childrenByParent[r.ParentConversationID], r.ConversationID)
	}

	trees := make([]ConversationTree, 0, len(rootIDs))
	for _, rid := range rootIDs {
		root := byID[rid]
		var descendants []SummaryRow
		visited := map[string]bool{rid: true}
		queue := append([]string(nil), childrenByParent[rid]...)
		for len(queue) > 0 {
			cid := queue[0]
			queue = queue[1:]
			if visited[cid] {
				continue
			}
			visited[cid] = true
			descendants = append(descendants, byID[cid])
			queue = append(queue, childrenByParent[cid]...)
		}

		// Sort subagents chronologically (older first for stable #1, #2 numbering), tie-breaking by ID.
		sort.SliceStable(descendants, func(i, j int) bool {
			ti, tj := descendants[i].LastModifiedTime, descendants[j].LastModifiedTime
			if !ti.Equal(tj) {
				if ti.IsZero() {
					return false
				}
				if tj.IsZero() {
					return true
				}
				return ti.Before(tj)
			}
			return descendants[i].ConversationID < descendants[j].ConversationID
		})

		trees = append(trees, ConversationTree{
			Root:     root,
			Children: descendants,
		})
	}

	// Sort trees: running (root or any child running) first, then latest activity descending.
	treeActivity := func(t ConversationTree) (running bool, latest time.Time) {
		running = t.Root.IsRunning()
		latest = t.Root.LastModifiedTime
		for _, c := range t.Children {
			if c.IsRunning() {
				running = true
			}
			if c.LastModifiedTime.After(latest) {
				latest = c.LastModifiedTime
			}
		}
		return running, latest
	}

	sort.SliceStable(trees, func(i, j int) bool {
		ri, ti := treeActivity(trees[i])
		rj, tj := treeActivity(trees[j])
		if ri != rj {
			return ri
		}
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return trees[i].Root.ConversationID < trees[j].Root.ConversationID
	})

	return trees
}

// OpenReadOnlySQLite verifies dbPath exists and opens a SQLite database in strict
// read-only WAL-compatible mode (mode=ro, query_only, busy_timeout; never immutable=1).
func OpenReadOnlySQLite(dbPath string) (*sql.DB, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, err
	}
	return openReadOnlySQLite(dbPath)
}

// openReadOnlySQLite opens a SQLite database in strict read-only WAL-compatible mode.
func openReadOnlySQLite(dbPath string) (*sql.DB, error) {
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		absPath = dbPath
	}
	dsn := buildReadOnlyDSN(absPath)
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite ro %q: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func buildReadOnlyDSN(absPath string) string {
	slashed := filepath.ToSlash(absPath)
	replacer := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")
	escaped := replacer.Replace(slashed)
	// mode=ro enforces OS/SQLite read-only open; _pragma applies query_only & busy_timeout on modernc.org/sqlite.
	return fmt.Sprintf("file:%s?mode=ro&_query_only=1&_busy_timeout=150&_pragma=query_only(1)&_pragma=busy_timeout(150)", escaped)
}

func parseSQLiteTime(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05.999999999-0700",
		"2006-01-02 15:04:05-0700",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func parseSQLiteBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "1" || s == "true" || s == "t" || s == "yes"
	case []byte:
		s := strings.ToLower(strings.TrimSpace(string(x)))
		return s == "1" || s == "true" || s == "t" || s == "yes"
	default:
		return false
	}
}

func parseWorkspaceURIs(raw string) ([]string, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, ""
	}
	var uris []string
	if strings.HasPrefix(s, "[") {
		_ = json.Unmarshal([]byte(s), &uris)
	} else {
		uris = []string{s}
	}
	if len(uris) == 0 {
		return nil, ""
	}
	return uris, cleanWorkspaceURI(uris[0])
}

func cleanWorkspaceURI(uri string) string {
	s := strings.TrimSpace(uri)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(s), "file://") {
		if u, err := url.Parse(s); err == nil {
			p := u.Path
			if unesc, err := url.PathUnescape(p); err == nil {
				p = unesc
			}
			// On Windows file:///d%3A/vps parses Path as "/d:/vps" -> strip leading slash before drive letter.
			if len(p) >= 3 && p[0] == '/' && ((p[1] >= 'a' && p[1] <= 'z') || (p[1] >= 'A' && p[1] <= 'Z')) && p[2] == ':' {
				p = p[1:]
			}
			s = p
		} else {
			s = strings.TrimPrefix(s, "file:///")
		}
	}
	if unesc, err := url.PathUnescape(s); err == nil {
		s = unesc
	}
	return strings.TrimRight(s, "/\\")
}
