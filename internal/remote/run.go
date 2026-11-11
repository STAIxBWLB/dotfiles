package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// RunOptions is one `dot remote run` request.
type RunOptions struct {
	// Path is the checkout to mirror; empty means the current checkout root,
	// which is the current directory when that is not inside a work tree.
	Path string
	// HostName is the --host value. Empty means the matching project's host and
	// then remote.default, in that order.
	HostName string
	// Command is CMD: one argument is a shell script, several are an argv.
	Command []string
	// Wait is how long to retry a busy lease before deferring. Zero defers at
	// once, which is the default: a remote run is not worth queueing silently.
	Wait time.Duration
	// Ignore is the set of admission rules this run steps past. It relaxes the
	// host's max_runs and its load check; it never relaxes the one-run-per-path
	// lease, because a second run would `rsync --delete` into a mirror another
	// run is using.
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

// sshFailureStatus is the status OpenSSH exits with when the connection itself
// failed, including when it handled a SIGINT or SIGTERM rather than dying by
// it.
const sshFailureStatus = 255

// Run mirrors the checkout at opts.Path and runs CMD in a container there.
//
// The order is the reference behaviour's: take the one-run-per-path lease, then
// one of the host's max_runs leases, then read the host's load, then clear
// whatever an earlier run or another machine left under this mirror's container
// name, then sync, then run. Every defer releases what it already holds, and
// nothing but the command's own output comes back.
func (d Deps) Run(ctx context.Context, cfg Config, opts RunOptions) error {
	path, err := d.checkoutPath(ctx, opts.Path)
	if err != nil {
		return err
	}
	h, err := cfg.HostFor(opts.HostName, path)
	if err != nil {
		return err
	}
	if err := cfg.RefuseRoots(path, resolveExisting(opts.Home)); err != nil {
		return err
	}
	rel, err := cfg.Rel(path)
	if err != nil {
		return err
	}
	mirror := h.MirrorDir(rel)
	container := h.ContainerName(rel)
	rsyncPath, err := d.localRsync(ctx)
	if err != nil {
		return err
	}
	if opts.DryRun {
		d.describe(opts, h, path, rel, mirror, container)
		return nil
	}
	if d.Store == nil {
		if d.StoreErr != nil {
			return fmt.Errorf("the admission store is unavailable (%w), so a remote run cannot hold its leases: make sure the state directory is writable (`dot admit status` opens the same store and reports the same error)", d.StoreErr)
		}
		return errors.New("the admission store is unavailable, so a remote run cannot hold its leases: run `dot admit status` to open and check the store")
	}
	runner := d.Runner
	if runner == nil {
		runner = exec.NewProbeRunner()
	}
	self, err := admission.SelfLease(ctx, runner, path)
	if err != nil {
		return err
	}
	// No bypass here on purpose: `--ignore slot` relaxes the host's max_runs,
	// not this, because a second run would delete files inside a mirror that is
	// in use.
	pathSlot, err := AcquirePath(ctx, d.Store, self, h, path, opts.Wait)
	if err != nil {
		return err
	}
	leases := &Leases{Path: pathSlot}
	defer leases.Release()
	hostSlot, err := AcquireHost(ctx, d.Store, self, h, opts.Wait, opts.Ignore.Slot)
	if err != nil {
		return err
	}
	leases.Host = hostSlot
	leases.stop = heartbeat(pathSlot, hostSlot)
	admission.NoteOverflowSlot(d.errOut(), hostSlot.Lease(), h.MaxRuns, opts.IgnoreSrc)

	// The host's own load, and only when the host sets a limit: a host with no
	// max_load_ratio has no gate to read for.
	if h.MaxLoadRatio > 0 {
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
	}

	pre, err := d.preflight(ctx, h, rel, container)
	if err != nil {
		return err
	}
	switch pre.Container {
	case containerRunning:
		// Leases are per machine, so this is the one exclusion the host
		// enforces for us: a run of the same mirror from somewhere else.
		return deferFor(PathScope(path), fmt.Sprintf("a container for %s is already running on %s (%s), so another machine is running this checkout; wait for it or remove it with `ssh %s %s rm -f %s`",
			mirror, h.Name, container, h.SSH, h.Runtime, container), time.Minute)
	case containerRemoved:
		fmt.Fprintf(d.errOut(), "dot remote: removed the stopped container %s left on %s\n", container, h.Name)
	}
	if pre.RecordFailed {
		fmt.Fprintf(d.errOut(), "dot remote: could not record the mirror of %s on %s, so `dot remote status` will not size it\n", path, h.Name)
	}
	if pre.Image == imageUnknown {
		return fmt.Errorf("%s does not have %s yet, so there is nothing to run in: run `dot remote setup`", h.Name, h.Image())
	}
	if imageRunsAsRoot(pre.ImageUser) {
		return fmt.Errorf("%s's image %s runs as root (no USER), so the command would write root-owned files into %s: give the image an unprivileged user (ARG UID/GID, then USER dev - see docs/guides/servers.md) and re-run `dot remote setup`",
			h.Name, h.Image(), mirror)
	}

	listing, isCheckout, err := d.gitListing(ctx, path)
	if err != nil {
		return err
	}
	var ignoreFiles []string
	if isCheckout {
		ignoreFiles = d.gitIgnoreFiles(ctx, path)
	} else {
		fmt.Fprintf(d.errOut(), "dot remote: %s is not a git checkout, mirroring it with the exclude list\n", path)
	}
	if err := d.sync(ctx, cfg, h, path, mirror, rsyncPath, ignoreFiles, listing); err != nil {
		return err
	}
	fmt.Fprintf(d.errOut(), "dot remote: running in %s on %s (mirror %s)\n", container, h.Name, mirror)
	exit, sig, runErr := d.ex()(ctx, SSHArgv(h.SSH, RunCommand(h, container, rel, opts.Command)), d.in(), d.out(), d.errOut())
	if runErr != nil {
		return runErr
	}
	if sig != 0 {
		d.removeContainer(ctx, h, container)
		return &ExitCode{Code: 128 + int(sig), Err: fmt.Errorf("interrupted by signal %d; removed %s on %s", int(sig), container, h.Name)}
	}
	if exit == sshFailureStatus {
		// ssh's own failure status, which is how a dropped connection and a
		// handled SIGINT both arrive. The container may still be running there.
		d.removeContainer(ctx, h, container)
		return &ExitCode{Code: exit, Err: fmt.Errorf("the ssh connection to %s failed with status %d, so the run did not finish; removed %s if it was left behind",
			h.Name, exit, container)}
	}
	if exit != 0 {
		return &ExitCode{Code: exit, Err: fmt.Errorf("remote command exited with status %d", exit)}
	}
	return nil
}

// removeContainer removes one container by name, ignoring a failure: this is the
// cleanup of a path that is already reporting a worse problem.
func (d Deps) removeContainer(ctx context.Context, h Host, container string) {
	_, _ = d.capture(context.WithoutCancel(ctx), SSHArgv(h.SSH, h.Runtime+" rm -f "+ShellQuote(container)), nil)
}

// sync sends git's view of the tree and then deletes what the checkout no
// longer has, protecting the mirror's own build outputs.
func (d Deps) sync(ctx context.Context, cfg Config, h Host, path, mirror, rsyncPath string, ignoreFiles []string, listing []byte) error {
	target := h.SSH + ":" + mirror
	for _, step := range SyncPlan(rsyncPath, path, target, cfg.Excludes(), ignoreFiles, listing) {
		if _, err := d.capture(ctx, step.Argv, step.Stdin); err != nil {
			return fmt.Errorf("mirroring %s to %s: %w", path, h.Name, err)
		}
	}
	return nil
}

// describe prints what a dry run would do, without touching the host.
func (d Deps) describe(opts RunOptions, h Host, path, rel, mirror, container string) {
	fmt.Fprintf(d.out(), "would mirror %s to %s:%s (container %s)\n", path, h.SSH, mirror, container)
	fmt.Fprintf(d.out(), "would run in %s on %s:\n  %s\n",
		container, h.Name, strings.Join(SSHArgv(h.SSH, RunCommand(h, container, rel, opts.Command)), " "))
	if opts.Ignore.Slot {
		fmt.Fprintln(d.out(), "would take a lease past the host's max_runs (--ignore slot)")
	}
	if opts.Ignore.RemoteLoad {
		fmt.Fprintln(d.out(), "would skip the host load check (--ignore remote-load)")
	}
}

// checkoutPath makes the requested checkout absolute, resolves a symlink, so
// one path has one mirror, and - when no path was given - answers the checkout
// the current directory belongs to rather than the directory itself. A run
// started in a subdirectory has to send the whole tree.
func (d Deps) checkoutPath(ctx context.Context, value string) (string, error) {
	if value != "" {
		return resolveLocalPath(value)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	out, gerr := d.git(ctx, cwd, "rev-parse", "--show-toplevel")
	if gerr == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			return resolveLocalPath(root)
		}
	} else if !notARepository(gerr) {
		return "", fmt.Errorf("finding the checkout %s belongs to: %w", cwd, gerr)
	}
	return resolveLocalPath(cwd)
}

