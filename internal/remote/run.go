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

	seen, err := d.inspect(ctx, h, rel, path, container)
	if err != nil {
		return err
	}
	if seen.RecordFailed {
		fmt.Fprintf(d.errOut(), "dot remote: could not record the mirror of %s on %s, so `dot remote status` will not size it\n", path, h.Name)
	}
	// A failed query is not an answer: an empty list from a broken runtime would
	// otherwise read as "nothing there" and, worse, as "nothing running", and
	// the removal below would kill a live run of another machine.
	if seen.Container == containerQueryFailed {
		return fmt.Errorf("%s could not report the container of %s (%s), so this run will neither sync nor remove anything there: check that %s answers on %s",
			h.Name, mirror, seen.ContainerDetail, h.Runtime, h.Name)
	}
	// The host-side claim is the exclusion the local leases cannot give: two
	// machines pointing at one host hold independent lease counts. A claim is
	// only this run's to take over when its owner record names this machine AND
	// this checkout: the local path lease proves exactly one previous run dead,
	// and only the record says which one.
	switch seen.Claim {
	case claimForeign:
		return deferFor(PathScope(path), claimHeldElsewhere(h.MirrorDir(rel), h, rel, seen.Detail), time.Minute)
	case claimUnreadable:
		return deferFor(PathScope(path), fmt.Sprintf("the claim on %s on %s cannot be read (%s), so this run will not guess whether another run is using the mirror; inspect it with `%s`",
			mirror, h.Name, seen.Detail, h.claimCommand("ls -la", rel)), time.Minute)
	}
	stale := seen.Claim == claimOurs
	if err := d.claimMirror(ctx, h, rel, path, stale); err != nil {
		return err
	}
	// Held from here to the end of the run, on every exit path - except one
	// where a container could not be removed: then the claim is the only thing
	// keeping another run out of a mirror that still has that container in it,
	// and this machine's next run takes it over and retries.
	keepClaim := false
	defer func() {
		if keepClaim {
			return
		}
		d.releaseClaim(ctx, h, rel, path)
	}()
	if stale {
		// The claim was this machine's own leftover for this same checkout, and
		// the local path lease this run holds proves the owner is dead - so
		// whatever is still under this name belongs to that dead run.
		fmt.Fprintf(d.errOut(), "dot remote: took over the claim an earlier run of %s left on %s\n", mirror, h.Name)
		if err := d.removeContainerStrict(ctx, h, container); err != nil {
			keepClaim = true
			return claimKept(err, mirror, h, rel, container)
		}
		if seen.Container != containerAbsent {
			fmt.Fprintf(d.errOut(), "dot remote: removed the container %s that run left behind\n", container)
		}
	} else {
		switch seen.Container {
		case containerRunning:
			// No claim, but a live container: an older dot, or a machine that
			// never took one. Either way something else is inside the mirror.
			return deferFor(PathScope(path), fmt.Sprintf("a container for %s is already running on %s (%s) without a claim, so another machine is running this checkout; wait for it or remove it with `%s`",
				mirror, h.Name, container, sshCommand(h, h.Runtime+" rm -f "+quotedWord(container))), time.Minute)
		case containerStopped:
			if err := d.removeContainerStrict(ctx, h, container); err != nil {
				keepClaim = true
				return claimKept(err, mirror, h, rel, container)
			}
			fmt.Fprintf(d.errOut(), "dot remote: removed the stopped container %s left on %s\n", container, h.Name)
		}
	}
	if seen.Image == imageUnknown {
		return fmt.Errorf("%s does not have %s yet, so there is nothing to run in: run `dot remote setup`", h.Name, h.Image())
	}
	if imageRunsAsRoot(seen.ImageUser) {
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
		// The interrupt cancels ctx; the removal still has to run, and it has to
		// say whether it worked: "removed" over a container that is still there
		// would leave the next machine locked out of nothing and this one
		// believing its mirror clean.
		if err := d.removeContainerStrict(ctx, h, container); err != nil {
			keepClaim = true
			return &ExitCode{Code: 128 + int(sig), Err: fmt.Errorf("interrupted by signal %d; %w", int(sig), claimKept(err, mirror, h, rel, container))}
		}
		return &ExitCode{Code: 128 + int(sig), Err: fmt.Errorf("interrupted by signal %d; removed %s on %s", int(sig), container, h.Name)}
	}
	if exit == sshFailureStatus {
		// 255 is ssh's own failure status, which is how a dropped connection and
		// a handled SIGINT both arrive - and it is also what a command that
		// exits 255 reports, since the run is one ssh invocation. The container
		// may still be running there.
		if err := d.removeContainerStrict(ctx, h, container); err != nil {
			keepClaim = true
			return &ExitCode{Code: exit, Err: fmt.Errorf("ssh or the command exited %d, so the run did not finish; %w", exit, claimKept(err, mirror, h, rel, container))}
		}
		return &ExitCode{Code: exit, Err: fmt.Errorf("ssh or the command exited %d, so the run did not finish; removed %s if it was left behind",
			exit, container)}
	}
	if exit != 0 {
		return &ExitCode{Code: exit, Err: fmt.Errorf("remote command exited with status %d", exit)}
	}
	return nil
}

