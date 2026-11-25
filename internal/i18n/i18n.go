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
	CombinedStats  string
	TreeSubtitle   string // fmt: "%d" subagents
	AgentStructure string
	RootOnly       string
	RootAndSubs    string // fmt: "%d" subagents
	SingleAgent    string

	// Session Switcher & Concurrency
	RunningBadge    string // fmt: "%d"
	SwitchParallel  string
	SwitchSession   string
	AutoFollowFull  string
	PinnedSession   string
	AutoFollowShort string
	ConvCountFmt    string // fmt: "%d"
	NoLocalSessions string
	NoActiveSession string

	// Main Header & Cost Split
	CostTotal  string
	CostRoot   string
	CostSub    string
	NoRequests string
	RecentFmt  string // fmt: "#%d · %s"

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
	TrayTotalShort     string // fmt: "%s"
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

	// R2 Account Quota View & Calibration
	QuotaTitle               string
	QuotaSummaryPrefix       string
	QuotaRefresh             string
	QuotaCalibrate           string
	QuotaRemaining           string
	QuotaResetInFmt          string // fmt: "3h 42m"
	QuotaResetSoon           string
	QuotaSourceLocal         string
	QuotaSourceJSON          string
	QuotaSourceUncalibrated  string
	QuotaUncalibratedShort   string
	QuotaUncalibratedTitle   string
	QuotaUncalibratedDesc    string
	QuotaCalibrateNow        string
	QuotaTrendTitle          string
	QuotaFilterAll           string
	QuotaResetMarker         string
	QuotaInspNowSuffix       string
	QuotaInspDeltaFmt        string // fmt: geminiDelta, claudeDelta
	QuotaInsp7dNote          string
	QuotaTrendEmpty          string
	QuotaTrendSinglePoint    string
	QuotaCalibTitle          string
	QuotaCalibPrivacyNote    string
	QuotaCalibRemPct         string
	QuotaCalibResetLabel     string
	QuotaCalibHoursUnit      string
	QuotaCalibMinutesUnit    string
	QuotaCalibJSONLabel      string
	QuotaCalibJSONHint       string
	QuotaCalibSave           string
	QuotaCalibClear          string
	QuotaCalibCancel         string
	QuotaErrInvalidPct       string
	QuotaErrInvalidReset     string
	QuotaErrUnsafeJSONSource string
	QuotaErrJSONFallbackFmt  string // fmt: short error reason

	// R3 Usage History View
	HistoryTitle           string
	HistoryScopeToday      string
	HistoryScope7D         string
	HistoryScope30D        string
	HistoryScopeAll        string
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
	HistoryClickToInspect  string
	HistoryUntitledSession string
	HistoryOtherModel      string
	HistoryEmpty           string
	HistoryEmptyRange      string
}

