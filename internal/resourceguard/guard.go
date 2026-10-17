// Package resourceguard admits one heavyweight job per repository, across all
// worktrees and agent profiles, while enforcing shared host-pressure recovery.
// It is a thin adapter over internal/admission, the engine behind `dot admit`,
// so `dot ai run`, the tooling updates and `dot admit` contend for the same
// slots and read the same pressure history (#162).
package resourceguard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// Options selects the slot. HomeDir is accepted for callers' symmetry but
// never partitions a slot: slots belong to the real user and repository.
type Options struct {
	HomeDir    string
	Purpose    string
	ProjectDir string
	ScopeKey   string // "tooling" selects the host-wide maintenance slot
}

// DeferredError reports that admission deferred the job; callers retry later.
type DeferredError struct {
	Reason              string
	prebuiltSampleRetry bool
}

func (e *DeferredError) Error() string { return "deferred-resource-pressure: " + e.Reason }

// sampleInterval paces WaitAcquire's retries.
const sampleInterval = 15 * time.Second

type adapter struct {
	root        func() (string, error)
	runner      *exec.Runner
	monitor     func() *admission.Monitor
	uncovered   func(ctx context.Context, dir string, maintenance bool, leased []int) ([]string, error)
	lease       func(ctx context.Context, runner *exec.Runner, dir string) (admission.Lease, error)
	resolve     func(ctx context.Context, runner *exec.Runner, dir string) (string, error)
	parentLease func(ctx context.Context, store *admission.Store, scope, class string, childPID int) bool
	heartbeat   time.Duration
}

func native() *adapter {
	runner := exec.NewProbeRunner()
	return &adapter{
		root:   admission.UserStateRoot,
		runner: runner,
		monitor: func() *admission.Monitor {
			home, _ := os.UserHomeDir()
			return &admission.Monitor{Runner: runner, Home: home}
		},
		uncovered: admission.FindUncovered,
		lease:     admission.SelfLease,
		resolve:   admission.ResolveScope,
		parentLease: func(ctx context.Context, store *admission.Store, scope, class string, childPID int) bool {
			return hasLiveParentLease(ctx, runner, store, scope, class, childPID)
		},
		heartbeat: admission.DefaultHeartbeatInterval,
	}
}

// Acquire fails promptly on contention, pressure, unknown telemetry, or
// uncovered heavy work.
func Acquire(ctx context.Context, opts Options) (func(), error) {
	return native().acquire(ctx, opts)
}

// AcquireVerifiedPrebuiltUpdate reserves the host maintenance slot for the
// single bounded, checksum-verified native dot release update operation.
func AcquireVerifiedPrebuiltUpdate(ctx context.Context) (func(), error) {
	a := native()
	monitor := a.monitor()
	opts := Options{Purpose: "verified prebuilt dot update", ScopeKey: "tooling"}
	policy := admission.GatePolicy{
		HistoryFile: admission.PrebuiltHistoryFile,
		Thresholds:  admission.PrebuiltThresholds(),
		Snapshot: func(ctx context.Context) admission.PressureSnapshot {
			return admission.SnapshotPrebuilt(ctx, monitor)
		},
		Evaluate: admission.EvaluatePrebuiltPressure,
	}
	return retryPrebuiltAcquire(ctx, admission.PrebuiltSafetyWindow+sampleInterval, sampleInterval, func(waitCtx context.Context) (func(), error) {
		return a.acquirePolicy(waitCtx, opts, policy, monitor)
	})
}

func retryPrebuiltAcquire(ctx context.Context, maxWait, delay time.Duration, attempt func(context.Context) (func(), error)) (func(), error) {
	return retryAcquire(ctx, maxWait, delay, attempt, func(err error) bool {
		var deferred *DeferredError
		return errors.As(err, &deferred) && deferred.prebuiltSampleRetry
	}, "sample window wait ended")
}

func retryAcquire(ctx context.Context, maxWait, delay time.Duration, attempt func(context.Context) (func(), error), retryable func(error) bool, timeoutLabel string) (func(), error) {
	waitCtx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()
	var lastErr error
	for {
		release, err := attempt(waitCtx)
		if err == nil {
			return release, nil
		}
		lastErr = err
		if !retryable(err) {
			return nil, err
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("%w (%s: %v)", lastErr, timeoutLabel, waitCtx.Err())
		case <-time.After(delay):
		}
	}
}

// WaitAcquire retries deferrals with bounded, cancellable waits.
func WaitAcquire(ctx context.Context, opts Options, maxWait time.Duration) (func(), error) {
	return native().waitAcquire(ctx, opts, maxWait)
}

