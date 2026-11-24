package model

import (
	"math"
	"testing"
	"time"
)

func TestComputeDecodeTPS(t *testing.T) {
	// 1200 output tokens, total 11.5s, TTFT 1.5s => decode 10s => 120 t/s
	tps := ComputeDecodeTPS(1200, 11500*time.Millisecond, 1500*time.Millisecond)
	if math.Abs(tps-120.0) > 1e-6 {
		t.Fatalf("expected 120.0, got %f", tps)
	}

	// Guard against near-zero decode duration (clamps to 50ms)
	tpsFast := ComputeDecodeTPS(100, 1000*time.Millisecond, 990*time.Millisecond)
	if math.Abs(tpsFast-2000.0) > 1e-6 {
		t.Fatalf("expected 2000.0 (clamped 50ms), got %f", tpsFast)
	}
}

func TestComputeCacheHitRate(t *testing.T) {
	rate := ComputeCacheHitRate(2000, 18000, 0)
	if math.Abs(rate-0.9) > 1e-6 {
		t.Fatalf("expected 0.9, got %f", rate)
	}
	if ComputeCacheHitRate(0, 0, 0) != 0 {
		t.Fatalf("expected 0 for empty denominator")
	}
}

func TestAggregateScopeAndPeakContext(t *testing.T) {
	reqs := []RequestRecord{
		{
			Seq: 1, AgentID: RootAgentID, AgentName: "主 Agent", InCurrentTurn: false,
			InputTokens: 10000, CacheReadTokens: 0, OutputTokens: 1000,
			TTFT: 2 * time.Second, TotalDuration: 12 * time.Second,
			DecodeTPS: 100, CostUSD: 0.020,
		},
		{
			Seq: 2, AgentID: "sub-1", AgentName: "browser", InCurrentTurn: true,
			InputTokens: 2000, CacheReadTokens: 18000, OutputTokens: 1000,
			TTFT: 1 * time.Second, TotalDuration: 6 * time.Second,
			DecodeTPS: 200, CostUSD: 0.010,
		},
	}

	m := AggregateScope(reqs)
	if m.RequestCount != 2 || m.TurnRequestCount != 1 {
		t.Fatalf("unexpected counts: %+v", m)
	}
	if math.Abs(m.SessionCostUSD-0.030) > 1e-9 || math.Abs(m.TurnCostUSD-0.010) > 1e-9 {
		t.Fatalf("unexpected cost: session=%f turn=%f", m.SessionCostUSD, m.TurnCostUSD)
	}
	// Weighted cache hit: 18000 / 30000 = 0.60
	if math.Abs(m.WeightedCacheHitRate-0.60) > 1e-6 {
		t.Fatalf("expected weighted cache hit 0.60, got %f", m.WeightedCacheHitRate)
	}
	// Weighted decode TPS: (1000+1000) / (10s + 5s) = 133.333...
	if math.Abs(m.WeightedDecodeTPS-(2000.0/15.0)) > 1e-4 {
		t.Fatalf("expected weighted TPS 133.33, got %f", m.WeightedDecodeTPS)
	}
	if m.LatestRequest == nil || m.LatestRequest.AgentName != "browser" {
		t.Fatalf("expected latest request from browser, got %+v", m.LatestRequest)
	}

	root := AgentNode{ID: RootAgentID, Name: "主 Agent", ContextUsed: 15000, ContextLimit: 512000, ContextRatio: 15000.0 / 512000.0}
	sub := AgentNode{ID: "sub-1", Name: "browser", ContextUsed: 24000, ContextLimit: 200000, ContextRatio: 24000.0 / 200000.0}
	peak := ComputePeakContext(root, []AgentNode{sub})
	if peak.AgentID != "sub-1" || peak.AgentName != "browser" {
		t.Fatalf("expected peak context on browser, got %+v", peak)
	}
}
