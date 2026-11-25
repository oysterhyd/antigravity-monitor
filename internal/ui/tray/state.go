package tray

import (
	"fmt"
	"math"

	"github.com/oysterhyd/antigravity-monitor/internal/i18n"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

// Tab selects one of the 3 Pulse flyout views.
type Tab int

const (
	TabOverview Tab = iota
	TabPerf
	TabAgents
)

// TabLabels matches docs/prototype.html (#traySeg).
var TabLabels = [3]string{"总览", "性能", "Agent 树"}

const (
	// DotCols and DotRows match the 34x8 Pulse dot-matrix dimensions in prototype.html.
	DotCols = 34
	DotRows = 8
)

// ContextWarnLevel classifies context bar severity against Settings.ContextWarnThreshold.
type ContextWarnLevel uint8

const (
	ContextNormal ContextWarnLevel = iota
	ContextElevated
	ContextWarning
)

// State holds all pure UI state for the tray flyout. It is decoupled from mygo
// window handles so every transition and calculation is directly unit-testable.
type State struct {
	Tab             Tab
	SelectedConvID  string
	HoverDotCol     int
	SessionMenuOpen bool
	SettingsOpen    bool
	Settings        model.Settings
	Snapshot        model.AppSnapshot

	DotTargetsA [DotCols]float32
	DotTargetsB [DotCols]float32
	DotLevelsA  [DotCols]float32
	DotLevelsB  [DotCols]float32
	DotReqs     [DotCols]*model.RequestRecord
}

// NewState initializes tray flyout state with the given settings and initial snapshot.
func NewState(settings model.Settings, snap model.AppSnapshot) *State {
	st := &State{
		Tab:         TabOverview,
		HoverDotCol: -1,
		Settings:    settings,
	}
	st.ApplySnapshot(snap)
	// Start dot levels directly at initial targets so the first paint is crisp.
	st.DotLevelsA = st.DotTargetsA
	st.DotLevelsB = st.DotTargetsB
	return st
}

// ApplySnapshot updates the current AppSnapshot and follows the active
// conversation when AutoFollowActive is enabled (or when no valid selection exists).
func (s *State) ApplySnapshot(snap model.AppSnapshot) {
	s.Snapshot = snap
	if len(snap.Conversations) == 0 {
		s.SelectedConvID = ""
		s.syncDotTargets()
		return
	}

	if s.Settings.AutoFollowActive && snap.ActiveConversationID != "" {
		s.SelectedConvID = snap.ActiveConversationID
	} else if s.SelectedConvID == "" || !hasConversation(snap.Conversations, s.SelectedConvID) {
		if snap.ActiveConversationID != "" && hasConversation(snap.Conversations, snap.ActiveConversationID) {
			s.SelectedConvID = snap.ActiveConversationID
		} else {
			s.SelectedConvID = snap.Conversations[0].ID
		}
	}
	s.syncDotTargets()
}

// ApplySettings updates persisted settings and aligns auto-follow if re-enabled.
func (s *State) ApplySettings(settings model.Settings) {
	s.Settings = settings
	if settings.AutoFollowActive && s.Snapshot.ActiveConversationID != "" {
		s.SelectedConvID = s.Snapshot.ActiveConversationID
		s.syncDotTargets()
	}
}

// SetTab switches the active segmented tab and recomputes dot-matrix targets.
func (s *State) SetTab(tab Tab) {
	if tab < TabOverview || tab > TabAgents {
		return
	}
	s.Tab = tab
	s.syncDotTargets()
}

// SelectConversation switches the displayed conversation. A manual selection
// disables AutoFollowActive (pinning the user's choice), mirroring prototype.html.
func (s *State) SelectConversation(convID string, manual bool) {
	if convID == "" || !hasConversation(s.Snapshot.Conversations, convID) {
		return
	}
	s.SelectedConvID = convID
	if manual {
		s.Settings.AutoFollowActive = false
	}
	s.SessionMenuOpen = false
	s.HoverDotCol = -1
	s.syncDotTargets()
}

// ActiveConversation resolves the currently displayed conversation snapshot.
func (s *State) ActiveConversation() (model.ConversationSnapshot, bool) {
	return s.Snapshot.FindConversation(s.SelectedConvID)
}

// ComputeDotSeries computes the 34-column dual-channel dot targets and request
// alignment for a conversation and tab, matching docs/prototype.html lines 1516-1534.
func ComputeDotSeries(conv model.ConversationSnapshot, tab Tab) (targetsA, targetsB [DotCols]float32, reqs [DotCols]*model.RequestRecord) {
	n := len(conv.Requests)
	start := 0
	if n > DotCols {
		start = n - DotCols
	}
	recent := conv.Requests[start:]
	off := DotCols - len(recent)

	var maxCost float64
	if tab == TabAgents {
		for _, r := range recent {
			if r.CostUSD > maxCost {
				maxCost = r.CostUSD
			}
		}
	}

	for c := 0; c < DotCols; c++ {
		if c < off {
			continue
		}
		r := recent[c-off]
		recCopy := r
		reqs[c] = &recCopy

		switch tab {
		case TabOverview:
			targetsA[c] = clampFloat32(float32((r.DecodeTPS/170.0)*DotRows), 0, DotRows)
			targetsB[c] = clampFloat32(float32((r.CostUSD/0.024)*5.0), 0, DotRows-2)
		case TabPerf:
			targetsA[c] = clampFloat32(float32((r.DecodeTPS/170.0)*DotRows), 0, DotRows)
			targetsB[c] = clampFloat32(float32((r.TTFT.Seconds()/3.2)*6.0), 0, DotRows-1)
		case TabAgents:
			bar := float32(2)
			if maxCost > 0 && r.CostUSD > 0 {
				norm := math.Sqrt(r.CostUSD / maxCost)
				bar = clampFloat32(float32(1.0+norm*float64(DotRows-1)), 1, DotRows)
			}
			if r.AgentID == model.RootAgentID || r.AgentID == conv.Root.ID {
				targetsA[c] = bar
			} else {
				targetsB[c] = bar
			}
		}
	}
	return targetsA, targetsB, reqs
}

func (s *State) syncDotTargets() {
	conv, ok := s.ActiveConversation()
	if !ok {
		s.DotTargetsA = [DotCols]float32{}
		s.DotTargetsB = [DotCols]float32{}
		s.DotReqs = [DotCols]*model.RequestRecord{}
		return
	}
	s.DotTargetsA, s.DotTargetsB, s.DotReqs = ComputeDotSeries(conv, s.Tab)
}

// StepDotMatrix advances the 60fps smooth height interpolation toward targets.
// Returns true while any column is still moving, or false once settled (allowing
// the renderer to pause frame scheduling and achieve ~0% idle GPU).
func (s *State) StepDotMatrix(dtSec float32) bool {
	if dtSec <= 0 {
		dtSec = 1.0 / 60.0
	}
	if dtSec > 0.05 {
		dtSec = 0.05
	}
	// Frame-rate independent exponential smoothing matching prototype's 0.16 @ 60fps.
	alpha := float32(1.0 - math.Pow(1.0-0.16, float64(dtSec*60.0)))
	const eps = 0.015

	animating := false
	for c := 0; c < DotCols; c++ {
		da := s.DotTargetsA[c] - s.DotLevelsA[c]
		if float32(math.Abs(float64(da))) < eps {
			s.DotLevelsA[c] = s.DotTargetsA[c]
		} else {
			s.DotLevelsA[c] += da * alpha
			animating = true
		}

		db := s.DotTargetsB[c] - s.DotLevelsB[c]
		if float32(math.Abs(float64(db))) < eps {
			s.DotLevelsB[c] = s.DotTargetsB[c]
		} else {
			s.DotLevelsB[c] += db * alpha
			animating = true
		}
	}
	return animating
}

// HoverColumn maps a local x coordinate within width to a dot-matrix column index [0, DotCols-1].
func HoverColumn(localX, width float32) int {
	if width <= 0 || localX < 0 || localX >= width {
		return -1
	}
	col := int((localX / width) * DotCols)
	if col < 0 || col >= DotCols {
		return -1
	}
	return col
}

// HoverSummary returns the hovered RequestRecord and formatted top-right pill
// text ("#24 · 命中 96% · 1.62s · 126 t/s") for col, or (nil, "") if empty.
func (s *State) HoverSummary(col int) (*model.RequestRecord, string) {
	if col < 0 || col >= DotCols {
		return nil, ""
	}
	req := s.DotReqs[col]
	if req == nil {
		return nil, ""
	}
	hit := req.CacheHitRate
	if hit <= 0 && (req.InputTokens+req.CacheReadTokens+req.CacheWriteTokens) > 0 {
		hit = model.ComputeCacheHitRate(req.InputTokens, req.CacheReadTokens, req.CacheWriteTokens)
	}
	tr := i18n.Get(s.Settings.Language)
	label := fmt.Sprintf(tr.TrayHoverHitFmt,
		req.Seq, hit*100, req.TTFT.Seconds(), req.DecodeTPS)
	return req, label
}

// ContextWarn classifies a [0, 1] context ratio against the configured threshold.
func ContextWarn(ratio, warnThreshold float64) ContextWarnLevel {
	if warnThreshold > 0 && ratio >= warnThreshold {
		return ContextWarning
	}
	if warnThreshold > 0 && ratio >= warnThreshold*0.75 {
		return ContextElevated
	}
	return ContextNormal
}

// TrayTitle returns the compact status string suitable for macOS menu bar title
// or dock badge ("9.4% · 126 t/s").
func TrayTitle(snap model.AppSnapshot, selectedConvID string) string {
	conv, ok := snap.FindConversation(selectedConvID)
	if !ok {
		return "Antigravity"
	}
	tps := conv.TreeMetrics.WeightedDecodeTPS
	if conv.TreeMetrics.LatestRequest != nil && conv.TreeMetrics.LatestRequest.DecodeTPS > 0 {
		tps = conv.TreeMetrics.LatestRequest.DecodeTPS
	}
	return fmt.Sprintf("%.1f%% · %.0f t/s", conv.Root.ContextRatio*100, tps)
}

// TrayTooltip formats a crisp tooltip for Windows/Linux notification area hover.
func TrayTooltip(snap model.AppSnapshot, selectedConvID string) string {
	return TrayTooltipLang(snap, selectedConvID, i18n.LangZH)
}

// TrayTooltipLang formats a localized tooltip for Windows/Linux notification area hover.
func TrayTooltipLang(snap model.AppSnapshot, selectedConvID, lang string) string {
	tr := i18n.Get(lang)
	conv, ok := snap.FindConversation(selectedConvID)
	if !ok {
		return tr.TrayTooltipWaiting
	}
	tps := conv.TreeMetrics.WeightedDecodeTPS
	if conv.TreeMetrics.LatestRequest != nil && conv.TreeMetrics.LatestRequest.DecodeTPS > 0 {
		tps = conv.TreeMetrics.LatestRequest.DecodeTPS
	}
	return fmt.Sprintf(tr.TrayTooltipFmt,
		conv.Title,
		conv.Root.ContextRatio*100,
		FormatUSD(conv.TreeMetrics.TurnCostUSD),
		tps,
	)
}

func hasConversation(convs []model.ConversationSnapshot, id string) bool {
	for _, c := range convs {
		if c.ID == id {
			return true
		}
	}
	return false
}

func clampFloat32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
