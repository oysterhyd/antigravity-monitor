package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

const (
	defaultMaxConversations = 20
	defaultMaxHydratedTrees = 8
	defaultPollInterval     = 3 * time.Second
)

// Options configures the local read-only Antigravity data engine.
type Options struct {
	// RootDir is the Antigravity data root (defaults to ~/.gemini/antigravity).
	RootDir string
	// PricingOverridePath is an optional path to a user pricing override JSON file.
	PricingOverridePath string
	// Calculator optionally overrides the default pricing calculator.
	Calculator *metrics.Calculator
	// MaxOpenDBs limits concurrent open read-only per-conversation SQLite handles (default 5).
	MaxOpenDBs int
	// MaxConversations caps how many top-level conversations appear in AppSnapshot (default 20).
	MaxConversations int
	// MaxHydratedTrees caps how many top-level trees are hydrated via L2/L3 (default 8; all running trees are always hydrated).
	MaxHydratedTrees int
	// Debounce sets the per-conversation fsnotify debounce interval (default 150ms).
	Debounce time.Duration
	// PollInterval sets the low-frequency safety-net refresh interval (default 3s; negative disables polling).
	PollInterval time.Duration
	// DisableWatcher disables fsnotify and background polling (useful for one-shot CLI runs and unit tests).
	DisableWatcher bool
}

// DefaultRootDir returns ~/.gemini/antigravity.
func DefaultRootDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".gemini/antigravity"
	}
	return filepath.Join(home, ".gemini", "antigravity")
}

// Provider implements model.SnapshotProvider over Antigravity's local 3-layer data store
// (L1 conversation_summaries.db + L2 conversations/<id>.db + L3 transcript.jsonl).
// All methods are safe for concurrent use by multiple goroutines.
type Provider struct {
	opts Options
	calc *metrics.Calculator

	summaries *SummariesReader
	convPool  *ConvDBPool

	// Sync serialization prevents overlapping DB refreshes while keeping Snapshot() non-blocking.
	syncMu  sync.Mutex
	trees   []cachedTree
	tailers map[string]*TranscriptTailer

	snapMu   sync.RWMutex
	snapshot model.AppSnapshot

	subMu   sync.Mutex
	subs    map[int]chan model.AppSnapshot
	nextSub int
	closed  bool

	watcher   *dirWatcher
	triggerCh chan watchEvent
	stopCh    chan struct{}
	doneCh    chan struct{}
	hasLoop   bool
}

type cachedTree struct {
	tree     ConversationTree
	snapshot model.ConversationSnapshot
	hydrated bool
}

// Compile-time assertion that *Provider implements model.SnapshotProvider.
var _ model.SnapshotProvider = (*Provider)(nil)

// NewProvider initializes the read-only local data engine, performs an initial synchronous
// snapshot build, and starts the fsnotify watcher unless Options.DisableWatcher is set.
func NewProvider(opts Options) (*Provider, error) {
	if opts.RootDir == "" {
		opts.RootDir = DefaultRootDir()
	}
	if opts.MaxOpenDBs <= 0 {
		opts.MaxOpenDBs = defaultMaxOpenDBs
	}
	if opts.MaxConversations <= 0 {
		opts.MaxConversations = defaultMaxConversations
	}
	if opts.MaxHydratedTrees <= 0 {
		opts.MaxHydratedTrees = defaultMaxHydratedTrees
	}
	if opts.Debounce <= 0 {
		opts.Debounce = defaultDebounce
	}
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

	p := &Provider{
		opts:      opts,
		calc:      calc,
		summaries: NewSummariesReader(filepath.Join(opts.RootDir, "conversation_summaries.db")),
		convPool:  NewConvDBPool(filepath.Join(opts.RootDir, "conversations"), opts.MaxOpenDBs),
		tailers:   make(map[string]*TranscriptTailer),
		subs:      make(map[int]chan model.AppSnapshot),
		triggerCh: make(chan watchEvent, 64),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}

	if !opts.DisableWatcher {
		w, err := newDirWatcher(opts.RootDir, opts.Debounce, p.enqueueEvent)
		if err == nil {
			p.watcher = w
		}
	}

	// Perform initial synchronous refresh so Snapshot() is immediately ready.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = p.Refresh(ctx)
	cancel()

	if !opts.DisableWatcher {
		p.hasLoop = true
		go p.runLoop()
	}

	return p, nil
}

