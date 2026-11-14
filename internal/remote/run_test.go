package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// preflightAbsent is the host's ordinary answer: no claim and no container for
// this mirror, the image is there, and it runs as an unprivileged user.
const preflightAbsent = "claim\tfree\ncontainer\tabsent\nimage\tknown\nuser\tdev\n"

// fakeExec stands in for the real execer: it records every argv and answers the
// shapes a run sends (the load probe, the inspection, the claim, rsync, the
// container run, the container removal and the claim's release).
type fakeExec struct {
	t *testing.T
	// loads is what the load probe reads.
	loads string
	// probeErr makes the load probe fail instead of answering.
	probeErr error
	// preflight is what the host answers about the claim, the container and the
	// image.
	preflight string
	// preflightErr makes the inspection fail, as an unreachable host would.
	preflightErr error
	// claim is what the host answers when the run takes its claim with a mkdir.
	claim string
	// claimExit makes the claim call itself fail.
	claimExit int
	// removeErr makes the container removal fail.
	removeErr error
	// containerGone says the host no longer lists the container when a failed
	// removal is settled, as it is when --rm already reaped it.
	containerGone bool
	// presentErr makes that settling query fail too.
	presentErr error
	// releaseOut is what the release script prints, so a failed release can be
	// driven without a shell.
	releaseOut string
	// imageUser is what the image reports for its own user; empty means an
	// unprivileged "dev".
	imageUser string
	// imageErr makes the image inspect fail, as a missing image would.
	imageErr error
	// exit and sig are what the container run itself reports.
	exit int
	sig  syscall.Signal
	// output is what the container run writes to stdout.
	output string
	// progress is what a build or a pull narrates on stdout.
	progress string
	calls    [][]string
	stdin    [][]byte
}

func (f *fakeExec) execute(_ context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, syscall.Signal, error) {
	f.calls = append(f.calls, argv)
	var fed []byte
	if stdin != nil {
		fed, _ = io.ReadAll(stdin)
	}
	f.stdin = append(f.stdin, fed)
	command := ""
	if len(argv) > 0 {
		command = argv[len(argv)-1]
	}
	switch {
	case len(argv) > 1 && argv[0] == "rsync" && slices.Contains(argv, "--out-format=%n"):
		// The dry run that asks rsync what it would send: every name it was fed.
		fmt.Fprint(stdout, strings.ReplaceAll(string(fed), "\x00", "\n"))
		return 0, 0, nil
	case len(argv) > 0 && argv[0] == "rsync":
		return 0, 0, nil
	case strings.Contains(command, "loadavg"):
		if f.probeErr != nil {
			return 0, 0, f.probeErr
		}
		fmt.Fprint(stdout, f.loads)
		return 0, 0, nil
	case strings.HasPrefix(command, "docker rm -f"):
		if f.removeErr != nil {
			return 1, 0, f.removeErr
		}
		return 0, 0, nil
	case strings.Contains(command, "# container-present"):
		if f.presentErr != nil {
			return 1, 0, f.presentErr
		}
		if !f.containerGone {
			fmt.Fprintln(stdout, containerNamed(command))
		}
		return 0, 0, nil
	case strings.Contains(command, "# replaced-dirs"):
		return 0, 0, nil
	case strings.Contains(command, "claim: release"):
		fmt.Fprint(stdout, f.releaseOut)
		return 0, 0, nil
	case strings.Contains(command, "claim: take"):
		if f.claimExit != 0 {
			return f.claimExit, 0, nil
		}
		answer := f.claim
		if answer == "" {
			answer = "claim\tacquired\n"
		}
		fmt.Fprint(stdout, answer)
		return 0, 0, nil
	case strings.Contains(command, " build -t ") || strings.Contains(command, " pull "):
		fmt.Fprint(stdout, f.progress)
		return 0, 0, nil
	case strings.Contains(command, "ps -a --filter"):
		if f.preflightErr != nil {
			return 0, 0, f.preflightErr
		}
		fmt.Fprint(stdout, f.preflight)
		return 0, 0, nil
	case strings.HasPrefix(command, "docker image inspect"):
		if f.imageErr != nil {
			return 1, 0, nil
		}
		user := f.imageUser
		if user == "" {
			user = "dev"
		}
		fmt.Fprintln(stdout, user)
		return 0, 0, nil
	default:
		fmt.Fprint(stdout, f.output)
		return f.exit, f.sig, nil
	}
}

// containerNamed reads the container name out of a `name=^<container>$` filter.
func containerNamed(command string) string {
	_, after, ok := strings.Cut(command, "name=^")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(after, "$")
	return name
}

// probed reports whether the load probe ran, and what it answered.
func (f *fakeExec) probed() int {
	count := 0
	for _, argv := range f.calls {
		if strings.Contains(argv[len(argv)-1], "loadavg") {
			count++
		}
	}
	return count
}

// transfers lists the rsync argvs, in order.
func (f *fakeExec) transfers() [][]string {
	var out [][]string
	for _, argv := range f.calls {
		// The dry run that asks rsync what it would send transfers nothing.
		if len(argv) > 0 && argv[0] == "rsync" && !slices.Contains(argv, "--out-format=%n") {
			out = append(out, argv)
		}
	}
	return out
}

// preflights lists the container-and-image checks, in order.
func (f *fakeExec) preflights() []string {
	var out []string
	for _, argv := range f.calls {
		if strings.Contains(argv[len(argv)-1], "ps -a --filter") {
			out = append(out, argv[len(argv)-1])
		}
	}
	return out
}

// runArgv returns the container run's argv, or nil when it never started.
func (f *fakeExec) runArgv() []string {
	for _, argv := range f.calls {
		command := argv[len(argv)-1]
		if strings.Contains(command, " run --rm ") {
			return argv
		}
	}
	return nil
}

// stin is the stdin one recorded argv was fed.
func (f *fakeExec) stin(argv []string) []byte {
	want := strings.Join(argv, "\x00")
	for i, call := range f.calls {
		if strings.Join(call, "\x00") == want {
			return f.stdin[i]
		}
	}
	f.t.Fatalf("no recorded call for %v", argv)
	return nil
}

// removals lists the standalone "rm -f" commands, which is how a run cleans up
// a container whose connection failed. The preflight's own removal is inside
// its script and is asserted through the host's answer instead.
func (f *fakeExec) removals() []string {
	var out []string
	for _, argv := range f.calls {
		command := argv[len(argv)-1]
		if strings.HasPrefix(command, "docker rm -f") {
			out = append(out, command)
		}
	}
	return out
}

// order is the index of the first call one of whose arguments contains want, or
// -1.
func (f *fakeExec) order(want string) int {
	for i, argv := range f.calls {
		for _, arg := range argv {
			if strings.Contains(arg, want) {
				return i
			}
		}
	}
	return -1
}

