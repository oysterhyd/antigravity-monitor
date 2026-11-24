package theme

import (
	"math"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

// CardOptions configures a reusable Apple Liquid Glass card.
type CardOptions struct {
	Radius      float32
	Selected    bool
	Interactive bool
	UseShader   bool    // Use plugins/glass shader material when layering over rich content.
	Clarity     float32 // 0..1 clarity passed to glass.Glass when UseShader is true.
	PingAlpha   float32 // 0..1 subtle accent ripple ring when new telemetry arrives.
}

// GlassCard creates a Liquid Glass card with a 1px specular top bevel, subtle elevation,
// optional cursor-tracking sheen on hover, and selection accent halo.
// Exported for reuse across Track 2 (Desktop) and Track 3 (Tray).
func GlassCard(c *ui.Context, pal Palette, opts CardOptions) ui.Element {
	radius := opts.Radius
	if radius <= 0 {
		radius = 20
	}

	el := ui.Column(c).Radius(radius).Clip()
	hovered := el.Hovered()
	pressed := el.Pressed()

	if opts.Interactive {
		el.Cursor(ui.CursorPointer)
	}

	if opts.UseShader {
		tint := pal.CardBg
		if opts.Selected {
			tint = pal.CardActive
		} else if hovered {
			tint = pal.CardHover
		}
		el.Material(glass.Glass{
			Style:       glass.Regular,
			Tint:        tint,
			Clarity:     opts.Clarity,
			Interactive: opts.Interactive,
		})
	} else {
		bg := pal.CardBg
		if opts.Selected {
			bg = pal.CardActive
		} else if hovered && opts.Interactive {
			bg = pal.CardHover
		}
		el.Background(el.AnimateColor("card-bg", bg, ui.Animation{
			Duration:  180 * time.Millisecond,
			NonMotion: true,
		}))
	}

	borderColor := pal.RimEdge
	if opts.Selected {
		borderColor = pal.Accent
	} else if opts.PingAlpha > 0.01 {
		borderColor = pal.RimEdge.Mix(pal.Accent, opts.PingAlpha*0.65)
	}
	el.Border(1, borderColor)

	// 1px specular top edge highlight + soft ambient drop shadow.
	el.InsetShadow(0, 1, 0, 0, pal.RimTop)
	if opts.Selected {
		el.Shadow(0, 8, 22, 0, pal.AccentSoft)
	} else {
		el.Shadow(0, 4, 16, 0, pal.ShadowCard)
	}

	// Interactive spring micro-lift on hover / press (matches prototype.html glass-tile).
	if opts.Interactive {
		pose := ui.Pose{}
		if pressed {
			pose = ui.Pose{Scale: 0.988}
		} else if hovered {
			pose = ui.Pose{Y: -1.5}
		}
		el.AnimatePose(pose, ui.Animation{
			Spring: ui.SpringOptions{Stiffness: 260, Damping: 24},
		})
	}

	// Cursor-tracking specular sheen & optional arrival pulse.
	sheenTarget := float32(0)
	if hovered {
		sheenTarget = 1
	}
	sheenAlpha := el.AnimateTo("sheen", sheenTarget, ui.Animation{
		Duration:  220 * time.Millisecond,
		NonMotion: true,
	})
	px, py, hasPtr := el.PointerPosition()

	if sheenAlpha > 0.01 || opts.PingAlpha > 0.01 {
		ping := opts.PingAlpha
		sheenCol := pal.Sheen
		accent := pal.Accent
		el.Draw(func(p *ui.Painter, r ui.Rect) {
			if sheenAlpha > 0.01 && r.W > 0 && r.H > 0 {
				cx, cy := float32(0.5), float32(0.5)
				if hasPtr {
					cx = clamp01(px / r.W)
					cy = clamp01(py / r.H)
				}
				rad := float32(220)
				if r.W > rad {
					rad = r.W * 0.72
				}
				p.FillRadialGradient(r, ui.RadialGradient{
					Center: ui.Point{X: cx, Y: cy},
					Radius: rad,
					Stops: []ui.GradientStop{
						{Color: sheenCol.Alpha(float32(sheenCol.A) / 255.0 * sheenAlpha), At: 0},
						{Color: ui.Transparent, At: 0.75},
					},
				}, radius)
			}
			if ping > 0.01 {
				p.Stroke(r, accent.Alpha(ping*0.42), radius, 1.8)
			}
		})
	}

	return el
}

// GlassSidebar creates the macOS-26-style inset floating Liquid Glass sidebar.
// Uses crisp milky-white translucent layering directly over OS Acrylic/atmospheric glass
// (avoiding premultiplied-alpha black bleed from nested blur shaders over transparent shells).
func GlassSidebar(c *ui.Context, pal Palette, width float32) ui.Element {
	side := ui.Column(c).
		Width(width).
		Shrink(0).
		Radius(22).
		Padding(14).
		Justify(ui.SpaceBetween).
		Gap(14).
		Background(pal.SidebarBg).
		Border(1, pal.RimEdge).
		InsetShadow(0, 1, 0, 0, pal.RimTop).
		Shadow(0, 8, 24, 0, pal.ShadowCard)
	return side
}

// GlassPopover styles a floating Liquid Glass popover panel (session picker, settings sheet).
// Uses near-opaque frosted surface + crisp edge stroke so underlying sidebar text never bleeds through.
func GlassPopover(panel ui.Element, pal Palette, width float32) ui.Element {
	if width > 0 {
		panel.Width(width)
	}
	edge := ui.RGBA(15, 23, 42, 0.10)
	if pal.Dark {
		edge = ui.RGBA(255, 255, 255, 0.14)
	}
	return panel.
		Radius(16).
		Padding(8).
		Background(pal.PopoverBg).
		Border(1, edge).
		InsetShadow(0, 1, 0, 0, pal.RimTop).
		Shadow(0, 16, 40, 0, pal.ShadowPopover)
}

// SegmentedOptions configures the Apple spring-animated segmented pill control.
type SegmentedOptions struct {
	Width    float32
	Height   float32
	FontSize float32
	PaddingX float32
}

// SegmentedPill renders an Apple-style spring segmented pill with a smoothly sliding thumb.
// Returns true on the frame the selection changes. Exported for Track 3 reuse.
func SegmentedPill(c *ui.Context, pal Palette, key string, selected *int, labels []string, opts SegmentedOptions) bool {
	if len(labels) == 0 || selected == nil {
		return false
	}
	cur := *selected
	if cur < 0 || cur >= len(labels) {
		cur = 0
		*selected = 0
	}

	height := opts.Height
	if height <= 0 {
		height = 28
	}
	fontSize := opts.FontSize
	if fontSize <= 0 {
		fontSize = 11.5
	}
	padX := opts.PaddingX
	if padX <= 0 {
		padX = 10
	}

	seg := ui.Row(c.Key(key)).
		Height(height).
		Padding(3).
		Radius(999).
		Background(pal.SegBg).
		InsetShadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.06))
	if opts.Width > 0 {
		seg.Width(opts.Width)
	}

	animIdx := seg.AnimateTo("thumb", float32(cur), ui.Animation{
		Spring: ui.SpringOptions{Stiffness: 290, Damping: 25},
	})
	n := float32(len(labels))
	pillColor := pal.SegPill
	rimTop := pal.RimTop
	pillShadow := pal.ShadowPill

	seg.Draw(func(p *ui.Painter, r ui.Rect) {
		innerW := r.W - 6
		innerH := r.H - 6
		if innerW <= 0 || innerH <= 0 || n <= 0 {
			return
		}
		segW := innerW / n
		thumbRect := ui.Rect{
			X: r.X + 3 + animIdx*segW,
			Y: r.Y + 3,
			W: segW,
			H: innerH,
		}
		rad := innerH / 2
		p.Shadow(thumbRect, rad, 0, 2, 8, 0, pillShadow)
		p.Fill(thumbRect, pillColor, rad)
		p.Stroke(thumbRect, rimTop.Alpha(0.55), rad, 0.75)
	})

	changed := false
	seg.Children(func() {
		for i, lbl := range labels {
			active := i == cur
			txtCol := pal.Ink2
			weight := 500
			if active {
				txtCol = pal.Ink1
				weight = 600
			}
			item := ui.ButtonBase(c.Key(i)).
				Grow(1).
				FillHeight().
				Center().
				Padding(0, padX).
				Radius(999).
				Cursor(ui.CursorPointer).
				Label(lbl)
			item.Children(func() {
				ui.Text(c, lbl).
					FontSize(fontSize).
					FontWeight(weight).
					TextColor(txtCol).
					NoWrap()
			})
			if item.Clicked() && i != cur {
				*selected = i
				changed = true
			}
		}
	})
	return changed
}

