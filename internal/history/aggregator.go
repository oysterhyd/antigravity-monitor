package history

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/source"
)

// Service implements Aggregator over local Antigravity SQLite databases with
// WAL-safe incremental (conv_id, mtime, size) caching across one or more data roots.
type Service struct {
	roots        []string
	autoDiscover bool
	discoverFunc func() []string
	cachePath    string
	calc         *metrics.Calculator
	pricingHash  string

	loadOnce sync.Once
	syncMu   sync.Mutex

	mu         sync.RWMutex
	cache      cacheFile
	cacheDirty bool
	summaries  map[string]source.SummaryRow
	parents    map[string]string
	lastStats  RefreshStats
}

// Compile-time check that *Service implements Aggregator.
var _ Aggregator = (*Service)(nil)

// NewAggregator constructs a local read-only usage history aggregation Service.
//   - dataRoot == "" enables automatic discovery across native ~/.gemini/antigravity,
//     CLI, and T3 Code ACP provider directories.
//   - dataRoot != "" restricts aggregation strictly to that single root directory.
//   - cachePath == "" runs strictly in-memory without reading or writing disk cache.
//   - calc == nil defaults to metrics.NewDefaultCalculator().
func NewAggregator(dataRoot, cachePath string, calc *metrics.Calculator) *Service {
	dataRoot = strings.TrimSpace(dataRoot)
	if dataRoot == "" {
		return newService(nil, true, source.DiscoverRootDirs, cachePath, calc)
	}
	return newService([]string{filepath.Clean(dataRoot)}, false, nil, cachePath, calc)
}

// NewMultiRootAggregator constructs a usage history aggregation Service over an explicit
// list of Antigravity data root directories (disabling automatic discovery).
func NewMultiRootAggregator(roots []string, cachePath string, calc *metrics.Calculator) *Service {
	clean := make([]string, 0, len(roots))
	for _, r := range roots {
		if r = strings.TrimSpace(r); r != "" {
			clean = append(clean, filepath.Clean(r))
		}
	}
	if len(clean) == 0 {
		return NewAggregator("", cachePath, calc)
	}
	return newService(clean, false, nil, cachePath, calc)
}

func newService(roots []string, autoDiscover bool, discoverFn func() []string, cachePath string, calc *metrics.Calculator) *Service {
	cachePath = strings.TrimSpace(cachePath)
	if calc == nil {
		var err error
		calc, err = metrics.NewDefaultCalculator()
		if err != nil {
			calc = metrics.NewCalculatorFromConfig(model.PricingConfig{})
		}
	}

	pricingHash := computePricingHash(calc)

	return &Service{
		roots:        roots,
		autoDiscover: autoDiscover,
		discoverFunc: discoverFn,
		cachePath:    cachePath,
		calc:         calc,
		pricingHash:  pricingHash,
		summaries:    make(map[string]source.SummaryRow),
		parents:      make(map[string]string),
	}
}

func (s *Service) resolveRoots() []string {
	if s.autoDiscover && s.discoverFunc != nil {
		if discovered := s.discoverFunc(); len(discovered) > 0 {
			return discovered
		}
		return []string{source.DefaultRootDir()}
	}
	return s.roots
}

func (s *Service) ensureCacheLoaded() {
	s.loadOnce.Do(func() {
		s.cache, s.cacheDirty = loadCache(s.cachePath, s.pricingHash)
	})
}

// LastStats returns the cache hit/miss/prune counts from the most recent Refresh.
func (s *Service) LastStats() RefreshStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastStats
}

