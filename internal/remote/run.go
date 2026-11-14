package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
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
	// Ports are published on the host's loopback and forwarded to the same
	// local port over the run's own ssh connection.
	Ports []int
	// Cgroup delegates a writable cgroup v2 subtree to the command.
	Cgroup bool
}

// Extras are the run options that change the container and the ssh argv.
func (o RunOptions) Extras() RunExtras { return RunExtras{Ports: o.Ports, Cgroup: o.Cgroup} }

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
	if isCheckout {
		// git lists a file wherever it is tracked or simply not ignored, and a
		// file list makes rsync test only the listed name, so a file under a
		// directory the exclude list names (.maru, node_modules, target) would
		// travel. Those directories never leave this machine, tracked or not.
		listing, err = d.withoutExcludedParents(ctx, rsyncPath, path, cfg.Excludes(), listing)
		if err != nil {
			return err
		}
	}
	decision := d.syncInput(ctx, h, mirror, rel, path, SyncInput{Excludes: cfg.Excludes(), Listing: listing})
	if err := d.clearReplaced(ctx, h, mirror, d.sendable(ctx, rsyncPath, path, decision.Input.Excludes, decision.Replaced)); err != nil {
		return err
	}
	if err := d.sync(ctx, h, mirror, rsyncPath, path, decision.Input); err != nil {
		return err
	}
	// Recorded only after the mirror took the tree, and only when the decision
	// was reached: a listing saved for a sync that failed, or for a reconcile
	// that did not complete, would let the next run subtract from a record that
	// never described what is on the mirror.
	if isCheckout && decision.Record {
		d.recordSent(h, mirror, rel, path, listing)
	}
	fmt.Fprintf(d.errOut(), "dot remote: running in %s on %s (mirror %s)\n", container, h.Name, mirror)
	exit, sig, runErr := d.ex()(ctx, RunSSHArgv(h, opts.Ports, RunCommandWith(h, container, rel, opts.Command, opts.Extras())), d.in(), d.out(), d.errOut())
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

// syncDecision is what one sync of a checkout decided: the plan's input, and
// whether this run may record what it sent.
//
// I record only a run that either subtracted from its own record or reconciled
// the mirror completely. A run that could not reconcile deletes nothing there,
// and recording it anyway would spend the one chance: the next run would subtract
// from a listing that never covered the stale tree, and the file this one failed
// to see would never be a candidate again.
type syncDecision struct {
	Input SyncInput
	// Record is false when the reconcile did not complete, or when the path is
	// not a checkout and has no listing to record.
	Record bool
	// Replaced are the listed files and symlinks the mirror may still hold as a
	// directory, NUL-separated: the ones this machine did not send as they are
	// last time. clearReplaced removes such a directory before the passes run.
	Replaced []byte
}

// syncInput decides what one sync of a checkout removes.
//
// git's listing says what the tree holds now; what this machine sent to this
// mirror last time says what it used to hold, and the difference that is gone
// from disk is what leaves the mirror. Nothing is inferred from an ignore file,
// because rsync cannot read one the way git wrote it.
//
// A mirror this machine has no record of - its first run here, or a record that
// describes another mirror - is reconciled against its own contents instead:
// what it holds, this checkout neither lists nor has, and git does not ignore is
// gone, so an adopted mirror loses the tree an older checkout left while its own
// build outputs stay. A path git still lists but this machine no longer has on
// disk is gone too - `rm old.go` without staging leaves it listed, and the ship
// pass skips it rather than sending it.
func (d Deps) syncInput(ctx context.Context, h Host, mirror, rel, path string, in SyncInput) syncDecision {
	if in.Listing == nil {
		fmt.Fprintf(d.errOut(), "dot remote: %s is not a git checkout, mirroring it with the exclude list\n", path)
		return syncDecision{Input: in}
	}
	// A path git still lists under a directory the checkout turned into a file
	// or a symlink (the replacement is not staged yet) cannot be sent: rsync
	// answers "Not a directory" and fails the whole ship pass.
	in.Listing = shipListing(path, in.Listing)
	record, err := d.readSent(h, rel, path)
	switch {
	case err != nil:
		fmt.Fprintf(d.errOut(), "dot remote: could not read what this machine last sent to %s on %s (%v), so this run reconciles the mirror instead\n",
			mirror, h.Name, err)
	case record.Stale:
		fmt.Fprintf(d.errOut(), "dot remote: the record of what this machine sent to %s on %s was written for another mirror, so this run reconciles this one instead\n",
			mirror, h.Name)
	case record.Found:
		in.Deletes = withoutBlockedPaths(path, goneFromDisk(path, record.Listing, in.Listing))
		return syncDecision{Input: in, Record: true, Replaced: replacedCandidates(path, in.Listing, record.Listing)}
	}
	deletes, rerr := d.reconcile(ctx, h, mirror, path, in.Listing)
	if rerr != nil {
		fmt.Fprintf(d.errOut(), "dot remote: could not read what %s holds on %s (%v), so this run deletes nothing there and records nothing: the next run there reconciles the mirror again\n",
			mirror, h.Name, rerr)
		return syncDecision{Input: in, Replaced: replacedCandidates(path, in.Listing, nil)}
	}
	in.Deletes = withoutBlockedPaths(path, deletes)
	return syncDecision{Input: in, Record: true, Replaced: replacedCandidates(path, in.Listing, nil)}
}

