//go:build darwin || linux

package fileutil

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"golang.org/x/sys/unix"
)

func pinBackupClock(t *testing.T, instant time.Time) {
	t.Helper()
	previous := backupNow
	backupNow = func() time.Time { return instant }
	t.Cleanup(func() { backupNow = previous })
}

// BUG-15: backup() resolved its root from the process environment, so a run
// pointed at another home deposited that home's overwritten files into the
// invoking user's own tree. These rows pin the root to the home the CALLER
// gave, which is the only home a sandboxed run has any business writing to.
//
// The fixture points the process home at a second temp directory before it
// writes anything. That is load-bearing rather than tidy: a version that left
// the process home alone would deposit its own backups in the operator's home
// while asserting that backups no longer go to the operator's home.

// writeHelper is one of the two entry points that back a file up before
// overwriting it. Both call the same backup(), and a fix applied to one is
// not evidence about the other, so every row below runs twice.
type writeHelper struct {
	name string
	fn   func(runner *exec.Runner, home, path string, content []byte, perm os.FileMode) (bool, error)
}

var writeHelpers = []writeHelper{
	{"EnsureFile", EnsureFile},
	{"EnsureFileAtomic", EnsureFileAtomic},
}

// twoHomes returns an invoking home (what the process is pointed at) and a
// target home (what the caller passes), each distinct, so every row can
// assert presence in one AND absence in the other.
func twoHomes(t *testing.T) (invoking, target string) {
	t.Helper()
	invoking = t.TempDir()
	target = t.TempDir()
	t.Setenv("HOME", invoking)
	return invoking, target
}

// runnerWithLog builds a real (non-dry-run) runner whose warnings are
// capturable, since a failed backup is reported as a warning rather than
// returned to the helper's caller.
func runnerWithLog() (*exec.Runner, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return exec.NewRunner(false, logger), &buf
}

// backupNames lists what sits in home's backup directory. It names the EXACT
// directory rather than searching under home: a root derived from the target
// path's own parent would land the copy beside the file it copied, which is
// still "under the target home" and must fail these rows.
func backupNames(t *testing.T, home string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(home, backupDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading backup dir under %s: %v", home, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func backupRoot(home string) string {
	return filepath.Join(home, backupDir)
}

func onlyBackup(t *testing.T, home string) string {
	t.Helper()
	names := backupNames(t, home)
	if len(names) != 1 {
		t.Fatalf("backups = %v, want exactly one recovery copy", names)
	}
	return filepath.Join(backupRoot(home), names[0])
}

func assertOwnerOnlyMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %04o, want %04o", path, got, want)
	}
	if got := info.Mode().Perm() & 0o077; got != 0 {
		t.Errorf("%s exposes group/world bits %04o", path, got)
	}
}

func multiplyLinked(t *testing.T, path string) bool {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return st.Nlink > 1
}

func assertBackupTreeOwnerOnly(t *testing.T, home string) {
	t.Helper()
	root := backupRoot(home)
	assertOwnerOnlyMode(t, root, 0o700)
	assertBackupEntriesOwnerOnly(t, root, true)
}

// assertBackupEntriesOwnerOnly walks a backup subtree. Directories must be
// 0700 everywhere. A file directly in the root is one of backup()'s own
// reservations and must be exactly 0600; below the root the snapshot keeps its
// owner bits, so only group/world access must be gone.
func assertBackupEntriesOwnerOnly(t *testing.T, dir string, root bool) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read backup dir %s: %v", dir, err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("lstat %s: %v", path, err)
		}
		switch {
		case info.IsDir():
			assertOwnerOnlyMode(t, path, 0o700)
			assertBackupEntriesOwnerOnly(t, path, false)
		case info.Mode().IsRegular() && root:
			assertOwnerOnlyMode(t, path, 0o600)
		case info.Mode().IsRegular():
			if multiplyLinked(t, path) {
				// Skipped by design: the inode may be reachable outside the
				// tree, and the 0700 ancestors already gate the subtree.
				continue
			}
			if got := info.Mode().Perm() & 0o077; got != 0 {
				t.Errorf("%s exposes group/world bits %04o", path, got)
			}
		case info.Mode()&os.ModeSymlink != 0:
			// A snapshot payload symlink, skipped by design. The root refuses
			// them outright; TestWriteHelpers_RejectsUnsafeBackupEntry covers that.
		default:
			t.Errorf("backup entry %s mode = %s, want regular file or directory", path, info.Mode())
		}
	}
}

