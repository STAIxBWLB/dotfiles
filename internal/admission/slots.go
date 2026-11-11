package admission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// Slot classes.
const (
	// ClassHeavy is the per-repo build/test/index slot: one per scope.
	ClassHeavy = "heavy"
	// ClassMaintenance is the host-wide install/update slot: one shared
	// slot regardless of which repo the work started in.
	ClassMaintenance = "maintenance"
	// ClassBackground is dot's own peer sync and mirror push (#267): one slot
	// per purpose and never gated. It only makes the running sync visible: its
	// process tree is a leased owner, so the uncovered-work scan counts its
	// rsync and ssh children as covered for every other class.
	ClassBackground = "background"
)

// DefaultHeartbeatInterval is how often a running job refreshes its lease.
// The stale bound is twice this: one missed heartbeat never frees a slot.
const DefaultHeartbeatInterval = 30 * time.Second

const heartbeatStaleFactor = 2

// Environment seams.
const (
	// NestedEnv marks a process as the child of an admitted job. Its value
	// is a JSON {"scope":...,"class":...} object; a child `dot admit` for
	// the same scope and class runs without re-acquiring the slot its
	// parent already holds.
	NestedEnv = "DOT_ADMISSION_SCOPE"
	// OwnerEnv overrides the lease owner label (default user@host). It is a
	// display/ownership label only; liveness is always pid + start time.
	OwnerEnv = "DOT_ADMISSION_OWNER"
	// SessionEnv carries a session identifier into the lease for status
	// visibility.
	SessionEnv = "DOT_ADMISSION_SESSION"
)