// rsyncKeeps asks rsync itself which of the given names (NUL-separated, files
// or directories under src) its filter rules let through: a dry run of that
// list under the send pass's own exclude arguments, against an empty
// directory. rsync's own answer is the only one that agrees with rsync on
// anchors, `**`, directory-only patterns, the .env.example include and rule
// order.
//
// A name rsync would have to escape in its output (a control character or a
// backslash) is asked on its own: with one name in the list, any output at all
// is its answer, so the printed form is never read as another file's name.
func (d Deps) rsyncKeeps(ctx context.Context, rsyncPath, src string, excludes []string, names []byte) (map[string]bool, error) {
	kept := map[string]bool{}
	if len(names) == 0 {
		return kept, nil
	}
	empty, err := os.MkdirTemp("", "dot-remote-dry-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(empty)
	// --no-implied-dirs: every name is judged as itself. With implied
	// directories rsync prints a directory that another listed path sits under
	// even when a rule excludes it, which would hide exactly the answer wanted.
	argv := append([]string{rsyncPath, "-a", "-n", "--no-implied-dirs", "--out-format=%n"}, ExcludeArgs(excludes)...)
	argv = append(argv, "--files-from=-", "--from0", "--ignore-missing-args", src+"/", empty+"/")
	out, err := d.capture(ctx, argv, names)
	if err != nil {
		return nil, err
	}
	printed := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		printed[strings.TrimSuffix(line, "/")] = true
	}
	for _, name := range strings.Split(string(names), "\x00") {
		if name == "" {
			continue
		}
		if !plainName(name) {
			one, err := d.rsyncKeepsOne(ctx, rsyncPath, src, excludes, name)
			if err != nil {
				return nil, err
			}
			if one {
				kept[name] = true
			}
			continue
		}
		if printed[strings.TrimSuffix(name, "/")] {
			kept[name] = true
		}
	}
	return kept, nil
}

