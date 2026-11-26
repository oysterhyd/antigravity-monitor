package source

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"hash"
	"hash/fnv"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
)

// TranscriptPlanner holds plaintext telemetry from a PLANNER_RESPONSE line in transcript.jsonl,
// used both for timestamp alignment and as an L3 fallback when L2 Protobuf data is unavailable.
type TranscriptPlanner struct {
	StepIndex       int
	CreatedAt       time.Time
	Model           string
	InputTokens     int64
	CacheReadTokens int64
	OutputTokens    int64
}

// TranscriptData is an immutable snapshot of incrementally tailed transcript.jsonl data.
type TranscriptData struct {
	UserInputSteps []int
	UserInputTimes []time.Time
	StepTimestamps map[int]time.Time
	Planners       []TranscriptPlanner
	PlannersByStep map[int]TranscriptPlanner
	SubagentRoles  map[string]string // child conversationId -> Role
}

// LastUserInputTime returns the timestamp of the latest USER_INPUT event, or zero time.
func (d TranscriptData) LastUserInputTime() time.Time {
	if len(d.UserInputTimes) == 0 {
		return time.Time{}
	}
	return d.UserInputTimes[len(d.UserInputTimes)-1]
}

// TurnForStep computes the 1-based TurnIndex and InCurrentTurn flag for a given stepIndex.
// When transcript.jsonl has no USER_INPUT records yet, fallbackLastUserStep (from L1
// conversation_summaries.last_user_input_step_index) is used.
func (d TranscriptData) TurnForStep(stepIndex int, fallbackLastUserStep int) (turnIndex int, inCurrentTurn bool) {
	if len(d.UserInputSteps) > 0 {
		// Number of USER_INPUT steps with step <= stepIndex.
		cnt := sort.Search(len(d.UserInputSteps), func(i int) bool {
			return d.UserInputSteps[i] > stepIndex
		})
		if cnt == 0 {
			cnt = 1
		}
		lastStep := d.UserInputSteps[len(d.UserInputSteps)-1]
		return cnt, stepIndex >= lastStep
	}
	if fallbackLastUserStep >= 0 {
		inCur := stepIndex >= fallbackLastUserStep
		if inCur && fallbackLastUserStep > 0 {
			return 2, true
		}
		return 1, inCur
	}
	return 1, true
}

// FindPlannerForSteps returns the best matching TranscriptPlanner across a generation's
// explicit step indices. When one generation spans multiple steps (e.g. [6, 7]), the
// PLANNER_RESPONSE entry may live on a later step rather than steps[0].
func (d TranscriptData) FindPlannerForSteps(steps []int) (TranscriptPlanner, bool) {
	if len(d.PlannersByStep) == 0 || len(steps) == 0 {
		return TranscriptPlanner{}, false
	}
	var best TranscriptPlanner
	found := false
	for _, si := range steps {
		pl, ok := d.PlannersByStep[si]
		if !ok {
			continue
		}
		if !found {
			best = pl
			found = true
			continue
		}
		bestHasTokens := best.InputTokens > 0 || best.OutputTokens > 0 || best.CacheReadTokens > 0
		plHasTokens := pl.InputTokens > 0 || pl.OutputTokens > 0 || pl.CacheReadTokens > 0
		if !bestHasTokens && plHasTokens {
			if pl.Model == "" {
				pl.Model = best.Model
			}
			if pl.CreatedAt.IsZero() {
				pl.CreatedAt = best.CreatedAt
			}
			best = pl
			continue
		}
		if best.Model == "" && pl.Model != "" {
			best.Model = pl.Model
		}
		if best.CreatedAt.IsZero() && !pl.CreatedAt.IsZero() {
			best.CreatedAt = pl.CreatedAt
		}
	}
	return best, found
}

// TranscriptMatchResult pairs L2 GenMetadata rows with matching L3 TranscriptPlanners
// and collects unclaimed L3 planners representing additional unpersisted requests.
type TranscriptMatchResult struct {
	MatchedByGen      []*TranscriptPlanner
	UnclaimedPlanners []TranscriptPlanner
}

func genHasTokens(g GenMetadata) bool {
	return g.InputTokens > 0 || g.OutputTokens > 0 || g.CacheReadTokens > 0 || g.CacheWriteTokens > 0
}

func plannerHasTokens(pl TranscriptPlanner) bool {
	return pl.InputTokens > 0 || pl.OutputTokens > 0 || pl.CacheReadTokens > 0
}

func tokensMatchGenAndPlanner(g GenMetadata, pl TranscriptPlanner) (exact bool, partial bool) {
	if !genHasTokens(g) || !plannerHasTokens(pl) {
		return false, false
	}
	if g.InputTokens == pl.InputTokens && g.OutputTokens == pl.OutputTokens && g.CacheReadTokens == pl.CacheReadTokens {
		return true, true
	}
	matchedField := false
	if pl.OutputTokens > 0 {
		if g.OutputTokens != pl.OutputTokens {
			return false, false
		}
		matchedField = true
	}
	if pl.InputTokens > 0 {
		if g.InputTokens != pl.InputTokens {
			return false, false
		}
		matchedField = true
	}
	if pl.CacheReadTokens > 0 {
		if g.CacheReadTokens != pl.CacheReadTokens {
			return false, false
		}
		matchedField = true
	}
	return false, matchedField
}