// LiquidProgressBar draws a rounded liquid progress tube with smooth width interpolation
// and an optional living shimmer sweep when active (idle when shimmer=false -> 0% GPU).
func LiquidProgressBar(c *ui.Context, pal Palette, key string, ratio float32, fillColor ui.Color, height float32, opacity float32, shimmer bool) ui.Element {
	clamped := clamp01(ratio)
	if height <= 0 {
		height = 6
	}
	if opacity <= 0 {
		opacity = 1
	}

	bar := ui.Box(c.Key(key)).
		FillWidth().
		Height(height).
		Radius(height / 2).
		Background(pal.Track).
		Clip()

	animRatio := bar.AnimateTo("fill", clamped, ui.Animation{
		Spring: ui.SpringOptions{Stiffness: 140, Damping: 22},
	})
	fillCol := fillColor.Alpha(float32(fillColor.A) / 255.0 * opacity)
	sheenColor := ui.RGBA(255, 255, 255, 0.38)
	if pal.Dark {
		sheenColor = ui.RGBA(255, 255, 255, 0.24)
	}

	bar.Draw(func(p *ui.Painter, r ui.Rect) {
		w := r.W * clamp01(animRatio)
		if w < 1 {
			return
		}
		rad := r.H / 2
		fillRect := ui.Rect{X: r.X, Y: r.Y, W: w, H: r.H}
		p.Fill(fillRect, fillCol, rad)

		if shimmer && w > 24 && !p.Preferences().ReduceMotion {
			p.AnimationFrameRate(30)
			// 3.2s periodic shimmer sweep matching prototype.html liquidSweep.
			ms := float64(p.Now().UnixMilli()%3200) / 3200.0
			if ms < 0.58 {
				t := float32(ms / 0.58)
				sweepW := max(w*0.42, 28)
				sx := r.X - sweepW + (w+sweepW*1.6)*t
				p.Clip(fillRect, rad, func() {
					p.FillGradient(ui.Rect{X: sx, Y: r.Y, W: sweepW, H: r.H}, ui.LinearGradient{
						Angle: 90,
						Stops: []ui.GradientStop{
							{Color: ui.Transparent, At: 0},
							{Color: sheenColor, At: 0.5},
							{Color: ui.Transparent, At: 1},
						},
					}, 0)
				})
			}
		}
	})
	return bar
}