// runFixture is one configured host, one checkout and the seams a run uses.
type runFixture struct {
	t        *testing.T
	root     string
	checkout string
	cfg      Config
	host     Host
	store    *admission.Store
	exec     *fakeExec
	gitData  []byte
	gitErr   error
	gitDirs  []string
	// gitInside answers `git rev-parse --is-inside-work-tree`; empty means "true".
	gitInside    string
	gitInsideErr error
	// gitToplevel answers `git rev-parse --show-toplevel`; empty means "not a
	// repository".
	gitToplevel    string
	gitToplevelErr error
	// mirrorHeld is what the mirror already holds, for a run that has no record
	// of it, ignored is what git answers for those paths, and mirrorErr makes the
	// first listing of the mirror fail.
	mirrorHeld []string
	mirrorErr  error
	ignored    map[string]bool
	out        bytes.Buffer
	errOut     bytes.Buffer
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	// EvalSymlinks first: a run resolves the checkout, so an unresolved /var on
	// macOS would put the checkout outside the configured local_root.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(root, "repo")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &runFixture{
		t:        t,
		root:     root,
		checkout: checkout,
		host:     testHost(),
		store:    admission.NewStore(filepath.Join(root, "admission"), exec.NewProbeRunner()),
		exec:     &fakeExec{t: t, loads: "0.50 0.40 0.30 1/100 4242\n4\n", preflight: preflightAbsent},
		gitData:  []byte("main.go\x00docs/a.md\x00"),
	}
	// The checkout holds what its listing names: a run deletes a listed path that
	// is gone from disk, so a fixture whose listing named files it does not have
	// would put them on every delete pass and hide the one under test.
	for _, rel := range nulList(f.gitData) {
		path := filepath.Join(checkout, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.cfg = Config{Hosts: map[string]Host{f.host.Name: f.host}, LocalRoot: root}
	return f
}

func (f *runFixture) deps() Deps {
	return Deps{
		Store:      f.store,
		Runner:     exec.NewProbeRunner(),
		Exec:       f.exec.execute,
		Out:        &f.out,
		Err:        &f.errOut,
		LocalRsync: "rsync",
		// A first run reconciles the mirror before it syncs: the mirror's own
		// files come from the fixture, and git's answer about what it ignores is
		// the fake here, so no real repository is needed.
		MirrorFiles: func(_ context.Context, _ Host, _ string) ([]byte, error) {
			if f.mirrorErr != nil {
				err := f.mirrorErr
				f.mirrorErr = nil
				return nil, err
			}
			return []byte(strings.Join(f.mirrorHeld, "\x00")), nil
		},
		Ignored: func(_ context.Context, _ string, paths []string) (map[string]bool, error) {
			ignored := map[string]bool{}
			for _, rel := range paths {
				if f.ignored[rel] {
					ignored[rel] = true
				}
			}
			return ignored, nil
		},
		Git: func(_ context.Context, dir string, args ...string) ([]byte, error) {
			f.gitDirs = append(f.gitDirs, dir)
			if len(args) > 0 && args[0] == "ls-files" {
				if f.gitErr != nil {
					return nil, f.gitErr
				}
				return f.gitData, nil
			}
			switch strings.Join(args, " ") {
			case "rev-parse --is-inside-work-tree":
				if f.gitInsideErr != nil {
					return nil, f.gitInsideErr
				}
				if f.gitInside == "" {
					return []byte("true\n"), nil
				}
				return []byte(f.gitInside + "\n"), nil
			case "rev-parse --show-toplevel":
				if f.gitToplevelErr != nil {
					return nil, f.gitToplevelErr
				}
				if f.gitToplevel == "" {
					return nil, errors.New("exit status 128: fatal: not a git repository (or any of the parent directories): .git")
				}
				return []byte(f.gitToplevel + "\n"), nil
			}
			return nil, fmt.Errorf("unexpected git %v", args)
		},
	}
}

func (f *runFixture) run(opts RunOptions) error {
	f.t.Helper()
	if opts.Path == "" {
		opts.Path = f.checkout
	}
	if opts.HostName == "" {
		opts.HostName = f.host.Name
	}
	return f.deps().Run(context.Background(), f.cfg, opts)
}

// leases lists what is still held after the run, which has to be nothing.
func (f *runFixture) leases() []admission.Lease {
	f.t.Helper()
	held, err := f.store.ListLeases()
	if err != nil {
		f.t.Fatal(err)
	}
	return held
}

// container is the name a run of this fixture's checkout uses.
func (f *runFixture) container() string { return f.host.ContainerName("repo") }

// selfLease is the identity a caller of the admission store needs: the real
// process's, because a lease this process holds is never reclaimed.
func selfLease(t *testing.T, dir string) admission.Lease {
	t.Helper()
	lease, err := admission.SelfLease(context.Background(), exec.NewProbeRunner(), dir)
	if err != nil {
		t.Skipf("this process's start time is unavailable: %v", err)
	}
	return lease
}

// The order the run fixes: the load probe, the container check, git's listing
// shipped (with whatever the delete pass names), then the run itself. Nothing
// that changes the mirror may happen before the container check.
func TestRunSyncsAndThenRuns(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxLoadRatio = 0.7
	f.cfg = Config{Hosts: map[string]Host{f.host.Name: f.host}, LocalRoot: f.root}
	if err := f.run(RunOptions{Command: []string{"make", "test"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held := f.leases(); len(held) != 0 {
		t.Errorf("leases left behind: %+v", held)
	}
	if got := f.exec.probed(); got != 1 {
		t.Errorf("load probes = %d, want exactly one", got)
	}
	if got := len(f.exec.preflights()); got != 1 {
		t.Errorf("container checks = %d, want exactly one", got)
	}
	if check, transfer := f.exec.order("ps -a --filter"), f.exec.order("--files-from=-"); check < 0 || transfer < 0 || check > transfer {
		t.Errorf("container check at %d, ship pass at %d: the check has to come first", check, transfer)
	}
	// A first run has no record, so it asks the host to clear any directory the
	// checkout now has as a file, before anything is sent.
	if clear, transfer := f.exec.order("# replaced-dirs"), f.exec.order("--force"); clear < 0 || clear > transfer {
		t.Errorf("replaced-directory removal at %d, ship pass at %d: it has to run, and first", clear, transfer)
	}
	// The mirror in this fixture holds nothing, so reconciling it names nothing
	// to delete and the ship pass is the whole sync.
	transfers := f.exec.transfers()
	if len(transfers) != 1 {
		t.Fatalf("rsync calls = %d, want the ship pass alone", len(transfers))
	}
	if !strings.Contains(strings.Join(transfers[0], " "), "--files-from=-") {
		t.Errorf("first pass is not the file list: %v", transfers[0])
	}
	ship := f.exec.stin(transfers[0])
	if string(ship) != string(f.gitData) {
		t.Errorf("ship pass was fed %q, want git's listing %q", ship, f.gitData)
	}
	if len(f.gitDirs) == 0 || f.gitDirs[0] != f.checkout {
		t.Errorf("git read %v, want the checkout %s", f.gitDirs, f.checkout)
	}
	run := f.exec.runArgv()
	if run == nil {
		t.Fatal("the container never started")
	}
	command := run[len(run)-1]
	container := f.container()
	for _, want := range []string{
		"--name " + container,
		"-w /work/repo",
		`-v "$HOME"/'` + f.host.Root + `/repo':'/work/repo'`,
		"--cpus 16",
		"--memory 32g",
		"-e CARGO_BUILD_JOBS=16",
		f.host.Ref + " bash -c 'make test'",
	} {
		if !strings.Contains(command, want) {
			t.Errorf("run command is missing %q:\n%s", want, command)
		}
	}
	if argv := strings.Join(run[:len(run)-1], " "); argv != "ssh -o BatchMode=yes buildbox" {
		t.Errorf("the run was not sent over batch-mode ssh: %s", argv)
	}
	// The run is last of the mirror-changing steps: the mirror has to be in
	// place before it starts, and nothing may touch it after.
	lastChange := -1
	for i, argv := range f.exec.calls {
		if argv[0] == "rsync" || strings.Contains(argv[len(argv)-1], " run --rm ") {
			lastChange = i
		}
	}
	if lastChange != len(f.exec.calls)-2 {
		t.Errorf("the container run was not the last mirror-changing call (index %d of %d)", lastChange, len(f.exec.calls))
	}
	// The claim is held to the very end: the release is the last thing sent.
	if got := f.exec.calls[len(f.exec.calls)-1][len(f.exec.calls[len(f.exec.calls)-1])-1]; !strings.Contains(got, "claim: release") {
		t.Errorf("the run did not release its claim last: %q", got)
	}
}

// A mirror this machine has no record of is reconciled against its own contents
// before the run: the files it holds that this checkout neither lists nor has,
// and that git's ignore rules do not cover, are named on the delete pass, while
// a build output the host made stays where it is.
func TestAFirstRunReconcilesTheMirrorItAdopted(t *testing.T) {
	f := newRunFixture(t)
	f.mirrorHeld = []string{"./Cargo.lock", "./assets/logo.png", "./target/debug/app"}
	f.ignored = map[string]bool{"target/debug/app": true}

	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	transfers := f.exec.transfers()
	if len(transfers) != 2 {
		t.Fatalf("rsync calls = %d, want the delete and the ship pass", len(transfers))
	}
	drop := transfers[0]
	for _, want := range []string{"--files-from=-", "--delete-missing-args"} {
		if !slices.Contains(drop, want) {
			t.Errorf("delete pass is missing %q: %v", want, drop)
		}
	}
	if got := nulList(f.exec.stin(drop)); !slices.Equal(got, []string{"Cargo.lock", "assets/logo.png"}) {
		t.Errorf("delete pass was fed %v, want the paths git does not ignore", got)
	}
	// The ship pass replaces what contained a path that left the checkout, so it
	// goes last and carries --force.
	if !slices.Contains(transfers[1], "--force") {
		t.Errorf("ship pass = %v, want --force", transfers[1])
	}
	// The run records what it sent, so the next run there subtracts from it
	// instead of reading the mirror again.
	if record, err := f.deps().readSent(f.host, "repo", f.checkout); err != nil || !record.Found {
		t.Fatalf("readSent after the run = (%+v, %v), want the listing this run sent", record, err)
	}
}

// A run whose reconcile could not complete deletes nothing there and records
// nothing. Recording anyway would spend the one chance it has: the next run would
// subtract from a listing that never covered the stale tree, so the file this
// run failed to see would never be a candidate again.
func TestARunThatCouldNotReconcileRecordsNothing(t *testing.T) {
	f := newRunFixture(t)
	f.mirrorHeld = []string{"./stale.txt"}
	f.mirrorErr = errors.New("ssh: connect to host buildbox port 22: Connection refused")

	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if transfers := f.exec.transfers(); len(transfers) != 1 {
		t.Errorf("rsync calls = %d, want the ship pass alone: a run that could not read the mirror deletes nothing there", len(transfers))
	}
	if !strings.Contains(f.errOut.String(), "deletes nothing there and records nothing") {
		t.Errorf("stderr does not say the run recorded nothing:\n%s", f.errOut.String())
	}
	if record, err := f.deps().readSent(f.host, "repo", f.checkout); err != nil || record.Found {
		t.Fatalf("the run recorded a listing it could not reconcile against: %+v, %v", record, err)
	}

	// The next run reconciles again - the mirror answers this time - and the file
	// an earlier checkout left finally goes.
	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	transfers := f.exec.transfers()
	if len(transfers) != 3 {
		t.Fatalf("rsync calls = %d, want the first run's ship pass and the second run's two", len(transfers))
	}
	if got := nulList(f.exec.stin(transfers[1])); !slices.Equal(got, []string{"stale.txt"}) {
		t.Errorf("the second run's delete pass was fed %v, want the stale file", got)
	}
}

// A host that sets no load limit has nothing to read for, so it is not probed at
// all: the probe is the gate's evidence, not a routine hello.
func TestAHostWithNoLoadLimitIsNotProbed(t *testing.T) {
	f := newRunFixture(t)
	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.exec.probed(); got != 0 {
		t.Errorf("load probes = %d, want none without max_load_ratio", got)
	}
}

// A second job for the same path defers instead of sharing its mirror, and it
// sends nothing at all: the check happens before the first byte moves.
func TestRunDefersWhenTheMirrorPathIsBusy(t *testing.T) {
	f := newRunFixture(t)
	held, err := AcquirePath(context.Background(), f.store, selfLease(t, f.checkout), f.host, f.checkout, 0)
	if err != nil {
		t.Fatalf("holding the path lease: %v", err)
	}
	defer held.Release()

	err = f.run(RunOptions{Command: []string{"true"}})
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("Run = %v, want a defer while the path is busy", err)
	}
	if deferred.Outcome.Scope != PathScope(f.checkout) || deferred.Outcome.Class != admission.ClassRemote {
		t.Errorf("outcome = %+v, want the path scope and the remote class", deferred.Outcome)
	}
	if !strings.Contains(deferred.Outcome.Reason, "another run of "+f.checkout) {
		t.Errorf("reason = %q", deferred.Outcome.Reason)
	}
	if len(f.exec.calls) != 0 {
		t.Errorf("a deferred run sent %d commands", len(f.exec.calls))
	}
}

// The host's max_runs is a second, host-wide scope: different paths run side by
// side, and the run past the limit defers naming the configured ceiling.
func TestRunDefersWhenTheHostIsAtMaxRuns(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxRuns = 1
	f.cfg = Config{Hosts: map[string]Host{f.host.Name: f.host}, LocalRoot: f.root}
	self := selfLease(t, f.checkout)
	held, err := AcquireHost(context.Background(), f.store, self, f.host, 0, false)
	if err != nil {
		t.Fatalf("holding the host lease: %v", err)
	}
	defer held.Release()

	err = f.run(RunOptions{Command: []string{"true"}})
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("Run = %v, want a defer at max_runs", err)
	}
	if deferred.Outcome.Scope != HostScope(f.host.Name) {
		t.Errorf("scope = %q, want the host scope", deferred.Outcome.Scope)
	}
	if !strings.Contains(deferred.Outcome.Reason, "1 runs are already active on buildbox") {
		t.Errorf("reason = %q", deferred.Outcome.Reason)
	}
	if held := f.leases(); len(held) != 1 {
		t.Errorf("the deferred run released the wrong leases: %+v", held)
	}
}

// A loaded host defers before anything is sent, so a run never lands on a host
// that is already saturated.
func TestRunDefersOnHostLoad(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxLoadRatio = 0.5
	f.cfg = Config{Hosts: map[string]Host{f.host.Name: f.host}, LocalRoot: f.root}
	f.exec.loads = "3.00 2.00 1.00 2/100 4242\n4\n"

	err := f.run(RunOptions{Command: []string{"true"}})
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("Run = %v, want a defer on load", err)
	}
	if !strings.Contains(deferred.Outcome.Reason, "host load1 3.00 on 4 CPUs") {
		t.Errorf("reason = %q, want the numbers the probe read", deferred.Outcome.Reason)
	}
	if got := len(f.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls on a host that deferred, want none", got)
	}
	if f.exec.runArgv() != nil {
		t.Error("the container started on a host that deferred")
	}
	if held := f.leases(); len(held) != 0 {
		t.Errorf("leases left behind: %+v", held)
	}
}

// Missing telemetry is not health: an unreadable host defers rather than
// letting a run through.
func TestAnUnreadableLoadProbeDefers(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxLoadRatio = 0.5
	f.cfg = Config{Hosts: map[string]Host{f.host.Name: f.host}, LocalRoot: f.root}
	f.exec.probeErr = errors.New("ssh: connect to host buildbox port 22: Connection refused")

	err := f.run(RunOptions{Command: []string{"true"}})
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("Run = %v, want a defer when the probe cannot read", err)
	}
	if !strings.Contains(deferred.Outcome.Reason, "missing telemetry is not health") {
		t.Errorf("reason = %q", deferred.Outcome.Reason)
	}
	if !strings.Contains(deferred.Outcome.Reason, "Connection refused") {
		t.Errorf("reason = %q, want the probe's own error", deferred.Outcome.Reason)
	}
}