// Refresh incrementally scans conversation_summaries.db and conversations/*.db,
// updating the in-memory index and persisting usage_cache.json when dirty.
func (s *Service) Refresh(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.ensureCacheLoaded()
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	s.mu.RLock()
	prevConvs := s.cache.Conversations
	prevSummaries := s.summaries
	prevParents := s.parents
	dirty := s.cacheDirty
	s.mu.RUnlock()

	roots := s.resolveRoots()

	nextSummaries, nextParents, sumErr := s.readSummaries(ctx, roots)
	if sumErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Keep previous summaries if all L1 DBs had transient errors.
		if len(prevSummaries) > 0 {
			nextSummaries = prevSummaries
			nextParents = prevParents
		}
	}

	type convCandidate struct {
		root   string
		dbPath string
		stat   convFileStat
	}
	candidates := make(map[string]convCandidate)
	var firstDirErr error
	readAnyDir := false

	for _, root := range roots {
		convDir := filepath.Join(root, "conversations")
		dirEntries, err := os.ReadDir(convDir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && firstDirErr == nil {
				firstDirErr = err
			}
			continue
		}
		readAnyDir = true

		for _, de := range dirEntries {
			if de.IsDir() {
				continue
			}
			name := de.Name()
			if !strings.EqualFold(filepath.Ext(name), ".db") {
				continue
			}
			ext := filepath.Ext(name)
			convID := strings.TrimSuffix(name, ext)
			if convID == "" {
				continue
			}
			dbPath := filepath.Join(convDir, name)
			st, err := statConvDB(dbPath)
			if err != nil {
				continue
			}
			if existing, ok := candidates[convID]; ok {
				if existing.stat.effectiveMtimeNs > st.effectiveMtimeNs ||
					(existing.stat.effectiveMtimeNs == st.effectiveMtimeNs && existing.stat.effectiveSize >= st.effectiveSize) {
					continue
				}
			}
			candidates[convID] = convCandidate{
				root:   root,
				dbPath: dbPath,
				stat:   st,
			}
		}
	}
	if !readAnyDir && firstDirErr != nil {
		return firstDirErr
	}

	convIDs := make([]string, 0, len(candidates))
	for id := range candidates {
		convIDs = append(convIDs, id)
	}
	sort.Strings(convIDs)

	nextConvs := make(map[string]*cachedConv, len(convIDs))
	seenIDs := make(map[string]bool, len(convIDs))
	var stats RefreshStats

	for _, convID := range convIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		cand := candidates[convID]
		seenIDs[convID] = true

		if prev, ok := prevConvs[convID]; ok && prev.matches(cand.stat) {
			stats.Hits++
			nextConvs[convID] = prev
			continue
		}

		stats.Misses++
		dirty = true
		entry, err := s.scanConvDB(ctx, cand.root, convID, cand.dbPath, cand.stat, nextSummaries[convID])
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Skip unreadable/corrupted DB without aborting the rest of the scan.
			continue
		}
		nextConvs[convID] = entry
	}

	for id := range prevConvs {
		if !seenIDs[id] {
			stats.Pruned++
			dirty = true
		}
	}

	nextCache := cacheFile{
		Version:       cacheSchemaVersion,
		PricingHash:   s.pricingHash,
		Conversations: nextConvs,
	}

	if s.cachePath != "" && dirty {
		if err := saveCacheAtomic(s.cachePath, nextCache); err != nil {
			return err
		}
		dirty = false
	}

	s.mu.Lock()
	s.cache = nextCache
	s.cacheDirty = dirty
	s.summaries = nextSummaries
	s.parents = nextParents
	s.lastStats = stats
	s.mu.Unlock()

	return nil
}

func (s *Service) readSummaries(ctx context.Context, roots []string) (map[string]source.SummaryRow, map[string]string, error) {
	summaries := make(map[string]source.SummaryRow)
	parents := make(map[string]string)
	var firstErr error
	succeeded := 0

	for _, root := range roots {
		sumPath := filepath.Join(root, "conversation_summaries.db")
		reader := source.NewSummariesReader(sumPath)
		rows, err := reader.ReadAll(ctx)
		_ = reader.Close()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		succeeded++
		for _, row := range rows {
			if existing, ok := summaries[row.ConversationID]; ok {
				summaries[row.ConversationID] = mergeHistorySummary(existing, row)
			} else {
				summaries[row.ConversationID] = row
			}
			if row.ParentConversationID != "" && row.ParentConversationID != row.ConversationID {
				if _, exists := parents[row.ConversationID]; !exists {
					parents[row.ConversationID] = row.ParentConversationID
				}
			}
		}
	}
	if succeeded == 0 && firstErr != nil {
		return summaries, parents, firstErr
	}
	return summaries, parents, nil
}

func mergeHistorySummary(a, b source.SummaryRow) source.SummaryRow {
	primary, secondary := a, b
	if b.LastModifiedTime.After(a.LastModifiedTime) {
		primary, secondary = b, a
	}
	if strings.TrimSpace(primary.Title) == "" {
		primary.Title = secondary.Title
	}
	if strings.TrimSpace(primary.Preview) == "" {
		primary.Preview = secondary.Preview
	}
	if strings.TrimSpace(primary.AgentName) == "" {
		primary.AgentName = secondary.AgentName
	}
	if primary.Workspace == "" {
		primary.Workspace = secondary.Workspace
		primary.WorkspaceURIs = secondary.WorkspaceURIs
	}
	if primary.ParentConversationID == "" {
		primary.ParentConversationID = secondary.ParentConversationID
	}
	return primary
}

