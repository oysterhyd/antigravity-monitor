package desktop

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

func (a *App) renderMainCanvas(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, vm scopeView, now time.Time) {
	ui.Column(c.Key("main-canvas")).
		Grow(1).
		MinWidth(0).
		Padding(4, 6, 2, 6).
		Gap(14).
		Children(func() {
			a.renderMainHeader(c, pal, conv, vm)
			a.renderHeroContextCard(c, pal, conv, vm, now)
			a.renderKPIGrid(c, pal, vm)
			a.renderTelemetryCard(c, pal, conv, vm)
		})
}

func (a *App) renderMainHeader(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, vm scopeView) {
	title := vm.activeAgent.Name
	subtitle := vm.activeAgent.ModelDisplay
	if vm.isTreeView {
		title = "整棵会话树"
		subtitle = fmt.Sprintf("主 + %d 子节点", len(conv.Subagents))
	}

	hdrAll := fmtUSD(a.springs.Value("hdrAll", conv.TreeMetrics.SessionCostUSD))
	hdrRoot := fmtUSD(a.springs.Value("hdrRoot", conv.Root.Metrics.SessionCostUSD))
	hdrSub := fmtUSD(a.springs.Value("hdrSub", conv.SubagentsCostUSD))

	recentHint := "暂无请求"
	if vm.hasLatestReq {
		src := vm.latestSource
		if src == "" {
			src = vm.activeAgent.Name
		}
		recentHint = fmt.Sprintf("最近 #%d · %s", vm.latestReq.Seq, src)
	}

	// On Windows/Linux TitleBarHidden, add subtle top clearance on the right column
	// so the activity indicator never overlaps the native minimize/maximize/close buttons.
	bar := c.TitleBar()
	rightTopPad := float32(0)
	if bar.Right > 0 && bar.Height > 0 {
		rightTopPad = max(float32(18), bar.Height-10)
	}

	ui.Row(c.Key("main-header")).
		Padding(2, 4, 0, 4).
		Justify(ui.SpaceBetween).
		AlignItems(ui.End).
		Gap(12).
		DragWindow().
		Children(func() {
			// Left: current scope title + monochrome 3-way cost split.
			ui.Column(c).Gap(6).MinWidth(0).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Baseline).Children(func() {
					ui.Text(c, title).FontSize(20).FontWeight(600).LetterSpacing(-0.3).TextColor(pal.Ink1).NoWrap()
					ui.Text(c, subtitle).FontSize(12).TextColor(pal.Ink2).SingleLine()
				})

				// Quiet monochrome 3-way ratio: 合计 / 主 / 子.
				ui.Row(c.Key("hdr-split")).Gap(8).AlignItems(ui.Center).Children(func() {
					costRatioPair(c, pal, "合计", hdrAll)
					ui.Text(c, "/").FontSize(11.5).TextColor(pal.Ink3)
					costRatioPair(c, pal, "主", hdrRoot)
					ui.Text(c, "/").FontSize(11.5).TextColor(pal.Ink3)
					costRatioPair(c, pal, "子", hdrSub)
				})
			})

			// Right: quiet activity hint + optional dev harness controls.
			ui.Column(c).Padding(rightTopPad, 0, 0, 0).AlignItems(ui.End).Gap(6).Shrink(0).Children(func() {
				if a.opts.ShowDevToolbar {
					a.renderDevControls(c, pal)
				}
				ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
					ui.Text(c, recentHint).FontSize(12).FontFeatures("tnum").TextColor(pal.Ink2).NoWrap()
					dotCol := pal.Ink3
					if conv.Running {
						dotCol = pal.Accent
					}
					statusDot(c, "hdr-pulse", 6.5, dotCol, conv.Running)
				})
			})
		})
}

