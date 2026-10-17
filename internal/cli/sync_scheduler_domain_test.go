package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

func TestSyncExplicitHomeServiceDomain_DryRunGuidance(t *testing.T) {
	target := t.TempDir()
	if err := config.SaveStateForHome(target, &config.UserState{Name: "scheduler target"}); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSchedulerDomainStub(t, filepath.Join(binDir, "rsync"), "#!/bin/sh\necho 'rsync version test'\n")
	t.Setenv("PATH", binDir)

	out, errOut, err := runDotForTest("--home", target, "--dry-run", "sync", "setup", "--owner", "target-owner", "--push-interval", "1m")
	if err != nil {
		t.Fatalf("explicit-home setup dry-run: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	for _, want := range []string{
		"would stage or retire target-home scheduler files only",
		"no caller-domain service manager would run",
		"rerun this command without --home via sudo -iu <target-user>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run guidance missing %q:\n%s", want, out)
		}
	}
}

func writeSchedulerDomainStub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestSyncExplicitHomeServiceDomain_StatusStrings(t *testing.T) {
	state := syncer.SchedulerTargetUserActionRequired.String()
	for _, want := range []string{
		"target user action required",
		"no service-manager action ran in the caller domain",
		"sudo -iu <target-user>",
	} {
		if !strings.Contains(state, want) {
			t.Errorf("target-user scheduler state missing %q: %q", want, state)
		}
	}
}