// assertNoBackupDir fails when home received a dotfiles backup tree at all.
func assertNoBackupDir(t *testing.T, label, home string) {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", "dotfiles")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("%s home received %s (stat err: %v)", label, dir, err)
	}
}

// snapshotExternalTarget is deliberately small and exact: the attack tests
// need to prove that an outside target kept its directory mode, entries and
// bytes, not merely that the expected recovery file is absent.
func snapshotExternalTarget(t *testing.T, root string) string {
	t.Helper()
	rootInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatalf("lstat external target %s: %v", root, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read external target %s: %v", root, err)
	}
	var snapshot strings.Builder
	snapshot.WriteString(rootInfo.Mode().String())
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("lstat external entry %s: %v", path, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read external entry %s: %v", path, err)
		}
		snapshot.WriteString("\n")
		snapshot.WriteString(entry.Name())
		snapshot.WriteString("\t")
		snapshot.WriteString(info.Mode().String())
		snapshot.WriteString("\t")
		snapshot.Write(data)
	}
	return snapshot.String()
}

// assertOnlyFiles walks root and fails on any file outside want. It catches
// the near-miss the exact-directory assertion is aimed at: a backup joined
// against the written file's parent lands somewhere under the target home
// but not where the layout says.
func assertOnlyFiles(t *testing.T, root string, want ...string) {
	t.Helper()
	allowed := map[string]bool{}
	for _, w := range want {
		allowed[w] = true
	}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || allowed[p] {
			return nil
		}
		t.Errorf("unexpected file under %s: %s", root, p)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}

func TestWriteHelpers_BackupLandsUnderTheCallersHome(t *testing.T) {
	const before = "the bytes that were there before the overwrite\n"

	for _, h := range writeHelpers {
		t.Run(h.name, func(t *testing.T) {
			invoking, target := twoHomes(t)
			path := filepath.Join(target, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
				t.Fatal(err)
			}

			runner, log := runnerWithLog()
			written, err := h.fn(runner, target, path, []byte("after the overwrite\n"), 0o644)
			if err != nil {
				t.Fatalf("%s: %v", h.name, err)
			}
			if !written {
				t.Fatalf("%s reported no write", h.name)
			}
			if strings.Contains(log.String(), "backup failed") {
				t.Errorf("backup warned: %s", log.String())
			}

			// Presence, at the exact directory the layout names.
			names := backupNames(t, target)
			if len(names) != 1 || !strings.HasPrefix(names[0], "settings.json.") {
				t.Fatalf("%s/%s holds %v, want exactly one settings.json.<timestamp>", target, backupDir, names)
			}
			copied := filepath.Join(target, backupDir, names[0])
			got, err := os.ReadFile(copied)
			if err != nil {
				t.Fatal(err)
			}
			// The copy must hold the PRE-overwrite bytes, so a backup of the
			// wrong content fails rather than passing on existence alone.
			if string(got) != before {
				t.Errorf("backup content = %q, want %q", got, before)
			}

			// Absence, in the home the process happens to be pointed at.
			assertNoBackupDir(t, "invoking", invoking)
			// And nothing anywhere else under the target either.
			assertOnlyFiles(t, target, path, copied)
		})
	}
}

