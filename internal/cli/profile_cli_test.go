package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// profileCLIFixture sandboxes HOME/XDG and seeds a state file so the
// profile commands resolve everything inside temp dirs.
func newProfileCLIFixture(t *testing.T) (home, root string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	root = t.TempDir()
	writeCLITestFile(t, filepath.Join(home, ".config", "dotfiles", "config.yaml"), "name: original\n")
	return home, root
}

func TestProfileCLIBackupRestoreRoundtrip(t *testing.T) {
	home, root := newProfileCLIFixture(t)
	statePath := filepath.Join(home, ".config", "dotfiles", "config.yaml")
	keyPath := filepath.Join(home, ".ssh", "age_key")
	writeCLITestFile(t, keyPath, "ORIGINAL-KEY")
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runDotForTest("profile", "backup", "--to", root, "--tag", "cli-test", "--include-age-key")
	if err != nil {
		t.Fatalf("backup: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "snapshot created") || !strings.Contains(out, "included (1 file(s))") {
		t.Errorf("backup output unexpected:\n%s", out)
	}

	writeCLITestFile(t, statePath, "name: mutated\n")
	writeCLITestFile(t, keyPath, "MUTATED-KEY")

	out, errOut, err = runDotForTest("profile", "restore", "--from", root, "--include-age-key", "--yes")
	if err != nil {
		t.Fatalf("restore: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "restore complete") {
		t.Errorf("restore output unexpected:\n%s", out)
	}
	if !strings.Contains(out, "Previous:") {
		t.Errorf("restore output missing pre-restore backup path:\n%s", out)
	}
	got, _ := os.ReadFile(statePath)
	if string(got) != "name: original\n" {
		t.Errorf("state not restored: %q", got)
	}
	key, _ := os.ReadFile(keyPath)
	if string(key) != "ORIGINAL-KEY" {
		t.Errorf("age_key not restored: %q", key)
	}
}

func TestProfileCLIBackupWarnsWhenNoSecretsFound(t *testing.T) {
	_, root := newProfileCLIFixture(t)

	out, errOut, err := runDotForTest("profile", "backup", "--to", root, "--include-age-key")
	if err != nil {
		t.Fatalf("backup: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	combined := out + errOut
	if !strings.Contains(combined, "no age_key") {
		t.Errorf("expected warning about missing age keys:\n%s", combined)
	}
}

func TestProfileCLIRestoreRejectsDeadLatestFlag(t *testing.T) {
	_, root := newProfileCLIFixture(t)
	if _, _, err := runDotForTest("profile", "backup", "--to", root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runDotForTest("profile", "restore", "--from", root, "--latest", "--yes"); err == nil || !strings.Contains(err.Error(), "unknown flag: --latest") {
		t.Errorf("removed --latest flag should be rejected with unknown-flag error, got: %v", err)
	}
}

func TestProfileCLICrossHostRestore(t *testing.T) {
	home, root := newProfileCLIFixture(t)
	statePath := filepath.Join(home, ".config", "dotfiles", "config.yaml")

	// Simulate a snapshot taken on another machine by backing up and then
	// renaming the host directory.
	if _, _, err := runDotForTest("profile", "backup", "--to", root, "--tag", "other"); err != nil {
		t.Fatal(err)
	}
	profiles := filepath.Join(root, "profiles")
	entries, err := os.ReadDir(profiles)
	if err != nil || len(entries) != 1 {
		t.Fatalf("profiles dir: %v %v", entries, err)
	}
	if err := os.Rename(filepath.Join(profiles, entries[0].Name()), filepath.Join(profiles, "otherhost")); err != nil {
		t.Fatal(err)
	}

	writeCLITestFile(t, statePath, "name: mutated\n")
	out, errOut, err := runDotForTest("profile", "restore", "--from", root, "--host", "otherhost", "--yes")
	if err != nil {
		t.Fatalf("cross-host restore: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	got, _ := os.ReadFile(statePath)
	if string(got) != "name: original\n" {
		t.Errorf("state not restored cross-host: %q", got)
	}
}