// claimHeldElsewhere is why a claim this run may not take defers it. The owner
// record is quoted whole - machine, pid, start and checkout - because it is what
// tells another machine's run from a different checkout of this one, and only
// the record carries that.
func claimHeldElsewhere(mirror string, h Host, rel, owner string) string {
	return fmt.Sprintf("the claim on %s on %s belongs to %s, so this mirror is in use elsewhere; wait for it, or remove it once that run is gone with `%s`",
		mirror, h.Name, owner, h.claimCommand("rm -rf", rel))
}

// claimKept is a container removal that did not happen, with what keeps the
// run from going on: the claim stays, so no other machine can sync into a
// mirror that may still have that container in it.
func claimKept(err error, mirror string, h Host, rel, container string) error {
	return fmt.Errorf("%w, so this run stops here and keeps the claim on %s on %s: another machine cannot sync into a mirror that may still have %s in it, and this machine's next run of that checkout takes the claim over and retries the removal (or clear both with `%s` and `%s`)",
		err, mirror, h.Name, container, sshCommand(h, h.Runtime+" rm -f "+quotedWord(container)), h.claimCommand("rm -rf", rel))
}

// containerPresentScript is the query that settles a failed removal: the
// container's name if the host still lists it, nothing if it is gone.
func containerPresentScript(h Host, container string) string {
	return h.Runtime + " ps -a --filter " + ShellQuote("name=^"+container+"$") + " --format '{{.Names}}' # container-present"
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

// ResolveCheckout answers the checkout a run would target, so a caller can pick
// the host from it: the value given, or - when it is empty - the checkout the
// current directory belongs to, which is the same default the run applies.
func ResolveCheckout(ctx context.Context, d Deps, value string) (string, error) {
	return d.checkoutPath(ctx, value)
}

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

// inspection is what one read-only `dot remote` round trip under this mirror's
// name reports: the claim's owner, the container's state and the image's user.
type inspection struct {
	// Claim is claimFree, claimAcquired, claimOurs, claimForeign or
	// claimUnreadable.
	Claim string
	// Detail carries the claim's owner line, or the reason it could not be read.
	Detail string
	// Container is one of the container* states, and ContainerDetail is the
	// runtime's own error when that state is containerQueryFailed.
	Container       string
	ContainerDetail string
	Image           string
	ImageUser       string
	RecordFailed    bool
}

const (
	containerAbsent      = "absent"
	containerRunning     = "running"
	containerStopped     = "stopped"
	containerQueryFailed = "query-failed"

	claimFree       = "free"
	claimAcquired   = "acquired"
	claimOurs       = "ours"
	claimForeign    = "foreign"
	claimUnreadable = "unreadable"
	claimLostRace   = "lost-race"

	imageKnown   = "known"
	imageUnknown = "unknown"
)

// inspectTemplate reads this mirror's claim, container and image in one ssh
// round trip, and records the mirror so `dot remote status` can size it. It
// changes nothing on the host: the claim is taken separately, once this run has
// decided it may.
//
// Every runtime query is checked. An empty list from a broken runtime is not
// "no container waiting": read as one, the run would sync into a mirror another
// machine is using, and read the other way round it would remove a live one.
const inspectTemplate = `name=%s
runtime=%s
records="$HOME"/%s/.dot-remote
lock="$records"/%s.claim
me=%s
checkout=%s
if mkdir -p "$records" 2>/dev/null && printf '%%s\n' %s >"$records/%s" 2>/dev/null; then :; else printf 'record\tfailed\n'; fi
if [ -d "$lock" ]; then
  owner=$(cat "$lock/owner" 2>/dev/null)
  if [ -z "$owner" ]; then
    printf 'claim\tunreadable\tno readable %%s/owner\n' "$lock"
  elif [ "$(printf '%%s' "$owner" | cut -f1)" != "$me" ] || [ "$(printf '%%s' "$owner" | cut -f4)" != "$checkout" ]; then
    printf 'claim\tforeign\t%%s\n' "$owner"
  else
    printf 'claim\tours\t%%s\n' "$owner"
  fi
else
  printf 'claim\tfree\n'
fi
if listed=$("$runtime" ps -a --filter "name=^$name$" --format '{{.Names}}' 2>&1); then
  if [ -z "$listed" ]; then
    printf 'container\tabsent\n'
  elif running=$("$runtime" ps --filter "name=^$name$" --format '{{.Names}}' 2>&1); then
    if [ -n "$running" ]; then
      printf 'container\trunning\n'
    else
      printf 'container\tstopped\n'
    fi
  else
    printf 'container\tquery-failed\t%%s\n' "$running"
  fi
else
  printf 'container\tquery-failed\t%%s\n' "$listed"
fi
if user=$("$runtime" image inspect -f '{{.Config.User}}' %s 2>/dev/null); then
  printf 'image\tknown\nuser\t%%s\n' "$user"
else
  printf 'image\tunknown\n'
fi`

// claimTemplate takes this mirror's claim with a mkdir, which is atomic on the
// host: the winner of a race is the only run that gets the directory, so two
// machines cannot both pass a check and then sync into one mirror.
//
// replace says the claim found on the host is this run's own predecessor, and
// even then it is re-checked here rather than trusted from the earlier round
// trip: the owner must still name this machine and this checkout, or the run
// defers instead of removing a claim that changed hands in between.
//
// Every step reports its own failure with the reason the host gave. A failed
// mkdir that is not "someone else got there first" is a failure, never a lost
// race, and a failed removal is never success: the run has to stop and say what
// to look at.
const claimTemplate = `# claim: take
records="$HOME"/%s/.dot-remote
lock="$records"/%s.claim
me=%s
checkout=%s
mode=%s
if [ "$mode" = replace ]; then
  owner=$(cat "$lock/owner" 2>/dev/null)
  if [ "$(printf '%%s' "$owner" | cut -f1)" != "$me" ] || [ "$(printf '%%s' "$owner" | cut -f4)" != "$checkout" ]; then
    printf 'claim\tforeign\t%%s\n' "$owner"
    exit 0
  fi
  if ! err=$(rm -rf "$lock" 2>&1); then
    printf 'claim\tfailed\tthe leftover claim under %%s could not be removed (%%s)\n' "$lock" "$err"
    exit 0
  fi
fi
if err=$(mkdir "$lock" 2>&1); then :; else
  if [ -d "$lock" ]; then
    printf 'claim\tlost-race\n'
  else
    printf 'claim\tfailed\tcannot create the claim under %%s (%%s)\n' "$lock" "$err"
  fi
  exit 0
fi
if err=$(printf '%%s\t%%s\t%%s\t%%s\n' "$me" %d %s "$checkout" >"$lock/owner" 2>&1); then
  printf 'claim\tacquired\n'
else
  rm -rf "$lock" 2>/dev/null
  printf 'claim\tfailed\tthe owner file under %%s could not be written (%%s)\n' "$lock" "$err"
fi`

// releaseTemplate drops this mirror's claim, and only while the owner line is
// still this machine's and this checkout's: a claim that changed hands is not
// this run's to remove. A removal that fails says so with the host's own
// reason, because the next run of this checkout then takes over a claim that
// was supposed to be gone.
const releaseTemplate = `# claim: release
records="$HOME"/%s/.dot-remote
lock="$records"/%s.claim
me=%s
checkout=%s
if [ ! -d "$lock" ]; then exit 0; fi
owner=$(cat "$lock/owner" 2>/dev/null)
if [ "$(printf '%%s' "$owner" | cut -f1)" != "$me" ] || [ "$(printf '%%s' "$owner" | cut -f4)" != "$checkout" ]; then exit 0; fi
if ! err=$(rm -rf "$lock" 2>&1); then
  printf 'release\tfailed\tthe claim under %%s could not be removed (%%s)\n' "$lock" "$err"
fi`

// claimPath is the claim directory of one mirror below the host's $HOME, in the
// form a message spells it out: a record of where a claim is, not a word a shell
// can be handed as it stands - see claimCommand for that.
func (h Host) claimPath(rel string) string {
	return "$HOME/" + h.Root + "/.dot-remote/" + MirrorKey(rel) + ".claim"
}

// claimCommand is one command a person pastes to look at or clear a mirror's
// claim. $HOME is double quoted inside the remote part and that whole part is
// one single-quoted argument, so the host's shell expands it: left bare, the
// shell of the machine the message is printed on would expand it first and ask
// the host about this machine's own home, leaving the claim where it is.
func (h Host) claimCommand(action, rel string) string {
	path := `"$HOME"/` + quotedWord(h.Root) + "/.dot-remote/" + MirrorKey(rel) + ".claim"
	return sshCommand(h, action+" "+path)
}

// sshCommand renders a command a person pastes. The remote part is one
// single-quoted argument so any $HOME in it belongs to the host's shell; h.SSH
// is validated to hold no whitespace and to start with no dash, so it needs no
// quoting of its own.
func sshCommand(h Host, remote string) string {
	return "ssh " + h.SSH + " " + ShellQuote(remote)
}

// quotedWord renders one word to sit inside a command that is itself quoted
// whole: bare when a shell passes it through unchanged, in double quotes with
// what a double-quoted word still acts on escaped otherwise. Either form is one
// word for the shell that reads the message and for the one that runs it.
func quotedWord(word string) string {
	if safeWord.MatchString(word) {
		return word
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(word) + `"`
}

// claimScript is the shell one run's claim becomes. It is built here rather
// than at the call site so the tests can drive the same text through a shell.
func claimScript(h Host, rel, path string, d Deps, replace bool) string {
	mode := "take"
	if replace {
		mode = "replace"
	}
	return fmt.Sprintf(claimTemplate, ShellQuote(h.Root), MirrorKey(rel),
		ShellQuote(d.machine()), ShellQuote(path), mode, os.Getpid(), ShellQuote(d.started()))
}

// inspectScript is the read-only round trip a run makes before anything is sent.
func inspectScript(h Host, rel, path, container string, d Deps) string {
	return fmt.Sprintf(inspectTemplate,
		ShellQuote(container), ShellQuote(h.Runtime), ShellQuote(h.Root), MirrorKey(rel),
		ShellQuote(d.machine()), ShellQuote(path), ShellQuote(rel), MirrorKey(rel), ShellQuote(h.Image()))
}

// releaseScript is the shell that drops this run's claim, if it is still this
// run's.
func releaseScript(h Host, rel, path string, d Deps) string {
	return fmt.Sprintf(releaseTemplate, ShellQuote(h.Root), MirrorKey(rel),
		ShellQuote(d.machine()), ShellQuote(path))
}

// inspect asks the host about this mirror's claim, container and image before
// anything is sent. path is this run's checkout, which is what the claim's owner
// record is compared against: a claim naming this machine and a different
// checkout is not this run's predecessor.
func (d Deps) inspect(ctx context.Context, h Host, rel, path, container string) (inspection, error) {
	script := inspectScript(h, rel, path, container, d)
	out, err := d.capture(ctx, SSHArgv(h.SSH, script), nil)
	if err != nil {
		return inspection{}, fmt.Errorf("checking %s on %s: %w", rel, h.Name, err)
	}
	result := inspection{Container: containerAbsent, Image: imageUnknown, Claim: claimFree}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "record":
			result.RecordFailed = fields[1] == "failed"
		case "claim":
			result.Claim = fields[1]
			if len(fields) > 2 {
				result.Detail = strings.Join(fields[2:], " ")
			}
		case "container":
			result.Container = fields[1]
			if len(fields) > 2 {
				result.ContainerDetail = strings.Join(fields[2:], " ")
			}
		case "image":
			result.Image = fields[1]
		case "user":
			result.ImageUser = fields[1]
		}
	}
	return result, nil
}

