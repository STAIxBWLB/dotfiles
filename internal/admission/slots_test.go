package admission

import (
	"context"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	runner := exec.NewRunner(false, slog.Default())
	return NewStore(filepath.Join(t.TempDir(), "admission"), runner)
}

func selfLease(t *testing.T, store *Store) Lease {
	t.Helper()
	started := ""
	if res, err := store.Runner.RunQuery(context.Background(), "ps", "-o", "lstart=", "-p", strconv.Itoa(os.Getpid())); err == nil {
		started = strings.Join(strings.Fields(res.Stdout), " ")
	}
	if started == "" {
		t.Skip("ps lstart unavailable; cannot test liveness-guarded slot behavior")
	}
	return Lease{Owner: "test@host", PID: os.Getpid(), PIDStart: started, PGID: os.Getpid(), CWD: "/tmp"}
}

func TestAcquireContentionAndRelease(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	lease := selfLease(t, store)

	slot, holder, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, lease)
	if err != nil || slot == nil || holder != nil {
		t.Fatalf("first acquire = %v, %v, %v", slot, holder, err)
	}
	// Same repo, second contender: deferred with the owner visible.
	got, holder, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, Lease{Owner: "other@host", PID: 4242})
	if err != nil || got != nil {
		t.Fatalf("contending acquire = %v, %v", got, err)
	}
	if holder == nil || holder.Owner != "test@host" || holder.PID != os.Getpid() {
		t.Errorf("holder = %+v, want the first owner visible", holder)
	}
	if err := slot.Release(); err != nil {
		t.Fatal(err)
	}
	got, holder, err = store.Acquire(ctx, "/repo/a/.git", ClassHeavy, Lease{Owner: "other@host", PID: 4242})
	if err != nil || got == nil {
		t.Fatalf("acquire after release = %v, %v, %v", got, holder, err)
	}
}

func TestCrossRepoParallelAndMaintenanceSerial(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	lease := selfLease(t, store)

	slotA, _, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, lease)
	if err != nil || slotA == nil {
		t.Fatalf("repo A acquire = %v, %v", slotA, err)
	}
	slotB, holderB, err := store.Acquire(ctx, "/repo/b/.git", ClassHeavy, lease)
	if err != nil || slotB == nil || holderB != nil {
		t.Fatalf("repo B must admit in parallel: %v, %v, %v", slotB, holderB, err)
	}
	m1, _, err := store.Acquire(ctx, MaintenanceScope, ClassMaintenance, lease)
	if err != nil || m1 == nil {
		t.Fatalf("maintenance acquire = %v, %v", m1, err)
	}
	m2, holderM, err := store.Acquire(ctx, MaintenanceScope, ClassMaintenance, lease)
	if err != nil || m2 != nil || holderM == nil {
		t.Errorf("maintenance slot must serialize across repos: %v, %v, %v", m2, holderM, err)
	}
}

// deadPIDLease returns a lease whose pid is guaranteed gone (a process that
// already exited) with an already-expired deadline.
func deadPIDLease(t *testing.T, store *Store) Lease {
	t.Helper()
	cmd := osexec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot spawn a probe process")
	}
	past := store.now().Add(-2 * store.HeartbeatStaleAfter())
	return Lease{
		Owner: "dead@host", PID: cmd.Process.Pid, PIDStart: "Sun Sep 27 10:00:00 2026",
		AcquiredAt: past, HeartbeatAt: past, Deadline: past,
	}
}

func TestStaleOwnerCrashRecovery(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	dead := deadPIDLease(t, store)

	// Occupy the slot as the dead owner.
	dir := store.slotDir("/repo/a/.git", ClassHeavy)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeLease(dir, dead); err != nil {
		t.Fatal(err)
	}
	// A live contender reclaims it: owner crashed, heartbeat expired.
	lease := selfLease(t, store)
	slot, holder, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, lease)
	if err != nil || slot == nil {
		t.Fatalf("reclaim acquire = %v, %v, %v", slot, holder, err)
	}
	if got := slot.Lease(); got.PID != lease.PID {
		t.Errorf("reclaimed lease pid = %d, want %d", got.PID, lease.PID)
	}
}

