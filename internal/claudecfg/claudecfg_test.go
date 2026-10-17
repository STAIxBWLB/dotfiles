package claudecfg

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func testRunner() *exec.Runner {
	return exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMutate_AppliesEditAndWrites(t *testing.T) {
	home := t.TempDir()
	writeFile(t, SettingsPath(home), "{\n  \"existing\": true\n}\n")

	changed, err := Mutate(testRunner(), home, func(settings map[string]any) error {
		settings["added"] = "value"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("inserting a key should report changed")
	}
	settings, err := Read(home)
	if err != nil {
		t.Fatal(err)
	}
	if settings["added"] != "value" {
		t.Fatalf("added key missing: %#v", settings)
	}
	if settings["existing"] != true {
		t.Fatalf("pre-existing key lost: %#v", settings)
	}
}

func TestMutate_ReleasesLockOnClosureError(t *testing.T) {
	home := t.TempDir()
	writeFile(t, SettingsPath(home), "{}\n")
	closureErr := errors.New("edit failed")

	changed, err := Mutate(testRunner(), home, func(map[string]any) error {
		return closureErr
	})
	if !errors.Is(err, closureErr) {
		t.Fatalf("err = %v, want closure error", err)
	}
	if changed {
		t.Fatal("a failing closure must not report changed")
	}
	data, readErr := os.ReadFile(SettingsPath(home))
	if readErr != nil || string(data) != "{}\n" {
		t.Fatalf("file must be untouched on closure error: %q, %v", data, readErr)
	}
	if _, statErr := os.Stat(LockDir(home)); !os.IsNotExist(statErr) {
		t.Fatalf("lock directory must be released on closure error: %v", statErr)
	}
	// A second Mutate on the same home succeeds immediately.
	if _, err := Mutate(testRunner(), home, func(map[string]any) error {
		return ErrNoChange
	}); err != nil {
		t.Fatalf("second Mutate should re-acquire cleanly: %v", err)
	}
}

func TestMutate_ErrNoChangeSkipsWrite(t *testing.T) {
	home := t.TempDir()
	path := SettingsPath(home)
	writeFile(t, path, "{\n  \"a\": 1\n}\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := Mutate(testRunner(), home, func(settings map[string]any) error {
		settings["touched"] = true // mutation is discarded: no write happens
		return ErrNoChange
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("ErrNoChange must report unchanged")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("file changed despite ErrNoChange.\nbefore: %q\nafter:  %q", before, after)
	}
}

func TestRead_TakesNoLock(t *testing.T) {
	home := t.TempDir()
	writeFile(t, SettingsPath(home), "{\n  \"k\": \"v\"\n}\n")

	settings, err := Read(home)
	if err != nil {
		t.Fatal(err)
	}
	if settings["k"] != "v" {
		t.Fatalf("Read = %#v", settings)
	}
	if _, statErr := os.Stat(LockDir(home)); !os.IsNotExist(statErr) {
		t.Fatalf("Read must create no lock directory: %v", statErr)
	}

	// Missing file: empty non-nil map, nil error.
	settings, err = Read(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if settings == nil || len(settings) != 0 {
		t.Fatalf("missing file: Read = %#v, want empty map", settings)
	}

	// Whitespace-only file: same contract as missing.
	emptyHome := t.TempDir()
	writeFile(t, SettingsPath(emptyHome), "  \n")
	settings, err = Read(emptyHome)
	if err != nil {
		t.Fatal(err)
	}
	if settings == nil || len(settings) != 0 {
		t.Fatalf("empty file: Read = %#v, want empty map", settings)
	}
}

func TestSettingsPath_IsTheOnlyResolver(t *testing.T) {
	home := t.TempDir()
	settings := SettingsPath(home)
	lock := LockDir(home)

	if settings != filepath.Join(home, ".claude", "settings.json") {
		t.Fatalf("SettingsPath = %q, not rooted at the given home", settings)
	}
	if filepath.Dir(lock) != filepath.Join(home, ".claude") {
		t.Fatalf("LockDir = %q, not rooted at the given home", lock)
	}
	// The lock root is dot's own hidden directory, not a path any other tool
	// writes: it shares no name with a Claude Code artifact.
	if base := filepath.Base(lock); base != ".dot-lock" {
		t.Fatalf("LockDir base = %q, want .dot-lock", base)
	}
}

// TestMutate_PreservesForeignEditInsideTheWriteWindow is BUG-03's reproduction.
//
// The foreign writer is simulated from inside the closure on purpose: the
// closure runs after Mutate's unlocked pre-read and before its locked
// re-read, which is exactly the window a concurrent writer lands in. A
// direct write from the closure body needs no goroutine and no timing, and
// it is deterministic. An unconditional write (no re-hash before persisting)
// clobbers the foreign key; the retry re-applies the closure to the foreign
// writer's content instead.
func TestMutate_PreservesForeignEditInsideTheWriteWindow(t *testing.T) {
	home := t.TempDir()
	path := SettingsPath(home)
	writeFile(t, path, "{\n  \"base\": true\n}\n")

	attempts := 0
	changed, err := Mutate(testRunner(), home, func(settings map[string]any) error {
		attempts++
		if attempts == 1 {
			// Another writer replaces the file between our read and our write.
			writeFile(t, path, "{\n  \"base\": true,\n  \"foreign\": \"kept\"\n}\n")
		}
		settings["ours"] = "written"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("adding a key should report changed")
	}
	if attempts != 2 {
		t.Fatalf("closure ran %d time(s), want 2 (one pre-read pass, one retry)", attempts)
	}
	settings, err := Read(home)
	if err != nil {
		t.Fatal(err)
	}
	if settings["ours"] != "written" {
		t.Fatalf("our key missing: %#v", settings)
	}
	if settings["foreign"] != "kept" {
		t.Fatalf("foreign writer's edit was lost: %#v", settings)
	}
}

// TestMutate_RetryIsBoundedAtOne pins the other half of the retry contract:
// a writer that rewrites the file on every attempt makes Mutate fail loudly
// rather than loop or silently clobber.
func TestMutate_RetryIsBoundedAtOne(t *testing.T) {
	home := t.TempDir()
	path := SettingsPath(home)
	writeFile(t, path, "{\n  \"base\": true\n}\n")

	attempts := 0
	changed, err := Mutate(testRunner(), home, func(settings map[string]any) error {
		attempts++
		writeFile(t, path, fmt.Sprintf("{\n  \"foreign\": %d\n}\n", attempts))
		settings["ours"] = "written"
		return nil
	})
	if err == nil {
		t.Fatal("a writer that never stops must make Mutate fail, not loop")
	}
	if changed {
		t.Fatal("a failed Mutate must not report changed")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error must name the settings path: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("closure ran %d time(s), want exactly 2 (retry bounded at one)", attempts)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "\"foreign\": 2") {
		t.Fatalf("the last foreign write must survive untouched: %q", data)
	}
	if _, statErr := os.Stat(LockDir(home)); !os.IsNotExist(statErr) {
		t.Fatalf("lock must be released on the bounded-retry failure: %v", statErr)
	}
}

// TestMutate_RetryClosureDecliningReportsNoChange covers the retry pass's
// own ErrNoChange arm: a closure that declines after seeing the foreign
// writer's content is the closure working correctly, so Mutate returns
// (false, nil) through the deferred release rather than an error.
func TestMutate_RetryClosureDecliningReportsNoChange(t *testing.T) {
	home := t.TempDir()
	path := SettingsPath(home)
	writeFile(t, path, "{\n  \"base\": true\n}\n")
	const foreign = "{\n  \"base\": true,\n  \"owned\": \"someone else\"\n}\n"

	attempts := 0
	changed, err := Mutate(testRunner(), home, func(settings map[string]any) error {
		attempts++
		if attempts == 1 {
			writeFile(t, path, foreign)
			settings["ours"] = "written"
			return nil
		}
		// Second pass sees the foreign content and declines to overwrite it.
		return ErrNoChange
	})
	if err != nil {
		t.Fatalf("a closure declining on the retry pass is not an error: %v", err)
	}
	if changed {
		t.Fatal("a declined retry must report unchanged")
	}
	if attempts != 2 {
		t.Fatalf("closure ran %d time(s), want 2", attempts)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != foreign {
		t.Fatalf("the foreign writer's content must be left untouched: %q", data)
	}
	if _, statErr := os.Stat(LockDir(home)); !os.IsNotExist(statErr) {
		t.Fatalf("lock must be released when the retry declines: %v", statErr)
	}
}

// TestMutate_RefusesASymlinkedSettingsFile is BUG-02's symlink half: the
// non-atomic write followed the link and wrote through to its target, which
// is a write outside ~/.claude entirely.
func TestMutate_RefusesASymlinkedSettingsFile(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	const original = "{\n  \"outside\": true\n}\n"
	writeFile(t, outside, original)

	path := SettingsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	changed, err := Mutate(testRunner(), home, func(settings map[string]any) error {
		settings["added"] = true
		return nil
	})
	if err == nil {
		t.Fatal("a symlinked settings.json must be refused, not written through")
	}
	if changed {
		t.Fatal("a refused write must not report changed")
	}
	data, readErr := os.ReadFile(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != original {
		t.Fatalf("the symlink target was written through: %q", data)
	}
}

// TestMutate_NoOpTakesNoLockAndCreatesNoDirectory is STATE.md's WR-01: the
// pre-phase order acquired the lock first, and AcquirePIDLock's MkdirAll of
// the lock parent created ~/.claude on a home that had none.
func TestMutate_NoOpTakesNoLockAndCreatesNoDirectory(t *testing.T) {
	home := t.TempDir()

	changed, err := Mutate(testRunner(), home, func(settings map[string]any) error {
		settings["ignored"] = true
		return ErrNoChange
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("ErrNoChange must report unchanged")
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("a no-op Mutate must leave the home byte-identical, found: %v", names)
	}
}

// TestMutate_ByteIdenticalContentReportsUnchanged pins the hash-skip that
// EnsureFileAtomic inherits from EnsureFile: a closure that produces the
// same document reports (false, nil) without rewriting the file.
func TestMutate_ByteIdenticalContentReportsUnchanged(t *testing.T) {
	home := t.TempDir()
	path := SettingsPath(home)
	writeFile(t, path, "{\n  \"a\": 1\n}\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := Mutate(testRunner(), home, func(map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("byte-identical content must report unchanged")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("the file was rewritten despite identical content")
	}
}

// TestMutate_DryRunWritesNothing pins that the atomic swap keeps the runner's
// dry-run gate: WriteFileAtomic returns before touching the filesystem and
// EnsureFileAtomic still reports the change it declined to make.
func TestMutate_DryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	path := SettingsPath(home)
	writeFile(t, path, "{\n  \"a\": 1\n}\n")

	runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	changed, err := Mutate(runner, home, func(settings map[string]any) error {
		settings["added"] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a dry run must still report the change it declined to make")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "{\n  \"a\": 1\n}\n" {
		t.Fatalf("dry run wrote the file: %q", data)
	}
}

// TestMutate_ReclaimsAnAbandonedLockWellInsideTheHour is BUG-04's stale
// window clause seen from the caller that needed it. The lock directory has
// no readable pid (the shape a sudo run leaves behind) and is far younger
// than fileutil's package-level hour, so under the shared horizon dot ai hud
// and dot guard would be blocked behind it for the rest of that hour.
func TestMutate_ReclaimsAnAbandonedLockWellInsideTheHour(t *testing.T) {
	home := t.TempDir()
	writeFile(t, SettingsPath(home), "{\n  \"a\": 1\n}\n")

	lockDir := LockDir(home)
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	abandoned := time.Now().Add(-2 * settingsLockStaleAfter)
	if err := os.Chtimes(lockDir, abandoned, abandoned); err != nil {
		t.Fatal(err)
	}

	changed, err := Mutate(testRunner(), home, func(settings map[string]any) error {
		settings["added"] = true
		return nil
	})
	if err != nil {
		t.Fatalf("an abandoned settings lock must self-heal in minutes, not in an hour: %v", err)
	}
	if !changed {
		t.Fatal("the write should have gone through after reclaiming the lock")
	}
}
