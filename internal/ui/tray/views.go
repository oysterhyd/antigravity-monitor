package tray

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/i18n"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/quota"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

func (ctl *Controller) renderQuotaView(c *ui.Context, pal palette, now time.Time) {
	st := ctl.state
	tr := i18n.Get(st.Settings.Language)
	q := ctl.currentQuotaSnapshot(now)
	available := !q.UpdatedAt.IsZero()

	// 1. Header: Icon pill + Title on left; Refresh + Back icon pills on right.
	ui.Row(c.Key("tray-quota-hdr")).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Box(c).Size(24, 24).Radius(8).Background(pal.AccentSoft).Center().Children(func() {
				ui.Icon(c, theme.IconGauge).Size(13, 13).TextColor(pal.Accent)
			})
			ui.Text(c, tr.QuotaTitle).FontSize(13).FontWeight(600).TextColor(pal.Ink1).SingleLine()
		})

		ui.Row(c).AlignItems(ui.Center).Gap(4).Shrink(0).Children(func() {
			refBtn := ui.ButtonBase(c.Key("tray-quota-refresh")).
				Size(26, 26).Radius(13).Center().Background(pal.SegBg).Cursor(ui.CursorPointer).
				Label(tr.QuotaRefresh).Tooltip(tr.QuotaRefresh)
			if refBtn.Hovered() {
				refBtn.Background(pal.CardHover)
			}
			refBtn.Children(func() {
				ui.Icon(c, theme.IconRefresh).Size(13, 13).TextColor(pal.Ink2)
			})
			if refBtn.Clicked() {
				ctl.refreshQuotaAsync()
			}

			backBtn := ui.ButtonBase(c.Key("tray-quota-back")).
				Size(26, 26).Radius(13).Center().Background(pal.SegBg).Cursor(ui.CursorPointer).
				Label(tr.BackToLive).Tooltip(tr.BackToLive)
			if backBtn.Hovered() {
				backBtn.Background(pal.CardHover)
			}
			backBtn.Children(func() {
				ui.Icon(c, theme.IconArrowLeft).Size(13, 13).TextColor(pal.Ink2)
			})
			if backBtn.Clicked() {
				st.SetView(ViewLive)
			}
		})
	})

	// Optional quiet amber banner when quota source fails.
	if q.SourceError != nil {
		msg := tr.QuotaUnavailable
		switch {
		case available:
			msg = tr.QuotaShowingLast
		case errors.Is(q.SourceError, quota.ErrCLISetup):
			msg = tr.QuotaSetupFailed
		}
		ui.Row(c.Key("tray-quota-err")).
			AlignSelf(ui.Stretch).Padding(6, 10).Radius(10).Background(pal.AmberSoft).AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, msg).FontSize(10.5).FontWeight(500).TextColor(pal.Amber).SingleLine()
			})
	}

	// 2. Two compact pool cards (Gemini Models & Claude & GPT Models).
	renderTrayQuotaPoolCard(c, pal, tr, "tray-pool-gemini", "Gemini Models", available, q.Gemini, pal.Accent, now)
	renderTrayQuotaPoolCard(c, pal, tr, "tray-pool-claude", "Claude & GPT Models", available, q.ClaudeGPT, pal.Indigo, now)

	// 3. Compact 24h Quota Trend Card (5h solid + weekly dashed).
	renderTrayQuotaTrendCard(c, pal, tr, q)
}

func renderTrayQuotaPoolCard(
	c *ui.Context, pal palette, tr i18n.Strings,
	key, title string, available bool, pool quota.PoolQuota, color ui.Color, now time.Time,
) {
	ui.Column(c.Key(key)).
		AlignSelf(ui.Stretch).
		Padding(10, 12).
		Radius(14).
		Background(pal.CardBg).
		Border(1, pal.RimEdge).
		Gap(8).
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Box(c).Size(6.5, 6.5).Radius(3.25).Background(color).Shrink(0)
				ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(pal.Ink1).SingleLine()
			})
			renderTrayQuotaWindowRow(c, pal, tr, key+"-5h", tr.QuotaFiveHour, pool.FiveHour, available, color, now)
			renderTrayQuotaWindowRow(c, pal, tr, key+"-wk", tr.QuotaWeekly, pool.Weekly, available, color, now)
		})
}