func (s *Service) scanConvDB(ctx context.Context, rootDir, convID, dbPath string, st convFileStat, sum source.SummaryRow) (*cachedConv, error) {
	db, err := source.OpenReadOnlySQLite(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	entry := &cachedConv{
		ConvID:     convID,
		MtimeNs:    st.effectiveMtimeNs,
		Size:       st.effectiveSize,
		DBMtimeNs:  st.dbMtimeNs,
		DBSize:     st.dbSize,
		WalMtimeNs: st.walMtimeNs,
		WalSize:    st.walSize,
	}

	rows, err := db.QueryContext(ctx, `SELECT idx, data FROM gen_metadata ORDER BY idx ASC`)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// DB may not have gen_metadata table (e.g., empty/uninitialized DB); try L3 fallback cleanly.
		rows = nil
	}

	var validGens []source.GenMetadata
	var stepIdxs []int
	seenSteps := make(map[int]bool)

	if rows != nil {
		for rows.Next() {
			var (
				idx  int
				blob sql.RawBytes
			)
			if scanErr := rows.Scan(&idx, &blob); scanErr != nil {
				continue
			}
			if len(blob) == 0 {
				continue
			}
			gm, decErr := source.DecodeGenMetadata(idx, blob)
			if decErr != nil {
				continue
			}
			if entry.RootConvID == "" {
				if gm.RootCascadeID != "" && gm.RootCascadeID != convID {
					entry.RootConvID = gm.RootCascadeID
				} else if gm.ParentCascadeID != "" && gm.ParentCascadeID != convID {
					entry.RootConvID = gm.ParentCascadeID
				}
			}
			if gm.IsEmptyFailure() {
				continue
			}
			validGens = append(validGens, gm)
			if gm.StepIndex >= 0 && !seenSteps[gm.StepIndex] {
				seenSteps[gm.StepIndex] = true
				stepIdxs = append(stepIdxs, gm.StepIndex)
			}
		}
		_ = rows.Close()
	}

	stepTimes := make(map[int]time.Time, len(stepIdxs))
	if len(stepIdxs) > 0 {
		fetchStepTimestamps(ctx, db, stepIdxs, stepTimes)
	}

	needsTranscript := len(validGens) == 0
	if !needsTranscript {
		for _, gm := range validGens {
			if stepTimes[gm.StepIndex].IsZero() ||
				gm.ModelSlug == "" ||
				(gm.InputTokens == 0 && gm.OutputTokens == 0 && gm.CacheReadTokens == 0 && gm.CacheWriteTokens == 0) {
				needsTranscript = true
				break
			}
		}
	}

	var tr source.TranscriptData
	if needsTranscript {
		tailer := source.NewTranscriptTailer(source.TranscriptPath(rootDir, convID))
		_, _ = tailer.Sync()
		tr = tailer.Snapshot()
	}

	var fallbackMtime time.Time
	if st.effectiveMtimeNs > 0 {
		fallbackMtime = time.Unix(0, st.effectiveMtimeNs).UTC()
	}

	if len(validGens) > 0 {
		entry.Requests = make([]cachedRequest, 0, len(validGens))
		for _, gm := range validGens {
			rec := model.RequestRecord{
				GenIndex:         gm.GenIndex,
				StepIndex:        gm.StepIndex,
				ConversationID:   convID,
				ModelSlug:        gm.ModelSlug,
				ModelDisplay:     gm.ModelDisplay,
				InputTokens:      gm.InputTokens,
				CacheReadTokens:  gm.CacheReadTokens,
				CacheWriteTokens: gm.CacheWriteTokens,
				OutputTokens:     gm.OutputTokens,
				ThinkingTokens:   gm.ThinkingTokens,
				ResponseTokens:   gm.ResponseTokens,
				ContextUsed:      gm.ContextUsed,
				ContextLimit:     gm.ContextLimit,
				TTFT:             gm.TTFT,
				TotalDuration:    gm.TotalDuration,
			}

			if ts, ok := stepTimes[gm.StepIndex]; ok && !ts.IsZero() {
				rec.Timestamp = ts
			} else if ts, ok := tr.StepTimestamps[gm.StepIndex]; ok && !ts.IsZero() {
				rec.Timestamp = ts
			}

			if pl, ok := tr.PlannersByStep[gm.StepIndex]; ok {
				if rec.InputTokens == 0 && rec.OutputTokens == 0 && rec.CacheReadTokens == 0 && rec.CacheWriteTokens == 0 {
					rec.InputTokens = pl.InputTokens
					rec.CacheReadTokens = pl.CacheReadTokens
					rec.OutputTokens = pl.OutputTokens
					rec.ResponseTokens = pl.OutputTokens
				}
				if rec.ModelSlug == "" && pl.Model != "" {
					rec.ModelSlug = pl.Model
				}
				if rec.Timestamp.IsZero() && !pl.CreatedAt.IsZero() {
					rec.Timestamp = pl.CreatedAt
				}
			}

			if rec.Timestamp.IsZero() && !sum.LastModifiedTime.IsZero() {
				rec.Timestamp = sum.LastModifiedTime
			}
			if rec.Timestamp.IsZero() && !fallbackMtime.IsZero() {
				rec.Timestamp = fallbackMtime
			}

			s.calc.EnrichRecord(&rec)
			entry.Requests = append(entry.Requests, cachedRequest{
				TSUnixMs:      rec.Timestamp.UnixMilli(),
				TSUnixNs:      rec.Timestamp.UnixNano(),
				SubModel:      metrics.ClassifySubModel(rec.ModelSlug, rec.ModelDisplay),
				Pool:          metrics.ClassifyPool(rec.ModelSlug, rec.ModelDisplay),
				InputTokens:   rec.InputTokens,
				CacheReadTok:  rec.CacheReadTokens,
				CacheWriteTok: rec.CacheWriteTokens,
				OutputTokens:  rec.OutputTokens,
				CostUSD:       rec.CostUSD,
				CacheSavedUSD: rec.CacheSavedUSD,
			})
		}
		return entry, nil
	}

	// Full L3 transcript fallback if L2 gen_metadata was empty.
	if len(tr.Planners) > 0 {
		entry.Requests = make([]cachedRequest, 0, len(tr.Planners))
		for i, pl := range tr.Planners {
			rec := model.RequestRecord{
				GenIndex:        i,
				StepIndex:       pl.StepIndex,
				ConversationID:  convID,
				ModelSlug:       pl.Model,
				Timestamp:       pl.CreatedAt,
				InputTokens:     pl.InputTokens,
				CacheReadTokens: pl.CacheReadTokens,
				OutputTokens:    pl.OutputTokens,
				ResponseTokens:  pl.OutputTokens,
			}
			if rec.Timestamp.IsZero() && !sum.LastModifiedTime.IsZero() {
				rec.Timestamp = sum.LastModifiedTime
			}
			if rec.Timestamp.IsZero() && !fallbackMtime.IsZero() {
				rec.Timestamp = fallbackMtime
			}
			s.calc.EnrichRecord(&rec)
			entry.Requests = append(entry.Requests, cachedRequest{
				TSUnixMs:      rec.Timestamp.UnixMilli(),
				TSUnixNs:      rec.Timestamp.UnixNano(),
				SubModel:      metrics.ClassifySubModel(rec.ModelSlug, rec.ModelDisplay),
				Pool:          metrics.ClassifyPool(rec.ModelSlug, rec.ModelDisplay),
				InputTokens:   rec.InputTokens,
				CacheReadTok:  rec.CacheReadTokens,
				CacheWriteTok: rec.CacheWriteTokens,
				OutputTokens:  rec.OutputTokens,
				CostUSD:       rec.CostUSD,
				CacheSavedUSD: rec.CacheSavedUSD,
			})
		}
	}

	return entry, nil
}