func TestEnsureFileAtomic_BackupPermissionsOwnerOnly(t *testing.T) {
	_, home := twoHomes(t)
	path := filepath.Join(home, ".codex", "config.toml")
	const before = "private hud configuration\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	runner, log := runnerWithLog()
	written, err := EnsureFileAtomic(runner, home, path, []byte("updated hud configuration\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if !written {
		t.Fatal("EnsureFileAtomic reported no write")
	}
	if strings.Contains(log.String(), "backup failed") {
		t.Errorf("backup warned: %s", log.String())
	}
	if got, err := os.ReadFile(onlyBackup(t, home)); err != nil || string(got) != before {
		t.Errorf("recovery bytes = %q, err = %v, want %q", got, err, before)
	}
	assertOwnerOnlyMode(t, backupRoot(home), 0o700)
	assertOwnerOnlyMode(t, onlyBackup(t, home), 0o600)
	assertOwnerOnlyMode(t, path, 0o600)
}

func TestWriteHelpers_BackupPermissionsOwnerOnly(t *testing.T) {
	for _, h := range writeHelpers {
		for _, mode := range []os.FileMode{0o600, 0o644, 0o755} {
			t.Run(h.name+"/"+mode.String(), func(t *testing.T) {
				_, home := twoHomes(t)
				path := filepath.Join(home, ".config", "settings.toml")
				const before = "private setting before overwrite\n"
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(before), mode); err != nil {
					t.Fatal(err)
				}

				runner, _ := runnerWithLog()
				written, err := h.fn(runner, home, path, []byte("replacement\n"), mode)
				if err != nil || !written {
					t.Fatalf("written=%t err=%v", written, err)
				}
				copy := onlyBackup(t, home)
				if got, err := os.ReadFile(copy); err != nil || string(got) != before {
					t.Errorf("recovery bytes = %q, err = %v, want %q", got, err, before)
				}
				assertOwnerOnlyMode(t, backupRoot(home), 0o700)
				assertOwnerOnlyMode(t, copy, 0o600)
			})
		}
	}
}

