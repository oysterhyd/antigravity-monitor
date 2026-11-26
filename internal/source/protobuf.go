package source

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"google.golang.org/protobuf/encoding/protowire"
)

const maxUnknownPayloadBytes = 4096

// GenMetadata holds the decoded telemetry fields extracted from a single row of
// conversations/<conv_id>.db -> gen_metadata.data via lightweight streaming protowire.
type GenMetadata struct {
	GenIndex           int
	StepIndices        []int
	StepIndex          int
	StepIndexEstimated bool
	LastStepIndex      int
	RequestID       string
	CascadeID       string
	ParentCascadeID string
	RootCascadeID   string
	SubagentType    string
	Error           string
	ModelSlug       string
	ModelEnum       string
	ModelDisplay    string
	APIProvider     string

	InputTokens      int64
	OutputTokens     int64
	CacheWriteTokens int64
	CacheReadTokens  int64
	ThinkingTokens   int64
	ResponseTokens   int64

	ContextUsed     int64
	ContextLimit    int64
	ContextSegments []model.ContextSegment

	// Empirically verified against steps.metadata timestamps across local Antigravity DBs:
	//   f1.f11 = TTFT (t_first_token - t_start)
	//   f1.f12 = DecodeDuration (t_end - t_first_token)
	//   TotalDuration = TTFT + DecodeDuration
	TTFT           time.Duration
	DecodeDuration time.Duration
	TotalDuration  time.Duration
}

// IsEmptyFailure reports whether this generation row represents a canceled/failed request
// that produced zero token telemetry.
func (g GenMetadata) IsEmptyFailure() bool {
	return g.Error != "" &&
		g.InputTokens == 0 &&
		g.OutputTokens == 0 &&
		g.CacheReadTokens == 0 &&
		g.CacheWriteTokens == 0
}

// BelongsTo reports whether this generation belongs to convID rather than having been
// cloned from a parent conversation during a fork or side-question branch.
func (g GenMetadata) BelongsTo(convID string) bool {
	origin := strings.TrimSpace(g.CascadeID)
	target := strings.TrimSpace(convID)
	if origin == "" || target == "" {
		return true
	}
	return strings.EqualFold(origin, target)
}

// KnownStepIndices returns all explicit step indices associated with this generation
// (from StepIndices or LastStepIndex+1), or nil when StepIndex was only estimated from GenIndex.
func (g GenMetadata) KnownStepIndices() []int {
	if g.StepIndexEstimated {
		return nil
	}
	if len(g.StepIndices) > 0 {
		return g.StepIndices
	}
	if g.StepIndex >= 0 {
		return []int{g.StepIndex}
	}
	return nil
}

// TrajectoryMetadata holds decoded fields from conversations/<id>.db -> trajectory_metadata_blob.
type TrajectoryMetadata struct {
	ParentConversationID string
	RootConversationID   string
	NestingDepth         int
	SubagentTypeName     string
	SubagentRole         string
	AgentScriptName      string
}

