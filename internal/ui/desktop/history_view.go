package desktop

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/history"
	"github.com/oysterhyd/antigravity-monitor/internal/metrics"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

func (a *App) queryHistory(now time.Time) history.QueryResult {
	if a.historySvc == nil {
		return history.QueryResult{Range: a.histRange}
	}
	if a.histHasCache && a.histCachedRng == a.histRange && now.Sub(a.histCachedAt) < 2*time.Second {
		return a.histCached
	}
	res := a.historySvc.Query(a.histRange, now)
	a.histCached = res
	a.histCachedRng = a.histRange
	a.histCachedAt = now
	a.histHasCache = true
	return res
}

func (a *App) renderHistoryView(c *ui.Context, pal theme.Palette, now time.Time) {
	tr := a.tr()
	res := a.queryHistory(now)
	lm := a.layout
	rightTopPad := lm.headerRightPadT

	ui.Column(c.Key("history-canvas")).
		Grow(1).
		MinWidth(0).
		Padding(4, 6, 2, 6).
		Gap(lm.histSectionGap).
		Children(func() {
			// 1. Header: Title on left; Range pill + Back button on right.
			ui.Row(c.Key("hist-header")).
				Padding(2, 4, 0, 4).
				Justify(ui.SpaceBetween).
				AlignItems(ui.Center).
				Gap(10).
				DragWindow().
				Children(func() {
					ui.Text(c, tr.HistoryTitle).FontSize(lm.headerTitleSize).FontWeight(600).LetterSpacing(-0.3).TextColor(pal.Ink1).NoWrap()

					ui.Row(c).Padding(rightTopPad, 0, 0, 0).Gap(7).AlignItems(ui.Center).Shrink(0).Children(func() {
						rngIdx := 1
						switch a.histRange {
						case history.RangeToday:
							rngIdx = 0
						case history.Range7D:
							rngIdx = 1
						case history.Range30D:
							rngIdx = 2
						case history.RangeAll:
							rngIdx = 3
						}
						if theme.SegmentedPill(c, pal, "hist-range-seg", &rngIdx, []string{
							tr.HistoryRangeToday, tr.HistoryRange7D, tr.HistoryRange30D, tr.HistoryRangeAll,
						}, theme.SegmentedOptions{
							Height:   26,
							FontSize: 11,
							PaddingX: 9,
						}) {
							ranges := []history.Range{history.RangeToday, history.Range7D, history.Range30D, history.RangeAll}
							a.histRange = ranges[rngIdx]
							a.histHoverIdx = -1
							a.histHasCache = false
						}

						backBtn := ui.ButtonBase(c.Key("hist-back-btn")).
							Padding(5, 10).
							Radius(999).
							Background(pal.SegBg).
							Cursor(ui.CursorPointer).
							Label(tr.BackToLive)
						if backBtn.Hovered() {
							backBtn.Background(pal.CardHover)
						}
						backBtn.Children(func() {
							ui.Text(c, tr.BackToLive).FontSize(11).FontWeight(500).TextColor(pal.Ink1).NoWrap()
						})
						if backBtn.Clicked() {
							a.SetActiveView(ViewLive)
						}
					})
				})

			// 2. 4 Clickable KPI Glass Tiles.
			avgFmt := tr.HistorySubDailyAvgFmt
			avgVal := fmtUSD2(res.AvgCostUSD)
			if a.histRange == history.RangeToday {
				avgFmt = tr.HistorySubHourlyAvgFmt
				avgVal = fmtUSD2(res.AvgCostUSD) + "/h"
			}

			type hKPI struct {
				key         string
				metric      string
				title       string
				icon        *ui.SVG
				mainVal     string
				subText     string
				interactive bool
			}
			kpis := []hKPI{
				{
					key:         "hkpi-tok",
					metric:      "tokens",
					title:       tr.HistoryKPITokens,
					icon:        theme.IconContext,
					mainVal:     fmtCompactTokens(res.TotalTokens),
					subText:     fmt.Sprintf(tr.HistorySubOutFmt, fmtCompactTokens(res.OutputTokens)),
					interactive: true,
				},
				{
					key:         "hkpi-cost",
					metric:      "cost",
					title:       tr.HistoryKPICost,
					icon:        theme.IconCost,
					mainVal:     fmtUSD2(res.TotalCostUSD),
					subText:     fmt.Sprintf(avgFmt, avgVal),
					interactive: true,
				},
				{
					key:         "hkpi-cache",
					metric:      "cache",
					title:       tr.HistoryKPICacheSaved,
					icon:        theme.IconCache,
					mainVal:     fmtUSD2(res.CacheSavedUSD),
					subText:     fmt.Sprintf(tr.HistorySubHitFmt, fmtPct(res.CacheHitRate)),
					interactive: true,
				},
				{
					key:         "hkpi-sess",
					metric:      "",
					title:       tr.HistoryKPISessions,
					icon:        theme.IconTree,
					mainVal:     fmtIntCommas(res.SessionCount),
					subText:     fmt.Sprintf(tr.HistorySubCallsFmt, fmtIntCommas(res.RequestCount)),
					interactive: false,
				},
			}

			ui.Grid(c.Key("hist-kpi-grid")).Columns(4).Gap(lm.histSectionGap).Shrink(0).Children(func() {
				for _, sp := range kpis {
					sel := sp.interactive && a.histMetric == sp.metric
					tile := theme.GlassCard(c.Key(sp.key), pal, theme.CardOptions{
						Radius:      18,
						Selected:    sel,
						Interactive: sp.interactive,
					}).Padding(lm.cardPadY, lm.kpiPadX, lm.cardPadY-1, lm.kpiPadX).Gap(8 + 2*lm.scale).Label(sp.title)

					tile.Children(func() {
						ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
							ui.Text(c, sp.title).FontSize(11.5).FontWeight(500).TextColor(pal.Ink2).SingleLine()
							ui.Icon(c, sp.icon).Size(13.5, 13.5).TextColor(pal.Ink2)
						})
						ui.Column(c).Gap(2).MinWidth(0).Children(func() {
							ui.Text(c, sp.mainVal).
								FontSize(lm.kpiHistValueSize).
								FontWeight(600).
								LetterSpacing(-0.4).
								FontFeatures("tnum").
								TextColor(pal.Ink1).
								SingleLine()
							ui.Text(c, sp.subText).
								FontSize(11).
								FontFeatures("tnum").
								TextColor(pal.Ink2).
								SingleLine()
						})
					})

					if sp.interactive && tile.Clicked() {
						a.histMetric = sp.metric
					}
				}
			})

			// 3. Middle Card: macOS Screen-Time Stacked Vertical Bar Chart.
			a.renderHistoryChartCard(c, pal, res)

			// 4. Bottom 2-Column Split: Usage by Model + Top 3 Sessions.
			ui.Grid(c.Key("hist-bottom-grid")).
				ColumnTracks(ui.Fr(1), ui.Fr(1.15)).
				Gap(11).
				Shrink(0).
				Children(func() {
					a.renderHistoryModelCard(c, pal, res)
					a.renderHistoryTopSessionsCard(c, pal, res)
				})
		})
}

