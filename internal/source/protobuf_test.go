package source

import (
	"bytes"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"google.golang.org/protobuf/encoding/protowire"
)

func buildProtoMsg(fn func(b []byte) []byte) []byte {
	return fn(nil)
}

func appendSubMsg(b []byte, num protowire.Number, payload []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, payload)
}

func appendVarintField(b []byte, num protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func appendStringField(b []byte, num protowire.Number, s string) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendDurationField(b []byte, num protowire.Number, d time.Duration) []byte {
	secs := int64(d / time.Second)
	nanos := int64(d % time.Second)
	var sub []byte
	if secs > 0 {
		sub = appendVarintField(sub, 1, uint64(secs))
	}
	if nanos > 0 {
		sub = appendVarintField(sub, 2, uint64(nanos))
	}
	return appendSubMsg(b, num, sub)
}

func appendKV(b []byte, num protowire.Number, k, v string) []byte {
	var entry []byte
	entry = appendStringField(entry, 1, k)
	entry = appendStringField(entry, 2, v)
	return appendSubMsg(b, num, entry)
}

func TestDecodeGenMetadata_GeminiAndLargeUnknownPayload(t *testing.T) {
	// Mirrors real Gemini 4 Neon gen_metadata layout (including >4KB unknown payload).
	var packedSteps []byte
	for _, s := range []uint64{18, 19, 20, 21} {
		packedSteps = protowire.AppendVarint(packedSteps, s)
	}

	usage := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 2, 9690)  // input_tokens
		b = appendVarintField(b, 3, 1220)  // output_tokens
		b = appendVarintField(b, 5, 33325) // cache_read_tokens
		b = appendVarintField(b, 9, 579)   // thinking_tokens
		b = appendVarintField(b, 10, 641)  // response_tokens
		return b
	})

	ctxWin := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, 44235)
		b = appendVarintField(b, 4, 512000)
		return b
	})
	ctxContainer := appendSubMsg(nil, 10, ctxWin)

	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage)
		b = appendSubMsg(b, 9, ctxContainer)
		b = appendDurationField(b, 11, 4*time.Second+333*time.Millisecond)
		b = appendDurationField(b, 12, 9*time.Second+139*time.Millisecond)
		b = appendStringField(b, 19, "gemini-4-neon")
		b = appendKV(b, 20, "last_step_index", "17")
		b = appendKV(b, 20, "request_id", "traj-1-3")
		b = appendKV(b, 20, "cascade_id", "conv-root")
		b = appendStringField(b, 21, "Gemini 4 Neon (High)")
		// Unknown >4KB blob inside f1 that must be skipped cleanly.
		b = appendSubMsg(b, 99, bytes.Repeat([]byte("X"), 12_000))
		return b
	})

	raw := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 2, packedSteps)
		// Unknown >4KB blob at top-level.
		b = appendSubMsg(b, 8, bytes.Repeat([]byte{0xAB}, 8192))
		b = appendSubMsg(b, 1, inner)
		return b
	})

	gm, err := DecodeGenMetadata(3, raw)
	if err != nil {
		t.Fatalf("DecodeGenMetadata error: %v", err)
	}
	if gm.GenIndex != 3 || gm.StepIndex != 18 || gm.LastStepIndex != 17 {
		t.Fatalf("unexpected indices: gen=%d step=%d last=%d", gm.GenIndex, gm.StepIndex, gm.LastStepIndex)
	}
	if gm.InputTokens != 9690 || gm.CacheReadTokens != 33325 || gm.OutputTokens != 1220 {
		t.Fatalf("unexpected tokens: %+v", gm)
	}
	if gm.ThinkingTokens != 579 || gm.ResponseTokens != 641 {
		t.Fatalf("unexpected think/resp: %d/%d", gm.ThinkingTokens, gm.ResponseTokens)
	}
	if gm.ContextUsed != 44235 || gm.ContextLimit != 512000 {
		t.Fatalf("unexpected context: %d/%d", gm.ContextUsed, gm.ContextLimit)
	}
	if gm.TTFT != 4*time.Second+333*time.Millisecond {
		t.Fatalf("TTFT = %v, want 4.333s", gm.TTFT)
	}
	if gm.DecodeDuration != 9*time.Second+139*time.Millisecond {
		t.Fatalf("DecodeDuration = %v, want 9.139s", gm.DecodeDuration)
	}
	if gm.TotalDuration != gm.TTFT+gm.DecodeDuration {
		t.Fatalf("TotalDuration = %v, want %v", gm.TotalDuration, gm.TTFT+gm.DecodeDuration)
	}
	if gm.ModelSlug != "gemini-4-neon" || gm.ModelDisplay != "Gemini 4 Neon (High)" || gm.RequestID != "traj-1-3" {
		t.Fatalf("unexpected model/meta: %+v", gm)
	}
}