// resolveLocalPath makes a path absolute and resolves a symlink, so one path has
// one mirror.
func resolveLocalPath(value string) (string, error) {
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

// ResolvePath is resolveLocalPath for callers that need the same spelling of a
// path before they hand it to Run (the config's project match compares
// resolved paths).
func ResolvePath(value string) (string, error) { return resolveLocalPath(value) }

// gitListing returns git's view of the tree and whether path is a work tree.
//
// A repository whose listing cannot be read is an error, never a fallback: the
// exclude list is not a substitute for git's own view, and treating a timeout,
// a corrupt index or a safe.directory refusal as "not a checkout" would send
// every file git deliberately keeps home.
func (d Deps) gitListing(ctx context.Context, path string) ([]byte, bool, error) {
	listing, err := d.git(ctx, path, gitListArgs()...)
	if err == nil {
		return listing, true, nil
	}
	inside, ierr := d.git(ctx, path, "rev-parse", "--is-inside-work-tree")
	switch {
	case ierr == nil && strings.TrimSpace(string(inside)) == "true":
		return nil, true, fmt.Errorf("git cannot list %s, so the run will not guess what to send: %w", path, err)
	case ierr != nil && !notARepository(ierr):
		return nil, false, fmt.Errorf("checking whether %s is a git checkout: %w", path, ierr)
	}
	return nil, false, nil
}

// notARepository reports the one git failure that means "there is no repository
// here". Every other failure has to stop a run rather than widen what it sends.
func notARepository(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not a git repository")
}

// gitIgnoreFiles lists the other ignore files git honored for this checkout -
// .git/info/exclude and the global excludes file - so the delete pass protects
// what the ship pass never sent. Only files that exist are returned.
func (d Deps) gitIgnoreFiles(ctx context.Context, path string) []string {
	var candidates []string
	if out, err := d.git(ctx, path, "rev-parse", "--git-path", "info/exclude"); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			if !filepath.IsAbs(name) {
				name = filepath.Join(path, name)
			}
			candidates = append(candidates, name)
		}
	}
	if out, err := d.git(ctx, path, "config", "--path", "--get", "core.excludesFile"); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			candidates = append(candidates, name)
		}
	} else if home, herr := os.UserHomeDir(); herr == nil {
		// git's own default when core.excludesFile is unset:
		// $XDG_CONFIG_HOME/git/ignore, then ~/.config/git/ignore. The root comes
		// from config.ConfigHome rather than a second read of the variable,
		// because one place decides a path from it (internal/config.StateDir).
		candidates = append(candidates, filepath.Join(config.ConfigHome(home, false), "git", "ignore"))
	}
	var files []string
	for _, name := range candidates {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			files = append(files, name)
		}
	}
	return files
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

