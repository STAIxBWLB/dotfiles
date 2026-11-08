package resourceguard

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGroupNeverReapedBeforeLastSignal(t *testing.T) {
	reaped := false
	calls := []syscall.Signal{}
	err := finishGroup(123, func(pid int, sig syscall.Signal) error {
		if reaped {
			t.Fatal("signal after leader reaped risks foreign PID reuse")
		}
		if pid != -123 {
			t.Fatalf("wrong group %d", pid)
		}
		calls = append(calls, sig)
		return nil
	}, func(time.Duration) {}, func() error { reaped = true; return nil })
	if err != nil || !reaped || len(calls) != 2 || calls[0] != syscall.SIGTERM || calls[1] != syscall.SIGKILL {
		t.Fatalf("calls=%v reaped=%t err=%v", calls, reaped, err)
	}
}
func TestRunNormalExitCleansBackgroundChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "background.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.Command("/bin/sh", "-c", `sleep 60 >/dev/null 2>&1 & echo "$!" > "$1"; exit 0`, "test", pidFile)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := RunCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	assertNotRunning(t, pid)
}
func TestRunCancellationCleansOwnGroupOnly(t *testing.T) {
	foreign := exec.Command("/bin/sleep", "60")
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Process.Kill(); _ = foreign.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := RunCommand(ctx, exec.Command("/bin/sleep", "60")); err == nil {
		t.Fatal("canceled job succeeded")
	}
	if err := foreign.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("foreign process affected: %v", err)
	}
}
func TestRunCommandPreservesArgumentsEnvironmentAndExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output strings.Builder
	c := exec.Command("/bin/sh", "-c", `printf '%s:%s' "$TOKEN" "$1"`, "test", "literal ; $(false)")
	c.Env = append(os.Environ(), "TOKEN=right")
	c.Stdout = &output
	if err := RunCommand(ctx, c); err != nil {
		t.Fatal(err)
	}
	if output.String() != "right:literal ; $(false)" {
		t.Fatal(output.String())
	}
	if err := RunCommand(ctx, exec.Command("/bin/sh", "-c", "exit 7")); err == nil || !strings.Contains(err.Error(), "status 7") {
		t.Fatalf("%v", err)
	}
}
func assertNotRunning(t *testing.T, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	if err == nil && !strings.Contains(string(out), "Z") {
		t.Fatalf("background child %d remains running: %s", pid, out)
	}
}

func TestLimitsReplaceOnlyScopedVariables(t *testing.T) {
	got := LimitedEnvironment([]string{"HOME=/h", "GOMAXPROCS=99", "CARGO_BUILD_JOBS=9", "RUST_TEST_THREADS=8"})
	if len(got) != 4 || got[0] != "HOME=/h" {
		t.Fatalf("%v", got)
	}
}
