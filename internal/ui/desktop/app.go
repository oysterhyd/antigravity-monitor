package desktop

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/i18n"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/anim"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

// MetricKind identifies which KPI metric drives the morphing telemetry curve.
type MetricKind string

const (
	MetricTPS   MetricKind = "tps"
	MetricTTFT  MetricKind = "ttft"
	MetricCache MetricKind = "cache"
	MetricCost  MetricKind = "cost"
)

// ReqViewMode toggles the bottom card between the morphing curve and the per-request list.
type ReqViewMode int

const (
	ReqViewCurve ReqViewMode = 0
	ReqViewList  ReqViewMode = 1
)

const (
	scopeAll       = "all"
	chartWindowLen = 18
)

// Options configures the desktop UI view.
type Options struct {
	// ThemeMode selects "system", "light", or "dark" (default "system").
	ThemeMode string
	// Settings provides initial user settings; zero values fall back to model.DefaultSettings().
	Settings *model.Settings
	// Window is the host mygo window, used for Update() wakeups and AlwaysOnTop toggles.
	Window *mygo.Window
	// ShowDevToolbar shows subtle top-right dev harness controls (theme, live toggle, burst).
	ShowDevToolbar bool
	// LiveRunning indicates initial live stream state for the dev harness.
	LiveRunning bool
	// OnTriggerBurst is called when the user triggers a simulated burst in dev-desktop.
	OnTriggerBurst func()
	// OnToggleLive toggles the dev-desktop mock stream and returns the new running state.
	OnToggleLive func() bool
	// OnSettingsChange notifies Track 3 (config persistence / window flags) when settings change.
	OnSettingsChange func(model.Settings)
}

// App owns the desktop UI state, snapshot subscription, and 60fps animation engines.
type App struct {
	provider model.SnapshotProvider
	opts     Options

	mu        sync.RWMutex
	snap      model.AppSnapshot
	snapRev   uint64
	seenRev   uint64
	win       *mygo.Window
	stopSub   func()
	closeOnce sync.Once

	// Interactive UI state (accessed on UI thread inside View / tests).
	settings            model.Settings
	convID              string
	scope               string
	metric              MetricKind
	reqView             ReqViewMode
	pinnedReqSeq        int // 0 = auto-follow latest
	expandedReqSeq      int
	hoverChartIdx       int
	hoverCtxSegIdx      int
	sessionPopoverOpen  bool
	settingsPopoverOpen bool
	liveRunning         bool

	// 60fps spring & morphing state.
	springs     *anim.SpringBank
	morpher     *anim.CurveMorpher
	heroPulse   anim.Pulse
	badgePulse  anim.Pulse
	lastFrame   time.Time
	lastSeenSeq map[string]int
	tailBuf     []float32
}

// New constructs a desktop UI App bound to a frozen model.SnapshotProvider.
func New(provider model.SnapshotProvider, opts Options) *App {
	settings := model.DefaultSettings()
	if opts.Settings != nil {
		settings = *opts.Settings
	}
	if opts.ThemeMode != "" {
		settings.ThemeMode = opts.ThemeMode
	}
	if settings.ContextWarnThreshold < 0 {
		settings.ContextWarnThreshold = 0.80
	}

	a := &App{
		provider:      provider,
		opts:          opts,
		win:           opts.Window,
		settings:      settings,
		scope:         scopeAll,
		metric:        MetricTPS,
		reqView:        ReqViewCurve,
		hoverChartIdx:  -1,
		hoverCtxSegIdx: -1,
		liveRunning:    opts.LiveRunning,
		springs:       anim.NewSpringBank(anim.DefaultSpringConfig()),
		morpher:       anim.NewCurveMorpher(chartWindowLen),
		lastSeenSeq:   make(map[string]int, 4),
	}

	if provider != nil {
		initial := provider.Snapshot()
		a.snap = initial
		a.snapRev = 1
		a.convID = initial.ActiveConversationID
		if a.convID == "" && len(initial.Conversations) > 0 {
			a.convID = initial.Conversations[0].ID
		}
		for _, c := range initial.Conversations {
			if len(c.Requests) > 0 {
				a.lastSeenSeq[c.ID] = c.Requests[len(c.Requests)-1].Seq
			}
		}

		ch, cancel := provider.Subscribe()
		a.stopSub = cancel
		go a.watchSnapshots(ch)
	}

	return a
}