func renderTrayQuotaWindowRow(
	c *ui.Context, pal palette, tr i18n.Strings,
	key, label string, w quota.WindowQuota, available bool, color ui.Color, now time.Time,
) {
	pctText := "--%"
	resetText := tr.QuotaResetUnknown
	ratio := float32(0)
	barCol := color
	if available {
		pctText = fmtQuotaPct(w.RemainingPct)
		resetText = fmtResetCountdown(tr, w.ResetAt, now)
		ratio = clampFloat32(float32(w.RemainingPct/100.0), 0, 1)
		if w.RemainingPct < 25 {
			barCol = pal.Amber
		}
	}

	wrap := ui.Column(c.Key(key)).FillWidth().Gap(4)
	animRatio := wrap.Spring("qbar", ratio, barSpring)
	wrap.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Baseline).Justify(ui.SpaceBetween).Gap(8).Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Baseline).Gap(6).Children(func() {
				ui.Text(c, label).FontSize(11).FontWeight(500).TextColor(pal.Ink2).NoWrap()
				ui.Text(c, resetText).FontSize(10).FontFeatures("tnum").TextColor(pal.Ink3).SingleLine()
			})
			pctCol := pal.Ink1
			if available && w.RemainingPct < 25 {
				pctCol = pal.Amber
			}
			ui.Text(c, pctText).FontSize(13).FontWeight(600).FontFeatures("tnum").TextColor(pctCol).Shrink(0).NoWrap()
		})
		ui.Box(c).FillWidth().Height(5).Radius(2.5).Background(pal.Track).Draw(func(p *ui.Painter, r ui.Rect) {
			fillW := r.W * clampFloat32(animRatio, 0, 1)
			if fillW > 0.5 {
				p.Fill(ui.Rect{X: r.X, Y: r.Y, W: max(fillW, r.H), H: r.H}, barCol, r.H/2)
			}
		})
	})
}

func renderTrayQuotaTrendCard(c *ui.Context, pal palette, tr i18n.Strings, q quota.QuotaSnapshot) {
	fiveH := q.FiveHourTrend.Last24h
	weekly := q.WeeklyTrend.Last24h

	ui.Column(c.Key("tray-quota-trend")).
		AlignSelf(ui.Stretch).
		Padding(9, 12).
		Radius(14).
		Background(pal.CardBg).
		Border(1, pal.RimEdge).
		Gap(6).
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(6).Children(func() {
				ui.Text(c, tr.QuotaTrendTitle).FontSize(11).FontWeight(500).TextColor(pal.Ink2).SingleLine()
				ui.Row(c).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
					trayLegendDot(c, pal, pal.Accent, "Gemini")
					trayLegendDot(c, pal, pal.Indigo, "Claude")
				})
			})

			plot := ui.Box(c.Key("tray-quota-plot")).FillWidth().Height(54)
			if len(fiveH) == 0 && len(weekly) == 0 {
				plot.Center().Children(func() {
					ui.Text(c, tr.QuotaTrendEmpty).FontSize(10.5).TextColor(pal.Ink3)
				})
				return
			}

			fhCopy := append([]quota.TrendPoint(nil), fiveH...)
			wkCopy := append([]quota.TrendPoint(nil), weekly...)
			plot.Draw(func(p *ui.Painter, r ui.Rect) {
				inner := ui.Rect{X: r.X + 4, Y: r.Y + 4, W: r.W - 8, H: r.H - 8}
				if inner.W <= 4 || inner.H <= 4 {
					return
				}
				p.Line(inner.X, inner.Y+inner.H/2, inner.X+inner.W, inner.Y+inner.H/2, 1, pal.Divider)
				p.Line(inner.X, inner.Y+inner.H, inner.X+inner.W, inner.Y+inner.H, 1, pal.Divider)

				drawSeries := func(pts []quota.TrendPoint, dashed bool) {
					n := len(pts)
					if n == 0 {
						return
					}
					xs := make([]float32, n)
					gYs := make([]float32, n)
					cYs := make([]float32, n)
					for i, pt := range pts {
						if n == 1 {
							xs[i] = 0.5
						} else {
							xs[i] = float32(i) / float32(n-1)
						}
						gYs[i] = clampFloat32(float32(pt.GeminiPct/100.0), 0, 1)
						cYs[i] = clampFloat32(float32(pt.ClaudePct/100.0), 0, 1)
					}
					if n >= 2 {
						cLine := chart.Line(inner, xs, cYs, chart.CurveMonotone).Cap(ui.RoundCap)
						gLine := chart.Line(inner, xs, gYs, chart.CurveMonotone).Cap(ui.RoundCap)
						if dashed {
							p.StrokePath(cLine.Dash(0, 5, 4), 1.5, pal.Indigo.Alpha(0.72))
							p.StrokePath(gLine.Dash(0, 5, 4), 1.5, pal.Accent.Alpha(0.72))
						} else {
							p.StrokePath(cLine, 1.8, pal.Indigo)
							p.StrokePath(gLine, 2.0, pal.Accent)
						}
					} else {
						cx := chart.X(inner, xs[0])
						if dashed {
							p.StrokePath((&ui.Path{}).Circle(cx, chart.Y(inner, cYs[0]), 4), 1.2, pal.Indigo)
							p.StrokePath((&ui.Path{}).Circle(cx, chart.Y(inner, gYs[0]), 4), 1.2, pal.Accent)
						} else {
							p.FillPath((&ui.Path{}).Circle(cx, chart.Y(inner, cYs[0]), 3), pal.Indigo)
							p.FillPath((&ui.Path{}).Circle(cx, chart.Y(inner, gYs[0]), 3.2), pal.Accent)
						}
					}
				}

				drawSeries(wkCopy, true)
				drawSeries(fhCopy, false)
			})
		})
}