// claimMirror takes this mirror's claim on the host. The directory is the whole
// protocol: mkdir is atomic, the owner file says who holds it, and the removal
// on every exit path is what makes a leftover mean a dead run.
func (d Deps) claimMirror(ctx context.Context, h Host, rel, path string, replace bool) error {
	script := claimScript(h, rel, path, d, replace)
	out, err := d.capture(ctx, SSHArgv(h.SSH, script), nil)
	if err != nil {
		return fmt.Errorf("claiming the mirror %s on %s: %w", rel, h.Name, err)
	}
	state, detail := parseClaimAnswer(out)
	switch state {
	case claimAcquired:
		return nil
	case claimLostRace:
		return deferFor(PathScope(path), fmt.Sprintf("another run took the claim on %s on %s while this run was checking it, so that run is using the mirror; wait for it or remove it with `%s`",
			h.MirrorDir(rel), h.Name, h.claimCommand("rm -rf", rel)), time.Minute)
	case claimForeign:
		// The claim changed hands between the check and the take: the owner is
		// read again inside the take, so this is the record it found.
		return deferFor(PathScope(path), claimHeldElsewhere(h.MirrorDir(rel), h, rel, detail), time.Minute)
	default:
		return fmt.Errorf("claiming the mirror %s on %s failed: %s; nothing was synced, so look at the claim with `%s` before trying again",
			rel, h.Name, detail, h.claimCommand("ls -la", rel))
	}
}

