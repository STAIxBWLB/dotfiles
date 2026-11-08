package resourceguard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Run executes the caller's command with scoped worker limits.
func Run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	return RunInDirectory(ctx, "", args, in, out, stderr)
}

// RunInDirectory pins the invocation directory to the explicitly selected project.
func RunInDirectory(ctx context.Context, dir string, args []string, in io.Reader, out, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("command required")
	}
	c := exec.Command(args[0], args[1:]...)
	c.Dir = dir
	c.Stdin = in
	c.Stdout = out
	c.Stderr = stderr
	c.Env = LimitedEnvironment(os.Environ())
	return RunCommand(ctx, c)
}

// RunCommand preserves the supplied command's argv, environment, directory and
// streams. A small shell supervisor remains the process-group leader until all
// its children are cleaned up. We never reap that leader before the final group
// signal, so its PID cannot be recycled to an unrelated process group.
// Background work started by the command is not allowed to outlive this call.
func RunCommand(ctx context.Context, command *exec.Cmd) (runErr error) {
	if command.Err != nil {
		return command.Err
	}
	if len(command.ExtraFiles) != 0 {
		return fmt.Errorf("resource supervisor does not accept extra file descriptors")
	}
	if command.Path == "" {
		return fmt.Errorf("command path required")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := ctx.Err(); err != nil {
		return err
	}
	completionRead, completionWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = completionRead.Close(); _ = completionWrite.Close() }()
	anchorRead, anchorWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = anchorRead.Close(); _ = anchorWrite.Close() }()
	// Close the supervisor protocol descriptors in the child, so neither a
	// foreground nor background child can forge completion or keep the pipe open.
	const script = `trap 'interrupted=1' TERM
"$@" 3>&- 4>&-
result=$?
printf '%s\n' "$result" >&3
while :; do
  interrupted=0
  IFS= read -r anchor <&4
  if [ "$interrupted" = 1 ]; then continue; fi
  exit "$result"
done`
	args := []string{"-c", script, "dot-resource-supervisor", command.Path}
	if len(command.Args) > 1 {
		args = append(args, command.Args[1:]...)
	}
	c := exec.Command("/bin/sh", args...)
	c.Env = command.Env
	c.Dir = command.Dir
	c.Stdin = command.Stdin
	c.Stdout = command.Stdout
	c.Stderr = command.Stderr
	c.ExtraFiles = []*os.File{completionWrite, anchorRead}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	restoreTTY, err := foregroundTerminal(c, command.Stdin)
	if err != nil {
		return err
	}
	c.WaitDelay = 2 * time.Second
	if err = c.Start(); err != nil {
		return errors.Join(fmt.Errorf("starting resource supervisor: %w", err), restoreTTY())
	}
	defer func() { runErr = errors.Join(runErr, restoreTTY()) }()
	_ = completionWrite.Close()
	_ = anchorRead.Close()
	type result struct {
		code int
		err  error
	}
	completed := make(chan result, 1)
	go func() {
		line, e := bufio.NewReader(completionRead).ReadString('\n')
		if e != nil {
			completed <- result{err: e}
			return
		}
		code, e := strconv.Atoi(strings.TrimSpace(line))
		completed <- result{code: code, err: e}
	}()
	var outcome result
	select {
	case outcome = <-completed:
	case <-ctx.Done():
		outcome.err = ctx.Err()
	}
	// TERM is trapped while waiting on the anchor, keeping the supervisor alive.
	// Darwin returns EPERM when SIGKILL targets a group containing only a zombie;
	// retaining a live leader avoids that without ignoring real permission errors.
	// The supervisor is still alive (or unreaped if externally terminated), and
	// therefore still reserves the group ID throughout both signals.
	cleanupErr := finishGroup(c.Process.Pid, syscall.Kill, time.Sleep, c.Wait)
	if outcome.err != nil {
		return errors.Join(outcome.err, cleanupErr)
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	if outcome.code != 0 {
		return fmt.Errorf("command exited with status %d", outcome.code)
	}
	return nil
}

func finishGroup(pid int, kill func(int, syscall.Signal) error, pause func(time.Duration), wait func() error) error {
	var cleanupErr error
	if err := kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		cleanupErr = fmt.Errorf("sending TERM to owned resource group: %w", err)
	}
	pause(100 * time.Millisecond)
	if err := kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("sending KILL to owned resource group: %w", err))
	}
	// Expected supervisor signal exit is not the user's command exit status.
	// Other wait errors (including unreaped output pipes) remain visible.
	if err := wait(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

// LimitedEnvironment sets the shared per-command worker limits without changing the caller environment.
func LimitedEnvironment(env []string) []string {
	var result []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "CARGO_BUILD_JOBS=") || strings.HasPrefix(entry, "GOMAXPROCS=") || strings.HasPrefix(entry, "RUST_TEST_THREADS=") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "CARGO_BUILD_JOBS=2", "GOMAXPROCS=2", "RUST_TEST_THREADS=2")
}