func (ctl *Controller) renderHistoryView(c *ui.Context, pal palette, now time.Time) {
	st := ctl.state
	tr := i18n.Get(st.Settings.Language)
	res := ctl.queryHistory(now)

	// 1. Top Range Segmented Pill: [ Today | 7D | 30D | All ]
	ranges := [4]history.Range{history.RangeToday, history.Range7D, history.Range30D, history.RangeAll}
	labels := [4]string{tr.HistoryRangeToday, tr.HistoryRange7D, tr.HistoryRange30D, tr.HistoryRangeAll}
	rngIdx := 1
	for i, r := range ranges {
		if r == st.HistoryRange {
			rngIdx = i
			break
		}
	}

	seg := ui.Box(c.Key("tray-hist-seg")).AlignSelf(ui.Stretch).Height(32).Radius(16).
		Background(pal.SegBg).Padding(3)
	thumbIdx := seg.Spring("hseg-idx", float32(rngIdx), segSpring)
	seg.Draw(func(p *ui.Painter, r ui.Rect) {
		innerW := r.W - 6
		if innerW <= 0 {
			return
		}
		pillW := innerW / 4
		pillX := r.X + 3 + thumbIdx*pillW
		pillRect := ui.Rect{X: pillX, Y: r.Y + 3, W: pillW, H: r.H - 6}
		p.Shadow(pillRect, (r.H-6)/2, 0, 1.5, 6, 0, pal.Shadow)
		p.Fill(pillRect, pal.SegPill, (r.H-6)/2)
		p.Stroke(pillRect, pal.SegPillBorder, (r.H-6)/2, 0.75)
	})
	seg.Children(func() {
		ui.Row(c).Fill().AlignItems(ui.Center).Children(func() {
			for i, lbl := range labels {
				active := i == rngIdx
				col := pal.Ink2
				w := 500
				if active {
					col = pal.Ink1
					w = 600
				}
				btn := ui.ButtonBase(c.Key(fmt.Sprintf("hrng-%d", i))).
					Grow(1).Basis(0).Height(26).Center().Radius(13).Cursor(ui.CursorPointer).
					Label(lbl).
					Children(func() {
						ui.Text(c, lbl).FontSize(11.5).FontWeight(w).TextColor(col).SingleLine()
					})
				if btn.Clicked() {
					st.SetHistoryRange(ranges[i])
					ctl.histHasCache = false
				}
			}
		})
	})

	// 2. Header Row: Icon pill + Title on left; Back icon pill on right.
	ui.Row(c.Key("tray-hist-hdr")).AlignSelf(ui.Stretch).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Box(c).Size(24, 24).Radius(8).Background(pal.AccentSoft).Center().Children(func() {
				ui.Icon(c, theme.IconBarChart).Size(13, 13).TextColor(pal.Accent)
			})
			ui.Text(c, tr.HistoryTitle).FontSize(13).FontWeight(600).TextColor(pal.Ink1).SingleLine()
		})

		backBtn := ui.ButtonBase(c.Key("tray-hist-back")).
			Size(26, 26).Radius(13).Center().Background(pal.SegBg).Cursor(ui.CursorPointer).
			Label(tr.BackToLive).Tooltip(tr.BackToLive)
		if backBtn.Hovered() {
			backBtn.Background(pal.CardHover)
		}
		backBtn.Children(func() {
			ui.Icon(c, theme.IconArrowLeft).Size(13, 13).TextColor(pal.Ink2)
		})
		if backBtn.Clicked() {
			st.SetView(ViewLive)
		}
	})

	// 3. Hero Pair: Total Tokens & Total Cost (same visual rhythm as Live tabs).
	avgFmt := tr.HistorySubDailyAvgFmt
	avgVal := fmtUSD2(res.AvgCostUSD)
	if st.HistoryRange == history.RangeToday {
		avgFmt = tr.HistorySubHourlyAvgFmt
		avgVal = fmtUSD2(res.AvgCostUSD) + "/h"
	}
	ui.Row(c.Key("tray-hist-heroes")).AlignSelf(ui.Stretch).Gap(14).Children(func() {
		renderHeroCol(c, pal, pal.Accent,
			tr.HistoryKPITokens,
			fmtCompactTokens(res.TotalTokens),
			fmt.Sprintf(tr.HistorySubOutFmt, fmtCompactTokens(res.OutputTokens)),
		)
		renderHeroCol(c, pal, pal.Indigo,
			tr.HistoryKPICost,
			fmtUSD2(res.TotalCostUSD),
			fmt.Sprintf(avgFmt, avgVal),
		)
	})

	// 4. Compact Interactive Stacked Bar Chart + Floating Hover Pill.
	ctl.renderTrayHistoryBars(c, pal, tr, res)

	// 5. Compact Summary Metrics (Cache Saved & Sessions).
	ui.Column(c.Key("tray-hist-summary")).AlignSelf(ui.Stretch).Gap(3).Children(func() {
		renderMetricPairRow(c, "th-cache", pal,
			tr.HistoryKPICacheSaved,
			fmtUSD2(res.CacheSavedUSD),
			"/ "+fmt.Sprintf(tr.HistorySubHitFmt, fmt.Sprintf("%.1f%%", res.CacheHitRate)),
		)
		renderMetricPairRow(c, "th-sess", pal,
			tr.HistoryKPISessions,
			fmtIntCommas(res.SessionCount),
			"/ "+fmt.Sprintf(tr.HistorySubCallsFmt, fmtIntCommas(res.RequestCount)),
		)
	})

	// 6. Top 3 Sessions (clickable to focus in desktop dashboard).
	ui.Column(c.Key("tray-hist-top")).AlignSelf(ui.Stretch).Gap(4).Children(func() {
		ui.Text(c, tr.HistoryTopSessions).FontSize(11).FontWeight(500).TextColor(pal.Ink3)
		if len(res.TopSessions) == 0 {
			emptyMsg := tr.HistoryEmptyRange
			if st.HistoryRange == history.RangeAll {
				emptyMsg = tr.HistoryEmpty
			}
			ui.Text(c, emptyMsg).FontSize(11.5).TextColor(pal.Ink3).Padding(2, 2)
			return
		}
		limit := min(3, len(res.TopSessions))
		for i := 0; i < limit; i++ {
			sess := res.TopSessions[i]
			title := strings.TrimSpace(sess.Title)
			if title == "" {
				title = tr.HistoryUntitledSession
			}
			dotCol := pal.Accent
			if sess.PrimaryPool == metrics.PoolClaudeGPT {
				dotCol = pal.Indigo
			}
			row := ui.ButtonBase(c.Key("th-sess-" + sess.ConvID)).
				FillWidth().Padding(4, 6).Radius(8).Cursor(ui.CursorPointer).Label(title)
			if row.Hovered() {
				row.Background(pal.CardHover)
			}
			row.Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
					ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Box(c).Size(6, 6).Radius(3).Background(dotCol).Shrink(0)
						ui.Text(c, title).FontSize(11.5).FontWeight(500).TextColor(pal.Ink1).SingleLine()
					})
					ui.Text(c, fmtUSD2(sess.CostUSD)).
						FontSize(11.5).FontWeight(600).FontFeatures("tnum").TextColor(pal.Ink1).Shrink(0).NoWrap()
				})
			})
			if row.Clicked() {
				ctl.focusAgent(sess.ConvID, "")
			}
		}
	})
}

