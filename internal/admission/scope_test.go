package admission

import (
	"context"
	"log/slog"
	osexec "os/exec"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestScopeFromGitCommonDir(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want string
		ok   bool
	}{
		{name: "absolute common dir", out: "/Users/u/work/repo/.git\n", want: "/Users/u/work/repo/.git", ok: true},
		{name: "relative path is a miss", out: ".git\n", want: "", ok: false},
		{name: "empty output is a miss", out: "\n", want: "", ok: false},
		{name: "whitespace only is a miss", out: "  \n", want: "", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ScopeFromGitCommonDir(tc.out)
			if ok != tc.ok || got != tc.want {
				t.Errorf("ScopeFromGitCommonDir(%q) = %q, %v; want %q, %v", tc.out, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestFallbackScope(t *testing.T) {
	a, err := FallbackScope(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := FallbackScope(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("distinct dirs must not share a fallback scope: %q", a)
	}
	same, err := FallbackScope(".")
	if err != nil {
		t.Fatal(err)
	}
	if same == "" {
		t.Error("fallback scope must not be empty")
	}
	// Deterministic for the same dir.
	other, err := FallbackScope(".")
	if err != nil {
		t.Fatal(err)
	}
	if same != other {
		t.Errorf("fallback scope not deterministic: %q vs %q", same, other)
	}
}

// TestResolveScopeSharesAcrossWorktrees is the core repo-identity property:
// a worktree and its main checkout resolve to the same scope, so one heavy
// slot covers every worktree of the repo.
func TestResolveScopeSharesAcrossWorktrees(t *testing.T) {
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	runner := exec.NewRunner(false, slog.Default())
	ctx := context.Background()
	repo := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		if _, err := runner.Run(ctx, "git", append([]string{"-C", dir}, args...)...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	git(repo, "init")
	git(repo, "config", "user.email", "test@example.com")
	git(repo, "config", "user.name", "test")
	if err := exec.NewRunner(false, slog.Default()).WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", ".")
	git(repo, "commit", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	git(repo, "worktree", "add", wt)

	mainScope, err := ResolveScope(ctx, runner, repo)
	if err != nil {
		t.Fatal(err)
	}
	wtScope, err := ResolveScope(ctx, runner, wt)
	if err != nil {
		t.Fatal(err)
	}
	if mainScope != wtScope {
		t.Errorf("worktree scope drift: main %q vs worktree %q", mainScope, wtScope)
	}

	plain := t.TempDir()
	plainScope, err := ResolveScope(ctx, runner, plain)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := FallbackScope(plain)
	if err != nil {
		t.Fatal(err)
	}
	if plainScope != fallback {
		t.Errorf("non-git dir scope = %q, want fallback %q", plainScope, fallback)
	}
}
