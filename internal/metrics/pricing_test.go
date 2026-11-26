package metrics

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oysterhyd/antigravity-monitor/assets"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

func TestEmbeddedPricingMatchesAssets(t *testing.T) {
	assetsPath := filepath.Join("..", "..", "assets", "pricing.default.json")
	assetBytes, err := os.ReadFile(assetsPath)
	if err != nil {
		t.Fatalf("read assets/pricing.default.json: %v", err)
	}
	norm := func(b []byte) []byte {
		return bytes.TrimSpace(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")))
	}
	if !bytes.Equal(norm(assetBytes), norm(assets.DefaultPricingJSON)) {
		t.Fatalf("assets.DefaultPricingJSON is out of sync with assets/pricing.default.json")
	}
}

func TestMatchRuleSpecificityAndRealModels(t *testing.T) {
	calc, err := NewDefaultCalculator()
	if err != nil {
		t.Fatalf("NewDefaultCalculator: %v", err)
	}

	cases := []struct {
		slug        string
		display     string
		wantPattern string
	}{
		{"gemini-4-neon", "Gemini 4 Neon (High)", "gemini-4-neon"},
		{"gemini-4-hydrogen-tiered-high", "Gemini 4 Hydrogen (High)", "gemini-4-hydrogen"},
		{"gemini-4-argon-trial", "Gemini 4 Argon (High)", "gemini-4-argon"},
		{"gemini-4-carbon", "Gemini 4 Carbon (High)", "gemini-4-carbon"},
		{"gemini-pro-orchestrator-neon", "Orchestrator (Neon)", "gemini-pro-orchestrator"},
		{"gemini-flash-worker-agy", "Gemini 4 Hydrogen (Orchestrator)", "gemini-flash-worker"},
		{"claude-opus-5-5-high", "Claude Opus 5.5 (High)", "claude-opus-5"},
		{"claude-sonnet-4-5", "Claude 4.5 Sonnet", "claude-sonnet-4"},
		{"", "Gemini 4 Neon (XHigh)", "gemini-4-neon"},
		{"unknown-future-model", "Unknown Future", "*"},
	}

	for _, tc := range cases {
		rule := calc.MatchRule(tc.slug, tc.display)
		if rule.ModelPattern != tc.wantPattern {
			t.Errorf("MatchRule(%q, %q) = %q, want %q", tc.slug, tc.display, rule.ModelPattern, tc.wantPattern)
		}
	}
}

func TestEstimateCostLongContextTiering(t *testing.T) {
	calc, err := NewDefaultCalculator()
	if err != nil {
		t.Fatalf("NewDefaultCalculator: %v", err)
	}
	rule := calc.MatchRule("gemini-4-neon", "")

	// Below 200k threshold: 10k in ($1.25/M), 90k cache_read ($0.125/M), 1k out ($10/M)
	costShort, savedShort := calc.EstimateCost(rule, 10_000, 90_000, 0, 1_000, 101_000)
	wantCostShort := (10_000*1.25 + 90_000*0.125 + 1_000*10.0) / 1e6
	wantSavedShort := (90_000 * (1.25 - 0.125)) / 1e6
	if math.Abs(costShort-wantCostShort) > 1e-12 {
		t.Fatalf("short cost = %.12f, want %.12f", costShort, wantCostShort)
	}
	if math.Abs(savedShort-wantSavedShort) > 1e-12 {
		t.Fatalf("short saved = %.12f, want %.12f", savedShort, wantSavedShort)
	}

	// Above 200k threshold: 20k in ($2.50/M), 250k cache_read ($0.25/M), 2k out ($15/M)
	costLong, savedLong := calc.EstimateCost(rule, 20_000, 250_000, 0, 2_000, 272_000)
	wantCostLong := (20_000*2.5 + 250_000*0.25 + 2_000*15.0) / 1e6
	wantSavedLong := (250_000 * (2.5 - 0.25)) / 1e6
	if math.Abs(costLong-wantCostLong) > 1e-12 {
		t.Fatalf("long cost = %.12f, want %.12f", costLong, wantCostLong)
	}
	if math.Abs(savedLong-wantSavedLong) > 1e-12 {
		t.Fatalf("long saved = %.12f, want %.12f", savedLong, wantSavedLong)
	}

	// Claude cache write pricing check
	claudeRule := calc.MatchRule("claude-sonnet-4", "")
	costClaude, savedClaude := calc.EstimateCost(claudeRule, 2_000, 50_000, 10_000, 1_000, 63_000)
	wantClaude := (2_000*3.0 + 50_000*0.30 + 10_000*3.75 + 1_000*15.0) / 1e6
	wantClaudeSaved := (50_000 * (3.0 - 0.30)) / 1e6
	if math.Abs(costClaude-wantClaude) > 1e-12 {
		t.Fatalf("claude cost = %.12f, want %.12f", costClaude, wantClaude)
	}
	if math.Abs(savedClaude-wantClaudeSaved) > 1e-12 {
		t.Fatalf("claude saved = %.12f, want %.12f", savedClaude, wantClaudeSaved)
	}
}

func TestPricingOverrideAndEnrichRecord(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "pricing.override.json")
	overrideJSON := `{
		"version": 2,
		"rules": [
			{
				"model_pattern": "gemini-4-neon",
				"display_name": "Gemini 4 Neon Custom",
				"context_limit": 512000,
				"input_per_million": 2.0,
				"cache_read_per_million": 0.2,
				"cache_write_per_million": 0.5,
				"output_per_million": 20.0
			},
			{
				"model_pattern": "custom-model-*",
				"display_name": "Custom Wildcard",
				"context_limit": 128000,
				"input_per_million": 1.0,
				"cache_read_per_million": 0.1,
				"cache_write_per_million": 0.2,
				"output_per_million": 4.0
			}
		]
	}`
	if err := os.WriteFile(overridePath, []byte(overrideJSON), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}

	calc, err := NewCalculator(overridePath)
	if err != nil {
		t.Fatalf("NewCalculator: %v", err)
	}

	// Wildcard match from override
	wRule := calc.MatchRule("custom-model-v3", "")
	if wRule.DisplayName != "Custom Wildcard" || wRule.ContextLimit != 128000 {
		t.Fatalf("unexpected wildcard rule: %+v", wRule)
	}

	// Overridden gemini-4-neon rule
	rec := model.RequestRecord{
		ModelSlug:       "gemini-4-neon",
		InputTokens:     10_000,
		CacheReadTokens: 90_000,
		OutputTokens:    1_000,
		TTFT:            2 * time.Second,
		TotalDuration:   12 * time.Second,
	}
	calc.EnrichRecord(&rec)
	if rec.ModelDisplay != "Gemini 4 Neon Custom" {
		t.Fatalf("ModelDisplay = %q, want Gemini 4 Neon Custom", rec.ModelDisplay)
	}
	if rec.ContextLimit != 512000 {
		t.Fatalf("ContextLimit = %d, want 512000", rec.ContextLimit)
	}
	if math.Abs(rec.DecodeTPS-100.0) > 1e-6 {
		t.Fatalf("DecodeTPS = %f, want 100.0", rec.DecodeTPS)
	}
	if math.Abs(rec.CacheHitRate-0.9) > 1e-6 {
		t.Fatalf("CacheHitRate = %f, want 0.9", rec.CacheHitRate)
	}
	wantCost := (10_000*2.0 + 90_000*0.2 + 1_000*20.0) / 1e6
	if math.Abs(rec.CostUSD-wantCost) > 1e-12 {
		t.Fatalf("CostUSD = %.12f, want %.12f", rec.CostUSD, wantCost)
	}
}

