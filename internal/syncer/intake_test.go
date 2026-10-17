package syncer

import (
	"context"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// intakeFixture sets up a synthetic mirror + workspace pair with a
// real .dotfiles/sync layout, baseline.manifest seeded, and
// returns a Config ready for Intake().
type intakeFixture struct {
	t      *testing.T
	root   string
	local  string
	mirror string
	cfg    *Config
	runner *exec.Runner
}

func newIntakeFixture(t *testing.T) *intakeFixture {
	t.Helper()
	root := t.TempDir()
	local := filepath.Join(root, "workspace")
	mirror := filepath.Join(root, "mirror")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}

	paths := ResolveLocalPaths(local + "/")
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatalf("EnsureLocalLayout: %v", err)
	}
	cfg := &Config{
		LocalPath:    local + "/",
		MirrorPath:   mirror + "/",
		Target:       Target{Kind: TargetLocal, Path: mirror + "/"},
		FilterMode:   FilterModeExclude,
		IncludeFile:  paths.IncludeFile,
		ExcludesFile: paths.ExcludeFile,
		IgnoreFile:   paths.IgnoreFile,
		ConfigDir:    paths.StoreDir,
		LogFile:      filepath.Join(root, "gdrive-sync.log"),
		LockDir:      filepath.Join(root, "lock"),
		MaxDelete:    100,
		Propagation:  DefaultPropagationPolicy(),
		LocalPaths:   paths,
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := exec.NewRunner(false, logger)
	return &intakeFixture{t: t, root: root, local: local, mirror: mirror, cfg: cfg, runner: runner}
}

// writeMirror creates a file under mirror/<rel> with body and returns
// its mtime so tests can seed baseline manifest entries.
func (f *intakeFixture) writeMirror(rel, body string) time.Time {
	f.t.Helper()
	abs := filepath.Join(f.mirror, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		f.t.Fatal(err)
	}
	return info.ModTime()
}

func (f *intakeFixture) writeLocal(rel, body string) time.Time {
	f.t.Helper()
	abs := filepath.Join(f.local, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		f.t.Fatal(err)
	}
	return info.ModTime()
}

func (f *intakeFixture) seedBaseline(rel, body string, mtime time.Time) {
	f.t.Helper()
	existing, _ := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	existing[rel] = Fingerprint{Size: int64(len(body)), Mtime: mtime}
	if err := SaveBaselineManifest(f.cfg.LocalPaths.BaselineFile, existing); err != nil {
		f.t.Fatal(err)
	}
}

func TestIntake_SkipsBaselineMatches(t *testing.T) {
	f := newIntakeFixture(t)
	body := "ours-from-push"
	mtime := f.writeMirror("projects/foo/note.md", body)
	f.seedBaseline("projects/foo/note.md", body, mtime)

	res, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	if len(res.Intaked) != 0 {
		t.Errorf("Intaked = %v, want empty (baseline match should skip)", res.Intaked)
	}
	if len(res.SkippedBase) != 1 || res.SkippedBase[0] != "projects/foo/note.md" {
		t.Errorf("SkippedBase = %v", res.SkippedBase)
	}
}

func TestIntake_RsyncCopiesNewlineFilenameExactly(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	rel := "incoming/line\nbreak.txt"
	body := "content with exact filename"
	f.writeMirror(rel, body)

	res, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	if len(res.Intaked) != 1 || res.Intaked[0] != rel {
		t.Fatalf("Intaked = %q, want exact path %q", res.Intaked, rel)
	}
	got, err := os.ReadFile(filepath.Join(res.StagingDir, rel))
	if err != nil {
		t.Fatalf("reading staged path: %v", err)
	}
	if string(got) != body {
		t.Errorf("staged content = %q, want %q", got, body)
	}
	if _, err := os.Stat(filepath.Join(res.StagingDir, "incoming", "line")); !os.IsNotExist(err) {
		t.Errorf("rsync created a split filename artifact (err=%v)", err)
	}
}

