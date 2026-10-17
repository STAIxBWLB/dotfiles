package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// stubBeszelBrew installs brew and launchctl stubs on PATH so the beszel
// setup step can run end to end on any platform without a real Homebrew or
// launchd. brew: `info beszel-agent` fails (homebrew-core carries no such
// formula, verified 2026-09), the tap-qualified name resolves, install
// no-ops, --prefix echoes prefix. launchctl always exits 0, so a forced
// darwin manager never touches the real gui domain.
func stubBeszelBrew(t *testing.T, prefix string) (bin string) {
	t.Helper()
	bin = t.TempDir()
	brew := `#!/bin/sh
case "$1" in
  info)
    if [ "$2" = "henrygd/beszel/beszel-agent" ]; then exit 0; fi
    exit 1
    ;;
  install) exit 0 ;;
  --prefix) echo "` + prefix + `"; exit 0 ;;
esac
exit 1
`
	for name, script := range map[string]string{"brew": brew, "launchctl": "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	return bin
}

// fakeBeszelBinary lays down a stub agent binary under the prefix the brew
// stub reports, so beszelAgentPath resolves it.
func fakeBeszelBinary(t *testing.T, prefix string) string {
	t.Helper()
	path := filepath.Join(prefix, "bin", "beszel-agent")
	writeCLITestFile(t, path, "#!/bin/sh\nexit 0\n")
	return path
}

func beszelStepPrinter(out, errOut *strings.Builder) *Printer {
	return &Printer{Out: out, Err: errOut}
}

// stubLoggingBrew installs a brew stub that records every invocation and
// exits 1, so a code path that must NOT touch Homebrew can prove it didn't.
// Returns the invocation log path; it exists only if brew was called.
func stubLoggingBrew(t *testing.T) (logPath string) {
	t.Helper()
	bin := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "brew-calls.log")
	script := "#!/bin/sh\necho \"$@\" >> \"" + logPath + "\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "brew"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	return logPath
}