// Snapshot implements model.SnapshotProvider without blocking on I/O.
func (p *Provider) Snapshot() model.AppSnapshot {
	p.snapMu.RLock()
	defer p.snapMu.RUnlock()
	return p.snapshot
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
	for id, ch := range p.subs {
		delete(p.subs, id)
		close(ch)
	}
	p.subMu.Unlock()

	if p.watcher != nil {
		_ = p.watcher.Close()
	}
	if p.hasLoop {
		<-p.doneCh
	}

	err1 := p.convPool.Close()
	err2 := p.summaries.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// OpenConvDBHandles returns the number of currently open L2 SQLite handles (for diagnostics/tests).
func (p *Provider) OpenConvDBHandles() int {
	return p.convPool.OpenHandleCount()
}

// Refresh forces an immediate L1 + L2 + L3 incremental sync and broadcasts the updated snapshot.
func (p *Provider) Refresh(ctx context.Context) error {
	p.syncMu.Lock()
	defer p.syncMu.Unlock()

	rows, changed, err := p.summaries.readChanges(ctx)
	if err != nil {
		// Keep previous cached rows if L1 DB had a transient error.
		if len(p.trees) == 0 {
			return err
		}
	} else if changed && (len(rows) > 0 || len(p.trees) > 0) {
		p.updateTrees(rows)
	}

	snap := p.buildSnapshotLocked(ctx)

	p.snapMu.Lock()
	p.snapshot = snap
	p.snapMu.Unlock()

	p.broadcast(snap)
	return nil
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

func (p *Provider) runLoop() {
	defer close(p.doneCh)

	var ticker *time.Ticker
	var tickCh <-chan time.Time
	if p.opts.PollInterval > 0 {
		ticker = time.NewTicker(p.opts.PollInterval)
		tickCh = ticker.C
		defer ticker.Stop()
	}

	for {
		select {
		case <-p.stopCh:
			return
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
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = p.Refresh(ctx)
			cancel()
		case <-tickCh:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = p.Refresh(ctx)
			cancel()
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

func (p *Provider) syncTranscript(convID string) bool {
	tailer := p.tailers[convID]
	if tailer == nil {
		tailer = NewTranscriptTailer(TranscriptPath(p.opts.RootDir, convID))
		p.tailers[convID] = tailer
	}
	transcriptChanged, _ := tailer.Sync()
	return transcriptChanged
}

func (p *Provider) pruneTailers(keepIDs map[string]bool) {
	for id := range p.tailers {
		if !keepIDs[id] {
			delete(p.tailers, id)
		}
	}
}

func (p *Provider) buildSnapshotLocked(ctx context.Context) model.AppSnapshot {
	snap := model.AppSnapshot{
		GeneratedAt:   time.Now().UTC(),
		Conversations: make([]model.ConversationSnapshot, 0, len(p.trees)),
	}

	keepIDs := make(map[string]bool, len(p.trees)*2)
	hydratedIDs := make([]string, 0, len(p.trees)*2)
	hydratedCount := 0

	for _, cached := range p.trees {
		tree := cached.tree
		if !isTreeRunning(tree) && hydratedCount >= p.opts.MaxHydratedTrees {
			continue
		}
		hydratedCount++
		keepIDs[tree.Root.ConversationID] = true
		hydratedIDs = append(hydratedIDs, tree.Root.ConversationID)
		for _, child := range tree.Children {
			keepIDs[child.ConversationID] = true
			hydratedIDs = append(hydratedIDs, child.ConversationID)
		}
	}
	dbChanges := p.convPool.syncBatch(ctx, hydratedIDs)

	for i := range p.trees {
		cached := &p.trees[i]
		tree := cached.tree
		hydrate := keepIDs[tree.Root.ConversationID]
		changed := cached.snapshot.ID == "" || cached.hydrated != hydrate
		if hydrate {
			rootID := tree.Root.ConversationID
			changed = p.syncTranscript(rootID) || dbChanges[rootID] || changed
			for _, child := range tree.Children {
				id := child.ConversationID
				changed = p.syncTranscript(id) || dbChanges[id] || changed
			}
		}

		if changed {
			cached.snapshot = p.buildConversationSnapshot(tree, hydrate)
			cached.hydrated = hydrate
		}
		convSnap := cached.snapshot
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

	p.convPool.PruneCache(keepIDs)
	p.pruneTailers(keepIDs)
	if p.watcher != nil {
		p.watcher.WatchTranscriptDirs(keepIDs)
	}

	return snap
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

	p.convPool.mu.Lock()
	defer p.convPool.mu.Unlock()
	rootState := p.convPool.states[rootSum.ConversationID]
	rootTr := p.tailers[rootSum.ConversationID].data()

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
		state := p.convPool.states[child.ConversationID]
		gens := state.records
		tr := p.tailers[child.ConversationID].data()
		childrenData[i] = childHydrated{
			sum:       child,
			gens:      gens,
			stepTimes: state.stepTimes,
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
		rootState.records,
		rootState.stepTimes,
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