func TestWriteHelpers_HardensExistingBackupPermissions(t *testing.T) {
	for _, h := range writeHelpers {
		t.Run(h.name, func(t *testing.T) {
			_, home := twoHomes(t)
			root := backupRoot(home)
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			legacy := map[string]string{
				"settings.toml.legacy-one": "first preserved backup",
				"settings.toml.legacy-two": "second preserved backup",
			}
			for name, contents := range legacy {
				if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(home, ".config", "settings.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("live before"), 0o644); err != nil {
				t.Fatal(err)
			}

			runner, _ := runnerWithLog()
			written, err := h.fn(runner, home, path, []byte("live after"), 0o644)
			if err != nil || !written {
				t.Fatalf("written=%t err=%v", written, err)
			}
			for name, want := range legacy {
				got, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(got) != want {
					t.Errorf("legacy %s = %q, err = %v, want %q", name, got, err, want)
				}
			}
			assertBackupTreeOwnerOnly(t, home)

			if _, err := h.fn(runner, home, path, []byte("live after again"), 0o644); err != nil {
				t.Fatal(err)
			}
			assertBackupTreeOwnerOnly(t, home)
		})
	}
}

// dot's own features (agents, app-settings, profile restores) nest their
// snapshots under the same backup root, so a subdirectory there must be
// hardened and descended into rather than refused.
func TestWriteHelpers_HardensNestedBackupSubtree(t *testing.T) {
	for _, h := range writeHelpers {
		t.Run(h.name, func(t *testing.T) {
			_, home := twoHomes(t)
			root := backupRoot(home)
			nested := filepath.Join(root, "agents", "20260831T054202Z")
			if err := os.MkdirAll(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(nested, "claude")
			if err := os.WriteFile(snapshot, []byte("agent snapshot"), 0o644); err != nil {
				t.Fatal(err)
			}
			// A preserved hook: restore has to put it back runnable, so the
			// owner's execute bit must survive hardening.
			hook := filepath.Join(nested, "hooks", "session-start.sh")
			if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			// App snapshots (Raycast, for one) carry symlinks; they must be
			// skipped, never followed, and never refused. A hard link reaching
			// an inode outside the tree must not be chmod'd either.
			outsideDir := t.TempDir()
			outside := filepath.Join(outsideDir, "outside-secret")
			if err := os.WriteFile(outside, []byte("outside bytes"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(nested, "linked")); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(outside, filepath.Join(nested, "hard-linked")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, ".config", "settings.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("live before"), 0o644); err != nil {
				t.Fatal(err)
			}

			runner, _ := runnerWithLog()
			written, err := h.fn(runner, home, path, []byte("live after"), 0o644)
			if err != nil || !written {
				t.Fatalf("written=%t err=%v, want the backup to succeed beside a nested subtree", written, err)
			}
			if got, _ := os.ReadFile(snapshot); string(got) != "agent snapshot" {
				t.Errorf("nested snapshot = %q, want preserved", got)
			}
			if info, err := os.Lstat(outside); err != nil || info.Mode().Perm() != 0o640 {
				t.Errorf("outside inode mode = %v (err %v), want unchanged 0640", info, err)
			}
			if info, err := os.Lstat(filepath.Join(nested, "hard-linked")); err != nil || info.Mode().Perm() != 0o640 {
				t.Errorf("hard link mode = %v (err %v), want left alone at 0640", info, err)
			}
			if info, err := os.Lstat(hook); err != nil || info.Mode().Perm() != 0o700 {
				t.Errorf("preserved hook mode = %v (err %v), want 0700", info, err)
			}
			if info, err := os.Lstat(snapshot); err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("preserved snapshot mode = %v (err %v), want 0600", info, err)
			}
			assertBackupTreeOwnerOnly(t, home)
		})
	}
}

func TestWriteHelpers_RejectsUnsafeBackupEntry(t *testing.T) {
	for _, h := range writeHelpers {
		for _, kind := range []string{"symlink", "fifo"} {
			t.Run(h.name+"/"+kind, func(t *testing.T) {
				_, home := twoHomes(t)
				root := backupRoot(home)
				if err := os.MkdirAll(root, 0o700); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "outside-secret")
				if err := os.WriteFile(outside, []byte("outside bytes"), 0o640); err != nil {
					t.Fatal(err)
				}
				unsafe := filepath.Join(root, "unexpected")
				if kind == "symlink" {
					if err := os.Symlink(outside, unsafe); err != nil {
						t.Fatal(err)
					}
				} else if err := unix.Mkfifo(unsafe, 0o600); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(home, ".config", "settings.toml")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("live before"), 0o600); err != nil {
					t.Fatal(err)
				}
				outsideInfo, err := os.Lstat(outside)
				if err != nil {
					t.Fatal(err)
				}

				runner, _ := runnerWithLog()
				written, err := h.fn(runner, home, path, []byte("live after"), 0o600)
				if err == nil || written {
					t.Fatalf("written=%t err=%v, want unsafe backup entry refusal", written, err)
				}
				if got, _ := os.ReadFile(path); string(got) != "live before" {
					t.Errorf("live target = %q, want unchanged", got)
				}
				if got, _ := os.ReadFile(outside); string(got) != "outside bytes" {
					t.Errorf("outside target = %q, want unchanged", got)
				}
				if after, err := os.Lstat(outside); err != nil || after.Mode().Perm() != outsideInfo.Mode().Perm() {
					t.Errorf("outside mode changed from %04o to %v, err=%v", outsideInfo.Mode().Perm(), after, err)
				}
				if names := backupNames(t, home); len(names) != 1 || names[0] != "unexpected" {
					t.Errorf("backup entries = %v, want only unsafe fixture", names)
				}
			})
		}
	}
}

