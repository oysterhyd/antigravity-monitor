package quota

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	lowQuotaThresholdPct = 25
	maxStoredTrendPoints = 2400
	trendRetention       = 7 * 24 * time.Hour
	trendSampleInterval  = 5 * time.Minute
	historyVersion       = 2
)

type WindowQuota struct {
	RemainingPct float64   `json:"remaining_pct"`
	ResetAt      time.Time `json:"reset_at,omitempty"`
}

type PoolQuota struct {
	FiveHour WindowQuota `json:"five_hour"`
	Weekly   WindowQuota `json:"weekly"`
}

type TrendPoint struct {
	Timestamp  time.Time `json:"ts"`
	GeminiPct  float64   `json:"gemini_pct"`
	ClaudePct  float64   `json:"claude_pct"`
	ResetEvent bool      `json:"reset_event,omitempty"`
}

type Reading struct {
	Gemini    PoolQuota `json:"gemini"`
	ClaudeGPT PoolQuota `json:"claude_gpt"`
}

type Trend struct {
	Last24h []TrendPoint
	Last7d  []TrendPoint
}

type QuotaSnapshot struct {
	Reading
	UpdatedAt     time.Time
	SourceError   error
	HasLowQuota   bool
	FiveHourTrend Trend
	WeeklyTrend   Trend
}

type Tracker interface {
	State(now time.Time) QuotaSnapshot
	HasLowQuota() bool
	Refresh(ctx context.Context, now time.Time) error
}

type persistedState struct {
	Version int `json:"version"`
	Reading
	UpdatedAt time.Time    `json:"updated_at"`
	FiveHour  []TrendPoint `json:"five_hour_points,omitempty"`
	Weekly    []TrendPoint `json:"weekly_points,omitempty"`
}

func (state persistedState) hasLowQuota() bool {
	return !state.UpdatedAt.IsZero() &&
		min(state.Gemini.FiveHour.RemainingPct, state.Gemini.Weekly.RemainingPct,
			state.ClaudeGPT.FiveHour.RemainingPct, state.ClaudeGPT.Weekly.RemainingPct) < lowQuotaThresholdPct
}

type Service struct {
	mu          sync.RWMutex
	refreshMu   sync.Mutex
	historyPath string
	read        func(context.Context) (Reading, error)
	state       persistedState
	sourceError error
}

func NewService(historyPath string, read func(context.Context) (Reading, error)) *Service {
	service := &Service{historyPath: historyPath, read: read}
	raw, err := os.ReadFile(historyPath)
	if err == nil {
		var state persistedState
		if json.Unmarshal(raw, &state) == nil && state.Version == historyVersion {
			service.state = state
		}
	}
	return service
}

func (s *Service) State(now time.Time) QuotaSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return QuotaSnapshot{
		Reading:       s.state.Reading,
		UpdatedAt:     s.state.UpdatedAt,
		SourceError:   s.sourceError,
		HasLowQuota:   s.state.hasLowQuota(),
		FiveHourTrend: summarizeTrend(s.state.FiveHour, now),
		WeeklyTrend:   summarizeTrend(s.state.Weekly, now),
	}
}

func (s *Service) HasLowQuota() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.hasLowQuota()
}

func (s *Service) Refresh(ctx context.Context, now time.Time) error {
	if s.read == nil {
		return nil
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	reading, err := s.read(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		s.sourceError = err
		return err
	}
	s.sourceError = nil
	if now.Before(s.state.UpdatedAt) {
		return nil
	}

	fiveHourPoint := TrendPoint{
		Timestamp: now, GeminiPct: reading.Gemini.FiveHour.RemainingPct, ClaudePct: reading.ClaudeGPT.FiveHour.RemainingPct,
		ResetEvent: observedReset(s.state.Gemini.FiveHour, reading.Gemini.FiveHour, now) ||
			observedReset(s.state.ClaudeGPT.FiveHour, reading.ClaudeGPT.FiveHour, now),
	}
	weeklyPoint := TrendPoint{
		Timestamp: now, GeminiPct: reading.Gemini.Weekly.RemainingPct, ClaudePct: reading.ClaudeGPT.Weekly.RemainingPct,
		ResetEvent: observedReset(s.state.Gemini.Weekly, reading.Gemini.Weekly, now) ||
			observedReset(s.state.ClaudeGPT.Weekly, reading.ClaudeGPT.Weekly, now),
	}
	s.state = persistedState{
		Version: historyVersion, Reading: reading, UpdatedAt: now,
		FiveHour: appendPoint(s.state.FiveHour, fiveHourPoint),
		Weekly:   appendPoint(s.state.Weekly, weeklyPoint),
	}
	return s.saveLocked()
}

func observedReset(previous, next WindowQuota, now time.Time) bool {
	return !previous.ResetAt.IsZero() && !now.Before(previous.ResetAt) &&
		next.RemainingPct > previous.RemainingPct && !next.ResetAt.Equal(previous.ResetAt)
}

func appendPoint(points []TrendPoint, point TrendPoint) []TrendPoint {
	if len(points) > 0 {
		last := &points[len(points)-1]
		if last.Timestamp.Truncate(trendSampleInterval).Equal(point.Timestamp.Truncate(trendSampleInterval)) {
			point.ResetEvent = point.ResetEvent || last.ResetEvent
			*last = point
			return points
		}
	}
	points = append(points, point)
	cutoff := point.Timestamp.Add(-trendRetention)
	first := max(0, len(points)-maxStoredTrendPoints)
	for first < len(points) && points[first].Timestamp.Before(cutoff) {
		first++
	}
	return points[first:]
}

func summarizeTrend(points []TrendPoint, now time.Time) Trend {
	trend := Trend{Last7d: dailyLowest(points, now)}
	cutoff := now.Add(-24 * time.Hour)
	for _, point := range points {
		if !point.Timestamp.Before(cutoff) && !point.Timestamp.After(now) {
			trend.Last24h = append(trend.Last24h, point)
		}
	}
	return trend
}

func dailyLowest(points []TrendPoint, now time.Time) []TrendPoint {
	oldest := now.AddDate(0, 0, -6)
	cutoff := time.Date(oldest.Year(), oldest.Month(), oldest.Day(), 0, 0, 0, 0, now.Location())
	var days []TrendPoint
	for _, point := range points {
		if point.Timestamp.Before(cutoff) || point.Timestamp.After(now) {
			continue
		}
		local := point.Timestamp.In(now.Location())
		day := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, now.Location())
		if len(days) == 0 || !days[len(days)-1].Timestamp.Equal(day) {
			point.Timestamp = day
			days = append(days, point)
			continue
		}
		lowest := &days[len(days)-1]
		lowest.GeminiPct = min(lowest.GeminiPct, point.GeminiPct)
		lowest.ClaudePct = min(lowest.ClaudePct, point.ClaudePct)
		lowest.ResetEvent = lowest.ResetEvent || point.ResetEvent
	}
	return days
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
	file, err := os.CreateTemp(dir, ".quota-history-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(append(payload, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), s.historyPath)
}
