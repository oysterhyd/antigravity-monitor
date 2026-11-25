package quota

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
)

const (
	defaultWindowDuration = 5 * time.Hour
	lowQuotaThresholdPct  = 25.0
	maxJSONBytes          = 1 << 20 // 1 MiB
	maxStoredTrendPoints  = 1200
	trendRetention        = 7 * 24 * time.Hour

	// Conservative local rolling-window budgets for compliant burn deduction
	// when running in local-calibrated mode without an external JSON feed.
	geminiWindowCostBudgetUSD = 15.0
	geminiWindowTokenBudget   = 12_000_000.0
	claudeWindowCostBudgetUSD = 8.0
	claudeWindowTokenBudget   = 6_000_000.0
)

type persistedState struct {
	Version      int          `json:"version"`
	Calibrated   bool         `json:"calibrated"`
	Source       SourceKind   `json:"source"`
	SourcePath   string       `json:"source_path,omitempty"`
	AccountLabel string       `json:"account_label,omitempty"`
	PlanLabel    string       `json:"plan_label,omitempty"`
	UpdatedAt    time.Time    `json:"updated_at,omitempty"`
	LastSampled  time.Time    `json:"last_sampled,omitempty"`
	Gemini       PoolQuota    `json:"gemini"`
	ClaudeGPT    PoolQuota    `json:"claude_gpt"`
	Points       []TrendPoint `json:"points,omitempty"`
}

type externalQuotaJSON struct {
	Account   string            `json:"account"`
	Plan      string            `json:"plan"`
	UpdatedAt string            `json:"updated_at"`
	Gemini    *externalPoolJSON `json:"gemini"`
	ClaudeGPT *externalPoolJSON `json:"claude_gpt"`
}

type externalPoolJSON struct {
	RemainingPct float64            `json:"remaining_pct"`
	ResetsInSec  int64              `json:"resets_in_sec"`
	ResetsAt     string             `json:"resets_at"`
	Models       []externalModelRow `json:"models,omitempty"`
}

type externalModelRow struct {
	Name         string  `json:"name"`
	RemainingPct float64 `json:"remaining_pct"`
}

// Service implements compliant local quota tracking, calibration, optional safe
// local JSON ingestion, and atomic quota_history.json persistence.
type Service struct {
	mu          sync.RWMutex
	historyPath string
	state       persistedState
	sourceError string
	httpClient  *http.Client
}

var _ Tracker = (*Service)(nil)

// DefaultHistoryPath resolves the persisted quota history file location alongside settings.json.
func DefaultHistoryPath() string {
	if runtime.GOOS == "windows" {
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			return filepath.Join(dir, "antigravity-monitor", "quota_history.json")
		}
	} else {
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			return filepath.Join(xdg, "antigravity-monitor", "quota_history.json")
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, ".config", "antigravity-monitor", "quota_history.json")
		}
	}
	return filepath.Join(".", "quota_history.json")
}

// NewService opens or initializes a quota tracking Service backed by historyPath.
// Corrupt or empty files recover cleanly to the uncalibrated initial state.
func NewService(historyPath string) *Service {
	s := &Service{
		historyPath: strings.TrimSpace(historyPath),
		state: persistedState{
			Version: 1,
			Source:  SourceUncalibrated,
		},
		httpClient: &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 2 {
					return fmt.Errorf("too many redirects")
				}
				if !isLoopbackURL(req.URL) {
					return ErrUnsafeJSONSource
				}
				return nil
			},
		},
	}
	s.loadFromDisk()
	return s
}

// LastSampledAt returns the timestamp of the last quota sampling/calibration event.
func (s *Service) LastSampledAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.LastSampled
}

// State returns an immutable snapshot evaluated at now without mutating disk.
func (s *Service) State(now time.Time) QuotaSnapshot {
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.buildSnapshotLocked(now)
}

