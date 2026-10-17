package syncer

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestUnsupportedPathName(t *testing.T) {
	for _, tc := range []struct {
		rel  string
		want bool
	}{
		{"notes/report.md", false},
		{"report.md", false},
		{"archive.tar.gz", false},
		{"report.md ", true},        // trailing-space leaf
		{"docs /report.md", true},   // trailing-space directory segment
		{"a/b ./c/d.md", true},      // nested trailing-space segment
		{"draft.", true},            // trailing-dot leaf
		{"dotdir./x.md", true},      // trailing-dot directory segment
		{"weird. name/file", false}, // inner space is fine
	} {
		if got := UnsupportedPathName(tc.rel); got != tc.want {
			t.Errorf("UnsupportedPathName(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}

func TestPushArgs_UnsupportedNameExcludesLocalOnly(t *testing.T) {
	conflict := &ConflictDir{Timestamp: "2026-09-27T00-00-00Z"}

	local := newTestConfig(t)
	args := pushArgs(local, conflict, runtimeFilters{}, false)
	for _, want := range []string{"--exclude=* ", "--exclude=*."} {
		if !slices.Contains(args, want) {
			t.Errorf("local pushArgs missing %q\nargs: %v", want, args)
		}
	}

	ssh := newTestConfig(t)
	ssh.Target = Target{Kind: TargetSSH, Host: "peer", Path: "/work"}
	ssh.Profile = PeerProfile
	args = pushArgs(ssh, conflict, runtimeFilters{}, false)
	for _, forbidden := range []string{"--exclude=* ", "--exclude=*."} {
		if slices.Contains(args, forbidden) {
			t.Errorf("ssh pushArgs leaked %q — peer targets must not drop these names\nargs: %v", forbidden, args)
		}
	}
}

// A trailing-space folder must leave the create/update/delete/conflict lists
// entirely: it is what Dropbox would rename to a "(Unicode Encoding
// Conflict)" twin on arrival, and that twin is the conflict loop this plan
// field exists to break. Sibling files with storable names plan normally.
func TestPlanPush_TrailingSpaceFolderGoesToUnsupported(t *testing.T) {
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("docs /report.md", "payload")
	f.writeLocal("notes/keep.md", "payload")
	// A mirror-only unsupported rel must not become a conflict either.
	f.writeMirror("old /twin.md", "payload")

	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatalf("PlanPush: %v", err)
	}
	if !slices.Equal(plan.Unsupported, []string{"docs /report.md"}) {
		t.Errorf("Unsupported = %v, want the workspace name only", plan.Unsupported)
	}
	if !slices.Equal(plan.MirrorUnsupported, []string{"old /twin.md"}) || len(plan.Leftovers) != 0 {
		t.Errorf("MirrorUnsupported = %v, Leftovers = %v; want the unproven mirror-only name listed only", plan.MirrorUnsupported, plan.Leftovers)
	}
	if len(plan.Conflicts) != 0 {
		t.Errorf("Conflicts = %+v, want none", plan.Conflicts)
	}
	if !slices.Contains(plan.Creates, "notes/keep.md") {
		t.Errorf("Creates = %v, want it to contain notes/keep.md", plan.Creates)
	}
	for _, list := range [][]string{plan.Creates, plan.Updates, plan.Deletes, plan.SkippedPolicy} {
		for _, rel := range []string{"docs /report.md", "old /twin.md"} {
			if slices.Contains(list, rel) {
				t.Errorf("unsupported rel %q leaked into a transfer list: %v", rel, list)
			}
		}
	}
}

// The rsync leaf-pattern excludes and the Go predicate must agree, the same
// parity TestAlwaysExcluded_RsyncParity enforces for the always-on rules: a
// path only one side skips would either upload a name Dropbox renames or hide
// a mirror path from the plan.
func TestUnsupportedNameExcludes_RsyncParity(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	cfg := newTestConfig(t)
	excludeArgs := unsupportedNameExcludeArgs(cfg)
	if len(excludeArgs) == 0 {
		t.Fatal("local target produced no unsupported-name excludes")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	unsupported := []string{
		"report.md ",
		"docs /inner.md",
		"draft.",
		"dotdir./x.md",
	}
	kept := []string{
		"keep.md",
		"notes/inner.md",
		"archive.tar.gz",
	}
	for _, rel := range append(slices.Clone(unsupported), kept...) {
		path := filepath.Join(source, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	args := append([]string{"-r", "--dry-run", "--out-format=%n"}, excludeArgs...)
	args = append(args, source+"/", filepath.Join(root, "dest")+"/")
	out, err := exec.Command("rsync", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("rsync: %v\n%s", err, out)
	}
	var rsyncKept []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" && !strings.HasSuffix(line, "/") {
			rsyncKept = append(rsyncKept, line)
		}
	}

	var goKept []string
	err = filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == source {
			return err
		}
		rel, _ := filepath.Rel(source, path)
		rel = filepath.ToSlash(rel)
		if UnsupportedPathName(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			goKept = append(goKept, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(rsyncKept)
	slices.Sort(goKept)
	slices.Sort(kept)
	if !slices.Equal(rsyncKept, kept) {
		t.Errorf("rsync kept %v, want %v", rsyncKept, kept)
	}
	if !slices.Equal(goKept, kept) {
		t.Errorf("UnsupportedPathName kept %v, want %v", goKept, kept)
	}
}

func TestGetStatus_CountsUnsupportedNames(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeLocal("docs /report.md", "payload")
	f.writeLocal("notes/keep.md", "payload")

	st, err := GetStatus(context.Background(), f.runner, f.cfg, &config.UserState{}, nil)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	// The trailing-space directory and the file inside it both count.
	if st.UnsupportedNames != 2 {
		t.Errorf("UnsupportedNames = %d, want 2", st.UnsupportedNames)
	}
}

func TestGetStatus_NoUnsupportedNamesByDefault(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeLocal("notes/keep.md", "payload")

	st, err := GetStatus(context.Background(), f.runner, f.cfg, &config.UserState{}, nil)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.UnsupportedNames != 0 {
		t.Errorf("UnsupportedNames = %d, want 0", st.UnsupportedNames)
	}
}