// Preflight is what one `dot remote` round trip under the container name
// reports: the container's state, the image's user, and whether the mirror was
// recorded for `status`.
type preflight struct {
	Container    string
	ImageUser    string
	Image        string
	RecordFailed bool
}

const (
	containerAbsent       = "absent"
	containerRemoved      = "removed"
	containerRunning      = "running"
	containerRemoveFailed = "remove-failed"

	imageKnown   = "known"
	imageUnknown = "unknown"
)

// preflightTemplate checks, removes, records and inspects in one ssh round trip.
//
// The removal is unconditional rather than a response to a reclaimed lease: a
// lease is per machine, so a container can be left by this machine's killed run,
// by an older dot, or by another machine's run, and the name is derived from the
// mirror path. Checking it here, before the sync, is the only place that can
// stop the sync from changing a mirror another machine is using.
const preflightTemplate = `name=%s
runtime=%s
records="$HOME"/%s/.dot-remote
if mkdir -p "$records" 2>/dev/null && printf '%%s\n' %s >"$records/%s" 2>/dev/null; then :; else printf 'record\tfailed\n'; fi
if [ -n "$("$runtime" ps -a --filter "name=^$name$" --format '{{.Names}}' 2>/dev/null)" ]; then
  if [ -n "$("$runtime" ps --filter "name=^$name$" --format '{{.Names}}' 2>/dev/null)" ]; then
    printf 'container\trunning\n'
  elif "$runtime" rm -f "$name" >/dev/null 2>&1; then
    printf 'container\tremoved\n'
  else
    printf 'container\tremove-failed\n'
  fi
else
  printf 'container\tabsent\n'
fi
if user=$("$runtime" image inspect -f '{{.Config.User}}' %s 2>/dev/null); then
  printf 'image\tknown\nuser\t%%s\n' "$user"
else
  printf 'image\tunknown\n'
fi`