// Lease is the ownership record inside a slot directory. PID and PIDStart
// together identify the owning process incarnation (PID reuse safe, the same
// rule the watchdog reaper enforces); the deadline is heartbeat_at plus the
// stale factor and is what an owner crash actually expires.
type Lease struct {
	Owner       string    `json:"owner"`
	Session     string    `json:"session,omitempty"`
	PID         int       `json:"pid"`
	PIDStart    string    `json:"pid_start"`
	PGID        int       `json:"pgid"`
	Class       string    `json:"class"`
	Scope       string    `json:"scope"`
	CWD         string    `json:"cwd"`
	AcquiredAt  time.Time `json:"acquired_at"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
	Deadline    time.Time `json:"deadline"`
	// Index is the slot within its (scope, class): 0 is the original slot,
	// higher indexes are the configured extra capacity and an overflow slot a
	// bypass took. It is omitted at 0 so an existing lease file is unchanged.
	Index int `json:"index,omitempty"`
}

// Store owns the admission state root (~/.local/state/dot/admission/): the
// slot directories (one per class+scope), the pressure history, and the
// per-scope notify dedup marks.
type Store struct {
	Root   string
	Runner *exec.Runner // ps liveness probes
	Now    func() time.Time
	// HeartbeatInterval defaults to DefaultHeartbeatInterval when zero.
	HeartbeatInterval time.Duration
}

// DefaultStateRoot is the state root for the invoking user.
func DefaultStateRoot(home string) string {
	return filepath.Join(home, ".local", "state", "dot", "admission")
}

// NewStore builds a store rooted at root.
func NewStore(root string, runner *exec.Runner) *Store {
	return &Store{Root: root, Runner: runner}
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) heartbeatInterval() time.Duration {
	if s.HeartbeatInterval > 0 {
		return s.HeartbeatInterval
	}
	return DefaultHeartbeatInterval
}

// HeartbeatStaleAfter is the lease lifetime without a heartbeat refresh.
func (s *Store) HeartbeatStaleAfter() time.Duration {
	return heartbeatStaleFactor * s.heartbeatInterval()
}

// slotDir names a slot by class, a hash of the scope and its index: the hash
// keeps slashes and length out of the directory name, and the scope itself is
// recorded inside the lease for status. Index 0 keeps the original name, so a
// state root written before slots became configurable reads unchanged in both
// directions; a higher index appends its number.
func (s *Store) slotDir(scope, class string, index int) string {
	sum := sha256.Sum256([]byte(scope))
	name := fmt.Sprintf("%s-%s", class, hex.EncodeToString(sum[:])[:16])
	if index > 0 {
		name = fmt.Sprintf("%s-%d", name, index)
	}
	return filepath.Join(s.Root, "slots", name)
}

// HistoryPath is the cross-invocation pressure history.
func (s *Store) HistoryPath() string {
	return filepath.Join(s.Root, "history.json")
}

// Slot is a held slot. The zero value is invalid; Acquire returns it.
type Slot struct {
	store *Store
	dir   string
	lease Lease
}

// Lease returns a copy of the lease this slot holds.
func (sl *Slot) Lease() Lease { return sl.lease }

// Acquire takes the (scope, class) slot. Taking an index is a single atomic
// mkdir — there is deliberately no multi-lock ordering because there is only
// ever one lock per admission, and the capacity count that picks the index
// never needs one (see AcquireWithOptions). On success it returns a Slot. When
// the slot is held it returns the holder's lease (for owner+reason visibility)
// and a nil Slot. A stale slot is reclaimed only when its heartbeat deadline
// has passed AND the owning process incarnation is verifiably gone; an
// unverifiable identity (ps failure) is busy, mirroring the watchdog rule
// that an unverifiable identity answers on the safe side.
//
// The reclaim itself is serialized by a per-slot reclaim lock: without it
// two waiters could both judge the same lease stale, and the second one's
// RemoveAll would delete the first one's freshly written lease, admitting
// two holders. The lock is a leaf in the lock order — it is only ever taken
// by a waiter that holds NO slot — so no deadlock is possible by
// construction.
func (s *Store) Acquire(ctx context.Context, scope, class string, lease Lease) (*Slot, *Lease, error) {
	return s.AcquireWithOptions(ctx, scope, class, lease, AcquireOptions{})
}

// AcquireOptions is the concurrency one caller's resolved policy allows for a
// scope. A zero value is the policy default: one lease, no overflow.
type AcquireOptions struct {
	// Capacity is the number of concurrent leases this scope allows. It is
	// counted against the live leases across every index, so a lease a bypass
	// left past capacity, or one stranded there by a lowered capacity or an
	// expired relax override, still occupies the scope.
	Capacity int
	// Overflow keeps trying past Capacity, which is what the slot bypass does:
	// the job still takes a lease, in the first free overflow slot, so it stays
	// visible in `dot admit status` and its process tree stays covered for
	// every other caller's scan. It is never run without a lease.
	Overflow bool
}

// slotAcquireAttempts bounds the count-take-recount loop: each retry follows a
// lost index race, so a run of them means other jobs are acquiring this scope
// faster than we can.
const slotAcquireAttempts = 8

// AcquireWithOptions takes a slot for (scope, class) within the caller's
// capacity, choosing the lowest free index and reclaiming a stale lease when it
// lands on one. Capacity bounds the live leases of the whole scope, not the
// indexes a single caller walks: a bypass can leave a lease past capacity, and
// a lowered capacity or an expired relax override can strand one at a higher
// index, so counting only the first `capacity` indexes would admit a second job
// at capacity 1. It reports the lowest live holder when the scope is full, so
// the defer reason names who holds it.
//
// The count and the take are not one atomic step and do not need to be: every
// caller picks the lowest free index, so two callers racing for it collide on
// that one directory (one loses the mkdir) instead of taking two different
// indexes past the limit. The loser recounts and decides again.
func (s *Store) AcquireWithOptions(ctx context.Context, scope, class string, lease Lease, opts AcquireOptions) (*Slot, *Lease, error) {
	if err := os.MkdirAll(filepath.Join(s.Root, "slots"), 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating slots dir: %w", err)
	}
	capacity := opts.Capacity
	if capacity < 1 {
		capacity = 1
	}
	if capacity > MaxSlotsPerScope {
		capacity = MaxSlotsPerScope
	}
	// reach is the highest index this caller may take: capacity itself, plus the
	// overflow allowance when it bypasses.
	reach := capacity
	if opts.Overflow {
		reach += MaxOverflowSlots
	}
	var firstHolder *Lease
	for attempt := 0; attempt < slotAcquireAttempts; attempt++ {
		live, err := s.liveSlots(ctx, scope, class)
		if err != nil {
			return nil, nil, err
		}
		if firstHolder == nil {
			// The holder the defer reason names is the lowest live one, even when
			// it sits past this caller's reach: that is the job holding the scope.
			for index := 0; index < MaxSlotsPerScope+MaxOverflowSlots; index++ {
				if holder := live[index]; holder != nil {
					firstHolder = holder
					break
				}
			}
		}
		if len(live) >= reach {
			return nil, firstHolder, nil
		}
		// A free index below reach exists. Start the search at capacity when the
		// capacity is already spoken for: only a bypassing caller gets here then
		// (a plain caller's reach is the capacity, so it returned above), and the
		// overflow notice, the JSON "ignored" set and the resourceguard wrapper all
		// key on the index, so a bypass that quietly took a normal index would
		// report nothing. By pigeonhole a plain caller with fewer live leases than
		// its capacity always has a free index below it, so this offset only ever
		// moves a bypass into the overflow range.
		index := 0
		if len(live) >= capacity {
			index = capacity
		}
		for ; index < reach; index++ {
			if live[index] == nil {
				break
			}
		}
		if index >= reach {
			// Every index this caller may take is live: the overflow allowance is
			// spent even though the scope holds fewer leases than reach.
			return nil, firstHolder, nil
		}
		slot, holder, err := s.acquireIndex(ctx, scope, class, lease, index)
		if err != nil {
			return nil, nil, err
		}
		if slot != nil {
			return slot, nil, nil
		}
		if holder != nil && firstHolder == nil {
			firstHolder = holder
		}
	}
	if firstHolder != nil {
		return nil, firstHolder, nil
	}
	return nil, nil, fmt.Errorf("a %s slot for scope %s changed hands while acquiring; retry", class, scope)
}

// liveSlots reports the live lease at each index of (scope, class). A stale or
// corrupt-aged lease is not live: its index reads free, and acquireIndex
// reclaims it under the slot's reclaim lock when a caller takes it. A lease
// file that is unreadable in a directory younger than the stale bound counts as
// held, because a holder may be between its mkdir and its first lease write.
func (s *Store) liveSlots(ctx context.Context, scope, class string) (map[int]*Lease, error) {
	live := make(map[int]*Lease, MaxSlotsPerScope+MaxOverflowSlots)
	for index := 0; index < MaxSlotsPerScope+MaxOverflowSlots; index++ {
		dir := s.slotDir(scope, class, index)
		if _, err := os.Stat(dir); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("reading slot %s: %w", dir, err)
		}
		holder, stale, err := s.slotHolder(ctx, dir)
		if err != nil {
			return nil, err
		}
		if stale {
			continue
		}
		if holder == nil {
			holder = &Lease{Scope: scope, Class: class}
		}
		holder.Index = index // a lease file omits index 0
		live[index] = holder
	}
	return live, nil
}

// acquireIndex takes the slot at one index, or reports its holder.
func (s *Store) acquireIndex(ctx context.Context, scope, class string, lease Lease, index int) (*Slot, *Lease, error) {
	dir := s.slotDir(scope, class, index)
	lease.Index = index
	for attempt := 0; attempt < 3; attempt++ {
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			now := s.now()
			lease.Scope = scope
			lease.Class = class
			lease.AcquiredAt = now
			lease.HeartbeatAt = now
			lease.Deadline = now.Add(s.HeartbeatStaleAfter())
			if err := writeLease(dir, lease); err != nil {
				_ = os.RemoveAll(dir)
				return nil, nil, err
			}
			return &Slot{store: s, dir: dir, lease: lease}, nil, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, nil, fmt.Errorf("acquiring slot %s: %w", dir, err)
		}
		holder, stale, herr := s.slotHolder(ctx, dir)
		if herr != nil {
			return nil, nil, herr
		}
		if !stale {
			return nil, holder, nil
		}
		slot, busy, rerr := s.reclaimAndAcquire(ctx, dir, holder, lease, scope, class)
		if rerr != nil {
			return nil, nil, rerr
		}
		if slot != nil {
			return slot, nil, nil
		}
		if busy {
			// Lost the reclaim race, or the slot stopped being stale under
			// the lock: report the current holder.
			if current, rerr := readLease(dir); rerr == nil {
				return nil, &current, nil
			}
			return nil, holder, nil
		}
	}
	return nil, nil, fmt.Errorf("slot %s changed hands while acquiring; retry", dir)
}

// reclaimStaleAfter bounds a reclaim lock left behind by a reclaimer that
// died mid-reclaim. A reclaim runs in well under a second, so a minute-old
// lock cannot belong to a live one.
const reclaimStaleAfter = time.Minute

// reclaimAndAcquire performs the guarded stale reclaim end to end: take the
// slot's reclaim lock, re-verify staleness under it, remove the stale slot,
// and acquire it for ourselves with a fresh lease — all before releasing the
// lock, so a second reclaimer can never interleave its RemoveAll with the
// winner's fresh lease. busy=true means the slot is (or became) someone
// else's; the caller reports the holder.
func (s *Store) reclaimAndAcquire(ctx context.Context, dir string, judged *Lease, lease Lease, scope, class string) (*Slot, bool, error) {
	lockPath := dir + ".reclaim"
	locked := false
	for attempt := 0; attempt < 2; attempt++ {
		err := os.Mkdir(lockPath, 0o755)
		if err == nil {
			locked = true
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, false, fmt.Errorf("taking reclaim lock %s: %w", lockPath, err)
		}
		info, statErr := os.Lstat(lockPath)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				continue
			}
			return nil, false, statErr
		}
		if s.now().Sub(info.ModTime()) <= reclaimStaleAfter {
			return nil, true, nil // another reclaimer is working; the slot is busy
		}
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, false, fmt.Errorf("reclaiming stale reclaim lock %s: %w", lockPath, err)
		}
	}
	if !locked {
		return nil, true, nil
	}
	release := func() { _ = os.Remove(lockPath) }
	stillStale, err := s.reverifyStale(ctx, dir, judged)
	if err != nil || !stillStale {
		release()
		return nil, true, err
	}
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		release()
		return nil, false, fmt.Errorf("reclaiming stale slot %s: %w", dir, err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		release()
		if errors.Is(err, fs.ErrExist) {
			return nil, true, nil // released naturally between RemoveAll and mkdir
		}
		return nil, false, fmt.Errorf("re-acquiring reclaimed slot %s: %w", dir, err)
	}
	now := s.now()
	lease.Scope = scope
	lease.Class = class
	lease.AcquiredAt = now
	lease.HeartbeatAt = now
	lease.Deadline = now.Add(s.HeartbeatStaleAfter())
	if err := writeLease(dir, lease); err != nil {
		release()
		_ = os.RemoveAll(dir)
		return nil, false, err
	}
	release()
	return &Slot{store: s, dir: dir, lease: lease}, false, nil
}

// reverifyStale re-checks, under the reclaim lock, that the slot is still
// stale in exactly the way the caller judged: same lease identity, deadline
// still expired, owner incarnation still verifiably gone. Any change — a
// fresh heartbeat, a reclaimed-and-re-acquired slot, a live owner — aborts
// the reclaim. A nil judged means the caller's judgment was "corrupt lease,
// aged directory"; that judgment stands only while the lease is still
// unreadable and the directory still past the stale bound.
func (s *Store) reverifyStale(ctx context.Context, dir string, judged *Lease) (bool, error) {
	current, err := readLease(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil // someone else's reclaim beat us; their slot now
		}
		if judged != nil {
			return false, fmt.Errorf("re-reading lease under reclaim lock: %w", err)
		}
		info, serr := os.Stat(dir)
		if serr != nil {
			if errors.Is(serr, fs.ErrNotExist) {
				return false, nil
			}
			return false, serr
		}
		return s.now().Sub(info.ModTime()) > s.HeartbeatStaleAfter(), nil
	}
	if judged == nil {
		return false, nil // a corrupt lease became readable: changed hands
	}
	if current.PID != judged.PID || !current.AcquiredAt.Equal(judged.AcquiredAt) ||
		!current.HeartbeatAt.Equal(judged.HeartbeatAt) {
		return false, nil // changed underneath us; no longer our judgment to act on
	}
	if s.now().Before(current.Deadline) {
		return false, nil // heartbeat refreshed since we judged
	}
	match, merr := watchdog.ProcessStartMatches(ctx, s.Runner, current.PID, current.PIDStart)
	if merr != nil || match {
		return false, nil // unverifiable or alive: never reclaim on doubt
	}
	return true, nil
}

// slotHolder reads the existing lease and reports whether it is stale enough
// to reclaim. A fresh heartbeat is never reclaimed regardless of what ps
// says about the pid — the owner might be between heartbeats, and the
// deadline is the only clock that can prove abandonment.
func (s *Store) slotHolder(ctx context.Context, dir string) (holder *Lease, stale bool, err error) {
	lease, lerr := readLease(dir)
	if lerr != nil {
		// A corrupt lease cannot prove ownership; it is reclaimable only
		// after the slot directory itself has gone quiet past the stale
		// bound.
		info, serr := os.Stat(dir)
		if serr != nil {
			if errors.Is(serr, fs.ErrNotExist) {
				return nil, true, nil
			}
			return nil, false, serr
		}
		if s.now().Sub(info.ModTime()) > s.HeartbeatStaleAfter() {
			return nil, true, nil
		}
		return nil, false, nil
	}
	if s.now().Before(lease.Deadline) {
		return &lease, false, nil
	}
	match, merr := watchdog.ProcessStartMatches(ctx, s.Runner, lease.PID, lease.PIDStart)
	if merr != nil {
		// Identity unverifiable: stay busy rather than reclaiming from under
		// a live owner whose ps probe failed.
		return &lease, false, nil
	}
	if match {
		// Owner alive but heartbeat stalled; freeing it would admit a second
		// heavy job next to a live one.
		return &lease, false, nil
	}
	return &lease, true, nil
}

// Heartbeat refreshes the lease deadline. If the slot changed hands (a stale
// reclaim plus re-acquire by someone else), Heartbeat refuses rather than
// overwriting the new owner's record, and the caller should stop heartbeating.
func (sl *Slot) Heartbeat() error {
	current, err := readLease(sl.dir)
	if err != nil {
		return fmt.Errorf("heartbeat lost slot %s: %w", sl.dir, err)
	}
	if !sameLease(current, sl.lease) {
		return fmt.Errorf("heartbeat lost slot %s: slot changed hands", sl.dir)
	}
	sl.lease.HeartbeatAt = sl.store.now()
	sl.lease.Deadline = sl.lease.HeartbeatAt.Add(sl.store.HeartbeatStaleAfter())
	return writeLease(sl.dir, sl.lease)
}

// UpdateIdentity rewires the lease to a different process incarnation —
// concretely, the child workload the wrapper just started. Without this the
// lease would track the wrapper: a SIGKILLed wrapper would let the lease
// expire and be reclaimed while the orphaned child keeps running in its own
// process group, admitting a second heavy job next to it. The sameLease
// guard still applies: the rewrite is refused once the slot changed hands.
func (sl *Slot) UpdateIdentity(pid int, pidStart string) error {
	current, err := readLease(sl.dir)
	if err != nil {
		return fmt.Errorf("identity update lost slot %s: %w", sl.dir, err)
	}
	if !sameLease(current, sl.lease) {
		return fmt.Errorf("identity update lost slot %s: slot changed hands", sl.dir)
	}
	sl.lease.PID = pid
	sl.lease.PIDStart = pidStart
	sl.lease.PGID = pid // the child leads its own process group (Setpgid)
	sl.lease.HeartbeatAt = sl.store.now()
	sl.lease.Deadline = sl.lease.HeartbeatAt.Add(sl.store.HeartbeatStaleAfter())
	return writeLease(sl.dir, sl.lease)
}

// Release removes the slot directory, but only while the lease inside is
// still ours: a release that lands after a reclaim plus re-acquire must not
// delete the new owner's slot.
func (sl *Slot) Release() error {
	current, err := readLease(sl.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil && !sameLease(current, sl.lease) {
		return nil
	}
	if err := os.RemoveAll(sl.dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("releasing slot %s: %w", sl.dir, err)
	}
	return nil
}

func sameLease(a, b Lease) bool {
	return a.PID == b.PID && a.AcquiredAt.Equal(b.AcquiredAt)
}

func readLease(dir string) (Lease, error) {
	data, err := os.ReadFile(filepath.Join(dir, "lease.json"))
	if err != nil {
		return Lease{}, err
	}
	var lease Lease
	if err := json.Unmarshal(data, &lease); err != nil {
		return Lease{}, fmt.Errorf("parsing lease in %s: %w", dir, err)
	}
	return lease, nil
}

// writeLease persists the lease atomically (temp + rename), so a concurrent
// reader only ever sees the old or the new record, never a torn one.
func writeLease(dir string, lease Lease) error {
	data, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".lease-*")
	if err != nil {
		return fmt.Errorf("creating lease temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, "lease.json"))
}

// LoadHistory reads the pressure history. A missing file is an empty
// history, not an error: the first gate evaluation has no past.
func LoadHistory(path string) (History, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return History{}, nil
	}
	if err != nil {
		return History{}, fmt.Errorf("reading history: %w", err)
	}
	var h History
	if err := json.Unmarshal(data, &h); err != nil {
		return History{}, fmt.Errorf("parsing history %s: %w", path, err)
	}
	return h, nil
}

// SaveHistory writes the history atomically.
func SaveHistory(path string, h History) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating history dir: %w", err)
	}
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// GateBusyReason marks history-lock contention between concurrent gate
// evaluations: a transient busy state the caller may retry, not host
// pressure. A busy decision carries no defer episode.
const GateBusyReason = "another admission gate evaluation is in progress"

// IsGateBusy reports whether the decision is gate-lock contention rather
// than a pressure defer.
func IsGateBusy(d Decision) bool {
	return !d.Admit && len(d.Reasons) == 1 && d.Reasons[0] == GateBusyReason
}

// Gate runs one pressure-gate evaluation: snapshot, evaluate against the
// persisted history, persist the updated history. The load-evaluate-save
// cycle holds a short mkdir lock so concurrent invocations cannot interleave
// and silently drop one run's sustain/recovery streak. When another
// evaluation holds the lock, Gate defers conservatively rather than reading
// a half-updated history. The evaluation clock comes from the monitor when
// it carries one, matching the fixture seams.
func (s *Store) Gate(ctx context.Context, m *Monitor, th Thresholds) (Decision, error) {
	return s.GateWithPolicy(ctx, m, GatePolicy{Thresholds: th})
}

// GateResolved runs the standard policy with a resolved policy's thresholds and
// bypasses. The evaluation still samples and records history when pressure is
// bypassed.
func (s *Store) GateResolved(ctx context.Context, m *Monitor, policy Policy) (Decision, error) {
	return s.GateWithPolicy(ctx, m, GatePolicy{
		Thresholds:   policy.Thresholds,
		Ignore:       policy.Ignore,
		IgnoreSource: policy.IgnoreSource,
	})
}

// PrebuiltGate builds the verified-prebuilt profile from a resolved policy: its
// own thresholds, its own history file and its own safety window, so a change
// to one profile never leaks into the other.
func PrebuiltGate(policy Policy) GatePolicy {
	window := policy.Prebuilt.SafetyWindow
	return GatePolicy{
		HistoryFile: PrebuiltHistoryFile,
		Thresholds:  policy.Prebuilt.Thresholds,
		Evaluate: func(snap PressureSnapshot, th Thresholds, hist History, now time.Time) Decision {
			return EvaluatePrebuiltPressureWithWindow(snap, th, hist, now, window)
		},
	}
}

// GatePolicy selects one gate profile's history, thresholds and evaluator for a
// narrowly-scoped admission policy. HistoryFile must be a basename under
// Store.Root; the default policy uses history.json. A bypassed pressure rule
// admits past a defer while still sampling and recording history; IgnoreSource
// labels the bypass in the note and in the JSON record.
type GatePolicy struct {
	HistoryFile  string
	Thresholds   Thresholds
	Snapshot     func(context.Context) PressureSnapshot
	Evaluate     func(PressureSnapshot, Thresholds, History, time.Time) Decision
	Ignore       IgnoreSet
	IgnoreSource string
	// ValidUntil is the expiry of the override the limits above came from, and
	// zero when no override is in play. A probe can outlive it, so a sample
	// judged after it is discarded instead of persisted: see GateWithPolicy.
	ValidUntil time.Time
}

// GateWithPolicy runs one locked pressure evaluation and persists its result.
// Policies share the root lock and slot state while selecting a separate
// history file when needed.
func (s *Store) GateWithPolicy(ctx context.Context, m *Monitor, policy GatePolicy) (Decision, error) {
	historyName := policy.HistoryFile
	if historyName == "" {
		historyName = "history.json"
	}
	if filepath.Base(historyName) != historyName || historyName == "." || historyName == ".." || strings.ContainsAny(historyName, `/\\`) {
		return Decision{}, fmt.Errorf("invalid history file name %q", policy.HistoryFile)
	}
	historyPath := filepath.Join(s.Root, historyName)
	release, touch, busy, err := acquireHistoryLock(s.Root)
	if err != nil {
		return Decision{}, fmt.Errorf("locking history: %w", err)
	}
	if busy {
		return Decision{
			Admit:      false,
			Reasons:    []string{GateBusyReason},
			RetryAfter: 10 * time.Second,
		}, nil
	}
	defer release()
	hist, err := LoadHistory(historyPath)
	if err != nil {
		return Decision{}, err
	}
	snapshot := policy.Snapshot
	if snapshot == nil {
		snapshot = func(ctx context.Context) PressureSnapshot { return m.SnapshotPressure(ctx) }
	}
	snap := snapshot(ctx)
	// Take the evaluation timestamp after all probes have completed.
	now := s.now()
	if m.Now != nil {
		now = m.Now()
	}
	// The limits were resolved before the probe, and a Darwin pass can spend most
	// of a minute in it. Judging this sample against an override that expired in
	// the meantime would persist a verdict under limits the controller no longer
	// holds — and a closed episode cannot be reopened by the next iteration, so
	// the sample is dropped and the caller asked to resample: nothing is saved,
	// not even the sample time.
	if !policy.ValidUntil.IsZero() && now.After(policy.ValidUntil) {
		return Decision{
			Admit:        false,
			Reasons:      []string{"admission override expired during the probe; resample to use the current limits"},
			RetryAfter:   time.Second,
			Recovering:   true, // makes `dot admit` resample inside its --wait budget
			ProfileRetry: true, // and the verified prebuilt path inside its own budget
		}, nil
	}
	hist = PrepareHistorySample(hist, policy.Thresholds, snap, now)
	// The snapshot can run for most of a minute on a loaded host (probe
	// timeouts); refresh the lock so a concurrent invocation never reads
	// this live evaluation as abandoned.
	touch()
	evaluate := policy.Evaluate
	if evaluate == nil {
		evaluate = EvaluatePressure
	}
	d := evaluate(snap, policy.Thresholds, hist, now)
	// The snapshot is taken and d.Next persisted either way: a bypassed
	// pressure rule must leave the recovery streak exactly as continuous as a
	// real defer, or the gate would never recover after the bypass ends.
	if policy.Ignore.Pressure {
		d = ApplyIgnore(d, policy.Ignore, policy.IgnoreSource)
	}
	d.Next.LastSampleAt = now
	if err := SaveHistory(historyPath, d.Next); err != nil {
		return Decision{}, err
	}
	return d, nil
}

// historyLockStaleAfter bounds a history lock left behind by a gate
// evaluation that died mid-pass. A Darwin evaluation runs up to seven
// sequential probes with 10s timeouts plus the DiagnosticReports scan
// (~80s worst case), so the bound must clear that comfortably; Gate also
// touches the lock after the snapshot returns, so a long-but-live
// evaluation never looks abandoned.
const historyLockStaleAfter = 3 * time.Minute

// acquireHistoryLock serializes the load-evaluate-save cycle on
// history.json between concurrent `dot admit` invocations, mirroring the
// watchdog pass lock: a lock older than the bound belongs to a dead pass.
// The returned touch callback refreshes the lock mtime; long evaluations
// call it to prove they are alive.
func acquireHistoryLock(root string) (release func(), touch func(), busy bool, err error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, nil, false, err
	}
	lockPath := filepath.Join(root, "history.lock")
	for attempt := 0; attempt < 2; attempt++ {
		err := os.Mkdir(lockPath, 0o755)
		if err == nil {
			return func() { _ = os.Remove(lockPath) },
				func() { now := time.Now(); _ = os.Chtimes(lockPath, now, now) },
				false, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, nil, false, err
		}
		info, statErr := os.Lstat(lockPath)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				continue
			}
			return nil, nil, false, statErr
		}
		if time.Since(info.ModTime()) <= historyLockStaleAfter {
			return nil, nil, true, nil
		}
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, false, err
		}
	}
	return nil, nil, true, nil
}

// ClaimNotify atomically claims the right to send the alert for one defer
// episode: the mark file's NAME carries the episode, and creation is
// O_CREATE|O_EXCL, so two concurrent defer handlers for the same episode
// cannot both win — exactly one notification per defer episode per scope.
// The returned path lets the caller release a claim whose notification
// failed (remove it, and the next defer retries). A new episode has a new
// name, so it claims independently; older marks for the same scope are
// removed best-effort once the new claim lands.
func (s *Store) ClaimNotify(scope, class string, episode time.Time) (claimed bool, path string, err error) {
	if episode.IsZero() {
		episode = s.now()
	}
	dir := filepath.Join(s.Root, "notify")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, "", err
	}
	sum := sha256.Sum256([]byte(scope))
	prefix := fmt.Sprintf("%s-%s-", class, hex.EncodeToString(sum[:])[:16])
	path = filepath.Join(dir, fmt.Sprintf("%s%d.json", prefix, episode.Unix()))
	data, err := json.Marshal(map[string]time.Time{"episode": episode})
	if err != nil {
		return false, "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, path, nil
	}
	if err != nil {
		return false, "", err
	}
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return false, "", werr
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return false, "", err
	}
	// Best-effort sweep of older episode marks for this scope+class; the
	// current claim's file must survive.
	if entries, rerr := os.ReadDir(dir); rerr == nil {
		for _, e := range entries {
			if e.Name() != filepath.Base(path) && strings.HasPrefix(e.Name(), prefix) {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return true, path, nil
}

// ListLeases returns the lease of every slot currently on disk. Bounded by
// the slot count (one per repo plus maintenance); unreadable slots are
// skipped rather than failing the whole status view.
func (s *Store) ListLeases() ([]Lease, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "slots"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing slots: %w", err)
	}
	var out []Lease
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		lease, err := readLease(filepath.Join(s.Root, "slots", e.Name()))
		if err != nil {
			continue
		}
		out = append(out, lease)
	}
	return out, nil
}

// NestedScope reports whether env (the value of DOT_ADMISSION_SCOPE) marks
// this process as the child of an admitted job for scope and class.
func NestedScope(env, scope, class string) bool {
	if env == "" {
		return false
	}
	var mark struct {
		Scope string `json:"scope"`
		Class string `json:"class"`
	}
	if json.Unmarshal([]byte(env), &mark) != nil {
		return false
	}
	return mark.Scope == scope && mark.Class == class
}

// NestedEnvValue renders the DOT_ADMISSION_SCOPE value a parent exports to
// its child.
func NestedEnvValue(scope, class string) string {
	data, _ := json.Marshal(map[string]string{"scope": scope, "class": class})
	return string(data)
}