// Calibrate validates and applies a manual local baseline, records a trend sample,
// and atomically persists quota_history.json.
func (s *Service) Calibrate(in CalibrationInput, now time.Time) (QuotaSnapshot, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if err := validatePct(in.GeminiRemainingPct); err != nil {
		return s.State(now), err
	}
	if err := validatePct(in.ClaudeRemainingPct); err != nil {
		return s.State(now), err
	}
	if in.GeminiResetIn < 0 || in.ClaudeResetIn < 0 {
		return s.State(now), ErrInvalidReset
	}

	if src := strings.TrimSpace(in.JSONSource); src != "" {
		return s.SetJSONSource(src, now)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	gemPct := round1(in.GeminiRemainingPct)
	claPct := round1(in.ClaudeRemainingPct)
	gemReset := now.Add(in.GeminiResetIn)
	claReset := now.Add(in.ClaudeResetIn)
	resetEvent := false
	if in.GeminiResetIn == 0 {
		gemPct = 100
		gemReset = now.Add(defaultWindowDuration)
		resetEvent = true
	}
	if in.ClaudeResetIn == 0 {
		claPct = 100
		claReset = now.Add(defaultWindowDuration)
		resetEvent = true
	}

	s.state.Calibrated = true
	s.state.Source = SourceLocal
	s.state.SourcePath = ""
	s.state.UpdatedAt = now
	s.state.LastSampled = now
	s.sourceError = ""
	s.state.Gemini = PoolQuota{
		RemainingPct: gemPct,
		ResetAt:      gemReset,
		SubModels:    defaultGeminiSubModels(gemPct),
	}
	s.state.ClaudeGPT = PoolQuota{
		RemainingPct: claPct,
		ResetAt:      claReset,
		SubModels:    defaultClaudeSubModels(claPct),
	}
	s.appendPointLocked(TrendPoint{
		Timestamp:  now,
		GeminiPct:  gemPct,
		ClaudePct:  claPct,
		ResetEvent: resetEvent,
	})
	_ = s.saveLocked()
	return s.buildSnapshotLocked(now), nil
}

// ClearCalibration resets calibration back to the uncalibrated state while preserving history path.
func (s *Service) ClearCalibration(now time.Time) (QuotaSnapshot, error) {
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state = persistedState{
		Version: 1,
		Source:  SourceUncalibrated,
	}
	s.sourceError = ""
	err := s.saveLocked()
	return s.buildSnapshotLocked(now), err
}

// SetJSONSource validates and ingests a user-configured local JSON file or loopback HTTP endpoint.
// On error, any prior calibrated state is preserved intact.
func (s *Service) SetJSONSource(source string, now time.Time) (QuotaSnapshot, error) {
	if now.IsZero() {
		now = time.Now()
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return s.State(now), ErrUnsafeJSONSource
	}

	payload, err := s.fetchJSONSource(source)
	if err != nil {
		s.mu.Lock()
		s.sourceError = err.Error()
		snap := s.buildSnapshotLocked(now)
		s.mu.Unlock()
		return snap, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyExternalPayloadLocked(source, payload, now)
	_ = s.saveLocked()
	return s.buildSnapshotLocked(now), nil
}

// SampleNow advances local rolling-window consumption and records a new trend sample.
func (s *Service) SampleNow(now time.Time, burn history.PoolConsumption) QuotaSnapshot {
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.state.Calibrated {
		return s.buildSnapshotLocked(now)
	}
	s.advanceLocalLocked(now, burn)
	_ = s.saveLocked()
	return s.buildSnapshotLocked(now)
}

// Refresh updates quota from the configured JSON source (if active), or falls back cleanly
// to local rolling-window tracking when JSON fails or is unconfigured.
func (s *Service) Refresh(now time.Time, burn history.PoolConsumption) (QuotaSnapshot, error) {
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.RLock()
	srcKind := s.state.Source
	srcPath := s.state.SourcePath
	s.mu.RUnlock()

	if srcKind == SourceJSON && srcPath != "" {
		payload, err := s.fetchJSONSource(srcPath)
		if err == nil {
			s.mu.Lock()
			s.applyExternalPayloadLocked(srcPath, payload, now)
			_ = s.saveLocked()
			snap := s.buildSnapshotLocked(now)
			s.mu.Unlock()
			return snap, nil
		}

		// Graceful fallback: retain prior valid quota & apply local burn while surfacing SourceError.
		s.mu.Lock()
		s.sourceError = err.Error()
		if s.state.Calibrated {
			s.advanceLocalLocked(now, burn)
			_ = s.saveLocked()
		}
		snap := s.buildSnapshotLocked(now)
		s.mu.Unlock()
		return snap, err
	}

	snap := s.SampleNow(now, burn)
	return snap, nil
}

func (s *Service) advanceLocalLocked(now time.Time, burn history.PoolConsumption) {
	resetEvent := false

	// Check rolling-window auto-reset for Gemini pool.
	if !s.state.Gemini.ResetAt.IsZero() && !now.Before(s.state.Gemini.ResetAt) {
		s.state.Gemini.RemainingPct = 100
		s.state.Gemini.ResetAt = nextResetAfter(s.state.Gemini.ResetAt, now)
		resetEvent = true
	}
	// Check rolling-window auto-reset for Claude & GPT pool.
	if !s.state.ClaudeGPT.ResetAt.IsZero() && !now.Before(s.state.ClaudeGPT.ResetAt) {
		s.state.ClaudeGPT.RemainingPct = 100
		s.state.ClaudeGPT.ResetAt = nextResetAfter(s.state.ClaudeGPT.ResetAt, now)
		resetEvent = true
	}

	// Deduct local consumption proportionally within each pool independently.
	if burn.GeminiTokens > 0 || burn.GeminiCostUSD > 0 {
		drop := maxFloat(
			(burn.GeminiCostUSD/geminiWindowCostBudgetUSD)*100.0,
			(float64(burn.GeminiTokens)/geminiWindowTokenBudget)*100.0,
		)
		if drop < 0.1 {
			drop = 0.1
		}
		s.state.Gemini.RemainingPct = round1(clampPct(s.state.Gemini.RemainingPct - drop))
	}
	if burn.ClaudeTokens > 0 || burn.ClaudeCostUSD > 0 {
		drop := maxFloat(
			(burn.ClaudeCostUSD/claudeWindowCostBudgetUSD)*100.0,
			(float64(burn.ClaudeTokens)/claudeWindowTokenBudget)*100.0,
		)
		if drop < 0.1 {
			drop = 0.1
		}
		s.state.ClaudeGPT.RemainingPct = round1(clampPct(s.state.ClaudeGPT.RemainingPct - drop))
	}

	s.state.Gemini.SubModels = adjustSubModels(s.state.Gemini.SubModels, s.state.Gemini.RemainingPct, true)
	s.state.ClaudeGPT.SubModels = adjustSubModels(s.state.ClaudeGPT.SubModels, s.state.ClaudeGPT.RemainingPct, false)
	s.state.UpdatedAt = now
	s.state.LastSampled = now

	s.appendPointLocked(TrendPoint{
		Timestamp:  now,
		GeminiPct:  s.state.Gemini.RemainingPct,
		ClaudePct:  s.state.ClaudeGPT.RemainingPct,
		ResetEvent: resetEvent,
	})
}

func (s *Service) applyExternalPayloadLocked(source string, p externalQuotaJSON, now time.Time) {
	prevGem := s.state.Gemini.RemainingPct
	prevCla := s.state.ClaudeGPT.RemainingPct
	wasCal := s.state.Calibrated

	gemPct := prevGem
	gemReset := s.state.Gemini.ResetAt
	var gemSubs []SubModelQuota
	if p.Gemini != nil {
		gemPct = round1(clampPct(p.Gemini.RemainingPct))
		gemReset = resolveExternalReset(p.Gemini, now)
		gemSubs = convertExternalModels(p.Gemini.Models, gemPct, true)
	} else {
		gemSubs = defaultGeminiSubModels(gemPct)
	}

	claPct := prevCla
	claReset := s.state.ClaudeGPT.ResetAt
	var claSubs []SubModelQuota
	if p.ClaudeGPT != nil {
		claPct = round1(clampPct(p.ClaudeGPT.RemainingPct))
		claReset = resolveExternalReset(p.ClaudeGPT, now)
		claSubs = convertExternalModels(p.ClaudeGPT.Models, claPct, false)
	} else {
		claSubs = defaultClaudeSubModels(claPct)
	}

	resetEvent := wasCal && ((gemPct-prevGem >= 25) || (claPct-prevCla >= 25))

	updated := now
	if ts := strings.TrimSpace(p.UpdatedAt); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			updated = parsed
		}
	}

	s.state.Calibrated = true
	s.state.Source = SourceJSON
	s.state.SourcePath = source
	s.state.AccountLabel = strings.TrimSpace(p.Account)
	s.state.PlanLabel = strings.TrimSpace(p.Plan)
	s.state.UpdatedAt = updated
	s.state.LastSampled = now
	s.sourceError = ""
	s.state.Gemini = PoolQuota{
		RemainingPct: gemPct,
		ResetAt:      gemReset,
		SubModels:    gemSubs,
	}
	s.state.ClaudeGPT = PoolQuota{
		RemainingPct: claPct,
		ResetAt:      claReset,
		SubModels:    claSubs,
	}

	s.appendPointLocked(TrendPoint{
		Timestamp:  now,
		GeminiPct:  gemPct,
		ClaudePct:  claPct,
		ResetEvent: resetEvent,
	})
}

