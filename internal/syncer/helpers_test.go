package syncer

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

func TestAcquireLock_FailsWhenLiveLockHeld(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "live.lock")

	release1, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	defer release1()

	// Second call must refuse.
	release2, err := AcquireLock(dir)
	if err == nil {
		release2()
		t.Fatal("AcquireLock did not refuse a held lock")
	}
	if !strings.Contains(err.Error(), "another sync") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestAcquireLock_BreaksStaleLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stale.lock")

	// Pre-seed a "lock" with a guaranteed-dead PID. PID 0 is reserved
	// (init/swapper) and signal-0 to it returns EPERM not ESRCH on most
	// systems, so use a high unlikely-to-exist PID instead.
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	pidFile := filepath.Join(dir, "lock.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(99999999)), 0644); err != nil {
		t.Fatalf("seed pid file: %v", err)
	}

	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock did not break stale lock: %v", err)
	}
	defer release()

	// Lock should now belong to the current process.
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("reading new pid file: %v", err)
	}
	gotPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parsing pid: %v", err)
	}
	if gotPID != os.Getpid() {
		t.Errorf("new lock PID = %d, want %d (current process)", gotPID, os.Getpid())
	}
}

func TestAcquireLock_MissingPIDFileGraceThenReclaim(t *testing.T) {
	// A freshly created lock dir with no lock.pid is the TOCTOU window between
	// Mkdir and the pid write in a live acquirer: it must be treated as held,
	// not stale, so a concurrent caller cannot clobber an in-progress lock.
	dir := filepath.Join(t.TempDir(), "noPID.lock")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	if release, err := AcquireLock(dir); err == nil {
		release()
		t.Fatal("AcquireLock reclaimed a pid-less lock still within its grace window")
	}

	// Once the pid-less dir has outlived the grace window it is abandoned and
	// may be reclaimed. Backdate its mtime well past the fileutil grace period.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatalf("backdate lock dir: %v", err)
	}
	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock should reclaim an abandoned pid-less lock: %v", err)
	}
	release()
}

func TestAcquireLock_ReleaseRemovesLockDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "release.lock")

	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("lock dir should exist after acquire: %v", err)
	}
	release()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("lock dir should be gone after release; stat err = %v", err)
	}
}

func TestNewTimestamp_FilesystemSafe(t *testing.T) {
	ts := newTimestamp()
	if strings.Contains(ts, ":") {
		t.Errorf("timestamp must not contain colons: %q", ts)
	}
	if !strings.HasSuffix(ts, "Z") {
		t.Errorf("timestamp should be UTC (suffix Z): %q", ts)
	}
}

func TestRotateLog_TrimsExcess(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "x.log")

	// Write 100 lines, rotate keeping last 10 when over 50.
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "line "+strconv.Itoa(i))
	}
	if err := os.WriteFile(logFile, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	RotateLog(logFile, 50, 10)

	got, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	keptLines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(keptLines) != 10 {
		t.Errorf("RotateLog kept %d lines, want 10", len(keptLines))
	}
	if keptLines[0] != "line 90" {
		t.Errorf("RotateLog kept wrong tail: first=%q want %q", keptLines[0], "line 90")
	}
}

func TestRotateLog_NoOpUnderThreshold(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "x.log")

	original := "a\nb\nc\n"
	if err := os.WriteFile(logFile, []byte(original), 0644); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	RotateLog(logFile, 50, 10)
	got, _ := os.ReadFile(logFile)
	if string(got) != original {
		t.Errorf("RotateLog modified file under threshold: got %q want %q", got, original)
	}
}

// TestAcquireLock_RefusalWordingIsUnchanged pins the exact operator-visible
// string, not just a substring. The lock primitive gained a caller-supplied
// label for BUG-04; the syncer passes the zero value precisely so this
// wording, which every sync and peer refusal wraps, does not move.
func TestAcquireLock_RefusalWordingIsUnchanged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "live.lock")
	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	defer release()

	_, err = AcquireLock(dir)
	if err == nil {
		t.Fatal("AcquireLock did not refuse a held lock")
	}
	want := "another sync is running (lock: " + dir + ")"
	if err.Error() != want {
		t.Fatalf("refusal = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, fileutil.ErrLockHeld) {
		t.Fatalf("the syncer refusal must still classify as contention: %v", err)
	}
}
