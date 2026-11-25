package desktop

import (
	"fmt"
	"math"
	"strconv"
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

func (a *App) currentQuotaSnapshot(now time.Time) quota.QuotaSnapshot {
	if a.quotaSvc == nil {
		return quota.QuotaSnapshot{Source: quota.SourceUncalibrated}
	}
	return a.quotaSvc.State(now)
}

func (a *App) syncCalibDraftFromSnap(q quota.QuotaSnapshot, now time.Time) {
	a.calibError = ""
	if !q.Calibrated {
		return
	}
	a.calibGemPct = fmt.Sprintf("%.0f", math.Round(q.Gemini.RemainingPct))
	gh, gm := splitResetHM(q.Gemini.ResetAt, now)
	a.calibGemHours = strconv.Itoa(gh)
	a.calibGemMins = strconv.Itoa(gm)

	a.calibClaPct = fmt.Sprintf("%.0f", math.Round(q.ClaudeGPT.RemainingPct))
	ch, cm := splitResetHM(q.ClaudeGPT.ResetAt, now)
	a.calibClaHours = strconv.Itoa(ch)
	a.calibClaMins = strconv.Itoa(cm)

	a.calibJSONSource = q.SourcePath
}

func (a *App) refreshQuotaSample(now time.Time) {
	if a.quotaSvc == nil {
		return
	}
	var burn history.PoolConsumption
	if a.historySvc != nil {
		since := a.quotaSvc.LastSampledAt()
		if !since.IsZero() {
			burn = a.historySvc.ConsumptionSince(since)
		}
	}
	_, _ = a.quotaSvc.Refresh(now, burn)
}

func (a *App) renderQuotaView(c *ui.Context, pal theme.Palette, now time.Time) {
	tr := a.tr()
	q := a.currentQuotaSnapshot(now)

	bar := c.TitleBar()
	rightTopPad := float32(0)
	if bar.Right > 0 && bar.Height > 0 {
		rightTopPad = max(float32(18), bar.Height-10)
	}

	// Build clean subtitle without stray empty separators.
	var subParts []string
	if strings.TrimSpace(q.AccountLabel) != "" {
		subParts = append(subParts, strings.TrimSpace(q.AccountLabel))
	}
	if strings.TrimSpace(q.PlanLabel) != "" {
		subParts = append(subParts, strings.TrimSpace(q.PlanLabel))
	}
	if len(subParts) == 0 {
		switch q.Source {
		case quota.SourceJSON:
			subParts = append(subParts, tr.QuotaSourceJSON)
		case quota.SourceLocal:
			subParts = append(subParts, tr.QuotaSourceLocal)
		default:
			subParts = append(subParts, tr.QuotaSourceUncalibrated)
		}
	}
	subtitle := strings.Join(subParts, " · ")

	gemSummary := "--%"
	claSummary := "--%"
	if q.Calibrated {
		gemSummary = fmtQuotaPct(q.Gemini.RemainingPct)
		claSummary = fmtQuotaPct(q.ClaudeGPT.RemainingPct)
	}

	ui.Column(c.Key("quota-canvas")).
		Grow(1).
		MinWidth(0).
		Padding(4, 6, 2, 6).
		Gap(11).
		Children(func() {
			// 1. Header
			var calibBtn ui.Element
			ui.Row(c.Key("quota-header")).
				Padding(2, 4, 0, 4).
				Justify(ui.SpaceBetween).
				AlignItems(ui.End).
				Gap(10).
				DragWindow().
				Children(func() {
					ui.Column(c).Gap(4).MinWidth(0).Children(func() {
						ui.Row(c).Gap(8).AlignItems(ui.Baseline).Children(func() {
							ui.Text(c, tr.QuotaTitle).FontSize(20).FontWeight(600).LetterSpacing(-0.3).TextColor(pal.Ink1).NoWrap()
							ui.Text(c, subtitle).FontSize(11.5).TextColor(pal.Ink2).SingleLine()
						})
						ui.Row(c.Key("quota-hdr-split")).Gap(7).AlignItems(ui.Center).Children(func() {
							ui.Text(c, tr.QuotaSummaryPrefix).FontSize(11.5).TextColor(pal.Ink2).NoWrap()
							costRatioPair(c, pal, "Gemini", gemSummary)
							ui.Text(c, "/").FontSize(11.5).TextColor(pal.Ink3)
							costRatioPair(c, pal, "Claude & GPT", claSummary)
						})
					})

					ui.Row(c).Padding(rightTopPad, 0, 0, 0).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
						calibBtn = ui.ButtonBase(c.Key("quota-calib-btn")).
							Padding(5, 10).
							Radius(999).
							Background(pal.CardBg).
							Border(1, pal.RimEdge).
							Cursor(ui.CursorPointer).
							Label(tr.QuotaCalibrate)
						if calibBtn.Hovered() || a.quotaCalibOpen {
							calibBtn.Background(pal.CardHover)
						}
						calibBtn.Children(func() {
							ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
								ui.Icon(c, theme.IconSliders).Size(11.5, 11.5).TextColor(pal.Ink2)
								ui.Text(c, tr.QuotaCalibrate).FontSize(11).FontWeight(500).TextColor(pal.Ink1).NoWrap()
							})
						})
						if calibBtn.Clicked() {
							if !a.quotaCalibOpen {
								a.syncCalibDraftFromSnap(q, now)
							}
							a.quotaCalibOpen = !a.quotaCalibOpen
							a.settingsPopoverOpen = false
							a.sessionPopoverOpen = false
						}

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
							a.refreshQuotaSample(now)
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

			// Calibration Popover anchored to Calibrate button.
			if calibBtn.Valid() {
				a.renderQuotaCalibrationPopover(c, pal, calibBtn, now)
			}

			// Compact uncalibrated prompt banner OR JSON fallback warning strip.
			if !q.Calibrated {
				banner := theme.GlassCard(c.Key("quota-uncal-banner"), pal, theme.CardOptions{Radius: 16}).
					Padding(9, 14, 9, 14).Shrink(0)
				banner.Children(func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(10).Children(func() {
						ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
							ui.Text(c, tr.QuotaUncalibratedTitle).FontSize(12).FontWeight(600).TextColor(pal.Ink1).SingleLine()
							ui.Text(c, tr.QuotaUncalibratedDesc).FontSize(10.5).TextColor(pal.Ink2).SingleLine()
						})
						cta := ui.ButtonBase(c.Key("quota-cal-now-btn")).
							Padding(5, 12).
							Radius(999).
							Background(pal.Accent).
							Cursor(ui.CursorPointer).
							Label(tr.QuotaCalibrateNow)
						cta.Children(func() {
							ui.Text(c, tr.QuotaCalibrateNow).FontSize(11).FontWeight(600).TextColor(ui.RGB(255, 255, 255)).NoWrap()
						})
						if cta.Clicked() {
							a.syncCalibDraftFromSnap(q, now)
							a.quotaCalibOpen = true
						}
					})
				})
			} else if q.SourceError != "" {
				ui.Row(c.Key("quota-src-err")).
					FillWidth().
					Padding(6, 12).
					Radius(12).
					Background(pal.AmberSoft).
					AlignItems(ui.Center).
					Shrink(0).
					Children(func() {
						ui.Textf(c, tr.QuotaErrJSONFallbackFmt, q.SourceError).
							FontSize(11).FontWeight(500).TextColor(pal.Amber).SingleLine()
					})
			}

			// 2. Two Side-by-Side Hero Pool Cards (Gemini Models & Claude & GPT Models).
			ui.Grid(c.Key("quota-pools-grid")).Columns(2).Gap(11).Shrink(0).Children(func() {
				a.renderQuotaPoolCard(c, pal, "pool-gemini", "Gemini Models", q.Calibrated, q.Gemini, pal.Accent, pal.Teal, now, a.quotaSeries == "gemini" || a.quotaSeries == "all", func() {
					a.quotaSeries = "gemini"
				})
				a.renderQuotaPoolCard(c, pal, "pool-claude", "Claude & GPT Models", q.Calibrated, q.ClaudeGPT, pal.Indigo, pal.Amber, now, a.quotaSeries == "claude", func() {
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
	key string,
	title string,
	calibrated bool,
	pool quota.PoolQuota,
	primaryCol ui.Color,
	secondaryCol ui.Color,
	now time.Time,
	selected bool,
	onClick func(),
) {
	tr := a.tr()
	card := theme.GlassCard(c.Key(key), pal, theme.CardOptions{
		Radius:      20,
		Selected:    selected,
		Interactive: true,
	}).Padding(13, 15, 12, 15).Gap(9).Label(title)

	ringCol := primaryCol
	pctText := "--%"
	pctRatio := float32(0)
	resetText := tr.QuotaUncalibratedShort
	if calibrated {
		pctText = fmtQuotaPct(pool.RemainingPct)
		pctRatio = float32(pool.RemainingPct / 100.0)
		if pool.RemainingPct < 25 {
			ringCol = pal.Amber
		}
		resetText = fmtResetCountdown(tr, pool.ResetAt, now)
	}

	subs := pool.SubModels
	if len(subs) == 0 {
		if strings.Contains(title, "Gemini") {
			subs = []quota.SubModelQuota{
				{Name: metrics.SubModelGeminiNeon, RemainingPct: pool.RemainingPct},
				{Name: metrics.SubModelGeminiFlash, RemainingPct: pool.RemainingPct},
			}
		} else {
			subs = []quota.SubModelQuota{
				{Name: metrics.SubModelClaude45, RemainingPct: pool.RemainingPct},
				{Name: metrics.SubModelGPTCodex, RemainingPct: pool.RemainingPct},
			}
		}
	}

	card.Children(func() {
		// Top section: pool title + big % + reset countdown on left; Ring Gauge on right.
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Start).Gap(10).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(7, 7).Radius(3.5).Background(primaryCol).Shrink(0)
					ui.Text(c, title).FontSize(12.5).FontWeight(600).TextColor(pal.Ink1).SingleLine()
				})
				ui.Text(c, pctText).
					FontSize(26).
					FontWeight(600).
					LetterSpacing(-0.5).
					FontFeatures("tnum").
					TextColor(pal.Ink1).
					NoWrap()
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					ui.Text(c, tr.QuotaRemaining).FontSize(10.5).TextColor(pal.Ink2).NoWrap()
					ui.Text(c, "·").FontSize(10.5).TextColor(pal.Ink3)
					ui.Text(c, resetText).FontSize(10.5).FontFeatures("tnum").TextColor(pal.Ink2).SingleLine()
				})
			})

			// 54x54 Apple Circular Ring Gauge.
			ring := ui.Box(c.Key(key + "-ring")).Size(54, 54).Shrink(0).Center()
			animRing := ring.AnimateTo("ring", pctRatio, ui.Animation{
				Spring: ui.SpringOptions{Stiffness: 150, Damping: 22},
			})
			trackCol := pal.RingBg
			arcCol := ringCol
			ring.Draw(func(p *ui.Painter, r ui.Rect) {
				cx := r.X + r.W/2
				cy := r.Y + r.H/2
				rad := min(r.W, r.H)/2 - 4
				if rad <= 2 {
					return
				}
				bgPath := (&ui.Path{}).Circle(cx, cy, rad)
				p.StrokePath(bgPath, 4.8, trackCol)

				frac := animRing
				if frac < 0 {
					frac = 0
				}
				if frac > 1 {
					frac = 1
				}
				if frac > 0.005 {
					arc := buildArcPath(cx, cy, rad, -math.Pi/2, float64(frac)*2*math.Pi)
					p.StrokePath(arc, 4.8, arcCol)
				}
			})
			ring.Children(func() {
				ui.Text(c, pctText).FontSize(10.5).FontWeight(600).FontFeatures("tnum").TextColor(pal.Ink1).NoWrap()
			})
		})

		// Sub-model mini bars.
		ui.Column(c).FillWidth().Padding(8, 0, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Gap(6).Children(func() {
			limit := min(2, len(subs))
			for i := 0; i < limit; i++ {
				sm := subs[i]
				barCol := primaryCol
				if i > 0 {
					barCol = secondaryCol
				}
				valTxt := "--%"
				ratio := float32(0)
				if calibrated {
					valTxt = fmtQuotaPct(sm.RemainingPct)
					ratio = float32(sm.RemainingPct / 100.0)
				}
				ui.Column(c.Key(fmt.Sprintf("%s-sub-%d", key, i))).FillWidth().Gap(3).Children(func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Text(c, sm.Name).FontSize(10.5).TextColor(pal.Ink2).SingleLine()
						ui.Text(c, valTxt).FontSize(10.5).FontWeight(500).FontFeatures("tnum").TextColor(pal.Ink1).NoWrap()
					})
					theme.LiquidProgressBar(c, pal, fmt.Sprintf("%s-sbar-%d", key, i), ratio, barCol, 4.5, 1.0, false)
				})
			}
		})
	})

	if card.Clicked() && onClick != nil {
		onClick()
	}
}

