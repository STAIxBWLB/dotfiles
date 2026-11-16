package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// P2 CLI surface: warp pass gates, setup --headless / warp daemon dry-run
// output, uninstall dry-run coverage, and the new status rows.

func TestWatchdogWarp_LinuxGate(t *testing.T) {
	err := runWatchdogWarpForGOOS(watchdogFlagCmd(t, nil), nil, "linux")
	if err == nil || !strings.Contains(err.Error(), "macOS-only") {
		t.Fatalf("linux warp = %v, want a macOS-only refusal", err)
	}
}

func TestWatchdogWarp_MissingSnapshotIsActionable(t *testing.T) {
	home := t.TempDir()
	cmd := watchdogFlagCmd(t, []string{"--home", home})
	err := runWatchdogWarpForGOOS(cmd, nil, "darwin")
	if err == nil || !strings.Contains(err.Error(), "dot watchdog setup") {
		t.Fatalf("missing snapshot error must point at setup, got: %v", err)
	}
}

func TestWatchdogWarp_DisabledInSnapshot(t *testing.T) {
	home := t.TempDir()
	seedWatchdogSnapshot(t, home, "enabled: true\n")
	cmd := watchdogFlagCmd(t, []string{"--home", home})
	err := runWatchdogWarpForGOOS(cmd, nil, "darwin")
	if err == nil || !strings.Contains(err.Error(), "warp.enabled") {
		t.Fatalf("disabled warp error = %v, want it to name warp.enabled", err)
	}
}

func TestWatchdogSetup_HeadlessRequiresConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	writeCLITestFile(t, cfgPath, "watchdog:\n  enabled: true\n")
	cmd := watchdogFlagCmd(t, []string{"--config", cfgPath, "--yes", "--headless"})
	err := runWatchdogSetupForGOOS(cmd, nil, "darwin")
	if err == nil || !strings.Contains(err.Error(), "watchdog.power.headless") {
		t.Fatalf("--headless without config headless = %v, want a refusal", err)
	}
}

func TestWatchdogSetup_DryRunCoversWarpAndPower(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	writeCLITestFile(t, cfgPath, "watchdog:\n  enabled: true\n  warp:\n    enabled: true\n  power:\n    headless: true\n")
	cmd := watchdogFlagCmd(t, []string{"--config", cfgPath, "--yes", "--dry-run", "--headless"})
	var out strings.Builder
	cmd.SetOut(&out)
	if err := runWatchdogSetupForGOOS(cmd, nil, "darwin"); err != nil {
		t.Fatalf("dry-run setup: %v", err)
	}
	for _, want := range []string{
		"would install and load",
		"would install the root WARP heal daemon",
		"would save prior power values",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run setup missing %q:\n%s", want, out.String())
		}
	}
}

func TestWatchdogUninstall_DryRunBaseStillWorks(t *testing.T) {
	home := t.TempDir()
	cmd := watchdogFlagCmd(t, []string{"--home", home, "--dry-run"})
	var out strings.Builder
	cmd.SetOut(&out)
	// The uninstall dry-run ignores --home on purpose (it guards system
	// paths); drive the goos-injected entry point to keep it deterministic.
	if err := runWatchdogUninstallForGOOS(cmd, nil, "darwin"); err == nil {
		t.Fatal("uninstall must still refuse --home")
	}
	cmd = watchdogFlagCmd(t, []string{"--dry-run"})
	cmd.SetOut(&out)
	if err := runWatchdogUninstallForGOOS(cmd, nil, "darwin"); err != nil {
		t.Fatalf("dry-run uninstall: %v", err)
	}
	if !strings.Contains(out.String(), "would unload and remove") {
		t.Fatalf("dry-run uninstall output = %q", out.String())
	}
}

func TestWatchdogStatus_P2Rows(t *testing.T) {
	home := t.TempDir()
	seedWatchdogSnapshot(t, home, "enabled: true\nwarp:\n  enabled: true\n")
	out, _, err := runDotForTest("--home", home, "watchdog", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"WARP daemon", "Tunnel"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q row:\n%s", want, out)
		}
	}
}

func TestWarpDryRunRendersComputedActionAndState(t *testing.T) {
	t.Setenv("COLUMNS", "")
	for _, tc := range []struct {
		name    string
		action  watchdog.WarpAction
		preview string
	}{
		{"no-op", watchdog.WarpActionNone, "take no WARP action"},
		{"reconnect", watchdog.WarpActionReconnect, "reconnect WARP"},
		{"kickstart", watchdog.WarpActionKickstart, "kickstart the WARP daemon"},
		{"rate-limit", watchdog.WarpActionRateLimited, "defer the WARP restart (rate-limited)"},
		{"unknown", watchdog.WarpAction(99), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := watchdog.WarpState{ConsecutiveFailures: 2, Attempts: 1}
			before := state
			var out strings.Builder
			p := &Printer{Out: ui.NewColorWriter(&out, ui.ColorNever, false)}
			printWarpDryRun(p, false, state, tc.action)
			want := "warp healthy=false consecutive=2 attempts=1\n  ~  would " + tc.preview + "\n"
			if tc.name == "unknown" {
				want = "warp healthy=false consecutive=2 attempts=1\nUnknown WARP action 99; no action preview is available.\n"
			}
			if got := out.String(); got != want {
				t.Fatalf("computed preview=%q,want %q", got, want)
			}
			if state != before {
				t.Fatal("dry-run renderer changed computed state")
			}
		})
	}
}
