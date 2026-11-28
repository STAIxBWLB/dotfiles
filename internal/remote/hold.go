package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// GPUs is how many devices to lease; 0 leases none and skips the probe.
	GPUs int
	// GPUMinFreeMB is the free memory a device needs to be eligible.
	GPUMinFreeMB int
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
	if opts.GPUs < 0 || opts.GPUMinFreeMB < 0 {
		return errors.New("--gpus and --gpu-min-free-mb must not be negative")
	}
	program := filepath.Base(opts.Command[0])
	if opts.DryRun {
		return d.holdDryRun(ctx, h, opts, program)
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
	deadline := time.Now().Add(opts.Wait)
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
	var gpuUUIDs []string
	var gpuSlots []*admission.Slot
	if opts.GPUs > 0 {
		gpuSlots, gpuUUIDs, err = d.acquireGPUs(ctx, h, self, opts.GPUs, opts.GPUMinFreeMB, deadline)
		if err != nil {
			return err
		}
		defer releaseSlots(gpuSlots)
		if err := slot.SetGPUs(gpuUUIDs); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Keep the child in the foreground process group so it can read the tty.
	cmd := exec.CommandContext(ctx, opts.Command[0], opts.Command[1:]...) // #nosec G204 -- explicit local command
	cmd.Dir = opts.Cwd
	if len(gpuUUIDs) > 0 {
		cmd.Env = append(os.Environ(), "DOT_REMOTE_GPUS="+strings.Join(gpuUUIDs, ","))
	}
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
	stop := heartbeat(append([]*admission.Slot{slot}, gpuSlots...)...)
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

// holdDryRun reports the slot, the host load and, with --gpus, the probe and
// the devices a hold would lease. It takes nothing.
func (d Deps) holdDryRun(ctx context.Context, h Host, opts HoldOptions, program string) error {
	load := d.ProbeLoad(ctx, h)
	reason := h.LoadDefer(load, false)
	var gpus []GPU
	var pick []string
	gpuErr := ""
	if opts.GPUs > 0 {
		probed, err := d.ProbeGPUs(ctx, h)
		if err != nil {
			gpuErr = err.Error()
		} else {
			gpus = probed
			held, herr := heldGPUs(d.Store, h.Name, d.now())
			eligible := eligibleGPUs(gpus, opts.GPUMinFreeMB)
			var heldOut []string
			if herr != nil {
				gpuErr = "could not read this machine's GPU leases (" + herr.Error() + "); check the lease store with `dot admit status`"
			} else {
				for _, uuid := range eligible {
					if held[uuid] {
						heldOut = append(heldOut, uuid)
					} else if len(pick) < opts.GPUs {
						pick = append(pick, uuid)
					}
				}
			}
			if reason == "" {
				switch {
				case herr != nil:
					reason = gpuErr
				case opts.GPUs > len(gpus):
					reason = fmt.Sprintf("%d GPUs requested but %s has %d. Lower --gpus to at most %d", opts.GPUs, h.Name, len(gpus), len(gpus))
				case len(pick) < opts.GPUs:
					reason = fmt.Sprintf("%d of %d GPUs on %s have at least %d MiB free", len(eligible), len(gpus), h.Name, opts.GPUMinFreeMB)
					if len(heldOut) > 0 {
						reason += fmt.Sprintf(", %s held by other dot leases", strings.Join(heldOut, ", "))
					}
					reason += fmt.Sprintf("; %d requested. See who holds them with `dot remote status`, wait with --wait, or lower --gpu-min-free-mb", opts.GPUs)
				}
			}
		}
		if reason == "" && gpuErr != "" {
			reason = gpuErr
		}
	}
	if opts.JSON {
		return json.NewEncoder(d.out()).Encode(struct {
			Outcome  string   `json:"outcome"`
			Scope    string   `json:"scope"`
			Class    string   `json:"class"`
			Program  string   `json:"program"`
			MaxRuns  int      `json:"max_runs"`
			Load     HostLoad `json:"load"`
			Probe    []GPU    `json:"gpu_probe,omitempty"`
			ProbeErr string   `json:"gpu_probe_error,omitempty"`
			GPUs     []string `json:"gpus,omitempty"`
			Verdict  string   `json:"verdict,omitempty"`
		}{"dry-run", HostScope(h.Name), admission.ClassRemote, program, h.MaxRuns, load, gpus, gpuErr, pick, reason})
	}
	ui.WriteDryRun(d.out(), fmt.Sprintf("hold one of %d %s slots for %s", h.MaxRuns, HostScope(h.Name), program))
	fmt.Fprintln(d.out(), load.String())
	if opts.GPUs > 0 {
		if gpuErr != "" && len(gpus) == 0 {
			fmt.Fprintf(d.out(), "GPU probe unavailable (%s)\n", gpuErr)
		} else if gpuErr != "" && gpuErr != reason {
			fmt.Fprintf(d.out(), "GPU leases unknown: %s\n", gpuErr)
		}
		for _, g := range gpus {
			fmt.Fprintf(d.out(), "GPU %s %d MiB free\n", g.UUID, g.FreeMB)
		}
		if len(pick) > 0 {
			fmt.Fprintf(d.out(), "would lease GPUs %s\n", strings.Join(pick, ", "))
		}
	}
	if reason != "" {
		fmt.Fprintf(d.out(), "would defer: %s\n", reason)
	}
	return nil
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