func (a *App) renderQuotaTrendCard(c *ui.Context, pal theme.Palette, q quota.QuotaSnapshot, now time.Time) {
	tr := a.tr()
	card := theme.GlassCard(c.Key("quota-trend-card"), pal, theme.CardOptions{
		Radius: 20,
	}).Padding(12, 16, 11, 16).Gap(8).Grow(1).MinHeight(162)

	pts := q.Trend24h
	if a.quotaRange == "7d" {
		pts = q.Trend7d
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

func (a *App) renderQuotaCalibrationPopover(c *ui.Context, pal theme.Palette, anchor ui.Element, now time.Time) {
	tr := a.tr()
	ui.PopoverBase(c.Key("quota-calib-popover"), anchor, &a.quotaCalibOpen, func(panel ui.Element) {
		panel.AttachTo(anchor, ui.AnchorBottomEnd, ui.AnchorTopEnd)
		theme.GlassPopover(panel, pal, 336).Margin(6, 0, 0, 0).Padding(14).Gap(10)

		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, tr.QuotaCalibTitle).FontSize(12.5).FontWeight(600).TextColor(pal.Ink1)
			ui.Text(c, tr.QuotaCalibPrivacyNote).FontSize(10).TextColor(pal.Ink3)
		})

		// Gemini baseline row
		a.renderCalibPoolRow(c, pal, "cal-gem", "Gemini Models", pal.Accent, &a.calibGemPct, &a.calibGemHours, &a.calibGemMins)
		// Claude & GPT baseline row
		a.renderCalibPoolRow(c, pal, "cal-cla", "Claude & GPT Models", pal.Indigo, &a.calibClaPct, &a.calibClaHours, &a.calibClaMins)

		// Optional local JSON source
		ui.Column(c.Key("cal-json-wrap")).FillWidth().Gap(4).Children(func() {
			ui.Text(c, tr.QuotaCalibJSONLabel).FontSize(11).FontWeight(500).TextColor(pal.Ink2)
			ui.TextInput(c.Key("cal-json-input"), &a.calibJSONSource).
				FillWidth().
				Height(28).
				Padding(4, 8).
				Radius(8).
				FontSize(11).
				Label(tr.QuotaCalibJSONLabel)
			ui.Text(c, tr.QuotaCalibJSONHint).FontSize(9.5).TextColor(pal.Ink3)
		})

		if a.calibError != "" {
			ui.Text(c, a.calibError).FontSize(10.5).FontWeight(500).TextColor(pal.Danger)
		}

		// Action buttons: Clear | Cancel | Save
		ui.Row(c.Key("cal-actions")).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Padding(4, 0, 0, 0).Children(func() {
			clearBtn := ui.ButtonBase(c.Key("cal-clear-btn")).
				Padding(5, 10).Radius(8).Cursor(ui.CursorPointer).Label(tr.QuotaCalibClear)
			if clearBtn.Hovered() {
				clearBtn.Background(pal.SegBg)
			}
			clearBtn.Children(func() {
				ui.Text(c, tr.QuotaCalibClear).FontSize(11).TextColor(pal.Ink2).NoWrap()
			})
			if clearBtn.Clicked() && a.quotaSvc != nil {
				_, _ = a.quotaSvc.ClearCalibration(now)
				a.calibJSONSource = ""
				a.calibError = ""
				a.quotaCalibOpen = false
			}

			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				cancelBtn := ui.ButtonBase(c.Key("cal-cancel-btn")).
					Padding(5, 11).Radius(8).Background(pal.SegBg).Cursor(ui.CursorPointer).Label(tr.QuotaCalibCancel)
				if cancelBtn.Hovered() {
					cancelBtn.Background(pal.CardHover)
				}
				cancelBtn.Children(func() {
					ui.Text(c, tr.QuotaCalibCancel).FontSize(11).FontWeight(500).TextColor(pal.Ink1).NoWrap()
				})
				if cancelBtn.Clicked() {
					a.calibError = ""
					a.quotaCalibOpen = false
				}

				saveBtn := ui.ButtonBase(c.Key("cal-save-btn")).
					Padding(5, 13).Radius(8).Background(pal.Accent).Cursor(ui.CursorPointer).Label(tr.QuotaCalibSave)
				saveBtn.Children(func() {
					ui.Text(c, tr.QuotaCalibSave).FontSize(11).FontWeight(600).TextColor(ui.RGB(255, 255, 255)).NoWrap()
				})
				if saveBtn.Clicked() {
					a.submitQuotaCalibration(now)
				}
			})
		})
	})
}

