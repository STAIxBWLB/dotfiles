package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

func newSyncConfigureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "configure",
		Short:        "Persist sync profile and scheduler settings",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runSyncConfigure,
	}
	cmd.Flags().String("target", "", "local:path or ssh:user@host:path")
	cmd.Flags().String("owner", "", "self, none, or a machine name")
	cmd.Flags().String("propagate", "", "comma-separated create,update,delete")
	cmd.Flags().Int("max-delete", -1, "maximum deletes per push; 0 uses the default")
	cmd.Flags().String("push-interval", "", "push cadence, or 0 to disable")
	cmd.Flags().String("pull-interval", "", "pull cadence, or 0 to disable")
	cmd.Flags().String("push-mode", "", "clean or force")
	cmd.Flags().String("pull-mode", "", "clean or force")
	cmd.Flags().Bool("json", false, "print the refreshed status document")
	return cmd
}

func runSyncConfigure(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	state, cfg, runner := bs.State, bs.Config, bs.Runner
	if err := syncer.RejectGenericPeerProfile(cfg); err != nil {
		return fmt.Errorf("configure: %w; use `dot peer init` and `dot peer setup`", err)
	}
	if cfg.LocalPaths == nil {
		return fmt.Errorf("sync profile store unresolved")
	}
	local, ok, err := syncer.LoadLocalConfig(cfg.LocalPaths)
	if err != nil {
		return err
	}
	if !ok || local == nil {
		local = &syncer.LocalConfig{Propagation: syncer.DefaultPropagationPolicy()}
	}
	scheduleChanged := false
	if cmd.Flags().Changed("target") {
		raw, _ := cmd.Flags().GetString("target")
		target, err := syncer.ParseTarget(raw)
		if err != nil {
			return err
		}
		if target.Kind == syncer.TargetLocal {
			target.Path = appsettings.ExpandHome(target.Path, homeFor(cmd))
			local.MirrorPath = target.Path
		}
		local.Target = target.String()
	}
	if cmd.Flags().Changed("owner") {
		owner, _ := cmd.Flags().GetString("owner")
		switch strings.TrimSpace(owner) {
		case "self":
			self := syncer.PreferredMachineName()
			if self == "" {
				return fmt.Errorf("cannot determine this machine's name")
			}
			syncer.AssignOwner(local, self)
		case "none":
			syncer.AssignOwner(local, "")
		default:
			syncer.AssignOwner(local, strings.TrimSpace(owner))
		}
	}
	// --filter-mode is inherited from the parent sync command's persistent
	// flags, so it is valid both before and after the configure subcommand.
	if cmd.Flags().Changed("filter-mode") {
		raw, _ := cmd.Flags().GetString("filter-mode")
		mode, err := syncer.ParseFilterMode(raw)
		if err != nil {
			return err
		}
		local.FilterMode = mode
	}
	if cmd.Flags().Changed("propagate") {
		raw, _ := cmd.Flags().GetString("propagate")
		policy, err := parsePropagateFlag(raw)
		if err != nil {
			return err
		}
		local.Propagation = policy
	}
	if cmd.Flags().Changed("max-delete") {
		value, _ := cmd.Flags().GetInt("max-delete")
		if value < 0 {
			return fmt.Errorf("--max-delete must be non-negative")
		}
		local.MaxDelete = value
	}
	if cmd.Flags().Changed("push-interval") {
		raw, _ := cmd.Flags().GetString("push-interval")
		value, err := parseIntervalFlag(raw)
		if err != nil {
			return err
		}
		local.Interval = value
		scheduleChanged = true
	}
	if cmd.Flags().Changed("pull-interval") {
		raw, _ := cmd.Flags().GetString("pull-interval")
		value, err := parseIntervalFlag(raw)
		if err != nil {
			return err
		}
		local.PullInterval = value
		scheduleChanged = true
	}
	if cmd.Flags().Changed("push-mode") {
		raw, _ := cmd.Flags().GetString("push-mode")
		value, err := parseAutomaticModeFlag(raw)
		if err != nil {
			return err
		}
		local.PushMode = value
		scheduleChanged = true
	}
	if cmd.Flags().Changed("pull-mode") {
		raw, _ := cmd.Flags().GetString("pull-mode")
		value, err := parseAutomaticModeFlag(raw)
		if err != nil {
			return err
		}
		local.PullMode = value
		scheduleChanged = true
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if !dryRun {
		if err := syncer.SaveLocalConfig(cfg.LocalPaths, local); err != nil {
			return err
		}
		// The same home Bootstrap resolved with: re-resolving through the
		// process home would install a scheduler for the invoking user after
		// writing the config into the target home's store.
		cfg, err = syncer.ResolveConfigForHomeProfile(state, homeOverrideFrom(cmd), cfg.Profile)
		if err != nil {
			return err
		}
		if scheduleChanged {
			scheduler, _, err := syncer.ResolveScheduler(cfg, runner)
			if err != nil {
				return err
			}
			if err := scheduler.Install(cmd.Context()); err != nil {
				return err
			}
		}
	} else if err := applyLocalConfigPreview(cfg, local, homeFor(cmd)); err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		scheduler, _, err := syncer.ResolveScheduler(cfg, runner)
		if err != nil {
			return err
		}
		status, err := syncer.GetStatus(cmd.Context(), runner, cfg, state, scheduler)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(buildSyncStatusJSON(cfg, status, scheduler))
	}
	if dryRun {
		printerFrom(cmd).Line("dry-run: configuration validated; no files or schedulers changed")
	} else {
		printerFrom(cmd).Success("sync profile %s configured", cfg.Profile)
	}
	return nil
}

// applyLocalConfigPreview projects validated edits onto a resolved config so
// --dry-run --json describes the configuration that would be persisted. It
// intentionally touches no files or schedulers.
func applyLocalConfigPreview(cfg *syncer.Config, local *syncer.LocalConfig, home string) error {
	if cfg == nil || local == nil {
		return fmt.Errorf("sync configuration unresolved")
	}
	if spec := strings.TrimSpace(local.Target); spec != "" {
		target, err := syncer.ParseTarget(spec)
		if err != nil {
			return err
		}
		cfg.MirrorPath = ""
		if target.Kind == syncer.TargetLocal {
			target.Path = appsettings.ExpandHome(target.Path, home)
			if !strings.HasSuffix(target.Path, "/") {
				target.Path += "/"
			}
			cfg.MirrorPath = target.Path
		}
		cfg.Target = target
	}
	cfg.Owner = local.Owner
	cfg.OwnerAliases = local.OwnerAliases
	if local.FilterMode != "" {
		cfg.FilterMode = local.FilterMode
	}
	if err := local.Propagation.Validate(); err != nil {
		return err
	}
	cfg.Propagation = local.Propagation
	cfg.MaxDelete = syncer.EffectiveMaxDelete(local.MaxDelete)
	schedule, err := syncer.ScheduleSettingsFromLocalConfig(local).Normalize()
	if err != nil {
		return err
	}
	cfg.Interval = schedule.Interval
	cfg.PullInterval = schedule.PullInterval
	cfg.PushMode = schedule.PushMode
	cfg.PullMode = schedule.PullMode
	cfg.Paused = local.Paused
	return nil
}
