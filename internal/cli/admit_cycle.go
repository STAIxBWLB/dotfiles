package cli

import (
	"context"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// The admission cycle: gate, slot, regate, scan, commit point. admit_cmd.go
// keeps the command surface and the per-run setup; this file keeps the sequence
// that turns a resolved policy into a held slot or a defer.

// admitCommitClock is the clock the commit point reads. Everything before it
// resolved the policy and acted on it; this is the read that decides whether
// those limits still stand, so a test sets it to put an override's expiry inside
// the run.
var admitCommitClock = time.Now

// resolveAdmissionPolicy layers the user's config and the active relax override
// over the policy defaults. The config path is the real user's state file:
// admission state is per real user, never a --home partition. keepExpired is set
// by read-only views (status) so they never delete state.
func resolveAdmissionPolicy(store *admission.Store, ignore admission.IgnoreSet, keepExpired bool) (admission.Policy, error) {
	return store.ResolvePolicy(admission.ResolveOptions{
		ConfigPath:  config.StatePath(),
		Ignore:      ignore,
		KeepExpired: keepExpired,
	})
}

// gateWithBusyRetry re-evaluates history-lock contention and the recovery
// countdown inside the wait budget. Neither is current host pressure, and the
// countdown only completes when samples arrive within the 45s gap, so a
// one-shot caller retrying minutes later could never close the episode.
// Pressure defers return immediately.
//
// The limits are resolved again before every sample: the countdown can last the
// whole --wait, and an override that expires or is cleared during it must not
// keep closing the episode on a relaxed recover_sustain. Each evaluation
// persists the episode state, so a stale layer would end it for good. Only the
// policy behind an admitting decision is recorded in watch: a defer that the
// caller resamples granted nothing.
func gateWithBusyRetry(ctx context.Context, store *admission.Store, monitor *admission.Monitor, resolveNow func() (admission.Policy, error), deadline time.Time, watch *admission.RelaxWatch) (admission.Decision, error) {
	for {
		policy, err := resolveNow()
		if err != nil {
			return admission.Decision{}, err
		}
		gate := admission.GatePolicy{
			Thresholds:   policy.Thresholds,
			Ignore:       policy.Ignore,
			IgnoreSource: policy.IgnoreSource,
		}
		if policy.Relax != nil {
			// The probe runs after this read, so an override that ends during it
			// must not judge the sample it produced.
			gate.ValidUntil = policy.Relax.ExpiresAt
		}
		d, err := admitGate(ctx, store, monitor, gate)
		if err != nil {
			return d, err
		}
		if d.Admit {
			watch.Note(policy)
			return d, nil
		}
		if !admission.IsGateBusy(d) && !d.Recovering {
			return d, nil
		}
		backoff := d.RetryAfter
		if backoff <= 0 {
			backoff = 2 * time.Second
		}
		if time.Now().Add(backoff).After(deadline) {
			return d, nil
		}
		select {
		case <-ctx.Done():
			return d, ctx.Err()
		case <-time.After(backoff):
		}
		// The timer can wake late; never admit past the wait budget.
		if time.Now().After(deadline) {
			return d, nil
		}
	}
}

// admitInput is everything one admission cycle needs: the resolved-policy
// reader, the scope and class it admits for, and the wait budget that decides
// whether an ended override restarts the cycle or ends it.
type admitInput struct {
	p          *Printer
	store      *admission.Store
	monitor    *admission.Monitor
	runner     *exec.Runner
	resolveNow func() (admission.Policy, error)
	scope      string
	class      string
	cwd        string
	wait       time.Duration
	deadline   time.Time
}

// admitOutcome is what one cycle reached: a held slot and the decision that
// admitted it, a defer to report, or a restart because the limits the cycle
// acted on no longer stand.
type admitOutcome struct {
	slot     *admission.Slot
	decision admission.Decision
	owner    string
	bypassed bypass
	restart  bool
}

// admitCycle runs one admission from the pressure gate to the commit point. It
// either holds the slot, or answers a decision the caller reports as a defer,
// or asks for a restart; in the last two cases it holds nothing.
func admitCycle(ctx context.Context, in admitInput) (admitOutcome, error) {
	var out admitOutcome
	var watch admission.RelaxWatch
	decision, err := gateWithBusyRetry(ctx, in.store, in.monitor, in.resolveNow, in.deadline, &watch)
	if err != nil {
		return out, err
	}
	out.decision = decision
	out.bypassed.pressure(in.p, decision)
	if !decision.Admit {
		return out, nil
	}
	lease, err := admission.SelfLease(ctx, in.runner, in.cwd)
	if err != nil {
		return out, err
	}
	capacity := 0
	var policy admission.Policy
	backoff := 2 * time.Second
	var slot *admission.Slot
	for {
		// Re-resolve on every attempt: an override that expired or was cleared
		// while we waited must change the capacity, the slot bypass and the gate,
		// not take effect only at the next invocation.
		if policy, err = in.resolveNow(); err != nil {
			return out, err
		}
		capacity = policy.Slots.For(in.class)
		got, holder, aerr := in.store.AcquireWithOptions(ctx, in.scope, in.class, lease, admission.AcquireOptions{
			Capacity: capacity,
			Overflow: policy.Ignore.Slot,
		})
		if aerr != nil {
			return out, aerr
		}
		if got != nil {
			slot = got
			watch.Note(policy)
			break
		}
		if in.wait == 0 || time.Now().Add(backoff).After(in.deadline) {
			if holder != nil {
				out.owner = holder.Owner
			}
			reason := admission.SlotBusyReason(in.class, holder, capacity, policy.Ignore.Slot)
			out.decision = admission.Decision{Admit: false, Reasons: []string{reason}, RetryAfter: backoff}
			return out, nil
		}
		time.Sleep(backoff)
		backoff = minDuration(backoff*3/2, 15*time.Second)
	}
	admission.NoteOverflowSlot(in.p.Err, slot.Lease(), capacity, policy.IgnoreSource)
	if slot.Lease().Index >= capacity {
		out.bypassed.rules = admissionIgnoreRules(out.bypassed.rules, admission.RuleSlot)
		out.bypassed.slot(admission.OverflowSlotReason(capacity, slot.Lease().Index, policy.IgnoreSource))
	}

	// The wait may have been long, so gate again before spending the slot on work
	// that pressure would now defer anyway; the gate re-resolves the limits itself.
	if in.wait > 0 {
		decision, err = gateWithBusyRetry(ctx, in.store, in.monitor, in.resolveNow, in.deadline, &watch)
		if err != nil {
			_ = slot.Release()
			return out, err
		}
		out.decision = decision
		out.bypassed.pressure(in.p, decision)
		if !decision.Admit {
			_ = slot.Release()
			return out, nil
		}
	}

	// Heavy work that holds no slot is invisible to the leases. The scan runs
	// with the slot held, so the job that owned it is never mistaken for
	// uncovered work, and it sees anything that started while we waited. A
	// failed scan or work in this repository (or of unknown ownership) defers;
	// a bypassed uncovered rule names what it found and admits.

	// The scan reads the uncovered settings, so resolve once more right before it.
	if policy, err = in.resolveNow(); err != nil {
		_ = slot.Release()
		return out, err
	}
	watch.Note(policy)
	jobs, scanErr := admitFindUncovered(ctx, in.cwd, in.class == admission.ClassMaintenance, admission.LeasedPIDs(in.store), policy.Uncovered)
	if scanErr == nil && len(jobs) > 0 && policy.Ignore.Uncovered {
		admission.NoteIgnoredUncovered(in.p.Err, jobs, policy.IgnoreSource)
		out.bypassed.rules = admissionIgnoreRules(out.bypassed.rules, admission.RuleUncovered)
		out.bypassed.uncovered(jobs)
		jobs = nil
	}
	if scanErr != nil || len(jobs) > 0 {
		reason := "uncovered heavyweight work: " + strings.Join(jobs, ", ")
		if scanErr != nil {
			reason = "heavy-work inventory unavailable: " + scanErr.Error()
		}
		out.decision = admission.Decision{Admit: false, Reasons: []string{reason}, RetryAfter: 30 * time.Second}
		_ = slot.Release()
		return out, nil
	}

	// Commit point: the overrides every step above acted on must still be the
	// active ones. The gate sampled history, the slot take used a capacity and a
	// bypass, and the scan used settings, each read before the step it fed; an
	// override that ended between a step and this launch must not decide the run.
	// The slot goes back so a restart re-acquires on the fresh limits, and a spent
	// wait ends the run with a rerun reason instead of launching.
	fresh, err := in.resolveNow()
	if err != nil {
		_ = slot.Release()
		return out, err
	}
	if verr := watch.Verify(fresh, admitCommitClock()); verr != nil {
		_ = slot.Release()
		out.decision = admission.Decision{Admit: false, Reasons: []string{verr.Error()}, RetryAfter: time.Second}
		out.restart = in.wait > 0
		return out, nil
	}
	out.slot = slot
	return out, nil
}
