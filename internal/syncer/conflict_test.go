package syncer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestConflictDir_FilesystemSafeTimestamp(t *testing.T) {
	c := NewConflictDir()
	if strings.Contains(c.Timestamp, ":") {
		t.Errorf("timestamp contains colon (not filesystem-safe): %s", c.Timestamp)
	}
	// Sanity: should still parse as RFC3339 once colons are restored.
	restored := strings.Replace(c.Timestamp, "-", ":", 2) // restore HH:MM
	// Above is a rough check; the important thing is the format starts YYYY-MM-DDTHH-MM.
	if len(c.Timestamp) < len("2026-05-01T12-00-00Z") {
		t.Errorf("timestamp shorter than expected RFC3339-with-dashes: %s (restored guess: %s)", c.Timestamp, restored)
	}
}

func TestConflictDir_BackupRels(t *testing.T) {
	c := &ConflictDir{Timestamp: "2026-05-01T12-30-00Z"}

	pull := c.PullBackupRel()
	push := c.PushBackupRel()

	wantPull := filepath.Join(".sync-conflicts", "2026-05-01T12-30-00Z", "from-gdrive")
	wantPush := filepath.Join(".sync-conflicts", "2026-05-01T12-30-00Z", "from-workspace")

	if pull != wantPull {
		t.Errorf("PullBackupRel = %q, want %q", pull, wantPull)
	}
	if push != wantPush {
		t.Errorf("PushBackupRel = %q, want %q", push, wantPush)
	}

	// Both passes share the same <ts> parent.
	if filepath.Dir(pull) != filepath.Dir(push) {
		t.Errorf("pull and push share different timestamp dirs: %s vs %s", pull, push)
	}
}

func TestListConflicts_EmptyTreeReturnsNilNoError(t *testing.T) {
	dir := t.TempDir() // no .sync-conflicts/ inside
	entries, err := ListConflicts(dir)
	if err != nil {
		t.Fatalf("ListConflicts on empty tree returned error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("ListConflicts on empty tree returned %d entries, want 0", len(entries))
	}
}

func TestListConflicts_OldestFirst(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, conflictsDirName)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Three timestamp dirs. Touch them so mtimes are deterministic.
	stamps := []string{
		"2026-05-01T10-00-00Z",
		"2026-05-01T11-00-00Z",
		"2026-05-01T12-00-00Z",
	}
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	for i, s := range stamps {
		p := filepath.Join(root, s)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		mt := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}

	// Add a regular file inside conflicts root — must be ignored.
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("seed stray file: %v", err)
	}

	got, err := ListConflicts(dir)
	if err != nil {
		t.Fatalf("ListConflicts: %v", err)
	}
	if len(got) != len(stamps) {
		t.Fatalf("got %d entries, want %d (regular file should be skipped)", len(got), len(stamps))
	}
	for i, s := range stamps {
		if got[i].Timestamp != s {
			t.Errorf("entry[%d].Timestamp = %q, want %q (sort order broken?)", i, got[i].Timestamp, s)
		}
	}
}