// SetWindow attaches or updates the host *mygo.Window after construction.
func (a *App) SetWindow(w *mygo.Window) {
	a.mu.Lock()
	a.win = w
	a.mu.Unlock()
}

// Close unsubscribes from the SnapshotProvider.
func (a *App) Close() {
	a.closeOnce.Do(func() {
		if a.stopSub != nil {
			a.stopSub()
		}
	})
}

func (a *App) watchSnapshots(ch <-chan model.AppSnapshot) {
	for snap := range ch {
		a.mu.Lock()
		a.snap = snap
		a.snapRev++
		win := a.win
		a.mu.Unlock()

		if win != nil {
			win.Update(func() {})
		}
	}
}

// SyncSnapshot pulls the latest snapshot synchronously (useful in headless tests).
func (a *App) SyncSnapshot() {
	if a.provider == nil {
		return
	}
	snap := a.provider.Snapshot()
	a.mu.Lock()
	a.snap = snap
	a.snapRev++
	a.mu.Unlock()
}

// FocusConversation selects a conversation; manual=true pins it (disables auto-follow).
func (a *App) FocusConversation(convID string, manual bool) {
	if convID == "" {
		return
	}
	a.convID = convID
	if manual && a.settings.AutoFollowActive {
		a.settings.AutoFollowActive = false
		a.emitSettings()
	}
	a.scope = scopeAll
	a.pinnedReqSeq = 0
	a.expandedReqSeq = 0
	a.hoverChartIdx = -1
	a.sessionPopoverOpen = false
	a.morpher.ClearCrosshair()
}

// FocusAgent switches the active scope to a specific agent node ("all", "root", or subagent ID).
func (a *App) FocusAgent(agentID string) {
	if agentID == "" {
		agentID = scopeAll
	}
	if a.scope == agentID {
		return
	}
	a.scope = agentID
	a.pinnedReqSeq = 0
	a.expandedReqSeq = 0
	a.hoverChartIdx = -1
	a.morpher.ClearCrosshair()
}

// SetThemeMode updates the theme mode ("system" | "light" | "dark").
func (a *App) SetThemeMode(mode string) {
	if a.settings.ThemeMode == mode {
		return
	}
	a.settings.ThemeMode = mode
	a.emitSettings()
}

// SetLanguage updates the UI language ("zh" | "en").
func (a *App) SetLanguage(lang string) {
	lang = i18n.Normalize(lang)
	if i18n.Normalize(a.settings.Language) == lang {
		return
	}
	a.settings.Language = lang
	a.emitSettings()
}

func (a *App) tr() i18n.Strings {
	return i18n.Get(a.settings.Language)
}

// SelectMetric switches the active KPI metric driving the morphing chart.
func (a *App) SelectMetric(m MetricKind) {
	a.metric = m
}

// SetReqView toggles between Curve and Per-Request List view.
func (a *App) SetReqView(v ReqViewMode) {
	a.reqView = v
}

// FocusConversationAgent atomically selects a conversation (pinning it when specified)
// and focuses a specific agent node ("all", "root", or subagent ID). Intended for external
// callers such as the Tray Flyout; safe to call from the mygo main UI thread.
func (a *App) FocusConversationAgent(convID, agentID string) {
	if convID != "" && convID != a.convID {
		a.convID = convID
		a.sessionPopoverOpen = false
		if a.settings.AutoFollowActive {
			a.settings.AutoFollowActive = false
			a.emitSettings()
		}
	}
	if agentID == "" {
		agentID = scopeAll
	}
	a.scope = agentID
	a.pinnedReqSeq = 0
	a.expandedReqSeq = 0
	a.hoverChartIdx = -1
	a.morpher.ClearCrosshair()

	a.mu.RLock()
	win := a.win
	a.mu.RUnlock()
	if win != nil {
		win.Invalidate()
	}
}