func (a *App) renderDevControls(c *ui.Context, pal theme.Palette) {
	ui.Row(c.Key("dev-controls")).Gap(6).AlignItems(ui.Center).Children(func() {
		// Light / Dark glass toggle pill.
		themeLbl := "浅色玻璃"
		themeIcon := theme.IconSun
		if pal.Dark {
			themeLbl = "深色玻璃"
			themeIcon = theme.IconMoon
		}
		themeBtn := ui.ButtonBase(c.Key("dev-theme-btn")).
			Padding(4, 10).
			Radius(999).
			Background(pal.CardBg).
			Border(1, pal.RimEdge).
			InsetShadow(0, 1, 0, 0, pal.RimTop).
			Cursor(ui.CursorPointer).
			Label(themeLbl)
		if themeBtn.Hovered() {
			themeBtn.Background(pal.CardHover)
		}
		themeBtn.Children(func() {
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, themeIcon).Size(12, 12).TextColor(pal.Ink2)
				ui.Text(c, themeLbl).FontSize(11).FontWeight(500).TextColor(pal.Ink1).NoWrap()
			})
		})
		if themeBtn.Clicked() {
			if pal.Dark {
				a.SetThemeMode(theme.ModeLight)
			} else {
				a.SetThemeMode(theme.ModeDark)
			}
		}

		// Live stream toggle pill.
		if a.opts.OnToggleLive != nil {
			liveTxt := "已暂停"
			dotCol := pal.Ink3
			if a.liveRunning {
				liveTxt = "实时流"
				dotCol = pal.Accent
			}
			liveBtn := ui.ButtonBase(c.Key("dev-live-btn")).
				Padding(4, 10).
				Radius(999).
				Background(pal.CardBg).
				Border(1, pal.RimEdge).
				InsetShadow(0, 1, 0, 0, pal.RimTop).
				Cursor(ui.CursorPointer).
				Label(liveTxt)
			if liveBtn.Hovered() {
				liveBtn.Background(pal.CardHover)
			}
			liveBtn.Children(func() {
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					statusDot(c, "dev-live-dot", 6, dotCol, a.liveRunning)
					ui.Text(c, liveTxt).FontSize(11).FontWeight(500).TextColor(pal.Ink1).NoWrap()
				})
			})
			if liveBtn.Clicked() {
				a.liveRunning = a.opts.OnToggleLive()
			}
		}

		// Burst trigger pill (single Apple Blue accent).
		if a.opts.OnTriggerBurst != nil {
			burstBtn := ui.ButtonBase(c.Key("dev-burst-btn")).
				Padding(4, 11).
				Radius(999).
				Background(pal.Accent).
				Cursor(ui.CursorPointer).
				Label("模拟请求").
				Tooltip("立即注入一条模拟请求 (快捷键 B)")
			if burstBtn.Pressed() {
				burstBtn.Opacity(0.86)
			}
			burstBtn.Children(func() {
				ui.Text(c, "模拟请求").FontSize(11).FontWeight(600).TextColor(ui.RGB(255, 255, 255)).NoWrap()
			})
			if burstBtn.Clicked() {
				a.opts.OnTriggerBurst()
			}
		}
	})
}

func costRatioPair(c *ui.Context, pal theme.Palette, label, val string) {
	ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(pal.Ink2).NoWrap()
		ui.Text(c, val).FontSize(12).FontWeight(600).FontFeatures("tnum").TextColor(pal.Ink1).NoWrap()
	})
}

