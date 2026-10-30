package aipolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func nativeSettingsGitFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "user.name=Policy Fixture", "-c", "user.email=policy@example.invalid"}, args...)...)
	command.Dir = dir
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git fixture: %v: %s", err, output)
	}
}
func nativeSettingsRepository(t *testing.T, absorbed bool) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(base, "main")
	linked := filepath.Join(base, "elsewhere", "linked")
	if absorbed {
		meta := filepath.Join(base, "metadata", "modules", "nested")
		if err := os.MkdirAll(filepath.Dir(meta), 0700); err != nil {
			t.Fatal(err)
		}
		nativeSettingsGitFixture(t, base, "init", "-q", "--separate-git-dir", meta, main)
		relative, err := filepath.Rel(meta, main)
		if err != nil {
			t.Fatal(err)
		}
		nativeSettingsGitFixture(t, main, "config", "core.worktree", relative)
	} else {
		nativeSettingsGitFixture(t, base, "init", "-q", main)
	}
	nativeSettingsGitFixture(t, main, "commit", "--allow-empty", "-qm", "fixture")
	nativeSettingsGitFixture(t, main, "worktree", "add", "-q", "--detach", linked, "HEAD")
	return main, linked
}
func assertNativeMainSettingsBlocked(t *testing.T, main, cwd string) {
	t.Helper()
	dir := filepath.Join(main, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(dir, "settings.local.json")
	if err := os.WriteFile(local, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://unverified.invalid"},"permissions":{"ask":["mcp__vmem__vmem_create"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := projectSettingsFiles("claude", cwd)
	if err != nil || !slices.Contains(files, local) {
		t.Fatalf("missing main local: %v %v", files, err)
	}
	conflict, err := projectSubscriptionConflict("claude", cwd)
	if err != nil || !conflict {
		t.Fatalf("main billing override missed: %v %v", conflict, err)
	}
	conflicts, err := KnowledgeConflicts(Runtime{Agent: "claude", Home: t.TempDir()}, cwd, []KnowledgeApproval{{Server: "vmem", Tool: "vmem_create", ApprovalMode: "approve"}})
	if err != nil || len(conflicts) == 0 {
		t.Fatalf("main knowledge ask missed: %v %v", conflicts, err)
	}
}
func TestNativeSettingsStandaloneAndOutsideWorktree(t *testing.T) {
	main, linked := nativeSettingsRepository(t, false)
	assertNativeMainSettingsBlocked(t, main, main)
	assertNativeMainSettingsBlocked(t, main, linked)
}
func TestNativeSettingsAbsorbedSubmoduleWorktree(t *testing.T) {
	main, linked := nativeSettingsRepository(t, true)
	resolved, err := mainCheckout(linked)
	if err != nil || resolved != main {
		t.Fatalf("absorbed main: %s %v", resolved, err)
	}
	assertNativeMainSettingsBlocked(t, main, linked)
}
func TestNativeSettingsUnreachableMainFailsClosed(t *testing.T) {
	main, linked := nativeSettingsRepository(t, true)
	if err := os.Rename(main, main+"-moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := projectSettingsFiles("claude", linked); err == nil {
		t.Fatal("unreachable main treated as clean")
	}
}
func TestNativeSettingsGitEnvironmentCannotRedirect(t *testing.T) {
	main, linked := nativeSettingsRepository(t, false)
	other, _ := nativeSettingsRepository(t, false)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	resolved, err := mainCheckout(linked)
	if err != nil || resolved != main {
		t.Fatalf("ambient git redirected metadata: %s %v", resolved, err)
	}
}
func TestNativeSettingsHomeRootFallback(t *testing.T) {
	main, linked := nativeSettingsRepository(t, false)
	t.Setenv("HOME", main)
	files, err := projectSettingsFiles("claude", linked)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(files, filepath.Join(main, ".claude", "settings.local.json")) {
		t.Fatal("home-root main local incorrectly inherited")
	}
}
