package remote

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
)

// The remote class holds two leases per job, and both come from the admission
// controller: one per mirror path, so a repository and each of its worktrees
// run at most one job at a time; one per host, so this machine adds at most
// max_runs of them. Neither is the local heavy slot: the work is not on this
// machine, so the local pressure gate and the per-repo heavy slot do not apply.

// PathScope is the admission scope of one mirror path. Worktrees of one
// repository are separate paths and run side by side.
func PathScope(absolute string) string { return filepath.Clean(absolute) }

// HostScope is the admission scope holding one host's runs from this machine.
//
// The scope lives in this machine's state root, so max_runs is a per-machine
// count: two machines pointing at one host can each start max_runs jobs. What
// the host enforces by itself is one run per mirror, through the container name
// MirrorKey derives from the mirror's path.
// ponytail: known ceiling. See docs/CEILINGS.md (cross-machine remote runs per host).
func HostScope(host string) string { return "remote-host:" + host }

// Deferred reports why a remote job did not start, in the record shape
// `dot admit` uses (exit 75, EX_TEMPFAIL). The caller prints it and exits.
type Deferred struct {
	Outcome admission.DeferOutcome
}

func (d *Deferred) Error() string { return d.Outcome.Reason }

// Leases are the leases one remote job holds.
type Leases struct {
	// Path is the one-run-per-mirror-path lease.
	Path *admission.Slot
	// Host is the one-of-max_runs-per-host lease.
	Host *admission.Slot
	stop func()
}

// Release gives both leases back and stops the heartbeat.
func (l *Leases) Release() {
	if l == nil {
		return
	}
	if l.stop != nil {
		l.stop()
		l.stop = nil
	}
	if l.Host != nil {
		_ = l.Host.Release()
		l.Host = nil
	}
	if l.Path != nil {
		_ = l.Path.Release()
		l.Path = nil
	}
}

// AcquirePath takes the one-run-per-mirror-path lease. The whole sync and run
// hold it, so a second job for the same path defers instead of sharing a
// mirror with the first one. There is no bypass: `--ignore slot` relaxes the
// host's max_runs, and a second run would delete files inside a mirror that is
// in use.
func AcquirePath(ctx context.Context, store *admission.Store, self admission.Lease, host Host, path string, wait time.Duration) (*admission.Slot, error) {
	self.Host = host.Name
	if self.Session == "" {
		self.Session = "dot remote"
	}
	return acquire(ctx, store, PathScope(path), self, 1, wait, false, func(holder *admission.Lease) string {
		return pathBusyReason(path, holder)
	})
}

// AcquireHost takes one of the host's max_runs leases.
func AcquireHost(ctx context.Context, store *admission.Store, self admission.Lease, host Host, wait time.Duration, bypass bool) (*admission.Slot, error) {
	self.Host = host.Name
	if self.Session == "" {
		self.Session = "dot remote"
	}
	return acquire(ctx, store, HostScope(host.Name), self, host.MaxRuns, wait, bypass, func(holder *admission.Lease) string {
		return hostBusyReason(host, holder)
	})
}

// acquire takes scope's lease within capacity, waiting inside wait. A wait of
// zero defers at once, which is the default: a remote run is not worth
// queueing for silently.
func acquire(ctx context.Context, store *admission.Store, scope string, self admission.Lease, capacity int, wait time.Duration, bypass bool, reason func(*admission.Lease) string) (*admission.Slot, error) {
	deadline := time.Now().Add(wait)
	backoff := 2 * time.Second
	for {
		slot, holder, err := store.AcquireWithOptions(ctx, scope, admission.ClassRemote, self, admission.AcquireOptions{
			Capacity: capacity,
			Overflow: bypass,
		})
		if err != nil {
			return nil, err
		}
		if slot != nil {
			return slot, nil
		}
		if backoff <= 0 || time.Now().Add(backoff).After(deadline) {
			return nil, deferFor(scope, reason(holder), backoff)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = minDuration(backoff*3/2, 15*time.Second)
	}
}

// deferFor renders the exit-75 record one scope's exhaustion produces.
func deferFor(scope, reason string, retryAfter time.Duration) error {
	second := int64(retryAfter / time.Second)
	if second < 1 {
		second = 1
	}
	return &Deferred{Outcome: admission.DeferOutcome{
		Outcome:           "deferred",
		Scope:             scope,
		Class:             admission.ClassRemote,
		Reason:            reason,
		RetryAfterSeconds: second,
	}}
}

func pathBusyReason(path string, holder *admission.Lease) string {
	reason := "another run of " + path + " is active"
	if holder != nil {
		reason = fmt.Sprintf("%s since %s (%s, pid %d)", reason, holder.AcquiredAt.Format(time.RFC3339), holder.Owner, holder.PID)
	}
	return reason
}

func hostBusyReason(host Host, holder *admission.Lease) string {
	reason := fmt.Sprintf("%d runs are already active on %s (remote.hosts.%s.max_runs)", host.MaxRuns, host.Name, host.Name)
	if holder != nil {
		reason = fmt.Sprintf("%s; the oldest started %s (%s, pid %d)", reason, holder.AcquiredAt.Format(time.RFC3339), holder.Owner, holder.PID)
	}
	return reason
}

// heartbeat refreshes both leases until the returned func is called. A lost
// lease (a stale reclaim that another run then took) only shows up in the next
// probe, exactly as `dot admit` treats it: the job keeps running.
func heartbeat(slots ...*admission.Slot) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(admission.DefaultHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				for _, slot := range slots {
					if slot != nil {
						_ = slot.Heartbeat()
					}
				}
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop) }
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