func isEightDigitDate(s string) bool {
	if len(s) != 8 {
		return false
	}
	for i := 0; i < 8; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isNumericToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// canonicalModelKey normalizes either a model slug ("gpt-5.4", "gemini-4-neon-xhigh",
// "claude-sonnet-4-5-20250929") or a display label ("GPT-5.4", "Claude Opus 5.5 (High)",
// "Claude 4.5 Sonnet") into a version-preserving canonical key while stripping only
// effort/thinking tier qualifiers and release-date suffixes. Distinct model versions
// (e.g. "GPT-5.4" vs "gpt-5.5") always produce distinct keys.
func canonicalModelKey(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "models/")
	for {
		open := strings.IndexByte(s, '(')
		if open < 0 {
			break
		}
		closeIdx := strings.IndexByte(s[open:], ')')
		if closeIdx < 0 {
			break
		}
		inner := strings.TrimSpace(s[open+1 : open+closeIdx])
		switch inner {
		case "max", "xhigh", "high", "medium", "low", "minimal", "thinking":
			s = strings.TrimSpace(s[:open] + " " + s[open+closeIdx+1:])
		default:
			s = strings.TrimSpace(s[:open] + "-" + inner + "-" + s[open+closeIdx+1:])
		}
	}

	var b strings.Builder
	b.Grow(len(s))
	lastHyphen := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
			lastHyphen = false
		} else if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	norm := strings.TrimRight(b.String(), "-")
	if norm == "" {
		return ""
	}

	parts := strings.Split(norm, "-")
	for len(parts) > 1 {
		last := parts[len(parts)-1]
		if last == "max" || last == "xhigh" || last == "high" || last == "medium" || last == "low" || last == "minimal" || last == "thinking" || isEightDigitDate(last) {
			parts = parts[:len(parts)-1]
			continue
		}
		break
	}

	if len(parts) >= 3 && parts[0] == "claude" {
		last := parts[len(parts)-1]
		if (last == "opus" || last == "sonnet" || last == "haiku" || last == "fable") && isNumericToken(parts[1]) {
			reordered := make([]string, 0, len(parts))
			reordered = append(reordered, "claude", last)
			reordered = append(reordered, parts[1:len(parts)-1]...)
			parts = reordered
		}
	}

	return strings.Join(parts, "-")
}

func isUnresolvedModelID(calc *metrics.Calculator, s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	if calc != nil {
		return calc.IsUnresolvedModelEnum(s)
	}
	return metrics.IsPlaceholderModel(s)
}

// effectiveConcreteModelKey returns the version-exact canonical model key for (slug, display),
// falling back from an empty or unresolved placeholder slug (e.g. MODEL_PLACEHOLDER_M999) to
// a unique catalog alias lookup on display (e.g. "Orchestrator (Carbon)" -> "gemini-pro-orchestrator")
// and finally to canonicalModelKey(display). Returns "" when neither identifies a concrete model.
func effectiveConcreteModelKey(calc *metrics.Calculator, slug, disp string) string {
	resSlug := strings.TrimSpace(slug)
	resDisp := strings.TrimSpace(disp)
	if calc != nil {
		resSlug, resDisp = calc.ResolveModel(resSlug, resDisp)
	}
	if !isUnresolvedModelID(calc, resSlug) {
		if calc != nil {
			if aliasID := calc.ResolveModelByLabel(resSlug); aliasID != "" {
				return canonicalModelKey(aliasID)
			}
		}
		return canonicalModelKey(resSlug)
	}
	if !isUnresolvedModelID(calc, resDisp) {
		if calc != nil {
			if aliasID := calc.ResolveModelByLabel(resDisp); aliasID != "" {
				return canonicalModelKey(aliasID)
			}
		}
		return canonicalModelKey(resDisp)
	}
	return ""
}

type tokenTriple struct {
	in, out, cr int64
}

type modelTokenKey struct {
	model string
	tok   tokenTriple
}

type modelIntKey struct {
	model string
	val   int64
}

func popUnclaimed(q *[]int, claimed []bool) int {
	s := *q
	for len(s) > 0 {
		idx := s[0]
		s = s[1:]
		if !claimed[idx] {
			*q = s
			return idx
		}
	}
	*q = nil
	return -1
}

func popPartialTokenMatch(q *[]int, claimed []bool, planners []TranscriptPlanner, g GenMetadata) int {
	s := *q
	// Prune leading already-claimed items in amortized O(1).
	for len(s) > 0 && claimed[s[0]] {
		s = s[1:]
	}
	*q = s
	for k, idx := range s {
		if claimed[idx] {
			continue
		}
		if _, partial := tokensMatchGenAndPlanner(g, planners[idx]); partial {
			if k == 0 {
				*q = s[1:]
			}
			return idx
		}
	}
	return -1
}

