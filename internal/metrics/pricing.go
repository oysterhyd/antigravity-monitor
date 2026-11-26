package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/oysterhyd/antigravity-monitor/assets"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

const maxPricingMatchCacheEntries = 256

// Calculator resolves model pricing rules (including placeholder mappings, time-phased rates,
// and long-context tiers) and enriches RequestRecord instances with Pure Decode TPS,
// cache hit rate, cost, and cache savings.
// Calculator is immutable after creation and safe for concurrent use.
type Calculator struct {
	cfg            model.PricingConfig
	normPats       []string
	placeholders   map[string]model.ModelPlaceholder
	labelToModelID map[string]string

	cacheMu    sync.RWMutex
	matchCache map[string]model.PricingRule
}

func newCalculator(cfg model.PricingConfig) *Calculator {
	pats := make([]string, len(cfg.Rules))
	for i, r := range cfg.Rules {
		pats[i] = normalizeModelKey(r.ModelPattern)
	}
	phs := make(map[string]model.ModelPlaceholder, len(cfg.Placeholders))
	for k, v := range cfg.Placeholders {
		key := normalizePlaceholderKey(k)
		if key == "" {
			continue
		}
		phs[key] = model.ModelPlaceholder{
			ModelID: strings.TrimSpace(v.ModelID),
			Label:   strings.TrimSpace(v.Label),
		}
	}
	if len(phs) > 0 {
		cfg.Placeholders = phs
	} else {
		cfg.Placeholders = nil
	}
	labelMap := buildUniqueLabelModelMap(phs)
	return &Calculator{
		cfg:            cfg,
		normPats:       pats,
		placeholders:   phs,
		labelToModelID: labelMap,
		matchCache:     make(map[string]model.PricingRule, 32),
	}
}

func buildUniqueLabelModelMap(phs map[string]model.ModelPlaceholder) map[string]string {
	type entry struct {
		modelID   string
		ambiguous bool
	}
	acc := make(map[string]entry, len(phs))
	// Only index explicit catalog placeholder entries (whose model_id is a concrete model ID,
	// never pricing rule prefix patterns such as "claude-haiku-5").
	for _, ph := range phs {
		lbl := strings.ToLower(strings.TrimSpace(ph.Label))
		mid := strings.TrimSpace(ph.ModelID)
		if lbl == "" || mid == "" {
			continue
		}
		if cur, exists := acc[lbl]; exists {
			if !strings.EqualFold(cur.modelID, mid) {
				cur.ambiguous = true
				acc[lbl] = cur
			}
			continue
		}
		acc[lbl] = entry{modelID: mid}
	}

	if len(acc) == 0 {
		return nil
	}
	out := make(map[string]string, len(acc))
	for lbl, e := range acc {
		if !e.ambiguous && e.modelID != "" {
			out[lbl] = e.modelID
		}
	}
	return out
}

// ResolveModelByLabel returns the unique catalog model_id associated with displayLabel
// (case-insensitively), or "" when displayLabel is absent or maps ambiguously to multiple models.
func (c *Calculator) ResolveModelByLabel(displayLabel string) string {
	if c == nil || len(c.labelToModelID) == 0 {
		return ""
	}
	return c.labelToModelID[strings.ToLower(strings.TrimSpace(displayLabel))]
}

func normalizePlaceholderKey(k string) string {
	return strings.ToUpper(strings.TrimSpace(k))
}

// IsPlaceholderModel reports whether s is an opaque Antigravity model enum/placeholder
// identifier (such as MODEL_PLACEHOLDER_M999) rather than a canonical model slug.
func IsPlaceholderModel(s string) bool {
	u := strings.ToUpper(strings.TrimSpace(s))
	return strings.HasPrefix(u, "MODEL_PLACEHOLDER_") || strings.HasPrefix(u, "MODEL_")
}

