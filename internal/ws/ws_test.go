package ws

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

func TestValidateRelPath(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
		wantOut string
	}{
		{"empty", "", true, ""},
		{"dot", ".", true, ""},
		{"slash", "/foo", true, ""},
		{"absolute", "/etc/passwd", true, ""},
		{"traversal", "..", true, ""},
		{"traversal-prefix", "../escape", true, ""},
		{"hidden-traversal", "foo/../../bar", true, ""},
		{"simple", "foo", false, "foo"},
		{"nested", "a/b/c", false, "a/b/c"},
		{"with-dot", "./foo", false, "foo"},
		{"with-slash-end", "foo/", false, "foo"},
		{"double-slash", "a//b", false, "a/b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateRelPath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if got != tc.wantOut {
				t.Fatalf("want %q, got %q", tc.wantOut, got)
			}
		})
	}
}

func setupRoots(t *testing.T) (Roots, *exec.Runner) {
	t.Helper()
	tmp := t.TempDir()
	work := filepath.Join(tmp, "work")
	gdrive := filepath.Join(tmp, "gdrive")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gdrive, 0755); err != nil {
		t.Fatal(err)
	}
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return Roots{Work: work, Gdrive: gdrive}, runner
}

func TestMkdirBothSides(t *testing.T) {
	roots, runner := setupRoots(t)

	msgs, err := Mkdir(runner, roots, "projects/foo")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 msgs, got %d: %v", len(msgs), msgs)
	}
	wa, ga := roots.ResolvePair("projects/foo")
	if !fileutil.IsDir(wa) {
		t.Errorf("work side missing: %s", wa)
	}
	if !fileutil.IsDir(ga) {
		t.Errorf("gdrive side missing: %s", ga)
	}

	// Idempotent second call
	if _, err := Mkdir(runner, roots, "projects/foo"); err != nil {
		t.Fatalf("second mkdir should be idempotent, got: %v", err)
	}
}

