package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/secrets"
)

// BUG-08: every standalone `dot secrets` subcommand loaded state with the bare
// loader, resolved the home with os.UserHomeDir and asked secrets.StorePath
// for the store, so `dot --home <other> secrets ...` read, wrote and recorded
// against the INVOKING user's secrets. The one-stop backup and restore steps
// already honored the session home, which is how the same flag came to behave
// two different ways depending on the entry point.
//
// The fixture is shared with the BUG-07 rows (status_home_test.go): the target
// home carries the SSH key and two archives, the invoking home carries the
// shell secrets file and five archives, so every assertion below discriminates
// in both directions.

// addAgeRecipient appends a recipient to a seeded home's state file. `secrets
// init` refuses before any path lookup without one, so which home the
// recipients come from is itself a home-resolution assertion.
func addAgeRecipient(t *testing.T, home, recipient string) {
	t.Helper()
	path := config.StatePathForHome(home)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeCLITestFile(t, path, string(data)+"secrets:\n  age_recipients:\n    - "+recipient+"\n")
}

func targetStore(home string) string { return filepath.Join(home, secrets.StoreDirRel) }

// stubAgeOnUsablePATH installs the age stub over a PATH that still carries the
// system utilities. The shared fixture empties PATH so module probes answer the
// same everywhere, but the stub is a /bin/sh script that calls cat, so it exits
// 127 on an empty PATH. Real age stays out of reach either way: the stub dir is
// first and no Homebrew prefix is on this PATH.
func stubAgeOnUsablePATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", "/usr/bin:/bin")
	stubAge(t, false)
}

