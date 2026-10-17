package profilesnap

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func newTestEngine(t *testing.T, home string, root string) *Engine {
	t.Helper()
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return &Engine{
		Runner:     runner,
		HomeDir:    home,
		Root:       root,
		Hostname:   "testhost",
		User:       "tester",
		StatePath:  filepath.Join(home, ".config", "dotfiles", "config.yaml"),
		SecretsDir: filepath.Join(home, ".ssh"),
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestBackupAndRestoreRoundtrip(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)

	// seed state + age key
	const stateYAML = "name: Tester\nprofile: full\nmodules:\n  macapps:\n    enabled: true\n    casks: [raycast]\n    casks_extra: [maccy]\n    backup_apps: [raycast]\n"
	writeFile(t, eng.StatePath, stateYAML, 0o644)
	writeFile(t, filepath.Join(eng.SecretsDir, "age_key"), "AGE-SECRET-KEY-TEST", 0o600)
	writeFile(t, filepath.Join(eng.SecretsDir, "age_key.pub"), "age1public", 0o644)

	snap, err := eng.Backup(BackupOptions{Tag: "first", IncludeSecrets: true})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if snap.Tag != "first" {
		t.Errorf("tag: %q", snap.Tag)
	}
	if !snap.WithSecret {
		t.Errorf("WithSecret flag not preserved")
	}

	// artifacts present?
	for _, rel := range []string{"config.yaml", "meta.yaml", "apps/install.yaml", "apps/backup.yaml", "secrets/age_key"} {
		if _, err := os.Stat(filepath.Join(snap.Path, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(eng.LatestPointerPath()); err != nil {
		t.Errorf("latest.txt missing: %v", err)
	}

	// Corrupt live state + age key, then restore and verify.
	writeFile(t, eng.StatePath, "name: MUTATED\n", 0o644)
	writeFile(t, filepath.Join(eng.SecretsDir, "age_key"), "MUTATED", 0o600)

	restored, err := eng.Restore(RestoreOptions{IncludeSecrets: true, IncludeState: true})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Version != snap.Version {
		t.Errorf("restored wrong version: %q vs %q", restored.Version, snap.Version)
	}
	got, _ := os.ReadFile(eng.StatePath)
	if string(got) != stateYAML {
		t.Errorf("state not restored: %q", got)
	}
	keyGot, _ := os.ReadFile(filepath.Join(eng.SecretsDir, "age_key"))
	if string(keyGot) != "AGE-SECRET-KEY-TEST" {
		t.Errorf("age_key not restored: %q", keyGot)
	}
}

func TestListNewestFirstAndLatestMarker(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)
	writeFile(t, eng.StatePath, "name: x\n", 0o644)

	s1, err := eng.Backup(BackupOptions{Tag: "one"})
	if err != nil {
		t.Fatal(err)
	}
	// Ensure second snapshot has a distinct version id (1s resolution).
	// Force a different directory name by bumping filesystem state—use a new
	// version id manually to keep the test fast.
	v2 := s1.Version + "-2"
	dest := eng.VersionPath(v2)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dest, "meta.yaml"), "version: "+v2+"\nhostname: testhost\n", 0o644)
	writeFile(t, eng.LatestPointerPath(), v2, 0o644)

	snaps, err := eng.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots, got %d", len(snaps))
	}
	if snaps[0].Version != v2 {
		t.Errorf("newest first broken: got %q want %q", snaps[0].Version, v2)
	}
	if !snaps[0].IsLatest {
		t.Errorf("latest marker missing on %q", snaps[0].Version)
	}
	if snaps[1].IsLatest {
		t.Errorf("older snapshot should not be latest")
	}
}

func TestRestoreMissingVersion(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)
	writeFile(t, eng.StatePath, "name: x\n", 0o644)

	if _, err := eng.Restore(RestoreOptions{Version: "nope", IncludeState: true}); err == nil {
		t.Error("expected error for missing version")
	}
}

func TestRestoreDefaultsToLatestPointer(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)
	writeFile(t, eng.StatePath, "name: original\n", 0o644)

	s1, err := eng.Backup(BackupOptions{Tag: "a"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, eng.StatePath, "name: mutated\n", 0o644)

	restored, err := eng.Restore(RestoreOptions{IncludeState: true})
	if err != nil {
		t.Fatalf("restore latest: %v", err)
	}
	if restored.Version != s1.Version {
		t.Errorf("want %q got %q", s1.Version, restored.Version)
	}
}