// MatchPlanners matches L2 gen_metadata rows against L3 transcript planners in two phases:
//  1. Generations with explicit step indices (StepIndices / last_step_index+1) match and claim
//     every covered step (including cloned parent steps and confirmed empty failures).
//  2. Generations whose step index was estimated from GenIndex are matched in linear amortized
//     time across strict priority tiers (known-model exact matches before unknown-model wildcard
//     matches, and concrete token matches before zero-token backfills), while preventing
//     uncorroborated foreign parent records from swallowing child transcript requests.
func (d TranscriptData) MatchPlanners(gens []GenMetadata, convID string, calc *metrics.Calculator) TranscriptMatchResult {
	res := TranscriptMatchResult{
		MatchedByGen: make([]*TranscriptPlanner, len(gens)),
	}
	planners := d.Planners
	if len(planners) == 0 && len(d.PlannersByStep) > 0 {
		planners = make([]TranscriptPlanner, 0, len(d.PlannersByStep))
		for _, pl := range d.PlannersByStep {
			planners = append(planners, pl)
		}
		sort.Slice(planners, func(i, j int) bool {
			return planners[i].StepIndex < planners[j].StepIndex
		})
	}
	if len(planners) == 0 {
		return res
	}

	claimed := make([]bool, len(planners))
	var coveredSteps map[int]bool
	var estIdxs []int

	// Phase 1: Explicit step indices.
	for i, g := range gens {
		if g.StepIndexEstimated {
			estIdxs = append(estIdxs, i)
			continue
		}
		known := g.KnownStepIndices()
		if len(known) > 0 {
			if coveredSteps == nil {
				coveredSteps = make(map[int]bool, len(gens))
			}
			for _, si := range known {
				if si >= 0 {
					coveredSteps[si] = true
				}
			}
			if g.BelongsTo(convID) && !g.IsEmptyFailure() {
				if pl, ok := d.FindPlannerForSteps(known); ok {
					p := pl
					res.MatchedByGen[i] = &p
				}
			}
		}
	}
	if len(coveredSteps) > 0 {
		for j, pl := range planners {
			if coveredSteps[pl.StepIndex] {
				claimed[j] = true
			}
		}
	}

	// Phase 2: Linear priority-tiered matching for estimated-step generations using indexed FIFO queues.
	if len(estIdxs) > 0 {
		type estMeta struct {
			idx     int
			key     string
			tok     tokenTriple
			hasTok  bool
			belongs bool
			isFail  bool
		}
		est := make([]estMeta, len(estIdxs))
		for k, i := range estIdxs {
			g := gens[i]
			est[k] = estMeta{
				idx:     i,
				key:     effectiveConcreteModelKey(calc, g.ModelSlug, g.ModelDisplay),
				tok:     tokenTriple{in: g.InputTokens, out: g.OutputTokens, cr: g.CacheReadTokens},
				hasTok:  genHasTokens(g),
				belongs: g.BelongsTo(convID),
				isFail:  g.IsEmptyFailure(),
			}
		}

		byModelTok := make(map[modelTokenKey][]int)
		byModelOut := make(map[modelIntKey][]int)
		byModelIn := make(map[modelIntKey][]int)
		byModelCR := make(map[modelIntKey][]int)
		byModelZero := make(map[string][]int)
		byModelNonZero := make(map[string][]int)

		byTokAll := make(map[tokenTriple][]int)
		byTokEmptyModel := make(map[tokenTriple][]int)
		byOutAll := make(map[int64][]int)
		byInAll := make(map[int64][]int)
		byCRAll := make(map[int64][]int)
		byOutEmptyModel := make(map[int64][]int)
		byInEmptyModel := make(map[int64][]int)
		byCREmptyModel := make(map[int64][]int)

		var zeroAll []int
		var zeroEmptyModel []int
		var nonZeroAll []int
		var nonZeroEmptyModel []int

		for j, pl := range planners {
			if claimed[j] {
				continue
			}
			plKey := effectiveConcreteModelKey(calc, pl.Model, "")
			plTok := tokenTriple{in: pl.InputTokens, out: pl.OutputTokens, cr: pl.CacheReadTokens}
			plHasTok := plannerHasTokens(pl)

			if plHasTok {
				nonZeroAll = append(nonZeroAll, j)
				byTokAll[plTok] = append(byTokAll[plTok], j)
				if pl.OutputTokens > 0 {
					byOutAll[pl.OutputTokens] = append(byOutAll[pl.OutputTokens], j)
				}
				if pl.InputTokens > 0 {
					byInAll[pl.InputTokens] = append(byInAll[pl.InputTokens], j)
				}
				if pl.CacheReadTokens > 0 {
					byCRAll[pl.CacheReadTokens] = append(byCRAll[pl.CacheReadTokens], j)
				}
				if plKey != "" {
					byModelTok[modelTokenKey{model: plKey, tok: plTok}] = append(byModelTok[modelTokenKey{model: plKey, tok: plTok}], j)
					byModelNonZero[plKey] = append(byModelNonZero[plKey], j)
					if pl.OutputTokens > 0 {
						byModelOut[modelIntKey{model: plKey, val: pl.OutputTokens}] = append(byModelOut[modelIntKey{model: plKey, val: pl.OutputTokens}], j)
					}
					if pl.InputTokens > 0 {
						byModelIn[modelIntKey{model: plKey, val: pl.InputTokens}] = append(byModelIn[modelIntKey{model: plKey, val: pl.InputTokens}], j)
					}
					if pl.CacheReadTokens > 0 {
						byModelCR[modelIntKey{model: plKey, val: pl.CacheReadTokens}] = append(byModelCR[modelIntKey{model: plKey, val: pl.CacheReadTokens}], j)
					}
				} else {
					nonZeroEmptyModel = append(nonZeroEmptyModel, j)
					byTokEmptyModel[plTok] = append(byTokEmptyModel[plTok], j)
					if pl.OutputTokens > 0 {
						byOutEmptyModel[pl.OutputTokens] = append(byOutEmptyModel[pl.OutputTokens], j)
					}
					if pl.InputTokens > 0 {
						byInEmptyModel[pl.InputTokens] = append(byInEmptyModel[pl.InputTokens], j)
					}
					if pl.CacheReadTokens > 0 {
						byCREmptyModel[pl.CacheReadTokens] = append(byCREmptyModel[pl.CacheReadTokens], j)
					}
				}
			} else {
				zeroAll = append(zeroAll, j)
				if plKey != "" {
					byModelZero[plKey] = append(byModelZero[plKey], j)
				} else {
					zeroEmptyModel = append(zeroEmptyModel, j)
				}
			}
		}

		matched := make([]bool, len(est))
		assign := func(k, j int) {
			matched[k] = true
			claimed[j] = true
			m := est[k]
			if m.belongs && !m.isFail {
				p := planners[j]
				res.MatchedByGen[m.idx] = &p
			}
		}

		// Tier 60: Concrete model exact match + exact token match.
		for k, m := range est {
			if matched[k] || m.isFail || !m.hasTok || m.key == "" {
				continue
			}
			mk := modelTokenKey{model: m.key, tok: m.tok}
			if q := byModelTok[mk]; len(q) > 0 {
				if j := popUnclaimed(&q, claimed); j >= 0 {
					byModelTok[mk] = q
					assign(k, j)
				} else {
					delete(byModelTok, mk)
				}
			}
		}

		// Tier 50: Concrete model exact match + partial token match.
		for k, m := range est {
			if matched[k] || m.isFail || !m.hasTok || m.key == "" {
				continue
			}
			g := gens[m.idx]
			j := -1
			if m.tok.out > 0 {
				mk := modelIntKey{model: m.key, val: m.tok.out}
				if q := byModelOut[mk]; len(q) > 0 {
					j = popPartialTokenMatch(&q, claimed, planners, g)
					byModelOut[mk] = q
				}
			}
			if j < 0 && m.tok.in > 0 {
				mk := modelIntKey{model: m.key, val: m.tok.in}
				if q := byModelIn[mk]; len(q) > 0 {
					j = popPartialTokenMatch(&q, claimed, planners, g)
					byModelIn[mk] = q
				}
			}
			if j < 0 && m.tok.cr > 0 {
				mk := modelIntKey{model: m.key, val: m.tok.cr}
				if q := byModelCR[mk]; len(q) > 0 {
					j = popPartialTokenMatch(&q, claimed, planners, g)
					byModelCR[mk] = q
				}
			}
			if j >= 0 {
				assign(k, j)
			}
		}

		// Tier 45: Empty failure + concrete model exact match + zero-token planner.
		for k, m := range est {
			if matched[k] || !m.isFail || m.key == "" {
				continue
			}
			if q := byModelZero[m.key]; len(q) > 0 {
				if j := popUnclaimed(&q, claimed); j >= 0 {
					byModelZero[m.key] = q
					assign(k, j)
				} else {
					delete(byModelZero, m.key)
				}
			}
		}

		// Tier 40: Wildcard model (either DB unknown or transcript empty model) + exact token match.
		for k, m := range est {
			if matched[k] || m.isFail || !m.hasTok {
				continue
			}
			var qMap map[tokenTriple][]int
			if m.key == "" {
				qMap = byTokAll
			} else {
				qMap = byTokEmptyModel
			}
			if q := qMap[m.tok]; len(q) > 0 {
				if j := popUnclaimed(&q, claimed); j >= 0 {
					qMap[m.tok] = q
					assign(k, j)
				} else {
					delete(qMap, m.tok)
				}
			}
		}

		// Tier 35: Wildcard model + partial token match.
		for k, m := range est {
			if matched[k] || m.isFail || !m.hasTok {
				continue
			}
			g := gens[m.idx]
			outMap, inMap, crMap := byOutEmptyModel, byInEmptyModel, byCREmptyModel
			if m.key == "" {
				outMap, inMap, crMap = byOutAll, byInAll, byCRAll
			}
			j := -1
			if m.tok.out > 0 {
				if q := outMap[m.tok.out]; len(q) > 0 {
					j = popPartialTokenMatch(&q, claimed, planners, g)
					outMap[m.tok.out] = q
				}
			}
			if j < 0 && m.tok.in > 0 {
				if q := inMap[m.tok.in]; len(q) > 0 {
					j = popPartialTokenMatch(&q, claimed, planners, g)
					inMap[m.tok.in] = q
				}
			}
			if j < 0 && m.tok.cr > 0 {
				if q := crMap[m.tok.cr]; len(q) > 0 {
					j = popPartialTokenMatch(&q, claimed, planners, g)
					crMap[m.tok.cr] = q
				}
			}
			if j >= 0 {
				assign(k, j)
			}
		}

		// Tier 30: Concrete model exact match + DB has tokens + transcript omitted tokens.
		for k, m := range est {
			if matched[k] || m.isFail || !m.hasTok || m.key == "" {
				continue
			}
			if q := byModelZero[m.key]; len(q) > 0 {
				if j := popUnclaimed(&q, claimed); j >= 0 {
					byModelZero[m.key] = q
					assign(k, j)
				} else {
					delete(byModelZero, m.key)
				}
			}
		}

		// Tier 25: Own empty failure + wildcard model + zero-token planner.
		for k, m := range est {
			if matched[k] || !m.isFail || !m.belongs {
				continue
			}
			qPtr := &zeroEmptyModel
			if m.key == "" {
				qPtr = &zeroAll
			}
			if j := popUnclaimed(qPtr, claimed); j >= 0 {
				assign(k, j)
			}
		}

		// Tier 20: Own zero-token non-failure + concrete model exact match + non-zero planner.
		for k, m := range est {
			if matched[k] || m.isFail || m.hasTok || !m.belongs || m.key == "" {
				continue
			}
			if q := byModelNonZero[m.key]; len(q) > 0 {
				if j := popUnclaimed(&q, claimed); j >= 0 {
					byModelNonZero[m.key] = q
					assign(k, j)
				} else {
					delete(byModelNonZero, m.key)
				}
			}
		}

		// Tier 15: Own zero-token non-failure + concrete model exact match + zero-token planner.
		for k, m := range est {
			if matched[k] || m.isFail || m.hasTok || !m.belongs || m.key == "" {
				continue
			}
			if q := byModelZero[m.key]; len(q) > 0 {
				if j := popUnclaimed(&q, claimed); j >= 0 {
					byModelZero[m.key] = q
					assign(k, j)
				} else {
					delete(byModelZero, m.key)
				}
			}
		}

		// Tier 10: Own zero-token non-failure + wildcard model + non-zero planner.
		for k, m := range est {
			if matched[k] || m.isFail || m.hasTok || !m.belongs {
				continue
			}
			qPtr := &nonZeroEmptyModel
			if m.key == "" {
				qPtr = &nonZeroAll
			}
			if j := popUnclaimed(qPtr, claimed); j >= 0 {
				assign(k, j)
			}
		}

		// Tier 5: Own zero-token non-failure + wildcard model + zero-token planner.
		for k, m := range est {
			if matched[k] || m.isFail || m.hasTok || !m.belongs {
				continue
			}
			qPtr := &zeroEmptyModel
			if m.key == "" {
				qPtr = &zeroAll
			}
			if j := popUnclaimed(qPtr, claimed); j >= 0 {
				assign(k, j)
			}
		}
	}

	for j, pl := range planners {
		if !claimed[j] {
			res.UnclaimedPlanners = append(res.UnclaimedPlanners, pl)
		}
	}
	return res
}

