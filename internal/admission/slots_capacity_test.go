package admission

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// AC1: index 0 keeps the directory name a state root written before slots were
// configurable already uses, so old and new state read each other.
func TestSlotDirectoriesKeepIndexZeroName(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	index0 := store.slotDir("/repo/a/.git", ClassHeavy, 0)
	if !regexp.MustCompile(`^heavy-[0-9a-f]{16}$`).MatchString(filepath.Base(index0)) {
		t.Errorf("index 0 dir = %s, want the unsuffixed class-hash name", filepath.Base(index0))
	}
	index1 := store.slotDir("/repo/a/.git", ClassHeavy, 1)
	if filepath.Base(index1) != filepath.Base(index0)+"-1" {
		t.Errorf("index 1 dir = %s, want %s-1", filepath.Base(index1), filepath.Base(index0))
	}
	if index0 == index1 {
		t.Error("index 0 and 1 share a directory")
	}
}

// AC2: slots.heavy_per_repo N admits N concurrent leases and defers the next.
func TestCapacityAdmitsConfiguredCount(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	lease := selfLease(t, store)

	var held []*Slot
	for index := 0; index < 2; index++ {
		slot, holder, err := store.AcquireWithOptions(ctx, "/repo/a/.git", ClassHeavy, lease, AcquireOptions{Capacity: 2})
		if err != nil || slot == nil {
			t.Fatalf("acquire %d = %v, %v, %v", index, slot, holder, err)
		}
		if got := slot.Lease().Index; got != index {
			t.Errorf("acquire %d took index %d, want %d", index, got, index)
		}
		held = append(held, slot)
	}
	defer func() {
		for _, slot := range held {
			_ = slot.Release()
		}
	}()
	if _, holder, err := store.AcquireWithOptions(ctx, "/repo/a/.git", ClassHeavy, lease, AcquireOptions{Capacity: 2}); err != nil || holder == nil {
		t.Fatalf("third acquire = %v, %v, want the holder of the first busy index", holder, err)
	}
}

// AC4: the slot bypass still takes a lease, in the first free overflow slot
// past the configured capacity, and never without one.
func TestOverflowTakesLeasePastCapacity(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	lease := selfLease(t, store)

	first, _, err := store.AcquireWithOptions(ctx, "/repo/a/.git", ClassHeavy, lease, AcquireOptions{Capacity: 1})
	if err != nil || first == nil {
		t.Fatalf("first acquire = %v, %v", first, err)
	}
	defer func() { _ = first.Release() }()

	overflow, _, err := store.AcquireWithOptions(ctx, "/repo/a/.git", ClassHeavy, lease, AcquireOptions{Capacity: 1, Overflow: true})
	if err != nil || overflow == nil {
		t.Fatalf("overflow acquire = %v, %v", overflow, err)
	}
	defer func() { _ = overflow.Release() }()
	if got := overflow.Lease().Index; got != 1 {
		t.Errorf("overflow index = %d, want 1 (the first slot past capacity 1)", got)
	}
	leases, err := store.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 2 {
		t.Fatalf("leases = %d, want both the held and the overflow lease listed", len(leases))
	}
	// A second overflow goes to the next index, never reusing a held one.
	next, _, err := store.AcquireWithOptions(ctx, "/repo/a/.git", ClassHeavy, lease, AcquireOptions{Capacity: 1, Overflow: true})
	if err != nil || next == nil || next.Lease().Index != 2 {
		if next != nil {
			_ = next.Release()
		}
		t.Fatalf("second overflow = %v, %v, want index 2", next, err)
	}
	_ = next.Release()
}

// AC5: stale-lease reclaim works per index, not just on index 0.
func TestStaleLeaseReclaimedOnNonZeroIndex(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	dead := deadPIDLease(t, store)

	// Index 0 is held by a live owner, so the fresh contender reaches the
	// stale lease at index 1.
	held, _, err := store.AcquireWithOptions(ctx, "/repo/a/.git", ClassHeavy, selfLease(t, store), AcquireOptions{Capacity: 2})
	if err != nil || held == nil {
		t.Fatalf("seeding index 0 = %v, %v", held, err)
	}
	defer func() { _ = held.Release() }()

	dir := store.slotDir("/repo/a/.git", ClassHeavy, 1)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeLease(dir, dead); err != nil {
		t.Fatal(err)
	}
	lease := selfLease(t, store)
	slot, holder, err := store.AcquireWithOptions(ctx, "/repo/a/.git", ClassHeavy, lease, AcquireOptions{Capacity: 2})
	if err != nil || slot == nil {
		t.Fatalf("reclaim acquire = %v, %v, %v", slot, holder, err)
	}
	defer func() { _ = slot.Release() }()
	if got := slot.Lease().Index; got != 1 {
		t.Errorf("index = %d, want the reclaimed index 1", got)
	}
	if got := slot.Lease().PID; got != lease.PID {
		t.Errorf("reclaimed pid = %d, want %d", got, lease.PID)
	}
}