// rsyncKeepsOne judges one name rsync cannot print as itself: a dry run of that
// single name, where any output at all means its filter rules let it through.
func (d Deps) rsyncKeepsOne(ctx context.Context, rsyncPath, src string, excludes []string, name string) (bool, error) {
	empty, err := os.MkdirTemp("", "dot-remote-dry-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(empty)
	argv := append([]string{rsyncPath, "-a", "-n", "--no-implied-dirs", "--out-format=%n"}, ExcludeArgs(excludes)...)
	argv = append(argv, "--files-from=-", "--from0", "--ignore-missing-args", src+"/", empty+"/")
	out, err := d.capture(ctx, argv, []byte(name+"\x00"))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// plainName reports whether rsync prints a name as it is.
func plainName(name string) bool {
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	return true
}

// sendable keeps the candidates the send pass would really send. A path the
// exclude list covers is never sent, so there is nothing to make way for and
// what the host holds there is not this run's to remove. A dry run that fails
// keeps nothing, which removes nothing.
func (d Deps) sendable(ctx context.Context, rsyncPath, src string, excludes []string, candidates []byte) []byte {
	kept, err := d.rsyncKeeps(ctx, rsyncPath, src, excludes, candidates)
	if err != nil {
		fmt.Fprintf(d.errOut(), "dot remote: could not check which replaced paths this run sends (%v), so it removes no directory on the host first\n", err)
		return nil
	}
	var out []byte
	for _, rel := range strings.Split(string(candidates), "\x00") {
		if kept[rel] {
			out = append(out, rel...)
			out = append(out, 0)
		}
	}
	return out
}

// withoutExcludedParents drops every listed path that lies under a directory
// the exclude list covers. The directories above the listed paths go through
// the same rsync dry run, so "covered" means what it means to rsync; a failure
// stops the run, because sending the unfiltered list could send a secret.
func (d Deps) withoutExcludedParents(ctx context.Context, rsyncPath, src string, excludes []string, listing []byte) ([]byte, error) {
	seen := map[string]bool{}
	var dirs []string
	for _, rel := range strings.Split(string(listing), "\x00") {
		for i := strings.IndexByte(rel, '/'); i >= 0; {
			dir := rel[:i]
			if !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
			}
			next := strings.IndexByte(rel[i+1:], '/')
			if next < 0 {
				break
			}
			i += next + 1
		}
	}
	if len(dirs) == 0 {
		return listing, nil
	}
	kept, err := d.rsyncKeeps(ctx, rsyncPath, src, excludes, []byte(strings.Join(dirs, "\x00")+"\x00"))
	if err != nil {
		return nil, fmt.Errorf("could not check the listed paths of %s against the exclude list (%w), so nothing is sent: run `%s --version` and retry", src, err, rsyncPath)
	}
	out := make([]byte, 0, len(listing))
	var dropped int
	for _, rel := range strings.Split(string(listing), "\x00") {
		if rel == "" {
			continue
		}
		excluded := false
		for i := strings.IndexByte(rel, '/'); i >= 0; {
			// A directory that is gone from disk is not printed either; its
			// files are gone with it and the send pass skips them anyway.
			if dir := rel[:i]; !kept[dir] {
				if _, serr := os.Lstat(filepath.Join(src, filepath.FromSlash(dir))); serr == nil {
					excluded = true
					break
				}
			}
			next := strings.IndexByte(rel[i+1:], '/')
			if next < 0 {
				break
			}
			i += next + 1
		}
		if excluded {
			dropped++
			continue
		}
		out = append(out, rel...)
		out = append(out, 0)
	}
	if dropped > 0 {
		fmt.Fprintf(d.errOut(), "dot remote: left %d listed file(s) at home: they are under a directory the exclude list names\n", dropped)
	}
	return out, nil
}

// clearReplaced removes, on the host, each directory the checkout has replaced
// with a file or a symlink.
//
// rsync cannot do it: the ship pass carries the exclude list, an exclude also
// protects on the receiving side, and a directory holding a target/ or a
// node_modules/ the host built is therefore never empty enough for --force to
// replace. The checkout has decided that path is not a directory any more, so
// the directory goes whole, by name, and nothing else is touched.
func (d Deps) clearReplaced(ctx context.Context, h Host, mirror string, candidates []byte) error {
	if len(candidates) == 0 {
		return nil
	}
	var removed []byte
	var err error
	if d.ClearReplaced != nil {
		removed, err = d.ClearReplaced(ctx, h, mirror, candidates)
	} else {
		var out string
		out, err = d.capture(ctx, SSHArgv(h.SSH, replacedDirsCommand(mirror)), candidates)
		removed = []byte(out)
	}
	if err != nil {
		return fmt.Errorf("could not remove the directories this checkout replaced with files under %s on %s (%w): look at the mirror with `%s` and remove the directory that is now a file here",
			mirror, h.Name, err, sshCommand(h, `ls -la "$HOME"/`+quotedWord(mirror)))
	}
	for _, rel := range strings.Split(string(removed), "\x00") {
		if rel != "" {
			fmt.Fprintf(d.errOut(), "dot remote: removed the directory %s on %s, which the mirrored path now has as a file\n", mirror+"/"+rel, h.Name)
		}
	}
	return nil
}

// reconcile is the deletion set for a mirror this machine has no record of: what
// the mirror holds that this checkout neither lists nor has, minus what git's
// ignore rules cover, together with what git still lists and this machine no
// longer has on disk.
func (d Deps) reconcile(ctx context.Context, h Host, mirror, path string, listing []byte) ([]byte, error) {
	held, err := d.mirrorFiles(ctx, h, mirror)
	if err != nil {
		return nil, err
	}
	candidates := mirrorCandidates(path, held, listing)
	var ignored map[string]bool
	if len(candidates) > 0 {
		ignored, err = d.gitIgnored(ctx, path, candidates)
		if err != nil {
			return nil, err
		}
	}
	return nulUnion(deletionSet(candidates, ignored), goneFromDisk(path, listing)), nil
}

// mirrorFiles lists every file one mirror holds, NUL-separated and relative to
// the mirror. A directory that cannot be entered reads as an empty mirror, which
// deletes nothing.
func (d Deps) mirrorFiles(ctx context.Context, h Host, mirror string) ([]byte, error) {
	if d.MirrorFiles != nil {
		return d.MirrorFiles(ctx, h, mirror)
	}
	out, err := d.capture(ctx, SSHArgv(h.SSH, mirrorFindCommand(mirror)), nil)
	if err != nil {
		return nil, err
	}
	return []byte(strings.Join(mirrorPaths([]byte(out)), "\x00")), nil
}

// gitIgnored asks git which of the given paths its ignore rules cover in this
// checkout. Every ignore source git honors is read - .gitignore at any depth,
// .git/info/exclude and the global excludes file - with git's own semantics, a
// `!` negation included.
func (d Deps) gitIgnored(ctx context.Context, path string, candidates []string) (map[string]bool, error) {
	if d.Ignored != nil {
		return d.Ignored(ctx, path, candidates)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := osexec.CommandContext(ctx, "git", append([]string{"-C", path}, gitIgnoreArgs()...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd.Stdin = strings.NewReader(strings.Join(candidates, "\x00") + "\x00")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// check-ignore exits 1 when no path is ignored, and that is an answer.
		var exitErr *osexec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("git check-ignore in %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
		}
	}
	ignored := map[string]bool{}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" {
			ignored[rel] = true
		}
	}
	return ignored, nil
}

// sync removes what left the checkout and then sends git's view of the tree,
// protecting the mirror's own build outputs by never naming them.
func (d Deps) sync(ctx context.Context, h Host, mirror, rsyncPath, path string, in SyncInput) error {
	target := h.SSH + ":" + mirror
	for _, step := range SyncPlan(rsyncPath, path, target, in) {
		err := d.runStep(ctx, step)
		if err == nil {
			continue
		}
		// A path that is not a checkout gets one plain --delete pass, which
		// cannot make way for a file where the mirror holds a directory with an
		// excluded name in it. The checkout has decided that path is not a
		// directory any more, so the directory goes by name, as the git path's
		// delete pass removes it, and the pass runs once more.
		if in.Listing == nil {
			if named := staleDirs(err, path); len(named) > 0 {
				if cerr := d.clearReplaced(ctx, h, mirror, named); cerr != nil {
					return fmt.Errorf("mirroring %s to %s: %w", path, h.Name, cerr)
				}
				if rerr := d.runStep(ctx, step); rerr != nil {
					return fmt.Errorf("mirroring %s to %s: %w; %s", path, h.Name, rerr, syncNextStep)
				}
				continue
			}
		}
		return fmt.Errorf("mirroring %s to %s: %w; %s", path, h.Name, err, syncNextStep)
	}
	return nil
}

// syncNextStep is what a caller can do about a failed pass on its own: the plan
// it would send names every path rsync decided on.
const syncNextStep = "rerun with --dry-run to print the plan this pass would send"

// runStep runs one rsync step and applies the tolerance that step's own input
// gives its status.
func (d Deps) runStep(ctx context.Context, step Step) error {
	_, exit, err := d.captureExit(ctx, step.Argv, step.Stdin)
	return stepError(step, exit, err)
}

// staleDirMarkers are the lines rsync names a directory it could not make way
// for. The way-making refusal is on stderr with the rest of the diagnostics; the
// deletion refusal it follows goes to stdout, and is accepted here in case a
// version moves it.
var staleDirMarkers = []string{
	"could not make way for new regular file: ",
	"cannot delete non-empty directory: ",
}

// staleDirs reads the paths rsync named as a directory it could not delete, and
// keeps only those this checkout now has as something other than a directory:
// the mirror's directory has to give way to a file, and a path the checkout
// still has as a directory is not a run's to remove. A name rsync had to escape
// in its message, or one that does not stay inside the mirror, is left alone.
func staleDirs(err error, src string) []byte {
	var failure *cmdFailure
	if !errors.As(err, &failure) {
		return nil
	}
	var out []byte
	for _, line := range strings.Split(failure.Stderr, "\n") {
		line = strings.TrimSpace(line)
		rel, ok := "", false
		for _, marker := range staleDirMarkers {
			if rel, ok = strings.CutPrefix(line, marker); ok {
				break
			}
		}
		if !ok || !plainName(rel) || !safeRelPath(rel) {
			continue
		}
		info, ierr := os.Lstat(filepath.Join(src, filepath.FromSlash(rel)))
		if ierr != nil || info.IsDir() {
			continue
		}
		out = append(out, rel...)
		out = append(out, 0)
	}
	return out
}

// stepError is what one rsync step's outcome means: nothing when it succeeded or
// exited with the status that step's own input makes benign, and its failure
// otherwise. The delete pass names paths the checkout no longer has, so rsync's
// "some files vanished" (24) is that step's expected answer when a parent
// directory went with them; anywhere else the same status is a failure.
func stepError(step Step, exit int, err error) error {
	if err == nil || (step.Tolerate != 0 && exit == step.Tolerate) {
		return nil
	}
	return err
}

// describe prints what a dry run would do, without touching the host.
func (d Deps) describe(opts RunOptions, h Host, path, rel, mirror, container string) {
	fmt.Fprintf(d.out(), "would mirror %s to %s:%s (container %s)\n", path, h.SSH, mirror, container)
	fmt.Fprintf(d.out(), "would run in %s on %s:\n  %s\n",
		container, h.Name, strings.Join(RunSSHArgv(h, opts.Ports, RunCommandWith(h, container, rel, opts.Command, opts.Extras())), " "))
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