// releaseClaim drops this mirror's claim, and reports rather than hides a
// failure. The text names the claim and the two commands that clear it: a
// removal that failed may have taken the owner file and left the directory, and
// a later run reads that as a claim it cannot read and defers instead of taking
// it over, so promising a takeover here would be wrong for exactly the failure
// this message is about.
func (d Deps) releaseClaim(ctx context.Context, h Host, rel, path string) {
	script := releaseScript(h, rel, path, d)
	out, err := d.capture(context.WithoutCancel(ctx), SSHArgv(h.SSH, script), nil)
	detail, failed := parseReleaseAnswer(out)
	inspect, clear := h.claimCommand("ls -la", rel), h.claimCommand("rm -rf", rel)
	switch {
	case err != nil:
		fmt.Fprintf(d.errOut(), "dot remote: could not release the claim on %s on %s (%v); it is at %s, so look at it with `%s` and clear it with `%s`\n",
			rel, h.Name, err, h.claimPath(rel), inspect, clear)
	case failed:
		fmt.Fprintf(d.errOut(), "dot remote: could not release the claim on %s on %s (%s); it is at %s, so look at it with `%s` and clear it with `%s`\n",
			rel, h.Name, detail, h.claimPath(rel), inspect, clear)
	}
}

// parseClaimAnswer reads the state and detail a claim script printed.
func parseClaimAnswer(out string) (string, string) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 2 || fields[0] != "claim" {
			continue
		}
		state := fields[1]
		if len(fields) > 2 {
			return state, strings.Join(fields[2:], " ")
		}
		return state, ""
	}
	return "", "the host printed no claim answer"
}