// SegmentColor returns the restrained Apple system color for a context breakdown bucket.
func SegmentColor(pal Palette, key model.ContextSegmentKey) ui.Color {
	if pal.Dark {
		switch key {
		case model.ContextSegSystem, model.ContextSegInput:
			return ui.Hex("#5E5CE6") // Apple Indigo Dark
		case model.ContextSegTools:
			return ui.Hex("#40C8E0") // Apple Teal Dark
		case model.ContextSegToolOutput, model.ContextSegCache:
			return ui.Hex("#0A84FF") // Apple Blue Dark
		case model.ContextSegMessages, model.ContextSegOutput:
			return ui.Hex("#30D158") // Apple Green Dark
		case model.ContextSegSummary:
			return ui.Hex("#FF9F0A") // Apple Amber Dark
		default:
			return pal.Ink2
		}
	}
	switch key {
	case model.ContextSegSystem, model.ContextSegInput:
		return ui.Hex("#5856D6") // Apple Indigo
	case model.ContextSegTools:
		return ui.Hex("#32ADE6") // Apple Teal
	case model.ContextSegToolOutput, model.ContextSegCache:
		return ui.Hex("#007AFF") // Apple Blue
	case model.ContextSegMessages, model.ContextSegOutput:
		return ui.Hex("#34C759") // Apple Green
	case model.ContextSegSummary:
		return ui.Hex("#FF9500") // Apple Amber
	default:
		return pal.Ink2
	}
}

