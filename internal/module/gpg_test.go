package module

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

func gpgTestContext(t *testing.T, signing bool, home string) *RunContext {
	t.Helper()
	return &RunContext{
		Config: &config.Config{Modules: config.ModulesConfig{
			Git: config.GitConfig{Signing: signing},
		}},
		Runner:   dotexec.NewRunner(false, slog.Default()),
		Template: template.NewEngine(),
		HomeDir:  home,
	}
}

func gpgHasChange(changes []Change, desc string) bool {
	for _, c := range changes {
		if c.Description == desc {
			return true
		}
	}
	return false
}

// seedTargetGitConfig writes signing keys into the target home's ~/.gitconfig
// — the file gitGlobalCmd resolves --global against for a non-invoking home.
func seedTargetGitConfig(t *testing.T, home string, kvs ...[2]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	gitconfig := filepath.Join(home, ".gitconfig")
	for _, kv := range kvs {
		if out, err := exec.Command("git", "config", "--file", gitconfig, kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("seed %s=%s: %v (%s)", kv[0], kv[1], err, out)
		}
	}
}

// When commit.gpgsign is already true but gpg.format is unset, Check must still
// report the pending gpg.format change (Apply would set it) — otherwise
// check/diff under-reports a change Apply performs.
func TestGPGModule_Check_ReportsMissingGPGFormat(t *testing.T) {
	home := t.TempDir()
	seedTargetGitConfig(t, home, [2]string{"commit.gpgsign", "true"})

	res, err := (&GPGModule{}).Check(context.Background(), gpgTestContext(t, true, home))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !gpgHasChange(res.Changes, "configure git gpg format") {
		t.Errorf("Check must report a pending gpg.format change; got %+v", res.Changes)
	}
	if gpgHasChange(res.Changes, "configure git commit signing") {
		t.Errorf("commit.gpgsign already true — must not report a commit signing change; got %+v", res.Changes)
	}
	if res.Satisfied {
		t.Error("Check must not be satisfied while gpg.format is unset")
	}
}

// With both signing keys already set, Check must report no signing-related
// changes (template file changes are unrelated and allowed).
func TestGPGModule_Check_NoSigningChangesWhenBothSet(t *testing.T) {
	home := t.TempDir()
	seedTargetGitConfig(t, home,
		[2]string{"commit.gpgsign", "true"},
		[2]string{"gpg.format", "openpgp"},
	)

	res, err := (&GPGModule{}).Check(context.Background(), gpgTestContext(t, true, home))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if gpgHasChange(res.Changes, "configure git gpg format") ||
		gpgHasChange(res.Changes, "configure git commit signing") {
		t.Errorf("both signing keys set — no signing changes expected; got %+v", res.Changes)
	}
}

// Signing drift for a --home target must be judged against the target home's
// gitconfig, not the invoking user's: with the invoking environment claiming
// both keys set, an empty target home must still report both changes.
func TestGPGModule_Check_HomeOverrideIgnoresInvokingGitConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	invoking := filepath.Join(t.TempDir(), "invoking-gitconfig")
	for _, kv := range [][2]string{{"commit.gpgsign", "true"}, {"gpg.format", "openpgp"}} {
		if out, err := exec.Command("git", "config", "--file", invoking, kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("seed invoking config: %v (%s)", err, out)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", invoking)

	res, err := (&GPGModule{}).Check(context.Background(), gpgTestContext(t, true, t.TempDir()))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !gpgHasChange(res.Changes, "configure git commit signing") ||
		!gpgHasChange(res.Changes, "configure git gpg format") {
		t.Errorf("empty target home must report both signing changes despite invoking-user config; got %+v", res.Changes)
	}
}