func TestPlaceholderResolutionAndPhasedPricing(t *testing.T) {
	calc, err := NewDefaultCalculator()
	if err != nil {
		t.Fatalf("NewDefaultCalculator: %v", err)
	}

	// Placeholder resolution: MODEL_PLACEHOLDER_M535 -> gemini-4-neon-xhigh / Gemini 4 Neon (XHigh)
	recPh := model.RequestRecord{
		ModelSlug:       "MODEL_PLACEHOLDER_M535",
		InputTokens:     10_000,
		CacheReadTokens: 90_000,
		OutputTokens:    1_000,
	}
	calc.EnrichRecord(&recPh)
	if recPh.ModelSlug != "gemini-4-neon-xhigh" || recPh.ModelDisplay != "Gemini 4 Neon (XHigh)" {
		t.Fatalf("placeholder resolution failed: slug=%q display=%q", recPh.ModelSlug, recPh.ModelDisplay)
	}
	if recPh.ContextLimit != 512000 {
		t.Fatalf("ContextLimit = %d, want 512000", recPh.ContextLimit)
	}

	// Time-phased pricing on gemini-3.8-flash: before vs after 2027-01-01T00:00:00Z
	beforeRec := model.RequestRecord{
		ModelSlug:       "MODEL_PLACEHOLDER_M318",
		Timestamp:       time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC),
		InputTokens:     100_000,
		CacheReadTokens: 200_000,
		OutputTokens:    10_000,
	}
	calc.EnrichRecord(&beforeRec)
	wantBefore := (100_000*0.75 + 200_000*0.075 + 10_000*3.75) / 1e6
	if math.Abs(beforeRec.CostUSD-wantBefore) > 1e-12 {
		t.Fatalf("before-phase cost = %.12f, want %.12f", beforeRec.CostUSD, wantBefore)
	}

	afterRec := model.RequestRecord{
		ModelSlug:       "MODEL_PLACEHOLDER_M318",
		Timestamp:       time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC),
		InputTokens:     100_000,
		CacheReadTokens: 200_000,
		OutputTokens:    10_000,
	}
	calc.EnrichRecord(&afterRec)
	wantAfter := (100_000*1.50 + 200_000*0.15 + 10_000*7.50) / 1e6
	if math.Abs(afterRec.CostUSD-wantAfter) > 1e-12 {
		t.Fatalf("after-phase cost = %.12f, want %.12f", afterRec.CostUSD, wantAfter)
	}
}

