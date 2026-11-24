package metrics

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

//go:embed pricing.default.json
var embeddedDefaultPricing []byte

// Calculator resolves model pricing rules (including long-context tiers) and enriches
// RequestRecord instances with Pure Decode TPS, cache hit rate, cost, and cache savings.
// Calculator is immutable after creation and safe for concurrent use.
type Calculator struct {
	cfg model.PricingConfig
}

// DefaultPricingConfig parses and returns the embedded default pricing configuration.
func DefaultPricingConfig() (model.PricingConfig, error) {
	var cfg model.PricingConfig
	if err := json.Unmarshal(embeddedDefaultPricing, &cfg); err != nil {
		return model.PricingConfig{}, fmt.Errorf("parse embedded default pricing: %w", err)
	}
	return cfg, nil
}

// NewDefaultCalculator constructs a Calculator using the embedded pricing table.
func NewDefaultCalculator() (*Calculator, error) {
	cfg, err := DefaultPricingConfig()
	if err != nil {
		return nil, err
	}
	return &Calculator{cfg: cfg}, nil
}

// NewCalculator constructs a Calculator starting from embedded defaults and optionally
// merging a user-supplied override JSON file if overridePath is non-empty.
func NewCalculator(overridePath string) (*Calculator, error) {
	base, err := DefaultPricingConfig()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(overridePath) == "" {
		return &Calculator{cfg: base}, nil
	}
	raw, err := os.ReadFile(overridePath)
	if err != nil {
		return nil, fmt.Errorf("read pricing override %q: %w", overridePath, err)
	}
	var override model.PricingConfig
	if err := json.Unmarshal(raw, &override); err != nil {
		return nil, fmt.Errorf("parse pricing override %q: %w", overridePath, err)
	}
	merged := mergePricingConfig(base, override)
	return &Calculator{cfg: merged}, nil
}

// NewCalculatorFromConfig constructs a Calculator from an explicit PricingConfig.
func NewCalculatorFromConfig(cfg model.PricingConfig) *Calculator {
	return &Calculator{cfg: cfg}
}

// Config returns a copy of the active PricingConfig.
func (c *Calculator) Config() model.PricingConfig {
	out := c.cfg
	out.Rules = append([]model.PricingRule(nil), c.cfg.Rules...)
	return out
}