// DecodeGenMetadata decodes a raw gen_metadata.data protobuf blob using protowire,
// skipping unknown fields and any unknown length-delimited payload > 4KB without copying.
func DecodeGenMetadata(genIndex int, raw []byte) (GenMetadata, error) {
	m := GenMetadata{
		GenIndex:      genIndex,
		LastStepIndex: -1,
	}
	b := raw
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return m, fmt.Errorf("gen_metadata[%d] tag: %w", genIndex, protowire.ParseError(n))
		}
		b = b[n:]

		switch num {
		case 1: // inner telemetry message
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return m, fmt.Errorf("gen_metadata[%d] f1: %w", genIndex, protowire.ParseError(vn))
				}
				b = b[vn:]
				if err := decodeInnerGenMeta(val, &m); err != nil {
					return m, fmt.Errorf("gen_metadata[%d] f1 inner: %w", genIndex, err)
				}
				continue
			}
		case 2: // step indices produced by this generation (packed varints or single varint)
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return m, fmt.Errorf("gen_metadata[%d] f2: %w", genIndex, protowire.ParseError(vn))
				}
				b = b[vn:]
				if len(val) <= maxUnknownPayloadBytes {
					m.StepIndices = decodePackedVarints(val)
				}
				continue
			}
			if typ == protowire.VarintType {
				v, vn := protowire.ConsumeVarint(b)
				if vn < 0 {
					return m, fmt.Errorf("gen_metadata[%d] f2 varint: %w", genIndex, protowire.ParseError(vn))
				}
				b = b[vn:]
				m.StepIndices = append(m.StepIndices, int(v))
				continue
			}
		case 5: // top-level error string on failed/canceled generations
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return m, fmt.Errorf("gen_metadata[%d] f5: %w", genIndex, protowire.ParseError(vn))
				}
				b = b[vn:]
				if len(val) <= maxUnknownPayloadBytes {
					m.Error = string(val)
				}
				continue
			}
		}

		sn, err := skipField(num, typ, b)
		if err != nil {
			return m, fmt.Errorf("gen_metadata[%d] skip f%d: %w", genIndex, num, err)
		}
		b = b[sn:]
	}

	// Reconcile output / thinking / response token breakdown.
	if m.OutputTokens == 0 && (m.ThinkingTokens > 0 || m.ResponseTokens > 0) {
		m.OutputTokens = m.ThinkingTokens + m.ResponseTokens
	}
	if m.ResponseTokens == 0 && m.OutputTokens > 0 && m.ThinkingTokens <= m.OutputTokens {
		m.ResponseTokens = m.OutputTokens - m.ThinkingTokens
	}

	// Fallback to model enum / placeholder when canonical response model slug is absent.
	if m.ModelSlug == "" && m.ModelEnum != "" {
		m.ModelSlug = m.ModelEnum
	}

	m.TotalDuration = m.TTFT + m.DecodeDuration

	switch {
	case len(m.StepIndices) > 0:
		m.StepIndex = m.StepIndices[0]
	case m.LastStepIndex >= 0:
		m.StepIndex = m.LastStepIndex + 1
	default:
		m.StepIndex = genIndex
		m.StepIndexEstimated = true
	}

	return m, nil
}

func decodeInnerGenMeta(b []byte, m *GenMetadata) error {
	var hasPrimaryUsage bool
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]

		switch num {
		case 3: // model enum varint (e.g. 1535 -> MODEL_PLACEHOLDER_M535)
			if typ == protowire.VarintType {
				v, vn := protowire.ConsumeVarint(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				b = b[vn:]
				if enumStr := decodeProtoModelEnum(v); enumStr != "" && m.ModelEnum == "" {
					m.ModelEnum = enumStr
				}
				continue
			}
		case 4: // token usage
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				b = b[vn:]
				if err := decodeTokenUsage(val, m); err != nil {
					return err
				}
				hasPrimaryUsage = m.InputTokens > 0 || m.OutputTokens > 0 || m.CacheReadTokens > 0 || m.CacheWriteTokens > 0
				continue
			}
		case 9: // context window container (f9.f10.{f1, f4})
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				b = b[vn:]
				if err := decodeContextContainer(val, m); err != nil {
					return err
				}
				continue
			}
		case 11: // TTFT google.protobuf.Duration
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				b = b[vn:]
				if d, ok := decodeDuration(val); ok {
					m.TTFT = d
				}
				continue
			}
		case 12: // Decode / streaming duration google.protobuf.Duration
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				b = b[vn:]
				if d, ok := decodeDuration(val); ok {
					m.DecodeDuration = d
				}
				continue
			}
		case 17: // secondary/mirror usage & error wrapper
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				b = b[vn:]
				if err := decodeMirrorBlock(val, m, hasPrimaryUsage); err != nil {
					return err
				}
				continue
			}
		case 19: // canonical model slug
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				b = b[vn:]
				if len(val) <= maxUnknownPayloadBytes {
					m.ModelSlug = string(val)
				}
				continue
			}
		case 20: // metadata map<string, string> entry {f1: key, f2: value}
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				b = b[vn:]
				if len(val) <= maxUnknownPayloadBytes {
					decodeMetaMapEntry(val, m)
				}
				continue
			}
		case 21: // display model name
			if typ == protowire.BytesType {
				val, vn := protowire.ConsumeBytes(b)
				if vn < 0 {
					return protowire.ParseError(vn)
				}
				b = b[vn:]
				if len(val) <= maxUnknownPayloadBytes {
					m.ModelDisplay = string(val)
				}
				continue
			}
		}

		sn, err := skipField(num, typ, b)
		if err != nil {
			return err
		}
		b = b[sn:]
	}
	return nil
}