const (
	tailerSameSizeRecentWindow = 2 * time.Second
	tailerFullPrefixHashLimit  = 64 * 1024
	tailerEdgeFingerprintBytes = 4 * 1024
)

// TranscriptTailer incrementally tails a single brain/<conv_id>/.system_generated/logs/transcript.jsonl
// by byte offset so repeated syncs perform O(delta) I/O.
type TranscriptTailer struct {
	path        string
	offset      int64
	hasFile     bool
	lastInfo    os.FileInfo
	lastMtimeNs int64
	hasher      hash.Hash64
	lastHash    uint64
	headBytes   []byte
	tailBytes   []byte

	userInputSteps        []int
	userInputTimes        []time.Time
	stepTimestamps        map[int]time.Time
	planners              []TranscriptPlanner
	plannersByStep        map[int]TranscriptPlanner
	pendingSubagentByStep map[int]string
	pendingSubagentQueue  []string
	subagentRoles         map[string]string
}

// NewTranscriptTailer constructs an incremental tailer for the given transcript.jsonl path.
func NewTranscriptTailer(transcriptPath string) *TranscriptTailer {
	t := &TranscriptTailer{path: transcriptPath}
	t.resetLocked()
	return t
}

// TranscriptPath returns the canonical L3 transcript path for a conversation under rootDir.
func TranscriptPath(rootDir, convID string) string {
	return filepath.Join(rootDir, "brain", convID, ".system_generated", "logs", "transcript.jsonl")
}

