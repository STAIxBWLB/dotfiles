package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	runexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// fakeExec stands in for the real execer: it records every argv and answers
// the three shapes a run sends (the load probe, rsync, the container run).
type fakeExec struct {
	t     *testing.T
	loads string
	// probeErr makes the load probe fail instead of answering.
	probeErr error
	// exit and sig are what the container run itself reports.
	exit int
	sig  syscall.Signal
	// output is what the container run writes to stdout.
	output string
	calls  [][]string
	stdin  [][]byte
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
	case len(argv) > 0 && argv[0] == "rsync":
		return 0, 0, nil
	case strings.Contains(command, "loadavg"):
		if f.probeErr != nil {
			return 0, 0, f.probeErr
		}
		fmt.Fprint(stdout, f.loads)
		return 0, 0, nil
	case strings.Contains(command, " rm -f "):
		return 0, 0, nil
	default:
		fmt.Fprint(stdout, f.output)
		return f.exit, f.sig, nil
	}
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
		if len(argv) > 0 && argv[0] == "rsync" {
			out = append(out, argv)
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

// removals lists the "rm -f" commands, which is how a run cleans up a
// container it must not leave behind.
func (f *fakeExec) removals() []string {
	var out []string
	for _, argv := range f.calls {
		command := argv[len(argv)-1]
		if strings.Contains(command, " rm -f ") {
			out = append(out, command)
		}
	}
	return out
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
	out      bytes.Buffer
	errOut   bytes.Buffer
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
		exec:     &fakeExec{t: t, loads: "0.50 0.40 0.30 1/100 4242\n4\n"},
		gitData:  []byte("main.go\x00docs/a.md\x00"),
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
		Git: func(_ context.Context, dir string, _ ...string) ([]byte, error) {
			f.gitDirs = append(f.gitDirs, dir)
			if f.gitErr != nil {
				return nil, f.gitErr
			}
			return f.gitData, nil
		},
	}
}

func (f *runFixture) run(opts RunOptions) error {
	f.t.Helper()
	if opts.Path == "" {
		opts.Path = f.checkout
	}
	return f.deps().Run(context.Background(), f.cfg, f.host, opts)
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

// deadLease is a lease whose owner is already gone: a process that has exited.
// Its deadline is still fresh, so the store only frees it once the clock moves
// past that deadline and the liveness probe finds no such process.
func deadLease(t *testing.T) admission.Lease {
	t.Helper()
	cmd := runexec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot spawn a probe process")
	}
	return admission.Lease{Owner: "dead@host", PID: cmd.Process.Pid, PIDStart: "Sun Sep 27 10:00:00 2026", PGID: cmd.Process.Pid}
}

// The order is the reference behaviour's: the load probe, then git's listing
// shipped, then the delete pass, then the run itself.
func TestRunSyncsAndThenRuns(t *testing.T) {
	f := newRunFixture(t)
	if err := f.run(RunOptions{Command: []string{"make", "test"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held := f.leases(); len(held) != 0 {
		t.Errorf("leases left behind: %+v", held)
	}
	if got := f.exec.probed(); got != 1 {
		t.Errorf("load probes = %d, want exactly one", got)
	}
	transfers := f.exec.transfers()
	if len(transfers) != 2 {
		t.Fatalf("rsync calls = %d, want the ship and the delete pass", len(transfers))
	}
	if !strings.Contains(strings.Join(transfers[0], " "), "--files-from=-") {
		t.Errorf("first pass is not the file list: %v", transfers[0])
	}
	ship := f.exec.stin(transfers[0])
	if string(ship) != string(f.gitData) {
		t.Errorf("ship pass was fed %q, want git's listing %q", ship, f.gitData)
	}
	if !strings.Contains(strings.Join(transfers[1], " "), "--delete") {
		t.Errorf("second pass is not the delete pass: %v", transfers[1])
	}
	if got := f.exec.stin(transfers[1]); len(got) != 0 {
		t.Errorf("delete pass was fed %q, want nothing", got)
	}
	if len(f.gitDirs) != 1 || f.gitDirs[0] != f.checkout {
		t.Errorf("git read %v, want the checkout %s", f.gitDirs, f.checkout)
	}
	run := f.exec.runArgv()
	if run == nil {
		t.Fatal("the container never started")
	}
	command := run[len(run)-1]
	container := f.host.ContainerName(PathKey(f.checkout))
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
	// The run is last: the mirror has to be in place before it starts.
	if last := f.exec.calls[len(f.exec.calls)-1]; last[len(last)-1] != command {
		t.Errorf("the container run was not the last command sent")
	}
}

// A second job for the same path defers instead of sharing its mirror, and it
// sends nothing at all: the check happens before the first byte moves.
func TestRunDefersWhenTheMirrorPathIsBusy(t *testing.T) {
	f := newRunFixture(t)
	held, err := AcquirePath(context.Background(), f.store, selfLease(t, f.checkout), f.host, f.checkout, 0, false)
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
	f.gitErr = errors.New("fatal: not a git repository")
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
	container := f.host.ContainerName(PathKey(f.checkout))
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

// A lease reclaimed from a run that died means its container may still be
// running under a name the next run needs, so it is removed before the new one
// starts.
func TestAReclaimedLeaseRemovesTheContainerItLeftBehind(t *testing.T) {
	f := newRunFixture(t)
	if _, err := AcquirePath(context.Background(), f.store, deadLease(t), f.host, f.checkout, 0, false); err != nil {
		t.Fatalf("leaving a dead owner's lease: %v", err)
	}
	// The lease still looks live until its deadline passes; the liveness probe
	// is what then decides it is stale.
	f.store.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }

	if err := f.run(RunOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("Run over a stale lease = %v", err)
	}
	container := f.host.ContainerName(PathKey(f.checkout))
	removals := f.exec.removals()
	if len(removals) != 1 || !strings.Contains(removals[0], "rm -f '"+container+"'") {
		t.Fatalf("removals = %v, want one removal of %s", removals, container)
	}
	if !strings.Contains(f.errOut.String(), "removing the container left behind by a run that died") {
		t.Errorf("stderr does not explain the removal:\n%s", f.errOut.String())
	}
	if run := f.exec.runArgv(); run == nil {
		t.Error("the run did not start after the reclaim")
	}
}