func TestMkdirOnlyMissingSide(t *testing.T) {
	roots, runner := setupRoots(t)
	// Pre-create on work side only
	if err := os.MkdirAll(filepath.Join(roots.Work, "existing"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Mkdir(runner, roots, "existing"); err != nil {
		t.Fatal(err)
	}
	if !fileutil.IsDir(filepath.Join(roots.Gdrive, "existing")) {
		t.Errorf("gdrive side should have been created")
	}
}

func TestMoveBothSides(t *testing.T) {
	roots, runner := setupRoots(t)
	if _, err := Mkdir(runner, roots, "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(context.Background(), runner, roots, "old", "new"); err != nil {
		t.Fatal(err)
	}
	wa, ga := roots.ResolvePair("new")
	if !fileutil.IsDir(wa) || !fileutil.IsDir(ga) {
		t.Errorf("new not on both sides")
	}
	oldWa, oldGa := roots.ResolvePair("old")
	if fileutil.IsDir(oldWa) || fileutil.IsDir(oldGa) {
		t.Errorf("old should be gone")
	}
}

func TestMoveFailsWhenDstExists(t *testing.T) {
	roots, runner := setupRoots(t)
	if _, err := Mkdir(runner, roots, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Mkdir(runner, roots, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(context.Background(), runner, roots, "a", "b"); err == nil {
		t.Fatal("expected error when dst exists")
	}
}

func TestRemoveEmpty(t *testing.T) {
	roots, runner := setupRoots(t)
	if _, err := Mkdir(runner, roots, "empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(context.Background(), runner, roots, "empty", false); err != nil {
		t.Fatal(err)
	}
	wa, ga := roots.ResolvePair("empty")
	if fileutil.IsDir(wa) || fileutil.IsDir(ga) {
		t.Errorf("not removed")
	}
}

func TestRemoveNonEmptyFailsWithoutRecursive(t *testing.T) {
	roots, runner := setupRoots(t)
	if _, err := Mkdir(runner, roots, "full"); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Create(filepath.Join(roots.Work, "full", "x.txt"))
	_ = f.Close()
	if _, err := Remove(context.Background(), runner, roots, "full", false); err == nil {
		t.Fatal("expected error for non-empty without --recursive")
	}
}

func TestAuditDetectsDrift(t *testing.T) {
	roots, runner := setupRoots(t)
	if _, err := Mkdir(runner, roots, "both"); err != nil {
		t.Fatal(err)
	}
	// Create drift: workspace-only and gdrive-only
	if err := os.MkdirAll(filepath.Join(roots.Work, "work-only"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(roots.Gdrive, "gdrive-only"), 0755); err != nil {
		t.Fatal(err)
	}

	mismatches, err := Audit(roots, AuditOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 2 {
		t.Fatalf("want 2 mismatches, got %d: %v", len(mismatches), mismatches)
	}
	// Verify sides
	found := map[string]Side{}
	for _, m := range mismatches {
		found[m.RelPath] = m.OnlyOn
	}
	if found["work-only"] != SideWork {
		t.Errorf("work-only should be SideWork, got %v", found["work-only"])
	}
	if found["gdrive-only"] != SideGdrive {
		t.Errorf("gdrive-only should be SideGdrive, got %v", found["gdrive-only"])
	}
}

func TestAuditSkipsIgnoreDirs(t *testing.T) {
	roots, _ := setupRoots(t)
	if err := os.MkdirAll(filepath.Join(roots.Work, "node_modules/foo"), 0755); err != nil {
		t.Fatal(err)
	}
	mismatches, err := Audit(roots, AuditOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("node_modules should be ignored, got: %v", mismatches)
	}
}

func TestAuditSkipsSymlinks(t *testing.T) {
	roots, _ := setupRoots(t)
	// Symlink in work side only — should NOT be flagged as mismatch
	target := filepath.Join(roots.Gdrive, "real-target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(roots.Work, "linky")); err != nil {
		t.Fatal(err)
	}
	mismatches, err := Audit(roots, AuditOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mismatches {
		if m.RelPath == "linky" {
			t.Fatalf("symlink should not be flagged: %v", m)
		}
	}
}

func TestAuditScopeLimitsSearch(t *testing.T) {
	roots, _ := setupRoots(t)
	// Drift in two scopes
	os.MkdirAll(filepath.Join(roots.Work, "a/only"), 0755)
	os.MkdirAll(filepath.Join(roots.Work, "b/only"), 0755)
	os.MkdirAll(filepath.Join(roots.Gdrive, "a"), 0755)
	os.MkdirAll(filepath.Join(roots.Gdrive, "b"), 0755)

	mismatches, err := Audit(roots, AuditOptions{Scope: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 1 || mismatches[0].RelPath != "a/only" {
		t.Fatalf("scope should limit to a/, got: %v", mismatches)
	}
}

func TestRepoTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tests := []struct {
		name      string
		repo      string
		vaultPath string
		want      string
	}{
		{"work unaffected by vault path", "work", "/elsewhere/vault", "/ws/work"},
		{"vault default", "vault", "", "/ws/vault"},
		{"vault redirect absolute", "vault", "/custom/vault", "/custom/vault"},
		{"vault redirect tilde", "vault", "~/workspace/work/vault", filepath.Join(home, "workspace", "work", "vault")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RepoTarget("/ws", tc.repo, tc.vaultPath); got != tc.want {
				t.Fatalf("RepoTarget(%q, %q) = %q, want %q", tc.repo, tc.vaultPath, got, tc.want)
			}
		})
	}
}

// A stale vault repo entry must not re-clone when the configured vault
// location (e.g. the work submodule) is already populated.
func TestInitSkipsVaultRedirectedToPopulatedTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wsDir := filepath.Join(home, "workspace")
	vaultDir := filepath.Join(wsDir, "work", "vault")
	if err := os.MkdirAll(filepath.Join(vaultDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	msgs, err := Init(context.Background(), runner, wsDir,
		[]config.RepoConfig{{Name: "vault", Remote: "git@github.com:u/vault.git"}},
		InitOptions{Yes: true, VaultPath: "~/workspace/work/vault"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "skip "+vaultDir) {
		t.Fatalf("expected skip of populated %s, got %v", vaultDir, msgs)
	}
}

// Without the redirect the same entry would plan a duplicate clone into
// <workspace>/vault; with it the clone targets the configured vault path.
func TestInitWouldCloneVaultToRedirectedTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wsDir := filepath.Join(home, "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	vaultDir := filepath.Join(wsDir, "work", "vault")

	msgs, err := Init(context.Background(), runner, wsDir,
		[]config.RepoConfig{{Name: "vault", Remote: "git@github.com:u/vault.git"}},
		InitOptions{Yes: true, VaultPath: "~/workspace/work/vault"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	want := "would clone git@github.com:u/vault.git -> " + vaultDir
	if len(msgs) != 1 || !strings.Contains(msgs[0], want) {
		t.Fatalf("expected %q, got %v", want, msgs)
	}
}

// A nested vault target (e.g. <ws>/work/vault for a standalone vault repo
// cloned before work) needs its parent chain created before git runs.
// The bogus remote makes the clone itself fail fast and offline; the
// assertion is on the parent directory the run must leave behind.
func TestInitCreatesVaultTargetParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wsDir := filepath.Join(home, "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	vaultDir := filepath.Join(wsDir, "work", "vault")

	_, _ = Init(context.Background(), runner, wsDir,
		[]config.RepoConfig{{Name: "vault", Remote: "bogus-remote"}},
		InitOptions{Yes: true, VaultPath: "~/workspace/work/vault"})

	if !fileutil.IsDir(filepath.Dir(vaultDir)) {
		t.Fatalf("expected parent of vault target to be created: %s", filepath.Dir(vaultDir))
	}
}