// An expired override leaves the slot capacity alone: a bypass is not a
// permission to run leaseless, and the lease still names the overflow index.
func TestOverflowLeaseRecordsIndexInFile(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	// Index 0 is taken, so the overflow acquire lands on index 1.
	held, _, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, selfLease(t, store))
	if err != nil || held == nil {
		t.Fatalf("seeding index 0 = %v, %v", held, err)
	}
	lease := selfLease(t, store)
	slot, _, err := store.AcquireWithOptions(ctx, "/repo/a/.git", ClassHeavy, lease, AcquireOptions{Capacity: 1, Overflow: true})
	if err != nil || slot == nil {
		t.Fatalf("acquire = %v, %v", slot, err)
	}
	// A fresh index-0 lease carries no index field at all, so an ordinary
	// lease record is byte-identical to before.
	data, err := os.ReadFile(filepath.Join(store.slotDir("/repo/a/.git", ClassHeavy, 1), "lease.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, `"index": 1`) {
		t.Errorf("overflow lease file = %s, want the index recorded", got)
	}
	if err := slot.Release(); err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("releasing the seeded index 0: %v", err)
	}
	plain, _, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, lease)
	if err != nil || plain == nil {
		t.Fatalf("plain acquire = %v, %v", plain, err)
	}
	defer func() { _ = plain.Release() }()
	data, err = os.ReadFile(filepath.Join(store.slotDir("/repo/a/.git", ClassHeavy, 0), "lease.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"index"`) {
		t.Errorf("index-0 lease file = %s, want no index field", data)
	}
}

// Heartbeat and release act on the index the slot holds.
func TestOverflowSlotHeartbeatAndRelease(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	// Index 0 is taken, so the overflow acquire lands on index 1.
	held, _, err := store.Acquire(ctx, "/repo/a/.git", ClassHeavy, selfLease(t, store))
	if err != nil || held == nil {
		t.Fatalf("seeding index 0 = %v, %v", held, err)
	}
	lease := selfLease(t, store)
	slot, _, err := store.AcquireWithOptions(ctx, "/repo/a/.git", ClassHeavy, lease, AcquireOptions{Capacity: 1, Overflow: true})
	if err != nil || slot == nil {
		t.Fatalf("acquire = %v, %v", slot, err)
	}
	if err := slot.Heartbeat(); err != nil {
		t.Fatalf("heartbeat on the overflow slot: %v", err)
	}
	if err := slot.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(store.slotDir("/repo/a/.git", ClassHeavy, 1)); !os.IsNotExist(err) {
		t.Errorf("overflow slot survived release: %v", err)
	}
}

// A background lease never overflows: dot's own sync holds exactly one slot.
func TestBackgroundClassUsesCapacityOne(t *testing.T) {
	capacity := DefaultPolicy().Slots
	if got := capacity.For(ClassBackground); got != 1 {
		t.Errorf("background capacity = %d, want 1", got)
	}
	if got := capacity.For(ClassMaintenance); got != DefaultMaintenanceSlots {
		t.Errorf("maintenance capacity = %d, want %d", got, DefaultMaintenanceSlots)
	}
}

// Capacity counts the live leases of the whole scope, not the indexes one
// caller walks. The bypass can leave a lease past capacity, and a lowered
// capacity or an expired relax override can strand one at a higher index: with
// index 0 free and index 1 held, a capacity-1 caller used to take index 0 and
// run a second heavyweight job beside the first.
func TestCapacityCountsLeasesAtHigherIndexes(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	scope := "/repo/a/.git"

	a, _, err := store.AcquireWithOptions(ctx, scope, ClassHeavy, selfLease(t, store), AcquireOptions{Capacity: 1})
	if err != nil || a == nil {
		t.Fatalf("seeding index 0 = %v, %v", a, err)
	}
	b, _, err := store.AcquireWithOptions(ctx, scope, ClassHeavy, selfLease(t, store), AcquireOptions{Capacity: 1, Overflow: true})
	if err != nil || b == nil {
		t.Fatalf("overflow acquire = %v, %v", b, err)
	}
	defer func() { _ = b.Release() }()
	if got := b.Lease().Index; got != 1 {
		t.Fatalf("overflow index = %d, want 1", got)
	}
	if err := a.Release(); err != nil {
		t.Fatalf("releasing index 0: %v", err)
	}

	slot, holder, err := store.AcquireWithOptions(ctx, scope, ClassHeavy, selfLease(t, store), AcquireOptions{Capacity: 1})
	if err != nil {
		t.Fatalf("acquire at capacity 1 = %v, %v", holder, err)
	}
	if slot != nil {
		index := slot.Lease().Index
		_ = slot.Release()
		t.Fatalf("acquire took index %d while index 1 is live; want a defer at capacity 1", index)
	}
	if holder == nil || holder.Index != 1 {
		t.Fatalf("defer holder = %+v, want the live lease at index 1", holder)
	}
	leases, err := store.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 {
		t.Fatalf("leases = %d, want the single live lease", len(leases))
	}

	// The count is of live leases, not of held indexes: one lease at index 1 is
	// within a capacity of 2, and the new job takes the lowest free index.
	room, _, err := store.AcquireWithOptions(ctx, scope, ClassHeavy, selfLease(t, store), AcquireOptions{Capacity: 2})
	if err != nil || room == nil {
		t.Fatalf("acquire at capacity 2 = %v, %v", room, err)
	}
	if got := room.Lease().Index; got != 0 {
		t.Errorf("capacity-2 acquire took index %d, want the lowest free index 0", got)
	}
	if err := room.Release(); err != nil {
		t.Fatalf("releasing index 0: %v", err)
	}

	// A bypass still runs, next to the stranded lease, and it lands in an overflow
	// index: the notice, the JSON "ignored" set and the resourceguard adapter key
	// on the index, so a bypass that quietly took the free index 0 would report
	// nothing while two jobs ran.
	bypass, _, err := store.AcquireWithOptions(ctx, scope, ClassHeavy, selfLease(t, store), AcquireOptions{Capacity: 1, Overflow: true})
	if err != nil || bypass == nil {
		t.Fatalf("bypass acquire = %v, %v", bypass, err)
	}
	defer func() { _ = bypass.Release() }()
	if got := bypass.Lease().Index; got < 1 {
		t.Errorf("bypass index = %d, want an overflow index at or past the capacity of 1", got)
	}
	leases, err = store.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 2 {
		t.Fatalf("leases = %d, want the stranded lease and the bypass", len(leases))
	}
}