func (a *App) renderCalibPoolRow(c *ui.Context, pal theme.Palette, key, title string, dotCol ui.Color, pct, hours, mins *string) {
	tr := a.tr()
	ui.Column(c.Key(key)).FillWidth().Gap(4).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(6.5, 6.5).Radius(3.25).Background(dotCol)
			ui.Text(c, title).FontSize(11.5).FontWeight(600).TextColor(pal.Ink1)
		})
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, tr.QuotaCalibRemPct).FontSize(10.5).TextColor(pal.Ink2).NoWrap()
			ui.TextInput(c.Key(key+"-pct"), pct).Width(48).Height(26).Padding(3, 6).Radius(7).FontSize(11).Label(title + " %")

			ui.Text(c, tr.QuotaCalibResetLabel).FontSize(10.5).TextColor(pal.Ink2).NoWrap()
			ui.TextInput(c.Key(key+"-hr"), hours).Width(36).Height(26).Padding(3, 6).Radius(7).FontSize(11).Label(title + " h")
			ui.Text(c, tr.QuotaCalibHoursUnit).FontSize(10.5).TextColor(pal.Ink3).NoWrap()
			ui.TextInput(c.Key(key+"-mn"), mins).Width(36).Height(26).Padding(3, 6).Radius(7).FontSize(11).Label(title + " m")
			ui.Text(c, tr.QuotaCalibMinutesUnit).FontSize(10.5).TextColor(pal.Ink3).NoWrap()
		})
	})
}