// ApplySettings synchronizes external settings updates (e.g. from the Tray Flyout)
// into the desktop view without re-emitting OnSettingsChange. Must run on the main thread.
func (a *App) ApplySettings(s model.Settings) {
	if s.ContextWarnThreshold < 0 {
		s.ContextWarnThreshold = 0.80
	}
	a.settings = s
	a.mu.RLock()
	win := a.win
	a.mu.RUnlock()
	if win != nil {
		win.SetAlwaysOnTop(s.AlwaysOnTop)
		win.Invalidate()
	}
}

// Settings returns a copy of the current desktop settings.
func (a *App) Settings() model.Settings {
	return a.settings
}

func (a *App) emitSettings() {
	if a.opts.OnSettingsChange != nil {
		a.opts.OnSettingsChange(a.settings)
	}
}

func (a *App) toggleAlwaysOnTop() {
	a.mu.RLock()
	win := a.win
	a.mu.RUnlock()
	if win != nil {
		win.SetAlwaysOnTop(a.settings.AlwaysOnTop)
	}
	a.emitSettings()
}

// View builds the full Apple Liquid Glass desktop UI frame.
func (a *App) View(c *ui.Context) {
	now := c.Now()
	dtSec := 0.0
	if !a.lastFrame.IsZero() {
		dtSec = now.Sub(a.lastFrame).Seconds()
	}
	a.lastFrame = now

	pal := theme.ResolvePalette(c, a.settings.ThemeMode)
	theme.ApplyTheme(c, pal)

	// Pull latest immutable snapshot.
	a.mu.RLock()
	snap := a.snap
	rev := a.snapRev
	a.mu.RUnlock()

	if rev != a.seenRev {
		a.seenRev = rev
		a.onNewSnapshot(snap, now)
	}

	// Keyboard shortcuts (convenient for both production & dev-desktop).
	a.handleShortcuts(c, snap)

	// Resolve active conversation and scope view model.
	conv, hasConv := snap.FindConversation(a.convID)
	if !hasConv {
		a.renderEmptyState(c, pal)
		return
	}
	a.convID = conv.ID
	if a.scope != scopeAll {
		if _, ok := conv.FindAgent(a.scope); !ok {
			a.scope = scopeAll
		}
	}

	vm := a.buildScopeView(conv)
	a.updateAnimationTargets(conv, vm, c.Preferences().ReduceMotion)

	// Advance physics & morphing only while active; idle settles to ~0% GPU.
	animActive := false
	if c.Preferences().ReduceMotion {
		a.snapAllAnimations(conv, vm)
	} else {
		if a.springs.StepAll(dtSec) {
			animActive = true
		}
		if a.morpher.Step(dtSec) {
			animActive = true
		}
		if a.heroPulse.Active(now) || a.badgePulse.Active(now) {
			animActive = true
		}
	}
	if animActive {
		c.AnimationFrame()
	}

	// Root window vibrancy / atmospheric glass shell.
	vibrant := c.Vibrancy()
	root := c.Root()
	if vibrant {
		root.Background(ui.Transparent)
	} else {
		root.Background(pal.BackdropFrom)
	}

	bar := c.TitleBar()
	shell := ui.Box(c.Key("desktop-shell")).Fill().TextColor(pal.Ink1)
	if !vibrant {
		shell.Draw(func(p *ui.Painter, r ui.Rect) {
			theme.DrawAtmosphericBackdrop(p, r, pal)
			p.Fill(r, pal.ShellBg, 0)
		})
	} else {
		shell.Draw(func(p *ui.Painter, r ui.Rect) {
			theme.DrawVibrantLiquidWash(p, r, pal)
		})
	}

	// Keep the floating glass sidebar cleanly aligned at 12px from the top edge
	// (matching prototype.html); only add top-right clearance inside the main header.
	shell.Children(func() {
		// Full-width invisible top drag handle when titlebar is hidden.
		if bar.Height > 0 {
			ui.Box(c.Key("titlebar-drag")).
				Absolute().Top(0).Left(0).Right(bar.Right + 4).Height(max(bar.Height, 28)).
				DragWindow()
		}

		ui.Row(c.Key("desktop-layout")).
			Fill().
			Padding(12, 12, 12, 12).
			Gap(12).
			AlignItems(ui.Stretch).
			Children(func() {
				a.renderSidebar(c, pal, snap, conv, now)
				a.renderMainCanvas(c, pal, conv, vm, now)
			})
	})
}

