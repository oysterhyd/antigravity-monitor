package quota

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func cliProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}

// prependOrAppendCLI prefers native .exe binaries over .cmd/.bat/.ps1 shims on Windows
// so background quota polls never flash a console host window via cmd.exe wrappers.
func prependOrAppendCLI(candidates []string, lookedUp string) []string {
	ext := strings.ToLower(filepath.Ext(lookedUp))
	if ext == ".cmd" || ext == ".bat" || ext == ".ps1" {
		return append(candidates, lookedUp)
	}
	return append([]string{lookedUp}, candidates...)
}

// execCLICommand runs command completely hidden and binds it to a Windows Job Object
// configured with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE so any grandchild processes spawned
// by agy.exe are terminated cleanly when the command finishes or times out.
func execCLICommand(command *exec.Cmd) ([]byte, error) {
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = nil

	if err := command.Start(); err != nil {
		return nil, err
	}

	var job windows.Handle
	if h, err := windows.CreateJobObject(nil, nil); err == nil && h != 0 {
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		_, setErr := windows.SetInformationJobObject(
			h,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		)
		if setErr == nil && command.Process != nil {
			if proc, openErr := windows.OpenProcess(
				windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
				false,
				uint32(command.Process.Pid),
			); openErr == nil {
				if windows.AssignProcessToJobObject(h, proc) == nil {
					job = h
				}
				_ = windows.CloseHandle(proc)
			}
		}
		if job == 0 {
			_ = windows.CloseHandle(h)
		}
	}
	if job != 0 {
		defer windows.CloseHandle(job)
	}

	if err := command.Wait(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
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
