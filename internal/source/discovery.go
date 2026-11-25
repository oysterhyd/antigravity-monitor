package source

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// DiscoverRootDirs returns all local Antigravity data root directories in deterministic order:
//  1. Native Antigravity desktop/IDE root (~/.gemini/antigravity)
//  2. Standalone Antigravity CLI root (~/.gemini/antigravity-cli), if present
//  3. T3 Code Antigravity ACP provider roots (~/.t3/userdata/providers/antigravity/*/antigravity-acp)
//  4. T3 Code Antigravity CLI roots (~/.t3/userdata/providers/antigravity/*/antigravity-cli), if populated
func DiscoverRootDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return []string{DefaultRootDir()}
	}
	return discoverRootDirsFromHome(home)
}

func discoverRootDirsFromHome(home string) []string {
	var roots []string
	seen := make(map[string]bool)
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		clean := filepath.Clean(dir)
		key := normalizeRootKey(clean)
		if seen[key] {
			return
		}
		seen[key] = true
		roots = append(roots, clean)
	}

	// 1. Primary native root (always included so Monitor picks it up once created).
	add(filepath.Join(home, ".gemini", "antigravity"))

	// 2. Standalone Antigravity CLI root if it holds Antigravity storage.
	cliRoot := filepath.Join(home, ".gemini", "antigravity-cli")
	if hasAntigravityStore(cliRoot) {
		add(cliRoot)
	}

	// 3. T3 Code isolated Antigravity provider roots.
	t3Base := filepath.Join(home, ".t3", "userdata", "providers", "antigravity")
	if entries, err := os.ReadDir(t3Base); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			acpRoot := filepath.Join(t3Base, name, "antigravity-acp")
			if isDir(acpRoot) {
				add(acpRoot)
			}
			t3CLIRoot := filepath.Join(t3Base, name, "antigravity-cli")
			if hasAntigravityStore(t3CLIRoot) {
				add(t3CLIRoot)
			}
		}
	}

	return roots
}

func dedupeRootDirs(dirs []string) []string {
	out := make([]string, 0, len(dirs))
	seen := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		clean := filepath.Clean(d)
		key := normalizeRootKey(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
	}
	return out
}

func normalizeRootKey(dir string) string {
	clean := filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func hasAntigravityStore(rootDir string) bool {
	if !isDir(rootDir) {
		return false
	}
	if _, err := os.Stat(filepath.Join(rootDir, "conversation_summaries.db")); err == nil {
		return true
	}
	return isDir(filepath.Join(rootDir, "conversations"))
}
