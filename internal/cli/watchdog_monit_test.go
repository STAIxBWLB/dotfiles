package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// P3 CLI surface: setup dry-run coverage, status monitrc drift, and
// uninstall dry-run coverage, all through the goos-injected entry points.

func TestWatchdogSetup_DryRunCoversMonit(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	writeCLITestFile(t, cfgPath, "watchdog:\n  enabled: true\n  monit:\n    enabled: true\n")
	cmd := watchdogFlagCmd(t, []string{"--config", cfgPath, "--yes", "--dry-run"})
	var out strings.Builder
	cmd.SetOut(&out)
	if err := runWatchdogSetupForGOOS(cmd, nil, "darwin"); err != nil {
		t.Fatalf("dry-run setup: %v", err)
	}
	for _, want := range []string{
		"Monit",
		"[dry-run] would render",
		"monitrc",
		"com.dotfiles.monit.plist",
		watchdog.ScreenSharingHealPath,
		watchdog.ScreenSharingSudoersPath,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run setup missing %q:\n%s", want, out.String())
		}
	}
}

// An explicit screensharing: false must keep the root helper out of even the
// dry-run plan.
func TestWatchdogSetup_DryRunSkipsHealWhenScreenSharingDisabled(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	writeCLITestFile(t, cfgPath, "watchdog:\n  enabled: true\n  monit:\n    enabled: true\n    screensharing: false\n")
	cmd := watchdogFlagCmd(t, []string{"--config", cfgPath, "--yes", "--dry-run"})
	var out strings.Builder
	cmd.SetOut(&out)
	if err := runWatchdogSetupForGOOS(cmd, nil, "darwin"); err != nil {
		t.Fatalf("dry-run setup: %v", err)
	}
	if strings.Contains(out.String(), watchdog.ScreenSharingHealPath) {
		t.Fatalf("screensharing: false still planned the heal helper:\n%s", out.String())
	}
}

func TestWatchdogSetup_InvalidMonitConfigFails(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	writeCLITestFile(t, cfgPath, "watchdog:\n  enabled: true\n  monit:\n    enabled: true\n    cycles: 100\n")
	cmd := watchdogFlagCmd(t, []string{"--config", cfgPath, "--yes", "--dry-run"})
	err := runWatchdogSetupForGOOS(cmd, nil, "darwin")
	if err == nil || !strings.Contains(err.Error(), "cycles") {
		t.Fatalf("cycles above monit's limit must fail setup, got: %v", err)
	}
}

// stubDotOnPATH puts a fake `dot` first on PATH so the drift check resolves
// the same binary path deterministically on any platform.
func stubDotOnPATH(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	dot := filepath.Join(bin, "dot")
	if err := os.WriteFile(dot, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dot
}

func TestMonitrcStatus_DriftAndSync(t *testing.T) {
	home := t.TempDir()
	dot := stubDotOnPATH(t)
	mgr := watchdog.NewManager(watchdogRunner(false), home)
	wcfg := &config.WatchdogConfig{Monit: config.WatchdogMonitConfig{Enabled: true}}

	if got := monitrcStatus(mgr, wcfg); got != "missing" {
		t.Fatalf("no monitrc = %q, want missing", got)
	}
	writeCLITestFile(t, mgr.MonitrcPath(), "set daemon 30\n")
	if got := monitrcStatus(mgr, wcfg); !strings.Contains(got, "DRIFTED") {
		t.Fatalf("stale monitrc = %q, want DRIFTED", got)
	}
	settings, err := watchdog.ResolveMonit(wcfg.Monit, runtime.NumCPU())
	if err != nil {
		t.Fatalf("ResolveMonit: %v", err)
	}
	rendered, err := watchdog.RenderMonitrc(dot, mgr.MonitLogPath(), settings)
	if err != nil {
		t.Fatalf("RenderMonitrc: %v", err)
	}
	writeCLITestFile(t, mgr.MonitrcPath(), rendered)
	if got := monitrcStatus(mgr, wcfg); got != "in sync" {
		t.Fatalf("freshly rendered monitrc = %q, want in sync", got)
	}
	// No snapshot config to compare against: present, never a false drift.
	wcfg.Monit.Enabled = false
	if got := monitrcStatus(mgr, wcfg); got != "present" {
		t.Fatalf("monit disabled in snapshot = %q, want present", got)
	}
}

func TestWatchdogStatus_P3Rows(t *testing.T) {
	home := t.TempDir()
	seedWatchdogSnapshot(t, home, "enabled: true\nmonit:\n  enabled: true\n")
	out, _, err := runDotForTest("--home", home, "watchdog", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"Monit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q row:\n%s", want, out)
		}
	}
}

func TestWatchdogUninstall_DryRunCoversMonit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCLITestFile(t, filepath.Join(home, "Library", "LaunchAgents", "com.dotfiles.monit.plist"), "<plist/>")
	cmd := watchdogFlagCmd(t, []string{"--dry-run"})
	var out strings.Builder
	cmd.SetOut(&out)
	if err := runWatchdogUninstallForGOOS(cmd, nil, "darwin"); err != nil {
		t.Fatalf("dry-run uninstall: %v", err)
	}
	if !strings.Contains(out.String(), "com.dotfiles.monit.plist") || !strings.Contains(out.String(), "monitrc") {
		t.Fatalf("dry-run uninstall must cover the monit files:\n%s", out.String())
	}
}

// The consent-yes flow, end to end on a dry runner with a stub monit on PATH:
// binary resolution, agent install, and the root helper install all report.
func TestSetupMonitStep_DryRunEndToEnd(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "monit"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	mgr := watchdog.NewManager(watchdogRunner(true), t.TempDir())
	mgr.GOOS = "darwin"
	cmd := watchdogFlagCmd(t, nil)
	var out strings.Builder
	cmd.SetOut(&out)
	wcfg := config.WatchdogConfig{Monit: config.WatchdogMonitConfig{Enabled: true}}
	if err := setupMonitStep(printerFrom(cmd), mgr, wcfg, "/usr/local/bin/dot", true); err != nil {
		t.Fatalf("setupMonitStep: %v", err)
	}
	for _, want := range []string{"monit agent installed", "heal helper installed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("setup output missing %q:\n%s", want, out.String())
		}
	}
}

// When PATH lacks monit (GUI/launchd context), the resolver must find the
// binary under the brew prefix instead of erroring.
func TestResolveMonitPath_BrewPrefixFallback(t *testing.T) {
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", "monit"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	brew := "#!/bin/sh\nif [ \"$1\" = \"--prefix\" ]; then echo \"" + prefix + "\"; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "brew"), []byte(brew), 0o755); err != nil {
		t.Fatal(err)
	}
	// PATH carries the brew stub but deliberately no monit.
	t.Setenv("PATH", bin)
	got, err := resolveMonitPath(context.Background(), watchdogRunner(false))
	if err != nil {
		t.Fatalf("resolveMonitPath: %v", err)
	}
	if got != filepath.Join(prefix, "bin", "monit") {
		t.Fatalf("resolveMonitPath = %q, want the brew-prefix candidate", got)
	}
}