// TestPIDReuseNotForceFreed pins the conservative branch: a fresh heartbeat
// is never reclaimed, whatever the pid situation looks like.
func TestPIDReuseNotForceFreed(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	dead := deadPIDLease(t, store)
	dead.Deadline = store.now().Add(time.Minute) // fresh heartbeat
	dead.HeartbeatAt = store.now()

	dir := store.slotDir("/repo/a/.git", ClassHeavy)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeLease(dir, dead); err != nil {
		t.Fatal(err)
	}
	got, holder, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, Lease{Owner: "new@host", PID: 7})
	if err != nil || got != nil {
		t.Fatalf("fresh-heartbeat acquire = %v, %v", got, err)
	}
	if holder == nil || holder.Owner != "dead@host" {
		t.Errorf("holder = %+v, want the original owner reported", holder)
	}
}

// TestLiveOwnerStalledHeartbeatNotReclaimed: the heartbeat expired but the
// owning incarnation is verifiably alive — freeing it would admit a second
// heavy job next to a live one.
func TestLiveOwnerStalledHeartbeatNotReclaimed(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	lease := selfLease(t, store) // own live pid + real lstart
	past := store.now().Add(-2 * store.HeartbeatStaleAfter())
	lease.HeartbeatAt = past
	lease.Deadline = past
	lease.AcquiredAt = past

	dir := store.slotDir("/repo/a/.git", ClassHeavy)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeLease(dir, lease); err != nil {
		t.Fatal(err)
	}
	got, holder, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, Lease{Owner: "new@host", PID: 7})
	if err != nil || got != nil || holder == nil {
		t.Errorf("stalled-but-alive owner = %v, %v, %v; want busy", got, holder, err)
	}
}

func TestHeartbeatAndReleaseOwnershipGuard(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	lease := selfLease(t, store)
	slot, _, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, lease)
	if err != nil || slot == nil {
		t.Fatalf("acquire = %v, %v", slot, err)
	}
	if err := slot.Heartbeat(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	current, err := readLease(store.slotDir("/repo/a/.git", ClassHeavy))
	if err != nil {
		t.Fatal(err)
	}
	if !current.Deadline.After(current.HeartbeatAt) {
		t.Errorf("deadline %v not after heartbeat %v", current.Deadline, current.HeartbeatAt)
	}

	// Simulate a reclaim + re-acquire by someone else: both Heartbeat and
	// Release must refuse to touch it.
	foreign := lease
	foreign.PID = lease.PID + 1000
	foreign.AcquiredAt = store.now().Add(time.Second)
	if err := writeLease(store.slotDir("/repo/a/.git", ClassHeavy), foreign); err != nil {
		t.Fatal(err)
	}
	if err := slot.Heartbeat(); err == nil {
		t.Error("heartbeat after slot changed hands must fail")
	}
	if err := slot.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.slotDir("/repo/a/.git", ClassHeavy)); err != nil {
		t.Error("release after slot changed hands must not remove the new owner's slot")
	}
}

func TestCorruptLeaseReclaimBound(t *testing.T) {
	store := testStore(t)
	dir := store.slotDir("/repo/a/.git", ClassHeavy)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lease.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Fresh directory: corrupt lease cannot prove ownership, but the slot is
	// not yet old enough to reclaim.
	got, _, err := store.Acquire(context.Background(), "/repo/a/.git", ClassHeavy, Lease{Owner: "new@host", PID: 7})
	if err != nil || got != nil {
		t.Errorf("fresh corrupt slot = %v, %v; want busy", got, err)
	}
	// Aged past the stale bound: reclaimable.
	past := store.now().Add(-2 * store.HeartbeatStaleAfter())
	if err := os.Chtimes(dir, past, past); err != nil {
		t.Fatal(err)
	}
	got, _, err = store.Acquire(context.Background(), "/repo/a/.git", ClassHeavy, Lease{Owner: "new@host", PID: 7})
	if err != nil || got == nil {
		t.Errorf("stale corrupt slot = %v, %v; want reclaimed", got, err)
	}
}

func TestNestedScope(t *testing.T) {
	env := NestedEnvValue("/repo/a/.git", ClassHeavy)
	if !NestedScope(env, "/repo/a/.git", ClassHeavy) {
		t.Error("same scope+class not detected as nested")
	}
	if NestedScope(env, "/repo/b/.git", ClassHeavy) {
		t.Error("different scope detected as nested")
	}
	if NestedScope(env, "/repo/a/.git", ClassMaintenance) {
		t.Error("different class detected as nested")
	}
	if NestedScope("{garbage", "/repo/a/.git", ClassHeavy) || NestedScope("", "/repo/a/.git", ClassHeavy) {
		t.Error("garbage/empty env detected as nested")
	}
}

