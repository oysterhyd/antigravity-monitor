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
	GenIndex        int
	StepIndices     []int
	StepIndex       int
	LastStepIndex   int
	RequestID       string
	CascadeID       string
	ParentCascadeID string
	RootCascadeID   string
	SubagentType    string
	Error           string
	ModelSlug       string
	ModelDisplay    string

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

	m.TotalDuration = m.TTFT + m.DecodeDuration

	switch {
	case len(m.StepIndices) > 0:
		m.StepIndex = m.StepIndices[0]
	case m.LastStepIndex >= 0:
		m.StepIndex = m.LastStepIndex + 1
	default:
		m.StepIndex = genIndex
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
			case 2:
				m.InputTokens = int64(v)
			case 3:
				m.OutputTokens = int64(v)
			case 4:
				m.CacheWriteTokens = int64(v)
			case 5:
				m.CacheReadTokens = int64(v)
			case 9:
				m.ThinkingTokens = int64(v)
			case 10:
				m.ResponseTokens = int64(v)
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

// decodeContextBreakdown decodes Antigravity's native context breakdown (f1.f9.f10.f3)
// into 5 clean semantic buckets: system, tools, tool_output, messages, and summary.
func decodeContextBreakdown(b []byte) []model.ContextSegment {
	var systemToks, toolsToks, toolOutToks, msgToks, summaryToks int64

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
			decodeContextCategory(val, &systemToks, &toolsToks, &toolOutToks, &msgToks, &summaryToks)
			continue
		}

		sn, err := skipField(num, typ, b)
		if err != nil {
			return nil
		}
		b = b[sn:]
	}

	out := make([]model.ContextSegment, 0, 5)
	if systemToks > 0 {
		out = append(out, model.ContextSegment{Key: model.ContextSegSystem, Tokens: systemToks})
	}
	if toolsToks > 0 {
		out = append(out, model.ContextSegment{Key: model.ContextSegTools, Tokens: toolsToks})
	}
	if toolOutToks > 0 {
		out = append(out, model.ContextSegment{Key: model.ContextSegToolOutput, Tokens: toolOutToks})
	}
	if msgToks > 0 {
		out = append(out, model.ContextSegment{Key: model.ContextSegMessages, Tokens: msgToks})
	}
	if summaryToks > 0 {
		out = append(out, model.ContextSegment{Key: model.ContextSegSummary, Tokens: summaryToks})
	}
	return out
}

func decodeContextCategory(b []byte, systemToks, toolsToks, toolOutToks, msgToks, summaryToks *int64) {
	var (
		catName    string
		catTokens  int64
		subToolOut int64
		subMsgs    int64
		subSummary int64
		subTotal   int64
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
				kind := itemName
				if idx := strings.Index(kind, ": "); idx >= 0 {
					kind = kind[idx+2:]
				}
				switch strings.ToUpper(strings.TrimSpace(kind)) {
				case "CHECKPOINT":
					subSummary += itemToks
				case "USER_INPUT", "PLANNER_RESPONSE":
					subMsgs += itemToks
				default:
					// GENERIC (tool outputs / file reads), SYSTEM_MESSAGE, etc.
					subToolOut += itemToks
				}
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

	switch strings.ToLower(strings.TrimSpace(catName)) {
	case "chat messages":
		if subTotal > 0 {
			*toolOutToks += subToolOut
			*msgToks += subMsgs
			*summaryToks += subSummary
			if catTokens > subTotal {
				*msgToks += catTokens - subTotal
			}
		} else {
			*msgToks += catTokens
		}
	case "tools", "mcp tools", "skills":
		*toolsToks += catTokens
	default:
		// "System Prompt", "Rules", "Subagents", "Plugins", etc.
		*systemToks += catTokens
	}
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
	}
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