func (a *App) renderHistoryChartCard(c *ui.Context, pal theme.Palette, res history.QueryResult) {
	tr := a.tr()
	card := theme.GlassCard(c.Key("hist-chart-card"), pal, theme.CardOptions{
		Radius: 20,
	}).Padding(12, 16, 11, 16).Gap(8).Grow(1).MinHeight(154)

	chartTitle := tr.HistoryChartTokens
	metricIdx := 0
	switch a.histMetric {
	case "cost":
		chartTitle = tr.HistoryChartCost
		metricIdx = 1
	case "cache":
		chartTitle = tr.HistoryChartCache
		metricIdx = 2
	}

	card.Children(func() {
		ui.Row(c.Key("hist-chart-hdr")).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).MinWidth(0).Children(func() {
				ui.Text(c, chartTitle).FontSize(12).FontWeight(600).TextColor(pal.Ink1).SingleLine()

				// Compact inline legend.
				ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
					switch a.histMetric {
					case "cost":
						legendDotItem(c, pal, pal.Accent, "Gemini")
						legendDotItem(c, pal, pal.Indigo, "Claude & GPT")
					case "cache":
						legendDotItem(c, pal, pal.Green, tr.HistoryLegendHitPct)
					default:
						legendDotItem(c, pal, pal.Accent, tr.CtxSegCache)
						legendDotItem(c, pal, pal.Indigo, tr.CtxSegInput)
						legendDotItem(c, pal, pal.Green, tr.CtxSegOutput)
					}
				})
			})

			if theme.SegmentedPill(c, pal, "hist-metric-seg", &metricIdx, []string{
				tr.HistoryMetricToken, tr.HistoryMetricCost, tr.HistoryMetricCache,
			}, theme.SegmentedOptions{
				Height:   26,
				FontSize: 10.5,
				PaddingX: 9,
			}) {
				metricsOrder := []string{"tokens", "cost", "cache"}
				a.histMetric = metricsOrder[metricIdx]
			}
		})

		buckets := res.Buckets
		if len(buckets) == 0 || res.RequestCount == 0 {
			ui.Box(c.Key("hist-bars-empty")).FillWidth().Grow(1).MinHeight(76).Center().Children(func() {
				emptyMsg := tr.HistoryEmptyRange
				if a.histRange == history.RangeAll {
					emptyMsg = tr.HistoryEmpty
				}
				ui.Text(c, emptyMsg).FontSize(12).TextColor(pal.Ink3)
			})
			return
		}

		// Interactive Stacked Bar Canvas.
		plotBox := ui.Box(c.Key("hist-bars-canvas")).
			FillWidth().
			Grow(1).
			MinHeight(78).
			Cursor(ui.CursorPointer)

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
				a.histHoverIdx = idx
			}
		} else if a.histHoverIdx != -1 {
			a.histHoverIdx = -1
		}

		activeIdx := a.histHoverIdx
		if activeIdx < 0 || activeIdx >= len(buckets) {
			// Default highlight the last non-empty bucket (or last bucket).
			activeIdx = len(buckets) - 1
			for i := len(buckets) - 1; i >= 0; i-- {
				if buckets[i].TotalTokens > 0 || buckets[i].TotalCostUSD > 0 {
					activeIdx = i
					break
				}
			}
		}

		var maxVal float64
		for _, bk := range buckets {
			switch a.histMetric {
			case "cost":
				if bk.TotalCostUSD > maxVal {
					maxVal = bk.TotalCostUSD
				}
			case "cache":
				maxVal = 100
			default:
				if float64(bk.TotalTokens) > maxVal {
					maxVal = float64(bk.TotalTokens)
				}
			}
		}
		if maxVal <= 0 {
			maxVal = 1
		}

		mode := a.histMetric
		hovered := a.histHoverIdx
		maxBarW := 34 + 20*a.layout.scale

		plotBox.Draw(func(p *ui.Painter, r ui.Rect) {
			n := len(buckets)
			if n == 0 || r.W <= 8 || r.H <= 16 {
				return
			}
			const padT = float32(6)
			const padB = float32(4)
			plotH := r.H - padT - padB
			if plotH < 8 {
				return
			}

			// Subtle baseline.
			p.Line(r.X, r.Y+padT+plotH, r.X+r.W, r.Y+padT+plotH, 1, pal.Divider)

			slotW := r.W / float32(n)
			barW := min(maxBarW, slotW*0.56)
			if barW < 4 {
				barW = max(2, slotW-2)
			}

			for i, bk := range buckets {
				alpha := float32(1.0)
				if hovered >= 0 && i != hovered {
					alpha = 0.42
				}
				var val float64
				switch mode {
				case "cost":
					val = bk.TotalCostUSD
				case "cache":
					if bk.TotalTokens > 0 {
						val = bk.CacheHitPct
					}
				default:
					val = float64(bk.TotalTokens)
				}

				cx := r.X + (float32(i)+0.5)*slotW
				bx := cx - barW/2
				if val <= 0 {
					// Quiet subtle floor tick for zero buckets.
					p.Fill(ui.Rect{X: bx, Y: r.Y + padT + plotH - 2, W: barW, H: 2}, pal.Track, 1)
					continue
				}

				frac := float32(val / maxVal)
				if frac > 1 {
					frac = 1
				}
				bh := max(float32(5), plotH*frac)
				by := r.Y + padT + plotH - bh
				barRect := ui.Rect{X: bx, Y: by, W: barW, H: bh}
				rad := min(float32(6), barW/2)

				p.Clip(barRect, rad, func() {
					switch mode {
					case "cost":
						tot := bk.GeminiCostUSD + bk.ClaudeGPTCostUSD
						if tot <= 0 {
							p.Fill(barRect, pal.Accent.Alpha(alpha), 0)
							return
						}
						hG := bh * float32(bk.GeminiCostUSD/tot)
						hC := bh - hG
						if hG > 0.5 {
							p.Fill(ui.Rect{X: bx, Y: by + hC, W: barW, H: hG}, pal.Accent.Alpha(alpha), 0)
						}
						if hC > 0.5 {
							p.Fill(ui.Rect{X: bx, Y: by, W: barW, H: hC}, pal.Indigo.Alpha(alpha), 0)
						}
					case "cache":
						p.Fill(barRect, pal.Green.Alpha(alpha), 0)
					default:
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
					}
				})
			}
		})

		// Thinned X-axis labels row matching buckets.
		step := 1
		if len(buckets) > 12 {
			step = 4
		} else if len(buckets) > 8 {
			step = 2
		}
		ui.Row(c.Key("hist-x-labels")).FillWidth().Justify(ui.SpaceBetween).Padding(0, 4).Children(func() {
			for i, bk := range buckets {
				if len(buckets) > 8 && i%step != 0 && i != len(buckets)-1 {
					continue
				}
				lblCol := pal.Ink3
				w := 400
				if i == activeIdx {
					lblCol = pal.Ink1
					w = 600
				}
				ui.Text(c, bk.Label).FontSize(9.5).FontWeight(w).FontFeatures("tnum").TextColor(lblCol).NoWrap()
			}
		})

		// Bottom Inspector Readout Bar.
		cur := buckets[activeIdx]
		ui.Row(c.Key("hist-inspector")).
			FillWidth().
			Padding(7, 0, 0, 0).
			BorderWidth(1, 0, 0, 0).
			BorderColor(pal.Divider).
			Justify(ui.SpaceBetween).
			AlignItems(ui.Center).
			Gap(10).
			Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Baseline).MinWidth(0).Children(func() {
					ui.Text(c, cur.Label).FontSize(11.5).FontWeight(600).FontFeatures("tnum").TextColor(pal.Ink1).NoWrap()
					ui.Textf(c, tr.HistoryInspTokensFmt,
						fmtCompactTokens(cur.CacheReadTokens),
						fmtCompactTokens(cur.InputTokens),
						fmtCompactTokens(cur.OutputTokens),
					).FontSize(11).FontFeatures("tnum").TextColor(pal.Ink3).SingleLine()
				})
				ui.Row(c).Gap(12).AlignItems(ui.Baseline).Shrink(0).Children(func() {
					inspMetricPair(c, pal, tr.InspHit, fmtPct(cur.CacheHitPct))
					inspMetricPair(c, pal, tr.HistoryMetricCost, fmtUSD2(cur.TotalCostUSD))
				})
			})
	})
}

