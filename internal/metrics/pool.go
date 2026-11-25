package metrics

import "strings"

// ModelPool identifies the quota / cost pool a model belongs to.
type ModelPool string

const (
	PoolGemini    ModelPool = "gemini"
	PoolClaudeGPT ModelPool = "claude_gpt"
)

// Canonical sub-model display buckets.
const (
	SubModelGeminiNeon        = "Gemini 4 Neon (High)"
	SubModelGeminiFlash       = "Gemini 4 Flash"
	SubModelGeminiHydrogen    = "Gemini 4 Hydrogen"
	SubModelGeminiCarbon      = "Gemini 4 Carbon"
	SubModelGeminiArgon       = "Gemini 4 Argon"
	SubModelGeminiProOrch     = "Gemini Pro Orchestrator"
	SubModelGemini25Pro       = "Gemini 2.5 Pro"
	SubModelClaudeOpus55      = "Claude Opus 5.5"
	SubModelClaude45          = "Claude 4.5 Sonnet / Opus"
	SubModelClaudeHaiku4      = "Claude Haiku 4"
	SubModelGPTCodex          = "GPT-5.2 / Codex"
	SubModelUnknown           = "Unknown Model"
)

// ClassifyPool classifies a model by its slug (preferred) and display name into
// either PoolClaudeGPT or PoolGemini (with unknown/empty falling back to PoolGemini).
func ClassifyPool(modelSlug, modelDisplay string) ModelPool {
	if p, ok := classifyPoolNorm(normalizeModelKey(modelSlug)); ok {
		return p
	}
	if p, ok := classifyPoolNorm(normalizeModelKey(modelDisplay)); ok {
		return p
	}
	return PoolGemini
}

func classifyPoolNorm(norm string) (ModelPool, bool) {
	if norm == "" {
		return "", false
	}
	if hasClaudeGPTIndicator(norm) {
		return PoolClaudeGPT, true
	}
	if strings.Contains(norm, "gemini") || strings.Contains(norm, "orchestrator") {
		return PoolGemini, true
	}
	return "", false
}

func hasClaudeGPTIndicator(norm string) bool {
	for _, kw := range []string{"claude", "anthropic", "opus", "sonnet", "haiku", "gpt", "codex", "openai"} {
		if strings.Contains(norm, kw) {
			return true
		}
	}
	return hasBoundaryOpenAIReasoningToken(norm)
}

func hasGPTIndicator(norm string) bool {
	for _, kw := range []string{"gpt", "codex", "openai"} {
		if strings.Contains(norm, kw) {
			return true
		}
	}
	return hasBoundaryOpenAIReasoningToken(norm)
}

// hasBoundaryOpenAIReasoningToken checks for standalone o1 / o3 / o4 tokens separated
// by normalized delimiters ('-', '.', '_') so words like "model" or "hero1" never match.
func hasBoundaryOpenAIReasoningToken(norm string) bool {
	if norm == "" {
		return false
	}
	tokens := strings.FieldsFunc(norm, func(r rune) bool {
		return r == '-' || r == '.' || r == '_'
	})
	for _, tok := range tokens {
		if tok == "o1" || tok == "o3" || tok == "o4" {
			return true
		}
	}
	return false
}

// ClassifySubModel maps (modelSlug, modelDisplay) cleanly to family display buckets,
// collapsing effort/tier suffixes such as (High), (XHigh), (Medium), (Low), (Trial), (Orchestrator).
func ClassifySubModel(modelSlug, modelDisplay string) string {
	slug := normalizeModelKey(modelSlug)
	disp := normalizeModelKey(modelDisplay)

	if slug != "" {
		if bucket, ok := classifySubModelFromSlug(slug); ok {
			return bucket
		}
	}

	if bucket, ok := classifySubModelFromDisplay(disp, slug == ""); ok {
		return bucket
	}

	if cleaned := cleanModelDisplayFallback(modelDisplay); cleaned != "" {
		return cleaned
	}
	if s := strings.TrimSpace(modelSlug); s != "" {
		return s
	}
	return SubModelUnknown
}

