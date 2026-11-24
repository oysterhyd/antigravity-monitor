package metrics

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if !bytes.Equal(norm(assetBytes), norm(embeddedDefaultPricing)) {
		t.Fatalf("internal/metrics/pricing.default.json is out of sync with assets/pricing.default.json")
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