func TestWriteHelpers_BackupPermissionHardeningFailure(t *testing.T) {
	for _, h := range writeHelpers {
		t.Run(h.name, func(t *testing.T) {
			_, home := twoHomes(t)
			root := backupRoot(home)
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			legacy := filepath.Join(root, "settings.toml.legacy")
			if err := os.WriteFile(legacy, []byte("prior recovery bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, ".config", "settings.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("live before"), 0o600); err != nil {
				t.Fatal(err)
			}
			runner, _ := runnerWithLog()
			previous := chmodBackupFD
			chmodBackupFD = func(fd int, path string, mode os.FileMode) error {
				if path == root {
					return errors.New("forced backup hardening failure")
				}
				return previous(fd, path, mode)
			}
			t.Cleanup(func() { chmodBackupFD = previous })

			written, err := h.fn(runner, home, path, []byte("live after"), 0o600)
			if err == nil || written {
				t.Fatalf("written=%t err=%v, want hardening failure", written, err)
			}
			if got, _ := os.ReadFile(path); string(got) != "live before" {
				t.Errorf("live target = %q, want unchanged", got)
			}
			if got, _ := os.ReadFile(legacy); string(got) != "prior recovery bytes" {
				t.Errorf("legacy backup = %q, want unchanged", got)
			}
			if names := backupNames(t, home); len(names) != 1 || names[0] != filepath.Base(legacy) {
				t.Errorf("backup entries = %v, want only legacy backup", names)
			}
		})
	}
}

func TestWriteHelpers_RejectSymlinkedBackupAncestor(t *testing.T) {
	for _, h := range writeHelpers {
		t.Run(h.name, func(t *testing.T) {
			_, home := twoHomes(t)
			external := t.TempDir()
			if err := os.Chmod(external, 0o751); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(external, "sentinel"), []byte("outside bytes"), 0o640); err != nil {
				t.Fatal(err)
			}
			before := snapshotExternalTarget(t, external)
			if err := os.Symlink(external, filepath.Join(home, ".local")); err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(home, ".config", "settings.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("live before"), 0o600); err != nil {
				t.Fatal(err)
			}

			runner, _ := runnerWithLog()
			written, err := h.fn(runner, home, path, []byte("live after"), 0o600)
			if err == nil || written {
				t.Fatalf("written=%t err=%v, want symlinked ancestor refusal", written, err)
			}
			if got, _ := os.ReadFile(path); string(got) != "live before" {
				t.Errorf("live target = %q, want unchanged", got)
			}
			if after := snapshotExternalTarget(t, external); after != before {
				t.Errorf("external target changed:\nbefore=%s\nafter=%s", before, after)
			}
			if info, err := os.Lstat(filepath.Join(home, ".local")); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("backup ancestor = %v, err=%v, want unchanged symlink", info, err)
			}
		})
	}
}

func TestWriteHelpers_RejectFinalBackupDirectorySwapBeforeHardening(t *testing.T) {
	for _, h := range writeHelpers {
		t.Run(h.name, func(t *testing.T) {
			_, home := twoHomes(t)
			root := backupRoot(home)
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			external := t.TempDir()
			if err := os.Chmod(external, 0o751); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(external, "sentinel"), []byte("outside bytes"), 0o640); err != nil {
				t.Fatal(err)
			}
			before := snapshotExternalTarget(t, external)
			path := filepath.Join(home, ".config", "settings.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("live before"), 0o600); err != nil {
				t.Fatal(err)
			}

			previous := beforeBackupDirectoryHarden
			beforeBackupDirectoryHarden = func(backupPath string) error {
				if backupPath != root {
					return errors.New("unexpected backup path for swap seam")
				}
				held := root + "-held"
				if err := os.Rename(root, held); err != nil {
					return err
				}
				return os.Symlink(external, root)
			}
			t.Cleanup(func() { beforeBackupDirectoryHarden = previous })

			runner, _ := runnerWithLog()
			written, err := h.fn(runner, home, path, []byte("live after"), 0o600)
			if err == nil || written {
				t.Fatalf("written=%t err=%v, want swapped backup directory refusal", written, err)
			}
			if got, _ := os.ReadFile(path); string(got) != "live before" {
				t.Errorf("live target = %q, want unchanged", got)
			}
			if after := snapshotExternalTarget(t, external); after != before {
				t.Errorf("external target changed:\nbefore=%s\nafter=%s", before, after)
			}
			heldEntries, err := os.ReadDir(root + "-held")
			if err != nil || len(heldEntries) != 0 {
				t.Errorf("held backup directory entries = %v, err=%v, want no reservation", heldEntries, err)
			}
		})
	}
}