func TestDecodeGenMetadata_ClaudeCacheWriteAndSubagentType(t *testing.T) {
	// Mirrors Claude sample (f4.f4 cache_creation_tokens, no f9 thinking_tokens, f12 < f11).
	usage := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 2, 11749) // input
		b = appendVarintField(b, 3, 315)   // output
		b = appendVarintField(b, 4, 4200)  // cache_creation_tokens
		b = appendVarintField(b, 5, 22602) // cache_read_tokens
		b = appendVarintField(b, 10, 315)  // response_tokens
		return b
	})
	ctxWin := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, 30000)
		b = appendVarintField(b, 4, 840000)
		return b
	})
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, usage)
		b = appendSubMsg(b, 9, appendSubMsg(nil, 10, ctxWin))
		b = appendDurationField(b, 11, 3*time.Second+188*time.Millisecond)
		b = appendDurationField(b, 12, 1*time.Second+992*time.Millisecond)
		b = appendStringField(b, 19, "claude-opus-5-5-high")
		b = appendKV(b, 20, "last_step_index", "15")
		b = appendKV(b, 20, "subagent_type", "browser")
		b = appendStringField(b, 21, "Claude Opus 5.5 (High)")
		return b
	})
	raw := appendSubMsg(nil, 1, inner)

	gm, err := DecodeGenMetadata(7, raw)
	if err != nil {
		t.Fatalf("DecodeGenMetadata error: %v", err)
	}
	if gm.StepIndex != 16 {
		t.Fatalf("StepIndex = %d, want 16 (last_step_index+1)", gm.StepIndex)
	}
	if gm.CacheWriteTokens != 4200 || gm.CacheReadTokens != 22602 || gm.InputTokens != 11749 || gm.OutputTokens != 315 {
		t.Fatalf("unexpected claude tokens: %+v", gm)
	}
	if gm.ThinkingTokens != 0 || gm.ResponseTokens != 315 {
		t.Fatalf("unexpected claude output split: think=%d resp=%d", gm.ThinkingTokens, gm.ResponseTokens)
	}
	if gm.TotalDuration != 5*time.Second+180*time.Millisecond {
		t.Fatalf("TotalDuration = %v, want 5.18s", gm.TotalDuration)
	}
	if gm.SubagentType != "browser" {
		t.Fatalf("SubagentType = %q, want browser", gm.SubagentType)
	}
}

func TestDecodeGenMetadata_EmptyFailureDetected(t *testing.T) {
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendStringField(b, 21, "Orchestrator (Carbon)")
		return b
	})
	raw := buildProtoMsg(func(b []byte) []byte {
		b = appendStringField(b, 5, "request failed: context canceled")
		b = appendSubMsg(b, 1, inner)
		return b
	})

	gm, err := DecodeGenMetadata(9, raw)
	if err != nil {
		t.Fatalf("DecodeGenMetadata error: %v", err)
	}
	if !gm.IsEmptyFailure() {
		t.Fatalf("expected IsEmptyFailure() == true, got %+v", gm)
	}
}

func TestDecodeStepTimestamp(t *testing.T) {
	want := time.Date(2026, 11, 24, 14, 40, 23, 123456700, time.UTC)
	tsMsg := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, uint64(want.Unix()))
		b = appendVarintField(b, 2, uint64(want.Nanosecond()))
		return b
	})
	meta := appendSubMsg(nil, 1, tsMsg)
	got := DecodeStepTimestamp(meta)
	if !got.Equal(want) {
		t.Fatalf("DecodeStepTimestamp = %v, want %v", got, want)
	}
}

