package model

import "time"

// RootAgentID is the canonical AgentNode.ID for the root/parent agent of a conversation.
const RootAgentID = "root"

// RequestRecord represents a single LLM generation call from gen_metadata (or transcript fallback).
// This struct is FROZEN during parallel track development.
type RequestRecord struct {
	Seq              int           `json:"seq"`
	GenIndex         int           `json:"gen_index"`
	StepIndex        int           `json:"step_index"`
	TurnIndex        int           `json:"turn_index"`
	InCurrentTurn    bool          `json:"in_current_turn"`
	ConversationID   string        `json:"conversation_id"`
	AgentID          string        `json:"agent_id"`
	AgentName        string        `json:"agent_name"`
	ModelSlug        string        `json:"model_slug"`
	ModelDisplay     string        `json:"model_display"`
	Timestamp        time.Time     `json:"timestamp"`
	InputTokens      int64         `json:"input_tokens"`
	CacheReadTokens  int64         `json:"cache_read_tokens"`
	CacheWriteTokens int64         `json:"cache_write_tokens"`
	OutputTokens     int64         `json:"output_tokens"`
	ThinkingTokens   int64         `json:"thinking_tokens"`
	ResponseTokens   int64         `json:"response_tokens"`
	ContextUsed      int64         `json:"context_used"`
	ContextLimit     int64         `json:"context_limit"`
	TTFT             time.Duration `json:"ttft"`
	TotalDuration    time.Duration `json:"total_duration"`
	// DecodeTPS is strictly Pure Decode TPS: OutputTokens / max((TotalDuration - TTFT).Seconds(), 0.05).
	DecodeTPS     float64 `json:"decode_tps"`
	CacheHitRate  float64 `json:"cache_hit_rate"` // [0.0, 1.0]
	CostUSD       float64 `json:"cost_usd"`
	CacheSavedUSD float64 `json:"cache_saved_usd"`
}

// ScopeMetrics holds aggregated telemetry for either a whole conversation tree or a single AgentNode.
type ScopeMetrics struct {
	RequestCount         int            `json:"request_count"`
	TurnRequestCount     int            `json:"turn_request_count"`
	TurnCostUSD          float64        `json:"turn_cost_usd"`
	SessionCostUSD       float64        `json:"session_cost_usd"`
	CacheSavedUSD        float64        `json:"cache_saved_usd"`
	TotalTokens          int64          `json:"total_tokens"`
	TotalOutputTokens    int64          `json:"total_output_tokens"`
	WeightedCacheHitRate float64        `json:"weighted_cache_hit_rate"` // [0.0, 1.0]
	AvgTTFT              time.Duration  `json:"avg_ttft"`
	WeightedDecodeTPS    float64        `json:"weighted_decode_tps"`
	LatestRequest        *RequestRecord `json:"latest_request,omitempty"`
}

// AgentNode represents either the root agent or a dispatched subagent within a conversation tree.
type AgentNode struct {
	ID            string       `json:"id"`
	ParentID      string       `json:"parent_id,omitempty"`
	Name          string       `json:"name"`
	Role          string       `json:"role,omitempty"`
	ModelDisplay  string       `json:"model_display"`
	Active        bool         `json:"active"`
	ContextUsed   int64        `json:"context_used"`
	ContextLimit  int64        `json:"context_limit"`
	ContextCache  int64        `json:"context_cache"`
	ContextInput  int64        `json:"context_input"`
	ContextOutput int64        `json:"context_output"`
	ContextRatio  float64      `json:"context_ratio"` // [0.0, 1.0]
	Metrics       ScopeMetrics `json:"metrics"`
}

// PeakContextInfo highlights the agent with the highest context utilization in a conversation tree.
type PeakContextInfo struct {
	AgentID   string  `json:"agent_id"`
	AgentName string  `json:"agent_name"`
	Used      int64   `json:"used"`
	Limit     int64   `json:"limit"`
	Ratio     float64 `json:"ratio"` // [0.0, 1.0]
}

