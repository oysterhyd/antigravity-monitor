package e2e_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/oysterhyd/antigravity-monitor/internal/source"
)

// TestCompliance_NoUndocumentedProbingOrOAuthAccess enforces R2 security &
// compliance across all non-test production Go files:
//   - Rule A: Only the documented quota CLI runner may execute a process. No process/DLL token scraping.
//   - Rule B: No private OAuth / IDE credential store access.
//   - Rule C: No raw sockets/probing, restricted net/http, no undocumented local ports.
//   - Rule D: Strict read-only Antigravity SQLite guarantees (no immutable=1, no mutations).
func TestCompliance_NoUndocumentedProbingOrOAuthAccess(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()

	forbiddenScrapingLiterals := []string{
		"language_server",
		"csrf_token",
		"extension_server_csrf",
		"wmic process",
		"win32_process",
		"tasklist",
		"ps aux",
		"ps -ef",
		"pgrep",
		"lsof -i",
	}

	forbiddenCredentialLiterals := []string{
		"oauth_creds.json",
		"credentials.json",
		".gemini/oauth",
		"application_default_credentials.json",
		"google_accounts.json",
		"state.vscdb",
		"cryptunprotectdata",
		"credread",
		"seckeychain",
		"refresh_token",
		"oauth_token",
		"access_token",
	}

	forbiddenSyscallNames := map[string]bool{
		"NewLazyDLL":               true,
		"LoadDLL":                  true,
		"NewProc":                  true,
		"CreateToolhelp32Snapshot": true,
		"Process32First":           true,
		"OpenProcess":              true,
		"ReadProcessMemory":        true,
		"CryptUnprotectData":       true,
	}

	forbiddenXSysNames := map[string]bool{
		"NewLazySystemDLL":         true,
		"CryptUnprotectData":       true,
		"CreateToolhelp32Snapshot": true,
		"OpenProcess":              true,
	}

	forbiddenNetNames := []string{"Dial", "Listen", "Lookup"}

	reLoopbackURLPort := regexp.MustCompile(`(?i)https?://(127\.0\.0\.1|localhost|\[::1\]):\d+`)
	reLoopbackHostPort := regexp.MustCompile(`(?i)(127\.0\.0\.1|localhost):[0-9]{2,5}`)
	forbiddenRPCPaths := []string{
		"/exa.language_server_pb",
		"getuserstatus",
		"getcommandmodelconfigs",
	}

	sqlMutationKeywords := []string{
		"insert into",
		"update ",
		"delete from",
		"create table",
		"drop table",
		"alter table",
	}

	var violations []string
	scannedFiles := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "dist" || name == "vendor" || name == "tests" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		scannedFiles++

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		relSlash := filepath.ToSlash(rel)
		pkgDir := filepath.ToSlash(filepath.Dir(rel))

		fileAST, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			violations = append(violations, relSlash+": parse error: "+parseErr.Error())
			return nil
		}

		// Map local import aliases for Rule A & Rule C.
		importAliases := make(map[string]string) // local ident -> import path
		for _, imp := range fileAST.Imports {
			impPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			localName := filepath.Base(impPath)
			if imp.Name != nil {
				localName = imp.Name.Name
			}
			importAliases[localName] = impPath

			if impPath == "os/exec" && relSlash != "internal/quota/cli.go" {
				pos := fset.Position(imp.Pos())
				violations = append(violations, formatViolation(pos, "Rule A: forbidden import os/exec"))
			}
			// Rule C: net/http restricted strictly to internal/updater and internal/quota.
			if impPath == "net/http" && pkgDir != "internal/updater" && pkgDir != "internal/quota" {
				pos := fset.Position(imp.Pos())
				violations = append(violations, formatViolation(pos, "Rule C: net/http forbidden outside internal/updater and internal/quota (found in "+pkgDir+")"))
			}
			// Rule C: forbid unsafe across production code.
			if impPath == "unsafe" {
				pos := fset.Position(imp.Pos())
				violations = append(violations, formatViolation(pos, "Rule C: forbidden import unsafe"))
			}
		}

		isReadOnlyDBLayer := pkgDir == "internal/source" || pkgDir == "internal/history"

		ast.Inspect(fileAST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if ident, ok := node.X.(*ast.Ident); ok {
					impPath := importAliases[ident.Name]
					sel := node.Sel.Name
					if impPath == "syscall" && forbiddenSyscallNames[sel] {
						violations = append(violations, formatViolation(fset.Position(node.Pos()), "Rule A: forbidden syscall."+sel))
					}
					if strings.HasPrefix(impPath, "golang.org/x/sys") && forbiddenXSysNames[sel] {
						violations = append(violations, formatViolation(fset.Position(node.Pos()), "Rule A: forbidden x/sys."+sel))
					}
					if impPath == "net" {
						for _, pfx := range forbiddenNetNames {
							if strings.HasPrefix(sel, pfx) {
								violations = append(violations, formatViolation(fset.Position(node.Pos()), "Rule C: forbidden net."+sel))
							}
						}
					}
				}
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(node.Value)
				if err != nil {
					val = node.Value
				}
				lower := strings.ToLower(strings.ReplaceAll(val, "\\", "/"))
				pos := fset.Position(node.Pos())

				for _, pat := range forbiddenScrapingLiterals {
					if strings.Contains(lower, pat) {
						violations = append(violations, formatViolation(pos, "Rule A: forbidden process/token literal "+pat))
					}
				}
				for _, pat := range forbiddenCredentialLiterals {
					if strings.Contains(lower, pat) {
						violations = append(violations, formatViolation(pos, "Rule B: forbidden OAuth/credential literal "+pat))
					}
				}
				if reLoopbackURLPort.MatchString(val) || reLoopbackHostPort.MatchString(val) {
					violations = append(violations, formatViolation(pos, "Rule C: hardcoded loopback port literal "+val))
				}
				for _, rpc := range forbiddenRPCPaths {
					if strings.Contains(lower, rpc) {
						violations = append(violations, formatViolation(pos, "Rule C: undocumented RPC literal "+rpc))
					}
				}

				if isReadOnlyDBLayer {
					if strings.Contains(lower, "immutable=1") {
						violations = append(violations, formatViolation(pos, "Rule D: forbidden SQLite immutable=1 in "+pkgDir))
					}
					for _, kw := range sqlMutationKeywords {
						if strings.Contains(lower, kw) {
							violations = append(violations, formatViolation(pos, "Rule D: forbidden SQL mutation keyword "+strings.TrimSpace(kw)+" in "+pkgDir))
						}
					}
				}
			}
			return true
		})

		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if scannedFiles == 0 {
		t.Fatalf("expected non-zero production .go files scanned")
	}
	if len(violations) > 0 {
		t.Fatalf("compliance violations detected across %d production files:\n%s",
			scannedFiles, strings.Join(violations, "\n"))
	}
}