func (a *App) handleShortcuts(c *ui.Context, snap model.AppSnapshot) {
	if c.FocusTakesText() {
		return
	}
	if a.opts.OnTriggerBurst != nil && c.Shortcut(0, ui.KeyB) {
		a.opts.OnTriggerBurst()
	}
	if c.Shortcut(0, ui.KeyT) {
		if a.settings.ThemeMode == theme.ModeDark {
			a.SetThemeMode(theme.ModeLight)
		} else {
			a.SetThemeMode(theme.ModeDark)
		}
	}
	if a.reqView == ReqViewCurve {
		if c.Shortcut(0, ui.KeyLeft) {
			if conv, ok := snap.FindConversation(a.convID); ok {
				a.stepInspect(a.buildScopeView(conv).requests, -1)
			}
		}
		if c.Shortcut(0, ui.KeyRight) {
			if conv, ok := snap.FindConversation(a.convID); ok {
				a.stepInspect(a.buildScopeView(conv).requests, 1)
			}
		}
	}
}

func (a *App) onNewSnapshot(snap model.AppSnapshot, now time.Time) {
	if len(snap.Conversations) == 0 {
		return
	}

	updatedConvID := ""
	for _, c := range snap.Conversations {
		lastSeq := 0
		if len(c.Requests) > 0 {
			lastSeq = c.Requests[len(c.Requests)-1].Seq
		}
		if prev, ok := a.lastSeenSeq[c.ID]; ok && lastSeq > prev {
			updatedConvID = c.ID
		}
		a.lastSeenSeq[c.ID] = lastSeq
	}

	if a.settings.AutoFollowActive && snap.ActiveConversationID != "" && snap.ActiveConversationID != a.convID {
		a.convID = snap.ActiveConversationID
		a.scope = scopeAll
		a.pinnedReqSeq = 0
		a.hoverChartIdx = -1
		a.morpher.ClearCrosshair()
	}

	if updatedConvID != "" {
		if updatedConvID == a.convID {
			a.heroPulse.Trigger(now, 800*time.Millisecond)
		} else {
			// Background conversation pulsed while pinned: quiet badge pulse, never steal focus.
			a.badgePulse.Trigger(now, 900*time.Millisecond)
		}
	}
}

type scopeView struct {
	isTreeView   bool
	activeAgent  model.AgentNode
	metrics      model.ScopeMetrics
	requests     []model.RequestRecord
	latestReq    model.RequestRecord
	hasLatestReq bool
	latestSource string
}

func (a *App) buildScopeView(conv model.ConversationSnapshot) scopeView {
	hasSubs := len(conv.Subagents) > 0
	isTree := a.scope == scopeAll && hasSubs

	if isTree {
		sv := scopeView{
			isTreeView: true,
			activeAgent: conv.Root,
			metrics:    conv.TreeMetrics,
			requests:   conv.Requests,
		}
		if conv.TreeMetrics.LatestRequest != nil {
			sv.latestReq = *conv.TreeMetrics.LatestRequest
			sv.hasLatestReq = true
			sv.latestSource = sv.latestReq.AgentName
			if sv.latestSource == "" {
				if ag, ok := conv.FindAgent(sv.latestReq.AgentID); ok {
					sv.latestSource = ag.Name
				}
			}
		} else if len(conv.Requests) > 0 {
			sv.latestReq = conv.Requests[len(conv.Requests)-1]
			sv.hasLatestReq = true
			sv.latestSource = sv.latestReq.AgentName
		}
		return sv
	}

	targetID := a.scope
	if targetID == scopeAll {
		targetID = conv.Root.ID
	}
	ag, ok := conv.FindAgent(targetID)
	if !ok {
		ag = conv.Root
	}

	filtered := make([]model.RequestRecord, 0, len(conv.Requests))
	for _, r := range conv.Requests {
		if r.AgentID == ag.ID {
			filtered = append(filtered, r)
		}
	}

	sv := scopeView{
		isTreeView:  false,
		activeAgent: ag,
		metrics:     ag.Metrics,
		requests:    filtered,
	}
	if ag.Metrics.LatestRequest != nil {
		sv.latestReq = *ag.Metrics.LatestRequest
		sv.hasLatestReq = true
		sv.latestSource = ag.Name
	} else if len(filtered) > 0 {
		sv.latestReq = filtered[len(filtered)-1]
		sv.hasLatestReq = true
		sv.latestSource = ag.Name
	}
	return sv
}

