package syncer

import (
	"context"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func requireRsync(t *testing.T) {
	t.Helper()
	if _, err := osexec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
}

func TestFetch_FileAndDirectory(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.writeMirror("docs/report.pdf", "pdf-payload")
	f.writeMirror("assets/deck/a.png", "img-a")
	f.writeMirror("assets/deck/b.png", "img-b")

	res, err := Fetch(context.Background(), f.runner, f.cfg, []string{"docs/report.pdf", "assets/deck", "no/such/path"}, false)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Fetched) != 2 {
		t.Fatalf("Fetched = %v, want 2 paths", res.Fetched)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "no/such/path" {
		t.Fatalf("Missing = %v", res.Missing)
	}
	for _, rel := range []string{"docs/report.pdf", "assets/deck/a.png", "assets/deck/b.png"} {
		if _, err := os.Stat(filepath.Join(f.local, rel)); err != nil {
			t.Errorf("fetched path missing locally: %s (%v)", rel, err)
		}
	}
}

func TestFetch_NeverOverwritesNewerLocal(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.writeMirror("data/table.xlsx", "old-mirror-version")
	f.writeLocal("data/table.xlsx", "newer-local-version")
	// Make the local copy strictly newer.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(f.local, "data/table.xlsx"), future, future); err != nil {
		t.Fatal(err)
	}

	if _, err := Fetch(context.Background(), f.runner, f.cfg, []string{"data/table.xlsx"}, false); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(f.local, "data/table.xlsx"))
	if string(got) != "newer-local-version" {
		t.Errorf("fetch overwrote newer local file: %q", got)
	}
}

func TestFetch_BlocksSecretsAndGit(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.writeMirror("proj/.env", "TOKEN=leak")
	f.writeMirror("proj/data.pdf", "fine")
	f.writeMirror("proj/.git", "gitdir: nope")

	if _, err := Fetch(context.Background(), f.runner, f.cfg, []string{"proj"}, false); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.local, "proj/data.pdf")); err != nil {
		t.Errorf("payload not fetched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.local, "proj/.env")); !os.IsNotExist(err) {
		t.Error("secret imported by fetch")
	}
	if _, err := os.Stat(filepath.Join(f.local, "proj/.git")); !os.IsNotExist(err) {
		t.Error(".git imported by fetch")
	}
}

func TestFetch_AllowedSecretComesThrough(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.AllowPatterns = []string{"/proj/.env"}
	f.writeMirror("proj/.env", "TOKEN=allowed")

	if _, err := Fetch(context.Background(), f.runner, f.cfg, []string{"proj"}, false); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(f.local, "proj/.env"))
	if err != nil {
		t.Fatalf("allowed secret not fetched: %v", err)
	}
	if string(got) != "TOKEN=allowed" {
		t.Errorf("unexpected content: %q", got)
	}
}

func TestFetch_DryRunWritesNothing(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.writeMirror("docs/x.pdf", "payload")

	res, err := Fetch(context.Background(), f.runner, f.cfg, []string{"docs/x.pdf"}, true)
	if err != nil {
		t.Fatalf("Fetch dry-run: %v", err)
	}
	if len(res.Fetched) != 1 {
		t.Fatalf("Fetched = %v", res.Fetched)
	}
	if _, err := os.Stat(filepath.Join(f.local, "docs/x.pdf")); !os.IsNotExist(err) {
		t.Error("dry-run materialized the file")
	}
}

