package desktop

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
	"github.com/oysterhyd/antigravity-monitor/internal/i18n"
	"github.com/oysterhyd/antigravity-monitor/internal/quota"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

func (a *App) currentQuotaSnapshot(now time.Time) quota.QuotaSnapshot {
	if a.quotaSvc == nil {
		return quota.QuotaSnapshot{}
	}
	return a.quotaSvc.State(now)
}

func (a *App) renderQuotaView(c *ui.Context, pal theme.Palette, now time.Time) {
	tr := a.tr()
	q := a.currentQuotaSnapshot(now)
	available := !q.UpdatedAt.IsZero()
	lm := a.layout
	rightTopPad := lm.headerRightPadT

	ui.Column(c.Key("quota-canvas")).
		Grow(1).
		MinWidth(0).
		Padding(4, 6, 2, 6).
		Gap(lm.histSectionGap).
		Children(func() {
			// 1. Header
			ui.Row(c.Key("quota-header")).
				Padding(2, 4, 0, 4).
				Justify(ui.SpaceBetween).
				AlignItems(ui.Center).
				Gap(10).
				DragWindow().
				Children(func() {
					ui.Text(c, tr.QuotaTitle).FontSize(lm.headerTitleSize).FontWeight(600).LetterSpacing(-0.3).TextColor(pal.Ink1).NoWrap()

					ui.Row(c).Padding(rightTopPad, 0, 0, 0).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
						refreshBtn := ui.ButtonBase(c.Key("quota-refresh-btn")).
							Padding(5, 10).
							Radius(999).
							Background(pal.CardBg).
							Border(1, pal.RimEdge).
							Cursor(ui.CursorPointer).
							Label(tr.QuotaRefresh)
						if refreshBtn.Hovered() {
							refreshBtn.Background(pal.CardHover)
						}
						refreshBtn.Children(func() {
							ui.Text(c, tr.QuotaRefresh).FontSize(11).FontWeight(500).TextColor(pal.Ink1).NoWrap()
						})
						if refreshBtn.Clicked() {
							a.requestQuotaRefresh()
						}

						backBtn := ui.ButtonBase(c.Key("quota-back-btn")).
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

			if q.SourceError != nil {
				message := tr.QuotaUnavailable
				switch {
				case available:
					message = tr.QuotaShowingLast
				case errors.Is(q.SourceError, quota.ErrCLISetup):
					message = tr.QuotaSetupFailed
				}
				ui.Row(c.Key("quota-src-err")).
					FillWidth().
					Padding(6, 12).
					Radius(12).
					Background(pal.AmberSoft).
					AlignItems(ui.Center).
					Shrink(0).
					Children(func() {
						ui.Text(c, message).
							FontSize(11).FontWeight(500).TextColor(pal.Amber).SingleLine()
					})
			}

			// 2. Two Side-by-Side Hero Pool Cards (Gemini Models & Claude & GPT Models).
			ui.Grid(c.Key("quota-pools-grid")).Columns(2).Gap(11).Shrink(0).Children(func() {
				a.renderQuotaPoolCard(c, pal, "pool-gemini", "Gemini Models", available, q.Gemini, pal.Accent, now, a.quotaSeries == "gemini" || a.quotaSeries == "all", func() {
					a.quotaSeries = "gemini"
				})
				a.renderQuotaPoolCard(c, pal, "pool-claude", "Claude & GPT Models", available, q.ClaudeGPT, pal.Indigo, now, a.quotaSeries == "claude", func() {
					a.quotaSeries = "claude"
				})
			})

			// 3. Bottom Quota Usage Trend Card.
			a.renderQuotaTrendCard(c, pal, q, now)
		})
}

func (a *App) renderQuotaPoolCard(
	c *ui.Context,
	pal theme.Palette,
	key, title string,
	available bool,
	pool quota.PoolQuota,
	color ui.Color,
	now time.Time,
	selected bool,
	onClick func(),
) {
	tr := a.tr()
	card := theme.GlassCard(c.Key(key), pal, theme.CardOptions{
		Radius: 20, Selected: selected, Interactive: true,
	}).Padding(13, 15, 12, 15).Gap(8).Label(title)
	card.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(7, 7).Radius(3.5).Background(color).Shrink(0)
			ui.Text(c, title).FontSize(12.5).FontWeight(600).TextColor(pal.Ink1).SingleLine()
		})
		renderQuotaWindow(c, pal, tr, key+"-five-hour", tr.QuotaFiveHour, pool.FiveHour, available, color, now)
		ui.Box(c).FillWidth().Height(1).Background(pal.Divider).Shrink(0)
		renderQuotaWindow(c, pal, tr, key+"-weekly", tr.QuotaWeekly, pool.Weekly, available, color, now)
	})
	if card.Clicked() {
		onClick()
	}
}