func (t *TranscriptTailer) resetLocked() {
	t.offset = 0
	t.hasFile = false
	t.lastInfo = nil
	t.lastMtimeNs = 0
	t.hasher = fnv.New64a()
	t.lastHash = 0
	t.headBytes = t.headBytes[:0]
	t.tailBytes = t.tailBytes[:0]
	t.userInputSteps = nil
	t.userInputTimes = nil
	t.stepTimestamps = make(map[int]time.Time)
	t.planners = nil
	t.plannersByStep = make(map[int]TranscriptPlanner)
	t.pendingSubagentByStep = make(map[int]string)
	t.pendingSubagentQueue = nil
	t.subagentRoles = make(map[string]string)
}

func (t *TranscriptTailer) recordEdgeFingerprintLocked(b []byte) {
	if len(b) == 0 {
		return
	}
	if len(t.headBytes) < tailerEdgeFingerprintBytes {
		need := tailerEdgeFingerprintBytes - len(t.headBytes)
		if need > len(b) {
			need = len(b)
		}
		t.headBytes = append(t.headBytes, b[:need]...)
	}
	if len(b) >= tailerEdgeFingerprintBytes {
		t.tailBytes = append(t.tailBytes[:0], b[len(b)-tailerEdgeFingerprintBytes:]...)
	} else {
		total := len(t.tailBytes) + len(b)
		if total > tailerEdgeFingerprintBytes {
			drop := total - tailerEdgeFingerprintBytes
			copy(t.tailBytes, t.tailBytes[drop:])
			t.tailBytes = t.tailBytes[:len(t.tailBytes)-drop]
		}
		t.tailBytes = append(t.tailBytes, b...)
	}
}

