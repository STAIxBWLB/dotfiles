package module

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// TestPackagesCheck_LeavesProcessPATHUnchanged pins D-02's read-only half in Go
// rather than only in the region-scoped grep the plan runs by hand: Check is a
// read-only path and must not mutate process state at all.
//
// The call it used to make, rc.Brew.RefreshPath(), os.Setenv's PATH whenever a
// Homebrew prefix exists, so every later lookup in the run — not just brew's —
// widened. Its twin in Apply survives on purpose: that is a write path, where
// making a just-installed brew reachable is the point.
//
// The row does not skip. On a host with a Homebrew prefix it observes the
// mutation directly; on one without it holds the line against a new mutation
// that does not depend on a prefix existing.
func TestPackagesCheck_LeavesProcessPATHUnchanged(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	want := os.Getenv("PATH")

	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rc := &RunContext{
		Config: &config.Config{},
		Runner: runner,
		Brew:   exec.NewBrew(runner),
		Out:    io.Discard,
	}
	if _, err := (&PackagesModule{}).Check(context.Background(), rc); err != nil {
		t.Fatalf("PackagesModule.Check: %v", err)
	}

	if got := os.Getenv("PATH"); got != want {
		t.Errorf("PackagesModule.Check widened the process PATH from a read-only path (BUG-06, D-02)\n  before: %q\n   after: %q", want, got)
	}
}
