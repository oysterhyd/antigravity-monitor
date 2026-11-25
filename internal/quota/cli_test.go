package quota

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const usageOutput = `{
	"status":"SUCCESS","num_turns":0,
	"command":{"name":"usage","data":{"groups":[
		{"buckets":[
			{"id":"3p-weekly","window":"weekly","remaining_fraction":1,"reset_time":"1970-01-01T00:00:00Z"},
			{"id":"3p-5h","window":"5h","remaining_fraction":0,"reset_time":"2026-11-25T07:00:00Z"}
		]},
		{"buckets":[
			{"id":"gemini-5h","window":"5h","remaining_fraction":0.78,"reset_time":"2026-11-25T06:45:24Z"},
			{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.60,"reset_time":"2026-12-01T06:30:22Z"}
		]}
	]}}
}`

func TestParseUsageReadsEachReportedWindow(t *testing.T) {
	reading, err := parseUsage([]byte(usageOutput))
	if err != nil {
		t.Fatal(err)
	}
	if reading.Gemini.FiveHour.RemainingPct != 78 || reading.Gemini.Weekly.RemainingPct != 60 ||
		reading.ClaudeGPT.FiveHour.RemainingPct != 0 || reading.ClaudeGPT.Weekly.RemainingPct != 100 {
		t.Fatalf("wrong quotas: %+v", reading)
	}
	if reading.Gemini.Weekly.ResetAt.Format(time.RFC3339) != "2026-12-01T06:30:22Z" ||
		!reading.ClaudeGPT.Weekly.ResetAt.IsZero() {
		t.Fatalf("wrong reset times: %+v", reading)
	}
}

func TestParseUsageRejectsIncompleteOrNonCommandResponses(t *testing.T) {
	for _, output := range []string{
		strings.Replace(usageOutput, `"remaining_fraction":0.78,`, "", 1),
		strings.Replace(usageOutput, `"remaining_fraction":0.78`, `"remaining_fraction":1.1`, 1),
		strings.Replace(usageOutput, `"num_turns":0`, `"num_turns":1`, 1),
		`{"status":"SUCCESS","response":"plain text"}`,
	} {
		if _, err := parseUsage([]byte(output)); !errors.Is(err, ErrCLIUsage) {
			t.Fatalf("expected usage error, got %v for %s", err, output)
		}
	}
}

func TestSupportsUsage(t *testing.T) {
	for _, test := range []struct {
		version string
		want    bool
	}{
		{"1.1.10", false}, {"1.1.11", true}, {"1.5.16\n", true}, {"2.0.0", true}, {"", false},
	} {
		if got := supportsUsage(test.version); got != test.want {
			t.Errorf("supportsUsage(%q) = %t, want %t", test.version, got, test.want)
		}
	}
}

func TestCLIValidationExpiresWhenBinaryChanges(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "agy.exe")
	if err := os.WriteFile(binary, []byte("original"), 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	validatedCLI.Lock()
	validatedCLI.path, validatedCLI.info = binary, info
	validatedCLI.Unlock()
	t.Cleanup(func() { forgetCLI(binary) })

	if !cliSupportsUsage(context.Background(), binary) {
		t.Fatal("unchanged binary missed validation cache")
	}
	if err := os.WriteFile(binary, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if cliSupportsUsage(context.Background(), binary) {
		t.Fatal("replacement reused validation for the old binary")
	}
}

func TestCleanCLIEnvStripsACPVars(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GEMINI_HOME=C:\\t3\\isolated",
		"antigravity_app_data_dir=C:\\t3\\isolated\\antigravity-acp",
		"AGY_ACP_FORCE_FILE_STORAGE=1",
		"USERPROFILE=C:\\Users\\oyster",
	}
	got := cleanCLIEnv(in)
	if len(got) != 2 || got[0] != "PATH=/usr/bin" || got[1] != "USERPROFILE=C:\\Users\\oyster" {
		t.Fatalf("cleanCLIEnv = %v, want only PATH and USERPROFILE", got)
	}
}