func (t *TranscriptTailer) hasCachedDataLocked() bool {
	return t.offset > 0 ||
		len(t.planners) > 0 ||
		len(t.userInputSteps) > 0 ||
		len(t.stepTimestamps) > 0 ||
		len(t.subagentRoles) > 0
}

func (t *TranscriptTailer) verifyPrefixUnchangedLocked(f *os.File) (bool, error) {
	if t.offset <= 0 {
		return true, nil
	}
	if t.offset <= tailerFullPrefixHashLimit {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return false, err
		}
		h := fnv.New64a()
		n, err := io.CopyN(h, f, t.offset)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
		if n != t.offset {
			return false, nil
		}
		return h.Sum64() == t.lastHash, nil
	}
	// For large transcripts (>64 KiB), verify strictly O(1) head and tail boundary windows
	// (up to 4 KiB each) so incremental appends stay strictly O(delta) regardless of file size
	// or PLANNER_RESPONSE density while still catching deletion, truncation, atomic replacement,
	// same-length overwrites, and full-file rewrites.
	var buf [tailerEdgeFingerprintBytes]byte
	if len(t.headBytes) > 0 {
		b := buf[:len(t.headBytes)]
		if _, err := f.ReadAt(b, 0); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return false, nil
			}
			return false, err
		}
		if !bytes.Equal(b, t.headBytes) {
			return false, nil
		}
	}
	if len(t.tailBytes) > 0 {
		b := buf[:len(t.tailBytes)]
		if _, err := f.ReadAt(b, t.offset-int64(len(t.tailBytes))); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return false, nil
			}
			return false, err
		}
		if !bytes.Equal(b, t.tailBytes) {
			return false, nil
		}
	}
	return true, nil
}

// Sync reads any newly appended complete lines since the last byte offset,
// resetting cached state if the transcript file was deleted, truncated, or replaced.
func (t *TranscriptTailer) Sync() (changed bool, err error) {
	fi, err := os.Stat(t.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			hadData := t.hasCachedDataLocked()
			if t.hasFile || hadData {
				t.resetLocked()
			}
			return hadData, nil
		}
		return false, err
	}

	size := fi.Size()
	mtimeNs := fi.ModTime().UnixNano()
	replaced := false
	needPrefixVerify := false
	if t.hasFile {
		if t.lastInfo != nil && !os.SameFile(t.lastInfo, fi) {
			replaced = true
		} else if size < t.offset {
			replaced = true
		} else if size == t.offset && t.offset > 0 {
			if mtimeNs != t.lastMtimeNs {
				replaced = true
			} else if size <= tailerFullPrefixHashLimit || time.Since(fi.ModTime()) <= tailerSameSizeRecentWindow {
				needPrefixVerify = true
			}
		} else if size > t.offset && t.offset > 0 {
			needPrefixVerify = true
		}
	}

	if !replaced && !needPrefixVerify && t.hasFile && size == t.offset {
		t.lastMtimeNs = mtimeNs
		return false, nil
	}

	f, err := os.Open(t.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			hadData := t.hasCachedDataLocked()
			t.resetLocked()
			return hadData, nil
		}
		return false, err
	}
	defer f.Close()

	if !replaced && needPrefixVerify {
		unchanged, vErr := t.verifyPrefixUnchangedLocked(f)
		if vErr != nil {
			return false, vErr
		}
		if !unchanged {
			replaced = true
		} else if size == t.offset {
			t.lastMtimeNs = mtimeNs
			return false, nil
		}
	}

	prevHash := t.lastHash
	hadDataBeforeReset := false
	if replaced {
		hadDataBeforeReset = t.hasCachedDataLocked()
		t.resetLocked()
	}

	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return hadDataBeforeReset, err
	}

	t.hasFile = true
	// Use f.Stat() on the open file handle so Windows eagerly populates the file ID
	// (vol/idxhi/idxlo) before any later atomic os.Rename replaces the path.
	if openFi, statErr := f.Stat(); statErr == nil {
		t.lastInfo = openFi
	} else {
		_ = os.SameFile(fi, fi)
		t.lastInfo = fi
	}
	t.lastMtimeNs = mtimeNs

	reader := bufio.NewReaderSize(f, 32*1024)
	var partial []byte
	parsedAny := false
	for {
		line, readErr := reader.ReadSlice('\n')
		switch readErr {
		case io.EOF:
			if replaced {
				return t.lastHash != prevHash || (hadDataBeforeReset != t.hasCachedDataLocked()), nil
			}
			return parsedAny, nil
		case bufio.ErrBufferFull:
			partial = append(partial, line...)
			continue
		case nil:
		default:
			return parsedAny || hadDataBeforeReset, readErr
		}
		if len(partial) > 0 {
			_, _ = t.hasher.Write(partial)
			t.recordEdgeFingerprintLocked(partial)
		}
		_, _ = t.hasher.Write(line)
		t.recordEdgeFingerprintLocked(line)
		t.lastHash = t.hasher.Sum64()
		t.offset += int64(len(partial) + len(line))
		rawLine := line
		if len(partial) > 0 {
			partial = append(partial, line...)
			rawLine = partial
			partial = partial[:0]
		}
		if trimmed := bytes.TrimSpace(rawLine); len(trimmed) > 0 {
			t.parseLine(trimmed)
			parsedAny = true
		}
	}
}

