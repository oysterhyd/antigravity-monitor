package desktop

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/i18n"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
	appupdater "github.com/oysterhyd/antigravity-monitor/internal/updater"
)

const (
	sidebarWidth = 244
	rootRowH     = 48
	subRowH      = 44
	treeRowGap   = 4
)

func (a *App) renderSidebar(c *ui.Context, pal theme.Palette, snap model.AppSnapshot, conv model.ConversationSnapshot, hasConv bool, now time.Time) {
	sideW := a.layout.sidebarW
	if sideW <= 0 {
		sideW = sidebarWidth
	}
	side := theme.GlassSidebar(c.Key("sidebar"), pal, sideW)

	side.Children(func() {
		// Top section: status bar, session picker, combined toggle, parent-child topology tree.
		ui.Column(c.Key("sidebar-top")).Gap(11).Grow(1).MinHeight(0).Children(func() {
			a.renderSidebarHeader(c, pal, snap, now)
			if hasConv {
				a.renderSessionSwitcher(c, pal, snap, conv)

				// Combined "整棵会话树" row — cleanly hidden for single-agent conversations.
				if len(conv.Subagents) > 0 {
					a.renderCombinedNode(c, pal, conv)
				}

				a.renderAgentTopologyTree(c, pal, conv)
			} else {
				tr := a.tr()
				ui.Box(c.Key("sidebar-no-conv")).Padding(10, 6).Children(func() {
					ui.Text(c, tr.NoActiveSession).FontSize(11.5).TextColor(pal.Ink3)
				})
			}
		})

		// Quiet minimal footer inside sidebar: workspace path + Quota / History / Settings buttons.
		a.renderSidebarFooter(c, pal, conv, now)
	})
}

func (a *App) renderSidebarHeader(c *ui.Context, pal theme.Palette, snap model.AppSnapshot, now time.Time) {
	tr := a.tr()
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
			badgeText := fmt.Sprintf(tr.RunningBadge, snap.RunningCount)
			badge := ui.ButtonBase(c.Key("concurrent-badge")).
				Padding(3, 8).
				Radius(999).
				Cursor(ui.CursorPointer).
				Label(badgeText).
				Tooltip(tr.SwitchParallel)

			if badge.Hovered() || a.sessionPopoverOpen {
				badge.Background(pal.SegBg)
			}
			if pulseAlpha > 0.01 {
				badge.Border(1, pal.Accent.Alpha(pulseAlpha*0.65))
			}

			badge.Children(func() {
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					statusDot(c, "conc-dot", 6, pal.Accent, true)
					ui.Text(c, badgeText).
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
	tr := a.tr()
	picker := ui.ButtonBase(c.Key("session-picker")).
		FillWidth().
		Padding(10, 12).
		Radius(14).
		Cursor(ui.CursorPointer).
		Border(1, pal.RimEdge).
		InsetShadow(0, 1, 0, 0, pal.RimTop).
		Label(tr.SwitchSession)

	bg := pal.CardBg
	if picker.Hovered() || a.sessionPopoverOpen {
		bg = pal.CardHover
	}
	picker.Background(bg)

	picker.Children(func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, conv.Title).
				Grow(1).
				MinWidth(0).
				FontSize(12).
				FontWeight(600).
				TextColor(pal.Ink1).
				SingleLine()
			ui.Icon(c, theme.IconChevronDown).Size(13, 13).TextColor(pal.Ink3).Shrink(0)
		})
	})

	if picker.Clicked() {
		a.sessionPopoverOpen = !a.sessionPopoverOpen
		a.settingsPopoverOpen = false
	}

	// Floating Liquid Glass Session Popover (wider, crisp opaque surface, bounded scrollable list).
	ui.PopoverBase(c.Key("session-popover"), picker, &a.sessionPopoverOpen, func(panel ui.Element) {
		theme.GlassPopover(panel, pal, 286).Margin(6, 0, 0, 0).Gap(6)

		// Auto-follow toggle header.
		ui.Row(c.Key("auto-follow-row")).
			Padding(4, 8, 8, 8).
			Justify(ui.SpaceBetween).
			AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).
			BorderColor(pal.Divider).
			Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, tr.AutoFollowShort).FontSize(11.5).FontWeight(500).TextColor(pal.Ink2).NoWrap()
					if len(snap.Conversations) > 0 {
						ui.Textf(c, tr.ConvCountFmt, len(snap.Conversations)).
							FontSize(10.5).FontFeatures("tnum").TextColor(pal.Ink3).NoWrap()
					}
				})
				sw := ui.Switch(c.Key("auto-follow-sw"), &a.settings.AutoFollowActive).Label(tr.AutoFollowShort)
				if sw.Changed() {
					a.emitSettings()
				}
			})

		// Bounded scrollable conversation list so many sessions never clip off the window bottom.
		listH := float32(len(snap.Conversations))*49 + 6
		if listH < 56 {
			listH = 56
		}
		if listH > 344 {
			listH = 344
		}

		ui.Scroll(c.Key("session-scroll")).
			FillWidth().
			Height(listH).
			Children(func() {
				ui.Column(c.Key("session-list")).FillWidth().Padding(2, 2, 4, 2).Gap(3).Children(func() {
					if len(snap.Conversations) == 0 {
						ui.Text(c, tr.NoLocalSessions).FontSize(11).TextColor(pal.Ink3).Padding(10, 8)
						return
					}
					for _, item := range snap.Conversations {
						isSel := item.ID == conv.ID
						row := ui.ButtonBase(c.Key("conv-"+item.ID)).
							FillWidth().
							Padding(8, 10).
							Radius(11).
							Cursor(ui.CursorPointer).
							Label(item.Title)

						if isSel {
							row.Background(pal.AccentSoft).
								Border(1, pal.Accent.Alpha(0.24))
						} else if row.Hovered() {
							row.Background(pal.SegBg)
						}

						subLabel := tr.SubagentSummary(len(item.Subagents))
						ctxRatio := item.Root.ContextRatio
						if item.PeakContext.Ratio > ctxRatio {
							ctxRatio = item.PeakContext.Ratio
						}
						ctxPct := ctxRatio * 100

						titleWeight := 500
						if isSel {
							titleWeight = 600
						}

						row.Children(func() {
							ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
								ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
									dotCol := pal.Ink3.Alpha(0.45)
									if isSel || item.Running {
										dotCol = pal.Accent
									}
									statusDot(c, "cd-"+item.ID, 6.5, dotCol, item.Running)
									ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
										ui.Text(c, item.Title).FontSize(12).FontWeight(titleWeight).TextColor(pal.Ink1).SingleLine()
										ui.Textf(c, "%s · Ctx %.0f%%", subLabel, ctxPct).
											FontSize(10).
											FontFeatures("tnum").
											TextColor(pal.Ink3).
											SingleLine()
									})
								})
								costCol := pal.Ink2
								if isSel {
									costCol = pal.Ink1
								}
								ui.Text(c, fmtUSD(item.TreeMetrics.SessionCostUSD)).
									FontSize(11.5).
									FontWeight(titleWeight).
									FontFeatures("tnum").
									TextColor(costCol).
									NoWrap()
							})
						})

						if row.Clicked() {
							a.FocusConversation(item.ID, true)
						}
					}
				})
			})
	})
}