func TestPullTracked_RestoresMissingBaselineFile(t *testing.T) {
	f := newIntakeFixture(t)
	body := "binary-payload"
	mtime := f.writeMirror("assets/image.bin", body)
	f.seedBaseline("assets/image.bin", body, mtime)

	res, err := PullTracked(f.cfg, PullOptions{})
	if err != nil {
		t.Fatalf("PullTracked: %v", err)
	}
	if len(res.Restored) != 1 || res.Restored[0] != "assets/image.bin" {
		t.Fatalf("Restored = %v, want assets/image.bin", res.Restored)
	}
	got, err := os.ReadFile(filepath.Join(f.local, "assets/image.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("restored body = %q, want %q", got, body)
	}
}

func TestPullTracked_UpdatesLocalWhenOnlyDriveChanged(t *testing.T) {
	f := newIntakeFixture(t)
	v1Mtime := f.writeMirror("reports/chart.png", "v1")
	f.writeLocal("reports/chart.png", "v1")
	if err := os.Chtimes(filepath.Join(f.local, "reports/chart.png"), v1Mtime, v1Mtime); err != nil {
		t.Fatal(err)
	}
	f.seedBaseline("reports/chart.png", "v1", v1Mtime)

	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(f.mirror, "reports/chart.png"), []byte("v2-from-drive"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := PullTracked(f.cfg, PullOptions{})
	if err != nil {
		t.Fatalf("PullTracked: %v", err)
	}
	if len(res.Pulled) != 1 || res.Pulled[0] != "reports/chart.png" {
		t.Fatalf("Pulled = %v, want reports/chart.png", res.Pulled)
	}
	got, err := os.ReadFile(filepath.Join(f.local, "reports/chart.png"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2-from-drive" {
		t.Errorf("local body = %q, want Drive version", got)
	}
	base, err := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if base["reports/chart.png"].Sha == "" {
		t.Errorf("baseline should be strict sha after pull update: %+v", base["reports/chart.png"])
	}
}

func TestPullTracked_ConflictPreservesLocalUntilForced(t *testing.T) {
	f := newIntakeFixture(t)
	v1Mtime := f.writeMirror("shared/deck.pptx", "v1")
	f.writeLocal("shared/deck.pptx", "v1")
	f.seedBaseline("shared/deck.pptx", "v1", v1Mtime)

	if err := os.WriteFile(filepath.Join(f.local, "shared/deck.pptx"), []byte("v2-local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.mirror, "shared/deck.pptx"), []byte("v2-drive"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := PullTracked(f.cfg, PullOptions{})
	if err != nil {
		t.Fatalf("PullTracked: %v", err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].RelPath != "shared/deck.pptx" {
		t.Fatalf("Conflicts = %+v, want shared/deck.pptx", res.Conflicts)
	}
	localBody, err := os.ReadFile(filepath.Join(f.local, "shared/deck.pptx"))
	if err != nil {
		t.Fatal(err)
	}
	if string(localBody) != "v2-local" {
		t.Errorf("local file overwritten during conflict: %q", localBody)
	}
	if res.Conflicts[0].BackupPath != "" {
		t.Fatalf("non-forced conflict advertised backup path unexpectedly: %s", res.Conflicts[0].BackupPath)
	}
}

func TestPullTracked_DirectoryConflictDoesNotAdvertiseMissingBackup(t *testing.T) {
	f := newIntakeFixture(t)
	rel := "shared/deck.pptx"
	v1Mtime := f.writeMirror(rel, "v1")
	f.seedBaseline(rel, "v1", v1Mtime)
	if err := os.MkdirAll(filepath.Join(f.local, rel), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.mirror, rel), []byte("v2-drive"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := PullTracked(f.cfg, PullOptions{})
	if err != nil {
		t.Fatalf("PullTracked: %v", err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].RelPath != rel {
		t.Fatalf("Conflicts = %+v, want %s", res.Conflicts, rel)
	}
	if res.Conflicts[0].BackupPath != "" {
		t.Fatalf("directory conflict advertised missing backup path: %s", res.Conflicts[0].BackupPath)
	}
	if !strings.Contains(res.Conflicts[0].Reason, "no backup was created") {
		t.Fatalf("conflict reason should explain backup absence, got %q", res.Conflicts[0].Reason)
	}
	if fi, err := os.Stat(filepath.Join(f.local, rel)); err != nil || !fi.IsDir() {
		t.Fatalf("local directory should remain untouched; stat=%v err=%v", fi, err)
	}
}

func TestPullTracked_ForceOverwritesLocalAndBacksItUp(t *testing.T) {
	f := newIntakeFixture(t)
	v1Mtime := f.writeMirror("shared/deck.pptx", "v1")
	f.writeLocal("shared/deck.pptx", "v1")
	f.seedBaseline("shared/deck.pptx", "v1", v1Mtime)

	if err := os.WriteFile(filepath.Join(f.local, "shared/deck.pptx"), []byte("v2-local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.mirror, "shared/deck.pptx"), []byte("v2-drive"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := PullTracked(f.cfg, PullOptions{Force: true})
	if err != nil {
		t.Fatalf("PullTracked force: %v", err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].RelPath != "shared/deck.pptx" {
		t.Fatalf("Conflicts = %+v, want shared/deck.pptx", res.Conflicts)
	}
	localBody, err := os.ReadFile(filepath.Join(f.local, "shared/deck.pptx"))
	if err != nil {
		t.Fatal(err)
	}
	if string(localBody) != "v2-drive" {
		t.Errorf("local body = %q, want Drive version", localBody)
	}
	backupBody, err := os.ReadFile(res.Conflicts[0].BackupPath)
	if err != nil {
		t.Fatalf("missing conflict backup %s: %v", res.Conflicts[0].BackupPath, err)
	}
	if string(backupBody) != "v2-local" {
		t.Errorf("backup body = %q, want local version", backupBody)
	}
}

func TestPlanPush_DetectsDriveConflictWithoutAutoPull(t *testing.T) {
	f := newIntakeFixture(t)
	v1Mtime := f.writeMirror("shared/deck.pptx", "v1")
	f.writeLocal("shared/deck.pptx", "v1")
	f.seedBaseline("shared/deck.pptx", "v1", v1Mtime)

	if err := os.WriteFile(filepath.Join(f.local, "shared/deck.pptx"), []byte("v2-local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.mirror, "shared/deck.pptx"), []byte("v2-drive"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatalf("PlanPush: %v", err)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].RelPath != "shared/deck.pptx" {
		t.Fatalf("Conflicts = %+v, want shared/deck.pptx", plan.Conflicts)
	}
}

func TestPush_DoesNotRestoreOldBaselinePathBeforePush(t *testing.T) {
	f := newIntakeFixture(t)
	mtime := f.writeMirror("docs/old.bin", "payload")
	f.writeLocal("docs/old.bin", "payload")
	f.seedBaseline("docs/old.bin", "payload", mtime)

	if err := os.Rename(filepath.Join(f.local, "docs/old.bin"), filepath.Join(f.local, "docs/new.bin")); err != nil {
		t.Fatal(err)
	}
	if err := Push(context.Background(), f.runner, f.cfg, false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.local, "docs/old.bin")); !os.IsNotExist(err) {
		t.Fatalf("push restored old local path before upload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "docs/new.bin")); err != nil {
		t.Fatalf("push did not upload renamed path: %v", err)
	}
}

func TestIntake_StagesGdriveOriginNewFile(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeMirror("from-cloud/note.md", "from drive")

	res, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	if len(res.Intaked) != 1 || res.Intaked[0] != "from-cloud/note.md" {
		t.Fatalf("Intaked = %v, want [from-cloud/note.md]", res.Intaked)
	}
	staged := filepath.Join(res.StagingDir, "from-cloud/note.md")
	if _, err := os.Stat(staged); err != nil {
		t.Errorf("staged file missing at %s: %v", staged, err)
	}
	// Workspace tree itself untouched.
	if _, err := os.Stat(filepath.Join(f.local, "from-cloud/note.md")); !os.IsNotExist(err) {
		t.Errorf("workspace got the file directly — intake must stage to inbox/gdrive only")
	}
}

func TestIntake_SkipsTrackedGdriveModifiedFile(t *testing.T) {
	f := newIntakeFixture(t)
	// We pushed v1 (recorded in baseline); GDrive editor changed it to v2.
	v1Mtime := f.writeMirror("doc.md", "v1-from-us")
	f.writeLocal("doc.md", "v1-from-us")
	if err := os.Chtimes(filepath.Join(f.local, "doc.md"), v1Mtime, v1Mtime); err != nil {
		t.Fatal(err)
	}
	f.seedBaseline("doc.md", "v1-from-us", v1Mtime)
	// Now overwrite mirror with v2 (longer + later).
	time.Sleep(10 * time.Millisecond) // ensure mtime advances
	if err := os.WriteFile(filepath.Join(f.mirror, "doc.md"), []byte("v2-from-collaborator"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	if len(res.Intaked) != 0 {
		t.Errorf("Intaked = %v, want 0 (tracked change should pull)", res.Intaked)
	}
	if res.Pull != nil {
		t.Fatalf("Pull = %+v, want nil; tracked pull is manual", res.Pull)
	}
	if len(res.SkippedTracked) != 1 || res.SkippedTracked[0] != "doc.md" {
		t.Fatalf("SkippedTracked = %+v, want doc.md", res.SkippedTracked)
	}
	got, err := os.ReadFile(filepath.Join(f.local, "doc.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1-from-us" {
		t.Errorf("local doc = %q, want original local version", got)
	}
}

func TestIntake_SkipsImportsManifestMatches(t *testing.T) {
	f := newIntakeFixture(t)
	mtime := f.writeMirror("from-cloud.md", "from drive")

	// First intake — stages it.
	res1, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res1.Intaked) != 1 {
		t.Fatalf("first intake should stage 1; got %v", res1.Intaked)
	}

	// Operator processes the file (moves it out of staging).
	if err := os.RemoveAll(res1.StagingDir); err != nil {
		t.Fatal(err)
	}

	// Second intake — should NOT re-stage (imports manifest still has it).
	res2, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Intaked) != 0 {
		t.Errorf("second intake re-staged after operator processing: %v", res2.Intaked)
	}
	if len(res2.SkippedImports) != 1 {
		t.Errorf("SkippedImports = %v, want 1", res2.SkippedImports)
	}
	_ = mtime // referenced only to keep test self-explanatory
}

func TestIntake_RestagesOnUpstreamChange(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeMirror("from-cloud.md", "v1-from-drive")

	res1, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil || len(res1.Intaked) != 1 {
		t.Fatalf("first intake setup failed: %v %v", err, res1)
	}

	time.Sleep(10 * time.Millisecond)
	// Mirror file changes upstream.
	if err := os.WriteFile(filepath.Join(f.mirror, "from-cloud.md"), []byte("v2-newer-from-drive"), 0o644); err != nil {
		t.Fatal(err)
	}

	res2, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Intaked) != 1 {
		t.Errorf("Intaked = %v, want 1 (upstream changed)", res2.Intaked)
	}
	// Two intake runs → two distinct timestamped subdirs.
	entries, err := os.ReadDir(filepath.Join(f.local, "inbox", "gdrive"))
	if err != nil {
		t.Fatal(err)
	}
	dirCount := 0
	for _, e := range entries {
		if e.IsDir() {
			dirCount++
		}
	}
	if dirCount != 2 {
		t.Errorf("got %d run-dirs in inbox/gdrive/, want 2", dirCount)
	}
}

func TestPullTracked_DetectsMirrorDeletion_Tombstone(t *testing.T) {
	f := newIntakeFixture(t)
	mtime := f.writeMirror("ephemeral.md", "ours")
	f.seedBaseline("ephemeral.md", "ours", mtime)
	// Mirror loses the file.
	if err := os.Remove(filepath.Join(f.mirror, "ephemeral.md")); err != nil {
		t.Fatal(err)
	}

	res, err := PullTracked(f.cfg, PullOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tombstones) != 1 || res.Tombstones[0].RelPath != "ephemeral.md" {
		t.Errorf("Tombstones = %v, want [ephemeral.md]", res.Tombstones)
	}
	// Local tree must NOT have a corresponding deletion — local was authoritative.
	if _, err := os.Stat(f.local); err != nil {
		t.Errorf("local workspace missing: %v", err)
	}
	// Tombstone log on disk.
	tomb, err := LoadTombstones(f.cfg.LocalPaths.TombstonesFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(tomb) != 1 {
		t.Errorf("tombstones.log has %d entries, want 1", len(tomb))
	}
}

func TestIntake_NeverIntakesInboxOrDotfiles(t *testing.T) {
	f := newIntakeFixture(t)
	// Mirror somehow has these (operator pushed before always-on excludes
	// landed). Intake must refuse to echo them back.
	f.writeMirror(".dotfiles/sync/secrets.txt", "should-never-cross")
	f.writeMirror("inbox/gdrive/2025-99-99/leftover.md", "should-never-cross")
	f.writeMirror("normal/file.md", "fine")

	res, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range res.Intaked {
		if strings.HasPrefix(rel, ".dotfiles") || strings.HasPrefix(rel, "inbox/gdrive") {
			t.Errorf("Intake leaked always-excluded path: %s", rel)
		}
	}
	if len(res.Intaked) != 1 || res.Intaked[0] != "normal/file.md" {
		t.Errorf("Intaked = %v, want [normal/file.md]", res.Intaked)
	}
}

func TestIntake_DryRun_NoSideEffects(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeMirror("from-cloud.md", "drive")

	_, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	// No staging dir should exist.
	entries, err := os.ReadDir(filepath.Join(f.local, "inbox", "gdrive"))
	if err == nil && len(entries) > 0 {
		t.Errorf("dry-run created %d entries in inbox/gdrive/", len(entries))
	}
	// Imports manifest should still be empty.
	imp, _ := LoadImportsManifest(f.cfg.LocalPaths.ImportsFile)
	if len(imp) != 0 {
		t.Errorf("dry-run wrote imports manifest: %v", imp)
	}
}

func TestIntake_StrictMode_SkipsTrackedContentChangeWithSameSizeMtime(t *testing.T) {
	f := newIntakeFixture(t)
	body := "exactly10b"
	mtime := f.writeMirror("paranoid.md", body)
	f.writeLocal("paranoid.md", body)
	// Baseline records strict-mode fingerprint of v1.
	v1FP, err := FingerprintFile(filepath.Join(f.mirror, "paranoid.md"), FingerprintStrict)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveBaselineManifest(f.cfg.LocalPaths.BaselineFile, map[string]Fingerprint{
		"paranoid.md": v1FP,
	}); err != nil {
		t.Fatal(err)
	}

	// Replace with same-length content + same mtime — fast mode would
	// miss this, strict mode catches it.
	if err := os.WriteFile(filepath.Join(f.mirror, "paranoid.md"), []byte("ALTERED10b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(f.mirror, "paranoid.md"), mtime, mtime); err != nil {
		t.Fatal(err)
	}

	// Fast intake no longer stages or pulls tracked changes.
	resFast, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{Strict: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(resFast.Intaked) != 0 || resFast.Pull != nil || len(resFast.SkippedTracked) != 1 {
		t.Fatalf("tracked strict change should be skipped: intaked=%v pull=%+v skipped=%v", resFast.Intaked, resFast.Pull, resFast.SkippedTracked)
	}

	// Reset local/baseline before strict re-run so we observe the same behavior.
	if err := os.WriteFile(filepath.Join(f.local, "paranoid.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(f.local, "paranoid.md"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := SaveBaselineManifest(f.cfg.LocalPaths.BaselineFile, map[string]Fingerprint{
		"paranoid.md": v1FP,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.cfg.LocalPaths.ImportsFile); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := SaveImportsManifest(f.cfg.LocalPaths.ImportsFile, map[string]ImportEntry{}); err != nil {
		t.Fatal(err)
	}

	resStrict, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(resStrict.Intaked) != 0 || resStrict.Pull != nil || len(resStrict.SkippedTracked) != 1 {
		t.Errorf("strict-mode Intake should skip tracked content change: intaked=%v pull=%+v skipped=%v", resStrict.Intaked, resStrict.Pull, resStrict.SkippedTracked)
	}
}

func TestRefreshBaseline_PopulatesFromMirrorTree(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeMirror("notes/a.md", "a")
	f.writeMirror("notes/b.md", "bb")
	f.writeLocal("notes/a.md", "a")
	f.writeLocal("notes/b.md", "bb")
	// .dotfiles/ and inbox/gdrive/ files must NOT land in baseline.
	f.writeMirror(".dotfiles/secret.txt", "x")
	f.writeMirror("inbox/gdrive/old/staged.md", "z")

	if err := RefreshBaseline(f.cfg, FingerprintFast); err != nil {
		t.Fatal(err)
	}
	base, err := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base["notes/a.md"]; !ok {
		t.Error("notes/a.md missing from baseline")
	}
	if _, ok := base["notes/b.md"]; !ok {
		t.Error("notes/b.md missing from baseline")
	}
	for k := range base {
		if strings.HasPrefix(k, ".dotfiles") || strings.HasPrefix(k, "inbox/gdrive") {
			t.Errorf("baseline includes always-excluded path: %s", k)
		}
	}
}

func TestRefreshBaseline_DoesNotAdoptMirrorOnlyNewFiles(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeMirror("unaccepted-drive-only.md", "still needs review")

	if err := RefreshBaseline(f.cfg, FingerprintFast); err != nil {
		t.Fatal(err)
	}
	base, err := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base["unaccepted-drive-only.md"]; ok {
		t.Fatalf("mirror-only file was adopted into baseline: %v", base)
	}
	res, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Intaked) != 1 || res.Intaked[0] != "unaccepted-drive-only.md" {
		t.Errorf("Intaked = %v, want unaccepted-drive-only.md", res.Intaked)
	}
}

func TestIntake_AppliesExcludeIgnoreAndSharedFilters(t *testing.T) {
	f := newIntakeFixture(t)
	f.cfg.SharedExcludes = []string{"shared/drop"}
	if err := os.WriteFile(f.cfg.LocalPaths.IgnoreFile, []byte("ignored-dir/\n*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f.writeMirror("node_modules/pkg/index.js", "skip")
	f.writeMirror(".git/config", "skip")
	f.writeMirror("ignored-dir/file.md", "skip")
	f.writeMirror("scratch.tmp", "skip")
	f.writeMirror("shared/drop/file.md", "skip")
	f.writeMirror("normal/file.md", "keep")

	res, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Intaked) != 1 || res.Intaked[0] != "normal/file.md" {
		t.Fatalf("Intaked = %v, want [normal/file.md]", res.Intaked)
	}
}

func TestIntake_IncludeModeStagesOnlyIncludedPayloadsCaseInsensitive(t *testing.T) {
	f := newIntakeFixture(t)
	f.cfg.FilterMode = FilterModeInclude
	f.cfg.IncludePatterns = []string{"*.pdf", "*.hwp*", "*.mp3"}

	f.writeMirror("docs/report.PDF", "keep")
	f.writeMirror("docs/source.md", "skip")
	f.writeMirror("audio/voice.MP3", "keep")
	f.writeMirror("forms/application.HWPX", "keep")
	f.writeMirror("node_modules/pkg/sound.mp3", "skip")

	res, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"audio/voice.MP3", "docs/report.PDF", "forms/application.HWPX"}
	if strings.Join(res.Intaked, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Intaked = %v, want %v", res.Intaked, want)
	}
}

func TestRefreshBaseline_IncludeModeSkipsTextAndHonorsCaseInsensitive(t *testing.T) {
	f := newIntakeFixture(t)
	f.cfg.FilterMode = FilterModeInclude
	f.cfg.IncludePatterns = []string{"*.pdf"}

	f.writeMirror("docs/report.PDF", "keep")
	f.writeLocal("docs/report.PDF", "keep")
	f.writeMirror("docs/source.md", "skip")
	f.writeLocal("docs/source.md", "skip")

	if err := RefreshBaseline(f.cfg, FingerprintFast); err != nil {
		t.Fatal(err)
	}
	base, err := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base["docs/report.PDF"]; !ok {
		t.Fatalf("included PDF missing from baseline: %v", base)
	}
	if _, ok := base["docs/source.md"]; ok {
		t.Fatalf("text source should not be in include-mode baseline: %v", base)
	}
}

// Union filter: tracked files (whatever their extension) and untracked
// binary-allowlist files are both payload; untracked source files are not.
func TestPlanPush_IncludeModeUnionsTrackedAndBinaryAllowlist(t *testing.T) {
	f := newIntakeFixture(t)
	f.cfg.FilterMode = FilterModeInclude
	f.cfg.IncludePatterns = []string{"*.pdf"}
	if err := osexec.Command("git", "-C", f.local, "init").Run(); err != nil {
		t.Skipf("git init unavailable: %v", err)
	}
	f.writeLocal("notes.md", "git-owned source")
	f.writeLocal("asset.PDF", "drive-owned")
	f.writeLocal("untracked.md", "neither tracked nor binary")
	if err := osexec.Command("git", "-C", f.local, "add", "notes.md").Run(); err != nil {
		t.Skipf("git add unavailable: %v", err)
	}

	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"asset.PDF", "notes.md"}
	if len(plan.Creates) != len(want) || plan.Creates[0] != want[0] || plan.Creates[1] != want[1] {
		t.Fatalf("Creates = %v, want %v", plan.Creates, want)
	}
	// Tracked edits must never surface as conflicts — a conflict here would
	// wedge the clean-mode scheduler.
	if plan.HasConflicts() {
		t.Fatalf("unexpected conflicts: %+v", plan.Conflicts)
	}
}

func TestRefreshBaseline_AppliesExcludeIgnoreAndSharedFilters(t *testing.T) {
	f := newIntakeFixture(t)
	f.cfg.SharedExcludes = []string{"shared/drop"}
	if err := os.WriteFile(f.cfg.LocalPaths.IgnoreFile, []byte("ignored-dir/\n*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f.writeMirror("node_modules/pkg/index.js", "skip")
	f.writeMirror(".git/config", "skip")
	f.writeMirror("ignored-dir/file.md", "skip")
	f.writeMirror("scratch.tmp", "skip")
	f.writeMirror("shared/drop/file.md", "skip")
	f.writeMirror("normal/file.md", "keep")
	f.writeLocal("normal/file.md", "keep")

	if err := RefreshBaseline(f.cfg, FingerprintFast); err != nil {
		t.Fatal(err)
	}
	base, err := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base["normal/file.md"]; !ok {
		t.Fatalf("normal/file.md missing from baseline: %v", base)
	}
	for _, rel := range []string{
		"node_modules/pkg/index.js",
		".git/config",
		"ignored-dir/file.md",
		"scratch.tmp",
		"shared/drop/file.md",
	} {
		if _, ok := base[rel]; ok {
			t.Errorf("baseline included filtered path %s", rel)
		}
	}
}

// Tracked files are sync payload since the union filter — the baseline
// must index them like any other mirrored file so drift detection and
// delete propagation cover them.
func TestRefreshBaseline_IncludesGitTrackedFiles(t *testing.T) {
	f := newIntakeFixture(t)
	if err := osexec.Command("git", "-C", f.local, "init").Run(); err != nil {
		t.Skipf("git init unavailable: %v", err)
	}
	f.writeLocal("tracked.md", "git-owned")
	f.writeMirror("tracked.md", "git-owned")
	f.writeLocal("asset.bin", "drive-owned")
	f.writeMirror("asset.bin", "drive-owned")
	if err := osexec.Command("git", "-C", f.local, "add", "tracked.md").Run(); err != nil {
		t.Skipf("git add unavailable: %v", err)
	}

	if err := RefreshBaseline(f.cfg, FingerprintStrict); err != nil {
		t.Fatal(err)
	}
	base, err := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base["tracked.md"]; !ok {
		t.Fatalf("Git-tracked file missing from baseline: %v", base)
	}
	if _, ok := base["asset.bin"]; !ok {
		t.Fatalf("untracked asset missing from baseline: %v", base)
	}
}

func TestIntake_ExcludesGitTrackedFiles(t *testing.T) {
	f := newIntakeFixture(t)
	if err := osexec.Command("git", "-C", f.local, "init").Run(); err != nil {
		t.Skipf("git init unavailable: %v", err)
	}
	f.writeLocal("tracked.md", "git-owned")
	f.writeMirror("tracked.md", "drive-copy")
	f.writeMirror("asset.bin", "drive-owned")
	if err := osexec.Command("git", "-C", f.local, "add", "tracked.md").Run(); err != nil {
		t.Skipf("git add unavailable: %v", err)
	}

	res, err := Intake(context.Background(), f.runner, f.cfg, IntakeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Intaked) != 1 || res.Intaked[0] != "asset.bin" {
		t.Fatalf("Intaked = %v, want only asset.bin", res.Intaked)
	}
}

func TestRefreshBaseline_SkipsUnreadableSubdirs(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission bits do not block root")
	}
	f := newIntakeFixture(t)
	f.writeMirror("ok/file.bin", "payload")
	f.writeLocal("ok/file.bin", "payload")
	f.writeMirror("locked/file.bin", "payload")
	f.writeLocal("locked/file.bin", "payload")
	locked := filepath.Join(f.mirror, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if err := RefreshBaseline(f.cfg, FingerprintFast); err != nil {
		t.Fatalf("RefreshBaseline should tolerate unreadable subdirs: %v", err)
	}
	base, err := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base["ok/file.bin"]; !ok {
		t.Fatalf("readable file missing from baseline: %v", base)
	}
	if _, ok := base["locked/file.bin"]; ok {
		t.Fatalf("unreadable subtree should be skipped, not indexed: %v", base)
	}
}

func TestRefreshBaseline_SSHRefusesUnreadableSubdirs(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission bits do not block root")
	}
	f := newIntakeFixture(t)
	f.cfg.Target = Target{Kind: TargetSSH, Host: "user@peer", Path: "/remote/work"}
	f.writeLocal("locked/file.bin", "payload")
	if err := SaveBaselineManifest(f.cfg.LocalPaths.BaselineFile, map[string]Fingerprint{
		"previous.bin": {Size: 1, Mtime: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(f.local, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if err := RefreshBaseline(f.cfg, FingerprintFast); err == nil {
		t.Fatal("SSH baseline must fail closed on an incomplete local inventory")
	}
	base, err := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base["previous.bin"]; !ok {
		t.Fatalf("failed refresh overwrote the prior baseline: %v", base)
	}
}
