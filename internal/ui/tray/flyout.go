package tray

import (
	"fmt"
	"math"
	"runtime"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	appupdater "github.com/oysterhyd/antigravity-monitor/internal/updater"
)

const (
	flyoutWidth  = 336
	flyoutRadius = 22
)

var (
	segSpring = ui.SpringOptions{Stiffness: 240, Damping: 25, Precision: 0.002}
	valSpring = ui.SpringOptions{Stiffness: 170, Damping: 22, Precision: 0.001}
	barSpring = ui.SpringOptions{Stiffness: 140, Damping: 22, Precision: 0.001}
)

// buildView renders the Pulse-style frosted glass tray flyout matching
// docs/prototype.html (#trayFlyout). Runs on the mygo main UI thread.
func (ctl *Controller) buildView(c *ui.Context) {
	st := ctl.state
	pal := resolvePalette(st.Settings.ThemeMode, c.Theme().Dark)
	vibrant := c.Vibrancy()

	// Let OS Acrylic/Liquid Glass show cleanly through the root when available.
	c.Root().Background(ui.Transparent)

	shell := ui.Column(c.Key("tray-shell")).Width(flyoutWidth).Padding(16).Gap(14)
	switch {
	case vibrant:
		// Luminous translucent Apple Liquid Glass wash over real OS Acrylic.
		dark := pal.Dark
		shell.Draw(func(p *ui.Painter, r ui.Rect) {
			if dark {
				p.FillGradient(r, ui.LinearGradient{
					Angle: 145,
					Stops: []ui.GradientStop{
						{Color: ui.RGBA(22, 28, 46, 0.72), At: 0},
						{Color: ui.RGBA(28, 26, 46, 0.74), At: 1},
					},
				}, 0)
			} else {
				p.FillGradient(r, ui.LinearGradient{
					Angle: 145,
					Stops: []ui.GradientStop{
						{Color: ui.RGBA(240, 246, 255, 0.74), At: 0},
						{Color: ui.RGBA(248, 244, 254, 0.76), At: 1},
					},
				}, 0)
			}
		})
		if runtime.GOOS != "windows" {
			shell.Radius(flyoutRadius).Border(1, pal.RimEdge)
		}
	case runtime.GOOS == "windows":
		shell.Background(pal.FlyoutOpaque)
	default:
		shell.Radius(flyoutRadius).Material(glass.Glass{
			Style:   glass.Regular,
			Tint:    pal.FlyoutGlass,
			Clarity: 0.35,
		}).Border(1, pal.RimEdge)
	}

	// Top 1px specular rim gleam across the glass top edge.
	shell.DrawOver(func(p *ui.Painter, r ui.Rect) {
		p.Line(r.X+16, r.Y+0.5, r.X+r.W-16, r.Y+0.5, 1, pal.RimTop)
	})

	conv, hasConv := st.ActiveConversation()

	shell.Children(func() {
		ctl.renderSegmentedTabs(c, pal)
		ctl.renderHeader(c, pal, conv, hasConv)
		ctl.renderHeroPair(c, pal, conv, hasConv)
		ctl.renderDotMatrixBlock(c, pal)
		ctl.renderMiddleSection(c, pal, conv, hasConv)
		ctl.renderBottomSection(c, pal, conv, hasConv)
		ctl.renderFooter(c, pal)
	})
}

func (ctl *Controller) renderSegmentedTabs(c *ui.Context, pal palette) {
	st := ctl.state
	seg := ui.Box(c.Key("tray-seg")).AlignSelf(ui.Stretch).Height(32).Radius(16).
		Background(pal.SegBg).Padding(3)

	thumbIdx := seg.Spring("seg-idx", float32(st.Tab), segSpring)
	seg.Draw(func(p *ui.Painter, r ui.Rect) {
		innerW := r.W - 6
		if innerW <= 0 {
			return
		}
		pillW := innerW / 3
		pillX := r.X + 3 + thumbIdx*pillW
		pillRect := ui.Rect{X: pillX, Y: r.Y + 3, W: pillW, H: r.H - 6}
		p.Shadow(pillRect, (r.H-6)/2, 0, 1.5, 6, 0, pal.Shadow)
		p.Fill(pillRect, pal.SegPill, (r.H-6)/2)
		p.Stroke(pillRect, pal.SegPillBorder, (r.H-6)/2, 0.75)
	})

	seg.Children(func() {
		ui.Row(c).Fill().AlignItems(ui.Center).Children(func() {
			for i, label := range TabLabels {
				tab := Tab(i)
				active := st.Tab == tab
				col := pal.Ink2
				weight := 500
				if active {
					col = pal.Ink1
					weight = 600
				}
				btn := ui.ButtonBase(c.Key(fmt.Sprintf("tab-%d", i))).
					Grow(1).Basis(0).Height(26).Center().Radius(13).Cursor(ui.CursorPointer).
					Label(label).
					Children(func() {
						ui.Text(c, label).FontSize(12).FontWeight(weight).TextColor(col).SingleLine()
					})
				if btn.Clicked() {
					st.SetTab(tab)
					ctl.dotMatrix.markDirty()
				}
			}
		})
	})
}

