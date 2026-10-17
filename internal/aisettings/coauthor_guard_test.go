package aisettings

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestPatchAgentsCoauthorInstruction(t *testing.T) {
	doc := "# AI Agents\n\n## Tool-Specific Notes\n\nKeep me.\n"
	got := patchAgentsCoauthorInstruction(doc)
	if !strings.Contains(got, coauthorGuardStart) || !strings.Contains(got, "Co-authored-by") {
		t.Fatalf("guard block missing:\n%s", got)
	}
	if !strings.Contains(got, "commit messages in English") {
		t.Fatalf("English commit policy missing:\n%s", got)
	}
	if !strings.Contains(got, "Keep me.") {
		t.Fatalf("existing section content removed:\n%s", got)
	}
	again := patchAgentsCoauthorInstruction(got)
	if strings.Count(again, coauthorGuardStart) != 1 {
		t.Fatalf("guard block duplicated:\n%s", again)
	}
}

func TestPatchGitHookConfig(t *testing.T) {
	home := t.TempDir()
	t.Run("fresh config gains the hook table", func(t *testing.T) {
		got := patchGitHookConfig("[user]\n    name = Test\n\n[core]\n    pager = less\n", home, true)
		if !strings.Contains(got, "[hook \"coauthor-guard\"]") ||
			!strings.Contains(got, "command = ~/.config/git/hooks/commit-msg") ||
			!strings.Contains(got, "event = commit-msg") {
			t.Fatalf("hook table missing:\n%s", got)
		}
		if !strings.Contains(got, "pager = less") {
			t.Fatalf("core key removed:\n%s", got)
		}
		if hookConfigDrift(got, home) != "in-sync" {
			t.Fatalf("patched config not in-sync:\n%s", got)
		}
	})
	t.Run("empty content", func(t *testing.T) {
		got := patchGitHookConfig("", home, true)
		if hookConfigDrift(got, home) != "in-sync" {
			t.Fatalf("patched empty config not in-sync:\n%s", got)
		}
	})
	t.Run("migrates a dot-managed core.hooksPath", func(t *testing.T) {
		got := patchGitHookConfig("[core]\n    hooksPath = ~/.config/git/hooks\n    pager = less\n", home, true)
		if strings.Contains(got, "hooksPath") {
			t.Fatalf("dot-managed hooksPath not removed:\n%s", got)
		}
		if !strings.Contains(got, "pager = less") {
			t.Fatalf("unrelated core key removed:\n%s", got)
		}
	})
	t.Run("keeps a non-dot core.hooksPath", func(t *testing.T) {
		in := "[core]\n    hooksPath = _meta/scripts/hooks\n"
		got := patchGitHookConfig(in, home, true)
		if !strings.Contains(got, "hooksPath = _meta/scripts/hooks") {
			t.Fatalf("non-dot hooksPath removed:\n%s", got)
		}
		if hookConfigDrift(got, home) != "in-sync" {
			t.Fatalf("hook table missing:\n%s", got)
		}
	})
	t.Run("keeps a hand-written absolute command path", func(t *testing.T) {
		in := "[hook \"coauthor-guard\"]\n    command = " + filepath.Join(home, ".config", "git", "hooks", "commit-msg") + "\n    event = commit-msg\n"
		got := patchGitHookConfig(in, home, true)
		if strings.Count(got, "[hook \"coauthor-guard\"]") != 1 {
			t.Fatalf("hook table duplicated:\n%s", got)
		}
		if !strings.Contains(got, filepath.Join(home, ".config", "git", "hooks", "commit-msg")) {
			t.Fatalf("hand-written command rewritten:\n%s", got)
		}
	})
	t.Run("fixes a wrong command", func(t *testing.T) {
		in := "[hook \"coauthor-guard\"]\n    command = /elsewhere/hook\n    event = commit-msg\n"
		got := patchGitHookConfig(in, home, true)
		if hookConfigDrift(got, home) != "in-sync" || strings.Contains(got, "/elsewhere/hook") {
			t.Fatalf("wrong command not fixed:\n%s", got)
		}
	})
	t.Run("unsupported git only migrates", func(t *testing.T) {
		got := patchGitHookConfig("[core]\n    hooksPath = ~/.config/git/hooks\n", home, false)
		if strings.Contains(got, "hooksPath") {
			t.Fatalf("dot-managed hooksPath not removed:\n%s", got)
		}
		if strings.Contains(got, "[hook") {
			t.Fatalf("hook table written for an unsupported git:\n%s", got)
		}
	})
}