func TestWriteHelpers_DryRunDoesNotHardenOrCreateBackup(t *testing.T) {
	for _, h := range writeHelpers {
		for _, legacy := range []bool{false, true} {
			t.Run(h.name+"/legacy="+strconv.FormatBool(legacy), func(t *testing.T) {
				_, home := twoHomes(t)
				root := backupRoot(home)
				if legacy {
					if err := os.MkdirAll(root, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, "legacy"), []byte("legacy bytes"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(home, ".config", "settings.toml")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("live before"), 0o600); err != nil {
					t.Fatal(err)
				}

				runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(io.Discard, nil)))
				written, err := h.fn(runner, home, path, []byte("live after"), 0o600)
				if err != nil || !written {
					t.Fatalf("written=%t err=%v", written, err)
				}
				if got, _ := os.ReadFile(path); string(got) != "live before" {
					t.Errorf("dry-run live target = %q, want unchanged", got)
				}
				if !legacy {
					if _, err := os.Lstat(root); !os.IsNotExist(err) {
						t.Errorf("dry-run created backup root (err=%v)", err)
					}
					return
				}
				rootInfo, err := os.Lstat(root)
				if err != nil || rootInfo.Mode().Perm() != 0o755 {
					t.Errorf("dry-run backup root mode = %v, err=%v, want 0755", rootInfo, err)
				}
				legacyPath := filepath.Join(root, "legacy")
				info, err := os.Lstat(legacyPath)
				if err != nil || info.Mode().Perm() != 0o644 {
					t.Errorf("dry-run legacy mode = %v, err=%v, want 0644", info, err)
				}
				if got, _ := os.ReadFile(legacyPath); string(got) != "legacy bytes" {
					t.Errorf("dry-run legacy bytes = %q, want unchanged", got)
				}
				if names := backupNames(t, home); len(names) != 1 || names[0] != "legacy" {
					t.Errorf("dry-run backup entries = %v, want only legacy", names)
				}
			})
		}
	}
}

func TestWriteHelpers_NoBackupWhenNothingIsOverwritten(t *testing.T) {
	for _, h := range writeHelpers {
		t.Run(h.name, func(t *testing.T) {
			t.Run("new file", func(t *testing.T) {
				invoking, target := twoHomes(t)
				path := filepath.Join(target, ".config", "fresh.toml")

				runner, _ := runnerWithLog()
				if _, err := h.fn(runner, target, path, []byte("v1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if names := backupNames(t, target); len(names) != 0 {
					t.Errorf("a first write made backups %v", names)
				}
				assertNoBackupDir(t, "invoking", invoking)
				assertNoBackupDir(t, "target", target)
			})

			t.Run("identical content", func(t *testing.T) {
				invoking, target := twoHomes(t)
				path := filepath.Join(target, ".config", "same.toml")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
					t.Fatal(err)
				}

				runner, _ := runnerWithLog()
				written, err := h.fn(runner, target, path, []byte("v1\n"), 0o644)
				if err != nil {
					t.Fatal(err)
				}
				if written {
					t.Error("a hash-identical write reported a write")
				}
				assertNoBackupDir(t, "invoking", invoking)
				assertNoBackupDir(t, "target", target)
			})
		})
	}
}

