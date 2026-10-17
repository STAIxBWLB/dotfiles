package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func writeAgeIdentity(t *testing.T, home, name, recipient string) {
	t.Helper()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, name), []byte("AGE-SECRET-KEY-1TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, name+".pub"), []byte(recipient+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInitHomeOverrideUsesTargetAgeKey(t *testing.T) {
	invoker := t.TempDir()
	target := t.TempDir()
	t.Setenv("HOME", invoker)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(invoker, ".config"))
	writeAgeIdentity(t, invoker, "age_key_invoker", "age1invoker")
	writeAgeIdentity(t, target, "age_key_target", "age1target")

	_, errOut, err := runDotForTest("--home", target, "--yes", "init")
	if err != nil {
		t.Fatalf("dot --home init --yes: %v\nstderr=%s", err, errOut)
	}

	state, err := config.LoadStateForHome(target)
	if err != nil {
		t.Fatalf("load target state: %v", err)
	}
	if got, want := state.Secrets.AgeIdentity, "~/.ssh/age_key_target"; got != want {
		t.Fatalf("target age identity = %q, want %q", got, want)
	}
}

func TestInitHomeOverrideDoesNotRecordInvokerAgeKey(t *testing.T) {
	invoker := t.TempDir()
	target := t.TempDir()
	t.Setenv("HOME", invoker)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(invoker, ".config"))
	writeAgeIdentity(t, invoker, "age_key_invoker", "age1invoker")

	_, errOut, err := runDotForTest("--home", target, "--yes", "init")
	if err != nil {
		t.Fatalf("dot --home init --yes: %v\nstderr=%s", err, errOut)
	}

	state, err := config.LoadStateForHome(target)
	if err != nil {
		t.Fatalf("load target state: %v", err)
	}
	if got := state.Secrets.AgeIdentity; got != "" {
		t.Fatalf("target state recorded invoking age identity %q", got)
	}
}

// TestInitDotfilesHomeUsesTargetIdentityAndState covers the environment-only
// override. Distinct age identities make a process-home fallback observable.
func TestInitDotfilesHomeUsesTargetIdentityAndState(t *testing.T) {
	invoker := t.TempDir()
	target := t.TempDir()
	t.Setenv("HOME", invoker)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(invoker, ".config"))
	t.Setenv("DOTFILES_HOME", target)
	writeAgeIdentity(t, invoker, "age_key_invoker", "age1invoker")
	writeAgeIdentity(t, target, "age_key_target", "age1target")

	out, errOut, err := runDotForTest("--yes", "init")
	if err != nil {
		t.Fatalf("DOTFILES_HOME dot init --yes: %v\nstderr=%s", err, errOut)
	}

	state, err := config.LoadStateForHome(target)
	if err != nil {
		t.Fatalf("load target state: %v", err)
	}
	if got, want := state.Secrets.AgeIdentity, "~/.ssh/age_key_target"; got != want {
		t.Fatalf("target age identity = %q, want %q", got, want)
	}
	if got, want := state.Secrets.AgeRecipients, []string{"age1target"}; !slices.Equal(got, want) {
		t.Fatalf("target age recipients = %q, want %q", got, want)
	}
	if _, err := os.Stat(config.StatePathForHome(invoker)); !os.IsNotExist(err) {
		t.Fatalf("invoker state was written, stat err=%v", err)
	}
	if want := config.StatePathForHome(target); !strings.Contains(out, want) {
		t.Fatalf("init output did not report target state path %q:\n%s", want, out)
	}
}

func TestInitHomeFlagOutranksDotfilesHome(t *testing.T) {
	invoker := t.TempDir()
	envTarget := t.TempDir()
	flagTarget := t.TempDir()
	t.Setenv("HOME", invoker)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(invoker, ".config"))
	t.Setenv("DOTFILES_HOME", envTarget)
	writeAgeIdentity(t, envTarget, "age_key_env", "age1env")
	writeAgeIdentity(t, flagTarget, "age_key_flag", "age1flag")

	_, errOut, err := runDotForTest("--home", flagTarget, "--yes", "init")
	if err != nil {
		t.Fatalf("dot --home init --yes: %v\nstderr=%s", err, errOut)
	}

	state, err := config.LoadStateForHome(flagTarget)
	if err != nil {
		t.Fatalf("load flag target state: %v", err)
	}
	if got, want := state.Secrets.AgeIdentity, "~/.ssh/age_key_flag"; got != want {
		t.Fatalf("flag target age identity = %q, want %q", got, want)
	}
	if _, err := os.Stat(config.StatePathForHome(envTarget)); !os.IsNotExist(err) {
		t.Fatalf("DOTFILES_HOME target was written despite --home, stat err=%v", err)
	}
}
