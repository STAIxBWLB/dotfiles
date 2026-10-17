package resourceguard

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	execprobe "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

const (
	prebuiltParentHelperEnv = "DOT_TEST_PREBUILT_PARENT_HELPER"
	prebuiltParentRootEnv   = "DOT_TEST_PREBUILT_PARENT_ROOT"
	prebuiltParentPIDEnv    = "DOT_TEST_PREBUILT_PARENT_PID"
)

func prebuiltTestPolicy(snapshot func(context.Context) admission.PressureSnapshot, evaluate func(admission.PressureSnapshot, admission.Thresholds, admission.History, time.Time) admission.Decision) admission.GatePolicy {
	return admission.GatePolicy{
		HistoryFile: admission.PrebuiltHistoryFile,
		Thresholds:  admission.PrebuiltThresholds(),
		Snapshot:    snapshot,
		Evaluate:    evaluate,
	}
}

func TestPrebuiltVerifiedParentReuseRunsProfileGateFirst(t *testing.T) {
	if os.Getenv(prebuiltParentHelperEnv) == "1" {
		return
	}
	root := t.TempDir()
	runner := execprobe.NewProbeRunner()
	started, err := admission.ProcessStart(context.Background(), runner, os.Getpid())
	if err != nil {
		t.Fatalf("read parent process start: %v", err)
	}
	store := admission.NewStore(root, runner)
	parent, _, err := store.Acquire(context.Background(), admission.MaintenanceScope, admission.ClassMaintenance, admission.Lease{
		Owner: "verified-parent@test", PID: os.Getpid(), PIDStart: started,
	})
	if err != nil || parent == nil {
		t.Fatalf("seed parent maintenance lease: %v", err)
	}
	defer func() { _ = parent.Release() }()

	marker := admission.NestedEnvValue(admission.MaintenanceScope, admission.ClassMaintenance)
	cmd := osexec.Command(os.Args[0], "-test.run=^TestPrebuiltParentReuseHelper$")
	cmd.Env = envWith(os.Environ(), prebuiltParentHelperEnv, "1")
	cmd.Env = envWith(cmd.Env, prebuiltParentRootEnv, root)
	cmd.Env = envWith(cmd.Env, prebuiltParentPIDEnv, fmt.Sprint(os.Getpid()))
	cmd.Env = envWith(cmd.Env, admission.NestedEnv, marker)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("authenticated nested prebuilt gate: %v\n%s", err, output)
	}
}

// TestPrebuiltParentReuseHelper runs as a child process so the validator sees
// a real PID/start pair and process ancestry, as it does for dot update.
func TestPrebuiltParentReuseHelper(t *testing.T) {
	if os.Getenv(prebuiltParentHelperEnv) != "1" {
		return
	}
	root := os.Getenv(prebuiltParentRootEnv)
	runner := execprobe.NewProbeRunner()
	store := admission.NewStore(root, runner)
	gateCalls := 0
	scanCalls := 0
	parentPID, err := strconv.Atoi(os.Getenv(prebuiltParentPIDEnv))
	if err != nil || parentPID <= 0 {
		t.Fatalf("invalid parent PID %q", os.Getenv(prebuiltParentPIDEnv))
	}
	a := &adapter{
		root:   func() (string, error) { return root, nil },
		runner: runner,
		parentLease: func(ctx context.Context, got *admission.Store, scope, class string, childPID int) bool {
			if scope != admission.MaintenanceScope || class != admission.ClassMaintenance || childPID != os.Getpid() {
				return false
			}
			return hasLiveParentLease(ctx, runner, got, scope, class, childPID)
		},
		lease: func(context.Context, *execprobe.Runner, string) (admission.Lease, error) {
			return admission.Lease{}, errors.New("valid parent should have reused its lease")
		},
		monitor: func() *admission.Monitor {
			return &admission.Monitor{GOOS: "linux", SnapshotFunc: func(context.Context, *admission.Monitor) admission.PressureSnapshot {
				return admission.PressureSnapshot{Platform: "linux", MemoryLevel: admission.MemoryNormal, MemoryAvailable: true, LoadAvailable: true, NumCPU: 8}
			}}
		},
		uncovered: func(_ context.Context, _ string, maintenance bool, leased []int) ([]string, error) {
			scanCalls++
			if !maintenance {
				t.Error("parent reentry inventory scan was not host-wide")
			}
			foundParent := false
			for _, pid := range leased {
				foundParent = foundParent || pid == parentPID
			}
			if !foundParent {
				t.Errorf("fresh inventory did not exclude parent PID %d: %v", parentPID, leased)
			}
			return nil, nil
		},
	}
	policy := prebuiltTestPolicy(func(context.Context) admission.PressureSnapshot {
		return admission.PressureSnapshot{Platform: "linux", MemoryLevel: admission.MemoryNormal, MemoryAvailable: true, LoadAvailable: true, NumCPU: 8}
	}, func(_ admission.PressureSnapshot, _ admission.Thresholds, history admission.History, now time.Time) admission.Decision {
		gateCalls++
		return admission.Decision{Admit: true, Next: history}
	})
	release, err := a.acquirePolicy(context.Background(), Options{Purpose: "verified nested update", ScopeKey: "tooling"}, policy, nil)
	if err != nil {
		t.Fatalf("acquirePolicy with authenticated parent: %v", err)
	}
	release()
	if gateCalls != 1 {
		t.Fatalf("profile evaluator calls = %d, want exactly one before parent reuse", gateCalls)
	}
	if scanCalls != 1 {
		t.Fatalf("fresh inventory scans = %d, want one before parent reuse", scanCalls)
	}
	leases, err := store.ListLeases()
	if err != nil || len(leases) != 1 || leases[0].Owner != "verified-parent@test" {
		t.Fatalf("nested path changed parent lease: leases=%+v err=%v", leases, err)
	}
}

