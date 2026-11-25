package tray

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// palette holds self-contained Apple Liquid Glass material colors aligned with
// docs/prototype.html (lines 22-83). During final merge with Track 2 these can
// be unified with internal/ui/theme.
type palette struct {
	Dark          bool
	Ink1          ui.Color
	Ink2          ui.Color
	Ink3          ui.Color
	Accent        ui.Color
	AccentSoft    ui.Color
	Indigo        ui.Color
	Green         ui.Color
	Amber         ui.Color
	AmberSoft     ui.Color
	Danger        ui.Color
	FlyoutGlass   ui.Color // Translucent wash over OS vibrancy/acrylic
	FlyoutOpaque  ui.Color // Fallback surface when OS vibrancy is unavailable
	SegBg         ui.Color
	SegPill       ui.Color
	SegPillBorder ui.Color
	CardBg        ui.Color
	CardHover     ui.Color
	Divider       ui.Color
	Track         ui.Color
	DotIdle       ui.Color
	HoverColBg    ui.Color
	RimTop        ui.Color
	RimEdge       ui.Color
	PopoverBg     ui.Color
	PopoverGlass  ui.Color
	Shadow        ui.Color
}

var (
	lightPalette = palette{
		Dark:          false,
		Ink1:          ui.RGBA(17, 17, 19, 0.90),
		Ink2:          ui.RGBA(17, 17, 19, 0.56),
		Ink3:          ui.RGBA(17, 17, 19, 0.36),
		Accent:        ui.Hex("#007AFF"),
		AccentSoft:    ui.RGBA(0, 122, 255, 0.12),
		Indigo:        ui.Hex("#5856D6"),
		Green:         ui.Hex("#34C759"),
		Amber:         ui.Hex("#E58500"),
		AmberSoft:     ui.RGBA(229, 133, 0, 0.12),
		Danger:        ui.Hex("#FF3B30"),
		FlyoutGlass:   ui.RGBA(255, 255, 255, 0.38),
		FlyoutOpaque:  ui.RGBA(246, 248, 252, 0.97),
		SegBg:         ui.RGBA(17, 17, 19, 0.068),
		SegPill:       ui.RGBA(255, 255, 255, 0.88),
		SegPillBorder: ui.RGBA(255, 255, 255, 0.94),
		CardBg:        ui.RGBA(255, 255, 255, 0.44),
		CardHover:     ui.RGBA(255, 255, 255, 0.68),
		Divider:       ui.RGBA(17, 17, 19, 0.08),
		Track:         ui.RGBA(17, 17, 19, 0.09),
		DotIdle:       ui.RGBA(17, 17, 19, 0.09),
		HoverColBg:    ui.RGBA(17, 17, 19, 0.055),
		RimTop:        ui.RGBA(255, 255, 255, 0.86),
		RimEdge:       ui.RGBA(255, 255, 255, 0.54),
		PopoverBg:     ui.RGBA(252, 253, 255, 1.0),
		PopoverGlass:  ui.RGBA(252, 253, 255, 1.0),
		Shadow:        ui.RGBA(15, 23, 42, 0.18),
	}

	darkPalette = palette{
		Dark:          true,
		Ink1:          ui.RGBA(255, 255, 255, 0.93),
		Ink2:          ui.RGBA(255, 255, 255, 0.56),
		Ink3:          ui.RGBA(255, 255, 255, 0.36),
		Accent:        ui.Hex("#0A84FF"),
		AccentSoft:    ui.RGBA(10, 132, 255, 0.22),
		Indigo:        ui.Hex("#5E5CE6"),
		Green:         ui.Hex("#30D158"),
		Amber:         ui.Hex("#FF9F0A"),
		AmberSoft:     ui.RGBA(255, 159, 10, 0.16),
		Danger:        ui.Hex("#FF453A"),
		FlyoutGlass:   ui.RGBA(24, 27, 36, 0.42),
		FlyoutOpaque:  ui.RGBA(26, 29, 38, 0.97),
		SegBg:         ui.RGBA(255, 255, 255, 0.085),
		SegPill:       ui.RGBA(255, 255, 255, 0.18),
		SegPillBorder: ui.RGBA(255, 255, 255, 0.22),
		CardBg:        ui.RGBA(255, 255, 255, 0.065),
		CardHover:     ui.RGBA(255, 255, 255, 0.12),
		Divider:       ui.RGBA(255, 255, 255, 0.095),
		Track:         ui.RGBA(255, 255, 255, 0.115),
		DotIdle:       ui.RGBA(255, 255, 255, 0.11),
		HoverColBg:    ui.RGBA(255, 255, 255, 0.08),
		RimTop:        ui.RGBA(255, 255, 255, 0.24),
		RimEdge:       ui.RGBA(255, 255, 255, 0.13),
		PopoverBg:     ui.RGBA(26, 30, 42, 1.0),
		PopoverGlass:  ui.RGBA(26, 30, 42, 1.0),
		Shadow:        ui.RGBA(0, 0, 0, 0.48),
	}
)

func resolvePalette(themeMode string, systemDark bool) palette {
	switch themeMode {
	case "dark":
		return darkPalette
	case "light":
		return lightPalette
	default:
		if systemDark {
			return darkPalette
		}
		return lightPalette
	}
}

