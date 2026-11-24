package mock

import (
	"math/rand"
	"sync"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

// Provider is a thread-safe in-memory SnapshotProvider simulating 2 concurrent running
// conversations (with parent-child subagent trees) plus 1 idle single-agent conversation.
// Tracks 2 and 3 develop and test against this provider with zero dependency on Track 1.
type Provider struct {
	mu       sync.RWMutex
	convs    []*simConv
	activeID string
	subs     map[int]chan model.AppSnapshot
	nextSub  int
	stopCh   chan struct{}
	closed   bool
	rng      *rand.Rand
}

type simAgent struct {
	id           string
	parentID     string
	name         string
	role         string
	modelSlug    string
	modelDisplay string
	ctxLimit     int64
	ctxCache     int64
	ctxInput     int64
	ctxOutput    int64
	baseTPS      float64
	baseTTFT     float64
	rateIn       float64
	rateOut      float64
}

type simConv struct {
	id            string
	title         string
	workspace     string
	running       bool
	activeAgentID string
	lastUpdated   time.Time
	agents        []*simAgent
	requests      []model.RequestRecord
}

// New creates a mock provider. If tickInterval > 0, simulated requests stream automatically.
func New(tickInterval time.Duration) *Provider {
	p := &Provider{
		subs:   make(map[int]chan model.AppSnapshot),
		stopCh: make(chan struct{}),
		rng:    rand.New(rand.NewSource(42)),
	}
	p.seed()
	if tickInterval > 0 {
		go p.loop(tickInterval)
	}
	return p
}

// Snapshot implements model.SnapshotProvider.
func (p *Provider) Snapshot() model.AppSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.buildSnapshotLocked()
}

// Subscribe implements model.SnapshotProvider.
func (p *Provider) Subscribe() (<-chan model.AppSnapshot, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.nextSub
	p.nextSub++
	ch := make(chan model.AppSnapshot, 1)
	if !p.closed {
		ch <- p.buildSnapshotLocked()
		p.subs[id] = ch
	} else {
		close(ch)
	}
	cancel := func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if existing, ok := p.subs[id]; ok {
			delete(p.subs, id)
			close(existing)
		}
	}
	return ch, cancel
}

// TriggerBurst immediately injects a realistic simulated request on a running conversation.
func (p *Provider) TriggerBurst() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.injectNextLocked()
	snap := p.buildSnapshotLocked()
	p.broadcastLocked(snap)
	p.mu.Unlock()
}

// Close implements model.SnapshotProvider.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	close(p.stopCh)
	for id, ch := range p.subs {
		delete(p.subs, id)
		close(ch)
	}
	return nil
}

func (p *Provider) loop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.TriggerBurst()
		}
	}
}