func (a *App) renderHistoryModelCard(c *ui.Context, pal theme.Palette, res history.QueryResult) {
	tr := a.tr()
	card := theme.GlassCard(c.Key("hist-model-card"), pal, theme.CardOptions{
		Radius: 18,
	}).Padding(12, 14, 12, 14).Gap(7)

	card.Children(func() {
		ui.Text(c, tr.HistoryByModel).FontSize(12).FontWeight(600).TextColor(pal.Ink1).SingleLine()
		if len(res.ByModel) == 0 {
			ui.Text(c, tr.HistoryEmptyRange).FontSize(11).TextColor(pal.Ink3)
			return
		}
		limit := min(3, len(res.ByModel))
		for i := 0; i < limit; i++ {
			m := res.ByModel[i]
			name := m.Name
			if name == "" {
				name = tr.HistoryOtherModel
			}
			barCol := pal.Accent
			if m.Pool == metrics.PoolClaudeGPT {
				barCol = pal.Indigo
			} else if strings.Contains(strings.ToLower(name), "flash") {
				barCol = pal.Teal
			}
			ui.Column(c.Key(fmt.Sprintf("hm-%d", i))).FillWidth().Gap(3).Children(func() {
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, name).FontSize(11).FontWeight(500).TextColor(pal.Ink1).SingleLine()
					ui.Textf(c, "%.0f%% · %s", m.Share, fmtUSD2(m.CostUSD)).
						FontSize(11).FontWeight(500).FontFeatures("tnum").TextColor(pal.Ink2).NoWrap()
				})
				theme.LiquidProgressBar(c, pal, fmt.Sprintf("hm-bar-%d", i), float32(m.Share/100.0), barCol, 5, 1.0, false)
			})
		}
	})
}