func formatViolation(pos token.Position, msg string) string {
	return pos.Filename + ":" + strconv.Itoa(pos.Line) + ": " + msg
}

// TestCompliance_ReadOnlySQLiteDoesNotMutateDBs verifies at runtime that reading
// WAL-mode SQLite databases via source.Provider never modifies .db file SHA-256 hashes.
func TestCompliance_ReadOnlySQLiteDoesNotMutateDBs(t *testing.T) {
	fx := seedBaselineMultiRangeFixture(t)
	fx.CheckpointSummaries(t)
	fx.Checkpoint(t, "conv-7d-claude")
	fx.Checkpoint(t, "conv-30d-mixed")
	// Leave conv-today-active uncheckpointed in WAL mode, and capture hashes before read.
	dbPaths := []string{
		filepath.Join(fx.RootDir, "conversation_summaries.db"),
		filepath.Join(fx.RootDir, "conversations", "conv-today-active.db"),
		filepath.Join(fx.RootDir, "conversations", "conv-7d-claude.db"),
		filepath.Join(fx.RootDir, "conversations", "conv-30d-mixed.db"),
		filepath.Join(fx.RootDir, "conversations", "orphan-sub-1.db"),
	}

	before := make(map[string]string, len(dbPaths))
	for _, p := range dbPaths {
		before[p] = sha256File(t, p)
	}

	prov, err := source.NewProvider(source.Options{
		RootDir:        fx.RootDir,
		MaxOpenDBs:     3,
		DisableWatcher: true,
	})
	if err != nil {
		t.Fatalf("source.NewProvider: %v", err)
	}
	defer prov.Close()

	if err := prov.Refresh(context.Background()); err != nil {
		t.Fatalf("prov.Refresh: %v", err)
	}
	snap := prov.Snapshot()
	if len(snap.Conversations) == 0 {
		t.Fatalf("expected conversations from synthetic fixture")
	}

	for _, p := range dbPaths {
		after := sha256File(t, p)
		if after != before[p] {
			t.Fatalf("read-only provider mutated %s: before=%s after=%s", p, before[p], after)
		}
	}
}