// A bypassed load probe still reads the host and names the load it found, so
// nothing is skipped silently.
func TestBypassingTheLoadProbeStillReadsAndNamesTheLoad(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxLoadRatio = 0.5
	f.cfg = Config{Hosts: map[string]Host{f.host.Name: f.host}, LocalRoot: f.root}
	f.exec.loads = "3.00 2.00 1.00 2/100 4242\n4\n"

	opts := RunOptions{Command: []string{"true"}, Ignore: admission.IgnoreSet{RemoteLoad: true}, IgnoreSrc: "DOT_ADMISSION_IGNORE"}
	if err := f.run(opts); err != nil {
		t.Fatalf("Run with the load bypass = %v", err)
	}
	if got := f.exec.probed(); got != 1 {
		t.Errorf("load probes = %d, want the bypass to read the host anyway", got)
	}
	stderr := f.errOut.String()
	if !strings.Contains(stderr, "admission: remote host load ignored (DOT_ADMISSION_IGNORE)") {
		t.Errorf("stderr does not name the bypass:\n%s", stderr)
	}
	if !strings.Contains(stderr, "load1 3.00 on 4 CPUs") {
		t.Errorf("stderr does not report the load the probe found:\n%s", stderr)
	}
	if f.exec.runArgv() == nil {
		t.Error("the run did not start under the bypass")
	}
}