func (ctl *Controller) renderHeader(c *ui.Context, pal palette, conv model.ConversationSnapshot, hasConv bool) {
	st := ctl.state

	var title string
	var ic *ui.SVG
	switch st.Tab {
	case TabPerf:
		title = "速率与缓存"
		ic = iconPerf
	case TabAgents:
		title = "父子 Agent 树"
		ic = iconTree
	default:
		title = "实时概览"
		ic = iconBolt
	}

	convTitle := "无活跃会话"
	if hasConv && conv.Title != "" {
		convTitle = conv.Title
	}

	var convAnchor ui.Element
	ui.Row(c.Key("tray-hdr")).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
		// Left: Monochrome icon pill + Section title
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Box(c).Size(24, 24).Radius(8).Background(pal.AccentSoft).Center().Children(func() {
				ui.Icon(c, ic).Size(13, 13).TextColor(pal.Accent)
			})
			ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(pal.Ink1).SingleLine()
		})

		// Right: Multi-conversation glass switcher capsule
		btnBg := pal.SegBg
		convAnchor = ui.ButtonBase(c.Key("conv-switcher")).
			Padding(4, 9).Radius(12).Background(btnBg).Cursor(ui.CursorPointer).
			Label("切换会话")
		if convAnchor.Hovered() || st.SessionMenuOpen {
			convAnchor.Background(pal.CardHover)
		}

		// Gentle breathing pulse when any conversation is running.
		isRunning := (hasConv && conv.Running) || st.Snapshot.RunningCount > 0
		dotCol := pal.Ink3
		if isRunning {
			dotCol = pal.Accent
		}

		convAnchor.Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				dot := ui.Box(c).Size(7, 7)
				dot.Draw(func(p *ui.Painter, r ui.Rect) {
					alpha := float32(1.0)
					scale := float32(0.85)
					if isRunning {
						phase := float64(p.Now().UnixMilli()%2200) / 2200.0 * 2 * math.Pi
						wave := float32(0.5 + 0.5*math.Sin(phase))
						alpha = 0.45 + 0.55*wave
						scale = 0.78 + 0.22*wave
						p.After(50 * time.Millisecond)
					}
					d := min(r.W, r.H) * scale
					col := dotCol
					col.A = uint8(math.Round(float64(col.A) * float64(alpha)))
					p.Fill(ui.Rect{
						X: r.X + (r.W-d)/2,
						Y: r.Y + (r.H-d)/2,
						W: d,
						H: d,
					}, col, d/2)
				})
				if st.Snapshot.RunningCount > 1 {
					ui.Textf(c, "%d · %s", st.Snapshot.RunningCount, TruncateRunes(convTitle, 11)).
						FontSize(11).FontWeight(500).TextColor(pal.Ink2).FontFeatures("tnum").SingleLine()
				} else {
					ui.Text(c, TruncateRunes(convTitle, 14)).
						FontSize(11).FontWeight(500).TextColor(pal.Ink2).SingleLine()
				}
				ui.Icon(c, iconDown).Size(10, 10).TextColor(pal.Ink3)
			})
		})
		if convAnchor.Clicked() {
			st.SessionMenuOpen = !st.SessionMenuOpen
			if st.SessionMenuOpen {
				st.SettingsOpen = false
			}
		}
	})

	// Floating crisp near-opaque multi-conversation dropdown popover with bounded scroll list.
	if st.SessionMenuOpen && convAnchor.Valid() {
		ui.PopoverBase(c.Key("conv-popover"), convAnchor, &st.SessionMenuOpen, func(panel ui.Element) {
			edge := ui.RGBA(15, 23, 42, 0.10)
			if pal.Dark {
				edge = ui.RGBA(255, 255, 255, 0.14)
			}
			listH := float32(len(st.Snapshot.Conversations))*46 + 6
			if listH < 52 {
				listH = 52
			}
			if listH > 236 {
				listH = 236
			}

			panel.AttachTo(convAnchor, ui.AnchorBottomEnd, ui.AnchorTopEnd).
				Margin(6, 0, 0, 0).Width(274).Radius(16).Padding(8).
				Background(pal.PopoverBg).
				Border(1, edge).
				InsetShadow(0, 1, 0, 0, pal.RimTop).
				Shadow(0, 14, 34, 0, pal.Shadow).
				Children(func() {
					ui.Column(c).FillWidth().Gap(6).Children(func() {
						// Auto-follow toggle header
						autoFollow := st.Settings.AutoFollowActive
						ui.Row(c).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Padding(2, 6, 6, 6).Children(func() {
							ui.Text(c, "自动跟随活跃").FontSize(11).FontWeight(500).TextColor(pal.Ink2)
							sw := ui.Switch(c.Key("pop-autofollow"), &autoFollow).Label("自动跟随活跃")
							if sw.Changed() {
								ctl.updateSettings(func(s *model.Settings) {
									s.AutoFollowActive = autoFollow
								})
							}
						})
						ui.Box(c).AlignSelf(ui.Stretch).Height(1).Background(pal.Divider)

						if len(st.Snapshot.Conversations) == 0 {
							ui.Text(c, "暂无本地会话记录").FontSize(11).TextColor(pal.Ink3).Padding(8, 6)
							return
						}

						ui.Scroll(c.Key("tray-conv-scroll")).FillWidth().Height(listH).Children(func() {
							ui.Column(c).FillWidth().Padding(2, 2, 4, 2).Gap(3).Children(func() {
								for _, item := range st.Snapshot.Conversations {
									item := item
									selected := item.ID == st.SelectedConvID
									row := ui.ButtonBase(c.Key("conv-item-" + item.ID)).
										AlignSelf(ui.Stretch).Padding(7, 9).Radius(10).Cursor(ui.CursorPointer)
									if selected {
										row.Background(pal.AccentSoft).Border(1, pal.Accent.Alpha(0.24))
									} else if row.Hovered() {
										row.Background(pal.SegBg)
									}

									subsN := len(item.Subagents)
									ctxRatio := item.Root.ContextRatio
									if item.PeakContext.Ratio > ctxRatio {
										ctxRatio = item.PeakContext.Ratio
									}
									subInfo := fmt.Sprintf("单 Agent · Ctx %.0f%%", ctxRatio*100)
									if subsN > 0 {
										subInfo = fmt.Sprintf("1 主 · %d 子 · Ctx %.0f%%", subsN, ctxRatio*100)
									}
									statusCol := pal.Ink3.Alpha(0.45)
									if selected || item.Running {
										statusCol = pal.Accent
									}
									titleWeight := 500
									costCol := pal.Ink2
									if selected {
										titleWeight = 600
										costCol = pal.Ink1
									}

									row.Children(func() {
										ui.Row(c).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
											ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Center).Gap(7).Children(func() {
												ui.Box(c).Size(6, 6).Radius(3).Shrink(0).Background(statusCol)
												ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
													ui.Text(c, TruncateRunes(item.Title, 22)).FontSize(12).FontWeight(titleWeight).TextColor(pal.Ink1).SingleLine()
													ui.Text(c, subInfo).FontSize(10).TextColor(pal.Ink3).FontFeatures("tnum").SingleLine()
												})
											})
											ui.Text(c, FormatUSD(item.TreeMetrics.SessionCostUSD)).
												FontSize(11).FontWeight(titleWeight).TextColor(costCol).FontFeatures("tnum").SingleLine()
										})
									})
									if row.Clicked() {
										st.SelectConversation(item.ID, true)
										ctl.updateSettings(func(s *model.Settings) {
											s.AutoFollowActive = false
										})
										ctl.dotMatrix.markDirty()
										ctl.syncTrayMetadata()
									}
								}
							})
						})
					})
				})
		})
	}
}

