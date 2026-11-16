package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// seedWatchdogSnapshot writes the resolved config the setup would have
// written, at the location the scheduled reaper reads.
func seedWatchdogSnapshot(t *testing.T, home, body string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(home, ".local", "state", "dot", "watchdog", "watchdog.yaml"), body)
}

// TestWatchdogReap_EndToEndDryRun runs a real pass against the live process
// table: the test binary itself is the (only possible) match, and dry-run
// mode plus a first sighting guarantee no candidate and no kill.
func TestWatchdogReap_EndToEndDryRun(t *testing.T) {
	home := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	seedWatchdogSnapshot(t, home, fmt.Sprintf(`
enabled: true
reaper:
  mode: dry-run
  cpu_threshold: 99
  match:
    orphan_only: false
    paths: ["%s"]
`, exe))

	if _, _, err := runDotForTest("--home", home, "watchdog", "reap"); err != nil {
		t.Fatalf("reap: %v", err)
	}

	samplesPath := filepath.Join(home, ".local", "state", "dot", "watchdog", "samples.json")
	if _, err := os.Stat(samplesPath); err != nil {
		t.Fatalf("samples.json not persisted: %v", err)
	}
	// A first sighting is never a candidate, so the log may not even exist;
	// when it does, it must contain neither a candidate nor a reap record.
	logPath := filepath.Join(home, "Library", "Logs", "dot", "watchdog.log")
	if logData, err := os.ReadFile(logPath); err == nil {
		for _, bad := range []string{`"event":"candidate"`, `"event":"reap"`} {
			if strings.Contains(string(logData), bad) {
				t.Fatalf("first-sighting dry-run logged %s:\n%s", bad, logData)
			}
		}
	}
}

func TestWatchdogReap_ScheduledRunStaysQuiet(t *testing.T) {
	home := t.TempDir()
	seedWatchdogSnapshot(t, home, "enabled: true\nreaper:\n  mode: dry-run\n")
	t.Setenv("DOT_WATCHDOG_RUN", "1")
	out, _, err := runDotForTest("--home", home, "watchdog", "reap")
	if err != nil {
		t.Fatalf("scheduled reap: %v", err)
	}
	if out != "" {
		t.Fatalf("a scheduled run wrote to stdout: %q", out)
	}
}

func TestWatchdogReap_MissingSnapshotIsActionable(t *testing.T) {
	_, _, err := runDotForTest("--home", t.TempDir(), "watchdog", "reap")
	if err == nil || !strings.Contains(err.Error(), "dot watchdog setup") {
		t.Fatalf("missing snapshot error must point at setup, got: %v", err)
	}
}

