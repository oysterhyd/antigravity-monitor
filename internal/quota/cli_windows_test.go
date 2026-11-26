package quota

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCLIProcessAttributesFullyHidden(t *testing.T) {
	attrs := cliProcessAttributes()
	if attrs == nil {
		t.Fatal("cliProcessAttributes returned nil")
	}
	if !attrs.HideWindow {
		t.Fatal("expected HideWindow=true to suppress child window flashes")
	}
	if attrs.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("expected CREATE_NO_WINDOW in CreationFlags, got %#x", attrs.CreationFlags)
	}
}

func TestPrependOrAppendCLIPrefersNativeExeOverScriptShims(t *testing.T) {
	base := []string{`C:\Users\me\AppData\Local\agy\bin\agy.exe`}
	gotCmd := prependOrAppendCLI(append([]string(nil), base...), `C:\npm\agy.cmd`)
	if gotCmd[0] != base[0] || gotCmd[1] != `C:\npm\agy.cmd` {
		t.Fatalf("expected .cmd shim appended after .exe candidates, got %v", gotCmd)
	}
	gotExe := prependOrAppendCLI(append([]string(nil), base...), `C:\tools\agy.exe`)
	if gotExe[0] != `C:\tools\agy.exe` {
		t.Fatalf("expected direct .exe prepended, got %v", gotExe)
	}
}

func TestT3CLIPathUsesOnlyActiveRelease(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "active.json")
	releaseID := strings.Repeat("abcdef01", 8)
	if err := os.WriteFile(active, fmt.Appendf(nil, `{"releaseId":%q}`, releaseID), 0o600); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "versions", releaseID, "agy.exe")
	if got := t3CLIPath(dir); got != want {
		t.Fatalf("binary path = %q, want %q", got, want)
	}
	if err := os.WriteFile(active, []byte(`{"releaseId":"..\\..\\elsewhere"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := t3CLIPath(dir); got != "" {
		t.Fatalf("accepted invalid release directory: %q", got)
	}
}
