package desktop

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

func (a *App) renderTelemetryCard(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, vm scopeView) {
	tr := a.tr()
	lm := a.layout
	card := theme.GlassCard(c.Key("telemetry-card"), pal, theme.CardOptions{
		Radius: 20,
	}).Padding(lm.cardPadY, lm.cardPadX, lm.cardPadY, lm.cardPadX).Gap(8 + 2*lm.scale).Grow(1).MinHeight(148)

	card.Children(func() {
		// Card Header: dynamic metric heading + spring "Curve | List" segmented pill.
		ui.Row(c.Key("telemetry-hdr")).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, metricTitle(tr, a.metric)).
				FontSize(12).
				FontWeight(600).
				TextColor(pal.Ink1).
				SingleLine()

			viewIdx := int(a.reqView)
			if theme.SegmentedPill(c, pal, "req-view-seg", &viewIdx, []string{tr.ViewCurve, tr.ViewList}, theme.SegmentedOptions{
				Width:    112,
				Height:   28,
				FontSize: 11,
				PaddingX: 8,
			}) {
				a.SetReqView(ReqViewMode(viewIdx))
			}
		})

		if a.reqView == ReqViewCurve {
			a.renderCurvePane(c, pal, conv, vm)
		} else {
			a.renderRequestListPane(c, pal, conv, vm.requests)
		}
	})
}

func (a *App) inspectedRequest(reqs []model.RequestRecord) (req model.RequestRecord, idx int, slice []model.RequestRecord) {
	slice = tailRequests(reqs, chartWindowLen)
	if len(slice) == 0 {
		return model.RequestRecord{}, -1, slice
	}
	if a.hoverChartIdx >= 0 && a.hoverChartIdx < len(slice) {
		return slice[a.hoverChartIdx], a.hoverChartIdx, slice
	}
	if a.pinnedReqSeq > 0 {
		for i, r := range slice {
			if r.Seq == a.pinnedReqSeq {
				return r, i, slice
			}
		}
	}
	lastIdx := len(slice) - 1
	return slice[lastIdx], lastIdx, slice
}

func (a *App) stepInspect(reqs []model.RequestRecord, delta int) {
	_, idx, slice := a.inspectedRequest(reqs)
	if len(slice) == 0 || idx < 0 {
		return
	}
	next := idx + delta
	if next < 0 {
		next = 0
	}
	if next >= len(slice) {
		next = len(slice) - 1
	}
	if next == len(slice)-1 {
		a.pinnedReqSeq = 0
	} else {
		a.pinnedReqSeq = slice[next].Seq
	}
}

