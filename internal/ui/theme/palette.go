package theme

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

const (
	ModeSystem = "system"
	ModeLight  = "light"
	ModeDark   = "dark"
)

// Palette defines the Apple Liquid Glass material & typography tokens
// matching docs/prototype.html (light and dark modes).
// Exported so Track 3 (Tray Flyout) can share the exact visual language after merge.
type Palette struct {
	Dark bool

	// Typography grayscale hierarchy (Apple neutral ink levels).
	Ink1 ui.Color
	Ink2 ui.Color
	Ink3 ui.Color

	// Accent palette: single Apple Blue primary + restrained Amber secondary/warn + Danger (>80%).
	Accent     ui.Color
	AccentSoft ui.Color
	Amber      ui.Color
	AmberSoft  ui.Color
	Danger     ui.Color

	// Translucent Liquid Glass surface layers.
	ShellBg     ui.Color
	SidebarBg   ui.Color
	SidebarTint ui.Color
	FlyoutBg    ui.Color
	CardBg      ui.Color
	CardHover   ui.Color
	CardActive  ui.Color
	PopoverBg   ui.Color
	PopoverTint ui.Color

	// Specular bevel rims, hairlines, tracks, segmented controls.
	RimTop     ui.Color
	RimEdge    ui.Color
	RimBottom  ui.Color
	Divider    ui.Color
	Track      ui.Color
	SegBg      ui.Color
	SegPill    ui.Color
	Sheen      ui.Color
	DotIdle    ui.Color
	TreeStroke ui.Color

	// Elevation shadows.
	ShadowShell   ui.Color
	ShadowCard    ui.Color
	ShadowPill    ui.Color
	ShadowPopover ui.Color

	// Fallback organic atmospheric backdrop when system Vibrancy is unavailable.
	BackdropFrom  ui.Color
	BackdropMid   ui.Color
	BackdropTo    ui.Color
	BackdropGlow1 ui.Color
	BackdropGlow2 ui.Color
	BackdropGlow3 ui.Color
}

// LightPalette returns the Apple Light Liquid Glass token set.
func LightPalette() Palette {
	return Palette{
		Dark: false,

		Ink1: ui.RGBA(17, 17, 19, 0.90),
		Ink2: ui.RGBA(17, 17, 19, 0.56),
		Ink3: ui.RGBA(17, 17, 19, 0.36),

		Accent:     ui.Hex("#007AFF"),
		AccentSoft: ui.RGBA(0, 122, 255, 0.12),
		Amber:      ui.Hex("#E58500"),
		AmberSoft:  ui.RGBA(229, 133, 0, 0.16),
		Danger:     ui.Hex("#FF3B30"),

		ShellBg:     ui.RGBA(246, 248, 252, 0.56),
		SidebarBg:   ui.RGBA(255, 255, 255, 0.50),
		SidebarTint: ui.RGBA(255, 255, 255, 0.24),
		FlyoutBg:    ui.RGBA(255, 255, 255, 0.60),
		CardBg:      ui.RGBA(255, 255, 255, 0.48),
		CardHover:   ui.RGBA(255, 255, 255, 0.68),
		CardActive:  ui.RGBA(255, 255, 255, 0.78),
		PopoverBg:   ui.RGBA(255, 255, 255, 0.84),
		PopoverTint: ui.RGBA(255, 255, 255, 0.50),

		RimTop:     ui.RGBA(255, 255, 255, 0.90),
		RimEdge:    ui.RGBA(255, 255, 255, 0.56),
		RimBottom:  ui.RGBA(15, 23, 42, 0.08),
		Divider:    ui.RGBA(17, 17, 19, 0.08),
		Track:      ui.RGBA(17, 17, 19, 0.09),
		SegBg:      ui.RGBA(17, 17, 19, 0.065),
		SegPill:    ui.RGBA(255, 255, 255, 0.88),
		Sheen:      ui.RGBA(255, 255, 255, 0.46),
		DotIdle:    ui.RGBA(17, 17, 19, 0.09),
		TreeStroke: ui.RGBA(17, 17, 19, 0.16),

		ShadowShell:   ui.RGBA(15, 23, 42, 0.18),
		ShadowCard:    ui.RGBA(15, 23, 42, 0.05),
		ShadowPill:    ui.RGBA(0, 0, 0, 0.09),
		ShadowPopover: ui.RGBA(15, 23, 42, 0.20),

		BackdropFrom:  ui.Hex("#88B4E8"),
		BackdropMid:   ui.Hex("#B5C8EE"),
		BackdropTo:    ui.Hex("#D4C2E4"),
		BackdropGlow1: ui.RGBA(56, 189, 248, 0.28),
		BackdropGlow2: ui.RGBA(129, 140, 248, 0.24),
		BackdropGlow3: ui.RGBA(251, 191, 36, 0.16),
	}
}