// A dry run reads the config and prints the plan; it takes no lease and sends
// nothing to the host.
func TestDryRunDescribesTheRunAndSendsNothing(t *testing.T) {
	f := newRunFixture(t)
	if err := f.run(RunOptions{Command: []string{"make", "test"}, DryRun: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.exec.calls) != 0 {
		t.Errorf("a dry run sent %d commands", len(f.exec.calls))
	}
	if held := f.leases(); len(held) != 0 {
		t.Errorf("a dry run took %+v", held)
	}
	stdout := f.out.String()
	if !strings.Contains(stdout, "would mirror "+f.checkout+" to buildbox:dot-remote/repo") {
		t.Errorf("stdout does not describe the mirror:\n%s", stdout)
	}
	if !strings.Contains(stdout, `-v "$HOME"/'dot-remote/repo':'/work/repo'`) {
		t.Errorf("stdout does not print the run command:\n%s", stdout)
	}
}

// A path that is not a git checkout still mirrors, with the exclude list
// standing in for git's idea of what stays home.
func TestANonGitCheckoutFallsBackToTheExcludeList(t *testing.T) {
	f := newRunFixture(t)
	f.gitErr = errors.New("exit status 128: fatal: not a git repository (or any of the parent directories): .git")
	f.gitInsideErr = f.gitErr
	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	transfers := f.exec.transfers()
	if len(transfers) != 1 {
		t.Fatalf("rsync calls = %d, want one plain mirroring pass", len(transfers))
	}
	joined := strings.Join(transfers[0], " ")
	if !strings.Contains(joined, "--delete") || strings.Contains(joined, "--files-from=-") {
		t.Errorf("plain pass = %s, want --delete and no file list", joined)
	}
	if !strings.Contains(f.errOut.String(), "is not a git checkout, mirroring it with the exclude list") {
		t.Errorf("stderr does not say why the transfer differs:\n%s", f.errOut.String())
	}
}

// A failed pass reports the lines above rsync's summary, not the summary alone,
// and names what the caller can do next.
func TestAFailedPassReportsEveryDiagnosticLine(t *testing.T) {
	src := t.TempDir()
	d := Deps{Err: io.Discard, Exec: func(_ context.Context, _ []string, _ io.Reader, _, stderr io.Writer) (int, syscall.Signal, error) {
		fmt.Fprintln(stderr, "could not make way for new regular file: pkg")
		fmt.Fprintln(stderr, "rsync error: some files/attrs were not transferred (see previous errors) (code 23) at main.c(1416)")
		return 23, 0, nil
	}}
	err := d.sync(context.Background(), testHost(), "/mirror", "rsync", src, SyncInput{Listing: []byte("a.txt\x00")})
	if err == nil {
		t.Fatal("a failed pass succeeded")
	}
	for _, want := range []string{"could not make way for new regular file: pkg", "some files/attrs were not transferred", "--dry-run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%s", want, err)
		}
	}
}