func (a *App) renderCurvePane(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, vm scopeView) {
	tr := a.tr()
	reqs := vm.requests
	slice := tailRequests(reqs, chartWindowLen)

	ui.Column(c.Key("pane-curve")).Grow(1).MinHeight(0).Justify(ui.SpaceBetween).Gap(6).Children(func() {
		// 60fps Morphing Curve Canvas built on ui/chart.
		plotBox := ui.Box(c.Key("morph-chart-box")).
			FillWidth().
			Grow(1).
			MinHeight(68).
			Cursor(ui.CursorCrosshair).
			Label(metricTitle(tr, a.metric))

		// Pointer hover & click pinning over chart points.
		if len(slice) > 0 {
			n := len(slice)
			xs := a.chartXs[:n]
			for i := 0; i < n; i++ {
				if n == 1 {
					xs[i] = 0.5
				} else {
					xs[i] = float32(i) / float32(n-1)
				}
			}

			if px, _, over := plotBox.PointerPosition(); over && plotBox.Hovered() {
				b := plotBox.Bounds()
				const padX = float32(10)
				plotW := b.W - padX*2
				if plotW > 0 {
					frac := (px - padX) / plotW
					if frac < 0 {
						frac = 0
					}
					if frac > 1 {
						frac = 1
					}
					nearest := chart.Nearest(xs, frac)
					if nearest >= 0 && nearest < n {
						a.hoverChartIdx = nearest
						a.morpher.SetCrosshairTarget(float64(xs[nearest]), false)
					}
				}
			} else if a.hoverChartIdx != -1 {
				a.hoverChartIdx = -1
			}

			if plotBox.Clicked() && a.hoverChartIdx >= 0 && a.hoverChartIdx < n {
				a.pinnedReqSeq = slice[a.hoverChartIdx].Seq
			}

			_, activeIdx, _ := a.inspectedRequest(reqs)
			drawYs := a.morpher.Tail(a.chartYs[:0], n)
			crossFrac, hasCross := a.morpher.CrosshairFrac()

			strokeCol := pal.Accent
			fillTop := pal.Accent.Alpha(0.22)
			haloCol := pal.Accent.Alpha(0.18)
			if a.metric == MetricTTFT {
				strokeCol = pal.Amber
				fillTop = pal.Amber.Alpha(0.22)
				haloCol = pal.Amber.Alpha(0.18)
			}

			dotMuted := ui.RGBA(17, 17, 19, 0.22)
			crossCol := ui.RGBA(17, 17, 19, 0.16)
			dotRing := ui.RGB(255, 255, 255)
			if pal.Dark {
				dotMuted = ui.RGBA(255, 255, 255, 0.30)
				crossCol = ui.RGBA(255, 255, 255, 0.24)
				dotRing = ui.Hex("#181B24")
			}

			plotBox.Draw(func(p *ui.Painter, r ui.Rect) {
				const padX = float32(10)
				const padT = float32(14)
				const padB = float32(12)
				inner := ui.Rect{
					X: r.X + padX,
					Y: r.Y + padT,
					W: r.W - padX*2,
					H: r.H - padT - padB,
				}
				if inner.W <= 4 || inner.H <= 4 || len(xs) == 0 {
					return
				}

				// Soft area wash beneath curve.
				if len(xs) >= 2 {
					areaPath := chart.Area(inner, xs, nil, drawYs, chart.CurveMonotone)
					p.FillPathGradient(areaPath, ui.LinearGradient{
						From:  fillTop,
						To:    ui.Transparent,
						Angle: 180,
					})
					// Luminous smooth monotone curve.
					linePath := chart.Line(inner, xs, drawYs, chart.CurveMonotone).Cap(ui.RoundCap).Join(ui.RoundJoin)
					p.StrokePath(linePath, 2.2, strokeCol)
				}

				// Subtle discrete request markers.
				for i := range xs {
					if i == activeIdx {
						continue
					}
					px := chart.X(inner, xs[i])
					py := chart.Y(inner, drawYs[i])
					p.FillPath((&ui.Path{}).Circle(px, py, 2.2), dotMuted)
				}

				// Spring-gliding vertical crosshair + luminous active marker.
				if activeIdx >= 0 && activeIdx < len(xs) {
					ax := chart.X(inner, xs[activeIdx])
					ay := chart.Y(inner, drawYs[activeIdx])

					cx := ax
					if hasCross {
						cx = chart.X(inner, float32(crossFrac))
					}
					p.Line(cx, inner.Y-4, cx, inner.Y+inner.H+4, 1, crossCol)

					// Outer soft halo.
					p.FillPath((&ui.Path{}).Circle(ax, ay, 9), haloCol)
					// Crisp white/dark ring + accent core.
					p.FillPath((&ui.Path{}).Circle(ax, ay, 5.8), dotRing)
					p.FillPath((&ui.Path{}).Circle(ax, ay, 4.2), strokeCol)
				}
			})
		} else {
			plotBox.Center().Children(func() {
				ui.Text(c, tr.NoRequests).FontSize(12).TextColor(pal.Ink3)
			})
		}

		// Serene Apple-Style Focused Request Scrubber Bar (always visible in curve mode).
		a.renderInspectorBar(c, pal, conv, vm)
	})
}

