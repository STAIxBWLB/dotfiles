package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

func TestSyncNamesCommandShape(t *testing.T) {
	syncCmd := newSyncCmd()
	cmd, _, err := syncCmd.Find([]string{"names"})
	if err != nil {
		t.Fatalf("find names under sync: %v", err)
	}
	if cmd.Use != "names" {
		t.Fatalf("Use = %q, want names", cmd.Use)
	}
	child, _, err := cmd.Find([]string{"normalize"})
	if err != nil {
		t.Fatalf("find normalize: %v", err)
	}
	if child == nil || child.Use != "normalize" {
		t.Fatalf("normalize command = %#v", child)
	}
}

func TestPrintNFDNormalizationPlanIsBounded(t *testing.T) {
	plan := &syncer.NameNormalizationPlan{WorkspaceRoot: "/workspace"}
	for i := 0; i < 100; i++ {
		plan.Renames = append(plan.Renames, syncer.NameRename{
			OldRel: "old/file-" + string(rune('a'+i%26)),
			NewRel: "new/file-" + string(rune('a'+i%26)),
		})
	}
	var out bytes.Buffer
	printNFDNormalizationPlan(&Printer{Out: &out, Err: &out}, plan, true)
	got := out.String()
	if !strings.Contains(got, "... and 80 more rename(s)") {
		t.Fatalf("bounded summary missing:\n%s", got)
	}
	if strings.Count(got, "->") != 20 {
		t.Fatalf("printed %d rename rows, want 20", strings.Count(got, "->"))
	}
}

func TestSyncNamesNormalizeCLI_DryRunThenYes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	workspace := filepath.Join(home, "workspace", "work")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(home, ".config", "dotfiles", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	state := "modules:\n  gsync:\n    local_path: " + workspace + "\n"
	if err := os.WriteFile(statePath, []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := syncer.ResolveLocalPathsForProfile(workspace, syncer.DefaultProfile)
	if err := syncer.EnsureLocalLayout(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("filter_mode: exclude\npropagation:\n  create: true\n  update: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nfc := "caf\u00e9.txt"
	path := filepath.Join(workspace, nfc)
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(nfc) {
		t.Fatal("fixture unexpectedly invalid UTF-8")
	}

	root := newNamesTestRoot()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"names", "normalize", "--dry-run"})
	// This isolated command mirrors the inherited sync flags; the production
	// root registers the same child below newSyncCmd.
	root.AddCommand(newSyncNamesCmd())
	if err := root.Execute(); err != nil {
		t.Fatalf("dry-run: %v\nstderr=%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "Renames") || !strings.Contains(out.String(), "Preview only") {
		t.Fatalf("dry-run output:\n%s", out.String())
	}
	if syncer.NFDMigrationMarked(workspace) {
		t.Fatal("dry-run wrote migration marker")
	}

	root = newNamesTestRoot()
	out.Reset()
	errOut.Reset()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"names", "normalize", "--yes"})
	root.AddCommand(newSyncNamesCmd())
	if err := root.Execute(); err != nil {
		t.Fatalf("apply: %v\nstderr=%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "Normalized 1 path name(s)") {
		t.Fatalf("apply output:\n%s", out.String())
	}
	if !syncer.NFDMigrationMarked(workspace) {
		t.Fatal("apply did not write migration marker")
	}
}

func newNamesTestRoot() *cobra.Command {
	root := &cobra.Command{Use: "dot", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().Bool("yes", false, "")
	root.PersistentFlags().Bool("dry-run", false, "")
	root.PersistentFlags().String("profile", syncer.DefaultProfile, "")
	root.PersistentFlags().Bool("verbose", false, "")
	root.PersistentFlags().String("filter-mode", "", "")
	return root
}