func TestSecretsStatusHonorsHomeFlag(t *testing.T) {
	invoker, target := newStatusHomeFixture(t)

	out, errOut, err := runDotForTest("--home", target, "secrets", "status")
	if err != nil {
		t.Fatalf("secrets status --home: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, filepath.Join(target, ".ssh", "id_ed25519")) {
		t.Errorf("secrets status --home reported an age identity outside the target home:\n%s", out)
	}
	if strings.Contains(out, invoker) {
		t.Errorf("secrets status --home reported paths under the invoking user's home:\n%s", out)
	}
	assertKV(t, out, "SSH key", "present")       // seeded in the target only
	assertKV(t, out, "90-secrets.sh", "missing") // the target has no shell secrets

	// Non-vacuity: without the flag the same document describes the process home.
	out, errOut, err = runDotForTest("secrets", "status")
	if err != nil {
		t.Fatalf("secrets status: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, filepath.Join(invoker, ".ssh", "id_ed25519")) {
		t.Errorf("secrets status without the flag left the process home:\n%s", out)
	}
	if strings.Contains(out, target) {
		t.Errorf("secrets status without the flag reported an unrelated home:\n%s", out)
	}
	assertKV(t, out, "SSH key", "missing")
}

func TestSecretsListHonorsHomeFlag(t *testing.T) {
	invoker, target := newStatusHomeFixture(t)

	out, errOut, err := runDotForTest("--home", target, "secrets", "list")
	if err != nil {
		t.Fatalf("secrets list --home: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, targetStore(target)) {
		t.Errorf("secrets list --home listed a store outside the target home:\n%s", out)
	}
	if strings.Contains(out, invoker) {
		t.Errorf("secrets list --home listed the invoking user's store:\n%s", out)
	}
	if strings.Count(out, ".age") != 2 {
		t.Errorf("secrets list --home did not list the target store's two archives:\n%s", out)
	}

	// A target with no store says so rather than falling back to the real one.
	empty := t.TempDir()
	out, errOut, err = runDotForTest("--home", empty, "secrets", "list")
	if err != nil {
		t.Fatalf("secrets list --home <empty>: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "No secrets store found") {
		t.Errorf("secrets list --home <empty> did not report the missing store:\n%s", out)
	}

	// Non-vacuity.
	out, errOut, err = runDotForTest("secrets", "list")
	if err != nil {
		t.Fatalf("secrets list: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, targetStore(invoker)) {
		t.Errorf("secrets list without the flag left the process home:\n%s", out)
	}
}

func TestSecretsInitHonorsHomeFlag(t *testing.T) {
	invoker, target := newStatusHomeFixture(t)
	addAgeRecipient(t, target, "age1targetrecipient")
	stubAgeOnUsablePATH(t)

	out, errOut, err := runDotForTest("--home", target, "secrets", "init")
	if err != nil {
		t.Fatalf("secrets init --home: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(targetStore(target), "id_ed25519.age")); err != nil {
		t.Errorf("secrets init --home did not encrypt into the target store: %v\nstdout=%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(targetStore(invoker), "id_ed25519.age")); !os.IsNotExist(err) {
		t.Errorf("secrets init --home wrote into the invoking user's store: %v", err)
	}

	// Non-vacuity AND error precedence: only the target home configured
	// recipients, so the same command without the flag must refuse on the
	// invoking home's state — before any path lookup.
	_, errOut, err = runDotForTest("secrets", "init")
	if err == nil {
		t.Fatalf("secrets init without the flag used the target home's recipients\nstderr=%s", errOut)
	}
	if !strings.Contains(err.Error(), "no age recipients configured") {
		t.Errorf("secrets init reported %v, want the no-recipients refusal", err)
	}
}

func TestSecretsRestoreHonorsHomeFlag(t *testing.T) {
	invoker, target := newStatusHomeFixture(t)
	stubAgeOnUsablePATH(t)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "id_ed25519.age"), []byte("restored-payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runDotForTest("--home", target, "--yes", "secrets", "restore", src)
	if err != nil {
		t.Fatalf("secrets restore --home: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	got, err := os.ReadFile(filepath.Join(target, ".ssh", "id_ed25519"))
	if err != nil {
		t.Fatalf("reading the target home's restored key: %v", err)
	}
	if string(got) != "restored-payload" {
		t.Errorf("the target home's key was not restored: %q", string(got))
	}
	if _, err := os.Stat(filepath.Join(invoker, ".ssh", "id_ed25519")); !os.IsNotExist(err) {
		t.Errorf("secrets restore --home wrote plaintext into the invoking user's home: %v", err)
	}
}

func TestSecretsBackupHonorsHomeFlag(t *testing.T) {
	invoker, target := newStatusHomeFixture(t)
	dest := filepath.Join(t.TempDir(), "dest")

	out, errOut, err := runDotForTest("--home", target, "secrets", "backup", dest)
	if err != nil {
		t.Fatalf("secrets backup --home: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "2 file(s)") {
		t.Errorf("secrets backup --home did not read the target home's store:\n%s", out)
	}

	// The last-backup record belongs in the target home's state file, not the
	// invoking user's: a backup run under the flag otherwise rewrites state
	// the operator never pointed the command at.
	targetState, err := config.LoadStateForHome(target)
	if err != nil {
		t.Fatal(err)
	}
	if targetState.Secrets.LastBackup == nil || targetState.Secrets.LastBackup.Files != 2 {
		t.Errorf("the target home's state has no 2-file last-backup record: %+v", targetState.Secrets.LastBackup)
	}
	invokerState, err := config.LoadStateForHome(invoker)
	if err != nil {
		t.Fatal(err)
	}
	if invokerState.Secrets.LastBackup != nil {
		t.Errorf("secrets backup --home recorded into the invoking user's state: %+v", invokerState.Secrets.LastBackup)
	}

	// Non-vacuity: without the flag both halves stay on the process home.
	out, errOut, err = runDotForTest("secrets", "backup", filepath.Join(t.TempDir(), "dest2"))
	if err != nil {
		t.Fatalf("secrets backup: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "5 file(s)") {
		t.Errorf("secrets backup without the flag did not read the process home's store:\n%s", out)
	}
	invokerState, err = config.LoadStateForHome(invoker)
	if err != nil {
		t.Fatal(err)
	}
	if invokerState.Secrets.LastBackup == nil || invokerState.Secrets.LastBackup.Files != 5 {
		t.Errorf("the process home's state has no 5-file last-backup record: %+v", invokerState.Secrets.LastBackup)
	}
}

// TestSecretsBackupWithUnreadableState is the recovery case: an explicit
// destination needs no state at all, so a malformed config.yaml must not stop
// the archives from being copied out. Resolving the session's state up front
// turned the one situation where preserving the .age files matters most into a
// hard refusal.
func TestSecretsBackupWithUnreadableState(t *testing.T) {
	_, target := newStatusHomeFixture(t)
	writeCLITestFile(t, config.StatePathForHome(target), "name: [unterminated\n")
	dest := filepath.Join(t.TempDir(), "dest")

	out, errOut, err := runDotForTest("--home", target, "secrets", "backup", dest)
	if err != nil {
		t.Fatalf("backup to an explicit destination refused on unreadable state: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	for _, name := range []string{"f0.age", "f1.age"} {
		if _, statErr := os.Stat(filepath.Join(dest, name)); statErr != nil {
			t.Errorf("%s was not copied out: %v", name, statErr)
		}
	}
	if !strings.Contains(errOut, "could not load state") {
		t.Errorf("the unreadable state was not reported as a warning:\nstderr=%s", errOut)
	}

	// Non-vacuity: with NO destination the state is what names one, so the
	// same broken file must still refuse rather than invent a path.
	_, _, err = runDotForTest("--home", target, "secrets", "backup")
	if err == nil {
		t.Fatal("backup with no destination resolved one without readable state")
	}
}
