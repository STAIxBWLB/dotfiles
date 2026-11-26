package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	probeexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// Interrupt is the signal that canceled the command context.
type Interrupt syscall.Signal

func (s Interrupt) Error() string { return fmt.Sprintf("interrupted by signal %d", s) }

// HoldOptions controls admission for an external runner.
type HoldOptions struct {
	Command []string
	Cwd     string
	Wait    time.Duration
	DryRun  bool
	JSON    bool
}

// Hold reserves one host slot while Command runs locally.
func (d Deps) Hold(ctx context.Context, h Host, opts HoldOptions) (err error) {
	defer func() {
		var sig Interrupt
		if errors.As(context.Cause(ctx), &sig) {
			err = &ExitCode{Code: 128 + int(sig), Err: sig}
		}
	}()
	if len(opts.Command) == 0 || opts.Command[0] == "" {
		return errors.New("hold requires a command after --")
	}
	program := filepath.Base(opts.Command[0])
	if opts.DryRun {
		load := d.ProbeLoad(ctx, h)
		reason := h.LoadDefer(load, false)
		if opts.JSON {
			return json.NewEncoder(d.out()).Encode(struct {
				Outcome string   `json:"outcome"`
				Scope   string   `json:"scope"`
				Class   string   `json:"class"`
				Program string   `json:"program"`
				MaxRuns int      `json:"max_runs"`
				Load    HostLoad `json:"load"`
				Verdict string   `json:"verdict,omitempty"`
			}{"dry-run", HostScope(h.Name), admission.ClassRemote, program, h.MaxRuns, load, reason})
		}
		ui.WriteDryRun(d.out(), fmt.Sprintf("hold one of %d %s slots for %s", h.MaxRuns, HostScope(h.Name), program))
		fmt.Fprintln(d.out(), load.String())
		if reason != "" {
			fmt.Fprintf(d.out(), "would defer: %s\n", reason)
		}
		return nil
	}
	if d.Store == nil {
		if d.StoreErr != nil {
			return fmt.Errorf("opening the admission store for hold: %w", d.StoreErr)
		}
		return errors.New("the admission store is unavailable: run `dot admit status` to check it")
	}
	runner := d.Runner
	if runner == nil {
		runner = probeexec.NewProbeRunner()
	}
	self, err := admission.SelfLease(ctx, runner, opts.Cwd)
	if err != nil {
		return err
	}
	self.Program = program
	slot, err := AcquireHost(ctx, d.Store, self, h, opts.Wait, false)
	if err != nil {
		return err
	}
	defer func() { _ = slot.Release() }()
	if h.MaxLoadRatio > 0 {
		load := d.ProbeLoad(ctx, h)
		if reason := h.LoadDefer(load, false); reason != "" {
			return deferFor(HostScope(h.Name), reason, time.Minute)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Keep the child in the foreground process group so it can read the tty.
	cmd := exec.CommandContext(ctx, opts.Command[0], opts.Command[1:]...) // #nosec G204 -- explicit local command
	cmd.Dir = opts.Cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = d.in(), d.out(), d.errOut()
	cmd.Cancel = func() error {
		return cancelHold(ctx, func() bool {
			file, ok := cmd.Stdin.(term.File)
			return ok && term.IsTerminal(file.Fd())
		}, cmd.Process.Signal)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", program, err)
	}
	if err := slot.SetCommandPID(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	stop := heartbeat(slot)
	defer stop()
	err = cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		}
		return &ExitCode{Code: code, Err: err}
	}
	return err
}

// cancelHold forwards cancellation to the child, except for Ctrl-C from a
// terminal. The tty already signals the foreground group, including the child.
func cancelHold(ctx context.Context, stdinIsTerminal func() bool, send func(os.Signal) error) error {
	sig := syscall.SIGTERM
	var interrupt Interrupt
	if errors.As(context.Cause(ctx), &interrupt) {
		sig = syscall.Signal(interrupt)
	}
	if sig == syscall.SIGINT && stdinIsTerminal() {
		return os.ErrProcessDone
	}
	return send(sig)
}
