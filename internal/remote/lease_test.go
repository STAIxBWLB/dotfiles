package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
)

func testLease() admission.Lease {
	return admission.Lease{Owner: "me@mac", PID: 4242, PIDStart: "Mon Aug 10 10:00:00 2026", PGID: 4242, CWD: "/tmp"}
}

func testStore(t *testing.T) *admission.Store {
	t.Helper()
	return admission.NewStore(t.TempDir(), nil)
}

func TestPathScopeIsTheCheckout(t *testing.T) {
	if got := PathScope("/Users/me/workspace/work/dev/one/"); got != "/Users/me/workspace/work/dev/one" {
		t.Errorf("PathScope = %q, want the cleaned path", got)
	}
	if HostScope("buildbox") == PathScope("buildbox") {
		t.Error("the host scope collides with a path scope")
	}
}

func TestOneRunPerMirrorPath(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	host := testHost()
	path := "/Users/me/workspace/work/dev/one"

	slot, err := AcquirePath(ctx, store, testLease(), host, path, 0)
	if err != nil || slot == nil {
		t.Fatalf("first acquire = %v, %v", slot, err)
	}
	_, err = AcquirePath(ctx, store, testLease(), host, path, 0)
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("second acquire = %v, want a defer", err)
	}
	if deferred.Outcome.Class != admission.ClassRemote || deferred.Outcome.Scope != path {
		t.Errorf("outcome = %+v, want class remote and the path scope", deferred.Outcome)
	}
	if !strings.Contains(deferred.Outcome.Reason, "another run of "+path+" is active") {
		t.Errorf("reason = %q", deferred.Outcome.Reason)
	}
	if deferred.Outcome.RetryAfterSeconds < 1 {
		t.Errorf("retry_after = %d, want at least one second", deferred.Outcome.RetryAfterSeconds)
	}
	if err := slot.Release(); err != nil {
		t.Fatal(err)
	}
	// The record carries the keys `dot admit` uses.
	data, err := json.Marshal(deferred.Outcome)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"outcome":"deferred"`, `"scope"`, `"class":"remote"`, `"reason"`, `"retry_after"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("JSON record is missing %s: %s", key, data)
		}
	}
}

func TestWorktreesOfOneRepoRunSideBySide(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	host := testHost()
	main, worktree := "/Users/me/repo", "/Users/me/repo/.worktrees/feature"
	first, err := AcquirePath(ctx, store, testLease(), host, main, 0)
	if err != nil || first == nil {
		t.Fatalf("main checkout acquire = %v", err)
	}
	defer func() { _ = first.Release() }()
	second, err := AcquirePath(ctx, store, testLease(), host, worktree, 0)
	if err != nil || second == nil {
		t.Fatalf("worktree acquire = %v, %v, want it to run beside the main checkout", second, err)
	}
	_ = second.Release()
}

func TestHostLeaseCountsMaxRunsAndRecordsTheHost(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	host := testHost()
	host.MaxRuns = 2

	held := make([]*admission.Slot, 0, 2)
	for i := 0; i < 2; i++ {
		slot, err := AcquireHost(ctx, store, testLease(), host, 0, false)
		if err != nil || slot == nil {
			t.Fatalf("host acquire %d = %v", i, err)
		}
		if slot.Lease().Host != host.Name {
			t.Errorf("lease host = %q, want %q", slot.Lease().Host, host.Name)
		}
		held = append(held, slot)
	}
	_, err := AcquireHost(ctx, store, testLease(), host, 0, false)
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("third host acquire = %v, want a defer", err)
	}
	if !strings.Contains(deferred.Outcome.Reason, "2 runs are already active on buildbox") {
		t.Errorf("reason = %q", deferred.Outcome.Reason)
	}
	if deferred.Outcome.Scope != HostScope(host.Name) {
		t.Errorf("scope = %q, want the host scope", deferred.Outcome.Scope)
	}
	for _, slot := range held {
		_ = slot.Release()
	}
	if slot, err := AcquireHost(ctx, store, testLease(), host, 0, false); err != nil || slot == nil {
		t.Fatalf("acquire after release = %v, %v", slot, err)
	}
}

// `--ignore slot` relaxes the host's max_runs and nothing else: a second run of
// one mirror would `rsync --delete` into a mirror that is in use, whatever the
// ignore set says.
func TestTheSlotBypassReachesTheHostLeaseOnly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	host := testHost()
	host.MaxRuns = 1
	path := "/Users/me/workspace/work/dev/one"
	firstPath, err := AcquirePath(ctx, store, testLease(), host, path, 0)
	if err != nil || firstPath == nil {
		t.Fatalf("first path acquire = %v", err)
	}
	defer func() { _ = firstPath.Release() }()
	firstHost, err := AcquireHost(ctx, store, testLease(), host, 0, false)
	if err != nil || firstHost == nil {
		t.Fatalf("first host acquire = %v", err)
	}
	defer func() { _ = firstHost.Release() }()

	// The path lease is held, and there is no way to take a second one.
	if _, err := AcquirePath(ctx, store, testLease(), host, path, 0); err == nil {
		t.Error("a second path lease was granted; the bypass must not reach this scope")
	} else {
		var deferred *Deferred
		if !errors.As(err, &deferred) {
			t.Errorf("second path acquire = %v, want a defer", err)
		}
	}
	// The host lease is the one the bypass relaxes, and the extra lease says so
	// by its index.
	overflow, err := AcquireHost(ctx, store, testLease(), host, 0, true)
	if err != nil || overflow == nil {
		t.Fatalf("bypassed host acquire = %v, %v", overflow, err)
	}
	defer func() { _ = overflow.Release() }()
	if got := overflow.Lease().Index; got < 1 {
		t.Errorf("overflow index = %d, want the lease past capacity so the bypass is visible", got)
	}
}

func TestWaitDoesNotRetryPastItsBudget(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	host := testHost()
	path := "/Users/me/workspace/work/dev/one"
	first, err := AcquirePath(ctx, store, testLease(), host, path, 0)
	if err != nil || first == nil {
		t.Fatalf("first acquire = %v", err)
	}
	defer func() { _ = first.Release() }()
	// The first backoff is longer than this wait, so the acquire answers at
	// once instead of sleeping past its deadline.
	started := time.Now()
	_, err = AcquirePath(ctx, store, testLease(), host, path, 50*time.Millisecond)
	var deferred *Deferred
	if !errors.As(err, &deferred) {
		t.Fatalf("acquire = %v, want a defer", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("waited %s for a 50ms budget", elapsed)
	}
}

func TestLeasesReleaseBothAndStopTheHeartbeat(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	host := testHost()
	path := "/Users/me/workspace/work/dev/one"
	pathSlot, err := AcquirePath(ctx, store, testLease(), host, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	hostSlot, err := AcquireHost(ctx, store, testLease(), host, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	leases := &Leases{Path: pathSlot, Host: hostSlot, stop: heartbeat(pathSlot, hostSlot)}
	leases.Release()
	left, err := store.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("leases left behind: %+v", left)
	}
	// A second release is a no-op rather than a double free.
	leases.Release()
}