// seedConflictDir creates <treeRoot>/.sync-conflicts/<stamp>/ with nested
// files totaling the given bodies, then pins the dir mtime.
func seedConflictDir(t *testing.T, treeRoot, stamp string, mtime time.Time, bodies map[string]string) {
	t.Helper()
	dir := filepath.Join(treeRoot, conflictsDirName, stamp)
	for rel, body := range bodies {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// Chtimes after writing files inside, or the writes refresh the mtime.
	if err := os.Chtimes(dir, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestPruneConflicts_RemovesOnlyOlderThanCutoff(t *testing.T) {
	tree := t.TempDir()
	now := time.Now()
	seedConflictDir(t, tree, "10d", now.Add(-10*24*time.Hour), map[string]string{"from-gdrive/a.txt": "aa"})
	seedConflictDir(t, tree, "20d", now.Add(-20*24*time.Hour), map[string]string{"from-gdrive/b.txt": "bbb"})
	seedConflictDir(t, tree, "40d", now.Add(-40*24*time.Hour), map[string]string{
		"from-gdrive/c/d.txt": "cccc",
		"from-workspace/e":    "ee",
	})

	res, err := PruneConflicts(tree, now.Add(-30*24*time.Hour), false)
	if err != nil {
		t.Fatalf("PruneConflicts: %v", err)
	}
	if len(res.Pruned) != 1 || res.Pruned[0].Timestamp != "40d" {
		t.Fatalf("Pruned = %+v, want only 40d", res.Pruned)
	}
	if res.Kept != 2 {
		t.Errorf("Kept = %d, want 2", res.Kept)
	}
	if want := int64(len("cccc") + len("ee")); res.Reclaimed != want {
		t.Errorf("Reclaimed = %d, want %d", res.Reclaimed, want)
	}
	if _, err := os.Stat(filepath.Join(tree, conflictsDirName, "40d")); !os.IsNotExist(err) {
		t.Error("40d should be removed from disk")
	}
	for _, keep := range []string{"10d", "20d"} {
		if _, err := os.Stat(filepath.Join(tree, conflictsDirName, keep)); err != nil {
			t.Errorf("%s should survive: %v", keep, err)
		}
	}
}

func TestPruneConflicts_DryRunRemovesNothing(t *testing.T) {
	tree := t.TempDir()
	now := time.Now()
	seedConflictDir(t, tree, "40d", now.Add(-40*24*time.Hour), map[string]string{"from-gdrive/a.txt": "aaaa"})

	res, err := PruneConflicts(tree, now.Add(-30*24*time.Hour), true)
	if err != nil {
		t.Fatalf("PruneConflicts: %v", err)
	}
	if !res.DryRun || len(res.Pruned) != 1 || res.Reclaimed != 4 {
		t.Fatalf("dry-run plan = %+v, want 1 entry / 4 bytes", res)
	}
	if _, err := os.Stat(filepath.Join(tree, conflictsDirName, "40d")); err != nil {
		t.Errorf("dry-run must not remove anything: %v", err)
	}
}

func TestPruneConflicts_NowCutoffPrunesEverythingAndRemovesRoot(t *testing.T) {
	tree := t.TempDir()
	now := time.Now()
	seedConflictDir(t, tree, "old", now.Add(-48*time.Hour), map[string]string{"from-gdrive/a": "x"})
	seedConflictDir(t, tree, "older", now.Add(-72*time.Hour), map[string]string{"from-workspace/b": "y"})

	res, err := PruneConflicts(tree, time.Now(), false)
	if err != nil {
		t.Fatalf("PruneConflicts: %v", err)
	}
	if len(res.Pruned) != 2 || res.Kept != 0 {
		t.Fatalf("Pruned = %+v Kept = %d, want all pruned", res.Pruned, res.Kept)
	}
	if _, err := os.Stat(filepath.Join(tree, conflictsDirName)); !os.IsNotExist(err) {
		t.Error("emptied .sync-conflicts root should be removed")
	}
}

func TestPruneConflicts_MissingRootIsNoop(t *testing.T) {
	res, err := PruneConflicts(t.TempDir(), time.Now(), false)
	if err != nil {
		t.Fatalf("PruneConflicts: %v", err)
	}
	if len(res.Pruned) != 0 || res.Kept != 0 || res.Reclaimed != 0 {
		t.Errorf("missing root should be a no-op: %+v", res)
	}
}

func TestPruneConflicts_StrayFileSurvivesAndBlocksRootRemoval(t *testing.T) {
	tree := t.TempDir()
	now := time.Now()
	seedConflictDir(t, tree, "old", now.Add(-48*time.Hour), map[string]string{"from-gdrive/a": "x"})
	stray := filepath.Join(tree, conflictsDirName, "stray.txt")
	if err := os.WriteFile(stray, []byte("keep me"), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := PruneConflicts(tree, time.Now(), false)
	if err != nil {
		t.Fatalf("PruneConflicts: %v", err)
	}
	if len(res.Pruned) != 1 {
		t.Fatalf("Pruned = %+v, want the timestamped dir", res.Pruned)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("stray file must survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, conflictsDirName)); err != nil {
		t.Errorf("root with strays must survive: %v", err)
	}
}

// A symlinked .sync-conflicts would make a prune delete directories outside
// the tree; listing and pruning refuse it and delete nothing (#231).
func TestPruneConflicts_RefusesASymlinkedRoot(t *testing.T) {
	tree := t.TempDir()
	outside := t.TempDir()
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.MkdirAll(filepath.Join(outside, "photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(outside, "photos"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, conflictsDirName)); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneConflicts(tree, time.Now(), false); err == nil || !strings.Contains(err.Error(), "unsafe backup directory") {
		t.Errorf("PruneConflicts = %v, want a refusal", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "photos")); err != nil {
		t.Errorf("the prune deleted a directory outside the tree: %v", err)
	}
}

// Status reports a .sync-conflicts it refuses to list instead of showing no
// backups (#231).
func TestStatus_ReportsARefusedConflictsDir(t *testing.T) {
	f := newIntakeFixture(t)
	conflicts := filepath.Join(f.local, conflictsDirName)
	if err := os.RemoveAll(conflicts); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), conflicts); err != nil {
		t.Fatal(err)
	}
	st, err := GetStatus(context.Background(), f.runner, f.cfg, &config.UserState{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.ConflictsError, "unsafe backup directory") {
		t.Errorf("ConflictsError = %q, want the refusal", st.ConflictsError)
	}
}

// A backup copy goes only when the workspace still holds a file of the same
// kind whose predecessor matched the baseline (#264); a file the workspace
// replaced with a directory keeps its backup.
func TestDropRoutineBackupsKeepsTypeChanges(t *testing.T) {
	f := newIntakeFixture(t)
	conflict := NewConflictDir()
	backup := filepath.Join(f.mirror, conflict.PushBackupRel())
	baseline := map[string]Fingerprint{}
	for _, rel := range []string{"notes/routine.md", "notes/item"} {
		abs := filepath.Join(backup, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		fp, err := FingerprintFile(abs, FingerprintFast)
		if err != nil {
			t.Fatal(err)
		}
		baseline[rel] = fp
	}
	f.writeLocal("notes/routine.md", "new")
	f.writeLocal("notes/item/child.md", "now a directory")

	dropRoutineBackups(f.cfg, conflict, baseline)
	if _, err := os.Stat(filepath.Join(backup, "notes/routine.md")); !os.IsNotExist(err) {
		t.Errorf("routine update backup kept: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(backup, "notes/item")); err != nil || string(got) != "old" {
		t.Errorf("file replaced by a directory lost its backup: %q, %v", got, err)
	}
}

// The cleanup never follows a symlinked .sync-conflicts out of the mirror.
func TestDropRoutineBackupsRefusesASymlinkedRoot(t *testing.T) {
	f := newIntakeFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(f.mirror, conflictsDirName)); err != nil {
		t.Fatal(err)
	}
	conflict := NewConflictDir()
	victim := filepath.Join(outside, conflict.Timestamp, backupSubFromWorkspace, "notes/a.md")
	if err := os.MkdirAll(filepath.Dir(victim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp, err := FingerprintFile(victim, FingerprintFast)
	if err != nil {
		t.Fatal(err)
	}
	f.writeLocal("notes/a.md", "new")

	dropRoutineBackups(f.cfg, conflict, map[string]Fingerprint{"notes/a.md": fp})
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("a file outside the mirror was removed: %v", err)
	}
}

// Without a sha in the baseline row, a backup goes only on the exact mtime: a
// same-size cloud edit inside the same second keeps its copy.
func TestDropRoutineBackupsNeedsTheExactMtimeWithoutASha(t *testing.T) {
	f := newIntakeFixture(t)
	conflict := NewConflictDir()
	backup := filepath.Join(f.mirror, conflict.PushBackupRel(), "notes/a.md")
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 18, 4, 0, 0, 500_000_000, time.UTC)
	if err := os.Chtimes(backup, when, when); err != nil {
		t.Fatal(err)
	}
	f.writeLocal("notes/a.md", "newer")
	sameSecond := Fingerprint{Size: int64(len("edited")), Mtime: when.Add(-100 * time.Millisecond)}

	dropRoutineBackups(f.cfg, conflict, map[string]Fingerprint{"notes/a.md": sameSecond})
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("a copy matching only to the second was removed: %v", err)
	}
	dropRoutineBackups(f.cfg, conflict, map[string]Fingerprint{"notes/a.md": {Size: sameSecond.Size, Mtime: when}})
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("an exact match was kept: %v", err)
	}
}