// ConversationSnapshot represents a complete top-level conversation and its subagent hierarchy.
type ConversationSnapshot struct {
	ID               string          `json:"id"`
	Title            string          `json:"title"`
	Workspace        string          `json:"workspace"`
	Running          bool            `json:"running"`
	LastUpdated      time.Time       `json:"last_updated"`
	ActiveAgentID    string          `json:"active_agent_id"`
	Root             AgentNode       `json:"root"`
	Subagents        []AgentNode     `json:"subagents,omitempty"`
	TreeMetrics      ScopeMetrics    `json:"tree_metrics"`
	SubagentsCostUSD float64         `json:"subagents_cost_usd"`
	PeakContext      PeakContextInfo `json:"peak_context"`
	Requests         []RequestRecord `json:"requests,omitempty"`
}

// AllAgents returns [Root, Subagents...] as a convenience slice for UI rendering.
func (c ConversationSnapshot) AllAgents() []AgentNode {
	out := make([]AgentNode, 0, 1+len(c.Subagents))
	out = append(out, c.Root)
	out = append(out, c.Subagents...)
	return out
}

// FindAgent looks up Root or any Subagent by ID.
func (c ConversationSnapshot) FindAgent(id string) (AgentNode, bool) {
	if id == "" || id == RootAgentID || id == c.Root.ID {
		return c.Root, true
	}
	for _, s := range c.Subagents {
		if s.ID == id {
			return s, true
		}
	}
	return AgentNode{}, false
}

// AppSnapshot is the immutable top-level state delivered from the data engine to UI views.
type AppSnapshot struct {
	GeneratedAt          time.Time              `json:"generated_at"`
	Conversations        []ConversationSnapshot `json:"conversations"`
	ActiveConversationID string                 `json:"active_conversation_id"`
	RunningCount         int                    `json:"running_count"`
}

// FindConversation locates a conversation snapshot by ID, falling back to ActiveConversationID or index 0.
func (a AppSnapshot) FindConversation(id string) (ConversationSnapshot, bool) {
	if len(a.Conversations) == 0 {
		return ConversationSnapshot{}, false
	}
	for _, c := range a.Conversations {
		if c.ID == id {
			return c, true
		}
	}
	if a.ActiveConversationID != "" {
		for _, c := range a.Conversations {
			if c.ID == a.ActiveConversationID {
				return c, true
			}
		}
	}
	return a.Conversations[0], true
}

// Settings holds persisted user preferences. Note: Pure Decode TPS is fixed (no TPS mode switch).
type Settings struct {
	LaunchAtLogin        bool    `json:"launch_at_login"`
	KeepInTrayOnClose    bool    `json:"keep_in_tray_on_close"`
	AlwaysOnTop          bool    `json:"always_on_top"`
	ContextWarnThreshold float64 `json:"context_warn_threshold"`
	AutoFollowActive     bool    `json:"auto_follow_active"`
	ThemeMode            string  `json:"theme_mode"` // "system" | "light" | "dark"
	PricingOverridePath  string  `json:"pricing_override_path,omitempty"`
}

// DefaultSettings returns the clean Apple-default user preferences.
func DefaultSettings() Settings {
	return Settings{
		LaunchAtLogin:        false,
		KeepInTrayOnClose:    true,
		AlwaysOnTop:          false,
		ContextWarnThreshold: 0.80,
		AutoFollowActive:     true,
		ThemeMode:            "system",
	}
}

// PricingRule defines per-million-token pricing and context ceiling for a model pattern.
type PricingRule struct {
	ModelPattern            string  `json:"model_pattern"`
	DisplayName             string  `json:"display_name"`
	ContextLimit            int64   `json:"context_limit"`
	InputPerMillion         float64 `json:"input_per_million"`
	CacheReadPerMillion     float64 `json:"cache_read_per_million"`
	CacheWritePerMillion    float64 `json:"cache_write_per_million"`
	OutputPerMillion        float64 `json:"output_per_million"`
	LongContextThreshold    int64   `json:"long_context_threshold,omitempty"`
	LongInputPerMillion     float64 `json:"long_input_per_million,omitempty"`
	LongCacheReadPerMillion float64 `json:"long_cache_read_per_million,omitempty"`
	LongOutputPerMillion    float64 `json:"long_output_per_million,omitempty"`
}

// PricingConfig is the root schema for pricing.default.json and user override files.
type PricingConfig struct {
	Version     int           `json:"version"`
	DefaultRule PricingRule   `json:"default_rule"`
	Rules       []PricingRule `json:"rules"`
}