// Snapshot returns a safe copy of the parsed transcript state.
func (t *TranscriptTailer) Snapshot() TranscriptData {
	return TranscriptData{
		UserInputSteps: slices.Clone(t.userInputSteps),
		UserInputTimes: slices.Clone(t.userInputTimes),
		StepTimestamps: cloneMap(t.stepTimestamps),
		Planners:       slices.Clone(t.planners),
		PlannersByStep: cloneMap(t.plannersByStep),
		SubagentRoles:  cloneMap(t.subagentRoles),
	}
}

func cloneMap[K comparable, V any](src map[K]V) map[K]V {
	if len(src) == 0 {
		return nil
	}
	return maps.Clone(src)
}

func (t *TranscriptTailer) data() TranscriptData {
	return TranscriptData{
		UserInputSteps: t.userInputSteps,
		UserInputTimes: t.userInputTimes,
		StepTimestamps: t.stepTimestamps,
		Planners:       t.planners,
		PlannersByStep: t.plannersByStep,
		SubagentRoles:  t.subagentRoles,
	}
}

type transcriptLine struct {
	StepIndex       int                  `json:"step_index"`
	Type            string               `json:"type"`
	CreatedAt       string               `json:"created_at"`
	Model           string               `json:"model"`
	InputTokens     int64                `json:"input_tokens"`
	CacheReadTokens int64                `json:"cache_read_tokens"`
	OutputTokens    int64                `json:"output_tokens"`
	ToolCalls       []transcriptToolCall `json:"tool_calls"`
}

type transcriptContentOnly struct {
	Content string `json:"content"`
}

type transcriptToolCall struct {
	Name string            `json:"name"`
	Args map[string]string `json:"args"`
}

var convIDMarker = []byte("conversationId")

func extractSubagentConvID(content string) (string, bool) {
	const needle = `"conversationId"`
	pos := strings.Index(content, needle)
	if pos < 0 {
		return "", false
	}
	rest := content[pos+len(needle):]
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\r' || rest[i] == '\n') {
		i++
	}
	if i >= len(rest) || rest[i] != ':' {
		return "", false
	}
	i++
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\r' || rest[i] == '\n') {
		i++
	}
	if i >= len(rest) || rest[i] != '"' {
		return "", false
	}
	start := i + 1
	end := strings.IndexByte(rest[start:], '"')
	if end <= 0 {
		return "", false
	}
	return rest[start : start+end], true
}

func (t *TranscriptTailer) parseLine(line []byte) {
	var entry transcriptLine
	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}

	ts := parseSQLiteTime(entry.CreatedAt)
	if !ts.IsZero() {
		t.stepTimestamps[entry.StepIndex] = ts
	}

	switch entry.Type {
	case "USER_INPUT":
		// Maintain sorted unique USER_INPUT step indices.
		n := len(t.userInputSteps)
		if n == 0 || entry.StepIndex > t.userInputSteps[n-1] {
			t.userInputSteps = append(t.userInputSteps, entry.StepIndex)
			t.userInputTimes = append(t.userInputTimes, ts)
		} else {
			idx := sort.SearchInts(t.userInputSteps, entry.StepIndex)
			if idx < n && t.userInputSteps[idx] == entry.StepIndex {
				if !ts.IsZero() {
					t.userInputTimes[idx] = ts
				}
			} else {
				t.userInputSteps = append(t.userInputSteps, 0)
				t.userInputTimes = append(t.userInputTimes, time.Time{})
				copy(t.userInputSteps[idx+1:], t.userInputSteps[idx:])
				copy(t.userInputTimes[idx+1:], t.userInputTimes[idx:])
				t.userInputSteps[idx] = entry.StepIndex
				t.userInputTimes[idx] = ts
			}
		}

	case "PLANNER_RESPONSE":
		p := TranscriptPlanner{
			StepIndex:       entry.StepIndex,
			CreatedAt:       ts,
			Model:           strings.TrimSpace(entry.Model),
			InputTokens:     entry.InputTokens,
			CacheReadTokens: entry.CacheReadTokens,
			OutputTokens:    entry.OutputTokens,
		}
		if _, exists := t.plannersByStep[entry.StepIndex]; exists {
			existingIdx, _ := findPlannerIndex(t.planners, entry.StepIndex)
			t.planners[existingIdx] = p
		} else {
			t.planners = append(t.planners, p)
		}
		t.plannersByStep[entry.StepIndex] = p

		// Correlate invoke_subagent tool calls with their subsequent tool result step.
		for tcIdx, tc := range entry.ToolCalls {
			if tc.Name != "invoke_subagent" {
				continue
			}
			role := unquoteToolArg(tc.Args["Role"])
			if role == "" {
				role = unquoteToolArg(tc.Args["TypeName"])
			}
			if role != "" {
				expectedResultStep := entry.StepIndex + 1 + tcIdx
				t.pendingSubagentByStep[expectedResultStep] = role
				t.pendingSubagentQueue = append(t.pendingSubagentQueue, role)
			}
		}

	default:
		// Look for subagent creation response containing "conversationId".
		// Defer decoding the potentially large "content" field until we confirm the marker exists.
		if bytes.Contains(line, convIDMarker) {
			var contentEntry transcriptContentOnly
			if err := json.Unmarshal(line, &contentEntry); err == nil && contentEntry.Content != "" {
				if childID, ok := extractSubagentConvID(contentEntry.Content); ok {
					role := ""
					if r, ok := t.pendingSubagentByStep[entry.StepIndex]; ok {
						role = r
						delete(t.pendingSubagentByStep, entry.StepIndex)
						t.removeFromQueue(r)
					} else if len(t.pendingSubagentQueue) > 0 {
						role = t.pendingSubagentQueue[0]
						t.pendingSubagentQueue = t.pendingSubagentQueue[1:]
					}
					if role != "" {
						t.subagentRoles[childID] = role
					}
				}
			}
		}
	}
}