func (s *Service) appendPointLocked(pt TrendPoint) {
	s.state.Points = append(s.state.Points, pt)
	sort.SliceStable(s.state.Points, func(i, j int) bool {
		return s.state.Points[i].Timestamp.Before(s.state.Points[j].Timestamp)
	})
	cutoff := pt.Timestamp.Add(-trendRetention)
	keep := s.state.Points[:0]
	for _, p := range s.state.Points {
		if !p.Timestamp.Before(cutoff) {
			keep = append(keep, p)
		}
	}
	if len(keep) > maxStoredTrendPoints {
		keep = keep[len(keep)-maxStoredTrendPoints:]
	}
	s.state.Points = append([]TrendPoint(nil), keep...)
}

func (s *Service) buildSnapshotLocked(now time.Time) QuotaSnapshot {
	gem := s.state.Gemini
	cla := s.state.ClaudeGPT
	if len(gem.SubModels) > 0 {
		gem.SubModels = append([]SubModelQuota(nil), gem.SubModels...)
	}
	if len(cla.SubModels) > 0 {
		cla.SubModels = append([]SubModelQuota(nil), cla.SubModels...)
	}

	// Evaluate read-only auto-reset display if ResetAt already passed.
	if s.state.Calibrated {
		if !gem.ResetAt.IsZero() && !now.Before(gem.ResetAt) {
			gem.RemainingPct = 100
			gem.ResetAt = nextResetAfter(gem.ResetAt, now)
			gem.SubModels = adjustSubModels(gem.SubModels, 100, true)
		}
		if !cla.ResetAt.IsZero() && !now.Before(cla.ResetAt) {
			cla.RemainingPct = 100
			cla.ResetAt = nextResetAfter(cla.ResetAt, now)
			cla.SubModels = adjustSubModels(cla.SubModels, 100, false)
		}
	}

	cutoff24 := now.Add(-24 * time.Hour)
	var trend24 []TrendPoint
	for _, p := range s.state.Points {
		if !p.Timestamp.Before(cutoff24) {
			trend24 = append(trend24, p)
		}
	}
	trend7d := buildDailyLowestTrend(s.state.Points, now)

	hasLow := s.state.Calibrated && (gem.RemainingPct < lowQuotaThresholdPct || cla.RemainingPct < lowQuotaThresholdPct)

	return QuotaSnapshot{
		Calibrated:   s.state.Calibrated,
		Source:       s.state.Source,
		SourcePath:   s.state.SourcePath,
		SourceError:  s.sourceError,
		AccountLabel: s.state.AccountLabel,
		PlanLabel:    s.state.PlanLabel,
		UpdatedAt:    s.state.UpdatedAt,
		Gemini:       gem,
		ClaudeGPT:    cla,
		HasLowQuota:  hasLow,
		Trend24h:     trend24,
		Trend7d:      trend7d,
	}
}