func (a *App) renderCombinedNode(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot) {
	tr := a.tr()
	active := a.activeView == ViewLive && a.scope == scopeAll
	row := ui.ButtonBase(c.Key("node-combined")).
		FillWidth().
		Padding(10, 11).
		Radius(14).
		Cursor(ui.CursorPointer).
		Label(tr.WholeTree)

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
				ui.Icon(c, theme.IconTree).Size(15, 15).TextColor(iconCol).Shrink(0)
				ui.Text(c, tr.WholeTree).Grow(1).MinWidth(0).FontSize(12).FontWeight(600).TextColor(pal.Ink1).SingleLine()
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
	tr := a.tr()
	subs := conv.Subagents
	countLabel := tr.RootOnly
	if len(subs) > 0 {
		countLabel = fmt.Sprintf(tr.RootAndSubs, len(subs))
	}

	ui.Column(c.Key("agent-tree-section")).Gap(6).Grow(1).MinHeight(0).Children(func() {
		ui.Row(c).Padding(0, 6).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
			ui.Text(c, tr.AgentStructure).FontSize(11).TextColor(pal.Ink3).NoWrap()
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

					// Stroke the shared vertical trunk ONCE so semi-transparent gray never
					// composites N times into solid black when a conversation has many subagents.
					lastEndY := float32(rootRowH+treeRowGap) + float32(len(subIDs)-1)*(subRowH+treeRowGap) + subRowH/2
					trunk := (&ui.Path{}).
						MoveTo(r.X+startX, r.Y+startY).
						LineTo(r.X+startX, r.Y+lastEndY-8).
						Cap(ui.RoundCap)
					p.StrokePath(trunk, 1.35, strokeBase)

					for i, sid := range subIDs {
						rowTop := float32(rootRowH+treeRowGap) + float32(i)*(subRowH+treeRowGap)
						endY := rowTop + subRowH/2

						elbow := (&ui.Path{}).
							MoveTo(r.X+startX, r.Y+endY-8).
							QuadTo(r.X+startX, r.Y+endY, r.X+startX+8, r.Y+endY).
							LineTo(r.X+endX, r.Y+endY).
							Cap(ui.RoundCap)
						p.StrokePath(elbow, 1.35, strokeBase)

						isBranchHot := (running && sid == activeID) || sid == selectedScope
						if isBranchHot {
							fullPath := (&ui.Path{}).
								MoveTo(r.X+startX, r.Y+startY).
								LineTo(r.X+startX, r.Y+endY-8).
								QuadTo(r.X+startX, r.Y+endY, r.X+startX+8, r.Y+endY).
								LineTo(r.X+endX, r.Y+endY).
								Cap(ui.RoundCap)
							if running && !p.Preferences().ReduceMotion {
								p.StrokePath(fullPath.Dash(dashOffset, 4, 20), 1.75, strokeAccent)
							} else {
								p.StrokePath(fullPath, 1.6, strokeAccent.Alpha(0.65))
							}
						}
					}
				})
			}

			wrap.Children(func() {
				// Root Agent node.
				root := conv.Root
				isRootSelected := a.activeView == ViewLive && (a.scope == root.ID || (len(subs) == 0 && a.scope == scopeAll))
				isRootHot := conv.Running && conv.ActiveAgentID == root.ID
				a.renderAgentRow(c, pal, root, true, isRootSelected, isRootHot)

				// Indented Subagent nodes.
				for _, sub := range subs {
					isSel := a.activeView == ViewLive && a.scope == sub.ID
					isHot := conv.Running && conv.ActiveAgentID == sub.ID
					a.renderAgentRow(c, pal, sub, false, isSel, isHot)
				}
			})
		})
	})
}