func (t *TranscriptTailer) removeFromQueue(role string) {
	for i, r := range t.pendingSubagentQueue {
		if r == role {
			t.pendingSubagentQueue = append(t.pendingSubagentQueue[:i], t.pendingSubagentQueue[i+1:]...)
			return
		}
	}
}

func findPlannerIndex(planners []TranscriptPlanner, stepIndex int) (int, bool) {
	for i := len(planners) - 1; i >= 0; i-- {
		if planners[i].StepIndex == stepIndex {
			return i, true
		}
	}
	return -1, false
}

// unquoteToolArg cleans Antigravity transcript tool_calls args, which store JSON-encoded strings
// inside string values (e.g., `"\"Codebase Researcher\""` -> `"Codebase Researcher"`).
func unquoteToolArg(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if unq, err := strconv.Unquote(s); err == nil {
			s = unq
		} else {
			var decoded string
			if err := json.Unmarshal([]byte(s), &decoded); err == nil {
				s = decoded
			}
		}
	}
	return strings.TrimSpace(s)
}

// SubagentDescriptor supplies the candidate naming inputs for a subagent node.
type SubagentDescriptor struct {
	ID               string
	SummaryAgentName string
	SubagentType     string
	MetaRole         string
	MetaTypeName     string
}

// ResolvedAgentName holds the final display name and raw role/type metadata for a subagent.
type ResolvedAgentName struct {
	Name string
	Role string
}

// ResolveSubagentNames implements the multi-tier Subagent naming strategy:
//  1. Parent transcript invoke_subagent Role parameter
//  2. Child trajectory_metadata_blob subagentSpec.role (L2 DB fallback when L3 transcript is absent)
//  3. conversation_summaries.agent_name (or trajectory_metadata_blob typeName / L2 f1.f20["subagent_type"])
//  4. Duplicate base names under the same parent are deterministically numbered: "<name> #1", "<name> #2".
func ResolveSubagentNames(children []SubagentDescriptor, parentRoles map[string]string) map[string]ResolvedAgentName {
	out := make(map[string]ResolvedAgentName, len(children))
	if len(children) == 0 {
		return out
	}

	baseNames := make([]string, len(children))
	roles := make([]string, len(children))
	counts := make(map[string]int, len(children))

	for i, c := range children {
		roleFromParent := strings.TrimSpace(parentRoles[c.ID])
		metaRole := strings.TrimSpace(c.MetaRole)
		agentName := strings.TrimSpace(c.SummaryAgentName)
		metaType := strings.TrimSpace(c.MetaTypeName)
		subType := strings.TrimSpace(c.SubagentType)

		base := roleFromParent
		if base == "" {
			base = metaRole
		}
		if base == "" {
			base = agentName
		}
		if base == "" {
			base = metaType
		}
		if base == "" {
			base = subType
		}
		if base == "" {
			base = "subagent"
		}

		role := agentName
		if role == "" {
			role = metaType
		}
		if role == "" {
			role = subType
		}
		if role == "" {
			role = metaRole
		}
		if role == "" {
			role = roleFromParent
		}

		baseNames[i] = base
		roles[i] = role
		counts[base]++
	}

	seen := make(map[string]int, len(counts))
	for i, c := range children {
		base := baseNames[i]
		name := base
		if counts[base] > 1 {
			seen[base]++
			name = base + " #" + strconv.Itoa(seen[base])
		}
		out[c.ID] = ResolvedAgentName{
			Name: name,
			Role: roles[i],
		}
	}
	return out
}