func (ctl *Controller) renderTrayHistoryBars(c *ui.Context, pal palette, tr i18n.Strings, res history.QueryResult) {
	st := ctl.state
	buckets := res.Buckets

	ui.Column(c.Key("tray-hist-bars")).AlignSelf(ui.Stretch).Gap(4).Children(func() {
		// Micro floating inspection pill above bars on hover (matches dot-matrix hover pill).
		hoverText := ""
		if st.HoverHistIdx >= 0 && st.HoverHistIdx < len(buckets) {
			bk := buckets[st.HoverHistIdx]
			hoverText = fmt.Sprintf("%s · %s · %.0f%% · %s",
				bk.Label, fmtCompactTokens(bk.TotalTokens), bk.CacheHitPct, fmtUSD2(bk.TotalCostUSD))
		}
		ui.Row(c).AlignSelf(ui.Stretch).Height(18).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(7).Children(func() {
				trayLegendDot(c, pal, pal.Accent, tr.CtxSegCache)
				trayLegendDot(c, pal, pal.Indigo, tr.CtxSegInput)
				trayLegendDot(c, pal, pal.Green, tr.CtxSegOutput)
			})
			if hoverText != "" {
				ui.Box(c.Key("th-hover-pill")).Padding(2, 7).Radius(9).
					Background(pal.SegPill).Border(0.75, pal.SegPillBorder).
					Shadow(0, 2, 6, 0, pal.Shadow).
					Children(func() {
						ui.Text(c, hoverText).FontSize(9.5).FontWeight(500).TextColor(pal.Ink1).FontFeatures("tnum").SingleLine()
					})
			}
		})

		if len(buckets) == 0 || res.RequestCount == 0 {
			ui.Box(c.Key("th-bars-empty")).AlignSelf(ui.Stretch).Height(56).Center().Children(func() {
				emptyMsg := tr.HistoryEmptyRange
				if st.HistoryRange == history.RangeAll {
					emptyMsg = tr.HistoryEmpty
				}
				ui.Text(c, emptyMsg).FontSize(11).TextColor(pal.Ink3)
			})
			return
		}

		plotBox := ui.Box(c.Key("th-bars-canvas")).AlignSelf(ui.Stretch).Height(56).Cursor(ui.CursorPointer)
		if px, _, over := plotBox.PointerPosition(); over && plotBox.Hovered() {
			b := plotBox.Bounds()
			if b.W > 0 {
				idx := int((px / b.W) * float32(len(buckets)))
				if idx < 0 {
					idx = 0
				}
				if idx >= len(buckets) {
					idx = len(buckets) - 1
				}
				st.HoverHistIdx = idx
			}
		} else if st.HoverHistIdx != -1 {
			st.HoverHistIdx = -1
		}

		var maxTok float64
		for _, bk := range buckets {
			if float64(bk.TotalTokens) > maxTok {
				maxTok = float64(bk.TotalTokens)
			}
		}
		if maxTok <= 0 {
			maxTok = 1
		}
		drawBuckets := append([]history.BucketSummary(nil), buckets...)
		hovered := st.HoverHistIdx

		plotBox.Draw(func(p *ui.Painter, r ui.Rect) {
			n := len(drawBuckets)
			if n == 0 || r.W <= 4 || r.H <= 8 {
				return
			}
			const padT = float32(3)
			const padB = float32(2)
			plotH := r.H - padT - padB
			p.Line(r.X, r.Y+padT+plotH, r.X+r.W, r.Y+padT+plotH, 1, pal.Divider)

			slotW := r.W / float32(n)
			barW := min(float32(24), slotW*0.58)
			if barW < 3 {
				barW = max(2, slotW-1.5)
			}

			for i, bk := range drawBuckets {
				alpha := float32(1.0)
				if hovered >= 0 && i != hovered {
					alpha = 0.42
				}
				cx := r.X + (float32(i)+0.5)*slotW
				bx := cx - barW/2
				if bk.TotalTokens <= 0 {
					p.Fill(ui.Rect{X: bx, Y: r.Y + padT + plotH - 2, W: barW, H: 2}, pal.Track, 1)
					continue
				}
				frac := clampFloat32(float32(float64(bk.TotalTokens)/maxTok), 0, 1)
				bh := max(float32(4), plotH*frac)
				by := r.Y + padT + plotH - bh
				barRect := ui.Rect{X: bx, Y: by, W: barW, H: bh}
				rad := min(float32(4.5), barW/2)

				p.Clip(barRect, rad, func() {
					tot := float64(bk.CacheReadTokens + bk.InputTokens + bk.OutputTokens)
					if tot <= 0 {
						p.Fill(barRect, pal.Accent.Alpha(alpha), 0)
						return
					}
					hCache := bh * float32(float64(bk.CacheReadTokens)/tot)
					hIn := bh * float32(float64(bk.InputTokens)/tot)
					hOut := bh - hCache - hIn
					curY := by
					if hOut > 0.5 {
						p.Fill(ui.Rect{X: bx, Y: curY, W: barW, H: hOut}, pal.Green.Alpha(alpha), 0)
						curY += hOut
					}
					if hIn > 0.5 {
						p.Fill(ui.Rect{X: bx, Y: curY, W: barW, H: hIn}, pal.Indigo.Alpha(alpha), 0)
						curY += hIn
					}
					if hCache > 0.5 {
						p.Fill(ui.Rect{X: bx, Y: curY, W: barW, H: hCache + 0.5}, pal.Accent.Alpha(alpha), 0)
					}
				})
			}
		})

		// Compact thinned X-axis labels row.
		step := 1
		if len(buckets) > 12 {
			step = 5
		} else if len(buckets) > 8 {
			step = 3
		}
		ui.Row(c.Key("th-x-labels")).AlignSelf(ui.Stretch).Justify(ui.SpaceBetween).Padding(0, 2).Children(func() {
			for i, bk := range buckets {
				if len(buckets) > 8 && i%step != 0 && i != len(buckets)-1 {
					continue
				}
				col := pal.Ink3
				w := 400
				if i == hovered || (hovered < 0 && i == len(buckets)-1) {
					col = pal.Ink1
					w = 600
				}
				ui.Text(c, bk.Label).FontSize(9).FontWeight(w).FontFeatures("tnum").TextColor(col).NoWrap()
			}
		})
	})
}

