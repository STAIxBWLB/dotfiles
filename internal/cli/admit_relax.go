package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// newAdmitRelaxCmd builds `dot admit relax`: a time-bounded override of the
// pressure gate, stored outside the config so it expires on its own.
func newAdmitRelaxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relax [--for 1h] [--ignore-pressure] [--<threshold> value ...] [--clear]",
		Short: "Set or clear a temporary override of the host-pressure gate",
		Long: `Override the host-pressure gate for a bounded time. With
--ignore-pressure the gate admits regardless of telemetry until the override
expires; with threshold flags the same keys as admission.thresholds in
~/.config/dotfiles/config.yaml are layered over the config until then. Every
evaluation still samples and records pressure history, so recovery stays
continuous after the override ends. An expired override is ignored and
removed by the next gate evaluation. Slot exclusivity and the uncovered-work
scan are never relaxed.

Threshold flags: --idle-defer-below, --idle-recover-at (percent),
--load-defer-frac, --load-recover-frac (fraction of the CPU count),
--defer-sustain, --recover-sustain, --ws-grace (Go durations such as 1m),
--mem-avail-warn-pct, --mem-avail-critical-pct (Linux, percent).

The override is stored in ~/.local/state/dot/admission/override.json.`,
		Args:         cobra.NoArgs,
		RunE:         runAdmitRelax,
		SilenceUsage: true,
	}
	cmd.Flags().Duration("for", admission.RelaxDefaultFor, "how long the override lasts (maximum 24h)")
	cmd.Flags().Bool("ignore-pressure", false, "Admit regardless of host pressure until the override expires")
	cmd.Flags().Bool("clear", false, "Remove the temporary override now")
	cmd.Flags().Float64("idle-defer-below", 0, "CPU idle percent below which work defers")
	cmd.Flags().Float64("idle-recover-at", 0, "CPU idle percent required to recover")
	cmd.Flags().Float64("load-defer-frac", 0, "load1 at this multiple of the CPU count defers")
	cmd.Flags().Float64("load-recover-frac", 0, "load1 below this multiple of the CPU count recovers")
	cmd.Flags().Duration("defer-sustain", 0, "how long a CPU/load excursion must last to defer")
	cmd.Flags().Duration("recover-sustain", 0, "how long normal telemetry must last to recover")
	cmd.Flags().Duration("ws-grace", 0, "WindowServer watchdog termination grace window")
	cmd.Flags().Float64("mem-avail-warn-pct", 0, "Linux MemAvailable percent that warns")
	cmd.Flags().Float64("mem-avail-critical-pct", 0, "Linux MemAvailable percent that is critical")
	return cmd
}

func runAdmitRelax(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	root, err := admission.UserStateRoot()
	if err != nil {
		return err
	}
	store := admission.NewStore(root, watchdogRunner(false))

	if clear, _ := cmd.Flags().GetBool("clear"); clear {
		other := ""
		cmd.Flags().Visit(func(f *pflag.Flag) {
			if f.Name != "clear" && other == "" {
				other = f.Name
			}
		})
		if other != "" {
			return fmt.Errorf("--clear takes no other options (got --%s)", other)
		}
		if err := store.ClearRelaxOverride(); err != nil {
			return err
		}
		p.Line("Temporary admission override cleared.")
		return nil
	}

	ignore, _ := cmd.Flags().GetBool("ignore-pressure")
	overrides := relaxOverridesFromFlags(cmd)
	if !ignore && overrides.IsZero() {
		return fmt.Errorf("nothing to relax: pass --ignore-pressure or at least one threshold flag")
	}
	window, _ := cmd.Flags().GetDuration("for")
	if window <= 0 {
		return fmt.Errorf("--for must be positive, got %s", window)
	}
	if window > admission.RelaxMaxFor {
		return fmt.Errorf("--for %s is longer than the %s maximum", window, admission.RelaxMaxFor)
	}
	configOverrides, err := admission.LoadConfigOverrides(config.StatePath())
	if err != nil {
		return err
	}
	if err := admission.ValidateLayered(configOverrides, overrides); err != nil {
		return fmt.Errorf("refusing to save an override the gate would reject: %w", err)
	}

	now := time.Now()
	override := admission.RelaxOverride{
		SetAt:          now,
		ExpiresAt:      now.Add(window),
		IgnorePressure: ignore,
		Thresholds:     overrides,
	}
	if err := store.SaveRelaxOverride(override); err != nil {
		return err
	}
	p.Line("Temporary admission override set until %s.", override.ExpiresAt.Format(time.RFC3339))
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if f.Name == "for" {
			return
		}
		p.Line("  --%s %s", f.Name, f.Value.String())
	})
	p.Line("Clear it early with 'dot admit relax --clear'; it expires on its own.")
	return nil
}

// relaxOverridesFromFlags collects only the threshold flags the caller set, so
// an unset flag never becomes an override of zero.
func relaxOverridesFromFlags(cmd *cobra.Command) admission.ThresholdOverrides {
	var overrides admission.ThresholdOverrides
	float := func(name string, field **float64) {
		if !cmd.Flags().Changed(name) {
			return
		}
		value, _ := cmd.Flags().GetFloat64(name)
		*field = &value
	}
	duration := func(name string, field **admission.FlexDuration) {
		if !cmd.Flags().Changed(name) {
			return
		}
		value, _ := cmd.Flags().GetDuration(name)
		flex := admission.FlexDuration(value)
		*field = &flex
	}
	float("idle-defer-below", &overrides.IdleDeferBelow)
	float("idle-recover-at", &overrides.IdleRecoverAt)
	float("load-defer-frac", &overrides.LoadDeferFrac)
	float("load-recover-frac", &overrides.LoadRecoverFrac)
	duration("defer-sustain", &overrides.DeferSustain)
	duration("recover-sustain", &overrides.RecoverSustain)
	duration("ws-grace", &overrides.WSGrace)
	float("mem-avail-warn-pct", &overrides.MemAvailWarnPct)
	float("mem-avail-critical-pct", &overrides.MemAvailCriticalPct)
	return overrides
}