func (ctl *Controller) renderHeroPair(c *ui.Context, pal palette, conv model.ConversationSnapshot, hasConv bool) {
	st := ctl.state

	var (
		lbl1, sub1 string
		lbl2, sub2 string
		target1    float64
		target2    float64
		fmt1, fmt2 func(float64) string
	)

	var lastTPS, lastTTFT float64
	if hasConv {
		if conv.TreeMetrics.LatestRequest != nil {
			lastTPS = conv.TreeMetrics.LatestRequest.DecodeTPS
			lastTTFT = conv.TreeMetrics.LatestRequest.TTFT.Seconds()
		} else {
			lastTPS = conv.TreeMetrics.WeightedDecodeTPS
			lastTTFT = conv.TreeMetrics.AvgTTFT.Seconds()
		}
	}

	switch st.Tab {
	case TabPerf:
		lbl1 = "吐字速度"
		target1 = lastTPS
		fmt1 = FormatTPS
		sub1 = fmt.Sprintf("均 %.0f", conv.TreeMetrics.WeightedDecodeTPS)

		lbl2 = "首字 TTFT"
		target2 = lastTTFT
		fmt2 = func(v float64) string { return fmt.Sprintf("%.2fs", max(0, v)) }
		sub2 = fmt.Sprintf("均 %.2fs", conv.TreeMetrics.AvgTTFT.Seconds())

	case TabAgents:
		rootCost := conv.Root.Metrics.SessionCostUSD
		subCost := conv.SubagentsCostUSD
		totalCost := conv.TreeMetrics.SessionCostUSD
		rootShare := 0.0
		if totalCost > 0 {
			rootShare = (rootCost / totalCost) * 100
		}

		lbl1 = "主 Agent"
		target1 = rootCost
		fmt1 = FormatUSD
		sub1 = fmt.Sprintf("%.0f%%", rootShare)

		lbl2 = "子 Agent 合计"
		target2 = subCost
		fmt2 = FormatUSD
		sub2 = fmt.Sprintf("总 %s", FormatUSD(totalCost))

	default:
		lbl1 = "本轮费用"
		target1 = conv.TreeMetrics.TurnCostUSD
		fmt1 = FormatUSD
		sub1 = fmt.Sprintf("/ %s", FormatUSD(conv.TreeMetrics.SessionCostUSD))

		lbl2 = "吐字速度"
		target2 = lastTPS
		fmt2 = FormatTPS
		sub2 = "t/s"
	}

	row := ui.Row(c.Key("tray-heroes")).AlignSelf(ui.Stretch).Gap(14)
	anim1 := float64(row.Spring(fmt.Sprintf("h1-%d", st.Tab), float32(target1), valSpring))
	anim2 := float64(row.Spring(fmt.Sprintf("h2-%d", st.Tab), float32(target2), valSpring))

	row.Children(func() {
		renderHeroCol(c, pal, pal.Accent, lbl1, fmt1(anim1), sub1)
		renderHeroCol(c, pal, pal.Amber, lbl2, fmt2(anim2), sub2)
	})
}