// An enforce-mode snapshot must not kill — or write anything — when the
// operator passes --dry-run: the flag forces dry-run mode for that pass.
func TestWatchdogReap_DryRunFlagOverridesEnforceSnapshot(t *testing.T) {
	home := t.TempDir()
	seedWatchdogSnapshot(t, home, "enabled: true\nreaper:\n  mode: enforce\n")
	if _, _, err := runDotForTest("--home", home, "watchdog", "reap", "--dry-run"); err != nil {
		t.Fatalf("reap --dry-run: %v", err)
	}
	stateDir := filepath.Join(home, ".local", "state", "dot", "watchdog")
	if _, err := os.Stat(filepath.Join(stateDir, "samples.json")); !os.IsNotExist(err) {
		t.Fatalf("--dry-run persisted samples: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "reap.lock")); !os.IsNotExist(err) {
		t.Fatalf("--dry-run touched the reap lock: %v", err)
	}
}

// A snapshot that exists but cannot be parsed is a damaged scheduled
// configuration: status must say so instead of claiming "no snapshot".
func TestWatchdogStatus_CorruptSnapshotSurfaces(t *testing.T) {
	home := t.TempDir()
	seedWatchdogSnapshot(t, home, "reaper: [broken")
	out, _, err := runDotForTest("--home", home, "watchdog", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "unreadable") {
		t.Fatalf("status must surface the corrupt snapshot: %q", out)
	}
	if strings.Contains(out, "no snapshot") {
		t.Fatalf("corrupt snapshot masqueraded as missing: %q", out)
	}
}

func TestWatchdogStatus_ReportsSnapshotAndDefaults(t *testing.T) {
	home := t.TempDir()
	out, _, err := runDotForTest("--home", home, "watchdog", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "no snapshot") {
		t.Fatalf("status must report the missing snapshot: %q", out)
	}

	seedWatchdogSnapshot(t, home, "enabled: true\nreaper:\n  mode: enforce\n")
	out, _, err = runDotForTest("--home", home, "watchdog", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"enforce", "50% sustained 30m0s", "Tracked processes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
}

func TestWatchdogLog_MissingAndSeeded(t *testing.T) {
	home := t.TempDir()
	out, _, err := runDotForTest("--home", home, "watchdog", "log")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if !strings.Contains(out, "No log file found") {
		t.Fatalf("missing log must say so: %q", out)
	}

	logPath := filepath.Join(home, "Library", "Logs", "dot", "watchdog.log")
	writeCLITestFile(t, logPath,
		"{\"ts\":\"2026-09-26T00:00:00Z\",\"level\":\"warn\",\"event\":\"candidate\",\"pid\":1}\n"+
			"{\"ts\":\"2026-09-26T00:05:00Z\",\"level\":\"warn\",\"event\":\"candidate\",\"pid\":2}\n"+
			"{\"ts\":\"2026-09-26T00:10:00Z\",\"level\":\"critical\",\"event\":\"reap\",\"pid\":3}\n")
	out, _, err = runDotForTest("--home", home, "watchdog", "log", "2")
	if err != nil {
		t.Fatalf("log 2: %v", err)
	}
	if strings.Contains(out, `"pid":1`) || !strings.Contains(out, `"pid":2`) || !strings.Contains(out, `"pid":3`) {
		t.Fatalf("log 2 must tail the newest two lines:\n%s", out)
	}

	if _, _, err := runDotForTest("--home", home, "watchdog", "log", "nope"); err == nil {
		t.Fatal("a non-numeric line count must fail")
	}
}

func TestWatchdogNotify_NoChannelsIsNoOp(t *testing.T) {
	home := t.TempDir()
	seedWatchdogSnapshot(t, home, "enabled: true\n")
	out, _, err := runDotForTest("--home", home, "watchdog", "notify", "info", "hello")
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if !strings.Contains(out, "notification sent") {
		t.Fatalf("notify output = %q", out)
	}
}

// watchdogFlagCmd builds a bare command carrying the persistent flags the
// watchdog runners read, so the goos-injected entry points can be exercised
// directly on either platform.
func watchdogFlagCmd(t *testing.T, args []string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().Bool("yes", false, "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("headless", false, "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("config", "", "")
	cmd.Flags().String("home", "", "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestWatchdogSetup_LinuxGate(t *testing.T) {
	err := runWatchdogSetupForGOOS(watchdogFlagCmd(t, nil), nil, "linux")
	if err == nil || !strings.Contains(err.Error(), "macOS-only") {
		t.Fatalf("linux setup = %v, want a macOS-only refusal", err)
	}
}

func TestWatchdogUninstall_LinuxGate(t *testing.T) {
	err := runWatchdogUninstallForGOOS(watchdogFlagCmd(t, nil), nil, "linux")
	if err == nil || !strings.Contains(err.Error(), "macOS-only") {
		t.Fatalf("linux uninstall = %v, want a macOS-only refusal", err)
	}
}

func TestWatchdogSetup_DisabledProfileRefuses(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	writeCLITestFile(t, cfgPath, "watchdog:\n  enabled: false\n")
	cmd := watchdogFlagCmd(t, []string{"--config", cfgPath, "--yes"})
	err := runWatchdogSetupForGOOS(cmd, nil, "darwin")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled watchdog must refuse setup: %v", err)
	}
}

func TestWatchdogSetup_DryRunExplains(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	writeCLITestFile(t, cfgPath, "watchdog:\n  enabled: true\n  reaper:\n    mode: dry-run\n")
	cmd := watchdogFlagCmd(t, []string{"--config", cfgPath, "--yes", "--dry-run"})
	var out strings.Builder
	cmd.SetOut(&out)
	if err := runWatchdogSetupForGOOS(cmd, nil, "darwin"); err != nil {
		t.Fatalf("dry-run setup: %v", err)
	}
	if !strings.Contains(out.String(), "would install and load") {
		t.Fatalf("dry-run setup output = %q", out.String())
	}
}

// Dry-run mode only describes the kill, so the caller prints it as a dry-run
// row. Enforce mode is not exercised here: it would signal a real process.
func TestActOnCandidate_DryRunReportsAPreview(t *testing.T) {
	home := t.TempDir()
	runner := exec.NewRunner(false, nil)
	mgr := watchdog.NewManager(runner, home)
	notifier := watchdog.NewNotifier(watchdog.NotifySettings{}, runner, "linux")
	c := watchdog.Candidate{Process: watchdog.Process{PID: 4242}, Reason: "test"}

	action, preview, err := actOnCandidate(context.Background(), watchdog.ReaperSettings{Mode: watchdog.ModeDryRun}, c, time.Now(), mgr, runner, notifier)
	if err != nil {
		t.Fatalf("actOnCandidate: %v", err)
	}
	if !preview {
		t.Fatal("a dry-run action was not reported as a preview")
	}
	if strings.Contains(action, "would") || strings.Contains(action, "dry-run") {
		t.Fatalf("the action carries the dry-run wording the row adds: %q", action)
	}
	if action != "kill" {
		t.Fatalf("action = %q, want kill", action)
	}
}