// preflight asks the host about this mirror's container and image before
// anything is sent, and records the mirror so `dot remote status` can size it.
func (d Deps) preflight(ctx context.Context, h Host, rel, container string) (preflight, error) {
	script := fmt.Sprintf(preflightTemplate,
		ShellQuote(container), ShellQuote(h.Runtime), ShellQuote(h.Root), ShellQuote(rel),
		MirrorKey(rel), ShellQuote(h.Image()))
	out, err := d.capture(ctx, SSHArgv(h.SSH, script), nil)
	if err != nil {
		return preflight{}, fmt.Errorf("checking %s on %s: %w", rel, h.Name, err)
	}
	result := preflight{Container: containerAbsent, Image: imageUnknown}
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "\t")
		if !found {
			continue
		}
		switch key {
		case "container":
			result.Container = value
		case "record":
			result.RecordFailed = value == "failed"
		case "image":
			result.Image = value
		case "user":
			result.ImageUser = value
		}
	}
	if result.Container == containerRemoveFailed {
		return result, fmt.Errorf("%s could not remove the stopped container %s, which holds the name this run needs: remove it by hand with `ssh %s %s rm -f %s`",
			h.Name, container, h.SSH, h.Runtime, container)
	}
	return result, nil
}

// imageRunsAsRoot reports whether an image's configured user is root. An empty
// USER is the runtime's default, which is root; "0" and "root" say so outright.
// The job has to run as the image's own unprivileged user, or everything it
// writes into the mirror belongs to root and the host user cannot clean it up.
func imageRunsAsRoot(user string) bool {
	user = strings.TrimSpace(user)
	if user == "" {
		return true
	}
	name, _, _ := strings.Cut(user, ":")
	if name == "" || name == "root" {
		return true
	}
	if id, err := strconv.Atoi(name); err == nil && id == 0 {
		return true
	}
	return false
}