func svgIcon(viewBox, shapes string, strokeWidth string) *ui.SVG {
	return ui.MustParseSVG([]byte(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="%s" fill="none" stroke="currentColor" stroke-width="%s" stroke-linecap="round" stroke-linejoin="round">%s</svg>`,
		viewBox, strokeWidth, shapes,
	)))
}

var (
	iconBolt = svgIcon("0 0 24 24", `<path d="M13 2L3 14h7l-1 8 10-12h-7l1-8z"/>`, "2")
	iconPerf = svgIcon("0 0 24 24", `<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`, "1.9")
	iconTree = svgIcon("0 0 24 24", `<circle cx="12" cy="5" r="2.5"/><circle cx="6" cy="19" r="2.5"/><circle cx="18" cy="19" r="2.5"/><path d="M12 7.5V13M12 13L6 16.5M12 13L18 16.5"/>`, "1.9")
	iconDown = svgIcon("0 0 24 24", `<path d="M6 9l6 6 6-6"/>`, "2.2")
	iconGear = svgIcon("0 0 24 24", `<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9c.26.604.852.997 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>`, "1.75")
)

// TrayIconPNG generates the 32x32 3-bar Pulse system tray icon matching
// docs/prototype.html (#trayToggleBtn). On macOS it renders pure monochrome
// black so AppKit tints it as a template image; on Windows/Linux it accents
// the center bar in Apple Blue with crisp high-contrast neutral side bars.
func TrayIconPNG(darkTaskbar bool) []byte {
	const sz = 32
	img := image.NewNRGBA(image.Rect(0, 0, sz, sz))

	type bar struct {
		x, y, w, h float64
		col        color.NRGBA
	}

	var leftCol, midCol, rightCol color.NRGBA
	if runtime.GOOS == "darwin" {
		leftCol = color.NRGBA{R: 0, G: 0, B: 0, A: 145}
		midCol = color.NRGBA{R: 0, G: 0, B: 0, A: 255}
		rightCol = color.NRGBA{R: 0, G: 0, B: 0, A: 220}
	} else if darkTaskbar {
		leftCol = color.NRGBA{R: 230, G: 236, B: 245, A: 145}
		midCol = color.NRGBA{R: 10, G: 132, B: 255, A: 255}
		rightCol = color.NRGBA{R: 245, G: 248, B: 255, A: 235}
	} else {
		leftCol = color.NRGBA{R: 28, G: 32, B: 42, A: 145}
		midCol = color.NRGBA{R: 0, G: 122, B: 255, A: 255}
		rightCol = color.NRGBA{R: 20, G: 24, B: 34, A: 230}
	}

	bars := []bar{
		{x: 3.5, y: 17.5, w: 5.5, h: 11.0, col: leftCol},
		{x: 13.25, y: 10.5, w: 5.5, h: 18.0, col: midCol},
		{x: 23.0, y: 4.5, w: 5.5, h: 24.0, col: rightCol},
	}

	for y := range sz {
		for x := range sz {
			px, py := float64(x)+0.5, float64(y)+0.5
			for _, b := range bars {
				r := b.w / 2
				cov := roundedRectCoverage(px, py, b.x, b.y, b.w, b.h, r)
				if cov > 0 {
					a := uint8(math.Round(float64(b.col.A) * cov))
					if a > 0 {
						img.SetNRGBA(x, y, color.NRGBA{R: b.col.R, G: b.col.G, B: b.col.B, A: a})
					}
				}
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func roundedRectCoverage(px, py, x, y, w, h, r float64) float64 {
	cx := x + w/2
	cy := y + h/2
	hx := w/2 - r
	hy := h/2 - r
	dx := math.Abs(px-cx) - hx
	dy := math.Abs(py-cy) - hy
	ox := math.Max(dx, 0)
	oy := math.Max(dy, 0)
	dist := math.Hypot(ox, oy) + math.Min(math.Max(dx, dy), 0) - r
	if dist <= -0.5 {
		return 1
	}
	if dist >= 0.5 {
		return 0
	}
	return 0.5 - dist
}

// FormatUSD formats a dollar figure with 3 decimal places ($0.048), matching prototype.html.
func FormatUSD(v float64) string {
	if v < 0 {
		v = 0
	}
	return fmt.Sprintf("$%.3f", v)
}

// FormatTokensK formats token counts cleanly (48.2K, 1.0M, 850).
func FormatTokensK(n int64) string {
	if n < 0 {
		n = 0
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000.0)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000.0)
	}
	return fmt.Sprintf("%d", n)
}

// FormatPct formats a [0, 1] ratio as a percentage with 1 decimal (94.2%).
func FormatPct(ratio float64) string {
	if ratio < 0 {
		ratio = 0
	}
	return fmt.Sprintf("%.1f%%", ratio*100)
}

// FormatSec formats a duration in seconds with 2 decimals (1.62s).
func FormatSec(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

// FormatTPS formats Pure Decode TPS with 1 decimal (126.4).
func FormatTPS(tps float64) string {
	if tps < 0 {
		tps = 0
	}
	return fmt.Sprintf("%.1f", tps)
}

// TruncateRunes truncates s to at most maxRunes runes, appending an ellipsis if shortened.
func TruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "…"
}
