package secrets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func pinBackupClock(t *testing.T, instant time.Time) {
	t.Helper()
	previous := backupNow
	backupNow = func() time.Time { return instant }
	t.Cleanup(func() { backupNow = previous })
}

// stubAge installs an executable "age" stub and prepends its dir to PATH.
// Args arrive as: -d -i <identity> -o <out> <src>, so $5 is the output path
// and $6 the source. In ok mode "decryption" copies the source file.
//
// Duplicated from the command layer rather than shared: the fixture is small
// and deliberately boring, and a shared testutil package would add a
// dependency edge this milestone does not want (D-15).
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

type restoreFixture struct {
	runner   *exec.Runner
	identity string
	srcAge   string
	dest     string
}

// newRestoreFixture seeds an identity file and a fake .age source whose
// stub-decrypted content is plaintext.
func newRestoreFixture(t *testing.T, plaintext string) *restoreFixture {
	t.Helper()
	dir := t.TempDir()
	identity := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(identity, []byte("identity"), 0600); err != nil {
		t.Fatal(err)
	}
	srcAge := filepath.Join(dir, "secret.age")
	if err := os.WriteFile(srcAge, []byte(plaintext), 0600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return &restoreFixture{
		runner:   exec.NewRunner(false, logger),
		identity: identity,
		srcAge:   srcAge,
		dest:     filepath.Join(dir, "restored", "secret"),
	}
}

func confirmAlways(bool) func(string) (bool, error) {
	return func(string) (bool, error) { return true, nil }
}

func confirmNever() func(string) (bool, error) {
	return func(string) (bool, error) { return false, nil }
}

func confirmFatal(t *testing.T) func(string) (bool, error) {
	return func(string) (bool, error) {
		t.Fatal("confirm must not be called")
		return false, nil
	}
}

func globBackups(t *testing.T, dest string) []string {
	t.Helper()
	matches, err := filepath.Glob(dest + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func globTempLitter(t *testing.T, dest string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".restore-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestRestoreFile_NewDest(t *testing.T) {
	stubAge(t, false)
	f := newRestoreFixture(t, "secret-key-bytes")

	status, backup, err := restoreFile(context.Background(), f.runner,
		f.identity, f.srcAge, f.dest, 0700, confirmFatal(t))
	if err != nil {
		t.Fatalf("restoreSecretFile: %v", err)
	}
	if status != restoreWritten {
		t.Errorf("status = %d, want restoreWritten", status)
	}
	if backup != "" {
		t.Errorf("backup = %q, want empty for new dest", backup)
	}

	data, err := os.ReadFile(f.dest)
	if err != nil {
		t.Fatalf("reading restored file: %v", err)
	}
	if string(data) != "secret-key-bytes" {
		t.Errorf("content = %q", string(data))
	}
	info, _ := os.Stat(f.dest)
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if got := globBackups(t, f.dest); len(got) != 0 {
		t.Errorf("unexpected backups: %v", got)
	}
	if got := globTempLitter(t, f.dest); len(got) != 0 {
		t.Errorf("temp litter left behind: %v", got)
	}
}

func TestRestoreFile_BackupOnOverwrite(t *testing.T) {
	stubAge(t, false)
	f := newRestoreFixture(t, "new-content")
	if err := os.MkdirAll(filepath.Dir(f.dest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dest, []byte("old-content"), 0600); err != nil {
		t.Fatal(err)
	}

	status, backup, err := restoreFile(context.Background(), f.runner,
		f.identity, f.srcAge, f.dest, 0700, confirmAlways(true))
	if err != nil {
		t.Fatalf("restoreSecretFile: %v", err)
	}
	if status != restoreWritten {
		t.Errorf("status = %d, want restoreWritten", status)
	}

	data, _ := os.ReadFile(f.dest)
	if string(data) != "new-content" {
		t.Errorf("dest = %q, want new content", string(data))
	}
	backups := globBackups(t, f.dest)
	if len(backups) != 1 || backups[0] != backup {
		t.Fatalf("backups = %v, returned %q", backups, backup)
	}
	old, _ := os.ReadFile(backup)
	if string(old) != "old-content" {
		t.Errorf("backup content = %q, want old content", string(old))
	}
	info, _ := os.Stat(backup)
	if info.Mode().Perm() != 0600 {
		t.Errorf("backup mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestRestoreFile_SameSecondBackups(t *testing.T) {
	stubAge(t, false)
	pinBackupClock(t, time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC))
	f := newRestoreFixture(t, "new-content-a")
	if err := os.MkdirAll(filepath.Dir(f.dest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dest, []byte("old-content-a"), 0600); err != nil {
		t.Fatal(err)
	}
	_, first, err := restoreFile(context.Background(), f.runner, f.identity, f.srcAge, f.dest, 0700, confirmAlways(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.srcAge, []byte("new-content-b"), 0600); err != nil {
		t.Fatal(err)
	}
	_, second, err := restoreFile(context.Background(), f.runner, f.identity, f.srcAge, f.dest, 0700, confirmAlways(true))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("same-second backups collapsed to %q", first)
	}
	for path, want := range map[string]string{first: "old-content-a", second: "new-content-a"} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Errorf("backup %s = %q, %v; want %q", path, got, readErr, want)
		}
	}
}

func TestRestoreFile_ConcurrentBackupReservations(t *testing.T) {
	pinBackupClock(t, time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC))
	dest := filepath.Join(t.TempDir(), "secret")
	const workers = 8
	start := make(chan struct{})
	paths := make(chan string, workers)
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for i := range workers {
		go func() {
			payload := []byte(fmt.Sprintf("backup-%d", i))
			ready.Done()
			<-start
			path, err := writeRestoreBackupFile(dest, payload)
			if err != nil {
				errs <- err
				return
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(payload) {
				errs <- fmt.Errorf("backup %s = %q, %v", path, got, err)
				return
			}
			paths <- path
		}()
	}
	ready.Wait()
	close(start)
	seen := make(map[string]bool, workers)
	for range workers {
		select {
		case err := <-errs:
			t.Fatal(err)
		case path := <-paths:
			if seen[path] {
				t.Fatalf("duplicate reserved path %s", path)
			}
			seen[path] = true
		}
	}
}

func TestRestoreFile_BackupFailureLeavesDestIntact(t *testing.T) {
	stubAge(t, false)
	f := newRestoreFixture(t, "new-content")
	if err := os.MkdirAll(filepath.Dir(f.dest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dest, []byte("old-content"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := writeRestoreBackup
	writeRestoreBackup = func(*os.File, []byte) (int, error) { return 0, errors.New("forced backup write failure") }
	t.Cleanup(func() { writeRestoreBackup = previous })

	_, backup, err := restoreFile(context.Background(), f.runner, f.identity, f.srcAge, f.dest, 0700, confirmAlways(true))
	if err == nil || backup != "" {
		t.Fatalf("restore error=%v backup=%q, want failure without backup path", err, backup)
	}
	if got, readErr := os.ReadFile(f.dest); readErr != nil || string(got) != "old-content" {
		t.Errorf("destination = %q, %v; want untouched old content", got, readErr)
	}
	if backups := globBackups(t, f.dest); len(backups) != 0 {
		t.Errorf("failed owned backup left behind: %v", backups)
	}
}

func TestRestoreFile_DeclinedOverwrite(t *testing.T) {
	stubAge(t, false)
	f := newRestoreFixture(t, "new-content")
	if err := os.MkdirAll(filepath.Dir(f.dest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dest, []byte("old-content"), 0600); err != nil {
		t.Fatal(err)
	}

	status, backup, err := restoreFile(context.Background(), f.runner,
		f.identity, f.srcAge, f.dest, 0700, confirmNever())
	if err != nil {
		t.Fatalf("restoreSecretFile: %v", err)
	}
	if status != restoreSkipped {
		t.Errorf("status = %d, want restoreSkipped", status)
	}
	if backup != "" {
		t.Errorf("backup = %q, want empty", backup)
	}
	data, _ := os.ReadFile(f.dest)
	if string(data) != "old-content" {
		t.Errorf("dest changed despite declined overwrite: %q", string(data))
	}
}

func TestRestoreFile_FailedDecryptLeavesDestIntact(t *testing.T) {
	stubAge(t, true)
	f := newRestoreFixture(t, "new-content")
	if err := os.MkdirAll(filepath.Dir(f.dest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dest, []byte("old-content"), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := restoreFile(context.Background(), f.runner,
		f.identity, f.srcAge, f.dest, 0700, confirmFatal(t))
	if err == nil {
		t.Fatal("expected error from failed decrypt")
	}
	if !strings.Contains(err.Error(), "untouched") {
		t.Errorf("error should state dest was untouched: %v", err)
	}
	data, _ := os.ReadFile(f.dest)
	if string(data) != "old-content" {
		t.Errorf("dest corrupted by failed decrypt: %q", string(data))
	}
	if got := globBackups(t, f.dest); len(got) != 0 {
		t.Errorf("unexpected backups: %v", got)
	}
	if got := globTempLitter(t, f.dest); len(got) != 0 {
		t.Errorf("temp litter left behind: %v", got)
	}
}

func TestRestoreFile_UnchangedSkipsBackup(t *testing.T) {
	stubAge(t, false)
	f := newRestoreFixture(t, "same-content")
	if err := os.MkdirAll(filepath.Dir(f.dest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dest, []byte("same-content"), 0600); err != nil {
		t.Fatal(err)
	}

	status, backup, err := restoreFile(context.Background(), f.runner,
		f.identity, f.srcAge, f.dest, 0700, confirmFatal(t))
	if err != nil {
		t.Fatalf("restoreSecretFile: %v", err)
	}
	if status != restoreUnchanged {
		t.Errorf("status = %d, want restoreUnchanged", status)
	}
	if backup != "" || len(globBackups(t, f.dest)) != 0 {
		t.Error("identical content must not create a backup")
	}
}

func TestRestoreFile_EmptyPlaintextRestores(t *testing.T) {
	// A genuinely empty secret (e.g. an empty 90-secrets.sh that was
	// backed up) must restore faithfully — real decrypt failures exit
	// non-zero and are caught before this point.
	stubAge(t, false)
	f := newRestoreFixture(t, "") // stub copies the empty source → empty output

	status, _, err := restoreFile(context.Background(), f.runner,
		f.identity, f.srcAge, f.dest, 0700, confirmFatal(t))
	if err != nil {
		t.Fatalf("restoreSecretFile: %v", err)
	}
	if status != restoreWritten {
		t.Errorf("status = %d, want restoreWritten", status)
	}
	info, err := os.Stat(f.dest)
	if err != nil {
		t.Fatalf("empty secret should be restored: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("size = %d, want 0", info.Size())
	}
}

func TestRestoreFile_UnchangedHealsPermissions(t *testing.T) {
	stubAge(t, false)
	f := newRestoreFixture(t, "same-content")
	if err := os.MkdirAll(filepath.Dir(f.dest), 0700); err != nil {
		t.Fatal(err)
	}
	// Identical content, but permissions drifted to group/world-readable.
	if err := os.WriteFile(f.dest, []byte("same-content"), 0644); err != nil {
		t.Fatal(err)
	}

	status, _, err := restoreFile(context.Background(), f.runner,
		f.identity, f.srcAge, f.dest, 0700, confirmFatal(t))
	if err != nil {
		t.Fatalf("restoreSecretFile: %v", err)
	}
	if status != restoreUnchanged {
		t.Errorf("status = %d, want restoreUnchanged", status)
	}
	info, _ := os.Stat(f.dest)
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode = %v, want drifted permissions healed to 0600", info.Mode().Perm())
	}
}

func TestRestoreFile_DryRun(t *testing.T) {
	f := newRestoreFixture(t, "content")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	dryRunner := exec.NewRunner(true, logger)

	status, _, err := restoreFile(context.Background(), dryRunner,
		f.identity, f.srcAge, f.dest, 0700, confirmFatal(t))
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if status != restoreWritten {
		t.Errorf("status = %d, want restoreWritten", status)
	}
	if _, statErr := os.Stat(f.dest); !os.IsNotExist(statErr) {
		t.Error("dry-run must not touch the filesystem")
	}
}

func TestRestoreFile_MissingIdentity(t *testing.T) {
	stubAge(t, false)
	f := newRestoreFixture(t, "content")
	missing := f.identity + ".missing"

	_, _, err := restoreFile(context.Background(), f.runner,
		missing, f.srcAge, f.dest, 0700, confirmFatal(t))
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v, want missing-identity error naming the path", err)
	}
}

func TestBackupTimestamp_FilesystemSafe(t *testing.T) {
	if ts := backupTimestamp(); strings.ContainsRune(ts, ':') {
		t.Errorf("timestamp %q contains ':'", ts)
	}
}

func TestEncryptFile_FailureLeavesDestUntouched(t *testing.T) {
	stubAge(t, true) // every age call fails
	dir := t.TempDir()
	src := filepath.Join(dir, "plain")
	dest := filepath.Join(dir, "store", "plain.age")
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("previous-good-archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := exec.NewRunner(false, logger)

	err := encryptFile(context.Background(), runner, []string{"-r", "age1x"}, src, dest, nil)
	if err == nil {
		t.Fatal("expected encryption failure")
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "previous-good-archive" {
		t.Errorf("previous archive corrupted: %q", got)
	}
	litter, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".*enc-*"))
	if len(litter) != 0 {
		t.Errorf("temp litter left behind: %v", litter)
	}
}

func TestEncryptFile_DryRunTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plain")
	dest := filepath.Join(dir, "store", "plain.age")
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("previous-good-archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	dryRunner := exec.NewRunner(true, logger)

	if err := encryptFile(context.Background(), dryRunner, []string{"-r", "age1x"}, src, dest, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "previous-good-archive" {
		t.Errorf("dry-run replaced the archive: %q", got)
	}
}

func TestCopyArchive_TightensPermissions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.age")
	dst := filepath.Join(dir, "out", "a.age")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := copyArchive(exec.NewRunner(false, logger), src, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("backup copy mode = %v, want 0600", info.Mode().Perm())
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "payload" {
		t.Errorf("content = %q", got)
	}
}

func TestRestore_ReportsUnmatchedArchives(t *testing.T) {
	stubAge(t, false)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	for _, name := range []string{"id_ed25519.age", "90-secrets.sh.age", "id_rsa.age"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("payload-"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := exec.NewRunner(false, logger)

	result, err := Restore(context.Background(), RestoreOptions{
		Runner:  runner,
		State:   &config.UserState{},
		Home:    home,
		Src:     src,
		Confirm: func(ConfirmKind, string) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	// id_ed25519.age restores onto the identical identity file content?
	// No — stub writes "payload-id_ed25519.age", differing → overwrite (unattended=true).
	if result.Restored != 2 {
		t.Errorf("Restored = %d, want 2", result.Restored)
	}
	if len(result.Unmatched) != 1 || result.Unmatched[0] != "id_rsa.age" {
		t.Errorf("Unmatched = %v, want [id_rsa.age]", result.Unmatched)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "shell", "90-secrets.sh")); err != nil {
		t.Errorf("shell secrets not restored: %v", err)
	}
}

// stubSSHKeygen installs an ssh-keygen stub that exits with the given code,
// simulating a passphrase-protected (exit 1) or unprotected (exit 0) key.
func stubSSHKeygen(t *testing.T, exitCode int) {
	t.Helper()
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)
	if err := os.WriteFile(filepath.Join(bin, "ssh-keygen"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestVerifier_PassphraseProtectedIdentitySkips(t *testing.T) {
	stubSSHKeygen(t, 1) // ssh-keygen -y -P "" fails → passphrase-protected
	dir := t.TempDir()
	identity := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(identity, []byte("-----BEGIN OPENSSH PRIVATE KEY-----"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := exec.NewRunner(false, logger)

	verify, reason := verifier(context.Background(), runner, identity)
	if verify != nil || !strings.Contains(reason, "passphrase-protected") {
		t.Errorf("verify=%v reason=%q, want skip with passphrase reason", verify != nil, reason)
	}
}

func TestVerifier_UnprotectedSSHIdentityVerifies(t *testing.T) {
	stubSSHKeygen(t, 0)
	dir := t.TempDir()
	identity := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(identity, []byte("-----BEGIN OPENSSH PRIVATE KEY-----"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := exec.NewRunner(false, logger)

	verify, reason := verifier(context.Background(), runner, identity)
	if verify == nil || reason != "" {
		t.Errorf("verify=%v reason=%q, want a usable verifier", verify != nil, reason)
	}
}

func TestSSHKeyNameRejectsPathSeparators(t *testing.T) {
	for _, bad := range []string{"../evil", "a/b", "..", "."} {
		state := &config.UserState{}
		state.SSH.KeyName = bad
		if _, err := sshKeyName(state); err == nil {
			t.Errorf("sshKeyName(%q) should fail", bad)
		}
	}
	state := &config.UserState{}
	state.SSH.KeyName = "id_rsa"
	if name, err := sshKeyName(state); err != nil || name != "id_rsa" {
		t.Errorf("sshKeyName(id_rsa) = %q, %v", name, err)
	}
}

// The Beszel agent env file carries the hub KEY/TOKEN, so it is a
// secrets-managed archive ↔ plaintext pair like the SSH key and shell
// secrets — `dot secrets` init/backup/restore cover it, and watchdog setup
// only ever probes its presence.
func TestEntries_IncludesBeszelAgentEnv(t *testing.T) {
	home := t.TempDir()
	entries, err := Entries(&config.UserState{}, home)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	var found *Entry
	for i := range entries {
		if entries[i].Label == "Beszel agent env" {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("no beszel entry in %#v", entries)
	}
	if found.AgeName != "beszel-agent.env.age" {
		t.Errorf("beszel archive name = %q", found.AgeName)
	}
	if want := filepath.Join(home, ".config", "beszel", "agent.env"); found.Plain != want {
		t.Errorf("beszel plaintext = %q, want %q", found.Plain, want)
	}
	if found.DirPerm != 0o700 {
		t.Errorf("beszel plaintext dir perm = %v, want 0700 (it carries the hub KEY/TOKEN)", found.DirPerm)
	}
}

// The Telegram bot env file carries TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID, so
// it is a secrets-managed archive ↔ plaintext pair like the Beszel agent env.
func TestEntries_IncludesTelegramBotEnv(t *testing.T) {
	home := t.TempDir()
	entries, err := Entries(&config.UserState{}, home)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	var found *Entry
	for i := range entries {
		if entries[i].Label == "Telegram bot env" {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("no telegram entry in %#v", entries)
	}
	if found.AgeName != "telegram.env.age" {
		t.Errorf("telegram archive name = %q", found.AgeName)
	}
	if want := filepath.Join(home, ".config", "dot", "telegram.env"); found.Plain != want {
		t.Errorf("telegram plaintext = %q, want %q", found.Plain, want)
	}
	if found.DirPerm != 0o700 {
		t.Errorf("telegram plaintext dir perm = %v, want 0700 (it carries the bot token)", found.DirPerm)
	}
}
