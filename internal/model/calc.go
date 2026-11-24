package model

import "time"

const minDecodeSeconds = 0.05

// ComputeDecodeTPS computes Pure Decode TPS: OutputTokens / max((TotalDuration - TTFT), 50ms).
func ComputeDecodeTPS(outputTokens int64, totalDuration, ttft time.Duration) float64 {
	if outputTokens <= 0 {
		return 0
	}
	decodeSec := (totalDuration - ttft).Seconds()
	if decodeSec < minDecodeSeconds {
		decodeSec = minDecodeSeconds
	}
	return float64(outputTokens) / decodeSec
}

// ComputeCacheHitRate computes CacheRead / (Input + CacheRead + CacheWrite), clamped to [0, 1].
func ComputeCacheHitRate(input, cacheRead, cacheWrite int64) float64 {
	denom := input + cacheRead + cacheWrite
	if denom <= 0 {
		return 0
	}
	r := float64(cacheRead) / float64(denom)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

// AggregateScope computes weighted ScopeMetrics across a chronological slice of requests.
func AggregateScope(reqs []RequestRecord) ScopeMetrics {
	if len(reqs) == 0 {
		return ScopeMetrics{}
	}
	var (
		m              ScopeMetrics
		sumIn          int64
		sumCR          int64
		sumCW          int64
		sumTTFT        time.Duration
		sumDecodeSec   float64
		validTTFTCount int
	)

	m.RequestCount = len(reqs)
	for i := range reqs {
		r := reqs[i]
		sumIn += r.InputTokens
		sumCR += r.CacheReadTokens
		sumCW += r.CacheWriteTokens
		m.TotalTokens += r.InputTokens + r.CacheReadTokens + r.CacheWriteTokens + r.OutputTokens
		m.TotalOutputTokens += r.OutputTokens
		m.SessionCostUSD += r.CostUSD
		m.CacheSavedUSD += r.CacheSavedUSD
		if r.InCurrentTurn {
			m.TurnRequestCount++
			m.TurnCostUSD += r.CostUSD
		}
		if r.TTFT > 0 {
			sumTTFT += r.TTFT
			validTTFTCount++
		}
		if r.OutputTokens > 0 {
			if r.DecodeTPS > 0 {
				sumDecodeSec += float64(r.OutputTokens) / r.DecodeTPS
			} else if d := (r.TotalDuration - r.TTFT).Seconds(); d >= minDecodeSeconds {
				sumDecodeSec += d
			}
		}
	}

	m.WeightedCacheHitRate = ComputeCacheHitRate(sumIn, sumCR, sumCW)
	if validTTFTCount > 0 {
		m.AvgTTFT = sumTTFT / time.Duration(validTTFTCount)
	}
	if sumDecodeSec > 0 && m.TotalOutputTokens > 0 {
		m.WeightedDecodeTPS = float64(m.TotalOutputTokens) / sumDecodeSec
	}
	latest := reqs[len(reqs)-1]
	m.LatestRequest = &latest
	return m
}

// ComputePeakContext finds the agent with the highest ContextRatio in [root, subagents...].
func ComputePeakContext(root AgentNode, subagents []AgentNode) PeakContextInfo {
	peak := PeakContextInfo{
		AgentID:   root.ID,
		AgentName: root.Name,
		Used:      root.ContextUsed,
		Limit:     root.ContextLimit,
		Ratio:     root.ContextRatio,
	}
	for _, s := range subagents {
		if s.ContextRatio > peak.Ratio {
			peak = PeakContextInfo{
				AgentID:   s.ID,
				AgentName: s.Name,
				Used:      s.ContextUsed,
				Limit:     s.ContextLimit,
				Ratio:     s.ContextRatio,
			}
		}
	}
	return peak
}
