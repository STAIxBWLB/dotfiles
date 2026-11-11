package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// newAdmitRelaxCmd builds `dot admit relax`: a time-bounded override of the
// admission limits, stored outside the config so it expires on its own.
func newAdmitRelaxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relax --for 1h [--ignore pressure,slot,uncovered,remote-load|all] [--set <key>=<value> ...] | relax --clear",
		Short: "Set or clear a temporary override of the admission limits",
		Long: `Override admission for a bounded time. --ignore steps past the named rules
until the override expires; --set layers the same keys the admission block of
~/.config/dotfiles/config.yaml takes, relative to admission.
Every evaluation still measures what it bypassed, so the limits come back
correctly when the override ends: a bypassed pressure gate still samples and
records history, a bypassed slot still takes a lease (in an overflow slot past
the configured capacity), a bypassed scan still runs and names what it found,
and a bypassed remote-load probe still reads the remote host. An expired
override is ignored and removed by the next evaluation.

--set keys and examples:
  thresholds.recover_sustain=1m      thresholds.idle_defer_below=5
  prebuilt.idle_defer_below=10       prebuilt.safety_window=10s
  slots.heavy_per_repo=2             slots.maintenance=2
  uncovered.enabled=false            uncovered.cpu_percent=0
  uncovered.ignore_names=node,rustc

The override is stored in ~/.local/state/dot/admission/override.json.
--dry-run prints the override that would be written (or cleared) and writes
nothing.`,
		Args:         cobra.NoArgs,
		RunE:         runAdmitRelax,
		SilenceUsage: true,
	}
	cmd.Flags().Duration("for", admission.RelaxDefaultFor, "how long the override lasts (maximum 24h)")
	cmd.Flags().String("ignore", "", "rules to step past until the override expires: pressure, slot, uncovered, remote-load, or all")
	cmd.Flags().StringArray("set", nil, "policy key to layer until the override expires, as <key>=<value> (repeatable)")
	cmd.Flags().Bool("clear", false, "Remove the temporary override now")
	return cmd
}

func runAdmitRelax(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	root, err := admission.UserStateRoot()
	if err != nil {
		return err
	}
	store := admission.NewStore(root, watchdogRunner(false))

	if clear, _ := cmd.Flags().GetBool("clear"); clear {
		other := ""
		cmd.Flags().Visit(func(f *pflag.Flag) {
			// --dry-run is the root's persistent flag, not an option of --clear.
			if f.Name != "clear" && f.Name != "dry-run" && other == "" {
				other = f.Name
			}
		})
		if other != "" {
			return fmt.Errorf("--clear takes no other options (got --%s)", other)
		}
		if dryRun {
			p.Line("dry-run: would clear the temporary admission override (%s).", store.OverridePath())
			return nil
		}
		if err := store.ClearRelaxOverride(); err != nil {
			return err
		}
		p.Line("Temporary admission override cleared.")
		return nil
	}

	ignoreSpec, _ := cmd.Flags().GetString("ignore")
	ignore, err := admission.ParseIgnoreSet(ignoreSpec)
	if err != nil {
		return fmt.Errorf("--ignore %q: %w", ignoreSpec, err)
	}
	overrides, setValues, err := relaxOverridesFromFlags(cmd)
	if err != nil {
		return err
	}
	if !ignore.Any() && overrides.IsZero() {
		return fmt.Errorf("nothing to relax: pass --ignore <rules> or at least one --set <key>=<value>")
	}
	window, _ := cmd.Flags().GetDuration("for")
	if window <= 0 {
		return fmt.Errorf("--for must be positive, got %s", window)
	}
	if window > admission.RelaxMaxFor {
		return fmt.Errorf("--for %s is longer than the %s maximum", window, admission.RelaxMaxFor)
	}
	configOverrides, err := admission.LoadPolicyOverrides(config.StatePath())
	if err != nil {
		return err
	}
	// The caller's own --set values are checked first: a config that is already
	// invalid must not mask the value the person just typed.
	if err := overrides.Validate(); err != nil {
		return fmt.Errorf("refusing to save an override the controller would reject: %w", err)
	}
	if err := admission.ValidateLayered(configOverrides, overrides); err != nil {
		// The layered check is where a config value that is only wrong next to
		// this --set surfaces, so name the file: the person has to edit it.
		return fmt.Errorf("refusing to save an override the controller would reject (config %s plus this --set): %w",
			config.StatePath(), err)
	}

	now := time.Now()
	override := admission.RelaxOverride{
		SetAt:     now,
		ExpiresAt: now.Add(window),
		Ignore:    ignore.Strings(),
		Set:       setValues,
	}
	if dryRun {
		p.Line("dry-run: would set a temporary admission override until %s in %s; nothing written.",
			override.ExpiresAt.Format(time.RFC3339), store.OverridePath())
		if len(override.Ignore) > 0 {
			p.Line("  --ignore %s", ignore.Label())
		}
		for _, key := range sortedKeys(setValues) {
			p.Line("  --set admission.%s=%s", key, setValues[key])
		}
		return nil
	}
	if err := store.SaveRelaxOverride(override); err != nil {
		return err
	}
	p.Line("Temporary admission override set until %s.", override.ExpiresAt.Format(time.RFC3339))
	if len(override.Ignore) > 0 {
		p.Line("  --ignore %s", ignore.Label())
	}
	for _, key := range sortedKeys(setValues) {
		p.Line("  --set admission.%s=%s", key, setValues[key])
	}
	p.Line("Clear it early with 'dot admit relax --clear'; it expires on its own.")
	return nil
}

// relaxOverridesFromFlags parses the --set arguments through the same key table
// the config file uses, so an unknown key or an unparsable value is refused
// here instead of being stored for a later evaluation to trip over. It returns
// both the typed layer and the written key/value pairs the override file keeps.
func relaxOverridesFromFlags(cmd *cobra.Command) (admission.PolicyOverrides, map[string]string, error) {
	var overrides admission.PolicyOverrides
	values := map[string]string{}
	sets, _ := cmd.Flags().GetStringArray("set")
	for _, assignment := range sets {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok {
			return admission.PolicyOverrides{}, nil, fmt.Errorf("--set %q: expected <key>=<value>, such as thresholds.recover_sustain=1m", assignment)
		}
		key = strings.TrimPrefix(strings.TrimSpace(key), "admission.")
		if err := overrides.SetKey(key, value); err != nil {
			return admission.PolicyOverrides{}, nil, fmt.Errorf("--set %s: %w", assignment, err)
		}
		values[key] = strings.TrimSpace(value)
	}
	return overrides, values, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