func (p *Provider) broadcastLocked(snap model.AppSnapshot) {
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

func (p *Provider) injectNextLocked() {
	var running []*simConv
	for _, c := range p.convs {
		if c.running {
			running = append(running, c)
		}
	}
	if len(running) == 0 {
		return
	}
	conv := running[0]
	if len(running) > 1 && p.rng.Float64() >= 0.65 {
		conv = running[1]
	}

	ag := conv.agents[0]
	if len(conv.agents) > 1 && p.rng.Float64() >= 0.55 {
		ag = conv.agents[1+p.rng.Intn(len(conv.agents)-1)]
	}
	conv.activeAgentID = ag.id
	conv.lastUpdated = time.Now()
	p.activeID = conv.id

	prev := ag.ctxCache + ag.ctxInput + ag.ctxOutput
	hitRatio := 0.86 + p.rng.Float64()*0.11
	if p.rng.Float64() < 0.10 {
		hitRatio = 0.45
	}
	newIn := int64(1200 + p.rng.Intn(2600))
	cacheR := int64(float64(prev) * hitRatio)
	out := int64(500 + p.rng.Intn(1100))
	think := int64(float64(out) * 0.55)
	resp := out - think

	tps := ag.baseTPS + (p.rng.Float64()-0.5)*22
	if tps < 20 {
		tps = 20
	}
	ttftSec := ag.baseTTFT + (p.rng.Float64()-0.5)*0.6
	if ttftSec < 0.45 {
		ttftSec = 0.45
	}
	ttft := time.Duration(ttftSec * float64(time.Second))
	decodeDur := time.Duration((float64(out) / tps) * float64(time.Second))
	totalDur := ttft + decodeDur

	cost := (float64(newIn)*ag.rateIn + float64(cacheR)*(ag.rateIn*0.1) + float64(out)*ag.rateOut) / 1e6
	saved := (float64(cacheR) * (ag.rateIn * 0.9)) / 1e6

	if prev+newIn+out < int64(float64(ag.ctxLimit)*0.85) {
		ag.ctxCache = cacheR
		ag.ctxInput = newIn
		ag.ctxOutput = out
	} else {
		ag.ctxCache = int64(float64(ag.ctxLimit) * 0.24)
		ag.ctxInput = newIn
		ag.ctxOutput = out
	}

	seq := len(conv.requests) + 1
	if seq > 1 {
		seq = conv.requests[len(conv.requests)-1].Seq + 1
	}

	rec := model.RequestRecord{
		Seq:             seq,
		GenIndex:        seq - 1,
		StepIndex:       seq * 3,
		TurnIndex:       4,
		InCurrentTurn:   true,
		ConversationID:  conv.id,
		AgentID:         ag.id,
		AgentName:       ag.name,
		ModelSlug:       ag.modelSlug,
		ModelDisplay:    ag.modelDisplay,
		Timestamp:       conv.lastUpdated,
		InputTokens:     newIn,
		CacheReadTokens: cacheR,
		OutputTokens:    out,
		ThinkingTokens:  think,
		ResponseTokens:  resp,
		ContextUsed:     ag.ctxCache + ag.ctxInput + ag.ctxOutput,
		ContextLimit:    ag.ctxLimit,
		TTFT:            ttft,
		TotalDuration:   totalDur,
		DecodeTPS:       model.ComputeDecodeTPS(out, totalDur, ttft),
		CacheHitRate:    model.ComputeCacheHitRate(newIn, cacheR, 0),
		CostUSD:         cost,
		CacheSavedUSD:   saved,
	}
	conv.requests = append(conv.requests, rec)
	if len(conv.requests) > 80 {
		conv.requests = conv.requests[len(conv.requests)-80:]
	}
}

func (p *Provider) buildSnapshotLocked() model.AppSnapshot {
	snap := model.AppSnapshot{
		GeneratedAt:          time.Now(),
		ActiveConversationID: p.activeID,
		Conversations:        make([]model.ConversationSnapshot, 0, len(p.convs)),
	}

	for _, c := range p.convs {
		if c.running {
			snap.RunningCount++
		}
		byAgent := make(map[string][]model.RequestRecord, len(c.agents))
		for _, r := range c.requests {
			byAgent[r.AgentID] = append(byAgent[r.AgentID], r)
		}

		var rootNode model.AgentNode
		subs := make([]model.AgentNode, 0, len(c.agents)-1)
		for idx, ag := range c.agents {
			used := ag.ctxCache + ag.ctxInput + ag.ctxOutput
			ratio := 0.0
			if ag.ctxLimit > 0 {
				ratio = float64(used) / float64(ag.ctxLimit)
			}
			node := model.AgentNode{
				ID:              ag.id,
				ParentID:        ag.parentID,
				Name:            ag.name,
				Role:            ag.role,
				ModelDisplay:    ag.modelDisplay,
				Active:          c.running && c.activeAgentID == ag.id,
				ContextUsed:     used,
				ContextLimit:    ag.ctxLimit,
				ContextCache:    ag.ctxCache,
				ContextInput:    ag.ctxInput,
				ContextOutput:   ag.ctxOutput,
				ContextSegments: mockContextSegments(used, idx == 0),
				ContextRatio:    ratio,
				Metrics:         model.AggregateScope(byAgent[ag.id]),
			}
			if idx == 0 {
				rootNode = node
			} else {
				subs = append(subs, node)
			}
		}

		treeMetrics := model.AggregateScope(c.requests)
		subCost := treeMetrics.SessionCostUSD - rootNode.Metrics.SessionCostUSD
		if subCost < 0 {
			subCost = 0
		}

		reqsCopy := make([]model.RequestRecord, len(c.requests))
		copy(reqsCopy, c.requests)

		snap.Conversations = append(snap.Conversations, model.ConversationSnapshot{
			ID:               c.id,
			Title:            c.title,
			Workspace:        c.workspace,
			Running:          c.running,
			LastUpdated:      c.lastUpdated,
			ActiveAgentID:    c.activeAgentID,
			Root:             rootNode,
			Subagents:        subs,
			TreeMetrics:      treeMetrics,
			SubagentsCostUSD: subCost,
			PeakContext:      model.ComputePeakContext(rootNode, subs),
			Requests:         reqsCopy,
		})
	}
	return snap
}

func (p *Provider) seed() {
	now := time.Now()
	c1 := &simConv{
		id:            "conv-monitor",
		title:         "antigravity_monitor",
		workspace:     "d:/antigravity_monitor",
		running:       true,
		activeAgentID: model.RootAgentID,
		lastUpdated:   now,
		agents: []*simAgent{
			{id: model.RootAgentID, name: "主 Agent", role: "Coordinator", modelSlug: "gemini-4-neon", modelDisplay: "Gemini 4 Neon (High)", ctxLimit: 512000, ctxCache: 41200, ctxInput: 4850, ctxOutput: 2150, baseTPS: 126, baseTTFT: 1.65, rateIn: 1.25, rateOut: 10.0},
			{id: "sub-research", parentID: model.RootAgentID, name: "Codebase Researcher", role: "research", modelSlug: "gemini-4-flash", modelDisplay: "Gemini 4 Flash", ctxLimit: 512000, ctxCache: 27400, ctxInput: 3100, ctxOutput: 1100, baseTPS: 152, baseTTFT: 0.92, rateIn: 0.30, rateOut: 2.5},
			{id: "sub-schema", parentID: model.RootAgentID, name: "Schema Reader", role: "self", modelSlug: "gemini-4-neon", modelDisplay: "Gemini 4 Neon", ctxLimit: 512000, ctxCache: 15800, ctxInput: 2400, ctxOutput: 1200, baseTPS: 124, baseTTFT: 1.55, rateIn: 1.25, rateOut: 10.0},
			{id: "sub-browser", parentID: model.RootAgentID, name: "browser", role: "browser", modelSlug: "claude-sonnet-4", modelDisplay: "Claude 4.5 Sonnet", ctxLimit: 200000, ctxCache: 16200, ctxInput: 2200, ctxOutput: 1000, baseTPS: 94, baseTTFT: 2.05, rateIn: 3.0, rateOut: 15.0},
		},
	}

	c2 := &simConv{
		id:            "conv-payments",
		title:         "payments-api 重构",
		workspace:     "d:/work/payments-api",
		running:       true,
		activeAgentID: "sub-browser-2",
		lastUpdated:   now.Add(-4 * time.Second),
		agents: []*simAgent{
			{id: model.RootAgentID, name: "主 Agent", role: "Coordinator", modelSlug: "claude-sonnet-4", modelDisplay: "Claude 4.5 Sonnet", ctxLimit: 200000, ctxCache: 84200, ctxInput: 5600, ctxOutput: 2400, baseTPS: 93, baseTTFT: 2.15, rateIn: 3.0, rateOut: 15.0},
			{id: "sub-browser-2", parentID: model.RootAgentID, name: "browser", role: "browser", modelSlug: "gemini-4-flash", modelDisplay: "Gemini 4 Flash", ctxLimit: 512000, ctxCache: 19400, ctxInput: 2100, ctxOutput: 860, baseTPS: 150, baseTTFT: 0.95, rateIn: 0.30, rateOut: 2.5},
		},
	}

	c3 := &simConv{
		id:            "conv-sql",
		title:         "SQL 慢查询排查",
		workspace:     "d:/work/analytics-db",
		running:       false,
		activeAgentID: model.RootAgentID,
		lastUpdated:   now.Add(-12 * time.Minute),
		agents: []*simAgent{
			{id: model.RootAgentID, name: "主 Agent", role: "Coordinator", modelSlug: "gemini-4-neon", modelDisplay: "Gemini 4 Neon (High)", ctxLimit: 512000, ctxCache: 21800, ctxInput: 1900, ctxOutput: 940, baseTPS: 130, baseTTFT: 1.48, rateIn: 1.25, rateOut: 10.0},
		},
	}

	p.convs = []*simConv{c1, c2, c3}
	p.activeID = c1.id

	// Seed initial request history across all 3 conversations.
	for i := 0; i < 16; i++ {
		p.seedOne(c1, i >= 12)
	}
	for i := 0; i < 8; i++ {
		p.seedOne(c2, i >= 5)
	}
	for i := 0; i < 4; i++ {
		p.seedOne(c3, i >= 2)
	}
}

func (p *Provider) seedOne(conv *simConv, inTurn bool) {
	ag := conv.agents[0]
	if len(conv.agents) > 1 && p.rng.Float64() >= 0.55 {
		ag = conv.agents[1+p.rng.Intn(len(conv.agents)-1)]
	}
	seq := len(conv.requests) + 1
	newIn := int64(1500 + p.rng.Intn(3000))
	cacheR := int64(float64(ag.ctxCache) * (0.75 + p.rng.Float64()*0.2))
	if seq == 1 {
		cacheR = 0
		newIn = 14800
	}
	out := int64(600 + p.rng.Intn(1000))
	ttft := time.Duration((ag.baseTTFT + (p.rng.Float64()-0.5)*0.5) * float64(time.Second))
	tps := ag.baseTPS + (p.rng.Float64()-0.5)*18
	totalDur := ttft + time.Duration((float64(out)/tps)*float64(time.Second))
	cost := (float64(newIn)*ag.rateIn + float64(cacheR)*(ag.rateIn*0.1) + float64(out)*ag.rateOut) / 1e6
	saved := (float64(cacheR) * (ag.rateIn * 0.9)) / 1e6

	conv.requests = append(conv.requests, model.RequestRecord{
		Seq:             seq,
		GenIndex:        seq - 1,
		StepIndex:       seq * 3,
		TurnIndex:       1,
		InCurrentTurn:   inTurn,
		ConversationID:  conv.id,
		AgentID:         ag.id,
		AgentName:       ag.name,
		ModelSlug:       ag.modelSlug,
		ModelDisplay:    ag.modelDisplay,
		Timestamp:       conv.lastUpdated.Add(-time.Duration(20-seq) * 15 * time.Second),
		InputTokens:     newIn,
		CacheReadTokens: cacheR,
		OutputTokens:    out,
		ThinkingTokens:  out / 2,
		ResponseTokens:  out - out/2,
		ContextUsed:     ag.ctxCache + ag.ctxInput + ag.ctxOutput,
		ContextLimit:    ag.ctxLimit,
		TTFT:            ttft,
		TotalDuration:   totalDur,
		DecodeTPS:       model.ComputeDecodeTPS(out, totalDur, ttft),
		CacheHitRate:    model.ComputeCacheHitRate(newIn, cacheR, 0),
		CostUSD:         cost,
		CacheSavedUSD:   saved,
	})
}

func mockContextSegments(total int64, isRoot bool) []model.ContextSegment {
	if total <= 0 {
		return nil
	}
	sys := int64(float64(total) * 0.14)
	tools := int64(float64(total) * 0.19)
	toolOut := int64(float64(total) * 0.42)
	summary := int64(0)
	if isRoot && total > 35000 {
		summary = int64(float64(total) * 0.08)
	}
	msgs := total - sys - tools - toolOut - summary
	if msgs < 0 {
		msgs = 0
	}
	segs := []model.ContextSegment{
		{Key: model.ContextSegSystem, Tokens: sys},
		{Key: model.ContextSegTools, Tokens: tools},
		{Key: model.ContextSegToolOutput, Tokens: toolOut},
		{Key: model.ContextSegMessages, Tokens: msgs},
	}
	if summary > 0 {
		segs = append(segs, model.ContextSegment{Key: model.ContextSegSummary, Tokens: summary})
	}
	return segs
}
