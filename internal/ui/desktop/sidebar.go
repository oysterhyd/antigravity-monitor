package desktop

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

const (
	sidebarWidth = 244
	rootRowH     = 48
	subRowH      = 44
	treeRowGap   = 4
)

func (a *App) renderSidebar(c *ui.Context, pal theme.Palette, snap model.AppSnapshot, conv model.ConversationSnapshot, now time.Time) {
	side := theme.GlassSidebar(c.Key("sidebar"), pal, sidebarWidth)

	side.Children(func() {
		// Top section: status bar, session picker, combined toggle, parent-child topology tree.
		ui.Column(c.Key("sidebar-top")).Gap(14).Grow(1).MinHeight(0).Children(func() {
			a.renderSidebarHeader(c, pal, snap, now)
			a.renderSessionSwitcher(c, pal, snap, conv)

			// Combined "整棵会话树" row — cleanly hidden for single-agent conversations.
			if len(conv.Subagents) > 0 {
				a.renderCombinedNode(c, pal, conv)
			}

			a.renderAgentTopologyTree(c, pal, conv)
		})

		// Quiet minimal footer inside sidebar: workspace path + settings gear.
		a.renderSidebarFooter(c, pal, conv)
	})
}

func (a *App) renderSidebarHeader(c *ui.Context, pal theme.Palette, snap model.AppSnapshot, now time.Time) {
	bar := c.TitleBar()
	topInset := float32(0)
	if bar.Left > 0 && bar.Bottom > 0 {
		// On macOS TitleBarHiddenInset, keep clear of native traffic lights.
		topInset = max(0, bar.Bottom-18)
	}

	hdr := ui.Row(c.Key("sidebar-hdr")).
		Padding(topInset, 4, 0, 4).
		Justify(ui.SpaceBetween).
		AlignItems(ui.Center).
		DragWindow()

	hdr.Children(func() {
		// Quiet monochrome brand anchor (no colorful logo tiles).
		ui.Row(c.Key("brand-mark")).Gap(6).AlignItems(ui.Center).Children(func() {
			statusDot(c, "brand-dot", 7, pal.Accent, snap.RunningCount > 0)
			ui.Text(c, "Monitor").FontSize(12).FontWeight(600).TextColor(pal.Ink1).NoWrap()
		})

		// Multi-conversation concurrency badge: ONLY visible when > 1 running conversation.
		if snap.RunningCount > 1 {
			pulseAlpha := a.badgePulse.Alpha(now)
			badge := ui.ButtonBase(c.Key("concurrent-badge")).
				Padding(3, 8).
				Radius(999).
				Cursor(ui.CursorPointer).
				Label(fmt.Sprintf("%d 进行中", snap.RunningCount)).
				Tooltip("切换并行对话")

			if badge.Hovered() || a.sessionPopoverOpen {
				badge.Background(pal.SegBg)
			}
			if pulseAlpha > 0.01 {
				badge.Border(1, pal.Accent.Alpha(pulseAlpha*0.65))
			}

			badge.Children(func() {
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					statusDot(c, "conc-dot", 6, pal.Accent, true)
					ui.Textf(c, "%d 进行中", snap.RunningCount).
						FontSize(11).
						FontWeight(500).
						FontFeatures("tnum").
						TextColor(pal.Ink2).
						NoWrap()
				})
			})
			if badge.Clicked() {
				a.sessionPopoverOpen = !a.sessionPopoverOpen
				a.settingsPopoverOpen = false
			}
		}
	})
}

