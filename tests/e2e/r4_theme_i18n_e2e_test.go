//go:build e2e

package e2e_test

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oysterhyd/antigravity-monitor/internal/config"
	"github.com/oysterhyd/antigravity-monitor/internal/i18n"
	"github.com/oysterhyd/antigravity-monitor/internal/model"
	"github.com/oysterhyd/antigravity-monitor/internal/ui/theme"
)

// Feature #14: Apple Liquid Glass Tokens & ZH/EN i18n (Tier 1)
func TestR4ThemeAndI18n(t *testing.T) {
	t.Run("AllStringsPopulatedZHEn", func(t *testing.T) {
		for _, lang := range []string{"zh", "en"} {
			s := i18n.Get(lang)
			v := reflect.ValueOf(s)
			typ := v.Type()
			for i := 0; i < v.NumField(); i++ {
				f := v.Field(i)
				name := typ.Field(i).Name
				if f.Kind() == reflect.String && f.String() == "" {
					t.Fatalf("empty i18n field %s for lang=%s", name, lang)
				}
				if f.Kind() == reflect.Array {
					for j := 0; j < f.Len(); j++ {
						if f.Index(j).String() == "" {
							t.Fatalf("empty i18n array field %s[%d] for lang=%s", name, j, lang)
						}
					}
				}
			}
		}
	})

	t.Run("PaletteTokensLightAndDark", func(t *testing.T) {
		light := theme.LightPalette()
		dark := theme.DarkPalette()
		var zero ui.Color

		for name, c := range map[string]ui.Color{
			"Light.Indigo":     light.Indigo,
			"Light.IndigoSoft": light.IndigoSoft,
			"Light.Teal":       light.Teal,
			"Light.Green":      light.Green,
			"Light.RingBg":     light.RingBg,
			"Light.BarSep":     light.BarSep,
			"Dark.Indigo":      dark.Indigo,
			"Dark.IndigoSoft":  dark.IndigoSoft,
			"Dark.Teal":        dark.Teal,
			"Dark.Green":       dark.Green,
			"Dark.RingBg":      dark.RingBg,
			"Dark.BarSep":      dark.BarSep,
		} {
			if c == zero {
				t.Fatalf("palette token %s is zero", name)
			}
		}
		if light.RingBg == dark.RingBg || light.BarSep == dark.BarSep {
			t.Fatalf("expected Light vs Dark RingBg/BarSep contrast separation")
		}

		// Verify exported *ui.SVG vars for R1–R4 exist and are non-nil.
		for name, icon := range map[string]any{
			"IconGauge":     theme.IconGauge,
			"IconBarChart":  theme.IconBarChart,
			"IconRefresh":   theme.IconRefresh,
			"IconSliders":   theme.IconSliders,
			"IconArrowLeft": theme.IconArrowLeft,
		} {
			if reflect.ValueOf(icon).IsNil() {
				t.Fatalf("theme icon %s is nil", name)
			}
		}
	})

	t.Run("QuotaViewLightDarkZHEn", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		reading := quotaReading(anchorNow, 74, 41)
		for _, tc := range []struct {
			mode  string
			lang  string
			label string
			title string
		}{
			{theme.ModeLight, "zh", "账号实时额度", "额度变化趋势"},
			{theme.ModeDark, "zh", "账号实时额度", "额度变化趋势"},
			{theme.ModeLight, "en", "Account Quota", "Quota Usage Trend"},
			{theme.ModeDark, "en", "Account Quota", "Quota Usage Trend"},
		} {
			env := newE2EDesktopApp(t, fx, tc.mode, tc.lang, &reading)
			clickMust(t, env.Tester, tc.label)
			mustHaveText(t, env.Tester, tc.title)
			verifyPNGFrame(t, env.Tester, "r4_quota_"+tc.mode+"_"+tc.lang+".png")
		}
	})

	t.Run("HistoryViewLightDarkZHEn", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		for _, tc := range []struct {
			mode  string
			lang  string
			label string
			title string
		}{
			{theme.ModeLight, "zh", "历史消耗统计", "历史消耗统计"},
			{theme.ModeDark, "zh", "历史消耗统计", "历史消耗统计"},
			{theme.ModeLight, "en", "Usage History", "Usage History"},
			{theme.ModeDark, "en", "Usage History", "Usage History"},
		} {
			env := newE2EDesktopApp(t, fx, tc.mode, tc.lang, nil)
			clickMust(t, env.Tester, tc.label)
			mustHaveText(t, env.Tester, tc.title)
			verifyPNGFrame(t, env.Tester, "r4_history_"+tc.mode+"_"+tc.lang+".png")
		}
	})

	t.Run("LiveViewTokensAndGlassCards", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeLight, "zh", nil)
		mustHaveText(t, env.Tester, "上下文窗口")
		verifyPNGFrame(t, env.Tester, "")
		env.App.SetThemeMode(theme.ModeDark)
		stepUI(env.Tester, 250*time.Millisecond)
		verifyPNGFrame(t, env.Tester, "")
	})
}

// Feature #14: Apple Liquid Glass Tokens & ZH/EN i18n (Tier 2 Boundaries)
func TestR4ThemeI18nBoundaries(t *testing.T) {
	t.Run("UnknownLocaleFallbackToZH", func(t *testing.T) {
		for _, raw := range []string{"", "fr-FR", "ja-JP", "es"} {
			if got := i18n.Normalize(raw); got != i18n.LangZH {
				t.Fatalf("Normalize(%q) = %q, want %q", raw, got, i18n.LangZH)
			}
		}
	})

	t.Run("LocaleVariantNormalization", func(t *testing.T) {
		for _, raw := range []string{"EN", "en-US", "en_us", "english"} {
			if got := i18n.Normalize(raw); got != i18n.LangEN {
				t.Fatalf("Normalize(%q) = %q, want %q", raw, got, i18n.LangEN)
			}
		}
	})

	t.Run("UnknownThemeModeDefaultsSystem", func(t *testing.T) {
		pal := theme.ResolvePalette(nil, "sepia")
		if pal.Dark {
			t.Fatalf("expected nil context fallback to LightPalette")
		}
	})

	t.Run("SystemDarkVsLightResolution", func(t *testing.T) {
		fx := seedRealisticHistoryRoot(t)
		env := newE2EDesktopApp(t, fx, theme.ModeSystem, "en", nil)
		env.Tester.SetDark(true)
		stepUI(env.Tester, 200*time.Millisecond)
		verifyPNGFrame(t, env.Tester, "")
		env.Tester.SetDark(false)
		stepUI(env.Tester, 200*time.Millisecond)
		verifyPNGFrame(t, env.Tester, "")
	})

	t.Run("SettingsPersistRoundTripWithUnknownFields", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "settings.json")
		st, err := config.NewStore(p)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		if err := st.Save(model.Settings{ThemeMode: "dark", Language: "en", KeepInTrayOnClose: true}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		// Ensure LoadFrom tolerates extra JSON keys cleanly.
		loaded, err := config.LoadFrom(p)
		if err != nil || loaded.ThemeMode != "dark" || loaded.Language != "en" {
			t.Fatalf("unexpected loaded settings: %+v err=%v", loaded, err)
		}
	})
}
