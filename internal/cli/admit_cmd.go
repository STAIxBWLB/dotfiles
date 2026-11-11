package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// ExitDeferred is EX_TEMPFAIL: the admission gate or the slot wait deferred
// the job, and retrying later is the correct response.
const ExitDeferred = 75

// ExitCodeError lets a command choose its process exit code; cmd/dot/main.go
// honors it via errors.As.
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string { return e.Err.Error() }
func (e *ExitCodeError) Unwrap() error { return e.Err }
func (e *ExitCodeError) ExitCode() int { return e.Code }

// admitNewMonitor builds the pressure monitor the gate probes through. It is
// the fixture seam: the JSON golden fixtures substitute a monitor with a
// fixed clock and deterministic pressure evidence so the goldens stay
// byte-stable, the same role the ForGOOS seams play for the watchdog flows.
var admitNewMonitor = func(runner *exec.Runner, home string) *admission.Monitor {
	return &admission.Monitor{Runner: runner, Home: home}
}

// admitFindUncovered is the seam for the heavy-work scan: tests substitute a
// fixed inventory instead of the real process table.
var admitFindUncovered = admission.FindUncovered

// admitGate is the seam for gate evaluation: tests substitute scripted
// decisions instead of probing the host.
var admitGate = func(ctx context.Context, store *admission.Store, m *admission.Monitor, policy admission.GatePolicy) (admission.Decision, error) {
	return store.GateWithPolicy(ctx, m, policy)
}

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
func gateWithBusyRetry(ctx context.Context, store *admission.Store, monitor *admission.Monitor, policy admission.Policy, deadline time.Time) (admission.Decision, error) {
	gate := admission.GatePolicy{
		Thresholds:   policy.Thresholds,
		Ignore:       policy.Ignore,
		IgnoreSource: policy.IgnoreSource,
	}
	for {
		d, err := admitGate(ctx, store, monitor, gate)
		if err != nil || d.Admit || (!admission.IsGateBusy(d) && !d.Recovering) {
			return d, err
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

func newAdmitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admit [--class heavy|maintenance] [--wait 30m] [--json] [--ignore pressure,slot,uncovered|all] -- <command> [args...]",
		Short: "Run one heavy job per repo behind the host-pressure gate",
		Long: `Admit a heavy job (build, full test run, indexing, bulk copy, dependency
update) through the resource-admission controller. The gate defers while the
host shows memory pressure warning/critical, verifiable thermal pressure, a
WindowServer watchdog termination inside its grace window, or CPU/load
saturation sustained past the policy thresholds; recovery requires five
continuous minutes of normal telemetry, and admit keeps sampling through
that countdown within --wait. Thresholds come from policy defaults, then the
admission block of ~/.config/dotfiles/config.yaml, then an active
'dot admit relax' override. One heavy slot is held per project repo (shared
across its worktrees, branches, and sessions); different repos run in
parallel, and admission.slots can raise that count. --class maintenance takes
the host-wide maintenance slot instead. On defer the exit code is 75
(EX_TEMPFAIL) with a machine-readable outcome on stdout when --json is set.
When a command runs, its own stdout is the payload and the --json completion
record goes to stderr, so the exit code is the machine-readable result.
Selected-tooling updates share these slots. 'dot peer sync' and 'dot sync
push' hold a background lease while they run, so their rsync never defers
other work. Heavy work launched outside all of these holds no lease; a
bounded process-table scan defers when such work runs in this repo or its
repo is unknown. Use '--' before commands that collide with
subcommand names.

--ignore pressure,slot,uncovered (or DOT_ADMISSION_IGNORE=pressure,slot,uncovered,
or 'all') steps past those rules for this run only. Nothing is skipped
silently: a bypassed pressure gate still samples and records history and names
what it bypassed on stderr, a bypassed slot still takes a lease in an overflow
slot past the configured capacity, and a bypassed scan still runs and prints
what it found. Slot scans and leases stay honest; --json adds "ignored".

The child receives CARGO_BUILD_JOBS=2, RUST_TEST_THREADS=2 and GOMAXPROCS=2,
replacing inherited values unless --no-limit-threads is set. Tool-specific
flags (Go -p 2 -parallel 2; browser workers=1) remain the caller's choice.
--project selects both the repository slot and the child's working directory.
--dry-run previews the command without probes or state writes.`,
		Args:         cobra.MinimumNArgs(1),
		RunE:         runAdmit,
		SilenceUsage: true,
	}
	cmd.Flags().String("class", admission.ClassHeavy, "slot class: heavy (per-repo) or maintenance (host-wide)")
	cmd.Flags().Duration("wait", 30*time.Minute, "maximum time to wait for the slot before deferring")
	cmd.Flags().Bool("json", false, "print the outcome as JSON")
	cmd.Flags().String("ignore", "", "rules to step past for this run: pressure, slot, uncovered, or all")
	cmd.Flags().Bool("no-limit-threads", false, "Keep inherited worker settings instead of limiting Cargo, Rust tests and Go to 2")
	cmd.Flags().String("project", "", "Checkout whose slot and working directory to use (defaults to current directory)")
	cmd.AddCommand(newAdmitStatusCmd())
	cmd.AddCommand(newAdmitRelaxCmd())
	return cmd
}

func runAdmit(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	p := printerFrom(cmd)
	class, _ := cmd.Flags().GetString("class")
	wait, _ := cmd.Flags().GetDuration("wait")
	asJSON, _ := cmd.Flags().GetBool("json")
	ignoreSpec, _ := cmd.Flags().GetString("ignore")
	ignoreFlag, err := admission.ParseIgnoreSet(ignoreSpec)
	if err != nil {
		return fmt.Errorf("--ignore %q: %w", ignoreSpec, err)
	}
	if class != admission.ClassHeavy && class != admission.ClassMaintenance {
		return fmt.Errorf("--class must be %q or %q, got %q", admission.ClassHeavy, admission.ClassMaintenance, class)
	}
	if wait < 0 {
		return fmt.Errorf("--wait must not be negative")
	}
	if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
		p.Line("Would acquire repository resource slot and run %q (no probes or writes)", args)
		return nil
	}
	home := homeFor(cmd)
	runner := watchdogRunner(false)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if project, _ := cmd.Flags().GetString("project"); project != "" {
		cwd, err = filepath.Abs(project)
		if err != nil {
			return fmt.Errorf("resolve --project %q: %w; pass an existing checkout directory", project, err)
		}
		info, statErr := os.Stat(cwd)
		if statErr != nil {
			return fmt.Errorf("--project %q cannot be read: %w; pass an existing checkout directory", project, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("--project %q is not a directory; pass the checkout directory", project)
		}
	}
	root, err := admission.UserStateRoot()
	if err != nil {
		return err
	}
	store := admission.NewStore(root, runner)
	scope, err := admission.ResolveScope(ctx, runner, cwd)
	if err != nil {
		return err
	}
	effective := scope
	if class == admission.ClassMaintenance {
		effective = admission.MaintenanceScope
	}

	// A child of an admitted job for the same scope and class runs without
	// re-acquiring the slot its parent already holds.
	if admission.NestedScope(os.Getenv(admission.NestedEnv), effective, class) {
		return runAdmittedChild(cmd, runner, args, effective, class, nil, asJSON, bypass{})
	}

	monitor := admitNewMonitor(runner, home)
	policy, err := resolveAdmissionPolicy(store, ignoreFlag, false)
	if err != nil {
		return err
	}
	monitor.Thresholds = &policy.Thresholds
	var bypassed bypass
	deadline := time.Now().Add(wait)
	decision, err := gateWithBusyRetry(ctx, store, monitor, policy, deadline)
	if err != nil {
		return err
	}
	bypassed.pressure(p, decision)
	if !decision.Admit {
		notifyDefer(ctx, cmd, store, effective, class, decision)
		return deferExit(p, effective, class, "", decision, asJSON, bypassed)
	}
	lease, err := admission.SelfLease(ctx, runner, cwd)
	if err != nil {
		return err
	}
	capacity := policy.Slots.For(class)
	backoff := 2 * time.Second
	var slot *admission.Slot
	for {
		got, holder, aerr := store.AcquireWithOptions(ctx, effective, class, lease, admission.AcquireOptions{
			Capacity: capacity,
			Overflow: policy.Ignore.Slot,
		})
		if aerr != nil {
			return aerr
		}
		if got != nil {
			slot = got
			break
		}
		if wait == 0 || time.Now().Add(backoff).After(deadline) {
			owner := ""
			if holder != nil {
				owner = holder.Owner
			}
			reason := admission.SlotBusyReason(class, holder, capacity, policy.Ignore.Slot)
			d := admission.Decision{Admit: false, Reasons: []string{reason}, RetryAfter: backoff}
			return deferExit(p, effective, class, owner, d, asJSON, bypassed)
		}
		time.Sleep(backoff)
		backoff = minDuration(backoff*3/2, 15*time.Second)
	}
	defer func() { _ = slot.Release() }()
	admission.NoteOverflowSlot(p.Err, slot.Lease(), capacity, policy.IgnoreSource)
	if slot.Lease().Index >= capacity {
		bypassed.rules = admissionIgnoreRules(bypassed.rules, admission.RuleSlot)
	}

	// The wait may have been long; re-gate before spending the slot on work
	// that pressure would now defer anyway.
	if wait > 0 {
		decision, err = gateWithBusyRetry(ctx, store, monitor, policy, deadline)
		if err != nil {
			return err
		}
		bypassed.pressure(p, decision)
		if !decision.Admit {
			notifyDefer(ctx, cmd, store, effective, class, decision)
			return deferExit(p, effective, class, "", decision, asJSON, bypassed)
		}
	}

	// Heavy work that holds no slot is invisible to the leases. The scan runs
	// with the slot held, so the job that owned it is never mistaken for
	// uncovered work, and it sees anything that started while we waited. A
	// failed scan or work in this repository (or of unknown ownership) defers;
	// a bypassed uncovered rule names what it found and admits; the deferred
	// Release above frees the slot.
	jobs, scanErr := admitFindUncovered(ctx, cwd, class == admission.ClassMaintenance, admission.LeasedPIDs(store), policy.Uncovered)
	if scanErr == nil && len(jobs) > 0 && policy.Ignore.Uncovered {
		admission.NoteIgnoredUncovered(p.Err, jobs, policy.IgnoreSource)
		bypassed.rules = admissionIgnoreRules(bypassed.rules, admission.RuleUncovered)
		jobs = nil
	}
	if scanErr != nil || len(jobs) > 0 {
		reason := "uncovered heavyweight work: " + strings.Join(jobs, ", ")
		if scanErr != nil {
			reason = "heavy-work inventory unavailable: " + scanErr.Error()
		}
		d := admission.Decision{Admit: false, Reasons: []string{reason}, RetryAfter: 30 * time.Second}
		return deferExit(p, effective, class, "", d, asJSON, bypassed)
	}

	return runAdmittedChild(cmd, runner, args, effective, class, slot, asJSON, bypassed)
}

// admissionIgnoreRules adds a rule to a bypass record without duplicating it.
func admissionIgnoreRules(rules []string, rule string) []string {
	for _, have := range rules {
		if have == rule {
			return rules
		}
	}
	return append(rules, rule)
}