func (a *adapter) waitAcquire(ctx context.Context, opts Options, maxWait time.Duration) (func(), error) {
	return retryAcquire(ctx, maxWait, sampleInterval, func(ctx context.Context) (func(), error) {
		return a.acquire(ctx, opts)
	}, func(err error) bool {
		var deferred *DeferredError
		return errors.As(err, &deferred)
	}, "wait ended")
}

func (a *adapter) acquire(ctx context.Context, opts Options) (func(), error) {
	return a.acquirePolicy(ctx, opts, admission.GatePolicy{}, nil)
}

func (a *adapter) acquirePolicy(ctx context.Context, opts Options, policy admission.GatePolicy, selectedMonitor *admission.Monitor) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := opts.ProjectDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = wd
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs // the lease records it; `dot admit status` prints it
	}
	scope, class := "", admission.ClassHeavy
	switch opts.ScopeKey {
	case "":
		s, err := a.resolve(ctx, a.runner, dir)
		if err != nil {
			return nil, &DeferredError{Reason: err.Error()}
		}
		scope = s
	case "tooling":
		scope, class = admission.MaintenanceScope, admission.ClassMaintenance
	default:
		return nil, fmt.Errorf("unknown shared resource scope %q", opts.ScopeKey)
	}
	// A child of a job that already holds this slot runs inside it.
	nested := admission.NestedScope(os.Getenv(admission.NestedEnv), scope, class)
	if policy.HistoryFile == "" && nested && class != admission.ClassMaintenance {
		return func() {}, nil
	}
	root, err := a.root()
	if err != nil {
		return nil, err
	}
	store := admission.NewStore(root, a.runner)
	monitor := a.monitor()
	if selectedMonitor != nil {
		monitor = selectedMonitor
	}
	// The probes are bounded on their own; a caller canceling mid-probe must
	// not record failed telemetry into the shared history.
	var d admission.Decision
	if policy.HistoryFile == "" {
		d, err = store.Gate(context.WithoutCancel(ctx), monitor, admission.DefaultThresholds())
	} else {
		d, err = store.GateWithPolicy(context.WithoutCancel(ctx), monitor, policy)
	}
	if err != nil {
		return nil, err
	}
	if !d.Admit {
		return nil, &DeferredError{Reason: strings.Join(d.Reasons, "; "), prebuiltSampleRetry: d.ProfileRetry}
	}
	if ctx.Err() != nil {
		// The wait ended during the gate; report it as a deferral, not as a
		// later identity-probe failure.
		return nil, &DeferredError{Reason: "the gate outlived the wait"}
	}
	if nested && class == admission.ClassMaintenance && a.parentLease != nil && a.parentLease(ctx, store, scope, class, os.Getpid()) {
		// A verified ancestor owns this exact slot. Re-scan after our gate and
		// immediately before reuse because uncovered work may have started while
		// the quiet-window samples were collected.
		jobs, scanErr := a.uncovered(ctx, dir, true, admission.LeasedPIDs(store))
		if scanErr != nil {
			return nil, &DeferredError{Reason: "heavy-work inventory unavailable: " + scanErr.Error()}
		}
		if len(jobs) > 0 {
			return nil, &DeferredError{Reason: "uncovered heavyweight work: " + strings.Join(jobs, ", ")}
		}
		return func() {}, nil
	}
	lease, err := a.lease(ctx, a.runner, dir)
	if err != nil {
		return nil, err
	}
	if lease.Session == "" {
		lease.Session = opts.Purpose // names the job in `dot admit status`
	}
	slot, holder, err := store.Acquire(ctx, scope, class, lease)
	if err != nil {
		return nil, err
	}
	if slot == nil {
		reason := "another heavyweight job owns scope " + scope
		if holder != nil {
			reason += fmt.Sprintf(" (%s, pid %d)", holder.Owner, holder.PID)
		}
		return nil, &DeferredError{Reason: reason}
	}
	// Scan with the slot held, so the job that owned it is never mistaken for
	// uncovered work; release it again on defer.
	jobs, err := a.uncovered(ctx, dir, class == admission.ClassMaintenance, admission.LeasedPIDs(store))
	if err != nil || len(jobs) > 0 {
		_ = slot.Release()
		if err != nil {
			return nil, &DeferredError{Reason: "heavy-work inventory unavailable: " + err.Error()}
		}
		return nil, &DeferredError{Reason: "uncovered heavyweight work: " + strings.Join(jobs, ", ")}
	}

	// Children inherit the nested marker, so a `dot admit` or `dot ai run`
	// inside this job runs in the slot instead of deadlocking on it.
	// ponytail: process-wide env; fine while one guarded job runs per process.
	prev, hadPrev := os.LookupEnv(admission.NestedEnv)
	_ = os.Setenv(admission.NestedEnv, admission.NestedEnvValue(scope, class))
	stopHeartbeat := keepAlive(slot, a.heartbeat)
	var once sync.Once
	return func() {
		once.Do(func() {
			stopHeartbeat()
			if hadPrev {
				_ = os.Setenv(admission.NestedEnv, prev)
			} else {
				_ = os.Unsetenv(admission.NestedEnv)
			}
			_ = slot.Release()
		})
	}, nil
}