func decodeTokenUsage(b []byte, m *GenMetadata) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]

		if typ == protowire.VarintType {
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return protowire.ParseError(vn)
			}
			b = b[vn:]
			switch num {
			case 1:
				if enumStr := decodeProtoModelEnum(v); enumStr != "" && m.ModelEnum == "" {
					m.ModelEnum = enumStr
				}
			case 2:
				m.InputTokens = int64(v)
			case 3:
				m.OutputTokens = int64(v)
			case 4:
				m.CacheWriteTokens = int64(v)
			case 5:
				m.CacheReadTokens = int64(v)
			case 6:
				if prov := decodeProtoAPIProvider(v); prov != "" && m.APIProvider == "" {
					m.APIProvider = prov
				}
			case 9:
				m.ThinkingTokens = int64(v)
			case 10:
				m.ResponseTokens = int64(v)
			}
			continue
		}
		if num == 1 && typ == protowire.BytesType {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return protowire.ParseError(vn)
			}
			b = b[vn:]
			if len(val) <= maxUnknownPayloadBytes {
				if s := strings.TrimSpace(string(val)); s != "" && m.ModelEnum == "" {
					m.ModelEnum = s
				}
			}
			continue
		}

		sn, err := skipField(num, typ, b)
		if err != nil {
			return err
		}
		b = b[sn:]
	}
	return nil
}

func decodeContextContainer(b []byte, m *GenMetadata) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]

		if num == 10 && typ == protowire.BytesType {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return protowire.ParseError(vn)
			}
			b = b[vn:]
			if err := decodeContextWindow(val, m); err != nil {
				return err
			}
			continue
		}

		sn, err := skipField(num, typ, b)
		if err != nil {
			return err
		}
		b = b[sn:]
	}
	return nil
}

func decodeContextWindow(b []byte, m *GenMetadata) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]

		if typ == protowire.VarintType {
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return protowire.ParseError(vn)
			}
			b = b[vn:]
			switch num {
			case 1:
				m.ContextUsed = int64(v)
			case 4:
				m.ContextLimit = int64(v)
			}
			continue
		}
		if num == 3 && typ == protowire.BytesType {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return protowire.ParseError(vn)
			}
			b = b[vn:]
			if segs := decodeContextBreakdown(val); len(segs) > 0 {
				m.ContextSegments = segs
			}
			continue
		}

		sn, err := skipField(num, typ, b)
		if err != nil {
			return err
		}
		b = b[sn:]
	}
	return nil
}

type contextBreakdownCounts struct {
	sysPrompt   int64
	userRules   int64
	sysOther    int64
	sysTools    int64
	mcpTools    int64
	skills      int64
	toolOut     int64
	userInput   int64
	assistant   int64
	subagentMsg int64
	summary     int64
}

