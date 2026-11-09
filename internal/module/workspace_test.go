package module

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

// workspaceApplyFixture builds a workspace whose cloud root already holds
// work/inbox/{downloads,incoming}, so Apply only has to federate symlinks.
func workspaceApplyFixture(t *testing.T) (*RunContext, string) {
	t.Helper()
	home := t.TempDir()
	drive := filepath.Join(home, "drive")
	for _, dir := range []string{
		filepath.Join(drive, "work", "inbox", "downloads"),
		filepath.Join(drive, "work", "inbox", "incoming"),
		filepath.Join(home, "workspace", "work"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	rc := &RunContext{
		Config: &config.Config{Modules: config.ModulesConfig{Workspace: config.WorkConfig{
			Enabled:       true,
			Path:          filepath.Join(home, "workspace"),
			Gdrive:        drive,
			GdriveSymlink: filepath.Join(home, "gdrive-workspace"),
		}}},
		Runner:   dotexec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Template: template.NewEngine(),
		HomeDir:  home,
		Out:      io.Discard,
	}
	return rc, home
}

// A link whose target is gone is what a moved cloud mirror leaves behind:
// Check plans the relink and Apply must remove the stale link before creating it.
func TestWorkspaceApplyReplacesDanglingGdriveLink(t *testing.T) {
	rc, home := workspaceApplyFixture(t)
	gdriveXref := filepath.Join(home, "workspace", "work", ".gdrive")
	if err := os.Symlink(filepath.Join(home, "old-drive", "work"), gdriveXref); err != nil {
		t.Fatal(err)
	}

	if _, err := (&WorkspaceModule{}).Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := filepath.Join(home, "gdrive-workspace", "work")
	got, err := os.Readlink(gdriveXref)
	if err != nil {
		t.Fatalf("work/.gdrive is not a symlink after Apply: %v", err)
	}
	if got != want {
		t.Errorf("work/.gdrive -> %q, want %q", got, want)
	}
	if info, err := os.Stat(gdriveXref); err != nil || !info.IsDir() {
		t.Errorf("work/.gdrive does not resolve to a directory: info=%v err=%v", info, err)
	}

	check, err := (&WorkspaceModule{}).Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check after Apply: %v", err)
	}
	if !check.Satisfied {
		t.Errorf("Check after Apply is not satisfied: %+v", check.Changes)
	}
}

// A link whose target exists is the healthy state and must not be touched.
func TestWorkspaceApplyLeavesLiveSymlinkAlone(t *testing.T) {
	rc, home := workspaceApplyFixture(t)
	target := filepath.Join(home, "live-target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	gdriveXref := filepath.Join(home, "workspace", "work", ".gdrive")
	if err := os.Symlink(target, gdriveXref); err != nil {
		t.Fatal(err)
	}

	if _, err := (&WorkspaceModule{}).Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := os.Readlink(gdriveXref)
	if err != nil {
		t.Fatalf("work/.gdrive is not a symlink after Apply: %v", err)
	}
	if got != target {
		t.Errorf("work/.gdrive -> %q, want unchanged %q", got, target)
	}
}

// A real directory at the link path is user data: Apply must never replace it.
func TestWorkspaceApplyLeavesRealDirectoryAlone(t *testing.T) {
	rc, home := workspaceApplyFixture(t)
	gdriveXref := filepath.Join(home, "workspace", "work", ".gdrive")
	if err := os.MkdirAll(gdriveXref, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(gdriveXref, "keep.txt")
	if err := os.WriteFile(marker, []byte("user data\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := (&WorkspaceModule{}).Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if info, err := os.Lstat(gdriveXref); err != nil || !info.IsDir() {
		t.Fatalf("work/.gdrive is no longer a real directory: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("file inside the real directory was lost: %v", err)
	}
}