var zhStrings = Strings{
	Lang: LangZH,

	MainAgent:      "主 Agent",
	WholeTree:      "整棵会话树",
	CombinedStats:  "合并统计",
	TreeSubtitle:   "主 + %d 子节点",
	AgentStructure: "父子 Agent 结构",
	RootOnly:       "仅主 Agent",
	RootAndSubs:    "1 主 · %d 子",
	SingleAgent:    "单 Agent",

	RunningBadge:    "%d 进行中",
	SwitchParallel:  "切换并行对话",
	SwitchSession:   "切换对话",
	AutoFollowFull:  "自动跟随活跃对话",
	PinnedSession:   "已锁定当前对话",
	AutoFollowShort: "自动跟随活跃",
	ConvCountFmt:    "· %d 个对话",
	NoLocalSessions: "暂无本地会话记录",
	NoActiveSession: "无活跃会话",

	CostTotal:  "合计",
	CostRoot:   "主",
	CostSub:    "子",
	NoRequests: "暂无请求",
	RecentFmt:  "最近 #%d · %s",

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
	TrayHeroTTFT:       "首字 TTFT",
	TrayHeroSubCost:    "子 Agent 合计",
	TrayAvgShort:       "均 %s",
	TrayTotalShort:     "总 %s",
	TrayCacheSection:   "缓存命中（单次 vs 总计）",
	TrayLatestSingle:   "最新单次",
	TraySessionTotal:   "会话总计",
	TrayCostSplit:      "费用结构（主 vs 子）",
	TrayRecentReqs:     "最近请求（命中 · TTFT · TPS）",
	TrayAgentDetails:   "节点明细（点击联动主窗）",
	TrayNoNodes:        "暂无节点",
	TrayLiveMetrics:    "实时指标（单次 / 总计）",
	TrayCacheHitRate:   "缓存命中率",
	TrayTTFTLabel:      "首字时长 TTFT",
	TrayOpenDesktop:    "打开主面板",
	TrayHoverHitFmt:    "#%d · 命中 %.0f%% · %.2fs · %.0f t/s",
	TrayTooltipWaiting: "Antigravity Monitor — 等待活跃会话",
	TrayTooltipFmt:     "Antigravity Monitor · %s\n上下文 %.1f%% · 本轮 %s · %.0f t/s",
	MenuToggleFlyout:   "打开实时弹层",
	MenuOpenDesktop:    "打开主面板",
	MenuAutoFollow:     "自动跟随活跃对话",
	MenuQuit:           "退出 Antigravity Monitor",

	BackToLive: "← 返回监控",

	QuotaTitle:               "账号实时额度",
	QuotaSummaryPrefix:       "当前窗口状态",
	QuotaRefresh:             "↻ 刷新采样",
	QuotaCalibrate:           "校准",
	QuotaRemaining:           "剩余额度",
	QuotaResetInFmt:          "%s 后重置",
	QuotaResetSoon:           "即将重置",
	QuotaSourceLocal:         "本地校准窗口",
	QuotaSourceJSON:          "本地 JSON 源",
	QuotaSourceUncalibrated:  "未校准 · 仅本地估算",
	QuotaUncalibratedShort:   "未校准",
	QuotaUncalibratedTitle:   "尚未校准额度基线",
	QuotaUncalibratedDesc:    "点击「立即校准」输入当前剩余 % 与重置倒计时，或配置本地 JSON 源以追踪本地窗口消耗。",
	QuotaCalibrateNow:        "立即校准",
	QuotaTrendTitle:          "额度变化趋势",
	QuotaFilterAll:           "全部",
	QuotaResetMarker:         "↺ 重置",
	QuotaInspNowSuffix:       " (当前)",
	QuotaInspDeltaFmt:        "较上采样点: Gemini %s · Claude & GPT %s",
	QuotaInsp7dNote:          "每日窗口最低剩余水位快照",
	QuotaTrendEmpty:          "暂无趋势采样，完成校准或点击「刷新采样」后生成曲线",
	QuotaTrendSinglePoint:    "已记录首个采样点，继续采样后将绘制平滑趋势曲线",
	QuotaCalibTitle:          "校准本地额度基线",
	QuotaCalibPrivacyNote:    "纯本地窗口追踪，不探测未公开服务或读取 OAuth 凭据",
	QuotaCalibRemPct:         "剩余 %",
	QuotaCalibResetLabel:     "重置倒计时",
	QuotaCalibHoursUnit:      "时",
	QuotaCalibMinutesUnit:    "分",
	QuotaCalibJSONLabel:      "本地 JSON 源 (可选)",
	QuotaCalibJSONHint:       "支持本地文件路径或 http://127.0.0.1/... URL；留空使用上方校准值",
	QuotaCalibSave:           "保存",
	QuotaCalibClear:          "清除校准",
	QuotaCalibCancel:         "取消",
	QuotaErrInvalidPct:       "请输入 0–100 的有效百分比",
	QuotaErrInvalidReset:     "请输入有效的重置倒计时（小时 ≥ 0，分钟 0–59）",
	QuotaErrUnsafeJSONSource: "仅支持本地文件路径、file:// 或 localhost/127.0.0.1 地址",
	QuotaErrJSONFallbackFmt:  "JSON 源读取失败，已回退本地估算 (%s)",

	HistoryTitle:           "历史消耗统计",
	HistoryScopeToday:      "本地只读聚合 · 今日分时段",
	HistoryScope7D:         "本地只读聚合 · 近 7 天",
	HistoryScope30D:        "本地只读聚合 · 近 30 天",
	HistoryScopeAll:        "本地只读聚合 · 全部历史",
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
	HistoryChartCost:       "费用走势（按模型族）",
	HistoryChartCache:      "Prompt 缓存命中率趋势",
	HistoryLegendHitPct:    "命中率 %",
	HistoryInspTokensFmt:   "缓存 %s · 输入 %s · 输出 %s",
	HistoryByModel:         "模型消耗占比",
	HistoryTopSessions:     "高消耗会话 Top 3",
	HistoryClickToInspect:  "点击跳转详情",
	HistoryUntitledSession: "未命名会话",
	HistoryOtherModel:      "其他模型",
	HistoryEmpty:           "暂无本地历史消耗记录",
	HistoryEmptyRange:      "当前时间范围内暂无消耗记录",
}