// decodeContextBreakdown decodes Antigravity's native context breakdown (f1.f9.f10.f3)
// into 5 clean top-level semantic buckets (system, tools, tool_output, messages, summary)
// while preserving fine-grained SubItems (Prompt/Rules/Plugins, Built-in/MCP/Skills, User/Model/Subagent).
func decodeContextBreakdown(b []byte) []model.ContextSegment {
	var acc contextBreakdownCounts

	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil
		}
		b = b[n:]

		if num == 1 && typ == protowire.BytesType {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return nil
			}
			b = b[vn:]
			decodeContextCategory(val, &acc)
			continue
		}

		sn, err := skipField(num, typ, b)
		if err != nil {
			return nil
		}
		b = b[sn:]
	}

	out := make([]model.ContextSegment, 0, 5)
	if tot := acc.sysPrompt + acc.userRules + acc.sysOther; tot > 0 {
		seg := model.ContextSegment{Key: model.ContextSegSystem, Tokens: tot}
		if acc.sysPrompt > 0 {
			seg.SubItems = append(seg.SubItems, model.ContextSubSegment{Key: model.ContextSubSystemPrompt, Tokens: acc.sysPrompt})
		}
		if acc.userRules > 0 {
			seg.SubItems = append(seg.SubItems, model.ContextSubSegment{Key: model.ContextSubUserRules, Tokens: acc.userRules})
		}
		if acc.sysOther > 0 {
			seg.SubItems = append(seg.SubItems, model.ContextSubSegment{Key: model.ContextSubSubagentDefs, Tokens: acc.sysOther})
		}
		out = append(out, seg)
	}
	if tot := acc.sysTools + acc.mcpTools + acc.skills; tot > 0 {
		seg := model.ContextSegment{Key: model.ContextSegTools, Tokens: tot}
		if acc.sysTools > 0 {
			seg.SubItems = append(seg.SubItems, model.ContextSubSegment{Key: model.ContextSubSystemTools, Tokens: acc.sysTools})
		}
		if acc.mcpTools > 0 {
			seg.SubItems = append(seg.SubItems, model.ContextSubSegment{Key: model.ContextSubMCPTools, Tokens: acc.mcpTools})
		}
		if acc.skills > 0 {
			seg.SubItems = append(seg.SubItems, model.ContextSubSegment{Key: model.ContextSubSkills, Tokens: acc.skills})
		}
		out = append(out, seg)
	}
	if acc.toolOut > 0 {
		out = append(out, model.ContextSegment{Key: model.ContextSegToolOutput, Tokens: acc.toolOut})
	}
	if tot := acc.userInput + acc.assistant + acc.subagentMsg; tot > 0 {
		seg := model.ContextSegment{Key: model.ContextSegMessages, Tokens: tot}
		if acc.assistant > 0 {
			seg.SubItems = append(seg.SubItems, model.ContextSubSegment{Key: model.ContextSubAssistant, Tokens: acc.assistant})
		}
		if acc.userInput > 0 {
			seg.SubItems = append(seg.SubItems, model.ContextSubSegment{Key: model.ContextSubUserInput, Tokens: acc.userInput})
		}
		if acc.subagentMsg > 0 {
			seg.SubItems = append(seg.SubItems, model.ContextSubSegment{Key: model.ContextSubSubagentMsg, Tokens: acc.subagentMsg})
		}
		out = append(out, seg)
	}
	if acc.summary > 0 {
		out = append(out, model.ContextSegment{Key: model.ContextSegSummary, Tokens: acc.summary})
	}
	return out
}

type rawContextSubItem struct {
	name   string
	tokens int64
}

