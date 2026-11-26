package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

const (
	defaultMaxConversations     = 20
	defaultMaxHydratedTrees     = defaultMaxConversations
	defaultWatchTranscriptTrees = 8
	defaultPollInterval         = 3 * time.Second
	defaultIdlePollInterval     = 12 * time.Second
	defaultRootDiscoverEvery    = 30 * time.Second
)

// Options configures the local read-only Antigravity data engine.
type Options struct {
	// RootDir is a single explicit Antigravity data root override. When both RootDir
	// and RootDirs are empty, Provider auto-discovers all local Antigravity roots
	// (native ~/.gemini/antigravity, CLI, and T3 Code ACP provider directories).
	RootDir string
	// RootDirs optionally specifies an explicit list of Antigravity data roots to merge.
	// Takes precedence over RootDir and disables automatic root discovery.
	RootDirs []string
	// DiscoverRoots optionally overrides DiscoverRootDirs when auto-discovery is active (used in tests).
	DiscoverRoots func() []string
	// PricingOverridePath is an optional path to a user pricing override JSON file.
	PricingOverridePath string
	// Calculator optionally overrides the default pricing calculator.
	Calculator *metrics.Calculator
	// MaxOpenDBs limits concurrent open read-only per-conversation SQLite handles per root (default 5).
	MaxOpenDBs int
	// MaxConversations caps how many top-level conversations appear in AppSnapshot (default 20).
	MaxConversations int
	// MaxHydratedTrees caps how many top-level trees are hydrated via L2/L3 (default MaxConversations; all running and pinned trees are always hydrated).
	MaxHydratedTrees int
	// Debounce sets the per-conversation fsnotify debounce interval (default 150ms).
	Debounce time.Duration
	// PollInterval sets the low-frequency safety-net refresh interval (0 = adaptive 3s active / 12s idle; negative disables polling).
	PollInterval time.Duration
	// DisableWatcher disables fsnotify and background polling (useful for one-shot CLI runs and unit tests).
	DisableWatcher bool
	// AsyncInit hydrates only the top-1 active conversation synchronously during NewProvider
	// and completes remaining background hydration asynchronously in runLoop for faster startup.
	AsyncInit bool
}

// DefaultRootDir returns ~/.gemini/antigravity.
func DefaultRootDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".gemini/antigravity"
	}
	return filepath.Join(home, ".gemini", "antigravity")
}

type rootSource struct {
	key       string
	rootDir   string
	summaries *SummariesReader
	convPool  *ConvDBPool
	watcher   *dirWatcher
	rows      []SummaryRow
}

// Provider implements model.SnapshotProvider over Antigravity's local 3-layer data store
// (L1 conversation_summaries.db + L2 conversations/<id>.db + L3 transcript.jsonl),
// transparently merging conversations across one or more local data roots.
// All methods are safe for concurrent use by multiple goroutines.
type Provider struct {
	opts         Options
	calc         *metrics.Calculator
	autoDiscover bool
	adaptivePoll bool
	discoverFunc func() []string

	// Sync serialization prevents overlapping DB refreshes while keeping Snapshot() non-blocking.
	syncMu               sync.Mutex
	roots                []*rootSource
	rootsByKey           map[string]*rootSource
	convRoot             map[string]*rootSource
	resolvedRoot         map[string]*rootSource
	trees                []cachedTree
	tailers              map[string]*TranscriptTailer
	hasSnapshot          bool
	fullHydrated         bool
	snapshotDirty        bool
	onDemandTimeout      time.Duration
	lastRootDiscover     time.Time
	rootDiscoverInterval time.Duration

	snapMu   sync.RWMutex
	snapshot model.AppSnapshot

	pinMu          sync.Mutex
	pinnedBySlot   map[string]string
	hydratedSet    map[string]bool
	watchedSet     map[string]bool
	pinGen         uint64
	appliedPinGen  uint64
	stopped        bool
	onDemandActive bool
	onDemandWG     sync.WaitGroup

	subMu   sync.Mutex
	subs    map[int]chan model.AppSnapshot
	nextSub int
	closed  bool

	baseCtx    context.Context
	cancelBase context.CancelFunc
	triggerCh  chan watchEvent
	hydrateCh  chan struct{}
	stopCh     chan struct{}
	doneCh     chan struct{}
	hasLoop    bool
}

type cachedTree struct {
	tree          ConversationTree
	snapshot      model.ConversationSnapshot
	hydrated      bool // true only when L2 DB sync for the tree completed without error
	builtHydrated bool // true when snapshot reflects hydrate=true data (including partial L3 fallback)
}

// Compile-time assertions that *Provider implements model.SnapshotProvider and model.ConversationHydrator.
var (
	_ model.SnapshotProvider     = (*Provider)(nil)
	_ model.ConversationHydrator = (*Provider)(nil)
)