// parseReleaseAnswer reads the failure a release script printed, if any.
func parseReleaseAnswer(out string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) >= 2 && fields[0] == "release" && fields[1] == "failed" {
			return strings.Join(fields[2:], " "), true
		}
	}
	return "", false
}

// removeContainerStrict removes one container and reports anything short of a
// container that is gone: "could not remove" rather than a claim of removal.
//
// A failed rm is settled by asking the host whether the name is still listed,
// because the container is started with --rm and may already have been reaped
// when the connection dropped - a removal that had nothing left to do is not a
// failure, and calling it one would keep a claim a person then has to clear by
// hand. The context is detached from the run's own: an interrupted run still
// has to clean up after itself.
func (d Deps) removeContainerStrict(ctx context.Context, h Host, container string) error {
	ctx = context.WithoutCancel(ctx)
	_, rmErr := d.capture(ctx, SSHArgv(h.SSH, h.Runtime+" rm -f "+ShellQuote(container)), nil)
	if rmErr == nil {
		return nil
	}
	left, listErr := d.capture(ctx, SSHArgv(h.SSH, containerPresentScript(h, container)), nil)
	switch {
	case listErr == nil && strings.TrimSpace(left) == "":
		return nil
	case listErr != nil:
		return fmt.Errorf("could not remove the container %s on %s (%v, and %s could not confirm it is gone either: %v)",
			container, h.Name, rmErr, h.Runtime, listErr)
	default:
		return fmt.Errorf("could not remove the container %s on %s (%v)", container, h.Name, rmErr)
	}
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

// machine is this machine's name, written into the host-side claim so a later
// run can tell its own dead predecessor from another machine's live run.
func (d Deps) machine() string {
	if d.Machine != "" {
		return d.Machine
	}
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "unknown-machine"
	}
	return name
}

// started is this process's start time, in the claim's owner line, for the
// person reading it on the host.
func (d Deps) started() string {
	if d.Started != "" {
		return d.Started
	}
	return d.now().UTC().Format(time.RFC3339)
}
