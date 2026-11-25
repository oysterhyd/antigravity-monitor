package theme

import (
	"image/color"
	"testing"

	"github.com/egoist/mygo/ui"
)

func hasNonZeroAlpha(pix []uint8) bool {
	for i := 3; i < len(pix); i += 4 {
		if pix[i] > 0 {
			return true
		}
	}
	return false
}

func TestAllExportedIconsValidAndRasterizable(t *testing.T) {
	icons := []struct {
		name string
		svg  *ui.SVG
	}{
		{"IconTree", IconTree},
		{"IconContext", IconContext},
		{"IconCost", IconCost},
		{"IconCache", IconCache},
		{"IconClock", IconClock},
		{"IconBolt", IconBolt},
		{"IconChevronDown", IconChevronDown},
		{"IconChevronLeft", IconChevronLeft},
		{"IconChevronRight", IconChevronRight},
		{"IconSettings", IconSettings},
		{"IconSun", IconSun},
		{"IconMoon", IconMoon},
		{"IconBurst", IconBurst},
		{"IconGauge", IconGauge},
		{"IconBarChart", IconBarChart},
		{"IconRefresh", IconRefresh},
		{"IconSliders", IconSliders},
		{"IconArrowLeft", IconArrowLeft},
	}

	ink := color.NRGBA{R: 17, G: 17, B: 19, A: 255}
	for _, tc := range icons {
		if tc.svg == nil {
			t.Fatalf("%s is nil", tc.name)
		}
		w, h := tc.svg.Size()
		if w != 24 || h != 24 {
			t.Fatalf("%s.Size() = (%v, %v), want (24, 24)", tc.name, w, h)
		}
		if unsup := tc.svg.Unsupported(); len(unsup) != 0 {
			t.Fatalf("%s.Unsupported() = %v, want empty", tc.name, unsup)
		}

		for _, sz := range []int{24, 14} {
			img := tc.svg.Image(sz, sz, ink)
			if img == nil || img.Bounds().Dx() != sz || img.Bounds().Dy() != sz {
				t.Fatalf("%s.Image(%d, %d) returned unexpected bounds", tc.name, sz, sz)
			}
			if !hasNonZeroAlpha(img.Pix) {
				t.Fatalf("%s.Image(%d, %d) rendered zero non-transparent pixels", tc.name, sz, sz)
			}
		}
	}
}

func TestNewIconsRenderInUITester(t *testing.T) {
	pal := LightPalette()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Gap(6).Children(func() {
			ui.Icon(c, IconGauge).Size(14.5, 14.5).TextColor(pal.Accent).Label("gauge")
			ui.Icon(c, IconBarChart).Size(14.5, 14.5).TextColor(pal.Indigo).Label("barchart")
			ui.Icon(c, IconRefresh).Size(12, 12).TextColor(pal.Ink2).Label("refresh")
			ui.Icon(c, IconSliders).Size(12, 12).TextColor(pal.Ink2).Label("sliders")
			ui.Icon(c, IconArrowLeft).Size(12, 12).TextColor(pal.Ink2).Label("arrowleft")
		})
	}, 160, 48)
	tt.Frame()

	for _, lbl := range []string{"gauge", "barchart", "refresh", "sliders", "arrowleft"} {
		r, ok := tt.Find(lbl)
		if !ok {
			t.Fatalf("expected icon label %q in layout", lbl)
		}
		if r.W <= 0 || r.H <= 0 {
			t.Fatalf("icon %q has non-positive size: %+v", lbl, r)
		}
	}
}