func (a *App) renderSessionSwitcher(c *ui.Context, pal theme.Palette, snap model.AppSnapshot, conv model.ConversationSnapshot) {
	picker := ui.ButtonBase(c.Key("session-picker")).
		FillWidth().
		Padding(8, 12).
		Radius(16).
		Cursor(ui.CursorPointer).
		Border(1, pal.RimEdge).
		InsetShadow(0, 1, 0, 0, pal.RimTop).
		Label("切换对话")

	bg := pal.CardBg
	if picker.Hovered() || a.sessionPopoverOpen {
		bg = pal.CardHover
	}
	picker.Background(bg)

	followText := "自动跟随活跃对话"
	if !a.settings.AutoFollowActive {
		followText = "已锁定当前对话"
	}

	picker.Children(func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, conv.Title).
					FontSize(12).
					FontWeight(600).
					TextColor(pal.Ink1).
					SingleLine()
				ui.Text(c, followText).
					FontSize(10).
					TextColor(pal.Ink3).
					SingleLine()
			})
			ui.Icon(c, theme.IconChevronDown).Size(13, 13).TextColor(pal.Ink3)
		})
	})

	if picker.Clicked() {
		a.sessionPopoverOpen = !a.sessionPopoverOpen
		a.settingsPopoverOpen = false
	}

	// Floating Liquid Glass Session Popover.
	ui.PopoverBase(c.Key("session-popover"), picker, &a.sessionPopoverOpen, func(panel ui.Element) {
		theme.GlassPopover(panel, pal, sidebarWidth-24).Margin(6, 0, 0, 0).Gap(6)

		// Auto-follow toggle header.
		ui.Row(c.Key("auto-follow-row")).
			Padding(4, 6, 8, 6).
			Justify(ui.SpaceBetween).
			AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).
			BorderColor(pal.Divider).
			Children(func() {
				ui.Text(c, "自动跟随活跃").FontSize(11).TextColor(pal.Ink2).NoWrap()
				sw := ui.Switch(c.Key("auto-follow-sw"), &a.settings.AutoFollowActive).Label("自动跟随活跃")
				if sw.Changed() {
					a.emitSettings()
				}
			})

		// Conversation list.
		ui.Column(c.Key("session-list")).Gap(3).Children(func() {
			for _, item := range snap.Conversations {
				isSel := item.ID == conv.ID
				row := ui.ButtonBase(c.Key("conv-" + item.ID)).
					FillWidth().
					Padding(8, 10).
					Radius(12).
					Cursor(ui.CursorPointer).
					Label(item.Title)

				if isSel {
					row.Background(pal.SegPill).
						InsetShadow(0, 1, 0, 0, pal.RimTop).
						Shadow(0, 2, 8, 0, pal.ShadowPill)
				} else if row.Hovered() {
					row.Background(pal.CardHover)
				}

				subLabel := "单 Agent"
				if len(item.Subagents) > 0 {
					subLabel = fmt.Sprintf("1 主 · %d 子", len(item.Subagents))
				}
				ctxPct := item.Root.ContextRatio * 100

				row.Children(func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
						ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
							dotCol := pal.Ink3
							if item.Running {
								dotCol = pal.Accent
							}
							statusDot(c, "cd-"+item.ID, 6, dotCol, item.Running)
							ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
								ui.Text(c, item.Title).FontSize(12).FontWeight(500).TextColor(pal.Ink1).SingleLine()
								ui.Textf(c, "%s · Ctx %.0f%%", subLabel, ctxPct).
									FontSize(10).
									FontFeatures("tnum").
									TextColor(pal.Ink3).
									SingleLine()
							})
						})
						ui.Text(c, fmtUSD(item.TreeMetrics.SessionCostUSD)).
							FontSize(11.5).
							FontWeight(500).
							FontFeatures("tnum").
							TextColor(pal.Ink2).
							NoWrap()
					})
				})

				if row.Clicked() {
					a.FocusConversation(item.ID, true)
				}
			}
		})
	})
}

func (a *App) renderCombinedNode(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot) {
	active := a.scope == scopeAll
	row := ui.ButtonBase(c.Key("node-combined")).
		FillWidth().
		Padding(9, 11).
		Radius(14).
		Cursor(ui.CursorPointer).
		Label("整棵会话树")

	if active {
		row.Background(pal.SegPill).
			InsetShadow(0, 1, 0, 0, pal.RimTop).
			Shadow(0, 2, 10, 0, pal.ShadowPill)
	} else if row.Hovered() {
		row.Background(pal.CardHover)
	}

	iconCol := pal.Ink2
	if active {
		iconCol = pal.Accent
	}

	row.Children(func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Gap(9).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, theme.IconTree).Size(15, 15).TextColor(iconCol)
				ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
					ui.Text(c, "整棵会话树").FontSize(12).FontWeight(600).TextColor(pal.Ink1).SingleLine()
					ui.Text(c, "合并统计").FontSize(10.5).TextColor(pal.Ink3).SingleLine()
				})
			})
			ui.Text(c, fmtUSD(conv.TreeMetrics.SessionCostUSD)).
				FontSize(12).
				FontWeight(600).
				FontFeatures("tnum").
				TextColor(pal.Ink1).
				NoWrap()
		})
	})

	if row.Clicked() {
		a.FocusAgent(scopeAll)
	}
}

