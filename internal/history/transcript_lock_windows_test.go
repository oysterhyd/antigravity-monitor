//go:build windows

package history

import (
	"syscall"
	"testing"
)

// lockFileExclusively opens path with zero sharing on Windows so os.Stat succeeds
// while os.Open fails with ERROR_SHARING_VIOLATION until the returned unlock func runs.
func lockFileExclusively(t *testing.T, path string) func() {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString(%q): %v", path, err)
	}
	h, err := syscall.CreateFile(
		p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, // exclusive lock: deny read/write/delete sharing
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("CreateFile exclusive lock %q: %v", path, err)
	}
	closed := false
	unlock := func() {
		if !closed {
			closed = true
			_ = syscall.CloseHandle(h)
		}
	}
	t.Cleanup(unlock)
	return unlock
}
