package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/secrets"
)

func TestRenderSecretsEvent_RestoreBackupOrder(t *testing.T) {
	var out, errOut bytes.Buffer
	render := renderSecretsEvent(&Printer{Out: &out, Err: &errOut})
	dest := "/very/long/path/with spaces/restored secret"
	backup := dest + ".bak-2026-08-26T12-00-00"
	render(secrets.Event{Kind: secrets.EventRestored, Path: dest})
	render(secrets.Event{Kind: secrets.EventRestoreBackup, Path: backup})
	if got, want := out.String(), "  Restored: "+dest+"\n  Backup:   "+backup+"\n"; got != want {
		t.Errorf("rendered output = %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected stderr: %q", errOut.String())
	}
}

// stubAge installs an executable "age" stub and prepends its dir to PATH.
// Args arrive as: -d -i <identity> -o <out> <src>, so $5 is the output path
// and $6 the source. In ok mode "decryption" copies the source file.
func stubAge(t *testing.T, fail bool) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\ncat \"$6\" > \"$5\"\n"
	if fail {
		script = "#!/bin/sh\nexit 1\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "age"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// stubAgeEncryptOnly installs an age stub where encrypt copies the source
// but decrypt always fails — the shape of a typo'd recipient: archives are
// produced fine and only the round-trip check can catch them.
func stubAgeEncryptOnly(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"-e\" ]; then cat \"$6\" > \"$5\"; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "age"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSecretsBackupCLI_DryRunCreatesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store := filepath.Join(home, ".local", "share", "dotfiles-secrets")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "x.age"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(home, "backup-dest")

	out, errOut, err := runDotForTest("secrets", "backup", dest, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run backup: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dry-run created the destination dir: %v", err)
	}
}

func TestSecretsInitCLI_VerificationFailureLeavesStoreClean(t *testing.T) {
	stubAgeEncryptOnly(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// State with a (typo'd) recipient; identity is a native age key so the
	// round-trip check runs without ssh-keygen passphrase probing.
	writeCLITestFile(t, filepath.Join(home, ".config", "dotfiles", "config.yaml"),
		"secrets:\n  age_recipients: [\"age1typo\"]\n  age_identity: ~/.ssh/age_key\n")
	writeCLITestFile(t, filepath.Join(home, ".ssh", "age_key"), "AGE-SECRET-KEY-1TEST")
	writeCLITestFile(t, filepath.Join(home, ".ssh", "id_ed25519"), "ssh-key-material")

	out, errOut, err := runDotForTest("secrets", "init")
	if err == nil {
		t.Fatalf("expected round-trip verification failure\nstdout=%s\nstderr=%s", out, errOut)
	}
	if !strings.Contains(err.Error(), "verification") {
		t.Errorf("error should mention verification: %v", err)
	}
	store := filepath.Join(home, ".local", "share", "dotfiles-secrets")
	entries, _ := os.ReadDir(store)
	for _, e := range entries {
		t.Errorf("store should be clean after failed verification, found %s", e.Name())
	}
}

func TestSecretsInitCLI_RoundtripVerifiedSuccess(t *testing.T) {
	stubAge(t, false) // encrypt and decrypt both "work"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	writeCLITestFile(t, filepath.Join(home, ".config", "dotfiles", "config.yaml"),
		"secrets:\n  age_recipients: [\"age1good\"]\n  age_identity: ~/.ssh/age_key\n")
	writeCLITestFile(t, filepath.Join(home, ".ssh", "age_key"), "AGE-SECRET-KEY-1TEST")
	writeCLITestFile(t, filepath.Join(home, ".ssh", "id_ed25519"), "ssh-key-material")

	out, errOut, err := runDotForTest("secrets", "init")
	if err != nil {
		t.Fatalf("init: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	archive := filepath.Join(home, ".local", "share", "dotfiles-secrets", "id_ed25519.age")
	got, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("archive missing: %v", err)
	}
	if string(got) != "ssh-key-material" {
		t.Errorf("stub-encrypted archive content = %q", got)
	}
	litter, _ := filepath.Glob(filepath.Join(home, ".local", "share", "dotfiles-secrets", ".*"))
	if len(litter) != 0 {
		t.Errorf("temp litter in store: %v", litter)
	}
}

func TestSecretsInitCLI_MissingIdentitySkipsVerification(t *testing.T) {
	stubAge(t, false)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Identity points at a path that doesn't exist yet (fresh machine):
	// init must warn, skip verification, and still encrypt.
	writeCLITestFile(t, filepath.Join(home, ".config", "dotfiles", "config.yaml"),
		"secrets:\n  age_recipients: [\"age1good\"]\n  age_identity: ~/.ssh/missing_age_key\n")
	writeCLITestFile(t, filepath.Join(home, ".ssh", "id_ed25519"), "ssh-key-material")

	out, errOut, err := runDotForTest("secrets", "init")
	if err != nil {
		t.Fatalf("init must succeed with verification skipped: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(errOut, "verification skipped") || !strings.Contains(errOut, "not found") {
		t.Errorf("missing skip warning on stderr:\nstdout=%s\nstderr=%s", out, errOut)
	}
	archive := filepath.Join(home, ".local", "share", "dotfiles-secrets", "id_ed25519.age")
	got, err := os.ReadFile(archive)
	if err != nil || string(got) != "ssh-key-material" {
		t.Errorf("archive not written despite skipped verification: %q err=%v", got, err)
	}
}

// shortHost returns this machine's short hostname, matching the wizard's
// host-scoping used by secrets backup.
func shortHost(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if i := strings.Index(h, "."); i > 0 {
		h = h[:i]
	}
	return h
}

func TestSecretsBackupCLI_DefaultDestFollowsBackupRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	root := filepath.Join(home, "Dropbox", "secrets", "dotfiles-backup")
	writeCLITestFile(t, filepath.Join(home, ".config", "dotfiles", "config.yaml"),
		"modules:\n  macapps:\n    backup_root: "+root+"\n")
	writeCLITestFile(t, filepath.Join(home, ".local", "share", "dotfiles-secrets", "x.age"), "payload")

	out, errOut, err := runDotForTest("secrets", "backup")
	if err != nil {
		t.Fatalf("secrets backup (no arg): %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	wantDest := filepath.Join(root, "secrets-age", shortHost(t))
	if !strings.Contains(out, wantDest) {
		t.Errorf("default destination not shown:\nwant %s\n%s", wantDest, out)
	}
	if _, err := os.Stat(filepath.Join(wantDest, "x.age")); err != nil {
		t.Errorf("archive not copied to default dest: %v", err)
	}
}

func TestSecretsBackupCLI_ExplicitDestUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	writeCLITestFile(t, filepath.Join(home, ".config", "dotfiles", "config.yaml"), "name: x\n")
	writeCLITestFile(t, filepath.Join(home, ".local", "share", "dotfiles-secrets", "x.age"), "payload")

	dest := filepath.Join(home, "explicit-dest")
	out, errOut, err := runDotForTest("secrets", "backup", dest)
	if err != nil {
		t.Fatalf("secrets backup (explicit): %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dest, "x.age")); err != nil {
		t.Errorf("archive not copied to explicit dest: %v", err)
	}
	// Explicit dest must NOT be wrapped in secrets-age/<host>.
	if strings.Contains(out, filepath.Join(dest, "secrets-age")) {
		t.Errorf("explicit dest should be used verbatim:\n%s", out)
	}
}

func TestSecretsBackupCLI_DefaultDestDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	root := filepath.Join(home, "Dropbox", "secrets", "dotfiles-backup")
	writeCLITestFile(t, filepath.Join(home, ".config", "dotfiles", "config.yaml"),
		"modules:\n  macapps:\n    backup_root: "+root+"\n")
	writeCLITestFile(t, filepath.Join(home, ".local", "share", "dotfiles-secrets", "x.age"), "payload")

	out, _, err := runDotForTest("secrets", "backup", "--dry-run")
	if err != nil {
		t.Fatalf("secrets backup --dry-run: %v", err)
	}
	wantDest := filepath.Join(root, "secrets-age", shortHost(t))
	if !strings.Contains(out, wantDest) {
		t.Errorf("dry-run should still show the default dest %s:\n%s", wantDest, out)
	}
	if _, err := os.Stat(filepath.Join(home, "Dropbox")); !os.IsNotExist(err) {
		t.Errorf("dry-run created the Dropbox destination tree: %v", err)
	}
}