func TestPlaceholderOverrideCaseInsensitiveDeterministic(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "pricing.override.json")
	overrideJSON := `{
		"version": 2,
		"placeholders": {
			"model_placeholder_m535": {
				"model_id": "claude-opus-5-5",
				"label": "Custom Override M535"
			}
		}
	}`
	if err := os.WriteFile(overridePath, []byte(overrideJSON), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}

	for i := 0; i < 50; i++ {
		calc, err := NewCalculator(overridePath)
		if err != nil {
			t.Fatalf("iter %d: NewCalculator: %v", i, err)
		}
		slug, disp := calc.ResolveModel("MODEL_PLACEHOLDER_M535", "")
		if slug != "claude-opus-5-5" || disp != "Custom Override M535" {
			t.Fatalf("iter %d: ResolveModel(UPPER) = (%q, %q), want (claude-opus-5-5, Custom Override M535)", i, slug, disp)
		}
		slugLow, dispLow := calc.ResolveModel("model_placeholder_m535", "")
		if slugLow != "claude-opus-5-5" || dispLow != "Custom Override M535" {
			t.Fatalf("iter %d: ResolveModel(lower) = (%q, %q), want (claude-opus-5-5, Custom Override M535)", i, slugLow, dispLow)
		}
		cfg := calc.Config()
		if _, dup := cfg.Placeholders["model_placeholder_m535"]; dup {
			t.Fatalf("iter %d: expected normalized uppercase placeholder keys without lowercase duplicate", i)
		}
	}
}