func (a *App) renderAgentRow(c *ui.Context, pal theme.Palette, ag model.AgentNode, isRoot bool, selected bool, hot bool) {
	tr := a.tr()
	dispName := tr.AgentName(ag.ID, ag.Name)
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

	row := ui.ButtonBase(c.Key("agent-"+ag.ID)).
		Height(h).
		Padding(0, 10).
		Radius(14).
		Cursor(ui.CursorPointer).
		Label(dispName)
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
					ui.Text(c, dispName).FontSize(12).FontWeight(titleWeight).TextColor(pal.Ink1).SingleLine()
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

func (a *App) renderSidebarFooter(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, now time.Time) {
	tr := a.tr()
	hasLowQuota := a.quotaSvc != nil && a.quotaSvc.HasLowQuota()

	ui.Row(c.Key("sidebar-footer")).
		Padding(10, 2, 0, 4).
		BorderWidth(1, 0, 0, 0).
		BorderColor(pal.Divider).
		Justify(ui.SpaceBetween).
		AlignItems(ui.Center).
		Gap(6).
		Children(func() {
			ui.Text(c, conv.Workspace).
				Grow(1).
				MinWidth(0).
				FontSize(11).
				TextColor(pal.Ink3).
				SingleLine()

			// Right: 3 monochrome SF-style icon buttons [Quota] [History] [Settings].
			ui.Row(c.Key("sidebar-footer-actions")).Gap(3).AlignItems(ui.Center).Shrink(0).Children(func() {
				// 1. Account Quota button (Gauge icon + quiet amber dot when < 25%).
				quotaActive := a.activeView == ViewQuota
				quotaBtn := ui.ButtonBase(c.Key("quota-nav-btn")).
					Size(26, 26).
					Radius(8).
					Center().
					Cursor(ui.CursorPointer).
					Label(tr.QuotaTitle).
					Tooltip(tr.QuotaTitle)

				quotaIconCol := pal.Ink2
				if quotaActive {
					quotaBtn.Background(pal.AccentSoft).Border(1, pal.Accent.Alpha(0.24))
					quotaIconCol = pal.Accent
				} else if quotaBtn.Hovered() {
					quotaBtn.Background(pal.CardHover)
					quotaIconCol = pal.Ink1
				}
				if hasLowQuota {
					amber := pal.Amber
					quotaBtn.DrawOver(func(p *ui.Painter, r ui.Rect) {
						p.FillPath((&ui.Path{}).Circle(r.X+r.W-5, r.Y+5, 2.5), amber)
					})
				}
				quotaBtn.Children(func() {
					ui.Icon(c, theme.IconGauge).Size(14, 14).TextColor(quotaIconCol)
				})
				if quotaBtn.Clicked() {
					a.ToggleSubView(ViewQuota)
				}

				// 2. Usage History button (3-Bar Chart icon).
				histActive := a.activeView == ViewHistory
				histBtn := ui.ButtonBase(c.Key("history-nav-btn")).
					Size(26, 26).
					Radius(8).
					Center().
					Cursor(ui.CursorPointer).
					Label(tr.HistoryTitle).
					Tooltip(tr.HistoryTitle)

				histIconCol := pal.Ink2
				if histActive {
					histBtn.Background(pal.AccentSoft).Border(1, pal.Accent.Alpha(0.24))
					histIconCol = pal.Accent
				} else if histBtn.Hovered() {
					histBtn.Background(pal.CardHover)
					histIconCol = pal.Ink1
				}
				histBtn.Children(func() {
					ui.Icon(c, theme.IconBarChart).Size(14, 14).TextColor(histIconCol)
				})
				if histBtn.Clicked() {
					a.ToggleSubView(ViewHistory)
				}

				// 3. Settings gear button.
				gear := ui.ButtonBase(c.Key("settings-btn")).
					Size(26, 26).
					Radius(8).
					Center().
					Cursor(ui.CursorPointer).
					Label(tr.Settings).
					Tooltip(tr.Settings)

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
					theme.GlassPopover(panel, pal, 252).Margin(0, 0, 8, 0).Padding(14).Gap(11)

					ui.Row(c).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
						ui.Text(c, tr.Settings).FontSize(12.5).FontWeight(600).TextColor(pal.Ink1)
						doneBtn := ui.ButtonBase(c.Key("settings-done")).Cursor(ui.CursorPointer).Label(tr.Done)
						doneBtn.Children(func() {
							ui.Text(c, tr.Done).FontSize(12).FontWeight(500).TextColor(pal.Accent)
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
					if theme.SegmentedPill(c, pal, "settings-theme-seg", &modeIdx, []string{tr.ThemeSystem, tr.ThemeLight, tr.ThemeDark}, theme.SegmentedOptions{
						Height:   28,
						FontSize: 11,
						PaddingX: 6,
					}) {
						modes := []string{theme.ModeSystem, theme.ModeLight, theme.ModeDark}
						a.SetThemeMode(modes[modeIdx])
					}

					// Language selector row (中文 / English).
					ui.Row(c.Key("set-lang-row")).Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
						ui.Text(c, tr.LanguageLabel).FontSize(12).TextColor(pal.Ink2).NoWrap()
						langIdx := 0
						if i18n.Normalize(a.settings.Language) == i18n.LangEN {
							langIdx = 1
						}
						if theme.SegmentedPill(c, pal, "settings-lang-seg", &langIdx, []string{"中文", "EN"}, theme.SegmentedOptions{
							Width:    112,
							Height:   26,
							FontSize: 11,
							PaddingX: 8,
						}) {
							if langIdx == 1 {
								a.SetLanguage(i18n.LangEN)
							} else {
								a.SetLanguage(i18n.LangZH)
							}
						}
					})

					a.renderSettingSwitchRow(c, pal, "set-launch", tr.LaunchAtLogin, &a.settings.LaunchAtLogin, func() {
						a.emitSettings()
					})
					a.renderSettingSwitchRow(c, pal, "set-tray", tr.KeepInTray, &a.settings.KeepInTrayOnClose, func() {
						a.emitSettings()
					})
					a.renderSettingSwitchRow(c, pal, "set-ontop", tr.AlwaysOnTop, &a.settings.AlwaysOnTop, func() {
						a.toggleAlwaysOnTop()
					})

					warnEnabled := a.settings.ContextWarnThreshold > 0
					a.renderSettingSwitchRow(c, pal, "set-warn", tr.ContextWarn80, &warnEnabled, func() {
						if warnEnabled {
							a.settings.ContextWarnThreshold = 0.80
						} else {
							a.settings.ContextWarnThreshold = 0
						}
						a.emitSettings()
					})

					ui.Box(c).FillWidth().Height(1).Background(pal.Divider)

					// Quiet version + Check for Updates row
					ui.Row(c.Key("desk-update-row")).Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
						verText := appupdater.VersionLabel()
						if avail := appupdater.AvailableVersion(); avail != "" {
							verText = fmt.Sprintf("%s → v%s", verText, avail)
						}
						ui.Text(c, verText).FontSize(11).FontFeatures("tnum").TextColor(pal.Ink3).SingleLine()

						checkBtn := ui.ButtonBase(c.Key("desk-check-updates")).
							Padding(4, 10).Radius(8).Background(pal.SegBg).Cursor(ui.CursorPointer).
							Label(tr.CheckUpdates)
						if checkBtn.Hovered() {
							checkBtn.Background(pal.CardHover)
						}
						checkBtn.Children(func() {
							ui.Text(c, tr.CheckUpdates).FontSize(11).FontWeight(500).TextColor(pal.Accent).SingleLine()
						})
						if checkBtn.Clicked() {
							a.settingsPopoverOpen = false
							appupdater.CheckNow()
						}
					})
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