// DefaultPricingConfig parses and returns the embedded default pricing configuration.
func DefaultPricingConfig() (model.PricingConfig, error) {
	var cfg model.PricingConfig
	if err := json.Unmarshal(assets.DefaultPricingJSON, &cfg); err != nil {
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
	return newCalculator(cfg), nil
}

// NewCalculator constructs a Calculator starting from embedded defaults and optionally
// merging a user-supplied override JSON file if overridePath is non-empty.
func NewCalculator(overridePath string) (*Calculator, error) {
	base, err := DefaultPricingConfig()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(overridePath) == "" {
		return newCalculator(base), nil
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
	return newCalculator(merged), nil
}

// NewCalculatorFromConfig constructs a Calculator from an explicit PricingConfig.
func NewCalculatorFromConfig(cfg model.PricingConfig) *Calculator {
	return newCalculator(cfg)
}

// Config returns a copy of the active PricingConfig.
func (c *Calculator) Config() model.PricingConfig {
	out := c.cfg
	out.Rules = append([]model.PricingRule(nil), c.cfg.Rules...)
	if len(c.cfg.Placeholders) > 0 {
		out.Placeholders = make(map[string]model.ModelPlaceholder, len(c.cfg.Placeholders))
		for k, v := range c.cfg.Placeholders {
			out.Placeholders[k] = v
		}
	}
	return out
}

func (c *Calculator) matchDirectSlugRule(rawSlug string) (model.PricingRule, bool) {
	norm := normalizeModelKey(rawSlug)
	if norm == "" {
		return model.PricingRule{}, false
	}
	bestScore := -1
	bestIdx := -1
	for i := range c.cfg.Rules {
		pat := c.normPats[i]
		if pat == "" || pat == "*" {
			continue
		}
		if score := directMatchScore(pat, norm); score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	if bestIdx >= 0 {
		return c.cfg.Rules[bestIdx], true
	}
	return model.PricingRule{}, false
}

func (c *Calculator) hasDirectRuleMatch(slug string) bool {
	_, ok := c.matchDirectSlugRule(slug)
	return ok
}

// ResolveModel expands opaque Antigravity model placeholder IDs (such as MODEL_PLACEHOLDER_M535)
// and unique catalog display aliases (such as "Orchestrator Worker (Hydrogen)") into canonical
// model slugs and display labels, while always preserving explicit pricing rules configured for
// the raw modelSlug.
func (c *Calculator) ResolveModel(modelSlug, modelDisplay string) (resolvedSlug, resolvedDisplay string) {
	resolvedSlug = strings.TrimSpace(modelSlug)
	resolvedDisplay = strings.TrimSpace(modelDisplay)
	if len(c.placeholders) == 0 {
		return resolvedSlug, resolvedDisplay
	}
	if resolvedSlug != "" {
		if ph, ok := c.placeholders[strings.ToUpper(resolvedSlug)]; ok {
			if ph.ModelID != "" {
				resolvedSlug = ph.ModelID
			}
			if resolvedDisplay == "" && ph.Label != "" {
				resolvedDisplay = ph.Label
			}
			return resolvedSlug, resolvedDisplay
		}
		if !c.hasDirectRuleMatch(resolvedSlug) {
			if aliasID := c.ResolveModelByLabel(resolvedSlug); aliasID != "" {
				if resolvedDisplay == "" {
					resolvedDisplay = resolvedSlug
				}
				resolvedSlug = aliasID
				return resolvedSlug, resolvedDisplay
			}
		}
	}
	if resolvedDisplay != "" {
		if ph, ok := c.placeholders[strings.ToUpper(resolvedDisplay)]; ok {
			if resolvedSlug == "" && ph.ModelID != "" {
				resolvedSlug = ph.ModelID
			}
			if ph.Label != "" {
				resolvedDisplay = ph.Label
			}
			return resolvedSlug, resolvedDisplay
		}
		if resolvedSlug == "" || (IsPlaceholderModel(resolvedSlug) && !c.hasDirectRuleMatch(resolvedSlug)) {
			if aliasID := c.ResolveModelByLabel(resolvedDisplay); aliasID != "" {
				resolvedSlug = aliasID
			}
		}
	}
	return resolvedSlug, resolvedDisplay
}

// HasKnownModel reports whether (modelSlug, modelDisplay) resolves via a configured
// placeholder mapping or matches an explicit (non-default) pricing rule.
func (c *Calculator) HasKnownModel(modelSlug, modelDisplay string) bool {
	if c == nil {
		return false
	}
	slug := strings.TrimSpace(modelSlug)
	disp := strings.TrimSpace(modelDisplay)
	if slug != "" {
		if _, ok := c.placeholders[normalizePlaceholderKey(slug)]; ok {
			return true
		}
	}
	if disp != "" {
		if _, ok := c.placeholders[normalizePlaceholderKey(disp)]; ok {
			return true
		}
	}
	_, matched := c.matchSpecificRule(slug, disp)
	return matched
}

// IsUnresolvedModelEnum reports whether slug is an opaque model enum/placeholder
// (such as MODEL_PLACEHOLDER_M999) that does not resolve to any configured placeholder
// or explicit pricing rule.
func (c *Calculator) IsUnresolvedModelEnum(slug string) bool {
	s := strings.TrimSpace(slug)
	if !IsPlaceholderModel(s) {
		return false
	}
	if c == nil {
		return true
	}
	return !c.HasKnownModel(s, "")
}

// PreferTranscriptModel reports whether transcriptModel should replace currentSlug.
// Transcript models are preferred when currentSlug is empty, or when currentSlug is an
// unresolved model enum/placeholder and transcriptModel is either recognized by pricing
// rules/placeholders or is a canonical non-placeholder model slug.
func (c *Calculator) PreferTranscriptModel(currentSlug, currentDisplay, transcriptModel string) bool {
	tr := strings.TrimSpace(transcriptModel)
	if tr == "" {
		return false
	}
	cur := strings.TrimSpace(currentSlug)
	if cur == "" {
		return true
	}
	if !c.IsUnresolvedModelEnum(cur) {
		return false
	}
	if c != nil && c.HasKnownModel(tr, "") {
		return true
	}
	return !IsPlaceholderModel(tr)
}

func (c *Calculator) matchSpecificRule(modelSlug, modelDisplay string) (model.PricingRule, bool) {
	if rule, ok := c.matchDirectSlugRule(modelSlug); ok {
		return rule, true
	}
	if strings.TrimSpace(modelSlug) == "" && IsPlaceholderModel(modelDisplay) {
		if rule, ok := c.matchDirectSlugRule(modelDisplay); ok {
			return rule, true
		}
	}
	resSlug, resDisp := c.ResolveModel(modelSlug, modelDisplay)
	slug := normalizeModelKey(resSlug)
	disp := normalizeModelKey(resDisp)

	bestScore := -1
	bestIdx := -1
	for i := range c.cfg.Rules {
		pat := c.normPats[i]
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
		return c.cfg.Rules[bestIdx], true
	}
	return c.cfg.DefaultRule, false
}

// MatchRule selects the most specific PricingRule matching modelSlug or modelDisplay.
// Specificity prioritizes exact > prefix/wildcard > substring, with longer patterns winning ties.
func (c *Calculator) MatchRule(modelSlug, modelDisplay string) model.PricingRule {
	cacheKey := modelSlug + "\x00" + modelDisplay
	c.cacheMu.RLock()
	if cached, ok := c.matchCache[cacheKey]; ok {
		c.cacheMu.RUnlock()
		return cached
	}
	c.cacheMu.RUnlock()

	matched, _ := c.matchSpecificRule(modelSlug, modelDisplay)

	c.cacheMu.Lock()
	if len(c.matchCache) < maxPricingMatchCacheEntries {
		c.matchCache[cacheKey] = matched
	}
	c.cacheMu.Unlock()
	return matched
}

// EstimateCost calculates total USD cost and USD saved from prompt cache reads,
// automatically switching to long-context tier rates when prompt/context size exceeds
// rule.LongContextThreshold.
func (c *Calculator) EstimateCost(rule model.PricingRule, input, cacheRead, cacheWrite, output, contextUsed int64) (costUSD, savedUSD float64) {
	return c.EstimateCostAt(rule, input, cacheRead, cacheWrite, output, contextUsed, time.Time{})
}

// EstimateCostAt calculates total USD cost and USD saved at timestamp at, applying any
// matching time-phased rate interval [From, Until) defined on rule before long-context tiering.
func (c *Calculator) EstimateCostAt(rule model.PricingRule, input, cacheRead, cacheWrite, output, contextUsed int64, at time.Time) (costUSD, savedUSD float64) {
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

	effective := resolvePricingRuleAt(rule, at)
	inRate := effective.InputPerMillion
	crRate := effective.CacheReadPerMillion
	cwRate := effective.CacheWritePerMillion
	outRate := effective.OutputPerMillion

	promptTotal := input + cacheRead + cacheWrite
	effectiveCtx := promptTotal
	if contextUsed > effectiveCtx {
		effectiveCtx = contextUsed
	}

	if effective.LongContextThreshold > 0 && effectiveCtx > effective.LongContextThreshold {
		if effective.LongInputPerMillion > 0 {
			inRate = effective.LongInputPerMillion
		}
		if effective.LongCacheReadPerMillion > 0 {
			crRate = effective.LongCacheReadPerMillion
		}
		if effective.LongOutputPerMillion > 0 {
			outRate = effective.LongOutputPerMillion
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

func resolvePricingRuleAt(rule model.PricingRule, at time.Time) model.PricingRule {
	if len(rule.Phases) == 0 {
		return rule
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	for _, ph := range rule.Phases {
		hasFrom := strings.TrimSpace(ph.From) != ""
		hasUntil := strings.TrimSpace(ph.Until) != ""
		if !hasFrom && !hasUntil {
			continue
		}
		var fromT, untilT time.Time
		if hasFrom {
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(ph.From))
			if err != nil {
				continue
			}
			fromT = t.UTC()
		}
		if hasUntil {
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(ph.Until))
			if err != nil {
				continue
			}
			untilT = t.UTC()
		}
		if hasFrom && hasUntil && !fromT.Before(untilT) {
			continue
		}
		if hasFrom && at.Before(fromT) {
			continue
		}
		if hasUntil && !at.Before(untilT) {
			continue
		}
		out := rule
		out.InputPerMillion = ph.InputPerMillion
		out.CacheReadPerMillion = ph.CacheReadPerMillion
		if ph.CacheWritePerMillion > 0 {
			out.CacheWritePerMillion = ph.CacheWritePerMillion
		}
		out.OutputPerMillion = ph.OutputPerMillion
		if ph.LongContextThreshold > 0 {
			out.LongContextThreshold = ph.LongContextThreshold
		}
		if ph.LongInputPerMillion > 0 {
			out.LongInputPerMillion = ph.LongInputPerMillion
		}
		if ph.LongCacheReadPerMillion > 0 {
			out.LongCacheReadPerMillion = ph.LongCacheReadPerMillion
		}
		if ph.LongOutputPerMillion > 0 {
			out.LongOutputPerMillion = ph.LongOutputPerMillion
		}
		return out
	}
	return rule
}

// EnrichRecord populates derived metrics on rec in place: placeholder resolution,
// ModelDisplay fallback, ContextLimit fallback, DecodeTPS, CacheHitRate, CostUSD, and CacheSavedUSD.
func (c *Calculator) EnrichRecord(rec *model.RequestRecord) {
	if rec == nil {
		return
	}
	rawSlug, rawDisp := rec.ModelSlug, rec.ModelDisplay
	rule := c.MatchRule(rawSlug, rawDisp)
	if resSlug, resDisp := c.ResolveModel(rawSlug, rawDisp); resSlug != rawSlug || resDisp != rawDisp {
		rec.ModelSlug = resSlug
		rec.ModelDisplay = resDisp
	}

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
	rec.CostUSD, rec.CacheSavedUSD = c.EstimateCostAt(
		rule,
		rec.InputTokens,
		rec.CacheReadTokens,
		rec.CacheWriteTokens,
		rec.OutputTokens,
		rec.ContextUsed,
		rec.Timestamp,
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

	if len(base.Placeholders) > 0 || len(override.Placeholders) > 0 {
		mergedPhs := make(map[string]model.ModelPlaceholder, len(base.Placeholders)+len(override.Placeholders))
		for k, v := range base.Placeholders {
			key := normalizePlaceholderKey(k)
			if key == "" {
				continue
			}
			mergedPhs[key] = model.ModelPlaceholder{
				ModelID: strings.TrimSpace(v.ModelID),
				Label:   strings.TrimSpace(v.Label),
			}
		}
		for k, v := range override.Placeholders {
			key := normalizePlaceholderKey(k)
			if key == "" {
				continue
			}
			mergedPhs[key] = model.ModelPlaceholder{
				ModelID: strings.TrimSpace(v.ModelID),
				Label:   strings.TrimSpace(v.Label),
			}
		}
		merged.Placeholders = mergedPhs
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
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '*' || r == '_' {
			b.WriteRune(r)
			prevDash = false
		} else if r == '-' || r == ' ' || r == '/' || r == '.' {
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// directMatchScore scores exact, prefix, and wildcard matches against candidate,
// excluding substring matches so short generic patterns (e.g. "o3") cannot hijack
// opaque placeholder IDs before placeholder resolution.
func directMatchScore(pat, candidate string) int {
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
	return -1
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