func (a *App) renderAgentTopologyTree(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot) {
	subs := conv.Subagents
	countLabel := "仅主 Agent"
	if len(subs) > 0 {
		countLabel = fmt.Sprintf("1 主 · %d 子", len(subs))
	}

	ui.Column(c.Key("agent-tree-section")).Gap(6).Grow(1).MinHeight(0).Children(func() {
		ui.Row(c).Padding(0, 6).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "父子 Agent 结构").FontSize(11).TextColor(pal.Ink3).NoWrap()
			ui.Text(c, countLabel).FontSize(11).FontFeatures("tnum").TextColor(pal.Ink3).NoWrap()
		})

		ui.Scroll(c.Key("tree-scroll")).Grow(1).MinHeight(0).Children(func() {
			wrap := ui.Column(c.Key("tree-wrap")).Gap(treeRowGap).FillWidth()

			// Curved SVG-style parent-child connectors with live animated flow pulse on active branches.
			if len(subs) > 0 {
				activeID := conv.ActiveAgentID
				selectedScope := a.scope
				running := conv.Running
				subIDs := make([]string, len(subs))
				for i, s := range subs {
					subIDs[i] = s.ID
				}
				strokeBase := pal.TreeStroke
				strokeAccent := pal.Accent

				wrap.Draw(func(p *ui.Painter, r ui.Rect) {
					const startX = float32(14)
					const startY = float32(rootRowH - 6)
					const endX = float32(22)

					hasAnimatedBranch := false
					for _, sid := range subIDs {
						if (running && sid == activeID) || sid == selectedScope {
							hasAnimatedBranch = true
							break
						}
					}

					var dashOffset float32
					if hasAnimatedBranch && running && !p.Preferences().ReduceMotion {
						p.AnimationFrameRate(30)
						// 1.4s smooth dash flow matching prototype.html flowDash (-24 period).
						phase := float64(p.Now().UnixMilli()%1400) / 1400.0
						dashOffset = -float32(phase * 24.0)
					}

					for i, sid := range subIDs {
						rowTop := float32(rootRowH+treeRowGap) + float32(i)*(subRowH+treeRowGap)
						endY := rowTop + subRowH/2

						path := (&ui.Path{}).
							MoveTo(r.X+startX, r.Y+startY).
							LineTo(r.X+startX, r.Y+endY-8).
							QuadTo(r.X+startX, r.Y+endY, r.X+startX+8, r.Y+endY).
							LineTo(r.X+endX, r.Y+endY).
							Cap(ui.RoundCap)

						p.StrokePath(path, 1.35, strokeBase)

						isBranchHot := (running && sid == activeID) || sid == selectedScope
						if isBranchHot {
							if running && !p.Preferences().ReduceMotion {
								flowPath := (&ui.Path{}).
									MoveTo(r.X+startX, r.Y+startY).
									LineTo(r.X+startX, r.Y+endY-8).
									QuadTo(r.X+startX, r.Y+endY, r.X+startX+8, r.Y+endY).
									LineTo(r.X+endX, r.Y+endY).
									Cap(ui.RoundCap).
									Dash(dashOffset, 4, 20)
								p.StrokePath(flowPath, 1.75, strokeAccent)
							} else {
								p.StrokePath(path, 1.6, strokeAccent.Alpha(0.65))
							}
						}
					}
				})
			}

			wrap.Children(func() {
				// Root Agent node.
				root := conv.Root
				isRootSelected := a.scope == root.ID || (len(subs) == 0 && a.scope == scopeAll)
				isRootHot := conv.Running && conv.ActiveAgentID == root.ID
				a.renderAgentRow(c, pal, root, true, isRootSelected, isRootHot)

				// Indented Subagent nodes.
				for _, sub := range subs {
					isSel := a.scope == sub.ID
					isHot := conv.Running && conv.ActiveAgentID == sub.ID
					a.renderAgentRow(c, pal, sub, false, isSel, isHot)
				}
			})
		})
	})
}

func (a *App) renderAgentRow(c *ui.Context, pal theme.Palette, ag model.AgentNode, isRoot bool, selected bool, hot bool) {
	h := float32(subRowH)
	leftMargin := float32(20)
	dotSize := float32(6)
	titleWeight := 500
	if isRoot {
		h = rootRowH
		leftMargin = 0
		dotSize = 7.5
		titleWeight = 600
	}

	row := ui.ButtonBase(c.Key("agent-" + ag.ID)).
		Height(h).
		Padding(0, 10).
		Radius(14).
		Cursor(ui.CursorPointer).
		Label(ag.Name)
	if leftMargin > 0 {
		row.Margin(0, 0, 0, leftMargin)
	}

	if selected {
		row.Background(pal.SegPill).
			InsetShadow(0, 1, 0, 0, pal.RimTop).
			Shadow(0, 2, 10, 0, pal.ShadowPill)
	} else if row.Hovered() {
		row.Background(pal.CardHover)
	}

	dotCol := pal.Ink3
	if selected || hot {
		dotCol = pal.Accent
	}

	subLine := ""
	if isRoot {
		subLine = fmt.Sprintf("%s · %.1f%%", ag.ModelDisplay, ag.ContextRatio*100)
	} else {
		subLine = fmt.Sprintf("%.1f%% · %.0f t/s", ag.ContextRatio*100, math.Round(ag.Metrics.WeightedDecodeTPS))
	}

	row.Children(func() {
		ui.Row(c).Fill().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
				statusDot(c, "ad-"+ag.ID, dotSize, dotCol, hot)
				ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
					ui.Text(c, ag.Name).FontSize(12).FontWeight(titleWeight).TextColor(pal.Ink1).SingleLine()
					ui.Text(c, subLine).FontSize(10.5).FontFeatures("tnum").TextColor(pal.Ink3).SingleLine()
				})
			})
			ui.Text(c, fmtUSD(ag.Metrics.SessionCostUSD)).
				FontSize(11.5).
				FontWeight(500).
				FontFeatures("tnum").
				TextColor(pal.Ink2).
				NoWrap()
		})
	})

	if row.Clicked() {
		a.FocusAgent(ag.ID)
	}
}