func renderHeroCol(c *ui.Context, pal palette, dotCol ui.Color, label, value, sub string) {
	ui.Column(c).Grow(1).Basis(0).Gap(5).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Box(c).Size(6, 6).Radius(3).Background(dotCol)
			ui.Text(c, label).FontSize(12).TextColor(pal.Ink2).SingleLine()
		})
		ui.Row(c).AlignItems(ui.Baseline).Gap(5).Children(func() {
			ui.Text(c, value).FontSize(22).FontWeight(600).TextColor(pal.Ink1).FontFeatures("tnum").SingleLine()
			ui.Text(c, sub).FontSize(11).TextColor(pal.Ink3).FontFeatures("tnum").SingleLine()
		})
	})
}

func (ctl *Controller) renderDotMatrixBlock(c *ui.Context, pal palette) {
	st := ctl.state

	ui.Column(c.Key("dot-block")).AlignSelf(ui.Stretch).Gap(4).Children(func() {
		// Micro floating inspection pill above dot matrix on column hover.
		_, hoverText := st.HoverSummary(st.HoverDotCol)
		ui.Row(c).AlignSelf(ui.Stretch).Height(18).AlignItems(ui.Center).Justify(ui.End).Children(func() {
			if hoverText != "" {
				ui.Box(c.Key("dot-pill")).Padding(2, 8).Radius(9).
					Background(pal.SegPill).Border(0.75, pal.SegPillBorder).
					Shadow(0, 2, 6, 0, pal.Shadow).
					Children(func() {
						ui.Text(c, hoverText).FontSize(10).FontWeight(500).TextColor(pal.Ink1).FontFeatures("tnum").SingleLine()
					})
			}
		})

		canvasBox := ui.Box(c.Key("dot-matrix")).AlignSelf(ui.Stretch).Height(58).Cursor(ui.CursorCrosshair)
		if px, _, over := canvasBox.PointerPosition(); over {
			bounds := canvasBox.Bounds()
			col := HoverColumn(px, bounds.W)
			if col != st.HoverDotCol {
				st.HoverDotCol = col
				ctl.dotMatrix.markDirty()
			}
		} else if st.HoverDotCol != -1 {
			st.HoverDotCol = -1
			ctl.dotMatrix.markDirty()
		}

		canvasBox.Draw(func(p *ui.Painter, r ui.Rect) {
			ctl.dotMatrix.paint(c, p, r, st, pal)
		})
	})
}

