package theme

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func assertRGBA(t *testing.T, name string, got ui.Color, r, g, b, a uint8) {
	t.Helper()
	if got.R != r || got.G != g || got.B != b || got.A != a {
		t.Fatalf("%s: got RGBA(%d,%d,%d,%d), want RGBA(%d,%d,%d,%d)", name, got.R, got.G, got.B, got.A, r, g, b, a)
	}
}

func TestPaletteNewTokensLightAndDark(t *testing.T) {
	light := LightPalette()
	if light.Dark {
		t.Fatal("LightPalette().Dark should be false")
	}
	assertRGBA(t, "light.Indigo", light.Indigo, 88, 86, 214, 255)
	assertRGBA(t, "light.IndigoSoft", light.IndigoSoft, 88, 86, 214, 33)
	assertRGBA(t, "light.Teal", light.Teal, 50, 173, 230, 255)
	assertRGBA(t, "light.Green", light.Green, 52, 199, 89, 255)
	assertRGBA(t, "light.RingBg", light.RingBg, 17, 17, 19, 19)
	assertRGBA(t, "light.BarSep", light.BarSep, 255, 255, 255, 199)
	// Preserve Amber & AmberSoft unchanged.
	assertRGBA(t, "light.Amber", light.Amber, 229, 133, 0, 255)
	assertRGBA(t, "light.AmberSoft", light.AmberSoft, 229, 133, 0, 41)

	dark := DarkPalette()
	if !dark.Dark {
		t.Fatal("DarkPalette().Dark should be true")
	}
	assertRGBA(t, "dark.Indigo", dark.Indigo, 94, 92, 230, 255)
	assertRGBA(t, "dark.IndigoSoft", dark.IndigoSoft, 94, 92, 230, 56)
	assertRGBA(t, "dark.Teal", dark.Teal, 64, 200, 224, 255)
	assertRGBA(t, "dark.Green", dark.Green, 48, 209, 88, 255)
	assertRGBA(t, "dark.RingBg", dark.RingBg, 255, 255, 255, 28)
	assertRGBA(t, "dark.BarSep", dark.BarSep, 18, 22, 32, 199)
	// Preserve Amber & AmberSoft unchanged.
	assertRGBA(t, "dark.Amber", dark.Amber, 255, 159, 10, 255)
	assertRGBA(t, "dark.AmberSoft", dark.AmberSoft, 255, 159, 10, 51)
}

func TestResolvePalette(t *testing.T) {
	if p := ResolvePalette(nil, ModeLight); p.Dark || p.Indigo != LightPalette().Indigo {
		t.Fatalf("ResolvePalette(nil, ModeLight) unexpected: %+v", p.Indigo)
	}
	if p := ResolvePalette(nil, " DARK "); !p.Dark || p.Indigo != DarkPalette().Indigo {
		t.Fatalf("ResolvePalette(nil, DARK) unexpected: %+v", p.Indigo)
	}
	if p := ResolvePalette(nil, ModeSystem); p.Dark {
		t.Fatalf("ResolvePalette(nil, ModeSystem) should default to light when context is nil")
	}

	var sysLight, sysDark Palette
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(ui.LightTheme())
		sysLight = ResolvePalette(c, ModeSystem)
		c.SetTheme(ui.DarkTheme())
		sysDark = ResolvePalette(c, ModeSystem)
	}, 32, 32)
	tt.Frame()

	if sysLight.Dark || sysLight.Indigo != LightPalette().Indigo {
		t.Fatalf("ResolvePalette(lightCtx, ModeSystem) expected light palette")
	}
	if !sysDark.Dark || sysDark.Indigo != DarkPalette().Indigo {
		t.Fatalf("ResolvePalette(darkCtx, ModeSystem) expected dark palette")
	}
}
