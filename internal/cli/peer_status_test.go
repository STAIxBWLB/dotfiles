package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestPlistInteger(t *testing.T) {
	body := `<dict><key>StartInterval</key><integer>900</integer></dict>`
	if got := plistInteger(body, "StartInterval"); got != 900 {
		t.Fatalf("plistInteger = %d, want 900", got)
	}
	if got := plistInteger(body, "Missing"); got != 0 {
		t.Fatalf("missing plist integer = %d, want 0", got)
	}
}

// The peer agent hits the same launchd constraint failure as the sync
// scheduler (#233), so `dot peer status` must name spawn failed with the
// peer's own reload command (`dot peer setup` re-bootstraps the job), not
// "running". The fixture uses the #233 failure fields with the peer label.
func TestInspectPeerSchedulerNamesSpawnFailed(t *testing.T) {
	home := t.TempDir()
	writeCLITestFile(t, filepath.Join(home, "Library", "LaunchAgents", "com.dotfiles.peer.plist"),
		"<plist><dict><key>StartInterval</key><integer>600</integer></dict></plist>\n")
	dump := filepath.Join(t.TempDir(), "print.out")
	writeCLITestFile(t, dump, "gui/501/com.dotfiles.peer = {\n\tstate = spawn failed\n\tlast exit code = 78: EX_CONFIG\n\truns = 12\n}\n")
	binDir := t.TempDir()
	stub := filepath.Join(binDir, "launchctl")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n/bin/cat \""+dump+"\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	snapshot := inspectPeerScheduler(context.Background(), runner, home, false, "darwin")
	if !strings.Contains(snapshot.State, "spawn failed") || !strings.Contains(snapshot.State, "dot peer setup") {
		t.Errorf("state = %q, want spawn failed with the peer reload command", snapshot.State)
	}
	if snapshot.LastExitCode == nil || *snapshot.LastExitCode != 78 {
		t.Errorf("last exit code = %v, want 78 from the annotated fixture", snapshot.LastExitCode)
	}
	if snapshot.IntervalSeconds != 600 {
		t.Errorf("interval = %d, want 600 from the plist", snapshot.IntervalSeconds)
	}
}