func (ctl *Controller) renderMiddleSection(c *ui.Context, pal palette, conv model.ConversationSnapshot, hasConv bool) {
	st := ctl.state
	ui.Column(c.Key("tray-mid")).AlignSelf(ui.Stretch).Gap(8).Children(func() {
		switch st.Tab {
		case TabPerf:
			ui.Text(c, "缓存命中（单次 vs 总计）").FontSize(11).FontWeight(500).TextColor(pal.Ink3)
			lastHit := 0.0
			if hasConv && conv.TreeMetrics.LatestRequest != nil {
				lastHit = conv.TreeMetrics.LatestRequest.CacheHitRate
			}
			totalHit := conv.TreeMetrics.WeightedCacheHitRate
			renderLabeledBar(c, "perf-last-hit", pal, "最新单次", FormatPct(lastHit), lastHit, pal.Accent, pal.Ink1, pal.Ink1, nil)
			renderLabeledBar(c, "perf-total-hit", pal, "会话总计", FormatPct(totalHit), totalHit, pal.Ink2, pal.Ink2, pal.Ink2, nil)

		case TabAgents:
			ui.Text(c, "费用结构（主 vs 子）").FontSize(11).FontWeight(500).TextColor(pal.Ink3)
			rootRatio := 1.0
			if hasConv && conv.TreeMetrics.SessionCostUSD > 0 {
				rootRatio = conv.Root.Metrics.SessionCostUSD / conv.TreeMetrics.SessionCostUSD
				if rootRatio < 0 {
					rootRatio = 0
				} else if rootRatio > 1 {
					rootRatio = 1
				}
			}
			track := ui.Box(c.Key("agent-split-track")).AlignSelf(ui.Stretch).Height(8).Radius(4).Background(pal.Track)
			animSplit := track.Spring("split", float32(rootRatio), barSpring)
			track.Draw(func(p *ui.Painter, r ui.Rect) {
				if r.W <= 0 {
					return
				}
				clamped := clampFloat32(animSplit, 0, 1)
				leftW := r.W * clamped
				rightW := r.W - leftW
				rad := r.H / 2
				if leftW > 0.5 {
					p.FillRadii(ui.Rect{X: r.X, Y: r.Y, W: leftW, H: r.H}, [4]float32{rad, 0, 0, rad}, pal.Accent)
				}
				if rightW > 0.5 {
					p.FillRadii(ui.Rect{X: r.X + leftW, Y: r.Y, W: rightW, H: r.H}, [4]float32{0, rad, rad, 0}, pal.Amber)
				}
			})

		default:
			ui.Text(c, "上下文窗口").FontSize(11).FontWeight(500).TextColor(pal.Ink3)
			if !hasConv {
				ui.Text(c, "等待会话数据…").FontSize(12).TextColor(pal.Ink3)
				return
			}
			agents := conv.AllAgents()
			limit := min(2, len(agents))
			for i := 0; i < limit; i++ {
				ag := agents[i]
				fill := pal.Accent
				if i > 0 {
					fill = pal.Amber
				}
				switch ContextWarn(ag.ContextRatio, st.Settings.ContextWarnThreshold) {
				case ContextWarning:
					fill = pal.Danger
				case ContextElevated:
					fill = pal.Amber
				}
				rightText := fmt.Sprintf("%s / %s", FormatTokensK(ag.ContextUsed), FormatTokensK(ag.ContextLimit))
				onClick := func() {
					ctl.focusAgent(conv.ID, ag.ID)
				}
				renderLabeledBar(c, "ctx-"+ag.ID, pal, ag.Name, rightText, ag.ContextRatio, fill, pal.Ink1, pal.Ink2, onClick)
			}
		}
	})
}

