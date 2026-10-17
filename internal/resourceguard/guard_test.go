package resourceguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func healthySnapshot() admission.PressureSnapshot {
	return admission.PressureSnapshot{
		Platform: "darwin", MemoryLevel: admission.MemoryNormal, MemoryAvailable: true, MemoryFreePct: 50,
		ThermalAvailable: true, Load1: 1, NumCPU: 10, LoadAvailable: true,
		IdlePct: 90, IdleAvailable: true, WSScanOK: true,
	}
}

// testAdapter wires the adapter to a temp state root, a fixed snapshot, a
// fixed scope and an empty process table.
func testAdapter(t *testing.T, snap *admission.PressureSnapshot, uncovered *[]string) (*adapter, *admission.Store) {
	t.Helper()
	var none error
	return testAdapterScan(t, snap, uncovered, &none)
}

func testAdapterScan(t *testing.T, snap *admission.PressureSnapshot, uncovered *[]string, scanErr *error) (*adapter, *admission.Store) {
	t.Helper()
	t.Setenv(admission.NestedEnv, "")
	root := t.TempDir()
	a := &adapter{
		root: func() (string, error) { return root, nil },
		monitor: func() *admission.Monitor {
			return &admission.Monitor{GOOS: "darwin", SnapshotFunc: func(context.Context, *admission.Monitor) admission.PressureSnapshot { return *snap }}
		},
		uncovered: func(context.Context, string, bool, []int) ([]string, error) { return *uncovered, *scanErr },
		lease: func(_ context.Context, _ *exec.Runner, dir string) (admission.Lease, error) {
			return admission.Lease{Owner: "adapter@test", PID: os.Getpid(), PIDStart: "Mon Sep 28 01:00:00 2026", CWD: dir}, nil
		},
		resolve:   func(context.Context, *exec.Runner, string) (string, error) { return "repo-a", nil },
		heartbeat: time.Hour,
	}
	return a, admission.NewStore(root, nil)
}

func deferred(err error) bool {
	var d *DeferredError
	return errors.As(err, &d)
}

