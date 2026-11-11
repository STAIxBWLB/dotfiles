package admission

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// RelaxOverride bounds. `dot admit relax` defaults to an hour and refuses
// anything longer than a day, because a temporary bypass that outlives the
// incident it was set for is how an admission limit quietly stops mattering.
const (
	RelaxDefaultFor = time.Hour
	RelaxMaxFor     = 24 * time.Hour
	RelaxFileName   = "override.json"
)

// RelaxOverride is the time-bounded override `dot admit relax` writes: which
// rules to step past, and/or policy keys to layer over config until ExpiresAt.
// Set holds the keys in their written form so the file stays readable and the
// loader re-validates it through the same key table as the config.
type RelaxOverride struct {
	SetAt     time.Time         `json:"set_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	Ignore    []string          `json:"ignore,omitempty"`
	Set       map[string]string `json:"set,omitempty"`
}

// Expired reports whether the override no longer applies at now.
func (r RelaxOverride) Expired(now time.Time) bool { return !now.Before(r.ExpiresAt) }

// RelaxSnapshot is the identity of the relax override one resolved policy came
// from: its expiry together with every key it set, or the absence of one.
// Identity rather than the expiry alone, because `dot admit relax` can rewrite
// the override mid-run: same end, different limits.
type RelaxSnapshot struct {
	present   bool
	expiresAt time.Time
	ignore    []string
	set       map[string]string
}

func snapshotOf(r *RelaxOverride) RelaxSnapshot {
	if r == nil {
		return RelaxSnapshot{}
	}
	return RelaxSnapshot{
		present:   true,
		expiresAt: r.ExpiresAt,
		ignore:    append([]string(nil), r.Ignore...),
		set:       maps.Clone(r.Set),
	}
}

// describe renders the snapshot for a defer reason.
func (s RelaxSnapshot) describe() string {
	if !s.present {
		return "no override"
	}
	return "relax until " + s.expiresAt.Format(time.RFC3339)
}

func (s RelaxSnapshot) expired(now time.Time) bool {
	return s.present && !now.Before(s.expiresAt)
}

func (s RelaxSnapshot) same(other RelaxSnapshot) bool {
	if s.present != other.present {
		return false
	}
	if !s.present {
		return true
	}
	if !s.expiresAt.Equal(other.expiresAt) || len(s.ignore) != len(other.ignore) || !maps.Equal(s.set, other.set) {
		return false
	}
	// Rule order is how the person wrote it, not which rules: the same set in
	// another order is the same override.
	a := slices.Clone(s.ignore)
	b := slices.Clone(other.ignore)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// RelaxWatch records the relax overrides one admission acted on, so its commit
// point can require each one is still in force. Without it a run judges each
// step against the limits it read before that step, and an override that ends or
// is rewritten between a step and the launch still decides the run.
type RelaxWatch struct {
	seen []RelaxSnapshot
}

// Note records the override the policy behind one step came from. A policy with
// no override records nothing: only a layer that can expire needs rechecking,
// and a config or one-off bypass is not one.
func (w *RelaxWatch) Note(p Policy) {
	snap := snapshotOf(p.Relax)
	if !snap.present {
		return
	}
	for _, seen := range w.seen {
		if seen.same(snap) {
			return
		}
	}
	w.seen = append(w.seen, snap)
}

// Verify requires every recorded override to be unexpired at now and still the
// active one in fresh. The clock is the commit point's own, not the resolver's:
// the resolver prunes on its own clock and can still report an ended override as
// live. An error says which override ended or changed and that a rerun is what
// applies the limits now in force.
func (w *RelaxWatch) Verify(fresh Policy, now time.Time) error {
	active := snapshotOf(fresh.Relax)
	for _, seen := range w.seen {
		if seen.expired(now) {
			return fmt.Errorf("the admission override that set limits for this run ended during admission (%s); rerun to use the limits now in force", seen.describe())
		}
		if !seen.same(active) {
			return fmt.Errorf("the admission override that set limits for this run changed during admission (%s, now %s); rerun to use the limits now in force", seen.describe(), active.describe())
		}
	}
	return nil
}

// IgnoreSet parses the stored rule names.
func (r RelaxOverride) IgnoreSet() (IgnoreSet, error) {
	set, err := ParseIgnoreSet(joinRules(r.Ignore))
	if err != nil {
		return IgnoreSet{}, fmt.Errorf("admission relax override: %w (clear it with `dot admit relax --clear`)", err)
	}
	return set, nil
}

// Overrides turns the stored keys into the layer this override applies.
func (r RelaxOverride) Overrides() (PolicyOverrides, error) {
	var overrides PolicyOverrides
	// Sorted, so a file with two bad keys always reports the same one.
	keys := make([]string, 0, len(r.Set))
	for key := range r.Set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := overrides.SetKey(key, r.Set[key]); err != nil {
			return PolicyOverrides{}, fmt.Errorf("admission relax override: %w (clear it with `dot admit relax --clear`)", err)
		}
	}
	return overrides, nil
}

func joinRules(rules []string) string {
	out := ""
	for i, rule := range rules {
		if i > 0 {
			out += ","
		}
		out += rule
	}
	return out
}

// OverridePath is the temporary override file under the state root.
func (s *Store) OverridePath() string {
	return filepath.Join(s.Root, RelaxFileName)
}

// LoadRelaxOverride reads the temporary override. A missing file is no
// override. An expired override is never returned; it is removed when prune is
// set (the gate paths prune, status is read-only and only displays). A corrupt
// file is an error rather than a silent fallback: a broken override that reads
// as "no override" is exactly the silent behavior change the config validation
// exists to prevent.
func (s *Store) LoadRelaxOverride(prune bool) (*RelaxOverride, error) {
	path := s.OverridePath()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the admission relax override: %w", err)
	}
	var override RelaxOverride
	if err := json.Unmarshal(data, &override); err != nil {
		return nil, fmt.Errorf("parsing the admission relax override %s: %w (clear it with `dot admit relax --clear`)", path, err)
	}
	if override.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("admission relax override %s has no expires_at (clear it with `dot admit relax --clear`)", path)
	}
	if !override.Expired(s.now()) {
		return &override, nil
	}
	if prune {
		// Best-effort: contention with a live gate only leaves an expired
		// file on disk for the next gate, and it is already ignored.
		s.pruneExpiredOverride()
	}
	return nil, nil
}

// pruneExpiredOverride removes the override only if it is still expired once
// the root lock is held. Re-reading under the lock matters: a `dot admit relax`
// that landed between the caller's read and this removal would otherwise be
// deleted moments after it was set, silently returning the host to defaults.
func (s *Store) pruneExpiredOverride() {
	_ = s.withRootLock("pruning the expired admission relax override", func() error {
		data, err := os.ReadFile(s.OverridePath())
		if err != nil {
			return nil // already gone
		}
		var current RelaxOverride
		if err := json.Unmarshal(data, &current); err != nil {
			return nil // unreadable: the loud parse error path owns this file
		}
		if current.ExpiresAt.IsZero() || !current.Expired(s.now()) {
			return nil
		}
		if err := os.Remove(s.OverridePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
}

// SaveRelaxOverride writes the override atomically under the admission root
// lock, the same lock a gate evaluation holds, so a relax write cannot
// interleave with a gate reading the previous one.
func (s *Store) SaveRelaxOverride(override RelaxOverride) error {
	return s.withRootLock("writing the admission relax override", func() error {
		data, err := json.MarshalIndent(override, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(s.Root, 0o755); err != nil {
			return fmt.Errorf("creating the admission state root: %w", err)
		}
		tmp, err := os.CreateTemp(s.Root, ".override-*")
		if err != nil {
			return fmt.Errorf("creating the relax override temp file: %w", err)
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), s.OverridePath()); err != nil {
			return fmt.Errorf("writing the relax override: %w", err)
		}
		return nil
	})
}

// ClearRelaxOverride removes the temporary override. A missing file is not an
// error, so `--clear` is idempotent.
func (s *Store) ClearRelaxOverride() error {
	return s.withRootLock("clearing the admission relax override", func() error {
		if err := os.Remove(s.OverridePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing the relax override: %w", err)
		}
		return nil
	})
}

// withRootLock runs one root-state mutation under the admission root lock. A
// live gate holding the lock is reported as actionable contention, not as a
// partial write: nothing has changed yet when the lock cannot be taken.
func (s *Store) withRootLock(action string, fn func() error) error {
	release, _, busy, err := acquireHistoryLock(s.Root)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if busy {
		return fmt.Errorf("%s: %s; retry", action, GateBusyReason)
	}
	defer release()
	return fn()
}
