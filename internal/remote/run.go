package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// RunOptions is one `dot remote run` request.
type RunOptions struct {
	// Path is the checkout to mirror; empty means the current directory.
	Path string
	// Command is CMD: one argument is a shell script, several are an argv.
	Command []string
	// Wait is how long to retry a busy lease before deferring. Zero defers at
	// once, which is the default: a remote run is not worth queueing silently.
	Wait time.Duration
	// Ignore is the set of admission rules this run steps past.
	Ignore    admission.IgnoreSet
	IgnoreSrc string
	DryRun    bool
	// Home is the local home, so a run can refuse to mirror it.
	Home string
}

// ExitCode is a command's own status, which becomes dot's.
type ExitCode struct {
	Code int
	Err  error
}

func (e *ExitCode) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("exited with status %d", e.Code)
}

// Unwrap exposes the underlying failure to errors.Is/As.
func (e *ExitCode) Unwrap() error { return e.Err }

// ExitCode is the process status this error asks for.
func (e *ExitCode) ExitCode() int { return e.Code }

// Run mirrors the checkout at opts.Path and runs CMD in a container there.
//
// The order is the reference behaviour's: take the one-run-per-path lease, then
// one of the host's max_runs leases, then read the host's load, then sync, then
// run. Every defer releases what it already holds, and nothing but the command's
// own output comes back.
func (d Deps) Run(ctx context.Context, cfg Config, h Host, opts RunOptions) error {
	path, err := resolveLocalPath(opts.Path)
	if err != nil {
		return err
	}
	if err := cfg.RefuseRoots(path, opts.Home); err != nil {
		return err
	}
	rel, err := cfg.Rel(path)
	if err != nil {
		return err
	}
	key := PathKey(path)
	mirror := h.MirrorDir(rel)
	container := h.ContainerName(key)
	rsyncPath, err := d.localRsync(ctx)
	if err != nil {
		return err
	}
	if opts.DryRun {
		d.describe(opts, h, path, rel, mirror, container)
		return nil
	}
	if d.Store == nil {
		return errors.New("the admission store is unavailable, so a remote run cannot hold its leases")
	}
	runner := d.Runner
	if runner == nil {
		runner = exec.NewProbeRunner()
	}
	self, err := admission.SelfLease(ctx, runner, path)
	if err != nil {
		return err
	}
	pathSlot, err := AcquirePath(ctx, d.Store, self, h, path, opts.Wait, opts.Ignore.Slot)
	if err != nil {
		return err
	}
	leases := &Leases{Path: pathSlot}
	defer leases.Release()
	admission.NoteOverflowSlot(d.errOut(), pathSlot.Lease(), 1, opts.IgnoreSrc)
	hostSlot, err := AcquireHost(ctx, d.Store, self, h, opts.Wait, opts.Ignore.Slot)
	if err != nil {
		return err
	}
	leases.Host = hostSlot
	leases.stop = heartbeat(pathSlot, hostSlot)
	admission.NoteOverflowSlot(d.errOut(), hostSlot.Lease(), h.MaxRuns, opts.IgnoreSrc)

	if leases.ReclaimedPath() {
		// The run that held this path died without removing its container. The
		// name is derived from the path, so the next run has to clear it before
		// the runtime will accept the same name again.
		fmt.Fprintf(d.errOut(), "dot remote: removing the container left behind by a run that died (%s)\n", container)
		_, _ = d.capture(ctx, SSHArgv(h.SSH, h.Runtime+" rm -f "+ShellQuote(container)), nil)
	}

	load := d.ProbeLoad(ctx, h)
	if reason := h.LoadDefer(load, false); reason != "" {
		if !opts.Ignore.RemoteLoad {
			return deferFor(HostScope(h.Name), reason, time.Minute)
		}
		source := opts.IgnoreSrc
		if source == "" {
			source = "--ignore"
		}
		fmt.Fprintf(d.errOut(), "admission: remote host load ignored (%s): %s\n", source, reason)
	}

	if err := d.sync(ctx, cfg, h, path, mirror, rsyncPath); err != nil {
		return err
	}
	fmt.Fprintf(d.errOut(), "dot remote: running in %s on %s (mirror %s)\n", container, h.Name, mirror)
	exit, sig, runErr := d.ex()(ctx, SSHArgv(h.SSH, RunCommand(h, container, rel, opts.Command)), d.in(), d.out(), d.errOut())
	if runErr != nil {
		return runErr
	}
	if sig != 0 {
		// A dropped ssh leaves the container running on the host. The name is
		// ours, so remove it before reporting the interrupt.
		_, _ = d.capture(context.WithoutCancel(ctx), SSHArgv(h.SSH, h.Runtime+" rm -f "+ShellQuote(container)), nil)
		return &ExitCode{Code: 128 + int(sig), Err: fmt.Errorf("interrupted by signal %d; removed %s", int(sig), container)}
	}
	if exit != 0 {
		return &ExitCode{Code: exit, Err: fmt.Errorf("remote command exited with status %d", exit)}
	}
	return nil
}