func TestPrebuiltParentReentryDefersOnFreshMaintenanceInventory(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	a, store := testAdapter(t, &snap, &none)
	marker := admission.NestedEnvValue(admission.MaintenanceScope, admission.ClassMaintenance)
	t.Setenv(admission.NestedEnv, marker)
	parent, _, err := store.Acquire(context.Background(), admission.MaintenanceScope, admission.ClassMaintenance, admission.Lease{
		Owner: "verified-parent@test", PID: os.Getpid(), PIDStart: "parent-start",
	})
	if err != nil || parent == nil {
		t.Fatalf("seed parent maintenance lease: %v", err)
	}
	defer func() { _ = parent.Release() }()
	a.parentLease = func(context.Context, *admission.Store, string, string, int) bool { return true }
	gateCalls, leaseCalls, scanCalls := 0, 0, 0
	a.lease = func(context.Context, *execprobe.Runner, string) (admission.Lease, error) {
		leaseCalls++
		return admission.Lease{}, errors.New("authenticated parent path must reuse its lease")
	}
	a.uncovered = func(_ context.Context, _ string, maintenance bool, leased []int) ([]string, error) {
		scanCalls++
		if !maintenance {
			t.Error("reentry inventory scan was not host-wide")
		}
		foundParent := false
		for _, pid := range leased {
			foundParent = foundParent || pid == os.Getpid()
		}
		if !foundParent {
			t.Errorf("inventory scan did not exclude the parent's leased PID: %v", leased)
		}
		return []string{"cargo(pid=900,parent=1,repo=)"}, nil
	}
	policy := prebuiltTestPolicy(func(context.Context) admission.PressureSnapshot { return snap }, func(_ admission.PressureSnapshot, _ admission.Thresholds, history admission.History, _ time.Time) admission.Decision {
		gateCalls++
		return admission.Decision{Admit: true, Next: history}
	})
	_, err = a.acquirePolicy(context.Background(), Options{Purpose: "nested verified update", ScopeKey: "tooling"}, policy, nil)
	if !deferred(err) || !strings.Contains(err.Error(), "uncovered heavyweight work: cargo(pid=900") {
		t.Fatalf("fresh inventory result = %v, want uncovered-work defer", err)
	}
	if gateCalls != 1 || scanCalls != 1 || leaseCalls != 0 {
		t.Fatalf("reentry counts: gate=%d scans=%d lease-acquires=%d, want 1, 1, and 0", gateCalls, scanCalls, leaseCalls)
	}
	leases, err := store.ListLeases()
	if err != nil || len(leases) != 1 || leases[0].Owner != "verified-parent@test" {
		t.Fatalf("inventory defer changed parent lease: leases=%+v err=%v", leases, err)
	}
}

