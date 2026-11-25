package quota

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	ErrCLINotFound = errors.New("Antigravity CLI not found")
	ErrCLIOutdated = errors.New("Antigravity CLI 1.1.11 or later required")
	ErrCLIUsage    = errors.New("Antigravity CLI quota unavailable")
)

func ReadCLI(ctx context.Context) (Reading, error) {
	binary, err := exec.LookPath("agy")
	if errors.Is(err, exec.ErrNotFound) && runtime.GOOS == "windows" && os.Getenv("LOCALAPPDATA") != "" {
		binary, err = exec.LookPath(filepath.Join(os.Getenv("LOCALAPPDATA"), "agy", "bin", "agy.exe"))
	}
	if err != nil {
		return Reading{}, ErrCLINotFound
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	version, err := runCLI(ctx, binary, "--version")
	if err != nil {
		return Reading{}, ErrCLIUsage
	}
	if !supportsUsage(string(version)) {
		return Reading{}, ErrCLIOutdated
	}
	output, err := runCLI(ctx, binary, "--output-format", "json", "--mode", "plan", "--print", "/usage")
	if err != nil {
		return Reading{}, ErrCLIUsage
	}
	return parseUsage(output)
}

func runCLI(ctx context.Context, binary string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = os.TempDir()
	command.WaitDelay = time.Second
	command.SysProcAttr = cliProcessAttributes()
	return command.Output()
}

func supportsUsage(version string) bool {
	version, _, _ = strings.Cut(strings.TrimSpace(version), "+")
	version, _, _ = strings.Cut(version, "-")
	major, tail, _ := strings.Cut(strings.TrimPrefix(version, "v"), ".")
	minor, patch, _ := strings.Cut(tail, ".")
	majorNumber, majorErr := strconv.Atoi(major)
	minorNumber, minorErr := strconv.Atoi(minor)
	patchNumber, patchErr := strconv.Atoi(patch)
	return majorErr == nil && minorErr == nil && patchErr == nil &&
		(majorNumber > 1 || majorNumber == 1 && (minorNumber > 1 || minorNumber == 1 && patchNumber >= 11))
}

func parseUsage(output []byte) (Reading, error) {
	var result struct {
		Status   string
		NumTurns int `json:"num_turns"`
		Command  struct {
			Name string
			Data struct {
				Groups []struct {
					Buckets []struct {
						ID                string
						Window            string
						RemainingFraction *float64  `json:"remaining_fraction"`
						ResetTime         time.Time `json:"reset_time"`
					}
				}
			}
		}
	}
	if err := json.Unmarshal(output, &result); err != nil || result.Status != "SUCCESS" ||
		result.NumTurns != 0 || result.Command.Name != "usage" {
		return Reading{}, ErrCLIUsage
	}

	buckets := make(map[string]WindowQuota, 4)
	for _, group := range result.Command.Data.Groups {
		for _, bucket := range group.Buckets {
			if bucket.RemainingFraction == nil ||
				*bucket.RemainingFraction < 0 || *bucket.RemainingFraction > 1 {
				continue
			}
			window := WindowQuota{RemainingPct: *bucket.RemainingFraction * 100}
			if bucket.ResetTime.After(time.Unix(0, 0)) {
				window.ResetAt = bucket.ResetTime
			}
			buckets[bucket.ID+"/"+bucket.Window] = window
		}
	}
	geminiFiveHour, geminiFiveHourOK := buckets["gemini-5h/5h"]
	geminiWeekly, geminiWeeklyOK := buckets["gemini-weekly/weekly"]
	claudeFiveHour, claudeFiveHourOK := buckets["3p-5h/5h"]
	claudeWeekly, claudeWeeklyOK := buckets["3p-weekly/weekly"]
	if !geminiFiveHourOK || !geminiWeeklyOK || !claudeFiveHourOK || !claudeWeeklyOK {
		return Reading{}, ErrCLIUsage
	}
	return Reading{
		Gemini:    PoolQuota{FiveHour: geminiFiveHour, Weekly: geminiWeekly},
		ClaudeGPT: PoolQuota{FiveHour: claudeFiveHour, Weekly: claudeWeekly},
	}, nil
}