// NewProvider initializes the read-only local data engine, performs an initial synchronous
// snapshot build, and starts the fsnotify watcher unless Options.DisableWatcher is set.
func NewProvider(opts Options) (*Provider, error) {
	if opts.MaxOpenDBs <= 0 {
		opts.MaxOpenDBs = defaultMaxOpenDBs
	}
	if opts.MaxConversations <= 0 {
		opts.MaxConversations = defaultMaxConversations
	}
	if opts.MaxHydratedTrees <= 0 {
		opts.MaxHydratedTrees = opts.MaxConversations
	}
	if opts.Debounce <= 0 {
		opts.Debounce = defaultDebounce
	}
	adaptivePoll := opts.PollInterval == 0
	if opts.PollInterval == 0 {
		opts.PollInterval = defaultPollInterval
	}

	calc := opts.Calculator
	if calc == nil {
		var err error
		calc, err = metrics.NewCalculator(opts.PricingOverridePath)
		if err != nil {
			return nil, fmt.Errorf("init pricing calculator: %w", err)
		}
	}

	discoverFn := opts.DiscoverRoots
	if discoverFn == nil {
		discoverFn = DiscoverRootDirs
	}

	var initialRoots []string
	autoDiscover := false
	switch {
	case len(opts.RootDirs) > 0:
		initialRoots = dedupeRootDirs(opts.RootDirs)
	case strings.TrimSpace(opts.RootDir) != "":
		initialRoots = dedupeRootDirs([]string{opts.RootDir})
	default:
		autoDiscover = true
		initialRoots = dedupeRootDirs(discoverFn())
	}
	if len(initialRoots) == 0 {
		initialRoots = []string{DefaultRootDir()}
	}

	baseCtx, cancelBase := context.WithCancel(context.Background())

	p := &Provider{
		opts:                 opts,
		calc:                 calc,
		autoDiscover:         autoDiscover,
		adaptivePoll:         adaptivePoll,
		discoverFunc:         discoverFn,
		rootsByKey:           make(map[string]*rootSource, len(initialRoots)),
		convRoot:             make(map[string]*rootSource),
		resolvedRoot:         make(map[string]*rootSource),
		tailers:              make(map[string]*TranscriptTailer),
		subs:                 make(map[int]chan model.AppSnapshot),
		pinnedBySlot:         make(map[string]string),
		hydratedSet:          make(map[string]bool),
		watchedSet:           make(map[string]bool),
		baseCtx:              baseCtx,
		cancelBase:           cancelBase,
		triggerCh:            make(chan watchEvent, 64),
		hydrateCh:            make(chan struct{}, 1),
		stopCh:               make(chan struct{}),
		doneCh:               make(chan struct{}),
		onDemandTimeout:      5 * time.Second,
		lastRootDiscover:     time.Now(),
		rootDiscoverInterval: defaultRootDiscoverEvery,
	}

	for _, dir := range initialRoots {
		p.addRootLocked(dir)
	}

	// Perform initial synchronous refresh so Snapshot() is immediately ready.
	// When AsyncInit is enabled with the background loop active, synchronously
	// hydrate only the top-1 active conversation for instant first frame and let
	// runLoop finish remaining background trees asynchronously.
	initialHydrated := 0
	if opts.AsyncInit && !opts.DisableWatcher {
		initialHydrated = 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = p.refreshInternal(ctx, false, initialHydrated)
	cancel()

	if !opts.DisableWatcher {
		p.hasLoop = true
		go p.runLoop()
	}

	return p, nil
}

func (p *Provider) addRootLocked(rootDir string) bool {
	clean := filepath.Clean(strings.TrimSpace(rootDir))
	if clean == "" {
		return false
	}
	key := normalizeRootKey(clean)
	if _, exists := p.rootsByKey[key]; exists {
		return false
	}
	rs := &rootSource{
		key:       key,
		rootDir:   clean,
		summaries: NewSummariesReader(filepath.Join(clean, "conversation_summaries.db")),
		convPool:  NewConvDBPool(filepath.Join(clean, "conversations"), p.opts.MaxOpenDBs),
	}
	if !p.opts.DisableWatcher {
		if w, err := newDirWatcher(clean, p.opts.Debounce, p.enqueueEvent); err == nil {
			rs.watcher = w
		}
	}
	p.roots = append(p.roots, rs)
	p.rootsByKey[key] = rs
	clear(p.resolvedRoot)
	return true
}

func (p *Provider) syncDiscoveredRootsLocked() bool {
	if !p.autoDiscover || p.discoverFunc == nil {
		return false
	}
	added := false
	for _, dir := range dedupeRootDirs(p.discoverFunc()) {
		if p.addRootLocked(dir) {
			added = true
		}
	}
	return added
}

// Snapshot implements model.SnapshotProvider without blocking on I/O.
func (p *Provider) Snapshot() model.AppSnapshot {
	p.snapMu.RLock()
	defer p.snapMu.RUnlock()
	return p.snapshot
}

// PinConversation implements model.ConversationHydrator, binding a consumer slot
// (such as "desktop" or "tray") to convID so it stays hydrated and watched without
// accumulating stale pins or overwriting other consumers. Passing an empty convID
// releases the slot's pin. Always non-blocking; safe to call from UI threads.
func (p *Provider) PinConversation(slot, convID string) {
	slot = strings.TrimSpace(slot)
	if slot == "" {
		slot = "default"
	}
	id := strings.TrimSpace(convID)

	p.pinMu.Lock()
	if p.stopped {
		p.pinMu.Unlock()
		return
	}
	if p.pinnedBySlot == nil {
		p.pinnedBySlot = make(map[string]string)
	}
	prevID, hadPrev := p.pinnedBySlot[slot]
	slotChanged := false
	if id == "" {
		if hadPrev {
			delete(p.pinnedBySlot, slot)
			slotChanged = true
		}
	} else if !hadPrev || prevID != id {
		p.pinnedBySlot[slot] = id
		slotChanged = true
	}

	needSync := slotChanged
	if id != "" {
		if !p.hydratedSet[id] || (p.hasLoop && !p.watchedSet[id]) {
			needSync = true
		}
	}
	if !needSync {
		p.pinMu.Unlock()
		return
	}
	p.pinGen++

	if !p.hasLoop {
		if !p.onDemandActive {
			p.onDemandActive = true
			p.onDemandWG.Add(1)
			go p.runOnDemandHydrate()
		}
		p.pinMu.Unlock()
		return
	}
	p.pinMu.Unlock()

	select {
	case p.hydrateCh <- struct{}{}:
	default:
	}
}

func (p *Provider) runOnDemandHydrate() {
	defer p.onDemandWG.Done()
	for {
		p.pinMu.Lock()
		if p.stopped || p.appliedPinGen == p.pinGen {
			p.onDemandActive = false
			p.pinMu.Unlock()
			return
		}
		attemptGen := p.pinGen
		beforeApplied := p.appliedPinGen
		timeout := p.onDemandTimeout
		p.pinMu.Unlock()

		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		ctx, cancel := context.WithTimeout(p.baseCtx, timeout)
		err := p.refreshInternal(ctx, false, 0)
		cancel()

		p.pinMu.Lock()
		if p.stopped {
			p.onDemandActive = false
			p.pinMu.Unlock()
			return
		}
		// If a newer PinConversation arrived while this attempt was running (even if this
		// attempt timed out or failed), continue immediately so the new selection is not dropped.
		if p.pinGen != attemptGen {
			p.pinMu.Unlock()
			continue
		}
		if err != nil || p.appliedPinGen == p.pinGen || p.appliedPinGen == beforeApplied {
			p.onDemandActive = false
			p.pinMu.Unlock()
			return
		}
		p.pinMu.Unlock()
	}
}

// Subscribe implements model.SnapshotProvider with latest-wins coalescing semantics.
func (p *Provider) Subscribe() (<-chan model.AppSnapshot, func()) {
	p.subMu.Lock()
	id := p.nextSub
	p.nextSub++
	ch := make(chan model.AppSnapshot, 1)
	if !p.closed {
		ch <- p.Snapshot()
		p.subs[id] = ch
	} else {
		close(ch)
	}
	p.subMu.Unlock()

	cancel := func() {
		p.subMu.Lock()
		defer p.subMu.Unlock()
		if existing, ok := p.subs[id]; ok {
			delete(p.subs, id)
			close(existing)
		}
	}
	return ch, cancel
}

// Close implements model.SnapshotProvider, stopping watchers and releasing all read-only DB handles.
func (p *Provider) Close() error {
	p.subMu.Lock()
	if p.closed {
		p.subMu.Unlock()
		return nil
	}
	p.closed = true
	close(p.stopCh)
	if p.cancelBase != nil {
		p.cancelBase()
	}
	for id, ch := range p.subs {
		delete(p.subs, id)
		close(ch)
	}
	p.subMu.Unlock()

	p.pinMu.Lock()
	p.stopped = true
	p.pinMu.Unlock()

	if p.hasLoop {
		<-p.doneCh
	}
	p.onDemandWG.Wait()

	p.syncMu.Lock()
	defer p.syncMu.Unlock()

	var firstErr error
	for _, rs := range p.roots {
		if rs.watcher != nil {
			_ = rs.watcher.Close()
		}
		if err := rs.convPool.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := rs.summaries.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// OpenConvDBHandles returns the number of currently open L2 SQLite handles across all roots (for diagnostics/tests).
func (p *Provider) OpenConvDBHandles() int {
	p.syncMu.Lock()
	defer p.syncMu.Unlock()
	total := 0
	for _, rs := range p.roots {
		total += rs.convPool.OpenHandleCount()
	}
	return total
}

// Refresh forces an immediate L1 + L2 + L3 incremental sync across all roots and broadcasts the updated snapshot.
func (p *Provider) Refresh(ctx context.Context) error {
	return p.refreshInternal(ctx, true, 0)
}

func (p *Provider) refreshInternal(ctx context.Context, discoverRoots bool, maxHydratedOverride int) error {
	p.syncMu.Lock()
	defer p.syncMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	anyChanged := false
	if discoverRoots {
		anyChanged = p.syncDiscoveredRootsLocked()
		p.lastRootDiscover = time.Now()
	}
	var firstErr error
	succeeded := 0

	for _, rs := range p.roots {
		rows, changed, err := rs.summaries.ReadChanges(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		succeeded++
		if changed {
			rs.rows = rows
			anyChanged = true
		}
	}

	if succeeded == 0 && firstErr != nil && len(p.trees) == 0 {
		return firstErr
	}

	if anyChanged {
		p.snapshotDirty = true
		merged := p.mergeSummaryRowsLocked()
		if len(merged) > 0 || len(p.trees) > 0 {
			p.updateTrees(merged)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	maxHydrated := p.opts.MaxHydratedTrees
	strictLimit := false
	if maxHydratedOverride > 0 && maxHydratedOverride < maxHydrated {
		maxHydrated = maxHydratedOverride
		strictLimit = true
	}
	isFull := !strictLimit
	forceRebuild := !p.hasSnapshot || p.snapshotDirty

	snap, treeChanged := p.buildSnapshotLocked(ctx, maxHydrated, strictLimit, forceRebuild)
	if isFull && ctx.Err() == nil {
		p.fullHydrated = true
	}
	if !forceRebuild && !treeChanged {
		return ctx.Err()
	}

	p.hasSnapshot = true
	p.snapshotDirty = false

	p.snapMu.Lock()
	p.snapshot = snap
	p.snapMu.Unlock()

	p.broadcast(snap)
	return ctx.Err()
}

func (p *Provider) enqueueEvent(ev watchEvent) {
	select {
	case <-p.stopCh:
		return
	case p.triggerCh <- ev:
	default:
		// Channel full: refresh is already queued.
	}
}

func (p *Provider) nextPollInterval() time.Duration {
	if !p.adaptivePoll {
		return p.opts.PollInterval
	}
	if p.Snapshot().RunningCount > 0 {
		return defaultPollInterval
	}
	return defaultIdlePollInterval
}

func (p *Provider) runLoop() {
	defer close(p.doneCh)

	// Finish full hydration asynchronously after AsyncInit fast first frame.
	p.syncMu.Lock()
	needFullHydrate := !p.fullHydrated
	p.syncMu.Unlock()
	if needFullHydrate {
		select {
		case <-p.stopCh:
			return
		default:
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = p.refreshInternal(ctx, false, 0)
			cancel()
		}
	}

	var timer *time.Timer
	var tickCh <-chan time.Time
	resetTimer := func() {
		if p.opts.PollInterval <= 0 {
			return
		}
		d := p.nextPollInterval()
		if timer == nil {
			timer = time.NewTimer(d)
			tickCh = timer.C
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d)
	}
	resetTimer()
	if timer != nil {
		defer timer.Stop()
	}

	shouldDiscoverRoots := func() bool {
		if !p.autoDiscover {
			return false
		}
		p.syncMu.Lock()
		interval := p.rootDiscoverInterval
		if interval <= 0 {
			interval = defaultRootDiscoverEvery
		}
		due := time.Since(p.lastRootDiscover) >= interval
		p.syncMu.Unlock()
		return due
	}

	for {
		select {
		case <-p.stopCh:
			return
		case <-p.hydrateCh:
			p.pinMu.Lock()
			needSync := p.appliedPinGen != p.pinGen
			if !needSync {
				for _, id := range p.pinnedBySlot {
					if id != "" && (!p.hydratedSet[id] || !p.watchedSet[id]) {
						needSync = true
						break
					}
				}
			}
			p.pinMu.Unlock()
			if !needSync {
				continue
			}
			ctx, cancel := context.WithTimeout(p.baseCtx, 10*time.Second)
			_ = p.refreshInternal(ctx, false, 0)
			cancel()
			resetTimer()
		case <-p.triggerCh:
			// Drain any burst of already-queued debounced triggers before rebuilding once.
		drain:
			for {
				select {
				case <-p.triggerCh:
				default:
					break drain
				}
			}
			discover := shouldDiscoverRoots()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = p.refreshInternal(ctx, discover, 0)
			cancel()
			resetTimer()
		case <-tickCh:
			discover := shouldDiscoverRoots()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = p.refreshInternal(ctx, discover, 0)
			cancel()
			resetTimer()
		}
	}
}

func (p *Provider) broadcast(snap model.AppSnapshot) {
	p.subMu.Lock()
	defer p.subMu.Unlock()
	if p.closed {
		return
	}
	for _, ch := range p.subs {
		select {
		case ch <- snap:
		default:
			// Coalesce stale frame for slow consumer (latest-wins).
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snap:
			default:
			}
		}
	}
}

func (p *Provider) updateTrees(rows []SummaryRow) {
	previous := make(map[string]*cachedTree, len(p.trees))
	for i := range p.trees {
		previous[p.trees[i].tree.Root.ConversationID] = &p.trees[i]
	}
	trees := BuildConversationTrees(rows)
	if len(trees) > p.opts.MaxConversations {
		trees = trees[:p.opts.MaxConversations]
	}
	p.trees = make([]cachedTree, len(trees))
	for i, tree := range trees {
		if old := previous[tree.Root.ConversationID]; old != nil && reflect.DeepEqual(old.tree, tree) {
			p.trees[i] = *old
		} else {
			p.trees[i].tree = tree
		}
	}
}

func (p *Provider) mergeSummaryRowsLocked() []SummaryRow {
	type candidate struct {
		row  SummaryRow
		root *rootSource
	}

	helperIDs := make(map[string]bool)
	byID := make(map[string]candidate)
	order := make([]string, 0)

	for _, rs := range p.roots {
		for _, row := range rs.rows {
			if row.ConversationID == "" {
				continue
			}
			if row.IsInternalHelperSession() {
				helperIDs[row.ConversationID] = true
				continue
			}
			existing, ok := byID[row.ConversationID]
			if !ok {
				byID[row.ConversationID] = candidate{row: row, root: rs}
				order = append(order, row.ConversationID)
				continue
			}
			if preferSummaryCandidate(row, rs.rootDir, existing.row, existing.root.rootDir) {
				byID[row.ConversationID] = candidate{
					row:  mergeSummaryMetadata(row, existing.row),
					root: rs,
				}
			} else {
				byID[row.ConversationID] = candidate{
					row:  mergeSummaryMetadata(existing.row, row),
					root: existing.root,
				}
			}
		}
	}

	nextConvRoot := make(map[string]*rootSource, len(byID))
	merged := make([]SummaryRow, 0, len(byID))
	for _, id := range order {
		if helperIDs[id] {
			continue
		}
		cand := byID[id]
		if cand.row.ParentConversationID != "" && helperIDs[cand.row.ParentConversationID] {
			continue
		}
		nextConvRoot[id] = cand.root
		merged = append(merged, cand.row)
	}
	p.convRoot = nextConvRoot
	clear(p.resolvedRoot)
	return merged
}

func preferSummaryCandidate(cand SummaryRow, candRoot string, cur SummaryRow, curRoot string) bool {
	candHasLocal, candLocalMtime := localConvFreshness(candRoot, cand.ConversationID)
	curHasLocal, curLocalMtime := localConvFreshness(curRoot, cur.ConversationID)
	if candHasLocal != curHasLocal {
		return candHasLocal
	}
	if cand.IsRunning() != cur.IsRunning() {
		return cand.IsRunning()
	}
	if candHasLocal && candLocalMtime != curLocalMtime {
		return candLocalMtime > curLocalMtime
	}
	if !cand.LastModifiedTime.Equal(cur.LastModifiedTime) {
		return cand.LastModifiedTime.After(cur.LastModifiedTime)
	}
	return cand.StepCount > cur.StepCount
}

func mergeSummaryMetadata(primary, fallback SummaryRow) SummaryRow {
	if strings.TrimSpace(primary.Title) == "" {
		primary.Title = fallback.Title
	}
	if strings.TrimSpace(primary.Preview) == "" {
		primary.Preview = fallback.Preview
	}
	if primary.Workspace == "" {
		primary.Workspace = fallback.Workspace
		primary.WorkspaceURIs = fallback.WorkspaceURIs
	}
	if primary.ParentConversationID == "" {
		primary.ParentConversationID = fallback.ParentConversationID
	}
	if primary.AgentName == "" {
		primary.AgentName = fallback.AgentName
	}
	if primary.LastUserInputTime.IsZero() {
		primary.LastUserInputTime = fallback.LastUserInputTime
	}
	if primary.LastUserInputStepIndex < 0 && fallback.LastUserInputStepIndex >= 0 {
		primary.LastUserInputStepIndex = fallback.LastUserInputStepIndex
	}
	return primary
}

func localConvFreshness(rootDir, convID string) (bool, int64) {
	dbPath := filepath.Join(rootDir, "conversations", convID+".db")
	if fi, err := os.Stat(dbPath); err == nil {
		mtime := fi.ModTime().UnixNano()
		if walFi, err := os.Stat(dbPath + "-wal"); err == nil && walFi.Size() > 0 {
			if wm := walFi.ModTime().UnixNano(); wm > mtime {
				mtime = wm
			}
		}
		return true, mtime
	}
	trPath := TranscriptPath(rootDir, convID)
	if fi, err := os.Stat(trPath); err == nil {
		return true, fi.ModTime().UnixNano()
	}
	return false, 0
}

func (p *Provider) rootForConvLocked(convID string) *rootSource {
	if rs := p.convRoot[convID]; rs != nil {
		return rs
	}
	if len(p.roots) > 0 {
		return p.roots[0]
	}
	return nil
}

func (p *Provider) resolveStorageRootLocked(convID string) *rootSource {
	primary := p.rootForConvLocked(convID)
	if primary == nil || len(p.roots) <= 1 {
		return primary
	}
	var staleCached *rootSource
	if cached, ok := p.resolvedRoot[convID]; ok {
		if hasLocal, _ := localConvFreshness(cached.rootDir, convID); hasLocal {
			return cached
		}
		// Local files disappeared from the previously cached root; invalidate and relocate.
		delete(p.resolvedRoot, convID)
		staleCached = cached
	}
	if primary != staleCached {
		if hasLocal, _ := localConvFreshness(primary.rootDir, convID); hasLocal {
			p.resolvedRoot[convID] = primary
			return primary
		}
	}
	var best *rootSource
	var bestMtime int64
	for _, rs := range p.roots {
		if rs == primary || rs == staleCached {
			continue
		}
		if ok, mtime := localConvFreshness(rs.rootDir, convID); ok {
			if best == nil || mtime > bestMtime {
				best = rs
				bestMtime = mtime
			}
		}
	}
	if best != nil {
		p.convRoot[convID] = best
		p.resolvedRoot[convID] = best
		return best
	}
	// Do not cache primary when no root has local data yet; a conversation DB or
	// transcript may be created in another root on a subsequent refresh.
	return primary
}

func (p *Provider) syncTranscript(convID string) bool {
	rs := p.rootForConvLocked(convID)
	if rs == nil {
		return false
	}
	wantPath := TranscriptPath(rs.rootDir, convID)
	tailer := p.tailers[convID]
	switched := false
	if tailer == nil || tailer.path != wantPath {
		if tailer != nil {
			switched = true
		}
		tailer = NewTranscriptTailer(wantPath)
		p.tailers[convID] = tailer
	}
	transcriptChanged, _ := tailer.Sync()
	return transcriptChanged || switched
}

func (p *Provider) pruneTailers(keepIDs map[string]bool) {
	for id := range p.tailers {
		if !keepIDs[id] {
			delete(p.tailers, id)
		}
	}
}

func (p *Provider) buildSnapshotLocked(ctx context.Context, maxHydrated int, strictHydrateLimit bool, forceRebuild bool) (model.AppSnapshot, bool) {
	keepIDs := make(map[string]bool, len(p.trees)*2)
	activeIDs := make(map[string]bool)
	hydratedByRoot := make(map[*rootSource][]string, len(p.roots))
	keepByRoot := make(map[*rootSource]map[string]bool, len(p.roots))
	watchByRoot := make(map[*rootSource]map[string]bool, len(p.roots))
	hydratedCount := 0

	p.pinMu.Lock()
	startPinGen := p.pinGen
	pinnedSlots := make(map[string]string, len(p.pinnedBySlot))
	pinned := make(map[string]bool, len(p.pinnedBySlot))
	for slot, id := range p.pinnedBySlot {
		if id != "" {
			pinnedSlots[slot] = id
			pinned[id] = true
		}
	}
	p.pinMu.Unlock()

	addHydrated := func(id string, active bool, watchTranscript bool) {
		keepIDs[id] = true
		if active {
			activeIDs[id] = true
		}
		rs := p.resolveStorageRootLocked(id)
		if rs == nil {
			return
		}
		hydratedByRoot[rs] = append(hydratedByRoot[rs], id)
		rk := keepByRoot[rs]
		if rk == nil {
			rk = make(map[string]bool)
			keepByRoot[rs] = rk
		}
		rk[id] = true
		if watchTranscript {
			rw := watchByRoot[rs]
			if rw == nil {
				rw = make(map[string]bool)
				watchByRoot[rs] = rw
			}
			rw[id] = true
		}
	}

	treeIDs := make(map[string]bool, len(p.trees))
	wantWatchRoots := make(map[string]bool, len(p.trees))
	for i, cached := range p.trees {
		tree := cached.tree
		rootID := tree.Root.ConversationID
		treeIDs[rootID] = true
		treeRunning := isTreeRunning(tree)
		treePinned := pinned[rootID]
		if hydratedCount >= maxHydrated && (strictHydrateLimit || (!treeRunning && !treePinned)) {
			continue
		}
		hydratedCount++
		watchTranscript := treeRunning || treePinned || i < defaultWatchTranscriptTrees
		if watchTranscript {
			wantWatchRoots[rootID] = true
		}
		addHydrated(rootID, tree.Root.IsRunning(), watchTranscript)
		for _, child := range tree.Children {
			addHydrated(child.ConversationID, child.IsRunning(), watchTranscript)
		}
	}

	// Register fsnotify transcript watches BEFORE reading L2/L3 so no transcript append
	// occurring between read and watch registration can be missed. Only mark a tree as
	// watched when its transcript directory (and any child dirs) actually registered.
	actuallyWatchedIDs := make(map[string]bool)
	for _, rs := range p.roots {
		if rs.watcher != nil {
			for id := range rs.watcher.WatchTranscriptDirs(watchByRoot[rs]) {
				actuallyWatchedIDs[id] = true
			}
		}
	}
	watchedRoots := make(map[string]bool, len(wantWatchRoots))
	for _, cached := range p.trees {
		tree := cached.tree
		rootID := tree.Root.ConversationID
		if !wantWatchRoots[rootID] || !actuallyWatchedIDs[rootID] {
			continue
		}
		allChildrenWatched := true
		for _, child := range tree.Children {
			if !actuallyWatchedIDs[child.ConversationID] {
				allChildrenWatched = false
				break
			}
		}
		if allChildrenWatched {
			watchedRoots[rootID] = true
		}
	}

	commitPinState := func(pruneCache bool, ackPins bool) {
		hydratedRoots := make(map[string]bool, len(p.trees))
		for i := range p.trees {
			if p.trees[i].hydrated {
				hydratedRoots[p.trees[i].tree.Root.ConversationID] = true
			}
		}
		p.pinMu.Lock()
		for slot, id := range pinnedSlots {
			if !treeIDs[id] && p.pinnedBySlot[slot] == id {
				delete(p.pinnedBySlot, slot)
			}
		}
		p.hydratedSet = hydratedRoots
		p.watchedSet = watchedRoots
		if ackPins && startPinGen > p.appliedPinGen {
			p.appliedPinGen = startPinGen
		}
		p.pinMu.Unlock()

		if pruneCache {
			for _, rs := range p.roots {
				rs.convPool.PruneCache(keepByRoot[rs])
			}
		}
	}

	dbChanges := make(map[string]bool)
	dbFailed := make(map[string]bool)
	for rs, ids := range hydratedByRoot {
		chMap, failMap := rs.convPool.syncBatch(ctx, ids, activeIDs)
		for id, ch := range chMap {
			if ch {
				dbChanges[id] = true
			}
		}
		for id := range failMap {
			dbFailed[id] = true
		}
	}

	anyTreeChanged := false
	pinnedFailed := false
	for i := range p.trees {
		cached := &p.trees[i]
		tree := cached.tree
		rootID := tree.Root.ConversationID
		hydrate := keepIDs[rootID]
		treeFailed := false
		if hydrate {
			if dbFailed[rootID] {
				treeFailed = true
			}
			for _, child := range tree.Children {
				if dbFailed[child.ConversationID] {
					treeFailed = true
					break
				}
			}
			if treeFailed && pinned[rootID] {
				pinnedFailed = true
			}
		}

		dataChanged := false
		if hydrate {
			dataChanged = p.syncTranscript(rootID) || dbChanges[rootID]
			for _, child := range tree.Children {
				id := child.ConversationID
				dataChanged = p.syncTranscript(id) || dbChanges[id] || dataChanged
			}
		}

		// Always rebuild when visible data changed, when the snapshot has never been built,
		// when un-hydrating, or when transitioning cleanly to hydrated without DB errors.
		// When L2 fails, still surface any available L3 transcript or healthy sibling data,
		// while keeping cached.hydrated=false so retries remain enabled.
		stateChanged := cached.snapshot.ID == "" ||
			(cached.builtHydrated != hydrate && (!hydrate || !treeFailed))
		if dataChanged || stateChanged {
			cached.snapshot = p.buildConversationSnapshot(tree, hydrate)
			if hydrate {
				cached.builtHydrated = !treeFailed || dataChanged
				cached.hydrated = !treeFailed
			} else {
				cached.builtHydrated = false
				cached.hydrated = false
			}
			anyTreeChanged = true
		} else if hydrate && treeFailed && cached.hydrated {
			// Clear the clean-hydrated flag on subsequent L2 read failures while preserving
			// the last good cached snapshot so re-selecting the session retries cleanly.
			cached.hydrated = false
		} else if hydrate && !treeFailed && !cached.hydrated {
			// DB recovered with no new rows; mark cleanly hydrated without spurious broadcast.
			cached.hydrated = true
			cached.builtHydrated = true
		}
	}

	ackPins := ctx.Err() == nil && !pinnedFailed
	if !anyTreeChanged && !forceRebuild {
		commitPinState(false, ackPins)
		return model.AppSnapshot{}, false
	}

	snap := model.AppSnapshot{
		GeneratedAt:   time.Now().UTC(),
		Conversations: make([]model.ConversationSnapshot, 0, len(p.trees)),
	}
	for i := range p.trees {
		convSnap := p.trees[i].snapshot
		if convSnap.Running {
			snap.RunningCount++
		}
		snap.Conversations = append(snap.Conversations, convSnap)
	}

	// Re-sort snapshots cleanly by Running DESC, LastUpdated DESC (hydration may refine LastUpdated).
	sort.SliceStable(snap.Conversations, func(i, j int) bool {
		if snap.Conversations[i].Running != snap.Conversations[j].Running {
			return snap.Conversations[i].Running
		}
		if !snap.Conversations[i].LastUpdated.Equal(snap.Conversations[j].LastUpdated) {
			return snap.Conversations[i].LastUpdated.After(snap.Conversations[j].LastUpdated)
		}
		return snap.Conversations[i].ID < snap.Conversations[j].ID
	})

	if len(snap.Conversations) > 0 {
		snap.ActiveConversationID = snap.Conversations[0].ID
	}

	commitPinState(true, ackPins)
	p.pruneTailers(keepIDs)

	return snap, true
}

func (p *Provider) buildConversationSnapshot(tree ConversationTree, hydrate bool) model.ConversationSnapshot {
	rootSum := tree.Root
	running := isTreeRunning(tree)
	lastUpdated := rootSum.LastModifiedTime
	for _, c := range tree.Children {
		if c.LastModifiedTime.After(lastUpdated) {
			lastUpdated = c.LastModifiedTime
		}
	}

	if !hydrate {
		rootNode := model.AgentNode{
			ID:     model.RootAgentID,
			Name:   "主 Agent",
			Role:   "Coordinator",
			Active: rootSum.IsRunning(),
		}
		return model.ConversationSnapshot{
			ID:            rootSum.ConversationID,
			Title:         rootSum.DisplayTitle(),
			Workspace:     rootSum.Workspace,
			Running:       running,
			LastUpdated:   lastUpdated,
			ActiveAgentID: model.RootAgentID,
			Root:          rootNode,
			PeakContext:   model.ComputePeakContext(rootNode, nil),
		}
	}

	var rootGens []GenMetadata
	var rootStepTimes map[int]time.Time
	if rs := p.rootForConvLocked(rootSum.ConversationID); rs != nil {
		rootGens, rootStepTimes = rs.convPool.cachedState(rootSum.ConversationID)
	}
	var rootTr TranscriptData
	if tailer := p.tailers[rootSum.ConversationID]; tailer != nil {
		rootTr = tailer.data()
	}

	rootLastUserTime := rootTr.LastUserInputTime()
	if rootLastUserTime.IsZero() {
		rootLastUserTime = rootSum.LastUserInputTime
	}

	// Collect subagent naming descriptors from the synchronized L2 and L3 data.
	type childHydrated struct {
		sum       SummaryRow
		gens      []GenMetadata
		stepTimes map[int]time.Time
		tr        TranscriptData
	}
	childrenData := make([]childHydrated, len(tree.Children))
	descriptors := make([]SubagentDescriptor, len(tree.Children))

	for i, child := range tree.Children {
		var gens []GenMetadata
		var stepTimes map[int]time.Time
		if rs := p.rootForConvLocked(child.ConversationID); rs != nil {
			gens, stepTimes = rs.convPool.cachedState(child.ConversationID)
		}
		var tr TranscriptData
		if tailer := p.tailers[child.ConversationID]; tailer != nil {
			tr = tailer.data()
		}
		childrenData[i] = childHydrated{
			sum:       child,
			gens:      gens,
			stepTimes: stepTimes,
			tr:        tr,
		}
		subType := ""
		for j := len(gens) - 1; j >= 0; j-- {
			if gens[j].SubagentType != "" {
				subType = gens[j].SubagentType
				break
			}
		}
		descriptors[i] = SubagentDescriptor{
			ID:               child.ConversationID,
			SummaryAgentName: child.AgentName,
			SubagentType:     subType,
		}
	}

	// Resolve 3-tier subagent names (Role -> agent_name -> #1/#2 disambiguation).
	// Also merge any subagent roles discovered in nested subagent transcripts.
	mergedRoles := make(map[string]string, len(rootTr.SubagentRoles))
	for k, v := range rootTr.SubagentRoles {
		mergedRoles[k] = v
	}
	for _, ch := range childrenData {
		for k, v := range ch.tr.SubagentRoles {
			if _, exists := mergedRoles[k]; !exists {
				mergedRoles[k] = v
			}
		}
	}
	resolvedNames := ResolveSubagentNames(descriptors, mergedRoles)

	// Convert Root telemetry to RequestRecords.
	rootReqs := p.buildAgentRecords(
		rootSum,
		model.RootAgentID,
		"主 Agent",
		rootGens,
		rootStepTimes,
		rootTr,
		time.Time{},
		0,
		true,
	)

	rootTurnCount := len(rootTr.UserInputSteps)
	if rootTurnCount == 0 {
		rootTurnCount = 1
	}

	allReqs := make([]model.RequestRecord, 0, len(rootReqs)+len(childrenData)*4)
	allReqs = append(allReqs, rootReqs...)

	subNodes := make([]model.AgentNode, 0, len(childrenData))
	var activeSubID string
	var activeSubTime time.Time

	for _, ch := range childrenData {
		res := resolvedNames[ch.sum.ConversationID]
		agentReqs := p.buildAgentRecords(
			ch.sum,
			ch.sum.ConversationID,
			res.Name,
			ch.gens,
			ch.stepTimes,
			ch.tr,
			rootLastUserTime,
			rootTurnCount,
			false,
		)
		allReqs = append(allReqs, agentReqs...)

		parentAgentID := model.RootAgentID
		if ch.sum.ParentConversationID != "" && ch.sum.ParentConversationID != rootSum.ConversationID {
			parentAgentID = ch.sum.ParentConversationID
		}

		subNode := p.buildAgentNode(
			ch.sum.ConversationID,
			parentAgentID,
			res.Name,
			res.Role,
			ch.sum.IsRunning(),
			agentReqs,
		)
		subNodes = append(subNodes, subNode)

		if subNode.Active {
			t := ch.sum.LastModifiedTime
			if subNode.Metrics.LatestRequest != nil && subNode.Metrics.LatestRequest.Timestamp.After(t) {
				t = subNode.Metrics.LatestRequest.Timestamp
			}
			if activeSubID == "" || t.After(activeSubTime) {
				activeSubID = subNode.ID
				activeSubTime = t
			}
		}
	}

	// Chronologically order all requests across the conversation tree and assign Seq 1..N.
	sort.SliceStable(allReqs, func(i, j int) bool {
		ti, tj := allReqs[i].Timestamp, allReqs[j].Timestamp
		if !ti.Equal(tj) {
			if ti.IsZero() {
				return false
			}
			if tj.IsZero() {
				return true
			}
			return ti.Before(tj)
		}
		if allReqs[i].ConversationID != allReqs[j].ConversationID {
			if allReqs[i].AgentID == model.RootAgentID {
				return true
			}
			if allReqs[j].AgentID == model.RootAgentID {
				return false
			}
			return allReqs[i].ConversationID < allReqs[j].ConversationID
		}
		return allReqs[i].GenIndex < allReqs[j].GenIndex
	})
	for i := range allReqs {
		allReqs[i].Seq = i + 1
	}

	// Re-partition ordered (Seq-assigned) requests per agent so LatestRequest.Seq is consistent.
	byAgent := make(map[string][]model.RequestRecord, 1+len(subNodes))
	for _, r := range allReqs {
		byAgent[r.AgentID] = append(byAgent[r.AgentID], r)
	}

	rootNode := p.buildAgentNode(
		model.RootAgentID,
		"",
		"主 Agent",
		"Coordinator",
		rootSum.IsRunning(),
		byAgent[model.RootAgentID],
	)
	for i := range subNodes {
		s := subNodes[i]
		subNodes[i] = p.buildAgentNode(s.ID, s.ParentID, s.Name, s.Role, s.Active, byAgent[s.ID])
	}

	treeMetrics := model.AggregateScope(allReqs)
	subCost := treeMetrics.SessionCostUSD - rootNode.Metrics.SessionCostUSD
	if subCost < 0 {
		subCost = 0
	}

	if treeMetrics.LatestRequest != nil && treeMetrics.LatestRequest.Timestamp.After(lastUpdated) {
		lastUpdated = treeMetrics.LatestRequest.Timestamp
	}

	activeAgentID := model.RootAgentID
	if activeSubID != "" {
		activeAgentID = activeSubID
	} else if !rootNode.Active && treeMetrics.LatestRequest != nil && treeMetrics.LatestRequest.AgentID != "" {
		activeAgentID = treeMetrics.LatestRequest.AgentID
	}

	return model.ConversationSnapshot{
		ID:               rootSum.ConversationID,
		Title:            rootSum.DisplayTitle(),
		Workspace:        rootSum.Workspace,
		Running:          running,
		LastUpdated:      lastUpdated,
		ActiveAgentID:    activeAgentID,
		Root:             rootNode,
		Subagents:        subNodes,
		TreeMetrics:      treeMetrics,
		SubagentsCostUSD: subCost,
		PeakContext:      model.ComputePeakContext(rootNode, subNodes),
		Requests:         allReqs,
	}
}

func (p *Provider) buildAgentRecords(
	sum SummaryRow,
	agentID string,
	agentName string,
	gens []GenMetadata,
	stepTimes map[int]time.Time,
	tr TranscriptData,
	rootLastUserTime time.Time,
	rootTurnCount int,
	isRoot bool,
) []model.RequestRecord {
	var records []model.RequestRecord

	if len(gens) > 0 {
		records = make([]model.RequestRecord, 0, len(gens))
		for _, g := range gens {
			if g.IsEmptyFailure() {
				continue
			}

			rec := model.RequestRecord{
				GenIndex:         g.GenIndex,
				StepIndex:        g.StepIndex,
				ConversationID:   sum.ConversationID,
				AgentID:          agentID,
				AgentName:        agentName,
				ModelSlug:        g.ModelSlug,
				ModelDisplay:     g.ModelDisplay,
				InputTokens:      g.InputTokens,
				CacheReadTokens:  g.CacheReadTokens,
				CacheWriteTokens: g.CacheWriteTokens,
				OutputTokens:     g.OutputTokens,
				ThinkingTokens:   g.ThinkingTokens,
				ResponseTokens:   g.ResponseTokens,
				ContextUsed:      g.ContextUsed,
				ContextLimit:     g.ContextLimit,
				ContextSegments:  g.ContextSegments,
				TTFT:             g.TTFT,
				TotalDuration:    g.TotalDuration,
			}

			// Timestamp priority: L2 step metadata (ns precision) -> L3 transcript -> L1 summary mtime.
			if ts, ok := stepTimes[g.StepIndex]; ok && !ts.IsZero() {
				rec.Timestamp = ts
			} else if ts, ok := tr.StepTimestamps[g.StepIndex]; ok && !ts.IsZero() {
				rec.Timestamp = ts
			} else {
				rec.Timestamp = sum.LastModifiedTime
			}

			// Graceful L3 fallback if L2 Protobuf omitted token fields or model slug.
			if pl, ok := tr.PlannersByStep[g.StepIndex]; ok {
				if rec.InputTokens == 0 && rec.OutputTokens == 0 && rec.CacheReadTokens == 0 {
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

			// Turn alignment.
			if isRoot {
				rec.TurnIndex, rec.InCurrentTurn = tr.TurnForStep(rec.StepIndex, sum.LastUserInputStepIndex)
			} else {
				if !rootLastUserTime.IsZero() && !rec.Timestamp.IsZero() {
					rec.InCurrentTurn = !rec.Timestamp.Before(rootLastUserTime)
					if rec.InCurrentTurn {
						rec.TurnIndex = rootTurnCount
					} else {
						rec.TurnIndex = 1
					}
				} else {
					rec.TurnIndex, rec.InCurrentTurn = tr.TurnForStep(rec.StepIndex, sum.LastUserInputStepIndex)
				}
			}

			p.calc.EnrichRecord(&rec)
			records = append(records, rec)
		}
		return records
	}

	// Full L3 fallback when L2 conversations/<id>.db is missing or empty.
	if len(tr.Planners) == 0 {
		return nil
	}
	records = make([]model.RequestRecord, 0, len(tr.Planners))
	for i, pl := range tr.Planners {
		rec := model.RequestRecord{
			GenIndex:        i,
			StepIndex:       pl.StepIndex,
			ConversationID:  sum.ConversationID,
			AgentID:         agentID,
			AgentName:       agentName,
			ModelSlug:       pl.Model,
			Timestamp:       pl.CreatedAt,
			InputTokens:     pl.InputTokens,
			CacheReadTokens: pl.CacheReadTokens,
			OutputTokens:    pl.OutputTokens,
			ResponseTokens:  pl.OutputTokens,
		}
		if rec.Timestamp.IsZero() {
			rec.Timestamp = sum.LastModifiedTime
		}
		if isRoot {
			rec.TurnIndex, rec.InCurrentTurn = tr.TurnForStep(rec.StepIndex, sum.LastUserInputStepIndex)
		} else if !rootLastUserTime.IsZero() && !rec.Timestamp.IsZero() {
			rec.InCurrentTurn = !rec.Timestamp.Before(rootLastUserTime)
			if rec.InCurrentTurn {
				rec.TurnIndex = rootTurnCount
			} else {
				rec.TurnIndex = 1
			}
		} else {
			rec.TurnIndex, rec.InCurrentTurn = tr.TurnForStep(rec.StepIndex, sum.LastUserInputStepIndex)
		}
		p.calc.EnrichRecord(&rec)
		records = append(records, rec)
	}
	return records
}

func (p *Provider) buildAgentNode(id, parentID, name, role string, active bool, reqs []model.RequestRecord) model.AgentNode {
	scope := model.AggregateScope(reqs)
	node := model.AgentNode{
		ID:       id,
		ParentID: parentID,
		Name:     name,
		Role:     role,
		Active:   active,
		Metrics:  scope,
	}
	if scope.LatestRequest != nil {
		latest := scope.LatestRequest
		node.ModelDisplay = latest.ModelDisplay
		node.ContextCache = latest.CacheReadTokens
		node.ContextInput = latest.InputTokens + latest.CacheWriteTokens
		node.ContextOutput = latest.OutputTokens
		node.ContextUsed = latest.ContextUsed
		node.ContextLimit = latest.ContextLimit
		actualTokens := node.ContextCache + node.ContextInput + node.ContextOutput
		if actualTokens > node.ContextUsed {
			node.ContextUsed = actualTokens
		}
		// Attach latest semantic context breakdown from gen_metadata (f1.f9.f10.f3),
		// falling back gracefully to exact API Cache / Input / Output buckets if unavailable.
		for i := len(reqs) - 1; i >= 0; i-- {
			if len(reqs[i].ContextSegments) > 0 {
				segs := make([]model.ContextSegment, len(reqs[i].ContextSegments))
				copy(segs, reqs[i].ContextSegments)
				node.ContextSegments = segs
				break
			}
		}
		if len(node.ContextSegments) == 0 && actualTokens > 0 {
			fallback := make([]model.ContextSegment, 0, 3)
			if node.ContextCache > 0 {
				fallback = append(fallback, model.ContextSegment{Key: model.ContextSegCache, Tokens: node.ContextCache})
			}
			if node.ContextInput > 0 {
				fallback = append(fallback, model.ContextSegment{Key: model.ContextSegInput, Tokens: node.ContextInput})
			}
			if node.ContextOutput > 0 {
				fallback = append(fallback, model.ContextSegment{Key: model.ContextSegOutput, Tokens: node.ContextOutput})
			}
			node.ContextSegments = fallback
		}
		if node.ContextLimit > 0 {
			r := float64(node.ContextUsed) / float64(node.ContextLimit)
			if r < 0 {
				r = 0
			}
			if r > 1 {
				r = 1
			}
			node.ContextRatio = r
		}
	}
	return node
}

func isTreeRunning(t ConversationTree) bool {
	if t.Root.IsRunning() {
		return true
	}
	for _, c := range t.Children {
		if c.IsRunning() {
			return true
		}
	}
	return false
}