// The excerpt a failed pass reports is bounded: the diagnostics above the
// summary, never a page of them.
func TestAFailedPassKeepsAtMostFiveStderrLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	got := tailLines(b.String(), stderrLines)
	if strings.Contains(got, "line 1\n") || strings.Contains(got, "line 3\n") ||
		!strings.Contains(got, "line 4") || !strings.Contains(got, "line 8") {
		t.Errorf("tailLines kept %q, want lines 4 to 8", got)
	}
}

// A repository whose listing cannot be read is an error, not a reason to send
// the tree unfiltered: git is what keeps build outputs and secrets home, and a
// timeout or a refused safe.directory is not "this is not a checkout".
func TestAGitFailureInsideACheckoutStopsTheRun(t *testing.T) {
	f := newRunFixture(t)
	f.gitErr = errors.New("git ls-files in /repo: context deadline exceeded")
	if err := f.run(RunOptions{Command: []string{"true"}}); err == nil {
		t.Fatal("a run over an unlistable checkout was accepted")
	} else if !strings.Contains(err.Error(), "will not guess what to send") {
		t.Errorf("error = %q, want it to say the run will not guess", err)
	}
	if got := len(f.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls after a failed listing, want none", got)
	}
	if f.exec.runArgv() != nil {
		t.Error("the container started after a failed listing")
	}

	// The same failure with a rev-parse that failed for a reason other than
	// "not a repository" also stops the run: nothing here can tell that the path
	// is safe to mirror unfiltered.
	second := newRunFixture(t)
	second.gitErr = errors.New("git ls-files: index corrupt")
	second.gitInsideErr = errors.New("git rev-parse: cannot change to '/repo': Permission denied")
	if err := second.run(RunOptions{Command: []string{"true"}}); err == nil {
		t.Fatal("a run whose checkout check failed was accepted")
	} else if !strings.Contains(err.Error(), "checking whether") {
		t.Errorf("error = %q, want it to name the checkout check", err)
	}
	if got := len(second.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls after a failed checkout check, want none", got)
	}
}

// The command's own status becomes dot's: a failing test run on the host makes
// `dot remote run` fail with that code.
func TestRunReportsTheCommandsExitCode(t *testing.T) {
	f := newRunFixture(t)
	f.exec.exit = 3
	err := f.run(RunOptions{Command: []string{"make", "test"}})
	var exit *ExitCode
	if !errors.As(err, &exit) {
		t.Fatalf("Run = %v, want the command's exit code", err)
	}
	if exit.Code != 3 || exit.ExitCode() != 3 {
		t.Errorf("exit = %d, want 3", exit.Code)
	}
	if held := f.leases(); len(held) != 0 {
		t.Errorf("a failed run left %+v behind", held)
	}
}

// A dropped ssh leaves the container running: the interrupt path removes it by
// name and reports 128+signal.
func TestAnInterruptRemovesTheContainer(t *testing.T) {
	f := newRunFixture(t)
	f.exec.sig = syscall.SIGINT
	err := f.run(RunOptions{Command: []string{"sleep", "60"}})
	var exit *ExitCode
	if !errors.As(err, &exit) {
		t.Fatalf("Run = %v, want an interrupt", err)
	}
	if want := 128 + int(syscall.SIGINT); exit.Code != want {
		t.Errorf("exit = %d, want %d", exit.Code, want)
	}
	container := f.container()
	removals := f.exec.removals()
	if len(removals) != 1 || !strings.Contains(removals[0], "rm -f '"+container+"'") {
		t.Errorf("removals = %v, want one removal of %s", removals, container)
	}
	if !strings.Contains(exit.Error(), "interrupted by signal 2; removed "+container) {
		t.Errorf("error = %q, want it to name the signal and the removal", exit.Error())
	}
	if held := f.leases(); len(held) != 0 {
		t.Errorf("an interrupted run left %+v behind", held)
	}
}

// OpenSSH does not die by the signal it was sent: it handles SIGINT and SIGTERM
// and exits 255, so that status has to be treated as "the run did not finish"
// and cleaned up like an interrupt.
func TestAnSSHFailureRemovesTheContainer(t *testing.T) {
	f := newRunFixture(t)
	f.exec.exit = sshFailureStatus
	err := f.run(RunOptions{Command: []string{"make", "test"}})
	var exit *ExitCode
	if !errors.As(err, &exit) {
		t.Fatalf("Run = %v, want an ssh failure", err)
	}
	if exit.Code != sshFailureStatus {
		t.Errorf("exit = %d, want %d", exit.Code, sshFailureStatus)
	}
	container := f.container()
	removals := f.exec.removals()
	if len(removals) != 1 || !strings.Contains(removals[0], "rm -f '"+container+"'") {
		t.Errorf("removals = %v, want the container removed after an ssh failure", removals)
	}
	if !strings.Contains(exit.Error(), "ssh or the command exited 255") {
		t.Errorf("error = %q, want it to name ssh or the command's own 255", exit.Error())
	}
}

// An interrupt that cannot remove its container says so and keeps the claim:
// the mirror may still hold that container, so releasing the claim would let
// another machine sync into it.
func TestAnInterruptedRunWhoseRemovalFailsKeepsTheClaim(t *testing.T) {
	f := newRunFixture(t)
	f.exec.sig = syscall.SIGINT
	f.exec.removeErr = errors.New("Error response from daemon: container is in use")
	err := f.run(RunOptions{Command: []string{"sleep", "60"}})
	var exit *ExitCode
	if !errors.As(err, &exit) {
		t.Fatalf("Run = %v, want an interrupt", err)
	}
	if want := 128 + int(syscall.SIGINT); exit.Code != want {
		t.Errorf("exit = %d, want %d", exit.Code, want)
	}
	for _, want := range []string{"interrupted by signal 2", "could not remove the container", "keeps the claim"} {
		if !strings.Contains(exit.Error(), want) {
			t.Errorf("error = %q, want it to say %q", exit.Error(), want)
		}
	}
	if strings.Contains(exit.Error(), "removed "+f.container()) {
		t.Errorf("error = %q, claims a removal that did not happen", exit.Error())
	}
	if at := f.exec.order("claim: release"); at >= 0 {
		t.Errorf("the claim was released at call %d over a container that could not be removed", at)
	}
}