func renderQuotaWindow(c *ui.Context, pal theme.Palette, tr i18n.Strings, key, label string, window quota.WindowQuota, available bool, color ui.Color, now time.Time) {
	pct, reset, ratio := quotaWindowDisplay(tr, window, available, now)
	if available && window.RemainingPct < 25 {
		color = pal.Amber
	}
	ui.Row(c.Key(key)).Gap(10).FillWidth().AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, label).FontSize(11.5).FontWeight(500).TextColor(pal.Ink1).SingleLine()
			ui.Text(c, reset).FontSize(10.5).FontFeatures("tnum").TextColor(pal.Ink2).SingleLine()
		})
		ui.Text(c, pct).FontSize(18).FontWeight(600).FontFeatures("tnum").TextColor(pal.Ink1).NoWrap()
		ring := ui.Box(c.Key(key+"-ring")).Size(34, 34).Shrink(0)
		progress := ring.AnimateTo("ring", ratio, ui.Animation{Spring: ui.SpringOptions{Stiffness: 150, Damping: 22}})
		ring.Draw(func(p *ui.Painter, bounds ui.Rect) {
			cx := bounds.X + bounds.W/2
			cy := bounds.Y + bounds.H/2
			radius := min(bounds.W, bounds.H)/2 - 3
			if radius <= 0 {
				return
			}
			p.StrokePath((&ui.Path{}).Circle(cx, cy, radius), 3.6, pal.RingBg)
			if progress > 0.005 {
				arc := buildArcPath(cx, cy, radius, -math.Pi/2, float64(min(progress, 1))*2*math.Pi)
				p.StrokePath(arc, 3.6, color)
			}
		})
	})
}

func quotaWindowDisplay(tr i18n.Strings, window quota.WindowQuota, available bool, now time.Time) (string, string, float32) {
	if !available {
		return "--%", tr.QuotaResetUnknown, 0
	}
	return fmtQuotaPct(window.RemainingPct), fmtResetCountdown(tr, window.ResetAt, now), float32(window.RemainingPct / 100)
}

