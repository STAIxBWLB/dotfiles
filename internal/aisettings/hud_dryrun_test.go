package aisettings

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
)

// TestApplyClaudeHUD_DryRunWritesNothingWithARealRunner is the falsifiable
// test for the dry-run guard in applyClaudeHUD.
//
// The runner is a real (non-dry-run) one on purpose, and that choice is
// load-bearing: internal/cli builds the manager with
// execrun.NewRunner(dryRun, logger), so under a real --dry-run invocation
// the runner itself suppresses every write that passes through
// fileutil.EnsureFile and the guard is unobservable from outside the
// process. Dry-run logic paired with a real runner is the one configuration
// the CLI never produces, and the only one in which the guard is the thing
// under test. Deleting the `&& !dryRun` clause from the write guard must
// turn this test red; a version that stays green with the guard deleted is
// a broken test.
//
// The .dot-lock assertion is a leak check only, not guard evidence: the
// release closure in fileutil.AcquirePIDLock removes the directory, so it
// is absent whether or not the lock was ever taken. The file assertions are
// what discriminate.
func TestApplyClaudeHUD_DryRunWritesNothingWithARealRunner(t *testing.T) {
	home := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	m := &HUDManager{Runner: dotexec.NewRunner(false, logger), HomeDir: home}

	item, err := m.applyClaudeHUD(true, false)
	if err != nil {
		t.Fatal(err)
	}
	// A dry run must still describe the work it declined to do.
	if !item.Changed || item.Drift != "out-of-sync" {
		t.Fatalf("item = %+v, want Changed=true Drift=out-of-sync", item)
	}
	for _, rel := range []string{
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "statusline-dot.py"),
		filepath.Join(".claude", ".dot-lock"),
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); !os.IsNotExist(err) {
			t.Fatalf("dry run wrote %s (stat err: %v)", rel, err)
		}
	}
}