func (a *App) updateAnimationTargets(conv model.ConversationSnapshot, vm scopeView, instant bool) {
	set := func(k string, v float64) {
		if instant {
			a.springs.Jump(k, v)
		} else {
			a.springs.Set(k, v)
		}
	}

	set("hdrAll", conv.TreeMetrics.SessionCostUSD)
	set("hdrRoot", conv.Root.Metrics.SessionCostUSD)
	set("hdrSub", conv.SubagentsCostUSD)

	if vm.isTreeView {
		set("heroCtxPct", conv.PeakContext.Ratio*100)
	} else {
		set("heroCtxPct", vm.activeAgent.ContextRatio*100)
	}

	set("turnCost", vm.metrics.TurnCostUSD)
	set("sessionCost", vm.metrics.SessionCostUSD)

	if vm.isTreeView {
		set("cacheMain", vm.metrics.WeightedCacheHitRate*100)
		set("cacheSub", vm.latestReq.CacheHitRate*100)
		set("ttftMain", vm.metrics.AvgTTFT.Seconds())
		set("ttftSub", vm.latestReq.TTFT.Seconds())
		set("tpsMain", vm.metrics.WeightedDecodeTPS)
		set("tpsSub", vm.latestReq.DecodeTPS)
	} else {
		set("cacheMain", vm.latestReq.CacheHitRate*100)
		set("cacheSub", vm.metrics.WeightedCacheHitRate*100)
		set("ttftMain", vm.latestReq.TTFT.Seconds())
		set("ttftSub", vm.metrics.AvgTTFT.Seconds())
		set("tpsMain", vm.latestReq.DecodeTPS)
		set("tpsSub", vm.metrics.WeightedDecodeTPS)
	}

	// Sync morphing curve targets with robust outlier-resistant Y-scaling so a single
	// micro-burst spike (e.g. 1000 t/s on 29 tokens) never crushes normal 120-160 t/s waves.
	slice := tailRequests(vm.requests, chartWindowLen)
	vals := make([]float64, len(slice))
	for i, r := range slice {
		vals[i] = metricValue(a.metric, r)
	}
	if a.metric == MetricCache {
		a.morpher.SetTargets(vals, 100)
	} else {
		scaled, ceiling := robustChartScale(vals)
		a.morpher.SetTargets(scaled, ceiling)
	}

	_, idx, _ := a.inspectedRequest(vm.requests)
	if idx >= 0 && len(slice) > 0 {
		frac := 0.5
		if len(slice) > 1 {
			frac = float64(idx) / float64(len(slice)-1)
		}
		a.morpher.SetCrosshairTarget(frac, instant)
	}
}

func (a *App) snapAllAnimations(conv model.ConversationSnapshot, vm scopeView) {
	a.updateAnimationTargets(conv, vm, true)
	a.morpher.Step(10)
}

func (a *App) renderEmptyState(c *ui.Context, pal theme.Palette) {
	tr := a.tr()
	c.Root().Background(pal.BackdropFrom)
	ui.Column(c).Fill().Center().Gap(8).TextColor(pal.Ink1).Children(func() {
		ui.Text(c, "Antigravity Monitor").FontSize(18).Bold()
		ui.Text(c, tr.WaitingData).FontSize(13).TextColor(pal.Ink2)
	})
}

