package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/oysterhyd/antigravity-monitor/internal/model"
)

const (
	appDirName      = "antigravity-monitor"
	settingsFile    = "settings.json"
	minWarnFraction = 0.0
	maxWarnFraction = 1.0
)

// DefaultPath resolves the standard settings.json location:
//   - Windows: %AppData%\antigravity-monitor\settings.json (via os.UserConfigDir)
//   - Unix/macOS: $XDG_CONFIG_HOME/antigravity-monitor/settings.json or ~/.config/antigravity-monitor/settings.json
func DefaultPath() (string, error) {
	if runtime.GOOS == "windows" {
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			return filepath.Join(dir, appDirName, settingsFile), nil
		}
	} else {
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			return filepath.Join(xdg, appDirName, settingsFile), nil
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, ".config", appDirName, settingsFile), nil
		}
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			return filepath.Join(dir, appDirName, settingsFile), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve config directory: %w", err)
	}
	return filepath.Join(home, ".config", appDirName, settingsFile), nil
}

// Normalize clamps and validates Settings fields while preserving intentional
// values such as ContextWarnThreshold == 0 (warning disabled).
func Normalize(s model.Settings) model.Settings {
	switch strings.ToLower(strings.TrimSpace(s.ThemeMode)) {
	case "light":
		s.ThemeMode = "light"
	case "dark":
		s.ThemeMode = "dark"
	default:
		s.ThemeMode = "system"
	}

	if s.ContextWarnThreshold < minWarnFraction {
		s.ContextWarnThreshold = model.DefaultSettings().ContextWarnThreshold
	} else if s.ContextWarnThreshold > maxWarnFraction {
		s.ContextWarnThreshold = maxWarnFraction
	}

	s.PricingOverridePath = strings.TrimSpace(s.PricingOverridePath)
	return s
}

// LoadFrom reads settings from path, merging with model.DefaultSettings() so
// missing fields inherit clean defaults. If the file does not exist, it returns
// DefaultSettings() with nil error.
func LoadFrom(path string) (model.Settings, error) {
	defaults := model.DefaultSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaults, nil
		}
		return defaults, fmt.Errorf("read settings %q: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return defaults, nil
	}

	out := defaults
	if err := json.Unmarshal(data, &out); err != nil {
		return defaults, fmt.Errorf("parse settings %q: %w", path, err)
	}
	return Normalize(out), nil
}

// SaveTo atomically writes settings as formatted JSON to path using a temp file
// in the destination directory followed by rename.
func SaveTo(path string, s model.Settings) error {
	s = Normalize(s)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir %q: %w", dir, err)
	}

	payload, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	payload = append(payload, '\n')

	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp settings file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp settings file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp settings file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp settings file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace settings %q: %w", path, err)
	}
	cleanup = false
	return nil
}

// Store manages thread-safe access and atomic persistence of model.Settings.
type Store struct {
	mu       sync.RWMutex
	path     string
	settings model.Settings
}

// NewStore opens or initializes a Store at path. When path is empty,
// DefaultPath() is used. If the settings file does not yet exist, defaults are
// loaded without creating the file until Save/Update is called.
func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	s, err := LoadFrom(path)
	if err != nil {
		return nil, err
	}
	return &Store{
		path:     path,
		settings: s,
	}, nil
}

// Path returns the resolved settings file path.
func (st *Store) Path() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.path
}

// Get returns a copy of the currently loaded settings.
func (st *Store) Get() model.Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.settings
}

// Reload re-reads settings from disk and updates the in-memory copy.
func (st *Store) Reload() (model.Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, err := LoadFrom(st.path)
	if err != nil {
		return st.settings, err
	}
	st.settings = s
	return s, nil
}

// Save atomically persists s to disk and updates the in-memory copy.
func (st *Store) Save(s model.Settings) error {
	s = Normalize(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := SaveTo(st.path, s); err != nil {
		return err
	}
	st.settings = s
	return nil
}

// Update applies mut to a copy of the current settings, normalizes, and
// atomically saves the result to disk.
func (st *Store) Update(mut func(*model.Settings)) (model.Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	next := st.settings
	if mut != nil {
		mut(&next)
	}
	next = Normalize(next)
	if err := SaveTo(st.path, next); err != nil {
		return st.settings, err
	}
	st.settings = next
	return next, nil
}