// SegmentedContextBar draws a macOS-Storage-style multi-color stacked context window bar
// with smooth spring width interpolation, subtle segment dividers, optional danger ring,
// and pointer hover segment hit-testing (returning the hovered segment index or -1).
func SegmentedContextBar(
	c *ui.Context,
	pal Palette,
	key string,
	totalRatio float32,
	segments []model.ContextSegment,
	height float32,
	opacity float32,
	shimmer bool,
	warnBorder bool,
	highlightIdx int,
) (ui.Element, int) {
	clamped := clamp01(totalRatio)
	if height <= 0 {
		height = 8
	}
	if opacity <= 0 {
		opacity = 1
	}

	var segSum int64
	for _, s := range segments {
		if s.Tokens > 0 {
			segSum += s.Tokens
		}
	}
	if segSum <= 0 {
		fill := pal.Accent
		if warnBorder {
			fill = pal.Danger
		}
		return LiquidProgressBar(c, pal, key, totalRatio, fill, height, opacity, shimmer), -1
	}

	bar := ui.Box(c.Key(key)).
		FillWidth().
		Height(height).
		Radius(height / 2).
		Background(pal.Track).
		Clip()
	if warnBorder {
		bar.Border(1.2, pal.Danger.Alpha(0.75))
	}

	animRatio := bar.AnimateTo("fill", clamped, ui.Animation{
		Spring: ui.SpringOptions{Stiffness: 140, Damping: 22},
	})

	// Compute normalized segment fractions once for both hit-testing and drawing.
	fracs := make([]float32, len(segments))
	for i, s := range segments {
		if s.Tokens > 0 {
			fracs[i] = float32(s.Tokens) / float32(segSum)
		}
	}

	hoveredIdx := -1
	if px, _, over := bar.PointerPosition(); over && bar.Hovered() {
		b := bar.Bounds()
		fillW := b.W * clamp01(animRatio)
		if fillW > 1 && px >= 0 && px <= fillW {
			cursor := float32(0)
			for i, fr := range fracs {
				if fr <= 0 {
					continue
				}
				sw := fillW * fr
				if px >= cursor && px <= cursor+sw {
					hoveredIdx = i
					break
				}
				cursor += sw
			}
		}
	}

	activeHighlight := highlightIdx
	if hoveredIdx >= 0 {
		activeHighlight = hoveredIdx
	}

	colors := make([]ui.Color, len(segments))
	for i, s := range segments {
		colors[i] = SegmentColor(pal, s.Key)
	}
	sepCol := ui.RGBA(255, 255, 255, 0.72)
	sheenColor := ui.RGBA(255, 255, 255, 0.34)
	if pal.Dark {
		sepCol = ui.RGBA(18, 22, 32, 0.75)
		sheenColor = ui.RGBA(255, 255, 255, 0.22)
	}

	bar.Draw(func(p *ui.Painter, r ui.Rect) {
		fillW := r.W * clamp01(animRatio)
		if fillW < 1 {
			return
		}
		rad := r.H / 2
		fillRect := ui.Rect{X: r.X, Y: r.Y, W: fillW, H: r.H}

		p.Clip(fillRect, rad, func() {
			// Ensure very small non-zero segments remain visible (>= 2.5px) while fitting fillW.
			widths := make([]float32, len(fracs))
			var sumW float32
			for i, fr := range fracs {
				if fr <= 0 {
					continue
				}
				w := fillW * fr
				if fillW >= 18 && w < 2.5 {
					w = 2.5
				}
				widths[i] = w
				sumW += w
			}
			scale := float32(1)
			if sumW > 0 {
				scale = fillW / sumW
			}

			x := r.X
			var dividers []float32
			for i, w := range widths {
				if w <= 0 {
					continue
				}
				sw := w * scale
				segAlpha := opacity
				if activeHighlight >= 0 {
					if i == activeHighlight {
						segAlpha = 1.0
					} else {
						segAlpha = opacity * 0.38
					}
				}
				if x > r.X+0.5 {
					dividers = append(dividers, x)
				}
				p.Fill(ui.Rect{X: x, Y: r.Y, W: sw + 0.4, H: r.H}, colors[i].Alpha(segAlpha), 0)
				x += sw
			}
			for _, dx := range dividers {
				p.Fill(ui.Rect{X: dx - 0.6, Y: r.Y, W: 1.2, H: r.H}, sepCol, 0)
			}

			if shimmer && fillW > 24 && !p.Preferences().ReduceMotion {
				p.AnimationFrameRate(30)
				ms := float64(p.Now().UnixMilli()%3200) / 3200.0
				if ms < 0.58 {
					t := float32(ms / 0.58)
					sweepW := max(fillW*0.42, 28)
					sx := r.X - sweepW + (fillW+sweepW*1.6)*t
					p.FillGradient(ui.Rect{X: sx, Y: r.Y, W: sweepW, H: r.H}, ui.LinearGradient{
						Angle: 90,
						Stops: []ui.GradientStop{
							{Color: ui.Transparent, At: 0},
							{Color: sheenColor, At: 0.5},
							{Color: ui.Transparent, At: 1},
						},
					}, 0)
				}
			}
		})
	})

	return bar, hoveredIdx
}

