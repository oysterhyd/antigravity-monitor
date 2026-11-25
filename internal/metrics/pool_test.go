package metrics

import "testing"

func TestClassifyPoolAndSubModelRealPairs(t *testing.T) {
	cases := []struct {
		name         string
		slug         string
		display      string
		wantPool     ModelPool
		wantSubModel string
		wantQuotaSub string
	}{
		// All 11 real DB pairs observed in survey_3
		{
			name:         "hydrogen xhigh",
			slug:         "gemini-4-hydrogen",
			display:      "Gemini 4 Hydrogen (XHigh)",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Hydrogen",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "hydrogen tiered high",
			slug:         "gemini-4-hydrogen-tiered-high",
			display:      "Gemini 4 Hydrogen (High)",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Hydrogen",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "neon high",
			slug:         "gemini-4-neon",
			display:      "Gemini 4 Neon (High)",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Neon (High)",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "neon xhigh",
			slug:         "gemini-4-neon",
			display:      "Gemini 4 Neon (XHigh)",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Neon (High)",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "carbon high",
			slug:         "gemini-4-carbon",
			display:      "Gemini 4 Carbon (High)",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Carbon",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "argon trial",
			slug:         "gemini-4-argon-trial",
			display:      "Gemini 4 Argon (High)",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Argon",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "flash worker with hydrogen orchestrator display",
			slug:         "gemini-flash-worker-agy",
			display:      "Gemini 4 Hydrogen (Orchestrator)",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Flash",
			wantQuotaSub: "Gemini 4 Flash",
		},
		{
			name:         "pro orchestrator carbon",
			slug:         "gemini-pro-orchestrator",
			display:      "Orchestrator (Carbon)",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini Pro Orchestrator",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "pro orchestrator neon",
			slug:         "gemini-pro-orchestrator-neon",
			display:      "Orchestrator (Neon)",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini Pro Orchestrator",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "claude opus 5.5 high",
			slug:         "claude-opus-5-5-high",
			display:      "Claude Opus 5.5 (High)",
			wantPool:     PoolClaudeGPT,
			wantSubModel: "Claude Opus 5.5",
			wantQuotaSub: "Claude 4.5 Sonnet / Opus",
		},
		{
			name:         "empty slug orchestrator carbon display",
			slug:         "",
			display:      "Orchestrator (Carbon)",
			wantPool:     PoolGemini,
			wantSubModel: "Orchestrator (Carbon)",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		// pricing.default.json patterns
		{
			name:         "gemini 4 flash",
			slug:         "gemini-4-flash",
			display:      "Gemini 4 Flash",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Flash",
			wantQuotaSub: "Gemini 4 Flash",
		},
		{
			name:         "gemini 2.5 flash",
			slug:         "gemini-2.5-flash",
			display:      "Gemini 2.5 Flash",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Flash",
			wantQuotaSub: "Gemini 4 Flash",
		},
		{
			name:         "gemini 2.5 pro",
			slug:         "gemini-2.5-pro",
			display:      "Gemini 2.5 Pro",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 2.5 Pro",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "claude opus 4",
			slug:         "claude-opus-4-5",
			display:      "Claude Opus 4.5 (High)",
			wantPool:     PoolClaudeGPT,
			wantSubModel: "Claude 4.5 Sonnet / Opus",
			wantQuotaSub: "Claude 4.5 Sonnet / Opus",
		},
		{
			name:         "claude sonnet 4",
			slug:         "claude-sonnet-4-5",
			display:      "Claude 4.5 Sonnet",
			wantPool:     PoolClaudeGPT,
			wantSubModel: "Claude 4.5 Sonnet / Opus",
			wantQuotaSub: "Claude 4.5 Sonnet / Opus",
		},
		{
			name:         "claude haiku 4",
			slug:         "claude-haiku-4-5",
			display:      "Claude Haiku 4",
			wantPool:     PoolClaudeGPT,
			wantSubModel: "Claude Haiku 4",
			wantQuotaSub: "Claude 4.5 Sonnet / Opus",
		},
		{
			name:         "gpt 5",
			slug:         "gpt-5.2-codex",
			display:      "GPT-5.2 Codex",
			wantPool:     PoolClaudeGPT,
			wantSubModel: "GPT-5.2 / Codex",
			wantQuotaSub: "GPT-5.2 / Codex",
		},
		{
			name:         "codex standalone",
			slug:         "codex-mini",
			display:      "",
			wantPool:     PoolClaudeGPT,
			wantSubModel: "GPT-5.2 / Codex",
			wantQuotaSub: "GPT-5.2 / Codex",
		},
		{
			name:         "openai o3",
			slug:         "openai/o3-mini",
			display:      "",
			wantPool:     PoolClaudeGPT,
			wantSubModel: "GPT-5.2 / Codex",
			wantQuotaSub: "GPT-5.2 / Codex",
		},
		{
			name:         "anthropic display only",
			slug:         "",
			display:      "Anthropic Sonnet",
			wantPool:     PoolClaudeGPT,
			wantSubModel: "Anthropic Sonnet",
			wantQuotaSub: "Claude 4.5 Sonnet / Opus",
		},
		{
			name:         "empty slug neon display",
			slug:         "",
			display:      "Gemini 4 Neon (XHigh)",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Neon (High)",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "empty slug flash display",
			slug:         "",
			display:      "Gemini Flash Preview",
			wantPool:     PoolGemini,
			wantSubModel: "Gemini 4 Flash",
			wantQuotaSub: "Gemini 4 Flash",
		},
		{
			name:         "unknown fallback strips effort suffix",
			slug:         "future-experimental",
			display:      "Future Model (Medium)",
			wantPool:     PoolGemini,
			wantSubModel: "Future Model",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "unknown slug only fallback",
			slug:         "future-experimental",
			display:      "",
			wantPool:     PoolGemini,
			wantSubModel: "future-experimental",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
		{
			name:         "both empty fallback",
			slug:         "",
			display:      "",
			wantPool:     PoolGemini,
			wantSubModel: "Unknown Model",
			wantQuotaSub: "Gemini 4 Neon (High)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyPool(tc.slug, tc.display); got != tc.wantPool {
				t.Errorf("ClassifyPool(%q, %q) = %q, want %q", tc.slug, tc.display, got, tc.wantPool)
			}
			if got := ClassifySubModel(tc.slug, tc.display); got != tc.wantSubModel {
				t.Errorf("ClassifySubModel(%q, %q) = %q, want %q", tc.slug, tc.display, got, tc.wantSubModel)
			}
			if got := ClassifyQuotaSubModel(tc.slug, tc.display); got != tc.wantQuotaSub {
				t.Errorf("ClassifyQuotaSubModel(%q, %q) = %q, want %q", tc.slug, tc.display, got, tc.wantQuotaSub)
			}
		})
	}
}