func renderLabeledBar(c *ui.Context, key string, pal palette, left, right string, ratio float64, barColor, leftCol, rightCol ui.Color, onClick func()) {
	clamped := math.Max(0, math.Min(1, ratio))
	wrap := ui.Column(c.Key(key)).AlignSelf(ui.Stretch).Gap(5)
	if onClick != nil {
		wrap.Cursor(ui.CursorPointer)
	}
	animRatio := wrap.Spring("bar", float32(clamped), barSpring)

	wrap.Children(func() {
		ui.Row(c).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
			ui.Text(c, TruncateRunes(left, 20)).FontSize(12).FontWeight(500).TextColor(leftCol).SingleLine()
			ui.Text(c, right).FontSize(12).TextColor(rightCol).FontFeatures("tnum").SingleLine()
		})
		ui.Box(c).AlignSelf(ui.Stretch).Height(6).Radius(3).Background(pal.Track).Draw(func(p *ui.Painter, r ui.Rect) {
			w := r.W * clampFloat32(animRatio, 0, 1)
			if w > 0.5 {
				p.Fill(ui.Rect{X: r.X, Y: r.Y, W: max(w, r.H), H: r.H}, barColor, r.H/2)
			}
		})
	})

	if onClick != nil && wrap.Clicked() {
		onClick()
	}
}

func (ctl *Controller) renderBottomSection(c *ui.Context, pal palette, conv model.ConversationSnapshot, hasConv bool) {
	st := ctl.state
	ui.Column(c.Key("tray-bot")).AlignSelf(ui.Stretch).Gap(5).Children(func() {
		switch st.Tab {
		case TabPerf:
			ui.Text(c, "最近请求（命中 · TTFT · TPS）").FontSize(11).FontWeight(500).TextColor(pal.Ink3)
			if !hasConv || len(conv.Requests) == 0 {
				ui.Text(c, "暂无请求记录").FontSize(12).TextColor(pal.Ink3).Padding(4, 2)
				return
			}
			n := len(conv.Requests)
			count := min(3, n)
			for i := 0; i < count; i++ {
				r := conv.Requests[n-1-i]
				agName := r.AgentName
				if agName == "" {
					if ag, ok := conv.FindAgent(r.AgentID); ok {
						agName = ag.Name
					}
				}
				hit := r.CacheHitRate
				if hit <= 0 && (r.InputTokens+r.CacheReadTokens+r.CacheWriteTokens) > 0 {
					hit = model.ComputeCacheHitRate(r.InputTokens, r.CacheReadTokens, r.CacheWriteTokens)
				}
				ui.Row(c.Key(fmt.Sprintf("recent-%d", r.Seq))).AlignSelf(ui.Stretch).
					AlignItems(ui.Center).Justify(ui.SpaceBetween).Padding(3, 2).Gap(8).Children(func() {
					ui.Textf(c, "#%d %s", r.Seq, TruncateRunes(agName, 16)).
						FontSize(12).TextColor(pal.Ink2).FontFeatures("tnum").SingleLine()
					ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
						ui.Textf(c, "%.0f%% · %.2fs ·", hit*100, r.TTFT.Seconds()).
							FontSize(12).TextColor(pal.Ink2).FontFeatures("tnum").SingleLine()
						ui.Textf(c, "%.0f", r.DecodeTPS).
							FontSize(12).FontWeight(600).TextColor(pal.Ink1).FontFeatures("tnum").SingleLine()
					})
				})
			}

		case TabAgents:
			ui.Text(c, "节点明细（点击联动主窗）").FontSize(11).FontWeight(500).TextColor(pal.Ink3)
			if !hasConv {
				ui.Text(c, "暂无节点").FontSize(12).TextColor(pal.Ink3).Padding(4, 2)
				return
			}
			agents := conv.AllAgents()
			for idx, ag := range agents {
				ag := ag
				row := ui.ButtonBase(c.Key("agent-row-" + ag.ID)).
					AlignSelf(ui.Stretch).Padding(5, 8).Radius(10).Cursor(ui.CursorPointer)
				if row.Hovered() {
					row.Background(pal.CardHover)
				}
				nameCol := pal.Ink1
				weight := 500
				prefix := ""
				leftPad := float32(0)
				if idx > 0 {
					nameCol = pal.Ink2
					weight = 400
					prefix = "└ "
					leftPad = 10
				}
				row.Children(func() {
					ui.Row(c).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
						ui.Text(c, prefix+TruncateRunes(ag.Name, 18)).
							Padding(0, 0, 0, leftPad).
							FontSize(12).FontWeight(weight).TextColor(nameCol).SingleLine()
						ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
							ui.Text(c, FormatUSD(ag.Metrics.SessionCostUSD)).
								FontSize(12).TextColor(pal.Ink1).FontFeatures("tnum").SingleLine()
							ui.Textf(c, "· %.0f", ag.Metrics.WeightedDecodeTPS).
								FontSize(11).TextColor(pal.Ink3).FontFeatures("tnum").SingleLine()
						})
					})
				})
				if row.Clicked() {
					ctl.focusAgent(conv.ID, ag.ID)
				}
			}

		default:
			ui.Text(c, "实时指标（单次 / 总计）").FontSize(11).FontWeight(500).TextColor(pal.Ink3)
			lastHit := 0.0
			var lastTTFT string
			if hasConv && conv.TreeMetrics.LatestRequest != nil {
				lastHit = conv.TreeMetrics.LatestRequest.CacheHitRate
				lastTTFT = FormatSec(conv.TreeMetrics.LatestRequest.TTFT)
			} else {
				lastTTFT = "0.00s"
			}
			renderMetricPairRow(c, "ov-cache", pal, "缓存命中率", FormatPct(lastHit), "/ "+FormatPct(conv.TreeMetrics.WeightedCacheHitRate))
			renderMetricPairRow(c, "ov-ttft", pal, "首字时长 TTFT", lastTTFT, "/ "+FormatSec(conv.TreeMetrics.AvgTTFT))
		}
	})
}