// DrawVibrantLiquidWash paints a luminous translucent Apple Liquid Glass wash
// on top of real OS Acrylic/Liquid Glass vibrancy so the window keeps crisp
// sky-to-lavender glass depth (even over stark white windows) while still
// letting real blurred desktop content shine through underneath.
func DrawVibrantLiquidWash(p *ui.Painter, r ui.Rect, pal Palette) {
	if pal.Dark {
		p.FillGradient(r, ui.LinearGradient{
			Angle: 135,
			Stops: []ui.GradientStop{
				{Color: ui.RGBA(14, 24, 48, 0.56), At: 0},
				{Color: ui.RGBA(22, 27, 52, 0.52), At: 0.52},
				{Color: ui.RGBA(16, 22, 42, 0.58), At: 1},
			},
		}, 0)
	} else {
		p.FillGradient(r, ui.LinearGradient{
			Angle: 135,
			Stops: []ui.GradientStop{
				{Color: ui.RGBA(162, 198, 248, 0.46), At: 0},
				{Color: ui.RGBA(206, 218, 248, 0.40), At: 0.50},
				{Color: ui.RGBA(214, 196, 238, 0.46), At: 1},
			},
		}, 0)
	}

	maxDim := float32(math.Max(float64(r.W), float64(r.H)))
	p.FillRadialGradient(r, ui.RadialGradient{
		Center: ui.Point{X: 0.18, Y: 0.20},
		Radius: maxDim * 0.55,
		Stops: []ui.GradientStop{
			{Color: pal.BackdropGlow1.Alpha(0.24), At: 0},
			{Color: ui.Transparent, At: 1},
		},
	}, 0)
	p.FillRadialGradient(r, ui.RadialGradient{
		Center: ui.Point{X: 0.84, Y: 0.24},
		Radius: maxDim * 0.50,
		Stops: []ui.GradientStop{
			{Color: pal.BackdropGlow2.Alpha(0.20), At: 0},
			{Color: ui.Transparent, At: 1},
		},
	}, 0)
	p.FillRadialGradient(r, ui.RadialGradient{
		Center: ui.Point{X: 0.56, Y: 0.86},
		Radius: maxDim * 0.48,
		Stops: []ui.GradientStop{
			{Color: pal.BackdropGlow3.Alpha(0.14), At: 0},
			{Color: ui.Transparent, At: 1},
		},
	}, 0)
	// Crisp 1px top specular glass rim along the window edge.
	p.Line(r.X, r.Y+0.5, r.X+r.W, r.Y+0.5, 1, pal.RimTop.Alpha(0.75))
}

// DrawAtmosphericBackdrop paints a subtle Apple-inspired organic gradient backdrop
// when system Vibrancy is unavailable (e.g., non-vibrant window or headless testing),
// ensuring Liquid Glass panes refract rich luminous depth.
func DrawAtmosphericBackdrop(p *ui.Painter, r ui.Rect, pal Palette) {
	p.FillGradient(r, ui.LinearGradient{
		Angle: 135,
		Stops: []ui.GradientStop{
			{Color: pal.BackdropFrom, At: 0},
			{Color: pal.BackdropMid, At: 0.48},
			{Color: pal.BackdropTo, At: 1},
		},
	}, 0)

	// Soft luminous radial ambient orbs (static when idle for 0% GPU).
	maxDim := float32(math.Max(float64(r.W), float64(r.H)))
	p.FillRadialGradient(r, ui.RadialGradient{
		Center: ui.Point{X: 0.20, Y: 0.22},
		Radius: maxDim * 0.55,
		Stops: []ui.GradientStop{
			{Color: pal.BackdropGlow1, At: 0},
			{Color: ui.Transparent, At: 1},
		},
	}, 0)
	p.FillRadialGradient(r, ui.RadialGradient{
		Center: ui.Point{X: 0.80, Y: 0.28},
		Radius: maxDim * 0.50,
		Stops: []ui.GradientStop{
			{Color: pal.BackdropGlow2, At: 0},
			{Color: ui.Transparent, At: 1},
		},
	}, 0)
	p.FillRadialGradient(r, ui.RadialGradient{
		Center: ui.Point{X: 0.52, Y: 0.84},
		Radius: maxDim * 0.48,
		Stops: []ui.GradientStop{
			{Color: pal.BackdropGlow3, At: 0},
			{Color: ui.Transparent, At: 1},
		},
	}, 0)
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
