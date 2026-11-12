package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Execer runs one argv to completion with the given stdin, returning its exit
// code and, when a signal ended it, that signal.
type Execer func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, syscall.Signal, error)

// Deps are the seams one remote command runs on: where leases live, how
// commands run, and where output goes. The zero value works for everything
// except the lease store, so a caller that only reads config needs no wiring.
type Deps struct {
	Store *admission.Store
	// StoreErr is why the store could not be opened, when it could not. It is
	// carried rather than logged so a run can say what to fix.
	StoreErr error
	// SentRoot is the directory holding what this machine last sent to each
	// mirror. Empty means the dot state directory beside the admission root.
	SentRoot string
	Runner   *exec.Runner
	Exec     Execer
	Now      func() time.Time
	In       io.Reader
	Out      io.Writer
	Err      io.Writer
	// LocalRsync is the rsync 3.x client every transfer uses. It is resolved
	// before a run, never guessed here: a non-login shell puts openrsync first
	// on macOS, and openrsync cannot serve --files-from --from0.
	LocalRsync string
	// Machine is this machine's name, written into the host-side claim so a
	// later run can tell its own dead predecessor from another machine's live
	// run. Empty means os.Hostname().
	Machine string
	// Started is what the claim's owner line records as this run's start. Empty
	// means now.
	Started string
	// Git runs one read-only git query in a local checkout and returns stdout.
	Git func(ctx context.Context, dir string, args ...string) ([]byte, error)
	// MirrorFiles lists every file one mirror holds on the host, NUL-separated
	// and relative to the mirror. Empty means the listing is taken over ssh.
	MirrorFiles func(ctx context.Context, h Host, mirror string) ([]byte, error)
	// Ignored reports which of the given paths git's ignore rules cover in one
	// checkout, as a set. Empty means git itself runs there.
	Ignored func(ctx context.Context, checkout string, paths []string) (map[string]bool, error)
}

// DefaultDeps wires the real terminal, git and the invoking user's admission
// state root.
func DefaultDeps(runner *exec.Runner) Deps {
	deps := Deps{Runner: runner}
	root, err := admission.UserStateRoot()
	if err != nil {
		deps.StoreErr = err
		return deps
	}
	deps.Store = admission.NewStore(root, runner)
	return deps
}

func (d Deps) ex() Execer {
	if d.Exec != nil {
		return d.Exec
	}
	return execAttached
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) out() io.Writer {
	if d.Out != nil {
		return d.Out
	}
	return os.Stdout
}

func (d Deps) errOut() io.Writer {
	if d.Err != nil {
		return d.Err
	}
	return os.Stderr
}

func (d Deps) in() io.Reader {
	if d.In != nil {
		return d.In
	}
	return os.Stdin
}

func (d Deps) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if d.Git != nil {
		return d.Git(ctx, dir, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := osexec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// LC_ALL=C because two answers are read as text: "not a git repository"
	// decides whether a path may fall back to the exclude list, and a
	// translated message would make a non-repository look like a listing
	// failure that stops the run.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// capture runs one argv whose output is not the payload and returns its
// stdout. A non-zero exit is an error carrying the stderr tail.
func (d Deps) capture(ctx context.Context, argv []string, stdin []byte) (string, error) {
	var stdout, stderr bytes.Buffer
	var reader io.Reader
	if stdin != nil {
		reader = bytes.NewReader(stdin)
	}
	exit, sig, err := d.ex()(ctx, argv, reader, &stdout, &stderr)
	if err != nil {
		return stdout.String(), err
	}
	if sig != 0 {
		return stdout.String(), fmt.Errorf("%s was interrupted by signal %d", strings.Join(argv[:1], " "), sig)
	}
	if exit != 0 {
		return stdout.String(), fmt.Errorf("%s exited %d: %s", strings.Join(argv[:1], " "), exit, tail(stderr.String()))
	}
	return stdout.String(), nil
}

// execAttached runs argv with attached stdio and forwards SIGINT, SIGTERM and
// SIGHUP to its process group, so a Ctrl-C reaches the runtime client and the
// ssh under it rather than only this process.
//
// The signal this process received is returned as well, even when the child
// exited normally. OpenSSH handles SIGINT and SIGTERM itself and exits 255
// instead of dying by the signal, and a dropped connection also exits 255; in
// both cases the caller has to know the run was interrupted, because a
// container may be left running on the host and "255" is not an answer a person
// can act on.
func execAttached(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, syscall.Signal, error) {
	if len(argv) == 0 {
		return 0, 0, errors.New("no command to run")
	}
	cmd := osexec.Command(argv[0], argv[1:]...) // #nosec G204 -- argv is this package's own construction
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, 0, fmt.Errorf("starting %s: %w", argv[0], err)
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	var forwarded signalMemory
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigCh:
				if s, ok := sig.(syscall.Signal); ok {
					forwarded.remember(s)
					_ = syscall.Kill(-cmd.Process.Pid, s)
				}
			case <-done:
				return
			}
		}
	}()
	var err error
	if ctx != nil {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-ctx.Done():
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			case <-stop:
			}
		}()
	}
	err = cmd.Wait()
	close(done)
	if err == nil {
		return 0, forwarded.signal(), nil
	}
	var exitErr *osexec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), status.Signal(), nil
		}
		return exitErr.ExitCode(), forwarded.signal(), nil
	}
	return 0, 0, err
}

// signalMemory is the last signal this process received and forwarded, written
// from the notifier goroutine and read after the child exits.
type signalMemory struct {
	mu   sync.Mutex
	last syscall.Signal
}

func (s *signalMemory) remember(sig syscall.Signal) {
	s.mu.Lock()
	s.last = sig
	s.mu.Unlock()
}

// signal is the remembered signal, or 0 when the notifier has none yet.
func (s *signalMemory) signal() syscall.Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// tail keeps the last line of a command's stderr, which is where rsync and the
// runtimes put the sentence worth reading.
func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}