func (a *App) renderHeroContextCard(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, vm scopeView, now time.Time) {
	card := theme.GlassCard(c.Key("hero-context-card"), pal, theme.CardOptions{
		Radius:    20,
		PingAlpha: a.heroPulse.Alpha(now),
	}).Padding(16).Gap(12)

	pctVal := a.springs.Value("heroCtxPct", vm.activeAgent.ContextRatio*100)
	if vm.isTreeView {
		pctVal = a.springs.Value("heroCtxPct", conv.PeakContext.Ratio*100)
	}

	card.Children(func() {
		// Top line: icon + "上下文窗口" on left, peak/current percentage on right.
		ui.Row(c).Justify(ui.SpaceBetween).AlignItems(ui.Baseline).Gap(8).Children(func() {
			ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, theme.IconContext).Size(15, 15).TextColor(pal.Ink2)
				ui.Text(c, "上下文窗口").FontSize(12).FontWeight(500).TextColor(pal.Ink2).NoWrap()
			})

			ui.Row(c).Gap(7).AlignItems(ui.Baseline).Children(func() {
				if vm.isTreeView {
					ui.Text(c, "最高水位").FontSize(11).TextColor(pal.Ink3).NoWrap()
					ui.Text(c, fmtPct(pctVal)).
						FontSize(20).
						FontWeight(600).
						LetterSpacing(-0.3).
						FontFeatures("tnum").
						TextColor(pal.Ink1).
						NoWrap()
					ui.Text(c, conv.PeakContext.AgentName).FontSize(12).TextColor(pal.Ink2).NoWrap()
				} else {
					ui.Text(c, fmtPct(pctVal)).
						FontSize(20).
						FontWeight(600).
						LetterSpacing(-0.3).
						FontFeatures("tnum").
						TextColor(pal.Ink1).
						NoWrap()
					ui.Textf(c, "%s / %s", fmtTokensK(vm.activeAgent.ContextUsed), fmtTokensK(vm.activeAgent.ContextLimit)).
						FontSize(12).
						FontFeatures("tnum").
						TextColor(pal.Ink2).
						NoWrap()
				}
			})
		})

		// Progress bars: parallel clickable rows in Tree View, single thicker bar in Single-Node View.
		// ContextWarnThreshold == 0 intentionally disables threshold warnings.
		warnThreshold := a.settings.ContextWarnThreshold

		if vm.isTreeView {
			agents := conv.AllAgents()
			ui.Column(c.Key("hero-ctx-bars-tree")).Gap(9).Children(func() {
				for idx, ag := range agents {
					ratio := ag.ContextRatio
					fill := contextBarColor(pal, ratio, warnThreshold, idx == 0)
					barOpacity := float32(1.0)
					if idx > 0 {
						barOpacity = 0.76
					}

					row := ui.ButtonBase(c.Key("ctx-row-" + ag.ID)).
						FillWidth().
						Cursor(ui.CursorPointer).
						Label(ag.Name)

					nameCol := pal.Ink2
					if row.Hovered() {
						nameCol = pal.Accent
					}

					row.Children(func() {
						ui.Column(c).FillWidth().Gap(4).Children(func() {
							ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
								ui.Text(c, ag.Name).FontSize(11).FontWeight(500).TextColor(nameCol).SingleLine()
								ui.Textf(c, "%s / %s · %.1f%%", fmtTokensK(ag.ContextUsed), fmtTokensK(ag.ContextLimit), ratio*100).
									FontSize(11).
									FontFeatures("tnum").
									TextColor(pal.Ink2).
									NoWrap()
							})
							theme.LiquidProgressBar(c, pal, "bar-"+ag.ID, float32(ratio), fill, 6, barOpacity, conv.Running && conv.ActiveAgentID == ag.ID)
						})
					})

					if row.Clicked() {
						a.FocusAgent(ag.ID)
					}
				}
			})
		} else {
			ratio := vm.activeAgent.ContextRatio
			fill := contextBarColor(pal, ratio, warnThreshold, true)
			theme.LiquidProgressBar(c, pal, "bar-single-"+vm.activeAgent.ID, float32(ratio), fill, 10, 1.0, conv.Running && conv.ActiveAgentID == vm.activeAgent.ID)
		}
	})
}

func contextBarColor(pal theme.Palette, ratio, warnThreshold float64, isPrimary bool) ui.Color {
	switch {
	case warnThreshold > 0 && ratio >= warnThreshold:
		return pal.Danger
	case warnThreshold > 0 && ratio >= 0.60:
		return pal.Amber
	case isPrimary:
		return pal.Accent
	default:
		return pal.Ink2
	}
}