var enStrings = Strings{
	Lang: LangEN,

	MainAgent:      "Main Agent",
	WholeTree:      "Session Tree",
	CombinedStats:  "Combined",
	TreeSubtitle:   "Main + %d Sub",
	AgentStructure: "Agent Topology",
	RootOnly:       "Main Only",
	RootAndSubs:    "1 Main · %d Sub",
	SingleAgent:    "Single Agent",

	RunningBadge:    "%d Active",
	SwitchParallel:  "Switch active sessions",
	SwitchSession:   "Switch Session",
	AutoFollowFull:  "Auto-follow active",
	PinnedSession:   "Session pinned",
	AutoFollowShort: "Auto-follow",
	ConvCountFmt:    "· %d chats",
	NoLocalSessions: "No local sessions yet",
	NoActiveSession: "No active session",

	CostTotal:  "Total",
	CostRoot:   "Main",
	CostSub:    "Sub",
	NoRequests: "No requests",
	RecentFmt:  "Latest #%d · %s",

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
	TrayHeroSubCost:    "Subagents",
	TrayAvgShort:       "Avg %s",
	TrayTotalShort:     "Total %s",
	TrayCacheSection:   "Cache Hit (Last vs Total)",
	TrayLatestSingle:   "Latest Call",
	TraySessionTotal:   "Session Total",
	TrayCostSplit:      "Cost Split (Main vs Sub)",
	TrayRecentReqs:     "Recent Calls (Hit · TTFT · TPS)",
	TrayAgentDetails:   "Agents (Click to Focus)",
	TrayNoNodes:        "No agents",
	TrayLiveMetrics:    "Live Metrics (Last / Total)",
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

	QuotaTitle:               "Account Quota",
	QuotaSummaryPrefix:       "Current Window",
	QuotaRefresh:             "↻ Refresh",
	QuotaCalibrate:           "Calibrate",
	QuotaRemaining:           "Remaining",
	QuotaResetInFmt:          "Resets in %s",
	QuotaResetSoon:           "Resetting soon",
	QuotaSourceLocal:         "Local Calibrated Window",
	QuotaSourceJSON:          "Local JSON Source",
	QuotaSourceUncalibrated:  "Uncalibrated · Setup Needed",
	QuotaUncalibratedShort:   "Uncalibrated",
	QuotaUncalibratedTitle:   "Quota Baseline Not Calibrated",
	QuotaUncalibratedDesc:    "Click Calibrate Now to set your remaining % and reset timer, or configure a local JSON source.",
	QuotaCalibrateNow:        "Calibrate Now",
	QuotaTrendTitle:          "Quota Usage Trend",
	QuotaFilterAll:           "All",
	QuotaResetMarker:         "↺ Reset",
	QuotaInspNowSuffix:       " (Now)",
	QuotaInspDeltaFmt:        "Delta: Gemini %s · Claude & GPT %s",
	QuotaInsp7dNote:          "Daily lowest remaining window snapshot",
	QuotaTrendEmpty:          "No trend snapshots yet — calibrate or click Refresh",
	QuotaTrendSinglePoint:    "First snapshot recorded — trend curve builds as samples accumulate",
	QuotaCalibTitle:          "Calibrate Quota Baseline",
	QuotaCalibPrivacyNote:    "Local window tracking only; no host probing or OAuth access",
	QuotaCalibRemPct:         "Remaining %",
	QuotaCalibResetLabel:     "Resets in",
	QuotaCalibHoursUnit:      "h",
	QuotaCalibMinutesUnit:    "m",
	QuotaCalibJSONLabel:      "Local JSON Source (Optional)",
	QuotaCalibJSONHint:       "Local file path or http://127.0.0.1/... URL; blank uses manual baseline",
	QuotaCalibSave:           "Save",
	QuotaCalibClear:          "Clear",
	QuotaCalibCancel:         "Cancel",
	QuotaErrInvalidPct:       "Enter a valid percentage (0–100)",
	QuotaErrInvalidReset:     "Enter a valid reset timer (hours ≥ 0, minutes 0–59)",
	QuotaErrUnsafeJSONSource: "Only local file paths, file://, or localhost/127.0.0.1 URLs allowed",
	QuotaErrJSONFallbackFmt:  "JSON source failed, using local estimate (%s)",

	HistoryTitle:           "Usage History",
	HistoryScopeToday:      "Local Read-Only · Today Hourly",
	HistoryScope7D:         "Local Read-Only · Last 7 Days",
	HistoryScope30D:        "Local Read-Only · Last 30 Days",
	HistoryScopeAll:        "Local Read-Only · All Time",
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
	HistoryChartCost:       "Cost by Model Family",
	HistoryChartCache:      "Prompt Cache Hit Rate",
	HistoryLegendHitPct:    "Hit Rate %",
	HistoryInspTokensFmt:   "Cache %s · In %s · Out %s",
	HistoryByModel:         "Usage by Model",
	HistoryTopSessions:     "Top 3 Sessions",
	HistoryClickToInspect:  "Click to inspect",
	HistoryUntitledSession: "Untitled Session",
	HistoryOtherModel:      "Other",
	HistoryEmpty:           "No local usage history yet",
	HistoryEmptyRange:      "No usage recorded in this range",
}

// Get returns the localized string table for lang ("zh" or "en").
func Get(lang string) Strings {
	if Normalize(lang) == LangEN {
		return enStrings
	}
	return zhStrings
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