// A dropped ssh connection removes the container too, and that path has to
// report a removal it could not do the same way the interrupt does.
func TestAnSSHFailureWhoseRemovalFailsKeepsTheClaim(t *testing.T) {
	f := newRunFixture(t)
	f.exec.exit = sshFailureStatus
	f.exec.removeErr = errors.New("Error response from daemon: container is in use")
	err := f.run(RunOptions{Command: []string{"make", "test"}})
	var exit *ExitCode
	if !errors.As(err, &exit) {
		t.Fatalf("Run = %v, want an ssh failure", err)
	}
	if exit.Code != sshFailureStatus {
		t.Errorf("exit = %d, want %d", exit.Code, sshFailureStatus)
	}
	for _, want := range []string{"exited 255", "could not remove the container", "keeps the claim"} {
		if !strings.Contains(exit.Error(), want) {
			t.Errorf("error = %q, want it to say %q", exit.Error(), want)
		}
	}
	if strings.Contains(exit.Error(), "removed "+f.container()) {
		t.Errorf("error = %q, claims a removal that did not happen", exit.Error())
	}
	if at := f.exec.order("claim: release"); at >= 0 {
		t.Errorf("the claim was released at call %d over a container that could not be removed", at)
	}
}

// A container the runtime no longer has is not a failed removal: --rm reaps it
// when the command finishes, so an rm answered with "no such container" is
// settled by asking the host, and the run still releases its claim.
func TestARemovalWithNothingLeftToRemoveIsNotAFailure(t *testing.T) {
	f := newRunFixture(t)
	f.exec.sig = syscall.SIGINT
	f.exec.removeErr = errors.New("Error response from daemon: No such container")
	f.exec.containerGone = true
	err := f.run(RunOptions{Command: []string{"sleep", "60"}})
	var exit *ExitCode
	if !errors.As(err, &exit) {
		t.Fatalf("Run = %v, want an interrupt", err)
	}
	if strings.Contains(exit.Error(), "could not remove") {
		t.Errorf("error = %q, calls a container that is already gone a failed removal", exit.Error())
	}
	if !strings.Contains(exit.Error(), "removed "+f.container()) {
		t.Errorf("error = %q, want it to name the container it cleared", exit.Error())
	}
	if at := f.exec.order("claim: release"); at < 0 {
		t.Errorf("the claim was kept over a container the host no longer lists")
	}
}

// When neither the removal nor the query that settles it answers, the run says
// both reasons instead of reporting the first as the whole story.
func TestAnUnconfirmedRemovalNamesBothFailures(t *testing.T) {
	f := newRunFixture(t)
	f.exec.sig = syscall.SIGINT
	f.exec.removeErr = errors.New("Error response from daemon: container is in use")
	f.exec.presentErr = errors.New("Cannot connect to the Docker daemon")
	err := f.run(RunOptions{Command: []string{"sleep", "60"}})
	var exit *ExitCode
	if !errors.As(err, &exit) {
		t.Fatalf("Run = %v, want an interrupt", err)
	}
	for _, want := range []string{"could not remove the container", "container is in use", "could not confirm it is gone"} {
		if !strings.Contains(exit.Error(), want) {
			t.Errorf("error = %q, want it to say %q", exit.Error(), want)
		}
	}
	if at := f.exec.order("claim: release"); at >= 0 {
		t.Errorf("the claim was released at call %d over an unconfirmed removal", at)
	}
}

// A failed release names the claim and the two commands that clear it. It must
// not promise a takeover: the failure can be a partial rm that took the owner
// file and left the directory, and the next run reads that as a claim it cannot
// read and defers.
func TestAFailedReleaseNamesTheClaimAndTheCommandsToClearIt(t *testing.T) {
	f := newRunFixture(t)
	f.exec.releaseOut = "release\tfailed\tthe claim under $HOME/dot-remote/.dot-remote/x.claim could not be removed (Permission denied)\n"
	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("Run = %v", err)
	}
	msg := f.errOut.String()
	for _, want := range []string{"could not release the claim", "Permission denied", "ls -la", "rm -rf", ".claim"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr = %q, want it to carry %q", msg, want)
		}
	}
	if strings.Contains(msg, "takes it over") {
		t.Errorf("stderr = %q, promises a takeover a partial removal may not allow", msg)
	}
}

// A stopped container under this mirror's name is what a killed run, an older
// dot or another machine leaves behind; it is removed before the sync, so the
// name is free and the leftover cannot be mistaken for the new run.
func TestAStoppedContainerIsRemovedBeforeTheSync(t *testing.T) {
	f := newRunFixture(t)
	f.exec.preflight = "claim\tfree\ncontainer\tstopped\nimage\tknown\nuser\tdev\n"
	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("Run over a stopped container = %v", err)
	}
	if !strings.Contains(f.errOut.String(), "removed the stopped container") {
		t.Errorf("stderr does not explain the removal:\n%s", f.errOut.String())
	}
	if removals := f.exec.removals(); len(removals) != 1 || !strings.Contains(removals[0], f.container()) {
		t.Errorf("removals = %v, want the stopped container removed", removals)
	}
	if check, transfer := f.exec.order("docker rm -f"), f.exec.order("--files-from=-"); check < 0 || transfer < 0 || check > transfer {
		t.Errorf("removal at %d, ship pass at %d: the leftover must be gone before the sync", check, transfer)
	}
	if f.exec.runArgv() == nil {
		t.Error("the run did not start after the removal")
	}
}

// A container that is still running under this mirror's name is another
// machine's run: the lease store cannot see it, so the host is asked before the
// sync changes a mirror that is in use.
func TestARunningContainerForTheMirrorDefersTheRun(t *testing.T) {
	f := newRunFixture(t)
	f.exec.preflight = "container\trunning\nimage\tknown\nuser\tdev\n"
	err := f.run(RunOptions{Command: []string{"true"}})
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("Run = %v, want a defer when the mirror's container is running", err)
	}
	if deferred.Outcome.Scope != PathScope(f.checkout) {
		t.Errorf("scope = %q, want the path scope", deferred.Outcome.Scope)
	}
	if !strings.Contains(deferred.Outcome.Reason, "another machine is running this checkout") {
		t.Errorf("reason = %q", deferred.Outcome.Reason)
	}
	if got := len(f.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls with a container running, want none: the mirror must not change", got)
	}
	if held := f.leases(); len(held) != 0 {
		t.Errorf("the deferred run left %+v behind", held)
	}
}

// A container the host cannot remove holds the name the run needs, so the run
// stops with the cause rather than syncing into a mirror it cannot guarantee.
// It also keeps its claim: releasing it would let another machine into a mirror
// that still has that container in it.
func TestAFailedContainerRemovalStopsTheRun(t *testing.T) {
	f := newRunFixture(t)
	f.exec.preflight = "claim\tfree\ncontainer\tstopped\nimage\tknown\nuser\tdev\n"
	f.exec.removeErr = errors.New("Error response from daemon: container is in use")
	err := f.run(RunOptions{Command: []string{"true"}})
	if err == nil {
		t.Fatal("a run whose container could not be removed was accepted")
	}
	for _, want := range []string{"could not remove the container", "keeps the claim"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "removed the stopped container") {
		t.Errorf("error %q reads as a removal that happened", err)
	}
	if got := len(f.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls after a failed removal, want none", got)
	}
	if f.exec.runArgv() != nil {
		t.Error("the run started despite the failed removal")
	}
	if release := f.exec.order("claim: release"); release >= 0 {
		t.Errorf("the claim was released at call %d after a removal that did not happen", release)
	}
}

