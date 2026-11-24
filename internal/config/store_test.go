package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

func TestStoreMissingFileReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	st, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	got := st.Get()
	want := model.DefaultSettings()
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file not to be created on initial load")
	}
}

func TestStoreSaveReloadAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	st, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	updated, err := st.Update(func(s *model.Settings) {
		s.LaunchAtLogin = true
		s.KeepInTrayOnClose = false
		s.AlwaysOnTop = true
		s.ContextWarnThreshold = 0.85
		s.AutoFollowActive = false
		s.ThemeMode = "dark"
		s.PricingOverridePath = "  C:/custom/pricing.json  "
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if updated.PricingOverridePath != "C:/custom/pricing.json" {
		t.Fatalf("expected trimmed path, got %q", updated.PricingOverridePath)
	}

	// Verify no temporary files are left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("unexpected directory entries: %v", entries)
	}

	// Verify JSON does not include any TPS mode field.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "tps_mode") {
		t.Fatalf("settings.json must not contain tps_mode: %s", string(raw))
	}

	// Reload from a fresh store instance.
	st2, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore reload failed: %v", err)
	}
	if st2.Get() != updated {
		t.Fatalf("reloaded %+v != saved %+v", st2.Get(), updated)
	}
}

func TestLoadPartialJSONPreservesDefaultsAndNormalizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	partial := `{"always_on_top": true, "theme_mode": "invalid-mode", "context_warn_threshold": 1.5}`
	if err := os.WriteFile(path, []byte(partial), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	got, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}
	if !got.AlwaysOnTop {
		t.Fatalf("expected AlwaysOnTop=true")
	}
	if !got.KeepInTrayOnClose || !got.AutoFollowActive {
		t.Fatalf("expected omitted bool fields to keep defaults: %+v", got)
	}
	if got.ThemeMode != "system" {
		t.Fatalf("expected invalid ThemeMode normalized to system, got %q", got.ThemeMode)
	}
	if got.ContextWarnThreshold != 1.0 {
		t.Fatalf("expected threshold clamped to 1.0, got %v", got.ContextWarnThreshold)
	}
}

func TestLoadAllowsZeroWarnThresholdAsDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := `{"context_warn_threshold": 0}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	got, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}
	if got.ContextWarnThreshold != 0 {
		t.Fatalf("expected 0 threshold to stay 0 (disabled), got %v", got.ContextWarnThreshold)
	}
}

func TestLoadCorruptJSONReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{bad json"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := LoadFrom(path); err == nil {
		t.Fatalf("expected error for corrupt JSON")
	}
}

func TestStoreConcurrentUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	st, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.Update(func(s *model.Settings) {
				s.AlwaysOnTop = i%2 == 0
				if i%3 == 0 {
					s.ThemeMode = "dark"
				} else {
					s.ThemeMode = "light"
				}
			})
			if err != nil {
				t.Errorf("concurrent update %d failed: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// Resulting file must always be valid JSON.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var parsed model.Settings
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("resulting file is not valid JSON: %v", err)
	}
}

func TestDefaultPathNonEmpty(t *testing.T) {
	p, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath failed: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(p), "/antigravity-monitor/settings.json") {
		t.Fatalf("unexpected default path: %q", p)
	}
}