func (a *App) submitQuotaCalibration(now time.Time) {
	tr := a.tr()
	if a.quotaSvc == nil {
		a.quotaCalibOpen = false
		return
	}

	if src := strings.TrimSpace(a.calibJSONSource); src != "" {
		if _, err := a.quotaSvc.SetJSONSource(src, now); err != nil {
			if err == quota.ErrUnsafeJSONSource {
				a.calibError = tr.QuotaErrUnsafeJSONSource
			} else {
				a.calibError = fmt.Sprintf(tr.QuotaErrJSONFallbackFmt, err.Error())
			}
			return
		}
		a.calibError = ""
		a.quotaCalibOpen = false
		return
	}

	gPct, err1 := strconv.ParseFloat(strings.TrimSpace(a.calibGemPct), 64)
	cPct, err2 := strconv.ParseFloat(strings.TrimSpace(a.calibClaPct), 64)
	if err1 != nil || err2 != nil || math.IsNaN(gPct) || math.IsNaN(cPct) || gPct < 0 || gPct > 100 || cPct < 0 || cPct > 100 {
		a.calibError = tr.QuotaErrInvalidPct
		return
	}
	gH, err3 := strconv.Atoi(strings.TrimSpace(a.calibGemHours))
	gM, err4 := strconv.Atoi(strings.TrimSpace(a.calibGemMins))
	cH, err5 := strconv.Atoi(strings.TrimSpace(a.calibClaHours))
	cM, err6 := strconv.Atoi(strings.TrimSpace(a.calibClaMins))
	if err3 != nil || err4 != nil || err5 != nil || err6 != nil || gH < 0 || cH < 0 || gM < 0 || gM > 59 || cM < 0 || cM > 59 {
		a.calibError = tr.QuotaErrInvalidReset
		return
	}

	_, err := a.quotaSvc.Calibrate(quota.CalibrationInput{
		GeminiRemainingPct: gPct,
		GeminiResetIn:      time.Duration(gH)*time.Hour + time.Duration(gM)*time.Minute,
		ClaudeRemainingPct: cPct,
		ClaudeResetIn:      time.Duration(cH)*time.Hour + time.Duration(cM)*time.Minute,
	}, now)
	if err != nil {
		a.calibError = err.Error()
		return
	}
	a.calibError = ""
	a.quotaCalibOpen = false
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
	if resetAt.IsZero() || !resetAt.After(now) {
		return tr.QuotaResetSoon
	}
	d := resetAt.Sub(now)
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

func splitResetHM(resetAt, now time.Time) (int, int) {
	if resetAt.IsZero() || !resetAt.After(now) {
		return 0, 0
	}
	d := resetAt.Sub(now)
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	return h, m
}
