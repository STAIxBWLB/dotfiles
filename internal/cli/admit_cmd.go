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

// resolveAdmissionThresholds layers the user's config and the active relax
// override over the policy defaults. The config path is the real user's state
// file: admission state is per real user, never a --home partition. keepExpired
// is set by read-only views (status) so they never delete state.
func resolveAdmissionThresholds(store *admission.Store, ignoreFlag, keepExpired bool) (admission.ResolvedThresholds, error) {
	return store.ResolveThresholds(admission.ResolveOptions{
		ConfigPath:     config.StatePath(),
		IgnorePressure: ignoreFlag,
		KeepExpired:    keepExpired,
	})
}

// gateWithBusyRetry re-evaluates history-lock contention and the recovery
// countdown inside the wait budget. Neither is current host pressure, and the
// countdown only completes when samples arrive within the 45s gap, so a
// one-shot caller retrying minutes later could never close the episode.
// Pressure defers return immediately.
func gateWithBusyRetry(ctx context.Context, store *admission.Store, monitor *admission.Monitor, resolved admission.ResolvedThresholds, deadline time.Time) (admission.Decision, error) {
	policy := admission.GatePolicy{
		Thresholds:     resolved.Thresholds,
		IgnorePressure: resolved.IgnorePressure,
		IgnoreSource:   resolved.IgnoreSource,
	}
	for {
		d, err := admitGate(ctx, store, monitor, policy)
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
		Use:   "admit [--class heavy|maintenance] [--wait 30m] [--json] [--ignore-pressure] -- <command> [args...]",
		Short: "Run one heavy job per repo behind the host-pressure gate",
		Long: `Admit a heavy job (build, full test run, indexing, bulk copy, dependency
update) through the resource-admission controller. The gate defers while the
host shows memory pressure warning/critical, verifiable thermal pressure, a
WindowServer watchdog termination inside its grace window, or CPU/load
saturation sustained past the policy thresholds; recovery requires five
continuous minutes of normal telemetry, and admit keeps sampling through
that countdown within --wait. Thresholds come from policy defaults, then the
admission.thresholds block of ~/.config/dotfiles/config.yaml, then an active
'dot admit relax' override. One heavy slot is held per project
repo (shared across its worktrees, branches, and sessions); different repos
run in parallel. --class maintenance takes the single host-wide maintenance
slot instead. On defer the exit code is 75 (EX_TEMPFAIL) with a
machine-readable outcome on stdout when --json is set. When a command runs,
its own stdout is the payload and the --json completion record goes to
stderr, so the exit code is the machine-readable result. Selected-tooling
updates share these slots. 'dot peer sync' and 'dot sync push'
hold a background lease while they run, so their rsync never defers other
work. Heavy work launched outside all of these holds no lease; a bounded
process-table scan defers when such work runs in this repo or its repo is
unknown. Use '--' before commands that collide with
subcommand names.

--ignore-pressure (or DOT_ADMISSION_IGNORE_PRESSURE=1) bypasses the pressure
gate for this run only: the snapshot is still taken and recorded, one stderr
line names what was bypassed, and slot exclusivity and the uncovered-work scan
are unchanged.

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
	cmd.Flags().Bool("ignore-pressure", false, "Admit despite host pressure for this run (still samples and records history)")
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
	ignoreFlag, _ := cmd.Flags().GetBool("ignore-pressure")
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
		return runAdmittedChild(cmd, runner, args, effective, class, nil, asJSON, false)
	}

	monitor := admitNewMonitor(runner, home)
	resolved, err := resolveAdmissionThresholds(store, ignoreFlag, false)
	if err != nil {
		return err
	}
	monitor.Thresholds = &resolved.Thresholds
	deadline := time.Now().Add(wait)
	decision, err := gateWithBusyRetry(ctx, store, monitor, resolved, deadline)
	if err != nil {
		return err
	}
	ignored := false
	if decision.PressureIgnored {
		admission.NoteIgnoredPressure(p.Err, decision)
		ignored = true
	}
	if !decision.Admit {
		notifyDefer(ctx, cmd, store, effective, class, decision)
		return deferExit(p, effective, class, "", decision, asJSON)
	}
	lease, err := admission.SelfLease(ctx, runner, cwd)
	if err != nil {
		return err
	}
	backoff := 2 * time.Second
	var slot *admission.Slot
	for {
		got, holder, aerr := store.Acquire(ctx, effective, class, lease)
		if aerr != nil {
			return aerr
		}
		if got != nil {
			slot = got
			break
		}
		if wait == 0 || time.Now().Add(backoff).After(deadline) {
			owner := ""
			reason := "slot busy"
			if holder != nil {
				owner = holder.Owner
				reason = fmt.Sprintf("slot busy: %s class held by %s (pid %d, since %s)",
					class, holder.Owner, holder.PID, holder.AcquiredAt.Format(time.RFC3339))
			}
			d := admission.Decision{Admit: false, Reasons: []string{reason}, RetryAfter: backoff}
			return deferExit(p, effective, class, owner, d, asJSON)
		}
		time.Sleep(backoff)
		backoff = minDuration(backoff*3/2, 15*time.Second)
	}
	defer func() { _ = slot.Release() }()

	// The wait may have been long; re-gate before spending the slot on work
	// that pressure would now defer anyway.
	if wait > 0 {
		decision, err = gateWithBusyRetry(ctx, store, monitor, resolved, deadline)
		if err != nil {
			return err
		}
		if decision.PressureIgnored && !ignored {
			admission.NoteIgnoredPressure(p.Err, decision)
			ignored = true
		}
		if !decision.Admit {
			notifyDefer(ctx, cmd, store, effective, class, decision)
			return deferExit(p, effective, class, "", decision, asJSON)
		}
	}

	// Heavy work that holds no slot is invisible to the leases. The scan runs
	// with the slot held, so the job that owned it is never mistaken for
	// uncovered work, and it sees anything that started while we waited. A
	// failed scan or work in this repository (or of unknown ownership) defers;
	// the deferred Release above frees the slot.
	if jobs, uerr := admitFindUncovered(ctx, cwd, class == admission.ClassMaintenance, admission.LeasedPIDs(store)); uerr != nil || len(jobs) > 0 {
		reason := "uncovered heavyweight work: " + strings.Join(jobs, ", ")
		if uerr != nil {
			reason = "heavy-work inventory unavailable: " + uerr.Error()
		}
		d := admission.Decision{Admit: false, Reasons: []string{reason}, RetryAfter: 30 * time.Second}
		return deferExit(p, effective, class, "", d, asJSON)
	}

	return runAdmittedChild(cmd, runner, args, effective, class, slot, asJSON, ignored)
}
