package source

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultMaxOpenDBs = 5

type convHandleEntry struct {
	convID string
	db     *sql.DB
}

type convCacheState struct {
	lastIdx   int
	records   []GenMetadata
	stepTimes map[int]time.Time
}

// ConvDBPool reads conversations/<conv_id>.db incrementally (`WHERE idx > lastIdx`)
// while bounding open read-only SQLite handles via an LRU pool (default 5).
type ConvDBPool struct {
	mu      sync.Mutex
	convDir string
	maxOpen int

	lruList *list.List
	handles map[string]*list.Element
	states  map[string]*convCacheState
}

// NewConvDBPool creates an LRU-bounded incremental L2 reader for conversations/*.db.
func NewConvDBPool(convDir string, maxOpen int) *ConvDBPool {
	if maxOpen <= 0 {
		maxOpen = defaultMaxOpenDBs
	}
	return &ConvDBPool{
		convDir: convDir,
		maxOpen: maxOpen,
		lruList: list.New(),
		handles: make(map[string]*list.Element, maxOpen),
		states:  make(map[string]*convCacheState),
	}
}

// OpenHandleCount returns the current number of open read-only SQLite handles (for tests/metrics).
func (p *ConvDBPool) OpenHandleCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.handles)
}

// Close closes all open read-only SQLite handles in the pool.
func (p *ConvDBPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	for id, elem := range p.handles {
		entry := elem.Value.(*convHandleEntry)
		if entry.db != nil {
			if err := entry.db.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		delete(p.handles, id)
	}
	p.lruList.Init()
	return firstErr
}

// Sync incrementally queries new gen_metadata rows (idx > lastIdx) for convID and returns
// a snapshot copy of all decoded records and known step timestamps for convID.
func (p *ConvDBPool) Sync(ctx context.Context, convID string) (changed bool, records []GenMetadata, stepTimes map[int]time.Time, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	changed, state, err := p.syncLocked(ctx, convID)
	return changed, cloneGenMetadata(state.records), cloneStepTimes(state.stepTimes), err
}

func (p *ConvDBPool) syncBatch(ctx context.Context, ids []string) map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	cached := 0
	for i, id := range ids {
		if p.handles[id] != nil {
			ids[cached], ids[i] = ids[i], ids[cached]
			cached++
		}
	}

	var changed map[string]bool
	for _, id := range ids {
		if updated, _, _ := p.syncLocked(ctx, id); updated {
			if changed == nil {
				changed = make(map[string]bool)
			}
			changed[id] = true
		}
	}
	return changed
}

func (p *ConvDBPool) syncLocked(ctx context.Context, convID string) (changed bool, state *convCacheState, err error) {
	state = p.states[convID]
	if state == nil {
		state = &convCacheState{
			lastIdx:   -1,
			stepTimes: make(map[int]time.Time),
		}
		p.states[convID] = state
	}

	db, err := p.acquireLocked(convID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, state, nil
		}
		return false, state, err
	}

	rows, err := db.QueryContext(ctx, `SELECT idx, data FROM gen_metadata WHERE idx > ? ORDER BY idx ASC`, state.lastIdx)
	if err != nil {
		// Evict possibly stale handle on error so next sync reopens cleanly.
		p.evictByIDLocked(convID)
		return false, state, fmt.Errorf("query gen_metadata %s: %w", convID, err)
	}

	var newStepIdxs []int
	for rows.Next() {
		var (
			idx  int
			blob sql.RawBytes
		)
		if err := rows.Scan(&idx, &blob); err != nil {
			_ = rows.Close()
			return changed, state, fmt.Errorf("scan gen_metadata %s: %w", convID, err)
		}
		if idx > state.lastIdx {
			state.lastIdx = idx
		}
		if len(blob) == 0 {
			continue
		}
		gm, decErr := DecodeGenMetadata(idx, blob)
		if decErr != nil {
			// Skip corrupted partial record gracefully without breaking the conversation stream.
			continue
		}
		state.records = append(state.records, gm)
		changed = true
		if gm.StepIndex >= 0 {
			if _, ok := state.stepTimes[gm.StepIndex]; !ok {
				newStepIdxs = append(newStepIdxs, gm.StepIndex)
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return changed, state, fmt.Errorf("iterate gen_metadata %s: %w", convID, err)
	}
	_ = rows.Close()

	if len(newStepIdxs) > 0 {
		p.fetchStepTimestampsLocked(ctx, db, newStepIdxs, state.stepTimes)
	}

	return changed, state, nil
}

// PruneCache drops cached state for conversation IDs not in keepIDs, preventing unbounded memory growth.
func (p *ConvDBPool) PruneCache(keepIDs map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for id := range p.states {
		if !keepIDs[id] {
			delete(p.states, id)
			p.evictByIDLocked(id)
		}
	}
}

func (p *ConvDBPool) acquireLocked(convID string) (*sql.DB, error) {
	if elem, ok := p.handles[convID]; ok {
		p.lruList.MoveToFront(elem)
		return elem.Value.(*convHandleEntry).db, nil
	}

	dbPath := filepath.Join(p.convDir, convID+".db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, err
	}
	db, err := openReadOnlySQLite(dbPath)
	if err != nil {
		return nil, err
	}

	if p.lruList.Len() >= p.maxOpen {
		oldest := p.lruList.Back()
		if oldest != nil {
			entry := oldest.Value.(*convHandleEntry)
			if entry.db != nil {
				_ = entry.db.Close()
			}
			delete(p.handles, entry.convID)
			p.lruList.Remove(oldest)
		}
	}

	elem := p.lruList.PushFront(&convHandleEntry{convID: convID, db: db})
	p.handles[convID] = elem
	return db, nil
}

func (p *ConvDBPool) evictByIDLocked(convID string) {
	elem, ok := p.handles[convID]
	if !ok {
		return
	}
	entry := elem.Value.(*convHandleEntry)
	if entry.db != nil {
		_ = entry.db.Close()
	}
	delete(p.handles, convID)
	p.lruList.Remove(elem)
}

func (p *ConvDBPool) fetchStepTimestampsLocked(ctx context.Context, db *sql.DB, idxs []int, dst map[int]time.Time) {
	// Batch in chunks of 100 to stay well within SQLite variable limits.
	const chunkSize = 100
	for start := 0; start < len(idxs); start += chunkSize {
		end := start + chunkSize
		if end > len(idxs) {
			end = len(idxs)
		}
		chunk := idxs[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for i, v := range chunk {
			args[i] = v
		}

		q := "SELECT idx, metadata FROM steps WHERE idx IN (" + placeholders + ")"
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return
		}
		for rows.Next() {
			var (
				sIdx int
				meta sql.RawBytes
			)
			if err := rows.Scan(&sIdx, &meta); err != nil {
				continue
			}
			if ts := DecodeStepTimestamp(meta); !ts.IsZero() {
				dst[sIdx] = ts
			}
		}
		_ = rows.Close()
	}
}

func cloneGenMetadata(src []GenMetadata) []GenMetadata {
	if len(src) == 0 {
		return nil
	}
	out := make([]GenMetadata, len(src))
	copy(out, src)
	return out
}

func cloneStepTimes(src map[int]time.Time) map[int]time.Time {
	if len(src) == 0 {
		return nil
	}
	out := make(map[int]time.Time, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
