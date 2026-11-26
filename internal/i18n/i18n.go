package i18n

import (
	"fmt"
	"strings"

	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

const (
	LangZH = "zh"
	LangEN = "en"
)

// Normalize normalizes any language string into "zh" or "en" (defaulting to "zh").
func Normalize(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "en", "en-us", "en_us", "english":
		return LangEN
	default:
		return LangZH
	}
}

// Strings holds all localized UI copy across Desktop, Tray Flyout, and System Tray Menu.
type Strings struct {
	Lang string

	// Common & Agent Topology
	MainAgent      string
	WholeTree      string
	TreeSubtitle   string // fmt: "%d" subagents
	AgentStructure string
	RootOnly       string
	RootAndSubs    string // fmt: "%d" subagents
	SingleAgent    string

	// Session Switcher & Concurrency
	RunningBadge    string // fmt: "%d"
	SwitchParallel  string
	SwitchSession   string
	AutoFollowShort string
	ConvCountFmt    string // fmt: "%d"
	NoLocalSessions string
	NoActiveSession string

	// Main Header & Cost Split
	CostTotal     string
	CostRoot      string
	CostSub       string
	NoRequests    string
	RecentFmt     string // fmt: "#%d · %s"
	RecentNodeFmt string // fmt: "#%d"

	// Context Window & Breakdown
	ContextWindow  string
	PeakWatermark  string
	WaitingData    string
	WaitingSession string
	CtxSegSystem   string
	CtxSegTools    string
	CtxSegToolOut  string
	CtxSegMessages string
	CtxSegSummary  string
	CtxSegCache    string
	CtxSegInput    string
	CtxSegOutput   string

	// KPI Tiles (Tree vs Single-Node)
	KPITreeTurnCost    string
	KPINodeTurnCost    string
	KPITreeSessionCost string
	KPINodeSessionCost string
	KPITreeCache       string
	KPINodeCache       string
	KPITreeTTFT        string
	KPINodeTTFT        string
	KPITreeTPS         string
	KPINodeTPS         string
	SubLatest          string
	SubTotal           string
	SubAvg             string
	SubAvgTPS          string

	// Telemetry Chart & Per-Request List
	MetricTPS    string
	MetricTTFT   string
	MetricCache  string
	MetricCost   string
	ViewCurve    string
	ViewList     string
	PrevRequest  string
	PrevReqTip   string
	NextRequest  string
	NextReqTip   string
	TokIn        string
	TokCache     string
	TokNewInput  string
	TokOut       string
	InspHit      string
	ColReqNode   string
	ColCacheHit  string
	ColDecodeTPS string
	ColCost      string

	// Settings Popover & Theme / Language
	Settings        string
	Done            string
	ThemeSystem     string
	ThemeFollow     string
	ThemeLight      string
	ThemeDark       string
	Appearance      string
	LanguageLabel   string
	LaunchAtLogin   string
	KeepInTray      string
	KeepInTrayShort string
	AlwaysOnTop     string
	ContextWarn80   string
	CheckUpdates    string

	// Dev Harness Controls
	DevLightGlass string
	DevDarkGlass  string
	DevPaused     string
	DevLive       string
	DevBurst      string
	DevBurstTip   string

	// Tray Flyout & System Menu
	TrayTabs           [3]string
	TrayTitleOverview  string
	TrayTitlePerf      string
	TrayTitleAgents    string
	TrayHeroTurnCost   string
	TrayHeroTPS        string
	TrayHeroTTFT       string
	TrayHeroSubCost    string
	TrayAvgShort       string // fmt: "%.0f" or "%.2fs"
	TrayCacheSection   string
	TrayLatestSingle   string
	TraySessionTotal   string
	TrayCostSplit      string
	TrayRecentReqs     string
	TrayAgentDetails   string
	TrayNoNodes        string
	TrayLiveMetrics    string
	TrayCacheHitRate   string
	TrayTTFTLabel      string
	TrayOpenDesktop    string
	TrayHoverHitFmt    string // fmt: seq, hitPct, ttftSec, tps
	TrayTooltipWaiting string
	TrayTooltipFmt     string // fmt: title, ctxPct, turnCost, tps
	MenuToggleFlyout   string
	MenuOpenDesktop    string
	MenuAutoFollow     string
	MenuQuit           string

	// R1 Navigation & Shared Sub-View Actions
	BackToLive string

	// R3 Usage History View
	HistoryTitle           string
	HistoryRangeToday      string
	HistoryRange7D         string
	HistoryRange30D        string
	HistoryRangeAll        string
	HistoryKPITokens       string
	HistorySubOutFmt       string // fmt: compact tokens e.g. "2.9M"
	HistoryKPICost         string
	HistorySubDailyAvgFmt  string // fmt: e.g. "$6.12"
	HistorySubHourlyAvgFmt string // fmt: e.g. "$1.36/h"
	HistoryKPICacheSaved   string
	HistorySubHitFmt       string // fmt: e.g. "94.2%"
	HistoryKPISessions     string
	HistorySubCallsFmt     string // fmt: comma-formatted count e.g. "1,846"
	HistoryMetricToken     string
	HistoryMetricCost      string
	HistoryMetricCache     string
	HistoryChartTokens     string
	HistoryChartCost       string
	HistoryChartCache      string
	HistoryLegendHitPct    string
	HistoryInspTokensFmt   string // fmt: cache, input, output
	HistoryByModel         string
	HistoryTopSessions     string
	HistoryUntitledSession string
	HistoryOtherModel      string
	HistoryEmpty           string
	HistoryEmptyRange      string
}