func TestRestoreCreatesPreRestoreBackup(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)

	writeFile(t, eng.StatePath, "name: original\n", 0o644)
	writeFile(t, filepath.Join(eng.SecretsDir, "age_key"), "ORIGINAL-KEY", 0o600)

	if _, err := eng.Backup(BackupOptions{IncludeSecrets: true}); err != nil {
		t.Fatal(err)
	}

	// Mutate live copies, then restore.
	writeFile(t, eng.StatePath, "name: mutated\n", 0o644)
	writeFile(t, filepath.Join(eng.SecretsDir, "age_key"), "MUTATED-KEY", 0o600)

	snap, err := eng.Restore(RestoreOptions{IncludeSecrets: true, IncludeState: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.PreRestoreBackup == "" {
		t.Fatal("PreRestoreBackup not set despite overwriting differing files")
	}
	if !snap.RestoredState || snap.RestoredSecrets != 1 {
		t.Errorf("restore report wrong: state=%v secrets=%d", snap.RestoredState, snap.RestoredSecrets)
	}

	pre, err := os.ReadFile(filepath.Join(snap.PreRestoreBackup, "config.yaml"))
	if err != nil || string(pre) != "name: mutated\n" {
		t.Errorf("pre-restore config copy wrong: %q err=%v", pre, err)
	}
	preKey, err := os.ReadFile(filepath.Join(snap.PreRestoreBackup, "secrets", "age_key"))
	if err != nil || string(preKey) != "MUTATED-KEY" {
		t.Errorf("pre-restore key copy wrong: %q err=%v", preKey, err)
	}

	// The backup location must never leak into future snapshots: a fresh
	// IncludeSecrets backup captures only ~/.ssh/age_key*.
	snap2, err := eng.Backup(BackupOptions{IncludeSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(snap2.Path, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "age_key" {
		t.Errorf("snapshot secrets polluted: %v", entries)
	}
}

func TestRestoreIdenticalSecretSkipsBackupAndHealsPerms(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)

	writeFile(t, eng.StatePath, "name: x\n", 0o644)
	keyPath := filepath.Join(eng.SecretsDir, "age_key")
	writeFile(t, keyPath, "SAME", 0o600)
	if _, err := eng.Backup(BackupOptions{IncludeSecrets: true}); err != nil {
		t.Fatal(err)
	}

	// Drift permissions but keep content identical.
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := eng.Restore(RestoreOptions{IncludeSecrets: true, IncludeState: false})
	if err != nil {
		t.Fatal(err)
	}
	if snap.PreRestoreBackup != "" {
		t.Errorf("identical content must not trigger a pre-restore backup: %s", snap.PreRestoreBackup)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions not healed: %v", info.Mode().Perm())
	}
}

func TestRestoreForcesSecretPermissions(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)

	writeFile(t, eng.StatePath, "name: x\n", 0o644)
	writeFile(t, filepath.Join(eng.SecretsDir, "age_key"), "KEY", 0o600)
	snap, err := eng.Backup(BackupOptions{IncludeSecrets: true})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a cloud backend normalizing modes in the archive.
	if err := os.Chmod(filepath.Join(snap.Path, "secrets", "age_key"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Fresh host: no ~/.ssh at all.
	if err := os.RemoveAll(eng.SecretsDir); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Restore(RestoreOptions{IncludeSecrets: true, IncludeState: true}); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(eng.SecretsDir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("~/.ssh mode = %v, want 0700", dirInfo.Mode().Perm())
	}
	keyInfo, err := os.Stat(filepath.Join(eng.SecretsDir, "age_key"))
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Errorf("age_key mode = %v, want 0600", keyInfo.Mode().Perm())
	}
}

func TestBackupFailureLeavesNoOrphanDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based failure injection is ineffective as root")
	}
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)

	writeFile(t, eng.StatePath, "name: x\n", 0o644)
	if err := os.Chmod(eng.StatePath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(eng.StatePath, 0o644) })

	if _, err := eng.Backup(BackupOptions{}); err == nil {
		t.Fatal("expected backup to fail on unreadable state")
	}
	entries, err := os.ReadDir(eng.HostRoot())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, en := range entries {
		if en.IsDir() {
			t.Errorf("orphan version dir left behind: %s", en.Name())
		}
	}
}

func TestListAndLatestSkipDirsWithoutMeta(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)
	writeFile(t, eng.StatePath, "name: x\n", 0o644)

	snap, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Hand-made orphan that sorts newest.
	orphan := eng.VersionPath("99999999T999999Z")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}

	snaps, err := eng.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Version != snap.Version {
		t.Errorf("List must skip meta-less dirs: %+v", snaps)
	}

	// Latest fallback (pointer removed) must also skip the orphan.
	if err := os.Remove(eng.LatestPointerPath()); err != nil {
		t.Fatal(err)
	}
	latest, err := eng.ResolveLatest()
	if err != nil {
		t.Fatal(err)
	}
	if latest != snap.Version {
		t.Errorf("latest fallback picked %q, want %q", latest, snap.Version)
	}
}

