package quota

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrCLISetup = errors.New("could not prepare the quota reader")
	ErrCLIUsage = errors.New("Antigravity CLI quota unavailable")
)

var validatedCLI struct {
	sync.Mutex
	path string
	info os.FileInfo
}

func ReadCLI(ctx context.Context) (Reading, error) {
	binary, err := findCLI(ctx)
	if err != nil {
		return Reading{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := runCLI(ctx, binary, "--output-format", "json", "--mode", "plan", "--print", "/usage")
	if err != nil {
		forgetCLI(binary)
		return Reading{}, ErrCLIUsage
	}
	reading, err := parseUsage(output)
	if err != nil {
		forgetCLI(binary)
	}
	return reading, err
}

func findCLI(ctx context.Context) (string, error) {
	candidates := cliCandidates()
	if binary, err := exec.LookPath("agy"); err == nil {
		candidates = prependOrAppendCLI(candidates, binary)
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if cliSupportsUsage(ctx, candidate) {
			return candidate, nil
		}
	}
	binary, err := prepareCLI(ctx)
	if err != nil {
		return "", err
	}
	if !cliSupportsUsage(ctx, binary) {
		return "", ErrCLISetup
	}
	return binary, nil
}

func cliSupportsUsage(ctx context.Context, binary string) bool {
	info, err := os.Stat(binary)
	if err != nil {
		return false
	}
	validatedCLI.Lock()
	previous := validatedCLI.info
	validated := binary == validatedCLI.path && previous != nil &&
		info.Size() == previous.Size() && info.ModTime().Equal(previous.ModTime()) && os.SameFile(info, previous)
	validatedCLI.Unlock()
	if validated {
		return true
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	version, err := runCLI(ctx, binary, "--version")
	if err != nil || !supportsUsage(string(version)) {
		return false
	}
	validatedCLI.Lock()
	validatedCLI.path, validatedCLI.info = binary, info
	validatedCLI.Unlock()
	return true
}

func forgetCLI(binary string) {
	validatedCLI.Lock()
	defer validatedCLI.Unlock()
	if validatedCLI.path == binary {
		validatedCLI.path, validatedCLI.info = "", nil
	}
}

func runCLI(ctx context.Context, binary string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = os.TempDir()
	command.Stdin = nil
	command.Env = cleanCLIEnv(os.Environ())
	command.WaitDelay = time.Second
	command.SysProcAttr = cliProcessAttributes()
	return execCLICommand(command)
}

// cleanCLIEnv strips ACP-scoped isolation variables (e.g. when Monitor is launched
// from inside a T3 Code terminal) so `agy /usage` uses the standard CLI profile
// instead of failing auth against an isolated ACP directory.
func cleanCLIEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(key) {
		case "GEMINI_HOME", "ANTIGRAVITY_APP_DATA_DIR", "AGY_ACP_FORCE_FILE_STORAGE":
			continue
		}
		out = append(out, kv)
	}
	return out
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