func fetchStepTimestamps(ctx context.Context, db *sql.DB, idxs []int, dst map[int]time.Time) {
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
			if ts := source.DecodeStepTimestamp(meta); !ts.IsZero() {
				dst[sIdx] = ts
			}
		}
		_ = rows.Close()
	}
}

// Query aggregates historical usage across the requested Range using now's location
// and half-open [Start, End) calendar bucket boundaries.
func (s *Service) Query(r Range, now time.Time) QueryResult {
	s.ensureCacheLoaded()
	if now.IsZero() {
		now = time.Now()
	}
	switch r {
	case RangeToday, Range7D, Range30D, RangeAll:
	default:
		r = Range7D
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	loc := now.Location()
	y, m, d := now.In(loc).Date()
	todayStart := time.Date(y, m, d, 0, 0, 0, 0, loc)

	var (
		rangeStart time.Time
		rangeEnd   time.Time
		buckets    []BucketSummary
		earliestTS time.Time
		latestTS   time.Time
	)

	switch r {
	case RangeToday:
		rangeStart = todayStart
		rangeEnd = todayStart.AddDate(0, 0, 1)
		buckets = make([]BucketSummary, 24)
		for h := 0; h < 24; h++ {
			bStart := time.Date(y, m, d, h, 0, 0, 0, loc)
			bEnd := time.Date(y, m, d, h+1, 0, 0, 0, loc)
			buckets[h] = BucketSummary{
				Label: bStart.Format("15:04"),
				Start: bStart,
				End:   bEnd,
			}
		}
	case Range7D:
		rangeStart = todayStart.AddDate(0, 0, -6)
		rangeEnd = todayStart.AddDate(0, 0, 1)
		buckets = make([]BucketSummary, 7)
		for i := 0; i < 7; i++ {
			bStart := todayStart.AddDate(0, 0, i-6)
			bEnd := bStart.AddDate(0, 0, 1)
			buckets[i] = BucketSummary{
				Label: bStart.Format("01/02"),
				Start: bStart,
				End:   bEnd,
			}
		}
	case Range30D:
		rangeStart = todayStart.AddDate(0, 0, -29)
		rangeEnd = todayStart.AddDate(0, 0, 1)
		w1Start := todayStart.AddDate(0, 0, -29)
		w2Start := todayStart.AddDate(0, 0, -20)
		w3Start := todayStart.AddDate(0, 0, -13)
		w4Start := todayStart.AddDate(0, 0, -6)
		buckets = []BucketSummary{
			{Label: "W1", Start: w1Start, End: w2Start},
			{Label: "W2", Start: w2Start, End: w3Start},
			{Label: "W3", Start: w3Start, End: w4Start},
			{Label: "W4", Start: w4Start, End: rangeEnd},
		}
	case RangeAll:
		for _, entry := range s.cache.Conversations {
			for _, req := range entry.Requests {
				ts := req.timestamp()
				if ts.IsZero() {
					continue
				}
				if earliestTS.IsZero() || ts.Before(earliestTS) {
					earliestTS = ts
				}
				if latestTS.IsZero() || ts.After(latestTS) {
					latestTS = ts
				}
			}
		}
		nowMonth := time.Date(y, m, 1, 0, 0, 0, 0, loc)
		if earliestTS.IsZero() {
			buckets = []BucketSummary{{
				Label: nowMonth.Format("Jan"),
				Start: nowMonth,
				End:   nowMonth.AddDate(0, 1, 0),
			}}
		} else {
			ey, em, _ := earliestTS.In(loc).Date()
			earliestMonth := time.Date(ey, em, 1, 0, 0, 0, 0, loc)
			ly, lm, _ := latestTS.In(loc).Date()
			latestMonth := time.Date(ly, lm, 1, 0, 0, 0, 0, loc)
			endMonth := nowMonth
			if latestMonth.After(endMonth) {
				endMonth = latestMonth
			}

			monthCount := (endMonth.Year()-earliestMonth.Year())*12 + int(endMonth.Month()-earliestMonth.Month()) + 1
			layout := "Jan"
			if monthCount > 12 {
				layout = "Jan '06"
			}
			buckets = make([]BucketSummary, 0, monthCount)
			for cur := earliestMonth; !cur.After(endMonth); cur = cur.AddDate(0, 1, 0) {
				buckets = append(buckets, BucketSummary{
					Label: cur.Format(layout),
					Start: cur,
					End:   cur.AddDate(0, 1, 0),
				})
			}
		}
	}

	res := QueryResult{
		Range:   r,
		Buckets: buckets,
	}

	type modelAccum struct {
		name    string
		pool    metrics.ModelPool
		costUSD float64
		tokens  int64
	}
	byModelMap := make(map[string]*modelAccum)

	type sessionAccum struct {
		convID       string
		totalTokens  int64
		costUSD      float64
		geminiCost   float64
		claudeCost   float64
		geminiTokens int64
		claudeTokens int64
		updatedAt    time.Time
	}
	sessionMap := make(map[string]*sessionAccum)

	var sumRawInput, sumCacheWrite int64

	for convID, entry := range s.cache.Conversations {
		rootID := s.resolveRootConvIDLocked(convID)
		for _, req := range entry.Requests {
			ts := req.timestamp()
			if ts.IsZero() {
				continue
			}
			tsLoc := ts.In(loc)
			if r != RangeAll {
				if tsLoc.Before(rangeStart) || !tsLoc.Before(rangeEnd) {
					continue
				}
			}

			inp := req.InputTokens + req.CacheWriteTok
			cr := req.CacheReadTok
			out := req.OutputTokens
			tot := cr + inp + out

			res.RequestCount++
			res.CacheReadTokens += cr
			res.InputTokens += inp
			res.OutputTokens += out
			res.TotalTokens += tot
			res.TotalCostUSD += req.CostUSD
			res.CacheSavedUSD += req.CacheSavedUSD
			sumRawInput += req.InputTokens
			sumCacheWrite += req.CacheWriteTok

			for i := range res.Buckets {
				if !tsLoc.Before(res.Buckets[i].Start) && tsLoc.Before(res.Buckets[i].End) {
					res.Buckets[i].CacheReadTokens += cr
					res.Buckets[i].InputTokens += inp
					res.Buckets[i].OutputTokens += out
					res.Buckets[i].TotalTokens += tot
					res.Buckets[i].TotalCostUSD += req.CostUSD
					if req.Pool == metrics.PoolClaudeGPT {
						res.Buckets[i].ClaudeGPTCostUSD += req.CostUSD
					} else {
						res.Buckets[i].GeminiCostUSD += req.CostUSD
					}
					break
				}
			}

			ma := byModelMap[req.SubModel]
			if ma == nil {
				ma = &modelAccum{
					name: req.SubModel,
					pool: req.Pool,
				}
				byModelMap[req.SubModel] = ma
			}
			ma.costUSD += req.CostUSD
			ma.tokens += tot

			sa := sessionMap[rootID]
			if sa == nil {
				sa = &sessionAccum{convID: rootID}
				sessionMap[rootID] = sa
			}
			sa.totalTokens += tot
			sa.costUSD += req.CostUSD
			if req.Pool == metrics.PoolClaudeGPT {
				sa.claudeCost += req.CostUSD
				sa.claudeTokens += tot
			} else {
				sa.geminiCost += req.CostUSD
				sa.geminiTokens += tot
			}
			if ts.After(sa.updatedAt) {
				sa.updatedAt = ts
			}
		}
	}

	res.CacheHitRate = model.ComputeCacheHitRate(sumRawInput, res.CacheReadTokens, sumCacheWrite) * 100
	for i := range res.Buckets {
		b := &res.Buckets[i]
		b.CacheHitPct = model.ComputeCacheHitRate(b.InputTokens, b.CacheReadTokens, 0) * 100
	}

	if res.TotalCostUSD > 0 {
		switch r {
		case RangeToday:
			elapsedHours := now.In(loc).Hour() + 1
			if elapsedHours < 1 {
				elapsedHours = 1
			}
			res.AvgCostUSD = res.TotalCostUSD / float64(elapsedHours)
		case Range7D:
			res.AvgCostUSD = res.TotalCostUSD / 7.0
		case Range30D:
			res.AvgCostUSD = res.TotalCostUSD / 30.0
		case RangeAll:
			spanDays := 1
			if !earliestTS.IsZero() {
				ey, em, ed := earliestTS.In(loc).Date()
				endRef := now
				if latestTS.After(endRef) {
					endRef = latestTS
				}
				ly, lm, ld := endRef.In(loc).Date()
				eUTC := time.Date(ey, em, ed, 0, 0, 0, 0, time.UTC)
				lUTC := time.Date(ly, lm, ld, 0, 0, 0, 0, time.UTC)
				if d := int(lUTC.Sub(eUTC)/(24*time.Hour)) + 1; d > spanDays {
					spanDays = d
				}
			}
			res.AvgCostUSD = res.TotalCostUSD / float64(spanDays)
		}
	}

	if len(byModelMap) > 0 {
		res.ByModel = make([]ModelUsage, 0, len(byModelMap))
		for _, ma := range byModelMap {
			var share float64
			if res.TotalCostUSD > 0 {
				share = (ma.costUSD / res.TotalCostUSD) * 100
			} else if res.TotalTokens > 0 {
				share = (float64(ma.tokens) / float64(res.TotalTokens)) * 100
			}
			res.ByModel = append(res.ByModel, ModelUsage{
				Name:    ma.name,
				Pool:    ma.pool,
				CostUSD: ma.costUSD,
				Tokens:  ma.tokens,
				Share:   share,
			})
		}
		sort.Slice(res.ByModel, func(i, j int) bool {
			if res.ByModel[i].CostUSD != res.ByModel[j].CostUSD {
				return res.ByModel[i].CostUSD > res.ByModel[j].CostUSD
			}
			if res.ByModel[i].Tokens != res.ByModel[j].Tokens {
				return res.ByModel[i].Tokens > res.ByModel[j].Tokens
			}
			return res.ByModel[i].Name < res.ByModel[j].Name
		})
	}

	res.SessionCount = len(sessionMap)
	if len(sessionMap) > 0 {
		sessions := make([]SessionSummary, 0, len(sessionMap))
		for _, sa := range sessionMap {
			pool := metrics.PoolGemini
			if sa.claudeCost > sa.geminiCost || (sa.claudeCost == sa.geminiCost && sa.claudeTokens > sa.geminiTokens) {
				pool = metrics.PoolClaudeGPT
			}
			sessions = append(sessions, SessionSummary{
				ConvID:      sa.convID,
				Title:       s.resolveSessionTitleLocked(sa.convID),
				PrimaryPool: pool,
				TotalTokens: sa.totalTokens,
				CostUSD:     sa.costUSD,
				UpdatedAt:   sa.updatedAt,
			})
		}
		sort.Slice(sessions, func(i, j int) bool {
			if sessions[i].CostUSD != sessions[j].CostUSD {
				return sessions[i].CostUSD > sessions[j].CostUSD
			}
			if sessions[i].TotalTokens != sessions[j].TotalTokens {
				return sessions[i].TotalTokens > sessions[j].TotalTokens
			}
			if !sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
				return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
			}
			return sessions[i].ConvID < sessions[j].ConvID
		})
		if len(sessions) > 3 {
			sessions = sessions[:3]
		}
		res.TopSessions = sessions
	}

	return res
}

