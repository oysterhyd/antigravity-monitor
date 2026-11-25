package history

import (
	"context"
	"time"

	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
)

// Range selects the historical aggregation window for Query.
type Range string

const (
	RangeToday Range = "today"
	Range7D    Range = "7d"
	Range30D   Range = "30d"
	RangeAll   Range = "all"
)

// QueryResult holds aggregated usage KPIs, chart buckets, model breakdown, and top sessions
// for a queried range. All percentage fields (CacheHitRate, BucketSummary.CacheHitPct,
// ModelUsage.Share) are expressed on the 0..100 scale.
type QueryResult struct {
	Range           Range
	TotalTokens     int64
	OutputTokens    int64
	InputTokens     int64 // Includes CacheWriteTokens so TotalTokens == CacheReadTokens + InputTokens + OutputTokens.
	CacheReadTokens int64
	TotalCostUSD    float64
	AvgCostUSD      float64 // Daily avg for 7d/30d/all, hourly avg for today.
	CacheSavedUSD   float64
	CacheHitRate    float64 // 0..100 percentage.
	SessionCount    int
	RequestCount    int
	Buckets         []BucketSummary
	ByModel         []ModelUsage
	TopSessions     []SessionSummary // Up to 3, sorted by CostUSD desc then TotalTokens desc.
}

// BucketSummary represents one stacked bar interval [Start, End) in QueryResult.Buckets.
type BucketSummary struct {
	Label            string
	Start            time.Time
	End              time.Time
	CacheReadTokens  int64
	InputTokens      int64 // Includes CacheWriteTokens so TotalTokens == CacheReadTokens + InputTokens + OutputTokens.
	OutputTokens     int64
	TotalTokens      int64
	GeminiCostUSD    float64
	ClaudeGPTCostUSD float64
	TotalCostUSD     float64
	CacheHitPct      float64 // 0..100 percentage.
}

// ModelUsage summarizes usage for a single canonical model family.
type ModelUsage struct {
	Name    string
	Pool    metrics.ModelPool
	CostUSD float64
	Tokens  int64
	Share   float64 // 0..100 percentage.
}

// SessionSummary summarizes a resolved root conversation session within the queried range.
type SessionSummary struct {
	ConvID      string
	Title       string
	PrimaryPool metrics.ModelPool
	TotalTokens int64
	CostUSD     float64
	UpdatedAt   time.Time
}

// PoolConsumption summarizes token and USD consumption across the two quota pools since a timestamp.
type PoolConsumption struct {
	GeminiTokens  int64
	GeminiCostUSD float64
	ClaudeTokens  int64
	ClaudeCostUSD float64
}

// RefreshStats reports incremental cache telemetry from the most recent Refresh call.
type RefreshStats struct {
	Hits   int
	Misses int
	Pruned int
}

// Aggregator defines the read-only usage history aggregation contract consumed by Quota and UI.
type Aggregator interface {
	Refresh(ctx context.Context) error
	Query(r Range, now time.Time) QueryResult
	ConsumptionSince(since time.Time) PoolConsumption
}