// MatchRule selects the most specific PricingRule matching modelSlug or modelDisplay.
// Specificity prioritizes exact > prefix/wildcard > substring, with longer patterns winning ties.
func (c *Calculator) MatchRule(modelSlug, modelDisplay string) model.PricingRule {
	slug := normalizeModelKey(modelSlug)
	disp := normalizeModelKey(modelDisplay)

	bestScore := -1
	bestIdx := -1
	for i, rule := range c.cfg.Rules {
		pat := normalizeModelKey(rule.ModelPattern)
		if pat == "" || pat == "*" {
			continue
		}
		score := matchScore(pat, slug, disp)
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	if bestIdx >= 0 {
		return c.cfg.Rules[bestIdx]
	}
	return c.cfg.DefaultRule
}

// EstimateCost calculates total USD cost and USD saved from prompt cache reads,
// automatically switching to long-context tier rates when prompt/context size exceeds
// rule.LongContextThreshold.
func (c *Calculator) EstimateCost(rule model.PricingRule, input, cacheRead, cacheWrite, output, contextUsed int64) (costUSD, savedUSD float64) {
	if input < 0 {
		input = 0
	}
	if cacheRead < 0 {
		cacheRead = 0
	}
	if cacheWrite < 0 {
		cacheWrite = 0
	}
	if output < 0 {
		output = 0
	}

	inRate := rule.InputPerMillion
	crRate := rule.CacheReadPerMillion
	cwRate := rule.CacheWritePerMillion
	outRate := rule.OutputPerMillion

	promptTotal := input + cacheRead + cacheWrite
	effectiveCtx := promptTotal
	if contextUsed > effectiveCtx {
		effectiveCtx = contextUsed
	}

	if rule.LongContextThreshold > 0 && effectiveCtx > rule.LongContextThreshold {
		if rule.LongInputPerMillion > 0 {
			inRate = rule.LongInputPerMillion
		}
		if rule.LongCacheReadPerMillion > 0 {
			crRate = rule.LongCacheReadPerMillion
		}
		if rule.LongOutputPerMillion > 0 {
			outRate = rule.LongOutputPerMillion
		}
	}

	costUSD = (float64(input)*inRate +
		float64(cacheRead)*crRate +
		float64(cacheWrite)*cwRate +
		float64(output)*outRate) / 1e6

	if cacheRead > 0 && inRate > crRate {
		savedUSD = float64(cacheRead) * (inRate - crRate) / 1e6
	}
	return costUSD, savedUSD
}

// EnrichRecord populates derived metrics on rec in place: ModelDisplay fallback,
// ContextLimit fallback, DecodeTPS, CacheHitRate, CostUSD, and CacheSavedUSD.
func (c *Calculator) EnrichRecord(rec *model.RequestRecord) {
	if rec == nil {
		return
	}
	rule := c.MatchRule(rec.ModelSlug, rec.ModelDisplay)

	if strings.TrimSpace(rec.ModelDisplay) == "" {
		if rule.DisplayName != "" && rule.ModelPattern != "*" {
			rec.ModelDisplay = rule.DisplayName
		} else if rec.ModelSlug != "" {
			rec.ModelDisplay = rec.ModelSlug
		} else {
			rec.ModelDisplay = rule.DisplayName
		}
	}
	if rec.ContextLimit <= 0 && rule.ContextLimit > 0 {
		rec.ContextLimit = rule.ContextLimit
	}
	if rec.ContextUsed <= 0 {
		rec.ContextUsed = rec.InputTokens + rec.CacheReadTokens + rec.CacheWriteTokens + rec.OutputTokens
	}

	rec.DecodeTPS = model.ComputeDecodeTPS(rec.OutputTokens, rec.TotalDuration, rec.TTFT)
	rec.CacheHitRate = model.ComputeCacheHitRate(rec.InputTokens, rec.CacheReadTokens, rec.CacheWriteTokens)
	rec.CostUSD, rec.CacheSavedUSD = c.EstimateCost(
		rule,
		rec.InputTokens,
		rec.CacheReadTokens,
		rec.CacheWriteTokens,
		rec.OutputTokens,
		rec.ContextUsed,
	)
}

func mergePricingConfig(base, override model.PricingConfig) model.PricingConfig {
	merged := base
	if override.Version > 0 {
		merged.Version = override.Version
	}
	if override.DefaultRule.ModelPattern != "" ||
		override.DefaultRule.InputPerMillion > 0 ||
		override.DefaultRule.OutputPerMillion > 0 ||
		override.DefaultRule.ContextLimit > 0 {
		merged.DefaultRule = override.DefaultRule
	}

	if len(override.Rules) == 0 {
		return merged
	}

	byPat := make(map[string]int, len(base.Rules))
	rules := make([]model.PricingRule, len(base.Rules))
	copy(rules, base.Rules)
	for i, r := range rules {
		byPat[normalizeModelKey(r.ModelPattern)] = i
	}
	for _, r := range override.Rules {
		key := normalizeModelKey(r.ModelPattern)
		if key == "" {
			continue
		}
		if idx, ok := byPat[key]; ok {
			rules[idx] = r
		} else {
			byPat[key] = len(rules)
			rules = append(rules, r)
		}
	}
	merged.Rules = rules
	return merged
}

func normalizeModelKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '*' || r == '.' || r == '_' {
			b.WriteRune(r)
			prevDash = false
		} else if r == '-' || r == ' ' || r == '/' {
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func matchScore(pat, slug, disp string) int {
	if s := singleMatchScore(pat, slug); s >= 0 {
		return s + 10000 // prefer slug matches over display name matches
	}
	if s := singleMatchScore(pat, disp); s >= 0 {
		return s
	}
	return -1
}

func singleMatchScore(pat, candidate string) int {
	if pat == "" || candidate == "" {
		return -1
	}
	if strings.Contains(pat, "*") {
		ok, err := path.Match(pat, candidate)
		if err == nil && ok {
			return 2000 + len(strings.ReplaceAll(pat, "*", ""))
		}
		return -1
	}
	if candidate == pat {
		return 3000 + len(pat)
	}
	if strings.HasPrefix(candidate, pat) {
		return 2500 + len(pat)
	}
	if strings.Contains(candidate, pat) {
		return 1000 + len(pat)
	}
	return -1
}