// ConsumptionSince returns pool-partitioned token & USD consumption for all requests
// with non-zero timestamp >= since.
func (s *Service) ConsumptionSince(since time.Time) PoolConsumption {
	s.ensureCacheLoaded()
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out PoolConsumption
	for _, entry := range s.cache.Conversations {
		for _, req := range entry.Requests {
			ts := req.timestamp()
			if ts.IsZero() || ts.Before(since) {
				continue
			}
			tot := req.totalTokens()
			if req.Pool == metrics.PoolClaudeGPT {
				out.ClaudeTokens += tot
				out.ClaudeCostUSD += req.CostUSD
			} else {
				out.GeminiTokens += tot
				out.GeminiCostUSD += req.CostUSD
			}
		}
	}
	return out
}

func (s *Service) resolveRootConvIDLocked(convID string) string {
	cur := convID
	visited := map[string]bool{cur: true}
	for {
		var parent string
		if sum, ok := s.summaries[cur]; ok && sum.ParentConversationID != "" && sum.ParentConversationID != cur {
			parent = sum.ParentConversationID
		} else if p, ok := s.parents[cur]; ok && p != "" && p != cur {
			parent = p
		} else if entry := s.cache.Conversations[cur]; entry != nil && entry.RootConvID != "" && entry.RootConvID != cur {
			parent = entry.RootConvID
		}

		if parent == "" || visited[parent] {
			return cur
		}
		_, inSum := s.summaries[parent]
		_, inConv := s.cache.Conversations[parent]
		if !inSum && !inConv {
			return cur
		}
		visited[parent] = true
		cur = parent
	}
}

func (s *Service) resolveSessionTitleLocked(convID string) string {
	if sum, ok := s.summaries[convID]; ok {
		if t := strings.TrimSpace(sum.Title); t != "" {
			return t
		}
		if p := strings.TrimSpace(sum.Preview); p != "" {
			return p
		}
		if a := strings.TrimSpace(sum.AgentName); a != "" {
			return a
		}
		if sum.Workspace != "" {
			if base := filepath.Base(sum.Workspace); base != "" && base != "." && base != string(filepath.Separator) {
				return base
			}
		}
	}
	if len(convID) >= 8 {
		return convID[:8]
	}
	if convID != "" {
		return convID
	}
	return "session"
}
