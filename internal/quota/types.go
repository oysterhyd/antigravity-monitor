package quota

import (
	"errors"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/history"
)

// SourceKind identifies how the active quota snapshot was produced.
type SourceKind string

const (
	SourceUncalibrated SourceKind = "uncalibrated"
	SourceLocal        SourceKind = "local"
	SourceJSON         SourceKind = "json"
)

var (
	ErrInvalidPct       = errors.New("quota percentage must be a finite number in [0, 100]")
	ErrInvalidReset     = errors.New("quota reset duration must be non-negative")
	ErrUnsafeJSONSource = errors.New("only local file paths, file:// URIs, or loopback HTTP URLs are allowed")
)

// CalibrationInput captures user-entered local quota baselines.
type CalibrationInput struct {
	GeminiRemainingPct float64
	GeminiResetIn      time.Duration
	ClaudeRemainingPct float64
	ClaudeResetIn      time.Duration
	JSONSource         string
}

// SubModelQuota represents one sub-model progress bar inside a quota pool card.
type SubModelQuota struct {
	Name         string  `json:"name"`
	RemainingPct float64 `json:"remaining_pct"`
}

// PoolQuota holds the remaining percentage, reset deadline, and sub-model breakdown for a model pool.
type PoolQuota struct {
	RemainingPct float64         `json:"remaining_pct"`
	ResetAt      time.Time       `json:"reset_at,omitempty"`
	SubModels    []SubModelQuota `json:"sub_models,omitempty"`
}

// TrendPoint represents one recorded quota sampling point for 24h/7d trend charts.
type TrendPoint struct {
	Timestamp  time.Time `json:"ts"`
	GeminiPct  float64   `json:"gemini_pct"`
	ClaudePct  float64   `json:"claude_pct"`
	ResetEvent bool      `json:"reset_event,omitempty"`
}

// QuotaSnapshot is the immutable view-model snapshot rendered by the Account Quota view.
type QuotaSnapshot struct {
	Calibrated   bool
	Source       SourceKind
	SourcePath   string
	SourceError  string
	AccountLabel string
	PlanLabel    string
	UpdatedAt    time.Time
	Gemini       PoolQuota
	ClaudeGPT    PoolQuota
	HasLowQuota  bool
	Trend24h     []TrendPoint
	Trend7d      []TrendPoint
}

// Tracker defines the compliant local quota tracking interface consumed by the desktop UI.
type Tracker interface {
	State(now time.Time) QuotaSnapshot
	Calibrate(in CalibrationInput, now time.Time) (QuotaSnapshot, error)
	ClearCalibration(now time.Time) (QuotaSnapshot, error)
	SetJSONSource(source string, now time.Time) (QuotaSnapshot, error)
	SampleNow(now time.Time, burn history.PoolConsumption) QuotaSnapshot
	Refresh(now time.Time, burn history.PoolConsumption) (QuotaSnapshot, error)
	LastSampledAt() time.Time
}