// DarkPalette returns the Apple Dark Liquid Glass token set.
func DarkPalette() Palette {
	return Palette{
		Dark: true,

		Ink1: ui.RGBA(255, 255, 255, 0.93),
		Ink2: ui.RGBA(255, 255, 255, 0.58),
		Ink3: ui.RGBA(255, 255, 255, 0.36),

		Accent:     ui.Hex("#0A84FF"),
		AccentSoft: ui.RGBA(10, 132, 255, 0.22),
		Amber:      ui.Hex("#FF9F0A"),
		AmberSoft:  ui.RGBA(255, 159, 10, 0.20),
		Danger:     ui.Hex("#FF453A"),

		ShellBg:     ui.RGBA(22, 26, 36, 0.58),
		SidebarBg:   ui.RGBA(255, 255, 255, 0.07),
		SidebarTint: ui.RGBA(22, 26, 36, 0.34),
		FlyoutBg:    ui.RGBA(26, 30, 40, 0.64),
		CardBg:      ui.RGBA(255, 255, 255, 0.075),
		CardHover:   ui.RGBA(255, 255, 255, 0.12),
		CardActive:  ui.RGBA(255, 255, 255, 0.155),
		PopoverBg:   ui.RGBA(28, 32, 44, 0.88),
		PopoverTint: ui.RGBA(20, 24, 34, 0.60),

		RimTop:     ui.RGBA(255, 255, 255, 0.24),
		RimEdge:    ui.RGBA(255, 255, 255, 0.12),
		RimBottom:  ui.RGBA(0, 0, 0, 0.35),
		Divider:    ui.RGBA(255, 255, 255, 0.095),
		Track:      ui.RGBA(255, 255, 255, 0.12),
		SegBg:      ui.RGBA(255, 255, 255, 0.085),
		SegPill:    ui.RGBA(255, 255, 255, 0.18),
		Sheen:      ui.RGBA(255, 255, 255, 0.13),
		DotIdle:    ui.RGBA(255, 255, 255, 0.11),
		TreeStroke: ui.RGBA(255, 255, 255, 0.20),

		ShadowShell:   ui.RGBA(0, 0, 0, 0.52),
		ShadowCard:    ui.RGBA(0, 0, 0, 0.24),
		ShadowPill:    ui.RGBA(0, 0, 0, 0.36),
		ShadowPopover: ui.RGBA(0, 0, 0, 0.56),

		BackdropFrom:  ui.Hex("#0B1426"),
		BackdropMid:   ui.Hex("#141B35"),
		BackdropTo:    ui.Hex("#0D172B"),
		BackdropGlow1: ui.RGBA(14, 165, 233, 0.28),
		BackdropGlow2: ui.RGBA(99, 102, 241, 0.26),
		BackdropGlow3: ui.RGBA(249, 115, 22, 0.16),
	}
}

// ResolvePalette picks Light or Dark palette from themeMode ("system"|"light"|"dark")
// and the host window's current appearance.
func ResolvePalette(c *ui.Context, themeMode string) Palette {
	switch strings.ToLower(strings.TrimSpace(themeMode)) {
	case ModeLight:
		return LightPalette()
	case ModeDark:
		return DarkPalette()
	default:
		if c != nil && c.Theme().Dark {
			return DarkPalette()
		}
		return LightPalette()
	}
}

// ApplyTheme aligns mygo's built-in widget theme (switches, scrollbars, focus rings)
// with the resolved Apple Liquid Glass palette.
func ApplyTheme(c *ui.Context, pal Palette) {
	if c == nil {
		return
	}
	var base *ui.Theme
	if pal.Dark {
		base = ui.DarkTheme()
	} else {
		base = ui.LightTheme()
	}
	t := *base.WithAccent(pal.Accent)
	t.Text = pal.Ink1
	t.TextMuted = pal.Ink2
	t.Border = pal.Divider
	t.Surface = pal.CardBg
	t.SurfaceHover = pal.CardHover
	t.InactiveAccent = pal.Accent
	t.Radius = 10
	t.Scrollbar = pal.Track
	t.ScrollbarWidth = 4
	c.SetTheme(&t)
}