// The claim is the exclusion two machines cannot get from the lease store: it
// is a mkdir on the host, taken before anything is sent and held to the end.
func TestTheMirrorIsClaimedBeforeTheSyncAndReleasedAtTheEnd(t *testing.T) {
	f := newRunFixture(t)
	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	claim, transfer := f.exec.order("claim: take"), f.exec.order("--files-from=-")
	if claim < 0 {
		t.Fatal("the run never claimed the mirror")
	}
	if claim > transfer {
		t.Errorf("claim at %d, ship pass at %d: the claim has to come first", claim, transfer)
	}
	take := f.exec.calls[claim][len(f.exec.calls[claim])-1]
	if !strings.Contains(take, "mode=take") {
		t.Errorf("a fresh claim was taken as a replacement:\n%s", take)
	}
	if !strings.Contains(take, "mkdir") {
		t.Errorf("the claim is not a mkdir, so it is not atomic:\n%s", take)
	}
	if !strings.Contains(take, f.checkout) {
		t.Errorf("the claim's owner record does not name this checkout, so a later run cannot tell whose leftover it is:\n%s", take)
	}
}

// A claim another machine holds means that machine is inside this mirror right
// now. The lease store cannot see it, so the run defers and names the command
// that clears the claim once that run is gone.
func TestAClaimAnotherMachineHoldsDefersTheRun(t *testing.T) {
	f := newRunFixture(t)
	f.exec.preflight = "claim\tforeign\totherbox\t4242\t2026-01-01T00:00:00Z\t/elsewhere/repo\ncontainer\tabsent\nimage\tknown\nuser\tdev\n"
	err := f.run(RunOptions{Command: []string{"true"}})
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("Run = %v, want a defer while another machine holds the claim", err)
	}
	if deferred.Outcome.Scope != PathScope(f.checkout) {
		t.Errorf("scope = %q, want the path scope", deferred.Outcome.Scope)
	}
	for _, want := range []string{"belongs to", "otherbox", "ssh buildbox 'rm -rf \"$HOME\"", ".claim"} {
		if !strings.Contains(deferred.Outcome.Reason, want) {
			t.Errorf("reason %q does not name %q", deferred.Outcome.Reason, want)
		}
	}
	if f.exec.order("claim: take") >= 0 {
		t.Error("a run that deferred on the claim took one anyway")
	}
	if got := len(f.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls while another machine holds the claim, want none", got)
	}
	if f.exec.runArgv() != nil {
		t.Error("the container started while another machine holds the claim")
	}
}

// The same machine name with a different checkout is not this run's predecessor:
// the local path lease this run holds proves only that one path's run is dead,
// so a claim for another checkout of this machine defers like any other.
func TestAClaimForAnotherCheckoutOfThisMachineDefersTheRun(t *testing.T) {
	f := newRunFixture(t)
	f.exec.preflight = "claim\tforeign\t" + f.deps().machine() + "\t4242\t2026-01-01T00:00:00Z\t/home/u/other-clone/repo\ncontainer\trunning\nimage\tknown\nuser\tdev\n"
	err := f.run(RunOptions{Command: []string{"true"}})
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("Run = %v, want a defer for another checkout's claim", err)
	}
	for _, want := range []string{"belongs to", "other-clone", "ssh buildbox 'rm -rf \"$HOME\""} {
		if !strings.Contains(deferred.Outcome.Reason, want) {
			t.Errorf("reason %q does not name %q", deferred.Outcome.Reason, want)
		}
	}
	if f.exec.order("claim: take") >= 0 {
		t.Error("a run took over a claim that names a different checkout")
	}
	if removals := f.exec.removals(); len(removals) != 0 {
		t.Errorf("removals = %v, want none while another checkout's claim stands", removals)
	}
}

// A claim whose owner file cannot be read is neither free nor foreign, and
// guessing either way is what the old check did.
func TestAnUnreadableClaimDefersTheRun(t *testing.T) {
	f := newRunFixture(t)
	f.exec.preflight = "claim\tunreadable\tno readable $HOME/dot-remote/.dot-remote/x.claim/owner\ncontainer\tabsent\nimage\tknown\nuser\tdev\n"
	err := f.run(RunOptions{Command: []string{"true"}})
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("Run = %v, want a defer when the claim cannot be read", err)
	}
	if !strings.Contains(deferred.Outcome.Reason, "cannot be read") {
		t.Errorf("reason = %q", deferred.Outcome.Reason)
	}
	if got := len(f.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls with an unreadable claim, want none", got)
	}
}

// The claim a SIGKILLed run leaves names this machine and this checkout, and the
// local path lease this run holds proves that owner dead - so the container it
// left running is removed and the claim taken over, instead of being reported as
// another machine's forever.
func TestAClaimThisMachineLeftIsTakenOver(t *testing.T) {
	f := newRunFixture(t)
	container := f.container()
	f.exec.preflight = "claim\tours\t" + f.deps().machine() + "\t4242\t2026-01-01T00:00:00Z\t" + f.checkout + "\ncontainer\trunning\nimage\tknown\nuser\tdev\n"
	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("Run over this machine's own leftover = %v", err)
	}
	take := ""
	for _, argv := range f.exec.calls {
		if command := argv[len(argv)-1]; strings.Contains(command, "claim: take") {
			take = command
		}
	}
	if !strings.Contains(take, "mode=replace") || !strings.Contains(take, "rm -rf") {
		t.Errorf("the leftover claim was not replaced:\n%s", take)
	}
	removals := f.exec.removals()
	if len(removals) != 1 || !strings.Contains(removals[0], container) {
		t.Errorf("removals = %v, want the dead run's container removed", removals)
	}
	if !strings.Contains(f.errOut.String(), "took over the claim") {
		t.Errorf("stderr does not say the claim was taken over:\n%s", f.errOut.String())
	}
	if f.exec.runArgv() == nil {
		t.Error("the run did not start after the takeover")
	}
}