func (a *App) renderKPIGrid(c *ui.Context, pal theme.Palette, vm scopeView) {
	shortSrc := ""
	if vm.isTreeView && vm.latestSource != "" {
		shortSrc = " · " + shortName(vm.latestSource, 10)
	}

	type kpiSpec struct {
		kind     MetricKind
		title    string
		icon     *ui.SVG
		mainVal  string
		subLabel string
		subVal   string
		subSrc   string
	}

	specs := []kpiSpec{
		{
			kind:     MetricCost,
			title:    ternary(vm.isTreeView, "全树本轮", "本轮费用"),
			icon:     theme.IconCost,
			mainVal:  fmtUSD(a.springs.Value("turnCost", vm.metrics.TurnCostUSD)),
			subLabel: ternary(vm.isTreeView, "全树会话", "会话"),
			subVal:   fmtUSD(a.springs.Value("sessionCost", vm.metrics.SessionCostUSD)),
		},
		{
			kind:     MetricCache,
			title:    ternary(vm.isTreeView, "全树命中", "缓存命中"),
			icon:     theme.IconCache,
			mainVal:  fmtPct(a.springs.Value("cacheMain", vm.metrics.WeightedCacheHitRate*100)),
			subLabel: ternary(vm.isTreeView, "最新", "总计"),
			subVal:   fmtPct(a.springs.Value("cacheSub", vm.latestReq.CacheHitRate*100)),
			subSrc:   shortSrc,
		},
		{
			kind:     MetricTTFT,
			title:    ternary(vm.isTreeView, "全树均 TTFT", "首字时长"),
			icon:     theme.IconClock,
			mainVal:  fmtSec(a.springs.Value("ttftMain", vm.metrics.AvgTTFT.Seconds())),
			subLabel: ternary(vm.isTreeView, "最新", "均值"),
			subVal:   fmtSec(a.springs.Value("ttftSub", vm.latestReq.TTFT.Seconds())),
			subSrc:   shortSrc,
		},
		{
			kind:     MetricTPS,
			title:    ternary(vm.isTreeView, "全树均速", "吐字速度"),
			icon:     theme.IconBolt,
			mainVal:  fmtTPS(a.springs.Value("tpsMain", vm.metrics.WeightedDecodeTPS)),
			subLabel: ternary(vm.isTreeView, "最新", "均速"),
			subVal:   ternary(vm.isTreeView, fmtTPS(a.springs.Value("tpsSub", vm.latestReq.DecodeTPS)), fmtTPS(a.springs.Value("tpsSub", vm.metrics.WeightedDecodeTPS))+" t/s"),
			subSrc:   shortSrc,
		},
	}

	ui.Grid(c.Key("kpi-grid")).Columns(4).Gap(12).Children(func() {
		for _, sp := range specs {
			selected := a.metric == sp.kind
			tile := theme.GlassCard(c.Key("kpi-"+string(sp.kind)), pal, theme.CardOptions{
				Radius:      20,
				Selected:    selected,
				Interactive: true,
			}).Padding(15, 15, 14, 15).Gap(10).Label(sp.title)

			tile.Children(func() {
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
					ui.Text(c, sp.title).FontSize(12).FontWeight(500).TextColor(pal.Ink2).SingleLine()
					ui.Icon(c, sp.icon).Size(14, 14).TextColor(pal.Ink2)
				})

				ui.Column(c).Gap(3).MinWidth(0).Children(func() {
					ui.Text(c, sp.mainVal).
						FontSize(23).
						FontWeight(600).
						LetterSpacing(-0.4).
						FontFeatures("tnum").
						TextColor(pal.Ink1).
						SingleLine()

					ui.Row(c).Gap(4).AlignItems(ui.Baseline).MinWidth(0).Children(func() {
						ui.Text(c, sp.subLabel).FontSize(11.5).TextColor(pal.Ink2).NoWrap()
						ui.Text(c, sp.subVal).FontSize(11.5).FontWeight(500).FontFeatures("tnum").TextColor(pal.Ink1).NoWrap()
						if sp.subSrc != "" {
							ui.Text(c, sp.subSrc).FontSize(10.5).TextColor(pal.Ink3).SingleLine()
						}
					})
				})
			})

			if tile.Clicked() {
				a.SelectMetric(sp.kind)
			}
		}
	})
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