func buildDailyLowestTrend(points []TrendPoint, now time.Time) []TrendPoint {
	if len(points) == 0 {
		return nil
	}
	loc := now.Location()
	cutoff := now.In(loc).AddDate(0, 0, -6)
	y, m, d := cutoff.Date()
	startDay := time.Date(y, m, d, 0, 0, 0, 0, loc)

	type dayAgg struct {
		day    time.Time
		minGem float64
		minCla float64
		reset  bool
	}
	byDay := make(map[string]*dayAgg)
	var keys []string

	for _, pt := range points {
		t := pt.Timestamp.In(loc)
		if t.Before(startDay) {
			continue
		}
		k := t.Format("2006-01-02")
		agg, ok := byDay[k]
		if !ok {
			py, pm, pd := t.Date()
			agg = &dayAgg{
				day:    time.Date(py, pm, pd, 12, 0, 0, 0, loc),
				minGem: pt.GeminiPct,
				minCla: pt.ClaudePct,
				reset:  pt.ResetEvent,
			}
			byDay[k] = agg
			keys = append(keys, k)
		} else {
			if pt.GeminiPct < agg.minGem {
				agg.minGem = pt.GeminiPct
			}
			if pt.ClaudePct < agg.minCla {
				agg.minCla = pt.ClaudePct
			}
			if pt.ResetEvent {
				agg.reset = true
			}
		}
	}
	sort.Strings(keys)
	out := make([]TrendPoint, 0, len(keys))
	for _, k := range keys {
		a := byDay[k]
		out = append(out, TrendPoint{
			Timestamp:  a.day,
			GeminiPct:  a.minGem,
			ClaudePct:  a.minCla,
			ResetEvent: a.reset,
		})
	}
	return out
}