func trayLegendDot(c *ui.Context, pal palette, col ui.Color, label string) {
	ui.Row(c).AlignItems(ui.Center).Gap(3.5).Children(func() {
		ui.Box(c).Size(5.5, 5.5).Radius(2.75).Background(col).Shrink(0)
		ui.Text(c, label).FontSize(9.5).TextColor(pal.Ink3).NoWrap()
	})
}

func fmtQuotaPct(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return fmt.Sprintf("%.0f%%", math.Round(v))
}

func fmtResetCountdown(tr i18n.Strings, resetAt, now time.Time) string {
	if resetAt.IsZero() {
		return tr.QuotaResetUnknown
	}
	if !resetAt.After(now) {
		return tr.QuotaResetSoon
	}
	d := resetAt.Sub(now)
	if d >= 24*time.Hour {
		days := int(d / (24 * time.Hour))
		hours := int(d/time.Hour) % 24
		return fmt.Sprintf(tr.QuotaResetInFmt, fmt.Sprintf(tr.QuotaResetDaysHoursFmt, days, hours))
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if h == 0 && m == 0 {
		return tr.QuotaResetSoon
	}
	durStr := fmt.Sprintf("%dh %02dm", h, m)
	if h == 0 {
		durStr = fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf(tr.QuotaResetInFmt, durStr)
}

func fmtCompactTokens(n int64) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func fmtUSD2(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		v = 0
	}
	return fmt.Sprintf("$%.2f", v)
}

func fmtIntCommas(n int) string {
	if n < 0 {
		return "-" + fmtIntCommas(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