func decodeContextCategory(b []byte, acc *contextBreakdownCounts) {
	var (
		catName   string
		catType   uint64
		catTokens int64
		subItems  []rawContextSubItem
		subTotal  int64
	)

	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]

		switch {
		case num == 1 && typ == protowire.BytesType:
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return
			}
			b = b[vn:]
			catName = string(val)
			continue
		case num == 2 && typ == protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return
			}
			b = b[vn:]
			catType = v
			continue
		case num == 4 && typ == protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return
			}
			b = b[vn:]
			catTokens = int64(v)
			continue
		case num == 5 && typ == protowire.BytesType:
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return
			}
			b = b[vn:]
			itemName, itemToks := decodeContextSubItem(val)
			if itemToks > 0 {
				subTotal += itemToks
				subItems = append(subItems, rawContextSubItem{name: itemName, tokens: itemToks})
			}
			continue
		}

		sn, err := skipField(num, typ, b)
		if err != nil {
			return
		}
		b = b[sn:]
	}

	if catTokens <= 0 && subTotal <= 0 {
		return
	}

	normName := strings.ToLower(strings.TrimSpace(catName))
	// Protobuf TokenType enum: 1=SYSTEM_PROMPT, 2=TOOLS, 3=RULES, 4=MCP_TOOLS,
	// 5=SKILLS, 6=SUBAGENTS, 7=CHAT_MESSAGES, 8=WORKFLOWS, 9=PLUGINS, 10=FLOWS.
	switch {
	case catType == 7 || normName == "chat messages" || normName == "messages":
		if subTotal > 0 {
			for _, it := range subItems {
				kind := it.name
				if idx := strings.Index(kind, ": "); idx >= 0 {
					kind = kind[idx+2:]
				}
				upper := strings.ToUpper(strings.TrimSpace(kind))
				switch {
				case strings.Contains(upper, "CHECKPOINT") || strings.Contains(upper, "SUMMARY"):
					acc.summary += it.tokens
				case strings.Contains(upper, "PLANNER_RESPONSE") || strings.Contains(upper, "ASSISTANT") || strings.Contains(upper, "MODEL"):
					acc.assistant += it.tokens
				case strings.Contains(upper, "SYSTEM_MESSAGE") || strings.Contains(upper, "SUBAGENT"):
					acc.subagentMsg += it.tokens
				case strings.Contains(upper, "USER_INPUT") || upper == "USER":
					acc.userInput += it.tokens
				default:
					// GENERIC, VIEW_FILE, RUN_COMMAND, CODE_ACTION, etc.
					acc.toolOut += it.tokens
				}
			}
			if catTokens > subTotal {
				acc.userInput += catTokens - subTotal
			}
		} else {
			acc.userInput += catTokens
		}

	case catType == 5 || normName == "skills" || normName == "skill":
		acc.skills += catTokens

	case catType == 4 || normName == "mcp tools" || normName == "mcp_tools" || normName == "mcp servers" || strings.Contains(normName, "mcp"):
		acc.mcpTools += catTokens

	case catType == 2 || normName == "tools":
		if subTotal > 0 {
			for _, it := range subItems {
				if isMCPToolName(it.name) {
					acc.mcpTools += it.tokens
				} else {
					acc.sysTools += it.tokens
				}
			}
			if catTokens > subTotal {
				acc.sysTools += catTokens - subTotal
			}
		} else {
			acc.sysTools += catTokens
		}

	case catType == 3 || normName == "rules" || normName == "user rules" || normName == "user_rules" || normName == "rule":
		acc.userRules += catTokens

	case catType == 6 || catType == 8 || catType == 9 || catType == 10 ||
		normName == "subagents" || normName == "plugins" || normName == "featured plugins" ||
		normName == "workflows" || normName == "flows":
		acc.sysOther += catTokens

	case catType == 1 || normName == "system prompt":
		if subTotal > 0 {
			for _, it := range subItems {
				sn := strings.ToLower(strings.TrimSpace(it.name))
				switch {
				case sn == "skills" || sn == "skill":
					acc.skills += it.tokens
				case sn == "rules" || sn == "user rules" || sn == "user_rules" || sn == "rule":
					acc.userRules += it.tokens
				case strings.Contains(sn, "mcp"):
					acc.mcpTools += it.tokens
				default:
					acc.sysPrompt += it.tokens
				}
			}
			if catTokens > subTotal {
				acc.sysPrompt += catTokens - subTotal
			}
		} else {
			acc.sysPrompt += catTokens
		}

	default:
		acc.sysPrompt += catTokens
	}
}

func isMCPToolName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(n, "mcp_") ||
		strings.HasPrefix(n, "mcp-") ||
		strings.Contains(n, "mcp") ||
		n == "call_mcp_tool" ||
		n == "list_mcp_resources" ||
		n == "read_mcp_resource"
}

func decodeContextSubItem(b []byte) (string, int64) {
	var name string
	var toks int64
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return name, toks
		}
		b = b[n:]
		if num == 1 && typ == protowire.BytesType {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return name, toks
			}
			b = b[vn:]
			name = string(val)
			continue
		}
		if num == 3 && typ == protowire.VarintType {
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return name, toks
			}
			b = b[vn:]
			toks = int64(v)
			continue
		}
		sn, err := skipField(num, typ, b)
		if err != nil {
			return name, toks
		}
		b = b[sn:]
	}
	return name, toks
}

func decodeMirrorBlock(b []byte, m *GenMetadata, hasPrimaryUsage bool) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]

		if num == 2 && typ == protowire.BytesType && !hasPrimaryUsage {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return protowire.ParseError(vn)
			}
			b = b[vn:]
			if err := decodeTokenUsage(val, m); err != nil {
				return err
			}
			continue
		}
		if num == 3 && typ == protowire.BytesType && m.Error == "" {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return protowire.ParseError(vn)
			}
			b = b[vn:]
			if len(val) <= maxUnknownPayloadBytes {
				m.Error = string(val)
			}
			continue
		}

		sn, err := skipField(num, typ, b)
		if err != nil {
			return err
		}
		b = b[sn:]
	}
	return nil
}