func (s *Service) fetchJSONSource(source string) (externalQuotaJSON, error) {
	var raw []byte
	var err error

	lower := strings.ToLower(source)
	switch {
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		u, parseErr := url.Parse(source)
		if parseErr != nil || !isLoopbackURL(u) {
			return externalQuotaJSON{}, ErrUnsafeJSONSource
		}
		resp, reqErr := s.httpClient.Get(u.String())
		if reqErr != nil {
			return externalQuotaJSON{}, fmt.Errorf("fetch loopback source: %w", reqErr)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return externalQuotaJSON{}, fmt.Errorf("http %d", resp.StatusCode)
		}
		raw, err = io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
		if err != nil {
			return externalQuotaJSON{}, fmt.Errorf("read response: %w", err)
		}

	case strings.HasPrefix(lower, "file://"):
		u, parseErr := url.Parse(source)
		if parseErr != nil {
			return externalQuotaJSON{}, ErrUnsafeJSONSource
		}
		p := u.Path
		if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		p = filepath.FromSlash(p)
		raw, err = readBoundedFile(p)
		if err != nil {
			return externalQuotaJSON{}, err
		}

	default:
		// Reject unknown URI schemes (ftp://, ws://, etc.) while allowing Windows drive paths (C:\...).
		if strings.Contains(source, "://") {
			return externalQuotaJSON{}, ErrUnsafeJSONSource
		}
		raw, err = readBoundedFile(source)
		if err != nil {
			return externalQuotaJSON{}, err
		}
	}

	var payload externalQuotaJSON
	if err := json.Unmarshal(raw, &payload); err != nil {
		return externalQuotaJSON{}, fmt.Errorf("invalid json: %w", err)
	}
	if payload.Gemini == nil && payload.ClaudeGPT == nil {
		return externalQuotaJSON{}, fmt.Errorf("json missing gemini and claude_gpt fields")
	}
	return payload, nil
}

func readBoundedFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxJSONBytes))
}

func isLoopbackURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func (s *Service) loadFromDisk() {
	if s.historyPath == "" {
		return
	}
	raw, err := os.ReadFile(s.historyPath)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return
	}
	var st persistedState
	if err := json.Unmarshal(raw, &st); err != nil {
		return
	}
	if st.Source == "" {
		if st.Calibrated {
			st.Source = SourceLocal
		} else {
			st.Source = SourceUncalibrated
		}
	}
	st.Gemini.RemainingPct = clampPct(st.Gemini.RemainingPct)
	st.ClaudeGPT.RemainingPct = clampPct(st.ClaudeGPT.RemainingPct)
	if st.Calibrated {
		if len(st.Gemini.SubModels) == 0 {
			st.Gemini.SubModels = defaultGeminiSubModels(st.Gemini.RemainingPct)
		}
		if len(st.ClaudeGPT.SubModels) == 0 {
			st.ClaudeGPT.SubModels = defaultClaudeSubModels(st.ClaudeGPT.RemainingPct)
		}
	}
	s.state = st
}

func (s *Service) saveLocked() error {
	if s.historyPath == "" {
		return nil
	}
	dir := filepath.Dir(s.historyPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	tmp, err := os.CreateTemp(dir, ".quota-history-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.historyPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func resolveExternalReset(p *externalPoolJSON, now time.Time) time.Time {
	if p == nil {
		return now.Add(defaultWindowDuration)
	}
	if ts := strings.TrimSpace(p.ResetsAt); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			return parsed
		}
	}
	if p.ResetsInSec > 0 {
		return now.Add(time.Duration(p.ResetsInSec) * time.Second)
	}
	return now.Add(defaultWindowDuration)
}

func convertExternalModels(rows []externalModelRow, poolPct float64, isGemini bool) []SubModelQuota {
	if len(rows) == 0 {
		if isGemini {
			return defaultGeminiSubModels(poolPct)
		}
		return defaultClaudeSubModels(poolPct)
	}
	out := make([]SubModelQuota, 0, len(rows))
	for _, r := range rows {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			continue
		}
		out = append(out, SubModelQuota{
			Name:         name,
			RemainingPct: round1(clampPct(r.RemainingPct)),
		})
	}
	if len(out) == 0 {
		if isGemini {
			return defaultGeminiSubModels(poolPct)
		}
		return defaultClaudeSubModels(poolPct)
	}
	return out
}

func defaultGeminiSubModels(poolPct float64) []SubModelQuota {
	neon := round1(clampPct(poolPct))
	flash := round1(clampPct(poolPct + 12))
	if poolPct <= 0 {
		flash = 0
	}
	return []SubModelQuota{
		{Name: metrics.SubModelGeminiNeon, RemainingPct: neon},
		{Name: metrics.SubModelGeminiFlash, RemainingPct: flash},
	}
}

func defaultClaudeSubModels(poolPct float64) []SubModelQuota {
	claude := round1(clampPct(poolPct))
	gpt := round1(clampPct(poolPct + 10))
	if poolPct <= 0 {
		gpt = 0
	}
	return []SubModelQuota{
		{Name: metrics.SubModelClaude45, RemainingPct: claude},
		{Name: metrics.SubModelGPTCodex, RemainingPct: gpt},
	}
}

func adjustSubModels(existing []SubModelQuota, poolPct float64, isGemini bool) []SubModelQuota {
	if len(existing) == 0 {
		if isGemini {
			return defaultGeminiSubModels(poolPct)
		}
		return defaultClaudeSubModels(poolPct)
	}
	if poolPct >= 100 {
		out := make([]SubModelQuota, len(existing))
		for i, m := range existing {
			out[i] = SubModelQuota{Name: m.Name, RemainingPct: 100}
		}
		return out
	}
	out := make([]SubModelQuota, len(existing))
	copy(out, existing)
	out[0].RemainingPct = round1(clampPct(poolPct))
	if len(out) > 1 {
		out[1].RemainingPct = round1(clampPct(maxFloat(poolPct, out[1].RemainingPct-(out[0].RemainingPct-poolPct))))
	}
	return out
}

func nextResetAfter(prevReset, now time.Time) time.Time {
	r := prevReset
	if r.IsZero() {
		return now.Add(defaultWindowDuration)
	}
	for !r.After(now) {
		r = r.Add(defaultWindowDuration)
	}
	return r
}

func validatePct(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
		return ErrInvalidPct
	}
	return nil
}

func clampPct(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