func (a *App) renderInspectorBar(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, vm scopeView) {
	tr := a.tr()
	reqs := vm.requests
	req, idx, slice := a.inspectedRequest(reqs)

	ui.Row(c.Key("inspector-bar")).
		FillWidth().
		Padding(10, 0, 0, 0).
		BorderWidth(1, 0, 0, 0).
		BorderColor(pal.Divider).
		Justify(ui.SpaceBetween).
		AlignItems(ui.Center).
		Gap(10).
		Children(func() {
			// Left: ‹ › stepper pill + "#id node" + token breakdown.
			ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
				ui.Row(c.Key("insp-stepper")).
					Padding(2).
					Radius(999).
					Background(pal.SegBg).
					Gap(1).
					Shrink(0).
					Children(func() {
						prevBtn := ui.ButtonBase(c.Key("prev-req-btn")).
							Size(20, 20).
							Radius(999).
							Center().
							Cursor(ui.CursorPointer).
							Disabled(idx <= 0).
							Label(tr.PrevRequest).
							Tooltip(tr.PrevReqTip)
						if prevBtn.Hovered() && idx > 0 {
							prevBtn.Background(pal.CardHover)
						}
						prevBtn.Children(func() {
							ui.Icon(c, theme.IconChevronLeft).Size(11, 11).TextColor(pal.Ink2)
						})
						if prevBtn.Clicked() {
							a.stepInspect(reqs, -1)
						}

						nextBtn := ui.ButtonBase(c.Key("next-req-btn")).
							Size(20, 20).
							Radius(999).
							Center().
							Cursor(ui.CursorPointer).
							Disabled(idx < 0 || idx >= len(slice)-1).
							Label(tr.NextRequest).
							Tooltip(tr.NextReqTip)
						if nextBtn.Hovered() && idx >= 0 && idx < len(slice)-1 {
							nextBtn.Background(pal.CardHover)
						}
						nextBtn.Children(func() {
							ui.Icon(c, theme.IconChevronRight).Size(11, 11).TextColor(pal.Ink2)
						})
						if nextBtn.Clicked() {
							a.stepInspect(reqs, 1)
						}
					})

				if idx >= 0 {
					if vm.isTreeView {
						agName := req.AgentName
						if agName == "" {
							if ag, ok := conv.FindAgent(req.AgentID); ok {
								agName = ag.Name
							}
						}
						agName = tr.AgentName(req.AgentID, agName)
						ui.Textf(c, "#%d %s", req.Seq, agName).
							FontSize(12).
							FontWeight(600).
							FontFeatures("tnum").
							TextColor(pal.Ink1).
							NoWrap()
					} else {
						ui.Textf(c, "#%d", req.Seq).
							FontSize(12).
							FontWeight(600).
							FontFeatures("tnum").
							TextColor(pal.Ink1).
							NoWrap()
					}

					ui.Textf(c, "%s %s · %s %s · %s %s",
						tr.TokIn, fmtTokensK(req.InputTokens),
						tr.TokCache, fmtTokensK(req.CacheReadTokens),
						tr.TokOut, fmtTokensK(req.OutputTokens),
					).
						FontSize(11).
						FontFeatures("tnum").
						TextColor(pal.Ink3).
						SingleLine()
				}
			})

			// Right: Hit · TTFT · TPS · Cost.
			if idx >= 0 {
				ui.Row(c).Gap(10).AlignItems(ui.Baseline).Shrink(0).Children(func() {
					inspMetricPair(c, pal, tr.InspHit, fmtPct(req.CacheHitRate*100))
					inspMetricPair(c, pal, "TTFT", fmtSec(req.TTFT.Seconds()))
					inspMetricPair(c, pal, "TPS", fmtTPS(req.DecodeTPS))
					ui.Text(c, fmtUSD(req.CostUSD)).
						FontSize(12).
						FontWeight(600).
						FontFeatures("tnum").
						TextColor(pal.Ink1).
						NoWrap()
				})
			}
		})
}

func inspMetricPair(c *ui.Context, pal theme.Palette, label, val string) {
	ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
		ui.Text(c, label).FontSize(11.5).TextColor(pal.Ink3).NoWrap()
		ui.Text(c, val).FontSize(12).FontWeight(600).FontFeatures("tnum").TextColor(pal.Ink1).NoWrap()
	})
}

func (a *App) renderRequestListPane(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, reqs []model.RequestRecord) {
	tr := a.tr()
	ui.Column(c.Key("pane-list")).Grow(1).MinHeight(0).Gap(4).Children(func() {
		// Table header.
		ui.Grid(c.Key("req-list-hdr")).
			ColumnTracks(ui.Fr(4), ui.Fr(2), ui.Fr(2), ui.Fr(2), ui.Fr(2)).
			Padding(0, 8, 6, 8).
			BorderWidth(0, 0, 1, 0).
			BorderColor(pal.Divider).
			Children(func() {
				ui.Text(c, tr.ColReqNode).FontSize(11).TextColor(pal.Ink3)
				ui.Text(c, tr.ColCacheHit).FontSize(11).TextColor(pal.Ink3).TextAlign(ui.End)
				ui.Text(c, "TTFT").FontSize(11).TextColor(pal.Ink3).TextAlign(ui.End)
				ui.Text(c, tr.ColDecodeTPS).FontSize(11).TextColor(pal.Ink3).TextAlign(ui.End)
				ui.Text(c, tr.ColCost).FontSize(11).TextColor(pal.Ink3).TextAlign(ui.End)
			})

		// Scrollable per-request rows (newest first, up to 24).
		ui.Scroll(c.Key("req-list-scroll")).Grow(1).MinHeight(0).Children(func() {
			items := tailRequests(reqs, 24)
			if len(items) == 0 {
				ui.Box(c).Padding(24).Center().Children(func() {
					ui.Text(c, tr.NoRequests).FontSize(12).TextColor(pal.Ink3)
				})
				return
			}

			ui.Column(c.Key("req-list-rows")).FillWidth().Children(func() {
				for i := len(items) - 1; i >= 0; i-- {
					r := items[i]
					a.renderRequestListRow(c, pal, conv, r, i < len(items)-1)
				}
			})
		})
	})
}