func (a *App) renderQuotaTrendCard(c *ui.Context, pal theme.Palette, q quota.QuotaSnapshot, now time.Time) {
	tr := a.tr()
	card := theme.GlassCard(c.Key("quota-trend-card"), pal, theme.CardOptions{
		Radius: 20,
	}).Padding(12, 16, 11, 16).Gap(8).Grow(1).MinHeight(162)

	pts := q.FiveHourTrend.Last24h
	if a.quotaRange == "7d" {
		pts = q.FiveHourTrend.Last7d
	}

	card.Children(func() {
		// Header: Title + Series filter pill + 24h/7d range pill.
		ui.Row(c.Key("quota-trend-hdr")).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).MinWidth(0).Children(func() {
				ui.Text(c, tr.QuotaTrendTitle).FontSize(12).FontWeight(600).TextColor(pal.Ink1).SingleLine()
				ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
					legendDotItem(c, pal, pal.Accent, "Gemini")
					legendDotItem(c, pal, pal.Indigo, "Claude & GPT")
				})
			})

			ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
				sIdx := 0
				switch a.quotaSeries {
				case "gemini":
					sIdx = 1
				case "claude":
					sIdx = 2
				}
				if theme.SegmentedPill(c, pal, "quota-series-seg", &sIdx, []string{
					tr.QuotaFilterAll, "Gemini", "Claude & GPT",
				}, theme.SegmentedOptions{
					Height:   25,
					FontSize: 10.5,
					PaddingX: 8,
				}) {
					series := []string{"all", "gemini", "claude"}
					a.quotaSeries = series[sIdx]
				}

				rIdx := 0
				if a.quotaRange == "7d" {
					rIdx = 1
				}
				if theme.SegmentedPill(c, pal, "quota-range-seg", &rIdx, []string{"24h", "7d"}, theme.SegmentedOptions{
					Width:    82,
					Height:   25,
					FontSize: 10.5,
					PaddingX: 7,
				}) {
					if rIdx == 1 {
						a.quotaRange = "7d"
					} else {
						a.quotaRange = "24h"
					}
					a.quotaHoverIdx = -1
				}
			})
		})

		// Chart Body: Y-axis labels column (100% / 50% / 0%) + plot box.
		lastResetIdx := -1
		for i, pt := range pts {
			if pt.ResetEvent {
				lastResetIdx = i
			}
		}
		hasReset := lastResetIdx >= 0

		ui.Row(c.Key("quota-chart-body")).FillWidth().Grow(1).MinHeight(78).Gap(6).AlignItems(ui.Stretch).Children(func() {
			// Y-axis ticks (100% / 50% / 0%).
			ui.Column(c.Key("quota-y-axis")).Justify(ui.SpaceBetween).AlignItems(ui.End).Width(28).Shrink(0).Padding(4, 0, 4, 0).Children(func() {
				ui.Text(c, "100%").FontSize(9).FontFeatures("tnum").TextColor(pal.Ink3).NoWrap()
				ui.Text(c, "50%").FontSize(9).FontFeatures("tnum").TextColor(pal.Ink3).NoWrap()
				ui.Text(c, "0%").FontSize(9).FontFeatures("tnum").TextColor(pal.Ink3).NoWrap()
			})

			plotBox := ui.Box(c.Key("quota-plot-box")).Grow(1).MinWidth(0).FillHeight().Cursor(ui.CursorCrosshair)

			if len(pts) > 0 {
				xs := make([]float32, len(pts))
				gemYs := make([]float32, len(pts))
				claYs := make([]float32, len(pts))
				for i, pt := range pts {
					if len(pts) == 1 {
						xs[i] = 0.5
					} else {
						xs[i] = float32(i) / float32(len(pts)-1)
					}
					gemYs[i] = float32(pt.GeminiPct / 100.0)
					claYs[i] = float32(pt.ClaudePct / 100.0)
				}

				if px, _, over := plotBox.PointerPosition(); over && plotBox.Hovered() {
					b := plotBox.Bounds()
					const padX = float32(8)
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
						if nearest >= 0 && nearest < len(pts) {
							a.quotaHoverIdx = nearest
						}
					}
				} else if a.quotaHoverIdx != -1 {
					a.quotaHoverIdx = -1
				}

				activeIdx := a.quotaHoverIdx
				if activeIdx < 0 || activeIdx >= len(pts) {
					activeIdx = len(pts) - 1
				}

				showG := a.quotaSeries == "all" || a.quotaSeries == "gemini"
				showC := a.quotaSeries == "all" || a.quotaSeries == "claude"
				seriesMode := a.quotaSeries
				drawPts := append([]quota.TrendPoint(nil), pts...)
				dotRing := ui.RGB(255, 255, 255)
				if pal.Dark {
					dotRing = ui.Hex("#181B24")
				}

				plotBox.Draw(func(p *ui.Painter, r ui.Rect) {
					const padX = float32(8)
					const padY = float32(8)
					inner := ui.Rect{X: r.X + padX, Y: r.Y + padY, W: r.W - padX*2, H: r.H - padY*2}
					if inner.W <= 4 || inner.H <= 4 {
						return
					}

					// Horizontal 100% / 50% / 0% gridlines.
					p.Line(inner.X, inner.Y, inner.X+inner.W, inner.Y, 1, pal.Divider)
					p.Line(inner.X, inner.Y+inner.H/2, inner.X+inner.W, inner.Y+inner.H/2, 1, pal.Divider)
					p.Line(inner.X, inner.Y+inner.H, inner.X+inner.W, inner.Y+inner.H, 1, pal.Divider)

					// Dashed vertical green reset markers.
					for i, pt := range drawPts {
						if !pt.ResetEvent {
							continue
						}
						rx := chart.X(inner, xs[i])
						resetLine := (&ui.Path{}).MoveTo(rx, inner.Y-2).LineTo(rx, inner.Y+inner.H+2).Dash(0, 3, 3)
						p.StrokePath(resetLine, 1.2, pal.Green.Alpha(0.75))
					}

					if showC && len(xs) >= 2 {
						cArea := chart.Area(inner, xs, nil, claYs, chart.CurveMonotone)
						cAlpha := float32(0.14)
						cStroke := float32(1.9)
						if seriesMode == "claude" {
							cAlpha = 0.22
							cStroke = 2.3
						}
						p.FillPathGradient(cArea, ui.LinearGradient{From: pal.Indigo.Alpha(cAlpha), To: ui.Transparent, Angle: 180})
						p.StrokePath(chart.Line(inner, xs, claYs, chart.CurveMonotone).Cap(ui.RoundCap), cStroke, pal.Indigo)
					}
					if showG && len(xs) >= 2 {
						gArea := chart.Area(inner, xs, nil, gemYs, chart.CurveMonotone)
						gAlpha := float32(0.18)
						gStroke := float32(2.1)
						if seriesMode == "gemini" {
							gAlpha = 0.24
							gStroke = 2.4
						}
						p.FillPathGradient(gArea, ui.LinearGradient{From: pal.Accent.Alpha(gAlpha), To: ui.Transparent, Angle: 180})
						p.StrokePath(chart.Line(inner, xs, gemYs, chart.CurveMonotone).Cap(ui.RoundCap), gStroke, pal.Accent)
					}

					// Crosshair + active point markers.
					if activeIdx >= 0 && activeIdx < len(xs) {
						cx := chart.X(inner, xs[activeIdx])
						p.Line(cx, inner.Y-3, cx, inner.Y+inner.H+3, 1, pal.Ink3.Alpha(0.45))
						if showC {
							cy := chart.Y(inner, claYs[activeIdx])
							p.FillPath((&ui.Path{}).Circle(cx, cy, 5.2), dotRing)
							p.FillPath((&ui.Path{}).Circle(cx, cy, 3.8), pal.Indigo)
						}
						if showG {
							gy := chart.Y(inner, gemYs[activeIdx])
							p.FillPath((&ui.Path{}).Circle(cx, gy, 5.6), dotRing)
							p.FillPath((&ui.Path{}).Circle(cx, gy, 4.1), pal.Accent)
						}
					}
				})

				// Crisp frosted Reset badge anchored right next to the dashed green reset line,
				// plus optional single-point hint when only 1 sample exists.
				if hasReset || len(pts) == 1 {
					plotBox.Children(func() {
						if hasReset {
							b := plotBox.Bounds()
							badge := ui.Row(c.Key("quota-reset-badge")).
								Absolute().
								Top(11).
								Padding(2, 6).
								Radius(999).
								Background(pal.CardBg).
								Border(1, pal.Green.Alpha(0.42)).
								AlignItems(ui.Center)
							if b.W > 40 && lastResetIdx >= 0 && lastResetIdx < len(xs) {
								rx := float32(8) + (b.W-16)*xs[lastResetIdx]
								leftX := rx + 6
								if leftX > b.W-64 {
									leftX = max(float32(8), rx-62)
								}
								badge.Left(leftX)
							} else {
								badge.Right(10)
							}
							badge.Children(func() {
								ui.Text(c, tr.QuotaResetMarker).FontSize(9.5).FontWeight(600).TextColor(pal.Green).NoWrap()
							})
						}
						if len(pts) == 1 {
							ui.Row(c.Key("quota-single-hint")).Absolute().Top(11).Right(10).Children(func() {
								ui.Text(c, tr.QuotaTrendSinglePoint).FontSize(9.5).TextColor(pal.Ink3).SingleLine()
							})
						}
					})
				}
			} else {
				plotBox.Draw(func(p *ui.Painter, r ui.Rect) {
					p.Line(r.X+8, r.Y+8, r.X+r.W-8, r.Y+8, 1, pal.Divider)
					p.Line(r.X+8, r.Y+r.H/2, r.X+r.W-8, r.Y+r.H/2, 1, pal.Divider)
					p.Line(r.X+8, r.Y+r.H-8, r.X+r.W-8, r.Y+r.H-8, 1, pal.Divider)
				})
				plotBox.Center().Children(func() {
					ui.Text(c, tr.QuotaTrendEmpty).FontSize(11).TextColor(pal.Ink3)
				})
			}
		})

		// Thinned X-axis labels row.
		if len(pts) > 0 {
			step := 1
			if len(pts) > 10 {
				step = (len(pts) + 5) / 6
			}
			activeIdx := a.quotaHoverIdx
			if activeIdx < 0 || activeIdx >= len(pts) {
				activeIdx = len(pts) - 1
			}
			ui.Row(c.Key("quota-x-labels")).FillWidth().Padding(0, 6, 0, 34).Justify(ui.SpaceBetween).Children(func() {
				for i, pt := range pts {
					if len(pts) > 8 && i%step != 0 && i != len(pts)-1 {
						continue
					}
					lbl := pt.Timestamp.In(now.Location()).Format("15:04")
					if a.quotaRange == "7d" {
						lbl = pt.Timestamp.In(now.Location()).Format("01/02")
					}
					col := pal.Ink3
					w := 400
					if i == activeIdx {
						col = pal.Ink1
						w = 600
					}
					ui.Text(c, lbl).FontSize(9.5).FontWeight(w).FontFeatures("tnum").TextColor(col).NoWrap()
				}
			})
		}

		// Bottom Inspector Readout Bar.
		ui.Row(c.Key("quota-inspector")).
			FillWidth().
			Padding(7, 0, 0, 0).
			BorderWidth(1, 0, 0, 0).
			BorderColor(pal.Divider).
			Justify(ui.SpaceBetween).
			AlignItems(ui.Center).
			Gap(10).
			Children(func() {
				if len(pts) == 0 {
					ui.Text(c, tr.QuotaTrendEmpty).FontSize(11).TextColor(pal.Ink3).SingleLine()
					return
				}
				idx := a.quotaHoverIdx
				if idx < 0 || idx >= len(pts) {
					idx = len(pts) - 1
				}
				pt := pts[idx]
				timeLbl := pt.Timestamp.In(now.Location()).Format("15:04")
				if a.quotaRange == "7d" {
					timeLbl = pt.Timestamp.In(now.Location()).Format("01/02")
				}
				if idx == len(pts)-1 {
					timeLbl += tr.QuotaInspNowSuffix
				}

				note := tr.QuotaInsp7dNote
				if a.quotaRange == "24h" {
					dg, dc := 0.0, 0.0
					if idx > 0 {
						dg = pt.GeminiPct - pts[idx-1].GeminiPct
						dc = pt.ClaudePct - pts[idx-1].ClaudePct
					}
					note = fmt.Sprintf(tr.QuotaInspDeltaFmt, fmtSignedPctDelta(dg), fmtSignedPctDelta(dc))
				}

				ui.Row(c).Gap(8).AlignItems(ui.Baseline).MinWidth(0).Children(func() {
					ui.Text(c, timeLbl).FontSize(11.5).FontWeight(600).FontFeatures("tnum").TextColor(pal.Ink1).NoWrap()
					ui.Text(c, note).FontSize(11).FontFeatures("tnum").TextColor(pal.Ink3).SingleLine()
				})
				ui.Row(c).Gap(12).AlignItems(ui.Baseline).Shrink(0).Children(func() {
					ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
						ui.Text(c, "Gemini").FontSize(11).TextColor(pal.Ink3).NoWrap()
						ui.Text(c, fmtQuotaPct(pt.GeminiPct)).FontSize(11.5).FontWeight(600).FontFeatures("tnum").TextColor(pal.Accent).NoWrap()
					})
					ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
						ui.Text(c, "Claude & GPT").FontSize(11).TextColor(pal.Ink3).NoWrap()
						ui.Text(c, fmtQuotaPct(pt.ClaudePct)).FontSize(11.5).FontWeight(600).FontFeatures("tnum").TextColor(pal.Indigo).NoWrap()
					})
				})
			})
	})
}

func buildArcPath(cx, cy, r float32, startAngle, sweepAngle float64) *ui.Path {
	steps := int(math.Max(12, math.Ceil(math.Abs(sweepAngle)/(math.Pi/24))))
	p := &ui.Path{}
	for i := 0; i <= steps; i++ {
		ang := startAngle + sweepAngle*float64(i)/float64(steps)
		x := cx + r*float32(math.Cos(ang))
		y := cy + r*float32(math.Sin(ang))
		if i == 0 {
			p.MoveTo(x, y)
		} else {
			p.LineTo(x, y)
		}
	}
	return p.Cap(ui.RoundCap)
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

func fmtSignedPctDelta(delta float64) string {
	rounded := math.Round(delta)
	if rounded == 0 {
		return "0%"
	}
	if rounded > 0 {
		return fmt.Sprintf("+%.0f%%", rounded)
	}
	return fmt.Sprintf("%.0f%%", rounded)
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