func TestResolveLatestFallsBackFromDanglingPointer(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)
	writeFile(t, eng.StatePath, "name: x\n", 0o644)

	snap, err := eng.Backup(BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eng.LatestPointerPath(), []byte("missing-version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	latest, err := eng.ResolveLatest()
	if err != nil {
		t.Fatal(err)
	}
	if latest != snap.Version {
		t.Fatalf("latest fallback picked %q, want %q", latest, snap.Version)
	}
}

func TestRestoreErrorsWhenSnapshotHasNoState(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)

	// Backup with no state file produces a config-less snapshot.
	if _, err := eng.Backup(BackupOptions{}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, eng.StatePath, "name: live\n", 0o644)

	_, err := eng.Restore(RestoreOptions{IncludeState: true})
	if err == nil || !strings.Contains(err.Error(), "contains no config.yaml") {
		t.Fatalf("want missing-config error, got %v", err)
	}
	got, _ := os.ReadFile(eng.StatePath)
	if string(got) != "name: live\n" {
		t.Errorf("live state must be untouched, got %q", got)
	}
}

func TestBackupIncludeSecretsWithNoKeys(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)
	writeFile(t, eng.StatePath, "name: x\n", 0o644)

	snap, err := eng.Backup(BackupOptions{IncludeSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.WithSecret || snap.SecretsCopied != 0 {
		t.Errorf("WithSecret=%v SecretsCopied=%d, want false/0", snap.WithSecret, snap.SecretsCopied)
	}
	if _, err := os.Stat(filepath.Join(snap.Path, "secrets")); !os.IsNotExist(err) {
		t.Errorf("empty secrets/ dir must not be created: %v", err)
	}
	meta, err := readMeta(filepath.Join(snap.Path, "meta.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.IncludeSecrets {
		t.Error("meta.IncludeSecrets must be false when zero keys were copied")
	}
}

func TestDryRunRestoreWritesNothing(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)
	writeFile(t, eng.StatePath, "name: original\n", 0o644)
	writeFile(t, filepath.Join(eng.SecretsDir, "age_key"), "KEY", 0o600)
	if _, err := eng.Backup(BackupOptions{IncludeSecrets: true}); err != nil {
		t.Fatal(err)
	}

	writeFile(t, eng.StatePath, "name: mutated\n", 0o644)
	writeFile(t, filepath.Join(eng.SecretsDir, "age_key"), "MUTATED", 0o600)

	dry := newTestEngine(t, home, root)
	dry.Runner = exec.NewRunner(true, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	snap, err := dry.Restore(RestoreOptions{IncludeSecrets: true, IncludeState: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.PreRestoreBackup != "" {
		t.Errorf("dry-run must not report a pre-restore dir: %s", snap.PreRestoreBackup)
	}
	got, _ := os.ReadFile(eng.StatePath)
	if string(got) != "name: mutated\n" {
		t.Errorf("dry-run mutated state: %q", got)
	}
	key, _ := os.ReadFile(filepath.Join(eng.SecretsDir, "age_key"))
	if string(key) != "MUTATED" {
		t.Errorf("dry-run mutated key: %q", key)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "dotfiles", "backup", "profile-pre-restore")); !os.IsNotExist(err) {
		t.Errorf("dry-run created pre-restore tree: %v", err)
	}
}

func TestUniqueVersionExhaustionErrors(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	eng := newTestEngine(t, home, root)

	fixed := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	base := NewVersion(fixed)
	if err := os.MkdirAll(eng.VersionPath(base), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 2; i < 100; i++ {
		if err := os.MkdirAll(eng.VersionPath(base+"-"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eng.uniqueVersion(fixed); err == nil {
		t.Error("expected exhaustion error instead of silently reusing an id")
	}
}

func TestListHosts(t *testing.T) {
	root := t.TempDir()
	if hosts, err := ListHosts(root); err != nil || hosts != nil {
		t.Fatalf("missing tree should be (nil, nil): %v %v", hosts, err)
	}
	for _, h := range []string{"zeta", "alpha"} {
		if err := os.MkdirAll(filepath.Join(root, "profiles", h), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "profiles", "stray.txt"), "x", 0o644)
	hosts, err := ListHosts(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0] != "alpha" || hosts[1] != "zeta" {
		t.Errorf("hosts = %v", hosts)
	}
}