// sync sends git's view of the tree and then deletes what the checkout no
// longer has, protecting the mirror's own build outputs.
func (d Deps) sync(ctx context.Context, cfg Config, h Host, path, mirror, rsyncPath string) error {
	listing, err := d.git(ctx, path, gitListArgs()...)
	if err != nil {
		// Not a git checkout: mirror it as it is, with the exclude list standing
		// in for git's own idea of what stays home.
		fmt.Fprintf(d.errOut(), "dot remote: %s is not a git checkout, mirroring it with the exclude list\n", path)
		listing = nil
	}
	target := h.SSH + ":" + mirror
	for _, step := range SyncPlan(rsyncPath, path, target, cfg.Excludes(), listing) {
		if _, err := d.capture(ctx, step.Argv, step.Stdin); err != nil {
			return fmt.Errorf("mirroring %s to %s: %w", path, h.Name, err)
		}
	}
	return nil
}

// describe prints what a dry run would do, without touching the host.
func (d Deps) describe(opts RunOptions, h Host, path, rel, mirror, container string) {
	fmt.Fprintf(d.out(), "would mirror %s to %s:%s (container %s)\n", path, h.SSH, mirror, h.WorkDir(rel))
	fmt.Fprintf(d.out(), "would run in %s on %s:\n  %s\n",
		container, h.Name, strings.Join(SSHArgv(h.SSH, RunCommand(h, container, rel, opts.Command)), " "))
	if opts.Ignore.Slot {
		fmt.Fprintln(d.out(), "would take a lease past capacity (--ignore slot)")
	}
	if opts.Ignore.RemoteLoad {
		fmt.Fprintln(d.out(), "would skip the host load check (--ignore remote-load)")
	}
}

// resolveLocalPath makes the requested checkout absolute and resolves a
// symlink, so one path has one mirror.
func resolveLocalPath(value string) (string, error) {
	if value == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		value = cwd
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", value, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("%s cannot be read: %w", value, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory; pass the checkout to mirror", value)
	}
	return resolved, nil
}

// localRsync resolves the local rsync client a transfer uses, and refuses one
// too old for the two-pass sync. A non-login shell puts /usr/bin ahead of
// Homebrew on macOS, so the absolute candidates are tried before PATH.
func (d Deps) localRsync(ctx context.Context) (string, error) {
	if d.LocalRsync != "" {
		return d.LocalRsync, nil
	}
	var rejected []string
	for _, candidate := range []string{"rsync", "/opt/homebrew/bin/rsync", "/usr/local/bin/rsync"} {
		out, err := d.capture(ctx, []string{candidate, "--version"}, nil)
		if err != nil {
			continue
		}
		if _, verr := CheckRsyncVersion(out); verr != nil {
			rejected = append(rejected, candidate+": "+verr.Error())
			continue
		}
		return candidate, nil
	}
	if len(rejected) == 0 {
		return "", fmt.Errorf("no local rsync found; the mirror needs rsync %s or newer (brew install rsync)", rsyncMinimum)
	}
	return "", fmt.Errorf("no usable local rsync: %s", strings.Join(rejected, "; "))
}