func (a *App) renderSidebarFooter(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot) {
	ui.Row(c.Key("sidebar-footer")).
		Padding(10, 4, 0, 4).
		BorderWidth(1, 0, 0, 0).
		BorderColor(pal.Divider).
		Justify(ui.SpaceBetween).
		AlignItems(ui.Center).
		Gap(8).
		Children(func() {
			ui.Text(c, conv.Workspace).
				Grow(1).
				MinWidth(0).
				FontSize(11).
				TextColor(pal.Ink3).
				SingleLine()

			gear := ui.ButtonBase(c.Key("settings-btn")).
				Size(26, 26).
				Radius(8).
				Center().
				Cursor(ui.CursorPointer).
				Label("设置").
				Tooltip("设置")

			if gear.Hovered() || a.settingsPopoverOpen {
				gear.Background(pal.CardHover)
			}
			gear.Children(func() {
				ui.Icon(c, theme.IconSettings).Size(14, 14).TextColor(pal.Ink2)
			})
			if gear.Clicked() {
				a.settingsPopoverOpen = !a.settingsPopoverOpen
				a.sessionPopoverOpen = false
			}

			// Ultra-minimal Apple Liquid Glass Settings Popover (no redundant TPS mode toggle).
			ui.PopoverBase(c.Key("settings-popover"), gear, &a.settingsPopoverOpen, func(panel ui.Element) {
				theme.GlassPopover(panel, pal, 244).Margin(0, 0, 8, 0).Padding(14).Gap(12)

				ui.Row(c).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "设置").FontSize(12.5).FontWeight(600).TextColor(pal.Ink1)
					doneBtn := ui.ButtonBase(c.Key("settings-done")).Cursor(ui.CursorPointer).Label("完成")
					doneBtn.Children(func() {
						ui.Text(c, "完成").FontSize(12).FontWeight(500).TextColor(pal.Accent)
					})
					if doneBtn.Clicked() {
						a.settingsPopoverOpen = false
					}
				})

				// Theme mode segmented selector (System / Light / Dark).
				modeIdx := 0
				switch a.settings.ThemeMode {
				case theme.ModeLight:
					modeIdx = 1
				case theme.ModeDark:
					modeIdx = 2
				}
				if theme.SegmentedPill(c, pal, "settings-theme-seg", &modeIdx, []string{"系统", "浅色", "深色"}, theme.SegmentedOptions{
					Height:   28,
					FontSize: 11,
					PaddingX: 6,
				}) {
					modes := []string{theme.ModeSystem, theme.ModeLight, theme.ModeDark}
					a.SetThemeMode(modes[modeIdx])
				}

				a.renderSettingSwitchRow(c, pal, "set-tray", "关闭主窗驻留托盘", &a.settings.KeepInTrayOnClose, func() {
					a.emitSettings()
				})
				a.renderSettingSwitchRow(c, pal, "set-ontop", "主窗始终置顶", &a.settings.AlwaysOnTop, func() {
					a.toggleAlwaysOnTop()
				})

				warnEnabled := a.settings.ContextWarnThreshold <= 0.85
				a.renderSettingSwitchRow(c, pal, "set-warn", "上下文 80% 预警", &warnEnabled, func() {
					if warnEnabled {
						a.settings.ContextWarnThreshold = 0.80
					} else {
						a.settings.ContextWarnThreshold = 0.95
					}
					a.emitSettings()
				})
			})
		})
}

func (a *App) renderSettingSwitchRow(c *ui.Context, pal theme.Palette, key string, label string, val *bool, onChange func()) {
	ui.Row(c.Key(key)).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(pal.Ink2).NoWrap()
		if ui.Switch(c.Key(key+"-sw"), val).Label(label).Changed() && onChange != nil {
			onChange()
		}
	})
}
