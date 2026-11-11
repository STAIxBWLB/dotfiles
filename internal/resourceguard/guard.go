// Package resourceguard admits one heavyweight job per repository, across all
// worktrees and agent profiles, while enforcing shared host-pressure recovery.
// It is a thin adapter over internal/admission, the engine behind `dot admit`,
// so tooling updates and `dot admit` contend for the same
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
	"github.com/STAIxBWLB/dotfiles/internal/config"
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

// guardProfile selects which gate an acquire runs: the standard pressure gate
// or the verified prebuilt update profile. Both read the same resolved policy,
// so a config key or a bypass reaches each of them.
type guardProfile int

const (
	profileStandard guardProfile = iota
	profilePrebuilt
)

// prebuiltSeams lets a test replace the verified prebuilt profile's probes and
// evaluator; real callers pass the zero value.
type prebuiltSeams struct {
	Snapshot func(context.Context) admission.PressureSnapshot
	Evaluate func(admission.PressureSnapshot, admission.Thresholds, admission.History, time.Time) admission.Decision
}

type adapter struct {
	root        func() (string, error)
	runner      *exec.Runner
	monitor     func() *admission.Monitor
	uncovered   func(ctx context.Context, dir string, maintenance bool, leased []int, settings admission.UncoveredSettings) ([]string, error)
	lease       func(ctx context.Context, runner *exec.Runner, dir string) (admission.Lease, error)
	resolve     func(ctx context.Context, runner *exec.Runner, dir string) (string, error)
	parentLease func(ctx context.Context, store *admission.Store, scope, class string, childPID int) bool
	heartbeat   time.Duration
	// configPath names the user config file the policy and the relax override
	// are read from. Nil means no config layer, which keeps tests deterministic
	// on any host.
	configPath func() string
	// now is the clock a slow step's post-check reads: the uncovered scan
	// outlasts the policy it ran under, so a test sets this to put an override's
	// expiry inside the scan window. Nil is the real clock.
	now func() time.Time
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
		heartbeat:  admission.DefaultHeartbeatInterval,
		configPath: config.StatePath,
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
	budget, err := a.prebuiltRetryBudget()
	if err != nil {
		return nil, err
	}
	return retryPrebuiltAcquire(ctx, budget, sampleInterval, func(waitCtx context.Context) (func(), error) {
		return a.acquireProfile(waitCtx, opts, profilePrebuilt, prebuiltSeams{}, monitor)
	})
}

// prebuiltRetryBudget is how long the sample warmup may take. A cold history
// needs three samples about sampleInterval apart before the safety window can
// be judged, so a short or zero safety_window must not shrink the budget below
// the default window's: the samples never fit and a healthy host defers. The
// budget is only a cap, so a shorter window still admits as soon as its samples
// pass, and a longer one gets the time its window needs.
func (a *adapter) prebuiltRetryBudget() (time.Duration, error) {
	policy, err := a.policy()
	if err != nil {
		return 0, err
	}
	window := policy.Prebuilt.SafetyWindow
	if window < admission.PrebuiltSafetyWindow {
		window = admission.PrebuiltSafetyWindow
	}
	return window + sampleInterval, nil
}

// policy resolves the effective admission policy from the real user's config
// and any active relax override.
func (a *adapter) policy() (admission.Policy, error) {
	root, err := a.root()
	if err != nil {
		return admission.Policy{}, err
	}
	configPath := ""
	if a.configPath != nil {
		configPath = a.configPath()
	}
	return admission.NewStore(root, a.runner).ResolvePolicy(admission.ResolveOptions{ConfigPath: configPath})
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
	return a.acquireProfile(ctx, opts, profileStandard, prebuiltSeams{}, nil)
}

