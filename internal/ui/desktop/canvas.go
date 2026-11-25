package desktop

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

func (a *App) renderMainCanvas(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, vm scopeView, now time.Time) {
	lm := a.layout
	ui.Column(c.Key("main-canvas")).
		Grow(1).
		MinWidth(0).
		Padding(4, 6, 2, 6).
		Gap(lm.sectionGap).
		Children(func() {
			a.renderMainHeader(c, pal, conv, vm)
			a.renderHeroContextCard(c, pal, conv, vm, now)
			a.renderKPIGrid(c, pal, vm)
			a.renderTelemetryCard(c, pal, conv, vm)
		})
}

func (a *App) renderMainHeader(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, vm scopeView) {
	tr := a.tr()
	title := tr.AgentName(vm.activeAgent.ID, vm.activeAgent.Name)
	subtitle := vm.activeAgent.ModelDisplay
	if vm.isTreeView {
		title = tr.WholeTree
		subtitle = fmt.Sprintf(tr.TreeSubtitle, len(conv.Subagents))
	}

	hdrAll := fmtUSD(a.springs.Value("hdrAll", conv.TreeMetrics.SessionCostUSD))
	hdrRoot := fmtUSD(a.springs.Value("hdrRoot", conv.Root.Metrics.SessionCostUSD))
	hdrSub := fmtUSD(a.springs.Value("hdrSub", conv.SubagentsCostUSD))

	recentHint := tr.NoRequests
	if vm.hasLatestReq {
		if vm.isTreeView {
			src := tr.AgentName(vm.latestReq.AgentID, vm.latestSource)
			recentHint = fmt.Sprintf(tr.RecentFmt, vm.latestReq.Seq, src)
		} else {
			recentHint = fmt.Sprintf(tr.RecentNodeFmt, vm.latestReq.Seq)
		}
	}

	lm := a.layout
	rightTopPad := lm.headerRightPadT

	ui.Row(c.Key("main-header")).
		Padding(2, 4, 0, 4).
		Justify(ui.SpaceBetween).
		AlignItems(ui.End).
		Gap(12).
		DragWindow().
		Children(func() {
			// Left: current scope title (+ monochrome 3-way cost split only in whole-tree view).
			ui.Column(c).Gap(6).MinWidth(0).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Baseline).Children(func() {
					ui.Text(c, title).FontSize(lm.headerTitleSize).FontWeight(600).LetterSpacing(-0.3).TextColor(pal.Ink1).NoWrap()
					ui.Text(c, subtitle).FontSize(12).TextColor(pal.Ink2).SingleLine()
				})

				if vm.isTreeView {
					// Quiet monochrome 3-way ratio: Total / Main / Sub.
					ui.Row(c.Key("hdr-split")).Gap(8).AlignItems(ui.Center).Children(func() {
						costRatioPair(c, pal, tr.CostTotal, hdrAll)
						ui.Text(c, "/").FontSize(11.5).TextColor(pal.Ink3)
						costRatioPair(c, pal, tr.CostRoot, hdrRoot)
						ui.Text(c, "/").FontSize(11.5).TextColor(pal.Ink3)
						costRatioPair(c, pal, tr.CostSub, hdrSub)
					})
				}
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
	tr := a.tr()
	ui.Row(c.Key("dev-controls")).Gap(6).AlignItems(ui.Center).Children(func() {
		// Light / Dark glass toggle pill.
		themeLbl := tr.DevLightGlass
		themeIcon := theme.IconSun
		if pal.Dark {
			themeLbl = tr.DevDarkGlass
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
			liveTxt := tr.DevPaused
			dotCol := pal.Ink3
			if a.liveRunning {
				liveTxt = tr.DevLive
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
				Label(tr.DevBurst).
				Tooltip(tr.DevBurstTip)
			if burstBtn.Pressed() {
				burstBtn.Opacity(0.86)
			}
			burstBtn.Children(func() {
				ui.Text(c, tr.DevBurst).FontSize(11).FontWeight(600).TextColor(ui.RGB(255, 255, 255)).NoWrap()
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
	tr := a.tr()
	lm := a.layout
	card := theme.GlassCard(c.Key("hero-context-card"), pal, theme.CardOptions{
		Radius:    20,
		PingAlpha: a.heroPulse.Alpha(now),
	}).Padding(lm.cardPadY+1, lm.cardPadX, lm.cardPadY, lm.cardPadX).Gap(9 + 2*lm.scale).Shrink(0)

	pctVal := a.springs.Value("heroCtxPct", vm.activeAgent.ContextRatio*100)
	peakRatio := vm.activeAgent.ContextRatio
	if vm.isTreeView {
		pctVal = a.springs.Value("heroCtxPct", conv.PeakContext.Ratio*100)
		peakRatio = conv.PeakContext.Ratio
	}
	warnThreshold := a.settings.ContextWarnThreshold
	isWarn := warnThreshold > 0 && peakRatio >= warnThreshold

	pctColor := pal.Ink1
	if isWarn {
		pctColor = pal.Danger
	}

	// Pick segments to display in the legend (active agent in single view, peak agent in tree view).
	legendAgent := vm.activeAgent
	if vm.isTreeView {
		if ag, ok := conv.FindAgent(conv.PeakContext.AgentID); ok {
			legendAgent = ag
		} else {
			legendAgent = conv.Root
		}
	}
	legendSegs := legendAgent.ContextSegments

	card.Children(func() {
		// Top line: icon + "上下文窗口" (+ hovered segment readout pill) on left, percentage on right.
		ui.Row(c).Justify(ui.SpaceBetween).AlignItems(ui.Baseline).Gap(8).Children(func() {
			ui.Row(c).Gap(7).AlignItems(ui.Center).MinWidth(0).Children(func() {
				ui.Icon(c, theme.IconContext).Size(15, 15).TextColor(pal.Ink2)
				ui.Text(c, tr.ContextWindow).FontSize(12).FontWeight(500).TextColor(pal.Ink2).NoWrap()

				// Inline hover readout pill (zero layout jitter!) when inspecting a colored segment.
				if a.hoverCtxSegIdx >= 0 && a.hoverCtxSegIdx < len(legendSegs) {
					seg := legendSegs[a.hoverCtxSegIdx]
					segCol := theme.SegmentColor(pal, seg.Key)
					sharePct := 0.0
					if legendAgent.ContextLimit > 0 {
						sharePct = float64(seg.Tokens) / float64(legendAgent.ContextLimit) * 100
					}
					ui.Row(c.Key("ctx-hover-pill")).
						Padding(2, 8).
						Radius(999).
						Background(pal.SegBg).
						Gap(5).
						AlignItems(ui.Center).
						Children(func() {
							ui.Box(c).Size(6, 6).Radius(3).Background(segCol).Shrink(0)
							ui.Textf(c, "%s %s (%.1f%%)", tr.SegmentLabel(seg.Key), fmtTokensK(seg.Tokens), sharePct).
								FontSize(10.5).
								FontWeight(500).
								FontFeatures("tnum").
								TextColor(pal.Ink1).
								NoWrap()
						})
				}
			})

			ui.Row(c).Gap(7).AlignItems(ui.Baseline).Shrink(0).Children(func() {
				if vm.isTreeView {
					ui.Text(c, tr.PeakWatermark).FontSize(11).TextColor(pal.Ink3).NoWrap()
					ui.Text(c, fmtPct(pctVal)).
						FontSize(lm.heroPctSize).
						FontWeight(600).
						LetterSpacing(-0.3).
						FontFeatures("tnum").
						TextColor(pctColor).
						NoWrap()
					ui.Text(c, tr.AgentName(conv.PeakContext.AgentID, conv.PeakContext.AgentName)).
						FontSize(12).TextColor(pal.Ink2).NoWrap()
				} else {
					ui.Text(c, fmtPct(pctVal)).
						FontSize(lm.heroPctSize).
						FontWeight(600).
						LetterSpacing(-0.3).
						FontFeatures("tnum").
						TextColor(pctColor).
						NoWrap()
					ui.Textf(c, "%s / %s", fmtTokensK(vm.activeAgent.ContextUsed), fmtTokensK(vm.activeAgent.ContextLimit)).
						FontSize(12).
						FontFeatures("tnum").
						TextColor(pal.Ink2).
						NoWrap()
				}
			})
		})

		nextHover := -1

		// Progress bars: parallel clickable segmented rows in Tree View, single 10px macOS Storage bar in Single-Node View.
		if vm.isTreeView {
			agents := conv.AllAgents()
			renderRows := func() {
				ui.Column(c.Key("hero-ctx-bars-tree")).FillWidth().Gap(6).Children(func() {
					for idx, ag := range agents {
						ratio := ag.ContextRatio
						barOpacity := float32(1.0)
						if idx > 0 {
							barOpacity = 0.86
						}
						agWarn := warnThreshold > 0 && ratio >= warnThreshold
						dispName := tr.AgentName(ag.ID, ag.Name)

						row := ui.ButtonBase(c.Key("ctx-row-" + ag.ID)).
							FillWidth().
							Cursor(ui.CursorPointer).
							Label(dispName)

						nameCol := pal.Ink2
						if row.Hovered() {
							nameCol = pal.Accent
						}

						row.Children(func() {
							ui.Column(c).FillWidth().Gap(3).Children(func() {
								ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
									ui.Text(c, dispName).FontSize(11).FontWeight(500).TextColor(nameCol).SingleLine()
									pctCol := pal.Ink2
									if agWarn {
										pctCol = pal.Danger
									}
									ui.Textf(c, "%s / %s · %.1f%%", fmtTokensK(ag.ContextUsed), fmtTokensK(ag.ContextLimit), ratio*100).
										FontSize(11).
										FontFeatures("tnum").
										TextColor(pctCol).
										NoWrap()
								})
								hi := -1
								if ag.ID == legendAgent.ID {
									hi = a.hoverCtxSegIdx
								}
								_, hov := theme.SegmentedContextBar(
									c, pal, "bar-"+ag.ID,
									float32(ratio), ag.ContextSegments,
									6, barOpacity,
									conv.Running && conv.ActiveAgentID == ag.ID,
									agWarn, hi,
								)
								if ag.ID == legendAgent.ID && hov >= 0 {
									nextHover = hov
								}
							})
						})

						if row.Clicked() {
							a.FocusAgent(ag.ID)
						}
					}
				})
			}
			if len(agents) > 4 {
				ui.Scroll(c.Key("hero-ctx-scroll")).FillWidth().Height(108).Children(renderRows)
			} else {
				renderRows()
			}
		} else {
			ratio := vm.activeAgent.ContextRatio
			agWarn := warnThreshold > 0 && ratio >= warnThreshold
			_, hov := theme.SegmentedContextBar(
				c, pal, "bar-single-"+vm.activeAgent.ID,
				float32(ratio), vm.activeAgent.ContextSegments,
				lm.heroBarH, 1.0,
				conv.Running && conv.ActiveAgentID == vm.activeAgent.ID,
				agWarn, a.hoverCtxSegIdx,
			)
			if hov >= 0 {
				nextHover = hov
			}
		}

		// Compact macOS-Storage-style legend row right below the bar (hoverable to highlight segment).
		if len(legendSegs) > 0 {
			ui.Row(c.Key("ctx-legend-row")).
				FillWidth().
				Padding(2, 0, 0, 0).
				Gap(12).
				AlignItems(ui.Center).
				Children(func() {
					for i, seg := range legendSegs {
						segCol := theme.SegmentColor(pal, seg.Key)
						isHi := a.hoverCtxSegIdx == i
						item := ui.Row(c.Key(fmt.Sprintf("ctx-leg-%d", i))).
							Gap(5).
							AlignItems(ui.Center).
							Cursor(ui.CursorPointer)
						if item.Hovered() {
							nextHover = i
							isHi = true
						}

						lblCol := pal.Ink2
						valCol := pal.Ink3
						weight := 400
						if isHi {
							lblCol = pal.Ink1
							valCol = pal.Ink1
							weight = 600
						}
						item.Children(func() {
							ui.Box(c).Size(7, 7).Radius(3.5).Background(segCol).Shrink(0)
							ui.Text(c, tr.SegmentLabel(seg.Key)).FontSize(10.5).FontWeight(weight).TextColor(lblCol).NoWrap()
							ui.Text(c, fmtTokensK(seg.Tokens)).FontSize(10.5).FontWeight(weight).FontFeatures("tnum").TextColor(valCol).NoWrap()
						})
					}
				})
		}

		a.hoverCtxSegIdx = nextHover
	})
}

func (a *App) renderKPIGrid(c *ui.Context, pal theme.Palette, vm scopeView) {
	tr := a.tr()
	shortSrc := ""
	if vm.isTreeView && vm.latestSource != "" {
		shortSrc = " · " + shortName(tr.AgentName(vm.latestReq.AgentID, vm.latestSource), 10)
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
			title:    ternary(vm.isTreeView, tr.KPITreeTurnCost, tr.KPINodeTurnCost),
			icon:     theme.IconCost,
			mainVal:  fmtUSD(a.springs.Value("turnCost", vm.metrics.TurnCostUSD)),
			subLabel: ternary(vm.isTreeView, tr.KPITreeSessionCost, tr.KPINodeSessionCost),
			subVal:   fmtUSD(a.springs.Value("sessionCost", vm.metrics.SessionCostUSD)),
		},
		{
			kind:     MetricCache,
			title:    ternary(vm.isTreeView, tr.KPITreeCache, tr.KPINodeCache),
			icon:     theme.IconCache,
			mainVal:  fmtPct(a.springs.Value("cacheMain", vm.metrics.WeightedCacheHitRate*100)),
			subLabel: ternary(vm.isTreeView, tr.SubLatest, tr.SubTotal),
			subVal:   fmtPct(a.springs.Value("cacheSub", vm.latestReq.CacheHitRate*100)),
			subSrc:   shortSrc,
		},
		{
			kind:     MetricTTFT,
			title:    ternary(vm.isTreeView, tr.KPITreeTTFT, tr.KPINodeTTFT),
			icon:     theme.IconClock,
			mainVal:  fmtSec(a.springs.Value("ttftMain", vm.metrics.AvgTTFT.Seconds())),
			subLabel: ternary(vm.isTreeView, tr.SubLatest, tr.SubAvg),
			subVal:   fmtSec(a.springs.Value("ttftSub", vm.latestReq.TTFT.Seconds())),
			subSrc:   shortSrc,
		},
		{
			kind:     MetricTPS,
			title:    ternary(vm.isTreeView, tr.KPITreeTPS, tr.KPINodeTPS),
			icon:     theme.IconBolt,
			mainVal:  fmtTPS(a.springs.Value("tpsMain", vm.metrics.WeightedDecodeTPS)),
			subLabel: ternary(vm.isTreeView, tr.SubLatest, tr.SubAvgTPS),
			subVal:   ternary(vm.isTreeView, fmtTPS(a.springs.Value("tpsSub", vm.latestReq.DecodeTPS)), fmtTPS(a.springs.Value("tpsSub", vm.metrics.WeightedDecodeTPS))+" t/s"),
			subSrc:   shortSrc,
		},
	}

	lm := a.layout
	ui.Grid(c.Key("kpi-grid")).Columns(4).Gap(lm.sectionGap).Children(func() {
		for _, sp := range specs {
			selected := a.metric == sp.kind
			tile := theme.GlassCard(c.Key("kpi-"+string(sp.kind)), pal, theme.CardOptions{
				Radius:      20,
				Selected:    selected,
				Interactive: true,
			}).Padding(lm.kpiPadY, lm.kpiPadX, lm.kpiPadY-1, lm.kpiPadX).Gap(10 + 2*lm.scale).Label(sp.title)

			tile.Children(func() {
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
					ui.Text(c, sp.title).FontSize(12).FontWeight(500).TextColor(pal.Ink2).SingleLine()
					ui.Icon(c, sp.icon).Size(14, 14).TextColor(pal.Ink2)
				})

				ui.Column(c).Gap(3).MinWidth(0).Children(func() {
					ui.Text(c, sp.mainVal).
						FontSize(lm.kpiValueSize).
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
