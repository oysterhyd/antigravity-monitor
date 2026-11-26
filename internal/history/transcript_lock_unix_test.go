//go:build !windows

package history

import (
	"os"
	"testing"
)

func lockFileExclusively(t *testing.T, path string) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("cannot simulate unreadable file with chmod 000 as root")
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod 000 %q: %v", path, err)
	}
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			_ = os.Chmod(path, 0o600)
		}
	}
	t.Cleanup(unlock)
	return unlock
}