func TestFetchScopeArgs(t *testing.T) {
	// Known file + known dir: parents traversable, dir gets /** expansion,
	// catch-all last. Order matters (first-match-wins in rsync).
	args, err := fetchScopeArgs([]fetchEntry{
		{rel: "a/b/report.pdf", isDir: false, known: true},
		{rel: "a/deck", isDir: true, known: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--include=/a/",
		"--include=/a/b/",
		"--include=/a/b/report.pdf",
		"--include=/a/deck/",
		"--include=/a/deck/**",
		"--exclude=*",
	}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("scope args = %v, want %v", args, want)
	}

	// Unknown shape (ssh): both dir and file forms emitted.
	sshArgs, _ := fetchScopeArgs([]fetchEntry{{rel: "x/y", known: false}})
	joined := strings.Join(sshArgs, " ")
	for _, w := range []string{"--include=/x/", "--include=/x/y/", "--include=/x/y/**", "--include=/x/y", "--exclude=*"} {
		if !strings.Contains(joined, w) {
			t.Fatalf("ssh scope args missing %q: %v", w, sshArgs)
		}
	}

	// Escaped (#228): a wildcard name matches itself, a backslash under a
	// "/**" is doubled, and a path that cannot be a rule is refused.
	esc, err := fetchScopeArgs([]fetchEntry{
		{rel: "notes/[x] a.pdf", known: true},
		{rel: `back\slash`, isDir: true, known: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(esc, "\n")
	for _, w := range []string{"--include=/notes/\\[x] a.pdf", `--include=/back\slash/`, `--include=/back\\slash/**`} {
		if !strings.Contains(joined, w) {
			t.Errorf("escaped scope args missing %q:\n%s", w, joined)
		}
	}
	if _, err := fetchScopeArgs([]fetchEntry{{rel: "../outside", known: true}}); err == nil {
		t.Error("a path outside the workspace became a fetch rule")
	}
}

// A requested path a filter layer keeps out (a shared folder an allow
// re-include opens, an unallowed secret) is reported as excluded, not
// fetched, and is not transferred (#231).
func TestFetch_ReportsAFilteredPathAsExcluded(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.SharedExcludes = []string{"team/ops"}
	f.cfg.AllowPatterns = []string{"/team/ops/.env"}
	f.writeMirror("team/ops/r.pdf", "shared")
	f.writeMirror("proj/.env", "TOKEN=leak")
	f.writeMirror("notes/a.pdf", "fine")
	res, err := Fetch(context.Background(), f.runner, f.cfg, []string{"team/ops/r.pdf", "proj/.env", "notes/a.pdf"}, false)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !slices.Equal(res.Fetched, []string{"notes/a.pdf"}) || !slices.Equal(res.Excluded, []string{"team/ops/r.pdf", "proj/.env"}) {
		t.Errorf("Fetched = %v, Excluded = %v", res.Fetched, res.Excluded)
	}
	for rel, want := range map[string]bool{"notes/a.pdf": true, "team/ops/r.pdf": false, "proj/.env": false} {
		if _, err := os.Stat(filepath.Join(f.local, rel)); (err == nil) != want {
			t.Errorf("%s in the workspace = %v, want %v", rel, err == nil, want)
		}
	}
}

// An absolute path under the workspace is read relative to it; any other
// absolute path stays refused (#231).
func TestFetch_AnAbsolutePathUnderTheWorkspace(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.writeMirror("notes/a.pdf", "fine")
	res, err := Fetch(context.Background(), f.runner, f.cfg, []string{filepath.Join(f.local, "notes/a.pdf")}, false)
	if err != nil || !slices.Equal(res.Fetched, []string{"notes/a.pdf"}) {
		t.Fatalf("Fetch(absolute under the workspace) = %+v, %v", res, err)
	}
	if _, err := Fetch(context.Background(), f.runner, f.cfg, []string{"/notes/a.pdf"}, false); err == nil || !strings.Contains(err.Error(), "not a path below the workspace root") {
		t.Errorf("Fetch(/notes/a.pdf) = %v, want a refusal", err)
	}
}

// A symlinked .sync-conflicts would carry a backup out of the workspace, so
// every workspace-side backup writer refuses the run and writes nothing
// through it (#231).
func TestWorkspaceBackupWritersRefuseASymlinkedConflictsDir(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	outside := t.TempDir()
	conflicts := filepath.Join(f.local, conflictsDirName)
	if err := os.RemoveAll(conflicts); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, conflicts); err != nil {
		t.Fatal(err)
	}
	f.writeLocal("notes/a.md", "old")
	f.writeMirror("notes/a.md", "newer in the mirror")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(f.mirror, "notes/a.md"), future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := PullTracked(f.cfg, PullOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "unsafe backup directory") {
		t.Errorf("PullTracked --force = %v, want a refusal", err)
	}
	// Without --force a tracked pull backs nothing up, so it is not refused.
	if _, err := PullTracked(f.cfg, PullOptions{}); err != nil {
		t.Errorf("PullTracked = %v, want no refusal", err)
	}
	if err := PullDirect(context.Background(), f.runner, f.cfg, false); err == nil || !strings.Contains(err.Error(), "unsafe backup directory") {
		t.Errorf("PullDirect = %v, want a refusal", err)
	}
	if _, err := Fetch(context.Background(), f.runner, f.cfg, []string{"notes/a.md"}, false); err == nil || !strings.Contains(err.Error(), "unsafe backup directory") {
		t.Errorf("Fetch = %v, want a refusal", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("a backup was written through the symlink: %v", entries)
	}
	if body, _ := os.ReadFile(filepath.Join(f.local, "notes/a.md")); string(body) != "old" {
		t.Errorf("the workspace copy changed: %q", body)
	}
}

// An SSH path of unknown shape goes to rsync in both forms, so it is excluded
// only when both are: a shared folder an allow re-include opens is fetched as
// a directory (#231).
func TestFetchEntryExcluded_AnUnknownShapeNeedsBothForms(t *testing.T) {
	f := newIntakeFixture(t)
	f.cfg.SharedExcludes = []string{"team/ops"}
	f.cfg.AllowPatterns = []string{"/team/ops/.env"}
	filter, err := newSyncFilter(f.cfg, f.mirror)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		e    fetchEntry
		want bool
	}{
		{fetchEntry{rel: "team/ops"}, false},
		{fetchEntry{rel: "team/ops", known: true}, true},
		{fetchEntry{rel: "team/ops", known: true, isDir: true}, false},
		{fetchEntry{rel: "team/ops/r.pdf"}, true},
		{fetchEntry{rel: "notes/a.pdf"}, false},
	} {
		if got := fetchEntryExcluded(filter, tc.e); got != tc.want {
			t.Errorf("fetchEntryExcluded(%+v) = %v, want %v", tc.e, got, tc.want)
		}
	}
}

// A pull that backs nothing up (a peer transfer passes no --backup) is not
// refused over a symlinked .sync-conflicts it never writes into.
func TestPullDirect_WithoutBackupIgnoresTheConflictsDir(t *testing.T) {
	workspace := t.TempDir()
	cfg := newPeerWorktreeTestConfig(t, workspace)
	cfg.LogFile = filepath.Join(t.TempDir(), "sync.log")
	if err := os.Symlink(t.TempDir(), filepath.Join(workspace, conflictsDirName)); err != nil {
		t.Fatal(err)
	}
	runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := PullDirect(context.Background(), runner, cfg, false); err != nil && strings.Contains(err.Error(), "unsafe backup directory") {
		t.Errorf("PullDirect refused although it passes no --backup: %v", err)
	}
}