// A takeover that cannot remove the dead run's container has not freed the name,
// so the run stops, says "could not remove" and keeps the claim it just took:
// releasing it would invite another machine into a mirror that still holds that
// container.
func TestATakeoverWhoseRemovalFailsKeepsTheClaim(t *testing.T) {
	f := newRunFixture(t)
	f.exec.preflight = "claim\tours\t" + f.deps().machine() + "\t4242\t2026-01-01T00:00:00Z\t" + f.checkout + "\ncontainer\trunning\nimage\tknown\nuser\tdev\n"
	f.exec.removeErr = errors.New("Error response from daemon: container is in use")
	err := f.run(RunOptions{Command: []string{"true"}})
	if err == nil {
		t.Fatal("a takeover whose removal failed was accepted")
	}
	for _, want := range []string{"could not remove the container", "keeps the claim"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if strings.Contains(f.errOut.String(), "removed the container") {
		t.Errorf("stderr reads as a removal that happened:\n%s", f.errOut.String())
	}
	if release := f.exec.order("claim: release"); release >= 0 {
		t.Errorf("the claim was released at call %d after a removal that did not happen", release)
	}
	if got := len(f.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls after the failed takeover, want none", got)
	}
	if f.exec.runArgv() != nil {
		t.Error("the run started after the failed takeover")
	}
}

// Two machines can inspect an unclaimed mirror at once; the mkdir decides. The
// loser defers rather than syncing into a mirror the winner is using.
func TestLosingTheClaimRaceDefersTheRun(t *testing.T) {
	f := newRunFixture(t)
	f.exec.claim = "claim\tlost-race\n"
	err := f.run(RunOptions{Command: []string{"true"}})
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("Run = %v, want a defer after losing the claim race", err)
	}
	if !strings.Contains(deferred.Outcome.Reason, "took the claim") {
		t.Errorf("reason = %q", deferred.Outcome.Reason)
	}
	if got := len(f.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls after losing the claim race, want none", got)
	}
}

// A runtime that cannot answer is not a host with nothing there: read as an
// answer, the second query's empty output would say "stopped" and the run would
// remove a container another machine is running in.
func TestAFailedContainerQueryStopsTheRun(t *testing.T) {
	for name, answer := range map[string]string{
		"first query":  "claim\tfree\ncontainer\tquery-failed\tCannot connect to the Docker daemon\nimage\tknown\nuser\tdev\n",
		"second query": "claim\tfree\ncontainer\tquery-failed\tpermission denied while connecting\nimage\tknown\nuser\tdev\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := newRunFixture(t)
			f.exec.preflight = answer
			err := f.run(RunOptions{Command: []string{"true"}})
			if err == nil {
				t.Fatalf("a run whose %s failed was accepted", name)
			}
			for _, want := range []string{"could not report the container", "neither sync nor remove"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
			if removals := f.exec.removals(); len(removals) != 0 {
				t.Errorf("removals = %v, want none when the query failed", removals)
			}
			if got := len(f.exec.transfers()); got != 0 {
				t.Errorf("%d rsync calls after a failed query, want none", got)
			}
			if f.exec.order("claim: take") >= 0 {
				t.Error("a run that stopped on the query took a claim anyway")
			}
		})
	}
}

// A claim the host refuses to create is a hard failure, not a silent run: the
// exclusion is the whole point of taking it.
func TestAFailedClaimStopsTheRun(t *testing.T) {
	f := newRunFixture(t)
	f.exec.claimExit = 1
	err := f.run(RunOptions{Command: []string{"true"}})
	if err == nil {
		t.Fatal("a run whose claim failed was accepted")
	}
	if !strings.Contains(err.Error(), "claiming the mirror") {
		t.Errorf("error = %q", err)
	}
	if got := len(f.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls without a claim, want none", got)
	}
}

// An image without a USER runs as root, and everything it writes into the mirror
// would belong to root. The run refuses instead, naming the fix.
func TestAnImageWithoutAUserIsRefused(t *testing.T) {
	f := newRunFixture(t)
	f.exec.preflight = "container\tabsent\nimage\tknown\nuser\t\n"
	err := f.run(RunOptions{Command: []string{"true"}})
	if err == nil {
		t.Fatal("a run in a root image was accepted")
	}
	for _, want := range []string{"runs as root", "USER dev", "dot remote setup"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if got := len(f.exec.transfers()); got != 0 {
		t.Errorf("%d rsync calls into a root image, want none", got)
	}
}

// An image the host does not have is a setup problem, and the message says so.
func TestAnImageTheHostDoesNotHaveNamesSetup(t *testing.T) {
	f := newRunFixture(t)
	f.exec.preflight = "container\tabsent\nimage\tunknown\n"
	err := f.run(RunOptions{Command: []string{"true"}})
	if err == nil {
		t.Fatal("a run with no image on the host was accepted")
	}
	if !strings.Contains(err.Error(), "dot remote setup") {
		t.Errorf("error = %q, want it to name setup", err)
	}
}

// The mirror is recorded on the host so `dot remote status` can size it: a
// checkout below local_root mirrors to a nested directory, which no top-level
// listing can name.
func TestTheRunRecordsTheMirrorOnTheHost(t *testing.T) {
	f := newRunFixture(t)
	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	script := f.exec.preflights()[0]
	for _, want := range []string{".dot-remote", "'repo'", MirrorKey("repo")} {
		if !strings.Contains(script, want) {
			t.Errorf("the preflight script does not carry %q:\n%s", want, script)
		}
	}
}

// A run started inside a subdirectory sends the checkout, not the directory: the
// default path is what git calls the top of the work tree.
func TestTheDefaultPathIsTheCheckoutRoot(t *testing.T) {
	f := newRunFixture(t)
	sub := filepath.Join(f.checkout, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f.gitToplevel = f.checkout
	t.Chdir(sub)
	if err := f.deps().Run(context.Background(), f.cfg, RunOptions{HostName: f.host.Name, Command: []string{"true"}}); err != nil {
		t.Fatalf("Run from a subdirectory: %v", err)
	}
	if !slices.Contains(f.gitDirs, f.checkout) {
		t.Errorf("git read %v, want the checkout root %s among them", f.gitDirs, f.checkout)
	}
	if slices.Contains(f.gitDirs[1:], sub) {
		t.Errorf("git was asked to list the subdirectory %s: %v", sub, f.gitDirs)
	}
	transfer := strings.Join(f.exec.transfers()[0], " ")
	if !strings.Contains(transfer, f.checkout+"/") {
		t.Errorf("the transfer does not start at the checkout root:\n%s", transfer)
	}
}

// A missing admission store says what to do about it, with the reason it could
// not be opened.
func TestAMissingStoreNamesTheNextStep(t *testing.T) {
	f := newRunFixture(t)
	deps := f.deps()
	deps.Store = nil
	deps.StoreErr = errors.New("permission denied: /home/u/.local/state")
	err := deps.Run(context.Background(), f.cfg, RunOptions{Path: f.checkout, HostName: f.host.Name, Command: []string{"true"}})
	if err == nil {
		t.Fatal("a run without a lease store was accepted")
	}
	for _, want := range []string{"permission denied", "dot admit status"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}
