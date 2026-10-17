package watchdog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func probeRunner() *exec.Runner {
	return exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestProcessStartMatches(t *testing.T) {
	runner := probeRunner()
	out, err := osexec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	started := strings.Join(strings.Fields(string(out)), " ")
	if started == "" {
		t.Skip("ps returned no lstart for own pid")
	}
	ctx := context.Background()

	ok, err := ProcessStartMatches(ctx, runner, os.Getpid(), started)
	if err != nil || !ok {
		t.Fatalf("own pid with its own lstart = %v, %v; want true", ok, err)
	}
	ok, err = ProcessStartMatches(ctx, runner, os.Getpid(), "Thu Jan  1 00:00:00 1970")
	if err != nil || ok {
		t.Fatalf("own pid with a foreign lstart = %v, %v; want false", ok, err)
	}
	ok, err = ProcessStartMatches(ctx, runner, 1<<30, started)
	if err != nil || ok {
		t.Fatalf("impossible pid = %v, %v; want false, nil", ok, err)
	}
}

// Enforce's ESRCH classification depends on signalErr wrapping with %w.
func TestSignalErr_WrapsForErrorsIs(t *testing.T) {
	if err := signalErr(nil, 100, "SIGTERM"); err != nil {
		t.Fatalf("nil error must stay nil: %v", err)
	}
	err := signalErr(syscall.ESRCH, 100, "SIGTERM")
	if err == nil || !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("wrapped ESRCH lost its identity: %v", err)
	}
	if !strings.Contains(err.Error(), "SIGTERM") || !strings.Contains(err.Error(), "100") {
		t.Fatalf("error must name signal and pid: %v", err)
	}
}

func TestResolveNotify_AndNewNotifier(t *testing.T) {
	s := ResolveNotify(config.WatchdogNotifyConfig{MacOS: true, NtfyURL: "https://ntfy.example/x"}, t.TempDir())
	if !s.MacOS || s.NtfyURL != "https://ntfy.example/x" {
		t.Fatalf("ResolveNotify = %#v", s)
	}
	if n := NewNotifier(s, probeRunner(), "linux"); n == nil || n.GOOS != "linux" {
		t.Fatalf("NewNotifier = %#v", n)
	}
}
