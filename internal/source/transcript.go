package source

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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

// TranscriptTailer incrementally tails a single brain/<conv_id>/.system_generated/logs/transcript.jsonl
// by byte offset so repeated syncs perform O(delta) I/O.
type TranscriptTailer struct {
	path   string
	offset int64

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
	t.userInputSteps = nil
	t.userInputTimes = nil
	t.stepTimestamps = make(map[int]time.Time)
	t.planners = nil
	t.plannersByStep = make(map[int]TranscriptPlanner)
	t.pendingSubagentByStep = make(map[int]string)
	t.pendingSubagentQueue = nil
	t.subagentRoles = make(map[string]string)
}

// Sync reads any newly appended complete lines since the last byte offset.
func (t *TranscriptTailer) Sync() (changed bool, err error) {
	fi, err := os.Stat(t.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	size := fi.Size()
	if size < t.offset {
		t.resetLocked()
		changed = true
	}
	if size == t.offset {
		return changed, nil
	}

	f, err := os.Open(t.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return false, err
	}

	raw, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}
	if len(raw) == 0 {
		return changed, nil
	}

	// Only consume up to the last newline so a half-flushed line is retried on the next sync.
	lastNL := bytes.LastIndexByte(raw, '\n')
	if lastNL < 0 {
		return changed, nil
	}
	complete := raw[:lastNL+1]
	t.offset += int64(len(complete))

	lines := bytes.Split(complete, []byte{'\n'})
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		t.parseLine(line)
		changed = true
	}
	return changed, nil
}

// Snapshot returns a safe copy of the parsed transcript state.
func (t *TranscriptTailer) Snapshot() TranscriptData {
	d := TranscriptData{
		UserInputSteps: append([]int(nil), t.userInputSteps...),
		UserInputTimes: append([]time.Time(nil), t.userInputTimes...),
		Planners:       append([]TranscriptPlanner(nil), t.planners...),
	}
	if len(t.stepTimestamps) > 0 {
		d.StepTimestamps = make(map[int]time.Time, len(t.stepTimestamps))
		for k, v := range t.stepTimestamps {
			d.StepTimestamps[k] = v
		}
	}
	if len(t.plannersByStep) > 0 {
		d.PlannersByStep = make(map[int]TranscriptPlanner, len(t.plannersByStep))
		for k, v := range t.plannersByStep {
			d.PlannersByStep[k] = v
		}
	}
	if len(t.subagentRoles) > 0 {
		d.SubagentRoles = make(map[string]string, len(t.subagentRoles))
		for k, v := range t.subagentRoles {
			d.SubagentRoles[k] = v
		}
	}
	return d
}

type transcriptLine struct {
	StepIndex       int                  `json:"step_index"`
	Type            string               `json:"type"`
	CreatedAt       string               `json:"created_at"`
	Model           string               `json:"model"`
	InputTokens     int64                `json:"input_tokens"`
	CacheReadTokens int64                `json:"cache_read_tokens"`
	OutputTokens    int64                `json:"output_tokens"`
	Content         string               `json:"content"`
	ToolCalls       []transcriptToolCall `json:"tool_calls"`
}

type transcriptToolCall struct {
	Name string            `json:"name"`
	Args map[string]string `json:"args"`
}

var reSubagentConvID = regexp.MustCompile(`"conversationId"\s*:\s*"([^"]+)"`)

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
		if existingIdx, ok := findPlannerIndex(t.planners, entry.StepIndex); ok {
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
		if entry.Content != "" && strings.Contains(entry.Content, "conversationId") {
			if m := reSubagentConvID.FindStringSubmatch(entry.Content); len(m) == 2 {
				childID := m[1]
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
}

// ResolvedAgentName holds the final display name and raw role/type metadata for a subagent.
type ResolvedAgentName struct {
	Name string
	Role string
}

// ResolveSubagentNames implements the 3-tier Subagent naming strategy:
//  1. Parent transcript invoke_subagent Role parameter
//  2. conversation_summaries.agent_name (or L2 f1.f20["subagent_type"])
//  3. Duplicate base names under the same parent are deterministically numbered: "<name> #1", "<name> #2".
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
		agentName := strings.TrimSpace(c.SummaryAgentName)
		subType := strings.TrimSpace(c.SubagentType)

		base := roleFromParent
		if base == "" {
			base = agentName
		}
		if base == "" {
			base = subType
		}
		if base == "" {
			base = "subagent"
		}

		role := agentName
		if role == "" {
			role = subType
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
