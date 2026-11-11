package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newAdmitStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show admission owners, pressure evidence, effective limits and hysteresis state",
		Long: `Report the resource-admission controller's view: active slot owners
(scope, class, owner, pid, cwd, since, one line per held slot index), the
effective value of every admission limit with the layer each came from
(default, config or relax), the rules a one-off bypass or an active relax
override steps past, an active relax override with its expiry, the
host-pressure evidence with per-probe availability, the last WindowServer
watchdog evidence, and the hysteresis countdown when a defer episode is
recovering. The evaluation is read-only: status never advances or resets the
recovery window and never removes an expired override. Jobs launched outside
admission wrappers hold no lease and are caught only by the admit-time process
scan.`,
		Args:         cobra.NoArgs,
		RunE:         runAdmitStatus,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("json", false, "print the diagnostic bundle as JSON")
	return cmd
}

func runAdmitStatus(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()
	p := printerFrom(cmd)
	asJSON, _ := cmd.Flags().GetBool("json")
	home := homeFor(cmd)
	runner := watchdogRunner(false)
	root, err := admission.UserStateRoot()
	if err != nil {
		return err
	}
	store := admission.NewStore(root, runner)
	// Status is a read-only view: an expired relax override is displayed as
	// inactive, never pruned from here, and no bypass is taken from a flag it
	// does not have.
	policy, err := resolveAdmissionPolicy(store, admission.IgnoreSet{}, true)
	if err != nil {
		return err
	}
	monitor := admitNewMonitor(runner, home)
	monitor.Thresholds = &policy.Thresholds
	snap := monitor.SnapshotPressure(ctx)
	// Status samples are read-only, but the timestamp is still taken after
	// probing so slow probes cannot make a stale recovery countdown look fresh.
	now := time.Now()
	if monitor.Now != nil {
		now = monitor.Now()
	}
	hist, err := admission.LoadHistory(store.HistoryPath())
	if err != nil {
		return err
	}
	th := policy.Thresholds
	hist = admission.PrepareHistorySample(hist, th, snap, now)
	// Read-only evaluation: status shows the hysteresis countdown but never
	// advances or resets it — only real gate runs move the streak.
	d := admission.EvaluatePressure(snap, th, hist, now)
	d = admission.ApplyIgnore(d, policy.Ignore, policy.IgnoreSource)
	owners, err := store.ListLeases()
	if err != nil {
		return err
	}
	if asJSON {
		bundle := admission.BuildBundle(snap, d, hist, owners, now)
		data, err := json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			return err
		}
		p.Line("%s", data)
		return nil
	}

	p.Header("dot admit status")
	if policy.Ignore.Any() {
		p.KV("Bypassed rules", policy.Ignore.Label()+" ("+policy.IgnoreSource+")")
		for _, reason := range d.IgnoredReasons {
			p.Bullet(ui.MarkWarn, "pressure would defer: "+reason)
		}
	}
	if d.Admit {
		p.KV("Gate", "admitting")
	} else {
		p.KV("Gate", "deferring")
		for _, reason := range d.Reasons {
			p.Bullet(ui.MarkWarn, reason)
		}
		if d.RetryAfter > 0 {
			p.KV("Retry after", d.RetryAfter.Round(time.Second).String())
		}
	}
	p.Section("Effective limits")
	for _, row := range policy.Rows() {
		p.KV(row.Key, fmt.Sprintf("%s (%s)", row.Value, row.Source))
	}
	if policy.Relax != nil {
		p.KV("Relax override", "active until "+policy.Relax.ExpiresAt.Format(time.RFC3339))
	}
	p.Section("Pressure evidence")
	p.KV("Memory", probeRow(snap.MemoryAvailable, snap.MemoryLevel))
	if snap.Platform == "darwin" {
		p.KV("Thermal", probeRow(snap.ThermalAvailable, admission.ThermalStateName(snap.ThermalState)))
		p.KV("CPU idle", probeRow(snap.IdleAvailable, fmt.Sprintf("%.0f%%", snap.IdlePct)))
		if snap.WSScanOK {
			ws := "none"
			if !snap.WSEvent.IsZero() {
				ws = "last watchdog termination " + snap.WSEvent.Format(time.RFC3339)
			}
			p.KV("WindowServer", ws)
		} else {
			p.KV("WindowServer", "(scan failed)")
		}
	} else {
		p.KV("Thermal", "(macOS only)")
		p.KV("CPU idle", "(macOS only)")
	}
	p.KV("Load", probeRow(snap.LoadAvailable, fmt.Sprintf("load1 %.2f on %d CPUs", snap.Load1, snap.NumCPU)))
	if hist.DeferActive {
		p.Section("Hysteresis")
		p.KV("Defer episode since", hist.DeferSince.Format(time.RFC3339))
		if hist.RecoverSince.IsZero() {
			p.KV("Recovery", "waiting for normal telemetry")
		} else {
			remaining := th.RecoverSustain - now.Sub(hist.RecoverSince)
			if remaining < 0 {
				remaining = 0
			}
			p.KV("Recovery", remaining.Round(time.Second).String()+" of normal telemetry still required")
		}
	}
	p.Section("Owners")
	if len(owners) == 0 {
		p.Line("  none")
	}
	for _, l := range owners {
		line := fmt.Sprintf("  %s [%s] %s pid %d since %s", l.Scope, l.Class, l.Owner, l.PID, l.AcquiredAt.Format(time.RFC3339))
		if l.Index > 0 {
			line += fmt.Sprintf(" slot %d", l.Index)
		}
		if l.Session != "" {
			line += " (" + l.Session + ")" // e.g. an admitted job's purpose
		}
		p.Line("%s", line)
		p.Line("    cwd %s", l.CWD)
	}
	p.Blank()
	p.Line("Note: %s (uncovered scan: %s).", admission.UncoveredNote, policy.Uncovered.Coverage())
	return nil
}

// probeRow renders one probe's evidence, keeping an unavailable probe
// visibly unavailable rather than blank (the no-false-healthy rule applies
// to the display too).
func probeRow(available bool, detail string) string {
	if !available {
		return "(unavailable)"
	}
	return detail
}