func (a *App) renderRequestListRow(c *ui.Context, pal theme.Palette, conv model.ConversationSnapshot, r model.RequestRecord, withTopDivider bool) {
	tr := a.tr()
	isOpen := a.expandedReqSeq == r.Seq

	agName := r.AgentName
	modelDisplay := r.ModelDisplay
	if ag, ok := conv.FindAgent(r.AgentID); ok {
		if agName == "" {
			agName = ag.Name
		}
		if modelDisplay == "" {
			modelDisplay = ag.ModelDisplay
		}
	}
	agName = tr.AgentName(r.AgentID, agName)

	wrapper := ui.Column(c.Key(fmt.Sprintf("req-item-%d", r.Seq))).FillWidth()
	if withTopDivider {
		wrapper.BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider)
	}

	// Smooth spring accordion expand progress (0..1).
	targetOpen := float32(0)
	if isOpen {
		targetOpen = 1
	}
	openFrac := wrapper.AnimateTo("expand", targetOpen, ui.Animation{
		Spring: ui.SpringOptions{Stiffness: 220, Damping: 25},
	})

	wrapper.Children(func() {
		rowBtn := ui.ButtonBase(c.Key(fmt.Sprintf("req-row-%d", r.Seq))).
			FillWidth().
			Padding(7, 8).
			Radius(10).
			Cursor(ui.CursorPointer).
			Label(fmt.Sprintf("#%d %s", r.Seq, agName))

		if isOpen {
			rowBtn.Background(pal.CardHover)
		} else if rowBtn.Hovered() {
			rowBtn.Background(pal.SegBg)
		}

		rowBtn.Children(func() {
			ui.Grid(c).
				FillWidth().
				ColumnTracks(ui.Fr(4), ui.Fr(2), ui.Fr(2), ui.Fr(2), ui.Fr(2)).
				AlignItems(ui.Center).
				Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
						ui.Textf(c, "#%d", r.Seq).FontSize(11.5).FontFeatures("tnum").TextColor(pal.Ink3).NoWrap()
						ui.Text(c, agName).FontSize(12).FontWeight(500).TextColor(pal.Ink1).SingleLine()
					})
					ui.Text(c, fmtPct(r.CacheHitRate*100)).FontSize(12).FontWeight(500).FontFeatures("tnum").TextColor(pal.Ink1).TextAlign(ui.End)
					ui.Text(c, fmtSec(r.TTFT.Seconds())).FontSize(12).FontFeatures("tnum").TextColor(pal.Ink2).TextAlign(ui.End)
					ui.Text(c, fmtTPS(r.DecodeTPS)).FontSize(12).FontWeight(600).FontFeatures("tnum").TextColor(pal.Ink1).TextAlign(ui.End)
					ui.Text(c, fmtUSD(r.CostUSD)).FontSize(12).FontFeatures("tnum").TextColor(pal.Ink2).TextAlign(ui.End)
				})
		})

		if rowBtn.Clicked() {
			if isOpen {
				a.expandedReqSeq = 0
			} else {
				a.expandedReqSeq = r.Seq
			}
		}

		// Expandable inline token composition detail with 3-segment liquid bar.
		if openFrac > 0.02 {
			totalTok := float32(max(int64(1), r.InputTokens+r.CacheReadTokens+r.OutputTokens))
			pCR := float32(r.CacheReadTokens) / totalTok
			pIn := float32(r.InputTokens) / totalTok
			pOut := float32(r.OutputTokens) / totalTok

			detailH := openFrac * 48
			detail := ui.Column(c.Key(fmt.Sprintf("req-detail-%d", r.Seq))).
				FillWidth().
				Height(detailH).
				Clip().
				Opacity(min(1, openFrac*1.2)).
				Padding(4, 8, 10, 8).
				Gap(7)

			detail.Children(func() {
				// 3-segment Token composition liquid bar (Cache / New Input / Output).
				bar := ui.Box(c).FillWidth().Height(6).Radius(3).Background(pal.Track).Clip()
				bar.Draw(func(p *ui.Painter, rect ui.Rect) {
					x := rect.X
					if w := rect.W * pCR; w > 0.5 {
						p.Fill(ui.Rect{X: x, Y: rect.Y, W: w, H: rect.H}, pal.Accent, 0)
						x += w
					}
					if w := rect.W * pIn; w > 0.5 {
						p.Fill(ui.Rect{X: x, Y: rect.Y, W: w, H: rect.H}, pal.Ink2.Alpha(0.55), 0)
						x += w
					}
					if w := rect.W * pOut; w > 0.5 {
						p.Fill(ui.Rect{X: x, Y: rect.Y, W: w, H: rect.H}, pal.Amber, 0)
					}
				})

				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Textf(c, "%s %s · %s %s · %s %s",
						tr.TokCache, fmtTokensK(r.CacheReadTokens),
						tr.TokNewInput, fmtTokensK(r.InputTokens),
						tr.TokOut, fmtTokensK(r.OutputTokens),
					).FontSize(11).FontFeatures("tnum").TextColor(pal.Ink2).SingleLine()

					ui.Text(c, modelDisplay).FontSize(11).TextColor(pal.Ink3).NoWrap()
				})
			})
		}
	})
}