// AC1: dot admit (a direct store slot) and dot ai run (the adapter) exclude
// each other for the same repository.
func TestAdapterAndDotAdmitShareRepositorySlot(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	a, store := testAdapter(t, &snap, &none)
	ctx := context.Background()

	held, _, err := store.Acquire(ctx, "repo-a", admission.ClassHeavy, admission.Lease{Owner: "admit@test", PID: 4242, PIDStart: "Mon Sep 28 00:00:00 2026"})
	if err != nil || held == nil {
		t.Fatalf("seeding the dot admit slot = %v, %v", held, err)
	}
	if r, err := a.acquire(ctx, Options{ProjectDir: t.TempDir()}); !deferred(err) || !strings.Contains(err.Error(), "admit@test") {
		if r != nil {
			r()
		}
		t.Fatalf("adapter admitted while dot admit held the slot: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}

	release, err := a.acquire(ctx, Options{ProjectDir: t.TempDir(), Purpose: "manual heavyweight command"})
	if err != nil {
		t.Fatal(err)
	}
	got, holder, err := store.Acquire(ctx, "repo-a", admission.ClassHeavy, admission.Lease{Owner: "admit@test", PID: 4242, PIDStart: "x"})
	if err != nil || got != nil || holder == nil || holder.Session != "manual heavyweight command" {
		t.Fatalf("dot admit acquired while the adapter held the slot: slot=%v holder=%+v err=%v", got, holder, err)
	}
	release()
	release() // idempotent
	if got, _, err := store.Acquire(ctx, "repo-a", admission.ClassHeavy, admission.Lease{Owner: "admit@test", PID: 4242, PIDStart: "x"}); err != nil || got == nil {
		t.Fatalf("slot not released: %v, %v", got, err)
	} else {
		_ = got.Release()
	}
}

// AC2: the tooling scope is the host-wide maintenance slot `dot admit
// --class maintenance` takes.
func TestAdapterToolingUsesMaintenanceSlot(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	a, store := testAdapter(t, &snap, &none)
	ctx := context.Background()
	held, _, err := store.Acquire(ctx, admission.MaintenanceScope, admission.ClassMaintenance, admission.Lease{Owner: "brew@test", PID: 4242, PIDStart: "x"})
	if err != nil || held == nil {
		t.Fatalf("seeding the maintenance slot = %v, %v", held, err)
	}
	defer func() { _ = held.Release() }()
	if r, err := a.acquire(ctx, Options{ScopeKey: "tooling"}); !deferred(err) {
		if r != nil {
			r()
		}
		t.Fatalf("tooling update admitted during maintenance: %v", err)
	}
	if r, err := a.acquire(ctx, Options{ProjectDir: t.TempDir()}); err != nil {
		t.Fatalf("a repository slot was blocked by maintenance: %v", err)
	} else {
		r()
	}
	if _, err := a.acquire(ctx, Options{ScopeKey: "other"}); err == nil || deferred(err) {
		t.Fatalf("unknown scope key accepted: %v", err)
	}
}

// AC6 (adapter side): heavy work outside any slot defers.
func TestAdapterDefersOnUncoveredWork(t *testing.T) {
	snap, jobs := healthySnapshot(), []string{"make(pid=77,parent=1,repo=/a/.git)"}
	a, store := testAdapter(t, &snap, &jobs)
	if r, err := a.acquire(context.Background(), Options{ProjectDir: t.TempDir()}); !deferred(err) || !strings.Contains(err.Error(), "uncovered heavyweight work") {
		if r != nil {
			r()
		}
		t.Fatalf("uncovered work did not defer: %v", err)
	}
	// The defer released the slot it took for the scan.
	if got, _, err := store.Acquire(context.Background(), "repo-a", admission.ClassHeavy, admission.Lease{Owner: "admit@test", PID: 4242, PIDStart: "x"}); err != nil || got == nil {
		t.Fatalf("slot leaked after an uncovered-work defer: %v, %v", got, err)
	} else {
		_ = got.Release()
	}
}

// A busy slot defers with its owner even when the holder's own heavy work is
// on the process table: the scan runs only with the slot held.
func TestAdapterBusySlotReportsOwnerNotUncovered(t *testing.T) {
	snap, jobs := healthySnapshot(), []string{"cargo(pid=88,parent=4242,repo=/a/.git)"}
	a, store := testAdapter(t, &snap, &jobs)
	held, _, err := store.Acquire(context.Background(), "repo-a", admission.ClassHeavy, admission.Lease{Owner: "admit@test", PID: 4242, PIDStart: "x"})
	if err != nil || held == nil {
		t.Fatalf("seeding = %v, %v", held, err)
	}
	defer func() { _ = held.Release() }()
	_, err = a.acquire(context.Background(), Options{ProjectDir: t.TempDir()})
	if !deferred(err) || !strings.Contains(err.Error(), "admit@test") || strings.Contains(err.Error(), "uncovered") {
		t.Fatalf("defer = %v, want the slot owner and no uncovered-work reason", err)
	}
}

// The production wiring roots slots at the real user's state root.
func TestNativeUsesUserStateRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := native().root()
	want, werr := admission.UserStateRoot()
	if err != nil || werr != nil || got != want {
		t.Fatalf("native root = %q, %v; want %q, %v", got, err, want, werr)
	}
}

func TestAdapterDefersOnPressure(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	snap.MemoryLevel = admission.MemoryCritical
	a, _ := testAdapter(t, &snap, &none)
	if r, err := a.acquire(context.Background(), Options{ProjectDir: t.TempDir()}); !deferred(err) || !strings.Contains(err.Error(), "memory pressure critical") {
		if r != nil {
			r()
		}
		t.Fatalf("memory pressure did not defer: %v", err)
	}
}

// A job's children see the nested marker, so a nested acquire runs inside
// the held slot; release restores the environment.
func TestAdapterNestedMarker(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	a, _ := testAdapter(t, &snap, &none)
	ctx := context.Background()
	release, err := a.acquire(ctx, Options{ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := os.Getenv(admission.NestedEnv), admission.NestedEnvValue("repo-a", admission.ClassHeavy); got != want {
		t.Fatalf("nested marker = %q, want %q", got, want)
	}
	inner, err := a.acquire(ctx, Options{ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatalf("nested acquire deadlocked on its own slot: %v", err)
	}
	inner()
	release()
	if got := os.Getenv(admission.NestedEnv); got != "" {
		t.Fatalf("nested marker not restored: %q", got)
	}
}

func TestVerifiedPrebuiltPolicyDoesNotTrustNestedMarker(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	snap.VMActivityAvailable = true
	snap.SwapInAvailable = true
	snap.SwapOutAvailable = true
	snap.SwapInBytes = 4096
	snap.SwapOutBytes = 8192
	a, store := testAdapter(t, &snap, &none)
	t.Setenv(admission.NestedEnv, admission.NestedEnvValue(admission.MaintenanceScope, admission.ClassMaintenance))
	policy := admission.GatePolicy{
		HistoryFile: admission.PrebuiltHistoryFile,
		Thresholds:  admission.PrebuiltThresholds(),
		Evaluate:    admission.EvaluatePrebuiltPressure,
	}
	_, err := a.acquirePolicy(context.Background(), Options{Purpose: "verified prebuilt dot update", ScopeKey: "tooling"}, policy, nil)
	if !deferred(err) || !strings.Contains(err.Error(), "prebuilt safety window") {
		t.Fatalf("nested marker bypassed the verified prebuilt gate: %v", err)
	}
	if _, err := os.Stat(store.HistoryPath()); !os.IsNotExist(err) {
		t.Fatalf("prebuilt gate changed heavy history: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, admission.PrebuiltHistoryFile)); err != nil {
		t.Fatalf("prebuilt gate did not persist separate history: %v", err)
	}
}

func TestWaitAcquireStopsAtDeadline(t *testing.T) {
	snap, jobs := healthySnapshot(), []string{"cargo(pid=9,parent=1,repo=)"}
	a, _ := testAdapter(t, &snap, &jobs)
	start := time.Now()
	_, err := a.waitAcquire(context.Background(), Options{ProjectDir: t.TempDir()}, 50*time.Millisecond)
	if !deferred(err) || !strings.Contains(err.Error(), "wait ended") || time.Since(start) > 5*time.Second {
		t.Fatalf("wait = %v after %v", err, time.Since(start))
	}
}

// A failed scan defers with its cause and frees the slot it took.
func TestAdapterDefersOnScanError(t *testing.T) {
	snap, none, scanErr := healthySnapshot(), []string(nil), errors.New("ps timed out")
	a, store := testAdapterScan(t, &snap, &none, &scanErr)
	if _, err := a.acquire(context.Background(), Options{ProjectDir: t.TempDir()}); !deferred(err) || !strings.Contains(err.Error(), "heavy-work inventory unavailable: ps timed out") {
		t.Fatalf("scan error = %v, want a deferral naming it", err)
	}
	if got, _, err := store.Acquire(context.Background(), "repo-a", admission.ClassHeavy, admission.Lease{Owner: "x", PID: 4242, PIDStart: "x"}); err != nil || got == nil {
		t.Fatalf("slot leaked after a scan-error defer: %v, %v", got, err)
	} else {
		_ = got.Release()
	}
}

// A wait that ends while the gate runs is a deferral, not an error from the
// identity probe that follows.
func TestAdapterWaitEndingDuringGateDefers(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	a, _ := testAdapter(t, &snap, &none)
	ctx, cancel := context.WithCancel(context.Background())
	a.monitor = func() *admission.Monitor {
		return &admission.Monitor{GOOS: "darwin", SnapshotFunc: func(context.Context, *admission.Monitor) admission.PressureSnapshot {
			cancel() // the caller's wait ends mid-gate
			return snap
		}}
	}
	a.lease = func(ctx context.Context, _ *exec.Runner, _ string) (admission.Lease, error) {
		if ctx.Err() != nil {
			return admission.Lease{}, errors.New("cannot verify this process's start time")
		}
		return admission.Lease{Owner: "adapter@test", PID: os.Getpid(), PIDStart: "x"}, nil
	}
	if _, err := a.acquire(ctx, Options{ProjectDir: t.TempDir()}); !deferred(err) || !strings.Contains(err.Error(), "outlived the wait") {
		t.Fatalf("mid-gate wait end = %v, want a deferral", err)
	}
}

// Pressure found by a gate that outlives the wait stays the reason given.
func TestAdapterWaitEndKeepsPressureReason(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	snap.MemoryLevel = admission.MemoryCritical
	a, _ := testAdapter(t, &snap, &none)
	ctx, cancel := context.WithCancel(context.Background())
	a.monitor = func() *admission.Monitor {
		return &admission.Monitor{GOOS: "darwin", SnapshotFunc: func(context.Context, *admission.Monitor) admission.PressureSnapshot {
			cancel()
			return snap
		}}
	}
	if _, err := a.acquire(ctx, Options{ProjectDir: t.TempDir()}); !deferred(err) || !strings.Contains(err.Error(), "memory pressure critical") {
		t.Fatalf("mid-gate wait end under pressure = %v, want the pressure reason", err)
	}
}

// The adapter hands the live leases' PIDs to the scan.
func TestAdapterPassesLeasesToScan(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	a, store := testAdapter(t, &snap, &none)
	held, _, err := store.Acquire(context.Background(), admission.MaintenanceScope, admission.ClassMaintenance, admission.Lease{Owner: "tooling@mac", PID: 4242, PIDStart: "x"})
	if err != nil || held == nil {
		t.Fatalf("seeding = %v, %v", held, err)
	}
	defer func() { _ = held.Release() }()
	var got []int
	a.uncovered = func(_ context.Context, _ string, _ bool, leased []int) ([]string, error) {
		got = leased
		return nil, nil
	}
	release, err := a.acquire(context.Background(), Options{ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	release()
	found := false
	for _, pid := range got {
		found = found || pid == 4242
	}
	if !found {
		t.Fatalf("scan got leased PIDs %v, want 4242", got)
	}
}

// A running peer sync holds a background lease (#267): `dot admit status`
// lists it with its start time, its PID counts as a leased owner for the
// uncovered-work scan, and the release removes the lease.
func TestHoldBackgroundSyncHoldsANamedLease(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	a, store := testAdapter(t, &snap, &none)

	release := a.holdBackground(context.Background(), "peer sync")
	leases, err := store.ListLeases()
	if err != nil || len(leases) != 1 {
		t.Fatalf("leases = %v, %v; want the peer sync lease", leases, err)
	}
	if l := leases[0]; l.Scope != "peer sync" || l.Class != admission.ClassBackground || l.PID != os.Getpid() || l.AcquiredAt.IsZero() {
		t.Fatalf("lease = %+v", l)
	}
	if pids := admission.LeasedPIDs(store); len(pids) != 1 || pids[0] != os.Getpid() {
		t.Fatalf("leased PIDs = %v; the sync's process tree must count as owned", pids)
	}
	release()
	release() // idempotent
	if leases, _ := store.ListLeases(); len(leases) != 0 {
		t.Fatalf("lease left after release: %v", leases)
	}
}

// A failure to record the lease never stops the sync.
func TestHoldBackgroundSyncFailsOpen(t *testing.T) {
	snap, none := healthySnapshot(), []string(nil)
	a, _ := testAdapter(t, &snap, &none)
	a.lease = func(context.Context, *exec.Runner, string) (admission.Lease, error) {
		return admission.Lease{}, errors.New("ps unavailable")
	}
	a.holdBackground(context.Background(), "peer sync")()
}
