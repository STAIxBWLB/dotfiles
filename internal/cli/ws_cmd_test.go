package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// TestWsMv_SSHTargetTouchesNothingOutsideWorkspace covers #379: with an ssh:
// sync target, `dot ws mv` must not act on the default local mirror tree.
func TestWsMv_SSHTargetTouchesNothingOutsideWorkspace(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "workspace", "work")
	if err := os.MkdirAll(filepath.Join(work, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := &config.UserState{}
	state.Modules.Workspace.Path = "~/workspace"
	if err := config.SaveStateForHome(home, state); err != nil {
		t.Fatal(err)
	}
	paths := syncer.ResolveLocalPathsForProfile(work+"/", syncer.DefaultProfile)
	if err := syncer.EnsureLocalLayout(paths); err != nil {
		t.Fatal(err)
	}
	if err := syncer.SaveLocalConfig(paths, &syncer.LocalConfig{Target: "ssh:peer:/w", Propagation: syncer.DefaultPropagationPolicy()}); err != nil {
		t.Fatal(err)
	}
	// Stale local trees a wrong resolution could hit: every candidate
	// directory under home that is not the workspace gets a sentinel dir.
	var stale []string
	for _, rel := range []string{"gdrive-workspace/work", "Library/CloudStorage/work", "w"} {
		d := filepath.Join(home, rel, "a")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		stale = append(stale, d)
	}

	cmd := &cobra.Command{}
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("yes", true, "")
	cmd.Flags().String("home", home, "")
	err := runWsMv(cmd, []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "dot sync push") {
		t.Fatalf("want refusal naming 'dot sync push', got %v", err)
	}
	for _, d := range stale {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("stale tree changed: %s: %v", d, err)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(d), "b")); err == nil {
			t.Errorf("file created outside workspace: %s", filepath.Join(filepath.Dir(d), "b"))
		}
	}
	if _, err := os.Stat(filepath.Join(work, "a")); err != nil {
		t.Errorf("workspace changed despite refusal: %v", err)
	}
}

// A malformed sync config must name the file to fix, not a command that
// parses the same broken file.
func TestWsMv_MalformedSyncConfigNamesFile(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "workspace", "work")
	state := &config.UserState{}
	state.Modules.Workspace.Path = "~/workspace"
	if err := config.SaveStateForHome(home, state); err != nil {
		t.Fatal(err)
	}
	paths := syncer.ResolveLocalPathsForProfile(work+"/", syncer.DefaultProfile)
	if err := syncer.EnsureLocalLayout(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("target: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("yes", true, "")
	cmd.Flags().String("home", home, "")
	err := runWsMv(cmd, []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "fix "+paths.ConfigFile) {
		t.Fatalf("want error naming %s to fix, got %v", paths.ConfigFile, err)
	}
}