func TestDecodeGenMetadata_ContextBreakdown(t *testing.T) {
	cat := func(name string, total uint64, subs ...struct {
		label string
		toks  uint64
	}) []byte {
		return buildProtoMsg(func(b []byte) []byte {
			b = appendStringField(b, 1, name)
			b = appendVarintField(b, 4, total)
			for _, sub := range subs {
				sb := buildProtoMsg(func(sb []byte) []byte {
					sb = appendStringField(sb, 1, sub.label)
					sb = appendVarintField(sb, 3, sub.toks)
					return sb
				})
				b = appendSubMsg(b, 5, sb)
			}
			return b
		})
	}

	breakdown := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 1, cat("System Prompt", 3981))
		b = appendSubMsg(b, 1, cat("Rules", 439))
		b = appendSubMsg(b, 1, cat("Skills", 4826))
		b = appendSubMsg(b, 1, cat("Tools", 10903))
		b = appendSubMsg(b, 1, cat("Mcp Tools", 575))
		b = appendSubMsg(b, 1, cat("Chat Messages", 120964,
			struct {
				label string
				toks  uint64
			}{"477: CHECKPOINT", 11895},
			struct {
				label string
				toks  uint64
			}{"478: PLANNER_RESPONSE", 26302},
			struct {
				label string
				toks  uint64
			}{"479: USER_INPUT", 1333},
			struct {
				label string
				toks  uint64
			}{"482: GENERIC", 77432},
			struct {
				label string
				toks  uint64
			}{"538: SYSTEM_MESSAGE", 4002},
		))
		return b
	})

	ctxWin := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 1, 141688)
		b = appendSubMsg(b, 3, breakdown)
		b = appendVarintField(b, 4, 512000)
		return b
	})
	ctxContainer := appendSubMsg(nil, 10, ctxWin)
	inner := appendSubMsg(nil, 9, ctxContainer)
	raw := appendSubMsg(nil, 1, inner)

	gm, err := DecodeGenMetadata(42, raw)
	if err != nil {
		t.Fatalf("DecodeGenMetadata error: %v", err)
	}
	if gm.ContextUsed != 141688 || gm.ContextLimit != 512000 {
		t.Fatalf("unexpected context window: used=%d limit=%d", gm.ContextUsed, gm.ContextLimit)
	}

	got := map[model.ContextSegmentKey]int64{}
	gotSubs := map[model.ContextSegmentKey]map[model.ContextSubKey]int64{}
	for _, s := range gm.ContextSegments {
		got[s.Key] = s.Tokens
		if len(s.SubItems) > 0 {
			m := make(map[model.ContextSubKey]int64, len(s.SubItems))
			for _, sub := range s.SubItems {
				m[sub.Key] = sub.Tokens
			}
			gotSubs[s.Key] = m
		}
	}
	want := map[model.ContextSegmentKey]int64{
		model.ContextSegSystem:     3981 + 439,
		model.ContextSegTools:      4826 + 10903 + 575,
		model.ContextSegToolOutput: 77432,
		model.ContextSegMessages:   26302 + 1333 + 4002,
		model.ContextSegSummary:    11895,
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("segment %s = %d, want %d (all=%+v)", k, got[k], w, gm.ContextSegments)
		}
	}
	if gotSubs[model.ContextSegSystem][model.ContextSubSystemPrompt] != 3981 ||
		gotSubs[model.ContextSegSystem][model.ContextSubUserRules] != 439 {
		t.Fatalf("unexpected system sub-items: %+v", gotSubs[model.ContextSegSystem])
	}
	if gotSubs[model.ContextSegTools][model.ContextSubSystemTools] != 10903 ||
		gotSubs[model.ContextSegTools][model.ContextSubMCPTools] != 575 ||
		gotSubs[model.ContextSegTools][model.ContextSubSkills] != 4826 {
		t.Fatalf("unexpected tools sub-items: %+v", gotSubs[model.ContextSegTools])
	}
	if gotSubs[model.ContextSegMessages][model.ContextSubAssistant] != 26302 ||
		gotSubs[model.ContextSegMessages][model.ContextSubUserInput] != 1333 ||
		gotSubs[model.ContextSegMessages][model.ContextSubSubagentMsg] != 4002 {
		t.Fatalf("unexpected messages sub-items: %+v", gotSubs[model.ContextSegMessages])
	}
}