func tailRequests(reqs []model.RequestRecord, maxN int) []model.RequestRecord {
	if len(reqs) <= maxN {
		return reqs
	}
	return reqs[len(reqs)-maxN:]
}

// robustChartScale compresses extreme single-request outliers via a smooth soft-knee
// above 1.6x P85 so normal telemetry waves remain readable while preserving relative peaks.
func robustChartScale(vals []float64) ([]float64, float64) {
	if len(vals) == 0 {
		return vals, 1
	}
	sorted := make([]float64, 0, len(vals))
	rawMax := 0.0
	for _, v := range vals {
		if v > rawMax {
			rawMax = v
		}
		if v > 0 {
			sorted = append(sorted, v)
		}
	}
	if rawMax <= 0 {
		return vals, 1
	}
	if len(sorted) < 4 {
		return vals, rawMax * 1.18
	}
	sort.Float64s(sorted)
	p85 := sorted[int(float64(len(sorted)-1)*0.85)]
	knee := p85 * 1.45
	if knee <= 0 || rawMax <= knee*1.25 {
		return vals, rawMax * 1.18
	}
	// Soft-knee compress values exceeding knee into [knee, knee*1.25].
	ceiling := knee * 1.32
	out := make([]float64, len(vals))
	for i, v := range vals {
		if v <= knee {
			out[i] = v
		} else {
			excess := (v - knee) / (rawMax - knee)
			out[i] = knee + (ceiling*0.95-knee)*math.Sqrt(excess)
		}
	}
	return out, ceiling
}

func metricTitle(tr i18n.Strings, m MetricKind) string {
	switch m {
	case MetricTTFT:
		return tr.MetricTTFT
	case MetricCache:
		return tr.MetricCache
	case MetricCost:
		return tr.MetricCost
	default:
		return tr.MetricTPS
	}
}

func metricValue(m MetricKind, r model.RequestRecord) float64 {
	switch m {
	case MetricTTFT:
		return r.TTFT.Seconds()
	case MetricCache:
		return r.CacheHitRate * 100
	case MetricCost:
		return r.CostUSD
	default:
		return r.DecodeTPS
	}
}

func fmtTokensK(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000.0)
	}
	return fmt.Sprintf("%d", n)
}

func fmtUSD(v float64) string {
	if math.IsNaN(v) || v < 0 {
		v = 0
	}
	return fmt.Sprintf("$%.3f", v)
}

func fmtPct(pct float64) string {
	if math.IsNaN(pct) || pct < 0 {
		pct = 0
	}
	return fmt.Sprintf("%.1f%%", pct)
}

func fmtSec(sec float64) string {
	if math.IsNaN(sec) || sec < 0 {
		sec = 0
	}
	return fmt.Sprintf("%.2fs", sec)
}

func fmtTPS(v float64) string {
	if math.IsNaN(v) || v < 0 {
		v = 0
	}
	return fmt.Sprintf("%.1f", v)
}

func shortName(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxRunes]) + "…"
}

// statusDot renders a quiet monochrome/accent status dot.
// Only requests 30fps breathing frames while running=true (0% GPU when idle).
func statusDot(c *ui.Context, key string, size float32, color ui.Color, breathing bool) ui.Element {
	dot := ui.Box(c.Key(key)).Size(size, size).Shrink(0)
	dot.Draw(func(p *ui.Painter, r ui.Rect) {
		cx := r.X + r.W/2
		cy := r.Y + r.H/2
		rad := min(r.W, r.H) / 2
		alpha := float32(1.0)
		scale := float32(1.0)
		if breathing && !p.Preferences().ReduceMotion {
			p.AnimationFrameRate(30)
			phase := float64(p.Now().UnixMilli()%2200) / 2200.0
			wave := float32(0.5 - 0.5*math.Cos(phase*2*math.Pi))
			alpha = 0.45 + 0.55*wave
			scale = 0.90 + 0.22*wave
		}
		path := (&ui.Path{}).Circle(cx, cy, rad*scale)
		p.FillPath(path, color.Alpha(float32(color.A)/255.0*alpha))
	})
	return dot
}