// ClassifyQuotaSubModel maps (modelSlug, modelDisplay) into one of the 4 canonical
// Account Quota buckets ("Gemini 4 Neon (High)", "Gemini 4 Flash",
// "Claude 4.5 Sonnet / Opus", "GPT-5.2 / Codex").
func ClassifyQuotaSubModel(modelSlug, modelDisplay string) string {
	pool := ClassifyPool(modelSlug, modelDisplay)
	slug := normalizeModelKey(modelSlug)
	disp := normalizeModelKey(modelDisplay)

	if pool == PoolClaudeGPT {
		if hasGPTIndicator(slug) || ((slug == "" || !hasClaudeFamilyIndicator(slug)) && hasGPTIndicator(disp)) {
			return SubModelGPTCodex
		}
		return SubModelClaude45
	}

	if strings.Contains(slug, "flash") || (slug == "" && strings.Contains(disp, "flash")) {
		return SubModelGeminiFlash
	}
	return SubModelGeminiNeon
}

func hasClaudeFamilyIndicator(norm string) bool {
	for _, kw := range []string{"claude", "anthropic", "opus", "sonnet", "haiku"} {
		if strings.Contains(norm, kw) {
			return true
		}
	}
	return false
}

func classifySubModelFromSlug(slug string) (string, bool) {
	switch {
	case strings.HasPrefix(slug, "gemini-4-neon"):
		return SubModelGeminiNeon, true
	case strings.HasPrefix(slug, "gemini-flash-worker"),
		strings.HasPrefix(slug, "gemini-4-flash"),
		strings.HasPrefix(slug, "gemini-2.5-flash"),
		strings.Contains(slug, "flash"):
		return SubModelGeminiFlash, true
	case strings.HasPrefix(slug, "gemini-4-hydrogen"):
		return SubModelGeminiHydrogen, true
	case strings.HasPrefix(slug, "gemini-4-carbon"):
		return SubModelGeminiCarbon, true
	case strings.HasPrefix(slug, "gemini-4-argon"):
		return SubModelGeminiArgon, true
	case strings.HasPrefix(slug, "gemini-pro-orchestrator"):
		return SubModelGeminiProOrch, true
	case strings.HasPrefix(slug, "gemini-2.5-pro"):
		return SubModelGemini25Pro, true
	case strings.HasPrefix(slug, "claude-opus-5"):
		return SubModelClaudeOpus55, true
	case strings.HasPrefix(slug, "claude-opus-4"), strings.HasPrefix(slug, "claude-sonnet-4"):
		return SubModelClaude45, true
	case strings.HasPrefix(slug, "claude-haiku-4"):
		return SubModelClaudeHaiku4, true
	case strings.HasPrefix(slug, "gpt-"),
		strings.HasPrefix(slug, "codex"),
		strings.HasPrefix(slug, "openai"),
		hasGPTIndicator(slug):
		return SubModelGPTCodex, true
	default:
		return "", false
	}
}

func classifySubModelFromDisplay(disp string, slugEmpty bool) (string, bool) {
	if disp == "" {
		return "", false
	}
	switch {
	case strings.Contains(disp, "gemini-4-neon"):
		return SubModelGeminiNeon, true
	case slugEmpty && strings.Contains(disp, "flash"):
		return SubModelGeminiFlash, true
	case strings.Contains(disp, "gemini-4-hydrogen"):
		return SubModelGeminiHydrogen, true
	case strings.Contains(disp, "gemini-4-carbon"):
		return SubModelGeminiCarbon, true
	case strings.Contains(disp, "gemini-4-argon"):
		return SubModelGeminiArgon, true
	case strings.Contains(disp, "gemini-pro-orchestrator"):
		return SubModelGeminiProOrch, true
	case strings.Contains(disp, "gemini-2.5-pro"):
		return SubModelGemini25Pro, true
	case strings.Contains(disp, "claude-opus-5"):
		return SubModelClaudeOpus55, true
	case strings.Contains(disp, "claude-opus-4"),
		strings.Contains(disp, "claude-sonnet-4"),
		strings.Contains(disp, "claude-4.5-sonnet"),
		strings.Contains(disp, "claude-4.5-opus"):
		return SubModelClaude45, true
	case strings.Contains(disp, "claude-haiku-4"):
		return SubModelClaudeHaiku4, true
	case hasGPTIndicator(disp):
		return SubModelGPTCodex, true
	default:
		return "", false
	}
}

func cleanModelDisplayFallback(display string) string {
	s := strings.TrimSpace(display)
	if s == "" {
		return ""
	}
	for _, suffix := range []string{"(XHigh)", "(High)", "(Medium)", "(Low)", "(xhigh)", "(high)", "(medium)", "(low)"} {
		s = strings.ReplaceAll(s, suffix, "")
	}
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}
