package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func pinNoTerminal(t *testing.T) {
	t.Helper()
	old := ui.TerminalAttached
	ui.TerminalAttached = func() bool { return false }
	t.Cleanup(func() { ui.TerminalAttached = old })
	for _, env := range []string{"DOTFILES_YES", "DOTFILES_PROFILE", "DOTFILES_NAME", "DOTFILES_EMAIL"} {
		t.Setenv(env, "")
	}
}

// #183 AC1: no TTY and a complete stored config runs the dry run with the
// stored values instead of opening a prompt.
func TestApply_NoTerminalDryRunUsesStoredConfig(t *testing.T) {
	pinNoTerminal(t)
	home := t.TempDir()
	if err := config.SaveStateForHome(home, &config.UserState{Name: "Stored User", Email: "stored@example.invalid", Profile: "minimal"}); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runDotForTest("--home", home, "apply", "--module", "git", "--dry-run")
	if err != nil {
		t.Fatalf("apply: %v\nstdout:\n%s\nstderr:\n%s", err, out, errOut)
	}
	if strings.Contains(out, "=== Configuration ===") {
		t.Fatalf("a prompt section ran without a terminal:\n%s", out)
	}
}

// #183 AC2: a missing required value exits non-zero with the way out, not a
// bubbletea error; so does a real apply that needs its confirmation.
func TestApply_NoTerminalMissingValueOrConfirmationFails(t *testing.T) {
	pinNoTerminal(t)
	for _, tc := range []struct {
		name  string
		state *config.UserState
		args  []string
		want  string
	}{
		{"missing identity", &config.UserState{Profile: "minimal"}, []string{"--dry-run"}, "the name and email (set DOTFILES_NAME and DOTFILES_EMAIL"},
		{"missing profile", &config.UserState{Name: "N", Email: "n@example.invalid"}, []string{"--dry-run"}, "the profile (pass --profile"},
		{"real apply needs confirmation", &config.UserState{Name: "N", Email: "n@example.invalid", Profile: "minimal"}, nil, "no terminal to confirm the apply; pass --yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := config.SaveStateForHome(home, tc.state); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"--home", home, "apply", "--module", "git"}, tc.args...)
			_, _, err := runDotForTest(args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "bubbletea") {
				t.Fatalf("prompt still ran: %v", err)
			}
		})
	}
}

// Every prompt refuses without a terminal instead of reaching bubbletea,
// so init and reconfigure fail with the same clear message.
func TestPrompts_NoTerminalReturnClearError(t *testing.T) {
	pinNoTerminal(t)
	if _, err := ui.Input("Full name", "x", false); !errors.Is(err, ui.ErrNoTerminal) || !strings.Contains(err.Error(), "Full name") {
		t.Fatalf("Input err = %v", err)
	}
	if _, err := ui.Confirm("Apply?", false); !errors.Is(err, ui.ErrNoTerminal) {
		t.Fatalf("Confirm err = %v", err)
	}
	if v, err := ui.Input("Full name", "stored", true); err != nil || v != "stored" {
		t.Fatalf("unattended Input = %q, %v", v, err)
	}
}

// The identity overrides count as supplied values without a terminal.
func TestApply_NoTerminalIdentityFromEnvironment(t *testing.T) {
	pinNoTerminal(t)
	t.Setenv("DOTFILES_NAME", "Env User")
	t.Setenv("DOTFILES_EMAIL", "env@example.invalid")
	home := t.TempDir()
	if err := config.SaveStateForHome(home, &config.UserState{Profile: "minimal"}); err != nil {
		t.Fatal(err)
	}
	if out, errOut, err := runDotForTest("--home", home, "apply", "--module", "git", "--dry-run"); err != nil {
		t.Fatalf("apply: %v\nstdout:\n%s\nstderr:\n%s", err, out, errOut)
	}
}

// The error names the prompt without the styling escapes.
func TestPrompts_NoTerminalErrorIsPlainText(t *testing.T) {
	pinNoTerminal(t)
	_, err := ui.InputWithDetected("Full name", "x", true, false)
	if err == nil || strings.Contains(err.Error(), "\x1b") || !strings.Contains(err.Error(), "Full name (auto-detected)") {
		t.Fatalf("err = %q", err)
	}
}