func TestCoauthorGuardHookWarnAndBlock(t *testing.T) {
	dir := t.TempDir()
	msg := filepath.Join(dir, "COMMIT_EDITMSG")
	if err := os.WriteFile(msg, []byte("feat: test\n\nCo-authored-by: Bot <bot@example.com>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode string
		want int
	}{
		{CoauthorGuardWarn, 0},
		{CoauthorGuardBlock, 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			hook := filepath.Join(dir, "hook-"+tc.mode)
			if err := os.WriteFile(hook, []byte(coauthorGuardHookScript(tc.mode)), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", hook, msg)
			err := cmd.Run()
			got := 0
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					got = exit.ExitCode()
				} else {
					t.Fatalf("run hook: %v", err)
				}
			}
			if got != tc.want {
				t.Fatalf("exit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCoauthorGuardApplyLeavesNonDotHooksPathAlone(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, ".config", "git", "config"), []byte("[core]\n    hooksPath = ~/.other-hooks\n"))
	mgr := NewCoauthorGuardManager(dotexec.NewRunner(false, slog.Default()), home)
	result, err := mgr.Apply(CoauthorGuardOptions{Mode: CoauthorGuardBlock})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".config", "git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hooksPath = ~/.other-hooks") {
		t.Fatalf("non-dot hooksPath removed:\n%s", data)
	}
	if result.Status.HookConfigDrift != "in-sync" && result.Status.GitHooksSupported {
		t.Fatalf("hook config not in-sync after apply: %+v", result.Status)
	}
}

func TestPreserveForeignHooksPath(t *testing.T) {
	home := t.TempDir()
	rendered := "[core]\n    pager = less\n\n[hook \"coauthor-guard\"]\n    command = ~/.config/git/hooks/commit-msg\n    event = commit-msg\n"
	t.Run("foreign hooksPath carried into the render", func(t *testing.T) {
		got := PreserveForeignHooksPath(rendered, "[core]\n    hooksPath = _meta/scripts/hooks\n", home)
		if !strings.Contains(got, "hooksPath = _meta/scripts/hooks") || !strings.Contains(got, "pager = less") {
			t.Fatalf("foreign hooksPath not preserved:\n%s", got)
		}
	})
	t.Run("dot-managed hooksPath not preserved", func(t *testing.T) {
		got := PreserveForeignHooksPath(rendered, "[core]\n    hooksPath = ~/.config/git/hooks\n", home)
		if strings.Contains(got, "hooksPath") {
			t.Fatalf("dot-managed hooksPath survived the render:\n%s", got)
		}
	})
	t.Run("no hooksPath in existing config", func(t *testing.T) {
		if got := PreserveForeignHooksPath(rendered, "[user]\n    name = T\n", home); got != rendered {
			t.Fatalf("render changed without a foreign hooksPath:\n%s", got)
		}
	})
	t.Run("rendered config already has a hooksPath", func(t *testing.T) {
		withKey := "[core]\n    hooksPath = /already/there\n"
		if got := PreserveForeignHooksPath(withKey, "[core]\n    hooksPath = _meta/scripts/hooks\n", home); got != withKey {
			t.Fatalf("render with its own hooksPath changed:\n%s", got)
		}
	})
}

// writeHookStub plants an executable stub hook.
func writeHookStub(t *testing.T, path, script string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestCoauthorGuardIntegration exercises the AC of #173 with the real git:
// a trailer commit fails in a plain repo, in a repo with a repo-local
// core.hooksPath, and in a repo whose .git/hooks/pre-commit still runs.
func TestCoauthorGuardIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	mgr := NewCoauthorGuardManager(dotexec.NewRunner(false, slog.Default()), home)
	st, err := mgr.Status(CoauthorGuardBlock)
	if err != nil {
		t.Fatal(err)
	}
	if !st.GitHooksSupported {
		t.Skipf("git %s predates config-based hooks (2.54)", st.GitVersion)
	}
	if _, err := mgr.Apply(CoauthorGuardOptions{Mode: CoauthorGuardBlock}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Applying on top of itself must leave exactly one hook table.
	if _, err := mgr.Apply(CoauthorGuardOptions{Mode: CoauthorGuardBlock}); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	configBody, err := os.ReadFile(filepath.Join(home, ".config", "git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(configBody), "[hook \"coauthor-guard\"]") != 1 {
		t.Fatalf("duplicate hook tables:\n%s", configBody)
	}

	trailer := "feat: change\n\nCo-authored-by: Bot <bot@example.com>\n"
	clean := "feat: change\n"

	// Git resolves its global config at $XDG_CONFIG_HOME/git/config when
	// XDG_CONFIG_HOME is set (it is, on GitHub's ubuntu runners), not at
	// $HOME/.config/git/config — both must point at the sandbox home.
	gitEnv := func() []string {
		return append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	}
	gitCommit := func(t *testing.T, repo, message string) error {
		t.Helper()
		msg := filepath.Join(repo, "MSG")
		if err := os.WriteFile(msg, []byte(message), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-F", "MSG")
		cmd.Dir = repo
		cmd.Env = gitEnv()
		out, err := cmd.CombinedOutput()
		if err == nil && message != clean {
			// The trailer slipped through: dump what git sees so a CI
			// failure explains itself instead of just asserting.
			diag := exec.Command("git", "config", "--show-origin", "--get-regexp", "^hook\\.")
			diag.Dir = repo
			diag.Env = gitEnv()
			origins, _ := diag.CombinedOutput()
			ver, _ := exec.Command("git", "version").CombinedOutput()
			var envDbg []string
			for _, e := range gitEnv() {
				if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "XDG_") || strings.HasPrefix(e, "GIT_") {
					envDbg = append(envDbg, e)
				}
			}
			cfgBody, _ := os.ReadFile(filepath.Join(home, ".config", "git", "config"))
			t.Logf("commit output:\n%s\ngit: %s\nhook config:\n%s\nconfig file:\n%s\nenv: %s", out, ver, origins, cfgBody, strings.Join(envDbg, " "))
		}
		if err != nil {
			t.Logf("commit output:\n%s", out)
		}
		return err
	}
	newRepo := func(t *testing.T) string {
		t.Helper()
		repo := filepath.Join(t.TempDir(), "repo")
		cmd := exec.Command("git", "init", "-q", repo)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		return repo
	}

	t.Run("plain repo", func(t *testing.T) {
		repo := newRepo(t)
		if err := gitCommit(t, repo, trailer); err == nil {
			t.Fatal("trailer commit succeeded in a plain repo")
		}
		if err := gitCommit(t, repo, clean); err != nil {
			t.Fatalf("clean commit failed: %v", err)
		}
	})

	t.Run("repo-local core.hooksPath", func(t *testing.T) {
		repo := newRepo(t)
		hooksDir := filepath.Join(repo, "localhooks")
		marker := filepath.Join(repo, "local-commit-msg-ran")
		writeHookStub(t, filepath.Join(hooksDir, "commit-msg"),
			"#!/bin/sh\ntouch '"+marker+"'\n")
		cmd := exec.Command("git", "config", "core.hooksPath", "localhooks")
		cmd.Dir = repo
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("set repo hooksPath: %v\n%s", err, out)
		}
		if err := gitCommit(t, repo, trailer); err == nil {
			t.Fatal("trailer commit succeeded with a repo-local core.hooksPath")
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatal("repo-local commit-msg hook did not run")
		}
	})

	t.Run("dot git hooks pre-commit still runs", func(t *testing.T) {
		repo := newRepo(t)
		marker := filepath.Join(repo, "pre-commit-ran")
		writeHookStub(t, filepath.Join(repo, ".git", "hooks", "pre-commit"),
			"#!/bin/sh\ntouch '"+marker+"'\n")
		if err := gitCommit(t, repo, trailer); err == nil {
			t.Fatal("trailer commit succeeded with .git/hooks present")
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatal(".git/hooks/pre-commit did not run")
		}
		if err := gitCommit(t, repo, clean); err != nil {
			t.Fatalf("clean commit failed: %v", err)
		}
	})

	t.Run("bypass env", func(t *testing.T) {
		repo := newRepo(t)
		msg := filepath.Join(repo, "MSG")
		if err := os.WriteFile(msg, []byte(trailer), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-F", "MSG")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "HOME="+home, "DOTFILES_COAUTHOR_GUARD_ALLOW=1")
		if err := cmd.Run(); err != nil {
			t.Fatalf("bypass commit failed: %v", err)
		}
	})
}