func (a *App) renderHistoryTopSessionsCard(c *ui.Context, pal theme.Palette, res history.QueryResult) {
	tr := a.tr()
	card := theme.GlassCard(c.Key("hist-sessions-card"), pal, theme.CardOptions{
		Radius: 18,
	}).Padding(12, 14, 12, 14).Gap(6)

	card.Children(func() {
		ui.Text(c, tr.HistoryTopSessions).FontSize(12).FontWeight(600).TextColor(pal.Ink1).SingleLine()
		if len(res.TopSessions) == 0 {
			ui.Text(c, tr.HistoryEmptyRange).FontSize(11).TextColor(pal.Ink3)
			return
		}
		limit := min(3, len(res.TopSessions))
		for i := 0; i < limit; i++ {
			sess := res.TopSessions[i]
			title := sess.Title
			if strings.TrimSpace(title) == "" {
				title = tr.HistoryUntitledSession
			}
			dotCol := pal.Accent
			if sess.PrimaryPool == metrics.PoolClaudeGPT {
				dotCol = pal.Indigo
			}
			row := ui.ButtonBase(c.Key("hsess-" + sess.ConvID)).
				FillWidth().
				Padding(4, 6).
				Radius(8).
				Cursor(ui.CursorPointer).
				Label(title)
			if row.Hovered() {
				row.Background(pal.SegBg)
			}
			row.Children(func() {
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(7).Children(func() {
					ui.Row(c).Grow(1).MinWidth(0).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Box(c).Size(6, 6).Radius(3).Background(dotCol).Shrink(0)
						ui.Text(c, title).FontSize(11.5).FontWeight(500).TextColor(pal.Ink1).SingleLine()
					})
					ui.Row(c).Gap(7).AlignItems(ui.Baseline).Shrink(0).Children(func() {
						ui.Text(c, fmtCompactTokens(sess.TotalTokens)).FontSize(10.5).FontFeatures("tnum").TextColor(pal.Ink3).NoWrap()
						ui.Text(c, fmtUSD2(sess.CostUSD)).FontSize(11.5).FontWeight(600).FontFeatures("tnum").TextColor(pal.Ink1).NoWrap()
					})
				})
			})
			if row.Clicked() {
				a.FocusConversation(sess.ConvID, true)
			}
		}
	})
}

func legendDotItem(c *ui.Context, pal theme.Palette, col ui.Color, label string) {
	ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(6, 6).Radius(3).Background(col).Shrink(0)
		ui.Text(c, label).FontSize(10).TextColor(pal.Ink2).NoWrap()
	})
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