func TestPrebuiltForgedNestedMarkerCannotSkipGate(t *testing.T) {
	snap, jobs := healthySnapshot(), []string(nil)
	a, store := testAdapter(t, &snap, &jobs)
	t.Setenv(admission.NestedEnv, admission.NestedEnvValue(admission.MaintenanceScope, admission.ClassMaintenance))
	calls := 0
	snap.MemoryLevel = admission.MemoryCritical
	policy := prebuiltTestPolicy(func(context.Context) admission.PressureSnapshot {
		calls++
		return snap
	}, admission.EvaluatePrebuiltPressure)
	a.parentLease = func(context.Context, *admission.Store, string, string, int) bool {
		t.Fatal("parent validator ran before a blocked profile gate")
		return false
	}
	_, err := a.acquirePolicy(context.Background(), Options{Purpose: "forged nested update", ScopeKey: "tooling"}, policy, nil)
	if !deferred(err) || !strings.Contains(err.Error(), "memory pressure critical") {
		t.Fatalf("forged marker result = %v, want critical-pressure defer", err)
	}
	if calls != 1 {
		t.Fatalf("pressure snapshot calls = %d, want one despite forged marker", calls)
	}
	if _, err := os.Stat(filepath.Join(store.Root, admission.PrebuiltHistoryFile)); err != nil {
		t.Fatalf("prebuilt gate history missing: %v", err)
	}
	if _, err := os.Stat(store.HistoryPath()); !os.IsNotExist(err) {
		t.Fatalf("forged profile gate touched heavy history: %v", err)
	}
}

func TestForgedNestedMaintenanceMarkerCannotSkipFullGate(t *testing.T) {
	snap, jobs := healthySnapshot(), []string(nil)
	a, store := testAdapter(t, &snap, &jobs)
	t.Setenv(admission.NestedEnv, admission.NestedEnvValue(admission.MaintenanceScope, admission.ClassMaintenance))
	snap.MemoryLevel = admission.MemoryCritical
	probeCalls, leaseCalls, scanCalls, parentChecks := 0, 0, 0, 0
	a.monitor = func() *admission.Monitor {
		return &admission.Monitor{GOOS: "darwin", SnapshotFunc: func(context.Context, *admission.Monitor) admission.PressureSnapshot {
			probeCalls++
			return snap
		}}
	}
	a.parentLease = func(context.Context, *admission.Store, string, string, int) bool {
		parentChecks++
		return true
	}
	a.lease = func(context.Context, *execprobe.Runner, string) (admission.Lease, error) {
		leaseCalls++
		return admission.Lease{}, nil
	}
	a.uncovered = func(context.Context, string, bool, []int) ([]string, error) {
		scanCalls++
		return nil, nil
	}
	_, err := a.acquire(context.Background(), Options{Purpose: "forged full maintenance", ScopeKey: "tooling"})
	if !deferred(err) || !strings.Contains(err.Error(), "memory pressure critical") {
		t.Fatalf("forged full-maintenance marker = %v, want critical-pressure defer", err)
	}
	if probeCalls != 1 || leaseCalls != 0 || scanCalls != 0 || parentChecks != 0 {
		t.Fatalf("forged-path calls: probes=%d leases=%d scans=%d parent-checks=%d", probeCalls, leaseCalls, scanCalls, parentChecks)
	}
	leases, err := store.ListLeases()
	if err != nil || len(leases) != 0 {
		t.Fatalf("blocked full gate created a lease: leases=%+v err=%v", leases, err)
	}
}