var zhStrings = Strings{
	Lang: LangZH,

	MainAgent:      "主 Agent",
	WholeTree:      "整棵会话树",
	TreeSubtitle:   "主 + %d 子节点",
	AgentStructure: "父子 Agent 结构",
	RootOnly:       "仅主 Agent",
	RootAndSubs:    "1 主 · %d 子",
	SingleAgent:    "单 Agent",

	RunningBadge:    "%d 进行中",
	SwitchParallel:  "切换并行对话",
	SwitchSession:   "切换对话",
	AutoFollowShort: "自动跟随活跃",
	ConvCountFmt:    "· %d 个对话",
	NoLocalSessions: "暂无本地会话记录",
	NoActiveSession: "无活跃会话",

	CostTotal:     "合计",
	CostRoot:      "主",
	CostSub:       "子",
	NoRequests:    "暂无请求",
	RecentFmt:     "最近 #%d · %s",
	RecentNodeFmt: "最近 #%d",

	ContextWindow:  "上下文窗口",
	PeakWatermark:  "最高水位",
	WaitingData:    "等待本地会话遥测数据…",
	WaitingSession: "等待会话数据…",
	CtxSegSystem:   "系统",
	CtxSegTools:    "工具",
	CtxSegToolOut:  "工具返回",
	CtxSegMessages: "对话",
	CtxSegSummary:  "摘要",
	CtxSegCache:    "缓存",
	CtxSegInput:    "输入",
	CtxSegOutput:   "输出",

	KPITreeTurnCost:    "全树本轮",
	KPINodeTurnCost:    "本轮费用",
	KPITreeSessionCost: "全树会话",
	KPINodeSessionCost: "会话",
	KPITreeCache:       "全树命中",
	KPINodeCache:       "缓存命中",
	KPITreeTTFT:        "全树均 TTFT",
	KPINodeTTFT:        "首字时长",
	KPITreeTPS:         "全树均速",
	KPINodeTPS:         "吐字速度",
	SubLatest:          "最新",
	SubTotal:           "总计",
	SubAvg:             "均值",
	SubAvgTPS:          "均速",

	MetricTPS:    "纯吐字速度 · 逐次请求",
	MetricTTFT:   "首字时长 TTFT · 逐次请求",
	MetricCache:  "缓存命中率 · 逐次请求",
	MetricCost:   "单次费用 · 逐次请求",
	ViewCurve:    "曲线",
	ViewList:     "逐次",
	PrevRequest:  "上一次请求",
	PrevReqTip:   "上一次请求 (←)",
	NextRequest:  "下一次请求",
	NextReqTip:   "下一次请求 (→)",
	TokIn:        "输入",
	TokCache:     "缓存",
	TokNewInput:  "新增输入",
	TokOut:       "输出",
	InspHit:      "命中",
	ColReqNode:   "请求 / 节点",
	ColCacheHit:  "缓存命中",
	ColDecodeTPS: "吐字 TPS",
	ColCost:      "费用",

	Settings:        "设置",
	Done:            "完成",
	ThemeSystem:     "系统",
	ThemeFollow:     "跟随",
	ThemeLight:      "浅色",
	ThemeDark:       "深色",
	Appearance:      "外观",
	LanguageLabel:   "语言",
	LaunchAtLogin:   "开机驻留托盘",
	KeepInTray:      "关闭主窗驻留托盘",
	KeepInTrayShort: "关闭退守托盘",
	AlwaysOnTop:     "主窗始终置顶",
	ContextWarn80:   "上下文 80% 预警",
	CheckUpdates:    "检查更新",

	DevLightGlass: "浅色玻璃",
	DevDarkGlass:  "深色玻璃",
	DevPaused:     "已暂停",
	DevLive:       "实时流",
	DevBurst:      "模拟请求",
	DevBurstTip:   "立即注入一条模拟请求 (快捷键 B)",

	TrayTabs:           [3]string{"总览", "性能", "Agent 树"},
	TrayTitleOverview:  "实时概览",
	TrayTitlePerf:      "速率与缓存",
	TrayTitleAgents:    "父子 Agent 树",
	TrayHeroTurnCost:   "本轮费用",
	TrayHeroTPS:        "吐字速度",
	TrayHeroTTFT:       "首字时长",
	TrayHeroSubCost:    "子 Agent",
	TrayAvgShort:       "均 %s",
	TrayCacheSection:   "缓存命中",
	TrayLatestSingle:   "最新单次",
	TraySessionTotal:   "会话总计",
	TrayCostSplit:      "费用结构",
	TrayRecentReqs:     "最近请求",
	TrayAgentDetails:   "节点明细",
	TrayNoNodes:        "暂无节点",
	TrayLiveMetrics:    "实时指标",
	TrayCacheHitRate:   "缓存命中率",
	TrayTTFTLabel:      "首字时长",
	TrayOpenDesktop:    "打开主面板",
	TrayHoverHitFmt:    "#%d · 命中 %.0f%% · %.2fs · %.0f t/s",
	TrayTooltipWaiting: "Antigravity Monitor — 等待活跃会话",
	TrayTooltipFmt:     "Antigravity Monitor · %s\n上下文 %.1f%% · 本轮 %s · %.0f t/s",
	MenuToggleFlyout:   "打开实时弹层",
	MenuOpenDesktop:    "打开主面板",
	MenuAutoFollow:     "自动跟随活跃对话",
	MenuQuit:           "退出 Antigravity Monitor",

	BackToLive: "← 返回监控",

	HistoryTitle:           "历史消耗统计",
	HistoryRangeToday:      "今日",
	HistoryRange7D:         "7 天",
	HistoryRange30D:        "30 天",
	HistoryRangeAll:        "全部",
	HistoryKPITokens:       "总 Token",
	HistorySubOutFmt:       "输出 %s",
	HistoryKPICost:         "总费用",
	HistorySubDailyAvgFmt:  "日均 %s",
	HistorySubHourlyAvgFmt: "时均 %s",
	HistoryKPICacheSaved:   "缓存节省",
	HistorySubHitFmt:       "均命中 %s",
	HistoryKPISessions:     "会话与请求",
	HistorySubCallsFmt:     "%s 次请求",
	HistoryMetricToken:     "Token",
	HistoryMetricCost:      "费用",
	HistoryMetricCache:     "缓存率",
	HistoryChartTokens:     "Token 消耗分布",
	HistoryChartCost:       "费用走势",
	HistoryChartCache:      "缓存命中率",
	HistoryLegendHitPct:    "命中率 %",
	HistoryInspTokensFmt:   "缓存 %s · 输入 %s · 输出 %s",
	HistoryByModel:         "模型消耗占比",
	HistoryTopSessions:     "高消耗会话 Top 3",
	HistoryUntitledSession: "未命名会话",
	HistoryOtherModel:      "其他模型",
	HistoryEmpty:           "暂无本地历史消耗记录",
	HistoryEmptyRange:      "当前时间范围内暂无消耗记录",
}