func TestDisplayAliasAppliesToPricingAndIgnoresRulePrefixesAndAmbiguousRules(t *testing.T) {
	calc, err := NewDefaultCalculator()
	if err != nil {
		t.Fatalf("NewDefaultCalculator: %v", err)
	}

	// 1. Display-only "Orchestrator Worker (Hydrogen)" must price identically to "gemini-flash-worker-agy" ($0.000250 for 100 output tokens).
	recByDisp := model.RequestRecord{ModelDisplay: "Orchestrator Worker (Hydrogen)", OutputTokens: 100}
	calc.EnrichRecord(&recByDisp)
	recByID := model.RequestRecord{ModelSlug: "gemini-flash-worker-agy", OutputTokens: 100}
	calc.EnrichRecord(&recByID)

	wantCost := 100 * 2.50 / 1e6
	if math.Abs(recByDisp.CostUSD-wantCost) > 1e-12 || math.Abs(recByDisp.CostUSD-recByID.CostUSD) > 1e-12 {
		t.Fatalf("Orchestrator Worker cost = %.6f (byID=%.6f), want %.6f", recByDisp.CostUSD, recByID.CostUSD, wantCost)
	}

	// 2. Pricing rule prefix ("claude-haiku-5" for "Claude Haiku 5.5") must NOT be exposed as an exact label alias.
	if got := calc.ResolveModelByLabel("Claude Haiku 5.5"); got != "" {
		t.Fatalf("ResolveModelByLabel(Claude Haiku 5.5) = %q, want empty (must not return pricing prefix)", got)
	}

	// 3. Two custom pricing rules sharing the same DisplayName must not resolve to the first rule.
	customCalc := NewCalculatorFromConfig(model.PricingConfig{
		Version: 2,
		Rules: []model.PricingRule{
			{ModelPattern: "alpha-v1", DisplayName: "Custom", OutputPerMillion: 10},
			{ModelPattern: "beta-v2", DisplayName: "Custom", OutputPerMillion: 20},
		},
		Placeholders: map[string]model.ModelPlaceholder{
			"MODEL_PLACEHOLDER_A": {ModelID: "model-a", Label: "Ambiguous"},
			"MODEL_PLACEHOLDER_B": {ModelID: "model-b", Label: "Ambiguous"},
			"MODEL_PLACEHOLDER_U": {ModelID: "model-unique", Label: "Unique Label"},
		},
	})
	if got := customCalc.ResolveModelByLabel("Custom"); got != "" {
		t.Fatalf("ResolveModelByLabel(Custom) = %q, want empty", got)
	}
	if got := customCalc.ResolveModelByLabel("Ambiguous"); got != "" {
		t.Fatalf("ResolveModelByLabel(Ambiguous) = %q, want empty", got)
	}
	if got := customCalc.ResolveModelByLabel("Unique Label"); got != "model-unique" {
		t.Fatalf("ResolveModelByLabel(Unique Label) = %q, want model-unique", got)
	}

	// 4. Explicit pricing rule for a placeholder model ID must NOT be overridden by a display label alias.
	overrideCfg := calc.Config()
	overrideCfg.Rules = append(overrideCfg.Rules, model.PricingRule{
		ModelPattern:     "MODEL_PLACEHOLDER_M999",
		DisplayName:      "Custom M999",
		OutputPerMillion: 40.0,
	})
	overrideCalc := NewCalculatorFromConfig(overrideCfg)
	recExact := model.RequestRecord{
		ModelSlug:    "MODEL_PLACEHOLDER_M999",
		ModelDisplay: "Orchestrator Worker (Hydrogen)",
		OutputTokens: 100,
	}
	overrideCalc.EnrichRecord(&recExact)
	wantOverrideCost := 100 * 40.0 / 1e6
	if math.Abs(recExact.CostUSD-wantOverrideCost) > 1e-12 {
		t.Fatalf("explicit M999 rule cost = %.6f, want %.6f (display alias must not override explicit ID rule)", recExact.CostUSD, wantOverrideCost)
	}

	// 5. Built-in display alias supplied in ModelSlug (e.g. from transcript.jsonl) must resolve
	// identically to ModelDisplay ($0.000250 for 100 output tokens, not default $0.000500).
	recFromTranscript := model.RequestRecord{
		ModelSlug:    "Orchestrator Worker (Hydrogen)",
		OutputTokens: 100,
	}
	calc.EnrichRecord(&recFromTranscript)
	if math.Abs(recFromTranscript.CostUSD-wantCost) > 1e-12 {
		t.Fatalf("transcript alias cost = %.6f, want %.6f", recFromTranscript.CostUSD, wantCost)
	}
}

func TestBuiltInPlaceholderCustomRulePriority(t *testing.T) {
	baseCalc, err := NewDefaultCalculator()
	if err != nil {
		t.Fatalf("NewDefaultCalculator: %v", err)
	}
	cfg := baseCalc.Config()
	cfg.Rules = append(cfg.Rules, model.PricingRule{
		ModelPattern:     "MODEL_PLACEHOLDER_M535",
		DisplayName:      "Custom M535 Rate",
		OutputPerMillion: 40.0,
	})
	calc := NewCalculatorFromConfig(cfg)

	// 1,000 output tokens on MODEL_PLACEHOLDER_M535 must use the explicit $40/M rule ($0.040),
	// not fall through to the expanded gemini-4-neon rule ($0.010).
	rec := model.RequestRecord{
		ModelSlug:    "MODEL_PLACEHOLDER_M535",
		OutputTokens: 1_000,
	}
	calc.EnrichRecord(&rec)
	want := 1_000 * 40.0 / 1e6
	if math.Abs(rec.CostUSD-want) > 1e-12 {
		t.Fatalf("custom M535 cost = %.6f, want %.6f", rec.CostUSD, want)
	}
	if rec.ModelSlug != "gemini-4-neon-xhigh" || rec.ModelDisplay != "Gemini 4 Neon (XHigh)" {
		t.Fatalf("expected placeholder display/slug resolution preserved, got slug=%q display=%q", rec.ModelSlug, rec.ModelDisplay)
	}

	// Direct MatchRule call on raw placeholder ID must also honor the custom rule.
	if rule := calc.MatchRule("MODEL_PLACEHOLDER_M535", ""); rule.OutputPerMillion != 40.0 {
		t.Fatalf("MatchRule(MODEL_PLACEHOLDER_M535) output rate = %.2f, want 40.0", rule.OutputPerMillion)
	}

	// Placeholder in ModelDisplay only must also honor the custom rule.
	recDispOnly := model.RequestRecord{
		ModelDisplay: "MODEL_PLACEHOLDER_M535",
		OutputTokens: 1_000,
	}
	calc.EnrichRecord(&recDispOnly)
	if math.Abs(recDispOnly.CostUSD-want) > 1e-12 {
		t.Fatalf("display-only custom M535 cost = %.6f, want %.6f", recDispOnly.CostUSD, want)
	}
}