func TestAuthenticatedNestedMaintenanceRunsFullGateAndFreshInventory(t *testing.T) {
	snap, jobs := healthySnapshot(), []string(nil)
	a, store := testAdapter(t, &snap, &jobs)
	t.Setenv(admission.NestedEnv, admission.NestedEnvValue(admission.MaintenanceScope, admission.ClassMaintenance))
	parent, _, err := store.Acquire(context.Background(), admission.MaintenanceScope, admission.ClassMaintenance, admission.Lease{
		Owner: "authenticated-full-parent@test", PID: os.Getpid(), PIDStart: "parent-start",
	})
	if err != nil || parent == nil {
		t.Fatalf("seed full-gate parent lease: %v", err)
	}
	defer func() { _ = parent.Release() }()
	probeCalls, leaseCalls, scanCalls := 0, 0, 0
	a.monitor = func() *admission.Monitor {
		return &admission.Monitor{GOOS: "darwin", SnapshotFunc: func(context.Context, *admission.Monitor) admission.PressureSnapshot {
			probeCalls++
			return snap
		}}
	}
	a.parentLease = func(_ context.Context, got *admission.Store, scope, class string, childPID int) bool {
		return scope == admission.MaintenanceScope && class == admission.ClassMaintenance && childPID == os.Getpid() && got.Root == store.Root
	}
	a.lease = func(context.Context, *execprobe.Runner, string) (admission.Lease, error) {
		leaseCalls++
		return admission.Lease{}, errors.New("authenticated nested maintenance should reuse its parent")
	}
	a.uncovered = func(_ context.Context, _ string, maintenance bool, leased []int) ([]string, error) {
		scanCalls++
		if !maintenance {
			t.Error("full maintenance reentry did not run a host-wide inventory")
		}
		foundParent := false
		for _, pid := range leased {
			foundParent = foundParent || pid == os.Getpid()
		}
		if !foundParent {
			t.Errorf("full maintenance inventory omitted parent PID: %v", leased)
		}
		return nil, nil
	}
	release, err := a.acquire(context.Background(), Options{Purpose: "authenticated full maintenance", ScopeKey: "tooling"})
	if err != nil {
		t.Fatalf("authenticated nested full-gate acquire: %v", err)
	}
	release()
	if probeCalls != 1 || scanCalls != 1 || leaseCalls != 0 {
		t.Fatalf("full reentry calls: probes=%d scans=%d lease-acquires=%d", probeCalls, scanCalls, leaseCalls)
	}
	if _, err := os.Stat(store.HistoryPath()); err != nil {
		t.Fatalf("default full-gate history missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, admission.PrebuiltHistoryFile)); !os.IsNotExist(err) {
		t.Fatalf("full gate wrote prebuilt history: %v", err)
	}
}

func TestNestedHeavyClassStillReusesParentWithoutGate(t *testing.T) {
	snap, jobs := healthySnapshot(), []string(nil)
	a, _ := testAdapter(t, &snap, &jobs)
	t.Setenv(admission.NestedEnv, admission.NestedEnvValue("repo-a", admission.ClassHeavy))
	probeCalls, scanCalls, leaseCalls := 0, 0, 0
	a.monitor = func() *admission.Monitor {
		return &admission.Monitor{GOOS: "darwin", SnapshotFunc: func(context.Context, *admission.Monitor) admission.PressureSnapshot {
			probeCalls++
			snap.MemoryLevel = admission.MemoryCritical
			return snap
		}}
	}
	a.lease = func(context.Context, *execprobe.Runner, string) (admission.Lease, error) {
		leaseCalls++
		return admission.Lease{}, nil
	}
	a.uncovered = func(context.Context, string, bool, []int) ([]string, error) {
		scanCalls++
		return nil, nil
	}
	release, err := a.acquire(context.Background(), Options{ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatalf("nested heavy acquire: %v", err)
	}
	release()
	if probeCalls != 0 || scanCalls != 0 || leaseCalls != 0 {
		t.Fatalf("nested heavy path changed: probes=%d scans=%d lease-acquires=%d", probeCalls, scanCalls, leaseCalls)
	}
}

func TestPrebuiltParentIdentityAndAncestryMismatchesFailClosed(t *testing.T) {
	t.Run("self PID with exact process start", func(t *testing.T) {
		t.Setenv(admission.NestedEnv, admission.NestedEnvValue(admission.MaintenanceScope, admission.ClassMaintenance))
		runner := execprobe.NewProbeRunner()
		started, err := admission.ProcessStart(context.Background(), runner, os.Getpid())
		if err != nil {
			t.Fatalf("read self process start: %v", err)
		}
		store := admission.NewStore(t.TempDir(), runner)
		parent, _, err := store.Acquire(context.Background(), admission.MaintenanceScope, admission.ClassMaintenance, admission.Lease{
			Owner: "wrapped-admit@test", PID: os.Getpid(), PIDStart: started,
		})
		if err != nil || parent == nil {
			t.Fatalf("seed self lease: %v", err)
		}
		defer func() { _ = parent.Release() }()
		if !hasLiveParentLease(context.Background(), runner, store, admission.MaintenanceScope, admission.ClassMaintenance, os.Getpid()) {
			t.Fatal("rejected self lease with an exact live PID/start identity")
		}
	})

	for _, tc := range []struct {
		name  string
		pid   int
		start string
	}{
		{name: "wrong start", pid: os.Getpid(), start: "not-this-process-start"},
		{name: "wrong pid", pid: 2_000_000_000, start: "Mon Sep 28 01:00:00 2026"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(admission.NestedEnv, admission.NestedEnvValue(admission.MaintenanceScope, admission.ClassMaintenance))
			store := admission.NewStore(t.TempDir(), nil)
			parent, _, err := store.Acquire(context.Background(), admission.MaintenanceScope, admission.ClassMaintenance, admission.Lease{
				Owner: "unverified-parent@test", PID: tc.pid, PIDStart: tc.start,
			})
			if err != nil || parent == nil {
				t.Fatalf("seed mismatched parent: %v", err)
			}
			defer func() { _ = parent.Release() }()
			if hasLiveParentLease(context.Background(), execprobe.NewProbeRunner(), store, admission.MaintenanceScope, admission.ClassMaintenance, os.Getpid()) {
				t.Fatal("accepted a parent lease with a mismatched PID/start identity")
			}
		})
	}

	processes := map[int]watchdog.Process{
		101: {PID: 101, PPID: 202, Started: "child"},
		202: {PID: 202, PPID: 1, Started: "unrelated-parent"},
	}
	if processHasAncestor(101, 303, processes) {
		t.Fatal("accepted a PID that is absent from the process ancestry")
	}
	if !processHasAncestor(101, 202, processes) {
		t.Fatal("rejected a present ancestor PID")
	}
	processes[202] = watchdog.Process{PID: 202, PPID: 101, Started: "cycle"}
	if processHasAncestor(101, 303, processes) {
		t.Fatal("accepted an ancestry cycle")
	}
}

func TestPrebuiltContentionCanRetryAfterOwnerReleases(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	a, store := testAdapter(t, &snap, &none)
	t.Setenv(admission.NestedEnv, admission.NestedEnvValue(admission.MaintenanceScope, admission.ClassMaintenance))
	parent, _, err := store.Acquire(context.Background(), admission.MaintenanceScope, admission.ClassMaintenance, admission.Lease{
		Owner: "busy-parent@test", PID: os.Getpid(), PIDStart: "parent-start",
	})
	if err != nil || parent == nil {
		t.Fatalf("seed contention lease: %v", err)
	}
	a.parentLease = func(context.Context, *admission.Store, string, string, int) bool { return false }
	policy := prebuiltTestPolicy(func(context.Context) admission.PressureSnapshot { return snap }, func(_ admission.PressureSnapshot, _ admission.Thresholds, history admission.History, _ time.Time) admission.Decision {
		return admission.Decision{Admit: true, Next: history}
	})
	if _, err := a.acquirePolicy(context.Background(), Options{Purpose: "contending update", ScopeKey: "tooling"}, policy, nil); !deferred(err) || !strings.Contains(err.Error(), "another heavyweight job owns scope") {
		t.Fatalf("contention result = %v, want a maintenance-slot defer", err)
	}
	if err := parent.Release(); err != nil {
		t.Fatal(err)
	}
	release, err := a.acquirePolicy(context.Background(), Options{Purpose: "retried update", ScopeKey: "tooling"}, policy, nil)
	if err != nil {
		t.Fatalf("retry after owner release: %v", err)
	}
	release()
	got, _, err := store.Acquire(context.Background(), admission.MaintenanceScope, admission.ClassMaintenance, admission.Lease{Owner: "post-release-check", PID: os.Getpid(), PIDStart: "check"})
	if err != nil || got == nil {
		t.Fatalf("maintenance slot leaked after retry release: %v", err)
	}
	_ = got.Release()
}

func TestRetryPrebuiltAcquireSuccessCancellationAndContention(t *testing.T) {
	t.Run("warmup then success", func(t *testing.T) {
		calls := 0
		released := false
		release, err := retryPrebuiltAcquire(context.Background(), time.Second, time.Millisecond, func(context.Context) (func(), error) {
			calls++
			if calls < 3 {
				return nil, &DeferredError{Reason: "prebuilt safety window needs fresh samples", prebuiltSampleRetry: true}
			}
			return func() { released = true }, nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("retry result = %v after %d attempts", err, calls)
		}
		release()
		if !released {
			t.Fatal("successful retry release was not returned")
		}
	})
	t.Run("canceled wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		_, err := retryPrebuiltAcquire(ctx, time.Second, time.Hour, func(context.Context) (func(), error) {
			calls++
			return nil, &DeferredError{Reason: "swap activity; restarting the 30-second safety window", prebuiltSampleRetry: true}
		})
		if err == nil || calls != 1 || !strings.Contains(err.Error(), "sample window wait ended") {
			t.Fatalf("canceled retry = %v after %d attempts", err, calls)
		}
	})
	t.Run("contention is not a warmup retry", func(t *testing.T) {
		calls := 0
		_, err := retryPrebuiltAcquire(context.Background(), time.Second, time.Millisecond, func(context.Context) (func(), error) {
			calls++
			return nil, &DeferredError{Reason: fmt.Sprintf("another heavyweight job owns scope %s", admission.MaintenanceScope)}
		})
		if !deferred(err) || calls != 1 {
			t.Fatalf("contention retry = %v after %d attempts", err, calls)
		}
	})
}

func envWith(env []string, key, value string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			filtered = append(filtered, item)
		}
	}
	return append(filtered, prefix+value)
}