func assertNoBrewCalls(t *testing.T, logPath string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err == nil {
		t.Fatalf("the step touched Homebrew (%s) before validating; the documented skip must need no brew", strings.TrimSpace(string(data)))
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestBeszelFormula_CoreMissFallsBackToUpstreamTap(t *testing.T) {
	prefix := t.TempDir()
	stubBeszelBrew(t, prefix)
	mgr := watchdog.NewManager(watchdogRunner(false), t.TempDir())
	if got := beszelFormula(context.Background(), mgr.Runner); got != "henrygd/beszel/beszel-agent" {
		t.Fatalf("formula = %q, want the upstream tap fallback", got)
	}
}

func TestWatchdogSetupBeszel_SkipsWithoutHubURL(t *testing.T) {
	brewLog := stubLoggingBrew(t)
	home := t.TempDir()
	mgr := watchdog.NewManager(watchdogRunner(false), home)

	var out, errOut strings.Builder
	err := setupBeszelStep(beszelStepPrinter(&out, &errOut), mgr, config.WatchdogBeszelConfig{Enabled: true}, true)
	if err != nil {
		t.Fatalf("a missing hub_url must skip, not fail: %v", err)
	}
	if !strings.Contains(errOut.String(), "hub_url") {
		t.Fatalf("skip warning must name hub_url:\n%s", errOut.String())
	}
	if _, statErr := os.Stat(mgr.BeszelPlistPath()); !os.IsNotExist(statErr) {
		t.Fatalf("plist was installed despite the missing hub_url: %v", statErr)
	}
	assertNoBrewCalls(t, brewLog)
}

func TestWatchdogSetupBeszel_SkipsWithoutEnvFile(t *testing.T) {
	brewLog := stubLoggingBrew(t)
	home := t.TempDir()
	mgr := watchdog.NewManager(watchdogRunner(false), home)

	var out, errOut strings.Builder
	bcfg := config.WatchdogBeszelConfig{Enabled: true, HubURL: "https://hub.example"}
	err := setupBeszelStep(beszelStepPrinter(&out, &errOut), mgr, bcfg, true)
	if err != nil {
		t.Fatalf("a missing env file must skip, not fail: %v", err)
	}
	if !strings.Contains(errOut.String(), "dot secrets restore") {
		t.Fatalf("skip warning must point at `dot secrets restore`:\n%s", errOut.String())
	}
	if _, statErr := os.Stat(mgr.BeszelPlistPath()); !os.IsNotExist(statErr) {
		t.Fatalf("plist was installed despite the missing env file: %v", statErr)
	}
	assertNoBrewCalls(t, brewLog)
}

// A custom env_path skips too, but its message must point at placing the
// file MANUALLY: only the default ~/.config/beszel/agent.env pair is covered
// by `dot secrets` backup/restore.
func TestWatchdogSetupBeszel_CustomEnvPathSkipNamesManualPlacement(t *testing.T) {
	brewLog := stubLoggingBrew(t)
	home := t.TempDir()
	mgr := watchdog.NewManager(watchdogRunner(false), home)

	var out, errOut strings.Builder
	bcfg := config.WatchdogBeszelConfig{Enabled: true, HubURL: "https://hub.example", EnvPath: "~/.config/beszel/custom.env"}
	err := setupBeszelStep(beszelStepPrinter(&out, &errOut), mgr, bcfg, true)
	if err != nil {
		t.Fatalf("a missing custom env file must skip, not fail: %v", err)
	}
	if !strings.Contains(errOut.String(), "manually") || !strings.Contains(errOut.String(), "not covered by `dot secrets`") {
		t.Fatalf("custom-path skip must say the file is placed manually and is not secrets-covered:\n%s", errOut.String())
	}
	if strings.Contains(errOut.String(), "restore it with `dot secrets restore`") {
		t.Fatalf("custom-path skip must not point at `dot secrets restore`:\n%s", errOut.String())
	}
	assertNoBrewCalls(t, brewLog)
}

// A custom env_path that EXISTS installs fine, with a clear note that the
// path is outside `dot secrets` coverage.
func TestWatchdogSetupBeszel_CustomEnvPathInstallNotesSecretsCoverage(t *testing.T) {
	prefix := t.TempDir()
	stubBeszelBrew(t, prefix)
	fakeBeszelBinary(t, prefix)
	home := t.TempDir()
	mgr := watchdog.NewManager(watchdogRunner(false), home)
	mgr.GOOS = "darwin"
	customEnv := filepath.Join(home, ".config", "beszel", "custom.env")
	writeCLITestFile(t, customEnv, "KEY=x\n")

	var out, errOut strings.Builder
	bcfg := config.WatchdogBeszelConfig{Enabled: true, HubURL: "https://hub.example", EnvPath: "~/.config/beszel/custom.env"}
	if err := setupBeszelStep(beszelStepPrinter(&out, &errOut), mgr, bcfg, true); err != nil {
		t.Fatalf("setup step: %v\nstderr=%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "not covered by `dot secrets`") {
		t.Fatalf("install with a custom env_path must note the secrets-coverage gap:\n%s", out.String())
	}
	data, err := os.ReadFile(mgr.BeszelPlistPath())
	if err != nil {
		t.Fatalf("plist not installed: %v", err)
	}
	if !strings.Contains(string(data), `$HOME/.config/beszel/custom.env`) {
		t.Errorf("plist does not source the custom env file:\n%s", data)
	}
}

func TestWatchdogSetupBeszel_InstallsPlistWhenReady(t *testing.T) {
	prefix := t.TempDir()
	stubBeszelBrew(t, prefix)
	agentPath := fakeBeszelBinary(t, prefix)
	home := t.TempDir()
	mgr := watchdog.NewManager(watchdogRunner(false), home)
	mgr.GOOS = "darwin"
	writeCLITestFile(t, mgr.BeszelEnvPath(), "KEY=supersecret-sentinel\n")

	var out, errOut strings.Builder
	bcfg := config.WatchdogBeszelConfig{Enabled: true, HubURL: "https://hub.example"}
	if err := setupBeszelStep(beszelStepPrinter(&out, &errOut), mgr, bcfg, true); err != nil {
		t.Fatalf("setup step: %v\nstderr=%s", err, errOut.String())
	}
	data, err := os.ReadFile(mgr.BeszelPlistPath())
	if err != nil {
		t.Fatalf("plist not installed: %v", err)
	}
	plist := string(data)
	if !strings.Contains(plist, watchdog.BeszelLabel) {
		t.Errorf("plist lost the label:\n%s", plist)
	}
	if !strings.Contains(plist, `set -a; . "$HOME/.config/beszel/agent.env"; set +a; export LISTEN="${LISTEN:-:45876}"; exec "`+agentPath+`"`) {
		t.Errorf("plist lost the env-sourcing exec line:\n%s", plist)
	}
	// The secrets-managed env file is never read into output or rewritten.
	env, err := os.ReadFile(mgr.BeszelEnvPath())
	if err != nil || string(env) != "KEY=supersecret-sentinel\n" {
		t.Errorf("env file changed during setup: %q, %v", env, err)
	}
	if strings.Contains(out.String()+errOut.String(), "supersecret-sentinel") {
		t.Error("env file contents leaked into step output")
	}
}

func TestWatchdogSetup_DryRunCoversBeszel(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	writeCLITestFile(t, cfgPath, "watchdog:\n  enabled: true\n  beszel:\n    enabled: true\n    hub_url: https://hub.example\n")
	cmd := watchdogFlagCmd(t, []string{"--config", cfgPath, "--yes", "--dry-run"})
	var out strings.Builder
	cmd.SetOut(&out)
	if err := runWatchdogSetupForGOOS(cmd, nil, "darwin"); err != nil {
		t.Fatalf("dry-run setup: %v", err)
	}
	if !strings.Contains(out.String(), "[dry-run] would ensure the beszel-agent binary and install") {
		t.Fatalf("dry-run setup missing the beszel line:\n%s", out.String())
	}
}

// The env file row reports presence only: the hub KEY/TOKEN contents must
// never reach `dot watchdog status` output.
func TestWatchdogStatus_BeszelRowsPresenceOnly(t *testing.T) {
	home := t.TempDir()
	seedWatchdogSnapshot(t, home, "enabled: true\nbeszel:\n  enabled: true\n  hub_url: https://hub.example\n")
	writeCLITestFile(t, filepath.Join(home, ".config", "beszel", "agent.env"), "TOKEN=supersecret-sentinel\n")

	out, _, err := runDotForTest("--home", home, "watchdog", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"Beszel hub", "https://hub.example", "Beszel env file", filepath.Join(home, ".config", "beszel", "agent.env"), "Beszel plist"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "supersecret-sentinel") {
		t.Fatalf("env file contents leaked into status output:\n%s", out)
	}
}

func TestWatchdogStatus_BeszelDisabledRow(t *testing.T) {
	home := t.TempDir()
	out, _, err := runDotForTest("--home", home, "watchdog", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "Beszel agent") {
		t.Fatalf("status without a snapshot must still carry the beszel row:\n%s", out)
	}
}