func TestBoundarySafeOpenAITokens(t *testing.T) {
	shouldMatchGPT := []struct {
		slug    string
		display string
	}{
		{"o1", ""},
		{"o1-preview", ""},
		{"o3", ""},
		{"o3-mini", ""},
		{"o4-mini-high", ""},
		{"", "OpenAI o3 (High)"},
		{"", "Reasoning o4"},
	}
	for _, tc := range shouldMatchGPT {
		if got := ClassifyPool(tc.slug, tc.display); got != PoolClaudeGPT {
			t.Errorf("ClassifyPool(%q, %q) = %q, want %q", tc.slug, tc.display, got, PoolClaudeGPT)
		}
		if got := ClassifySubModel(tc.slug, tc.display); got != SubModelGPTCodex {
			t.Errorf("ClassifySubModel(%q, %q) = %q, want %q", tc.slug, tc.display, got, SubModelGPTCodex)
		}
		if got := ClassifyQuotaSubModel(tc.slug, tc.display); got != SubModelGPTCodex {
			t.Errorf("ClassifyQuotaSubModel(%q, %q) = %q, want %q", tc.slug, tc.display, got, SubModelGPTCodex)
		}
	}

	shouldNotMatchGPT := []struct {
		slug    string
		display string
	}{
		{"model", "My Model (High)"},
		{"hero1", "Hero1"},
		{"audio3-pro", "Audio3 Pro"},
		{"pro4-vision", "Pro4 Vision"},
		{"o10-nano", "O10 Nano"},
	}
	for _, tc := range shouldNotMatchGPT {
		if got := ClassifyPool(tc.slug, tc.display); got != PoolGemini {
			t.Errorf("ClassifyPool(%q, %q) = %q, want %q (false match on boundary token)", tc.slug, tc.display, got, PoolGemini)
		}
		if got := ClassifySubModel(tc.slug, tc.display); got == SubModelGPTCodex {
			t.Errorf("ClassifySubModel(%q, %q) = %q, must not match GPT bucket", tc.slug, tc.display, got)
		}
	}
}