func renderMetricPairRow(c *ui.Context, key string, pal palette, label, primary, secondary string) {
	ui.Row(c.Key(key)).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Padding(3, 2).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(pal.Ink2).SingleLine()
		ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
			ui.Text(c, primary).FontSize(12).FontWeight(600).TextColor(pal.Ink1).FontFeatures("tnum").SingleLine()
			ui.Text(c, secondary).FontSize(12).TextColor(pal.Ink3).FontFeatures("tnum").SingleLine()
		})
	})
}

func (ctl *Controller) renderFooter(c *ui.Context, pal palette) {
	st := ctl.state

	ui.Box(c).AlignSelf(ui.Stretch).Height(1).Background(pal.Divider)

	var gearAnchor ui.Element
	ui.Row(c.Key("tray-footer")).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
		openBtn := ui.ButtonBase(c.Key("open-desktop-btn")).
			Padding(6, 14).Radius(14).Background(pal.SegBg).Cursor(ui.CursorPointer).
			Label("打开主面板")
		if openBtn.Hovered() {
			openBtn.Background(pal.CardHover)
		}
		openBtn.Children(func() {
			ui.Text(c, "打开主面板").FontSize(12).FontWeight(500).TextColor(pal.Ink1).SingleLine()
		})
		if openBtn.Clicked() {
			ctl.openDesktop()
		}

		gearAnchor = ui.ButtonBase(c.Key("settings-gear-btn")).
			Size(28, 28).Radius(14).Center().Cursor(ui.CursorPointer).
			Label("设置").Tooltip("设置")
		if gearAnchor.Hovered() || st.SettingsOpen {
			gearAnchor.Background(pal.SegBg)
		}
		gearAnchor.Children(func() {
			ui.Icon(c, iconGear).Size(15, 15).TextColor(pal.Ink2)
		})
		if gearAnchor.Clicked() {
			st.SettingsOpen = !st.SettingsOpen
			if st.SettingsOpen {
				st.SessionMenuOpen = false
			}
		}
	})

	// Ultra-minimal frosted Settings Popover (crisp opaque surface, no bleed-through).
	if st.SettingsOpen && gearAnchor.Valid() {
		ui.PopoverBase(c.Key("settings-popover"), gearAnchor, &st.SettingsOpen, func(panel ui.Element) {
			edge := ui.RGBA(15, 23, 42, 0.10)
			if pal.Dark {
				edge = ui.RGBA(255, 255, 255, 0.14)
			}
			panel.AttachTo(gearAnchor, ui.AnchorTopEnd, ui.AnchorBottomEnd).
				Margin(0, 0, 8, 0).Width(246).Radius(18).Padding(14).
				Background(pal.PopoverBg).
				Border(1, edge).
				InsetShadow(0, 1, 0, 0, pal.RimTop).
				Shadow(0, 14, 34, 0, pal.Shadow).
				Children(func() {
					ui.Column(c).Gap(10).Children(func() {
						ui.Row(c).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
							ui.Text(c, "设置").FontSize(12).FontWeight(600).TextColor(pal.Ink1)
							done := ui.ButtonBase(c.Key("settings-done")).Padding(2, 4).Radius(6).Cursor(ui.CursorPointer).
								Children(func() {
									ui.Text(c, "完成").FontSize(12).FontWeight(500).TextColor(pal.Accent)
								})
							if done.Clicked() {
								st.SettingsOpen = false
							}
						})

						ctl.renderSettingSwitch(c, pal, "set-launch", "开机驻留托盘", st.Settings.LaunchAtLogin, func(v bool) {
							ctl.updateSettings(func(s *model.Settings) { s.LaunchAtLogin = v })
						})
						ctl.renderSettingSwitch(c, pal, "set-keep-tray", "关闭退守托盘", st.Settings.KeepInTrayOnClose, func(v bool) {
							ctl.updateSettings(func(s *model.Settings) { s.KeepInTrayOnClose = v })
						})
						ctl.renderSettingSwitch(c, pal, "set-top", "主窗始终置顶", st.Settings.AlwaysOnTop, func(v bool) {
							ctl.updateSettings(func(s *model.Settings) { s.AlwaysOnTop = v })
						})
						ctl.renderSettingSwitch(c, pal, "set-warn", "上下文 80% 预警", st.Settings.ContextWarnThreshold > 0, func(v bool) {
							ctl.updateSettings(func(s *model.Settings) {
								if v {
									s.ContextWarnThreshold = 0.80
								} else {
									s.ContextWarnThreshold = 0
								}
							})
						})
						ctl.renderSettingSwitch(c, pal, "set-follow", "自动跟随活跃", st.Settings.AutoFollowActive, func(v bool) {
							ctl.updateSettings(func(s *model.Settings) { s.AutoFollowActive = v })
						})

						ui.Box(c).AlignSelf(ui.Stretch).Height(1).Background(pal.Divider)

						// Compact Theme Mode selector (System / Light / Dark)
						ui.Row(c).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
							ui.Text(c, "外观").FontSize(12).TextColor(pal.Ink2)
							ui.Row(c).Background(pal.SegBg).Radius(10).Padding(2).Gap(2).Children(func() {
								for _, opt := range []struct {
									mode  string
									label string
								}{
									{"system", "跟随"},
									{"light", "浅色"},
									{"dark", "深色"},
								} {
									opt := opt
									active := st.Settings.ThemeMode == opt.mode
									b := ui.ButtonBase(c.Key("theme-" + opt.mode)).
										Padding(3, 8).Radius(8).Cursor(ui.CursorPointer)
									col := pal.Ink2
									if active {
										b.Background(pal.SegPill)
										col = pal.Ink1
									}
									b.Children(func() {
										ui.Text(c, opt.label).FontSize(11).FontWeight(500).TextColor(col).SingleLine()
									})
									if b.Clicked() && !active {
										ctl.updateSettings(func(s *model.Settings) { s.ThemeMode = opt.mode })
									}
								}
							})
						})

						ui.Box(c).AlignSelf(ui.Stretch).Height(1).Background(pal.Divider)

						// Version & Check for Updates row
						ui.Row(c).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
							verText := appupdater.VersionLabel()
							if avail := appupdater.AvailableVersion(); avail != "" {
								verText = fmt.Sprintf("%s → v%s", verText, avail)
							}
							ui.Text(c, verText).FontSize(11).FontFeatures("tnum").TextColor(pal.Ink3).SingleLine()

							checkBtn := ui.ButtonBase(c.Key("tray-check-updates")).
								Padding(3, 9).Radius(8).Background(pal.SegBg).Cursor(ui.CursorPointer).
								Label("检查更新")
							if checkBtn.Hovered() {
								checkBtn.Background(pal.CardHover)
							}
							checkBtn.Children(func() {
								ui.Text(c, "检查更新").FontSize(11).FontWeight(500).TextColor(pal.Accent).SingleLine()
							})
							if checkBtn.Clicked() {
								st.SettingsOpen = false
								ctl.HideFlyout()
								appupdater.CheckNow()
							}
						})
					})
				})
		})
	}
}

func (ctl *Controller) renderSettingSwitch(c *ui.Context, pal palette, key, label string, current bool, onChange func(bool)) {
	val := current
	ui.Row(c).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(pal.Ink2).SingleLine()
		sw := ui.Switch(c.Key(key), &val).Label(label)
		if sw.Changed() && onChange != nil {
			onChange(val)
		}
	})
}