func TestDecodeGenMetadata_ModelEnumAndBelongsTo(t *testing.T) {
	usage := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 2, 1000)
		b = appendVarintField(b, 3, 200)
		b = appendVarintField(b, 6, 26) // API_PROVIDER_ANTHROPIC_VERTEX
		return b
	})
	inner := buildProtoMsg(func(b []byte) []byte {
		b = appendVarintField(b, 3, 1535) // MODEL_PLACEHOLDER_M535
		b = appendSubMsg(b, 4, usage)
		b = appendKV(b, 20, "cascade_id", "Conv-Parent-1")
		return b
	})
	raw := appendSubMsg(nil, 1, inner)

	gm, err := DecodeGenMetadata(0, raw)
	if err != nil {
		t.Fatalf("DecodeGenMetadata: %v", err)
	}
	if gm.ModelEnum != "MODEL_PLACEHOLDER_M535" || gm.ModelSlug != "MODEL_PLACEHOLDER_M535" {
		t.Fatalf("unexpected model enum/slug: enum=%q slug=%q", gm.ModelEnum, gm.ModelSlug)
	}
	if gm.APIProvider != "API_PROVIDER_ANTHROPIC_VERTEX" {
		t.Fatalf("APIProvider = %q, want API_PROVIDER_ANTHROPIC_VERTEX", gm.APIProvider)
	}
	if !gm.BelongsTo("conv-parent-1") {
		t.Fatal("expected case-insensitive BelongsTo match")
	}
	if gm.BelongsTo("conv-child-fork") {
		t.Fatal("expected BelongsTo=false for cloned parent step in forked child")
	}
}

func TestDecodeTrajectoryMetadata(t *testing.T) {
	spec := buildProtoMsg(func(b []byte) []byte {
		b = appendStringField(b, 1, "research")
		b = appendStringField(b, 2, "Codebase Auditor")
		return b
	})
	agentScript := appendStringField(nil, 1, "audit-worker")
	raw := buildProtoMsg(func(b []byte) []byte {
		b = appendSubMsg(b, 4, agentScript)
		b = appendStringField(b, 5, "parent-conv-1")
		b = appendStringField(b, 6, "root-conv-1")
		b = appendSubMsg(b, 8, spec)
		b = appendVarintField(b, 17, 2)
		return b
	})

	tm := DecodeTrajectoryMetadata(raw)
	if tm.ParentConversationID != "parent-conv-1" || tm.RootConversationID != "root-conv-1" {
		t.Fatalf("unexpected parent/root: %+v", tm)
	}
	if tm.SubagentTypeName != "research" || tm.SubagentRole != "Codebase Auditor" || tm.AgentScriptName != "audit-worker" {
		t.Fatalf("unexpected spec/script: %+v", tm)
	}
	if tm.NestingDepth != 2 {
		t.Fatalf("NestingDepth = %d, want 2", tm.NestingDepth)
	}
}

func TestParseAnnotationTitle(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "plain utf8",
			raw:  "last_user_view_time { seconds: 1700000000 }\ntitle: \"Refactor Telemetry Engine\"\n",
			want: "Refactor Telemetry Engine",
		},
		{
			name: "octal escaped chinese",
			raw:  "title: \"\\344\\270\\255\\346\\226\\207\\346\\240\\207\\351\\242\\230 \\\"v2\\\"\"\n",
			want: "中文标题 \"v2\"",
		},
		{
			name: "hex and single quote",
			raw:  "title: 'Fix \\x41\\x42 bug'\n",
			want: "Fix AB bug",
		},
		{
			name: "no title field",
			raw:  "subtitle: \"ignored\"\n",
			want: "",
		},
	}
	for _, tc := range cases {
		if got := ParseAnnotationTitle(tc.raw); got != tc.want {
			t.Errorf("%s: ParseAnnotationTitle = %q, want %q", tc.name, got, tc.want)
		}
	}
}