// keepAlive heartbeats slot until the returned func is called.
func keepAlive(slot *admission.Slot, every time.Duration) (stop func()) {
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-t.C:
				_ = slot.Heartbeat()
			}
		}
	}()
	return func() {
		close(stopCh)
		<-done
	}
}

// HoldBackgroundSync holds a lease named purpose while dot's own peer sync or
// mirror push runs (#267). `dot admit status` lists it with its start time,
// and the uncovered-work scan treats the leased process tree as covered, so
// the sync's rsync children never defer heavy work in another repository or
// the maintenance slot. It never defers or blocks the sync: there is no gate,
// and any failure returns a no-op release.
func HoldBackgroundSync(ctx context.Context, purpose string) (release func()) {
	return native().holdBackground(ctx, purpose)
}

func (a *adapter) holdBackground(ctx context.Context, purpose string) func() {
	noop := func() {}
	root, err := a.root()
	if err != nil {
		return noop
	}
	dir, _ := os.Getwd()
	lease, err := a.lease(ctx, a.runner, dir)
	if err != nil {
		return noop
	}
	slot, _, err := admission.NewStore(root, a.runner).Acquire(ctx, purpose, admission.ClassBackground, lease)
	if err != nil || slot == nil {
		return noop
	}
	stopHeartbeat := keepAlive(slot, a.heartbeat)
	var once sync.Once
	return func() {
		once.Do(func() {
			stopHeartbeat()
			_ = slot.Release()
		})
	}
}

func hasLiveParentLease(ctx context.Context, runner *exec.Runner, store *admission.Store, scope, class string, childPID int) bool {
	if !admission.NestedScope(os.Getenv(admission.NestedEnv), scope, class) || scope != admission.MaintenanceScope || class != admission.ClassMaintenance {
		return false
	}
	leases, err := store.ListLeases()
	if err != nil {
		return false
	}
	var parent *admission.Lease
	for i := range leases {
		if leases[i].Scope == scope && leases[i].Class == class {
			if parent != nil {
				return false
			}
			parent = &leases[i]
		}
	}
	if parent == nil || parent.PID <= 0 || parent.PIDStart == "" || time.Now().After(parent.Deadline) {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	match, err := watchdog.ProcessStartMatches(probeCtx, runner, parent.PID, parent.PIDStart)
	if err != nil || !match {
		return false
	}
	res, err := runner.RunQuery(probeCtx, "ps", "-Ao", watchdog.PSArgs)
	if err != nil {
		return false
	}
	processes, err := watchdog.ParsePS(res.Stdout)
	if err != nil {
		return false
	}
	byPID := make(map[int]watchdog.Process, len(processes))
	for _, process := range processes {
		byPID[process.PID] = process
	}
	ancestor, ok := byPID[parent.PID]
	if !ok || ancestor.Started != parent.PIDStart {
		return false
	}
	// `dot admit` may transfer the lease identity to its wrapped child after
	// exec; processHasAncestor accepts a self match only after these identity
	// checks have succeeded.
	return processHasAncestor(childPID, parent.PID, byPID)
}

func processHasAncestor(childPID, ancestorPID int, processes map[int]watchdog.Process) bool {
	seen := make(map[int]bool)
	current, ok := processes[childPID]
	if !ok || current.PID == 0 {
		return false
	}
	if childPID == ancestorPID {
		return true
	}
	for depth := 0; depth < 64 && current.PPID > 0; depth++ {
		pid := current.PPID
		if pid == ancestorPID {
			return true
		}
		if seen[pid] {
			return false
		}
		seen[pid] = true
		current, ok = processes[pid]
		if !ok {
			return false
		}
	}
	return false
}