var enStrings = Strings{
	Lang: LangEN,

	MainAgent:      "Main Agent",
	WholeTree:      "Session Tree",
	TreeSubtitle:   "Main + %d Sub",
	AgentStructure: "Agent Topology",
	RootOnly:       "Main Only",
	RootAndSubs:    "1 Main · %d Sub",
	SingleAgent:    "Single Agent",

	RunningBadge:    "%d Active",
	SwitchParallel:  "Switch active sessions",
	SwitchSession:   "Switch Session",
	AutoFollowShort: "Auto-follow",
	ConvCountFmt:    "· %d chats",
	NoLocalSessions: "No local sessions yet",
	NoActiveSession: "No active session",

	CostTotal:     "Total",
	CostRoot:      "Main",
	CostSub:       "Sub",
	NoRequests:    "No requests",
	RecentFmt:     "Latest #%d · %s",
	RecentNodeFmt: "Latest #%d",

	ContextWindow:  "Context Window",
	PeakWatermark:  "Peak",
	WaitingData:    "Waiting for local telemetry…",
	WaitingSession: "Waiting for session data…",
	CtxSegSystem:   "System",
	CtxSegTools:    "Tools",
	CtxSegToolOut:  "Tool Out",
	CtxSegMessages: "Chat",
	CtxSegSummary:  "Summary",
	CtxSegCache:    "Cache",
	CtxSegInput:    "Input",
	CtxSegOutput:   "Output",

	KPITreeTurnCost:    "Tree Turn",
	KPINodeTurnCost:    "Turn Cost",
	KPITreeSessionCost: "Tree",
	KPINodeSessionCost: "Session",
	KPITreeCache:       "Tree Cache",
	KPINodeCache:       "Cache Hit",
	KPITreeTTFT:        "Tree TTFT",
	KPINodeTTFT:        "TTFT",
	KPITreeTPS:         "Tree TPS",
	KPINodeTPS:         "Decode TPS",
	SubLatest:          "Last",
	SubTotal:           "Total",
	SubAvg:             "Avg",
	SubAvgTPS:          "Avg",

	MetricTPS:    "Pure Decode TPS · Per Request",
	MetricTTFT:   "Time to First Token · Per Request",
	MetricCache:  "Prompt Cache Hit · Per Request",
	MetricCost:   "Request Cost · Per Request",
	ViewCurve:    "Curve",
	ViewList:     "List",
	PrevRequest:  "Previous request",
	PrevReqTip:   "Previous request (←)",
	NextRequest:  "Next request",
	NextReqTip:   "Next request (→)",
	TokIn:        "In",
	TokCache:     "Cache",
	TokNewInput:  "New In",
	TokOut:       "Out",
	InspHit:      "Hit",
	ColReqNode:   "Request / Node",
	ColCacheHit:  "Cache Hit",
	ColDecodeTPS: "Decode TPS",
	ColCost:      "Cost",

	Settings:        "Settings",
	Done:            "Done",
	ThemeSystem:     "Auto",
	ThemeFollow:     "Auto",
	ThemeLight:      "Light",
	ThemeDark:       "Dark",
	Appearance:      "Theme",
	LanguageLabel:   "Language",
	LaunchAtLogin:   "Launch at login",
	KeepInTray:      "Keep in tray on close",
	KeepInTrayShort: "Keep in tray",
	AlwaysOnTop:     "Always on top",
	ContextWarn80:   "Context 80% alert",
	CheckUpdates:    "Check Updates",

	DevLightGlass: "Light Glass",
	DevDarkGlass:  "Dark Glass",
	DevPaused:     "Paused",
	DevLive:       "Live",
	DevBurst:      "Simulate",
	DevBurstTip:   "Inject a simulated request (Shortcut B)",

	TrayTabs:           [3]string{"Overview", "Perf", "Agents"},
	TrayTitleOverview:  "Live Overview",
	TrayTitlePerf:      "Speed & Cache",
	TrayTitleAgents:    "Agent Tree",
	TrayHeroTurnCost:   "Turn Cost",
	TrayHeroTPS:        "Decode TPS",
	TrayHeroTTFT:       "TTFT",
	TrayHeroSubCost:    "Sub Agent",
	TrayAvgShort:       "Avg %s",
	TrayCacheSection:   "Cache Hit",
	TrayLatestSingle:   "Latest Call",
	TraySessionTotal:   "Session Total",
	TrayCostSplit:      "Cost Split",
	TrayRecentReqs:     "Recent Calls",
	TrayAgentDetails:   "Agents",
	TrayNoNodes:        "No agents",
	TrayLiveMetrics:    "Live Metrics",
	TrayCacheHitRate:   "Cache Hit Rate",
	TrayTTFTLabel:      "TTFT",
	TrayOpenDesktop:    "Open Dashboard",
	TrayHoverHitFmt:    "#%d · Hit %.0f%% · %.2fs · %.0f t/s",
	TrayTooltipWaiting: "Antigravity Monitor — Waiting for active session",
	TrayTooltipFmt:     "Antigravity Monitor · %s\nContext %.1f%% · Turn %s · %.0f t/s",
	MenuToggleFlyout:   "Show Live Flyout",
	MenuOpenDesktop:    "Open Dashboard",
	MenuAutoFollow:     "Auto-Follow Active Session",
	MenuQuit:           "Quit Antigravity Monitor",

	BackToLive: "← Live View",

	HistoryTitle:           "Usage History",
	HistoryRangeToday:      "Today",
	HistoryRange7D:         "7D",
	HistoryRange30D:        "30D",
	HistoryRangeAll:        "All",
	HistoryKPITokens:       "Total Tokens",
	HistorySubOutFmt:       "Out %s",
	HistoryKPICost:         "Total Cost",
	HistorySubDailyAvgFmt:  "Avg %s",
	HistorySubHourlyAvgFmt: "Avg %s",
	HistoryKPICacheSaved:   "Cache Saved",
	HistorySubHitFmt:       "Hit %s",
	HistoryKPISessions:     "Sessions",
	HistorySubCallsFmt:     "%s calls",
	HistoryMetricToken:     "Token",
	HistoryMetricCost:      "Cost",
	HistoryMetricCache:     "Cache %",
	HistoryChartTokens:     "Token Breakdown",
	HistoryChartCost:       "Cost Trend",
	HistoryChartCache:      "Cache Hit Rate",
	HistoryLegendHitPct:    "Hit Rate %",
	HistoryInspTokensFmt:   "Cache %s · In %s · Out %s",
	HistoryByModel:         "Usage by Model",
	HistoryTopSessions:     "Top 3 Sessions",
	HistoryUntitledSession: "Untitled Session",
	HistoryOtherModel:      "Other",
	HistoryEmpty:           "No local usage history yet",
	HistoryEmptyRange:      "No usage recorded in this range",
}