func TestClaudeFable52DisplayOnlyAndClaude4EnumPricing(t *testing.T) {
	calc, err := NewDefaultCalculator()
	if err != nil {
		t.Fatalf("NewDefaultCalculator: %v", err)
	}

	// 1. Display-only "Claude Fable 5.2" must match claude-fable-5-2 ($40/M -> $0.040/1k),
	// matching slug "claude-fable-5-2" rather than falling back to claude-fable-5 ($0.050/1k).
	fableCases := []struct {
		slug    string
		display string
		wantOut float64
		wantPat string
	}{
		{"claude-fable-5-2", "", 0.040, "claude-fable-5-2"},
		{"claude-fable-5.2", "", 0.040, "claude-fable-5-2"},
		{"", "Claude Fable 5.2", 0.040, "claude-fable-5-2"},
		{"", "Claude Fable 5.2 (High)", 0.040, "claude-fable-5-2"},
		{"Claude Fable 5.2", "", 0.040, "claude-fable-5-2"},
		{"", "Claude Fable 5", 0.050, "claude-fable-5"},
		{"claude-fable-5", "", 0.050, "claude-fable-5"},
	}
	for _, tc := range fableCases {
		rule := calc.MatchRule(tc.slug, tc.display)
		if rule.ModelPattern != tc.wantPat {
			t.Errorf("MatchRule(%q, %q) = %q, want %q", tc.slug, tc.display, rule.ModelPattern, tc.wantPat)
		}
		rec := model.RequestRecord{ModelSlug: tc.slug, ModelDisplay: tc.display, OutputTokens: 1_000}
		calc.EnrichRecord(&rec)
		if math.Abs(rec.CostUSD-tc.wantOut) > 1e-12 {
			t.Errorf("EnrichRecord(%q, %q) cost = %.6f, want %.6f", tc.slug, tc.display, rec.CostUSD, tc.wantOut)
		}
	}

	// 2. All four decoded Claude 4 enums (281, 282, 290, 291) must resolve to Sonnet 4 ($0.015/1k)
	// and Opus 4 ($0.075/1k) instead of falling through to the default rule ($0.005/1k).
	enumCases := []struct {
		enumStr string
		wantPat string
		wantOut float64
	}{
		{"MODEL_CLAUDE_4_SONNET", "claude-sonnet-4", 0.015},
		{"MODEL_CLAUDE_4_SONNET_THINKING", "claude-sonnet-4", 0.015},
		{"MODEL_CLAUDE_4_OPUS", "claude-opus-4", 0.075},
		{"MODEL_CLAUDE_4_OPUS_THINKING", "claude-opus-4", 0.075},
	}
	for _, tc := range enumCases {
		if calc.IsUnresolvedModelEnum(tc.enumStr) {
			t.Errorf("IsUnresolvedModelEnum(%q) = true, want false", tc.enumStr)
		}
		rule := calc.MatchRule(tc.enumStr, "")
		if rule.ModelPattern != tc.wantPat {
			t.Errorf("MatchRule(%q) = %q, want %q", tc.enumStr, rule.ModelPattern, tc.wantPat)
		}
		rec := model.RequestRecord{ModelSlug: tc.enumStr, OutputTokens: 1_000}
		calc.EnrichRecord(&rec)
		if math.Abs(rec.CostUSD-tc.wantOut) > 1e-12 {
			t.Errorf("EnrichRecord(%q) cost = %.6f, want %.6f", tc.enumStr, rec.CostUSD, tc.wantOut)
		}
	}
}
