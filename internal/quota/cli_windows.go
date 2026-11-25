package quota

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func cliProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}

func cliCandidates() []string {
	var candidates []string
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		candidates = append(candidates, filepath.Join(local, "agy", "bin", "agy.exe"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
		if binary := t3CLIPath(filepath.Join(home, ".t3", "tools", "antigravity-acp", "win32-"+arch)); binary != "" {
			candidates = append(candidates, binary)
		}
	}
	if managed, err := managedCLIPath(); err == nil {
		candidates = append(candidates, managed)
	}
	return candidates
}

func t3CLIPath(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "active.json"))
	if err != nil {
		return ""
	}
	var active struct {
		ReleaseID string `json:"releaseId"`
	}
	if json.Unmarshal(raw, &active) != nil || len(active.ReleaseID) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(active.ReleaseID); err != nil {
		return ""
	}
	return filepath.Join(dir, "versions", active.ReleaseID, "agy.exe")
}

func managedCLIPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "antigravity-monitor", "quota-reader", "agy.exe"), nil
}

func prepareCLI(ctx context.Context) (string, error) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return "", ErrCLISetup
	}
	binary, err := managedCLIPath()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrCLISetup, err)
	}
	client := &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || !trustedCLIURL(req.URL) {
				return ErrCLISetup
			}
			return nil
		},
	}
	manifestURL := cliManifestBase + "/manifests/windows_" + runtime.GOARCH + ".json"
	if err := downloadCLI(ctx, client, manifestURL, binary); err != nil {
		return "", fmt.Errorf("%w: %w", ErrCLISetup, err)
	}
	return binary, nil
}