func TestHistoryRoundTripAndGate(t *testing.T) {
	store := testStore(t)
	if _, err := LoadHistory(store.HistoryPath()); err != nil {
		t.Fatalf("missing history must be empty, got %v", err)
	}
	fixed := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	monitor := &Monitor{
		GOOS: "darwin",
		Now:  func() time.Time { return fixed },
		SnapshotFunc: func(context.Context, *Monitor) PressureSnapshot {
			return PressureSnapshot{
				Platform: "darwin", MemoryLevel: MemoryWarn, MemoryAvailable: true,
				ThermalAvailable: true,
				Load1:            1, NumCPU: 10, LoadAvailable: true,
				IdlePct: 90, IdleAvailable: true, WSScanOK: true,
			}
		},
	}
	d, err := store.Gate(context.Background(), monitor, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if d.Admit {
		t.Error("gate admitted under memory warn")
	}
	hist, err := LoadHistory(store.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !hist.DeferActive || !hist.DeferSince.Equal(fixed) {
		t.Errorf("persisted history = %+v, want episode opened at %v", hist, fixed)
	}
	if !hist.LastSampleAt.Equal(fixed) {
		t.Errorf("last sample = %v, want completed-snapshot time %v", hist.LastSampleAt, fixed)
	}
}

func TestLoadLegacyHistoryWithoutSampleTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte(`{"defer_active":true,"defer_since":"2026-09-27T12:00:00Z","recover_since":"2026-09-27T12:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hist, err := LoadHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hist.DeferActive || !hist.LastSampleAt.IsZero() || hist.RecoverSince.IsZero() {
		t.Fatalf("legacy history decode = %+v; want existing fields and no sample timestamp", hist)
	}
	updated := EvaluatePressure(healthyDarwin(), DefaultThresholds(), hist, evalT0)
	if updated.Admit || !updated.Next.DeferActive || !updated.Next.RecoverSince.Equal(evalT0) || !updated.Next.LastSampleAt.Equal(evalT0) {
		t.Errorf("legacy history evaluation = %+v; want preserved defer with recovery restarted at this sample", updated)
	}
}

func TestGateWithPolicyUsesCompletedSnapshotTimeAndOwnHistory(t *testing.T) {
	store := testStore(t)
	fixed := evalT0
	snapshotComplete := false
	monitor := &Monitor{Now: func() time.Time {
		if snapshotComplete {
			return fixed
		}
		return fixed.Add(-time.Minute)
	}}
	d, err := store.GateWithPolicy(context.Background(), monitor, GatePolicy{
		HistoryFile: "history-prebuilt.json",
		Thresholds:  DefaultThresholds(),
		Snapshot: func(context.Context) PressureSnapshot {
			snapshotComplete = true
			return healthyDarwin()
		},
	})
	if err != nil || !d.Admit {
		t.Fatalf("GateWithPolicy = %+v, %v", d, err)
	}
	hist, err := LoadHistory(filepath.Join(store.Root, "history-prebuilt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !hist.LastSampleAt.Equal(fixed) {
		t.Errorf("sample time = %v, want post-snapshot clock %v", hist.LastSampleAt, fixed)
	}
	if _, err := os.Stat(store.HistoryPath()); !os.IsNotExist(err) {
		t.Errorf("policy wrote default history path: %v", err)
	}
	if _, err := store.GateWithPolicy(context.Background(), monitor, GatePolicy{HistoryFile: "../outside.json"}); err == nil {
		t.Error("unsafe history path was accepted")
	}
}

// TestClaimNotifyConcurrent pins the atomic claim: any number of
// concurrent defer handlers for the same episode produce exactly one
// notification winner.
func TestClaimNotifyConcurrent(t *testing.T) {
	store := testStore(t)
	episode := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	const contenders = 16
	results := make(chan bool, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, _, err := store.ClaimNotify("/repo/a/.git", ClassHeavy, episode)
			if err != nil {
				t.Errorf("ClaimNotify: %v", err)
				results <- false
				return
			}
			results <- claimed
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for claimed := range results {
		if claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1", winners)
	}
	// The same episode stays claimed; a new episode claims independently.
	if claimed, _, err := store.ClaimNotify("/repo/a/.git", ClassHeavy, episode); err != nil || claimed {
		t.Errorf("re-claim of the same episode = %v, %v; want false", claimed, err)
	}
	if claimed, _, err := store.ClaimNotify("/repo/a/.git", ClassHeavy, episode.Add(time.Hour)); err != nil || !claimed {
		t.Errorf("new episode claim = %v, %v; want true", claimed, err)
	}
}

func TestListLeases(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	lease := selfLease(t, store)
	if _, _, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, lease); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Acquire(ctx, MaintenanceScope, ClassMaintenance, lease); err != nil {
		t.Fatal(err)
	}
	owners, err := store.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 2 {
		t.Errorf("owners = %d, want 2", len(owners))
	}
	classes := map[string]bool{}
	for _, o := range owners {
		classes[o.Class] = true
	}
	if !classes[ClassHeavy] || !classes[ClassMaintenance] {
		t.Errorf("classes = %v, want heavy and maintenance", classes)
	}
}

// TestConcurrentReclaimExactlyOneWinner: two waiters judging the same
// dead-owner slot stale must not both end up holding it. The reclaim lock
// serializes them; the loser reports busy.
func TestConcurrentReclaimExactlyOneWinner(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	dead := deadPIDLease(t, store)
	dir := store.slotDir("/repo/a/.git", ClassHeavy)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeLease(dir, dead); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		slot   *Slot
		holder *Lease
		err    error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			slot, holder, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy,
				Lease{Owner: "contender", PID: 100 + i, PIDStart: "Sun Sep 27 10:00:00 2026"})
			results <- outcome{slot, holder, err}
		}(i)
	}
	var slots, busy int
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("Acquire: %v", r.err)
		}
		if r.slot != nil {
			slots++
		} else if r.holder != nil {
			busy++
		}
	}
	if slots != 1 || busy != 1 {
		t.Errorf("slots = %d, busy = %d; want exactly one winner and one busy", slots, busy)
	}
	// The surviving lease belongs to the winner; the stale one is gone.
	current, err := readLease(dir)
	if err != nil {
		t.Fatal(err)
	}
	if current.Owner != "contender" {
		t.Errorf("lease owner = %q, want the winning contender", current.Owner)
	}
	// And the reclaim lock was released.
	if _, err := os.Lstat(dir + ".reclaim"); !os.IsNotExist(err) {
		t.Error("reclaim lock left behind")
	}
}

// TestReverifyStaleAbortsOnFreshLease: the re-verification under the
// reclaim lock must stop a reclaim when the judged-stale lease changed
// underneath — here, replaced by a fresh live one between judgment and lock.
func TestReverifyStaleAbortsOnFreshLease(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	dead := deadPIDLease(t, store)
	dir := store.slotDir("/repo/a/.git", ClassHeavy)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeLease(dir, dead); err != nil {
		t.Fatal(err)
	}
	// The lease is judged stale, then a live owner's fresh lease lands
	// before the reclaim lock is taken.
	live := selfLease(t, store)
	live.HeartbeatAt = store.now()
	live.Deadline = store.now().Add(time.Minute)
	if err := writeLease(dir, live); err != nil {
		t.Fatal(err)
	}
	stale, err := store.reverifyStale(ctx, dir, &dead)
	if err != nil || stale {
		t.Errorf("reverifyStale = %v, %v; want false (lease changed)", stale, err)
	}
	// The fresh lease must be untouched.
	current, err := readLease(dir)
	if err != nil || current.PID != live.PID {
		t.Errorf("lease after aborted reclaim = %+v, %v; want the fresh one", current, err)
	}
}

// TestUpdateIdentityBindsChild: after the wrapper rewires the lease to the
// child workload, the lease carries the child's identity, and a stale-owner
// probe against the (live) child reports busy — an orphaned workload keeps
// its slot.
func TestUpdateIdentityBindsChild(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	lease := selfLease(t, store)
	slot, _, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, lease)
	if err != nil || slot == nil {
		t.Fatalf("acquire = %v, %v", slot, err)
	}
	child := osexec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Skip("cannot spawn a child process")
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()
	childStart, err := psStartOf(t, store, child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := slot.UpdateIdentity(child.Process.Pid, childStart); err != nil {
		t.Fatalf("UpdateIdentity: %v", err)
	}
	current, err := readLease(store.slotDir("/repo/a/.git", ClassHeavy))
	if err != nil {
		t.Fatal(err)
	}
	if current.PID != child.Process.Pid || current.PIDStart != childStart {
		t.Errorf("lease identity = pid %d start %q, want child %d %q",
			current.PID, current.PIDStart, child.Process.Pid, childStart)
	}
	// Expire the heartbeat on disk: the probe must still report busy
	// because the child incarnation is alive.
	current.HeartbeatAt = store.now().Add(-2 * store.HeartbeatStaleAfter())
	current.Deadline = current.HeartbeatAt
	if err := writeLease(store.slotDir("/repo/a/.git", ClassHeavy), current); err != nil {
		t.Fatal(err)
	}
	// Slot's in-memory lease no longer matches the expired one on disk, so
	// go through a fresh contender's judgment instead.
	got, holder, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, Lease{Owner: "new@host", PID: 7})
	if err != nil || got != nil || holder == nil {
		t.Errorf("expired lease with live child = %v, %v, %v; want busy", got, holder, err)
	}
}

// psStartOf reads a process's lstart the way the stale probe will compare it.
func psStartOf(t *testing.T, store *Store, pid int) (string, error) {
	t.Helper()
	res, err := store.Runner.RunQuery(context.Background(), "ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	if err != nil {
		return "", err
	}
	started := strings.Join(strings.Fields(res.Stdout), " ")
	if started == "" {
		t.Skip("ps lstart unavailable")
	}
	return started, nil
}

// TestHistoryLockBoundAndTouch pins the stale bound (3 minutes, above the
// ~80s worst-case Darwin probe time) and the touch callback that keeps a
// long-but-live evaluation looking alive.
func TestHistoryLockBoundAndTouch(t *testing.T) {
	if historyLockStaleAfter != 3*time.Minute {
		t.Errorf("historyLockStaleAfter = %v, want 3m", historyLockStaleAfter)
	}
	root := t.TempDir()
	release, touch, busy, err := acquireHistoryLock(root)
	if err != nil || busy {
		t.Fatalf("acquire = %v, %v", busy, err)
	}
	lockPath := filepath.Join(root, "history.lock")
	// Age the lock by two minutes: still inside the bound, still busy for
	// a second acquirer.
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	_, _, busy, err = acquireHistoryLock(root)
	if err != nil || !busy {
		t.Errorf("2m-old lock = busy %v, %v; want busy", busy, err)
	}
	// Touch refreshes it to now.
	touch()
	info, err := os.Lstat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > 10*time.Second {
		t.Errorf("lock mtime after touch = %v old", time.Since(info.ModTime()))
	}
	release()
	// A lock aged past the bound is reclaimed.
	release2, _, busy, err := acquireHistoryLock(root)
	if err != nil || busy {
		t.Fatalf("re-acquire after release = %v, %v", busy, err)
	}
	older := time.Now().Add(-(historyLockStaleAfter + time.Minute))
	if err := os.Chtimes(lockPath, older, older); err != nil {
		t.Fatal(err)
	}
	release2()
	release3, _, busy, err := acquireHistoryLock(root)
	if err != nil || busy {
		t.Errorf("past-bound lock = busy %v, %v; want reclaimed", busy, err)
	}
	release3()
}

// TestGateHoldsLockDuringSnapshot: the lock exists while the snapshot seam
// runs and is gone after Gate returns; a concurrent Gate during the
// snapshot defers instead of interleaving history.
func TestGateHoldsLockDuringSnapshot(t *testing.T) {
	store := testStore(t)
	lockPath := filepath.Join(store.Root, "history.lock")
	fixed := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	inSnapshot := make(chan struct{})
	unblock := make(chan struct{})
	monitor := &Monitor{
		GOOS: "linux",
		Now:  func() time.Time { return fixed },
		SnapshotFunc: func(context.Context, *Monitor) PressureSnapshot {
			if _, err := os.Lstat(lockPath); err != nil {
				t.Errorf("history lock missing during snapshot: %v", err)
			}
			close(inSnapshot)
			<-unblock
			return PressureSnapshot{Platform: "linux", MemoryLevel: MemoryNormal, MemoryAvailable: true, Load1: 1, NumCPU: 8, LoadAvailable: true}
		},
	}
	done := make(chan Decision, 1)
	go func() {
		d, err := store.Gate(context.Background(), monitor, DefaultThresholds())
		if err != nil {
			t.Errorf("Gate: %v", err)
		}
		done <- d
	}()
	<-inSnapshot
	second, err := store.GateWithPolicy(context.Background(), monitor, GatePolicy{
		HistoryFile: "history-prebuilt.json",
		Thresholds:  DefaultThresholds(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !IsGateBusy(second) {
		t.Errorf("custom policy did not share the root history lock: %+v", second)
	}
	close(unblock)
	if second.Admit {
		t.Error("concurrent Gate admitted while the lock was held")
	}
	if d := <-done; !d.Admit {
		t.Errorf("first Gate deferred: %v", d.Reasons)
	}
	if _, err := os.Lstat(lockPath); !os.IsNotExist(err) {
		t.Error("history lock left behind after Gate")
	}
}
