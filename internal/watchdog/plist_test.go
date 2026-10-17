package watchdog

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestRenderReapPlist_Golden(t *testing.T) {
	got := RenderReapPlist("/Users/test/.local/bin/dot", 300*time.Second, "/Users/test/Library/Logs/dot")
	golden := filepath.Join("testdata", "reap.plist.golden")
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
		t.Errorf("plist drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderReapPlist_CarriesTheScheduledMarker(t *testing.T) {
	plist := RenderReapPlist("/usr/local/bin/dot", 60*time.Second, "/tmp/logs")
	if !strings.Contains(plist, "<key>"+ScheduledRunEnv+"</key>") {
		t.Fatalf("plist lost the scheduled-run marker %s", ScheduledRunEnv)
	}
	if !strings.Contains(plist, "<integer>60</integer>") {
		t.Fatal("plist lost the interval")
	}
	if !strings.Contains(plist, ReapLabel) {
		t.Fatal("plist lost the label")
	}
}

func TestManager_Paths(t *testing.T) {
	m := NewManager(nil, "/home/u")
	if got := m.PlistPath(); got != "/home/u/Library/LaunchAgents/com.dotfiles.watchdog.reap.plist" {
		t.Errorf("PlistPath = %q", got)
	}
	if got := m.SnapshotPath(); got != "/home/u/.local/state/dot/watchdog/watchdog.yaml" {
		t.Errorf("SnapshotPath = %q", got)
	}
	if got := m.SamplesPath(); got != "/home/u/.local/state/dot/watchdog/samples.json" {
		t.Errorf("SamplesPath = %q", got)
	}
	if got := m.LogPath(); got != "/home/u/Library/Logs/dot/watchdog.log" {
		t.Errorf("LogPath = %q", got)
	}
}

func linuxManager(home string) *Manager {
	m := NewManager(exec.NewRunner(false, slog.Default()), home)
	m.GOOS = "linux"
	return m
}

func TestManager_InstallRefusedOffDarwin(t *testing.T) {
	m := linuxManager(t.TempDir())
	err := m.Install(context.Background(), "/usr/local/bin/dot", 300*time.Second, []byte("enabled: true\n"))
	if !errors.Is(err, ErrNeedsDarwin) {
		t.Fatalf("Install off-darwin = %v, want ErrNeedsDarwin", err)
	}
	// And nothing was written: the refusal must precede any mutation.
	if _, statErr := os.Stat(m.PlistPath()); !os.IsNotExist(statErr) {
		t.Fatalf("plist exists after a refused install: %v", statErr)
	}
}

func TestManager_UninstallRefusedOffDarwin(t *testing.T) {
	m := linuxManager(t.TempDir())
	if err := m.Uninstall(context.Background()); !errors.Is(err, ErrNeedsDarwin) {
		t.Fatalf("Uninstall off-darwin = %v, want ErrNeedsDarwin", err)
	}
}

func TestManager_ProbeOffDarwinIsPlistOnly(t *testing.T) {
	m := linuxManager(t.TempDir())
	if st := m.Probe(context.Background()); st.PlistExists || st.Loaded {
		t.Fatalf("empty home probe = %#v", st)
	}
	// A plist dropped on a linux box still reports, but never "loaded":
	// the launchctl print must not even be attempted.
	dir := filepath.Dir(m.PlistPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.PlistPath(), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := m.Probe(context.Background())
	if !st.PlistExists || st.Loaded {
		t.Fatalf("probe = %#v, want {true false}", st)
	}
}

func TestRenderWarpPlist_Golden(t *testing.T) {
	got := RenderWarpPlist("/Users/test/.local/bin/dot", "/Users/test", 120*time.Second, "/Users/test/Library/Logs/dot")
	golden := filepath.Join("testdata", "warp.plist.golden")
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
		t.Errorf("warp plist drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderWarpPlist_CarriesLabelMarkerAndInterval(t *testing.T) {
	plist := RenderWarpPlist("/usr/local/bin/dot", "/Users/test", 120*time.Second, "/tmp/logs")
	for _, want := range []string{WarpLabel, "<key>" + ScheduledRunEnv + "</key>", "<integer>120</integer>", "watchdog</string>", "warp</string>"} {
		if !strings.Contains(plist, want) {
			t.Errorf("warp plist missing %q", want)
		}
	}
}

// The root daemon runs as root: without the owning user's home pinned in
// its arguments, homeFor resolves /var/root and the pass never finds the
// setup snapshot.
func TestRenderWarpPlist_PinsTheUsersHome(t *testing.T) {
	plist := RenderWarpPlist("/usr/local/bin/dot", "/Users/test", 120*time.Second, "/tmp/logs")
	for _, want := range []string{"<string>--home</string>", "<string>/Users/test</string>"} {
		if !strings.Contains(plist, want) {
			t.Errorf("warp plist missing %q", want)
		}
	}
}

func TestRenderMonitPlist_Golden(t *testing.T) {
	got := RenderMonitPlist("/opt/homebrew/bin/monit", "/Users/test/.config/monit/monitrc", "/Users/test/Library/Logs/dot")
	golden := filepath.Join("testdata", "monit.plist.golden")
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
		t.Errorf("monit plist drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderMonitPlist_ForegroundAgainstTheControlFile(t *testing.T) {
	plist := RenderMonitPlist("/usr/local/bin/monit", "/tmp/monitrc", "/tmp/logs")
	for _, want := range []string{MonitLabel, "<string>-I</string>", "<string>-c</string>", "<string>/tmp/monitrc</string>", "<key>KeepAlive</key>"} {
		if !strings.Contains(plist, want) {
			t.Errorf("monit plist missing %q", want)
		}
	}
}

func TestSudoInstallContent_DryRunCoversStaging(t *testing.T) {
	dry := exec.NewRunner(true, slog.Default())
	if err := sudoInstallContent(context.Background(), dry, []byte("<plist/>"), "/nonexistent-dest", 0o644); err != nil {
		t.Fatalf("dry-run staging must succeed end to end: %v", err)
	}
}

func TestManager_WarpRefusalsOffDarwin(t *testing.T) {
	m := linuxManager(t.TempDir())
	if _, err := m.InstallWarp(context.Background(), "/usr/local/bin/dot", 120*time.Second); !errors.Is(err, ErrNeedsDarwin) {
		t.Fatalf("InstallWarp off-darwin = %v, want ErrNeedsDarwin", err)
	}
	if err := m.UninstallWarp(context.Background()); !errors.Is(err, ErrNeedsDarwin) {
		t.Fatalf("UninstallWarp off-darwin = %v, want ErrNeedsDarwin", err)
	}
	if st := m.ProbeWarp(context.Background()); st.PlistExists || st.Loaded {
		t.Fatalf("warp probe off-darwin = %#v, want all false", st)
	}
}

func TestManager_WarpStatePaths(t *testing.T) {
	m := NewManager(nil, "/home/u")
	if got := m.WarpPlistPath(); got != "/Library/LaunchDaemons/com.dotfiles.watchdog.warp.plist" {
		t.Errorf("WarpPlistPath = %q", got)
	}
	if got := m.WarpStatePath(); got != "/home/u/.local/state/dot/watchdog/warp.json" {
		t.Errorf("WarpStatePath = %q", got)
	}
	if got := m.PowerStatePath(); got != "/home/u/.local/state/dot/watchdog/power.json" {
		t.Errorf("PowerStatePath = %q", got)
	}
}