// Get returns the localized string table for lang ("zh" or "en").
func Get(lang string) Strings {
	return *GetPtr(lang)
}

// GetPtr returns a read-only pointer to the localized string table for lang ("zh" or "en"),
// avoiding ~2.5 KB struct-value copies in UI render loops.
func GetPtr(lang string) *Strings {
	if Normalize(lang) == LangEN {
		return &enStrings
	}
	return &zhStrings
}

// AgentName localizes the canonical root agent label ("主 Agent" <-> "Main Agent")
// while leaving custom subagent roles/names untouched.
func (s Strings) AgentName(agentID, rawName string) string {
	if agentID == model.RootAgentID || rawName == "" || rawName == "主 Agent" || rawName == "Main Agent" {
		return s.MainAgent
	}
	return rawName
}

// SegmentLabel returns the short localized label for a context breakdown segment.
func (s Strings) SegmentLabel(key model.ContextSegmentKey) string {
	switch key {
	case model.ContextSegSystem:
		return s.CtxSegSystem
	case model.ContextSegTools:
		return s.CtxSegTools
	case model.ContextSegToolOutput:
		return s.CtxSegToolOut
	case model.ContextSegMessages:
		return s.CtxSegMessages
	case model.ContextSegSummary:
		return s.CtxSegSummary
	case model.ContextSegCache:
		return s.CtxSegCache
	case model.ContextSegInput:
		return s.CtxSegInput
	case model.ContextSegOutput:
		return s.CtxSegOutput
	default:
		return string(key)
	}
}

// SubagentSummary returns "单 Agent" / "1 主 · N 子" (or English equivalent).
func (s Strings) SubagentSummary(subCount int) string {
	if subCount <= 0 {
		return s.SingleAgent
	}
	return fmt.Sprintf(s.RootAndSubs, subCount)
}
