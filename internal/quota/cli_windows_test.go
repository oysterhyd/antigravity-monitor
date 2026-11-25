package quota

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