func (a *adapter) acquireProfile(ctx context.Context, opts Options, kind guardProfile, seams prebuiltSeams, selectedMonitor *admission.Monitor) (func(), error) {
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
	if kind == profileStandard && nested && class != admission.ClassMaintenance {
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
	configPath := ""
	if a.configPath != nil {
		configPath = a.configPath()
	}
	policy, err := store.ResolvePolicy(admission.ResolveOptions{ConfigPath: configPath})
	if err != nil {
		return nil, err
	}

	gate := admission.GatePolicy{Thresholds: policy.Thresholds}
	if kind == profilePrebuilt {
		gate = admission.PrebuiltGate(policy)
	}
	gate.Ignore, gate.IgnoreSource = policy.Ignore, policy.IgnoreSource
	if policy.Relax != nil {
		// The probe runs after this read, so an override that ends during it must
		// not judge the sample it produced.
		gate.ValidUntil = policy.Relax.ExpiresAt
	}
	if seams.Snapshot != nil {
		gate.Snapshot = seams.Snapshot
	} else if kind == profilePrebuilt {
		gate.Snapshot = func(ctx context.Context) admission.PressureSnapshot {
			return admission.SnapshotPrebuilt(ctx, monitor)
		}
	}
	if seams.Evaluate != nil {
		gate.Evaluate = seams.Evaluate
	}
	// The Linux MemAvailable mapping reads the level the gate it feeds judges
	// against, so each profile maps with its own memory thresholds.
	monitor.Thresholds = &gate.Thresholds
	// The probes are bounded on their own; a caller canceling mid-probe must
	// not record failed telemetry into the shared history.
	d, err := store.GateWithPolicy(context.WithoutCancel(ctx), monitor, gate)
	if err != nil {
		return nil, err
	}
	if d.PressureIgnored {
		admission.NoteIgnoredPressure(os.Stderr, d)
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
		jobs, scanErr := a.scanUncovered(ctx, dir, true, store, policy)
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
	capacity := policy.Slots.For(class)
	slot, holder, err := store.AcquireWithOptions(ctx, scope, class, lease, admission.AcquireOptions{
		Capacity: capacity,
		Overflow: policy.Ignore.Slot,
	})
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
	// A bypassed capacity still holds a lease, in an overflow slot: it stays
	// visible in status and its process tree stays covered for other callers.
	admission.NoteOverflowSlot(os.Stderr, slot.Lease(), capacity, policy.IgnoreSource)
	// Scan with the slot held, so the job that owned it is never mistaken for
	// uncovered work; release it again on defer.
	jobs, err := a.scanUncovered(ctx, dir, class == admission.ClassMaintenance, store, policy)
	if err != nil || len(jobs) > 0 {
		_ = slot.Release()
		if err != nil {
			return nil, &DeferredError{Reason: "heavy-work inventory unavailable: " + err.Error()}
		}
		return nil, &DeferredError{Reason: "uncovered heavyweight work: " + strings.Join(jobs, ", ")}
	}

	// Children inherit the nested marker, so a `dot admit` invocation
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

// scanUncovered runs the uncovered-work scan under the resolved settings. A
// bypassed uncovered rule still scans — the findings are named on stderr, so the
// person sees what the limit would have deferred — and then admits.
func (a *adapter) scanUncovered(ctx context.Context, dir string, maintenance bool, store *admission.Store, policy admission.Policy) ([]string, error) {
	jobs, err := a.uncovered(ctx, dir, maintenance, admission.LeasedPIDs(store), policy.Uncovered)
	if err != nil || len(jobs) == 0 {
		return jobs, err
	}
	if !policy.Ignore.Uncovered {
		return jobs, nil
	}
	// The scan is the slowest step between the resolve above and this decision, so
	// an override that ended inside it must not let its findings through: resolve
	// again and judge the bypass on the policy in force now, keeping the jobs as
	// found. The clock is read here rather than taken from the fresh resolve,
	// because the resolver prunes on its own clock and can still see the override
	// as live when the scan outlasted it.
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	if policy.OverrideExpired(now) {
		if policy, err = a.policy(); err != nil {
			return nil, err
		}
	}
	if !policy.Ignore.Uncovered || policy.OverrideExpired(now) {
		return jobs, nil
	}
	admission.NoteIgnoredUncovered(os.Stderr, jobs, policy.IgnoreSource)
	return nil, nil
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
	var candidates []admission.Lease
	for _, lease := range leases {
		// A maintenance scope can hold several leases at once (a configured
		// capacity above one, or an overflow slot a bypass took), so the parent
		// is whichever candidate this child's process tree descends from, not
		// "the only lease on the scope".
		if lease.Scope == scope && lease.Class == class {
			candidates = append(candidates, lease)
		}
	}
	if len(candidates) == 0 {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
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
	now := time.Now()
	for _, parent := range candidates {
		if parent.PID <= 0 || parent.PIDStart == "" || now.After(parent.Deadline) {
			continue
		}
		match, err := watchdog.ProcessStartMatches(probeCtx, runner, parent.PID, parent.PIDStart)
		if err != nil || !match {
			continue
		}
		ancestor, ok := byPID[parent.PID]
		if !ok || ancestor.Started != parent.PIDStart {
			continue
		}
		// `dot admit` may transfer the lease identity to its wrapped child after
		// exec; processHasAncestor accepts a self match only after these identity
		// checks have succeeded.
		if processHasAncestor(childPID, parent.PID, byPID) {
			return true
		}
	}
	return false
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
