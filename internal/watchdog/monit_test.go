package watchdog

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func monitTestSettings(t *testing.T) MonitSettings {
	t.Helper()
	s, err := ResolveMonit(config.WatchdogMonitConfig{Enabled: true}, 8)
	if err != nil {
		t.Fatalf("ResolveMonit: %v", err)
	}
	return s
}

func renderMonitrcOrFail(t *testing.T, dotPath, logPath string, s MonitSettings) string {
	t.Helper()
	rc, err := RenderMonitrc(dotPath, logPath, s)
	if err != nil {
		t.Fatalf("RenderMonitrc: %v", err)
	}
	return rc
}

func TestRenderMonitrc_Golden(t *testing.T) {
	got := renderMonitrcOrFail(t, "/Users/test/.local/bin/dot", "/Users/test/Library/Logs/dot/monit.log", monitTestSettings(t))
	golden := filepath.Join("testdata", "monitrc.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("monitrc drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderMonitrc_RendersThresholdsAndCycles(t *testing.T) {
	s, err := ResolveMonit(config.WatchdogMonitConfig{
		Enabled: true, Load1Threshold: 12.5, CPUUserThreshold: 80, CPUSystemThreshold: 40, Cycles: 5,
	}, 8)
	if err != nil {
		t.Fatalf("ResolveMonit: %v", err)
	}
	rc := renderMonitrcOrFail(t, "/usr/local/bin/dot", "/tmp/monit.log", s)
	for _, want := range []string{
		"if loadavg(5min) > 12.5 for 5 cycles then",
		"if cpu(user) > 80% for 5 cycles then",
		"if cpu(system) > 40% for 5 cycles then",
		`if failed port 5900 send "" expect "RFB" for 5 cycles then`,
		"set daemon 60",
		`set logfile "/tmp/monit.log"`,
	} {
		if !strings.Contains(rc, want) {
			t.Errorf("monitrc missing %q:\n%s", want, rc)
		}
	}
	if strings.Contains(rc, "set httpd") {
		t.Error("monitrc must keep the web UI off (no set httpd)")
	}
}

// monit strings cannot carry a double quote or a newline: paths containing
// either must be rejected at render time, while spaces survive quoting.
func TestRenderMonitrc_RejectsUnquotablePaths(t *testing.T) {
	s := monitTestSettings(t)
	for _, bad := range []string{`/Users/te"st/dot`, "/Users/te\nst/dot", "/Users/te\rst/dot"} {
		if _, err := RenderMonitrc(bad, "/tmp/monit.log", s); err == nil {
			t.Errorf("dot path %q must be rejected", bad)
		}
		if _, err := RenderMonitrc("/usr/local/bin/dot", bad, s); err == nil {
			t.Errorf("log path %q must be rejected", bad)
		}
	}
	rc, err := RenderMonitrc("/Users/te st/.local/bin/dot", "/Users/te st/Library/Logs/dot/monit.log", s)
	if err != nil {
		t.Fatalf("paths with spaces must survive quoting: %v", err)
	}
	if !strings.Contains(rc, `set logfile "/Users/te st/Library/Logs/dot/monit.log"`) {
		t.Errorf("spaced log path lost its quotes:\n%s", rc)
	}
}

// A disabled screensharing knob must drop the heal check entirely: an
// installed check would keep exec'ing a sudo helper the operator opted out of.
func TestRenderMonitrc_OmitsScreenSharingWhenDisabled(t *testing.T) {
	off := false
	s, err := ResolveMonit(config.WatchdogMonitConfig{Enabled: true, ScreenSharing: &off}, 8)
	if err != nil {
		t.Fatalf("ResolveMonit: %v", err)
	}
	rc := renderMonitrcOrFail(t, "/usr/local/bin/dot", "/tmp/monit.log", s)
	for _, bad := range []string{"check host screensharing", "5900", "screensharing-heal"} {
		if strings.Contains(rc, bad) {
			t.Errorf("disabled screensharing still rendered %q:\n%s", bad, rc)
		}
	}
}

// The screensharing exec must quote the helper path for the shell monit runs
// exec programs through: without the inner quotes the space in
// "/Library/Application Support" would split the command.
func TestRenderMonitrc_QuotesTheSpacedHelperPath(t *testing.T) {
	rc := renderMonitrcOrFail(t, "/usr/local/bin/dot", "/tmp/monit.log", monitTestSettings(t))
	want := `exec "/usr/bin/sudo -n '/Library/Application Support/dot/screensharing-heal'"`
	if !strings.Contains(rc, want) {
		t.Errorf("monitrc missing %q:\n%s", want, rc)
	}
}

func TestRenderScreenSharingHeal_Script(t *testing.T) {
	got := RenderScreenSharingHeal()
	for _, want := range []string{"#!/bin/sh", "launchctl kickstart -k system/com.apple.screensharing"} {
		if !strings.Contains(got, want) {
			t.Errorf("heal helper missing %q:\n%s", want, got)
		}
	}
}

// The grant is exactly one line, passwordless, for exactly the helper — the
// space backslash-escaped the way sudoers expresses a literal space.
func TestRenderScreenSharingSudoers_ExactLine(t *testing.T) {
	got := RenderScreenSharingSudoers("alice")
	want := `alice ALL=(root) NOPASSWD: /Library/Application\ Support/dot/screensharing-heal` + "\n"
	if got != want {
		t.Errorf("sudoers = %q, want exactly %q", got, want)
	}
}

func TestManager_MonitPaths(t *testing.T) {
	m := NewManager(nil, "/home/u")
	if got := m.MonitrcPath(); got != "/home/u/.config/monit/monitrc" {
		t.Errorf("MonitrcPath = %q", got)
	}
	if got := m.MonitPlistPath(); got != "/home/u/Library/LaunchAgents/com.dotfiles.monit.plist" {
		t.Errorf("MonitPlistPath = %q", got)
	}
	if got := m.MonitLogPath(); got != "/home/u/Library/Logs/dot/monit.log" {
		t.Errorf("MonitLogPath = %q", got)
	}
}

// darwinDryManager fakes the platform gate with a dry-run runner, so the
// install/uninstall bodies execute end to end without touching launchctl,
// sudo, or the filesystem — the pattern TestSudoInstallContent_DryRunCoversStaging
// already uses, and what keeps these statements covered on Linux CI.
func darwinDryManager(home string) *Manager {
	m := NewManager(exec.NewRunner(true, slog.Default()), home)
	m.GOOS = "darwin"
	return m
}

func TestManager_MonitInstallUninstallDryRun(t *testing.T) {
	m := darwinDryManager(t.TempDir())
	ctx := context.Background()
	if err := m.InstallMonit(ctx, "/usr/local/bin/monit", "set daemon 60\n"); err != nil {
		t.Fatalf("dry-run InstallMonit: %v", err)
	}
	if err := m.UninstallMonit(ctx); err != nil {
		t.Fatalf("dry-run UninstallMonit: %v", err)
	}
	if err := m.InstallScreenSharingHeal(ctx, "alice"); err != nil {
		t.Fatalf("dry-run InstallScreenSharingHeal: %v", err)
	}
	if err := m.UninstallScreenSharingHeal(ctx); err != nil {
		t.Fatalf("dry-run UninstallScreenSharingHeal: %v", err)
	}
}

// With the plist present but monit not loaded (launchctl missing on Linux
// CI, or the service absent on a real Mac), the probe reports the file but
// never claims "loaded".
func TestManager_ProbeMonitFakeDarwin(t *testing.T) {
	m := darwinDryManager(t.TempDir())
	dir := filepath.Dir(m.MonitPlistPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.MonitPlistPath(), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := m.ProbeMonit(context.Background())
	if !st.PlistExists || st.Loaded {
		t.Fatalf("monit probe = %#v, want {true false}", st)
	}
}

func TestManager_MonitRefusalsOffDarwin(t *testing.T) {
	m := linuxManager(t.TempDir())
	if err := m.InstallMonit(context.Background(), "/usr/local/bin/monit", "set daemon 60\n"); !errors.Is(err, ErrNeedsDarwin) {
		t.Fatalf("InstallMonit off-darwin = %v, want ErrNeedsDarwin", err)
	}
	if err := m.UninstallMonit(context.Background()); !errors.Is(err, ErrNeedsDarwin) {
		t.Fatalf("UninstallMonit off-darwin = %v, want ErrNeedsDarwin", err)
	}
	if err := m.InstallScreenSharingHeal(context.Background(), "alice"); !errors.Is(err, ErrNeedsDarwin) {
		t.Fatalf("InstallScreenSharingHeal off-darwin = %v, want ErrNeedsDarwin", err)
	}
	if err := m.UninstallScreenSharingHeal(context.Background()); !errors.Is(err, ErrNeedsDarwin) {
		t.Fatalf("UninstallScreenSharingHeal off-darwin = %v, want ErrNeedsDarwin", err)
	}
	if st := m.ProbeMonit(context.Background()); st.PlistExists || st.Loaded {
		t.Fatalf("monit probe off-darwin = %#v, want all false", st)
	}
}

// A plist dropped on a linux box still reports, but never "loaded", mirroring
// the reaper probe contract.
func TestManager_ProbeMonitOffDarwinIsPlistOnly(t *testing.T) {
	m := linuxManager(t.TempDir())
	dir := filepath.Dir(m.MonitPlistPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.MonitPlistPath(), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := m.ProbeMonit(context.Background())
	if !st.PlistExists || st.Loaded {
		t.Fatalf("monit probe = %#v, want {true false}", st)
	}
}

// stubSudo shadows sudo with a script that logs every invocation and fails
// visudo when failVisudo is set, so the heal install's command sequence and
// failure cleanup are testable without root.
func stubSudo(t *testing.T, failVisudo bool) string {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"$DOT_TEST_SUDO_LOG\"\n"
	if failVisudo {
		script += "if [ \"$1\" = \"visudo\" ]; then exit 1; fi\n"
	}
	script += "exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath := filepath.Join(t.TempDir(), "sudo.log")
	t.Setenv("DOT_TEST_SUDO_LOG", logPath)
	return logPath
}

func fakeDarwinManager(home string) *Manager {
	m := NewManager(exec.NewRunner(false, slog.Default()), home)
	m.GOOS = "darwin"
	return m
}

// The fresh-Mac sequence: create the parent dir (BSD install makes no
// parents), install the helper, validate the sudoers file, install it.
func TestManager_InstallScreenSharingHeal_CommandSequence(t *testing.T) {
	logPath := stubSudo(t, false)
	m := fakeDarwinManager(t.TempDir())
	if err := m.InstallScreenSharingHeal(context.Background(), "alice"); err != nil {
		t.Fatalf("InstallScreenSharingHeal: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read sudo log: %v", err)
	}
	log := string(data)
	sequence := []string{
		"install -d -m 0755 -o root -g wheel /Library/Application Support/dot",
		"-o root -g wheel", // the helper install itself (temp source varies)
		"visudo -c -f",
		"dot-watchdog-screensharing",
	}
	at := 0
	for _, step := range sequence {
		idx := strings.Index(log[at:], step)
		if idx < 0 {
			t.Fatalf("sudo log missing %q after offset %d:\n%s", step, at, log)
		}
		at += idx + len(step)
	}
	if !strings.Contains(log, ScreenSharingHealPath) {
		t.Fatalf("sudo log never installed the helper:\n%s", log)
	}
}

// A failed visudo must not leave the un-granted root helper behind.
func TestManager_InstallScreenSharingHeal_RemovesHelperWhenSudoersFails(t *testing.T) {
	logPath := stubSudo(t, true)
	m := fakeDarwinManager(t.TempDir())
	err := m.InstallScreenSharingHeal(context.Background(), "alice")
	if err == nil || !strings.Contains(err.Error(), "visudo") {
		t.Fatalf("a visudo failure must surface, got: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read sudo log: %v", rerr)
	}
	if !strings.HasSuffix(strings.TrimSpace(string(data)), "rm -f "+ScreenSharingHealPath) {
		t.Fatalf("the failed install must end by removing the helper:\n%s", data)
	}
}