func TestWriteHelpers_EmptyHome(t *testing.T) {
	// An empty home would join the backup path relative to the process
	// working directory, writing into whatever tree the operator was
	// standing in. Refusing it names the caller's mistake instead.
	for _, h := range writeHelpers {
		t.Run(h.name, func(t *testing.T) {
			invoking, target := twoHomes(t)
			path := filepath.Join(target, ".config", "cfg.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			runner, _ := runnerWithLog()
			written, err := h.fn(runner, "", path, []byte("v2\n"), 0o644)
			if err == nil || written {
				t.Fatalf("%s wrote=%t err=%v, want backup refusal", h.name, written, err)
			}
			if got, _ := os.ReadFile(path); string(got) != "v1\n" {
				t.Errorf("content = %q, want unchanged v1", got)
			}

			assertNoBackupDir(t, "invoking", invoking)
			assertNoBackupDir(t, "target", target)
			// Nor into the process working directory, which is where a
			// relative join would have landed.
			if _, err := os.Stat(filepath.Join(".local", "share", "dotfiles")); !os.IsNotExist(err) {
				t.Errorf("backup landed relative to the working directory (stat err: %v)", err)
			}
		})
	}
}

func TestWriteHelpers_SameSecondBackups(t *testing.T) {
	pinBackupClock(t, time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC))
	for _, h := range writeHelpers {
		t.Run(h.name, func(t *testing.T) {
			_, home := twoHomes(t)
			path := filepath.Join(home, ".config", "cfg.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("a"), 0o644); err != nil {
				t.Fatal(err)
			}
			runner, _ := runnerWithLog()
			if _, err := h.fn(runner, home, path, []byte("b"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := h.fn(runner, home, path, []byte("c"), 0o644); err != nil {
				t.Fatal(err)
			}
			names := backupNames(t, home)
			if len(names) != 2 {
				t.Fatalf("backups = %v, want two same-second recovery copies", names)
			}
			contents := map[string]bool{}
			for _, name := range names {
				data, err := os.ReadFile(filepath.Join(home, backupDir, name))
				if err != nil {
					t.Fatal(err)
				}
				contents[string(data)] = true
			}
			if !contents["a"] || !contents["b"] {
				t.Errorf("recovery contents = %v, want a and b", contents)
			}
			assertBackupTreeOwnerOnly(t, home)
		})
	}
}

func TestWriteHelpers_ConcurrentBackupReservations(t *testing.T) {
	pinBackupClock(t, time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC))
	home := t.TempDir()
	dir, err := openBackupDirectory(home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	const workers = 8
	start := make(chan struct{})
	paths := make(chan string, workers)
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for i := range workers {
		go func() {
			payload := []byte("backup-" + string(rune('a'+i)))
			ready.Done()
			<-start
			path, err := writeBackupCopy(dir, "cfg.toml", payload)
			if err != nil {
				errs <- err
				return
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(payload) {
				errs <- errors.New("backup contents were replaced")
				return
			}
			paths <- path
		}()
	}
	ready.Wait()
	close(start)
	seen := map[string]bool{}
	for range workers {
		select {
		case err := <-errs:
			t.Fatal(err)
		case path := <-paths:
			if seen[path] {
				t.Fatalf("duplicate backup path %s", path)
			}
			seen[path] = true
		}
	}
	assertBackupTreeOwnerOnly(t, home)
}

func TestWriteHelpers_BackupFailure(t *testing.T) {
	for _, h := range writeHelpers {
		t.Run(h.name, func(t *testing.T) {
			_, home := twoHomes(t)
			path := filepath.Join(home, ".config", "cfg.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
				t.Fatal(err)
			}
			previous := writeReservedBackup
			writeReservedBackup = func(*os.File, []byte) (int, error) { return 0, errors.New("forced backup write failure") }
			t.Cleanup(func() { writeReservedBackup = previous })

			runner, _ := runnerWithLog()
			written, err := h.fn(runner, home, path, []byte("after"), 0o644)
			if err == nil || written {
				t.Fatalf("written=%t err=%v, want backup failure", written, err)
			}
			if got, _ := os.ReadFile(path); string(got) != "before" {
				t.Errorf("live target = %q, want before", got)
			}
			if names := backupNames(t, home); len(names) != 0 {
				t.Errorf("owned partial backups left behind: %v", names)
			}
			assertOwnerOnlyMode(t, backupRoot(home), 0o700)
		})
	}
}
