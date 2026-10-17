package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// A scheduled peer run writes its own log (#270): a start and an exit line
// with a timestamp, like the mirror log, and the file rotates past a size
// limit. launchd still redirects the job's output to the same file, which is
// what catches output from before the log opens (a crash during bootstrap).
const (
	peerLogMaxBytes = 10 << 20
	peerLogKeep     = 3
)

// redirectStdio points this process's stdout and stderr, and so every child
// it starts, at f. Tests replace it: the test binary's own stdio must stay.
var redirectStdio = func(f *os.File) error {
	for _, fd := range []int{1, 2} {
		if err := unix.Dup2(int(f.Fd()), fd); err != nil {
			return err
		}
	}
	return nil
}

// openScheduledPeerLog starts the dated log of a real scheduled run and
// returns the func that writes its exit line; any other run gets a no-op.
func openScheduledPeerLog(bs *syncer.BootstrapResult, dryRun bool) func(error) {
	if !isScheduledRun() || dryRun {
		return func(error) {}
	}
	finish, err := startScheduledPeerLog(bs.Config.LogFile)
	if err != nil {
		bs.Runner.Logger.Warn("peer run log unavailable; output stays on launchd's redirection", "err", err)
		return func(error) {}
	}
	return finish
}

// startScheduledPeerLog rotates the log at path, re-points stdout and stderr
// at a fresh handle and writes the start line. The returned func writes the
// exit line for the run's error.
func startScheduledPeerLog(path string) (func(error), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// launchd opened the file before dot ran; after the rename that handle
	// writes into .1, which is why stdio is re-pointed below.
	rotateLogFile(path, peerLogMaxBytes, peerLogKeep)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if err := redirectStdio(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	stamp := func() string { return time.Now().Format("2006-01-02 15:04:05") }
	fmt.Fprintf(f, "%s op=peer-sync start pid=%d\n", stamp(), os.Getpid())
	return func(runErr error) {
		fmt.Fprintln(f, peerExitLine(runErr))
		_ = f.Close()
	}, nil
}

// peerExitLine is the dated exit line of a peer run.
func peerExitLine(runErr error) string {
	line := fmt.Sprintf("%s op=peer-sync exit=%d", time.Now().Format("2006-01-02 15:04:05"), exitCodeOf(runErr))
	if runErr != nil {
		msg, _, _ := strings.Cut(runErr.Error(), "\n")
		line += " err=" + msg
	}
	return line
}

// exitCodeOf mirrors cmd/dot: an error that carries its own code keeps it.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var coder interface{ ExitCode() int }
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return 1
}

// rotateLogFile renames path to path.1 (path.1 to path.2, and so on, keeping
// keep old files) once it is larger than maxBytes. Best effort: a log that
// cannot rotate keeps growing rather than stopping the run.
func rotateLogFile(path string, maxBytes int64, keep int) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= maxBytes {
		return
	}
	for i := keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
}