func decodeMetaMapEntry(b []byte, m *GenMetadata) {
	var key, val string
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]
		if typ == protowire.BytesType {
			raw, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return
			}
			b = b[vn:]
			if num == 1 {
				key = string(raw)
			} else if num == 2 {
				val = string(raw)
			}
			continue
		}
		sn, err := skipField(num, typ, b)
		if err != nil {
			return
		}
		b = b[sn:]
	}

	switch key {
	case "last_step_index":
		if v, err := strconv.Atoi(val); err == nil {
			m.LastStepIndex = v
		}
	case "request_id":
		m.RequestID = val
	case "cascade_id":
		m.CascadeID = val
	case "parent_cascade_id":
		m.ParentCascadeID = val
	case "root_cascade_id":
		m.RootCascadeID = val
	case "subagent_type":
		m.SubagentType = val
	case "model_enum":
		clean := strings.TrimSpace(val)
		upper := strings.ToUpper(clean)
		if clean != "" && clean != "0" && upper != "MODEL_UNSPECIFIED" && upper != "MODEL_UNKNOWN" {
			m.ModelEnum = clean
		}
	case "used_claude":
		if strings.EqualFold(strings.TrimSpace(val), "true") && m.APIProvider == "" {
			m.APIProvider = "API_PROVIDER_ANTHROPIC"
		}
	}
}

func decodeProtoModelEnum(v uint64) string {
	if v == 0 {
		return ""
	}
	if v >= 1000 && v < 100000 {
		return fmt.Sprintf("MODEL_PLACEHOLDER_M%d", v-1000)
	}
	switch v {
	case 246:
		return "MODEL_GOOGLE_GEMINI_2_5_PRO"
	case 281:
		return "MODEL_CLAUDE_4_SONNET"
	case 282:
		return "MODEL_CLAUDE_4_SONNET_THINKING"
	case 290:
		return "MODEL_CLAUDE_4_OPUS"
	case 291:
		return "MODEL_CLAUDE_4_OPUS_THINKING"
	case 312:
		return "MODEL_GOOGLE_GEMINI_2_5_FLASH"
	case 313:
		return "MODEL_GOOGLE_GEMINI_2_5_FLASH_THINKING"
	case 330:
		return "MODEL_GOOGLE_GEMINI_2_5_FLASH_LITE"
	case 333:
		return "MODEL_CLAUDE_4_5_SONNET"
	case 334:
		return "MODEL_CLAUDE_4_5_SONNET_THINKING"
	case 340:
		return "MODEL_CLAUDE_4_5_HAIKU"
	case 341:
		return "MODEL_CLAUDE_4_5_HAIKU_THINKING"
	case 342:
		return "MODEL_OPENAI_GPT_OSS_120B_MEDIUM"
	default:
		return ""
	}
}

func decodeProtoAPIProvider(v uint64) string {
	switch v {
	case 1:
		return "API_PROVIDER_INTERNAL"
	case 3:
		return "API_PROVIDER_GOOGLE_VERTEX"
	case 24:
		return "API_PROVIDER_GOOGLE_GEMINI"
	case 26:
		return "API_PROVIDER_ANTHROPIC_VERTEX"
	case 30:
		return "API_PROVIDER_GOOGLE_EVERGREEN"
	case 31:
		return "API_PROVIDER_OPENAI_VERTEX"
	case 32:
		return "API_PROVIDER_OPENAI_RESPONSES_VERTEX"
	default:
		return ""
	}
}

// DecodeTrajectoryMetadata decodes conversations/<id>.db -> trajectory_metadata_blob.data
// to extract parent/root conversation IDs and subagent spec (typeName, role) without L3 transcript.
func DecodeTrajectoryMetadata(raw []byte) TrajectoryMetadata {
	var tm TrajectoryMetadata
	b := raw
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return tm
		}
		b = b[n:]
		switch {
		case num == 4 && typ == protowire.BytesType: // agentScript { f1: name }
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return tm
			}
			b = b[vn:]
			tm.AgentScriptName = decodeFirstStringField(val, 1)
			continue
		case num == 5 && typ == protowire.BytesType: // parentConversationId
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return tm
			}
			b = b[vn:]
			if len(val) <= maxUnknownPayloadBytes {
				tm.ParentConversationID = strings.TrimSpace(string(val))
			}
			continue
		case num == 6 && typ == protowire.BytesType: // rootConversationId
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return tm
			}
			b = b[vn:]
			if len(val) <= maxUnknownPayloadBytes {
				tm.RootConversationID = strings.TrimSpace(string(val))
			}
			continue
		case num == 8 && typ == protowire.BytesType: // subagentSpec { f1: typeName, f2: role }
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return tm
			}
			b = b[vn:]
			tm.SubagentTypeName, tm.SubagentRole = decodeSubagentSpec(val)
			continue
		case num == 17 && typ == protowire.VarintType: // nestingDepth
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return tm
			}
			b = b[vn:]
			tm.NestingDepth = int(int32(v))
			continue
		}
		sn, err := skipField(num, typ, b)
		if err != nil {
			return tm
		}
		b = b[sn:]
	}
	return tm
}

func decodeSubagentSpec(b []byte) (typeName, role string) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return typeName, role
		}
		b = b[n:]
		if typ == protowire.BytesType && (num == 1 || num == 2) {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return typeName, role
			}
			b = b[vn:]
			if len(val) <= maxUnknownPayloadBytes {
				if num == 1 {
					typeName = strings.TrimSpace(string(val))
				} else {
					role = strings.TrimSpace(string(val))
				}
			}
			continue
		}
		sn, err := skipField(num, typ, b)
		if err != nil {
			return typeName, role
		}
		b = b[sn:]
	}
	return typeName, role
}

func decodeFirstStringField(b []byte, wantNum protowire.Number) string {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return ""
		}
		b = b[n:]
		if num == wantNum && typ == protowire.BytesType {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return ""
			}
			if len(val) <= maxUnknownPayloadBytes {
				return strings.TrimSpace(string(val))
			}
			return ""
		}
		sn, err := skipField(num, typ, b)
		if err != nil {
			return ""
		}
		b = b[sn:]
	}
	return ""
}

func decodeDuration(b []byte) (time.Duration, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var secs, nanos int64
	var seen bool
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return 0, false
		}
		b = b[n:]
		if typ == protowire.VarintType {
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return 0, false
			}
			b = b[vn:]
			switch num {
			case 1:
				secs = int64(v)
				seen = true
			case 2:
				nanos = int64(v)
				seen = true
			}
			continue
		}
		sn, err := skipField(num, typ, b)
		if err != nil {
			return 0, false
		}
		b = b[sn:]
	}
	if !seen {
		return 0, false
	}
	d := time.Duration(secs)*time.Second + time.Duration(nanos)*time.Nanosecond
	if d < 0 {
		return 0, false
	}
	return d, true
}

// DecodeStepTimestamp extracts the created_at protobuf Timestamp (field 1) from a
// conversations/<id>.db -> steps.metadata blob. Returns zero time if absent.
func DecodeStepTimestamp(meta []byte) time.Time {
	b := meta
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return time.Time{}
		}
		b = b[n:]
		if num == 1 && typ == protowire.BytesType {
			val, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return time.Time{}
			}
			b = b[vn:]
			return decodeTimestamp(val)
		}
		sn, err := skipField(num, typ, b)
		if err != nil {
			return time.Time{}
		}
		b = b[sn:]
	}
	return time.Time{}
}

func decodeTimestamp(b []byte) time.Time {
	var secs, nanos int64
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return time.Time{}
		}
		b = b[n:]
		if typ == protowire.VarintType {
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return time.Time{}
			}
			b = b[vn:]
			if num == 1 {
				secs = int64(v)
			} else if num == 2 {
				nanos = int64(v)
			}
			continue
		}
		sn, err := skipField(num, typ, b)
		if err != nil {
			return time.Time{}
		}
		b = b[sn:]
	}
	if secs <= 1_500_000_000 {
		return time.Time{}
	}
	return time.Unix(secs, nanos).UTC()
}

func decodePackedVarints(b []byte) []int {
	var out []int
	for len(b) > 0 {
		v, n := protowire.ConsumeVarint(b)
		if n < 0 {
			return nil
		}
		b = b[n:]
		out = append(out, int(v))
	}
	return out
}

func skipField(num protowire.Number, typ protowire.Type, b []byte) (int, error) {
	if typ == protowire.BytesType {
		ln, vn := protowire.ConsumeVarint(b)
		if vn < 0 {
			return 0, protowire.ParseError(vn)
		}
		if ln > uint64(len(b)-vn) {
			return 0, io.ErrUnexpectedEOF
		}
		return vn + int(ln), nil
	}
	n := protowire.ConsumeFieldValue(num, typ, b)
	if n < 0 {
		return 0, protowire.ParseError(n)
	}
	return n, nil
}
